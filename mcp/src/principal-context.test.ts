import assert from 'node:assert/strict';
import {
  createHash,
  createPrivateKey,
  createPublicKey,
  generateKeyPairSync,
} from 'node:crypto';
import { chmodSync, mkdtempSync, readFileSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';

import { decodeProtectedHeader, jwtVerify } from 'jose';

import {
  BoundedSecurityEventReporter,
  PRINCIPAL_ASSERTION_AUDIENCE,
  PRINCIPAL_ASSERTION_ISSUER,
  loadPrincipalSigner,
  PrincipalCheckError,
  SECURITY_EVENT_ASSERTION_VERSION,
  SecurityEventRateLimitedError,
  stablePrincipalCheckBody,
  TEAM_MEMORY_REMEMBER_PATH,
  TeamPrincipalClient,
  type TeamPrincipalContext,
} from './principal-context.js';
import {
  canonicalTeamRememberBody,
  TeamContractError,
  TeamDomainError,
} from './team-contracts.js';

const NOW = 1_789_000_000;

function keyFixture() {
  const dir = mkdtempSync(join(tmpdir(), 'pulse-u3-key-'));
  const { privateKey } = generateKeyPairSync('ed25519');
  const privatePath = join(dir, 'active.pk8.pem');
  const privatePEM = privateKey.export({ format: 'pem', type: 'pkcs8' });
  writeFileSync(privatePath, privatePEM, { mode: 0o600 });
  const publicJWK = createPublicKey(createPrivateKey(privatePEM)).export({ format: 'jwk' });
  assert.equal(typeof publicJWK.x, 'string');
  const keyringPath = join(dir, 'verify-keyring.json');
  writeFileSync(keyringPath, JSON.stringify({
    active: { kid: 'gateway-key-1', public_key: publicJWK.x },
    previous: [],
  }), { mode: 0o600 });
  return { dir, keyringPath, privateKey, privatePath, publicKey: createPublicKey(privateKey) };
}

test('signs the exact request-local pulse.principal.v1 claim contract', async () => {
  const keys = keyFixture();
  const signer = loadPrincipalSigner({
    privateKeyFile: keys.privatePath,
    keyId: 'gateway-key-1',
    verifyKeyringFile: keys.keyringPath,
    storeId: 'store_test',
    teamId: 'team_test',
    now: () => NOW,
    randomId: (() => {
      const values = ['assertion-jti', 'request-id'];
      return () => values.shift() ?? 'unexpected';
    })(),
  });
  const body = stablePrincipalCheckBody({
    oauthIssuer: 'https://auth.example.com',
    oauthSubject: 'human-subject-1',
    oauthClientId: 'agent-client-a',
    capabilities: ['pulse:read', 'pulse:connect', 'pulse:read'],
  });
  assert.equal(body.text, '{"oauth_issuer":"https://auth.example.com","oauth_subject":"human-subject-1","oauth_client_id":"agent-client-a","capabilities":["pulse:connect","pulse:read"]}');

  const assertion = await signer.signPrincipalCheck({
    requestId: 'request-id',
    method: 'POST',
    path: '/team/v1/principal/check',
    body: body.bytes,
    oauthIssuer: 'https://auth.example.com',
    oauthSubject: 'human-subject-1',
    oauthClientId: 'agent-client-a',
    capabilities: ['pulse:read', 'pulse:connect'],
  });
  assert.deepEqual(decodeProtectedHeader(assertion), {
    alg: 'EdDSA',
    kid: 'gateway-key-1',
    typ: 'pulse.principal.v1',
  });
  const verified = await jwtVerify(assertion, keys.publicKey, {
    issuer: PRINCIPAL_ASSERTION_ISSUER,
    audience: PRINCIPAL_ASSERTION_AUDIENCE,
    algorithms: ['EdDSA'],
    currentDate: new Date(NOW * 1000),
  });
  assert.deepEqual(verified.payload, {
    version: 'pulse.principal.v1',
    iss: PRINCIPAL_ASSERTION_ISSUER,
    aud: PRINCIPAL_ASSERTION_AUDIENCE,
    iat: NOW,
    nbf: NOW - 1,
    exp: NOW + 30,
    jti: 'assertion-jti',
    request_id: 'request-id',
    method: 'POST',
    path: '/team/v1/principal/check',
    body_sha256: '073d92fe4db0c2b078dc30fcbc7eb2639e424f94c521ec730768138308ebd9d5',
    store_id: 'store_test',
    team_id: 'team_test',
    oauth_issuer: 'https://auth.example.com',
    oauth_subject: 'human-subject-1',
    oauth_client_id: 'agent-client-a',
    grant_kind: 'registered',
    capabilities: ['pulse:connect', 'pulse:read'],
  });
  assert.equal('v' in verified.payload, false);
});

test('domain write reuses pulse.principal.v1 and binds the canonical body without internal authority', async () => {
  const keys = keyFixture();
  const signer = loadPrincipalSigner({
    privateKeyFile: keys.privatePath,
    keyId: 'gateway-key-1',
    verifyKeyringFile: keys.keyringPath,
    storeId: 'store_test',
    teamId: 'team_test',
    now: () => NOW,
    randomId: () => 'domain-jti',
  });
  const body = canonicalTeamRememberBody({
    schema: 'pulse.team.memory.v1',
    source: {
      host: 'codex', conversation_scope: 'current_turn', timestamp: '2026-07-11T05:00:00Z',
    },
    items: [{
      kind: 'decision', redacted_summary: 'Use the remote team store.', confidence: 1,
      evidence_hint: 'user_confirmed', tags: ['pilot'],
    }],
    raw_input_included: false,
    active_context: { project_id: 'project-pulse' },
    privacy_tier: 'normal',
    retention: 'project',
    idempotency_key: 'remember-domain-001',
  });
  const assertion = await signer.signDomainRequest({
    requestId: 'domain-request-001',
    method: 'POST',
    path: '/team/v1/memory/remember',
    body: body.bytes,
    oauthIssuer: 'https://auth.example.com',
    oauthSubject: 'human-subject-1',
    oauthClientId: 'agent-client-a',
    capabilities: ['pulse:write', 'pulse:connect'],
  });
  assert.deepEqual(decodeProtectedHeader(assertion), {
    alg: 'EdDSA', kid: 'gateway-key-1', typ: 'pulse.principal.v1',
  });
  const verified = await jwtVerify(assertion, keys.publicKey, {
    issuer: PRINCIPAL_ASSERTION_ISSUER,
    audience: PRINCIPAL_ASSERTION_AUDIENCE,
    algorithms: ['EdDSA'],
    currentDate: new Date(NOW * 1000),
  });
  assert.deepEqual(verified.payload, {
    version: 'pulse.principal.v1',
    request_id: 'domain-request-001',
    method: 'POST',
    path: '/team/v1/memory/remember',
    body_sha256: createHash('sha256').update(body.bytes).digest('hex'),
    store_id: 'store_test',
    team_id: 'team_test',
    oauth_issuer: 'https://auth.example.com',
    oauth_subject: 'human-subject-1',
    oauth_client_id: 'agent-client-a',
    grant_kind: 'registered',
    capabilities: ['pulse:connect', 'pulse:write'],
    iss: PRINCIPAL_ASSERTION_ISSUER,
    aud: PRINCIPAL_ASSERTION_AUDIENCE,
    iat: NOW,
    nbf: NOW - 1,
    exp: NOW + 30,
    jti: 'domain-jti',
  });
  for (const forbidden of [
    'authorization', 'principal_id', 'human_principal_id', 'agent_binding_id',
    'membership_id', 'membership_role', 'team_auth_epoch',
  ]) {
    assert.equal(forbidden in verified.payload, false, forbidden);
  }
});

function teamRememberInput() {
  return {
    schema: 'pulse.team.memory.v1',
    source: {
      host: 'codex', conversation_scope: 'current_turn', timestamp: '2026-07-11T12:00:00+07:00',
    },
    items: [{
      kind: 'decision', redacted_summary: 'Use the remote team store.', confidence: 1,
      evidence_hint: 'user_confirmed', tags: ['pilot'],
    }],
    raw_input_included: false,
    active_context: { project_id: 'project-pulse' },
    privacy_tier: 'normal',
    retention: 'project',
    idempotency_key: 'remember-domain-001',
  };
}

function writePrincipalContext(): Readonly<TeamPrincipalContext> {
  return Object.freeze({
    version: 'pulse.team.principal_context.v1',
    request_id: 'domain-request-001',
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
    capabilities: ['pulse:connect', 'pulse:write'],
  });
}

function storedTeamMemoryResult() {
  return {
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
}

test('bound team domain sends one canonical loopback request with no bearer or internal principal claims', async () => {
  const keys = keyFixture();
  const signer = loadPrincipalSigner({
    privateKeyFile: keys.privatePath, keyId: 'gateway-key-1', verifyKeyringFile: keys.keyringPath,
    storeId: 'store_test', teamId: 'team_test', now: () => NOW, randomId: () => 'remember-jti',
  });
  const requests: Array<{ url: string; headers: Headers; body: string }> = [];
  const client = new TeamPrincipalClient({
    daemonBaseURL: 'http://127.0.0.1:18789', signer, apiKey: () => 'ipc-secret',
    fetch: async (input, init) => {
      requests.push({ url: input.toString(), headers: new Headers(init?.headers), body: String(init?.body) });
      return Response.json(storedTeamMemoryResult());
    },
  });
  const identity = {
    issuer: 'https://auth.example.com', subject: 'human-subject-1', clientId: 'agent-client-a',
    capabilities: ['pulse:connect', 'pulse:write'] as const,
  };
  const domain = client.bindDomain(identity, writePrincipalContext());
  const result = await domain.remember(teamRememberInput());
  assert.deepEqual(result, storedTeamMemoryResult());
  assert.equal(requests.length, 1);
  assert.equal(requests[0].url, `http://127.0.0.1:18789${TEAM_MEMORY_REMEMBER_PATH}`);
  const canonical = canonicalTeamRememberBody(teamRememberInput());
  assert.equal(requests[0].body, canonical.text);
  assert.deepEqual([...requests[0].headers.keys()].sort(), [
    'content-type', 'x-pulse-key', 'x-pulse-principal', 'x-pulse-request-id',
  ]);
  assert.equal(requests[0].headers.get('content-type'), 'application/json');
  assert.equal(requests[0].headers.get('x-pulse-key'), 'ipc-secret');
  assert.equal(requests[0].headers.get('x-pulse-request-id'), 'domain-request-001');
  assert.equal(requests[0].headers.has('authorization'), false);
  const verified = await jwtVerify(requests[0].headers.get('x-pulse-principal') ?? '', keys.publicKey, {
    issuer: PRINCIPAL_ASSERTION_ISSUER, audience: PRINCIPAL_ASSERTION_AUDIENCE,
    algorithms: ['EdDSA'], currentDate: new Date(NOW * 1000),
  });
  assert.equal(verified.payload.path, TEAM_MEMORY_REMEMBER_PATH);
  assert.equal(verified.payload.body_sha256, createHash('sha256').update(requests[0].body).digest('hex'));
  assert.equal(verified.payload.oauth_issuer, identity.issuer);
  assert.equal(verified.payload.oauth_subject, identity.subject);
  assert.equal(verified.payload.oauth_client_id, identity.clientId);
  for (const forbidden of ['authorization', 'principal_id', 'human_principal_id', 'membership_role']) {
    assert.equal(forbidden in verified.payload, false, forbidden);
  }
});

test('team domain preserves allowlisted errors and collapses malformed or unavailable responses', async () => {
  const keys = keyFixture();
  const signer = loadPrincipalSigner({
    privateKeyFile: keys.privatePath, keyId: 'gateway-key-1', verifyKeyringFile: keys.keyringPath,
    storeId: 'store_test', teamId: 'team_test', now: () => NOW,
  });
  const identity = {
    issuer: 'https://auth.example.com', subject: 'human-subject-1', clientId: 'agent-client-a',
    capabilities: ['pulse:connect', 'pulse:write'] as const,
  };
  for (const [status, body, expected] of [
    [400, { error: 'invalid_team_memory', fallback: false }, 'invalid_team_memory'],
    [401, { error: 'principal_request_mismatch', fallback: false }, 'principal_request_mismatch'],
    [403, { error: 'principal_revoked', fallback: false }, 'principal_revoked'],
    [403, { error: 'policy_denied', fallback: false }, 'policy_denied'],
    [404, { error: 'not_found', fallback: false }, 'not_found'],
    [409, { error: 'idempotency_conflict', fallback: false }, 'idempotency_conflict'],
    [409, { error: 'authorization_stale', fallback: false }, 'authorization_stale'],
    [503, { error: 'shared_memory_unavailable', fallback: false }, 'shared_memory_unavailable'],
  ] as const) {
    const domain = new TeamPrincipalClient({
      daemonBaseURL: 'http://127.0.0.1:18789', signer, apiKey: () => 'ipc-secret',
      fetch: async () => Response.json(body, { status }),
    }).bindDomain(identity, writePrincipalContext());
    await assert.rejects(
      domain.remember(teamRememberInput()),
      (error: unknown) => error instanceof TeamDomainError && error.code === expected,
    );
  }

  for (const fetcher of [
    async () => { throw new Error('synthetic outage'); },
    async () => Response.json({ ...storedTeamMemoryResult(), internal_detail: 'nope' }),
    async () => Response.json({ error: 'policy_denied', fallback: false, target: 'secret' }, { status: 403 }),
  ]) {
    const domain = new TeamPrincipalClient({
      daemonBaseURL: 'http://127.0.0.1:18789', signer, apiKey: () => 'ipc-secret', fetch: fetcher,
    }).bindDomain(identity, writePrincipalContext());
    await assert.rejects(
      domain.remember(teamRememberInput()),
      (error: unknown) => error instanceof TeamDomainError && error.code === 'shared_memory_unavailable',
    );
  }

  let called = false;
  const invalidDomain = new TeamPrincipalClient({
    daemonBaseURL: 'http://127.0.0.1:18789', signer, apiKey: () => 'ipc-secret',
    fetch: async () => { called = true; return Response.json(storedTeamMemoryResult()); },
  }).bindDomain(identity, writePrincipalContext());
  await assert.rejects(
    invalidDomain.remember({ ...teamRememberInput(), raw_input_included: true }),
    TeamContractError,
  );
  assert.equal(called, false);
});

test('principal check preserves only allowlisted bounded Go denial codes', async () => {
  const keys = keyFixture();
  const signer = loadPrincipalSigner({
    privateKeyFile: keys.privatePath, keyId: 'gateway-key-1', verifyKeyringFile: keys.keyringPath,
    storeId: 'store_test', teamId: 'team_test', now: () => NOW,
  });
  for (const [status, body, expected] of [
    [403, { error: 'principal_revoked' }, 'principal_revoked'],
    [503, { error: 'principal_store_unavailable' }, 'principal_store_unavailable'],
    [401, { error: 'principal_replay' }, 'principal_replayed'],
    [401, { error: 'principal_request_mismatch' }, 'principal_binding_mismatch'],
    [401, { error: 'secret-server-detail', extra: 'nope' }, 'invalid_principal'],
  ] as const) {
    const client = new TeamPrincipalClient({
      daemonBaseURL: 'http://127.0.0.1:18789', signer, apiKey: () => 'ipc-key',
      fetch: async () => Response.json(body, { status }),
    });
    await assert.rejects(
      client.check({
        issuer: 'https://auth.example.com', subject: 'subject', clientId: 'client', capabilities: ['pulse:connect'],
      }, 'request-id'),
      (error: unknown) => error instanceof PrincipalCheckError && error.code === expected,
    );
  }
  for (const [fetcher, expected] of [
    [async () => { throw new Error('synthetic outage'); }, 'principal_store_unavailable'],
    [async () => Response.json({ secret: 'wrong success shape' }), 'invalid_principal'],
  ] as const) {
    const client = new TeamPrincipalClient({
      daemonBaseURL: 'http://127.0.0.1:18789', signer, apiKey: () => 'ipc-key', fetch: fetcher,
    });
    await assert.rejects(
      client.check({
        issuer: 'https://auth.example.com', subject: 'subject', clientId: 'client', capabilities: ['pulse:connect'],
      }, 'request-id'),
      (error: unknown) => error instanceof PrincipalCheckError && error.code === expected && error.status === 503,
    );
  }
});

test('strict loader rejects symlinks, group-readable keys, and active keyring mismatch', () => {
  const keys = keyFixture();
  const symlink = join(keys.dir, 'active-link.pem');
  symlinkSync(keys.privatePath, symlink);
  const base = {
    keyId: 'gateway-key-1',
    verifyKeyringFile: keys.keyringPath,
    storeId: 'store_test',
    teamId: 'team_test',
  };
  assert.throws(() => loadPrincipalSigner({ ...base, privateKeyFile: symlink }), /signing key file/);

  chmodSync(keys.privatePath, 0o644);
  assert.throws(() => loadPrincipalSigner({ ...base, privateKeyFile: keys.privatePath }), /signing key file/);

  const replacement = keyFixture();
  assert.throws(() => loadPrincipalSigner({
    ...base,
    privateKeyFile: replacement.privatePath,
  }), /does not match active verification keyring/);
});

test('strict loader accepts DER PKCS8 and runtime boundaries reject bad request IDs/capabilities', async () => {
  const keys = keyFixture();
  const derPath = join(keys.dir, 'active.pk8.der');
  writeFileSync(derPath, keys.privateKey.export({ format: 'der', type: 'pkcs8' }), { mode: 0o600 });
  const signer = loadPrincipalSigner({
    privateKeyFile: derPath,
    keyId: 'gateway-key-1',
    verifyKeyringFile: keys.keyringPath,
    storeId: 'store_test',
    teamId: 'team_test',
    now: () => NOW,
    randomId: () => 'assertion-jti',
  });
  const common = {
    method: 'POST',
    path: '/team/v1/principal/check',
    body: Buffer.from('{}'),
    oauthIssuer: 'https://auth.example.com',
    oauthSubject: 'subject',
    oauthClientId: 'client',
  };
  await assert.rejects(
    signer.signPrincipalCheck({ ...common, requestId: 'short', capabilities: ['pulse:connect'] }),
    /request ID/,
  );
  await assert.rejects(
    signer.signPrincipalCheck({
      ...common,
      requestId: 'request-id',
      capabilities: ['pulse:connect', 'pulse:root' as 'pulse:connect'],
    }),
    /capabilities/,
  );
  assert.throws(() => stablePrincipalCheckBody({
    oauthIssuer: 'https://auth.example.com', oauthSubject: ' subject ', oauthClientId: 'client',
    capabilities: ['pulse:connect'],
  }), /identity field/);
  assert.throws(() => stablePrincipalCheckBody({
    oauthIssuer: 'https://auth.example.com', oauthSubject: 'subject', oauthClientId: 'client\u0000id',
    capabilities: ['pulse:connect'],
  }), /identity field/);
});

test('verification keyring rejects duplicate and unbounded previous kids', () => {
  const keys = keyFixture();
  const publicKey = JSON.parse(readFileSync(keys.keyringPath, 'utf8')).active.public_key;
  const base = {
    privateKeyFile: keys.privatePath,
    keyId: 'gateway-key-1',
    verifyKeyringFile: keys.keyringPath,
    storeId: 'store_test',
    teamId: 'team_test',
  };
  writeFileSync(keys.keyringPath, JSON.stringify({
    active: { kid: 'gateway-key-1', public_key: publicKey },
    previous: [{ kid: 'gateway-key-1', public_key: publicKey }],
  }), { mode: 0o600 });
  assert.throws(() => loadPrincipalSigner(base), /verification keyring/);

  writeFileSync(keys.keyringPath, JSON.stringify({
    active: { kid: 'gateway-key-1', public_key: publicKey },
    previous: Array.from({ length: 5 }, (_, index) => ({ kid: `old-${index}`, public_key: publicKey })),
  }), { mode: 0o600 });
  assert.throws(() => loadPrincipalSigner(base), /verification keyring/);

  writeFileSync(keys.keyringPath, JSON.stringify({
    active: { kid: 'gateway-key-1', public_key: publicKey, unexpected: true },
    previous: [],
  }), { mode: 0o600 });
  assert.throws(() => loadPrincipalSigner(base), /verification keyring/);

  writeFileSync(keys.keyringPath, JSON.stringify({
    active: { kid: 'gateway-key-1', public_key: 'not-base64url' },
    previous: [],
  }), { mode: 0o600 });
  assert.throws(() => loadPrincipalSigner(base), /verification keyring/);

  const keyringLink = join(keys.dir, 'keyring-link.json');
  symlinkSync(keys.keyringPath, keyringLink);
  assert.throws(() => loadPrincipalSigner({ ...base, verifyKeyringFile: keyringLink }), /verification keyring file/);

  chmodSync(keys.keyringPath, 0o644);
  assert.throws(() => loadPrincipalSigner(base), /verification keyring file/);
});

test('concurrent principal checks stay request-local and never forward Authorization', async () => {
  const keys = keyFixture();
  const signer = loadPrincipalSigner({
    privateKeyFile: keys.privatePath,
    keyId: 'gateway-key-1',
    verifyKeyringFile: keys.keyringPath,
    storeId: 'store_test',
    teamId: 'team_test',
    now: () => NOW,
  });
  const requests: Array<{ headers: Headers; body: string }> = [];
  const securityEvents: Array<{ headers: Headers; body: string }> = [];
  const client = new TeamPrincipalClient({
    daemonBaseURL: 'http://127.0.0.1:18789',
    signer,
    apiKey: () => 'ipc-secret',
    fetch: async (input, init) => {
      const body = String(init?.body);
      const headers = new Headers(init?.headers);
      if (input.toString().endsWith('/team/v1/security-events')) {
        securityEvents.push({ headers, body });
        return new Response(null, { status: 204 });
      }
      requests.push({ headers, body });
      const parsed = JSON.parse(body);
      const suffix = parsed.oauth_subject === 'human-a' ? 'a' : 'b';
      return Response.json({
        version: 'pulse.team.principal_context.v1',
        request_id: `request-${suffix}`,
        store_id: 'store_test',
        team_id: 'team_test',
        principal_id: `principal-${suffix}`,
        principal_kind: 'agent',
        oauth_client_key: suffix.repeat(64),
        human_principal_id: `human-${suffix}`,
        agent_binding_id: `binding-${suffix}`,
        membership_id: `membership-${suffix}`,
        membership_role: 'member',
        team_auth_epoch: 2,
        principal_auth_epoch: 1,
        binding_auth_epoch: 1,
        membership_auth_epoch: 1,
        capabilities: parsed.capabilities,
      });
    },
  });
  const contexts = await Promise.all([
    client.check({
      issuer: 'https://auth.example.com', subject: 'human-a', clientId: 'client-a',
      capabilities: ['pulse:connect', 'pulse:read'],
    }, 'request-a'),
    client.check({
      issuer: 'https://auth.example.com', subject: 'human-b', clientId: 'client-b',
      capabilities: ['pulse:connect', 'pulse:write'],
    }, 'request-b'),
  ]);
  assert.deepEqual(contexts.map((context) => context.principal_id).sort(), ['principal-a', 'principal-b']);
  assert.deepEqual(contexts.map((context) => context.oauth_client_key).sort(), ['a'.repeat(64), 'b'.repeat(64)]);
  assert.equal('oauth_client_id' in contexts[0], false);
  assert.throws(() => {
    (contexts[0] as { capabilities: string[] }).capabilities.push('pulse:delete');
  }, TypeError);
  assert.equal(requests.length, 2);
  assert.deepEqual(requests.map(({ body }) => JSON.parse(body).oauth_subject).sort(), ['human-a', 'human-b']);
  const assertions = requests.map(({ headers }) => headers.get('x-pulse-principal'));
  assert.ok(assertions.every(Boolean));
  assert.notEqual(assertions[0], assertions[1]);
  for (const request of requests) {
    assert.equal(request.headers.has('authorization'), false);
    assert.equal(request.headers.get('x-pulse-key'), 'ipc-secret');
    assert.equal(request.headers.get('x-pulse-request-id'), JSON.parse(request.body).oauth_subject === 'human-a' ? 'request-a' : 'request-b');
    const verified = await jwtVerify(request.headers.get('x-pulse-principal') ?? '', keys.publicKey, {
      issuer: PRINCIPAL_ASSERTION_ISSUER,
      audience: PRINCIPAL_ASSERTION_AUDIENCE,
      algorithms: ['EdDSA'],
      currentDate: new Date(NOW * 1000),
    });
    assert.equal(verified.payload.body_sha256.length, 64);
  }

  await client.recordSecurityEvent({
    eventType: 'authentication_denied',
    reasonCode: 'missing_credential',
    methodClass: 'other',
    requestId: 'security-request',
  });
  assert.equal(securityEvents.length, 1);
  assert.deepEqual(JSON.parse(securityEvents[0].body), {
    event_type: 'authentication_denied',
    reason_code: 'missing_credential',
    method_class: 'other',
    path_class: 'mcp',
    request_id: 'security-request',
    count: 1,
  });
  assert.equal(securityEvents[0].headers.has('authorization'), false);
  assert.equal(securityEvents[0].headers.has('x-pulse-principal'), false);
  const gatewayAssertion = securityEvents[0].headers.get('x-pulse-gateway-assertion') ?? '';
  assert.deepEqual(decodeProtectedHeader(gatewayAssertion), {
    alg: 'EdDSA', kid: 'gateway-key-1', typ: SECURITY_EVENT_ASSERTION_VERSION,
  });
  const verifiedGatewayAssertion = await jwtVerify(gatewayAssertion, keys.publicKey, {
    issuer: PRINCIPAL_ASSERTION_ISSUER,
    audience: PRINCIPAL_ASSERTION_AUDIENCE,
    algorithms: ['EdDSA'],
    currentDate: new Date(NOW * 1000),
  });
  const { jti: gatewayJti, ...gatewayPayload } = verifiedGatewayAssertion.payload;
  assert.equal(typeof gatewayJti, 'string');
  assert.deepEqual(gatewayPayload, {
    version: SECURITY_EVENT_ASSERTION_VERSION,
    request_id: 'security-request',
    method: 'POST',
    path: '/team/v1/security-events',
    body_sha256: createHash('sha256').update(securityEvents[0].body).digest('hex'),
    store_id: 'store_test',
    team_id: 'team_test',
    iss: PRINCIPAL_ASSERTION_ISSUER,
    aud: PRINCIPAL_ASSERTION_AUDIENCE,
    iat: NOW,
    nbf: NOW - 1,
    exp: NOW + 30,
  });
  assert.equal('oauth_subject' in verifiedGatewayAssertion.payload, false);
  assert.equal('capabilities' in verifiedGatewayAssertion.payload, false);

  await client.recordSecurityEvent({
    eventType: 'principal_assertion_denied', reasonCode: 'assertion_invalid',
    methodClass: 'write', requestId: 'assertion-request',
  });
  await client.recordSecurityEvent({
    eventType: 'principal_assertion_denied', reasonCode: 'assertion_replayed',
    methodClass: 'write', requestId: 'replayed-request',
  });
  await client.recordSecurityEvent({
    eventType: 'authorization_denied', reasonCode: 'principal_revoked',
    methodClass: 'read', requestId: 'revoked-request',
  });
  await client.recordSecurityEvent({
    eventType: 'audit_degraded', reasonCode: 'store_unavailable',
    methodClass: 'other', requestId: 'store-request',
  });
  assert.deepEqual(securityEvents.slice(1).map(({ body }) => JSON.parse(body)), [
    {
      event_type: 'principal_assertion_denied', reason_code: 'assertion_invalid',
      method_class: 'write', path_class: 'mcp', request_id: 'assertion-request', count: 1,
    },
    {
      event_type: 'principal_assertion_denied', reason_code: 'assertion_replayed',
      method_class: 'write', path_class: 'mcp', request_id: 'replayed-request', count: 1,
    },
    {
      event_type: 'authorization_denied', reason_code: 'principal_revoked',
      method_class: 'read', path_class: 'mcp', request_id: 'revoked-request', count: 1,
    },
    {
      event_type: 'audit_degraded', reason_code: 'store_unavailable',
      method_class: 'other', path_class: 'mcp', request_id: 'store-request', count: 1,
    },
  ]);
  const eventJtis = await Promise.all(securityEvents.map(async ({ headers }) => {
    const verified = await jwtVerify(headers.get('x-pulse-gateway-assertion') ?? '', keys.publicKey, {
      issuer: PRINCIPAL_ASSERTION_ISSUER, audience: PRINCIPAL_ASSERTION_AUDIENCE,
      algorithms: ['EdDSA'], currentDate: new Date(NOW * 1000),
    });
    return verified.payload.jti;
  }));
  assert.equal(new Set(eventJtis).size, eventJtis.length, 'every security event attempt needs a fresh JTI');

  const reporter = new BoundedSecurityEventReporter(client, { log: () => undefined });
  for (let index = 0; index < 300; index++) {
    reporter.report({
      eventType: 'authentication_denied', reasonCode: 'missing_credential',
      methodClass: 'other', requestId: `flood-${String(index).padStart(4, '0')}`,
    });
  }
  await reporter.drain();
  const flood = securityEvents.slice(5).map(({ body }) => JSON.parse(body));
  assert.ok(flood.length <= 3, `expected coalesced security events, got ${flood.length}`);
  assert.ok(flood.every((event) => event.count <= 256));
  assert.equal(flood.reduce((total, event) => total + event.count, 0), 300);

  const rateLimitedClient = new TeamPrincipalClient({
    daemonBaseURL: 'http://127.0.0.1:18789', signer, apiKey: () => 'ipc-secret',
    fetch: async () => new Response(null, { status: 429 }),
  });
  await assert.rejects(
    rateLimitedClient.recordSecurityEvent({
      eventType: 'authentication_denied', reasonCode: 'missing_credential',
      methodClass: 'other', requestId: 'limited-request',
    }),
    SecurityEventRateLimitedError,
  );
  const degradedSignals: string[] = [];
  const limitedReporter = new BoundedSecurityEventReporter(rateLimitedClient, {
    log: (message) => degradedSignals.push(message),
  });
  limitedReporter.report({
    eventType: 'authentication_denied', reasonCode: 'missing_credential',
    methodClass: 'other', requestId: 'limited-reporter-request',
  });
  await limitedReporter.drain();
  assert.deepEqual(degradedSignals, ['[pulse-mcp] team security audit rate limited']);

  const overflowSignals: string[] = [];
  const overflowReporter = new BoundedSecurityEventReporter(client, {
    log: (message) => overflowSignals.push(message),
  });
  const authenticationReasons = [
    'missing_credential', 'malformed_credential', 'invalid_credential',
    'expired_credential', 'credential_not_yet_valid', 'issuer_mismatch',
    'audience_mismatch', 'incomplete_claims', 'unknown_signing_key',
  ] as const;
  const methods = ['read', 'write', 'delete', 'other'] as const;
  let overflowIndex = 0;
  for (const reasonCode of authenticationReasons) {
    for (const methodClass of methods) {
      overflowReporter.report({
        eventType: 'authentication_denied', reasonCode, methodClass,
        requestId: `overflow-${String(overflowIndex++).padStart(3, '0')}`,
      });
    }
  }
  assert.deepEqual(overflowSignals, ['[pulse-mcp] team security audit degraded']);
  await overflowReporter.drain();
});
