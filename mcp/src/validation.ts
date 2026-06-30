/**
 * Shared content contract for the standalone (Safe Mode) store and the MCP
 * boundary.
 *
 * This is a 1:1 port of the Go daemon validators in
 *   pulse-app/internal/store/memory_capsule.go  (validateMemoryCapsule)
 *   pulse-app/internal/store/semantic_delta.go   (validateSemanticDelta)
 *   pulse-app/internal/store/continuity.go        (validateContinuityStrings)
 *
 * WHY this exists: the JSON Schema advertised in tools/list is caller-advisory
 * only — a careless or hostile MCP client can send anything. Without this, the
 * zero-config Safe Mode path would persist secrets, file paths, or raw
 * transcripts and hand them to a future agent, breaking Pulse's core trust
 * contract. The schema is not the gate; this is.
 *
 * Keep in sync with the Go validators. The unit tests + published-package
 * negative smoke are the sync guard.
 */

export const CAPSULE_SCHEMA = 'pulse.memory_capsule.v1';
export const DELTA_SCHEMA = 'pulse.semantic_delta.v1';

const HOSTS = new Set([
  'chatgpt', 'claude', 'codex', 'claude-code', 'gemini-cli', 'cursor', 'langchain', 'crewai', 'pulse-cli',
]);
const SCOPES = new Set(['current_turn', 'user_selected_excerpt', 'project_context', 'install_event']);
const KINDS = new Set([
  'fact', 'decision', 'preference', 'project_state', 'open_loop', 'correction',
  'relationship_note', 'do_not_repeat', 'system_event', 'state_signal',
]);
const EVIDENCE = new Set(['user_selected', 'current_turn', 'assistant_inferred', 'tool_result', 'user_confirmed']);
const PRIVACY = new Set(['normal', 'sensitive', 'private']);
const RETENTION = new Set(['session', 'project', 'long_term']);
const ENTITY_KINDS = new Set([
  'person', 'place', 'project', 'org', 'product', 'community', 'skill', 'concept', 'thing', 'event_series',
]);
const DOMAINS = new Set(['real', 'fiction_content', 'fiction_meta', 'meta_authorial']);
const ASSERTION_SCOPE_TYPES = new Set(['personal', 'project', 'repo', 'agent', 'session']);
const ASSERTION_VISIBILITY = new Set(['private', 'shared']);
const PLUTCHIK = new Set([
  'joy', 'sadness', 'anger', 'fear', 'trust', 'disgust', 'anticipation', 'surprise', 'shame', 'guilt',
]);

const SAFE_TAG = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$/;
const SEMANTIC_REF = /^[A-Za-z0-9][A-Za-z0-9._:-]{1,95}$/;
const RFC3339 = /^\d{4}-\d{2}-\d{2}[Tt]\d{2}:\d{2}:\d{2}(\.\d+)?([Zz]|[+-]\d{2}:\d{2})$/;

// Mirror of looksSensitiveOrPathLike (memory_capsule.go).
const SECRET_MARKERS = [
  '/users/', 'file://', 'token=', 'api_key', 'apikey', 'password', 'secret',
  'private_key', 'begin private key', 'sk-', 'akia', 'xoxb-', 'ghp_',
];

export class ContractError extends Error {}

function fail(msg: string): never {
  throw new ContractError(msg);
}

function occurrences(haystack: string, needle: string): number {
  let n = 0;
  let i = haystack.indexOf(needle);
  while (i >= 0) {
    n += 1;
    i = haystack.indexOf(needle, i + needle.length);
  }
  return n;
}

function looksLikeTranscript(text: string): boolean {
  const lower = text.toLowerCase();
  return occurrences(lower, 'user:') >= 3 || occurrences(lower, 'assistant:') >= 3 || occurrences(lower, '\n') > 30;
}

function looksSensitiveOrPathLike(text: string): boolean {
  const lower = text.toLowerCase();
  return SECRET_MARKERS.some((marker) => lower.includes(marker));
}

function asRecord(value: unknown, what: string): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    fail(`invalid ${what}: expected an object`);
  }
  return value as Record<string, unknown>;
}

