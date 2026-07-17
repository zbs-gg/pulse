import { spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  chmodSync,
  closeSync,
  existsSync,
  fsyncSync,
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
import { dirname, isAbsolute, join, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

import { isCanonicalRepositoryID } from './host-adapter.js';

const SHA256 = /^[a-f0-9]{64}$/;
const PRODUCT_BINDING_VERIFIER = fileURLToPath(new URL('./product-binding-verifier.js', import.meta.url));
const MANAGED_CONFIG_SCHEMA = 'pulse.managed_embedder.config.v1';
const MANAGED_CONFIG_KEYS = [
  'dimensions', 'embedder_runtime_activation_digest', 'embedder_runtime_tree_digest', 'helper_path',
  'model', 'model_activation_digest', 'model_file', 'model_tree_digest', 'normalized', 'pooling',
  'protocol', 'python_executable', 'schema', 'support_directory',
];

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
	const repositoryID = binding.workspace?.repository_id;
	if (!isCanonicalRepositoryID(repositoryID)) {
		throw new SupervisorError('vault_runtime_invalid', 'binding has no canonical repository authority');
	}
  const workspacePath = requireAbsolutePath(binding.workspace?.canonical_path, 'binding workspace');
  if (dataDir === cacheDir || dataDir.startsWith(`${cacheDir}/`) || cacheDir.startsWith(`${dataDir}/`)) {
    throw new SupervisorError('vault_runtime_invalid', 'vault data and cache roots must be distinct');
  }
  return {
    schema: 'pulse.local-vault-runtime.v1',
    binding_id: binding.binding_id,
		binding_digest: binding.binding_digest,
		repository_id: repositoryID,
		workspace_path: workspacePath,
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

function canonicalJSON(value) {
  if (value === null || typeof value !== 'object') return JSON.stringify(value);
  if (Array.isArray(value)) return `[${value.map((item) => canonicalJSON(item)).join(',')}]`;
  return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalJSON(value[key])}`).join(',')}}`;
}

function syncDirectory(path) {
  const descriptor = openSync(path, 'r');
  try { fsyncSync(descriptor); } finally { closeSync(descriptor); }
}

function atomicPrivateJSON(path, value) {
  ensurePrivateDirectory(dirname(path));
  const temporary = `${path}.${process.pid}.${Date.now()}.new`;
  try {
    writeFileSync(temporary, `${canonicalJSON(value)}\n`, { encoding: 'utf8', mode: 0o600, flag: 'wx' });
    chmodSync(temporary, 0o600);
    const descriptor = openSync(temporary, 'r');
    try { fsyncSync(descriptor); } finally { closeSync(descriptor); }
    renameSync(temporary, path);
    syncDirectory(dirname(path));
  } finally {
    rmSync(temporary, { force: true });
  }
}

function inside(root, candidate) {
  const base = resolve(root);
  const value = resolve(candidate);
  return value.startsWith(`${base}${sep}`);
}

function activatedFile(activation, relativePath, { executable = false } = {}) {
  const path = join(activation.version_path, relativePath);
  if (!inside(activation.version_path, path)) {
    throw new SupervisorError('managed_artifact_path_invalid', 'managed artifact path escaped its activation');
  }
  const info = lstatSync(path);
  const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.uid !== currentUID ||
      (info.mode & 0o022) !== 0 || (executable && (info.mode & 0o111) === 0)) {
    throw new SupervisorError('managed_artifact_file_unsafe', `managed artifact file is unsafe: ${relativePath}`);
  }
  return path;
}

function activatedDirectory(activation, relativePath) {
  const path = join(activation.version_path, relativePath);
  if (!inside(activation.version_path, path)) {
    throw new SupervisorError('managed_artifact_path_invalid', 'managed artifact path escaped its activation');
  }
  const info = lstatSync(path);
  const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
  if (!info.isDirectory() || info.isSymbolicLink() || info.uid !== currentUID || (info.mode & 0o077) !== 0) {
    throw new SupervisorError('managed_artifact_directory_unsafe', `managed artifact directory is unsafe: ${relativePath}`);
  }
  return path;
}

export function inspectManagedEmbedderConfig(runtime, managedEmbedder) {
  if (!managedEmbedder || typeof managedEmbedder !== 'object' ||
      managedEmbedder.config_path !== join(runtime.data_dir, 'runtime', 'managed-embedder.json') ||
      !SHA256.test(managedEmbedder.config_digest ?? '')) {
    throw new SupervisorError('managed_embedder_config_invalid', 'managed embedder config identity is invalid');
  }
  const info = lstatSync(managedEmbedder.config_path);
  const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.uid !== currentUID ||
      (info.mode & 0o077) !== 0 || info.size > 16 * 1024) {
    throw new SupervisorError('managed_embedder_config_unsafe', 'managed embedder config must be a private regular file');
  }
  const bytes = readFileSync(managedEmbedder.config_path, 'utf8');
  if (createHash('sha256').update(bytes).digest('hex') !== managedEmbedder.config_digest) {
    throw new SupervisorError('managed_embedder_config_digest_mismatch', 'managed embedder config digest changed');
  }
  let config;
  try { config = JSON.parse(bytes); } catch {
    throw new SupervisorError('managed_embedder_config_invalid', 'managed embedder config is not valid JSON');
  }
  if (Object.keys(config).sort().join('\0') !== [...MANAGED_CONFIG_KEYS].sort().join('\0') ||
      bytes !== `${canonicalJSON(config)}\n` || config.schema !== MANAGED_CONFIG_SCHEMA || config.protocol !== 1 ||
      config.model !== 'bge-m3' || config.dimensions !== 1024 || config.pooling !== 'cls' || config.normalized !== true ||
      ![config.embedder_runtime_activation_digest, config.embedder_runtime_tree_digest,
        config.model_activation_digest, config.model_tree_digest].every((value) => SHA256.test(value ?? '')) ||
      ![config.python_executable, config.helper_path, config.support_directory, config.model_file]
        .every((value) => typeof value === 'string' && isAbsolute(value))) {
    throw new SupervisorError('managed_embedder_config_invalid', 'managed embedder config contract is invalid');
  }
  for (const field of [
    'embedder_runtime_activation_digest', 'embedder_runtime_tree_digest',
    'model_activation_digest', 'model_tree_digest',
  ]) {
    if (managedEmbedder[field] !== undefined && managedEmbedder[field] !== config[field]) {
      throw new SupervisorError('managed_embedder_config_identity_mismatch', `${field} does not match the config`);
    }
  }
  return { ...managedEmbedder, config };
}

