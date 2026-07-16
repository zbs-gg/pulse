import { createHash } from 'node:crypto';

import { continuityDeliveryAnnotation, isStableHostID } from './host-adapter.js';
import { callTeamRemoteTool, TeamRemoteClientError } from './team-remote-client.js';

const RESUME_SECTIONS = Object.freeze([
  'where_we_left_off',
  'active_decisions',
  'open_loops',
  'do_not_repeat',
  'relevant_emotional_state_context',
  'suggested_next_step',
]);
function safeID(value, code) {
  if (!isStableHostID(value)) throw new Error(code);
  return value;
}

function canonicalIDs(value, code) {
  if (value === undefined) return Object.freeze([]);
  if (!Array.isArray(value)) throw new Error(code);
  return Object.freeze([...new Set(value.map((item) => safeID(item, code)))].sort());
}

function manifest(objectIDs = [], evidenceIDs = []) {
  return Object.freeze({
    object_ids: canonicalIDs(objectIDs, 'resume_manifest_invalid'),
    evidence_ids: canonicalIDs(evidenceIDs, 'resume_manifest_invalid'),
  });
}

function localBaseline(result) {
  const fields = [
    result?.baseline_kind,
    result?.source_equivalent_tokens,
    result?.coverage_counted,
    result?.coverage_total,
  ];
  if (fields.every((value) => value === undefined)) return Object.freeze({});
  if (result?.baseline_kind !== 'canonical_structured_resume_v1' ||
      !Number.isSafeInteger(result?.source_equivalent_tokens) || result.source_equivalent_tokens < 0 ||
      result.source_equivalent_tokens > 10_485_760 ||
      !Number.isSafeInteger(result?.coverage_counted) || result.coverage_counted < 1 ||
      !Number.isSafeInteger(result?.coverage_total) || result.coverage_total < result.coverage_counted ||
      result.coverage_total > 1_000_000) {
    throw new Error('local_resume_baseline_invalid');
  }
  return Object.freeze({
    baseline_kind: result.baseline_kind,
    source_equivalent_tokens: result.source_equivalent_tokens,
    coverage_counted: result.coverage_counted,
    coverage_total: result.coverage_total,
  });
}

export function createContinuityDeliveryOffer(resolved, event, payload, renderedManifest) {
  if (typeof payload !== 'string' || payload.length === 0 ||
      !/^[a-f0-9]{64}$/.test(resolved?.binding?.binding_digest ?? '') ||
      !isStableHostID(resolved?.binding?.workspace?.repository_id) ||
      !['codex', 'claude-code', 'cursor'].includes(event?.host) ||
      !['session_start', 'subagent_start'].includes(event?.event) ||
      !isStableHostID(event?.session_id) ||
      !/^event_[a-f0-9]{64}$/.test(event?.source_event_key ?? '')) {
    throw new Error('continuity_delivery_source_invalid');
  }
  const bytes = Buffer.from(payload, 'utf8');
  const payloadDigest = createHash('sha256').update(bytes).digest('hex');
  const sessionRef = `session:${createHash('sha256')
    .update(`session\x1f${event.session_id}`).digest('hex')}`;
  const objectIDs = canonicalIDs(renderedManifest?.object_ids, 'resume_manifest_invalid');
  const evidenceIDs = canonicalIDs(renderedManifest?.evidence_ids, 'resume_manifest_invalid');
  const baseline = localBaseline(renderedManifest);
  const contextID = `context_${createHash('sha256').update([
    'pulse-continuity-context-v1', resolved.binding.binding_digest,
    resolved.binding.workspace.repository_id, event.host, sessionRef, event.event,
    event.source_event_key.slice('event_'.length), payloadDigest,
  ].join('\x1f')).digest('hex')}`;
  const offer = Object.freeze({
    schema: 'pulse.continuity_delivery.v1',
    context_id: contextID,
    purpose: event.event,
    binding_digest: resolved.binding.binding_digest,
    repository_id: resolved.binding.workspace.repository_id,
    host: event.host,
    session_ref: sessionRef,
    source_event_digest: event.source_event_key.slice('event_'.length),
    payload_digest: payloadDigest,
    object_ids: objectIDs,
    evidence_ids: evidenceIDs,
    rendered_bytes: bytes.length,
    method_id: 'utf8_bytes_div4_ceil',
    method_version: '1',
    pulse_tokens: Math.ceil(bytes.length / 4),
    ...baseline,
    coverage_counted: baseline.coverage_counted ?? 0,
    coverage_total: baseline.coverage_total ?? 0,
  });
  // The native source event is the idempotency authority. Keeping payload
  // measurements out of this key means an exact retry replays one receipt,
  // while a changed payload for that same native event conflicts downstream.
  const idempotencyMaterial = [
    offer.schema, offer.purpose, offer.binding_digest, offer.repository_id,
    offer.host, offer.session_ref, offer.source_event_digest,
  ].join('\x1f');
  return Object.freeze({
    offer,
    idempotencyKey: `continuity-offer:${createHash('sha256')
      .update(idempotencyMaterial).digest('hex')}`,
  });
}

