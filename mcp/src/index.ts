#!/usr/bin/env node
/**
 * Pulse MCP server for host-extracted memory.
 *
 * The host model creates a minimal pulse.memory_capsule.v1 in tool
 * arguments. Pulse stores, recalls, deletes, and wipes memory without calling
 * an LLM backend by default. Export/import stay CLI-only in v1.
 */
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { randomUUID } from 'node:crypto';
import {
  readFileSync, realpathSync,
} from 'node:fs';
import { homedir } from 'node:os';
import { isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StreamableHTTPServerTransport } from '@modelcontextprotocol/sdk/server/streamableHttp.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from '@modelcontextprotocol/sdk/types.js';

import { StandaloneStore } from './standalone.js';
import { assertTruthfulDeletionReceipt, assertTruthfulWriteResponse, mcpRequestIdempotencyKey } from './write-receipts.js';
import {
  validateConsolidationExplanation,
  validateConsolidationReport,
} from './lifecycle-contracts.js';

const PULSE_BASE_URL =
  process.env.PULSE_BASE_URL ?? 'http://127.0.0.1:18789';
// `||` on purpose: an empty PULSE_DATA_DIR must not become a relative path.
const PULSE_DATA_DIR = process.env.PULSE_DATA_DIR || join(homedir(), '.pulse');

const VERSION = '0.4.1';
const args = process.argv.slice(2);
const HTTP_REQUESTED = args.includes('--http');
const RUNTIME_MODE = resolveRuntimeMode(process.env.PULSE_RUNTIME_MODE, HTTP_REQUESTED);

type RuntimeMode = 'local-stdio' | 'development-http';

function resolveRuntimeMode(value: string | undefined, httpRequested: boolean): RuntimeMode {
	if (value === undefined || value === '') {
		return httpRequested ? 'development-http' : 'local-stdio';
	}
	if (value !== 'local-stdio' && value !== 'development-http') {
		throw new Error(`invalid PULSE_RUNTIME_MODE: ${value} (use local-stdio or development-http)`);
	}
	if ((value === 'development-http') !== httpRequested) {
		throw new Error('development-http requires --http and local-stdio forbids it');
	}
	return value;
}

type EngineMode = 'auto' | 'daemon' | 'standalone';
const ENGINE_MODE = parseEngineMode(process.env.PULSE_MCP_MODE);
const PRODUCT_HOST_ADAPTER = process.env.PULSE_HOST_ADAPTER === 'codex' ||
  process.env.PULSE_HOST_ADAPTER === 'claude-code' ||
  process.env.PULSE_HOST_ADAPTER === 'cursor';
const PRODUCT_HOST = PRODUCT_HOST_ADAPTER
  ? process.env.PULSE_HOST_ADAPTER as 'codex' | 'claude-code' | 'cursor'
  : undefined;
const PRODUCT_UNASSIGNED_REASON = PRODUCT_HOST_ADAPTER &&
  (process.env.PULSE_PRODUCT_UNASSIGNED === 'binding_missing' || process.env.PULSE_PRODUCT_UNASSIGNED === 'workspace_not_git')
  ? process.env.PULSE_PRODUCT_UNASSIGNED
  : undefined;

