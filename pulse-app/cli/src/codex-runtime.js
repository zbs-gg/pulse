import { createHash } from 'node:crypto';
import { existsSync, readdirSync } from 'node:fs';
import { isAbsolute, join, resolve } from 'node:path';

import { readCommittedArtifactSet } from './artifact-installer.js';
import { inspectCodexRuntimeAt } from './codex-install.js';
import { RELEASE_ARTIFACT_KINDS, RELEASE_REQUIRED_ARTIFACT_KINDS } from './release-manifest.js';
import { inspectWorkspaceBinding, resolveWorkspaceBinding } from './workspace-binding.js';
import { stageUnassignedCapsule } from './unassigned-inbox.js';
import {
	SupervisorError,
	activateManagedEmbedderConfig,
	assertVaultRuntimeHealthy,
	inspectManagedEmbedderConfig,
	inspectVaultRuntime,
	resolveManagedRuntime,
	startVaultRuntime,
	stopVaultRuntimeAndWait,
	vaultRuntimeFromBinding,
} from './local-supervisor.js';
import { captureEnabledForHost } from './capture-state.js';
import { callTeamRemoteTool, isReadOnlyTeamTool } from './team-remote-client.js';
import { renderGitTeamMemoryCards } from './host-adapter.js';
import { ensureBoundPortableProjectID, readBoundProjectSourceWindow } from './project-source.js';
import { publishGitTeamMemory, syncCommittedGitTeamMemory } from './git-team-memory.js';
import { defaultPlatformServices, PlatformServicesError } from './platform-services.js';

const STABLE_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$/;

function mappedPlatformError(label, error) {
	if (error instanceof PlatformServicesError) {
		throw new Error(`${label}_unsafe:${error.code}`);
	}
	throw error;
}

function readPrivateText(path, maxBytes, label, platformServices = defaultPlatformServices) {
	try {
		return platformServices.readPrivateFile(path, { encoding: 'utf8', minBytes: 1, maxBytes });
	} catch (error) {
		return mappedPlatformError(label, error);
	}
}

function readPrivateJSON(path, maxBytes, label, platformServices = defaultPlatformServices) {
	return JSON.parse(readPrivateText(path, maxBytes, label, platformServices));
}

function samePlatformPath(left, right, platformServices = defaultPlatformServices) {
	return platformServices.isPathInside(left, right) && platformServices.isPathInside(right, left);
}

function inspectPrivateExecutable(path, label, platformServices = defaultPlatformServices) {
	try {
		platformServices.assertPrivateState(path, { kind: 'file' });
		const proof = platformServices.inspectExecutable(path);
		if (!proof || proof.owner_only !== true || proof.regular_file !== true || proof.reparse_point !== false) {
			throw new Error(`${label}_unsafe`);
		}
		return proof;
	} catch (error) {
		if (error instanceof PlatformServicesError) return mappedPlatformError(label, error);
		throw error;
	}
}

