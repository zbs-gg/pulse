import {
  createHash,
  createPrivateKey,
  createPublicKey,
  generateKeyPairSync,
  randomBytes as cryptoRandomBytes,
  sign as cryptoSign,
  timingSafeEqual,
  verify as cryptoVerify,
} from 'node:crypto';
import { spawnSync as nodeSpawnSync } from 'node:child_process';
import { chmodSync, lstatSync, mkdtempSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { RemoteAuthError, remoteAuthFail as fail } from './remote-auth-errors.js';
import {
  boundedRemoteFetch,
  boundedRemoteRead,
  DEFAULT_REMOTE_NETWORK_TIMEOUT_MS,
} from './remote-auth-network.js';

const SAFE_METHOD = /^(GET|HEAD|POST|PUT|PATCH|DELETE)$/;
const SAFE_ID = /^[A-Za-z0-9][A-Za-z0-9._|:@/-]{0,255}$/;
const SAFE_CREDENTIAL_REF = /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,511}$/;
const COMPACT_PART = /^[A-Za-z0-9_-]+$/;
const DEFAULT_CLOCK_SKEW_SECONDS = 30;
const DEFAULT_MAX_ACCESS_LIFETIME_SECONDS = 900;
const DEFAULT_NETWORK_TIMEOUT_MS = DEFAULT_REMOTE_NETWORK_TIMEOUT_MS;
const MAX_JWKS_BYTES = 256 * 1024;
const KEYCHAIN_SERVICE = 'gg.zbs.pulse.remote-auth';
const DEFAULT_KEYCHAIN_PROCESS_TIMEOUT_MS = 5_000;
const MAX_KEYCHAIN_PROCESS_TIMEOUT_MS = 15_000;
const PRESENCE_HELPER = '/Library/PrivilegedHelperTools/gg.zbs.pulse.presence-helper';
const PRESENCE_HELPER_IDENTIFIER = 'gg.zbs.pulse.presence-helper';
const PRESENCE_HELPER_TEAM_ID = '44N4NZ86S5';
const REFRESH_STATE_SCHEMA = 'pulse.remote_refresh_state.v1';
export const INSTALLATION_ENROLLMENT_REQUEST_SCHEMA = 'pulse.team.installation_enrollment_request.v1';

function assertRemoteOperationActive({ signal, deadlineAt } = {}) {
  if (signal?.aborted) fail('network_aborted');
  if (deadlineAt !== undefined && (!Number.isFinite(deadlineAt) || Date.now() >= deadlineAt)) {
    fail('network_timeout');
  }
}

function refreshStateRef(credentialRef) {
  safeString(credentialRef, 'credential_ref_invalid', { max: 512 });
  const digest = createHash('sha256').update('pulse-refresh-state-v1\0').update(credentialRef).digest('hex');
  return `keychain:pulse/refresh-state/${digest}`;
}

function parseRefreshState(serialized) {
  let state;
  try { state = JSON.parse(serialized); } catch { fail('refresh_state_invalid'); }
  const expectedKeys = ['attempt_id', 'credential_ref_sha256', 'schema', 'started_at', 'status'];
  if (!state || typeof state !== 'object' || Array.isArray(state) ||
      Object.keys(state).sort().join('\0') !== expectedKeys.join('\0') ||
      state.schema !== REFRESH_STATE_SCHEMA ||
      !['rotation_in_progress', 'reauth_required'].includes(state.status) ||
      typeof state.attempt_id !== 'string' || !/^[A-Za-z0-9_-]{24}$/.test(state.attempt_id) ||
      typeof state.credential_ref_sha256 !== 'string' || !/^[a-f0-9]{64}$/.test(state.credential_ref_sha256) ||
      !Number.isSafeInteger(state.started_at) || state.started_at < 0) {
    fail('refresh_state_invalid');
  }
  return state;
}

function assertRefreshStateClear(store, credentialRef, operation) {
  if (!store || typeof store.get !== 'function') fail('credential_store_unavailable');
  const serialized = store.get(refreshStateRef(credentialRef), operation);
  if (serialized === undefined) return;
  parseRefreshState(serialized);
  fail('refresh_reauth_required');
}

function beginRefreshRotation(store, credentialRef, now, operation) {
  if (!store || typeof store.set !== 'function' || typeof store.delete !== 'function') {
    fail('credential_store_unavailable');
  }
  const marker = {
    schema: REFRESH_STATE_SCHEMA,
    status: 'rotation_in_progress',
    attempt_id: randomBase64URL(18),
    credential_ref_sha256: createHash('sha256').update(credentialRef).digest('hex'),
    started_at: now,
  };
  store.set(refreshStateRef(credentialRef), JSON.stringify(marker), operation);
  return marker;
}

function requireRefreshReauth(store, credentialRef, marker, operation) {
  store.set(refreshStateRef(credentialRef), JSON.stringify({
    ...marker,
    status: 'reauth_required',
  }), operation);
}

function clearRefreshRotation(store, credentialRef, operation) {
  store.delete(refreshStateRef(credentialRef), operation);
}

function safeString(value, code, { max = 2048, pattern } = {}) {
  if (typeof value !== 'string' || value.length === 0 || value.length > max || /[\u0000-\u001f\u007f]/.test(value)) {
    fail(code);
  }
  if (pattern && !pattern.test(value)) fail(code);
  return value;
}

function strictURL(value, code) {
  let parsed;
  try {
    parsed = new URL(value);
  } catch {
    fail(code);
  }
  if (parsed.username || parsed.password || parsed.hash) fail(code);
  return parsed;
}

function pinnedHTTPSURL(value, code) {
  const parsed = strictURL(value, code);
  if (parsed.protocol !== 'https:' || parsed.search) fail(code);
  return parsed;
}

function safeEqual(left, right) {
  if (typeof left !== 'string' || typeof right !== 'string') return false;
  const a = Buffer.from(left);
  const b = Buffer.from(right);
  return a.length === b.length && timingSafeEqual(a, b);
}

function uniqueParam(params, name, code) {
  const values = params.getAll(name);
  if (values.length !== 1 || values[0] === '') fail(code);
  return values[0];
}

function randomBase64URL(bytes, randomBytes = cryptoRandomBytes) {
  const value = randomBytes(bytes);
  if (!Buffer.isBuffer(value) && !(value instanceof Uint8Array)) fail('random_source_invalid');
  if (value.length !== bytes) fail('random_source_invalid');
  return Buffer.from(value).toString('base64url');
}

function sha256Base64URL(value) {
  return createHash('sha256').update(value).digest('base64url');
}

function encodeJSON(value) {
  return Buffer.from(JSON.stringify(value)).toString('base64url');
}

function decodePart(value, code) {
  if (!COMPACT_PART.test(value)) fail(code);
  let decoded;
  try {
    decoded = Buffer.from(value, 'base64url');
  } catch {
    fail(code);
  }
  if (decoded.toString('base64url') !== value) fail(code);
  return decoded;
}

function parseCompactJWT(token, code = 'jwt_invalid') {
  safeString(token, code, { max: 32 * 1024 });
  const parts = token.split('.');
  if (parts.length !== 3 || parts.some((part) => part === '')) fail(code);
  let header;
  let payload;
  try {
    header = JSON.parse(decodePart(parts[0], code).toString('utf8'));
    payload = JSON.parse(decodePart(parts[1], code).toString('utf8'));
  } catch (error) {
    if (error instanceof RemoteAuthError) throw error;
    fail(code);
  }
  if (!header || Array.isArray(header) || typeof header !== 'object' ||
      !payload || Array.isArray(payload) || typeof payload !== 'object') fail(code);
  return {
    header,
    payload,
    signature: decodePart(parts[2], code),
    signingInput: Buffer.from(`${parts[0]}.${parts[1]}`),
  };
}

