import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import test, { after, before } from 'node:test';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { EXPECTED_HELPER_SELF_TEST } from './trust-helper.js';

const helperRoot = resolve(
  dirname(fileURLToPath(import.meta.url)),
  '..', '..', 'native', 'pulse-presence-helper',
);
let buildRoot;
let helper;

before(() => {
  if (process.platform !== 'darwin') return;
  buildRoot = mkdtempSync(join(tmpdir(), 'pulse-presence-helper-test-'));
  helper = join(buildRoot, 'gg.zbs.pulse.presence-helper');
  const built = spawnSync('/usr/bin/swiftc', [
    '-parse-as-library',
    '-target', 'arm64-apple-macos13.0',
    join(helperRoot, 'main.swift'),
    '-o', helper,
    '-framework', 'AppKit',
    '-framework', 'LocalAuthentication',
    '-framework', 'Security',
    '-framework', 'CryptoKit',
  ], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 60_000 });
  assert.equal(built.status, 0, `${built.stdout}\n${built.stderr}`);
});

after(() => {
  if (buildRoot) rmSync(buildRoot, { recursive: true, force: true });
});

function run(command) {
  const result = spawnSync(helper, [command], {
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
    timeout: 5_000,
    maxBuffer: 16 * 1024,
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.signal, null);
  return JSON.parse(result.stdout);
}

test('native helper source builds and exposes the exact non-mutating capability contract', {
  skip: process.platform !== 'darwin',
}, () => {
  assert.deepEqual(run('contract'), {
    capabilities: [
      'dpop-create', 'dpop-delete', 'dpop-proof', 'dpop-public',
      'prove', 'public-key', 'self-test', 'sign-binding-registry',
    ],
    self_test: EXPECTED_HELPER_SELF_TEST,
    schema: 'pulse.presence_helper.contract.v1',
    version: 3,
  });
});

test('native helper source passes DER-to-P1363 known-answer and malformed vectors without Keychain access', {
  skip: process.platform !== 'darwin',
}, () => {
  assert.deepEqual(run('self-test'), {
    ...EXPECTED_HELPER_SELF_TEST,
    status: 'pass',
  });
});
