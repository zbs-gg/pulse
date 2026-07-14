import { canonicalizeEnvelopeJSON } from './canonical-envelope.js';

export const AIRLOCK_ENVELOPE_SCHEMA = 'pulse.team.airlock_envelope.v1' as const;
export const AIRLOCK_ENVELOPE_ACTION = 'team.commons.publish' as const;

const AIRLOCK_MAX_ENVELOPE_BYTES = 64 * 1024;
const AIRLOCK_TOP_LEVEL_FIELDS = [
  'schema',
  'action',
  'deployment_id',
  'store_id',
  'team_id',
  'target_kind',
  'target_id',
  'publication_key',
  'policy_epoch',
  'writer_principal_id',
  'client_key',
  'writer_id',
  'source_timestamp',
  'content',
  'metadata',
] as const;
const AIRLOCK_METADATA_FIELDS = new Set(['kind', 'tags']);
const AIRLOCK_MEMORY_KINDS = new Set<AirlockMemoryKind>([
  'fact',
  'decision',
  'preference',
  'project_state',
  'open_loop',
  'correction',
  'relationship_note',
  'do_not_repeat',
  'system_event',
  'state_signal',
]);

const SAFE_OPAQUE = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$/;
const SAFE_TAG = /^[\p{L}\p{N}][\p{L}\p{N}._:-]{0,63}$/u;
const CLIENT_KEY = /^[0-9a-f]{64}$/;
const FORMAT_CHARACTER = /\p{Cf}/u;
const HTML_ELEMENT = /<\s*\/?\s*[A-Za-z][^>]*>/u;
const ACTIVE_CONTENT = /(?:javascript|vbscript|data)\s*:/iu;
const UNSAFE_SECRET = /(?:token=|api_key|apikey|password|secret|private_key|begin private key|authorization\s*:\s*bearer|api[_ -]?key|private[_ -]?key|begin\s+(?:rsa\s+)?private\s+key|\bsk-[A-Za-z0-9_-]{12,}\b|\bghp_[A-Za-z0-9_]{12,}\b|\bxoxb-[A-Za-z0-9-]{12,}\b|\bAKIA[0-9A-Z]{12,}\b)/iu;
const UNSAFE_PATH = /(?:\/(?:users|home|etc|var|private|volumes|tmp|opt|workspace)\/|file:\/\/|(?:^|\s)~\/|(?:^|\s)[a-z]:\\|\\\\[^\\\s]+\\)/iu;
const PRIVATE_REFERENCE = /\b(?:memory|candidate|session|thread|desk|personal|tray|vault)_[A-Za-z0-9][A-Za-z0-9._:-]{7,}\b/iu;
const WORD = /[\p{L}\p{N}_:-]+/gu;
const LATIN = /\p{Script=Latin}/u;
const CYRILLIC = /\p{Script=Cyrillic}/u;
const GREEK = /\p{Script=Greek}/u;

export type AirlockMemoryKind =
  | 'fact'
  | 'decision'
  | 'preference'
  | 'project_state'
  | 'open_loop'
  | 'correction'
  | 'relationship_note'
  | 'do_not_repeat'
  | 'system_event'
  | 'state_signal';

export interface AirlockMetadata {
  kind: AirlockMemoryKind;
  tags: string[];
}

export interface AirlockEnvelope {
  schema: typeof AIRLOCK_ENVELOPE_SCHEMA;
  action: typeof AIRLOCK_ENVELOPE_ACTION;
  deployment_id: string;
  store_id: string;
  team_id: string;
  target_kind: 'commons';
  target_id: string;
  publication_key: string;
  policy_epoch: number;
  writer_principal_id: string;
  client_key: string;
  writer_id: string;
  source_timestamp: string;
  content: string;
  metadata: AirlockMetadata;
}

export interface CanonicalAirlockEnvelope {
  value: AirlockEnvelope;
  bytes: string;
  envelopeDigest: string;
}

export class AirlockContractError extends Error {
  readonly code = 'invalid_airlock_contract' as const;

