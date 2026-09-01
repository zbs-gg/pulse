import { createHash } from 'node:crypto';
import { closeSync, createReadStream, existsSync, lstatSync, openSync, readFileSync, readSync } from 'node:fs';
import { basename, dirname, isAbsolute, join, resolve } from 'node:path';
import { Readable } from 'node:stream';
import { fileURLToPath } from 'node:url';

import {
  activateArtifactVersion,
  commitArtifactGeneration,
  downloadVerifiedArtifact,
  materializeVerifiedTree,
  readActivatedArtifactSet,
  readArtifactGenerationFloor,
  readCommittedArtifactSet,
  readStagedArtifactSet,
  recoverArtifactActivation,
} from './artifact-installer.js';
import { acquireInstallLock, writeInstallJournal } from './install-journal.js';
import {
  advanceMinimumReleaseEpoch,
  canonicalReleaseJSON,
  pinnedReleaseKeyring,
  readMinimumReleaseEpoch,
  verifyPersonalReleaseArtifactSet,
  verifyReleaseManifestEnvelope,
} from './release-manifest.js';
import { createPlatformServices } from './platform-services.js';
import { detectDesktopLibc } from './desktop-target.js';

const PACKAGE_ROOT = resolve(fileURLToPath(new URL('..', import.meta.url)));
const DEFAULT_MANIFEST_PATH = join(PACKAGE_ROOT, 'release', 'personal-preview-manifest.json');
const DEFAULT_PACKAGED_SNAPSHOT_PATH = join(PACKAGE_ROOT, 'release', 'personal-release-snapshot.json');
const PACKAGE_JSON_PATH = join(PACKAGE_ROOT, 'package.json');
const ARTIFACT_SET_SCHEMA = 'pulse.personal_release_artifact_set.v1';
const SNAPSHOT_MAX_BYTES = 2 * 1024 * 1024;
const SNAPSHOT_TIMEOUT_MS = 10_000;

export class PersonalRuntimeInstallerError extends Error {
  constructor(code) {
    super(code);
    this.name = 'PersonalRuntimeInstallerError';
    this.code = code;
  }
}

function fail(code) { throw new PersonalRuntimeInstallerError(code); }

function nativeFixtureInstallerStage(stage) {
  if (process.env.PULSE_NATIVE_PACKED_FIXTURE_ATTESTATION !== '1') return;
  if (typeof stage !== 'string' || !/^[a-z0-9_-]{1,96}$/.test(stage)) return;
  process.stderr.write(`[pulse-native-fixture] runtime provision stage: ${stage}\n`);
}

function readCanonicalEnvelope(path) {
  let bytes;
  let value;
  try {
    const stat = lstatSync(path);
    if (!stat.isFile() || stat.isSymbolicLink() || stat.size < 1 || stat.size > 2 * 1024 * 1024) fail('release_manifest_unsafe');
    bytes = readFileSync(path, 'utf8');
    value = JSON.parse(bytes);
  } catch (error) {
    if (error instanceof PersonalRuntimeInstallerError) throw error;
    fail('release_manifest_unavailable');
  }
  if (bytes !== `${canonicalReleaseJSON(value)}\n`) fail('release_manifest_noncanonical');
  return value;
}

function snapshotCachePath(dataDir) {
  return join(resolve(dataDir), 'runtime', 'release-snapshot.json');
}

