import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import {
  createHash, generateKeyPairSync, sign,
} from 'node:crypto';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { once } from 'node:events';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { loadNativeUniversalMatrix } from '../scripts/native-universal-matrix.mjs';
import { loadPersonalReleaseHostPolicy } from '../scripts/personal-release-host-policy.mjs';
import { DESKTOP_TARGET_IDS } from './desktop-target.js';
import {
  ReleaseManifestError,
  advanceMinimumReleaseEpoch,
  assertSupportedNodeVersion,
  canonicalReleaseJSON,
  pinnedReleaseKeyring,
  readMinimumReleaseEpoch,
  releaseKeyID,
  verifyPersonalReleaseArtifactSet,
  verifyReleaseManifestEnvelope,
} from './release-manifest.js';

function keyFixture() {
  const pair = generateKeyPairSync('ed25519');
  const publicKey = pair.publicKey.export({ type: 'spki', format: 'pem' });
  return { ...pair, publicKey, keyID: releaseKeyID(publicKey) };
}

function artifact(kind, overrides = {}) {
  const executable = ['daemon', 'presence-helper', 'embedder-runtime'].includes(kind);
  const format = executable ? 'dmg' : kind === 'model' ? 'safetensors' : 'tar.gz';
  return {
    architecture: 'arm64',
    bytes: 4096,
    epoch: 7,
    executable,
    format,
    id: `pulse-${kind}`,
    kind,
    minimum_os: '13.0',
    model_policy: kind === 'model' ? { custom_code: false, data_only: true } : null,
    origin: 'https://releases.zbs.gg',
    platform: 'darwin',
    sha256: 'a'.repeat(64),
    signing: executable ? {
      gatekeeper: true,
      identifier: `gg.zbs.pulse.${kind}`,
      notarized: true,
      scheme: 'apple-developer-id',
      stapled: true,
      team_id: '44N4NZ86S5',
    } : {
      gatekeeper: false,
      identifier: null,
      notarized: false,
      scheme: 'release-manifest',
      stapled: false,
      team_id: null,
    },
    url: `https://releases.zbs.gg/pulse/0.8.1/${kind}.${format}`,
    version: '0.8.1',
    ...overrides,
  };
}

function payload(keyID, overrides = {}) {
  const base = {
    allowed_origins: ['https://releases.zbs.gg'],
    artifacts: {
      daemon: artifact('daemon'),
      'embedder-runtime': artifact('embedder-runtime'),
      model: artifact('model'),
      'plugin-runtime': artifact('plugin-runtime'),
      'presence-helper': artifact('presence-helper'),
    },
    release: {
      channel: 'preview',
      epoch: 7,
      expires_at: '2026-08-01T00:00:00.000Z',
      issued_at: '2026-07-15T00:00:00.000Z',
      key_id: keyID,
      package: '@zbs-gg/pulse',
      version: '0.8.1',
    },
    schema: 'pulse.personal_preview.release_manifest.v1',
  };
  return { ...base, ...overrides };
}

function envelope(keys, manifest = payload(keys.keyID), overrides = {}) {
  const signature = sign(null, Buffer.from(canonicalReleaseJSON(manifest)), keys.privateKey).toString('base64');
  return {
    payload: manifest,
    schema: 'pulse.release_envelope.v1',
    signature: { algorithm: 'ed25519', key_id: keys.keyID, value: signature },
    ...overrides,
  };
}

function verifyOptions(keys, overrides = {}) {
  return {
    architecture: 'arm64',
    minimumAcceptedEpoch: 7,
    now: new Date('2026-07-16T00:00:00.000Z'),
    osVersion: '14.5',
    packageVersion: '0.8.1',
    platform: 'darwin',
    trustedKeys: [{
      key_id: keys.keyID,
      public_key_pem: keys.publicKey,
      valid_from_epoch: 1,
      valid_through_epoch: 20,
    }],
    ...overrides,
  };
}

function portableSigning() {
  return {
    gatekeeper: false,
    identifier: null,
    notarized: false,
    scheme: 'release-manifest',
    stapled: false,
    team_id: null,
  };
}

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

