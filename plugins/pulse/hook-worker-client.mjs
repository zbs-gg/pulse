import { spawn } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import {
  closeSync, existsSync, lstatSync, mkdirSync, openSync, readFileSync, realpathSync, rmSync,
} from 'node:fs';
import { createConnection } from 'node:net';
import { homedir } from 'node:os';
import { dirname, isAbsolute, join, resolve } from 'node:path';

const SHA256 = /^[a-f0-9]{64}$/;
const RECEIPT_SCHEMA = 'pulse.hook_worker.v1';
const REQUEST_SCHEMA = 'pulse.hook_worker.request.v1';
const RESPONSE_SCHEMA = 'pulse.hook_worker.response.v1';
const MAX_INPUT_BYTES = 1 << 20;
const MAX_RESPONSE_BYTES = (1 << 20) + (64 << 10);
const MAX_WORKER_LIFETIME_MS = 120_000;

function hookWorkerStartTimeout(platform = process.platform) {
  // Native Windows startup includes Authenticode/ACL checks and can be much
  // slower on clean ARM64 runners. This is install-time prewarm only; the
  // first-value lifecycle gate remains unchanged.
  return platform === 'win32' ? 120_000 : 20_000;
}

function exactObject(value, keys) {
  return value && typeof value === 'object' && !Array.isArray(value) &&
    Object.keys(value).sort().join('\0') === [...keys].sort().join('\0');
}

async function readHookInput(stream = process.stdin) {
  const chunks = [];
  let size = 0;
  for await (const chunk of stream) {
    size += chunk.length;
    if (size > MAX_INPUT_BYTES) throw new Error('hook_worker_input_too_large');
    chunks.push(chunk);
  }
  if (chunks.length === 0) throw new Error('hook_worker_input_empty');
  const input = JSON.parse(Buffer.concat(chunks).toString('utf8'));
  if (!input || typeof input !== 'object' || Array.isArray(input)) throw new Error('hook_worker_input_invalid');
  return input;
}

function workerWorkspace(pluginRoot, input) {
  const cwd = typeof input.cwd === 'string' ? input.cwd : process.cwd();
  if (!isAbsolute(cwd)) throw new Error('hook_worker_cwd_invalid');
  const workspacePath = realpathSync(resolve(cwd));
  const pluginPath = realpathSync(resolve(pluginRoot));
  const digest = createHash('sha256')
    .update('pulse-hook-worker-workspace-v1\0')
    .update(pluginPath)
    .update('\0')
    .update(workspacePath)
    .digest('hex');
  return { digest, workspacePath };
}

function workerReceiptPath(host, workspaceDigest) {
  return join(homedir(), '.pulse', 'runtime', 'hook-workers', host, `${workspaceDigest}.json`);
}

function privateReceipt(path) {
  try {
    const info = lstatSync(path);
    if (!info.isFile() || info.isSymbolicLink() || info.size < 1 || info.size > 8192 ||
        (process.platform !== 'win32' && (info.mode & 0o077) !== 0)) return undefined;
    return JSON.parse(readFileSync(path, 'utf8'));
  } catch {
    return undefined;
  }
}

function validReceipt(receipt, { host, workspaceDigest, expected, now = Date.now() }) {
  const keys = [
    'created_at_ms', 'expires_at_ms', 'hook_digest', 'host', 'pid', 'plugin_digest', 'port',
    'runtime_digest', 'schema', 'token', 'workspace_digest',
  ];
  if (!exactObject(receipt, keys) || receipt.schema !== RECEIPT_SCHEMA || receipt.host !== host ||
      receipt.workspace_digest !== workspaceDigest || !Number.isSafeInteger(receipt.pid) || receipt.pid < 2 ||
      !Number.isSafeInteger(receipt.port) || receipt.port < 1 || receipt.port > 65535 ||
      !Number.isSafeInteger(receipt.created_at_ms) || !Number.isSafeInteger(receipt.expires_at_ms) ||
      receipt.created_at_ms > now || receipt.expires_at_ms <= now ||
      receipt.expires_at_ms - receipt.created_at_ms !== MAX_WORKER_LIFETIME_MS ||
      ![receipt.token, receipt.hook_digest, receipt.plugin_digest, receipt.runtime_digest]
        .every((value) => SHA256.test(value ?? ''))) return false;
  return !expected || (
    receipt.hook_digest === expected.hookDigest &&
    receipt.plugin_digest === expected.productEnvironment.PULSE_PLUGIN_TREE_DIGEST &&
    receipt.runtime_digest === expected.productEnvironment.PULSE_RUNTIME_DIGEST
  );
}

function workerError(code) {
  const error = new Error(code);
  error.code = code;
  return error;
}

