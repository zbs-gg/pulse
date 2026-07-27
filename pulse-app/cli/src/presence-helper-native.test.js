import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
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

test('native helper permits the exact home.open user-presence action', () => {
  const source = readFileSync(join(helperRoot, 'main.swift'), 'utf8');
  assert.match(source, /"home\.open"/);
  assert.match(source, /setActivationPolicy\(\.regular\)/);
  assert.match(source, /makeKeyAndOrderFront\(nil\)/);
  assert.match(source, /applicationShouldTerminateAfterLastWindowClosed/);
  assert.match(source, /applicationShouldTerminate[\s\S]*?\.terminateCancel/);
  assert.match(source, /reviewInChild\(command: "review-action-internal"/);
  assert.match(source, /reviewInChild\(command: "review-binding-registry-internal"/);
});

test('native helper persists only an opaque Secure Enclave representation in private user state', () => {
  const source = readFileSync(join(helperRoot, 'main.swift'), 'utf8');
  const functionBody = source.match(
    /private func privateKey\(reason: String\) throws -> SecKey \{([\s\S]*?)\n\}/,
  )?.[1];
  assert.equal(functionBody, undefined, 'presence trust must not persist a SecKey in Keychain');
  assert.match(source, /SecureEnclave\.P256\.Signing\.PrivateKey/);
  assert.match(source, /key\.dataRepresentation/);
  assert.match(source, /O_WRONLY \| O_CREAT \| O_EXCL \| O_NOFOLLOW/);
  assert.match(source, /info\.st_nlink == 1/);
  assert.match(source, /\\n-----END PUBLIC KEY-----\\n/);
});

test('native helper source passes DER-to-P1363 known-answer and malformed vectors without Keychain access', {
  skip: process.platform !== 'darwin',
}, () => {
  assert.deepEqual(run('self-test'), {
    ...EXPECTED_HELPER_SELF_TEST,
    status: 'pass',
  });
});
