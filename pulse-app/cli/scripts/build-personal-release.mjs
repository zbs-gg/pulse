#!/usr/bin/env node

import { execFileSync, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  chmodSync, constants, copyFileSync, cpSync, existsSync, lstatSync, mkdirSync, mkdtempSync,
  openSync, closeSync, fstatSync, readFileSync, readSync, readdirSync, rmSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { basename, dirname, isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  materializeVerifiedDmg,
  materializeVerifiedTarGz,
  validateSafetensorsFile,
} from '../src/artifact-installer.js';
import { includeRuntimePath, normalizePrivateTree } from '../src/codex-install.js';
import { loadManagedEmbedderSourceManifest } from '../src/managed-embedder-release.js';
import {
  canonicalReleaseJSON,
  pinnedReleaseKeyring,
  releaseKeyID,
  verifyReleaseManifestEnvelope,
} from '../src/release-manifest.js';

const EXPECTED_TEAM_ID = '44N4NZ86S5';
const IDENTITY = `Developer ID Application: Nikita Shilov (${EXPECTED_TEAM_ID})`;
const IDENTIFIERS = Object.freeze({
  daemon: 'gg.zbs.pulse.daemon',
  'embedder-runtime': 'gg.zbs.pulse.embedder-runtime',
  'presence-helper': 'gg.zbs.pulse.presence-helper',
});
const FILENAMES = Object.freeze({
  daemon: 'daemon.dmg',
  'embedder-runtime': 'embedder-runtime.dmg',
  model: 'model.safetensors',
  'plugin-runtime': 'plugin-runtime.tar.gz',
  'presence-helper': 'presence-helper.dmg',
});
const IDS = Object.freeze({
  daemon: 'pulse-daemon',
  'embedder-runtime': 'pulse-embedder-runtime',
  model: 'pulse-model',
  'plugin-runtime': 'pulse-plugin-runtime',
  'presence-helper': 'pulse-presence-helper',
});

const scriptRoot = dirname(fileURLToPath(import.meta.url));
const cliRoot = resolve(scriptRoot, '..');
const appRoot = resolve(cliRoot, '..');
const repoRoot = resolve(appRoot, '..');
const mcpRoot = join(repoRoot, 'mcp');
const releaseRoot = join(cliRoot, 'release');
const packageJSON = JSON.parse(readFileSync(join(cliRoot, 'package.json'), 'utf8'));
const sources = loadManagedEmbedderSourceManifest();

function fail(code, detail = '') {
  throw new Error(detail ? `${code}: ${detail}` : code);
}

function run(command, args, options = {}) {
  execFileSync(command, args, {
    cwd: options.cwd ?? repoRoot,
    env: options.env ?? process.env,
    stdio: options.stdio ?? 'inherit',
    timeout: options.timeout ?? 30 * 60 * 1000,
  });
}

function sha256File(path, maximum = 64 * 1024 * 1024 * 1024) {
  let descriptor;
  try {
    descriptor = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW);
    const stat = fstatSync(descriptor);
    if (!stat.isFile() || stat.nlink !== 1 || stat.size < 1 || stat.size > maximum) {
      fail('release_artifact_file_invalid', path);
    }
    const hash = createHash('sha256');
    const buffer = Buffer.allocUnsafe(1024 * 1024);
    let bytes = 0;
    for (;;) {
      const count = readSync(descriptor, buffer, 0, buffer.length, null);
      if (count === 0) break;
      hash.update(buffer.subarray(0, count));
      bytes += count;
    }
    if (bytes !== stat.size) fail('release_artifact_changed', path);
    return Object.freeze({ bytes, sha256: hash.digest('hex') });
  } finally {
    if (descriptor !== undefined) closeSync(descriptor);
  }
}

function requireAbsoluteFile(path, label) {
  if (typeof path !== 'string' || !isAbsolute(path) || resolve(path) !== path) fail(`${label}_required`);
  const stat = lstatSync(path);
  if (!stat.isFile() || stat.isSymbolicLink() || stat.nlink !== 1) fail(`${label}_invalid`);
  return path;
}

