import { createHash } from 'node:crypto';

import { recoverWorkspaceBindingTransaction } from './binding-admin.js';
import {
  boundPulseRequest,
  consumeHostToolLease,
  readHostTurnContext,
  resolveBoundCodexRuntime,
  writeHostFinalizeMarker,
  writeHostToolLease,
  writeHostTurnContext,
} from './codex-runtime.js';
import { composePromptMemoryContext, PERSONAL_AUTO_CAPTURE_CONTEXT } from './product-compositor.js';
import { readOpenCodeOptions } from './opencode-install.js';
import { defaultPlatformServices } from './platform-services.js';

const HOST = 'opencode';
const STABLE_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$/;
const MAX_INPUT_BYTES = 128 * 1024;
const FUN_FACT_UNSAFE = /(?:\b(?:api[_ -]?key|authorization|bearer|password|secret|token)\s*[:=]|\b(?:sk|ghp|github_pat|xox[baprs])[-_][A-Za-z0-9_-]+|(?:^|\s)(?:\/Users\/|\/home\/|\/private\/|[A-Za-z]:\\)|[a-z][a-z0-9+.-]*:\/\/)/i;

function safeFunFactText(value) {
  return typeof value === 'string' && value.trim() === value && value.length >= 1 && value.length <= 180 &&
    !value.includes('\u0000') && !FUN_FACT_UNSAFE.test(value);
}

function stable(value, code) {
  if (typeof value !== 'string' || !STABLE_ID.test(value)) throw new Error(code);
  return value;
}

function canonicalEvent(input) {
  const sessionID = stable(input.session_id, 'opencode_session_invalid');
  const turnID = stable(input.turn_id, 'opencode_turn_invalid');
  const workspace = input.cwd;
  if (typeof workspace !== 'string' || workspace.length === 0 || workspace.includes('\u0000')) {
    throw new Error('opencode_workspace_invalid');
  }
  const model = typeof input.model === 'string' && input.model.length > 0
    ? input.model.slice(0, 255)
    : 'opencode_model_unavailable';
  const sourceMaterial = [HOST, sessionID, turnID, workspace, 'prompt_submitted'].join('\x1f');
  const sourceDigest = createHash('sha256').update(`pulse-opencode-source-event-v1\x1f${sourceMaterial}`).digest('hex');
  const sourceEventKey = `event_${sourceDigest}`;
  const event = {
    schema: 'pulse.lifecycle_event.v1',
    host: HOST,
    native_event: 'chat.message',
    event: 'turn_start',
    session_id: sessionID,
    turn_id: turnID,
    workspace,
    model,
    source: 'prompt_submitted',
    stop_hook_active: false,
    source_event_key: sourceEventKey,
  };
  event.idempotency_key = `lifecycle:${createHash('sha256').update([
    event.schema, event.host, event.event, event.session_id, event.turn_id, event.workspace, event.source,
  ].join('\x1f')).digest('hex')}`;
  return event;
}

