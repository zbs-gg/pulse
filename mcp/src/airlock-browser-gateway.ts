import {
  createHash,
  randomBytes,
  timingSafeEqual,
} from 'node:crypto';
import {
  request as httpRequest,
  type IncomingMessage,
  type ServerResponse,
} from 'node:http';
import { TextDecoder } from 'node:util';

import { canonicalAirlockEnvelope } from './airlock-contracts.js';
import {
  AIRLOCK_HUMAN_PRESENCE_ACR,
  AIRLOCK_HUMAN_PRESENCE_SCHEMA,
  AIRLOCK_PLATFORM_WEBAUTHN_FACTOR,
  type OAuthResourceVerifier,
  type VerifiedBrowserAuthorization,
} from './oauth-resource.js';
import type { PrincipalSigner } from './principal-context.js';

export const TEAM_PUBLICATION_AIRLOCK_PATH = '/airlock/team-publication';
export const TEAM_PUBLICATION_AIRLOCK_CALLBACK_PATH = '/airlock/team-publication/callback';

const SESSION_COOKIE = '__Host-pulse-airlock-session';
const FLOW_COOKIE = '__Host-pulse-airlock-flow';
const CSRF_COOKIE = '__Host-pulse-airlock-csrf';
const SESSION_SECONDS = 300;
const MAX_BODY_BYTES = 96 * 1024;
const MAX_PROXY_RESPONSE_BYTES = 256 * 1024;
const MAX_TOKEN_RESPONSE_BYTES = 64 * 1024;
const MAX_IN_MEMORY_RECORDS = 256;
const MAX_ADMISSION_RECORDS = 256;
const MAX_ACTIVE_FLOWS_PER_SOURCE = 8;
const MAX_FLOW_STARTS_PER_SOURCE_PER_MINUTE = 32;
const OPAQUE_TOKEN = /^[A-Za-z0-9_-]{43}$/;
const SAFE_REQUEST_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{7,254}$/;
const HEX_DIGEST = /^[0-9a-f]{64}$/;
const FORM_FIELDS = new Set([
  'csrf_token',
  'decision',
  'canonical_envelope',
  'envelope_digest',
  'store_id',
  'team_id',
  'publisher_principal_id',
  'request_id',
]);

interface BrowserFlow {
  id: string;
  state: string;
  nonce: string;
  verifier: string;
  createdAt: number;
  sourceKey: string;
  expiresAt: number;
}

interface BrowserSession {
  identity: VerifiedBrowserAuthorization;
  flowId: string;
  nonceDigest: string;
  authorizationStartedAt: number;
  expiresAt: number;
}

interface AdmissionRecord {
  count: number;
  windowStartedAt: number;
  lastSeenAt: number;
}

export interface AirlockBrowserTokenVerifier {
  verifyBrowserAuthorization(input: {
    accessToken: string;
    idToken: string;
    clientId: string;
    nonce: string;
    requiredCapabilities: readonly ['pulse:owner'];
    maxAuthenticationAgeSeconds: number;
    authorizationStartedAt: number;
  }): Promise<VerifiedBrowserAuthorization>;
}

export interface AirlockOwnerStepUpSigner {
  signOwnerStepUp(input: Parameters<PrincipalSigner['signOwnerStepUp']>[0]): Promise<string>;
}

export interface AirlockDaemonProxyRequest {
  method: 'GET' | 'POST';
  path: typeof TEAM_PUBLICATION_AIRLOCK_PATH;
  headers: Readonly<Record<string, string>>;
  body?: Uint8Array;
}

export interface AirlockDaemonProxyResponse {
  status: number;
  headers: Readonly<Record<string, string | readonly string[] | undefined>>;
  body: Uint8Array;
}

export type AirlockDaemonProxy = (
  request: AirlockDaemonProxyRequest,
) => Promise<AirlockDaemonProxyResponse>;

export interface TeamPublicationBrowserGatewayOptions {
  publicBaseURL: string;
  authIssuer: string;
  authorizationEndpoint: string;
  tokenEndpoint: string;
  clientId: string;
  daemonBaseURL: string;
  verifier: AirlockBrowserTokenVerifier | OAuthResourceVerifier;
  signer: AirlockOwnerStepUpSigner | PrincipalSigner;
  fetch?: typeof fetch;
  proxy?: AirlockDaemonProxy;
  now?: () => number;
  randomToken?: () => string;
}

