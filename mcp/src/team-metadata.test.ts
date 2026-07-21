import assert from 'node:assert/strict';
import {
  createPrivateKey,
  createPublicKey,
  generateKeyPairSync,
} from 'node:crypto';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';

import { jwtVerify } from 'jose';

import {
  loadPrincipalSigner,
  PRINCIPAL_ASSERTION_AUDIENCE,
  PRINCIPAL_ASSERTION_ISSUER,
  TEAM_AUDIT_PATH,
  TEAM_INSPECT_PATH,
  TEAM_STATUS_PATH,
  TeamPrincipalClient,
  type TeamPrincipalContext,
} from './principal-context.js';
import {
  canonicalTeamAuditBody,
  canonicalTeamInspectBody,
  canonicalTeamStatusBody,
  TEAM_TOOL_DESCRIPTORS,
  TeamContractError,
  TeamDomainError,
  validateTeamAuditResult,
  validateTeamInspectResult,
  validateTeamStatusResult,
} from './team-contracts.js';

const NOW = 1_789_000_000;

function activeContext() {
  return {
    project_id: 'project-pulse',
    repo_id: 'repo-pulse',
    agent_id: 'agent-binding-a',
    session_id: 'session-pulse-001',
  };
}

function statusInput() {
  return { schema: 'pulse.team.status.v1', active_context: activeContext() };
}

function inspectInput() {
  return {
    schema: 'pulse.team.inspect.v1',
    object_id: 'team_object_001',
    active_context: activeContext(),
  };
}

function auditInput() {
  return { schema: 'pulse.team.audit.v1', active_context: activeContext() };
}

function principalContext(
  capabilities: TeamPrincipalContext['capabilities'],
): Readonly<TeamPrincipalContext> {
  return Object.freeze({
    version: 'pulse.team.principal_context.v1',
    request_id: 'metadata-request-001',
    store_id: 'store_test',
    team_id: 'team_test',
    principal_id: 'principal-agent-a',
    principal_kind: 'agent',
    oauth_client_key: 'a'.repeat(64),
    human_principal_id: 'human-a',
    agent_binding_id: 'binding-a',
    membership_id: 'membership-a',
    membership_role: 'member',
    team_auth_epoch: 2,
    principal_auth_epoch: 1,
    binding_auth_epoch: 1,
    membership_auth_epoch: 1,
    capabilities,
  });
}

function statusResult() {
  return {
    schema: 'pulse.team.status_result.v1',
    mode: 'team-remote',
    team_id: 'team_test',
    store_id: 'store_test',
    principal_id: 'principal-agent-a',
    principal_kind: 'agent',
    human_principal_id: 'human-a',
    agent_binding_id: 'binding-a',
    membership_id: 'membership-a',
    membership_role: 'member',
    active_context: activeContext(),
    effective_capabilities: ['pulse:connect', 'pulse:status'],
    policy_version: 1,
    projection_state: 'ready',
    degraded: false,
    degraded_reasons: [],
    fallback: false,
  };
}

function inspectResult() {
  return {
    schema: 'pulse.team.inspect_result.v1',
    object_id: 'team_object_001',
    object_kind: 'memory_capsule',
    author_principal_id: 'principal-agent-a',
    created_at: '2026-07-12T04:00:00.000Z',
    scope: {
      type: 'project',
      id: 'project-pulse',
      owner_principal_id: 'human-a',
    },
    privacy_tier: 'normal',
    retention: 'project',
    lifecycle_state: 'active',
    generation: 1,
    projection_state: 'ready',
    deletion_state: 'none',
    fallback: false,
  };
}

function auditResult() {
  return {
    schema: 'pulse.team.audit_result.v1',
    events: [{
      event_id: 'audit_001',
      occurred_at: '2026-07-12T04:00:00.000Z',
      action: 'team.object.write',
      outcome: 'allowed',
      actor_principal_id: 'principal-agent-a',
      client_key: 'a'.repeat(64),
      team_id: 'team_test',
      project_id: 'project-pulse',
      target_kind: 'memory_capsule',
      target_id: 'team_object_001',
      request_id: 'metadata-request-001',
      policy_version: 1,
      mode: 'team-remote',
      reason_code: 'object_stored',
    }],
    own_actions_only: true,
    fallback: false,
  };
}

