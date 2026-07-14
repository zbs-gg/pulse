import assert from 'node:assert/strict';
import test from 'node:test';

import { inspectTeamInstallation, setTeamStatusExitCode } from './team-status.js';

function binding() {
  return {
    mode: 'team', fallback: false, principal_ref: 'principal_nik',
    workspace: { workspace_id: 'workspace_pulse', repository_id: 'repository_pulse' },
    desk: { store_id: 'store_desk_nik' },
    commons: { team_id: 'team_zbs', store_id: 'store_commons_zbs', project_id: 'project_zbs' },
  };
}

function statusResponse(overrides = {}) {
  return {
    schema: 'pulse.team.status_result.v1', mode: 'team-remote',
    team_id: 'team_zbs', store_id: 'store_commons_zbs', membership_role: 'member',
    active_context: {
      project_id: 'project_zbs', repo_id: 'repository_pulse', agent_id: 'principal_nik',
    },
    effective_capabilities: ['pulse:connect', 'pulse:read'],
    projection_state: 'ready', degraded: false, degraded_reasons: [], fallback: false,
    ...overrides,
  };
}

test('Team status proves accepted enrollment, read capability, sender constraint, and no fallback', async () => {
  const result = await inspectTeamInstallation(binding(), {
    teamRequest: async (_binding, tool, input) => {
      assert.equal(tool, 'pulse_team_status');
      assert.equal(input.active_context.agent_id, 'principal_nik');
      assert.equal(input.active_context.project_id, 'project_zbs');
      return statusResponse({ effective_capabilities: ['pulse:read', 'pulse:connect'] });
    },
  });
  assert.deepEqual(result, {
    schema: 'pulse.team.installation_status.v1', status: 'ready', ready: true,
    team_id: 'team_zbs', store_id: 'store_commons_zbs', membership_role: 'member',
    capabilities: ['pulse:connect', 'pulse:read'], registry_enrollment: 'accepted',
    sender_constrained: true, refresh_configured: true, commons_reads: true,
    commons_agent_writes: false, airlock_only_publication: true, fallback: false,
    projection_state: 'ready', degraded: false, degraded_reasons: [],
  });
});

test('Team installation treats a failed projection cycle as pending retry work', async () => {
  const cases = [
    [{}, 'ready', true],
    [{ degraded: true, degraded_reasons: ['embedding_dependency_not_configured'] }, 'degraded', false],
    [{ projection_state: 'pending', degraded: true, degraded_reasons: ['projection_jobs_pending'] }, 'pending', false],
    [{ projection_state: 'failed', degraded: true, degraded_reasons: ['projection_cycle_failed'] }, 'pending', false],
    [{ projection_state: 'pending', degraded: true, degraded_reasons: ['worker_heartbeat_missing'] }, 'missing', false],
    [{ projection_state: 'pending', degraded: true, degraded_reasons: ['worker_heartbeat_stale'] }, 'stale', false],
  ];
  for (const [overrides, expected, ready] of cases) {
    const result = await inspectTeamInstallation(binding(), {
      teamRequest: async () => statusResponse(overrides),
    });
    assert.equal(result.status, expected);
    assert.equal(result.ready, ready);
  }
});

test('Team status CLI readiness exit code fails closed for every non-ready state', () => {
  for (const status of ['degraded', 'pending', 'failed', 'missing', 'stale']) {
    const processObject = { exitCode: undefined };
    setTeamStatusExitCode({ status, ready: false }, processObject);
    assert.equal(processObject.exitCode, 1);
  }
  const processObject = { exitCode: undefined };
  setTeamStatusExitCode({ status: 'ready', ready: true }, processObject);
  assert.equal(processObject.exitCode, undefined);
});

test('Team status rejects wrong deployment identity and readless principals', async () => {
  for (const response of [
    statusResponse({ team_id: 'team_other' }),
    statusResponse({ effective_capabilities: ['pulse:connect'] }),
    statusResponse({ active_context: {
      project_id: 'workspace_pulse', repo_id: 'repository_pulse', agent_id: 'principal_nik',
    } }),
    statusResponse({ degraded: false, degraded_reasons: ['projection_jobs_pending'] }),
  ]) {
    await assert.rejects(
      inspectTeamInstallation(binding(), { teamRequest: async () => response }),
      /team_status_invalid/,
    );
  }
});
