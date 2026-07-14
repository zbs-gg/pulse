#!/usr/bin/env node
/**
 * Pulse MCP server for host-extracted memory.
 *
 * The host model creates a minimal pulse.memory_capsule.v1 in tool
 * arguments. Pulse stores, recalls, deletes, and wipes memory without calling
 * an LLM backend by default. Export/import stay CLI-only in v1.
 */
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { createHash, randomUUID } from 'node:crypto';
import {
  existsSync, lstatSync, mkdirSync, readFileSync, realpathSync, renameSync, rmSync, writeFileSync,
} from 'node:fs';
import { homedir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StreamableHTTPServerTransport } from '@modelcontextprotocol/sdk/server/streamableHttp.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from '@modelcontextprotocol/sdk/types.js';

import { StandaloneStore } from './standalone.js';
import {
  assertTeamRemoteStaticConfig,
  resolveRuntimeMode,
  type RuntimeMode,
} from './runtime-mode.js';
import { assertTruthfulDeletionReceipt, assertTruthfulWriteResponse, mcpRequestIdempotencyKey } from './write-receipts.js';
import type {
  BoundTeamDomain,
  GatewaySecurityEventInput,
  TeamPrincipalContext,
} from './principal-context.js';

const PULSE_BASE_URL =
  process.env.PULSE_BASE_URL ?? 'http://127.0.0.1:18789';
// `||` on purpose: an empty PULSE_DATA_DIR must not become a relative path.
const PULSE_DATA_DIR = process.env.PULSE_DATA_DIR || join(homedir(), '.pulse');

const VERSION = '0.4.1';
const args = process.argv.slice(2);
const HTTP_REQUESTED = args.includes('--http');
const RUNTIME_MODE = resolveRuntimeMode(process.env.PULSE_RUNTIME_MODE, HTTP_REQUESTED);

type EngineMode = 'auto' | 'daemon' | 'standalone';
const ENGINE_MODE = parseEngineMode(process.env.PULSE_MCP_MODE);
const CODEX_HOST_ADAPTER = process.env.PULSE_HOST_ADAPTER === 'codex';

async function assertCodexBindingCurrent(): Promise<void> {
  if (!CODEX_HOST_ADAPTER) return;
  const moduleURL = process.env.PULSE_CODEX_AUTHORITY_MODULE ?? '';
  const expectedWorkspace = process.env.PULSE_CODEX_WORKSPACE ?? '';
  if (!moduleURL.startsWith('file:') || expectedWorkspace === '') {
    throw new Error('Codex binding authority is unavailable; restart this Codex task');
  }
  const authority = await import(moduleURL) as {
    resolveWorkspaceBinding(options: {
      cwd: string;
      registryPath?: string;
      publicKeyPath?: string;
    }): {
      binding_digest: string;
      resolver_epoch: number;
      workspace: { canonical_path: string };
    };
  };
  const options: { cwd: string; registryPath?: string; publicKeyPath?: string } = {
    cwd: process.cwd(),
  };
  if (process.env.PULSE_BINDING_REGISTRY_PATH) {
    options.registryPath = process.env.PULSE_BINDING_REGISTRY_PATH;
  }
  if (process.env.PULSE_BINDING_PUBLIC_KEY_PATH) {
    options.publicKeyPath = process.env.PULSE_BINDING_PUBLIC_KEY_PATH;
  }
  const current = authority.resolveWorkspaceBinding(options);
  if (current.binding_digest !== process.env.PULSE_BINDING_DIGEST ||
      current.resolver_epoch !== Number(process.env.PULSE_RESOLVER_EPOCH) ||
      realpathSync(current.workspace.canonical_path) !== realpathSync(expectedWorkspace) ||
      realpathSync(process.cwd()) !== realpathSync(expectedWorkspace)) {
    throw new Error('Codex workspace binding changed or was revoked; restart this Codex task');
  }
}
if (RUNTIME_MODE === 'team-remote') {
  assertTeamRemoteStaticConfig({
    args,
    authIssuer: process.env.PULSE_REMOTE_AUTH_ISSUER ?? '',
    daemonBaseURL: PULSE_BASE_URL,
    engineMode: ENGINE_MODE,
    env: process.env,
    host: argValue('--host') ?? process.env.PULSE_MCP_HOST ?? '127.0.0.1',
    nodeVersion: process.versions.node,
    publicBaseURL: trimTrailingSlash(process.env.PULSE_REMOTE_PUBLIC_BASE_URL ?? ''),
  });
}
// In auto mode the first daemon connection failure locks the process into the
// standalone lite store; a daemon that answered once is never silently
// downgraded, so one process never splits writes across two stores.
let resolvedEngine: 'daemon' | 'standalone' | null =
  ENGINE_MODE === 'auto' ? null : ENGINE_MODE;
// Gate serializing tool calls while the engine is still unresolved.
let firstCallGate: Promise<void> | null = null;
let standaloneStore: StandaloneStore | null = null;

function parseEngineMode(value: string | undefined): EngineMode {
  if (value === undefined || value === '' || value === 'auto') {
    return 'auto';
  }
  if (value === 'daemon' || value === 'standalone') {
    return value;
  }
  throw new Error(`invalid PULSE_MCP_MODE: ${value} (use auto, daemon, or standalone)`);
}

let apiKeyCache = '';
function resolveApiKey(): string {
  // Cache only a found key: `pulse init` may create secret.key after this
  // server already started, so keep re-checking while none is known.
  if (apiKeyCache !== '') {
    return apiKeyCache;
  }
  const fromEnv = process.env.PULSE_API_KEY ?? '';
  if (fromEnv !== '') {
    apiKeyCache = fromEnv;
    return apiKeyCache;
  }
  try {
    apiKeyCache = readFileSync(join(PULSE_DATA_DIR, 'secret.key'), 'utf8').trim();
  } catch {
    // no secret yet — try again on the next call
  }
  return apiKeyCache;
}

function isConnectionError(err: unknown): boolean {
  if (!(err instanceof Error)) {
    return false;
  }
  if (err.message.startsWith('Pulse HTTP')) {
    return false;
  }
  const cause = (err as { cause?: { code?: string } }).cause;
  const code = cause?.code ?? '';
  return (
    err.message === 'fetch failed' ||
    ['ECONNREFUSED', 'ECONNRESET', 'ENOTFOUND', 'EHOSTUNREACH', 'ETIMEDOUT'].includes(code)
  );
}

type Host =
  | 'chatgpt'
  | 'claude'
  | 'codex'
  | 'claude-code'
  | 'gemini-cli'
  | 'cursor'
  | 'langchain'
  | 'crewai';

type ConversationScope =
  | 'current_turn'
  | 'user_selected_excerpt'
  | 'project_context';

interface MemoryCapsuleItem {
  kind:
    | 'fact'
    | 'decision'
    | 'preference'
    | 'project_state'
    | 'open_loop'
    | 'correction'
    | 'relationship_note'
    | 'do_not_repeat';
  redacted_summary: string;
  confidence: number;
  evidence_hint:
    | 'user_selected'
    | 'current_turn'
    | 'assistant_inferred'
    | 'tool_result';
  privacy_tier: 'normal' | 'sensitive' | 'private';
  retention: 'session' | 'project' | 'long_term';
  tags?: string[];
}

interface MemoryCapsule {
  schema: 'pulse.memory_capsule.v1';
  source: {
    host: Host;
    conversation_scope: ConversationScope;
    timestamp: string;
  };
  items: MemoryCapsuleItem[];
  raw_input_included: false;
}

interface CodexTurnContext {
  schema: 'pulse.codex_turn_context.v1';
  host: 'codex';
  session_id: string;
  turn_id: string;
  workspace: string;
  source_event_key: string;
  idempotency_key: string;
  binding_digest: string;
  policy_epoch: number;
  resolver_epoch: number;
  expires_at: string;
}

interface RecallBody {
  query: string;
  scope?: 'session' | 'project' | 'user';
  limit?: number;
  privacy_ceiling?: 'normal' | 'sensitive' | 'private';
}

interface ContextQueryBody {
  query: string;
  mode?: 'auto' | 'factual' | 'empathic' | 'chain';
  top_k?: number;
  scope?: 'user' | 'assistant' | 'shared';
  audience?: string;
  privacy_floor?: string;
  include_trace?: boolean;
  user_state?: Record<string, unknown>;
  domain_hints?: string[];
  graph_mode?: 'off' | 'anchored' | 'walk';
}

interface ResumeBody {
  thread_id?: string;
  project_id?: string;
  session_id?: string;
  host?: Host;
  token_budget?: number;
}

interface SemanticDeltaBody {
  schema: 'pulse.semantic_delta.v1';
  source: {
    host: Host;
    conversation_scope: ConversationScope;
    timestamp: string;
    thread_id?: string;
    session_id?: string;
    project_id?: string;
  };
  nodes?: Array<Record<string, unknown>>;
  edges?: Array<Record<string, unknown>>;
  facts?: Array<Record<string, unknown>>;
  events?: Array<Record<string, unknown>>;
  continuity?: Record<string, unknown>;
  raw_input_included: false;
}

interface DevOAuthCode {
  clientId: string;
  redirectUri: string;
  codeChallenge: string;
  scope: string;
  expiresAt: number;
}

interface DevOAuthToken {
  scope: string;
  expiresAt: number;
}

async function pulseFetch<T>(
  path: string,
  body?: unknown,
  method = 'POST',
  idempotencyKey?: string,
): Promise<T> {
  const url = `${PULSE_BASE_URL.replace(/\/$/, '')}${path}`;
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
  };
  const apiKey = resolveApiKey();
  if (apiKey) {
    headers['X-Pulse-Key'] = apiKey;
  }
	if (method !== 'GET') {
		headers['Idempotency-Key'] = idempotencyKey ?? `mcp_${randomUUID().replaceAll('-', '')}`;
	}
  const resp = await fetch(url, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!resp.ok) {
    const text = await resp.text();
    throw new Error(
      `Pulse HTTP ${resp.status} on ${path}: ${text.slice(0, 500)}`,
    );
  }
  if (resp.status === 204) {
    return { ok: true } as T;
  }
  return (await resp.json()) as T;
}

