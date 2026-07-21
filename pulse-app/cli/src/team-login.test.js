import assert from 'node:assert/strict';
import { generateKeyPairSync, sign as cryptoSign } from 'node:crypto';
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { MemoryCredentialStore } from './remote-auth.js';
import { readTeamAuthProfile, runTeamLogin } from './team-login.js';

const ISSUER = 'https://tenant.example.auth0.com/';
const AUDIENCE = 'https://pulse.example/mcp';
const CLIENT_ID = 'pulse-native-nik';
const SUBJECT = 'auth0|synthetic-nik';

function tokenIssuer() {
  const { privateKey, publicKey } = generateKeyPairSync('rsa', { modulusLength: 2048 });
  const publicJWK = publicKey.export({ format: 'jwk' });
  Object.assign(publicJWK, { kid: 'issuer-key-1', alg: 'RS256', use: 'sig' });
  return {
    jwks: { keys: [publicJWK] },
    token(payload, header = {}) {
      const protectedHeader = Buffer.from(JSON.stringify({
        typ: 'JWT', alg: 'RS256', kid: 'issuer-key-1', ...header,
      })).toString('base64url');
      const encodedPayload = Buffer.from(JSON.stringify(payload)).toString('base64url');
      const input = `${protectedHeader}.${encodedPayload}`;
      const signature = cryptoSign('RSA-SHA256', Buffer.from(input), privateKey).toString('base64url');
      return `${input}.${signature}`;
    },
  };
}

function profileDocument(overrides = {}) {
  return {
    schema: 'pulse.team.auth_profile.v1',
    issuer: ISSUER,
    authorization_endpoint: `${ISSUER}authorize`,
    token_endpoint: `${ISSUER}oauth/token`,
    jwks_uri: `${ISSUER}.well-known/jwks.json`,
    audience: AUDIENCE,
    client_id: CLIENT_ID,
    expected_subject: SUBJECT,
    scopes: ['openid', 'offline_access', 'pulse:connect', 'pulse:status', 'pulse:read', 'pulse:audit'],
    ...overrides,
  };
}

test('Team auth profile is closed, owner-controlled, and pins one provider origin', () => {
  const dir = mkdtempSync(join(tmpdir(), 'pulse-team-auth-profile-'));
  const path = join(dir, 'profile.json');
  writeFileSync(path, JSON.stringify(profileDocument()), { mode: 0o600 });
  const profile = readTeamAuthProfile(path, {
    trustMode: 'test', effectiveUID: process.geteuid(),
  });
  assert.equal(profile.audience, AUDIENCE);
  assert.equal(profile.expectedSubject, SUBJECT);

  writeFileSync(path, JSON.stringify(profileDocument({ client_secret: 'forbidden' })), { mode: 0o600 });
  assert.throws(
    () => readTeamAuthProfile(path, { trustMode: 'test', effectiveUID: process.geteuid() }),
    /team_login_profile_invalid/,
  );
  writeFileSync(path, JSON.stringify(profileDocument({ token_endpoint: 'https://evil.example/token' })), { mode: 0o600 });
  assert.throws(
    () => readTeamAuthProfile(path, { trustMode: 'test', effectiveUID: process.geteuid() }),
    /team_login_provider_origin_mismatch/,
  );
});

