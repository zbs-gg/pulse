import {
  createHash,
  createPrivateKey,
  createPublicKey,
  randomUUID,
  timingSafeEqual,
  type KeyObject,
} from 'node:crypto';
import {
  closeSync,
  constants as fsConstants,
  fstatSync,
  openSync,
  readFileSync,
} from 'node:fs';

import { SignJWT } from 'jose';

import { OAuthResourceVerifier, type VerifiedOAuthIdentity } from './oauth-resource.js';
import {
  canonicalTeamRememberBody,
  requiredTeamCapabilities,
  TEAM_CAPABILITIES,
  TeamDomainError,
  validateTeamRememberResult,
  type TeamCapability,
  type TeamDomainErrorCode,
  type TeamRememberResult,
} from './team-contracts.js';

export const PRINCIPAL_ASSERTION_ISSUER = 'pulse-team-gateway';
export const PRINCIPAL_ASSERTION_AUDIENCE = 'pulse-team-daemon';
const PRINCIPAL_ASSERTION_VERSION = 'pulse.principal.v1';
export const SECURITY_EVENT_ASSERTION_VERSION = 'pulse.security_event.v1';
export const TEAM_MEMORY_REMEMBER_PATH = '/team/v1/memory/remember';
const MAX_PRIVATE_KEY_BYTES = 16 * 1024;
const MAX_KEYRING_BYTES = 32 * 1024;
const MAX_PREVIOUS_KEYS = 4;

export interface PrincipalSignerOptions {
  privateKeyFile: string;
  keyId: string;
  verifyKeyringFile: string;
  storeId: string;
  teamId: string;
  now?: () => number;
  randomId?: () => string;
}

export interface PrincipalCheckInput {
  requestId: string;
  method: string;
  path: string;
  body: Uint8Array;
  oauthIssuer: string;
  oauthSubject: string;
  oauthClientId: string;
  capabilities: readonly TeamCapability[];
}

export interface PrincipalCheckBodyInput {
  oauthIssuer: string;
  oauthSubject: string;
  oauthClientId: string;
  capabilities: readonly TeamCapability[];
}

export interface PrincipalVerificationKeyring {
  active: { kid: string; public_key: string };
  previous: Array<{ kid: string; public_key: string }>;
}

export interface TeamPrincipalClientOptions {
  daemonBaseURL: string;
  signer: PrincipalSigner;
  apiKey: () => string;
  fetch?: typeof fetch;
  timeoutMs?: number;
}

export interface BoundTeamDomain {
  remember(input: unknown): Promise<TeamRememberResult>;
}

export interface TeamPrincipalContext {
  version: 'pulse.team.principal_context.v1';
  request_id: string;
  store_id: string;
  team_id: string;
  principal_id: string;
  principal_kind: 'agent' | 'service';
  oauth_client_key: string;
  human_principal_id: string | null;
  agent_binding_id: string | null;
  membership_id: string;
  membership_role: 'owner' | 'member' | 'reviewer';
  team_auth_epoch: number;
  principal_auth_epoch: number;
  binding_auth_epoch: number | null;
  membership_auth_epoch: number;
  capabilities: TeamCapability[];
}

export class PrincipalCheckError extends Error {
  readonly code: 'principal_revoked' | 'principal_store_unavailable' | 'principal_replayed' | 'principal_binding_mismatch' | 'invalid_principal';
  readonly status: 403 | 503;

  constructor(code: 'principal_revoked' | 'principal_store_unavailable' | 'principal_replayed' | 'principal_binding_mismatch' | 'invalid_principal') {
    super('team principal check denied');
    this.name = 'PrincipalCheckError';
    this.code = code;
    this.status = code === 'principal_revoked' ? 403 : 503;
  }
}

export class SecurityEventRateLimitedError extends Error {
  constructor() {
    super('team security event storage is rate limited');
    this.name = 'SecurityEventRateLimitedError';
  }
}