// Mirror of validateSemanticText: trims, enforces max length, rejects
// transcript/secret/path-like content. Returns the trimmed value.
function safeText(field: string, value: unknown, max: number, required: boolean): string {
  if (value === undefined || value === null || value === '') {
    if (required) fail(`${field} is required`);
    return '';
  }
  if (typeof value !== 'string') fail(`${field} must be a string`);
  const trimmed = value.trim();
  if (trimmed === '') {
    if (required) fail(`${field} is required`);
    return '';
  }
  if (trimmed.length > max) fail(`${field} is too long`);
  if (looksLikeTranscript(trimmed)) fail(`${field} looks like raw transcript`);
  if (looksSensitiveOrPathLike(trimmed)) fail(`${field} contains secret/path-like text`);
  return trimmed;
}

function rfc3339(field: string, value: unknown): string {
  if (typeof value !== 'string' || !RFC3339.test(value.trim()) || Number.isNaN(Date.parse(value.trim()))) {
    fail(`${field} must be RFC3339`);
  }
  return (value as string).trim();
}

function num01(field: string, value: unknown, fallback: number): number {
  if (value === undefined || value === null) return fallback;
  if (typeof value !== 'number' || Number.isNaN(value)) fail(`${field} must be a number`);
  if (value < 0 || value > 1) fail(`${field} must be 0..1`);
  return value;
}

function inEnum(field: string, value: unknown, set: Set<string>): string {
  if (typeof value !== 'string' || !set.has(value)) fail(`${field} is unsupported or missing`);
  return value;
}

function validTag(tag: unknown): tag is string {
  if (typeof tag !== 'string') return false;
  const t = tag.trim();
  return t !== '' && t.length <= 64 && !looksSensitiveOrPathLike(t) && SAFE_TAG.test(t);
}

export interface CleanItem {
  kind: string;
  redacted_summary: string;
  confidence: number;
  evidence_hint: string;
  privacy_tier: string;
  retention: string;
  tags: string[];
}
export interface CleanCapsule {
  source: { host: string; conversation_scope: string; timestamp: string };
  items: CleanItem[];
}

export function validateCapsule(input: unknown): CleanCapsule {
  const capsule = asRecord(input, 'memory capsule');
  if (capsule.schema !== CAPSULE_SCHEMA) fail(`schema must be ${CAPSULE_SCHEMA}`);
  if (capsule.raw_input_included !== false) fail('raw_input_included must be false');
  const source = asRecord(capsule.source, 'memory capsule source');
  const host = inEnum('source.host', source.host, HOSTS);
  const scope = inEnum('source.conversation_scope', source.conversation_scope, SCOPES);
  const timestamp = rfc3339('source.timestamp', source.timestamp);
  if (!Array.isArray(capsule.items) || capsule.items.length === 0) fail('items are required');
  if (capsule.items.length > 20) fail('too many items: max 20');
  const items: CleanItem[] = capsule.items.map((entry, i) => {
    const item = asRecord(entry, `items[${i}]`);
    const kind = inEnum(`items[${i}].kind`, item.kind, KINDS);
    const redacted_summary = safeText(`items[${i}].redacted_summary`, item.redacted_summary, 1200, true);
    const confidence = num01(`items[${i}].confidence`, item.confidence, 0.5);
    const evidence_hint = inEnum(`items[${i}].evidence_hint`, item.evidence_hint, EVIDENCE);
    const privacy_tier = inEnum(`items[${i}].privacy_tier`, item.privacy_tier, PRIVACY);
    const retention = inEnum(`items[${i}].retention`, item.retention, RETENTION);
    const rawTags = Array.isArray(item.tags) ? item.tags : [];
    rawTags.forEach((tag, j) => {
      if (!validTag(tag)) fail(`items[${i}].tags[${j}] is an unsafe tag`);
    });
    return {
      kind,
      redacted_summary,
      confidence,
      evidence_hint,
      privacy_tier,
      retention,
      tags: (rawTags as string[]).map((t) => t.trim()),
    };
  });
  return { source: { host, conversation_scope: scope, timestamp }, items };
}

