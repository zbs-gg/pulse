import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { isAbsolute, join, normalize } from 'node:path';

const STABLE_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$/;
const CONTROL = /[\u0000-\u001f\u007f-\u009f]/;
const AUTHORITY_FIELDS = new Set([
  'audience', 'principal', 'role', 'scope', 'team_id', 'vault', 'visibility', 'workspace',
]);
const RECEIPT_STATUSES = new Set([
  'pending', 'created', 'updated', 'deduplicated', 'canceled', 'rejected', 'failed',
]);

const HOST_EVENTS = new Map([
  ['SessionStart', 'session_start'],
  ['UserPromptSubmit', 'turn_start'],
  ['PreToolUse', 'turn_start'],
  ['PostToolUse', 'tool_receipt'],
  ['PreCompact', 'pre_compact'],
  ['PostCompact', 'session_resume'],
  ['SubagentStart', 'subagent_start'],
  ['SubagentStop', 'subagent_stop'],
  ['Stop', 'turn_finalize'],
]);

function record(value, code) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(code);
  return value;
}

function safeString(value, code, { stable = false } = {}) {
  if (typeof value !== 'string' || value.length === 0 || value.trim() !== value || CONTROL.test(value)) {
    throw new Error(code);
  }
  if (stable && !STABLE_ID.test(value)) throw new Error(code);
  return value;
}

function digest(label, ...parts) {
  const hash = createHash('sha256');
  hash.update(label);
  for (const part of parts) {
    hash.update('\x1f');
    hash.update(String(part));
  }
  return hash.digest('hex');
}

function eventSource(eventName, input) {
  if (eventName === 'SessionStart') return safeString(input.source, 'invalid_source', { stable: true });
  if (eventName === 'PreCompact' || eventName === 'PostCompact') {
    return safeString(input.trigger, 'invalid_trigger', { stable: true });
  }
  if (eventName === 'PreToolUse' || eventName === 'PostToolUse') {
    return safeString(input.tool_name, 'invalid_tool_name');
  }
  if (eventName === 'SubagentStart' || eventName === 'SubagentStop') {
    return safeString(input.agent_type, 'invalid_agent_type', { stable: true });
  }
  if (eventName === 'UserPromptSubmit') return 'prompt_submitted';
  return 'stop';
}

function normalizeHostHook(host, eventName, rawInput) {
  const event = HOST_EVENTS.get(eventName);
  if (!event) throw new Error(`unsupported_${host.replaceAll('-', '_')}_hook`);
  const input = record(rawInput, `invalid_${host.replaceAll('-', '_')}_hook_input`);
  if (input.hook_event_name !== eventName) throw new Error(`${host.replaceAll('-', '_')}_hook_event_mismatch`);
  for (const field of Object.keys(input).map((item) => item.trim().toLowerCase())) {
    if (AUTHORITY_FIELDS.has(field)) throw new Error(`authority_field_forbidden:${field}`);
  }
  const sessionID = safeString(input.session_id, 'invalid_session_id', { stable: true });
  const cwd = safeString(input.cwd, 'invalid_workspace');
  if (!isAbsolute(cwd)) throw new Error('invalid_workspace');
  const workspace = normalize(cwd);
  const model = safeString(input.model, 'invalid_model');
  const source = eventSource(eventName, input);
  const turnID = input.turn_id === undefined && eventName === 'SessionStart'
    ? `session_${digest('pulse-thread-scoped-turn-v1', host, sessionID, workspace, source)}`
    : safeString(input.turn_id, 'invalid_turn_id', { stable: true });
  const stopHookActive = input.stop_hook_active ?? false;
  if (typeof stopHookActive !== 'boolean') throw new Error('invalid_stop_hook_active');
  const agentID = input.agent_id === undefined ? '' : safeString(input.agent_id, 'invalid_agent_id', { stable: true });
  const sourceDigest = digest(
    `pulse-${host}-source-event-v1`, event, sessionID, turnID, workspace, source, agentID,
  );
  const normalized = {
    schema: 'pulse.lifecycle_event.v1',
    host,
    native_event: eventName,
    event,
    session_id: sessionID,
    turn_id: turnID,
    workspace,
    model,
    source,
    stop_hook_active: stopHookActive,
    source_event_key: `event_${sourceDigest}`,
  };
  normalized.idempotency_key = lifecycleIdempotencyKey(normalized);
  return normalized;
}

export function normalizeCodexHook(eventName, rawInput) {
  return normalizeHostHook('codex', eventName, rawInput);
}

