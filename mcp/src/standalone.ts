/**
 * Standalone lite engine for pulse-mcp.
 *
 * Used when no local Pulse daemon is reachable. Implements the same tool
 * surface against a plain-JSON local store so `claude mcp add pulse -- npx
 * -y @zbs-gg/pulse-mcp@preview` works with zero extra setup. Stored items
 * use the same shape as the daemon's /memory/export items, so upgrading to
 * the full engine later can import them without conversion.
 */
import { createHash, randomBytes } from 'node:crypto';
import {
  closeSync,
  existsSync,
  mkdirSync,
  openSync,
  readFileSync,
  renameSync,
  rmSync,
  statSync,
  writeFileSync,
  writeSync,
} from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';

import { validateCapsule, validateDelta } from './validation.js';

const STORE_SCHEMA = 'pulse.standalone_store.v1';
const CAPSULE_SCHEMA = 'pulse.memory_capsule.v1';
const PRIVACY_RANK: Record<string, number> = { normal: 0, sensitive: 1, private: 2 };
// Mirrors the daemon's retentionFilter (internal/store/memory_capsule.go):
// scope "user" means "everything", not a retention tier.
const SCOPE_TO_RETENTION: Record<string, string | undefined> = {
  session: 'session',
  project: 'project',
  user: undefined,
};
const UPGRADE_HINT =
  'SAFE FALLBACK MODE: structured local memory with keyword recall — not the Pulse engine. For real Pulse (state-aware retrieval with visible reasons), run: npx -y @zbs-gg/pulse@preview init claude-code, then pulse doctor, then pulse demo. Never present this fallback as Pulse retrieval and never quote bench numbers next to it.';

