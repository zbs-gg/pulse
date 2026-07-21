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
  canonicalOwnerAdminMutationBody,
  canonicalOwnerApprovalBody,
  canonicalOwnerAuditBody,
  canonicalOwnerBootstrapBody,
  canonicalOwnerDeletionStatusBody,
  canonicalOwnerSharedDeleteBody,
  isExactOwnerBrowserRequest,
  isOwnerPublicPath,
  OWNER_ACTIVATE_INTERNAL_PATH,
  OWNER_ACTIVATE_PUBLIC_PATH,
  OWNER_APPROVAL_INTERNAL_PATH,
  OWNER_APPROVAL_PUBLIC_PATH,
  OWNER_AUDIT_INTERNAL_PATH,
  OWNER_AUDIT_PUBLIC_PATH,
  OWNER_BOOTSTRAP_INTERNAL_PATH,
  OWNER_BOOTSTRAP_PUBLIC_PATH,
  OWNER_BINDINGS_INTERNAL_PATH,
  OWNER_BINDINGS_PUBLIC_PATH,
  OWNER_MEMBERS_INTERNAL_PATH,
  OWNER_MEMBERS_PUBLIC_PATH,
  OWNER_PROJECT_GRANTS_INTERNAL_PATH,
  OWNER_PROJECT_GRANTS_PUBLIC_PATH,
  OWNER_PROJECTS_INTERNAL_PATH,
  OWNER_PROJECTS_PUBLIC_PATH,
  OWNER_SERVICES_INTERNAL_PATH,
  OWNER_SERVICES_PUBLIC_PATH,
  OWNER_SHARED_DELETE_INTERNAL_PATH,
  OWNER_SHARED_DELETE_PUBLIC_PATH,
  OWNER_DELETION_STATUS_INTERNAL_PATH,
  OWNER_DELETION_STATUS_PUBLIC_PATH,
  OWNER_STEP_UP_ASSERTION_VERSION,
  OwnerApprovalGateway,
  OwnerGatewayError,
  ownerAdminMutationTarget,
  ownerActivationTargetDigest,
  ownerAuditTargetDigest,
  ownerBootstrapTargetDigest,
  ownerDeletionStatusTargetDigest,
  ownerOperationStepUpNonce,
  ownerSharedDeletionTargetDigest,
} from './owner-approval.js';
import {
  loadPrincipalSigner,
  PRINCIPAL_ASSERTION_AUDIENCE,
} from './principal-context.js';
import { TEAM_TOOL_DESCRIPTORS } from './team-contracts.js';
import type { VerifiedOAuthIdentity } from './oauth-resource.js';

