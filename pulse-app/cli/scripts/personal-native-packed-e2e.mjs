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
import { exactTarballPulseInvocation } from '../src/release-attestation.js';
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
  // an exact private copy instead of weakening the executable policy.
  copyFileSync(realpathSync(executable), destination);
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

function installedRuntime(registryPath, workspace) {
  const envelope = json(readFileSync(registryPath, 'utf8'), 'native packed binding registry is invalid');
  const bindings = envelope?.payload?.bindings;
  assert.equal(Array.isArray(bindings), true);
  assert.equal(bindings.length, 1, `isolated native packed fixture must have one binding for ${workspace}`);
  const [binding] = bindings;
  assert.equal(binding.mode, 'personal');
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

function rememberThroughInstalledMCP(pluginRoot, memoryArguments, { cwd, env }) {
  const input = [
    { jsonrpc: '2.0', id: 1, method: 'initialize', params: {
      protocolVersion: '2024-11-05', capabilities: {},
      clientInfo: { name: 'pulse-native-packed-e2e', version: '1' },
    } },
    { jsonrpc: '2.0', method: 'notifications/initialized', params: {} },
    { jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} },
    { jsonrpc: '2.0', id: 3, method: 'tools/call', params: {
      name: 'pulse_remember', arguments: memoryArguments,
    } },
  ].map((message) => JSON.stringify(message)).join('\n') + '\n';
  const result = run(process.execPath, [join(pluginRoot, 'mcp', 'server.mjs')], {
    cwd, env, input, timeout: 30_000,
  });
  const messages = result.stdout.trim().split(/\r?\n/).filter(Boolean).map((line) =>
    json(line, 'native packed MCP output is invalid'));
  assert.equal(messages.find((message) => message.id === 1)?.result?.serverInfo?.name, 'pulse-mcp');
  assert.equal(messages.find((message) => message.id === 2)?.result?.tools?.some((tool) =>
    tool.name === 'pulse_remember'), true);
  const callResult = messages.find((message) => message.id === 3)?.result;
  assert.equal(Array.isArray(callResult?.content), true);
  return { callResult, remembered: json(callResult.content[0].text, 'native packed remember output is invalid') };
}

async function openVisibleHomeCard({ candidate, runtime, secret }) {
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
    headers: { 'Content-Type': 'application/json', 'X-Pulse-Key': secret },
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
  assert.equal(page.includes(`data-candidate-id="${candidate.candidate_id}"`), true);
  assert.equal(page.includes(candidate.candidate.capsule.items[0].redacted_summary), true);
  const csrf = page.match(/name="csrf_token" value="([^"]+)"/)?.[1];
  assert.equal(typeof csrf, 'string');
  const form = new URLSearchParams({
    csrf_token: csrf,
    candidate_id: candidate.candidate_id,
    expected_version: String(candidate.version),
  });
  const presentResponse = await fetch(new URL('present', session.target_url), {
    method: 'POST',
    headers: {
      Cookie: cookie,
      Origin: runtime.base_url,
      'Sec-Fetch-Site': 'same-origin',
      'Sec-Fetch-Mode': 'cors',
      'Sec-Fetch-Dest': 'empty',
      'Content-Type': 'application/x-www-form-urlencoded',
    },
    body: form,
    signal: AbortSignal.timeout(5000),
  });
  assert.equal(presentResponse.status, 204, await presentResponse.text());
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

