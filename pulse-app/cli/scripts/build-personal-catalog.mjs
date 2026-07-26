#!/usr/bin/env node

import {
  createHash, createPrivateKey, createPublicKey, sign,
} from 'node:crypto';
import {
  chmodSync, copyFileSync, lstatSync, mkdirSync, readFileSync, rmSync, writeFileSync,
} from 'node:fs';
import { basename, dirname, isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  canonicalReleaseJSON, pinnedReleaseKeyring, releaseKeyID, verifyReleaseManifestEnvelope,
} from '../src/release-manifest.js';

const scriptPath = fileURLToPath(import.meta.url);
const scriptRoot = dirname(scriptPath);
const cliRoot = resolve(scriptRoot, '..');
const PACKAGE_VERSION = JSON.parse(readFileSync(join(cliRoot, 'package.json'), 'utf8')).version;
const MODEL_REVISION = '5617a9f61b028005a4858fdac845db406aefb181';
const MODEL_FILES = Object.freeze([
  'LICENSES/BGE-M3-MIT.txt',
  'PROVENANCE.json',
  'model_int8.onnx',
  'pulse-model-contract.json',
  'support/config.json',
  'support/special_tokens_map.json',
  'support/tokenizer.json',
  'support/tokenizer_config.json',
]);
const IDENTIFIERS = Object.freeze({
  daemon: 'gg.zbs.pulse.daemon',
  'embedder-runtime': 'gg.zbs.pulse.embedder-runtime',
});
const SHA256 = /^[a-f0-9]{64}$/;

function fail(code, detail = '') {
  throw new Error(detail ? `${code}: ${detail}` : code);
}

function parseCLI(argv) {
  const values = {};
  for (let index = 0; index < argv.length; index += 2) {
    if (!argv[index]?.startsWith('--') || argv[index + 1] === undefined) fail('release_catalog_arguments_invalid');
    const name = argv[index].slice(2);
    if (Object.hasOwn(values, name)) fail('release_catalog_arguments_invalid');
    values[name] = argv[index + 1];
  }
  const epoch = Number(values.epoch);
  const paths = {
    channelKey: values['channel-key'] ? resolve(values['channel-key']) : null,
    modelRoot: values.model ? resolve(values.model) : null,
    outputRoot: values.output ? resolve(values.output) : null,
    pluginRoot: values.plugin ? resolve(values.plugin) : null,
    rootKey: values['root-key'] ? resolve(values['root-key']) : null,
    targetRoot: values.target ? resolve(values.target) : null,
  };
  if (Object.values(paths).some((value) => !value || !isAbsolute(value)) ||
      !Number.isSafeInteger(epoch) || epoch < 1 || typeof values.origin !== 'string') {
    fail('release_catalog_arguments_invalid');
  }
  return Object.freeze({ ...paths, epoch, origin: values.origin });
}

function privateKey(path, label) {
  const stat = lstatSync(path);
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size < 1 || stat.size > 16 * 1024 || (stat.mode & 0o077) !== 0) {
    fail('release_catalog_key_invalid', label);
  }
  const key = createPrivateKey(readFileSync(path));
  if (key.asymmetricKeyType !== 'ed25519') fail('release_catalog_key_invalid', label);
  const publicKey = createPublicKey(key).export({ type: 'spki', format: 'pem' });
  return Object.freeze({ key, keyID: releaseKeyID(publicKey), publicKey });
}

function canonicalFile(path, schema) {
  const stat = lstatSync(path);
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size < 1 || stat.size > 1024 * 1024) {
    fail('release_catalog_fragment_invalid', basename(path));
  }
  const bytes = readFileSync(path, 'utf8');
  let value;
  try { value = JSON.parse(bytes); } catch { fail('release_catalog_fragment_invalid', basename(path)); }
  if (bytes !== `${JSON.stringify(value)}\n` || value.schema !== schema) {
    fail('release_catalog_fragment_invalid', basename(path));
  }
  return value;
}

function digestFile(path) {
  const stat = lstatSync(path);
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size < 1) fail('release_catalog_carrier_invalid');
  return {
    bytes: stat.size,
    sha256: createHash('sha256').update(readFileSync(path)).digest('hex'),
  };
}

function copyCarrier(root, filename, expected, destination) {
  const source = join(root, filename);
  const actual = digestFile(source);
  if (actual.bytes !== expected.bytes || actual.sha256 !== expected.sha256) fail('release_catalog_carrier_mismatch', filename);
  mkdirSync(dirname(destination), { recursive: true, mode: 0o700 });
  copyFileSync(source, destination);
  chmodSync(destination, 0o600);
  const copied = digestFile(destination);
  if (copied.bytes !== expected.bytes || copied.sha256 !== expected.sha256) fail('release_catalog_carrier_mismatch', filename);
}

function manifestSignature(payload, authority) {
  return Object.freeze({
    algorithm: 'ed25519',
    key_id: authority.keyID,
    value: sign(null, Buffer.from(canonicalReleaseJSON(payload)), authority.key).toString('base64'),
  });
}

