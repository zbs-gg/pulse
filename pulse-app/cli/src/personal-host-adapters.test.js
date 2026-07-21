import assert from 'node:assert/strict';
import test from 'node:test';

import {
  activateDetectedPersonalHosts,
  inspectDetectedPersonalHosts,
} from './personal-host-adapters.js';

function hosts(...targets) {
  return ['claude-code', 'codex', 'cursor'].map((host) => ({
    host,
    detected: targets.includes(host),
    compatible: targets.includes(host),
    activation_target: targets.includes(host),
  }));
}

function adapter(state, calls, host) {
  return {
    inspect: async (context) => {
      assert.equal(context.store_id, 'store_personal_test');
      calls.push(`inspect:${host}`);
      return state.ready
        ? { ready: true, lifecycle_ready: state.lifecycleReady !== false, reason_code: 'host_verified' }
        : { ready: false, lifecycle_ready: false, reason_code: `${host.replaceAll('-', '_')}_activation_required` };
    },
    activate: async () => {
      calls.push(`activate:${host}`);
      state.ready = true;
    },
  };
}

test('singleton activation invokes only the detected adapter', async () => {
  for (const selected of ['claude-code', 'codex', 'cursor']) {
    const calls = [];
    const states = Object.fromEntries(['claude-code', 'codex', 'cursor'].map((host) => [host, { ready: false }]));
    const registry = Object.fromEntries(Object.keys(states).map((host) => [host, adapter(states[host], calls, host)]));
    const result = await activateDetectedPersonalHosts({
      context: { store_id: 'store_personal_test' }, hosts: hosts(selected), registry,
    });
    assert.equal(result.product_ready, true, selected);
    assert.equal(result.parity, 'complete', selected);
    assert.deepEqual(result.hosts.map((entry) => entry.host), [selected]);
    assert.deepEqual(calls, [`inspect:${selected}`, `activate:${selected}`, `inspect:${selected}`]);
  }
});

test('healthy adapters are reused and a failed secondary host degrades parity without blocking product readiness', async () => {
  const calls = [];
  const registry = {
    'claude-code': adapter({ ready: true }, calls, 'claude-code'),
    codex: {
      inspect: async () => ({ ready: false, lifecycle_ready: false, reason_code: 'codex_hook_trust_required' }),
      activate: async () => { calls.push('activate:codex'); },
    },
    cursor: adapter({ ready: false }, calls, 'cursor'),
  };
  const result = await activateDetectedPersonalHosts({
    context: { store_id: 'store_personal_test' }, hosts: hosts('claude-code', 'codex'), registry,
  });
  assert.equal(result.product_ready, true);
  assert.equal(result.parity, 'degraded');
  assert.deepEqual(result.hosts.map(({ host, verified, reason_code }) => ({ host, verified, reason_code })), [
    { host: 'claude-code', verified: true, reason_code: 'host_verified' },
    { host: 'codex', verified: false, reason_code: 'codex_hook_trust_required' },
  ]);
  assert.deepEqual(calls, ['inspect:claude-code', 'activate:codex', 'inspect:claude-code']);
});

test('activation skips prior verified mutation but returns fresh evidence for every target', async () => {
  const calls = [];
  const cursorState = { ready: false };
  const registry = {
    'claude-code': adapter({ ready: true }, calls, 'claude-code'),
    codex: adapter({ ready: false }, calls, 'codex'),
    cursor: adapter(cursorState, calls, 'cursor'),
  };
  const prior = {
    product_ready: true,
    parity: 'degraded',
    hosts: [
      { host: 'claude-code', detected: true, compatible: true, installed: true, mcp_ready: true,
        activated: true, verified: true, lifecycle_ready: true, reload_required: false,
        milestones: ['turn_capture'], reason_code: 'host_verified' },
      { host: 'cursor', detected: true, compatible: true, installed: false, mcp_ready: false,
        activated: false, verified: false, lifecycle_ready: false, reload_required: false,
        milestones: [], reason_code: 'cursor_activation_required' },
    ],
  };
  const result = await activateDetectedPersonalHosts({
    context: { store_id: 'store_personal_test' },
    hosts: hosts('claude-code', 'cursor'),
    registry,
    prior,
  });
  assert.equal(result.parity, 'complete');
  assert.deepEqual(calls, ['activate:cursor', 'inspect:claude-code', 'inspect:cursor']);
});