export type SecurityEventReason =
  | 'missing_credential' | 'malformed_credential' | 'invalid_credential'
  | 'expired_credential' | 'credential_not_yet_valid' | 'issuer_mismatch'
  | 'audience_mismatch' | 'incomplete_claims' | 'unknown_signing_key'
  | 'insufficient_scope' | 'principal_unmapped' | 'principal_revoked' | 'assertion_invalid'
  | 'assertion_expired' | 'assertion_replayed' | 'assertion_binding_mismatch'
  | 'stale_generation' | 'store_unavailable' | 'rate_limited' | 'internal_failure';

type AuthenticationDenialReason =
  | 'missing_credential' | 'malformed_credential' | 'invalid_credential'
  | 'expired_credential' | 'credential_not_yet_valid' | 'issuer_mismatch'
  | 'audience_mismatch' | 'incomplete_claims' | 'unknown_signing_key';
type AuthorizationDenialReason = 'insufficient_scope' | 'principal_unmapped' | 'principal_revoked';
type PrincipalAssertionDenialReason =
  | 'assertion_invalid' | 'assertion_expired' | 'assertion_replayed'
  | 'assertion_binding_mismatch' | 'stale_generation';
type AuditDegradedReason = 'store_unavailable' | 'rate_limited' | 'internal_failure';

type SecurityEventCommon = {
  methodClass: 'read' | 'write' | 'delete' | 'other';
  requestId: string;
};

export type GatewaySecurityEventInput = SecurityEventCommon & (
  | { eventType: 'authentication_denied'; reasonCode: AuthenticationDenialReason }
  | { eventType: 'authorization_denied'; reasonCode: AuthorizationDenialReason }
  | { eventType: 'principal_assertion_denied'; reasonCode: PrincipalAssertionDenialReason }
  | { eventType: 'audit_degraded'; reasonCode: AuditDegradedReason }
);

export class BoundedSecurityEventReporter {
  private readonly client: TeamPrincipalClient;
  private readonly log: (message: string) => void;
  private readonly queue: Array<{ event: GatewaySecurityEventInput; count: number; key: string }> = [];
  private active = false;
  private scheduled = false;
  private lastDegradedLog = Number.NEGATIVE_INFINITY;
  private readonly idleWaiters: Array<() => void> = [];

  constructor(client: TeamPrincipalClient, options: { log?: (message: string) => void } = {}) {
    this.client = client;
    this.log = options.log ?? ((message) => console.error(message));
  }

  report(event: GatewaySecurityEventInput): void {
    const key = `${event.eventType}|${event.reasonCode}|${event.methodClass}`;
    const existing = this.queue.find((item) => item.key === key && item.count < 256);
    if (existing) {
      existing.count++;
    } else if (this.queue.length < 32) {
      this.queue.push({ event: { ...event }, count: 1, key });
    } else {
      this.signalDegraded('[pulse-mcp] team security audit degraded');
    }
    if (!this.scheduled && !this.active) {
      this.scheduled = true;
      queueMicrotask(() => void this.pump());
    }
  }

  async drain(): Promise<void> {
    if (!this.active && !this.scheduled && this.queue.length === 0) return;
    await new Promise<void>((resolve) => this.idleWaiters.push(resolve));
  }

  private async pump(): Promise<void> {
    this.scheduled = false;
    if (this.active) return;
    this.active = true;
    while (this.queue.length > 0) {
      const item = this.queue.shift();
      if (!item) break;
      try {
        await this.client.recordSecurityEvent({ ...item.event, count: item.count });
      } catch (error) {
        this.signalDegraded(error instanceof SecurityEventRateLimitedError
          ? '[pulse-mcp] team security audit rate limited'
          : '[pulse-mcp] team security audit degraded');
      }
    }
    this.active = false;
    for (const resolve of this.idleWaiters.splice(0)) resolve();
    if (this.queue.length > 0 && !this.scheduled) {
      this.scheduled = true;
      queueMicrotask(() => void this.pump());
    }
  }

  private signalDegraded(message: string): void {
    const now = Date.now();
    if (now - this.lastDegradedLog < 60_000) return;
    this.lastDegradedLog = now;
    this.log(message);
  }
}

export interface TeamRequestSecurityOptions {
  verifier: OAuthResourceVerifier;
  principalClient: TeamPrincipalClient;
}

export class TeamRequestSecurity {
  readonly verifier: OAuthResourceVerifier;
  readonly principalClient: TeamPrincipalClient;

