import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { chmodSync, mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
	acquireVaultActivationLock,
  callBoundTeamTool,
  consumeCodexToolLease,
	readProductActivation,
  readCodexTurnContext,
  writeCodexToolLease,
  writeCodexTurnContext,
} from './codex-runtime.js';

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

test('product activation admits only one exact runtime and daemon digest pair', () => {
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
	assert.equal(readProductActivation(dataDir).daemon_digest, activation.daemon_digest);
	writeFileSync(activationPath, JSON.stringify({ ...activation, extra_authority: true }), { mode: 0o600 });
	assert.throws(() => readProductActivation(dataDir), /product_activation_invalid/);
	writeFileSync(activationPath, JSON.stringify({ ...activation, daemon_digest: 'b'.repeat(64) }), { mode: 0o600 });
	assert.throws(() => readProductActivation(dataDir), /product_activation_daemon_digest_mismatch/);
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