function requireAbsoluteDirectory(path, label) {
  if (typeof path !== 'string' || !isAbsolute(path) || resolve(path) !== path) fail(`${label}_required`);
  const stat = lstatSync(path);
  if (!stat.isDirectory() || stat.isSymbolicLink()) fail(`${label}_invalid`);
  return path;
}

function releaseOrigin(value = 'https://releases.zbs.gg') {
  let parsed;
  try { parsed = new URL(value); } catch { fail('release_origin_invalid'); }
  if (parsed.protocol !== 'https:' || parsed.username || parsed.password || parsed.search || parsed.hash ||
      parsed.pathname !== '/' || parsed.origin !== value) fail('release_origin_invalid');
  return parsed.origin;
}

function releaseEpoch(value) {
  const epoch = Number.parseInt(value ?? '', 10);
  if (!Number.isSafeInteger(epoch) || epoch < 1 || String(epoch) !== value) fail('release_epoch_required');
  return epoch;
}

function artifactTree(root) {
  const files = [];
  const visit = (directory, prefix = '') => {
    for (const name of readdirSync(directory).sort()) {
      if (name === 'pulse-artifact-tree.json') continue;
      if (name === '.DS_Store' || name.includes('\0')) fail('release_tree_entry_invalid', name);
      const path = join(directory, name);
      const relative = prefix ? `${prefix}/${name}` : name;
      const stat = lstatSync(path);
      if (stat.isSymbolicLink() || (stat.mode & 0o022) !== 0 || (stat.isFile() && stat.nlink !== 1)) {
        fail('release_tree_entry_unsafe', relative);
      }
      if (stat.isDirectory()) {
        visit(path, relative);
      } else if (stat.isFile()) {
        const digest = sha256File(path, 4 * 1024 * 1024 * 1024);
        const executable = (stat.mode & 0o111) !== 0;
        const mode = executable ? 0o700 : 0o600;
        if ((stat.mode & 0o777) !== mode) fail('release_tree_mode_invalid', relative);
        files.push({ bytes: digest.bytes, executable, mode, path: relative, sha256: digest.sha256 });
      } else {
        fail('release_tree_entry_invalid', relative);
      }
    }
  };
  visit(root);
  if (files.length < 1 || files.length > 8191) fail('release_tree_file_count_invalid');
  return Object.freeze({ files: Object.freeze(files), schema: 'pulse.artifact_tree.v1' });
}

function writeArtifactTree(root) {
  const tree = artifactTree(root);
  writeFileSync(join(root, 'pulse-artifact-tree.json'), `${canonicalReleaseJSON(tree)}\n`, {
    encoding: 'utf8', flag: 'wx', mode: 0o600,
  });
  return tree;
}

function cloneArtifact(source, destination) {
  rmSync(destination, { force: true });
  copyFileSync(source, destination, constants.COPYFILE_FICLONE);
  chmodSync(destination, 0o600);
  return destination;
}

function notarize(carrier, profile) {
  run('/usr/bin/xcrun', ['notarytool', 'submit', carrier, '--keychain-profile', profile, '--wait']);
  run('/usr/bin/xcrun', ['stapler', 'staple', carrier]);
  run('/usr/bin/xcrun', ['stapler', 'validate', carrier]);
  run('/usr/sbin/spctl', ['-a', '-t', 'open', '--context', 'context:primary-signature', '-vv', carrier]);
}

function buildDaemon(output, profile, work) {
  const staging = join(work, 'Pulse Daemon');
  const binary = join(staging, 'bin', 'pulse');
  mkdirSync(dirname(binary), { recursive: true, mode: 0o700 });
  run('go', ['build', '-trimpath', '-o', binary, './cmd/pulse'], {
    cwd: appRoot,
    env: { ...process.env, CGO_ENABLED: '1', GOARCH: 'arm64', GOOS: 'darwin' },
  });
  chmodSync(binary, 0o700);
  run('/usr/bin/codesign', [
    '--force', '--options', 'runtime', '--timestamp', '--identifier', IDENTIFIERS.daemon, '--sign', IDENTITY, binary,
  ]);
  run('/usr/bin/codesign', ['--verify', '--strict', '--verbose=2', binary]);
  writeArtifactTree(staging);
  rmSync(output, { force: true });
  run('/usr/bin/hdiutil', ['create', '-volname', 'Pulse Daemon', '-srcfolder', staging, '-format', 'UDZO', '-ov', output]);
  run('/usr/bin/codesign', [
    '--force', '--timestamp', '--identifier', IDENTIFIERS.daemon, '--sign', IDENTITY, output,
  ]);
  run('/usr/bin/codesign', ['--verify', '--strict', '--verbose=2', output]);
  notarize(output, profile);
}

