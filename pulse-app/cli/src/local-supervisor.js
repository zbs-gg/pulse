import { spawn, spawnSync } from 'node:child_process';
import {
  closeSync,
  existsSync,
  lstatSync,
  mkdirSync,
  openSync,
  readFileSync,
  realpathSync,
  renameSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs';
import { dirname, isAbsolute, join, resolve } from 'node:path';

export class SupervisorError extends Error {
  constructor(code, message) {
    super(message);
    this.name = 'SupervisorError';
    this.code = code;
  }
}

function requireAbsolutePath(value, name) {
  if (typeof value !== 'string' || !isAbsolute(value)) {
    throw new SupervisorError('vault_runtime_invalid', `${name} must be absolute`);
  }
  return resolve(value);
}

export function vaultRuntimeFromBinding(binding) {
  if (!binding || binding.fallback !== false) {
    throw new SupervisorError('vault_runtime_invalid', 'trusted binding with fallback=false is required');
  }
  const vault = binding.mode === 'personal' ? binding.personal : binding.mode === 'team' ? binding.desk : undefined;
  const kind = binding.mode === 'personal' ? 'personal' : binding.mode === 'team' ? 'desk' : undefined;
  if (!vault || !kind) {
    throw new SupervisorError('vault_runtime_invalid', 'binding has no local product vault');
  }
  const endpoint = new URL(vault.base_url);
  if (endpoint.protocol !== 'http:' || !['127.0.0.1', '[::1]', '::1'].includes(endpoint.hostname) || endpoint.pathname !== '/') {
    throw new SupervisorError('vault_runtime_invalid', 'local product vault endpoint must be numeric loopback HTTP');
  }
  const dataDir = requireAbsolutePath(vault.data_dir, 'vault data_dir');
  const cacheDir = requireAbsolutePath(vault.cache_dir, 'vault cache_dir');
  if (dataDir === cacheDir || dataDir.startsWith(`${cacheDir}/`) || cacheDir.startsWith(`${dataDir}/`)) {
    throw new SupervisorError('vault_runtime_invalid', 'vault data and cache roots must be distinct');
  }
  return {
    schema: 'pulse.local-vault-runtime.v1',
    binding_id: binding.binding_id,
    binding_digest: binding.binding_digest,
    resolver_epoch: binding.resolver_epoch,
    kind,
    runtime_mode: `${kind}-local`,
    store_id: vault.store_id,
    data_dir: dataDir,
    cache_dir: cacheDir,
    addr: endpoint.host,
    base_url: endpoint.origin,
    pid_file: join(dataDir, 'supervisor-runtime.json'),
    log_file: join(dataDir, 'logs', `${kind}.log`),
    fallback: false,
  };
}

function ensurePrivateDirectory(path) {
  mkdirSync(path, { recursive: true, mode: 0o700 });
  const info = lstatSync(path);
  const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
  if (!info.isDirectory() || info.isSymbolicLink() || info.uid !== currentUID || (info.mode & 0o077) !== 0) {
    throw new SupervisorError('vault_directory_unsafe', 'vault directory must be owner-only 0700 and not a symlink');
  }
}

function trustedExecutable(path) {
  const executable = realpathSync(resolve(path));
  const info = statSync(executable);
  const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
  if (!info.isFile() || (info.mode & 0o111) === 0 || (info.mode & 0o022) !== 0 || ![0, currentUID].includes(info.uid)) {
    throw new SupervisorError('vault_binary_unsafe', 'vault daemon must be an owner/root-controlled executable');
  }
  return executable;
}

function readRuntimeReceipt(path) {
  if (!existsSync(path)) return undefined;
  const info = lstatSync(path);
  const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
  if (!info.isFile() || info.isSymbolicLink() || info.uid !== currentUID || (info.mode & 0o077) !== 0 || info.size > 8192) {
    throw new SupervisorError('vault_runtime_receipt_unsafe', 'runtime receipt is unsafe');
  }
  try {
    return JSON.parse(readFileSync(path, 'utf8'));
  } catch {
    throw new SupervisorError('vault_runtime_receipt_invalid', 'runtime receipt is invalid');
  }
}

function processCommand(pid) {
  const result = spawnSync('/bin/ps', ['-ww', '-p', String(pid), '-o', 'command='], { encoding: 'utf8' });
  return result.status === 0 ? result.stdout.trim() : '';
}

function receiptMatches(runtime, receipt) {
  if (!receipt || receipt.schema !== 'pulse.local-vault-process.v1' ||
      receipt.binding_digest !== runtime.binding_digest || receipt.kind !== runtime.kind ||
      receipt.store_id !== runtime.store_id || receipt.data_dir !== runtime.data_dir ||
      !Number.isSafeInteger(receipt.pid) || receipt.pid <= 1 || typeof receipt.executable !== 'string') {
    return false;
  }
  const command = processCommand(receipt.pid);
  return command.includes(receipt.executable) && command.includes(`-data-dir ${runtime.data_dir}`) &&
    command.includes(`-addr ${runtime.addr}`);
}

export function inspectVaultRuntime(runtime) {
  const receipt = readRuntimeReceipt(runtime.pid_file);
  if (!receipt) return { status: 'stopped', runtime, fallback: false };
  if (!receiptMatches(runtime, receipt)) {
    return { status: 'stale_or_mismatched', runtime, fallback: false };
  }
  return { status: 'running', runtime, pid: receipt.pid, fallback: false };
}

async function waitForVault(runtime, timeoutMs) {
  const secretPath = join(runtime.data_dir, 'secret.key');
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      if (existsSync(secretPath)) {
        const info = lstatSync(secretPath);
        if (!info.isFile() || info.isSymbolicLink() || (info.mode & 0o077) !== 0 || info.size !== 64) {
          throw new SupervisorError('vault_secret_unsafe', 'vault IPC secret is unsafe');
        }
        const secret = readFileSync(secretPath, 'utf8');
        const response = await fetch(`${runtime.base_url}/health`, {
          headers: { 'X-Pulse-Key': secret }, signal: AbortSignal.timeout(500),
        });
        if (response.ok) return;
      }
    } catch (error) {
      if (error instanceof SupervisorError) throw error;
    }
    await new Promise((resolveDelay) => setTimeout(resolveDelay, 100));
  }
  throw new SupervisorError('vault_start_timeout', 'bound local vault did not become ready');
}

