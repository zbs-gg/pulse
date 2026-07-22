import assert from 'node:assert/strict';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import test from 'node:test';

import {
  HistoricalIngestCLIError,
  runHistoricalIngestCommand,
  waitForHistoricalEgress,
} from './historical-ingest.js';

const snapshot = 'a'.repeat(64);
const contract = 'b'.repeat(64);
const jobID = 'job_0123456789abcdef';

function status(state, overrides = {}) {
  return {
    schema: 'pulse.historical_ingest.status.v1', job_id: jobID, state, generation: 1,
    total_units: 3, accepted_units: 0, pending_units: 3, leased_units: 0,
    usage: { input_tokens: 0, cached_input_tokens: 0, output_tokens: 0, reasoning_tokens: 0 },
    snapshot_digest: snapshot, runner_contract_digest: contract,
    source_root_count: 50, source_file_count: 73, source_bytes: 1024 * 1024, evidence_bytes: 256 * 1024,
    egress_authorized: state !== 'awaiting_egress_consent',
    ...overrides,
  };
}

test('ingest freezes exact roots, opens Home consent, qualifies Luna, and returns a dry run', async () => {
  const calls = [];
  const output = [];
  let opened = 0;
  let workerOptions;
  const result = await runHistoricalIngestCommand({
    argv: ['ingest', 'codex', '--roots', '50'], dataDir: mkdtempSync(path.join(tmpdir(), 'pulse-history-cli-')),
    currentSessionID: '019f8311-64eb-7120-9995-df81b0b7b06e', stdout: (line) => output.push(line),
    request: async (method, route, body) => {
      calls.push({ method, route, body });
      return status('awaiting_egress_consent');
    },
    openHome: async () => { opened += 1; },
    waitForEgress: async ({ jobID: waited }) => {
      assert.equal(waited, jobID);
      return status('extracting');
    },
    qualify: async ({ egressAuthorized }) => ({ live_model_qualified: egressAuthorized, contract_digest: contract }),
    runWorker: async (options) => {
      workerOptions = options;
      return { state: 'manifest_ready', accepted_units: 3, processed_units: 3 };
    },
  });
  assert.deepEqual(calls[0], {
    method: 'POST', route: '/memory/historical-ingest/jobs',
    body: { source: 'codex', root_limit: 50, excluded_session_id: '019f8311-64eb-7120-9995-df81b0b7b06e' },
  });
  assert.equal(opened, 2);
  assert.equal(workerOptions.jobID, jobID);
  assert.equal(workerOptions.qualification.contract_digest, contract);
  assert.deepEqual(result, { state: 'manifest_ready', accepted_units: 3, processed_units: 3 });
  assert.match(output.join('\n'), /Nothing has left this Mac/);
  assert.match(output.join('\n'), /Memory writes: 0/);
  assert.doesNotMatch(JSON.stringify({ calls, result, output }), /Users\/|trusted_prompt|"evidence":/);
});

test('status, explain, and usage expose one content-free daemon lifecycle', async () => {
  for (const action of ['status', 'explain', 'usage']) {
    const calls = [];
    const output = [];
    const result = await runHistoricalIngestCommand({
      argv: [action], dataDir: mkdtempSync(path.join(tmpdir(), 'pulse-history-cli-')),
      request: async (method, route) => { calls.push({ method, route }); return status('paused_quota', { accepted_units: 2, pending_units: 1 }); },
      openHome: async () => {}, stdout: (line) => output.push(line),
    });
    assert.deepEqual(calls, [{ method: 'GET', route: '/memory/historical-ingest/jobs/latest' }]);
    assert.ok(result);
    assert.doesNotMatch(output.join('\n'), /raw transcript|authorization:|\/home\//i);
  }
});

test('cancel uses the same job and stops its registered foreground worker', async () => {
  const calls = [];
  const stopped = [];
  const result = await runHistoricalIngestCommand({
    argv: ['cancel', '--job', jobID], dataDir: mkdtempSync(path.join(tmpdir(), 'pulse-history-cli-')),
    request: async (method, route, body) => {
      calls.push({ method, route, body });
      return route.endsWith('/cancel') ? status('canceled') : status('extracting');
    },
    openHome: async () => {}, stdout: () => {},
    stopWorker: (dataDir, stoppedJob) => stopped.push({ dataDir, stoppedJob }),
  });
  assert.equal(result.state, 'canceled');
  assert.equal(calls[0].route, `/memory/historical-ingest/jobs/${jobID}`);
  assert.equal(calls[1].route, `/memory/historical-ingest/jobs/${jobID}/cancel`);
  assert.equal(stopped[0].stoppedJob, jobID);
});

test('egress wait polls without inventing a second lifecycle', async () => {
  const states = [status('awaiting_egress_consent'), status('awaiting_egress_consent'), status('extracting')];
  let sleeps = 0;
  const result = await waitForHistoricalEgress({
    jobID, request: async () => states.shift(), sleep: async () => { sleeps += 1; }, timeoutMs: 10_000,
  });
  assert.equal(result.state, 'extracting');
  assert.equal(sleeps, 2);
});

test('history CLI has no remote, Team, store, token, or apply switch', async () => {
  for (const forbidden of ['--base', '--store', '--team', '--token', '--egress-token', '--apply']) {
    await assert.rejects(() => runHistoricalIngestCommand({
      argv: ['status', forbidden, 'value'], dataDir: mkdtempSync(path.join(tmpdir(), 'pulse-history-cli-')),
      request: async () => status('extracting'), openHome: async () => {},
    }), (error) => error instanceof HistoricalIngestCLIError && error.code === 'historical_forbidden_option');
  }
});
