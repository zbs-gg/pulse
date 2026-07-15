import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { readFileSync, realpathSync, statfsSync, statSync } from 'node:fs';
import { homedir, totalmem } from 'node:os';
import { dirname, isAbsolute, join, resolve } from 'node:path';

import { assertSupportedNodeVersion, RELEASE_ARTIFACT_KINDS } from './release-manifest.js';
import { personalPrincipalPath } from './personal-principal.js';
import { DEFAULT_TRUST_PATHS } from './trust-helper.js';
import { canonicalizeWorkspace, defaultBindingPaths } from './workspace-binding.js';

const SCHEMA = 'pulse.personal_install_plan.v1';
const MINIMUM_MEMORY_BYTES = 8 * 1024 ** 3;
const INSTALL_HEADROOM_BYTES = 2 * 1024 ** 3;
const CURRENT_STATE_KEYS = Object.freeze([
  'binding', 'daemon', 'embedder', 'hook_trust', 'install_receipt',
  'plugin', 'presence', 'principal', 'runtime', 'vault',
]);
const DEFAULT_CODEX_CANDIDATES = Object.freeze([
  '/opt/homebrew/bin/codex',
  '/usr/local/bin/codex',
  '/usr/bin/codex',
]);
const RELEASE_REASON_CODES = new Set([
  'allowed_origins_invalid',
  'artifact_architecture_incompatible',
  'artifact_digest_invalid',
  'artifact_executable_invalid',
  'artifact_format_invalid',
  'artifact_identity_invalid',
  'artifact_minimum_os_incompatible',
  'artifact_minimum_os_invalid',
  'artifact_model_policy_invalid',
  'artifact_origin_not_allowed',
  'artifact_platform_incompatible',
  'artifact_signing_invalid',
  'artifact_size_invalid',
  'artifact_url_invalid',
  'artifact_version_incompatible',
  'envelope_schema_invalid',
  'manifest_epoch_downgrade',
  'manifest_epoch_invalid',
  'manifest_expired',
  'manifest_not_yet_valid',
  'manifest_schema_invalid',
  'manifest_validity_invalid',
  'minimum_epoch_file_invalid',
  'minimum_epoch_invalid',
  'minimum_epoch_lock_failed',
  'minimum_epoch_locked',
  'minimum_epoch_rollback',
  'minimum_epoch_unlock_failed',
  'minimum_epoch_write_failed',
  'personal_runtime_configuration_invalid',
  'release_identity_invalid',
  'release_key_epoch_invalid',
  'release_key_id_invalid',
  'release_key_id_mismatch',
  'release_key_invalid',
  'release_key_unknown',
  'release_manifest_noncanonical',
  'release_manifest_unavailable',
  'release_manifest_unsafe',
  'release_os_version_invalid',
  'release_override_forbidden',
  'release_package_version_incompatible',
  'release_package_version_invalid',
  'release_root_invalid',
  'release_signature_invalid',
  'release_test_asset_invalid',
  'release_test_asset_root_invalid',
  'release_test_asset_url_invalid',
  'release_test_materializer_forbidden',
  'release_test_materializer_spec_invalid',
  'trusted_keys_invalid',
  'verification_time_invalid',
]);

function nodeStatus(version) {
  const match = String(version ?? '').match(/^v?(\d+)\.(\d+)\.(\d+)/);
  const actual = match ? `${match[1]}.${match[2]}.${match[3]}` : null;
  let ok = false;
  if (actual) {
    try { assertSupportedNodeVersion(actual); ok = true; } catch { /* stable plan reason below */ }
  }
  return {
    actual,
    minimum: '20.0.0',
    ok,
  };
}

function canonicalCodexExecutable(path) {
  try {
    const target = realpathSync(path);
    const stat = statSync(target);
    if (!stat.isFile() || (stat.mode & 0o111) === 0) return null;
    return {
      path: target,
      sha256: createHash('sha256').update(readFileSync(target)).digest('hex'),
    };
  } catch {
    return null;
  }
}

