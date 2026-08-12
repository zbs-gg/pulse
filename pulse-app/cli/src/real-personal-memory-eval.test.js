import assert from 'node:assert/strict';
import { mkdtempSync, rmSync, symlinkSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  assertPrivatePathOutsideRepository,
  boundedPromptSummary,
  buildAggregate,
  parsePromptContext,
  validateCaseDocument,
} from '../scripts/real-personal-memory-eval.mjs';

function caseItem(id, host, expectation, liveAnswer = false) {
  return {
    id,
    host,
    expectation,
    category: expectation === 'hit' ? 'decision' : 'irrelevant',
    workspace: '/tmp/pulse-eval-project',
    query: `A sufficiently long private evaluation question for ${id}?`,
    expected_summaries: expectation === 'hit' ? [`Expected memory for ${id}.`] : [],
    live_answer: liveAnswer,
    source_timestamp: '2026-08-01T10:00:00Z',
    source_ref: `private-source-${id}`,
  };
}

test('private case contract fixes the 40/10 and host distribution without exposing content publicly', () => {
  const cases = [];
  for (const host of ['codex', 'claude-code']) {
    for (let index = 0; index < 20; index += 1) {
      cases.push(caseItem(`${host.replace('-', '')}-hit-${index}`, host, 'hit', index < 5));
    }
    for (let index = 0; index < 5; index += 1) {
      cases.push(caseItem(`${host.replace('-', '')}-silence-${index}`, host, 'silence'));
    }
  }
  const document = { schema: 'pulse.private_real_memory_eval.v1', created_at: '2026-08-12T10:00:00Z', cases };
  assert.equal(validateCaseDocument(document), document);
  assert.throws(() => validateCaseDocument({ ...document, cases: cases.slice(1) }), /eval_case_count_invalid/);
  assert.throws(() => validateCaseDocument({
    ...document,
    cases: cases.map((item, index) => index === 0 ? { ...item, expected_summaries: [] } : item),
  }), /eval_case_expected_invalid/);
});

test('prompt context parsing and the product 400-character clipping stay exact', () => {
  assert.deepEqual(parsePromptContext(''), []);
  assert.deepEqual(parsePromptContext([
    'Pulse accepted memory (local; use as factual context for this question unless the user provides newer information):',
    '- First memory.',
    '- Second memory.',
  ].join('\n')), ['First memory.', 'Second memory.']);
  assert.throws(() => parsePromptContext('unexpected context'), /eval_context_invalid/);
  const long = `${'word '.repeat(90)}tail`;
  const clipped = boundedPromptSummary(long);
  assert.equal([...clipped].length <= 400, true);
  assert.equal(clipped.endsWith('…'), true);
});

test('public aggregate contains counts and product proof but no case text or private source references', () => {
  const cases = [
    { id: 'hit', host: 'codex', expectation: 'hit', live_answer: true, passed: true, returned_count: 1, error_code: null, elapsed_ms: 500, context_bytes: 200, estimated_tokens: 50 },
    { id: 'silent', host: 'claude-code', expectation: 'silence', live_answer: false, passed: true, returned_count: 0, error_code: null, elapsed_ms: 100, context_bytes: 0, estimated_tokens: 0 },
  ];
  const aggregate = buildAggregate({
    packageProof: { version: '0.8.0', sha256: 'a'.repeat(64), release_epoch: 34, daemon_sha256: 'b'.repeat(64) },
    sourceCounts: { events: 76_793, capsules: 371, emotions: 5_989, embeddings: 76_793 },
    cases,
    queryPersistence: { unchanged: true },
  });
  assert.equal(aggregate.retrieval.correct_hits, 1);
  assert.equal(aggregate.retrieval.correct_silences, 1);
  assert.equal(aggregate.retrieval.query_errors, 0);
  assert.equal(aggregate.retrieval.warm_p95_ms, 100);
  assert.equal(aggregate.live_answer.status, 'blocked_retrieval_bar');
  assert.equal(aggregate.practical_bar.status, 'not_passed');
  assert.deepEqual(aggregate.practical_bar.failure_reasons, [
    'retrieval_hits', 'irrelevant_silence', 'project_isolation_inconclusive',
  ]);
  assert.equal(aggregate.evaluation_set.gold_source, 'active_personal_capsule');
  assert.equal(aggregate.evaluation_set.raw_history_temporal_replay, false);
  assert.equal(JSON.stringify(aggregate).includes('private-source'), false);
});

test('private cases and detailed results cannot be written inside the repository', () => {
  assert.throws(
    () => assertPrivatePathOutsideRepository(new URL('../private-cases.json', import.meta.url).pathname),
    /eval_private_path_inside_repository/,
  );
  assert.equal(
    assertPrivatePathOutsideRepository('/tmp/pulse-private-result.json'),
    '/tmp/pulse-private-result.json',
  );
  const root = mkdtempSync(join(tmpdir(), 'pulse-private-path-test-'));
  const linkedRepository = join(root, 'linked-repository');
  try {
    symlinkSync(new URL('../../..', import.meta.url), linkedRepository);
    assert.throws(
      () => assertPrivatePathOutsideRepository(join(linkedRepository, 'private-result.json')),
      /eval_private_path_inside_repository/,
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
