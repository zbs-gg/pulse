import { createHash } from 'node:crypto';
import {
  existsSync,
  lstatSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  renameSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { dirname, join } from 'node:path';

import { resolveWorkspaceBinding } from './workspace-binding.js';
import { vaultRuntimeFromBinding } from './local-supervisor.js';

const STABLE_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$/;

function canonicalJSON(value) {
  if (value === null || typeof value !== 'object') return JSON.stringify(value);
  if (Array.isArray(value)) return `[${value.map((item) => canonicalJSON(item)).join(',')}]`;
  return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalJSON(value[key])}`).join(',')}}`;
}

function codexToolInputDigest(toolName, toolInput) {
  return createHash('sha256')
    .update('pulse-codex-tool-input-v1\x1f')
    .update(toolName)
    .update('\x1f')
    .update(canonicalJSON(toolInput))
    .digest('hex');
}

export function resolveCodexRuntime(input = {}) {
  const cwd = typeof input === 'string' ? input : input.cwd;
  const options = { cwd };
  if (process.env.PULSE_BINDING_REGISTRY_PATH) options.registryPath = process.env.PULSE_BINDING_REGISTRY_PATH;
  if (process.env.PULSE_BINDING_PUBLIC_KEY_PATH) options.publicKeyPath = process.env.PULSE_BINDING_PUBLIC_KEY_PATH;
  const binding = resolveWorkspaceBinding(options);
  const runtime = vaultRuntimeFromBinding(binding);
  return { binding, runtime };
}

export function resolveBoundCodexRuntime(input = {}) {
  const resolved = resolveCodexRuntime(input);
  const { runtime } = resolved;
  const capturePath = join(runtime.data_dir, 'capture-state.json');
  if (!existsSync(capturePath)) throw new Error('capture_state_missing');
  const capture = JSON.parse(readFileSync(capturePath, 'utf8'));
  if (capture?.schema !== 'pulse.capture_state.v1' || capture.enabled !== true) {
    throw new Error('capture_disabled');
  }
  return resolved;
}

export function readRuntimeSecret(runtime) {
  const path = join(runtime.data_dir, 'secret.key');
  const info = lstatSync(path);
  const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
  if (!info.isFile() || info.isSymbolicLink() || info.uid !== currentUID ||
      (info.mode & 0o077) !== 0 || info.size !== 64) {
    throw new Error('vault_secret_unsafe');
  }
  const secret = readFileSync(path, 'utf8');
  if (!/^[a-f0-9]{64}$/.test(secret)) throw new Error('vault_secret_invalid');
  return secret;
}

export async function boundPulseRequest(resolved, path, options = {}) {
  const method = options.method ?? 'POST';
  const headers = {
    Accept: 'application/json',
    'X-Pulse-Key': readRuntimeSecret(resolved.runtime),
  };
  if (method !== 'GET') {
    headers['Content-Type'] = 'application/json';
    if (options.idempotencyKey) headers['Idempotency-Key'] = options.idempotencyKey;
  }
  const response = await fetch(`${resolved.runtime.base_url}${path}`, {
    method,
    headers,
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    signal: AbortSignal.timeout(options.timeoutMs ?? 2500),
  });
  const text = await response.text();
  if (!response.ok) {
    const error = new Error(`pulse_http_${response.status}:${text.slice(0, 160)}`);
    error.status = response.status;
    throw error;
  }
  if (response.status === 204 || text === '') return { ok: true };
  try { return JSON.parse(text); } catch { throw new Error('pulse_response_invalid'); }
}

export function codexTurnContextPath(dataDir, sessionID) {
  if (!STABLE_ID.test(sessionID ?? '')) throw new Error('invalid_session_id');
  const digest = createHash('sha256').update('pulse-codex-turn-context-v1\x1f').update(sessionID).digest('hex');
  return join(dataDir, 'codex-turn-context', `${digest}.json`);
}