async function fetchCanonicalSnapshot(url, { fetchImpl, timeoutMs = SNAPSHOT_TIMEOUT_MS } = {}) {
  let parsed;
  try { parsed = new URL(url); } catch { fail('release_snapshot_url_invalid'); }
  if (parsed.protocol !== 'https:' || parsed.username || parsed.password || parsed.search || parsed.hash) {
    fail('release_snapshot_url_invalid');
  }
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), timeoutMs);
  let response;
  try {
    response = await fetchImpl(parsed.href, { redirect: 'manual', signal: controller.signal });
  } catch {
    clearTimeout(timeout);
    fail('release_snapshot_unavailable');
  }
  try {
    if (!response || response.status >= 300 && response.status < 400 || response.redirected === true) {
      fail('release_snapshot_redirect_forbidden');
    }
    if (response.status !== 200) fail('release_snapshot_unavailable');
    if (response.url) {
      let finalURL;
      try { finalURL = new URL(response.url); } catch { fail('release_snapshot_redirect_forbidden'); }
      if (finalURL.href !== parsed.href || finalURL.origin !== parsed.origin) fail('release_snapshot_redirect_forbidden');
    }
    const declaredHeader = response.headers?.get?.('content-length');
    const declaredLength = declaredHeader === null || declaredHeader === undefined ? null : Number(declaredHeader);
    if (declaredLength !== null && (!Number.isFinite(declaredLength) ||
        declaredLength < 1 || declaredLength > SNAPSHOT_MAX_BYTES)) {
      fail('release_snapshot_unsafe');
    }
    const chunks = [];
    let size = 0;
    if (!response.body) fail('release_snapshot_unavailable');
    for await (const chunk of response.body) {
      const bytes = Buffer.from(chunk);
      size += bytes.length;
      if (size > SNAPSHOT_MAX_BYTES) fail('release_snapshot_unsafe');
      chunks.push(bytes);
    }
    if (size < 2) fail('release_snapshot_unsafe');
    const bytes = Buffer.concat(chunks).toString('utf8');
    let value;
    try { value = JSON.parse(bytes); } catch { fail('release_snapshot_invalid'); }
    if (bytes !== `${canonicalReleaseJSON(value)}\n`) fail('release_snapshot_noncanonical');
    return Object.freeze({ bytes, value });
  } catch (error) {
    if (error instanceof PersonalRuntimeInstallerError) throw error;
    fail('release_snapshot_unavailable');
  } finally {
    clearTimeout(timeout);
  }
}

function packageVersion(path = PACKAGE_JSON_PATH) {
  try {
    const value = JSON.parse(readFileSync(path, 'utf8'));
    if (typeof value.version !== 'string') fail('release_package_version_invalid');
    return value.version;
  } catch (error) {
    if (error instanceof PersonalRuntimeInstallerError) throw error;
    fail('release_package_version_invalid');
  }
}

function previousActivationDigest(path) {
  if (!existsSync(path)) return null;
  try {
    const stat = lstatSync(path);
    if (!stat.isFile() || stat.isSymbolicLink() || stat.size < 1 || stat.size > 64 * 1024) return null;
    return createHash('sha256').update(readFileSync(path)).digest('hex');
  } catch { return null; }
}

function loadReleaseTestMaterializers(path) {
  if (!isAbsolute(path)) fail('release_test_materializer_spec_invalid');
  let bytes;
  let value;
  try {
    const stat = lstatSync(path);
    if (!stat.isFile() || stat.isSymbolicLink() || stat.size < 1 || stat.size > 2 * 1024 * 1024) {
      fail('release_test_materializer_spec_invalid');
    }
    bytes = readFileSync(path, 'utf8');
    value = JSON.parse(bytes);
  } catch (error) {
    if (error instanceof PersonalRuntimeInstallerError) throw error;
    fail('release_test_materializer_spec_invalid');
  }
  if (bytes !== `${canonicalReleaseJSON(value)}\n` || value?.schema !== 'pulse.release_test_materializers.v1' ||
      Object.keys(value).sort().join('\0') !== 'artifacts\0schema' || !value.artifacts || Array.isArray(value.artifacts)) {
    fail('release_test_materializer_spec_invalid');
  }
  const materializers = {};
  for (const [kind, fixture] of Object.entries(value.artifacts)) {
    if (!fixture || Object.keys(fixture).sort().join('\0') !== 'source_root\0tree_manifest' ||
        !isAbsolute(fixture.source_root) || !fixture.tree_manifest) fail('release_test_materializer_spec_invalid');
    materializers[kind] = {
      treeManifest: fixture.tree_manifest,
      materialize: (_carrier, target, artifact, _treeManifest, options) => materializeVerifiedTree(
        fixture.source_root, target, artifact, fixture.tree_manifest, options,
      ),
    };
  }
  return materializers;
}