export function detectCodexCLI({
  candidates = DEFAULT_CODEX_CANDIDATES,
  codexPath,
  versionProbe,
} = {}) {
  if (!Array.isArray(candidates) || candidates.length > 32 ||
      candidates.some((candidate) => typeof candidate !== 'string' || !isAbsolute(candidate) || resolve(candidate) !== candidate) ||
      (codexPath !== undefined && (typeof codexPath !== 'string' || !isAbsolute(codexPath) || resolve(codexPath) !== codexPath)) ||
      (versionProbe !== undefined && (typeof versionProbe !== 'function' || codexPath === undefined))) {
    throw new TypeError('codex_path_invalid');
  }
  const selected = codexPath
    ? canonicalCodexExecutable(codexPath)
    : candidates.map(canonicalCodexExecutable).find(Boolean);
  if (!selected) {
    return {
      available: false, executable_path: null, executable_sha256: null,
      version: null, reason_code: 'codex_missing',
    };
  }
  const identity = { executable_path: selected.path, executable_sha256: selected.sha256 };
  if (!versionProbe) return { available: true, ...identity, version: null, reason_code: null };

  let result;
  try { result = versionProbe(selected.path); } catch {
    return { available: false, ...identity, version: null, reason_code: 'codex_probe_failed' };
  }
  if (result?.status !== 0) return { available: false, ...identity, version: null, reason_code: 'codex_probe_failed' };
  const output = `${result.stdout ?? ''}\n${result.stderr ?? ''}`.slice(0, 4096);
  const match = output.match(/(?:^|\s)v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)(?:\s|$)/);
  if (!match) return { available: false, ...identity, version: null, reason_code: 'codex_version_invalid' };
  return { available: true, ...identity, version: match[1], reason_code: null };
}

export function detectInstallResources({ home = homedir(), spawn = spawnSync } = {}) {
  let diskFreeBytes = null;
  try {
    const info = statfsSync(home);
    const available = info.bavail ?? info.bfree;
    if (typeof available === 'bigint' || typeof info.bsize === 'bigint') {
      const bytes = BigInt(available) * BigInt(info.bsize);
      if (bytes <= BigInt(Number.MAX_SAFE_INTEGER)) diskFreeBytes = Number(bytes);
    } else if (Number.isSafeInteger(available) && Number.isSafeInteger(info.bsize)) {
      const bytes = available * info.bsize;
      if (Number.isSafeInteger(bytes)) diskFreeBytes = bytes;
    }
  } catch { /* report unknown without turning a read-only plan into a mutation */ }
  let port = 'unknown';
  const probe = spawn('/usr/sbin/lsof', ['-nP', '-iTCP:18789', '-sTCP:LISTEN'], {
    encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 3000, killSignal: 'SIGTERM',
  });
  if (probe?.status === 0) port = 'occupied';
  else if (probe?.status === 1 || probe?.error?.code === 'ENOENT') port = probe?.status === 1 ? 'free' : 'unknown';
  return {
    disk_free_bytes: diskFreeBytes,
    memory_total_bytes: totalmem(),
    port_18789: port,
  };
}

function releaseStatus(release) {
  if (!release) return null;
  if (release.schema !== 'pulse.verified_release_manifest.v1' ||
      typeof release.version !== 'string' || !Number.isSafeInteger(release.epoch) || release.epoch < 1 ||
      !/^[a-f0-9]{64}$/.test(release.manifest_digest ?? '') ||
      !release.artifacts || Object.keys(release.artifacts).sort().join('\0') !== [...RELEASE_ARTIFACT_KINDS].sort().join('\0')) {
    throw new TypeError('install_plan_release_invalid');
  }
  const artifacts = RELEASE_ARTIFACT_KINDS.map((kind) => {
    const artifact = release.artifacts[kind];
    if (!artifact || !Number.isSafeInteger(artifact.bytes) || artifact.bytes < 1 ||
        typeof artifact.id !== 'string' || typeof artifact.origin !== 'string' || typeof artifact.url !== 'string') {
      throw new TypeError('install_plan_release_invalid');
    }
    return {
      bytes: artifact.bytes,
      id: artifact.id,
      kind,
      origin: artifact.origin,
      url: artifact.url,
    };
  });
  return {
    artifacts,
    epoch: release.epoch,
    manifest_digest: release.manifest_digest,
    origins: [...new Set(artifacts.map((artifact) => artifact.origin))].sort(),
    total_download_bytes: artifacts.reduce((total, artifact) => total + artifact.bytes, 0),
    version: release.version,
  };
}

