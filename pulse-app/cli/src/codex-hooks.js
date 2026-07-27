import { createHash } from 'node:crypto';
import { spawn } from 'node:child_process';
import {
  existsSync,
  mkdirSync,
  readFileSync,
	realpathSync,
  renameSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { homedir } from 'node:os';
import { dirname, isAbsolute, join, resolve } from 'node:path';

import { inspectCodexPluginCompatibility } from './codex-install.js';

import {
  annotateContinuityDelivery,
  contextLease,
  eventBoundContextLease,
  extractPulseReceiptRefs,
  gitTeamMemoryCardApproverLabels,
  gitTeamMemoryCardMarkers,
  isDestructivePulseShellInvocation,
  isDestructivePulseTool,
  isPulseRuntimeAuthorityMutation,
  isGuardedCodexTool,
  isTrustedPulseProductTool,
  isUntrustedPulseMemoryWriteTool,
  normalizeCodexHook,
  renderAdditionalContext,
  renderGitTeamMemoryCards,
  verifyGitTeamMemoryCardBlock,
} from './host-adapter.js';
import {
	activatedBoundPulseRequest,
  readCodexFinalizeMarker,
  readCodexTurnContext,
  resolveBoundCodexRuntime,
  resolveCodexRuntime,
  writeCodexToolLease,
  writeCodexTurnContext,
} from './codex-runtime.js';
import { ensureBoundPortableProjectID } from './project-source.js';
import { syncCommittedGitTeamMemory } from './git-team-memory.js';
import {
  composeBoundResumeEvidence,
  hasContinuitySessionDelivery,
  observePendingContinuityDelivery,
  persistContinuityDelivery,
  recordContinuityObservationTicket,
} from './product-compositor.js';

const MAX_HOOK_INPUT = 1 << 20;

const HEALTHY = Symbol('pulse.codex_hook_healthy');
const WRITE_CORROBORATED = Symbol('pulse.codex_hook_write_corroborated');
const CODEX_PRODUCT_TOOL = Object.freeze({ codexPluginAlias: true });

const PERSONAL_AUTO_CAPTURE_CONTEXT = `
Pulse Personal automatic capture (local, private, and silent):
- During this same normal turn, before the single final user-facing response, call the installed pulse-product pulse_remember tool once only when the work produced a compact durable decision, correction, preference, open loop, or project-state change.
- Omit tags unless every tag is an ASCII safe slug matching ^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$; never use display labels or tags containing spaces.
- Do not announce routine capture, narrate the tool call, or add a save status, receipt, or second user-facing response. A routine capture failure must not alter the user-facing answer; if the user explicitly asks whether saving succeeded, answer truthfully. If nothing durable changed, do not call a memory tool.
- The user's current tool-use instruction wins. If this turn forbids tools, do not capture memory.
- Never store raw prompts, transcripts, secrets, credentials, local paths, one-turn output formatting, evaluation protocol, exact-response instructions, NO_AUTO_CONTEXT checks, or other test-control instructions. An explicit lasting project fact remains eligible when the user identifies it as durable project state.`;

function healthy(output) {
  Object.defineProperty(output, HEALTHY, { value: true });
  return output;
}

function corroboratedWrite(output) {
  Object.defineProperty(output, WRITE_CORROBORATED, { value: true });
  return output;
}

function hookFailureReceipt(event, reasonCode, now) {
  const digest = createHash('sha256')
    .update('pulse-codex-hook-failure-v1\x1f')
    .update(event.source_event_key)
    .update('\x1f')
    .update(reasonCode)
    .digest('hex');
  return {
    schema: 'pulse.hook_failure_receipt.v1',
    receipt_id: `hook_${digest}`,
    host: 'codex',
    session_id: event.session_id,
    turn_id: event.turn_id,
    source_event_key: event.source_event_key,
    status: 'failed',
    reason_code: reasonCode,
    created_at: now.toISOString(),
  };
}

function recordHookFailure(resolved, receipt) {
  const directory = process.env.PULSE_PLUGIN_DATA
    ? join(process.env.PULSE_PLUGIN_DATA, 'hook-receipts')
    : resolved?.runtime?.data_dir
      ? join(resolved.runtime.data_dir, 'hook-receipts')
      : undefined;
  if (!directory) return;
  const path = join(directory, `${receipt.receipt_id}.json`);
  const temporary = `${path}.new`;
  try {
    mkdirSync(directory, { recursive: true, mode: 0o700 });
    if (existsSync(path)) return;
    writeFileSync(temporary, `${JSON.stringify(receipt)}\n`, { mode: 0o600, flag: 'wx' });
    renameSync(temporary, path);
  } catch {
    // Failure evidence is best-effort. A diagnostics path must never break or
    // restart the user's completed turn.
  } finally {
    rmSync(temporary, { force: true });
  }
}

function threadContext(resolved, event) {
  return {
    thread_id: resolved.binding.workspace.repository_id,
    project_id: resolved.binding.workspace.workspace_id,
    session_id: event.session_id,
    host: 'codex',
  };
}

function opaqueTurnCorrelation(kind, value) {
  return `${kind}:${createHash('sha256').update(`${kind}\x1f${value}`).digest('hex')}`;
}

function sharedMemoryAuthority(resolved, dependencies) {
  const portableProjectID = (dependencies.portableProjectID ?? ensureBoundPortableProjectID)(resolved);
  const repositoryID = resolved?.binding?.workspace?.repository_id;
  const bindingDigest = resolved?.binding?.binding_digest;
  if (!/^project_[a-f0-9]{32}$/.test(portableProjectID ?? '') ||
      typeof repositoryID !== 'string' || !repositoryID.startsWith('repository_') ||
      !/^[a-f0-9]{64}$/.test(bindingDigest ?? '')) {
    throw new Error('git_team_memory_hook_authority_unavailable');
  }
  return {
    portable_project_id: portableProjectID,
    repository_id: repositoryID,
    binding_digest: bindingDigest,
  };
}

function exactGitTeamMemoryOK(value) {
  return typeof value === 'string' && value.normalize('NFC').trim().toLowerCase() === 'ok';
}

async function presentGitTeamMemoryCards(resolved, event, rawInput, request, dependencies) {
  const markers = gitTeamMemoryCardMarkers(rawInput.last_assistant_message);
  const approverLabels = gitTeamMemoryCardApproverLabels(rawInput.last_assistant_message);
  if (markers.length === 0 && approverLabels.length === 0) return undefined;
  if (markers.length !== 1 || approverLabels.length !== 1) {
    throw new Error('git_team_memory_card_presentation_ambiguous');
  }
  const authority = sharedMemoryAuthority(resolved, dependencies);
  const batch = await request(resolved, '/project/shared-memory/review/inspect', {
    body: {
      schema: 'pulse.git_team_memory.inspect.v1', ...authority, batch_id: markers[0].batch_id,
    },
  });
  const cards = renderGitTeamMemoryCards(batch, { approverLabel: approverLabels[0] });
  if (cards.batch_generation !== markers[0].batch_generation ||
      !verifyGitTeamMemoryCardBlock(rawInput.last_assistant_message, cards)) {
    throw new Error('git_team_memory_card_presentation_mismatch');
  }
  return request(resolved, '/project/shared-memory/review/present', {
    body: {
      schema: 'pulse.git_team_memory.presentation.v1', ...authority,
      batch_id: cards.batch_id, batch_generation: cards.batch_generation,
      host: 'codex', task_id: batch.task_id,
      session_ref: opaqueTurnCorrelation('session', event.session_id),
      turn_ref: opaqueTurnCorrelation('turn', event.turn_id),
      source_event_digest: event.source_event_key.slice('event_'.length),
      card_block_digest: cards.card_block_digest,
      candidate_digests: cards.candidate_digests,
      approver_label_digest: cards.approver_label_digest,
    },
  });
}

async function approveExactGitTeamMemoryOK(resolved, event, rawInput, request, dependencies) {
  if (!exactGitTeamMemoryOK(rawInput.prompt)) return undefined;
  const authority = sharedMemoryAuthority(resolved, dependencies);
  return request(resolved, '/project/shared-memory/review/exact-ok', {
    body: {
      schema: 'pulse.git_team_memory.exact_ok.v1', ...authority, host: 'codex',
      session_ref: opaqueTurnCorrelation('session', event.session_id),
      prompt_event_digest: event.source_event_key.slice('event_'.length),
    },
  });
}

function receiptMatchesEvent(receipt, ref, marker, event) {
  return receipt?.schema === 'pulse.write_receipt.v1' &&
    receipt.receipt_id === ref.receipt_id && receipt.ledger_id === ref.ledger_id &&
    receipt.candidate_id === ref.candidate_id && receipt.status === ref.status &&
    receipt.ledger_id === marker.ledger_id && receipt.safe_provenance?.host === 'codex' &&
    receipt.safe_provenance.session_id === opaqueTurnCorrelation('session', event.session_id) &&
    receipt.safe_provenance.turn_id === opaqueTurnCorrelation('turn', event.turn_id) &&
    receipt.safe_provenance.source_event_key === opaqueTurnCorrelation('event', event.source_event_key);
}

async function resumeContext(resolved, event, request, dependencies = {}) {
  const composed = await (dependencies.composeResume ?? composeBoundResumeEvidence)(resolved, event, {
    host: 'codex',
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
    host: 'codex',
    session_id: event.session_id,
    turn_id: event.turn_id,
    source_event_key: event.source_event_key,
    idempotency_key: event.idempotency_key,
    binding_digest: resolved.binding.binding_digest,
    policy_epoch: 0,
    resolver_epoch: resolved.binding.resolver_epoch,
  };
}

function promptBootstrapSessionEvent(event) {
  return Object.freeze({
    ...event,
    native_event: 'SessionStart',
    event: 'session_start',
    source: 'prompt_bootstrap',
  });
}

async function finalizeNoChange(resolved, event, request) {
  try {
    return await request(resolved, '/turn/no-change', {
      body: noChangeBody(resolved, event),
      idempotencyKey: event.idempotency_key,
    });
  } catch (error) {
    if (error?.status === 409 && /turn already finalized with a different result/.test(error.message)) {
      return { status: 'already_finalized' };
    }
    throw error;
  }
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

function authorityDenied() {
  return preToolDenied('pulse_authority_unavailable: restart the task after Pulse binding is restored');
}

function canonicalCodexTurnEvent(rawInput) {
  return normalizeCodexHook('Stop', {
    ...rawInput,
    agent_id: undefined,
    hook_event_name: 'Stop',
    stop_hook_active: rawInput.stop_hook_active ?? false,
  });
}

export async function handleCodexHook(eventName, rawInput, dependencies = {}) {
  const now = dependencies.now?.() ?? new Date();
  const event = eventName === 'Stop'
    ? canonicalCodexTurnEvent(rawInput)
    : normalizeCodexHook(eventName, rawInput);
  const resolveRuntime = dependencies.resolveRuntime ?? resolveBoundCodexRuntime;
	const request = dependencies.request ?? activatedBoundPulseRequest;
  const recordFailure = dependencies.recordFailure ?? recordHookFailure;

  if (eventName === 'PreToolUse') {
    if (isDestructivePulseTool(rawInput.tool_name) ||
        isDestructivePulseShellInvocation(rawInput.tool_name, rawInput.tool_input) ||
        isPulseRuntimeAuthorityMutation(rawInput.tool_name, rawInput.tool_input)) {
      return healthy(preToolDenied(
        'Pulse deletion is user-controlled. Product vault wipe requires the privileged OS-backed Pulse surface and is never agent-callable.',
      ));
    }
    if (isUntrustedPulseMemoryWriteTool(rawInput.tool_name, CODEX_PRODUCT_TOOL)) {
      return healthy(preToolDenied(
        'Pulse Personal memory writes require the pulse-product server. Legacy or lookalike Pulse servers cannot create Personal memory.',
      ));
    }
    if (!isGuardedCodexTool(rawInput.tool_name)) return {};
    try {
      const resolved = resolveRuntime(rawInput);
      const stopEvent = canonicalCodexTurnEvent(rawInput);
      (dependencies.readTurnContext ?? readCodexTurnContext)(resolved, stopEvent, now);
      await request(resolved, '/memory/status', { method: 'GET', timeoutMs: 1200 });
      if (isTrustedPulseProductTool(rawInput.tool_name, CODEX_PRODUCT_TOOL)) {
        (dependencies.writeToolLease ?? writeCodexToolLease)(
          resolved, stopEvent, rawInput.tool_name, rawInput.tool_input, rawInput.tool_use_id, now,
        );
      }
      return healthy({});
    } catch {
      return authorityDenied();
    }
  }

  let resolved;
  try {
    resolved = resolveRuntime(rawInput);
    if (eventName === 'SessionStart') {
      let syncMessage;
      try {
        const sync = await (dependencies.syncSharedMemory ?? syncCommittedGitTeamMemory)(resolved, {
          ensureProjectID: dependencies.portableProjectID ?? ensureBoundPortableProjectID,
          requestIndex: (body) => request(resolved, '/project/shared-memory/index', {
            body, timeoutMs: 45_000,
          }),
        });
        if (sync?.state === 'indexed') {
          syncMessage = `Pulse Git Team Memory indexed: ${sync.active_count} active project memories (${sync.receipt_id}).`;
        }
      } catch {
        syncMessage = 'Pulse Git Team Memory sync was blocked; no unverified shared project memory was admitted.';
      }
      const context = await resumeContext(resolved, event, request, dependencies);
      return annotateContinuityDelivery(healthy({
        continue: true,
        hookSpecificOutput: { hookEventName: 'SessionStart', additionalContext: context.additionalContext },
        ...(syncMessage ? { systemMessage: syncMessage } : {}),
      }), resolved, event, context.manifest);
    }
    if (eventName === 'UserPromptSubmit') {
      try {
        await (dependencies.observeDelivery ?? observePendingContinuityDelivery)(resolved, event, {
          request: dependencies.deliveryRequest ?? request,
          platformServices: dependencies.platformServices,
        });
      } catch { /* observation evidence is fail-closed and never blocks the user's prompt */ }
      let memorySnapshotDigest;
      if (dependencies.hasSessionDelivery === undefined) {
        try {
          const status = await request(resolved, '/memory/status', { method: 'GET', timeoutMs: 1200 });
          if (/^[a-f0-9]{64}$/.test(status?.memory_snapshot_digest ?? '')) {
            memorySnapshotDigest = status.memory_snapshot_digest;
          }
        } catch { /* a missing current snapshot requires the prompt bootstrap */ }
      }
      let hadObservedSessionDelivery = false;
      try {
        hadObservedSessionDelivery = memorySnapshotDigest === undefined && dependencies.hasSessionDelivery === undefined
          ? false
          : await (dependencies.hasSessionDelivery ?? hasContinuitySessionDelivery)(resolved, event, {
              platformServices: dependencies.platformServices,
              ...(memorySnapshotDigest === undefined ? {} : {
                expectedMemorySnapshotDigest: memorySnapshotDigest,
              }),
            });
      } catch { /* stale or missing observation proof requires the prompt bootstrap */ }
      const stopEvent = canonicalCodexTurnEvent(rawInput);
      (dependencies.writeTurnContext ?? writeCodexTurnContext)(resolved, stopEvent, now);
      let approval;
      try {
        approval = await approveExactGitTeamMemoryOK(resolved, event, rawInput, request, dependencies);
      } catch (error) {
        if (error?.status !== 409 || !/approval is unavailable/.test(error.message ?? '')) throw error;
      }
      const approvalContext = approval
        ? `\nPulse shared-memory approval lease (host-owned; single-use): ${JSON.stringify(approval)}`
        : '';
      if (!hadObservedSessionDelivery) {
        const bootstrapEvent = promptBootstrapSessionEvent(event);
        const context = await resumeContext(resolved, bootstrapEvent, request, dependencies);
        const deliveryManifest = approvalContext
          ? {
              object_ids: context.manifest.object_ids,
              evidence_ids: context.manifest.evidence_ids,
            }
          : context.manifest;
        return annotateContinuityDelivery(healthy({
          continue: true,
          hookSpecificOutput: {
            hookEventName: 'UserPromptSubmit',
            additionalContext: `${context.additionalContext}${approvalContext}${PERSONAL_AUTO_CAPTURE_CONTEXT}`,
          },
        }), resolved, bootstrapEvent, deliveryManifest);
      }
      return healthy({
        continue: true,
        hookSpecificOutput: {
          hookEventName: 'UserPromptSubmit',
          additionalContext: `${renderAdditionalContext([], contextLease(
            resolved.binding, now, 30_000, memorySnapshotDigest,
          ))}${approvalContext}${PERSONAL_AUTO_CAPTURE_CONTEXT}`,
        },
      });
    }
    if (eventName === 'PostToolUse') {
      if (isTrustedPulseProductTool(rawInput.tool_name, CODEX_PRODUCT_TOOL)) {
        const refs = extractPulseReceiptRefs(rawInput.tool_response);
        if (refs.length > 0) {
          const stopEvent = canonicalCodexTurnEvent(rawInput);
          const marker = (dependencies.readFinalizeMarker ?? readCodexFinalizeMarker)(resolved, stopEvent);
          const corroborated = [];
          for (const ref of refs) {
            const receipt = await request(resolved, `/memory/receipts/${encodeURIComponent(ref.receipt_id)}`, {
              method: 'GET', timeoutMs: 1200,
            });
            if (receiptMatchesEvent(receipt, ref, marker, stopEvent)) {
              corroborated.push(ref);
            }
          }
          if (corroborated.length === 0) return healthy({});
          return corroboratedWrite(healthy({}));
        }
      }
      return healthy({});
    }
    if (eventName === 'PreCompact') {
      return healthy({ systemMessage: 'Pulse kept the current turn open across compaction.' });
    }
    if (eventName === 'PostCompact') {
      return healthy({ systemMessage: 'Pulse binding will be reloaded on the compacted session start.' });
    }
    if (eventName === 'SubagentStart') {
      const context = await resumeContext(resolved, event, request, dependencies);
      return annotateContinuityDelivery(healthy({
        hookSpecificOutput: { hookEventName: 'SubagentStart', additionalContext: context.additionalContext },
		systemMessage: 'Pulse subagent boundary: return typed durable-memory candidates to the parent; the parent finalizes the turn once. Role-scoped retrieval is not active.',
      }), resolved, event, context.manifest);
    }
    if (eventName === 'SubagentStop') {
      return healthy({});
    }
    if (eventName === 'Stop') {
      try {
        await (dependencies.observeDelivery ?? observePendingContinuityDelivery)(resolved, event, {
          request: dependencies.deliveryRequest ?? request,
          platformServices: dependencies.platformServices,
        });
      } catch { /* delivery evidence remains pending and never blocks turn finalization */ }
      const presentation = await presentGitTeamMemoryCards(resolved, event, rawInput, request, dependencies);
      if (presentation) {
        await finalizeNoChange(resolved, event, request);
        return healthy({
          systemMessage: `Pulse shared-memory cards presented: ${presentation.presentation_id}`,
        });
      }
      try {
        (dependencies.readFinalizeMarker ?? readCodexFinalizeMarker)(resolved, event);
        return healthy({});
      } catch {
        // No truthful write marker: close the turn as no-change without
        // starting another model pass.
      }
      await finalizeNoChange(resolved, event, request);
      return healthy({});
    }
    throw new Error('unsupported_codex_hook');
  } catch (error) {
    const degradedReason = dependencies.degradedReason?.(error);
    const degradedDiagnostic = typeof degradedReason === 'string'
      ? { pulseTestDiagnostic: degradedReason }
      : {};
    if (eventName === 'Stop') {
      const receipt = hookFailureReceipt(event, 'finalize_failed', now);
      recordFailure(resolved, receipt);
      return {
        continue: true,
        ...degradedDiagnostic,
      };
    }
    return {
      continue: true,
      systemMessage: `Pulse ${eventName} degraded: bound memory context is unavailable.`,
      ...degradedDiagnostic,
    };
  }
}

async function readHookInput(stream = process.stdin) {
  const chunks = [];
  let size = 0;
  for await (const chunk of stream) {
    size += chunk.length;
    if (size > MAX_HOOK_INPUT) throw new Error('codex_hook_input_too_large');
    chunks.push(chunk);
  }
  if (chunks.length === 0) throw new Error('codex_hook_input_empty');
  return JSON.parse(Buffer.concat(chunks).toString('utf8'));
}

function writeCodexOutput(serialized, stream = process.stdout) {
  return new Promise((resolveWrite, rejectWrite) => {
    stream.write(serialized, (error) => {
      if (error) rejectWrite(error); else resolveWrite();
    });
  });
}

export async function flushCodexHookOutput(eventName, input, result, dependencies = {}) {
  const serialized = `${JSON.stringify(result)}\n`;
  // Persist the exact final payload measurement before stdout. There is no
  // two-resource transaction with the host pipe: this is an offer attempt,
  // never proof that the provider consumed the context.
  const delivery = await persistContinuityDelivery(
    result,
    result?.hookSpecificOutput?.additionalContext,
    {
      recordDelivery: dependencies.recordDelivery,
      request: dependencies.deliveryRequest ?? activatedBoundPulseRequest,
    },
  );
  await (dependencies.writeOutput ?? writeCodexOutput)(serialized);
  if (delivery?.receipt && delivery.offer.purpose === 'session_start') {
    try {
      await (dependencies.recordObservationTicket ?? recordContinuityObservationTicket)(delivery, {
        platformServices: dependencies.platformServices,
      });
    } catch { /* a missing ticket keeps readiness pending without breaking the host */ }
  }
  if (result?.[HEALTHY] === true && delivery?.offer.purpose !== 'subagent_start') {
    try {
      const resolved = delivery?.resolved ??
        (dependencies.resolveRuntime ?? resolveBoundCodexRuntime)(input);
      (dependencies.recordReadiness ?? recordCodexHookReadiness)(eventName, resolved, {
			input, output: result, environment: dependencies.environment ?? process.env,
		});
    } catch {
      // Readiness evidence never changes hook behavior.
    }
  }
}

export async function runCodexHookCLI(eventName, dependencies = {}) {
  const input = dependencies.input ?? await readHookInput(dependencies.inputStream);
  const result = await (dependencies.handleHook ?? handleCodexHook)(eventName, input, dependencies);
  await flushCodexHookOutput(eventName, input, result, dependencies);
}

export function codexWorkspaceDigest(canonicalPath) {
  return createHash('sha256')
    .update('pulse-codex-workspace-v1\x1f')
    .update(canonicalPath)
    .digest('hex');
}

const CODEX_NATIVE_HOOK_EVENTS = new Map([
	['SessionStart', 'sessionStart'],
	['UserPromptSubmit', 'userPromptSubmit'],
	['PreToolUse', 'preToolUse'],
	['PostToolUse', 'postToolUse'],
	['PreCompact', 'preCompact'],
	['PostCompact', 'postCompact'],
	['SubagentStart', 'subagentStart'],
	['SubagentStop', 'subagentStop'],
	['Stop', 'stop'],
]);

function nativeHookFailure(reason, detail, extra = {}) {
	return { ready: false, reason, detail, ...extra };
}

export function inspectCodexNativeHookList(response, {
	cwd, pluginRoot, marketplacePluginRoot, cachePluginRoot, edge,
} = {}) {
	if (!Array.isArray(response?.data) || typeof cwd !== 'string' || typeof pluginRoot !== 'string' || !edge) {
		return nativeHookFailure('codex_native_hook_query_invalid', 'Codex returned an invalid hooks/list response');
	}
	if (!isAbsolute(cwd)) {
		return nativeHookFailure('codex_native_hook_query_invalid', 'Codex hook inspection requires an absolute workspace path');
	}
	let canonicalCwd;
	try { canonicalCwd = realpathSync(cwd); } catch {
		return nativeHookFailure('codex_native_hook_query_invalid', 'Codex hook inspection workspace is unavailable');
	}
	const target = response.data.find((entry) => {
		if (typeof entry?.cwd !== 'string' || !isAbsolute(entry.cwd)) return false;
		try { return realpathSync(entry.cwd) === canonicalCwd; } catch { return false; }
	});
	if (!target || !Array.isArray(target.hooks) || !Array.isArray(target.errors)) {
		return nativeHookFailure('codex_native_hook_query_invalid', 'Codex returned no hook state for this workspace');
	}
	if (target.errors.length > 0) {
		return nativeHookFailure('codex_native_hook_query_error', 'Codex reported hook configuration errors');
	}
	const pulseHooks = target.hooks.filter((hook) => hook?.pluginId === 'pulse@zbs-gg');
	if (pulseHooks.length !== CODEX_NATIVE_HOOK_EVENTS.size) {
		return nativeHookFailure('codex_native_hook_set_mismatch', 'Codex did not load the complete Pulse plugin hook set');
	}
	const sourcePaths = new Set();
	for (const hook of pulseHooks) {
		try { sourcePaths.add(realpathSync(hook.sourcePath)); } catch { sourcePaths.add(''); }
	}
	if (sourcePaths.size !== 1 || sourcePaths.has('')) {
		return nativeHookFailure('codex_native_hook_set_mismatch', 'Codex Pulse hooks do not share one exact source');
	}
	const [nativeSource] = sourcePaths;
	const allowedSources = new Map();
	for (const root of [pluginRoot, marketplacePluginRoot, cachePluginRoot]) {
		if (typeof root !== 'string') continue;
		try {
			const canonicalRoot = realpathSync(root);
			allowedSources.set(realpathSync(join(canonicalRoot, 'hooks', 'hooks.json')), canonicalRoot);
		} catch { /* unavailable roots are not trusted */ }
	}
	const nativePluginRoot = allowedSources.get(nativeSource);
	if (!nativePluginRoot) {
		return nativeHookFailure('codex_native_hook_set_mismatch',
			'Codex native hook source is outside the signed marketplace, installed plugin, and versioned Codex cache');
	}
	const compatibility = inspectCodexPluginCompatibility({
		installed: true, enabled: true, version: edge.release_version, path: nativePluginRoot,
	}, edge);
	if (!compatibility.ok) {
		return nativeHookFailure('codex_native_hook_set_mismatch',
			`Codex native hook source is not the signed plugin: ${compatibility.reason}`);
	}
	let expectedEvents;
	let expectedDefinitions;
	try {
		const config = JSON.parse(readFileSync(nativeSource, 'utf8'));
		expectedDefinitions = new Map(Object.entries(config?.hooks ?? {}).map(([eventName, entries]) => {
			if (!CODEX_NATIVE_HOOK_EVENTS.has(eventName) || !Array.isArray(entries) || entries.length !== 1 ||
				!Array.isArray(entries[0]?.hooks) || entries[0].hooks.length !== 1 ||
				entries[0].hooks[0]?.type !== 'command') {
				throw new Error('invalid Pulse native hook definition');
			}
			const handler = entries[0].hooks[0];
			if (typeof handler.command !== 'string' || !Number.isSafeInteger(handler.timeout)) {
				throw new Error('invalid Pulse native hook command');
			}
			const nativeEvent = CODEX_NATIVE_HOOK_EVENTS.get(eventName);
			return [nativeEvent, {
				command: handler.command.replaceAll('${PLUGIN_ROOT}', nativePluginRoot),
				matcher: entries[0].matcher ?? null,
				timeoutSec: handler.timeout,
			}];
		}));
		expectedEvents = [...expectedDefinitions.keys()].sort();
		if (expectedEvents.length !== CODEX_NATIVE_HOOK_EVENTS.size) {
			throw new Error('incomplete Pulse native hook definition');
		}
	} catch (error) {
		return nativeHookFailure('codex_native_hook_set_mismatch', error.message);
	}
	const hashes = [];
	const trustStatuses = new Set();
	for (const eventName of expectedEvents) {
		const matches = pulseHooks.filter((hook) => hook.eventName === eventName);
		if (matches.length !== 1) {
			return nativeHookFailure('codex_native_hook_set_mismatch', `Codex Pulse hook mismatch: ${eventName}`);
		}
		const hook = matches[0];
		const expected = expectedDefinitions.get(eventName);
		let sourcePath;
		try { sourcePath = realpathSync(hook.sourcePath); } catch { sourcePath = ''; }
		const mismatches = [
			[sourcePath !== nativeSource, 'source'],
			[hook.source !== 'plugin', 'source-kind'],
			[hook.handlerType !== 'command', 'handler'],
			[hook.command !== expected.command, 'command'],
			[(hook.matcher ?? null) !== expected.matcher, 'matcher'],
			[hook.timeoutSec !== expected.timeoutSec, 'timeout'],
			[hook.enabled !== true, 'enabled'],
			[!/^sha256:[a-f0-9]{64}$/.test(hook.currentHash ?? ''), 'hash'],
		].filter(([failed]) => failed).map(([, field]) => field);
		if (mismatches.length > 0) {
			return nativeHookFailure('codex_native_hook_set_mismatch',
				`Codex Pulse hook identity mismatch (${mismatches.join(', ')}): ${eventName}`);
		}
		if (!['trusted', 'managed'].includes(hook.trustStatus)) {
			return nativeHookFailure('codex_native_hook_trust_required',
				`Codex native hook review is ${hook.trustStatus ?? 'unavailable'}: ${eventName}`,
				{ trust_status: hook.trustStatus ?? 'unavailable' });
		}
		if ((hook.trustStatus === 'managed') !== (hook.isManaged === true)) {
			return nativeHookFailure('codex_native_hook_set_mismatch',
				`Codex Pulse hook management state mismatch: ${eventName}`);
		}
		trustStatuses.add(hook.trustStatus);
		hashes.push(`${eventName}\x00${hook.currentHash}`);
	}
	const hookSetDigest = createHash('sha256')
		.update('pulse-codex-native-hook-set-v1\x00')
		.update(hashes.sort().join('\x00'))
		.digest('hex');
	const trustStatus = trustStatuses.size === 1 ? [...trustStatuses][0] : 'mixed';
	return {
		ready: true,
		reason: 'codex_native_hooks_trusted',
		detail: `Codex reports the exact Pulse plugin hook set as ${trustStatus}`,
		hook_set_digest: hookSetDigest,
		native_plugin_root: nativePluginRoot,
		trust_status: trustStatus,
	};
}

function queryCodexNativeHooks({ codexExecutable, cwd, timeoutMs }) {
	return new Promise((resolveQuery, rejectQuery) => {
		const command = codexExecutable !== 'codex' && codexExecutable.endsWith('.js')
			? process.execPath : codexExecutable;
		const args = command === process.execPath
			? [codexExecutable, 'app-server', '--stdio']
			: ['app-server', '--stdio'];
		const child = spawn(command, args, {
			cwd, env: process.env, stdio: ['pipe', 'pipe', 'pipe'],
		});
		let settled = false;
		let finishing = false;
		let stdout = '';
		let timer;
		let killTimer;
		let shutdownTimer;
		const settle = (error, value) => {
			if (settled) return;
			settled = true;
			clearTimeout(timer);
			clearTimeout(killTimer);
			clearTimeout(shutdownTimer);
			if (error) rejectQuery(error); else resolveQuery(value);
		};
		const finish = (error, value) => {
			if (settled || finishing) return;
			finishing = true;
			clearTimeout(timer);
			try { child.stdin.end(); } catch { /* already closed */ }
			child.once('close', () => settle(error, value));
			try { child.kill('SIGTERM'); } catch { /* close/error path will settle */ }
			killTimer = setTimeout(() => {
				try { child.kill('SIGKILL'); } catch { /* close/error path will settle */ }
			}, 100);
			shutdownTimer = setTimeout(() => settle(error, value), 1000);
		};
		const send = (message) => child.stdin.write(`${JSON.stringify(message)}\n`);
		const handleLine = (line) => {
			if (!line.trim()) return;
			let message;
			try { message = JSON.parse(line); } catch { return; }
			if (message.id === 1) {
				if (message.error) return finish(new Error('codex_native_hook_initialize_failed'));
				send({ method: 'initialized', params: {} });
				send({ id: 2, method: 'hooks/list', params: { cwds: [cwd] } });
			} else if (message.id === 2) {
				if (message.error) return finish(new Error('codex_native_hook_query_failed'));
				finish(undefined, message.result);
			}
		};
		child.stdout.on('data', (chunk) => {
			stdout += chunk.toString('utf8');
			if (stdout.length > 4 * 1024 * 1024) return finish(new Error('codex_native_hook_response_too_large'));
			const lines = stdout.split(/\r?\n/);
			stdout = lines.pop() ?? '';
			for (const line of lines) handleLine(line);
		});
		child.stderr.resume();
		child.stdin.on('error', () => finish(new Error('codex_native_hook_query_unavailable')));
		child.once('error', () => finish(new Error('codex_native_hook_query_unavailable')));
		child.once('exit', (code) => {
			if (!settled && !finishing) finish(new Error(code === 0
				? 'codex_native_hook_response_missing'
				: 'codex_native_hook_query_failed'));
		});
		timer = setTimeout(() => finish(new Error('codex_native_hook_query_timeout')), timeoutMs);
		send({
			id: 1,
			method: 'initialize',
			params: {
				clientInfo: { name: 'pulse-doctor', version: '0.7.0' },
				capabilities: { experimentalApi: true },
			},
		});
	});
}

export async function inspectCodexNativeHookTrust({
	codexExecutable = 'codex', cwd = process.cwd(), pluginRoot, marketplacePluginRoot, cachePluginRoot,
	edge, timeoutMs = 5000, query,
} = {}) {
	try {
		const response = await (query ?? queryCodexNativeHooks)({ codexExecutable, cwd, timeoutMs });
		return inspectCodexNativeHookList(response, {
			cwd, pluginRoot, marketplacePluginRoot, cachePluginRoot, edge,
		});
	} catch (error) {
		return nativeHookFailure('codex_native_hook_query_unavailable', error.message);
	}
}

function syntheticLifecycleAttestorEnabled(environment = process.env) {
	return environment?.PULSE_TRUST_MODE === 'test' &&
		environment?.PULSE_TEST_CODEX_LIFECYCLE_ATTESTOR === '1';
}

export function projectCodexLifecycleAttestation({
	syntheticAuthority = false, testAttestor = false, readiness,
} = {}) {
	if (syntheticAuthority && testAttestor) {
		return {
			...readiness,
			trusted_hook_observed: readiness?.ready === true,
		};
	}
	return {
		ready: false,
		hooks_digest: readiness?.hooks_digest ?? '',
		reason: 'codex_native_lifecycle_attestation_unavailable',
		detail: 'Codex trusts the exact Pulse hooks, but Codex 0.136 exposes no replayable native hook execution evidence to plugins.',
		trusted_hook_observed: false,
	};
}

const READINESS_LIFECYCLE_INPUTS_SCHEMA = 'pulse.readiness_lifecycle_inputs.v1';

function readinessFactTime(value) {
	if (typeof value !== 'string') return undefined;
	const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?Z$/.exec(value);
	if (!match) return undefined;
	const [, yearText, monthText, dayText, hourText, minuteText, secondText, fraction = ''] = match;
	if (fraction.endsWith('0')) return undefined;
	const [year, month, day, hour, minute, second] =
		[yearText, monthText, dayText, hourText, minuteText, secondText].map(Number);
	if (year < 1970 || month < 1 || month > 12 || day < 1 || hour > 23 || minute > 59 || second > 59) {
		return undefined;
	}
	const milliseconds = Date.UTC(year, month - 1, day, hour, minute, second);
	const parsed = new Date(milliseconds);
	if (parsed.getUTCFullYear() !== year || parsed.getUTCMonth() !== month - 1 ||
		parsed.getUTCDate() !== day || parsed.getUTCHours() !== hour ||
		parsed.getUTCMinutes() !== minute || parsed.getUTCSeconds() !== second) return undefined;
	return BigInt(milliseconds) * 1_000_000n + BigInt(fraction.padEnd(9, '0'));
}

function readinessDigest(value) {
	return typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);
}

function readinessIDs(value) {
	return Array.isArray(value) && value.every((item) =>
		typeof item === 'string' && item.length > 0 && item.trim() === item);
}

function readinessID(value) {
	return typeof value === 'string' && value.length > 0 && value.trim() === value;
}

function readinessSessionRef(value) {
	return typeof value === 'string' && /^session:[a-f0-9]{64}$/.test(value);
}

function terminalReadinessFact(fact) {
	const at = readinessFactTime(fact?.created_at);
	if (at === undefined || !['created', 'updated', 'deduplicated'].includes(fact?.status) ||
		fact?.active !== true || !readinessID(fact.receipt_id) ||
		(fact.presentation_receipt_id !== undefined && !readinessID(fact.presentation_receipt_id)) ||
		!readinessID(fact.object_id) || !readinessDigest(fact.content_digest) ||
		!readinessID(fact.memory_kind) || fact.memory_kind === 'system_event' ||
		!readinessID(fact.conversation_scope) || fact.conversation_scope === 'install_event' ||
		!readinessDigest(fact.binding_digest) || !readinessID(fact.repository_id) ||
		!readinessID(fact.host) || fact.host === 'pulse-cli' || !readinessSessionRef(fact.session_ref) ||
		!readinessIDs(fact.evidence_ids ?? [])) return undefined;
	return { fact: structuredClone(fact), at };
}

function contextReferencesTerminal(fact, terminal) {
	return fact.object_ids.includes(terminal.object_id) &&
		(terminal.evidence_ids.length === 0 || terminal.evidence_ids.some((id) => fact.evidence_ids.includes(id)));
}

function sameReadinessIDs(left, right) {
	return left.length === right.length && left.every((value, index) => value === right[index]);
}

function matchingContextReadinessFact(fact, terminal, after, acknowledgement, offered) {
	const at = readinessFactTime(fact?.created_at);
	if (at === undefined || at <= after || fact?.acknowledgement !== acknowledgement ||
		fact?.purpose !== 'session_start' ||
		!readinessID(fact.context_id) || !readinessDigest(fact.payload_digest) ||
		fact.binding_digest !== terminal.binding_digest || fact.repository_id !== terminal.repository_id ||
		!readinessID(fact.host) || !readinessSessionRef(fact.session_ref) ||
		fact.session_ref === terminal.session_ref || !readinessIDs(fact.object_ids) ||
		!readinessIDs(fact.evidence_ids) || !contextReferencesTerminal(fact, terminal)) return undefined;
	if (offered && (fact.context_id !== offered.context_id || fact.payload_digest !== offered.payload_digest ||
		fact.purpose !== offered.purpose || fact.host !== offered.host || fact.session_ref !== offered.session_ref ||
		!sameReadinessIDs(fact.object_ids, offered.object_ids) ||
		!sameReadinessIDs(fact.evidence_ids, offered.evidence_ids))) return undefined;
	return { fact: structuredClone(fact), at };
}

function earliestReadinessFact(facts) {
	return facts.sort((left, right) => {
		if (left.at < right.at) return -1;
		if (left.at > right.at) return 1;
		return String(left.fact.receipt_id ?? left.fact.context_id).localeCompare(
			String(right.fact.receipt_id ?? right.fact.context_id));
	})[0];
}

// This pure projection deliberately writes nothing. Future doctor/Home
// ReadinessSnapshot consumers receive the same terminal/offered/observed facts
// from the authoritative vault and recompute this chain on every read.
export function projectReadinessLifecycleInputs(memories = [], deliveries = []) {
	const result = { schema: READINESS_LIFECYCLE_INPUTS_SCHEMA, state: 'first_memory_pending' };
	if (!Array.isArray(memories) || !Array.isArray(deliveries)) return result;
	const terminal = earliestReadinessFact(memories.map(terminalReadinessFact).filter(Boolean));
	if (!terminal) return result;
	result.terminal_memory = terminal.fact;
	result.state = 'context_offer_pending';
	const offers = deliveries.map((fact) =>
		matchingContextReadinessFact(fact, terminal.fact, terminal.at, 'offered_to_host')).filter(Boolean);
	offers.sort((left, right) => {
		if (left.at < right.at) return -1;
		if (left.at > right.at) return 1;
		return String(left.fact.context_id).localeCompare(String(right.fact.context_id));
	});
	if (offers.length === 0) return result;
	result.offered_to_host = offers[0].fact;
	result.state = 'host_observation_pending';
	for (const offered of offers) {
		const observed = earliestReadinessFact(deliveries.map((fact) =>
			matchingContextReadinessFact(fact, terminal.fact, offered.at, 'host_observed', offered.fact)).filter(Boolean));
		if (!observed) continue;
		result.offered_to_host = offered.fact;
		result.host_observed = observed.fact;
		result.state = 'ready';
		break;
	}
	return result;
}

function readinessMilestone(eventName, options) {
	if (eventName === 'SessionStart' &&
		options.output?.hookSpecificOutput?.hookEventName === 'SessionStart') return 'session_context';
  if (eventName === 'UserPromptSubmit' &&
      options.output?.hookSpecificOutput?.hookEventName === 'UserPromptSubmit') return 'prompt_context';
  if (eventName === 'PostToolUse' &&
      isTrustedPulseProductTool(options.input?.tool_name, CODEX_PRODUCT_TOOL) &&
      options.output?.[WRITE_CORROBORATED] === true) return 'write_receipt';
  if (eventName === 'Stop' && options.output?.decision !== 'block' && options.output?.continue !== true) {
    return 'turn_finalize';
  }
  return undefined;
}

export function recordCodexHookReadiness(eventName, resolved, options = {}) {
	if (!syntheticLifecycleAttestorEnabled(options.environment)) return false;
  const hooksDigest = options.hooksDigest ?? process.env.PULSE_HOOK_BUNDLE_DIGEST;
  const milestone = options.milestone ?? readinessMilestone(eventName, options);
  if (!/^[a-f0-9]{64}$/.test(hooksDigest ?? '') ||
		  !['session_context', 'prompt_context', 'write_receipt', 'turn_finalize'].includes(milestone) ||
      !resolved?.binding || !resolved?.runtime) return false;
  const { binding, runtime } = resolved;
  const sessionID = options.input?.session_id;
  const turnID = options.input?.turn_id;
	const sessionProof = options.sessionProof ?? (
		typeof sessionID === 'string'
			? createHash('sha256')
				.update('pulse-codex-readiness-session-v1\x1f')
				.update(binding.binding_digest ?? '')
				.update('\x1f').update(sessionID)
				.digest('hex')
			: undefined
	);
	let turnProof = options.turnProof ?? (
    typeof sessionID === 'string' && typeof turnID === 'string'
      ? createHash('sha256')
        .update('pulse-codex-readiness-turn-v1\x1f')
        .update(binding.binding_digest ?? '')
        .update('\x1f').update(sessionID).update('\x1f').update(turnID)
        .digest('hex')
      : undefined
  );
  if (!/^[a-f0-9]{64}$/.test(binding.binding_digest ?? '') ||
      !Number.isSafeInteger(binding.resolver_epoch) ||
      typeof binding.workspace?.repository_id !== 'string' ||
      typeof binding.workspace?.canonical_path !== 'string' ||
		  !/^[a-f0-9]{64}$/.test(sessionProof ?? '') ||
		  (milestone !== 'session_context' && !/^[a-f0-9]{64}$/.test(turnProof ?? ''))) return false;
  const dataDir = options.dataDir ?? process.env.PULSE_DATA_DIR ?? join(homedir(), '.pulse');
  mkdirSync(dataDir, { recursive: true, mode: 0o700 });
  const path = join(dataDir, 'codex-hook-readiness.json');
  const temporary = `${path}.${process.pid}.${Date.now()}.new`;
  const authority = {
    binding_digest: binding.binding_digest,
    resolver_epoch: binding.resolver_epoch,
    repository_id: binding.workspace.repository_id,
    workspace_digest: codexWorkspaceDigest(binding.workspace.canonical_path),
  };
  let milestones = {};
  try {
    const current = JSON.parse(readFileSync(path, 'utf8'));
		if (current?.schema === 'pulse.codex_hook_readiness.v2' &&
				current.hooks_digest === hooksDigest && current.session_proof === sessionProof &&
        Object.entries(authority).every(([key, value]) => current[key] === value) &&
        current.milestones && typeof current.milestones === 'object') {
			if (milestone === 'session_context' || current.turn_proof === null || current.turn_proof === turnProof) {
				milestones = current.milestones;
				if (milestone === 'session_context' && current.turn_proof) turnProof = current.turn_proof;
			} else if (typeof current.milestones.session_context === 'string') {
				milestones = { session_context: current.milestones.session_context };
			}
    }
  } catch {
    // Missing, invalid, stale, or another turn starts a fresh receipt.
  }
  const observedAt = (options.now ?? new Date()).toISOString();
  const receipt = {
		schema: 'pulse.codex_hook_readiness.v2',
    hooks_digest: hooksDigest,
    ...authority,
		session_proof: sessionProof,
		turn_proof: turnProof ?? null,
    milestones: { ...milestones, [milestone]: observedAt },
    last_event: eventName,
    observed_at: observedAt,
  };
  try {
    writeFileSync(temporary, `${JSON.stringify(receipt)}\n`, { mode: 0o600, flag: 'wx' });
    renameSync(temporary, path);
  } finally {
    rmSync(temporary, { force: true });
  }
  return true;
}

export function resolveCodexMcpRuntime(cwd = process.cwd()) {
  return resolveCodexRuntime(cwd);
}

export function defaultProductDaemonPath() {
  return process.env.PULSE_GO_BIN || join(process.env.PULSE_DATA_DIR || join(homedir(), '.pulse'), 'bin', 'pulse-product-daemon');
}