function commonSigning() {
  return Object.freeze({
    gatekeeper: false,
    identifier: null,
    notarized: false,
    scheme: 'release-manifest',
    stapled: false,
    team_id: null,
  });
}

function appleSigning(kind, teamID) {
  return Object.freeze({
    gatekeeper: true,
    identifier: IDENTIFIERS[kind],
    notarized: true,
    scheme: 'apple-developer-id',
    stapled: false,
    team_id: teamID,
  });
}

function descriptor({
  artifact, architecture, epoch, id, kind, minimumOS, origin, platform, signing, url,
}) {
  return Object.freeze({
    architecture,
    bytes: artifact.bytes,
    epoch,
    executable: ['daemon', 'embedder-runtime', 'presence-helper'].includes(kind),
    format: artifact.format,
    id,
    kind,
    minimum_os: minimumOS,
    model_policy: kind === 'model' ? {
      custom_code: false,
      data_only: true,
      engine: 'transformers-js-onnx',
      model: 'BAAI/bge-m3',
      required_files: [...MODEL_FILES],
      revision: MODEL_REVISION,
    } : null,
    origin,
    platform,
    sha256: artifact.sha256,
    signing,
    tree_digest: artifact.tree_digest,
    url,
    version: PACKAGE_VERSION,
  });
}

