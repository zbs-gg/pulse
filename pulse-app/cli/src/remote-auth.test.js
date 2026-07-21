import assert from 'node:assert/strict';
import { spawnSync as nodeSpawnSync } from 'node:child_process';
import {
  generateKeyPairSync,
  sign as cryptoSign,
} from 'node:crypto';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import {
  MacOSKeychainCredentialStore,
  MemoryCredentialStore,
  activateRotatedRemoteInstallation,
  buildSenderConstrainedRemoteHeaders,
  createInstallationEnrollmentRequest,
  createInstallationKeyRecord,
  markRemoteCredentialRevoked,
  persistRemoteCredential,
  provisionInstallationKey,
  refreshRemoteCredential,
  verifyInstallationProof,
} from './remote-auth.js';
import { boundedRemoteFetch, boundedRemoteRead } from './remote-auth-network.js';
import {
  createAuthorizationCodePKCERequest,
  exchangeAuthorizationCode,
  validateAuthorizationCallback,
  validateOAuthTokenSet,
} from './remote-auth-oauth.js';

const NOW = 1_783_987_200;
const ISSUER = 'https://tenant.example.auth0.com/';
const AUDIENCE = 'https://pulse.example/mcp';
const CLIENT_ID = 'pulse-native-nik';
const SUBJECT = 'auth0|synthetic-nik';
const CALLBACK = 'http://127.0.0.1:49152/pulse/oauth/callback-abc';
const RESOURCE = 'https://pulse.example/team/v1/context?workspace=synthetic';
const HELPER_RESOURCE = AUDIENCE;
const HELPER_CONSTRAINTS = Object.freeze({
  resource: HELPER_RESOURCE,
  tokenEndpoint: `${ISSUER}oauth/token`,
  clientID: CLIENT_ID,
  subject: SUBJECT,
});
const HELPER_OWNER_PATHS = Object.freeze([
  '/owner/v1/approval', '/owner/v1/bootstrap', '/owner/v1/activate',
  '/owner/v1/members', '/owner/v1/bindings', '/owner/v1/services',
  '/owner/v1/projects', '/owner/v1/project-grants', '/owner/v1/shared-delete',
  '/owner/v1/audit', '/owner/v1/deletion-status',
]);
const TEST_KEY_THUMBPRINT = 'a'.repeat(43);
const AUTHORITY = Object.freeze({
  tokenEndpoint: `${ISSUER}oauth/token`,
  jwksURI: `${ISSUER}.well-known/jwks.json`,
});

function macHelperHarness({ malformedSignature = false, switchPublicKey = false } = {}) {
  const primary = generateKeyPairSync('ec', { namedCurve: 'P-256' });
  const alternate = generateKeyPairSync('ec', { namedCurve: 'P-256' });
  const publicJWK = primary.publicKey.export({ format: 'jwk' });
  const alternatePublicJWK = alternate.publicKey.export({ format: 'jwk' });
  const generic = new Map();
  const dpopMetadata = new Map();
  const calls = [];
  let publicReads = 0;
  const helperPath = '/test/gg.zbs.pulse.presence-helper';
  const response = (value) => ({ status: 0, signal: null, stdout: `${JSON.stringify(value)}\n`, stderr: '' });
  const spawnSync = (command, args, options = {}) => {
    calls.push({ command, args: [...args], input: options.input });
    if (command === '/usr/bin/security') {
      if (args[0] === '-i') {
        const match = /^add-generic-password -s ([^ ]+) -a ([^ ]+) -X ([a-f0-9]+) -U\n$/.exec(options.input);
        assert.ok(match);
        generic.set(match[2], Buffer.from(match[3], 'hex').toString('utf8'));
        return { status: 0, signal: null, stdout: '', stderr: '' };
      }
      if (args[0] === 'find-generic-password') {
        const ref = args[args.indexOf('-a') + 1];
        return generic.has(ref)
          ? { status: 0, signal: null, stdout: `${generic.get(ref)}\n`, stderr: '' }
          : { status: 44, signal: null, stdout: '', stderr: '' };
      }
      if (args[0] === 'delete-generic-password') {
        const ref = args[args.indexOf('-a') + 1];
        const existed = generic.delete(ref);
        return { status: existed ? 0 : 44, signal: null, stdout: '', stderr: '' };
      }
    }
    assert.equal(command, helperPath);
    const payload = JSON.parse(readFileSync(args[args.indexOf('--payload') + 1], 'utf8'));
    calls.at(-1).payload = payload;
    const keyRef = payload.key_ref;
    if (args[0] === 'dpop-create') {
      dpopMetadata.set(keyRef, {
        resource: payload.resource,
        tokenEndpoint: payload.token_endpoint,
      });
      return response({ schema: 'pulse.dpop.public.v1', key_ref: keyRef, public_jwk: publicJWK });
    }
    if (args[0] === 'dpop-public') {
      publicReads += 1;
      const jwk = switchPublicKey && publicReads > 0 ? alternatePublicJWK : publicJWK;
      return response({ schema: 'pulse.dpop.public.v1', key_ref: keyRef, public_jwk: jwk });
    }
    if (args[0] === 'dpop-proof') {
      const metadata = dpopMetadata.get(keyRef);
      let targetAllowed = false;
      try {
        if (payload.purpose === 'token') {
          targetAllowed = payload.htm === 'POST' && payload.htu === metadata?.tokenEndpoint;
        } else if (payload.purpose === 'resource' && metadata) {
          const resource = new URL(metadata.resource);
          const target = new URL(payload.htu);
          const exactMCP = payload.htu === metadata.resource && ['GET', 'POST', 'DELETE'].includes(payload.htm);
          const exactOwner = payload.htm === 'POST' && target.origin === resource.origin &&
            target.username === '' && target.password === '' && target.search === '' && target.hash === '' &&
            target.toString() === payload.htu && HELPER_OWNER_PATHS.includes(target.pathname);
          targetAllowed = exactMCP || exactOwner;
        }
      } catch {
        targetAllowed = false;
      }
      if (!targetAllowed) {
        return { status: 1, signal: null, stdout: '', stderr: 'denied' };
      }
      const proofPayload = payload.purpose === 'token'
        ? {
          client_id: payload.client_id, htm: payload.htm, htu: payload.htu, iat: payload.iat,
          jti: payload.jti, sub: payload.sub, ...(payload.nonce ? { nonce: payload.nonce } : {}),
        }
        : {
          ath: payload.ath, client_id: payload.client_id,
          enrollment_generation: payload.enrollment_generation, enrollment_id: payload.enrollment_id,
          htm: payload.htm, htu: payload.htu, iat: payload.iat, jti: payload.jti, sub: payload.sub,
        };
      const input = Buffer.from(`${b64urlJSON({ typ: 'dpop+jwt', alg: 'ES256', jwk: publicJWK })}.${b64urlJSON(proofPayload)}`);
      const signature = malformedSignature
        ? Buffer.alloc(63, 1)
        : cryptoSign('sha256', input, { key: primary.privateKey, dsaEncoding: 'ieee-p1363' });
      return response({
        schema: 'pulse.dpop.proof_result.v1', key_ref: keyRef,
        proof: `${input.toString()}.${signature.toString('base64url')}`,
      });
    }
    if (args[0] === 'dpop-delete') {
      return response({ schema: 'pulse.dpop.deleted.v1', key_ref: keyRef });
    }
    return { status: 1, signal: null, stdout: '', stderr: 'denied' };
  };
  return { spawnSync, helperPath, generic, calls, publicJWK };
}

