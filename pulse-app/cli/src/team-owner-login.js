import { lstatSync, readFileSync, rmSync, statSync } from 'node:fs';
import { isAbsolute, resolve } from 'node:path';

import {
  createInstallationEnrollmentRequest,
  installationKeyThumbprint,
  persistRemoteCredential,
  provisionInstallationKey,
} from './remote-auth.js';
import {
  createAuthorizationCodePKCERequest,
  exchangeAuthorizationCode,
} from './remote-auth-oauth.js';
import {
  createTeamAuthCallbackReceiver,
  fetchTeamAuthJWKS,
  writeTeamEnrollmentRequest,
} from './team-login.js';
import { ownerCredentialRef, TeamOwnerError } from './team-owner-client.js';

const PROFILE_SCHEMA = 'pulse.team.owner_auth_profile.v1';
const PROFILE_FIELDS = [
  'schema', 'issuer', 'authorization_endpoint', 'token_endpoint', 'jwks_uri',
  'audience', 'client_id', 'expected_subject', 'scopes',
];
const REQUIRED_SCOPES = ['openid', 'offline_access', 'pulse:connect', 'pulse:owner'];
const FORBIDDEN_SCOPES = new Set(['pulse:read', 'pulse:write', 'pulse:delete', 'pulse:audit', 'pulse:status']);
const SAFE_ID = /^[A-Za-z0-9][A-Za-z0-9._|:@/-]{0,255}$/;
const MAX_PROFILE_BYTES = 32 * 1024;
const OWNER_HUMAN_PRESENCE_ACR = 'https://pulse.zbs.gg/acr/airlock-human-presence/v1';

function fail(code) {
  throw new TeamOwnerError(`login_${code}`);
}

function exactObject(value, fields, code = 'profile_invalid') {
  if (!value || typeof value !== 'object' || Array.isArray(value) ||
      Object.keys(value).sort().join('\0') !== [...fields].sort().join('\0')) fail(code);
  return value;
}

function exactString(value, code, { max = 2048, pattern } = {}) {
  if (typeof value !== 'string' || value === '' || value.length > max || value.trim() !== value ||
      /[\u0000-\u001f\u007f]/.test(value) || (pattern && !pattern.test(value))) fail(code);
  return value;
}

function httpsURL(value, code) {
  let parsed;
  try { parsed = new URL(exactString(value, code)); } catch { fail(code); }
  if (parsed.protocol !== 'https:' || parsed.username || parsed.password || parsed.search || parsed.hash) fail(code);
  return parsed;
}

export function readTeamOwnerAuthProfile(path, {
  trustMode = process.env.PULSE_TRUST_MODE === 'test' ? 'test' : 'production',
  effectiveUID = typeof process.geteuid === 'function' ? process.geteuid() : undefined,
} = {}) {
  if (!isAbsolute(path)) fail('profile_path_invalid');
  const absolute = resolve(path);
  const link = lstatSync(absolute);
  const info = statSync(absolute);
  const expectedUID = trustMode === 'test' ? effectiveUID : 0;
  if (link.isSymbolicLink() || !info.isFile() || info.size < 2 || info.size > MAX_PROFILE_BYTES ||
      (info.mode & 0o022) !== 0 || !Number.isInteger(expectedUID) || info.uid !== expectedUID) {
    fail('profile_unsafe');
  }
  let value;
  try { value = JSON.parse(readFileSync(absolute, 'utf8')); } catch { fail('profile_invalid'); }
  exactObject(value, PROFILE_FIELDS);
  if (value.schema !== PROFILE_SCHEMA) fail('profile_invalid');
  const issuer = httpsURL(value.issuer, 'issuer_invalid');
  const authorization = httpsURL(value.authorization_endpoint, 'authorization_endpoint_invalid');
  const token = httpsURL(value.token_endpoint, 'token_endpoint_invalid');
  const jwks = httpsURL(value.jwks_uri, 'jwks_uri_invalid');
  if (value.issuer !== issuer.toString()) fail('issuer_invalid');
  if ([authorization, token, jwks].some((url) => url.origin !== issuer.origin)) fail('provider_origin_mismatch');
  const audience = httpsURL(value.audience, 'audience_invalid');
  if (audience.pathname !== '/mcp') fail('audience_invalid');
  const clientID = exactString(value.client_id, 'client_invalid', { max: 255, pattern: SAFE_ID });
  const expectedSubject = exactString(value.expected_subject, 'subject_invalid', { max: 255, pattern: SAFE_ID });
  if (!Array.isArray(value.scopes) || value.scopes.length !== REQUIRED_SCOPES.length ||
      new Set(value.scopes).size !== value.scopes.length) fail('scope_invalid');
  const scopes = value.scopes.map((scope) => exactString(scope, 'scope_invalid', {
    max: 128, pattern: /^[A-Za-z0-9:._/-]+$/,
  }));
  if (REQUIRED_SCOPES.some((scope) => !scopes.includes(scope)) ||
      scopes.some((scope) => FORBIDDEN_SCOPES.has(scope))) fail('scope_invalid');
  return Object.freeze({
    schema: PROFILE_SCHEMA,
    issuer: issuer.toString(), authorizationEndpoint: authorization.toString(),
    tokenEndpoint: token.toString(), jwksURI: jwks.toString(), audience: audience.toString(),
    clientID, expectedSubject, scopes: Object.freeze(scopes),
  });
}