function portableModelPolicy() {
  return {
    custom_code: false,
    data_only: true,
    engine: 'transformers-js-onnx',
    model: 'BAAI/bge-m3',
    required_files: [...PORTABLE_MODEL_REQUIRED_FILES],
    revision: '5617a9f61b028005a4858fdac845db406aefb181',
  };
}

function verificationProfile(platform) {
  if (platform === 'darwin') {
    return { gatekeeper: true, kind: 'apple', notarized: true, stapled: false, team_id: '44N4NZ86S5' };
  }
  if (platform === 'win32') {
    return {
      kind: 'windows',
      publisher: 'CN=ZBS GG Inc.',
      timestamp_url: 'https://timestamp.digicert.com',
      timestamped: true,
    };
  }
  return { kind: 'linux', policy: 'signed-catalog-tree-v1' };
}

function targetArtifact(kind, targetID, overrides = {}) {
  const [platform, architecture, libc = null] = targetID.split('-');
  const nativeExecutable = ['daemon', 'embedder-runtime', 'presence-helper'].includes(kind);
  const signing = nativeExecutable && platform === 'darwin'
    ? { ...artifact(kind).signing, stapled: false }
    : nativeExecutable && platform === 'win32'
      ? { ...portableSigning(), scheme: 'windows-authenticode' }
      : portableSigning();
  return artifact(kind, {
    architecture,
    format: 'tar.gz',
    minimum_os: platform === 'darwin' ? '13.0' : '0.0',
    model_policy: kind === 'model' ? portableModelPolicy() : null,
    platform,
    signing,
    tree_digest: 'b'.repeat(64),
    url: `https://releases.zbs.gg/pulse/0.8.1/${targetID}/${kind}.tar.gz`,
    ...overrides,
  });
}

function catalogPayload(channelKeys, overrides = {}) {
  const targets = Object.fromEntries(DESKTOP_TARGET_IDS.map((targetID) => {
    const [platform, architecture, libc = null] = targetID.split('-');
    const capabilities = platform === 'darwin' ? ['presence-helper'] : [];
    const artifacts = {
      daemon: targetArtifact('daemon', targetID),
      'embedder-runtime': targetArtifact('embedder-runtime', targetID),
      ...(capabilities.includes('presence-helper') ? { 'presence-helper': targetArtifact('presence-helper', targetID) } : {}),
    };
    return [targetID, {
      architecture, artifacts, capabilities, libc, platform,
      verification_profile: verificationProfile(platform),
    }];
  }));
  return {
    allowed_origins: ['https://releases.zbs.gg'],
    common_artifacts: {
      model: targetArtifact('model', 'darwin-arm64', {
        architecture: 'all', minimum_os: '0.0', platform: 'all',
        signing: portableSigning(),
        url: 'https://releases.zbs.gg/pulse/0.8.1/model.tar.gz',
      }),
      'plugin-runtime': targetArtifact('plugin-runtime', 'darwin-arm64', {
        architecture: 'all', minimum_os: '0.0', platform: 'all',
        signing: portableSigning(),
        url: 'https://releases.zbs.gg/pulse/0.8.1/plugin-runtime.tar.gz',
      }),
    },
    release: {
      channel: 'preview', epoch: 7, expires_at: '2026-08-01T00:00:00.000Z',
      issued_at: '2026-07-15T00:00:00.000Z', key_id: channelKeys.keyID,
      package: '@zbs-gg/pulse', version: '0.8.1',
    },
    schema: 'pulse.personal_preview.release_catalog.v2',
    targets,
    ...overrides,
  };
}

function authorityEnvelope(rootKeys, channelKeys, overrides = {}) {
  const authorityPayload = {
    channel: 'preview', epoch: 7,
    expires_at: '2026-08-02T00:00:00.000Z', issued_at: '2026-07-15T00:00:00.000Z',
    keys: [{
      key_id: channelKeys.keyID, public_key_pem: channelKeys.publicKey,
      valid_from_epoch: 7, valid_through_epoch: 9,
    }],
    revoked_key_ids: [], schema: 'pulse.release_authority.v1',
    ...overrides,
  };
  return {
    payload: authorityPayload,
    schema: 'pulse.release_authority_envelope.v1',
    signature: {
      algorithm: 'ed25519', key_id: rootKeys.keyID,
      value: sign(null, Buffer.from(canonicalReleaseJSON(authorityPayload)), rootKeys.privateKey).toString('base64'),
    },
  };
}

