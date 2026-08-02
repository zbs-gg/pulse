#!/usr/bin/env node

import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createHash, generateKeyPairSync } from 'node:crypto';
import {
  chmodSync, copyFileSync, existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, realpathSync, rmSync,
  writeFileSync,
} from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { basename, dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { nativePackedFixtureApprovalDigest } from '../src/personal-install-command.js';
import { projectPersonalLiveReadiness } from '../src/personal-live-readiness.js';
import { productBindingRequestHeaders } from '../src/codex-runtime.js';
import { exactTarballPulseInvocation } from '../src/release-attestation.js';
import { resolveWorkspaceBinding } from '../src/workspace-binding.js';
import { loadBundledWindowsAdapter } from '../src/windows-bootstrap-adapter.js';
import { releaseTargetDefinition } from './release-builder-core.mjs';
import { writeProductEdgeFixture } from './product-release-fixture.mjs';
import { buildAndInstallTargetFixture } from './target-release-fixture.mjs';

const cliRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const appRoot = resolve(cliRoot, '..');

function run(command, args, { cwd, env, input, statuses = [0], timeout = 120_000 } = {}) {
  const result = spawnSync(command, args, {
    cwd, env, input, encoding: 'utf8', timeout, stdio: ['pipe', 'pipe', 'pipe'], maxBuffer: 32 * 1024 * 1024,
  });
  if (!statuses.includes(result.status)) {
    throw new Error([
      `${command} ${args.join(' ')} exited ${result.status}`,
      result.stdout,
      result.stderr,
    ].filter(Boolean).join('\n'));
  }
  return result;
}

function terminateCommandTree(child) {
  if (!Number.isSafeInteger(child.pid) || child.pid < 2) return;
  if (process.platform === 'win32') {
    const taskkill = join(process.env.SystemRoot ?? 'C:\\Windows', 'System32', 'taskkill.exe');
    const result = spawnSync(taskkill, ['/PID', String(child.pid), '/T', '/F'], {
      encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 30_000, windowsHide: true,
    });
    if (result.status === 0) return;
    try { child.kill('SIGKILL'); } catch { /* process already exited */ }
    return;
  }
  try { process.kill(-child.pid, 'SIGKILL'); } catch {
    try { child.kill('SIGKILL'); } catch { /* process already exited */ }
  }
}

function runCommandTree(command, args, {
  cwd, env, input, statuses = [0], timeout = 120_000, maxBuffer = 32 * 1024 * 1024,
} = {}) {
  return new Promise((accept, reject) => {
    const child = spawn(command, args, {
      cwd, env, detached: process.platform !== 'win32', windowsHide: true,
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    let stdout = '';
    let stderr = '';
    let outputBytes = 0;
    let timedOut = false;
    let overflow = false;
    let settled = false;
    let timer;
    const finish = (error, value) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      if (error) reject(error);
      else accept(value);
    };
    const capture = (stream) => (chunk) => {
      const text = String(chunk);
      outputBytes += Buffer.byteLength(text);
      if (outputBytes > maxBuffer) {
        overflow = true;
        terminateCommandTree(child);
        return;
      }
      if (stream === 'stdout') stdout += text;
      else stderr += text;
    };
    child.stdout.setEncoding('utf8');
    child.stderr.setEncoding('utf8');
    child.stdout.on('data', capture('stdout'));
    child.stderr.on('data', capture('stderr'));
    child.once('error', (error) => finish(error));
    child.once('close', (status, signal) => {
      if (timedOut || overflow || !statuses.includes(status)) {
        finish(new Error([
          `${command} ${args.join(' ')} ${timedOut ? 'timed out' : overflow ? 'exceeded output limit' : `exited ${status ?? signal}`}`,
          stdout, stderr,
        ].filter(Boolean).join('\n')));
        return;
      }
      finish(null, { status, stdout, stderr });
    });
    timer = setTimeout(() => {
      timedOut = true;
      terminateCommandTree(child);
    }, timeout);
    if (input === undefined) child.stdin.end();
    else child.stdin.end(input);
  });
}

function targetID() {
  const architecture = process.arch === 'arm64' ? 'arm64' : process.arch === 'x64' ? 'x64' : null;
  if (!architecture) throw new Error('native packed fixture target is unsupported');
  if (process.platform === 'darwin') return `darwin-${architecture}`;
  if (process.platform === 'win32') return `win32-${architecture}`;
  if (process.platform === 'linux' && process.report?.getReport()?.header?.glibcVersionRuntime) {
    return `linux-${architecture}-gnu`;
  }
  throw new Error('native packed fixture target is unsupported');
}

async function freePort() {
  const server = createServer();
  await new Promise((accept, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', accept);
  });
  const port = server.address().port;
  await new Promise((accept) => server.close(accept));
  return port;
}

function buildFixtureEmbedder({ outputRoot, runnerName, targetID: selectedTarget }) {
  const target = releaseTargetDefinition(selectedTarget);
  const path = join(outputRoot, 'bin', runnerName);
  rmSync(path, { force: true });
  run('go', ['build', '-trimpath', '-o', path, './cmd/pulse-fixture-embedder'], {
    cwd: appRoot,
    env: { ...process.env, CGO_ENABLED: '0', GOARCH: target.goarch, GOOS: target.goos },
    timeout: 180_000,
  });
  chmodSync(path, 0o700);
}

function npmCLI() {
  const path = process.env.npm_execpath;
  if (!path || !resolve(path) || !existsSync(path)) throw new Error('run native packed proof through npm');
  return path;
}

function pack(root) {
  const supplied = process.env.PULSE_PERSONAL_PACKED_TARBALL;
  if (supplied !== undefined) {
    if (resolve(supplied) !== supplied) {
      throw new Error('PULSE_PERSONAL_PACKED_TARBALL must be an absolute canonical path');
    }
    const canonical = realpathSync(supplied);
    const info = lstatSync(canonical);
    if (canonical !== supplied || !info.isFile() || info.isSymbolicLink() || info.nlink !== 1) {
      throw new Error('PULSE_PERSONAL_PACKED_TARBALL must be one regular, non-linked file');
    }
    return {
      path: canonical,
      bytes: info.size,
      sha256: createHash('sha256').update(readFileSync(canonical)).digest('hex'),
    };
  }
  const output = join(root, 'package');
  mkdirSync(output, { mode: 0o700 });
  const result = run(process.execPath, [npmCLI(), 'pack', '--ignore-scripts', '--pack-destination', output, '--json'], {
    cwd: cliRoot,
  });
  const packed = JSON.parse(result.stdout);
  assert.equal(packed.length, 1);
  const path = resolve(output, packed[0].filename);
  assert.equal(lstatSync(path).isFile(), true);
  return {
    path,
    bytes: lstatSync(path).size,
    sha256: createHash('sha256').update(readFileSync(path)).digest('hex'),
  };
}

function packedPulse(tarball, args, options = {}) {
  return runCommandTree(process.execPath, [npmCLI(), ...exactTarballPulseInvocation(tarball.path, args)], options);
}

function json(stdout, code) {
  try { return JSON.parse(stdout); } catch { throw new Error(code); }
}

function writeTrustKey(root) {
  const trust = join(root, 'trust');
  mkdirSync(trust, { recursive: true, mode: 0o700 });
  const pair = generateKeyPairSync('ec', { namedCurve: 'P-256' });
  const privatePath = join(trust, 'bindings.key');
  const publicPath = join(trust, 'bindings.pub');
  writeFileSync(privatePath, pair.privateKey.export({ type: 'pkcs8', format: 'pem' }), { mode: 0o600 });
  writeFileSync(publicPath, pair.publicKey.export({ type: 'spki', format: 'pem' }), { mode: 0o600 });
  return { privatePath, publicPath };
}

function installedPluginRoot(codexHome) {
  const versionsRoot = join(codexHome, 'plugins', 'cache', 'zbs-gg', 'pulse');
  const versions = readdirSync(versionsRoot);
  assert.equal(versions.length, 1);
  return join(versionsRoot, versions[0]);
}

function nativeCodexExecutable(executable) {
  const resolved = realpathSync(executable);
  if (!resolved.endsWith('.js')) return resolved;
  const optionalPackages = join(resolve(dirname(resolved), '..'), 'node_modules', '@openai');
  if (!existsSync(optionalPackages)) throw new Error('native packed Codex optional package is unavailable');
  const candidates = [];
  const visit = (directory, depth = 0) => {
    if (depth > 6) return;
    for (const name of readdirSync(directory).sort()) {
      const path = join(directory, name);
      const info = lstatSync(path);
      if (info.isSymbolicLink()) continue;
      if (info.isDirectory()) visit(path, depth + 1);
      else if (info.isFile() && name === 'codex' && path.includes(`${join('vendor', '')}`) &&
          path.includes(`${join('bin', '')}`)) candidates.push(path);
    }
  };
  visit(optionalPackages);
  if (candidates.length !== 1) {
    throw new Error(`native packed Codex binary selection is ambiguous: ${candidates.length}`);
  }
  return realpathSync(candidates[0]);
}

function exposeCodexToIsolatedHome(home) {
  const configured = process.env.PULSE_NATIVE_PACKED_CODEX_EXECUTABLE;
  let executable = configured;
  if (!executable) {
    const command = process.platform === 'win32' ? 'where' : '/usr/bin/which';
    const result = run(command, ['codex'], { cwd: cliRoot });
    executable = result.stdout.split(/\r?\n/).find(Boolean);
  }
  if (!executable || !existsSync(executable)) throw new Error('native packed Codex executable is unavailable');
  if (process.platform === 'win32') {
    assert.equal(/\.exe$/i.test(executable), true);
    // npm's global package tree can live behind runner-managed junctions. The
    // product deliberately rejects such ancestry, so calibrate against an
    // owner-only, ordinary file in the fixture HOME instead of weakening the
    // production executable policy.
    const bin = join(home, '.local', 'bin');
    const adapter = loadBundledWindowsAdapter();
    adapter.ensurePrivateDirectory(bin);
    const destination = join(bin, 'codex.exe');
    copyFileSync(realpathSync(executable), destination);
    const proof = adapter.inspectExecutable(destination);
    assert.equal(proof.executable, true);
    return proof.canonical_path;
  }
  const bin = join(home, '.local', 'bin');
  mkdirSync(bin, { recursive: true, mode: 0o700 });
  const destination = join(bin, 'codex');
  // Runner-managed global npm trees may be group-writable. Production host
  // discovery correctly rejects that; the clean-room harness therefore uses
  // an exact private native binary instead of a JS launcher whose optional
  // package would disappear when copied away from its npm tree.
  copyFileSync(nativeCodexExecutable(executable), destination);
  chmodSync(destination, 0o700);
  return realpathSync(destination);
}

function stopFixtureProcesses(root) {
  const visit = (path) => {
    if (!existsSync(path)) return;
    const info = lstatSync(path);
    if (info.isDirectory()) {
      for (const name of readdirSync(path)) visit(join(path, name));
      return;
    }
    if (info.isFile() && path.endsWith('supervisor-runtime.json')) {
      try {
        const receipt = JSON.parse(readFileSync(path, 'utf8'));
        if (Number.isSafeInteger(receipt.pid) && receipt.pid > 1) process.kill(receipt.pid, 'SIGTERM');
      } catch { /* best-effort cleanup after assertions */ }
    }
  };
  visit(root);
}

async function productJSON(runtime, secret, path, { method = 'GET', body } = {}) {
  const response = await fetch(`${runtime.base_url}${path}`, {
    method,
    headers: {
      Accept: 'application/json',
      'X-Pulse-Key': secret,
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(5000),
  });
  if (!response.ok) throw new Error(`native packed product query failed: ${path}: ${response.status}`);
  return response.json();
}

function installedRuntime(registryPath, workspace, { publicKeyPath, anchorPath }) {
  const envelope = json(readFileSync(registryPath, 'utf8'), 'native packed binding registry is invalid');
  const bindings = envelope?.payload?.bindings;
  assert.equal(Array.isArray(bindings), true);
  assert.equal(bindings.length, 1, `isolated native packed fixture must have one binding for ${workspace}`);
  const [storedBinding] = bindings;
  assert.equal(storedBinding.mode, 'personal');
  const binding = resolveWorkspaceBinding({
    cwd: workspace, registryPath, publicKeyPath, anchorPath, rootAnchor: false,
  });
  assert.equal(binding.personal?.base_url.startsWith('http://127.0.0.1:'), true);
  assert.equal(realpathSync(binding.personal.data_dir), binding.personal.data_dir);
  return { binding, runtime: binding.personal };
}

function codexHook(pluginRoot, eventName, input, { cwd, env }) {
  const result = run(process.execPath, [join(pluginRoot, 'hooks', 'pulse-hook.mjs'), eventName], {
    cwd, env, input: JSON.stringify(input), timeout: 30_000,
  });
  return json(result.stdout, `native packed ${eventName} hook output is invalid`);
}

function codexHookInput({ eventName, root, sessionID, turnID, workspace, extra = {} }) {
  return {
    session_id: sessionID,
    ...(turnID ? { turn_id: turnID } : {}),
    transcript_path: join(root, 'must-not-be-read.jsonl'),
    cwd: workspace,
    hook_event_name: eventName,
    model: 'gpt-5',
    permission_mode: 'default',
    ...extra,
  };
}

function startInstalledMCP(pluginRoot, { cwd, env }) {
  const child = spawn(process.execPath, [join(pluginRoot, 'mcp', 'server.mjs')], {
    cwd, env, windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'],
  });
  child.stdout.setEncoding('utf8');
  child.stderr.setEncoding('utf8');
  let stdout = '';
  let stderr = '';
  let nextID = 1;
  let closed = false;
  const pending = new Map();
  const failPending = (error) => {
    for (const { reject, timer } of pending.values()) {
      clearTimeout(timer);
      reject(error);
    }
    pending.clear();
  };
  const exited = new Promise((accept) => {
    child.once('error', (error) => {
      failPending(error);
      accept({ error });
    });
    child.once('close', (status, signal) => {
      const error = status === 0 || closed
        ? undefined
        : new Error(`installed MCP exited ${status ?? signal}: ${stderr.slice(-2000)}`);
      if (error) failPending(error);
      accept({ status, signal, error });
    });
  });
  child.stderr.on('data', (chunk) => {
    stderr = `${stderr}${chunk}`.slice(-8192);
  });
  child.stdout.on('data', (chunk) => {
    stdout += chunk;
    for (let newline = stdout.indexOf('\n'); newline >= 0; newline = stdout.indexOf('\n')) {
      const line = stdout.slice(0, newline).trim();
      stdout = stdout.slice(newline + 1);
      if (!line) continue;
      let message;
      try { message = JSON.parse(line); } catch {
        failPending(new Error('native packed MCP output is invalid'));
        continue;
      }
      const waiter = pending.get(message.id);
      if (!waiter) continue;
      pending.delete(message.id);
      clearTimeout(waiter.timer);
      if (message.error) waiter.reject(new Error(`native packed MCP error ${message.error.code}`));
      else waiter.resolve(message.result);
    }
  });
  const send = (message) => child.stdin.write(`${JSON.stringify(message)}\n`);
  const request = (method, params, timeoutMs = 30_000) => new Promise((resolveRequest, reject) => {
    const id = nextID++;
    const timer = setTimeout(() => {
      pending.delete(id);
      reject(new Error(`native packed MCP ${method} timed out`));
    }, timeoutMs);
    pending.set(id, { reject, resolve: resolveRequest, timer });
    send({ jsonrpc: '2.0', id, method, params });
  });
  const ready = (async () => {
    // The parent intentionally runs synchronous native hook launchers while
    // the host-owned MCP boots. Allow their combined Windows ARM64 wall time
    // before the event loop can consume the already-buffered response.
    const initialized = await request('initialize', {
      protocolVersion: '2024-11-05', capabilities: {},
      clientInfo: { name: 'pulse-native-packed-e2e', version: '1' },
    }, 60_000);
    assert.equal(initialized?.serverInfo?.name, 'pulse-mcp');
    send({ jsonrpc: '2.0', method: 'notifications/initialized', params: {} });
    return request('tools/list', {});
  })();
  return Object.freeze({
    ready,
    async call(toolName, toolArguments) {
      const listed = await ready;
      assert.equal(listed?.tools?.some((tool) => tool.name === toolName), true,
        `installed MCP does not expose ${toolName}`);
      const callResult = await request('tools/call', { name: toolName, arguments: toolArguments });
      assert.equal(Array.isArray(callResult?.content), true);
      assert.notEqual(callResult.isError, true, callResult.content?.[0]?.text);
      return {
        callResult,
        output: json(callResult.content[0].text, `native packed ${toolName} output is invalid`),
      };
    },
    async close() {
      if (closed) return;
      closed = true;
      child.stdin.end();
      let killTimer;
      const outcome = await Promise.race([
        exited,
        new Promise((accept) => {
          killTimer = setTimeout(() => {
            terminateCommandTree(child);
            accept({ error: new Error('installed MCP did not stop') });
          }, 5000);
        }),
      ]);
      clearTimeout(killTimer);
      if (outcome.error) throw outcome.error;
    },
  });
}

function seedSyntheticConsolidationArtifacts(home) {
  const fixtures = [
    ['pulse-release-fixture', 'release.fixture', 'synthetic release artifact\n'],
    ['pulse-backup-fixture', 'backup.fixture', 'synthetic backup artifact\n'],
  ].map(([directory, name, content]) => {
    const root = join(home, directory);
    mkdirSync(root, { recursive: true, mode: 0o700 });
    const path = join(root, name);
    writeFileSync(path, content, { mode: 0o600 });
    return { path };
  });
  return fixtures;
}

function snapshotConsolidationSources(paths) {
  return paths.filter((path) => existsSync(path)).map((path) => {
    const info = lstatSync(path);
    assert.equal(info.isFile(), true, `consolidation source is not a file: ${basename(path)}`);
    return {
      path,
      bytes: readFileSync(path),
      size: info.size,
      mode: info.mode & 0o777,
      modified: info.mtimeMs,
    };
  });
}

function assertConsolidationSourcesPreserved(snapshots) {
  for (const snapshot of snapshots) {
    const info = lstatSync(snapshot.path);
    assert.equal(info.isFile(), true, `consolidation source changed type: ${basename(snapshot.path)}`);
    assert.equal(info.size, snapshot.size, `consolidation source changed size: ${basename(snapshot.path)}`);
    assert.equal(info.mode & 0o777, snapshot.mode, `consolidation source changed mode: ${basename(snapshot.path)}`);
    assert.equal(info.mtimeMs, snapshot.modified, `consolidation source changed mtime: ${basename(snapshot.path)}`);
    assert.deepEqual(readFileSync(snapshot.path), snapshot.bytes,
      `consolidation source changed bytes: ${basename(snapshot.path)}`);
  }
  return true;
}

async function waitForPackedConsolidationReport(tarball, report, options) {
  const deadline = Date.now() + 180_000;
  let current = report;
  while (!['report_ready', 'partial', 'stale', 'canceled'].includes(current.phase)) {
    if (Date.now() >= deadline) throw new Error(`native packed consolidation report timed out in ${current.phase}`);
    await new Promise((accept) => setTimeout(accept, 100));
    const result = await packedPulse(tarball, [
      'consolidate', 'report', 'status', '--id', current.invocation_id, '--json',
    ], { ...options, timeout: 30_000 });
    current = json(result.stdout, 'native packed consolidation status is invalid');
  }
  return current;
}

async function readMemoryHomePage(runtime, secret, binding) {
  const checks = Object.fromEntries([
    'presence_trust', 'authority', 'codex', 'plugin', 'marketplace', 'plugin_mcp',
    'mcp_shadow', 'legacy_hooks', 'native_hook_trust', 'binding', 'runtime',
    'activation', 'vault', 'capture', 'retrieval', 'hooks',
  ].map((name) => [name, { ok: true }]));
  const liveReadiness = projectPersonalLiveReadiness(checks, new Date());
  const sessionResponse = await fetch(`${runtime.base_url}/home/session`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Pulse-Key': secret,
      ...productBindingRequestHeaders({ binding }),
    },
    body: JSON.stringify({ live_readiness: liveReadiness }),
    signal: AbortSignal.timeout(5000),
  });
  assert.equal(sessionResponse.status, 200);
  const session = await sessionResponse.json();
  const pageResponse = await fetch(session.target_url, {
    headers: { Cookie: `${session.cookie_name}=${session.cookie_value}` },
    signal: AbortSignal.timeout(5000),
  });
  assert.equal(pageResponse.status, 200);
  return pageResponse.text();
}

async function assertVisibleHomeMemory({ binding, candidate, runtime, secret }) {
  const checks = Object.fromEntries([
    'presence_trust', 'authority', 'codex', 'plugin', 'marketplace', 'plugin_mcp',
    'mcp_shadow', 'legacy_hooks', 'native_hook_trust', 'binding', 'runtime',
    'activation', 'vault', 'capture', 'retrieval', 'hooks',
  ].map((name) => [name, { ok: true }]));
  checks.hooks = { ok: false, reason: 'lifecycle_receipt_missing' };
  const liveReadiness = projectPersonalLiveReadiness(checks, new Date());
  assert.equal(liveReadiness.reason_code, 'codex_hook_lifecycle_required');
  const sessionResponse = await fetch(`${runtime.base_url}/home/session`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Pulse-Key': secret,
      ...productBindingRequestHeaders({ binding }),
    },
    body: JSON.stringify({ live_readiness: liveReadiness }),
    signal: AbortSignal.timeout(5000),
  });
  assert.equal(sessionResponse.status, 200);
  const session = await sessionResponse.json();
  assert.equal(session.target_url.startsWith(`${runtime.base_url}/home/s/`), true);
  const cookie = `${session.cookie_name}=${session.cookie_value}`;
  const pageResponse = await fetch(session.target_url, {
    headers: { Cookie: cookie }, signal: AbortSignal.timeout(5000),
  });
  assert.equal(pageResponse.status, 200);
  const page = await pageResponse.text();
  assert.equal(page.includes(candidate.candidate.capsule.items[0].redacted_summary), true);
  assert.equal(page.includes(candidate.canonical_object_id), true);
}

