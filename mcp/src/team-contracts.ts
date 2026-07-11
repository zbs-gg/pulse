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
export const TEAM_GRAPH_DELTA_SCHEMA = 'pulse.team.graph_delta.v1' as const;
export const TEAM_GRAPH_DELTA_RESULT_SCHEMA = 'pulse.team.graph_delta_result.v1' as const;
export const TEAM_RECALL_SCHEMA = 'pulse.team.recall.v1' as const;
export const TEAM_RECALL_RESULT_SCHEMA = 'pulse.team.recall_result.v1' as const;
export const TEAM_CONTEXT_QUERY_SCHEMA = 'pulse.team.context.v1' as const;
export const TEAM_CONTEXT_QUERY_RESULT_SCHEMA = 'pulse.team.context_result.v1' as const;
export const TEAM_RESUME_SCHEMA = 'pulse.team.resume.v1' as const;
export const TEAM_RESUME_RESULT_SCHEMA = 'pulse.team.resume_result.v1' as const;
export const TEAM_DELETE_SCHEMA = 'pulse.team.delete.v1' as const;
export const TEAM_DELETE_RESULT_SCHEMA = 'pulse.team.delete_result.v1' as const;
export const TEAM_DELETE_STATUS_SCHEMA = 'pulse.team.delete_status.v1' as const;
export const TEAM_DELETE_STATUS_RESULT_SCHEMA = 'pulse.team.delete_status_result.v1' as const;
const TEAM_MEMORY_MAX_BODY_BYTES = 256 << 10;
const TEAM_GRAPH_DELTA_MAX_BODY_BYTES = 256 << 10;
const TEAM_READ_MAX_BODY_BYTES = 64 << 10;

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

const TEAM_GRAPH_DOMAIN_SCHEMA = {
  type: 'string' as const,
  enum: ['real', 'fiction_content', 'fiction_meta', 'meta_authorial'],
};

const TEAM_GRAPH_SCORE_SCHEMA = {
  type: 'number' as const,
  minimum: 0,
  maximum: 1,
};

