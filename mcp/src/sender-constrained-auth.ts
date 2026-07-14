import {
  closeSync,
  constants as fsConstants,
  fstatSync,
  openSync,
  readFileSync,
} from 'node:fs';
import { isAbsolute } from 'node:path';
import {
  createHash,
  createPublicKey,
  timingSafeEqual,
  verify as cryptoVerify,
  type KeyObject,
} from 'node:crypto';

import type { JWK } from 'jose';

import type { VerifiedOAuthIdentity } from './oauth-resource.js';
import type { TeamCapability } from './team-contracts.js';

export const ENROLLMENT_REGISTRY_SCHEMA = 'pulse.team.installation_enrollment_registry.v1' as const;

export const INSTALLATION_ENROLLMENT_REGISTRY_JSON_SCHEMA = deepFreeze({
  $schema: 'https://json-schema.org/draft/2020-12/schema',
  $id: 'https://zbs.gg/schemas/pulse.team.installation_enrollment_registry.v1.json',
  title: 'Pulse Team installation enrollment registry',
  type: 'object',
  additionalProperties: false,
  required: ['schema', 'issuer', 'enrollments'],
  properties: {
    schema: { const: ENROLLMENT_REGISTRY_SCHEMA },
    issuer: { type: 'string', minLength: 1, maxLength: 2048, format: 'uri' },
    enrollments: {
      type: 'array',
      maxItems: 1024,
      items: {
        type: 'object',
        additionalProperties: false,
        required: [
          'enrollment_id', 'generation', 'client_id', 'subject', 'status', 'public_jwk',
        ],
        properties: {
          enrollment_id: {
            type: 'string', minLength: 1, maxLength: 256,
            pattern: '^[A-Za-z0-9][A-Za-z0-9._|:@/-]{0,255}$',
          },
          generation: { type: 'integer', minimum: 1, maximum: Number.MAX_SAFE_INTEGER },
          client_id: { type: 'string', minLength: 1, maxLength: 512 },
          subject: { type: 'string', minLength: 1, maxLength: 512 },
          status: { enum: ['active', 'revoked'] },
          public_jwk: {
            type: 'object',
            additionalProperties: false,
            required: ['kty', 'crv', 'x', 'y'],
            properties: {
              kty: { const: 'EC' },
              crv: { const: 'P-256' },
              x: { type: 'string', pattern: '^[A-Za-z0-9_-]{43}$' },
              y: { type: 'string', pattern: '^[A-Za-z0-9_-]{43}$' },
            },
          },
        },
      },
    },
  },
} as const);

const MAX_REGISTRY_BYTES = 256 * 1024;
const MAX_ENROLLMENTS = 1024;
const MAX_AUTHORIZATION_BYTES = 16 * 1024;
const MAX_PROOF_BYTES = 16 * 1024;
const SAFE_ENROLLMENT_ID = /^[A-Za-z0-9][A-Za-z0-9._|:@/-]{0,255}$/;
const SAFE_JTI = /^[A-Za-z0-9_-]{1,128}$/;
const SAFE_METHOD = /^(GET|HEAD|POST|PUT|PATCH|DELETE)$/;
const COMPACT_PART = /^[A-Za-z0-9_-]+$/;

export type InstallationProofErrorCode =
  | 'sender_headers_invalid'
  | 'registry_unavailable'
  | 'registry_invalid'
  | 'installation_proof_invalid'
  | 'installation_proof_header_invalid'
  | 'installation_key_invalid'
  | 'installation_proof_signature_invalid'
  | 'proof_target_mismatch'
  | 'proof_method_mismatch'
  | 'proof_clock_invalid'
  | 'proof_jti_invalid'
  | 'proof_token_mismatch'
  | 'wrong_enrollment'
  | 'issuer_mismatch'
  | 'client_mismatch'
  | 'subject_mismatch'
  | 'installation_key_mismatch'
  | 'enrollment_revoked'
  | 'proof_replayed'
  | 'proof_replay_capacity';

export class InstallationProofError extends Error {
  readonly code: InstallationProofErrorCode;

  constructor(code: InstallationProofErrorCode) {
    super('Sender-constrained credential rejected');
    this.name = 'InstallationProofError';
    this.code = code;
  }
}

