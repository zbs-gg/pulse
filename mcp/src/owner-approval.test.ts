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
  canonicalOwnerActivateBody,
  canonicalOwnerApprovalBody,
  canonicalOwnerBootstrapBody,
  isExactOwnerBrowserRequest,
  isOwnerPublicPath,
  OWNER_ACTIVATE_INTERNAL_PATH,
  OWNER_ACTIVATE_PUBLIC_PATH,
  OWNER_APPROVAL_INTERNAL_PATH,
  OWNER_APPROVAL_PUBLIC_PATH,
  OWNER_BOOTSTRAP_INTERNAL_PATH,
  OWNER_BOOTSTRAP_PUBLIC_PATH,
  OWNER_STEP_UP_ASSERTION_VERSION,
  OwnerApprovalGateway,
  OwnerGatewayError,
  ownerActivationTargetDigest,
  ownerBootstrapTargetDigest,
} from './owner-approval.js';
import {
  loadPrincipalSigner,
  PRINCIPAL_ASSERTION_AUDIENCE,
} from './principal-context.js';
import { TEAM_TOOL_DESCRIPTORS } from './team-contracts.js';
import type { VerifiedOAuthIdentity } from './oauth-resource.js';

const NOW = 1_789_000_000;

function keyFixture() {
  const dir = mkdtempSync(join(tmpdir(), 'pulse-owner-gateway-'));
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

function signerFixture() {
  const keys = keyFixture();
  return {
    ...keys,
    signer: loadPrincipalSigner({
      privateKeyFile: keys.privatePath,
      keyId: 'gateway-key-1',
      verifyKeyringFile: keys.keyringPath,
      storeId: 'store_test',
      teamId: 'team_test',
      now: () => NOW,
      randomId: () => 'owner-step-up-jti-001',
    }),
  };
}

function ownerIdentity(overrides: Partial<VerifiedOAuthIdentity> = {}): VerifiedOAuthIdentity {
  return {
    issuer: 'https://auth.example.com',
    subject: 'owner-human-subject',
    clientId: 'owner-browser-client',
    capabilities: ['pulse:connect', 'pulse:owner'],
    authTime: NOW,
    ...overrides,
  };
}

function bootstrapIntent() {
  return {
    store_id: 'store_test',
    team_id: 'team_test',
    owner_principal_id: 'principal_owner',
    owner_membership_id: 'membership_owner',
  };
}

function bootstrapApprovalInput() {
  const intent = bootstrapIntent();
  const teamName = 'Pulse synthetic pilot';
  return {
    schema: 'pulse.team.owner.approval.v1',
    action: 'team.bootstrap',
    store_id: 'store_test',
    team_id: 'team_test',
    target_kind: 'team',
    target_id: 'team_test',
    target_digest: ownerBootstrapTargetDigest(intent, teamName),
    team_name: teamName,
    bootstrap_intent: intent,
  };
}

function activationApprovalInput() {
  const gateDigest = 'c'.repeat(64);
  return {
    schema: 'pulse.team.owner.approval.v1',
    action: 'team.activation.synthetic',
    store_id: 'store_test',
    team_id: 'team_test',
    target_kind: 'team_activation',
    target_id: 'team_test',
    target_digest: ownerActivationTargetDigest('store_test', 'team_test', gateDigest),
    gate_digest: gateDigest,
  };
}

function approvalResult(input: ReturnType<typeof bootstrapApprovalInput>) {
  return {
    schema: 'pulse.team.owner.approval_result.v1',
    approval_nonce: 'd'.repeat(64),
    action: input.action,
    store_id: input.store_id,
    team_id: input.team_id,
    target_kind: input.target_kind,
    target_id: input.target_id,
    expires_at: '2026-09-10T00:30:40.000Z',
    fallback: false,
  };
}

test('Owner browser routes are exact and remain completely outside MCP', () => {
  assert.deepEqual([
    OWNER_APPROVAL_PUBLIC_PATH,
    OWNER_BOOTSTRAP_PUBLIC_PATH,
    OWNER_ACTIVATE_PUBLIC_PATH,
  ], ['/owner/v1/approval', '/owner/v1/bootstrap', '/owner/v1/activate']);
  for (const path of [
    OWNER_APPROVAL_PUBLIC_PATH, OWNER_BOOTSTRAP_PUBLIC_PATH, OWNER_ACTIVATE_PUBLIC_PATH,
  ]) {
    assert.equal(isOwnerPublicPath(path), true);
  }
  for (const near of ['/owner/v1/approval/', '/owner/v1/bootstrap?debug=1', '/team/v1/owner/approval']) {
    assert.equal(isOwnerPublicPath(near), false);
  }
  assert.equal(TEAM_TOOL_DESCRIPTORS.some(({ name }) => name.includes('owner')), false);
  assert.equal(isExactOwnerBrowserRequest({
    origin: 'https://pulse.example.com',
    host: 'pulse.example.com',
    publicBaseURL: 'https://pulse.example.com',
    allowedOrigins: new Set(['https://pulse.example.com']),
  }), true);
  for (const headers of [
    { origin: '', host: 'pulse.example.com' },
    { origin: 'https://evil.example', host: 'pulse.example.com' },
    { origin: 'https://pulse.example.com', host: 'evil.example' },
    { origin: 'https://pulse.example.com/', host: 'pulse.example.com' },
  ]) {
    assert.equal(isExactOwnerBrowserRequest({
      ...headers,
      publicBaseURL: 'https://pulse.example.com',
      allowedOrigins: new Set(['https://pulse.example.com']),
    }), false);
  }
});

test('Owner canonical bodies are closed, deterministic, and contain no caller authority', () => {
  assert.equal(
    ownerBootstrapTargetDigest(bootstrapIntent(), 'Pulse synthetic pilot'),
    'c015b6f66d46bb78311c542a94a54c395f206fc3d9828844a88d28c166596a86',
  );
  assert.equal(
    ownerActivationTargetDigest('store_test', 'team_test', 'c'.repeat(64)),
    'e47e9c046d7d263703e80f6b0d6625f813d8f503ab00b7177094af0d0304f849',
  );
  assert.equal(
    canonicalOwnerBootstrapBody({ schema: 'pulse.team.owner.bootstrap.v1', operation: 'prepare' }).text,
    '{"schema":"pulse.team.owner.bootstrap.v1","operation":"prepare"}',
  );
  const execute = {
    schema: 'pulse.team.owner.bootstrap.v1', operation: 'execute',
    team_name: 'Pulse synthetic pilot', bootstrap_intent: bootstrapIntent(),
    approval_nonce: 'd'.repeat(64),
  };
  assert.equal(canonicalOwnerBootstrapBody(execute).text, JSON.stringify(execute));
  assert.equal(canonicalOwnerApprovalBody(bootstrapApprovalInput()).text,
    JSON.stringify(bootstrapApprovalInput()));
  assert.equal(canonicalOwnerApprovalBody(activationApprovalInput()).text,
    JSON.stringify(activationApprovalInput()));
  const activate = {
    schema: 'pulse.team.owner.activate.v1', approval_nonce: 'd'.repeat(64),
    gate_digest: 'c'.repeat(64),
  };
  assert.equal(canonicalOwnerActivateBody(activate).text, JSON.stringify(activate));

  for (const invalid of [
    { ...bootstrapApprovalInput(), role: 'owner' },
    { ...bootstrapApprovalInput(), actor_principal_id: 'principal_spoof' },
    { ...bootstrapApprovalInput(), team_name: '/Users/example/private' },
    { ...bootstrapApprovalInput(), target_digest: 'a'.repeat(64) },
    { ...activationApprovalInput(), target_id: 'other_team' },
    { ...activationApprovalInput(), target_digest: 'not-a-digest' },
  ]) {
    assert.throws(() => canonicalOwnerApprovalBody(invalid), OwnerGatewayError);
  }
});

test('Owner IPC forwarding accepts only the current numeric-loopback daemon boundary', () => {
  const { signer } = signerFixture();
  const make = (daemonBaseURL: string) => new OwnerApprovalGateway({
    daemonBaseURL, signer, apiKey: () => 'ipc-secret', now: () => NOW,
  });
  assert.doesNotThrow(() => make('http://127.0.0.1:18789'));
  assert.doesNotThrow(() => make('http://[::1]:18789'));
  assert.throws(() => make('https://pulse.example.com'));
  assert.throws(() => make('http://localhost:18789'));
});

test('Owner gateway rejects missing, stale, future, or under-scoped browser step-up before IPC', async () => {
  const { signer } = signerFixture();
  let calls = 0;
  const gateway = new OwnerApprovalGateway({
    daemonBaseURL: 'http://127.0.0.1:18789', signer, apiKey: () => 'ipc-secret',
    now: () => NOW, fetch: async () => { calls++; return Response.json({}); },
  });
  const identities = [
    ownerIdentity({ authTime: undefined }),
    ownerIdentity({ authTime: NOW - 301 }),
    ownerIdentity({ authTime: NOW + 1 }),
    ownerIdentity({ capabilities: ['pulse:connect'] }),
  ];
  for (const identity of identities) {
    await assert.rejects(
      gateway.call(OWNER_APPROVAL_PUBLIC_PATH, identity, 'owner-request-001', bootstrapApprovalInput()),
      (error: unknown) => error instanceof OwnerGatewayError &&
        error.code === 'owner_step_up_required' && !/token|subject|client/i.test(error.message),
    );
  }
  assert.equal(calls, 0);
});

test('approval signs one exact action/body/path-bound assertion and forwards no bearer authority', async () => {
  const { signer, publicKey } = signerFixture();
  const input = bootstrapApprovalInput();
  const requests: Array<{ path: string; headers: Headers; body: string; redirect: RequestRedirect | undefined }> = [];
  const gateway = new OwnerApprovalGateway({
    daemonBaseURL: 'http://127.0.0.1:18789', signer, apiKey: () => 'ipc-secret', now: () => NOW,
    fetch: async (request, init) => {
      const url = new URL(request.toString());
      requests.push({
        path: url.pathname, headers: new Headers(init?.headers),
        body: String(init?.body), redirect: init?.redirect,
      });
      return Response.json(approvalResult(input));
    },
  });
  assert.deepEqual(
    await gateway.call(OWNER_APPROVAL_PUBLIC_PATH, ownerIdentity(), 'owner-request-001', input),
    approvalResult(input),
  );
  assert.equal(requests.length, 1);
  assert.equal(requests[0].path, OWNER_APPROVAL_INTERNAL_PATH);
  assert.equal(requests[0].body, canonicalOwnerApprovalBody(input).text);
  assert.equal(requests[0].redirect, 'error');
  assert.deepEqual([...requests[0].headers.keys()].sort(), [
    'content-type', 'x-pulse-key', 'x-pulse-owner-step-up', 'x-pulse-request-id',
  ]);
  assert.equal(requests[0].headers.has('authorization'), false);
  const assertion = requests[0].headers.get('x-pulse-owner-step-up') ?? '';
  const verified = await jwtVerify(assertion, publicKey, {
    issuer: 'pulse-team-gateway', audience: PRINCIPAL_ASSERTION_AUDIENCE,
    algorithms: ['EdDSA'], currentDate: new Date(NOW * 1000),
  });
  assert.deepEqual(verified.protectedHeader, {
    alg: 'EdDSA', kid: 'gateway-key-1', typ: OWNER_STEP_UP_ASSERTION_VERSION,
  });
  assert.equal(verified.payload.version, OWNER_STEP_UP_ASSERTION_VERSION);
  assert.equal(verified.payload.path, OWNER_APPROVAL_INTERNAL_PATH);
  assert.equal(verified.payload.action, 'team.bootstrap');
  assert.equal(verified.payload.store_id, 'store_test');
  assert.equal(verified.payload.team_id, 'team_test');
  assert.equal(verified.payload.oauth_issuer, ownerIdentity().issuer);
  assert.equal(verified.payload.oauth_subject, ownerIdentity().subject);
  assert.equal(verified.payload.oauth_client_id, ownerIdentity().clientId);
  assert.equal(verified.payload.auth_time, NOW);
  for (const forbidden of ['authorization', 'role', 'principal_id', 'membership_id', 'capabilities']) {
    assert.equal(forbidden in verified.payload, false, forbidden);
  }
});

test('bootstrap and activate use exact loopback routes, one-time nonce bodies, and no Owner assertion replay', async () => {
  const { signer } = signerFixture();
  const prepared = {
    schema: 'pulse.team.owner.bootstrap_result.v1', operation: 'prepared',
    bootstrap_intent: bootstrapIntent(), fallback: false,
  };
  const complete = {
    schema: 'pulse.team.owner.bootstrap_result.v1', operation: 'complete',
    ...bootstrapIntent(), activation_state: 'inactive', content_boundary: 'synthetic',
    public_enabled: false, fallback: false,
  };
  const activated = {
    schema: 'pulse.team.owner.activate_result.v1', store_id: 'store_test', team_id: 'team_test',
    activation_state: 'active', content_boundary: 'synthetic', public_enabled: true,
    gate_digest: 'c'.repeat(64), activated_by_principal_id: 'principal_owner',
    audit_event_id: 'audit_activation_001', activated_at: '2026-09-10T00:26:40.000Z',
    fallback: false,
  };
  const requests: Array<{ path: string; headers: Headers }> = [];
  const gateway = new OwnerApprovalGateway({
    daemonBaseURL: 'http://127.0.0.1:18789', signer, apiKey: () => 'ipc-secret', now: () => NOW,
    fetch: async (request, init) => {
      const path = new URL(request.toString()).pathname;
      requests.push({ path, headers: new Headers(init?.headers) });
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      if (path === OWNER_BOOTSTRAP_INTERNAL_PATH && body.operation === 'prepare') return Response.json(prepared);
      if (path === OWNER_BOOTSTRAP_INTERNAL_PATH) return Response.json(complete);
      if (path === OWNER_ACTIVATE_INTERNAL_PATH) return Response.json(activated);
      throw new Error('wrong internal path');
    },
  });
  assert.deepEqual(await gateway.call(
    OWNER_BOOTSTRAP_PUBLIC_PATH, ownerIdentity(), 'owner-request-prepare',
    { schema: 'pulse.team.owner.bootstrap.v1', operation: 'prepare' },
  ), prepared);
  assert.deepEqual(await gateway.call(
    OWNER_BOOTSTRAP_PUBLIC_PATH, ownerIdentity(), 'owner-request-execute', {
      schema: 'pulse.team.owner.bootstrap.v1', operation: 'execute',
      team_name: 'Pulse synthetic pilot', bootstrap_intent: bootstrapIntent(),
      approval_nonce: 'd'.repeat(64),
    },
  ), complete);
  assert.deepEqual(await gateway.call(
    OWNER_ACTIVATE_PUBLIC_PATH, ownerIdentity(), 'owner-request-activate', {
      schema: 'pulse.team.owner.activate.v1', approval_nonce: 'd'.repeat(64),
      gate_digest: 'c'.repeat(64),
    },
  ), activated);
  assert.deepEqual(requests.map(({ path }) => path), [
    OWNER_BOOTSTRAP_INTERNAL_PATH, OWNER_BOOTSTRAP_INTERNAL_PATH, OWNER_ACTIVATE_INTERNAL_PATH,
  ]);
  for (const request of requests) {
    assert.deepEqual([...request.headers.keys()].sort(), [
      'content-type', 'x-pulse-key', 'x-pulse-request-id',
    ]);
    assert.equal(request.headers.has('authorization'), false);
    assert.equal(request.headers.has('x-pulse-owner-step-up'), false);
  }
});

test('Owner gateway rejects malformed daemon success and collapses denial details without retry', async () => {
  const { signer } = signerFixture();
  let calls = 0;
  const denied = new OwnerApprovalGateway({
    daemonBaseURL: 'http://127.0.0.1:18789', signer, apiKey: () => 'ipc-secret', now: () => NOW,
    fetch: async () => {
      calls++;
      return Response.json({ error: 'raw-owner-subject-and-token', fallback: false }, { status: 403 });
    },
  });
  await assert.rejects(
    denied.call(OWNER_APPROVAL_PUBLIC_PATH, ownerIdentity(), 'owner-request-denied', bootstrapApprovalInput()),
    (error: unknown) => error instanceof OwnerGatewayError &&
      error.code === 'owner_operation_denied' && !/raw|token|subject/i.test(error.message),
  );
  assert.equal(calls, 1);

  const malformed = new OwnerApprovalGateway({
    daemonBaseURL: 'http://127.0.0.1:18789', signer, apiKey: () => 'ipc-secret', now: () => NOW,
    fetch: async () => Response.json({ ...approvalResult(bootstrapApprovalInput()), debug_path: '/private/db' }),
  });
  await assert.rejects(
    malformed.call(OWNER_APPROVAL_PUBLIC_PATH, ownerIdentity(), 'owner-request-malformed', bootstrapApprovalInput()),
    (error: unknown) => error instanceof OwnerGatewayError &&
      error.code === 'owner_service_unavailable',
  );
});

test('Owner step-up signer allows only the exact Airlock publication action and path pair', async () => {
  const { signer, publicKey } = signerFixture();
  const body = Buffer.from('{"action":"team.commons.publish","schema":"pulse.team.airlock_envelope.v1"}');
  const assertion = await signer.signOwnerStepUp({
    requestId: 'owner-airlock-request-001',
    path: '/airlock/team-publication',
    action: 'team.commons.publish',
    body,
    storeId: 'store_test',
    teamId: 'team_test',
    oauthIssuer: ownerIdentity().issuer,
    oauthSubject: ownerIdentity().subject,
    oauthClientId: ownerIdentity().clientId,
    authTime: NOW,
  });
  const verified = await jwtVerify(assertion, publicKey, {
    issuer: 'pulse-team-gateway', audience: PRINCIPAL_ASSERTION_AUDIENCE,
    algorithms: ['EdDSA'], currentDate: new Date(NOW * 1000),
  });
  assert.equal(verified.payload.path, '/airlock/team-publication');
  assert.equal(verified.payload.action, 'team.commons.publish');

  await assert.rejects(signer.signOwnerStepUp({
    requestId: 'owner-airlock-request-002',
    path: '/airlock/team-publication/near',
    action: 'team.commons.publish',
    body,
    storeId: 'store_test',
    teamId: 'team_test',
    oauthIssuer: ownerIdentity().issuer,
    oauthSubject: ownerIdentity().subject,
    oauthClientId: ownerIdentity().clientId,
    authTime: NOW,
  } as never), /binding is invalid/);
});
