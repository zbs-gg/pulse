import assert from 'node:assert/strict';
import test from 'node:test';

import { probeProductRetrieval } from './product-retrieval-health.js';

test('retrieval health requires one real semantic response', async () => {
  const calls = [];
  const result = await probeProductRetrieval({ binding: true }, {
    request: async (resolved, path, options) => {
      calls.push({ resolved, path, options });
      return { event_ids: [] };
    },
  });

  assert.equal(result.ok, true);
  assert.match(result.detail, /^semantic retrieval answered in \d+ ms$/);
  assert.deepEqual(calls, [{
    resolved: { binding: true },
    path: '/retrieve',
    options: {
      body: { query: 'Pulse semantic retrieval health check', mode: 'factual', top_k: 1 },
      timeoutMs: 5000,
    },
  }]);
});

test('retrieval health rejects metadata-only and timed-out readiness', async () => {
  const invalid = await probeProductRetrieval({}, {
    request: async () => ({ full_retrieval: true, embedder: 'bge-m3' }),
  });
  assert.deepEqual(invalid, {
    ok: false,
    detail: 'semantic retrieval returned an invalid response',
  });

  const timedOut = await probeProductRetrieval({}, {
    request: async () => { throw new DOMException('timed out', 'TimeoutError'); },
  });
  assert.deepEqual(timedOut, { ok: false, detail: 'semantic retrieval timed out' });
});
