import { createHash } from 'node:crypto';
import { isAbsolute, join, resolve } from 'node:path';

import { continuityDeliveryAnnotation, isStableHostID } from './host-adapter.js';
import { defaultPlatformServices } from './platform-services.js';
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

function isTransientLocalDeliveryError(error) {
  const status = error?.status;
  const code = error?.code ?? error?.cause?.code;
  return error?.name === 'TimeoutError' || error?.name === 'AbortError' ||
    ['ECONNREFUSED', 'ECONNRESET', 'ETIMEDOUT', 'UND_ERR_CONNECT_TIMEOUT'].includes(code) ||
    status === 408 || status === 425 || status === 429 || status >= 500;
}

function canonicalIDs(value, code) {
  if (value === undefined) return Object.freeze([]);
  if (!Array.isArray(value)) throw new Error(code);
  return Object.freeze([...new Set(value.map((item) => safeID(item, code)))].sort());
}

function manifest(objectIDs = [], evidenceIDs = [], memorySnapshotDigest) {
  if (memorySnapshotDigest !== undefined && !/^[a-f0-9]{64}$/.test(memorySnapshotDigest)) {
    throw new Error('resume_manifest_invalid');
  }
  return Object.freeze({
    object_ids: canonicalIDs(objectIDs, 'resume_manifest_invalid'),
    evidence_ids: canonicalIDs(evidenceIDs, 'resume_manifest_invalid'),
    ...(memorySnapshotDigest === undefined ? {} : { memory_snapshot_digest: memorySnapshotDigest }),
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
  const memorySnapshotDigest = renderedManifest?.memory_snapshot_digest;
  if (memorySnapshotDigest !== undefined && !/^[a-f0-9]{64}$/.test(memorySnapshotDigest)) {
    throw new Error('resume_manifest_invalid');
  }
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
    ...(memorySnapshotDigest === undefined ? {} : { memory_snapshot_digest: memorySnapshotDigest }),
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
    return request(resolved, '/continuity/delivery/offers', {
      body: offer,
      idempotencyKey,
      timeoutMs: 2500,
    });
  });
  let receipt;
  try {
    receipt = await persist(measurement.offer, source.resolved, measurement.idempotencyKey);
  } catch (error) {
    if (!isTransientLocalDeliveryError(error)) throw error;
    // The native source event supplies an exact idempotency key. Retrying the
    // same offer can recover a slow local daemon without creating a second
    // delivery fact when the first request committed before its timeout.
    receipt = await persist(measurement.offer, source.resolved, measurement.idempotencyKey);
  }
  return Object.freeze({ resolved: source.resolved, receipt, ...measurement });
}

function continuitySessionRef(sessionID) {
  if (!isStableHostID(sessionID)) throw new Error('continuity_observation_event_invalid');
  return `session:${createHash('sha256').update(`session\x1f${sessionID}`).digest('hex')}`;
}

function continuityObservationDirectory(resolved) {
  const dataDir = resolved?.runtime?.data_dir;
  if (typeof dataDir !== 'string' || !isAbsolute(dataDir) || resolve(dataDir) !== dataDir ||
      !/^[a-f0-9]{64}$/.test(resolved?.binding?.binding_digest ?? '') ||
      !isStableHostID(resolved?.binding?.workspace?.repository_id)) {
    throw new Error('continuity_observation_runtime_invalid');
  }
  return join(dataDir, 'runtime', 'continuity-observations');
}

function continuityObservedDirectory(resolved) {
  return join(resolve(resolved.runtime.data_dir), 'runtime', 'continuity-observed');
}

function continuityObservationTicketPath(resolved, host, sessionRef) {
  if (!['codex', 'claude-code', 'cursor'].includes(host) ||
      !/^session:[a-f0-9]{64}$/.test(sessionRef ?? '')) {
    throw new Error('continuity_observation_identity_invalid');
  }
  return join(continuityObservationDirectory(resolved), `${host}-${sessionRef.slice('session:'.length)}.json`);
}

function continuityObservedMarkerPath(resolved, host, sessionRef) {
  if (!['codex', 'claude-code', 'cursor'].includes(host) ||
      !/^session:[a-f0-9]{64}$/.test(sessionRef ?? '')) {
    throw new Error('continuity_observation_identity_invalid');
  }
  return join(continuityObservedDirectory(resolved), `${host}-${sessionRef.slice('session:'.length)}.json`);
}