export class TeamPublicationBrowserGateway {
  private readonly publicOrigin: string;
  private readonly publicHost: string;
  private readonly callbackURL: string;
  private readonly authorizationEndpoint: string;
  private readonly tokenEndpoint: string;
  private readonly clientId: string;
  private readonly verifier: AirlockBrowserTokenVerifier;
  private readonly signer: AirlockOwnerStepUpSigner;
  private readonly fetcher: typeof fetch;
  private readonly proxy: AirlockDaemonProxy;
  private readonly now: () => number;
  private readonly randomToken: () => string;
  private readonly flows = new Map<string, BrowserFlow>();
  private readonly sessions = new Map<string, BrowserSession>();
  private readonly admissions = new Map<string, AdmissionRecord>();

  constructor(options: TeamPublicationBrowserGatewayOptions) {
    const publicURL = exactHTTPSOrigin(options.publicBaseURL, 'public base URL');
    this.publicOrigin = publicURL.origin;
    this.publicHost = publicURL.host;
    this.callbackURL = `${publicURL.origin}${TEAM_PUBLICATION_AIRLOCK_CALLBACK_PATH}`;
    const issuer = exactIssuer(options.authIssuer);
    this.authorizationEndpoint = exactIssuerEndpoint(
      options.authorizationEndpoint,
      issuer,
      'authorization endpoint',
    );
    this.tokenEndpoint = exactIssuerEndpoint(
      options.tokenEndpoint,
      issuer,
      'token endpoint',
    );
    if (!/^[A-Za-z0-9._:-]{1,255}$/.test(options.clientId)) {
      throw new Error('Airlock OIDC client ID is invalid');
    }
    this.clientId = options.clientId;
    this.verifier = options.verifier;
    this.signer = options.signer;
    this.fetcher = options.fetch ?? fetch;
    this.now = options.now ?? (() => Math.floor(Date.now() / 1000));
    this.randomToken = options.randomToken ?? (() => randomBytes(32).toString('base64url'));
    this.proxy = options.proxy ?? createLoopbackAirlockProxy(
      options.daemonBaseURL,
      this.publicHost,
    );
  }

  matches(path: string): boolean {
    return path === TEAM_PUBLICATION_AIRLOCK_PATH ||
      path === TEAM_PUBLICATION_AIRLOCK_CALLBACK_PATH;
  }

  async handle(req: IncomingMessage, res: ServerResponse, requestURL: URL): Promise<void> {
    setBrowserSecurityHeaders(res);
    try {
      if (!this.matches(requestURL.pathname) || !this.validHost(req)) {
        writeFixedError(res, 404, 'not found');
        return;
      }
      if (requestURL.pathname === TEAM_PUBLICATION_AIRLOCK_CALLBACK_PATH) {
        await this.handleCallback(req, res, requestURL);
        return;
      }
      if (requestURL.search !== '') {
        writeFixedError(res, 404, 'not found');
        return;
      }
      if (req.method === 'GET') {
        await this.handlePreview(req, res);
        return;
      }
      if (req.method === 'POST') {
        await this.handleApproval(req, res);
        return;
      }
      res.setHeader('Allow', 'GET, POST');
      writeFixedError(res, 405, 'method not allowed');
    } catch {
      writeFixedError(res, 503, 'airlock unavailable');
    }
  }

  private async handlePreview(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const session = this.readSession(req);
    if (!session) {
      this.startAuthorization(req, res);
      return;
    }
    const response = await this.proxy({
      method: 'GET',
      path: TEAM_PUBLICATION_AIRLOCK_PATH,
      headers: { Host: this.publicHost },
    });
    forwardDaemonResponse(res, response, true);
  }

