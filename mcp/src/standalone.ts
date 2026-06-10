/**
 * Standalone lite engine for pulse-mcp.
 *
 * Used when no local Pulse daemon is reachable. Implements the same tool
 * surface against a plain-JSON local store so `claude mcp add pulse -- npx
 * -y @zbs-gg/pulse-mcp@preview` works with zero extra setup. Stored items
 * use the same shape as the daemon's /memory/export items, so upgrading to
 * the full engine later can import them without conversion.
 */
import { randomBytes } from 'node:crypto';
import {
  existsSync,
  mkdirSync,
  readFileSync,
  renameSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';

const STORE_SCHEMA = 'pulse.standalone_store.v1';
const CAPSULE_SCHEMA = 'pulse.memory_capsule.v1';
const DELTA_SCHEMA = 'pulse.semantic_delta.v1';
const PRIVACY_RANK: Record<string, number> = { normal: 0, sensitive: 1, private: 2 };
const SCOPE_TO_RETENTION: Record<string, string> = {
  session: 'session',
  project: 'project',
  user: 'long_term',
};
const UPGRADE_HINT =
  'Standalone lite engine. For the full Pulse retrieval engine (typed graph, emotional scoring, viewer), run: npx -y @zbs-gg/pulse@preview init claude-code';

// Returned on every read tool while the store is empty. Written for the host
// agent: the agent is the onboarding UI on the zero-config path, so the first
// response after install carries a guided demo the agent can act out live.
const FIRST_RUN = {
  welcome:
    'Pulse is connected and this store is empty. Pulse keeps the thread: what the user tells one agent, their other Pulse-connected agents and sessions will remember.',
  guided_demo: [
    'Step 1 — ask the user for one real thing they are working on right now, plus one preference about how they like to be helped. Real beats hypothetical: the payoff lands only if it is their own context.',
    'Step 2 — with their OK, save it: pulse_remember with a minimal capsule (one decision/project_state item + one preference item). Tell the user explicitly: structured summary only, no raw transcript, stored locally.',
    'Step 3 — call pulse_graph_delta with a continuity block (summary of where this conversation stands, one open_loop for what is next).',
    'Step 4 — the lived proof: ask the user to open a DIFFERENT session or agent connected to Pulse (new Claude Code session, Claude Desktop, Cursor) and ask it "where did we leave off?" — that agent should call pulse_resume and answer without the user re-explaining anything.',
  ],
  trust: [
    'Show what is stored anytime: pulse_recall with their query.',
    'Erase everything anytime: pulse_wipe with confirm "wipe pulse memory".',
    'No model API keys, no backend LLM calls, no raw transcript capture.',
  ],
} as const;

interface StoredItem {
  id: string;
  schema: typeof CAPSULE_SCHEMA;
  source: { host: string; conversation_scope: string; timestamp: string };
  kind: string;
  redacted_summary: string;
  confidence: number;
  evidence_hint: string;
  privacy_tier: string;
  retention: string;
  tags: string[];
  created_at: string;
  raw_input_included: false;
}

interface StoredCheckpoint {
  id: number;
  thread_id: string;
  session_id: string;
  host: string;
  project_id: string;
  summary: string;
  decisions: string[];
  open_loops: string[];
  do_not_repeat: string[];
  emotional_anchors: string[];
  state_signals: string[];
  active_threads: string[];
  review_insights: string[];
  created_at: string;
}

interface StoreFile {
  schema: typeof STORE_SCHEMA;
  version: 1;
  created_at: string;
  last_write: string | null;
  items: StoredItem[];
  graph: {
    nodes: Array<Record<string, unknown>>;
    edges: Array<Record<string, unknown>>;
    facts: Array<Record<string, unknown>>;
    events: Array<Record<string, unknown>>;
    next_event_id: number;
  };
  checkpoints: StoredCheckpoint[];
}

function emptyStore(): StoreFile {
  return {
    schema: STORE_SCHEMA,
    version: 1,
    created_at: new Date().toISOString(),
    last_write: null,
    items: [],
    graph: { nodes: [], edges: [], facts: [], events: [], next_event_id: 1 },
    checkpoints: [],
  };
}

function newMemoryID(index: number): string {
  const nanos = BigInt(Date.now()) * 1_000_000n;
  return `pulse:${nanos}:${index}:${randomBytes(8).toString('hex')}`;
}

function asRecord(value: unknown, what: string): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error(`invalid ${what}: expected an object`);
  }
  return value as Record<string, unknown>;
}

