import assert from 'node:assert/strict';
import {
  chmodSync, existsSync, linkSync, lstatSync, mkdirSync, mkdtempSync, readFileSync,
  symlinkSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import test from 'node:test';

import {
  readUnassignedInbox,
  stageUnassignedCapsule,
  unassignedInboxPath,
} from './unassigned-inbox.js';

function capsule(summary = 'Keep the exact project decision available after assignment.') {
  return {
    schema: 'pulse.memory_capsule.v1',
    source: { host: 'codex', conversation_scope: 'current_turn', timestamp: '2026-07-17T10:00:00Z' },
    items: [{
      kind: 'decision', redacted_summary: summary, confidence: 1,
      evidence_hint: 'current_turn', privacy_tier: 'normal', retention: 'project', tags: ['pulse'],
    }],
    raw_input_included: false,
  };
}

test('unbound structured memory stages in the private supervisor queue with an exact digest receipt', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-unassigned.'));
  const path = unassignedInboxPath(home);
  const result = stageUnassignedCapsule(capsule(), {
    path, host: 'codex', idempotencyKey: 'mcp_request_01', now: new Date('2026-07-17T10:00:00Z'),
  });
  assert.equal(result.schema, 'pulse.unassigned_stage_receipt.v1');
  assert.equal(result.status, 'staged');
  assert.equal(result.receipts.length, 1);
  assert.match(result.receipts[0].item_id, /^unassigned_[a-f0-9]{32}$/);
  assert.match(result.receipts[0].content_digest, /^[a-f0-9]{64}$/);
  assert.equal(result.receipts[0].destination, 'unassigned_inbox');

  const inbox = readUnassignedInbox(path);
  assert.equal(inbox.items.length, 1);
  assert.equal(inbox.items[0].content_digest, result.receipts[0].content_digest);
  assert.equal(inbox.items[0].candidate.capsule.items[0].redacted_summary,
    'Keep the exact project decision available after assignment.');
  assert.equal(inbox.items[0].candidate.capsule.source.host, 'codex');
  assert.deepEqual(Object.keys(inbox.receipts[0]).sort(), [
    'action', 'content_digest', 'created_at', 'item_id', 'receipt_id', 'status',
  ]);
  const info = lstatSync(path);
  assert.equal(info.mode & 0o077, 0);

  const retry = stageUnassignedCapsule(capsule(), {
    path, host: 'codex', idempotencyKey: 'mcp_request_01', now: new Date('2026-07-17T10:00:01Z'),
  });
  assert.deepEqual(retry, result);
  assert.equal(readUnassignedInbox(path).items.length, 1);

  const sameContentOtherRequest = stageUnassignedCapsule(capsule(), {
    path, host: 'codex', idempotencyKey: 'mcp_request_02', now: new Date('2026-07-17T10:00:02Z'),
  });
  assert.deepEqual(sameContentOtherRequest, result);
  assert.equal(readUnassignedInbox(path).items.length, 1);
});

test('unsafe secrets, transcript-shaped summaries, and path-like content are rejected before queue creation', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-unassigned-unsafe.'));
  const path = unassignedInboxPath(home);
  for (const summary of [
    'Use password=correct-horse-battery-staple for the service.',
    '/Users/alice/private/project/notes.md contains the decision.',
    Array.from({ length: 4 }, (_, index) => `User: ${index}\nAssistant: reply`).join('\n'),
  ]) {
    assert.throws(() => stageUnassignedCapsule(capsule(summary), {
      path, host: 'codex', idempotencyKey: `mcp_${summary.length}`,
    }), /unsafe|transcript|secret|path/i);
  }
  assert.equal(existsSync(path), false);
});