function pruneEmptyDirectories(root) {
  const visit = (directory, keep) => {
    for (const name of readdirSync(directory)) {
      const path = join(directory, name);
      if (lstatSync(path).isDirectory()) visit(path, false);
    }
    if (!keep && readdirSync(directory).length === 0) rmSync(directory, { recursive: true });
  };
  visit(root, true);
}

function buildPluginRuntime(output, work) {
  run('npm', ['ci', '--ignore-scripts', '--silent'], { cwd: cliRoot });
  run('npm', ['ci', '--ignore-scripts', '--silent'], { cwd: mcpRoot });
  run('npm', ['run', '--silent', 'build'], { cwd: mcpRoot });
  const vendorMcp = join(cliRoot, 'vendor', 'pulse-mcp-dist');
  rmSync(vendorMcp, { recursive: true, force: true });
  cpSync(join(mcpRoot, 'dist'), vendorMcp, { recursive: true, dereference: true });

  const staging = join(work, 'Pulse Plugin Runtime');
  mkdirSync(join(staging, 'marketplace', '.agents', 'plugins'), { recursive: true, mode: 0o700 });
  mkdirSync(join(staging, 'marketplace', '.claude-plugin'), { recursive: true, mode: 0o700 });
  cpSync(join(repoRoot, '.agents', 'plugins', 'marketplace.json'),
    join(staging, 'marketplace', '.agents', 'plugins', 'marketplace.json'), { dereference: true });
  cpSync(join(repoRoot, '.claude-plugin', 'marketplace.json'),
    join(staging, 'marketplace', '.claude-plugin', 'marketplace.json'), { dereference: true });
  cpSync(join(repoRoot, 'plugins', 'pulse'), join(staging, 'marketplace', 'plugins', 'pulse'), {
    recursive: true, dereference: true,
  });
  cpSync(cliRoot, join(staging, 'runtime'), {
    recursive: true,
    dereference: true,
    filter: (sourcePath) => includeRuntimePath(cliRoot, sourcePath),
  });
  pruneEmptyDirectories(staging);
  normalizePrivateTree(staging);
  writeArtifactTree(staging);
  rmSync(output, { force: true });
  run('/usr/bin/tar', ['-czf', output, '-C', staging, 'marketplace', 'runtime', 'pulse-artifact-tree.json'], {
    env: { ...process.env, COPYFILE_DISABLE: '1' },
  });
  chmodSync(output, 0o600);
}

function unsignedSigning() {
  return {
    gatekeeper: false, identifier: null, notarized: false, scheme: 'release-manifest', stapled: false, team_id: null,
  };
}

function nativeSigning(identifier) {
  return {
    gatekeeper: true, identifier, notarized: true, scheme: 'apple-developer-id', stapled: true,
    team_id: EXPECTED_TEAM_ID,
  };
}

function descriptor(kind, file, { epoch, origin, version }) {
  const digest = sha256File(file);
  const native = Object.hasOwn(IDENTIFIERS, kind);
  return Object.freeze({
    architecture: 'arm64',
    bytes: digest.bytes,
    epoch,
    executable: native,
    format: kind === 'model' ? 'safetensors' : kind === 'plugin-runtime' ? 'tar.gz' : 'dmg',
    id: IDS[kind],
    kind,
    minimum_os: kind === 'daemon' || kind === 'presence-helper' ? '13.0' : '13.5',
    model_policy: kind === 'model' ? { custom_code: false, data_only: true } : null,
    origin,
    platform: 'darwin',
    sha256: digest.sha256,
    signing: native ? nativeSigning(IDENTIFIERS[kind]) : unsignedSigning(),
    url: `${origin}/pulse/${version}/${FILENAMES[kind]}`,
    version,
  });
}

