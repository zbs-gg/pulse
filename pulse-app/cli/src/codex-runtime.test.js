import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
	acquireVaultActivationLock,
  callBoundLocalProductTool,
  callBoundTeamTool,
  consumeCodexToolLease,
	readProductActivation,
  readCodexTurnContext,
  writeCodexToolLease,
  writeCodexTurnContext,
} from './codex-runtime.js';
import { activateArtifactVersion, readActivatedArtifact, writeActivatedArtifactSet } from './artifact-installer.js';
import { resolveManagedRuntime } from './local-supervisor.js';

function artifactDescriptor(id, kind, sha256) {
	return {
		id, kind, sha256, version: '0.8.0', epoch: 8, bytes: 1,
		origin: 'https://releases.zbs.gg', url: `https://releases.zbs.gg/${id}`,
		...(kind === 'model' ? { model_policy: { data_only: true, custom_code: false } } : {}),
	};
}

function artifactTree(files) {
	return {
		schema: 'pulse.artifact_tree.v1',
		files: files.map(([path, bytes, mode]) => ({
			path, bytes: bytes.length, mode, executable: (mode & 0o111) !== 0,
			sha256: createHash('sha256').update(bytes).digest('hex'),
		})),
	};
}

function tinySafetensors() {
	const header = Buffer.from(JSON.stringify({ embedding: { dtype: 'F32', shape: [1], data_offsets: [0, 4] } }));
	const prefix = Buffer.alloc(8);
	prefix.writeBigUInt64LE(BigInt(header.length));
	return Buffer.concat([prefix, header, Buffer.alloc(4)]);
}

async function managedActivationFixture(dataDir) {
	const installRoot = join(dataDir, 'artifacts');
	const fixtures = [
		[artifactDescriptor('pulse-daemon', 'daemon', '1'.repeat(64)), [['bin/pulse', Buffer.from('#!/bin/sh\n'), 0o700]]],
		[artifactDescriptor('pulse-embedder-runtime', 'embedder-runtime', '2'.repeat(64)), [
			['runtime/bin/python3.12', Buffer.from('#!/bin/sh\n'), 0o700],
			['helper.py', Buffer.from('helper\n'), 0o600],
			['support/config.json', Buffer.from('{}\n'), 0o600],
			['support/tokenizer.json', Buffer.from('{}\n'), 0o600],
		]],
		[artifactDescriptor('pulse-model', 'model', '3'.repeat(64)), [['model.safetensors', tinySafetensors(), 0o600]]],
		[artifactDescriptor('pulse-plugin-runtime', 'plugin-runtime', '4'.repeat(64)), [
			['marketplace/plugins/pulse/.codex-plugin/plugin.json', Buffer.from('{}\n'), 0o600],
			['runtime/src/cli.js', Buffer.from('#!/usr/bin/env node\n'), 0o600],
		]],
		[artifactDescriptor('pulse-presence-helper', 'presence-helper', '5'.repeat(64)), [
			['bin/gg.zbs.pulse.presence-helper', Buffer.from('#!/bin/sh\n'), 0o700],
		]],
	];
	for (const [descriptor, files] of fixtures) {
		await activateArtifactVersion(descriptor, join(dataDir, 'unused'), {
			installRoot, treeManifest: artifactTree(files), testOnlyMaterializer: true,
			materialize: async (_source, target) => {
				for (const [path, bytes, mode] of files) {
					const destination = join(target, path);
					mkdirSync(join(destination, '..'), { recursive: true, mode: 0o700 });
					writeFileSync(destination, bytes, { mode });
					chmodSync(destination, mode);
				}
			},
		});
	}
	const release = {
		schema: 'pulse.verified_release_manifest.v1', manifest_digest: 'a'.repeat(64),
		version: '0.8.0', epoch: 8,
		artifacts: Object.fromEntries(fixtures.map(([descriptor]) => [descriptor.kind, descriptor])),
	};
	writeActivatedArtifactSet(release, { installRoot });
	const activations = Object.fromEntries(fixtures.map(([descriptor]) => [
		descriptor.kind,
		readActivatedArtifact(descriptor.id, { installRoot, expectedKind: descriptor.kind }),
	]));
	const vault = { data_dir: join(dataDir, 'vault') };
	const managed = resolveManagedRuntime(vault, {
		installRoot,
		verifiedActivations: {
			 daemon: activations.daemon,
			 embedderRuntime: activations['embedder-runtime'],
			 model: activations.model,
		},
	});
	return { activations, managed, release };
}