function existingOwnerCredential(store, credentialRef, profile) {
  const serialized = store?.get?.(credentialRef);
  if (serialized === undefined) return undefined;
  let value;
  try { value = JSON.parse(serialized); } catch { fail('credential_invalid'); }
  if (!value || value.schema !== 'pulse.remote_credential.v2' || !value.oauth ||
      value.oauth.issuer !== profile.issuer || value.oauth.audience !== profile.audience ||
      value.oauth.clientID !== profile.clientID || value.oauth.subject !== profile.expectedSubject ||
      !value.installation || !value.enrollment || value.enrollment.status !== 'active' ||
      value.enrollment.revokedAt !== null || value.enrollment.clientID !== profile.clientID ||
      value.enrollment.subject !== profile.expectedSubject) fail('credential_invalid');
  const { keyID, keyRef, keyThumbprint, publicJWK } = value.installation;
  if (typeof keyID !== 'string' || typeof keyRef !== 'string' ||
      typeof keyThumbprint !== 'string' || installationKeyThumbprint(publicJWK) !== keyThumbprint) {
    fail('credential_invalid');
  }
  const live = store.getSigningPublicJWK?.(keyRef);
  if (!live || installationKeyThumbprint(live) !== keyThumbprint) fail('credential_invalid');
  return Object.freeze({
    key: Object.freeze({ keyID, keyRef, keyThumbprint, publicJWK: Object.freeze(publicJWK) }),
    enrollment: Object.freeze({ ...value.enrollment }),
  });
}

function accessTokenAuthTime(token) {
  if (typeof token !== 'string') fail('step_up_stale');
  const parts = token.split('.');
  if (parts.length !== 3) fail('step_up_stale');
  let payload;
  try { payload = JSON.parse(Buffer.from(parts[1], 'base64url').toString('utf8')); } catch { fail('step_up_stale'); }
  if (!Number.isSafeInteger(payload.auth_time) || payload.auth_time < 1) fail('step_up_stale');
  return payload.auth_time;
}

async function performOwnerOAuth({
  profile, key, credentialStore, openAuthorizationURL, fetch: fetchFn,
  timeoutMs, networkTimeoutMs, signal, now, operationNonce,
}) {
  const jwks = await fetchTeamAuthJWKS(profile, fetchFn, { networkTimeoutMs, signal });
  const receiver = await createTeamAuthCallbackReceiver({ timeoutMs });
  const authorizationStartedAt = now();
  try {
    const pending = createAuthorizationCodePKCERequest({
      issuer: profile.issuer, authorizationEndpoint: profile.authorizationEndpoint,
      tokenEndpoint: profile.tokenEndpoint, audience: profile.audience,
      clientID: profile.clientID, callbackURL: receiver.callbackURL,
      scopes: profile.scopes, dpopJKT: key.keyThumbprint,
      ...(operationNonce === undefined ? {} : { nonce: operationNonce }),
    });
    const authorization = new URL(pending.authorizationURL);
    authorization.searchParams.set('prompt', 'login');
    authorization.searchParams.set('max_age', '0');
    authorization.searchParams.set('acr_values', OWNER_HUMAN_PRESENCE_ACR);
    await openAuthorizationURL(authorization.toString());
    const callback = await receiver.wait();
    const tokenSet = await exchangeAuthorizationCode({
      pending, callbackURL: callback.callbackURL, callbackRequest: callback.callbackRequest,
      fetch: fetchFn, installationKey: key, credentialStore,
      validation: {
        expectedSubject: profile.expectedSubject,
        requiredScopes: profile.scopes, allowedScopes: profile.scopes,
        forbiddenScopes: [...FORBIDDEN_SCOPES], jwks,
      },
      networkTimeoutMs, signal,
    });
    return {
      tokenSet, authTime: accessTokenAuthTime(tokenSet.tokens.accessToken), observedAt: now(),
      authorizationStartedAt,
    };
  } finally {
    receiver.close();
  }
}