function observationTicket(delivery) {
  const { resolved, offer, receipt } = delivery ?? {};
  if (offer?.purpose !== 'session_start' || receipt?.schema !== 'pulse.continuity_delivery_receipt.v1' ||
      receipt.state !== 'offered_to_host' || receipt.context_id !== offer.context_id ||
      receipt.purpose !== offer.purpose || receipt.binding_digest !== offer.binding_digest ||
      receipt.repository_id !== offer.repository_id || receipt.host !== offer.host ||
      receipt.session_ref !== offer.session_ref || receipt.payload_digest !== offer.payload_digest ||
      receipt.source_event_digest !== offer.source_event_digest || !isStableHostID(receipt.receipt_id) ||
      typeof receipt.created_at !== 'string' || Number.isNaN(Date.parse(receipt.created_at)) ||
      receipt.binding_digest !== resolved?.binding?.binding_digest ||
      receipt.repository_id !== resolved?.binding?.workspace?.repository_id) {
    throw new Error('continuity_observation_offer_receipt_invalid');
  }
  const memorySnapshotDigest = delivery?.memory_snapshot_digest;
  if (memorySnapshotDigest !== undefined && !/^[a-f0-9]{64}$/.test(memorySnapshotDigest)) {
    throw new Error('continuity_observation_offer_receipt_invalid');
  }
  return Object.freeze({
    schema: memorySnapshotDigest === undefined
      ? 'pulse.continuity_observation_ticket.v1'
      : 'pulse.continuity_observation_ticket.v2',
    offer_receipt_id: receipt.receipt_id,
    context_id: receipt.context_id,
    binding_digest: receipt.binding_digest,
    repository_id: receipt.repository_id,
    host: receipt.host,
    session_ref: receipt.session_ref,
    offer_source_event_digest: receipt.source_event_digest,
    ...(memorySnapshotDigest === undefined ? {} : { memory_snapshot_digest: memorySnapshotDigest }),
    expires_at: new Date(Date.parse(receipt.created_at) + 7 * 24 * 60 * 60 * 1000).toISOString(),
  });
}

export function recordContinuityObservationTicket(delivery, {
  platformServices = defaultPlatformServices,
} = {}) {
  const ticket = observationTicket(delivery);
  const directory = continuityObservationDirectory(delivery.resolved);
  platformServices.ensurePrivateDirectory(resolve(delivery.resolved.runtime.data_dir));
  platformServices.ensurePrivateDirectory(join(resolve(delivery.resolved.runtime.data_dir), 'runtime'));
  platformServices.ensurePrivateDirectory(directory);
  platformServices.atomicWritePrivateFile(
    continuityObservationTicketPath(delivery.resolved, ticket.host, ticket.session_ref),
    `${JSON.stringify(ticket)}\n`,
    { ensureParent: false, maxBytes: 4096 },
  );
  return ticket;
}

function readObservationTicket(resolved, event, platformServices) {
  const sessionRef = continuitySessionRef(event?.session_id);
  const path = continuityObservationTicketPath(resolved, event?.host, sessionRef);
  const bytes = platformServices.readPrivateFile(path, { missing: true, minBytes: 1, maxBytes: 4096 });
  if (bytes === null) return undefined;
  let ticket;
  try { ticket = JSON.parse(bytes); } catch { throw new Error('continuity_observation_ticket_invalid'); }
  const ticketFieldsV1 = [
    'binding_digest', 'context_id', 'expires_at', 'host', 'offer_receipt_id',
    'offer_source_event_digest', 'repository_id', 'schema', 'session_ref',
  ];
  const ticketFieldsV2 = [...ticketFieldsV1, 'memory_snapshot_digest'].sort();
  const expectedFields = ticket?.schema === 'pulse.continuity_observation_ticket.v2'
    ? ticketFieldsV2
    : ticketFieldsV1;
  if (!ticket || typeof ticket !== 'object' || Array.isArray(ticket) ||
      Object.keys(ticket).sort().join('\0') !== [...expectedFields].sort().join('\0') ||
      !['pulse.continuity_observation_ticket.v1', 'pulse.continuity_observation_ticket.v2'].includes(ticket.schema) ||
      !isStableHostID(ticket.offer_receipt_id) || !isStableHostID(ticket.context_id) ||
      ticket.binding_digest !== resolved.binding.binding_digest ||
      ticket.repository_id !== resolved.binding.workspace.repository_id ||
      ticket.host !== event.host || ticket.session_ref !== sessionRef ||
      !/^[a-f0-9]{64}$/.test(ticket.offer_source_event_digest ?? '') ||
      (ticket.schema === 'pulse.continuity_observation_ticket.v2' &&
        !/^[a-f0-9]{64}$/.test(ticket.memory_snapshot_digest ?? '')) ||
      typeof ticket.expires_at !== 'string' || Number.isNaN(Date.parse(ticket.expires_at)) ||
      Date.parse(ticket.expires_at) <= Date.now()) {
    throw new Error('continuity_observation_ticket_invalid');
  }
  return { path, ticket };
}

