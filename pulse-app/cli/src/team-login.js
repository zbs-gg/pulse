import { randomBytes } from 'node:crypto';
import { closeSync, lstatSync, mkdirSync, openSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
import { dirname, isAbsolute, resolve } from 'node:path';

import {
  createInstallationEnrollmentRequest,
  persistRemoteCredential,
  provisionInstallationKey,
} from './remote-auth.js';
import { RemoteAuthError } from './remote-auth-errors.js';
import { boundedRemoteFetch, boundedRemoteRead } from './remote-auth-network.js';
import {
  createAuthorizationCodePKCERequest,
  exchangeAuthorizationCode,
} from './remote-auth-oauth.js';

const PROFILE_SCHEMA = 'pulse.team.auth_profile.v1';
const PROFILE_FIELDS = Object.freeze([
  'schema', 'issuer', 'authorization_endpoint', 'token_endpoint', 'jwks_uri',
  'audience', 'client_id', 'expected_subject', 'scopes',
]);
const MAX_PROFILE_BYTES = 64 * 1024;
const MAX_JWKS_BYTES = 256 * 1024;
const SAFE_ID = /^[A-Za-z0-9][A-Za-z0-9._|:@/-]{0,255}$/;
const REQUIRED_INSTALLATION_SCOPES = Object.freeze([
  'openid', 'offline_access', 'pulse:connect', 'pulse:status', 'pulse:read', 'pulse:audit',
]);
const FORBIDDEN_INSTALLATION_SCOPES = new Set(['pulse:write', 'pulse:delete', 'pulse:owner']);

function invalid(code) {
  throw new Error(`team_login_${code}`);
}

function exactObject(value, fields) {
  if (!value || typeof value !== 'object' || Array.isArray(value) ||
      Object.keys(value).sort().join('\0') !== [...fields].sort().join('\0')) invalid('profile_invalid');
  return value;
}

function exactString(value, code, { max = 2048, pattern } = {}) {
  if (typeof value !== 'string' || value === '' || value.length > max || value.trim() !== value ||
      /[\u0000-\u001f\u007f]/.test(value) || (pattern && !pattern.test(value))) invalid(code);
  return value;
}

function httpsURL(value, code) {
  let url;
  try { url = new URL(exactString(value, code)); } catch { invalid(code); }
  if (url.protocol !== 'https:' || url.username || url.password || url.search || url.hash) invalid(code);
  return url;
}

export function readTeamAuthProfile(path, {
  trustMode = process.env.PULSE_TRUST_MODE === 'test' ? 'test' : 'production',
  effectiveUID = typeof process.geteuid === 'function' ? process.geteuid() : undefined,
} = {}) {
  if (!isAbsolute(path)) invalid('profile_path_invalid');
  const absolute = resolve(path);
  const link = lstatSync(absolute);
  const info = statSync(absolute);
  const expectedUID = trustMode === 'test' ? effectiveUID : 0;
  if (link.isSymbolicLink() || !info.isFile() || info.size < 2 || info.size > MAX_PROFILE_BYTES ||
      (info.mode & 0o022) !== 0 || !Number.isInteger(expectedUID) || info.uid !== expectedUID) {
    invalid('profile_unsafe');
  }
  let value;
  try { value = JSON.parse(readFileSync(absolute, 'utf8')); } catch { invalid('profile_invalid'); }
  exactObject(value, PROFILE_FIELDS);
  if (value.schema !== PROFILE_SCHEMA) invalid('profile_invalid');
  const issuer = httpsURL(value.issuer, 'issuer_invalid');
  const authorization = httpsURL(value.authorization_endpoint, 'authorization_endpoint_invalid');
  const token = httpsURL(value.token_endpoint, 'token_endpoint_invalid');
  const jwks = httpsURL(value.jwks_uri, 'jwks_uri_invalid');
  if ([authorization, token, jwks].some((url) => url.origin !== issuer.origin)) invalid('provider_origin_mismatch');
  const audience = httpsURL(value.audience, 'audience_invalid').toString();
  const clientID = exactString(value.client_id, 'client_invalid', { max: 255, pattern: SAFE_ID });
  const expectedSubject = exactString(value.expected_subject, 'subject_invalid', { max: 255, pattern: SAFE_ID });
  if (!Array.isArray(value.scopes) || value.scopes.length < 3 || value.scopes.length > 16 ||
      new Set(value.scopes).size !== value.scopes.length) invalid('scope_invalid');
  const scopes = value.scopes.map((scope) => exactString(scope, 'scope_invalid', {
    max: 128, pattern: /^[A-Za-z0-9:._/-]+$/,
  }));
  if (REQUIRED_INSTALLATION_SCOPES.some((scope) => !scopes.includes(scope)) ||
      scopes.some((scope) => FORBIDDEN_INSTALLATION_SCOPES.has(scope))) invalid('scope_invalid');
  return Object.freeze({
    schema: PROFILE_SCHEMA,
    issuer: issuer.toString(),
    authorizationEndpoint: authorization.toString(),
    tokenEndpoint: token.toString(),
    jwksURI: jwks.toString(),
    audience,
    clientID,
    expectedSubject,
    scopes: Object.freeze(scopes),
  });
}

async function fetchJWKS(profile, fetchFn, { networkTimeoutMs, signal } = {}) {
  let response;
  try {
    response = await boundedRemoteFetch(fetchFn, profile.jwksURI, {
      method: 'GET', redirect: 'error',
    }, { timeoutMs: networkTimeoutMs, signal });
  } catch (error) {
    if (error instanceof RemoteAuthError) throw error;
    invalid('jwks_unavailable');
  }
  if (!response?.ok || response.url && response.url !== profile.jwksURI) invalid('jwks_unavailable');
  let bytes;
  try {
    bytes = Buffer.from(await boundedRemoteRead(() => response.arrayBuffer(), {
      timeoutMs: networkTimeoutMs, signal, cancel: () => response.body?.cancel(),
    }));
  } catch (error) {
    if (error instanceof RemoteAuthError) throw error;
    invalid('jwks_invalid');
  }
  if (bytes.length < 2 || bytes.length > MAX_JWKS_BYTES) invalid('jwks_invalid');
  let jwks;
  try { jwks = JSON.parse(bytes.toString('utf8')); } catch { invalid('jwks_invalid'); }
  if (!jwks || typeof jwks !== 'object' || Array.isArray(jwks) || !Array.isArray(jwks.keys)) invalid('jwks_invalid');
  return jwks;
}

function callbackReceiver({ timeoutMs = 180_000 } = {}) {
  const callbackPath = `/pulse/oauth/callback-${randomBytes(18).toString('base64url')}`;
  let settle;
  let reject;
  let done = false;
  const received = new Promise((resolvePromise, rejectPromise) => {
    settle = resolvePromise;
    reject = rejectPromise;
  });
  const server = createServer((request, response) => {
    const host = typeof request.headers.host === 'string' ? request.headers.host : '';
    let parsed;
    try { parsed = new URL(request.url ?? '', `http://${host}`); } catch {
      response.writeHead(400).end('Invalid callback.');
      return;
    }
    if (done || request.method !== 'GET' || parsed.pathname !== callbackPath) {
      response.writeHead(404, { 'Content-Type': 'text/plain; charset=utf-8' }).end('Not found.');
      return;
    }
    done = true;
    response.writeHead(200, {
      'Content-Type': 'text/plain; charset=utf-8',
      'Cache-Control': 'no-store',
      'X-Content-Type-Options': 'nosniff',
    }).end('Pulse received the authorization response. Return to the terminal.');
    settle({
      callbackURL: parsed.toString(),
      callbackRequest: {
        method: request.method,
        host,
        remoteAddress: request.socket.remoteAddress ?? '',
      },
    });
  });
  server.headersTimeout = 5_000;
  server.requestTimeout = 5_000;
  server.maxRequestsPerSocket = 4;

  return new Promise((resolvePromise, rejectPromise) => {
    server.once('error', rejectPromise);
    server.listen(0, '127.0.0.1', () => {
      const address = server.address();
      if (!address || typeof address === 'string') {
        server.close();
        rejectPromise(new Error('team_login_callback_unavailable'));
        return;
      }
      const timer = setTimeout(() => {
        if (!done) reject(new Error('team_login_callback_timeout'));
        server.close();
      }, timeoutMs);
      timer.unref?.();
      resolvePromise({
        callbackURL: `http://127.0.0.1:${address.port}${callbackPath}`,
        wait: () => received.finally(() => {
          clearTimeout(timer);
          server.close();
        }),
        close: () => {
          clearTimeout(timer);
          server.close();
        },
      });
    });
  });
}

function writeEnrollmentRequest(path, request) {
  if (!isAbsolute(path)) invalid('output_path_invalid');
  const absolute = resolve(path);
  mkdirSync(dirname(absolute), { recursive: true, mode: 0o700 });
  let fd;
  try {
    fd = openSync(absolute, 'wx', 0o600);
    writeFileSync(fd, `${JSON.stringify(request, null, 2)}\n`, 'utf8');
  } catch {
    invalid('output_unavailable');
  } finally {
    if (fd !== undefined) closeSync(fd);
  }
  return absolute;
}

export async function runTeamLogin({
  profile,
  binding,
  credentialStore,
  outputPath,
  openAuthorizationURL,
  fetch: fetchFn = globalThis.fetch,
  timeoutMs = 180_000,
  networkTimeoutMs = 10_000,
  signal,
} = {}) {
  if (!binding || binding.mode !== 'team' || !binding.commons || binding.fallback !== false) invalid('binding_required');
  if (profile.audience !== binding.commons.resource) invalid('audience_binding_mismatch');
  if (typeof binding.commons.credential_ref !== 'string' || binding.commons.credential_ref === '') invalid('binding_required');
  if (typeof openAuthorizationURL !== 'function' || typeof fetchFn !== 'function') invalid('runtime_unavailable');
  const jwks = await fetchJWKS(profile, fetchFn, { networkTimeoutMs, signal });
  const receiver = await callbackReceiver({ timeoutMs });
  let key;
  let credentialPersisted = false;
  try {
    key = provisionInstallationKey(credentialStore, binding.commons.credential_ref, {
      constraints: {
        resource: profile.audience,
        tokenEndpoint: profile.tokenEndpoint,
        clientID: profile.clientID,
        subject: profile.expectedSubject,
      },
    });
    const pending = createAuthorizationCodePKCERequest({
      issuer: profile.issuer,
      authorizationEndpoint: profile.authorizationEndpoint,
      tokenEndpoint: profile.tokenEndpoint,
      audience: profile.audience,
      clientID: profile.clientID,
      callbackURL: receiver.callbackURL,
      scopes: profile.scopes,
      dpopJKT: key.keyThumbprint,
    });
    await openAuthorizationURL(pending.authorizationURL);
    const callback = await receiver.wait();
    const tokenSet = await exchangeAuthorizationCode({
      pending,
      callbackURL: callback.callbackURL,
      callbackRequest: callback.callbackRequest,
      fetch: fetchFn,
      installationKey: key,
      credentialStore,
      validation: {
        expectedSubject: profile.expectedSubject,
        requiredScopes: profile.scopes,
        allowedScopes: profile.scopes,
        forbiddenScopes: [...FORBIDDEN_INSTALLATION_SCOPES],
        jwks,
      },
      networkTimeoutMs,
      signal,
    });
    const proposed = createInstallationEnrollmentRequest({ tokenSet, key });
    const writtenPath = writeEnrollmentRequest(outputPath, proposed.request);
    try {
      persistRemoteCredential(credentialStore, binding.commons.credential_ref, {
        tokenSet,
        key,
        enrollment: proposed.enrollment,
        authority: {
          tokenEndpoint: profile.tokenEndpoint,
          jwksURI: profile.jwksURI,
        },
      });
      credentialPersisted = true;
    } catch (error) {
      rmSync(writtenPath, { force: true });
      throw error;
    }
    return Object.freeze({
      credentialRef: binding.commons.credential_ref,
      enrollmentID: proposed.enrollment.id,
      requestDigest: proposed.requestDigest,
      requestPath: writtenPath,
      status: 'pending_owner_registry_approval',
    });
  } finally {
    if (key && !credentialPersisted) credentialStore?.deleteSigningKey?.(key.keyRef);
    receiver.close();
  }
}
