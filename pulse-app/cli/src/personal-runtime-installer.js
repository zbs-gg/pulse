import { createHash } from 'node:crypto';
import { closeSync, createReadStream, existsSync, lstatSync, openSync, readFileSync, readSync } from 'node:fs';
import { basename, isAbsolute, join, resolve } from 'node:path';
import { Readable } from 'node:stream';
import { fileURLToPath } from 'node:url';

import {
  activateArtifactVersion,
  downloadVerifiedArtifact,
  materializeVerifiedTree,
  readActivatedArtifactSet,
  recoverArtifactActivation,
  writeActivatedArtifactSet,
} from './artifact-installer.js';
import { acquireInstallLock, writeInstallJournal } from './install-journal.js';
import {
  advanceMinimumReleaseEpoch,
  canonicalReleaseJSON,
  pinnedReleaseKeyring,
  readMinimumReleaseEpoch,
  verifyReleaseManifestEnvelope,
} from './release-manifest.js';
import { createPlatformServices } from './platform-services.js';

const PACKAGE_ROOT = resolve(fileURLToPath(new URL('..', import.meta.url)));
const DEFAULT_MANIFEST_PATH = join(PACKAGE_ROOT, 'release', 'personal-preview-manifest.json');
const PACKAGE_JSON_PATH = join(PACKAGE_ROOT, 'package.json');

export class PersonalRuntimeInstallerError extends Error {
  constructor(code) {
    super(code);
    this.name = 'PersonalRuntimeInstallerError';
    this.code = code;
  }
}

function fail(code) { throw new PersonalRuntimeInstallerError(code); }

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