async function consumeCodexTurnContext(toolName: string, toolInput: unknown): Promise<CodexTurnContext> {
  const moduleURL = process.env.PULSE_CODEX_RUNTIME_MODULE ?? '';
  if (!moduleURL.startsWith('file:')) {
    throw new Error('Codex tool lease authority is unavailable; restart this Codex task');
  }
  const runtime = await import(moduleURL) as {
    consumeCodexToolLease(
      resolved: {
        binding: {
          binding_digest: string;
          resolver_epoch: number;
          workspace: { canonical_path: string };
        };
        runtime: { data_dir: string };
      },
      name: string,
      input: unknown,
    ): CodexTurnContext;
  };
  return runtime.consumeCodexToolLease({
    binding: {
      binding_digest: process.env.PULSE_BINDING_DIGEST ?? '',
      resolver_epoch: Number(process.env.PULSE_RESOLVER_EPOCH),
      workspace: { canonical_path: process.env.PULSE_CODEX_WORKSPACE ?? '' },
    },
    runtime: { data_dir: PULSE_DATA_DIR },
  }, toolName, toolInput);
}

function codexFinalizeBody(capsule: MemoryCapsule, context: CodexTurnContext): Record<string, unknown> {
  const timestamp = new Date().toISOString();
  return {
    schema: 'pulse.turn_finalize.v1',
    host: context.host,
    session_id: context.session_id,
    turn_id: context.turn_id,
    source_event_key: context.source_event_key,
    idempotency_key: context.idempotency_key,
    binding_digest: context.binding_digest,
    policy_epoch: context.policy_epoch,
    resolver_epoch: context.resolver_epoch,
    candidates: capsule.items.map((item) => ({
      kind: 'memory_capsule',
      capsule: {
        schema: 'pulse.memory_capsule.v1',
        source: { host: 'codex', conversation_scope: 'current_turn', timestamp },
        items: [item],
        raw_input_included: false,
      },
    })),
  };
}

function writeCodexFinalizeMarker(context: CodexTurnContext, value: unknown): void {
  const result = value as Record<string, unknown>;
  const finalize = result.finalize_receipt as Record<string, unknown>;
  const digest = createHash('sha256')
    .update('pulse-codex-finalize-marker-v1\x1f')
    .update(context.session_id)
    .update('\x1f')
    .update(context.turn_id)
    .digest('hex');
  const directory = join(PULSE_DATA_DIR, 'codex-turn-finalized');
  const path = join(directory, `${digest}.json`);
  const temporary = `${path}.${process.pid}.${Date.now()}.new`;
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  try {
    writeFileSync(temporary, `${JSON.stringify({
      schema: 'pulse.codex_finalize_marker.v1',
      session_id: context.session_id,
      turn_id: context.turn_id,
      source_event_key: context.source_event_key,
      binding_digest: context.binding_digest,
      ledger_id: result.ledger_id,
      receipt_id: finalize.receipt_id,
      status: result.status,
      observed_at: new Date().toISOString(),
    })}\n`, { mode: 0o600, flag: 'wx' });
    renameSync(temporary, path);
  } finally {
    rmSync(temporary, { force: true });
  }
}

function jsonText(value: unknown) {
  return {
    content: [
      {
        type: 'text' as const,
        text: JSON.stringify(value, null, 2),
      },
    ],
  };
}

function redactStatusForMcp(value: unknown): unknown {
  if (!value || typeof value !== 'object') {
    return value;
  }
  const status = { ...(value as Record<string, unknown>) };
  if (typeof status.storage !== 'string') {
    status.storage = 'local_sqlite';
  }
  if ('storage_path' in status) {
    status.storage_path = '<local>';
  }
  return status;
}

