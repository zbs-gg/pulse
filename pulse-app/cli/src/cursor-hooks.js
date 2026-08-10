import { createHash } from 'node:crypto';
import { dirname, isAbsolute, join, resolve } from 'node:path';

import {
  annotateContinuityDelivery,
  eventBoundContextLease,
  extractPulseReceiptRefs,
  isDestructivePulseShellInvocation,
  isDestructivePulseTool,
  isPulseRuntimeAuthorityMutation,
  isPulseProductTool,
  isTrustedPulseProductTool,
  isUntrustedPulseMemoryWriteTool,
  normalizeCursorHook,
  renderAdditionalContext,
} from './host-adapter.js';
import {
  activatedBoundPulseRequest,
  boundPulseRequest,
  readHostFinalizeMarker,
  readHostTurnContext,
  resolveBoundCodexRuntime,
  writeHostToolLease,
  writeHostTurnContext,
} from './codex-runtime.js';
import {
  composeBoundResumeEvidence,
  observePendingContinuityDelivery,
  persistContinuityDelivery,
  recordContinuityObservationTicket,
} from './product-compositor.js';
import { defaultPlatformServices } from './platform-services.js';

const HOST = 'cursor';
const MAX_HOOK_INPUT = 1 << 20;
const LIFECYCLE_EVENTS = Object.freeze(['prompt_recall', 'write_receipt']);

function isCursorProductMemoryTool(toolName) {
  return typeof toolName === 'string' && /^mcp__pulse-product__pulse_memory$/i.test(toolName);
}

function lifecycleDirectory(resolved) {
  const dataDir = resolved?.runtime?.data_dir;
  if (typeof dataDir !== 'string' || !isAbsolute(dataDir) || resolve(dataDir) !== dataDir ||
      !/^[a-f0-9]{64}$/.test(resolved?.binding?.binding_digest ?? '')) {
    throw new Error('cursor_lifecycle_context_invalid');
  }
  return join(dataDir, 'runtime', 'cursor-lifecycle');
}

function privateDirectory(path, platformServices) {
  try { platformServices.ensurePrivateDirectory(path); } catch {
    throw new Error('cursor_lifecycle_directory_unsafe');
  }
}

function emptyObserved() {
  return Object.fromEntries(LIFECYCLE_EVENTS.map((event) => [event, false]));
}

function cursorLifecycleResult(record) {
  const ready = LIFECYCLE_EVENTS.every((event) => record.observed[event] === true);
  return {
    ready,
    observed: { ...record.observed },
    reason_code: ready ? 'cursor_lifecycle_verified' : 'cursor_lifecycle_required',
    updated_at: record.updated_at,
  };
}

export function inspectCursorLifecycleReadiness(resolved, { platformServices = defaultPlatformServices } = {}) {
  const directory = lifecycleDirectory(resolved);
  const observed = emptyObserved();
  let updatedAt;
  for (const event of LIFECYCLE_EVENTS) {
    const path = join(directory, `${event}.json`);
    try {
      const bytes = platformServices.readPrivateFile(path, { missing: true, maxBytes: 2048 });
      if (bytes === null) continue;
      const record = JSON.parse(bytes);
      if (record?.schema !== 'pulse.cursor_lifecycle_event.v1' ||
          record.binding_digest !== resolved.binding.binding_digest || record.event !== event ||
          Number.isNaN(Date.parse(record.observed_at))) throw new Error('invalid');
      observed[event] = true;
      if (!updatedAt || Date.parse(record.observed_at) > Date.parse(updatedAt)) updatedAt = record.observed_at;
    } catch {
      // Each immutable event marker is independent; a missing or unsafe marker
      // cannot erase evidence already recorded by another hook process.
    }
  }
  return cursorLifecycleResult({ observed, updated_at: updatedAt });
}