function releaseTestFetch(assetRoot) {
  if (!isAbsolute(assetRoot)) fail('release_test_asset_root_invalid');
  return async (url) => {
    let name;
    try { name = basename(new URL(url).pathname); } catch { fail('release_test_asset_url_invalid'); }
    if (!/^[a-z0-9][a-z0-9._-]{0,127}$/.test(name)) fail('release_test_asset_url_invalid');
    const path = join(resolve(assetRoot), name);
    let stat;
    try {
      stat = lstatSync(path);
      if (!stat.isFile() || stat.isSymbolicLink()) fail('release_test_asset_invalid');
    } catch (error) {
      if (error instanceof PersonalRuntimeInstallerError) throw error;
      fail('release_test_asset_invalid');
    }
    const descriptor = openSync(path, 'r');
    const hash = createHash('sha256');
    const buffer = Buffer.allocUnsafe(1024 * 1024);
    try {
      let offset = 0;
      while (offset < stat.size) {
        const count = readSync(descriptor, buffer, 0, Math.min(buffer.length, stat.size - offset), offset);
        if (count < 1) fail('release_test_asset_invalid');
        hash.update(buffer.subarray(0, count));
        offset += count;
      }
    } finally { closeSync(descriptor); }
    const etag = hash.digest('hex');
    return new Response(Readable.toWeb(createReadStream(path)), { status: 200, headers: { etag: `"${etag}"` } });
  };
}

function journalRecord(release, previous, phase) {
  return {
    artifact_ids: Object.values(release.artifacts).map((artifact) => artifact.id).sort(),
    manifest_digest: release.manifest_digest,
    phase,
    previous_activation_digest: previous,
    release_epoch: release.epoch,
    release_version: release.version,
    schema: 'pulse.personal_install_journal.v1',
  };
}

function recoverInvalidCurrent(artifact, installRoot, platformServices) {
  try {
    recoverArtifactActivation(artifact.id, { installRoot, platformServices });
  } catch {
    throw new PersonalRuntimeInstallerError('artifact_activation_unrecoverable');
  }
}

function readVerifiedPersonalRelease(manifestPath, epochPath, installRoot, verification) {
  const authorityPlatformServices = typeof verification.platformServices?.readPrivateFile === 'function'
    ? verification.platformServices
    : createPlatformServices({ platform: verification.platform, architecture: verification.architecture });
  const authorityFloor = readArtifactGenerationFloor({
    installRoot, platformServices: authorityPlatformServices,
  });
  const minimumAcceptedEpoch = authorityFloor > 0 ? authorityFloor : readMinimumReleaseEpoch(epochPath);
  const manifest = readCanonicalEnvelope(manifestPath);
  const options = {
    allowFixtureVerification: verification.testMode === true,
    allowExpiredSnapshot: verification.allowExpiredSnapshot === true,
    architecture: verification.architecture,
    libc: verification.libc,
    minimumAcceptedEpoch,
    now: verification.now,
    osVersion: verification.osVersion,
    packageVersion: verification.packageVersion,
    platform: verification.platform,
    trustedKeys: verification.trustedKeys,
  };
  if (manifest.schema === ARTIFACT_SET_SCHEMA) {
    if (typeof verification.snapshotPath !== 'string' || !isAbsolute(verification.snapshotPath)) {
      fail('release_snapshot_unavailable');
    }
    return verifyPersonalReleaseArtifactSet(manifest, readCanonicalEnvelope(verification.snapshotPath), options);
  }
  return verifyReleaseManifestEnvelope(manifest, options);
}

