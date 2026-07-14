import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createHash, generateKeyPairSync, sign } from 'node:crypto';
import {
  chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync,
} from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  bindingRegistryAnchor, canonicalJSONStringify, canonicalizeWorkspace,
} from '../src/workspace-binding.js';

const scriptDir = dirname(fileURLToPath(import.meta.url));
const cliRoot = resolve(scriptDir, '..');
const pulseAppRoot = resolve(cliRoot, '..');
const repoRoot = resolve(cliRoot, '..', '..');

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

function writeSignedPersonalBinding(root, workspace, port) {
  const trust = join(root, 'trust');
  mkdirSync(trust, { recursive: true, mode: 0o700 });
  const registryPath = join(trust, 'workspace-bindings.json');
  const publicKeyPath = join(trust, 'workspace-bindings.pub.pem');
  const anchorPath = join(trust, 'workspace-bindings.anchor.json');
  const payload = {
    schema: 'pulse.workspace-binding-registry.v1',
    epoch: 1,
    bindings: [{
      binding_id: 'binding_multiharness_e2e',
      receipt_id: 'receipt_multiharness_e2e',
      resolver_epoch: 1,
      workspace: {
        workspace_id: workspace.workspace_id,
        repository_id: workspace.repository_id,
      },
      mode: 'personal',
      principal_ref: 'principal_multiharness_e2e',
      personal: {
        store_id: 'store_personal_multiharness_e2e',
        data_dir: join(root, 'vaults', 'personal'),
        base_url: `http://127.0.0.1:${port}`,
        credential_ref: 'local:pulse/multiharness-e2e',
        cache_dir: join(root, 'caches', 'personal'),
      },
    }],
  };
  const { privateKey, publicKey } = generateKeyPairSync('ed25519');
  const signature = sign(null, Buffer.from(canonicalJSONStringify(payload)), privateKey).toString('base64');
  const registryBytes = Buffer.from(JSON.stringify({ algorithm: 'ed25519', payload, signature }));
  writeFileSync(registryPath, registryBytes, { mode: 0o600 });
  writeFileSync(publicKeyPath, publicKey.export({ type: 'spki', format: 'pem' }), { mode: 0o600 });
  writeFileSync(anchorPath, `${canonicalJSONStringify(bindingRegistryAnchor(registryBytes, payload.epoch))}\n`, { mode: 0o600 });
  return { registryPath, publicKeyPath, anchorPath };
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
  writeFileSync(path, `#!${process.execPath}
const fs = require('node:fs');
const args = process.argv.slice(2);
const state = process.env.PULSE_FAKE_CLAUDE_STATE;
if (args[0] === '--version') { console.log('2.1.207 (Claude Code)'); process.exit(0); }
if (args[0] !== 'mcp') process.exit(2);
if (args[1] === 'remove') {
  if (process.env.PULSE_FAKE_CLAUDE_FAIL_REMOVE === '1' && fs.existsSync(state)) process.exit(3);
  try { fs.rmSync(state); } catch {}
  process.exit(0);
}
if (args[1] === 'add-json') {
  const config = JSON.parse(args[args.length - 1]);
  fs.writeFileSync(state, JSON.stringify(config));
	if (process.env.PULSE_FAKE_CLAUDE_ADD_WRITES_THEN_FAIL === '1') process.exit(2);
  console.log('Added stdio MCP server pulse');
  process.exit(0);
}
if (args[1] === 'get') {
  if (!fs.existsSync(state)) process.exit(1);
  const config = JSON.parse(fs.readFileSync(state, 'utf8'));
  console.log('pulse:');
  console.log('  Scope: Local config (private to you in this project)');
  console.log('  Status: ✔ Connected');
  console.log('  Type: stdio');
  console.log('  Command: ' + config.command);
  console.log('  Args: ' + config.args.join(' '));
  if (config.env && Object.keys(config.env).length) {
    console.log('  Environment:');
    for (const [name, value] of Object.entries(config.env)) console.log('    ' + name + '=' + value);
  }
  console.log('');
  console.log('To remove this server, run: claude mcp remove pulse');
  process.exit(0);
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
const root = path.join(process.env.CODEX_HOME, 'plugins', 'cache', 'zbs-gg', 'pulse', '0.7.0');
if (args[0] === '--version') { console.log('codex-cli 0.1.0'); process.exit(0); }
if (args[0] !== 'plugin') process.exit(2);
if (args[1] === 'marketplace' && args[2] === 'add') process.exit(0);
if (args[1] === 'add') {
  fs.mkdirSync(path.dirname(root), { recursive: true });
  fs.cpSync(process.env.PULSE_FAKE_CODEX_PLUGIN_SOURCE, root, { recursive: true });
  if (process.env.PULSE_FAKE_CODEX_BAD_PLUGIN === '1') {
    fs.writeFileSync(path.join(root, '.mcp.json'), JSON.stringify({ mcpServers: { pulse: { url: 'https://unsafe.invalid' } } }));
  }
  process.exit(0);
}
if (args[1] === 'remove') { fs.rmSync(root, { recursive: true, force: true }); process.exit(0); }
if (args[1] === 'list') {
  if (fs.existsSync(root)) console.log('pulse@zbs-gg  installed, enabled  0.7.0  ' + root);
  process.exit(0);
}
process.exit(2);
`, { mode: 0o700 });
  chmodSync(path, 0o700);
}