function catalogEnvelope(rootKeys, channelKeys, manifest = catalogPayload(channelKeys), authorityOverrides = {}) {
  return {
    authority: authorityEnvelope(rootKeys, channelKeys, authorityOverrides),
    payload: manifest,
    schema: 'pulse.release_catalog_envelope.v2',
    signature: {
      algorithm: 'ed25519', key_id: channelKeys.keyID,
      value: sign(null, Buffer.from(canonicalReleaseJSON(manifest)), channelKeys.privateKey).toString('base64'),
    },
  };
}

function artifactSetEnvelope(channelKeys, overrides = {}) {
  const payload = catalogPayload(channelKeys);
  delete payload.release.expires_at;
  delete payload.release.issued_at;
  payload.schema = 'pulse.personal_release_artifact_set_payload.v1';
  payload.snapshot_url = 'https://releases.zbs.gg/pulse/0.8.1/catalog/snapshot.json';
  payload.host_policy = {
    harnesses: loadPersonalReleaseHostPolicy(loadNativeUniversalMatrix()).map((harness) => structuredClone(harness)),
  };
  Object.assign(payload, overrides);
  return {
    payload,
    schema: 'pulse.personal_release_artifact_set.v1',
    signature: {
      algorithm: 'ed25519', key_id: channelKeys.keyID,
      value: sign(null, Buffer.from(canonicalReleaseJSON(payload)), channelKeys.privateKey).toString('base64'),
    },
  };
}

function snapshotEnvelope(rootKeys, channelKeys, artifactSet, overrides = {}) {
  const payload = {
    artifact_set: {
      sha256: createHash('sha256').update(`${canonicalReleaseJSON(artifactSet)}\n`).digest('hex'),
      url: 'https://releases.zbs.gg/pulse/0.8.1/epoch-7/catalog/artifact-set.json',
    },
    channel: {
      key_id: channelKeys.keyID,
      public_key_pem: channelKeys.publicKey,
      valid_from_epoch: 7,
      valid_through_epoch: 7,
    },
    expires_at: '2026-08-14T00:00:00.000Z',
    issued_at: '2026-07-15T00:00:00.000Z',
    package: '@zbs-gg/pulse',
    release_epoch: 7,
    revoked_key_ids: [],
    schema: 'pulse.release_snapshot.v1',
    version: '0.8.1',
    ...overrides,
  };
  return {
    payload,
    schema: 'pulse.release_snapshot_envelope.v1',
    signature: {
      algorithm: 'ed25519', key_id: rootKeys.keyID,
      value: sign(null, Buffer.from(canonicalReleaseJSON(payload)), rootKeys.privateKey).toString('base64'),
    },
  };
}

function expectCode(fn, code) {
  assert.throws(fn, (error) => error instanceof ReleaseManifestError && error.code === code);
}

test('canonical signed exact compatibility set verifies and returns an immutable digest', () => {
  const keys = keyFixture();
  const first = envelope(keys);
  const reordered = {
    signature: first.signature,
    schema: first.schema,
    payload: JSON.parse(JSON.stringify(first.payload)),
  };
  const result = verifyReleaseManifestEnvelope(reordered, verifyOptions(keys));
  assert.equal(result.schema, 'pulse.verified_release_manifest.v1');
  assert.equal(result.epoch, 7);
  assert.match(result.manifest_digest, /^[a-f0-9]{64}$/);
  assert.deepEqual(Object.keys(result.artifacts).sort(), [
    'daemon', 'embedder-runtime', 'model', 'plugin-runtime', 'presence-helper',
  ]);
});