function verifyJWTSignature(parsed, jwks) {
  if (!jwks || !Array.isArray(jwks.keys)) fail('jwks_invalid');
  const { alg, kid } = parsed.header;
  if ((alg !== 'RS256' && alg !== 'ES256') || typeof kid !== 'string' || kid === '') fail('jwt_header_invalid');
  const matches = jwks.keys.filter((key) => key && key.kid === kid &&
    (!key.alg || key.alg === alg) && (!key.use || key.use === 'sig'));
  if (matches.length !== 1) fail('signing_key_unavailable');
  let publicKey;
  try {
    publicKey = createPublicKey({ key: matches[0], format: 'jwk' });
  } catch {
    fail('signing_key_invalid');
  }
  const options = alg === 'ES256' ? { key: publicKey, dsaEncoding: 'ieee-p1363' } : publicKey;
  const algorithm = alg === 'ES256' ? 'sha256' : 'RSA-SHA256';
  if (!cryptoVerify(algorithm, parsed.signingInput, options, parsed.signature)) fail('jwt_signature_invalid');
}

function exactAudience(actual, expected) {
  return typeof actual === 'string' && actual === expected;
}

function validateClock(payload, { now, clockSkewSeconds, maxLifetimeSeconds, prefix = '' }) {
  const iat = payload.iat;
  const exp = payload.exp;
  const nbf = payload.nbf ?? iat;
  if (!Number.isInteger(iat) || !Number.isInteger(exp) || !Number.isInteger(nbf) || exp <= iat) {
    fail(`${prefix}clock_invalid`);
  }
  if (iat > now + clockSkewSeconds || nbf > now + clockSkewSeconds) fail(`${prefix}not_yet_valid`);
  if (exp < now - clockSkewSeconds) fail(`${prefix}expired`);
  if (maxLifetimeSeconds && exp - iat > maxLifetimeSeconds) fail(`${prefix}lifetime_too_long`);
}

function scopeSet(value) {
  if (typeof value !== 'string' || value === '' || /[\u0000-\u001f\u007f]/.test(value)) fail('scope_invalid');
  const parts = value.split(' ');
  if (parts.some((part) => !/^[A-Za-z0-9:._/-]+$/.test(part)) || new Set(parts).size !== parts.length) {
    fail('scope_invalid');
  }
  return new Set(parts);
}

function configuredScopes(values, code = 'scope_invalid') {
  if (!Array.isArray(values) || new Set(values).size !== values.length) fail(code);
  return new Set(values.map((scope) => safeString(scope, code, {
    max: 128, pattern: /^[A-Za-z0-9:._/-]+$/,
  })));
}

function validateScopeAuthority(actual, { allowedScopes, forbiddenScopes = [], requiredScopes = [] }) {
  const required = configuredScopes(requiredScopes);
  const forbidden = configuredScopes(forbiddenScopes);
  const allowed = allowedScopes === undefined ? undefined : configuredScopes(allowedScopes);
  if (allowed && [...required].some((scope) => !allowed.has(scope))) fail('scope_invalid');
  if ([...actual].some((scope) => forbidden.has(scope) || (allowed && !allowed.has(scope)))) {
    fail('scope_overgrant');
  }
  if ([...required].some((scope) => !actual.has(scope))) fail('scope_mismatch');
  return actual;
}

function validateJWTClaims(token, {
  jwks,
  issuer,
  audience,
  clientID,
  expectedSubject,
  requiredScopes = [],
  allowedScopes,
  forbiddenScopes = [],
  nonce,
  now,
  clockSkewSeconds,
  maxLifetimeSeconds,
  accessToken = false,
}) {
  const parsed = parseCompactJWT(token);
  verifyJWTSignature(parsed, jwks);
  const { header, payload } = parsed;
  if (accessToken && header.typ !== 'at+jwt') fail('access_token_type_invalid');
  if (!accessToken && header.typ !== 'JWT') fail('id_token_type_invalid');
  if (payload.iss !== issuer) fail('issuer_mismatch');
  if (!exactAudience(payload.aud, audience)) fail('audience_mismatch');
  const actualClient = accessToken ? payload.client_id : (payload.client_id ?? payload.azp);
  if (actualClient !== clientID) fail('client_mismatch');
  if (payload.client_id !== undefined && payload.azp !== undefined && payload.client_id !== payload.azp) {
    fail('client_mismatch');
  }
  safeString(payload.sub, 'subject_invalid', { max: 255, pattern: SAFE_ID });
  if (expectedSubject && payload.sub !== expectedSubject) fail('subject_mismatch');
  validateClock(payload, { now, clockSkewSeconds, maxLifetimeSeconds, prefix: accessToken ? 'access_token_' : 'id_token_' });
  if (nonce !== undefined && payload.nonce !== nonce) fail('nonce_mismatch');
  if (requiredScopes.length > 0 || allowedScopes !== undefined || forbiddenScopes.length > 0) {
    validateScopeAuthority(scopeSet(payload.scope), { requiredScopes, allowedScopes, forbiddenScopes });
  }
  return payload;
}

function validateOAuthRefreshTokenSet(response, {
  existingTokens,
  issuer,
  audience,
  clientID,
  expectedSubject,
  requiredScopes = [],
  allowedScopes,
  forbiddenScopes = [],
  jwks,
  now,
  clockSkewSeconds = DEFAULT_CLOCK_SKEW_SECONDS,
  maxAccessLifetimeSeconds = DEFAULT_MAX_ACCESS_LIFETIME_SECONDS,
  installationKeyThumbprint,
}) {
  if (!response || typeof response !== 'object' || Array.isArray(response) || response.token_type !== 'DPoP') {
    fail('token_response_invalid');
  }
  safeString(response.access_token, 'access_token_missing', { max: 32 * 1024 });
  safeString(existingTokens?.refreshToken, 'refresh_token_missing', { max: 32 * 1024 });
  safeString(existingTokens?.idToken, 'id_token_missing', { max: 32 * 1024 });
  if (response.refresh_token !== undefined) safeString(response.refresh_token, 'refresh_token_missing', { max: 32 * 1024 });
  if (response.id_token !== undefined) safeString(response.id_token, 'id_token_missing', { max: 32 * 1024 });
  if (!Number.isInteger(response.expires_in) || response.expires_in <= 0 ||
      response.expires_in > maxAccessLifetimeSeconds + clockSkewSeconds) fail('token_expiry_invalid');
  const required = [...configuredScopes(requiredScopes)];
  const approved = allowedScopes === undefined ? undefined : [...configuredScopes(allowedScopes)];
  const responseScopes = response.scope === undefined && approved
    ? new Set(approved)
    : scopeSet(response.scope);
  validateScopeAuthority(responseScopes, {
    requiredScopes: required, allowedScopes: approved, forbiddenScopes,
  });
  const accessClaims = validateJWTClaims(response.access_token, {
    jwks, issuer, audience, clientID, expectedSubject, requiredScopes: required,
    allowedScopes: approved, forbiddenScopes,
    now, clockSkewSeconds, maxLifetimeSeconds: maxAccessLifetimeSeconds, accessToken: true,
  });
  if (!accessClaims.cnf || typeof accessClaims.cnf !== 'object' || Array.isArray(accessClaims.cnf) ||
      Object.keys(accessClaims.cnf).length !== 1 ||
      !safeEqual(accessClaims.cnf.jkt, installationKeyThumbprint)) {
    fail('access_token_key_mismatch');
  }
  if (Math.abs((accessClaims.exp - now) - response.expires_in) > clockSkewSeconds) fail('token_expiry_mismatch');
  if (response.id_token !== undefined) {
    const idClaims = validateJWTClaims(response.id_token, {
      jwks, issuer, audience: clientID, clientID, expectedSubject,
      now, clockSkewSeconds, maxLifetimeSeconds: undefined,
    });
    if (idClaims.sub !== accessClaims.sub) fail('subject_mismatch');
  }
  const scope = [...responseScopes].sort();
  const oauth = Object.freeze({
    issuer,
    audience,
    clientID,
    subject: accessClaims.sub,
    scope,
    accessExpiresAt: accessClaims.exp,
    tokenKeyThumbprint: installationKeyThumbprint,
  });
  return Object.freeze({
    tokens: Object.freeze({
      accessToken: response.access_token,
      refreshToken: response.refresh_token ?? existingTokens.refreshToken,
      idToken: response.id_token ?? existingTokens.idToken,
    }),
    oauth,
  });
}

