#!/usr/bin/env node

import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import {
  createHash,
  createPublicKey,
  generateKeyPairSync,
} from 'node:crypto';
import { once } from 'node:events';
import { createServer as createHttpServer } from 'node:http';
import {
  chmodSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';

import { exportJWK, SignJWT } from 'jose';

import {
  MemoryCredentialStore,
  buildSenderConstrainedRemoteHeaders,
  createInstallationKeyRecord,
  persistRemoteCredential,
} from '../../pulse-app/cli/src/remote-auth.js';
import {
  buildTeamOwnerOperation,
  buildTeamOwnerStepUp,
  createTeamOwnerRemotePost,
  ownerCredentialRef,
} from '../../pulse-app/cli/src/team-owner-client.js';
import { authorizeTeamOwnerPublicOperation } from '../dist/index.js';
import {
  AIRLOCK_HUMAN_PRESENCE_ACR,
  AIRLOCK_HUMAN_PRESENCE_CLAIM,
  AIRLOCK_HUMAN_PRESENCE_SCHEMA,
  AIRLOCK_PLATFORM_WEBAUTHN_FACTOR,
  OAuthResourceVerifier,
} from '../dist/oauth-resource.js';
import {
  InstallationEnrollmentRegistry,
  InstallationProofVerifier,
  SenderConstrainedOAuthVerifier,
} from '../dist/sender-constrained-auth.js';

import {
  OWNER_ACTIVATE_PUBLIC_PATH,
  OWNER_APPROVAL_PUBLIC_PATH,
  OWNER_AUDIT_PUBLIC_PATH,
  OWNER_BINDINGS_PUBLIC_PATH,
  OWNER_BOOTSTRAP_PUBLIC_PATH,
  OWNER_DELETION_STATUS_PUBLIC_PATH,
  OWNER_MEMBERS_PUBLIC_PATH,
  OWNER_PROJECT_GRANTS_PUBLIC_PATH,
  OWNER_PROJECTS_PUBLIC_PATH,
  OwnerApprovalGateway,
  ownerAdminMutationTarget,
  ownerActivationTargetDigest,
  ownerAuditTargetDigest,
  ownerBootstrapTargetDigest,
  ownerDeletionStatusTargetDigest,
  ownerOperationStepUpNonce,
} from '../dist/owner-approval.js';
import {
  TeamPrincipalClient,
  loadPrincipalSigner,
} from '../dist/principal-context.js';

const scriptDir = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(scriptDir, '..', '..');
const pulseAppDir = join(repoRoot, 'pulse-app');

const OWNER_ISSUER = 'https://synthetic-owner.invalid/';
const OWNER_SUBJECT = 'synthetic-owner';
const OWNER_CLIENT_ID = 'synthetic-owner-browser';
const OWNER_ENROLLMENT_ID = 'enrollment_synthetic_owner';
const PUBLIC_BASE_URL = 'https://pulse.synthetic.invalid';
const OWNER_RESOURCE = `${PUBLIC_BASE_URL}/mcp`;
const MEMBER_ISSUER = OWNER_ISSUER;
const MEMBER_SUBJECT = 'synthetic-member';
const MEMBER_CLIENT_ID = 'synthetic-member-codex';
const KEY_ID = 'real-acceptance-gateway';

const OWNER_BOOTSTRAP_INTERNAL_PATH = '/team/v1/owner/bootstrap';

const ACTION_MEMBERSHIP_CREATE = 'membership.create';
const ACTION_MEMBERSHIP_REVOKE = 'membership.revoke';
const ACTION_BINDING_CREATE = 'agent_binding.create';
const ACTION_PROJECT_CREATE = 'project.create';
const ACTION_PROJECT_GRANT_CREATE = 'project_grant.create';

const SCHEMA_MEMBERS = 'pulse.team.owner.members.v1';
const SCHEMA_BINDINGS = 'pulse.team.owner.bindings.v1';
const SCHEMA_PROJECTS = 'pulse.team.owner.projects.v1';
const SCHEMA_PROJECT_GRANTS = 'pulse.team.owner.project_grants.v1';

function sha256(value) {
  return createHash('sha256').update(value).digest('hex');
}

function syntheticStepUp(label) {
  return { assertionJTI: `owner_browser_${createHash('sha256').update(label).digest('base64url')}` };
}

function fail(message, child) {
  const output = child?.output?.trim();
  throw new Error(output ? `${message}\n${output}` : message);
}

async function acceptanceStep(label, operation) {
  try {
    return await operation();
  } catch (error) {
    throw new Error(`${label}: ${error instanceof Error ? error.message : String(error)}`);
  }
}

async function unusedLoopbackPort() {
  const server = createServer();
  await new Promise((resolveListen, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolveListen);
  });
  const address = server.address();
  assert.ok(address && typeof address === 'object');
  const port = address.port;
  await new Promise((resolveClose, reject) => server.close((error) => (
    error ? reject(error) : resolveClose()
  )));
  return port;
}

