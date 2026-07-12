import { createHash } from 'node:crypto';

import type { VerifiedOAuthIdentity } from './oauth-resource.js';
import {
  OWNER_STEP_UP_ASSERTION_VERSION,
  PrincipalSigner,
} from './principal-context.js';

export { OWNER_STEP_UP_ASSERTION_VERSION };

export const OWNER_APPROVAL_PUBLIC_PATH = '/owner/v1/approval';
export const OWNER_BOOTSTRAP_PUBLIC_PATH = '/owner/v1/bootstrap';
export const OWNER_ACTIVATE_PUBLIC_PATH = '/owner/v1/activate';

export const OWNER_APPROVAL_INTERNAL_PATH = '/team/v1/owner/approval';
export const OWNER_BOOTSTRAP_INTERNAL_PATH = '/team/v1/owner/bootstrap';
export const OWNER_ACTIVATE_INTERNAL_PATH = '/team/v1/owner/activate';

const OWNER_APPROVAL_SCHEMA = 'pulse.team.owner.approval.v1';
const OWNER_APPROVAL_RESULT_SCHEMA = 'pulse.team.owner.approval_result.v1';
const OWNER_BOOTSTRAP_SCHEMA = 'pulse.team.owner.bootstrap.v1';
const OWNER_BOOTSTRAP_RESULT_SCHEMA = 'pulse.team.owner.bootstrap_result.v1';
const OWNER_ACTIVATE_SCHEMA = 'pulse.team.owner.activate.v1';
const OWNER_ACTIVATE_RESULT_SCHEMA = 'pulse.team.owner.activate_result.v1';
const OWNER_MAX_BODY_BYTES = 64 * 1024;
const OWNER_MAX_RESPONSE_BYTES = 64 * 1024;
const OWNER_CAPABILITY = 'pulse:owner';

const OWNER_PUBLIC_PATHS = new Set([
  OWNER_APPROVAL_PUBLIC_PATH,
  OWNER_BOOTSTRAP_PUBLIC_PATH,
  OWNER_ACTIVATE_PUBLIC_PATH,
]);
const SAFE_OPAQUE = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$/;
const HEX_DIGEST = /^[0-9a-f]{64}$/;
const UNSAFE_SECRET = /(?:authorization:\s*bearer|api[_-]?key|password|private[_-]?key|begin private key|\bsk-[A-Za-z0-9_-]{12,}\b|\bghp_[A-Za-z0-9_]{12,}\b)/i;
const UNSAFE_PATH = /(?:\/(?:users|home|etc|var|private|volumes)\/|file:\/\/|(?:^|\s)~\/|(?:^|\s)[a-z]:\\|\\\\[^\\\s]+\\)/i;

export type OwnerPublicPath =
  | typeof OWNER_APPROVAL_PUBLIC_PATH
  | typeof OWNER_BOOTSTRAP_PUBLIC_PATH
  | typeof OWNER_ACTIVATE_PUBLIC_PATH;

export type OwnerGatewayErrorCode =
  | 'invalid_owner_request'
  | 'owner_step_up_required'
  | 'owner_operation_denied'
  | 'owner_service_unavailable';

export class OwnerGatewayError extends Error {
  readonly code: OwnerGatewayErrorCode;
  readonly status: 400 | 403 | 503;

  constructor(code: OwnerGatewayErrorCode) {
    super(code === 'invalid_owner_request'
      ? 'Owner request is invalid'
      : code === 'owner_step_up_required'
        ? 'Recent browser approval is required'
        : code === 'owner_operation_denied'
          ? 'Owner operation was denied'
          : 'Owner service is unavailable');
    this.name = 'OwnerGatewayError';
    this.code = code;
    this.status = code === 'invalid_owner_request' ? 400
      : code === 'owner_service_unavailable' ? 503 : 403;
  }
}

export interface OwnerBootstrapIntent {
  store_id: string;
  team_id: string;
  owner_principal_id: string;
  owner_membership_id: string;
}