const selectedTarget = targetID();
const target = releaseTargetDefinition(selectedTarget);
const host = process.env.PULSE_NATIVE_PACKED_HOST ?? 'codex';
if (host !== 'codex') throw new Error('the first native packed proof currently calibrates Codex');
const root = realpathSync(mkdtempSync(join(tmpdir(), 'pulse-native-packed.')));
let keep = process.env.PULSE_KEEP_NATIVE_PACKED_ROOT === '1';
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
  const installed = await packedPulse(tarball, ['install', '--json'], {
    cwd: workspace, env, statuses: [0, 1], timeout: process.platform === 'win32' ? 180_000 : 15 * 60_000,
  });
  const installResult = json(installed.stdout, 'native packed install result is invalid');
  assert.equal(installResult.outcome, 'action_required', `${JSON.stringify(installResult)}\n${installed.stderr}`);
  assert.equal(
    installResult.reason_code,
    'codex_lifecycle_required',
    `${JSON.stringify(installResult)}\n${installed.stderr}`,
  );
  assert.equal(installResult.host_status.hosts[0].installed, true);
  assert.equal(installResult.host_status.hosts[0].reload_required, true);
  const pluginRoot = installedPluginRoot(codexHome);
  assert.equal(basename(pluginRoot), '0.7.0');

  const { runtime } = installedRuntime(baseEnv.PULSE_BINDING_REGISTRY_PATH, workspace);
  const secret = readFileSync(join(runtime.data_dir, 'secret.key'), 'utf8').trim();
  assert.match(secret, /^[a-f0-9]{64}$/);
  const initialStatus = await productJSON(runtime, secret, '/memory/status');
  assert.equal(initialStatus.full_retrieval, true);
  assert.equal(initialStatus.raw_capture_enabled, false);
  assert.equal(initialStatus.backend_llm_enabled, false);
  // R25 starts at a genuinely ready product, not while npm and the native
  // runtime are still being installed.
  const firstValueStartedAt = Date.now();

  const freshHostEnv = Object.fromEntries(
    Object.entries(env).filter(([name]) => !name.startsWith('PULSE_') ||
      ['PULSE_TRUST_MODE', 'PULSE_TEST_CODEX_LIFECYCLE_ATTESTOR'].includes(name)),
  );
  const hookEnv = { ...freshHostEnv, PLUGIN_DATA: join(root, 'plugin-data') };
  const firstSessionID = 'session-native-packed-first';
  const firstTurnID = 'turn-native-packed-first';
  const firstSession = codexHook(pluginRoot, 'SessionStart', codexHookInput({
    eventName: 'SessionStart', root, sessionID: firstSessionID, workspace,
    extra: { source: 'startup' },
  }), { cwd: workspace, env: hookEnv });
  assert.equal(firstSession.continue, true);
  assert.match(firstSession.hookSpecificOutput.additionalContext, /pulse\.context\.v1/);

  const firstPrompt = codexHook(pluginRoot, 'UserPromptSubmit', codexHookInput({
    eventName: 'UserPromptSubmit', root, sessionID: firstSessionID, turnID: firstTurnID, workspace,
    extra: { prompt: 'Do not store this raw prompt.' },
  }), { cwd: workspace, env: hookEnv });
  assert.equal(firstPrompt.continue, true);
  const preFinalize = codexHook(pluginRoot, 'Stop', codexHookInput({
    eventName: 'Stop', root, sessionID: firstSessionID, turnID: firstTurnID, workspace,
    extra: { stop_hook_active: false, last_assistant_message: 'Do not store this raw message.' },
  }), { cwd: workspace, env: hookEnv });
  assert.equal(preFinalize.decision, 'block');
  assert.match(preFinalize.reason, /bounded Pulse finalization pass/);

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
  const { callResult, remembered } = rememberThroughInstalledMCP(pluginRoot, memoryArguments, {
    cwd: workspace, env: hookEnv,
  });
  assert.equal(remembered.status, 'candidates');
  assert.equal(remembered.receipts.length, 1);
  assert.equal(remembered.receipts[0].status, 'pending');
  assert.equal(remembered.receipts[0].object_id ?? '', '');

  const postTool = codexHook(pluginRoot, 'PostToolUse', codexHookInput({
    eventName: 'PostToolUse', root, sessionID: firstSessionID, turnID: firstTurnID, workspace,
    extra: {
      tool_name: 'mcp__pulse-product__pulse_remember', tool_input: memoryArguments,
      tool_use_id: 'tool-native-packed-remember', tool_response: callResult,
    },
  }), { cwd: workspace, env: hookEnv });
  assert.match(postTool.systemMessage, new RegExp(remembered.receipts[0].receipt_id));
  assert.match(postTool.systemMessage, /:pending/);
  const firstStop = codexHook(pluginRoot, 'Stop', codexHookInput({
    eventName: 'Stop', root, sessionID: firstSessionID, turnID: firstTurnID, workspace,
    extra: { stop_hook_active: false, last_assistant_message: 'Do not store this raw message.' },
  }), { cwd: workspace, env: hookEnv });
  assert.deepEqual(firstStop, {});

  const pendingTray = await productJSON(runtime, secret, '/memory/tray?limit=20');
  const pendingCard = pendingTray.candidates.find((candidate) =>
    candidate.candidate_id === remembered.receipts[0].candidate_id);
  assert.equal(pendingCard.state, 'pending');
  assert.equal(pendingCard.grace_expires_at, '');
  assert.equal(pendingCard.candidate.capsule.items[0].redacted_summary, summary);
  await openVisibleHomeCard({ candidate: pendingCard, runtime, secret });
  const terminalCard = await waitForTerminalCandidate(runtime, secret, pendingCard.candidate_id);
  assert.equal(['created', 'updated', 'deduplicated'].includes(terminalCard.latest_receipt.status), true);
  assert.equal(terminalCard.latest_receipt.object_id, terminalCard.canonical_object_id);
  assert.equal(terminalCard.latest_receipt.safe_provenance.host, 'codex');
  assert.match(terminalCard.latest_receipt.receipt_id, /^receipt_/);
  const objectID = terminalCard.canonical_object_id;
  const freshSessionID = 'session-native-packed-fresh';
  const freshTurnID = 'turn-native-packed-fresh';
  const freshSession = codexHook(pluginRoot, 'SessionStart', codexHookInput({
    eventName: 'SessionStart', root, sessionID: freshSessionID, workspace,
    extra: { source: 'resume' },
  }), { cwd: workspace, env: hookEnv });
  assert.equal(freshSession.continue, true);
  assert.match(freshSession.hookSpecificOutput.additionalContext, /pulse\.context\.v1/);
  assert.equal(freshSession.hookSpecificOutput.additionalContext.includes(summary), true);
  const freshPrompt = codexHook(pluginRoot, 'UserPromptSubmit', codexHookInput({
    eventName: 'UserPromptSubmit', root, sessionID: freshSessionID, turnID: freshTurnID, workspace,
    extra: { prompt: 'Continue from the saved project decision.' },
  }), { cwd: workspace, env: hookEnv });
  assert.equal(freshPrompt.continue, true);

  const firstValueMs = Date.now() - firstValueStartedAt;
  assert.equal(firstValueMs <= 60_000, true, `native packed ready-to-recall took ${firstValueMs}ms`);

  const lifecycle = await productJSON(runtime, secret, '/memory/lifecycle-readiness');
  const codexLifecycle = lifecycle.hosts.find((value) => value.host === 'codex');
  assert.equal(codexLifecycle.lifecycle_ready, true, JSON.stringify(codexLifecycle));
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
    canonical_object_id: objectID,
    fresh_session_context: true,
    host_observation: true,
    lifecycle_ready: true,
    repair_ready: true,
    same_object_recalled: true,
    first_value_ms: firstValueMs,
    token_economy: {
      state: economy.state,
      method_id: economy.method_id,
      method_version: economy.method_version,
      pulse_tokens: economy.pulse_tokens,
    },
    production_ready: false,
    support_proven: false,
  };
  process.stdout.write(`${JSON.stringify(receipt)}\n`);
  process.stdout.write('Pulse native packed install saved one visible card and recalled it in a fresh Codex session.\n');
} catch (error) {
  keep = true;
  process.stderr.write(`Native packed fixture root preserved at ${root}\n`);
  throw error;
} finally {
  stopFixtureProcesses(root);
  if (!keep) rmSync(root, { recursive: true, force: true });
}