export async function refreshPersonalReleaseSnapshot({
  architecture = process.arch,
  dataDir,
  fetchImpl = globalThis.fetch,
  libc,
  manifestPath = DEFAULT_MANIFEST_PATH,
  now = new Date(),
  osVersion,
  packageVersion: expectedPackageVersion = packageVersion(),
  platform = process.platform,
  platformServices = createPlatformServices({ platform, architecture }),
  snapshotPath,
  testMode = false,
  trustedKeys = pinnedReleaseKeyring(),
} = {}) {
  if (typeof dataDir !== 'string' || !isAbsolute(dataDir) || typeof fetchImpl !== 'function') {
    fail('personal_runtime_configuration_invalid');
  }
  snapshotPath ??= snapshotCachePath(dataDir);
  if (typeof snapshotPath !== 'string' || !isAbsolute(snapshotPath)) fail('personal_runtime_configuration_invalid');
  let detectedOSVersion = osVersion;
  try { detectedOSVersion ??= platformServices.desktopOSVersion(); } catch { fail('release_os_version_invalid'); }
  const manifest = readCanonicalEnvelope(manifestPath);
  if (manifest.schema !== ARTIFACT_SET_SCHEMA) {
    return Object.freeze({ refreshed: false, release: verifyReleaseManifestEnvelope(manifest, {
      allowFixtureVerification: testMode === true,
      architecture, libc, minimumAcceptedEpoch: 0, now, osVersion: detectedOSVersion,
      packageVersion: expectedPackageVersion, platform, trustedKeys,
    }), snapshotPath: null });
  }
  const snapshotURL = manifest.payload?.snapshot_url;
  const fetched = await fetchCanonicalSnapshot(snapshotURL, { fetchImpl });
  const installRoot = join(resolve(dataDir), 'artifacts');
  const authorityFloor = readArtifactGenerationFloor({ installRoot, platformServices });
  const minimumAcceptedEpoch = authorityFloor > 0
    ? authorityFloor
    : readMinimumReleaseEpoch(join(resolve(dataDir), 'runtime', 'minimum-release-epoch.json'));
  const release = verifyPersonalReleaseArtifactSet(manifest, fetched.value, {
    allowFixtureVerification: testMode === true,
    architecture, libc, minimumAcceptedEpoch, now, osVersion: detectedOSVersion,
    packageVersion: expectedPackageVersion, platform, trustedKeys,
  });
  platformServices.ensurePrivateDirectory(dirname(snapshotPath));
  platformServices.atomicWritePrivateFile(snapshotPath, fetched.bytes);
  return Object.freeze({ refreshed: true, release, snapshotPath });
}