export const TEAM_GRAPH_DELTA_INPUT_SCHEMA = {
  type: 'object' as const,
  properties: {
    schema: { type: 'string' as const, const: TEAM_GRAPH_DELTA_SCHEMA },
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
    nodes: {
      type: 'array' as const,
      maxItems: 30,
      items: {
        type: 'object' as const,
        properties: {
          client_id: { type: 'string' as const, minLength: 2, maxLength: 96 },
          kind: {
            type: 'string' as const,
            enum: [
              'person', 'place', 'project', 'org', 'product', 'community',
              'skill', 'concept', 'thing', 'event_series',
            ],
          },
          canonical_name: { type: 'string' as const, minLength: 1, maxLength: 160 },
          summary: { type: 'string' as const, minLength: 1, maxLength: 1200 },
          aliases: {
            type: 'array' as const,
            maxItems: 20,
            uniqueItems: true,
            items: { type: 'string' as const, minLength: 1, maxLength: 160 },
          },
          salience: TEAM_GRAPH_SCORE_SCHEMA,
          emotional_weight: TEAM_GRAPH_SCORE_SCHEMA,
          domain: TEAM_GRAPH_DOMAIN_SCHEMA,
        },
        required: ['client_id', 'kind', 'canonical_name', 'domain'],
        additionalProperties: false,
      },
    },
    edges: {
      type: 'array' as const,
      maxItems: 50,
      items: {
        type: 'object' as const,
        properties: {
          from: { type: 'string' as const, minLength: 2, maxLength: 96 },
          to: { type: 'string' as const, minLength: 2, maxLength: 96 },
          kind: { type: 'string' as const, minLength: 1, maxLength: 64 },
          summary: { type: 'string' as const, minLength: 1, maxLength: 1200 },
          strength: TEAM_GRAPH_SCORE_SCHEMA,
        },
        required: ['from', 'to', 'kind'],
        additionalProperties: false,
      },
    },
    facts: {
      type: 'array' as const,
      maxItems: 50,
      items: {
        type: 'object' as const,
        properties: {
          node: { type: 'string' as const, minLength: 2, maxLength: 96 },
          text: { type: 'string' as const, minLength: 1, maxLength: 1200 },
          predicate: { type: 'string' as const, minLength: 1, maxLength: 120 },
          object_text: { type: 'string' as const, minLength: 1, maxLength: 400 },
          valid_from: { type: 'string' as const, format: 'date-time' },
          change_cue: { type: 'boolean' as const },
          source_event_refs: {
            type: 'array' as const,
            maxItems: 20,
            uniqueItems: true,
            items: { type: 'string' as const, minLength: 2, maxLength: 96 },
          },
          confidence: TEAM_GRAPH_SCORE_SCHEMA,
          domain: TEAM_GRAPH_DOMAIN_SCHEMA,
        },
        required: ['node', 'text', 'confidence', 'domain'],
        dependentRequired: {
          predicate: ['object_text'],
          object_text: ['predicate'],
          valid_from: ['predicate', 'object_text'],
          change_cue: ['predicate', 'object_text'],
          source_event_refs: ['predicate', 'object_text'],
        },
        additionalProperties: false,
      },
    },
    events: {
      type: 'array' as const,
      maxItems: 20,
      items: {
        type: 'object' as const,
        properties: {
          client_id: { type: 'string' as const, minLength: 2, maxLength: 96 },
          title: { type: 'string' as const, minLength: 1, maxLength: 180 },
          summary: { type: 'string' as const, minLength: 1, maxLength: 1200 },
          entity_refs: {
            type: 'array' as const,
            maxItems: 20,
            uniqueItems: true,
            items: { type: 'string' as const, minLength: 2, maxLength: 96 },
          },
          sentiment: { type: 'string' as const, minLength: 1, maxLength: 240 },
          emotional_weight: TEAM_GRAPH_SCORE_SCHEMA,
          confidence: TEAM_GRAPH_SCORE_SCHEMA,
          domain: TEAM_GRAPH_DOMAIN_SCHEMA,
          occurred_at: { type: 'string' as const, format: 'date-time' },
          anchor: { type: 'boolean' as const },
          biometrics: {
            type: 'object' as const,
            properties: {
              hrv: { type: 'number' as const, minimum: 0, maximum: 300 },
              sleep_quality: TEAM_GRAPH_SCORE_SCHEMA,
              stress_proxy: TEAM_GRAPH_SCORE_SCHEMA,
              hr_trend: { type: 'string' as const, enum: ['rising', 'stable', 'falling'] },
              hrv_trend: { type: 'string' as const, enum: ['rising', 'stable', 'falling'] },
              workout: { type: 'boolean' as const },
            },
            additionalProperties: false,
          },
          emotions: {
            type: 'object' as const,
            properties: Object.fromEntries([
              'joy', 'sadness', 'anger', 'fear', 'trust', 'disgust',
              'anticipation', 'surprise', 'shame', 'guilt',
            ].map((emotion) => [emotion, TEAM_GRAPH_SCORE_SCHEMA])),
            additionalProperties: false,
          },
        },
        required: ['client_id', 'title', 'summary', 'confidence', 'domain'],
        additionalProperties: false,
      },
    },
    continuity: {
      type: 'object' as const,
      properties: {
        thread_id: { type: 'string' as const, minLength: 1, maxLength: 96 },
        session_id: { type: 'string' as const, minLength: 1, maxLength: 96 },
        summary: { type: 'string' as const, minLength: 1, maxLength: 1200 },
        decisions: teamContinuityArraySchema(),
        open_loops: teamContinuityArraySchema(),
        do_not_repeat: teamContinuityArraySchema(),
        emotional_anchors: teamContinuityArraySchema(),
        state_signals: teamContinuityArraySchema(),
        active_threads: teamContinuityArraySchema(),
        review_insights: teamContinuityArraySchema(),
      },
      required: ['thread_id', 'session_id', 'summary'],
      additionalProperties: false,
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
        { properties: { type: { const: 'personal' } }, not: { required: ['id'] } },
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
    'schema', 'source', 'nodes', 'edges', 'facts', 'events',
    'raw_input_included', 'active_context', 'privacy_tier', 'retention',
    'idempotency_key',
  ],
  anyOf: [
    { properties: { nodes: { minItems: 1 } } },
    { properties: { edges: { minItems: 1 } } },
    { properties: { facts: { minItems: 1 } } },
    { properties: { events: { minItems: 1 } } },
    { required: ['continuity'] },
  ],
  additionalProperties: false,
} as const;

const TEAM_READ_ACTIVE_CONTEXT_SCHEMA = {
  type: 'object' as const,
  properties: {
    project_id: { type: 'string' as const, minLength: 1, maxLength: 255 },
    repo_id: { type: 'string' as const, minLength: 1, maxLength: 255 },
    agent_id: { type: 'string' as const, minLength: 1, maxLength: 255 },
    session_id: { type: 'string' as const, minLength: 1, maxLength: 255 },
  },
  additionalProperties: false,
} as const;

export const TEAM_RECALL_INPUT_SCHEMA = {
  type: 'object' as const,
  properties: {
    schema: { type: 'string' as const, const: TEAM_RECALL_SCHEMA },
    query: { type: 'string' as const, minLength: 1, maxLength: 1200 },
    active_context: TEAM_READ_ACTIVE_CONTEXT_SCHEMA,
    privacy_ceiling: { type: 'string' as const, enum: ['normal', 'sensitive', 'private'] },
    retention: { type: 'string' as const, enum: ['session', 'project', 'long_term'] },
    limit: { type: 'integer' as const, minimum: 1, maximum: 50, default: 5 },
  },
  required: ['schema', 'query', 'active_context', 'privacy_ceiling'],
  additionalProperties: false,
} as const;

export const TEAM_CONTEXT_QUERY_INPUT_SCHEMA = {
  type: 'object' as const,
  properties: {
    schema: { type: 'string' as const, const: TEAM_CONTEXT_QUERY_SCHEMA },
    query: { type: 'string' as const, minLength: 1, maxLength: 1200 },
    active_context: TEAM_READ_ACTIVE_CONTEXT_SCHEMA,
    privacy_ceiling: { type: 'string' as const, enum: ['normal', 'sensitive', 'private'] },
    retention: { type: 'string' as const, enum: ['session', 'project', 'long_term'] },
    limit: { type: 'integer' as const, minimum: 1, maximum: 50, default: 10 },
    include_trace: { type: 'boolean' as const, default: false },
    graph_mode: {
      type: 'string' as const, enum: ['off', 'anchored', 'walk'], default: 'anchored',
    },
  },
  required: ['schema', 'query', 'active_context', 'privacy_ceiling'],
  additionalProperties: false,
} as const;

export const TEAM_RESUME_INPUT_SCHEMA = {
  type: 'object' as const,
  properties: {
    schema: { type: 'string' as const, const: TEAM_RESUME_SCHEMA },
    active_context: TEAM_READ_ACTIVE_CONTEXT_SCHEMA,
    thread_id: { type: 'string' as const, minLength: 1, maxLength: 255 },
    limit: { type: 'integer' as const, minimum: 1, maximum: 50, default: 20 },
  },
  required: ['schema', 'active_context'],
  anyOf: [
    { required: ['thread_id'] },
    { properties: { active_context: { required: ['project_id'] } } },
    { properties: { active_context: { required: ['session_id'] } } },
  ],
  additionalProperties: false,
} as const;

export const TEAM_DELETE_INPUT_SCHEMA = {
  type: 'object' as const,
  properties: {
    schema: { type: 'string' as const, const: TEAM_DELETE_SCHEMA },
    object_id: { type: 'string' as const, minLength: 1, maxLength: 255 },
    active_context: TEAM_READ_ACTIVE_CONTEXT_SCHEMA,
    idempotency_key: { type: 'string' as const, minLength: 8, maxLength: 255 },
  },
  required: ['schema', 'object_id', 'active_context', 'idempotency_key'],
  additionalProperties: false,
} as const;

export const TEAM_DELETE_STATUS_INPUT_SCHEMA = {
  type: 'object' as const,
  properties: {
    schema: { type: 'string' as const, const: TEAM_DELETE_STATUS_SCHEMA },
    operation_id: { type: 'string' as const, minLength: 1, maxLength: 255 },
    active_context: TEAM_READ_ACTIVE_CONTEXT_SCHEMA,
  },
  required: ['schema', 'operation_id', 'active_context'],
  additionalProperties: false,
} as const;

function teamContinuityArraySchema() {
  return {
    type: 'array' as const,
    maxItems: 20,
    items: { type: 'string' as const, minLength: 1, maxLength: 1200 },
  };
}

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
const TEAM_GRAPH_ENTITY_KINDS = new Set([
  'person', 'place', 'project', 'org', 'product', 'community',
  'skill', 'concept', 'thing', 'event_series',
]);
const TEAM_GRAPH_DOMAINS = new Set([
  'real', 'fiction_content', 'fiction_meta', 'meta_authorial',
]);
const TEAM_GRAPH_TRENDS = new Set(['rising', 'stable', 'falling']);
const TEAM_GRAPH_EMOTION_ORDER = [
  'joy', 'sadness', 'anger', 'fear', 'trust', 'disgust',
  'anticipation', 'surprise', 'shame', 'guilt',
] as const;
const TEAM_GRAPH_EMOTIONS = new Set<string>(TEAM_GRAPH_EMOTION_ORDER);
const TEAM_ENVELOPE_FIELDS = new Set([
  'schema', 'source', 'items', 'raw_input_included', 'active_context',
  'target_scope', 'privacy_tier', 'retention', 'expires_at', 'idempotency_key',
]);
const TEAM_GRAPH_ENVELOPE_FIELDS = new Set([
  'schema', 'source', 'nodes', 'edges', 'facts', 'events', 'continuity',
  'raw_input_included', 'active_context', 'target_scope', 'privacy_tier',
  'retention', 'expires_at', 'idempotency_key',
]);
const TEAM_SOURCE_FIELDS = new Set(['host', 'conversation_scope', 'timestamp']);
const TEAM_ITEM_FIELDS = new Set([
  'kind', 'redacted_summary', 'confidence', 'evidence_hint', 'tags',
]);
const TEAM_ACTIVE_CONTEXT_FIELDS = new Set([
  'project_id', 'repo_id', 'agent_id', 'session_id',
]);
const TEAM_TARGET_FIELDS = new Set(['type', 'id']);
const TEAM_GRAPH_NODE_FIELDS = new Set([
  'client_id', 'kind', 'canonical_name', 'summary', 'aliases',
  'salience', 'emotional_weight', 'domain',
]);
const TEAM_GRAPH_EDGE_FIELDS = new Set(['from', 'to', 'kind', 'summary', 'strength']);
const TEAM_GRAPH_FACT_FIELDS = new Set([
  'node', 'text', 'predicate', 'object_text', 'valid_from', 'change_cue',
  'source_event_refs', 'confidence', 'domain',
]);
const TEAM_GRAPH_EVENT_FIELDS = new Set([
  'client_id', 'title', 'summary', 'entity_refs', 'sentiment',
  'emotional_weight', 'confidence', 'domain', 'occurred_at', 'anchor',
  'biometrics', 'emotions',
]);
const TEAM_GRAPH_BIOMETRIC_FIELDS = new Set([
  'hrv', 'sleep_quality', 'stress_proxy', 'hr_trend', 'hrv_trend', 'workout',
]);
const TEAM_GRAPH_CONTINUITY_FIELDS = new Set([
  'thread_id', 'session_id', 'summary', 'decisions', 'open_loops',
  'do_not_repeat', 'emotional_anchors', 'state_signals', 'active_threads',
  'review_insights',
]);
const TEAM_RECALL_FIELDS = new Set([
  'schema', 'query', 'active_context', 'privacy_ceiling', 'retention', 'limit',
]);
const TEAM_CONTEXT_QUERY_FIELDS = new Set([
  'schema', 'query', 'active_context', 'privacy_ceiling', 'retention', 'limit',
  'include_trace', 'graph_mode',
]);
const TEAM_RESUME_FIELDS = new Set(['schema', 'active_context', 'thread_id', 'limit']);
const TEAM_DELETE_FIELDS = new Set([
  'schema', 'object_id', 'active_context', 'idempotency_key',
]);
const TEAM_DELETE_STATUS_FIELDS = new Set(['schema', 'operation_id', 'active_context']);
const TEAM_GRAPH_MODES = new Set(['off', 'anchored', 'walk']);
const TEAM_SAFE_TAG = /^[\p{L}\p{N}][\p{L}\p{N}._:-]{0,63}$/u;
const TEAM_SAFE_OPAQUE = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$/;
const TEAM_SEMANTIC_REF = /^[A-Za-z0-9][A-Za-z0-9._:-]{1,95}$/;
const TEAM_RFC3339 = /^(\d{4})-(\d{2})-(\d{2})[Tt](\d{2}):(\d{2}):(\d{2})(\.\d+)?([Zz]|([+-])(\d{2}):(\d{2}))$/;
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
  | 'invalid_team_graph_delta'
  | 'invalid_team_recall'
  | 'invalid_team_context'
  | 'invalid_team_resume'
  | 'invalid_team_delete'
  | 'invalid_team_delete_status'
  | 'invalid_principal'
  | 'principal_request_mismatch'
  | 'principal_replay'
  | 'principal_revoked'
  | 'policy_denied'
  | 'not_found'
  | 'concealed_not_found'
  | 'idempotency_conflict'
  | 'idempotency_in_progress'
  | 'idempotency_failed'
  | 'authorization_stale'
  | 'shared_memory_unavailable';

export class TeamDomainError extends Error {
  readonly code: TeamDomainErrorCode;

  constructor(code: TeamDomainErrorCode) {
    super('team domain request failed');
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

function isCanonicalJSONUnicode(value: string): boolean {
  for (let index = 0; index < value.length; index++) {
    const unit = value.charCodeAt(index);
    if (unit === 0x2028 || unit === 0x2029) return false;
    if (unit >= 0xD800 && unit <= 0xDBFF) {
      const next = value.charCodeAt(index + 1);
      if (next < 0xDC00 || next > 0xDFFF) return false;
      index++;
    } else if (unit >= 0xDC00 && unit <= 0xDFFF) {
      return false;
    }
  }
  return true;
}

function teamString(field: string, value: unknown, minimum: number, maximum: number): string {
  if (typeof value !== 'string') failTeamContract(`${field} must be a string`);
  if (!isCanonicalJSONUnicode(value)) {
    failTeamContract(`${field} must contain cross-runtime-safe Unicode`);
  }
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
  const parts = TEAM_RFC3339.exec(clean);
  if (!parts) failTeamContract(`${field} must be RFC3339`);
  const year = Number(parts[1]);
  const month = Number(parts[2]);
  const day = Number(parts[3]);
  const hour = Number(parts[4]);
  const minute = Number(parts[5]);
  const second = Number(parts[6]);
  const offsetHour = parts[10] === undefined ? 0 : Number(parts[10]);
  const offsetMinute = parts[11] === undefined ? 0 : Number(parts[11]);
  const leapYear = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
  const daysInMonth = [0, 31, leapYear ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][month] ?? 0;
  const parsed = Date.parse(clean);
  if (
    day < 1 || day > daysInMonth || hour > 23 || minute > 59 || second > 59 ||
    offsetHour > 23 || offsetMinute > 59 || Number.isNaN(parsed)
  ) {
    failTeamContract(`${field} must be RFC3339`);
  }
  return new Date(parsed).toISOString();
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

function safeTeamBoundedOpaque(
  field: string,
  value: unknown,
  minimum: number,
  maximum: number,
): string {
  const clean = teamString(field, value, minimum, maximum);
  if (!TEAM_SAFE_OPAQUE.test(clean) || unsafeTeamContentReason(clean)) {
    failTeamContract(`${field} must be a safe opaque identifier`);
  }
  return clean;
}

function safeTeamSemanticRef(field: string, value: unknown): string {
  const clean = teamString(field, value, 2, 96);
  if (!TEAM_SEMANTIC_REF.test(clean) || unsafeTeamContentReason(clean)) {
    failTeamContract(`${field} must be a safe semantic reference`);
  }
  return clean;
}

function safeTeamSlug(field: string, value: unknown): string {
  const clean = safeTeamText(field, value, 64);
  if (!TEAM_SAFE_TAG.test(clean)) failTeamContract(`${field} is unsafe`);
  return clean;
}

function optionalSafeTeamText(field: string, value: unknown, maximum: number): string | undefined {
  return value === undefined ? undefined : safeTeamText(field, value, maximum);
}

function teamFiniteNumber(
  field: string,
  value: unknown,
  minimum: number,
  maximum: number,
  required: boolean,
  fallback?: number,
): number | undefined {
  if (value === undefined) {
    if (required) failTeamContract(`${field} is required`);
    return fallback;
  }
  if (
    typeof value !== 'number' || !Number.isFinite(value) ||
    value < minimum || value > maximum
  ) {
    failTeamContract(`${field} must be ${minimum}..${maximum}`);
  }
  return value;
}

function optionalTeamBoolean(field: string, value: unknown, fallback: boolean): boolean {
  if (value === undefined) return fallback;
  if (typeof value !== 'boolean') failTeamContract(`${field} must be a boolean`);
  return value;
}

function canonicalTeamSet(
  field: string,
  value: unknown,
  maximumItems: number,
  cleaner: (field: string, value: unknown) => string,
): string[] {
  if (value === undefined) return [];
  if (!Array.isArray(value) || value.length > maximumItems) {
    failTeamContract(`${field} must contain at most ${maximumItems} items`);
  }
  const clean = value.map((entry, index) => cleaner(`${field}[${index}]`, entry));
  clean.sort(compareUnicodeCodePoints);
  for (let index = 1; index < clean.length; index++) {
    if (clean[index] === clean[index - 1]) failTeamContract(`${field} contains a duplicate`);
  }
  return clean;
}

function continuityTeamStrings(field: string, value: unknown): string[] {
  if (value === undefined) return [];
  if (!Array.isArray(value) || value.length > 20) {
    failTeamContract(`${field} must contain at most 20 items`);
  }
  return value.map((entry, index) => safeTeamText(`${field}[${index}]`, entry, 1200));
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

export interface TeamReadActiveContext {
  project_id?: string;
  repo_id?: string;
  agent_id?: string;
  session_id?: string;
}

export interface CleanTeamRecallInput {
  schema: typeof TEAM_RECALL_SCHEMA;
  query: string;
  active_context: TeamReadActiveContext;
  privacy_ceiling: string;
  retention?: string;
  limit: number;
}

export interface CleanTeamContextQueryInput {
  schema: typeof TEAM_CONTEXT_QUERY_SCHEMA;
  query: string;
  active_context: TeamReadActiveContext;
  privacy_ceiling: string;
  retention?: string;
  limit: number;
  include_trace: boolean;
  graph_mode: string;
}

export interface CleanTeamResumeInput {
  schema: typeof TEAM_RESUME_SCHEMA;
  active_context: TeamReadActiveContext;
  thread_id?: string;
  limit: number;
}

function cleanTeamReadActiveContext(value: unknown): TeamReadActiveContext {
  const input = teamRecord(value, 'active_context', TEAM_ACTIVE_CONTEXT_FIELDS);
  const clean: TeamReadActiveContext = {};
  for (const field of ['project_id', 'repo_id', 'agent_id', 'session_id'] as const) {
    if (input[field] !== undefined) {
      clean[field] = safeTeamOpaque(`active_context.${field}`, input[field]);
    }
  }
  return clean;
}

function teamReadLimit(field: string, value: unknown, fallback: number): number {
  if (value === undefined) return fallback;
  if (!Number.isInteger(value) || (value as number) < 1 || (value as number) > 50) {
    failTeamContract(`${field} must be an integer from 1 to 50`);
  }
  return value as number;
}

function canonicalTeamReadBody<T>(value: T): { value: T; text: string; bytes: Buffer } {
  const text = JSON.stringify(value);
  const bytes = Buffer.from(text, 'utf8');
  if (bytes.byteLength > TEAM_READ_MAX_BODY_BYTES) {
    failTeamContract('canonical team read body is too large');
  }
  return { value, text, bytes };
}

export function canonicalTeamRecallBody(input: unknown): {
  value: CleanTeamRecallInput; text: string; bytes: Buffer;
} {
  const envelope = teamRecord(input, 'input', TEAM_RECALL_FIELDS);
  if (envelope.schema !== TEAM_RECALL_SCHEMA) {
    failTeamContract(`schema must be ${TEAM_RECALL_SCHEMA}`);
  }
  const value: CleanTeamRecallInput = {
    schema: TEAM_RECALL_SCHEMA,
    query: safeTeamText('query', envelope.query, 1200),
    active_context: cleanTeamReadActiveContext(envelope.active_context),
    privacy_ceiling: teamEnum('privacy_ceiling', envelope.privacy_ceiling, TEAM_PRIVACY_TIERS),
    ...(envelope.retention === undefined
      ? {}
      : { retention: teamEnum('retention', envelope.retention, TEAM_RETENTION) }),
    limit: teamReadLimit('limit', envelope.limit, 5),
  };
  return canonicalTeamReadBody(value);
}

export function canonicalTeamContextQueryBody(input: unknown): {
  value: CleanTeamContextQueryInput; text: string; bytes: Buffer;
} {
  const envelope = teamRecord(input, 'input', TEAM_CONTEXT_QUERY_FIELDS);
  if (envelope.schema !== TEAM_CONTEXT_QUERY_SCHEMA) {
    failTeamContract(`schema must be ${TEAM_CONTEXT_QUERY_SCHEMA}`);
  }
  const value: CleanTeamContextQueryInput = {
    schema: TEAM_CONTEXT_QUERY_SCHEMA,
    query: safeTeamText('query', envelope.query, 1200),
    active_context: cleanTeamReadActiveContext(envelope.active_context),
    privacy_ceiling: teamEnum('privacy_ceiling', envelope.privacy_ceiling, TEAM_PRIVACY_TIERS),
    ...(envelope.retention === undefined
      ? {}
      : { retention: teamEnum('retention', envelope.retention, TEAM_RETENTION) }),
    limit: teamReadLimit('limit', envelope.limit, 10),
    include_trace: optionalTeamBoolean('include_trace', envelope.include_trace, false),
    graph_mode: envelope.graph_mode === undefined
      ? 'anchored'
      : teamEnum('graph_mode', envelope.graph_mode, TEAM_GRAPH_MODES),
  };
  return canonicalTeamReadBody(value);
}

export function canonicalTeamResumeBody(input: unknown): {
  value: CleanTeamResumeInput; text: string; bytes: Buffer;
} {
  const envelope = teamRecord(input, 'input', TEAM_RESUME_FIELDS);
  if (envelope.schema !== TEAM_RESUME_SCHEMA) {
    failTeamContract(`schema must be ${TEAM_RESUME_SCHEMA}`);
  }
  const active = cleanTeamReadActiveContext(envelope.active_context);
  const threadID = envelope.thread_id === undefined
    ? undefined
    : safeTeamOpaque('thread_id', envelope.thread_id);
  if (threadID === undefined && active.project_id === undefined && active.session_id === undefined) {
    failTeamContract('resume requires thread_id, active_context.project_id, or active_context.session_id');
  }
  const value: CleanTeamResumeInput = {
    schema: TEAM_RESUME_SCHEMA,
    active_context: active,
    ...(threadID === undefined ? {} : { thread_id: threadID }),
    limit: teamReadLimit('limit', envelope.limit, 20),
  };
  return canonicalTeamReadBody(value);
}

export interface CleanTeamDeleteInput {
  schema: typeof TEAM_DELETE_SCHEMA;
  object_id: string;
  active_context: TeamReadActiveContext;
  idempotency_key: string;
}

export interface CleanTeamDeleteStatusInput {
  schema: typeof TEAM_DELETE_STATUS_SCHEMA;
  operation_id: string;
  active_context: TeamReadActiveContext;
}

export function canonicalTeamDeleteBody(input: unknown): {
  value: CleanTeamDeleteInput; text: string; bytes: Buffer;
} {
  const envelope = teamRecord(input, 'input', TEAM_DELETE_FIELDS);
  if (envelope.schema !== TEAM_DELETE_SCHEMA) {
    failTeamContract(`schema must be ${TEAM_DELETE_SCHEMA}`);
  }
  return canonicalTeamReadBody({
    schema: TEAM_DELETE_SCHEMA,
    object_id: safeTeamOpaque('object_id', envelope.object_id),
    active_context: cleanTeamReadActiveContext(envelope.active_context),
    idempotency_key: safeTeamOpaque('idempotency_key', envelope.idempotency_key, 8),
  });
}

export function canonicalTeamDeleteStatusBody(input: unknown): {
  value: CleanTeamDeleteStatusInput; text: string; bytes: Buffer;
} {
  const envelope = teamRecord(input, 'input', TEAM_DELETE_STATUS_FIELDS);
  if (envelope.schema !== TEAM_DELETE_STATUS_SCHEMA) {
    failTeamContract(`schema must be ${TEAM_DELETE_STATUS_SCHEMA}`);
  }
  return canonicalTeamReadBody({
    schema: TEAM_DELETE_STATUS_SCHEMA,
    operation_id: safeTeamOpaque('operation_id', envelope.operation_id),
    active_context: cleanTeamReadActiveContext(envelope.active_context),
  });
}

export interface CleanTeamGraphNode {
  client_id: string;
  kind: string;
  canonical_name: string;
  summary?: string;
  aliases: string[];
  salience: number;
  emotional_weight: number;
  domain: string;
}

export interface CleanTeamGraphEdge {
  from: string;
  to: string;
  kind: string;
  summary?: string;
  strength: number;
}

export interface CleanTeamGraphFact {
  node: string;
  text: string;
  predicate?: string;
  object_text?: string;
  valid_from?: string;
  change_cue?: boolean;
  source_event_refs?: string[];
  confidence: number;
  domain: string;
}

export interface CleanTeamGraphBiometrics {
  hrv?: number;
  sleep_quality?: number;
  stress_proxy?: number;
  hr_trend?: string;
  hrv_trend?: string;
  workout?: boolean;
}

export interface CleanTeamGraphEvent {
  client_id: string;
  title: string;
  summary: string;
  entity_refs: string[];
  sentiment?: string;
  emotional_weight: number;
  confidence: number;
  domain: string;
  occurred_at: string;
  anchor: boolean;
  biometrics?: CleanTeamGraphBiometrics;
  emotions: Record<string, number>;
}

export interface CleanTeamGraphContinuity {
  thread_id: string;
  session_id: string;
  summary: string;
  decisions: string[];
  open_loops: string[];
  do_not_repeat: string[];
  emotional_anchors: string[];
  state_signals: string[];
  active_threads: string[];
  review_insights: string[];
}

export interface CleanTeamGraphDeltaInput {
  schema: typeof TEAM_GRAPH_DELTA_SCHEMA;
  source: { host: string; conversation_scope: string; timestamp: string };
  nodes: CleanTeamGraphNode[];
  edges: CleanTeamGraphEdge[];
  facts: CleanTeamGraphFact[];
  events: CleanTeamGraphEvent[];
  continuity?: CleanTeamGraphContinuity;
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

export type TeamGraphProjectionKind = 'claim' | 'continuity' | 'embedding' | 'graph';

function requiredTeamGraphArray(
  field: string,
  value: unknown,
  maximum: number,
): unknown[] {
  if (!Array.isArray(value) || value.length > maximum) {
    failTeamContract(`${field} must be an array with at most ${maximum} items`);
  }
  return value;
}

function cleanTeamGraphActiveContext(value: unknown): CleanTeamGraphDeltaInput['active_context'] {
  const active = teamRecord(value, 'active_context', TEAM_ACTIVE_CONTEXT_FIELDS);
  const clean: CleanTeamGraphDeltaInput['active_context'] = {};
  for (const field of ['project_id', 'repo_id', 'agent_id', 'session_id'] as const) {
    if (active[field] !== undefined) {
      clean[field] = safeTeamOpaque(`active_context.${field}`, active[field]);
    }
  }
  return clean;
}

function cleanTeamGraphTarget(value: unknown): CleanTeamGraphDeltaInput['target_scope'] {
  if (value === undefined) return undefined;
  const target = teamRecord(value, 'target_scope', TEAM_TARGET_FIELDS);
  const type = teamEnum('target_scope.type', target.type, TEAM_TARGET_TYPES);
  if (type === 'personal') {
    if (target.id !== undefined) failTeamContract('target_scope.id is not allowed for personal scope');
    return { type };
  }
  return { type, id: safeTeamOpaque('target_scope.id', target.id) };
}

function cleanTeamGraphBiometrics(value: unknown, field: string): CleanTeamGraphBiometrics | undefined {
  if (value === undefined) return undefined;
  const raw = teamRecord(value, field, TEAM_GRAPH_BIOMETRIC_FIELDS);
  const clean: CleanTeamGraphBiometrics = {};
  if (raw.hrv !== undefined) {
    clean.hrv = teamFiniteNumber(`${field}.hrv`, raw.hrv, 0, 300, false) as number;
  }
  if (raw.sleep_quality !== undefined) {
    clean.sleep_quality = teamFiniteNumber(
      `${field}.sleep_quality`, raw.sleep_quality, 0, 1, false,
    ) as number;
  }
  if (raw.stress_proxy !== undefined) {
    clean.stress_proxy = teamFiniteNumber(
      `${field}.stress_proxy`, raw.stress_proxy, 0, 1, false,
    ) as number;
  }
  if (raw.hr_trend !== undefined) {
    clean.hr_trend = teamEnum(`${field}.hr_trend`, raw.hr_trend, TEAM_GRAPH_TRENDS);
  }
  if (raw.hrv_trend !== undefined) {
    clean.hrv_trend = teamEnum(`${field}.hrv_trend`, raw.hrv_trend, TEAM_GRAPH_TRENDS);
  }
  if (raw.workout !== undefined) {
    if (typeof raw.workout !== 'boolean') failTeamContract(`${field}.workout must be a boolean`);
    clean.workout = raw.workout;
  }
  return clean;
}

function cleanTeamGraphEmotions(value: unknown, field: string): Record<string, number> {
  if (value === undefined) return {};
  const raw = teamRecord(value, field, TEAM_GRAPH_EMOTIONS);
  const clean: Record<string, number> = {};
  for (const emotion of TEAM_GRAPH_EMOTION_ORDER) {
    if (raw[emotion] !== undefined) {
      clean[emotion] = teamFiniteNumber(`${field}.${emotion}`, raw[emotion], 0, 1, true) as number;
    }
  }
  return clean;
}

export function validateTeamGraphDeltaInput(input: unknown): CleanTeamGraphDeltaInput {
  const envelope = teamRecord(input, 'input', TEAM_GRAPH_ENVELOPE_FIELDS);
  if (envelope.schema !== TEAM_GRAPH_DELTA_SCHEMA) {
    failTeamContract(`schema must be ${TEAM_GRAPH_DELTA_SCHEMA}`);
  }
  if (envelope.raw_input_included !== false) {
    failTeamContract('raw_input_included must be false');
  }

  const source = teamRecord(envelope.source, 'source', TEAM_SOURCE_FIELDS);
  const cleanSource = {
    host: teamEnum('source.host', source.host, TEAM_HOSTS),
    conversation_scope: teamEnum(
      'source.conversation_scope', source.conversation_scope, TEAM_CONVERSATION_SCOPES,
    ),
    timestamp: teamRFC3339('source.timestamp', source.timestamp),
  };
  const activeContext = cleanTeamGraphActiveContext(envelope.active_context);
  const targetScope = cleanTeamGraphTarget(envelope.target_scope);

  const nodesIn = requiredTeamGraphArray('nodes', envelope.nodes, 30);
  const edgesIn = requiredTeamGraphArray('edges', envelope.edges, 50);
  const factsIn = requiredTeamGraphArray('facts', envelope.facts, 50);
  const eventsIn = requiredTeamGraphArray('events', envelope.events, 20);
  if (
    nodesIn.length === 0 && edgesIn.length === 0 && factsIn.length === 0 &&
    eventsIn.length === 0 && envelope.continuity === undefined
  ) {
    failTeamContract('team graph delta must include graph content or continuity');
  }

  const nodeRefs = new Set<string>();
  const nodeKeys = new Set<string>();
  const nodes = nodesIn.map((entry, index): CleanTeamGraphNode => {
    const field = `nodes[${index}]`;
    const node = teamRecord(entry, field, TEAM_GRAPH_NODE_FIELDS);
    const clientId = safeTeamSemanticRef(`${field}.client_id`, node.client_id);
    if (nodeRefs.has(clientId)) failTeamContract(`${field}.client_id is duplicate`);
    nodeRefs.add(clientId);
    const kind = teamEnum(`${field}.kind`, node.kind, TEAM_GRAPH_ENTITY_KINDS);
    const canonicalName = safeTeamText(`${field}.canonical_name`, node.canonical_name, 160);
    const domain = teamEnum(`${field}.domain`, node.domain, TEAM_GRAPH_DOMAINS);
    const nodeKey = `${domain}\u0000${kind}\u0000${canonicalName.normalize('NFKC').toLowerCase()}`;
    if (nodeKeys.has(nodeKey)) failTeamContract(`${field} duplicates a semantic node`);
    nodeKeys.add(nodeKey);
    const summary = optionalSafeTeamText(`${field}.summary`, node.summary, 1200);
    return {
      client_id: clientId,
      kind,
      canonical_name: canonicalName,
      ...(summary === undefined ? {} : { summary }),
      aliases: canonicalTeamSet(
        `${field}.aliases`, node.aliases, 20,
        (aliasField, value) => safeTeamText(aliasField, value, 160),
      ),
      salience: teamFiniteNumber(`${field}.salience`, node.salience, 0, 1, false, 0) as number,
      emotional_weight: teamFiniteNumber(
        `${field}.emotional_weight`, node.emotional_weight, 0, 1, false, 0,
      ) as number,
      domain,
    };
  });

  const edgeKeys = new Set<string>();
  const edges = edgesIn.map((entry, index): CleanTeamGraphEdge => {
    const field = `edges[${index}]`;
    const edge = teamRecord(entry, field, TEAM_GRAPH_EDGE_FIELDS);
    const from = safeTeamSemanticRef(`${field}.from`, edge.from);
    const to = safeTeamSemanticRef(`${field}.to`, edge.to);
    if (!nodeRefs.has(from)) failTeamContract(`${field}.from references unknown node`);
    if (!nodeRefs.has(to)) failTeamContract(`${field}.to references unknown node`);
    const kind = safeTeamSlug(`${field}.kind`, edge.kind);
    const key = `${from}\u0000${to}\u0000${kind}`;
    if (edgeKeys.has(key)) failTeamContract(`${field} duplicates a semantic edge`);
    edgeKeys.add(key);
    const summary = optionalSafeTeamText(`${field}.summary`, edge.summary, 1200);
    return {
      from,
      to,
      kind,
      ...(summary === undefined ? {} : { summary }),
      strength: teamFiniteNumber(`${field}.strength`, edge.strength, 0, 1, false, 0) as number,
    };
  });

  const eventRefs = new Set<string>();
  const events = eventsIn.map((entry, index): CleanTeamGraphEvent => {
    const field = `events[${index}]`;
    const event = teamRecord(entry, field, TEAM_GRAPH_EVENT_FIELDS);
    const clientId = safeTeamSemanticRef(`${field}.client_id`, event.client_id);
    if (eventRefs.has(clientId)) failTeamContract(`${field}.client_id is duplicate`);
    eventRefs.add(clientId);
    const entityRefs = canonicalTeamSet(
      `${field}.entity_refs`, event.entity_refs, 20, safeTeamSemanticRef,
    );
    for (const ref of entityRefs) {
      if (!nodeRefs.has(ref)) failTeamContract(`${field}.entity_refs references unknown node`);
    }
    const sentiment = optionalSafeTeamText(`${field}.sentiment`, event.sentiment, 240);
    const occurredAt = event.occurred_at === undefined
      ? cleanSource.timestamp
      : teamRFC3339(`${field}.occurred_at`, event.occurred_at);
    const biometrics = cleanTeamGraphBiometrics(event.biometrics, `${field}.biometrics`);
    return {
      client_id: clientId,
      title: safeTeamText(`${field}.title`, event.title, 180),
      summary: safeTeamText(`${field}.summary`, event.summary, 1200),
      entity_refs: entityRefs,
      ...(sentiment === undefined ? {} : { sentiment }),
      emotional_weight: teamFiniteNumber(
        `${field}.emotional_weight`, event.emotional_weight, 0, 1, false, 0,
      ) as number,
      confidence: teamFiniteNumber(`${field}.confidence`, event.confidence, 0, 1, true) as number,
      domain: teamEnum(`${field}.domain`, event.domain, TEAM_GRAPH_DOMAINS),
      occurred_at: occurredAt,
      anchor: optionalTeamBoolean(`${field}.anchor`, event.anchor, false),
      ...(biometrics === undefined ? {} : { biometrics }),
      emotions: cleanTeamGraphEmotions(event.emotions, `${field}.emotions`),
    };
  });

  const factKeys = new Set<string>();
  const facts = factsIn.map((entry, index): CleanTeamGraphFact => {
    const field = `facts[${index}]`;
    const fact = teamRecord(entry, field, TEAM_GRAPH_FACT_FIELDS);
    const node = safeTeamSemanticRef(`${field}.node`, fact.node);
    if (!nodeRefs.has(node)) failTeamContract(`${field}.node references unknown node`);
    const text = safeTeamText(`${field}.text`, fact.text, 1200);
    const hasPredicate = fact.predicate !== undefined;
    const hasObject = fact.object_text !== undefined;
    if (hasPredicate !== hasObject) {
      failTeamContract(`${field}.predicate and object_text must be supplied together`);
    }
    const hasClaimMetadata = fact.valid_from !== undefined || fact.change_cue !== undefined ||
      fact.source_event_refs !== undefined;
    if (!hasPredicate && hasClaimMetadata) {
      failTeamContract(`${field} structured claim metadata requires predicate and object_text`);
    }

    let predicate: string | undefined;
    let objectText: string | undefined;
    let validFrom: string | undefined;
    let changeCue: boolean | undefined;
    let sourceEventRefs: string[] | undefined;
    if (hasPredicate) {
      predicate = safeTeamText(`${field}.predicate`, fact.predicate, 120);
      objectText = safeTeamText(`${field}.object_text`, fact.object_text, 400);
      validFrom = fact.valid_from === undefined
        ? cleanSource.timestamp
        : teamRFC3339(`${field}.valid_from`, fact.valid_from);
      changeCue = optionalTeamBoolean(`${field}.change_cue`, fact.change_cue, false);
      sourceEventRefs = canonicalTeamSet(
        `${field}.source_event_refs`, fact.source_event_refs, 20, safeTeamSemanticRef,
      );
      for (const ref of sourceEventRefs) {
        if (!eventRefs.has(ref)) {
          failTeamContract(`${field}.source_event_refs references unknown event`);
        }
      }
    }
    const domain = teamEnum(`${field}.domain`, fact.domain, TEAM_GRAPH_DOMAINS);
    const key = [node, domain, text, predicate ?? '', objectText ?? '', validFrom ?? ''].join('\u0000');
    if (factKeys.has(key)) failTeamContract(`${field} duplicates a semantic fact`);
    factKeys.add(key);
    return {
      node,
      text,
      ...(predicate === undefined ? {} : {
        predicate,
        object_text: objectText as string,
        valid_from: validFrom as string,
        change_cue: changeCue as boolean,
        source_event_refs: sourceEventRefs as string[],
      }),
      confidence: teamFiniteNumber(`${field}.confidence`, fact.confidence, 0, 1, true) as number,
      domain,
    };
  });

  let continuity: CleanTeamGraphContinuity | undefined;
  if (envelope.continuity !== undefined) {
    const raw = teamRecord(envelope.continuity, 'continuity', TEAM_GRAPH_CONTINUITY_FIELDS);
    const threadId = safeTeamBoundedOpaque('continuity.thread_id', raw.thread_id, 1, 96);
    const sessionId = safeTeamBoundedOpaque('continuity.session_id', raw.session_id, 1, 96);
    if (activeContext.session_id === undefined || activeContext.session_id !== sessionId) {
      failTeamContract('continuity.session_id must equal active_context.session_id');
    }
    if (targetScope?.type === 'session' && targetScope.id !== sessionId) {
      failTeamContract('session target_scope.id must equal continuity.session_id');
    }
    continuity = {
      thread_id: threadId,
      session_id: sessionId,
      summary: safeTeamText('continuity.summary', raw.summary, 1200),
      decisions: continuityTeamStrings('continuity.decisions', raw.decisions),
      open_loops: continuityTeamStrings('continuity.open_loops', raw.open_loops),
      do_not_repeat: continuityTeamStrings('continuity.do_not_repeat', raw.do_not_repeat),
      emotional_anchors: continuityTeamStrings(
        'continuity.emotional_anchors', raw.emotional_anchors,
      ),
      state_signals: continuityTeamStrings('continuity.state_signals', raw.state_signals),
      active_threads: continuityTeamStrings('continuity.active_threads', raw.active_threads),
      review_insights: continuityTeamStrings('continuity.review_insights', raw.review_insights),
    };
  }

  const clean: CleanTeamGraphDeltaInput = {
    schema: TEAM_GRAPH_DELTA_SCHEMA,
    source: cleanSource,
    nodes,
    edges,
    facts,
    events,
    ...(continuity === undefined ? {} : { continuity }),
    raw_input_included: false,
    active_context: activeContext,
    ...(targetScope === undefined ? {} : { target_scope: targetScope }),
    privacy_tier: teamEnum('privacy_tier', envelope.privacy_tier, TEAM_PRIVACY_TIERS),
    retention: teamEnum('retention', envelope.retention, TEAM_RETENTION),
    ...(envelope.expires_at === undefined
      ? {}
      : { expires_at: teamRFC3339('expires_at', envelope.expires_at) }),
    idempotency_key: safeTeamOpaque('idempotency_key', envelope.idempotency_key, 8),
  };
  if (Buffer.byteLength(JSON.stringify(clean), 'utf8') > TEAM_GRAPH_DELTA_MAX_BODY_BYTES) {
    failTeamContract('canonical team graph delta body is too large: max 256 KiB');
  }
  return clean;
}

export function expectedTeamGraphProjectionKinds(
  input: CleanTeamGraphDeltaInput,
): TeamGraphProjectionKind[] {
  const kinds: TeamGraphProjectionKind[] = [];
  const hasGraph = input.nodes.length > 0 || input.edges.length > 0 ||
    input.facts.length > 0 || input.events.length > 0;
  if (input.facts.some((fact) => fact.predicate !== undefined)) kinds.push('claim');
  if (input.continuity !== undefined) kinds.push('continuity');
  if (hasGraph) kinds.push('embedding', 'graph');
  return kinds.sort();
}

export function canonicalTeamGraphDeltaBody(input: unknown): {
  value: CleanTeamGraphDeltaInput;
  text: string;
  bytes: Buffer;
  projectionKinds: TeamGraphProjectionKind[];
} {
  const value = validateTeamGraphDeltaInput(input);
  const text = JSON.stringify(value);
  const bytes = Buffer.from(text, 'utf8');
  if (bytes.byteLength > TEAM_GRAPH_DELTA_MAX_BODY_BYTES) {
    failTeamContract('canonical team graph delta body is too large: max 256 KiB');
  }
  return {
    value,
    text,
    bytes,
    projectionKinds: expectedTeamGraphProjectionKinds(value),
  };
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

export interface TeamGraphDeltaResult {
  schema: typeof TEAM_GRAPH_DELTA_RESULT_SCHEMA;
  object_id: string;
  audit_event_id: string;
  status: 'stored';
  projection_state: 'pending';
  projection_jobs: Array<{
    kind: TeamGraphProjectionKind;
    job_id: string;
    state: 'pending';
  }>;
  fully_projected: false;
  replayed: boolean;
  fallback: false;
}

const TEAM_GRAPH_RESULT_FIELDS = new Set([
  'schema', 'object_id', 'audit_event_id', 'status', 'projection_state',
  'projection_jobs', 'fully_projected', 'replayed', 'fallback',
]);
const TEAM_GRAPH_JOB_FIELDS = new Set(['kind', 'job_id', 'state']);
const TEAM_GRAPH_PROJECTION_KINDS = new Set<TeamGraphProjectionKind>([
  'claim', 'continuity', 'embedding', 'graph',
]);

export function validateTeamGraphDeltaResult(
  value: unknown,
  expectedKinds: readonly TeamGraphProjectionKind[],
): TeamGraphDeltaResult {
  const error = 'team graph delta response is invalid';
  if (
    !Array.isArray(expectedKinds) || expectedKinds.length < 1 || expectedKinds.length > 4 ||
    expectedKinds.some((kind) => !TEAM_GRAPH_PROJECTION_KINDS.has(kind)) ||
    new Set(expectedKinds).size !== expectedKinds.length ||
    [...expectedKinds].sort().some((kind, index) => kind !== expectedKinds[index])
  ) {
    throw new Error(error);
  }
  const result = exactTeamResponseRecord(value, TEAM_GRAPH_RESULT_FIELDS, error);
  if (
    result.schema !== TEAM_GRAPH_DELTA_RESULT_SCHEMA || result.status !== 'stored' ||
    result.projection_state !== 'pending' || result.fully_projected !== false ||
    typeof result.replayed !== 'boolean' || result.fallback !== false ||
    !teamResponseOpaque(result.object_id) || !teamResponseOpaque(result.audit_event_id) ||
    !Array.isArray(result.projection_jobs) || result.projection_jobs.length !== expectedKinds.length
  ) {
    throw new Error(error);
  }
  const jobs = result.projection_jobs.map((entry) =>
    exactTeamResponseRecord(entry, TEAM_GRAPH_JOB_FIELDS, error));
  if (
    jobs.some((job, index) =>
      job.kind !== expectedKinds[index] || job.state !== 'pending' ||
      !teamResponseOpaque(job.job_id)) ||
    new Set(jobs.map((job) => job.job_id)).size !== jobs.length
  ) {
    throw new Error(error);
  }
  return {
    schema: TEAM_GRAPH_DELTA_RESULT_SCHEMA,
    object_id: result.object_id as string,
    audit_event_id: result.audit_event_id as string,
    status: 'stored',
    projection_state: 'pending',
    projection_jobs: jobs.map((job) => ({
      kind: job.kind as TeamGraphProjectionKind,
      job_id: job.job_id as string,
      state: 'pending',
    })),
    fully_projected: false,
    replayed: result.replayed,
    fallback: false,
  };
}

export interface TeamRecallResult {
  schema: typeof TEAM_RECALL_RESULT_SCHEMA;
  items: Array<{
    object_id: string;
    kind: string;
    redacted_summary: string;
    confidence: number;
    privacy_tier: string;
    retention: string;
    tags: string[];
  }>;
  returned_count: number;
  fallback: false;
}

export interface TeamContextQueryResult {
  schema: typeof TEAM_CONTEXT_QUERY_RESULT_SCHEMA;
  facts: TeamContextFactResult[];
  events: TeamContextEventResult[];
  entities: TeamContextEntityResult[];
  relations: TeamContextRelationResult[];
  assertions: TeamContextAssertionResult[];
  trace?: { stages: Array<{ kind: string; returned_object_ids: string[] }> };
  returned_counts: {
    facts: number; events: number; entities: number; relations: number; assertions: number;
  };
  fallback: false;
}

export interface TeamContextFactResult {
  root_object_id: string; object_id: string; text: string;
  score: number; confidence: number; domain: string;
}

export interface TeamContextEventResult {
  root_object_id: string; object_id: string; title: string; summary: string;
  score: number; confidence: number; domain: string;
}

export interface TeamContextEntityResult {
  root_object_id: string; object_id: string; kind: string;
  canonical_name: string; summary: string; score: number; confidence: number;
}

export interface TeamContextRelationResult {
  root_object_id: string; object_id: string; kind: string;
  from_object_id: string; to_object_id: string; summary: string;
  score: number; confidence: number;
}

export interface TeamContextAssertionResult {
  root_object_id: string; object_id: string; subject_object_id: string;
  predicate: string; object_text: string; confidence: number;
}

export interface TeamResumeResult {
  schema: typeof TEAM_RESUME_RESULT_SCHEMA;
  thread_id?: string;
  sections: Record<string, Array<{ object_id: string; text: string }>>;
  returned_count: number;
  fallback: false;
}

const TEAM_RECALL_RESULT_FIELDS = new Set(['schema', 'items', 'returned_count', 'fallback']);
const TEAM_RECALL_ITEM_FIELDS = new Set([
  'object_id', 'kind', 'redacted_summary', 'confidence', 'privacy_tier', 'retention', 'tags',
]);
const TEAM_CONTEXT_RESULT_FIELDS = new Set([
  'schema', 'facts', 'events', 'entities', 'relations', 'assertions',
  'returned_counts', 'fallback',
]);
const TEAM_CONTEXT_RESULT_TRACE_FIELDS = new Set([...TEAM_CONTEXT_RESULT_FIELDS, 'trace']);
const TEAM_CONTEXT_FACT_FIELDS = new Set([
  'root_object_id', 'object_id', 'text', 'score', 'confidence', 'domain',
]);
const TEAM_CONTEXT_EVENT_FIELDS = new Set([
  'root_object_id', 'object_id', 'title', 'summary', 'score', 'confidence', 'domain',
]);
const TEAM_CONTEXT_ENTITY_FIELDS = new Set([
  'root_object_id', 'object_id', 'kind', 'canonical_name', 'summary', 'score', 'confidence',
]);
const TEAM_CONTEXT_RELATION_FIELDS = new Set([
  'root_object_id', 'object_id', 'kind', 'from_object_id', 'to_object_id',
  'summary', 'score', 'confidence',
]);
const TEAM_CONTEXT_ASSERTION_FIELDS = new Set([
  'root_object_id', 'object_id', 'subject_object_id', 'predicate', 'object_text', 'confidence',
]);
const TEAM_CONTEXT_COUNT_FIELDS = new Set(['facts', 'events', 'entities', 'relations', 'assertions']);
const TEAM_CONTEXT_TRACE_FIELDS = new Set(['stages']);
const TEAM_CONTEXT_TRACE_STAGE_FIELDS = new Set(['kind', 'returned_object_ids']);
const TEAM_CONTEXT_TRACE_KINDS = new Set(['lexical', 'vector', 'graph', 'assertion', 'continuity']);
const TEAM_RESUME_RESULT_BASE_FIELDS = new Set(['schema', 'sections', 'returned_count', 'fallback']);
const TEAM_RESUME_RESULT_THREAD_FIELDS = new Set([...TEAM_RESUME_RESULT_BASE_FIELDS, 'thread_id']);
const TEAM_RESUME_SECTION_FIELDS = new Set([
  'where_we_left_off', 'active_decisions', 'open_loops', 'do_not_repeat',
  'relevant_emotional_state_context', 'suggested_next_step',
]);
const TEAM_RESUME_ENTRY_FIELDS = new Set(['object_id', 'text']);

function validTeamResponseText(value: unknown, maximum: number): value is string {
  if (typeof value !== 'string' || !isCanonicalJSONUnicode(value) || value.trim() !== value) return false;
  const length = Array.from(value).length;
  return length >= 1 && length <= maximum && unsafeTeamContentReason(value) === undefined;
}

function validTeamResponseScore(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 && value <= 1;
}

function exactTeamResponseArray(
  value: unknown,
  limit: number,
  error: string,
): unknown[] {
  if (!Array.isArray(value) || value.length > limit) throw new Error(error);
  return value;
}

function exactTeamResponseTags(value: unknown, error: string): string[] {
  if (!Array.isArray(value) || value.length > 20) throw new Error(error);
  const tags = value.map((tag) => {
    if (typeof tag !== 'string' || !TEAM_SAFE_TAG.test(tag) || unsafeTeamContentReason(tag)) {
      throw new Error(error);
    }
    return tag;
  });
  if (new Set(tags).size !== tags.length) throw new Error(error);
  return tags;
}

export function validateTeamRecallResult(value: unknown, limit: number): TeamRecallResult {
  const error = 'team recall response is invalid';
  if (!Number.isInteger(limit) || limit < 1 || limit > 50) throw new Error(error);
  const result = exactTeamResponseRecord(value, TEAM_RECALL_RESULT_FIELDS, error);
  const rawItems = exactTeamResponseArray(result.items, limit, error);
  if (
    result.schema !== TEAM_RECALL_RESULT_SCHEMA || result.fallback !== false ||
    result.returned_count !== rawItems.length
  ) throw new Error(error);
  const items = rawItems.map((entry) => {
    const item = exactTeamResponseRecord(entry, TEAM_RECALL_ITEM_FIELDS, error);
    if (
      !teamResponseOpaque(item.object_id) || !TEAM_MEMORY_KINDS.has(String(item.kind)) ||
      !validTeamResponseText(item.redacted_summary, 1200) ||
      !validTeamResponseScore(item.confidence) || !TEAM_PRIVACY_TIERS.has(String(item.privacy_tier)) ||
      !TEAM_RETENTION.has(String(item.retention))
    ) throw new Error(error);
    return {
      object_id: item.object_id,
      kind: item.kind as string,
      redacted_summary: item.redacted_summary,
      confidence: item.confidence as number,
      privacy_tier: item.privacy_tier as string,
      retention: item.retention as string,
      tags: exactTeamResponseTags(item.tags, error),
    };
  });
  return {
    schema: TEAM_RECALL_RESULT_SCHEMA,
    items,
    returned_count: items.length,
    fallback: false,
  };
}

function validateContextCommon(
  entry: unknown,
  fields: ReadonlySet<string>,
  error: string,
): Record<string, unknown> {
  const item = exactTeamResponseRecord(entry, fields, error);
  if (
    !teamResponseOpaque(item.root_object_id) || !teamResponseOpaque(item.object_id) ||
    !validTeamResponseScore(item.confidence)
  ) throw new Error(error);
  return item;
}

export function validateTeamContextQueryResult(
  value: unknown,
  limit: number,
  includeTrace: boolean,
): TeamContextQueryResult {
  const error = 'team context response is invalid';
  if (!Number.isInteger(limit) || limit < 1 || limit > 50 || typeof includeTrace !== 'boolean') {
    throw new Error(error);
  }
  const result = exactTeamResponseRecord(
    value,
    includeTrace ? TEAM_CONTEXT_RESULT_TRACE_FIELDS : TEAM_CONTEXT_RESULT_FIELDS,
    error,
  );
  if (result.schema !== TEAM_CONTEXT_QUERY_RESULT_SCHEMA || result.fallback !== false) {
    throw new Error(error);
  }
  const facts = exactTeamResponseArray(result.facts, limit, error).map((entry) => {
    const item = validateContextCommon(entry, TEAM_CONTEXT_FACT_FIELDS, error);
    if (!validTeamResponseText(item.text, 1200) || !validTeamResponseScore(item.score) ||
      !TEAM_GRAPH_DOMAINS.has(String(item.domain))) throw new Error(error);
    return {
      root_object_id: item.root_object_id as string,
      object_id: item.object_id as string,
      text: item.text,
      score: item.score,
      confidence: item.confidence as number,
      domain: item.domain as string,
    };
  });
  const events = exactTeamResponseArray(result.events, limit, error).map((entry) => {
    const item = validateContextCommon(entry, TEAM_CONTEXT_EVENT_FIELDS, error);
    if (!validTeamResponseText(item.title, 180) || !validTeamResponseText(item.summary, 1200) ||
      !validTeamResponseScore(item.score) || !TEAM_GRAPH_DOMAINS.has(String(item.domain))) {
      throw new Error(error);
    }
    return {
      root_object_id: item.root_object_id as string,
      object_id: item.object_id as string,
      title: item.title,
      summary: item.summary,
      score: item.score,
      confidence: item.confidence as number,
      domain: item.domain as string,
    };
  });
  const entities = exactTeamResponseArray(result.entities, limit, error).map((entry) => {
    const item = validateContextCommon(entry, TEAM_CONTEXT_ENTITY_FIELDS, error);
    if (!TEAM_GRAPH_ENTITY_KINDS.has(String(item.kind)) ||
      !validTeamResponseText(item.canonical_name, 160) ||
      !validTeamResponseText(item.summary, 1200) || !validTeamResponseScore(item.score)) {
      throw new Error(error);
    }
    return {
      root_object_id: item.root_object_id as string,
      object_id: item.object_id as string,
      kind: item.kind as string,
      canonical_name: item.canonical_name,
      summary: item.summary,
      score: item.score,
      confidence: item.confidence as number,
    };
  });
  const relations = exactTeamResponseArray(result.relations, limit, error).map((entry) => {
    const item = validateContextCommon(entry, TEAM_CONTEXT_RELATION_FIELDS, error);
    if (typeof item.kind !== 'string' || !TEAM_SAFE_TAG.test(item.kind) ||
      !teamResponseOpaque(item.from_object_id) ||
      !teamResponseOpaque(item.to_object_id) || !validTeamResponseText(item.summary, 1200) ||
      !validTeamResponseScore(item.score)) throw new Error(error);
    return {
      root_object_id: item.root_object_id as string,
      object_id: item.object_id as string,
      kind: item.kind as string,
      from_object_id: item.from_object_id as string,
      to_object_id: item.to_object_id as string,
      summary: item.summary,
      score: item.score,
      confidence: item.confidence as number,
    };
  });
  const assertions = exactTeamResponseArray(result.assertions, limit, error).map((entry) => {
    const item = validateContextCommon(entry, TEAM_CONTEXT_ASSERTION_FIELDS, error);
    if (!teamResponseOpaque(item.subject_object_id) || typeof item.predicate !== 'string' ||
      !TEAM_SAFE_TAG.test(item.predicate) ||
      !validTeamResponseText(item.object_text, 400)) throw new Error(error);
    return {
      root_object_id: item.root_object_id as string,
      object_id: item.object_id as string,
      subject_object_id: item.subject_object_id as string,
      predicate: item.predicate,
      object_text: item.object_text,
      confidence: item.confidence as number,
    };
  });
  const groups = { facts, events, entities, relations, assertions };
  const all = Object.values(groups).flat();
  const derivativeIDs = all.map((item) => item.object_id as string);
  if (new Set(derivativeIDs).size !== derivativeIDs.length) throw new Error(error);
  const entityIDs = new Set(entities.map((item) => item.object_id as string));
  if (relations.some((item) =>
    !entityIDs.has(item.from_object_id as string) || !entityIDs.has(item.to_object_id as string)) ||
    assertions.some((item) => !entityIDs.has(item.subject_object_id as string))) {
    throw new Error(error);
  }
  const counts = exactTeamResponseRecord(result.returned_counts, TEAM_CONTEXT_COUNT_FIELDS, error);
  for (const [kind, entries] of Object.entries(groups)) {
    if (counts[kind] !== entries.length) throw new Error(error);
  }
  let trace: TeamContextQueryResult['trace'];
  if (includeTrace) {
    const rawTrace = exactTeamResponseRecord(result.trace, TEAM_CONTEXT_TRACE_FIELDS, error);
    const rawStages = exactTeamResponseArray(rawTrace.stages, TEAM_CONTEXT_TRACE_KINDS.size, error);
    const seenKinds = new Set<string>();
    const returnedIDs = new Set(derivativeIDs);
    const stages = rawStages.map((entry) => {
      const stage = exactTeamResponseRecord(entry, TEAM_CONTEXT_TRACE_STAGE_FIELDS, error);
      if (!TEAM_CONTEXT_TRACE_KINDS.has(String(stage.kind)) || seenKinds.has(String(stage.kind)) ||
        !Array.isArray(stage.returned_object_ids) || stage.returned_object_ids.length > derivativeIDs.length) {
        throw new Error(error);
      }
      seenKinds.add(String(stage.kind));
      const ids = stage.returned_object_ids.map((id) => {
        if (!teamResponseOpaque(id) || !returnedIDs.has(id)) throw new Error(error);
        return id;
      });
      if (new Set(ids).size !== ids.length) throw new Error(error);
      return { kind: stage.kind as string, returned_object_ids: ids };
    });
    trace = { stages };
  }
  return {
    schema: TEAM_CONTEXT_QUERY_RESULT_SCHEMA,
    facts, events, entities, relations, assertions,
    ...(trace === undefined ? {} : { trace }),
    returned_counts: {
      facts: facts.length, events: events.length, entities: entities.length,
      relations: relations.length, assertions: assertions.length,
    },
    fallback: false,
  };
}

export function validateTeamResumeResult(
  value: unknown,
  limit: number,
  expectedThreadID?: string,
): TeamResumeResult {
  const error = 'team resume response is invalid';
  if (!Number.isInteger(limit) || limit < 1 || limit > 50) throw new Error(error);
  const hasThread = Boolean(value && typeof value === 'object' && !Array.isArray(value) &&
    Object.hasOwn(value as Record<string, unknown>, 'thread_id'));
  const result = exactTeamResponseRecord(
    value,
    hasThread ? TEAM_RESUME_RESULT_THREAD_FIELDS : TEAM_RESUME_RESULT_BASE_FIELDS,
    error,
  );
  if (result.schema !== TEAM_RESUME_RESULT_SCHEMA || result.fallback !== false ||
    (hasThread && !teamResponseOpaque(result.thread_id)) ||
    (expectedThreadID !== undefined && result.thread_id !== expectedThreadID)) {
    throw new Error(error);
  }
  const rawSections = exactTeamResponseRecord(result.sections, TEAM_RESUME_SECTION_FIELDS, error);
  const sections: Record<string, Array<{ object_id: string; text: string }>> = {};
  let returnedCount = 0;
  for (const field of TEAM_RESUME_SECTION_FIELDS) {
    const entries = exactTeamResponseArray(rawSections[field], limit, error).map((entry) => {
      const item = exactTeamResponseRecord(entry, TEAM_RESUME_ENTRY_FIELDS, error);
      if (!teamResponseOpaque(item.object_id) || !validTeamResponseText(item.text, 1200)) {
        throw new Error(error);
      }
      return { object_id: item.object_id, text: item.text };
    });
    returnedCount += entries.length;
    if (returnedCount > limit) throw new Error(error);
    sections[field] = entries;
  }
  if (result.returned_count !== returnedCount) throw new Error(error);
  return {
    schema: TEAM_RESUME_RESULT_SCHEMA,
    ...(hasThread ? { thread_id: result.thread_id as string } : {}),
    sections,
    returned_count: returnedCount,
    fallback: false,
  };
}

export interface TeamDeleteResult {
  schema: typeof TEAM_DELETE_RESULT_SCHEMA;
  operation_id: string;
  object_id: string;
  audit_event_id: string;
  status: 'deletion_in_progress' | 'complete';
  replayed: boolean;
  fallback: false;
}

export interface TeamDeleteStatusResult {
  schema: typeof TEAM_DELETE_STATUS_RESULT_SCHEMA;
  operation_id: string;
  object_id: string;
  audit_event_id: string;
  status: 'deletion_in_progress' | 'cleanup_failed' | 'complete';
  attempts: number;
  next_attempt_at?: string;
  completed_at?: string;
  fallback: false;
}

const TEAM_DELETE_RESULT_FIELDS = new Set([
  'schema', 'operation_id', 'object_id', 'audit_event_id', 'status', 'replayed', 'fallback',
]);
const TEAM_DELETE_STATUS_BASE_FIELDS = [
  'schema', 'operation_id', 'object_id', 'audit_event_id', 'status', 'attempts', 'fallback',
] as const;

function teamDeletionResponseOpaque(value: unknown): value is string {
  return typeof value === 'string' && TEAM_SAFE_OPAQUE.test(value) &&
    unsafeTeamContentReason(value) === undefined;
}

function teamDeletionResponseTimestamp(value: unknown, error: string): string {
  try {
    return teamRFC3339('timestamp', value);
  } catch {
    throw new Error(error);
  }
}

export function validateTeamDeleteResult(value: unknown): TeamDeleteResult {
  const error = 'team delete response is invalid';
  const result = exactTeamResponseRecord(value, TEAM_DELETE_RESULT_FIELDS, error);
  if (
    result.schema !== TEAM_DELETE_RESULT_SCHEMA ||
    (result.status !== 'deletion_in_progress' && result.status !== 'complete') ||
    typeof result.replayed !== 'boolean' || result.fallback !== false ||
    !teamDeletionResponseOpaque(result.operation_id) ||
    !teamDeletionResponseOpaque(result.object_id) ||
    !teamDeletionResponseOpaque(result.audit_event_id)
  ) {
    throw new Error(error);
  }
  return {
    schema: TEAM_DELETE_RESULT_SCHEMA,
    operation_id: result.operation_id,
    object_id: result.object_id,
    audit_event_id: result.audit_event_id,
    status: result.status,
    replayed: result.replayed,
    fallback: false,
  };
}

export function validateTeamDeleteStatusResult(value: unknown): TeamDeleteStatusResult {
  const error = 'team delete status response is invalid';
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(error);
  const raw = value as Record<string, unknown>;
  const hasNextAttempt = Object.hasOwn(raw, 'next_attempt_at');
  const hasCompleted = Object.hasOwn(raw, 'completed_at');
  const result = exactTeamResponseRecord(value, new Set([
    ...TEAM_DELETE_STATUS_BASE_FIELDS,
    ...(hasNextAttempt ? ['next_attempt_at'] : []),
    ...(hasCompleted ? ['completed_at'] : []),
  ]), error);
  if (
    result.schema !== TEAM_DELETE_STATUS_RESULT_SCHEMA || result.fallback !== false ||
    !teamDeletionResponseOpaque(result.operation_id) ||
    !teamDeletionResponseOpaque(result.object_id) ||
    !teamDeletionResponseOpaque(result.audit_event_id) ||
    !Number.isInteger(result.attempts) ||
    (result.attempts as number) < 0 || (result.attempts as number) > 1_000_000
  ) {
    throw new Error(error);
  }
  if (
    (result.status === 'complete' && (!hasCompleted || hasNextAttempt)) ||
    (result.status === 'cleanup_failed' && (!hasNextAttempt || hasCompleted)) ||
    (result.status === 'deletion_in_progress' && hasCompleted) ||
    !['deletion_in_progress', 'cleanup_failed', 'complete'].includes(result.status as string)
  ) {
    throw new Error(error);
  }
  const nextAttempt = hasNextAttempt
    ? teamDeletionResponseTimestamp(result.next_attempt_at, error)
    : undefined;
  const completed = hasCompleted
    ? teamDeletionResponseTimestamp(result.completed_at, error)
    : undefined;
  return {
    schema: TEAM_DELETE_STATUS_RESULT_SCHEMA,
    operation_id: result.operation_id,
    object_id: result.object_id,
    audit_event_id: result.audit_event_id,
    status: result.status as TeamDeleteStatusResult['status'],
    attempts: result.attempts as number,
    ...(nextAttempt === undefined ? {} : { next_attempt_at: nextAttempt }),
    ...(completed === undefined ? {} : { completed_at: completed }),
    fallback: false,
  };
}

function exactTeamResponseRecord(
  value: unknown,
  fields: ReadonlySet<string>,
  error = 'team memory response is invalid',
): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error(error);
  }
  const record = value as Record<string, unknown>;
  const keys = Object.keys(record);
  if (keys.length !== fields.size || keys.some((key) => !fields.has(key))) {
    throw new Error(error);
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
  if (name === 'pulse_team_graph_delta') {
    return {
      name,
      description: `${purpose} Contract: ${contract}. Gateway validation and domain execution are active.`,
      inputSchema: TEAM_GRAPH_DELTA_INPUT_SCHEMA,
    };
  }
  if (name === 'pulse_team_recall') {
    return {
      name,
      description: `${purpose} Contract: ${contract}. Gateway validation and domain execution are active.`,
      inputSchema: TEAM_RECALL_INPUT_SCHEMA,
    };
  }
  if (name === 'pulse_team_context_query') {
    return {
      name,
      description: `${purpose} Contract: ${contract}. Gateway validation and domain execution are active.`,
      inputSchema: TEAM_CONTEXT_QUERY_INPUT_SCHEMA,
    };
  }
  if (name === 'pulse_team_resume') {
    return {
      name,
      description: `${purpose} Contract: ${contract}. Gateway validation and domain execution are active.`,
      inputSchema: TEAM_RESUME_INPUT_SCHEMA,
    };
  }
  if (name === 'pulse_team_delete') {
    return {
      name,
      description: `${purpose} Contract: ${contract}. Gateway validation and domain execution are active.`,
      inputSchema: TEAM_DELETE_INPUT_SCHEMA,
    };
  }
  if (name === 'pulse_team_delete_status') {
    return {
      name,
      description: `${purpose} Contract: ${contract}. Gateway validation and domain execution are active.`,
      inputSchema: TEAM_DELETE_STATUS_INPUT_SCHEMA,
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