test('delegated catalog selects one exact target and preserves optional capabilities', () => {
  const root = keyFixture();
  const channel = keyFixture();
  const signed = catalogEnvelope(root, channel);
  for (const targetID of DESKTOP_TARGET_IDS) {
    const [platform, architecture, libc = null] = targetID.split('-');
    const result = verifyReleaseManifestEnvelope(signed, verifyOptions(root, {
      architecture, libc, platform,
    }));
    assert.equal(result.schema, 'pulse.verified_release_manifest.v2');
    assert.equal(result.catalog_schema, 'pulse.personal_preview.release_catalog.v2');
    assert.equal(result.target_id, targetID);
    assert.equal(result.historical_only, false);
    assert.equal(result.authority.root_key_id, root.keyID);
    assert.equal(result.authority.channel_key_id, channel.keyID);
    assert.deepEqual(result.verification_profile, verificationProfile(platform));
    assert.equal(result.artifacts.model.format, 'tar.gz');
    assert.equal(result.artifacts.model.tree_digest, 'b'.repeat(64));
    assert.deepEqual(result.artifacts.model.model_policy.required_files, PORTABLE_MODEL_REQUIRED_FILES);
    assert.deepEqual(result.capabilities, platform === 'darwin' ? ['presence-helper'] : []);
    assert.deepEqual(Object.keys(result.artifacts).sort(), platform === 'darwin'
      ? ['daemon', 'embedder-runtime', 'model', 'plugin-runtime', 'presence-helper']
      : ['daemon', 'embedder-runtime', 'model', 'plugin-runtime']);
  }
});

test('v3 immutable artifact set is authorized by a fresh root snapshot for every target', () => {
  const root = keyFixture();
  const channel = keyFixture();
  const artifactSet = artifactSetEnvelope(channel);
  const snapshot = snapshotEnvelope(root, channel, artifactSet);
  for (const targetID of DESKTOP_TARGET_IDS) {
    const [platform, architecture, libc = null] = targetID.split('-');
    const verified = verifyPersonalReleaseArtifactSet(artifactSet, snapshot, verifyOptions(root, {
      architecture, libc, platform,
    }));
    assert.equal(verified.schema, 'pulse.verified_release_manifest.v3');
    assert.equal(verified.catalog_schema, 'pulse.personal_release_artifact_set.v1');
    assert.equal(verified.target_id, targetID);
    assert.equal(verified.epoch, 7);
    assert.equal(verified.snapshot_refresh_required, false);
    assert.match(verified.authority.snapshot_digest, /^[a-f0-9]{64}$/);
  }
});

test('v3 host policy rejects malformed vendor download identity with a stable failure', () => {
  const root = keyFixture();
  const channel = keyFixture();
  const artifactSet = artifactSetEnvelope(channel);
  artifactSet.payload.host_policy.harnesses[0].vendor_source = 'not-a-vendor-url';
  const snapshot = snapshotEnvelope(root, channel, artifactSet);
  expectCode(
    () => verifyPersonalReleaseArtifactSet(artifactSet, snapshot, verifyOptions(root)),
    'release_host_policy_invalid',
  );
});

test('v3 snapshot rejects tamper, revocation, expiry, downgrade, and cross-origin artifact set', () => {
  const root = keyFixture();
  const channel = keyFixture();
  const artifactSet = artifactSetEnvelope(channel);
  const options = verifyOptions(root);
  const tampered = snapshotEnvelope(root, channel, artifactSet);
  tampered.payload.artifact_set.sha256 = '0'.repeat(64);
  expectCode(() => verifyPersonalReleaseArtifactSet(artifactSet, tampered, options), 'release_snapshot_signature_invalid');
  expectCode(() => verifyPersonalReleaseArtifactSet(
    artifactSet,
    snapshotEnvelope(root, channel, artifactSet, { revoked_key_ids: [channel.keyID] }),
    options,
  ), 'release_key_revoked');
  expectCode(() => verifyPersonalReleaseArtifactSet(
    artifactSet,
    snapshotEnvelope(root, channel, artifactSet, { expires_at: '2026-07-16T00:00:00.000Z' }),
    options,
  ), 'release_snapshot_expired');
  const expired = verifyPersonalReleaseArtifactSet(
    artifactSet,
    snapshotEnvelope(root, channel, artifactSet, { expires_at: '2026-07-16T00:00:00.000Z' }),
    { ...options, allowExpiredSnapshot: true },
  );
  assert.equal(expired.snapshot_refresh_required, true);
  expectCode(() => verifyPersonalReleaseArtifactSet(
    artifactSet, snapshotEnvelope(root, channel, artifactSet), { ...options, minimumAcceptedEpoch: 8 },
  ), 'manifest_epoch_downgrade');
  expectCode(() => verifyPersonalReleaseArtifactSet(
    artifactSet,
    snapshotEnvelope(root, channel, artifactSet, {
      artifact_set: {
        sha256: createHash('sha256').update(`${canonicalReleaseJSON(artifactSet)}\n`).digest('hex'),
        url: 'https://evil.example/pulse/0.8.1/epoch-7/catalog/artifact-set.json',
      },
    }),
    options,
  ), 'release_snapshot_origin_invalid');
});

