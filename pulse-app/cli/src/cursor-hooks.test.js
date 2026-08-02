import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { normalizeCursorHook } from './host-adapter.js';
import {
  flushCursorHookOutput,
  handleCursorHook,
  inspectCursorLifecycleReadiness,
  recordCursorLifecycleReadiness,
} from './cursor-hooks.js';

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
  let delivery;
  const output = await handleCursorHook('sessionStart', {
    session_id: base.conversation_id, workspace_roots: ['/workspace/pulse'], composer_mode: 'agent',
  }, {
    resolveRuntime: () => resolved,
    request: async (_runtime, path, options) => {
      calls.push({ path, options });
      return {
        resume_markdown: 'Decision created in Codex is available in Cursor.',
        included_object_ids: ['memory_shared_01'],
        included_evidence_ids: ['pulse:memory_shared_01'],
      };
    },
    now: () => new Date('2026-07-16T10:00:00Z'),
  });
  await flushCursorHookOutput({}, output, {
    recordDelivery: async (offer) => { delivery = offer; },
    writeOutput: async () => {},
  });
  assert.equal(calls[0].path, '/continuity/resume');
  assert.equal(calls[0].options.body.host, 'cursor');
  assert.match(output.additional_context, /Decision created in Codex is available in Cursor/);
  assert.match(output.additional_context, /pulse.context_lease.v2/);
  assert.deepEqual(delivery.object_ids, ['memory_shared_01']);
  assert.equal(delivery.host, 'cursor');
});

test('Cursor beforeSubmitPrompt binds the turn without persisting prompt content', async () => {
  const written = [];
  let observations = 0;
  const output = await handleCursorHook('beforeSubmitPrompt', {
    ...base, prompt: 'private prompt that must not persist',
  }, {
    resolveRuntime: () => resolved,
    writeTurnContext: (_runtime, event, host) => written.push({ event, host }),
    observeDelivery: async () => { observations++; },
  });
  assert.deepEqual(output, { continue: true });
  assert.equal(written[0].host, 'cursor');
  assert.equal(written[0].event.source_event_key, stopEvent().source_event_key);
  assert.equal(observations, 1);
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
  await handleCursorHook('preToolUse', {
    ...base, tool_name: 'mcp__pulse__pulse_remember', tool_input: toolInput,
    tool_use_id: 'tool-legacy-memory',
  }, {
    resolveRuntime: () => resolved,
    readTurnContext: () => ({ binding_digest: resolved.binding.binding_digest }),
    request: async () => ({ capture_enabled: true }),
    writeToolLease: (...args) => leases.push(args),
  });
  assert.equal(leases.length, 1);
});

test('Cursor ordinary tools remain available when Pulse authority is unavailable', async () => {
  let resolvedRuntime = false;
  const output = await handleCursorHook('preToolUse', {
    ...base, tool_name: 'update_plan', tool_input: { plan: [] }, tool_use_id: 'tool-goal-control',
  }, {
    resolveRuntime: () => { resolvedRuntime = true; throw new Error('binding unavailable'); },
  });
  assert.deepEqual(output, { permission: 'allow' });
  assert.equal(resolvedRuntime, false);
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
    request: async () => ({ full_retrieval: true }),
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

test('Cursor stop never creates a follow-up when Pulse is unavailable', async () => {
  const output = await handleCursorHook('stop', { ...base, status: 'completed', loop_count: 0 }, {
    resolveRuntime: () => resolved,
    readFinalizeMarker: () => { throw new Error('not finalized'); },
    request: async () => { throw new Error('daemon unavailable'); },
  });
  assert.deepEqual(output, {});
});

test('Cursor lifecycle readiness is content-free, cumulative, and requires the capability floor', () => {
  const dataDir = mkdtempSync(join(tmpdir(), 'pulse-cursor-lifecycle.'));
  const local = { ...resolved, runtime: { ...resolved.runtime, data_dir: dataDir } };
  try {
    assert.equal(inspectCursorLifecycleReadiness(local).ready, false);
    for (const event of ['session_context', 'turn_capture', 'write_receipt', 'finalize']) {
      recordCursorLifecycleReadiness(local, event, new Date('2026-07-17T10:00:00Z'));
    }
    const repeated = recordCursorLifecycleReadiness(local, 'turn_capture', new Date('2026-07-17T11:00:00Z'));
    assert.equal(repeated.updated_at, '2026-07-17T10:00:00.000Z');
    const readiness = inspectCursorLifecycleReadiness(local);
    assert.equal(readiness.ready, true);
    assert.deepEqual(readiness.observed, {
      session_context: true, turn_capture: true, write_receipt: true, finalize: true,
    });
    assert.doesNotMatch(JSON.stringify(readiness), /cursor-session|prompt|transcript|\/workspace/);
  } finally {
    rmSync(dataDir, { recursive: true, force: true });
  }
});

test('concurrent Cursor hook processes preserve independent lifecycle events', async () => {
  const dataDir = mkdtempSync(join(tmpdir(), 'pulse-cursor-lifecycle-race.'));
  const local = { ...resolved, runtime: { ...resolved.runtime, data_dir: dataDir } };
  const runner = `
    const { recordCursorLifecycleReadiness } = await import(process.env.PULSE_CURSOR_HOOKS_MODULE);
    recordCursorLifecycleReadiness(JSON.parse(process.env.PULSE_CURSOR_RESOLVED), process.env.PULSE_CURSOR_EVENT, new Date('2026-07-17T12:00:00Z'));
  `;
  const invoke = (event) => new Promise((resolveChild, rejectChild) => {
    const child = spawn(process.execPath, ['--input-type=module', '--eval', runner], {
      env: {
        ...process.env,
        PULSE_CURSOR_HOOKS_MODULE: new URL('./cursor-hooks.js', import.meta.url).href,
        PULSE_CURSOR_RESOLVED: JSON.stringify(local),
        PULSE_CURSOR_EVENT: event,
      },
      stdio: ['ignore', 'ignore', 'pipe'],
    });
    let stderr = '';
    child.stderr.setEncoding('utf8');
    child.stderr.on('data', (chunk) => { stderr += chunk; });
    child.once('error', rejectChild);
    child.once('exit', (status) => status === 0
      ? resolveChild()
      : rejectChild(new Error(`cursor lifecycle child exited ${status}: ${stderr}`)));
  });
  try {
    await Promise.all(['session_context', 'turn_capture', 'write_receipt', 'finalize'].map(invoke));
    assert.equal(inspectCursorLifecycleReadiness(local).ready, true);
  } finally {
    rmSync(dataDir, { recursive: true, force: true });
  }
});
