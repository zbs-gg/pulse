import {
  createHash, createPublicKey, verify,
} from 'node:crypto';
import {
  chmodSync, closeSync, existsSync, fsyncSync, lstatSync, mkdirSync, openSync, readFileSync, renameSync, rmSync, rmdirSync, writeFileSync,
} from 'node:fs';
import { dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const MANIFEST_SCHEMA = 'pulse.personal_preview.release_manifest.v1';
const ENVELOPE_SCHEMA = 'pulse.release_envelope.v1';
const EPOCH_SCHEMA = 'pulse.minimum_release_epoch.v1';
const REQUIRED_ARTIFACTS = Object.freeze([
  'daemon', 'embedder-runtime', 'model', 'plugin-runtime', 'presence-helper',
]);
const NATIVE_EXECUTABLES = new Set(['daemon', 'embedder-runtime', 'presence-helper']);
const FORMAT_BY_KIND = Object.freeze({
  daemon: 'dmg',
  'embedder-runtime': 'dmg',
  model: 'safetensors',
  'plugin-runtime': 'tar.gz',
  'presence-helper': 'dmg',
});
const SAFE_ID = /^[a-z0-9][a-z0-9._-]{0,127}$/;
const SEMVER = /^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?$/;
const SHA256 = /^[a-f0-9]{64}$/;
const TEAM_ID = /^[A-Z0-9]{10}$/;

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

function validateSigning(signing, executable) {
  exactKeys(signing, ['gatekeeper', 'identifier', 'notarized', 'scheme', 'stapled', 'team_id'], 'artifact_signing_fields_invalid');
  if (executable) {
    if (signing.scheme !== 'apple-developer-id' || signing.notarized !== true || signing.gatekeeper !== true || signing.stapled !== true ||
        typeof signing.identifier !== 'string' || !SAFE_ID.test(signing.identifier) ||
        typeof signing.team_id !== 'string' || !TEAM_ID.test(signing.team_id)) fail('artifact_signing_invalid');
    return;
  }
  if (signing.scheme !== 'release-manifest' || signing.notarized !== false || signing.gatekeeper !== false || signing.stapled !== false ||
      signing.identifier !== null || signing.team_id !== null) fail('artifact_signing_invalid');
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
  validateSigning(artifact.signing, executable);
}

function validatePayload(payload, options) {
  exactKeys(payload, ['allowed_origins', 'artifacts', 'release', 'schema'], 'manifest_fields_invalid');
  if (payload.schema !== MANIFEST_SCHEMA) fail('manifest_schema_invalid');
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
  exactKeys(payload.artifacts, REQUIRED_ARTIFACTS, 'artifact_set_invalid');
  const allowedOrigins = new Set(payload.allowed_origins);
  for (const name of REQUIRED_ARTIFACTS) validateArtifact(name, payload.artifacts[name], release, allowedOrigins, options);
  return release;
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

function deepFreeze(value) {
  if (value && typeof value === 'object') {
    for (const child of Object.values(value)) deepFreeze(child);
    Object.freeze(value);
  }
  return value;
}

export function verifyReleaseManifestEnvelope(envelope, options = {}) {
  exactKeys(envelope, ['payload', 'schema', 'signature'], 'envelope_fields_invalid');
  if (envelope.schema !== ENVELOPE_SCHEMA) fail('envelope_schema_invalid');
  exactKeys(envelope.signature, ['algorithm', 'key_id', 'value'], 'signature_fields_invalid');
  if (envelope.signature.algorithm !== 'ed25519' || typeof envelope.signature.key_id !== 'string' ||
      !SHA256.test(envelope.signature.key_id)) fail('release_signature_invalid');
  const release = validatePayload(envelope.payload, options);
  if (release.key_id !== envelope.signature.key_id) fail('release_key_id_mismatch');
  const canonicalBytes = Buffer.from(canonicalReleaseJSON(envelope.payload));
  const digest = createHash('sha256').update(canonicalBytes).digest('hex');
  const key = trustedReleaseKey(options.trustedKeys, envelope.signature.key_id, release.epoch);
  if (!verify(null, canonicalBytes, key, signatureBytes(envelope.signature.value))) fail('release_signature_invalid');

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
  const artifacts = JSON.parse(canonicalReleaseJSON(envelope.payload.artifacts));
  return deepFreeze({
    artifacts,
    downgrade_authorized: downgradeAuthorized,
    epoch: release.epoch,
    manifest_digest: digest,
    schema: 'pulse.verified_release_manifest.v1',
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

export const RELEASE_ARTIFACT_KINDS = REQUIRED_ARTIFACTS;
