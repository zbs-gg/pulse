import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtempSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { normalizeClaudeHook } from './host-adapter.js';
import { handleClaudeHook, recordClaudeHookReadiness } from './claude-hooks.js';

const base = {
  session_id: '019f-cld-session-01',
  prompt_id: '019f-cld-prompt-01',
  transcript_path: '/private/claude/transcript.jsonl',
  cwd: '/workspace/pulse/subdir',
  hook_event_name: 'UserPromptSubmit',
  permission_mode: 'default',
};

const resolved = {
  binding: {
    binding_id: 'binding-pulse',
    binding_digest: 'b'.repeat(64),
    resolver_epoch: 8,
    mode: 'personal',
    workspace: {
      workspace_id: 'workspace-pulse',
      repository_id: 'repository-pulse',
      canonical_path: '/workspace/pulse',
    },
  },
  runtime: {
    kind: 'personal',
    store_id: 'store_personal_nik',
    data_dir: '/pulse/personal',
    base_url: 'http://127.0.0.1:18801',
  },
};

function opaque(kind, value) {
  return `${kind}:${createHash('sha256').update(`${kind}\x1f${value}`).digest('hex')}`;
}

function stopEvent(input = base) {
  return normalizeClaudeHook('Stop', {
    ...input,
    cwd: resolved.binding.workspace.canonical_path,
    hook_event_name: 'Stop',
    stop_hook_active: input.stop_hook_active ?? false,
  });
}

test('official Claude fixtures use prompt_id and discard prompt, transcript, and model absence', () => {
  const event = normalizeClaudeHook('UserPromptSubmit', {
    ...base,
    prompt: 'raw private prompt must never persist',
  });
  assert.equal(event.host, 'claude-code');
  assert.equal(event.turn_id, base.prompt_id);
  assert.equal(event.model, 'claude_model_unavailable');
  assert.doesNotMatch(JSON.stringify(event), /raw private prompt|transcript/);

  const session = normalizeClaudeHook('SessionStart', {
    session_id: base.session_id,
    transcript_path: base.transcript_path,
    cwd: '/workspace/pulse',
    hook_event_name: 'SessionStart',
    source: 'resume',
  });
  assert.match(session.turn_id, /^session_[a-f0-9]{64}$/);
  assert.equal(session.model, 'claude_model_unavailable');
});

test('Claude SessionStart injects the same bound continuity contract', async () => {
  const calls = [];
  const output = await handleClaudeHook('SessionStart', {
    session_id: base.session_id,
    transcript_path: base.transcript_path,
    cwd: '/workspace/pulse',
    hook_event_name: 'SessionStart',
    source: 'resume',
  }, {
    resolveRuntime: () => resolved,
    request: async (_runtime, path, options) => {
      calls.push({ path, options });
      return { resume_markdown: 'Codex decision available in Claude Code.' };
    },
    now: () => new Date('2026-07-14T10:00:00Z'),
  });
  assert.equal(calls[0].path, '/continuity/resume');
  assert.equal(calls[0].options.body.host, 'claude-code');
  assert.match(output.hookSpecificOutput.additionalContext, /Codex decision available/);
  assert.match(output.hookSpecificOutput.additionalContext, /pulse.context_lease.v1/);
	assert.match(output.hookSpecificOutput.additionalContext, /"practices":\[\]/);
	assert.match(output.hookSpecificOutput.additionalContext, /Pulse host rules \(host-owned\)/);
});

