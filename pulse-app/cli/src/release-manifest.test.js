import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import {
  generateKeyPairSync, sign,
} from 'node:crypto';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { once } from 'node:events';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  ReleaseManifestError,
  advanceMinimumReleaseEpoch,
  assertSupportedNodeVersion,
  canonicalReleaseJSON,
  pinnedReleaseKeyring,
  readMinimumReleaseEpoch,
  releaseKeyID,
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
    url: `https://releases.zbs.gg/pulse/0.8.0/${kind}.${format}`,
    version: '0.8.0',
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
      version: '0.8.0',
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
    packageVersion: '0.8.0',
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
  assert.ok(packageJSON.files.includes('release'));
  const schema = JSON.parse(readFileSync(new URL('../release/personal-preview-manifest.schema.json', import.meta.url), 'utf8'));
  assert.equal(schema.additionalProperties, false);
  assert.equal(schema.properties.artifacts.additionalProperties, false);
  const keys = pinnedReleaseKeyring();
  assert.equal(keys.length, 1);
  assert.match(keys[0].key_id, /^[a-f0-9]{64}$/);
  const packager = readFileSync(new URL('../scripts/prepare-preview-vendor.mjs', import.meta.url), 'utf8');
  assert.match(packager, /refusing production packaging: canonical signed Personal release manifest is missing/);
  assert.match(packager, /presence helper does not match the signed release manifest/);
  const builder = readFileSync(new URL('../scripts/build-presence-helper.mjs', import.meta.url), 'utf8');
  assert.match(builder, /PULSE_PRODUCTION_RELEASE/);
  assert.match(builder, /notarytool[^\n]+submit/);
  assert.match(builder, /stapler[^\n]+staple/);
  assert.match(builder, /stapler[^\n]+validate/);
  const cli = readFileSync(new URL('./cli.js', import.meta.url), 'utf8');
  assert.match(cli, /needs 20\+/);
});