// Returned on every read tool while the store is empty. Written for the host
// agent. CRITICAL FRAMING: this mode is the safe fallback, not the product —
// the agent must say so and route the user to the Local Preview demo.
const FIRST_RUN = {
  welcome:
    'Pulse SAFE FALLBACK mode is connected (structured local memory, keyword recall — not the state-aware Pulse engine). Tell the user this plainly. The real Pulse demo — same question, different user state, different remembered episode, with visible reasons — runs on the Local Preview: npx -y @zbs-gg/pulse@preview init claude-code, then pulse doctor, then pulse demo.',
  guided_demo: [
    'Step 1 — say what mode this is: safe fallback memory (keyword recall). Offer the real thing first: if the machine can run it, install the Pulse Local Preview (npx -y @zbs-gg/pulse@preview init claude-code) and run pulse doctor + pulse demo — that demo shows state-aware retrieval with per-line reasons on a clearly-labeled simulated corpus.',
    'Step 2 — if the user stays on fallback: with their OK, save one real thing they are working on via pulse_remember (structured summary only, no raw transcript, stored locally) and checkpoint the thread via pulse_graph_delta with a continuity block.',
    'Step 3 — the fallback proof (continuity, not retrieval quality): a DIFFERENT Pulse-connected session or agent asks "where did we leave off?" and pulse_resume answers without re-explaining.',
    'Step 4 — close the trust loop: show what is stored (pulse_recall), how to erase everything (pulse_wipe with confirm "wipe pulse memory"), and repeat that retrieval quality claims apply only to the full engine.',
  ],
  trust: [
    'Show what is stored anytime: pulse_recall with their query.',
    'Erase everything anytime: pulse_wipe with confirm "wipe pulse memory".',
    'No model API keys, no backend LLM calls, no raw transcript capture.',
    'This mode carries no benchmark claims — those belong to the full engine only.',
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

type RememberResult = { id: string; result: 'created' | 'deduplicated' };

function itemIdentity(item: {
  kind: string;
  redacted_summary: string;
  confidence: number;
  evidence_hint: string;
  privacy_tier: string;
  retention: string;
  tags: string[];
}): string {
  return createHash('sha256').update(JSON.stringify({
    schema: CAPSULE_SCHEMA,
    items: [item],
    raw_input_included: false,
  })).digest('hex');
}

function storedItemIdentity(item: StoredItem): string {
  return itemIdentity({
    kind: item.kind,
    redacted_summary: item.redacted_summary,
    confidence: item.confidence,
    evidence_hint: item.evidence_hint,
    privacy_tier: item.privacy_tier,
    retention: item.retention,
    tags: item.tags,
  });
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

function truncateToUTF8ByteBudget(value: string, maxBytes: number): string {
  if (Buffer.byteLength(value, 'utf8') <= maxBytes) {
    return value;
  }

  let bytes = 0;
  let end = 0;
  for (const character of value) {
    const characterBytes = Buffer.byteLength(character, 'utf8');
    if (bytes + characterBytes > maxBytes) {
      break;
    }
    bytes += characterBytes;
    end += character.length;
  }
  return value.slice(0, end);
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

  /**
   * Cross-process advisory lock. Multiple MCP server processes (one per host
   * session) share one store.json; unguarded read-modify-write loses updates
   * because the last tmp+rename wins. All store ops are short and synchronous,
   * so a spin lock with a stale-steal is enough.
   */
  private withLock<T>(fn: () => T): T {
    const lockPath = `${this.storePath}.lock`;
    mkdirSync(dirname(this.storePath), { recursive: true, mode: 0o700 });
    const deadline = Date.now() + 5_000;
    for (;;) {
      try {
        const fd = openSync(lockPath, 'wx');
        try {
          writeSync(fd, String(process.pid));
        } finally {
          closeSync(fd);
        }
        break;
      } catch {
        try {
          // A crashed holder must not deadlock the store: steal stale locks.
          if (Date.now() - statSync(lockPath).mtimeMs > 10_000) {
            rmSync(lockPath, { force: true });
            continue;
          }
        } catch {
          continue; // lock vanished between openSync and statSync — retry now
        }
        if (Date.now() > deadline) {
          throw new Error('standalone store is locked by another Pulse process (timeout after 5s)');
        }
        Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 5);
      }
    }
    try {
      return fn();
    } finally {
      rmSync(lockPath, { force: true });
    }
  }

  private load(): StoreFile {
    if (!existsSync(this.storePath)) {
      // First-run creation races other processes too — create under the lock.
      return this.withLock(() => this.loadUnlocked());
    }
    return this.loadUnlocked();
  }

  private loadUnlocked(): StoreFile {
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

  remember(input: unknown): { ok: true; ids: string[]; results: RememberResult[] } {
    // Full content-contract validation (enums, limits, secret/path/transcript
    // rejection) BEFORE anything is persisted. See validation.ts — the JSON
    // Schema in tools/list is advisory; this is the real gate. Mirrors the Go
    // daemon's validateMemoryCapsule so Safe Mode can never store what the
    // engine would reject.
    const capsule = validateCapsule(input);
    return this.withLock(() => {
      const store = this.loadUnlocked();
      const now = new Date().toISOString();
      const ids: string[] = [];
      const results: RememberResult[] = [];
      let created = false;
      capsule.items.forEach((item, index) => {
        const digest = itemIdentity(item);
        const existing = store.items.find((candidate) => storedItemIdentity(candidate) === digest);
        if (existing) {
          ids.push(existing.id);
          results.push({ id: existing.id, result: 'deduplicated' });
          return;
        }
        const id = newMemoryID(index);
        store.items.push({
          id,
          schema: CAPSULE_SCHEMA,
          source: { ...capsule.source },
          kind: item.kind,
          redacted_summary: item.redacted_summary,
          confidence: item.confidence,
          evidence_hint: item.evidence_hint,
          privacy_tier: item.privacy_tier,
          retention: item.retention,
          tags: item.tags,
          created_at: now,
          raw_input_included: false,
        });
        ids.push(id);
        results.push({ id, result: 'created' });
        created = true;
      });
      if (created) {
        store.last_write = now;
        this.persist(store);
      }
      return { ok: true, ids, results };
    });
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
    // Validate + normalize first: only whitelisted, contract-checked fields
    // survive. Raw spreads of caller objects are gone, so arbitrary/raw-ish
    // fields can never be persisted. Mirrors validateSemanticDelta in Go.
    const delta = validateDelta(input);
    return this.withLock(() => {
      const store = this.loadUnlocked();
      const now = new Date().toISOString();

      let nodesUpserted = 0;
      for (const node of delta.nodes) {
        const existing = store.graph.nodes.findIndex((n) => n.client_id === node.client_id);
        const record = { ...node, last_seen: now };
        if (existing >= 0) {
          store.graph.nodes[existing] = { ...store.graph.nodes[existing], ...record };
        } else {
          store.graph.nodes.push({ ...record, first_seen: now });
        }
        nodesUpserted += 1;
      }

      let edgesUpserted = 0;
      for (const edge of delta.edges) {
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
      for (const fact of delta.facts) {
        const exists = store.graph.facts.some((f) => f.node === fact.node && f.text === fact.text);
        if (!exists) {
          store.graph.facts.push({ ...fact, created_at: now });
        }
        factsUpserted += 1;
      }

      const eventIds: number[] = [];
      for (const event of delta.events) {
        const id = store.graph.next_event_id;
        store.graph.next_event_id += 1;
        store.graph.events.push({ ...event, id, created_at: now });
        eventIds.push(id);
      }

      let checkpointSaved = false;
      if (delta.continuity) {
        const continuity = delta.continuity;
        const threadId = delta.source.thread_id || delta.source.project_id || 'default';
        store.checkpoints.push({
          id: store.checkpoints.length + 1,
          thread_id: threadId,
          session_id: delta.source.session_id ?? '',
          host: delta.source.host,
          project_id: delta.source.project_id ?? '',
          summary: continuity.summary,
          decisions: continuity.decisions,
          open_loops: continuity.open_loops,
          do_not_repeat: continuity.do_not_repeat,
          emotional_anchors: continuity.emotional_anchors,
          state_signals: continuity.state_signals,
          active_threads: continuity.active_threads,
          review_insights: continuity.review_insights,
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
    });
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
          'Nothing stored yet — SAFE FALLBACK mode (keyword recall, not the Pulse engine). Agent: follow the first_run block of this response and say so plainly.',
          ...FIRST_RUN.guided_demo.map((step) => `- ${step}`),
        ].join('\n'),
      );
    } else if (markdownParts.length === 1) {
      markdownParts.push(
        'No stored continuity for this thread yet. Save decisions with pulse_remember or pulse_graph_delta first.',
      );
    }
    const resumeMarkdown = truncateToUTF8ByteBudget(markdownParts.join('\n\n'), tokenBudget * 4);
    const renderedBytes = Buffer.byteLength(resumeMarkdown, 'utf8');
    const tokenEstimate = Math.ceil(renderedBytes / 4);
    const sessionId =
      (typeof body.session_id === 'string' && body.session_id) ||
      `${host}:${threadId}:${new Date().toISOString()}`;

    return {
      schema: 'pulse.continuity.v2',
      engine: 'standalone_lite',
      ...(storeIsEmpty ? { first_run: FIRST_RUN } : {}),
      thread_id: threadId,
      project_id: typeof body.project_id === 'string' ? body.project_id : '',
      session_id: sessionId,
      token_budget: tokenBudget,
      token_estimate: tokenEstimate,
      token_economy: {
        state: 'collecting_baseline',
        method_id: 'utf8_bytes_div4_ceil',
        method_version: '1',
        rendered_bytes: renderedBytes,
        pulse_tokens: tokenEstimate,
        reason_code: 'comparable_receipt_required',
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
    return this.withLock(() => {
      const store = this.loadUnlocked();
      const index = store.items.findIndex((item) => item.id === target);
      if (index < 0) {
        throw new Error(`memory id not found: ${target}`);
      }
      store.items.splice(index, 1);
      store.last_write = new Date().toISOString();
      this.persist(store);
      return { ok: true } as const;
    });
  }

  wipe(confirm: unknown): { ok: true } {
    if (confirm !== 'wipe pulse memory') {
      throw new Error('pulse_wipe requires confirm="wipe pulse memory"');
    }
    return this.withLock(() => {
      rmSync(this.storePath, { force: true });
      rmSync(`${this.storePath}.tmp`, { force: true });
      return { ok: true } as const;
    });
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