function createTokenEndpointProof({
  key, credential, store, tokenEndpoint, nonce,
  now = Math.floor(Date.now() / 1000), operation,
}) {
  const publicJWK = publicInstallationJWK(key?.publicJWK ?? credential?.installation?.publicJWK);
  const header = { typ: 'dpop+jwt', alg: 'ES256', jwk: publicJWK };
  const payload = {
    htu: proofTarget(tokenEndpoint),
    htm: 'POST',
    iat: now,
    jti: randomBase64URL(24),
    ...(nonce ? { nonce } : {}),
  };
  const keyRefValue = key?.keyRef ?? credential?.installation?.keyRef;
  if (typeof store?.createDPoPProof === 'function' && keyRefValue) {
    return store.createDPoPProof(keyRefValue, {
      schema: 'pulse.dpop.proof.v1', key_ref: keyRefValue, purpose: 'token',
      htu: payload.htu, htm: payload.htm, iat: payload.iat, jti: payload.jti,
      nonce: payload.nonce ?? '', ath: '', enrollment_id: '', enrollment_generation: 0,
      client_id: key?.constraints?.clientID ?? credential?.oauth?.clientID,
      sub: key?.constraints?.subject ?? credential?.oauth?.subject,
    }, operation);
  }
  const input = `${encodeJSON(header)}.${encodeJSON(payload)}`;
  const signature = key?.privateJWK
    ? signES256(key.privateJWK, Buffer.from(input))
    : key
      ? store?.sign?.(key.keyRef, Buffer.from(input), operation)
    : store?.sign?.(credential.installation.keyRef, Buffer.from(input), operation);
  if (!Buffer.isBuffer(signature) || signature.length !== 64) fail('installation_key_unavailable');
  return `${input}.${signature.toString('base64url')}`;
}

function publicInstallationJWK(jwk) {
  if (!jwk || jwk.kty !== 'EC' || jwk.crv !== 'P-256' ||
      typeof jwk.x !== 'string' || typeof jwk.y !== 'string' || jwk.d !== undefined ||
      !/^[A-Za-z0-9_-]{43}$/.test(jwk.x) || !/^[A-Za-z0-9_-]{43}$/.test(jwk.y)) {
    fail('installation_key_invalid');
  }
  for (const coordinate of [jwk.x, jwk.y]) {
    const bytes = Buffer.from(coordinate, 'base64url');
    if (bytes.length !== 32 || bytes.toString('base64url') !== coordinate) fail('installation_key_invalid');
  }
  return { kty: 'EC', crv: 'P-256', x: jwk.x, y: jwk.y };
}

export function installationKeyThumbprint(jwk) {
  const publicJWK = publicInstallationJWK(jwk);
  return sha256Base64URL(JSON.stringify({
    crv: publicJWK.crv,
    kty: publicJWK.kty,
    x: publicJWK.x,
    y: publicJWK.y,
  }));
}

export function createInstallationKeyRecord({ keyID = `install_${randomBase64URL(18)}` } = {}) {
  safeString(keyID, 'installation_key_id_invalid', { max: 255, pattern: SAFE_ID });
  const { privateKey, publicKey } = generateKeyPairSync('ec', { namedCurve: 'P-256' });
  const publicJWK = publicInstallationJWK(publicKey.export({ format: 'jwk' }));
  return Object.freeze({
    keyID,
    publicJWK: Object.freeze(publicJWK),
    privateJWK: Object.freeze(privateKey.export({ format: 'jwk' })),
    keyThumbprint: installationKeyThumbprint(publicJWK),
  });
}

// Creates the public, content-free record that a human Owner may add to the
// operator-managed enrollment registry. This function grants no remote
// authority: the gateway remains fail-closed until the exact entry exists in
// its protected registry. Tokens and private key material are deliberately
// absent from the returned request.
export function createInstallationEnrollmentRequest({
  tokenSet,
  key,
  enrollmentID = `enrollment_${randomBase64URL(18)}`,
  generation = 1,
} = {}) {
  if (!tokenSet?.oauth || !key) fail('enrollment_request_invalid');
  safeString(enrollmentID, 'wrong_enrollment', { max: 255, pattern: SAFE_ID });
  if (!Number.isSafeInteger(generation) || generation < 1) fail('enrollment_invalid');
  const issuer = pinnedHTTPSURL(tokenSet.oauth.issuer, 'issuer_invalid').toString();
  const clientID = safeString(tokenSet.oauth.clientID, 'client_invalid', { max: 255, pattern: SAFE_ID });
  const subject = safeString(tokenSet.oauth.subject, 'subject_invalid', { max: 255, pattern: SAFE_ID });
  const publicJWK = publicInstallationJWK(key.publicJWK);
  const keyThumbprint = installationKeyThumbprint(publicJWK);
  if (key.keyThumbprint !== keyThumbprint || tokenSet.oauth.tokenKeyThumbprint !== keyThumbprint) {
    fail('installation_key_mismatch');
  }
  const entry = Object.freeze({
    enrollment_id: enrollmentID,
    generation,
    client_id: clientID,
    subject,
    status: 'active',
    public_jwk: Object.freeze({ ...publicJWK }),
  });
  const request = Object.freeze({
    schema: INSTALLATION_ENROLLMENT_REQUEST_SCHEMA,
    issuer,
    enrollment: entry,
  });
  const digest = `sha256:${createHash('sha256').update(JSON.stringify(request)).digest('hex')}`;
  const enrollment = Object.freeze({
    id: enrollmentID,
    status: 'active',
    generation,
    keyThumbprint,
    clientID,
    subject,
    revokedAt: null,
  });
  return Object.freeze({ request, requestDigest: digest, enrollment });
}

function signES256(privateJWK, signingInput) {
  let key;
  try {
    key = createPrivateKey({ key: privateJWK, format: 'jwk' });
  } catch {
    fail('installation_key_invalid');
  }
  return cryptoSign('sha256', signingInput, { key, dsaEncoding: 'ieee-p1363' });
}

export class MemoryCredentialStore {
  constructor() {
    this.values = new Map();
  }

  get(ref) { return this.values.get(ref); }
  set(ref, value) { this.values.set(ref, value); }
  delete(ref) { this.values.delete(ref); }
  readJSON(ref) {
    const value = this.get(ref);
    if (value === undefined) return undefined;
    try { return JSON.parse(value); } catch { fail('credential_invalid'); }
  }
  createSigningKey(ref, record) { this.set(ref, JSON.stringify(record)); }
  createInstallationKey(credentialRef, { keyID = `install_${randomBase64URL(18)}` } = {}) {
    const record = createInstallationKeyRecord({ keyID });
    const ref = keyRef(credentialRef, keyID);
    this.createSigningKey(ref, record);
    return Object.freeze({
      keyID, keyRef: ref, publicJWK: record.publicJWK, keyThumbprint: record.keyThumbprint,
    });
  }
  getSigningPublicJWK(ref) { return this.readJSON(ref)?.publicJWK; }
  sign(ref, input) {
    const record = this.readJSON(ref);
    if (!record?.privateJWK) fail('installation_key_unavailable');
    return signES256(record.privateJWK, input);
  }
  deleteSigningKey(ref) { this.delete(ref); }
}

