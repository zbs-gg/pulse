import {
  createHash, createPublicKey, verify,
} from 'node:crypto';
import {
  chmodSync, closeSync, existsSync, fsyncSync, lstatSync, mkdirSync, openSync, readFileSync, renameSync, rmSync, rmdirSync, writeFileSync,
} from 'node:fs';
import { dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  DESKTOP_TARGET_IDS,
  DesktopTargetError,
  desktopTargetDefinition,
  resolveDesktopTarget,
  selectDesktopTarget,
} from './desktop-target.js';

const LEGACY_MANIFEST_SCHEMA = 'pulse.personal_preview.release_manifest.v1';
const LEGACY_ENVELOPE_SCHEMA = 'pulse.release_envelope.v1';
const CATALOG_SCHEMA = 'pulse.personal_preview.release_catalog.v2';
const CATALOG_ENVELOPE_SCHEMA = 'pulse.release_catalog_envelope.v2';
const ARTIFACT_SET_SCHEMA = 'pulse.personal_release_artifact_set.v1';
const ARTIFACT_SET_PAYLOAD_SCHEMA = 'pulse.personal_release_artifact_set_payload.v1';
const SNAPSHOT_ENVELOPE_SCHEMA = 'pulse.release_snapshot_envelope.v1';
const SNAPSHOT_PAYLOAD_SCHEMA = 'pulse.release_snapshot.v1';
const VERIFIED_CATALOG_SCHEMA = 'pulse.verified_release_manifest.v2';
const VERIFIED_ARTIFACT_SET_SCHEMA = 'pulse.verified_release_manifest.v3';
const VERIFIED_LEGACY_SCHEMA = 'pulse.verified_release_manifest.v1';
const AUTHORITY_SCHEMA = 'pulse.release_authority.v1';
const AUTHORITY_ENVELOPE_SCHEMA = 'pulse.release_authority_envelope.v1';
const EPOCH_SCHEMA = 'pulse.minimum_release_epoch.v1';
const LEGACY_ARTIFACTS = Object.freeze([
  'daemon', 'embedder-runtime', 'model', 'plugin-runtime', 'presence-helper',
]);
const REQUIRED_ARTIFACTS = Object.freeze(['daemon', 'embedder-runtime', 'model', 'plugin-runtime']);
const COMMON_ARTIFACTS = Object.freeze(['model', 'plugin-runtime']);
const TARGET_ARTIFACTS = Object.freeze(['daemon', 'embedder-runtime']);
const OPTIONAL_ARTIFACTS = Object.freeze(['presence-helper']);
const NATIVE_EXECUTABLES = new Set(['daemon', 'embedder-runtime', 'presence-helper']);
const FORMAT_BY_KIND = Object.freeze({
  daemon: 'dmg',
  'embedder-runtime': 'dmg',
  model: 'safetensors',
  'plugin-runtime': 'tar.gz',
  'presence-helper': 'dmg',
});
const PORTABLE_MODEL_REQUIRED_FILES = Object.freeze([
  'LICENSES/BGE-M3-MIT.txt',
  'PROVENANCE.json',
  'model_int8.onnx',
  'pulse-model-contract.json',
  'support/config.json',
  'support/special_tokens_map.json',
  'support/tokenizer.json',
  'support/tokenizer_config.json',
]);
const PORTABLE_MODEL_REVISION = '5617a9f61b028005a4858fdac845db406aefb181';
const SAFE_ID = /^[a-z0-9][a-z0-9._-]{0,127}$/;
const SEMVER = /^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?$/;
const SHA256 = /^[a-f0-9]{64}$/;
const TEAM_ID = /^[A-Z0-9]{10}$/;
const GOLD_HOSTS = Object.freeze(['claude-code', 'codex', 'cursor']);

export class ReleaseManifestError extends Error {
  constructor(code) {
    super(code);
    this.name = 'ReleaseManifestError';
    this.code = code;
  }
}

function fail(code) {
  throw new ReleaseManifestError(code);
}

function exactKeys(value, expected, code) {
  if (!value || Array.isArray(value) || typeof value !== 'object' ||
      Object.keys(value).sort().join('\0') !== [...expected].sort().join('\0')) fail(code);
}

