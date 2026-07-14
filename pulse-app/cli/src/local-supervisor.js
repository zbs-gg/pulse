import { spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
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

function executableDigest(path) {
  return createHash('sha256').update(readFileSync(path)).digest('hex');
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

function receiptMetadataMatches(runtime, receipt) {
  if (!receipt || receipt.schema !== 'pulse.local-vault-process.v1' ||
      receipt.binding_digest !== runtime.binding_digest || receipt.kind !== runtime.kind ||
      receipt.store_id !== runtime.store_id || receipt.data_dir !== runtime.data_dir ||
      !Number.isSafeInteger(receipt.pid) || receipt.pid <= 1 || typeof receipt.executable !== 'string' ||
			!/^[a-f0-9]{64}$/.test(receipt.executable_digest ?? '') ||
			(receipt.stop_requested_at !== undefined &&
				(typeof receipt.stop_requested_at !== 'string' || Number.isNaN(Date.parse(receipt.stop_requested_at))))) {
    return false;
  }
  try {
    return trustedExecutable(receipt.executable) === receipt.executable &&
      executableDigest(receipt.executable) === receipt.executable_digest;
  } catch {
    return false;
  }
}

function receiptProcessMatches(runtime, receipt) {
  const command = processCommand(receipt.pid);
  return command.includes(receipt.executable) && command.includes(`-data-dir ${runtime.data_dir}`) &&
    command.includes(`-addr ${runtime.addr}`);
}

export function inspectVaultRuntime(runtime) {
  const receipt = readRuntimeReceipt(runtime.pid_file);
  if (!receipt) return { status: 'stopped', runtime, fallback: false };
  if (!receiptMetadataMatches(runtime, receipt)) {
    return { status: 'stale_or_mismatched', runtime, fallback: false };
  }
  const command = processCommand(receipt.pid);
  if (command === '') {
    return {
      status: 'crashed', runtime, pid: receipt.pid, executable: receipt.executable,
      executable_digest: receipt.executable_digest, fallback: false,
    };
  }
  if (!receiptProcessMatches(runtime, receipt)) {
    return { status: 'stale_or_mismatched', runtime, fallback: false };
  }
  return {
    status: 'running', runtime, pid: receipt.pid, executable: receipt.executable,
    executable_digest: receipt.executable_digest, fallback: false,
  };
}

async function waitForProcessExit(pid, timeoutMs = 3000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (processCommand(pid) === '') return;
    await new Promise((resolveDelay) => setTimeout(resolveDelay, 50));
  }
  throw new SupervisorError('vault_stop_timeout', 'bound local vault did not stop in time');
}

async function terminateSpawnedProcess(pid) {
  try { process.kill(pid, 'SIGTERM'); } catch { return; }
  try {
    await waitForProcessExit(pid);
    return;
  } catch (error) {
    if (!(error instanceof SupervisorError) || error.code !== 'vault_stop_timeout') throw error;
  }
  try { process.kill(pid, 'SIGKILL'); } catch { return; }
  await waitForProcessExit(pid, 1500);
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

export async function assertVaultRuntimeHealthy(runtime, { timeoutMs = 1500 } = {}) {
  const status = inspectVaultRuntime(runtime);
  if (status.status !== 'running') {
    throw new SupervisorError('vault_not_running', `bound local vault is ${status.status}`);
  }
  await waitForVault(runtime, timeoutMs);
  return status;
}

export async function startVaultRuntime(runtime, {
  daemonPath, timeoutMs = 12000, host = 'pulse-product', allowRollback = true,
} = {}) {
  const executable = trustedExecutable(daemonPath);
  const desiredDigest = executableDigest(executable);
  const status = inspectVaultRuntime(runtime);
  let rollbackPath;
	if (status.status === 'running') {
		if (status.executable === executable && status.executable_digest === desiredDigest) return status;
		rollbackPath = status.executable;
		await stopVaultRuntimeAndWait(runtime);
  }
  if (status.status === 'crashed') {
    // Exact receipt metadata plus a dead PID is safe to recover. Never signal
    // it: the PID may already have been recycled after a reboot.
    rmSync(runtime.pid_file, { force: true });
  }
  if (status.status === 'stale_or_mismatched') {
    throw new SupervisorError('vault_runtime_receipt_mismatch', 'refusing to replace a mismatched runtime receipt');
  }
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
      PULSE_BINDING_DIGEST: runtime.binding_digest,
      PULSE_POLICY_EPOCH: '0',
      PULSE_RESOLVER_EPOCH: String(runtime.resolver_epoch),
      PULSE_DATA_DIR: runtime.data_dir,
      PULSE_CACHE_DIR: runtime.cache_dir,
      PULSE_HOST: host,
      PULSE_LOCAL_EMBED_PYTHON: process.env.PULSE_LOCAL_EMBED_PYTHON ?? '',
      PULSE_LOCAL_EMBED_HELPER: process.env.PULSE_LOCAL_EMBED_HELPER ?? '',
      PULSE_LOCAL_EMBED_MODEL: process.env.PULSE_LOCAL_EMBED_MODEL ?? '',
      ANTHROPIC_API_KEY: '', OPENAI_API_KEY: '', COHERE_API_KEY: '',
    },
  });
  child.unref();
  closeSync(logFD);
  const receipt = {
    schema: 'pulse.local-vault-process.v1', pid: child.pid,
    executable, executable_digest: desiredDigest, binding_digest: runtime.binding_digest,
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
	let terminationError;
	try {
		await terminateSpawnedProcess(child.pid);
	} catch (failure) {
		terminationError = failure;
	}
    rmSync(temporary, { force: true });
    rmSync(runtime.pid_file, { force: true });
	let rollbackError;
    if (allowRollback && rollbackPath) {
      try {
        await startVaultRuntime(runtime, {
          daemonPath: rollbackPath, timeoutMs, host, allowRollback: false,
        });
	  } catch (failure) {
		rollbackError = failure;
      }
    }
	if (terminationError || rollbackError) {
		const details = [
			`activation failed: ${error instanceof Error ? error.message : String(error)}`,
			terminationError ? `new daemon termination failed: ${terminationError.message}` : '',
			rollbackError ? `previous daemon restoration failed: ${rollbackError.message}` : '',
		].filter(Boolean).join('; ');
		throw new SupervisorError('vault_upgrade_rollback_failed', details);
	}
    throw error;
  }
}

