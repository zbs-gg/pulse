#!/usr/bin/env node
/**
 * Pulse MCP server.
 *
 * Exposes Pulse memory engine as MCP tools so any MCP-compatible client
 * (Claude Desktop, Cursor, mcp-agent, etc.) can use state-aware empathic
 * retrieval for free.
 *
 * Tools:
 *   - pulse_recall(query, mode?, top_k?, user_state?)
 *       Retrieves event_ids ranked by hybrid retrieval. Mode auto/factual/
 *       empathic/chain. Optional user_state biases stateful queries to
 *       body-relevant memories.
 *   - pulse_ingest(text, ts?)
 *       Adds an observation to Pulse's memory store. Pulse's extraction
 *       pipeline asynchronously builds the event graph, embeddings, and
 *       (when configured) atomic facts.
 *   - pulse_state()
 *       Returns the most recent biometric / mood snapshot known to Pulse.
 *       Useful for clients that want to read state without setting it.
 *
 * Connection: this MCP server is a thin wrapper. It does NOT contain the
 * memory engine itself — it talks to a running Pulse HTTP server (default
 * http://127.0.0.1:18789) via the same /retrieve, /ingest endpoints used
 * by host applications that embed Pulse.
 *
 * Setup:
 *   1. Run Pulse engine somewhere (`pulse server` from the Go binary,
 *      or use the hosted Pulse cloud once published).
 *   2. Configure your MCP client (e.g. Claude Desktop) with:
 *      {
 *        "command": "npx",
 *        "args": ["-y", "@nikshilov/pulse-mcp"],
 *        "env": {
 *          "PULSE_BASE_URL": "http://127.0.0.1:18789",
 *          "PULSE_API_KEY": "your-ipc-secret"
 *        }
 *      }
 */
import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from '@modelcontextprotocol/sdk/types.js';

const PULSE_BASE_URL =
  process.env.PULSE_BASE_URL ?? 'http://127.0.0.1:18789';
const PULSE_API_KEY = process.env.PULSE_API_KEY ?? '';

const VERSION = '0.1.0';

/* ────────────────────────────────────────────────────────────────────────
 * HTTP client for Pulse engine
 * ──────────────────────────────────────────────────────────────────────── */

interface RetrieveBody {
  query: string;
  mode?: 'auto' | 'factual' | 'empathic' | 'chain';
  top_k?: number;
  user_state?: Record<string, unknown>;
}

interface RetrieveResponse {
  event_ids: number[];
  mode_used: string;
  confidence: number;
  classifier: string;
  reasoning?: string;
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

interface ContextQueryResponse {
  schema_version: 'pulse.context.v1';
  query: string;
  mode_used: string;
  scope: string;
  facts: unknown[];
  emotional_anchors: unknown[];
  events: unknown[];
  entities: unknown[];
  relations: unknown[];
  forbidden: unknown[];
  private: unknown[];
  uncertainty: unknown[];
  importance_questions: unknown[];
  trace?: unknown;
}

interface IngestBody {
  content_text: string;
  captured_at: string;
  scope: string;
  source_kind: string;
  source_id: string;
}

async function pulseFetch<T>(path: string, body: unknown): Promise<T> {
  const url = `${PULSE_BASE_URL.replace(/\/$/, '')}${path}`;
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
  };
  if (PULSE_API_KEY) {
    headers['X-Pulse-Key'] = PULSE_API_KEY;
  }
  const resp = await fetch(url, {
    method: 'POST',
    headers,
    body: JSON.stringify(body),
  });
  if (!resp.ok) {
    const text = await resp.text();
    throw new Error(
      `Pulse HTTP ${resp.status} on ${path}: ${text.slice(0, 500)}`,
    );
  }
  return (await resp.json()) as T;
}

/* ────────────────────────────────────────────────────────────────────────
 * MCP server setup
 * ──────────────────────────────────────────────────────────────────────── */

const server = new Server(
  { name: 'pulse-mcp', version: VERSION },
  { capabilities: { tools: {} } },
);