export async function provisionPersonalRuntime({
  architecture = process.arch,
  dataDir,
  fetchImpl = globalThis.fetch,
  manifestPath = DEFAULT_MANIFEST_PATH,
  materializers,
  libc,
  now = new Date(),
  osVersion,
  packageVersion: expectedPackageVersion = packageVersion(),
  platform = process.platform,
  platformServices = createPlatformServices({ platform, architecture }),
  snapshotPath,
  testMode = false,
  trustedKeys = pinnedReleaseKeyring(),
} = {}) {
  nativeFixtureInstallerStage('configuration_started');
  if (typeof dataDir !== 'string' || !isAbsolute(dataDir) || typeof fetchImpl !== 'function') {
    fail('personal_runtime_configuration_invalid');
  }
  snapshotPath ??= snapshotCachePath(dataDir);
  if (materializers !== undefined && !testMode) fail('release_test_materializer_forbidden');
  let detectedOSVersion = osVersion;
  try { detectedOSVersion ??= platformServices.desktopOSVersion(); } catch { fail('release_os_version_invalid'); }
  nativeFixtureInstallerStage('configuration_complete');
  const installRoot = join(resolve(dataDir), 'artifacts');
  const runtimeRoot = join(resolve(dataDir), 'runtime');
  const epochPath = join(runtimeRoot, 'minimum-release-epoch.json');
  const journalPath = join(runtimeRoot, 'install-journal.json');
  const lockPath = join(runtimeRoot, 'install.lock');
  if (existsSync(lockPath)) {
    nativeFixtureInstallerStage('existing_lock_recovery_started');
    const existingLock = acquireInstallLock(lockPath, { platformServices });
    existingLock();
    nativeFixtureInstallerStage('existing_lock_recovered');
  }
  nativeFixtureInstallerStage('preflight_release_started');
  const preflightRelease = readVerifiedPersonalRelease(manifestPath, epochPath, installRoot, {
        architecture, libc, now, osVersion: detectedOSVersion, packageVersion: expectedPackageVersion,
        platform, platformServices, snapshotPath, testMode, trustedKeys,
  });
  nativeFixtureInstallerStage('preflight_release_complete');
  if (preflightRelease.historical_only) fail('release_manifest_legacy');
  nativeFixtureInstallerStage('install_lock_started');
  const releaseLock = acquireInstallLock(lockPath, { platformServices });
  nativeFixtureInstallerStage('install_lock_acquired');
  try {
    nativeFixtureInstallerStage('release_verification_started');
    const release = readVerifiedPersonalRelease(manifestPath, epochPath, installRoot, {
      architecture, libc, now, osVersion: detectedOSVersion, packageVersion: expectedPackageVersion,
      platform, platformServices, snapshotPath, testMode, trustedKeys,
    });
    nativeFixtureInstallerStage('release_verification_complete');
    if (release.historical_only) fail('release_manifest_legacy');
    const activeSetPath = join(installRoot, 'artifact-generation-authority.json');
    const previous = previousActivationDigest(activeSetPath);
    try {
      nativeFixtureInstallerStage('active_set_inspection_started');
      const activationSet = readActivatedArtifactSet(release, { installRoot, platformServices });
      nativeFixtureInstallerStage('active_set_reused');
      writeInstallJournal(journalPath, journalRecord(release, previous, 'activated'), { platformServices });
      return { activationSet, release };
    } catch {
      nativeFixtureInstallerStage('active_set_requires_provision');
      /* provision or repair the exact signed set below */
    }

    writeInstallJournal(journalPath, journalRecord(release, previous, 'planned'), { platformServices });
    const stagingRoot = join(installRoot, 'downloads');
    const staged = {};
    writeInstallJournal(journalPath, journalRecord(release, previous, 'downloading'), { platformServices });
    for (const kind of Object.keys(release.artifacts).sort()) {
      const artifact = release.artifacts[kind];
      nativeFixtureInstallerStage(`download_${kind}_started`);
      staged[kind] = await downloadVerifiedArtifact(artifact, { stagingRoot, fetchImpl, platformServices });
      nativeFixtureInstallerStage(`download_${kind}_complete`);
    }
    writeInstallJournal(journalPath, journalRecord(release, previous, 'artifacts_staged'), { platformServices });
    writeInstallJournal(journalPath, journalRecord(release, previous, 'activating'), { platformServices });
    for (const kind of Object.keys(release.artifacts).sort()) {
      const artifact = release.artifacts[kind];
      nativeFixtureInstallerStage(`activation_${kind}_started`);
      const fixture = materializers?.[kind];
      const options = {
        installRoot, platformServices, publishDerivedPointers: false,
        ...(fixture ? {
          materialize: fixture.materialize,
          testOnlyMaterializer: true,
          ...(fixture.treeManifest ? { treeManifest: fixture.treeManifest } : {}),
        } : {}),
      };
      try {
        await activateArtifactVersion(artifact, staged[kind].path, options);
      } catch (error) {
        if (error?.code !== 'artifact_activation_current_invalid') throw error;
        recoverInvalidCurrent(artifact, installRoot, platformServices);
        await activateArtifactVersion(artifact, staged[kind].path, options);
      }
      nativeFixtureInstallerStage(`activation_${kind}_complete`);
    }
    nativeFixtureInstallerStage('staged_set_inspection_started');
    const activationSet = readStagedArtifactSet(release, { installRoot, platformServices });
    nativeFixtureInstallerStage('staged_set_inspection_complete');
    writeInstallJournal(journalPath, journalRecord(release, previous, 'candidate_staged'), { platformServices });
    return { activationSet, release };
  } finally {
    nativeFixtureInstallerStage('install_lock_release_started');
    releaseLock();
    nativeFixtureInstallerStage('install_lock_released');
  }
}

