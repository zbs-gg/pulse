import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  annotateContinuityDelivery,
  contextLease,
  eventBoundContextLease,
  extractPulseReceiptRefs,
  isDestructivePulseShellInvocation,
  isDestructivePulseTool,
  isPulseRuntimeAuthorityMutation,
  isPulseProductTool,
  isUntrustedPulseMemoryWriteTool,
  normalizeClaudeHook,
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
import {
  composeBoundResumeEvidence,
  observePendingContinuityDelivery,
  persistContinuityDelivery,
  recordContinuityObservationTicket,
} from './product-compositor.js';

const MAX_HOOK_INPUT = 1 << 20;
const HOST = 'claude-code';

function isClaudeProductRememberTool(toolName) {
  return typeof toolName === 'string' && /^mcp__pulse-product__pulse_remember$/i.test(toolName);
}

export function claudeHookContractDigest(runtimeDigest) {
	if (!/^[a-f0-9]{64}$/.test(runtimeDigest ?? '')) return undefined;
	return createHash('sha256')
		.update('pulse-claude-code-hook-contract-v1\x1f')
		.update(runtimeDigest)
		.update('\x1f')
		.update([
			'SessionStart', 'UserPromptSubmit', 'PreToolUse', 'PostToolUse', 'PreCompact',
			'PostCompact', 'SubagentStart', 'SubagentStop', 'Stop',
		].join(','))
		.digest('hex');
}

function installedClaudeHookContractDigest() {
	try {
		const manifest = JSON.parse(readFileSync(join(
			dirname(fileURLToPath(import.meta.url)), '..', 'runtime-manifest.json',
		), 'utf8'));
		if (manifest?.schema !== 'pulse.codex_runtime.v2') return undefined;
		return claudeHookContractDigest(manifest.tree_digest);
	} catch {
		return undefined;
	}
}

function hookFailureReceipt(event, reasonCode, now) {
  const digest = createHash('sha256')
    .update('pulse-claude-code-hook-failure-v1\x1f')
    .update(event.source_event_key)
    .update('\x1f')
    .update(reasonCode)
    .digest('hex');
  return {
    schema: 'pulse.hook_failure_receipt.v1',
    receipt_id: `hook_${digest}`,
    host: HOST,
    session_id: event.session_id,
    turn_id: event.turn_id,
    source_event_key: event.source_event_key,
    status: 'failed',
    reason_code: reasonCode,
    created_at: now.toISOString(),
  };
}

function recordHookFailure(resolved, receipt) {
  const directory = resolved?.runtime?.data_dir
    ? join(resolved.runtime.data_dir, 'hook-receipts')
    : undefined;
  if (!directory) return;
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  const path = join(directory, `${receipt.receipt_id}.json`);
  if (existsSync(path)) return;
  const temporary = `${path}.new`;
  try {
    writeFileSync(temporary, `${JSON.stringify(receipt)}\n`, { mode: 0o600, flag: 'wx' });
    renameSync(temporary, path);
  } finally {
    rmSync(temporary, { force: true });
  }
}