test('v2 catalog binds canonical trees and rejects mismatched platform verification profiles', () => {
  const root = keyFixture();
  const channel = keyFixture();

  const missingTree = catalogPayload(channel);
  delete missingTree.common_artifacts.model.tree_digest;
  expectCode(() => verifyReleaseManifestEnvelope(catalogEnvelope(root, channel, missingTree), verifyOptions(root)), 'artifact_fields_invalid');

  const legacyModel = catalogPayload(channel);
  legacyModel.common_artifacts.model.format = 'safetensors';
  legacyModel.common_artifacts.model.url = 'https://releases.zbs.gg/pulse/0.8.1/model.safetensors';
  expectCode(() => verifyReleaseManifestEnvelope(catalogEnvelope(root, channel, legacyModel), verifyOptions(root)), 'artifact_format_invalid');

  const wrongProfile = catalogPayload(channel);
  wrongProfile.targets['win32-x64'].verification_profile = verificationProfile('linux');
  expectCode(() => verifyReleaseManifestEnvelope(catalogEnvelope(root, channel, wrongProfile), verifyOptions(root, {
    architecture: 'x64', platform: 'win32',
  })), 'release_verification_profile_invalid');

  const wrongPublisher = catalogPayload(channel);
  wrongPublisher.targets['win32-x64'].verification_profile.publisher = 'Unknown Publisher';
  expectCode(() => verifyReleaseManifestEnvelope(catalogEnvelope(root, channel, wrongPublisher), verifyOptions(root, {
    architecture: 'x64', platform: 'win32',
  })), 'release_verification_profile_invalid');

  const missingTimestamp = catalogPayload(channel);
  missingTimestamp.targets['win32-x64'].verification_profile.timestamped = false;
  expectCode(() => verifyReleaseManifestEnvelope(catalogEnvelope(root, channel, missingTimestamp), verifyOptions(root, {
    architecture: 'x64', platform: 'win32',
  })), 'release_verification_profile_invalid');
});

test('fixture verification profile is explicit and cannot satisfy production verification', () => {
  const root = keyFixture();
  const channel = keyFixture();
  const fixtureCatalog = catalogPayload(channel);
  fixtureCatalog.targets['linux-x64-gnu'].verification_profile = {
    fixture_id: 'linux-x64-pr', kind: 'fixture', production: false,
  };
  for (const artifact of Object.values(fixtureCatalog.targets['linux-x64-gnu'].artifacts)) {
    artifact.signing = { ...portableSigning(), scheme: 'fixture' };
  }
  const signed = catalogEnvelope(root, channel, fixtureCatalog);
  const options = verifyOptions(root, { architecture: 'x64', libc: 'gnu', platform: 'linux' });
  expectCode(() => verifyReleaseManifestEnvelope(signed, options), 'release_fixture_verification_forbidden');
  const verified = verifyReleaseManifestEnvelope(signed, { ...options, allowFixtureVerification: true });
  assert.equal(verified.verification_profile.kind, 'fixture');

  const fixtureSmuggledIntoProduction = catalogPayload(channel);
  fixtureSmuggledIntoProduction.targets['linux-x64-gnu'].artifacts.daemon.signing = {
    ...portableSigning(), scheme: 'fixture',
  };
  expectCode(() => verifyReleaseManifestEnvelope(
    catalogEnvelope(root, channel, fixtureSmuggledIntoProduction), options,
  ), 'artifact_signing_invalid');
});

test('a production catalog missing any target and capability-artifact confusion fail closed', () => {
  const root = keyFixture();
  const channel = keyFixture();
  const missing = catalogPayload(channel);
  delete missing.targets['linux-x64-gnu'];
  expectCode(() => verifyReleaseManifestEnvelope(catalogEnvelope(root, channel, missing), verifyOptions(root, {
    architecture: 'x64', libc: 'gnu', platform: 'linux',
  })), 'release_target_catalog_incomplete');

  expectCode(() => verifyReleaseManifestEnvelope(catalogEnvelope(root, channel, missing), verifyOptions(root, {
    architecture: 'arm64', platform: 'darwin',
  })), 'release_target_catalog_incomplete');

  const confused = catalogPayload(channel);
  confused.targets['win32-x64'].capabilities = ['presence-helper'];
  expectCode(() => verifyReleaseManifestEnvelope(catalogEnvelope(root, channel, confused), verifyOptions(root, {
    architecture: 'x64', platform: 'win32',
  })), 'release_target_capability_invalid');
});