export class MacOSKeychainCredentialStore {
  constructor({
    spawnSync = nodeSpawnSync,
    service = KEYCHAIN_SERVICE,
    helperPath = PRESENCE_HELPER,
    trustMode = 'production',
    processTimeoutMs = DEFAULT_KEYCHAIN_PROCESS_TIMEOUT_MS,
    signal,
  } = {}) {
    if (!Number.isInteger(processTimeoutMs) || processTimeoutMs < 10 ||
        processTimeoutMs > MAX_KEYCHAIN_PROCESS_TIMEOUT_MS) {
      fail('credential_store_timeout_invalid');
    }
    this.spawnSync = spawnSync;
    this.service = service;
    this.helperPath = helperPath;
    this.trustMode = trustMode;
    this.processTimeoutMs = processTimeoutMs;
    this.signal = signal;
    this.helperVerified = false;
    this.constraintsVerified = new Set();
  }

  processBudget(defaultTimeoutMs, { signal = this.signal, deadlineAt } = {}) {
    if (signal?.aborted) fail('credential_store_aborted');
    let timeout = defaultTimeoutMs;
    if (deadlineAt !== undefined) {
      if (!Number.isFinite(deadlineAt)) fail('credential_store_timeout_invalid');
      timeout = Math.min(timeout, Math.floor(deadlineAt - Date.now()));
    }
    if (!Number.isInteger(timeout) || timeout < 1) fail('credential_store_timeout');
    return { signal, timeout };
  }

  runProcess(command, args, spawnOptions, operation, defaultTimeoutMs) {
    const { signal, timeout } = this.processBudget(defaultTimeoutMs, operation);
    const result = this.spawnSync(command, args, {
      ...spawnOptions,
      timeout,
      // spawnSync waits for a timed-out child to exit. SIGTERM is therefore
      // not a hard wall when a compromised helper ignores it; SIGKILL is.
      killSignal: 'SIGKILL',
      signal,
    });
    if (result?.error?.code === 'ETIMEDOUT' || ['SIGKILL', 'SIGTERM'].includes(result?.signal)) {
      fail('credential_store_timeout');
    }
    if (result?.error?.name === 'AbortError' || signal?.aborted) {
      fail('credential_store_aborted');
    }
    if (operation?.deadlineAt !== undefined && Date.now() >= operation.deadlineAt) {
      fail('credential_store_timeout');
    }
    return result;
  }

  run(args, input, operation) {
    return this.runProcess('/usr/bin/security', args, {
      encoding: 'utf8',
      input,
      stdio: ['pipe', 'pipe', 'pipe'],
      maxBuffer: 512 * 1024,
    }, operation, this.processTimeoutMs);
  }

  get(ref, operation) {
    safeString(ref, 'credential_ref_invalid', { max: 512 });
    const result = this.run(['find-generic-password', '-s', this.service, '-a', ref, '-w'], undefined, operation);
    if (result.status === 44) return undefined;
    if (result.status !== 0) fail('credential_store_unavailable');
    return result.stdout.replace(/\r?\n$/, '');
  }

  set(ref, value, operation) {
    safeString(ref, 'credential_ref_invalid', { max: 512, pattern: SAFE_CREDENTIAL_REF });
    safeString(value, 'credential_invalid', { max: 256 * 1024 });
    try {
      const parsed = JSON.parse(value);
      const containsPrivateKey = (candidate) => candidate && typeof candidate === 'object' && (
        Object.keys(candidate).some((key) => key === 'd' || key === 'privateJWK') ||
        Object.values(candidate).some(containsPrivateKey)
      );
      if (containsPrivateKey(parsed)) fail('private_key_persistence_forbidden');
    } catch (error) {
      if (error instanceof RemoteAuthError) throw error;
      // Non-JSON values are supported for backwards-compatible token storage.
    }
    safeString(this.service, 'credential_store_unavailable', { max: 255, pattern: SAFE_CREDENTIAL_REF });
    // `security -i` reads a command from stdin. Hex data avoids command parsing
    // ambiguity while keeping access/refresh tokens and private keys out of
    // argv and process listings.
    const hexValue = Buffer.from(value).toString('hex');
    const result = this.run(['-i'], `add-generic-password -s ${this.service} -a ${ref} -X ${hexValue} -U\n`, operation);
    if (result.status !== 0) fail('credential_store_unavailable');
    const persisted = this.get(ref, operation);
    if (!safeEqual(persisted, value)) fail('credential_store_unavailable');
  }

  delete(ref, operation) {
    safeString(ref, 'credential_ref_invalid', { max: 512 });
    const result = this.run(['delete-generic-password', '-s', this.service, '-a', ref], undefined, operation);
    if (result.status !== 0 && result.status !== 44) fail('credential_store_unavailable');
  }

  readJSON(ref, operation) {
    const value = this.get(ref, operation);
    if (value === undefined) return undefined;
    try { return JSON.parse(value); } catch { fail('credential_invalid'); }
  }

