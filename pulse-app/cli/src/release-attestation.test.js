import assert from 'node:assert/strict';
import test from 'node:test';

import {
  contextQueryHasNoInfluence,
  exactTarballPulseInvocation,
  physicalHostLifecycleEvidence,
  unassignedAssignmentTurnRef,
} from './release-attestation.js';

test('physical attestation derives the exact Go-protected Unassigned turn reference', () => {
  assert.equal(
    unassignedAssignmentTurnRef('a'.repeat(64)),
    'turn:07f170a4518a07651e47c22799e808411ce177ba80a8db548f7d8b3ceec678a3',
  );
  assert.throws(() => unassignedAssignmentTurnRef('A'.repeat(64)), /digest is invalid/);
});

test('Beta trace proof rejects influence under any event ID, not only the Alpha object ID', () => {
  const empty = {
    schema_version: 'pulse.context.v1', facts: [], emotional_anchors: [], events: [], entities: [],
    relations: [], forbidden: [], private: [], uncertainty: [], importance_questions: [],
    trace: { retrieval: { event_ids: [], score_breakdowns: {}, project_memory: {} } },
  };
  assert.equal(contextQueryHasNoInfluence(empty), true);
  assert.equal(contextQueryHasNoInfluence({
    ...empty,
    trace: { retrieval: { event_ids: [987654], score_breakdowns: { 987654: { final_score: 0.2 } }, project_memory: {} } },
  }), false);
  assert.equal(contextQueryHasNoInfluence({
    ...empty, events: [{ id: 987654, summary: 'leaked under an unrelated ID' }],
  }), false);
});

test('physical commands stay on the exact absolute packed tarball', () => {
  assert.deepEqual(exactTarballPulseInvocation('/tmp/pulse-0.7.0.tgz', ['doctor', 'cursor', '--json']), [
    'exec', '--yes', '--package=/tmp/pulse-0.7.0.tgz', '--', 'pulse', 'doctor', 'cursor', '--json',
  ]);
  assert.throws(() => exactTarballPulseInvocation('pulse.tgz', ['doctor']), /packed invocation is invalid/);
});

test('physical v2 receipt lifecycle evidence is exact for every supported host', () => {
  assert.deepEqual(physicalHostLifecycleEvidence('claude-code'), {
    kind: 'claude_code_native_hooks', ready: true,
  });
  assert.deepEqual(physicalHostLifecycleEvidence('cursor'), {
    kind: 'cursor_native_hooks', ready: true,
  });
  assert.deepEqual(physicalHostLifecycleEvidence('codex'), {
    kind: 'codex_native_hooks', ready: true, native_hook_trusted: true, trusted_hook_observed: true,
  });
  assert.throws(() => physicalHostLifecycleEvidence('gemini'), /host is invalid/);
});