export function readProductActivationBundle(
	dataDir = process.env.PULSE_DATA_DIR,
	{ verifyArtifacts = true, platformServices = defaultPlatformServices } = {},
) {
	if (typeof dataDir !== 'string' || !isAbsolute(dataDir)) throw new Error('product_activation_data_dir_invalid');
	const path = join(resolve(dataDir), 'runtime', 'product-daemon.json');
	const activation = readPrivateJSON(path, 8192, 'product_activation', platformServices);
	if (activation?.schema === 'pulse.product_activation.v2') {
		throw new Error('product_activation_v2_not_ready');
	}
	if (activation?.schema === 'pulse.product_activation.v3') {
		throw new Error('product_activation_v3_not_ready');
	}
	const allowed = [
		'activated_at',
		'daemon_activation_digest', 'daemon_artifact_id', 'daemon_artifact_sha256', 'daemon_digest', 'daemon_path', 'daemon_tree_digest',
		'embedder_runtime_activation_digest', 'embedder_runtime_artifact_id', 'embedder_runtime_artifact_sha256', 'embedder_runtime_tree_digest',
		'model_activation_digest', 'model_artifact_id', 'model_artifact_sha256', 'model_tree_digest',
		'plugin_runtime_activation_digest', 'plugin_runtime_artifact_id', 'plugin_runtime_artifact_sha256', 'plugin_runtime_tree_digest',
		'plugin_tree_digest',
		'release_epoch', 'release_manifest_digest', 'release_version',
		'runtime_path', 'runtime_tree_digest', 'schema',
	];
	const digestFields = [
		'daemon_activation_digest', 'daemon_artifact_sha256', 'daemon_digest', 'daemon_tree_digest',
		'embedder_runtime_activation_digest', 'embedder_runtime_artifact_sha256', 'embedder_runtime_tree_digest',
		'model_activation_digest', 'model_artifact_sha256', 'model_tree_digest',
		'plugin_runtime_activation_digest', 'plugin_runtime_artifact_sha256', 'plugin_runtime_tree_digest',
		'plugin_tree_digest', 'release_manifest_digest',
		'runtime_tree_digest',
	];
	const idFields = [
		'daemon_artifact_id', 'embedder_runtime_artifact_id', 'model_artifact_id', 'plugin_runtime_artifact_id',
	];
	if (activation?.schema !== 'pulse.product_activation.v4' ||
			Object.keys(activation).length !== allowed.length || Object.keys(activation).some((name) => !allowed.includes(name)) ||
			![activation.daemon_path, activation.runtime_path]
				.every((value) => typeof value === 'string' && isAbsolute(value)) ||
			!digestFields.every((field) => /^[a-f0-9]{64}$/.test(activation[field] ?? '')) ||
			!idFields.every((field) => /^[a-z0-9][a-z0-9._-]{0,127}$/.test(activation[field] ?? '')) ||
			typeof activation.release_version !== 'string' || activation.release_version.length < 1 ||
			!Number.isSafeInteger(activation.release_epoch) || activation.release_epoch < 1 ||
			typeof activation.activated_at !== 'string' || Number.isNaN(Date.parse(activation.activated_at))) {
		throw new Error('product_activation_invalid');
	}
	const expectedRuntimePath = join(resolve(dataDir), 'runtime', 'codex', 'current', 'src', 'cli.js');
	if (resolve(activation.runtime_path) !== expectedRuntimePath) throw new Error('product_activation_runtime_mismatch');
	const inspectedRuntime = inspectCodexRuntimeAt(activation.runtime_path);
	if (!inspectedRuntime.ok) throw new Error(`product_activation_runtime_invalid:${inspectedRuntime.detail}`);
	const manifest = inspectedRuntime.manifest;
	if (manifest?.schema !== 'pulse.codex_runtime.v2' ||
			manifest.tree_digest !== activation.runtime_tree_digest || manifest.entrypoint !== 'src/cli.js' ||
			manifest.release_manifest_digest !== activation.release_manifest_digest ||
			manifest.release_version !== activation.release_version || manifest.release_epoch !== activation.release_epoch ||
			manifest.plugin_runtime_artifact_id !== activation.plugin_runtime_artifact_id ||
			manifest.plugin_runtime_artifact_sha256 !== activation.plugin_runtime_artifact_sha256 ||
			manifest.plugin_runtime_activation_digest !== activation.plugin_runtime_activation_digest ||
			manifest.plugin_runtime_tree_digest !== activation.plugin_runtime_tree_digest ||
			manifest.plugin_tree_digest !== activation.plugin_tree_digest) {
		throw new Error('product_activation_runtime_digest_mismatch');
	}
	const daemonProof = inspectPrivateExecutable(activation.daemon_path, 'product_daemon', platformServices);
	if (daemonProof.sha256 !== activation.daemon_digest) {
		throw new Error('product_activation_daemon_digest_mismatch');
	}
	if (!verifyArtifacts) return { activation, daemonProof };
	const installRoot = join(resolve(dataDir), 'artifacts');
	const committedSet = readCommittedArtifactSet({ installRoot });
	const committed = committedSet.activations;
	const daemon = committed.daemon;
	const embedderRuntime = committed['embedder-runtime'];
	const model = committed.model;
	const pluginRuntime = committed['plugin-runtime'];
	const presenceHelper = committed['presence-helper'];
	const committedKinds = Object.keys(committed);
	if (RELEASE_REQUIRED_ARTIFACT_KINDS.some((kind) => !committedKinds.includes(kind)) ||
			committedKinds.some((kind) => !RELEASE_ARTIFACT_KINDS.includes(kind)) ||
			!daemon || !embedderRuntime || !model || !pluginRuntime ||
			committedSet.record.manifest_digest !== activation.release_manifest_digest ||
			committedSet.record.version !== activation.release_version ||
			committedSet.record.epoch !== activation.release_epoch ||
			daemon.artifact_id !== activation.daemon_artifact_id || daemon.sha256 !== activation.daemon_artifact_sha256 ||
			daemon.activation_digest !== activation.daemon_activation_digest || daemon.tree_digest !== activation.daemon_tree_digest ||
			!samePlatformPath(
				daemonProof.canonical_path,
				inspectPrivateExecutable(
					join(daemon.version_path, 'bin', platformServices.platform === 'win32' ? 'pulse.exe' : 'pulse'),
					'product_daemon_artifact', platformServices,
				).canonical_path,
				platformServices,
			) ||
			embedderRuntime.artifact_id !== activation.embedder_runtime_artifact_id ||
			embedderRuntime.sha256 !== activation.embedder_runtime_artifact_sha256 ||
			embedderRuntime.activation_digest !== activation.embedder_runtime_activation_digest ||
			embedderRuntime.tree_digest !== activation.embedder_runtime_tree_digest ||
			model.artifact_id !== activation.model_artifact_id || model.sha256 !== activation.model_artifact_sha256 ||
			model.activation_digest !== activation.model_activation_digest || model.tree_digest !== activation.model_tree_digest ||
			pluginRuntime.artifact_id !== activation.plugin_runtime_artifact_id ||
			pluginRuntime.sha256 !== activation.plugin_runtime_artifact_sha256 ||
			pluginRuntime.activation_digest !== activation.plugin_runtime_activation_digest ||
			pluginRuntime.tree_digest !== activation.plugin_runtime_tree_digest) {
		throw new Error('product_activation_artifact_identity_mismatch');
	}
	return {
		activation,
		activations: { daemon, embedderRuntime, model, pluginRuntime, presenceHelper },
		committedSet,
		daemonProof,
	};
}

export function readProductActivation(dataDir = process.env.PULSE_DATA_DIR, options = {}) {
	return readProductActivationBundle(dataDir, options).activation;
}

export async function acquireVaultActivationLock(runtime, { platformServices = defaultPlatformServices } = {}) {
	try {
		platformServices.ensurePrivateDirectory(runtime.data_dir);
		const release = platformServices.acquirePrivateLock(
			join(runtime.data_dir, 'supervisor-activation.lock'),
			{ staleAfterMs: 30_000, timeoutMs: 3_000 },
		);
		return async () => release();
	} catch (error) {
		if (error instanceof PlatformServicesError) {
			if (error.code === 'platform_lock_occupied') throw new Error('vault_activation_lock_timeout');
			if (error.code === 'platform_native_adapter_unavailable') throw new Error('vault_activation_lock_unavailable');
			throw new Error(`vault_activation_lock_failed:${error.code}`);
		}
		throw error;
	}
}

