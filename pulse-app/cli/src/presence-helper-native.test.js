import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import test from 'node:test';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const helper = resolve(
  dirname(fileURLToPath(import.meta.url)),
  '..', '..', 'native', 'pulse-presence-helper', 'dist', 'gg.zbs.pulse.presence-helper',
);

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

test('shipped native helper exposes the exact non-mutating capability contract', {
  skip: process.platform !== 'darwin' || !existsSync(helper),
}, () => {
  assert.deepEqual(run('contract'), {
    capabilities: [
      'dpop-create', 'dpop-delete', 'dpop-proof', 'dpop-public',
      'prove', 'public-key', 'self-test', 'sign-binding-registry',
    ],
    schema: 'pulse.presence_helper.contract.v1',
    version: 2,
  });
});

test('shipped native helper passes DER-to-P1363 known-answer and malformed vectors without Keychain access', {
  skip: process.platform !== 'darwin' || !existsSync(helper),
}, () => {
  assert.deepEqual(run('self-test'), {
    schema: 'pulse.presence_helper.self_test.v1',
    status: 'pass',
    vectors: 13,
  });
});
