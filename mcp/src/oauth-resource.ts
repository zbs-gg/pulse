import { isIP } from 'node:net';
import { request as httpsRequest } from 'node:https';

import {
  createLocalJWKSet,
  decodeProtectedHeader,
  errors as joseErrors,
  jwtVerify,
  type JSONWebKeySet,
  type JWTPayload,
} from 'jose';

import { TEAM_CAPABILITIES, type TeamCapability } from './team-contracts.js';

export interface VerifiedOAuthIdentity {
  issuer: string;
  subject: string;
  clientId: string;
  capabilities: TeamCapability[];
  authTime?: number;
}

export type OAuthSecurityReason =
  | 'missing_credential' | 'malformed_credential' | 'invalid_credential'
  | 'expired_credential' | 'credential_not_yet_valid' | 'issuer_mismatch'
  | 'audience_mismatch' | 'incomplete_claims' | 'unknown_signing_key'
  | 'insufficient_scope';

export class OAuthResourceError extends Error {
  readonly status: 401 | 403;
  readonly code: 'invalid_token' | 'insufficient_scope';
  readonly requiredCapabilities: TeamCapability[];
  readonly reasonCode: OAuthSecurityReason;

  constructor(
    code: 'invalid_token' | 'insufficient_scope',
    requiredCapabilities: readonly TeamCapability[] = [],
    reasonCode: OAuthSecurityReason = code === 'insufficient_scope' ? 'insufficient_scope' : 'invalid_credential',
  ) {
    super(code === 'invalid_token' ? 'OAuth access token rejected' : 'OAuth capability is required');
    this.name = 'OAuthResourceError';
    this.status = code === 'invalid_token' ? 401 : 403;
    this.code = code;
    this.requiredCapabilities = [...requiredCapabilities].sort();
    this.reasonCode = reasonCode;
  }
}

export interface OAuthResourceVerifierOptions {
  issuer: string;
  resource: string;
  algorithms?: readonly string[];
  maxTokenLifetimeSeconds?: number;
  clockToleranceSeconds?: number;
  cacheTtlSeconds?: number;
  staleIfErrorSeconds?: number;
  fetch?: typeof fetch;
  resolveHost?: (hostname: string) => Promise<readonly string[]>;
  now?: () => number;
  fetchTimeoutMs?: number;
  maxMetadataBytes?: number;
  maxJwksBytes?: number;
}

interface CachedJwks {
  loadedAt: number;
  value: JSONWebKeySet;
}

interface AuthorizationServerMetadata {
  issuer: string;
  jwks_uri: string;
  code_challenge_methods_supported: string[];
}

const MAX_AUTHORIZATION_BYTES = 16 * 1024;
const MAX_TOKEN_HEADER_BYTES = 4 * 1024;
const MAX_SCOPE_BYTES = 2 * 1024;
const MAX_SCOPES = 64;
const MAX_JWKS_KEYS = 16;

export function protectedResourceMetadata(resource: string, issuer: string) {
  return {
    resource,
    authorization_servers: [issuer],
    bearer_methods_supported: ['header'],
    scopes_supported: [...TEAM_CAPABILITIES],
  };
}

export class OAuthResourceVerifier {
  private readonly issuer: string;
  private readonly resource: string;
  private readonly algorithms: string[];
  private readonly maxTokenLifetimeSeconds: number;
  private readonly clockToleranceSeconds: number;
  private readonly cacheTtlSeconds: number;
  private readonly staleIfErrorSeconds: number;
  private readonly fetcher?: typeof fetch;
  private readonly resolveHost: (hostname: string) => Promise<readonly string[]>;
  private readonly now: () => number;
  private readonly fetchTimeoutMs: number;
  private readonly maxMetadataBytes: number;
  private readonly maxJwksBytes: number;
  private metadata?: AuthorizationServerMetadata;
  private jwks?: CachedJwks;
  private initialJwksLoad?: Promise<JSONWebKeySet>;
  private forcedJwksRefresh?: Promise<JSONWebKeySet>;
  private lastForcedRefresh = Number.NEGATIVE_INFINITY;
  private readonly negativeKids = new Map<string, number>();