function threadContext(resolved, event) {
  return {
    thread_id: resolved.binding.workspace.repository_id,
    project_id: resolved.binding.workspace.workspace_id,
    session_id: event.session_id,
    host: HOST,
  };
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

function preToolDenied(reason) {
  return {
    hookSpecificOutput: {
      hookEventName: 'PreToolUse',
      permissionDecision: 'deny',
      permissionDecisionReason: reason,
    },
  };
}

function opaqueTurnCorrelation(kind, value) {
  return `${kind}:${createHash('sha256').update(`${kind}\x1f${value}`).digest('hex')}`;
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

function canonicalTurnEvent(rawInput, resolved) {
  return normalizeClaudeHook('Stop', {
    ...rawInput,
    agent_id: undefined,
    cwd: resolved.binding.workspace.canonical_path,
    hook_event_name: 'Stop',
    stop_hook_active: rawInput.stop_hook_active ?? false,
  });
}

export async function handleClaudeHook(eventName, rawInput, dependencies = {}) {
  const now = dependencies.now?.() ?? new Date();
  const resolveRuntime = dependencies.resolveRuntime ??
    ((input) => resolveBoundCodexRuntime(input, { host: HOST }));
	const request = dependencies.request ?? activatedBoundPulseRequest;
  const recordFailure = dependencies.recordFailure ?? recordHookFailure;

  if (eventName === 'PreToolUse' &&
      (isDestructivePulseTool(rawInput.tool_name) ||
       isDestructivePulseShellInvocation(rawInput.tool_name, rawInput.tool_input) ||
       isPulseRuntimeAuthorityMutation(rawInput.tool_name, rawInput.tool_input))) {
    return preToolDenied('Pulse deletion is user-controlled. Product vault wipe requires the privileged OS-backed Pulse surface and is never agent-callable.');
  }
  if (eventName === 'PreToolUse' && isUntrustedPulseMemoryWriteTool(rawInput.tool_name)) {
    return preToolDenied('Pulse Personal memory writes require the pulse-product server. Legacy or lookalike Pulse servers cannot create Personal memory.');
  }
  if (eventName === 'PreToolUse' && !isPulseProductTool(rawInput.tool_name)) return {};

  let resolved;
  try {
    resolved = resolveRuntime(rawInput);
    const nativeEvent = normalizeClaudeHook(eventName, rawInput);
    const event = eventName === 'SessionStart'
      ? nativeEvent
      : canonicalTurnEvent(rawInput, resolved);

    if (eventName === 'SessionStart') {
      const context = await resumeContext(resolved, event, request, dependencies);
      return annotateContinuityDelivery({
        continue: true,
        hookSpecificOutput: {
          hookEventName: 'SessionStart',
          additionalContext: context.additionalContext,
        },
      }, resolved, event, context.manifest);
    }
    if (eventName === 'UserPromptSubmit') {
      try {
        await (dependencies.observeDelivery ?? observePendingContinuityDelivery)(resolved, nativeEvent, {
          request: dependencies.deliveryRequest ?? request,
          platformServices: dependencies.platformServices,
        });
      } catch { /* observation evidence is fail-closed and never blocks the user's prompt */ }
      (dependencies.writeTurnContext ?? writeHostTurnContext)(resolved, event, HOST, now);
      return {
        continue: true,
        hookSpecificOutput: {
          hookEventName: 'UserPromptSubmit',
          additionalContext: renderAdditionalContext([], contextLease(resolved.binding, now)),
        },
      };
    }
    if (eventName === 'PreToolUse') {
      (dependencies.readTurnContext ?? readHostTurnContext)(resolved, event, HOST, now);
      await request(resolved, '/memory/status', { method: 'GET', timeoutMs: 1200 });
      if (isClaudeProductRememberTool(rawInput.tool_name)) {
        (dependencies.writeToolLease ?? writeHostToolLease)(
          resolved, event, HOST, rawInput.tool_name, rawInput.tool_input, rawInput.tool_use_id, now,
        );
      }
      return {};
    }
    if (eventName === 'PostToolUse') {
      if (!isClaudeProductRememberTool(rawInput.tool_name)) return {};
      const refs = extractPulseReceiptRefs(rawInput.tool_response);
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
        systemMessage: `Pulse Memory Tray receipt: ${corroborated.map((ref) => `${ref.receipt_id}:${ref.status}`).join(', ')}`,
      };
    }
    if (eventName === 'PreCompact') {
      return { systemMessage: 'Pulse kept the current turn open across compaction.' };
    }
    if (eventName === 'PostCompact') {
      return { systemMessage: 'Pulse binding will be reloaded on the compacted session start.' };
    }
    if (eventName === 'SubagentStart') {
      const context = await resumeContext(resolved, nativeEvent, request, dependencies);
      return annotateContinuityDelivery({
        hookSpecificOutput: {
          hookEventName: 'SubagentStart',
          additionalContext: context.additionalContext,
        },
		systemMessage: 'Pulse subagent boundary: return typed durable-memory candidates to the parent; the parent finalizes the turn once. Role-scoped retrieval is not active.',
      }, resolved, nativeEvent, context.manifest);
    }
    if (eventName === 'SubagentStop') return {};
    if (eventName === 'Stop') {
      if (Array.isArray(rawInput.background_tasks) && rawInput.background_tasks.length > 0) {
        return {
          decision: 'block',
          reason: `Pulse deferred turn finalization while ${rawInput.background_tasks.length} background task(s) remain active.`,
        };
      }
      try {
        (dependencies.readFinalizeMarker ?? readHostFinalizeMarker)(resolved, event, HOST);
        return {};
      } catch {
        // First Stop requests one bounded model pass; recursive Stop seals no-change.
      }
      if (!event.stop_hook_active) {
        await request(resolved, '/memory/status', { method: 'GET', timeoutMs: 1200 });
        return {
          decision: 'block',
          reason: 'Perform one bounded Pulse finalization pass for this turn. Propose only durable decisions, corrections, open loops, preferences, or project-state changes through pulse_remember in one batch. Never send raw prompts, transcripts, secrets, credentials, or local paths. If there is nothing durable, stop again without calling a memory tool.',
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
    throw new Error('unsupported_claude_code_hook');
  } catch (error) {
    if (eventName === 'PreToolUse') {
      return preToolDenied('pulse_authority_unavailable: restart the task after Pulse binding is restored');
    }
    if (eventName === 'Stop') {
      let event;
      try {
        event = resolved
          ? canonicalTurnEvent(rawInput, resolved)
          : normalizeClaudeHook('Stop', rawInput);
      } catch {
        event = undefined;
      }
      if (event) {
        const receipt = hookFailureReceipt(event, 'finalize_failed', now);
        recordFailure(resolved, receipt);
      }
      return {};
    }
    return {
      continue: true,
      systemMessage: `Pulse ${eventName} degraded: bound memory context is unavailable.`,
    };
  }
}

async function readHookInput(stream = process.stdin) {
  const chunks = [];
  let size = 0;
  for await (const chunk of stream) {
    size += chunk.length;
    if (size > MAX_HOOK_INPUT) throw new Error('claude_hook_input_too_large');
    chunks.push(chunk);
  }
  if (chunks.length === 0) throw new Error('claude_hook_input_empty');
  return JSON.parse(Buffer.concat(chunks).toString('utf8'));
}

function writeClaudeOutput(serialized, stream = process.stdout) {
  return new Promise((resolveWrite, rejectWrite) => {
    stream.write(serialized, (error) => {
      if (error) rejectWrite(error); else resolveWrite();
    });
  });
}

export async function flushClaudeHookOutput(eventName, input, output, dependencies = {}) {
  const serialized = JSON.stringify(output);
  // Persist the exact final payload measurement before stdout. There is no
  // two-resource transaction with the host pipe: this is an offer attempt,
  // never proof that the provider consumed the context.
  const delivery = await persistContinuityDelivery(
    output,
    output?.hookSpecificOutput?.additionalContext,
    {
      recordDelivery: dependencies.recordDelivery,
      request: dependencies.deliveryRequest ?? activatedBoundPulseRequest,
    },
  );
  await (dependencies.writeOutput ?? writeClaudeOutput)(`${serialized}\n`);
  if (delivery?.receipt && delivery.offer.purpose === 'session_start') {
    try {
      await (dependencies.recordObservationTicket ?? recordContinuityObservationTicket)(delivery, {
        platformServices: dependencies.platformServices,
      });
    } catch { /* a missing ticket keeps readiness pending without breaking the host */ }
  }
  if (delivery?.offer.purpose !== 'subagent_start' &&
      !/degraded|finalize_failed|pulse_authority_unavailable/.test(serialized)) {
    try {
      const resolved = delivery?.resolved ??
        (dependencies.resolveRuntime ?? ((value) => resolveBoundCodexRuntime(value, { host: HOST })))(input);
      (dependencies.recordReadiness ?? recordClaudeHookReadiness)(eventName, resolved, { output, input });
    } catch {
      // Readiness is evidence, never a reason to change hook behavior.
    }
  }
}

export async function runClaudeHookCLI(eventName, dependencies = {}) {
  const input = dependencies.input ?? await readHookInput(dependencies.inputStream);
  const output = await (dependencies.handleHook ?? handleClaudeHook)(eventName, input, dependencies);
  await flushClaudeHookOutput(eventName, input, output, dependencies);
}

export function claudeWorkspaceDigest(canonicalPath) {
  return createHash('sha256')
    .update('pulse-claude-workspace-v1\x1f')
    .update(canonicalPath)
    .digest('hex');
}

function readinessMilestone(eventName, options) {
  if (eventName === 'UserPromptSubmit' &&
      options.output?.hookSpecificOutput?.hookEventName === 'UserPromptSubmit') return 'prompt_context';
  if (eventName === 'PostToolUse' && isClaudeProductRememberTool(options.input?.tool_name) &&
      /^Pulse Memory Tray receipt:/.test(options.output?.systemMessage ?? '')) return 'write_receipt';
  if (eventName === 'Stop' && options.output?.decision !== 'block' &&
      options.output?.continue !== true) return 'turn_finalize';
  return undefined;
}

export function recordClaudeHookReadiness(eventName, resolved, options = {}) {
	const digest = options.hooksDigest ?? installedClaudeHookContractDigest() ?? process.env.PULSE_CLAUDE_HOOKS_DIGEST;
  const milestone = options.milestone ?? readinessMilestone(eventName, options);
  if (!/^[a-f0-9]{64}$/.test(digest ?? '') || ![
    'prompt_context', 'write_receipt', 'turn_finalize',
  ].includes(milestone) || !resolved?.binding || !resolved?.runtime) return false;
  const { binding, runtime } = resolved;
  const sessionID = options.input?.session_id;
  const turnID = options.input?.prompt_id;
  const turnProof = options.turnProof ?? (
    typeof sessionID === 'string' && typeof turnID === 'string'
      ? createHash('sha256')
        .update('pulse-claude-readiness-turn-v1\x1f')
        .update(binding.binding_digest ?? '')
        .update('\x1f')
        .update(sessionID)
        .update('\x1f')
        .update(turnID)
        .digest('hex')
      : undefined
  );
  if (!/^[a-f0-9]{64}$/.test(binding.binding_digest ?? '') ||
      !Number.isSafeInteger(binding.resolver_epoch) ||
      typeof binding.workspace?.repository_id !== 'string' ||
      typeof binding.workspace?.canonical_path !== 'string' ||
      !/^[a-f0-9]{64}$/.test(turnProof ?? '')) return false;
  const dataDir = runtime.data_dir;
  mkdirSync(dataDir, { recursive: true, mode: 0o700 });
  const path = join(dataDir, 'claude-code-hook-readiness.json');
  const temporary = `${path}.${process.pid}.${Date.now()}.new`;
  const authority = {
    binding_digest: binding.binding_digest,
    resolver_epoch: binding.resolver_epoch,
    repository_id: binding.workspace.repository_id,
    workspace_digest: claudeWorkspaceDigest(binding.workspace.canonical_path),
  };
  let milestones = {};
  try {
    const current = JSON.parse(readFileSync(path, 'utf8'));
    if (current?.schema === 'pulse.claude_code_hook_readiness.v1' &&
        current.hooks_digest === digest && current.turn_proof === turnProof &&
        Object.entries(authority).every(([key, value]) => current[key] === value) &&
        current.milestones && typeof current.milestones === 'object') {
      milestones = current.milestones;
    }
  } catch {
    // Missing, invalid, stale, or another workspace starts a fresh receipt.
  }
  const observedAt = (options.now ?? new Date()).toISOString();
  try {
    writeFileSync(temporary, `${JSON.stringify({
      schema: 'pulse.claude_code_hook_readiness.v1',
      hooks_digest: digest,
      ...authority,
      turn_proof: turnProof,
      milestones: { ...milestones, [milestone]: observedAt },
      last_event: eventName,
      observed_at: observedAt,
    })}\n`, { mode: 0o600, flag: 'wx' });
    renameSync(temporary, path);
  } finally {
    rmSync(temporary, { force: true });
  }
  return true;
}
