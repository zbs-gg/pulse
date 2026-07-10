const TEAM_TOOL_CONTRACTS = [
  {
    name: 'pulse_team_status',
    contract: 'pulse.team.status.v1',
    purpose: 'Report team runtime, principal, binding, context, capabilities, policy, store, and readiness state.',
  },
  {
    name: 'pulse_team_remember',
    contract: 'pulse.team.memory.v1',
    purpose: 'Store an idempotent structured capsule inside a server-authorized target scope.',
  },
  {
    name: 'pulse_team_graph_delta',
    contract: 'pulse.team.graph_delta.v1',
    purpose: 'Store an idempotent, scope-partitioned semantic delta with contribution lineage.',
  },
  {
    name: 'pulse_team_recall',
    contract: 'pulse.team.recall.v1',
    purpose: 'Recall capsules constrained by canonical scope, active context, and retention.',
  },
  {
    name: 'pulse_team_context_query',
    contract: 'pulse.team.context.v1',
    purpose: 'Return pre-authorized state-aware context, graph, assertions, and typed trace.',
  },
  {
    name: 'pulse_team_resume',
    contract: 'pulse.team.resume.v1',
    purpose: 'Return a scoped continuity pack for the active thread, project, or session.',
  },
  {
    name: 'pulse_team_inspect',
    contract: 'pulse.team.inspect.v1',
    purpose: 'Inspect visible provenance, scope, projection state, and deletion state.',
  },
  {
    name: 'pulse_team_audit',
    contract: 'pulse.team.audit.v1',
    purpose: 'Inspect content-free audit records for the caller\'s own actions.',
  },
  {
    name: 'pulse_team_delete',
    contract: 'pulse.team.delete.v1',
    purpose: 'Start an idempotent delete for an authorized personal or project object.',
  },
  {
    name: 'pulse_team_delete_status',
    contract: 'pulse.team.delete_status.v1',
    purpose: 'Read the state of an already visible deletion operation.',
  },
] as const;

export type TeamToolName = (typeof TEAM_TOOL_CONTRACTS)[number]['name'];

const TEAM_TOOL_NAME_SET = new Set<string>(TEAM_TOOL_CONTRACTS.map(({ name }) => name));

export const TEAM_TOOL_DESCRIPTORS = TEAM_TOOL_CONTRACTS.map(({ name, contract, purpose }) => ({
  name,
  description: `${purpose} Contract: ${contract}. U1 preflight stub; domain execution is not active.`,
  inputSchema: {
    type: 'object' as const,
    properties: {},
    additionalProperties: true,
  },
}));

export function isTeamToolName(name: string): name is TeamToolName {
  return TEAM_TOOL_NAME_SET.has(name);
}

export function teamNotReadyResult(name: TeamToolName) {
  return {
    isError: true,
    content: [
      {
        type: 'text' as const,
        text: JSON.stringify({
          schema: 'pulse.team.not_ready.v1',
          error: 'team_remote_not_ready',
          mode: 'team-remote',
          tool: name,
          fallback: false,
        }),
      },
    ],
  };
}