function canonical(value) {
  if (value === null) return 'null';
  if (typeof value === 'string' || typeof value === 'boolean') return JSON.stringify(value);
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value) || Object.is(value, -0)) fail('canonical_number_invalid');
    return String(value);
  }
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`;
  if (value && typeof value === 'object') {
    return `{${Object.keys(value).sort().map((key) => {
      if (typeof value[key] === 'undefined') fail('canonical_value_invalid');
      return `${JSON.stringify(key)}:${canonical(value[key])}`;
    }).join(',')}}`;
  }
  fail('canonical_value_invalid');
}

export function canonicalReleaseJSON(value) {
  return canonical(value);
}

function keyDER(publicKeyPEM) {
  try {
    if (typeof publicKeyPEM !== 'string' || publicKeyPEM.length > 8192) fail('release_key_invalid');
    const key = createPublicKey(publicKeyPEM);
    if (key.asymmetricKeyType !== 'ed25519') fail('release_key_invalid');
    return { key, der: key.export({ type: 'spki', format: 'der' }) };
  } catch (error) {
    if (error instanceof ReleaseManifestError) throw error;
    fail('release_key_invalid');
  }
}

export function releaseKeyID(publicKeyPEM) {
  return createHash('sha256').update(keyDER(publicKeyPEM).der).digest('hex');
}

export function pinnedReleaseKeyring(rootPath = fileURLToPath(new URL('../release/pulse-release-root.pem', import.meta.url))) {
  let publicKeyPEM;
  try {
    const stat = lstatSync(rootPath);
    if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 8192) fail('release_root_invalid');
    publicKeyPEM = readFileSync(rootPath, 'utf8');
  } catch (error) {
    if (error instanceof ReleaseManifestError) throw error;
    fail('release_root_invalid');
  }
  return Object.freeze([Object.freeze({
    key_id: releaseKeyID(publicKeyPEM),
    public_key_pem: publicKeyPEM,
    valid_from_epoch: 1,
    valid_through_epoch: Number.MAX_SAFE_INTEGER,
  })]);
}

function strictTimestamp(value, code) {
  if (typeof value !== 'string' || value.length !== 24) fail(code);
  const time = Date.parse(value);
  if (!Number.isFinite(time) || new Date(time).toISOString() !== value) fail(code);
  return time;
}

function compareVersion(left, right) {
  const parse = (value) => value.split('.').map((part) => Number.parseInt(part, 10));
  const a = parse(left);
  const b = parse(right);
  for (let index = 0; index < 3; index += 1) {
    if (a[index] !== b[index]) return a[index] - b[index];
  }
  return 0;
}

function validateSigning(signing, executable, platform = 'darwin') {
  exactKeys(signing, ['gatekeeper', 'identifier', 'notarized', 'scheme', 'stapled', 'team_id'], 'artifact_signing_fields_invalid');
  if (executable && platform === 'darwin') {
    if (signing.scheme !== 'apple-developer-id' || signing.notarized !== true || signing.gatekeeper !== true || signing.stapled !== true ||
        typeof signing.identifier !== 'string' || !SAFE_ID.test(signing.identifier) ||
        typeof signing.team_id !== 'string' || !TEAM_ID.test(signing.team_id)) fail('artifact_signing_invalid');
    return;
  }
  if (signing.scheme !== 'release-manifest' || signing.notarized !== false || signing.gatekeeper !== false || signing.stapled !== false ||
      signing.identifier !== null || signing.team_id !== null) fail('artifact_signing_invalid');
}

function validateCatalogSigning(signing, executable, platform, fixture = false) {
  exactKeys(signing, ['gatekeeper', 'identifier', 'notarized', 'scheme', 'stapled', 'team_id'], 'artifact_signing_fields_invalid');
  if (fixture) {
    if (signing.scheme !== 'fixture' || signing.notarized !== false || signing.gatekeeper !== false ||
        signing.stapled !== false || signing.identifier !== null || signing.team_id !== null) fail('artifact_signing_invalid');
    return;
  }
  if (executable && platform === 'darwin') {
    if (signing.scheme !== 'apple-developer-id' || signing.notarized !== true || signing.gatekeeper !== true ||
        signing.stapled !== false || typeof signing.identifier !== 'string' || !SAFE_ID.test(signing.identifier) ||
        typeof signing.team_id !== 'string' || !TEAM_ID.test(signing.team_id)) fail('artifact_signing_invalid');
    return;
  }
  if (executable && platform === 'win32') {
    if (signing.scheme !== 'windows-authenticode' || signing.notarized !== false || signing.gatekeeper !== false ||
        signing.stapled !== false || signing.identifier !== null || signing.team_id !== null) fail('artifact_signing_invalid');
    return;
  }
  if (signing.scheme !== 'release-manifest' || signing.notarized !== false || signing.gatekeeper !== false ||
      signing.stapled !== false || signing.identifier !== null || signing.team_id !== null) fail('artifact_signing_invalid');
}

function validateArtifact(name, artifact, release, allowedOrigins, options) {
  exactKeys(artifact, [
    'architecture', 'bytes', 'epoch', 'executable', 'format', 'id', 'kind', 'minimum_os', 'model_policy', 'origin',
    'platform', 'sha256', 'signing', 'url', 'version',
  ], 'artifact_fields_invalid');
  if (artifact.kind !== name || typeof artifact.id !== 'string' || !SAFE_ID.test(artifact.id)) fail('artifact_identity_invalid');
  if (artifact.format !== FORMAT_BY_KIND[name]) fail('artifact_format_invalid');
  if (name === 'model') {
    exactKeys(artifact.model_policy, ['custom_code', 'data_only'], 'artifact_model_policy_invalid');
    if (artifact.model_policy.custom_code !== false || artifact.model_policy.data_only !== true) fail('artifact_model_policy_invalid');
  } else if (artifact.model_policy !== null) {
    fail('artifact_model_policy_invalid');
  }
  if (artifact.version !== release.version || artifact.epoch !== release.epoch) fail('artifact_version_incompatible');
  if (artifact.platform !== options.platform) fail('artifact_platform_incompatible');
  if (artifact.architecture !== options.architecture) fail('artifact_architecture_incompatible');
  if (typeof artifact.minimum_os !== 'string' || !/^\d+\.\d+$/.test(artifact.minimum_os)) fail('artifact_minimum_os_invalid');
  if (typeof options.osVersion !== 'string' || !/^\d+\.\d+(?:\.\d+)?$/.test(options.osVersion) ||
      compareVersion(`${artifact.minimum_os}.0`, options.osVersion.split('.').length === 2 ? `${options.osVersion}.0` : options.osVersion) > 0) {
    fail('artifact_minimum_os_incompatible');
  }
  if (!Number.isSafeInteger(artifact.bytes) || artifact.bytes < 1 || artifact.bytes > 64 * 1024 * 1024 * 1024) fail('artifact_size_invalid');
  if (typeof artifact.sha256 !== 'string' || !SHA256.test(artifact.sha256)) fail('artifact_digest_invalid');
  if (typeof artifact.origin !== 'string' || !allowedOrigins.has(artifact.origin)) fail('artifact_origin_not_allowed');
  let url;
  try { url = new URL(artifact.url); } catch { fail('artifact_url_invalid'); }
  if (url.protocol !== 'https:' || url.username || url.password || url.search || url.hash || url.origin !== artifact.origin ||
      url.pathname.split('/').some((part) => part === '..' || part === '.')) fail('artifact_url_invalid');
  if (!url.pathname.endsWith(`.${artifact.format}`)) fail('artifact_format_invalid');
  const executable = NATIVE_EXECUTABLES.has(name);
  if (artifact.executable !== executable) fail('artifact_executable_invalid');
  validateSigning(artifact.signing, executable, options.platform);
}

function validatePayload(payload, options) {
  exactKeys(payload, ['allowed_origins', 'artifacts', 'release', 'schema'], 'manifest_fields_invalid');
  if (payload.schema !== LEGACY_MANIFEST_SCHEMA) fail('manifest_schema_invalid');
  exactKeys(payload.release, ['channel', 'epoch', 'expires_at', 'issued_at', 'key_id', 'package', 'version'], 'release_fields_invalid');
  const release = payload.release;
  if (release.package !== '@zbs-gg/pulse' || release.channel !== 'preview' ||
      typeof release.version !== 'string' || !SEMVER.test(release.version)) fail('release_identity_invalid');
  if (release.version !== options.packageVersion) fail('release_package_version_incompatible');
  if (!Number.isSafeInteger(release.epoch) || release.epoch < 1) fail('manifest_epoch_invalid');
  if (typeof release.key_id !== 'string' || !SHA256.test(release.key_id)) fail('release_key_id_invalid');
  const issued = strictTimestamp(release.issued_at, 'manifest_issued_at_invalid');
  const expires = strictTimestamp(release.expires_at, 'manifest_expires_at_invalid');
  if (expires <= issued || expires - issued > 93 * 24 * 60 * 60 * 1000) fail('manifest_validity_invalid');
  const now = options.now instanceof Date ? options.now.getTime() : Number.NaN;
  if (!Number.isFinite(now)) fail('verification_time_invalid');
  if (now < issued) fail('manifest_not_yet_valid');
  if (now >= expires) fail('manifest_expired');
  if (!Array.isArray(payload.allowed_origins) || payload.allowed_origins.length < 1 || payload.allowed_origins.length > 8 ||
      payload.allowed_origins.some((origin) => typeof origin !== 'string') ||
      [...new Set(payload.allowed_origins)].sort().join('\0') !== payload.allowed_origins.join('\0')) fail('allowed_origins_invalid');
  for (const origin of payload.allowed_origins) {
    let parsed;
    try { parsed = new URL(origin); } catch { fail('allowed_origins_invalid'); }
    if (parsed.protocol !== 'https:' || parsed.origin !== origin || parsed.pathname !== '/' || parsed.search || parsed.hash || parsed.username || parsed.password) {
      fail('allowed_origins_invalid');
    }
  }
  exactKeys(payload.artifacts, LEGACY_ARTIFACTS, 'artifact_set_invalid');
  const allowedOrigins = new Set(payload.allowed_origins);
  for (const name of LEGACY_ARTIFACTS) validateArtifact(name, payload.artifacts[name], release, allowedOrigins, options);
  return release;
}

function validateReleaseIdentity(release, options) {
  exactKeys(release, ['channel', 'epoch', 'expires_at', 'issued_at', 'key_id', 'package', 'version'], 'release_fields_invalid');
  if (release.package !== '@zbs-gg/pulse' || release.channel !== 'preview' ||
      typeof release.version !== 'string' || !SEMVER.test(release.version)) fail('release_identity_invalid');
  if (release.version !== options.packageVersion) fail('release_package_version_incompatible');
  if (!Number.isSafeInteger(release.epoch) || release.epoch < 1) fail('manifest_epoch_invalid');
  if (typeof release.key_id !== 'string' || !SHA256.test(release.key_id)) fail('release_key_id_invalid');
  const issued = strictTimestamp(release.issued_at, 'manifest_issued_at_invalid');
  const expires = strictTimestamp(release.expires_at, 'manifest_expires_at_invalid');
  if (expires <= issued || expires - issued > 93 * 24 * 60 * 60 * 1000) fail('manifest_validity_invalid');
  const now = options.now instanceof Date ? options.now.getTime() : Number.NaN;
  if (!Number.isFinite(now)) fail('verification_time_invalid');
  if (now < issued) fail('manifest_not_yet_valid');
  if (now >= expires) fail('manifest_expired');
  return { expires, issued, release };
}

function validateAllowedOrigins(origins) {
  if (!Array.isArray(origins) || origins.length < 1 || origins.length > 8 ||
      origins.some((origin) => typeof origin !== 'string') ||
      [...new Set(origins)].sort().join('\0') !== origins.join('\0')) fail('allowed_origins_invalid');
  for (const origin of origins) {
    let parsed;
    try { parsed = new URL(origin); } catch { fail('allowed_origins_invalid'); }
    if (parsed.protocol !== 'https:' || parsed.origin !== origin || parsed.pathname !== '/' || parsed.search || parsed.hash ||
        parsed.username || parsed.password) fail('allowed_origins_invalid');
  }
  return new Set(origins);
}

function validateCatalogArtifact(name, artifact, release, allowedOrigins, expected, options = {}) {
  exactKeys(artifact, [
    'architecture', 'bytes', 'epoch', 'executable', 'format', 'id', 'kind', 'minimum_os', 'model_policy', 'origin',
    'platform', 'sha256', 'signing', 'tree_digest', 'url', 'version',
  ], 'artifact_fields_invalid');
  if (artifact.kind !== name || typeof artifact.id !== 'string' || !SAFE_ID.test(artifact.id)) fail('artifact_identity_invalid');
  const allowedFormats = ['tar.gz'];
  if (!allowedFormats.includes(artifact.format)) fail('artifact_format_invalid');
  if (name === 'model') {
    exactKeys(artifact.model_policy, [
      'custom_code', 'data_only', 'engine', 'model', 'required_files', 'revision',
    ], 'artifact_model_policy_invalid');
    if (artifact.model_policy.custom_code !== false || artifact.model_policy.data_only !== true) fail('artifact_model_policy_invalid');
    if (artifact.model_policy.engine !== 'transformers-js-onnx' || artifact.model_policy.model !== 'BAAI/bge-m3' ||
        artifact.model_policy.revision !== PORTABLE_MODEL_REVISION ||
        canonicalReleaseJSON(artifact.model_policy.required_files) !== canonicalReleaseJSON(PORTABLE_MODEL_REQUIRED_FILES)) {
      fail('artifact_model_policy_invalid');
    }
  } else if (artifact.model_policy !== null) fail('artifact_model_policy_invalid');
  if (artifact.version !== release.version || artifact.epoch !== release.epoch) fail('artifact_version_incompatible');
  if (artifact.platform !== expected.platform) fail('artifact_platform_incompatible');
  if (artifact.architecture !== expected.architecture) fail('artifact_architecture_incompatible');
  if (typeof artifact.minimum_os !== 'string' || !/^\d+\.\d+$/.test(artifact.minimum_os)) fail('artifact_minimum_os_invalid');
  if (!Number.isSafeInteger(artifact.bytes) || artifact.bytes < 1 || artifact.bytes > 64 * 1024 * 1024 * 1024) fail('artifact_size_invalid');
  if (typeof artifact.sha256 !== 'string' || !SHA256.test(artifact.sha256)) fail('artifact_digest_invalid');
  if (typeof artifact.tree_digest !== 'string' || !SHA256.test(artifact.tree_digest)) fail('artifact_tree_digest_invalid');
  if (typeof artifact.origin !== 'string' || !allowedOrigins.has(artifact.origin)) fail('artifact_origin_not_allowed');
  let url;
  try { url = new URL(artifact.url); } catch { fail('artifact_url_invalid'); }
  if (url.protocol !== 'https:' || url.username || url.password || url.search || url.hash || url.origin !== artifact.origin ||
      url.pathname.split('/').some((part) => part === '..' || part === '.')) fail('artifact_url_invalid');
  if (!url.pathname.endsWith(`.${artifact.format}`)) fail('artifact_format_invalid');
  const executable = NATIVE_EXECUTABLES.has(name);
  if (artifact.executable !== executable) fail('artifact_executable_invalid');
  validateCatalogSigning(artifact.signing, executable, expected.platform, options.fixture === true);
}

// This profile is signed release-policy metadata. Platform attestation still
// has to prove the declared policy against the extracted native files; merely
// verifying this catalog never turns the declaration into runtime evidence.
function validateVerificationProfile(profile, definition, artifacts, options) {
  if (!profile || Array.isArray(profile) || typeof profile !== 'object') fail('release_verification_profile_invalid');
  if (profile.kind === 'fixture') {
    exactKeys(profile, ['fixture_id', 'kind', 'production'], 'release_verification_profile_invalid');
    if (profile.production !== false || typeof profile.fixture_id !== 'string' || !SAFE_ID.test(profile.fixture_id)) {
      fail('release_verification_profile_invalid');
    }
    if (options.allowFixtureVerification !== true) fail('release_fixture_verification_forbidden');
    return;
  }
  if (profile.kind === 'apple') {
    exactKeys(profile, ['gatekeeper', 'kind', 'notarized', 'stapled', 'team_id'], 'release_verification_profile_invalid');
    if (definition.platform !== 'darwin' || profile.gatekeeper !== true || profile.notarized !== true || profile.stapled !== false ||
        typeof profile.team_id !== 'string' || !TEAM_ID.test(profile.team_id) ||
        Object.values(artifacts).filter((artifact) => artifact.executable).some((artifact) => artifact.signing.team_id !== profile.team_id)) {
      fail('release_verification_profile_invalid');
    }
    return;
  }
  if (profile.kind === 'windows') {
    exactKeys(profile, ['kind', 'publisher', 'timestamp_url', 'timestamped'], 'release_verification_profile_invalid');
    let timestamp;
    try { timestamp = new URL(profile.timestamp_url); } catch { fail('release_verification_profile_invalid'); }
    if (definition.platform !== 'win32' || profile.timestamped !== true ||
        typeof profile.publisher !== 'string' || !/^CN=[^,\r\n]{1,128}(?:, ?(?:O|OU|L|S|C)=[^,\r\n]{1,128})*$/.test(profile.publisher) ||
        timestamp.protocol !== 'https:' || timestamp.username || timestamp.password || timestamp.search || timestamp.hash) {
      fail('release_verification_profile_invalid');
    }
    return;
  }
  exactKeys(profile, ['kind', 'policy'], 'release_verification_profile_invalid');
  if (profile.kind !== 'linux' || profile.policy !== 'signed-catalog-tree-v1' || definition.platform !== 'linux') {
    fail('release_verification_profile_invalid');
  }
}

function validateCatalogTarget(targetID, target, release, allowedOrigins, options) {
  let definition;
  try { definition = desktopTargetDefinition(targetID); } catch (error) {
    if (error instanceof DesktopTargetError) fail('release_target_catalog_invalid');
    throw error;
  }
  exactKeys(target, [
    'architecture', 'artifacts', 'capabilities', 'libc', 'platform', 'verification_profile',
  ], 'release_target_fields_invalid');
  if (target.platform !== definition.platform || target.architecture !== definition.architecture || target.libc !== definition.libc) {
    fail('release_target_identity_invalid');
  }
  if (!Array.isArray(target.capabilities) || target.capabilities.length > 16 ||
      target.capabilities.some((capability) => typeof capability !== 'string' || !SAFE_ID.test(capability)) ||
      [...new Set(target.capabilities)].sort().join('\0') !== target.capabilities.join('\0')) fail('release_target_capability_invalid');
  const expectedArtifacts = [...TARGET_ARTIFACTS, ...(target.capabilities.includes('presence-helper') ? OPTIONAL_ARTIFACTS : [])];
  exactKeys(target.artifacts, expectedArtifacts, 'release_target_capability_invalid');
  const fixture = target.verification_profile.kind === 'fixture';
  for (const name of expectedArtifacts) {
    validateCatalogArtifact(name, target.artifacts[name], release, allowedOrigins, definition, { fixture });
  }
  validateVerificationProfile(target.verification_profile, definition, target.artifacts, options);
}

function validateCatalogPayload(payload, options) {
  exactKeys(payload, ['allowed_origins', 'common_artifacts', 'release', 'schema', 'targets'], 'manifest_fields_invalid');
  if (payload.schema !== CATALOG_SCHEMA) fail('manifest_schema_invalid');
  const validated = validateReleaseIdentity(payload.release, options);
  const allowedOrigins = validateAllowedOrigins(payload.allowed_origins);
  exactKeys(payload.common_artifacts, COMMON_ARTIFACTS, 'artifact_set_invalid');
  for (const name of COMMON_ARTIFACTS) validateCatalogArtifact(name, payload.common_artifacts[name], payload.release, allowedOrigins, {
    platform: 'all', architecture: 'all',
  });
  if (!payload.targets || Array.isArray(payload.targets) || typeof payload.targets !== 'object') {
    fail('release_target_catalog_invalid');
  }
  const targetIDs = Object.keys(payload.targets).sort();
  if (targetIDs.length < 1 || targetIDs.some((targetID) => !DESKTOP_TARGET_IDS.includes(targetID))) {
    fail('release_target_catalog_invalid');
  }
  const fixtureOnlyCatalog = options.allowFixtureVerification === true &&
    targetIDs.every((targetID) => payload.targets[targetID]?.verification_profile?.kind === 'fixture');
  if (!fixtureOnlyCatalog && targetIDs.join('\0') !== DESKTOP_TARGET_IDS.join('\0')) {
    fail('release_target_catalog_incomplete');
  }
  for (const [targetID, target] of Object.entries(payload.targets)) {
    validateCatalogTarget(targetID, target, payload.release, allowedOrigins, options);
  }
  let selected;
  let target;
  try {
    target = resolveDesktopTarget({
      platform: options.platform, architecture: options.architecture, libc: options.libc,
    });
    selected = selectDesktopTarget(payload.targets, target);
  } catch (error) {
    if (error instanceof DesktopTargetError) fail(error.code);
    throw error;
  }
  return { ...validated, selected, target };
}

function validateArtifactSetRelease(release, options) {
  exactKeys(release, ['channel', 'epoch', 'key_id', 'package', 'version'], 'release_fields_invalid');
  if (release.package !== '@zbs-gg/pulse' || release.channel !== 'preview' ||
      typeof release.version !== 'string' || !SEMVER.test(release.version)) fail('release_identity_invalid');
  if (release.version !== options.packageVersion) fail('release_package_version_incompatible');
  if (!Number.isSafeInteger(release.epoch) || release.epoch < 1) fail('manifest_epoch_invalid');
  if (typeof release.key_id !== 'string' || !SHA256.test(release.key_id)) fail('release_key_id_invalid');
  return release;
}

function validateSnapshotURL(value, expectedPathSuffix) {
  let parsed;
  try { parsed = new URL(value); } catch { fail('release_snapshot_url_invalid'); }
  if (parsed.protocol !== 'https:' || parsed.username || parsed.password || parsed.search || parsed.hash ||
      parsed.pathname.split('/').some((part) => part === '..' || part === '.') ||
      !parsed.pathname.endsWith(expectedPathSuffix)) fail('release_snapshot_url_invalid');
  return parsed;
}

function validateHostPolicy(policy) {
  exactKeys(policy, ['harnesses'], 'release_host_policy_invalid');
  if (!Array.isArray(policy.harnesses) || policy.harnesses.length !== GOLD_HOSTS.length) {
    fail('release_host_policy_invalid');
  }
  const hosts = [];
  for (const harness of policy.harnesses) {
    exactKeys(harness, [
      'distribution', 'downloads', 'executable', 'executable_digest_policy', 'host', 'identity', 'supported_targets',
      'vendor', 'vendor_source', 'version',
    ], 'release_host_policy_invalid');
    if (!GOLD_HOSTS.includes(harness.host) || typeof harness.vendor !== 'string' || harness.vendor.length < 1 ||
        typeof harness.distribution !== 'string' || !SAFE_ID.test(harness.distribution) ||
        typeof harness.identity !== 'string' || harness.identity.length < 1 || harness.identity.length > 256 ||
        typeof harness.version !== 'string' || !/^\d+\.\d+(?:\.\d+)?$/.test(harness.version) ||
        typeof harness.executable !== 'string' || !SAFE_ID.test(harness.executable) ||
        harness.executable_digest_policy !== 'native_evidence_sha256') fail('release_host_policy_invalid');
    let vendorSource;
    try { vendorSource = new URL(harness.vendor_source); } catch { fail('release_host_policy_invalid'); }
    if (vendorSource.protocol !== 'https:' || vendorSource.username || vendorSource.password ||
        vendorSource.search || vendorSource.hash || vendorSource.pathname.split('/').some((part) => part === '..' || part === '.')) {
      fail('release_host_policy_invalid');
    }
    if (!Array.isArray(harness.supported_targets) ||
        harness.supported_targets.join('\0') !== DESKTOP_TARGET_IDS.join('\0')) fail('release_host_policy_invalid');
    if (!harness.downloads || Array.isArray(harness.downloads) || typeof harness.downloads !== 'object' ||
        Object.keys(harness.downloads).sort().join('\0') !== DESKTOP_TARGET_IDS.join('\0')) {
      fail('release_host_policy_invalid');
    }
    for (const targetID of DESKTOP_TARGET_IDS) {
      let download;
      try { download = new URL(harness.downloads[targetID]); } catch { fail('release_host_policy_invalid'); }
      const expectedOrigin = harness.distribution === 'npm' ? 'https://registry.npmjs.org' : 'https://api2.cursor.sh';
      if (download.protocol !== 'https:' || download.origin !== expectedOrigin || download.username ||
          download.password || download.search || download.hash) fail('release_host_policy_invalid');
    }
    hosts.push(harness.host);
  }
  if (hosts.sort().join('\0') !== GOLD_HOSTS.join('\0')) fail('release_host_policy_invalid');
}

function validateArtifactSetPayload(payload, options) {
  exactKeys(payload, [
    'allowed_origins', 'common_artifacts', 'host_policy', 'release', 'schema', 'snapshot_url', 'targets',
  ], 'manifest_fields_invalid');
  if (payload.schema !== ARTIFACT_SET_PAYLOAD_SCHEMA) fail('manifest_schema_invalid');
  const release = validateArtifactSetRelease(payload.release, options);
  const allowedOrigins = validateAllowedOrigins(payload.allowed_origins);
  const snapshotURL = validateSnapshotURL(payload.snapshot_url, `/pulse/${release.version}/catalog/snapshot.json`);
  if (!allowedOrigins.has(snapshotURL.origin)) fail('release_snapshot_origin_invalid');
  validateHostPolicy(payload.host_policy);
  exactKeys(payload.common_artifacts, COMMON_ARTIFACTS, 'artifact_set_invalid');
  for (const name of COMMON_ARTIFACTS) validateCatalogArtifact(name, payload.common_artifacts[name], release, allowedOrigins, {
    platform: 'all', architecture: 'all',
  });
  if (!payload.targets || Array.isArray(payload.targets) || typeof payload.targets !== 'object') {
    fail('release_target_catalog_invalid');
  }
  const targetIDs = Object.keys(payload.targets).sort();
  if (targetIDs.length < 1 || targetIDs.some((targetID) => !DESKTOP_TARGET_IDS.includes(targetID))) {
    fail('release_target_catalog_invalid');
  }
  for (const [targetID, target] of Object.entries(payload.targets)) {
    validateCatalogTarget(targetID, target, release, allowedOrigins, options);
  }
  let selected;
  let target;
  try {
    target = resolveDesktopTarget({
      platform: options.platform, architecture: options.architecture, libc: options.libc,
    });
    selected = selectDesktopTarget(payload.targets, target);
  } catch (error) {
    if (error instanceof DesktopTargetError) fail(error.code);
    throw error;
  }
  return { release, selected, snapshotURL, target };
}

function validateSnapshotEnvelope(snapshot, artifactSet, options) {
  exactKeys(snapshot, ['payload', 'schema', 'signature'], 'release_snapshot_fields_invalid');
  if (snapshot.schema !== SNAPSHOT_ENVELOPE_SCHEMA) fail('release_snapshot_schema_invalid');
  const signature = validateSignature(snapshot.signature, 'release_snapshot_signature_invalid');
  const payload = snapshot.payload;
  exactKeys(payload, [
    'artifact_set', 'channel', 'expires_at', 'issued_at', 'package', 'release_epoch', 'revoked_key_ids',
    'schema', 'version',
  ], 'release_snapshot_fields_invalid');
  if (payload.schema !== SNAPSHOT_PAYLOAD_SCHEMA || payload.package !== '@zbs-gg/pulse' ||
      payload.version !== artifactSet.release.version || payload.version !== options.packageVersion ||
      payload.release_epoch !== artifactSet.release.epoch) fail('release_snapshot_identity_invalid');
  exactKeys(payload.artifact_set, ['sha256', 'url'], 'release_snapshot_fields_invalid');
  if (!SHA256.test(payload.artifact_set.sha256 ?? '')) fail('release_snapshot_artifact_set_invalid');
  const artifactSetURL = validateSnapshotURL(
    payload.artifact_set.url,
    `/pulse/${payload.version}/epoch-${payload.release_epoch}/catalog/artifact-set.json`,
  );
  if (!artifactSet.allowedOrigins.has(artifactSetURL.origin)) fail('release_snapshot_origin_invalid');
  exactKeys(payload.channel, [
    'key_id', 'public_key_pem', 'valid_from_epoch', 'valid_through_epoch',
  ], 'release_snapshot_fields_invalid');
  if (releaseKeyID(payload.channel.public_key_pem) !== payload.channel.key_id ||
      payload.channel.key_id !== artifactSet.release.key_id) fail('release_key_id_invalid');
  if (!Number.isSafeInteger(payload.channel.valid_from_epoch) ||
      !Number.isSafeInteger(payload.channel.valid_through_epoch) || payload.channel.valid_from_epoch < 1 ||
      payload.channel.valid_through_epoch < payload.channel.valid_from_epoch ||
      payload.release_epoch < payload.channel.valid_from_epoch ||
      payload.release_epoch > payload.channel.valid_through_epoch) fail('release_key_epoch_invalid');
  if (!Array.isArray(payload.revoked_key_ids) || payload.revoked_key_ids.length > 32 ||
      payload.revoked_key_ids.some((keyID) => typeof keyID !== 'string' || !SHA256.test(keyID)) ||
      [...new Set(payload.revoked_key_ids)].sort().join('\0') !== payload.revoked_key_ids.join('\0')) {
    fail('release_snapshot_revocations_invalid');
  }
  if (payload.revoked_key_ids.includes(payload.channel.key_id)) fail('release_key_revoked');
  const issued = strictTimestamp(payload.issued_at, 'release_snapshot_issued_at_invalid');
  const expires = strictTimestamp(payload.expires_at, 'release_snapshot_expires_at_invalid');
  if (expires <= issued || expires - issued > 31 * 24 * 60 * 60 * 1000) fail('release_snapshot_validity_invalid');
  const now = options.now instanceof Date ? options.now.getTime() : Number.NaN;
  if (!Number.isFinite(now)) fail('verification_time_invalid');
  if (now < issued) fail('release_snapshot_not_yet_valid');
  const expired = now >= expires;
  if (expired && options.allowExpiredSnapshot !== true) fail('release_snapshot_expired');
  const rootKey = trustedReleaseKey(options.trustedKeys, signature.key_id, payload.release_epoch);
  const canonicalBytes = Buffer.from(canonicalReleaseJSON(payload));
  if (!verify(null, canonicalBytes, rootKey, signatureBytes(signature.value))) {
    fail('release_snapshot_signature_invalid');
  }
  return {
    artifactSetURL: artifactSetURL.href,
    channelKey: keyDER(payload.channel.public_key_pem).key,
    expiresAt: payload.expires_at,
    expired,
    rootKeyID: signature.key_id,
  };
}

export function verifyPersonalReleaseArtifactSet(artifactSetEnvelope, snapshotEnvelope, options = {}) {
  exactKeys(artifactSetEnvelope, ['payload', 'schema', 'signature'], 'envelope_fields_invalid');
  if (artifactSetEnvelope.schema !== ARTIFACT_SET_SCHEMA) fail('envelope_schema_invalid');
  const signature = validateSignature(artifactSetEnvelope.signature);
  const validated = validateArtifactSetPayload(artifactSetEnvelope.payload, options);
  if (signature.key_id !== validated.release.key_id) fail('release_key_id_mismatch');
  const artifactSetBytes = Buffer.from(`${canonicalReleaseJSON(artifactSetEnvelope)}\n`);
  const artifactSetDigest = createHash('sha256').update(artifactSetBytes).digest('hex');
  const snapshot = validateSnapshotEnvelope(snapshotEnvelope, {
    allowedOrigins: validateAllowedOrigins(artifactSetEnvelope.payload.allowed_origins),
    release: validated.release,
  }, options);
  if (snapshotEnvelope.payload.artifact_set.sha256 !== artifactSetDigest) {
    fail('release_snapshot_artifact_set_mismatch');
  }
  const artifactPayload = Buffer.from(canonicalReleaseJSON(artifactSetEnvelope.payload));
  if (!verify(null, artifactPayload, snapshot.channelKey, signatureBytes(signature.value))) {
    fail('release_signature_invalid');
  }
  const minimum = options.minimumAcceptedEpoch ?? 0;
  if (!Number.isSafeInteger(minimum) || minimum < 0) fail('minimum_epoch_invalid');
  if (validated.release.epoch < minimum) fail('manifest_epoch_downgrade');
  const snapshotDigest = createHash('sha256')
    .update(`${canonicalReleaseJSON(snapshotEnvelope)}\n`)
    .digest('hex');
  return deepFreeze({
    artifacts: JSON.parse(canonicalReleaseJSON({
      ...artifactSetEnvelope.payload.common_artifacts,
      ...validated.selected.artifacts,
    })),
    authority: {
      artifact_set_url: snapshot.artifactSetURL,
      channel_key_id: signature.key_id,
      root_key_id: snapshot.rootKeyID,
      snapshot_digest: snapshotDigest,
      snapshot_expires_at: snapshot.expiresAt,
    },
    capabilities: [...validated.selected.capabilities],
    catalog_schema: ARTIFACT_SET_SCHEMA,
    downgrade_authorized: false,
    epoch: validated.release.epoch,
    historical_only: false,
    manifest_digest: artifactSetDigest,
    schema: VERIFIED_ARTIFACT_SET_SCHEMA,
    snapshot_refresh_required: snapshot.expired,
    target_id: validated.target.id,
    verification_profile: JSON.parse(canonicalReleaseJSON(validated.selected.verification_profile)),
    version: validated.release.version,
  });
}

function trustedReleaseKey(keys, keyID, epoch) {
  if (!Array.isArray(keys) || keys.length > 16) fail('trusted_keys_invalid');
  const entry = keys.find((candidate) => candidate?.key_id === keyID);
  if (!entry) fail('release_key_unknown');
  exactKeys(entry, ['key_id', 'public_key_pem', 'valid_from_epoch', 'valid_through_epoch'], 'trusted_key_fields_invalid');
  const computed = releaseKeyID(entry.public_key_pem);
  if (computed !== entry.key_id) fail('release_key_id_invalid');
  if (!Number.isSafeInteger(entry.valid_from_epoch) || !Number.isSafeInteger(entry.valid_through_epoch) ||
      entry.valid_from_epoch < 1 || entry.valid_through_epoch < entry.valid_from_epoch ||
      epoch < entry.valid_from_epoch || epoch > entry.valid_through_epoch) fail('release_key_epoch_invalid');
  return keyDER(entry.public_key_pem).key;
}

function signatureBytes(value) {
  if (typeof value !== 'string' || value.length > 256 || !/^[A-Za-z0-9+/]+={0,2}$/.test(value)) fail('release_signature_invalid');
  const bytes = Buffer.from(value, 'base64');
  if (bytes.length !== 64 || bytes.toString('base64') !== value) fail('release_signature_invalid');
  return bytes;
}

function validateSignature(signature, code = 'release_signature_invalid') {
  exactKeys(signature, ['algorithm', 'key_id', 'value'], 'signature_fields_invalid');
  if (signature.algorithm !== 'ed25519' || typeof signature.key_id !== 'string' || !SHA256.test(signature.key_id)) fail(code);
  return signature;
}

function verifyAuthorityEnvelope(authority, release, options) {
  exactKeys(authority, ['payload', 'schema', 'signature'], 'release_authority_fields_invalid');
  if (authority.schema !== AUTHORITY_ENVELOPE_SCHEMA) fail('release_authority_schema_invalid');
  const rootSignature = validateSignature(authority.signature, 'release_authority_signature_invalid');
  const payload = authority.payload;
  exactKeys(payload, ['channel', 'epoch', 'expires_at', 'issued_at', 'keys', 'revoked_key_ids', 'schema'], 'release_authority_fields_invalid');
  if (payload.schema !== AUTHORITY_SCHEMA || payload.channel !== release.channel ||
      !Number.isSafeInteger(payload.epoch) || payload.epoch < 1) fail('release_authority_invalid');
  const issued = strictTimestamp(payload.issued_at, 'release_authority_invalid');
  const expires = strictTimestamp(payload.expires_at, 'release_authority_invalid');
  const now = options.now instanceof Date ? options.now.getTime() : Number.NaN;
  if (!Number.isFinite(now)) fail('verification_time_invalid');
  if (expires <= issued || expires - issued > 31 * 24 * 60 * 60 * 1000) fail('release_authority_invalid');
  if (now < issued) fail('release_authority_not_yet_valid');
  if (now >= expires) fail('release_authority_expired');
  if (strictTimestamp(release.issued_at, 'manifest_issued_at_invalid') < issued ||
      strictTimestamp(release.expires_at, 'manifest_expires_at_invalid') > expires) fail('release_authority_validity_invalid');
  if (!Array.isArray(payload.revoked_key_ids) || payload.revoked_key_ids.length > 32 ||
      payload.revoked_key_ids.some((keyID) => typeof keyID !== 'string' || !SHA256.test(keyID)) ||
      [...new Set(payload.revoked_key_ids)].sort().join('\0') !== payload.revoked_key_ids.join('\0')) fail('release_authority_invalid');
  if (!Array.isArray(payload.keys) || payload.keys.length < 1 || payload.keys.length > 8) fail('release_authority_invalid');
  const rootKey = trustedReleaseKey(options.trustedKeys, rootSignature.key_id, payload.epoch);
  const authorityBytes = Buffer.from(canonicalReleaseJSON(payload));
  if (!verify(null, authorityBytes, rootKey, signatureBytes(rootSignature.value))) fail('release_authority_signature_invalid');
  const channel = payload.keys.find((entry) => entry?.key_id === release.key_id);
  if (!channel) fail('release_key_unknown');
  exactKeys(channel, ['key_id', 'public_key_pem', 'valid_from_epoch', 'valid_through_epoch'], 'trusted_key_fields_invalid');
  if (releaseKeyID(channel.public_key_pem) !== channel.key_id) fail('release_key_id_invalid');
  if (payload.revoked_key_ids.includes(channel.key_id)) fail('release_key_revoked');
  if (!Number.isSafeInteger(channel.valid_from_epoch) || !Number.isSafeInteger(channel.valid_through_epoch) ||
      channel.valid_from_epoch < 1 || channel.valid_through_epoch < channel.valid_from_epoch ||
      release.epoch < channel.valid_from_epoch || release.epoch > channel.valid_through_epoch) fail('release_key_epoch_invalid');
  return {
    channelKey: keyDER(channel.public_key_pem).key,
    metadata: {
      channel_key_id: channel.key_id,
      delegation_epoch: payload.epoch,
      expires_at: payload.expires_at,
      root_key_id: rootSignature.key_id,
    },
  };
}

function deepFreeze(value) {
  if (value && typeof value === 'object') {
    for (const child of Object.values(value)) deepFreeze(child);
    Object.freeze(value);
  }
  return value;
}

export function verifyReleaseManifestEnvelope(envelope, options = {}) {
  if (!envelope || Array.isArray(envelope) || typeof envelope !== 'object') fail('envelope_fields_invalid');
  let release;
  let result;
  let canonicalBytes;
  let digest;
  if (envelope.schema === CATALOG_ENVELOPE_SCHEMA) {
    exactKeys(envelope, ['authority', 'payload', 'schema', 'signature'], 'envelope_fields_invalid');
    const signature = validateSignature(envelope.signature);
    const validated = validateCatalogPayload(envelope.payload, options);
    release = validated.release;
    if (release.key_id !== signature.key_id) fail('release_key_id_mismatch');
    const authority = verifyAuthorityEnvelope(envelope.authority, release, options);
    canonicalBytes = Buffer.from(canonicalReleaseJSON(envelope.payload));
    digest = createHash('sha256').update(canonicalBytes).digest('hex');
    if (!verify(null, canonicalBytes, authority.channelKey, signatureBytes(signature.value))) fail('release_signature_invalid');
    result = {
      artifacts: JSON.parse(canonicalReleaseJSON({
        ...envelope.payload.common_artifacts,
        ...validated.selected.artifacts,
      })),
      authority: authority.metadata,
      catalog_schema: CATALOG_SCHEMA,
      capabilities: [...validated.selected.capabilities],
      historical_only: false,
      schema: VERIFIED_CATALOG_SCHEMA,
      target_id: validated.target.id,
      verification_profile: JSON.parse(canonicalReleaseJSON(validated.selected.verification_profile)),
    };
  } else {
    exactKeys(envelope, ['payload', 'schema', 'signature'], 'envelope_fields_invalid');
    if (envelope.schema !== LEGACY_ENVELOPE_SCHEMA) fail('envelope_schema_invalid');
    const signature = validateSignature(envelope.signature);
    release = validatePayload(envelope.payload, options);
    if (release.key_id !== signature.key_id) fail('release_key_id_mismatch');
    canonicalBytes = Buffer.from(canonicalReleaseJSON(envelope.payload));
    digest = createHash('sha256').update(canonicalBytes).digest('hex');
    const key = trustedReleaseKey(options.trustedKeys, signature.key_id, release.epoch);
    if (!verify(null, canonicalBytes, key, signatureBytes(signature.value))) fail('release_signature_invalid');
    result = {
      artifacts: JSON.parse(canonicalReleaseJSON(envelope.payload.artifacts)),
      authority: null,
      catalog_schema: null,
      capabilities: ['presence-helper'],
      historical_only: true,
      schema: VERIFIED_LEGACY_SCHEMA,
      target_id: 'darwin-arm64',
      verification_profile: null,
    };
  }
  const minimum = options.minimumAcceptedEpoch ?? 0;
  if (!Number.isSafeInteger(minimum) || minimum < 0) fail('minimum_epoch_invalid');
  let downgradeAuthorized = false;
  if (release.epoch < minimum) {
    if (typeof options.verifyDowngradeAuthorization !== 'function' || !options.downgradeAuthorization ||
        options.verifyDowngradeAuthorization({
          authorization: options.downgradeAuthorization,
          manifestDigest: digest,
          manifestEpoch: release.epoch,
          minimumAcceptedEpoch: minimum,
        }) !== true) fail('manifest_epoch_downgrade');
    downgradeAuthorized = true;
  }
  return deepFreeze({
    ...result,
    downgrade_authorized: downgradeAuthorized,
    epoch: release.epoch,
    manifest_digest: digest,
    version: release.version,
  });
}

export function assertSupportedNodeVersion(version = process.versions.node) {
  if (typeof version !== 'string') fail('node_unsupported');
  const match = version.match(/^(\d+)\.(\d+)\.(\d+)(?:[-+].*)?$/);
  if (!match) fail('node_unsupported');
  const major = Number.parseInt(match[1], 10);
  if (major < 20) fail('node_unsupported');
  return Object.freeze({ major, version });
}

export function readMinimumReleaseEpoch(path) {
  if (!existsSync(path)) return 0;
  try {
    const stat = lstatSync(path);
    if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 1024 || (stat.mode & 0o077) !== 0) fail('minimum_epoch_file_invalid');
    const bytes = readFileSync(path, 'utf8');
    const value = JSON.parse(bytes);
    exactKeys(value, ['epoch', 'schema'], 'minimum_epoch_file_invalid');
    if (value.schema !== EPOCH_SCHEMA || !Number.isSafeInteger(value.epoch) || value.epoch < 1 ||
        bytes !== `${canonicalReleaseJSON(value)}\n`) fail('minimum_epoch_file_invalid');
    return value.epoch;
  } catch (error) {
    if (error instanceof ReleaseManifestError) throw error;
    fail('minimum_epoch_file_invalid');
  }
}

export function advanceMinimumReleaseEpoch(path, epoch) {
  if (typeof path !== 'string' || path.length < 1 || !Number.isSafeInteger(epoch) || epoch < 1) fail('minimum_epoch_invalid');
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  const lockPath = `${path}.lock`;
  try {
    mkdirSync(lockPath, { mode: 0o700 });
  } catch (error) {
    if (error?.code === 'EEXIST') fail('minimum_epoch_locked');
    fail('minimum_epoch_lock_failed');
  }
  try {
    const current = readMinimumReleaseEpoch(path);
    if (epoch < current) fail('minimum_epoch_rollback');
    if (epoch === current) return current;
    const temporary = `${path}.new-${process.pid}`;
    const bytes = `${canonicalReleaseJSON({ epoch, schema: EPOCH_SCHEMA })}\n`;
    try {
      writeFileSync(temporary, bytes, { encoding: 'utf8', flag: 'wx', mode: 0o600 });
      chmodSync(temporary, 0o600);
      const file = openSync(temporary, 'r');
      try { fsyncSync(file); } finally { closeSync(file); }
      renameSync(temporary, path);
      const directory = openSync(dirname(path), 'r');
      try { fsyncSync(directory); } finally { closeSync(directory); }
    } catch (error) {
      rmSync(temporary, { force: true });
      if (error instanceof ReleaseManifestError) throw error;
      fail('minimum_epoch_write_failed');
    }
    return epoch;
  } finally {
    try { rmdirSync(lockPath); } catch { fail('minimum_epoch_unlock_failed'); }
  }
}

export const RELEASE_ARTIFACT_KINDS = LEGACY_ARTIFACTS;
export const RELEASE_REQUIRED_ARTIFACT_KINDS = REQUIRED_ARTIFACTS;