export interface CleanNode {
  client_id: string;
  kind: string;
  canonical_name: string;
  summary: string;
  privacy_tier: string;
  domain: string;
  salience: number;
  emotional_weight: number;
  aliases: string[];
}
export interface CleanEdge {
  from: string;
  to: string;
  kind: string;
  summary: string;
  privacy_tier: string;
  strength: number;
}
export interface CleanFact {
  node: string;
  text: string;
  predicate: string;
  object_text: string;
  valid_from: string;
  source_event_refs: string[];
  scope_type: string;
  scope_id: string;
  visibility: string;
  confidence: number;
  privacy_tier: string;
  domain: string;
}
export interface CleanEvent {
  client_id: string;
  title: string;
  summary: string;
  sentiment: string;
  entity_refs: string[];
  emotional_weight: number;
  confidence: number;
  privacy_tier: string;
  domain: string;
  occurred_at: string;
  emotions: Record<string, number>;
}
export interface CleanContinuity {
  summary: string;
  decisions: string[];
  open_loops: string[];
  do_not_repeat: string[];
  emotional_anchors: string[];
  state_signals: string[];
  active_threads: string[];
  review_insights: string[];
}
export interface CleanDelta {
  source: {
    host: string;
    conversation_scope: string;
    timestamp: string;
    thread_id?: string;
    session_id?: string;
    project_id?: string;
  };
  nodes: CleanNode[];
  edges: CleanEdge[];
  facts: CleanFact[];
  events: CleanEvent[];
  continuity?: CleanContinuity;
}

function normalizeDomain(value: unknown): string {
  if (value === undefined || value === null || (typeof value === 'string' && value.trim() === '')) return 'real';
  if (typeof value !== 'string' || !DOMAINS.has(value.trim())) fail('domain is unsupported');
  return value.trim();
}

function validRef(value: unknown): value is string {
  return typeof value === 'string' && SEMANTIC_REF.test(value.trim()) && !looksSensitiveOrPathLike(value.trim());
}

function validSlug(value: unknown): value is string {
  if (typeof value !== 'string') return false;
  const s = value.trim();
  return s !== '' && s.length <= 64 && SAFE_TAG.test(s) && !looksSensitiveOrPathLike(s);
}

function continuityStrings(field: string, value: unknown): string[] {
  if (value === undefined || value === null) return [];
  if (!Array.isArray(value)) fail(`${field} must be an array`);
  if (value.length > 20) fail(`${field} has too many items: max 20`);
  return value.map((entry, i) => safeText(`${field}[${i}]`, entry, 1200, true));
}