test('offline-root delegation enforces expiry, epoch bounds, and revocation', () => {
  const root = keyFixture();
  const channel = keyFixture();
  expectCode(() => verifyReleaseManifestEnvelope(catalogEnvelope(root, channel, undefined, {
    expires_at: '2026-07-16T00:00:00.000Z',
  }), verifyOptions(root, { now: new Date('2026-07-16T00:00:00.000Z') })), 'release_authority_expired');
  expectCode(() => verifyReleaseManifestEnvelope(catalogEnvelope(root, channel, undefined, {
    keys: [{ key_id: channel.keyID, public_key_pem: channel.publicKey, valid_from_epoch: 8, valid_through_epoch: 9 }],
  }), verifyOptions(root)), 'release_key_epoch_invalid');
  expectCode(() => verifyReleaseManifestEnvelope(catalogEnvelope(root, channel, undefined, {
    revoked_key_ids: [channel.keyID],
  }), verifyOptions(root)), 'release_key_revoked');
});

test('legacy v1 envelope is historical evidence and cannot claim universal readiness', () => {
  const keys = keyFixture();
  const result = verifyReleaseManifestEnvelope(envelope(keys), verifyOptions(keys));
  assert.equal(result.schema, 'pulse.verified_release_manifest.v1');
  assert.equal(result.historical_only, true);
  assert.equal(result.target_id, 'darwin-arm64');
});

test('authority-bearing unknown and missing fields fail closed at every signed layer', () => {
  const keys = keyFixture();
  expectCode(() => verifyReleaseManifestEnvelope(envelope(keys, payload(keys.keyID), { policy: 'relaxed' }), verifyOptions(keys)), 'envelope_fields_invalid');
  expectCode(() => verifyReleaseManifestEnvelope(envelope(keys, payload(keys.keyID, { mirror: 'https://evil.example' })), verifyOptions(keys)), 'manifest_fields_invalid');
  const missing = payload(keys.keyID);
  delete missing.artifacts.model.signing;
  expectCode(() => verifyReleaseManifestEnvelope(envelope(keys, missing), verifyOptions(keys)), 'artifact_fields_invalid');
});

test('wrong schema, architecture, digest, format, origin, or signing expectation is rejected', () => {
  const keys = keyFixture();
  const cases = [
    ['daemon', artifact('daemon', { version: '0.7.0' }), 'artifact_version_incompatible'],
    ['daemon', artifact('daemon', { platform: 'linux' }), 'artifact_platform_incompatible'],
    ['daemon', artifact('daemon', { architecture: 'x86_64' }), 'artifact_architecture_incompatible'],
    ['daemon', artifact('daemon', { sha256: 'not-a-digest' }), 'artifact_digest_invalid'],
    ['daemon', artifact('daemon', { format: 'tar.gz' }), 'artifact_format_invalid'],
    ['model', artifact('model', { model_policy: { custom_code: true, data_only: true } }), 'artifact_model_policy_invalid'],
    ['daemon', artifact('daemon', { origin: 'https://evil.example' }), 'artifact_origin_not_allowed'],
    ['daemon', artifact('daemon', { signing: { gatekeeper: true, identifier: 'gg.zbs.pulse.daemon', notarized: true, scheme: 'apple-developer-id', stapled: false, team_id: '44N4NZ86S5' } }), 'artifact_signing_invalid'],
    ['daemon', artifact('daemon', { signing: { gatekeeper: false, identifier: null, notarized: false, scheme: 'release-manifest', stapled: false, team_id: null } }), 'artifact_signing_invalid'],
  ];
  for (const [name, changedArtifact, code] of cases) {
    const manifest = payload(keys.keyID);
    manifest.artifacts[name] = changedArtifact;
    expectCode(() => verifyReleaseManifestEnvelope(envelope(keys, manifest), verifyOptions(keys)), code);
  }
  const wrongSchema = payload(keys.keyID);
  wrongSchema.schema = 'pulse.personal_preview.release_manifest.v0';
  expectCode(() => verifyReleaseManifestEnvelope(envelope(keys, wrongSchema), verifyOptions(keys)), 'manifest_schema_invalid');
  expectCode(() => verifyReleaseManifestEnvelope(envelope(keys), verifyOptions(keys, {
    packageVersion: '0.7.0',
  })), 'release_package_version_incompatible');
  expectCode(() => verifyReleaseManifestEnvelope(envelope(keys), verifyOptions(keys, {
    osVersion: '12.6.9',
  })), 'artifact_minimum_os_incompatible');
});

