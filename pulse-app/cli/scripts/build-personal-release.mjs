#!/usr/bin/env node

import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { buildPortableEmbedderRuntime } from './build-portable-embedder-runtime.mjs';
import {
  buildDaemonTarget,
  packNormalizedArtifact,
  prepareNormalizedArtifact,
  releaseTargetDefinition,
  releaseTargetIDs,
} from './release-builder-core.mjs';

const scriptRoot = dirname(fileURLToPath(import.meta.url));
const cliRoot = resolve(scriptRoot, '..');
const appRoot = resolve(cliRoot, '..');
const portableEmbedderRoot = join(cliRoot, 'runtime', 'embedder-portable');
const APPLE_TEAM_ID = '44N4NZ86S5';
const APPLE_IDENTIFIERS = Object.freeze({
  daemon: 'gg.zbs.pulse.daemon',
  'embedder-runtime': 'gg.zbs.pulse.embedder-runtime',
});

function fail(code, detail = '') {
  throw new Error(detail ? `${code}: ${detail}` : code);
}

function run(command, args, options = {}) {
  execFileSync(command, args, {
    cwd: options.cwd ?? appRoot,
    env: { ...process.env, ...(options.env ?? {}) },
    stdio: options.stdio ?? 'inherit',
    timeout: options.timeout ?? 30 * 60 * 1000,
  });
}

function parseCLI(argv) {
  const values = {};
  let check = false;
  for (let index = 0; index < argv.length;) {
    const key = argv[index];
    if (key === '--check') { check = true; index += 1; continue; }
    if (!key?.startsWith('--') || argv[index + 1] === undefined) fail('release_builder_arguments_invalid');
    const name = key.slice(2);
    if (Object.hasOwn(values, name)) fail('release_builder_arguments_invalid');
    values[name] = argv[index + 1];
    index += 2;
  }
  if (!['fixture', 'production'].includes(values.mode) || !releaseTargetIDs().includes(values.target)) {
    fail('release_builder_arguments_invalid');
  }
  const outputRoot = values.output ? resolve(values.output) : null;
  if (!check && (!outputRoot || !isAbsolute(outputRoot))) fail('release_builder_output_required');
  return Object.freeze({
    check,
    mode: values.mode,
    outputRoot,
    runnerInput: values.runner ? resolve(values.runner) : undefined,
    targetID: values.target,
    targetRuntimeRoot: values['target-runtime'] ? resolve(values['target-runtime']) : undefined,
  });
}

function currentTargetID() {
  const platform = process.platform;
  const architecture = process.arch === 'x64' ? 'x64' : process.arch === 'arm64' ? 'arm64' : null;
  if (!architecture) return null;
  if (platform === 'darwin') return `darwin-${architecture}`;
  if (platform === 'win32') return `win32-${architecture}`;
  if (platform === 'linux' && process.report?.getReport()?.header?.glibcVersionRuntime) return `linux-${architecture}-gnu`;
  return null;
}

function requireProductionAuthority(targetID) {
  if (process.env.PULSE_PRODUCTION_RELEASE !== '1' ||
      process.env.PULSE_RELEASE_SUBMISSION_AUTHORIZATION !== 'target-build-approved') {
    fail('production_release_authorization_required');
  }
  if (currentTargetID() !== targetID) fail('production_release_native_target_required');
}

function executablePaths(staging, target) {
  return Object.freeze({
    daemon: join(staging.daemon, 'bin', target.daemon_name),
    'embedder-runtime': join(staging.embedder, 'bin', target.platform === 'win32' ? 'pulse-embedder.exe' : 'pulse-embedder'),
  });
}

function applePolicy() {
  return Object.freeze({
    gatekeeper: true,
    kind: 'apple',
    notarized: true,
    stapled: false,
    team_id: APPLE_TEAM_ID,
  });
}

function signAndSubmitApple(executables, work) {
  const identity = process.env.PULSE_APPLE_SIGNING_IDENTITY;
  const profile = process.env.PULSE_NOTARYTOOL_PROFILE;
  if (typeof identity !== 'string' || !identity.endsWith(`(${APPLE_TEAM_ID})`) || /[\r\n\0]/.test(identity) ||
      typeof profile !== 'string' || profile.length < 1 || profile.length > 128 || /[\r\n\0]/.test(profile)) {
    fail('apple_release_credentials_required');
  }
  for (const [kind, path] of Object.entries(executables)) {
    run('/usr/bin/codesign', [
      '--force', '--options', 'runtime', '--timestamp', '--identifier', APPLE_IDENTIFIERS[kind], '--sign', identity, path,
    ]);
    run('/usr/bin/codesign', ['--verify', '--strict', '--verbose=4', path]);
    // Apple can notarize a standalone CLI binary submitted in a ZIP, but cannot
    // staple the resulting ticket to that binary or to the ZIP. Runtime proof
    // therefore requires Gatekeeper's Notarized Developer ID evidence online.
    const submission = join(work, `${kind}-notary-submission.zip`);
    run('/usr/bin/ditto', ['-c', '-k', '--keepParent', path, submission]);
    run('/usr/bin/xcrun', ['notarytool', 'submit', submission, '--keychain-profile', profile, '--wait']);
    rmSync(submission, { force: true });
    run('/usr/sbin/spctl', ['-a', '-t', 'execute', '-vv', path]);
  }
  return applePolicy();
}

