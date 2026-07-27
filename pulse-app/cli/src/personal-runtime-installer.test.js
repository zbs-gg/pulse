import assert from 'node:assert/strict';
import { createHash, generateKeyPairSync, sign } from 'node:crypto';
import {
  chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import test from 'node:test';

import { loadNativeUniversalMatrix } from '../scripts/native-universal-matrix.mjs';
import { canonicalReleaseJSON, releaseKeyID } from './release-manifest.js';
import { readActivatedArtifactSet } from './artifact-installer.js';
import { DESKTOP_TARGET_IDS, desktopTargetDefinition } from './desktop-target.js';
import { acquireInstallLock } from './install-journal.js';
import { createPlatformServices } from './platform-services.js';
import {
  commitPersonalRuntimeRelease,
  inspectPersonalRelease,
  inspectPersonalRuntime,
  provisionPersonalRuntime,
  refreshPersonalReleaseSnapshot,
} from './personal-runtime-installer.js';

const digest = (bytes) => createHash('sha256').update(bytes).digest('hex');

function tree(files) {
  return {
    schema: 'pulse.artifact_tree.v1',
    files: files.map(([path, bytes, mode]) => ({
      path, bytes: bytes.length, sha256: digest(bytes), mode, executable: (mode & 0o111) !== 0,
    })),
  };
}

const PORTABLE_MODEL_REQUIRED_FILES = Object.freeze([
  'LICENSES/BGE-M3-MIT.txt', 'PROVENANCE.json', 'model_int8.onnx', 'pulse-model-contract.json',
  'support/config.json', 'support/special_tokens_map.json', 'support/tokenizer.json', 'support/tokenizer_config.json',
]);

function portableModelPolicy() {
  return {
    custom_code: false, data_only: true, engine: 'transformers-js-onnx', model: 'BAAI/bge-m3',
    required_files: [...PORTABLE_MODEL_REQUIRED_FILES],
    revision: '5617a9f61b028005a4858fdac845db406aefb181',
  };
}

function treeDigest(value) {
  return digest(Buffer.from(canonicalReleaseJSON(value)));
}

function fixture() {
  const rootPair = generateKeyPairSync('ed25519');
  const channelPair = generateKeyPairSync('ed25519');
  const publicKey = rootPair.publicKey.export({ type: 'spki', format: 'pem' });
  const keyID = releaseKeyID(publicKey);
  const channelPublicKey = channelPair.publicKey.export({ type: 'spki', format: 'pem' });
  const channelKeyID = releaseKeyID(channelPublicKey);
  const files = {
    daemon: [['bin/pulse', Buffer.from('#!/bin/sh\nexit 0\n'), 0o700]],
    'embedder-runtime': [
      ['bin/pulse-embedder', Buffer.from('#!/bin/sh\nexit 0\n'), 0o700],
      ['runtime/package.json', Buffer.from('{}\n'), 0o600],
    ],
    model: [
      ['LICENSES/BGE-M3-MIT.txt', Buffer.from('MIT\n'), 0o600],
      ['PROVENANCE.json', Buffer.from('{"source":"BAAI/bge-m3"}\n'), 0o600],
      ['model_int8.onnx', Buffer.from('fixture-onnx\n'), 0o600],
      ['pulse-model-contract.json', Buffer.from('{"schema":"pulse.portable_embedder.model.v1"}\n'), 0o600],
      ['support/config.json', Buffer.from('{}\n'), 0o600],
      ['support/special_tokens_map.json', Buffer.from('{}\n'), 0o600],
      ['support/tokenizer.json', Buffer.from('{}\n'), 0o600],
      ['support/tokenizer_config.json', Buffer.from('{}\n'), 0o600],
    ],
    'plugin-runtime': [['runtime/index.js', Buffer.from('export {};\n'), 0o600]],
    'presence-helper': [['bin/gg.zbs.pulse.presence-helper', Buffer.from('#!/bin/sh\nexit 0\n'), 0o700]],
  };
  const artifacts = {};
  const carriers = new Map();
  for (const kind of Object.keys(files)) {
    const executable = ['daemon', 'embedder-runtime', 'presence-helper'].includes(kind);
    const format = 'tar.gz';
    const carrier = Buffer.from(`carrier:${kind}`);
    const url = `https://releases.zbs.gg/pulse/0.7.0/${kind}.${format}`;
    carriers.set(url, carrier);
    artifacts[kind] = {
      architecture: 'arm64', bytes: carrier.length, epoch: 7, executable, format,
      id: `pulse-${kind}`, kind, minimum_os: '13.0',
      model_policy: kind === 'model' ? portableModelPolicy() : null,
      origin: 'https://releases.zbs.gg', platform: 'darwin', sha256: digest(carrier),
      signing: executable ? {
        gatekeeper: true, identifier: `gg.zbs.pulse.${kind}`, notarized: true,
        scheme: 'apple-developer-id', stapled: false, team_id: '44N4NZ86S5',
      } : {
        gatekeeper: false, identifier: null, notarized: false,
        scheme: 'release-manifest', stapled: false, team_id: null,
      },
      tree_digest: treeDigest(tree(files[kind])), url, version: '0.7.0',
    };
  }
  const targets = Object.fromEntries(DESKTOP_TARGET_IDS.map((targetID) => {
    const target = desktopTargetDefinition(targetID);
    const capabilities = target.platform === 'darwin' ? ['presence-helper'] : [];
    const kinds = ['daemon', 'embedder-runtime', ...(capabilities.length > 0 ? ['presence-helper'] : [])];
    const targetArtifacts = Object.fromEntries(kinds.map((kind) => {
      const url = `https://releases.zbs.gg/pulse/0.7.0/${targetID}/${kind}.tar.gz`;
      const signing = target.platform === 'darwin' ? artifacts[kind].signing : {
        gatekeeper: false,
        identifier: null,
        notarized: false,
        scheme: target.platform === 'win32' ? 'windows-authenticode' : 'release-manifest',
        stapled: false,
        team_id: null,
      };
      carriers.set(url, Buffer.from(`carrier:${kind}`));
      return [kind, {
        ...artifacts[kind],
        architecture: target.architecture,
        id: `pulse-${targetID}-${kind}`,
        minimum_os: target.platform === 'darwin' ? '13.0' : '0.0',
        platform: target.platform,
        signing,
        url,
      }];
    }));
    const verificationProfile = target.platform === 'darwin' ? {
      gatekeeper: true, kind: 'apple', notarized: true, stapled: false, team_id: '44N4NZ86S5',
    } : target.platform === 'win32' ? {
      kind: 'windows', publisher: 'CN=ZBS GG Inc.',
      timestamp_url: 'https://timestamp.digicert.com', timestamped: true,
    } : {
      kind: 'linux', policy: 'signed-catalog-tree-v1',
    };
    return [targetID, {
      architecture: target.architecture,
      artifacts: targetArtifacts,
      capabilities,
      libc: target.libc,
      platform: target.platform,
      verification_profile: verificationProfile,
    }];
  }));
  const payload = {
    allowed_origins: ['https://releases.zbs.gg'],
    common_artifacts: {
      model: { ...artifacts.model, architecture: 'all', minimum_os: '0.0', platform: 'all' },
      'plugin-runtime': { ...artifacts['plugin-runtime'], architecture: 'all', minimum_os: '0.0', platform: 'all' },
    },
    release: {
      channel: 'preview', epoch: 7, expires_at: '2026-08-01T00:00:00.000Z',
      issued_at: '2026-07-15T00:00:00.000Z', key_id: channelKeyID,
      package: '@zbs-gg/pulse', version: '0.7.0',
    },
    schema: 'pulse.personal_preview.release_catalog.v2',
    targets,
  };
  const authorityPayload = {
    channel: 'preview', epoch: 7, expires_at: '2026-08-02T00:00:00.000Z',
    issued_at: '2026-07-15T00:00:00.000Z',
    keys: [{
      key_id: channelKeyID, public_key_pem: channelPublicKey,
      valid_from_epoch: 7, valid_through_epoch: 9,
    }],
    revoked_key_ids: [], schema: 'pulse.release_authority.v1',
  };
  const envelope = {
    authority: {
      payload: authorityPayload, schema: 'pulse.release_authority_envelope.v1',
      signature: {
        algorithm: 'ed25519', key_id: keyID,
        value: sign(null, Buffer.from(canonicalReleaseJSON(authorityPayload)), rootPair.privateKey).toString('base64'),
      },
    },
    payload, schema: 'pulse.release_catalog_envelope.v2',
    signature: {
      algorithm: 'ed25519', key_id: channelKeyID,
      value: sign(null, Buffer.from(canonicalReleaseJSON(payload)), channelPair.privateKey).toString('base64'),
    },
  };
  const materializers = Object.fromEntries(Object.entries(files).map(([kind, entries]) => [kind, {
    treeManifest: tree(entries),
    materialize: async (_source, target) => {
      for (const [path, bytes, mode] of entries) {
        const destination = join(target, path);
        mkdirSync(dirname(destination), { recursive: true, mode: 0o700 });
        writeFileSync(destination, bytes, { mode });
        chmodSync(destination, mode);
      }
      return { treeManifest: tree(entries) };
    },
  }]));
  return {
    artifacts, carriers, channelPrivateKey: channelPair.privateKey, envelope, files, materializers,
    publicKey, keyID, rootPrivateKey: rootPair.privateKey,
  };
}

function legacyEnvelope(value) {
  const artifacts = Object.fromEntries(Object.entries(value.artifacts).map(([kind, artifact]) => {
    const { tree_digest: _treeDigest, ...legacy } = artifact;
    if (kind !== 'model') {
      return [kind, legacy.executable ? {
        ...legacy, format: 'dmg', signing: { ...legacy.signing, stapled: true },
        url: `https://releases.zbs.gg/pulse/0.7.0/${kind}.dmg`,
      } : legacy];
    }
    const bytes = Buffer.from('\u0010\u0000\u0000\u0000\u0000\u0000\u0000\u0000{"x":{"dtype":"U8","shape":[1],"data_offsets":[0,1]}}\u0000');
    return [kind, {
      ...legacy, bytes: bytes.length, format: 'safetensors',
      model_policy: { custom_code: false, data_only: true }, sha256: digest(bytes),
      url: 'https://releases.zbs.gg/pulse/0.7.0/model.safetensors',
    }];
  }));
  const payload = {
    allowed_origins: ['https://releases.zbs.gg'], artifacts,
    release: {
      channel: 'preview', epoch: 7, expires_at: '2026-08-01T00:00:00.000Z',
      issued_at: '2026-07-15T00:00:00.000Z', key_id: value.keyID,
      package: '@zbs-gg/pulse', version: '0.7.0',
    },
    schema: 'pulse.personal_preview.release_manifest.v1',
  };
  return {
    payload, schema: 'pulse.release_envelope.v1',
    signature: {
      algorithm: 'ed25519', key_id: value.keyID,
      value: sign(null, Buffer.from(canonicalReleaseJSON(payload)), value.rootPrivateKey).toString('base64'),
    },
  };
}

function resignCatalog(value) {
  value.envelope.signature.value = sign(
    null,
    Buffer.from(canonicalReleaseJSON(value.envelope.payload)),
    value.channelPrivateKey,
  ).toString('base64');
}

function v3Fixture(value, { expiresAt = '2026-08-14T00:00:00.000Z' } = {}) {
  const payload = structuredClone(value.envelope.payload);
  delete payload.release.expires_at;
  delete payload.release.issued_at;
  payload.schema = 'pulse.personal_release_artifact_set_payload.v1';
  payload.snapshot_url = 'https://releases.zbs.gg/pulse/0.7.0/catalog/snapshot.json';
  payload.host_policy = {
    harnesses: loadNativeUniversalMatrix().harnesses.map((harness) => structuredClone(harness)),
  };
  const artifactSet = {
    payload,
    schema: 'pulse.personal_release_artifact_set.v1',
    signature: {
      algorithm: 'ed25519', key_id: payload.release.key_id,
      value: sign(null, Buffer.from(canonicalReleaseJSON(payload)), value.channelPrivateKey).toString('base64'),
    },
  };
  const channel = value.envelope.authority.payload.keys[0];
  const snapshotPayload = {
    artifact_set: {
      sha256: digest(Buffer.from(`${canonicalReleaseJSON(artifactSet)}\n`)),
      url: 'https://releases.zbs.gg/pulse/0.7.0/epoch-7/catalog/artifact-set.json',
    },
    channel: { ...channel, valid_from_epoch: 7, valid_through_epoch: 7 },
    expires_at: expiresAt,
    issued_at: '2026-07-15T00:00:00.000Z',
    package: '@zbs-gg/pulse',
    release_epoch: 7,
    revoked_key_ids: [],
    schema: 'pulse.release_snapshot.v1',
    version: '0.7.0',
  };
  const snapshot = {
    payload: snapshotPayload,
    schema: 'pulse.release_snapshot_envelope.v1',
    signature: {
      algorithm: 'ed25519', key_id: value.keyID,
      value: sign(null, Buffer.from(canonicalReleaseJSON(snapshotPayload)), value.rootPrivateKey).toString('base64'),
    },
  };
  return { artifactSet, snapshot, snapshotBytes: `${canonicalReleaseJSON(snapshot)}\n` };
}

test('fixture release verification is available only through explicit test mode', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-personal-fixture-release.'));
  const manifestPath = join(root, 'manifest.json');
  const dataDir = join(root, 'data');
  const value = fixture();
  const target = value.envelope.payload.targets['darwin-arm64'];
  target.verification_profile = { fixture_id: 'native-darwin-arm64', kind: 'fixture', production: false };
  for (const artifact of Object.values(target.artifacts)) {
    artifact.signing = {
      gatekeeper: false, identifier: null, notarized: false,
      scheme: 'fixture', stapled: false, team_id: null,
    };
  }
  resignCatalog(value);
  mkdirSync(join(dataDir, 'artifacts'), { recursive: true, mode: 0o700 });
  writeFileSync(manifestPath, `${canonicalReleaseJSON(value.envelope)}\n`, { mode: 0o600 });
  const options = {
    architecture: 'arm64', dataDir, manifestPath,
    now: new Date('2026-07-16T00:00:00.000Z'), osVersion: '14.5',
    packageVersion: '0.7.0', platform: 'darwin',
    trustedKeys: [{
      key_id: value.keyID, public_key_pem: value.publicKey,
      valid_from_epoch: 1, valid_through_epoch: 20,
    }],
  };
  try {
    assert.throws(() => inspectPersonalRelease(options), (error) => error?.code === 'release_fixture_verification_forbidden');
    const inspected = inspectPersonalRelease({ ...options, testMode: true });
    assert.equal(inspected.release.verification_profile.production, false);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('empty Personal install stages a complete candidate then publishes one atomic generation after health', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-personal-runtime.'));
  const manifestPath = join(root, 'manifest.json');
  const dataDir = join(root, 'data');
  const value = fixture();
  const basePlatformServices = createPlatformServices({ platform: 'darwin', architecture: 'arm64' });
  let privateDirectoryCalls = 0;
  const platformServices = {
    ...basePlatformServices,
    ensurePrivateDirectory(path) {
      privateDirectoryCalls += 1;
      return basePlatformServices.ensurePrivateDirectory(path);
    },
  };
  writeFileSync(manifestPath, `${canonicalReleaseJSON(value.envelope)}\n`, { mode: 0o600 });
  const requests = [];
  try {
    const installed = await provisionPersonalRuntime({
      architecture: 'arm64', dataDir, manifestPath,
      fetchImpl: async (url) => {
        requests.push(String(url));
        const bytes = value.carriers.get(String(url));
        return new Response(bytes, { status: 200, headers: { etag: `"${digest(bytes)}"` } });
      },
      materializers: value.materializers,
      now: new Date('2026-07-16T00:00:00.000Z'), osVersion: '14.5',
      packageVersion: '0.7.0', platform: 'darwin', testMode: true,
      platformServices,
      trustedKeys: [{
        key_id: value.keyID, public_key_pem: value.publicKey,
        valid_from_epoch: 1, valid_through_epoch: 20,
      }],
    });
    assert.equal(requests.length, 5);
    assert.ok(privateDirectoryCalls > 0, 'selected platform services reach artifact activation and private journals');
    assert.equal(installed.release.manifest_digest.length, 64);
    assert.equal(installed.release.schema, 'pulse.verified_release_manifest.v2');
    assert.deepEqual(installed.release.artifacts.model.model_policy.required_files, PORTABLE_MODEL_REQUIRED_FILES);
    assert.deepEqual(Object.keys(installed.activationSet.activations).sort(), Object.keys(value.files).sort());
		assert.equal(JSON.parse(readFileSync(join(dataDir, 'runtime', 'install-journal.json'), 'utf8')).phase, 'candidate_staged');
		assert.equal(existsSync(join(dataDir, 'runtime', 'minimum-release-epoch.json')), false,
			'artifact provisioning alone must not commit the anti-rollback floor');
		const stagedInspection = inspectPersonalRuntime({
			architecture: 'arm64', dataDir, manifestPath,
			now: new Date('2026-07-16T00:00:00.000Z'), osVersion: '14.5',
			packageVersion: '0.7.0', platform: 'darwin', testMode: true,
			platformServices,
			trustedKeys: [{
				key_id: value.keyID, public_key_pem: value.publicKey,
				valid_from_epoch: 1, valid_through_epoch: 20,
			}],
		});
		assert.equal(stagedInspection.ready, true);
		assert.equal(stagedInspection.reason_code, 'runtime_candidate_staged');
		assert.equal(existsSync(join(dataDir, 'artifacts', 'artifact-generation-authority.json')), false,
			'candidate inspection must not promote release authority');
		assert.throws(
			() => commitPersonalRuntimeRelease(installed.release, { dataDir: 'relative-data' }),
			/personal_runtime_configuration_invalid/,
		);
    assert.equal(commitPersonalRuntimeRelease(installed.release, { dataDir }), 7);
    assert.equal(JSON.parse(readFileSync(join(dataDir, 'runtime', 'minimum-release-epoch.json'), 'utf8')).epoch, 7);
    assert.equal(readActivatedArtifactSet(installed.release, { installRoot: join(dataDir, 'artifacts') }).record.epoch, 7);

		writeFileSync(join(dataDir, 'runtime', 'minimum-release-epoch.json'), 'corrupt derived cache\n', { mode: 0o600 });

    const inspection = inspectPersonalRuntime({
      architecture: 'arm64', dataDir, manifestPath,
      now: new Date('2026-07-16T00:00:00.000Z'), osVersion: '14.5',
      packageVersion: '0.7.0', platform: 'darwin',
      trustedKeys: [{
        key_id: value.keyID, public_key_pem: value.publicKey,
        valid_from_epoch: 1, valid_through_epoch: 20,
      }],
    });
    assert.equal(inspection.ready, true);
    assert.equal(inspection.reason_code, 'runtime_staged');
    assert.equal(inspection.release.manifest_digest, installed.release.manifest_digest);

    await provisionPersonalRuntime({
      architecture: 'arm64', dataDir, manifestPath,
      fetchImpl: async () => { throw new Error('idempotent install must not download'); },
      materializers: value.materializers,
      now: new Date('2026-07-16T00:00:00.000Z'), osVersion: '14.5',
      packageVersion: '0.7.0', platform: 'darwin', testMode: true,
      trustedKeys: [{
        key_id: value.keyID, public_key_pem: value.publicKey,
        valid_from_epoch: 1, valid_through_epoch: 20,
      }],
    });
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('v3 installer refreshes a bounded snapshot, caches it privately, and keeps an activated vault ready offline after expiry', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-personal-runtime-v3.'));
  const manifestPath = join(root, 'artifact-set.json');
  const dataDir = join(root, 'data');
  const snapshotPath = join(dataDir, 'runtime', 'release-snapshot.json');
  const value = fixture();
  const release = v3Fixture(value);
  const trustedKeys = [{
    key_id: value.keyID, public_key_pem: value.publicKey,
    valid_from_epoch: 1, valid_through_epoch: 20,
  }];
  writeFileSync(manifestPath, `${canonicalReleaseJSON(release.artifactSet)}\n`, { mode: 0o600 });
  let snapshotRequests = 0;
  try {
    const refreshed = await refreshPersonalReleaseSnapshot({
      architecture: 'arm64', dataDir, manifestPath,
      fetchImpl: async (url, init) => {
        snapshotRequests += 1;
        assert.equal(url, release.artifactSet.payload.snapshot_url);
        assert.equal(init.redirect, 'manual');
        assert.equal(init.signal instanceof AbortSignal, true);
        return new Response(release.snapshotBytes, { status: 200 });
      },
      now: new Date('2026-07-16T00:00:00.000Z'), osVersion: '14.5',
      packageVersion: '0.7.0', platform: 'darwin', testMode: true, trustedKeys,
    });
    assert.equal(refreshed.refreshed, true);
    assert.equal(snapshotRequests, 1);
    assert.equal(readFileSync(snapshotPath, 'utf8'), release.snapshotBytes);
    assert.equal((await import('node:fs')).statSync(snapshotPath).mode & 0o077, 0);

    const installed = await provisionPersonalRuntime({
      architecture: 'arm64', dataDir, manifestPath, snapshotPath,
      fetchImpl: async (url) => {
        const bytes = value.carriers.get(String(url));
        return new Response(bytes, { status: 200, headers: { etag: `"${digest(bytes)}"` } });
      },
      materializers: value.materializers,
      now: new Date('2026-07-16T00:00:00.000Z'), osVersion: '14.5',
      packageVersion: '0.7.0', platform: 'darwin', testMode: true, trustedKeys,
    });
    assert.equal(installed.release.schema, 'pulse.verified_release_manifest.v3');
    assert.equal(commitPersonalRuntimeRelease(installed.release, { dataDir }), 7);
    const offline = inspectPersonalRuntime({
      architecture: 'arm64', dataDir, manifestPath, snapshotPath,
      now: new Date('2026-08-15T00:00:00.000Z'), osVersion: '14.5',
      packageVersion: '0.7.0', platform: 'darwin', testMode: true, trustedKeys,
    });
    assert.equal(offline.ready, true);
    assert.equal(offline.reason_code, 'catalog_refresh_required');
    assert.equal(offline.release.snapshot_refresh_required, true);
    assert.throws(() => inspectPersonalRelease({
      architecture: 'arm64', dataDir, manifestPath, snapshotPath,
      now: new Date('2026-08-15T00:00:00.000Z'), osVersion: '14.5',
      packageVersion: '0.7.0', platform: 'darwin', testMode: true, trustedKeys,
    }), (error) => error?.code === 'release_snapshot_expired');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('v3 snapshot refresh rejects redirects and oversized responses without caching them', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-personal-runtime-v3-fail.'));
  const manifestPath = join(root, 'artifact-set.json');
  const dataDir = join(root, 'data');
  const value = fixture();
  const release = v3Fixture(value);
  const options = {
    architecture: 'arm64', dataDir, manifestPath,
    now: new Date('2026-07-16T00:00:00.000Z'), osVersion: '14.5',
    packageVersion: '0.7.0', platform: 'darwin', testMode: true,
    trustedKeys: [{
      key_id: value.keyID, public_key_pem: value.publicKey,
      valid_from_epoch: 1, valid_through_epoch: 20,
    }],
  };
  writeFileSync(manifestPath, `${canonicalReleaseJSON(release.artifactSet)}\n`, { mode: 0o600 });
  try {
    await assert.rejects(refreshPersonalReleaseSnapshot({
      ...options, fetchImpl: async () => new Response('', { status: 302, headers: { location: 'https://evil.example' } }),
    }), (error) => error?.code === 'release_snapshot_redirect_forbidden');
    await assert.rejects(refreshPersonalReleaseSnapshot({
      ...options, fetchImpl: async () => new Response(release.snapshotBytes, {
        status: 200, headers: { 'content-length': String(2 * 1024 * 1024 + 1) },
      }),
    }), (error) => error?.code === 'release_snapshot_unsafe');
    assert.equal(existsSync(join(dataDir, 'runtime', 'release-snapshot.json')), false);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('Personal runtime inspection verifies the release without creating install state', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-personal-runtime-inspect.'));
  const manifestPath = join(root, 'manifest.json');
  const dataDir = join(root, 'missing-data');
  const value = fixture();
  writeFileSync(manifestPath, `${canonicalReleaseJSON(value.envelope)}\n`, { mode: 0o600 });
  try {
    const releaseInspection = inspectPersonalRelease({
      architecture: 'arm64', dataDir, manifestPath,
      now: new Date('2026-07-16T00:00:00.000Z'), osVersion: '14.5',
      packageVersion: '0.7.0', platform: 'darwin',
      trustedKeys: [{
        key_id: value.keyID, public_key_pem: value.publicKey,
        valid_from_epoch: 1, valid_through_epoch: 20,
      }],
    });
    assert.equal(releaseInspection.ready, true);
    assert.equal(releaseInspection.reason_code, 'release_manifest_verified');
    assert.equal(releaseInspection.release.epoch, 7);

    const inspection = inspectPersonalRuntime({
      architecture: 'arm64', dataDir, manifestPath,
      now: new Date('2026-07-16T00:00:00.000Z'), osVersion: '14.5',
      packageVersion: '0.7.0', platform: 'darwin',
      trustedKeys: [{
        key_id: value.keyID, public_key_pem: value.publicKey,
        valid_from_epoch: 1, valid_through_epoch: 20,
      }],
    });
    assert.equal(inspection.ready, false);
    assert.equal(inspection.release.epoch, 7);
    assert.equal(existsSync(dataDir), false, 'inspection must not create Pulse product state');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('Personal release inspection obtains OS version through injected platform services', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-personal-platform-version.'));
  const manifestPath = join(root, 'manifest.json');
  const value = fixture();
  writeFileSync(manifestPath, `${canonicalReleaseJSON(value.envelope)}\n`, { mode: 0o600 });
  let calls = 0;
  try {
    const inspection = inspectPersonalRelease({
      architecture: 'arm64', dataDir: join(root, 'data'), manifestPath,
      now: new Date('2026-07-16T00:00:00.000Z'), packageVersion: '0.7.0', platform: 'darwin',
      platformServices: { desktopOSVersion() { calls += 1; return '14.5'; } },
      trustedKeys: [{
        key_id: value.keyID, public_key_pem: value.publicKey,
        valid_from_epoch: 1, valid_through_epoch: 20,
      }],
    });
    assert.equal(inspection.ready, true);
    assert.equal(calls, 1);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('legacy v1 is historical-only and an unavailable target leaves zero install state', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-personal-target.'));
  const dataDir = join(root, 'data');
  const manifestPath = join(root, 'manifest.json');
  const value = fixture();
  const trustedKeys = [{
    key_id: value.keyID, public_key_pem: value.publicKey,
    valid_from_epoch: 1, valid_through_epoch: 20,
  }];
  try {
    writeFileSync(manifestPath, `${canonicalReleaseJSON(legacyEnvelope(value))}\n`, { mode: 0o600 });
    const historical = inspectPersonalRelease({
      architecture: 'arm64', dataDir, manifestPath,
      now: new Date('2026-07-16T00:00:00.000Z'), osVersion: '14.5',
      packageVersion: '0.7.0', platform: 'darwin', trustedKeys,
    });
    assert.equal(historical.ready, false);
    assert.equal(historical.reason_code, 'release_manifest_legacy');
    assert.equal(historical.release.historical_only, true);
    assert.equal(existsSync(dataDir), false);

    writeFileSync(manifestPath, `${canonicalReleaseJSON(value.envelope)}\n`, { mode: 0o600 });
    delete value.envelope.payload.targets['darwin-arm64'];
    resignCatalog(value);
    writeFileSync(manifestPath, `${canonicalReleaseJSON(value.envelope)}\n`, { mode: 0o600 });
    await assert.rejects(() => provisionPersonalRuntime({
      architecture: 'arm64', dataDir, manifestPath,
      now: new Date('2026-07-16T00:00:00.000Z'), osVersion: '14.5',
      packageVersion: '0.7.0', platform: 'darwin', testMode: true, trustedKeys,
    }), (error) => error.code === 'release_target_catalog_incomplete');
    assert.equal(existsSync(dataDir), false, 'unavailable target must fail before the install lock mutates state');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('test-only materializers are rejected outside explicit test mode', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-personal-runtime-policy.'));
  const value = fixture();
  const manifestPath = join(root, 'manifest.json');
  writeFileSync(manifestPath, `${canonicalReleaseJSON(value.envelope)}\n`, { mode: 0o600 });
  try {
    await assert.rejects(provisionPersonalRuntime({
      architecture: 'arm64', dataDir: join(root, 'data'), manifestPath,
      materializers: value.materializers,
      now: new Date('2026-07-16T00:00:00.000Z'), osVersion: '14.5',
      packageVersion: '0.7.0', platform: 'darwin', testMode: false,
      trustedKeys: [{
        key_id: value.keyID, public_key_pem: value.publicKey,
        valid_from_epoch: 1, valid_through_epoch: 20,
      }],
    }), /release_test_materializer_forbidden/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('Personal provisioning rejects relative data directories before filesystem mutation', async () => {
  await assert.rejects(provisionPersonalRuntime({
    dataDir: 'relative-data',
    fetchImpl: async () => { throw new Error('must not fetch'); },
  }), /personal_runtime_configuration_invalid/);
});

test('Personal provisioning serializes epoch verification behind the install lock', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-personal-runtime-lock.'));
  const dataDir = join(root, 'data');
  const release = acquireInstallLock(join(dataDir, 'runtime', 'install.lock'));
  try {
    await assert.rejects(provisionPersonalRuntime({
      dataDir,
      manifestPath: join(root, 'missing-manifest.json'),
      fetchImpl: async () => { throw new Error('must not fetch'); },
    }), /install_locked/);
  } finally {
    release();
    rmSync(root, { recursive: true, force: true });
  }
});
