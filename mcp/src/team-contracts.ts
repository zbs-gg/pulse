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

export const TEAM_MEMORY_SCHEMA = 'pulse.team.memory.v1' as const;
export const TEAM_MEMORY_RESULT_SCHEMA = 'pulse.team.memory_result.v1' as const;
const TEAM_MEMORY_MAX_BODY_BYTES = 256 << 10;

export const TEAM_REMEMBER_INPUT_SCHEMA = {
  type: 'object' as const,
  properties: {
    schema: { type: 'string' as const, const: TEAM_MEMORY_SCHEMA },
    source: {
      type: 'object' as const,
      properties: {
        host: {
          type: 'string' as const,
          enum: [
            'chatgpt', 'claude', 'codex', 'claude-code', 'gemini-cli',
            'cursor', 'langchain', 'crewai', 'pulse-cli',
          ],
        },
        conversation_scope: {
          type: 'string' as const,
          enum: ['current_turn', 'user_selected_excerpt', 'project_context', 'install_event'],
        },
        timestamp: { type: 'string' as const, format: 'date-time' },
      },
      required: ['host', 'conversation_scope', 'timestamp'],
      additionalProperties: false,
    },
    items: {
      type: 'array' as const,
      minItems: 1,
      maxItems: 20,
      items: {
        type: 'object' as const,
        properties: {
          kind: {
            type: 'string' as const,
            enum: [
              'fact', 'decision', 'preference', 'project_state', 'open_loop',
              'correction', 'relationship_note', 'do_not_repeat', 'system_event', 'state_signal',
            ],
          },
          redacted_summary: { type: 'string' as const, minLength: 1, maxLength: 1200 },
          confidence: { type: 'number' as const, minimum: 0, maximum: 1 },
          evidence_hint: {
            type: 'string' as const,
            enum: ['user_selected', 'current_turn', 'assistant_inferred', 'tool_result', 'user_confirmed'],
          },
          tags: {
            type: 'array' as const,
            maxItems: 20,
            items: { type: 'string' as const, minLength: 1, maxLength: 64 },
          },
        },
        required: ['kind', 'redacted_summary', 'confidence', 'evidence_hint'],
        additionalProperties: false,
      },
    },
    raw_input_included: { type: 'boolean' as const, const: false },
    active_context: {
      type: 'object' as const,
      properties: {
        project_id: { type: 'string' as const, minLength: 1, maxLength: 255 },
        repo_id: { type: 'string' as const, minLength: 1, maxLength: 255 },
        agent_id: { type: 'string' as const, minLength: 1, maxLength: 255 },
        session_id: { type: 'string' as const, minLength: 1, maxLength: 255 },
      },
      additionalProperties: false,
    },
    target_scope: {
      type: 'object' as const,
      properties: {
        type: { type: 'string' as const, enum: ['personal', 'project', 'repo', 'agent', 'session'] },
        id: { type: 'string' as const, minLength: 1, maxLength: 255 },
      },
      required: ['type'],
      additionalProperties: false,
      oneOf: [
        {
          properties: { type: { const: 'personal' } },
          not: { required: ['id'] },
        },
        {
          properties: { type: { enum: ['project', 'repo', 'agent', 'session'] } },
          required: ['id'],
        },
      ],
    },
    privacy_tier: { type: 'string' as const, enum: ['normal', 'sensitive', 'private'] },
    retention: { type: 'string' as const, enum: ['session', 'project', 'long_term'] },
    expires_at: { type: 'string' as const, format: 'date-time' },
    idempotency_key: { type: 'string' as const, minLength: 8, maxLength: 255 },
  },
  required: [
    'schema',
    'source',
    'items',
    'raw_input_included',
    'active_context',
    'privacy_tier',
    'retention',
    'idempotency_key',
  ],
  additionalProperties: false,
} as const;