function daemonEnvironment({ homeDir, keyringPath, ownerOnly, storeId = '', teamId = '' }) {
  const clean = Object.fromEntries(Object.entries(process.env).filter(([name]) => (
    !name.startsWith('PULSE_') && name !== 'ANTHROPIC_API_KEY' && name !== 'COHERE_API_KEY'
  )));
  return {
    ...clean,
    HOME: homeDir,
    PULSE_RUNTIME_MODE: 'team-remote',
    PULSE_TEAM_OWNER_ADMIN_ONLY: ownerOnly ? '1' : '',
    PULSE_TEAM_BOOTSTRAP_ISSUER: OWNER_ISSUER,
    PULSE_TEAM_BOOTSTRAP_SUBJECT: OWNER_SUBJECT,
    PULSE_TEAM_BOOTSTRAP_ADMIN_CLIENT_ID: OWNER_CLIENT_ID,
    PULSE_TEAM_EXPECTED_STORE_ID: storeId,
    PULSE_TEAM_EXPECTED_TEAM_ID: teamId,
    PULSE_TEAM_PRINCIPAL_VERIFY_KEYRING_FILE: keyringPath,
  };
}

async function startDaemon(options) {
  const port = await unusedLoopbackPort();
  const child = spawn(options.binaryPath, [
    '-addr', `127.0.0.1:${port}`,
    '-data-dir', options.dataDir,
  ], {
    cwd: pulseAppDir,
    env: daemonEnvironment({ ...options, ownerOnly: options.ownerOnly }),
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  child.output = '';
  const capture = (chunk) => {
    child.output = `${child.output}${chunk.toString()}`.slice(-16_384);
  };
  child.stdout.on('data', capture);
  child.stderr.on('data', capture);

  const secretPath = join(options.dataDir, 'secret.key');
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) fail(`Team daemon exited during startup (${child.exitCode})`, child);
    if (existsSync(secretPath)) {
      const apiKey = readFileSync(secretPath, 'utf8');
      if (/^[0-9a-f]{64}$/.test(apiKey)) {
        const baseURL = `http://127.0.0.1:${port}`;
        try {
          const response = await fetch(baseURL, {
            redirect: 'error',
            signal: AbortSignal.timeout(250),
            headers: { 'X-Pulse-Key': apiKey },
          });
          await response.body?.cancel();
          return { child, apiKey, baseURL };
        } catch {
          // The credential is created before the listener is bound. Keep polling.
        }
      }
    }
    await delay(25);
  }
  await stopDaemon({ child });
  fail('Team daemon did not create its strict IPC credential', child);
}

async function stopDaemon(runtime) {
  if (!runtime?.child || runtime.child.exitCode !== null) return;
  runtime.child.kill('SIGTERM');
  const exited = once(runtime.child, 'exit');
  const timedOut = delay(12_000).then(() => 'timeout');
  if (await Promise.race([exited, timedOut]) === 'timeout') {
    runtime.child.kill('SIGKILL');
    await once(runtime.child, 'exit');
  }
}

async function requestJSON(runtime, path, body, extraHeaders = {}) {
  const response = await fetch(`${runtime.baseURL}${path}`, {
    method: body === undefined ? 'GET' : 'POST',
    redirect: 'error',
    signal: AbortSignal.timeout(5_000),
    headers: {
      'X-Pulse-Key': runtime.apiKey,
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      ...extraHeaders,
    },
    ...(body === undefined ? {} : { body: typeof body === 'string' ? body : JSON.stringify(body) }),
  });
  const text = await response.text();
  let value;
  try {
    value = JSON.parse(text);
  } catch {
    throw new Error(`Non-JSON response from ${path} (${response.status})`);
  }
  return { status: response.status, value };
}

async function waitForRequest(runtime, path, body, expectedStatus = 200) {
  const deadline = Date.now() + 20_000;
  let lastError;
  while (Date.now() < deadline) {
    if (runtime.child.exitCode !== null) fail(`Team daemon exited before ${path} became ready`, runtime.child);
    try {
      const result = await requestJSON(runtime, path, body);
      if (result.status === expectedStatus) return result.value;
      lastError = new Error(`${path} returned ${result.status}: ${JSON.stringify(result.value)}`);
    } catch (error) {
      lastError = error;
    }
    await delay(50);
  }
  throw lastError ?? new Error(`${path} did not become ready`);
}

function ownerIdentity() {
  return {
    issuer: OWNER_ISSUER,
    subject: OWNER_SUBJECT,
    clientId: OWNER_CLIENT_ID,
    capabilities: ['pulse:connect', 'pulse:owner'],
    authTime: Math.floor(Date.now() / 1000) - 5,
  };
}

function ownerBinding(storeId, teamId, ownerPrincipalId) {
  return {
    mode: 'team',
    fallback: false,
    principal_ref: ownerPrincipalId,
    commons: {
      resource: OWNER_RESOURCE,
      credential_ref: `keychain:pulse/team/${teamId}/${ownerPrincipalId}`,
      store_id: storeId,
      team_id: teamId,
    },
  };
}

