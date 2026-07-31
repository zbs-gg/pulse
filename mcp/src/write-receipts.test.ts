import assert from 'node:assert/strict';
import test from 'node:test';

import { assertTruthfulDeletionReceipt, assertTruthfulWriteResponse, mcpRequestIdempotencyKey } from './write-receipts.js';

function receipt(overrides: Record<string, unknown> = {}) {
  return {
    schema: 'pulse.write_receipt.v1', receipt_id: 'receipt_01', ledger_id: 'turn_01',
		candidate_id: 'candidate_01', candidate_version: 1, status: 'pending', destination: 'personal',
		destination_store_id: 'store_personal_01',
    safe_provenance: {
      host: 'codex', session_id: 'session_01', turn_id: 'turn_01',
      source_event_key: 'codex:session_01:turn_01:stop',
    },
    content_digest: 'a'.repeat(64), policy_epoch: 2, resolver_epoch: 3,
    measurement_method: 'host_structured_v1', created_at: '2026-07-14T09:00:00Z',
    ...overrides,
  };
}

function result(item = receipt(), overrides: Record<string, unknown> = {}) {
  return {
    ledger_id: 'turn_01', status: 'candidates',
    finalize_receipt: {
      schema: 'pulse.turn_finalize_receipt.v1', receipt_id: 'receipt_finalize_01', ledger_id: 'turn_01',
			status: 'candidates', destination: 'personal', destination_store_id: 'store_personal_01',
      safe_provenance: {
        host: 'codex', session_id: 'session_01', turn_id: 'turn_01',
        source_event_key: 'codex:session_01:turn_01:stop',
      },
      policy_epoch: 2, resolver_epoch: 3, created_at: '2026-07-14T09:00:00Z',
    },
    receipts: [item], ...overrides,
  };
}

test('accepts a truthful pending receipt with no canonical object ID', () => {
  assert.doesNotThrow(() => assertTruthfulWriteResponse(result()));
});

test('rejects pending that lies by returning a canonical object ID', () => {
  assert.throws(() => assertTruthfulWriteResponse(result(receipt({ object_id: 'pulse:already-saved' }))), /object_id_forbidden/);
});

test('requires canonical object ID only for materialized statuses', () => {
  assert.doesNotThrow(() => assertTruthfulWriteResponse(result(receipt({ status: 'created', object_id: 'pulse:01' }))));
  assert.throws(() => assertTruthfulWriteResponse(result(receipt({ status: 'created' }))), /object_id_required/);
});

test('accepts explicit Local Preview result shapes without relabeling them', () => {
  assert.doesNotThrow(() => assertTruthfulWriteResponse({ ok: true, ids: ['pulse:01'] }));
  assert.doesNotThrow(() => assertTruthfulWriteResponse({ ok: true, events_inserted: 1 }));
});

test('rejects loose preview and malformed product receipt shapes', () => {
  assert.throws(() => assertTruthfulWriteResponse({ ok: true, ids: [] }), /invalid IDs/);
  assert.throws(() => assertTruthfulWriteResponse({ ok: true, ids: ['pulse:01'], saved: true }), /unexpected fields/);
  assert.throws(() => assertTruthfulWriteResponse({ ok: true, events_inserted: -1 }), /invalid counts/);
  assert.throws(() => assertTruthfulWriteResponse(result(receipt({ policy_epoch: -1 }))), /malformed/);
  assert.throws(() => assertTruthfulWriteResponse(result(receipt({ content_digest: 'A'.repeat(64) }))), /lacks candidate/);
  assert.throws(() => assertTruthfulWriteResponse(result(receipt({ created_at: 'yesterday' }))), /malformed/);
  assert.throws(() => assertTruthfulWriteResponse(result(receipt({ surprise: true }))), /unexpected fields/);
});

test('rejects an unreceipted generic saved claim', () => {
  assert.throws(() => assertTruthfulWriteResponse({ ok: true, saved: true }), /no durable receipt/);
});

test('accepts only a canonical updated receipt for deletion', () => {
  const deleted = receipt({
    status: 'updated', object_id: 'pulse:01', reason_code: 'user_deleted',
  });
  assert.doesNotThrow(() => assertTruthfulDeletionReceipt(deleted));
  assert.doesNotThrow(() => assertTruthfulDeletionReceipt({ ok: true, deleted_id: 'pulse:01' }, 'pulse:01'));
  assert.throws(() => assertTruthfulDeletionReceipt({ ok: true, deleted_id: 'pulse:02' }, 'pulse:01'), /mismatch/);
  assert.throws(() => assertTruthfulDeletionReceipt({ ok: true }), /receipt_schema|malformed|invalid_receipt/);
  assert.throws(() => assertTruthfulDeletionReceipt(receipt({ status: 'canceled', reason_code: 'user_deleted' })), /updated receipt/);
});

test('MCP request identity produces a stable non-content idempotency key', () => {
  const first = mcpRequestIdempotencyKey('session-01', 42);
  assert.equal(first, mcpRequestIdempotencyKey('session-01', 42));
  assert.notEqual(first, mcpRequestIdempotencyKey('session-01', 43));
  assert.notEqual(first, mcpRequestIdempotencyKey('session-02', 42));
  assert.match(first, /^mcp_[a-f0-9]{64}$/);
});