  constructor(options: OAuthResourceVerifierOptions) {
    assertPinnedHTTPSURL(options.issuer, 'issuer');
    assertPinnedHTTPSURL(options.resource, 'resource');
    this.issuer = options.issuer;
    this.resource = options.resource;
    this.algorithms = [...(options.algorithms ?? ['RS256'])];
    if (this.algorithms.length === 0 || this.algorithms.length > 4 || this.algorithms.some((value) => !/^[A-Z0-9-]{3,16}$/.test(value))) {
      throw new Error('OAuth algorithm allowlist is invalid');
    }
    this.maxTokenLifetimeSeconds = options.maxTokenLifetimeSeconds ?? 900;
    if (!Number.isInteger(this.maxTokenLifetimeSeconds) || this.maxTokenLifetimeSeconds < 1 || this.maxTokenLifetimeSeconds > 900) {
      throw new Error('OAuth token lifetime must be between 1 and 900 seconds');
    }
    this.clockToleranceSeconds = options.clockToleranceSeconds ?? 30;
    if (!Number.isInteger(this.clockToleranceSeconds) || this.clockToleranceSeconds < 0 || this.clockToleranceSeconds > 30) {
      throw new Error('OAuth clock tolerance must be between 0 and 30 seconds');
    }
    this.cacheTtlSeconds = boundedInteger(options.cacheTtlSeconds ?? 300, 1, 300, 'JWKS cache TTL');
    this.staleIfErrorSeconds = boundedInteger(options.staleIfErrorSeconds ?? 600, 0, 900, 'JWKS stale window');
    this.fetcher = options.fetch;
    this.resolveHost = options.resolveHost ?? resolvePublicHost;
    this.now = options.now ?? (() => Math.floor(Date.now() / 1000));
    this.fetchTimeoutMs = boundedInteger(options.fetchTimeoutMs ?? 2_000, 100, 5_000, 'OAuth fetch timeout');
    this.maxMetadataBytes = boundedInteger(options.maxMetadataBytes ?? 64 * 1024, 1024, 64 * 1024, 'metadata size');
    this.maxJwksBytes = boundedInteger(options.maxJwksBytes ?? 128 * 1024, 1024, 128 * 1024, 'JWKS size');
  }

  async verifyAuthorization(
    authorization: string | string[] | undefined,
    requiredCapabilities: readonly TeamCapability[],
  ): Promise<VerifiedOAuthIdentity> {
    const token = parseBearerAuthorization(authorization);
    let header: ReturnType<typeof decodeProtectedHeader>;
    try {
      if (Buffer.byteLength(token.split('.')[0] ?? '', 'utf8') > MAX_TOKEN_HEADER_BYTES) {
        throw new Error('header too large');
      }
      header = decodeProtectedHeader(token);
    } catch {
      throw new OAuthResourceError('invalid_token', [], 'malformed_credential');
    }
    if (
      (header.typ !== 'at+jwt' && header.typ !== 'application/at+jwt') ||
      typeof header.alg !== 'string' ||
      !this.algorithms.includes(header.alg) ||
      typeof header.kid !== 'string' ||
      header.kid.length < 1 ||
      header.kid.length > 128
    ) {
      throw new OAuthResourceError('invalid_token', [], 'malformed_credential');
    }
    if ((this.negativeKids.get(header.kid) ?? 0) > this.now()) {
      throw new OAuthResourceError('invalid_token', [], 'unknown_signing_key');
    }

    let verified: { payload: JWTPayload };
    try {
      const jwks = await this.loadJwks(false);
      verified = await this.verifyWith(token, jwks);
    } catch (error) {
      if (!isUnknownKid(error)) {
        throw new OAuthResourceError('invalid_token', [], oauthReasonForError(error));
      }
      try {
        const refreshed = await this.refreshForUnknownKid(header.kid);
        verified = await this.verifyWith(token, refreshed);
      } catch (refreshError) {
        if (isUnknownKid(refreshError)) this.rememberUnknownKid(header.kid);
        throw new OAuthResourceError('invalid_token', [], 'unknown_signing_key');
      }
    }

    const identity = this.validatePayload(verified.payload);
    const missing = [...new Set(requiredCapabilities)].filter(
      (capability) => !identity.capabilities.includes(capability),
    );
    if (missing.length > 0) {
      throw new OAuthResourceError('insufficient_scope', missing);
    }
    return identity;
  }