function observationMarker(ticket, receipt) {
  if (receipt?.schema !== 'pulse.continuity_delivery_receipt.v1' ||
      receipt.state !== 'host_observed' || !isStableHostID(receipt.receipt_id) ||
      receipt.context_id !== ticket.context_id || receipt.parent_receipt_id !== ticket.offer_receipt_id ||
      receipt.binding_digest !== ticket.binding_digest || receipt.repository_id !== ticket.repository_id ||
      receipt.host !== ticket.host || receipt.session_ref !== ticket.session_ref ||
      typeof receipt.created_at !== 'string' || Number.isNaN(Date.parse(receipt.created_at))) {
    throw new Error('continuity_observation_receipt_invalid');
  }
  return Object.freeze({
    schema: ticket.schema === 'pulse.continuity_observation_ticket.v2'
      ? 'pulse.continuity_observed_marker.v2'
      : 'pulse.continuity_observed_marker.v1',
    offer_receipt_id: ticket.offer_receipt_id,
    observation_receipt_id: receipt.receipt_id,
    context_id: ticket.context_id,
    binding_digest: ticket.binding_digest,
    repository_id: ticket.repository_id,
    host: ticket.host,
    session_ref: ticket.session_ref,
    ...(ticket.memory_snapshot_digest === undefined
      ? {}
      : { memory_snapshot_digest: ticket.memory_snapshot_digest }),
    observed_at: receipt.created_at,
  });
}

function readObservedMarker(resolved, event, platformServices) {
  const sessionRef = continuitySessionRef(event?.session_id);
  const path = continuityObservedMarkerPath(resolved, event?.host, sessionRef);
  const bytes = platformServices.readPrivateFile(path, { missing: true, minBytes: 1, maxBytes: 4096 });
  if (bytes === null) return undefined;
  let marker;
  try { marker = JSON.parse(bytes); } catch { throw new Error('continuity_observed_marker_invalid'); }
  const fieldsV1 = [
    'binding_digest', 'context_id', 'host', 'observation_receipt_id', 'observed_at',
    'offer_receipt_id', 'repository_id', 'schema', 'session_ref',
  ];
  const fieldsV2 = [...fieldsV1, 'memory_snapshot_digest'].sort();
  const expectedFields = marker?.schema === 'pulse.continuity_observed_marker.v2'
    ? fieldsV2
    : fieldsV1;
  if (!marker || typeof marker !== 'object' || Array.isArray(marker) ||
      Object.keys(marker).sort().join('\0') !== [...expectedFields].sort().join('\0') ||
      !['pulse.continuity_observed_marker.v1', 'pulse.continuity_observed_marker.v2'].includes(marker.schema) ||
      !isStableHostID(marker.offer_receipt_id) || !isStableHostID(marker.observation_receipt_id) ||
      !isStableHostID(marker.context_id) ||
      marker.binding_digest !== resolved.binding.binding_digest ||
      marker.repository_id !== resolved.binding.workspace.repository_id ||
      marker.host !== event.host || marker.session_ref !== sessionRef ||
      (marker.schema === 'pulse.continuity_observed_marker.v2' &&
        !/^[a-f0-9]{64}$/.test(marker.memory_snapshot_digest ?? '')) ||
      typeof marker.observed_at !== 'string' || Number.isNaN(Date.parse(marker.observed_at))) {
    throw new Error('continuity_observed_marker_invalid');
  }
  return marker;
}