function b64urlJSON(value) {
  return Buffer.from(JSON.stringify(value)).toString('base64url');
}

function createIssuer() {
  const { privateKey, publicKey } = generateKeyPairSync('rsa', { modulusLength: 2048 });
  const publicJWK = publicKey.export({ format: 'jwk' });
  Object.assign(publicJWK, { kid: 'issuer-key-1', alg: 'RS256', use: 'sig' });
  return {
    jwks: { keys: [publicJWK] },
    token(payload, header = {}) {
      const protectedHeader = b64urlJSON({ typ: 'JWT', alg: 'RS256', kid: 'issuer-key-1', ...header });
      const encodedPayload = b64urlJSON(payload);
      const input = `${protectedHeader}.${encodedPayload}`;
      const signature = cryptoSign('RSA-SHA256', Buffer.from(input), privateKey).toString('base64url');
      return `${input}.${signature}`;
    },
  };
}

function validClaims(overrides = {}) {
  return {
    iss: ISSUER,
    sub: SUBJECT,
    aud: AUDIENCE,
    client_id: CLIENT_ID,
    iat: NOW - 5,
    nbf: NOW - 5,
    exp: NOW + 300,
    scope: 'openid offline_access pulse:read pulse:audit',
    ...overrides,
  };
}

function validTokenResponse(issuer, overrides = {}, keyThumbprint = TEST_KEY_THUMBPRINT) {
  const idToken = issuer.token(validClaims({ aud: CLIENT_ID, nonce: 'nonce-good' }));
  const accessToken = issuer.token(validClaims({ cnf: { jkt: keyThumbprint } }), { typ: 'at+jwt' });
  return {
    token_type: 'DPoP',
    expires_in: 300,
    access_token: accessToken,
    refresh_token: 'refresh-secret-sentinel',
    id_token: idToken,
    scope: 'openid offline_access pulse:read pulse:audit',
    ...overrides,
  };
}

function validateTokens(response, issuer, overrides = {}) {
  return validateOAuthTokenSet(response, {
    issuer: ISSUER,
    audience: AUDIENCE,
    clientID: CLIENT_ID,
    expectedSubject: SUBJECT,
    nonce: 'nonce-good',
    requiredScopes: ['pulse:read', 'pulse:audit'],
    allowedScopes: ['openid', 'offline_access', 'pulse:read', 'pulse:audit'],
    forbiddenScopes: ['pulse:write', 'pulse:delete', 'pulse:owner'],
    jwks: issuer.jwks,
    now: NOW,
    clockSkewSeconds: 30,
    maxAccessLifetimeSeconds: 600,
    installationKeyThumbprint: TEST_KEY_THUMBPRINT,
    ...overrides,
  });
}

test('builds Authorization Code + PKCE S256 and hardens the exact loopback callback', () => {
  const bytes = Buffer.alloc(64, 7);
  const pending = createAuthorizationCodePKCERequest({
    issuer: ISSUER,
    authorizationEndpoint: `${ISSUER}authorize`,
    tokenEndpoint: `${ISSUER}oauth/token`,
    audience: AUDIENCE,
    clientID: CLIENT_ID,
    callbackURL: CALLBACK,
    scopes: ['openid', 'offline_access', 'pulse:read', 'pulse:audit'],
    dpopJKT: TEST_KEY_THUMBPRINT,
    randomBytes: (count) => bytes.subarray(0, count),
  });

  const authorize = new URL(pending.authorizationURL);
  assert.equal(authorize.origin, 'https://tenant.example.auth0.com');
  assert.equal(authorize.searchParams.get('response_type'), 'code');
  assert.equal(authorize.searchParams.get('code_challenge_method'), 'S256');
  assert.equal(authorize.searchParams.get('audience'), AUDIENCE);
  assert.equal(authorize.searchParams.get('client_id'), CLIENT_ID);
  assert.equal(authorize.searchParams.get('redirect_uri'), CALLBACK);
  assert.notEqual(authorize.searchParams.get('code_challenge'), pending.codeVerifier);
  assert.equal(authorize.searchParams.get('state'), pending.state);
  assert.equal(authorize.searchParams.get('nonce'), pending.nonce);

  const callback = `${CALLBACK}?code=one-time-code&state=${pending.state}`;
  assert.equal(validateAuthorizationCallback(callback, pending, {
    method: 'GET', host: '127.0.0.1:49152', remoteAddress: '127.0.0.1',
  }), 'one-time-code');

  for (const [name, badURL, request] of [
    ['state', `${CALLBACK}?code=c&state=wrong`, { method: 'GET', host: '127.0.0.1:49152', remoteAddress: '127.0.0.1' }],
    ['path', `http://127.0.0.1:49152/other?code=c&state=${pending.state}`, { method: 'GET', host: '127.0.0.1:49152', remoteAddress: '127.0.0.1' }],
    ['host', callback, { method: 'GET', host: 'localhost:49152', remoteAddress: '127.0.0.1' }],
    ['peer', callback, { method: 'GET', host: '127.0.0.1:49152', remoteAddress: '10.0.0.8' }],
    ['method', callback, { method: 'POST', host: '127.0.0.1:49152', remoteAddress: '127.0.0.1' }],
    ['duplicate code', `${callback}&code=second`, { method: 'GET', host: '127.0.0.1:49152', remoteAddress: '127.0.0.1' }],
  ]) {
    assert.throws(() => validateAuthorizationCallback(badURL, pending, request), /remote_auth_/, name);
  }
});