test('all Claude turn hooks bind to one canonical Stop identity at the trusted repo root', async () => {
  const written = [];
  const prompt = { ...base, prompt: 'private input' };
  await handleClaudeHook('UserPromptSubmit', prompt, {
    resolveRuntime: () => resolved,
    writeTurnContext: (_runtime, event, host) => written.push({ event, host }),
  });
  const expected = stopEvent(prompt);
  assert.equal(written[0].host, 'claude-code');
  assert.deepEqual(written[0].event, expected);
  assert.equal(written[0].event.workspace, '/workspace/pulse');
  assert.doesNotMatch(JSON.stringify(written), /private input|transcript/);

  let read;
  await handleClaudeHook('PreToolUse', {
    ...base,
    agent_id: 'subagent-1',
    cwd: '/workspace/pulse/another-subdir',
    hook_event_name: 'PreToolUse',
    tool_name: 'Bash',
    tool_input: { command: 'git status' },
    tool_use_id: 'tool-status',
  }, {
    resolveRuntime: () => resolved,
    readTurnContext: (_runtime, event) => { read = event; },
    request: async () => ({ capture_enabled: true }),
  });
  assert.equal(read.source_event_key, expected.source_event_key);
  assert.equal(read.idempotency_key, expected.idempotency_key);
});

test('Claude PreToolUse uses official deny output and mints an exact Pulse lease', async () => {
  const denied = await handleClaudeHook('PreToolUse', {
    ...base,
    hook_event_name: 'PreToolUse',
    tool_name: 'Bash',
    tool_input: { command: 'pulse wipe' },
    tool_use_id: 'tool-wipe',
  });
  assert.equal(denied.hookSpecificOutput.permissionDecision, 'deny');
  const wrapped = await handleClaudeHook('PreToolUse', {
    ...base,
    hook_event_name: 'PreToolUse',
    tool_name: 'Bash',
    tool_input: { command: "script -q /dev/null pulse wipe --confirm 'wipe pulse memory'" },
    tool_use_id: 'tool-wrapped-wipe',
  });
  assert.equal(wrapped.hookSpecificOutput.permissionDecision, 'deny');

  const leases = [];
  const toolInput = { schema: 'pulse.memory_capsule.v1', items: [], raw_input_included: false };
  const output = await handleClaudeHook('PreToolUse', {
    ...base,
    hook_event_name: 'PreToolUse',
    tool_name: 'mcp__pulse__pulse_remember',
    tool_input: toolInput,
    tool_use_id: 'tool-memory',
  }, {
    resolveRuntime: () => resolved,
    readTurnContext: () => ({ binding_digest: resolved.binding.binding_digest }),
    request: async () => ({ capture_enabled: true }),
    writeToolLease: (...args) => leases.push(args),
  });
  assert.deepEqual(output, {});
  assert.equal(leases.length, 1);
  assert.equal(leases[0][2], 'claude-code');
  assert.equal(leases[0][3], 'mcp__pulse__pulse_remember');
  assert.deepEqual(leases[0][4], toolInput);
});

test('Claude PostToolUse announces only daemon-corroborated same-turn receipts', async () => {
  const expected = stopEvent();
  const output = await handleClaudeHook('PostToolUse', {
    ...base,
    hook_event_name: 'PostToolUse',
    tool_name: 'mcp__pulse__pulse_remember',
    tool_use_id: 'tool-memory',
    tool_response: { content: [{ type: 'text', text: JSON.stringify({ receipts: [{
      schema: 'pulse.write_receipt.v1', receipt_id: 'receipt_01', ledger_id: 'ledger_01',
      candidate_id: 'candidate_01', status: 'pending',
    }] }) }] },
  }, {
    resolveRuntime: () => resolved,
    readFinalizeMarker: () => ({ ledger_id: 'ledger_01' }),
    request: async () => ({
      schema: 'pulse.write_receipt.v1', receipt_id: 'receipt_01', ledger_id: 'ledger_01',
      candidate_id: 'candidate_01', status: 'pending',
      safe_provenance: {
        host: 'claude-code', session_id: opaque('session', expected.session_id),
        turn_id: opaque('turn', expected.turn_id),
        source_event_key: opaque('event', expected.source_event_key),
      },
    }),
  });
  assert.match(output.systemMessage, /receipt_01:pending/);

  const ignored = await handleClaudeHook('PostToolUse', {
    ...base,
    hook_event_name: 'PostToolUse',
    tool_name: 'mcp__pulse-lookalike__pulse_remember',
    tool_response: { receipts: [{ receipt_id: 'forged' }] },
  }, { resolveRuntime: () => resolved });
  assert.deepEqual(ignored, {});
});