export interface CleanOwnerBootstrapApproval {
  schema: typeof OWNER_APPROVAL_SCHEMA;
  action: 'team.bootstrap';
  store_id: string;
  team_id: string;
  target_kind: 'team';
  target_id: string;
  target_digest: string;
  team_name: string;
  bootstrap_intent: OwnerBootstrapIntent;
}

export interface CleanOwnerActivationApproval {
  schema: typeof OWNER_APPROVAL_SCHEMA;
  action: 'team.activation.synthetic';
  store_id: string;
  team_id: string;
  target_kind: 'team_activation';
  target_id: string;
  target_digest: string;
  gate_digest: string;
}

export type CleanOwnerApproval = CleanOwnerBootstrapApproval | CleanOwnerActivationApproval;

export type CleanOwnerBootstrap =
  | { schema: typeof OWNER_BOOTSTRAP_SCHEMA; operation: 'prepare' }
  | {
      schema: typeof OWNER_BOOTSTRAP_SCHEMA;
      operation: 'execute';
      team_name: string;
      bootstrap_intent: OwnerBootstrapIntent;
      approval_nonce: string;
    };

export interface CleanOwnerActivate {
  schema: typeof OWNER_ACTIVATE_SCHEMA;
  approval_nonce: string;
  gate_digest: string;
}

interface CanonicalOwnerBody<T> {
  value: T;
  text: string;
  bytes: Buffer;
}

export function isOwnerPublicPath(value: string): value is OwnerPublicPath {
  return OWNER_PUBLIC_PATHS.has(value);
}

export function isExactOwnerBrowserRequest(input: {
  origin: string;
  host: string;
  publicBaseURL: string;
  allowedOrigins: ReadonlySet<string>;
}): boolean {
  try {
    const base = new URL(input.publicBaseURL);
    const origin = new URL(input.origin);
    return input.publicBaseURL === base.origin && base.protocol === 'https:' &&
      input.host === base.host && input.origin === origin.origin &&
      input.allowedOrigins.has(input.origin);
  } catch {
    return false;
  }
}

export function canonicalOwnerApprovalBody(input: unknown): CanonicalOwnerBody<CleanOwnerApproval> {
  const envelope = ownerRecord(input, [
    'schema', 'action', 'store_id', 'team_id', 'target_kind', 'target_id',
    'target_digest', 'team_name', 'bootstrap_intent', 'gate_digest',
  ]);
  if (envelope.schema !== OWNER_APPROVAL_SCHEMA) invalidOwnerRequest();
  const action = envelope.action;
  const storeID = ownerOpaque(envelope.store_id);
  const teamID = ownerOpaque(envelope.team_id);
  const targetID = ownerOpaque(envelope.target_id);
  const targetDigest = ownerDigest(envelope.target_digest);
  if (targetID !== teamID) invalidOwnerRequest();
  if (action === 'team.bootstrap') {
    if (envelope.target_kind !== 'team' || envelope.gate_digest !== undefined) invalidOwnerRequest();
    const intent = ownerBootstrapIntent(envelope.bootstrap_intent);
    const teamName = ownerTeamName(envelope.team_name);
    if (
      intent.store_id !== storeID || intent.team_id !== teamID ||
      targetDigest !== ownerBootstrapTargetDigest(intent, teamName)
    ) invalidOwnerRequest();
    return canonicalOwnerBody({
      schema: OWNER_APPROVAL_SCHEMA,
      action,
      store_id: storeID,
      team_id: teamID,
      target_kind: 'team',
      target_id: targetID,
      target_digest: targetDigest,
      team_name: teamName,
      bootstrap_intent: intent,
    });
  }
  if (action === 'team.activation.synthetic') {
    if (
      envelope.target_kind !== 'team_activation' || envelope.team_name !== undefined ||
      envelope.bootstrap_intent !== undefined
    ) invalidOwnerRequest();
    const gateDigest = ownerDigest(envelope.gate_digest);
    if (targetDigest !== ownerActivationTargetDigest(storeID, teamID, gateDigest)) {
      invalidOwnerRequest();
    }
    return canonicalOwnerBody({
      schema: OWNER_APPROVAL_SCHEMA,
      action,
      store_id: storeID,
      team_id: teamID,
      target_kind: 'team_activation',
      target_id: targetID,
      target_digest: targetDigest,
      gate_digest: gateDigest,
    });
  }
  invalidOwnerRequest();
}

