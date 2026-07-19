#!/usr/bin/env node

import { createHash, generateKeyPairSync, sign } from 'node:crypto';
import {
  chmodSync, existsSync, mkdirSync, readFileSync, writeFileSync,
} from 'node:fs';
import { dirname, isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  activateArtifactVersion, commitArtifactGeneration, readCommittedArtifactSet,
} from '../src/artifact-installer.js';
import {
  canonicalReleaseJSON, releaseKeyID, verifyReleaseManifestEnvelope,
} from '../src/release-manifest.js';
import { buildPortableEmbedderRuntime } from './build-portable-embedder-runtime.mjs';
import {
  buildDaemonTarget, packNormalizedArtifact, prepareNormalizedArtifact, releaseTargetDefinition, releaseTargetIDs,
} from './release-builder-core.mjs';

const scriptRoot = dirname(fileURLToPath(import.meta.url));
const cliRoot = resolve(scriptRoot, '..');
const appRoot = resolve(cliRoot, '..');
const embedderSourceRoot = join(cliRoot, 'runtime', 'embedder-portable');
const packageVersion = JSON.parse(readFileSync(join(cliRoot, 'package.json'), 'utf8')).version;
const ORIGIN = 'https://fixtures.invalid';
const MODEL_REVISION = '5617a9f61b028005a4858fdac845db406aefb181';
const MODEL_FILES = Object.freeze([
  'LICENSES/BGE-M3-MIT.txt', 'PROVENANCE.json', 'model_int8.onnx', 'pulse-model-contract.json',
  'support/config.json', 'support/special_tokens_map.json', 'support/tokenizer.json', 'support/tokenizer_config.json',
]);

function fail(code) {
  const error = new Error(code);
  error.code = code;
  throw error;
}

function writePrivate(path, bytes, mode = 0o600) {
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  writeFileSync(path, bytes, { flag: 'wx', mode });
  chmodSync(path, mode);
}

function fixtureSigning() {
  return { gatekeeper: false, identifier: null, notarized: false, scheme: 'fixture', stapled: false, team_id: null };
}

function manifestSigning() {
  return { gatekeeper: false, identifier: null, notarized: false, scheme: 'release-manifest', stapled: false, team_id: null };
}

function modelPolicy() {
  return {
    custom_code: false, data_only: true, engine: 'transformers-js-onnx', model: 'BAAI/bge-m3',
    required_files: [...MODEL_FILES], revision: MODEL_REVISION,
  };
}

function keyPair() {
  const pair = generateKeyPairSync('ed25519');
  const publicKey = pair.publicKey.export({ type: 'spki', format: 'pem' });
  return { keyID: releaseKeyID(publicKey), privateKey: pair.privateKey, publicKey };
}

function signature(payload, key) {
  return {
    algorithm: 'ed25519', key_id: key.keyID,
    value: sign(null, Buffer.from(canonicalReleaseJSON(payload)), key.privateKey).toString('base64'),
  };
}

function writeModelFixture(root) {
  const values = {
    'LICENSES/BGE-M3-MIT.txt': 'MIT fixture only\n',
    'PROVENANCE.json': `${canonicalReleaseJSON({ fixture: true, model: 'BAAI/bge-m3', revision: MODEL_REVISION })}\n`,
    'model_int8.onnx': 'PULSE_SYNTHETIC_ONNX_FIXTURE_ONLY\n',
    'pulse-model-contract.json': `${canonicalReleaseJSON({
      dimensions: 1024, engine: 'transformers-js-onnx', model_file: 'model_int8.onnx',
      normalized: true, pooling: 'cls', schema: 'pulse.portable_embedder.model.v1',
    })}\n`,
    'support/config.json': '{}\n',
    'support/special_tokens_map.json': '{}\n',
    'support/tokenizer.json': '{}\n',
    'support/tokenizer_config.json': '{}\n',
  };
  for (const path of MODEL_FILES) writePrivate(join(root, path), values[path]);
}

