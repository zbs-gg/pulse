#!/usr/bin/env node

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, lstatSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { dirname, isAbsolute, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { currentNativeTargetID, loadNativeUniversalMatrix } from './native-universal-matrix.mjs';

const cliRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');

function argument(name) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : undefined;
}

function command(command, args, { env = process.env, timeout = 20 * 60_000 } = {}) {
  const result = spawnSync(command, args, {
    cwd: cliRoot, env, encoding: 'utf8', timeout,
    stdio: ['ignore', 'pipe', 'pipe'], maxBuffer: 64 * 1024 * 1024,
    shell: process.platform === 'win32' && command === 'codex',
  });
  if (result.status !== 0) {
    throw new Error(`${command} ${args.join(' ')} exited ${result.status}\n${result.stdout}\n${result.stderr}`);
  }
  return result.stdout.trim();
}

function exactOutputPath(value) {
  if (typeof value !== 'string' || value.length < 1 || isAbsolute(value)) {
    throw new Error('native universal evidence path must be relative to the package root');
  }
  const path = resolve(cliRoot, value);
  const local = relative(cliRoot, path);
  if (local === '..' || local.startsWith(`..${process.platform === 'win32' ? '\\' : '/'}`)) {
    throw new Error('native universal evidence path escapes the package root');
  }
  return path;
}

function nativeCodexExecutable(npmExecPath) {
  const globalRoot = command(process.execPath, [npmExecPath, 'root', '--global']);
  const packageRoot = join(globalRoot, '@openai', 'codex', 'node_modules', '@openai');
  const executableName = process.platform === 'win32' ? 'codex.exe' : 'codex';
  const matches = [];
  const visit = (path, depth = 0) => {
    if (depth > 8) return;
    for (const name of readdirSync(path)) {
      const child = join(path, name);
      const info = lstatSync(child);
      if (info.isSymbolicLink()) continue;
      if (info.isDirectory()) visit(child, depth + 1);
      else if (info.isFile() && name === executableName) matches.push(child);
    }
  };
  visit(packageRoot);
  assert.equal(matches.length, 1, `expected one native Codex executable under ${packageRoot}`);
  return resolve(matches[0]);
}

const matrix = loadNativeUniversalMatrix();
const requestedTarget = argument('--target');
const target = matrix.targets.find((value) => value.target_id === requestedTarget);
assert.equal(Boolean(target), true, 'native universal target is not declared');
assert.equal(currentNativeTargetID(), target.target_id);

const codexVersion = command('codex', ['--version']);
assert.match(codexVersion, new RegExp(`(?:^|\\s)${matrix.harness.version.replaceAll('.', '\\.')}$(?:|\\s)`));
const npmExecPath = process.env.npm_execpath;
if (!npmExecPath || !isAbsolute(npmExecPath)) throw new Error('run target proof through npm');
const codexExecutable = nativeCodexExecutable(npmExecPath);
const stdout = command(process.execPath, [npmExecPath, 'run', '--silent', 'test:personal-native-packed'], {
  env: {
    ...process.env,
    PULSE_NATIVE_PACKED_HOST: matrix.harness.host,
    PULSE_NATIVE_PACKED_CODEX_EXECUTABLE: codexExecutable,
  },
});
process.stdout.write(`${stdout}\n`);
const productReceipt = stdout.split(/\r?\n/).map((line) => {
  try { return JSON.parse(line); } catch { return null; }
}).find((value) => value?.schema === 'pulse.native_packed_product_fixture.v1');
assert.equal(productReceipt?.target_id, target.target_id);
assert.equal(productReceipt?.host, matrix.harness.host);
for (const field of [
  'exact_public_install_command', 'native_daemon', 'native_fixture_embedder', 'visible_memory_card',
  'first_memory_saved', 'fresh_session_context', 'host_observation', 'lifecycle_ready', 'repair_ready',
  'same_object_recalled',
]) assert.equal(productReceipt[field], true, field);
assert.equal(productReceipt.production_ready, false);
assert.equal(productReceipt.support_proven, false);
assert.equal(productReceipt.first_value_ms <= 60_000, true);
assert.match(productReceipt.packed_tarball_sha256, /^[a-f0-9]{64}$/);
assert.match(productReceipt.release_manifest_digest, /^[a-f0-9]{64}$/);
assert.equal(Array.isArray(productReceipt.release_artifact_ids), true);
assert.equal(productReceipt.release_artifact_ids.length, target.platform === 'darwin' ? 5 : 4);
for (const kind of ['daemon', 'embedder-runtime', 'model', 'plugin-runtime']) {
  assert.equal(productReceipt.release_artifact_ids.filter((id) => id.endsWith(`-${kind}`)).length, 1, kind);
}
assert.equal(
  productReceipt.release_artifact_ids.filter((id) => id.endsWith('-presence-helper')).length,
  target.platform === 'darwin' ? 1 : 0,
);
assert.equal(['collecting_baseline', 'estimated', 'measured'].includes(productReceipt.token_economy?.state), true);

const commit = process.env.GITHUB_SHA || command('git', ['rev-parse', 'HEAD']);
assert.match(commit, /^[a-f0-9]{40}$/);
const evidence = {
  schema: 'pulse.native_universal_target_evidence.v1',
  authority: 'pr-fixture',
  production: false,
  support_claim: false,
  commit,
  target,
  runner: {
    name: process.env.RUNNER_NAME || 'local',
    image: process.env.ImageOS || process.env.RUNNER_OS || process.platform,
  },
  harness: { host: matrix.harness.host, package: matrix.harness.package, version: matrix.harness.version },
  package: {
    sha256: productReceipt.packed_tarball_sha256,
    bytes: productReceipt.packed_tarball_bytes,
  },
  release: {
    manifest_digest: productReceipt.release_manifest_digest,
    artifact_ids: productReceipt.release_artifact_ids,
  },
  runtime: {
    store_id: productReceipt.store_id,
    full_retrieval: productReceipt.native_fixture_embedder,
    lifecycle_ready: productReceipt.lifecycle_ready,
  },
  first_value: {
    milliseconds: productReceipt.first_value_ms,
    visible_card: productReceipt.visible_memory_card,
    same_object_recalled: productReceipt.same_object_recalled,
    object_id: productReceipt.canonical_object_id,
  },
  token_economy: productReceipt.token_economy,
};
const output = exactOutputPath(argument('--out'));
mkdirSync(dirname(output), { recursive: true, mode: 0o700 });
writeFileSync(output, `${JSON.stringify(evidence)}\n`, { mode: 0o600 });
chmodSync(output, 0o600);
assert.deepEqual(JSON.parse(readFileSync(output, 'utf8')), evidence);
process.stdout.write(`${JSON.stringify(evidence)}\n`);
