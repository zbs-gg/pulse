import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { createServer } from 'node:http';
import { test } from 'node:test';

import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPServerTransport } from '@modelcontextprotocol/sdk/server/streamableHttp.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';

import { createPulseMcpServer } from './index.js';
import type {
  BoundTeamDomain,
  GatewaySecurityEventInput,
  TeamPrincipalContext,
} from './principal-context.js';
import { ContractError, validateCapsule, validateDelta } from './validation.js';

import {
  canonicalTeamContextQueryBody,
  canonicalTeamGraphDeltaBody,
  canonicalTeamRecallBody,
  canonicalTeamRememberBody,
  canonicalTeamResumeBody,
  expectedTeamGraphProjectionKinds,
  isTeamToolName,
  requiredTeamCapabilities,
  TEAM_BASELINE_CAPABILITY,
  TeamContractError,
  TeamDomainError,
  TEAM_TOOL_DESCRIPTORS,
  teamNotReadyResult,
  validateTeamGraphDeltaInput,
  validateTeamGraphDeltaResult,
  validateTeamContextQueryResult,
  validateTeamRecallResult,
  validateTeamRememberResult,
  validateTeamRememberInput,
  validateTeamResumeResult,
} from './team-contracts.js';

function validateTeamRemember(input: unknown): Record<string, unknown> {
  return validateTeamRememberInput(input) as unknown as Record<string, unknown>;
}

function assertTeamRememberRejected(input: unknown, expected: RegExp): void {
  let thrown: unknown;
  try {
    validateTeamRemember(input);
  } catch (error) {
    thrown = error;
  }
  assert.ok(thrown instanceof Error, 'team remember input must be rejected');
  assert.ok(thrown instanceof TeamContractError);
  assert.equal((thrown as Error & { code?: string }).code, 'invalid_team_contract');
  assert.match(thrown.message, expected);
}

function baseTeamRemember() {
  return {
    schema: 'pulse.team.memory.v1',
    source: {
      host: 'claude-code',
      conversation_scope: 'current_turn',
      timestamp: '2026-07-11T05:00:00Z',
    },
    items: [{
      kind: 'decision',
      redacted_summary: 'Use the dedicated team store for the pilot.',
      confidence: 0.9,
      evidence_hint: 'current_turn',
      tags: ['pilot', 'storage'],
    }],
    raw_input_included: false as const,
    active_context: {
      project_id: 'project-pulse',
      repo_id: 'repo-pulse',
      agent_id: 'agent-bound',
      session_id: 'session-2026-07-11',
    },
    privacy_tier: 'normal',
    retention: 'project',
    idempotency_key: 'remember-request-001',
  };
}

function teamReadActiveContext() {
  return {
    project_id: 'project-pulse',
    repo_id: 'repo-pulse',
    agent_id: 'agent-bound',
    session_id: 'session-2026-07-11',
  };
}

function baseTeamRecall() {
  return {
    schema: 'pulse.team.recall.v1',
    query: 'What did we decide about scoped retrieval?',
    active_context: teamReadActiveContext(),
    privacy_ceiling: 'sensitive',
  };
}

function baseTeamContextQuery() {
  return {
    schema: 'pulse.team.context.v1',
    query: 'What is the current Pulse team plan?',
    active_context: teamReadActiveContext(),
    privacy_ceiling: 'sensitive',
  };
}

function baseTeamResume() {
  return {
    schema: 'pulse.team.resume.v1',
    active_context: teamReadActiveContext(),
    thread_id: 'pulse-team-foundation',
  };
}

function storedTeamRecallResult() {
  return {
    schema: 'pulse.team.recall_result.v1' as const,
    items: [{
      object_id: 'team_root_memory_001',
      kind: 'decision',
      redacted_summary: 'Use pre-retrieval authorization for every read surface.',
      confidence: 0.95,
      privacy_tier: 'sensitive',
      retention: 'project',
      tags: ['authorization', 'pulse'],
    }],
    returned_count: 1,
    fallback: false as const,
  };
}

function storedTeamContextResult() {
  return {
    schema: 'pulse.team.context_result.v1' as const,
    facts: [{
      root_object_id: 'team_root_graph_001', object_id: 'team_fact_001',
      text: 'Authorization runs before candidate generation.', score: 0.95,
      confidence: 0.9, domain: 'real',
    }],
    events: [{
      root_object_id: 'team_root_graph_002', object_id: 'team_event_001',
      title: 'U6 started', summary: 'Scoped retrieval implementation started.',
      score: 0.85, confidence: 0.9, domain: 'real',
    }],
    entities: [{
      root_object_id: 'team_root_graph_003', object_id: 'team_entity_001',
      kind: 'project', canonical_name: 'Pulse', summary: 'Team memory foundation.',
      score: 0.8, confidence: 0.95,
    }],
    relations: [{
      root_object_id: 'team_root_graph_004', object_id: 'team_relation_001',
      kind: 'uses', from_object_id: 'team_entity_001', to_object_id: 'team_entity_001',
      summary: 'Pulse uses scoped candidate filters.', score: 0.75, confidence: 0.9,
    }],
    assertions: [{
      root_object_id: 'team_root_graph_005', object_id: 'team_assertion_001',
      subject_object_id: 'team_entity_001', predicate: 'authorization_boundary',
      object_text: 'pre_retrieval', confidence: 0.9,
    }],
    trace: {
      stages: [
        { kind: 'lexical' as const, returned_object_ids: ['team_fact_001'] },
        { kind: 'graph' as const, returned_object_ids: ['team_entity_001', 'team_relation_001'] },
      ],
    },
    returned_counts: { facts: 1, events: 1, entities: 1, relations: 1, assertions: 1 },
    fallback: false as const,
  };
}

function storedTeamResumeResult() {
  return {
    schema: 'pulse.team.resume_result.v1' as const,
    thread_id: 'pulse-team-foundation',
    sections: {
      where_we_left_off: [{ object_id: 'team_root_continuity_001', text: 'U6 transport is next.' }],
      active_decisions: [{ object_id: 'team_root_continuity_001', text: 'Filter before retrieval.' }],
      open_loops: [],
      do_not_repeat: [],
      relevant_emotional_state_context: [],
      suggested_next_step: [{ object_id: 'team_root_continuity_001', text: 'Wire read routes.' }],
    },
    returned_count: 3,
    fallback: false as const,
  };
}

function baseTeamGraphDelta() {
  return {
    schema: 'pulse.team.graph_delta.v1',
    source: {
      host: 'claude-code',
      conversation_scope: 'current_turn',
      timestamp: '2026-07-11T12:00:00+07:00',
    },
    nodes: [
      {
        client_id: 'person:alex',
        kind: 'person',
        canonical_name: ' Alex ',
        summary: ' Works on the Pulse pilot. ',
        aliases: [' Alexander ', 'Alexey'],
        salience: 0.8,
        emotional_weight: 0.2,
        domain: 'real',
      },
      {
        client_id: 'project:pulse',
        kind: 'project',
        canonical_name: 'Pulse',
        aliases: [],
        salience: 0.9,
        emotional_weight: 0,
        domain: 'real',
      },
    ],
    edges: [{
      from: 'person:alex',
      to: 'project:pulse',
      kind: 'works_on',
      summary: ' Alex contributes to Pulse. ',
      strength: 0.9,
    }],
    facts: [{
      node: 'person:alex',
      text: ' Alex is based in Lisbon. ',
      predicate: 'home_base',
      object_text: ' Lisbon ',
      valid_from: '2026-07-01T07:00:00+07:00',
      change_cue: true,
      source_event_refs: ['event:moved'],
      confidence: 0.9,
      domain: 'real',
    }],
    events: [{
      client_id: 'event:moved',
      title: ' Alex moved ',
      summary: ' Alex changed home base to Lisbon. ',
      entity_refs: ['person:alex'],
      sentiment: ' restoration ',
      emotional_weight: 0.3,
      confidence: 0.9,
      domain: 'real',
      occurred_at: '2026-07-01T07:00:00+07:00',
      anchor: false,
      biometrics: {
        hrv: 58,
        sleep_quality: 0.8,
        stress_proxy: 0.2,
        hr_trend: 'stable',
        hrv_trend: 'rising',
        workout: true,
      },
      emotions: { joy: 0.3, trust: 0.6 },
    }],
    continuity: {
      thread_id: 'pulse-pilot',
      session_id: 'session-2026-07-11',
      summary: ' Stopped after agreeing on scoped team storage. ',
      decisions: [' Use a dedicated team store. '],
      open_loops: [' Wire the team graph gateway. '],
      do_not_repeat: [],
      emotional_anchors: [],
      state_signals: [],
      active_threads: ['U10'],
      review_insights: [],
    },
    raw_input_included: false as const,
    active_context: {
      project_id: 'project-pulse',
      repo_id: 'repo-pulse',
      agent_id: 'agent-bound',
      session_id: 'session-2026-07-11',
    },
    target_scope: { type: 'project', id: 'project-pulse' },
    privacy_tier: 'normal',
    retention: 'project',
    expires_at: '2026-08-01T07:00:00+07:00',
    idempotency_key: 'graph-request-001',
  };
}