function currentTargetID() {
  const architecture = process.arch === 'arm64' ? 'arm64' : process.arch === 'x64' ? 'x64' : null;
  if (!architecture) return null;
  if (process.platform === 'darwin') return `darwin-${architecture}`;
  if (process.platform === 'win32') return `win32-${architecture}`;
  if (process.platform === 'linux' && process.report?.getReport()?.header?.glibcVersionRuntime) {
    return `linux-${architecture}-gnu`;
  }
  return null;
}

function artifactDescriptor(kind, target, carrier, treeDigest, { common = false, epoch, version }) {
  const executable = ['daemon', 'embedder-runtime', 'presence-helper'].includes(kind);
  return {
    architecture: common ? 'all' : target.architecture,
    bytes: carrier.bytes,
    epoch,
    executable,
    format: 'tar.gz',
    id: `pulse-fixture-${target.target_id}-${kind}`,
    kind,
    minimum_os: common || target.platform !== 'darwin' ? '0.0' : '13.0',
    model_policy: kind === 'model' ? modelPolicy() : null,
    origin: ORIGIN,
    platform: common ? 'all' : target.platform,
    sha256: carrier.sha256,
    signing: common ? manifestSigning() : fixtureSigning(),
    tree_digest: treeDigest,
    url: `${ORIGIN}/pulse/${version}/${target.target_id}/${kind}.tar.gz`,
    version,
  };
}

async function packFixture(root, outputRoot, kind) {
  const prepared = prepareNormalizedArtifact(root);
  const path = join(outputRoot, 'carriers', `${kind}.tar.gz`);
  const carrier = await packNormalizedArtifact(root, path);
  return { carrier, path, tree_digest: prepared.tree_digest };
}

