// P0: the standalone (Safe Mode) store must enforce the SAME content contract
// as the Go daemon (internal/store/memory_capsule.go + semantic_delta.go).
// JSON Schema in tools/list is caller-advisory; the server must validate.
// These tests assert dangerous / out-of-contract payloads are REJECTED.
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';

import { StandaloneStore } from './standalone.js';

function tempDataDir(): string {
  return mkdtempSync(join(tmpdir(), 'pulse-validation-test-'));
}

function baseCapsule() {
  return {
    schema: 'pulse.memory_capsule.v1',
    source: {
      host: 'claude-code',
      conversation_scope: 'current_turn',
      timestamp: '2026-06-22T10:00:00Z',
    },
    items: [
      {
        kind: 'decision',
        redacted_summary: 'Picked SQLite for the local store.',
        confidence: 0.9,
        evidence_hint: 'current_turn',
        privacy_tier: 'normal',
        retention: 'project',
        tags: ['storage'],
      },
    ],
    raw_input_included: false as const,
  };
}

function withItem(patch: Record<string, unknown>) {
  const c = baseCapsule();
  c.items[0] = { ...c.items[0], ...patch } as typeof c.items[0];
  return c;
}

function baseDelta() {
  return {
    schema: 'pulse.semantic_delta.v1',
    source: {
      host: 'claude-code',
      conversation_scope: 'current_turn',
      timestamp: '2026-06-22T10:00:00Z',
    },
    nodes: [
      {
        client_id: 'n1',
        kind: 'project',
        canonical_name: 'Pulse',
        summary: 'The retrieval engine.',
        privacy_tier: 'normal',
        domain: 'real',
        salience: 0.5,
        emotional_weight: 0.2,
      },
    ],
    raw_input_included: false as const,
  };
}

// ---- pulse_remember: secret / path / transcript content ----

test('remember rejects an API-key secret in the summary', () => {
  const store = new StandaloneStore(tempDataDir());
  assert.throws(
    () => store.remember(withItem({ redacted_summary: 'token=sk-ABCDEF0123456789 leaked' })),
    /secret|path/i,
  );
});

test('remember rejects an absolute filesystem path in the summary', () => {
  const store = new StandaloneStore(tempDataDir());
  assert.throws(
    () => store.remember(withItem({ redacted_summary: 'see /Users/nik/.pulse/secret.key' })),
    /secret|path/i,
  );
});

test('remember rejects transcript-like summary', () => {
  const store = new StandaloneStore(tempDataDir());
  const transcript = `${'user: hi\nassistant: hello\n'.repeat(3)}`;
  assert.throws(() => store.remember(withItem({ redacted_summary: transcript })), /transcript/i);
});

test('a rejected secret capsule leaves nothing recallable', () => {
  const dir = tempDataDir();
  const store = new StandaloneStore(dir);
  assert.throws(() => store.remember(withItem({ redacted_summary: 'api_key sk-DEADBEEF00112233' })));
  const recalled = store.recall({ query: 'api_key sk-DEADBEEF00112233' });
  assert.equal(recalled.items.length, 0);
});

// ---- pulse_remember: enums / limits ----

test('remember rejects an unsupported host', () => {
  const store = new StandaloneStore(tempDataDir());
  const c = baseCapsule();
  c.source.host = 'evil-host';
  assert.throws(() => store.remember(c), /host/i);
});

test('remember rejects an unsupported conversation_scope', () => {
  const store = new StandaloneStore(tempDataDir());
  const c = baseCapsule();
  c.source.conversation_scope = 'whatever';
  assert.throws(() => store.remember(c), /scope/i);
});

test('remember rejects a non-RFC3339 timestamp', () => {
  const store = new StandaloneStore(tempDataDir());
  const c = baseCapsule();
  c.source.timestamp = 'last tuesday';
  assert.throws(() => store.remember(c), /timestamp|rfc3339/i);
});

test('remember rejects more than 20 items', () => {
  const store = new StandaloneStore(tempDataDir());
  const c = baseCapsule();
  c.items = Array.from({ length: 21 }, () => ({ ...baseCapsule().items[0] }));
  assert.throws(() => store.remember(c), /20|too many/i);
});

test('remember rejects an unsupported kind', () => {
  const store = new StandaloneStore(tempDataDir());
  assert.throws(() => store.remember(withItem({ kind: 'rumor' })), /kind/i);
});