  private startAuthorization(req: IncomingMessage, res: ServerResponse): void {
    if (hasUnexpectedBody(req)) {
      writeFixedError(res, 503, 'airlock unavailable');
      return;
    }
    this.cleanupExpired();
    const sourceKey = admissionSourceKey(req);
    if (!this.admitFlowStart(sourceKey)) {
      res.setHeader('Retry-After', '60');
      writeFixedError(res, 429, 'airlock busy');
      return;
    }
    this.makeFlowCapacity(sourceKey);
    const flowId = this.newOpaqueToken();
    const state = this.newOpaqueToken();
    const nonce = this.newOpaqueToken();
    const verifier = this.newOpaqueToken();
    this.flows.set(flowId, {
      id: flowId,
      state,
      nonce,
      verifier,
      createdAt: this.now(),
      sourceKey,
      expiresAt: this.now() + SESSION_SECONDS,
    });
    const location = new URL(this.authorizationEndpoint);
    location.searchParams.set('response_type', 'code');
    location.searchParams.set('client_id', this.clientId);
    location.searchParams.set('redirect_uri', this.callbackURL);
    location.searchParams.set('scope', 'openid pulse:owner');
    location.searchParams.set('audience', `${this.publicOrigin}/mcp`);
    location.searchParams.set('state', state);
    location.searchParams.set('nonce', nonce);
    location.searchParams.set('code_challenge', createHash('sha256').update(verifier).digest('base64url'));
    location.searchParams.set('code_challenge_method', 'S256');
    location.searchParams.set('prompt', 'login');
    location.searchParams.set('max_age', '0');
    location.searchParams.set('acr_values', AIRLOCK_HUMAN_PRESENCE_ACR);
    setCookie(res, FLOW_COOKIE, flowId, {
      path: TEAM_PUBLICATION_AIRLOCK_CALLBACK_PATH,
      maxAge: SESSION_SECONDS,
      sameSite: 'Lax',
    });
    res.writeHead(303, { Location: location.toString() });
    res.end();
  }

  private async handleCallback(req: IncomingMessage, res: ServerResponse, requestURL: URL): Promise<void> {
    if (req.method !== 'GET' || hasUnexpectedBody(req)) {
      writeFixedError(res, 405, 'method not allowed');
      return;
    }
    const query = exactQuery(requestURL.searchParams, ['code', 'state']);
    const flowId = exactCookie(req, FLOW_COOKIE);
    const previousSessionId = exactCookie(req, SESSION_COOKIE);
    const flow = flowId ? this.flows.get(flowId) : undefined;
    if (flowId) this.flows.delete(flowId);
    if (previousSessionId) this.sessions.delete(previousSessionId);
    clearCookie(res, FLOW_COOKIE, TEAM_PUBLICATION_AIRLOCK_CALLBACK_PATH, 'Lax');
    if (previousSessionId) {
      clearCookie(res, SESSION_COOKIE, TEAM_PUBLICATION_AIRLOCK_PATH, 'Strict');
    }
    if (
      !flow || flow.expiresAt <= this.now() ||
      !safeEqual(query?.state ?? '', flow.state) ||
      !isBoundedAuthorizationCode(query?.code)
    ) {
      writeFixedError(res, 401, 'authentication rejected');
      return;
    }
    const tokens = await this.exchangeCode(query.code, flow.verifier);
    const identity = await this.verifier.verifyBrowserAuthorization({
      accessToken: tokens.accessToken,
      idToken: tokens.idToken,
      clientId: this.clientId,
      nonce: flow.nonce,
      requiredCapabilities: ['pulse:owner'],
      maxAuthenticationAgeSeconds: SESSION_SECONDS,
      authorizationStartedAt: flow.createdAt,
    });
    const authTime = identity.authTime;
    if (
      identity.clientId !== this.clientId ||
      !identity.capabilities.includes('pulse:owner') ||
      !Number.isInteger(authTime) || authTime === undefined || authTime <= 0 ||
      authTime > this.now() + 5 || authTime < flow.createdAt - 5 ||
      identity.authenticationContext !== AIRLOCK_HUMAN_PRESENCE_ACR ||
      !validAuthenticationMethods(identity.authenticationMethods) ||
      identity.humanPresence.schema !== AIRLOCK_HUMAN_PRESENCE_SCHEMA ||
      identity.humanPresence.factor !== AIRLOCK_PLATFORM_WEBAUTHN_FACTOR ||
      !Number.isInteger(identity.humanPresence.verifiedAt) ||
      identity.humanPresence.verifiedAt < flow.createdAt - 5 ||
      identity.humanPresence.verifiedAt > this.now() + 5 ||
      !Number.isInteger(identity.tokenExpiresAt) || identity.tokenExpiresAt <= this.now()
    ) {
      writeFixedError(res, 401, 'authentication rejected');
      return;
    }
    this.cleanupExpired();
    if (this.sessions.size >= MAX_IN_MEMORY_RECORDS) {
      writeFixedError(res, 503, 'airlock unavailable');
      return;
    }
    const sessionId = this.newOpaqueToken();
    this.sessions.set(sessionId, {
      identity: {
        ...identity,
        capabilities: [...identity.capabilities],
        authenticationMethods: [...identity.authenticationMethods],
        humanPresence: { ...identity.humanPresence },
        authTime,
      },
      flowId: flow.id,
      nonceDigest: createHash('sha256').update(flow.nonce).digest('hex'),
      authorizationStartedAt: flow.createdAt,
      expiresAt: Math.min(this.now() + SESSION_SECONDS, identity.tokenExpiresAt),
    });
    setCookie(res, SESSION_COOKIE, sessionId, {
      path: TEAM_PUBLICATION_AIRLOCK_PATH,
      maxAge: SESSION_SECONDS,
      sameSite: 'Strict',
    });
    res.writeHead(303, { Location: `${this.publicOrigin}${TEAM_PUBLICATION_AIRLOCK_PATH}` });
    res.end();
  }

