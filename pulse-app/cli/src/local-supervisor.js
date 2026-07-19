import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { closeSync, openSync } from 'node:fs';
import { dirname, isAbsolute, join, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

import { isCanonicalRepositoryID } from './host-adapter.js';
import { managedEmbedderRuntimeContract } from './managed-embedder-release.js';
import { defaultPlatformServices } from './platform-services.js';

const SHA256 = /^[a-f0-9]{64}$/;
const PRODUCT_BINDING_VERIFIER = fileURLToPath(new URL('./product-binding-verifier.js', import.meta.url));
const MANAGED_CONFIG_SCHEMA = 'pulse.managed_embedder.config.v2';
const LEGACY_MANAGED_CONFIG_SCHEMA = 'pulse.managed_embedder.config.v1';
const MANAGED_CONFIG_KEYS = [
  'embedder_runtime_activation_digest', 'embedder_runtime_tree_digest', 'engine', 'model_activation_digest',
  'model_root', 'model_tree_digest', 'protocol', 'runner_args', 'runner_path', 'schema', 'support_root',
  'vector_contract',
];
const VECTOR_CONTRACT_KEYS = [
  'dimensions', 'model', 'normalized', 'opset', 'pooling', 'quantization', 'revision', 'source',
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

function productAuthorityTestEnvironment(environment = process.env) {
  if (environment.PULSE_TRUST_MODE !== 'test') return {};
  const names = [
    'PULSE_BINDING_REGISTRY_PATH', 'PULSE_BINDING_PUBLIC_KEY_PATH', 'PULSE_BINDING_ANCHOR_PATH',
  ];
  if (names.some((name) => typeof environment[name] !== 'string' || !isAbsolute(environment[name]))) {
    return {};
  }
  return Object.fromEntries([
    ['PULSE_PRODUCT_AUTHORITY_TEST_MODE', '1'], ['PULSE_TRUST_MODE', 'test'],
    ...names.map((name) => [name, resolve(environment[name])]),
  ]);
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

function ensurePrivateDirectory(path, platformServices) {
  try { platformServices.ensurePrivateDirectory(path); } catch {
    throw new SupervisorError('vault_directory_unsafe', 'vault directory must be owner-only 0700 and not a symlink');
  }
}

function canonicalJSON(value) {
  if (value === null || typeof value !== 'object') return JSON.stringify(value);
  if (Array.isArray(value)) return `[${value.map((item) => canonicalJSON(item)).join(',')}]`;
  return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalJSON(value[key])}`).join(',')}}`;
}

function atomicPrivateJSON(path, value, platformServices) {
  ensurePrivateDirectory(dirname(path), platformServices);
  try {
    platformServices.atomicWritePrivateFile(path, `${canonicalJSON(value)}\n`, {
      ensureParent: false, maxBytes: 64 * 1024,
    });
  } catch (error) {
    if (error instanceof SupervisorError) throw error;
    throw new SupervisorError('vault_private_state_write_failed', 'vault private state cannot be written safely');
  }
}

function inside(root, candidate) {
  const base = resolve(root);
  const value = resolve(candidate);
  return value === base || value.startsWith(`${base}${sep}`);
}

function samePlatformPath(first, second, platformServices) {
  return platformServices.isPathInside(first, second) && platformServices.isPathInside(second, first);
}

function activatedFile(activation, relativePath, platformServices, { executable = false } = {}) {
  const path = join(activation.version_path, relativePath);
  if (!inside(activation.version_path, path)) {
    throw new SupervisorError('managed_artifact_path_invalid', 'managed artifact path escaped its activation');
  }
  let executableProof;
  try {
    platformServices.readIntegrityFile(path, { owner: 'current', maxBytes: 64 * 1024 * 1024 });
    if (executable) {
      executableProof = platformServices.inspectExecutable(path);
      if (!executableProof?.executable) throw new Error('not executable');
    }
  } catch {
    throw new SupervisorError('managed_artifact_file_unsafe', `managed artifact file is unsafe: ${relativePath}`);
  }
  return executable ? executableProof.canonical_path : path;
}

function activatedDirectory(activation, relativePath, platformServices) {
  const path = join(activation.version_path, relativePath);
  if (!inside(activation.version_path, path)) {
    throw new SupervisorError('managed_artifact_path_invalid', 'managed artifact path escaped its activation');
  }
  try { platformServices.assertPrivateState(path, { kind: 'directory' }); } catch {
    throw new SupervisorError('managed_artifact_directory_unsafe', `managed artifact directory is unsafe: ${relativePath}`);
  }
  return path;
}

export function inspectManagedEmbedderConfig(
  runtime, managedEmbedder, { platformServices = defaultPlatformServices } = {},
) {
  if (!managedEmbedder || typeof managedEmbedder !== 'object' ||
      managedEmbedder.config_path !== join(runtime.data_dir, 'runtime', 'managed-embedder.json') ||
      !SHA256.test(managedEmbedder.config_digest ?? '')) {
    throw new SupervisorError('managed_embedder_config_invalid', 'managed embedder config identity is invalid');
  }
  let bytes;
  try {
    bytes = platformServices.readPrivateFile(managedEmbedder.config_path, { minBytes: 1, maxBytes: 16 * 1024 });
  } catch {
    throw new SupervisorError('managed_embedder_config_unsafe', 'managed embedder config must be a private regular file');
  }
  if (createHash('sha256').update(bytes).digest('hex') !== managedEmbedder.config_digest) {
    throw new SupervisorError('managed_embedder_config_digest_mismatch', 'managed embedder config digest changed');
  }
  let config;
  try { config = JSON.parse(bytes); } catch {
    throw new SupervisorError('managed_embedder_config_invalid', 'managed embedder config is not valid JSON');
  }
  if (config?.schema === LEGACY_MANAGED_CONFIG_SCHEMA) {
    throw new SupervisorError('managed_embedder_config_legacy', 'managed embedder config v1 is historical and not ready');
  }
  const expectedVector = managedEmbedderRuntimeContract({ engine: 'transformers-js-onnx' }).vector_contract;
  const vector = config?.vector_contract;
  const argsValid = Array.isArray(config?.runner_args) && config.runner_args.length <= 8 &&
    config.runner_args.every((value) => typeof value === 'string' && value.length >= 1 && value.length <= 4096 &&
      !value.includes('\0') && !/[\r\n]/.test(value)) && Buffer.byteLength(config.runner_args.join('\0')) <= 16 * 1024;
  if (Object.keys(config).sort().join('\0') !== [...MANAGED_CONFIG_KEYS].sort().join('\0') ||
      bytes !== `${canonicalJSON(config)}\n` || config.schema !== MANAGED_CONFIG_SCHEMA || config.protocol !== 1 ||
      config.engine !== 'transformers-js-onnx' || !argsValid ||
      Object.keys(vector ?? {}).sort().join('\0') !== [...VECTOR_CONTRACT_KEYS].sort().join('\0') ||
      canonicalJSON(vector) !== canonicalJSON(expectedVector) ||
      ![config.embedder_runtime_activation_digest, config.embedder_runtime_tree_digest,
        config.model_activation_digest, config.model_tree_digest].every((value) => SHA256.test(value ?? '')) ||
      ![config.runner_path, config.model_root, config.support_root]
        .every((value) => typeof value === 'string' && isAbsolute(value)) ||
      !inside(config.model_root, config.support_root)) {
    throw new SupervisorError('managed_embedder_config_invalid', 'managed embedder config contract is invalid');
  }
  const expectedArgs = ['--model-root', config.model_root, '--support-root', config.support_root];
  if (canonicalJSON(config.runner_args) !== canonicalJSON(expectedArgs)) {
    throw new SupervisorError('managed_embedder_config_invalid', 'managed embedder runner arguments are invalid');
  }
  try {
    const runner = platformServices.inspectExecutable(config.runner_path);
    if (!runner?.executable || !samePlatformPath(runner.canonical_path, config.runner_path, platformServices)) {
      throw new Error('runner is not exact');
    }
    platformServices.assertPrivateState(config.model_root, { kind: 'directory' });
    platformServices.assertPrivateState(config.support_root, { kind: 'directory' });
    platformServices.inspectPathIdentity(join(config.model_root, 'model_int8.onnx'), { kind: 'file' });
    for (const controlPath of [
      join(config.model_root, 'pulse-model-contract.json'),
      join(config.support_root, 'config.json'),
      join(config.support_root, 'special_tokens_map.json'),
      join(config.support_root, 'tokenizer.json'),
      join(config.support_root, 'tokenizer_config.json'),
    ]) platformServices.readIntegrityFile(controlPath, { owner: 'current', maxBytes: 64 * 1024 * 1024 });
  } catch {
    throw new SupervisorError('managed_embedder_config_unsafe', 'managed embedder files do not match the private v2 contract');
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

export function activateManagedEmbedderConfig(
  runtime, managedEmbedder, { platformServices = defaultPlatformServices } = {},
) {
  if (!managedEmbedder || managedEmbedder.config_path !== join(runtime.data_dir, 'runtime', 'managed-embedder.json') ||
      !managedEmbedder.config ||
      createHash('sha256').update(`${canonicalJSON(managedEmbedder.config)}\n`).digest('hex') !== managedEmbedder.config_digest) {
    throw new SupervisorError('managed_embedder_config_invalid', 'managed embedder activation is invalid');
  }
  if (managedEmbedder.config.schema === LEGACY_MANAGED_CONFIG_SCHEMA) {
    throw new SupervisorError('managed_embedder_config_legacy', 'managed embedder config v1 is historical and not ready');
  }
  if (managedEmbedder.config.schema !== MANAGED_CONFIG_SCHEMA) {
    throw new SupervisorError('managed_embedder_config_invalid', 'managed embedder activation schema is invalid');
  }
  atomicPrivateJSON(managedEmbedder.config_path, managedEmbedder.config, platformServices);
  return inspectManagedEmbedderConfig(runtime, managedEmbedder, { platformServices });
}

export function resolveManagedRuntime(runtime, {
  installRoot, publishConfig = true, verifiedActivations, platformServices = defaultPlatformServices,
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
  const daemonRelativePath = platformServices.platform === 'win32' ? 'bin/pulse.exe' : 'bin/pulse';
  const daemonPath = activatedFile(daemonActivation, daemonRelativePath, platformServices, { executable: true });
  const modelRoot = activatedDirectory(modelActivation, '.', platformServices);
  const supportRoot = activatedDirectory(modelActivation, 'support', platformServices);
  activatedFile(modelActivation, 'model_int8.onnx', platformServices);
  activatedFile(modelActivation, 'pulse-model-contract.json', platformServices);
  for (const supportFile of [
    'config.json', 'special_tokens_map.json', 'tokenizer.json', 'tokenizer_config.json',
  ]) activatedFile(modelActivation, `support/${supportFile}`, platformServices);
  const contract = managedEmbedderRuntimeContract({
    engine: 'transformers-js-onnx', platform: platformServices.platform,
  });
  let runnerPath;
  try {
    runnerPath = activatedFile(embedderActivation, contract.runner_relative_path, platformServices, { executable: true });
  } catch { throw new SupervisorError('managed_embedder_portable_unavailable', 'portable managed embedder runner is unavailable'); }
  const runnerArgs = ['--model-root', modelRoot, '--support-root', supportRoot];
  const config = {
    embedder_runtime_activation_digest: embedderActivation.activation_digest,
    embedder_runtime_tree_digest: embedderActivation.tree_digest,
    engine: contract.engine,
    model_activation_digest: modelActivation.activation_digest,
    model_root: modelRoot,
    model_tree_digest: modelActivation.tree_digest,
    protocol: 1,
    runner_args: runnerArgs,
    runner_path: runnerPath,
    schema: MANAGED_CONFIG_SCHEMA,
    support_root: supportRoot,
    vector_contract: contract.vector_contract,
  };
  ensurePrivateDirectory(runtime.data_dir, platformServices);
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
  if (publishConfig) managedEmbedder = activateManagedEmbedderConfig(runtime, managedEmbedder, { platformServices });
  const identity = (activation, path) => ({
    artifact_id: activation.artifact_id,
    artifact_sha256: activation.sha256,
    activation_digest: activation.activation_digest,
    tree_digest: activation.tree_digest,
    version: activation.version,
    version_path: activation.version_path,
    ...(path ? { path, digest: createHash('sha256').update(platformServices.readIntegrityFile(path, {
      owner: 'current', maxBytes: 64 * 1024 * 1024,
    })).digest('hex') } : {}),
  });
  return {
    schema: 'pulse.managed_product_runtime.v1',
    daemon: identity(daemonActivation, daemonPath),
    embedder_runtime: identity(embedderActivation),
    model: identity(modelActivation),
    managed_embedder: managedEmbedder,
  };
}

function trustedExecutable(path, platformServices) {
  let proof;
  try {
    proof = platformServices.inspectExecutable(resolve(path));
    if (!proof) throw new Error('missing executable proof');
    platformServices.readIntegrityFile(proof.canonical_path, { owner: 'root-or-current', maxBytes: 64 * 1024 * 1024 });
  } catch {
    throw new SupervisorError('vault_binary_unsafe', 'vault daemon must be an owner/root-controlled executable');
  }
  return proof.canonical_path;
}

function executableDigest(path, platformServices) {
  return createHash('sha256').update(platformServices.readIntegrityFile(path, {
    owner: 'root-or-current', maxBytes: 64 * 1024 * 1024,
  })).digest('hex');
}

function readRuntimeReceipt(path, platformServices) {
  let bytes;
  try { bytes = platformServices.readPrivateFile(path, { missing: true, maxBytes: 8192 }); } catch {
    throw new SupervisorError('vault_runtime_receipt_unsafe', 'runtime receipt is unsafe');
  }
  if (bytes === null) return undefined;
  try {
    return JSON.parse(bytes);
  } catch {
    throw new SupervisorError('vault_runtime_receipt_invalid', 'runtime receipt is invalid');
  }
}

function processProof(pid, platformServices) {
  try {
    return platformServices.inspectProcess(pid);
  } catch {
    throw new SupervisorError('vault_process_inspection_unavailable', 'bound process identity cannot be proven');
  }
}

function receiptMetadataMatches(runtime, receipt, platformServices) {
  if (!receipt || receipt.schema !== 'pulse.local-vault-process.v1' ||
		receipt.binding_digest !== runtime.binding_digest ||
		(receipt.repository_id !== undefined && receipt.repository_id !== runtime.repository_id) ||
			receipt.kind !== runtime.kind ||
      receipt.store_id !== runtime.store_id || receipt.data_dir !== runtime.data_dir ||
      !Number.isSafeInteger(receipt.pid) || receipt.pid <= 1 || typeof receipt.executable !== 'string' ||
			!/^[a-f0-9]{64}$/.test(receipt.executable_digest ?? '') ||
			(receipt.startup_nonce !== undefined && !/^[a-f0-9]{64}$/.test(receipt.startup_nonce)) ||
			(receipt.process_identity_token !== undefined &&
				(typeof receipt.process_identity_token !== 'string' || receipt.process_identity_token.length < 1 ||
					receipt.process_identity_token.length > 1024)) ||
			(receipt.stop_requested_at !== undefined &&
				(typeof receipt.stop_requested_at !== 'string' || Number.isNaN(Date.parse(receipt.stop_requested_at))))) {
    return false;
  }
  try {
    if (trustedExecutable(receipt.executable, platformServices) !== receipt.executable ||
        executableDigest(receipt.executable, platformServices) !== receipt.executable_digest) return false;
    const managed = managedEmbedderFromReceipt(receipt);
    if (managed === null) return false;
    if (managed) inspectManagedEmbedderConfig(runtime, managed, { platformServices });
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

function receiptProcessMatches(runtime, receipt, proof) {
  const inspected = proof ?? processProof(receipt.pid, defaultPlatformServices);
  if (inspected.running !== true || inspected.pid !== receipt.pid) return false;
  if (receipt.process_identity_token !== undefined) {
    return inspected.identity_token === receipt.process_identity_token;
  }
  return inspected.command.includes(receipt.executable) &&
    inspected.command.includes(`-data-dir ${runtime.data_dir}`) && inspected.command.includes(`-addr ${runtime.addr}`);
}

export function inspectVaultRuntime(runtime, { platformServices = defaultPlatformServices } = {}) {
  const receipt = readRuntimeReceipt(runtime.pid_file, platformServices);
  if (!receipt) return { status: 'stopped', runtime, fallback: false };
  if (!receiptMetadataMatches(runtime, receipt, platformServices)) {
    return { status: 'stale_or_mismatched', runtime, fallback: false };
  }
  const proof = processProof(receipt.pid, platformServices);
  if (!proof.running) {
    const managedEmbedder = managedEmbedderFromReceipt(receipt) || undefined;
    return {
      status: 'crashed', runtime, pid: receipt.pid, executable: receipt.executable,
      executable_digest: receipt.executable_digest, managed_embedder: managedEmbedder, fallback: false,
		legacy_authority: receipt.repository_id === undefined,
      startup_nonce: receipt.startup_nonce,
    };
  }
	if (!receiptProcessMatches(runtime, receipt, proof)) {
    return { status: 'stale_or_mismatched', runtime, fallback: false };
  }
  return {
    status: 'running', runtime, pid: receipt.pid, executable: receipt.executable,
    executable_digest: receipt.executable_digest,
    managed_embedder: managedEmbedderFromReceipt(receipt) || undefined,
		legacy_authority: receipt.repository_id === undefined,
    startup_nonce: receipt.startup_nonce,
    fallback: false,
  };
}

async function waitForProcessExit(pid, timeoutMs = 3000, platformServices = defaultPlatformServices) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (!processProof(pid, platformServices).running) return;
    await new Promise((resolveDelay) => setTimeout(resolveDelay, 50));
  }
  throw new SupervisorError('vault_stop_timeout', 'bound local vault did not stop in time');
}

async function terminateSpawnedProcess(pid, platformServices) {
  if (!platformServices.terminateProcess(pid, { force: false })) return;
  try {
    await waitForProcessExit(pid, 3000, platformServices);
    return;
  } catch (error) {
    if (!(error instanceof SupervisorError) || error.code !== 'vault_stop_timeout') throw error;
  }
  if (!platformServices.terminateProcess(pid, { force: true })) return;
  await waitForProcessExit(pid, 1500, platformServices);
}

async function waitForVault(runtime, timeoutMs, {
  fullRetrieval = false, retrievalSmoke = fullRetrieval, startupNonce,
  platformServices = defaultPlatformServices,
} = {}) {
  const secretPath = join(runtime.data_dir, 'secret.key');
  const deadline = Date.now() + timeoutMs;
	let lastCheck = 'daemon has not answered yet';
  while (Date.now() < deadline) {
    try {
      const secret = platformServices.readPrivateFile(secretPath, { missing: true, minBytes: 64, maxBytes: 64 });
      if (secret !== null) {
        const response = await fetch(`${runtime.base_url}/health`, {
          headers: { 'X-Pulse-Key': secret }, signal: AbortSignal.timeout(500),
        });
        if (response.ok) {
          if (startupNonce !== undefined) {
            let health;
            try { health = await response.json(); } catch { health = null; }
            if (health?.startup_nonce !== startupNonce) {
              lastCheck = 'health startup nonce does not match the launched process';
              await new Promise((resolveDelay) => setTimeout(resolveDelay, 100));
              continue;
            }
          }
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
	  if (error?.code === 'platform_private_state_unsafe') {
		throw new SupervisorError('vault_secret_unsafe', 'vault IPC secret is unsafe');
	  }
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
	timeoutMs = 1500, status, fullRetrievalSmoke = true, platformServices = defaultPlatformServices,
} = {}) {
	status ??= inspectVaultRuntime(runtime, { platformServices });
  if (status.status !== 'running') {
    throw new SupervisorError('vault_not_running', `bound local vault is ${status.status}`);
  }
	await waitForVault(runtime, timeoutMs, {
		fullRetrieval: Boolean(status.managed_embedder), retrievalSmoke: fullRetrievalSmoke,
		startupNonce: status.startup_nonce, platformServices,
	});
  return status;
}

export async function startVaultRuntime(runtime, {
  daemonPath, managedEmbedder, timeoutMs = 12000, host = 'pulse-product', allowRollback = true,
  platformServices = defaultPlatformServices, requireStartupNonce = true,
} = {}) {
  const executable = trustedExecutable(daemonPath, platformServices);
  const desiredDigest = executableDigest(executable, platformServices);
  const desiredManaged = managedEmbedder
    ? inspectManagedEmbedderConfig(runtime, managedEmbedder, { platformServices }) : undefined;
  if (typeof requireStartupNonce !== 'boolean') {
    throw new SupervisorError('vault_startup_nonce_invalid', 'startup nonce policy is invalid');
  }
  const status = inspectVaultRuntime(runtime, { platformServices });
  let rollbackPath;
	let rollbackManaged;
	if (status.status === 'running') {
		const sameManaged = status.managed_embedder?.config_digest === desiredManaged?.config_digest &&
			status.managed_embedder?.config_path === desiredManaged?.config_path;
		if (status.executable === executable && status.executable_digest === desiredDigest && sameManaged &&
			(!requireStartupNonce || typeof status.startup_nonce === 'string') &&
			status.legacy_authority !== true) return status;
		rollbackPath = status.executable;
		rollbackManaged = status.managed_embedder;
		await stopVaultRuntimeAndWait(runtime, { platformServices });
  }
  if (status.status === 'crashed') {
    // Exact receipt metadata plus a dead PID is safe to recover. Never signal
    // it: the PID may already have been recycled after a reboot.
    platformServices.removePrivateFile(runtime.pid_file, { missing: true });
  }
  if (status.status === 'stale_or_mismatched') {
    throw new SupervisorError('vault_runtime_receipt_mismatch', 'refusing to replace a mismatched runtime receipt');
  }
  ensurePrivateDirectory(runtime.data_dir, platformServices);
  ensurePrivateDirectory(dirname(runtime.log_file), platformServices);
  ensurePrivateDirectory(runtime.cache_dir, platformServices);
  const logFD = openSync(runtime.log_file, 'a', 0o600);
  const startupNonce = requireStartupNonce ? platformServices.createStartupNonce() : undefined;
  const child = spawn(executable, ['-data-dir', runtime.data_dir, '-addr', runtime.addr], {
    detached: true,
    stdio: ['ignore', logFD, logFD],
    env: {
      HOME: process.env.HOME ?? '', PATH: '',
		...productAuthorityTestEnvironment(),
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
      PULSE_STARTUP_NONCE: startupNonce ?? '',
      PULSE_MANAGED_EMBEDDER_CONFIG: desiredManaged?.config_path ?? '',
      PULSE_LOCAL_EMBED_PYTHON: '', PULSE_LOCAL_EMBED_HELPER: '', PULSE_LOCAL_EMBED_MODEL: '',
      ANTHROPIC_API_KEY: '', OPENAI_API_KEY: '', COHERE_API_KEY: '',
    },
  });
  child.unref();
  closeSync(logFD);
  let spawnedProof;
  try {
    spawnedProof = processProof(child.pid, platformServices);
    if (!spawnedProof.running) {
      throw new SupervisorError('vault_process_identity_unavailable', 'launched process identity cannot be proven');
    }
  } catch (error) {
    try { await terminateSpawnedProcess(child.pid, platformServices); } catch { /* preserve identity failure */ }
    throw error;
  }
  const receipt = {
    schema: 'pulse.local-vault-process.v1', pid: child.pid,
		executable, executable_digest: desiredDigest, binding_digest: runtime.binding_digest,
		repository_id: runtime.repository_id,
    kind: runtime.kind, store_id: runtime.store_id, data_dir: runtime.data_dir,
    started_at: new Date().toISOString(),
    ...(startupNonce ? { startup_nonce: startupNonce } : {}),
    ...(typeof spawnedProof.identity_token === 'string' && spawnedProof.identity_token.length > 0
      ? { process_identity_token: spawnedProof.identity_token } : {}),
    ...(desiredManaged ? {
      managed_embedder_config_path: desiredManaged.config_path,
      managed_embedder_config_digest: desiredManaged.config_digest,
      embedder_runtime_activation_digest: desiredManaged.embedder_runtime_activation_digest,
      embedder_runtime_tree_digest: desiredManaged.embedder_runtime_tree_digest,
      model_activation_digest: desiredManaged.model_activation_digest,
      model_tree_digest: desiredManaged.model_tree_digest,
    } : {}),
  };
  try {
    platformServices.atomicWritePrivateFile(runtime.pid_file, `${canonicalJSON(receipt)}\n`, {
      ensureParent: false, maxBytes: 8192,
    });
    await waitForVault(runtime, timeoutMs, {
      fullRetrieval: Boolean(desiredManaged), startupNonce, platformServices,
    });
    return {
      status: 'running', runtime, pid: child.pid, managed_embedder: desiredManaged,
      startup_nonce: startupNonce, fallback: false,
    };
  } catch (error) {
	let terminationError;
	try {
		await terminateSpawnedProcess(child.pid, platformServices);
	} catch (failure) {
		terminationError = failure;
	}
	try { platformServices.removePrivateFile(runtime.pid_file, { missing: true }); } catch { /* preserve activation failure */ }
	let rollbackError;
    if (allowRollback && rollbackPath) {
      try {
        await startVaultRuntime(runtime, {
          daemonPath: rollbackPath, managedEmbedder: rollbackManaged, timeoutMs, host, allowRollback: false,
          platformServices, requireStartupNonce: Boolean(status.startup_nonce),
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

export function stopVaultRuntime(runtime, { platformServices = defaultPlatformServices } = {}) {
  const receipt = readRuntimeReceipt(runtime.pid_file, platformServices);
  if (!receipt) return { status: 'stopped', runtime, fallback: false };
  const proof = processProof(receipt.pid, platformServices);
  if (!receiptMetadataMatches(runtime, receipt, platformServices) || !receiptProcessMatches(runtime, receipt, proof)) {
    throw new SupervisorError('vault_runtime_receipt_mismatch', 'refusing to signal a mismatched process');
  }
	const stoppingReceipt = { ...receipt, stop_requested_at: new Date().toISOString() };
	platformServices.atomicWritePrivateFile(runtime.pid_file, `${canonicalJSON(stoppingReceipt)}\n`, {
		ensureParent: false, maxBytes: 8192,
	});
	platformServices.terminateProcess(receipt.pid, { force: false });
	return { status: 'stopping', runtime, pid: receipt.pid, fallback: false };
}

export async function stopVaultRuntimeAndWait(runtime, {
  timeoutMs = 3000, platformServices = defaultPlatformServices,
} = {}) {
	let receipt = readRuntimeReceipt(runtime.pid_file, platformServices);
	if (!receipt) return { status: 'stopped', runtime, fallback: false };
	if (!receiptMetadataMatches(runtime, receipt, platformServices)) {
		throw new SupervisorError('vault_runtime_receipt_mismatch', 'refusing to stop a mismatched process');
	}
	if (receipt.stop_requested_at === undefined) {
		const proof = processProof(receipt.pid, platformServices);
		if (!proof.running) {
			platformServices.removePrivateFile(runtime.pid_file, { missing: true });
			return { status: 'stopped', runtime, fallback: false };
		}
		if (!receiptProcessMatches(runtime, receipt, proof)) {
			throw new SupervisorError('vault_runtime_receipt_mismatch', 'refusing to stop a mismatched process');
		}
		stopVaultRuntime(runtime, { platformServices });
		receipt = readRuntimeReceipt(runtime.pid_file, platformServices);
	}
	await waitForProcessExit(receipt.pid, timeoutMs, platformServices);
	platformServices.removePrivateFile(runtime.pid_file, { missing: true });
	return { status: 'stopped', runtime, fallback: false };
}