  constructor(message: string) {
    super(message);
    this.name = 'AirlockContractError';
  }
}

/**
 * Parses raw JSON, validates the complete disclosure boundary, and returns the
 * one byte representation that preview, approval, audit, and persistence must
 * share. Deliberately accepting raw JSON (rather than an already parsed value)
 * keeps duplicate-key rejection inside the contract.
 */
export function canonicalAirlockEnvelope(raw: string): CanonicalAirlockEnvelope {
  if (typeof raw !== 'string' || Buffer.byteLength(raw, 'utf8') > AIRLOCK_MAX_ENVELOPE_BYTES) {
    failAirlock('airlock_envelope_too_large');
  }

  const canonical = canonicalizeEnvelopeJSON(raw, AIRLOCK_TOP_LEVEL_FIELDS);
  if (Buffer.byteLength(canonical.bytes, 'utf8') > AIRLOCK_MAX_ENVELOPE_BYTES) {
    failAirlock('airlock_envelope_too_large');
  }

  const input = exactRecord(
    JSON.parse(canonical.bytes),
    'envelope',
    new Set(AIRLOCK_TOP_LEVEL_FIELDS),
  );
  requireFields(input, AIRLOCK_TOP_LEVEL_FIELDS, 'envelope');

  if (input.schema !== AIRLOCK_ENVELOPE_SCHEMA) failAirlock('airlock_schema_invalid');
  if (input.action !== AIRLOCK_ENVELOPE_ACTION) failAirlock('airlock_action_invalid');
  if (input.target_kind !== 'commons') failAirlock('airlock_target_invalid');

  const deploymentID = prefixedOpaque(input.deployment_id, 'deployment_id', 'deployment_');
  const storeID = prefixedOpaque(input.store_id, 'store_id', 'store_');
  const teamID = prefixedOpaque(input.team_id, 'team_id', 'team_');
  const targetID = prefixedOpaque(input.target_id, 'target_id', 'team_');
  if (targetID !== teamID) failAirlock('airlock_target_mismatch');
  const publicationKey = safeOpaque(input.publication_key, 'publication_key');
  if (PRIVATE_REFERENCE.test(publicationKey)) failAirlock('airlock_publication_key_private_reference');

  if (
    typeof input.policy_epoch !== 'number' ||
    !Number.isSafeInteger(input.policy_epoch) ||
    input.policy_epoch < 1
  ) failAirlock('airlock_policy_epoch_invalid');

  const writerPrincipalID = prefixedOpaque(
    input.writer_principal_id,
    'writer_principal_id',
    'principal_',
  );
  if (typeof input.client_key !== 'string' || !CLIENT_KEY.test(input.client_key)) {
    failAirlock('airlock_client_key_invalid');
  }
  const writerID = safeOpaque(input.writer_id, 'writer_id');
  const sourceTimestamp = safeTimestamp(input.source_timestamp);
  const content = safeDisclosureText(input.content, 'content', 1200);
  const metadata = cleanMetadata(input.metadata);

  return {
    value: {
      schema: AIRLOCK_ENVELOPE_SCHEMA,
      action: AIRLOCK_ENVELOPE_ACTION,
      deployment_id: deploymentID,
      store_id: storeID,
      team_id: teamID,
      target_kind: 'commons',
      target_id: targetID,
      publication_key: publicationKey,
      policy_epoch: input.policy_epoch,
      writer_principal_id: writerPrincipalID,
      client_key: input.client_key,
      writer_id: writerID,
      source_timestamp: sourceTimestamp,
      content,
      metadata,
    },
    bytes: canonical.bytes,
    envelopeDigest: canonical.digest.slice('sha256:'.length),
  };
}

function safeTimestamp(value: unknown): string {
  if (typeof value !== 'string' ||
      !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/u.test(value) ||
      Number.isNaN(Date.parse(value)) || new Date(value).toISOString() !== value) {
    failAirlock('airlock_source_timestamp_invalid');
  }
  return value;
}

