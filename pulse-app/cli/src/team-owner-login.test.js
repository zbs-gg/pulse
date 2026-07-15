import assert from 'node:assert/strict';
import { generateKeyPairSync, sign as cryptoSign } from 'node:crypto';
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { MemoryCredentialStore } from './remote-auth.js';
import {
  readTeamOwnerAuthProfile,
  runTeamOwnerLogin,
  runTeamOwnerStepUp,
} from './team-owner-login.js';
import { ownerCredentialRef } from './team-owner-client.js';

const ISSUER = 'https://tenant.example.auth0.com/';
const AUDIENCE = 'https://pulse.example/mcp';
const CLIENT_ID = 'pulse-owner-native';
const SUBJECT = 'auth0|synthetic-owner';

function binding() {
  return {
    mode: 'team', fallback: false, principal_ref: 'principal_owner',
    commons: {
      resource: AUDIENCE, team_id: 'team_test', store_id: 'store_test',
      credential_ref: 'keychain:pulse/team/team_test/principal_owner',
    },
  };
}

function profileDocument(overrides = {}) {
  return {
    schema: 'pulse.team.owner_auth_profile.v1',
    issuer: ISSUER,
    authorization_endpoint: `${ISSUER}authorize`,
    token_endpoint: `${ISSUER}oauth/token`,
    jwks_uri: `${ISSUER}.well-known/jwks.json`,
    audience: AUDIENCE,
    client_id: CLIENT_ID,
    expected_subject: SUBJECT,
    scopes: ['openid', 'offline_access', 'pulse:connect', 'pulse:owner'],
    ...overrides,
  };
}

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

test('Owner auth profile is root-controlled, least-privilege, and distinct from installation auth', () => {
  const dir = mkdtempSync(join(tmpdir(), 'pulse-team-owner-profile-'));
  const path = join(dir, 'profile.json');
  writeFileSync(path, JSON.stringify(profileDocument()), { mode: 0o600 });
  const profile = readTeamOwnerAuthProfile(path, {
    trustMode: 'test', effectiveUID: process.geteuid(),
  });
  assert.equal(profile.audience, AUDIENCE);
  assert.deepEqual(profile.scopes, ['openid', 'offline_access', 'pulse:connect', 'pulse:owner']);
  assert.notEqual(ownerCredentialRef(binding()), binding().commons.credential_ref);

  writeFileSync(path, JSON.stringify(profileDocument({ issuer: 'https://tenant.example.auth0.com' })), {
    mode: 0o600,
  });
  assert.throws(
    () => readTeamOwnerAuthProfile(path, { trustMode: 'test', effectiveUID: process.geteuid() }),
    /team_owner_login_issuer_invalid/,
  );

  for (const scope of ['pulse:read', 'pulse:write', 'pulse:delete', 'pulse:audit', 'pulse:status']) {
    writeFileSync(path, JSON.stringify(profileDocument({
      scopes: [...profileDocument().scopes, scope],
    })), { mode: 0o600 });
    assert.throws(
      () => readTeamOwnerAuthProfile(path, { trustMode: 'test', effectiveUID: process.geteuid() }),
      /team_owner_login_scope_invalid/,
    );
  }
});