export function createPulseMcpServer(
  runtimeMode: RuntimeMode = RUNTIME_MODE,
  teamContext?: Readonly<TeamPrincipalContext>,
  teamDomain?: Readonly<BoundTeamDomain>,
  teamSecurityEventSink?: (event: GatewaySecurityEventInput) => void,
): Server {
  const server = new Server(
    { name: 'pulse-mcp', version: VERSION },
    { capabilities: { tools: {} } },
  );

  server.setRequestHandler(ListToolsRequestSchema, async () => {
    if (runtimeMode === 'team-remote') {
      if (!teamContext) throw new Error('team request context is unavailable');
      const { TEAM_TOOL_DESCRIPTORS } = await loadTeamRemoteContracts();
      return { tools: TEAM_TOOL_DESCRIPTORS };
    }
    const tools = [
    {
      name: 'pulse_remember',
      description:
        'Propose a minimal private Pulse memory capsule. Product Personal/Desk mode returns a pending Memory Tray receipt and is saved only after a created, deduplicated, or updated receipt; Local Preview stores immediately. Never send raw full transcripts, arbitrary chat history, secrets, credentials, or store-everything payloads. Use only when the user explicitly asks to remember something, confirms saving, selects an excerpt, or project rules allow it.',
      inputSchema: {
        type: 'object',
        properties: {
          schema: { type: 'string', const: 'pulse.memory_capsule.v1' },
          source: {
            type: 'object',
            properties: {
              host: {
                type: 'string',
                enum: [
                  'chatgpt',
                  'claude',
                  'codex',
                  'claude-code',
                  'gemini-cli',
                  'cursor',
                  'langchain',
                  'crewai',
                ],
              },
              conversation_scope: {
                type: 'string',
                enum: [
                  'current_turn',
                  'user_selected_excerpt',
                  'project_context',
                ],
              },
              timestamp: {
                type: 'string',
                description: 'ISO8601 timestamp for the source turn/excerpt.',
              },
            },
            required: ['host', 'conversation_scope', 'timestamp'],
            additionalProperties: false,
          },
          items: {
            type: 'array',
            minItems: 1,
            maxItems: 20,
            items: {
              type: 'object',
              properties: {
                kind: {
                  type: 'string',
                  enum: [
                    'fact',
                    'decision',
                    'preference',
                    'project_state',
                    'open_loop',
                    'correction',
                    'relationship_note',
                    'do_not_repeat',
                  ],
                },
                redacted_summary: {
                  type: 'string',
                  maxLength: 1200,
                  description:
                    'Minimal redacted summary. Do not include raw transcript or full chat history.',
                },
                confidence: { type: 'number', minimum: 0, maximum: 1 },
                evidence_hint: {
                  type: 'string',
                  enum: [
                    'user_selected',
                    'current_turn',
                    'assistant_inferred',
                    'tool_result',
                  ],
                },
                privacy_tier: {
                  type: 'string',
                  enum: ['normal', 'sensitive', 'private'],
                },
                retention: {
                  type: 'string',
                  enum: ['session', 'project', 'long_term'],
                },
                tags: {
                  type: 'array',
                  items: { type: 'string' },
                  maxItems: 20,
                },
              },
              required: [
                'kind',
                'redacted_summary',
                'confidence',
                'evidence_hint',
                'privacy_tier',
                'retention',
              ],
              additionalProperties: false,
            },
          },
          raw_input_included: { type: 'boolean', const: false },
        },
        required: ['schema', 'source', 'items', 'raw_input_included'],
        additionalProperties: false,
      },
    },
    {
      name: 'pulse_recall',
      description:
        'Recall host-extracted Pulse memories from structured capsules. Returns summaries and evidence refs, not raw transcript dumps.',
      inputSchema: {
        type: 'object',
        properties: {
          query: { type: 'string' },
          scope: {
            type: 'string',
            enum: ['session', 'project', 'user'],
            description: 'Optional retention scope filter.',
          },
          limit: {
            type: 'integer',
            minimum: 1,
            maximum: 50,
            description: 'Max memory items to return. Defaults to 5.',
          },
          privacy_ceiling: {
            type: 'string',
            enum: ['normal', 'sensitive', 'private'],
            description:
              'Maximum privacy tier to return. Defaults to sensitive.',
          },
        },
        required: ['query'],
        additionalProperties: false,
      },
    },
    {
      name: 'pulse_context_query',
      description:
        'Local/dev tool: query Pulse for a typed pulse.context.v1 projection from the existing retrieval/graph engine. Use for agent context, not raw search dumps. Optional user_state.context_flags (object of flag name → 0..1 weight, e.g. {"deadline_pressure": 0.9}; weights >= 0.5 are active) steers state-aware ranking: remembered items tagged "state:<flag>" are boosted while that flag is active, and items tagged "state:calm" when user_state is present with no active flag. Not store-safe for public connector builds until trace/user_state/privacy controls are narrowed.',
      inputSchema: {
        type: 'object',
        properties: {
          query: { type: 'string' },
          mode: {
            type: 'string',
            enum: ['auto', 'factual', 'empathic', 'chain'],
          },
          top_k: { type: 'integer', minimum: 1, maximum: 50 },
          scope: { type: 'string', enum: ['user', 'assistant', 'shared'] },
          audience: { type: 'string' },
          privacy_floor: { type: 'string' },
          include_trace: { type: 'boolean' },
          user_state: { type: 'object', additionalProperties: true },
          domain_hints: { type: 'array', items: { type: 'string' } },
          graph_mode: {
            type: 'string',
            enum: ['off', 'anchored', 'walk'],
            description:
              'Temporal entity-graph retrieval override. Default (server) = anchored. "walk" adds typed relation traversal (multi-hop).',
          },
        },
        required: ['query'],
        additionalProperties: false,
      },
    },
    {
      name: 'pulse_graph_delta',
      description:
        'Propose a private host-extracted pulse.semantic_delta.v1 graph delta. Product Personal/Desk mode returns a pending Memory Tray receipt and is saved only after a created, deduplicated, or updated receipt; Local Preview stores immediately. Use for durable semantic nodes, relations, facts, events, decisions, open loops, do-not-repeat, or emotional/state anchors. Never send raw transcript, secrets, credentials, local paths, or store-everything payloads.',
      inputSchema: {
        type: 'object',
        properties: {
          schema: { type: 'string', const: 'pulse.semantic_delta.v1' },
          source: {
            type: 'object',
            properties: {
              host: {
                type: 'string',
                enum: [
                  'chatgpt',
                  'claude',
                  'codex',
                  'claude-code',
                  'gemini-cli',
                  'cursor',
                  'langchain',
                  'crewai',
                ],
              },
              conversation_scope: {
                type: 'string',
                enum: [
                  'current_turn',
                  'user_selected_excerpt',
                  'project_context',
                ],
              },
              timestamp: {
                type: 'string',
                description: 'RFC3339 timestamp for the source turn/excerpt.',
              },
              thread_id: {
                type: 'string',
                description:
                  'Durable topic id, for example pulse-distribution or garden-atlas.',
              },
              session_id: { type: 'string' },
              project_id: { type: 'string' },
            },
            required: ['host', 'conversation_scope', 'timestamp'],
            additionalProperties: false,
          },
          nodes: {
            type: 'array',
            maxItems: 30,
            items: {
              type: 'object',
              properties: {
                client_id: { type: 'string' },
                kind: {
                  type: 'string',
                  enum: [
                    'person',
                    'place',
                    'project',
                    'org',
                    'product',
                    'community',
                    'skill',
                    'concept',
                    'thing',
                    'event_series',
                  ],
                },
                canonical_name: { type: 'string', maxLength: 160 },
                summary: { type: 'string', maxLength: 1200 },
                aliases: {
                  type: 'array',
                  maxItems: 20,
                  items: { type: 'string', maxLength: 160 },
                },
                salience: { type: 'number', minimum: 0, maximum: 1 },
                emotional_weight: { type: 'number', minimum: 0, maximum: 1 },
                privacy_tier: {
                  type: 'string',
                  enum: ['normal', 'sensitive', 'private'],
                },
                domain: {
                  type: 'string',
                  enum: ['real', 'fiction_content', 'fiction_meta', 'meta_authorial'],
                },
              },
              required: ['client_id', 'kind', 'canonical_name', 'privacy_tier'],
              additionalProperties: false,
            },
          },
          edges: {
            type: 'array',
            maxItems: 50,
            items: {
              type: 'object',
              properties: {
                from: { type: 'string' },
                to: { type: 'string' },
                kind: { type: 'string', maxLength: 64 },
                summary: { type: 'string', maxLength: 1200 },
                strength: { type: 'number', minimum: 0, maximum: 1 },
                privacy_tier: {
                  type: 'string',
                  enum: ['normal', 'sensitive', 'private'],
                },
              },
              required: ['from', 'to', 'kind', 'privacy_tier'],
              additionalProperties: false,
            },
          },
          facts: {
            type: 'array',
            maxItems: 50,
            items: {
              type: 'object',
              properties: {
                node: { type: 'string' },
                text: { type: 'string', maxLength: 1200 },
                predicate: {
                  type: 'string',
                  maxLength: 160,
                  description:
                    'Optional structured assertion predicate. If set, object_text must also be set; Pulse uses subject node + predicate as the stable claim key.',
                },
                object_text: {
                  type: 'string',
                  maxLength: 1200,
                  description:
                    'Optional structured assertion object/value. A later delta with the same subject+predicate and a different object supersedes the prior assertion.',
                },
                valid_from: {
                  type: 'string',
                  description: 'Optional RFC3339 valid-time for when this claim became true.',
                },
                source_event_refs: {
                  type: 'array',
                  maxItems: 20,
                  items: { type: 'string' },
                  description:
                    'Optional semantic_delta event client_ids that prove this fact. If set, every ref must match an event in the same delta.',
                },
                scope_type: {
                  type: 'string',
                  enum: ['personal', 'project', 'repo', 'agent', 'session'],
                },
                scope_id: { type: 'string', maxLength: 160 },
                visibility: {
                  type: 'string',
                  enum: ['private', 'shared'],
                },
                confidence: { type: 'number', minimum: 0, maximum: 1 },
                privacy_tier: {
                  type: 'string',
                  enum: ['normal', 'sensitive', 'private'],
                },
                domain: {
                  type: 'string',
                  enum: ['real', 'fiction_content', 'fiction_meta', 'meta_authorial'],
                },
              },
              required: ['node', 'text', 'confidence', 'privacy_tier'],
              additionalProperties: false,
            },
          },
          events: {
            type: 'array',
            maxItems: 20,
            items: {
              type: 'object',
              properties: {
                client_id: { type: 'string' },
                title: { type: 'string', maxLength: 180 },
                summary: { type: 'string', maxLength: 1200 },
                sentiment: { type: 'string', maxLength: 240 },
                entity_refs: {
                  type: 'array',
                  maxItems: 20,
                  items: { type: 'string' },
                },
                emotional_weight: { type: 'number', minimum: 0, maximum: 1 },
                confidence: { type: 'number', minimum: 0, maximum: 1 },
                privacy_tier: {
                  type: 'string',
                  enum: ['normal', 'sensitive', 'private'],
                },
                domain: {
                  type: 'string',
                  enum: ['real', 'fiction_content', 'fiction_meta', 'meta_authorial'],
                },
              },
              required: [
                'client_id',
                'title',
                'summary',
                'confidence',
                'privacy_tier',
              ],
              additionalProperties: false,
            },
          },
          continuity: {
            type: 'object',
            properties: {
              summary: { type: 'string', maxLength: 1200 },
              decisions: {
                type: 'array',
                maxItems: 20,
                items: { type: 'string', maxLength: 1200 },
              },
              open_loops: {
                type: 'array',
                maxItems: 20,
                items: { type: 'string', maxLength: 1200 },
              },
              do_not_repeat: {
                type: 'array',
                maxItems: 20,
                items: { type: 'string', maxLength: 1200 },
              },
              emotional_anchors: {
                type: 'array',
                maxItems: 20,
                items: { type: 'string', maxLength: 1200 },
              },
              state_signals: {
                type: 'array',
                maxItems: 20,
                items: { type: 'string', maxLength: 1200 },
              },
              active_threads: {
                type: 'array',
                maxItems: 20,
                items: { type: 'string', maxLength: 1200 },
              },
              review_insights: {
                type: 'array',
                maxItems: 20,
                items: { type: 'string', maxLength: 1200 },
              },
            },
            required: ['summary'],
            additionalProperties: false,
          },
          raw_input_included: { type: 'boolean', const: false },
        },
        required: ['schema', 'source', 'raw_input_included'],
        additionalProperties: false,
      },
    },
    {
      name: 'pulse_resume',
      description:
        'Return a small Pulse continuity block for a new host session: where we left off, decisions, open loops, do-not-repeat, emotional/state context, suggested next step, and evidence refs. Use at session start before asking the user to re-explain.',
      inputSchema: {
        type: 'object',
        properties: {
          thread_id: {
            type: 'string',
            description:
              'Durable topic id such as pulse-distribution, garden-atlas, or paper-publication.',
          },
          project_id: {
            type: 'string',
            description: 'Optional project fallback when thread_id is unknown.',
          },
          session_id: {
            type: 'string',
            description: 'Optional current host session/run id.',
          },
          host: {
            type: 'string',
            enum: [
              'chatgpt',
              'claude',
              'codex',
              'claude-code',
              'gemini-cli',
              'cursor',
              'langchain',
              'crewai',
            ],
          },
          token_budget: {
            type: 'integer',
            minimum: 400,
            maximum: 2000,
            description: 'Resume token budget. Defaults to 1200; hard max 2000.',
          },
        },
        additionalProperties: false,
      },
    },
    {
      name: 'pulse_tray',
      description: 'Inspect pending and terminal private Memory Tray candidates and their truthful receipt state.',
      inputSchema: {
        type: 'object',
        properties: { limit: { type: 'integer', minimum: 1, maximum: 100 } },
        additionalProperties: false,
      },
    },
    {
      name: 'pulse_status',
      description:
        'Show Pulse billing mode, host, backend LLM state, raw capture state, retention state, and last write. Local filesystem paths are redacted for MCP hosts.',
      inputSchema: {
        type: 'object',
        properties: {},
        additionalProperties: false,
      },
    },
    {
      name: 'pulse_forget',
      description: 'Delete one host-extracted Pulse memory capsule item by id. Does not delete continuity checkpoints or graph rows.',
      inputSchema: {
        type: 'object',
        properties: {
          id: { type: 'string' },
        },
        required: ['id'],
        additionalProperties: false,
      },
    },
    {
      name: 'pulse_wipe',
      description:
        'Delete all host-extracted memory capsules, continuity checkpoints/observations/sessions/threads, and host-extracted graph rows. Use only after explicit user confirmation.',
      inputSchema: {
        type: 'object',
        properties: {
          confirm: {
            type: 'string',
            const: 'wipe pulse memory',
          },
        },
        required: ['confirm'],
        additionalProperties: false,
      },
    },
      ];
    return {
      tools: CODEX_HOST_ADAPTER
        ? tools.filter((tool) => !['pulse_forget', 'pulse_wipe', 'pulse_graph_delta'].includes(tool.name))
        : tools,
    };
  });

  server.setRequestHandler(CallToolRequestSchema, async (request, extra) => {
    const { name, arguments: args } = request.params;
	const invocationKey = mcpRequestIdempotencyKey(extra.sessionId, extra.requestId);

    try {
      await assertCodexBindingCurrent();
      if (runtimeMode === 'team-remote') {
        if (!teamContext) throw new Error('team request context is unavailable');
        const contracts = await loadTeamRemoteContracts();
        const { isTeamToolName, teamNotReadyResult } = contracts;
        if (!isTeamToolName(name)) {
          throw new Error('Unknown team tool');
        }
        if (
          name === 'pulse_team_status' || name === 'pulse_team_remember' ||
          name === 'pulse_team_graph_delta' ||
          name === 'pulse_team_recall' || name === 'pulse_team_context_query' ||
          name === 'pulse_team_resume' || name === 'pulse_team_inspect' ||
          name === 'pulse_team_audit' || name === 'pulse_team_delete' ||
          name === 'pulse_team_delete_status'
        ) {
          const methodClass: 'read' | 'write' | 'delete' = name === 'pulse_team_delete'
            ? 'delete'
            : name === 'pulse_team_remember' || name === 'pulse_team_graph_delta'
              ? 'write'
              : 'read';
          if (!teamDomain) {
            reportTeamDomainFailure(
              teamSecurityEventSink, teamContext.request_id, 'shared_memory_unavailable', methodClass,
            );
            return contracts.teamDomainErrorResult('shared_memory_unavailable');
          }
          try {
            let result: unknown;
            if (name === 'pulse_team_status') result = await teamDomain.status(args);
            else if (name === 'pulse_team_remember') result = await teamDomain.remember(args);
            else if (name === 'pulse_team_graph_delta') result = await teamDomain.graphDelta(args);
            else if (name === 'pulse_team_recall') result = await teamDomain.recall(args);
            else if (name === 'pulse_team_context_query') result = await teamDomain.contextQuery(args);
            else if (name === 'pulse_team_resume') result = await teamDomain.resume(args);
            else if (name === 'pulse_team_inspect') result = await teamDomain.inspect(args);
            else if (name === 'pulse_team_audit') result = await teamDomain.audit(args);
            else if (name === 'pulse_team_delete') result = await teamDomain.delete(args);
            else result = await teamDomain.deleteStatus(args);
            return jsonText(result);
          } catch (error) {
            if (error instanceof contracts.TeamContractError) {
              reportTeamDomainFailure(
                teamSecurityEventSink, teamContext.request_id, 'invalid_contract', methodClass,
              );
              return contracts.teamDomainErrorResult('invalid_team_contract');
            }
            if (error instanceof contracts.TeamDomainError) {
              reportTeamDomainFailure(
                teamSecurityEventSink, teamContext.request_id, error.code, methodClass,
              );
              return contracts.teamDomainErrorResult(error.code);
            }
            reportTeamDomainFailure(
              teamSecurityEventSink, teamContext.request_id, 'unexpected_domain_failure', methodClass,
            );
            return contracts.teamDomainErrorResult('shared_memory_unavailable');
          }
        }
        return teamNotReadyResult(name);
      }
      if (resolvedEngine === 'standalone') {
        return standaloneResult(name, args);
      }
      // Serialize calls until the engine is resolved: without this, two
      // concurrent first calls can split between daemon and standalone
      // when the daemon dies between them.
      let releaseGate: (() => void) | undefined;
      if (resolvedEngine === null) {
        while (firstCallGate) {
          await firstCallGate;
        }
        if (resolvedEngine !== null) {
          return resolvedEngine === 'standalone'
            ? standaloneResult(name, args)
            : await daemonToolCall(name, args, invocationKey);
        }
        firstCallGate = new Promise((resolve) => {
          releaseGate = resolve;
        });
      }
      try {
        const out = await daemonToolCall(name, args, invocationKey);
        resolvedEngine = 'daemon';
        return out;
      } catch (err: unknown) {
        if (err instanceof Error && err.message.startsWith('Pulse HTTP')) {
          // The daemon answered (even with an error): it exists — lock to it.
          resolvedEngine = 'daemon';
          throw err;
        }
        if (resolvedEngine !== 'daemon' && isConnectionError(err)) {
          if (resolvedEngine === null) {
            resolvedEngine = 'standalone';
            // eslint-disable-next-line no-console
            console.error(
              `[pulse-mcp v${VERSION}] no Pulse daemon at ${PULSE_BASE_URL}; using standalone lite store`,
            );
          }
          return standaloneResult(name, args);
        }
        throw err;
      } finally {
        if (releaseGate) {
          firstCallGate = null;
          releaseGate();
        }
      }
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : String(err);
      return {
        isError: true,
        content: [{ type: 'text', text: `Tool error: ${message}` }],
      };
    }
  });

  return server;
}