export function activateManagedEmbedderConfig(runtime, managedEmbedder) {
  if (!managedEmbedder || managedEmbedder.config_path !== join(runtime.data_dir, 'runtime', 'managed-embedder.json') ||
      !managedEmbedder.config ||
      createHash('sha256').update(`${canonicalJSON(managedEmbedder.config)}\n`).digest('hex') !== managedEmbedder.config_digest) {
    throw new SupervisorError('managed_embedder_config_invalid', 'managed embedder activation is invalid');
  }
  atomicPrivateJSON(managedEmbedder.config_path, managedEmbedder.config);
  return inspectManagedEmbedderConfig(runtime, managedEmbedder);
}

export function resolveManagedRuntime(runtime, {
  installRoot, publishConfig = true, verifiedActivations,
} = {}) {
  if (typeof installRoot !== 'string' || !isAbsolute(installRoot)) {
    throw new SupervisorError('managed_artifact_root_invalid', 'managed artifact root must be absolute');
  }
  const daemonActivation = verifiedActivations?.daemon;
  const embedderActivation = verifiedActivations?.embedderRuntime ?? verifiedActivations?.['embedder-runtime'];
  const modelActivation = verifiedActivations?.model;
  if (!daemonActivation || !embedderActivation || !modelActivation) {
    throw new SupervisorError(
      'managed_artifact_set_required',
      'managed product runtime requires one verified signed compatibility set',
    );
  }
  const daemonPath = activatedFile(daemonActivation, 'bin/pulse', { executable: true });
  const pythonExecutable = activatedFile(embedderActivation, 'runtime/bin/python3.12', { executable: true });
  const helperPath = activatedFile(embedderActivation, 'helper.py');
  const supportDirectory = activatedDirectory(embedderActivation, 'support');
  activatedFile(embedderActivation, 'support/config.json');
  activatedFile(embedderActivation, 'support/tokenizer.json');
  const modelFile = activatedFile(modelActivation, 'model.safetensors');
  const config = {
    dimensions: 1024,
    embedder_runtime_activation_digest: embedderActivation.activation_digest,
    embedder_runtime_tree_digest: embedderActivation.tree_digest,
    helper_path: helperPath,
    model: 'bge-m3',
    model_activation_digest: modelActivation.activation_digest,
    model_file: modelFile,
    model_tree_digest: modelActivation.tree_digest,
    normalized: true,
    pooling: 'cls',
    protocol: 1,
    python_executable: pythonExecutable,
    schema: MANAGED_CONFIG_SCHEMA,
    support_directory: supportDirectory,
  };
  ensurePrivateDirectory(runtime.data_dir);
  const configPath = join(runtime.data_dir, 'runtime', 'managed-embedder.json');
  let managedEmbedder = {
    config_path: configPath,
    config_digest: createHash('sha256').update(`${canonicalJSON(config)}\n`).digest('hex'),
    embedder_runtime_activation_digest: embedderActivation.activation_digest,
    embedder_runtime_tree_digest: embedderActivation.tree_digest,
    model_activation_digest: modelActivation.activation_digest,
    model_tree_digest: modelActivation.tree_digest,
    config,
  };
  if (publishConfig) managedEmbedder = activateManagedEmbedderConfig(runtime, managedEmbedder);
  const identity = (activation, path) => ({
    artifact_id: activation.artifact_id,
    artifact_sha256: activation.sha256,
    activation_digest: activation.activation_digest,
    tree_digest: activation.tree_digest,
    version: activation.version,
    version_path: activation.version_path,
    ...(path ? { path, digest: createHash('sha256').update(readFileSync(path)).digest('hex') } : {}),
  });
  return {
    schema: 'pulse.managed_product_runtime.v1',
    daemon: identity(daemonActivation, daemonPath),
    embedder_runtime: identity(embedderActivation),
    model: identity(modelActivation),
    managed_embedder: managedEmbedder,
  };
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
		receipt.binding_digest !== runtime.binding_digest ||
		(receipt.repository_id !== undefined && receipt.repository_id !== runtime.repository_id) ||
		receipt.kind !== runtime.kind ||
      receipt.store_id !== runtime.store_id || receipt.data_dir !== runtime.data_dir ||
      !Number.isSafeInteger(receipt.pid) || receipt.pid <= 1 || typeof receipt.executable !== 'string' ||
			!/^[a-f0-9]{64}$/.test(receipt.executable_digest ?? '') ||
			(receipt.stop_requested_at !== undefined &&
				(typeof receipt.stop_requested_at !== 'string' || Number.isNaN(Date.parse(receipt.stop_requested_at))))) {
    return false;
  }
  try {
    if (trustedExecutable(receipt.executable) !== receipt.executable ||
        executableDigest(receipt.executable) !== receipt.executable_digest) return false;
    const managed = managedEmbedderFromReceipt(receipt);
    if (managed === null) return false;
    if (managed) inspectManagedEmbedderConfig(runtime, managed);
    return true;
  } catch {
    return false;
  }
}