async function assertProductBindingCurrent(): Promise<void> {
  if (!PRODUCT_HOST_ADAPTER) return;
  const moduleURL = process.env.PULSE_HOST_AUTHORITY_MODULE ?? '';
  const expectedWorkspace = process.env.PULSE_HOST_WORKSPACE ?? '';
  if (!moduleURL.startsWith('file:') || expectedWorkspace === '') {
    throw new Error('Pulse host binding authority is unavailable; restart this task');
  }
  const authority = await import(moduleURL) as {
    inspectProductWorkspaceBinding(options: { cwd: string }): {
      status: 'bound' | 'unassigned';
      reason?: string;
      workspace?: { canonical_path: string };
    };
    resolveProductWorkspaceBinding(options: { cwd: string }): {
      binding_digest: string;
      resolver_epoch: number;
      workspace: { canonical_path: string; repository_id: string };
    };
  };
  if (PRODUCT_UNASSIGNED_REASON) {
    const inspected = authority.inspectProductWorkspaceBinding({ cwd: process.cwd() });
    if (inspected.status !== 'unassigned' || inspected.reason !== PRODUCT_UNASSIGNED_REASON ||
        !inspected.workspace || realpathSync(inspected.workspace.canonical_path) !== realpathSync(expectedWorkspace) ||
        realpathSync(process.cwd()) !== realpathSync(expectedWorkspace)) {
      throw new Error('Pulse unassigned workspace state changed; restart this task');
    }
    return;
  }
  const current = authority.resolveProductWorkspaceBinding({ cwd: process.cwd() });
  if (current.binding_digest !== process.env.PULSE_BINDING_DIGEST ||
      current.workspace.repository_id !== process.env.PULSE_REPOSITORY_ID ||
      current.resolver_epoch !== Number(process.env.PULSE_RESOLVER_EPOCH) ||
      realpathSync(current.workspace.canonical_path) !== realpathSync(expectedWorkspace) ||
      realpathSync(process.cwd()) !== realpathSync(expectedWorkspace)) {
    throw new Error('Pulse workspace binding changed or was revoked; restart this task');
  }
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

interface HostTurnContext {
  schema: string;
  host: 'codex' | 'claude-code' | 'cursor';
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
	scope?: 'user' | 'assistant';
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
  if (PRODUCT_HOST_ADAPTER) {
    const workspace = process.env.PULSE_HOST_WORKSPACE ?? '';
    const bindingDigest = process.env.PULSE_BINDING_DIGEST ?? '';
    const repositoryID = process.env.PULSE_REPOSITORY_ID ?? '';
    const resolverEpoch = process.env.PULSE_RESOLVER_EPOCH ?? '';
    if (!isAbsolute(workspace) || Buffer.byteLength(workspace, 'utf8') > 4096 ||
        !/^[a-f0-9]{64}$/.test(bindingDigest) ||
        !/^repository_[A-Za-z0-9._:-]{1,240}$/.test(repositoryID) ||
        !/^[1-9][0-9]*$/.test(resolverEpoch)) {
      throw new Error('Pulse product request authority is unavailable; restart this task');
    }
    headers['X-Pulse-Product-Workspace'] = Buffer.from(workspace, 'utf8').toString('base64url');
    headers['X-Pulse-Product-Binding'] = bindingDigest;
    headers['X-Pulse-Product-Repository'] = repositoryID;
    headers['X-Pulse-Product-Resolver-Epoch'] = resolverEpoch;
  }
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

function productRuntimeResolution() {
  return {
    binding: {
      binding_digest: process.env.PULSE_BINDING_DIGEST ?? '',
      resolver_epoch: Number(process.env.PULSE_RESOLVER_EPOCH),
      workspace: {
        canonical_path: process.env.PULSE_HOST_WORKSPACE ?? '',
        repository_id: process.env.PULSE_REPOSITORY_ID ?? '',
      },
    },
    runtime: { data_dir: PULSE_DATA_DIR },
  };
}

interface ProductRuntimeModule {
  stageUnassignedProductCandidate(
    host: 'codex' | 'claude-code' | 'cursor',
    input: unknown,
    idempotencyKey: string,
  ): unknown;
  consumeHostToolLease(
    resolved: ReturnType<typeof productRuntimeResolution>,
    host: 'codex' | 'claude-code' | 'cursor',
    name: string,
    input: unknown,
  ): HostTurnContext;
  writeHostFinalizeMarker(
    resolved: ReturnType<typeof productRuntimeResolution>,
    event: HostTurnContext,
    host: 'codex' | 'claude-code' | 'cursor',
    result: unknown,
  ): unknown;
}

async function productRuntimeModule(): Promise<ProductRuntimeModule> {
  const moduleURL = process.env.PULSE_HOST_RUNTIME_MODULE ?? '';
  if (!moduleURL.startsWith('file:') || !PRODUCT_HOST) {
    throw new Error('Pulse tool lease authority is unavailable; restart this task');
  }
  return await import(moduleURL) as ProductRuntimeModule;
}

async function consumeProductTurnContext(toolInput: unknown): Promise<HostTurnContext> {
  if (!PRODUCT_HOST) throw new Error('Pulse product host is unavailable');
  const runtime = await productRuntimeModule();
  return runtime.consumeHostToolLease(
    productRuntimeResolution(), PRODUCT_HOST, 'pulse_remember', toolInput,
  );
}

function assertProductRememberSourceHost(toolInput: unknown): void {
  if (!PRODUCT_HOST) return;
  if (!toolInput || typeof toolInput !== 'object' || Array.isArray(toolInput)) {
    throw new Error('Pulse source host does not match the bound harness');
  }
  const source = (toolInput as Record<string, unknown>).source;
  if (!source || typeof source !== 'object' || Array.isArray(source) ||
      (source as Record<string, unknown>).host !== PRODUCT_HOST) {
    throw new Error('Pulse source host does not match the bound harness');
  }
}

function productFinalizeBody(capsule: MemoryCapsule, context: HostTurnContext): Record<string, unknown> {
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
        source: { host: context.host, conversation_scope: 'current_turn', timestamp },
        items: [item],
        raw_input_included: false,
      },
    })),
  };
}