function windowsPolicy() {
  const publisher = process.env.PULSE_WINDOWS_SIGNING_PUBLISHER;
  const timestampURL = process.env.PULSE_WINDOWS_TIMESTAMP_URL;
  let parsed;
  try { parsed = new URL(timestampURL); } catch { fail('windows_release_credentials_required'); }
  if (typeof publisher !== 'string' || !/^CN=[^,\r\n]{1,128}(?:, ?(?:O|OU|L|S|C)=[^,\r\n]{1,128})*$/.test(publisher) ||
      parsed.protocol !== 'https:' || parsed.username || parsed.password || parsed.search || parsed.hash) {
    fail('windows_release_credentials_required');
  }
  return Object.freeze({ kind: 'windows', publisher, timestamp_url: parsed.href, timestamped: true });
}

function signWindows(executables) {
  const tool = process.env.PULSE_SIGNTOOL_PATH;
  const thumbprint = process.env.PULSE_WINDOWS_CERTIFICATE_SHA1;
  if (!tool || !isAbsolute(tool) || resolve(tool) !== tool) fail('windows_signtool_required');
  if (typeof thumbprint !== 'string' || !/^[A-Fa-f0-9]{40}$/.test(thumbprint)) fail('windows_certificate_required');
  const policy = windowsPolicy();
  for (const path of Object.values(executables)) {
    run(tool, ['sign', '/fd', 'SHA256', '/td', 'SHA256', '/tr', policy.timestamp_url, '/sha1', thumbprint, path]);
    run(tool, ['verify', '/pa', '/all', '/v', path]);
  }
  return policy;
}

function verificationPolicy({ mode, target }, executables, work) {
  if (mode === 'fixture') return Object.freeze({ fixture_id: `pr-${target.target_id}`, kind: 'fixture', production: false });
  if (target.platform === 'darwin') return signAndSubmitApple(executables, work);
  if (target.platform === 'win32') return signWindows(executables);
  return Object.freeze({ kind: 'linux', policy: 'signed-catalog-tree-v1' });
}

async function buildSelectedTarget(options) {
  const target = releaseTargetDefinition(options.targetID);
  if (options.mode === 'production') requireProductionAuthority(options.targetID);
  mkdirSync(dirname(options.outputRoot), { recursive: true, mode: 0o700 });
  mkdirSync(options.outputRoot, { mode: 0o700 });
  const work = mkdtempSync(join(tmpdir(), 'pulse-target-release-'));
  try {
    const staging = Object.freeze({ daemon: join(work, 'daemon'), embedder: join(work, 'embedder-runtime') });
    mkdirSync(staging.daemon, { recursive: true, mode: 0o700 });
    mkdirSync(staging.embedder, { recursive: true, mode: 0o700 });
    buildDaemonTarget({ appRoot, outputRoot: staging.daemon, targetID: options.targetID });
    buildPortableEmbedderRuntime({
      fixture: options.mode === 'fixture',
      outputRoot: staging.embedder,
      platform: target.platform,
      runnerInput: options.runnerInput,
      sourceRoot: portableEmbedderRoot,
      targetRuntimeRoot: options.targetRuntimeRoot,
    });
    const policy = verificationPolicy({ mode: options.mode, target }, executablePaths(staging, target), work);
    const artifacts = {};
    for (const [kind, root] of Object.entries(staging)) {
      const artifactKind = kind === 'embedder' ? 'embedder-runtime' : kind;
      const prepared = prepareNormalizedArtifact(root);
      const filename = `${artifactKind}.tar.gz`;
      const carrier = await packNormalizedArtifact(root, join(options.outputRoot, filename));
      artifacts[artifactKind] = Object.freeze({
        ...carrier,
        filename,
        tree_digest: prepared.tree_digest,
      });
    }
    const fragment = Object.freeze({
      artifacts: Object.freeze(artifacts),
      attestation_state: options.mode === 'fixture' ? 'fixture-only' : 'pending-signed-catalog-runtime-proof',
      production_ready: false,
      schema: 'pulse.target_release_build.v2',
      target: Object.freeze({
        architecture: target.architecture, libc: target.libc, platform: target.platform, target_id: target.target_id,
      }),
      verification_profile: policy,
    });
    writeFileSync(join(options.outputRoot, 'target-release-fragment.json'), `${JSON.stringify(fragment)}\n`, {
      encoding: 'utf8', flag: 'wx', mode: 0o600,
    });
    return fragment;
  } finally {
    rmSync(work, { recursive: true, force: true });
  }
}

const options = parseCLI(process.argv.slice(2));
if (options.check) {
  process.stdout.write(`${JSON.stringify({
    mode: options.mode,
    native_target: currentTargetID(),
    production_native_match: options.mode === 'production' && currentTargetID() === options.targetID,
    schema: 'pulse.target_release_builder_preflight.v2',
    target_id: options.targetID,
  })}\n`);
} else {
  const fragment = await buildSelectedTarget(options);
  process.stdout.write(`${JSON.stringify(fragment)}\n`);
}