async function createOwnerPublicFixture({ scratch, binding }) {
  const now = Math.floor(Date.now() / 1000);
  const { privateKey: issuerPrivateKey, publicKey: issuerPublicKey } = generateKeyPairSync(
    'rsa', { modulusLength: 2048 },
  );
  const issuerJWK = await exportJWK(issuerPublicKey);
  Object.assign(issuerJWK, { kid: 'synthetic-owner-issuer', alg: 'RS256', use: 'sig' });

  const credentialStore = new MemoryCredentialStore();
  const installationKey = createInstallationKeyRecord({ keyID: 'synthetic-owner-installation-key' });
  const accessToken = await new SignJWT({
    iss: OWNER_ISSUER,
    sub: OWNER_SUBJECT,
    aud: OWNER_RESOURCE,
    iat: now - 1,
    nbf: now - 1,
    exp: now + 300,
    auth_time: now - 1,
    jti: 'synthetic-owner-access-token',
    client_id: OWNER_CLIENT_ID,
    scope: 'pulse:connect pulse:owner',
    cnf: { jkt: installationKey.keyThumbprint },
  }).setProtectedHeader({ alg: 'RS256', kid: issuerJWK.kid, typ: 'at+jwt' })
    .sign(issuerPrivateKey);
  const enrollment = {
    id: OWNER_ENROLLMENT_ID,
    generation: 1,
    clientID: OWNER_CLIENT_ID,
    subject: OWNER_SUBJECT,
    status: 'active',
    revokedAt: null,
    keyThumbprint: installationKey.keyThumbprint,
  };
  persistRemoteCredential(credentialStore, ownerCredentialRef(binding), {
    tokenSet: {
      tokens: { accessToken, refreshToken: 'synthetic-owner-refresh-token', idToken: 'unused.id.token' },
      oauth: {
        issuer: OWNER_ISSUER,
        audience: OWNER_RESOURCE,
        clientID: OWNER_CLIENT_ID,
        subject: OWNER_SUBJECT,
        scope: ['pulse:connect', 'pulse:owner'],
        accessExpiresAt: now + 300,
        tokenKeyThumbprint: installationKey.keyThumbprint,
      },
      metadata: {},
    },
    key: installationKey,
    enrollment,
    authority: {
      tokenEndpoint: `${OWNER_ISSUER}oauth/token`,
      jwksURI: `${OWNER_ISSUER}jwks`,
    },
  });

  const registryPath = join(scratch, 'owner-enrollments.json');
  writeFileSync(registryPath, JSON.stringify({
    schema: 'pulse.team.installation_enrollment_registry.v1',
    issuer: OWNER_ISSUER,
    enrollments: [{
      enrollment_id: OWNER_ENROLLMENT_ID,
      generation: 1,
      client_id: OWNER_CLIENT_ID,
      subject: OWNER_SUBJECT,
      status: 'active',
      public_jwk: installationKey.publicJWK,
    }],
  }), { mode: 0o600 });

  const issuerMetadataURL = new URL('/.well-known/oauth-authorization-server', OWNER_ISSUER).toString();
  const issuerJWKSURL = new URL('jwks', OWNER_ISSUER).toString();
  const browserVerifier = new OAuthResourceVerifier({
    issuer: OWNER_ISSUER,
    resource: OWNER_RESOURCE,
    now: () => Math.floor(Date.now() / 1000),
    resolveHost: async () => ['203.0.113.10'],
    fetch: async (input) => {
      const url = input.toString();
      if (url === issuerMetadataURL) {
        return Response.json({
          issuer: OWNER_ISSUER,
          jwks_uri: issuerJWKSURL,
          code_challenge_methods_supported: ['S256'],
        });
      }
      if (url === issuerJWKSURL) return Response.json({ keys: [issuerJWK] });
      throw new Error('unexpected synthetic issuer request');
    },
  });
  const registry = new InstallationEnrollmentRegistry({ file: registryPath, issuer: OWNER_ISSUER });
  registry.assertReady();
  const senderVerifier = new SenderConstrainedOAuthVerifier({
    oauthVerifier: browserVerifier,
    proofVerifier: new InstallationProofVerifier({ registry }),
  });

  const idToken = (nonce) => new SignJWT({
    iss: OWNER_ISSUER,
    sub: OWNER_SUBJECT,
    aud: OWNER_CLIENT_ID,
    iat: now - 1,
    exp: now + 300,
    auth_time: now - 1,
    nonce,
    acr: AIRLOCK_HUMAN_PRESENCE_ACR,
    amr: ['pwd', 'mfa'],
    [AIRLOCK_HUMAN_PRESENCE_CLAIM]: {
      schema: AIRLOCK_HUMAN_PRESENCE_SCHEMA,
      factor: AIRLOCK_PLATFORM_WEBAUTHN_FACTOR,
      verified_at: now - 1,
    },
  }).setProtectedHeader({ alg: 'RS256', kid: issuerJWK.kid, typ: 'JWT' })
    .sign(issuerPrivateKey);

  return {
    credentialStore,
    idToken,
    async startEdge({ runtime, signer }) {
      const gateway = new OwnerApprovalGateway({
        daemonBaseURL: runtime.baseURL,
        signer,
        expectedOAuthIssuer: OWNER_ISSUER,
        apiKey: () => runtime.apiKey,
      });
      let sequence = 0;
      const server = createHttpServer(async (req, res) => {
        try {
          const path = new URL(req.url ?? '/', 'http://127.0.0.1').pathname;
          if (req.method !== 'POST' || (path !== OWNER_APPROVAL_PUBLIC_PATH && path !== OWNER_MEMBERS_PUBLIC_PATH)) {
            res.writeHead(404, { 'Content-Type': 'application/json' });
            res.end(JSON.stringify({ error: 'not_found' }));
            return;
          }
          const chunks = [];
          for await (const chunk of req) chunks.push(Buffer.from(chunk));
          const body = JSON.parse(Buffer.concat(chunks).toString('utf8'));
          const senderHeaders = {
            authorization: String(req.headers.authorization ?? ''),
            dpop: String(req.headers.dpop ?? ''),
            enrollmentID: String(req.headers['x-pulse-enrollment'] ?? ''),
          };
          let identity;
          let ownerStepUp;
          if (path === OWNER_APPROVAL_PUBLIC_PATH) {
            const authorized = await authorizeTeamOwnerPublicOperation({
              senderVerifier,
              browserVerifier,
              ownerGateway: gateway,
              ownerOperationStepUpNonce: (value, challenge) => ownerOperationStepUpNonce(value, challenge),
              ownerStepUpError: () => new Error('owner_step_up_required'),
            }, {
              senderHeaders,
              method: 'POST',
              targetURL: `${PUBLIC_BASE_URL}${path}`,
              body,
              idToken: String(req.headers['x-pulse-owner-id-token'] ?? ''),
              operationChallenge: String(req.headers['x-pulse-owner-operation-challenge'] ?? ''),
              authorizationStartedAt: Number(req.headers['x-pulse-owner-authorization-started-at']),
              maxAuthenticationAgeSeconds: 300,
            });
            identity = authorized.identity;
            ownerStepUp = authorized.ownerStepUp;
          } else {
            identity = await senderVerifier.verifyAuthorization({
              ...senderHeaders, method: 'POST', targetURL: `${PUBLIC_BASE_URL}${path}`,
            }, ['pulse:owner']);
            gateway.verifyRecentStepUp(identity);
          }
          const value = await gateway.call(
            path, identity, `public-owner-${++sequence}`, body, ownerStepUp,
          );
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify(value));
        } catch (error) {
          const status = Number.isInteger(error?.status) ? error.status : 403;
          res.writeHead(status, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ error: error?.code ?? error?.message ?? 'owner_operation_denied', fallback: false }));
        }
      });
      await new Promise((resolveListen, reject) => {
        server.once('error', reject);
        server.listen(0, '127.0.0.1', resolveListen);
      });
      const address = server.address();
      assert.ok(address && typeof address === 'object');
      const localOrigin = `http://127.0.0.1:${address.port}`;
      const post = createTeamOwnerRemotePost(binding, {
        credentialStore,
        now: () => Math.floor(Date.now() / 1000),
        refresh: async () => {},
        buildHeaders: buildSenderConstrainedRemoteHeaders,
        acquireLock: async () => async () => {},
        fetch: async (url, init) => fetch(`${localOrigin}${new URL(url).pathname}`, init),
      });
      return {
        post,
        stop: () => new Promise((resolveClose) => server.close(() => resolveClose())),
      };
    },
  };
}