function managedEmbedderFromReceipt(receipt) {
  const mapping = {
    config_path: 'managed_embedder_config_path',
    config_digest: 'managed_embedder_config_digest',
    embedder_runtime_activation_digest: 'embedder_runtime_activation_digest',
    embedder_runtime_tree_digest: 'embedder_runtime_tree_digest',
    model_activation_digest: 'model_activation_digest',
    model_tree_digest: 'model_tree_digest',
  };
  const present = Object.values(mapping).filter((field) => receipt[field] !== undefined);
  if (present.length === 0) return undefined;
  if (present.length !== Object.keys(mapping).length) return null;
  const value = Object.fromEntries(Object.entries(mapping).map(([name, field]) => [name, receipt[field]]));
  if (![value.config_digest, value.embedder_runtime_activation_digest, value.embedder_runtime_tree_digest,
    value.model_activation_digest, value.model_tree_digest].every((digest) => SHA256.test(digest ?? '')) ||
    typeof value.config_path !== 'string' || !isAbsolute(value.config_path)) return null;
  return value;
}

function receiptProcessMatches(runtime, receipt, command = processCommand(receipt.pid)) {
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
    const managedEmbedder = managedEmbedderFromReceipt(receipt) || undefined;
    return {
      status: 'crashed', runtime, pid: receipt.pid, executable: receipt.executable,
      executable_digest: receipt.executable_digest, managed_embedder: managedEmbedder, fallback: false,
		legacy_authority: receipt.repository_id === undefined,
    };
  }
	if (!receiptProcessMatches(runtime, receipt, command)) {
    return { status: 'stale_or_mismatched', runtime, fallback: false };
  }
  return {
    status: 'running', runtime, pid: receipt.pid, executable: receipt.executable,
    executable_digest: receipt.executable_digest,
    managed_embedder: managedEmbedderFromReceipt(receipt) || undefined,
		legacy_authority: receipt.repository_id === undefined,
    fallback: false,
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

async function waitForVault(runtime, timeoutMs, { fullRetrieval = false, retrievalSmoke = fullRetrieval } = {}) {
  const secretPath = join(runtime.data_dir, 'secret.key');
  const deadline = Date.now() + timeoutMs;
	let lastCheck = 'daemon has not answered yet';
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
        if (response.ok) {
          if (!fullRetrieval) return;
			lastCheck = 'health is ready; memory status is pending';
          const status = await fetch(`${runtime.base_url}/memory/status`, {
            headers: { 'X-Pulse-Key': secret }, signal: AbortSignal.timeout(750),
          });
          if (status.ok) {
            const body = await status.json();
            if (body.full_retrieval === true && body.embedder === 'bge-m3') {
				if (!retrievalSmoke) return;
				lastCheck = 'managed embedder is ready; retrieval query is pending';
              const smoke = await fetch(`${runtime.base_url}/retrieve`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json', 'X-Pulse-Key': secret },
                body: JSON.stringify({ query: 'Pulse managed retrieval readiness', mode: 'factual', top_k: 1 }),
                signal: AbortSignal.timeout(1500),
              });
              if (smoke.ok) {
                const result = await smoke.json();
                if (Array.isArray(result.event_ids)) return;
				lastCheck = 'retrieval returned an invalid response contract';
			  } else {
				lastCheck = `retrieval returned HTTP ${smoke.status}`;
              }
			} else if (body.full_retrieval !== true) {
			  lastCheck = 'memory status reports full retrieval disabled';
			} else {
			  lastCheck = 'memory status reports an unexpected embedder';
            }
		  } else {
			lastCheck = `memory status returned HTTP ${status.status}`;
          }
		} else {
		  lastCheck = `health returned HTTP ${response.status}`;
        }
	  } else {
		lastCheck = 'daemon secret is pending';
      }
    } catch (error) {
      if (error instanceof SupervisorError) throw error;
	  lastCheck = `readiness request failed (${error?.name ?? 'Error'})`;
    }
    await new Promise((resolveDelay) => setTimeout(resolveDelay, 100));
  }
  throw new SupervisorError(
    fullRetrieval ? 'vault_full_retrieval_unavailable' : 'vault_start_timeout',
    fullRetrieval
	  ? `bound local vault did not pass the managed full-retrieval smoke: ${lastCheck}`
	  : `bound local vault did not become ready: ${lastCheck}`,
  );
}