  verifyHelper(operation) {
    if (this.helperVerified) return;
    if (this.trustMode !== 'test') {
      if (this.helperPath !== PRESENCE_HELPER) fail('installation_key_helper_invalid');
      let link;
      let info;
      try {
        link = lstatSync(this.helperPath);
        info = statSync(this.helperPath);
      } catch { fail('installation_key_unavailable'); }
      if (link.isSymbolicLink() || !info.isFile() || info.uid !== 0 ||
          (info.mode & 0o022) !== 0 || (info.mode & 0o111) === 0) {
        fail('installation_key_helper_invalid');
      }
      const verified = this.runProcess('/usr/bin/codesign', [
        '--verify', '--strict', '--verbose=2', this.helperPath,
      ], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }, operation, 30_000);
      const described = this.runProcess('/usr/bin/codesign', [
        '-d', '--verbose=4', this.helperPath,
      ], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }, operation, 30_000);
      const details = `${described.stdout ?? ''}\n${described.stderr ?? ''}`;
      if (verified.status !== 0 || described.status !== 0 ||
          !details.includes(`Identifier=${PRESENCE_HELPER_IDENTIFIER}`) ||
          !details.includes(`TeamIdentifier=${PRESENCE_HELPER_TEAM_ID}`)) {
        fail('installation_key_helper_invalid');
      }
    }
    this.helperVerified = true;
  }

  runHelper(command, payload, operation) {
    this.verifyHelper(operation);
    const directory = mkdtempSync(join(tmpdir(), 'pulse-dpop-'));
    chmodSync(directory, 0o700);
    const path = join(directory, 'request.json');
    try {
      writeFileSync(path, `${JSON.stringify(payload)}\n`, { mode: 0o600, flag: 'wx' });
      const result = this.runProcess(this.helperPath, [command, '--payload', path], {
        encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], maxBuffer: 16 * 1024,
      }, operation, 30_000);
      if (result.status !== 0 || result.signal || typeof result.stdout !== 'string' ||
          result.stdout.length < 2 || result.stdout.length > 4096) fail('installation_key_unavailable');
      try { return JSON.parse(result.stdout); } catch { fail('installation_key_helper_invalid'); }
    } finally {
      rmSync(directory, { recursive: true, force: true });
    }
  }

  publicResult(value, ref) {
    if (!value || Array.isArray(value) || typeof value !== 'object' ||
        Object.keys(value).sort().join('\0') !== 'key_ref\0public_jwk\0schema' ||
        value.schema !== 'pulse.dpop.public.v1' || value.key_ref !== ref) {
      fail('installation_key_helper_invalid');
    }
    return publicInstallationJWK(value.public_jwk);
  }

  createSigningKey(ref, record = {}, operation) {
    safeString(ref, 'credential_ref_invalid', { max: 512, pattern: SAFE_CREDENTIAL_REF });
    if (record?.privateJWK || record?.d) fail('private_key_persistence_forbidden');
    const constraints = installationConstraints(record.constraints);
    const publicJWK = this.publicResult(this.runHelper('dpop-create', {
      schema: 'pulse.dpop.create.v2', key_ref: ref,
      resource: constraints.resource,
      token_endpoint: constraints.tokenEndpoint,
      client_id: constraints.clientID,
      subject: constraints.subject,
    }, operation), ref);
    if (record?.publicJWK && installationKeyThumbprint(record.publicJWK) !== installationKeyThumbprint(publicJWK)) {
      fail('installation_key_mismatch');
    }
    return publicJWK;
  }

  ensureInstallationConstraints(ref, constraints, expectedPublicJWK, operation) {
    const boundedConstraints = installationConstraints(constraints);
    const cacheKey = `${ref}\0${JSON.stringify(boundedConstraints)}`;
    if (this.constraintsVerified.has(cacheKey)) return;
    this.createSigningKey(ref, { constraints: boundedConstraints, publicJWK: expectedPublicJWK }, operation);
    this.constraintsVerified.add(cacheKey);
  }

  createInstallationKey(credentialRef, {
    keyID = `install_${randomBase64URL(18)}`,
    constraints,
  } = {}) {
    safeString(credentialRef, 'credential_ref_invalid', { max: 512 });
    safeString(keyID, 'installation_key_id_invalid', { max: 255, pattern: SAFE_ID });
    const ref = keyRef(credentialRef, keyID);
    const boundedConstraints = installationConstraints(constraints);
    const publicJWK = this.createSigningKey(ref, { constraints: boundedConstraints });
    this.constraintsVerified.add(`${ref}\0${JSON.stringify(boundedConstraints)}`);
    return Object.freeze({
      keyID, keyRef: ref, publicJWK: Object.freeze(publicJWK),
      keyThumbprint: installationKeyThumbprint(publicJWK),
      constraints: boundedConstraints,
    });
  }

  getSigningPublicJWK(ref, operation) {
    safeString(ref, 'credential_ref_invalid', { max: 512, pattern: SAFE_CREDENTIAL_REF });
    return this.publicResult(this.runHelper('dpop-public', {
      schema: 'pulse.dpop.key_ref.v1', key_ref: ref,
    }, operation), ref);
  }

  createDPoPProof(ref, claims, operation) {
    safeString(ref, 'credential_ref_invalid', { max: 512, pattern: SAFE_CREDENTIAL_REF });
    if (!claims || typeof claims !== 'object' || Array.isArray(claims) ||
        Object.keys(claims).sort().join('\0') !== [
          'ath', 'client_id', 'enrollment_generation', 'enrollment_id', 'htm', 'htu',
          'iat', 'jti', 'key_ref', 'nonce', 'purpose', 'schema', 'sub',
        ].sort().join('\0') || claims.schema !== 'pulse.dpop.proof.v1' || claims.key_ref !== ref) {
      fail('installation_key_invalid');
    }
    const value = this.runHelper('dpop-proof', claims, operation);
    if (!value || Array.isArray(value) || typeof value !== 'object' ||
        Object.keys(value).sort().join('\0') !== 'key_ref\0proof\0schema' ||
        value.schema !== 'pulse.dpop.proof_result.v1' || value.key_ref !== ref ||
        typeof value.proof !== 'string' || value.proof.length > 16 * 1024) {
      fail('installation_key_helper_invalid');
    }
    const parsed = parseCompactJWT(value.proof, 'installation_proof_invalid');
    const publicJWK = verifyES256Proof(parsed);
    const live = this.getSigningPublicJWK(ref, operation);
    if (installationKeyThumbprint(publicJWK) !== installationKeyThumbprint(live)) {
      fail('installation_key_mismatch');
    }
    const expectedPayload = claims.purpose === 'token'
      ? {
        client_id: claims.client_id, htm: claims.htm, htu: claims.htu, iat: claims.iat,
        jti: claims.jti, sub: claims.sub, ...(claims.nonce ? { nonce: claims.nonce } : {}),
      }
      : {
        ath: claims.ath, client_id: claims.client_id,
        enrollment_generation: claims.enrollment_generation, enrollment_id: claims.enrollment_id,
        htm: claims.htm, htu: claims.htu, iat: claims.iat, jti: claims.jti, sub: claims.sub,
      };
    const sorted = (object) => JSON.stringify(Object.fromEntries(Object.entries(object).sort(([a], [b]) => a.localeCompare(b))));
    if (sorted(parsed.payload) !== sorted(expectedPayload)) {
      fail('installation_key_helper_invalid');
    }
    return value.proof;
  }

  sign() {
    fail('arbitrary_signing_forbidden');
  }

  deleteSigningKey(ref, operation) {
    safeString(ref, 'credential_ref_invalid', { max: 512, pattern: SAFE_CREDENTIAL_REF });
    const value = this.runHelper('dpop-delete', {
      schema: 'pulse.dpop.delete.v1', key_ref: ref,
    }, operation);
    if (!value || Array.isArray(value) || typeof value !== 'object' ||
        Object.keys(value).sort().join('\0') !== 'key_ref\0schema' ||
        value.schema !== 'pulse.dpop.deleted.v1' || value.key_ref !== ref) {
      fail('installation_key_helper_invalid');
    }
  }
}

export function createOSCredentialStore(options = {}) {
  if ((options.platform ?? process.platform) !== 'darwin') fail('credential_store_unsupported');
  return new MacOSKeychainCredentialStore(options);
}

function keyRef(credentialRef, keyID) {
  return `${credentialRef}:key:${keyID}`;
}

function installationConstraints(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value) ||
      Object.keys(value).sort().join('\0') !== 'clientID\0resource\0subject\0tokenEndpoint') {
    fail('installation_constraints_invalid');
  }
  const resource = pinnedHTTPSURL(value.resource, 'installation_constraints_invalid');
  if (resource.pathname !== '/mcp' || resource.search || resource.hash) fail('installation_constraints_invalid');
  const tokenEndpoint = pinnedHTTPSURL(value.tokenEndpoint, 'installation_constraints_invalid');
  const clientID = safeString(value.clientID, 'installation_constraints_invalid', { max: 255, pattern: SAFE_ID });
  const subject = safeString(value.subject, 'installation_constraints_invalid', { max: 255, pattern: SAFE_ID });
  return Object.freeze({ resource: resource.toString(), tokenEndpoint: tokenEndpoint.toString(), clientID, subject });
}