export async function startVaultRuntime(runtime, { daemonPath, timeoutMs = 12000 } = {}) {
  const status = inspectVaultRuntime(runtime);
  if (status.status === 'running') return status;
  if (status.status === 'stale_or_mismatched') {
    throw new SupervisorError('vault_runtime_receipt_mismatch', 'refusing to replace a mismatched runtime receipt');
  }
  const executable = trustedExecutable(daemonPath);
  ensurePrivateDirectory(runtime.data_dir);
  ensurePrivateDirectory(dirname(runtime.log_file));
  ensurePrivateDirectory(runtime.cache_dir);
  const logFD = openSync(runtime.log_file, 'a', 0o600);
  const child = spawn(executable, ['-data-dir', runtime.data_dir, '-addr', runtime.addr], {
    detached: true,
    stdio: ['ignore', logFD, logFD],
    env: {
      HOME: process.env.HOME ?? '', PATH: '/usr/bin:/bin',
      PULSE_RUNTIME_MODE: runtime.runtime_mode,
      PULSE_VAULT_STORE_ID: runtime.store_id,
      PULSE_DATA_DIR: runtime.data_dir,
      PULSE_CACHE_DIR: runtime.cache_dir,
      ANTHROPIC_API_KEY: '', OPENAI_API_KEY: '', COHERE_API_KEY: '',
    },
  });
  child.unref();
  closeSync(logFD);
  const receipt = {
    schema: 'pulse.local-vault-process.v1', pid: child.pid,
    executable, binding_digest: runtime.binding_digest,
    kind: runtime.kind, store_id: runtime.store_id, data_dir: runtime.data_dir,
    started_at: new Date().toISOString(),
  };
  const temporary = `${runtime.pid_file}.new`;
  try {
    writeFileSync(temporary, JSON.stringify(receipt), { mode: 0o600, flag: 'wx' });
    renameSync(temporary, runtime.pid_file);
    await waitForVault(runtime, timeoutMs);
    return { status: 'running', runtime, pid: child.pid, fallback: false };
  } catch (error) {
    try { process.kill(child.pid, 'SIGTERM'); } catch { /* already stopped */ }
    rmSync(temporary, { force: true });
    rmSync(runtime.pid_file, { force: true });
    throw error;
  }
}

export function stopVaultRuntime(runtime) {
  const receipt = readRuntimeReceipt(runtime.pid_file);
  if (!receipt) return { status: 'stopped', runtime, fallback: false };
  if (!receiptMatches(runtime, receipt)) {
    throw new SupervisorError('vault_runtime_receipt_mismatch', 'refusing to signal a mismatched process');
  }
  try { process.kill(receipt.pid, 'SIGTERM'); } catch { /* process already exited */ }
  rmSync(runtime.pid_file, { force: true });
  return { status: 'stopped', runtime, fallback: false };
}