test('installed runtime proxies only read-only Team tools through the exact current binding', async () => {
	const binding = {
		mode: 'team', fallback: false, binding_digest: 'a'.repeat(64), resolver_epoch: 7,
		principal_ref: 'principal_signed',
		workspace: { repository_id: 'repository_signed' },
		commons: {
			project_id: 'project_signed', resource: 'https://pulse.example.test/mcp',
			credential_ref: 'credential',
		},
	};
	const resolution = { binding: { binding_digest: binding.binding_digest, resolver_epoch: 7 } };
	let called;
	const result = await callBoundTeamTool(resolution, 'codex', 'pulse_team_resume', { schema: 'x' }, {
		resolveBinding: () => binding,
		teamRequest: async (...args) => { called = args; return { schema: 'pulse.team.resume_result.v1' }; },
	});
	assert.equal(result.schema, 'pulse.team.resume_result.v1');
	assert.equal(called[0], binding);
	assert.equal(called[1], 'pulse_team_resume');
	await assert.rejects(
		callBoundTeamTool(resolution, 'codex', 'pulse_team_remember', {}, { resolveBinding: () => binding }),
		/product_team_tool_forbidden/,
	);
	await assert.rejects(
		callBoundTeamTool(resolution, 'codex', 'pulse_team_resume', {}, {
			resolveBinding: () => ({ ...binding, binding_digest: 'b'.repeat(64) }),
		}),
		/product_team_binding_changed/,
	);
});

test('read-only Team proxy injects signed active context without mutating Codex or Claude input', async () => {
	const binding = {
		mode: 'team', fallback: false, binding_digest: 'a'.repeat(64), resolver_epoch: 7,
		principal_ref: 'principal_signed',
		workspace: { repository_id: 'repository_signed' },
		commons: {
			project_id: 'project_signed', resource: 'https://pulse.example.test/mcp',
			credential_ref: 'credential',
		},
	};
	const resolution = { binding: { binding_digest: binding.binding_digest, resolver_epoch: 7 } };
	for (const host of ['codex', 'claude-code']) {
		const input = {
			schema: 'pulse.team.recall.v1', query: 'signed context', privacy_ceiling: 'normal',
			active_context: { session_id: `session_${host.replace('-', '_')}` },
		};
		const original = structuredClone(input);
		let proxied;
		await callBoundTeamTool(resolution, host, 'pulse_team_recall', input, {
			resolveBinding: () => binding,
			teamRequest: async (_binding, _name, request) => { proxied = request; return {}; },
		});
		assert.deepEqual(input, original);
		assert.notEqual(proxied, input);
		assert.notEqual(proxied.active_context, input.active_context);
		assert.deepEqual(proxied.active_context, {
			project_id: 'project_signed', repo_id: 'repository_signed',
			agent_id: 'principal_signed', session_id: `session_${host.replace('-', '_')}`,
		});
	}
});

test('read-only Team proxy rejects signed-project drift and fixed-context spoofing before transport', async () => {
	const binding = {
		mode: 'team', fallback: false, binding_digest: 'a'.repeat(64), resolver_epoch: 7,
		principal_ref: 'principal_signed',
		workspace: { repository_id: 'repository_signed' },
		commons: {
			project_id: 'project_signed', resource: 'https://pulse.example.test/mcp',
			credential_ref: 'credential',
		},
	};
	const resolution = { binding: { binding_digest: binding.binding_digest, resolver_epoch: 7 } };
	for (const [field, value] of [
		['project_id', 'project_other_grant'],
		['repo_id', 'repository_drifted'],
		['agent_id', 'principal_spoofed'],
	]) {
		let calls = 0;
		await assert.rejects(
			callBoundTeamTool(resolution, 'codex', 'pulse_team_resume', {
				schema: 'pulse.team.resume.v1', active_context: { [field]: value, session_id: 'session_agent' },
			}, {
				resolveBinding: () => binding,
				teamRequest: async () => { calls += 1; return {}; },
			}),
			/product_team_context_mismatch/,
		);
		assert.equal(calls, 0, `${field} drift reached Team transport`);
	}
});

