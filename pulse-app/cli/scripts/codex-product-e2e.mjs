import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { generateKeyPairSync, sign } from 'node:crypto';
import {
  chmodSync,
	  existsSync,
	  lstatSync,
	  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
	  rmSync,
	  symlinkSync,
	  writeFileSync,
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
const repoRoot = resolve(cliRoot, '..', '..');
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

function writeSignedPersonalBindings(root, fixtures) {
  const supervisor = join(root, 'trust');
  mkdirSync(supervisor, { recursive: true, mode: 0o700 });
  const registryPath = join(supervisor, 'workspace-bindings.json');
  const publicKeyPath = join(supervisor, 'workspace-bindings.pub.pem');
  const anchorPath = join(supervisor, 'workspace-bindings.anchor.json');
  const payload = {
    schema: 'pulse.workspace-binding-registry.v1',
    epoch: 1,
		bindings: fixtures.map(({ workspace, port, suffix }) => ({
			binding_id: `binding_codex_e2e_${suffix}`,
			receipt_id: `receipt_codex_e2e_${suffix}`,
      resolver_epoch: 1,
      workspace: {
        workspace_id: workspace.workspace_id,
        repository_id: workspace.repository_id,
      },
      mode: 'personal',
			principal_ref: `principal_codex_e2e_${suffix}`,
			personal: {
				store_id: `store_personal_codex_e2e_${suffix}`,
				data_dir: join(root, 'vaults', `personal-${suffix}`),
				base_url: `http://127.0.0.1:${port}`,
				credential_ref: `local:pulse/codex-e2e-${suffix}`,
				cache_dir: join(root, 'caches', `personal-${suffix}`),
			},
		})),
  };
  const { privateKey, publicKey } = generateKeyPairSync('ed25519');
  const signature = sign(null, Buffer.from(canonicalJSONStringify(payload)), privateKey).toString('base64');
  const registryBytes = Buffer.from(JSON.stringify({ algorithm: 'ed25519', payload, signature }));
  writeFileSync(registryPath, registryBytes, { mode: 0o600 });
  writeFileSync(publicKeyPath, publicKey.export({ type: 'spki', format: 'pem' }), { mode: 0o600 });
  writeFileSync(anchorPath, `${canonicalJSONStringify(bindingRegistryAnchor(registryBytes, payload.epoch))}\n`, { mode: 0o600 });
  chmodSync(registryPath, 0o600);
  chmodSync(publicKeyPath, 0o600);
  return { registryPath, publicKeyPath, anchorPath };
}

function initializeRepository(path) {
  mkdirSync(path, { recursive: true });
  run('/usr/bin/git', ['init', '-q'], { cwd: path });
  run('/usr/bin/git', ['config', 'user.email', 'pulse-e2e@example.test'], { cwd: path });
  run('/usr/bin/git', ['config', 'user.name', 'Pulse E2E'], { cwd: path });
  run('/usr/bin/git', ['commit', '--allow-empty', '-q', '-m', 'fixture'], { cwd: path });
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

const root = mkdtempSync(join(tmpdir(), 'pulse-codex-product.'));
let runtimeStopped = false;
try {
  const home = join(root, 'home');
  const codexHome = join(root, 'codex');
  const workspace = join(root, 'workspace');
	const workspaceB = join(root, 'workspace-b');
  const installRoot = join(root, 'packed-cli');
  mkdirSync(home, { recursive: true });
  mkdirSync(codexHome, { recursive: true });
  mkdirSync(installRoot, { recursive: true });
  initializeRepository(workspace);
	initializeRepository(workspaceB);

  const port = await freePort();
	const portB = await freePort();
	const bindingPaths = writeSignedPersonalBindings(root, [
		{ workspace: canonicalizeWorkspace(workspace), port, suffix: 'a' },
		{ workspace: canonicalizeWorkspace(workspaceB), port: portB, suffix: 'b' },
	]);
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
    cwd: root,
    timeout: 120_000,
  });
  const packedCLI = join(installRoot, 'node_modules', '@zbs-gg', 'pulse', 'src', 'cli.js');
  const env = {
    ...process.env,
    HOME: home,
    CODEX_HOME: codexHome,
    PULSE_DATA_DIR: join(root, 'pulse'),
    PULSE_GO_BIN: daemon,
    PULSE_BINDING_REGISTRY_PATH: bindingPaths.registryPath,
    PULSE_BINDING_PUBLIC_KEY_PATH: bindingPaths.publicKeyPath,
    PULSE_BINDING_ANCHOR_PATH: bindingPaths.anchorPath,
    PULSE_TRUST_MODE: 'test',
    PULSE_CODEX_MARKETPLACE_SOURCE: repoRoot,
    PULSE_LOCAL_EMBED_PYTHON: process.execPath,
    PULSE_LOCAL_EMBED_HELPER: fakeEmbedHelper,
    PULSE_LOCAL_EMBED_MODEL: fakeEmbedModel,
  };

	const unhealthyDaemon = join(root, 'pulse-product-daemon-unhealthy.mjs');
	writeFileSync(unhealthyDaemon, `#!${process.execPath}
const fs = require('node:fs');
const http = require('node:http');
const value = (name) => process.argv[process.argv.indexOf(name) + 1];
const dataDir = value('-data-dir');
const [host, port] = value('-addr').split(':');
fs.mkdirSync(dataDir, { recursive: true, mode: 0o700 });
fs.writeFileSync(dataDir + '/secret.key', 'a'.repeat(64), { mode: 0o600 });
const server = http.createServer((_request, response) => { response.statusCode = 503; response.end('not ready'); });
server.listen(Number(port), host);
process.on('SIGTERM', () => server.close(() => process.exit(0)));
`, { mode: 0o700 });
	chmodSync(unhealthyDaemon, 0o700);
	const failedConnect = run(process.execPath, [packedCLI, 'connect', 'codex'], {
		cwd: workspace, env: { ...env, PULSE_GO_BIN: unhealthyDaemon }, status: 1, timeout: 30_000,
	});
	assert.match(`${failedConnect.stdout}${failedConnect.stderr}`, /did not become ready/);
	const pluginAfterFailure = run('codex', ['plugin', 'list', '--marketplace', 'zbs-gg'], { cwd: workspace, env });
	assert.match(pluginAfterFailure.stdout, /pulse@zbs-gg\s+not installed/);
	assert.equal(existsSync(join(root, 'pulse', 'runtime', 'codex', 'current')), false);
	assert.equal(existsSync(join(root, 'pulse', 'capture-state.json')), false);
	assert.equal(existsSync(join(codexHome, 'pulse', 'product-locators.json')), false);

  const connected = run(process.execPath, [packedCLI, 'connect', 'codex'], { cwd: workspace, env });
  assert.match(connected.stdout, /pulse@|Codex plugin installed/);

  const nativeMcp = run('codex', ['mcp', 'get', 'pulse-product', '--json'], { cwd: workspace, env });
  const nativeMcpConfig = JSON.parse(nativeMcp.stdout);
  assert.equal(nativeMcpConfig.transport.type, 'stdio');
  assert.equal(nativeMcpConfig.transport.command, 'node');
  assert.equal(nativeMcpConfig.transport.args[0], '${PLUGIN_ROOT}/mcp/server.mjs');
  assert.equal(nativeMcpConfig.transport.cwd ?? null, null);

  const cacheVersions = readdirSync(join(codexHome, 'plugins', 'cache', 'zbs-gg', 'pulse'));
  assert.equal(cacheVersions.length, 1);
  const pluginRoot = join(codexHome, 'plugins', 'cache', 'zbs-gg', 'pulse', cacheVersions[0]);
  const hook = join(pluginRoot, 'hooks', 'pulse-hook.mjs');
  const sessionID = 'session-codex-e2e';
  const freshHostEnv = Object.fromEntries(
		Object.entries(env).filter(([name]) => !name.startsWith('PULSE_') || name === 'PULSE_TRUST_MODE'),
  );
	const hookEnv = {
	  ...freshHostEnv,
	  PLUGIN_DATA: join(root, 'plugin-data'),
	};
	const activationPath = join(root, 'pulse', 'runtime', 'product-daemon.json');
	const activationBeforeMismatch = readFileSync(activationPath);
	const mismatchedActivation = JSON.parse(activationBeforeMismatch);
	mismatchedActivation.runtime_tree_digest = 'f'.repeat(64);
	writeFileSync(activationPath, JSON.stringify(mismatchedActivation), { mode: 0o600 });
	const rejectedMixedPair = run(process.execPath, [hook, 'SessionStart'], {
		cwd: workspace,
		env: hookEnv,
		status: 1,
		input: JSON.stringify({
			session_id: 'session-rejected-mixed-pair', cwd: workspace,
			hook_event_name: 'SessionStart', source: 'startup', permission_mode: 'default',
		}),
	});
	assert.match(`${rejectedMixedPair.stdout}${rejectedMixedPair.stderr}`, /runtime and activation are out of sync/);
	writeFileSync(activationPath, activationBeforeMismatch, { mode: 0o600 });
	const installedRuntimeCLI = join(root, 'pulse', 'runtime', 'codex', 'current', 'src', 'cli.js');
	const installedRuntimeCLIBytes = readFileSync(installedRuntimeCLI);
	const installedRuntimeCLIMode = lstatSync(installedRuntimeCLI).mode & 0o777;
	writeFileSync(installedRuntimeCLI, Buffer.concat([
		installedRuntimeCLIBytes, Buffer.from('\n// untrusted-runtime-tamper\n'),
	]));
	const rejectedTamperedRuntime = run(process.execPath, [hook, 'SessionStart'], {
		cwd: workspace,
		env: hookEnv,
		status: 1,
		input: JSON.stringify({
			session_id: 'session-rejected-runtime-tamper', cwd: workspace,
			hook_event_name: 'SessionStart', source: 'startup', permission_mode: 'default',
		}),
	});
	assert.match(`${rejectedTamperedRuntime.stdout}${rejectedTamperedRuntime.stderr}`, /runtime and activation are out of sync/);
	writeFileSync(installedRuntimeCLI, installedRuntimeCLIBytes, { mode: installedRuntimeCLIMode });

	const runtimeSymlinkTarget = join(root, 'untrusted-runtime-target.mjs');
	writeFileSync(runtimeSymlinkTarget, 'throw new Error("untrusted runtime executed");\n', { mode: 0o600 });
	rmSync(installedRuntimeCLI);
	symlinkSync(runtimeSymlinkTarget, installedRuntimeCLI);
	const rejectedRuntimeSymlink = run(process.execPath, [hook, 'SessionStart'], {
		cwd: workspace,
		env: hookEnv,
		status: 1,
		input: JSON.stringify({
			session_id: 'session-rejected-runtime-symlink', cwd: workspace,
			hook_event_name: 'SessionStart', source: 'startup', permission_mode: 'default',
		}),
	});
	assert.match(`${rejectedRuntimeSymlink.stdout}${rejectedRuntimeSymlink.stderr}`, /trusted runtime contains a symlink: src\/cli\.js/);
	rmSync(installedRuntimeCLI);
	writeFileSync(installedRuntimeCLI, installedRuntimeCLIBytes, { mode: installedRuntimeCLIMode });
	const syntheticWithoutHostOptIn = { ...hookEnv };
	delete syntheticWithoutHostOptIn.PULSE_TRUST_MODE;
	const rejectedSyntheticLaunch = run(process.execPath, [hook, 'SessionStart'], {
		cwd: workspace,
		env: syntheticWithoutHostOptIn,
		status: 1,
		input: JSON.stringify({
			session_id: 'session-rejected-synthetic', cwd: workspace,
			hook_event_name: 'SessionStart', source: 'startup', permission_mode: 'default',
		}),
	});
	assert.match(`${rejectedSyntheticLaunch.stdout}${rejectedSyntheticLaunch.stderr}`, /synthetic test locator requires an explicitly test-mode host process/);
  const sessionStart = run(process.execPath, [hook, 'SessionStart'], {
    cwd: workspace,
    env: hookEnv,
    input: JSON.stringify({
      session_id: sessionID,
      transcript_path: join(root, 'must-not-be-read.jsonl'),
      cwd: workspace,
      hook_event_name: 'SessionStart',
      model: 'gpt-5',
      source: 'startup',
      permission_mode: 'default',
    }),
  });
  const sessionOutput = JSON.parse(sessionStart.stdout);
  assert.equal(sessionOutput.continue, true);
  assert.match(sessionOutput.hookSpecificOutput.additionalContext, /pulse.context.v1/);

  const prompt = run(process.execPath, [hook, 'UserPromptSubmit'], {
    cwd: workspace,
    env: hookEnv,
    input: JSON.stringify({
      session_id: sessionID,
      turn_id: 'turn-codex-e2e',
      transcript_path: join(root, 'must-not-be-read.jsonl'),
      cwd: workspace,
      hook_event_name: 'UserPromptSubmit',
      model: 'gpt-5',
      permission_mode: 'default',
      prompt: 'must not be stored',
    }),
  });
  assert.equal(JSON.parse(prompt.stdout).continue, true);

  const preFinalizeStop = run(process.execPath, [hook, 'Stop'], {
    cwd: workspace,
    env: hookEnv,
    input: JSON.stringify({
      session_id: sessionID,
      turn_id: 'turn-codex-e2e',
      transcript_path: join(root, 'must-not-be-read.jsonl'),
      cwd: workspace,
      hook_event_name: 'Stop',
      model: 'gpt-5',
      permission_mode: 'default',
      stop_hook_active: false,
      last_assistant_message: 'must not be stored',
    }),
  });
  const preFinalizeOutput = JSON.parse(preFinalizeStop.stdout);
  assert.equal(preFinalizeOutput.decision, 'block');
  assert.match(preFinalizeOutput.reason, /bounded Pulse finalization pass/);

  const memoryArguments = {
    schema: 'pulse.memory_capsule.v1',
    source: { host: 'codex', conversation_scope: 'current_turn', timestamp: '2026-07-14T10:00:00Z' },
    items: [{
      kind: 'decision',
      redacted_summary: 'Use one trusted local runtime for Codex lifecycle memory.',
      confidence: 0.98,
      evidence_hint: 'current_turn',
      privacy_tier: 'normal',
      retention: 'project',
    }],
    raw_input_included: false,
  };
  const preTool = run(process.execPath, [hook, 'PreToolUse'], {
    cwd: workspace,
    env: hookEnv,
    input: JSON.stringify({
      session_id: sessionID,
      turn_id: 'turn-codex-e2e',
      transcript_path: join(root, 'must-not-be-read.jsonl'),
      cwd: workspace,
      hook_event_name: 'PreToolUse',
      model: 'gpt-5',
      permission_mode: 'default',
      tool_name: 'mcp__pulse-product__pulse_remember',
      tool_input: memoryArguments,
      tool_use_id: 'tool-codex-e2e-remember',
    }),
  });
  assert.deepEqual(JSON.parse(preTool.stdout), {});

  const mcpInput = [
    { jsonrpc: '2.0', id: 1, method: 'initialize', params: {
      protocolVersion: '2024-11-05', capabilities: {}, clientInfo: { name: 'pulse-e2e', version: '1' },
    } },
    { jsonrpc: '2.0', method: 'notifications/initialized', params: {} },
    { jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} },
    { jsonrpc: '2.0', id: 3, method: 'tools/call', params: {
      name: 'pulse_remember',
      arguments: memoryArguments,
    } },
  ].map((message) => JSON.stringify(message)).join('\n') + '\n';
  const mcp = run(process.execPath, [join(pluginRoot, 'mcp', 'server.mjs')], {
    cwd: workspace,
    env: hookEnv,
    input: mcpInput,
    timeout: 20_000,
  });
  const mcpMessages = mcp.stdout.trim().split(/\r?\n/).filter(Boolean).map((line) => JSON.parse(line));
  assert.equal(mcpMessages.find((message) => message.id === 1)?.result?.serverInfo?.name, 'pulse-mcp');
  assert.equal(Array.isArray(mcpMessages.find((message) => message.id === 2)?.result?.tools), true);
  const toolNames = mcpMessages.find((message) => message.id === 2).result.tools.map((tool) => tool.name);
  assert.equal(toolNames.includes('pulse_wipe'), false);
  assert.equal(toolNames.includes('pulse_forget'), false);
  assert.equal(toolNames.includes('pulse_tray'), true);
  const remembered = JSON.parse(mcpMessages.find((message) => message.id === 3).result.content[0].text);
  assert.equal(remembered.status, 'candidates');
  assert.equal(remembered.receipts[0].status, 'pending');
  assert.equal(remembered.receipts[0].safe_provenance.host, 'codex');
  assert.match(remembered.receipts[0].safe_provenance.session_id, /^session:[a-f0-9]{64}$/);
  assert.match(remembered.receipts[0].safe_provenance.turn_id, /^turn:[a-f0-9]{64}$/);

  const postTool = run(process.execPath, [hook, 'PostToolUse'], {
    cwd: workspace,
    env: hookEnv,
    input: JSON.stringify({
      session_id: sessionID,
      turn_id: 'turn-codex-e2e',
      transcript_path: join(root, 'must-not-be-read.jsonl'),
      cwd: workspace,
      hook_event_name: 'PostToolUse',
      model: 'gpt-5',
      permission_mode: 'default',
      tool_name: 'mcp__pulse-product__pulse_remember',
      tool_input: memoryArguments,
      tool_use_id: 'tool-codex-e2e-remember',
      tool_response: mcpMessages.find((message) => message.id === 3).result,
    }),
  });
  const postToolOutput = JSON.parse(postTool.stdout);
  assert.match(postToolOutput.systemMessage, new RegExp(remembered.receipts[0].receipt_id));
  assert.match(postToolOutput.systemMessage, /:pending/);

  const stop = run(process.execPath, [hook, 'Stop'], {
    cwd: workspace,
    env: hookEnv,
    input: JSON.stringify({
      session_id: sessionID,
      turn_id: 'turn-codex-e2e',
      transcript_path: join(root, 'must-not-be-read.jsonl'),
      cwd: workspace,
      hook_event_name: 'Stop',
      model: 'gpt-5',
      permission_mode: 'default',
      stop_hook_active: false,
      last_assistant_message: 'must not be stored',
    }),
  });
  const firstStopOutput = JSON.parse(stop.stdout);
  assert.deepEqual(firstStopOutput, {});

  const recursiveStop = run(process.execPath, [hook, 'Stop'], {
    cwd: workspace,
    env: hookEnv,
    input: JSON.stringify({
      session_id: sessionID,
      turn_id: 'turn-codex-e2e',
      transcript_path: join(root, 'must-not-be-read.jsonl'),
      cwd: workspace,
      hook_event_name: 'Stop',
      model: 'gpt-5',
      permission_mode: 'default',
      stop_hook_active: true,
      last_assistant_message: 'must not be stored',
    }),
  });
  assert.deepEqual(JSON.parse(recursiveStop.stdout), {});

  const doctor = run(process.execPath, [packedCLI, 'doctor', 'codex', '--json'], { cwd: workspace, env });
  const report = JSON.parse(doctor.stdout);
	assert.equal(report.verdict, 'Pulse Codex synthetic test lifecycle ready; production authority is not active.');
	assert.equal(report.trust.authority_mode, 'synthetic-test');
  assert.equal(Object.values(report.checks).every((check) => check.ok), true);
  assert.equal(report.trust.raw_transcript_capture, false);
  assert.equal(report.trust.full_retrieval, true);
  assert.equal(report.trust.external_embedding_api, false);

	run(process.execPath, [packedCLI, 'connect', 'codex'], { cwd: workspaceB, env, timeout: 120_000 });
	const receiptBv1 = JSON.parse(readFileSync(join(root, 'vaults', 'personal-b', 'supervisor-runtime.json'), 'utf8'));
	writeFileSync(packedCLI, `${readFileSync(packedCLI, 'utf8')}\n// multi-workspace-runtime-v2\n`);
	const daemonV2 = join(root, 'pulse-product-daemon-v2');
	writeFileSync(daemonV2, Buffer.concat([readFileSync(daemon), Buffer.from('\nPULSE_MULTI_WORKSPACE_V2\n')]), { mode: 0o700 });
	chmodSync(daemonV2, 0o700);
	run(process.execPath, [packedCLI, 'connect', 'codex'], {
		cwd: workspace, env: { ...env, PULSE_GO_BIN: daemonV2 }, timeout: 120_000,
	});
	const activationV2 = JSON.parse(readFileSync(join(root, 'pulse', 'runtime', 'product-daemon.json'), 'utf8'));
	const receiptAv2 = JSON.parse(readFileSync(join(root, 'vaults', 'personal-a', 'supervisor-runtime.json'), 'utf8'));
	assert.equal(receiptAv2.executable_digest, activationV2.daemon_digest);
	assert.notEqual(receiptBv1.executable_digest, activationV2.daemon_digest);
	const workspaceBResume = run(process.execPath, [hook, 'SessionStart'], {
		cwd: workspaceB,
		env: hookEnv,
		input: JSON.stringify({
			session_id: 'session-workspace-b-after-upgrade', cwd: workspaceB,
			hook_event_name: 'SessionStart', source: 'resume', model: 'gpt-5', permission_mode: 'default',
		}),
		timeout: 30_000,
	});
	assert.match(JSON.parse(workspaceBResume.stdout).hookSpecificOutput.additionalContext, /pulse.context.v1/);
	const receiptBv2 = JSON.parse(readFileSync(join(root, 'vaults', 'personal-b', 'supervisor-runtime.json'), 'utf8'));
	assert.equal(receiptBv2.executable_digest, activationV2.daemon_digest,
		'first hook in another workspace must reconcile its vault before reading memory');

  const runtimeReceipt = JSON.parse(readFileSync(join(root, 'vaults', 'personal-a', 'supervisor-runtime.json'), 'utf8'));
  process.kill(runtimeReceipt.pid, 'SIGSTOP');
  try {
    const hungDoctor = run(process.execPath, [packedCLI, 'doctor', 'codex', '--json'], {
      cwd: workspace, env, status: 1,
    });
    const hungReport = JSON.parse(hungDoctor.stdout);
    assert.equal(hungReport.checks.vault.ok, false);
    assert.match(hungReport.checks.vault.detail, /timeout|unavailable|pulse/i);
  } finally {
    process.kill(runtimeReceipt.pid, 'SIGCONT');
  }

	run(process.execPath, [packedCLI, 'disconnect', 'codex'], { cwd: workspaceB, env });
  run(process.execPath, [packedCLI, 'disconnect', 'codex'], { cwd: workspace, env });
  const disconnected = run(process.execPath, [packedCLI, 'doctor', 'codex', '--json'], {
    cwd: workspace, env, status: 1,
  });
  const disconnectedReport = JSON.parse(disconnected.stdout);
  assert.equal(disconnectedReport.checks.plugin.ok, false);
  assert.equal(disconnectedReport.checks.capture.ok, false);

  process.stdout.write('Pulse Codex packed-product E2E passed.\n');
} finally {
  if (!runtimeStopped) {
    const receiptPaths = [
		join(root, 'vaults', 'personal-a', 'supervisor-runtime.json'),
		join(root, 'vaults', 'personal-b', 'supervisor-runtime.json'),
    ];
    for (const path of receiptPaths) {
      try {
        const receipt = JSON.parse(readFileSync(path, 'utf8'));
        process.kill(receipt.pid, 'SIGTERM');
      } catch { /* no running fixture */ }
    }
  }
  rmSync(root, { recursive: true, force: true });
}
