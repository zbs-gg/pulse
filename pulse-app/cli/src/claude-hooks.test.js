import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

import { normalizeClaudeHook } from './host-adapter.js';
import { handleClaudeHook } from './claude-hooks.js';

const base = {
  session_id: '019f-cld-session-01', prompt_id: '019f-cld-prompt-01',
  cwd: '/workspace/pulse', hook_event_name: 'UserPromptSubmit', permission_mode: 'default',
};
const resolved = {
  binding: {
    binding_digest: 'b'.repeat(64), resolver_epoch: 8,
    workspace: { workspace_id: 'workspace-pulse', repository_id: 'repository-pulse', canonical_path: '/workspace/pulse' },
  },
  runtime: { data_dir: '/pulse/personal', base_url: 'http://127.0.0.1:18801' },
};

test('Claude prompt recall is automatic while raw prompt stays out of the turn lease', async () => {
  const written = [];
  const input = { ...base, prompt: 'What did we decide without repeating the phrase?' };
  const output = await handleClaudeHook('UserPromptSubmit', input, {
    resolveRuntime: () => resolved,
    writeTurnContext: (_runtime, event) => written.push(event),
    composePromptMemory: async (_runtime, prompt) => {
      assert.equal(prompt, input.prompt);
      return 'Pulse memory (local; use only when relevant):\n- Memory should stay invisible.';
    },
  });
  assert.match(output.hookSpecificOutput.additionalContext, /Memory should stay invisible/);
  assert.match(output.hookSpecificOutput.additionalContext, /pulse_memory/);
  assert.doesNotMatch(JSON.stringify(written), /What did we decide/);
});

test('Claude SessionStart and Stop are no-op and never request a second pass', async () => {
  let requests = 0;
  const deps = { resolveRuntime: () => resolved, request: async () => { requests++; } };
  const session = await handleClaudeHook('SessionStart', {
    session_id: base.session_id, cwd: '/workspace/pulse', hook_event_name: 'SessionStart', source: 'startup',
  }, deps);
  assert.deepEqual(session, { continue: true });
  const stop = await handleClaudeHook('Stop', {
    ...base, hook_event_name: 'Stop', stop_hook_active: false, background_tasks: ['still-running'],
  }, deps);
  assert.deepEqual(stop, {});
  assert.equal(requests, 0);
});

test('Claude runs authority only for pulse_memory and mints its exact lease', async () => {
  let resolvedCount = 0;
  const ordinary = await handleClaudeHook('PreToolUse', {
    ...base, hook_event_name: 'PreToolUse', tool_name: 'Bash', tool_input: { command: 'pwd' }, tool_use_id: 'tool-1',
  }, { resolveRuntime: () => { resolvedCount++; return resolved; } });
  assert.deepEqual(ordinary, {});
  assert.equal(resolvedCount, 0);

  const leases = [];
  const toolInput = { items: [{ kind: 'preference', scope: 'personal', summary: 'Keep reports compact.' }] };
  const result = await handleClaudeHook('PreToolUse', {
    ...base, hook_event_name: 'PreToolUse', tool_name: 'mcp__plugin_pulse_pulse-product__pulse_memory',
    tool_input: toolInput, tool_use_id: 'tool-memory',
  }, {
    resolveRuntime: () => resolved, readTurnContext: () => ({}),
    writeToolLease: (...args) => leases.push(args),
  });
  assert.deepEqual(result, {});
  assert.equal(leases.length, 1);
  assert.deepEqual(leases[0][4], toolInput);
});

test('Claude PostToolUse uses the host marker and stays silent', async () => {
  let reads = 0;
  const output = await handleClaudeHook('PostToolUse', {
    ...base, hook_event_name: 'PostToolUse', tool_name: 'mcp__pulse-product__pulse_memory', tool_response: {},
  }, {
    resolveRuntime: () => resolved,
    readFinalizeMarker: () => { reads++; return { ledger_id: 'turn-1' }; },
  });
  assert.deepEqual(output, {});
  assert.equal(reads, 1);
});

test('installed Claude hooks contain only prompt recall and pulse_memory lease/receipt', () => {
  const root = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..', '..');
  const config = JSON.parse(readFileSync(resolve(root, 'plugins/pulse/claude-hooks/hooks.json'), 'utf8'));
  assert.deepEqual(Object.keys(config.hooks).sort(), ['PostToolUse', 'PreToolUse', 'UserPromptSubmit']);
  assert.match(config.hooks.PreToolUse[0].matcher, /mcp__plugin_pulse_pulse-product__pulse_memory/);
  assert.match(config.hooks.PostToolUse[0].matcher, /mcp__plugin_pulse_pulse-product__pulse_memory/);
});

test('Claude normalization discards prompt and transcript content', () => {
  const event = normalizeClaudeHook('UserPromptSubmit', {
    ...base, prompt: 'raw private prompt', transcript_path: '/private/claude/transcript.jsonl',
  });
  assert.equal(event.turn_id, base.prompt_id);
  assert.doesNotMatch(JSON.stringify(event), /raw private prompt|transcript/);
});