const TEAM_HOSTS = new Set([
  'chatgpt', 'claude', 'codex', 'claude-code', 'gemini-cli',
  'cursor', 'langchain', 'crewai', 'pulse-cli',
]);
const TEAM_CONVERSATION_SCOPES = new Set([
  'current_turn', 'user_selected_excerpt', 'project_context', 'install_event',
]);
const TEAM_MEMORY_KINDS = new Set([
  'fact', 'decision', 'preference', 'project_state', 'open_loop',
  'correction', 'relationship_note', 'do_not_repeat', 'system_event', 'state_signal',
]);
const TEAM_EVIDENCE_HINTS = new Set([
  'user_selected', 'current_turn', 'assistant_inferred', 'tool_result', 'user_confirmed',
]);
const TEAM_PRIVACY_TIERS = new Set(['normal', 'sensitive', 'private']);
const TEAM_RETENTION = new Set(['session', 'project', 'long_term']);
const TEAM_TARGET_TYPES = new Set(['personal', 'project', 'repo', 'agent', 'session']);
const TEAM_ENVELOPE_FIELDS = new Set([
  'schema', 'source', 'items', 'raw_input_included', 'active_context',
  'target_scope', 'privacy_tier', 'retention', 'expires_at', 'idempotency_key',
]);
const TEAM_SOURCE_FIELDS = new Set(['host', 'conversation_scope', 'timestamp']);
const TEAM_ITEM_FIELDS = new Set([
  'kind', 'redacted_summary', 'confidence', 'evidence_hint', 'tags',
]);
const TEAM_ACTIVE_CONTEXT_FIELDS = new Set([
  'project_id', 'repo_id', 'agent_id', 'session_id',
]);
const TEAM_TARGET_FIELDS = new Set(['type', 'id']);
const TEAM_SAFE_TAG = /^[\p{L}\p{N}][\p{L}\p{N}._:-]{0,63}$/u;
const TEAM_SAFE_OPAQUE = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$/;
const TEAM_RFC3339 = /^\d{4}-\d{2}-\d{2}[Tt]\d{2}:\d{2}:\d{2}(\.\d+)?([Zz]|[+-]\d{2}:\d{2})$/;
const TEAM_SECRET_MARKERS = [
  'token=', 'api_key', 'apikey', 'password', 'secret', 'private_key',
  'begin private key', 'authorization: bearer', 'akia', 'xoxb-', 'ghp_',
];
const TEAM_SECRET_PATTERNS = [/\bsk-[A-Za-z0-9_-]{12,}\b/i];
const TEAM_PATH_PATTERNS = [
  /\/(?:users|home|etc|var|private|volumes)\//i,
  /file:\/\//i,
  /(?:^|\s)~\//,
  /(?:^|\s)[a-z]:\\/i,
  /\\\\[^\\\s]+\\[^\\\s]+/,
];

export class TeamContractError extends Error {
  readonly code = 'invalid_team_contract' as const;

  constructor(message: string) {
    super(message);
    this.name = 'TeamContractError';
  }
}

export type TeamDomainErrorCode =
  | 'invalid_team_memory'
  | 'invalid_principal'
  | 'principal_request_mismatch'
  | 'principal_replay'
  | 'principal_revoked'
  | 'policy_denied'
  | 'not_found'
  | 'idempotency_conflict'
  | 'idempotency_in_progress'
  | 'idempotency_failed'
  | 'authorization_stale'
  | 'shared_memory_unavailable';

export class TeamDomainError extends Error {
  readonly code: TeamDomainErrorCode;

  constructor(code: TeamDomainErrorCode) {
    super('team memory domain request failed');
    this.name = 'TeamDomainError';
    this.code = code;
  }
}

function failTeamContract(message: string): never {
  throw new TeamContractError(message);
}

function teamRecord(
  value: unknown,
  field: string,
  allowed: ReadonlySet<string>,
): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    failTeamContract(`${field} must be an object`);
  }
  const record = value as Record<string, unknown>;
  for (const key of Object.keys(record)) {
    if (!allowed.has(key)) failTeamContract(`${field}.${key} is not allowed`);
  }
  return record;
}