function validateMemoryInput(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value) ||
      Object.keys(value).some((key) => ![
        'session_id', 'turn_id', 'source_event_key', 'idempotency_key', 'items', 'tool_use_id',
      ].includes(key)) || !Array.isArray(value.items) || value.items.length < 1 || value.items.length > 3) {
    throw new Error('pulse_memory_input_invalid');
  }
  const kinds = new Set(['decision', 'preference', 'open_loop', 'project_state', 'correction', 'emotion']);
  const emotionLabels = new Set([
    'joy', 'sadness', 'anger', 'fear', 'trust', 'disgust',
    'anticipation', 'surprise', 'shame', 'guilt',
  ]);
  const items = value.items.map((item) => {
    if (!item || typeof item !== 'object' || Array.isArray(item) ||
        Object.keys(item).some((key) => !['kind', 'scope', 'summary', 'emotion'].includes(key)) ||
        !kinds.has(item.kind) || !['personal', 'project'].includes(item.scope) ||
        typeof item.summary !== 'string' || item.summary.trim() !== item.summary ||
        item.summary.length < 1 || item.summary.length > 400 || Buffer.byteLength(item.summary, 'utf8') > 1200) {
      throw new Error('pulse_memory_item_invalid');
    }
    if (item.kind !== 'emotion') {
      if (item.emotion !== undefined) throw new Error('pulse_memory_emotion_unexpected');
      return item;
    }
    const emotion = item.emotion;
    if (!emotion || typeof emotion !== 'object' || Array.isArray(emotion) ||
        Object.keys(emotion).some((key) => !['label', 'intensity', 'source', 'cause'].includes(key)) ||
        !emotionLabels.has(emotion.label) || !Number.isFinite(emotion.intensity) ||
        emotion.intensity < 0 || emotion.intensity > 1 || !['user', 'inferred'].includes(emotion.source) ||
        (emotion.cause !== undefined && (typeof emotion.cause !== 'string' ||
          emotion.cause.trim() !== emotion.cause || emotion.cause.length < 1 || emotion.cause.length > 240))) {
      throw new Error('pulse_memory_emotion_invalid');
    }
    return item;
  });
  return {
    session_id: stable(value.session_id, 'opencode_session_invalid'),
    turn_id: stable(value.turn_id, 'opencode_turn_invalid'),
    source_event_key: value.source_event_key,
    idempotency_key: value.idempotency_key,
    tool_use_id: stable(value.tool_use_id, 'opencode_tool_use_invalid'),
    items,
  };
}

function memoryFinalizeBody(input, context, now = new Date()) {
  const timestamp = now.toISOString();
  const candidates = input.items.map((item, index) => {
    const memoryScope = item.scope === 'personal' ? 'personal_global' : 'project';
    if (item.kind !== 'emotion') {
      return {
        kind: 'memory_capsule',
        memory_scope: memoryScope,
        capsule: {
          schema: 'pulse.memory_capsule.v1',
          source: { host: HOST, conversation_scope: 'current_turn', timestamp },
          items: [{
            kind: item.kind,
            redacted_summary: item.summary,
            confidence: 1,
            evidence_hint: 'current_turn',
            privacy_tier: 'normal',
            retention: item.scope === 'personal' ? 'long_term' : 'project',
          }],
          raw_input_included: false,
        },
      };
    }
    const emotion = item.emotion;
    const confidence = emotion.source === 'user' ? 1 : 0.8;
    const derivation = emotion.source === 'user' ? 'explicit' : 'inferred';
    return {
      kind: 'semantic_delta',
      memory_scope: memoryScope,
      semantic_delta: {
        schema: 'pulse.semantic_delta.v1',
        source: {
          host: HOST, conversation_scope: 'current_turn', timestamp, session_id: context.session_id,
        },
        events: [{
          client_id: `emotion_${createHash('sha256').update([
            context.source_event_key, String(index), item.summary, emotion.label,
          ].join('\x1f')).digest('hex').slice(0, 24)}`,
          title: `Emotional moment: ${emotion.label}`,
          summary: item.summary,
          emotional_weight: emotion.intensity,
          confidence,
          privacy_tier: 'normal',
          emotions: { [emotion.label]: emotion.intensity },
          emotion_derivation: derivation,
          emotion_confidence: confidence,
          observed_label: emotion.label,
          ...(emotion.cause === undefined ? {} : {
            trigger: { summary: emotion.cause, derivation, confidence, confirmed: emotion.source === 'user' },
          }),
        }],
        raw_input_included: false,
      },
    };
  });
  return {
    schema: 'pulse.turn_finalize.v1',
    host: HOST,
    session_id: context.session_id,
    turn_id: context.turn_id,
    source_event_key: context.source_event_key,
    idempotency_key: context.idempotency_key,
    binding_digest: context.binding_digest,
    policy_epoch: context.policy_epoch,
    resolver_epoch: context.resolver_epoch,
    candidates,
  };
}

