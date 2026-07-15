import { randomBytes as cryptoRandomBytes } from 'node:crypto';

import { RemoteAuthError, remoteAuthFail as fail } from './remote-auth-errors.js';
import { boundedRemoteFetch, boundedRemoteRead } from './remote-auth-network.js';
import {
  installationKeyThumbprint,
  remoteOAuthPrimitives,
} from './remote-auth.js';

const CALLBACK_PEERS = new Set(['127.0.0.1', '::1', '::ffff:127.0.0.1']);
const SAFE_ID = /^[A-Za-z0-9][A-Za-z0-9._|:@/-]{0,255}$/;
const {
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
} = remoteOAuthPrimitives;

export function createAuthorizationCodePKCERequest({
  issuer,
  authorizationEndpoint,
  tokenEndpoint,
  audience,
  clientID,
  callbackURL,
  scopes,
  dpopJKT,
  nonce: suppliedNonce,
  randomBytes = cryptoRandomBytes,
}) {
  const issuerURL = pinnedHTTPSURL(issuer, 'issuer_invalid');
  const authorizeURL = pinnedHTTPSURL(authorizationEndpoint, 'authorization_endpoint_invalid');
  const tokenURL = pinnedHTTPSURL(tokenEndpoint, 'token_endpoint_invalid');
  if (authorizeURL.origin !== issuerURL.origin || tokenURL.origin !== issuerURL.origin) fail('provider_origin_mismatch');
  safeString(audience, 'audience_invalid', { max: 512 });
  safeString(clientID, 'client_invalid', { max: 255, pattern: SAFE_ID });
  if (!Array.isArray(scopes) || scopes.length < 2 || new Set(scopes).size !== scopes.length) fail('scope_invalid');
  scopes.forEach((scope) => safeString(scope, 'scope_invalid', { max: 128, pattern: /^[A-Za-z0-9:._/-]+$/ }));
  safeString(dpopJKT, 'installation_key_invalid', { max: 64, pattern: /^[A-Za-z0-9_-]{43}$/ });

  const callback = strictURL(callbackURL, 'callback_invalid');
  if (callback.protocol !== 'http:' || callback.hostname !== '127.0.0.1' ||
      !callback.port || callback.pathname === '/' || callback.search || callback.hash) fail('callback_invalid');
  const codeVerifier = randomBase64URL(64, randomBytes);
  if (codeVerifier.length < 43 || codeVerifier.length > 128) fail('pkce_verifier_invalid');
  const state = randomBase64URL(32, randomBytes);
  const nonce = suppliedNonce === undefined
    ? randomBase64URL(32, randomBytes)
    : safeString(suppliedNonce, 'nonce_invalid', {
        max: 256, pattern: /^[A-Za-z0-9_-]{32,256}$/,
      });
  authorizeURL.searchParams.set('response_type', 'code');
  authorizeURL.searchParams.set('client_id', clientID);
  authorizeURL.searchParams.set('redirect_uri', callback.toString());
  authorizeURL.searchParams.set('audience', audience);
  authorizeURL.searchParams.set('scope', scopes.join(' '));
  authorizeURL.searchParams.set('code_challenge', sha256Base64URL(codeVerifier));
  authorizeURL.searchParams.set('code_challenge_method', 'S256');
  authorizeURL.searchParams.set('state', state);
  authorizeURL.searchParams.set('nonce', nonce);
  authorizeURL.searchParams.set('dpop_jkt', dpopJKT);
  return Object.freeze({
    authorizationURL: authorizeURL.toString(),
    issuer: issuerURL.toString(),
    tokenEndpoint: tokenURL.toString(),
    audience,
    clientID,
    callbackURL: callback.toString(),
    scopes: Object.freeze([...scopes]),
    dpopJKT,
    codeVerifier,
    state,
    nonce,
  });
}

export function validateAuthorizationCallback(callbackURL, pending, request = {}) {
  let callback;
  try {
    callback = new URL(callbackURL);
  } catch {
    fail('callback_invalid');
  }
  const expected = strictURL(pending?.callbackURL, 'callback_invalid');
  if (callback.origin !== expected.origin || callback.pathname !== expected.pathname || callback.hash) {
    fail('callback_target_mismatch');
  }
  if (request.method !== 'GET') fail('callback_method_invalid');
  if (request.host !== expected.host) fail('callback_host_mismatch');
  if (!CALLBACK_PEERS.has(request.remoteAddress)) fail('callback_peer_invalid');
  if (callback.searchParams.has('error')) fail('authorization_denied');
  const state = uniqueParam(callback.searchParams, 'state', 'state_invalid');
  if (!safeEqual(state, pending.state)) fail('state_mismatch');
  return uniqueParam(callback.searchParams, 'code', 'authorization_code_invalid');
}