test('remember rejects confidence out of 0..1', () => {
  const store = new StandaloneStore(tempDataDir());
  assert.throws(() => store.remember(withItem({ confidence: 99 })), /confidence/i);
});

test('remember rejects an over-long summary', () => {
  const store = new StandaloneStore(tempDataDir());
  assert.throws(() => store.remember(withItem({ redacted_summary: 'a'.repeat(1201) })), /too long/i);
});

test('remember rejects an unsupported privacy_tier', () => {
  const store = new StandaloneStore(tempDataDir());
  assert.throws(() => store.remember(withItem({ privacy_tier: 'public' })), /privacy_tier/i);
});

test('remember rejects an unsupported retention', () => {
  const store = new StandaloneStore(tempDataDir());
  assert.throws(() => store.remember(withItem({ retention: 'forever' })), /retention/i);
});

test('remember rejects a secret hidden in a tag', () => {
  const store = new StandaloneStore(tempDataDir());
  assert.throws(() => store.remember(withItem({ tags: ['api_key'] })), /tag/i);
});

test('remember rejects an unsafe tag charset', () => {
  const store = new StandaloneStore(tempDataDir());
  assert.throws(() => store.remember(withItem({ tags: ['bad tag!'] })), /tag/i);
});

test('remember still accepts a clean capsule and it is recallable', () => {
  const store = new StandaloneStore(tempDataDir());
  const saved = store.remember(baseCapsule());
  assert.equal(saved.ok, true);
  assert.equal(saved.ids.length, 1);
  const recalled = store.recall({ query: 'SQLite local store' });
  assert.equal(recalled.items.length, 1);
});

// ---- pulse_graph_delta ----

test('graph_delta rejects a secret in a node summary', () => {
  const store = new StandaloneStore(tempDataDir());
  const d = baseDelta();
  d.nodes[0].summary = 'password: hunter2 at /Users/nik';
  assert.throws(() => store.graphDelta(d), /secret|path/i);
});

test('graph_delta rejects an unsupported node kind', () => {
  const store = new StandaloneStore(tempDataDir());
  const d = baseDelta();
  d.nodes[0].kind = 'banana';
  assert.throws(() => store.graphDelta(d), /kind/i);
});

test('graph_delta rejects an unsupported node privacy_tier', () => {
  const store = new StandaloneStore(tempDataDir());
  const d = baseDelta();
  d.nodes[0].privacy_tier = 'public';
  assert.throws(() => store.graphDelta(d), /privacy_tier/i);
});

test('graph_delta rejects more than 30 nodes', () => {
  const store = new StandaloneStore(tempDataDir());
  const d = baseDelta();
  d.nodes = Array.from({ length: 31 }, (_, i) => ({ ...baseDelta().nodes[0], client_id: `n${i}` }));
  assert.throws(() => store.graphDelta(d), /30|too many/i);
});

test('graph_delta rejects a secret in continuity summary', () => {
  const store = new StandaloneStore(tempDataDir());
  const d = baseDelta() as Record<string, unknown>;
  d.continuity = { summary: 'shipped; api_key sk-0011223344556677 in env' };
  assert.throws(() => store.graphDelta(d), /secret|path/i);
});

test('graph_delta does not persist arbitrary unknown fields on a node', () => {
  const dir = tempDataDir();
  const store = new StandaloneStore(dir);
  const d = baseDelta() as Record<string, unknown>;
  (d.nodes as Array<Record<string, unknown>>)[0].evil_raw_blob = 'token=sk-AAAABBBBCCCCDDDD';
  store.graphDelta(d);
  const persisted = readFileSync(store.path(), 'utf8');
  assert.equal(persisted.includes('evil_raw_blob'), false);
  assert.equal(persisted.includes('sk-AAAABBBBCCCCDDDD'), false);
});

