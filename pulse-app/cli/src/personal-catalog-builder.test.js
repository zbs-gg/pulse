import assert from 'node:assert/strict';
import {
  createHash, generateKeyPairSync,
} from 'node:crypto';
import {
  chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { buildPersonalCatalog } from '../scripts/build-personal-catalog.mjs';
import { refreshReleaseSnapshot } from '../scripts/refresh-release-snapshot.mjs';
import { publishR2SnapshotRefresh } from '../scripts/publish-r2-release.mjs';
import { DESKTOP_TARGET_IDS, desktopTargetDefinition } from './desktop-target.js';
import {
  releaseKeyID, verifyPersonalReleaseArtifactSet,
} from './release-manifest.js';

const PACKAGE_VERSION = JSON.parse(readFileSync(new URL('../package.json', import.meta.url), 'utf8')).version;
const EPOCH = 9;

function digest(bytes) {
  return createHash('sha256').update(bytes).digest('hex');
}

function writeCanonical(path, value) {
  writeFileSync(path, `${JSON.stringify(value)}\n`, { mode: 0o600 });
}

function carrier(root, kind, identity) {
  const filename = `${kind}.tar.gz`;
  const bytes = Buffer.from(`synthetic catalog builder carrier: ${identity}: ${kind}\n`);
  writeFileSync(join(root, filename), bytes, { mode: 0o600 });
  return {
    bytes: bytes.length,
    filename,
    format: 'tar.gz',
    sha256: digest(bytes),
    tree_digest: digest(Buffer.from(`tree: ${identity}: ${kind}\n`)),
  };
}

function verificationProfile(platform) {
  if (platform === 'darwin') {
    return { gatekeeper: true, kind: 'apple', notarized: true, stapled: false, team_id: '44N4NZ86S5' };
  }
  if (platform === 'win32') {
    return {
      kind: 'windows', publisher: 'CN=ZBS GG Inc.',
      timestamp_url: 'https://timestamp.digicert.com', timestamped: true,
    };
  }
  return { kind: 'linux', policy: 'signed-catalog-tree-v1' };
}

function privateAuthority(root, name) {
  const pair = generateKeyPairSync('ed25519');
  const privatePath = join(root, `${name}.pem`);
  const publicKey = pair.publicKey.export({ type: 'spki', format: 'pem' });
  writeFileSync(privatePath, pair.privateKey.export({ type: 'pkcs8', format: 'pem' }), { mode: 0o600 });
  chmodSync(privatePath, 0o600);
  return { keyID: releaseKeyID(publicKey), path: privatePath, publicKey };
}

function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'pulse-personal-catalog-builder.'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const rootAuthority = privateAuthority(root, 'root-key');
  const channelAuthority = privateAuthority(root, 'channel-key');
  const targetRoots = Object.fromEntries(DESKTOP_TARGET_IDS.map((targetID) => {
    const target = desktopTargetDefinition(targetID);
    const targetRoot = join(root, 'targets', targetID);
    mkdirSync(targetRoot, { recursive: true, mode: 0o700 });
    const fragment = {
      artifacts: {
        daemon: carrier(targetRoot, 'daemon', targetID),
        'embedder-runtime': carrier(targetRoot, 'embedder-runtime', targetID),
      },
      attestation_state: 'pending-signed-catalog-runtime-proof',
      production_ready: false,
      schema: 'pulse.target_release_build.v2',
      target: {
        architecture: target.architecture,
        libc: target.libc,
        platform: target.platform,
        target_id: targetID,
      },
      verification_profile: verificationProfile(target.platform),
    };
    writeCanonical(join(targetRoot, 'target-release-fragment.json'), fragment);
    return [targetID, targetRoot];
  }));
  const modelRoot = join(root, 'model');
  const pluginRoot = join(root, 'plugin');
  mkdirSync(modelRoot, { mode: 0o700 });
  mkdirSync(pluginRoot, { mode: 0o700 });
  writeCanonical(join(modelRoot, 'portable-model-fragment.json'), {
    artifact: carrier(modelRoot, 'model', 'common'),
    production_ready: true,
    schema: 'pulse.portable_model_build.v1',
  });
  writeCanonical(join(pluginRoot, 'plugin-runtime-fragment.json'), {
    artifact: carrier(pluginRoot, 'plugin-runtime', 'common'),
    package_version: PACKAGE_VERSION,
    production_ready: true,
    schema: 'pulse.plugin_runtime_build.v1',
  });
  const trustedKeys = [{
    key_id: rootAuthority.keyID,
    public_key_pem: rootAuthority.publicKey,
    valid_from_epoch: EPOCH,
    valid_through_epoch: EPOCH,
  }];
  return {
    options: {
      channelKey: channelAuthority.path,
      epoch: EPOCH,
      modelRoot,
      origin: 'https://releases.example',
      outputRoot: join(root, 'output'),
      pluginRoot,
      rootKey: rootAuthority.path,
      targetRoots,
      testMode: true,
      testOnlyTrustedKeys: trustedKeys,
    },
    root,
    trustedKeys,
  };
}