function storedTeamGraphDeltaResult(suffix = '001') {
  const projectionKinds = expectedTeamGraphProjectionKinds(
    validateTeamGraphDeltaInput(baseTeamGraphDelta()),
  );
  return {
    schema: 'pulse.team.graph_delta_result.v1' as const,
    object_id: `team_graph_object_${suffix}`,
    audit_event_id: `team_graph_audit_${suffix}`,
    status: 'stored' as const,
    projection_state: 'pending' as const,
    projection_jobs: projectionKinds.map((kind) => ({
      kind,
      job_id: `team_graph_job_${kind}_${suffix}`,
      state: 'pending' as const,
    })),
    fully_projected: false as const,
    replayed: false,
    fallback: false as const,
  };
}

function assertTeamGraphRejected(input: unknown, expected: RegExp): void {
  assert.throws(
    () => validateTeamGraphDeltaInput(input),
    (error: unknown) => error instanceof TeamContractError &&
      error.code === 'invalid_team_contract' && expected.test(error.message),
  );
}

const EXPECTED_TEAM_TOOLS = [
  ['pulse_team_status', 'pulse.team.status.v1'],
  ['pulse_team_remember', 'pulse.team.memory.v1'],
  ['pulse_team_graph_delta', 'pulse.team.graph_delta.v1'],
  ['pulse_team_recall', 'pulse.team.recall.v1'],
  ['pulse_team_context_query', 'pulse.team.context.v1'],
  ['pulse_team_resume', 'pulse.team.resume.v1'],
  ['pulse_team_inspect', 'pulse.team.inspect.v1'],
  ['pulse_team_audit', 'pulse.team.audit.v1'],
  ['pulse_team_delete', 'pulse.team.delete.v1'],
  ['pulse_team_delete_status', 'pulse.team.delete_status.v1'],
] as const;

test('team capabilities are derived from the allowlisted MCP operation', () => {
  assert.deepEqual(requiredTeamCapabilities({ method: 'initialize' }), [TEAM_BASELINE_CAPABILITY]);
  assert.deepEqual(requiredTeamCapabilities({ method: 'tools/list' }), [TEAM_BASELINE_CAPABILITY]);
  assert.deepEqual(requiredTeamCapabilities({
    method: 'tools/call', params: { name: 'pulse_team_status' },
  }), ['pulse:connect', 'pulse:status']);
  assert.deepEqual(requiredTeamCapabilities({
    method: 'tools/call', params: { name: 'pulse_team_remember' },
  }), ['pulse:connect', 'pulse:write']);
  assert.deepEqual(requiredTeamCapabilities({
    method: 'tools/call', params: { name: 'pulse_team_recall' },
  }), ['pulse:connect', 'pulse:read']);
  assert.deepEqual(requiredTeamCapabilities({
    method: 'tools/call', params: { name: 'pulse_team_audit' },
  }), ['pulse:audit', 'pulse:connect']);
  assert.deepEqual(requiredTeamCapabilities({
    method: 'tools/call', params: { name: 'pulse_team_delete' },
  }), ['pulse:connect', 'pulse:delete']);
  assert.throws(
    () => requiredTeamCapabilities({ method: 'tools/call', params: { name: 'pulse_status' } }),
    /unknown team tool/i,
  );
});

function teamContext(principalId: string): Readonly<TeamPrincipalContext> {
  return Object.freeze({
    version: 'pulse.team.principal_context.v1',
    request_id: `request-${principalId}`,
    store_id: 'store_test',
    team_id: 'team_test',
    principal_id: principalId,
    principal_kind: 'agent',
    oauth_client_key: 'a'.repeat(64),
    human_principal_id: `human-${principalId}`,
    agent_binding_id: `binding-${principalId}`,
    membership_id: `membership-${principalId}`,
    membership_role: 'member',
    team_auth_epoch: 1,
    principal_auth_epoch: 1,
    binding_auth_epoch: 1,
    membership_auth_epoch: 1,
    capabilities: ['pulse:connect', 'pulse:read'],
  });
}

async function startTeamRegistryServer(
  domainFactory?: (context: Readonly<TeamPrincipalContext>) => Readonly<BoundTeamDomain>,
  securityEventSink?: (event: GatewaySecurityEventInput) => void,
) {
  const seenPrincipals: string[] = [];
  const httpServer = createServer(async (req, res) => {
    const requestedPrincipal = typeof req.headers['x-test-principal'] === 'string'
      ? req.headers['x-test-principal']
      : 'missing';
    seenPrincipals.push(requestedPrincipal);
    const context = teamContext(requestedPrincipal);
    const requestServer = createPulseMcpServer(
      'team-remote', context, domainFactory?.(context), securityEventSink,
    );
    const transport = new StreamableHTTPServerTransport({ sessionIdGenerator: undefined });
    let cleaned = false;
    const cleanup = () => {
      if (cleaned) return;
      cleaned = true;
      void Promise.allSettled([transport.close(), requestServer.close()]);
    };
    try {
      await requestServer.connect(transport);
      res.once('close', cleanup);
      await transport.handleRequest(req, res);
    } catch {
      cleanup();
      if (!res.headersSent) res.writeHead(500);
      res.end();
    }
  });
  await new Promise<void>((resolve, reject) => {
    httpServer.once('error', reject);
    httpServer.listen(0, '127.0.0.1', () => resolve());
  });
  const address = httpServer.address();
  assert.ok(address && typeof address === 'object');
  return {
    seenPrincipals,
    url: `http://127.0.0.1:${address.port}/mcp`,
    stop: () => new Promise<void>((resolve) => httpServer.close(() => resolve())),
  };
}

function toolJSON(result: { content?: Array<{ type: string; text?: string }> }) {
  assert.equal(result.content?.[0]?.type, 'text');
  return JSON.parse(result.content?.[0]?.text ?? 'null');
}

test('pulse_team_remember advertises a closed pulse.team.memory.v1 input contract', () => {
  const descriptor = TEAM_TOOL_DESCRIPTORS.find(({ name }) => name === 'pulse_team_remember');
  assert.ok(descriptor);
  assert.match(descriptor.description, /domain execution (?:is|are) active/i);
  assert.doesNotMatch(descriptor.description, /not active/i);
  const schema = descriptor.inputSchema as Record<string, unknown>;
  assert.equal(schema.additionalProperties, false);
  assert.deepEqual(schema.required, [
    'schema',
    'source',
    'items',
    'raw_input_included',
    'active_context',
    'privacy_tier',
    'retention',
    'idempotency_key',
  ]);
  const properties = schema.properties as Record<string, Record<string, unknown>>;
  assert.equal(properties.schema.const, 'pulse.team.memory.v1');
  assert.equal(properties.source.additionalProperties, false);
  assert.equal(properties.active_context.additionalProperties, false);
  assert.equal(properties.target_scope.additionalProperties, false);
  assert.equal(properties.items.maxItems, 20);
  assert.equal(
    (properties.items.items as Record<string, unknown>).additionalProperties,
    false,
  );
});

test('pulse_team_graph_delta advertises an exact closed active domain contract', () => {
  const descriptor = TEAM_TOOL_DESCRIPTORS.find(({ name }) => name === 'pulse_team_graph_delta');
  assert.ok(descriptor);
  assert.match(descriptor.description, /domain execution (?:is|are) active/i);
  assert.doesNotMatch(descriptor.description, /not active/i);
  const schema = descriptor.inputSchema as Record<string, unknown>;
  assert.equal(schema.additionalProperties, false);
  assert.deepEqual(schema.required, [
    'schema', 'source', 'nodes', 'edges', 'facts', 'events',
    'raw_input_included', 'active_context', 'privacy_tier', 'retention',
    'idempotency_key',
  ]);
  const properties = schema.properties as Record<string, Record<string, unknown>>;
  assert.equal(properties.schema.const, 'pulse.team.graph_delta.v1');
  for (const field of ['source', 'active_context', 'target_scope', 'continuity']) {
    assert.equal(properties[field].additionalProperties, false, `${field} must be closed`);
  }
  for (const [field, maximum] of [['nodes', 30], ['edges', 50], ['facts', 50], ['events', 20]] as const) {
    assert.equal(properties[field].maxItems, maximum);
    assert.equal((properties[field].items as Record<string, unknown>).additionalProperties, false);
  }
  const eventProperties = (
    properties.events.items as Record<string, Record<string, unknown>>
  ).properties as Record<string, unknown>;
  assert.equal(Object.hasOwn(eventProperties, 'claims'), false);
});