export function provisionInstallationKey(store, credentialRef, {
  keyID = `install_${randomBase64URL(18)}`,
  constraints,
} = {}) {
  if (!store || typeof store.createInstallationKey !== 'function') fail('credential_store_unavailable');
  safeString(credentialRef, 'credential_ref_invalid', { max: 512 });
  safeString(keyID, 'installation_key_id_invalid', { max: 255, pattern: SAFE_ID });
  const key = store.createInstallationKey(credentialRef, { keyID, constraints });
  const expectedRef = keyRef(credentialRef, keyID);
  if (!key || key.keyID !== keyID || key.keyRef !== expectedRef || !key.publicJWK ||
      key.keyThumbprint !== installationKeyThumbprint(key.publicJWK)) {
    fail('installation_key_invalid');
  }
  const live = store.getSigningPublicJWK?.(expectedRef);
  if (!live || installationKeyThumbprint(live) !== key.keyThumbprint) fail('installation_key_mismatch');
  return Object.freeze({
    keyID, keyRef: expectedRef, publicJWK: Object.freeze(publicInstallationJWK(key.publicJWK)),
    keyThumbprint: key.keyThumbprint,
    ...(key.constraints ? { constraints: installationConstraints(key.constraints) } : {}),
  });
}

function validateEnrollment(enrollment, oauth, { active = true } = {}) {
  if (!enrollment || typeof enrollment !== 'object') fail('enrollment_invalid');
  safeString(enrollment.id, 'wrong_enrollment', { max: 255, pattern: SAFE_ID });
  if (!Number.isInteger(enrollment.generation) || enrollment.generation < 1) fail('enrollment_invalid');
  safeString(enrollment.keyThumbprint, 'installation_key_mismatch', { max: 128, pattern: /^[A-Za-z0-9_-]+$/ });
  if (enrollment.clientID !== oauth.clientID) fail('client_mismatch');
  if (enrollment.subject !== oauth.subject) fail('subject_mismatch');
  if (active && (enrollment.status !== 'active' || enrollment.revokedAt !== null)) fail('enrollment_revoked');
}

function credentialAuthority(authority, issuer) {
  if (!authority || typeof authority !== 'object' || Array.isArray(authority)) fail('credential_authority_invalid');
  const tokenEndpoint = pinnedHTTPSURL(authority.tokenEndpoint, 'token_endpoint_invalid');
  const jwksURI = pinnedHTTPSURL(authority.jwksURI, 'jwks_uri_invalid');
  const issuerURL = pinnedHTTPSURL(issuer, 'issuer_invalid');
  if (tokenEndpoint.origin !== issuerURL.origin || jwksURI.origin !== issuerURL.origin) {
    fail('provider_origin_mismatch');
  }
  return { tokenEndpoint: tokenEndpoint.toString(), jwksURI: jwksURI.toString() };
}

function credentialDocument(tokenSet, key, enrollment, authority) {
  validateEnrollment(enrollment, tokenSet.oauth);
  if (enrollment.keyThumbprint !== key.keyThumbprint ||
      tokenSet.oauth.tokenKeyThumbprint !== key.keyThumbprint) fail('installation_key_mismatch');
  const boundedAuthority = credentialAuthority(authority, tokenSet.oauth.issuer);
  if (key.constraints) {
    const constraints = installationConstraints(key.constraints);
    if (constraints.resource !== tokenSet.oauth.audience ||
        constraints.tokenEndpoint !== boundedAuthority.tokenEndpoint ||
        constraints.clientID !== tokenSet.oauth.clientID || constraints.subject !== tokenSet.oauth.subject) {
      fail('installation_constraints_mismatch');
    }
  }
  return {
    schema: 'pulse.remote_credential.v2',
    tokens: { ...tokenSet.tokens },
    oauth: { ...tokenSet.oauth },
    authority: boundedAuthority,
    installation: {
      keyID: key.keyID,
      keyRef: '',
      keyThumbprint: key.keyThumbprint,
      publicJWK: { ...key.publicJWK },
    },
    enrollment: { ...enrollment },
  };
}

export function persistRemoteCredential(store, credentialRef, { tokenSet, key, enrollment, authority }) {
  if (!store || typeof store.set !== 'function' || typeof store.createSigningKey !== 'function') fail('credential_store_unavailable');
  safeString(credentialRef, 'credential_ref_invalid', { max: 512 });
  const document = credentialDocument(tokenSet, key, enrollment, authority);
  const expectedKeyRef = keyRef(credentialRef, key.keyID);
  if (key.keyRef !== undefined && key.keyRef !== expectedKeyRef) fail('installation_key_mismatch');
  document.installation.keyRef = expectedKeyRef;
  let createdHere = false;
  if (key.privateJWK) {
    store.createSigningKey(expectedKeyRef, key);
    createdHere = true;
  } else {
    const live = store.getSigningPublicJWK?.(expectedKeyRef);
    if (!live || installationKeyThumbprint(live) !== key.keyThumbprint) fail('installation_key_mismatch');
  }
  try {
    store.set(credentialRef, JSON.stringify(document));
    store.delete?.(refreshStateRef(credentialRef));
  } catch (error) {
    if (createdHere) (store.deleteSigningKey ?? store.delete)?.call(store, document.installation.keyRef);
    throw error;
  }
  return Object.freeze({ ...tokenSet.metadata, enrollmentID: enrollment.id, generation: enrollment.generation });
}

function readCredential(store, credentialRef, now, { allowExpired = false, operation } = {}) {
  if (!store || typeof store.get !== 'function' ||
      (typeof store.createDPoPProof !== 'function' && typeof store.sign !== 'function')) {
    fail('credential_store_unavailable');
  }
  assertRefreshStateClear(store, credentialRef, operation);
  const serialized = store.get(credentialRef, operation);
  if (serialized === undefined) fail('credential_unavailable');
  let credential;
  try { credential = JSON.parse(serialized); } catch { fail('credential_invalid'); }
  if (credential?.schema !== 'pulse.remote_credential.v2') fail('credential_invalid');
  const { oauth, tokens, installation, enrollment, authority } = credential;
  if (!oauth || !tokens || !installation) fail('credential_invalid');
  safeString(tokens.accessToken, 'access_token_missing', { max: 32 * 1024 });
  if (!Number.isInteger(oauth.accessExpiresAt) ||
      (!allowExpired && oauth.accessExpiresAt < now - DEFAULT_CLOCK_SKEW_SECONDS)) {
    fail('access_token_expired');
  }
  validateEnrollment(enrollment, oauth);
  credentialAuthority(authority, oauth.issuer);
  if (installation.keyThumbprint !== enrollment.keyThumbprint ||
      oauth.tokenKeyThumbprint !== installation.keyThumbprint) fail('installation_key_mismatch');
  store.ensureInstallationConstraints?.(installation.keyRef, {
    resource: oauth.audience,
    tokenEndpoint: authority.tokenEndpoint,
    clientID: oauth.clientID,
    subject: oauth.subject,
  }, installation.publicJWK, operation);
  const livePublicJWK = store.getSigningPublicJWK?.(installation.keyRef, operation);
  if (!livePublicJWK) fail('installation_key_unavailable');
  if (installationKeyThumbprint(livePublicJWK) !== installation.keyThumbprint) fail('installation_key_mismatch');
  return credential;
}

function proofTarget(value) {
  const target = strictURL(value, 'remote_url_invalid');
  if (target.protocol !== 'https:' || target.username || target.password) fail('remote_url_invalid');
  target.hash = '';
  target.search = '';
  return target.toString();
}