function fail(code: InstallationProofErrorCode): never {
  throw new InstallationProofError(code);
}

interface InstallationEnrollment {
  enrollmentID: string;
  generation: number;
  clientID: string;
  subject: string;
  status: 'active' | 'revoked';
  publicJWK: Readonly<Required<Pick<JWK, 'kty' | 'crv' | 'x' | 'y'>>>;
  keyThumbprint: string;
}

interface EnrollmentRegistryDocument {
  issuer: string;
  enrollments: ReadonlyMap<string, Readonly<InstallationEnrollment>>;
}

export interface InstallationEnrollmentRegistryEntryDocument {
  enrollment_id: string;
  generation: number;
  client_id: string;
  subject: string;
  status: 'active' | 'revoked';
  public_jwk: Readonly<Required<Pick<JWK, 'kty' | 'crv' | 'x' | 'y'>>>;
}

export interface ValidatedInstallationEnrollmentRegistryDocument {
  schema: typeof ENROLLMENT_REGISTRY_SCHEMA;
  issuer: string;
  enrollments: readonly Readonly<InstallationEnrollmentRegistryEntryDocument>[];
}

export interface InstallationEnrollmentRegistryOptions {
  file: string;
  issuer: string;
}

export class InstallationEnrollmentRegistry {
  private readonly file: string;
  private readonly issuer: string;

  constructor(options: InstallationEnrollmentRegistryOptions) {
    if (
      typeof options.file !== 'string' || !isAbsolute(options.file) ||
      options.file.length < 1 || options.file.length > 4096 ||
      /[\u0000-\u001f\u007f]/.test(options.file)
    ) {
      fail('registry_unavailable');
    }
    this.issuer = strictIssuer(options.issuer);
    this.file = options.file;
  }

  assertReady(): void {
    this.load();
  }

  current(
    enrollmentID: string,
    identity: Readonly<VerifiedOAuthIdentity>,
  ): Readonly<InstallationEnrollment> {
    const document = this.load();
    if (identity.issuer !== document.issuer) fail('issuer_mismatch');
    const enrollment = document.enrollments.get(enrollmentID);
    if (!enrollment) fail('wrong_enrollment');
    if (enrollment.clientID !== identity.clientId) fail('client_mismatch');
    if (enrollment.subject !== identity.subject) fail('subject_mismatch');
    if (enrollment.status !== 'active') fail('enrollment_revoked');
    return enrollment;
  }

  private load(): EnrollmentRegistryDocument {
    const bytes = readStrictOwnerFile(this.file);
    let value: unknown;
    try {
      value = JSON.parse(bytes.toString('utf8'));
    } catch {
      fail('registry_invalid');
    }
    return validateRegistryDocument(value, this.issuer).internal;
  }
}

export function validateInstallationEnrollmentRegistry(
  value: unknown,
  options: { issuer: string },
): Readonly<ValidatedInstallationEnrollmentRegistryDocument> {
  if (!options || typeof options !== 'object') fail('registry_invalid');
  return validateRegistryDocument(value, strictIssuer(options.issuer)).publicDocument;
}

function validateRegistryDocument(value: unknown, issuer: string): {
  internal: EnrollmentRegistryDocument;
  publicDocument: Readonly<ValidatedInstallationEnrollmentRegistryDocument>;
} {
  if (!isExactRecord(value, ['schema', 'issuer', 'enrollments'])) fail('registry_invalid');
  if (value.schema !== ENROLLMENT_REGISTRY_SCHEMA || value.issuer !== issuer) fail('registry_invalid');
  if (!Array.isArray(value.enrollments) || value.enrollments.length > MAX_ENROLLMENTS) {
    fail('registry_invalid');
  }
  const enrollments = new Map<string, Readonly<InstallationEnrollment>>();
  const publicEntries: Readonly<InstallationEnrollmentRegistryEntryDocument>[] = [];
  const activeIdentities = new Set<string>();
  const identityGenerations = new Set<string>();
  for (const raw of value.enrollments) {
    const enrollment = parseEnrollment(raw);
    if (enrollments.has(enrollment.enrollmentID)) fail('registry_invalid');
    const identityKey = `${enrollment.clientID}\u0000${enrollment.subject}`;
    const generationKey = `${identityKey}\u0000${enrollment.generation}`;
    if (identityGenerations.has(generationKey)) fail('registry_invalid');
    identityGenerations.add(generationKey);
    if (enrollment.status === 'active') {
      if (activeIdentities.has(identityKey)) fail('registry_invalid');
      activeIdentities.add(identityKey);
    }
    enrollments.set(enrollment.enrollmentID, enrollment);
    publicEntries.push(Object.freeze({
      enrollment_id: enrollment.enrollmentID,
      generation: enrollment.generation,
      client_id: enrollment.clientID,
      subject: enrollment.subject,
      status: enrollment.status,
      public_jwk: enrollment.publicJWK,
    }));
  }
  return {
    internal: { issuer, enrollments },
    publicDocument: Object.freeze({
      schema: ENROLLMENT_REGISTRY_SCHEMA,
      issuer,
      enrollments: Object.freeze(publicEntries),
    }),
  };
}

