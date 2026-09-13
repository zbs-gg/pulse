import test from 'node:test';
import assert from 'node:assert/strict';
import { validateMomentItems, memoryMomentBody, compactMomentResult, expandMomentItems } from './memory-moment.js';
const context = { host: 'codex', session_id: 'session_test', turn_id: 'turn_test', source_event_key: 'event_test', idempotency_key: 'write_test', binding_digest: 'a'.repeat(64), policy_epoch: 0, resolver_epoch: 1 };
for (const size of [4, 21, 61]) test(`all ${size} meaningful parts survive construction`, () => {
  const items = Array.from({ length: size }, (_, i) => ({ kind: 'preference', scope: 'personal', summary: `Meaningful distinct part ${i} of a moment.` }));
  const body = memoryMomentBody({ items }, context);
  assert.equal(body.candidates.length, size);
  assert.deepEqual(body.candidates.map(x => x.capsule.items[0].redacted_summary), items.map(x => x.summary));
});
test('coexisting feelings retain names, cause and separate provenance', () => {
  const input = {    
kind: 'emotion', scope: 'personal', summary: 'Tender warmth and sadness coexist in this remembered moment.', emotions: [
      { label: 'trust', name: 'Tender warmth', source: 'user', intensity: 0.8, cause: 'An invitation to return home.' },
      { label: 'sadness', name: 'Bittersweet sadness', source: 'inferred', intensity: 0.4 }
    ]  
};
  const body = memoryMomentBody({ items: [input] }, context);
  assert.equal(body.candidates.length, 2);
  const events = body.candidates.map(x => x.semantic_delta.events[0]);
  assert.deepEqual(events.map(e => e.observed_label), ['Tender warmth', 'Bittersweet sadness']);
  assert.deepEqual(events.map(e => e.emotion_derivation), ['explicit', 'inferred']);
  assert.equal(events[0].trigger.confirmed, true);
  assert.equal(events[1].trigger, undefined);
  assert.ok(events.every(e => e.summary === input.summary));
});
test('long Russian summary splits without losing codepoints', () => {
  const summary = 'ТеплотаНежность🌿'.repeat(220);
  const parts = expandMomentItems([{ kind: 'preference', scope: 'personal', summary }]);
  assert.equal(parts.map(p => p.summary).join(''), summary);
  assert.ok(parts.every(p => Buffer.byteLength(p.summary) <= 1100));
});
test('a pending item or missing receipt cannot become stored', () => {
  const result = { ledger_id: 'ledger', moment_id: 'moment:' + 'a'.repeat(64), receipts: [{ status: 'created', object_id: 'one' }, { status: 'pending', candidate_id: 'two' }] };
  assert.equal(compactMomentResult(result, 2).status, 'partial');
  assert.deepEqual(compactMomentResult(result, 2).ids, ['one']);
  assert.equal(compactMomentResult({ ...result, receipts: result.receipts.slice(0, 1) }, 2).status, 'partial');
  assert.throws(() => compactMomentResult({ ledger_id: 'x', receipts: [] }, 1));
});
test('validate every part before writing; no selection by count', () => {
  assert.throws(() => validateMomentItems([{ kind: 'preference', scope: 'personal', summary: 'valid' }, { kind: 'emotion', scope: 'personal', summary: 'missing feelings' }]));
});
test('lost response replays identical request once; validation never retries', async () => {
  const { writeMemoryMoment } = await import('./memory-moment.js');
  const body = memoryMomentBody({ items: [{ kind: 'preference', scope: 'personal', summary: 'Keep this meaningful memory.' }] }, context);
  const calls = [];
  const answer = await writeMemoryMoment(async (path, options) => { calls.push(options.body); if (calls.length === 1) throw new Error('connection reset'); return { ok: true }; }, body);
  assert.deepEqual(answer, { ok: true }); assert.equal(calls.length, 2); assert.equal(calls[0], calls[1]);
  let attempts = 0;
  await assert.rejects(() => writeMemoryMoment(async () => { attempts++; throw Object.assign(new Error('invalid'), { status: 400 }); }, body));
  assert.equal(attempts, 1);
});
test('splitting cannot hide sensitive text spanning chunk boundaries', () => {
  assert.throws(() => memoryMomentBody({ items: [{ kind: 'preference', scope: 'personal', summary: 'a'.repeat(1098) + 'secret=' + 'a'.repeat(100) }] }, context));
});