function validateInputs() {
  if (process.platform !== 'darwin' || process.arch !== 'arm64') fail('release_platform_unsupported');
  const profile = process.env.PULSE_NOTARYTOOL_PROFILE;
  if (typeof profile !== 'string' || profile.length < 1 || profile.length > 128 || /[\r\n\0]/.test(profile)) {
    fail('notary_profile_required');
  }
  const releaseKey = requireAbsoluteFile(process.env.PULSE_RELEASE_SIGNING_KEY_PATH, 'release_signing_key');
  const mlxModel = requireAbsoluteFile(process.env.PULSE_BGE_M3_MLX_MODEL, 'mlx_model');
  const referenceModel = requireAbsoluteDirectory(process.env.PULSE_BGE_M3_REFERENCE_MODEL, 'reference_model');
  const referencePython = requireAbsoluteFile(process.env.PULSE_FLAGEMBEDDING_PYTHON, 'flagembedding_python');
  const expectedModel = sources.components.find((item) => item.id === 'bge-m3-mlx-fp16');
  const actualModel = sha256File(mlxModel);
  if (actualModel.bytes !== expectedModel.bytes || actualModel.sha256 !== expectedModel.sha256) fail('mlx_model_digest_mismatch');
  for (const expected of sources.quality_reference.model_files) {
    const actual = sha256File(join(referenceModel, expected.path));
    if (actual.bytes !== expected.bytes || actual.sha256 !== expected.sha256) {
      fail('reference_model_digest_mismatch', expected.path);
    }
  }
  const publicRoot = readFileSync(join(releaseRoot, 'pulse-release-root.pem'), 'utf8');
  const privateKey = readFileSync(releaseKey, 'utf8');
  const keyID = releaseKeyID(privateKey);
  if (keyID !== releaseKeyID(publicRoot)) fail('release_signing_key_does_not_match_pinned_root');
  return Object.freeze({
    epoch: releaseEpoch(process.env.PULSE_RELEASE_EPOCH),
    keyID,
    mlxModel,
    origin: releaseOrigin(process.env.PULSE_RELEASE_ORIGIN),
    profile,
    referenceModel,
    referencePython,
    releaseKey,
    version: packageJSON.version,
  });
}

const inputs = validateInputs();
if (process.argv.includes('--check')) {
  process.stdout.write(`${JSON.stringify({
    schema: 'pulse.personal_release_preflight.v1',
    status: 'ready_for_authorized_notarization',
    version: inputs.version,
    epoch: inputs.epoch,
    origin: inputs.origin,
    model_sha256: sources.components.find((item) => item.id === 'bge-m3-mlx-fp16').sha256,
    release_key_id: inputs.keyID,
  })}\n`);
  process.exit(0);
}
if (process.env.PULSE_PRODUCTION_RELEASE !== '1') fail('production_release_confirmation_required');
if (process.env.PULSE_RELEASE_SUBMISSION_AUTHORIZATION !== 'apple-notarization-approved') {
  fail('notarization_submission_authorization_required');
}