test('a prior verified host that disappears cannot make the final result ready', async () => {
  const claudeState = { ready: false };
  const registry = {
    'claude-code': adapter(claudeState, [], 'claude-code'),
    codex: adapter({ ready: false }, [], 'codex'),
    cursor: {
      inspect: async () => ({ ready: false, lifecycle_ready: false, reason_code: 'cursor_activation_required' }),
      activate: async () => { throw new Error('cursor unavailable'); },
    },
  };
  const result = await activateDetectedPersonalHosts({
    context: { store_id: 'store_personal_test' },
    hosts: hosts('claude-code', 'cursor'),
    registry,
    prior: {
      product_ready: true,
      parity: 'degraded',
      hosts: [
        { host: 'claude-code', detected: true, compatible: true, installed: true, mcp_ready: true,
          activated: true, verified: true, lifecycle_ready: true, reload_required: false,
          milestones: ['turn_capture'], reason_code: 'host_verified' },
        { host: 'cursor', detected: true, compatible: true, installed: false, mcp_ready: false,
          activated: false, verified: false, lifecycle_ready: false, reload_required: false,
          milestones: [], reason_code: 'cursor_activation_required' },
      ],
    },
  });
  assert.equal(result.product_ready, false);
  assert.equal(result.parity, 'blocked');
  assert.equal(result.hosts.every((host) => host.verified === false), true);
});

test('inspection never activates and reports zero verified hosts as not ready', async () => {
  const calls = [];
  const registry = {
    'claude-code': adapter({ ready: false }, calls, 'claude-code'),
    codex: adapter({ ready: false }, calls, 'codex'),
    cursor: adapter({ ready: false }, calls, 'cursor'),
  };
  const result = await inspectDetectedPersonalHosts({
    context: { store_id: 'store_personal_test' }, hosts: hosts('cursor'), registry,
  });
  assert.equal(result.product_ready, false);
  assert.equal(result.parity, 'blocked');
  assert.deepEqual(calls, ['inspect:cursor']);
});

test('static plugin readiness remains reload-required until a real lifecycle is observed', async () => {
  const calls = [];
  const registry = {
    'claude-code': adapter({ ready: false }, calls, 'claude-code'),
    codex: adapter({ ready: true, lifecycleReady: false }, calls, 'codex'),
    cursor: adapter({ ready: false }, calls, 'cursor'),
  };
  const result = await inspectDetectedPersonalHosts({
    context: { store_id: 'store_personal_test' }, hosts: hosts('codex'), registry,
  });
  assert.equal(result.product_ready, false);
  assert.equal(result.parity, 'blocked');
  assert.deepEqual(result.hosts[0], {
    host: 'codex', detected: true, compatible: true, installed: true, mcp_ready: true,
    activated: true, verified: false, lifecycle_ready: false, reload_required: true,
    milestones: [], reason_code: 'codex_lifecycle_required',
  });
});

test('unknown hosts and unsafe thrown reason strings fail closed to stable codes', async () => {
  await assert.rejects(() => inspectDetectedPersonalHosts({
    context: {}, hosts: [{ host: 'unknown', compatible: true, activation_target: true }], registry: {},
  }), /personal_host_inventory_invalid/);
  const result = await activateDetectedPersonalHosts({
    context: { store_id: 'store_personal_test' },
    hosts: hosts('cursor'),
    registry: {
      'claude-code': adapter({ ready: false }, [], 'claude-code'),
      codex: adapter({ ready: false }, [], 'codex'),
      cursor: {
        inspect: async () => ({ ready: false, reason_code: 'cursor_activation_required' }),
        activate: async () => { throw new Error('/Users/private secret=abc'); },
      },
    },
  });
  assert.equal(result.hosts[0].reason_code, 'cursor_activation_failed');
  assert.equal(JSON.stringify(result).includes('/Users/private'), false);
});
