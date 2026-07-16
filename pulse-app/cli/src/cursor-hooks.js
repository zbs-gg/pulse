import { createHash } from 'node:crypto';

import {
  annotateContinuityDelivery,
  eventBoundContextLease,
  extractPulseReceiptRefs,
  isDestructivePulseShellInvocation,
  isDestructivePulseTool,
  isGuardedCodexTool,
  isTrustedPulseProductTool,
  normalizeCursorHook,
  renderAdditionalContext,
} from './host-adapter.js';
import {
  activatedBoundPulseRequest,
  readHostFinalizeMarker,
  readHostTurnContext,
  resolveBoundCodexRuntime,
  writeHostToolLease,
  writeHostTurnContext,
} from './codex-runtime.js';
import { composeBoundResumeEvidence, persistContinuityDelivery } from './product-compositor.js';

const HOST = 'cursor';
const MAX_HOOK_INPUT = 1 << 20;

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
    teamRequest: dependencies.teamRequest,
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

  if (eventName === 'preToolUse' &&
      (isDestructivePulseTool(rawInput.tool_name) ||
       isDestructivePulseShellInvocation(rawInput.tool_name, rawInput.tool_input))) {
    return denied('Pulse deletion is user-controlled and is never agent-callable.');
  }

  let resolved;
  try {
    resolved = resolveRuntime(rawInput);
    const nativeEvent = normalizeCursorHook(eventName, rawInput);
    const event = eventName === 'sessionStart'
      ? nativeEvent
      : canonicalTurnEvent(rawInput, resolved);

    if (eventName === 'sessionStart') {
      const context = await resumeContext(resolved, event, request, dependencies);
      return annotateContinuityDelivery({
        additional_context: context.additionalContext,
      }, resolved, event, context.manifest);
    }
    if (eventName === 'beforeSubmitPrompt') {
      (dependencies.writeTurnContext ?? writeHostTurnContext)(resolved, event, HOST, now);
      return { continue: true };
    }
    if (eventName === 'preToolUse') {
      if (!isGuardedCodexTool(rawInput.tool_name)) return { permission: 'allow' };
      (dependencies.readTurnContext ?? readHostTurnContext)(resolved, event, HOST, now);
      await request(resolved, '/memory/status', { method: 'GET', timeoutMs: 1200 });
      if (/^mcp__(?:pulse-product|pulse)__pulse_remember$/i.test(rawInput.tool_name ?? '')) {
        (dependencies.writeToolLease ?? writeHostToolLease)(
          resolved, event, HOST, rawInput.tool_name, rawInput.tool_input, rawInput.tool_use_id, now,
        );
      }
      return { permission: 'allow' };
    }
    if (eventName === 'postToolUse') {
      if (!isTrustedPulseProductTool(rawInput.tool_name)) return {};
      const refs = extractPulseReceiptRefs(rawInput.tool_output);
      if (refs.length === 0) return {};
      const marker = (dependencies.readFinalizeMarker ?? readHostFinalizeMarker)(resolved, event, HOST);
      const corroborated = [];
      for (const ref of refs) {
        const receipt = await request(resolved, `/memory/receipts/${encodeURIComponent(ref.receipt_id)}`, {
          method: 'GET', timeoutMs: 1200,
        });
        if (receiptMatchesEvent(receipt, ref, marker, event)) corroborated.push(ref);
      }
      return corroborated.length === 0 ? {} : {
        additional_context: `Pulse Memory Tray receipt: ${corroborated.map((ref) => `${ref.receipt_id}:${ref.status}`).join(', ')}`,
      };
    }
    if (eventName === 'preCompact') return {};
    if (eventName === 'stop') {
      if (rawInput.status !== undefined && rawInput.status !== 'completed') return {};
      try {
        (dependencies.readFinalizeMarker ?? readHostFinalizeMarker)(resolved, event, HOST);
        return {};
      } catch {
        // A missing marker triggers one bounded host-owned finalization pass.
      }
      if ((rawInput.loop_count ?? 0) === 0) {
        return {
          followup_message: 'Perform one bounded Pulse finalization pass for this turn. Propose only durable decisions, corrections, open loops, preferences, or project-state changes through pulse_remember in one batch. Never send raw prompts, transcripts, secrets, credentials, or local paths. If there is nothing durable, finish without calling a memory tool.',
        };
      }
      try {
        await request(resolved, '/turn/no-change', {
          body: noChangeBody(resolved, event),
          idempotencyKey: event.idempotency_key,
        });
      } catch (error) {
        if (!(error?.status === 409 && /turn already finalized with a different result/.test(error.message))) throw error;
      }
      return {};
    }
    throw new Error('unsupported_cursor_hook');
  } catch {
    if (eventName === 'preToolUse') {
      return denied('pulse_authority_unavailable: restart the task after Pulse binding is restored');
    }
    if (eventName === 'sessionStart') {
      return { additional_context: 'Pulse sessionStart degraded: bound memory context is unavailable.' };
    }
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
  await persistContinuityDelivery(output, output?.additional_context, {
    recordDelivery: dependencies.recordDelivery,
    request: dependencies.deliveryRequest ?? activatedBoundPulseRequest,
  });
  await (dependencies.writeOutput ?? writeCursorOutput)(`${JSON.stringify(output)}\n`);
}

export async function runCursorHookCLI(eventName, dependencies = {}) {
  const input = dependencies.input ?? await readHookInput(dependencies.inputStream);
  const output = await (dependencies.handleHook ?? handleCursorHook)(eventName, input, dependencies);
  await flushCursorHookOutput(input, output, dependencies);
}