async function loadTeamRemoteContracts() {
  // Keep the entire team-only branch lazy. U3 can add JWT/JWKS dependencies
  // behind the same runtime boundary without raising Local Preview's Node floor.
  return import('./team-contracts.js');
}

function standaloneResult(name: string, args: unknown) {
  const out = resolveStandaloneStore().call(name, args);
  return jsonText(name === 'pulse_status' ? redactStatusForMcp(out) : out);
}

function resolveStandaloneStore(): StandaloneStore {
  if (RUNTIME_MODE === 'team-remote') {
    throw new Error('team-remote cannot construct or use standalone storage');
  }
  standaloneStore ??= new StandaloneStore(PULSE_DATA_DIR);
  return standaloneStore;
}

async function daemonToolCall(name: string, args: Record<string, unknown> | undefined, invocationKey: string) {
  if (name === 'pulse_remember') {
    if (CODEX_HOST_ADAPTER) {
      const context = await consumeCodexTurnContext('mcp__pulse-product__pulse_remember', args);
      const body = codexFinalizeBody(args as unknown as MemoryCapsule, context);
      const out = await pulseFetch<unknown>(
        '/turn/finalize', body, 'POST', String(body.idempotency_key),
      );
      assertTruthfulWriteResponse(out);
      try {
        writeCodexFinalizeMarker(context, out);
      } catch {
        // The durable daemon receipt is authoritative. A missing local marker
        // only causes Stop to perform the bounded server-side idempotency check.
      }
      return jsonText(out);
    }
    const out = await pulseFetch<unknown>(
      '/memory/remember',
      args as unknown as MemoryCapsule,
      'POST',
      invocationKey,
    );
    assertTruthfulWriteResponse(out);
    return jsonText(out);
  }

  if (name === 'pulse_recall') {
    const out = await pulseFetch('/memory/recall', args as unknown as RecallBody);
    return jsonText(out);
  }

  if (name === 'pulse_context_query') {
    const out = await pulseFetch('/context/query', args as unknown as ContextQueryBody);
    return jsonText(out);
  }

  if (name === 'pulse_graph_delta') {
    if (CODEX_HOST_ADAPTER) throw new Error('Codex graph writes require the governed candidate path');
    const out = await pulseFetch('/graph/delta', args as unknown as SemanticDeltaBody, 'POST', invocationKey);
    assertTruthfulWriteResponse(out);
    return jsonText(out);
  }

  if (name === 'pulse_resume') {
    const out = await pulseFetch('/continuity/resume', args as unknown as ResumeBody);
    return jsonText(out);
  }

  if (name === 'pulse_status') {
    const out = await pulseFetch('/memory/status', undefined, 'GET');
    return jsonText(redactStatusForMcp(out));
  }

  if (name === 'pulse_tray') {
    const limit = args?.limit === undefined ? 50 : Number(args.limit);
    if (!Number.isInteger(limit) || limit < 1 || limit > 100) throw new Error('pulse_tray limit must be 1..100');
    const out = await pulseFetch(`/memory/tray?limit=${limit}`, undefined, 'GET');
    return jsonText(out);
  }

  if (name === 'pulse_forget') {
    if (CODEX_HOST_ADAPTER) throw new Error('Pulse deletion is user-controlled through Viewer or the explicit CLI');
    const out = await pulseFetch('/memory/delete', { id: args?.id }, 'POST', invocationKey);
    assertTruthfulDeletionReceipt(out, String(args?.id || ''));
    return jsonText(out);
  }

  if (name === 'pulse_wipe') {
    if (CODEX_HOST_ADAPTER) throw new Error('Pulse wipe is user-controlled through Viewer or the explicit CLI');
    if (args?.confirm !== 'wipe pulse memory') {
      throw new Error('pulse_wipe requires confirm="wipe pulse memory"');
    }
    const out = await pulseFetch('/memory/wipe', { confirm: 'wipe pulse memory' }, 'POST', invocationKey);
    return jsonText(out);
  }

  throw new Error(`Unknown tool: ${name}`);
}

async function main(): Promise<void> {
  if (RUNTIME_MODE !== 'local-stdio') {
    await startHttpMode();
    return;
  }
  const server = createPulseMcpServer();
  const transport = new StdioServerTransport();
  await server.connect(transport);
  // eslint-disable-next-line no-console
  console.error(
    `[pulse-mcp v${VERSION}] host-extracted stdio connected; engine: ${
      ENGINE_MODE === 'auto'
        ? `auto (daemon at ${PULSE_BASE_URL} when reachable, standalone lite store otherwise)`
        : ENGINE_MODE === 'daemon'
          ? `daemon at ${PULSE_BASE_URL}`
          : 'standalone lite store'
    }`,
  );
}

