import assert from 'node:assert/strict';
import test from 'node:test';

import { selectHomeDoctorReport } from './home-doctor.js';

function report(host, outcome = 'ready') {
  return {
    target_host: host,
    personal_live_readiness: {
      schema: host === 'codex' ? 'pulse.personal_live_readiness.v1' : 'pulse.supported_host_live_readiness.v1',
      outcome,
    },
  };
}

test('Home explicit selector inspects exactly the requested supported host', async () => {
  for (const host of ['claude-code', 'codex', 'cursor', 'opencode']) {
    const called = [];
    const selected = await selectHomeDoctorReport({
      requestedHost: host,
      doctorForHost: async (value) => { called.push(value); return report(value); },
    });
    assert.equal(selected.target_host, host);
    assert.deepEqual(called, [host]);
  }
});

test('Home auto-selection skips failed doctors and returns the first ready enabled host', async () => {
  const called = [];
  const selected = await selectHomeDoctorReport({
    enabledHosts: ['claude-code', 'codex', 'cursor', 'opencode'],
    doctorForHost: async (host) => {
      called.push(host);
      if (host === 'claude-code') throw new Error('missing');
      return report(host, ['claude-code', 'codex', 'cursor'].includes(host) ? 'action_required' : 'ready');
    },
  });
  assert.equal(selected.target_host, 'opencode');
  assert.deepEqual(called, ['claude-code', 'codex', 'cursor', 'opencode']);
});

test('Home auto-selection preserves the first actionable report and fails only when all doctors fail', async () => {
  const actionable = await selectHomeDoctorReport({
    enabledHosts: ['claude-code', 'cursor'],
    doctorForHost: async (host) => report(host, 'action_required'),
  });
  assert.equal(actionable.target_host, 'claude-code');
  await assert.rejects(selectHomeDoctorReport({
    enabledHosts: ['claude-code'], doctorForHost: async () => { throw new Error('missing'); },
  }), /could not inspect any supported host/);
  await assert.rejects(selectHomeDoctorReport({
    requestedHost: 'gemini', doctorForHost: async () => report('gemini'),
  }), /must be claude-code, codex, cursor, or opencode/);
});