function teamString(field: string, value: unknown, minimum: number, maximum: number): string {
  if (typeof value !== 'string') failTeamContract(`${field} must be a string`);
  const clean = value.trim();
  const length = Array.from(clean).length;
  if (length < minimum || length > maximum) {
    failTeamContract(`${field} must be ${minimum}..${maximum} characters`);
  }
  return clean;
}

function compareUnicodeCodePoints(left: string, right: string): number {
  const leftPoints = Array.from(left, (value) => value.codePointAt(0) ?? 0);
  const rightPoints = Array.from(right, (value) => value.codePointAt(0) ?? 0);
  const length = Math.min(leftPoints.length, rightPoints.length);
  for (let index = 0; index < length; index++) {
    if (leftPoints[index] !== rightPoints[index]) return leftPoints[index] - rightPoints[index];
  }
  return leftPoints.length - rightPoints.length;
}

function teamEnum(field: string, value: unknown, allowed: ReadonlySet<string>): string {
  const clean = teamString(field, value, 1, 255);
  if (!allowed.has(clean)) failTeamContract(`${field} is unsupported`);
  return clean;
}

function teamRFC3339(field: string, value: unknown): string {
  const clean = teamString(field, value, 1, 64);
  if (!TEAM_RFC3339.test(clean) || Number.isNaN(Date.parse(clean))) {
    failTeamContract(`${field} must be RFC3339`);
  }
  return new Date(clean).toISOString();
}

function countOccurrences(text: string, needle: string): number {
  let count = 0;
  let position = text.indexOf(needle);
  while (position >= 0) {
    count += 1;
    position = text.indexOf(needle, position + needle.length);
  }
  return count;
}

function unsafeTeamContentReason(value: string): 'transcript' | 'secret' | 'path' | undefined {
  const lower = value.toLowerCase();
  if (
    countOccurrences(lower, 'user:') >= 3 ||
    countOccurrences(lower, 'assistant:') >= 3 ||
    countOccurrences(lower, '\n') > 30
  ) {
    return 'transcript';
  }
  if (
    TEAM_SECRET_MARKERS.some((marker) => lower.includes(marker)) ||
    TEAM_SECRET_PATTERNS.some((pattern) => pattern.test(value))
  ) return 'secret';
  if (TEAM_PATH_PATTERNS.some((pattern) => pattern.test(value))) return 'path';
  return undefined;
}

function safeTeamText(field: string, value: unknown, maximum: number): string {
  const clean = teamString(field, value, 1, maximum);
  const unsafe = unsafeTeamContentReason(clean);
  if (unsafe) failTeamContract(`${field} contains ${unsafe}-like content`);
  return clean;
}

function safeTeamOpaque(field: string, value: unknown, minimum = 1): string {
  const clean = teamString(field, value, minimum, 255);
  if (!TEAM_SAFE_OPAQUE.test(clean) || unsafeTeamContentReason(clean)) {
    failTeamContract(`${field} must be a safe opaque identifier`);
  }
  return clean;
}

export interface CleanTeamRememberInput {
  schema: typeof TEAM_MEMORY_SCHEMA;
  source: { host: string; conversation_scope: string; timestamp: string };
  items: Array<{
    kind: string;
    redacted_summary: string;
    confidence: number;
    evidence_hint: string;
    tags: string[];
  }>;
  raw_input_included: false;
  active_context: {
    project_id?: string;
    repo_id?: string;
    agent_id?: string;
    session_id?: string;
  };
  target_scope?: { type: string; id?: string };
  privacy_tier: string;
  retention: string;
  expires_at?: string;
  idempotency_key: string;
}