function managedRuntimeForActivation(runtime, bundle, {
	publishConfig = false, platformServices = defaultPlatformServices,
} = {}) {
	const { activation, activations } = bundle;
	const dataDir = process.env.PULSE_DATA_DIR;
	if (typeof dataDir !== 'string' || !isAbsolute(dataDir)) throw new Error('product_activation_data_dir_invalid');
	const managed = resolveManagedRuntime(runtime, {
		installRoot: join(resolve(dataDir), 'artifacts'), publishConfig, verifiedActivations: activations,
	});
	if (managed.daemon.artifact_id !== activation.daemon_artifact_id ||
			managed.daemon.artifact_sha256 !== activation.daemon_artifact_sha256 ||
			managed.daemon.activation_digest !== activation.daemon_activation_digest ||
			managed.daemon.tree_digest !== activation.daemon_tree_digest ||
			managed.daemon.digest !== activation.daemon_digest ||
			!samePlatformPath(
				inspectPrivateExecutable(managed.daemon.path, 'product_managed_daemon', platformServices).canonical_path,
				inspectPrivateExecutable(activation.daemon_path, 'product_daemon', platformServices).canonical_path,
				platformServices,
			) ||
			managed.embedder_runtime.artifact_id !== activation.embedder_runtime_artifact_id ||
			managed.embedder_runtime.artifact_sha256 !== activation.embedder_runtime_artifact_sha256 ||
			managed.embedder_runtime.activation_digest !== activation.embedder_runtime_activation_digest ||
			managed.embedder_runtime.tree_digest !== activation.embedder_runtime_tree_digest ||
			managed.model.artifact_id !== activation.model_artifact_id ||
			managed.model.artifact_sha256 !== activation.model_artifact_sha256 ||
			managed.model.activation_digest !== activation.model_activation_digest ||
			managed.model.tree_digest !== activation.model_tree_digest) {
		throw new Error('product_activation_managed_runtime_mismatch');
	}
	return managed;
}

function runtimeMatchesActivation(
	status, activation, daemonProof, managedEmbedder, platformServices = defaultPlatformServices,
) {
	const managedIdentityMatches = status.managed_embedder?.config_path ===
		join(status.runtime.data_dir, 'runtime', 'managed-embedder.json') &&
		status.managed_embedder?.embedder_runtime_activation_digest === activation.embedder_runtime_activation_digest &&
		status.managed_embedder?.embedder_runtime_tree_digest === activation.embedder_runtime_tree_digest &&
		status.managed_embedder?.model_activation_digest === activation.model_activation_digest &&
		status.managed_embedder?.model_tree_digest === activation.model_tree_digest;
	return status.status === 'running' && samePlatformPath(status.executable, daemonProof.canonical_path, platformServices) &&
			status.executable_digest === activation.daemon_digest &&
			managedIdentityMatches && (!managedEmbedder || (
				status.managed_embedder.config_path === managedEmbedder.config_path &&
				status.managed_embedder.config_digest === managedEmbedder.config_digest
			));
}

export async function ensureActivatedVaultRuntime(resolved, { platformServices = defaultPlatformServices } = {}) {
	let bundle = readProductActivationBundle(undefined, { verifyArtifacts: false, platformServices });
	let { activation } = bundle;
	let status = inspectVaultRuntime(resolved.runtime, { platformServices });
	if (runtimeMatchesActivation(status, activation, bundle.daemonProof, undefined, platformServices)) {
		try {
			await assertVaultRuntimeHealthy(resolved.runtime, { status, fullRetrievalSmoke: false, platformServices });
			return activation;
		} catch (error) {
			if (!(error instanceof SupervisorError) || error.code !== 'vault_full_retrieval_unavailable') throw error;
			// The daemon identity is exact but its managed helper died. Re-check
			// under the vault lock and restart the same committed generation once.
		}
	}
	if (!['running', 'stopped', 'crashed'].includes(status.status)) {
		throw new Error(`product_vault_${status.status}`);
	}
	const release = await acquireVaultActivationLock(resolved.runtime, { platformServices });
	try {
		bundle = readProductActivationBundle(undefined, { platformServices });
		activation = bundle.activation;
		let managedRuntime = managedRuntimeForActivation(resolved.runtime, bundle, { platformServices });
		status = inspectVaultRuntime(resolved.runtime, { platformServices });
		if (runtimeMatchesActivation(
			status, activation, bundle.daemonProof, managedRuntime.managed_embedder, platformServices,
		)) {
			try {
				await assertVaultRuntimeHealthy(resolved.runtime, { status, fullRetrievalSmoke: false, platformServices });
				return activation;
			} catch (error) {
				if (!(error instanceof SupervisorError) || error.code !== 'vault_full_retrieval_unavailable') throw error;
				await stopVaultRuntimeAndWait(resolved.runtime);
		status = inspectVaultRuntime(resolved.runtime, { platformServices });
			}
		}
		let started = false;
		if (!runtimeMatchesActivation(
			status, activation, bundle.daemonProof, managedRuntime.managed_embedder, platformServices,
		)) {
			if (!['running', 'stopped', 'crashed'].includes(status.status)) {
				throw new Error(`product_vault_${status.status}`);
			}
			let previous;
			const configChanged = ['running', 'crashed'].includes(status.status) &&
				status.managed_embedder?.config_digest !== managedRuntime.managed_embedder.config_digest;
			if (configChanged) {
				previous = {
					daemonPath: status.executable,
					managedEmbedder: inspectManagedEmbedderConfig(resolved.runtime, status.managed_embedder),
				};
				await stopVaultRuntimeAndWait(resolved.runtime);
			}
			managedRuntime.managed_embedder = activateManagedEmbedderConfig(
				resolved.runtime, managedRuntime.managed_embedder,
			);
			try {
				await startVaultRuntime(resolved.runtime, {
					daemonPath: activation.daemon_path,
					managedEmbedder: managedRuntime.managed_embedder,
					host: 'pulse-product',
				});
				started = true;
			} catch (error) {
				if (previous) {
					try {
						previous.managedEmbedder = activateManagedEmbedderConfig(
							resolved.runtime, previous.managedEmbedder,
						);
						await startVaultRuntime(resolved.runtime, {
							daemonPath: previous.daemonPath,
							managedEmbedder: previous.managedEmbedder,
							host: 'pulse-product', allowRollback: false,
						});
					} catch (rollbackError) {
						throw new Error(`product_vault_upgrade_and_rollback_failed:${error.message}:${rollbackError.message}`);
					}
				}
				throw error;
			}
		}
		if (!started) {
			await assertVaultRuntimeHealthy(resolved.runtime, {
				status, fullRetrievalSmoke: false,
			});
		}
		return activation;
	} finally {
		await release();
	}
}