test('Owner browser login enrolls once then reuses the same DPoP key for fresh step-up', async () => {
  const issuer = tokenIssuer();
  const dir = mkdtempSync(join(tmpdir(), 'pulse-team-owner-login-'));
  const profilePath = join(dir, 'profile.json');
  const firstOutput = join(dir, 'owner-enrollment.json');
  const secondOutput = join(dir, 'should-not-exist.json');
  writeFileSync(profilePath, JSON.stringify(profileDocument()), { mode: 0o600 });
  const profile = readTeamOwnerAuthProfile(profilePath, {
    trustMode: 'test', effectiveUID: process.geteuid(),
  });
  const store = new MemoryCredentialStore();
  const now = Math.floor(Date.now() / 1000);
  let nonce = '';
  let dpopJKT = '';
  let authTime = now;
  const fetchFn = async (url, init = {}) => {
    if (url === profile.jwksURI) return Response.json(issuer.jwks);
    assert.equal(url, profile.tokenEndpoint);
    const claims = {
      iss: ISSUER, sub: SUBJECT, client_id: CLIENT_ID,
      iat: now - 1, nbf: now - 1, exp: now + 300, auth_time: authTime,
      scope: 'openid offline_access pulse:connect pulse:owner',
    };
    return Response.json({
      token_type: 'DPoP', expires_in: 300,
      access_token: issuer.token({ ...claims, aud: AUDIENCE, cnf: { jkt: dpopJKT } }, { typ: 'at+jwt' }),
      refresh_token: 'owner-refresh-secret',
      id_token: issuer.token({ ...claims, aud: CLIENT_ID, nonce }),
      scope: claims.scope,
    });
  };
  const openAuthorizationURL = async (authorizationURL) => {
    const authorization = new URL(authorizationURL);
    assert.equal(authorization.searchParams.get('prompt'), 'login');
    assert.equal(authorization.searchParams.get('max_age'), '0');
    assert.equal(
      authorization.searchParams.get('acr_values'),
      'https://pulse.zbs.gg/acr/airlock-human-presence/v1',
    );
    nonce = authorization.searchParams.get('nonce');
    dpopJKT = authorization.searchParams.get('dpop_jkt');
    const callback = new URL(authorization.searchParams.get('redirect_uri'));
    callback.searchParams.set('code', 'one-time-code');
    callback.searchParams.set('state', authorization.searchParams.get('state'));
    assert.equal((await fetch(callback)).status, 200);
  };

  const first = await runTeamOwnerLogin({
    profile, binding: binding(), credentialStore: store, outputPath: firstOutput,
    openAuthorizationURL, fetch: fetchFn, now: () => now,
  });
  assert.equal(first.status, 'pending_owner_registry_approval');
  assert.equal(first.credentialRef, ownerCredentialRef(binding()));
  assert.equal(first.requestPath, firstOutput);
  assert.equal(existsSync(firstOutput), true);
  assert.doesNotMatch(readFileSync(firstOutput, 'utf8'), /access_token|refresh_token|owner-refresh-secret|private/);
  const initial = store.readJSON(ownerCredentialRef(binding()));

  authTime = now + 1;
  const second = await runTeamOwnerLogin({
    profile, binding: binding(), credentialStore: store, outputPath: secondOutput,
    openAuthorizationURL, fetch: fetchFn, now: () => now + 1,
  });
  assert.equal(second.status, 'owner_auth_refreshed_enrollment_unverified');
  assert.equal(second.enrollmentID, first.enrollmentID);
  assert.equal(second.requestPath, undefined);
  assert.equal(existsSync(secondOutput), false);
  const refreshed = store.readJSON(ownerCredentialRef(binding()));
  assert.equal(refreshed.installation.keyID, initial.installation.keyID);
  assert.equal(refreshed.installation.keyThumbprint, initial.installation.keyThumbprint);

  authTime = now + 2;
  const operationNonce = 'n'.repeat(43);
  const stepUp = await runTeamOwnerStepUp({
    profile, binding: binding(), credentialStore: store, operationNonce,
    openAuthorizationURL, fetch: fetchFn, now: () => now + 2,
  });
  assert.equal(nonce, operationNonce);
  assert.equal(stepUp.status, 'owner_operation_step_up_ready');
  assert.equal(stepUp.authorizationStartedAt, now + 2);
  assert.match(stepUp.idToken, /^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/);
});

test('Owner login rejects a stale auth_time instead of pretending step-up succeeded', async () => {
  const profile = Object.freeze({
    ...profileDocument(),
    authorizationEndpoint: `${ISSUER}authorize`, tokenEndpoint: `${ISSUER}oauth/token`,
    jwksURI: `${ISSUER}.well-known/jwks.json`, expectedSubject: SUBJECT,
  });
  await assert.rejects(runTeamOwnerLogin({
    profile, binding: binding(), credentialStore: new MemoryCredentialStore(),
    outputPath: join(mkdtempSync(join(tmpdir(), 'pulse-owner-stale-')), 'request.json'),
    now: () => 1_000,
    oauthFlow: async () => ({ authTime: 600 }),
  }), /team_owner_login_step_up_stale/);
});