server.setRequestHandler(ListToolsRequestSchema, async () => ({
  tools: [
    {
      name: 'pulse_recall',
      description:
        "Retrieve memories from Pulse. Returns event_ids ranked by hybrid retrieval (factual / empathic / chain modes auto-routed). Use mode='empathic' for emotional/state-aware retrieval (Pulse's strength); 'factual' for date/name/list lookups; 'chain' for causal trace queries.",
      inputSchema: {
        type: 'object',
        properties: {
          query: {
            type: 'string',
            description: 'The user query to retrieve memories for.',
          },
          mode: {
            type: 'string',
            enum: ['auto', 'factual', 'empathic', 'chain'],
            description:
              "'auto' (default): router classifies. 'factual': force fact lookup. 'empathic': force state-aware. 'chain': force causal-trace.",
          },
          top_k: {
            type: 'integer',
            minimum: 1,
            maximum: 50,
            description: 'How many events to return (default 5).',
          },
          user_state: {
            type: 'object',
            description:
              'Optional current user biometric/mood snapshot. Fields: mood_vector (Plutchik-10), sleep_quality, sleep_hours, hrv, hr_trend, hrv_trend, stress_proxy, recent_life_events_7d, time_of_day, snapshot_days_ago.',
            additionalProperties: true,
          },
        },
        required: ['query'],
      },
    },
    {
      name: 'pulse_ingest',
      description:
        "Add an observation to Pulse's memory. Pulse asynchronously extracts entities, emotion tags, and atomic facts from the text. Use this to log conversation turns, journal entries, or any text the user wants remembered.",
      inputSchema: {
        type: 'object',
        properties: {
          text: {
            type: 'string',
            description: 'The text content to ingest.',
          },
          ts: {
            type: 'string',
            description:
              'ISO8601 timestamp of the observation (default: now).',
          },
          scope: {
            type: 'string',
            enum: ['user', 'assistant', 'shared'],
            description: 'Which scope to ingest into (default "shared").',
          },
        },
        required: ['text'],
      },
    },
    {
      name: 'pulse_context_query',
      description:
        'Query Pulse for a typed, provenance-bearing context projection. Returns facts, emotional anchors, events, entities, relations, redactions, uncertainty, and importance questions. Use this instead of raw /retrieve when building a prompt context.',
      inputSchema: {
        type: 'object',
        properties: {
          query: {
            type: 'string',
            description: 'The user query to project Pulse context for.',
          },
          mode: {
            type: 'string',
            enum: ['auto', 'factual', 'empathic', 'chain'],
            description: 'Optional retrieval mode. Defaults to auto.',
          },
          top_k: {
            type: 'integer',
            minimum: 1,
            maximum: 50,
            description: 'How many retrieval seeds to project from.',
          },
          scope: {
            type: 'string',
            enum: ['user', 'assistant', 'shared'],
            description: 'Memory scope to project for.',
          },
          audience: { type: 'string' },
          privacy_floor: { type: 'string' },
          include_trace: { type: 'boolean' },
          user_state: { type: 'object', additionalProperties: true },
          domain_hints: {
            type: 'array',
            items: { type: 'string' },
          },
        },
        required: ['query'],
      },
    },
    {
      name: 'pulse_state',
      description:
        "Returns the most recent biometric / mood snapshot known to Pulse. Useful when the AI client wants to know the user's state without setting it.",
      inputSchema: {
        type: 'object',
        properties: {},
      },
    },
    {
      name: 'pulse_recent_signals',
      description:
        'Return the most-salient unconsumed proactive feed signals (Mac-local M9). ' +
        'Use at session start to notice mood/topic shifts that the user did not name yet. ' +
        'Each signal has signal_kind (like_burst | dwell_spike | topic_cluster | post_event_shift | hrv_x_topic), ' +
        'salience (0-1), evidence_obs_ids, and optional subject_entity_id. ' +
        'Frame any surfaced signal as one possible read of state, never as fact, ' +
        'and never quote raw content.',
      inputSchema: {
        type: 'object',
        properties: {
          limit: {
            type: 'integer',
            minimum: 1,
            maximum: 10,
            default: 3,
            description: 'Max signals to return.',
          },
          mark_consumed: {
            type: 'boolean',
            default: false,
            description: 'If true, marks returned signals as consumed (so the same one is not surfaced again).',
          },
        },
      },
    },

  ],
}));