export function stopVaultRuntime(runtime) {
  const receipt = readRuntimeReceipt(runtime.pid_file);
  if (!receipt) return { status: 'stopped', runtime, fallback: false };
  if (!receiptMetadataMatches(runtime, receipt) || !receiptProcessMatches(runtime, receipt)) {
    throw new SupervisorError('vault_runtime_receipt_mismatch', 'refusing to signal a mismatched process');
  }
	const stoppingReceipt = { ...receipt, stop_requested_at: new Date().toISOString() };
	const temporary = `${runtime.pid_file}.${process.pid}.${Date.now()}.stopping`;
	try {
		writeFileSync(temporary, JSON.stringify(stoppingReceipt), { mode: 0o600, flag: 'wx' });
		renameSync(temporary, runtime.pid_file);
	} finally {
		rmSync(temporary, { force: true });
	}
	try { process.kill(receipt.pid, 'SIGTERM'); } catch { /* process already exited */ }
	return { status: 'stopping', runtime, pid: receipt.pid, fallback: false };
}

export async function stopVaultRuntimeAndWait(runtime, { timeoutMs = 3000 } = {}) {
	let receipt = readRuntimeReceipt(runtime.pid_file);
	if (!receipt) return { status: 'stopped', runtime, fallback: false };
	if (!receiptMetadataMatches(runtime, receipt)) {
		throw new SupervisorError('vault_runtime_receipt_mismatch', 'refusing to stop a mismatched process');
	}
	if (receipt.stop_requested_at === undefined) {
		if (processCommand(receipt.pid) === '') {
			rmSync(runtime.pid_file, { force: true });
			return { status: 'stopped', runtime, fallback: false };
		}
		if (!receiptProcessMatches(runtime, receipt)) {
			throw new SupervisorError('vault_runtime_receipt_mismatch', 'refusing to stop a mismatched process');
		}
		stopVaultRuntime(runtime);
		receipt = readRuntimeReceipt(runtime.pid_file);
	}
	await waitForProcessExit(receipt.pid, timeoutMs);
	rmSync(runtime.pid_file, { force: true });
	return { status: 'stopped', runtime, fallback: false };
}
