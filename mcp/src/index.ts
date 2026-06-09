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

import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StreamableHTTPServerTransport } from '@modelcontextprotocol/sdk/server/streamableHttp.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from '@modelcontextprotocol/sdk/types.js';

const PULSE_BASE_URL =
  process.env.PULSE_BASE_URL ?? 'http://127.0.0.1:18789';
const PULSE_API_KEY = process.env.PULSE_API_KEY ?? '';

const VERSION = '0.3.0';
const args = process.argv.slice(2);

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
): Promise<T> {
  const url = `${PULSE_BASE_URL.replace(/\/$/, '')}${path}`;
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
  };
  if (PULSE_API_KEY) {
    headers['X-Pulse-Key'] = PULSE_API_KEY;
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
  status.storage = 'local_sqlite';
  if ('storage_path' in status) {
    status.storage_path = '<local>';
  }
  return status;
}

function createPulseMcpServer(): Server {
  const server = new Server(
    { name: 'pulse-mcp', version: VERSION },
    { capabilities: { tools: {} } },
  );

  server.setRequestHandler(ListToolsRequestSchema, async () => ({
    tools: [
    {
      name: 'pulse_remember',
      description:
        'Save a minimal, user-approved Pulse memory capsule. Never send raw full transcripts, arbitrary chat history, secrets, credentials, or store-everything payloads. Use only when the user explicitly asks to remember something, confirms saving, selects an excerpt, or project rules allow it.',
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
        'Local/dev tool: query Pulse for a typed pulse.context.v1 projection from the existing retrieval/graph engine. Use for agent context, not raw search dumps. Not store-safe for public connector builds until trace/user_state/privacy controls are narrowed.',
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
        },
        required: ['query'],
        additionalProperties: false,
      },
    },
    {
      name: 'pulse_graph_delta',
      description:
        'Write a host-extracted pulse.semantic_delta.v1 graph delta. Use when the current host model has identified durable semantic nodes, relations, facts, events, decisions, open loops, do-not-repeat, or emotional/state anchors. Never send raw transcript, secrets, credentials, local paths, or store-everything payloads.',
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
    ],
  }));

  server.setRequestHandler(CallToolRequestSchema, async (request) => {
    const { name, arguments: args } = request.params;

    try {
      if (name === 'pulse_remember') {
        const out = await pulseFetch<{ ok: boolean; ids: string[] }>(
          '/memory/remember',
          args as unknown as MemoryCapsule,
        );
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
        const out = await pulseFetch('/graph/delta', args as unknown as SemanticDeltaBody);
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

      if (name === 'pulse_forget') {
        const out = await pulseFetch('/memory/delete', { id: args?.id });
        return jsonText(out);
      }

      if (name === 'pulse_wipe') {
        if (args?.confirm !== 'wipe pulse memory') {
          throw new Error('pulse_wipe requires confirm="wipe pulse memory"');
        }
        const out = await pulseFetch('/memory/wipe', { confirm: 'wipe pulse memory' });
        return jsonText(out);
      }

      throw new Error(`Unknown tool: ${name}`);
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

async function main(): Promise<void> {
  if (args.includes('--http')) {
    await startHttpMode();
    return;
  }
  const server = createPulseMcpServer();
  const transport = new StdioServerTransport();
  await server.connect(transport);
  // eslint-disable-next-line no-console
  console.error(
    `[pulse-mcp v${VERSION}] host-extracted stdio connected; backing Pulse: ${PULSE_BASE_URL}`,
  );
}

async function startHttpMode(): Promise<void> {
  const host = argValue('--host') ?? process.env.PULSE_MCP_HOST ?? '127.0.0.1';
  const port = Number(argValue('--port') ?? process.env.PULSE_MCP_PORT ?? 8787);
  const bearer = process.env.PULSE_REMOTE_BEARER ?? '';
  const publicBaseURL = trimTrailingSlash(process.env.PULSE_REMOTE_PUBLIC_BASE_URL ?? '');
  const authIssuer = trimTrailingSlash(process.env.PULSE_REMOTE_AUTH_ISSUER ?? '');
  const oauthMode = publicBaseURL !== '' && authIssuer !== '';
  const oauthDevMode = oauthMode && process.env.PULSE_REMOTE_OAUTH_DEV === '1';
  const devAuthCodes = new Map<string, DevOAuthCode>();
  const devAccessTokens = new Map<string, DevOAuthToken>();
  const devRefreshTokens = new Map<string, DevOAuthToken>();
  const allowUnauthenticated =
    process.env.PULSE_REMOTE_ALLOW_UNAUTHENTICATED === '1' ||
    args.includes('--allow-unauthenticated');
  const publicBind = !isLoopbackHost(host);

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

  const httpServer = createServer(async (req, res) => {
    const path = requestPath(req, host);
    if (path === '/health') {
      writeCors(res);
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        ok: true,
        transport: 'streamable-http',
        path: '/mcp',
        auth: oauthMode ? 'oauth' : bearer ? 'bearer' : 'none',
      }));
      return;
    }
    if (oauthMode && isProtectedResourceMetadataPath(path)) {
      writeCors(res);
      writeJSON(res, protectedResourceMetadata(publicBaseURL, authIssuer));
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
      return;
    }
    if (!path.startsWith('/mcp')) {
      res.writeHead(404, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: 'not found' }));
      return;
    }
    if (req.method === 'OPTIONS') {
      writeCors(res);
      res.writeHead(204);
      res.end();
      return;
    }
    let parsedBody: unknown;
    if (oauthMode && req.method === 'POST') {
      parsedBody = await readRequestJSON(req);
      if (callsProtectedTool(parsedBody) && !isAuthorized(req, bearer, devAccessTokens)) {
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
      const requestServer = createPulseMcpServer();
      const transport = new StreamableHTTPServerTransport({
        sessionIdGenerator: undefined,
      });
      await requestServer.connect(transport);
      await transport.handleRequest(req, res, parsedBody);
      res.on('close', () => {
        void requestServer.close();
      });
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
  });

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

  const shutdown = async () => {
    httpServer.close(() => process.exit(0));
  };
  process.once('SIGTERM', shutdown);
  process.once('SIGINT', shutdown);
}

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

function requestPath(req: IncomingMessage, fallbackHost: string): string {
  return new URL(req.url ?? '/', `http://${req.headers.host ?? fallbackHost}`).pathname;
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

async function readRequestJSON(req: IncomingMessage): Promise<unknown> {
  const text = await readRequestText(req);
  if (text === '') {
    return undefined;
  }
  return JSON.parse(text);
}

async function readRequestText(req: IncomingMessage): Promise<string> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) {
    chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk));
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

main().catch((err) => {
  // eslint-disable-next-line no-console
  console.error('[pulse-mcp] fatal:', err);
  process.exit(1);
});