export function normalizeClaudeHook(eventName, rawInput) {
  const input = {
    ...record(rawInput, 'invalid_claude_code_hook_input'),
    model: rawInput?.model ?? 'claude_model_unavailable',
  };
  if (eventName !== 'SessionStart') {
    input.turn_id = safeString(rawInput?.prompt_id, 'invalid_prompt_id', { stable: true });
  }
  return normalizeHostHook('claude-code', eventName, input);
}

export function lifecycleIdempotencyKey(event) {
  const material = [
    event.schema, event.host, event.event, event.session_id, event.turn_id,
    event.workspace, event.source,
  ].join('\x1f');
  return `lifecycle:${createHash('sha256').update(material).digest('hex')}`;
}

function candidateReceipt(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value) ||
      value.schema !== 'pulse.write_receipt.v1' || !STABLE_ID.test(value.receipt_id ?? '') ||
      !STABLE_ID.test(value.ledger_id ?? '') || !RECEIPT_STATUSES.has(value.status)) {
    return undefined;
  }
  const out = {
    receipt_id: value.receipt_id,
    ledger_id: value.ledger_id,
    status: value.status,
  };
  for (const field of ['candidate_id', 'object_id']) {
    if (value[field] !== undefined) {
      if (!STABLE_ID.test(value[field])) return undefined;
      out[field] = value[field];
    }
  }
  return out;
}

function receiptRoots(response) {
  const roots = [];
  if (typeof response === 'string') {
    try { roots.push(JSON.parse(response)); } catch { return roots; }
  } else if (response && typeof response === 'object') {
    roots.push(response);
  }
  if (response && typeof response === 'object' && Array.isArray(response.content)) {
    for (const block of response.content) {
      if (block?.type !== 'text' || typeof block.text !== 'string') continue;
      try { roots.push(JSON.parse(block.text)); } catch { /* non-JSON MCP output is not a receipt */ }
    }
  }
  return roots;
}

export function extractPulseReceiptRefs(response) {
  const refs = [];
  const seen = new Set();
  for (const root of receiptRoots(response)) {
    const candidates = Array.isArray(root?.receipts) ? root.receipts : [root];
    for (const candidate of candidates) {
      const ref = candidateReceipt(candidate);
      if (!ref || seen.has(ref.receipt_id)) continue;
      seen.add(ref.receipt_id);
      refs.push(ref);
    }
  }
  return refs.slice(0, 20);
}

export function renderPulseContext(evidence, practices) {
  if (!Array.isArray(evidence) || !Array.isArray(practices) ||
      evidence.some((item) => typeof item !== 'string') ||
      practices.some((item) => typeof item !== 'string')) {
    throw new Error('invalid_pulse_context');
  }
  return JSON.stringify({ schema: 'pulse.context.v1', evidence, practices });
}

export function contextLease(binding, now, ttlMs = 30_000) {
  if (!binding || !/^[a-f0-9]{64}$/.test(binding.binding_digest ?? '') ||
      !Number.isSafeInteger(binding.resolver_epoch) || binding.resolver_epoch < 1) {
    throw new Error('invalid_binding_lease_source');
  }
  return {
    schema: 'pulse.context_lease.v1',
    binding_digest: `sha256:${binding.binding_digest}`,
    policy_epoch: 0,
    membership_generation: 0,
    object_generation: 0,
    expires_at: new Date(now.valueOf() + ttlMs).toISOString(),
  };
}

export function isGuardedCodexTool(toolName) {
  if (typeof toolName !== 'string') return false;
  if (/^mcp__/i.test(toolName)) return true;
  if (/^(Bash|apply_patch|Write|Edit|WebSearch|WebFetch|web|browser|ComputerUse)$/i.test(toolName)) return true;
  return /(?:^|__)(?:create|update|delete|remove|forget|wipe|send|post|write|merge|approve|upload|publish|execute|run|navigate)(?:_|$)/i.test(toolName);
}

export function isDestructivePulseTool(toolName) {
  return typeof toolName === 'string' &&
    /(?:^|__)pulse_(?:forget|wipe)(?:_|$)/i.test(toolName);
}

