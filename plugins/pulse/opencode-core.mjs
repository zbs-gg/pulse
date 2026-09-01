import { createHash } from 'node:crypto';

const CONTROL = /[\u0000-\u001f\u007f-\u009f]/;

function stableID(value, prefix) {
  if (typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$/.test(value)) return value;
  return `${prefix}_${createHash('sha256').update(String(value ?? '')).digest('hex')}`;
}

function promptText(parts) {
  if (!Array.isArray(parts)) return '';
  const text = parts
    .filter((part) => part?.type === 'text' && part.synthetic !== true && typeof part.text === 'string')
    .map((part) => part.text)
    .join('\n')
    .trim();
  if (text.length === 0 || Buffer.byteLength(text, 'utf8') > 64 * 1024 || CONTROL.test(text)) return '';
  return text;
}

function clearSession(state, sessionID) {
  if (typeof sessionID !== 'string') return;
  state.pending.delete(sessionID);
  state.turns.delete(sessionID);
  state.funFacts.delete(sessionID);
  state.funFactChecked.delete(sessionID);
  state.turnCounters.delete(sessionID);
}

function settleSession(state, sessionID) {
  if (typeof sessionID !== 'string') return;
  state.pending.delete(sessionID);
  state.turns.delete(sessionID);
}

function responseData(value) {
  if (value?.error) throw new Error('opencode_sdk_error');
  return value?.data ?? value;
}

function modelReference(value) {
  if (typeof value !== 'string') return undefined;
  const slash = value.indexOf('/');
  if (slash < 1 || slash === value.length - 1) return undefined;
  return { providerID: value.slice(0, slash), modelID: value.slice(slash + 1) };
}

function provablyCheaper(candidate, main) {
  const candidateInput = Number(candidate?.cost?.input);
  const candidateOutput = Number(candidate?.cost?.output);
  const mainInput = Number(main?.cost?.input);
  const mainOutput = Number(main?.cost?.output);
  return [candidateInput, candidateOutput, mainInput, mainOutput].every(Number.isFinite) &&
    candidateInput <= mainInput && candidateOutput <= mainOutput &&
    (candidateInput < mainInput || candidateOutput < mainOutput);
}

export async function selectSmallModel(client, directory, main, explicit) {
  const configured = modelReference(explicit);
  if (configured) return configured;
  if (!main?.providerID || !main?.modelID || !client?.config?.providers) return undefined;
  let providers;
  try {
    providers = responseData(await client.config.providers({ query: { directory } }))?.providers;
  } catch { return undefined; }
  if (!Array.isArray(providers)) return undefined;
  const provider = providers.find((item) => item?.id === main.providerID);
  const mainModel = provider?.models?.[main.modelID];
  if (!mainModel) return undefined;
  const candidates = Object.values(provider.models ?? {}).filter((model) =>
    model?.id !== main.modelID && model?.status === 'active' &&
    model?.capabilities?.output?.text === true && provablyCheaper(model, mainModel));
  candidates.sort((left, right) =>
    (left.cost.input + left.cost.output) - (right.cost.input + right.cost.output) ||
    left.id.localeCompare(right.id));
  return candidates[0] ? { providerID: provider.id, modelID: candidates[0].id } : undefined;
}

function usageReceipt(info) {
  const input = Number(info?.tokens?.input);
  const output = Number(info?.tokens?.output);
  if (![input, output].every((value) => Number.isSafeInteger(value) && value >= 0)) return undefined;
  return { input, output, total: input + output };
}

function withTimeout(promise, timeoutMs) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('small_model_timeout')), timeoutMs);
    Promise.resolve(promise).then(
      (value) => { clearTimeout(timer); resolve(value); },
      (error) => { clearTimeout(timer); reject(error); },
    );
  });
}