export function validateDelta(input: unknown): CleanDelta {
  const delta = asRecord(input, 'semantic delta');
  if (delta.schema !== DELTA_SCHEMA) fail(`schema must be ${DELTA_SCHEMA}`);
  if (delta.raw_input_included !== false) fail('raw_input_included must be false');
  const source = asRecord(delta.source, 'semantic delta source');
  const host = inEnum('source.host', source.host, HOSTS);
  const scope = inEnum('source.conversation_scope', source.conversation_scope, SCOPES);
  const timestamp = rfc3339('source.timestamp', source.timestamp);

  const nodesIn = Array.isArray(delta.nodes) ? delta.nodes : [];
  const edgesIn = Array.isArray(delta.edges) ? delta.edges : [];
  const factsIn = Array.isArray(delta.facts) ? delta.facts : [];
  const eventsIn = Array.isArray(delta.events) ? delta.events : [];
  const hasContinuity = delta.continuity !== undefined && delta.continuity !== null;
  if (nodesIn.length === 0 && edgesIn.length === 0 && factsIn.length === 0 && eventsIn.length === 0 && !hasContinuity) {
    fail('semantic delta must include graph content or continuity');
  }
  if (nodesIn.length > 30) fail('nodes has too many items: max 30');
  if (edgesIn.length > 50) fail('edges has too many items: max 50');
  if (factsIn.length > 50) fail('facts has too many items: max 50');
  if (eventsIn.length > 20) fail('events has too many items: max 20');

  const refs = new Set<string>();
  const nodes: CleanNode[] = nodesIn.map((entry, i) => {
    const node = asRecord(entry, `nodes[${i}]`);
    if (!validRef(node.client_id)) fail(`nodes[${i}].client_id is unsafe`);
    const clientId = (node.client_id as string).trim();
    if (refs.has(clientId)) fail(`nodes[${i}].client_id is duplicate`);
    refs.add(clientId);
    const kind = inEnum(`nodes[${i}].kind`, node.kind, ENTITY_KINDS);
    const canonical_name = safeText(`nodes[${i}].canonical_name`, node.canonical_name, 160, true);
    const summary = safeText(`nodes[${i}].summary`, node.summary, 1200, false);
    const privacy_tier = inEnum(`nodes[${i}].privacy_tier`, node.privacy_tier, PRIVACY);
    const domain = normalizeDomain(node.domain);
    const salience = num01(`nodes[${i}].salience`, node.salience, 0);
    const emotional_weight = num01(`nodes[${i}].emotional_weight`, node.emotional_weight, 0);
    const aliasesIn = Array.isArray(node.aliases) ? node.aliases : [];
    const aliases = aliasesIn.map((a, j) => safeText(`nodes[${i}].aliases[${j}]`, a, 160, true));
    return { client_id: clientId, kind, canonical_name, summary, privacy_tier, domain, salience, emotional_weight, aliases };
  });

  const edges: CleanEdge[] = edgesIn.map((entry, i) => {
    const edge = asRecord(entry, `edges[${i}]`);
    const from = typeof edge.from === 'string' ? edge.from.trim() : '';
    const to = typeof edge.to === 'string' ? edge.to.trim() : '';
    if (!refs.has(from)) fail(`edges[${i}].from references unknown node`);
    if (!refs.has(to)) fail(`edges[${i}].to references unknown node`);
    if (!validSlug(edge.kind)) fail(`edges[${i}].kind is unsafe`);
    const summary = safeText(`edges[${i}].summary`, edge.summary, 1200, false);
    const privacy_tier = inEnum(`edges[${i}].privacy_tier`, edge.privacy_tier, PRIVACY);
    const strength = num01(`edges[${i}].strength`, edge.strength, 0);
    return { from, to, kind: (edge.kind as string).trim(), summary, privacy_tier, strength };
  });

  const eventRefs = new Set<string>();
  eventsIn.forEach((entry, i) => {
    const event = asRecord(entry, `events[${i}]`);
    if (typeof event.client_id === 'string' && validRef(event.client_id)) {
      const clientId = event.client_id.trim();
      if (eventRefs.has(clientId)) fail(`events[${i}].client_id is duplicate`);
      eventRefs.add(clientId);
    }
  });

  const facts: CleanFact[] = factsIn.map((entry, i) => {
    const fact = asRecord(entry, `facts[${i}]`);
    const node = typeof fact.node === 'string' ? fact.node.trim() : '';
    if (!refs.has(node)) fail(`facts[${i}].node references unknown node`);
    const text = safeText(`facts[${i}].text`, fact.text, 1200, true);
    const hasStructuredAssertion =
      (typeof fact.predicate === 'string' && fact.predicate.trim() !== '') ||
      (typeof fact.object_text === 'string' && fact.object_text.trim() !== '');
    const predicate = hasStructuredAssertion
      ? safeText(`facts[${i}].predicate`, fact.predicate, 160, true)
      : '';
    const object_text = hasStructuredAssertion
      ? safeText(`facts[${i}].object_text`, fact.object_text, 1200, true)
      : '';
    const valid_from =
      fact.valid_from === undefined || fact.valid_from === null || fact.valid_from === ''
        ? ''
        : rfc3339(`facts[${i}].valid_from`, fact.valid_from);
    const scope_type =
      fact.scope_type === undefined || fact.scope_type === null || fact.scope_type === ''
        ? ''
        : inEnum(`facts[${i}].scope_type`, fact.scope_type, ASSERTION_SCOPE_TYPES);
    const scope_id = safeText(`facts[${i}].scope_id`, fact.scope_id, 160, false);
    if (scope_type === '' && scope_id !== '') {
      fail(`facts[${i}].scope_type is required when scope_id is set`);
    }
    if (scope_type !== '' && scope_type !== 'personal' && scope_id === '') {
      fail(`facts[${i}].scope_id is required for ${scope_type} scope`);
    }
    const sourceEventRefsIn =
      fact.source_event_refs === undefined || fact.source_event_refs === null
        ? []
        : fact.source_event_refs;
    if (!Array.isArray(sourceEventRefsIn)) fail(`facts[${i}].source_event_refs must be an array`);
    if (sourceEventRefsIn.length > 20) fail(`facts[${i}].source_event_refs has too many items: max 20`);
    const source_event_refs = sourceEventRefsIn.map((ref, j) => {
      const r = typeof ref === 'string' ? ref.trim() : '';
      if (!validRef(r)) fail(`facts[${i}].source_event_refs[${j}] is unsafe`);
      if (!eventRefs.has(r)) fail(`facts[${i}].source_event_refs[${j}] references unknown event`);
      return r;
    });
    const visibility =
      fact.visibility === undefined || fact.visibility === null || fact.visibility === ''
        ? ''
        : inEnum(`facts[${i}].visibility`, fact.visibility, ASSERTION_VISIBILITY);
    const confidence = num01(`facts[${i}].confidence`, fact.confidence, 0);
    const privacy_tier = inEnum(`facts[${i}].privacy_tier`, fact.privacy_tier, PRIVACY);
    const domain = normalizeDomain(fact.domain);
    return {
      node,
      text,
      predicate,
      object_text,
      valid_from,
      source_event_refs,
      scope_type,
      scope_id,
      visibility,
      confidence,
      privacy_tier,
      domain,
    };
  });

  const events: CleanEvent[] = eventsIn.map((entry, i) => {
    const event = asRecord(entry, `events[${i}]`);
    if (!validRef(event.client_id)) fail(`events[${i}].client_id is unsafe`);
    const title = safeText(`events[${i}].title`, event.title, 180, true);
    const summary = safeText(`events[${i}].summary`, event.summary, 1200, true);
    const sentiment = safeText(`events[${i}].sentiment`, event.sentiment, 240, false);
    const entityRefsIn = Array.isArray(event.entity_refs) ? event.entity_refs : [];
    const entity_refs = entityRefsIn.map((ref, j) => {
      const r = typeof ref === 'string' ? ref.trim() : '';
      if (!refs.has(r)) fail(`events[${i}].entity_refs[${j}] references unknown node`);
      return r;
    });
    const emotional_weight = num01(`events[${i}].emotional_weight`, event.emotional_weight, 0);
    const confidence = num01(`events[${i}].confidence`, event.confidence, 0);
    const privacy_tier = inEnum(`events[${i}].privacy_tier`, event.privacy_tier, PRIVACY);
    const domain = normalizeDomain(event.domain);
    const occurred_at =
      event.occurred_at === undefined || event.occurred_at === null || event.occurred_at === ''
        ? ''
        : rfc3339(`events[${i}].occurred_at`, event.occurred_at);
    const emotions: Record<string, number> = {};
    if (event.emotions !== undefined && event.emotions !== null) {
      const emo = asRecord(event.emotions, `events[${i}].emotions`);
      const keys = Object.keys(emo);
      if (keys.length > 10) fail(`events[${i}].emotions has too many keys`);
      for (const key of keys) {
        if (!PLUTCHIK.has(key)) fail(`events[${i}].emotions key "${key}" is not a Plutchik-10 emotion`);
        emotions[key] = num01(`events[${i}].emotions["${key}"]`, emo[key], 0);
      }
    }
    return {
      client_id: (event.client_id as string).trim(),
      title,
      summary,
      sentiment,
      entity_refs,
      emotional_weight,
      confidence,
      privacy_tier,
      domain,
      occurred_at,
      emotions,
    };
  });

  let continuity: CleanContinuity | undefined;
  if (hasContinuity) {
    const c = asRecord(delta.continuity, 'continuity');
    continuity = {
      summary: safeText('continuity.summary', c.summary, 1200, true),
      decisions: continuityStrings('continuity.decisions', c.decisions),
      open_loops: continuityStrings('continuity.open_loops', c.open_loops),
      do_not_repeat: continuityStrings('continuity.do_not_repeat', c.do_not_repeat),
      emotional_anchors: continuityStrings('continuity.emotional_anchors', c.emotional_anchors),
      state_signals: continuityStrings('continuity.state_signals', c.state_signals),
      active_threads: continuityStrings('continuity.active_threads', c.active_threads),
      review_insights: continuityStrings('continuity.review_insights', c.review_insights),
    };
  }

  const cleanSource: CleanDelta['source'] = { host, conversation_scope: scope, timestamp };
  if (typeof source.thread_id === 'string' && source.thread_id.trim() !== '') {
    cleanSource.thread_id = source.thread_id.trim();
  }
  if (typeof source.session_id === 'string' && source.session_id.trim() !== '') {
    cleanSource.session_id = source.session_id.trim();
  }
  if (typeof source.project_id === 'string' && source.project_id.trim() !== '') {
    cleanSource.project_id = source.project_id.trim();
  }

  return { source: cleanSource, nodes, edges, facts, events, continuity };
}