test('connect and lazy reconciliation serialize on one vault activation lock', async () => {
	const dataDir = mkdtempSync(join(tmpdir(), 'pulse-vault-activation-lock.'));
	const runtime = { data_dir: dataDir };
	const releaseFirst = await acquireVaultActivationLock(runtime);
	try {
		await assert.rejects(acquireVaultActivationLock(runtime), /vault_activation_lock_(?:failed|timeout)/);
	} finally {
		await releaseFirst();
	}
	const releaseSecond = await acquireVaultActivationLock(runtime);
	await releaseSecond();
});

test('legacy v2 product activation is explicitly non-ready', () => {
	const dataDir = mkdtempSync(join(tmpdir(), 'pulse-product-activation-test.'));
	const runtimeRoot = join(dataDir, 'runtime', 'codex', 'current');
	const runtimePath = join(runtimeRoot, 'src', 'cli.js');
	const daemonPath = join(dataDir, 'bin', 'pulse-product-daemon-test');
	mkdirSync(join(runtimeRoot, 'src'), { recursive: true, mode: 0o700 });
	mkdirSync(join(dataDir, 'bin'), { recursive: true, mode: 0o700 });
	mkdirSync(join(dataDir, 'runtime'), { recursive: true, mode: 0o700 });
	writeFileSync(runtimePath, '#!/usr/bin/env node\n', { mode: 0o700 });
	const runtimeDigest = 'a'.repeat(64);
	writeFileSync(join(runtimeRoot, 'runtime-manifest.json'), JSON.stringify({
		schema: 'pulse.codex_runtime.v1', tree_digest: runtimeDigest, entrypoint: 'src/cli.js',
	}), { mode: 0o600 });
	writeFileSync(daemonPath, '#!/bin/sh\nexit 0\n', { mode: 0o700 });
	chmodSync(daemonPath, 0o700);
	const activationPath = join(dataDir, 'runtime', 'product-daemon.json');
	const activation = {
		schema: 'pulse.product_activation.v2', runtime_path: runtimePath,
		runtime_tree_digest: runtimeDigest, daemon_path: daemonPath,
		daemon_digest: createHash('sha256').update('#!/bin/sh\nexit 0\n').digest('hex'),
		activated_at: '2026-07-14T10:00:00Z',
	};
	writeFileSync(activationPath, JSON.stringify(activation), { mode: 0o600 });
	assert.throws(
		() => readProductActivation(dataDir),
		/product_activation_v2_not_ready/,
		'legacy activation must require repair instead of silently restarting without managed retrieval',
	);
});

test('legacy v3 product activation is explicitly non-ready because it omits the signed Codex edge', async () => {
	const dataDir = mkdtempSync(join(tmpdir(), 'pulse-product-activation-v3.'));
	const runtimeRoot = join(dataDir, 'runtime', 'codex', 'current');
	const runtimePath = join(runtimeRoot, 'src', 'cli.js');
	mkdirSync(join(runtimeRoot, 'src'), { recursive: true, mode: 0o700 });
	const runtimeBytes = Buffer.from('#!/usr/bin/env node\n');
	writeFileSync(runtimePath, runtimeBytes, { mode: 0o700 });
	const runtimeDigest = createHash('sha256')
		.update('src/cli.js').update('\x00').update(runtimeBytes).update('\x00').digest('hex');
	writeFileSync(join(runtimeRoot, 'runtime-manifest.json'), JSON.stringify({
		schema: 'pulse.codex_runtime.v1', tree_digest: runtimeDigest, entrypoint: 'src/cli.js',
	}), { mode: 0o600 });
	const { managed } = await managedActivationFixture(dataDir);
	const activation = {
		activated_at: '2026-07-15T10:00:00Z',
		daemon_activation_digest: managed.daemon.activation_digest,
		daemon_artifact_id: managed.daemon.artifact_id,
		daemon_artifact_sha256: managed.daemon.artifact_sha256,
		daemon_digest: managed.daemon.digest,
		// macOS reports /var artifact roots as /private/var when a trusted
		// executable is canonicalized by the supervisor.
		daemon_path: realpathSync(managed.daemon.path),
		daemon_tree_digest: managed.daemon.tree_digest,
		embedder_runtime_activation_digest: managed.embedder_runtime.activation_digest,
		embedder_runtime_artifact_id: managed.embedder_runtime.artifact_id,
		embedder_runtime_artifact_sha256: managed.embedder_runtime.artifact_sha256,
		embedder_runtime_tree_digest: managed.embedder_runtime.tree_digest,
		model_activation_digest: managed.model.activation_digest,
		model_artifact_id: managed.model.artifact_id,
		model_artifact_sha256: managed.model.artifact_sha256,
		model_tree_digest: managed.model.tree_digest,
		runtime_path: runtimePath,
		runtime_tree_digest: runtimeDigest,
		schema: 'pulse.product_activation.v3',
	};
	const activationPath = join(dataDir, 'runtime', 'product-daemon.json');
	writeFileSync(activationPath, JSON.stringify(activation), { mode: 0o600 });

	assert.throws(
		() => readProductActivation(dataDir),
		/product_activation_v3_not_ready/,
	);
});