export function ownerBootstrapTargetDigest(
  intent: OwnerBootstrapIntent,
  teamName: string,
): string {
  const cleanIntent = ownerBootstrapIntent(intent);
  const cleanTeamName = ownerTeamName(teamName);
  return ownerApprovalDigest(
    'team.bootstrap', cleanIntent.store_id, cleanIntent.team_id,
    cleanIntent.owner_principal_id, cleanIntent.owner_membership_id, cleanTeamName,
  );
}

export function ownerActivationTargetDigest(
  storeID: string,
  teamID: string,
  gateDigest: string,
): string {
  return ownerApprovalDigest(
    'team.activation.synthetic', ownerOpaque(storeID), ownerOpaque(teamID),
    ownerDigest(gateDigest),
  );
}

function ownerApprovalDigest(...parts: string[]): string {
  const hash = createHash('sha256');
  for (const part of parts) {
    const bytes = Buffer.from(part, 'utf8');
    const size = Buffer.alloc(8);
    size.writeBigUInt64BE(BigInt(bytes.byteLength));
    hash.update(size);
    hash.update(bytes);
  }
  return hash.digest('hex');
}

export function canonicalOwnerBootstrapBody(input: unknown): CanonicalOwnerBody<CleanOwnerBootstrap> {
  const envelope = ownerRecord(input, [
    'schema', 'operation', 'team_name', 'bootstrap_intent', 'approval_nonce',
  ]);
  if (envelope.schema !== OWNER_BOOTSTRAP_SCHEMA) invalidOwnerRequest();
  if (envelope.operation === 'prepare') {
    if (
      envelope.team_name !== undefined || envelope.bootstrap_intent !== undefined ||
      envelope.approval_nonce !== undefined
    ) invalidOwnerRequest();
    return canonicalOwnerBody({ schema: OWNER_BOOTSTRAP_SCHEMA, operation: 'prepare' });
  }
  if (envelope.operation !== 'execute') invalidOwnerRequest();
  return canonicalOwnerBody({
    schema: OWNER_BOOTSTRAP_SCHEMA,
    operation: 'execute',
    team_name: ownerTeamName(envelope.team_name),
    bootstrap_intent: ownerBootstrapIntent(envelope.bootstrap_intent),
    approval_nonce: ownerDigest(envelope.approval_nonce),
  });
}

export function canonicalOwnerActivateBody(input: unknown): CanonicalOwnerBody<CleanOwnerActivate> {
  const envelope = ownerRecord(input, ['schema', 'approval_nonce', 'gate_digest']);
  if (envelope.schema !== OWNER_ACTIVATE_SCHEMA) invalidOwnerRequest();
  return canonicalOwnerBody({
    schema: OWNER_ACTIVATE_SCHEMA,
    approval_nonce: ownerDigest(envelope.approval_nonce),
    gate_digest: ownerDigest(envelope.gate_digest),
  });
}

function canonicalOwnerBody<T>(value: T): CanonicalOwnerBody<T> {
  const text = JSON.stringify(value);
  const bytes = Buffer.from(text, 'utf8');
  if (bytes.byteLength > OWNER_MAX_BODY_BYTES) invalidOwnerRequest();
  return { value, text, bytes };
}

function ownerRecord(value: unknown, fields: readonly string[]): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) invalidOwnerRequest();
  const result = value as Record<string, unknown>;
  const allowed = new Set(fields);
  if (Object.keys(result).some((key) => !allowed.has(key)) || Object.values(result).some((entry) => entry === null)) {
    invalidOwnerRequest();
  }
  return result;
}