export function recordCursorLifecycleReadiness(
  resolved, event, now = new Date(), { platformServices = defaultPlatformServices } = {},
) {
  if (!LIFECYCLE_EVENTS.includes(event) || !(now instanceof Date) || Number.isNaN(now.valueOf())) {
    throw new Error('cursor_lifecycle_event_invalid');
  }
  const directory = lifecycleDirectory(resolved);
  privateDirectory(resolve(resolved.runtime.data_dir), platformServices);
  privateDirectory(dirname(directory), platformServices);
  privateDirectory(directory, platformServices);
  const current = inspectCursorLifecycleReadiness(resolved, { platformServices });
  if (current.observed[event] === true) return current;
  const record = {
    schema: 'pulse.cursor_lifecycle_event.v1',
    binding_digest: resolved.binding.binding_digest,
    event,
    observed_at: now.toISOString(),
  };
  const path = join(directory, `${event}.json`);
  platformServices.atomicWritePrivateFile(path, `${JSON.stringify(record)}\n`, {
    ensureParent: false, maxBytes: 2048,
  });
  return inspectCursorLifecycleReadiness(resolved, { platformServices });
}

function opaqueTurnCorrelation(kind, value) {
  return `${kind}:${createHash('sha256').update(`${kind}\x1f${value}`).digest('hex')}`;
}

function canonicalTurnEvent(rawInput, resolved) {
  return normalizeCursorHook('stop', {
    ...rawInput,
    cwd: resolved.binding.workspace.canonical_path,
    status: rawInput.status ?? 'completed',
    loop_count: rawInput.loop_count ?? 0,
  });
}

async function resumeContext(resolved, event, request, dependencies = {}) {
  const composed = await (dependencies.composeResume ?? composeBoundResumeEvidence)(resolved, event, {
    host: HOST,
    request,
    localTokenBudget: Math.min(2000, Math.max(
      256, Number.parseInt(process.env.PULSE_RESUME_TOKENS ?? '1200', 10) || 1200,
    )),
  });
  return Object.freeze({
    additionalContext: renderAdditionalContext(
      composed.evidence,
      eventBoundContextLease(resolved.binding, event),
    ),
    manifest: composed.manifest ?? Object.freeze({ object_ids: Object.freeze([]), evidence_ids: Object.freeze([]) }),
  });
}

function noChangeBody(resolved, event) {
  return {
    schema: 'pulse.turn_no_change.v1',
    host: HOST,
    session_id: event.session_id,
    turn_id: event.turn_id,
    source_event_key: event.source_event_key,
    idempotency_key: event.idempotency_key,
    binding_digest: resolved.binding.binding_digest,
    policy_epoch: 0,
    resolver_epoch: resolved.binding.resolver_epoch,
  };
}

function denied(reason) {
  return { permission: 'deny', user_message: reason };
}

function receiptMatchesEvent(receipt, ref, marker, event) {
  return receipt?.schema === 'pulse.write_receipt.v1' &&
    receipt.receipt_id === ref.receipt_id && receipt.ledger_id === ref.ledger_id &&
    receipt.candidate_id === ref.candidate_id && receipt.status === ref.status &&
    receipt.ledger_id === marker.ledger_id && receipt.safe_provenance?.host === HOST &&
    receipt.safe_provenance.session_id === opaqueTurnCorrelation('session', event.session_id) &&
    receipt.safe_provenance.turn_id === opaqueTurnCorrelation('turn', event.turn_id) &&
    receipt.safe_provenance.source_event_key === opaqueTurnCorrelation('event', event.source_event_key);
}