test('team read tools advertise exact closed v1 contracts without caller authority fields', () => {
  const expectations = [
    ['pulse_team_recall', 'pulse.team.recall.v1', [
      'schema', 'query', 'active_context', 'privacy_ceiling',
    ]],
    ['pulse_team_context_query', 'pulse.team.context.v1', [
      'schema', 'query', 'active_context', 'privacy_ceiling',
    ]],
    ['pulse_team_resume', 'pulse.team.resume.v1', ['schema', 'active_context']],
  ] as const;
  for (const [name, contract, required] of expectations) {
    const descriptor = TEAM_TOOL_DESCRIPTORS.find((candidate) => candidate.name === name);
    assert.ok(descriptor);
    assert.match(descriptor.description, /domain execution (?:is|are) active/i);
    assert.doesNotMatch(descriptor.description, /not active/i);
    const schema = descriptor.inputSchema as Record<string, unknown>;
    assert.equal(schema.additionalProperties, false);
    assert.deepEqual(schema.required, required);
    const properties = schema.properties as Record<string, Record<string, unknown>>;
    assert.equal(properties.schema.const, contract);
    assert.equal(properties.active_context.additionalProperties, false);
    for (const forbidden of [
      'actor_id', 'principal_id', 'human_principal_id', 'owner_id', 'team_id',
      'membership_id', 'agent_binding_id', 'role', 'scope_id',
    ]) {
      assert.equal(Object.hasOwn(properties, forbidden), false, `${name}.${forbidden}`);
    }
  }
});

test('team read bodies canonicalize defaults without mutating callers', () => {
  const recall = baseTeamRecall();
  const recallOriginal = structuredClone(recall);
  const recallBody = canonicalTeamRecallBody(recall);
  assert.deepEqual(recall, recallOriginal);
  assert.deepEqual(recallBody.value, {
    ...recallOriginal, limit: 5,
  });

  const context = baseTeamContextQuery();
  const contextOriginal = structuredClone(context);
  const contextBody = canonicalTeamContextQueryBody(context);
  assert.deepEqual(context, contextOriginal);
  assert.deepEqual(contextBody.value, {
    ...contextOriginal, limit: 10, include_trace: false, graph_mode: 'anchored',
  });

  const resume = baseTeamResume();
  const resumeOriginal = structuredClone(resume);
  const resumeBody = canonicalTeamResumeBody(resume);
  assert.deepEqual(resume, resumeOriginal);
  assert.deepEqual(resumeBody.value, { ...resumeOriginal, limit: 20 });

  for (const body of [recallBody, contextBody, resumeBody]) {
    assert.equal(body.text, JSON.stringify(body.value));
    assert.deepEqual(body.bytes, Buffer.from(body.text, 'utf8'));
  }
});

test('team read bodies reject authority spoofing, unknown fields, unsafe text, and missing resume locator', () => {
  for (const [builder, canonicalizer] of [
    [baseTeamRecall, canonicalTeamRecallBody],
    [baseTeamContextQuery, canonicalTeamContextQueryBody],
    [baseTeamResume, canonicalTeamResumeBody],
  ] as const) {
    for (const [field, value] of [
      ['principal_id', 'principal-spoofed'],
      ['team_id', 'team-spoofed'],
      ['owner_id', 'owner-spoofed'],
      ['unexpected', true],
    ] as const) {
      assert.throws(() => canonicalizer({ ...builder(), [field]: value }), TeamContractError);
    }
  }
  assert.throws(
    () => canonicalTeamRecallBody({ ...baseTeamRecall(), query: 'api_key=sk-test-secret-value' }),
    TeamContractError,
  );
  assert.throws(
    () => canonicalTeamContextQueryBody({
      ...baseTeamContextQuery(), active_context: { ...teamReadActiveContext(), project_id: '../secret' },
    }),
    TeamContractError,
  );
  assert.throws(
    () => canonicalTeamRecallBody({
      ...baseTeamRecall(), active_context: { ...teamReadActiveContext(), team_id: 'team-spoofed' },
    }),
    TeamContractError,
  );
  assert.throws(
    () => canonicalTeamResumeBody({ schema: 'pulse.team.resume.v1', active_context: {}, limit: 20 }),
    /thread_id|project_id|session_id/i,
  );
});

test('team read result validators accept exact returned-only shapes and reject hidden influence', () => {
  assert.deepEqual(validateTeamRecallResult(storedTeamRecallResult(), 5), storedTeamRecallResult());
  assert.deepEqual(
    validateTeamContextQueryResult(storedTeamContextResult(), 10, true),
    storedTeamContextResult(),
  );
  assert.deepEqual(validateTeamResumeResult(storedTeamResumeResult(), 20), storedTeamResumeResult());

  assert.throws(() => validateTeamRecallResult({
    ...storedTeamRecallResult(), returned_count: 2,
  }, 5), /response/i);
  assert.throws(() => validateTeamRecallResult({
    ...storedTeamRecallResult(), total_count: 42,
  }, 5), /response/i);
  assert.throws(() => validateTeamRecallResult({
    ...storedTeamRecallResult(), fallback: true,
  }, 5), /response/i);
  assert.throws(() => validateTeamContextQueryResult({
    ...storedTeamContextResult(),
    trace: { stages: [{ kind: 'lexical', returned_object_ids: ['hidden_object_001'] }] },
  }, 10, true), /response/i);
  assert.throws(() => validateTeamContextQueryResult({
    ...storedTeamContextResult(),
    events: [{ ...storedTeamContextResult().events[0], object_id: 'team_fact_001' }],
  }, 10, true), /response/i);
  assert.throws(() => validateTeamContextQueryResult({
    ...storedTeamContextResult(), hidden_candidate_count: 1,
  }, 10, true), /response/i);
  assert.throws(() => validateTeamContextQueryResult(storedTeamContextResult(), 10, false), /response/i);
  assert.throws(() => validateTeamResumeResult({
    ...storedTeamResumeResult(), returned_count: 4,
  }, 20), /response/i);
  assert.throws(() => validateTeamResumeResult({
    ...storedTeamResumeResult(), sections: {
      ...storedTeamResumeResult().sections,
      evidence_refs: ['/Users/alex/private.txt'],
    },
  }, 20), /response/i);
});

test('team graph validator canonicalizes clean content without mutating the caller', () => {
  const input = baseTeamGraphDelta();
  input.nodes[0].aliases = [' я ', 'alpha', ' бета '];
  input.events[0].entity_refs = ['project:pulse', 'person:alex'];
  const original = structuredClone(input);
  const clean = validateTeamGraphDeltaInput(input);

  assert.deepEqual(input, original);
  assert.equal(clean.source.timestamp, '2026-07-11T05:00:00.000Z');
  assert.deepEqual(clean.nodes[0].aliases, ['alpha', 'бета', 'я']);
  assert.equal(clean.nodes[0].canonical_name, 'Alex');
  assert.equal(clean.edges[0].summary, 'Alex contributes to Pulse.');
  assert.equal(clean.facts[0].object_text, 'Lisbon');
  assert.equal(clean.facts[0].valid_from, '2026-07-01T00:00:00.000Z');
  assert.deepEqual(clean.events[0].entity_refs, ['person:alex', 'project:pulse']);
  assert.equal(clean.events[0].occurred_at, '2026-07-01T00:00:00.000Z');
  assert.equal(clean.continuity?.summary, 'Stopped after agreeing on scoped team storage.');
  assert.deepEqual(expectedTeamGraphProjectionKinds(clean), [
    'claim', 'continuity', 'embedding', 'graph',
  ]);
});

test('team graph and local semantic delta contracts cross-reject one another', () => {
  const local = structuredClone(baseTeamGraphDelta()) as Record<string, unknown>;
  local.schema = 'pulse.semantic_delta.v1';
  assertTeamGraphRejected(local, /schema|pulse\.team\.graph_delta\.v1/i);
  assert.throws(
    () => validateDelta(baseTeamGraphDelta()),
    (error: unknown) => error instanceof ContractError && /schema|pulse\.semantic_delta\.v1/i.test(error.message),
  );
});

test('team graph rejects authority, nested policy, local scope, and event claims', () => {
  for (const field of [
    'actor', 'principal_id', 'human_principal_id', 'owner_principal_id',
    'team_id', 'role', 'membership_role', 'oauth_client_key', 'body_digest',
  ]) {
    const input = baseTeamGraphDelta() as Record<string, unknown>;
    input[field] = 'spoofed-authority';
    assertTeamGraphRejected(input, new RegExp(field));
  }

  const nestedCases: Array<[string, string, unknown]> = [
    ['source', 'thread_id', 'spoofed-thread'],
    ['source', 'project_id', 'spoofed-project'],
    ['active_context', 'team_id', 'spoofed-team'],
    ['target_scope', 'owner_principal_id', 'spoofed-owner'],
    ['nodes.0', 'privacy_tier', 'normal'],
    ['facts.0', 'scope_type', 'project'],
    ['facts.0', 'scope_id', 'project-pulse'],
    ['facts.0', 'visibility', 'shared'],
    ['events.0', 'privacy_tier', 'normal'],
    ['events.0', 'claims', [{ subject: 'Alex', predicate: 'role', object: 'builder' }]],
  ];
  for (const [path, field, value] of nestedCases) {
    const input = baseTeamGraphDelta() as unknown as Record<string, unknown>;
    let target: Record<string, unknown>;
    if (path.endsWith('.0')) {
      const collection = input[path.slice(0, -2)] as Array<Record<string, unknown>>;
      target = collection[0];
    } else {
      target = input[path] as Record<string, unknown>;
    }
    target[field] = value;
    assertTeamGraphRejected(input, new RegExp(field));
  }
});