function ownerBootstrapIntent(value: unknown): OwnerBootstrapIntent {
  const intent = ownerRecord(value, [
    'store_id', 'team_id', 'owner_principal_id', 'owner_membership_id',
  ]);
  const result = {
    store_id: ownerOpaque(intent.store_id),
    team_id: ownerOpaque(intent.team_id),
    owner_principal_id: ownerOpaque(intent.owner_principal_id),
    owner_membership_id: ownerOpaque(intent.owner_membership_id),
  };
  if (
    !result.store_id.startsWith('store_') || !result.team_id.startsWith('team_') ||
    !result.owner_principal_id.startsWith('principal_') ||
    !result.owner_membership_id.startsWith('membership_')
  ) invalidOwnerRequest();
  return result;
}

function ownerOpaque(value: unknown): string {
  if (typeof value !== 'string' || !SAFE_OPAQUE.test(value) || unsafeOwnerText(value)) {
    invalidOwnerRequest();
  }
  return value;
}

function ownerDigest(value: unknown): string {
  if (typeof value !== 'string' || !HEX_DIGEST.test(value)) invalidOwnerRequest();
  return value;
}

function ownerTeamName(value: unknown): string {
  if (
    typeof value !== 'string' || value.trim() !== value || value.length === 0 ||
    Array.from(value).length > 128 || value.normalize('NFC') !== value ||
    /[\u0000-\u001f\u007f]/.test(value) || unsafeOwnerText(value)
  ) invalidOwnerRequest();
  return value;
}

function unsafeOwnerText(value: string): boolean {
  return UNSAFE_SECRET.test(value) || UNSAFE_PATH.test(value);
}

function invalidOwnerRequest(): never {
  throw new OwnerGatewayError('invalid_owner_request');
}

export interface OwnerApprovalGatewayOptions {
  daemonBaseURL: string;
  signer: PrincipalSigner;
  apiKey: () => string;
  fetch?: typeof fetch;
  now?: () => number;
  timeoutMs?: number;
  maxStepUpAgeSeconds?: number;
}

export class OwnerApprovalGateway {
  private readonly endpoints: Record<OwnerPublicPath, string>;
  private readonly signer: PrincipalSigner;
  private readonly apiKey: () => string;
  private readonly fetcher: typeof fetch;
  private readonly now: () => number;
  private readonly timeoutMs: number;
  private readonly maxStepUpAgeSeconds: number;

  constructor(options: OwnerApprovalGatewayOptions) {
    this.endpoints = {
      [OWNER_APPROVAL_PUBLIC_PATH]: ownerDaemonEndpoint(options.daemonBaseURL, OWNER_APPROVAL_INTERNAL_PATH),
      [OWNER_BOOTSTRAP_PUBLIC_PATH]: ownerDaemonEndpoint(options.daemonBaseURL, OWNER_BOOTSTRAP_INTERNAL_PATH),
      [OWNER_ACTIVATE_PUBLIC_PATH]: ownerDaemonEndpoint(options.daemonBaseURL, OWNER_ACTIVATE_INTERNAL_PATH),
    };
    this.signer = options.signer;
    this.apiKey = options.apiKey;
    this.fetcher = options.fetch ?? fetch;
    this.now = options.now ?? (() => Math.floor(Date.now() / 1000));
    this.timeoutMs = boundedInteger(options.timeoutMs ?? 3_000, 100, 5_000);
    this.maxStepUpAgeSeconds = boundedInteger(options.maxStepUpAgeSeconds ?? 300, 30, 300);
  }