function installCurrentState(value) {
  const fallback = Object.fromEntries(CURRENT_STATE_KEYS.map((key) => [key, 'unknown']));
  if (value === undefined) return fallback;
  if (!value || Array.isArray(value) || Object.keys(value).sort().join('\0') !== [...CURRENT_STATE_KEYS].sort().join('\0') ||
      Object.values(value).some((status) => typeof status !== 'string' || !/^[a-z0-9_]{1,64}$/.test(status))) {
    throw new TypeError('install_plan_current_state_invalid');
  }
  return Object.fromEntries(CURRENT_STATE_KEYS.map((key) => [key, value[key]]));
}

function unsafeCurrentStateReasons(state) {
  const reasons = [];
  const exact = {
    'binding:conflict': 'binding_conflict',
    'binding:legacy': 'binding_legacy',
    'binding:repair_required': 'binding_repair_required',
    'binding:corrupt': 'binding_repair_required',
    'binding:invalid': 'binding_repair_required',
    'binding:present_unverified': 'binding_repair_required',
    'binding:unsafe': 'binding_repair_required',
    'presence:synthetic_test_authority': 'synthetic_authority_forbidden',
    'principal:corrupt': 'principal_repair_required',
    'principal:invalid': 'principal_repair_required',
    'principal:unsafe': 'principal_repair_required',
  };
  for (const key of CURRENT_STATE_KEYS) {
    const status = state[key];
    const mapped = exact[`${key}:${status}`];
    if (mapped) reasons.push(mapped);
    else if (['corrupt', 'invalid', 'unsafe'].includes(status)) reasons.push(`${key}_${status}`);
  }
  return reasons;
}

function verifiedReleaseReason(code) {
  if (code === undefined) return 'release_manifest_unavailable';
  if (typeof code !== 'string' || !RELEASE_REASON_CODES.has(code)) {
    throw new TypeError('install_plan_release_reason_invalid');
  }
  return code;
}

function planNextAction(outcome, reasons) {
  if (outcome === 'ready_to_install') {
    return {
      code: 'approve_install_disclosure',
      command: 'pulse install',
      requires_human_approval: true,
    };
  }
  return {
    code: reasons[0] ?? 'preflight_incomplete',
    command: null,
    requires_human_approval: false,
  };
}

function canonical(value) {
  if (value === null || typeof value === 'string' || typeof value === 'boolean') return JSON.stringify(value);
  if (typeof value === 'number' && Number.isSafeInteger(value)) return String(value);
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`;
  if (value && typeof value === 'object') {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}`;
  }
  throw new TypeError('install_plan_value_invalid');
}

export function canonicalInstallPlanJSON(plan) {
  return canonical(plan);
}

function formatBytes(bytes) {
  if (!Number.isSafeInteger(bytes) || bytes < 0) return 'unknown';
  if (bytes < 1024) return `${bytes} bytes`;
  if (bytes < 1024 ** 2) return `${(bytes / 1024).toFixed(1)} KiB`;
  if (bytes < 1024 ** 3) return `${(bytes / 1024 ** 2).toFixed(1)} MiB`;
  return `${(bytes / 1024 ** 3).toFixed(2)} GiB`;
}