test('team graph uses facts as the only structured claim ingress', () => {
  const clean = validateTeamGraphDeltaInput(baseTeamGraphDelta());
  assert.equal(clean.facts[0].predicate, 'home_base');
  assert.equal(clean.facts[0].object_text, 'Lisbon');
  assert.equal(clean.facts[0].change_cue, true);
  assert.deepEqual(clean.facts[0].source_event_refs, ['event:moved']);

  for (const mutate of [
    (fact: Record<string, unknown>) => { delete fact.object_text; },
    (fact: Record<string, unknown>) => { delete fact.predicate; },
    (fact: Record<string, unknown>) => { fact.object_text = null; },
    (fact: Record<string, unknown>) => { fact.change_cue = null; },
    (fact: Record<string, unknown>) => { fact.source_event_refs = ['event:missing']; },
  ]) {
    const input = baseTeamGraphDelta();
    mutate(input.facts[0] as unknown as Record<string, unknown>);
    assertTeamGraphRejected(input, /predicate|object_text|change_cue|source_event_refs|unknown event/i);
  }

  const unstructured = baseTeamGraphDelta();
  delete (unstructured.facts[0] as Partial<typeof unstructured.facts[0]>).predicate;
  delete (unstructured.facts[0] as Partial<typeof unstructured.facts[0]>).object_text;
  delete (unstructured.facts[0] as Partial<typeof unstructured.facts[0]>).valid_from;
  delete (unstructured.facts[0] as Partial<typeof unstructured.facts[0]>).change_cue;
  delete (unstructured.facts[0] as Partial<typeof unstructured.facts[0]>).source_event_refs;
  delete (unstructured as Partial<typeof unstructured>).continuity;
  assert.deepEqual(expectedTeamGraphProjectionKinds(validateTeamGraphDeltaInput(unstructured)), [
    'embedding', 'graph',
  ]);

  const decoratedUnstructured = structuredClone(unstructured);
  (decoratedUnstructured.facts[0] as Record<string, unknown>).change_cue = false;
  assertTeamGraphRejected(decoratedUnstructured, /structured|predicate|change_cue/i);
});

test('team graph validates all references, duplicates, and exact set limits', () => {
  const unknownNode = baseTeamGraphDelta();
  unknownNode.edges[0].from = 'person:missing';
  assertTeamGraphRejected(unknownNode, /edges\[0\]\.from|unknown node/i);

  const unknownEventEntity = baseTeamGraphDelta();
  unknownEventEntity.events[0].entity_refs = ['person:missing'];
  assertTeamGraphRejected(unknownEventEntity, /entity_refs|unknown node/i);

  const duplicateNode = baseTeamGraphDelta();
  duplicateNode.nodes.push(structuredClone(duplicateNode.nodes[0]));
  assertTeamGraphRejected(duplicateNode, /client_id|duplicate/i);

  const duplicateSemanticNode = baseTeamGraphDelta();
  duplicateSemanticNode.nodes.push({
    ...structuredClone(duplicateSemanticNode.nodes[0]),
    client_id: 'person:alex-duplicate',
    canonical_name: 'alex',
  });
  assertTeamGraphRejected(duplicateSemanticNode, /semantic node|duplicate/i);

  const duplicateEvent = baseTeamGraphDelta();
  duplicateEvent.events.push(structuredClone(duplicateEvent.events[0]));
  assertTeamGraphRejected(duplicateEvent, /client_id|duplicate/i);

  for (const field of ['aliases', 'entity_refs', 'source_event_refs'] as const) {
    const input = baseTeamGraphDelta();
    if (field === 'aliases') input.nodes[0].aliases = ['same', ' same '];
    if (field === 'entity_refs') input.events[0].entity_refs = ['person:alex', 'person:alex'];
    if (field === 'source_event_refs') input.facts[0].source_event_refs = ['event:moved', 'event:moved'];
    assertTeamGraphRejected(input, new RegExp(`${field}|duplicate`, 'i'));
  }

  const tooManyNodes = baseTeamGraphDelta();
  tooManyNodes.nodes = Array.from({ length: 31 }, (_, index) => ({
    ...structuredClone(baseTeamGraphDelta().nodes[0]), client_id: `node:${index}`,
  }));
  assertTeamGraphRejected(tooManyNodes, /nodes|30/i);

  const tooManyRefs = baseTeamGraphDelta();
  tooManyRefs.events[0].entity_refs = Array.from({ length: 21 }, (_, index) => `node:${index}`);
  assertTeamGraphRejected(tooManyRefs, /entity_refs|20/i);
});

test('team graph requires continuity keys to bind the exact active session', () => {
  for (const mutate of [
    (input: ReturnType<typeof baseTeamGraphDelta>) => { delete (input.continuity as Partial<typeof input.continuity>).thread_id; },
    (input: ReturnType<typeof baseTeamGraphDelta>) => { delete (input.continuity as Partial<typeof input.continuity>).session_id; },
    (input: ReturnType<typeof baseTeamGraphDelta>) => { input.continuity.session_id = 'another-session'; },
    (input: ReturnType<typeof baseTeamGraphDelta>) => { delete (input.active_context as Partial<typeof input.active_context>).session_id; },
  ]) {
    const input = baseTeamGraphDelta();
    mutate(input);
    assertTeamGraphRejected(input, /thread_id|session_id|active_context/i);
  }

  const sessionTarget = baseTeamGraphDelta();
  sessionTarget.target_scope = { type: 'session', id: 'another-session' };
  assertTeamGraphRejected(sessionTarget, /target_scope|session_id/i);

  const continuityOnly = baseTeamGraphDelta();
  continuityOnly.nodes = [];
  continuityOnly.edges = [];
  continuityOnly.facts = [];
  continuityOnly.events = [];
  assert.deepEqual(expectedTeamGraphProjectionKinds(validateTeamGraphDeltaInput(continuityOnly)), ['continuity']);

  delete (continuityOnly as Partial<typeof continuityOnly>).continuity;
  assertTeamGraphRejected(continuityOnly, /graph content|continuity/i);
});

test('team graph rejects omitted/null required scalars and unsafe content', () => {
  const cases: Array<[RegExp, (input: ReturnType<typeof baseTeamGraphDelta>) => void]> = [
    [/confidence/i, (input) => { delete (input.facts[0] as Partial<typeof input.facts[0]>).confidence; }],
    [/confidence/i, (input) => { (input.events[0] as Record<string, unknown>).confidence = null; }],
    [/domain/i, (input) => { delete (input.nodes[0] as Partial<typeof input.nodes[0]>).domain; }],
    [/anchor/i, (input) => { (input.events[0] as Record<string, unknown>).anchor = null; }],
    [/raw_input_included/i, (input) => { delete (input as Partial<typeof input>).raw_input_included; }],
    [/raw_input_included/i, (input) => { (input as Record<string, unknown>).raw_input_included = null; }],
    [/timestamp|rfc3339/i, (input) => { input.source.timestamp = '2026-02-30T00:00:00Z'; }],
    [/secret|summary/i, (input) => { input.events[0].summary = 'Bearer token=sk-ABCDEF0123456789 was copied.'; }],
    [/path|text/i, (input) => { input.facts[0].text = 'Read /Users/example/private/notes.txt.'; }],
    [/transcript|summary/i, (input) => {
      input.continuity.summary = 'user: one\nassistant: two\nuser: three\nassistant: four\nuser: five\nassistant: six';
    }],
    [/emotions|joy/i, (input) => { input.events[0].emotions.joy = Number.NaN; }],
    [/biometrics|hrv/i, (input) => { input.events[0].biometrics.hrv = 301; }],
    [/hr_trend/i, (input) => { input.events[0].biometrics.hr_trend = 'sideways'; }],
  ];
  for (const [expected, mutate] of cases) {
    const input = baseTeamGraphDelta();
    mutate(input);
    assertTeamGraphRejected(input, expected);
  }
});

test('team contracts reject cross-runtime ambiguous Unicode before producing canonical bytes', () => {
  for (const malformed of [
    'broken\uD800text', 'broken\uDC00text',
    'line\u2028separator', 'paragraph\u2029separator',
  ]) {
    const graph = baseTeamGraphDelta();
    graph.events[0].summary = malformed;
    assertTeamGraphRejected(graph, /unicode|surrogate|well-formed/i);

    const memory = baseTeamRemember();
    memory.items[0].redacted_summary = malformed;
    assertTeamRememberRejected(memory, /unicode|surrogate|well-formed/i);
  }
});

