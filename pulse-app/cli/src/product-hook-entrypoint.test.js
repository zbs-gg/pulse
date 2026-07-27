import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { __productHookEntrypointTest, runProductHookEntrypoint } from './product-hook-entrypoint.js';

test('product hook entrypoint recovers production and isolated test authority exactly once', async () => {
  const calls = [];
  await __productHookEntrypointTest.recoverProductBindingAuthority({}, {
    recoverBinding: async (options) => calls.push(options),
  });
  const testEnvironment = {
    PULSE_TRUST_MODE: 'test',
    PULSE_BINDING_REGISTRY_PATH: '/fixture/bindings.json',
    PULSE_BINDING_PUBLIC_KEY_PATH: '/fixture/bindings.pub',
    PULSE_BINDING_ANCHOR_PATH: '/fixture/anchor.json',
  };
  await __productHookEntrypointTest.recoverProductBindingAuthority(testEnvironment, {
    recoverBinding: async (options) => calls.push(options),
  });
  assert.deepEqual(calls, [
    undefined,
    {
      registryPath: '/fixture/bindings.json',
      publicKeyPath: '/fixture/bindings.pub',
      anchorPath: '/fixture/anchor.json',
      rootPublicKey: false,
      rootAnchor: false,
    },
  ]);
});

test('binding transaction recovery runs only at the SessionStart trust boundary', () => {
  assert.equal(__productHookEntrypointTest.requiresProductBindingRecovery('SessionStart'), true);
  assert.equal(__productHookEntrypointTest.requiresProductBindingRecovery('sessionStart'), true);
  for (const eventName of ['UserPromptSubmit', 'PreToolUse', 'PostToolUse', 'Stop']) {
    assert.equal(__productHookEntrypointTest.requiresProductBindingRecovery(eventName), false);
  }
});

test('synthetic worker diagnostics expose only stable content-free error codes', () => {
  assert.equal(__productHookEntrypointTest.syntheticHookDiagnostic({ code: 'capture_state_unsafe' }), 'capture_state_unsafe');
  assert.equal(__productHookEntrypointTest.syntheticHookDiagnostic(new Error('pulse_response_invalid')), 'pulse_response_invalid');
  assert.equal(__productHookEntrypointTest.syntheticHookDiagnostic(new Error('/private/path leaked')), 'hook_failure_unclassified');
});

test('hook worker lease watches resolver inputs but not mutable runtime outputs', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-hook-entrypoint-witnesses.'));
  try {
    const dataDir = join(root, 'data');
    const runtimeDir = join(dataDir, 'runtime');
    mkdirSync(runtimeDir, { recursive: true });
    const authority = ['bindings.json', 'bindings.pub', 'bindings.anchor']
      .map((name) => join(root, name));
    const capture = join(dataDir, 'capture-state.json');
    const daemon = join(runtimeDir, 'product-daemon.json');
    const secret = join(dataDir, 'secret.key');
    const pid = join(runtimeDir, 'pulse.pid');
    for (const path of [...authority, capture, daemon, secret, pid]) writeFileSync(path, '{}\n');

    const witnesses = __productHookEntrypointTest.productHookWitnessPaths({
      runtime: { data_dir: dataDir, pid_file: pid },
    }, {
      PULSE_TRUST_MODE: 'test',
      PULSE_BINDING_REGISTRY_PATH: authority[0],
      PULSE_BINDING_PUBLIC_KEY_PATH: authority[1],
      PULSE_BINDING_ANCHOR_PATH: authority[2],
    });
    assert.deepEqual(witnesses, [...authority, capture]);
    assert.equal(witnesses.includes(daemon), false);
    assert.equal(witnesses.includes(secret), false);
    assert.equal(witnesses.includes(pid), false);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('worker entrypoint accepts filesystem aliases only after canonical identity matches', () => {
  const alias = '/var/folders/fixture/product-hook-entrypoint.bundle.js';
  const canonical = '/private/var/folders/fixture/product-hook-entrypoint.bundle.js';
  const realpath = (path) => path === alias ? canonical : path;
  assert.equal(__productHookEntrypointTest.invokedAsMain(
    alias, `file://${canonical}`, { realpath },
  ), true);
  assert.equal(__productHookEntrypointTest.invokedAsMain(
    '/var/folders/fixture/other.js', `file://${canonical}`, { realpath },
  ), false);
});

test('product hook entrypoint rejects an unknown host before authority or runtime work', async () => {
  let recovered = false;
  await assert.rejects(
    runProductHookEntrypoint('unknown', 'SessionStart', {
      recoverBinding: async () => { recovered = true; },
    }),
    /entrypoint is invalid/,
  );
  assert.equal(recovered, false);
});
