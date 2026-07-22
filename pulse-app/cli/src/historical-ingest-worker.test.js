import assert from 'node:assert/strict';
import test from 'node:test';

import { runHistoricalIngestWorker } from './historical-ingest-worker.js';

const digest = 'a'.repeat(64);

function lease() {
  return {
    schema: 'pulse.historical_ingest.worker_lease.v1',
    job_id: 'job_0123456789abcdef',
    unit: {
      id: 'unit_a', root_id: 'root_a', snapshot_digest: digest,
      evidence_digest: 'b'.repeat(64), source_aliases: ['source_0123456789abcdef'], ordinal: 0,
    },
    lease_token: `lease_${'c'.repeat(32)}`,
    checkpoint_generation: 2,
    expires_at: '2026-07-22T12:00:00Z',
    source_snapshot_digest: digest,
    runner_contract_digest: 'd'.repeat(64),
    trusted_prompt: 'extract bounded memory',
    evidence: 'private normalized evidence',
  };
}

test('worker sends normalized evidence only to runner stdin and submits structured result before acknowledgement', async () => {
  const calls = [];
  let leased = false;
  const request = async (method, path, body) => {
    calls.push({ method, path, body });
    if (path.endsWith('/lease')) {
      if (leased) return { done: true, status: { state: 'manifest_ready', accepted_units: 1 } };
      leased = true;
      return lease();
    }
    return { checkpoint_generation: 3, result_digest: 'e'.repeat(64) };
  };
  const result = await runHistoricalIngestWorker({
    jobID: 'job_0123456789abcdef', request,
    qualification: { live_model_qualified: true, contract_digest: 'd'.repeat(64) },
    runUnit: async (options) => {
      assert.equal(options.evidence, 'private normalized evidence');
      assert.equal(options.prompt, 'extract bounded memory');
      await options.acceptResult({
        schema_version: 'https://zbs.gg/schemas/pulse/historical-ingest/v1',
        job_id: 'job_0123456789abcdef', revision: 1, source_snapshot_digest: digest, items: [],
      }, {
        usage: { input_tokens: 100, cached_input_tokens: 20, output_tokens: 10, reasoning_output_tokens: 2 },
      });
      return { output_digest: 'e'.repeat(64) };
    },
  });
  const submit = calls.find((call) => call.path.endsWith('/submit'));
  assert.equal(submit.body.result.work_unit_id, 'unit_a');
  assert.equal(submit.body.result.zero_material, true);
  assert.equal(submit.body.usage.reasoning_tokens, 2);
  assert.deepEqual(result, { state: 'manifest_ready', accepted_units: 1, processed_units: 1 });
  assert.doesNotMatch(JSON.stringify(result), /private normalized evidence|extract bounded memory/);
});

test('quota pauses the exact uncommitted unit and returns no evidence', async () => {
  const calls = [];
  const result = await runHistoricalIngestWorker({
    jobID: 'job_0123456789abcdef',
    request: async (method, path, body) => {
      calls.push({ method, path, body });
      if (path.endsWith('/lease')) return lease();
      return { state: 'paused_quota', accepted_units: 0 };
    },
    qualification: { live_model_qualified: true, contract_digest: 'd'.repeat(64) },
    runUnit: async () => { const error = new Error('paused_quota'); error.code = 'paused_quota'; throw error; },
  });
  assert.deepEqual(result, { state: 'paused_quota', accepted_units: 0, processed_units: 0 });
  assert.equal(calls.at(-1).path.endsWith('/quota'), true);
  assert.equal(calls.at(-1).body.unit_id, 'unit_a');
});

test('worker refuses mismatched job, snapshot, qualification, and lease shape before model invocation', async () => {
  let invoked = false;
  for (const mutate of [
    (value) => { value.job_id = 'job_fedcba9876543210'; },
    (value) => { value.unit.snapshot_digest = 'f'.repeat(64); },
    (value) => { value.evidence = '/Users/example/private.jsonl'; },
  ]) {
    await assert.rejects(() => runHistoricalIngestWorker({
      jobID: 'job_0123456789abcdef',
      request: async () => { const value = lease(); mutate(value); return value; },
      qualification: { live_model_qualified: true, contract_digest: 'd'.repeat(64) },
      runUnit: async () => { invoked = true; },
    }));
  }
  assert.equal(invoked, false);
});