function canonicalJSON(value) {
  if (value === null || typeof value !== 'object') return JSON.stringify(value);
  if (Array.isArray(value)) return `[${value.map((item) => canonicalJSON(item)).join(',')}]`;
  return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalJSON(value[key])}`).join(',')}}`;
}

function hostSlug(host) {
  if (host === 'codex') return 'codex';
  if (host === 'claude-code') return 'claude-code';
  if (host === 'cursor') return 'cursor';
  throw new Error('unsupported_host_adapter');
}

const LOCAL_PRODUCT_TOOL_ACTIONS = new Set([
  'pulse_remember',
  'pulse_source_register', 'pulse_source_window', 'pulse_source_status',
  'pulse_shared_stage', 'pulse_shared_inspect', 'pulse_shared_edit',
  'pulse_shared_reject', 'pulse_shared_cards',
  'pulse_shared_publish', 'pulse_shared_sync',
]);

function productToolAction(toolName) {
  if (typeof toolName === 'string') {
    const action = toolName.split('__').at(-1)?.toLowerCase();
    if (LOCAL_PRODUCT_TOOL_ACTIONS.has(action)) return action;
  }
  throw new Error('invalid_product_tool_action');
}

function hostToolInputDigest(host, toolName, toolInput) {
  const action = productToolAction(toolName);
  return createHash('sha256')
    .update('pulse-host-tool-input-v1\x1f')
    .update(host)
    .update('\x1f')
    .update(action)
    .update('\x1f')
    .update(canonicalJSON(toolInput))
    .digest('hex');
}

export function resolveCodexRuntime(input = {}) {
  const cwd = typeof input === 'string' ? input : input.cwd;
  const binding = resolveProductWorkspaceBinding({ cwd });
  const runtime = vaultRuntimeFromBinding(binding);
  return { binding, runtime };
}

export function resolveProductWorkspaceBinding({ cwd = process.cwd() } = {}) {
  const registryPath = process.env.PULSE_BINDING_REGISTRY_PATH;
  const publicKeyPath = process.env.PULSE_BINDING_PUBLIC_KEY_PATH;
  const anchorPath = process.env.PULSE_BINDING_ANCHOR_PATH;
  const custom = registryPath !== undefined || publicKeyPath !== undefined || anchorPath !== undefined;
  if (custom && process.env.PULSE_TRUST_MODE !== 'test') {
    throw new Error('caller-controlled Pulse binding authority is forbidden in product mode');
  }
  if (process.env.PULSE_TRUST_MODE === 'test' && (!registryPath || !publicKeyPath || !anchorPath)) {
    throw new Error('synthetic test authority requires registry, public key, and anti-rollback anchor paths');
  }
  return resolveWorkspaceBinding({
    cwd,
    ...(process.env.PULSE_TRUST_MODE === 'test' ? { registryPath, publicKeyPath, anchorPath } : {}),
  });
}

export function inspectProductWorkspaceBinding({ cwd = process.cwd() } = {}) {
  const registryPath = process.env.PULSE_BINDING_REGISTRY_PATH;
  const publicKeyPath = process.env.PULSE_BINDING_PUBLIC_KEY_PATH;
  const anchorPath = process.env.PULSE_BINDING_ANCHOR_PATH;
  const custom = registryPath !== undefined || publicKeyPath !== undefined || anchorPath !== undefined;
  if (custom && process.env.PULSE_TRUST_MODE !== 'test') {
    throw new Error('caller-controlled Pulse binding authority is forbidden in product mode');
  }
  if (process.env.PULSE_TRUST_MODE === 'test' && (!registryPath || !publicKeyPath || !anchorPath)) {
    throw new Error('synthetic test authority requires registry, public key, and anti-rollback anchor paths');
  }
  return inspectWorkspaceBinding({
    cwd,
    ...(process.env.PULSE_TRUST_MODE === 'test' ? { registryPath, publicKeyPath, anchorPath } : {}),
  });
}

export function stageUnassignedProductCandidate(host, input, idempotencyKey, {
  inspectBinding = inspectProductWorkspaceBinding,
  stage = stageUnassignedCapsule,
  path,
} = {}) {
  if (!['codex', 'claude-code', 'cursor'].includes(host)) throw new Error('unassigned_host_invalid');
  const inspected = inspectBinding({ cwd: process.cwd() });
  if (inspected?.status !== 'unassigned' ||
      !['binding_missing', 'workspace_not_git'].includes(inspected.reason)) {
    throw new Error('unassigned_staging_forbidden');
  }
  return stage(input, {
    ...(path === undefined ? {} : { path }),
    host,
    idempotencyKey,
  });
}

function bindTeamActiveContext(binding, input) {
	if (!input || typeof input !== 'object' || Array.isArray(input)) {
		throw new Error('product_team_context_invalid');
	}
	const supplied = input.active_context ?? {};
	if (!supplied || typeof supplied !== 'object' || Array.isArray(supplied) ||
		Object.keys(supplied).some((field) => !['project_id', 'repo_id', 'agent_id', 'session_id'].includes(field))) {
		throw new Error('product_team_context_invalid');
	}
	const fixed = {
		project_id: binding?.commons?.project_id,
		repo_id: binding?.workspace?.repository_id,
		agent_id: binding?.principal_ref,
	};
	if (Object.values(fixed).some((value) => typeof value !== 'string' || !STABLE_ID.test(value))) {
		throw new Error('product_team_binding_invalid');
	}
	for (const [field, value] of Object.entries(fixed)) {
		if (supplied[field] !== undefined && supplied[field] !== value) {
			throw new Error('product_team_context_mismatch');
		}
	}
	if (supplied.session_id !== undefined &&
		(typeof supplied.session_id !== 'string' || !STABLE_ID.test(supplied.session_id))) {
		throw new Error('product_team_context_invalid');
	}
	return {
		...input,
		active_context: {
			...fixed,
			...(supplied.session_id === undefined ? {} : { session_id: supplied.session_id }),
		},
	};
}