server.setRequestHandler(CallToolRequestSchema, async (request) => {
  const { name, arguments: args } = request.params;

  try {
    if (name === 'pulse_recall') {
      const body: RetrieveBody = {
        query: String(args?.query ?? ''),
      };
      if (args?.mode) body.mode = args.mode as RetrieveBody['mode'];
      if (typeof args?.top_k === 'number') body.top_k = args.top_k;
      if (args?.user_state) {
        body.user_state = args.user_state as Record<string, unknown>;
      }
      const out = await pulseFetch<RetrieveResponse>('/retrieve', body);
      return {
        content: [
          {
            type: 'text',
            text: JSON.stringify(out, null, 2),
          },
        ],
      };
    }

    if (name === 'pulse_ingest') {
      // Pulse server's Observation schema (capture.Observation) requires:
      //   source_kind, source_id (UNIQUE for dedup),
      //   captured_at (ISO8601),
      //   scope (assistant|user|shared),
      //   content_text (the actual payload — note: NOT "text")
      // We tag MCP-originated observations with source_kind="mcp" and use
      // "mcp:<iso-timestamp>" as a stable per-call identifier.
      const text = String(args?.text ?? '');
      const captured_at =
        typeof args?.ts === 'string' ? args.ts : new Date().toISOString();
      const body: IngestBody = {
        content_text: text,
        captured_at,
        source_kind: 'mcp',
        source_id: `mcp:${captured_at}`,
        scope: typeof args?.scope === 'string' ? args.scope : 'shared',
      };
      const out = await pulseFetch<{ ok: boolean; observation_id?: number }>(
        '/ingest',
        { observations: [body] },
      );
      return {
        content: [
          {
            type: 'text',
            text: JSON.stringify(out, null, 2),
          },
        ],
      };
    }

    if (name === 'pulse_context_query') {
      const body: ContextQueryBody = {
        query: String(args?.query ?? ''),
      };
      if (args?.mode) body.mode = args.mode as ContextQueryBody['mode'];
      if (typeof args?.top_k === 'number') body.top_k = args.top_k;
      if (args?.scope) body.scope = args.scope as ContextQueryBody['scope'];
      if (typeof args?.audience === 'string') body.audience = args.audience;
      if (typeof args?.privacy_floor === 'string') {
        body.privacy_floor = args.privacy_floor;
      }
      if (typeof args?.include_trace === 'boolean') {
        body.include_trace = args.include_trace;
      }
      if (args?.user_state) {
        body.user_state = args.user_state as Record<string, unknown>;
      }
      if (Array.isArray(args?.domain_hints)) {
        body.domain_hints = args.domain_hints.map(String);
      }
      const out = await pulseFetch<ContextQueryResponse>('/context/query', body);
      return {
        content: [
          {
            type: 'text',
            text: JSON.stringify(out, null, 2),
          },
        ],
      };
    }

    if (name === 'pulse_state') {
      // Pulse Go server doesn't yet expose a state endpoint; return a stub
      // until pulse server.go gains a /state route in Phase H follow-up.
      return {
        content: [
          {
            type: 'text',
            text: JSON.stringify({
              status: 'unimplemented',
              note:
                "pulse_state will return the latest biometric/mood snapshot once Pulse server adds GET /state. Track progress at https://github.com/nikshilov/pulse/issues",
            }),
          },
        ],
      };
    }

    if (name === 'pulse_recent_signals') {
      const limit = (args?.limit as number) ?? 3;
      const markConsumed = (args?.mark_consumed as boolean) ?? false;
      const url = `${PULSE_BASE_URL.replace(/\/$/, '')}/feed_signals?limit=${limit}&mark_consumed=${markConsumed}`;
      const headers: Record<string, string> = { 'Content-Type': 'application/json' };
      if (PULSE_API_KEY) {
        headers['X-Pulse-Key'] = PULSE_API_KEY;
      }
      const resp = await fetch(url, { method: 'GET', headers });
      if (!resp.ok) {
        return {
          content: [{ type: 'text', text: `pulse_recent_signals failed: ${resp.status} ${await resp.text()}` }],
          isError: true,
        };
      }
      const data = await resp.json();
      return {
        content: [{ type: 'text', text: JSON.stringify(data, null, 2) }],
      };
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

/* ────────────────────────────────────────────────────────────────────────
 * Entry point
 * ──────────────────────────────────────────────────────────────────────── */

async function main(): Promise<void> {
  const transport = new StdioServerTransport();
  await server.connect(transport);
  // Server.connect blocks; output goes to stderr to avoid corrupting MCP stdio.
  // eslint-disable-next-line no-console
  console.error(
    `[pulse-mcp v${VERSION}] connected via stdio; backing Pulse: ${PULSE_BASE_URL}`,
  );
}

main().catch((err) => {
  // eslint-disable-next-line no-console
  console.error('[pulse-mcp] fatal:', err);
  process.exit(1);
});
