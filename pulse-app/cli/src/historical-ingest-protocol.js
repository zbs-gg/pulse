import { createHash } from 'node:crypto';

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
const UNSAFE_MATERIAL_TEXT = /[\p{Cc}\p{Cf}]/u;

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
    const maximum = {
      title: 4000, summary: 4000, subject_id: 256, predicate: 128, object_value: 4000,
      object_id: 256, entity_type: 64, name: 512, state_kind: 128, continuity_status: 32,
    }[key];
    if (typeof item !== 'string' || item.length < 1 || Buffer.byteLength(item, 'utf8') > maximum ||
        item.normalize('NFC') !== item || UNSAFE_MATERIAL_TEXT.test(item)) fail(`payload_${key}`);
    if (['subject_id', 'object_id'].includes(key) && !OPAQUE_LOCATOR.test(item)) fail(`payload_${key}`);
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

// The canonical manifest remains unchanged, but the model sees only Pulse's
// atomic memory contract. Graph-shaped material is neither requested nor
// accepted. Normalization below converts these atoms into the canonical review
// manifest before the existing validator and apply path see them.
export function codexHistoricalIngestOutputSchemaBytes() {
  const nullable = (schema) => ({ anyOf: [schema, { type: 'null' }] });
  const schema = {
    $schema: 'https://json-schema.org/draft/2020-12/schema',
    $id: 'https://zbs.gg/schemas/pulse/historical-ingest/codex-atoms/v2',
    title: 'Pulse Historical Atomic Memory Output v2',
    type: 'object', additionalProperties: false,
    required: ['schema_version', 'job_id', 'revision', 'source_snapshot_digest', 'items'],
    properties: {
      schema_version: { type: 'string', const: SCHEMA_VERSION },
      job_id: { type: 'string', pattern: '^job_[a-f0-9]{16,64}$' },
      revision: { type: 'integer', minimum: 1 },
      source_snapshot_digest: { $ref: '#/$defs/digest' },
      items: { type: 'array', maxItems: 10000, items: { $ref: '#/$defs/atom' } },
    },
    $defs: {
      digest: { type: 'string', pattern: '^[a-f0-9]{64}$' },
      atom: {
        type: 'object', additionalProperties: false,
        required: ['kind', 'summary', 'source_ids', 'emotion_label', 'emotion_intensity'],
        properties: {
          kind: { type: 'string', enum: ['fact', 'preference', 'event', 'decision', 'correction', 'open_question', 'project_state', 'emotion'] },
          summary: { type: 'string', minLength: 1, maxLength: 400 },
          source_ids: { type: 'array', minItems: 1, maxItems: 512, items: { type: 'string', pattern: '^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$' } },
          emotion_label: nullable({ type: 'string', minLength: 1, maxLength: 64 }),
          emotion_intensity: nullable({ type: 'number', minimum: 0, maximum: 1 }),
        },
      },
    },
  };
  return Buffer.from(`${JSON.stringify(schema)}\n`);
}

function atomMaterial(item, sourceRefsByLocator) {
  if (!item || typeof item.summary !== 'string' || item.summary.length < 1 || item.summary.length > 400) return null;
  if (!(sourceRefsByLocator instanceof Map) || !Array.isArray(item.source_ids) || item.source_ids.length < 1) return null;
  const sources = [...new Set(item.source_ids)].map((locator) => sourceRefsByLocator.get(locator));
  if (sources.some((source) => !source?.ref || typeof source.timestamp !== 'string')) return null;
  const inferredEmotion = item.kind === 'emotion';
  const base = {
    candidate_id: `candidate_${createHash('sha256').update(canonicalJSON({
      kind: item.kind, summary: item.summary, source_ids: [...new Set(item.source_ids)].sort(),
    })).digest('hex')}`,
    confidence: inferredEmotion ? 0.7 : 1,
    privacy: 'private',
    epistemic_status: inferredEmotion ? 'hypothesis' : 'explicit',
    derivation: inferredEmotion ? 'inferred' : 'direct',
    valid_time: { from: sources.map((source) => source.timestamp).sort()[0] },
    scope: { kind: 'unassigned' },
    source_refs: structuredClone(sources.map((source) => source.ref)),
  };
  switch (item.kind) {
  case 'decision': return { ...base, kind: 'decision', payload: { summary: item.summary } };
  case 'event': return { ...base, kind: 'event', payload: { title: item.summary, summary: item.summary } };
  case 'open_question': return { ...base, kind: 'continuity', payload: { summary: item.summary, continuity_status: 'open' } };
  case 'project_state': return { ...base, kind: 'state', payload: { state_kind: 'project_state', summary: item.summary } };
  case 'emotion':
    if (typeof item.emotion_label !== 'string' || typeof item.emotion_intensity !== 'number') return null;
    return { ...base, kind: 'state', payload: { state_kind: item.emotion_label, summary: item.summary, intensity: item.emotion_intensity } };
  case 'fact':
  case 'preference':
  case 'correction':
    return { ...base, kind: 'assertion', payload: { subject_id: 'memory_subject', predicate: item.kind, object_value: item.summary } };
  default: return null;
  }
}