test('team graph canonical body is exact, bounded, and carries the conditional job set', () => {
  const canonical = canonicalTeamGraphDeltaBody(baseTeamGraphDelta());
  assert.deepEqual(canonical.value, validateTeamGraphDeltaInput(baseTeamGraphDelta()));
  assert.equal(canonical.text, JSON.stringify(canonical.value));
  assert.deepEqual(canonical.bytes, Buffer.from(canonical.text, 'utf8'));
  assert.equal(
    createHash('sha256').update(canonical.bytes).digest('hex'),
    '15b83a96a9b2ce3f4a286a2ca077dc1ea4f02d44dd47139af9695df62f65a0dc',
  );
  assert.deepEqual(canonical.projectionKinds, ['claim', 'continuity', 'embedding', 'graph']);
  assert.match(canonical.text, /"timestamp":"2026-07-11T05:00:00.000Z"/);
  assert.match(canonical.text, /"aliases":\["Alexander","Alexey"\]/);

  const defaultTimes = baseTeamGraphDelta();
  delete (defaultTimes.facts[0] as Partial<typeof defaultTimes.facts[0]>).valid_from;
  delete (defaultTimes.events[0] as Partial<typeof defaultTimes.events[0]>).occurred_at;
  const defaulted = validateTeamGraphDeltaInput(defaultTimes);
  assert.equal(defaulted.facts[0].valid_from, defaulted.source.timestamp);
  assert.equal(defaulted.events[0].occurred_at, defaulted.source.timestamp);

  const oversized = baseTeamGraphDelta();
  const large = '🫧'.repeat(1200);
  for (const field of [
    'decisions', 'open_loops', 'do_not_repeat', 'emotional_anchors',
    'state_signals', 'active_threads', 'review_insights',
  ] as const) {
    oversized.continuity[field] = Array.from({ length: 20 }, () => large);
  }
  assertTeamGraphRejected(oversized, /256|body|large/i);
});

test('team graph result accepts only the exact stored pending projection set', () => {
  const expected = expectedTeamGraphProjectionKinds(validateTeamGraphDeltaInput(baseTeamGraphDelta()));
  const response = {
    schema: 'pulse.team.graph_delta_result.v1',
    object_id: 'team_object_graph_001',
    audit_event_id: 'team_audit_graph_001',
    status: 'stored',
    projection_state: 'pending',
    projection_jobs: expected.map((kind) => ({
      kind, job_id: `team_job_${kind}`, state: 'pending',
    })),
    fully_projected: false,
    replayed: false,
    fallback: false,
  };
  assert.deepEqual(validateTeamGraphDeltaResult(response, expected), response);

  for (const mutate of [
    (value: Record<string, unknown>) => { value.extra = 'server detail'; },
    (value: Record<string, unknown>) => { value.status = 'ready'; },
    (value: Record<string, unknown>) => { value.fully_projected = true; },
    (value: Record<string, unknown>) => {
      value.projection_jobs = [...(value.projection_jobs as unknown[])].reverse();
    },
    (value: Record<string, unknown>) => {
      value.projection_jobs = (value.projection_jobs as unknown[]).slice(1);
    },
  ]) {
    const invalid = structuredClone(response) as unknown as Record<string, unknown>;
    mutate(invalid);
    assert.throws(() => validateTeamGraphDeltaResult(invalid, expected), /team graph delta response/i);
  }

  assert.equal(new TeamDomainError('invalid_team_graph_delta').code, 'invalid_team_graph_delta');
});

test('team remember validator normalizes a clean envelope without inventing a target', () => {
  const input = baseTeamRemember();
  input.source.host = ' claude-code ';
  input.items[0].redacted_summary = '  Use the dedicated team store for the pilot.  ';
  input.items[0].tags = [' pilot ', 'storage'];
  const clean = validateTeamRemember(input);

  assert.equal(clean.schema, 'pulse.team.memory.v1');
  assert.equal((clean.source as Record<string, unknown>).host, 'claude-code');
  assert.equal(
    ((clean.items as Array<Record<string, unknown>>)[0]).redacted_summary,
    'Use the dedicated team store for the pilot.',
  );
  assert.deepEqual(((clean.items as Array<Record<string, unknown>>)[0]).tags, ['pilot', 'storage']);
  assert.equal(Object.hasOwn(clean, 'target_scope'), false);
});

test('team and local memory contracts reject cross-sent envelopes', () => {
  const local = baseTeamRemember() as Record<string, unknown>;
  local.schema = 'pulse.memory_capsule.v1';
  assertTeamRememberRejected(local, /schema|pulse\.team\.memory\.v1/i);

  assert.throws(
    () => validateCapsule(baseTeamRemember()),
    (error: unknown) => error instanceof ContractError && /schema|pulse\.memory_capsule\.v1/i.test(error.message),
  );
});

test('team remember rejects caller-supplied identity and authority fields at every envelope boundary', () => {
  const topLevelFields = [
    'actor', 'principal_id', 'human_principal_id', 'owner_principal_id', 'team_id',
    'role', 'membership_role', 'oauth_client_key', 'agent_id', 'body_digest',
  ];
  for (const field of topLevelFields) {
    const input = baseTeamRemember() as Record<string, unknown>;
    input[field] = 'spoofed-authority';
    assertTeamRememberRejected(input, new RegExp(field));
  }

  const sourceSpoof = baseTeamRemember();
  (sourceSpoof.source as Record<string, unknown>).principal_id = 'spoofed-principal';
  assertTeamRememberRejected(sourceSpoof, /source\.principal_id/i);

  const activeSpoof = baseTeamRemember();
  (activeSpoof.active_context as Record<string, unknown>).team_id = 'spoofed-team';
  assertTeamRememberRejected(activeSpoof, /active_context\.team_id/i);

  const targetSpoof = baseTeamRemember() as Record<string, unknown>;
  targetSpoof.target_scope = { type: 'personal', owner_principal_id: 'spoofed-owner' };
  assertTeamRememberRejected(targetSpoof, /target_scope\.owner_principal_id/i);
});

test('team remember accepts only server-derivable personal targets and explicit bounded non-team targets', () => {
  const personal = baseTeamRemember() as Record<string, unknown>;
  personal.target_scope = { type: 'personal' };
  assert.deepEqual(validateTeamRemember(personal).target_scope, { type: 'personal' });

  for (const type of ['project', 'repo', 'agent', 'session']) {
    const input = baseTeamRemember() as Record<string, unknown>;
    input.target_scope = { type, id: ` ${type}-scope ` };
    assert.deepEqual(validateTeamRemember(input).target_scope, { type, id: `${type}-scope` });
  }

  const personalSpoof = baseTeamRemember() as Record<string, unknown>;
  personalSpoof.target_scope = { type: 'personal', id: 'someone-else' };
  assertTeamRememberRejected(personalSpoof, /personal|target_scope\.id/i);

  for (const type of ['project', 'repo', 'agent', 'session']) {
    const missingID = baseTeamRemember() as Record<string, unknown>;
    missingID.target_scope = { type };
    assertTeamRememberRejected(missingID, /target_scope\.id/i);
  }

  const team = baseTeamRemember() as Record<string, unknown>;
  team.target_scope = { type: 'team', id: 'team-spoofed' };
  assertTeamRememberRejected(team, /team|target_scope\.type/i);
});

test('team remember rejects raw, transcript, secret, path-like, and unknown content', () => {
  const rawFlag = baseTeamRemember() as Record<string, unknown>;
  rawFlag.raw_input_included = true;
  assertTeamRememberRejected(rawFlag, /raw_input_included/i);

  const rawField = baseTeamRemember() as Record<string, unknown>;
  rawField.raw_input = 'the complete user prompt';
  assertTeamRememberRejected(rawField, /raw_input/i);

  const unsafeSummaries = [
    ['transcript', 'user: one\nassistant: two\nuser: three\nassistant: four\nuser: five\nassistant: six'],
    ['secret', 'The bearer token=sk-ABCDEF0123456789 was copied here.'],
    ['path', 'Read the source from /Users/example/private/notes.txt.'],
    ['path', 'Read the source from (/Users/example/private/notes.txt).'],
    ['path', 'Read the source from C:\\Users\\example\\private\\notes.txt.'],
  ] as const;
  for (const [reason, summary] of unsafeSummaries) {
    const input = baseTeamRemember();
    input.items[0].redacted_summary = summary;
    assertTeamRememberRejected(input, new RegExp(`${reason}|redacted_summary`, 'i'));
  }

  const unsafeTag = baseTeamRemember();
  unsafeTag.items[0].tags = ['api_key'];
  assertTeamRememberRejected(unsafeTag, /tags\[0\]|secret/i);

  const localPolicyField = baseTeamRemember();
  (localPolicyField.items[0] as Record<string, unknown>).privacy_tier = 'normal';
  assertTeamRememberRejected(localPolicyField, /items\[0\]\.privacy_tier/i);
});

