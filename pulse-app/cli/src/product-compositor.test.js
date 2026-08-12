import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, readdirSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  composeBoundResumeEvidence,
  composePromptMemoryContext,
  createContinuityDeliveryOffer,
  createContinuityDeliveryObservation,
  hasContinuitySessionDelivery,
  observePendingContinuityDelivery,
  persistContinuityDelivery,
  recordContinuityObservationTicket,
} from './product-compositor.js';
import { annotateContinuityDelivery } from './host-adapter.js';

const WORKSPACE = {
  workspace_id: 'workspace_pulse', repository_id: 'repository_pulse', canonical_path: '/repo',
};

function resolved() {
  return {
    binding: {
      mode: 'personal', fallback: false, principal_ref: 'principal_nik', workspace: WORKSPACE,
      binding_digest: 'a'.repeat(64),
      personal: { store_id: 'personal_nik' },
    },
    runtime: { data_dir: '/private/personal' },
  };
}

test('Personal binding returns only local continuity', async () => {
  const result = await composeBoundResumeEvidence(resolved(), {
    session_id: 'session_1', idempotency_key: 'event_1',
  }, {
    host: 'claude-code',
    request: async () => ({ resume_markdown: 'Personal continuity.' }),
  });
  assert.equal('commons' in result, false);
  assert.match(result.evidence[0], /^Personal Vault continuity/);
});

test('Personal carries only a complete controlled canonical baseline into the receipt manifest', async () => {
  const result = await composeBoundResumeEvidence(resolved(), {
    session_id: 'session_1', idempotency_key: 'event_1',
  }, {
    host: 'codex',
    request: async () => ({
      resume_markdown: 'Personal continuity.',
      included_object_ids: ['memory_1'],
      included_evidence_ids: ['pulse:memory_1'],
      baseline_kind: 'canonical_structured_resume_v1',
      source_equivalent_tokens: 500,
      coverage_counted: 1,
      coverage_total: 1,
    }),
  });
  assert.deepEqual(result.manifest, {
    object_ids: ['memory_1'], evidence_ids: ['pulse:memory_1'],
    baseline_kind: 'canonical_structured_resume_v1',
    source_equivalent_tokens: 500, coverage_counted: 1, coverage_total: 1,
  });

  await assert.rejects(composeBoundResumeEvidence(resolved(), {
    session_id: 'session_1', idempotency_key: 'event_1',
  }, {
    host: 'codex',
    request: async () => ({
      resume_markdown: 'Partial baseline must fail closed.',
      baseline_kind: 'canonical_structured_resume_v1',
      source_equivalent_tokens: 500,
    }),
  }), /local_resume_baseline_invalid/);
});

test('native lifecycle identity stays stable while divergent payload replay changes only the measured context', () => {
  const event = {
    host: 'codex', event: 'session_start', session_id: 'session_1',
    source_event_key: `event_${'b'.repeat(64)}`,
  };
  const first = createContinuityDeliveryOffer(resolved('personal'), event, 'first payload', {
    object_ids: ['memory_1'], evidence_ids: [],
  });
  const divergent = createContinuityDeliveryOffer(resolved('personal'), event, 'changed payload', {
    object_ids: ['memory_2'], evidence_ids: [],
  });
  assert.equal(divergent.idempotencyKey, first.idempotencyKey);
  assert.notEqual(divergent.offer.context_id, first.offer.context_id);
  assert.notEqual(divergent.offer.payload_digest, first.offer.payload_digest);
});

test('delivery persistence retries one transient local timeout with the exact idempotency key', async () => {
  const runtime = resolved('personal');
  const event = {
    host: 'codex', event: 'session_start', session_id: 'session_1',
    source_event_key: `event_${'b'.repeat(64)}`,
  };
  const output = annotateContinuityDelivery({}, runtime, event, {
    object_ids: ['memory_1'], evidence_ids: [],
  });
  const calls = [];
  const delivery = await persistContinuityDelivery(output, 'remembered context', {
    request: async (_resolved, path, options) => {
      calls.push({ path, options });
      if (calls.length === 1) {
        const error = new Error('local request exceeded its bounded deadline');
        error.name = 'TimeoutError';
        throw error;
      }
      return { schema: 'pulse.continuity_delivery_receipt.v1', state: 'offered_to_host' };
    },
  });
  assert.equal(delivery.receipt.state, 'offered_to_host');
  assert.equal(calls.length, 2);
  assert.equal(calls[0].path, '/continuity/delivery/offers');
  assert.equal(calls[0].options.timeoutMs, 2500);
  assert.equal(calls[1].options.idempotencyKey, calls[0].options.idempotencyKey);
  assert.deepEqual(calls[1].options.body, calls[0].options.body);
});