  constructor(options: TeamRequestSecurityOptions) {
    this.verifier = options.verifier;
    this.principalClient = options.principalClient;
  }

  authenticateBeforeBody(authorization: string | string[] | undefined): Promise<VerifiedOAuthIdentity> {
    return this.verifier.verifyAuthorization(authorization, ['pulse:connect']);
  }

  async resolveAfterBody(input: {
    authorization: string | string[] | undefined;
    baseline: VerifiedOAuthIdentity;
    body: unknown;
    requestId: string;
  }): Promise<Readonly<TeamPrincipalContext>> {
    const required = requiredTeamCapabilities(input.body);
    const verified = await this.verifier.verifyAuthorization(input.authorization, required);
    if (!sameOAuthIdentity(input.baseline, verified)) {
      throw new PrincipalCheckError('invalid_principal');
    }
    return this.principalClient.check(verified, input.requestId);
  }

  resolveBaseline(
    baseline: VerifiedOAuthIdentity,
    requestId: string,
  ): Promise<Readonly<TeamPrincipalContext>> {
    return this.principalClient.check(baseline, requestId);
  }
}

export class TeamPrincipalClient {
  private readonly principalEndpoint: string;
  private readonly securityEventEndpoint: string;
  private readonly teamMemoryEndpoint: string;
  private readonly signer: PrincipalSigner;
  private readonly apiKey: () => string;
  private readonly fetcher: typeof fetch;
  private readonly timeoutMs: number;

  constructor(options: TeamPrincipalClientOptions) {
    this.principalEndpoint = teamDaemonEndpoint(options.daemonBaseURL, '/team/v1/principal/check');
    this.securityEventEndpoint = teamDaemonEndpoint(options.daemonBaseURL, '/team/v1/security-events');
    this.teamMemoryEndpoint = teamDaemonEndpoint(options.daemonBaseURL, TEAM_MEMORY_REMEMBER_PATH);
    this.signer = options.signer;
    this.apiKey = options.apiKey;
    this.fetcher = options.fetch ?? fetch;
    this.timeoutMs = options.timeoutMs ?? 3_000;
    if (!Number.isInteger(this.timeoutMs) || this.timeoutMs < 100 || this.timeoutMs > 5_000) {
      throw new Error('principal check timeout is invalid');
    }
  }

  bindDomain(
    identity: Readonly<Omit<VerifiedOAuthIdentity, 'capabilities'> & { capabilities: readonly TeamCapability[] }>,
    context: Readonly<TeamPrincipalContext>,
  ): Readonly<BoundTeamDomain> {
    const capabilities = sortedCapabilities(identity.capabilities);
    if (
      context.store_id !== this.signer.storeId || context.team_id !== this.signer.teamId ||
      JSON.stringify(sortedCapabilities(context.capabilities)) !== JSON.stringify(capabilities) ||
      !capabilities.includes('pulse:connect') || !capabilities.includes('pulse:write')
    ) {
      throw new TeamDomainError('invalid_principal');
    }
    const boundIdentity = Object.freeze({
      issuer: requireBoundedIdentity(identity.issuer),
      subject: requireBoundedIdentity(identity.subject),
      clientId: requireBoundedIdentity(identity.clientId),
      capabilities: Object.freeze(capabilities),
    });
    const requestId = requireOpaqueValue(context.request_id, 'request ID');
    return Object.freeze({
      remember: (input: unknown) => this.remember(boundIdentity, requestId, input),
    });
  }

