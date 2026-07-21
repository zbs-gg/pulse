import { createHash } from 'node:crypto';
import { homedir } from 'node:os';
import { dirname, isAbsolute, join, resolve } from 'node:path';

import { defaultPlatformServices, PlatformServicesError } from './platform-services.js';

const SCHEMA = 'pulse.unassigned_inbox.v1';
const CANDIDATE_SCHEMA = 'pulse.unassigned_candidate.v1';
const RECEIPT_SCHEMA = 'pulse.unassigned_receipt.v1';
const MAX_ITEMS = 50;
const MAX_RECEIPTS = 100;
const MAX_FILE_BYTES = 512 * 1024;
const MAX_CANDIDATE_BYTES = 32 * 1024;

const HOSTS = new Set(['chatgpt', 'claude', 'codex', 'claude-code', 'gemini-cli', 'cursor', 'langchain', 'crewai', 'pulse-cli']);
const SCOPES = new Set(['current_turn', 'user_selected_excerpt', 'project_context', 'install_event']);
const KINDS = new Set([
  'fact', 'decision', 'preference', 'project_state', 'open_loop', 'correction',
  'relationship_note', 'do_not_repeat', 'system_event', 'state_signal',
]);
const EVIDENCE = new Set(['user_selected', 'current_turn', 'assistant_inferred', 'tool_result', 'user_confirmed']);
const PRIVACY = new Set(['normal', 'sensitive', 'private']);
const RETENTION = new Set(['session', 'project', 'long_term']);
const SAFE_TAG = /^[\p{L}\p{N}][\p{L}\p{N}._:-]{0,63}$/u;
const SAFE_IDEMPOTENCY_KEY = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,191}$/;
const SAFE_DESTINATION_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$/;
const SAFE_HEX_DIGEST = /^[a-f0-9]{64}$/;
const RFC3339 = /^\d{4}-\d{2}-\d{2}[Tt]\d{2}:\d{2}:\d{2}(\.\d+)?([Zz]|[+-]\d{2}:\d{2})$/;
const SECRET_MARKERS = [
  '/users/', '/home/', '/volumes/', '/.ssh/', '../', 'file://', 'token=', 'api_key', 'apikey',
  'password', 'secret', 'private_key', 'begin private key', 'sk-', 'akia', 'xoxb-', 'ghp_',
  'gho_', 'ghu_', 'ghs_', 'github_pat_', 'aiza', 'ya29.', 'xoxp-', 'xapp-',
];
const TRANSCRIPT_ROLE_LINE = /^\s*(user|assistant|human|system|ai)\s*:/im;
const TRANSCRIPT_ROLE_JSON = /"role"\s*:\s*"(user|assistant|system)"/i;
const JWT = /\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b/;
const BEARER = /authorization\s*:\s*bearer\s+[A-Za-z0-9._~-]{12,}/i;
const POSIX_PATH = /(^|[\s"'(=])\/(?:etc|tmp|var|opt|usr|bin|sbin|dev|private|applications|library|users|home|volumes)(?:\/[A-Za-z0-9._~@%+,:=-]+)+/i;
const GENERIC_PATH = /(^|[\s"'(=])\/(?:[A-Za-z0-9._~@%+,:=-]+\/)+(?:[A-Za-z0-9._~@%+,:=-]+\.(?:md|txt|json|ya?ml|toml|ini|conf|env|pem|key|db|sqlite|log|go|js|ts|py|sh)|[A-Za-z0-9._~@%+,:=-]+\/[A-Za-z0-9._~@%+,:=-]+)/i;
const WINDOWS_PATH = /(^|\s)[a-z]:\\/i;
const UNC_PATH = /\\\\[^\\\s]+\\[^\\\s]+/;
const HIGH_ENTROPY_TOKEN = /[A-Za-z0-9_-]{40,}/g;

export class UnassignedInboxError extends Error {
  constructor(code, message = code) {
    super(message);
    this.name = 'UnassignedInboxError';
    this.code = code;
  }
}

function fail(code, message = code) { throw new UnassignedInboxError(code, message); }

function canonicalValue(value) {
  if (value === null || typeof value === 'boolean' || typeof value === 'string') return value;
  if (typeof value === 'number' && Number.isFinite(value)) return value;
  if (Array.isArray(value)) return value.map(canonicalValue);
  if (!value || typeof value !== 'object') fail('unassigned_noncanonical', 'unassigned candidate contains unsupported data');
  const out = {};
  for (const key of Object.keys(value).sort()) out[key] = canonicalValue(value[key]);
  return out;
}

function canonicalJSON(value) { return JSON.stringify(canonicalValue(value)); }

function digest(label, value) {
  return createHash('sha256').update(label).update('\0').update(canonicalJSON(value)).digest('hex');
}

function occurrences(haystack, needle) {
  let count = 0;
  for (let index = haystack.indexOf(needle); index >= 0; index = haystack.indexOf(needle, index + needle.length)) count += 1;
  return count;
}

function safeText(value, field, max, { required = true } = {}) {
  if (typeof value !== 'string') fail('unassigned_candidate_invalid', `${field} must be a string`);
  if (value !== value.normalize('NFC') || [...value].some((char) => {
    const codepoint = char.codePointAt(0);
    return codepoint < 0x20 || (codepoint >= 0x7f && codepoint <= 0x9f) ||
      (codepoint >= 0x200b && codepoint <= 0x200f) || (codepoint >= 0x2028 && codepoint <= 0x202e) ||
      (codepoint >= 0x2060 && codepoint <= 0x2069) || codepoint === 0xfeff;
  })) {
    fail('unassigned_text_unsafe', `${field} contains unsafe Unicode or controls`);
  }
  const normalized = value.trim();
  if (required && normalized.length === 0) fail('unassigned_candidate_invalid', `${field} is required`);
  if (Buffer.byteLength(normalized, 'utf8') > max) fail('unassigned_candidate_invalid', `${field} is too long`);
  const lower = normalized.toLowerCase();
  if (TRANSCRIPT_ROLE_LINE.test(normalized) || TRANSCRIPT_ROLE_JSON.test(normalized) ||
      occurrences(lower, 'user:') >= 3 || occurrences(lower, 'assistant:') >= 3 || occurrences(lower, '\n') > 30) {
    fail('unassigned_transcript_unsafe', `${field} looks like a raw transcript`);
  }
  const highEntropy = normalized.match(HIGH_ENTROPY_TOKEN) ?? [];
  const credentialLike = highEntropy.some((token) => {
    const lowerCase = /[a-z]/.test(token);
    const upperCase = /[A-Z]/.test(token);
    const digit = /[0-9]/.test(token);
    const separator = /[_-]/.test(token);
    const hexOnly = /^[a-f0-9]+$/i.test(token);
    return (hexOnly && token.length >= 64) || (lowerCase && upperCase && digit && (separator || token.length >= 48)) ||
      (token.length >= 48 && lowerCase && (digit || separator)) || (token.length >= 56 && lowerCase);
  });
  if (SECRET_MARKERS.some((marker) => lower.includes(marker)) || JWT.test(normalized) || BEARER.test(normalized) ||
      POSIX_PATH.test(normalized) || GENERIC_PATH.test(normalized) || WINDOWS_PATH.test(` ${normalized}`) ||
      UNC_PATH.test(normalized) || lower.includes('\\users\\') || lower.includes('\\.ssh\\') ||
      lower.startsWith('/') || lower.startsWith('~/') || lower.startsWith('./') || lower.startsWith('../') || credentialLike) {
    fail('unassigned_secret_or_path_unsafe', `${field} contains secret or path-like text`);
  }
  return normalized;
}

function exactKeys(record, keys, field) {
  if (!record || typeof record !== 'object' || Array.isArray(record) ||
      Object.keys(record).sort().join('\0') !== [...keys].sort().join('\0')) {
    fail('unassigned_candidate_invalid', `${field} has unsupported fields`);
  }
}

function enumValue(value, allowed, field) {
  if (typeof value !== 'string' || !allowed.has(value)) fail('unassigned_candidate_invalid', `${field} is unsupported`);
  return value;
}

function cleanCapsule(input, expectedHost) {
  exactKeys(input, ['schema', 'source', 'items', 'raw_input_included'], 'capsule');
  if (input.schema !== 'pulse.memory_capsule.v1' || input.raw_input_included !== false) {
    fail('unassigned_candidate_invalid', 'capsule must be structured and raw_input_included must be false');
  }
  exactKeys(input.source, ['host', 'conversation_scope', 'timestamp'], 'capsule.source');
  const host = enumValue(input.source.host, HOSTS, 'source.host');
  if (host !== expectedHost) fail('unassigned_host_mismatch', 'capsule host does not match the active harness');
  const conversationScope = enumValue(input.source.conversation_scope, SCOPES, 'source.conversation_scope');
  const timestamp = safeText(input.source.timestamp, 'source.timestamp', 64);
  if (!RFC3339.test(timestamp) || Number.isNaN(Date.parse(timestamp))) {
    fail('unassigned_candidate_invalid', 'source.timestamp must be RFC3339');
  }
  if (!Array.isArray(input.items) || input.items.length < 1 || input.items.length > 20) {
    fail('unassigned_candidate_invalid', 'capsule must contain 1..20 items');
  }
  const items = input.items.map((item, index) => {
    exactKeys(item, [
      'kind', 'redacted_summary', 'confidence', 'evidence_hint', 'privacy_tier', 'retention',
      ...(item?.tags === undefined ? [] : ['tags']),
    ], `items[${index}]`);
    if (typeof item.confidence !== 'number' || !Number.isFinite(item.confidence) || item.confidence < 0 || item.confidence > 1) {
      fail('unassigned_candidate_invalid', `items[${index}].confidence must be 0..1`);
    }
    const rawTags = item.tags ?? [];
    if (!Array.isArray(rawTags) || rawTags.length > 20) fail('unassigned_candidate_invalid', `items[${index}].tags is invalid`);
    const tags = rawTags.map((tag, tagIndex) => {
      const clean = safeText(tag, `items[${index}].tags[${tagIndex}]`, 64);
      if (!SAFE_TAG.test(clean)) fail('unassigned_candidate_invalid', `items[${index}].tags[${tagIndex}] is unsafe`);
      return clean;
    });
    return {
      kind: enumValue(item.kind, KINDS, `items[${index}].kind`),
      redacted_summary: safeText(item.redacted_summary, `items[${index}].redacted_summary`, 1200),
      confidence: item.confidence,
      evidence_hint: enumValue(item.evidence_hint, EVIDENCE, `items[${index}].evidence_hint`),
      privacy_tier: enumValue(item.privacy_tier, PRIVACY, `items[${index}].privacy_tier`),
      retention: enumValue(item.retention, RETENTION, `items[${index}].retention`),
      tags,
    };
  });
  return {
    schema: 'pulse.memory_capsule.v1',
    source: { host, conversation_scope: conversationScope, timestamp },
    items,
    raw_input_included: false,
  };
}

function emptyInbox() { return { schema: SCHEMA, items: [], receipts: [] }; }

function inspectPrivateDirectory(path, platformServices, { missing = false } = {}) {
  try { platformServices.assertPrivateState(path, { kind: 'directory' }); } catch (error) {
    if (missing && error?.code === 'ENOENT') return false;
    fail('unassigned_directory_unsafe', 'unassigned inbox directory is unsafe');
  }
  return true;
}

function ensurePrivateDirectory(path, platformServices) {
  try { platformServices.ensurePrivateDirectory(path); } catch {
    fail('unassigned_directory_unsafe', 'unassigned inbox directory is unsafe');
  }
}

function validateStoredInbox(value) {
  exactKeys(value, ['schema', 'items', 'receipts'], 'inbox');
  if (value.schema !== SCHEMA || !Array.isArray(value.items) || !Array.isArray(value.receipts) ||
      value.items.length > MAX_ITEMS || value.receipts.length > MAX_RECEIPTS) {
    fail('unassigned_invalid', 'unassigned inbox has an unsupported schema');
  }
  for (const item of value.items) {
    exactKeys(item, [
      'schema', 'item_id', 'content_digest', 'created_at', 'host', 'idempotency_key', 'candidate',
      ...(item?.assignment === undefined ? [] : ['assignment']),
    ], 'inbox item');
    if (item.schema !== CANDIDATE_SCHEMA || !/^unassigned_[a-f0-9]{32}$/.test(item.item_id ?? '') ||
        !/^[a-f0-9]{64}$/.test(item.content_digest ?? '') || !SAFE_IDEMPOTENCY_KEY.test(item.idempotency_key ?? '')) {
      fail('unassigned_invalid', 'unassigned inbox item is invalid');
    }
    if (item.assignment !== undefined) {
      exactKeys(item.assignment, [
        'schema', 'binding_digest', 'repository_id', 'store_id', 'created_at',
      ], 'assignment intent');
      if (item.assignment.schema !== 'pulse.unassigned_assignment_intent.v1' ||
          !SAFE_HEX_DIGEST.test(item.assignment.binding_digest ?? '') ||
          !SAFE_DESTINATION_ID.test(item.assignment.repository_id ?? '') ||
          !SAFE_DESTINATION_ID.test(item.assignment.store_id ?? '') ||
          !RFC3339.test(item.assignment.created_at ?? '')) {
        fail('unassigned_invalid', 'unassigned assignment intent is invalid');
      }
    }
    const capsule = cleanCapsule(item.candidate?.capsule, item.host);
    exactKeys(item.candidate, ['kind', 'capsule'], 'candidate');
    if (item.candidate.kind !== 'memory_capsule' || digest('pulse-unassigned-candidate-v1', { kind: 'memory_capsule', capsule }) !== item.content_digest) {
      fail('unassigned_digest_mismatch', 'unassigned inbox candidate digest does not match');
    }
  }
  for (const receipt of value.receipts) {
    const destinationFields = receipt.action === 'assign' ? ['binding_digest', 'repository_id', 'store_id'] : [];
    exactKeys(receipt, [
      'receipt_id', 'item_id', 'content_digest', 'action', 'status', 'created_at', ...destinationFields,
    ], 'receipt');
    const terminalPair = `${receipt.action}:${receipt.status}`;
    if (!/^unassigned_receipt_[a-f0-9]{32}$/.test(receipt.receipt_id ?? '') ||
        !/^unassigned_[a-f0-9]{32}$/.test(receipt.item_id ?? '') ||
        !/^[a-f0-9]{64}$/.test(receipt.content_digest ?? '') ||
        !['stage:staged', 'assign:assigning', 'assign:assigned', 'delete:deleted'].includes(terminalPair) ||
        (receipt.action === 'assign' && (!SAFE_HEX_DIGEST.test(receipt.binding_digest ?? '') ||
          !SAFE_DESTINATION_ID.test(receipt.repository_id ?? '') || !SAFE_DESTINATION_ID.test(receipt.store_id ?? '')))) {
      fail('unassigned_invalid', 'unassigned inbox receipt is invalid');
    }
  }
  return value;
}

function readUnlocked(path, platformServices) {
  if (!inspectPrivateDirectory(dirname(path), platformServices, { missing: true })) return emptyInbox();
  let bytes;
  try { bytes = platformServices.readPrivateFile(path, { missing: true, minBytes: 1, maxBytes: MAX_FILE_BYTES }); } catch {
    fail('unassigned_file_unsafe', 'unassigned inbox file is unsafe: it must be a private regular file');
  }
  if (bytes === null) return emptyInbox();
  let value;
  try { value = JSON.parse(bytes); } catch { fail('unassigned_invalid', 'unassigned inbox is invalid JSON'); }
  return validateStoredInbox(value);
}

function writeUnlocked(path, value, platformServices) {
  ensurePrivateDirectory(dirname(path), platformServices);
  const bytes = `${canonicalJSON(value)}\n`;
  if (Buffer.byteLength(bytes) > MAX_FILE_BYTES) fail('unassigned_capacity', 'unassigned inbox is full');
  try {
    platformServices.atomicWritePrivateFile(path, bytes, { ensureParent: false, maxBytes: MAX_FILE_BYTES });
  } catch {
    fail('unassigned_file_unsafe', 'unassigned inbox file cannot be written safely');
  }
}

function withLock(path, platformServices, operation) {
  ensurePrivateDirectory(dirname(path), platformServices);
  const lockPath = `${path}.lock`;
  let release;
  try {
    release = platformServices.acquirePrivateLock(lockPath, { staleAfterMs: 30_000, timeoutMs: 5000 });
  } catch (error) {
    if (error instanceof PlatformServicesError && error.code === 'platform_lock_occupied') {
      fail('unassigned_locked', 'unassigned inbox is busy');
    }
    fail('unassigned_lock_unsafe', 'unassigned inbox lock cannot be created');
  }
  try {
    return operation();
  } finally {
    try { release(); } catch { fail('unassigned_lock_unsafe', 'unassigned inbox lock cannot be released'); }
  }
}

export function unassignedInboxPath(home = homedir()) {
  if (typeof home !== 'string' || !isAbsolute(home)) fail('unassigned_home_invalid');
  return join(resolve(home), '.pulse', 'supervisor', 'unassigned-inbox.json');
}

export function readUnassignedInbox(path = unassignedInboxPath(), { platformServices = defaultPlatformServices } = {}) {
  if (typeof path !== 'string' || !isAbsolute(path)) fail('unassigned_path_invalid');
  return readUnlocked(resolve(path), platformServices);
}

export function stageUnassignedCapsule(input, {
  path = unassignedInboxPath(), host, idempotencyKey, now = new Date(),
  platformServices = defaultPlatformServices,
} = {}) {
  if (typeof path !== 'string' || !isAbsolute(path)) fail('unassigned_path_invalid');
  if (!HOSTS.has(host)) fail('unassigned_host_invalid');
  if (!SAFE_IDEMPOTENCY_KEY.test(idempotencyKey ?? '')) fail('unassigned_idempotency_invalid');
  const capsule = cleanCapsule(input, host);
  const candidate = { kind: 'memory_capsule', capsule };
  const candidateBytes = Buffer.byteLength(canonicalJSON(candidate));
  if (candidateBytes > MAX_CANDIDATE_BYTES) fail('unassigned_candidate_too_large');
  const contentDigest = digest('pulse-unassigned-candidate-v1', candidate);
  const itemID = `unassigned_${contentDigest.slice(0, 32)}`;
  const receiptID = `unassigned_receipt_${digest('pulse-unassigned-stage-receipt-v1', { idempotency_key: idempotencyKey, item_id: itemID }).slice(0, 32)}`;
  const createdAt = now.toISOString();
  const selectedPath = resolve(path);
  return withLock(selectedPath, platformServices, () => {
    const inbox = readUnlocked(selectedPath, platformServices);
    const priorReceipt = inbox.receipts.find((receipt) => receipt.receipt_id === receiptID);
    if (priorReceipt) {
      const priorItem = inbox.items.find((item) => item.item_id === priorReceipt.item_id);
      if (!priorItem) {
        const terminal = [...inbox.receipts].reverse().find((receipt) =>
          receipt.item_id === priorReceipt.item_id && receipt.content_digest === contentDigest &&
          ((receipt.action === 'assign' && receipt.status === 'assigned') ||
            (receipt.action === 'delete' && receipt.status === 'deleted')));
        if (!terminal) fail('unassigned_idempotency_conflict');
        return {
          schema: 'pulse.unassigned_stage_receipt.v1', status: terminal.status,
          destination: terminal.action === 'assign' ? 'memory_tray' : 'deleted', receipts: [terminal],
        };
      }
      if (priorItem.content_digest !== contentDigest) fail('unassigned_idempotency_conflict');
      return {
        schema: 'pulse.unassigned_stage_receipt.v1', status: 'staged', destination: 'unassigned_inbox',
        receipts: [{ ...priorReceipt, destination: 'unassigned_inbox' }],
      };
    }
    const existingItem = inbox.items.find((item) => item.item_id === itemID);
    if (existingItem) {
      if (existingItem.content_digest !== contentDigest) fail('unassigned_digest_collision');
      const existingReceipt = [...inbox.receipts].reverse().find((receipt) =>
        receipt.item_id === itemID && receipt.action === 'stage' && receipt.status === 'staged');
      if (!existingReceipt) fail('unassigned_receipt_missing');
      return {
        schema: 'pulse.unassigned_stage_receipt.v1', status: 'staged', destination: 'unassigned_inbox',
        receipts: [{ ...existingReceipt, destination: 'unassigned_inbox' }],
      };
    }
    if (inbox.items.length >= MAX_ITEMS) fail('unassigned_capacity', 'unassigned inbox is full');
    const item = {
      schema: CANDIDATE_SCHEMA, item_id: itemID, content_digest: contentDigest, created_at: createdAt,
      host, idempotency_key: idempotencyKey, candidate,
    };
    const receipt = {
      receipt_id: receiptID, item_id: itemID, content_digest: contentDigest,
      action: 'stage', status: 'staged', created_at: createdAt,
    };
    inbox.items.push(item);
    inbox.receipts.push(receipt);
    if (inbox.receipts.length > MAX_RECEIPTS) inbox.receipts.splice(0, inbox.receipts.length - MAX_RECEIPTS);
    writeUnlocked(selectedPath, inbox, platformServices);
    return {
      schema: 'pulse.unassigned_stage_receipt.v1', status: 'staged', destination: 'unassigned_inbox',
      receipts: [{ ...receipt, destination: 'unassigned_inbox' }],
    };
  });
}