test('Claude Stop blocks once, closes no-change on recursion, and accepts a marker', async () => {
  const first = await handleClaudeHook('Stop', {
    ...base, hook_event_name: 'Stop', stop_hook_active: false,
    last_assistant_message: 'free text ignored', background_tasks: [], session_crons: [],
  }, {
    resolveRuntime: () => resolved,
    readFinalizeMarker: () => { throw new Error('missing'); },
  });
  assert.equal(first.decision, 'block');

  const requests = [];
  const recursive = await handleClaudeHook('Stop', {
    ...base, hook_event_name: 'Stop', stop_hook_active: true,
    last_assistant_message: 'free text ignored', background_tasks: [],
    session_crons: [{ id: 'cron-1', schedule: '0 9 * * *', recurring: true, prompt: 'private future prompt' }],
  }, {
    resolveRuntime: () => resolved,
    readFinalizeMarker: () => { throw new Error('missing'); },
    request: async (_runtime, path, options) => { requests.push({ path, options }); return { status: 'no_change' }; },
  });
  assert.deepEqual(recursive, {});
  assert.equal(requests[0].path, '/turn/no-change');
  assert.equal(requests[0].options.body.turn_id, base.prompt_id);
  assert.doesNotMatch(JSON.stringify(requests), /private future prompt|free text ignored/);

  const marker = await handleClaudeHook('Stop', {
    ...base, hook_event_name: 'Stop', stop_hook_active: false,
  }, {
    resolveRuntime: () => resolved,
    readFinalizeMarker: () => ({ ledger_id: 'ledger_01' }),
  });
  assert.deepEqual(marker, {});
});

test('active Claude background tasks defer without persisting task payload or sealing no-change', async () => {
  let requested = false;
  const output = await handleClaudeHook('Stop', {
    ...base, hook_event_name: 'Stop', stop_hook_active: true,
    background_tasks: [{
      id: 'task-1', type: 'local_bash', status: 'running',
      description: 'private description', command: 'private command',
    }],
    session_crons: [],
  }, {
    resolveRuntime: () => resolved,
    readFinalizeMarker: () => { throw new Error('missing'); },
    request: async () => { requested = true; },
  });
  assert.equal(requested, false);
  assert.equal(output.decision, 'block');
  assert.match(output.reason, /1 background task/);
  assert.doesNotMatch(JSON.stringify(output), /private description|private command/);

  const withMarker = await handleClaudeHook('Stop', {
    ...base, hook_event_name: 'Stop', stop_hook_active: false,
    background_tasks: [{ id: 'task-2', status: 'running', description: 'private late work' }],
  }, {
    resolveRuntime: () => resolved,
    readFinalizeMarker: () => ({ ledger_id: 'ledger_existing' }),
  });
  assert.equal(withMarker.decision, 'block');
  assert.doesNotMatch(JSON.stringify(withMarker), /private late work/);
});