  async call(
    path: OwnerPublicPath,
    identity: Readonly<VerifiedOAuthIdentity>,
    requestId: string,
    input: unknown,
  ): Promise<unknown> {
    if (!isOwnerPublicPath(path)) invalidOwnerRequest();
    this.requireRecentOwner(identity);
    const cleanRequestID = ownerRequestID(requestId);
    const canonical = path === OWNER_APPROVAL_PUBLIC_PATH
      ? canonicalOwnerApprovalBody(input)
      : path === OWNER_BOOTSTRAP_PUBLIC_PATH
        ? canonicalOwnerBootstrapBody(input)
        : canonicalOwnerActivateBody(input);
    if (path === OWNER_APPROVAL_PUBLIC_PATH) {
      const approval = canonical.value as CleanOwnerApproval;
      if (
        approval.action === 'team.activation.synthetic' &&
        (approval.store_id !== this.signer.storeId || approval.team_id !== this.signer.teamId)
      ) invalidOwnerRequest();
    }

    const ipcKey = this.apiKey();
    if (ipcKey === '' || Buffer.byteLength(ipcKey, 'utf8') > 512) {
      throw new OwnerGatewayError('owner_service_unavailable');
    }
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      'X-Pulse-Key': ipcKey,
      'X-Pulse-Request-ID': cleanRequestID,
    };
    if (path === OWNER_APPROVAL_PUBLIC_PATH) {
      const approval = canonical.value as CleanOwnerApproval;
      try {
        headers['X-Pulse-Owner-Step-Up'] = await this.signer.signOwnerStepUp({
          requestId: cleanRequestID,
          path: OWNER_APPROVAL_INTERNAL_PATH,
          action: approval.action,
          body: canonical.bytes,
          storeId: approval.store_id,
          teamId: approval.team_id,
          oauthIssuer: identity.issuer,
          oauthSubject: identity.subject,
          oauthClientId: identity.clientId,
          authTime: identity.authTime as number,
        });
      } catch {
        throw new OwnerGatewayError('owner_step_up_required');
      }
    }

    let response: Response;
    try {
      response = await this.fetcher(this.endpoints[path], {
        method: 'POST',
        redirect: 'error',
        signal: AbortSignal.timeout(this.timeoutMs),
        headers,
        body: canonical.text,
      });
    } catch {
      throw new OwnerGatewayError('owner_service_unavailable');
    }
    if (response.status !== 200) {
      await response.body?.cancel().catch(() => undefined);
      if (response.status === 400) throw new OwnerGatewayError('invalid_owner_request');
      if ([401, 403, 404, 409].includes(response.status)) {
        throw new OwnerGatewayError('owner_operation_denied');
      }
      throw new OwnerGatewayError('owner_service_unavailable');
    }
    const value = await readOwnerResponse(response);
    try {
      if (path === OWNER_APPROVAL_PUBLIC_PATH) {
        return validateOwnerApprovalResult(
          value, canonical.value as CleanOwnerApproval, this.now(),
        );
      }
      if (path === OWNER_BOOTSTRAP_PUBLIC_PATH) {
        return validateOwnerBootstrapResult(value, canonical.value as CleanOwnerBootstrap);
      }
      return validateOwnerActivateResult(
        value, canonical.value as CleanOwnerActivate, this.signer, this.now(),
      );
    } catch (error) {
      if (error instanceof OwnerGatewayError && error.code === 'owner_service_unavailable') throw error;
      throw new OwnerGatewayError('owner_service_unavailable');
    }
  }

  verifyRecentStepUp(identity: Readonly<VerifiedOAuthIdentity>): void {
    this.requireRecentOwner(identity);
  }

  private requireRecentOwner(identity: Readonly<VerifiedOAuthIdentity>): void {
    const now = this.now();
    if (
      !identity.capabilities.includes(OWNER_CAPABILITY) ||
      !boundedIdentity(identity.issuer) || !boundedIdentity(identity.subject) ||
      !boundedIdentity(identity.clientId) || !Number.isInteger(identity.authTime) ||
      (identity.authTime as number) <= 0 || (identity.authTime as number) > now ||
      now - (identity.authTime as number) > this.maxStepUpAgeSeconds
    ) {
      throw new OwnerGatewayError('owner_step_up_required');
    }
  }
}