function keyFixture() {
  const dir = mkdtempSync(join(tmpdir(), 'pulse-u8-metadata-'));
  const { privateKey } = generateKeyPairSync('ed25519');
  const privatePath = join(dir, 'active.pk8.pem');
  const privatePEM = privateKey.export({ format: 'pem', type: 'pkcs8' });
  writeFileSync(privatePath, privatePEM, { mode: 0o600 });
  const publicKey = createPublicKey(createPrivateKey(privatePEM));
  const publicJWK = publicKey.export({ format: 'jwk' });
  assert.equal(typeof publicJWK.x, 'string');
  const keyringPath = join(dir, 'verify-keyring.json');
  writeFileSync(keyringPath, JSON.stringify({
    active: { kid: 'gateway-key-1', public_key: publicJWK.x },
    previous: [],
  }), { mode: 0o600 });
  return { keyringPath, privatePath, publicKey };
}

test('team metadata tools advertise exact closed active contracts without caller authority', () => {
  const expected = [
    ['pulse_team_status', 'pulse.team.status.v1', ['schema', 'active_context']],
    ['pulse_team_inspect', 'pulse.team.inspect.v1', ['schema', 'object_id', 'active_context']],
    ['pulse_team_audit', 'pulse.team.audit.v1', ['schema', 'active_context']],
  ] as const;
  for (const [name, schemaName, required] of expected) {
    const descriptor = TEAM_TOOL_DESCRIPTORS.find((entry) => entry.name === name);
    assert.ok(descriptor);
    assert.match(descriptor.description, /domain execution (?:is|are) active/i);
    assert.doesNotMatch(descriptor.description, /stub|not active/i);
    const schema = descriptor.inputSchema as Record<string, unknown>;
    assert.equal(schema.additionalProperties, false);
    assert.deepEqual(schema.required, required);
    const properties = schema.properties as Record<string, Record<string, unknown>>;
    assert.equal(properties.schema.const, schemaName);
    assert.equal(properties.active_context.additionalProperties, false);
    for (const forbidden of ['actor_id', 'principal_id', 'team_id', 'role', 'owner_id']) {
      assert.equal(Object.hasOwn(properties, forbidden), false, `${name} exposes ${forbidden}`);
    }
  }
});

test('team metadata request canonicalization is closed, safe, and deterministic', () => {
  assert.equal(
    canonicalTeamStatusBody(statusInput()).text,
    '{"schema":"pulse.team.status.v1","active_context":{"project_id":"project-pulse","repo_id":"repo-pulse","agent_id":"agent-binding-a","session_id":"session-pulse-001"}}',
  );
  assert.equal(
    canonicalTeamInspectBody(inspectInput()).text,
    '{"schema":"pulse.team.inspect.v1","object_id":"team_object_001","active_context":{"project_id":"project-pulse","repo_id":"repo-pulse","agent_id":"agent-binding-a","session_id":"session-pulse-001"}}',
  );
  assert.equal(
    canonicalTeamAuditBody(auditInput()).text,
    '{"schema":"pulse.team.audit.v1","active_context":{"project_id":"project-pulse","repo_id":"repo-pulse","agent_id":"agent-binding-a","session_id":"session-pulse-001"},"limit":50}',
  );
  assert.equal(canonicalTeamAuditBody({ ...auditInput(), cursor: 'audit_cursor_001', limit: 7 }).text,
    '{"schema":"pulse.team.audit.v1","active_context":{"project_id":"project-pulse","repo_id":"repo-pulse","agent_id":"agent-binding-a","session_id":"session-pulse-001"},"cursor":"audit_cursor_001","limit":7}');

  assert.throws(
    () => canonicalTeamStatusBody({ ...statusInput(), principal_id: 'spoofed' }),
    TeamContractError,
  );
  assert.throws(
    () => canonicalTeamInspectBody({ ...inspectInput(), owner_id: 'spoofed' }),
    TeamContractError,
  );
  assert.throws(
    () => canonicalTeamAuditBody({ ...auditInput(), actor_id: 'spoofed' }),
    TeamContractError,
  );
  assert.throws(
    () => canonicalTeamInspectBody({ ...inspectInput(), object_id: '/Users/example/private' }),
    TeamContractError,
  );
  assert.throws(
    () => canonicalTeamAuditBody({ ...auditInput(), limit: 51 }),
    TeamContractError,
  );
});