export async function runTeamOwnerLogin({
  profile,
  binding,
  credentialStore,
  outputPath,
  openAuthorizationURL,
  fetch: fetchFn = globalThis.fetch,
  timeoutMs = 180_000,
  networkTimeoutMs = 10_000,
  signal,
  now = () => Math.floor(Date.now() / 1000),
  oauthFlow = performOwnerOAuth,
} = {}) {
  if (!profile || !binding || binding.mode !== 'team' || binding.fallback !== false || !binding.commons ||
      profile.audience !== binding.commons.resource || typeof oauthFlow !== 'function') fail('binding_required');
  if (oauthFlow === performOwnerOAuth &&
      (typeof openAuthorizationURL !== 'function' || typeof fetchFn !== 'function')) fail('runtime_unavailable');
  const credentialRef = ownerCredentialRef(binding);
  const existing = existingOwnerCredential(credentialStore, credentialRef, profile);
  let key = existing?.key;
  let persisted = false;
  let writtenPath;
  const authorizationStartedAt = now();
  try {
    if (!key) {
      key = provisionInstallationKey(credentialStore, credentialRef, {
        constraints: {
          resource: profile.audience, tokenEndpoint: profile.tokenEndpoint,
          clientID: profile.clientID, subject: profile.expectedSubject,
        },
      });
    }
    const result = await oauthFlow({
      profile, key, credentialStore, openAuthorizationURL, fetch: fetchFn,
      timeoutMs, networkTimeoutMs, signal, now,
    });
    const observedAt = result.observedAt ?? now();
    if (!Number.isSafeInteger(result.authTime) || result.authTime < authorizationStartedAt - 5 ||
        result.authTime > observedAt + 5 || observedAt - result.authTime > 300) fail('step_up_stale');
    if (!result.tokenSet) fail('credential_invalid');
    let enrollment = existing?.enrollment;
    let requestDigest;
    if (!enrollment) {
      const proposed = createInstallationEnrollmentRequest({ tokenSet: result.tokenSet, key });
      enrollment = proposed.enrollment;
      requestDigest = proposed.requestDigest;
      writtenPath = writeTeamEnrollmentRequest(outputPath, proposed.request);
    }
    try {
      persistRemoteCredential(credentialStore, credentialRef, {
        tokenSet: result.tokenSet, key, enrollment,
        authority: { tokenEndpoint: profile.tokenEndpoint, jwksURI: profile.jwksURI },
      });
      persisted = true;
    } catch (error) {
      if (writtenPath) rmSync(writtenPath, { force: true });
      throw error;
    }
    return Object.freeze({
      schema: 'pulse.team.owner_login_receipt.v1',
      status: existing ? 'owner_auth_refreshed_enrollment_unverified' : 'pending_owner_registry_approval',
      credentialRef,
      enrollmentID: enrollment.id,
      ...(requestDigest ? { requestDigest, requestPath: writtenPath } : {}),
      fallback: false,
    });
  } finally {
    if (key && !existing && !persisted) credentialStore?.deleteSigningKey?.(key.keyRef);
  }
}

export async function runTeamOwnerStepUp({
  profile,
  binding,
  credentialStore,
  operationNonce,
  openAuthorizationURL,
  fetch: fetchFn = globalThis.fetch,
  timeoutMs = 180_000,
  networkTimeoutMs = 10_000,
  signal,
  now = () => Math.floor(Date.now() / 1000),
  oauthFlow = performOwnerOAuth,
} = {}) {
  if (!profile || !binding || binding.mode !== 'team' || binding.fallback !== false || !binding.commons ||
      profile.audience !== binding.commons.resource || typeof oauthFlow !== 'function' ||
      typeof operationNonce !== 'string' || !/^[A-Za-z0-9_-]{43}$/.test(operationNonce)) {
    fail('binding_required');
  }
  if (oauthFlow === performOwnerOAuth &&
      (typeof openAuthorizationURL !== 'function' || typeof fetchFn !== 'function')) fail('runtime_unavailable');
  const credentialRef = ownerCredentialRef(binding);
  const existing = existingOwnerCredential(credentialStore, credentialRef, profile);
  if (!existing) fail('enrollment_required');
  const authorizationStartedAt = now();
  const result = await oauthFlow({
    profile, key: existing.key, credentialStore, openAuthorizationURL, fetch: fetchFn,
    timeoutMs, networkTimeoutMs, signal, now, operationNonce,
  });
  const observedAt = result.observedAt ?? now();
  const startedAt = result.authorizationStartedAt ?? authorizationStartedAt;
  if (!Number.isSafeInteger(startedAt) || startedAt < authorizationStartedAt - 5 ||
      startedAt > observedAt + 5 || !Number.isSafeInteger(result.authTime) ||
      result.authTime < startedAt - 5 || result.authTime > observedAt + 5 ||
      observedAt - result.authTime > 300 || !result.tokenSet?.tokens?.idToken) {
    fail('step_up_stale');
  }
  persistRemoteCredential(credentialStore, credentialRef, {
    tokenSet: result.tokenSet, key: existing.key, enrollment: existing.enrollment,
    authority: { tokenEndpoint: profile.tokenEndpoint, jwksURI: profile.jwksURI },
  });
  return Object.freeze({
    schema: 'pulse.team.owner_operation_step_up.v1',
    status: 'owner_operation_step_up_ready',
    credentialRef,
    enrollmentID: existing.enrollment.id,
    idToken: result.tokenSet.tokens.idToken,
    authorizationStartedAt: startedAt,
    fallback: false,
  });
}