async function waitForTerminalCandidate(runtime, secret, candidateID, timeoutMs = 30_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const tray = await productJSON(runtime, secret, '/memory/tray?limit=20');
    const candidate = tray.candidates?.find((value) => value.candidate_id === candidateID);
    if (candidate?.state === 'committed' && candidate.canonical_object_id) return candidate;
    await new Promise((accept) => setTimeout(accept, 250));
  }
  throw new Error('native packed visible Memory Home card did not reach a terminal receipt');
}

async function waitForProjectedCandidate(runtime, secret, candidateID, timeoutMs = 30_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const tray = await productJSON(runtime, secret, '/memory/tray?limit=20');
    const candidate = tray.candidates?.find((value) => value.candidate_id === candidateID);
    if (candidate?.state === 'committed' && candidate.projection_status === 'complete') return candidate;
    if (candidate?.projection_status === 'failed') {
      throw new Error(`native packed projection failed: ${candidateID}`);
    }
    await new Promise((accept) => setTimeout(accept, 250));
  }
  throw new Error('native packed visible Memory Home card did not finish retrieval projection');
}

const selectedTarget = targetID();
const target = releaseTargetDefinition(selectedTarget);
const host = process.env.PULSE_NATIVE_PACKED_HOST ?? 'codex';
if (host !== 'codex') throw new Error('the first native packed proof currently calibrates Codex');
const root = realpathSync(mkdtempSync(join(tmpdir(), 'pulse-native-packed.')));
let keep = process.env.PULSE_KEEP_NATIVE_PACKED_ROOT === '1';
let installedMCP;
try {
  const home = join(root, 'home');
  const codexHome = join(home, '.codex');
  const workspace = join(root, 'workspace');
  const dataDir = join(root, 'data');
  const npmCache = join(root, 'npm-cache');
  for (const path of [home, codexHome, workspace, dataDir, join(dataDir, 'artifacts'), npmCache]) {
    mkdirSync(path, { recursive: true, mode: 0o700 });
  }
  const codexExecutable = exposeCodexToIsolatedHome(home);
  run(process.platform === 'win32' ? 'git' : '/usr/bin/git', ['init', '--quiet'], { cwd: workspace });
  writeFileSync(join(workspace, 'README.md'), '# Native packed Pulse fixture\n', { mode: 0o600 });
  const fixture = await buildAndInstallTargetFixture({
    buildEmbedder: buildFixtureEmbedder,
    buildPluginRuntime: ({ outputRoot }) => writeProductEdgeFixture(outputRoot),
    nativeTargetID: selectedTarget,
    now: new Date(),
    outputRoot: join(root, 'target-release'),
    targetID: selectedTarget,
  });
  const tarball = pack(root);
  const trust = writeTrustKey(root);
  const port = await freePort();
  const baseEnv = {
    ...process.env,
    HOME: home,
    // Node's Windows homedir() follows USERPROFILE rather than HOME. Keep all
    // Windows install state and test authority under the clean-room root
    // without changing how native macOS/Linux harnesses resolve their profile.
    ...(process.platform === 'win32' ? {
      USERPROFILE: home,
      APPDATA: join(home, 'AppData', 'Roaming'),
      LOCALAPPDATA: join(home, 'AppData', 'Local'),
    } : {}),
    CODEX_HOME: codexHome,
    npm_config_cache: npmCache,
    PULSE_DATA_DIR: dataDir,
    PULSE_TRUST_MODE: 'test',
    PULSE_RELEASE_TEST_MODE: '1',
    PULSE_RELEASE_MANIFEST_PATH: fixture.installer.manifest_path,
    PULSE_RELEASE_TEST_ROOT_PATH: fixture.installer.root_key_path,
    PULSE_RELEASE_TEST_ASSET_ROOT: fixture.installer.asset_root,
    PULSE_BINDING_REGISTRY_PATH: join(root, 'trust', 'bindings.json'),
    PULSE_BINDING_PUBLIC_KEY_PATH: trust.publicPath,
    PULSE_BINDING_ANCHOR_PATH: join(root, 'trust', 'bindings.anchor'),
    PULSE_NATIVE_PACKED_FIXTURE_ATTESTATION: '1',
    PULSE_NATIVE_PACKED_FIXTURE_ROOT: root,
    PULSE_NATIVE_PACKED_FIXTURE_PORT: String(port),
    PULSE_NATIVE_PACKED_FIXTURE_BINDING_KEY_PATH: trust.privatePath,
    PULSE_NATIVE_PACKED_CODEX_EXECUTABLE: codexExecutable,
    PULSE_TEST_CODEX_LIFECYCLE_ATTESTOR: '1',
  };
  const planResult = await packedPulse(tarball, ['install-plan', '--json'], {
    cwd: workspace, env: baseEnv, timeout: 180_000,
  });
  const plan = json(planResult.stdout, 'native packed plan is invalid');
  assert.equal(plan.outcome, 'ready_to_install', `${JSON.stringify({
    current_state: plan.current_state, reason_codes: plan.reason_codes, resources: plan.resources,
  })}\n${planResult.stderr}`);
  assert.equal(plan.release.target_id, selectedTarget);
  assert.equal(plan.release.verification_profile.production, false);
  const env = {
    ...baseEnv,
    PULSE_NATIVE_PACKED_FIXTURE_APPROVAL: nativePackedFixtureApprovalDigest(plan),
  };
  const installed = await packedPulse(tarball, ['init', 'codex', '--yes', '--json'], {
    // Windows clean-room prewarm performs native ACL and executable checks.
    // This install-only timeout does not relax the measured 60s first-value gate below.
    cwd: workspace, env, statuses: [0, 1], timeout: process.platform === 'win32' ? 5 * 60_000 : 15 * 60_000,
  });
  const installResult = json(installed.stdout, 'native packed install result is invalid');
  assert.equal(installResult.outcome, 'ready', `${JSON.stringify(installResult)}\n${installed.stderr}`);
  assert.equal(
    installResult.reason_code,
    'installed',
    `${JSON.stringify(installResult)}\n${installed.stderr}`,
  );
  assert.equal(installResult.host_status.hosts[0].installed, true);
  assert.equal(installResult.host_status.hosts[0].lifecycle_ready, false);
  assert.equal(installResult.host_status.hosts[0].verified, false);
  assert.equal(installResult.host_status.hosts[0].reload_required, true);
  const pluginRoot = installedPluginRoot(codexHome);
  assert.equal(basename(pluginRoot), '0.7.1');

  const { binding, runtime } = installedRuntime(baseEnv.PULSE_BINDING_REGISTRY_PATH, workspace, {
    publicKeyPath: baseEnv.PULSE_BINDING_PUBLIC_KEY_PATH,
    anchorPath: baseEnv.PULSE_BINDING_ANCHOR_PATH,
  });
  const secret = readFileSync(join(runtime.data_dir, 'secret.key'), 'utf8').trim();
  assert.match(secret, /^[a-f0-9]{64}$/);
  const initialStatus = await productJSON(runtime, secret, '/memory/status');
  assert.equal(initialStatus.full_retrieval, true);
  assert.equal(initialStatus.raw_capture_enabled, false);
  assert.equal(initialStatus.backend_llm_enabled, false);
  // R25 starts at a genuinely ready product, not while npm and the native
  // runtime are still being installed.
  const firstValueStartedAt = Date.now();
  let previousFirstValueStageAt = firstValueStartedAt;
  const firstValueStages = [];
  const markFirstValueStage = (name) => {
    const now = Date.now();
    firstValueStages.push([name, now - previousFirstValueStageAt]);
    previousFirstValueStageAt = now;
  };

  const freshHostEnv = Object.fromEntries(
    Object.entries(env).filter(([name]) => !name.startsWith('PULSE_') ||
      ['PULSE_TRUST_MODE', 'PULSE_TEST_CODEX_LIFECYCLE_ATTESTOR'].includes(name)),
  );
  const hookEnv = { ...freshHostEnv, PLUGIN_DATA: join(root, 'plugin-data') };
  // A real host starts one stdio MCP server for the session and keeps it alive.
  // Start it alongside SessionStart so MCP boot overlaps host startup instead
  // of charging a synthetic one-shot Node launch to the first tool call.
  installedMCP = startInstalledMCP(pluginRoot, { cwd: workspace, env: hookEnv });
  const firstSessionID = 'session-native-packed-first';
  const firstTurnID = 'turn-native-packed-first';
  const firstSession = codexHook(pluginRoot, 'SessionStart', codexHookInput({
    eventName: 'SessionStart', root, sessionID: firstSessionID, workspace,
    extra: { source: 'startup' },
  }), { cwd: workspace, env: hookEnv });
  assert.equal(firstSession.continue, true);
  assert.match(
    firstSession.hookSpecificOutput?.additionalContext,
    /pulse\.context\.v1/,
    `native packed SessionStart degraded: ${JSON.stringify(firstSession)}`,
  );
  markFirstValueStage('session_start');

  const firstPrompt = codexHook(pluginRoot, 'UserPromptSubmit', codexHookInput({
    eventName: 'UserPromptSubmit', root, sessionID: firstSessionID, turnID: firstTurnID, workspace,
    extra: { prompt: 'Do not store this raw prompt.' },
  }), { cwd: workspace, env: hookEnv });
  assert.equal(firstPrompt.continue, true);
  assert.match(firstPrompt.hookSpecificOutput?.additionalContext, /before the single final user-facing response/);
  assert.match(firstPrompt.hookSpecificOutput?.additionalContext, /ASCII safe slug/);
  markFirstValueStage('prompt_submit');

  const summary = 'Use one trusted local runtime for native packed Codex lifecycle memory.';
  const memoryArguments = {
    schema: 'pulse.memory_capsule.v1',
    source: { host: 'codex', conversation_scope: 'current_turn', timestamp: new Date().toISOString() },
    items: [{
      kind: 'decision', redacted_summary: summary, confidence: 0.98,
      evidence_hint: 'current_turn', privacy_tier: 'normal', retention: 'project',
    }],
    raw_input_included: false,
  };
  const preTool = codexHook(pluginRoot, 'PreToolUse', codexHookInput({
    eventName: 'PreToolUse', root, sessionID: firstSessionID, turnID: firstTurnID, workspace,
    extra: {
      tool_name: 'mcp__pulse-product__pulse_remember', tool_input: memoryArguments,
      tool_use_id: 'tool-native-packed-remember',
    },
  }), { cwd: workspace, env: hookEnv });
  assert.deepEqual(preTool, {});
  markFirstValueStage('pre_tool');
  const { callResult, output: remembered } = await installedMCP.call('pulse_remember', memoryArguments);
  assert.equal(remembered.status, 'candidates');
  assert.equal(remembered.receipts.length, 1);
  assert.equal(remembered.receipts[0].status, 'created');
  assert.match(remembered.receipts[0].object_id, /^pulse:/);
  markFirstValueStage('remember_mcp');

  const postTool = codexHook(pluginRoot, 'PostToolUse', codexHookInput({
    eventName: 'PostToolUse', root, sessionID: firstSessionID, turnID: firstTurnID, workspace,
    extra: {
      tool_name: 'mcp__pulse-product__pulse_remember', tool_input: memoryArguments,
      tool_use_id: 'tool-native-packed-remember', tool_response: callResult,
    },
  }), { cwd: workspace, env: hookEnv });
  assert.deepEqual(postTool, {});
  markFirstValueStage('post_tool');
  const firstStop = codexHook(pluginRoot, 'Stop', codexHookInput({
    eventName: 'Stop', root, sessionID: firstSessionID, turnID: firstTurnID, workspace,
    extra: { stop_hook_active: false, last_assistant_message: 'Do not store this raw message.' },
  }), { cwd: workspace, env: hookEnv });
  assert.deepEqual(firstStop, {});
  markFirstValueStage('stop_finalize');

  const committedTray = await productJSON(runtime, secret, '/memory/tray?limit=20');
  const committedCard = committedTray.candidates.find((candidate) =>
    candidate.candidate_id === remembered.receipts[0].candidate_id);
  assert.equal(committedCard.state, 'committed');
  assert.equal(committedCard.current, true);
  assert.equal(committedCard.canonical_object_id, remembered.receipts[0].object_id);
  assert.equal(committedCard.latest_receipt.status, 'created');
  assert.equal(committedCard.projection_status, 'complete');
  assert.equal(committedCard.candidate.capsule.items[0].redacted_summary, summary);
  markFirstValueStage('committed_card');
  await assertVisibleHomeMemory({ binding, candidate: committedCard, runtime, secret });
  markFirstValueStage('visible_card');
  await packedPulse(tarball, ['home', '--host', 'codex'], {
    cwd: workspace, env: {
      ...env, PULSE_OPEN_DRY_RUN: '1', PULSE_HOME_ACCEPTANCE_STAGES: '1',
    }, timeout: 30_000,
  });
  markFirstValueStage('home_command');
  const terminalCard = await waitForTerminalCandidate(runtime, secret, committedCard.candidate_id);
  assert.equal(['created', 'updated', 'deduplicated'].includes(terminalCard.latest_receipt.status), true);
  assert.equal(terminalCard.latest_receipt.object_id, terminalCard.canonical_object_id);
  assert.equal(terminalCard.latest_receipt.safe_provenance.host, 'codex');
  assert.match(terminalCard.latest_receipt.receipt_id, /^receipt_/);
  const objectID = terminalCard.canonical_object_id;
  markFirstValueStage('terminal_receipt');
  const duplicateSessionID = 'session-native-packed-duplicate';
  const duplicateTurnID = 'turn-native-packed-duplicate';
  const duplicateArguments = {
    ...memoryArguments,
    source: { ...memoryArguments.source, timestamp: new Date().toISOString() },
  };
  codexHook(pluginRoot, 'SessionStart', codexHookInput({
    eventName: 'SessionStart', root, sessionID: duplicateSessionID, workspace,
    extra: { source: 'startup' },
  }), { cwd: workspace, env: hookEnv });
  codexHook(pluginRoot, 'UserPromptSubmit', codexHookInput({
    eventName: 'UserPromptSubmit', root, sessionID: duplicateSessionID,
    turnID: duplicateTurnID, workspace, extra: { prompt: 'Save the same decision once.' },
  }), { cwd: workspace, env: hookEnv });
  const duplicateToolID = 'tool-native-packed-duplicate';
  assert.deepEqual(codexHook(pluginRoot, 'PreToolUse', codexHookInput({
    eventName: 'PreToolUse', root, sessionID: duplicateSessionID,
    turnID: duplicateTurnID, workspace,
    extra: {
      tool_name: 'mcp__pulse-product__pulse_remember', tool_input: duplicateArguments,
      tool_use_id: duplicateToolID,
    },
  }), { cwd: workspace, env: hookEnv }), {});
  const duplicateCall = await installedMCP.call('pulse_remember', duplicateArguments);
  const duplicate = duplicateCall.output;
  assert.equal(duplicate.receipts.length, 1);
  assert.equal(duplicate.receipts[0].status, 'deduplicated');
  assert.equal(duplicate.receipts[0].object_id, objectID);
  assert.deepEqual(codexHook(pluginRoot, 'PostToolUse', codexHookInput({
    eventName: 'PostToolUse', root, sessionID: duplicateSessionID,
    turnID: duplicateTurnID, workspace,
    extra: {
      tool_name: 'mcp__pulse-product__pulse_remember', tool_input: duplicateArguments,
      tool_use_id: duplicateToolID, tool_response: duplicateCall.callResult,
    },
  }), { cwd: workspace, env: hookEnv }), {});
  assert.deepEqual(codexHook(pluginRoot, 'Stop', codexHookInput({
    eventName: 'Stop', root, sessionID: duplicateSessionID, turnID: duplicateTurnID, workspace,
    extra: { stop_hook_active: false, last_assistant_message: 'Saved once.' },
  }), { cwd: workspace, env: hookEnv }), {});
  markFirstValueStage('deduplicated_receipt');
  await waitForProjectedCandidate(runtime, secret, committedCard.candidate_id);
  markFirstValueStage('retrieval_projection');
  const freshSessionID = 'session-native-packed-fresh';
  const freshTurnID = 'turn-native-packed-fresh';
  const freshSession = codexHook(pluginRoot, 'SessionStart', codexHookInput({
    eventName: 'SessionStart', root, sessionID: freshSessionID, workspace,
    extra: { source: 'resume' },
  }), { cwd: workspace, env: hookEnv });
  assert.equal(freshSession.continue, true);
  assert.match(freshSession.hookSpecificOutput.additionalContext, /pulse\.context\.v1/);
  assert.equal(freshSession.hookSpecificOutput.additionalContext.includes(summary), true);
  markFirstValueStage('fresh_session');

  // Ready-to-recall is reached when a fresh host session receives the exact
  // saved memory. Prompt-context lifecycle calibration remains mandatory below,
  // but it happens after the memory has already been delivered to the host.
  const firstValueMs = Date.now() - firstValueStartedAt;
  // Windows process startup and the one-shot Home browser handoff are
  // consistently slower on a clean hosted machine. Keep the ordinary limit
  // strict while giving the same complete Windows acceptance one bounded
  // minute and a half instead of treating normal process startup as a hang.
  const firstValueLimitMs = process.platform === 'win32' ? 90_000 : 60_000;
  assert.equal(firstValueMs <= firstValueLimitMs, true,
    `native packed ready-to-recall took ${firstValueMs}ms; stages=${JSON.stringify(Object.fromEntries(firstValueStages))}`);

  const freshPrompt = codexHook(pluginRoot, 'UserPromptSubmit', codexHookInput({
    eventName: 'UserPromptSubmit', root, sessionID: freshSessionID, turnID: freshTurnID, workspace,
    extra: { prompt: 'Continue from the saved project decision.' },
  }), { cwd: workspace, env: hookEnv });
  assert.equal(freshPrompt.continue, true);

  let lifecycle = await productJSON(runtime, secret, '/memory/lifecycle-readiness');
  let codexLifecycle = lifecycle.hosts.find((value) => value.host === 'codex');
  const observationStartedAt = Date.now();
  const observationTrace = [];
  // Delivery observation is intentionally bounded and non-blocking. A slow
  // local platform may leave the prompt offer pending, so exercise the same
  // trusted Stop retry that closes the proof during ordinary host use. Space
  // retries out: firing three new hook processes 250ms apart is less realistic
  // than an ordinary prompt/turn boundary and can repeatedly hit the same
  // transient Windows ARM64 daemon backlog.
  for (let attempt = 0;
    codexLifecycle?.state === 'host_observation_pending' && attempt < 5;
    attempt += 1) {
    await new Promise((accept) => setTimeout(accept, [250, 500, 1_000, 1_500, 2_000][attempt]));
    const observationRetry = codexHook(pluginRoot, 'Stop', codexHookInput({
      eventName: 'Stop', root, sessionID: freshSessionID, turnID: freshTurnID, workspace,
      extra: { stop_hook_active: false, last_assistant_message: 'Continue from the saved decision.' },
    }), { cwd: workspace, env: hookEnv });
    assert.deepEqual(observationRetry, {});
    lifecycle = await productJSON(runtime, secret, '/memory/lifecycle-readiness');
    codexLifecycle = lifecycle.hosts.find((value) => value.host === 'codex');
    observationTrace.push({
      attempt: attempt + 1,
      elapsed_ms: Date.now() - observationStartedAt,
      state: codexLifecycle?.state ?? 'missing',
      lifecycle_ready: codexLifecycle?.lifecycle_ready === true,
      milestone_count: Array.isArray(codexLifecycle?.milestones) ? codexLifecycle.milestones.length : 0,
    });
  }
  assert.equal(codexLifecycle.lifecycle_ready, true, JSON.stringify({
    lifecycle: codexLifecycle,
    observation_trace: observationTrace,
  }));
  assert.equal(codexLifecycle.state, 'ready');
  assert.equal(codexLifecycle.object_id, objectID);
  assert.deepEqual(codexLifecycle.milestones, ['write_receipt', 'session_context', 'prompt_context']);

  const recalled = await productJSON(runtime, secret, '/memory/recall', {
    method: 'POST',
    body: { query: summary, scope: 'project', limit: 10, privacy_ceiling: 'private' },
  });
  assert.equal(recalled.items.some((item) => item.id === objectID && item.summary === summary), true);
  const viewer = await productJSON(runtime, secret,
    '/viewer/data?thread_id=native-packed-e2e&host=codex&token_budget=900');
  const economy = viewer?.next_resume?.token_economy;
  assert.equal(viewer?.next_resume?.included_object_ids?.includes(objectID), true);
  assert.equal(['collecting_baseline', 'estimated', 'measured'].includes(economy?.state), true);
  assert.equal(typeof economy?.method_id, 'string');
  assert.equal(economy.method_id.length > 0, true);
  assert.equal(Number.isInteger(economy.pulse_tokens) && economy.pulse_tokens > 0, true);
  assert.equal('savings_percentage' in economy, false);

  await installedMCP.close();
  installedMCP = undefined;
  const stopped = json((await packedPulse(tarball, ['supervisor', 'stop', '--json'], {
    cwd: workspace, env, timeout: 30_000,
  })).stdout, 'native packed supervisor stop result is invalid');
  assert.equal(stopped.status, 'stopped');
  const unavailableSession = codexHook(pluginRoot, 'SessionStart', codexHookInput({
    eventName: 'SessionStart', root, sessionID: 'session-native-packed-unavailable', workspace,
    extra: { source: 'startup' },
  }), { cwd: workspace, env: hookEnv });
  assert.equal(unavailableSession.continue === false, false);
  const unavailableGoal = codexHook(pluginRoot, 'PreToolUse', codexHookInput({
    eventName: 'PreToolUse', root, sessionID: 'session-native-packed-unavailable',
    turnID: 'turn-native-packed-unavailable', workspace,
    extra: { tool_name: 'update_plan', tool_input: { plan: [] }, tool_use_id: 'tool-goal-control' },
  }), { cwd: workspace, env: hookEnv });
  assert.deepEqual(unavailableGoal, {});
  const failOpenFile = join(workspace, 'pulse-fail-open-proof.txt');
  run(process.execPath, ['--input-type=module', '--eval',
    `await import('node:fs').then(({writeFileSync})=>writeFileSync(${JSON.stringify(failOpenFile)},'terminal and files stayed available\\n',{mode:0o600}))`,
  ], { cwd: workspace, env: hookEnv });
  assert.equal(readFileSync(failOpenFile, 'utf8'), 'terminal and files stayed available\n');
  const unavailableStop = codexHook(pluginRoot, 'Stop', codexHookInput({
    eventName: 'Stop', root, sessionID: 'session-native-packed-unavailable',
    turnID: 'turn-native-packed-unavailable', workspace,
    extra: { stop_hook_active: false, last_assistant_message: 'One response only.' },
  }), { cwd: workspace, env: hookEnv });
  assert.deepEqual(unavailableStop, {});
  const restarted = json((await packedPulse(tarball, ['supervisor', 'start', '--json'], {
    cwd: workspace, env, timeout: 60_000,
  })).stdout, 'native packed supervisor restart result is invalid');
  assert.equal(restarted.status, 'running');
  installedMCP = startInstalledMCP(pluginRoot, { cwd: workspace, env: hookEnv });
  const afterRestart = (await installedMCP.call('pulse_recall', {
    query: summary, scope: 'project', limit: 10, privacy_ceiling: 'private',
  })).output;
  assert.equal(afterRestart.items.some((item) => item.id === objectID && item.summary === summary), true);
  await packedPulse(tarball, ['home', '--host', 'codex'], {
    cwd: workspace, env: {
      ...env, PULSE_OPEN_DRY_RUN: '1', PULSE_HOME_ACCEPTANCE_STAGES: '1',
    }, timeout: 30_000,
  });
  markFirstValueStage('fail_open_and_restart');

  const sourceFixtures = seedSyntheticConsolidationArtifacts(home);
  const canonicalDatabase = join(runtime.data_dir, 'pulse.db');
  // WAL/SHM are live SQLite coordination files: ordinary read transactions
  // can change them while leaving every durable memory byte untouched. Prove
  // byte preservation on the canonical database and the external sources.
  const sourceSnapshots = snapshotConsolidationSources([
    canonicalDatabase, ...sourceFixtures.map((fixtureSource) => fixtureSource.path),
  ]);
  assert.equal(sourceSnapshots.some((snapshot) => snapshot.path === canonicalDatabase), true,
    'native packed consolidation proof did not snapshot the canonical database');
  const consolidationStartResult = await packedPulse(tarball, [
    'consolidate', 'report', 'start', '--json',
  ], { cwd: workspace, env, timeout: 180_000 });
  const consolidationStarted = json(
    consolidationStartResult.stdout,
    'native packed consolidation report is invalid',
  );
  const consolidation = await waitForPackedConsolidationReport(
    tarball, consolidationStarted, { cwd: workspace, env },
  );
  assert.equal(consolidation.schema, 'pulse.consolidation.report.v1');
  assert.equal(consolidation.phase, 'report_ready', JSON.stringify(consolidation));
  assert.equal(consolidation.destination.store_id, runtime.store_id);
  assert.equal(consolidation.destination.repository_id.length > 0, true);
  assert.equal(consolidation.totals.excluded, 2);
  for (const classification of ['canonical_vault', 'release_artifact', 'backup']) {
    assert.equal(consolidation.sources.some((source) => source.classification === classification), true,
      `missing ${classification} from packed consolidation report`);
  }
  const sourcesBytePreserved = assertConsolidationSourcesPreserved(sourceSnapshots);
  const consolidationStatusResult = await packedPulse(tarball, [
    'consolidate', 'report', 'status', '--json',
  ], { cwd: workspace, env, timeout: 30_000 });
  const consolidationStatus = json(
    consolidationStatusResult.stdout,
    'native packed consolidation status is invalid',
  );
  assert.equal(consolidationStatus.report_digest, consolidation.report_digest);
  assert.deepEqual(consolidationStatus.totals, consolidation.totals);

  const mcpConsolidation = (await installedMCP.call(
    'pulse_consolidation_report',
    { action: 'status', report_id: consolidation.invocation_id },
  )).output;
  assert.equal(mcpConsolidation.report_digest, consolidation.report_digest);
  assert.deepEqual(mcpConsolidation.totals, consolidation.totals);

  const consolidationHome = await readMemoryHomePage(runtime, secret, binding);
  for (const text of [
    'Memory ocean', 'What Pulse found on this computer', 'Where memory for this project is written',
    'Which sources were inspected', 'canonical_vault_01', 'release_artifact_01', 'backup_01',
  ]) assert.equal(consolidationHome.includes(text), true, `Memory Home missing ${text}`);
  assert.equal(consolidationHome.includes(sourceFixtures[0].path), false);
  assert.equal(consolidationHome.includes(sourceFixtures[1].path), false);
  await installedMCP.close();
  installedMCP = undefined;

  const consolidationReceipt = {
    schema: 'pulse.personal_consolidation_report_fixture.v1',
    content_free: true,
    target_id: selectedTarget,
    package_sha256: tarball.sha256,
    report_digest: consolidation.report_digest,
    inventory_digest: consolidation.inventory_digest,
    phase: consolidation.phase,
    source_classifications: consolidation.sources.map((source) => source.classification).sort(),
    totals: consolidation.totals,
    cli_parity: true,
    mcp_parity: true,
    memory_home_visible: true,
    sources_byte_preserved: sourcesBytePreserved,
    imported: false,
    merged: false,
    deleted: false,
    published: false,
  };

  const repairPlanResult = await packedPulse(tarball, ['install-plan', '--json'], {
    cwd: workspace, env: baseEnv, timeout: 180_000,
  });
  const repairPlan = json(repairPlanResult.stdout, 'native packed repair plan is invalid');
  assert.equal(repairPlan.outcome, 'ready_to_install', JSON.stringify(repairPlan));
  const repairEnv = {
    ...baseEnv,
    PULSE_NATIVE_PACKED_FIXTURE_APPROVAL: nativePackedFixtureApprovalDigest(repairPlan),
  };
  const repaired = await packedPulse(tarball, ['repair', '--json'], {
    cwd: workspace, env: repairEnv, statuses: [0, 1], timeout: process.platform === 'win32' ? 180_000 : 15 * 60_000,
  });
  const repairResult = json(repaired.stdout, 'native packed repair result is invalid');
  assert.equal(repairResult.outcome, 'ready', `${JSON.stringify(repairResult)}\n${repaired.stderr}`);
  assert.equal(repairResult.host_status.hosts[0].host, 'codex');
  assert.equal(repairResult.host_status.hosts[0].lifecycle_ready, true);
  assert.equal(repairResult.host_status.hosts[0].verified, true);
  assert.equal(repairResult.host_status.hosts[0].reload_required, false);
  const receipt = {
    schema: 'pulse.native_packed_product_fixture.v1',
    target_id: selectedTarget,
    platform: target.platform,
    architecture: target.architecture,
    host,
    packed_tarball_sha256: tarball.sha256,
    packed_tarball_bytes: tarball.bytes,
    release_manifest_digest: plan.release.manifest_digest,
    release_artifact_ids: plan.release.artifacts.map((artifact) => artifact.id).sort(),
    exact_public_install_command: true,
    native_daemon: true,
    native_fixture_embedder: true,
    store_id: runtime.store_id,
    static_host_attached: true,
    visible_memory_card: true,
    first_memory_saved: true,
    duplicate_receipt: 'deduplicated',
    same_object_after_duplicate: true,
    home_command_one_shot: true,
    fail_open_when_daemon_stopped: true,
    terminal_and_files_available: true,
    stop_available: true,
    goal_control_available: true,
    automatic_continuation: false,
    memory_survived_restart: true,
    automatic_durable_write: true,
    tray_save_proof: false,
    canonical_object_id: objectID,
    fresh_session_context: true,
    host_observation: true,
    lifecycle_ready: true,
    repair_ready: true,
    same_object_recalled: true,
    first_value_boundary: 'fresh_session_context',
    first_value_ms: firstValueMs,
    first_value_stages_ms: Object.fromEntries(firstValueStages),
    token_economy: {
      state: economy.state,
      method_id: economy.method_id,
      method_version: economy.method_version,
      pulse_tokens: economy.pulse_tokens,
    },
    production_ready: false,
    support_proven: false,
  };
  process.stdout.write(`${JSON.stringify(consolidationReceipt)}\n`);
  process.stdout.write(`${JSON.stringify(receipt)}\n`);
  process.stdout.write('Pulse native packed install saved one visible card and recalled it in a fresh Codex session.\n');
} catch (error) {
  keep = true;
  process.stderr.write(`Native packed fixture root preserved at ${root}\n`);
  throw error;
} finally {
  try { await installedMCP?.close(); } catch { /* cleanup must preserve the primary assertion */ }
  stopFixtureProcesses(root);
  if (!keep) rmSync(root, { recursive: true, force: true });
}