  private async handleApproval(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const sessionId = exactCookie(req, SESSION_COOKIE);
    const session = sessionId ? this.sessions.get(sessionId) : undefined;
    // A browser step-up authorizes one approval attempt. Consume before any
    // parsing or downstream I/O so parallel submits cannot reuse it.
    if (sessionId) this.sessions.delete(sessionId);
    clearCookie(res, SESSION_COOKIE, TEAM_PUBLICATION_AIRLOCK_PATH, 'Strict');
    if (!session || session.expiresAt <= this.now() || !validBoundSession(session, this.now())) {
      writeFixedError(res, 401, 'authentication required');
      return;
    }
    if (!this.validMutationHeaders(req)) {
      writeFixedError(res, 403, 'approval rejected');
      return;
    }
    const body = await readBoundedBody(req, MAX_BODY_BYTES);
    const form = exactForm(body);
    if (!form || !this.validCSRF(req, form.csrf_token)) {
      writeFixedError(res, 403, 'approval rejected');
      return;
    }
    let envelope: Buffer;
    try {
      envelope = Buffer.from(form.canonical_envelope, 'base64url');
    } catch {
      writeFixedError(res, 400, 'invalid approval request');
      return;
    }
    if (
      envelope.length < 2 || envelope.length > 64 * 1024 ||
      envelope.toString('base64url') !== form.canonical_envelope
    ) {
      writeFixedError(res, 400, 'invalid approval request');
      return;
    }
    let canonical: ReturnType<typeof canonicalAirlockEnvelope>;
    try {
      canonical = canonicalAirlockEnvelope(new TextDecoder('utf-8', { fatal: true }).decode(envelope));
    } catch {
      writeFixedError(res, 400, 'invalid approval request');
      return;
    }
    if (
      canonical.bytes !== envelope.toString('utf8') ||
      !HEX_DIGEST.test(form.envelope_digest) ||
      !safeEqual(canonical.envelopeDigest, form.envelope_digest) ||
      canonical.value.store_id !== form.store_id ||
      canonical.value.team_id !== form.team_id ||
      canonical.value.writer_principal_id !== form.publisher_principal_id ||
      form.decision !== 'approve' ||
      !SAFE_REQUEST_ID.test(form.request_id)
    ) {
      writeFixedError(res, 409, 'publication changed');
      return;
    }
    const assertion = await this.signer.signOwnerStepUp({
      requestId: form.request_id,
      path: TEAM_PUBLICATION_AIRLOCK_PATH,
      action: 'team.commons.publish',
      body: envelope,
      storeId: canonical.value.store_id,
      teamId: canonical.value.team_id,
      oauthIssuer: session.identity.issuer,
      oauthSubject: session.identity.subject,
      oauthClientId: session.identity.clientId,
      authTime: session.identity.authTime,
    });
    if (typeof assertion !== 'string' || assertion.length < 32 || assertion.length > 16 * 1024) {
      writeFixedError(res, 503, 'airlock unavailable');
      return;
    }
    const csrfCookie = exactCookie(req, CSRF_COOKIE);
    const response = await this.proxy({
      method: 'POST',
      path: TEAM_PUBLICATION_AIRLOCK_PATH,
      headers: {
        Host: this.publicHost,
        Origin: this.publicOrigin,
        'Sec-Fetch-Site': 'same-origin',
        'Content-Type': 'application/x-www-form-urlencoded',
        Cookie: `${CSRF_COOKIE}=${csrfCookie}`,
        'X-Pulse-Owner-Step-Up': assertion,
      },
      body,
    });
    forwardDaemonResponse(res, response, false);
  }