export async function persistContinuityDelivery(output, payload, {
  recordDelivery,
  request,
} = {}) {
  const source = continuityDeliveryAnnotation(output);
  if (!source) return undefined;
  const measurement = createContinuityDeliveryOffer(source.resolved, source.event, payload, source.manifest);
  const persist = recordDelivery ?? (async (offer, resolved, idempotencyKey) => {
    if (typeof request !== 'function') throw new Error('continuity_delivery_request_missing');
    await request(resolved, '/continuity/delivery/offers', {
      body: offer,
      idempotencyKey,
      timeoutMs: 1200,
    });
  });
  await persist(measurement.offer, source.resolved, measurement.idempotencyKey);
  return Object.freeze({ resolved: source.resolved, ...measurement });
}

function localEvidence(binding, result) {
  const value = result?.resume_markdown;
  if (typeof value !== 'string' || value.length > 64 * 1024) throw new Error('local_resume_invalid');
  const source = binding.mode === 'team' ? 'Private Desk' : 'Personal Vault';
  return `${source} continuity (local, private):\n${value}`;
}

export function renderCommonsResume(result, limit) {
  if (!result || typeof result !== 'object' || Array.isArray(result) ||
      result.schema !== 'pulse.team.resume_result.v1' || result.fallback !== false ||
      !Number.isInteger(result.returned_count) || result.returned_count < 0 || result.returned_count > limit ||
      !result.sections || typeof result.sections !== 'object' || Array.isArray(result.sections) ||
      Object.keys(result.sections).sort().join('\0') !== [...RESUME_SECTIONS].sort().join('\0')) {
    throw new Error('commons_resume_invalid');
  }
  if (result.thread_id !== undefined) safeID(result.thread_id, 'commons_resume_invalid');
  const evidence = [];
  for (const section of RESUME_SECTIONS) {
    const entries = result.sections[section];
    if (!Array.isArray(entries)) throw new Error('commons_resume_invalid');
    for (const entry of entries) {
      if (!entry || typeof entry !== 'object' || Array.isArray(entry) ||
          Object.keys(entry).sort().join('\0') !== 'object_id\0text') throw new Error('commons_resume_invalid');
      const objectID = safeID(entry.object_id, 'commons_resume_invalid');
      if (typeof entry.text !== 'string' || entry.text.length < 1 || Array.from(entry.text).length > 1200) {
        throw new Error('commons_resume_invalid');
      }
      evidence.push(`Team Commons [${section}] [${objectID}] (shared, authorized Commons evidence):\n${entry.text}`);
    }
  }
  if (evidence.length !== result.returned_count || evidence.length > limit) throw new Error('commons_resume_invalid');
  return evidence;
}

function commonsManifest(result) {
  const objectIDs = [];
  for (const section of RESUME_SECTIONS) {
    for (const entry of result.sections[section]) objectIDs.push(entry.object_id);
  }
  return manifest(objectIDs, []);
}

export async function composeBoundResumeEvidence(resolved, event, {
  host,
  request,
  teamRequest = callTeamRemoteTool,
  localTokenBudget = 800,
  commonsLimit = 8,
} = {}) {
  if (!resolved?.binding || !resolved?.runtime || !['codex', 'claude-code', 'cursor'].includes(host) ||
      typeof request !== 'function' || !Number.isInteger(localTokenBudget) || localTokenBudget < 256 ||
      localTokenBudget > 2000 || !Number.isInteger(commonsLimit) || commonsLimit < 1 || commonsLimit > 20) {
    throw new Error('compositor_configuration_invalid');
  }
  const workspace = resolved.binding.workspace;
  const sessionID = safeID(event?.session_id, 'compositor_event_invalid');
  const local = await request(resolved, '/continuity/resume', {
    body: {
      thread_id: workspace.repository_id,
      project_id: workspace.workspace_id,
      session_id: sessionID,
      host,
      token_budget: localTokenBudget,
    },
    idempotencyKey: event.idempotency_key,
  });
  const evidence = [localEvidence(resolved.binding, local)];
  const localManifest = manifest(local.included_object_ids, local.included_evidence_ids);
  if (resolved.binding.mode !== 'team') {
    return Object.freeze({
      evidence: Object.freeze(evidence),
      manifest: Object.freeze({ ...localManifest, ...localBaseline(local) }),
      commons: Object.freeze({ status: 'not_applicable' }),
    });
  }
  try {
    const remote = await teamRequest(resolved.binding, 'pulse_team_resume', {
      schema: 'pulse.team.resume.v1',
      active_context: {
        project_id: safeID(resolved.binding.commons?.project_id, 'compositor_binding_invalid'),
        repo_id: safeID(workspace.repository_id, 'compositor_binding_invalid'),
        agent_id: safeID(resolved.binding.principal_ref, 'compositor_binding_invalid'),
        session_id: sessionID,
      },
      thread_id: safeID(workspace.repository_id, 'compositor_binding_invalid'),
      limit: commonsLimit,
    });
    evidence.push(...renderCommonsResume(remote, commonsLimit));
    const remoteManifest = commonsManifest(remote);
    return Object.freeze({
      evidence: Object.freeze(evidence),
      manifest: manifest(
        [...localManifest.object_ids, ...remoteManifest.object_ids],
        localManifest.evidence_ids,
      ),
      commons: Object.freeze({ status: 'active', returned_count: remote.returned_count, fallback: false }),
    });
  } catch (error) {
    const code = error instanceof TeamRemoteClientError ? error.code : 'invalid_or_unavailable';
    evidence.push(`Team Commons unavailable (${code}); local Desk remains active. No fallback store was queried.`);
    return Object.freeze({
      evidence: Object.freeze(evidence),
      manifest: localManifest,
      commons: Object.freeze({ status: 'degraded', reason_code: code, fallback: false }),
    });
  }
}