export interface InstallationProofVerifierOptions {
  registry: InstallationEnrollmentRegistry;
  now?: () => number;
  clockSkewSeconds?: number;
  maxReplayEntries?: number;
}

export interface InstallationProofInput {
  authorization: string;
  dpop: string;
  enrollmentID: string;
  method: string;
  targetURL: string;
  identity: Readonly<VerifiedOAuthIdentity>;
}

export interface VerifiedInstallation {
  enrollmentID: string;
  generation: number;
  keyThumbprint: string;
}

export class InstallationProofVerifier {
  private readonly registry: InstallationEnrollmentRegistry;
  private readonly now: () => number;
  private readonly clockSkewSeconds: number;
  private readonly maxReplayEntries: number;
  private readonly consumedJtis = new Map<string, number>();

  constructor(options: InstallationProofVerifierOptions) {
    this.registry = options.registry;
    this.now = options.now ?? (() => Math.floor(Date.now() / 1000));
    this.clockSkewSeconds = options.clockSkewSeconds ?? 30;
    this.maxReplayEntries = options.maxReplayEntries ?? 50_000;
    if (
      !Number.isInteger(this.clockSkewSeconds) || this.clockSkewSeconds < 0 ||
      this.clockSkewSeconds > 30 || !Number.isInteger(this.maxReplayEntries) ||
      this.maxReplayEntries < 1 || this.maxReplayEntries > 1_000_000
    ) {
      fail('registry_invalid');
    }
  }

  verify(input: InstallationProofInput): VerifiedInstallation {
    const accessToken = dpopAccessToken(input.authorization);
    const parsed = parseAndVerifyProof(input.dpop);
    const now = this.now();
    const method = strictMethod(input.method);
    const target = proofTarget(input.targetURL);
    const payload = parsed.payload;
    if (payload.htu !== target) fail('proof_target_mismatch');
    if (payload.htm !== method) fail('proof_method_mismatch');
    if (!Number.isInteger(payload.iat) || Math.abs((payload.iat as number) - now) > this.clockSkewSeconds) {
      fail('proof_clock_invalid');
    }
    if (typeof payload.jti !== 'string' || !SAFE_JTI.test(payload.jti)) fail('proof_jti_invalid');
    const expectedTokenHash = createHash('sha256').update(accessToken).digest('base64url');
    if (!constantTimeTextEqual(payload.ath, expectedTokenHash)) fail('proof_token_mismatch');
    if (
      typeof input.enrollmentID !== 'string' || !SAFE_ENROLLMENT_ID.test(input.enrollmentID) ||
      payload.enrollment_id !== input.enrollmentID
    ) {
      fail('wrong_enrollment');
    }
    if (payload.client_id !== input.identity.clientId) fail('client_mismatch');
    if (payload.sub !== input.identity.subject) fail('subject_mismatch');
    const enrollment = this.registry.current(input.enrollmentID, input.identity);
    if (payload.enrollment_generation !== enrollment.generation) fail('wrong_enrollment');
    if (!constantTimeTextEqual(parsed.keyThumbprint, enrollment.keyThumbprint)) {
      fail('installation_key_mismatch');
    }
    if (!constantTimeTextEqual(input.identity.confirmationKeyThumbprint, enrollment.keyThumbprint)) {
      fail('installation_key_mismatch');
    }
    this.consumeJTI(payload.jti, payload.iat as number, now);
    return Object.freeze({
      enrollmentID: enrollment.enrollmentID,
      generation: enrollment.generation,
      keyThumbprint: enrollment.keyThumbprint,
    });
  }