async function writeProductFinalizeMarker(context: HostTurnContext, value: unknown): Promise<void> {
  if (!PRODUCT_HOST) throw new Error('Pulse product host is unavailable');
  const runtime = await productRuntimeModule();
  runtime.writeHostFinalizeMarker(productRuntimeResolution(), context, PRODUCT_HOST, value);
}

function jsonText(value: unknown) {
	const result: {
		content: [{ type: 'text'; text: string }];
		structuredContent?: Record<string, unknown>;
	} = {
		content: [
      {
        type: 'text' as const,
        text: JSON.stringify(value, null, 2),
      },
		],
	};
	if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
		result.structuredContent = value as Record<string, unknown>;
	}
	return result;
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

const SAFE_PROVENANCE_OUTPUT_SCHEMA = {
  type: 'object',
  properties: {
    host: { type: 'string' },
    session_id: { type: 'string' },
    turn_id: { type: 'string' },
    source_event_key: { type: 'string' },
  },
  required: ['host', 'session_id', 'turn_id', 'source_event_key'],
  additionalProperties: false,
};

const WRITE_RECEIPT_OUTPUT_SCHEMA = {
  type: 'object',
  properties: {
    schema: { type: 'string', const: 'pulse.write_receipt.v1' },
    receipt_id: { type: 'string' },
    ledger_id: { type: 'string' },
    candidate_id: { type: 'string' },
    candidate_version: { type: 'integer', minimum: 1 },
    status: { type: 'string', enum: ['pending', 'created', 'updated', 'deduplicated', 'canceled', 'rejected', 'failed'] },
    destination: { type: 'string', const: 'personal' },
    destination_store_id: { type: 'string' },
    safe_provenance: SAFE_PROVENANCE_OUTPUT_SCHEMA,
    content_digest: { type: 'string', pattern: '^[a-f0-9]{64}$' },
    object_id: { type: 'string' },
    reason_code: { type: 'string' },
    policy_epoch: { type: 'integer', minimum: 0 },
    resolver_epoch: { type: 'integer', minimum: 0 },
    measurement_method: { type: 'string' },
    created_at: { type: 'string', format: 'date-time' },
    actual_input_tokens: { type: 'integer', minimum: 0 },
    measurement: {
      type: 'object',
      properties: {
        kind: { type: 'string', enum: ['estimated', 'provider_actual'] },
        source: { type: 'string' },
      },
      required: ['kind', 'source'],
      additionalProperties: false,
    },
  },
  required: [
    'schema', 'receipt_id', 'ledger_id', 'status', 'destination', 'destination_store_id',
    'safe_provenance', 'policy_epoch', 'resolver_epoch', 'measurement_method', 'created_at',
  ],
  additionalProperties: false,
};

const LOCAL_REMEMBER_OUTPUT_SCHEMA = {
  type: 'object',
  properties: {
    ok: { type: 'boolean', const: true },
    ids: { type: 'array', minItems: 1, items: { type: 'string' } },
    results: {
      type: 'array',
      minItems: 1,
      items: {
        type: 'object',
        properties: {
          id: { type: 'string' },
          result: { type: 'string', enum: ['created', 'deduplicated'] },
        },
        required: ['id', 'result'],
        additionalProperties: false,
      },
    },
  },
  required: ['ok', 'ids', 'results'],
  additionalProperties: false,
};

