import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { chmodSync, linkSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, realpathSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
	acquireVaultActivationLock,
	boundPulseRequest,
  consumeCodexToolLease,
	consumeHostToolLease,
	readRuntimeSecret,
	stageUnassignedProductCandidate,
	readProductActivation,
  readCodexTurnContext,
  writeCodexToolLease,
  writeCodexTurnContext,
	writeHostToolLease,
} from './codex-runtime.js';
import { readUnassignedInbox, unassignedInboxPath } from './unassigned-inbox.js';
import {
	activateArtifactVersion, canonicalArtifactJSON, readActivatedArtifact, writeActivatedArtifactSet,
} from './artifact-installer.js';

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

test('bound Personal request carries the exact signed project authority to one principal daemon', async (t) => {
	const dataDir = mkdtempSync(join(tmpdir(), 'pulse-bound-request.'));
	writeFileSync(join(dataDir, 'secret.key'), 'a'.repeat(64), { mode: 0o600 });
	let captured;
	const server = createServer((request, response) => {
		captured = request.headers;
		response.setHeader('Content-Type', 'application/json');
		response.end('{"ok":true}');
	});
	await new Promise((resolvePromise, reject) => {
		server.once('error', reject);
		server.listen(0, '127.0.0.1', resolvePromise);
	});
	t.after(() => server.close());
	const address = server.address();
	const workspace = '/workspace/eye';
	const resolved = {
		binding: {
			binding_digest: 'b'.repeat(64), resolver_epoch: 3,
			workspace: { canonical_path: workspace, repository_id: 'repository_eye' },
		},
		runtime: { data_dir: dataDir, base_url: `http://127.0.0.1:${address.port}` },
	};
	const response = await boundPulseRequest(resolved, '/continuity/resume', { body: {} });
	assert.deepEqual(response, { ok: true });
	assert.equal(captured['x-pulse-product-binding'], 'b'.repeat(64));
	assert.equal(captured['x-pulse-product-repository'], 'repository_eye');
	assert.equal(captured['x-pulse-product-resolver-epoch'], '3');
	assert.equal(
		Buffer.from(captured['x-pulse-product-workspace'], 'base64url').toString('utf8'),
		workspace,
	);
});