  private readSession(req: IncomingMessage): BrowserSession | undefined {
    const id = exactCookie(req, SESSION_COOKIE);
    if (!id) return undefined;
    const session = this.sessions.get(id);
    if (!session || session.expiresAt <= this.now()) {
      this.sessions.delete(id);
      return undefined;
    }
    return session;
  }

  private validMutationHeaders(req: IncomingMessage): boolean {
    if (
      duplicateHeader(req, 'origin') || duplicateHeader(req, 'content-type') ||
      duplicateHeader(req, 'content-length') || duplicateHeader(req, 'transfer-encoding') ||
      singleHeader(req.headers['transfer-encoding']) !== '' ||
      singleHeader(req.headers.origin) !== this.publicOrigin ||
      singleHeader(req.headers['content-type']) !== 'application/x-www-form-urlencoded'
    ) return false;
    const fetchSite = singleHeader(req.headers['sec-fetch-site']);
    if (fetchSite !== '' && fetchSite !== 'same-origin') return false;
    const declared = singleHeader(req.headers['content-length']);
    return declared === '' || (/^\d+$/.test(declared) && Number(declared) <= MAX_BODY_BYTES);
  }

  private validCSRF(req: IncomingMessage, presented: string): boolean {
    const cookie = exactCookie(req, CSRF_COOKIE);
    return OPAQUE_TOKEN.test(presented) && cookie !== undefined && safeEqual(cookie, presented);
  }

  private validHost(req: IncomingMessage): boolean {
    return !duplicateHeader(req, 'host') && singleHeader(req.headers.host) === this.publicHost;
  }

  private async exchangeCode(code: string, verifier: string): Promise<{
    accessToken: string;
    idToken: string;
  }> {
    const body = new URLSearchParams({
      grant_type: 'authorization_code',
      code,
      redirect_uri: this.callbackURL,
      client_id: this.clientId,
      code_verifier: verifier,
    });
    const response = await this.fetcher(this.tokenEndpoint, {
      method: 'POST',
      redirect: 'error',
      signal: AbortSignal.timeout(5_000),
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/x-www-form-urlencoded',
      },
      body,
    });
    if (!response.ok) throw new Error('OIDC token exchange failed');
    const bytes = await readBoundedFetchBody(response, MAX_TOKEN_RESPONSE_BYTES);
    let value: unknown;
    try {
      value = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes));
    } catch {
      throw new Error('OIDC token response invalid');
    }
    if (!value || typeof value !== 'object' || Array.isArray(value)) {
      throw new Error('OIDC token response invalid');
    }
    const record = value as Record<string, unknown>;
    if (
      record.token_type !== 'Bearer' ||
      !isCompactToken(record.access_token) ||
      !isCompactToken(record.id_token)
    ) throw new Error('OIDC token response invalid');
    return { accessToken: record.access_token, idToken: record.id_token };
  }

  private cleanupExpired(): void {
    const now = this.now();
    for (const [id, flow] of this.flows) if (flow.expiresAt <= now) this.flows.delete(id);
    for (const [id, session] of this.sessions) if (session.expiresAt <= now) this.sessions.delete(id);
    for (const [key, admission] of this.admissions) {
      if (now - admission.lastSeenAt >= 60) this.admissions.delete(key);
    }
  }

  private admitFlowStart(sourceKey: string): boolean {
    const now = this.now();
    let record = this.admissions.get(sourceKey);
    if (!record) {
      if (this.admissions.size >= MAX_ADMISSION_RECORDS) {
        const oldest = [...this.admissions.entries()].sort(
          ([leftKey, left], [rightKey, right]) =>
            left.lastSeenAt - right.lastSeenAt || leftKey.localeCompare(rightKey),
        )[0];
        if (oldest) this.admissions.delete(oldest[0]);
      }
      record = { count: 0, windowStartedAt: now, lastSeenAt: now };
      this.admissions.set(sourceKey, record);
    } else if (now - record.windowStartedAt >= 60) {
      record.count = 0;
      record.windowStartedAt = now;
    }
    record.lastSeenAt = now;
    if (record.count >= MAX_FLOW_STARTS_PER_SOURCE_PER_MINUTE) return false;
    record.count++;
    return true;
  }

  private makeFlowCapacity(sourceKey: string): void {
    const sameSource = [...this.flows.values()].filter((flow) => flow.sourceKey === sourceKey);
    if (sameSource.length >= MAX_ACTIVE_FLOWS_PER_SOURCE) {
      const oldest = sameSource[0];
      if (oldest) this.flows.delete(oldest.id);
    }
    if (this.flows.size < MAX_IN_MEMORY_RECORDS) return;
    const counts = new Map<string, number>();
    for (const flow of this.flows.values()) {
      counts.set(flow.sourceKey, (counts.get(flow.sourceKey) ?? 0) + 1);
    }
    let selected: BrowserFlow | undefined;
    for (const flow of this.flows.values()) {
      if (!selected || (counts.get(flow.sourceKey) ?? 0) > (counts.get(selected.sourceKey) ?? 0)) {
        selected = flow;
      }
    }
    if (selected) this.flows.delete(selected.id);
  }

  private newOpaqueToken(): string {
    const value = this.randomToken();
    if (!OPAQUE_TOKEN.test(value)) throw new Error('Airlock random source failed');
    return value;
  }
}