test('team remember enforces closed nested shapes, enums, limits, and RFC3339 times', () => {
  const cases: Array<[RegExp, (input: ReturnType<typeof baseTeamRemember>) => void]> = [
    [/source\.host/i, (input) => { input.source.host = 'unknown-host'; }],
    [/conversation_scope/i, (input) => { input.source.conversation_scope = 'whole_chat'; }],
    [/timestamp|rfc3339/i, (input) => { input.source.timestamp = 'yesterday'; }],
    [/kind/i, (input) => { input.items[0].kind = 'rumor'; }],
    [/confidence/i, (input) => { input.items[0].confidence = 2; }],
    [/evidence_hint/i, (input) => { input.items[0].evidence_hint = 'the_model_said'; }],
    [/redacted_summary/i, (input) => { input.items[0].redacted_summary = ' '; }],
    [/redacted_summary|1200/i, (input) => { input.items[0].redacted_summary = 'x'.repeat(1201); }],
    [/privacy_tier/i, (input) => { input.privacy_tier = 'public'; }],
    [/retention/i, (input) => { input.retention = 'forever'; }],
    [/expires_at|rfc3339/i, (input) => {
      (input as Record<string, unknown>).expires_at = 'tomorrow';
    }],
    [/idempotency_key/i, (input) => { input.idempotency_key = 'short'; }],
  ];
  for (const [expected, mutate] of cases) {
    const input = baseTeamRemember();
    mutate(input);
    assertTeamRememberRejected(input, expected);
  }

  const noItems = baseTeamRemember();
  noItems.items = [] as unknown as typeof noItems.items;
  assertTeamRememberRejected(noItems, /items/i);

  const tooMany = baseTeamRemember();
  tooMany.items = Array.from({ length: 21 }, () => structuredClone(baseTeamRemember().items[0]));
  assertTeamRememberRejected(tooMany, /items|20/i);

  const invalidContext = baseTeamRemember();
  invalidContext.active_context.project_id = '/Users/example/project';
  assertTeamRememberRejected(invalidContext, /active_context\.project_id/i);
});

test('team remember normalization is pure and preserves an explicit expiry', () => {
  const input = baseTeamRemember() as ReturnType<typeof baseTeamRemember> & { expires_at: string };
  input.source.timestamp = ' 2026-07-11t12:00:00+07:00 ';
  input.expires_at = ' 2026-07-12T12:00:00+07:00 ';
  const original = structuredClone(input);
  const clean = validateTeamRemember(input);
  assert.deepEqual(input, original);
  assert.equal((clean.source as Record<string, unknown>).timestamp, '2026-07-11T05:00:00.000Z');
  assert.equal(clean.expires_at, '2026-07-12T05:00:00.000Z');
});

test('team remember canonical JSON is stable and signs the exact normalized bytes', () => {
  const input = baseTeamRemember() as ReturnType<typeof baseTeamRemember> & {
    target_scope: { type: string; id: string };
    expires_at: string;
  };
  input.source.timestamp = '2026-07-11T12:00:00+07:00';
  input.target_scope = { type: 'project', id: 'project-pulse' };
  input.expires_at = '2026-07-12T12:00:00+07:00';

  const canonical = canonicalTeamRememberBody(input);
  assert.equal(
    canonical.text,
    '{"schema":"pulse.team.memory.v1","source":{"host":"claude-code","conversation_scope":"current_turn","timestamp":"2026-07-11T05:00:00.000Z"},"items":[{"kind":"decision","redacted_summary":"Use the dedicated team store for the pilot.","confidence":0.9,"evidence_hint":"current_turn","tags":["pilot","storage"]}],"raw_input_included":false,"active_context":{"project_id":"project-pulse","repo_id":"repo-pulse","agent_id":"agent-bound","session_id":"session-2026-07-11"},"target_scope":{"type":"project","id":"project-pulse"},"privacy_tier":"normal","retention":"project","expires_at":"2026-07-12T05:00:00.000Z","idempotency_key":"remember-request-001"}',
  );
  assert.deepEqual(canonical.bytes, Buffer.from(canonical.text, 'utf8'));
  assert.deepEqual(canonical.value, validateTeamRememberInput(input));
});

test('team remember accepts only the exact stored/pending result envelope', () => {
  const response = {
    schema: 'pulse.team.memory_result.v1',
    object_id: 'team_object_001',
    audit_event_id: 'team_audit_001',
    capsule_ids: ['team_capsule_001'],
    status: 'stored',
    projection_state: 'pending',
    projection_jobs: [
      { kind: 'embedding', job_id: 'team_job_embedding', state: 'pending' },
      { kind: 'event', job_id: 'team_job_event', state: 'pending' },
    ],
    fully_projected: false,
    replayed: false,
    fallback: false,
  };
  assert.deepEqual(validateTeamRememberResult(response, 1), response);

  for (const mutate of [
    (value: Record<string, unknown>) => { value.extra = 'server detail'; },
    (value: Record<string, unknown>) => { delete value.audit_event_id; },
    (value: Record<string, unknown>) => { value.status = 'ready'; },
    (value: Record<string, unknown>) => { value.fully_projected = true; },
    (value: Record<string, unknown>) => { value.fallback = true; },
    (value: Record<string, unknown>) => {
      value.projection_jobs = [...(value.projection_jobs as unknown[])].reverse();
    },
    (value: Record<string, unknown>) => { value.capsule_ids = []; },
  ]) {
    const invalid = structuredClone(response) as unknown as Record<string, unknown>;
    mutate(invalid);
    assert.throws(() => validateTeamRememberResult(invalid, 1), /team memory response/i);
  }
});

test('team remember secret guard accepts harmless words containing sk-', () => {
  const input = baseTeamRemember();
  input.items[0].redacted_summary = 'Use risk-based access checks for every team write.';
  assert.equal(
    ((validateTeamRemember(input).items as Array<Record<string, unknown>>)[0]).redacted_summary,
    'Use risk-based access checks for every team write.',
  );
});

test('team remember uses Unicode code-point limits and canonical unique tag ordering', () => {
  const input = baseTeamRemember();
  input.items[0].redacted_summary = '🫧'.repeat(1200);
  input.items[0].tags = ['я', 'alpha', 'бета'];
  const clean = validateTeamRememberInput(input);
  assert.equal(clean.items[0].redacted_summary, '🫧'.repeat(1200));
  assert.deepEqual(clean.items[0].tags, ['alpha', 'бета', 'я']);

  const tooLong = structuredClone(input);
  tooLong.items[0].redacted_summary += '🫧';
  assertTeamRememberRejected(tooLong, /redacted_summary|1200/i);

  const duplicate = baseTeamRemember();
  duplicate.items[0].tags = ['pilot', 'storage', 'pilot'];
  assertTeamRememberRejected(duplicate, /duplicate|tags/i);
});

test('team StreamableHTTP registry exposes only exact descriptors and request-local stubs', async () => {
  assert.deepEqual(
    TEAM_TOOL_DESCRIPTORS.map((tool) => tool.name),
    EXPECTED_TEAM_TOOLS.map(([name]) => name),
  );
  assert.ok(TEAM_TOOL_DESCRIPTORS.every((tool) => tool.inputSchema.type === 'object'));
  for (const [name, contract] of EXPECTED_TEAM_TOOLS) {
    const descriptor = TEAM_TOOL_DESCRIPTORS.find((tool) => tool.name === name);
    assert.ok(descriptor?.description?.includes(contract), `${name} must declare ${contract}`);
  }
  assert.equal(isTeamToolName('pulse_status'), false);
  assert.deepEqual(teamNotReadyResult('pulse_team_status'), {
    isError: true,
    content: [{
      type: 'text',
      text: JSON.stringify({
      schema: 'pulse.team.not_ready.v1',
      error: 'team_remote_not_ready',
      mode: 'team-remote',
      tool: 'pulse_team_status',
      fallback: false,
      }),
    }],
  });

  const server = await startTeamRegistryServer();
  const exercise = async (principalId: string) => {
    const client = new Client({ name: `team-${principalId}`, version: '0.0.0' });
    const transport = new StreamableHTTPClientTransport(new URL(server.url), {
      requestInit: { headers: { 'X-Test-Principal': principalId } },
    });
    await client.connect(transport);
    const tools = await client.listTools();
    assert.deepEqual(tools.tools.map(({ name }) => name), EXPECTED_TEAM_TOOLS.map(([name]) => name));
    assert.equal(tools.tools.some(({ name }) => name === 'pulse_status' || !name.startsWith('pulse_team_')), false);
    const status = await client.callTool({ name: 'pulse_team_status', arguments: {} });
    assert.deepEqual(toolJSON(status), {
      schema: 'pulse.team.not_ready.v1', error: 'team_remote_not_ready',
      mode: 'team-remote', tool: 'pulse_team_status', fallback: false,
    });
    const legacy = await client.callTool({ name: 'pulse_status', arguments: {} });
    assert.equal(legacy.isError, true);
    assert.doesNotMatch(legacy.content[0]?.type === 'text' ? legacy.content[0].text : '', /pulse_status/);
    await client.close();
  };
  try {
    await Promise.all([exercise('principal-a'), exercise('principal-b')]);
    assert.ok(server.seenPrincipals.includes('principal-a'));
    assert.ok(server.seenPrincipals.includes('principal-b'));
  } finally {
    await server.stop();
  }
});

