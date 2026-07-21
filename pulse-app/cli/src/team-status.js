import { callTeamRemoteTool } from './team-remote-client.js';

const SAFE_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$/;

function requireID(value) {
  if (typeof value !== 'string' || !SAFE_ID.test(value)) throw new Error('team_status_invalid');
  return value;
}

function installationHealth(response) {
  if (!['ready', 'pending', 'failed'].includes(response.projection_state) ||
      typeof response.degraded !== 'boolean' || !Array.isArray(response.degraded_reasons) ||
      response.degraded_reasons.length > 16 ||
      response.degraded_reasons.some((reason) => typeof reason !== 'string' || !SAFE_ID.test(reason)) ||
      new Set(response.degraded_reasons).size !== response.degraded_reasons.length ||
      JSON.stringify(response.degraded_reasons) !== JSON.stringify([...response.degraded_reasons].sort()) ||
      response.degraded !== (response.degraded_reasons.length > 0) ||
      (response.projection_state !== 'ready' && response.degraded !== true)) {
    throw new Error('team_status_invalid');
  }
  if (response.degraded_reasons.includes('worker_heartbeat_missing')) return 'missing';
  if (response.degraded_reasons.includes('worker_heartbeat_stale')) return 'stale';
  if (response.projection_state === 'failed') return 'pending';
  if (response.projection_state === 'pending') return 'pending';
  return response.degraded ? 'degraded' : 'ready';
}

export function setTeamStatusExitCode(result, processObject = process) {
  if (!result || result.ready !== true) processObject.exitCode = 1;
}

export async function inspectTeamInstallation(binding, {
  teamRequest = callTeamRemoteTool,
} = {}) {
  if (!binding || binding.mode !== 'team' || binding.fallback !== false ||
      !binding.commons || !binding.workspace || typeof teamRequest !== 'function') {
    throw new Error('team_status_binding_required');
  }
  const response = await teamRequest(binding, 'pulse_team_status', {
    schema: 'pulse.team.status.v1',
    active_context: {
      project_id: requireID(binding.commons.project_id),
      repo_id: requireID(binding.workspace.repository_id),
      agent_id: requireID(binding.principal_ref),
    },
  });
  if (!response || response.schema !== 'pulse.team.status_result.v1' ||
      response.mode !== 'team-remote' || response.fallback !== false ||
      response.team_id !== binding.commons.team_id || response.store_id !== binding.commons.store_id ||
      !Array.isArray(response.effective_capabilities) ||
      !response.effective_capabilities.includes('pulse:connect') ||
      !response.effective_capabilities.includes('pulse:read') ||
      !response.active_context ||
      response.active_context.project_id !== binding.commons.project_id ||
      response.active_context.repo_id !== binding.workspace.repository_id ||
      response.active_context.agent_id !== binding.principal_ref ||
      typeof response.membership_role !== 'string' || response.membership_role === '') {
    throw new Error('team_status_invalid');
  }
  const status = installationHealth(response);
  return Object.freeze({
    schema: 'pulse.team.installation_status.v1',
    status,
    ready: status === 'ready',
    team_id: response.team_id,
    store_id: response.store_id,
    membership_role: response.membership_role,
    capabilities: Object.freeze([...response.effective_capabilities].sort()),
    registry_enrollment: 'accepted',
    sender_constrained: true,
    refresh_configured: true,
    commons_reads: true,
    commons_agent_writes: false,
    airlock_only_publication: true,
    projection_state: response.projection_state,
    degraded: response.degraded,
    degraded_reasons: Object.freeze([...response.degraded_reasons]),
    fallback: false,
  });
}