test('v4 product activation binds the complete signed release and rejects plugin-runtime drift', async () => {
	const dataDir = mkdtempSync(join(tmpdir(), 'pulse-product-activation-v4.'));
	const runtimeRoot = join(dataDir, 'runtime', 'codex', 'current');
	const runtimePath = join(runtimeRoot, 'src', 'cli.js');
	mkdirSync(join(runtimeRoot, 'src'), { recursive: true, mode: 0o700 });
	const runtimeBytes = Buffer.from('#!/usr/bin/env node\n');
	writeFileSync(runtimePath, runtimeBytes, { mode: 0o700 });
	const runtimeDigest = createHash('sha256')
		.update('src/cli.js').update('\x00').update(runtimeBytes).update('\x00').digest('hex');
	const pluginTreeDigest = 'b'.repeat(64);
	const { activations, managed, release } = await managedActivationFixture(dataDir);
	const pluginRuntime = activations['plugin-runtime'];
	writeFileSync(join(runtimeRoot, 'runtime-manifest.json'), JSON.stringify({
		schema: 'pulse.codex_runtime.v2', tree_digest: runtimeDigest, entrypoint: 'src/cli.js',
		package_version: release.version, installed_at: '2026-07-15T10:00:00Z',
		release_manifest_digest: release.manifest_digest, release_version: release.version, release_epoch: release.epoch,
		plugin_runtime_artifact_id: pluginRuntime.artifact_id,
		plugin_runtime_artifact_sha256: pluginRuntime.sha256,
		plugin_runtime_activation_digest: pluginRuntime.activation_digest,
		plugin_runtime_tree_digest: pluginRuntime.tree_digest,
		plugin_tree_digest: pluginTreeDigest,
	}), { mode: 0o600 });
	const activation = {
		activated_at: '2026-07-15T10:00:00Z',
		daemon_activation_digest: managed.daemon.activation_digest,
		daemon_artifact_id: managed.daemon.artifact_id,
		daemon_artifact_sha256: managed.daemon.artifact_sha256,
		daemon_digest: managed.daemon.digest,
		daemon_path: realpathSync(managed.daemon.path),
		daemon_tree_digest: managed.daemon.tree_digest,
		embedder_runtime_activation_digest: managed.embedder_runtime.activation_digest,
		embedder_runtime_artifact_id: managed.embedder_runtime.artifact_id,
		embedder_runtime_artifact_sha256: managed.embedder_runtime.artifact_sha256,
		embedder_runtime_tree_digest: managed.embedder_runtime.tree_digest,
		model_activation_digest: managed.model.activation_digest,
		model_artifact_id: managed.model.artifact_id,
		model_artifact_sha256: managed.model.artifact_sha256,
		model_tree_digest: managed.model.tree_digest,
		plugin_runtime_activation_digest: pluginRuntime.activation_digest,
		plugin_runtime_artifact_id: pluginRuntime.artifact_id,
		plugin_runtime_artifact_sha256: pluginRuntime.sha256,
		plugin_runtime_tree_digest: pluginRuntime.tree_digest,
		plugin_tree_digest: pluginTreeDigest,
		release_epoch: release.epoch,
		release_manifest_digest: release.manifest_digest,
		release_version: release.version,
		runtime_path: runtimePath,
		runtime_tree_digest: runtimeDigest,
		schema: 'pulse.product_activation.v4',
	};
	const activationPath = join(dataDir, 'runtime', 'product-daemon.json');
	writeFileSync(activationPath, JSON.stringify(activation), { mode: 0o600 });
	assert.equal(readProductActivation(dataDir).release_manifest_digest, release.manifest_digest);
	writeFileSync(activationPath, JSON.stringify({
		...activation, plugin_runtime_tree_digest: 'f'.repeat(64),
	}), { mode: 0o600 });
	assert.throws(
		() => readProductActivation(dataDir),
		/product_activation_(?:runtime_digest|artifact_identity)_mismatch/,
	);
});

