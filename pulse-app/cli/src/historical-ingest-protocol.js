const SCHEMA_VERSION = 'https://zbs.gg/schemas/pulse/historical-ingest/v1';
const HEX_DIGEST = /^[a-f0-9]{64}$/;
const JOB_ID = /^job_[a-f0-9]{16,64}$/;
const CANDIDATE_ID = /^candidate_[a-f0-9]{16,64}$/;
const SOURCE_ALIAS = /^source_[a-f0-9]{16,64}$/;
const PROJECT_ID = /^project_[A-Za-z0-9._:-]{1,247}$/;
const OPAQUE_LOCATOR = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$/;
const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;
const ABSOLUTE_PATH = /(?:^|[\s"'])\/(?:Users|home|var|private|Volumes|workspace)\/|[A-Za-z]:\\/;
const SENSITIVE = /(?:api[_-]?key|authorization\s*:|begin private key|ghp_|xox[baprs]-)/i;

const MATERIAL_KINDS = new Set(['event', 'decision', 'assertion', 'person', 'project', 'relation', 'state', 'continuity']);
const EPISTEMIC = new Set(['explicit', 'hypothesis', 'conflict']);
const DERIVATION = new Set(['direct', 'inferred']);
const SCOPES = new Set(['project', 'global', 'unassigned']);
const PAYLOAD_FIELDS = new Set([
  'title', 'summary', 'subject_id', 'predicate', 'object_value', 'object_id',
  'entity_type', 'name', 'state_kind', 'intensity', 'continuity_status',
]);

export class HistoricalIngestProtocolError extends Error {
  constructor(code) {
    super(code);
    this.name = 'HistoricalIngestProtocolError';
    this.code = code;
  }
}

function fail(code) {
  throw new HistoricalIngestProtocolError(code);
}

function object(value, fields, required, code) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) fail(code);
  const keys = Object.keys(value);
  if (keys.some((key) => !fields.has(key))) fail(`${code}_unknown_field`);
  if (required.some((key) => !Object.hasOwn(value, key))) fail(`${code}_missing_field`);
  return value;
}

function boundedString(value, minimum, maximum, code) {
  if (typeof value !== 'string' || value.length < minimum || value.length > maximum) fail(code);
}

function validTime(value) {
  object(value, new Set(['from', 'to']), ['from'], 'valid_time');
  if (!RFC3339.test(value.from) || Number.isNaN(Date.parse(value.from))) fail('valid_time_from');
  if (value.to !== undefined) {
    if (!RFC3339.test(value.to) || Number.isNaN(Date.parse(value.to)) || Date.parse(value.to) <= Date.parse(value.from)) {
      fail('valid_time_to');
    }
  }
}

function scope(value) {
  object(value, new Set(['kind', 'project_id']), ['kind'], 'scope');
  if (!SCOPES.has(value.kind)) fail('scope_kind');
  if (value.kind === 'project') {
    if (!PROJECT_ID.test(value.project_id ?? '')) fail('scope_project_id');
  } else if (value.project_id !== undefined) {
    fail('scope_project_id');
  }
}

function sourceRef(value) {
  object(value, new Set(['alias', 'prefix_digest', 'record_locator']), ['alias', 'prefix_digest', 'record_locator'], 'source_ref');
  if (!SOURCE_ALIAS.test(value.alias) || !HEX_DIGEST.test(value.prefix_digest) || !OPAQUE_LOCATOR.test(value.record_locator)) {
    fail('source_ref_identity');
  }
}

function payload(value, kind) {
  object(value, PAYLOAD_FIELDS, [], 'payload');
  for (const [key, item] of Object.entries(value)) {
    if (key === 'intensity') {
      if (typeof item !== 'number' || !Number.isFinite(item) || item < 0 || item > 1) fail('payload_intensity');
      continue;
    }
    boundedString(item, 1, ['summary', 'object_value'].includes(key) ? 8192 : key === 'name' || key === 'title' ? 512 : 256, `payload_${key}`);
  }
  if (kind === 'event' && (!value.title || !value.summary)) fail('payload_event');
  if (kind === 'decision' && !value.summary) fail('payload_decision');
  if (kind === 'assertion' && (!value.subject_id || !value.predicate || !value.object_value)) fail('payload_assertion');
  if (kind === 'person' && (value.entity_type !== 'person' || !value.name)) fail('payload_person');
  if (kind === 'project' && (value.entity_type !== 'project' || !value.name)) fail('payload_project');
  if (kind === 'relation' && (!value.subject_id || !value.predicate || !value.object_id)) fail('payload_relation');
  if (kind === 'state' && (!value.state_kind || !value.summary)) fail('payload_state');
  if (kind === 'continuity' && (!value.summary || !new Set(['open', 'closed', 'historical']).has(value.continuity_status))) {
    fail('payload_continuity');
  }
}

function materialItem(value) {
  object(value, new Set([
    'candidate_id', 'kind', 'confidence', 'privacy', 'epistemic_status', 'derivation',
    'valid_time', 'scope', 'source_refs', 'payload',
  ]), [
    'candidate_id', 'kind', 'confidence', 'privacy', 'epistemic_status', 'derivation',
    'valid_time', 'scope', 'source_refs', 'payload',
  ], 'item');
  if (!CANDIDATE_ID.test(value.candidate_id) || !MATERIAL_KINDS.has(value.kind)) fail('item_identity');
  if (typeof value.confidence !== 'number' || !Number.isFinite(value.confidence) || value.confidence < 0 || value.confidence > 1) {
    fail('item_confidence');
  }
  if (value.privacy !== 'private' || !EPISTEMIC.has(value.epistemic_status) || !DERIVATION.has(value.derivation)) fail('item_classification');
  if (value.derivation === 'inferred' && value.epistemic_status === 'explicit') fail('inferred_explicit');
  validTime(value.valid_time);
  scope(value.scope);
  if (!Array.isArray(value.source_refs) || value.source_refs.length < 1 || value.source_refs.length > 512) fail('source_refs');
  value.source_refs.forEach(sourceRef);
  payload(value.payload, value.kind);
}