function createProof({ credential, store, url, method, now, randomBytes = cryptoRandomBytes, operation }) {
  const normalizedMethod = safeString(method.toUpperCase(), 'method_invalid', { max: 16, pattern: SAFE_METHOD });
  const publicJWK = publicInstallationJWK(credential.installation.publicJWK);
  const header = { typ: 'dpop+jwt', alg: 'ES256', jwk: publicJWK };
  const payload = {
    htu: proofTarget(url),
    htm: normalizedMethod,
    iat: now,
    jti: randomBase64URL(24, randomBytes),
    ath: sha256Base64URL(credential.tokens.accessToken),
    enrollment_id: credential.enrollment.id,
    enrollment_generation: credential.enrollment.generation,
    client_id: credential.oauth.clientID,
    sub: credential.oauth.subject,
  };
  if (typeof store.createDPoPProof === 'function') {
    return store.createDPoPProof(credential.installation.keyRef, {
      schema: 'pulse.dpop.proof.v1', key_ref: credential.installation.keyRef, purpose: 'resource',
      htu: payload.htu, htm: payload.htm, iat: payload.iat, jti: payload.jti, nonce: '',
      ath: payload.ath, enrollment_id: payload.enrollment_id,
      enrollment_generation: payload.enrollment_generation,
      client_id: payload.client_id, sub: payload.sub,
    }, operation);
  }
  const input = `${encodeJSON(header)}.${encodeJSON(payload)}`;
  const signature = store.sign(credential.installation.keyRef, Buffer.from(input), operation);
  if (!Buffer.isBuffer(signature) || signature.length !== 64) fail('installation_key_unavailable');
  return `${input}.${signature.toString('base64url')}`;
}

export function buildSenderConstrainedRemoteHeaders(baseURL, {
  method = 'GET',
  credentialStore,
  credentialRef,
  now = Math.floor(Date.now() / 1000),
  randomBytes = cryptoRandomBytes,
  signal,
  deadlineAt,
} = {}) {
  proofTarget(baseURL);
  safeString(credentialRef, 'credential_ref_invalid', { max: 512 });
  const operation = { signal, deadlineAt };
  assertRemoteOperationActive(operation);
  const credential = readCredential(credentialStore, credentialRef, now, { operation });
  const proof = createProof({
    credential, store: credentialStore, url: baseURL, method, now, randomBytes, operation,
  });
  assertRemoteOperationActive(operation);
  return {
    'Content-Type': 'application/json',
    Authorization: `DPoP ${credential.tokens.accessToken}`,
    DPoP: proof,
    'X-Pulse-Enrollment': credential.enrollment.id,
  };
}

// Refreshes a DPoP-bound credential only when it is near expiry. The refresh
// grant itself carries a proof made by the enrolled installation key, and the
// replacement access token must carry the same cnf.jkt. A copied refresh token
// is therefore insufficient without the Keychain-held private key.
export async function refreshRemoteCredential(store, credentialRef, {
  fetch: fetchFn = globalThis.fetch,
  now = Math.floor(Date.now() / 1000),
  minValiditySeconds = 60,
  maxAccessLifetimeSeconds = DEFAULT_MAX_ACCESS_LIFETIME_SECONDS,
  force = false,
  networkTimeoutMs = DEFAULT_NETWORK_TIMEOUT_MS,
  signal,
  deadlineAt,
  onRefreshTransition = () => {},
} = {}) {
  if (typeof fetchFn !== 'function') fail('http_client_unavailable');
  if (!Number.isInteger(minValiditySeconds) || minValiditySeconds < 0 || minValiditySeconds > 300) {
    fail('refresh_configuration_invalid');
  }
  if (typeof force !== 'boolean') fail('refresh_configuration_invalid');
  if (typeof onRefreshTransition !== 'function') fail('refresh_configuration_invalid');
  const operation = { signal, deadlineAt };
  assertRemoteOperationActive(operation);
  const credential = readCredential(store, credentialRef, now, { allowExpired: true, operation });
  if (!force && credential.oauth.accessExpiresAt > now + minValiditySeconds) {
    return Object.freeze({ refreshed: false, accessExpiresAt: credential.oauth.accessExpiresAt });
  }
  const pinnedAuthority = credentialAuthority(credential.authority, credential.oauth.issuer);
  const endpoint = pinnedHTTPSURL(pinnedAuthority.tokenEndpoint, 'token_endpoint_invalid');
  if (endpoint.origin !== pinnedHTTPSURL(credential.oauth.issuer, 'issuer_invalid').origin) {
    fail('provider_origin_mismatch');
  }
  safeString(credential.tokens.refreshToken, 'refresh_token_missing', { max: 32 * 1024 });
  // Fetch and validate the pinned verification set before rotating a refresh
  // token. If JWKS is unavailable we have not consumed a one-time refresh
  // token and can retry safely later.
  let jwksResponse;
  try {
    jwksResponse = await boundedRemoteFetch(fetchFn, pinnedAuthority.jwksURI, {
      method: 'GET', redirect: 'error',
    }, { timeoutMs: networkTimeoutMs, signal });
  } catch (error) {
    if (error instanceof RemoteAuthError) throw error;
    fail('jwks_unavailable');
  }
  if (!jwksResponse?.ok || (jwksResponse.url && jwksResponse.url !== pinnedAuthority.jwksURI)) {
    fail('jwks_unavailable');
  }
  let jwksBytes;
  try {
    jwksBytes = Buffer.from(await boundedRemoteRead(() => jwksResponse.arrayBuffer(), {
      timeoutMs: networkTimeoutMs, signal, cancel: () => jwksResponse.body?.cancel(),
    }));
  } catch (error) {
    if (error instanceof RemoteAuthError) throw error;
    fail('jwks_invalid');
  }
  if (jwksBytes.length < 2 || jwksBytes.length > MAX_JWKS_BYTES) fail('jwks_invalid');
  let jwks;
  try { jwks = JSON.parse(jwksBytes.toString('utf8')); } catch { fail('jwks_invalid'); }
  if (!jwks || typeof jwks !== 'object' || Array.isArray(jwks) || !Array.isArray(jwks.keys)) fail('jwks_invalid');
  // This content-free marker closes the local crash ambiguity. Live Auth0
  // refresh-token reuse/overlap policy is still an external U10 deployment gate.
  const refreshMarker = beginRefreshRotation(store, credentialRef, now, operation);
  try {
    await onRefreshTransition('rotation_marked');
    const submit = async (dpopNonce) => boundedRemoteFetch(fetchFn, endpoint.toString(), {
      method: 'POST',
      headers: {
        'Content-Type': 'application/x-www-form-urlencoded',
        DPoP: createTokenEndpointProof({
          credential,
          store,
          tokenEndpoint: endpoint.toString(),
          nonce: dpopNonce,
          now,
          operation,
        }),
      },
      body: new URLSearchParams({
        grant_type: 'refresh_token',
        client_id: credential.oauth.clientID,
        refresh_token: credential.tokens.refreshToken,
        scope: credential.oauth.scope.join(' '),
      }).toString(),
      redirect: 'error',
    }, { timeoutMs: networkTimeoutMs, signal });
    let response;
    try { response = await submit(); } catch (error) {
      if (error instanceof RemoteAuthError) throw error;
      fail('token_refresh_failed');
    }
    if (!response?.ok) {
      const dpopNonce = response?.headers?.get?.('dpop-nonce') ?? '';
      let rejected;
      try {
        rejected = await boundedRemoteRead(() => response.json(), {
          timeoutMs: networkTimeoutMs, signal, cancel: () => response.body?.cancel(),
        });
      } catch (error) {
        if (error instanceof RemoteAuthError) throw error;
        rejected = undefined;
      }
      if (rejected?.error === 'use_dpop_nonce' && /^[A-Za-z0-9._~-]{16,512}$/.test(dpopNonce)) {
        try { response = await submit(dpopNonce); } catch (error) {
          if (error instanceof RemoteAuthError) throw error;
          fail('token_refresh_failed');
        }
      }
    }
    if (!response?.ok) fail('token_refresh_rejected');
    let body;
    try {
      body = await boundedRemoteRead(() => response.json(), {
        timeoutMs: networkTimeoutMs, signal, cancel: () => response.body?.cancel(),
      });
    } catch (error) {
      if (error instanceof RemoteAuthError) throw error;
      fail('token_response_invalid');
    }
    const tokenSet = validateOAuthRefreshTokenSet(body, {
      existingTokens: credential.tokens,
      issuer: credential.oauth.issuer,
      audience: credential.oauth.audience,
      clientID: credential.oauth.clientID,
      expectedSubject: credential.oauth.subject,
      requiredScopes: credential.oauth.scope.filter((scope) => scope.startsWith('pulse:')),
      allowedScopes: credential.oauth.scope,
      forbiddenScopes: ['pulse:write', 'pulse:delete', 'pulse:owner'],
      jwks,
      now,
      maxAccessLifetimeSeconds,
      installationKeyThumbprint: credential.installation.keyThumbprint,
    });
    if (tokenSet.oauth.issuer !== credential.oauth.issuer ||
        tokenSet.oauth.audience !== credential.oauth.audience ||
        tokenSet.oauth.clientID !== credential.oauth.clientID ||
        tokenSet.oauth.subject !== credential.oauth.subject) fail('refresh_identity_mismatch');
    await onRefreshTransition('provider_rotated');
    assertRemoteOperationActive(operation);
    credential.tokens = { ...tokenSet.tokens };
    credential.oauth = { ...tokenSet.oauth };
    store.set(credentialRef, JSON.stringify(credential), operation);
    await onRefreshTransition('credential_persisted');
    assertRemoteOperationActive(operation);
    clearRefreshRotation(store, credentialRef, operation);
    return Object.freeze({ refreshed: true, accessExpiresAt: tokenSet.oauth.accessExpiresAt });
  } catch (error) {
    try {
      assertRemoteOperationActive(operation);
      requireRefreshReauth(store, credentialRef, refreshMarker, operation);
    } catch { /* rotation_in_progress remains a fail-closed reauth marker */ }
    throw error;
  }
}

