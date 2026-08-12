import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { generateKeyPairSync } from 'node:crypto';
import {
  chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync,
} from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { canonicalizeWorkspace, resolveWorkspaceBinding } from '../src/workspace-binding.js';
import { defaultPlatformServices } from '../src/platform-services.js';
import { nativePackedFixtureApprovalDigest } from '../src/personal-install-command.js';
import { writeSyntheticReleaseCatalogFixture } from './product-release-fixture.mjs';

const scriptDir = dirname(fileURLToPath(import.meta.url));
const cliRoot = resolve(scriptDir, '..');
const pulseAppRoot = resolve(cliRoot, '..');

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: options.cwd,
    env: options.env ?? process.env,
    input: options.input,
    encoding: 'utf8',
    timeout: options.timeout ?? 60_000,
    stdio: ['pipe', 'pipe', 'pipe'],
  });
  if (result.status !== (options.status ?? 0)) {
    process.stderr.write(`${result.stdout || ''}${result.stderr || ''}`);
    throw new Error([
      `${command} ${args.join(' ')} exited ${result.status}`,
      result.stdout,
      result.stderr,
    ].filter(Boolean).join('\n'));
  }
  return result;
}

async function freePort() {
  const server = createServer();
  await new Promise((resolveListen, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolveListen);
  });
  const { port } = server.address();
  await new Promise((resolveClose) => server.close(resolveClose));
  return port;
}

function initializeRepository(path) {
  mkdirSync(path, { recursive: true });
  run('/usr/bin/git', ['init', '-q'], { cwd: path });
  run('/usr/bin/git', ['config', 'user.email', 'pulse-e2e@example.test'], { cwd: path });
  run('/usr/bin/git', ['config', 'user.name', 'Pulse E2E'], { cwd: path });
  run('/usr/bin/git', ['commit', '--allow-empty', '-q', '-m', 'fixture'], { cwd: path });
}

function writeFixtureBindingTrust(root) {
  const trust = join(root, 'trust');
  mkdirSync(trust, { recursive: true, mode: 0o700 });
  const registryPath = join(trust, 'workspace-bindings.json');
  const privateKeyPath = join(trust, 'workspace-bindings.key.pem');
  const publicKeyPath = join(trust, 'workspace-bindings.pub.pem');
  const anchorPath = join(trust, 'workspace-bindings.anchor.json');
  const { privateKey, publicKey } = generateKeyPairSync('ec', { namedCurve: 'P-256' });
  writeFileSync(privateKeyPath, privateKey.export({ type: 'pkcs8', format: 'pem' }), { mode: 0o600 });
  writeFileSync(publicKeyPath, publicKey.export({ type: 'spki', format: 'pem' }), { mode: 0o600 });
  return { registryPath, privateKeyPath, publicKeyPath, anchorPath };
}

function packedTarball(root) {
	const npmArgs = ['npm', 'pack', '--json', '--pack-destination', root];
	const [command, args] = process.platform === 'darwin'
		? ['/usr/bin/lockf', ['-k', '-t', '300', '/tmp/pulse-product-pack.lock', ...npmArgs]]
		: ['/usr/bin/flock', ['-w', '300', '/tmp/pulse-product-pack.lock', ...npmArgs]];
	run(command, args, {
		cwd: cliRoot, timeout: 330_000,
		env: { ...process.env, PULSE_ALLOW_UNNOTARIZED_INTERNAL_PREVIEW: '1' },
	});
  const tarballs = readdirSync(root).filter((name) => name.endsWith('.tgz'));
  assert.equal(tarballs.length, 1);
  return join(root, tarballs[0]);
}

function writeFakeClaude(path) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, `#!${process.execPath}
const fs = require('node:fs');
const path = require('node:path');
const args = process.argv.slice(2);
const state = process.env.PULSE_FAKE_CLAUDE_STATE;
function readState() {
  try { return JSON.parse(fs.readFileSync(state, 'utf8')); } catch { return {}; }
}
function writeState(value) {
  fs.mkdirSync(path.dirname(state), { recursive: true });
  fs.writeFileSync(state, JSON.stringify(value));
}
if (args[0] === '--version') { console.log('2.1.207 (Claude Code)'); process.exit(0); }
if (args[0] === 'plugin') {
  const command = args[1];
  const current = readState();
  if (command === 'list') {
    const list = current.installPath ? [{
      id: 'pulse@zbs-gg', version: current.version,
      installPath: current.installPath, enabled: current.enabled === true,
    }] : [];
    console.log(JSON.stringify(list));
    process.exit(0);
  }
  if (command === 'marketplace' && args[2] === 'add') {
    writeState({ ...current, marketplace: path.resolve(args[3]) });
    process.exit(0);
  }
  if (command === 'install' || command === 'update') {
    if (!current.marketplace) process.exit(2);
    const source = path.join(current.marketplace, 'plugins', 'pulse');
    const manifest = JSON.parse(fs.readFileSync(path.join(source, '.codex-plugin', 'plugin.json'), 'utf8'));
    const installPath = path.join(process.env.HOME, '.claude', 'plugins', 'cache', 'zbs-gg', 'pulse', manifest.version);
    fs.rmSync(installPath, { recursive: true, force: true });
    fs.mkdirSync(path.dirname(installPath), { recursive: true });
    fs.cpSync(source, installPath, { recursive: true });
    writeState({ ...current, installPath, version: manifest.version, enabled: current.enabled === true });
    process.exit(0);
  }
  if (command === 'enable' || command === 'disable') {
    if (!current.installPath) process.exit(2);
    writeState({ ...current, enabled: command === 'enable' });
    process.exit(0);
  }
  if (command === 'uninstall') {
    if (current.installPath) fs.rmSync(current.installPath, { recursive: true, force: true });
    writeState({ marketplace: current.marketplace });
    process.exit(0);
  }
  process.exit(2);
}
if (args[0] === 'mcp' && args[1] === 'remove') {
  console.error('No MCP server named "pulse" in local scope');
  process.exit(1);
}
process.exit(2);
`, { mode: 0o700 });
  chmodSync(path, 0o700);
}