test('pulse_team_remember dispatches only through its request-bound domain closure', async () => {
  const seen: Array<{ principal: string; input: unknown }> = [];
  const stored = {
    schema: 'pulse.team.memory_result.v1' as const,
    object_id: 'team_object_001',
    audit_event_id: 'team_audit_001',
    capsule_ids: ['team_capsule_001'],
    status: 'stored' as const,
    projection_state: 'pending' as const,
    projection_jobs: [
      { kind: 'embedding' as const, job_id: 'team_job_embedding', state: 'pending' as const },
      { kind: 'event' as const, job_id: 'team_job_event', state: 'pending' as const },
    ],
    fully_projected: false as const,
    replayed: false,
    fallback: false as const,
  };
  const server = await startTeamRegistryServer((context) => Object.freeze({
    remember: async (input: unknown) => {
      canonicalTeamRememberBody(input);
      seen.push({ principal: context.principal_id, input });
      return stored;
    },
  }));
  const client = new Client({ name: 'team-remember', version: '0.0.0' });
  const transport = new StreamableHTTPClientTransport(new URL(server.url), {
    requestInit: { headers: { 'X-Test-Principal': 'principal-writer' } },
  });
  try {
    await client.connect(transport);
    const result = await client.callTool({ name: 'pulse_team_remember', arguments: baseTeamRemember() });
    assert.notEqual(result.isError, true);
    assert.deepEqual(toolJSON(result), stored);
    assert.equal(seen.length, 1);
    assert.equal(seen[0].principal, 'principal-writer');

    const status = await client.callTool({ name: 'pulse_team_status', arguments: {} });
    assert.equal(status.isError, true);
    assert.equal(toolJSON(status).error, 'team_remote_not_ready');
    assert.equal(seen.length, 1, 'other team tools must remain stubs');
  } finally {
    await client.close();
    await server.stop();
  }
});

test('pulse_team_graph_delta uses isolated request-bound graph closures without local fallback', async () => {
  const calls: Array<{ principal: string; idempotencyKey: string }> = [];
  let rememberCalls = 0;
  const server = await startTeamRegistryServer((context) => Object.freeze({
    remember: async () => {
      rememberCalls++;
      throw new Error('memory domain must not receive graph calls');
    },
    graphDelta: async (input: unknown) => {
      const canonical = canonicalTeamGraphDeltaBody(input);
      calls.push({
        principal: context.principal_id,
        idempotencyKey: canonical.value.idempotency_key,
      });
      return storedTeamGraphDeltaResult(context.principal_id);
    },
  }));

  const exercise = async (principal: string) => {
    const client = new Client({ name: `team-graph-${principal}`, version: '0.0.0' });
    const transport = new StreamableHTTPClientTransport(new URL(server.url), {
      requestInit: { headers: { 'X-Test-Principal': principal } },
    });
    try {
      await client.connect(transport);
      const input = baseTeamGraphDelta();
      input.idempotency_key = `graph-request-${principal}`;
      const result = await client.callTool({ name: 'pulse_team_graph_delta', arguments: input });
      assert.notEqual(result.isError, true);
      assert.deepEqual(toolJSON(result), storedTeamGraphDeltaResult(principal));

      const legacy = await client.callTool({ name: 'pulse_graph_delta', arguments: input });
      assert.equal(legacy.isError, true);
      assert.equal(calls.filter((call) => call.principal === principal).length, 1);
    } finally {
      await client.close();
    }
  };

  try {
    await Promise.all([exercise('principal-a'), exercise('principal-b')]);
    assert.deepEqual(calls.sort((left, right) => left.principal.localeCompare(right.principal)), [
      { principal: 'principal-a', idempotencyKey: 'graph-request-principal-a' },
      { principal: 'principal-b', idempotencyKey: 'graph-request-principal-b' },
    ]);
    assert.equal(rememberCalls, 0);
  } finally {
    await server.stop();
  }
});

test('team read tools dispatch once through isolated request-bound closures without fallback', async () => {
  const calls: string[] = [];
  let mutationCalls = 0;
  const server = await startTeamRegistryServer(() => Object.freeze({
    remember: async () => { mutationCalls++; throw new Error('write fallback is unreachable'); },
    graphDelta: async () => { mutationCalls++; throw new Error('graph fallback is unreachable'); },
    recall: async (input: unknown) => {
      calls.push(`recall:${canonicalTeamRecallBody(input).value.query}`);
      return storedTeamRecallResult();
    },
    contextQuery: async (input: unknown) => {
      calls.push(`context:${canonicalTeamContextQueryBody(input).value.query}`);
      return storedTeamContextResult();
    },
    resume: async (input: unknown) => {
      calls.push(`resume:${canonicalTeamResumeBody(input).value.thread_id}`);
      return storedTeamResumeResult();
    },
  }));
  const client = new Client({ name: 'team-read-dispatch', version: '0.0.0' });
  const transport = new StreamableHTTPClientTransport(new URL(server.url), {
    requestInit: { headers: { 'X-Test-Principal': 'principal-reader' } },
  });
  try {
    await client.connect(transport);
    const recall = await client.callTool({ name: 'pulse_team_recall', arguments: baseTeamRecall() });
    const context = await client.callTool({
      name: 'pulse_team_context_query',
      arguments: { ...baseTeamContextQuery(), include_trace: true },
    });
    const resume = await client.callTool({ name: 'pulse_team_resume', arguments: baseTeamResume() });
    assert.deepEqual(toolJSON(recall), storedTeamRecallResult());
    assert.deepEqual(toolJSON(context), storedTeamContextResult());
    assert.deepEqual(toolJSON(resume), storedTeamResumeResult());
    assert.deepEqual(calls, [
      `recall:${baseTeamRecall().query}`,
      `context:${baseTeamContextQuery().query}`,
      `resume:${baseTeamResume().thread_id}`,
    ]);
    assert.equal(mutationCalls, 0);
  } finally {
    await client.close();
    await server.stop();
  }
});

test('team read tools return closed errors and never reach local fallback', async () => {
  for (const [name, call] of [
    ['pulse_team_recall', baseTeamRecall],
    ['pulse_team_context_query', baseTeamContextQuery],
    ['pulse_team_resume', baseTeamResume],
  ] as const) {
    const events: GatewaySecurityEventInput[] = [];
    const server = await startTeamRegistryServer(() => Object.freeze({
      recall: async () => { throw new TeamDomainError('principal_revoked'); },
      contextQuery: async () => { throw new TeamDomainError('principal_revoked'); },
      resume: async () => { throw new TeamDomainError('principal_revoked'); },
    }), (event) => events.push(event));
    const client = new Client({ name: `team-read-error-${name}`, version: '0.0.0' });
    const transport = new StreamableHTTPClientTransport(new URL(server.url));
    try {
      await client.connect(transport);
      const result = await client.callTool({ name, arguments: call() });
      assert.equal(result.isError, true);
      assert.deepEqual(toolJSON(result), { error: 'principal_revoked', fallback: false });
      assert.deepEqual(events, [{
        eventType: 'authorization_denied', reasonCode: 'principal_revoked',
        methodClass: 'read', requestId: 'request-missing',
      }]);
    } finally {
      await client.close();
      await server.stop();
    }
  }
});