function asString(value: unknown, what: string): string {
  if (typeof value !== 'string' || value.trim() === '') {
    throw new Error(`invalid ${what}: expected a non-empty string`);
  }
  return value;
}

function stringList(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.filter((entry): entry is string => typeof entry === 'string' && entry.trim() !== '');
}

function dedupe(values: string[], cap = 12): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const value of values) {
    if (!seen.has(value)) {
      seen.add(value);
      out.push(value);
    }
    if (out.length >= cap) {
      break;
    }
  }
  return out;
}

export class StandaloneStore {
  private readonly storePath: string;

  constructor(dataDir?: string) {
    // `||` on purpose: empty strings must not produce a relative store path.
    const root = dataDir || process.env.PULSE_DATA_DIR || join(homedir(), '.pulse');
    this.storePath = join(root, 'standalone', 'store.json');
  }

  path(): string {
    return this.storePath;
  }

  private load(): StoreFile {
    if (!existsSync(this.storePath)) {
      const fresh = emptyStore();
      this.persist(fresh);
      return fresh;
    }
    const parsed = JSON.parse(readFileSync(this.storePath, 'utf8')) as StoreFile;
    if (parsed.schema !== STORE_SCHEMA) {
      throw new Error(`unsupported standalone store schema: ${parsed.schema}`);
    }
    return parsed;
  }

  private persist(store: StoreFile): void {
    mkdirSync(dirname(this.storePath), { recursive: true, mode: 0o700 });
    const tmpPath = `${this.storePath}.tmp`;
    writeFileSync(tmpPath, `${JSON.stringify(store, null, 2)}\n`, { mode: 0o600 });
    renameSync(tmpPath, this.storePath);
  }

  remember(input: unknown): { ok: true; ids: string[] } {
    const capsule = asRecord(input, 'memory capsule');
    if (capsule.schema !== CAPSULE_SCHEMA) {
      throw new Error(`invalid memory capsule: schema must be ${CAPSULE_SCHEMA}`);
    }
    if (capsule.raw_input_included !== false) {
      throw new Error('invalid memory capsule: raw_input_included must be false');
    }
    const source = asRecord(capsule.source, 'memory capsule source');
    const host = asString(source.host, 'memory capsule source.host');
    const scope = asString(source.conversation_scope, 'memory capsule source.conversation_scope');
    const timestamp = asString(source.timestamp, 'memory capsule source.timestamp');
    if (!Array.isArray(capsule.items) || capsule.items.length === 0) {
      throw new Error('invalid memory capsule: items must be a non-empty array');
    }
    const store = this.load();
    const now = new Date().toISOString();
    const ids: string[] = [];
    capsule.items.forEach((entry, index) => {
      const item = asRecord(entry, `memory capsule item ${index}`);
      const id = newMemoryID(index);
      store.items.push({
        id,
        schema: CAPSULE_SCHEMA,
        source: { host, conversation_scope: scope, timestamp },
        kind: asString(item.kind, `item ${index} kind`),
        redacted_summary: asString(item.redacted_summary, `item ${index} redacted_summary`),
        confidence: typeof item.confidence === 'number' ? item.confidence : 0.5,
        evidence_hint: asString(item.evidence_hint, `item ${index} evidence_hint`),
        privacy_tier: asString(item.privacy_tier, `item ${index} privacy_tier`),
        retention: asString(item.retention, `item ${index} retention`),
        tags: stringList(item.tags),
        created_at: now,
        raw_input_included: false,
      });
      ids.push(id);
    });
    store.last_write = now;
    this.persist(store);
    return { ok: true, ids };
  }

