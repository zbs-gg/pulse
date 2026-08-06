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
  canonicalReleaseJSON, pinnedReleaseKeyring, releaseKeyID, verifyPersonalReleaseArtifactSet,
} from '../src/release-manifest.js';
import {
  DESKTOP_TARGET_IDS, desktopTargetDefinition,
} from '../src/desktop-target.js';
import { loadNativeUniversalMatrix } from './native-universal-matrix.mjs';

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
const TARGET_ARTIFACT_KINDS = Object.freeze(['daemon', 'embedder-runtime']);
const SHA256 = /^[a-f0-9]{64}$/;

function fail(code, detail = '') {
  throw new Error(detail ? `${code}: ${detail}` : code);
}

function parseCLI(argv) {
  const values = {};
  const targetRoots = {};
  const allowed = new Set(['channel-key', 'epoch', 'model', 'origin', 'output', 'plugin', 'root-key']);
  for (let index = 0; index < argv.length; index += 2) {
    const option = argv[index];
    const value = argv[index + 1];
    if (!option?.startsWith('--') || value === undefined) fail('release_catalog_arguments_invalid');
    const name = option.slice(2);
    if (name === 'target') {
      const separator = value.indexOf('=');
      const targetID = separator < 1 ? '' : value.slice(0, separator);
      const targetRoot = separator < 1 ? '' : value.slice(separator + 1);
      if (!DESKTOP_TARGET_IDS.includes(targetID) || !isAbsolute(targetRoot) || Object.hasOwn(targetRoots, targetID)) {
        fail('release_catalog_arguments_invalid');
      }
      targetRoots[targetID] = resolve(targetRoot);
      continue;
    }
    if (!allowed.has(name) || Object.hasOwn(values, name)) fail('release_catalog_arguments_invalid');
    values[name] = value;
  }
  const epoch = Number(values.epoch);
  const paths = {
    channelKey: values['channel-key'] ? resolve(values['channel-key']) : null,
    modelRoot: values.model ? resolve(values.model) : null,
    outputRoot: values.output ? resolve(values.output) : null,
    pluginRoot: values.plugin ? resolve(values.plugin) : null,
    rootKey: values['root-key'] ? resolve(values['root-key']) : null,
  };
  const targetIDs = DESKTOP_TARGET_IDS.filter((targetID) => Object.hasOwn(targetRoots, targetID));
  if (Object.values(paths).some((value) => !value || !isAbsolute(value)) ||
      targetIDs.length < 1 || Object.keys(targetRoots).some((targetID) => !DESKTOP_TARGET_IDS.includes(targetID)) ||
      !Number.isSafeInteger(epoch) || epoch < 1 || typeof values.origin !== 'string') {
    fail('release_catalog_arguments_invalid');
  }
  return Object.freeze({ ...paths, epoch, origin: values.origin, targetRoots: Object.freeze(targetRoots) });
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

function windowsSigning() {
  return Object.freeze({
    ...commonSigning(),
    scheme: 'windows-authenticode',
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

function exactKeys(value, expected) {
  return value && !Array.isArray(value) && typeof value === 'object' &&
    Object.keys(value).sort().join('\0') === [...expected].sort().join('\0');
}

function compatibleCarrier(artifact, kind) {
  return artifact && !Array.isArray(artifact) && typeof artifact === 'object' &&
    artifact.filename === `${kind}.tar.gz` && artifact.format === 'tar.gz' &&
    Number.isSafeInteger(artifact.bytes) && artifact.bytes > 0 && artifact.bytes <= 64 * 1024 * 1024 * 1024 &&
    SHA256.test(artifact.sha256 ?? '') && SHA256.test(artifact.tree_digest ?? '');
}

function compatibleVerificationProfile(profile, target) {
  if (target.platform === 'darwin') {
    return exactKeys(profile, ['gatekeeper', 'kind', 'notarized', 'stapled', 'team_id']) &&
      profile.kind === 'apple' && profile.gatekeeper === true && profile.notarized === true &&
      profile.stapled === false && profile.team_id === '44N4NZ86S5';
  }
  if (target.platform === 'win32') {
    if (!exactKeys(profile, ['kind', 'publisher', 'timestamp_url', 'timestamped']) ||
        profile.kind !== 'windows' || profile.timestamped !== true ||
        typeof profile.publisher !== 'string' ||
        !/^CN=[^,\r\n]{1,128}(?:, ?(?:O|OU|L|S|C)=[^,\r\n]{1,128})*$/.test(profile.publisher)) return false;
    let timestamp;
    try { timestamp = new URL(profile.timestamp_url); } catch { return false; }
    return timestamp.protocol === 'https:' && !timestamp.username && !timestamp.password &&
      !timestamp.search && !timestamp.hash;
  }
  return exactKeys(profile, ['kind', 'policy']) &&
    profile.kind === 'linux' && profile.policy === 'signed-catalog-tree-v1';
}

function compatibleTargetFragment(fragment, targetID) {
  const target = desktopTargetDefinition(targetID);
  return exactKeys(fragment, [
    'artifacts', 'attestation_state', 'production_ready', 'schema', 'target', 'verification_profile',
  ]) && fragment.schema === 'pulse.target_release_build.v2' &&
    fragment.attestation_state === 'pending-signed-catalog-runtime-proof' && fragment.production_ready === false &&
    exactKeys(fragment.target, ['architecture', 'libc', 'platform', 'target_id']) &&
    fragment.target.target_id === targetID && fragment.target.platform === target.platform &&
    fragment.target.architecture === target.architecture && fragment.target.libc === target.libc &&
    exactKeys(fragment.artifacts, TARGET_ARTIFACT_KINDS) &&
    TARGET_ARTIFACT_KINDS.every((kind) => compatibleCarrier(fragment.artifacts[kind], kind)) &&
    compatibleVerificationProfile(fragment.verification_profile, target);
}

function exactTargetRoots(targetRoots) {
  if (!targetRoots || Array.isArray(targetRoots) || typeof targetRoots !== 'object') return false;
  const targetIDs = DESKTOP_TARGET_IDS.filter((targetID) => Object.hasOwn(targetRoots, targetID));
  if (targetIDs.length < 1 || Object.keys(targetRoots).some((targetID) => !DESKTOP_TARGET_IDS.includes(targetID))) return false;
  return targetIDs.every((targetID) => {
    const value = targetRoots[targetID];
    return typeof value === 'string' && isAbsolute(value) && resolve(value) === value;
  });
}

function targetSigning(kind, target, profile) {
  if (target.platform === 'darwin') return appleSigning(kind, profile.team_id);
  if (target.platform === 'win32') return windowsSigning();
  return commonSigning();
}

function targetMinimumOS(target) {
  return target.platform === 'darwin' ? '13.0' : '0.0';
}

function verificationHost(target) {
  return Object.freeze({
    architecture: target.architecture,
    libc: target.libc,
    osVersion: target.platform === 'darwin' ? '26.2' : '0.0',
    platform: target.platform,
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
  channelKey, epoch, modelRoot, origin, outputRoot, pluginRoot, rootKey, targetRoots,
  testMode = false, testOnlyTrustedKeys,
} = {}) {
  if (!Number.isSafeInteger(epoch) || epoch < 1 ||
      [channelKey, modelRoot, outputRoot, pluginRoot, rootKey]
        .some((value) => typeof value !== 'string' || !isAbsolute(value) || resolve(value) !== value) ||
      !exactTargetRoots(targetRoots) || ![true, false].includes(testMode) ||
      (testMode !== true && testOnlyTrustedKeys !== undefined) ||
      (testMode === true && (!Array.isArray(testOnlyTrustedKeys) || testOnlyTrustedKeys.length !== 1))) {
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
  const pinned = testMode === true ? testOnlyTrustedKeys : pinnedReleaseKeyring();
  if (pinned.length !== 1 || pinned[0].key_id !== rootAuthority.keyID || rootAuthority.keyID === channelAuthority.keyID) {
    fail('release_catalog_authority_invalid');
  }
  const targetIDs = DESKTOP_TARGET_IDS.filter((targetID) => Object.hasOwn(targetRoots, targetID));
  const targetInputs = Object.fromEntries(targetIDs.map((targetID) => {
    const fragment = canonicalFile(
      join(targetRoots[targetID], 'target-release-fragment.json'),
      'pulse.target_release_build.v2',
    );
    if (!compatibleTargetFragment(fragment, targetID)) fail('release_catalog_fragment_incompatible', targetID);
    return [targetID, Object.freeze({
      definition: desktopTargetDefinition(targetID), fragment, root: targetRoots[targetID],
    })];
  }));
  const model = canonicalFile(
    join(modelRoot, 'portable-model-fragment.json'),
    'pulse.portable_model_build.v1',
  );
  const plugin = canonicalFile(
    join(pluginRoot, 'plugin-runtime-fragment.json'),
    'pulse.plugin_runtime_build.v1',
  );
  if (model.production_ready !== true || plugin.production_ready !== true || plugin.package_version !== PACKAGE_VERSION ||
      !compatibleCarrier(model.artifact, 'model') || !compatibleCarrier(plugin.artifact, 'plugin-runtime')) {
    fail('release_catalog_fragment_incompatible');
  }
  mkdirSync(dirname(outputRoot), { recursive: true, mode: 0o700 });
  mkdirSync(outputRoot, { mode: 0o700 });
  let complete = false;
  try {
    const assetRoot = join(outputRoot, 'pulse', PACKAGE_VERSION, `epoch-${epoch}`);
    const commonAssets = join(assetRoot, 'common');
    for (const targetID of targetIDs) {
      const input = targetInputs[targetID];
      const targetAssets = join(assetRoot, targetID);
      for (const kind of TARGET_ARTIFACT_KINDS) {
        copyCarrier(
          input.root,
          input.fragment.artifacts[kind].filename,
          input.fragment.artifacts[kind],
          join(targetAssets, `${kind}.tar.gz`),
        );
      }
    }
    copyCarrier(modelRoot, model.artifact.filename, model.artifact, join(commonAssets, 'model.tar.gz'));
    copyCarrier(pluginRoot, plugin.artifact.filename, plugin.artifact, join(commonAssets, 'plugin-runtime.tar.gz'));
    const prefix = `${origin}/pulse/${PACKAGE_VERSION}/epoch-${epoch}`;
    const commonArtifacts = {
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
    const catalogTargets = Object.fromEntries(targetIDs.map((targetID) => {
      const input = targetInputs[targetID];
      const artifacts = Object.fromEntries(TARGET_ARTIFACT_KINDS.map((kind) => [kind, descriptor({
        artifact: input.fragment.artifacts[kind],
        architecture: input.definition.architecture,
        epoch,
        id: `pulse-${PACKAGE_VERSION}-${targetID}-${kind}`,
        kind,
        minimumOS: targetMinimumOS(input.definition),
        origin,
        platform: input.definition.platform,
        signing: targetSigning(kind, input.definition, input.fragment.verification_profile),
        url: `${prefix}/${targetID}/${kind}.tar.gz`,
      })]));
      return [targetID, Object.freeze({
        architecture: input.definition.architecture,
        artifacts: Object.freeze(artifacts),
        capabilities: Object.freeze([]),
        libc: input.definition.libc,
        platform: input.definition.platform,
        verification_profile: input.fragment.verification_profile,
      })];
    }));
    const matrix = loadNativeUniversalMatrix();
    const snapshotURL = `${origin}/pulse/${PACKAGE_VERSION}/catalog/snapshot.json`;
    const artifactSetPayload = Object.freeze({
      allowed_origins: [origin],
      common_artifacts: {
        model: commonArtifacts.model,
        'plugin-runtime': commonArtifacts['plugin-runtime'],
      },
      host_policy: {
        harnesses: matrix.harnesses.map((harness) => Object.freeze({ ...harness })),
      },
      release: {
        channel: 'preview',
        epoch,
        key_id: channelAuthority.keyID,
        package: '@zbs-gg/pulse',
        version: PACKAGE_VERSION,
      },
      schema: 'pulse.personal_release_artifact_set_payload.v1',
      snapshot_url: snapshotURL,
      targets: catalogTargets,
    });
    const artifactSet = Object.freeze({
      payload: artifactSetPayload,
      schema: 'pulse.personal_release_artifact_set.v1',
      signature: manifestSignature(artifactSetPayload, channelAuthority),
    });
    const artifactSetBytes = `${canonicalReleaseJSON(artifactSet)}\n`;
    const artifactSetDigest = createHash('sha256').update(artifactSetBytes).digest('hex');
    const now = new Date();
    const issuedAt = new Date(now.valueOf() - 60_000);
    const snapshotExpiresAt = new Date(issuedAt.valueOf() + 30 * 24 * 60 * 60 * 1000);
    const snapshotPayload = Object.freeze({
      artifact_set: {
        sha256: artifactSetDigest,
        url: `${prefix}/catalog/artifact-set.json`,
      },
      channel: {
        key_id: channelAuthority.keyID,
        public_key_pem: channelAuthority.publicKey,
        valid_from_epoch: epoch,
        valid_through_epoch: epoch,
      },
      expires_at: snapshotExpiresAt.toISOString(),
      issued_at: issuedAt.toISOString(),
      package: '@zbs-gg/pulse',
      release_epoch: epoch,
      revoked_key_ids: [],
      schema: 'pulse.release_snapshot.v1',
      version: PACKAGE_VERSION,
    });
    const snapshot = Object.freeze({
      payload: snapshotPayload,
      schema: 'pulse.release_snapshot_envelope.v1',
      signature: manifestSignature(snapshotPayload, rootAuthority),
    });
    const snapshotBytes = `${canonicalReleaseJSON(snapshot)}\n`;
    const snapshotDigest = createHash('sha256').update(snapshotBytes).digest('hex');
    const releases = targetIDs.map((targetID) => {
      const release = verifyPersonalReleaseArtifactSet(artifactSet, snapshot, {
        ...verificationHost(targetInputs[targetID].definition),
        minimumAcceptedEpoch: epoch,
        now,
        packageVersion: PACKAGE_VERSION,
        trustedKeys: pinned,
      });
      if (release.target_id !== targetID) fail('release_catalog_target_verification_failed', targetID);
      return release;
    });
    if (new Set(releases.map((release) => release.manifest_digest)).size !== 1) {
      fail('release_catalog_target_verification_failed');
    }
    const manifestPath = join(outputRoot, 'personal-preview-manifest.json');
    const artifactSetPath = join(assetRoot, 'catalog', 'artifact-set.json');
    const snapshotPath = join(outputRoot, 'pulse', PACKAGE_VERSION, 'catalog', 'snapshot.json');
    mkdirSync(dirname(artifactSetPath), { recursive: true, mode: 0o700 });
    mkdirSync(dirname(snapshotPath), { recursive: true, mode: 0o700 });
    writeFileSync(artifactSetPath, artifactSetBytes, { encoding: 'utf8', flag: 'wx', mode: 0o600 });
    writeFileSync(snapshotPath, snapshotBytes, { encoding: 'utf8', flag: 'wx', mode: 0o600 });
    writeFileSync(manifestPath, artifactSetBytes, { encoding: 'utf8', flag: 'wx', mode: 0o600 });
    writeFileSync(join(outputRoot, 'snapshot.json'), snapshotBytes, { encoding: 'utf8', flag: 'wx', mode: 0o600 });
    const receipt = Object.freeze({
      artifact_count: 2 + targetIDs.length * TARGET_ARTIFACT_KINDS.length,
      artifact_set_digest: artifactSetDigest,
      artifact_set_url: snapshotPayload.artifact_set.url,
      channel_key_id: channelAuthority.keyID,
      host_target_count: matrix.harnesses.length * targetIDs.length,
      hosts: matrix.harnesses.map((harness) => harness.host).sort(),
      manifest_digest: artifactSetDigest,
      production_ready: testMode !== true,
      release_epoch: epoch,
      root_key_id: rootAuthority.keyID,
      schema: 'pulse.personal_release_catalog_build.v3',
      snapshot_digest: snapshotDigest,
      snapshot_expires_at: snapshotPayload.expires_at,
      snapshot_url: snapshotURL,
      target_count: targetIDs.length,
      target_ids: [...targetIDs],
    });
    writeFileSync(join(outputRoot, 'catalog-build-receipt.json'), `${canonicalReleaseJSON(receipt)}\n`, {
      encoding: 'utf8', flag: 'wx', mode: 0o600,
    });
    complete = true;
    return Object.freeze({ artifactSetPath, manifestPath, receipt, snapshotPath });
  } finally {
    if (!complete) rmSync(outputRoot, { recursive: true, force: true });
  }
}

if (process.argv[1] && resolve(process.argv[1]) === scriptPath) {
  const result = buildPersonalCatalog(parseCLI(process.argv.slice(2)));
  process.stdout.write(`${JSON.stringify(result)}\n`);
}