export function normalizeCodexHistoricalIngestManifest(value, { sourceRefsByLocator } = {}) {
  const normalized = structuredClone(value);
  if (!normalized || !Array.isArray(normalized.items)) return normalized;
  if (normalized.items.some((item) => Object.hasOwn(item ?? {}, 'summary'))) {
    normalized.items = normalizeDuplicateCandidateIDs(normalized.items.map((item) => atomMaterial(item, sourceRefsByLocator)).filter(Boolean));
    return normalized;
  }
  for (const item of normalized.items) {
    if (item?.scope?.project_id === null) delete item.scope.project_id;
    if (item?.valid_time && Object.hasOwn(item.valid_time, 'to')) {
      const from = item.valid_time.from;
      const to = item.valid_time.to;
      // The model-facing schema must require nullable optionals and cannot
      // express the canonical cross-field ordering rule. Preserve the
      // required start time, but discard an unusable optional end time before
      // the strict canonical validator runs.
      if (to === null || typeof to !== 'string' || !RFC3339.test(to) ||
          Number.isNaN(Date.parse(to)) || Date.parse(to) <= Date.parse(from)) {
        delete item.valid_time.to;
      }
    }
    if (item?.payload && typeof item.payload === 'object') {
      for (const [key, entry] of Object.entries(item.payload)) {
        if (entry === null) delete item.payload[key];
      }
    }
  }
  normalized.items = normalizeDuplicateCandidateIDs(
    normalized.items.filter((item) =>
      !new Set(['person', 'project', 'relation']).has(item?.kind) &&
      payloadIsCanonical(item?.payload, item?.kind)),
  );
  return normalized;
}

export function historicalCoverageRepairLocators(manifest, evidenceLocators) {
  assertHistoricalIngestManifest(manifest);
  if (!Array.isArray(evidenceLocators) || evidenceLocators.some((item) =>
    typeof item !== 'string' || !OPAQUE_LOCATOR.test(item))) fail('coverage_repair_locators');
  const covered = new Set(manifest.items.flatMap((item) =>
    item.source_refs.map((ref) => ref.record_locator)));
  return [...new Set(evidenceLocators)].filter((locator) => !covered.has(locator));
}

export function mergeHistoricalCoverageRepair(primary, repair, targetLocators) {
  assertHistoricalIngestManifest(primary);
  assertHistoricalIngestManifest(repair, {
    expectedJobID: primary.job_id,
    expectedSnapshotDigest: primary.source_snapshot_digest,
  });
  if (!Array.isArray(targetLocators) || targetLocators.length < 1 || targetLocators.some((item) =>
    typeof item !== 'string' || !OPAQUE_LOCATOR.test(item))) fail('coverage_repair_locators');
  const targets = new Set(targetLocators);
  const repairItems = repair.items.filter((item) =>
    item.source_refs.some((ref) => targets.has(ref.record_locator)));
  const merged = {
    schema_version: primary.schema_version,
    job_id: primary.job_id,
    revision: primary.revision,
    source_snapshot_digest: primary.source_snapshot_digest,
    items: normalizeDuplicateCandidateIDs([...primary.items, ...repairItems]),
  };
  return assertHistoricalIngestManifest(merged, {
    expectedJobID: primary.job_id,
    expectedSnapshotDigest: primary.source_snapshot_digest,
  });
}

function canonicalJSON(value) {
  if (Array.isArray(value)) return `[${value.map(canonicalJSON).join(',')}]`;
  if (value && typeof value === 'object') {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalJSON(value[key])}`).join(',')}}`;
  }
  return JSON.stringify(value);
}

function itemContentDigest(item) {
  const { candidate_id: _candidateID, ...content } = item;
  return createHash('sha256').update(canonicalJSON(content)).digest('hex');
}

function normalizeDuplicateCandidateIDs(items) {
  const seenIDs = new Set();
  const contentByOriginalID = new Map();
  const result = [];
  for (const item of items) {
    const originalID = item?.candidate_id;
    const contentDigest = itemContentDigest(item);
    if (!seenIDs.has(originalID)) {
      seenIDs.add(originalID);
      contentByOriginalID.set(originalID, new Set([contentDigest]));
      result.push(item);
      continue;
    }
    const originalContents = contentByOriginalID.get(originalID);
    if (originalContents?.has(contentDigest)) continue;
    originalContents?.add(contentDigest);
    let replacementID = `candidate_${contentDigest}`;
    let collision = 0;
    while (seenIDs.has(replacementID)) {
      collision += 1;
      replacementID = `candidate_${createHash('sha256').update(`${contentDigest}:${collision}`).digest('hex')}`;
    }
    seenIDs.add(replacementID);
    result.push({ ...item, candidate_id: replacementID });
  }
  return result;
}

function payloadIsCanonical(payloadValue, kind) {
  try {
    payload(payloadValue, kind);
    return true;
  } catch (error) {
    if (error instanceof HistoricalIngestProtocolError && error.code.startsWith('payload')) {
      return false;
    }
    throw error;
  }
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
