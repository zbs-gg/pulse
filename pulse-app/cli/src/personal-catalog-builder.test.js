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
import { DESKTOP_TARGET_IDS, desktopTargetDefinition } from './desktop-target.js';
import {
  releaseKeyID, verifyReleaseManifestEnvelope,
} from './release-manifest.js';

const PACKAGE_VERSION = JSON.parse(readFileSync(new URL('../package.json', import.meta.url), 'utf8')).version;
const EPOCH = 41;

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
  const receipt = JSON.parse(readFileSync(join(current.options.outputRoot, 'catalog-build-receipt.json'), 'utf8'));

  assert.deepEqual(Object.keys(envelope.payload.targets).sort(), DESKTOP_TARGET_IDS);
  assert.equal(receipt.schema, 'pulse.personal_release_catalog_build.v2');
  assert.equal(receipt.production_ready, false, 'injected test authority can never claim a production catalog');
  assert.equal(receipt.target_count, 6);
  assert.equal(receipt.artifact_count, 14);
  assert.deepEqual(receipt.target_ids, DESKTOP_TARGET_IDS);
  assert.doesNotMatch(readFileSync(result.manifestPath, 'utf8'), /\.dmg(?:"|\b)/);

  const manifestDigests = [];
  for (const targetID of DESKTOP_TARGET_IDS) {
    const target = desktopTargetDefinition(targetID);
    const release = verifyReleaseManifestEnvelope(envelope, {
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
    assert.deepEqual(Object.keys(release.artifacts).sort(), [
      'daemon', 'embedder-runtime', 'model', 'plugin-runtime',
    ]);
    assert.equal(existsSync(join(
      current.options.outputRoot, 'assets', 'pulse', PACKAGE_VERSION, targetID, 'daemon.tar.gz',
    )), true);
    manifestDigests.push(release.manifest_digest);
  }
  assert.equal(new Set(manifestDigests).size, 1);
  assert.equal(manifestDigests[0], receipt.manifest_digest);
});

test('personal catalog builder blocks a missing target before creating output', (t) => {
  const current = fixture(t);
  const targetRoots = { ...current.options.targetRoots };
  delete targetRoots['win32-arm64'];
  assert.throws(
    () => buildPersonalCatalog({ ...current.options, targetRoots }),
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