export async function handleCursorHook(eventName, rawInput, dependencies = {}) {
  const now = dependencies.now?.() ?? new Date();
  const resolveRuntime = dependencies.resolveRuntime ??
    ((input) => resolveBoundCodexRuntime(input, { host: HOST }));
  const request = dependencies.request ?? activatedBoundPulseRequest;
  const recordLifecycle = (event) => {
    try {
      (dependencies.recordLifecycle ?? recordCursorLifecycleReadiness)(resolved, event, now);
    } catch { /* readiness evidence is fail-closed and must not break the host lifecycle */ }
  };

  if (eventName === 'preToolUse' && isUntrustedPulseMemoryWriteTool(rawInput.tool_name)) {
    return denied('Pulse Personal memory writes require the pulse-product server.');
  }
  if (eventName === 'preToolUse' && !isCursorProductMemoryTool(rawInput.tool_name)) {
    return { permission: 'allow' };
  }

  let resolved;
  try {
    resolved = resolveRuntime(rawInput);
    const nativeEvent = normalizeCursorHook(eventName, rawInput);
    const event = eventName === 'sessionStart'
      ? nativeEvent
      : canonicalTurnEvent(rawInput, resolved);

    if (eventName === 'sessionStart') {
      return {};
    }
    if (eventName === 'beforeSubmitPrompt') {
      (dependencies.writeTurnContext ?? writeHostTurnContext)(resolved, event, HOST, now);
      return { continue: true };
    }
    if (eventName === 'preToolUse') {
      (dependencies.readTurnContext ?? readHostTurnContext)(resolved, event, HOST, now);
      (dependencies.writeToolLease ?? writeHostToolLease)(
        resolved, event, HOST, rawInput.tool_name, rawInput.tool_input, rawInput.tool_use_id, now,
      );
      return { permission: 'allow' };
    }
    if (eventName === 'postToolUse') {
      if (!isCursorProductMemoryTool(rawInput.tool_name)) return {};
      if (rawInput.tool_input && typeof rawInput.tool_input === 'object' &&
          !Array.isArray(rawInput.tool_input) && Object.hasOwn(rawInput.tool_input, 'query')) {
        if (rawInput.tool_output?.isError !== true) recordLifecycle('prompt_recall');
        return {};
      }
      (dependencies.readFinalizeMarker ?? readHostFinalizeMarker)(resolved, event, HOST);
      recordLifecycle('write_receipt');
      return {};
    }
    if (eventName === 'preCompact') return {};
    if (eventName === 'stop') return {};
    throw new Error('unsupported_cursor_hook');
  } catch {
    if (eventName === 'preToolUse') {
      return { permission: 'allow' };
    }
    if (eventName === 'sessionStart') return {};
    if (eventName === 'beforeSubmitPrompt') return { continue: true };
    return {};
  }
}

async function readHookInput(stream = process.stdin) {
  const chunks = [];
  let size = 0;
  for await (const chunk of stream) {
    size += chunk.length;
    if (size > MAX_HOOK_INPUT) throw new Error('cursor_hook_input_too_large');
    chunks.push(chunk);
  }
  if (chunks.length === 0) throw new Error('cursor_hook_input_empty');
  return JSON.parse(Buffer.concat(chunks).toString('utf8'));
}

function writeCursorOutput(serialized, stream = process.stdout) {
  return new Promise((resolveWrite, rejectWrite) => {
    stream.write(serialized, (error) => {
      if (error) rejectWrite(error); else resolveWrite();
    });
  });
}

export async function flushCursorHookOutput(input, output, dependencies = {}) {
  const delivery = await persistContinuityDelivery(output, output?.additional_context, {
    recordDelivery: dependencies.recordDelivery,
    request: dependencies.deliveryRequest ?? activatedBoundPulseRequest,
  });
  await (dependencies.writeOutput ?? writeCursorOutput)(`${JSON.stringify(output)}\n`);
  if (delivery?.receipt && delivery.offer.purpose === 'session_start') {
    try {
      await (dependencies.recordObservationTicket ?? recordContinuityObservationTicket)(delivery, {
        platformServices: dependencies.platformServices,
      });
    } catch { /* a missing ticket keeps readiness pending without breaking the host */ }
  }
}

export async function runCursorHookCLI(eventName, dependencies = {}) {
  const input = dependencies.input ?? await readHookInput(dependencies.inputStream);
  const output = await (dependencies.handleHook ?? handleCursorHook)(eventName, input, dependencies);
  await flushCursorHookOutput(input, output, dependencies);
}