function dispatchWorker(receipt, { host, eventName, input }, timeoutMs = 65_000) {
  const requestID = randomBytes(32).toString('hex');
  const request = `${JSON.stringify({
    schema: REQUEST_SCHEMA,
    request_id: requestID,
    token: receipt.token,
    host,
    event_name: eventName,
    workspace_digest: receipt.workspace_digest,
    input,
  })}\n`;
  if (Buffer.byteLength(request) > MAX_INPUT_BYTES + (64 << 10)) {
    throw workerError('hook_worker_request_too_large');
  }
  return new Promise((accept, reject) => {
    const socket = createConnection({ host: '127.0.0.1', port: receipt.port });
    let body = '';
    let size = 0;
    let settled = false;
    const finish = (error, output) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      socket.destroy();
      if (error) reject(error); else accept(output);
    };
    socket.setEncoding('utf8');
    socket.once('connect', () => socket.write(request));
    socket.on('data', (chunk) => {
      size += Buffer.byteLength(chunk);
      if (size > MAX_RESPONSE_BYTES) return finish(workerError('hook_worker_response_too_large'));
      body += chunk;
      const newline = body.indexOf('\n');
      if (newline < 0) return;
      let response;
      try { response = JSON.parse(body.slice(0, newline)); } catch {
        return finish(workerError('hook_worker_response_invalid'));
      }
      const keys = response?.ok
        ? ['ok', 'output', 'request_id', 'schema']
        : ['error_code', 'ok', 'request_id', 'schema'];
      if (!exactObject(response, keys) || response.schema !== RESPONSE_SCHEMA ||
          response.request_id !== requestID || typeof response.ok !== 'boolean') {
        return finish(workerError('hook_worker_response_invalid'));
      }
      if (!response.ok) return finish(workerError(response.error_code ?? 'hook_worker_request_failed'));
      if (typeof response.output !== 'string') return finish(workerError('hook_worker_response_invalid'));
      return finish(undefined, response.output);
    });
    socket.once('error', () => finish(workerError('hook_worker_unavailable')));
    socket.once('end', () => {
      if (!settled) finish(workerError('hook_worker_response_missing'));
    });
    const timer = setTimeout(() => finish(workerError('hook_worker_timeout')), timeoutMs);
  });
}

function spawnWorker({ host, expected, receiptPath, workspace, token }) {
  if (!expected || typeof expected.entrypointPath !== 'string' || !isAbsolute(expected.entrypointPath) ||
      !existsSync(expected.entrypointPath) || !SHA256.test(expected.hookDigest ?? '')) {
    throw new Error('hook_worker_product_environment_invalid');
  }
  const child = spawn(process.execPath, [expected.entrypointPath, '--pulse-hook-worker', host], {
    // Windows cannot remove a workspace while a live process owns it as cwd.
    // The request carries the canonical workspace explicitly, so keep the
    // bounded worker in its owner-private runtime directory instead.
    cwd: dirname(process.execPath),
    detached: true,
    env: {
      ...process.env,
      ...expected.productEnvironment,
      ...(expected.environmentPatch ?? {}),
      PULSE_HOOK_BUNDLE_DIGEST: expected.hookDigest,
      PULSE_HOOK_WORKER_RECEIPT: receiptPath,
      PULSE_HOOK_WORKER_TOKEN: token,
      PULSE_HOOK_WORKER_WORKSPACE_DIGEST: workspace.digest,
    },
    stdio: 'ignore',
    windowsHide: true,
  });
  return new Promise((accept, reject) => {
    child.once('error', () => reject(workerError('hook_worker_spawn_failed')));
    child.once('spawn', () => {
      child.unref();
      accept(child);
    });
  });
}

function delay(ms) {
  return new Promise((accept) => setTimeout(accept, ms));
}

async function waitForWorker(
  path,
  validation,
  timeoutMs = hookWorkerStartTimeout(),
  child = undefined,
) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const receipt = privateReceipt(path);
    if (validReceipt(receipt, validation)) return receipt;
    if (child && (child.exitCode !== null || child.signalCode !== null)) {
      throw workerError('hook_worker_start_failed');
    }
    await delay(25);
  }
  throw workerError('hook_worker_start_timeout');
}

async function ensureWorker({ host, expected, receiptPath, workspace }) {
  mkdirSync(dirname(receiptPath), { recursive: true, mode: 0o700 });
  const lockPath = `${receiptPath}.start`;
  let ownsLock = false;
  try {
    try {
      const handle = openSync(lockPath, 'wx', 0o600);
      closeSync(handle);
      ownsLock = true;
    } catch (error) {
      if (error?.code !== 'EEXIST') throw error;
    }
    let child;
    if (ownsLock) {
      rmSync(receiptPath, { force: true });
      child = await spawnWorker({
        host, expected, receiptPath, workspace, token: randomBytes(32).toString('hex'),
      });
    }
    return await waitForWorker(receiptPath, {
      host, workspaceDigest: workspace.digest, expected,
    }, hookWorkerStartTimeout(), child);
  } finally {
    if (ownsLock) rmSync(lockPath, { force: true });
  }
}