function fixture() {
  const dataDir = mkdtempSync(join(tmpdir(), 'pulse-codex-runtime-test.'));
  mkdirSync(dataDir, { recursive: true, mode: 0o700 });
  return {
    resolved: {
      binding: {
        binding_digest: 'a'.repeat(64),
        resolver_epoch: 7,
        workspace: { canonical_path: '/workspace/pulse' },
      },
      runtime: { data_dir: dataDir },
    },
    event: {
      session_id: 'session-test',
      turn_id: 'turn-test',
      source_event_key: `event_${'b'.repeat(64)}`,
      idempotency_key: `lifecycle:${'c'.repeat(64)}`,
    },
  };
}

test('Codex turn context is bound to the exact current turn event', () => {
  const { resolved, event } = fixture();
  const now = new Date('2026-07-14T10:00:00Z');
  writeCodexTurnContext(resolved, event, now);
  assert.equal(readCodexTurnContext(resolved, event, now).turn_id, event.turn_id);
  assert.throws(
    () => readCodexTurnContext(resolved, { ...event, turn_id: 'turn-other' }, now),
    /stale/,
  );
});

test('Codex tool lease joins stdio MCP without CODEX_THREAD_ID and is single-use', () => {
  const { resolved, event } = fixture();
  const now = new Date('2026-07-14T10:00:00Z');
  const toolName = 'mcp__pulse-product__pulse_remember';
  const args = { schema: 'pulse.memory_capsule.v1', items: [], raw_input_included: false };
  writeCodexToolLease(resolved, event, toolName, args, 'tool-use-1', now);

  const consumed = consumeCodexToolLease(resolved, toolName, {
    raw_input_included: false,
    items: [],
    schema: 'pulse.memory_capsule.v1',
  }, now);
  assert.equal(consumed.session_id, event.session_id);
  assert.equal(consumed.turn_id, event.turn_id);
  assert.throws(() => consumeCodexToolLease(resolved, toolName, args, now), /unavailable/);
});

test('bound local source registration consumes a host lease and sends metadata only', async () => {
  const { resolved, event } = fixture();
  resolved.binding.workspace = {
    canonical_path: resolved.runtime.data_dir,
    repository_id: 'repository_local_tools',
  };
  resolved.runtime.base_url = 'http://127.0.0.1:18801';
  const now = new Date('2026-07-16T10:00:00Z');
  const toolName = 'mcp__pulse-product__pulse_source_register';
  const input = { locator: 'notes/team.md' };
  writeCodexToolLease(resolved, event, toolName, input, 'tool-source-register', now);
  const calls = [];
  const result = await callBoundLocalProductTool(resolved, 'codex', 'pulse_source_register', input, {
    now,
    resolveBinding: () => resolved.binding,
    runtimeFromBinding: () => resolved.runtime,
    portableProjectID: () => 'project_0123456789abcdef0123456789abcdef',
    readSourceWindow: () => ({
      schema: 'pulse.project_source.window.v1', portable_project_id: 'project_0123456789abcdef0123456789abcdef',
      repository_id: 'repository_local_tools', source_kind: 'repository_text', locator: 'notes/team.md',
      version_digest: 'b'.repeat(64), byte_count: 88, cursor: 0, next_cursor: 1,
      status: 'complete', content: 'private source bytes', withheld: [],
    }),
    request: async (_resolved, path, options) => {
      calls.push({ path, options });
      return { schema: 'pulse.project_source.register.v1', source_id: 'source_local_01' };
    },
  });
  assert.equal(result.source_id, 'source_local_01');
  assert.equal(calls[0].path, '/project/sources/register');
  assert.equal(calls[0].options.body.locator, 'notes/team.md');
  assert.doesNotMatch(JSON.stringify(calls), /private source bytes|content|withheld/);
  await assert.rejects(
    callBoundLocalProductTool(resolved, 'codex', 'pulse_source_register', input, {
      now, resolveBinding: () => resolved.binding, runtimeFromBinding: () => resolved.runtime,
    }),
    /host_tool_lease_unavailable/,
  );
});