function admissionSourceKey(req: IncomingMessage): string {
  const address = req.socket.remoteAddress ?? 'unknown';
  const userAgent = singleHeader(req.headers['user-agent']).slice(0, 512);
  return createHash('sha256').update(address).update('\0').update(userAgent).digest('hex');
}

function validAuthenticationMethods(value: readonly string[]): boolean {
  return Array.isArray(value) && value.length >= 1 && value.length <= 8 &&
    value.every((method) => typeof method === 'string' && /^[a-z0-9_-]{1,32}$/.test(method)) &&
    new Set(value).size === value.length && value.includes('mfa');
}

function validBoundSession(session: BrowserSession, now: number): boolean {
  const identity = session.identity;
  return OPAQUE_TOKEN.test(session.flowId) && HEX_DIGEST.test(session.nonceDigest) &&
    identity.authenticationContext === AIRLOCK_HUMAN_PRESENCE_ACR &&
    validAuthenticationMethods(identity.authenticationMethods) &&
    identity.humanPresence.schema === AIRLOCK_HUMAN_PRESENCE_SCHEMA &&
    identity.humanPresence.factor === AIRLOCK_PLATFORM_WEBAUTHN_FACTOR &&
    Number.isInteger(identity.humanPresence.verifiedAt) &&
    Number.isInteger(session.authorizationStartedAt) && session.authorizationStartedAt > 0 &&
    identity.humanPresence.verifiedAt >= session.authorizationStartedAt - 5 &&
    identity.humanPresence.verifiedAt <= now + 5 &&
    Number.isInteger(identity.authTime) && identity.authTime > 0 && identity.authTime <= now + 5 &&
    identity.authTime >= session.authorizationStartedAt - 5 &&
    Number.isInteger(identity.tokenExpiresAt) && identity.tokenExpiresAt >= session.expiresAt &&
    identity.clientId.length > 0 && identity.subject.length > 0;
}

function exactHTTPSOrigin(raw: string, label: string): URL {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    throw new Error(`Airlock ${label} is invalid`);
  }
  if (
    raw !== url.origin || url.protocol !== 'https:' || url.username !== '' ||
    url.password !== '' || url.search !== '' || url.hash !== ''
  ) throw new Error(`Airlock ${label} is invalid`);
  return url;
}

function exactIssuer(raw: string): URL {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    throw new Error('Airlock issuer is invalid');
  }
  if (
    url.protocol !== 'https:' || url.username !== '' || url.password !== '' ||
    url.search !== '' || url.hash !== '' || (raw !== url.toString() && raw !== url.origin)
  ) throw new Error('Airlock issuer is invalid');
  return url;
}

function exactIssuerEndpoint(raw: string, issuer: URL, label: string): string {
  let endpoint: URL;
  try {
    endpoint = new URL(raw);
  } catch {
    throw new Error(`Airlock ${label} is invalid`);
  }
  if (
    endpoint.protocol !== 'https:' || endpoint.username !== '' || endpoint.password !== '' ||
    endpoint.search !== '' || endpoint.hash !== '' || endpoint.origin !== issuer.origin ||
    raw !== endpoint.toString()
  ) throw new Error(`Airlock ${label} is invalid`);
  return endpoint.toString();
}

