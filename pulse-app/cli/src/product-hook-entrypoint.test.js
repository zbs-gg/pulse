import assert from 'node:assert/strict';
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