  private consumeJTI(jti: string, issuedAt: number, now: number): void {
    if (this.consumedJtis.has(jti)) fail('proof_replayed');
    if (this.consumedJtis.size >= this.maxReplayEntries) {
      for (const [candidate, expiresAt] of this.consumedJtis) {
        if (expiresAt < now) this.consumedJtis.delete(candidate);
      }
      if (this.consumedJtis.size >= this.maxReplayEntries) fail('proof_replay_capacity');
    }
    this.consumedJtis.set(jti, issuedAt + this.clockSkewSeconds);
  }
}

interface HeaderRequest {
  rawHeaders: readonly string[];
  headers: Readonly<Record<string, string | string[] | undefined>>;
}

export interface SenderConstrainedHeaders {
  authorization: string;
  dpop: string;
  enrollmentID: string;
}

export function requireSenderConstrainedHeaders(request: HeaderRequest): SenderConstrainedHeaders {
  if (request.rawHeaders.length % 2 !== 0) fail('sender_headers_invalid');
  const required = ['authorization', 'dpop', 'x-pulse-enrollment'] as const;
  for (const name of required) {
    let count = 0;
    for (let index = 0; index < request.rawHeaders.length; index += 2) {
      if (request.rawHeaders[index]?.toLowerCase() === name) count++;
    }
    if (count !== 1 || typeof request.headers[name] !== 'string') fail('sender_headers_invalid');
  }
  return {
    authorization: request.headers.authorization as string,
    dpop: request.headers.dpop as string,
    enrollmentID: request.headers['x-pulse-enrollment'] as string,
  };
}

interface OAuthVerifier {
  verifyAuthorization(
    authorization: string | string[] | undefined,
    requiredCapabilities: readonly TeamCapability[],
  ): Promise<VerifiedOAuthIdentity>;
}

interface ProofVerifier {
  verify(input: InstallationProofInput): VerifiedInstallation;
}

export interface SenderConstrainedOAuthVerifierOptions {
  oauthVerifier: OAuthVerifier;
  proofVerifier: ProofVerifier;
}

export interface SenderConstrainedAuthorizationInput extends SenderConstrainedHeaders {
  method: string;
  targetURL: string;
}

export class SenderConstrainedOAuthVerifier {
  private readonly oauthVerifier: OAuthVerifier;
  private readonly proofVerifier: ProofVerifier;

  constructor(options: SenderConstrainedOAuthVerifierOptions) {
    this.oauthVerifier = options.oauthVerifier;
    this.proofVerifier = options.proofVerifier;
  }

  async verifyAuthorization(
    input: SenderConstrainedAuthorizationInput,
    requiredCapabilities: readonly TeamCapability[],
  ): Promise<VerifiedOAuthIdentity> {
    const accessToken = dpopAccessToken(input.authorization);
    const identity = await this.oauthVerifier.verifyAuthorization(
      `Bearer ${accessToken}`,
      requiredCapabilities,
    );
    this.proofVerifier.verify({ ...input, identity });
    return identity;
  }
}

function readStrictOwnerFile(path: string): Buffer {
  let fd: number | undefined;
  try {
    fd = openSync(path, fsConstants.O_RDONLY | fsConstants.O_NOFOLLOW);
    const before = fstatSync(fd);
    const uid = typeof process.getuid === 'function' ? process.getuid() : before.uid;
    if (
      !before.isFile() || before.uid !== uid || (before.mode & 0o077) !== 0 ||
      before.size < 1 || before.size > MAX_REGISTRY_BYTES
    ) {
      fail('registry_unavailable');
    }
    const bytes = readFileSync(fd);
    const after = fstatSync(fd);
    if (
      bytes.length !== before.size || bytes.length > MAX_REGISTRY_BYTES ||
      after.size !== before.size || after.ino !== before.ino ||
      after.mtimeMs !== before.mtimeMs || after.ctimeMs !== before.ctimeMs
    ) {
      fail('registry_unavailable');
    }
    return bytes;
  } catch (error) {
    if (error instanceof InstallationProofError) throw error;
    return fail('registry_unavailable');
  } finally {
    if (fd !== undefined) closeSync(fd);
  }
}