test('personal catalog builder emits one signed exact six-target release', (t) => {
  const current = fixture(t);
  const result = buildPersonalCatalog(current.options);
  const envelope = JSON.parse(readFileSync(result.manifestPath, 'utf8'));
  const snapshot = JSON.parse(readFileSync(result.snapshotPath, 'utf8'));
  const receipt = JSON.parse(readFileSync(join(current.options.outputRoot, 'catalog-build-receipt.json'), 'utf8'));

  assert.deepEqual(Object.keys(envelope.payload.targets).sort(), DESKTOP_TARGET_IDS);
  assert.equal(envelope.schema, 'pulse.personal_release_artifact_set.v1');
  assert.equal(snapshot.schema, 'pulse.release_snapshot_envelope.v1');
  assert.equal(receipt.schema, 'pulse.personal_release_catalog_build.v3');
  assert.equal(receipt.production_ready, false, 'injected test authority can never claim a production catalog');
  assert.equal(receipt.target_count, 6);
  assert.equal(receipt.artifact_count, 14);
  assert.equal(receipt.host_target_count, 18);
  assert.deepEqual(receipt.hosts, ['claude-code', 'codex', 'cursor']);
  assert.equal(receipt.release_epoch, EPOCH);
  assert.equal(receipt.snapshot_digest, digest(readFileSync(result.snapshotPath)));
  assert.equal(receipt.artifact_set_digest, digest(readFileSync(result.artifactSetPath)));
  assert.deepEqual(receipt.target_ids, DESKTOP_TARGET_IDS);
  assert.doesNotMatch(readFileSync(result.manifestPath, 'utf8'), /\.dmg(?:"|\b)/);

  const manifestDigests = [];
  for (const targetID of DESKTOP_TARGET_IDS) {
    const target = desktopTargetDefinition(targetID);
    const release = verifyPersonalReleaseArtifactSet(envelope, snapshot, {
      architecture: target.architecture,
      libc: target.libc,
      minimumAcceptedEpoch: EPOCH,
      now: new Date(),
      osVersion: target.platform === 'darwin' ? '26.2' : '0.0',
      packageVersion: PACKAGE_VERSION,
      platform: target.platform,
      trustedKeys: current.trustedKeys,
    });
    assert.equal(release.target_id, targetID);
    assert.equal(release.schema, 'pulse.verified_release_manifest.v3');
    assert.equal(release.snapshot_refresh_required, false);
    assert.deepEqual(Object.keys(release.artifacts).sort(), [
      'daemon', 'embedder-runtime', 'model', 'plugin-runtime',
    ]);
    assert.equal(existsSync(join(
      current.options.outputRoot, 'pulse', PACKAGE_VERSION, `epoch-${EPOCH}`, targetID, 'daemon.tar.gz',
    )), true);
    manifestDigests.push(release.manifest_digest);
  }
  assert.equal(new Set(manifestDigests).size, 1);
  assert.equal(manifestDigests[0], receipt.manifest_digest);
});

test('personal catalog builder can emit a signed Mac Apple Silicon release first', (t) => {
  const current = fixture(t);
  current.options.targetRoots = {
    'darwin-arm64': current.options.targetRoots['darwin-arm64'],
  };
  const result = buildPersonalCatalog(current.options);
  const envelope = JSON.parse(readFileSync(result.manifestPath, 'utf8'));
  const snapshot = JSON.parse(readFileSync(result.snapshotPath, 'utf8'));
  const receipt = JSON.parse(readFileSync(join(current.options.outputRoot, 'catalog-build-receipt.json'), 'utf8'));

  assert.deepEqual(Object.keys(envelope.payload.targets), ['darwin-arm64']);
  assert.equal(receipt.target_count, 1);
  assert.equal(receipt.artifact_count, 4);
  assert.equal(receipt.host_target_count, 3);
  const release = verifyPersonalReleaseArtifactSet(envelope, snapshot, {
    architecture: 'arm64', minimumAcceptedEpoch: EPOCH, now: new Date(), osVersion: '26.2',
    packageVersion: PACKAGE_VERSION, platform: 'darwin', trustedKeys: current.trustedKeys,
  });
  assert.equal(release.target_id, 'darwin-arm64');
});

test('snapshot refresh changes only root-signed freshness while preserving immutable artifact bytes', (t) => {
  const current = fixture(t);
  const built = buildPersonalCatalog(current.options);
  const originalArtifactSet = readFileSync(built.artifactSetPath);
  const originalSnapshot = JSON.parse(readFileSync(built.snapshotPath));
  const refreshed = refreshReleaseSnapshot({
    artifactSetPath: built.artifactSetPath,
    currentSnapshotPath: built.snapshotPath,
    now: new Date(Date.parse(originalSnapshot.payload.issued_at) + 14 * 24 * 60 * 60 * 1000),
    outputRoot: join(current.root, 'snapshot-refresh'),
    rootKeyPath: current.options.rootKey,
    trustedKeys: current.trustedKeys,
  });
  assert.deepEqual(readFileSync(built.artifactSetPath), originalArtifactSet);
  assert.equal(refreshed.receipt.release_epoch, 9);
  assert.equal(refreshed.receipt.artifact_set_digest, built.receipt.artifact_set_digest);
  assert.notEqual(refreshed.receipt.snapshot_digest, built.receipt.snapshot_digest);
  assert.equal(
    Date.parse(refreshed.receipt.expires_at) - Date.parse(refreshed.receipt.issued_at),
    30 * 24 * 60 * 60 * 1000,
  );
});

test('snapshot-only publication proves the existing artifact set before replacing freshness bytes', async (t) => {
  const current = fixture(t);
  const built = buildPersonalCatalog(current.options);
  const original = JSON.parse(readFileSync(built.snapshotPath));
  const refreshed = refreshReleaseSnapshot({
    artifactSetPath: built.artifactSetPath,
    currentSnapshotPath: built.snapshotPath,
    now: new Date(),
    outputRoot: join(current.root, 'snapshot-publish'),
    rootKeyPath: current.options.rootKey,
    trustedKeys: current.trustedKeys,
  });
  const artifactSetKey = `pulse/${PACKAGE_VERSION}/epoch-${EPOCH}/catalog/artifact-set.json`;
  const snapshotKey = `pulse/${PACKAGE_VERSION}/catalog/snapshot.json`;
  const store = new Map([[artifactSetKey, {
    bytes: readFileSync(built.artifactSetPath), cacheControl: 'public, max-age=31536000, immutable',
    contentType: 'application/json', etag: 'artifact-set',
  }]]);
  const client = {
    async get(key) { return store.get(key).bytes; },
    async head(key) {
      const value = store.get(key);
      return value ? { bytes: value.bytes.length, cacheControl: value.cacheControl, contentType: value.contentType, etag: value.etag } : null;
    },
    async put(object) {
      const bytes = readFileSync(object.path);
      store.set(object.key, { bytes, cacheControl: object.cacheControl, contentType: object.contentType, etag: digest(bytes).slice(0, 32) });
    },
  };
  const fetchImpl = async (url, init = {}) => {
    const value = store.get(new URL(url).pathname.slice(1));
    if (!value) return new Response('', { status: 404 });
    const headers = { 'cache-control': value.cacheControl, etag: `"${value.etag}"` };
    if (init.method === 'HEAD') return new Response(null, { status: 200, headers });
    if (init.headers?.Range) return new Response(value.bytes.subarray(0, 1), {
      status: 206, headers: { ...headers, 'content-range': `bytes 0-0/${value.bytes.length}` },
    });
    return new Response(value.bytes, { status: 200, headers });
  };
  const receipt = await publishR2SnapshotRefresh({
    artifactSetPath: built.artifactSetPath,
    client,
    fetchImpl,
    snapshotPath: refreshed.snapshotPath,
    trustedKeys: current.trustedKeys,
  });
  assert.equal(receipt.schema, 'pulse.r2_snapshot_publication.v1');
  assert.equal(receipt.snapshot_digest, refreshed.receipt.snapshot_digest);
  assert.deepEqual(store.get(snapshotKey).bytes, readFileSync(refreshed.snapshotPath));
});

test('personal catalog builder blocks an empty target set before creating output', (t) => {
  const current = fixture(t);
  assert.throws(
    () => buildPersonalCatalog({ ...current.options, targetRoots: {} }),
    /release_catalog_arguments_invalid/,
  );
  assert.equal(existsSync(current.options.outputRoot), false);
});

test('personal catalog builder removes partial output when any carrier is corrupt', (t) => {
  const current = fixture(t);
  writeFileSync(join(current.options.targetRoots['win32-x64'], 'daemon.tar.gz'), 'corrupt\n', { mode: 0o600 });
  assert.throws(
    () => buildPersonalCatalog(current.options),
    /release_catalog_carrier_mismatch/,
  );
  assert.equal(existsSync(current.options.outputRoot), false);
});