  async check(identity: VerifiedOAuthIdentity, requestId: string): Promise<Readonly<TeamPrincipalContext>> {
    const body = identityCheckBody(identity);
    const assertion = await this.signer.signPrincipalCheck({
      requestId,
      method: 'POST',
      path: '/team/v1/principal/check',
      body: body.bytes,
      oauthIssuer: identity.issuer,
      oauthSubject: identity.subject,
      oauthClientId: identity.clientId,
      capabilities: identity.capabilities,
    });
    const ipcKey = this.apiKey();
    if (ipcKey === '' || Buffer.byteLength(ipcKey, 'utf8') > 512) {
      throw new Error('team principal check is unavailable');
    }
    let response: Response;
    try {
      response = await this.fetcher(this.principalEndpoint, {
        method: 'POST',
        redirect: 'error',
        signal: AbortSignal.timeout(this.timeoutMs),
        headers: {
          'Content-Type': 'application/json',
          'X-Pulse-Key': ipcKey,
          'X-Pulse-Principal': assertion,
          'X-Pulse-Request-ID': requestId,
        },
        body: body.text,
      });
    } catch {
      throw new PrincipalCheckError('principal_store_unavailable');
    }
    if (!response.ok) {
      let value: unknown;
      try {
        value = await readBoundedJSONResponse(response, 4 * 1024);
      } catch {
        throw new PrincipalCheckError('invalid_principal');
      }
      if (response.status === 403 && isExactRecord(value, ['error']) && value.error === 'principal_revoked') {
        throw new PrincipalCheckError('principal_revoked');
      }
      if (response.status === 503 && isExactRecord(value, ['error']) && value.error === 'principal_store_unavailable') {
        throw new PrincipalCheckError('principal_store_unavailable');
      }
      if (response.status === 401 && isExactRecord(value, ['error']) && value.error === 'principal_replay') {
        throw new PrincipalCheckError('principal_replayed');
      }
      if (response.status === 401 && isExactRecord(value, ['error']) && value.error === 'principal_request_mismatch') {
        throw new PrincipalCheckError('principal_binding_mismatch');
      }
      throw new PrincipalCheckError('invalid_principal');
    }
    try {
      const value = await readBoundedJSONResponse(response, 16 * 1024);
      return validatePrincipalContext(value, identity, requestId, this.signer.storeId, this.signer.teamId);
    } catch {
      throw new PrincipalCheckError('invalid_principal');
    }
  }

  async recordSecurityEvent(input: GatewaySecurityEventInput & { count?: number }): Promise<void> {
    const requestId = requireOpaqueValue(input.requestId, 'request ID');
    const count = input.count ?? 1;
    if (!Number.isInteger(count) || count < 1 || count > 256) {
      throw new Error('team security event count is invalid');
    }
    const body = JSON.stringify({
      event_type: input.eventType,
      reason_code: input.reasonCode,
      method_class: input.methodClass,
      path_class: 'mcp',
      request_id: requestId,
      count,
    });
    const assertion = await this.signer.signSecurityEvent(requestId, Buffer.from(body));
    const ipcKey = this.apiKey();
    if (ipcKey === '' || Buffer.byteLength(ipcKey, 'utf8') > 512) {
      throw new Error('team security event storage is unavailable');
    }
    let response: Response;
    try {
      response = await this.fetcher(this.securityEventEndpoint, {
        method: 'POST',
        redirect: 'error',
        signal: AbortSignal.timeout(this.timeoutMs),
        headers: {
          'Content-Type': 'application/json',
          'X-Pulse-Key': ipcKey,
          'X-Pulse-Gateway-Assertion': assertion,
          'X-Pulse-Request-ID': requestId,
        },
        body,
      });
    } catch {
      throw new Error('team security event storage is unavailable');
    }
    if (response.status === 429) {
      await response.body?.cancel().catch(() => undefined);
      throw new SecurityEventRateLimitedError();
    }
    if (!response.ok) {
      await response.body?.cancel().catch(() => undefined);
      throw new Error('team security event storage is unavailable');
    }
    await response.body?.cancel().catch(() => undefined);
  }

