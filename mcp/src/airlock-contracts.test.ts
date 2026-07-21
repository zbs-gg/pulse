import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

import {
  AIRLOCK_ENVELOPE_ACTION,
  AIRLOCK_ENVELOPE_SCHEMA,
  canonicalAirlockEnvelope,
} from './airlock-contracts.js';

const CLIENT_KEY = 'a'.repeat(64);

function validEnvelope(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    schema: AIRLOCK_ENVELOPE_SCHEMA,
    action: AIRLOCK_ENVELOPE_ACTION,
    deployment_id: 'deployment_zbs',
    store_id: 'store_zbs',
    team_id: 'team_zbs',
    target_kind: 'commons',
    target_id: 'team_zbs',
    publication_key: 'publication_airlock_01',
    policy_epoch: 7,
    writer_principal_id: 'principal_nik',
    client_key: CLIENT_KEY,
    writer_id: 'writer_primary',
    source_timestamp: '2026-07-14T12:00:00.000Z',
    content: 'Use café.',
    metadata: { kind: 'decision', tags: ['pilot'] },
    ...overrides,
  };
}

test('Airlock produces one cross-runtime canonical envelope and digest', () => {
  const raw = JSON.stringify({
    writer_principal_id: 'principal_nik',
    metadata: { tags: ['pilot'], kind: 'decision' },
    content: 'Use cafe\u0301.',
    target_id: 'team_zbs',
    target_kind: 'commons',
    publication_key: 'publication_airlock_01',
    writer_id: 'writer_primary',
    source_timestamp: '2026-07-14T12:00:00.000Z',
    policy_epoch: 7,
    team_id: 'team_zbs',
    store_id: 'store_zbs',
    deployment_id: 'deployment_zbs',
    client_key: CLIENT_KEY,
    action: AIRLOCK_ENVELOPE_ACTION,
    schema: AIRLOCK_ENVELOPE_SCHEMA,
  });

  const result = canonicalAirlockEnvelope(raw);
  assert.equal(
    result.bytes,
    `{"action":"team.commons.publish","client_key":"${CLIENT_KEY}","content":"Use café.","deployment_id":"deployment_zbs","metadata":{"kind":"decision","tags":["pilot"]},"policy_epoch":7,"publication_key":"publication_airlock_01","schema":"pulse.team.airlock_envelope.v1","source_timestamp":"2026-07-14T12:00:00.000Z","store_id":"store_zbs","target_id":"team_zbs","target_kind":"commons","team_id":"team_zbs","writer_id":"writer_primary","writer_principal_id":"principal_nik"}`,
  );
  assert.equal(result.envelopeDigest, '251946d6d246694f10109a0c6de516686125b8795599b4e0b9ffb492d7bce54a');
  assert.deepEqual(result.value.metadata, { kind: 'decision', tags: ['pilot'] });
});

test('Airlock rejects duplicate and unknown fields at every level', () => {
  const raw = JSON.stringify(validEnvelope());
  const cases = [
    raw.replace('"schema":', '"source_object_id":"memory_private_12345678","schema":'),
    raw.replace('"schema":', '"schema":"pulse.team.airlock_envelope.v1","schema":'),
    raw.replace('"kind":"decision"', '"kind":"decision","kind":"fact"'),
    raw.replace('"tags":["pilot"]', '"tags":["pilot"],"source_session_id":"session_private_12345678"'),
  ];

  for (const candidate of cases) {
    assert.throws(() => canonicalAirlockEnvelope(candidate), /(?:canonical_|airlock_)/);
  }
});

test('Airlock requires every disclosure-bearing field and has no implicit defaults', () => {
  const topLevel = Object.keys(validEnvelope());
  for (const field of topLevel) {
    const candidate = validEnvelope();
    delete candidate[field];
    assert.throws(
      () => canonicalAirlockEnvelope(JSON.stringify(candidate)),
      /airlock_required_field/,
      field,
    );
  }

  for (const field of ['kind', 'tags']) {
    const candidate = validEnvelope();
    const metadata = { ...(candidate.metadata as Record<string, unknown>) };
    delete metadata[field];
    candidate.metadata = metadata;
    assert.throws(
      () => canonicalAirlockEnvelope(JSON.stringify(candidate)),
      /airlock_required_field/,
      `metadata.${field}`,
    );
  }
});