export function inspectPersonalRuntime({
  architecture = process.arch,
  dataDir,
  libc,
  manifestPath = DEFAULT_MANIFEST_PATH,
  now = new Date(),
  osVersion,
  packageVersion: expectedPackageVersion = packageVersion(),
  platform = process.platform,
  platformServices = createPlatformServices({ platform, architecture }),
  snapshotPath,
  testMode = false,
  trustedKeys = pinnedReleaseKeyring(),
} = {}) {
  if (typeof dataDir !== 'string' || !isAbsolute(dataDir)) {
    fail('personal_runtime_configuration_invalid');
  }
  snapshotPath ??= snapshotCachePath(dataDir);
  let detectedOSVersion = osVersion;
  try { detectedOSVersion ??= platformServices.desktopOSVersion(); } catch { fail('release_os_version_invalid'); }
  const root = resolve(dataDir);
  const release = readVerifiedPersonalRelease(
    manifestPath,
    join(root, 'runtime', 'minimum-release-epoch.json'),
    join(root, 'artifacts'),
    { architecture, libc, now, osVersion: detectedOSVersion, packageVersion: expectedPackageVersion,
      allowExpiredSnapshot: true, platform, platformServices, snapshotPath, testMode, trustedKeys },
  );
  if (release.historical_only) {
    return Object.freeze({
      activationSet: null,
      ready: false,
      reason_code: 'release_manifest_legacy',
      release,
    });
  }
  try {
    const activationSet = readActivatedArtifactSet(release, {
      installRoot: join(root, 'artifacts'), platformServices,
    });
    return Object.freeze({
      activationSet,
      ready: true,
      reason_code: release.snapshot_refresh_required === true ? 'catalog_refresh_required' : 'runtime_staged',
      release,
    });
  } catch (activeError) {
    try {
      const activationSet = readStagedArtifactSet(release, {
        installRoot: join(root, 'artifacts'), platformServices,
      });
      return Object.freeze({
        activationSet,
        ready: true,
        reason_code: release.snapshot_refresh_required === true ? 'catalog_refresh_required' : 'runtime_candidate_staged',
        release,
      });
    } catch {
      let committed;
      let committedError;
      try {
        committed = readCommittedArtifactSet({
          installRoot: join(root, 'artifacts'), platformServices,
        });
      } catch (error) {
        committedError = error;
      }
      if (committed?.record?.epoch < release.epoch) {
        return Object.freeze({
          activationSet: null,
          ready: false,
          reason_code: 'runtime_upgrade_required',
          release,
        });
      }
      return Object.freeze({
        activationSet: null,
        ready: false,
        reason_code: typeof committedError?.code === 'string'
          ? committedError.code
          : typeof activeError?.code === 'string' ? activeError.code : 'runtime_not_staged',
        release,
      });
    }
  }
}

// Verify only the signed release envelope and compatibility metadata. The
// disclosure screen uses this path before consent so it never pays the cost of
// hashing an already-installed model tree. Full artifact integrity remains a
// post-consent installer step under the install lock.
export function inspectPersonalRelease({
  architecture = process.arch,
  dataDir,
  libc,
  manifestPath = DEFAULT_MANIFEST_PATH,
  now = new Date(),
  osVersion,
  packageVersion: expectedPackageVersion = packageVersion(),
  platform = process.platform,
  platformServices = createPlatformServices({ platform, architecture }),
  snapshotPath,
  testMode = false,
  trustedKeys = pinnedReleaseKeyring(),
} = {}) {
  if (typeof dataDir !== 'string' || !isAbsolute(dataDir)) {
    fail('personal_runtime_configuration_invalid');
  }
  snapshotPath ??= snapshotCachePath(dataDir);
  let detectedOSVersion = osVersion;
  try { detectedOSVersion ??= platformServices.desktopOSVersion(); } catch { fail('release_os_version_invalid'); }
  const root = resolve(dataDir);
  const release = readVerifiedPersonalRelease(
    manifestPath,
    join(root, 'runtime', 'minimum-release-epoch.json'),
    join(root, 'artifacts'),
    { architecture, libc, now, osVersion: detectedOSVersion, packageVersion: expectedPackageVersion,
      platform, platformServices, snapshotPath, testMode, trustedKeys },
  );
  return Object.freeze({
    ready: !release.historical_only,
    reason_code: release.historical_only ? 'release_manifest_legacy' : 'release_manifest_verified',
    release,
  });
}