  private async remember(
    identity: Readonly<Omit<VerifiedOAuthIdentity, 'capabilities'> & { capabilities: readonly TeamCapability[] }>,
    requestId: string,
    input: unknown,
  ): Promise<TeamRememberResult> {
    const body = canonicalTeamRememberBody(input);
    let assertion: string;
    try {
      assertion = await this.signer.signDomainRequest({
        requestId,
        method: 'POST',
        path: TEAM_MEMORY_REMEMBER_PATH,
        body: body.bytes,
        oauthIssuer: identity.issuer,
        oauthSubject: identity.subject,
        oauthClientId: identity.clientId,
        capabilities: identity.capabilities,
      });
    } catch {
      throw new TeamDomainError('shared_memory_unavailable');
    }
    const ipcKey = this.apiKey();
    if (ipcKey === '' || Buffer.byteLength(ipcKey, 'utf8') > 512) {
      throw new TeamDomainError('shared_memory_unavailable');
    }
    let response: Response;
    try {
      response = await this.fetcher(this.teamMemoryEndpoint, {
        method: 'POST',
        redirect: 'error',
        signal: AbortSignal.timeout(this.timeoutMs),
        headers: {
          'Content-Type': 'application/json',
          'X-Pulse-Key': ipcKey,
          'X-Pulse-Principal': assertion,
          'X-Pulse-Request-ID': requestId,
        },
        body: body.text,
      });
    } catch {
      throw new TeamDomainError('shared_memory_unavailable');
    }
    if (!isJSONResponse(response)) {
      await response.body?.cancel().catch(() => undefined);
      throw new TeamDomainError('shared_memory_unavailable');
    }
    let value: unknown;
    try {
      value = await readBoundedJSONResponse(response, response.status === 200 ? 16 * 1024 : 4 * 1024);
    } catch {
      throw new TeamDomainError('shared_memory_unavailable');
    }
    if (response.status !== 200) {
      const code = exactTeamDomainError(response.status, value);
      throw new TeamDomainError(code ?? 'shared_memory_unavailable');
    }
    try {
      return validateTeamRememberResult(value, body.value.items.length);
    } catch {
      throw new TeamDomainError('shared_memory_unavailable');
    }
  }
}

export class PrincipalSigner {
  readonly publicKeyring: PrincipalVerificationKeyring;
  readonly storeId: string;
  readonly teamId: string;
  private readonly privateKey: KeyObject;
  private readonly keyId: string;
  private readonly now: () => number;
  private readonly randomId: () => string;

  constructor(
    options: PrincipalSignerOptions,
    privateKey: KeyObject,
    publicKeyring: PrincipalVerificationKeyring,
  ) {
    this.privateKey = privateKey;
    this.publicKeyring = publicKeyring;
    this.keyId = options.keyId;
    this.storeId = requireOpaqueValue(options.storeId, 'store ID');
    this.teamId = requireOpaqueValue(options.teamId, 'team ID');
    this.now = options.now ?? (() => Math.floor(Date.now() / 1000));
    this.randomId = options.randomId ?? randomUUID;
  }

  async signPrincipalCheck(input: PrincipalCheckInput): Promise<string> {
    if (input.method !== 'POST' || input.path !== '/team/v1/principal/check') {
      throw new Error('principal assertion request binding is invalid');
    }
    return this.signPrincipalRequest(input);
  }

  async signDomainRequest(input: PrincipalCheckInput): Promise<string> {
    if (input.method !== 'POST' || input.path !== TEAM_MEMORY_REMEMBER_PATH) {
      throw new Error('principal assertion request binding is invalid');
    }
    return this.signPrincipalRequest(input);
  }

  private async signPrincipalRequest(input: PrincipalCheckInput): Promise<string> {
    const now = this.now();
    const capabilities = sortedCapabilities(input.capabilities);
    return new SignJWT({
      version: PRINCIPAL_ASSERTION_VERSION,
      request_id: requireOpaqueValue(input.requestId, 'request ID'),
      method: input.method,
      path: input.path,
      body_sha256: createHash('sha256').update(input.body).digest('hex'),
      store_id: this.storeId,
      team_id: this.teamId,
      oauth_issuer: requireBoundedIdentity(input.oauthIssuer),
      oauth_subject: requireBoundedIdentity(input.oauthSubject),
      oauth_client_id: requireBoundedIdentity(input.oauthClientId),
      grant_kind: 'registered',
      capabilities,
    })
      .setProtectedHeader({ alg: 'EdDSA', kid: this.keyId, typ: PRINCIPAL_ASSERTION_VERSION })
      .setIssuer(PRINCIPAL_ASSERTION_ISSUER)
      .setAudience(PRINCIPAL_ASSERTION_AUDIENCE)
      .setIssuedAt(now)
      .setNotBefore(now - 1)
      .setExpirationTime(now + 30)
      .setJti(this.randomId())
      .sign(this.privateKey);
  }