export function formatPersonalInstallPlan(plan) {
  if (!plan || plan.schema !== SCHEMA || plan.contract_version !== 1) {
    throw new TypeError('install_plan_invalid');
  }
  const workspace = plan.detected?.workspace;
  const release = plan.release;
  const writes = plan.local_writes.map((entry) =>
    `  - ${entry.path} — ${entry.purpose}${entry.preserved_on_uninstall ? ' (preserved on uninstall)' : ''}`);
  const approvals = plan.required_human_approvals.map((entry) =>
    `  - ${entry.code} — must be approved by you`);
  const artifacts = release?.artifacts?.map((artifact) =>
    `  - ${artifact.kind}: ${formatBytes(artifact.bytes)} from ${artifact.origin}`) ?? ['  - signed release manifest unavailable'];
  const reasons = plan.reason_codes.length > 0
    ? `\nPreflight needs attention:\n${plan.reason_codes.map((code) => `  - ${code}`).join('\n')}\n`
    : '';
  const currentState = Object.entries(plan.current_state)
    .map(([name, status]) => `  - ${name}: ${status}`);
  return `Pulse Personal install

Project: ${workspace?.canonical_path ?? 'unavailable'}
Workspace: ${workspace?.workspace_id ?? 'unavailable'}
Repository: ${workspace?.repository_id ?? 'unavailable'}
Target: Codex on macOS Apple Silicon
Preflight: ${plan.outcome}

Current state:
${currentState.join('\n')}

Verified downloads (${formatBytes(release?.total_download_bytes)} total):
${artifacts.join('\n')}

Local writes:
${writes.join('\n')}

Machine resources:
  - free disk: ${formatBytes(plan.resources.disk_free_bytes)}
  - required disk: ${formatBytes(plan.resources.required_disk_bytes)}
  - memory: ${formatBytes(plan.resources.memory_total_bytes)}
  - required memory: ${formatBytes(plan.resources.minimum_memory_bytes)}
  - local port 18789: ${plan.resources.port_18789}

Network destinations after consent:
${(release?.origins ?? []).map((origin) => `  - ${origin}`).join('\n') || '  - none available until a signed release is present'}

Privacy:
  - raw transcript capture: ${plan.privacy.raw_transcript_capture}
  - backend model calls: ${plan.privacy.backend_model_calls}
  - old chat import: ${plan.privacy.old_chat_import}
  - memory: ${plan.privacy.memory_location}

Human approvals:
${approvals.join('\n')}

Removal boundary:
  - runtime uninstall: unavailable in this U3 build
  - disconnect removes only the Codex integration; the signed binding and Personal vault are preserved
  - wipe is separate, destructive, and requires fresh OS-backed presence
  - disconnect command: ${plan.rollback.remove_codex_connection}
${reasons}
Nothing above is written until you approve this disclosure in the interactive wizard.`;
}

function workspaceStatus(cwd, detector) {
  try {
    return { identity: detector(cwd), reason_code: null };
  } catch (error) {
    return {
      identity: null,
      reason_code: error?.code === 'workspace_not_git' ? 'workspace_not_git' : 'workspace_invalid',
    };
  }
}

