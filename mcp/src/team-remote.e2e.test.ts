import assert from 'node:assert/strict';
import {
  createHash,
  createPublicKey,
  generateKeyPairSync,
} from 'node:crypto';
import {
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { test } from 'node:test';

import { exportJWK, jwtVerify, SignJWT } from 'jose';

import {
  OWNER_ACTIVATE_PUBLIC_PATH,
  OWNER_APPROVAL_PUBLIC_PATH,
  OwnerApprovalGateway,
  ownerActivationTargetDigest,
} from './owner-approval.js';
import {
  OAuthResourceVerifier,
  type VerifiedOAuthIdentity,
} from './oauth-resource.js';
import {
  loadPrincipalSigner,
  PRINCIPAL_ASSERTION_AUDIENCE,
  PRINCIPAL_ASSERTION_ISSUER,
  PrincipalCheckError,
  TeamPrincipalClient,
  TeamRequestSecurity,
  type TeamPrincipalContext,
} from './principal-context.js';
import {
  requiredTeamCapabilities,
  TeamDomainError,
  type TeamCapability,
} from './team-contracts.js';

const NOW = 1_789_000_000;
const ISSUER = 'https://synthetic-idp.example/';
const RESOURCE = 'https://synthetic-pulse.example/mcp';
const STORE_ID = 'store_synthetic_e2e';
const TEAM_ID = 'team_synthetic_e2e';
const IPC_KEY = 'synthetic-ipc-key-never-logged';

interface Binding {
  clientId: string;
  humanPrincipalId: string;
  membershipId: string;
  principalId: string;
  bindingId: string;
  allowed: readonly TeamCapability[];
  revoked: boolean;
}

interface StoredObject {
  objectId: string;
  auditEventId: string;
  capsuleIds: string[];
  ownerHumanPrincipalId: string;
  summary: string;
  tombstoned: boolean;
}

interface IdempotentResult {
  digest: string;
  result: Record<string, unknown>;
}

interface DeletionOperation {
  operationId: string;
  objectId: string;
  auditEventId: string;
  ownerHumanPrincipalId: string;
  completedAt: string;
}

function sha256(value: string | Buffer): string {
  return createHash('sha256').update(value).digest('hex');
}

function json(value: unknown, status = 200): Response {
  return Response.json(value, { status });
}

function rememberInput(summary = 'Synthetic personal decision for the isolated team proof.') {
  return {
    schema: 'pulse.team.memory.v1',
    source: {
      host: 'codex',
      conversation_scope: 'current_turn',
      timestamp: '2026-09-10T00:26:40.000Z',
    },
    items: [{
      kind: 'decision',
      redacted_summary: summary,
      confidence: 1,
      evidence_hint: 'user_confirmed',
      tags: ['synthetic-e2e'],
    }],
    raw_input_included: false,
    active_context: {
      project_id: 'project-synthetic',
      session_id: 'session-synthetic',
    },
    privacy_tier: 'normal',
    retention: 'project',
    idempotency_key: 'remember-synthetic-001',
  };
}

function recallInput() {
  return {
    schema: 'pulse.team.recall.v1',
    query: 'What synthetic decision was recorded?',
    active_context: {
      project_id: 'project-synthetic',
      session_id: 'session-synthetic',
    },
    privacy_ceiling: 'sensitive',
    limit: 10,
  };
}

function deletionInput(objectId: string) {
  return {
    schema: 'pulse.team.delete.v1',
    object_id: objectId,
    active_context: {
      project_id: 'project-synthetic',
      session_id: 'session-synthetic',
    },
    idempotency_key: 'delete-synthetic-001',
  };
}

function deletionStatusInput(operationId: string) {
  return {
    schema: 'pulse.team.delete_status.v1',
    operation_id: operationId,
    active_context: {
      project_id: 'project-synthetic',
      session_id: 'session-synthetic',
    },
  };
}

function isTeamCapability(value: unknown): value is TeamCapability {
  return typeof value === 'string' && [
    'pulse:connect', 'pulse:status', 'pulse:read', 'pulse:write',
    'pulse:audit', 'pulse:delete', 'pulse:owner',
  ].includes(value);
}

test('synthetic team-remote proof composes OAuth, principal binding, scoped domain, deletion, and activation seams', async () => {
  const dataDir = mkdtempSync(join(tmpdir(), 'pulse-team-remote-e2e-'));
  const databasePath = join(dataDir, 'synthetic-team-database.json');
  assert.ok(resolve(databasePath).startsWith(`${resolve(tmpdir())}/`));

  const { privateKey: issuerPrivate, publicKey: issuerPublic } =
    generateKeyPairSync('rsa', { modulusLength: 2048 });
  const issuerJWK = await exportJWK(issuerPublic);
  Object.assign(issuerJWK, { kid: 'synthetic-issuer-key', alg: 'RS256', use: 'sig' });

  const { privateKey: gatewayPrivate } = generateKeyPairSync('ed25519');
  const gatewayPrivatePath = join(dataDir, 'gateway.pk8.pem');
  writeFileSync(
    gatewayPrivatePath,
    gatewayPrivate.export({ format: 'pem', type: 'pkcs8' }),
    { mode: 0o600 },
  );
  const gatewayPublic = createPublicKey(gatewayPrivate);
  const gatewayJWK = gatewayPublic.export({ format: 'jwk' });
  assert.equal(typeof gatewayJWK.x, 'string');
  const keyringPath = join(dataDir, 'gateway-keyring.json');
  writeFileSync(keyringPath, JSON.stringify({
    active: { kid: 'synthetic-gateway-key', public_key: gatewayJWK.x },
    previous: [],
  }), { mode: 0o600 });

  const signer = loadPrincipalSigner({
    privateKeyFile: gatewayPrivatePath,
    keyId: 'synthetic-gateway-key',
    verifyKeyringFile: keyringPath,
    storeId: STORE_ID,
    teamId: TEAM_ID,
    now: () => NOW,
  });

  const bindings = new Map<string, Binding>();
  const objects = new Map<string, StoredObject>();
  const idempotency = new Map<string, IdempotentResult>();
  const deletions = new Map<string, DeletionOperation>();
  const ownerNonces = new Set<string>();
  let objectSequence = 0;
  let deletionSequence = 0;
  let daemonOutage = false;
  let activationState: 'inactive' | 'active' = 'inactive';
  const contentBoundary = 'synthetic' as const;

  const bindingKey = (subject: string, clientId: string) => `${subject}\u0000${clientId}`;
  const register = (
    subject: string,
    clientId: string,
    allowed: readonly TeamCapability[],
  ): Binding => {
    const suffix = sha256(`${subject}:${clientId}`).slice(0, 16);
    const binding: Binding = {
      clientId,
      humanPrincipalId: `human_${sha256(subject).slice(0, 16)}`,
      membershipId: `membership_${sha256(subject).slice(0, 16)}`,
      principalId: `principal_${suffix}`,
      bindingId: `binding_${suffix}`,
      allowed,
      revoked: false,
    };
    bindings.set(bindingKey(subject, clientId), binding);
    return binding;
  };

  const writerBinding = register('human-subject-a', 'agent-client-writer', [
    'pulse:connect', 'pulse:read', 'pulse:write', 'pulse:delete',
  ]);
  const readerBinding = register('human-subject-a', 'agent-client-reader', [
    'pulse:connect', 'pulse:read', 'pulse:delete',
  ]);
  register('human-subject-b', 'agent-client-outsider', [
    'pulse:connect', 'pulse:read',
  ]);

  const persist = () => {
    writeFileSync(databasePath, JSON.stringify({
      store_id: STORE_ID,
      team_id: TEAM_ID,
      activation_state: activationState,
      content_boundary: contentBoundary,
      bindings: [...bindings.values()].map(({ allowed, ...binding }) => ({
        ...binding,
        allowed: [...allowed],
      })),
      objects: [...objects.values()],
      deletion_operations: [...deletions.values()],
    }, null, 2), { mode: 0o600 });
  };
  persist();

  const verifyGatewayAssertion = async (
    headers: Headers,
    path: string,
    bodyText: string,
  ) => {
    assert.equal(headers.get('x-pulse-key'), IPC_KEY);
    assert.equal(headers.has('authorization'), false);
    const assertion = headers.get('x-pulse-principal');
    assert.ok(assertion);
    const verified = await jwtVerify(assertion, gatewayPublic, {
      issuer: PRINCIPAL_ASSERTION_ISSUER,
      audience: PRINCIPAL_ASSERTION_AUDIENCE,
      algorithms: ['EdDSA'],
      currentDate: new Date(NOW * 1000),
    });
    assert.equal(verified.payload.path, path);
    assert.equal(verified.payload.body_sha256, sha256(bodyText));
    assert.equal(verified.payload.request_id, headers.get('x-pulse-request-id'));
    return verified.payload;
  };

  const daemonFetch: typeof fetch = async (input, init) => {
    const url = new URL(input.toString());
    const path = url.pathname;
    const headers = new Headers(init?.headers);
    const bodyText = typeof init?.body === 'string' ? init.body : '';
    if (daemonOutage && path.startsWith('/team/v1/')) {
      throw new Error('synthetic daemon outage');
    }

    if (path === '/team/v1/owner/approval') {
      assert.equal(headers.get('x-pulse-key'), IPC_KEY);
      const stepUp = headers.get('x-pulse-owner-step-up');
      assert.ok(stepUp);
      const verified = await jwtVerify(stepUp, gatewayPublic, {
        issuer: PRINCIPAL_ASSERTION_ISSUER,
        audience: PRINCIPAL_ASSERTION_AUDIENCE,
        algorithms: ['EdDSA'],
        currentDate: new Date(NOW * 1000),
      });
      const body = JSON.parse(bodyText) as Record<string, unknown>;
      assert.equal(verified.payload.path, path);
      assert.equal(verified.payload.action, body.action);
      assert.equal(verified.payload.auth_time, NOW - 30);
      assert.equal(verified.payload.body_sha256, sha256(bodyText));
      const nonce = sha256(`approval:${String(body.target_digest)}`);
      ownerNonces.add(nonce);
      return json({
        schema: 'pulse.team.owner.approval_result.v1',
        approval_nonce: nonce,
        action: body.action,
        store_id: body.store_id,
        team_id: body.team_id,
        target_kind: body.target_kind,
        target_id: body.target_id,
        expires_at: new Date((NOW + 120) * 1000).toISOString(),
        fallback: false,
      });
    }

    if (path === '/team/v1/owner/activate') {
      assert.equal(headers.get('x-pulse-key'), IPC_KEY);
      assert.equal(headers.has('x-pulse-owner-step-up'), false);
      const body = JSON.parse(bodyText) as Record<string, unknown>;
      const nonce = String(body.approval_nonce);
      if (!ownerNonces.delete(nonce)) {
        return json({ error: 'owner_approval_invalid', fallback: false }, 403);
      }
      activationState = 'active';
      persist();
      return json({
        schema: 'pulse.team.owner.activate_result.v1',
        store_id: STORE_ID,
        team_id: TEAM_ID,
        activation_state: 'active',
        content_boundary: contentBoundary,
        public_enabled: true,
        gate_digest: body.gate_digest,
        activated_by_principal_id: 'principal_synthetic_owner',
        audit_event_id: 'audit_synthetic_activation',
        activated_at: new Date(NOW * 1000).toISOString(),
        fallback: false,
      });
    }

    const assertion = await verifyGatewayAssertion(headers, path, bodyText);
    const subject = String(assertion.oauth_subject);
    const clientId = String(assertion.oauth_client_id);
    const binding = bindings.get(bindingKey(subject, clientId));
    if (!binding) return json({ error: 'principal_revoked' }, 403);

    if (path === '/team/v1/principal/check') {
      const body = JSON.parse(bodyText) as { capabilities?: unknown[] };
      const requested = (body.capabilities ?? []).filter(isTeamCapability);
      if (
        binding.revoked || requested.length !== (body.capabilities ?? []).length ||
        requested.some((capability) => !binding.allowed.includes(capability))
      ) {
        return json({ error: 'principal_revoked' }, 403);
      }
      const context: TeamPrincipalContext = {
        version: 'pulse.team.principal_context.v1',
        request_id: headers.get('x-pulse-request-id') ?? '',
        store_id: STORE_ID,
        team_id: TEAM_ID,
        principal_id: binding.principalId,
        principal_kind: 'agent',
        oauth_client_key: sha256(clientId),
        human_principal_id: binding.humanPrincipalId,
        agent_binding_id: binding.bindingId,
        membership_id: binding.membershipId,
        membership_role: 'member',
        team_auth_epoch: 1,
        principal_auth_epoch: 1,
        binding_auth_epoch: binding.revoked ? 2 : 1,
        membership_auth_epoch: 1,
        capabilities: requested,
      };
      return json(context);
    }

    if (binding.revoked) return json({ error: 'principal_revoked', fallback: false }, 403);

    if (path === '/team/v1/memory/remember') {
      const body = JSON.parse(bodyText) as ReturnType<typeof rememberInput>;
      const digest = sha256(bodyText);
      const key = `${binding.principalId}\u0000${body.idempotency_key}`;
      const previous = idempotency.get(key);
      if (previous) {
        if (previous.digest !== digest) {
          return json({ error: 'idempotency_conflict', fallback: false }, 409);
        }
        return json({ ...previous.result, replayed: true });
      }
      objectSequence++;
      const objectId = `team_root_synthetic_${objectSequence}`;
      const stored: StoredObject = {
        objectId,
        auditEventId: `audit_synthetic_${objectSequence}`,
        capsuleIds: [`capsule_synthetic_${objectSequence}`],
        ownerHumanPrincipalId: binding.humanPrincipalId,
        summary: body.items[0]?.redacted_summary ?? '',
        tombstoned: false,
      };
      objects.set(objectId, stored);
      const result = {
        schema: 'pulse.team.memory_result.v1',
        object_id: objectId,
        audit_event_id: stored.auditEventId,
        capsule_ids: stored.capsuleIds,
        status: 'stored',
        projection_state: 'pending',
        projection_jobs: [
          { kind: 'embedding', job_id: `job_embedding_${objectSequence}`, state: 'pending' },
          { kind: 'event', job_id: `job_event_${objectSequence}`, state: 'pending' },
        ],
        fully_projected: false,
        replayed: false,
        fallback: false,
      };
      idempotency.set(key, { digest, result });
      persist();
      return json(result);
    }

    if (path === '/team/v1/recall') {
      const visible = [...objects.values()].filter((object) =>
        !object.tombstoned && object.ownerHumanPrincipalId === binding.humanPrincipalId,
      );
      return json({
        schema: 'pulse.team.recall_result.v1',
        items: visible.map((object) => ({
          object_id: object.objectId,
          kind: 'decision',
          redacted_summary: object.summary,
          confidence: 1,
          privacy_tier: 'normal',
          retention: 'project',
          tags: ['synthetic-e2e'],
        })),
        returned_count: visible.length,
        fallback: false,
      });
    }

    if (path === '/team/v1/delete') {
      const body = JSON.parse(bodyText) as ReturnType<typeof deletionInput>;
      const object = objects.get(body.object_id);
      if (!object || object.ownerHumanPrincipalId !== binding.humanPrincipalId) {
        return json({ error: 'concealed_not_found', fallback: false }, 404);
      }
      object.tombstoned = true;
      deletionSequence++;
      const operation: DeletionOperation = {
        operationId: `delete_operation_synthetic_${deletionSequence}`,
        objectId: object.objectId,
        auditEventId: `audit_delete_synthetic_${deletionSequence}`,
        ownerHumanPrincipalId: binding.humanPrincipalId,
        completedAt: new Date(NOW * 1000).toISOString(),
      };
      deletions.set(operation.operationId, operation);
      persist();
      return json({
        schema: 'pulse.team.delete_result.v1',
        operation_id: operation.operationId,
        object_id: operation.objectId,
        audit_event_id: operation.auditEventId,
        status: 'deletion_in_progress',
        replayed: false,
        fallback: false,
      });
    }

    if (path === '/team/v1/delete/status') {
      const body = JSON.parse(bodyText) as ReturnType<typeof deletionStatusInput>;
      const operation = deletions.get(body.operation_id);
      if (!operation || operation.ownerHumanPrincipalId !== binding.humanPrincipalId) {
        return json({ error: 'concealed_not_found', fallback: false }, 404);
      }
      return json({
        schema: 'pulse.team.delete_status_result.v1',
        operation_id: operation.operationId,
        object_id: operation.objectId,
        audit_event_id: operation.auditEventId,
        status: 'complete',
        attempts: 1,
        completed_at: operation.completedAt,
        fallback: false,
      });
    }

    return json({ error: 'not_found', fallback: false }, 404);
  };

  const verifier = new OAuthResourceVerifier({
    issuer: ISSUER,
    resource: RESOURCE,
    now: () => NOW,
    resolveHost: async () => ['203.0.113.10'],
    fetch: async (input) => input.toString().endsWith('/jwks')
      ? json({ keys: [issuerJWK] })
      : json({
          issuer: ISSUER,
          jwks_uri: `${ISSUER}jwks`,
          code_challenge_methods_supported: ['S256'],
        }),
  });
  const principalClient = new TeamPrincipalClient({
    daemonBaseURL: 'http://127.0.0.1:18789',
    signer,
    apiKey: () => IPC_KEY,
    fetch: daemonFetch,
  });
  const security = new TeamRequestSecurity({ verifier, principalClient });
  const ownerGateway = new OwnerApprovalGateway({
    daemonBaseURL: 'http://127.0.0.1:18789',
    signer,
    expectedOAuthIssuer: ISSUER,
    apiKey: () => IPC_KEY,
    fetch: daemonFetch,
    now: () => NOW,
  });

  let tokenSequence = 0;
  const token = (
    subject: string,
    clientId: string,
    capabilities: readonly TeamCapability[],
    authTime?: number,
  ) => new SignJWT({
    iss: ISSUER,
    sub: subject,
    aud: RESOURCE,
    iat: NOW,
    exp: NOW + 300,
    jti: `synthetic-token-${++tokenSequence}`,
    client_id: clientId,
    scope: capabilities.join(' '),
    ...(authTime === undefined ? {} : { auth_time: authTime }),
  })
    .setProtectedHeader({ alg: 'RS256', kid: 'synthetic-issuer-key', typ: 'at+jwt' })
    .sign(issuerPrivate);

  const bind = async (
    bearer: string,
    requestId: string,
    toolName: string,
  ): Promise<{
    identity: VerifiedOAuthIdentity;
    context: Readonly<TeamPrincipalContext>;
    domain: ReturnType<TeamPrincipalClient['bindDomain']>;
  }> => {
    const authorization = `Bearer ${bearer}`;
    const identity = await security.authenticateBeforeBody(authorization);
    const context = await security.resolveAfterBody({
      baseline: identity,
      requiredCapabilities: requiredTeamCapabilities({ method: 'tools/call', params: { name: toolName } }),
      requestId,
    });
    return { identity, context, domain: principalClient.bindDomain(identity, context) };
  };

  try {
    const writerToken = await token(
      'human-subject-a', 'agent-client-writer', writerBinding.allowed,
    );
    const readerToken = await token(
      'human-subject-a', 'agent-client-reader', readerBinding.allowed,
    );
    const outsiderToken = await token(
      'human-subject-b', 'agent-client-outsider', ['pulse:connect', 'pulse:read'],
    );

    const writer = await bind(writerToken, 'request-writer-001', 'pulse_team_remember');
    const reader = await bind(readerToken, 'request-reader-001', 'pulse_team_recall');
    assert.notEqual(writer.context.agent_binding_id, reader.context.agent_binding_id);
    assert.equal(writer.context.human_principal_id, reader.context.human_principal_id);
    assert.deepEqual(writer.context.capabilities, [...writerBinding.allowed].sort());
    assert.deepEqual(reader.context.capabilities, [...readerBinding.allowed].sort());

    const first = await writer.domain.remember(rememberInput());
    const replay = await writer.domain.remember(rememberInput());
    assert.equal(replay.object_id, first.object_id);
    assert.equal(replay.audit_event_id, first.audit_event_id);
    assert.equal(replay.replayed, true);
    assert.equal(objects.size, 1);
    await assert.rejects(
      writer.domain.remember(rememberInput('Conflicting body under the same idempotency key.')),
      (error: unknown) => error instanceof TeamDomainError && error.code === 'idempotency_conflict',
    );
    assert.equal(objects.size, 1);

    const sameHumanRead = await reader.domain.recall(recallInput());
    assert.deepEqual(sameHumanRead.items.map((item) => item.object_id), [first.object_id]);
    const outsider = await bind(outsiderToken, 'request-outsider-001', 'pulse_team_recall');
    const isolatedRead = await outsider.domain.recall(recallInput());
    assert.equal(isolatedRead.returned_count, 0);
    assert.deepEqual(isolatedRead.items, []);

    writerBinding.revoked = true;
    persist();
    const mutationCountBeforeRevokedRequests = objects.size;
    for (const toolName of ['pulse_team_recall', 'pulse_team_remember']) {
      await assert.rejects(
        bind(writerToken, `request-revoked-${toolName}`, toolName),
        (error: unknown) => error instanceof PrincipalCheckError && error.code === 'principal_revoked',
      );
    }
    assert.equal(objects.size, mutationCountBeforeRevokedRequests);

    const deletion = await reader.domain.delete(deletionInput(first.object_id));
    assert.equal(deletion.status, 'deletion_in_progress');
    const hiddenImmediately = await reader.domain.recall(recallInput());
    assert.equal(hiddenImmediately.returned_count, 0);
    const deletionStatus = await reader.domain.deleteStatus(
      deletionStatusInput(deletion.operation_id),
    );
    assert.equal(deletionStatus.status, 'complete');

    const beforeOutage = readFileSync(databasePath, 'utf8');
    daemonOutage = true;
    await assert.rejects(
      reader.domain.recall(recallInput()),
      (error: unknown) => error instanceof TeamDomainError &&
        error.code === 'shared_memory_unavailable',
    );
    assert.equal(readFileSync(databasePath, 'utf8'), beforeOutage);
    daemonOutage = false;

    const ownerToken = await token(
      'human-subject-owner',
      'owner-browser-client',
      ['pulse:connect', 'pulse:owner'],
      NOW - 30,
    );
    const ownerIdentity = await verifier.verifyAuthorization(
      `Bearer ${ownerToken}`,
      ['pulse:owner'],
    );
    const gateDigest = sha256('synthetic AE1-AE12 evidence manifest');
    const approval = await ownerGateway.call(
      OWNER_APPROVAL_PUBLIC_PATH,
      ownerIdentity,
      'owner-request-approval',
      {
        schema: 'pulse.team.owner.approval.v1',
        action: 'team.activation.synthetic',
        store_id: STORE_ID,
        team_id: TEAM_ID,
        target_kind: 'team_activation',
        target_id: TEAM_ID,
        target_digest: ownerActivationTargetDigest(STORE_ID, TEAM_ID, gateDigest),
        gate_digest: gateDigest,
      },
      { assertionJTI: `owner_browser_${'e'.repeat(43)}` },
    ) as { approval_nonce: string };
    const activation = await ownerGateway.call(
      OWNER_ACTIVATE_PUBLIC_PATH,
      ownerIdentity,
      'owner-request-activation',
      {
        schema: 'pulse.team.owner.activate.v1',
        approval_nonce: approval.approval_nonce,
        gate_digest: gateDigest,
      },
    ) as {
      activation_state: string;
      content_boundary: string;
      public_enabled: boolean;
      fallback: boolean;
    };
    assert.deepEqual(activation, {
      ...activation,
      activation_state: 'active',
      content_boundary: 'synthetic',
      public_enabled: true,
      fallback: false,
    });
    assert.match(readFileSync(databasePath, 'utf8'), /"content_boundary": "synthetic"/);
    assert.doesNotMatch(readFileSync(databasePath, 'utf8'), /"content_boundary": "real"/);
  } finally {
    rmSync(dataDir, { recursive: true, force: true });
  }
});