  async signSecurityEvent(requestId: string, body: Uint8Array): Promise<string> {
    const now = this.now();
    return new SignJWT({
      version: SECURITY_EVENT_ASSERTION_VERSION,
      request_id: requireOpaqueValue(requestId, 'request ID'),
      method: 'POST',
      path: '/team/v1/security-events',
      body_sha256: createHash('sha256').update(body).digest('hex'),
      store_id: this.storeId,
      team_id: this.teamId,
    })
      .setProtectedHeader({ alg: 'EdDSA', kid: this.keyId, typ: SECURITY_EVENT_ASSERTION_VERSION })
      .setIssuer(PRINCIPAL_ASSERTION_ISSUER)
      .setAudience(PRINCIPAL_ASSERTION_AUDIENCE)
      .setIssuedAt(now)
      .setNotBefore(now - 1)
      .setExpirationTime(now + 30)
      .setJti(this.randomId())
      .sign(this.privateKey);
  }
}

export function loadPrincipalSigner(options: PrincipalSignerOptions): PrincipalSigner {
  if (!/^[A-Za-z0-9._-]{1,128}$/.test(options.keyId)) {
    throw new Error('principal signing key configuration is invalid');
  }
  const privateBytes = readStrictOwnerFile(options.privateKeyFile, MAX_PRIVATE_KEY_BYTES, 'signing key file');
  let privateKey: KeyObject;
  try {
    privateKey = createPrivateKey(privateBytes);
  } catch {
    try {
      privateKey = createPrivateKey({ key: privateBytes, format: 'der', type: 'pkcs8' });
    } catch {
      throw new Error('principal signing key file is invalid');
    }
  }
  if (privateKey.asymmetricKeyType !== 'ed25519') {
    throw new Error('principal signing key file is invalid');
  }
  const keyringBytes = readStrictOwnerFile(options.verifyKeyringFile, MAX_KEYRING_BYTES, 'verification keyring file');
  const keyring = parseVerificationKeyring(keyringBytes);
  const derived = createPublicKey(privateKey).export({ format: 'jwk' });
  if (
    keyring.active.kid !== options.keyId ||
    typeof derived.x !== 'string' ||
    !constantTimeTextEqual(keyring.active.public_key, derived.x)
  ) {
    throw new Error('principal signing key does not match active verification keyring');
  }
  return new PrincipalSigner(options, privateKey, keyring);
}

export function stablePrincipalCheckBody(input: PrincipalCheckBodyInput): { text: string; bytes: Buffer } {
  const value = {
    oauth_issuer: requireBoundedIdentity(input.oauthIssuer),
    oauth_subject: requireBoundedIdentity(input.oauthSubject),
    oauth_client_id: requireBoundedIdentity(input.oauthClientId),
    capabilities: sortedCapabilities(input.capabilities),
  };
  const text = JSON.stringify(value);
  return { text, bytes: Buffer.from(text, 'utf8') };
}

export function identityCheckBody(identity: VerifiedOAuthIdentity) {
  return stablePrincipalCheckBody({
    oauthIssuer: identity.issuer,
    oauthSubject: identity.subject,
    oauthClientId: identity.clientId,
    capabilities: identity.capabilities,
  });
}

function readStrictOwnerFile(path: string, maxBytes: number, label: string): Buffer {
  let fd: number | undefined;
  try {
    fd = openSync(path, fsConstants.O_RDONLY | fsConstants.O_NOFOLLOW);
    const stat = fstatSync(fd);
    const uid = typeof process.getuid === 'function' ? process.getuid() : stat.uid;
    if (!stat.isFile() || stat.uid !== uid || (stat.mode & 0o077) !== 0 || stat.size < 1 || stat.size > maxBytes) {
      throw new Error('unsafe');
    }
    return readFileSync(fd);
  } catch {
    throw new Error(`principal ${label} is invalid`);
  } finally {
    if (fd !== undefined) closeSync(fd);
  }
}