function writeOutput(output, stream = process.stdout) {
  return new Promise((accept, reject) => {
    stream.write(output, (error) => error ? reject(error) : accept());
  });
}

export async function runHookWorkerClient({
  host,
  eventName,
  pluginRoot,
  pluginData,
  resolveEnvironment,
  inputStream = process.stdin,
  outputStream = process.stdout,
  services = {},
} = {}) {
  if (!['claude-code', 'codex', 'cursor'].includes(host) || typeof eventName !== 'string' ||
      typeof pluginRoot !== 'string' || typeof resolveEnvironment !== 'function') {
    throw new Error('hook_worker_client_invalid');
  }
  const readInput = services.readHookInput ?? readHookInput;
  const resolveWorkspace = services.workerWorkspace ?? workerWorkspace;
  const receiptForWorkspace = services.workerReceiptPath ?? workerReceiptPath;
  const readReceipt = services.privateReceipt ?? privateReceipt;
  const receiptIsValid = services.validReceipt ?? validReceipt;
  const dispatch = services.dispatchWorker ?? dispatchWorker;
  const ensure = services.ensureWorker ?? ensureWorker;
  const emit = services.writeOutput ?? writeOutput;
  const input = await readInput(inputStream);
  const workspace = resolveWorkspace(pluginRoot, input);
  const boundedInput = typeof input.cwd === 'string'
    ? input
    : { ...input, cwd: workspace.workspacePath };
  const receiptPath = receiptForWorkspace(host, workspace.digest);
  let receipt = readReceipt(receiptPath);
  // A live receipt represents an exact, owner-private worker generation whose
  // runtime and plugin bytes were fully verified before it started. Reuse that
  // bounded generation at a new session boundary: the worker still refreshes
  // binding recovery and authority witnesses for SessionStart. If the worker
  // is gone or its 120-second lease expired, fall back to the full product
  // environment proof before starting the next generation.
  if (receiptIsValid(receipt, { host, workspaceDigest: workspace.digest })) {
    try {
      const output = await dispatch(receipt, { host, eventName, input: boundedInput });
      await emit(output, outputStream);
      return;
    } catch (error) {
      if (error?.code === 'hook_worker_execution_failed') throw error;
    }
  }

  const expected = await resolveEnvironment();
  receipt = await ensure({ host, expected, receiptPath, workspace });
  const output = await dispatch(receipt, { host, eventName, input: boundedInput });
  await emit(output, outputStream);
}

export async function prewarmHookWorker({
  host,
  pluginRoot,
  workspacePath,
  resolveEnvironment,
  services = {},
} = {}) {
  if (!['claude-code', 'codex', 'cursor'].includes(host) || typeof pluginRoot !== 'string' ||
      typeof workspacePath !== 'string' || !isAbsolute(workspacePath) ||
      typeof resolveEnvironment !== 'function') {
    throw new Error('hook_worker_prewarm_invalid');
  }
  const resolveWorkspace = services.workerWorkspace ?? workerWorkspace;
  const receiptForWorkspace = services.workerReceiptPath ?? workerReceiptPath;
  const readReceipt = services.privateReceipt ?? privateReceipt;
  const receiptIsValid = services.validReceipt ?? validReceipt;
  const ensure = services.ensureWorker ?? ensureWorker;
  const workspace = resolveWorkspace(pluginRoot, { cwd: workspacePath });
  const receiptPath = receiptForWorkspace(host, workspace.digest);
  const expected = await resolveEnvironment();
  let receipt = readReceipt(receiptPath);
  let reused = receiptIsValid(receipt, { host, workspaceDigest: workspace.digest, expected });
  if (!reused) {
    receipt = await ensure({ host, expected, receiptPath, workspace });
    if (!receiptIsValid(receipt, { host, workspaceDigest: workspace.digest, expected })) {
      throw new Error('hook_worker_prewarm_unverified');
    }
  }
  return Object.freeze({
    schema: 'pulse.hook_worker_prewarm.v1',
    host,
    workspace_digest: workspace.digest,
    hook_digest: expected.hookDigest,
    plugin_digest: expected.productEnvironment.PULSE_PLUGIN_TREE_DIGEST,
    runtime_digest: expected.productEnvironment.PULSE_RUNTIME_DIGEST,
    reused,
  });
}

export const __hookWorkerClientTest = Object.freeze({
  MAX_WORKER_LIFETIME_MS,
  hookWorkerStartTimeout,
  privateReceipt,
  validReceipt,
  workerReceiptPath,
  workerWorkspace,
});