async function selectSessionFunFact(client, directory, state, parentSessionID, mainModel, bridge) {
  const fact = state.funFacts.get(parentSessionID);
  if (!fact || fact.started || fact.candidates.length === 0) return;
  fact.started = true;
  const started = Date.now();
  let model;
  let serviceSessionID;
  let outcome = 'fallback';
  try {
    model = await selectSmallModel(client, directory, mainModel, state.config.smallModel);
    if (!model || !client?.session?.create || !client?.session?.prompt) throw new Error('small_model_unavailable');
    const created = responseData(await client.session.create({
      body: { title: 'Pulse fun fact selection' }, query: { directory },
    }));
    serviceSessionID = created?.id;
    if (typeof serviceSessionID !== 'string') throw new Error('small_model_session_invalid');
    state.serviceSessions.add(serviceSessionID);
    const candidateBlock = fact.candidates.map((candidate) => `${candidate.id}\t${candidate.text}`).join('\n');
    const promptPromise = client.session.prompt({
      path: { id: serviceSessionID }, query: { directory },
      body: {
        model,
        tools: { '*': false },
        system: 'Select one supplied fact ID suitable as a brief low-stakes session greeting. Return exactly that ID or none. Never rewrite or invent a fact.',
        parts: [{
          type: 'text',
          text: `Candidates (ID and verbatim text only):\n${candidateBlock}\nReturn one ID or none.`,
        }],
      },
    });
    const response = responseData(await withTimeout(promptPromise, 2_000));
    fact.usage = usageReceipt(response?.info);
    const answer = (response?.parts ?? []).filter((part) => part?.type === 'text')
      .map((part) => part.text).join('').trim();
    if (fact.locked) {
      outcome = 'fallback';
    } else if (answer === 'none') {
      fact.selectedID = 'none';
      outcome = 'none';
    } else if (fact.candidates.some((candidate) => candidate.id === answer)) {
      fact.selectedID = answer;
      outcome = 'selected';
    } else throw new Error('small_model_output_invalid');
  } catch {
    outcome = model ? 'failed' : 'fallback';
  } finally {
    const modelID = model ? `${model.providerID}/${model.modelID}` : 'deterministic';
    bridge('fun-fact-receipt', {
      session_id: parentSessionID,
      model: modelID,
      latency_ms: Math.max(0, Date.now() - started),
      ...(fact.usage ? { usage: fact.usage } : {}),
      candidate_digest: fact.candidateDigest,
      outcome,
    }, { timeoutMs: 1_000 }).catch(() => {});
    if (serviceSessionID) {
      client.session.abort?.({ path: { id: serviceSessionID }, query: { directory } }).catch(() => {});
      client.session.delete?.({ path: { id: serviceSessionID }, query: { directory } }).catch(() => {});
      state.serviceSessions.delete(serviceSessionID);
    }
  }
}

export function pulseMemoryTool(tool, bridge, state) {
  const emotion = tool.schema.object({
    label: tool.schema.enum([
      'joy', 'sadness', 'anger', 'fear', 'trust', 'disgust',
      'anticipation', 'surprise', 'shame', 'guilt',
    ]),
    intensity: tool.schema.number().min(0).max(1),
    source: tool.schema.enum(['user', 'inferred']),
    cause: tool.schema.string().min(1).max(240).optional(),
  });
  return tool({
    description: 'Save only a durable result from this normal turn. Never save raw wording, secrets, credentials, paths, transcripts, or temporary instructions. Personal memory follows the person; project memory stays in this project. An inferred emotion is only a fading hypothesis about this moment.',
    args: {
      items: tool.schema.array(tool.schema.object({
        kind: tool.schema.enum(['decision', 'preference', 'open_loop', 'project_state', 'correction', 'emotion']),
        scope: tool.schema.enum(['personal', 'project']),
        summary: tool.schema.string().min(1).max(400),
        emotion: emotion.optional(),
      })).min(1).max(3),
    },
    async execute(args, context) {
      const turn = state.turns.get(context.sessionID);
      if (!turn) return JSON.stringify({ status: 'unavailable' });
      try {
        const result = await bridge('memory', {
          ...turn,
          session_id: context.sessionID,
          items: args.items,
          tool_use_id: stableID(context.messageID, 'tool'),
        }, { signal: context.abort, timeoutMs: 8_000 });
        return JSON.stringify(result);
      } catch {
        return JSON.stringify({ status: 'unavailable' });
      }
    },
  });
}