function parseEnrollment(value: unknown): Readonly<InstallationEnrollment> {
  if (!isExactRecord(value, [
    'enrollment_id', 'generation', 'client_id', 'subject', 'status', 'public_jwk',
  ])) {
    fail('registry_invalid');
  }
  if (
    typeof value.enrollment_id !== 'string' || !SAFE_ENROLLMENT_ID.test(value.enrollment_id) ||
    !Number.isSafeInteger(value.generation) || (value.generation as number) < 1 ||
    !isBoundedIdentity(value.client_id) || !isBoundedIdentity(value.subject) ||
    (value.status !== 'active' && value.status !== 'revoked')
  ) {
    fail('registry_invalid');
  }
  const publicJWK = publicInstallationJWK(value.public_jwk, 'registry_invalid');
  validatePublicKey(publicJWK, 'registry_invalid');
  return Object.freeze({
    enrollmentID: value.enrollment_id,
    generation: value.generation as number,
    clientID: value.client_id,
    subject: value.subject,
    status: value.status,
    publicJWK: Object.freeze(publicJWK),
    keyThumbprint: installationKeyThumbprint(publicJWK),
  });
}

function parseAndVerifyProof(value: string): {
  payload: Record<string, unknown>;
  keyThumbprint: string;
} {
  if (typeof value !== 'string' || value.length < 1 || Buffer.byteLength(value, 'utf8') > MAX_PROOF_BYTES) {
    fail('installation_proof_invalid');
  }
  const parts = value.split('.');
  if (parts.length !== 3 || parts.some((part) => !COMPACT_PART.test(part))) {
    fail('installation_proof_invalid');
  }
  const [encodedHeader, encodedPayload, encodedSignature] = parts as [string, string, string];
  if (encodedHeader.length > 4096 || encodedPayload.length > 8192 || encodedSignature.length > 512) {
    fail('installation_proof_invalid');
  }
  const header = decodeJSONPart(encodedHeader, 'installation_proof_invalid');
  const payload = decodeJSONPart(encodedPayload, 'installation_proof_invalid');
  if (!isExactRecord(header, ['typ', 'alg', 'jwk']) || header.typ !== 'dpop+jwt' || header.alg !== 'ES256') {
    fail('installation_proof_header_invalid');
  }
  if (!isExactRecord(payload, [
    'htu', 'htm', 'iat', 'jti', 'ath', 'enrollment_id', 'enrollment_generation', 'client_id', 'sub',
  ])) {
    fail('installation_proof_invalid');
  }
  const publicJWK = publicInstallationJWK(header.jwk, 'installation_key_invalid');
  const publicKey = validatePublicKey(publicJWK, 'installation_key_invalid');
  const signature = decodeBase64URL(encodedSignature, 'installation_proof_invalid');
  if (signature.length !== 64) fail('installation_proof_invalid');
  let verified = false;
  try {
    verified = cryptoVerify(
      'sha256',
      Buffer.from(`${encodedHeader}.${encodedPayload}`),
      { key: publicKey, dsaEncoding: 'ieee-p1363' },
      signature,
    );
  } catch {
    fail('installation_proof_signature_invalid');
  }
  if (!verified) fail('installation_proof_signature_invalid');
  return { payload, keyThumbprint: installationKeyThumbprint(publicJWK) };
}

function decodeJSONPart(value: string, code: InstallationProofErrorCode): Record<string, unknown> {
  const bytes = decodeBase64URL(value, code);
  try {
    const parsed: unknown = JSON.parse(bytes.toString('utf8'));
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) fail(code);
    return parsed as Record<string, unknown>;
  } catch (error) {
    if (error instanceof InstallationProofError) throw error;
    fail(code);
  }
}

function decodeBase64URL(value: string, code: InstallationProofErrorCode): Buffer {
  if (!COMPACT_PART.test(value)) fail(code);
  let decoded: Buffer;
  try {
    decoded = Buffer.from(value, 'base64url');
  } catch {
    fail(code);
  }
  if (decoded.length < 1 || decoded.toString('base64url') !== value) fail(code);
  return decoded;
}