test('delivery persistence does not retry a deterministic conflict', async () => {
  const runtime = resolved('personal');
  const event = {
    host: 'codex', event: 'session_start', session_id: 'session_1',
    source_event_key: `event_${'c'.repeat(64)}`,
  };
  const output = annotateContinuityDelivery({}, runtime, event, {
    object_ids: ['memory_1'], evidence_ids: [],
  });
  let calls = 0;
  await assert.rejects(persistContinuityDelivery(output, 'changed context', {
    request: async () => {
      calls += 1;
      const error = new Error('delivery conflict');
      error.status = 409;
      throw error;
    },
  }), /delivery conflict/);
  assert.equal(calls, 1);
});

test('a later same-session host event promotes only a receipt-backed content-free offer ticket', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-continuity-observation-'));
  try {
    const runtime = resolved('personal');
    runtime.runtime.data_dir = root;
    const start = {
      host: 'claude-code', event: 'session_start', session_id: 'session_1',
      source_event_key: `event_${'b'.repeat(64)}`,
    };
    const measurement = createContinuityDeliveryOffer(runtime, start, 'remembered context payload', {
      object_ids: ['memory_1'], evidence_ids: ['pulse:memory_1'],
      memory_snapshot_digest: 'd'.repeat(64),
    });
    const offerReceipt = {
      schema: 'pulse.continuity_delivery_receipt.v1', receipt_id: 'delivery_offer_1',
      state: 'offered_to_host', purpose: measurement.offer.purpose,
      context_id: measurement.offer.context_id, binding_digest: measurement.offer.binding_digest,
      repository_id: measurement.offer.repository_id, host: measurement.offer.host,
      session_ref: measurement.offer.session_ref, payload_digest: measurement.offer.payload_digest,
      source_event_digest: measurement.offer.source_event_digest,
      created_at: new Date().toISOString(),
    };
    const ticket = recordContinuityObservationTicket({
      resolved: runtime, offer: measurement.offer, receipt: offerReceipt,
      memory_snapshot_digest: measurement.memory_snapshot_digest,
    });
    assert.equal(ticket.schema, 'pulse.continuity_observation_ticket.v2');
    const ticketDirectory = join(root, 'runtime', 'continuity-observations');
    const ticketPath = join(ticketDirectory, readdirSync(ticketDirectory)[0]);
    assert.doesNotMatch(readFileSync(ticketPath, 'utf8'), /remembered context payload|memory_1/);
    assert.equal(hasContinuitySessionDelivery(runtime, start), false);

    const promptEvent = {
      host: 'claude-code', native_event: 'UserPromptSubmit', event: 'turn_start',
      source: 'prompt_submitted', session_id: 'session_1',
      source_event_key: `event_${'c'.repeat(64)}`,
    };
    const expected = createContinuityDeliveryObservation(ticket, promptEvent);
    const calls = [];
    const observed = await observePendingContinuityDelivery(runtime, promptEvent, {
      request: async (_resolved, path, options) => {
        calls.push({ path, options });
        if (calls.length === 1) {
          const error = new Error('slow local observation');
          error.name = 'TimeoutError';
          throw error;
        }
        return {
          ...offerReceipt, receipt_id: 'delivery_observed_1', parent_receipt_id: offerReceipt.receipt_id,
          state: 'host_observed', source_event_digest: options.body.source_event_digest,
        };
      },
    });
    assert.equal(observed.state, 'host_observed');
    assert.equal(calls.length, 2);
    assert.equal(calls[0].path, '/continuity/delivery/observations');
    assert.deepEqual(calls[0].options.body, expected.body);
    assert.equal(calls[0].options.idempotencyKey, expected.idempotencyKey);
    assert.equal(calls[0].options.timeoutMs, 5000);
    assert.deepEqual(calls[1], calls[0]);
    assert.deepEqual(readdirSync(ticketDirectory), []);
    const observedDirectory = join(root, 'runtime', 'continuity-observed');
    const observedFiles = readdirSync(observedDirectory);
    assert.equal(observedFiles.length, 1);
    assert.doesNotMatch(readFileSync(join(observedDirectory, observedFiles[0]), 'utf8'),
      /remembered context payload|memory_1/);
    assert.equal(hasContinuitySessionDelivery(runtime, promptEvent), true);
    assert.equal(hasContinuitySessionDelivery(runtime, promptEvent, {
      expectedMemorySnapshotDigest: 'd'.repeat(64),
    }), true);
    assert.equal(hasContinuitySessionDelivery(runtime, promptEvent, {
      expectedMemorySnapshotDigest: 'e'.repeat(64),
    }), false);
    assert.equal(await observePendingContinuityDelivery(runtime, promptEvent, { request: async () => assert.fail() }), undefined);

    const stopEvent = {
      host: 'claude-code', native_event: 'Stop', event: 'turn_finalize',
      source: 'stop', session_id: 'session_1',
      source_event_key: `event_${'d'.repeat(64)}`,
    };
    const stopObservation = createContinuityDeliveryObservation(ticket, stopEvent);
    assert.equal(stopObservation.body.session_ref, ticket.session_ref);

    assert.throws(() => createContinuityDeliveryObservation(ticket, {
      ...promptEvent, host: 'cursor',
    }), /continuity_observation_event_invalid/);
    assert.throws(() => createContinuityDeliveryObservation(ticket, {
      ...promptEvent, native_event: 'PreToolUse', source: 'mcp__pulse-product__pulse_context',
    }), /continuity_observation_event_invalid/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('composition returns only the structured IDs declared as included in rendered continuity', async () => {
  const result = await composeBoundResumeEvidence(resolved('personal'), {
    session_id: 'session_1', idempotency_key: 'event_1',
  }, {
    host: 'codex',
    request: async () => ({
      resume_markdown: 'Rendered personal memory.',
      memory_snapshot_digest: 'f'.repeat(64),
      included_object_ids: ['memory_2', 'memory_1', 'memory_2'],
      included_evidence_ids: ['pulse:memory_2', 'pulse:memory_1', 'pulse:memory_2'],
      // Legacy aggregate refs can include entries removed by bounded rendering
      // and must never be promoted into the exact-delivery manifest.
      evidence_refs: ['pulse:not-rendered'],
      material_refs: ['memory_not-rendered'],
    }),
  });

  assert.deepEqual(result.manifest, {
    object_ids: ['memory_1', 'memory_2'],
    evidence_ids: ['pulse:memory_1', 'pulse:memory_2'],
    memory_snapshot_digest: 'f'.repeat(64),
  });
  assert.equal(Object.isFrozen(result.manifest), true);
  assert.equal(Object.isFrozen(result.manifest.object_ids), true);
});

test('prompt recall injects only the strongest capsule and bounds legacy summaries', async () => {
  const output = await composePromptMemoryContext(resolved(), 'How should memory behave?', {
    request: async (_runtime, path, options) => {
      assert.equal(path, '/context/query');
      assert.equal(options.body.query, 'How should memory behave?');
      assert.equal(options.body.top_k, 12);
      return {
        events: [
          { id: 1, summary: `Strong one. ${'legacy detail '.repeat(80)}` },
          { id: 2, summary: 'Weak noise.' },
          { id: 3, summary: 'Strong two.' },
          { id: 4, summary: 'Strong three.' },
          { id: 5, summary: 'Strong four.' },
          { id: 6, summary: 'Strong five must not appear.' },
        ],
        trace: { retrieval: {
          score_breakdowns: {
            1: { cosine: 0.72 }, 2: { cosine: 0.31 }, 3: { cosine: 0.65 },
            4: { cosine: 0.54 }, 5: { cosine: 0.44 }, 6: { cosine: 0.91 },
          },
          candidate_evidence: {
            1: { dense: true, lexical: true, direct_capsule: true },
            2: { dense: true, lexical: true, direct_capsule: true },
            3: { dense: true, lexical: false, direct_capsule: true },
            4: { dense: true, lexical: false, direct_capsule: true },
            5: { dense: true, lexical: true, direct_capsule: true },
            6: { dense: true, lexical: true, direct_capsule: true },
          },
        } },
      };
    },
  });
  assert.match(output, /Strong one/);
  assert.doesNotMatch(output, /Weak noise|Strong two|Strong three|Strong four|Strong five/);
  assert.equal(output.split('\n').filter((line) => line.startsWith('- ')).length, 1);
  assert.equal([...output.split('\n').find((line) => line.startsWith('- ')).slice(2)].length <= 400, true);
  assert.equal(Buffer.byteLength(output) <= 2400, true);
});

test('prompt recall injects nothing when relevance evidence is weak or absent', async () => {
	const activity = [];
  const weak = await composePromptMemoryContext(resolved(), 'Unrelated prompt', {
    request: async () => ({
      events: [{ id: 1, summary: 'Some memory.' }],
      trace: { retrieval: {
        score_breakdowns: { 1: { cosine: 0.12 } },
        candidate_evidence: { 1: { dense: true, lexical: true, direct_capsule: true } },
      } },
		}),
		recordActivity: async (receipt) => activity.push(receipt),
  });
  assert.equal(weak, '');
	assert.equal(activity.length, 1);
	assert.deepEqual(Object.keys(activity[0]).sort(), ['result_count', 'result_digest', 'schema']);
	assert.equal(activity[0].result_count, 0);
	assert.match(activity[0].result_digest, /^[a-f0-9]{64}$/);
	assert.doesNotMatch(JSON.stringify(activity), /Unrelated prompt|Some memory/);
});

test('prompt recall rejects the observed unrelated capsule but keeps a relevant personal capsule', async () => {
  const response = (cosine, summary) => ({
    events: [{ id: 1, summary }],
    trace: { retrieval: {
      score_breakdowns: { 1: { cosine } },
      candidate_evidence: { 1: { dense: true, lexical: false, direct_capsule: true } },
    } },
  });
  const unrelated = await composePromptMemoryContext(resolved(), 'Сколько минут варить яйцо всмятку?', {
    request: async () => response(0.3974492847919464, 'A detailed ZBS Eye codebase review.'),
  });
  const relevant = await composePromptMemoryContext(resolved(), 'Как Ник предпочитает получать технические отчёты?', {
    request: async () => response(0.5178588628768921,
      'Technical reports should use short titled sections followed by connected explanatory paragraphs.'),
  });
  assert.equal(unrelated, '');
  assert.match(relevant, /short titled sections/);
});

test('prompt recall keeps the stricter lexical agreement rule for archive events at 0.32', async () => {
  const output = await composePromptMemoryContext(resolved(), 'Old context', {
    request: async () => ({
      events: [{ id: 1, summary: 'Archive agreement.' }],
      trace: { retrieval: {
        score_breakdowns: { 1: { cosine: 0.33 } },
        candidate_evidence: { 1: { dense: true, lexical: true, direct_capsule: false } },
      } },
    }),
  });
  assert.match(output, /Archive agreement/);
});

test('prompt recall presents the accepted decision as factual context', async () => {
  const output = await composePromptMemoryContext(
    resolved(),
    'Какое правило мы приняли для связи решения и эмоционального момента?',
    {
      request: async () => ({
        events: [{
          id: 76783,
          summary: 'Эмоциональный момент хранится как отдельный элемент памяти, а не как поле решения.',
        }],
        trace: { retrieval: {
          score_breakdowns: { 76783: { cosine: 0.6851975917816162 } },
          candidate_evidence: {
            76783: { dense: true, lexical: true, direct_capsule: true },
          },
        } },
      }),
    },
  );
  assert.match(output, /^Pulse accepted memory/);
  assert.match(output, /use as factual context/);
  assert.match(output, /отдельный элемент памяти, а не как поле решения/);
});

test('prompt recall prefers direct capsules and never mixes archive noise', async () => {
  const output = await composePromptMemoryContext(resolved(), 'What did we decide?', {
    request: async () => ({
      events: [
        { id: 1, summary: 'Archive result with stronger raw score.' },
        { id: 2, summary: 'The durable decision.' },
      ],
      trace: { retrieval: {
        score_breakdowns: { 1: { cosine: 0.91 }, 2: { cosine: 0.55 } },
        candidate_evidence: {
          1: { dense: true, lexical: true, direct_capsule: false },
          2: { dense: true, lexical: false, direct_capsule: true },
        },
      } },
    }),
  });
  assert.match(output, /durable decision/);
  assert.doesNotMatch(output, /Archive result/);
});

test('prompt recall accepts at most two archive events only with dense and lexical agreement', async () => {
  const output = await composePromptMemoryContext(resolved(), 'Old context', {
    request: async () => ({
      events: [
        { id: 1, summary: 'Dense only archive noise.' },
        { id: 2, summary: 'Archive agreement one.' },
        { id: 3, summary: 'Archive agreement two.' },
        { id: 4, summary: 'Archive agreement three.' },
      ],
      trace: { retrieval: {
        score_breakdowns: {
          1: { cosine: 0.9 }, 2: { cosine: 0.7 }, 3: { cosine: 0.6 }, 4: { cosine: 0.5 },
        },
        candidate_evidence: {
          1: { dense: true, lexical: false, direct_capsule: false },
          2: { dense: true, lexical: true, direct_capsule: false },
          3: { dense: true, lexical: true, direct_capsule: false },
          4: { dense: true, lexical: true, direct_capsule: false },
        },
      } },
    }),
  });
  assert.doesNotMatch(output, /Dense only|agreement three/);
  assert.match(output, /agreement one/);
  assert.match(output, /agreement two/);
});