  private async verifyWith(token: string, jwks: JSONWebKeySet) {
    return jwtVerify(token, createLocalJWKSet(jwks), {
      algorithms: this.algorithms,
      audience: this.resource,
      issuer: this.issuer,
      requiredClaims: ['iss', 'sub', 'aud', 'exp', 'iat', 'jti', 'client_id', 'scope'],
      clockTolerance: this.clockToleranceSeconds,
      currentDate: new Date(this.now() * 1000),
    });
  }

  private validatePayload(payload: JWTPayload): VerifiedOAuthIdentity {
    const now = this.now();
    if (
      payload.iss !== this.issuer ||
      payload.aud !== this.resource ||
      !isBoundedIdentityString(payload.sub) ||
      !isBoundedString(payload.jti, 1, 512) ||
      !isBoundedIdentityString(payload.client_id) ||
      typeof payload.iat !== 'number' || !Number.isInteger(payload.iat) ||
      typeof payload.exp !== 'number' || !Number.isInteger(payload.exp) ||
      payload.exp <= payload.iat ||
      payload.exp - payload.iat > this.maxTokenLifetimeSeconds ||
      payload.iat > now + this.clockToleranceSeconds ||
      (payload.auth_time !== undefined &&
        (!Number.isInteger(payload.auth_time) || (payload.auth_time as number) <= 0 ||
          (payload.auth_time as number) > now + this.clockToleranceSeconds)) ||
      (payload.nbf !== undefined && (!Number.isInteger(payload.nbf) || payload.nbf > now + this.clockToleranceSeconds)) ||
      !isBoundedString(payload.scope, 1, MAX_SCOPE_BYTES)
    ) {
      throw new OAuthResourceError('invalid_token');
    }
    const scopes = payload.scope.split(/\s+/).filter(Boolean);
    if (
      scopes.length === 0 ||
      scopes.length > MAX_SCOPES ||
      scopes.some((scope) => !/^[A-Za-z0-9._:-]{1,128}$/.test(scope))
    ) {
      throw new OAuthResourceError('invalid_token');
    }
    const capabilities = [...new Set(scopes.filter(isTeamCapability))].sort() as TeamCapability[];
    return {
      issuer: payload.iss,
      subject: payload.sub,
      clientId: payload.client_id,
      capabilities,
      ...(payload.auth_time === undefined ? {} : { authTime: payload.auth_time as number }),
    };
  }

  private async loadJwks(force: boolean): Promise<JSONWebKeySet> {
    const now = this.now();
    if (!force && this.jwks && now - this.jwks.loadedAt <= this.cacheTtlSeconds) {
      return this.jwks.value;
    }
    const slot = force ? this.forcedJwksRefresh : this.initialJwksLoad;
    if (slot) return slot;
    const load = this.fetchJwks(force);
    if (force) this.forcedJwksRefresh = load;
    else this.initialJwksLoad = load;
    try {
      return await load;
    } finally {
      if (force) this.forcedJwksRefresh = undefined;
      else this.initialJwksLoad = undefined;
    }
  }

  private async fetchJwks(force: boolean): Promise<JSONWebKeySet> {
    const now = this.now();
    try {
      const metadata = this.metadata ?? await this.loadMetadata();
      const value = await fetchBoundedJSON(
        metadata.jwks_uri,
        this.maxJwksBytes,
        this.fetchTimeoutMs,
        this.fetcher,
        this.resolveHost,
      );
      const jwks = validateJwks(value, this.algorithms);
      this.jwks = { loadedAt: now, value: jwks };
      return jwks;
    } catch (error) {
      if (!force && this.jwks && now - this.jwks.loadedAt <= this.cacheTtlSeconds + this.staleIfErrorSeconds) {
        return this.jwks.value;
      }
      throw error;
    }
  }