test('team metadata result validators are exact, request-bound, and content-free', () => {
  const status = statusResult();
  assert.deepEqual(validateTeamStatusResult(
    status,
    principalContext(['pulse:connect', 'pulse:status']),
    activeContext(),
  ), status);
  assert.deepEqual(validateTeamInspectResult(inspectResult(), 'team_object_001'), inspectResult());
  assert.deepEqual(validateTeamAuditResult(auditResult(), 'principal-agent-a', 50), auditResult());

  const mismatchedStatus = structuredClone(status);
  mismatchedStatus.principal_id = 'principal-other';
  assert.throws(() => validateTeamStatusResult(
    mismatchedStatus,
    principalContext(['pulse:connect', 'pulse:status']),
    activeContext(),
  ), /status response/i);

  const wrongObject = structuredClone(inspectResult());
  wrongObject.object_id = 'team_object_hidden';
  assert.throws(() => validateTeamInspectResult(wrongObject, 'team_object_001'), /inspect response/i);

  const otherActor = structuredClone(auditResult());
  otherActor.events[0].actor_principal_id = 'principal-other';
  assert.throws(() => validateTeamAuditResult(otherActor, 'principal-agent-a', 50), /audit response/i);

  for (const unsafe of [
    { base: statusResult(), field: 'degraded_reasons', value: ['/Users/example/store.db'] },
    { base: inspectResult(), field: 'object_kind', value: 'authorization: bearer secret-value' },
    { base: auditResult(), field: 'extra', value: 'raw transcript payload' },
  ]) {
    const value = structuredClone(unsafe.base) as Record<string, unknown>;
    value[unsafe.field] = unsafe.value;
    const validate = value.schema === 'pulse.team.status_result.v1'
      ? () => validateTeamStatusResult(value, principalContext(['pulse:connect', 'pulse:status']), activeContext())
      : value.schema === 'pulse.team.inspect_result.v1'
        ? () => validateTeamInspectResult(value, 'team_object_001')
        : () => validateTeamAuditResult(value, 'principal-agent-a', 50);
    assert.throws(validate);
  }
});

test('bound metadata closures sign exact daemon routes and never forward bearer or caller authority', async () => {
  const keys = keyFixture();
  let sequence = 0;
  const signer = loadPrincipalSigner({
    privateKeyFile: keys.privatePath,
    keyId: 'gateway-key-1',
    verifyKeyringFile: keys.keyringPath,
    storeId: 'store_test',
    teamId: 'team_test',
    now: () => NOW,
    randomId: () => `metadata-jti-${++sequence}`,
  });
  const responses = new Map<string, unknown>([
    [TEAM_STATUS_PATH, statusResult()],
    [TEAM_INSPECT_PATH, inspectResult()],
    [TEAM_AUDIT_PATH, auditResult()],
  ]);
  const requests: Array<{ path: string; headers: Headers; body: string }> = [];
  const client = new TeamPrincipalClient({
    daemonBaseURL: 'http://127.0.0.1:18789',
    signer,
    apiKey: () => 'ipc-secret',
    fetch: async (input, init) => {
      const url = new URL(input.toString());
      requests.push({
        path: url.pathname,
        headers: new Headers(init?.headers),
        body: String(init?.body),
      });
      return Response.json(responses.get(url.pathname));
    },
  });
  const identity = {
    issuer: 'https://auth.example.com',
    subject: 'human-subject-1',
    clientId: 'agent-client-a',
    capabilities: ['pulse:connect', 'pulse:status'] as const,
  };
  const statusDomain = client.bindDomain(identity, principalContext([...identity.capabilities]));
  assert.deepEqual(await statusDomain.status(statusInput()), statusResult());

  const readIdentity = { ...identity, capabilities: ['pulse:connect', 'pulse:read'] as const };
  const inspectDomain = client.bindDomain(readIdentity, principalContext([...readIdentity.capabilities]));
  assert.deepEqual(await inspectDomain.inspect(inspectInput()), inspectResult());

  const auditIdentity = { ...identity, capabilities: ['pulse:audit', 'pulse:connect'] as const };
  const auditDomain = client.bindDomain(auditIdentity, principalContext([...auditIdentity.capabilities]));
  assert.deepEqual(await auditDomain.audit(auditInput()), auditResult());

  assert.deepEqual(requests.map(({ path }) => path), [
    TEAM_STATUS_PATH, TEAM_INSPECT_PATH, TEAM_AUDIT_PATH,
  ]);
  for (const request of requests) {
    assert.deepEqual([...request.headers.keys()].sort(), [
      'content-type', 'x-pulse-key', 'x-pulse-principal', 'x-pulse-request-id',
    ]);
    assert.equal(request.headers.has('authorization'), false);
    const verified = await jwtVerify(
      request.headers.get('x-pulse-principal') ?? '',
      keys.publicKey,
      {
        issuer: PRINCIPAL_ASSERTION_ISSUER,
        audience: PRINCIPAL_ASSERTION_AUDIENCE,
        algorithms: ['EdDSA'],
        currentDate: new Date(NOW * 1000),
      },
    );
    assert.equal(verified.payload.path, request.path);
    for (const forbidden of [
      'authorization', 'principal_id', 'human_principal_id', 'agent_binding_id',
      'membership_id', 'membership_role', 'team_auth_epoch',
    ]) {
      assert.equal(forbidden in verified.payload, false, forbidden);
    }
  }
});

