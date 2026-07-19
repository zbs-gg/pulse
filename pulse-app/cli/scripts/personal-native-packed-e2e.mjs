#!/usr/bin/env node

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash, generateKeyPairSync } from 'node:crypto';
import {
  chmodSync, existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, realpathSync, rmSync,
  symlinkSync, writeFileSync,
} from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { nativePackedFixtureApprovalDigest } from '../src/personal-install-command.js';
import { exactTarballPulseInvocation } from '../src/release-attestation.js';
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
  return run(process.execPath, [npmCLI(), ...exactTarballPulseInvocation(tarball.path, args)], options);
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
  const bin = join(home, '.local', 'bin');
  mkdirSync(bin, { recursive: true, mode: 0o700 });
  if (process.platform === 'win32') {
    throw new Error('Windows native host exposure requires the U11 calibrated runner adapter');
  }
  symlinkSync(realpathSync(executable), join(bin, 'codex'));
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
  exposeCodexToIsolatedHome(home);
  run('/usr/bin/git', ['init', '--quiet'], { cwd: workspace });
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
    PULSE_TEST_CODEX_LIFECYCLE_ATTESTOR: '1',
  };
  const planResult = packedPulse(tarball, ['install-plan', '--json'], {
    cwd: workspace, env: baseEnv, timeout: 180_000,
  });
  const plan = json(planResult.stdout, 'native packed plan is invalid');
  assert.equal(plan.outcome, 'ready_to_install', JSON.stringify({
    current_state: plan.current_state, reason_codes: plan.reason_codes, resources: plan.resources,
  }));
  assert.equal(plan.release.target_id, selectedTarget);
  assert.equal(plan.release.verification_profile.production, false);
  const env = {
    ...baseEnv,
    PULSE_NATIVE_PACKED_FIXTURE_APPROVAL: nativePackedFixtureApprovalDigest(plan),
  };
  const installed = packedPulse(tarball, ['install', '--json'], {
    cwd: workspace, env, statuses: [0, 1], timeout: 15 * 60_000,
  });
  const installResult = json(installed.stdout, 'native packed install result is invalid');
  assert.equal(installResult.outcome, 'action_required', `${JSON.stringify(installResult)}\n${installed.stderr}`);
  assert.equal(installResult.reason_code, 'codex_lifecycle_required');
  assert.equal(installResult.host_status.hosts[0].installed, true);
  assert.equal(installResult.host_status.hosts[0].reload_required, true);
  assert.equal(installedPluginRoot(codexHome).endsWith('/pulse/0.7.0'), true);
  const receipt = {
    schema: 'pulse.native_packed_product_fixture.v1',
    target_id: selectedTarget,
    platform: target.platform,
    architecture: target.architecture,
    host,
    packed_tarball_sha256: tarball.sha256,
    packed_tarball_bytes: tarball.bytes,
    exact_public_install_command: true,
    native_daemon: true,
    native_fixture_embedder: true,
    static_host_attached: true,
    lifecycle_ready: false,
    production_ready: false,
    support_proven: false,
  };
  process.stdout.write(`${JSON.stringify(receipt)}\n`);
  process.stdout.write('Pulse native packed install reached the truthful reload-required boundary.\n');
} catch (error) {
  keep = true;
  process.stderr.write(`Native packed fixture root preserved at ${root}\n`);
  throw error;
} finally {
  stopFixtureProcesses(root);
  if (!keep) rmSync(root, { recursive: true, force: true });
}