async function startHttpMode(): Promise<void> {
  const host = argValue('--host') ?? process.env.PULSE_MCP_HOST ?? '127.0.0.1';
  const port = Number(argValue('--port') ?? process.env.PULSE_MCP_PORT ?? 8787);
  const bearer = process.env.PULSE_REMOTE_BEARER ?? '';
  const publicBaseURL = trimTrailingSlash(process.env.PULSE_REMOTE_PUBLIC_BASE_URL ?? '');
  // OAuth issuer identifiers are exact identities. A trailing slash may be
  // canonical and must survive metadata, JWT, and principal mapping unchanged.
  const authIssuer = process.env.PULSE_REMOTE_AUTH_ISSUER ?? '';
  const oauthMode = publicBaseURL !== '' && authIssuer !== '';
  const oauthDevMode = oauthMode && process.env.PULSE_REMOTE_OAUTH_DEV === '1';
  const devAuthCodes = new Map<string, DevOAuthCode>();
  const devAccessTokens = new Map<string, DevOAuthToken>();
  const devRefreshTokens = new Map<string, DevOAuthToken>();
  // Persist OAuth tokens to disk so an mcp restart/redeploy does NOT log out
  // connected clients (e.g. Claude.ai). Load non-expired tokens on start.
  const oauthTokensFile = join(PULSE_DATA_DIR, 'oauth-tokens.json');
  if (RUNTIME_MODE === 'development-http') {
    loadOAuthTokens(oauthTokensFile, devAccessTokens, devRefreshTokens);
  }
  const persistOAuth = () => {
    if (RUNTIME_MODE === 'development-http') {
      persistOAuthTokens(oauthTokensFile, devAccessTokens, devRefreshTokens);
    }
  };
  const allowUnauthenticated =
    process.env.PULSE_REMOTE_ALLOW_UNAUTHENTICATED === '1' ||
    args.includes('--allow-unauthenticated');
  const publicBind = !isLoopbackHost(host);
  const teamSecurity = RUNTIME_MODE === 'team-remote'
    ? await loadTeamRemoteSecurity(publicBaseURL, authIssuer)
    : undefined;
  const teamAllowedOrigins = teamSecurity
    ? parseTeamAllowedOrigins(publicBaseURL, process.env.PULSE_REMOTE_ALLOWED_ORIGINS)
    : new Set<string>();
  let teamRequestsInFlight = 0;

  if (oauthMode && (!isHTTPSURL(publicBaseURL) || !isHTTPSURL(authIssuer))) {
    throw new Error('OAuth HTTP MCP mode requires HTTPS public base and authorization issuer URLs');
  }
  if (!bearer && publicBind && !oauthMode) {
    throw new Error('refusing authless public HTTP MCP bind; set PULSE_REMOTE_BEARER');
  }
  if (!bearer && !allowUnauthenticated && !oauthMode) {
    throw new Error(
      '--http requires PULSE_REMOTE_BEARER. For local experiments only, set PULSE_REMOTE_ALLOW_UNAUTHENTICATED=1 or pass --allow-unauthenticated.',
    );
  }

  const httpServer = createServer({
    maxHeaderSize: 16 * 1024,
    headersTimeout: 10_000,
    requestTimeout: 15_000,
    keepAliveTimeout: 5_000,
  }, async (req, res) => {
    try {
    const requestId = randomUUID();
    const requestURL = parseRequestTarget(req.url);
    const path = requestURL.pathname;
    const requestOrigin = singleHeader(req.headers.origin);
    if (teamSecurity && hasInvalidHostHeader(req)) {
      res.setHeader('Connection', 'close');
      writeJSON(res, { error: 'invalid_request' }, 400);
      return;
    }
    if (teamSecurity && hasDuplicateHeader(req, 'origin')) {
      recordTeamSecurityEvent(teamSecurity, {
        eventType: 'authentication_denied', reasonCode: 'invalid_credential',
        methodClass: 'other', requestId,
      });
      writeJSON(res, { error: 'origin_not_allowed' }, 403);
      return;
    }
    if (teamSecurity && requestOrigin !== '' && !teamAllowedOrigins.has(requestOrigin)) {
      recordTeamSecurityEvent(teamSecurity, {
        eventType: 'authentication_denied', reasonCode: 'invalid_credential',
        methodClass: 'other', requestId,
      });
      writeJSON(res, { error: 'origin_not_allowed' }, 403);
      return;
    }
    if (req.method === 'GET' && path === '/health' && (!teamSecurity || requestURL.search === '')) {
      if (teamSecurity) {
        writeTeamCors(res, requestOrigin);
      } else {
        writeCors(res);
      }
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify(teamSecurity ? { ok: true } : {
          ok: true,
          transport: 'streamable-http',
          path: '/mcp',
          auth: oauthMode ? 'oauth' : bearer ? 'bearer' : 'none',
        }));
      return;
    }
    if (req.method === 'GET' && oauthMode && isProtectedResourceMetadataPath(path) && (!teamSecurity || requestURL.search === '')) {
      if (teamSecurity) {
        writeTeamCors(res, requestOrigin);
        writeJSON(res, teamSecurity.metadata);
      } else {
        writeCors(res);
        writeJSON(res, protectedResourceMetadata(publicBaseURL, authIssuer));
      }
      return;
    }
    if (oauthDevMode && isAuthorizationServerMetadataPath(path)) {
      writeCors(res);
      writeJSON(res, authorizationServerMetadata(publicBaseURL));
      return;
    }
    if (oauthDevMode && path === '/register' && req.method === 'POST') {
      writeCors(res);
      const body = await readRequestJSON(req) as Record<string, unknown> | undefined;
      const redirectURIs = Array.isArray(body?.redirect_uris)
        ? body.redirect_uris.filter((uri): uri is string => typeof uri === 'string')
        : [];
      writeJSON(res, {
        client_id: 'pulse-dev-client',
        client_id_issued_at: Math.floor(Date.now() / 1000),
        token_endpoint_auth_method: 'none',
        redirect_uris: redirectURIs,
      }, 201);
      return;
    }
    if (oauthDevMode && path === '/authorize' && req.method === 'GET') {
      writeCors(res);
      handleDevAuthorize(req, res, devAuthCodes);
      return;
    }
    if (oauthDevMode && path === '/token' && req.method === 'POST') {
      writeCors(res);
      await handleDevToken(req, res, devAuthCodes, devAccessTokens, devRefreshTokens);
      persistOAuth();
      return;
    }
    if (teamSecurity && teamSecurity.isOwnerPublicPath(path)) {
      if (
        requestURL.search !== '' ||
        !teamSecurity.isExactOwnerBrowserRequest({
          origin: requestOrigin,
          host: singleHeader(req.headers.host),
          publicBaseURL,
          allowedOrigins: teamAllowedOrigins,
        })
      ) {
        writeJSON(res, { error: 'owner_request_denied', fallback: false }, 403);
        return;
      }
      writeOwnerCors(res, requestOrigin);
      if (req.method === 'OPTIONS') {
        res.writeHead(204);
        res.end();
        return;
      }
      if (req.method !== 'POST') {
        writeJSON(res, { error: 'method_not_allowed', fallback: false }, 405);
        return;
      }
      if (teamRequestsInFlight >= 64) {
        writeJSON(res, { error: 'owner_service_unavailable', fallback: false }, 503);
        return;
      }
      teamRequestsInFlight++;
      try {
        if (
          hasDuplicateHeader(req, 'authorization') || hasDuplicateHeader(req, 'content-type') ||
          hasDuplicateHeader(req, 'content-encoding') || hasDuplicateHeader(req, 'content-length') ||
          hasDuplicateHeader(req, 'transfer-encoding') ||
          singleHeader(req.headers['transfer-encoding']) !== ''
        ) {
          throw new teamSecurity.OAuthError('invalid_token');
        }
        const identity = await teamSecurity.verifier.verifyAuthorization(
          req.headers.authorization, ['pulse:owner'],
        );
        teamSecurity.ownerGateway.verifyRecentStepUp(identity);
        const contentEncoding = singleHeader(req.headers['content-encoding']);
        if (contentEncoding !== '' && contentEncoding.toLowerCase() !== 'identity') {
          writeJSON(res, { error: 'content_encoding_not_supported', fallback: false }, 415);
          return;
        }
        if (
          singleHeader(req.headers['content-type']).split(';', 1)[0]?.trim().toLowerCase() !==
          'application/json'
        ) {
          writeJSON(res, { error: 'content_type_not_supported', fallback: false }, 415);
          return;
        }
        const body = await readRequestJSON(req, 64 * 1024);
        const result = await teamSecurity.ownerGateway.call(path, identity, requestId, body);
        writeJSON(res, result);
      } catch (error) {
        if (teamSecurity.isOAuthError(error)) {
          writeTeamOAuthChallenge(res, publicBaseURL, error);
        } else if (teamSecurity.isOwnerGatewayError(error)) {
          writeJSON(res, { error: error.code, fallback: false }, error.status);
        } else if (error instanceof RequestBodyTooLargeError) {
          writeJSON(res, { error: 'invalid_owner_request', fallback: false }, 413);
        } else if (error instanceof SyntaxError) {
          writeJSON(res, { error: 'invalid_owner_request', fallback: false }, 400);
        } else {
          writeJSON(res, { error: 'owner_service_unavailable', fallback: false }, 503);
        }
      } finally {
        teamRequestsInFlight--;
      }
      return;
    }
    if (path !== '/mcp' || (teamSecurity && requestURL.search !== '')) {
      res.writeHead(404, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: 'not found' }));
      return;
    }
    if (req.method === 'OPTIONS') {
      if (teamSecurity) {
        writeTeamCors(res, requestOrigin);
      } else {
        writeCors(res);
      }
      res.writeHead(204);
      res.end();
      return;
    }
    if (teamSecurity) {
      if (req.method !== 'POST' && req.method !== 'GET' && req.method !== 'DELETE') {
        writeTeamCors(res, requestOrigin);
        writeJSON(res, { error: 'method_not_allowed' }, 405);
        return;
      }
      if (teamRequestsInFlight >= 64) {
        writeTeamCors(res, requestOrigin);
        writeJSON(res, { error: 'server_busy' }, 503);
        return;
      }
      teamRequestsInFlight++;
      let methodClass: 'read' | 'write' | 'delete' | 'other' = 'other';
      try {
        if (hasDuplicateHeader(req, 'authorization')) {
          throw new teamSecurity.OAuthError('invalid_token');
        }
        // Baseline validation happens before reading or parsing the request body.
        const identity = await teamSecurity.requestSecurity.authenticateBeforeBody(
          req.headers.authorization,
        );
        if (req.method !== 'POST' && hasUnexpectedRequestBody(req)) {
          res.setHeader('Connection', 'close');
          writeTeamCors(res, requestOrigin);
          writeJSON(res, { error: 'request_body_not_allowed' }, 400);
          return;
        }
        const contentEncoding = singleHeader(req.headers['content-encoding']);
        if (contentEncoding !== '' && contentEncoding.toLowerCase() !== 'identity') {
          writeTeamCors(res, requestOrigin);
          writeJSON(res, { error: 'content_encoding_not_supported' }, 415);
          return;
        }
        let parsedBody: unknown;
        let teamContext: Readonly<TeamPrincipalContext>;
        let teamDomain: Readonly<BoundTeamDomain> | undefined;
        if (req.method === 'POST') {
          if (singleHeader(req.headers['content-type']).split(';', 1)[0]?.trim().toLowerCase() !== 'application/json') {
            writeTeamCors(res, requestOrigin);
            writeJSON(res, { error: 'content_type_not_supported' }, 415);
            return;
          }
          parsedBody = await readRequestJSON(req, 1024 * 1024);
          const required = teamSecurity.requiredCapabilities(parsedBody);
          methodClass = methodClassForCapabilities(required);
          teamContext = await teamSecurity.requestSecurity.resolveAfterBody({
            authorization: req.headers.authorization,
            baseline: identity,
            body: parsedBody,
            requestId,
          });
          if (required.some((capability) => capability !== 'pulse:connect')) {
            teamDomain = teamSecurity.principalClient.bindDomain(identity, teamContext);
          }
        } else {
          teamContext = await teamSecurity.requestSecurity.resolveBaseline(identity, requestId);
        }
        writeTeamCors(res, requestOrigin);
        await dispatchMcpRequest(
          req, res, parsedBody, teamContext, teamDomain,
          (event) => recordTeamSecurityEvent(teamSecurity, event),
        );
      } catch (error) {
        if (terminateStartedResponse(res)) return;
        writeTeamCors(res, requestOrigin);
        if (teamSecurity.isOAuthError(error)) {
          if (error.code === 'insufficient_scope') {
            recordTeamSecurityEvent(teamSecurity, {
              eventType: 'authorization_denied', reasonCode: 'insufficient_scope', methodClass, requestId,
            });
          } else {
            recordTeamSecurityEvent(teamSecurity, {
              eventType: 'authentication_denied',
              reasonCode: error.reasonCode === 'insufficient_scope' ? 'invalid_credential' : error.reasonCode,
              methodClass,
              requestId,
            });
          }
          writeTeamOAuthChallenge(res, publicBaseURL, error);
        } else if (teamSecurity.isPrincipalError(error)) {
          if (error.code === 'principal_revoked') {
            recordTeamSecurityEvent(teamSecurity, {
              eventType: 'authorization_denied', reasonCode: 'principal_revoked',
              methodClass, requestId,
            });
            writeJSON(res, { error: 'principal_revoked' }, 403);
          } else if (error.code === 'principal_store_unavailable') {
            recordTeamSecurityEvent(teamSecurity, {
              eventType: 'audit_degraded', reasonCode: 'store_unavailable',
              methodClass, requestId,
            });
            writeJSON(res, { error: 'shared_memory_unavailable', fallback: false }, 503);
          } else {
            recordTeamSecurityEvent(teamSecurity, {
              eventType: 'principal_assertion_denied',
              reasonCode: error.code === 'principal_replayed'
                ? 'assertion_replayed'
                : error.code === 'principal_binding_mismatch'
                  ? 'assertion_binding_mismatch'
                  : 'assertion_invalid',
              methodClass, requestId,
            });
            writeJSON(res, { error: 'shared_memory_unavailable', fallback: false }, 503);
          }
        } else if (error instanceof RequestBodyTooLargeError) {
          writeJSON(res, { error: 'request_too_large' }, 413);
        } else if (error instanceof SyntaxError || (error instanceof Error && /Unknown team tool/.test(error.message))) {
          writeJSON(res, { error: 'invalid_request' }, 400);
        } else {
          recordTeamSecurityEvent(teamSecurity, {
            eventType: 'audit_degraded', reasonCode: 'internal_failure', methodClass, requestId,
          });
          writeJSON(res, { error: 'shared_memory_unavailable', fallback: false }, 503);
        }
      } finally {
        teamRequestsInFlight--;
      }
      return;
    }
    let parsedBody: unknown;
    if (oauthMode && req.method === 'POST') {
      parsedBody = await readRequestJSON(req);
      if (
        RUNTIME_MODE !== 'team-remote' &&
        callsProtectedTool(parsedBody) &&
        !isAuthorized(req, bearer, devAccessTokens)
      ) {
        writeOAuthChallenge(res, publicBaseURL);
        return;
      }
    }
    if (!oauthMode && !allowUnauthenticated && req.headers.authorization !== `Bearer ${bearer}`) {
      writeCors(res);
      res.writeHead(401, {
        'Content-Type': 'application/json',
        'WWW-Authenticate': 'Bearer',
      });
      res.end(JSON.stringify({ error: 'unauthorized' }));
      return;
    }
    try {
      writeCors(res);
      await dispatchMcpRequest(req, res, parsedBody);
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : String(err);
      if (!res.headersSent) {
        res.writeHead(500, { 'Content-Type': 'application/json' });
      }
      res.end(JSON.stringify({
        jsonrpc: '2.0',
        error: { code: -32603, message },
        id: null,
      }));
    }
    } catch (error) {
      terminateHttpRequest(res, error instanceof InvalidRequestTargetError ? 400 : 500);
    }
  });
  if (teamSecurity) {
    httpServer.maxConnections = 128;
    httpServer.maxHeadersCount = 64;
    httpServer.maxRequestsPerSocket = 100;
  }

  await new Promise<void>((resolve, reject) => {
    httpServer.once('error', reject);
    httpServer.listen(port, host, () => resolve());
  });
  const address = httpServer.address();
  const actualPort =
    typeof address === 'object' && address !== null ? address.port : port;
  // eslint-disable-next-line no-console
  console.error(
    `[pulse-mcp v${VERSION}] Streamable HTTP listening on http://${host}:${actualPort}/mcp; backing Pulse: ${PULSE_BASE_URL}`,
  );

  let shuttingDown = false;
  const shutdown = async () => {
    if (shuttingDown) return;
    shuttingDown = true;
    await new Promise<void>((resolve) => httpServer.close(() => resolve()));
    if (teamSecurity) {
      await drainSecurityReporterForShutdown(teamSecurity.securityReporter, 2_000);
    }
    process.exit(0);
  };
  process.once('SIGTERM', shutdown);
  process.once('SIGINT', shutdown);
}

