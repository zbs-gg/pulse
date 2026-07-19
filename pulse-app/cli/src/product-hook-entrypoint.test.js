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
  for (const eventName of ['UserPromptSubmit', 'PreToolUse', 'PostToolUse', 'Stop']) {
    assert.equal(__productHookEntrypointTest.requiresProductBindingRecovery(eventName), false);
  }
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