test('pulse_team_graph_delta returns typed closed errors and fixed content-free security metadata', async () => {
  const cases = [
    {
      name: 'invalid-contract',
      failure: new TeamContractError('secret graph field detail'),
      error: 'invalid_team_contract',
      event: {
        eventType: 'operation_denied', reasonCode: 'invalid_contract',
        methodClass: 'write', requestId: 'request-principal-writer',
      },
    },
    {
      name: 'invalid-graph',
      failure: new TeamDomainError('invalid_team_graph_delta'),
      error: 'invalid_team_graph_delta',
      event: {
        eventType: 'operation_denied', reasonCode: 'invalid_contract',
        methodClass: 'write', requestId: 'request-principal-writer',
      },
    },
    {
      name: 'policy',
      failure: new TeamDomainError('policy_denied'),
      error: 'policy_denied',
      event: {
        eventType: 'authorization_denied', reasonCode: 'policy_denied',
        methodClass: 'write', requestId: 'request-principal-writer',
      },
    },
    {
      name: 'revoked',
      failure: new TeamDomainError('principal_revoked'),
      error: 'principal_revoked',
      event: {
        eventType: 'authorization_denied', reasonCode: 'principal_revoked',
        methodClass: 'write', requestId: 'request-principal-writer',
      },
    },
    {
      name: 'stale',
      failure: new TeamDomainError('authorization_stale'),
      error: 'authorization_stale',
      event: {
        eventType: 'authorization_denied', reasonCode: 'stale_generation',
        methodClass: 'write', requestId: 'request-principal-writer',
      },
    },
    {
      name: 'idempotency-conflict',
      failure: new TeamDomainError('idempotency_conflict'),
      error: 'idempotency_conflict',
      event: {
        eventType: 'operation_denied', reasonCode: 'idempotency_conflict',
        methodClass: 'write', requestId: 'request-principal-writer',
      },
    },
    {
      name: 'idempotency-in-progress',
      failure: new TeamDomainError('idempotency_in_progress'),
      error: 'idempotency_in_progress',
      event: {
        eventType: 'operation_denied', reasonCode: 'operation_in_progress',
        methodClass: 'write', requestId: 'request-principal-writer',
      },
    },
    {
      name: 'store-outage',
      failure: new TeamDomainError('shared_memory_unavailable'),
      error: 'shared_memory_unavailable',
      event: {
        eventType: 'audit_degraded', reasonCode: 'store_unavailable',
        methodClass: 'write', requestId: 'request-principal-writer',
      },
    },
    {
      name: 'idempotency-failed',
      failure: new TeamDomainError('idempotency_failed'),
      error: 'idempotency_failed',
      event: {
        eventType: 'audit_degraded', reasonCode: 'internal_failure',
        methodClass: 'write', requestId: 'request-principal-writer',
      },
    },
    {
      name: 'unexpected',
      failure: new Error('secret graph daemon failure'),
      error: 'shared_memory_unavailable',
      event: {
        eventType: 'audit_degraded', reasonCode: 'internal_failure',
        methodClass: 'write', requestId: 'request-principal-writer',
      },
    },
  ] as const;

  for (const testCase of cases) {
    const events: GatewaySecurityEventInput[] = [];
    let graphCalls = 0;
    const server = await startTeamRegistryServer(() => Object.freeze({
      remember: async () => { throw new Error('memory fallback must remain unreachable'); },
      graphDelta: async () => {
        graphCalls++;
        throw testCase.failure;
      },
    }), (event) => events.push(event));
    const client = new Client({ name: `team-graph-error-${testCase.name}`, version: '0.0.0' });
    const transport = new StreamableHTTPClientTransport(new URL(server.url), {
      requestInit: { headers: { 'X-Test-Principal': 'principal-writer' } },
    });
    try {
      await client.connect(transport);
      const result = await client.callTool({
        name: 'pulse_team_graph_delta', arguments: baseTeamGraphDelta(),
      });
      assert.equal(result.isError, true);
      assert.deepEqual(toolJSON(result), { error: testCase.error, fallback: false });
      assert.deepEqual(events, [testCase.event]);
      assert.equal(graphCalls, 1, 'domain failures must not retry or fall back locally');
      assert.doesNotMatch(
        JSON.stringify({ result: toolJSON(result), events }),
        /secret|graph daemon|principal_id|oauth_subject|bearer/i,
      );
    } finally {
      await client.close();
      await server.stop();
    }
  }
});

test('pulse_team_graph_delta fails closed when its request-bound domain is unavailable', async () => {
  const events: GatewaySecurityEventInput[] = [];
  const server = await startTeamRegistryServer(undefined, (event) => events.push(event));
  const client = new Client({ name: 'team-graph-missing-domain', version: '0.0.0' });
  const transport = new StreamableHTTPClientTransport(new URL(server.url), {
    requestInit: { headers: { 'X-Test-Principal': 'principal-writer' } },
  });
  try {
    await client.connect(transport);
    const result = await client.callTool({
      name: 'pulse_team_graph_delta', arguments: baseTeamGraphDelta(),
    });
    assert.equal(result.isError, true);
    assert.deepEqual(toolJSON(result), { error: 'shared_memory_unavailable', fallback: false });
    assert.deepEqual(events, [{
      eventType: 'audit_degraded', reasonCode: 'store_unavailable',
      methodClass: 'write', requestId: 'request-principal-writer',
    }]);
  } finally {
    await client.close();
    await server.stop();
  }
});

test('pulse_team_graph_delta stays denied when security reporting fails', async () => {
  let graphCalls = 0;
  const server = await startTeamRegistryServer(() => Object.freeze({
    remember: async () => { throw new Error('memory fallback must remain unreachable'); },
    graphDelta: async () => {
      graphCalls++;
      throw new TeamDomainError('policy_denied');
    },
  }), () => { throw new Error('synthetic security sink outage'); });
  const client = new Client({ name: 'team-graph-audit-failure', version: '0.0.0' });
  const transport = new StreamableHTTPClientTransport(new URL(server.url), {
    requestInit: { headers: { 'X-Test-Principal': 'principal-writer' } },
  });
  try {
    await client.connect(transport);
    const result = await client.callTool({
      name: 'pulse_team_graph_delta', arguments: baseTeamGraphDelta(),
    });
    assert.equal(result.isError, true);
    assert.deepEqual(toolJSON(result), { error: 'policy_denied', fallback: false });
    assert.equal(graphCalls, 1);
  } finally {
    await client.close();
    await server.stop();
  }
});

test('pulse_team_remember returns closed typed errors without leaking domain failures', async () => {
  for (const [failure, expected] of [
    [new TeamContractError('secret invalid field detail'), 'invalid_team_contract'],
    [new TeamDomainError('policy_denied'), 'policy_denied'],
    [new Error('secret daemon failure'), 'shared_memory_unavailable'],
  ] as const) {
    const server = await startTeamRegistryServer(() => Object.freeze({
      remember: async () => { throw failure; },
    }));
    const client = new Client({ name: `team-error-${expected}`, version: '0.0.0' });
    const transport = new StreamableHTTPClientTransport(new URL(server.url));
    try {
      await client.connect(transport);
      const result = await client.callTool({ name: 'pulse_team_remember', arguments: baseTeamRemember() });
      assert.equal(result.isError, true);
      assert.deepEqual(toolJSON(result), { error: expected, fallback: false });
      assert.doesNotMatch(result.content[0]?.type === 'text' ? result.content[0].text : '', /secret/i);
    } finally {
      await client.close();
      await server.stop();
    }
  }
});

test('pulse_team_remember reports fixed metadata for authoritative denials and audit degradation', async () => {
  const cases = [
    {
      name: 'policy',
      failure: new TeamDomainError('policy_denied'),
      expected: {
        eventType: 'authorization_denied', reasonCode: 'policy_denied',
        methodClass: 'write', requestId: 'request-principal-writer',
      },
    },
    {
      name: 'revoked',
      failure: new TeamDomainError('principal_revoked'),
      expected: {
        eventType: 'authorization_denied', reasonCode: 'principal_revoked',
        methodClass: 'write', requestId: 'request-principal-writer',
      },
    },
    {
      name: 'invalid-contract',
      failure: new TeamContractError('secret rejected field'),
      expected: {
        eventType: 'operation_denied', reasonCode: 'invalid_contract',
        methodClass: 'write', requestId: 'request-principal-writer',
      },
    },
    {
      name: 'store-outage',
      failure: new TeamDomainError('shared_memory_unavailable'),
      expected: {
        eventType: 'audit_degraded', reasonCode: 'store_unavailable',
        methodClass: 'write', requestId: 'request-principal-writer',
      },
    },
    {
      name: 'unexpected-failure',
      failure: new Error('secret unexpected failure'),
      expected: {
        eventType: 'audit_degraded', reasonCode: 'internal_failure',
        methodClass: 'write', requestId: 'request-principal-writer',
      },
    },
  ] as const;

  for (const testCase of cases) {
    const events: GatewaySecurityEventInput[] = [];
    const server = await startTeamRegistryServer(() => Object.freeze({
      remember: async () => { throw testCase.failure; },
    }), (event) => events.push(event));
    const client = new Client({ name: `team-audit-${testCase.name}`, version: '0.0.0' });
    const transport = new StreamableHTTPClientTransport(new URL(server.url), {
      requestInit: { headers: { 'X-Test-Principal': 'principal-writer' } },
    });
    try {
      await client.connect(transport);
      const result = await client.callTool({ name: 'pulse_team_remember', arguments: baseTeamRemember() });
      assert.equal(result.isError, true);
      assert.deepEqual(events, [testCase.expected]);
      assert.doesNotMatch(JSON.stringify(events), /dedicated team store|bearer|oauth_subject|principal_id/i);
    } finally {
      await client.close();
      await server.stop();
    }
  }

  const unavailableEvents: GatewaySecurityEventInput[] = [];
  const unavailable = await startTeamRegistryServer(
    undefined, (event) => unavailableEvents.push(event),
  );
  const unavailableClient = new Client({ name: 'team-audit-missing-domain', version: '0.0.0' });
  const unavailableTransport = new StreamableHTTPClientTransport(new URL(unavailable.url), {
    requestInit: { headers: { 'X-Test-Principal': 'principal-writer' } },
  });
  try {
    await unavailableClient.connect(unavailableTransport);
    const result = await unavailableClient.callTool({
      name: 'pulse_team_remember', arguments: baseTeamRemember(),
    });
    assert.equal(result.isError, true);
    assert.deepEqual(unavailableEvents, [{
      eventType: 'audit_degraded', reasonCode: 'store_unavailable',
      methodClass: 'write', requestId: 'request-principal-writer',
    }]);
  } finally {
    await unavailableClient.close();
    await unavailable.stop();
  }
});