  recall(input: unknown): { items: Array<Record<string, unknown>> } {
    const body = asRecord(input, 'recall request');
    const query = asString(body.query, 'recall query');
    const limitRaw = typeof body.limit === 'number' ? Math.trunc(body.limit) : 5;
    const limit = Math.min(50, Math.max(1, limitRaw));
    const ceiling = PRIVACY_RANK[String(body.privacy_ceiling ?? 'sensitive')] ?? 1;
    const retention =
      typeof body.scope === 'string' ? SCOPE_TO_RETENTION[body.scope] : undefined;
    const tokens = query
      .toLowerCase()
      .split(/[^\p{L}\p{N}]+/u)
      .filter((token) => token.length > 1);
    const store = this.load();
    const scored = store.items
      .filter((item) => (PRIVACY_RANK[item.privacy_tier] ?? 1) <= ceiling)
      .filter((item) => (retention ? item.retention === retention : true))
      .map((item) => {
        const haystack = `${item.redacted_summary} ${item.tags.join(' ')}`.toLowerCase();
        const score = tokens.reduce(
          (total, token) => total + (haystack.includes(token) ? 1 : 0),
          0,
        );
        return { item, score };
      })
      .filter((entry) => entry.score > 0)
      .sort((a, b) =>
        b.score !== a.score
          ? b.score - a.score
          : b.item.created_at.localeCompare(a.item.created_at),
      )
      .slice(0, limit);
    return {
      items: scored.map(({ item }) => ({
        id: item.id,
        summary: item.redacted_summary,
        kind: item.kind,
        confidence: item.confidence,
        source: 'pulse',
        evidence_ref: '',
        privacy_tier: item.privacy_tier,
        retention: item.retention,
        tags: item.tags,
        created_at: item.created_at,
      })),
    };
  }

  graphDelta(input: unknown): Record<string, unknown> {
    const delta = asRecord(input, 'semantic delta');
    if (delta.schema !== DELTA_SCHEMA) {
      throw new Error(`invalid semantic delta: schema must be ${DELTA_SCHEMA}`);
    }
    if (delta.raw_input_included !== false) {
      throw new Error('invalid semantic delta: raw_input_included must be false');
    }
    const source = asRecord(delta.source, 'semantic delta source');
    const host = asString(source.host, 'semantic delta source.host');
    asString(source.conversation_scope, 'semantic delta source.conversation_scope');
    asString(source.timestamp, 'semantic delta source.timestamp');
    const store = this.load();
    const now = new Date().toISOString();

    let nodesUpserted = 0;
    for (const entry of Array.isArray(delta.nodes) ? delta.nodes : []) {
      const node = asRecord(entry, 'graph node');
      const clientId = asString(node.client_id, 'graph node client_id');
      const existing = store.graph.nodes.findIndex((n) => n.client_id === clientId);
      const record = { ...node, last_seen: now };
      if (existing >= 0) {
        store.graph.nodes[existing] = { ...store.graph.nodes[existing], ...record };
      } else {
        store.graph.nodes.push({ ...record, first_seen: now });
      }
      nodesUpserted += 1;
    }

    let edgesUpserted = 0;
    for (const entry of Array.isArray(delta.edges) ? delta.edges : []) {
      const edge = asRecord(entry, 'graph edge');
      const key = `${edge.from}|${edge.to}|${edge.kind}`;
      const existing = store.graph.edges.findIndex(
        (e) => `${e.from}|${e.to}|${e.kind}` === key,
      );
      const record = { ...edge, last_seen: now };
      if (existing >= 0) {
        store.graph.edges[existing] = { ...store.graph.edges[existing], ...record };
      } else {
        store.graph.edges.push({ ...record, first_seen: now });
      }
      edgesUpserted += 1;
    }

    let factsUpserted = 0;
    for (const entry of Array.isArray(delta.facts) ? delta.facts : []) {
      const fact = asRecord(entry, 'graph fact');
      const exists = store.graph.facts.some(
        (f) => f.node === fact.node && f.text === fact.text,
      );
      if (!exists) {
        store.graph.facts.push({ ...fact, created_at: now });
      }
      factsUpserted += 1;
    }

    const eventIds: number[] = [];
    for (const entry of Array.isArray(delta.events) ? delta.events : []) {
      const event = asRecord(entry, 'graph event');
      const id = store.graph.next_event_id;
      store.graph.next_event_id += 1;
      store.graph.events.push({ ...event, id, created_at: now });
      eventIds.push(id);
    }

    let checkpointSaved = false;
    if (delta.continuity && typeof delta.continuity === 'object') {
      const continuity = asRecord(delta.continuity, 'continuity block');
      const threadId =
        (typeof source.thread_id === 'string' && source.thread_id) ||
        (typeof source.project_id === 'string' && source.project_id) ||
        'default';
      store.checkpoints.push({
        id: store.checkpoints.length + 1,
        thread_id: threadId,
        session_id: typeof source.session_id === 'string' ? source.session_id : '',
        host,
        project_id: typeof source.project_id === 'string' ? source.project_id : '',
        summary: asString(continuity.summary, 'continuity summary'),
        decisions: stringList(continuity.decisions),
        open_loops: stringList(continuity.open_loops),
        do_not_repeat: stringList(continuity.do_not_repeat),
        emotional_anchors: stringList(continuity.emotional_anchors),
        state_signals: stringList(continuity.state_signals),
        active_threads: stringList(continuity.active_threads),
        review_insights: stringList(continuity.review_insights),
        created_at: now,
      });
      checkpointSaved = true;
    }

    store.last_write = now;
    this.persist(store);
    return {
      ok: true,
      nodes_upserted: nodesUpserted,
      edges_upserted: edgesUpserted,
      facts_upserted: factsUpserted,
      events_inserted: eventIds.length,
      event_ids: eventIds,
      checkpoint_saved: checkpointSaved,
    };
  }