  private async refreshForUnknownKid(kid: string): Promise<JSONWebKeySet> {
    const now = this.now();
    if ((this.negativeKids.get(kid) ?? 0) > now) {
      throw new joseErrors.JWKSNoMatchingKey();
    }
    if (this.forcedJwksRefresh) return this.forcedJwksRefresh;
    if (now - this.lastForcedRefresh < 5) {
      this.rememberUnknownKid(kid);
      throw new joseErrors.JWKSNoMatchingKey();
    }
    this.lastForcedRefresh = now;
    return this.loadJwks(true);
  }

  private rememberUnknownKid(kid: string): void {
    if (this.negativeKids.size >= 64) {
      const oldest = this.negativeKids.keys().next().value;
      if (oldest !== undefined) this.negativeKids.delete(oldest);
    }
    this.negativeKids.set(kid, this.now() + 30);
  }

  private async loadMetadata(): Promise<AuthorizationServerMetadata> {
    const issuer = new URL(this.issuer);
    const metadataURL = new URL(
      `/.well-known/oauth-authorization-server${issuer.pathname === '/' ? '' : issuer.pathname}`,
      issuer.origin,
    ).toString();
    const value = await fetchBoundedJSON(
      metadataURL,
      this.maxMetadataBytes,
      this.fetchTimeoutMs,
      this.fetcher,
      this.resolveHost,
    );
    if (!value || typeof value !== 'object') {
      throw new Error('Authorization Server metadata is invalid');
    }
    const record = value as Record<string, unknown>;
    if (
      record.issuer !== this.issuer ||
      typeof record.jwks_uri !== 'string' ||
      !Array.isArray(record.code_challenge_methods_supported) ||
      !record.code_challenge_methods_supported.includes('S256')
    ) {
      throw new Error('Authorization Server metadata is invalid');
    }
    if (new URL(record.jwks_uri).origin !== new URL(this.issuer).origin) {
      throw new Error('Authorization Server metadata is invalid');
    }
    await assertTrustedPublicHTTPSURL(record.jwks_uri, this.resolveHost);
    this.metadata = {
      issuer: this.issuer,
      jwks_uri: record.jwks_uri,
      code_challenge_methods_supported: ['S256'],
    };
    return this.metadata;
  }
}