export function buildPersonalCatalog({
  channelKey, epoch, modelRoot, origin, outputRoot, pluginRoot, rootKey, targetRoot,
} = {}) {
  if (!Number.isSafeInteger(epoch) || epoch < 1 ||
      [channelKey, modelRoot, outputRoot, pluginRoot, rootKey, targetRoot]
        .some((value) => typeof value !== 'string' || !isAbsolute(value) || resolve(value) !== value)) {
    fail('release_catalog_arguments_invalid');
  }
  let parsedOrigin;
  try { parsedOrigin = new URL(origin); } catch { fail('release_catalog_origin_invalid'); }
  if (parsedOrigin.protocol !== 'https:' || parsedOrigin.origin !== origin || parsedOrigin.pathname !== '/' ||
      parsedOrigin.search || parsedOrigin.hash || parsedOrigin.username || parsedOrigin.password) {
    fail('release_catalog_origin_invalid');
  }
  const rootAuthority = privateKey(rootKey, 'root');
  const channelAuthority = privateKey(channelKey, 'channel');
  const pinned = pinnedReleaseKeyring();
  if (pinned.length !== 1 || pinned[0].key_id !== rootAuthority.keyID || rootAuthority.keyID === channelAuthority.keyID) {
    fail('release_catalog_authority_invalid');
  }
  const target = canonicalFile(
    join(targetRoot, 'target-release-fragment.json'),
    'pulse.target_release_build.v2',
  );
  const model = canonicalFile(
    join(modelRoot, 'portable-model-fragment.json'),
    'pulse.portable_model_build.v1',
  );
  const plugin = canonicalFile(
    join(pluginRoot, 'plugin-runtime-fragment.json'),
    'pulse.plugin_runtime_build.v1',
  );
  if (target.target?.target_id !== 'darwin-arm64' || target.target.platform !== 'darwin' ||
      target.target.architecture !== 'arm64' || target.target.libc !== null ||
      target.verification_profile?.kind !== 'apple' || target.verification_profile.team_id !== '44N4NZ86S5' ||
      model.production_ready !== true || plugin.production_ready !== true || plugin.package_version !== PACKAGE_VERSION ||
      !['daemon', 'embedder-runtime'].every((kind) => {
        const artifact = target.artifacts?.[kind];
        return artifact?.format === 'tar.gz' && SHA256.test(artifact.sha256 ?? '') && SHA256.test(artifact.tree_digest ?? '');
      }) ||
      model.artifact?.format !== 'tar.gz' || plugin.artifact?.format !== 'tar.gz') {
    fail('release_catalog_fragment_incompatible');
  }
  mkdirSync(dirname(outputRoot), { recursive: true, mode: 0o700 });
  mkdirSync(outputRoot, { mode: 0o700 });
  let complete = false;
  try {
    const assetRoot = join(outputRoot, 'assets', 'pulse', PACKAGE_VERSION);
    const targetAssets = join(assetRoot, 'darwin-arm64');
    const commonAssets = join(assetRoot, 'common');
    copyCarrier(targetRoot, target.artifacts.daemon.filename, target.artifacts.daemon, join(targetAssets, 'daemon.tar.gz'));
    copyCarrier(
      targetRoot,
      target.artifacts['embedder-runtime'].filename,
      target.artifacts['embedder-runtime'],
      join(targetAssets, 'embedder-runtime.tar.gz'),
    );
    copyCarrier(modelRoot, model.artifact.filename, model.artifact, join(commonAssets, 'model.tar.gz'));
    copyCarrier(pluginRoot, plugin.artifact.filename, plugin.artifact, join(commonAssets, 'plugin-runtime.tar.gz'));
    const prefix = `${origin}/pulse/${PACKAGE_VERSION}`;
    const artifacts = {
      daemon: descriptor({
        artifact: target.artifacts.daemon,
        architecture: 'arm64',
        epoch,
        id: `pulse-${PACKAGE_VERSION}-darwin-arm64-daemon`,
        kind: 'daemon',
        minimumOS: '13.0',
        origin,
        platform: 'darwin',
        signing: appleSigning('daemon', target.verification_profile.team_id),
        url: `${prefix}/darwin-arm64/daemon.tar.gz`,
      }),
      'embedder-runtime': descriptor({
        artifact: target.artifacts['embedder-runtime'],
        architecture: 'arm64',
        epoch,
        id: `pulse-${PACKAGE_VERSION}-darwin-arm64-embedder`,
        kind: 'embedder-runtime',
        minimumOS: '13.0',
        origin,
        platform: 'darwin',
        signing: appleSigning('embedder-runtime', target.verification_profile.team_id),
        url: `${prefix}/darwin-arm64/embedder-runtime.tar.gz`,
      }),
      model: descriptor({
        artifact: model.artifact,
        architecture: 'all',
        epoch,
        id: `pulse-${PACKAGE_VERSION}-portable-model`,
        kind: 'model',
        minimumOS: '0.0',
        origin,
        platform: 'all',
        signing: commonSigning(),
        url: `${prefix}/common/model.tar.gz`,
      }),
      'plugin-runtime': descriptor({
        artifact: plugin.artifact,
        architecture: 'all',
        epoch,
        id: `pulse-${PACKAGE_VERSION}-plugin-runtime`,
        kind: 'plugin-runtime',
        minimumOS: '0.0',
        origin,
        platform: 'all',
        signing: commonSigning(),
        url: `${prefix}/common/plugin-runtime.tar.gz`,
      }),
    };
    const now = new Date();
    const issuedAt = new Date(now.valueOf() - 60_000);
    const releaseExpiresAt = new Date(now.valueOf() + 7 * 24 * 60 * 60 * 1000);
    const authorityExpiresAt = new Date(now.valueOf() + 8 * 24 * 60 * 60 * 1000);
    const payload = Object.freeze({
      allowed_origins: [origin],
      common_artifacts: {
        model: artifacts.model,
        'plugin-runtime': artifacts['plugin-runtime'],
      },
      release: {
        channel: 'preview',
        epoch,
        expires_at: releaseExpiresAt.toISOString(),
        issued_at: issuedAt.toISOString(),
        key_id: channelAuthority.keyID,
        package: '@zbs-gg/pulse',
        version: PACKAGE_VERSION,
      },
      schema: 'pulse.personal_preview.release_catalog.v2',
      targets: {
        'darwin-arm64': {
          architecture: 'arm64',
          artifacts: {
            daemon: artifacts.daemon,
            'embedder-runtime': artifacts['embedder-runtime'],
          },
          capabilities: [],
          libc: null,
          platform: 'darwin',
          verification_profile: target.verification_profile,
        },
      },
    });
    const authorityPayload = Object.freeze({
      channel: 'preview',
      epoch,
      expires_at: authorityExpiresAt.toISOString(),
      issued_at: issuedAt.toISOString(),
      keys: [{
        key_id: channelAuthority.keyID,
        public_key_pem: channelAuthority.publicKey,
        valid_from_epoch: epoch,
        valid_through_epoch: epoch,
      }],
      revoked_key_ids: [],
      schema: 'pulse.release_authority.v1',
    });
    const envelope = Object.freeze({
      authority: {
        payload: authorityPayload,
        schema: 'pulse.release_authority_envelope.v1',
        signature: manifestSignature(authorityPayload, rootAuthority),
      },
      payload,
      schema: 'pulse.release_catalog_envelope.v2',
      signature: manifestSignature(payload, channelAuthority),
    });
    const release = verifyReleaseManifestEnvelope(envelope, {
      architecture: 'arm64',
      libc: null,
      minimumAcceptedEpoch: epoch,
      now,
      osVersion: '26.2',
      packageVersion: PACKAGE_VERSION,
      platform: 'darwin',
      trustedKeys: pinned,
    });
    const manifestPath = join(outputRoot, 'personal-preview-manifest.json');
    writeFileSync(manifestPath, `${canonicalReleaseJSON(envelope)}\n`, { encoding: 'utf8', flag: 'wx', mode: 0o600 });
    const receipt = Object.freeze({
      artifact_count: Object.keys(release.artifacts).length,
      channel_key_id: channelAuthority.keyID,
      manifest_digest: release.manifest_digest,
      production_ready: true,
      root_key_id: rootAuthority.keyID,
      schema: 'pulse.personal_release_catalog_build.v1',
      target_id: release.target_id,
    });
    writeFileSync(join(outputRoot, 'catalog-build-receipt.json'), `${canonicalReleaseJSON(receipt)}\n`, {
      encoding: 'utf8', flag: 'wx', mode: 0o600,
    });
    complete = true;
    return Object.freeze({ manifestPath, receipt });
  } finally {
    if (!complete) rmSync(outputRoot, { recursive: true, force: true });
  }
}

if (process.argv[1] && resolve(process.argv[1]) === scriptPath) {
  const result = buildPersonalCatalog(parseCLI(process.argv.slice(2)));
  process.stdout.write(`${JSON.stringify(result)}\n`);
}