function parseVerificationKeyring(bytes: Buffer): PrincipalVerificationKeyring {
  let value: unknown;
  try {
    value = JSON.parse(bytes.toString('utf8'));
  } catch {
    throw new Error('principal verification keyring is invalid');
  }
  if (!isExactRecord(value, ['active', 'previous'])) {
    throw new Error('principal verification keyring is invalid');
  }
  const active = parsePublicEntry(value.active);
  if (!Array.isArray(value.previous) || value.previous.length > MAX_PREVIOUS_KEYS) {
    throw new Error('principal verification keyring is invalid');
  }
  const previous = value.previous.map(parsePublicEntry);
  const kids = new Set([active.kid]);
  for (const entry of previous) {
    if (kids.has(entry.kid)) {
      throw new Error('principal verification keyring is invalid');
    }
    kids.add(entry.kid);
  }
  return { active, previous };
}

function parsePublicEntry(value: unknown): { kid: string; public_key: string } {
  if (!isExactRecord(value, ['kid', 'public_key'])) {
    throw new Error('principal verification keyring is invalid');
  }
  if (
    typeof value.kid !== 'string' || !/^[A-Za-z0-9._-]{1,128}$/.test(value.kid) ||
    typeof value.public_key !== 'string' || !/^[A-Za-z0-9_-]{43}$/.test(value.public_key)
  ) {
    throw new Error('principal verification keyring is invalid');
  }
  let decoded: Buffer;
  try {
    decoded = Buffer.from(value.public_key, 'base64url');
  } catch {
    throw new Error('principal verification keyring is invalid');
  }
  if (decoded.length !== 32 || decoded.toString('base64url') !== value.public_key) {
    throw new Error('principal verification keyring is invalid');
  }
  return { kid: value.kid, public_key: value.public_key };
}

function isExactRecord(value: unknown, keys: readonly string[]): value is Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  const actual = Object.keys(value as Record<string, unknown>).sort();
  return actual.length === keys.length && actual.every((key, index) => key === [...keys].sort()[index]);
}

function isJSONResponse(response: Response): boolean {
  return response.headers.get('content-type')?.split(';', 1)[0]?.trim().toLowerCase() === 'application/json';
}

function exactTeamDomainError(status: number, value: unknown): TeamDomainErrorCode | undefined {
  if (!isExactRecord(value, ['error', 'fallback']) || value.fallback !== false || typeof value.error !== 'string') {
    return undefined;
  }
  const allowed: Partial<Record<number, readonly TeamDomainErrorCode[]>> = {
    400: ['invalid_team_memory'],
    401: ['invalid_principal', 'principal_request_mismatch', 'principal_replay'],
    403: ['principal_revoked', 'policy_denied'],
    404: ['not_found'],
    409: [
      'idempotency_conflict', 'idempotency_in_progress',
      'idempotency_failed', 'authorization_stale',
    ],
    503: ['shared_memory_unavailable'],
  };
  const code = value.error as TeamDomainErrorCode;
  return allowed[status]?.includes(code) ? code : undefined;
}

function sortedCapabilities(values: readonly TeamCapability[]): TeamCapability[] {
  if (
    !Array.isArray(values) ||
    values.length > TEAM_CAPABILITIES.length ||
    values.some((value) => !(TEAM_CAPABILITIES as readonly string[]).includes(value))
  ) {
    throw new Error('principal capabilities are invalid');
  }
  return [...new Set(values)].sort();
}

function requireOpaqueValue(value: string, label: string): string {
  const pattern = label === 'request ID'
    ? /^[A-Za-z0-9._:-]{8,64}$/
    : /^[A-Za-z0-9._:-]{1,256}$/;
  if (!pattern.test(value)) {
    throw new Error(`principal ${label} is invalid`);
  }
  return value;
}

function requireBoundedIdentity(value: string): string {
  if (
    typeof value !== 'string' || Buffer.byteLength(value, 'utf8') < 1 ||
    Buffer.byteLength(value, 'utf8') > 512 || value.trim() !== value ||
    /[\u0000-\u001f\u007f]/.test(value)
  ) {
    throw new Error('principal identity field is invalid');
  }
  return value;
}

function constantTimeTextEqual(left: string, right: string): boolean {
  const leftDigest = createHash('sha256').update(left).digest();
  const rightDigest = createHash('sha256').update(right).digest();
  return left.length === right.length && timingSafeEqual(leftDigest, rightDigest);
}

