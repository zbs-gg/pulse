import assert from 'node:assert/strict';
import test from 'node:test';

import { handleOpenCodeBridge } from './opencode-bridge.js';

const resolved = {
  binding: {
    binding_digest: 'a'.repeat(64), resolver_epoch: 1,
    workspace: { canonical_path: '/tmp/project', repository_id: 'repository_project' },
  },
  runtime: { data_dir: '/tmp/pulse', base_url: 'http://127.0.0.1:18789' },
};

test('OpenCode message writes only turn metadata and returns recall for the same model call', async () => {
  let written;
  const requests = [];
  const result = await handleOpenCodeBridge('message', {
    session_id: 'session_one', turn_id: 'message_one', cwd: '/tmp/project',
    model: 'openai/small', query: 'What did we decide?',
  }, {
    resolveRuntime: () => resolved,
    writeTurnContext: (_resolved, event) => { written = event; },
    composeMemory: async (_resolved, query, options) => {
      assert.equal(query, 'What did we decide?');
      await options.recordActivity({
        schema: 'pulse.memory_recall_activity.v1', result_count: 1, result_digest: 'b'.repeat(64),
      });
      return 'Pulse accepted memory:\n- Keep it local';
    },
    request: async (_resolved, path, options) => {
      requests.push({ path, options });
      return { ok: true };
    },
  });
  assert.equal(written.host, 'opencode');
  assert.equal(written.event, 'turn_start');
  assert.equal(Object.hasOwn(written, 'query'), false);
  assert.match(result.context, /Keep it local/);
  assert.match(result.context, /pulse_memory/);
  assert.equal(requests[0].path, '/memory/activity/recall');
  assert.equal(JSON.stringify(requests).includes('What did we decide?'), false);
});

test('OpenCode recall failure is silent and still leaves durable-memory guidance', async () => {
  const result = await handleOpenCodeBridge('message', {
    session_id: 'session_fail', turn_id: 'message_fail', cwd: '/tmp/project',
    model: 'local/model', query: 'Normal request',
  }, {
    resolveRuntime: () => resolved,
    writeTurnContext: () => {},
    composeMemory: async () => { throw new Error('daemon_offline'); },
  });
  assert.match(result.context, /pulse_memory/);
});

test('OpenCode pulse_memory consumes the exact session lease and never marks raw input included', async () => {
  const calls = [];
  const result = await handleOpenCodeBridge('memory', {
    session_id: 'session_two', turn_id: 'message_two',
    source_event_key: `event_${'c'.repeat(64)}`,
    idempotency_key: `lifecycle:${'d'.repeat(64)}`,
    tool_use_id: 'tool_two',
    items: [{ kind: 'decision', scope: 'project', summary: 'Use the OpenCode adapter.' }],
  }, {
    resolveRuntime: () => resolved,
    readTurnContext: () => ({
      host: 'opencode', session_id: 'session_two', turn_id: 'message_two',
      source_event_key: `event_${'c'.repeat(64)}`,
      idempotency_key: `lifecycle:${'d'.repeat(64)}`,
      binding_digest: 'a'.repeat(64), policy_epoch: 0, resolver_epoch: 1,
    }),
    writeToolLease: (...args) => calls.push(['write', ...args]),
    consumeToolLease: (...args) => calls.push(['consume', ...args]),
    writeFinalizeMarker: () => {},
    request: async (_resolved, path, options) => {
      calls.push(['request', path, options]);
      return {
        ledger_id: 'ledger_one', status: 'candidates',
        finalize_receipt: { receipt_id: 'receipt_finalize' },
        receipts: [{ receipt_id: 'receipt_one', status: 'created', object_id: 'object_one' }],
      };
    },
    now: () => new Date('2026-08-22T00:00:00.000Z'),
  });
  assert.deepEqual(result, { status: 'stored', ids: ['object_one'] });
  assert.equal(calls[0][0], 'write');
  assert.equal(calls[1][0], 'consume');
  const body = calls[2][2].body;
  assert.equal(body.host, 'opencode');
  assert.equal(body.candidates[0].capsule.raw_input_included, false);
  assert.equal(JSON.stringify(body).includes('/tmp/project'), false);
});

test('OpenCode pulse_memory rejects extra fields before any write', async () => {
  let touched = false;
  await assert.rejects(() => handleOpenCodeBridge('memory', {
    session_id: 'session_three', turn_id: 'message_three',
    source_event_key: `event_${'e'.repeat(64)}`,
    idempotency_key: `lifecycle:${'f'.repeat(64)}`,
    tool_use_id: 'tool_three', raw: 'forbidden',
    items: [{ kind: 'decision', scope: 'project', summary: 'Safe' }],
  }, {
    resolveRuntime: () => { touched = true; return resolved; },
  }), /pulse_memory_input_invalid/);
  assert.equal(touched, false);
});

test('OpenCode fun-fact candidates and receipts remain bounded and content-free', async () => {
  const candidateText = 'Pulse keeps approved memory local.';
  let receiptBody;
  const candidates = await handleOpenCodeBridge('fun-fact-candidates', {
    session_id: 'session_fact',
  }, {
    readOptions: () => ({ configured: true, fun_facts: 'small-model' }),
    resolveRuntime: () => resolved,
    request: async (_resolved, path, options) => {
      assert.equal(path, '/memory/fun-fact-candidates');
      assert.equal(options.method, 'GET');
      return {
        schema: 'pulse.opencode_fun_fact_candidates.v1',
        candidates: [{ id: `fact_${'a'.repeat(24)}`, text: candidateText }],
        candidate_digest: 'b'.repeat(64),
      };
    },
  });
  assert.deepEqual(candidates.candidates, [{ id: `fact_${'a'.repeat(24)}`, text: candidateText }]);

  const receipt = await handleOpenCodeBridge('fun-fact-receipt', {
    session_id: 'session_fact', model: 'same/tiny', latency_ms: 42,
    usage: { input: 8, output: 1, total: 9 },
    candidate_digest: 'b'.repeat(64), outcome: 'selected',
  }, {
    resolveRuntime: () => resolved,
    now: () => new Date('2026-08-22T00:00:00.000Z'),
    platformServices: {
      ensurePrivateDirectory: () => {},
      atomicWritePrivateFile: (_path, body) => { receiptBody = body; },
    },
  });
  assert.deepEqual(receipt, { schema: 'pulse.opencode_fun_fact_receipt.v1', recorded: true });
  const stored = JSON.parse(receiptBody);
  assert.equal(stored.model, 'same/tiny');
  assert.equal(stored.latency_ms, 42);
  assert.equal(stored.candidate_digest, 'b'.repeat(64));
  assert.deepEqual(stored.usage, { input: 8, output: 1, total: 9 });
  assert.equal(JSON.stringify(stored).includes(candidateText), false);
  assert.equal(JSON.stringify(stored).includes('session_fact'), false);
});

test('OpenCode rejects a path or secret-shaped fun fact even if the daemon response is malformed', async () => {
  for (const text of ['/Users/private/notes.txt', 'token=ghp_forbidden']) {
    await assert.rejects(() => handleOpenCodeBridge('fun-fact-candidates', {
      session_id: 'session_unsafe_fact',
    }, {
      readOptions: () => ({ configured: true, fun_facts: 'small-model' }),
      resolveRuntime: () => resolved,
      request: async () => ({
        schema: 'pulse.opencode_fun_fact_candidates.v1',
        candidates: [{ id: `fact_${'c'.repeat(24)}`, text }],
        candidate_digest: 'd'.repeat(64),
      }),
    }), /opencode_fun_fact_candidates_invalid/);
  }
});
