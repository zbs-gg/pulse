import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

import { normalizeCodexHook } from './host-adapter.js';
import { handleCodexHook } from './codex-hooks.js';

const base = {
  session_id: '019f5fc4-fea2-7142-90de-691158b1052d',
  turn_id: 'turn-42',
  cwd: '/workspace/pulse',
  hook_event_name: 'UserPromptSubmit',
  model: 'gpt-5.6-sol',
  permission_mode: 'default',
};

const resolved = {
  binding: {
    binding_digest: 'a'.repeat(64), resolver_epoch: 7,
    workspace: { workspace_id: 'workspace-pulse', repository_id: 'repository-pulse', canonical_path: '/workspace/pulse' },
  },
  runtime: { data_dir: '/pulse/personal', base_url: 'http://127.0.0.1:18801' },
};

test('Codex prompt recall is transient, bounded context and the turn lease contains no prompt', async () => {
  const written = [];
  const input = { ...base, prompt: 'What did we decide about invisible memory?' };
  const output = await handleCodexHook('UserPromptSubmit', input, {
    resolveRuntime: () => resolved,
    writeTurnContext: (_runtime, event) => written.push(event),
    composePromptMemory: async (_runtime, prompt) => {
      assert.equal(prompt, input.prompt);
      return 'Pulse memory (local; use only when relevant):\n- Keep memory invisible.';
    },
  });
  const context = output.hookSpecificOutput.additionalContext;
  assert.match(context, /Keep memory invisible/);
  assert.match(context, /pulse_memory/);
  assert.doesNotMatch(JSON.stringify(written), /What did we decide/);
  assert.equal(Buffer.byteLength(context) < 3000, true);
});

test('Codex prompt recall fails open and SessionStart adds no generic memory package', async () => {
  const prompt = await handleCodexHook('UserPromptSubmit', { ...base, prompt: 'ordinary work' }, {
    resolveRuntime: () => resolved,
    writeTurnContext: () => {},
    composePromptMemory: async () => { throw new Error('offline'); },
  });
  assert.match(prompt.hookSpecificOutput.additionalContext, /pulse_memory/);
  assert.doesNotMatch(prompt.hookSpecificOutput.additionalContext, /degraded|offline/i);
  const session = await handleCodexHook('SessionStart', {
    ...base, hook_event_name: 'SessionStart', turn_id: undefined, source: 'startup',
  }, { resolveRuntime: () => resolved });
  assert.deepEqual(session, { continue: true });
});

test('Codex consults Pulse authority only for pulse_memory and mints one exact lease', async () => {
  let resolvedCount = 0;
  const ordinary = await handleCodexHook('PreToolUse', {
    ...base, hook_event_name: 'PreToolUse', tool_name: 'Bash', tool_input: { command: 'pwd' }, tool_use_id: 'tool-1',
  }, { resolveRuntime: () => { resolvedCount++; return resolved; } });
  assert.deepEqual(ordinary, {});
  assert.equal(resolvedCount, 0);

  const leases = [];
  const memoryInput = { items: [{ kind: 'decision', scope: 'project', summary: 'Use one memory tool.' }] };
  const memory = await handleCodexHook('PreToolUse', {
    ...base, hook_event_name: 'PreToolUse', tool_name: 'mcp__pulse__pulse_memory',
    tool_input: memoryInput, tool_use_id: 'tool-memory-1',
  }, {
    resolveRuntime: () => resolved,
    readTurnContext: () => ({}),
    writeToolLease: (...args) => leases.push(args),
  });
  assert.deepEqual(memory, {});
  assert.equal(leases.length, 1);
  assert.equal(leases[0][2], 'mcp__pulse__pulse_memory');
  assert.deepEqual(leases[0][3], memoryInput);
});

test('Codex PostToolUse trusts the local marker and Stop never finalizes or restarts', async () => {
  let markerReads = 0;
  const post = await handleCodexHook('PostToolUse', {
    ...base, hook_event_name: 'PostToolUse', tool_name: 'mcp__pulse__pulse_memory', tool_response: {},
  }, {
    resolveRuntime: () => resolved,
    readFinalizeMarker: () => { markerReads++; return { ledger_id: 'turn-1' }; },
  });
  assert.deepEqual(post, {});
  assert.equal(markerReads, 1);
  let requests = 0;
  const stop = await handleCodexHook('Stop', {
    ...base, hook_event_name: 'Stop', status: 'completed', stop_hook_active: false,
  }, { resolveRuntime: () => resolved, request: async () => { requests++; } });
  assert.deepEqual(stop, {});
  assert.equal(requests, 0);
});

test('installed Codex hook surface has no SessionStart, compaction, subagent, or Stop hooks', () => {
  const root = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..', '..');
  const config = JSON.parse(readFileSync(resolve(root, 'plugins/pulse/hooks/hooks.json'), 'utf8'));
  assert.deepEqual(Object.keys(config.hooks).sort(), ['PostToolUse', 'PreToolUse', 'UserPromptSubmit']);
  assert.match(config.hooks.PreToolUse[0].matcher, /pulse_memory/);
  assert.match(config.hooks.PostToolUse[0].matcher, /pulse_memory/);
  for (const matchers of Object.values(config.hooks)) {
    for (const matcher of matchers) {
      for (const handler of matcher.hooks) {
        assert.doesNotMatch(handler.command, /CLAUDE_PLUGIN_ROOT/);
        assert.match(handler.command, /\$\{PLUGIN_ROOT\}\/hooks\/pulse-hook\.mjs/);
        assert.match(handler.command, /\$\{PLUGIN_ROOT\}\/hooks\/pulse-hook\.mjs/);
      }
    }
  }
});

test('Codex normalization discards raw prompt and transcript', () => {
  const event = normalizeCodexHook('UserPromptSubmit', {
    ...base, prompt: 'private wording', transcript_path: '/private/transcript.jsonl',
  });
  assert.doesNotMatch(JSON.stringify(event), /private wording|transcript/);
});