test('Home terminal receipts remain readable without retaining candidate content', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-unassigned-terminal.'));
  const path = unassignedInboxPath(home);
  const staged = stageUnassignedCapsule(capsule('Move this exact digest through Home.'), {
    path, host: 'codex', idempotencyKey: 'mcp_terminal_01', now: new Date('2026-07-17T10:00:00Z'),
  });
  const stored = JSON.parse(readFileSync(path, 'utf8'));
  const [item] = stored.items;
  stored.items = [];
  stored.receipts.push({
    receipt_id: `unassigned_receipt_${'b'.repeat(32)}`,
    item_id: item.item_id,
    content_digest: item.content_digest,
    action: 'assign', status: 'assigned', created_at: '2026-07-17T10:01:00Z',
    binding_digest: 'c'.repeat(64), repository_id: 'repository_pulse', store_id: 'store_personal_test',
  });
  writeFileSync(path, `${JSON.stringify(stored)}\n`, { mode: 0o600 });
  const terminal = readUnassignedInbox(path);
  assert.equal(terminal.items.length, 0);
  assert.equal(terminal.receipts.at(-1).status, 'assigned');
  assert.doesNotMatch(JSON.stringify(terminal.receipts), /Move this exact digest through Home/);
  assert.equal(staged.receipts[0].content_digest, item.content_digest);

  const retry = stageUnassignedCapsule(capsule('Move this exact digest through Home.'), {
    path, host: 'codex', idempotencyKey: 'mcp_terminal_01', now: new Date('2026-07-17T10:02:00Z'),
  });
  assert.equal(retry.status, 'assigned');
  assert.equal(retry.destination, 'memory_tray');
  assert.equal(retry.receipts[0].binding_digest, 'c'.repeat(64));
});

test('JS staging enforces the Go Vault UTF-8 byte limits before a card reaches Home', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-unassigned-unicode.'));
  const path = unassignedInboxPath(home);
  stageUnassignedCapsule(capsule('я'.repeat(600)), {
    path, host: 'codex', idempotencyKey: 'mcp_unicode_ok',
  });
  assert.throws(() => stageUnassignedCapsule(capsule('я'.repeat(601)), {
    path, host: 'codex', idempotencyKey: 'mcp_unicode_too_large',
  }), /too long/i);
});

test('unassigned queue refuses unsafe roots, symlinks, corruption, and over-capacity writes', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-unassigned-boundary.'));
  const path = unassignedInboxPath(home);
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  chmodSync(dirname(path), 0o755);
  assert.throws(() => stageUnassignedCapsule(capsule(), {
    path, host: 'codex', idempotencyKey: 'mcp_unsafe_root',
  }), /unsafe/);
  chmodSync(dirname(path), 0o700);
  writeFileSync(path, '{broken', { mode: 0o600 });
  assert.throws(() => readUnassignedInbox(path), /invalid/);
  assert.equal(readFileSync(path, 'utf8'), '{broken');

  const linkedHome = mkdtempSync(join(tmpdir(), 'pulse-unassigned-links.'));
  const linkedPath = unassignedInboxPath(linkedHome);
  stageUnassignedCapsule(capsule('Keep link defenses exact.'), {
    path: linkedPath, host: 'codex', idempotencyKey: 'mcp_link_base',
  });
  const hardlink = join(dirname(linkedPath), 'hardlink.json');
  linkSync(linkedPath, hardlink);
  assert.throws(() => readUnassignedInbox(linkedPath), /unsafe/);

  const symlinkHome = mkdtempSync(join(tmpdir(), 'pulse-unassigned-symlink.'));
  const symlinkPath = unassignedInboxPath(symlinkHome);
  mkdirSync(dirname(symlinkPath), { recursive: true, mode: 0o700 });
  symlinkSync(linkedPath, symlinkPath);
  assert.throws(() => readUnassignedInbox(symlinkPath), /unsafe/);

  const capacityHome = mkdtempSync(join(tmpdir(), 'pulse-unassigned-capacity.'));
  const capacityPath = unassignedInboxPath(capacityHome);
  for (let index = 0; index < 50; index += 1) {
    stageUnassignedCapsule(capsule(`Bounded queue item ${index}.`), {
      path: capacityPath, host: 'codex', idempotencyKey: `mcp_capacity_${index}`,
    });
  }
  assert.throws(() => stageUnassignedCapsule(capsule('This item exceeds the bounded queue.'), {
    path: capacityPath, host: 'codex', idempotencyKey: 'mcp_capacity_overflow',
  }), /full|capacity/i);
});

test('a non-contention lock creation failure returns immediately instead of retrying forever', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-unassigned-lock-error.'));
  const path = unassignedInboxPath(home);
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  chmodSync(dirname(path), 0o500);
  const started = Date.now();
  try {
    assert.throws(() => stageUnassignedCapsule(capsule(), {
      path, host: 'codex', idempotencyKey: 'mcp_lock_error',
    }), /lock cannot be created/i);
    assert.ok(Date.now() - started < 1000, 'non-EEXIST lock errors must not enter the contention loop');
  } finally {
    chmodSync(dirname(path), 0o700);
  }
});