function parseBearerAuthorization(value: string | string[] | undefined): string {
  if (value === undefined || value === '') {
    throw new OAuthResourceError('invalid_token', [], 'missing_credential');
  }
  if (typeof value !== 'string' || Buffer.byteLength(value, 'utf8') > MAX_AUTHORIZATION_BYTES) {
    throw new OAuthResourceError('invalid_token', [], 'malformed_credential');
  }
  const match = /^Bearer ([A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)$/.exec(value);
  if (!match) {
    throw new OAuthResourceError('invalid_token', [], 'malformed_credential');
  }
  return match[1];
}

function oauthReasonForError(error: unknown): OAuthSecurityReason {
  if (error instanceof joseErrors.JWTExpired) return 'expired_credential';
  if (error instanceof joseErrors.JWTClaimValidationFailed) {
    const claim = (error as { claim?: string }).claim;
    if (claim === 'nbf') return 'credential_not_yet_valid';
    if (claim === 'iss') return 'issuer_mismatch';
    if (claim === 'aud') return 'audience_mismatch';
    if (['sub', 'exp', 'iat', 'jti', 'client_id', 'scope'].includes(claim ?? '')) return 'incomplete_claims';
  }
  return 'invalid_credential';
}

function isTeamCapability(value: string): value is TeamCapability {
  return (TEAM_CAPABILITIES as readonly string[]).includes(value);
}

function isUnknownKid(error: unknown): boolean {
  return error instanceof joseErrors.JWKSNoMatchingKey ||
    (error instanceof Error && 'code' in error && error.code === 'ERR_JWKS_NO_MATCHING_KEY');
}

function validateJwks(value: unknown, algorithms: readonly string[]): JSONWebKeySet {
  if (!value || typeof value !== 'object' || !Array.isArray((value as { keys?: unknown }).keys)) {
    throw new Error('JWKS is invalid');
  }
  const keys = (value as { keys: Array<Record<string, unknown>> }).keys;
  if (keys.length < 1 || keys.length > MAX_JWKS_KEYS) {
    throw new Error('JWKS is invalid');
  }
  const kids = new Set<string>();
  for (const key of keys) {
    if (
      !isBoundedString(key.kid, 1, 128) ||
      kids.has(key.kid) ||
      (key.use !== undefined && key.use !== 'sig') ||
      (key.alg !== undefined && (typeof key.alg !== 'string' || !algorithms.includes(key.alg))) ||
      (key.kty !== 'RSA' && key.kty !== 'EC' && key.kty !== 'OKP')
    ) {
      throw new Error('JWKS is invalid');
    }
    kids.add(key.kid);
  }
  return { keys: keys as JSONWebKeySet['keys'] };
}

async function fetchBoundedJSON(
  rawURL: string,
  maxBytes: number,
  timeoutMs: number,
  fetcher: typeof fetch | undefined,
  resolveHost: (hostname: string) => Promise<readonly string[]>,
): Promise<unknown> {
  const deadline = Date.now() + timeoutMs;
  return withinDeadline((async () => {
    const endpoint = await resolveTrustedPublicHTTPSURL(rawURL, resolveHost);
    const remainingMs = deadline - Date.now();
    if (remainingMs <= 0) {
      throw new Error('OAuth metadata fetch failed');
    }
    if (!fetcher) {
      return fetchPinnedHTTPSJSON(endpoint, maxBytes, remainingMs);
    }
    const response = await fetcher(rawURL, {
      method: 'GET',
      redirect: 'error',
      signal: AbortSignal.timeout(remainingMs),
      headers: { Accept: 'application/json' },
    });
    if (!response.ok) {
      throw new Error('OAuth metadata fetch failed');
    }
    const declared = Number(response.headers.get('content-length') ?? '0');
    if (Number.isFinite(declared) && declared > maxBytes) {
      throw new Error('OAuth metadata response is too large');
    }
    const reader = response.body?.getReader();
    if (!reader) {
      throw new Error('OAuth metadata response is empty');
    }
    const chunks: Uint8Array[] = [];
    let length = 0;
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      length += value.byteLength;
      if (length > maxBytes) {
        await reader.cancel();
        throw new Error('OAuth metadata response is too large');
      }
      chunks.push(value);
    }
    const bytes = Buffer.concat(chunks, length);
    try {
      return JSON.parse(bytes.toString('utf8'));
    } catch {
      throw new Error('OAuth metadata response is invalid');
    }
  })(), deadline);
}

async function withinDeadline<T>(operation: Promise<T>, deadline: number): Promise<T> {
  const remainingMs = deadline - Date.now();
  if (remainingMs <= 0) throw new Error('OAuth metadata fetch failed');
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      operation,
      new Promise<T>((_resolve, reject) => {
        timer = setTimeout(() => reject(new Error('OAuth metadata fetch failed')), remainingMs);
      }),
    ]);
  } finally {
    if (timer) clearTimeout(timer);
  }
}

function assertPinnedHTTPSURL(rawURL: string, label: string): void {
  let url: URL;
  try {
    url = new URL(rawURL);
  } catch {
    throw new Error(`OAuth ${label} must be a pinned HTTPS URL`);
  }
  if (
    url.protocol !== 'https:' ||
    url.username !== '' ||
    url.password !== '' ||
    url.hash !== '' ||
    url.search !== ''
  ) {
    throw new Error(`OAuth ${label} must be a pinned HTTPS URL`);
  }
}

async function assertTrustedPublicHTTPSURL(
  rawURL: string,
  resolveHost: (hostname: string) => Promise<readonly string[]>,
): Promise<void> {
  await resolveTrustedPublicHTTPSURL(rawURL, resolveHost);
}