export async function buildAndInstallTargetFixture({
  buildDaemon = buildDaemonTarget,
  epoch = 1,
  nativeTargetID = currentTargetID(),
  outputRoot,
  targetID,
  version = packageVersion,
} = {}) {
  if (!releaseTargetIDs().includes(targetID) || typeof outputRoot !== 'string' || !isAbsolute(outputRoot) ||
      resolve(outputRoot) !== outputRoot || !Number.isSafeInteger(epoch) || epoch < 1) fail('target_fixture_configuration_invalid');
  const target = releaseTargetDefinition(targetID);
  if (existsSync(outputRoot)) fail('target_fixture_output_exists');
  mkdirSync(dirname(outputRoot), { recursive: true, mode: 0o700 });
  mkdirSync(outputRoot, { mode: 0o700 });
  const roots = Object.fromEntries(['daemon', 'embedder-runtime', 'model', 'plugin-runtime']
    .map((kind) => [kind, join(outputRoot, 'materialized', kind)]));
  if (target.platform === 'darwin') roots['presence-helper'] = join(outputRoot, 'materialized', 'presence-helper');
  for (const root of Object.values(roots)) mkdirSync(root, { recursive: true, mode: 0o700 });

  buildDaemon({ appRoot, outputRoot: roots.daemon, targetID });
  buildPortableEmbedderRuntime({
    fixture: true, outputRoot: roots['embedder-runtime'], platform: target.platform, sourceRoot: embedderSourceRoot,
  });
  writeModelFixture(roots.model);
  writePrivate(join(roots['plugin-runtime'], 'runtime', 'index.js'), 'export const fixture = true;\n');
  if (roots['presence-helper']) {
    writePrivate(join(roots['presence-helper'], 'bin', 'pulse-presence-helper'), '#!/bin/sh\nexit 0\n', 0o700);
  }

  const packed = {};
  for (const [kind, root] of Object.entries(roots)) packed[kind] = await packFixture(root, outputRoot, kind);
  const descriptors = Object.fromEntries(Object.entries(packed).map(([kind, value]) => [kind, artifactDescriptor(
    kind, target, value.carrier, value.tree_digest, { common: ['model', 'plugin-runtime'].includes(kind), epoch, version },
  )]));
  const rootKey = keyPair();
  const channelKey = keyPair();
  const issued = new Date('2026-06-30T00:00:00.000Z');
  const payload = {
    allowed_origins: [ORIGIN],
    common_artifacts: { model: descriptors.model, 'plugin-runtime': descriptors['plugin-runtime'] },
    release: {
      channel: 'preview', epoch, expires_at: '2026-07-02T00:00:00.000Z', issued_at: issued.toISOString(),
      key_id: channelKey.keyID, package: '@zbs-gg/pulse', version,
    },
    schema: 'pulse.personal_preview.release_catalog.v2',
    targets: {
      [targetID]: {
        architecture: target.architecture,
        artifacts: {
          daemon: descriptors.daemon,
          'embedder-runtime': descriptors['embedder-runtime'],
          ...(descriptors['presence-helper'] ? { 'presence-helper': descriptors['presence-helper'] } : {}),
        },
        capabilities: descriptors['presence-helper'] ? ['presence-helper'] : [],
        libc: target.libc,
        platform: target.platform,
        verification_profile: { fixture_id: `pr-${targetID}`, kind: 'fixture', production: false },
      },
    },
  };
  const authorityPayload = {
    channel: 'preview', epoch, expires_at: '2026-07-03T00:00:00.000Z', issued_at: issued.toISOString(),
    keys: [{
      key_id: channelKey.keyID, public_key_pem: channelKey.publicKey,
      valid_from_epoch: epoch, valid_through_epoch: epoch,
    }],
    revoked_key_ids: [], schema: 'pulse.release_authority.v1',
  };
  const envelope = {
    authority: {
      payload: authorityPayload, schema: 'pulse.release_authority_envelope.v1', signature: signature(authorityPayload, rootKey),
    },
    payload,
    schema: 'pulse.release_catalog_envelope.v2',
    signature: signature(payload, channelKey),
  };
  const release = verifyReleaseManifestEnvelope(envelope, {
    allowFixtureVerification: true,
    architecture: target.architecture,
    libc: target.libc,
    now: new Date('2026-07-01T00:00:00.000Z'),
    osVersion: target.platform === 'darwin' ? '14.0' : '0.0',
    packageVersion: version,
    platform: target.platform,
    trustedKeys: [{
      key_id: rootKey.keyID, public_key_pem: rootKey.publicKey,
      valid_from_epoch: epoch, valid_through_epoch: epoch,
    }],
  });
  const installRoot = join(outputRoot, 'installed');
  for (const [kind, artifact] of Object.entries(release.artifacts)) {
    await activateArtifactVersion(artifact, packed[kind].path, { installRoot, publishDerivedPointers: false });
  }
  commitArtifactGeneration(release, { installRoot });
  const committed = readCommittedArtifactSet({ installRoot });
  const receipt = {
    artifact_count: Object.keys(committed.activations).length,
    fixture_id: release.verification_profile.fixture_id,
    manifest_digest: release.manifest_digest,
    native_runner_match: nativeTargetID === targetID,
    production_ready: false,
    schema: 'pulse.target_release_fixture_install.v1',
    support_proven: false,
    target_id: targetID,
  };
  writeFileSync(join(outputRoot, 'fixture-install-receipt.json'), `${canonicalReleaseJSON(receipt)}\n`, { mode: 0o600 });
  return Object.freeze({ receipt: Object.freeze(receipt), release });
}

function parseCLI(argv) {
  const values = {};
  for (let index = 0; index < argv.length; index += 2) {
    if (!argv[index]?.startsWith('--') || argv[index + 1] === undefined) fail('target_fixture_arguments_invalid');
    values[argv[index].slice(2)] = argv[index + 1];
  }
  if (!values.target || !values.output) fail('target_fixture_arguments_invalid');
  return { outputRoot: resolve(values.output), targetID: values.target };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const result = await buildAndInstallTargetFixture(parseCLI(process.argv.slice(2)));
  process.stdout.write(`${canonicalReleaseJSON(result.receipt)}\n`);
}