export function hasContinuitySessionDelivery(resolved, event, {
  platformServices = defaultPlatformServices,
  expectedMemorySnapshotDigest,
} = {}) {
  if (expectedMemorySnapshotDigest !== undefined && !/^[a-f0-9]{64}$/.test(expectedMemorySnapshotDigest)) {
    throw new Error('continuity_observed_marker_invalid');
  }
  const marker = readObservedMarker(resolved, event, platformServices);
  if (!marker) return false;
  return expectedMemorySnapshotDigest === undefined ||
    (marker.schema === 'pulse.continuity_observed_marker.v2' &&
      marker.memory_snapshot_digest === expectedMemorySnapshotDigest);
}

export function createContinuityDeliveryObservation(ticket, event) {
  const promptObservation = event?.event === 'turn_start' &&
    event?.native_event === 'UserPromptSubmit' && event?.source === 'prompt_submitted';
  const stopObservation = event?.event === 'turn_finalize' &&
    event?.native_event === 'Stop' && event?.source === 'stop';
  if ((!promptObservation && !stopObservation) ||
      !/^event_[a-f0-9]{64}$/.test(event?.source_event_key ?? '') ||
      event.host !== ticket?.host || continuitySessionRef(event.session_id) !== ticket?.session_ref) {
    throw new Error('continuity_observation_event_invalid');
  }
  const sourceEventDigest = event.source_event_key.slice('event_'.length);
  if (sourceEventDigest === ticket.offer_source_event_digest) throw new Error('continuity_observation_event_replayed');
  const body = Object.freeze({
    schema: 'pulse.continuity_delivery_observation.v1',
    context_id: ticket.context_id,
    binding_digest: ticket.binding_digest,
    repository_id: ticket.repository_id,
    host: ticket.host,
    session_ref: ticket.session_ref,
    source_event_digest: sourceEventDigest,
  });
  return Object.freeze({
    body,
    idempotencyKey: `continuity-observation:${createHash('sha256').update([
      body.schema, body.context_id, body.binding_digest, body.repository_id,
      body.host, body.session_ref, body.source_event_digest,
    ].join('\x1f')).digest('hex')}`,
  });
}

export async function observePendingContinuityDelivery(resolved, event, {
  request,
  platformServices = defaultPlatformServices,
} = {}) {
  if (typeof request !== 'function') throw new Error('continuity_observation_request_missing');
  const sessionRef = continuitySessionRef(event?.session_id);
  const ticketPath = continuityObservationTicketPath(resolved, event?.host, sessionRef);
  const release = platformServices.acquirePrivateLock(`${ticketPath}.lock`, {
    staleAfterMs: 15_000, timeoutMs: 250,
  });
  try {
    const pending = readObservationTicket(resolved, event, platformServices);
    if (!pending) return undefined;
    const observation = createContinuityDeliveryObservation(pending.ticket, event);
    const observe = () => request(resolved, '/continuity/delivery/observations', {
      body: observation.body, idempotencyKey: observation.idempotencyKey, timeoutMs: 5000,
    });
    let receipt;
    try {
      receipt = await observe();
    } catch (error) {
      if (!isTransientLocalDeliveryError(error)) throw error;
      // The observation endpoint is idempotent. If a slow local daemon commits
      // just after the client deadline, one exact replay returns that receipt;
      // otherwise it gets one bounded second chance without duplicating state.
      receipt = await observe();
    }
    if (receipt?.schema !== 'pulse.continuity_delivery_receipt.v1' || receipt.state !== 'host_observed' ||
        receipt.context_id !== pending.ticket.context_id || receipt.parent_receipt_id !== pending.ticket.offer_receipt_id ||
        receipt.binding_digest !== pending.ticket.binding_digest || receipt.repository_id !== pending.ticket.repository_id ||
        receipt.host !== pending.ticket.host || receipt.session_ref !== pending.ticket.session_ref) {
      throw new Error('continuity_observation_receipt_invalid');
    }
    const marker = observationMarker(pending.ticket, receipt);
    const markerDirectory = continuityObservedDirectory(resolved);
    platformServices.ensurePrivateDirectory(markerDirectory);
    platformServices.atomicWritePrivateFile(
      continuityObservedMarkerPath(resolved, marker.host, marker.session_ref),
      `${JSON.stringify(marker)}\n`,
      { ensureParent: false, maxBytes: 4096 },
    );
    platformServices.removePrivateFile(pending.path, { missing: false });
    return receipt;
  } finally {
    release();
  }
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
  const localManifest = manifest(
    local.included_object_ids,
    local.included_evidence_ids,
    local.memory_snapshot_digest,
  );
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
        localManifest.memory_snapshot_digest,
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