async function resolveTrustedPublicHTTPSURL(
  rawURL: string,
  resolveHost: (hostname: string) => Promise<readonly string[]>,
): Promise<{ url: URL; address: string }> {
  assertPinnedHTTPSURL(rawURL, 'endpoint');
  const url = new URL(rawURL);
  const hostname = url.hostname.toLowerCase();
  if (hostname === 'localhost' || hostname.endsWith('.localhost') || hostname.endsWith('.local') || hostname.endsWith('.internal')) {
    throw new Error('OAuth endpoint must be a trusted public HTTPS endpoint');
  }
  const addresses = isIP(hostname) ? [hostname] : await resolveHost(hostname);
  if (addresses.length === 0 || addresses.length > 16 || addresses.some((address) => !isPublicAddress(address))) {
    throw new Error('OAuth endpoint must be a trusted public HTTPS endpoint');
  }
  return { url, address: normalizeMappedIPv4(addresses[0]) };
}

async function resolvePublicHost(hostname: string): Promise<readonly string[]> {
  const { resolve4, resolve6 } = await import('node:dns/promises');
  const [v4, v6] = await Promise.all([
    resolve4(hostname).catch(() => []),
    resolve6(hostname).catch(() => []),
  ]);
  return [...v4, ...v6];
}

function isPublicAddress(address: string): boolean {
  address = normalizeMappedIPv4(address);
  if (isIP(address) === 4) {
    const parts = address.split('.').map(Number);
    const [a, b] = parts;
    return !(
      a === 0 || a === 10 || a === 127 || a >= 224 ||
      (a === 100 && b >= 64 && b <= 127) ||
      (a === 169 && b === 254) ||
      (a === 172 && b >= 16 && b <= 31) ||
      (a === 192 && (b === 0 || b === 168)) ||
      (a === 198 && (b === 18 || b === 19))
    );
  }
  if (isIP(address) === 6) {
    const hextets = parseIPv6Hextets(address);
    if (!hextets) return false;
    const [a] = hextets;
    const prefix = (...values: number[]) => values.every((value, index) => hextets[index] === value);
    return !(
      // IPv4-compatible and other IPv4-embedded forms under ::/96.
      prefix(0, 0, 0, 0, 0, 0) ||
      // IPv4-translatable ::ffff:0:0/96 (standard ::ffff/96 was normalized above).
      prefix(0, 0, 0, 0, 0xffff, 0) ||
      (a & 0xfe00) === 0xfc00 || // unique-local fc00::/7
      (a & 0xffc0) === 0xfe80 || // link-local fe80::/10
      (a & 0xffc0) === 0xfec0 || // deprecated site-local fec0::/10
      (a & 0xff00) === 0xff00 || // multicast ff00::/8
      prefix(0x64, 0xff9b, 0x1) || // local-use translation 64:ff9b:1::/48
      prefix(0x100, 0, 0, 0) // discard-only 100::/64
    );
  }
  return false;
}

function normalizeMappedIPv4(address: string): string {
  const match = /^::ffff:(\d+\.\d+\.\d+\.\d+)$/i.exec(address);
  if (match) return match[1];
  const halves = address.toLowerCase().split('::');
  if (halves.length > 2) return address;
  const left = halves[0] === '' ? [] : halves[0].split(':');
  const right = halves.length === 1 || halves[1] === '' ? [] : halves[1].split(':');
  const fill = halves.length === 2 ? Array(Math.max(0, 8 - left.length - right.length)).fill('0') : [];
  const segments = [...left, ...fill, ...right].map((segment) => Number.parseInt(segment || '0', 16));
  if (
    segments.length === 8 && segments.slice(0, 5).every((segment) => segment === 0) &&
    segments[5] === 0xffff && segments.every((segment) => Number.isInteger(segment) && segment >= 0 && segment <= 0xffff)
  ) {
    return [segments[6] >> 8, segments[6] & 0xff, segments[7] >> 8, segments[7] & 0xff].join('.');
  }
  return address;
}

