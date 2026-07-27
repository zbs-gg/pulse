import assert from 'node:assert/strict';
import test from 'node:test';

import { prewarmHookWorker, runHookWorkerClient } from '../../../plugins/pulse/hook-worker-client.mjs';

const digest = 'a'.repeat(64);
const input = { cwd: '/workspace/pulse', session_id: 'session-one' };
const workspace = { digest, workspacePath: input.cwd };
const receipt = { generation: 'existing' };

function clientServices(overrides = {}) {
  return {
    readHookInput: async () => input,
    workerWorkspace: () => workspace,
    workerReceiptPath: () => '/private/worker.json',
    privateReceipt: () => receipt,
    validReceipt: () => true,
    dispatchWorker: async () => '{"continue":true}\n',
    ensureWorker: async () => ({ generation: 'replacement' }),
    writeOutput: async () => {},
    ...overrides,
  };
}

test('SessionStart reuses one live exact worker generation without repeating the full tree proof', async () => {
  let environmentResolutions = 0;
  let dispatchedReceipt;
  await runHookWorkerClient({
    host: 'codex',
    eventName: 'SessionStart',
    pluginRoot: '/signed/plugin',
    resolveEnvironment: async () => { environmentResolutions += 1; return {}; },
    services: clientServices({
      dispatchWorker: async (selected) => {
        dispatchedReceipt = selected;
        return '{"continue":true}\n';
      },
    }),
  });
  assert.equal(dispatchedReceipt, receipt);
  assert.equal(environmentResolutions, 0);
});

test('an unavailable worker falls back to a full environment proof and a replacement generation', async () => {
  let environmentResolutions = 0;
  const dispatched = [];
  await runHookWorkerClient({
    host: 'codex',
    eventName: 'SessionStart',
    pluginRoot: '/signed/plugin',
    resolveEnvironment: async () => { environmentResolutions += 1; return { exact: true }; },
    services: clientServices({
      dispatchWorker: async (selected) => {
        dispatched.push(selected.generation);
        if (selected === receipt) {
          const error = new Error('hook_worker_unavailable');
          error.code = 'hook_worker_unavailable';
          throw error;
        }
        return '{}\n';
      },
    }),
  });
  assert.deepEqual(dispatched, ['existing', 'replacement']);
  assert.equal(environmentResolutions, 1);
});

test('worker execution failures stay fail-closed without spawning a replacement generation', async () => {
  let environmentResolutions = 0;
  let replacements = 0;
  await assert.rejects(runHookWorkerClient({
    host: 'codex',
    eventName: 'SessionStart',
    pluginRoot: '/signed/plugin',
    resolveEnvironment: async () => { environmentResolutions += 1; return {}; },
    services: clientServices({
      dispatchWorker: async () => {
        const error = new Error('hook_worker_execution_failed');
        error.code = 'hook_worker_execution_failed';
        throw error;
      },
      ensureWorker: async () => { replacements += 1; return receipt; },
    }),
  }), /hook_worker_execution_failed/);
  assert.equal(environmentResolutions, 0);
  assert.equal(replacements, 0);
});

test('install prewarm proves the exact environment before reusing or starting a worker', async () => {
  let environmentResolutions = 0;
  let replacements = 0;
  const expected = {
    entrypointPath: '/private/runtime/entrypoint.mjs',
    hookDigest: 'b'.repeat(64),
    productEnvironment: {
      PULSE_PLUGIN_TREE_DIGEST: 'c'.repeat(64),
      PULSE_RUNTIME_DIGEST: 'd'.repeat(64),
    },
  };
  const result = await prewarmHookWorker({
    host: 'codex', pluginRoot: '/signed/plugin', workspacePath: '/workspace/pulse',
    resolveEnvironment: async () => { environmentResolutions += 1; return expected; },
    services: clientServices({
      validReceipt: (candidate, validation) => {
        assert.equal(validation.expected, expected);
        return candidate?.generation === 'replacement';
      },
      ensureWorker: async () => { replacements += 1; return { generation: 'replacement' }; },
    }),
  });
  assert.equal(environmentResolutions, 1);
  assert.equal(replacements, 1);
  assert.deepEqual(result, {
    schema: 'pulse.hook_worker_prewarm.v1', host: 'codex', workspace_digest: digest,
    hook_digest: expected.hookDigest,
    plugin_digest: expected.productEnvironment.PULSE_PLUGIN_TREE_DIGEST,
    runtime_digest: expected.productEnvironment.PULSE_RUNTIME_DIGEST,
    reused: false,
  });
});