function writeFakeCodex(path) {
  writeFileSync(path, `#!${process.execPath}
const fs = require('node:fs');
const path = require('node:path');
const args = process.argv.slice(2);
const marketplaceState = process.env.PULSE_FAKE_CODEX_MARKETPLACE_STATE;
const cacheRoot = path.join(process.env.CODEX_HOME, 'plugins', 'cache', 'zbs-gg', 'pulse');
function readMarketplace() {
  if (!marketplaceState || !fs.existsSync(marketplaceState)) return undefined;
  return fs.readFileSync(marketplaceState, 'utf8').trim();
}
function installedPlugin() {
  if (!fs.existsSync(cacheRoot)) return undefined;
  const versions = fs.readdirSync(cacheRoot).sort();
  if (versions.length !== 1) return undefined;
  return { version: versions[0], root: path.join(cacheRoot, versions[0]) };
}
function widenSnapshotDirectories(root) {
  const visit = (directory) => {
    fs.chmodSync(directory, 0o755);
    for (const name of fs.readdirSync(directory)) {
      const child = path.join(directory, name);
      const info = fs.lstatSync(child);
      if (info.isDirectory()) visit(child);
    }
  };
  visit(path.join(root, 'plugins'));
}
if (args[0] === '--version') { console.log('codex-cli 0.1.0'); process.exit(0); }
if (args[0] !== 'plugin') process.exit(2);
if (args[1] === 'marketplace' && args[2] === 'add') {
  const source = path.resolve(args[3]);
  fs.mkdirSync(path.dirname(marketplaceState), { recursive: true });
  fs.writeFileSync(marketplaceState, source);
  widenSnapshotDirectories(source);
  process.exit(0);
}
if (args[1] === 'marketplace' && args[2] === 'list') {
  console.log('MARKETPLACE  ROOT');
  const source = readMarketplace();
  if (source) console.log('zbs-gg      ' + source);
  process.exit(0);
}
if (args[1] === 'marketplace' && args[2] === 'remove') {
  try { fs.rmSync(marketplaceState); } catch {}
  process.exit(0);
}
if (args[1] === 'add') {
  const marketplace = readMarketplace();
  if (!marketplace) process.exit(3);
  const source = path.join(marketplace, 'plugins', 'pulse');
  const manifest = JSON.parse(fs.readFileSync(path.join(source, '.codex-plugin', 'plugin.json'), 'utf8'));
  const root = path.join(cacheRoot, manifest.version);
  fs.rmSync(cacheRoot, { recursive: true, force: true });
  fs.mkdirSync(path.dirname(root), { recursive: true });
  fs.cpSync(source, root, { recursive: true });
  if (process.env.PULSE_FAKE_CODEX_BAD_PLUGIN === '1') {
    fs.writeFileSync(path.join(root, '.mcp.json'), JSON.stringify({ mcpServers: { pulse: { url: 'https://unsafe.invalid' } } }));
  }
  process.exit(0);
}
if (args[1] === 'remove') { fs.rmSync(cacheRoot, { recursive: true, force: true }); process.exit(0); }
if (args[1] === 'list') {
  const installed = installedPlugin();
  if (installed) {
    const mcpPath = path.join(installed.root, '.mcp.json');
    const badUpgrade = fs.existsSync(mcpPath) && fs.readFileSync(mcpPath, 'utf8').includes('unsafe.invalid');
    const version = process.env.PULSE_FAKE_CODEX_STALE_PLUGIN === '1' && !badUpgrade
      ? '0.6.0' : installed.version;
    console.log('pulse@zbs-gg  installed, enabled  ' + version + '  ' + installed.root);
  }
  process.exit(0);
}
process.exit(2);
`, { mode: 0o700 });
  chmodSync(path, 0o700);
}