test('Claude readiness requires workspace-bound prompt, truthful receipt, and finalize milestones', () => {
  const dataDir = mkdtempSync(join(tmpdir(), 'pulse-claude-readiness.'));
  const bound = {
    binding: resolved.binding,
    runtime: { ...resolved.runtime, data_dir: dataDir },
  };
  const common = {
    hooksDigest: 'c'.repeat(64), turnProof: 'e'.repeat(64),
    now: new Date('2026-07-14T10:00:00Z'),
  };
  assert.equal(recordClaudeHookReadiness('PreCompact', bound, common), false);
  for (const [event, milestone] of [
    ['UserPromptSubmit', 'prompt_context'],
    ['PostToolUse', 'write_receipt'],
    ['Stop', 'turn_finalize'],
  ]) {
    assert.equal(recordClaudeHookReadiness(event, bound, { ...common, milestone }), true);
  }
  const ready = JSON.parse(readFileSync(join(dataDir, 'claude-code-hook-readiness.json'), 'utf8'));
  assert.deepEqual(Object.keys(ready.milestones).sort(), [
    'prompt_context', 'turn_finalize', 'write_receipt',
  ]);
  assert.equal(ready.binding_digest, resolved.binding.binding_digest);
  assert.equal(ready.repository_id, resolved.binding.workspace.repository_id);
  assert.doesNotMatch(JSON.stringify(ready), /\/workspace\/pulse/);

  const other = {
    binding: {
      ...resolved.binding,
      binding_digest: 'd'.repeat(64),
      workspace: { ...resolved.binding.workspace, repository_id: 'repository-other', canonical_path: '/workspace/other' },
    },
    runtime: { ...resolved.runtime, data_dir: dataDir },
  };
  recordClaudeHookReadiness('UserPromptSubmit', other, { ...common, milestone: 'prompt_context' });
  const reset = JSON.parse(readFileSync(join(dataDir, 'claude-code-hook-readiness.json'), 'utf8'));
  assert.deepEqual(Object.keys(reset.milestones), ['prompt_context']);
  assert.equal(reset.repository_id, 'repository-other');
});

test('Claude readiness never combines milestones from different turns', () => {
  const dataDir = mkdtempSync(join(tmpdir(), 'pulse-claude-readiness-turns.'));
  const bound = { binding: resolved.binding, runtime: { ...resolved.runtime, data_dir: dataDir } };
  const common = { hooksDigest: 'c'.repeat(64), now: new Date('2026-07-14T10:00:00Z') };
  recordClaudeHookReadiness('UserPromptSubmit', bound, {
    ...common, milestone: 'prompt_context', turnProof: '1'.repeat(64),
  });
  recordClaudeHookReadiness('PostToolUse', bound, {
    ...common, milestone: 'write_receipt', turnProof: '2'.repeat(64),
  });
  recordClaudeHookReadiness('Stop', bound, {
    ...common, milestone: 'turn_finalize', turnProof: '2'.repeat(64),
  });
  const receipt = JSON.parse(readFileSync(join(dataDir, 'claude-code-hook-readiness.json'), 'utf8'));
  assert.equal(receipt.turn_proof, '2'.repeat(64));
  assert.deepEqual(Object.keys(receipt.milestones).sort(), ['turn_finalize', 'write_receipt']);
});

test('Claude recursive finalize failure creates a durable content-free receipt', async () => {
  const receipts = [];
  const output = await handleClaudeHook('Stop', {
    ...base, hook_event_name: 'Stop', stop_hook_active: true,
    background_tasks: [], session_crons: [],
  }, {
    resolveRuntime: () => resolved,
    readFinalizeMarker: () => { throw new Error('missing'); },
    request: async () => { throw new Error('daemon unavailable'); },
    recordFailure: (_runtime, receipt) => receipts.push(receipt),
    now: () => new Date('2026-07-14T10:00:00Z'),
  });
  assert.match(output.systemMessage, /finalize_failed/);
  assert.equal(receipts[0].host, 'claude-code');
  assert.equal(receipts[0].turn_id, base.prompt_id);
  assert.doesNotMatch(JSON.stringify(receipts), /daemon unavailable|transcript/);
});

test('Claude compact and subagent events stay on the same prompt_id without closing it', async () => {
  const compact = await handleClaudeHook('PreCompact', {
    ...base, hook_event_name: 'PreCompact', trigger: 'auto',
  }, { resolveRuntime: () => resolved });
  assert.match(compact.systemMessage, /current turn open/);

  const subagent = await handleClaudeHook('SubagentStart', {
    ...base, hook_event_name: 'SubagentStart', agent_type: 'Explore', agent_id: 'agent-1',
  }, {
    resolveRuntime: () => resolved,
    request: async () => ({ resume_markdown: 'Bound team practice.' }),
  });
  assert.match(subagent.hookSpecificOutput.additionalContext, /Bound team practice/);
  assert.match(subagent.systemMessage, /parent finalizes/);
});