test('exchanges only the exact callback code and PKCE verifier without exposing them', async () => {
  const issuer = createIssuer();
  const installationKey = createInstallationKeyRecord({ keyID: 'exchange-key' });
  const pending = createAuthorizationCodePKCERequest({
    issuer: ISSUER,
    authorizationEndpoint: `${ISSUER}authorize`,
    tokenEndpoint: `${ISSUER}oauth/token`,
    audience: AUDIENCE,
    clientID: CLIENT_ID,
    callbackURL: CALLBACK,
    scopes: ['openid', 'offline_access', 'pulse:read', 'pulse:audit'],
    dpopJKT: installationKey.keyThumbprint,
  });
  let submitted;
  const credential = await exchangeAuthorizationCode({
    pending,
    callbackURL: `${CALLBACK}?code=code-secret-sentinel&state=${pending.state}`,
    callbackRequest: { method: 'GET', host: '127.0.0.1:49152', remoteAddress: '127.0.0.1' },
    fetch: async (url, init) => {
      assert.equal(url, `${ISSUER}oauth/token`);
      submitted = new URLSearchParams(init.body);
      assert.match(init.headers.DPoP, /^[^.]+\.[^.]+\.[^.]+$/);
      return Response.json(validTokenResponse(issuer, {
        id_token: issuer.token(validClaims({ aud: CLIENT_ID, nonce: pending.nonce })),
      }, installationKey.keyThumbprint));
    },
    installationKey,
    validation: {
      expectedSubject: SUBJECT,
      requiredScopes: ['pulse:read', 'pulse:audit'],
      allowedScopes: pending.scopes,
      forbiddenScopes: ['pulse:write', 'pulse:delete', 'pulse:owner'],
      jwks: issuer.jwks,
      now: NOW,
      clockSkewSeconds: 30,
      maxAccessLifetimeSeconds: 600,
    },
  });

  assert.equal(submitted.get('grant_type'), 'authorization_code');
  assert.equal(submitted.get('client_id'), CLIENT_ID);
  assert.equal(submitted.get('redirect_uri'), CALLBACK);
  assert.equal(submitted.get('code'), 'code-secret-sentinel');
  assert.equal(submitted.get('code_verifier'), pending.codeVerifier);
  assert.equal(credential.oauth.subject, SUBJECT);
  assert.equal(credential.oauth.accessExpiresAt, NOW + 300);
  assert.deepEqual(Object.keys(credential.metadata).sort(), [
    'accessExpiresAt', 'audience', 'clientID', 'issuer', 'scope', 'subject', 'tokenKeyThumbprint',
  ]);
  assert.doesNotMatch(JSON.stringify(credential.metadata), /code-secret|refresh-secret|eyJ/);
});

test('authorization-code token exchange aborts a hanging token endpoint at its bounded deadline', async () => {
  const installationKey = createInstallationKeyRecord({ keyID: 'exchange-timeout-key' });
  const pending = createAuthorizationCodePKCERequest({
    issuer: ISSUER,
    authorizationEndpoint: `${ISSUER}authorize`,
    tokenEndpoint: `${ISSUER}oauth/token`,
    audience: AUDIENCE,
    clientID: CLIENT_ID,
    callbackURL: CALLBACK,
    scopes: ['openid', 'offline_access', 'pulse:read', 'pulse:audit'],
    dpopJKT: installationKey.keyThumbprint,
  });
  let aborted = false;
  await assert.rejects(exchangeAuthorizationCode({
    pending,
    callbackURL: `${CALLBACK}?code=one-time-code&state=${pending.state}`,
    callbackRequest: { method: 'GET', host: '127.0.0.1:49152', remoteAddress: '127.0.0.1' },
    fetch: async (_url, init) => new Promise((_resolve, reject) => {
      init.signal.addEventListener('abort', () => {
        aborted = true;
        reject(init.signal.reason);
      }, { once: true });
    }),
    installationKey,
    networkTimeoutMs: 20,
  }), /remote_auth_network_timeout/);
  assert.equal(aborted, true);
});

test('authorization-code token exchange also bounds a hanging response body', async () => {
  const installationKey = createInstallationKeyRecord({ keyID: 'exchange-body-timeout-key' });
  const pending = createAuthorizationCodePKCERequest({
    issuer: ISSUER,
    authorizationEndpoint: `${ISSUER}authorize`,
    tokenEndpoint: `${ISSUER}oauth/token`,
    audience: AUDIENCE,
    clientID: CLIENT_ID,
    callbackURL: CALLBACK,
    scopes: ['openid', 'offline_access', 'pulse:read', 'pulse:audit'],
    dpopJKT: installationKey.keyThumbprint,
  });
  let cancelled = false;
  await assert.rejects(exchangeAuthorizationCode({
    pending,
    callbackURL: `${CALLBACK}?code=one-time-code&state=${pending.state}`,
    callbackRequest: { method: 'GET', host: '127.0.0.1:49152', remoteAddress: '127.0.0.1' },
    fetch: async () => ({
      ok: true,
      json: async () => new Promise(() => {}),
      body: { cancel: () => { cancelled = true; } },
    }),
    installationKey,
    networkTimeoutMs: 20,
  }), /remote_auth_network_timeout/);
  assert.equal(cancelled, true);
});

test('validates exact issuer, audience, client, subject, scope, nonce, and clock bounds', () => {
  const issuer = createIssuer();
  assert.equal(validateTokens(validTokenResponse(issuer), issuer).oauth.subject, SUBJECT);

  const cases = [
    ['issuer', { access_token: issuer.token(validClaims({ iss: 'https://wrong.example/' }), { typ: 'at+jwt' }) }],
    ['audience', { access_token: issuer.token(validClaims({ aud: [AUDIENCE, 'https://other.example'] }), { typ: 'at+jwt' }) }],
    ['client', { access_token: issuer.token(validClaims({ azp: 'wrong-client' }), { typ: 'at+jwt' }) }],
    ['access token azp-only', { access_token: issuer.token(validClaims({ client_id: undefined, azp: CLIENT_ID }), { typ: 'at+jwt' }) }],
    ['subject', { access_token: issuer.token(validClaims({ sub: 'auth0|other' }), { typ: 'at+jwt' }) }],
    ['scope', { access_token: issuer.token(validClaims({ scope: 'pulse:read' }), { typ: 'at+jwt' }) }],
    ['nonce', { id_token: issuer.token(validClaims({ aud: CLIENT_ID, nonce: 'wrong' })) }],
    ['expired', { access_token: issuer.token(validClaims({ iat: NOW - 900, exp: NOW - 31 }), { typ: 'at+jwt' }) }],
    ['future', { access_token: issuer.token(validClaims({ iat: NOW + 31, nbf: NOW + 31, exp: NOW + 331 }), { typ: 'at+jwt' }) }],
    ['too long lived', { access_token: issuer.token(validClaims({ iat: NOW, exp: NOW + 601 }), { typ: 'at+jwt' }) }],
  ];
  for (const [name, tokenOverride] of cases) {
    assert.throws(
      () => validateTokens(validTokenResponse(issuer, tokenOverride), issuer),
      /remote_auth_/,
      name,
    );
  }
});