function hookCommand(settings, event, payload) {
  const entries = settings.hooks[event].filter((entry) => {
    if (!entry.matcher || entry.matcher === '*') return true;
    if (event === 'PostToolUse' || event === 'PreToolUse') {
      return entry.matcher.split('|').includes(payload.tool_name);
    }
    return true;
  });
  const handlers = entries.flatMap((entry) => entry.hooks)
    .filter((handler) => /\b(?:claude-hook|product-hook)\s/.test(handler.command));
  assert.equal(handlers.length, 1, `${event} must have exactly one Pulse handler`);
  return handlers[0].command;
}

function runHook(settings, event, payload, workspace, env) {
  const result = run('/bin/sh', ['-c', hookCommand(settings, event, payload)], {
    cwd: workspace,
    env,
    input: JSON.stringify(payload),
    timeout: 30_000,
  });
  return JSON.parse(result.stdout);
}

function runProductMcp(config, input, workspace, env) {
  const result = run(config.command, config.args, {
    cwd: workspace, env: { ...env, ...(config.env ?? {}) }, input, timeout: 30_000,
  });
  return result.stdout.trim().split(/\r?\n/).filter(Boolean).map((line) => JSON.parse(line));
}

function readFilesRecursively(root) {
  const files = [];
  for (const entry of readdirSync(root, { withFileTypes: true })) {
    const path = join(root, entry.name);
    if (entry.isDirectory()) {
      files.push(...readFilesRecursively(path));
    } else if (entry.isFile()) {
      files.push(readFileSync(path));
    }
  }
  return files;
}

async function holdActivationLock(path) {
  return defaultPlatformServices.acquirePrivateLock(path, { staleAfterMs: 0, timeoutMs: 0 });
}

function mcpRememberInput(memoryArguments) {
  return [
    { jsonrpc: '2.0', id: 1, method: 'initialize', params: {
      protocolVersion: '2024-11-05', capabilities: {}, clientInfo: { name: 'pulse-e2e', version: '1' },
    } },
    { jsonrpc: '2.0', method: 'notifications/initialized', params: {} },
    { jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} },
    { jsonrpc: '2.0', id: 3, method: 'tools/call', params: {
      name: 'pulse_memory', arguments: memoryArguments,
    } },
  ].map((message) => JSON.stringify(message)).join('\n') + '\n';
}

async function waitForCandidate(baseUrl, secret, objectID, terminalStatus) {
  const deadline = Date.now() + 15_000;
  let lastStatus = 'missing';
  while (Date.now() < deadline) {
    const response = await fetch(`${baseUrl}/memory/tray?limit=100`, {
      headers: { 'X-Pulse-Key': secret },
    });
    if (response.ok) {
      const tray = await response.json();
      const candidate = tray.candidates?.find((entry) => entry.canonical_object_id === objectID);
      const receipt = candidate?.latest_receipt;
      lastStatus = receipt?.status ?? lastStatus;
      if (receipt?.status === terminalStatus) return candidate;
    }
    await new Promise((resolveDelay) => setTimeout(resolveDelay, 100));
  }
  throw new Error(`object ${objectID} did not reach ${terminalStatus}; last=${lastStatus}`);
}