test('unknown, invalid, expired, and epoch-ineligible signing keys fail closed', () => {
  const keys = keyFixture();
  const other = keyFixture();
  expectCode(() => verifyReleaseManifestEnvelope(envelope(keys), verifyOptions(keys, { trustedKeys: [] })), 'release_key_unknown');
  expectCode(() => verifyReleaseManifestEnvelope(envelope(keys), verifyOptions(keys, {
    trustedKeys: [{ key_id: keys.keyID, public_key_pem: other.publicKey, valid_from_epoch: 1, valid_through_epoch: 20 }],
  })), 'release_key_id_invalid');
  const bad = envelope(keys);
  bad.signature.value = Buffer.alloc(64).toString('base64');
  expectCode(() => verifyReleaseManifestEnvelope(bad, verifyOptions(keys)), 'release_signature_invalid');
  expectCode(() => verifyReleaseManifestEnvelope(envelope(keys), verifyOptions(keys, {
    now: new Date('2026-08-02T00:00:00.000Z'),
  })), 'manifest_expired');
  expectCode(() => verifyReleaseManifestEnvelope(envelope(keys), verifyOptions(keys, {
    trustedKeys: [{ key_id: keys.keyID, public_key_pem: keys.publicKey, valid_from_epoch: 8, valid_through_epoch: 20 }],
  })), 'release_key_epoch_invalid');
});

test('older epoch needs an exact fresh-presence downgrade authorization callback', () => {
  const keys = keyFixture();
  const signed = envelope(keys);
  expectCode(() => verifyReleaseManifestEnvelope(signed, verifyOptions(keys, { minimumAcceptedEpoch: 8 })), 'manifest_epoch_downgrade');
  expectCode(() => verifyReleaseManifestEnvelope(signed, verifyOptions(keys, {
    downgradeAuthorization: { approved: true }, minimumAcceptedEpoch: 8,
  })), 'manifest_epoch_downgrade');
  const result = verifyReleaseManifestEnvelope(signed, verifyOptions(keys, {
    downgradeAuthorization: { schema: 'pulse.release_downgrade_authorization.v1' },
    minimumAcceptedEpoch: 8,
    verifyDowngradeAuthorization: ({ authorization, manifestDigest, manifestEpoch, minimumAcceptedEpoch }) =>
      authorization.schema === 'pulse.release_downgrade_authorization.v1' &&
      manifestDigest.length === 64 && manifestEpoch === 7 && minimumAcceptedEpoch === 8,
  }));
  assert.equal(result.downgrade_authorized, true);
});