test('graph_delta accepts structured assertion fields on a clean fact', () => {
  const dir = tempDataDir();
  const store = new StandaloneStore(dir);
  const d = baseDelta() as Record<string, unknown>;
  d.events = [
    {
      client_id: 'event:pulse-store',
      title: 'Pulse store decision',
      summary: 'Pulse should keep the canonical memory store local-first.',
      entity_refs: ['n1'],
      confidence: 0.91,
      privacy_tier: 'private',
      domain: 'real',
    },
  ];
  d.facts = [
    {
      node: 'n1',
      text: 'Pulse canonical memory store is local-first.',
      predicate: 'canonical memory store',
      object_text: 'local-first Pulse store',
      valid_from: '2026-06-30T00:00:00Z',
      source_event_refs: ['event:pulse-store'],
      scope_type: 'project',
      scope_id: 'garden',
      visibility: 'private',
      confidence: 0.91,
      privacy_tier: 'private',
      domain: 'real',
    },
  ];

  const out = store.graphDelta(d) as { ok: boolean; facts_upserted: number };
  assert.equal(out.ok, true);
  assert.equal(out.facts_upserted, 1);
  const persisted = readFileSync(store.path(), 'utf8');
  assert.ok(persisted.includes('"predicate": "canonical memory store"'));
  assert.ok(persisted.includes('"object_text": "local-first Pulse store"'));
  assert.ok(persisted.includes('"source_event_refs": ['));
  assert.ok(persisted.includes('"event:pulse-store"'));
});

test('graph_delta rejects unsafe structured assertion fields', () => {
  const store = new StandaloneStore(tempDataDir());
  const d = baseDelta() as Record<string, unknown>;
  d.facts = [
    {
      node: 'n1',
      text: 'Pulse canonical memory store is local-first.',
      predicate: 'canonical memory store',
      object_text: 'see /Users/nik/.pulse/secret.key',
      confidence: 0.91,
      privacy_tier: 'private',
      domain: 'real',
    },
  ];

  assert.throws(() => store.graphDelta(d), /object_text|secret|path/i);
});

test('graph_delta rejects non-personal assertion scope without scope_id', () => {
  const store = new StandaloneStore(tempDataDir());
  const d = baseDelta() as Record<string, unknown>;
  d.facts = [
    {
      node: 'n1',
      text: 'Pulse canonical memory store is local-first.',
      predicate: 'canonical memory store',
      object_text: 'local-first Pulse store',
      scope_type: 'project',
      confidence: 0.91,
      privacy_tier: 'private',
      domain: 'real',
    },
  ];

  assert.throws(() => store.graphDelta(d), /scope_id/i);
});

test('graph_delta rejects assertion source_event_refs that do not match events', () => {
  const store = new StandaloneStore(tempDataDir());
  const d = baseDelta() as Record<string, unknown>;
  d.events = [
    {
      client_id: 'event:pulse-store',
      title: 'Pulse store decision',
      summary: 'Pulse should keep the canonical memory store local-first.',
      entity_refs: ['n1'],
      confidence: 0.91,
      privacy_tier: 'private',
      domain: 'real',
    },
  ];
  d.facts = [
    {
      node: 'n1',
      text: 'Pulse canonical memory store is local-first.',
      predicate: 'canonical memory store',
      object_text: 'local-first Pulse store',
      source_event_refs: ['event:missing'],
      confidence: 0.91,
      privacy_tier: 'private',
      domain: 'real',
    },
  ];

  assert.throws(() => store.graphDelta(d), /source_event_refs|unknown event/i);
});

test('graph_delta rejects duplicate event client ids before source refs can become ambiguous', () => {
  const store = new StandaloneStore(tempDataDir());
  const d = baseDelta() as Record<string, unknown>;
  d.events = [
    {
      client_id: 'event:pulse-store',
      title: 'Pulse store decision',
      summary: 'Pulse should keep the canonical memory store local-first.',
      entity_refs: ['n1'],
      confidence: 0.91,
      privacy_tier: 'private',
      domain: 'real',
    },
    {
      client_id: 'event:pulse-store',
      title: 'Duplicate Pulse store decision',
      summary: 'A duplicate client id would make source_event_refs ambiguous.',
      entity_refs: ['n1'],
      confidence: 0.91,
      privacy_tier: 'private',
      domain: 'real',
    },
  ];

  assert.throws(() => store.graphDelta(d), /events\[1\]\.client_id is duplicate/);
});

test('graph_delta still accepts a clean delta', () => {
  const store = new StandaloneStore(tempDataDir());
  const out = store.graphDelta(baseDelta()) as { ok: boolean; nodes_upserted: number };
  assert.equal(out.ok, true);
  assert.equal(out.nodes_upserted, 1);
});