function validateOwnerApprovalResult(
  value: unknown,
  request: CleanOwnerApproval,
  now: number,
): unknown {
  const result = exactOwnerResponse(value, [
    'schema', 'approval_nonce', 'action', 'store_id', 'team_id', 'target_kind',
    'target_id', 'expires_at', 'fallback',
  ]);
  if (
    result.schema !== OWNER_APPROVAL_RESULT_SCHEMA || result.fallback !== false ||
    result.action !== request.action || result.store_id !== request.store_id ||
    result.team_id !== request.team_id || result.target_kind !== request.target_kind ||
    result.target_id !== request.target_id || !isOwnerDigest(result.approval_nonce)
  ) invalidOwnerResponse();
  const expiresAt = ownerTimestamp(result.expires_at);
  const expiresUnix = Date.parse(expiresAt) / 1000;
  if (expiresUnix <= now || expiresUnix > now + 300) invalidOwnerResponse();
  return {
    schema: OWNER_APPROVAL_RESULT_SCHEMA,
    approval_nonce: result.approval_nonce,
    action: result.action,
    store_id: result.store_id,
    team_id: result.team_id,
    target_kind: result.target_kind,
    target_id: result.target_id,
    expires_at: expiresAt,
    fallback: false,
  };
}

function validateOwnerBootstrapResult(value: unknown, request: CleanOwnerBootstrap): unknown {
  if (request.operation === 'prepare') {
    const result = exactOwnerResponse(value, ['schema', 'operation', 'bootstrap_intent', 'fallback']);
    if (
      result.schema !== OWNER_BOOTSTRAP_RESULT_SCHEMA || result.operation !== 'prepared' ||
      result.fallback !== false
    ) invalidOwnerResponse();
    return {
      schema: OWNER_BOOTSTRAP_RESULT_SCHEMA,
      operation: 'prepared',
      bootstrap_intent: ownerBootstrapIntentResponse(result.bootstrap_intent),
      fallback: false,
    };
  }
  const result = exactOwnerResponse(value, [
    'schema', 'operation', 'store_id', 'team_id', 'owner_principal_id',
    'owner_membership_id', 'activation_state', 'content_boundary', 'public_enabled', 'fallback',
  ]);
  const intent = request.bootstrap_intent;
  if (
    result.schema !== OWNER_BOOTSTRAP_RESULT_SCHEMA || result.operation !== 'complete' ||
    result.store_id !== intent.store_id || result.team_id !== intent.team_id ||
    result.owner_principal_id !== intent.owner_principal_id ||
    result.owner_membership_id !== intent.owner_membership_id ||
    result.activation_state !== 'inactive' || result.content_boundary !== 'synthetic' ||
    result.public_enabled !== false || result.fallback !== false
  ) invalidOwnerResponse();
  return {
    schema: OWNER_BOOTSTRAP_RESULT_SCHEMA,
    operation: 'complete',
    store_id: result.store_id,
    team_id: result.team_id,
    owner_principal_id: result.owner_principal_id,
    owner_membership_id: result.owner_membership_id,
    activation_state: 'inactive',
    content_boundary: 'synthetic',
    public_enabled: false,
    fallback: false,
  };
}

function validateOwnerActivateResult(
  value: unknown,
  request: CleanOwnerActivate,
  signer: PrincipalSigner,
  now: number,
): unknown {
  const result = exactOwnerResponse(value, [
    'schema', 'store_id', 'team_id', 'activation_state', 'content_boundary',
    'public_enabled', 'gate_digest', 'activated_by_principal_id', 'audit_event_id',
    'activated_at', 'fallback',
  ]);
  if (
    result.schema !== OWNER_ACTIVATE_RESULT_SCHEMA || result.store_id !== signer.storeId ||
    result.team_id !== signer.teamId || result.activation_state !== 'active' ||
    result.content_boundary !== 'synthetic' || result.public_enabled !== true ||
    result.gate_digest !== request.gate_digest || !isOwnerOpaque(result.activated_by_principal_id) ||
    !isOwnerOpaque(result.audit_event_id) || result.fallback !== false
  ) invalidOwnerResponse();
  const activatedAt = ownerTimestamp(result.activated_at);
  const activatedUnix = Date.parse(activatedAt) / 1000;
  if (activatedUnix > now + 30 || activatedUnix < now - 300) invalidOwnerResponse();
  return {
    schema: OWNER_ACTIVATE_RESULT_SCHEMA,
    store_id: result.store_id,
    team_id: result.team_id,
    activation_state: 'active',
    content_boundary: 'synthetic',
    public_enabled: true,
    gate_digest: result.gate_digest,
    activated_by_principal_id: result.activated_by_principal_id,
    audit_event_id: result.audit_event_id,
    activated_at: activatedAt,
    fallback: false,
  };
}