test('Team login completes PKCE locally and emits only a pending public enrollment request', async () => {
  const issuer = tokenIssuer();
  const dir = mkdtempSync(join(tmpdir(), 'pulse-team-login-'));
  const profilePath = join(dir, 'profile.json');
  const outputPath = join(dir, 'enrollment-request.json');
  writeFileSync(profilePath, JSON.stringify(profileDocument()), { mode: 0o600 });
  const profile = readTeamAuthProfile(profilePath, {
    trustMode: 'test', effectiveUID: process.geteuid(),
  });
  const binding = {
    mode: 'team',
    fallback: false,
    commons: {
      resource: AUDIENCE,
      credential_ref: 'keychain:pulse/team/nik',
      team_id: 'team_test',
    },
  };
  let provisionedBeforeOAuth = false;
  const store = new class extends MemoryCredentialStore {
    createInstallationKey(...args) {
      provisionedBeforeOAuth = true;
      return super.createInstallationKey(...args);
    }
  }();
  let nonce = '';
  let dpopJKT = '';
  const now = Math.floor(Date.now() / 1000);
  const fetchFn = async (url, init = {}) => {
    if (url === profile.jwksURI) return Response.json(issuer.jwks);
    assert.equal(url, profile.tokenEndpoint);
    const submitted = new URLSearchParams(init.body);
    assert.equal(submitted.get('grant_type'), 'authorization_code');
    const claims = {
      iss: ISSUER, sub: SUBJECT, client_id: CLIENT_ID,
      iat: now - 2, nbf: now - 2, exp: now + 300,
      scope: 'openid offline_access pulse:connect pulse:status pulse:read pulse:audit',
    };
    return Response.json({
      token_type: 'DPoP', expires_in: 300,
      access_token: issuer.token({ ...claims, aud: AUDIENCE, cnf: { jkt: dpopJKT } }, { typ: 'at+jwt' }),
      refresh_token: 'refresh-secret-sentinel',
      id_token: issuer.token({ ...claims, aud: CLIENT_ID, nonce }),
      scope: claims.scope,
    });
  };

  const result = await runTeamLogin({
    profile,
    binding,
    credentialStore: store,
    outputPath,
    fetch: fetchFn,
    openAuthorizationURL: async (authorizationURL) => {
      assert.equal(provisionedBeforeOAuth, true);
      const authorization = new URL(authorizationURL);
      nonce = authorization.searchParams.get('nonce');
      dpopJKT = authorization.searchParams.get('dpop_jkt');
      assert.match(dpopJKT, /^[A-Za-z0-9_-]{43}$/);
      const callback = new URL(authorization.searchParams.get('redirect_uri'));
      callback.searchParams.set('code', 'one-time-code');
      callback.searchParams.set('state', authorization.searchParams.get('state'));
      const response = await fetch(callback);
      assert.equal(response.status, 200);
    },
  });

  assert.equal(result.status, 'pending_owner_registry_approval');
  assert.equal(result.credentialRef, binding.commons.credential_ref);
  assert.match(result.requestDigest, /^sha256:[a-f0-9]{64}$/);
  const requestText = readFileSync(outputPath, 'utf8');
  const request = JSON.parse(requestText);
  assert.equal(request.schema, 'pulse.team.installation_enrollment_request.v1');
  assert.equal(request.enrollment.client_id, CLIENT_ID);
  assert.equal(request.enrollment.subject, SUBJECT);
  assert.equal(request.enrollment.status, 'active');
  assert.doesNotMatch(requestText, /refresh-secret|access_token|refresh_token|id_token|private/);

  const stored = store.readJSON(binding.commons.credential_ref);
  assert.equal(stored.enrollment.id, result.enrollmentID);
  assert.equal(stored.oauth.subject, SUBJECT);
  assert.deepEqual(stored.authority, {
    tokenEndpoint: profile.tokenEndpoint,
    jwksURI: profile.jwksURI,
  });
  assert.notEqual(stored.tokens.refreshToken, undefined);
  assert.equal(stored.installation.publicJWK.d, undefined);
  assert.doesNotMatch(JSON.stringify(stored), /privateJWK|"d"\s*:/);
});