function readVerifiedPersonalRelease(manifestPath, epochPath, verification) {
  return verifyReleaseManifestEnvelope(readCanonicalEnvelope(manifestPath), {
    architecture: verification.architecture,
    libc: verification.libc,
    minimumAcceptedEpoch: readMinimumReleaseEpoch(epochPath),
    now: verification.now,
    osVersion: verification.osVersion,
    packageVersion: verification.packageVersion,
    platform: verification.platform,
    trustedKeys: verification.trustedKeys,
  });
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
  testMode = false,
  trustedKeys = pinnedReleaseKeyring(),
} = {}) {
  if (typeof dataDir !== 'string' || !isAbsolute(dataDir) || typeof fetchImpl !== 'function') {
    fail('personal_runtime_configuration_invalid');
  }
  if (materializers !== undefined && !testMode) fail('release_test_materializer_forbidden');
  let detectedOSVersion = osVersion;
  try { detectedOSVersion ??= platformServices.desktopOSVersion(); } catch { fail('release_os_version_invalid'); }
  const installRoot = join(resolve(dataDir), 'artifacts');
  const runtimeRoot = join(resolve(dataDir), 'runtime');
  const epochPath = join(runtimeRoot, 'minimum-release-epoch.json');
  const journalPath = join(runtimeRoot, 'install-journal.json');
  const lockPath = join(runtimeRoot, 'install.lock');
  if (existsSync(lockPath)) {
    const existingLock = acquireInstallLock(lockPath, { platformServices });
    existingLock();
  }
  const preflightRelease = readVerifiedPersonalRelease(manifestPath, epochPath, {
        architecture, libc, now, osVersion: detectedOSVersion, packageVersion: expectedPackageVersion, platform, trustedKeys,
  });
  if (preflightRelease.historical_only) fail('release_manifest_legacy');
  const releaseLock = acquireInstallLock(lockPath, { platformServices });
  try {
    const release = readVerifiedPersonalRelease(manifestPath, epochPath, {
      architecture, libc, now, osVersion: detectedOSVersion, packageVersion: expectedPackageVersion, platform, trustedKeys,
    });
    if (release.historical_only) fail('release_manifest_legacy');
    const activeSetPath = join(installRoot, 'active-release.json');
    const previous = previousActivationDigest(activeSetPath);
    try {
      const activationSet = readActivatedArtifactSet(release, { installRoot, platformServices });
      writeInstallJournal(journalPath, journalRecord(release, previous, 'activated'), { platformServices });
      return { activationSet, release };
    } catch { /* provision or repair the exact signed set below */ }

    writeInstallJournal(journalPath, journalRecord(release, previous, 'planned'), { platformServices });
    const stagingRoot = join(installRoot, 'downloads');
    const staged = {};
    writeInstallJournal(journalPath, journalRecord(release, previous, 'downloading'), { platformServices });
    for (const kind of Object.keys(release.artifacts).sort()) {
      const artifact = release.artifacts[kind];
      staged[kind] = await downloadVerifiedArtifact(artifact, { stagingRoot, fetchImpl, platformServices });
    }
    writeInstallJournal(journalPath, journalRecord(release, previous, 'artifacts_staged'), { platformServices });
    writeInstallJournal(journalPath, journalRecord(release, previous, 'activating'), { platformServices });
    for (const kind of Object.keys(release.artifacts).sort()) {
      const artifact = release.artifacts[kind];
      const fixture = materializers?.[kind];
      const options = {
        installRoot, platformServices,
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
    }
    const activationSet = writeActivatedArtifactSet(release, { installRoot, platformServices });
    writeInstallJournal(journalPath, journalRecord(release, previous, 'activated'), { platformServices });
    return { activationSet, release };
  } finally {
    releaseLock();
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
  trustedKeys = pinnedReleaseKeyring(),
} = {}) {
  if (typeof dataDir !== 'string' || !isAbsolute(dataDir)) {
    fail('personal_runtime_configuration_invalid');
  }
  let detectedOSVersion = osVersion;
  try { detectedOSVersion ??= platformServices.desktopOSVersion(); } catch { fail('release_os_version_invalid'); }
  const root = resolve(dataDir);
  const release = readVerifiedPersonalRelease(
    manifestPath,
    join(root, 'runtime', 'minimum-release-epoch.json'),
    { architecture, libc, now, osVersion: detectedOSVersion, packageVersion: expectedPackageVersion, platform, trustedKeys },
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
      reason_code: 'runtime_staged',
      release,
    });
  } catch (error) {
    return Object.freeze({
      activationSet: null,
      ready: false,
      reason_code: typeof error?.code === 'string' ? error.code : 'runtime_not_staged',
      release,
    });
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
  trustedKeys = pinnedReleaseKeyring(),
} = {}) {
  if (typeof dataDir !== 'string' || !isAbsolute(dataDir)) {
    fail('personal_runtime_configuration_invalid');
  }
  let detectedOSVersion = osVersion;
  try { detectedOSVersion ??= platformServices.desktopOSVersion(); } catch { fail('release_os_version_invalid'); }
  const root = resolve(dataDir);
  const release = readVerifiedPersonalRelease(
    manifestPath,
    join(root, 'runtime', 'minimum-release-epoch.json'),
    { architecture, libc, now, osVersion: detectedOSVersion, packageVersion: expectedPackageVersion, platform, trustedKeys },
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
export function commitPersonalRuntimeRelease(release, { dataDir } = {}) {
  if (typeof dataDir !== 'string' || !isAbsolute(dataDir)) {
    fail('personal_runtime_configuration_invalid');
  }
  if (release?.historical_only !== false) fail('release_manifest_legacy');
  const root = resolve(dataDir);
  const installRoot = join(root, 'artifacts');
  const runtimeRoot = join(root, 'runtime');
  const releaseLock = acquireInstallLock(join(runtimeRoot, 'install.lock'));
  try {
    readActivatedArtifactSet(release, { installRoot });
    return advanceMinimumReleaseEpoch(join(runtimeRoot, 'minimum-release-epoch.json'), release.epoch);
  } finally {
    releaseLock();
  }
}

export function packagedPersonalRuntimeOptions(dataDir) {
  const testMode = process.env.PULSE_RELEASE_TEST_MODE === '1';
  const manifestOverride = process.env.PULSE_RELEASE_MANIFEST_PATH;
  const rootOverride = process.env.PULSE_RELEASE_TEST_ROOT_PATH;
  if ((manifestOverride || rootOverride) && !testMode) fail('release_override_forbidden');
  const options = {
    dataDir,
    manifestPath: manifestOverride ?? DEFAULT_MANIFEST_PATH,
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
