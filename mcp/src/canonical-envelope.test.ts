import assert from 'node:assert/strict';
import { test } from 'node:test';

import { canonicalizeEnvelopeJSON } from './canonical-envelope.js';

test('canonical envelope matches the Go golden bytes and digest', () => {
  const result = canonicalizeEnvelopeJSON(
    '{"schema":"pulse.airlock_envelope.v1","metadata":{"z":2,"a":1},"content":"caf\\u00e9"}',
    ['schema', 'content', 'metadata'],
  );
  assert.equal(result.bytes, '{"content":"café","metadata":{"a":1,"z":2},"schema":"pulse.airlock_envelope.v1"}');
  assert.equal(result.digest, 'sha256:56163ff416d5a5c84b2ecbe34b814f635a5973af615dd2051a2d01cf8eafdcbf');
});

test('canonical envelope rejects duplicate, unknown, control, and fractional values', () => {
  for (const raw of [
    '{"schema":"x","schema":"y"}',
    '{"schema":"x","team_id":"team-attacker"}',
    '{"schema":"x","content":"line\\nfeed"}',
    '{"schema":"x","count":1.25}',
    '{"schema":"x","metadata":{"скрыто":1}}',
  ]) {
    assert.throws(() => canonicalizeEnvelopeJSON(raw, ['schema', 'content', 'count', 'metadata']), /canonical_/);
  }
});
