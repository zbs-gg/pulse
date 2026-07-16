import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import test from 'node:test';

import { normalizeCursorHook } from './host-adapter.js';
import { handleCursorHook } from './cursor-hooks.js';

const base = {
  conversation_id: 'cursor-session-01',
  generation_id: 'cursor-generation-01',
  cwd: '/workspace/pulse/subdir',
  model: 'cursor-model',
};

const resolved = {
  binding: {
    binding_id: 'binding-pulse', binding_digest: 'c'.repeat(64), resolver_epoch: 9,
    mode: 'personal',
    workspace: {
      workspace_id: 'workspace-pulse', repository_id: 'repository-pulse',
      canonical_path: '/workspace/pulse',
    },
  },
  runtime: {
    kind: 'personal', store_id: 'store_personal_nik', data_dir: '/pulse/personal',
    base_url: 'http://127.0.0.1:18801',
  },
};

function opaque(kind, value) {
  return `${kind}:${createHash('sha256').update(`${kind}\x1f${value}`).digest('hex')}`;
}

function stopEvent(input = base) {
  return normalizeCursorHook('stop', {
    ...input, cwd: resolved.binding.workspace.canonical_path, status: 'completed', loop_count: 0,
  });
}

test('Cursor sessionStart injects the same bound continuity contract without a second store', async () => {
  const calls = [];
  const output = await handleCursorHook('sessionStart', {
    session_id: base.conversation_id, workspace_roots: ['/workspace/pulse'], composer_mode: 'agent',
  }, {
    resolveRuntime: () => resolved,
    request: async (_runtime, path, options) => {
      calls.push({ path, options });
      return { resume_markdown: 'Decision created in Codex is available in Cursor.' };
    },
    now: () => new Date('2026-07-16T10:00:00Z'),
  });
  assert.equal(calls[0].path, '/continuity/resume');
  assert.equal(calls[0].options.body.host, 'cursor');
  assert.match(output.additional_context, /Decision created in Codex is available in Cursor/);
  assert.match(output.additional_context, /pulse.context_lease.v2/);
});

test('Cursor beforeSubmitPrompt binds the turn without persisting prompt content', async () => {
  const written = [];
  const output = await handleCursorHook('beforeSubmitPrompt', {
    ...base, prompt: 'private prompt that must not persist',
  }, {
    resolveRuntime: () => resolved,
    writeTurnContext: (_runtime, event, host) => written.push({ event, host }),
  });
  assert.deepEqual(output, { continue: true });
  assert.equal(written[0].host, 'cursor');
  assert.equal(written[0].event.source_event_key, stopEvent().source_event_key);
  assert.doesNotMatch(JSON.stringify(written), /private prompt/);
});

test('Cursor preToolUse mints an exact governed lease and blocks destructive Pulse calls', async () => {
  const denied = await handleCursorHook('preToolUse', {
    ...base, tool_name: 'mcp__pulse-product__pulse_wipe', tool_input: {}, tool_use_id: 'tool-wipe',
  });
  assert.equal(denied.permission, 'deny');

  const leases = [];
  const toolInput = { schema: 'pulse.memory_capsule.v1', items: [], raw_input_included: false };
  const allowed = await handleCursorHook('preToolUse', {
    ...base, tool_name: 'mcp__pulse-product__pulse_remember', tool_input: toolInput,
    tool_use_id: 'tool-memory',
  }, {
    resolveRuntime: () => resolved,
    readTurnContext: () => ({ binding_digest: resolved.binding.binding_digest }),
    request: async () => ({ capture_enabled: true }),
    writeToolLease: (...args) => leases.push(args),
  });
  assert.deepEqual(allowed, { permission: 'allow' });
  assert.equal(leases[0][2], 'cursor');
  assert.equal(leases[0][4], toolInput);
});

test('Cursor postToolUse injects only a daemon-corroborated Memory Tray receipt', async () => {
  const event = stopEvent();
  const receipt = {
    schema: 'pulse.write_receipt.v1', receipt_id: 'receipt-cursor-01', ledger_id: 'ledger-cursor-01',
    candidate_id: 'candidate-cursor-01', status: 'pending',
    safe_provenance: {
      host: 'cursor', session_id: opaque('session', event.session_id),
      turn_id: opaque('turn', event.turn_id), source_event_key: opaque('event', event.source_event_key),
    },
  };
  const output = await handleCursorHook('postToolUse', {
    ...base, tool_name: 'mcp__pulse-product__pulse_remember',
    tool_output: JSON.stringify(receipt), tool_use_id: 'tool-memory',
  }, {
    resolveRuntime: () => resolved,
    readFinalizeMarker: () => ({ ledger_id: receipt.ledger_id }),
    request: async () => receipt,
  });
  assert.match(output.additional_context, /receipt-cursor-01:pending/);
});

test('Cursor stop requests one bounded finalization pass then seals no-change', async () => {
  const requests = [];
  const first = await handleCursorHook('stop', { ...base, status: 'completed', loop_count: 0 }, {
    resolveRuntime: () => resolved,
    readFinalizeMarker: () => { throw new Error('not finalized'); },
  });
  assert.match(first.followup_message, /one bounded Pulse finalization pass/);

  const second = await handleCursorHook('stop', { ...base, status: 'completed', loop_count: 1 }, {
    resolveRuntime: () => resolved,
    readFinalizeMarker: () => { throw new Error('not finalized'); },
    request: async (_runtime, path, options) => { requests.push({ path, options }); return { status: 'no_change' }; },
  });
  assert.deepEqual(second, {});
  assert.equal(requests[0].path, '/turn/no-change');
  assert.equal(requests[0].options.body.host, 'cursor');
});