function publicInstallationJWK(
  value: unknown,
  code: InstallationProofErrorCode,
): Required<Pick<JWK, 'kty' | 'crv' | 'x' | 'y'>> {
  if (
    !isExactRecord(value, ['kty', 'crv', 'x', 'y']) ||
    value.kty !== 'EC' || value.crv !== 'P-256' ||
    !isP256Coordinate(value.x) || !isP256Coordinate(value.y)
  ) {
    fail(code);
  }
  return { kty: 'EC', crv: 'P-256', x: value.x, y: value.y };
}

function isP256Coordinate(value: unknown): value is string {
  if (typeof value !== 'string' || !/^[A-Za-z0-9_-]{43}$/.test(value)) return false;
  try {
    const bytes = Buffer.from(value, 'base64url');
    return bytes.length === 32 && bytes.toString('base64url') === value;
  } catch {
    return false;
  }
}

function validatePublicKey(
  jwk: Required<Pick<JWK, 'kty' | 'crv' | 'x' | 'y'>>,
  code: InstallationProofErrorCode,
): KeyObject {
  try {
    const key = createPublicKey({ key: jwk, format: 'jwk' });
    if (key.asymmetricKeyType !== 'ec' || key.asymmetricKeyDetails?.namedCurve !== 'prime256v1') fail(code);
    return key;
  } catch (error) {
    if (error instanceof InstallationProofError) throw error;
    fail(code);
  }
}

function installationKeyThumbprint(jwk: Required<Pick<JWK, 'kty' | 'crv' | 'x' | 'y'>>): string {
  return createHash('sha256').update(JSON.stringify({
    crv: jwk.crv,
    kty: jwk.kty,
    x: jwk.x,
    y: jwk.y,
  })).digest('base64url');
}

function dpopAccessToken(value: string): string {
  if (typeof value !== 'string' || Buffer.byteLength(value, 'utf8') > MAX_AUTHORIZATION_BYTES) {
    fail('sender_headers_invalid');
  }
  const match = /^DPoP ([A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)$/.exec(value);
  if (!match) fail('sender_headers_invalid');
  return match[1];
}

function strictIssuer(value: string): string {
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    fail('registry_invalid');
  }
  if (
    typeof value !== 'string' || value.length < 1 || value !== value.trim() ||
    url.protocol !== 'https:' || url.username !== '' || url.password !== '' ||
    url.search !== '' || url.hash !== ''
  ) {
    fail('registry_invalid');
  }
  return value;
}

function proofTarget(value: string): string {
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    fail('proof_target_mismatch');
  }
  if (
    typeof value !== 'string' || value.length < 1 || value !== value.trim() ||
    url.protocol !== 'https:' || url.username !== '' || url.password !== ''
  ) {
    fail('proof_target_mismatch');
  }
  url.search = '';
  url.hash = '';
  return url.toString();
}

function strictMethod(value: string): string {
  if (typeof value !== 'string') fail('proof_method_mismatch');
  const method = value.toUpperCase();
  if (!SAFE_METHOD.test(method)) fail('proof_method_mismatch');
  return method;
}

function isBoundedIdentity(value: unknown): value is string {
  return typeof value === 'string' && Buffer.byteLength(value, 'utf8') >= 1 &&
    Buffer.byteLength(value, 'utf8') <= 512 && value === value.trim() &&
    !/[\u0000-\u001f\u007f]/.test(value);
}

function isExactRecord(value: unknown, keys: readonly string[]): value is Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false;
  const expected = [...keys].sort();
  const actual = Object.keys(value as Record<string, unknown>).sort();
  return actual.length === expected.length && actual.every((key, index) => key === expected[index]);
}

function constantTimeTextEqual(left: unknown, right: string): boolean {
  if (typeof left !== 'string') return false;
  const leftDigest = createHash('sha256').update(left).digest();
  const rightDigest = createHash('sha256').update(right).digest();
  return left.length === right.length && timingSafeEqual(leftDigest, rightDigest);
}

function deepFreeze<T>(value: T): Readonly<T> {
  if (value && typeof value === 'object') {
    for (const child of Object.values(value as Record<string, unknown>)) deepFreeze(child);
    Object.freeze(value);
  }
  return value;
}