test('Codex tool lease rejects argument changes and expires after 30 seconds', () => {
  const { resolved, event } = fixture();
  const now = new Date('2026-07-14T10:00:00Z');
  const toolName = 'mcp__pulse-product__pulse_remember';
  const args = { schema: 'pulse.memory_capsule.v1', items: [], raw_input_included: false };
  writeCodexToolLease(resolved, event, toolName, args, 'tool-use-2', now);
  assert.throws(
    () => consumeCodexToolLease(resolved, toolName, { ...args, raw_input_included: true }, now),
    /unavailable/,
  );
  assert.throws(
    () => consumeCodexToolLease(resolved, toolName, args, new Date(now.valueOf() + 31_000)),
    /unavailable/,
  );
});

test('unrelated stale lease debris cannot hide a valid exact-argument lease', () => {
  const { resolved, event } = fixture();
  const root = join(resolved.runtime.data_dir, 'codex-tool-leases');
  mkdirSync(root, { recursive: true, mode: 0o700 });
  for (let index = 0; index < 300; index += 1) {
    writeFileSync(join(root, `${String(index).padStart(64, '0')}.json`), '{}\n', { mode: 0o600 });
  }
  const now = new Date('2026-07-14T10:00:00Z');
  const toolName = 'mcp__pulse-product__pulse_remember';
  const args = { schema: 'pulse.memory_capsule.v1', items: [], raw_input_included: false };
  writeCodexToolLease(resolved, event, toolName, args, 'tool-use-after-debris', now);
  assert.equal(consumeCodexToolLease(resolved, toolName, args, now).turn_id, event.turn_id);
});

test('same-turn exact-argument retry supersedes its abandoned lease', () => {
  const { resolved, event } = fixture();
  const first = new Date('2026-07-14T10:00:00.000Z');
  const retry = new Date('2026-07-14T10:00:00.100Z');
  const toolName = 'mcp__pulse-product__pulse_remember';
  const args = { schema: 'pulse.memory_capsule.v1', items: [], raw_input_included: false };
  writeCodexToolLease(resolved, event, toolName, args, 'tool-abandoned', first);
  writeCodexToolLease(resolved, event, toolName, args, 'tool-retry', retry);
  assert.equal(consumeCodexToolLease(resolved, toolName, args, retry).issued_at, retry.toISOString());
  assert.throws(() => consumeCodexToolLease(resolved, toolName, args, retry), /unavailable/);
});

test('identical arguments from different turns remain ambiguous and fail closed', () => {
  const { resolved, event } = fixture();
  const now = new Date('2026-07-14T10:00:00Z');
  const toolName = 'mcp__pulse-product__pulse_remember';
  const args = { schema: 'pulse.memory_capsule.v1', items: [], raw_input_included: false };
  writeCodexToolLease(resolved, event, toolName, args, 'tool-turn-one', now);
  writeCodexToolLease(resolved, {
    ...event,
    turn_id: 'turn-other',
    source_event_key: `event_${'d'.repeat(64)}`,
    idempotency_key: `lifecycle:${'e'.repeat(64)}`,
  }, toolName, args, 'tool-turn-two', now);
  assert.throws(() => consumeCodexToolLease(resolved, toolName, args, now), /ambiguous/);
});