test('fails closed when an initial token response or access token overgrants any unapproved scope', () => {
  const issuer = createIssuer();
  for (const extra of ['pulse:write', 'pulse:delete', 'pulse:owner', 'admin:surprise']) {
    assert.throws(() => validateTokens(validTokenResponse(issuer, {
      scope: `openid offline_access pulse:read pulse:audit ${extra}`,
    }), issuer), /remote_auth_scope_overgrant/, `response scope ${extra}`);
    assert.throws(() => validateTokens(validTokenResponse(issuer, {
      access_token: issuer.token(validClaims({
        scope: `openid offline_access pulse:read pulse:audit ${extra}`,
      }), { typ: 'at+jwt' }),
    }), issuer), /remote_auth_scope_overgrant/, `access token scope ${extra}`);
  }
});

test('initial token response may omit scope only when the signed access token stays inside the requested profile', () => {
  const issuer = createIssuer();
  const response = validTokenResponse(issuer);
  delete response.scope;
  assert.deepEqual(validateTokens(response, issuer).oauth.scope, [
    'offline_access', 'openid', 'pulse:audit', 'pulse:read',
  ]);
  response.access_token = issuer.token(validClaims({
    scope: 'openid offline_access pulse:read pulse:audit pulse:write',
  }), { typ: 'at+jwt' });
  assert.throws(() => validateTokens(response, issuer), /remote_auth_scope_overgrant/);
});

test('requires the OS credential seam and current enrolled installation proof on every remote use', () => {
  const issuer = createIssuer();
  const store = new MemoryCredentialStore();
  const key = createInstallationKeyRecord({ keyID: 'install-key-1' });
  const tokenSet = validateTokens(validTokenResponse(issuer, {}, key.keyThumbprint), issuer, {
    installationKeyThumbprint: key.keyThumbprint,
  });
  const enrollment = {
    id: 'enrollment_nik_mac_1',
    status: 'active',
    generation: 1,
    keyThumbprint: key.keyThumbprint,
    clientID: CLIENT_ID,
    subject: SUBJECT,
    revokedAt: null,
  };
  persistRemoteCredential(store, 'keychain:pulse/team/nik', { tokenSet, key, enrollment, authority: AUTHORITY });

  const headers = buildSenderConstrainedRemoteHeaders(RESOURCE, {
    method: 'POST', credentialStore: store, credentialRef: 'keychain:pulse/team/nik', now: NOW,
  });
  assert.match(headers.Authorization, /^DPoP eyJ/);
  assert.match(headers.DPoP, /^[^.]+\.[^.]+\.[^.]+$/);
  assert.equal(headers['X-Pulse-Enrollment'], enrollment.id);
  assert.doesNotThrow(() => verifyInstallationProof(headers.DPoP, {
    accessToken: tokenSet.tokens.accessToken,
    url: RESOURCE,
    method: 'POST',
    currentEnrollment: enrollment,
    expectedClientID: CLIENT_ID,
    expectedSubject: SUBJECT,
    now: NOW,
  }));
  assert.throws(() => verifyInstallationProof(headers.DPoP, {
    accessToken: tokenSet.tokens.accessToken,
    url: RESOURCE,
    method: 'POST',
    currentEnrollment: enrollment,
    expectedClientID: CLIENT_ID,
    expectedSubject: SUBJECT,
    now: NOW + 31,
  }), /remote_auth_proof_clock_invalid/);

  const stolen = new MemoryCredentialStore();
  stolen.set('keychain:pulse/team/nik', JSON.stringify({
    ...store.readJSON('keychain:pulse/team/nik'),
    tokens: { accessToken: tokenSet.tokens.accessToken },
  }));
  assert.throws(() => buildSenderConstrainedRemoteHeaders(RESOURCE, {
    method: 'GET', credentialStore: stolen, credentialRef: 'keychain:pulse/team/nik', now: NOW,
  }), /remote_auth_installation_key_unavailable/);

  const clone = createInstallationKeyRecord({ keyID: 'cloned-key' });
  store.createSigningKey('keychain:pulse/team/nik:key:install-key-1', clone);
  assert.throws(() => buildSenderConstrainedRemoteHeaders(RESOURCE, {
    method: 'GET', credentialStore: store, credentialRef: 'keychain:pulse/team/nik', now: NOW,
  }), /remote_auth_installation_key_mismatch/);
});