test('Team login deletes a provisioned installation key when OAuth fails and never falls back', async () => {
  const issuer = tokenIssuer();
  const dir = mkdtempSync(join(tmpdir(), 'pulse-team-login-cleanup-'));
  const profilePath = join(dir, 'profile.json');
  writeFileSync(profilePath, JSON.stringify(profileDocument()), { mode: 0o600 });
  const profile = readTeamAuthProfile(profilePath, {
    trustMode: 'test', effectiveUID: process.geteuid(),
  });
  let provisionedRef = '';
  let deletedRef = '';
  const store = new class extends MemoryCredentialStore {
    createInstallationKey(...args) {
      const key = super.createInstallationKey(...args);
      provisionedRef = key.keyRef;
      return key;
    }
    deleteSigningKey(ref) {
      deletedRef = ref;
      super.deleteSigningKey(ref);
    }
  }();
  await assert.rejects(runTeamLogin({
    profile,
    binding: {
      mode: 'team', fallback: false,
      commons: { resource: AUDIENCE, credential_ref: 'keychain:pulse/team/cleanup' },
    },
    credentialStore: store,
    outputPath: join(dir, 'enrollment-request.json'),
    fetch: async (url) => {
      assert.equal(url, profile.jwksURI);
      return Response.json(issuer.jwks);
    },
    openAuthorizationURL: async () => { throw new Error('browser_failed'); },
  }), /browser_failed/);
  assert.notEqual(provisionedRef, '');
  assert.equal(deletedRef, provisionedRef);
  assert.equal(store.get(provisionedRef), undefined);
  assert.equal(store.get('keychain:pulse/team/cleanup'), undefined);
});

test('Team installation auth profile rejects Commons write, delete, and owner scopes', () => {
  const dir = mkdtempSync(join(tmpdir(), 'pulse-team-auth-profile-privilege-'));
  const path = join(dir, 'profile.json');
  for (const scope of ['pulse:write', 'pulse:delete', 'pulse:owner']) {
    writeFileSync(path, JSON.stringify(profileDocument({
      scopes: [...profileDocument().scopes, scope],
    })), { mode: 0o600 });
    assert.throws(
      () => readTeamAuthProfile(path, { trustMode: 'test', effectiveUID: process.geteuid() }),
      /team_login_scope_invalid/,
    );
  }
});

test('Team login rejects a provider that overissues any unapproved initial scope', async () => {
  const issuer = tokenIssuer();
  const dir = mkdtempSync(join(tmpdir(), 'pulse-team-login-overgrant-'));
  const profilePath = join(dir, 'profile.json');
  writeFileSync(profilePath, JSON.stringify(profileDocument()), { mode: 0o600 });
  const profile = readTeamAuthProfile(profilePath, {
    trustMode: 'test', effectiveUID: process.geteuid(),
  });
  let nonce = '';
  let dpopJKT = '';
  const now = Math.floor(Date.now() / 1000);
  await assert.rejects(runTeamLogin({
    profile,
    binding: {
      mode: 'team', fallback: false,
      commons: { resource: AUDIENCE, credential_ref: 'keychain:pulse/team/nik' },
    },
    credentialStore: new MemoryCredentialStore(),
    outputPath: join(dir, 'enrollment-request.json'),
    networkTimeoutMs: 100,
    fetch: async (url) => {
      if (url === profile.jwksURI) return Response.json(issuer.jwks);
      const scope = `${profile.scopes.join(' ')} pulse:write`;
      return Response.json({
        token_type: 'DPoP', expires_in: 300,
        access_token: issuer.token({
          iss: ISSUER, sub: SUBJECT, client_id: CLIENT_ID, aud: AUDIENCE,
          iat: now - 2, nbf: now - 2, exp: now + 300, scope,
          cnf: { jkt: dpopJKT },
        }, { typ: 'at+jwt' }),
        refresh_token: 'refresh-secret-sentinel',
        id_token: issuer.token({
          iss: ISSUER, sub: SUBJECT, client_id: CLIENT_ID, aud: CLIENT_ID,
          iat: now - 2, nbf: now - 2, exp: now + 300, scope, nonce,
        }),
        scope,
      });
    },
    openAuthorizationURL: async (authorizationURL) => {
      const authorization = new URL(authorizationURL);
      nonce = authorization.searchParams.get('nonce');
      dpopJKT = authorization.searchParams.get('dpop_jkt');
      const callback = new URL(authorization.searchParams.get('redirect_uri'));
      callback.searchParams.set('code', 'one-time-code');
      callback.searchParams.set('state', authorization.searchParams.get('state'));
      await fetch(callback);
    },
  }), /remote_auth_scope_overgrant/);
});