async function executeOwnerMutation({
  gateway,
  storeId,
  teamId,
  action,
  mutation,
  route,
  schema,
  requestSuffix,
}) {
  const target = ownerAdminMutationTarget(action, mutation);
  const approved = await gateway.call(
    OWNER_APPROVAL_PUBLIC_PATH,
    ownerIdentity(),
    `accept-${requestSuffix}-approval`,
    {
      schema: 'pulse.team.owner.approval.v1',
      action,
      store_id: storeId,
      team_id: teamId,
      target_kind: target.kind,
      target_id: target.id,
      target_digest: target.digest,
      mutation,
    },
    syntheticStepUp(`accept-${requestSuffix}-approval`),
  );
  assert.match(approved.approval_nonce, /^[0-9a-f]{64}$/);

  const executed = await gateway.call(
    route,
    ownerIdentity(),
    `accept-${requestSuffix}-execute`,
    {
    schema,
    action,
    approval_nonce: approved.approval_nonce,
    ...mutation,
    },
  );
  assert.equal(executed.action, action);
  assert.equal(executed.status, 'complete');
  assert.equal(executed.fallback, false);
  return executed;
}

async function main() {
  const scratch = mkdtempSync(join(tmpdir(), 'pulse-team-real-acceptance-'));
  const homeDir = join(scratch, 'home');
  const dataDir = join(scratch, 'team-data');
  mkdirSync(homeDir, { mode: 0o700 });
  mkdirSync(dataDir, { mode: 0o700 });
  chmodSync(homeDir, 0o700);
  chmodSync(dataDir, 0o700);

  const binaryPath = join(scratch, 'pulse');
  const { privateKey } = generateKeyPairSync('ed25519');
  const signingKeyPath = join(scratch, 'gateway.pk8.pem');
  writeFileSync(
    signingKeyPath,
    privateKey.export({ format: 'pem', type: 'pkcs8' }),
    { mode: 0o600 },
  );
  const publicJWK = createPublicKey(privateKey).export({ format: 'jwk' });
  assert.equal(typeof publicJWK.x, 'string');
  const keyringPath = join(scratch, 'gateway-keyring.json');
  writeFileSync(keyringPath, JSON.stringify({
    active: { kid: KEY_ID, public_key: publicJWK.x },
    previous: [],
  }), { mode: 0o600 });

  const built = spawnSync('go', ['build', '-trimpath', '-o', binaryPath, './cmd/pulse'], {
    cwd: pulseAppDir,
    env: { ...process.env, GOPROXY: 'off', GOTOOLCHAIN: 'local' },
    encoding: 'utf8',
    timeout: 120_000,
  });
  if (built.error || built.status !== 0) {
    throw new Error(`Go Team daemon build failed: ${built.error?.message ?? built.stderr}`);
  }

  let runtime;
  try {
    runtime = await startDaemon({
      binaryPath, dataDir, homeDir, keyringPath, ownerOnly: true,
    });
    const discoveredIntent = (await waitForRequest(runtime, OWNER_BOOTSTRAP_INTERNAL_PATH, {
      schema: 'pulse.team.owner.bootstrap.v1',
      operation: 'prepare',
    })).bootstrap_intent;
    assert.match(discoveredIntent.store_id, /^store_/);
    assert.match(discoveredIntent.team_id, /^team_/);

    const signer = loadPrincipalSigner({
      privateKeyFile: signingKeyPath,
      keyId: KEY_ID,
      verifyKeyringFile: keyringPath,
      storeId: discoveredIntent.store_id,
      teamId: discoveredIntent.team_id,
    });
    const gateway = new OwnerApprovalGateway({
      daemonBaseURL: runtime.baseURL,
      signer,
      expectedOAuthIssuer: OWNER_ISSUER,
      apiKey: () => runtime.apiKey,
    });
    const prepared = await gateway.call(
      OWNER_BOOTSTRAP_PUBLIC_PATH,
      ownerIdentity(),
      'accept-bootstrap-prepare-gateway',
      { schema: 'pulse.team.owner.bootstrap.v1', operation: 'prepare' },
    );
    const bootstrapIntent = prepared.bootstrap_intent;
    assert.match(bootstrapIntent.store_id, /^store_/);
    assert.match(bootstrapIntent.team_id, /^team_/);

    const teamName = 'Synthetic Real Acceptance';
    const bootstrapApproval = await gateway.call(
      OWNER_APPROVAL_PUBLIC_PATH,
      ownerIdentity(),
      'accept-bootstrap-approval',
      {
        schema: 'pulse.team.owner.approval.v1',
        action: 'team.bootstrap',
        store_id: bootstrapIntent.store_id,
        team_id: bootstrapIntent.team_id,
        target_kind: 'team',
        target_id: bootstrapIntent.team_id,
        target_digest: ownerBootstrapTargetDigest(bootstrapIntent, teamName),
        team_name: teamName,
        bootstrap_intent: bootstrapIntent,
      },
      syntheticStepUp('accept-bootstrap-approval'),
    );
    const bootstrapped = await gateway.call(
      OWNER_BOOTSTRAP_PUBLIC_PATH,
      ownerIdentity(),
      'accept-bootstrap-execute',
      {
        schema: 'pulse.team.owner.bootstrap.v1',
        operation: 'execute',
        team_name: teamName,
        bootstrap_intent: bootstrapIntent,
        approval_nonce: bootstrapApproval.approval_nonce,
      },
    );
    assert.equal(bootstrapped.activation_state, 'inactive');
    assert.equal(bootstrapped.public_enabled, false);
    await stopDaemon(runtime);
    runtime = undefined;

    const storeId = bootstrapped.store_id;
    const teamId = bootstrapped.team_id;
    runtime = await startDaemon({
      binaryPath, dataDir, homeDir, keyringPath, ownerOnly: true, storeId, teamId,
    });

    const signerAfterBootstrap = loadPrincipalSigner({
      privateKeyFile: signingKeyPath,
      keyId: KEY_ID,
      verifyKeyringFile: keyringPath,
      storeId,
      teamId,
    });
    let ownerGatewayAfterBootstrap = new OwnerApprovalGateway({
      daemonBaseURL: runtime.baseURL,
      signer: signerAfterBootstrap,
      expectedOAuthIssuer: OWNER_ISSUER,
      apiKey: () => runtime.apiKey,
    });

    const publicFixture = await createOwnerPublicFixture({
      scratch,
      binding: ownerBinding(storeId, teamId, bootstrapped.owner_principal_id),
    });
    let publicEdge = await publicFixture.startEdge({ runtime, signer: signerAfterBootstrap });
    const memberInput = {
      action: ACTION_MEMBERSHIP_CREATE,
      issuer: MEMBER_ISSUER,
      subject: MEMBER_SUBJECT,
      role: 'member',
    };
    const memberStepUp = buildTeamOwnerStepUp(
      ownerBinding(storeId, teamId, bootstrapped.owner_principal_id),
      memberInput,
      { randomBytes: () => Buffer.alloc(32, 9) },
    );
    const memberBrowserProof = {
      idToken: await publicFixture.idToken(memberStepUp.nonce),
      operationChallenge: memberStepUp.challenge,
      authorizationStartedAt: Math.floor(Date.now() / 1000) - 2,
    };
    const changedMemberOperation = buildTeamOwnerOperation(
      ownerBinding(storeId, teamId, bootstrapped.owner_principal_id),
      { ...memberInput, role: 'reviewer' },
    );
    await assert.rejects(
      publicEdge.post(OWNER_APPROVAL_PUBLIC_PATH, changedMemberOperation.approval, memberBrowserProof),
      /team_owner_step_up_required/,
    );
    const memberApproval = await publicEdge.post(
      OWNER_APPROVAL_PUBLIC_PATH, memberStepUp.operation.approval, memberBrowserProof,
    );
    const member = await publicEdge.post(OWNER_MEMBERS_PUBLIC_PATH, {
      ...memberStepUp.operation.execution,
      approval_nonce: memberApproval.approval_nonce,
    });
    assert.equal(member.member.role, 'member');
    await publicEdge.stop();
    await stopDaemon(runtime);
    runtime = await startDaemon({
      binaryPath, dataDir, homeDir, keyringPath, ownerOnly: true, storeId, teamId,
    });
    ownerGatewayAfterBootstrap = new OwnerApprovalGateway({
      daemonBaseURL: runtime.baseURL,
      signer: signerAfterBootstrap,
      expectedOAuthIssuer: OWNER_ISSUER,
      apiKey: () => runtime.apiKey,
    });
    publicEdge = await publicFixture.startEdge({ runtime, signer: signerAfterBootstrap });
    const restartMemberStepUp = buildTeamOwnerStepUp(
      ownerBinding(storeId, teamId, bootstrapped.owner_principal_id),
      { ...memberInput, subject: 'synthetic-member-after-restart' },
      { randomBytes: () => Buffer.alloc(32, 10) },
    );
    const restartMemberBrowserProof = {
      idToken: await publicFixture.idToken(restartMemberStepUp.nonce),
      operationChallenge: restartMemberStepUp.challenge,
      authorizationStartedAt: Math.floor(Date.now() / 1000) - 2,
    };
    const restartMemberApproval = await publicEdge.post(
      OWNER_APPROVAL_PUBLIC_PATH,
      restartMemberStepUp.operation.approval,
      restartMemberBrowserProof,
    );
    const restartMember = await publicEdge.post(OWNER_MEMBERS_PUBLIC_PATH, {
      ...restartMemberStepUp.operation.execution,
      approval_nonce: restartMemberApproval.approval_nonce,
    });
    assert.equal(restartMember.member.role, 'member');
    await assert.rejects(
      publicEdge.post(OWNER_APPROVAL_PUBLIC_PATH, memberStepUp.operation.approval, memberBrowserProof),
      /team_owner_operation_denied/,
    );
    await publicEdge.stop();

    const binding = await executeOwnerMutation({
      gateway: ownerGatewayAfterBootstrap, storeId, teamId,
      action: ACTION_BINDING_CREATE,
      mutation: {
        issuer: MEMBER_ISSUER,
        subject: MEMBER_SUBJECT,
        client_id: MEMBER_CLIENT_ID,
      },
      route: OWNER_BINDINGS_PUBLIC_PATH, schema: SCHEMA_BINDINGS, requestSuffix: 'binding',
    });
    assert.equal(binding.binding.human_principal_id, member.member.principal_id);

    const projectName = 'Synthetic Acceptance Project';
    const project = await executeOwnerMutation({
      gateway: ownerGatewayAfterBootstrap, storeId, teamId,
      action: ACTION_PROJECT_CREATE,
      mutation: { name: projectName },
      route: OWNER_PROJECTS_PUBLIC_PATH, schema: SCHEMA_PROJECTS, requestSuffix: 'project',
    });
    assert.equal(project.project.name, projectName);

    const projectId = project.project.project_id;
    const agentPrincipalId = binding.binding.agent_principal_id;
    const grant = await executeOwnerMutation({
      gateway: ownerGatewayAfterBootstrap, storeId, teamId,
      action: ACTION_PROJECT_GRANT_CREATE,
      mutation: {
        project_id: projectId,
        target_principal_id: agentPrincipalId,
        access_level: 'write',
      },
      route: OWNER_PROJECT_GRANTS_PUBLIC_PATH, schema: SCHEMA_PROJECT_GRANTS,
      requestSuffix: 'project-grant',
    });
    assert.equal(grant.grant.project_id, projectId);
    assert.equal(grant.grant.principal_id, agentPrincipalId);

    const gateDigest = sha256(JSON.stringify({
      compiled_go_daemon: true,
      sqlite_store: true,
      member: member.member.membership_id,
      binding: binding.binding.binding_id,
      project: projectId,
      grant: grant.grant.grant_id,
    }));
    const activationApproval = await ownerGatewayAfterBootstrap.call(
      OWNER_APPROVAL_PUBLIC_PATH,
      ownerIdentity(),
      'accept-activation-approval',
      {
        schema: 'pulse.team.owner.approval.v1',
        action: 'team.activation.synthetic',
        store_id: storeId,
        team_id: teamId,
        target_kind: 'team_activation',
        target_id: teamId,
        target_digest: ownerActivationTargetDigest(storeId, teamId, gateDigest),
        gate_digest: gateDigest,
      },
      syntheticStepUp('accept-activation-approval'),
    );
    const activated = await ownerGatewayAfterBootstrap.call(
      OWNER_ACTIVATE_PUBLIC_PATH,
      ownerIdentity(),
      'accept-activation-execute',
      {
        schema: 'pulse.team.owner.activate.v1',
        approval_nonce: activationApproval.approval_nonce,
        gate_digest: gateDigest,
      },
    );
    assert.equal(activated.activation_state, 'active');
    assert.equal(activated.content_boundary, 'synthetic');
    assert.equal(activated.fallback, false);
    await stopDaemon(runtime);
    runtime = undefined;

    runtime = await startDaemon({
      binaryPath, dataDir, homeDir, keyringPath, ownerOnly: false, storeId, teamId,
    });
    const readiness = await waitForRequest(runtime, '/ready', undefined);
    assert.deepEqual(readiness, { status: 'ready', mode: 'team-remote', fallback: false });

    const principalClient = new TeamPrincipalClient({
      daemonBaseURL: runtime.baseURL,
      signer: signerAfterBootstrap,
      apiKey: () => runtime.apiKey,
    });
    const memberIdentity = {
      issuer: MEMBER_ISSUER,
      subject: MEMBER_SUBJECT,
      clientId: MEMBER_CLIENT_ID,
      capabilities: ['pulse:connect', 'pulse:status', 'pulse:write'],
    };
    const principal = await principalClient.check(memberIdentity, 'accept-principal-check');
    assert.equal(principal.principal_id, agentPrincipalId);
    assert.equal(principal.membership_role, 'member');
    const domain = principalClient.bindDomain(memberIdentity, principal);
    const status = await domain.status({
      schema: 'pulse.team.status.v1',
      active_context: { project_id: projectId },
    });
    assert.equal(status.mode, 'team-remote');
    assert.equal(status.degraded, false);
    assert.equal(status.fallback, false);

    const stored = await domain.remember({
      schema: 'pulse.team.memory.v1',
      source: {
        host: 'codex',
        conversation_scope: 'current_turn',
        timestamp: new Date().toISOString(),
      },
      items: [{
        kind: 'decision',
        redacted_summary: 'Synthetic acceptance memory written through the real Team daemon.',
        confidence: 1,
        evidence_hint: 'user_confirmed',
        tags: ['synthetic-acceptance'],
      }],
      raw_input_included: false,
      active_context: { project_id: projectId },
      target_scope: { type: 'project', id: projectId },
      privacy_tier: 'normal',
      retention: 'project',
      idempotency_key: 'real-acceptance-memory-001',
    });
    assert.equal(stored.status, 'stored');
    assert.equal(stored.fallback, false);

    const deletionIdentity = {
      issuer: MEMBER_ISSUER,
      subject: MEMBER_SUBJECT,
      clientId: MEMBER_CLIENT_ID,
      capabilities: ['pulse:connect', 'pulse:read', 'pulse:delete'],
    };
    const deletionPrincipal = await principalClient.check(
      deletionIdentity, 'accept-deletion-principal-check',
    );
    const deletionDomain = principalClient.bindDomain(deletionIdentity, deletionPrincipal);
    const deleted = await deletionDomain.delete({
      schema: 'pulse.team.delete.v1',
      object_id: stored.object_id,
      active_context: { project_id: projectId },
      idempotency_key: 'real-acceptance-delete-001',
    });
    assert.equal(deleted.object_id, stored.object_id);
    assert.equal(deleted.fallback, false);

    const ownerRuntimeGateway = new OwnerApprovalGateway({
      daemonBaseURL: runtime.baseURL,
      signer: signerAfterBootstrap,
      expectedOAuthIssuer: OWNER_ISSUER,
      apiKey: () => runtime.apiKey,
    });
    const statusApproval = await acceptanceStep('Owner deletion-status approval', () => ownerRuntimeGateway.call(
      OWNER_APPROVAL_PUBLIC_PATH,
      ownerIdentity(),
      'accept-owner-deletion-status-approval',
      {
        schema: 'pulse.team.owner.approval.v1',
        action: 'team.deletion.status',
        store_id: storeId,
        team_id: teamId,
        target_kind: 'deletion_operation',
        target_id: deleted.operation_id,
        target_digest: ownerDeletionStatusTargetDigest(deleted.operation_id),
        operation_id: deleted.operation_id,
      },
      syntheticStepUp('accept-owner-deletion-status-approval'),
    ));
    const ownerDeletionStatus = await acceptanceStep('Owner deletion-status execute', () => ownerRuntimeGateway.call(
      OWNER_DELETION_STATUS_PUBLIC_PATH,
      ownerIdentity(),
      'accept-owner-deletion-status-execute',
      {
        schema: 'pulse.team.owner.deletion_status.v1',
        approval_nonce: statusApproval.approval_nonce,
        operation_id: deleted.operation_id,
      },
    ));
    assert.equal(ownerDeletionStatus.operation_id, deleted.operation_id);
    assert.equal(ownerDeletionStatus.object_id, stored.object_id);
    assert.equal(ownerDeletionStatus.fallback, false);

    const auditLimit = 20;
    const auditApproval = await acceptanceStep('Owner audit approval', () => ownerRuntimeGateway.call(
      OWNER_APPROVAL_PUBLIC_PATH,
      ownerIdentity(),
      'accept-owner-audit-approval',
      {
        schema: 'pulse.team.owner.approval.v1',
        action: 'team.audit.inspect',
        store_id: storeId,
        team_id: teamId,
        target_kind: 'team_audit',
        target_id: teamId,
        target_digest: ownerAuditTargetDigest('', auditLimit),
        limit: auditLimit,
      },
      syntheticStepUp('accept-owner-audit-approval'),
    ));
    const ownerAudit = await acceptanceStep('Owner audit execute', () => ownerRuntimeGateway.call(
      OWNER_AUDIT_PUBLIC_PATH,
      ownerIdentity(),
      'accept-owner-audit-execute',
      {
        schema: 'pulse.team.owner.audit.v1',
        approval_nonce: auditApproval.approval_nonce,
        limit: auditLimit,
      },
    ));
    assert.equal(ownerAudit.own_actions_only, false);
    assert.equal(ownerAudit.fallback, false);
    assert.ok(ownerAudit.events.some((event) => event.action === 'team.object.write'));

    const revokedMember = await executeOwnerMutation({
      gateway: ownerRuntimeGateway, storeId, teamId,
      action: ACTION_MEMBERSHIP_REVOKE,
      mutation: { target_id: member.member.principal_id },
      route: OWNER_MEMBERS_PUBLIC_PATH, schema: SCHEMA_MEMBERS,
      requestSuffix: 'member-revoke',
    });
    assert.equal(revokedMember.target_id, member.member.principal_id);
    await assert.rejects(
      principalClient.check(memberIdentity, 'accept-principal-check-after-revoke'),
      (error) => error?.code === 'principal_revoked',
    );
    const cascades = spawnSync('sqlite3', ['-separator', '|', join(dataDir, 'pulse.db'), `
      SELECT
        (SELECT status FROM team_memberships WHERE principal_id='${member.member.principal_id}'),
        (SELECT status FROM team_agent_bindings WHERE binding_id='${binding.binding.binding_id}'),
        (SELECT status FROM team_project_grants WHERE grant_id='${grant.grant.grant_id}');
    `], { encoding: 'utf8', timeout: 10_000 });
    if (cascades.error || cascades.status !== 0) {
      throw new Error(`SQLite revocation verification failed: ${cascades.error?.message ?? cascades.stderr}`);
    }
    assert.equal(cascades.stdout.trim(), 'revoked|revoked|revoked');

    const sqliteHeader = readFileSync(join(dataDir, 'pulse.db')).subarray(0, 16).toString('binary');
    assert.equal(sqliteHeader, 'SQLite format 3\u0000');
    process.stdout.write(
      '[team-remote-daemon-store-acceptance] PASS: compiled Go daemon, temporary SQLite, synthetic Owner HTTP with CLI-built canonical approval, DPoP sender proof, signed access/ID tokens, action-bound browser nonce, tamper/restart-replay denial, member binding, project grant, activation, scoped write/delete, metadata-only audit, and revocation cascade. External TLS, live IdP behavior, protected registry installation, native helper execution, and packaged CLI installation are intentionally outside this gate.\n',
    );
  } finally {
    await stopDaemon(runtime);
    rmSync(scratch, { recursive: true, force: true });
  }
}

main().catch((error) => {
  process.stderr.write(`[team-remote-daemon-store-acceptance] FAIL: ${error instanceof Error ? error.message : String(error)}\n`);
  process.exitCode = 1;
});
