import assert from 'node:assert/strict';
import { Readable } from 'node:stream';
import test from 'node:test';

import { productHookHost, runProductHookCLI } from './product-hook-dispatch.js';

test('product hook routes Codex by its native turn id even when Claude compatibility environment exists', async () => {
  const input = { turn_id: 'turn_123', prompt_id: undefined };
  let selected = '';
  await runProductHookCLI('UserPromptSubmit', {
    input,
    environment: { CLAUDE_PLUGIN_ROOT: '/compatibility/path' },
    runCodex: async (eventName, dependencies) => {
      selected = `${eventName}:${dependencies.input.turn_id}`;
    },
    runClaude: async () => { throw new Error('wrong_host'); },
  });
  assert.equal(selected, 'UserPromptSubmit:turn_123');
});

test('product hook routes Claude Code by its native prompt id', async () => {
  const input = { prompt_id: 'prompt_123' };
  let selected = '';
  await runProductHookCLI('UserPromptSubmit', {
    input,
    runCodex: async () => { throw new Error('wrong_host'); },
    runClaude: async (eventName, dependencies) => {
      selected = `${eventName}:${dependencies.input.prompt_id}`;
    },
  });
  assert.equal(selected, 'UserPromptSubmit:prompt_123');
});

test('product hook rejects ambiguous host payloads', () => {
  assert.throws(() => productHookHost({ turn_id: 'turn', prompt_id: 'prompt' }), /host_ambiguous/);
  assert.throws(() => productHookHost({}), /host_ambiguous/);
});

test('product hook bounds native host input', async () => {
  await assert.rejects(
    runProductHookCLI('UserPromptSubmit', {
      inputStream: Readable.from([Buffer.alloc((1 << 20) + 1)]),
      runCodex: async () => {},
      runClaude: async () => {},
    }),
    /input_too_large/,
  );
});