function compactWriteResult(result) {
  const receipts = Array.isArray(result?.receipts) ? result.receipts : [];
  if (typeof result?.ledger_id !== 'string' || !result.finalize_receipt || receipts.some((item) =>
    !item || typeof item !== 'object' || typeof item.receipt_id !== 'string')) {
    throw new Error('pulse_write_receipt_invalid');
  }
  const rejected = receipts.some((item) => ['rejected', 'failed', 'canceled'].includes(item.status));
  const ids = [...new Set(receipts.map((item) => item.object_id ?? item.candidate_id)
    .filter((id) => typeof id === 'string' && id.length > 0))].slice(0, 3);
  return { status: rejected ? 'rejected' : 'stored', ids };
}

export async function handleOpenCodeBridge(action, input, dependencies = {}) {
  const resolveRuntime = dependencies.resolveRuntime ?? ((value) => resolveBoundCodexRuntime(value, { host: HOST }));
  const request = dependencies.request ?? boundPulseRequest;
  const now = dependencies.now?.() ?? new Date();
  if (action === 'message') {
    const event = canonicalEvent(input);
    if (typeof input.query !== 'string' || input.query.trim() === '' ||
        Buffer.byteLength(input.query, 'utf8') > 64 * 1024 || input.query.includes('\u0000')) {
      throw new Error('opencode_query_invalid');
    }
    const resolved = resolveRuntime({ cwd: input.cwd });
    (dependencies.writeTurnContext ?? writeHostTurnContext)(resolved, event, HOST, now);
    let recalled = '';
    try {
      recalled = await (dependencies.composeMemory ?? composePromptMemoryContext)(resolved, input.query, {
        request,
        recordActivity: (activity) => request(resolved, '/memory/activity/recall', {
          body: activity, productHost: HOST, timeoutMs: 1_000,
        }),
      });
    } catch { /* recall is optional */ }
    return {
      schema: 'pulse.opencode_message.v1',
      context: `${recalled ? `${recalled}\n` : ''}${PERSONAL_AUTO_CAPTURE_CONTEXT}`,
      source_event_key: event.source_event_key,
      idempotency_key: event.idempotency_key,
    };
  }
  if (action === 'memory') {
    const validated = validateMemoryInput(input);
    const resolved = resolveRuntime({ cwd: process.cwd() });
    const event = {
      session_id: validated.session_id,
      turn_id: validated.turn_id,
      source_event_key: validated.source_event_key,
      idempotency_key: validated.idempotency_key,
    };
    const context = (dependencies.readTurnContext ?? readHostTurnContext)(resolved, event, HOST, now);
    (dependencies.writeToolLease ?? writeHostToolLease)(
      resolved, event, HOST, 'pulse_memory', { items: validated.items }, validated.tool_use_id, now,
    );
    (dependencies.consumeToolLease ?? consumeHostToolLease)(
      resolved, HOST, 'pulse_memory', { items: validated.items }, now,
    );
    const body = memoryFinalizeBody(validated, context, now);
    const result = await request(resolved, '/turn/finalize', {
      body, productHost: HOST, timeoutMs: 4_000, idempotencyKey: body.idempotency_key,
    });
    const compact = compactWriteResult(result);
    try { (dependencies.writeFinalizeMarker ?? writeHostFinalizeMarker)(resolved, event, HOST, result, now); }
    catch { /* the daemon receipt remains authoritative */ }
    return compact;
  }
  if (action === 'fun-fact-candidates') {
    const sessionID = stable(input?.session_id, 'opencode_session_invalid');
    const workspaceDigest = process.env.PULSE_WORKSPACE_DIGEST;
    const options = (dependencies.readOptions ?? readOpenCodeOptions)({
      productHome: process.env.PULSE_HOME,
      workspaceDigest,
    });
    if (options.fun_facts !== 'small-model') {
      return { schema: 'pulse.opencode_fun_fact_candidates.v1', enabled: false, candidates: [] };
    }
    const resolved = resolveRuntime({ cwd: process.cwd() });
    const response = await request(resolved, '/memory/fun-fact-candidates', {
      method: 'GET', productHost: HOST, timeoutMs: 1_000,
    });
    if (response?.schema !== 'pulse.opencode_fun_fact_candidates.v1' ||
        !Array.isArray(response.candidates) || response.candidates.length > 6 ||
        !/^[a-f0-9]{64}$/.test(response.candidate_digest ?? '') ||
        response.candidates.some((candidate) =>
          !/^fact_[a-f0-9]{24}$/.test(candidate?.id ?? '') || !safeFunFactText(candidate?.text)) ||
        new Set(response.candidates.map((candidate) => candidate.id)).size !== response.candidates.length ||
        new Set(response.candidates.map((candidate) => candidate.text)).size !== response.candidates.length) {
      throw new Error('opencode_fun_fact_candidates_invalid');
    }
    return {
      schema: response.schema,
      enabled: true,
      session_id: sessionID,
      candidates: response.candidates,
      candidate_digest: response.candidate_digest,
    };
  }
  if (action === 'fun-fact-receipt') {
    const sessionID = stable(input?.session_id, 'opencode_session_invalid');
    const usage = input.usage;
    const invalidUsage = usage !== undefined && (!usage || typeof usage !== 'object' || Array.isArray(usage) ||
      Object.keys(usage).sort().join('\0') !== ['input', 'output', 'total'].sort().join('\0') ||
      Object.values(usage).some((value) => !Number.isSafeInteger(value) || value < 0 || value > 10_000_000) ||
      usage.total !== usage.input + usage.output);
    if (typeof input.model !== 'string' || input.model.length < 1 || input.model.length > 255 ||
        !Number.isSafeInteger(input.latency_ms) || input.latency_ms < 0 || input.latency_ms > 120_000 ||
        !/^[a-f0-9]{64}$/.test(input.candidate_digest ?? '') ||
        !['selected', 'none', 'fallback', 'failed'].includes(input.outcome) ||
        invalidUsage) {
      throw new Error('opencode_fun_fact_receipt_invalid');
    }
    const resolved = resolveRuntime({ cwd: process.cwd() });
    const platformServices = dependencies.platformServices ?? defaultPlatformServices;
    const directory = `${resolved.runtime.data_dir}/runtime/opencode-fun-fact-receipts`;
    platformServices.ensurePrivateDirectory(resolved.runtime.data_dir);
    platformServices.ensurePrivateDirectory(`${resolved.runtime.data_dir}/runtime`);
    platformServices.ensurePrivateDirectory(directory);
    const name = createHash('sha256').update(`pulse-opencode-fun-fact-session-v1\x1f${sessionID}`).digest('hex');
    const receipt = {
      schema: 'pulse.opencode_fun_fact_receipt.v1',
      session_digest: name,
      model: input.model,
      latency_ms: input.latency_ms,
      ...(input.usage === undefined ? {} : { usage: input.usage }),
      candidate_digest: input.candidate_digest,
      outcome: input.outcome,
      created_at: now.toISOString(),
    };
    platformServices.atomicWritePrivateFile(
      `${directory}/${name}.json`, `${JSON.stringify(receipt)}\n`, { ensureParent: false, maxBytes: 4096 },
    );
    return { schema: receipt.schema, recorded: true };
  }
  throw new Error('opencode_bridge_action_invalid');
}

async function readInput(stream = process.stdin) {
  const chunks = [];
  let bytes = 0;
  for await (const chunk of stream) {
    bytes += chunk.length;
    if (bytes > MAX_INPUT_BYTES) throw new Error('opencode_bridge_input_too_large');
    chunks.push(chunk);
  }
  if (chunks.length === 0) throw new Error('opencode_bridge_input_missing');
  return JSON.parse(Buffer.concat(chunks).toString('utf8'));
}

export async function runOpenCodeBridgeCLI(action, dependencies = {}) {
  await (dependencies.recoverBinding ?? recoverWorkspaceBindingTransaction)();
  const input = dependencies.input ?? await readInput(dependencies.inputStream);
  const result = await handleOpenCodeBridge(action, input, dependencies);
  (dependencies.output ?? process.stdout).write(`${JSON.stringify(result)}\n`);
}

export const __opencodeBridgeTest = Object.freeze({ canonicalEvent, compactWriteResult, memoryFinalizeBody, validateMemoryInput });