export function validateTeamRememberInput(input: unknown): CleanTeamRememberInput {
  const envelope = teamRecord(input, 'input', TEAM_ENVELOPE_FIELDS);
  if (envelope.schema !== TEAM_MEMORY_SCHEMA) {
    failTeamContract(`schema must be ${TEAM_MEMORY_SCHEMA}`);
  }
  if (envelope.raw_input_included !== false) {
    failTeamContract('raw_input_included must be false');
  }

  const source = teamRecord(envelope.source, 'source', TEAM_SOURCE_FIELDS);
  const cleanSource = {
    host: teamEnum('source.host', source.host, TEAM_HOSTS),
    conversation_scope: teamEnum(
      'source.conversation_scope',
      source.conversation_scope,
      TEAM_CONVERSATION_SCOPES,
    ),
    timestamp: teamRFC3339('source.timestamp', source.timestamp),
  };

  if (!Array.isArray(envelope.items) || envelope.items.length < 1 || envelope.items.length > 20) {
    failTeamContract('items must contain 1..20 structured entries');
  }
  const cleanItems = envelope.items.map((entry, index) => {
    const field = `items[${index}]`;
    const item = teamRecord(entry, field, TEAM_ITEM_FIELDS);
    if (
      typeof item.confidence !== 'number' ||
      !Number.isFinite(item.confidence) ||
      item.confidence < 0 ||
      item.confidence > 1
    ) {
      failTeamContract(`${field}.confidence must be 0..1`);
    }
    const rawTags = item.tags === undefined ? [] : item.tags;
    if (!Array.isArray(rawTags) || rawTags.length > 20) {
      failTeamContract(`${field}.tags must contain at most 20 tags`);
    }
    const tags = rawTags.map((tag, tagIndex) => {
      const tagField = `${field}.tags[${tagIndex}]`;
      const clean = safeTeamText(tagField, tag, 64);
      if (!TEAM_SAFE_TAG.test(clean)) failTeamContract(`${tagField} is unsafe`);
      return clean;
    });
    tags.sort(compareUnicodeCodePoints);
    for (let tagIndex = 1; tagIndex < tags.length; tagIndex++) {
      if (tags[tagIndex] === tags[tagIndex - 1]) {
        failTeamContract(`${field}.tags contains a duplicate`);
      }
    }
    return {
      kind: teamEnum(`${field}.kind`, item.kind, TEAM_MEMORY_KINDS),
      redacted_summary: safeTeamText(`${field}.redacted_summary`, item.redacted_summary, 1200),
      confidence: item.confidence,
      evidence_hint: teamEnum(`${field}.evidence_hint`, item.evidence_hint, TEAM_EVIDENCE_HINTS),
      tags,
    };
  });

  const active = teamRecord(envelope.active_context, 'active_context', TEAM_ACTIVE_CONTEXT_FIELDS);
  const cleanActive: CleanTeamRememberInput['active_context'] = {};
  for (const field of ['project_id', 'repo_id', 'agent_id', 'session_id'] as const) {
    if (active[field] !== undefined) {
      cleanActive[field] = safeTeamOpaque(`active_context.${field}`, active[field]);
    }
  }

  const clean: CleanTeamRememberInput = {
    schema: TEAM_MEMORY_SCHEMA,
    source: cleanSource,
    items: cleanItems,
    raw_input_included: false,
    active_context: cleanActive,
    privacy_tier: teamEnum('privacy_tier', envelope.privacy_tier, TEAM_PRIVACY_TIERS),
    retention: teamEnum('retention', envelope.retention, TEAM_RETENTION),
    idempotency_key: safeTeamOpaque('idempotency_key', envelope.idempotency_key, 8),
  };
  if (envelope.target_scope !== undefined) {
    const target = teamRecord(envelope.target_scope, 'target_scope', TEAM_TARGET_FIELDS);
    const type = teamEnum('target_scope.type', target.type, TEAM_TARGET_TYPES);
    if (type === 'personal') {
      if (target.id !== undefined) {
        failTeamContract('target_scope.id is not allowed for personal scope');
      }
      clean.target_scope = { type };
    } else {
      clean.target_scope = { type, id: safeTeamOpaque('target_scope.id', target.id) };
    }
  }
  if (envelope.expires_at !== undefined) {
    clean.expires_at = teamRFC3339('expires_at', envelope.expires_at);
  }
  return clean;
}

