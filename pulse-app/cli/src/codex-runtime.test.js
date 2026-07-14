import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  consumeCodexToolLease,
  readCodexTurnContext,
  writeCodexToolLease,
  writeCodexTurnContext,
} from './codex-runtime.js';

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
