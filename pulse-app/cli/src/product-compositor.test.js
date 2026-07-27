import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, readdirSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  composeBoundResumeEvidence,
  createContinuityDeliveryOffer,
  createContinuityDeliveryObservation,
  hasContinuitySessionDelivery,
  observePendingContinuityDelivery,
  persistContinuityDelivery,
  recordContinuityObservationTicket,
  renderCommonsResume,
} from './product-compositor.js';
import { annotateContinuityDelivery } from './host-adapter.js';
import { TeamRemoteClientError } from './team-remote-client.js';

const WORKSPACE = {
  workspace_id: 'workspace_pulse', repository_id: 'repository_pulse', canonical_path: '/repo',
};

function resolved(mode = 'team') {
  return {
    binding: {
      mode, fallback: false, principal_ref: 'principal_nik', workspace: WORKSPACE,
      binding_digest: 'a'.repeat(64),
      ...(mode === 'team'
        ? { desk: { store_id: 'desk_nik' }, commons: {
          project_id: 'project_zbs', resource: 'https://pulse.example/mcp',
        } }
        : { personal: { store_id: 'personal_nik' } }),
    },
    runtime: { data_dir: '/private/desk' },
  };
}

function remoteResume(text = 'Use the approved deployment checklist.') {
  return {
    schema: 'pulse.team.resume_result.v1',
    thread_id: 'repository_pulse',
    sections: {
      where_we_left_off: [{ object_id: 'commons_1', text }],
      active_decisions: [], open_loops: [], do_not_repeat: [],
      relevant_emotional_state_context: [], suggested_next_step: [],
    },
    returned_count: 1,
    fallback: false,
  };
}

test('team session composes private Desk and authorized Commons with separate provenance and budgets', async () => {
  const calls = [];
  const result = await composeBoundResumeEvidence(resolved(), {
    session_id: 'session_1', idempotency_key: 'event_1',
  }, {
    host: 'codex',
    localTokenBudget: 700,
    commonsLimit: 6,
    request: async (_resolved, path, options) => {
      calls.push({ path, body: options.body });
      return { resume_markdown: 'Desk decision stays private.' };
    },
    teamRequest: async (binding, tool, args) => {
      calls.push({ binding, tool, args });
      return remoteResume();
    },
  });
  assert.equal(result.commons.status, 'active');
  assert.equal(result.commons.fallback, false);
  assert.match(result.evidence[0], /^Private Desk continuity \(local, private\):/);
  assert.match(result.evidence[1], /^Team Commons \[where_we_left_off\].*shared, authorized Commons evidence/s);
  assert.equal(calls[0].body.token_budget, 700);
  assert.equal(calls[1].tool, 'pulse_team_resume');
  assert.equal(calls[1].args.limit, 6);
  assert.deepEqual(calls[1].args.active_context, {
    project_id: 'project_zbs', repo_id: 'repository_pulse',
    agent_id: 'principal_nik', session_id: 'session_1',
  });
});

test('Commons outage degrades loudly without replacing private Desk or querying a fallback', async () => {
  let remoteCalls = 0;
  const result = await composeBoundResumeEvidence(resolved(), {
    session_id: 'session_1', idempotency_key: 'event_1',
  }, {
    host: 'codex',
    request: async () => ({ resume_markdown: 'Private continuity.' }),
    teamRequest: async () => {
      remoteCalls++;
      throw new TeamRemoteClientError('revoked');
    },
  });
  assert.equal(remoteCalls, 1);
  assert.deepEqual(result.commons, { status: 'degraded', reason_code: 'revoked', fallback: false });
  assert.equal(result.evidence.length, 2);
  assert.match(result.evidence[1], /No fallback store was queried/);
});

test('Personal binding never creates a Commons client', async () => {
  let remoteCalls = 0;
  const result = await composeBoundResumeEvidence(resolved('personal'), {
    session_id: 'session_1', idempotency_key: 'event_1',
  }, {
    host: 'claude-code',
    request: async () => ({ resume_markdown: 'Personal continuity.' }),
    teamRequest: async () => { remoteCalls++; },
  });
  assert.equal(remoteCalls, 0);
  assert.equal(result.commons.status, 'not_applicable');
  assert.match(result.evidence[0], /^Personal Vault continuity/);
});

test('Personal carries only a complete controlled canonical baseline into the receipt manifest', async () => {
  const result = await composeBoundResumeEvidence(resolved('personal'), {
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

  await assert.rejects(composeBoundResumeEvidence(resolved('personal'), {
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

test('Team composition omits local baseline because mixed Desk and Commons are incomparable', async () => {
  const result = await composeBoundResumeEvidence(resolved('team'), {
    session_id: 'session_1', idempotency_key: 'event_1',
  }, {
    host: 'codex',
    request: async () => ({
      resume_markdown: 'Desk continuity.',
      included_object_ids: ['memory_private'],
      baseline_kind: 'canonical_structured_resume_v1',
      source_equivalent_tokens: 500,
      coverage_counted: 1,
      coverage_total: 1,
    }),
    teamRequest: async () => remoteResume(),
  });
  assert.equal('baseline_kind' in result.manifest, false);
  assert.equal('source_equivalent_tokens' in result.manifest, false);
  assert.equal('coverage_counted' in result.manifest, false);
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
    });
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
        return {
          ...offerReceipt, receipt_id: 'delivery_observed_1', parent_receipt_id: offerReceipt.receipt_id,
          state: 'host_observed', source_event_digest: options.body.source_event_digest,
        };
      },
    });
    assert.equal(observed.state, 'host_observed');
    assert.equal(calls[0].path, '/continuity/delivery/observations');
    assert.deepEqual(calls[0].options.body, expected.body);
    assert.equal(calls[0].options.idempotencyKey, expected.idempotencyKey);
    assert.deepEqual(readdirSync(ticketDirectory), []);
    const observedDirectory = join(root, 'runtime', 'continuity-observed');
    const observedFiles = readdirSync(observedDirectory);
    assert.equal(observedFiles.length, 1);
    assert.doesNotMatch(readFileSync(join(observedDirectory, observedFiles[0]), 'utf8'),
      /remembered context payload|memory_1/);
    assert.equal(hasContinuitySessionDelivery(runtime, promptEvent), true);
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
  });
  assert.equal(Object.isFrozen(result.manifest), true);
  assert.equal(Object.isFrozen(result.manifest.object_ids), true);
});

test('Commons object IDs remain structural instead of being parsed back out of rendered text', async () => {
  const result = await composeBoundResumeEvidence(resolved(), {
    session_id: 'session_1', idempotency_key: 'event_1',
  }, {
    host: 'codex',
    request: async () => ({
      resume_markdown: 'Private continuity.',
      included_object_ids: ['memory_private'],
      included_evidence_ids: ['pulse:memory_private'],
    }),
    teamRequest: async () => remoteResume('Text mentions [fake_object] but it is not provenance.'),
  });

  assert.deepEqual(result.manifest, {
    object_ids: ['commons_1', 'memory_private'],
    evidence_ids: ['pulse:memory_private'],
  });
});

test('Commons response parser rejects count drift and malformed object provenance', () => {
  assert.throws(() => renderCommonsResume({ ...remoteResume(), returned_count: 0 }, 8), /commons_resume_invalid/);
  const malformed = remoteResume();
  malformed.sections.where_we_left_off[0].object_id = '../private/desk';
  assert.throws(() => renderCommonsResume(malformed, 8), /commons_resume_invalid/);
});