async function dispatchMcpRequest(
  req: IncomingMessage,
  res: ServerResponse,
  parsedBody: unknown,
  teamContext?: Readonly<TeamPrincipalContext>,
  teamDomain?: Readonly<BoundTeamDomain>,
  teamSecurityEventSink?: (event: GatewaySecurityEventInput) => void,
): Promise<void> {
  const requestServer = createPulseMcpServer(
    RUNTIME_MODE, teamContext, teamDomain, teamSecurityEventSink,
  );
  const transport = new StreamableHTTPServerTransport({
    sessionIdGenerator: undefined,
  });
  let cleaned = false;
  const cleanup = () => {
    if (cleaned) return;
    cleaned = true;
    void Promise.allSettled([transport.close(), requestServer.close()]);
  };
  try {
    await requestServer.connect(transport);
    // Register before handleRequest: it may synchronously close the response.
    res.once('close', cleanup);
    await transport.handleRequest(req, res, parsedBody);
  } catch (error) {
    cleanup();
    throw error;
  }
}

function reportTeamDomainFailure(
  sink: ((event: GatewaySecurityEventInput) => void) | undefined,
  requestId: string,
  code: string,
  methodClass: 'read' | 'write' | 'delete' = 'write',
): void {
  if (!sink) return;
  let event: GatewaySecurityEventInput | undefined;
  switch (code) {
  case 'principal_revoked':
    event = {
      eventType: 'authorization_denied', reasonCode: 'principal_revoked',
      methodClass, requestId,
    };
    break;
  case 'policy_denied':
  case 'not_found':
  case 'concealed_not_found':
    event = {
      eventType: 'authorization_denied', reasonCode: 'policy_denied',
      methodClass, requestId,
    };
    break;
  case 'authorization_stale':
    event = {
      eventType: 'authorization_denied', reasonCode: 'stale_generation',
      methodClass, requestId,
    };
    break;
  case 'invalid_principal':
    event = {
      eventType: 'principal_assertion_denied', reasonCode: 'assertion_invalid',
      methodClass, requestId,
    };
    break;
  case 'principal_request_mismatch':
    event = {
      eventType: 'principal_assertion_denied', reasonCode: 'assertion_binding_mismatch',
      methodClass, requestId,
    };
    break;
  case 'principal_replay':
    event = {
      eventType: 'principal_assertion_denied', reasonCode: 'assertion_replayed',
      methodClass, requestId,
    };
    break;
  case 'invalid_contract':
  case 'invalid_team_memory':
  case 'invalid_team_graph_delta':
  case 'invalid_team_recall':
  case 'invalid_team_context':
  case 'invalid_team_resume':
  case 'invalid_team_delete':
  case 'invalid_team_delete_status':
    event = {
      eventType: 'operation_denied', reasonCode: 'invalid_contract',
      methodClass, requestId,
    };
    break;
  case 'idempotency_conflict':
    event = {
      eventType: 'operation_denied', reasonCode: 'idempotency_conflict',
      methodClass, requestId,
    };
    break;
  case 'idempotency_in_progress':
    event = {
      eventType: 'operation_denied', reasonCode: 'operation_in_progress',
      methodClass, requestId,
    };
    break;
  case 'shared_memory_unavailable':
    event = {
      eventType: 'audit_degraded', reasonCode: 'store_unavailable',
      methodClass, requestId,
    };
    break;
  case 'idempotency_failed':
  case 'unexpected_domain_failure':
    event = {
      eventType: 'audit_degraded', reasonCode: 'internal_failure',
      methodClass, requestId,
    };
    break;
  }
  if (!event) return;
  try {
    sink(event);
  } catch {
    // A denied operation stays denied if its metadata-only signal degrades.
  }
}

function recordTeamSecurityEvent(
  security: Awaited<ReturnType<typeof loadTeamRemoteSecurity>>,
  event: GatewaySecurityEventInput,
): void {
  security.securityReporter.report(event);
}

function methodClassForCapabilities(capabilities: readonly string[]): 'read' | 'write' | 'delete' | 'other' {
  if (capabilities.includes('pulse:delete')) return 'delete';
  if (capabilities.includes('pulse:write')) return 'write';
  if (capabilities.some((value) => value === 'pulse:read' || value === 'pulse:audit' || value === 'pulse:status')) return 'read';
  return 'other';
}

