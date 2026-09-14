import { validateMomentItems, memoryMomentBody, compactMomentResult, writeMemoryMoment } from './memory-moment.js';
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
const MAX_INPUT_BYTES = 16 * 1024 * 1024;
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
      ].includes(key)) || !Array.isArray(value.items) || value.items.length < 1) {
    throw new Error('pulse_memory_input_invalid');
  }
  const items = validateMomentItems(value.items);
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
  return memoryMomentBody(input, {...context, host:HOST}, now);
}
function compactWriteResult(result, expected) { return compactMomentResult(result, expected); }

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
  if (action === 'moment') {
    if (!/^moment:[a-f0-9]{64}$/.test(input.moment_id??'') || !Number.isSafeInteger(input.cursor??0) || (input.cursor??0)<0) throw new Error('invalid_moment_read');
    const event = {
      session_id: stable(input.session_id, 'opencode_session_invalid'),
      turn_id: stable(input.turn_id, 'opencode_turn_invalid'),
      source_event_key: input.source_event_key, idempotency_key: input.idempotency_key,
    };
    const resolved=resolveRuntime({cwd:process.cwd()});
    (dependencies.readTurnContext ?? readHostTurnContext)(resolved,event,HOST,now);
    const query={moment_id:input.moment_id,cursor:input.cursor??0,status:input.status===true};
    (dependencies.writeToolLease ?? writeHostToolLease)(resolved,event,HOST,'pulse_memory',query,input.tool_use_id,now);
    (dependencies.consumeToolLease ?? consumeHostToolLease)(resolved,HOST,'pulse_memory',query,now);
    return request(resolved,`/memory/moments/${query.moment_id}?cursor=${query.cursor}&status=${query.status}`,{method:'GET',productHost:HOST,timeoutMs:2500});
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
    const result = await writeMemoryMoment((path, options) => request(resolved,path,{...options,productHost:HOST}),body);
    const compact = compactWriteResult(result,body.candidates.length);
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