  resume(input: unknown): Record<string, unknown> {
    const body = asRecord(input ?? {}, 'resume request');
    const budgetRaw = typeof body.token_budget === 'number' ? Math.trunc(body.token_budget) : 1200;
    const tokenBudget = Math.min(2000, Math.max(400, budgetRaw));
    const host = typeof body.host === 'string' ? body.host : 'claude-code';
    const store = this.load();

    const requestedThread =
      (typeof body.thread_id === 'string' && body.thread_id) ||
      (typeof body.project_id === 'string' && body.project_id) ||
      '';
    const latestCheckpoint = store.checkpoints[store.checkpoints.length - 1];
    const threadId = requestedThread || latestCheckpoint?.thread_id || 'default';
    const checkpoints = store.checkpoints
      .filter((checkpoint) => checkpoint.thread_id === threadId)
      .slice(-3)
      .reverse();

    const itemsByKind = (kind: string) =>
      store.items
        .filter((item) => item.kind === kind)
        .slice(-5)
        .reverse()
        .map((item) => item.redacted_summary);

    const whereWeLeftOff = checkpoints.length > 0 ? [checkpoints[0].summary] : [];
    const decisions = dedupe([
      ...checkpoints.flatMap((c) => c.decisions),
      ...itemsByKind('decision'),
    ]);
    const openLoops = dedupe([
      ...checkpoints.flatMap((c) => c.open_loops),
      ...itemsByKind('open_loop'),
    ]);
    const doNotRepeat = dedupe([
      ...checkpoints.flatMap((c) => c.do_not_repeat),
      ...itemsByKind('do_not_repeat'),
    ]);
    const stateContext = dedupe([
      ...checkpoints.flatMap((c) => [...c.emotional_anchors, ...c.state_signals]),
      ...itemsByKind('state_signal'),
    ]);
    const activeThreads = dedupe(checkpoints.flatMap((c) => c.active_threads));
    const reviewInsights = dedupe(checkpoints.flatMap((c) => c.review_insights));
    const suggestedNextStep =
      openLoops.length > 0
        ? [`Continue with: ${openLoops[0]}`]
        : ['Confirm the next step with the user before assuming context.'];
    const evidenceRefs = dedupe([
      ...checkpoints.map((c) => `pulse:checkpoint:${c.id}`),
      ...store.items.slice(-5).map((item) => item.id),
    ]);

    const sectionBlocks: Array<[string, string[]]> = [
      ['Where we left off', whereWeLeftOff],
      ['Active decisions', decisions],
      ['Open loops', openLoops],
      ['Do-not-repeat', doNotRepeat],
      ['Relevant emotional/state context', stateContext],
      ['Suggested next step', suggestedNextStep],
    ];
    const markdownParts = ['# Pulse Resume'];
    for (const [title, entries] of sectionBlocks) {
      if (entries.length === 0) {
        continue;
      }
      markdownParts.push(`## ${title}`);
      markdownParts.push(entries.map((entry) => `- ${entry}`).join('\n'));
    }
    const storeIsEmpty = store.items.length === 0 && store.checkpoints.length === 0;
    if (storeIsEmpty) {
      markdownParts.splice(
        1,
        markdownParts.length,
        '## First run',
        [
          'Nothing stored yet. Agent: run the 3-minute lived demo from the first_run block of this response.',
          ...FIRST_RUN.guided_demo.map((step) => `- ${step}`),
        ].join('\n'),
      );
    } else if (markdownParts.length === 1) {
      markdownParts.push(
        'No stored continuity for this thread yet. Save decisions with pulse_remember or pulse_graph_delta first.',
      );
    }
    let resumeMarkdown = markdownParts.join('\n\n');
    if (Math.ceil(resumeMarkdown.length / 4) > tokenBudget) {
      resumeMarkdown = resumeMarkdown.slice(0, tokenBudget * 4);
    }
    const tokenEstimate = Math.ceil(resumeMarkdown.length / 4);
    const sessionId =
      (typeof body.session_id === 'string' && body.session_id) ||
      `${host}:${threadId}:${new Date().toISOString()}`;

    return {
      schema: 'pulse.continuity.v1',
      engine: 'standalone_lite',
      ...(storeIsEmpty ? { first_run: FIRST_RUN } : {}),
      thread_id: threadId,
      project_id: typeof body.project_id === 'string' ? body.project_id : '',
      session_id: sessionId,
      token_budget: tokenBudget,
      token_estimate: tokenEstimate,
      token_economy: {
        resume_tokens: tokenEstimate,
        estimated_raw_tokens: tokenEstimate * 8,
        estimated_saved_tokens: tokenEstimate * 7,
        estimated: true,
      },
      resume_markdown: resumeMarkdown,
      sections: {
        where_we_left_off: whereWeLeftOff,
        active_decisions: decisions,
        active_reviewed_threads: activeThreads,
        review_insights: reviewInsights,
        open_loops: openLoops,
        do_not_repeat: doNotRepeat,
        relevant_emotional_state_context: stateContext,
        suggested_next_step: suggestedNextStep,
        evidence_refs: evidenceRefs,
        material_refs: [],
      },
      evidence_refs: evidenceRefs,
      material_refs: [],
    };
  }