const PRODUCT_REMEMBER_OUTPUT_SCHEMA = {
  type: 'object',
  properties: {
    ledger_id: { type: 'string' },
    status: { type: 'string', enum: ['candidates', 'rejected'] },
    finalize_receipt: {
      type: 'object',
      properties: {
        schema: { type: 'string', const: 'pulse.turn_finalize_receipt.v1' },
        receipt_id: { type: 'string' },
        ledger_id: { type: 'string' },
        status: { type: 'string', enum: ['candidates', 'rejected'] },
        destination: { type: 'string', const: 'personal' },
        destination_store_id: { type: 'string' },
        safe_provenance: SAFE_PROVENANCE_OUTPUT_SCHEMA,
        policy_epoch: { type: 'integer', minimum: 0 },
        resolver_epoch: { type: 'integer', minimum: 0 },
        created_at: { type: 'string', format: 'date-time' },
      },
      required: [
        'schema', 'receipt_id', 'ledger_id', 'status', 'destination',
        'destination_store_id', 'safe_provenance', 'policy_epoch', 'resolver_epoch', 'created_at',
      ],
      additionalProperties: false,
    },
    receipts: { type: 'array', minItems: 1, items: WRITE_RECEIPT_OUTPUT_SCHEMA },
  },
  required: ['ledger_id', 'status', 'finalize_receipt', 'receipts'],
  additionalProperties: false,
};

const UNASSIGNED_REMEMBER_OUTPUT_SCHEMA = {
  type: 'object',
  properties: {
    schema: { type: 'string', const: 'pulse.unassigned_stage_receipt.v1' },
    status: { type: 'string', enum: ['staged', 'assigned', 'deleted'] },
    destination: { type: 'string', enum: ['unassigned_inbox', 'memory_tray', 'deleted'] },
    receipts: {
      type: 'array',
      minItems: 1,
      items: {
        type: 'object',
        properties: {
          receipt_id: { type: 'string' },
          item_id: { type: 'string' },
          content_digest: { type: 'string', pattern: '^[a-f0-9]{64}$' },
          action: { type: 'string', enum: ['stage', 'assign', 'delete'] },
          status: { type: 'string', enum: ['staged', 'assigned', 'deleted'] },
          destination: { type: 'string', enum: ['unassigned_inbox', 'memory_tray', 'deleted'] },
          created_at: { type: 'string', format: 'date-time' },
        },
        required: ['receipt_id', 'item_id', 'content_digest', 'action', 'status', 'destination', 'created_at'],
        additionalProperties: false,
      },
    },
  },
  required: ['schema', 'status', 'destination', 'receipts'],
  additionalProperties: false,
};

const RECALL_OUTPUT_SCHEMA = {
  type: 'object',
  properties: {
    items: {
      type: 'array',
      items: {
        type: 'object',
        properties: {
          id: { type: 'string' },
          summary: { type: 'string' },
          kind: { type: 'string' },
          confidence: { type: 'number', minimum: 0, maximum: 1 },
          source: { type: 'string' },
          evidence_ref: { type: 'string' },
          privacy_tier: { type: 'string', enum: ['normal', 'sensitive', 'private'] },
          retention: { type: 'string', enum: ['session', 'project', 'long_term'] },
          tags: { type: 'array', items: { type: 'string' } },
          created_at: { type: 'string', format: 'date-time' },
        },
        required: ['id', 'summary', 'kind', 'confidence', 'source', 'privacy_tier', 'retention', 'created_at'],
        additionalProperties: false,
      },
    },
  },
  required: ['items'],
  additionalProperties: false,
};