export function canonicalTeamRememberBody(input: unknown): {
  value: CleanTeamRememberInput;
  text: string;
  bytes: Buffer;
} {
  const clean = validateTeamRememberInput(input);
  const value: CleanTeamRememberInput = {
    schema: clean.schema,
    source: {
      host: clean.source.host,
      conversation_scope: clean.source.conversation_scope,
      timestamp: clean.source.timestamp,
    },
    items: clean.items.map((item) => ({
      kind: item.kind,
      redacted_summary: item.redacted_summary,
      confidence: item.confidence,
      evidence_hint: item.evidence_hint,
      tags: [...item.tags],
    })),
    raw_input_included: false,
    active_context: {
      ...(clean.active_context.project_id === undefined ? {} : { project_id: clean.active_context.project_id }),
      ...(clean.active_context.repo_id === undefined ? {} : { repo_id: clean.active_context.repo_id }),
      ...(clean.active_context.agent_id === undefined ? {} : { agent_id: clean.active_context.agent_id }),
      ...(clean.active_context.session_id === undefined ? {} : { session_id: clean.active_context.session_id }),
    },
    ...(clean.target_scope === undefined ? {} : {
      target_scope: {
        type: clean.target_scope.type,
        ...(clean.target_scope.id === undefined ? {} : { id: clean.target_scope.id }),
      },
    }),
    privacy_tier: clean.privacy_tier,
    retention: clean.retention,
    ...(clean.expires_at === undefined ? {} : { expires_at: clean.expires_at }),
    idempotency_key: clean.idempotency_key,
  };
  const text = JSON.stringify(value);
  const bytes = Buffer.from(text, 'utf8');
  if (bytes.byteLength > TEAM_MEMORY_MAX_BODY_BYTES) {
    failTeamContract('canonical team memory body is too large');
  }
  return { value, text, bytes };
}

export interface TeamRememberResult {
  schema: typeof TEAM_MEMORY_RESULT_SCHEMA;
  object_id: string;
  audit_event_id: string;
  capsule_ids: string[];
  status: 'stored';
  projection_state: 'pending';
  projection_jobs: Array<{
    kind: 'embedding' | 'event';
    job_id: string;
    state: 'pending';
  }>;
  fully_projected: false;
  replayed: boolean;
  fallback: false;
}

const TEAM_MEMORY_RESULT_FIELDS = new Set([
  'schema', 'object_id', 'audit_event_id', 'capsule_ids', 'status',
  'projection_state', 'projection_jobs', 'fully_projected', 'replayed', 'fallback',
]);
const TEAM_MEMORY_JOB_FIELDS = new Set(['kind', 'job_id', 'state']);

export function validateTeamRememberResult(value: unknown, expectedCapsules: number): TeamRememberResult {
  if (!Number.isInteger(expectedCapsules) || expectedCapsules < 1 || expectedCapsules > 20) {
    throw new Error('team memory response is invalid');
  }
  const result = exactTeamResponseRecord(value, TEAM_MEMORY_RESULT_FIELDS);
  if (
    result.schema !== TEAM_MEMORY_RESULT_SCHEMA ||
    result.status !== 'stored' || result.projection_state !== 'pending' ||
    result.fully_projected !== false || typeof result.replayed !== 'boolean' ||
    result.fallback !== false || !teamResponseOpaque(result.object_id) ||
    !teamResponseOpaque(result.audit_event_id) || !Array.isArray(result.capsule_ids) ||
    result.capsule_ids.length !== expectedCapsules ||
    result.capsule_ids.some((id) => !teamResponseOpaque(id)) ||
    new Set(result.capsule_ids).size !== result.capsule_ids.length ||
    !Array.isArray(result.projection_jobs) || result.projection_jobs.length !== 2
  ) {
    throw new Error('team memory response is invalid');
  }
  const jobs = result.projection_jobs.map((value) => exactTeamResponseRecord(value, TEAM_MEMORY_JOB_FIELDS));
  if (
    jobs[0]?.kind !== 'embedding' || jobs[1]?.kind !== 'event' ||
    jobs.some((job) => !teamResponseOpaque(job.job_id) || job.state !== 'pending') ||
    new Set(jobs.map((job) => job.job_id)).size !== jobs.length
  ) {
    throw new Error('team memory response is invalid');
  }
  return {
    schema: TEAM_MEMORY_RESULT_SCHEMA,
    object_id: result.object_id as string,
    audit_event_id: result.audit_event_id as string,
    capsule_ids: [...result.capsule_ids] as string[],
    status: 'stored',
    projection_state: 'pending',
    projection_jobs: jobs.map((job) => ({
      kind: job.kind as 'embedding' | 'event',
      job_id: job.job_id as string,
      state: 'pending',
    })),
    fully_projected: false,
    replayed: result.replayed,
    fallback: false,
  };
}