export async function assertVaultRuntimeHealthy(runtime, {
	timeoutMs = 1500, status = inspectVaultRuntime(runtime), fullRetrievalSmoke = true,
} = {}) {
  if (status.status !== 'running') {
    throw new SupervisorError('vault_not_running', `bound local vault is ${status.status}`);
  }
	await waitForVault(runtime, timeoutMs, {
		fullRetrieval: Boolean(status.managed_embedder), retrievalSmoke: fullRetrievalSmoke,
	});
  return status;
}

export async function startVaultRuntime(runtime, {
  daemonPath, managedEmbedder, timeoutMs = 12000, host = 'pulse-product', allowRollback = true,
} = {}) {
  const executable = trustedExecutable(daemonPath);
  const desiredDigest = executableDigest(executable);
  const desiredManaged = managedEmbedder ? inspectManagedEmbedderConfig(runtime, managedEmbedder) : undefined;
  const status = inspectVaultRuntime(runtime);
  let rollbackPath;
	let rollbackManaged;
	if (status.status === 'running') {
		const sameManaged = status.managed_embedder?.config_digest === desiredManaged?.config_digest &&
			status.managed_embedder?.config_path === desiredManaged?.config_path;
		if (status.executable === executable && status.executable_digest === desiredDigest && sameManaged &&
			status.legacy_authority !== true) return status;
		rollbackPath = status.executable;
		rollbackManaged = status.managed_embedder;
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
      PULSE_REPOSITORY_ID: runtime.repository_id,
		PULSE_PRODUCT_WORKSPACE: runtime.workspace_path,
		PULSE_PRODUCT_AUTHORITY_NODE: process.execPath,
		PULSE_PRODUCT_AUTHORITY_HELPER: PRODUCT_BINDING_VERIFIER,
      PULSE_POLICY_EPOCH: '0',
      PULSE_RESOLVER_EPOCH: String(runtime.resolver_epoch),
      PULSE_DATA_DIR: runtime.data_dir,
      PULSE_CACHE_DIR: runtime.cache_dir,
      PULSE_HOST: host,
      PULSE_MANAGED_EMBEDDER_CONFIG: desiredManaged?.config_path ?? '',
      PULSE_LOCAL_EMBED_PYTHON: '', PULSE_LOCAL_EMBED_HELPER: '', PULSE_LOCAL_EMBED_MODEL: '',
      ANTHROPIC_API_KEY: '', OPENAI_API_KEY: '', COHERE_API_KEY: '',
    },
  });
  child.unref();
  closeSync(logFD);
  const receipt = {
    schema: 'pulse.local-vault-process.v1', pid: child.pid,
		executable, executable_digest: desiredDigest, binding_digest: runtime.binding_digest,
		repository_id: runtime.repository_id,
    kind: runtime.kind, store_id: runtime.store_id, data_dir: runtime.data_dir,
    started_at: new Date().toISOString(),
    ...(desiredManaged ? {
      managed_embedder_config_path: desiredManaged.config_path,
      managed_embedder_config_digest: desiredManaged.config_digest,
      embedder_runtime_activation_digest: desiredManaged.embedder_runtime_activation_digest,
      embedder_runtime_tree_digest: desiredManaged.embedder_runtime_tree_digest,
      model_activation_digest: desiredManaged.model_activation_digest,
      model_tree_digest: desiredManaged.model_tree_digest,
    } : {}),
  };
  const temporary = `${runtime.pid_file}.new`;
  try {
    writeFileSync(temporary, JSON.stringify(receipt), { mode: 0o600, flag: 'wx' });
    renameSync(temporary, runtime.pid_file);
    await waitForVault(runtime, timeoutMs, { fullRetrieval: Boolean(desiredManaged) });
    return { status: 'running', runtime, pid: child.pid, managed_embedder: desiredManaged, fallback: false };
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
          daemonPath: rollbackPath, managedEmbedder: rollbackManaged, timeoutMs, host, allowRollback: false,
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