function outputSchemaForTool(name: string): Record<string, unknown> {
  if (name === 'pulse_remember') {
    if (PRODUCT_UNASSIGNED_REASON) return UNASSIGNED_REMEMBER_OUTPUT_SCHEMA;
    if (PRODUCT_HOST_ADAPTER) return PRODUCT_REMEMBER_OUTPUT_SCHEMA;
    return LOCAL_REMEMBER_OUTPUT_SCHEMA;
  }
  if (name === 'pulse_recall') return RECALL_OUTPUT_SCHEMA;
  return { type: 'object', additionalProperties: true };
}

export function createPulseMcpServer(): Server {
  const server = new Server(
    { name: 'pulse-mcp', version: VERSION },
    { capabilities: { tools: {} } },
  );

  server.setRequestHandler(ListToolsRequestSchema, async () => {
		const tools = [
    {
      name: 'pulse_remember',
      description:
			'Propose a minimal private Pulse memory capsule. Personal mode returns a truthful receipt and saves only after the local database commit; Local Preview stores immediately. Never send raw full transcripts, arbitrary chat history, secrets, credentials, or store-everything payloads. Use only when the user explicitly asks to remember something, confirms saving, selects an excerpt, or project rules allow it.',
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
					'user_confirmed',
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
                  items: {
                    type: 'string',
                    pattern: '^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$',
                  },
                  maxItems: 20,
                  description:
                    'Optional ASCII safe slugs. Omit tags when a concept needs spaces.',
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
			scope: { type: 'string', enum: ['user', 'assistant'] },
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
		'Propose a private host-extracted pulse.semantic_delta.v1 graph delta. Personal mode returns a truthful receipt and saves only after the local database commit; Local Preview stores immediately. Use for durable semantic nodes, relations, facts, events, decisions, open loops, do-not-repeat, or emotional/state anchors. Never send raw transcript, secrets, credentials, local paths, or store-everything payloads.',
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
				  const: 'private',
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
      name: 'pulse_consolidation_report',
      description:
        'Start or inspect the read-only local memory-source report for the current signed project binding. This tool cannot choose a destination, import, merge, delete, clean up, publish, or reveal local paths.',
      inputSchema: {
        type: 'object',
        properties: {
          action: { type: 'string', enum: ['start', 'status', 'explain', 'cancel', 'resume'] },
          report_id: { type: 'string', pattern: '^report_[A-Za-z0-9._-]{1,128}$' },
        },
        required: ['action'],
        allOf: [
          {
            if: { properties: { action: { enum: ['explain', 'cancel', 'resume'] } } },
            then: { required: ['report_id'] },
          },
          {
            if: { properties: { action: { const: 'start' } } },
            then: { not: { required: ['report_id'] } },
          },
        ],
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
    const localTools = PRODUCT_HOST_ADAPTER
      ? tools.filter((tool) => !['pulse_forget', 'pulse_wipe', 'pulse_graph_delta'].includes(tool.name))
      : tools.filter((tool) => tool.name !== 'pulse_consolidation_report');
    let productTools = PRODUCT_UNASSIGNED_REASON
      ? localTools.filter((tool) => tool.name === 'pulse_remember')
      : localTools;
		if (PRODUCT_HOST) {
      productTools = productTools.map((tool) => {
        if (tool.name !== 'pulse_remember') return tool;
        const descriptor = JSON.parse(JSON.stringify(tool)) as typeof tool;
        const source = descriptor.inputSchema.properties?.source;
        const host = source?.properties?.host;
        if (host) {
          source.properties.host = { type: 'string', const: PRODUCT_HOST } as unknown as typeof host;
        }
        return descriptor;
      });
		}
		productTools = productTools.map((tool) => ({
			...tool,
			outputSchema: outputSchemaForTool(tool.name),
		}));
		return { tools: productTools };
  });

  server.setRequestHandler(CallToolRequestSchema, async (request, extra) => {
    const { name, arguments: args } = request.params;
	const invocationKey = mcpRequestIdempotencyKey(extra.sessionId, extra.requestId);

    try {
      await assertProductBindingCurrent();
      if (PRODUCT_UNASSIGNED_REASON && name !== 'pulse_remember') {
        throw new Error('Choose a project before using Pulse recall or project memory tools');
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

function standaloneResult(name: string, args: unknown) {
  const out = resolveStandaloneStore().call(name, args);
  return jsonText(name === 'pulse_status' ? redactStatusForMcp(out) : out);
}

function resolveStandaloneStore(): StandaloneStore {
	standaloneStore ??= new StandaloneStore(PULSE_DATA_DIR);
  return standaloneStore;
}

async function daemonToolCall(name: string, args: Record<string, unknown> | undefined, invocationKey: string) {
  if (name === 'pulse_remember') {
    if (PRODUCT_HOST_ADAPTER) {
      assertProductRememberSourceHost(args);
      if (PRODUCT_UNASSIGNED_REASON) {
        if (!PRODUCT_HOST) throw new Error('Pulse product host is unavailable');
        const runtime = await productRuntimeModule();
        return jsonText(runtime.stageUnassignedProductCandidate(PRODUCT_HOST, args, invocationKey));
      }
      const context = await consumeProductTurnContext(args);
      const body = productFinalizeBody(args as unknown as MemoryCapsule, context);
      const out = await pulseFetch<unknown>(
        '/turn/finalize', body, 'POST', String(body.idempotency_key),
      );
      assertTruthfulWriteResponse(out);
      try {
        await writeProductFinalizeMarker(context, out);
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
    if (PRODUCT_HOST_ADAPTER) throw new Error('Product-host graph writes require the governed candidate path');
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

  if (name === 'pulse_consolidation_report') {
		if (!PRODUCT_HOST_ADAPTER) throw new Error('Consolidation reports require an installed Personal product binding');
    const action = args?.action;
    const reportId = args?.report_id;
    if (!['start', 'status', 'explain', 'cancel', 'resume'].includes(String(action))) {
      throw new Error('invalid consolidation report action');
    }
    if (reportId !== undefined && !/^report_[A-Za-z0-9._-]{1,128}$/.test(String(reportId))) {
      throw new Error('invalid consolidation report id');
    }
    if (['explain', 'cancel', 'resume'].includes(String(action)) && reportId === undefined) {
      throw new Error(`pulse_consolidation_report ${String(action)} requires report_id`);
    }
    if (action === 'start' && reportId !== undefined) {
      throw new Error('pulse_consolidation_report start does not accept report_id');
    }
    let path = '/memory/consolidation/reports';
    let method: 'GET' | 'POST' = 'POST';
    if (action === 'status') {
      method = 'GET';
      path = reportId === undefined
        ? '/memory/consolidation/reports/latest'
        : `/memory/consolidation/reports/${String(reportId)}`;
    } else if (action === 'explain') {
      method = 'GET';
      path = `/memory/consolidation/reports/${String(reportId)}/explain`;
    } else if (action === 'cancel' || action === 'resume') {
      path = `/memory/consolidation/reports/${String(reportId)}/${String(action)}`;
    }
    const out = await pulseFetch(path, method === 'POST' ? {} : undefined, method, invocationKey);
    return jsonText(action === 'explain'
      ? validateConsolidationExplanation(out)
      : validateConsolidationReport(out));
  }

  if (name === 'pulse_tray') {
    const limit = args?.limit === undefined ? 50 : Number(args.limit);
    if (!Number.isInteger(limit) || limit < 1 || limit > 100) throw new Error('pulse_tray limit must be 1..100');
    const out = await pulseFetch(`/memory/tray?limit=${limit}`, undefined, 'GET');
    return jsonText(out);
  }

  if (name === 'pulse_forget') {
    if (PRODUCT_HOST_ADAPTER) throw new Error('Pulse product deletion requires the privileged OS-backed user-presence surface, which is not active');
    const out = await pulseFetch('/memory/delete', { id: args?.id }, 'POST', invocationKey);
    assertTruthfulDeletionReceipt(out, String(args?.id || ''));
    return jsonText(out);
  }

  if (name === 'pulse_wipe') {
    if (PRODUCT_HOST_ADAPTER) throw new Error('Pulse product wipe requires the privileged OS-backed user-presence surface, which is not active');
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
		await startPersonalHttpMode();
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

async function startPersonalHttpMode(): Promise<void> {
	const host = httpArgValue('--host') ?? process.env.PULSE_MCP_HOST ?? '127.0.0.1';
	const port = Number(httpArgValue('--port') ?? process.env.PULSE_MCP_PORT ?? 8787);
	const bearer = process.env.PULSE_REMOTE_BEARER ?? '';
	const allowUnauthenticated = process.env.PULSE_REMOTE_ALLOW_UNAUTHENTICATED === '1';
	if (host !== '127.0.0.1' && host !== '::1') {
		throw new Error('development HTTP is local-only; use 127.0.0.1 or ::1');
	}
	if (!Number.isInteger(port) || port < 0 || port > 65535) {
		throw new Error('invalid Pulse MCP HTTP port');
	}
	if (!bearer && !allowUnauthenticated) {
		throw new Error('development HTTP requires PULSE_REMOTE_BEARER');
	}
	const httpServer = createServer(async (req, res) => {
		try {
			const requestURL = new URL(req.url ?? '/', `http://${host}`);
			writeDevelopmentCors(res);
			if (req.method === 'OPTIONS') {
				res.writeHead(204);
				res.end();
				return;
			}
			if (req.method === 'GET' && requestURL.pathname === '/health' && requestURL.search === '') {
				res.writeHead(200, { 'Content-Type': 'application/json' });
				res.end(JSON.stringify({ ok: true, mode: 'development-http' }));
				return;
			}
			if (requestURL.pathname !== '/mcp' || requestURL.search !== '') {
				res.writeHead(404, { 'Content-Type': 'application/json' });
				res.end(JSON.stringify({ error: 'not found' }));
				return;
			}
			if (!allowUnauthenticated && req.headers.authorization !== `Bearer ${bearer}`) {
				res.writeHead(401, { 'Content-Type': 'application/json', 'WWW-Authenticate': 'Bearer' });
				res.end(JSON.stringify({ error: 'unauthorized' }));
				return;
			}
			await dispatchPersonalMcpRequest(req, res);
		} catch (error) {
			if (!res.headersSent) {
				res.writeHead(500, { 'Content-Type': 'application/json' });
			}
			res.end(JSON.stringify({
				jsonrpc: '2.0', id: null,
				error: { code: -32603, message: error instanceof Error ? error.message : String(error) },
			}));
		}
	});
	await new Promise<void>((resolvePromise, rejectPromise) => {
		httpServer.once('error', rejectPromise);
		httpServer.listen(port, host, resolvePromise);
	});
	const address = httpServer.address();
	const actualPort = typeof address === 'object' && address !== null ? address.port : port;
	console.error(`[pulse-mcp v${VERSION}] Streamable HTTP listening on http://${host}:${actualPort}/mcp; backing Pulse: ${PULSE_BASE_URL}`);
	let closing = false;
	const shutdown = () => {
		if (closing) return;
		closing = true;
		httpServer.close(() => process.exit(0));
	};
	process.once('SIGTERM', shutdown);
	process.once('SIGINT', shutdown);
}

function httpArgValue(name: string): string | undefined {
	const index = args.indexOf(name);
	return index >= 0 ? args[index + 1] : undefined;
}

function writeDevelopmentCors(res: ServerResponse): void {
	res.setHeader('Access-Control-Allow-Origin', 'http://127.0.0.1');
	res.setHeader('Access-Control-Allow-Methods', 'GET, POST, DELETE, OPTIONS');
	res.setHeader('Access-Control-Allow-Headers', 'Authorization, Content-Type, MCP-Protocol-Version, MCP-Session-Id, Last-Event-ID');
	res.setHeader('Access-Control-Expose-Headers', 'MCP-Session-Id');
	res.setHeader('Vary', 'Origin');
}

async function dispatchPersonalMcpRequest(req: IncomingMessage, res: ServerResponse): Promise<void> {
	const requestServer = createPulseMcpServer();
	const transport = new StreamableHTTPServerTransport({ sessionIdGenerator: undefined });
	let cleaned = false;
	const cleanup = () => {
		if (cleaned) return;
		cleaned = true;
		void Promise.allSettled([transport.close(), requestServer.close()]);
	};
	try {
		await requestServer.connect(transport);
		res.once('close', cleanup);
		await transport.handleRequest(req, res);
	} catch (error) {
		cleanup();
		throw error;
	}
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