function exactTeamResponseRecord(value: unknown, fields: ReadonlySet<string>): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('team memory response is invalid');
  }
  const record = value as Record<string, unknown>;
  const keys = Object.keys(record);
  if (keys.length !== fields.size || keys.some((key) => !fields.has(key))) {
    throw new Error('team memory response is invalid');
  }
  return record;
}

function teamResponseOpaque(value: unknown): value is string {
  return typeof value === 'string' && TEAM_SAFE_OPAQUE.test(value);
}

export const TEAM_CAPABILITIES = [
  'pulse:connect',
  'pulse:status',
  'pulse:read',
  'pulse:write',
  'pulse:audit',
  'pulse:delete',
] as const;

export type TeamCapability = (typeof TEAM_CAPABILITIES)[number];
export const TEAM_BASELINE_CAPABILITY: TeamCapability = 'pulse:connect';

export type TeamToolName = (typeof TEAM_TOOL_CONTRACTS)[number]['name'];

const TEAM_TOOL_NAME_SET = new Set<string>(TEAM_TOOL_CONTRACTS.map(({ name }) => name));

export const TEAM_TOOL_DESCRIPTORS = TEAM_TOOL_CONTRACTS.map(({ name, contract, purpose }) => {
  if (name === 'pulse_team_remember') {
    return {
      name,
      description: `${purpose} Contract: ${contract}. Gateway validation and domain execution are active.`,
      inputSchema: TEAM_REMEMBER_INPUT_SCHEMA,
    };
  }
  return {
    name,
    description: `${purpose} Contract: ${contract}. U1 preflight stub; domain execution is not active.`,
    inputSchema: {
      type: 'object' as const,
      properties: {},
      additionalProperties: true,
    },
  };
});

export function isTeamToolName(name: string): name is TeamToolName {
  return TEAM_TOOL_NAME_SET.has(name);
}

const TEAM_TOOL_CAPABILITY: Record<TeamToolName, TeamCapability> = {
  pulse_team_status: 'pulse:status',
  pulse_team_remember: 'pulse:write',
  pulse_team_graph_delta: 'pulse:write',
  pulse_team_recall: 'pulse:read',
  pulse_team_context_query: 'pulse:read',
  pulse_team_resume: 'pulse:read',
  pulse_team_inspect: 'pulse:read',
  pulse_team_audit: 'pulse:audit',
  pulse_team_delete: 'pulse:delete',
  pulse_team_delete_status: 'pulse:read',
};

export function requiredTeamCapabilities(message: unknown): TeamCapability[] {
  if (Array.isArray(message)) {
    return [...new Set(message.flatMap(requiredTeamCapabilities))].sort();
  }
  const required = new Set<TeamCapability>([TEAM_BASELINE_CAPABILITY]);
  if (!message || typeof message !== 'object') {
    return [...required];
  }
  const record = message as Record<string, unknown>;
  if (record.method === 'tools/call') {
    const params = record.params;
    const name = params && typeof params === 'object'
      ? (params as Record<string, unknown>).name
      : undefined;
    if (typeof name !== 'string' || !isTeamToolName(name)) {
      throw new Error('Unknown team tool');
    }
    required.add(TEAM_TOOL_CAPABILITY[name]);
  }
  return [...required].sort();
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

export function teamDomainErrorResult(code: TeamDomainErrorCode | 'invalid_team_contract') {
  return {
    isError: true,
    content: [{
      type: 'text' as const,
      text: JSON.stringify({ error: code, fallback: false }),
    }],
  };
}