function teamDaemonEndpoint(baseURL: string, path: string): string {
  let url: URL;
  try {
    url = new URL(baseURL);
  } catch {
    throw new Error('team daemon endpoint is invalid');
  }
  const host = url.hostname.toLowerCase();
  if (
    !['http:', 'https:'].includes(url.protocol) ||
    (host !== '127.0.0.1' && host !== '::1' && host !== '[::1]') ||
    url.username !== '' || url.password !== '' || url.search !== '' || url.hash !== ''
  ) {
    throw new Error('team daemon endpoint is invalid');
  }
  url.pathname = `${url.pathname.replace(/\/$/, '')}${path}`;
  return url.toString();
}

async function readBoundedJSONResponse(response: Response, maxBytes: number): Promise<unknown> {
  const reader = response.body?.getReader();
  if (!reader) throw new Error('team principal response is invalid');
  const chunks: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > maxBytes) {
      await reader.cancel();
      throw new Error('team principal response is invalid');
    }
    chunks.push(value);
  }
  try {
    return JSON.parse(Buffer.concat(chunks.map((chunk) => Buffer.from(chunk)), size).toString('utf8'));
  } catch {
    throw new Error('team principal response is invalid');
  }
}

function validatePrincipalContext(
  value: unknown,
  identity: VerifiedOAuthIdentity,
  requestId: string,
  storeId: string,
  teamId: string,
): Readonly<TeamPrincipalContext> {
  const keys = [
    'agent_binding_id', 'binding_auth_epoch', 'capabilities', 'human_principal_id',
    'membership_auth_epoch', 'membership_id', 'membership_role', 'principal_auth_epoch',
    'oauth_client_key', 'principal_id', 'principal_kind', 'request_id', 'store_id', 'team_auth_epoch',
    'team_id', 'version',
  ];
  if (!isExactRecord(value, keys)) throw new Error('team principal response is invalid');
  const context = value as unknown as TeamPrincipalContext;
  const expectedCapabilities = sortedCapabilities(identity.capabilities);
  const agentFieldsValid = context.principal_kind === 'agent'
    ? isOpaque(context.human_principal_id) && isOpaque(context.agent_binding_id) && positiveInteger(context.binding_auth_epoch)
    : context.principal_kind === 'service' && context.human_principal_id === null && context.agent_binding_id === null && context.binding_auth_epoch === null;
  if (
    context.version !== 'pulse.team.principal_context.v1' ||
    context.request_id !== requestId || context.store_id !== storeId || context.team_id !== teamId ||
    !isOpaque(context.principal_id) || !/^[0-9a-f]{64}$/.test(context.oauth_client_key) ||
    !agentFieldsValid || !isOpaque(context.membership_id) ||
    !['owner', 'member', 'reviewer'].includes(context.membership_role) ||
    !positiveInteger(context.team_auth_epoch) || !positiveInteger(context.principal_auth_epoch) ||
    !positiveInteger(context.membership_auth_epoch) ||
    !Array.isArray(context.capabilities) ||
    JSON.stringify(sortedCapabilities(context.capabilities)) !== JSON.stringify(expectedCapabilities)
  ) {
    throw new Error('team principal response is invalid');
  }
  return deepFreeze(context);
}

function positiveInteger(value: unknown): value is number {
  return Number.isInteger(value) && (value as number) > 0;
}

function isOpaque(value: unknown): value is string {
  return typeof value === 'string' && /^[A-Za-z0-9._:-]{1,256}$/.test(value);
}

function deepFreeze<T>(value: T): Readonly<T> {
  if (value && typeof value === 'object') {
    for (const child of Object.values(value as Record<string, unknown>)) deepFreeze(child);
    Object.freeze(value);
  }
  return value;
}

function sameOAuthIdentity(left: VerifiedOAuthIdentity, right: VerifiedOAuthIdentity): boolean {
  return left.issuer === right.issuer && left.subject === right.subject &&
    left.clientId === right.clientId &&
    JSON.stringify(left.capabilities) === JSON.stringify(right.capabilities);
}
