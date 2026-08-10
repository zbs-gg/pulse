import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

import { normalizeCursorHook } from './host-adapter.js';
import { handleCursorHook } from './cursor-hooks.js';

const base = {
  conversation_id: 'cursor-session-01', generation_id: 'cursor-generation-01',
  cwd: '/workspace/pulse', model: 'cursor-model',
};
const resolved = {
  binding: {
    binding_digest: 'c'.repeat(64), resolver_epoch: 9,
    workspace: { workspace_id: 'workspace-pulse', repository_id: 'repository-pulse', canonical_path: '/workspace/pulse' },
  },
  runtime: { data_dir: '/pulse/personal', base_url: 'http://127.0.0.1:18801' },
};

test('Cursor beforeSubmitPrompt creates a turn lease without pretending to inject context', async () => {
  const written = [];
  const input = { ...base, prompt: 'Which old decision matters here?' };
  const output = await handleCursorHook('beforeSubmitPrompt', input, {
    resolveRuntime: () => resolved,
    writeTurnContext: (_runtime, event) => written.push(event),
  });
  assert.deepEqual(output, { continue: true });
  assert.doesNotMatch(JSON.stringify(written), /Which old decision/);
});

test('Cursor SessionStart and stop add no package or follow-up', async () => {
  let requests = 0;
  const deps = { resolveRuntime: () => resolved, request: async () => { requests++; } };
  const session = await handleCursorHook('sessionStart', {
    session_id: base.conversation_id, workspace_roots: ['/workspace/pulse'], composer_mode: 'agent',
  }, deps);
  assert.deepEqual(session, {});
  const stop = await handleCursorHook('stop', { ...base, status: 'completed', loop_count: 0 }, deps);
  assert.deepEqual(stop, {});
  assert.equal(requests, 0);
});

test('Cursor ordinary tools fail open; pulse_memory gets one exact lease', async () => {
  let resolvedCount = 0;
  const ordinary = await handleCursorHook('preToolUse', {
    ...base, tool_name: 'Shell', tool_input: { command: 'pwd' }, tool_use_id: 'tool-1',
  }, { resolveRuntime: () => { resolvedCount++; return resolved; } });
  assert.deepEqual(ordinary, { permission: 'allow' });
  assert.equal(resolvedCount, 0);

  const leases = [];
  const toolInput = { items: [{ kind: 'project_state', scope: 'project', summary: 'Acceptance is pending.' }] };
  const memory = await handleCursorHook('preToolUse', {
    ...base, tool_name: 'mcp__pulse-product__pulse_memory', tool_input: toolInput, tool_use_id: 'tool-memory',
  }, {
    resolveRuntime: () => resolved, readTurnContext: () => ({}),
    writeToolLease: (...args) => leases.push(args),
  });
  assert.deepEqual(memory, { permission: 'allow' });
  assert.equal(leases.length, 1);
  assert.deepEqual(leases[0][4], toolInput);
});

test('Cursor Pulse lookup stays fail-open when its turn lease is unavailable', async () => {
  const output = await handleCursorHook('preToolUse', {
    ...base, tool_name: 'mcp__pulse-product__pulse_memory',
    tool_input: { query: 'What matters from earlier work?' }, tool_use_id: 'tool-memory',
  }, {
    resolveRuntime: () => { throw new Error('Pulse stopped'); },
  });
  assert.deepEqual(output, { permission: 'allow' });
});

test('Cursor PostToolUse stays silent after validating the finalize marker', async () => {
  let reads = 0;
  const output = await handleCursorHook('postToolUse', {
    ...base, tool_name: 'mcp__pulse-product__pulse_memory', tool_output: {},
  }, {
    resolveRuntime: () => resolved,
    readFinalizeMarker: () => { reads++; return { ledger_id: 'turn-1' }; },
  });
  assert.deepEqual(output, {});
  assert.equal(reads, 1);
});

test('Cursor PostToolUse records one successful automatic recall without a finalize marker', async () => {
  const lifecycle = [];
  const output = await handleCursorHook('postToolUse', {
    ...base, tool_name: 'mcp__pulse-product__pulse_memory',
    tool_input: { query: 'How should emotion relate to a decision?' }, tool_output: { status: 'recalled' },
  }, {
    resolveRuntime: () => resolved,
    recordLifecycle: (_runtime, event) => lifecycle.push(event),
    readFinalizeMarker: () => { throw new Error('recall must not require a write marker'); },
  });
  assert.deepEqual(output, {});
  assert.deepEqual(lifecycle, ['prompt_recall']);
});

test('Cursor failed automatic recall does not satisfy Doctor readiness', async () => {
  const lifecycle = [];
  const output = await handleCursorHook('postToolUse', {
    ...base, tool_name: 'mcp__pulse-product__pulse_memory',
    tool_input: { query: 'What matters from earlier work?' }, tool_output: { isError: true },
  }, {
    resolveRuntime: () => resolved,
    recordLifecycle: (_runtime, event) => lifecycle.push(event),
  });
  assert.deepEqual(output, {});
  assert.deepEqual(lifecycle, []);
});

test('installed Cursor hooks omit sessionStart, compaction, and stop', () => {
  const root = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..', '..');
  const config = JSON.parse(readFileSync(resolve(root, 'plugins/pulse/cursor-hooks/hooks.json'), 'utf8'));
  assert.deepEqual(Object.keys(config.hooks).sort(), ['beforeSubmitPrompt', 'postToolUse', 'preToolUse']);
});

test('Cursor normalization discards raw prompt and transcript', () => {
  const event = normalizeCursorHook('beforeSubmitPrompt', {
    ...base, prompt: 'raw private prompt', transcript_path: '/private/cursor/transcript.jsonl',
  });
  assert.doesNotMatch(JSON.stringify(event), /raw private prompt|transcript/);
});