export function assertHistoricalIngestManifest(value, { expectedJobID, expectedSnapshotDigest } = {}) {
  object(value, new Set(['schema_version', 'job_id', 'revision', 'source_snapshot_digest', 'items']), [
    'schema_version', 'job_id', 'revision', 'source_snapshot_digest', 'items',
  ], 'manifest');
  if (value.schema_version !== SCHEMA_VERSION || !JOB_ID.test(value.job_id) ||
      !Number.isSafeInteger(value.revision) || value.revision < 1 || !HEX_DIGEST.test(value.source_snapshot_digest)) {
    fail('manifest_identity');
  }
  if (expectedJobID !== undefined && value.job_id !== expectedJobID) fail('manifest_job_mismatch');
  if (expectedSnapshotDigest !== undefined && value.source_snapshot_digest !== expectedSnapshotDigest) fail('manifest_snapshot_mismatch');
  if (!Array.isArray(value.items) || value.items.length > 10000) fail('manifest_items');
  const seen = new Set();
  for (const item of value.items) {
    materialItem(item);
    if (seen.has(item.candidate_id)) fail('duplicate_candidate_id');
    seen.add(item.candidate_id);
  }
  const encoded = JSON.stringify(value);
  if (Buffer.byteLength(encoded, 'utf8') > 16 * 1024 * 1024) fail('manifest_too_large');
  if (ABSOLUTE_PATH.test(encoded)) fail('manifest_local_path');
  if (SENSITIVE.test(encoded)) fail('manifest_sensitive');
  return value;
}

function nullable(schema) {
  return { anyOf: [schema, { type: 'null' }] };
}

function addExplicitPrimitiveTypes(schema) {
  if (!schema || typeof schema !== 'object') return;
  if (schema.type === undefined && Object.hasOwn(schema, 'const')) schema.type = typeof schema.const;
  if (schema.type === undefined && Array.isArray(schema.enum) && schema.enum.length > 0 && schema.enum.every((value) => typeof value === 'string')) {
    schema.type = 'string';
  }
  for (const value of Object.values(schema)) {
    if (Array.isArray(value)) value.forEach(addExplicitPrimitiveTypes);
    else addExplicitPrimitiveTypes(value);
  }
}

// Codex Structured Outputs rejects JSON Schema conditionals and requires every
// object property to be listed in required. This schema is derived from the
// canonical artifact and stays closed; the canonical validator below remains
// authoritative for cross-field and material-kind rules.
export function codexHistoricalIngestOutputSchemaBytes() {
  const schema = structuredClone(historicalIngestSchema());
  schema.$id = 'https://zbs.gg/schemas/pulse/historical-ingest/codex-output/v1';
  schema.title = 'Pulse Historical Ingest Codex Output v1';
  const scopeSchema = schema.$defs.scope;
  delete scopeSchema.allOf;
  scopeSchema.required = Object.keys(scopeSchema.properties);
  scopeSchema.properties.project_id = nullable(scopeSchema.properties.project_id);
  const timeSchema = schema.$defs.validTime;
  timeSchema.required = Object.keys(timeSchema.properties);
  timeSchema.properties.to = nullable(timeSchema.properties.to);
  const payloadSchema = schema.$defs.payload;
  payloadSchema.required = Object.keys(payloadSchema.properties);
  for (const [key, value] of Object.entries(payloadSchema.properties)) payloadSchema.properties[key] = nullable(value);
  delete schema.$defs.materialItem.allOf;
  addExplicitPrimitiveTypes(schema);
  return Buffer.from(`${JSON.stringify(schema)}\n`);
}

export function normalizeCodexHistoricalIngestManifest(value) {
  const normalized = structuredClone(value);
  if (!normalized || !Array.isArray(normalized.items)) return normalized;
  for (const item of normalized.items) {
    if (item?.scope?.project_id === null) delete item.scope.project_id;
    if (item?.valid_time?.to === null) delete item.valid_time.to;
    if (item?.payload && typeof item.payload === 'object') {
      for (const [key, entry] of Object.entries(item.payload)) {
        if (entry === null) delete item.payload[key];
      }
    }
  }
  return normalized;
}

export function contentFreeUnitReceipt({ manifest, outputDigest, usage, model, effort, cliVersion }) {
  assertHistoricalIngestManifest(manifest);
  if (!HEX_DIGEST.test(outputDigest)) fail('output_digest');
  return Object.freeze({
    schema: 'pulse.historical_ingest.unit_receipt.v1',
    job_id: manifest.job_id,
    revision: manifest.revision,
    source_snapshot_digest: manifest.source_snapshot_digest,
    output_digest: outputDigest,
    item_count: manifest.items.length,
    model,
    effort,
    cli_version: cliVersion,
    usage: Object.freeze({ ...usage }),
  });
}
import { historicalIngestSchema } from './historical-ingest-schema.js';