export function codexFinalizeMarkerPath(dataDir, sessionID, turnID) {
  if (!STABLE_ID.test(sessionID ?? '') || !STABLE_ID.test(turnID ?? '')) {
    throw new Error('invalid_turn_identity');
  }
  const digest = createHash('sha256')
    .update('pulse-codex-finalize-marker-v1\x1f')
    .update(sessionID)
    .update('\x1f')
    .update(turnID)
    .digest('hex');
  return join(dataDir, 'codex-turn-finalized', `${digest}.json`);
}

function atomicWriteJSON(path, value) {
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  const temporary = `${path}.${process.pid}.${Date.now()}.new`;
  try {
    writeFileSync(temporary, `${JSON.stringify(value)}\n`, { mode: 0o600, flag: 'wx' });
    renameSync(temporary, path);
  } finally {
    rmSync(temporary, { force: true });
  }
}

export function writeCodexTurnContext(resolved, event, now = new Date()) {
  const context = {
    schema: 'pulse.codex_turn_context.v1',
    host: 'codex',
    session_id: event.session_id,
    turn_id: event.turn_id,
    workspace: resolved.binding.workspace.canonical_path,
    source_event_key: event.source_event_key,
    idempotency_key: event.idempotency_key,
    binding_digest: resolved.binding.binding_digest,
    policy_epoch: 0,
    resolver_epoch: resolved.binding.resolver_epoch,
    expires_at: new Date(now.valueOf() + 6 * 60 * 60 * 1000).toISOString(),
  };
  atomicWriteJSON(codexTurnContextPath(resolved.runtime.data_dir, event.session_id), context);
  return context;
}

export function writeCodexToolLease(resolved, event, toolName, toolInput, toolUseID, now = new Date()) {
  if (!STABLE_ID.test(toolUseID ?? '') ||
      toolName !== 'mcp__pulse-product__pulse_remember') {
    throw new Error('invalid_codex_tool_lease');
  }
  const lease = {
    schema: 'pulse.codex_tool_lease.v1',
    host: 'codex',
    session_id: event.session_id,
    turn_id: event.turn_id,
    workspace: resolved.binding.workspace.canonical_path,
    source_event_key: event.source_event_key,
    idempotency_key: event.idempotency_key,
    binding_digest: resolved.binding.binding_digest,
    policy_epoch: 0,
    resolver_epoch: resolved.binding.resolver_epoch,
    tool_name: toolName,
    tool_input_digest: codexToolInputDigest(toolName, toolInput),
    issued_at: now.toISOString(),
    expires_at: new Date(now.valueOf() + 30_000).toISOString(),
  };
  const name = createHash('sha256')
    .update('pulse-codex-tool-lease-v1\x1f')
    .update(event.source_event_key)
    .update('\x1f')
    .update(toolUseID)
    .digest('hex');
  atomicWriteJSON(join(
    resolved.runtime.data_dir, 'codex-tool-leases', lease.tool_input_digest, `${name}.json`,
  ), lease);
  return lease;
}