function createLoopbackAirlockProxy(daemonBaseURL: string, publicHost: string): AirlockDaemonProxy {
  let base: URL;
  try {
    base = new URL(daemonBaseURL);
  } catch {
    throw new Error('Airlock daemon URL is invalid');
  }
  if (
    base.protocol !== 'http:' || !['127.0.0.1', '[::1]'].includes(base.hostname) ||
    base.username !== '' || base.password !== '' || base.pathname !== '/' ||
    base.search !== '' || base.hash !== ''
  ) throw new Error('Airlock daemon URL is invalid');
  return (request) => new Promise<AirlockDaemonProxyResponse>((resolve, reject) => {
    const upstream = httpRequest({
      protocol: base.protocol,
      hostname: base.hostname,
      port: base.port,
      method: request.method,
      path: request.path,
      headers: {
        ...request.headers,
        Host: publicHost,
        ...(request.body ? { 'Content-Length': String(request.body.byteLength) } : {}),
      },
      timeout: 10_000,
    }, (response) => {
      const chunks: Buffer[] = [];
      let length = 0;
      response.on('data', (chunk: Buffer) => {
        length += chunk.length;
        if (length > MAX_PROXY_RESPONSE_BYTES) {
          response.destroy(new Error('Airlock daemon response too large'));
          return;
        }
        chunks.push(chunk);
      });
      response.once('end', () => resolve({
        status: response.statusCode ?? 503,
        headers: response.headers,
        body: Buffer.concat(chunks, length),
      }));
      response.once('error', reject);
    });
    upstream.once('timeout', () => upstream.destroy(new Error('Airlock daemon timeout')));
    upstream.once('error', reject);
    if (request.body) upstream.end(request.body);
    else upstream.end();
  });
}

function forwardDaemonResponse(
  res: ServerResponse,
  response: AirlockDaemonProxyResponse,
  allowCSRFCookie: boolean,
): void {
  if (!Number.isInteger(response.status) || response.status < 200 || response.status > 599 ||
      response.body.byteLength > MAX_PROXY_RESPONSE_BYTES) {
    writeFixedError(res, 503, 'airlock unavailable');
    return;
  }
  for (const name of [
    'content-type', 'cache-control', 'content-security-policy', 'cross-origin-opener-policy',
    'cross-origin-resource-policy', 'permissions-policy', 'referrer-policy',
    'x-content-type-options', 'x-frame-options',
  ]) {
    const value = singleProxyHeader(response.headers[name]);
    if (value !== '' && !/[\r\n]/.test(value)) res.setHeader(name, value);
  }
  if (allowCSRFCookie) {
    const values = proxyHeaderValues(response.headers['set-cookie']);
    if (values.length === 1 && values[0]!.startsWith(`${CSRF_COOKIE}=`) && !/[\r\n]/.test(values[0]!)) {
      res.setHeader('Set-Cookie', values[0]!);
    }
  }
  res.writeHead(response.status);
  res.end(response.body);
}

function setBrowserSecurityHeaders(res: ServerResponse): void {
  res.setHeader('Cache-Control', 'no-store');
  res.setHeader('Content-Security-Policy', "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'");
  res.setHeader('Cross-Origin-Opener-Policy', 'same-origin');
  res.setHeader('Cross-Origin-Resource-Policy', 'same-origin');
  res.setHeader('Permissions-Policy', 'camera=(), microphone=(), geolocation=()');
  res.setHeader('Referrer-Policy', 'no-referrer');
  res.setHeader('X-Content-Type-Options', 'nosniff');
  res.setHeader('X-Frame-Options', 'DENY');
}

function writeFixedError(res: ServerResponse, status: number, message: string): void {
  if (res.headersSent) {
    res.destroy();
    return;
  }
  res.writeHead(status, { 'Content-Type': 'text/plain; charset=utf-8' });
  res.end(message);
}

function setCookie(
  res: ServerResponse,
  name: string,
  value: string,
  options: { path: string; maxAge: number; sameSite: 'Lax' | 'Strict' },
): void {
  appendSetCookie(
    res,
    `${name}=${value}; Path=${options.path}; Max-Age=${options.maxAge}; Secure; HttpOnly; SameSite=${options.sameSite}`,
  );
}

function clearCookie(res: ServerResponse, name: string, path: string, sameSite: 'Lax' | 'Strict'): void {
  appendSetCookie(
    res,
    `${name}=; Path=${path}; Max-Age=0; Expires=Thu, 01 Jan 1970 00:00:00 GMT; Secure; HttpOnly; SameSite=${sameSite}`,
  );
}

function appendSetCookie(res: ServerResponse, value: string): void {
  const existing = res.getHeader('Set-Cookie');
  const values = existing === undefined ? [] : Array.isArray(existing) ? existing.map(String) : [String(existing)];
  res.setHeader('Set-Cookie', [...values, value]);
}