test('Airlock binds the exact action, Commons target, deployment, policy, client, and writer', () => {
  for (const candidate of [
    validEnvelope({ schema: 'pulse.team.airlock_envelope.v2' }),
    validEnvelope({ action: 'team.object.write' }),
    validEnvelope({ deployment_id: 'team_zbs' }),
    validEnvelope({ target_kind: 'desk' }),
    validEnvelope({ target_id: 'team_other' }),
    validEnvelope({ policy_epoch: 0 }),
    validEnvelope({ policy_epoch: 1.5 }),
    validEnvelope({ writer_principal_id: 'agent_nik' }),
    validEnvelope({ client_key: 'A'.repeat(64) }),
    validEnvelope({ writer_id: '/tmp/writer' }),
  ]) {
    assert.throws(() => canonicalAirlockEnvelope(JSON.stringify(candidate)), /(?:canonical_|airlock_)/);
  }

  const original = canonicalAirlockEnvelope(JSON.stringify(validEnvelope()));
  for (const changedBinding of [
    { deployment_id: 'deployment_other' },
    { store_id: 'store_other' },
    { team_id: 'team_other', target_id: 'team_other' },
    { policy_epoch: 8 },
    { writer_principal_id: 'principal_dima' },
    { client_key: 'b'.repeat(64) },
    { writer_id: 'writer_secondary' },
    { source_timestamp: '2026-07-14T12:00:01.000Z' },
    { publication_key: 'publication_airlock_02' },
  ]) {
    const changed = canonicalAirlockEnvelope(JSON.stringify(validEnvelope(changedBinding)));
    assert.notEqual(changed.envelopeDigest, original.envelopeDigest);
    assert.notEqual(changed.bytes, original.bytes);
  }
});

test('Airlock rejects private provenance, paths, secrets, controls, markup, and ambiguous Unicode', () => {
  const unsafe = [
    'Private source memory_1234567890 must not cross.',
    'Read /Users/nik/private-notes.md.',
    'Authorization: Bearer not-a-real-token.',
    '<script>alert(1)</script>',
    'line\nfeed',
    'Pulsе uses a Cyrillic e in a Latin token.',
    'Use Ｐulse with a fullwidth letter.',
    'direction\u202Eoverride',
  ];

  for (const content of unsafe) {
    assert.throws(
      () => canonicalAirlockEnvelope(JSON.stringify(validEnvelope({ content }))),
      /(?:canonical_|airlock_unsafe_text|airlock_ambiguous_unicode)/,
      content,
    );
  }
});

test('Airlock accepts ordinary NFC multilingual content but rejects unsafe metadata', () => {
  const safe = canonicalAirlockEnvelope(JSON.stringify(validEnvelope({
    content: 'Команда использует Pulse для решений.',
    metadata: { kind: 'decision', tags: ['Pulse', 'команда'] },
  })));
  assert.equal(safe.value.content, 'Команда использует Pulse для решений.');

  for (const metadata of [
    { kind: 'unknown', tags: [] },
    { kind: 'decision', tags: ['memory_1234567890'] },
    { kind: 'decision', tags: Array.from({ length: 21 }, (_, index) => `tag-${index}`) },
    { kind: 'decision', tags: ['duplicate', 'duplicate'] },
    { kind: 'decision', tags: ['zeta', 'alpha'] },
  ]) {
    assert.throws(
      () => canonicalAirlockEnvelope(JSON.stringify(validEnvelope({ metadata }))),
      /airlock_/,
    );
  }
});

test('Airlock matches the shared Go/TypeScript adversarial corpus', () => {
  const corpus = JSON.parse(readFileSync('../testdata/airlock-adversarial.json', 'utf8')) as Array<{
    name: string;
    content: string;
    tags: string[];
    accept: boolean;
  }>;
  for (const fixture of corpus) {
    const candidate = JSON.stringify(validEnvelope({
      content: fixture.content,
      metadata: { kind: 'decision', tags: fixture.tags },
    }));
    if (fixture.accept) {
      assert.doesNotThrow(() => canonicalAirlockEnvelope(candidate), fixture.name);
    } else {
      assert.throws(() => canonicalAirlockEnvelope(candidate), /(?:canonical_|airlock_)/, fixture.name);
    }
  }
});