  status(): Record<string, unknown> {
    const store = this.load();
    const storeIsEmpty = store.items.length === 0 && store.checkpoints.length === 0;
    return {
      billing_mode: 'host-extracted',
      host: 'standalone',
      engine: 'standalone_lite',
      ...(storeIsEmpty ? { first_run: FIRST_RUN } : {}),
      backend_llm_enabled: false,
      raw_capture_enabled: false,
      storage: 'local_json',
      storage_path: this.storePath,
      schema: CAPSULE_SCHEMA,
      item_count: store.items.length,
      checkpoint_count: store.checkpoints.length,
      last_write: store.last_write,
      upgrade_hint: UPGRADE_HINT,
    };
  }

  contextQuery(input: unknown): Record<string, unknown> {
    const body = asRecord(input, 'context query');
    const query = asString(body.query, 'context query');
    const topK = typeof body.top_k === 'number' ? body.top_k : 8;
    const { items } = this.recall({ query, limit: topK, privacy_ceiling: 'private' });
    return {
      schema: 'pulse.context.v1',
      engine: 'standalone_lite',
      query,
      results: items,
      note: UPGRADE_HINT,
    };
  }

  forget(id: unknown): { ok: true } {
    const target = asString(id, 'memory id');
    const store = this.load();
    const index = store.items.findIndex((item) => item.id === target);
    if (index < 0) {
      throw new Error(`memory id not found: ${target}`);
    }
    store.items.splice(index, 1);
    store.last_write = new Date().toISOString();
    this.persist(store);
    return { ok: true };
  }

  wipe(confirm: unknown): { ok: true } {
    if (confirm !== 'wipe pulse memory') {
      throw new Error('pulse_wipe requires confirm="wipe pulse memory"');
    }
    rmSync(this.storePath, { force: true });
    rmSync(`${this.storePath}.tmp`, { force: true });
    return { ok: true };
  }

  call(name: string, args: unknown): unknown {
    if (name === 'pulse_remember') {
      return this.remember(args);
    }
    if (name === 'pulse_recall') {
      return this.recall(args);
    }
    if (name === 'pulse_context_query') {
      return this.contextQuery(args);
    }
    if (name === 'pulse_graph_delta') {
      return this.graphDelta(args);
    }
    if (name === 'pulse_resume') {
      return this.resume(args);
    }
    if (name === 'pulse_status') {
      return this.status();
    }
    if (name === 'pulse_forget') {
      return this.forget((args as Record<string, unknown> | undefined)?.id);
    }
    if (name === 'pulse_wipe') {
      return this.wipe((args as Record<string, unknown> | undefined)?.confirm);
    }
    throw new Error(`Unknown tool: ${name}`);
  }
}
