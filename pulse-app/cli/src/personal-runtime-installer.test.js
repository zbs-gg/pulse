import assert from 'node:assert/strict';
import { createHash, generateKeyPairSync, sign } from 'node:crypto';
import {
  chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import test from 'node:test';

import { canonicalReleaseJSON, releaseKeyID } from './release-manifest.js';
import { readActivatedArtifactSet } from './artifact-installer.js';
import { acquireInstallLock } from './install-journal.js';
import {
  commitPersonalRuntimeRelease,
  inspectPersonalRelease,
  inspectPersonalRuntime,
  provisionPersonalRuntime,
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

function safetensors() {
  const header = Buffer.from(JSON.stringify({ embedding: { dtype: 'F32', shape: [1], data_offsets: [0, 4] } }));
  const prefix = Buffer.alloc(8);
  prefix.writeBigUInt64LE(BigInt(header.length));
  return Buffer.concat([prefix, header, Buffer.alloc(4)]);
}

function fixture() {
  const pair = generateKeyPairSync('ed25519');
  const publicKey = pair.publicKey.export({ type: 'spki', format: 'pem' });
  const keyID = releaseKeyID(publicKey);
  const files = {
    daemon: [['bin/pulse', Buffer.from('#!/bin/sh\nexit 0\n'), 0o700]],
    'embedder-runtime': [
      ['runtime/bin/python3.12', Buffer.from('#!/bin/sh\nexit 0\n'), 0o700],
      ['helper.py', Buffer.from('helper\n'), 0o600],
      ['support/config.json', Buffer.from('{}\n'), 0o600],
      ['support/tokenizer.json', Buffer.from('{}\n'), 0o600],
    ],
    model: [['model.safetensors', safetensors(), 0o600]],
    'plugin-runtime': [['runtime/index.js', Buffer.from('export {};\n'), 0o600]],
    'presence-helper': [['bin/gg.zbs.pulse.presence-helper', Buffer.from('#!/bin/sh\nexit 0\n'), 0o700]],
  };
  const artifacts = {};
  const carriers = new Map();
  for (const kind of Object.keys(files)) {
    const executable = ['daemon', 'embedder-runtime', 'presence-helper'].includes(kind);
    const format = executable ? 'dmg' : kind === 'model' ? 'safetensors' : 'tar.gz';
    const carrier = kind === 'model'
      ? files[kind][0][1]
      : Buffer.from(`carrier:${kind}`);
    const url = `https://releases.zbs.gg/pulse/0.7.0/${kind}.${format}`;
    carriers.set(url, carrier);
    artifacts[kind] = {
      architecture: 'arm64', bytes: carrier.length, epoch: 7, executable, format,
      id: `pulse-${kind}`, kind, minimum_os: '13.0',
      model_policy: kind === 'model' ? { custom_code: false, data_only: true } : null,
      origin: 'https://releases.zbs.gg', platform: 'darwin', sha256: digest(carrier),
      signing: executable ? {
        gatekeeper: true, identifier: `gg.zbs.pulse.${kind}`, notarized: true,
        scheme: 'apple-developer-id', stapled: true, team_id: '44N4NZ86S5',
      } : {
        gatekeeper: false, identifier: null, notarized: false,
        scheme: 'release-manifest', stapled: false, team_id: null,
      },
      url, version: '0.7.0',
    };
  }
  const payload = {
    allowed_origins: ['https://releases.zbs.gg'], artifacts,
    release: {
      channel: 'preview', epoch: 7, expires_at: '2026-08-01T00:00:00.000Z',
      issued_at: '2026-07-15T00:00:00.000Z', key_id: keyID,
      package: '@zbs-gg/pulse', version: '0.7.0',
    },
    schema: 'pulse.personal_preview.release_manifest.v1',
  };
  const envelope = {
    payload, schema: 'pulse.release_envelope.v1',
    signature: {
      algorithm: 'ed25519', key_id: keyID,
      value: sign(null, Buffer.from(canonicalReleaseJSON(payload)), pair.privateKey).toString('base64'),
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
  return { carriers, envelope, files, materializers, publicKey, keyID };
}

test('empty Personal install downloads the signed compatibility set and publishes one atomic activation', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-personal-runtime.'));
  const manifestPath = join(root, 'manifest.json');
  const dataDir = join(root, 'data');
  const value = fixture();
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
      trustedKeys: [{
        key_id: value.keyID, public_key_pem: value.publicKey,
        valid_from_epoch: 1, valid_through_epoch: 20,
      }],
    });
    assert.equal(requests.length, 5);
    assert.equal(installed.release.manifest_digest.length, 64);
    assert.deepEqual(Object.keys(installed.activationSet.activations).sort(), Object.keys(value.files).sort());
    assert.equal(JSON.parse(readFileSync(join(dataDir, 'runtime', 'install-journal.json'), 'utf8')).phase, 'activated');
		assert.equal(existsSync(join(dataDir, 'runtime', 'minimum-release-epoch.json')), false,
			'artifact provisioning alone must not commit the anti-rollback floor');
		assert.throws(
			() => commitPersonalRuntimeRelease(installed.release, { dataDir: 'relative-data' }),
			/personal_runtime_configuration_invalid/,
		);
		assert.equal(commitPersonalRuntimeRelease(installed.release, { dataDir }), 7);
    assert.equal(JSON.parse(readFileSync(join(dataDir, 'runtime', 'minimum-release-epoch.json'), 'utf8')).epoch, 7);
    assert.equal(readActivatedArtifactSet(installed.release, { installRoot: join(dataDir, 'artifacts') }).record.epoch, 7);

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