export async function callBoundTeamTool(resolved, host, name, input, {
	resolveBinding = resolveProductWorkspaceBinding,
	teamRequest = callTeamRemoteTool,
} = {}) {
	if (!['codex', 'claude-code', 'cursor'].includes(host) || !isReadOnlyTeamTool(name) ||
		!resolved?.binding || !/^[a-f0-9]{64}$/.test(resolved.binding.binding_digest ?? '') ||
		!Number.isSafeInteger(resolved.binding.resolver_epoch) || resolved.binding.resolver_epoch < 1) {
		throw new Error('product_team_tool_forbidden');
	}
	const binding = resolveBinding({ cwd: process.cwd() });
	if (binding.mode !== 'team' || binding.fallback !== false ||
		binding.binding_digest !== resolved.binding.binding_digest ||
		binding.resolver_epoch !== resolved.binding.resolver_epoch) {
		throw new Error('product_team_binding_changed');
	}
	return teamRequest(binding, name, bindTeamActiveContext(binding, input));
}

export function resolveBoundCodexRuntime(input = {}, {
	host = 'codex', platformServices = defaultPlatformServices,
} = {}) {
  const resolved = resolveCodexRuntime(input);
  const { runtime } = resolved;
  const capturePath = join(runtime.data_dir, 'capture-state.json');
	let captureRaw;
	try {
		captureRaw = platformServices.readPrivateFile(capturePath, {
			missing: true, encoding: 'utf8', minBytes: 1, maxBytes: 8192,
		});
	} catch (error) {
		return mappedPlatformError('capture_state', error);
	}
	if (captureRaw === null) throw new Error('capture_state_missing');
	const capture = JSON.parse(captureRaw);
  if (!captureEnabledForHost(capture, host)) {
    throw new Error('capture_disabled');
  }
  return resolved;
}

export function readRuntimeSecret(runtime, { platformServices = defaultPlatformServices } = {}) {
  const path = join(runtime.data_dir, 'secret.key');
	const secret = readPrivateText(path, 64, 'vault_secret', platformServices);
	if (Buffer.byteLength(secret) !== 64) throw new Error('vault_secret_unsafe');
  if (!/^[a-f0-9]{64}$/.test(secret)) throw new Error('vault_secret_invalid');
  return secret;
}

export function productBindingRequestHeaders(resolved) {
	const binding = resolved?.binding;
	const workspace = binding?.workspace?.canonical_path;
	const repositoryID = binding?.workspace?.repository_id;
	if (typeof workspace !== 'string' || !isAbsolute(workspace) ||
		!/^[a-f0-9]{64}$/.test(binding?.binding_digest ?? '') ||
		typeof repositoryID !== 'string' || !repositoryID.startsWith('repository_') ||
		!Number.isSafeInteger(binding?.resolver_epoch) || binding.resolver_epoch < 1) {
		throw new Error('product_binding_request_invalid');
	}
	const encodedWorkspace = Buffer.from(workspace, 'utf8').toString('base64url');
	if (Buffer.byteLength(workspace, 'utf8') > 4096 || encodedWorkspace.length === 0) {
		throw new Error('product_binding_request_invalid');
	}
	return {
		'X-Pulse-Product-Workspace': encodedWorkspace,
		'X-Pulse-Product-Binding': binding.binding_digest,
		'X-Pulse-Product-Repository': repositoryID,
		'X-Pulse-Product-Resolver-Epoch': String(binding.resolver_epoch),
	};
}