function exactOwnerResponse(value: unknown, fields: readonly string[]): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) invalidOwnerResponse();
  const result = value as Record<string, unknown>;
  const actual = Object.keys(result).sort();
  const expected = [...fields].sort();
  if (actual.length !== expected.length || actual.some((key, index) => key !== expected[index])) {
    invalidOwnerResponse();
  }
  return result;
}

function ownerBootstrapIntentResponse(value: unknown): OwnerBootstrapIntent {
  try {
    return ownerBootstrapIntent(value);
  } catch {
    invalidOwnerResponse();
  }
}

function isOwnerOpaque(value: unknown): value is string {
  return typeof value === 'string' && SAFE_OPAQUE.test(value) && !unsafeOwnerText(value);
}

function isOwnerDigest(value: unknown): value is string {
  return typeof value === 'string' && HEX_DIGEST.test(value);
}

function ownerTimestamp(value: unknown): string {
  if (typeof value !== 'string' || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/.test(value)) {
    invalidOwnerResponse();
  }
  const parsed = Date.parse(value);
  if (!Number.isFinite(parsed)) invalidOwnerResponse();
  return new Date(parsed).toISOString();
}

function invalidOwnerResponse(): never {
  throw new OwnerGatewayError('owner_service_unavailable');
}

async function readOwnerResponse(response: Response): Promise<unknown> {
  if (response.headers.get('content-type')?.split(';', 1)[0]?.trim().toLowerCase() !== 'application/json') {
    await response.body?.cancel().catch(() => undefined);
    invalidOwnerResponse();
  }
  const declared = response.headers.get('content-length');
  if (declared !== null && (!/^\d+$/.test(declared) || Number(declared) > OWNER_MAX_RESPONSE_BYTES)) {
    await response.body?.cancel().catch(() => undefined);
    invalidOwnerResponse();
  }
  let bytes: Uint8Array;
  try {
    bytes = new Uint8Array(await response.arrayBuffer());
  } catch {
    invalidOwnerResponse();
  }
  if (bytes.byteLength === 0 || bytes.byteLength > OWNER_MAX_RESPONSE_BYTES) invalidOwnerResponse();
  try {
    return JSON.parse(Buffer.from(bytes).toString('utf8'));
  } catch {
    invalidOwnerResponse();
  }
}

function ownerDaemonEndpoint(baseURL: string, path: string): string {
  let base: URL;
  try {
    base = new URL(baseURL);
  } catch {
    throw new Error('Owner daemon URL is invalid');
  }
  const host = base.hostname.toLowerCase();
  if (
    !['http:', 'https:'].includes(base.protocol) ||
    (host !== '127.0.0.1' && host !== '::1' && host !== '[::1]') ||
    base.username !== '' || base.password !== '' || base.search !== '' || base.hash !== ''
  ) throw new Error('Owner daemon URL must be numeric loopback');
  base.pathname = `${base.pathname.replace(/\/$/, '')}${path}`;
  return base.toString();
}

function ownerRequestID(value: string): string {
  if (!/^[A-Za-z0-9][A-Za-z0-9._:-]{7,63}$/.test(value)) invalidOwnerRequest();
  return value;
}

function boundedIdentity(value: unknown): value is string {
  return typeof value === 'string' && value.length >= 1 && value.length <= 512 &&
    value.trim() === value && !/[\u0000-\u001f\u007f]/.test(value);
}

function boundedInteger(value: number, minimum: number, maximum: number): number {
  if (!Number.isInteger(value) || value < minimum || value > maximum) {
    throw new Error('Owner gateway bound is invalid');
  }
  return value;
}
