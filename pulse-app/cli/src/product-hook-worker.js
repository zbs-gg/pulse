import { createHash, timingSafeEqual } from 'node:crypto';
import {
  existsSync, lstatSync, mkdirSync, readFileSync, realpathSync, renameSync, rmSync, writeFileSync,
} from 'node:fs';
import { createServer } from 'node:net';
import { dirname, isAbsolute, relative, resolve } from 'node:path';

const SHA256 = /^[a-f0-9]{64}$/;
const TOKEN = /^[a-f0-9]{64}$/;
const REQUEST_SCHEMA = 'pulse.hook_worker.request.v1';
const RESPONSE_SCHEMA = 'pulse.hook_worker.response.v1';
const RECEIPT_SCHEMA = 'pulse.hook_worker.v1';
const MAX_REQUEST_BYTES = (1 << 20) + (64 << 10);
const MAX_WORKER_LIFETIME_MS = 120_000;

function exactObject(value, keys) {
  return value && typeof value === 'object' && !Array.isArray(value) &&
    Object.keys(value).sort().join('\0') === [...keys].sort().join('\0');
}

function safeEqual(left, right) {
  if (!TOKEN.test(left ?? '') || !TOKEN.test(right ?? '')) return false;
  return timingSafeEqual(Buffer.from(left, 'hex'), Buffer.from(right, 'hex'));
}

function fileWitness(path, maxBytes = 1 << 20) {
  const info = lstatSync(path);
  if (!info.isFile() || info.isSymbolicLink() || info.size < 1 || info.size > maxBytes) {
    throw new Error('hook_worker_witness_unsafe');
  }
  const bytes = readFileSync(path);
  if (bytes.length !== info.size) throw new Error('hook_worker_witness_changed');
  return createHash('sha256').update(bytes).digest('hex');
}

function witnessSet(paths) {
  return Object.freeze(Object.fromEntries(paths.map((path) => [resolve(path), fileWitness(path)])));
}

function sameWitnesses(witnesses) {
  try {
    return Object.entries(witnesses).every(([path, digest]) => fileWitness(path) === digest);
  } catch {
    return false;
  }
}

function inputInsideWorkspace(input, canonicalWorkspace) {
  const cwd = typeof input?.cwd === 'string' ? input.cwd : process.cwd();
  if (!isAbsolute(cwd)) return false;
  let current;
  try { current = realpathSync(resolve(cwd)); } catch { return false; }
  const path = relative(canonicalWorkspace, current);
  return path === '' || (!path.startsWith('..') && !isAbsolute(path));
}

function hookSessionID(host, input) {
  const value = host === 'cursor' ? input?.session_id ?? input?.conversation_id : input?.session_id;
  return typeof value === 'string' && value.length > 0 && value.length <= 255 ? value : undefined;
}

export function createHookWorkerRuntimeResolver({
  host,
  resolveRuntime,
  witnessPaths,
  now = () => Date.now(),
  ttlMs = MAX_WORKER_LIFETIME_MS,
} = {}) {
  if (!['claude-code', 'codex', 'cursor'].includes(host) || typeof resolveRuntime !== 'function' ||
      typeof witnessPaths !== 'function' || !Number.isSafeInteger(ttlMs) || ttlMs < 1 ||
      ttlMs > MAX_WORKER_LIFETIME_MS) {
    throw new Error('hook_worker_resolver_invalid');
  }
  const sessions = new Map();
  let activeEvent;

  return Object.freeze({
    begin(eventName, input) {
      const sessionID = hookSessionID(host, input);
      if (!sessionID) throw new Error('hook_worker_session_invalid');
      activeEvent = { eventName, sessionID };
      if (eventName === 'SessionStart' || eventName === 'sessionStart') sessions.delete(sessionID);
    },
    resolve(input) {
      const sessionID = hookSessionID(host, input);
      if (!activeEvent || activeEvent.sessionID !== sessionID) {
        throw new Error('hook_worker_event_context_missing');
      }
      const cached = sessions.get(sessionID);
      const currentTime = now();
      if (cached && cached.expiresAt > currentTime &&
          inputInsideWorkspace(input, cached.resolved.binding.workspace.canonical_path) &&
          sameWitnesses(cached.witnesses)) {
        return cached.resolved;
      }
      sessions.delete(sessionID);
      const resolved = resolveRuntime(input);
      if (!resolved?.binding?.workspace?.canonical_path || !inputInsideWorkspace(input, resolved.binding.workspace.canonical_path)) {
        throw new Error('hook_worker_workspace_mismatch');
      }
      const paths = witnessPaths(resolved);
      if (!Array.isArray(paths) || paths.length < 1 || paths.some((path) => typeof path !== 'string' || !isAbsolute(path))) {
        throw new Error('hook_worker_witness_paths_invalid');
      }
      sessions.set(sessionID, {
        expiresAt: currentTime + ttlMs,
        resolved,
        witnesses: witnessSet([...new Set(paths.map((path) => resolve(path)))]),
      });
      return resolved;
    },
  });
}