const outputRoot = join(releaseRoot, 'dist', inputs.version);
const work = mkdtempSync(join(tmpdir(), 'pulse-personal-release-'));
const verification = mkdtempSync(join(tmpdir(), 'pulse-personal-release-verify-'));
try {
  rmSync(outputRoot, { recursive: true, force: true });
  mkdirSync(outputRoot, { recursive: true, mode: 0o700 });

  const childEnv = {
    ...process.env,
    PULSE_PRODUCTION_RELEASE: '1',
    PULSE_NOTARYTOOL_PROFILE: inputs.profile,
    PULSE_FLAGEMBEDDING_PYTHON: inputs.referencePython,
    PULSE_BGE_M3_REFERENCE_MODEL: inputs.referenceModel,
    PULSE_BGE_M3_MLX_MODEL: inputs.mlxModel,
  };
  run(process.execPath, [join(scriptRoot, 'build-presence-helper.mjs')], { cwd: cliRoot, env: childEnv });
  run(process.execPath, [join(scriptRoot, 'build-embedder-runtime.mjs')], { cwd: cliRoot, env: childEnv });

  buildDaemon(join(outputRoot, FILENAMES.daemon), inputs.profile, work);
  buildPluginRuntime(join(outputRoot, FILENAMES['plugin-runtime']), work);
  cloneArtifact(inputs.mlxModel, join(outputRoot, FILENAMES.model));
  cloneArtifact(
    join(appRoot, 'native', 'pulse-embedder-runtime', 'dist', 'current', 'gg.zbs.pulse.embedder-runtime.dmg'),
    join(outputRoot, FILENAMES['embedder-runtime']),
  );
  cloneArtifact(
    join(appRoot, 'native', 'pulse-presence-helper', 'dist', 'gg.zbs.pulse.presence-helper.dmg'),
    join(outputRoot, FILENAMES['presence-helper']),
  );

  const artifacts = Object.fromEntries(Object.keys(FILENAMES).map((kind) => [
    kind, descriptor(kind, join(outputRoot, FILENAMES[kind]), inputs),
  ]));
  const issued = new Date(Date.now() - 60_000);
  const payload = {
    allowed_origins: [inputs.origin],
    artifacts,
    release: {
      channel: 'preview',
      epoch: inputs.epoch,
      expires_at: new Date(issued.getTime() + 90 * 24 * 60 * 60 * 1000).toISOString(),
      issued_at: issued.toISOString(),
      key_id: inputs.keyID,
      package: '@zbs-gg/pulse',
      version: inputs.version,
    },
    schema: 'pulse.personal_preview.release_manifest.v1',
  };
  const payloadPath = join(releaseRoot, 'personal-preview-manifest.payload.json');
  const manifestPath = join(releaseRoot, 'personal-preview-manifest.json');
  rmSync(payloadPath, { force: true });
  rmSync(manifestPath, { force: true });
  writeFileSync(payloadPath, `${canonicalReleaseJSON(payload)}\n`, { mode: 0o600 });
  run(process.execPath, [join(scriptRoot, 'sign-release-manifest.mjs'), payloadPath, manifestPath], {
    cwd: cliRoot,
    env: { ...process.env, PULSE_RELEASE_SIGNING_KEY_PATH: inputs.releaseKey },
  });

  const envelope = JSON.parse(readFileSync(manifestPath, 'utf8'));
  const macOS = execFileSync('/usr/bin/sw_vers', ['-productVersion'], { encoding: 'utf8' }).trim();
  const release = verifyReleaseManifestEnvelope(envelope, {
    architecture: 'arm64', minimumAcceptedEpoch: inputs.epoch, now: new Date(), osVersion: macOS,
    packageVersion: inputs.version, platform: 'darwin', trustedKeys: pinnedReleaseKeyring(),
  });

  await materializeVerifiedDmg(join(outputRoot, FILENAMES.daemon), join(verification, 'daemon'), artifacts.daemon);
  await materializeVerifiedDmg(
    join(outputRoot, FILENAMES['embedder-runtime']), join(verification, 'embedder-runtime'), artifacts['embedder-runtime'],
  );
  await materializeVerifiedDmg(
    join(outputRoot, FILENAMES['presence-helper']), join(verification, 'presence-helper'), artifacts['presence-helper'],
  );
  await materializeVerifiedTarGz(
    join(outputRoot, FILENAMES['plugin-runtime']), join(verification, 'plugin-runtime'), artifacts['plugin-runtime'],
  );
  validateSafetensorsFile(join(outputRoot, FILENAMES.model));

  const receipt = {
    schema: 'pulse.personal_release_build.v1',
    status: 'verified_unpublished',
    version: inputs.version,
    epoch: inputs.epoch,
    manifest_digest: release.manifest_digest,
    artifacts: Object.fromEntries(Object.entries(artifacts).map(([kind, value]) => [kind, {
      bytes: value.bytes, sha256: value.sha256, filename: basename(value.url),
    }])),
  };
  writeFileSync(join(outputRoot, 'release-receipt.json'), `${canonicalReleaseJSON(receipt)}\n`, { mode: 0o600 });
  process.stdout.write(`${JSON.stringify(receipt)}\n`);
} finally {
  rmSync(work, { recursive: true, force: true });
  rmSync(verification, { recursive: true, force: true });
}