function parseIPv6Hextets(address: string): number[] | undefined {
  let normalized = address.toLowerCase();
  if (normalized.includes('%')) return undefined;
  const dotted = /(^|:)(\d+\.\d+\.\d+\.\d+)$/.exec(normalized);
  if (dotted) {
    const octets = dotted[2].split('.').map(Number);
    if (octets.length !== 4 || octets.some((value) => !Number.isInteger(value) || value < 0 || value > 255)) {
      return undefined;
    }
    const replacement = `${((octets[0] << 8) | octets[1]).toString(16)}:${((octets[2] << 8) | octets[3]).toString(16)}`;
    normalized = `${normalized.slice(0, dotted.index + dotted[1].length)}${replacement}`;
  }
  const halves = normalized.split('::');
  if (halves.length > 2) return undefined;
  const parseHalf = (half: string): number[] | undefined => {
    if (half === '') return [];
    const parts = half.split(':');
    if (parts.some((part) => !/^[0-9a-f]{1,4}$/.test(part))) return undefined;
    return parts.map((part) => Number.parseInt(part, 16));
  };
  const left = parseHalf(halves[0] ?? '');
  const right = parseHalf(halves[1] ?? '');
  if (!left || !right) return undefined;
  if (halves.length === 1) return left.length === 8 ? left : undefined;
  const missing = 8 - left.length - right.length;
  if (missing < 1) return undefined;
  return [...left, ...Array(missing).fill(0), ...right];
}

function fetchPinnedHTTPSJSON(
  endpoint: { url: URL; address: string },
  maxBytes: number,
  timeoutMs: number,
): Promise<unknown> {
  return new Promise((resolve, reject) => {
    let settled = false;
    const finish = (callback: () => void) => {
      if (settled) return;
      settled = true;
      clearTimeout(wallClockTimer);
      callback();
    };
    const request = httpsRequest({
      protocol: 'https:',
      hostname: endpoint.address,
      port: endpoint.url.port || 443,
      path: `${endpoint.url.pathname}${endpoint.url.search}`,
      method: 'GET',
      servername: endpoint.url.hostname,
      rejectUnauthorized: true,
      headers: { Accept: 'application/json', Host: endpoint.url.host },
    }, (response) => {
      if ((response.statusCode ?? 500) < 200 || (response.statusCode ?? 500) >= 300) {
        response.destroy();
        finish(() => reject(new Error('OAuth metadata fetch failed')));
        return;
      }
      const declared = Number(response.headers['content-length'] ?? '0');
      if (Number.isFinite(declared) && declared > maxBytes) {
        response.destroy();
        finish(() => reject(new Error('OAuth metadata response is too large')));
        return;
      }
      const chunks: Buffer[] = [];
      let size = 0;
      response.on('data', (chunk: Buffer) => {
        size += chunk.length;
        if (size > maxBytes) {
          response.destroy(new Error('OAuth metadata response is too large'));
          return;
        }
        chunks.push(chunk);
      });
      response.on('error', (error) => finish(() => reject(error)));
      response.on('end', () => {
        try {
          const value = JSON.parse(Buffer.concat(chunks, size).toString('utf8'));
          finish(() => resolve(value));
        } catch {
          finish(() => reject(new Error('OAuth metadata response is invalid')));
        }
      });
    });
    const wallClockTimer = setTimeout(() => {
      request.destroy();
      finish(() => reject(new Error('OAuth metadata fetch failed')));
    }, timeoutMs);
    request.on('error', (error) => finish(() => reject(error)));
    request.end();
  });
}

function boundedInteger(value: number, min: number, max: number, label: string): number {
  if (!Number.isInteger(value) || value < min || value > max) {
    throw new Error(`${label} is outside its allowed bound`);
  }
  return value;
}

function isBoundedString(value: unknown, min: number, max: number): value is string {
  return typeof value === 'string' && Buffer.byteLength(value, 'utf8') >= min && Buffer.byteLength(value, 'utf8') <= max;
}

function isBoundedIdentityString(value: unknown): value is string {
  return isBoundedString(value, 1, 512) && value.trim() === value && !/[\u0000-\u001f\u007f]/.test(value);
}