function hookCommand(settings, event) {
  const handlers = settings.hooks[event].flatMap((entry) => entry.hooks)
    .filter((handler) => /claude-hook\s/.test(handler.command));
  assert.equal(handlers.length, 1, `${event} must have exactly one Pulse handler`);
  return handlers[0].command;
}

function runHook(settings, event, payload, workspace, env) {
  const result = run('/bin/sh', ['-c', hookCommand(settings, event)], {
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
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  const source = 'process.stdout.write("ready\\n");process.stdin.resume();process.stdin.on("end",()=>process.exit(0));';
  const child = spawn('/usr/bin/lockf', ['-k', '-t', '0', path, process.execPath, '-e', source], {
    stdio: ['pipe', 'pipe', 'pipe'],
  });
  await new Promise((resolveReady, rejectReady) => {
    const timer = setTimeout(() => rejectReady(new Error('fixture lock timed out')), 3000);
    child.stdout.setEncoding('utf8');
    child.stdout.once('data', (chunk) => {
      clearTimeout(timer);
      if (String(chunk).includes('ready')) resolveReady();
      else rejectReady(new Error('fixture lock did not become ready'));
    });
    child.once('error', rejectReady);
    child.once('exit', (status) => rejectReady(new Error(`fixture lock exited ${status}`)));
  });
  return async () => {
    child.stdin.end();
    await new Promise((resolveExit) => child.once('exit', resolveExit));
  };
}

function mcpRememberInput(memoryArguments) {
  return [
    { jsonrpc: '2.0', id: 1, method: 'initialize', params: {
      protocolVersion: '2024-11-05', capabilities: {}, clientInfo: { name: 'pulse-e2e', version: '1' },
    } },
    { jsonrpc: '2.0', method: 'notifications/initialized', params: {} },
    { jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} },
    { jsonrpc: '2.0', id: 3, method: 'tools/call', params: {
      name: 'pulse_remember', arguments: memoryArguments,
    } },
  ].map((message) => JSON.stringify(message)).join('\n') + '\n';
}

async function waitForCandidate(baseUrl, secret, candidateID, terminalStatus) {
  const deadline = Date.now() + 15_000;
  let lastStatus = 'missing';
  while (Date.now() < deadline) {
    const response = await fetch(`${baseUrl}/memory/tray?limit=100`, {
      headers: { 'X-Pulse-Key': secret },
    });
    if (response.ok) {
      const tray = await response.json();
      const receipt = tray.candidates?.find((candidate) => candidate.candidate_id === candidateID)?.latest_receipt;
      lastStatus = receipt?.status ?? lastStatus;
      if (receipt?.status === terminalStatus) return receipt;
    }
    await new Promise((resolveDelay) => setTimeout(resolveDelay, 100));
  }
  throw new Error(`candidate ${candidateID} did not reach ${terminalStatus}; last=${lastStatus}`);
}

const root = mkdtempSync(join(tmpdir(), 'pulse-claude-product.'));
try {
  const home = join(root, 'home');
  const workspace = join(root, 'workspace');
  const installRoot = join(root, 'packed-cli');
  const tools = join(root, 'tools');
	const codexHome = join(root, 'codex');
  mkdirSync(home, { recursive: true });
  mkdirSync(installRoot, { recursive: true });
  mkdirSync(tools, { recursive: true });
	mkdirSync(codexHome, { recursive: true });
  initializeRepository(workspace);

  const port = await freePort();
  const workspaceIdentity = canonicalizeWorkspace(workspace);
  const bindingPaths = writeSignedPersonalBinding(root, workspaceIdentity, port);
  const daemon = join(root, 'pulse-product-daemon');
  run('go', ['build', '-o', daemon, './cmd/pulse'], { cwd: pulseAppRoot, timeout: 120_000 });
  chmodSync(daemon, 0o700);
  const fakeEmbedHelper = join(root, 'fake-embed-helper.mjs');
  const fakeEmbedModel = join(root, 'fake-embed-model');
  mkdirSync(fakeEmbedModel, { recursive: true });
  writeFileSync(fakeEmbedHelper, [
    "import readline from 'node:readline';",
    "process.stdout.write(JSON.stringify({id:'__startup__',ok:true})+'\\n');",
    "const lines=readline.createInterface({input:process.stdin});",
    "for await (const line of lines) { const request=JSON.parse(line); process.stdout.write(JSON.stringify({id:request.id,embeddings:request.texts.map(()=>[1,0,0,0])})+'\\n'); }",
  ].join('\n'));

  const tarball = packedTarball(root);
  run('npm', ['install', '--ignore-scripts', '--no-audit', '--no-fund', '--prefix', installRoot, tarball], {
    cwd: root, timeout: 120_000,
  });
  const packedCLI = join(installRoot, 'node_modules', '@zbs-gg', 'pulse', 'src', 'cli.js');
  const fakeClaude = join(tools, 'claude');
  writeFakeClaude(fakeClaude);
	const fakeCodex = join(tools, 'codex');
	writeFakeCodex(fakeCodex);
  const fakeClaudeState = join(root, 'claude-mcp.json');
  const env = {
    ...process.env,
    HOME: home,
		CODEX_HOME: codexHome,
    PATH: `${tools}:${process.env.PATH}`,
    PULSE_DATA_DIR: join(root, 'pulse'),
    PULSE_GO_BIN: daemon,
    PULSE_BINDING_REGISTRY_PATH: bindingPaths.registryPath,
    PULSE_BINDING_PUBLIC_KEY_PATH: bindingPaths.publicKeyPath,
    PULSE_BINDING_ANCHOR_PATH: bindingPaths.anchorPath,
    PULSE_TRUST_MODE: 'test',
    PULSE_LOCAL_EMBED_PYTHON: process.execPath,
    PULSE_LOCAL_EMBED_HELPER: fakeEmbedHelper,
    PULSE_LOCAL_EMBED_MODEL: fakeEmbedModel,
    PULSE_FAKE_CLAUDE_STATE: fakeClaudeState,
		PULSE_FAKE_CODEX_PLUGIN_SOURCE: join(repoRoot, 'plugins', 'pulse'),
		PULSE_CODEX_MARKETPLACE_SOURCE: repoRoot,
    PULSE_CLI_ANIMATION: '0',
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
	const failedExternalRegistration = run(process.execPath, [packedCLI, 'connect', 'claude-code'], {
		cwd: workspace, env: { ...env, PULSE_FAKE_CLAUDE_ADD_WRITES_THEN_FAIL: '1' },
		timeout: 120_000, status: 1,
	});
	assert.match(`${failedExternalRegistration.stdout}${failedExternalRegistration.stderr}`, /claude mcp add-json failed/);
	assert.equal(existsSync(fakeClaudeState), false, 'failed external registration must be removed during rollback');
	assert.deepEqual(readFileSync(projectMcpPath), mcpBeforeFailedConnect);
	assert.deepEqual(readFileSync(projectSettingsPath), settingsBeforeFailedConnect);

	const failedExternalRollback = run(process.execPath, [packedCLI, 'connect', 'claude-code'], {
		cwd: workspace,
		env: {
			...env,
			PULSE_FAKE_CLAUDE_ADD_WRITES_THEN_FAIL: '1',
			PULSE_FAKE_CLAUDE_FAIL_REMOVE: '1',
		},
		timeout: 120_000, status: 1,
	});
	assert.match(`${failedExternalRollback.stdout}${failedExternalRollback.stderr}`, /rollback failed: claude mcp remove failed/);
	assert.equal(existsSync(fakeClaudeState), true, 'failed rollback must be reported instead of silently claiming cleanup');
	rmSync(fakeClaudeState, { force: true });
	const daemonDigest = createHash('sha256').update(readFileSync(daemon)).digest('hex');
  const poisonedDaemon = join(root, 'pulse', 'bin', `pulse-product-daemon-${daemonDigest}`);
  mkdirSync(dirname(poisonedDaemon), { recursive: true, mode: 0o700 });
  writeFileSync(poisonedDaemon, 'wrong bytes', { mode: 0o700 });
  const poisonedConnect = run(process.execPath, [packedCLI, 'connect', 'claude-code'], {
    cwd: workspace, env, timeout: 120_000, status: 1,
  });
  assert.match(`${poisonedConnect.stdout}${poisonedConnect.stderr}`, /integrity validation/);
	assert.deepEqual(readFileSync(projectMcpPath), mcpBeforeFailedConnect);
	assert.deepEqual(readFileSync(projectSettingsPath), settingsBeforeFailedConnect);
	assert.equal(existsSync(fakeClaudeState), false);
	assert.equal(existsSync(join(root, 'pulse', 'runtime', 'codex', 'current')), false);
	assert.equal(existsSync(join(root, 'pulse', 'capture-state.json')), false);
  rmSync(poisonedDaemon, { force: true });

  const connected = run(process.execPath, [packedCLI, 'connect', 'claude-code'], {
    cwd: workspace, env, timeout: 120_000,
  });
  assert.match(connected.stdout, /one bound vault, Claude Code connected/);
  assert.match(connected.stdout, /pinned local runtime/);
  const mcpConfig = JSON.parse(readFileSync(fakeClaudeState, 'utf8'));
  assert.equal(mcpConfig.type, 'stdio');
  assert.equal(mcpConfig.command, process.execPath);
  assert.equal(mcpConfig.args[1], 'claude-mcp');
  assert.equal(mcpConfig.env.PULSE_DATA_DIR, join(root, 'pulse'));
  assert.equal(mcpConfig.env.PULSE_GO_BIN, undefined);
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
  assert.deepEqual(Object.keys(settings.hooks).sort(), [
    'PostCompact', 'PostToolUse', 'PreCompact', 'PreToolUse', 'SessionStart',
    'Stop', 'SubagentStart', 'SubagentStop', 'UserPromptSubmit',
  ]);
  assert.equal(settings.hooks.SessionStart.find((entry) =>
    entry.hooks.some((hook) => /claude-hook SessionStart/.test(hook.command))).matcher,
  'startup|resume|clear|compact');
  const sessionCommands = settings.hooks.SessionStart.flatMap((entry) => entry.hooks.map((hook) => hook.command));
  assert.equal(sessionCommands.includes('echo keep-session'), true);
  assert.equal(sessionCommands.includes('pulse hook session-start'), false);
  assert.equal(settings.hooks.PostToolUse[0].matcher, 'mcp__pulse__pulse_remember');

  const nestedWorkspace = join(workspace, 'nested', 'feature');
  mkdirSync(nestedWorkspace, { recursive: true });

  const transcriptPath = join(root, 'must-not-be-read.jsonl');
  const claudeSession = 'session-claude-e2e';
  const promptID = '019f-claude-prompt-e2e';
  const sessionOutput = runHook(settings, 'SessionStart', {
    session_id: claudeSession, transcript_path: transcriptPath, cwd: workspace,
    hook_event_name: 'SessionStart', source: 'startup', permission_mode: 'default',
  }, workspace, env);
  assert.match(sessionOutput.hookSpecificOutput.additionalContext, /pulse.context.v1/);

  const promptPayload = {
    session_id: claudeSession, prompt_id: promptID, transcript_path: transcriptPath,
    cwd: workspace, hook_event_name: 'UserPromptSubmit', permission_mode: 'default',
    prompt: 'raw prompt must not be stored anywhere',
  };
  const promptOutput = runHook(settings, 'UserPromptSubmit', promptPayload, workspace, env);
  assert.equal(promptOutput.continue, true);
  const preStop = runHook(settings, 'Stop', {
    ...promptPayload, hook_event_name: 'Stop', stop_hook_active: false,
    last_assistant_message: 'raw assistant text must not be stored', background_tasks: [], session_crons: [],
  }, workspace, env);
  assert.equal(preStop.decision, 'block');

  const summary = 'Use one canonical memory object across Claude Code and Codex.';
  const claudeMemory = {
    schema: 'pulse.memory_capsule.v1',
    source: { host: 'claude-code', conversation_scope: 'current_turn', timestamp: '2026-07-14T10:00:00Z' },
    items: [{
      kind: 'decision', redacted_summary: summary, confidence: 0.98,
      evidence_hint: 'current_turn', privacy_tier: 'normal', retention: 'project',
    }],
    raw_input_included: false,
  };
  const preTool = runHook(settings, 'PreToolUse', {
    ...promptPayload, hook_event_name: 'PreToolUse', tool_name: 'mcp__pulse__pulse_remember',
    tool_input: claudeMemory, tool_use_id: 'tool-claude-e2e',
  }, workspace, env);
  assert.deepEqual(preTool, {});
  const freshMcpEnv = { ...env };
  for (const name of [
    'PULSE_DATA_DIR', 'PULSE_GO_BIN', 'PULSE_BINDING_REGISTRY_PATH',
    'PULSE_BINDING_PUBLIC_KEY_PATH', 'PULSE_BINDING_ANCHOR_PATH', 'PULSE_TRUST_MODE',
  ]) delete freshMcpEnv[name];
  const claudeMessages = runProductMcp(
    mcpConfig, mcpRememberInput(claudeMemory), nestedWorkspace, freshMcpEnv,
  );
  const claudeRemember = JSON.parse(claudeMessages.find((message) => message.id === 3).result.content[0].text);
  assert.equal(claudeRemember.receipts[0].safe_provenance.host, 'claude-code');
  assert.equal(claudeRemember.receipts[0].status, 'pending');
  const claudePost = runHook(settings, 'PostToolUse', {
    ...promptPayload, hook_event_name: 'PostToolUse', tool_name: 'mcp__pulse__pulse_remember',
    tool_input: claudeMemory, tool_use_id: 'tool-claude-e2e',
    tool_response: claudeMessages.find((message) => message.id === 3).result,
  }, workspace, env);
  assert.match(claudePost.systemMessage, new RegExp(claudeRemember.receipts[0].receipt_id));
  const claudeStop = runHook(settings, 'Stop', {
    ...promptPayload, hook_event_name: 'Stop', stop_hook_active: false,
    last_assistant_message: 'raw assistant text must not be stored', background_tasks: [], session_crons: [],
  }, workspace, env);
  assert.deepEqual(claudeStop, {});

  const vaultDir = join(root, 'vaults', 'personal');
  const secret = readFileSync(join(vaultDir, 'secret.key'), 'utf8');
  const baseUrl = `http://127.0.0.1:${port}`;
  const created = await waitForCandidate(baseUrl, secret, claudeRemember.receipts[0].candidate_id, 'created');
  assert.match(created.object_id, /^pulse:/);

  const codexConnected = run(process.execPath, [packedCLI, 'connect', 'codex'], {
    cwd: workspace, env, timeout: 120_000,
  });
  assert.match(codexConnected.stdout, /Codex plugin installed/);
  for (const capturePath of [join(root, 'pulse', 'capture-state.json'), join(vaultDir, 'capture-state.json')]) {
    const state = JSON.parse(readFileSync(capturePath, 'utf8'));
    assert.equal(state.hosts.codex.enabled, true);
    assert.equal(state.hosts['claude-code'].enabled, true);
  }
	const pluginMcpPath = join(codexHome, 'plugins', 'cache', 'zbs-gg', 'pulse', '0.7.0', '.mcp.json');
	const pluginMcpBeforeFailedUpgrade = readFileSync(pluginMcpPath);
	const failedPluginUpgrade = run(process.execPath, [packedCLI, 'connect', 'codex'], {
		cwd: workspace, env: { ...env, PULSE_FAKE_CODEX_BAD_PLUGIN: '1' }, timeout: 120_000, status: 1,
	});
	assert.match(`${failedPluginUpgrade.stdout}${failedPluginUpgrade.stderr}`, /plugin must expose exactly one pulse-product MCP server/);
	assert.deepEqual(readFileSync(pluginMcpPath), pluginMcpBeforeFailedUpgrade);

  const runtimeCLI = mcpConfig.args[0];
  const codexSession = 'session-codex-e2e';
  const codexTurn = 'turn-codex-e2e';
  const codexBase = {
    session_id: codexSession, turn_id: codexTurn, transcript_path: transcriptPath,
    cwd: workspace, model: 'gpt-5.6', permission_mode: 'default',
  };
  const codexResume = run(process.execPath, [runtimeCLI, 'codex-hook', 'SessionStart'], {
    cwd: nestedWorkspace, env, input: JSON.stringify({
      ...codexBase, hook_event_name: 'SessionStart', source: 'resume',
    }),
  });
  assert.match(JSON.parse(codexResume.stdout).hookSpecificOutput.additionalContext, new RegExp(summary));
  run(process.execPath, [runtimeCLI, 'codex-hook', 'UserPromptSubmit'], {
    cwd: workspace, env, input: JSON.stringify({
      ...codexBase, hook_event_name: 'UserPromptSubmit', prompt: 'also must not persist',
    }),
  });
  const codexSummary = 'Codex requires the same visible receipt before durable team memory.';
  const codexMemory = {
    ...claudeMemory,
    source: { host: 'codex', conversation_scope: 'current_turn', timestamp: '2026-07-14T11:00:00Z' },
    items: [{
      ...claudeMemory.items[0], redacted_summary: codexSummary,
    }],
  };
  run(process.execPath, [runtimeCLI, 'codex-hook', 'PreToolUse'], {
    cwd: workspace, env, input: JSON.stringify({
      ...codexBase, hook_event_name: 'PreToolUse', tool_name: 'mcp__pulse-product__pulse_remember',
      tool_input: codexMemory, tool_use_id: 'tool-codex-e2e',
    }),
  });
  const codexMcpConfig = { command: process.execPath, args: [runtimeCLI, 'codex-mcp'] };
  const codexMessages = runProductMcp(codexMcpConfig, mcpRememberInput(codexMemory), nestedWorkspace, env);
  const codexRemember = JSON.parse(codexMessages.find((message) => message.id === 3).result.content[0].text);
  assert.notEqual(codexRemember.receipts[0].content_digest, claudeRemember.receipts[0].content_digest);
  const codexCreated = await waitForCandidate(baseUrl, secret, codexRemember.receipts[0].candidate_id, 'created');
  assert.equal(codexCreated.safe_provenance.host, 'codex');

  const resumed = runHook(settings, 'SessionStart', {
    session_id: 'session-claude-fresh', transcript_path: transcriptPath, cwd: workspace,
    hook_event_name: 'SessionStart', source: 'resume', permission_mode: 'default',
  }, workspace, env);
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

	const oldClaudeHook = hookCommand(settings, 'SessionStart');
	writeFileSync(packedCLI, `${readFileSync(packedCLI, 'utf8')}\n// mixed-harness-upgrade\n`);
	const codexUpgrade = run(process.execPath, [packedCLI, 'connect', 'codex'], {
		cwd: workspace, env,
	});
	assert.match(codexUpgrade.stdout, /Codex plugin installed/);
	const upgradedSettings = JSON.parse(readFileSync(settingsPath, 'utf8'));
	const upgradedClaudeHook = hookCommand(upgradedSettings, 'SessionStart');
	assert.equal(upgradedClaudeHook, oldClaudeHook);
	const upgradedMcp = JSON.parse(readFileSync(fakeClaudeState, 'utf8'));
	assert.equal(upgradedMcp.args[0], mcpConfig.args[0]);
	for (const capturePath of [join(root, 'pulse', 'capture-state.json'), join(vaultDir, 'capture-state.json')]) {
		const state = JSON.parse(readFileSync(capturePath, 'utf8'));
		assert.equal(state.hosts.codex.enabled, true);
		assert.equal(state.hosts['claude-code'].enabled, true);
	}
	const daemonB = join(root, 'pulse-product-daemon-b');
	writeFileSync(daemonB, Buffer.concat([readFileSync(daemon), Buffer.from('\nPULSE_UPGRADE_B\n')]), { mode: 0o700 });
	chmodSync(daemonB, 0o700);
	const claudeUpgrade = run(process.execPath, [packedCLI, 'connect', 'claude-code'], {
		cwd: workspace, env: { ...env, PULSE_GO_BIN: daemonB }, timeout: 120_000,
	});
	assert.match(claudeUpgrade.stdout, /one bound vault, two connected harnesses/);
	const sharedMcp = JSON.parse(readFileSync(fakeClaudeState, 'utf8'));
	assert.equal(sharedMcp.env.PULSE_GO_BIN, undefined);
	assert.equal(upgradedMcp.env.PULSE_GO_BIN, undefined);
	const locator = JSON.parse(readFileSync(join(codexHome, 'pulse', 'product-locators.json'), 'utf8'));
	const locatorEntry = Object.values(locator.entries)[0];
	const upgradedRuntimeReceipt = JSON.parse(readFileSync(join(vaultDir, 'supervisor-runtime.json'), 'utf8'));
	const productDaemon = JSON.parse(readFileSync(join(root, 'pulse', 'runtime', 'product-daemon.json'), 'utf8'));
	assert.equal(locatorEntry.daemon_path, undefined, 'workspace locator must not carry daemon execution authority');
	assert.equal(productDaemon.schema, 'pulse.product_activation.v2');
	assert.equal(productDaemon.daemon_path, upgradedRuntimeReceipt.executable);
	assert.equal(productDaemon.daemon_digest, upgradedRuntimeReceipt.executable_digest);
	assert.match(productDaemon.runtime_tree_digest, /^[a-f0-9]{64}$/);

  writeFileSync(fakeClaudeState, JSON.stringify({
		...sharedMcp,
		env: { ...sharedMcp.env, PULSE_BINDING_PUBLIC_KEY_PATH: join(root, 'tampered.pub') },
  }));
  const tamperedDoctor = run(process.execPath, [packedCLI, 'doctor', 'claude-code', '--json'], {
    cwd: workspace, env, status: 1,
  });
  assert.equal(JSON.parse(tamperedDoctor.stdout).checks.mcp.ok, false);
	writeFileSync(fakeClaudeState, JSON.stringify(sharedMcp));

	const persisted = readFilesRecursively(vaultDir);
  for (const bytes of persisted) {
    assert.equal(bytes.includes(Buffer.from('raw prompt must not be stored anywhere')), false);
    assert.equal(bytes.includes(Buffer.from('raw assistant text must not be stored')), false);
  }

	const pluginRoot = dirname(pluginMcpPath);
	rmSync(pluginRoot, { recursive: true, force: true });
	const staleCodexMarkerConnect = run(process.execPath, [packedCLI, 'connect', 'claude-code'], {
		cwd: workspace, env: { ...env, PULSE_GO_BIN: daemonB }, timeout: 120_000,
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
		assert.match(`${wedgedConnect.stdout}${wedgedConnect.stderr}`, /did not become ready/);
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
  const codexAfterClaudeDisconnect = run(process.execPath, [runtimeCLI, 'codex-hook', 'SessionStart'], {
    cwd: nestedWorkspace, env, input: JSON.stringify({
      ...codexBase, session_id: 'session-codex-after-claude-disconnect',
      hook_event_name: 'SessionStart', source: 'resume',
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
  assert.equal(disconnectedReport.checks.mcp.ok, false);
  assert.equal(disconnectedReport.checks.capture.ok, false);

  process.stdout.write('Pulse Claude Code packed-product cross-harness E2E passed.\n');
} finally {
  try {
    const receipt = JSON.parse(readFileSync(join(root, 'vaults', 'personal', 'supervisor-runtime.json'), 'utf8'));
    process.kill(receipt.pid, 'SIGTERM');
  } catch { /* no running fixture */ }
  rmSync(root, { recursive: true, force: true });
}
