import { callTeamRemoteTool, TeamRemoteClientError } from './team-remote-client.js';

const RESUME_SECTIONS = Object.freeze([
  'where_we_left_off',
  'active_decisions',
  'open_loops',
  'do_not_repeat',
  'relevant_emotional_state_context',
  'suggested_next_step',
]);
const SAFE_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$/;

function safeID(value, code) {
  if (typeof value !== 'string' || !SAFE_ID.test(value)) throw new Error(code);
  return value;
}

function localEvidence(binding, result) {
  const value = result?.resume_markdown;
  if (typeof value !== 'string' || value.length > 64 * 1024) throw new Error('local_resume_invalid');
  const source = binding.mode === 'team' ? 'Private Desk' : 'Personal Vault';
  return `${source} continuity (local, private):\n${value}`;
}

export function renderCommonsResume(result, limit) {
  if (!result || typeof result !== 'object' || Array.isArray(result) ||
      result.schema !== 'pulse.team.resume_result.v1' || result.fallback !== false ||
      !Number.isInteger(result.returned_count) || result.returned_count < 0 || result.returned_count > limit ||
      !result.sections || typeof result.sections !== 'object' || Array.isArray(result.sections) ||
      Object.keys(result.sections).sort().join('\0') !== [...RESUME_SECTIONS].sort().join('\0')) {
    throw new Error('commons_resume_invalid');
  }
  if (result.thread_id !== undefined) safeID(result.thread_id, 'commons_resume_invalid');
  const evidence = [];
  for (const section of RESUME_SECTIONS) {
    const entries = result.sections[section];
    if (!Array.isArray(entries)) throw new Error('commons_resume_invalid');
    for (const entry of entries) {
      if (!entry || typeof entry !== 'object' || Array.isArray(entry) ||
          Object.keys(entry).sort().join('\0') !== 'object_id\0text') throw new Error('commons_resume_invalid');
      const objectID = safeID(entry.object_id, 'commons_resume_invalid');
      if (typeof entry.text !== 'string' || entry.text.length < 1 || Array.from(entry.text).length > 1200) {
        throw new Error('commons_resume_invalid');
      }
      evidence.push(`Team Commons [${section}] [${objectID}] (shared, authorized Commons evidence):\n${entry.text}`);
    }
  }
  if (evidence.length !== result.returned_count || evidence.length > limit) throw new Error('commons_resume_invalid');
  return evidence;
}

export async function composeBoundResumeEvidence(resolved, event, {
  host,
  request,
  teamRequest = callTeamRemoteTool,
  localTokenBudget = 800,
  commonsLimit = 8,
} = {}) {
  if (!resolved?.binding || !resolved?.runtime || !['codex', 'claude-code'].includes(host) ||
      typeof request !== 'function' || !Number.isInteger(localTokenBudget) || localTokenBudget < 256 ||
      localTokenBudget > 2000 || !Number.isInteger(commonsLimit) || commonsLimit < 1 || commonsLimit > 20) {
    throw new Error('compositor_configuration_invalid');
  }
  const workspace = resolved.binding.workspace;
  const sessionID = safeID(event?.session_id, 'compositor_event_invalid');
  const local = await request(resolved, '/continuity/resume', {
    body: {
      thread_id: workspace.repository_id,
      project_id: workspace.workspace_id,
      session_id: sessionID,
      host,
      token_budget: localTokenBudget,
    },
    idempotencyKey: event.idempotency_key,
  });
  const evidence = [localEvidence(resolved.binding, local)];
  if (resolved.binding.mode !== 'team') {
    return Object.freeze({ evidence: Object.freeze(evidence), commons: Object.freeze({ status: 'not_applicable' }) });
  }
  try {
    const remote = await teamRequest(resolved.binding, 'pulse_team_resume', {
      schema: 'pulse.team.resume.v1',
      active_context: {
        project_id: safeID(resolved.binding.commons?.project_id, 'compositor_binding_invalid'),
        repo_id: safeID(workspace.repository_id, 'compositor_binding_invalid'),
        agent_id: safeID(resolved.binding.principal_ref, 'compositor_binding_invalid'),
        session_id: sessionID,
      },
      thread_id: safeID(workspace.repository_id, 'compositor_binding_invalid'),
      limit: commonsLimit,
    });
    evidence.push(...renderCommonsResume(remote, commonsLimit));
    return Object.freeze({
      evidence: Object.freeze(evidence),
      commons: Object.freeze({ status: 'active', returned_count: remote.returned_count, fallback: false }),
    });
  } catch (error) {
    const code = error instanceof TeamRemoteClientError ? error.code : 'invalid_or_unavailable';
    evidence.push(`Team Commons unavailable (${code}); local Desk remains active. No fallback store was queried.`);
    return Object.freeze({
      evidence: Object.freeze(evidence),
      commons: Object.freeze({ status: 'degraded', reason_code: code, fallback: false }),
    });
  }
}