export function isDestructivePulseShellInvocation(toolName, toolInput) {
  if (!/^Bash$/i.test(toolName ?? '') || typeof toolInput?.command !== 'string') return false;
  const command = toolInput.command;
  // Deliberately broad: wrappers such as `script`, `env`, `sudo`, `exec`, or
  // `node /path/cli.js` must not turn an agent command into user presence.
  // False-positive denial is safer than allowing a destructive invocation.
  return /(?:^|[\s'";&|()])(?:[^\s'";&|()]*\/)?(?:pulse|cli\.js)\s+(?:wipe|delete)(?=$|[\s'";&|()])/i.test(command) ||
    /\/(?:memory\/wipe|memory\/delete)(?:\s|[?'"\\]|$)/i.test(command) ||
    /(?:^|[\s'"=])(?:~\/\.pulse|\$HOME\/\.pulse|\/[^\s'";|]+\/\.pulse)\/secret\.key(?:[\s'";|]|$)/i.test(command);
}

export function isTrustedPulseProductTool(toolName) {
  return typeof toolName === 'string' &&
    /^mcp__(?:pulse-product|pulse)__pulse_(?:remember|graph_delta|tray|tray_status)$/i.test(toolName);
}

export function hookBundleDigest(bytes) {
  return createHash('sha256').update(bytes).digest('hex');
}

export function codexHookExecutionDigest(pluginRoot, runtimePath) {
  const hash = createHash('sha256');
	for (const relative of [
		'.codex-plugin/plugin.json', '.mcp.json', 'runtime-locator.mjs', 'hooks/hooks.json', 'hooks/pulse-hook.mjs', 'mcp/server.mjs',
	]) {
    hash.update(relative);
    hash.update('\x00');
    hash.update(readFileSync(join(pluginRoot, relative)));
    hash.update('\x00');
  }
	const runtime = JSON.parse(readFileSync(join(normalize(runtimePath), '..', '..', 'runtime-manifest.json'), 'utf8'));
	if (runtime?.schema !== 'pulse.codex_runtime.v2' || !/^[a-f0-9]{64}$/.test(runtime.tree_digest ?? '')) {
    throw new Error('Codex runtime manifest is invalid');
  }
  hash.update('runtime-tree-digest\x00');
  hash.update(runtime.tree_digest);
  return hash.digest('hex');
}

export function validateHookReadiness(source, receipt, expected = {}) {
  const current = typeof source === 'string' && /^[a-f0-9]{64}$/.test(source)
    ? source
    : hookBundleDigest(source);
  if (!receipt || receipt.schema !== 'pulse.codex_hook_readiness.v2' ||
      receipt.hooks_digest !== current || !HOST_EVENTS.has(receipt.last_event) ||
      !/^[a-f0-9]{64}$/.test(receipt.binding_digest ?? '') ||
      !Number.isSafeInteger(receipt.resolver_epoch) ||
      typeof receipt.repository_id !== 'string' ||
      !/^[a-f0-9]{64}$/.test(receipt.workspace_digest ?? '') ||
		  !/^[a-f0-9]{64}$/.test(receipt.session_proof ?? '') ||
      !/^[a-f0-9]{64}$/.test(receipt.turn_proof ?? '') ||
		  !['session_context', 'prompt_context', 'write_receipt', 'turn_finalize'].every((name) =>
        typeof receipt.milestones?.[name] === 'string' && !Number.isNaN(Date.parse(receipt.milestones[name]))) ||
      Object.entries(expected).some(([name, value]) => value !== undefined && receipt[name] !== value) ||
      typeof receipt.observed_at !== 'string' || Number.isNaN(Date.parse(receipt.observed_at))) {
    return { ready: false, hooks_digest: current, reason: 'hook_lifecycle_receipt_required' };
  }
	return { ready: true, hooks_digest: current, reason: 'hook_lifecycle_observed_after_trust' };
}

function legacyPulseHookCommand(command) {
  return typeof command === 'string' &&
    /(?:^|\s)(?:pulse|[^\s]+\/cli\.js)(?:'|")?\s+hook\s+(?:session-start|user-prompt-submit|post-tool-use|stop)(?:\s|$)/.test(command) &&
    !command.includes('${PLUGIN_ROOT}/hooks/pulse-hook.mjs');
}

export function migrateLegacyPulseHookConfig(rawConfig) {
  const config = structuredClone(rawConfig ?? {});
  let removed = 0;
  if (!config.hooks || typeof config.hooks !== 'object' || Array.isArray(config.hooks)) {
    return { config, removed };
  }
  for (const [event, entries] of Object.entries(config.hooks)) {
    if (!Array.isArray(entries)) continue;
    const keptEntries = [];
    for (const entry of entries) {
      if (!entry || typeof entry !== 'object' || !Array.isArray(entry.hooks)) {
        keptEntries.push(entry);
        continue;
      }
      const hooks = entry.hooks.filter((handler) => {
        if (legacyPulseHookCommand(handler?.command)) {
          removed += 1;
          return false;
        }
        return true;
      });
      if (hooks.length > 0) keptEntries.push({ ...entry, hooks });
    }
    if (keptEntries.length > 0) config.hooks[event] = keptEntries;
    else delete config.hooks[event];
  }
  if (Object.keys(config.hooks).length === 0) delete config.hooks;
  return { config, removed };
}