const root = mkdtempSync(join(tmpdir(), 'pulse-claude-product.'));
let runtimeDataDir;
try {
  const home = join(root, 'home');
  const workspace = join(root, 'workspace');
  const installRoot = join(root, 'packed-cli');
  const pulseDataDir = join(root, 'pulse');
  const artifactRoot = join(pulseDataDir, 'artifacts');
  const tools = join(root, 'tools');
	const codexHome = join(root, 'codex');
  mkdirSync(home, { recursive: true });
  mkdirSync(installRoot, { recursive: true });
  mkdirSync(tools, { recursive: true });
	mkdirSync(codexHome, { recursive: true });
  initializeRepository(workspace);

  const port = await freePort();
  const workspaceIdentity = canonicalizeWorkspace(workspace);
  const bindingPaths = writeFixtureBindingTrust(root);
  const daemon = join(root, 'pulse-product-daemon');
  run('go', ['build', '-o', daemon, './cmd/pulse'], { cwd: pulseAppRoot, timeout: 120_000 });
  chmodSync(daemon, 0o700);
  let releaseFixture = await writeSyntheticReleaseCatalogFixture(root, readFileSync(daemon), 8);
  assert.equal(existsSync(artifactRoot), false, 'packed provisioning must start with an empty artifact root');

  const tarball = packedTarball(root);
  run('npm', ['install', '--ignore-scripts', '--no-audit', '--no-fund', '--prefix', installRoot, tarball], {
    cwd: root, timeout: 120_000,
  });
  const packedCLI = join(installRoot, 'node_modules', '@zbs-gg', 'pulse', 'src', 'cli.js');
  const fakeClaude = join(home, '.local', 'bin', 'claude');
  writeFakeClaude(fakeClaude);
	const fakeCodex = join(tools, 'codex');
	writeFakeCodex(fakeCodex);
  const fakeClaudeState = join(root, 'claude-mcp.json');
  const env = {
    ...process.env,
    HOME: home,
		CODEX_HOME: codexHome,
    PATH: `${tools}:${process.env.PATH}`,
    PULSE_DATA_DIR: pulseDataDir,
    PULSE_BINDING_REGISTRY_PATH: bindingPaths.registryPath,
    PULSE_BINDING_PUBLIC_KEY_PATH: bindingPaths.publicKeyPath,
    PULSE_BINDING_ANCHOR_PATH: bindingPaths.anchorPath,
    PULSE_TRUST_MODE: 'test',
		PULSE_TEST_CODEX_LIFECYCLE_ATTESTOR: '1',
    PULSE_RELEASE_TEST_MODE: '1',
    PULSE_RELEASE_MANIFEST_PATH: releaseFixture.manifestPath,
    PULSE_RELEASE_TEST_ROOT_PATH: releaseFixture.rootPath,
    PULSE_RELEASE_TEST_ASSET_ROOT: releaseFixture.assetsRoot,
    PULSE_FAKE_CLAUDE_STATE: fakeClaudeState,
		PULSE_FAKE_CODEX_MARKETPLACE_STATE: join(root, 'codex-marketplace.txt'),
    PULSE_CLI_ANIMATION: '0',
		PULSE_NATIVE_PACKED_FIXTURE_ATTESTATION: '1',
		PULSE_NATIVE_PACKED_FIXTURE_ROOT: root,
		PULSE_NATIVE_PACKED_FIXTURE_PORT: String(port),
		PULSE_NATIVE_PACKED_FIXTURE_BINDING_KEY_PATH: bindingPaths.privateKeyPath,
		PULSE_NATIVE_PACKED_CODEX_EXECUTABLE: fakeCodex,
		PULSE_NATIVE_PACKED_CLAUDE_EXECUTABLE: fakeClaude,
  };
  for (const name of [
    'PULSE_GO_BIN', 'PULSE_LOCAL_EMBED_PYTHON', 'PULSE_LOCAL_EMBED_HELPER', 'PULSE_LOCAL_EMBED_MODEL',
    'PULSE_MANAGED_EMBEDDER_CONFIG', 'COHERE_API_KEY',
	]) delete env[name];
	const approveInstall = () => {
		const plan = JSON.parse(run(process.execPath, [
			packedCLI, 'init', 'codex', '--only', 'codex', '--dry-run', '--json',
		], {
			cwd: workspace, env, timeout: 180_000,
		}).stdout);
		assert.equal(plan.outcome, 'ready_to_install');
		env.PULSE_NATIVE_PACKED_FIXTURE_APPROVAL = nativePackedFixtureApprovalDigest(plan);
	};

	const projectMcpPath = join(workspace, '.mcp.json');
	writeFileSync(projectMcpPath, JSON.stringify({
    mcpServers: {
      pulse: { command: 'legacy-pulse', env: { PULSE_API_KEY: 'legacy-secret-must-be-removed' } },
      keep: { command: 'keep-mcp' },
    },
  }));
	mkdirSync(join(workspace, '.claude'), { recursive: true });
	const projectSettingsPath = join(workspace, '.claude', 'settings.local.json');
	writeFileSync(projectSettingsPath, JSON.stringify({
    permissions: { allow: ['Bash(git status)'] },
    hooks: { SessionStart: [{ hooks: [
      { type: 'command', command: 'echo keep-session' },
      { type: 'command', command: 'pulse hook session-start' },
    ] }] },
  }));
	approveInstall();
	const installed = run(process.execPath, [
		packedCLI, 'init', 'codex', '--only', 'codex', '--yes', '--json',
	], { cwd: workspace, env, timeout: 15 * 60_000 });
	const installResult = JSON.parse(installed.stdout);
	assert.equal(installResult.outcome, 'ready');
	assert.deepEqual(installResult.host_status.hosts.map((host) => host.host), ['codex']);
	const binding = resolveWorkspaceBinding({
		cwd: workspace,
		registryPath: bindingPaths.registryPath,
		publicKeyPath: bindingPaths.publicKeyPath,
		anchorPath: bindingPaths.anchorPath,
		rootAnchor: false,
	});
	const vaultDir = binding.personal.data_dir;
	runtimeDataDir = vaultDir;

	const mcpBeforeFailedConnect = readFileSync(projectMcpPath);
	const settingsBeforeFailedConnect = readFileSync(projectSettingsPath);
	const activationLock = join(root, 'pulse', 'runtime', 'product-activation.lock');
	const releaseActivationLock = await holdActivationLock(activationLock);
	const lockedConnect = run(process.execPath, [packedCLI, 'connect', 'claude-code'], {
		cwd: workspace, env, timeout: 30_000, status: 1,
	});
	assert.match(`${lockedConnect.stdout}${lockedConnect.stderr}`, /another Pulse product activation is running/);
	const lockedDisconnect = run(process.execPath, [packedCLI, 'disconnect', 'claude-code'], {
		cwd: workspace, env, timeout: 30_000, status: 1,
	});
	assert.match(`${lockedDisconnect.stdout}${lockedDisconnect.stderr}`, /another Pulse product activation is running/);
	assert.deepEqual(readFileSync(projectMcpPath), mcpBeforeFailedConnect);
	assert.deepEqual(readFileSync(projectSettingsPath), settingsBeforeFailedConnect);
	await releaseActivationLock();
  const connected = run(process.execPath, [packedCLI, 'connect', 'claude-code'], {
    cwd: workspace, env, timeout: 120_000,
  });
  assert.match(connected.stdout, /one bound vault, two connected harnesses/);
  assert.match(connected.stdout, /pinned local runtime/);
  const claudePlugin = JSON.parse(readFileSync(fakeClaudeState, 'utf8'));
  assert.equal(claudePlugin.enabled, true);
  const mcpConfig = {
    command: join(home, '.local', 'bin', 'pulse'),
    args: ['claude-mcp'],
    env: {},
  };
  assert.doesNotMatch(JSON.stringify(mcpConfig), /PULSE_API_KEY|secret\.key|\bnpx\b/);
  assert.doesNotMatch(connected.stdout, /[?&]key=|PULSE_API_KEY|secret\.key/i);
	const viewerResult = run(process.execPath, [packedCLI, 'viewer', '--print-url'], {
		cwd: workspace, env,
	});
	const viewerURL = new URL(viewerResult.stdout.trim());
	assert.equal(viewerURL.origin, `http://127.0.0.1:${port}`);
	assert.equal(viewerURL.searchParams.get('thread_id'), workspaceIdentity.repository_id);
	const viewerDataURL = new URL('/viewer/data', viewerURL);
	viewerDataURL.search = viewerURL.search;
	const viewerResponse = await fetch(viewerDataURL);
	assert.equal(viewerResponse.status, 200);
	assert.equal((await viewerResponse.json()).memory_tray !== undefined, true);
  const projectMcp = JSON.parse(readFileSync(join(workspace, '.mcp.json'), 'utf8'));
  assert.equal(projectMcp.mcpServers.pulse, undefined);
  assert.equal(projectMcp.mcpServers.keep.command, 'keep-mcp');

  const settingsPath = join(workspace, '.claude', 'settings.local.json');
  const settings = JSON.parse(readFileSync(settingsPath, 'utf8'));
  assert.deepEqual(settings.permissions, { allow: ['Bash(git status)'] });
  assert.deepEqual(Object.keys(settings.hooks).sort(), ['SessionStart']);
  const sessionCommands = settings.hooks.SessionStart.flatMap((entry) => entry.hooks.map((hook) => hook.command));
  assert.equal(sessionCommands.includes('echo keep-session'), true);
  assert.equal(sessionCommands.some((command) => /claude-hook SessionStart/.test(command)), false);
  assert.equal(sessionCommands.includes('pulse hook session-start'), false);
  const claudeHooks = JSON.parse(readFileSync(join(claudePlugin.installPath, 'hooks', 'hooks.json'), 'utf8'));
  assert.equal(
    claudeHooks.hooks.PostToolUse[0].matcher.split('|').includes('mcp__pulse-product__pulse_memory'),
    true,
  );

  const nestedWorkspace = join(workspace, 'nested', 'feature');
  mkdirSync(nestedWorkspace, { recursive: true });

  const transcriptPath = join(root, 'must-not-be-read.jsonl');
  const claudeSession = 'session-claude-e2e';
  const promptID = '019f-claude-prompt-e2e';
  const promptPayload = {
    session_id: claudeSession, prompt_id: promptID, transcript_path: transcriptPath,
    cwd: workspace, hook_event_name: 'UserPromptSubmit', permission_mode: 'default',
    prompt: 'raw prompt must not be stored anywhere',
  };
  const pluginEnv = { ...env, CLAUDE_PLUGIN_ROOT: claudePlugin.installPath };
  const promptOutput = runHook(claudeHooks, 'UserPromptSubmit', promptPayload, workspace, pluginEnv);
  assert.equal(promptOutput.continue, true);
  assert.match(promptOutput.hookSpecificOutput.additionalContext, /Call pulse_memory once/);
  const claudeReadinessPath = join(vaultDir, 'claude-code-hook-readiness.json');
  assert.equal(existsSync(claudeReadinessPath), true, 'native prompt must record Claude readiness');

  const summary = 'Use one canonical memory object across Claude Code and Codex.';
  const claudeMemory = {
    items: [{
      kind: 'decision', scope: 'project', summary,
    }],
  };
  const preTool = runHook(claudeHooks, 'PreToolUse', {
    ...promptPayload, hook_event_name: 'PreToolUse', tool_name: 'mcp__pulse-product__pulse_memory',
    tool_input: claudeMemory, tool_use_id: 'tool-claude-e2e',
  }, workspace, pluginEnv);
  assert.deepEqual(preTool, {});
  const freshMcpEnv = { ...env };
  delete freshMcpEnv.PULSE_GO_BIN;
  freshMcpEnv.CLAUDE_PLUGIN_ROOT = claudePlugin.installPath;
  const claudeMessages = runProductMcp(
    mcpConfig, mcpRememberInput(claudeMemory), nestedWorkspace, freshMcpEnv,
  );
  assert.deepEqual(claudeMessages.find((message) => message.id === 2).result.tools.map((tool) => tool.name), ['pulse_memory']);
  const claudeRemember = JSON.parse(claudeMessages.find((message) => message.id === 3).result.content[0].text);
  assert.equal(claudeRemember.status, 'stored');
  assert.equal(claudeRemember.ids.length, 1);
  assert.match(claudeRemember.ids[0], /^pulse:/);
  const claudePost = runHook(claudeHooks, 'PostToolUse', {
    ...promptPayload, hook_event_name: 'PostToolUse', tool_name: 'mcp__pulse-product__pulse_memory',
    tool_input: claudeMemory, tool_use_id: 'tool-claude-e2e',
    tool_response: claudeMessages.find((message) => message.id === 3).result,
  }, workspace, pluginEnv);
  assert.deepEqual(claudePost, {});
  const claudeReadiness = JSON.parse(readFileSync(claudeReadinessPath, 'utf8'));
  assert.deepEqual(Object.keys(claudeReadiness.milestones).sort(), ['prompt_context', 'write_receipt']);

  const secret = readFileSync(join(vaultDir, 'secret.key'), 'utf8');
  const baseUrl = binding.personal.base_url;
  const claudeCommitted = await waitForCandidate(
    baseUrl, secret, claudeRemember.ids[0], 'created',
  );
  assert.equal(claudeCommitted.state, 'committed');
  assert.equal(claudeCommitted.current, true);
  assert.equal(claudeCommitted.canonical_object_id, claudeRemember.ids[0]);
  assert.equal(claudeCommitted.projection_status, 'complete');

  const codexConnected = run(process.execPath, [packedCLI, 'connect', 'codex'], {
    cwd: workspace, env, timeout: 120_000,
  });
  assert.match(codexConnected.stdout, /Codex plugin installed/);
  for (const capturePath of [join(root, 'pulse', 'capture-state.json'), join(vaultDir, 'capture-state.json')]) {
    const state = JSON.parse(readFileSync(capturePath, 'utf8'));
    assert.equal(state.hosts.codex.enabled, true);
    assert.equal(state.hosts['claude-code'].enabled, true);
  }
	const pluginCacheRoot = join(codexHome, 'plugins', 'cache', 'zbs-gg', 'pulse');
	const pluginVersions = readdirSync(pluginCacheRoot);
	assert.equal(pluginVersions.length, 1);
	const pluginMcpPath = join(pluginCacheRoot, pluginVersions[0], '.mcp.json');
	const pluginMcpBeforeFailedUpgrade = readFileSync(pluginMcpPath);
	const failedPluginUpgrade = run(process.execPath, [packedCLI, 'connect', 'codex'], {
		cwd: workspace,
		env: { ...env, PULSE_FAKE_CODEX_STALE_PLUGIN: '1', PULSE_FAKE_CODEX_BAD_PLUGIN: '1' },
		timeout: 120_000,
		status: 1,
	});
	assert.match(`${failedPluginUpgrade.stdout}${failedPluginUpgrade.stderr}`, /codex_plugin_snapshot_mismatch/);
	assert.deepEqual(readFileSync(pluginMcpPath), pluginMcpBeforeFailedUpgrade);

  const runtimeCLI = join(root, 'pulse', 'runtime', 'codex', 'current', 'src', 'cli.js');
  const codexSession = 'session-codex-e2e';
  const codexTurn = 'turn-codex-e2e';
  const codexBase = {
    session_id: codexSession, turn_id: codexTurn, transcript_path: transcriptPath,
    cwd: workspace, model: 'gpt-5.6', permission_mode: 'default',
  };
  const codexResume = run(process.execPath, [runtimeCLI, 'codex-hook', 'UserPromptSubmit'], {
    cwd: nestedWorkspace, env, input: JSON.stringify({
      ...codexBase, turn_id: 'turn-codex-recall-claude-memory',
      hook_event_name: 'UserPromptSubmit', prompt: summary,
    }),
  });
  assert.match(JSON.parse(codexResume.stdout).hookSpecificOutput.additionalContext, new RegExp(summary));
  run(process.execPath, [runtimeCLI, 'codex-hook', 'UserPromptSubmit'], {
    cwd: workspace, env, input: JSON.stringify({
      ...codexBase, hook_event_name: 'UserPromptSubmit', prompt: 'also must not persist',
    }),
  });
  const codexSummary = 'Codex writes to the same private vault without a review step.';
  const codexMemory = {
    items: [{
      kind: 'decision', scope: 'project', summary: codexSummary,
    }],
  };
  run(process.execPath, [runtimeCLI, 'codex-hook', 'PreToolUse'], {
    cwd: workspace, env, input: JSON.stringify({
      ...codexBase, hook_event_name: 'PreToolUse', tool_name: 'mcp__pulse-product__pulse_memory',
      tool_input: codexMemory, tool_use_id: 'tool-codex-e2e',
    }),
  });
  const codexMcpConfig = { command: process.execPath, args: [runtimeCLI, 'codex-mcp'] };
  const codexMessages = runProductMcp(codexMcpConfig, mcpRememberInput(codexMemory), nestedWorkspace, env);
  const codexRemember = JSON.parse(codexMessages.find((message) => message.id === 3).result.content[0].text);
  assert.equal(codexRemember.status, 'stored');
  assert.equal(codexRemember.ids.length, 1);
  assert.notEqual(codexRemember.ids[0], claudeRemember.ids[0]);
  assert.match(codexRemember.ids[0], /^pulse:/);
  run(process.execPath, [runtimeCLI, 'codex-hook', 'PostToolUse'], {
    cwd: workspace, env, input: JSON.stringify({
      ...codexBase, hook_event_name: 'PostToolUse', tool_name: 'mcp__pulse-product__pulse_memory',
      tool_input: codexMemory, tool_use_id: 'tool-codex-e2e',
      tool_response: codexMessages.find((message) => message.id === 3).result,
    }),
  });
  const codexCommitted = await waitForCandidate(
    baseUrl, secret, codexRemember.ids[0], 'created',
  );
  assert.equal(codexCommitted.state, 'committed');
  assert.equal(codexCommitted.current, true);
  assert.equal(codexCommitted.canonical_object_id, codexRemember.ids[0]);
  assert.equal(codexCommitted.projection_status, 'complete');
  assert.equal(codexCommitted.latest_receipt.safe_provenance.host, 'codex');

  const resumed = runHook(claudeHooks, 'UserPromptSubmit', {
    session_id: 'session-claude-fresh', prompt_id: 'prompt-claude-fresh', transcript_path: transcriptPath, cwd: workspace,
    hook_event_name: 'UserPromptSubmit', prompt: codexSummary, permission_mode: 'default',
  }, workspace, pluginEnv);
  assert.match(resumed.hookSpecificOutput.additionalContext, new RegExp(codexSummary));

  const doctor = run(process.execPath, [packedCLI, 'doctor', 'claude-code', '--json'], {
    cwd: workspace, env,
  });
  const report = JSON.parse(doctor.stdout);
	assert.equal(report.verdict, 'Pulse Claude Code synthetic test lifecycle ready; production authority is not active.');
	assert.equal(report.trust.authority_mode, 'synthetic-test');
  assert.equal(Object.values(report.checks).every((check) => check.ok), true);
  assert.equal(report.trust.raw_transcript_capture, false);
  assert.equal(report.trust.full_retrieval, true);

	const installedCodexHook = join(pluginCacheRoot, pluginVersions[0], 'hooks', 'pulse-hook.mjs');
	const sharedCodexHookRun = run(process.execPath, [installedCodexHook, 'UserPromptSubmit'], {
		cwd: nestedWorkspace,
		env,
		input: JSON.stringify({
			...codexBase,
			session_id: 'session-codex-after-claude-connect',
			turn_id: 'turn-codex-after-claude-connect',
			hook_event_name: 'UserPromptSubmit',
			prompt: codexSummary,
		}),
	});
	assert.match(
		JSON.parse(sharedCodexHookRun.stdout).hookSpecificOutput.additionalContext,
		new RegExp(codexSummary),
	);
	assert.equal(mcpConfig.env.PULSE_GO_BIN, undefined);
	const locator = JSON.parse(readFileSync(join(codexHome, 'pulse', 'product-locators.json'), 'utf8'));
	const locatorEntry = Object.values(locator.entries)[0];
	const upgradedRuntimeReceipt = JSON.parse(readFileSync(join(vaultDir, 'supervisor-runtime.json'), 'utf8'));
	const productDaemon = JSON.parse(readFileSync(join(root, 'pulse', 'runtime', 'product-daemon.json'), 'utf8'));
	assert.equal(locatorEntry.daemon_path, undefined, 'workspace locator must not carry daemon execution authority');
	assert.equal(productDaemon.schema, 'pulse.product_activation.v4');
	assert.equal(productDaemon.daemon_path, upgradedRuntimeReceipt.executable);
	assert.equal(productDaemon.daemon_digest, upgradedRuntimeReceipt.executable_digest);
	assert.match(productDaemon.runtime_tree_digest, /^[a-f0-9]{64}$/);
	for (const field of [
		'daemon_activation_digest', 'daemon_artifact_sha256', 'daemon_tree_digest',
		'embedder_runtime_activation_digest', 'embedder_runtime_artifact_sha256', 'embedder_runtime_tree_digest',
		'model_activation_digest', 'model_artifact_sha256', 'model_tree_digest',
		'plugin_runtime_activation_digest', 'plugin_runtime_artifact_sha256', 'plugin_runtime_tree_digest',
		'plugin_tree_digest', 'release_manifest_digest',
	]) assert.match(productDaemon[field], /^[a-f0-9]{64}$/);

  const installedClaudeMcpPath = join(claudePlugin.installPath, '.mcp.json');
  const installedClaudeMcp = readFileSync(installedClaudeMcpPath);
  writeFileSync(installedClaudeMcpPath, `${installedClaudeMcp.toString('utf8')}\n`);
  const tamperedDoctor = run(process.execPath, [packedCLI, 'doctor', 'claude-code', '--json'], {
    cwd: workspace, env, status: 1,
  });
  assert.equal(JSON.parse(tamperedDoctor.stdout).checks.plugin.ok, false);
	writeFileSync(installedClaudeMcpPath, installedClaudeMcp);

	const persisted = readFilesRecursively(vaultDir);
  for (const bytes of persisted) {
    assert.equal(bytes.includes(Buffer.from('raw prompt must not be stored anywhere')), false);
    assert.equal(bytes.includes(Buffer.from('raw assistant text must not be stored')), false);
  }

	const pluginRoot = dirname(pluginMcpPath);
	rmSync(pluginRoot, { recursive: true, force: true });
	const staleCodexMarkerConnect = run(process.execPath, [packedCLI, 'connect', 'claude-code'], {
		cwd: workspace, env, timeout: 120_000,
	});
	assert.match(staleCodexMarkerConnect.stdout, /one bound vault, Claude Code connected/);
	assert.doesNotMatch(staleCodexMarkerConnect.stdout, /two connected harnesses/);
	run(fakeCodex, ['plugin', 'add', 'pulse@zbs-gg'], { cwd: workspace, env });

  const runtimeReceipt = JSON.parse(readFileSync(join(vaultDir, 'supervisor-runtime.json'), 'utf8'));
  process.kill(runtimeReceipt.pid, 'SIGSTOP');
  try {
		const wedgedConnect = run(process.execPath, [packedCLI, 'connect', 'claude-code'], {
			cwd: workspace, env, status: 1, timeout: 5000,
		});
		assert.match(
			`${wedgedConnect.stdout}${wedgedConnect.stderr}`,
			/did not become ready|managed full-retrieval smoke: readiness request failed/,
		);
    const outage = run(process.execPath, [packedCLI, 'doctor', 'claude-code', '--json'], {
      cwd: workspace, env, status: 1,
    });
    assert.equal(JSON.parse(outage.stdout).checks.vault.ok, false);
  } finally {
    process.kill(runtimeReceipt.pid, 'SIGCONT');
  }

  run(process.execPath, [packedCLI, 'disconnect', 'claude-code'], { cwd: workspace, env });
  const disconnectedSettings = JSON.parse(readFileSync(settingsPath, 'utf8'));
  assert.deepEqual(disconnectedSettings.permissions, { allow: ['Bash(git status)'] });
  assert.deepEqual(
    disconnectedSettings.hooks.SessionStart.flatMap((entry) => entry.hooks.map((hook) => hook.command)),
    ['echo keep-session'],
  );
  const codexAfterClaudeDisconnect = run(process.execPath, [runtimeCLI, 'codex-hook', 'UserPromptSubmit'], {
    cwd: nestedWorkspace, env, input: JSON.stringify({
      ...codexBase, session_id: 'session-codex-after-claude-disconnect',
      turn_id: 'turn-codex-after-claude-disconnect',
      hook_event_name: 'UserPromptSubmit', prompt: codexSummary,
    }),
  });
  assert.match(
    JSON.parse(codexAfterClaudeDisconnect.stdout).hookSpecificOutput.additionalContext,
    new RegExp(codexSummary),
  );
  const disconnected = run(process.execPath, [packedCLI, 'doctor', 'claude-code', '--json'], {
    cwd: workspace, env, status: 1,
  });
  const disconnectedReport = JSON.parse(disconnected.stdout);
  assert.equal(disconnectedReport.checks.plugin.ok, false);
  assert.equal(disconnectedReport.checks.capture.ok, false);

  process.stdout.write('Pulse Claude Code packed-product cross-harness E2E passed.\n');
} finally {
  try {
    const receipt = JSON.parse(readFileSync(join(runtimeDataDir, 'supervisor-runtime.json'), 'utf8'));
    process.kill(receipt.pid, 'SIGTERM');
  } catch { /* no running fixture */ }
  rmSync(root, { recursive: true, force: true });
}
