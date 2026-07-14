import assert from 'node:assert/strict';
import {
  createHash,
  createPrivateKey,
  createPublicKey,
  generateKeyPairSync,
} from 'node:crypto';
import { chmodSync, mkdtempSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';

import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';
import { StreamableHTTPServerTransport } from '@modelcontextprotocol/sdk/server/streamableHttp.js';
import { jwtVerify } from 'jose';

import { createPulseMcpServer } from './index.js';
import {
  loadPrincipalSigner,
  PRINCIPAL_ASSERTION_AUDIENCE,
  PRINCIPAL_ASSERTION_ISSUER,
  TEAM_DELETE_PATH,
  TEAM_DELETE_STATUS_PATH,
  TeamPrincipalClient,
  type BoundTeamDomain,
  type GatewaySecurityEventInput,
  type TeamPrincipalContext,
} from './principal-context.js';
import {
  canonicalTeamDeleteBody,
  canonicalTeamDeleteStatusBody,
  TEAM_TOOL_DESCRIPTORS,
  TeamContractError,
  TeamDomainError,
  validateTeamDeleteResult,
  validateTeamDeleteStatusResult,
} from './team-contracts.js';

const NOW = 1_789_000_000;

function activeContext() {
  return {
    project_id: 'project-pulse',
    repo_id: 'repo-pulse',
    agent_id: 'agent-bound',
    session_id: 'session-2026-07-11',
  };
}

function deleteInput() {
  return {
    schema: 'pulse.team.delete.v1',
    object_id: 'team_root_001',
    active_context: activeContext(),
    idempotency_key: 'delete-request-001',
  };
}

function deleteStatusInput() {
  return {
    schema: 'pulse.team.delete_status.v1',
    operation_id: 'delete_operation_001',
    active_context: activeContext(),
  };
}

function deleteResult() {
  return {
    schema: 'pulse.team.delete_result.v1' as const,
    operation_id: 'delete_operation_001',
    object_id: 'team_root_001',
    audit_event_id: 'audit_delete_001',
    status: 'deletion_in_progress' as const,
    replayed: false,
    fallback: false as const,
  };
}

function deleteStatusResult() {
  return {
    schema: 'pulse.team.delete_status_result.v1' as const,
    operation_id: 'delete_operation_001',
    object_id: 'team_root_001',
    audit_event_id: 'audit_delete_001',
    status: 'deletion_in_progress' as const,
    attempts: 1,
    next_attempt_at: '2026-07-11T11:05:00.000Z',
    fallback: false as const,
  };
}

function keyFixture() {
  const dir = mkdtempSync(join(tmpdir(), 'pulse-u7-mcp-key-'));
  const { privateKey } = generateKeyPairSync('ed25519');
  const privatePath = join(dir, 'active.pk8.pem');
  const privatePEM = privateKey.export({ format: 'pem', type: 'pkcs8' });
  writeFileSync(privatePath, privatePEM, { mode: 0o600 });
  chmodSync(privatePath, 0o600);
  const publicKey = createPublicKey(createPrivateKey(privatePEM));
  const publicJWK = publicKey.export({ format: 'jwk' });
  assert.equal(typeof publicJWK.x, 'string');
  const keyringPath = join(dir, 'verify-keyring.json');
  writeFileSync(keyringPath, JSON.stringify({
    active: { kid: 'gateway-key-1', public_key: publicJWK.x },
    previous: [],
  }), { mode: 0o600 });
  chmodSync(keyringPath, 0o600);
  return { keyringPath, privatePath, publicKey };
}

function principalContext(
  capabilities: TeamPrincipalContext['capabilities'] = ['pulse:connect', 'pulse:read', 'pulse:delete'],
): Readonly<TeamPrincipalContext> {
  return Object.freeze({
    version: 'pulse.team.principal_context.v1',
    request_id: 'delete-request-ctx-001',
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

function unavailableDomain(): BoundTeamDomain {
  const unavailable = async () => { throw new Error('unreachable domain method'); };
  return {
    remember: unavailable,
    graphDelta: unavailable,
    recall: unavailable,
    contextQuery: unavailable,
    resume: unavailable,
    delete: unavailable,
    deleteStatus: unavailable,
  };
}

function toolJSON(result: { content?: Array<{ type: string; text?: string }> }) {
  assert.equal(result.content?.[0]?.type, 'text');
  return JSON.parse(result.content?.[0]?.text ?? 'null');
}

async function startTeamServer(
  domain?: Readonly<BoundTeamDomain>,
  events?: GatewaySecurityEventInput[],
) {
  const httpServer = createServer(async (req, res) => {
    const requestServer = createPulseMcpServer(
      'team-remote', principalContext(), domain,
      events === undefined ? undefined : (event) => events.push(event),
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
    url: `http://127.0.0.1:${address.port}/mcp`,
    stop: () => new Promise<void>((resolve) => httpServer.close(() => resolve())),
  };
}

test('team deletion tools advertise exact closed active contracts without authority fields', () => {
  for (const [name, contract, required] of [
    ['pulse_team_delete', 'pulse.team.delete.v1', [
      'schema', 'object_id', 'active_context', 'idempotency_key',
    ]],
    ['pulse_team_delete_status', 'pulse.team.delete_status.v1', [
      'schema', 'operation_id', 'active_context',
    ]],
  ] as const) {
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

test('team deletion inputs canonicalize exact bodies without mutating callers', () => {
  const deletion = deleteInput();
  const deletionOriginal = structuredClone(deletion);
  const deletionBody = canonicalTeamDeleteBody(deletion);
  assert.deepEqual(deletion, deletionOriginal);
  assert.deepEqual(deletionBody.value, deletionOriginal);
  assert.equal(deletionBody.text, JSON.stringify(deletionBody.value));
  assert.deepEqual(deletionBody.bytes, Buffer.from(deletionBody.text, 'utf8'));

  const status = deleteStatusInput();
  const statusOriginal = structuredClone(status);
  const statusBody = canonicalTeamDeleteStatusBody(status);
  assert.deepEqual(status, statusOriginal);
  assert.deepEqual(statusBody.value, statusOriginal);
  assert.equal(statusBody.text, JSON.stringify(statusBody.value));
  assert.deepEqual(statusBody.bytes, Buffer.from(statusBody.text, 'utf8'));
});

test('team deletion inputs reject spoofed authority, unsafe IDs, and unknown fields', () => {
  for (const [builder, canonicalizer] of [
    [deleteInput, canonicalTeamDeleteBody],
    [deleteStatusInput, canonicalTeamDeleteStatusBody],
  ] as const) {
    for (const [field, value] of [
      ['principal_id', 'principal-spoofed'], ['team_id', 'team-spoofed'],
      ['owner_id', 'owner-spoofed'], ['role', 'owner'], ['unexpected', true],
    ] as const) {
      assert.throws(() => canonicalizer({ ...builder(), [field]: value }), TeamContractError);
    }
    assert.throws(() => canonicalizer({
      ...builder(), active_context: { ...activeContext(), team_id: 'team-spoofed' },
    }), TeamContractError);
  }
  assert.throws(() => canonicalTeamDeleteBody({ ...deleteInput(), object_id: '../private' }), TeamContractError);
  assert.throws(() => canonicalTeamDeleteBody({ ...deleteInput(), idempotency_key: 'short' }), TeamContractError);
  assert.throws(() => canonicalTeamDeleteStatusBody({
    ...deleteStatusInput(), operation_id: '/Users/nik/private',
  }), TeamContractError);
});

test('team deletion response validators expose only opaque state and bounded attempts', () => {
  assert.deepEqual(validateTeamDeleteResult(deleteResult()), deleteResult());
  assert.deepEqual(validateTeamDeleteStatusResult(deleteStatusResult()), deleteStatusResult());
  const complete = {
    ...deleteStatusResult(), status: 'complete', attempts: 2,
    next_attempt_at: undefined, completed_at: '2026-07-11T11:10:00.000Z',
  };
  delete complete.next_attempt_at;
  assert.deepEqual(validateTeamDeleteStatusResult(complete), complete);
  const failed = {
    ...deleteStatusResult(), status: 'cleanup_failed', attempts: 3,
    next_attempt_at: '2026-07-11T11:20:00.000Z',
  };
  assert.deepEqual(validateTeamDeleteStatusResult(failed), failed);

  for (const invalid of [
    { ...deleteResult(), status: 'cleanup_failed' },
    { ...deleteResult(), lineage_count: 4 },
    { ...deleteResult(), fallback: true },
  ]) assert.throws(() => validateTeamDeleteResult(invalid), /response/i);

  for (const invalid of [
    { ...deleteStatusResult(), attempts: -1 },
    { ...deleteStatusResult(), attempts: 1.5 },
    { ...deleteStatusResult(), attempts: 1_000_001 },
    { ...deleteStatusResult(), completed_at: '2026-07-11T11:10:00.000Z' },
    { ...complete, completed_at: undefined },
    { ...complete, next_attempt_at: '2026-07-11T11:20:00.000Z' },
    { ...failed, next_attempt_at: undefined },
    { ...deleteStatusResult(), worker_id: 'worker-secret' },
    { ...deleteStatusResult(), derivative_count: 2 },
    { ...deleteStatusResult(), last_error: 'internal cleanup detail' },
  ]) assert.throws(() => validateTeamDeleteStatusResult(invalid), /response/i);
});

test('bound deletion domains send exact body-bound requests with no bearer or caller identity', async () => {
  const keys = keyFixture();
  let sequence = 0;
  const signer = loadPrincipalSigner({
    privateKeyFile: keys.privatePath,
    keyId: 'gateway-key-1',
    verifyKeyringFile: keys.keyringPath,
    storeId: 'store_test',
    teamId: 'team_test',
    now: () => NOW,
    randomId: () => `delete-jti-${++sequence}`,
  });
  const requests: Array<{ path: string; headers: Headers; body: string }> = [];
  const responses = new Map<string, unknown>([
    [TEAM_DELETE_PATH, deleteResult()],
    [TEAM_DELETE_STATUS_PATH, deleteStatusResult()],
  ]);
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
    capabilities: ['pulse:connect', 'pulse:read', 'pulse:delete'] as const,
  };
  const domain = client.bindDomain(identity, principalContext());
  assert.deepEqual(await domain.delete(deleteInput()), deleteResult());
  assert.deepEqual(await domain.deleteStatus(deleteStatusInput()), deleteStatusResult());
  assert.deepEqual(requests.map(({ path }) => path), [TEAM_DELETE_PATH, TEAM_DELETE_STATUS_PATH]);
  const canonicalBodies = [
    canonicalTeamDeleteBody(deleteInput()).text,
    canonicalTeamDeleteStatusBody(deleteStatusInput()).text,
  ];
  for (let index = 0; index < requests.length; index++) {
    const request = requests[index];
    assert.equal(request.body, canonicalBodies[index]);
    assert.deepEqual([...request.headers.keys()].sort(), [
      'content-type', 'x-pulse-key', 'x-pulse-principal', 'x-pulse-request-id',
    ]);
    assert.equal(request.headers.has('authorization'), false);
    const verified = await jwtVerify(request.headers.get('x-pulse-principal') ?? '', keys.publicKey, {
      issuer: PRINCIPAL_ASSERTION_ISSUER,
      audience: PRINCIPAL_ASSERTION_AUDIENCE,
      algorithms: ['EdDSA'],
      currentDate: new Date(NOW * 1000),
    });
    assert.equal(verified.payload.path, request.path);
    assert.equal(
      verified.payload.body_sha256,
      createHash('sha256').update(request.body).digest('hex'),
    );
    for (const forbidden of [
      'authorization', 'principal_id', 'human_principal_id', 'agent_binding_id',
      'membership_id', 'membership_role', 'team_auth_epoch',
    ]) assert.equal(forbidden in verified.payload, false, forbidden);
  }
});

test('domain signer admits only exact POST deletion paths and canonical bodies', async () => {
  const keys = keyFixture();
  let sequence = 0;
  const signer = loadPrincipalSigner({
    privateKeyFile: keys.privatePath,
    keyId: 'gateway-key-1',
    verifyKeyringFile: keys.keyringPath,
    storeId: 'store_test',
    teamId: 'team_test',
    now: () => NOW,
    randomId: () => `delete-path-jti-${++sequence}`,
  });
  const bodies = [
    [TEAM_DELETE_PATH, canonicalTeamDeleteBody(deleteInput()).bytes],
    [TEAM_DELETE_STATUS_PATH, canonicalTeamDeleteStatusBody(deleteStatusInput()).bytes],
  ] as const;
  for (const [path, body] of bodies) {
    const assertion = await signer.signDomainRequest({
      requestId: `delete-path-request-${sequence}`,
      method: 'POST',
      path,
      body,
      oauthIssuer: 'https://auth.example.com',
      oauthSubject: 'human-subject-1',
      oauthClientId: 'agent-client-a',
      capabilities: ['pulse:connect', path === TEAM_DELETE_PATH ? 'pulse:delete' : 'pulse:read'],
    });
    const verified = await jwtVerify(assertion, keys.publicKey, {
      issuer: PRINCIPAL_ASSERTION_ISSUER,
      audience: PRINCIPAL_ASSERTION_AUDIENCE,
      algorithms: ['EdDSA'],
      currentDate: new Date(NOW * 1000),
    });
    assert.equal(verified.payload.path, path);
    assert.equal(verified.payload.body_sha256, createHash('sha256').update(body).digest('hex'));
  }
  const common = {
    requestId: 'delete-path-rejected',
    method: 'POST',
    body: bodies[0][1],
    oauthIssuer: 'https://auth.example.com',
    oauthSubject: 'human-subject-1',
    oauthClientId: 'agent-client-a',
    capabilities: ['pulse:connect', 'pulse:delete'] as const,
  };
  for (const [method, path] of [
    ['DELETE', TEAM_DELETE_PATH],
    ['GET', TEAM_DELETE_STATUS_PATH],
    ['POST', `${TEAM_DELETE_PATH}/`],
    ['POST', `${TEAM_DELETE_STATUS_PATH}?debug=1`],
    ['POST', '/team/v1/deletion'],
    ['POST', '/delete/status'],
  ] as const) {
    await assert.rejects(
      signer.signDomainRequest({ ...common, method, path }),
      /request binding/i,
    );
  }
});

test('deletion domains enforce capability and preserve only operation-specific closed errors', async () => {
  const keys = keyFixture();
  const signer = loadPrincipalSigner({
    privateKeyFile: keys.privatePath,
    keyId: 'gateway-key-1',
    verifyKeyringFile: keys.keyringPath,
    storeId: 'store_test',
    teamId: 'team_test',
    now: () => NOW,
  });
  const cases = [
    ['delete', 400, { error: 'invalid_team_delete', fallback: false }, 'invalid_team_delete'],
    ['deleteStatus', 400, { error: 'invalid_team_delete_status', fallback: false }, 'invalid_team_delete_status'],
    ['delete', 404, { error: 'concealed_not_found', fallback: false }, 'concealed_not_found'],
    ['deleteStatus', 403, { error: 'principal_revoked', fallback: false }, 'principal_revoked'],
    ['delete', 409, { error: 'idempotency_conflict', fallback: false }, 'idempotency_conflict'],
    ['delete', 503, { error: 'shared_memory_unavailable', fallback: false }, 'shared_memory_unavailable'],
  ] as const;
  for (const [operation, status, body, expected] of cases) {
    let calls = 0;
    const identity = {
      issuer: 'https://auth.example.com', subject: 'subject', clientId: 'client',
      capabilities: operation === 'delete'
        ? ['pulse:connect', 'pulse:delete'] as const
        : ['pulse:connect', 'pulse:read'] as const,
    };
    const domain = new TeamPrincipalClient({
      daemonBaseURL: 'http://127.0.0.1:18789', signer, apiKey: () => 'ipc-secret',
      fetch: async () => { calls++; return Response.json(body, { status }); },
    }).bindDomain(identity, principalContext([...identity.capabilities]));
    const invoke = operation === 'delete'
      ? () => domain.delete(deleteInput())
      : () => domain.deleteStatus(deleteStatusInput());
    await assert.rejects(
      invoke(),
      (error: unknown) => error instanceof TeamDomainError && error.code === expected,
    );
    assert.equal(calls, 1);
  }

  let calls = 0;
  const deleteOnly = new TeamPrincipalClient({
    daemonBaseURL: 'http://127.0.0.1:18789', signer, apiKey: () => 'ipc-secret',
    fetch: async () => { calls++; return Response.json(deleteResult()); },
  }).bindDomain({
    issuer: 'https://auth.example.com', subject: 'subject', clientId: 'client',
    capabilities: ['pulse:connect', 'pulse:delete'],
  }, principalContext(['pulse:connect', 'pulse:delete']));
  await assert.rejects(deleteOnly.deleteStatus(deleteStatusInput()),
    (error: unknown) => error instanceof TeamDomainError && error.code === 'invalid_principal');
  assert.equal(calls, 0);
});

test('deletion domains collapse malformed or unavailable success responses without retry', async () => {
  const keys = keyFixture();
  const signer = loadPrincipalSigner({
    privateKeyFile: keys.privatePath,
    keyId: 'gateway-key-1',
    verifyKeyringFile: keys.keyringPath,
    storeId: 'store_test',
    teamId: 'team_test',
    now: () => NOW,
  });
  const identity = {
    issuer: 'https://auth.example.com', subject: 'subject', clientId: 'client',
    capabilities: ['pulse:connect', 'pulse:read', 'pulse:delete'] as const,
  };
  for (const [operation, fetcher] of [
    ['delete', async () => Response.json({ ...deleteResult(), lineage_count: 2 })],
    ['deleteStatus', async () => Response.json({ ...deleteStatusResult(), last_error: 'secret' })],
    ['delete', async () => new Response(JSON.stringify(deleteResult()), {
      headers: { 'content-type': 'text/plain' },
    })],
    ['deleteStatus', async () => { throw new Error('synthetic outage'); }],
  ] as const) {
    let calls = 0;
    const domain = new TeamPrincipalClient({
      daemonBaseURL: 'http://127.0.0.1:18789', signer, apiKey: () => 'ipc-secret',
      fetch: async (...args) => { calls++; return fetcher(...args); },
    }).bindDomain(identity, principalContext([...identity.capabilities]));
    const invoke = operation === 'delete'
      ? () => domain.delete(deleteInput())
      : () => domain.deleteStatus(deleteStatusInput());
    await assert.rejects(
      invoke(),
      (error: unknown) => error instanceof TeamDomainError &&
        error.code === 'shared_memory_unavailable',
    );
    assert.equal(calls, 1);
  }
});

test('agent-facing Team exposes deletion status but keeps deletion mutation human-controlled', async () => {
  const calls: string[] = [];
  const domain = unavailableDomain();
  domain.delete = async (input: unknown) => {
    calls.push(`delete:${canonicalTeamDeleteBody(input).value.object_id}`);
    return deleteResult();
  };
  domain.deleteStatus = async (input: unknown) => {
    calls.push(`status:${canonicalTeamDeleteStatusBody(input).value.operation_id}`);
    return deleteStatusResult();
  };
  const server = await startTeamServer(Object.freeze(domain));
  const client = new Client({ name: 'team-delete-dispatch', version: '0.0.0' });
  const transport = new StreamableHTTPClientTransport(new URL(server.url));
  try {
    await client.connect(transport);
    const listed = await client.listTools();
    assert.equal(listed.tools.some(({ name }) => name === 'pulse_team_delete'), false);
    assert.equal(listed.tools.some(({ name }) => name === 'pulse_team_delete_status'), true);
    const deletion = await client.callTool({ name: 'pulse_team_delete', arguments: deleteInput() });
    const status = await client.callTool({
      name: 'pulse_team_delete_status', arguments: deleteStatusInput(),
    });
    assert.equal(deletion.isError, true);
    assert.match(JSON.stringify(deletion.content), /Unknown team tool/i);
    assert.deepEqual(toolJSON(status), deleteStatusResult());
    assert.deepEqual(calls, ['status:delete_operation_001']);
    const legacy = await client.callTool({ name: 'pulse_forget', arguments: { id: 'team_root_001' } });
    assert.equal(legacy.isError, true);
    assert.deepEqual(calls, ['status:delete_operation_001']);
  } finally {
    await client.close();
    await server.stop();
  }
});

test('team deletion tools fail closed with content-free security metadata', async () => {
  for (const [name, input, methodClass] of [
    ['pulse_team_delete_status', deleteStatusInput(), 'read'],
  ] as const) {
    const events: GatewaySecurityEventInput[] = [];
    const domain = unavailableDomain();
    domain.delete = async () => { throw new TeamDomainError('principal_revoked'); };
    domain.deleteStatus = async () => { throw new TeamDomainError('principal_revoked'); };
    const server = await startTeamServer(Object.freeze(domain), events);
    const client = new Client({ name: `team-delete-denial-${name}`, version: '0.0.0' });
    const transport = new StreamableHTTPClientTransport(new URL(server.url));
    try {
      await client.connect(transport);
      const result = await client.callTool({ name, arguments: input });
      assert.equal(result.isError, true);
      assert.deepEqual(toolJSON(result), { error: 'principal_revoked', fallback: false });
      assert.deepEqual(events, [{
        eventType: 'authorization_denied',
        reasonCode: 'principal_revoked',
        methodClass,
        requestId: 'delete-request-ctx-001',
      }]);
      assert.doesNotMatch(JSON.stringify({ result: toolJSON(result), events }),
        /object_id|operation_id|lineage|worker|lease|bearer/i);
    } finally {
      await client.close();
      await server.stop();
    }
  }
});