async function loadTeamRemoteSecurity(publicBaseURL: string, authIssuer: string) {
  const [
    { OAuthResourceError, OAuthResourceVerifier, protectedResourceMetadata: teamMetadata },
    principal,
    contracts,
    owner,
  ] = await Promise.all([
    import('./oauth-resource.js'),
    import('./principal-context.js'),
    import('./team-contracts.js'),
    import('./owner-approval.js'),
  ]);
  const required = (name: string): string => {
    const value = process.env[name] ?? '';
    if (value === '' || value !== value.trim()) {
      throw new Error(`team-remote requires exact non-empty ${name}`);
    }
    return value;
  };
  const resource = `${publicBaseURL}/mcp`;
  const verifier = new OAuthResourceVerifier({
    issuer: authIssuer,
    resource,
    maxTokenLifetimeSeconds: parseBoundedEnvInt('PULSE_REMOTE_MAX_TOKEN_LIFETIME_SECONDS', 900, 1, 900),
  });
  const signer = principal.loadPrincipalSigner({
    privateKeyFile: required('PULSE_TEAM_PRINCIPAL_SIGNING_KEY_FILE'),
    keyId: required('PULSE_TEAM_PRINCIPAL_SIGNING_KID'),
    verifyKeyringFile: required('PULSE_TEAM_PRINCIPAL_VERIFY_KEYRING_FILE'),
    storeId: required('PULSE_TEAM_EXPECTED_STORE_ID'),
    teamId: required('PULSE_TEAM_EXPECTED_TEAM_ID'),
  });
  const principalClient = new principal.TeamPrincipalClient({
      daemonBaseURL: PULSE_BASE_URL,
      signer,
      apiKey: resolveApiKey,
    });
  const securityReporter = new principal.BoundedSecurityEventReporter(principalClient);
  const ownerGateway = new owner.OwnerApprovalGateway({
    daemonBaseURL: PULSE_BASE_URL,
    signer,
    apiKey: resolveApiKey,
    maxStepUpAgeSeconds: parseBoundedEnvInt(
      'PULSE_REMOTE_OWNER_MAX_AUTH_AGE_SECONDS', 300, 30, 300,
    ),
  });
  return {
    verifier,
    principalClient,
    securityReporter,
    requestSecurity: new principal.TeamRequestSecurity({ verifier, principalClient }),
    ownerGateway,
    isOwnerPublicPath: owner.isOwnerPublicPath,
    isExactOwnerBrowserRequest: owner.isExactOwnerBrowserRequest,
    isOwnerGatewayError: (error: unknown): error is InstanceType<typeof owner.OwnerGatewayError> =>
      error instanceof owner.OwnerGatewayError,
    requiredCapabilities: contracts.requiredTeamCapabilities,
    metadata: teamMetadata(resource, authIssuer),
    isOAuthError: (error: unknown): error is InstanceType<typeof OAuthResourceError> =>
      error instanceof OAuthResourceError,
    OAuthError: OAuthResourceError,
    isPrincipalError: (error: unknown): error is InstanceType<typeof principal.PrincipalCheckError> =>
      error instanceof principal.PrincipalCheckError,
  };
}

function parseBoundedEnvInt(name: string, fallback: number, min: number, max: number): number {
  const raw = process.env[name]?.trim();
  if (!raw) return fallback;
  const value = Number(raw);
  if (!Number.isInteger(value) || value < min || value > max) {
    throw new Error(`team-remote ${name} is outside its allowed bound`);
  }
  return value;
}

function parseTeamAllowedOrigins(publicBaseURL: string, configured: string | undefined): Set<string> {
  const values = configured?.split(',').map((value) => value.trim()).filter(Boolean) ?? [new URL(publicBaseURL).origin];
  if (values.length < 1 || values.length > 16 || values.some((value) => value === '*' || new URL(value).origin !== value)) {
    throw new Error('team-remote allowed origins must be exact URL origins');
  }
  return new Set(values);
}

function writeTeamCors(res: ServerResponse, origin: string): void {
  if (origin !== '') {
    res.setHeader('Access-Control-Allow-Origin', origin);
    res.setHeader('Vary', 'Origin');
  }
  res.setHeader('Access-Control-Allow-Methods', 'GET,POST,DELETE,OPTIONS');
  res.setHeader('Access-Control-Allow-Headers', 'Accept, Content-Type, Authorization, Mcp-Session-Id, MCP-Protocol-Version');
  res.setHeader('Access-Control-Expose-Headers', 'Mcp-Session-Id');
}

function writeOwnerCors(res: ServerResponse, origin: string): void {
  res.setHeader('Access-Control-Allow-Origin', origin);
  res.setHeader('Vary', 'Origin');
  res.setHeader('Access-Control-Allow-Methods', 'POST,OPTIONS');
  res.setHeader('Access-Control-Allow-Headers', 'Authorization, Content-Type');
}

function singleHeader(value: string | string[] | undefined): string {
  return typeof value === 'string' ? value : '';
}

function hasDuplicateHeader(req: IncomingMessage, name: string): boolean {
  let count = 0;
  for (let index = 0; index < req.rawHeaders.length; index += 2) {
    if (req.rawHeaders[index]?.toLowerCase() === name) count++;
  }
  return count > 1;
}

function writeTeamOAuthChallenge(
  res: ServerResponse,
  publicBaseURL: string,
  error: { status: 401 | 403; code: string; requiredCapabilities: string[] },
): void {
  const metadataURL = `${publicBaseURL}/.well-known/oauth-protected-resource/mcp`;
  const scope = error.requiredCapabilities.length > 0
    ? `, scope="${error.requiredCapabilities.join(' ')}"`
    : '';
  res.writeHead(error.status, {
    'Content-Type': 'application/json',
    'WWW-Authenticate': `Bearer resource_metadata="${metadataURL}", error="${error.code}"${scope}`,
  });
  res.end(JSON.stringify({ error: error.code }));
}

class RequestBodyTooLargeError extends Error {}
class InvalidRequestTargetError extends Error {}

function writeCors(res: ServerResponse): void {
  res.setHeader('Access-Control-Allow-Origin', process.env.PULSE_REMOTE_CORS_ORIGIN ?? 'http://127.0.0.1');
  res.setHeader('Access-Control-Allow-Methods', 'GET,POST,DELETE,OPTIONS');
  res.setHeader('Access-Control-Allow-Headers', 'Accept, Content-Type, Authorization, Mcp-Session-Id, MCP-Protocol-Version');
  res.setHeader('Access-Control-Expose-Headers', 'Mcp-Session-Id');
}

function writeJSON(res: ServerResponse, value: unknown, status = 200): void {
  res.writeHead(status, { 'Content-Type': 'application/json' });
  res.end(JSON.stringify(value));
}

function terminateHttpRequest(res: ServerResponse, status: 400 | 500): void {
  if (terminateStartedResponse(res)) return;
  try {
    res.setHeader('Connection', 'close');
    writeJSON(res, { error: status === 400 ? 'invalid_request' : 'internal_error' }, status);
  } catch {
    res.destroy();
  }
}

export function terminateStartedResponse(res: ServerResponse): boolean {
  if (!res.headersSent) return false;
  res.destroy();
  return true;
}

export async function drainSecurityReporterForShutdown(
  reporter: { drain(): Promise<void> },
  timeoutMs: number,
  log: (message: string) => void = (message) => console.error(message),
): Promise<boolean> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    const completed = await Promise.race([
      reporter.drain().then(() => true, () => false),
      new Promise<boolean>((resolve) => {
        timer = setTimeout(() => resolve(false), timeoutMs);
      }),
    ]);
    if (!completed) log('[pulse-mcp] team security audit degraded');
    return completed;
  } finally {
    if (timer) clearTimeout(timer);
  }
}

function argValue(name: string): string | undefined {
  const index = args.indexOf(name);
  if (index < 0) {
    return undefined;
  }
  return args[index + 1];
}

function isLoopbackHost(host: string): boolean {
  return host === '127.0.0.1' || host === 'localhost' || host === '::1' || host === '[::1]';
}

function trimTrailingSlash(value: string): string {
  return value.trim().replace(/\/+$/, '');
}

function parseRequestTarget(rawTarget: string | undefined): URL {
  const target = rawTarget ?? '/';
  if (!target.startsWith('/') || target.startsWith('//') || target.includes('\\') || /[\u0000-\u001f\u007f]/.test(target)) {
    throw new InvalidRequestTargetError('invalid request target');
  }
  const parsed = new URL(target, 'http://127.0.0.1');
  if (parsed.origin !== 'http://127.0.0.1' || parsed.hash !== '') {
    throw new InvalidRequestTargetError('invalid request target');
  }
  return parsed;
}

function hasUnexpectedRequestBody(req: IncomingMessage): boolean {
  if (hasDuplicateHeader(req, 'content-length') || hasDuplicateHeader(req, 'transfer-encoding')) return true;
  if (singleHeader(req.headers['transfer-encoding']) !== '') return true;
  const contentLength = singleHeader(req.headers['content-length']);
  if (contentLength === '') return false;
  return !/^\d+$/.test(contentLength) || Number(contentLength) !== 0;
}

function hasInvalidHostHeader(req: IncomingMessage): boolean {
  if (hasDuplicateHeader(req, 'host')) return true;
  const host = singleHeader(req.headers.host);
  if (host === '' || host !== host.trim()) return true;
  try {
    const parsed = new URL(`http://${host}`);
    return parsed.username !== '' || parsed.password !== '' || parsed.pathname !== '/' || parsed.search !== '' || parsed.hash !== '';
  } catch {
    return true;
  }
}

function isProtectedResourceMetadataPath(path: string): boolean {
  return path === '/.well-known/oauth-protected-resource' ||
    path === '/.well-known/oauth-protected-resource/mcp';
}

function isAuthorizationServerMetadataPath(path: string): boolean {
  return path === '/.well-known/oauth-authorization-server' ||
    path === '/.well-known/openid-configuration';
}

function protectedResourceMetadata(publicBaseURL: string, authIssuer: string) {
  return {
    resource: `${publicBaseURL}/mcp`,
    authorization_servers: [authIssuer],
    bearer_methods_supported: ['header'],
    scopes_supported: ['pulse:read', 'pulse:write'],
  };
}

function authorizationServerMetadata(publicBaseURL: string) {
  return {
    issuer: publicBaseURL,
    authorization_endpoint: `${publicBaseURL}/authorize`,
    token_endpoint: `${publicBaseURL}/token`,
    registration_endpoint: `${publicBaseURL}/register`,
    response_types_supported: ['code'],
    grant_types_supported: ['authorization_code', 'refresh_token'],
    token_endpoint_auth_methods_supported: ['none'],
    code_challenge_methods_supported: ['S256'],
    scopes_supported: ['pulse:read', 'pulse:write', 'offline_access'],
    client_id_metadata_document_supported: true,
  };
}