function cleanMetadata(value: unknown): AirlockMetadata {
  const metadata = exactRecord(value, 'metadata', AIRLOCK_METADATA_FIELDS);
  requireFields(metadata, ['kind', 'tags'], 'metadata');
  if (typeof metadata.kind !== 'string' || !AIRLOCK_MEMORY_KINDS.has(metadata.kind as AirlockMemoryKind)) {
    failAirlock('airlock_metadata_kind_invalid');
  }
  if (!Array.isArray(metadata.tags) || metadata.tags.length > 20) {
    failAirlock('airlock_metadata_tags_invalid');
  }

  const tags = metadata.tags.map((tag, index) => {
    const clean = safeDisclosureText(tag, `metadata.tags.${index}`, 64);
    if (!SAFE_TAG.test(clean)) failAirlock('airlock_metadata_tag_invalid');
    return clean;
  });
  if (new Set(tags).size !== tags.length) failAirlock('airlock_metadata_tags_duplicate');
  for (let index = 1; index < tags.length; index += 1) {
    if (tags[index - 1]! >= tags[index]!) failAirlock('airlock_metadata_tags_not_canonical');
  }
  return { kind: metadata.kind as AirlockMemoryKind, tags };
}

function exactRecord(
  value: unknown,
  field: string,
  allowed: ReadonlySet<string>,
): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    failAirlock(`airlock_field_invalid:${field}`);
  }
  const record = value as Record<string, unknown>;
  for (const key of Object.keys(record)) {
    if (!allowed.has(key)) failAirlock(`airlock_unknown_field:${field}.${key}`);
  }
  return record;
}

function requireFields(
  value: Record<string, unknown>,
  fields: readonly string[],
  parent: string,
): void {
  for (const field of fields) {
    if (!Object.hasOwn(value, field) || value[field] === undefined || value[field] === null) {
      failAirlock(`airlock_required_field:${parent}.${field}`);
    }
  }
}

function prefixedOpaque(value: unknown, field: string, prefix: string): string {
  const clean = safeOpaque(value, field);
  if (!clean.startsWith(prefix) || clean.length === prefix.length) {
    failAirlock(`airlock_field_invalid:${field}`);
  }
  return clean;
}

function safeOpaque(value: unknown, field: string): string {
  if (typeof value !== 'string' || !SAFE_OPAQUE.test(value)) {
    failAirlock(`airlock_field_invalid:${field}`);
  }
  return value;
}

function safeDisclosureText(value: unknown, field: string, maximum: number): string {
  if (
    typeof value !== 'string' ||
    value.length === 0 ||
    value.trim() !== value ||
    /[\r\n\t]/u.test(value) ||
    Array.from(value).length > maximum
  ) failAirlock(`airlock_field_invalid:${field}`);

  if (FORMAT_CHARACTER.test(value) || value.normalize('NFKC') !== value) {
    failAirlock(`airlock_ambiguous_unicode:${field}`);
  }
  const lower = value.toLowerCase();
  if ((lower.match(/user:/g) ?? []).length >= 3 ||
      (lower.match(/assistant:/g) ?? []).length >= 3 ||
      (value.match(/\n/g) ?? []).length > 30) {
    failAirlock(`airlock_unsafe_text:${field}:transcript`);
  }
  for (const word of value.match(WORD) ?? []) {
    const scripts = Number(LATIN.test(word)) + Number(CYRILLIC.test(word)) + Number(GREEK.test(word));
    if (scripts > 1) failAirlock(`airlock_ambiguous_unicode:${field}`);
  }
  if (HTML_ELEMENT.test(value) || ACTIVE_CONTENT.test(value)) {
    failAirlock(`airlock_unsafe_text:${field}:markup`);
  }
  if (UNSAFE_SECRET.test(value)) failAirlock(`airlock_unsafe_text:${field}:secret`);
  if (UNSAFE_PATH.test(value)) failAirlock(`airlock_unsafe_text:${field}:path`);
  if (PRIVATE_REFERENCE.test(value)) failAirlock(`airlock_unsafe_text:${field}:private_reference`);
  return value;
}

function failAirlock(message: string): never {
  throw new AirlockContractError(message);
}
