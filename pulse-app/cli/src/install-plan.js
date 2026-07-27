import { statfsSync } from 'node:fs';
import { homedir, totalmem } from 'node:os';
import { basename, dirname, isAbsolute, join, resolve } from 'node:path';

import { DesktopTargetError, detectDesktopLibc, resolveDesktopTarget } from './desktop-target.js';
import { createPlatformServices, defaultPlatformServices } from './platform-services.js';
import { assertSupportedNodeVersion } from './release-manifest.js';
import { detectCodexCLI, detectSupportedHosts, SUPPORTED_HOST_IDS } from './supported-hosts.js';
import {
  PERSONAL_PROTECTED_ACTIONS,
  portablePersonalAuthorityProfile,
} from './personal-authority-profile.js';
import { personalPrincipalPath } from './personal-principal.js';
import { DEFAULT_TRUST_PATHS } from './trust-helper.js';
import { canonicalizeWorkspace, defaultBindingPaths } from './workspace-binding.js';

const SCHEMA = 'pulse.personal_install_plan.v2';
// An advertised 8 GB desktop can expose only 7 GiB to user processes (the
// native macOS ARM runner does exactly this). The release runtime budget caps
// the embedder at 4 GiB, so 7 GiB is the honest preflight floor.
const MINIMUM_MEMORY_BYTES = 7 * 1024 ** 3;
const INSTALL_HEADROOM_BYTES = 2 * 1024 ** 3;
const CURRENT_STATE_KEYS = Object.freeze([
  'binding', 'daemon', 'embedder', 'hook_trust', 'install_receipt',
  'plugin', 'presence', 'principal', 'runtime', 'vault',
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
  'release_authority_expired',
  'release_key_revoked',
  'release_key_epoch_invalid',
  'release_key_id_invalid',
  'release_key_id_mismatch',
  'release_key_invalid',
  'release_key_unknown',
  'release_manifest_noncanonical',
  'release_manifest_legacy',
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
  'release_target_catalog_incomplete',
  'release_target_unavailable',
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

export { detectCodexCLI };

export function detectInstallResources({ home = homedir(), platformServices = defaultPlatformServices } = {}) {
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
  try { port = platformServices.probePort(18789); } catch { /* fail closed as unknown */ }
  return {
    disk_free_bytes: diskFreeBytes,
    memory_total_bytes: totalmem(),
    port_18789: port,
  };
}

function releaseStatus(release) {
  if (!release) return null;
  if (!['pulse.verified_release_manifest.v1', 'pulse.verified_release_manifest.v2'].includes(release.schema) ||
      typeof release.version !== 'string' || !Number.isSafeInteger(release.epoch) || release.epoch < 1 ||
      !/^[a-f0-9]{64}$/.test(release.manifest_digest ?? '') ||
      typeof release.target_id !== 'string' || typeof release.historical_only !== 'boolean' ||
      !Array.isArray(release.capabilities) || !release.artifacts || Array.isArray(release.artifacts)) {
    throw new TypeError('install_plan_release_invalid');
  }
  if (release.schema === 'pulse.verified_release_manifest.v2' &&
      (!release.verification_profile || Array.isArray(release.verification_profile) ||
       typeof release.verification_profile !== 'object')) {
    throw new TypeError('install_plan_release_invalid');
  }
  const kinds = Object.keys(release.artifacts).sort();
  const required = ['daemon', 'embedder-runtime', 'model', 'plugin-runtime'];
  if (required.some((kind) => !kinds.includes(kind)) ||
      kinds.some((kind) => ![...required, 'presence-helper'].includes(kind)) ||
      kinds.includes('presence-helper') !== release.capabilities.includes('presence-helper')) {
    throw new TypeError('install_plan_release_invalid');
  }
  const artifacts = kinds.map((kind) => {
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
    capabilities: [...release.capabilities],
    catalog_schema: release.catalog_schema ?? null,
    epoch: release.epoch,
    historical_only: release.historical_only,
    manifest_digest: release.manifest_digest,
    origins: [...new Set(artifacts.map((artifact) => artifact.origin))].sort(),
    total_download_bytes: artifacts.reduce((total, artifact) => total + artifact.bytes, 0),
    target_id: release.target_id,
    verification_profile: release.verification_profile === undefined
      ? null
      : JSON.parse(JSON.stringify(release.verification_profile)),
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
    else if (key !== 'presence' && ['corrupt', 'invalid', 'unsafe'].includes(status)) {
      reasons.push(`${key}_${status}`);
    }
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

const HUMAN_HOST_NAMES = Object.freeze({
  'claude-code': 'Claude Code',
  codex: 'Codex',
  cursor: 'Cursor',
});

function humanList(values) {
  if (values.length < 2) return values[0] ?? 'your AI app';
  if (values.length === 2) return `${values[0]} and ${values[1]}`;
  return `${values.slice(0, -1).join(', ')}, and ${values.at(-1)}`;
}

export function formatPersonalInstallIntroduction(plan) {
  if (!plan || plan.schema !== SCHEMA || plan.contract_version !== 2) {
    throw new TypeError('install_plan_invalid');
  }
  const workspacePath = plan.detected?.workspace?.canonical_path;
  const project = typeof workspacePath === 'string' && workspacePath
    ? basename(workspacePath)
    : 'this project';
  const targetHosts = (plan.detected?.hosts ?? [])
    .filter((host) => host.activation_target === true)
    .map((host) => HUMAN_HOST_NAMES[host.host] ?? host.host);
  const hosts = humanList(targetHosts);
  const download = Number.isSafeInteger(plan.release?.total_download_bytes)
    ? `- Downloads ${formatBytes(plan.release.total_download_bytes)} for the local runtime and on-device search.`
    : null;
  const bindingTrust = plan.current_state?.binding !== 'ready'
    ? '- Your operating system may ask once for administrator approval to protect the signed project binding.'
    : null;
  const readiness = plan.outcome === 'ready_to_install'
    ? 'Everything needed is ready.'
    : `Pulse needs one prerequisite fixed before installation (${plan.reason_codes[0] ?? 'preflight_incomplete'}).`;

  return [
    'Pulse Personal',
    '',
    `I checked the project boundary for ${project}, the compatible AI apps, and this computer.`,
    'Pulse turns useful context from normal AI conversations into cards in Memory Home.',
    `Useful structured memory is saved automatically and can arrive in new ${hosts} tasks.`,
    'You can edit or delete any memory in Memory Home.',
    '',
    readiness,
    targetHosts.length > 0 ? `- Connects ${hosts} automatically.` : null,
    download,
    bindingTrust,
    '- Keeps memory private on this computer.',
    '- Imports no old chats, stores no raw transcripts, and makes no paid model API calls.',
    '',
    'You do not need to choose a model, storage path, port, or hooks.',
    'Nothing is installed until you approve below.',
    'Technical details: pulse install-plan --json',
  ].filter((line) => line !== null).join('\n');
}

export function formatPersonalInstallPlan(plan) {
  if (!plan || plan.schema !== SCHEMA || plan.contract_version !== 2) {
    throw new TypeError('install_plan_invalid');
  }
  const workspace = plan.detected?.workspace;
  const detectedHosts = plan.detected?.hosts ?? [];
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
  const authorityProfile = plan.authority_profile ?? portablePersonalAuthorityProfile();
  const protectedActions = plan.protected_actions ?? {
    enhanced_presence_required: [...PERSONAL_PROTECTED_ACTIONS],
    ordinary_personal_requires_enhanced_presence: false,
    optional_setup_command: null,
  };
  const optionalProtectedSetup = protectedActions.optional_setup_command
    ? `  - optional setup for those protected actions only: ${protectedActions.optional_setup_command}`
    : '  - no enhanced-presence adapter is available for this target';
  return `Pulse Personal install

Project: ${workspace?.canonical_path ?? 'unavailable'}
Workspace: ${workspace?.workspace_id ?? 'unavailable'}
Repository: ${workspace?.repository_id ?? 'unavailable'}
Supported harnesses: Claude Code, Codex, Cursor
Activation: every compatible harness detected on this desktop
Detected harnesses:
${detectedHosts.map((host) => `  - ${host.host}: ${host.compatible ? 'compatible' : host.detected ? `incompatible (${host.reason_code ?? 'unknown'})` : 'not installed'}`).join('\n')}
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

Authority:
  - profile: ${authorityProfile.schema}
  - ordinary Personal memory: ready without enhanced presence
  - enhanced presence is optional and required only for: ${protectedActions.enhanced_presence_required.join(', ')}
${optionalProtectedSetup}

Human approvals:
${approvals.join('\n')}

Removal boundary:
  - runtime uninstall: unavailable in this U3 build
  - disconnect removes only the selected harness integration; the signed binding and Personal vault are preserved
  - wipe is separate, destructive, and requires fresh enhanced user presence
  - disconnect commands: ${(plan.rollback.disconnect_commands ?? []).join(', ') || 'none until a harness is detected'}
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
  libc = detectDesktopLibc({ platform }),
  nodeVersion = process.versions.node,
  detectWorkspace,
  detectClaude,
  detectCodex,
  detectCursor,
  detectResources = detectInstallResources,
  platformServices = createPlatformServices({ platform, architecture, home }),
  release,
  releaseReasonCode,
  currentState,
} = {}) {
  const pulseRoot = dataDir ?? join(resolve(home), '.pulse');
  if (![cwd, home, codexHome, pulseRoot].every((value) => typeof value === 'string' && isAbsolute(value)) ||
      resolve(pulseRoot) !== pulseRoot) {
    throw new TypeError('install_plan_path_invalid');
  }
  const workspaceDetector = detectWorkspace ?? ((path) => canonicalizeWorkspace(path, { platformServices }));
  const workspace = workspaceStatus(cwd, workspaceDetector);
  const hosts = detectSupportedHosts({
    home, platformServices,
    ...(detectClaude ? { detectClaude } : {}),
    ...(detectCodex ? { detectCodex } : {}),
    ...(detectCursor ? { detectCursor } : {}),
  });
  const codex = hosts.find((host) => host.host === 'codex');
  const node = nodeStatus(nodeVersion);
  const verifiedRelease = releaseStatus(release);
  let target = null;
  try {
    target = resolveDesktopTarget({ platform, architecture, libc });
  } catch (error) {
    if (!(error instanceof DesktopTargetError)) throw error;
  }
  const detectedCurrentState = installCurrentState(currentState);
  const resources = detectResources({ home, platformServices });
  if (!resources || !['free', 'occupied', 'unknown'].includes(resources.port_18789) ||
      ![resources.disk_free_bytes, resources.memory_total_bytes]
        .every((value) => value === null || (Number.isSafeInteger(value) && value >= 0))) {
    throw new TypeError('install_plan_resources_invalid');
  }
  const requiredDiskBytes = (verifiedRelease?.total_download_bytes ?? 0) + INSTALL_HEADROOM_BYTES;
  const reasons = [];
  if (!target) reasons.push('release_target_unavailable');
  if (!node.ok) reasons.push('node_unsupported');
  if (workspace.reason_code) reasons.push(workspace.reason_code);
  const compatibleHosts = hosts.filter((host) => host.compatible);
  if (compatibleHosts.length === 0) {
    reasons.push(hosts.some((host) => host.detected)
      ? 'supported_harness_incompatible'
      : 'supported_harness_missing');
  }
  if (!verifiedRelease) reasons.push(verifiedReleaseReason(releaseReasonCode));
  else if (verifiedRelease.historical_only) reasons.push('release_manifest_legacy');
  else if (target && verifiedRelease.target_id !== target.id) reasons.push('release_target_unavailable');
  reasons.push(...unsafeCurrentStateReasons(detectedCurrentState));
  if (resources.disk_free_bytes !== null && resources.disk_free_bytes < requiredDiskBytes) reasons.push('disk_insufficient');
  if (resources.memory_total_bytes !== null && resources.memory_total_bytes < MINIMUM_MEMORY_BYTES) reasons.push('memory_insufficient');
  const unsupported = reasons.some((reason) => [
    'release_target_unavailable', 'node_unsupported',
  ].includes(reason));
  const codexRoot = resolve(codexHome);
  const workspacePath = workspace.identity?.canonical_path ?? resolve(cwd);
  const bindingPaths = defaultBindingPaths(resolve(home));

  const outcome = unsupported ? 'unsupported' : reasons.length > 0 ? 'action_required' : 'ready_to_install';
  return {
    schema: SCHEMA,
    contract_version: 2,
    product: 'Pulse Personal',
    stage: 'personal_stage_1',
    supported_hosts: [...SUPPORTED_HOST_IDS],
    activation_policy: 'all_detected_supported_hosts',
    outcome,
    reason_codes: reasons,
    next_action: planNextAction(outcome, reasons),
    detected: {
      platform: { actual: platform, required: 'darwin|win32|linux', ok: target !== null },
      architecture: { actual: architecture, required: 'arm64|x64', ok: target !== null },
      target,
      node,
      codex,
      hosts,
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
      { path: dirname(DEFAULT_TRUST_PATHS.publicKeyPath), purpose: 'root_owned_binding_trust', preserved_on_uninstall: true },
      { path: join(resolve(home), '.pulse', 'product-locators.json'), purpose: 'shared_harness_workspace_locator', preserved_on_uninstall: false },
      ...compatibleHosts.map((host) => ({
        path: join(resolve(home), '.pulse', 'product-host-access', '<workspace_digest>', `${host.host}.json`),
        purpose: `${host.host.replaceAll('-', '_')}_workspace_access`,
        preserved_on_uninstall: false,
      })),
      ...(codex?.activation_target ? [
        { path: join(codexRoot, 'pulse', 'product-locators.json'), purpose: 'codex_workspace_locator', preserved_on_uninstall: false },
        { path: join(codexRoot, 'plugins'), purpose: 'codex_managed_pulse_plugin', preserved_on_uninstall: false },
      ] : []),
      ...(hosts.find((host) => host.host === 'claude-code')?.activation_target ? [
        { path: join(resolve(home), '.claude', 'plugins'), purpose: 'claude_managed_pulse_plugin', preserved_on_uninstall: false },
      ] : []),
      ...(hosts.find((host) => host.host === 'cursor')?.activation_target ? [
        { path: join(resolve(home), '.cursor', 'plugins', 'local', 'pulse'), purpose: 'cursor_local_pulse_plugin', preserved_on_uninstall: false },
      ] : []),
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
      { code: 'detected_harness_activation', destinations: compatibleHosts.map((host) => host.host) },
      { code: 'local_runtime', destination: '127.0.0.1 only' },
    ],
    privacy: {
      raw_transcript_capture: 'off',
      backend_model_calls: 'off',
      old_chat_import: 'not_requested',
      memory_location: 'local_private_vault',
    },
    authority_profile: portablePersonalAuthorityProfile(),
    protected_actions: {
      enhanced_presence_required: [...PERSONAL_PROTECTED_ACTIONS],
      ordinary_personal_requires_enhanced_presence: false,
      optional_setup_command: verifiedRelease?.capabilities.includes('presence-helper')
        ? 'pulse trust install --confirm "install pulse presence helper"'
        : null,
    },
    required_human_approvals: [
      { code: 'install_disclosure_consent', automatable_by_yes: false },
      ...(detectedCurrentState.binding !== 'ready'
        ? [{ code: 'binding_trust_bootstrap', automatable_by_yes: false }]
        : []),
      ...(codex?.activation_target ? [{ code: 'codex_hook_trust', automatable_by_yes: false }] : []),
    ],
    rollback: {
      remove_runtime: null,
      remove_codex_connection: 'pulse disconnect codex',
      disconnect_commands: compatibleHosts
        .map((host) => `pulse disconnect ${host.host}`),
      runtime_uninstall: 'unavailable_in_u3',
      preserve_vault: true,
      destructive_wipe_separate: true,
    },
  };
}