// Raise the anti-rollback floor only after managed retrieval and host
// activation have succeeded. A valid but unhealthy candidate must not strand
// the last-known-good release.
export function commitPersonalRuntimeRelease(release, {
  dataDir, platformServices = createPlatformServices(),
} = {}) {
  if (typeof dataDir !== 'string' || !isAbsolute(dataDir)) {
    fail('personal_runtime_configuration_invalid');
  }
  if (release?.historical_only !== false) fail('release_manifest_legacy');
  const root = resolve(dataDir);
  const installRoot = join(root, 'artifacts');
  const runtimeRoot = join(root, 'runtime');
  const releaseLock = acquireInstallLock(join(runtimeRoot, 'install.lock'));
  try {
    const authorityFloor = readArtifactGenerationFloor({ installRoot, platformServices });
    const legacyFloor = authorityFloor > 0 ? 0 : readMinimumReleaseEpoch(join(runtimeRoot, 'minimum-release-epoch.json'));
    commitArtifactGeneration(release, { installRoot, minimumAcceptedEpoch: legacyFloor, platformServices });
    try { advanceMinimumReleaseEpoch(join(runtimeRoot, 'minimum-release-epoch.json'), release.epoch); } catch {
      // Compatibility cache only. The atomic release authority remains the floor source.
    }
    return release.epoch;
  } finally {
    releaseLock();
  }
}

export function packagedPersonalRuntimeOptions(dataDir) {
  const testMode = process.env.PULSE_RELEASE_TEST_MODE === '1';
  const manifestOverride = process.env.PULSE_RELEASE_MANIFEST_PATH;
  const rootOverride = process.env.PULSE_RELEASE_TEST_ROOT_PATH;
  const snapshotOverride = process.env.PULSE_RELEASE_SNAPSHOT_PATH;
  if ((manifestOverride || rootOverride || snapshotOverride) && !testMode) fail('release_override_forbidden');
  const options = {
    dataDir,
    libc: detectDesktopLibc(),
    manifestPath: manifestOverride ?? DEFAULT_MANIFEST_PATH,
    snapshotPath: snapshotOverride ?? snapshotCachePath(dataDir),
    testMode,
    trustedKeys: pinnedReleaseKeyring(rootOverride),
  };
  if (testMode && process.env.PULSE_RELEASE_TEST_ASSET_ROOT) {
    options.fetchImpl = releaseTestFetch(process.env.PULSE_RELEASE_TEST_ASSET_ROOT);
  }
  if (testMode && process.env.PULSE_RELEASE_TEST_MATERIALIZER_SPEC) {
    options.materializers = loadReleaseTestMaterializers(process.env.PULSE_RELEASE_TEST_MATERIALIZER_SPEC);
  }
  return options;
}

// Installation disclosure must remain read-only. It verifies the snapshot
// shipped in the npm package and leaves the network refresh for the approved
// installation transaction.
export function packagedPersonalReleaseInspectionOptions(dataDir) {
  const options = packagedPersonalRuntimeOptions(dataDir);
  return {
    ...options,
    snapshotPath: process.env.PULSE_RELEASE_SNAPSHOT_PATH ?? DEFAULT_PACKAGED_SNAPSHOT_PATH,
  };
}

export async function refreshPackagedPersonalRelease(dataDir) {
  const options = packagedPersonalRuntimeOptions(dataDir);
  await refreshPersonalReleaseSnapshot(options);
  return options;
}