test('minimum accepted epoch advances atomically and never rolls back', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-release-epoch-'));
  const path = join(root, 'minimum-epoch.json');
  try {
    assert.equal(readMinimumReleaseEpoch(path), 0);
    assert.equal(advanceMinimumReleaseEpoch(path, 7), 7);
    assert.equal(readMinimumReleaseEpoch(path), 7);
    expectCode(() => advanceMinimumReleaseEpoch(path, 6), 'minimum_epoch_rollback');
    assert.equal(readMinimumReleaseEpoch(path), 7);
    assert.equal(readFileSync(path, 'utf8'), '{"epoch":7,"schema":"pulse.minimum_release_epoch.v1"}\n');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('minimum epoch lock makes cross-process concurrent and stale lower writers fail closed', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-release-epoch-race-'));
  const path = join(root, 'minimum-epoch.json');
  const lock = `${path}.lock`;
  let holder;
  try {
    assert.equal(advanceMinimumReleaseEpoch(path, 8), 8);
    holder = spawn(process.execPath, ['--input-type=module', '-e', `
      import { mkdirSync, rmdirSync } from 'node:fs';
      mkdirSync(${JSON.stringify(lock)}, { mode: 0o700 });
      process.stdout.write('locked\\n');
      setTimeout(() => rmdirSync(${JSON.stringify(lock)}), 250);
    `], { stdio: ['ignore', 'pipe', 'inherit'] });
    await once(holder.stdout, 'data');
    expectCode(() => advanceMinimumReleaseEpoch(path, 7), 'minimum_epoch_locked');
    assert.equal(readMinimumReleaseEpoch(path), 8);
    const [code] = await once(holder, 'exit');
    assert.equal(code, 0);
    holder = null;
    expectCode(() => advanceMinimumReleaseEpoch(path, 7), 'minimum_epoch_rollback');
    assert.equal(readMinimumReleaseEpoch(path), 8);
  } finally {
    holder?.kill('SIGKILL');
    rmSync(root, { recursive: true, force: true });
  }
});

test('Stage 1 rejects Node 18 without mutation', () => {
  expectCode(() => assertSupportedNodeVersion('18.20.4'), 'node_unsupported');
  assert.equal(assertSupportedNodeVersion('20.0.0').major, 20);
});

test('audited package pins Node 20, the release verifier, schema, and root key', () => {
  const packageJSON = JSON.parse(readFileSync(new URL('../package.json', import.meta.url), 'utf8'));
  assert.equal(packageJSON.engines.node, '>=20');
  const packageLock = JSON.parse(readFileSync(new URL('../package-lock.json', import.meta.url), 'utf8'));
  assert.equal(packageLock.packages[''].engines.node, '>=20');
  assert.ok(packageJSON.files.includes('src/release-manifest.js'));
  for (const path of [
    'release/README.md',
    'release/personal-preview-manifest.json',
    'release/personal-release-snapshot.json',
    'release/personal-preview-manifest.schema.json',
    'release/pulse-release-root.pem',
  ]) {
    assert.ok(packageJSON.files.includes(path), path);
  }
  const schema = JSON.parse(readFileSync(new URL('../release/personal-preview-manifest.schema.json', import.meta.url), 'utf8'));
  assert.equal(schema.additionalProperties, false);
  assert.equal(schema.properties.schema.const, 'pulse.personal_preview.release_catalog.v2');
  assert.equal(schema.properties.common_artifacts.additionalProperties, false);
  assert.deepEqual(schema.properties.targets.propertyNames.enum, DESKTOP_TARGET_IDS);
  assert.deepEqual(schema.properties.targets.required, DESKTOP_TARGET_IDS);
  assert.equal(schema.properties.targets.minProperties, DESKTOP_TARGET_IDS.length);
  assert.equal(schema.properties.targets.maxProperties, DESKTOP_TARGET_IDS.length);
  assert.deepEqual(schema.$defs.artifact.required.includes('tree_digest'), true);
  assert.deepEqual(schema.$defs.artifact.properties.format.enum, ['tar.gz']);
  assert.equal(schema.$defs.target.required.includes('verification_profile'), true);
  const keys = pinnedReleaseKeyring();
  assert.equal(keys.length, 1);
  assert.match(keys[0].key_id, /^[a-f0-9]{64}$/);
  const packager = readFileSync(new URL('../scripts/prepare-preview-vendor.mjs', import.meta.url), 'utf8');
  assert.match(packager, /refusing production packaging: canonical signed Personal release manifest is missing/);
  assert.match(packager, /targetIDs\.length < 1/);
  assert.match(packager, /DESKTOP_TARGET_IDS\.includes/);
  assert.match(packager, /release catalog digest mismatch/);
  const builder = readFileSync(new URL('../scripts/build-presence-helper.mjs', import.meta.url), 'utf8');
  assert.match(builder, /PULSE_PRODUCTION_RELEASE/);
  assert.match(builder, /notarytool[^\n]+submit/);
  assert.match(builder, /stapler[^\n]+staple/);
  assert.match(builder, /stapler[^\n]+validate/);
  const cli = readFileSync(new URL('./cli.js', import.meta.url), 'utf8');
  assert.match(cli, /needs 20\+/);
});