export function consumeCodexToolLease(resolved, toolName, toolInput, now = new Date()) {
  const inputDigest = codexToolInputDigest(toolName, toolInput);
  const directory = join(resolved.runtime.data_dir, 'codex-tool-leases', inputDigest);
  if (!existsSync(directory)) throw new Error('codex_tool_lease_unavailable');
  const directoryInfo = lstatSync(directory);
  const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : directoryInfo.uid;
  if (!directoryInfo.isDirectory() || directoryInfo.isSymbolicLink() || directoryInfo.uid !== currentUID ||
      (directoryInfo.mode & 0o077) !== 0) {
    throw new Error('codex_tool_lease_directory_unsafe');
  }
  const matches = [];
  for (const name of readdirSync(directory)) {
    if (!/^[a-f0-9]{64}\.json$/.test(name)) continue;
    const path = join(directory, name);
    let info;
    try { info = lstatSync(path); } catch { continue; }
    if (!info.isFile() || info.isSymbolicLink() || info.uid !== currentUID ||
        (info.mode & 0o077) !== 0 || info.size > 8192) continue;
    let lease;
    try { lease = JSON.parse(readFileSync(path, 'utf8')); } catch {
      rmSync(path, { force: true });
      continue;
    }
    const expiry = Date.parse(lease?.expires_at);
    const issued = Date.parse(lease?.issued_at);
    if (!Number.isNaN(expiry) && expiry <= now.valueOf()) {
      rmSync(path, { force: true });
      continue;
    }
    if (lease?.schema !== 'pulse.codex_tool_lease.v1' || lease.host !== 'codex' ||
        lease.workspace !== resolved.binding.workspace.canonical_path ||
        lease.binding_digest !== resolved.binding.binding_digest || lease.policy_epoch !== 0 ||
        lease.resolver_epoch !== resolved.binding.resolver_epoch || lease.tool_name !== toolName ||
        lease.tool_input_digest !== inputDigest || !STABLE_ID.test(lease.session_id ?? '') ||
        !STABLE_ID.test(lease.turn_id ?? '') || !/^event_[a-f0-9]{64}$/.test(lease.source_event_key ?? '') ||
        !/^lifecycle:[a-f0-9]{64}$/.test(lease.idempotency_key ?? '') ||
        Number.isNaN(issued) || issued > now.valueOf() + 5_000 || Number.isNaN(expiry)) continue;
    matches.push({ path, lease, issued });
  }
  if (matches.length === 0) throw new Error('codex_tool_lease_unavailable');
  const turnKey = (match) => [
    match.lease.session_id, match.lease.turn_id, match.lease.source_event_key,
    match.lease.idempotency_key,
  ].join('\x1f');
  if (new Set(matches.map(turnKey)).size !== 1) throw new Error('codex_tool_lease_ambiguous');
  matches.sort((left, right) => right.issued - left.issued || right.path.localeCompare(left.path));
  const selected = matches[0];
  const consumed = `${selected.path}.${process.pid}.${Date.now()}.consumed`;
  try {
    renameSync(selected.path, consumed);
    for (const stale of matches.slice(1)) rmSync(stale.path, { force: true });
    return selected.lease;
  } finally {
    rmSync(consumed, { force: true });
  }
}

export function readCodexTurnContext(resolved, event, now = new Date()) {
  const path = codexTurnContextPath(resolved.runtime.data_dir, event.session_id);
  const info = lstatSync(path);
  const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
  if (!info.isFile() || info.isSymbolicLink() || info.uid !== currentUID ||
      (info.mode & 0o077) !== 0 || info.size > 8192) {
    throw new Error('codex_turn_context_unsafe');
  }
  const context = JSON.parse(readFileSync(path, 'utf8'));
  if (context?.schema !== 'pulse.codex_turn_context.v1' || context.host !== 'codex' ||
      context.session_id !== event.session_id || context.turn_id !== event.turn_id ||
      context.source_event_key !== event.source_event_key ||
      context.idempotency_key !== event.idempotency_key ||
      context.workspace !== resolved.binding.workspace.canonical_path ||
      context.binding_digest !== resolved.binding.binding_digest ||
      context.resolver_epoch !== resolved.binding.resolver_epoch || context.policy_epoch !== 0 ||
      Number.isNaN(Date.parse(context.expires_at)) || Date.parse(context.expires_at) <= now.valueOf()) {
    throw new Error('codex_turn_context_stale');
  }
  return context;
}

export function readCodexFinalizeMarker(resolved, event) {
  const path = codexFinalizeMarkerPath(resolved.runtime.data_dir, event.session_id, event.turn_id);
  const info = lstatSync(path);
  const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
  if (!info.isFile() || info.isSymbolicLink() || info.uid !== currentUID ||
      (info.mode & 0o077) !== 0 || info.size > 4096) {
    throw new Error('codex_finalize_marker_unsafe');
  }
  const marker = JSON.parse(readFileSync(path, 'utf8'));
  if (marker?.schema !== 'pulse.codex_finalize_marker.v1' ||
      marker.session_id !== event.session_id || marker.turn_id !== event.turn_id ||
      marker.binding_digest !== resolved.binding.binding_digest ||
      marker.source_event_key !== event.source_event_key ||
      !STABLE_ID.test(marker.ledger_id ?? '') || !STABLE_ID.test(marker.receipt_id ?? '') ||
      !['candidates', 'rejected'].includes(marker.status)) {
    throw new Error('codex_finalize_marker_stale');
  }
  return marker;
}