function validRequest(value, expectedHost, token) {
  const keys = ['event_name', 'host', 'input', 'request_id', 'schema', 'token'];
  return exactObject(value, keys) && value.schema === REQUEST_SCHEMA && value.host === expectedHost &&
    typeof value.event_name === 'string' && value.event_name.length > 0 && value.event_name.length <= 64 &&
    typeof value.request_id === 'string' && SHA256.test(value.request_id) &&
    value.input && typeof value.input === 'object' && !Array.isArray(value.input) && safeEqual(value.token, token);
}

function writeReceipt(path, receipt) {
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  const temporary = `${path}.${process.pid}.new`;
  try {
    writeFileSync(temporary, `${JSON.stringify(receipt)}\n`, { flag: 'wx', mode: 0o600 });
    renameSync(temporary, path);
  } finally {
    rmSync(temporary, { force: true });
  }
}

function removeOwnReceipt(path, pid) {
  try {
    const current = JSON.parse(readFileSync(path, 'utf8'));
    if (current?.schema === RECEIPT_SCHEMA && current.pid === pid) rmSync(path, { force: true });
  } catch { /* another generation owns the receipt or it is already gone */ }
}

export async function serveProductHookWorker({
  host,
  token,
  receiptPath,
  workspaceDigest,
  runtimeDigest,
  pluginDigest,
  hookDigest,
  handleRequest,
  lifetimeMs = MAX_WORKER_LIFETIME_MS,
  now = () => Date.now(),
} = {}) {
  if (!['claude-code', 'codex', 'cursor'].includes(host) || !TOKEN.test(token ?? '') ||
      typeof receiptPath !== 'string' || !isAbsolute(receiptPath) || !SHA256.test(workspaceDigest ?? '') ||
      ![runtimeDigest, pluginDigest, hookDigest].every((value) => SHA256.test(value ?? '')) ||
      typeof handleRequest !== 'function' || !Number.isSafeInteger(lifetimeMs) || lifetimeMs < 1 ||
      lifetimeMs > MAX_WORKER_LIFETIME_MS) {
    throw new Error('hook_worker_configuration_invalid');
  }
  const startedAt = now();
  let queue = Promise.resolve();
  let closing = false;
  let active = 0;
  const server = createServer((socket) => {
    socket.setEncoding('utf8');
    socket.setTimeout(65_000, () => socket.destroy());
    let body = '';
    let received = 0;
    let complete = false;
    const respond = (response) => {
      if (!socket.destroyed) socket.end(`${JSON.stringify(response)}\n`);
    };
    socket.on('data', (chunk) => {
      if (complete) return;
      received += Buffer.byteLength(chunk);
      if (received > MAX_REQUEST_BYTES) {
        complete = true;
        respond({ schema: RESPONSE_SCHEMA, request_id: '', ok: false, error_code: 'hook_worker_request_too_large' });
        return;
      }
      body += chunk;
      const newline = body.indexOf('\n');
      if (newline < 0) return;
      complete = true;
      const serialized = body.slice(0, newline);
      let request;
      try { request = JSON.parse(serialized); } catch { request = null; }
      if (!validRequest(request, host, token)) {
        respond({ schema: RESPONSE_SCHEMA, request_id: request?.request_id ?? '', ok: false, error_code: 'hook_worker_request_invalid' });
        return;
      }
      active += 1;
      queue = queue.then(async () => {
        try {
          const output = await handleRequest(request.event_name, request.input);
          if (typeof output !== 'string' || Buffer.byteLength(output) > (1 << 20)) {
            throw new Error('hook_worker_output_invalid');
          }
          respond({ schema: RESPONSE_SCHEMA, request_id: request.request_id, ok: true, output });
        } catch {
          respond({ schema: RESPONSE_SCHEMA, request_id: request.request_id, ok: false, error_code: 'hook_worker_execution_failed' });
        } finally {
          active -= 1;
          if (closing && active === 0) server.close();
        }
      });
    });
    socket.on('error', () => {});
  });

  await new Promise((accept, reject) => {
    server.once('error', reject);
    server.listen({ host: '127.0.0.1', port: 0, exclusive: true }, accept);
  });
  const address = server.address();
  if (!address || typeof address === 'string') throw new Error('hook_worker_listen_invalid');
  const receipt = {
    schema: RECEIPT_SCHEMA,
    host,
    hook_digest: hookDigest,
    plugin_digest: pluginDigest,
    runtime_digest: runtimeDigest,
    workspace_digest: workspaceDigest,
    pid: process.pid,
    port: address.port,
    token,
    created_at_ms: startedAt,
    expires_at_ms: startedAt + lifetimeMs,
  };
  writeReceipt(receiptPath, receipt);

  const close = () => {
    closing = true;
    if (active === 0) server.close();
  };
  const timer = setTimeout(close, lifetimeMs);
  timer.unref?.();
  await new Promise((accept) => server.once('close', accept));
  clearTimeout(timer);
  removeOwnReceipt(receiptPath, process.pid);
}

export const __productHookWorkerTest = Object.freeze({
  MAX_WORKER_LIFETIME_MS,
  RECEIPT_SCHEMA,
  REQUEST_SCHEMA,
  RESPONSE_SCHEMA,
  fileWitness,
  validRequest,
});