async function readRequestJSON(req: IncomingMessage, maxBytes = 1024 * 1024): Promise<unknown> {
  const text = await readRequestText(req, maxBytes);
  if (text === '') {
    return undefined;
  }
  return JSON.parse(text);
}

async function readRequestText(req: IncomingMessage, maxBytes = 1024 * 1024): Promise<string> {
  const declared = Number(singleHeader(req.headers['content-length']));
  if (Number.isFinite(declared) && declared > maxBytes) {
    throw new RequestBodyTooLargeError();
  }
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of req) {
    const bytes = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
    size += bytes.length;
    if (size > maxBytes) {
      throw new RequestBodyTooLargeError();
    }
    chunks.push(bytes);
  }
  if (chunks.length === 0) {
    return '';
  }
  return Buffer.concat(chunks).toString('utf8');
}

function callsProtectedTool(body: unknown): boolean {
  if (Array.isArray(body)) {
    return body.some(callsProtectedTool);
  }
  if (!body || typeof body !== 'object') {
    return false;
  }
  const message = body as Record<string, unknown>;
  return message.method === 'tools/call';
}

function isAuthorized(req: IncomingMessage, bearer: string, devAccessTokens: Map<string, DevOAuthToken>): boolean {
  const auth = req.headers.authorization ?? '';
  if (bearer && auth === `Bearer ${bearer}`) {
    return true;
  }
  const token = bearerToken(auth);
  if (token) {
    const devToken = devAccessTokens.get(token);
    if (devToken && devToken.expiresAt > Date.now()) {
      return true;
    }
  }
  return process.env.PULSE_REMOTE_AUTH_PROXY_MODE === '1' &&
    process.env.PULSE_REMOTE_TRUST_AUTH_HEADER === '1' &&
    /^Bearer\s+\S+$/i.test(auth);
}

function bearerToken(header: string | string[] | undefined): string {
  if (Array.isArray(header)) {
    header = header[0] ?? '';
  }
  const match = (header ?? '').match(/^Bearer\s+(.+)$/i);
  return match?.[1] ?? '';
}

function handleDevAuthorize(
  req: IncomingMessage,
  res: ServerResponse,
  codes: Map<string, DevOAuthCode>,
): void {
  const url = new URL(req.url ?? '/', `http://${req.headers.host ?? '127.0.0.1'}`);
  const responseType = url.searchParams.get('response_type') ?? '';
  const clientId = url.searchParams.get('client_id') ?? '';
  const redirectUri = url.searchParams.get('redirect_uri') ?? '';
  const codeChallenge = url.searchParams.get('code_challenge') ?? '';
  const codeChallengeMethod = url.searchParams.get('code_challenge_method') ?? '';
  const scope = url.searchParams.get('scope') || 'pulse:read pulse:write';
  const state = url.searchParams.get('state') ?? '';
  if (responseType !== 'code' || clientId === '' || redirectUri === '' || codeChallenge === '' || codeChallengeMethod !== 'S256') {
    writeJSON(res, { error: 'invalid_request' }, 400);
    return;
  }
  // PIN gate: when PULSE_OAUTH_PIN is set, the authorization code is only
  // issued after the human enters the PIN. This keeps the public OAuth
  // endpoint from auto-granting access to anyone who finds the URL.
  const requiredPin = process.env.PULSE_OAUTH_PIN ?? '';
  if (requiredPin) {
    const givenPin = url.searchParams.get('pin') ?? '';
    if (givenPin !== requiredPin) {
      const esc = (s: string) => s.replace(/[&<>"']/g, (c) => (
        { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c] as string));
      const carry = new URLSearchParams(url.search);
      carry.delete('pin');
      const hidden = [...carry.entries()]
        .map(([k, v]) => `<input type="hidden" name="${esc(k)}" value="${esc(v)}">`).join('');
      const wrong = url.searchParams.has('pin') ? '<p style="color:#c00">Wrong PIN</p>' : '';
      res.writeHead(url.searchParams.has('pin') ? 401 : 200, { 'Content-Type': 'text/html; charset=utf-8' });
      res.end(`<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><title>Pulse</title><body style="font-family:system-ui;max-width:360px;margin:18vh auto;padding:0 20px"><h2>Pulse — authorize</h2><p>Enter your PIN to connect this app to your memory.</p>${wrong}<form method="GET" action="/authorize">${hidden}<input name="pin" type="password" inputmode="numeric" placeholder="PIN" autofocus style="font-size:18px;padding:10px;width:100%;box-sizing:border-box"><button style="margin-top:14px;padding:10px 18px;font-size:16px">Authorize</button></form></body>`);
      return;
    }
  }
  const code = `pulse_dev_code_${randomUUID()}`;
  codes.set(code, {
    clientId,
    redirectUri,
    codeChallenge,
    scope,
    expiresAt: Date.now() + 5 * 60 * 1000,
  });
  const callback = new URL(redirectUri);
  callback.searchParams.set('code', code);
  if (state) {
    callback.searchParams.set('state', state);
  }
  res.writeHead(302, { Location: callback.toString() });
  res.end();
}

async function handleDevToken(
  req: IncomingMessage,
  res: ServerResponse,
  codes: Map<string, DevOAuthCode>,
  accessTokens: Map<string, DevOAuthToken>,
  refreshTokens: Map<string, DevOAuthToken>,
): Promise<void> {
  const form = new URLSearchParams(await readRequestText(req));
  const grantType = form.get('grant_type') ?? '';
  if (grantType === 'authorization_code') {
    const code = form.get('code') ?? '';
    const verifier = form.get('code_verifier') ?? '';
    const clientId = form.get('client_id') ?? '';
    const redirectUri = form.get('redirect_uri') ?? '';
    const pending = codes.get(code);
    if (!pending || pending.expiresAt <= Date.now() || pending.clientId !== clientId || pending.redirectUri !== redirectUri) {
      writeJSON(res, { error: 'invalid_grant' }, 400);
      return;
    }
    if (pkceChallenge(verifier) !== pending.codeChallenge) {
      writeJSON(res, { error: 'invalid_grant' }, 400);
      return;
    }
    codes.delete(code);
    writeDevTokenResponse(res, pending.scope, accessTokens, refreshTokens);
    return;
  }
  if (grantType === 'refresh_token') {
    const refreshToken = form.get('refresh_token') ?? '';
    const token = refreshTokens.get(refreshToken);
    if (!token) {
      writeJSON(res, { error: 'invalid_grant' }, 400);
      return;
    }
    refreshTokens.delete(refreshToken);
    writeDevTokenResponse(res, token.scope, accessTokens, refreshTokens);
    return;
  }
  writeJSON(res, { error: 'unsupported_grant_type' }, 400);
}

function writeDevTokenResponse(
  res: ServerResponse,
  scope: string,
  accessTokens: Map<string, DevOAuthToken>,
  refreshTokens: Map<string, DevOAuthToken>,
): void {
  const accessToken = `pulse_dev_at_${randomUUID()}`;
  const refreshToken = `pulse_dev_rt_${randomUUID()}`;
  accessTokens.set(accessToken, {
    scope,
    expiresAt: Date.now() + 60 * 60 * 1000,
  });
  refreshTokens.set(refreshToken, {
    scope,
    expiresAt: Date.now() + 30 * 24 * 60 * 60 * 1000,
  });
  writeJSON(res, {
    access_token: accessToken,
    token_type: 'Bearer',
    expires_in: 3600,
    refresh_token: refreshToken,
    scope,
  });
}

function persistOAuthTokens(
  file: string,
  access: Map<string, DevOAuthToken>,
  refresh: Map<string, DevOAuthToken>,
): void {
  try {
    const now = Date.now();
    const data = {
      access: [...access.entries()].filter(([, t]) => t.expiresAt > now),
      refresh: [...refresh.entries()].filter(([, t]) => t.expiresAt > now),
    };
    writeFileSync(file, JSON.stringify(data), { mode: 0o600 });
  } catch {
    // best-effort: never break the token response over a write failure
  }
}

function loadOAuthTokens(
  file: string,
  access: Map<string, DevOAuthToken>,
  refresh: Map<string, DevOAuthToken>,
): void {
  try {
    if (!existsSync(file)) return;
    const now = Date.now();
    const data = JSON.parse(readFileSync(file, 'utf8')) as {
      access?: [string, DevOAuthToken][];
      refresh?: [string, DevOAuthToken][];
    };
    for (const [k, t] of data.access ?? []) {
      if (t && t.expiresAt > now) access.set(k, t);
    }
    for (const [k, t] of data.refresh ?? []) {
      if (t && t.expiresAt > now) refresh.set(k, t);
    }
  } catch {
    // ignore a missing/corrupt token store
  }
}

function pkceChallenge(verifier: string): string {
  return createHash('sha256').update(verifier).digest('base64url');
}

function isHTTPSURL(value: string): boolean {
  try {
    return new URL(value).protocol === 'https:';
  } catch {
    return false;
  }
}

function writeOAuthChallenge(res: ServerResponse, publicBaseURL: string): void {
  writeCors(res);
  const metadataURL = `${publicBaseURL}/.well-known/oauth-protected-resource/mcp`;
  res.writeHead(401, {
    'Content-Type': 'application/json',
    'WWW-Authenticate': `Bearer error="invalid_token", resource_metadata="${metadataURL}", scope="pulse:read pulse:write"`,
  });
  res.end(JSON.stringify({
    error: 'invalid_token',
    error_description: 'Authentication required for Pulse MCP tools',
  }));
}

const invokedAsEntrypoint = (() => {
  if (!process.argv[1]) return false;
  try {
    return realpathSync(fileURLToPath(import.meta.url)) === realpathSync(resolve(process.argv[1]));
  } catch {
    return false;
  }
})();

// The published CLI imports the prebuilt MCP module instead of duplicating
// its startup logic. Keep one explicit callable entrypoint while preserving
// direct `node dist/index.js` execution.
export async function runMcpEntrypoint(): Promise<void> {
  await main();
}

if (invokedAsEntrypoint) {
  runMcpEntrypoint().catch((err) => {
    // eslint-disable-next-line no-console
    console.error('[pulse-mcp] fatal:', err);
    process.exit(1);
  });
}