async function managedActivationFixture(dataDir, { presenceHelper = true } = {}) {
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
	].filter(([descriptor]) => presenceHelper || descriptor.kind !== 'presence-helper');
	for (const [descriptor, files] of fixtures) {
		const manifest = artifactTree(files);
		descriptor.tree_digest = createHash('sha256').update(canonicalArtifactJSON(manifest)).digest('hex');
		await activateArtifactVersion(descriptor, join(dataDir, 'unused'), {
			installRoot, treeManifest: manifest, testOnlyMaterializer: true,
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
		schema: 'pulse.verified_release_manifest.v2', manifest_digest: 'a'.repeat(64),
		version: '0.8.0', epoch: 8,
		artifacts: Object.fromEntries(fixtures.map(([descriptor]) => [descriptor.kind, descriptor])),
	};
	writeActivatedArtifactSet(release, { installRoot });
	const activations = Object.fromEntries(fixtures.map(([descriptor]) => [
		descriptor.kind,
		readActivatedArtifact(descriptor.id, { installRoot, expectedKind: descriptor.kind }),
	]));
	const identity = (activation) => ({
		activation_digest: activation.activation_digest,
		artifact_id: activation.artifact_id,
		artifact_sha256: activation.sha256,
		tree_digest: activation.tree_digest,
	});
	const daemonPath = join(activations.daemon.version_path, 'bin', 'pulse');
	const managed = {
		daemon: {
			...identity(activations.daemon),
			digest: createHash('sha256').update(readFileSync(daemonPath)).digest('hex'),
			path: daemonPath,
		},
		embedder_runtime: identity(activations['embedder-runtime']),
		model: identity(activations.model),
	};
	return { activations, managed, release };
}

test('product runtime stages only an exactly unassigned structured candidate', () => {
	const home = mkdtempSync(join(tmpdir(), 'pulse-runtime-unassigned.'));
	const path = unassignedInboxPath(home);
	const input = {
		schema: 'pulse.memory_capsule.v1',
		source: { host: 'codex', conversation_scope: 'current_turn', timestamp: '2026-07-17T10:00:00Z' },
		items: [{
			kind: 'decision', redacted_summary: 'Assign this only after choosing the exact project.', confidence: 1,
			evidence_hint: 'user_confirmed', privacy_tier: 'normal', retention: 'project', tags: ['pulse'],
		}],
		raw_input_included: false,
	};
	for (const reason of ['binding_missing', 'workspace_not_git']) {
		const receipt = stageUnassignedProductCandidate('codex', input, `request_${reason}`, {
			path,
			inspectBinding: () => ({ status: 'unassigned', reason }),
		});
		assert.equal(receipt.destination, 'unassigned_inbox');
	}
	assert.equal(readUnassignedInbox(path).items.length, 1);
	for (const inspection of [
		{ status: 'bound', binding: {} },
		{ status: 'unassigned', reason: 'binding_ambiguous' },
		{ status: 'unassigned', reason: 'binding_workspace_mismatch' },
	]) {
		assert.throws(
			() => stageUnassignedProductCandidate('codex', input, `request_${inspection.reason ?? 'bound'}`, {
				path, inspectBinding: () => inspection,
			}),
			/unassigned_staging_forbidden/,
		);
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

test('vault activation lock delegates to the portable lock service without a system utility', async () => {
	const dataDir = mkdtempSync(join(tmpdir(), 'pulse-portable-vault-lock.'));
	const calls = [];
	let released = false;
	const platformServices = {
		ensurePrivateDirectory(path) { calls.push(['directory', path]); },
		acquirePrivateLock(path, options) {
			calls.push(['lock', path, options]);
			return () => { released = true; };
		},
	};
	const release = await acquireVaultActivationLock({ data_dir: dataDir }, { platformServices });
	assert.deepEqual(calls, [
		['directory', dataDir],
		['lock', join(dataDir, 'supervisor-activation.lock'), { staleAfterMs: 30_000, timeoutMs: 3_000 }],
	]);
	await release();
	assert.equal(released, true);
});

test('runtime secret and tool leases reject hard-linked private state', () => {
	const dataDir = mkdtempSync(join(tmpdir(), 'pulse-runtime-hardlink.'));
	const secretPath = join(dataDir, 'secret.key');
	writeFileSync(secretPath, 'a'.repeat(64), { mode: 0o600 });
	linkSync(secretPath, join(dataDir, 'secret-copy.key'));
	assert.throws(() => readRuntimeSecret({ data_dir: dataDir }), /vault_secret_unsafe/);

	const { resolved, event } = fixture();
	const now = new Date('2026-07-14T10:00:00Z');
	const toolName = 'mcp__pulse-product__pulse_remember';
	const args = { schema: 'pulse.memory_capsule.v1', items: [], raw_input_included: false };
	writeCodexToolLease(resolved, event, toolName, args, 'tool-hardlink', now);
	const leaseRoot = join(resolved.runtime.data_dir, 'codex-tool-leases');
	const digestDirectory = join(leaseRoot, readdirSync(leaseRoot)[0]);
	const original = readdirSync(digestDirectory).find((name) => name.endsWith('.json'));
	linkSync(join(digestDirectory, original), join(digestDirectory, `${'f'.repeat(64)}.json`));
	assert.throws(() => consumeCodexToolLease(resolved, toolName, args, now), /host_tool_lease_unavailable/);
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

test('v4 product activation accepts a host-neutral release without optional presence and rejects plugin-runtime drift', async () => {
	const dataDir = mkdtempSync(join(tmpdir(), 'pulse-product-activation-v4.'));
	const runtimeRoot = join(dataDir, 'runtime', 'codex', 'current');
	const runtimePath = join(runtimeRoot, 'src', 'cli.js');
	mkdirSync(join(runtimeRoot, 'src'), { recursive: true, mode: 0o700 });
	const runtimeBytes = Buffer.from('#!/usr/bin/env node\n');
	writeFileSync(runtimePath, runtimeBytes, { mode: 0o700 });
	const runtimeDigest = createHash('sha256')
		.update('src/cli.js').update('\x00').update(runtimeBytes).update('\x00').digest('hex');
	const pluginTreeDigest = 'b'.repeat(64);
	const { activations, managed, release } = await managedActivationFixture(dataDir, { presenceHelper: false });
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

test('Claude MCP server name does not become part of the governed product action', () => {
  const { resolved, event } = fixture();
  const now = new Date('2026-07-14T10:00:00Z');
  const toolName = 'mcp__pulse__pulse_remember';
  const input = { schema: 'pulse.memory_capsule.v1', items: [], raw_input_included: false };
  writeHostToolLease(resolved, event, 'claude-code', toolName, input, 'tool-claude-name', now);
  const consumed = consumeHostToolLease(resolved, 'claude-code', toolName, input, now);
  assert.equal(consumed.tool_name, 'pulse_remember');
});

test('emotional graph writes use the same exact single-use product lease', () => {
  const { resolved, event } = fixture();
  const now = new Date('2026-07-14T10:00:00Z');
  const toolName = 'mcp__pulse-product__pulse_graph_delta';
  const input = {
    schema: 'pulse.semantic_delta.v1',
    source: { host: 'codex', conversation_scope: 'current_turn', timestamp: now.toISOString() },
    events: [{ client_id: 'event:emotion', title: 'A moment', summary: 'A short event.', emotions: { fear: 0.8 }, confidence: 0.9, privacy_tier: 'private' }],
    raw_input_included: false,
  };
  writeHostToolLease(resolved, event, 'codex', toolName, input, 'tool-graph', now);
  const consumed = consumeHostToolLease(resolved, 'codex', toolName, input, now);
  assert.equal(consumed.tool_name, 'pulse_graph_delta');
  assert.throws(() => consumeHostToolLease(resolved, 'codex', toolName, input, now), /unavailable/);
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