function exactCookie(req: IncomingMessage, name: string): string | undefined {
  if (duplicateHeader(req, 'cookie')) return undefined;
  const raw = singleHeader(req.headers.cookie);
  if (raw === '' || raw.length > 8 * 1024) return undefined;
  let found: string | undefined;
  for (const part of raw.split(';')) {
    const trimmed = part.trim();
    const separator = trimmed.indexOf('=');
    if (separator < 1) continue;
    if (trimmed.slice(0, separator) !== name) continue;
    if (found !== undefined) return undefined;
    found = trimmed.slice(separator + 1);
  }
  return found !== undefined && OPAQUE_TOKEN.test(found) ? found : undefined;
}

function exactQuery(params: URLSearchParams, names: readonly string[]): Record<string, string> | undefined {
  const allowed = new Set(names);
  const result: Record<string, string> = {};
  let count = 0;
  for (const [name, value] of params) {
    count++;
    if (!allowed.has(name) || Object.hasOwn(result, name) || value === '') return undefined;
    result[name] = value;
  }
  return count === names.length && names.every((name) => Object.hasOwn(result, name)) ? result : undefined;
}

function exactForm(body: Uint8Array): Record<string, string> | undefined {
  let text: string;
  try {
    text = new TextDecoder('utf-8', { fatal: true }).decode(body);
  } catch {
    return undefined;
  }
  const params = new URLSearchParams(text);
  const result: Record<string, string> = {};
  let count = 0;
  for (const [name, value] of params) {
    count++;
    if (!FORM_FIELDS.has(name) || Object.hasOwn(result, name) || value === '') return undefined;
    result[name] = value;
  }
  return count === FORM_FIELDS.size && [...FORM_FIELDS].every((name) => Object.hasOwn(result, name))
    ? result
    : undefined;
}

async function readBoundedBody(req: IncomingMessage, maxBytes: number): Promise<Buffer> {
  const chunks: Buffer[] = [];
  let length = 0;
  for await (const chunk of req) {
    const bytes = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
    length += bytes.length;
    if (length > maxBytes) throw new Error('request too large');
    chunks.push(bytes);
  }
  return Buffer.concat(chunks, length);
}

async function readBoundedFetchBody(response: Response, maxBytes: number): Promise<Uint8Array> {
  const declared = Number(response.headers.get('content-length') ?? '0');
  if (Number.isFinite(declared) && declared > maxBytes) throw new Error('response too large');
  const reader = response.body?.getReader();
  if (!reader) throw new Error('response missing');
  const chunks: Uint8Array[] = [];
  let length = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    length += value.byteLength;
    if (length > maxBytes) {
      await reader.cancel();
      throw new Error('response too large');
    }
    chunks.push(value);
  }
  return Buffer.concat(chunks, length);
}

function hasUnexpectedBody(req: IncomingMessage): boolean {
  if (duplicateHeader(req, 'content-length') || duplicateHeader(req, 'transfer-encoding')) return true;
  if (singleHeader(req.headers['transfer-encoding']) !== '') return true;
  const length = singleHeader(req.headers['content-length']);
  return length !== '' && (!/^\d+$/.test(length) || Number(length) !== 0);
}

function duplicateHeader(req: IncomingMessage, name: string): boolean {
  let count = 0;
  for (let index = 0; index < req.rawHeaders.length; index += 2) {
    if (req.rawHeaders[index]?.toLowerCase() === name) count++;
  }
  return count > 1;
}

function singleHeader(value: string | string[] | undefined): string {
  return typeof value === 'string' ? value : '';
}

function proxyHeaderValues(value: string | readonly string[] | undefined): string[] {
  if (value === undefined) return [];
  return typeof value === 'string' ? [value] : [...value];
}

function singleProxyHeader(value: string | readonly string[] | undefined): string {
  return typeof value === 'string' ? value : '';
}

function safeEqual(left: string, right: string): boolean {
  const a = Buffer.from(left, 'utf8');
  const b = Buffer.from(right, 'utf8');
  return a.length === b.length && timingSafeEqual(a, b);
}

function isBoundedAuthorizationCode(value: string | undefined): value is string {
  return typeof value === 'string' && value.length >= 16 && value.length <= 4096 &&
    !/[\u0000-\u0020\u007f]/.test(value);
}

function isCompactToken(value: unknown): value is string {
  return typeof value === 'string' && value.length >= 32 && value.length <= 16 * 1024 &&
    /^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/.test(value);
}