const NOW = 1_789_000_000;
const BROWSER_STEP_UP = { assertionJTI: `owner_browser_${'b'.repeat(43)}` } as const;

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
    issuer: 'https://auth.example.com/',
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
    OWNER_MEMBERS_PUBLIC_PATH,
    OWNER_BINDINGS_PUBLIC_PATH,
    OWNER_SERVICES_PUBLIC_PATH,
    OWNER_PROJECTS_PUBLIC_PATH,
    OWNER_PROJECT_GRANTS_PUBLIC_PATH,
    OWNER_SHARED_DELETE_PUBLIC_PATH,
    OWNER_AUDIT_PUBLIC_PATH,
    OWNER_DELETION_STATUS_PUBLIC_PATH,
  ], [
    '/owner/v1/approval', '/owner/v1/bootstrap', '/owner/v1/activate',
    '/owner/v1/members', '/owner/v1/bindings', '/owner/v1/services',
    '/owner/v1/projects', '/owner/v1/project-grants', '/owner/v1/shared-delete',
    '/owner/v1/audit', '/owner/v1/deletion-status',
  ]);
  for (const path of [
    OWNER_APPROVAL_PUBLIC_PATH, OWNER_BOOTSTRAP_PUBLIC_PATH, OWNER_ACTIVATE_PUBLIC_PATH,
    OWNER_MEMBERS_PUBLIC_PATH, OWNER_BINDINGS_PUBLIC_PATH, OWNER_SERVICES_PUBLIC_PATH,
    OWNER_PROJECTS_PUBLIC_PATH, OWNER_PROJECT_GRANTS_PUBLIC_PATH, OWNER_SHARED_DELETE_PUBLIC_PATH,
    OWNER_AUDIT_PUBLIC_PATH, OWNER_DELETION_STATUS_PUBLIC_PATH,
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

test('Owner mutation approvals derive the exact Go action target and reject field smuggling', () => {
  const cases = [
    {
      action: 'membership.create', mutation: {
        issuer: 'https://issuer.example/', subject: 'dima', role: 'member',
      }, target: {
        kind: 'membership',
        id: '3e8a6392ce47efbb12d56201c9b41f72d439aa597c5827a001906e8a366502cd',
        digest: '2daeababfb56d90a9c0d3106078e75411810901ced860613cef96cf07ff3817c',
      },
    },
    {
      action: 'agent_binding.create', mutation: {
        issuer: 'https://issuer.example/', subject: 'dima', client_id: 'codex-dima',
      }, target: {
        kind: 'agent_binding',
        id: 'd63360386642235e2a3058411110b773d20589e961695e5602cef25f5ecfbaff',
        digest: '2961cfb7c064aa13b5bf2ecc0d485a54deab69d93a137d3f73e2097e35b19c09',
      },
    },
    {
      action: 'service_principal.create', mutation: {
        issuer: 'https://issuer.example/', client_id: 'projection-worker',
      }, target: {
        kind: 'service_principal',
        id: 'f9a47bfabb0c346943992e00d62072672772dde5686dc9124f8cec64bbea5ff6',
        digest: '7e219ea529d634f57f0488dc63f44ff552d5083b933c4b9c14093feb2b1fdaf5',
      },
    },
    {
      action: 'project.create', mutation: { name: 'Pulse Pilot' }, target: {
        kind: 'project',
        id: '2417e3ada53cff9d0653ade171d046727f3c28449129eb5fd17e6a26ae91878a',
        digest: '1e72df7b6d946d5caaa769f260efceb32ea238e03d086b277ccf270d9d8ecbf4',
      },
    },
    {
      action: 'project_grant.create', mutation: {
        project_id: 'project_alpha', target_principal_id: 'principal_dima', access_level: 'write',
      }, target: {
        kind: 'project_grant',
        id: 'dfc8cd7b20832a269a8d70da9c0906da5d6a6d2f73f31005eeb47fda0726c1b5',
        digest: '41ded0790ae34fb4a44834c324619ac7e4dde17b79441beb4b1e6f2e9f2f1cd2',
      },
    },
    {
      action: 'membership.revoke', mutation: { target_id: 'principal_dima' }, target: {
        kind: 'membership', id: 'principal_dima',
        digest: '7413c11ff2aca3ff1844d8f0aaf9168b2b2021e5651071d19d0ce05f0f453e38',
      },
    },
    {
      action: 'agent_binding.revoke', mutation: { target_id: 'binding_dima' }, target: {
        kind: 'agent_binding', id: 'binding_dima',
        digest: '80194ee5b27c394039f70288a1b115806d624a07985c80ec7a6f955fb9ce2980',
      },
    },
    {
      action: 'service_principal.revoke', mutation: { target_id: 'principal_worker' }, target: {
        kind: 'service_principal', id: 'principal_worker',
        digest: 'a7c0a30e88a7cc16425fc83a261a71446fd653d9e183a3ffb6b5248a67713ced',
      },
    },
    {
      action: 'project_grant.revoke', mutation: { target_id: 'grant_dima' }, target: {
        kind: 'project_grant', id: 'grant_dima',
        digest: '580a2adaeed6686797652dc8e56664ac36c708d3e10db4b6e1ca4fad54e8aaf5',
      },
    },
  ] as const;

  for (const item of cases) {
    assert.deepEqual(ownerAdminMutationTarget(item.action, item.mutation), item.target);
    const approval = {
      schema: 'pulse.team.owner.approval.v1', action: item.action,
      store_id: 'store_test', team_id: 'team_test', target_kind: item.target.kind,
      target_id: item.target.id, target_digest: item.target.digest, mutation: item.mutation,
    };
    assert.equal(canonicalOwnerApprovalBody(approval).text, JSON.stringify(approval));
    assert.throws(
      () => canonicalOwnerApprovalBody({ ...approval, actor_principal_id: 'principal_spoof' }),
      OwnerGatewayError,
    );
    assert.throws(
      () => canonicalOwnerApprovalBody({ ...approval, target_digest: 'a'.repeat(64) }),
      OwnerGatewayError,
    );
    assert.throws(
      () => canonicalOwnerApprovalBody({
        ...approval, mutation: { ...item.mutation, action: 'membership.revoke' },
      }),
      OwnerGatewayError,
    );
  }

  assert.equal(
    ownerSharedDeletionTargetDigest('object_shared_1'),
    '6ad44ef0d128bb829e2184a5ae04216fa8af6b2976362264e506f067fdafbd80',
  );
  const sharedApproval = {
    schema: 'pulse.team.owner.approval.v1', action: 'team.object.delete.shared',
    store_id: 'store_test', team_id: 'team_test', target_kind: 'team_object',
    target_id: 'object_shared_1', target_digest: ownerSharedDeletionTargetDigest('object_shared_1'),
  };
  assert.equal(canonicalOwnerApprovalBody(sharedApproval).text, JSON.stringify(sharedApproval));
});

test('Owner operation browser nonce matches the CLI cross-runtime vector and binds exact approval bytes', () => {
  const mutation = {
    issuer: 'https://issuer.example/', subject: 'member-subject', role: 'member',
  };
  const target = ownerAdminMutationTarget('membership.create', mutation);
  const approval = {
    schema: 'pulse.team.owner.approval.v1', action: 'membership.create',
    store_id: 'store_test', team_id: 'team_test', target_kind: target.kind,
    target_id: target.id, target_digest: target.digest, mutation,
  };
  const challenge = 'BwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwc';
  assert.equal(
    ownerOperationStepUpNonce(approval, challenge),
    'aiAVs-kId_SG9kMUf4KlBj38BX-UTFmxO2R9BFLcfLE',
  );
  const changedMutation = { ...mutation, role: 'reviewer' };
  const changedTarget = ownerAdminMutationTarget('membership.create', changedMutation);
  assert.notEqual(
    ownerOperationStepUpNonce({
      ...approval, target_id: changedTarget.id, target_digest: changedTarget.digest,
      mutation: changedMutation,
    }, challenge),
    'aiAVs-kId_SG9kMUf4KlBj38BX-UTFmxO2R9BFLcfLE',
  );
});

test('Owner execution bodies are route-specific closed unions with no actor or Team authority', () => {
  const nonce = 'd'.repeat(64);
  const bodies = [
    [OWNER_MEMBERS_PUBLIC_PATH, {
      schema: 'pulse.team.owner.members.v1', action: 'membership.create', approval_nonce: nonce,
      issuer: 'https://issuer.example/', subject: 'dima', role: 'member',
    }],
    [OWNER_BINDINGS_PUBLIC_PATH, {
      schema: 'pulse.team.owner.bindings.v1', action: 'agent_binding.create', approval_nonce: nonce,
      issuer: 'https://issuer.example/', subject: 'dima', client_id: 'codex-dima',
    }],
    [OWNER_SERVICES_PUBLIC_PATH, {
      schema: 'pulse.team.owner.services.v1', action: 'service_principal.create', approval_nonce: nonce,
      issuer: 'https://issuer.example/', client_id: 'projection-worker',
    }],
    [OWNER_PROJECTS_PUBLIC_PATH, {
      schema: 'pulse.team.owner.projects.v1', action: 'project.create', approval_nonce: nonce,
      name: 'Pulse Pilot',
    }],
    [OWNER_PROJECT_GRANTS_PUBLIC_PATH, {
      schema: 'pulse.team.owner.project_grants.v1', action: 'project_grant.create', approval_nonce: nonce,
      project_id: 'project_alpha', target_principal_id: 'principal_dima', access_level: 'write',
    }],
    [OWNER_MEMBERS_PUBLIC_PATH, {
      schema: 'pulse.team.owner.members.v1', action: 'membership.revoke', approval_nonce: nonce,
      target_id: 'principal_dima',
    }],
    [OWNER_BINDINGS_PUBLIC_PATH, {
      schema: 'pulse.team.owner.bindings.v1', action: 'agent_binding.revoke', approval_nonce: nonce,
      target_id: 'binding_dima',
    }],
    [OWNER_SERVICES_PUBLIC_PATH, {
      schema: 'pulse.team.owner.services.v1', action: 'service_principal.revoke', approval_nonce: nonce,
      target_id: 'principal_worker',
    }],
    [OWNER_PROJECT_GRANTS_PUBLIC_PATH, {
      schema: 'pulse.team.owner.project_grants.v1', action: 'project_grant.revoke', approval_nonce: nonce,
      target_id: 'grant_dima',
    }],
  ] as const;
  for (const [path, input] of bodies) {
    assert.equal(canonicalOwnerAdminMutationBody(path, input).text, JSON.stringify(input));
    for (const forbidden of ['actor_principal_id', 'store_id', 'team_id', 'capabilities']) {
      assert.throws(
        () => canonicalOwnerAdminMutationBody(path, { ...input, [forbidden]: 'spoofed' }),
        OwnerGatewayError,
      );
    }
  }
  const sharedDelete = {
    schema: 'pulse.team.owner.shared_delete.v1', object_id: 'object_shared_1',
    idempotency_key: 'delete-shared-0001', approval_nonce: nonce,
  };
  assert.equal(canonicalOwnerSharedDeleteBody(sharedDelete).text, JSON.stringify(sharedDelete));
  assert.throws(
    () => canonicalOwnerSharedDeleteBody({ ...sharedDelete, principal_id: 'principal_spoof' }),
    OwnerGatewayError,
  );
});

test('Owner audit and deletion-status approvals bind exact query parameters and closed execution bodies', () => {
  const auditDigest = ownerAuditTargetDigest('', 10);
  assert.equal(auditDigest, '13ebff04a96bfd0acbc703b63ea1cd96e902713450fd44f6aaa86ecbf116808f');
  const auditApproval = {
    schema: 'pulse.team.owner.approval.v1', action: 'team.audit.inspect',
    store_id: 'store_test', team_id: 'team_test', target_kind: 'team_audit',
    target_id: 'team_test', target_digest: auditDigest, limit: 10,
  };
  assert.equal(canonicalOwnerApprovalBody(auditApproval).text, JSON.stringify(auditApproval));
  const auditBody = {
    schema: 'pulse.team.owner.audit.v1', approval_nonce: 'd'.repeat(64), limit: 10,
  };
  assert.equal(canonicalOwnerAuditBody(auditBody).text, JSON.stringify(auditBody));
  assert.throws(
    () => canonicalOwnerApprovalBody({ ...auditApproval, cursor: 'other-cursor' }),
    OwnerGatewayError,
  );

  const operationID = 'delete_operation_001';
  const statusDigest = ownerDeletionStatusTargetDigest(operationID);
  assert.equal(statusDigest, 'a9337ebca8cd0e71bd364a7a7a8a4a3b7c3da44abc9bf91d366e4ccb268195e0');
  const statusApproval = {
    schema: 'pulse.team.owner.approval.v1', action: 'team.deletion.status',
    store_id: 'store_test', team_id: 'team_test', target_kind: 'deletion_operation',
    target_id: operationID, target_digest: statusDigest, operation_id: operationID,
  };
  assert.equal(canonicalOwnerApprovalBody(statusApproval).text, JSON.stringify(statusApproval));
  const statusBody = {
    schema: 'pulse.team.owner.deletion_status.v1', approval_nonce: 'd'.repeat(64),
    operation_id: operationID,
  };
  assert.equal(canonicalOwnerDeletionStatusBody(statusBody).text, JSON.stringify(statusBody));
  assert.throws(
    () => canonicalOwnerDeletionStatusBody({ ...statusBody, team_id: 'team_spoof' }),
    OwnerGatewayError,
  );
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
    daemonBaseURL, signer, expectedOAuthIssuer: ownerIdentity().issuer,
    apiKey: () => 'ipc-secret', now: () => NOW,
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
    daemonBaseURL: 'http://127.0.0.1:18789', signer, expectedOAuthIssuer: ownerIdentity().issuer,
    apiKey: () => 'ipc-secret',
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
  await assert.rejects(
    gateway.call(
      OWNER_APPROVAL_PUBLIC_PATH, ownerIdentity(), 'owner-request-missing-action-proof',
      bootstrapApprovalInput(),
    ),
    (error: unknown) => error instanceof OwnerGatewayError && error.code === 'owner_step_up_required',
  );
  assert.equal(calls, 0);
});

test('Owner gateway rejects identity mutations outside the pinned OAuth issuer before IPC', async () => {
  const { signer } = signerFixture();
  let calls = 0;
  const gateway = new OwnerApprovalGateway({
    daemonBaseURL: 'http://127.0.0.1:18789',
    signer,
    expectedOAuthIssuer: ownerIdentity().issuer,
    apiKey: () => 'ipc-secret',
    now: () => NOW,
    fetch: async () => { calls++; return Response.json({}); },
  });
  const mutation = {
    issuer: 'https://other-issuer.example/', subject: 'member-subject', role: 'member',
  } as const;
  const target = ownerAdminMutationTarget('membership.create', mutation);
  await assert.rejects(gateway.call(
    OWNER_APPROVAL_PUBLIC_PATH,
    ownerIdentity(),
    'owner-request-wrong-issuer',
    {
      schema: 'pulse.team.owner.approval.v1', action: 'membership.create',
      store_id: 'store_test', team_id: 'team_test', target_kind: target.kind,
      target_id: target.id, target_digest: target.digest, mutation,
    },
    BROWSER_STEP_UP,
  ), OwnerGatewayError);
  assert.equal(calls, 0);
});

test('approval signs one exact action/body/path-bound assertion and forwards no bearer authority', async () => {
  const { signer, publicKey } = signerFixture();
  const input = bootstrapApprovalInput();
  const requests: Array<{ path: string; headers: Headers; body: string; redirect: RequestRedirect | undefined }> = [];
  const gateway = new OwnerApprovalGateway({
    daemonBaseURL: 'http://127.0.0.1:18789', signer, expectedOAuthIssuer: ownerIdentity().issuer,
    apiKey: () => 'ipc-secret', now: () => NOW,
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
    await gateway.call(
      OWNER_APPROVAL_PUBLIC_PATH, ownerIdentity(), 'owner-request-001', input, BROWSER_STEP_UP,
    ),
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
  assert.equal(verified.payload.jti, BROWSER_STEP_UP.assertionJTI);
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
    daemonBaseURL: 'http://127.0.0.1:18789', signer, expectedOAuthIssuer: ownerIdentity().issuer,
    apiKey: () => 'ipc-secret', now: () => NOW,
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

test('Owner membership approval and execution stay action-bound across public-to-loopback routing', async () => {
  const { signer, publicKey } = signerFixture();
  const mutation = { issuer: 'https://auth.example.com/', subject: 'dima', role: 'member' } as const;
  const target = ownerAdminMutationTarget('membership.create', mutation);
  const approval = {
    schema: 'pulse.team.owner.approval.v1', action: 'membership.create',
    store_id: 'store_test', team_id: 'team_test', target_kind: target.kind,
    target_id: target.id, target_digest: target.digest, mutation,
  };
  const execution = {
    schema: 'pulse.team.owner.members.v1', action: 'membership.create',
    approval_nonce: 'd'.repeat(64), ...mutation,
  };
  const requests: Array<{ path: string; headers: Headers; body: string }> = [];
  const gateway = new OwnerApprovalGateway({
    daemonBaseURL: 'http://127.0.0.1:18789', signer, expectedOAuthIssuer: ownerIdentity().issuer,
    apiKey: () => 'ipc-secret', now: () => NOW,
    fetch: async (request, init) => {
      const path = new URL(request.toString()).pathname;
      const headers = new Headers(init?.headers);
      const body = String(init?.body);
      requests.push({ path, headers, body });
      if (path === OWNER_APPROVAL_INTERNAL_PATH) {
        return Response.json({
          schema: 'pulse.team.owner.approval_result.v1', approval_nonce: 'd'.repeat(64),
          action: 'membership.create', store_id: 'store_test', team_id: 'team_test',
          target_kind: target.kind, target_id: target.id,
          expires_at: '2026-09-10T00:30:40.000Z', fallback: false,
        });
      }
      return Response.json({
        schema: 'pulse.team.owner.members_result.v1', action: 'membership.create',
        audit_event_id: 'audit_member_001', auth_epoch: 2, status: 'complete',
        member: {
          principal_id: 'principal_dima', membership_id: 'membership_dima',
          role: 'member', auth_epoch: 2,
        },
        fallback: false,
      });
    },
  });

  await gateway.call(
    OWNER_APPROVAL_PUBLIC_PATH, ownerIdentity(), 'owner-request-member-approval', approval, BROWSER_STEP_UP,
  );
  const result = await gateway.call(
    OWNER_MEMBERS_PUBLIC_PATH, ownerIdentity(), 'owner-request-member-execute', execution,
  );
  assert.equal((result as { action: string }).action, 'membership.create');
  assert.deepEqual(requests.map(({ path }) => path), [
    OWNER_APPROVAL_INTERNAL_PATH, OWNER_MEMBERS_INTERNAL_PATH,
  ]);
  assert.equal(requests[0].body, canonicalOwnerApprovalBody(approval).text);
  assert.equal(requests[1].body, canonicalOwnerAdminMutationBody(OWNER_MEMBERS_PUBLIC_PATH, execution).text);
  assert.equal(requests[1].headers.has('authorization'), false);
  assert.equal(requests[1].headers.has('x-pulse-owner-step-up'), false);
  const assertion = requests[0].headers.get('x-pulse-owner-step-up') ?? '';
  const verified = await jwtVerify(assertion, publicKey, {
    issuer: 'pulse-team-gateway', audience: PRINCIPAL_ASSERTION_AUDIENCE,
    algorithms: ['EdDSA'], currentDate: new Date(NOW * 1000),
  });
  assert.equal(verified.payload.action, 'membership.create');
  assert.equal(verified.payload.path, OWNER_APPROVAL_INTERNAL_PATH);
});

test('Owner shared deletion preserves the daemon deletion_in_progress contract', async () => {
  const { signer } = signerFixture();
  const gateway = new OwnerApprovalGateway({
    daemonBaseURL: 'http://127.0.0.1:18789', signer, expectedOAuthIssuer: ownerIdentity().issuer,
    apiKey: () => 'ipc-secret', now: () => NOW,
    fetch: async () => Response.json({
      schema: 'pulse.team.owner.shared_delete_result.v1',
      operation_id: 'delete_operation_001', object_id: 'object_shared_1',
      audit_event_id: 'audit_delete_001', status: 'deletion_in_progress',
      replayed: false, fallback: false,
    }),
  });
  const result = await gateway.call(
    OWNER_SHARED_DELETE_PUBLIC_PATH, ownerIdentity(), 'owner-request-shared-delete-001', {
      schema: 'pulse.team.owner.shared_delete.v1', object_id: 'object_shared_1',
      idempotency_key: 'delete-shared-0001', approval_nonce: 'd'.repeat(64),
    },
  );
  assert.equal((result as { status: string }).status, 'deletion_in_progress');
});

test('Owner audit and deletion status proxy only exact metadata-only daemon responses', async () => {
  const { signer } = signerFixture();
  const requests: string[] = [];
  const gateway = new OwnerApprovalGateway({
    daemonBaseURL: 'http://127.0.0.1:18789', signer, expectedOAuthIssuer: ownerIdentity().issuer,
    apiKey: () => 'ipc-secret', now: () => NOW,
    fetch: async (request) => {
      const path = new URL(request.toString()).pathname;
      requests.push(path);
      if (path === OWNER_AUDIT_INTERNAL_PATH) {
        return Response.json({
          schema: 'pulse.team.owner.audit_result.v1',
          events: [{
            event_id: 'audit_event_001', occurred_at: '2026-09-10T00:26:40Z',
            action: 'membership.create', outcome: 'allowed', actor_principal_id: 'principal_owner',
            client_key: 'a'.repeat(64), team_id: 'team_test', project_id: null,
            target_kind: 'membership', target_id: 'principal_dima', request_id: 'owner-request-001',
            policy_version: 1, mode: 'team-remote', reason_code: 'approved',
          }],
          own_actions_only: false, fallback: false,
        });
      }
      return Response.json({
        schema: 'pulse.team.owner.deletion_status_result.v1',
        operation_id: 'delete_operation_001', object_id: 'object_shared_1',
        audit_event_id: 'audit_delete_001', status: 'deletion_in_progress', attempts: 0,
        fallback: false,
      });
    },
  });
  const audit = await gateway.call(OWNER_AUDIT_PUBLIC_PATH, ownerIdentity(), 'owner-request-audit-001', {
    schema: 'pulse.team.owner.audit.v1', approval_nonce: 'd'.repeat(64), limit: 10,
  });
  assert.equal((audit as { own_actions_only: boolean }).own_actions_only, false);
  const status = await gateway.call(
    OWNER_DELETION_STATUS_PUBLIC_PATH, ownerIdentity(), 'owner-request-status-001', {
      schema: 'pulse.team.owner.deletion_status.v1', approval_nonce: 'd'.repeat(64),
      operation_id: 'delete_operation_001',
    },
  );
  assert.equal((status as { status: string }).status, 'deletion_in_progress');
  assert.deepEqual(requests, [OWNER_AUDIT_INTERNAL_PATH, OWNER_DELETION_STATUS_INTERNAL_PATH]);
});

test('Owner gateway rejects malformed daemon success and collapses denial details without retry', async () => {
  const { signer } = signerFixture();
  let calls = 0;
  const denied = new OwnerApprovalGateway({
    daemonBaseURL: 'http://127.0.0.1:18789', signer, expectedOAuthIssuer: ownerIdentity().issuer,
    apiKey: () => 'ipc-secret', now: () => NOW,
    fetch: async () => {
      calls++;
      return Response.json({ error: 'raw-owner-subject-and-token', fallback: false }, { status: 403 });
    },
  });
  await assert.rejects(
    denied.call(
      OWNER_APPROVAL_PUBLIC_PATH, ownerIdentity(), 'owner-request-denied',
      bootstrapApprovalInput(), BROWSER_STEP_UP,
    ),
    (error: unknown) => error instanceof OwnerGatewayError &&
      error.code === 'owner_operation_denied' && !/raw|token|subject/i.test(error.message),
  );
  assert.equal(calls, 1);

  const malformed = new OwnerApprovalGateway({
    daemonBaseURL: 'http://127.0.0.1:18789', signer, expectedOAuthIssuer: ownerIdentity().issuer,
    apiKey: () => 'ipc-secret', now: () => NOW,
    fetch: async () => Response.json({ ...approvalResult(bootstrapApprovalInput()), debug_path: '/private/db' }),
  });
  await assert.rejects(
    malformed.call(
      OWNER_APPROVAL_PUBLIC_PATH, ownerIdentity(), 'owner-request-malformed',
      bootstrapApprovalInput(), BROWSER_STEP_UP,
    ),
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