export function validateOAuthTokenSet(response, {
  issuer,
  audience,
  clientID,
  expectedSubject,
  nonce,
  requiredScopes = [],
  allowedScopes,
  forbiddenScopes = [],
  jwks,
  now = Math.floor(Date.now() / 1000),
  clockSkewSeconds = DEFAULT_CLOCK_SKEW_SECONDS,
  maxAccessLifetimeSeconds = DEFAULT_MAX_ACCESS_LIFETIME_SECONDS,
  installationKeyThumbprint: expectedKeyThumbprint,
}) {
  if (!response || typeof response !== 'object' || Array.isArray(response)) fail('token_response_invalid');
  if (response.token_type !== 'DPoP') fail('token_type_invalid');
  safeString(expectedKeyThumbprint, 'installation_key_invalid', {
    max: 64, pattern: /^[A-Za-z0-9_-]{43}$/,
  });
  safeString(response.access_token, 'access_token_missing', { max: 32 * 1024 });
  safeString(response.refresh_token, 'refresh_token_missing', { max: 32 * 1024 });
  safeString(response.id_token, 'id_token_missing', { max: 32 * 1024 });
  if (!Number.isInteger(response.expires_in) || response.expires_in <= 0 ||
      response.expires_in > maxAccessLifetimeSeconds + clockSkewSeconds) fail('token_expiry_invalid');
  const required = [...configuredScopes(requiredScopes)];
  const approved = allowedScopes === undefined ? undefined : [...configuredScopes(allowedScopes)];
  const responseScopes = response.scope === undefined && approved ? new Set(approved) : scopeSet(response.scope);
  validateScopeAuthority(responseScopes, {
    requiredScopes: required, allowedScopes: approved, forbiddenScopes,
  });
  const idClaims = validateJWTClaims(response.id_token, {
    jwks, issuer, audience: clientID, clientID, expectedSubject, nonce,
    now, clockSkewSeconds, maxLifetimeSeconds: undefined,
  });
  const accessClaims = validateJWTClaims(response.access_token, {
    jwks, issuer, audience, clientID, expectedSubject, requiredScopes: required,
    allowedScopes: approved, forbiddenScopes,
    now, clockSkewSeconds, maxLifetimeSeconds: maxAccessLifetimeSeconds, accessToken: true,
  });
  if (!accessClaims.cnf || typeof accessClaims.cnf !== 'object' || Array.isArray(accessClaims.cnf) ||
      Object.keys(accessClaims.cnf).length !== 1 || !safeEqual(accessClaims.cnf.jkt, expectedKeyThumbprint)) {
    fail('access_token_key_mismatch');
  }
  if (accessClaims.sub !== idClaims.sub) fail('subject_mismatch');
  if (Math.abs((accessClaims.exp - now) - response.expires_in) > clockSkewSeconds) fail('token_expiry_mismatch');
  const scope = [...responseScopes].sort();
  const oauth = Object.freeze({
    issuer,
    audience,
    clientID,
    subject: accessClaims.sub,
    scope,
    accessExpiresAt: accessClaims.exp,
    tokenKeyThumbprint: expectedKeyThumbprint,
  });
  return Object.freeze({
    tokens: Object.freeze({
      accessToken: response.access_token,
      refreshToken: response.refresh_token,
      idToken: response.id_token,
    }),
    oauth,
    metadata: Object.freeze({ ...oauth }),
  });
}

export async function exchangeAuthorizationCode({
  pending,
  callbackURL,
  callbackRequest,
  fetch: fetchFn = globalThis.fetch,
  installationKey,
  credentialStore,
  validation = {},
  networkTimeoutMs = DEFAULT_NETWORK_TIMEOUT_MS,
  signal,
}) {
  if (typeof fetchFn !== 'function') fail('http_client_unavailable');
  if (!installationKey?.publicJWK) fail('installation_key_invalid');
  if (!installationKey.privateJWK) {
    if (!installationKey.keyRef ||
        (typeof credentialStore?.createDPoPProof !== 'function' && typeof credentialStore?.sign !== 'function') ||
        typeof credentialStore?.getSigningPublicJWK !== 'function') fail('installation_key_unavailable');
    const live = credentialStore.getSigningPublicJWK(installationKey.keyRef);
    if (!live || installationKeyThumbprint(live) !== installationKey.keyThumbprint) {
      fail('installation_key_mismatch');
    }
  }
  if (!safeEqual(pending?.dpopJKT, installationKey.keyThumbprint)) fail('installation_key_mismatch');
  const code = validateAuthorizationCallback(callbackURL, pending, callbackRequest);
  const submit = async (dpopNonce) => boundedRemoteFetch(fetchFn, pending.tokenEndpoint, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/x-www-form-urlencoded',
      DPoP: createTokenEndpointProof({
        key: installationKey,
        store: credentialStore,
        tokenEndpoint: pending.tokenEndpoint,
        nonce: dpopNonce,
      }),
    },
    body: new URLSearchParams({
      grant_type: 'authorization_code',
      client_id: pending.clientID,
      redirect_uri: pending.callbackURL,
      code,
      code_verifier: pending.codeVerifier,
    }).toString(),
    redirect: 'error',
  }, { timeoutMs: networkTimeoutMs, signal });
  let response;
  try {
    response = await submit();
  } catch (error) {
    if (error instanceof RemoteAuthError) throw error;
    fail('token_exchange_failed');
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
        fail('token_exchange_failed');
      }
    }
  }
  if (!response?.ok) fail('token_exchange_rejected');
  let body;
  try {
    body = await boundedRemoteRead(() => response.json(), {
      timeoutMs: networkTimeoutMs, signal, cancel: () => response.body?.cancel(),
    });
  } catch (error) {
    if (error instanceof RemoteAuthError) throw error;
    fail('token_response_invalid');
  }
  return validateOAuthTokenSet(body, {
    issuer: pending.issuer,
    audience: pending.audience,
    clientID: pending.clientID,
    nonce: pending.nonce,
    installationKeyThumbprint: installationKey.keyThumbprint,
    ...validation,
  });
}