export function createPulseOpenCodeHooks({ directory, client, tool, bridge }) {
  if (typeof directory !== 'string' || typeof tool !== 'function' || typeof bridge !== 'function') {
    throw new TypeError('opencode_plugin_dependencies_invalid');
  }
  const state = {
    pending: new Map(), turns: new Map(), turnCounters: new Map(),
    funFacts: new Map(), funFactChecked: new Set(),
    serviceSessions: new Set(),
    config: { smallModel: undefined },
  };
  return {
    tool: { pulse_memory: pulseMemoryTool(tool, bridge, state) },
    config: async (config) => {
      state.config.smallModel = typeof config?.small_model === 'string' ? config.small_model : undefined;
    },
    'chat.message': async (input, output) => {
      const query = promptText(output?.parts);
      if (!query || typeof input?.sessionID !== 'string') return;
      const sessionID = stableID(input.sessionID, 'session');
      if (state.serviceSessions.has(sessionID)) return;
      const turnSequence = (state.turnCounters.get(sessionID) ?? 0) + 1;
      state.turnCounters.set(sessionID, turnSequence);
      const turnID = typeof input.messageID === 'string'
        ? stableID(input.messageID, 'turn')
        : `turn_${createHash('sha256').update(`pulse-opencode-turn-v1\x1f${sessionID}\x1f${turnSequence}`).digest('hex')}`;
      try {
        const candidatesPromise = state.funFactChecked.has(sessionID)
          ? Promise.resolve()
          : bridge('fun-fact-candidates', { session_id: sessionID }, { timeoutMs: 1_500 })
          .then((facts) => {
            if (facts?.enabled !== true || !Array.isArray(facts.candidates) || facts.candidates.length === 0) return;
            state.funFacts.set(sessionID, {
              candidates: facts.candidates,
              candidateDigest: facts.candidate_digest,
              selectedID: undefined,
              presented: false,
              locked: false,
              started: false,
            });
            selectSessionFunFact(client, directory, state, sessionID, input.model, bridge).catch(() => {});
          }).catch(() => {});
        state.funFactChecked.add(sessionID);
        const resultPromise = bridge('message', {
          session_id: sessionID,
          turn_id: turnID,
          cwd: directory,
          model: input.model ? `${input.model.providerID}/${input.model.modelID}` : 'opencode_model_unavailable',
          query,
        }, { timeoutMs: 4_000 });
        const result = await resultPromise;
        await candidatesPromise;
        if (result?.schema !== 'pulse.opencode_message.v1' || typeof result.context !== 'string') return;
        const turn = {
          turn_id: turnID,
          source_event_key: result.source_event_key,
          idempotency_key: result.idempotency_key,
        };
        state.turns.set(sessionID, turn);
        state.pending.set(sessionID, { context: result.context, injected: false });
      } catch {
        // Optional memory must never block a normal OpenCode response.
      }
    },
    'experimental.chat.system.transform': async (input, output) => {
      if (typeof input?.sessionID !== 'string') return;
      const sessionID = stableID(input.sessionID, 'session');
      if (state.serviceSessions.has(sessionID)) return;
      const pending = state.pending.get(sessionID);
      if (!pending || pending.injected || pending.context === '') return;
      output.system.push(pending.context);
      const fact = state.funFacts.get(sessionID);
      if (fact && !fact.presented) {
        const selected = fact.selectedID === 'none'
          ? undefined
          : fact.candidates.find((candidate) => candidate.id === fact.selectedID) ?? fact.candidates[0];
        fact.locked = true;
        fact.presented = true;
        if (selected) {
          output.system.push(`Pulse session fact (verbatim approved local candidate; do not rewrite or invent): ${selected.text}`);
        }
      }
      pending.injected = true;
      state.pending.delete(sessionID);
    },
    'chat.params': async (input, output) => {
      if (state.serviceSessions.has(input?.sessionID)) output.maxOutputTokens = 32;
    },
    event: async ({ event }) => {
      if (!['session.idle', 'session.error', 'session.deleted'].includes(event?.type)) return;
      const sessionID = event.properties?.sessionID ?? event.properties?.info?.id;
      if (state.serviceSessions.has(sessionID)) {
        state.serviceSessions.delete(sessionID);
        return;
      }
      if (event.type === 'session.deleted') clearSession(state, sessionID);
      else settleSession(state, sessionID);
    },
  };
}

export const __opencodeCoreTest = Object.freeze({ promptText, stableID });
