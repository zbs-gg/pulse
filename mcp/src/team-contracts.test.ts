import assert from 'node:assert/strict';
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
import { ContractError, validateCapsule } from './validation.js';

import {
  canonicalTeamRememberBody,
  isTeamToolName,
  requiredTeamCapabilities,
  TEAM_BASELINE_CAPABILITY,
  TeamContractError,
  TeamDomainError,
  TEAM_TOOL_DESCRIPTORS,
  teamNotReadyResult,
  validateTeamRememberResult,
  validateTeamRememberInput,
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