export function buildPersonalInstallPlan({
  cwd = process.cwd(),
  home = homedir(),
  dataDir,
  codexHome = join(home, '.codex'),
  platform = process.platform,
  architecture = process.arch,
  nodeVersion = process.versions.node,
  detectWorkspace = canonicalizeWorkspace,
  detectCodex = detectCodexCLI,
  detectResources = detectInstallResources,
  release,
  releaseReasonCode,
  currentState,
} = {}) {
  const pulseRoot = dataDir ?? join(resolve(home), '.pulse');
  if (![cwd, home, codexHome, pulseRoot].every((value) => typeof value === 'string' && isAbsolute(value)) ||
      resolve(pulseRoot) !== pulseRoot) {
    throw new TypeError('install_plan_path_invalid');
  }
  const workspace = workspaceStatus(cwd, detectWorkspace);
  const codex = detectCodex();
  const node = nodeStatus(nodeVersion);
  const verifiedRelease = releaseStatus(release);
  const detectedCurrentState = installCurrentState(currentState);
  const resources = detectResources({ home });
  if (!resources || !['free', 'occupied', 'unknown'].includes(resources.port_18789) ||
      ![resources.disk_free_bytes, resources.memory_total_bytes]
        .every((value) => value === null || (Number.isSafeInteger(value) && value >= 0))) {
    throw new TypeError('install_plan_resources_invalid');
  }
  const requiredDiskBytes = (verifiedRelease?.total_download_bytes ?? 0) + INSTALL_HEADROOM_BYTES;
  const reasons = [];
  if (platform !== 'darwin') reasons.push('platform_unsupported');
  if (architecture !== 'arm64') reasons.push('architecture_unsupported');
  if (!node.ok) reasons.push('node_unsupported');
  if (workspace.reason_code) reasons.push(workspace.reason_code);
  if (!codex.available) reasons.push(codex.reason_code ?? 'codex_probe_failed');
  if (!verifiedRelease) reasons.push(verifiedReleaseReason(releaseReasonCode));
  reasons.push(...unsafeCurrentStateReasons(detectedCurrentState));
  if (resources.disk_free_bytes !== null && resources.disk_free_bytes < requiredDiskBytes) reasons.push('disk_insufficient');
  if (resources.memory_total_bytes !== null && resources.memory_total_bytes < MINIMUM_MEMORY_BYTES) reasons.push('memory_insufficient');
  const unsupported = reasons.some((reason) => [
    'platform_unsupported', 'architecture_unsupported', 'node_unsupported',
  ].includes(reason));
  const codexRoot = resolve(codexHome);
  const workspacePath = workspace.identity?.canonical_path ?? resolve(cwd);
  const bindingPaths = defaultBindingPaths(resolve(home));

  const outcome = unsupported ? 'unsupported' : reasons.length > 0 ? 'action_required' : 'ready_to_install';
  return {
    schema: SCHEMA,
    contract_version: 1,
    product: 'Pulse Personal',
    stage: 'personal_stage_1',
    target_host: 'codex',
    outcome,
    reason_codes: reasons,
    next_action: planNextAction(outcome, reasons),
    detected: {
      platform: { actual: platform, required: 'darwin', ok: platform === 'darwin' },
      architecture: { actual: architecture, required: 'arm64', ok: architecture === 'arm64' },
      node,
      codex,
      workspace: workspace.identity,
    },
    release: verifiedRelease,
    current_state: detectedCurrentState,
    resources: {
      ...resources,
      minimum_memory_bytes: MINIMUM_MEMORY_BYTES,
      required_disk_bytes: requiredDiskBytes,
    },
    local_writes: [
      { path: personalPrincipalPath(home), purpose: 'device_local_principal', preserved_on_uninstall: true },
      { path: join(pulseRoot, 'artifacts'), purpose: 'verified_release_artifacts', preserved_on_uninstall: false },
      { path: join(pulseRoot, 'runtime'), purpose: 'runtime_and_install_journal', preserved_on_uninstall: false },
      { path: bindingPaths.registryPath, purpose: 'signed_workspace_binding_registry', preserved_on_uninstall: true },
      { path: join(pulseRoot, 'vaults', 'personal', 'store_personal_<generated>'), purpose: 'private_memory_vault', preserved_on_uninstall: true },
      { path: join(pulseRoot, 'caches', 'personal', 'store_personal_<generated>'), purpose: 'rebuildable_retrieval_cache', preserved_on_uninstall: false },
      { path: join(pulseRoot, 'receipts', 'install', '<workspace_id>.json'), purpose: 'install_receipt', preserved_on_uninstall: false },
      { path: DEFAULT_TRUST_PATHS.helperPath, purpose: 'macos_presence_helper', preserved_on_uninstall: false },
      { path: dirname(DEFAULT_TRUST_PATHS.publicKeyPath), purpose: 'root_owned_binding_trust', preserved_on_uninstall: true },
      { path: join(codexRoot, 'pulse', 'product-locators.json'), purpose: 'codex_workspace_locator', preserved_on_uninstall: false },
      { path: join(codexRoot, 'plugins'), purpose: 'codex_managed_pulse_plugin', preserved_on_uninstall: false },
      { path: join(workspacePath, '.gitignore'), purpose: 'exclude_local_pulse_state', preserved_on_uninstall: false },
    ],
    network_effects: [
      { code: 'npx_package_cache_precedes_consent', destination: 'npm registry', product_mutation: false },
      {
        code: 'verified_release_downloads',
        destinations: verifiedRelease?.origins ?? [],
        artifacts: verifiedRelease?.artifacts ?? [],
        total_bytes: verifiedRelease?.total_download_bytes ?? null,
      },
      { code: 'codex_plugin_activation', destination: 'Codex native plugin manager' },
      { code: 'local_runtime', destination: '127.0.0.1 only' },
    ],
    privacy: {
      raw_transcript_capture: 'off',
      backend_model_calls: 'off',
      old_chat_import: 'not_requested',
      memory_location: 'local_private_vault',
    },
    required_human_approvals: [
      { code: 'install_disclosure_consent', automatable_by_yes: false },
      { code: 'macos_presence_and_binding', automatable_by_yes: false },
      { code: 'codex_hook_trust', automatable_by_yes: false },
      { code: 'binding_replacement_if_needed', automatable_by_yes: false },
    ],
    rollback: {
      remove_runtime: null,
      remove_codex_connection: 'pulse disconnect codex',
      runtime_uninstall: 'unavailable_in_u3',
      preserve_vault: true,
      destructive_wipe_separate: true,
    },
  };
}