export async function boundPulseRequest(resolved, path, options = {}) {
  const method = options.method ?? 'POST';
  const headers = {
    Accept: 'application/json',
		...productBindingRequestHeaders(resolved),
    'X-Pulse-Key': readRuntimeSecret(resolved.runtime, {
		platformServices: options.platformServices ?? defaultPlatformServices,
	}),
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

export async function activatedBoundPulseRequest(resolved, path, options = {}) {
	await ensureActivatedVaultRuntime(resolved, {
		platformServices: options.platformServices ?? defaultPlatformServices,
	});
	return boundPulseRequest(resolved, path, options);
}

function closedLocalToolInput(input, allowed, required = allowed) {
  if (!input || typeof input !== 'object' || Array.isArray(input) ||
      Object.keys(input).some((key) => !allowed.includes(key)) ||
      required.some((key) => input[key] === undefined)) {
    throw new Error('product_local_tool_input_invalid');
  }
  return input;
}

function localGitMemoryAuthority(resolved, portableProjectID) {
  const projectID = portableProjectID(resolved);
  const repositoryID = resolved?.binding?.workspace?.repository_id;
  const bindingDigest = resolved?.binding?.binding_digest;
  if (!/^project_[a-f0-9]{32}$/.test(projectID ?? '') ||
      typeof repositoryID !== 'string' || !repositoryID.startsWith('repository_') ||
      !/^[a-f0-9]{64}$/.test(bindingDigest ?? '')) {
    throw new Error('product_local_tool_authority_invalid');
  }
  return {
    portable_project_id: projectID,
    repository_id: repositoryID,
    binding_digest: bindingDigest,
  };
}

export async function callBoundLocalProductTool(resolved, host, name, input, {
  now = new Date(),
  resolveBinding = resolveProductWorkspaceBinding,
  runtimeFromBinding = vaultRuntimeFromBinding,
  portableProjectID = ensureBoundPortableProjectID,
  readSourceWindow = readBoundProjectSourceWindow,
  syncSharedMemory = syncCommittedGitTeamMemory,
  request = activatedBoundPulseRequest,
} = {}) {
  const action = productToolAction(name);
  if (!['codex', 'claude-code', 'cursor'].includes(host) || action === 'pulse_remember' ||
      !resolved?.binding || !/^[a-f0-9]{64}$/.test(resolved.binding.binding_digest ?? '') ||
      !Number.isSafeInteger(resolved.binding.resolver_epoch) || resolved.binding.resolver_epoch < 1) {
    throw new Error('product_local_tool_forbidden');
  }
  const binding = resolveBinding({ cwd: process.cwd() });
  if (binding.binding_digest !== resolved.binding.binding_digest ||
      binding.resolver_epoch !== resolved.binding.resolver_epoch ||
      binding.workspace?.canonical_path !== resolved.binding.workspace?.canonical_path) {
    throw new Error('product_local_binding_changed');
  }
  const current = { binding, runtime: runtimeFromBinding(binding) };
  const turn = consumeHostToolLease(current, host, action, input, now);
  const authority = localGitMemoryAuthority(current, portableProjectID);

  if (action === 'pulse_source_window') {
    return readSourceWindow(current, closedLocalToolInput(
      input, ['locator', 'cursor', 'max_bytes', 'expected_version_digest'],
      ['locator', 'cursor', 'max_bytes'],
    ));
  }
  if (action === 'pulse_source_register') {
    const body = closedLocalToolInput(input, ['locator']);
    const observed = readSourceWindow(current, { locator: body.locator, cursor: 0, max_bytes: 64 });
    return request(current, '/project/sources/register', { body: {
      schema: 'pulse.project_source.register.v1', ...authority,
      source_kind: observed.source_kind, locator: observed.locator,
      version_digest: observed.version_digest, byte_count: observed.byte_count,
      observed_at: now.toISOString(),
    } });
  }
  if (action === 'pulse_source_status') {
    const body = closedLocalToolInput(input, ['source_id']);
    return request(current, '/project/sources/status', { body: {
      schema: 'pulse.project_source.status.v1', ...authority, source_id: body.source_id,
    } });
  }
  if (action === 'pulse_shared_stage') {
    const body = closedLocalToolInput(input,
      ['source_id', 'source_version_digest', 'candidates', 'raw_input_included']);
    return request(current, '/project/shared-memory/review/stage', { body: {
      schema: 'pulse.git_team_memory.stage.v1', ...authority,
      host, task_id: turn.turn_id,
      idempotency_key: `shared_stage_${hostToolInputDigest(host, action, input)}`,
      source_id: body.source_id, source_version_digest: body.source_version_digest,
      candidates: body.candidates, raw_input_included: body.raw_input_included,
    } });
  }
  if (action === 'pulse_shared_inspect' || action === 'pulse_shared_cards') {
    const body = action === 'pulse_shared_cards'
      ? closedLocalToolInput(input, ['batch_id', 'approver_label'])
      : closedLocalToolInput(input, ['batch_id']);
    const batch = await request(current, '/project/shared-memory/review/inspect', { body: {
      schema: 'pulse.git_team_memory.inspect.v1', ...authority, batch_id: body.batch_id,
    } });
    if (action === 'pulse_shared_inspect') return batch;
    const cards = renderGitTeamMemoryCards(batch, { approverLabel: body.approver_label });
    return {
      schema: 'pulse.git_team_memory.cards.v1', batch_id: cards.batch_id,
      batch_generation: cards.batch_generation, card_block: cards.block,
      card_block_digest: cards.card_block_digest, candidate_digests: cards.candidate_digests,
      approver_label_digest: cards.approver_label_digest,
    };
  }
  if (action === 'pulse_shared_edit') {
    const body = closedLocalToolInput(input, ['candidate_id', 'expected_version', 'candidate']);
    return request(current, `/project/shared-memory/review/candidates/${encodeURIComponent(body.candidate_id)}/edit`, {
      body: {
        schema: 'pulse.git_team_memory.edit.v1', ...authority,
        candidate_id: body.candidate_id, expected_version: body.expected_version, candidate: body.candidate,
      },
    });
  }
  if (action === 'pulse_shared_reject') {
    const body = closedLocalToolInput(input, ['candidate_id', 'expected_version', 'reason_code']);
    return request(current, `/project/shared-memory/review/candidates/${encodeURIComponent(body.candidate_id)}/reject`, {
      body: {
        schema: 'pulse.git_team_memory.reject.v1', ...authority,
        candidate_id: body.candidate_id, expected_version: body.expected_version, reason_code: body.reason_code,
      },
    });
  }
  if (action === 'pulse_shared_publish') {
    const body = closedLocalToolInput(input, ['approval_lease_id', 'approver_label']);
    return publishGitTeamMemory(current, body, {
      beginPublication: (publication) => request(current, '/project/shared-memory/publications/start', {
        body: {
          schema: 'pulse.git_team_memory.publication_start.v1', ...authority,
          approval_lease_id: publication.approval_lease_id,
          approver_label: publication.approver_label,
          expected_parent: publication.expected_parent,
        },
      }),
      finalizePublication: (publication) => request(current, '/project/shared-memory/publications/finalize', {
        body: {
          schema: 'pulse.git_team_memory.publication_finalize.v1', ...authority,
          publication_id: publication.publication_id,
          files_digest: publication.files_digest,
          outcome: publication.outcome,
          commit_hash: publication.commit_hash,
        },
      }),
    });
  }
  if (action === 'pulse_shared_sync') {
    closedLocalToolInput(input, [], []);
    return syncSharedMemory(current, {
      ensureProjectID: portableProjectID,
      requestIndex: (index) => request(current, '/project/shared-memory/index', {
        body: index, timeoutMs: 45_000,
      }),
    });
  }
  throw new Error('product_local_tool_forbidden');
}

export function codexTurnContextPath(dataDir, sessionID) {
  return hostTurnContextPath(dataDir, 'codex', sessionID);
}

export function hostTurnContextPath(dataDir, host, sessionID) {
  if (!STABLE_ID.test(sessionID ?? '')) throw new Error('invalid_session_id');
  const slug = hostSlug(host);
  const digest = createHash('sha256').update('pulse-host-turn-context-v1\x1f').update(host).update('\x1f').update(sessionID).digest('hex');
  return join(dataDir, `${slug}-turn-context`, `${digest}.json`);
}

export function codexFinalizeMarkerPath(dataDir, sessionID, turnID) {
  return hostFinalizeMarkerPath(dataDir, 'codex', sessionID, turnID);
}

export function hostFinalizeMarkerPath(dataDir, host, sessionID, turnID) {
  if (!STABLE_ID.test(sessionID ?? '') || !STABLE_ID.test(turnID ?? '')) {
    throw new Error('invalid_turn_identity');
  }
  const slug = hostSlug(host);
  const digest = createHash('sha256')
    .update('pulse-host-finalize-marker-v1\x1f')
    .update(host)
    .update('\x1f')
    .update(sessionID)
    .update('\x1f')
    .update(turnID)
    .digest('hex');
  return join(dataDir, `${slug}-turn-finalized`, `${digest}.json`);
}

function atomicWriteJSON(path, value, {
	platformServices = defaultPlatformServices, maxBytes = 8192, label = 'private_state',
} = {}) {
	const data = `${JSON.stringify(value)}\n`;
	try {
		platformServices.atomicWritePrivateFile(path, data, { ensureParent: true, maxBytes });
	} catch (error) {
		return mappedPlatformError(label, error);
	}
}

function readOwnerJSON(path, maxBytes, unsafeCode, platformServices = defaultPlatformServices) {
	try {
		return JSON.parse(platformServices.readPrivateFile(path, {
			encoding: 'utf8', minBytes: 1, maxBytes,
		}));
	} catch (error) {
		if (error instanceof PlatformServicesError) throw new Error(`${unsafeCode}:${error.code}`);
		throw error;
	}
}

export function writeCodexTurnContext(resolved, event, now = new Date(), options = {}) {
	return writeHostTurnContext(resolved, event, 'codex', now, options);
}

export function writeHostTurnContext(resolved, event, host, now = new Date(), {
	platformServices = defaultPlatformServices,
} = {}) {
  const slug = hostSlug(host).replaceAll('-', '_');
  const context = {
    schema: `pulse.${slug}_turn_context.v1`,
    host,
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
	atomicWriteJSON(hostTurnContextPath(resolved.runtime.data_dir, host, event.session_id), context, {
		platformServices, maxBytes: 8192, label: 'host_turn_context',
	});
  return context;
}

export function writeCodexToolLease(resolved, event, toolName, toolInput, toolUseID, now = new Date(), options = {}) {
	return writeHostToolLease(resolved, event, 'codex', toolName, toolInput, toolUseID, now, options);
}

export function writeHostToolLease(
	resolved, event, host, toolName, toolInput, toolUseID, now = new Date(),
	{ platformServices = defaultPlatformServices } = {},
) {
  const slug = hostSlug(host).replaceAll('-', '_');
  if (!STABLE_ID.test(toolUseID ?? '')) throw new Error('invalid_host_tool_lease');
  const action = productToolAction(toolName);
  const lease = {
    schema: `pulse.${slug}_tool_lease.v1`,
    host,
    session_id: event.session_id,
    turn_id: event.turn_id,
    workspace: resolved.binding.workspace.canonical_path,
    source_event_key: event.source_event_key,
    idempotency_key: event.idempotency_key,
    binding_digest: resolved.binding.binding_digest,
    policy_epoch: 0,
    resolver_epoch: resolved.binding.resolver_epoch,
    tool_name: action,
    tool_input_digest: hostToolInputDigest(host, toolName, toolInput),
    issued_at: now.toISOString(),
    expires_at: new Date(now.valueOf() + 30_000).toISOString(),
  };
  const name = createHash('sha256')
    .update('pulse-host-tool-lease-v1\x1f')
    .update(host)
    .update('\x1f')
    .update(event.source_event_key)
    .update('\x1f')
    .update(toolUseID)
    .digest('hex');
	const directory = join(resolved.runtime.data_dir, `${hostSlug(host)}-tool-leases`, lease.tool_input_digest);
	let release;
	try {
		platformServices.ensurePrivateDirectory(directory);
		release = platformServices.acquirePrivateLock(join(directory, '.consume.lock'), {
			staleAfterMs: 30_000, timeoutMs: 3_000,
		});
		atomicWriteJSON(join(directory, `${name}.json`), lease, {
			platformServices, maxBytes: 8192, label: 'host_tool_lease',
		});
	} catch (error) {
		if (error instanceof PlatformServicesError) throw new Error(`host_tool_lease_unsafe:${error.code}`);
		throw error;
	} finally {
		release?.();
	}
	return lease;
}

export function consumeCodexToolLease(resolved, toolName, toolInput, now = new Date(), options = {}) {
	return consumeHostToolLease(resolved, 'codex', toolName, toolInput, now, options);
}

export function consumeHostToolLease(resolved, host, toolName, toolInput, now = new Date(), {
	platformServices = defaultPlatformServices,
} = {}) {
  const slug = hostSlug(host).replaceAll('-', '_');
  const action = productToolAction(toolName);
  const inputDigest = hostToolInputDigest(host, toolName, toolInput);
	const directory = join(resolved.runtime.data_dir, `${hostSlug(host)}-tool-leases`, inputDigest);
	if (!existsSync(directory)) throw new Error('host_tool_lease_unavailable');
	let release;
	try {
		platformServices.assertPrivateState(directory, { kind: 'directory' });
		release = platformServices.acquirePrivateLock(join(directory, '.consume.lock'), {
			staleAfterMs: 30_000, timeoutMs: 3_000,
		});
		const matches = [];
		for (const name of readdirSync(directory)) {
			if (!/^[a-f0-9]{64}\.json$/.test(name)) continue;
			const path = join(directory, name);
			let raw;
			try {
				raw = platformServices.readPrivateFile(path, { encoding: 'utf8', minBytes: 1, maxBytes: 8192 });
			} catch {
				continue;
			}
			let lease;
			try { lease = JSON.parse(raw); } catch {
				platformServices.removePrivateFile(path, { missing: true });
				continue;
			}
			const expiry = Date.parse(lease?.expires_at);
			const issued = Date.parse(lease?.issued_at);
			if (!Number.isNaN(expiry) && expiry <= now.valueOf()) {
				platformServices.removePrivateFile(path, { missing: true });
				continue;
			}
			if (lease?.schema !== `pulse.${slug}_tool_lease.v1` || lease.host !== host ||
					lease.workspace !== resolved.binding.workspace.canonical_path ||
					lease.binding_digest !== resolved.binding.binding_digest || lease.policy_epoch !== 0 ||
					lease.resolver_epoch !== resolved.binding.resolver_epoch || lease.tool_name !== action ||
					lease.tool_input_digest !== inputDigest || !STABLE_ID.test(lease.session_id ?? '') ||
					!STABLE_ID.test(lease.turn_id ?? '') || !/^event_[a-f0-9]{64}$/.test(lease.source_event_key ?? '') ||
					!/^lifecycle:[a-f0-9]{64}$/.test(lease.idempotency_key ?? '') ||
					Number.isNaN(issued) || issued > now.valueOf() + 5_000 || Number.isNaN(expiry)) continue;
			matches.push({ path, lease, issued });
		}
		if (matches.length === 0) throw new Error('host_tool_lease_unavailable');
		const turnKey = (match) => [
			match.lease.session_id, match.lease.turn_id, match.lease.source_event_key,
			match.lease.idempotency_key,
		].join('\x1f');
		if (new Set(matches.map(turnKey)).size !== 1) throw new Error('host_tool_lease_ambiguous');
		matches.sort((left, right) => right.issued - left.issued || right.path.localeCompare(left.path));
		const selected = matches[0];
		platformServices.removePrivateFile(selected.path, { missing: false });
		for (const stale of matches.slice(1)) platformServices.removePrivateFile(stale.path, { missing: true });
		return selected.lease;
	} catch (error) {
		if (error instanceof PlatformServicesError) throw new Error(`host_tool_lease_unavailable:${error.code}`);
		throw error;
	} finally {
		release?.();
	}
}

export function readCodexTurnContext(resolved, event, now = new Date(), options = {}) {
	return readHostTurnContext(resolved, event, 'codex', now, options);
}

export function readHostTurnContext(resolved, event, host, now = new Date(), {
	platformServices = defaultPlatformServices,
} = {}) {
  const slug = hostSlug(host).replaceAll('-', '_');
  const path = hostTurnContextPath(resolved.runtime.data_dir, host, event.session_id);
	const context = readOwnerJSON(path, 8192, 'host_turn_context_unsafe', platformServices);
  if (context?.schema !== `pulse.${slug}_turn_context.v1` || context.host !== host ||
      context.session_id !== event.session_id || context.turn_id !== event.turn_id ||
      context.source_event_key !== event.source_event_key ||
      context.idempotency_key !== event.idempotency_key ||
      context.workspace !== resolved.binding.workspace.canonical_path ||
      context.binding_digest !== resolved.binding.binding_digest ||
      context.resolver_epoch !== resolved.binding.resolver_epoch || context.policy_epoch !== 0 ||
      Number.isNaN(Date.parse(context.expires_at)) || Date.parse(context.expires_at) <= now.valueOf()) {
    throw new Error('host_turn_context_stale');
  }
  return context;
}

export function readCodexFinalizeMarker(resolved, event, options = {}) {
	return readHostFinalizeMarker(resolved, event, 'codex', options);
}

export function writeHostFinalizeMarker(resolved, event, host, result, now = new Date(), {
	platformServices = defaultPlatformServices,
} = {}) {
  const slug = hostSlug(host).replaceAll('-', '_');
  const finalize = result?.finalize_receipt;
  const marker = {
    schema: `pulse.${slug}_finalize_marker.v1`,
    host,
    session_id: event.session_id,
    turn_id: event.turn_id,
    source_event_key: event.source_event_key,
    binding_digest: resolved.binding.binding_digest,
    ledger_id: result?.ledger_id,
    receipt_id: finalize?.receipt_id,
    status: result?.status,
    observed_at: now.toISOString(),
  };
	atomicWriteJSON(hostFinalizeMarkerPath(
		resolved.runtime.data_dir, host, event.session_id, event.turn_id,
	), marker, { platformServices, maxBytes: 4096, label: 'host_finalize_marker' });
  return marker;
}

export function readHostFinalizeMarker(resolved, event, host, {
	platformServices = defaultPlatformServices,
} = {}) {
  const slug = hostSlug(host).replaceAll('-', '_');
  const path = hostFinalizeMarkerPath(resolved.runtime.data_dir, host, event.session_id, event.turn_id);
	const marker = readOwnerJSON(path, 4096, 'host_finalize_marker_unsafe', platformServices);
  if (marker?.schema !== `pulse.${slug}_finalize_marker.v1` || marker.host !== host ||
      marker.session_id !== event.session_id || marker.turn_id !== event.turn_id ||
      marker.binding_digest !== resolved.binding.binding_digest ||
      marker.source_event_key !== event.source_event_key ||
      !STABLE_ID.test(marker.ledger_id ?? '') || !STABLE_ID.test(marker.receipt_id ?? '') ||
      !['candidates', 'rejected'].includes(marker.status)) {
    throw new Error('host_finalize_marker_stale');
  }
  return marker;
}