test('creates a public Owner-reviewable enrollment request without credentials or private key material', () => {
  const issuer = createIssuer();
  const key = createInstallationKeyRecord({ keyID: 'install-key-public-request' });
  const tokenSet = validateTokens(validTokenResponse(issuer, {}, key.keyThumbprint), issuer, {
    installationKeyThumbprint: key.keyThumbprint,
  });
  const result = createInstallationEnrollmentRequest({
    tokenSet,
    key,
    enrollmentID: 'enrollment_nik_mac_public_1',
  });
  assert.deepEqual(result.request, {
    schema: 'pulse.team.installation_enrollment_request.v1',
    issuer: ISSUER,
    enrollment: {
      enrollment_id: 'enrollment_nik_mac_public_1',
      generation: 1,
      client_id: CLIENT_ID,
      subject: SUBJECT,
      status: 'active',
      public_jwk: key.publicJWK,
    },
  });
  assert.match(result.requestDigest, /^sha256:[a-f0-9]{64}$/);
  assert.equal(result.enrollment.keyThumbprint, key.keyThumbprint);
  const rendered = JSON.stringify(result.request);
  assert.doesNotMatch(rendered, /refresh-secret|access_token|refresh_token|id_token|private|\"d\"/);
  assert.throws(
    () => createInstallationEnrollmentRequest({ tokenSet, key, generation: 0 }),
    /remote_auth_enrollment_invalid/,
  );
});

test('refresh grant is DPoP-bound to the enrolled key and validates replacement cnf.jkt', async () => {
  const issuer = createIssuer();
  const key = createInstallationKeyRecord({ keyID: 'refresh-key' });
  const expiring = validTokenResponse(issuer, {
    expires_in: 30,
    access_token: issuer.token(validClaims({
      exp: NOW + 30,
      cnf: { jkt: key.keyThumbprint },
    }), { typ: 'at+jwt' }),
  }, key.keyThumbprint);
  const tokenSet = validateTokens(expiring, issuer, {
    installationKeyThumbprint: key.keyThumbprint,
  });
  const enrollment = {
    id: 'enrollment_refresh_1', status: 'active', generation: 1,
    keyThumbprint: key.keyThumbprint, clientID: CLIENT_ID, subject: SUBJECT, revokedAt: null,
  };
  const store = new MemoryCredentialStore();
  persistRemoteCredential(store, 'credential-refresh', { tokenSet, key, enrollment, authority: AUTHORITY });
  let calls = 0;
  const result = await refreshRemoteCredential(store, 'credential-refresh', {
    now: NOW,
    fetch: async (url, init = {}) => {
      if (url === AUTHORITY.jwksURI) return Response.json(issuer.jwks);
      assert.equal(url, AUTHORITY.tokenEndpoint);
      calls++;
      const submitted = new URLSearchParams(init.body);
      assert.equal(submitted.get('grant_type'), 'refresh_token');
      assert.equal(submitted.get('refresh_token'), 'refresh-secret-sentinel');
      assert.match(init.headers.DPoP, /^[^.]+\.[^.]+\.[^.]+$/);
      const proofPayload = JSON.parse(Buffer.from(init.headers.DPoP.split('.')[1], 'base64url'));
      if (calls === 1) {
        assert.equal(proofPayload.nonce, undefined);
        return Response.json({ error: 'use_dpop_nonce' }, {
          status: 400,
          headers: { 'DPoP-Nonce': 'nonce-from-auth0-123456789' },
        });
      }
      assert.equal(proofPayload.nonce, 'nonce-from-auth0-123456789');
      return Response.json(validTokenResponse(issuer, {
        refresh_token: 'rotated-refresh-secret',
      }, key.keyThumbprint));
    },
  });
  assert.equal(calls, 2);
  assert.equal(result.refreshed, true);
  assert.equal(store.readJSON('credential-refresh').tokens.refreshToken, 'rotated-refresh-secret');

  store.readJSON('credential-refresh').oauth.accessExpiresAt = NOW;
  const current = store.readJSON('credential-refresh');
  current.oauth.accessExpiresAt = NOW;
  store.set('credential-refresh', JSON.stringify(current));
  const withoutRotations = await refreshRemoteCredential(store, 'credential-refresh', {
    now: NOW,
    fetch: async (url) => {
      if (url === AUTHORITY.jwksURI) return Response.json(issuer.jwks);
      const replacement = validTokenResponse(issuer, {}, key.keyThumbprint);
      delete replacement.refresh_token;
      delete replacement.id_token;
      delete replacement.scope;
      return Response.json(replacement);
    },
  });
  assert.equal(withoutRotations.refreshed, true);
  assert.equal(store.readJSON('credential-refresh').tokens.refreshToken, 'rotated-refresh-secret');
  assert.match(store.readJSON('credential-refresh').tokens.idToken, /^[^.]+\.[^.]+\.[^.]+$/);

  const refreshBaseline = new Map(store.values);
  for (const overgrant of [
    { scope: 'openid offline_access pulse:read pulse:audit pulse:write' },
    {
      access_token: issuer.token(validClaims({
        scope: 'openid offline_access pulse:read pulse:audit admin:surprise',
        cnf: { jkt: key.keyThumbprint },
      }), { typ: 'at+jwt' }),
    },
  ]) {
    const attemptStore = new MemoryCredentialStore();
    for (const [ref, value] of refreshBaseline) attemptStore.set(ref, value);
    const before = attemptStore.get('credential-refresh');
    const expired = attemptStore.readJSON('credential-refresh');
    expired.oauth.accessExpiresAt = NOW;
    attemptStore.set('credential-refresh', JSON.stringify(expired));
    await assert.rejects(refreshRemoteCredential(attemptStore, 'credential-refresh', {
      now: NOW,
      fetch: async (url) => {
        if (url === AUTHORITY.jwksURI) return Response.json(issuer.jwks);
        return Response.json(validTokenResponse(issuer, overgrant, key.keyThumbprint));
      },
    }), /remote_auth_scope_overgrant/);
    const after = attemptStore.readJSON('credential-refresh');
    assert.equal(after.tokens.refreshToken, JSON.parse(before).tokens.refreshToken);
    await assert.rejects(
      refreshRemoteCredential(attemptStore, 'credential-refresh', {
        now: NOW, fetch: async () => { throw new Error('must not reuse stale refresh token'); },
      }),
      /remote_auth_refresh_reauth_required/,
    );
  }

  for (const hangingPhase of ['jwks', 'jwks-body', 'token']) {
    const attemptStore = new MemoryCredentialStore();
    for (const [ref, value] of refreshBaseline) attemptStore.set(ref, value);
    const expired = attemptStore.readJSON('credential-refresh');
    expired.oauth.accessExpiresAt = NOW;
    attemptStore.set('credential-refresh', JSON.stringify(expired));
    let aborted = false;
    await assert.rejects(refreshRemoteCredential(attemptStore, 'credential-refresh', {
      now: NOW,
      networkTimeoutMs: 20,
      fetch: async (url, init) => {
        if (hangingPhase === 'jwks-body' && url === AUTHORITY.jwksURI) {
          return {
            ok: true,
            url: AUTHORITY.jwksURI,
            arrayBuffer: async () => new Promise(() => {}),
            body: { cancel: () => { aborted = true; } },
          };
        }
        if (hangingPhase === 'token' && url === AUTHORITY.jwksURI) return Response.json(issuer.jwks);
        return new Promise((_resolve, reject) => {
          init.signal.addEventListener('abort', () => {
            aborted = true;
            reject(init.signal.reason);
          }, { once: true });
        });
      },
    }), /remote_auth_network_timeout/, hangingPhase);
    assert.equal(aborted, true, hangingPhase);
  }

  const stolen = new MemoryCredentialStore();
  stolen.set('credential-refresh', store.get('credential-refresh'));
  await assert.rejects(
    refreshRemoteCredential(stolen, 'credential-refresh', {
      now: NOW + 241,
      fetch: async () => { throw new Error('must not fetch'); },
    }),
    /remote_auth_installation_key_unavailable/,
  );
});

test('refresh rotation kill points leave an explicit reauth marker and never retry stale authority', async () => {
  const issuer = createIssuer();
  const waits = new Int32Array(new SharedArrayBuffer(4));
  for (const killPoint of ['rotation_marked', 'provider_rotated', 'credential_persisted']) {
    const key = createInstallationKeyRecord({ keyID: `refresh-kill-${killPoint}` });
    const expiring = validTokenResponse(issuer, {
      expires_in: 30,
      access_token: issuer.token(validClaims({
        exp: NOW + 30,
        cnf: { jkt: key.keyThumbprint },
      }), { typ: 'at+jwt' }),
    }, key.keyThumbprint);
    const tokenSet = validateTokens(expiring, issuer, {
      installationKeyThumbprint: key.keyThumbprint,
    });
    const enrollment = {
      id: `enrollment_${killPoint}`, status: 'active', generation: 1,
      keyThumbprint: key.keyThumbprint, clientID: CLIENT_ID, subject: SUBJECT, revokedAt: null,
    };
    const store = new MemoryCredentialStore();
    persistRemoteCredential(store, 'credential-kill', {
      tokenSet, key, enrollment, authority: AUTHORITY,
    });
    const before = store.get('credential-kill');
    let providerCalls = 0;
    await assert.rejects(
      refreshRemoteCredential(store, 'credential-kill', {
        now: NOW,
        fetch: async (url) => {
          if (url === AUTHORITY.jwksURI) return Response.json(issuer.jwks);
          providerCalls++;
          return Response.json(validTokenResponse(issuer, {
            refresh_token: `rotated-${killPoint}`,
          }, key.keyThumbprint));
        },
        onRefreshTransition: (phase) => {
          if (phase === killPoint) {
            Atomics.notify(waits, 0);
            throw new Error(`simulated_sigkill_${phase}`);
          }
        },
      }),
      new RegExp(`simulated_sigkill_${killPoint}`),
    );
    if (killPoint !== 'credential_persisted') assert.equal(store.get('credential-kill'), before);
    const markerEntries = [...store.values.entries()].filter(([, value]) => {
      try { return JSON.parse(value).schema === 'pulse.remote_refresh_state.v1'; } catch { return false; }
    });
    assert.equal(markerEntries.length, 1, `${killPoint}: missing refresh state marker`);
    const marker = JSON.parse(markerEntries[0][1]);
    assert.ok(['rotation_in_progress', 'reauth_required'].includes(marker.status));
    assert.doesNotMatch(markerEntries[0][1], /access|refresh_token|rotated-|eyJ/);

    const callsBeforeRetry = providerCalls;
    await assert.rejects(
      refreshRemoteCredential(store, 'credential-kill', {
        now: NOW,
        fetch: async () => { providerCalls++; throw new Error('stale credential reached provider'); },
      }),
      /remote_auth_refresh_reauth_required/,
    );
    assert.equal(providerCalls, callsBeforeRetry, `${killPoint}: stale refresh token was retried`);
  }
});

test('macOS Keychain seam keeps credential values out of argv and errors', () => {
  const calls = [];
  const secret = 'access-refresh-private-secret-sentinel';
  const store = new MacOSKeychainCredentialStore({
    spawnSync: (command, args, options) => {
      calls.push({ command, args, input: options.input });
      return {
        status: 0,
        stdout: args[0] === 'find-generic-password' ? `${secret}\n` : '',
        stderr: '',
      };
    },
  });
  store.set('keychain:pulse/team/nik', secret);
  assert.equal(calls[0].command, '/usr/bin/security');
  assert.deepEqual(calls[0].args, ['-i']);
  assert.doesNotMatch(calls[0].args.join(' '), /access-refresh-private-secret-sentinel/);
  assert.doesNotMatch(calls[0].input, /access-refresh-private-secret-sentinel/);
  assert.match(calls[0].input, /-X [a-f0-9]+/);
});

test('macOS Keychain get fails closed when security exceeds its process deadline', () => {
  const store = new MacOSKeychainCredentialStore({
    processTimeoutMs: 1200,
    spawnSync: (_command, _args, options) => {
      assert.equal(options.timeout, 1200);
      assert.equal(options.killSignal, 'SIGKILL');
      return { status: null, signal: 'SIGTERM', error: { code: 'ETIMEDOUT' }, stdout: '', stderr: '' };
    },
  });
  assert.throws(() => store.get('credential'), /remote_auth_credential_store_timeout/);
});

test('macOS subprocess deadline hard-kills a child that ignores SIGTERM', () => {
  const observedKillSignals = [];
  const store = new MacOSKeychainCredentialStore({
    processTimeoutMs: 20,
    spawnSync: (_command, _args, options) => {
      observedKillSignals.push(options.killSignal);
      return nodeSpawnSync(process.execPath, ['-e', [
        "process.on('SIGTERM', () => {});",
        'setTimeout(() => {}, 300);',
      ].join('')], { ...options, input: undefined });
    },
  });
  const started = Date.now();
  assert.throws(() => store.get('credential'), /remote_auth_credential_store_timeout/);
  assert.ok(Date.now() - started < 150, `subprocess exceeded hard wall: ${Date.now() - started}ms`);
  assert.deepEqual(observedKillSignals, ['SIGKILL']);
});

test('macOS Keychain set fails closed when security exceeds its process deadline', () => {
  const store = new MacOSKeychainCredentialStore({
    spawnSync: () => ({
      status: null, signal: 'SIGTERM', error: { code: 'ETIMEDOUT' }, stdout: '', stderr: '',
    }),
  });
  assert.throws(() => store.set('credential', 'value'), /remote_auth_credential_store_timeout/);
});

test('macOS Keychain delete fails closed when security is aborted', () => {
  const controller = new AbortController();
  controller.abort();
  const store = new MacOSKeychainCredentialStore({
    signal: controller.signal,
    spawnSync: (_command, _args, options) => {
      assert.equal(options.signal, controller.signal);
      return { status: null, signal: null, error: { name: 'AbortError' }, stdout: '', stderr: '' };
    },
  });
  assert.throws(() => store.delete('credential'), /remote_auth_credential_store_aborted/);
});

test('macOS presence helper subprocess is capped by the caller operation deadline', () => {
  const waits = new Int32Array(new SharedArrayBuffer(4));
  const observedTimeouts = [];
  const store = new MacOSKeychainCredentialStore({
    trustMode: 'test', helperPath: '/test/gg.zbs.pulse.presence-helper',
    spawnSync: (_command, _args, options) => {
      observedTimeouts.push(options.timeout);
      Atomics.wait(waits, 0, 0, Math.min(options.timeout + 5, 120));
      return { status: null, signal: 'SIGTERM', error: { code: 'ETIMEDOUT' }, stdout: '', stderr: '' };
    },
  });
  assert.throws(
    () => store.getSigningPublicJWK('keychain:pulse/team/nik:key:1', {
      deadlineAt: Date.now() + 30,
    }),
    /remote_auth_credential_store_timeout/,
  );
  assert.ok(observedTimeouts.length > 0 && observedTimeouts.every((value) => value <= 30), observedTimeouts);
});

test('macOS production store provisions and signs with a non-exportable helper key only', () => {
  const harness = macHelperHarness();
  const store = new MacOSKeychainCredentialStore({
    spawnSync: harness.spawnSync, helperPath: harness.helperPath, trustMode: 'test',
  });
  const key = provisionInstallationKey(store, 'keychain:pulse/team/nik', {
    keyID: 'install_device_bound_1', constraints: HELPER_CONSTRAINTS,
  });
  assert.equal(key.privateJWK, undefined);
  assert.equal(key.publicJWK.d, undefined);
  assert.ok(harness.calls.some((call) => call.command === harness.helperPath && call.args[0] === 'dpop-create'));
  assert.ok(harness.calls.some((call) => call.command === harness.helperPath && call.args[0] === 'dpop-public'));

  const proof = store.createDPoPProof(key.keyRef, {
    schema: 'pulse.dpop.proof.v1', key_ref: key.keyRef, purpose: 'resource',
    htu: HELPER_RESOURCE, htm: 'POST', iat: NOW, jti: 'j'.repeat(32), nonce: '',
    ath: 'h'.repeat(43), enrollment_id: 'enrollment_1', enrollment_generation: 1,
    client_id: CLIENT_ID, sub: SUBJECT,
  });
  assert.equal(proof.split('.').length, 3);
  assert.ok(harness.calls.some((call) => call.command === harness.helperPath && call.args[0] === 'dpop-proof'));
  assert.throws(() => store.sign(key.keyRef, Buffer.from('arbitrary')), /remote_auth_arbitrary_signing_forbidden/);
  assert.equal(harness.calls.filter((call) => call.command === '/usr/bin/security').length, 0);

  assert.throws(
    () => store.createSigningKey('keychain:pulse/team/nik:key:forbidden', createInstallationKeyRecord()),
    /remote_auth_private_key_persistence_forbidden/,
  );
  assert.throws(
    () => store.set('keychain:pulse/team/nik', JSON.stringify({ publicJWK: { ...key.publicJWK, d: 'secret' } })),
    /remote_auth_private_key_persistence_forbidden/,
  );
});

test('macOS production helper policy signs only exact same-origin Owner POST targets', () => {
  const harness = macHelperHarness();
  const store = new MacOSKeychainCredentialStore({
    spawnSync: harness.spawnSync, helperPath: harness.helperPath, trustMode: 'test',
  });
  const key = provisionInstallationKey(store, 'keychain:pulse/team-owner/nik', {
    keyID: 'install_owner_device_bound_1', constraints: HELPER_CONSTRAINTS,
  });
  const claims = (htu, htm = 'POST') => ({
    schema: 'pulse.dpop.proof.v1', key_ref: key.keyRef, purpose: 'resource',
    htu, htm, iat: NOW, jti: 'j'.repeat(32), nonce: '',
    ath: 'h'.repeat(43), enrollment_id: 'enrollment_1', enrollment_generation: 1,
    client_id: CLIENT_ID, sub: SUBJECT,
  });

  for (const path of HELPER_OWNER_PATHS) {
    const target = `https://pulse.example${path}`;
    const proof = store.createDPoPProof(key.keyRef, claims(target));
    assert.equal(JSON.parse(Buffer.from(proof.split('.')[1], 'base64url')).htu, target);
  }

  for (const [target, method] of [
    ['https://other.example/owner/v1/members', 'POST'],
    ['https://pulse.example/owner/v1/members/', 'POST'],
    ['https://pulse.example/owner/v1/members?debug=1', 'POST'],
    ['https://pulse.example/owner/v1/unknown', 'POST'],
    ['https://pulse.example/owner/v1/members', 'GET'],
  ]) {
    assert.throws(
      () => store.createDPoPProof(key.keyRef, claims(target, method)),
      /remote_auth_installation_key_unavailable/,
      `${method} ${target}`,
    );
  }
});

test('macOS production credential document contains tokens and public JWK but no private key', () => {
  const harness = macHelperHarness();
  const store = new MacOSKeychainCredentialStore({
    spawnSync: harness.spawnSync, helperPath: harness.helperPath, trustMode: 'test',
  });
  const key = provisionInstallationKey(store, 'keychain:pulse/team/nik', {
    keyID: 'install_device_bound_2', constraints: HELPER_CONSTRAINTS,
  });
  const issuer = createIssuer();
  const tokenSet = validateTokens(validTokenResponse(issuer, {}, key.keyThumbprint), issuer, {
    installationKeyThumbprint: key.keyThumbprint,
  });
  const enrollment = {
    id: 'enrollment_device_bound_2', status: 'active', generation: 1,
    keyThumbprint: key.keyThumbprint, clientID: CLIENT_ID, subject: SUBJECT, revokedAt: null,
  };
  persistRemoteCredential(store, 'keychain:pulse/team/nik', {
    tokenSet, key, enrollment, authority: AUTHORITY,
  });
  const serialized = harness.generic.get('keychain:pulse/team/nik');
  const document = JSON.parse(serialized);
  assert.equal(document.installation.publicJWK.d, undefined);
  assert.equal(document.installation.keyRef, key.keyRef);
  assert.equal(document.tokens.refreshToken, 'refresh-secret-sentinel');
  assert.doesNotMatch(serialized, /privateJWK|"d"\s*:/);
  assert.equal(harness.generic.has(key.keyRef), false);

  const restartedStore = new MacOSKeychainCredentialStore({
    spawnSync: harness.spawnSync, helperPath: harness.helperPath, trustMode: 'test',
  });
  const headers = buildSenderConstrainedRemoteHeaders(HELPER_RESOURCE, {
    method: 'POST', credentialStore: restartedStore,
    credentialRef: 'keychain:pulse/team/nik', now: NOW,
  });
  assert.match(headers.Authorization, /^DPoP /);
  const migrated = harness.calls.filter((call) => call.command === harness.helperPath && call.args[0] === 'dpop-create').at(-1);
  assert.deepEqual({
    resource: migrated.payload.resource,
    token_endpoint: migrated.payload.token_endpoint,
    client_id: migrated.payload.client_id,
    subject: migrated.payload.subject,
  }, {
    resource: HELPER_RESOURCE,
    token_endpoint: AUTHORITY.tokenEndpoint,
    client_id: CLIENT_ID,
    subject: SUBJECT,
  });
});

test('macOS helper rejects malformed P1363 output and public-key substitution without software fallback', () => {
  const malformed = macHelperHarness({ malformedSignature: true });
  const malformedStore = new MacOSKeychainCredentialStore({
    spawnSync: malformed.spawnSync, helperPath: malformed.helperPath, trustMode: 'test',
  });
  const key = malformedStore.createInstallationKey('credential', {
    keyID: 'install_malformed', constraints: HELPER_CONSTRAINTS,
  });
  assert.throws(
    () => malformedStore.createDPoPProof(key.keyRef, {
      schema: 'pulse.dpop.proof.v1', key_ref: key.keyRef, purpose: 'resource',
      htu: HELPER_RESOURCE, htm: 'POST', iat: NOW, jti: 'j'.repeat(32), nonce: '',
      ath: 'h'.repeat(43), enrollment_id: 'enrollment_1', enrollment_generation: 1,
      client_id: CLIENT_ID, sub: SUBJECT,
    }),
    /remote_auth_installation_proof_(?:signature_)?invalid/,
  );

  const substituted = macHelperHarness({ switchPublicKey: true });
  const substitutedStore = new MacOSKeychainCredentialStore({
    spawnSync: substituted.spawnSync, helperPath: substituted.helperPath, trustMode: 'test',
  });
  assert.throws(
    () => provisionInstallationKey(substitutedStore, 'credential', {
      keyID: 'install_substituted', constraints: HELPER_CONSTRAINTS,
    }),
    /remote_auth_installation_key_mismatch/,
  );
  assert.equal(substituted.generic.size, 0);
});

test('old, wrong, rotated, and revoked enrollments fail before domain access', () => {
  const issuer = createIssuer();
  const store = new MemoryCredentialStore();
  const firstKey = createInstallationKeyRecord({ keyID: 'install-key-1' });
  const tokenSet = validateTokens(validTokenResponse(issuer, {}, firstKey.keyThumbprint), issuer, {
    installationKeyThumbprint: firstKey.keyThumbprint,
  });
  const firstEnrollment = {
    id: 'enrollment_nik_mac_1', status: 'active', generation: 1,
    keyThumbprint: firstKey.keyThumbprint, clientID: CLIENT_ID, subject: SUBJECT, revokedAt: null,
  };
  persistRemoteCredential(store, 'credential', {
    tokenSet, key: firstKey, enrollment: firstEnrollment, authority: AUTHORITY,
  });
  const firstHeaders = buildSenderConstrainedRemoteHeaders(RESOURCE, {
    method: 'GET', credentialStore: store, credentialRef: 'credential', now: NOW,
  });

  const secondKey = createInstallationKeyRecord({ keyID: 'install-key-2' });
  const secondEnrollment = {
    ...firstEnrollment, id: 'enrollment_nik_mac_2', generation: 2,
    keyThumbprint: secondKey.keyThumbprint,
  };
  const rotatedTokenSet = validateTokens(validTokenResponse(issuer, {}, secondKey.keyThumbprint), issuer, {
    installationKeyThumbprint: secondKey.keyThumbprint,
  });
  activateRotatedRemoteInstallation(store, 'credential', {
    key: secondKey, enrollment: secondEnrollment, tokenSet: rotatedTokenSet, now: NOW,
  });
  assert.throws(() => verifyInstallationProof(firstHeaders.DPoP, {
    accessToken: tokenSet.tokens.accessToken, url: RESOURCE, method: 'GET',
    currentEnrollment: secondEnrollment, expectedClientID: CLIENT_ID, expectedSubject: SUBJECT, now: NOW,
  }), /remote_auth_(wrong_enrollment|installation_key_mismatch)/);

  const currentHeaders = buildSenderConstrainedRemoteHeaders(RESOURCE, {
    method: 'GET', credentialStore: store, credentialRef: 'credential', now: NOW,
  });
  assert.throws(() => verifyInstallationProof(currentHeaders.DPoP, {
    accessToken: rotatedTokenSet.tokens.accessToken, url: RESOURCE, method: 'GET',
    currentEnrollment: { ...secondEnrollment, id: 'enrollment_someone_else' },
    expectedClientID: CLIENT_ID, expectedSubject: SUBJECT, now: NOW,
  }), /remote_auth_wrong_enrollment/);

  markRemoteCredentialRevoked(store, 'credential', { revokedAt: NOW });
  assert.throws(() => buildSenderConstrainedRemoteHeaders(RESOURCE, {
    method: 'GET', credentialStore: store, credentialRef: 'credential', now: NOW + 1,
  }), /remote_auth_enrollment_revoked/);
  assert.throws(() => verifyInstallationProof(currentHeaders.DPoP, {
    accessToken: tokenSet.tokens.accessToken, url: RESOURCE, method: 'GET',
    currentEnrollment: { ...secondEnrollment, status: 'revoked', revokedAt: NOW },
    expectedClientID: CLIENT_ID, expectedSubject: SUBJECT, now: NOW + 1,
  }), /remote_auth_enrollment_revoked/);
});

test('auth failures and metadata stay secret-free', () => {
  const issuer = createIssuer();
  const secretAccess = issuer.token(validClaims({ aud: 'wrong-audience-secret' }), { typ: 'at+jwt' });
  const response = validTokenResponse(issuer, {
    access_token: secretAccess,
    refresh_token: 'refresh-secret-sentinel',
  });
  let message = '';
  try {
    validateTokens(response, issuer);
  } catch (error) {
    message = `${error.message}\n${error.stack}`;
  }
  assert.match(message, /remote_auth_audience_mismatch/);
  assert.doesNotMatch(message, /refresh-secret-sentinel|wrong-audience-secret|eyJ/);
});