function verifyES256Proof(parsed) {
  if (parsed.header.typ !== 'dpop+jwt' || parsed.header.alg !== 'ES256') fail('installation_proof_header_invalid');
  const publicJWK = publicInstallationJWK(parsed.header.jwk);
  let publicKey;
  try { publicKey = createPublicKey({ key: publicJWK, format: 'jwk' }); } catch { fail('installation_key_invalid'); }
  if (!cryptoVerify('sha256', parsed.signingInput, { key: publicKey, dsaEncoding: 'ieee-p1363' }, parsed.signature)) {
    fail('installation_proof_signature_invalid');
  }
  return publicJWK;
}

export function verifyInstallationProof(proof, {
  accessToken,
  url,
  method,
  currentEnrollment,
  expectedClientID,
  expectedSubject,
  now = Math.floor(Date.now() / 1000),
  clockSkewSeconds = DEFAULT_CLOCK_SKEW_SECONDS,
  consumeJTI,
}) {
  safeString(accessToken, 'access_token_missing', { max: 32 * 1024 });
  const parsed = parseCompactJWT(proof, 'installation_proof_invalid');
  const publicJWK = verifyES256Proof(parsed);
  const oauth = { clientID: expectedClientID, subject: expectedSubject };
  validateEnrollment(currentEnrollment, oauth);
  const { payload } = parsed;
  if (payload.htu !== proofTarget(url)) fail('proof_target_mismatch');
  if (payload.htm !== method.toUpperCase()) fail('proof_method_mismatch');
  if (!Number.isInteger(payload.iat) || Math.abs(payload.iat - now) > clockSkewSeconds) fail('proof_clock_invalid');
  safeString(payload.jti, 'proof_jti_invalid', { max: 128, pattern: /^[A-Za-z0-9_-]+$/ });
  if (payload.ath !== sha256Base64URL(accessToken)) fail('proof_token_mismatch');
  if (payload.enrollment_id !== currentEnrollment.id) fail('wrong_enrollment');
  if (payload.enrollment_generation !== currentEnrollment.generation) fail('wrong_enrollment');
  if (payload.client_id !== expectedClientID) fail('client_mismatch');
  if (payload.sub !== expectedSubject) fail('subject_mismatch');
  if (installationKeyThumbprint(publicJWK) !== currentEnrollment.keyThumbprint) fail('installation_key_mismatch');
  if (consumeJTI && consumeJTI(payload.jti, payload.iat) !== true) fail('proof_replayed');
  return Object.freeze({
    enrollmentID: currentEnrollment.id,
    generation: currentEnrollment.generation,
    keyThumbprint: currentEnrollment.keyThumbprint,
    jti: payload.jti,
  });
}

export function activateRotatedRemoteInstallation(store, credentialRef, {
  key,
  enrollment,
  tokenSet,
  now = Math.floor(Date.now() / 1000),
}) {
  const credential = readCredential(store, credentialRef, now);
  if (enrollment.generation !== credential.enrollment.generation + 1) fail('rotation_generation_invalid');
  if (enrollment.id === credential.enrollment.id) fail('wrong_enrollment');
  validateEnrollment(enrollment, credential.oauth);
  if (key.keyThumbprint !== enrollment.keyThumbprint) fail('installation_key_mismatch');
  const rotated = credentialDocument(tokenSet, key, enrollment, credential.authority);
  if (rotated.oauth.issuer !== credential.oauth.issuer ||
      rotated.oauth.audience !== credential.oauth.audience ||
      rotated.oauth.clientID !== credential.oauth.clientID ||
      rotated.oauth.subject !== credential.oauth.subject) fail('rotation_identity_mismatch');
  const oldKeyRef = credential.installation.keyRef;
  const newKeyRef = keyRef(credentialRef, key.keyID);
  if (key.keyRef !== undefined && key.keyRef !== newKeyRef) fail('installation_key_mismatch');
  if (key.privateJWK) {
    store.createSigningKey(newKeyRef, key);
  } else {
    const live = store.getSigningPublicJWK?.(newKeyRef);
    if (!live || installationKeyThumbprint(live) !== key.keyThumbprint) fail('installation_key_mismatch');
  }
  credential.installation = {
    keyID: key.keyID,
    keyRef: newKeyRef,
    keyThumbprint: key.keyThumbprint,
    publicJWK: { ...key.publicJWK },
  };
  credential.tokens = rotated.tokens;
  credential.oauth = rotated.oauth;
  credential.enrollment = { ...enrollment };
  store.set(credentialRef, JSON.stringify(credential));
  (store.deleteSigningKey ?? store.delete)?.call(store, oldKeyRef);
}

export function markRemoteCredentialRevoked(store, credentialRef, { revokedAt = Math.floor(Date.now() / 1000) } = {}) {
  const serialized = store.get(credentialRef);
  if (serialized === undefined) fail('credential_unavailable');
  let credential;
  try { credential = JSON.parse(serialized); } catch { fail('credential_invalid'); }
  if (!Number.isInteger(revokedAt) || revokedAt < 0) fail('revocation_invalid');
  credential.enrollment.status = 'revoked';
  credential.enrollment.revokedAt = revokedAt;
  store.set(credentialRef, JSON.stringify(credential));
}

// Narrow internal seam used by remote-auth-oauth.js. Credential/DPoP stays in
// this module; OAuth owns browser callback and token-validation flow.
export const remoteOAuthPrimitives = Object.freeze({
  DEFAULT_CLOCK_SKEW_SECONDS,
  DEFAULT_MAX_ACCESS_LIFETIME_SECONDS,
  DEFAULT_NETWORK_TIMEOUT_MS,
  configuredScopes,
  createTokenEndpointProof,
  pinnedHTTPSURL,
  randomBase64URL,
  safeEqual,
  safeString,
  scopeSet,
  sha256Base64URL,
  strictURL,
  uniqueParam,
  validateJWTClaims,
  validateScopeAuthority,
});