test('metadata closures preserve only operation-specific errors and never retry or fall back', async () => {
  const keys = keyFixture();
  const signer = loadPrincipalSigner({
    privateKeyFile: keys.privatePath,
    keyId: 'gateway-key-1',
    verifyKeyringFile: keys.keyringPath,
    storeId: 'store_test',
    teamId: 'team_test',
    now: () => NOW,
    randomId: () => 'metadata-error-jti',
  });
  const cases = [
    {
      capabilities: ['pulse:connect', 'pulse:status'] as const,
      call: (domain: ReturnType<TeamPrincipalClient['bindDomain']>) => domain.status(statusInput()),
      code: 'invalid_team_status' as const,
    },
    {
      capabilities: ['pulse:connect', 'pulse:read'] as const,
      call: (domain: ReturnType<TeamPrincipalClient['bindDomain']>) => domain.inspect(inspectInput()),
      code: 'invalid_team_inspect' as const,
    },
    {
      capabilities: ['pulse:audit', 'pulse:connect'] as const,
      call: (domain: ReturnType<TeamPrincipalClient['bindDomain']>) => domain.audit(auditInput()),
      code: 'invalid_team_audit' as const,
    },
  ];
  for (const item of cases) {
    let calls = 0;
    const client = new TeamPrincipalClient({
      daemonBaseURL: 'http://127.0.0.1:18789', signer, apiKey: () => 'ipc-secret',
      fetch: async () => {
        calls++;
        return Response.json({ error: item.code, fallback: false }, { status: 400 });
      },
    });
    const identity = {
      issuer: 'https://auth.example.com', subject: 'human-subject-1', clientId: 'agent-client-a',
      capabilities: item.capabilities,
    };
    const domain = client.bindDomain(identity, principalContext([...item.capabilities]));
    await assert.rejects(
      item.call(domain),
      (error: unknown) => error instanceof TeamDomainError && error.code === item.code,
    );
    assert.equal(calls, 1);
  }

  let calls = 0;
  const malformed = new TeamPrincipalClient({
    daemonBaseURL: 'http://127.0.0.1:18789', signer, apiKey: () => 'ipc-secret',
    fetch: async () => {
      calls++;
      return Response.json({ error: 'invalid_team_audit', fallback: false }, { status: 400 });
    },
  }).bindDomain(
    {
      issuer: 'https://auth.example.com', subject: 'human-subject-1', clientId: 'agent-client-a',
      capabilities: ['pulse:connect', 'pulse:status'],
    },
    principalContext(['pulse:connect', 'pulse:status']),
  );
  await assert.rejects(
    malformed.status(statusInput()),
    (error: unknown) => error instanceof TeamDomainError &&
      error.code === 'shared_memory_unavailable',
  );
  assert.equal(calls, 1);
});
