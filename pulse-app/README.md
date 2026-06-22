# Pulse

Host-extracted portable memory, continuity, and state-aware retrieval engine,
written in Go.

Pulse keeps the thread across AI chats. It lets Claude Code save minimal
structured memory, resume where a session left off, and inspect what will be
injected next without sending raw chat history to another LLM backend. The
first supported host is Claude Code via MCP, but the product surface is Pulse
MCP.

Memory answers: "what should I know?" Continuity answers: "where were we?"
Pulse needs both.

Pulse stores conversational events, continuity checkpoints, memory capsules,
and a lightweight knowledge graph in a single SQLite file. It retrieves them
with a hybrid ranker that combines dense embeddings, lexical search, and a set
of *conditional* affective boosts.

The design goal is simple: a retrieval engine for AI companions and
assistants that surfaces the right memory at the right moment, and that
degrades gracefully to a plain semantic-retrieval baseline whenever the
extra signals are absent.

## What makes it different

Most memory engines rank purely on embedding similarity (optionally with a
recency decay). Pulse keeps that as its base score and then layers
*conditional* boosts on top:

```
score = base × emotion × state × anchor × date
        └─ cosine × recency × belief-class weighting
```

Each boost term is `1.0` (a no-op) unless its signal is genuinely present:

- **Emotion** — a Plutchik-10 typed emotion vector. When the query (or the
  caller-supplied user state) has a dominant emotion, events whose emotion
  vector aligns are boosted. No dominant emotion → term collapses to 1.0.
- **State** — fit between the event and the user's current state (body
  load, recent life events, restoration/depletion). Neutral state → 1.0.
- **Anchor** — a small bump for high-salience "anchor" events, applied only
  within the top-N by base score so it can re-rank but not hijack.
- **Date** — proximity boost when the query carries a temporal reference
  ("yesterday", "last week", "сегодня", …). No temporal cue → 1.0.

Because every boost is multiplicative and gated, a fully neutral query
returns exactly the semantic-retrieval baseline. The conditional gating is
load-bearing: always-on boosts measurably hurt retrieval, so they only fire
when the signal is real.

On top of the scored substrate, Pulse fuses a parallel **BM25 / FTS5**
lexical ranking via Reciprocal Rank Fusion (RRF, k=60). Lexical catches the
exact-phrase matches that dense embeddings round off; if the FTS5 layer or
the DB is unavailable, retrieval falls through to pure cosine and never
fails the request.

A **query router** classifies each query into one of three modes before
retrieval:

- **factual** — wh-questions about names / dates / lists → atomic-fact cosine
- **chain** — causal / temporal "what led to…" requests → predecessor BFS
- **empathic** — everything else → the full conditional formula above

Routing is heuristic-first, with an optional LLM fallback when heuristic
confidence is low.

## Implementation status

The Go scoring substrate (`internal/retrieve`) is parity-validated against a
frozen Python reference: per-event scores match to float precision and the
top-5 ordering matches on the reference test set. The boost constants and the
conditional gating logic are frozen — see `internal/retrieve/v3boosts.go`.

The parity gate runs in a clean checkout (and CI) against a small, fully
synthetic public fixture committed under
`internal/retrieve/testdata/parity_golden_public.json`, produced by the same
reference engine via `testdata/gen_parity_public.py`. It exercises every boost
term (emotion, state, anchor, date, chain expansion) and the v2_pure-collapse
property on fabricated data — zero private corpus required. The richer private
golden (real evaluation corpus) stays gitignored and is used only when present
on a dev machine.

Benchmark framing here is deliberately conservative: Pulse is built to be
evaluated on public memory benchmarks (e.g. LongMemEval, LoCoMo). This
repository ships the engine and its unit/parity tests, not any private
evaluation corpus.

## Layout

```
cmd/pulse/         HTTP server entrypoint
cmd/pulse-smoke/   minimal smoke-test client
cli/               @zbs-gg/pulse installer/status/export/delete/wipe CLI
internal/retrieve/ hybrid retrieval: router, factual/empathic/chain, v3 boosts
internal/store/    SQLite store + migrations (events, graph, emotions, FTS5)
internal/embed/    embedders (Cohere API + optional local subprocess)
internal/ingest/   observation ingest + sensitive-actor redaction
internal/contextquery/ scoped context projection over the graph
internal/server/   HTTP handlers
internal/...       prompt assembly, providers, erasure (GDPR), health, outbox
mcp/               @zbs-gg/pulse-mcp stdio MCP server
```

## Build & run

Requires Go 1.25+. SQLite is pure-Go (`modernc.org/sqlite`), so no CGo and
no separate vector database.

### Pulse MCP quickstart

```bash
# first install target: Claude Code stdio MCP
npx @zbs-gg/pulse init claude-code

# local-auto continuity hooks + viewer entrypoint
npx @zbs-gg/pulse connect claude-code

# same local Claude Code session, but typed from Claude mobile / claude.ai/code
npx @zbs-gg/pulse connect claude-code --remote-control
claude --remote-control "Pulse Memory"

# local daemon; host-extracted mode does not require ANTHROPIC_API_KEY
go run ./cmd/pulse -addr 127.0.0.1:18789 -data-dir ~/.pulse
```

The default memory path is host-extracted:

```
Claude model extracts a pulse.memory_capsule.v1
→ Pulse stores/searches/exports/imports it
→ Pulse does no backend LLM call by default
```

This does not mean the Pulse backend can use Claude Max, ChatGPT Plus, or any
host subscription as backend API billing. The active host subscription can
power extraction inside the current host chat; Pulse receives structured tool
arguments and stores/validates them. Backend model API calls are opt-in only.

The same rule applies to the graph layer:

```
Harness pays for understanding.
Pulse owns the graph.
API keys only power optional background work.
```

Claude Code Remote Control is the first mobile-friendly entrypoint: the user
types from Claude mobile or `claude.ai/code`, while the session still runs on
the local machine with Pulse MCP, hooks, project files, and local SQLite
available. Ordinary Claude Chat across web/mobile/Desktop uses the remote MCP
custom connector path with public HTTPS and OAuth. The MCP package has a
development Streamable HTTP mode for that path, and the CLI has a handoff
command for the logged-in Claude UI step:

```bash
npx @zbs-gg/pulse connect claude-chat --base "$PULSE_PUBLIC_ORIGIN"
```

The actual Claude UI install still must be verified in Claude Chat/mobile
before this path is called complete. The local HTTP preview looks like:

```bash
PULSE_BASE_URL=http://127.0.0.1:18789 \
PULSE_API_KEY=your-pulse-ipc-secret \
PULSE_REMOTE_BEARER=dev-token \
npx -y @zbs-gg/pulse-mcp@preview --http --port 8787
```

Endpoint:

```text
http://127.0.0.1:8787/mcp
```

The HTTP MCP mode requires `PULSE_REMOTE_BEARER` by default and refuses
authless public binds. This is not the final public auth model; store-grade
Claude Chat/mobile distribution needs HTTPS plus OAuth/auth review.

The MCP package also includes an OAuth protected-resource readiness mode for
Claude custom connector work:

```bash
PULSE_REMOTE_PUBLIC_BASE_URL=https://pulse.example.com \
PULSE_REMOTE_AUTH_ISSUER=https://auth.example.com \
npx -y @zbs-gg/pulse-mcp@preview --http --host 0.0.0.0 --port 8787
```

### Technical Preview Demo

For sharing with technical friends, use the narrow proof script instead of a
platform pitch:

- [Pulse MCP demo flow](docs/PULSE_MCP_DEMO_FLOW.md)
- [Pulse MCP preview pitch](docs/PULSE_MCP_PREVIEW_PITCH.md)

The safe claim is: Pulse kept one structured decision across a clean Pulse-only
Claude Code two-session proof, and the Context Restore Bench passed 8/8
structured continuity cases on a clean local daemon. Do not claim full Auto
Continuity v1 or consumer-ready install.

That mode exposes protected-resource metadata and returns transport-level
`401 WWW-Authenticate` challenges for unauthenticated `tools/call` requests.
It is not a full OAuth provider; token issuance and production verification
still belong to the public auth layer.

The Cloudflare Worker + D1 prototype under `cloudflare/` is a separate hosted
connector experiment, not the local Go engine. It must be configured with a
non-empty `PULSE_OWNER_CODE`; unset owner code fails closed for authorization
and viewer access. Its `pulse_graph_delta` surface is deliberately
continuity-only in this slice. Full nodes, edges, facts, events, archive
migration, and relationship graph review stay in the local Pulse viewer until
the hosted validator matches the local Go validator.

For a private smoke test before choosing a real auth provider, set
`PULSE_REMOTE_OAUTH_DEV=1` with `PULSE_REMOTE_PUBLIC_BASE_URL` and
`PULSE_REMOTE_AUTH_ISSUER` on the same temporary HTTPS origin. That enables
dev-only dynamic client registration, PKCE S256 token exchange, refresh tokens,
and in-memory bearer validation. It auto-consents and is not production auth.
OAuth mode rejects non-HTTPS public/auth issuer URLs. A separate auth-proxy
trust mode exists only for deployments where a verified proxy validates bearer
tokens before forwarding requests.

Trust controls are split by blast radius. MCP exposes `pulse_status`,
`pulse_graph_delta`, `pulse_resume`, `pulse_forget`, and `pulse_wipe`;
export/import stay CLI-only through `pulse export`, `pulse import`,
`pulse delete --id`, and `pulse wipe --confirm "wipe pulse memory"`.
Confirmed wipe also removes host-extracted graph rows created by
`pulse_graph_delta`.

Reviewed archive/local-history previews keep review advice separate from user
state. `Why this may matter now` insights are stored as `review_insights`; when
the user marks one active, Pulse stores an `active_threads` continuity field and
renders it in the next `pulse_resume` under `Active reviewed threads`. This is a
review handoff, not an autonomous prioritization claim.

Local-auto continuity is a separate local mode for Claude Code/Codex-style
hosts:

```
Session start    → Pulse injects the resume block
Each prompt      → Pulse observes a redacted, structured local signal
After tool calls → Pulse observes a redacted, structured local signal
Session end      → Pulse writes a local continuity checkpoint
Viewer           → "what Pulse will tell Claude next time"
```

Raw refs are optional, local-only, and disabled by default. Enable them only
with `PULSE_LOCAL_AUTO_RAW_REFS=1` or `PULSE_RAW_REFS=1`; public MCP recall
never returns raw captured transcripts by default.

`pulse connect claude-code` writes a local `~/.pulse/mode` marker so the daemon
reports `local-auto`. The write endpoints `pulse_observe` and
`pulse_checkpoint` are accepted only in local-auto mode; `pulse_resume` remains
safe for public MCP.

Hook summaries are bounded and redacted from Claude Code hook payloads. If a
Stop hook receives structured checkpoint fields (`summary`, `decisions`,
`open_loops`, `do_not_repeat`, `emotional_anchors`, `state_signals`), Pulse
stores them directly. If the host supplies no semantic payload, Pulse records
only lifecycle scaffolding and should not be claimed as full session
summarization.

SessionStart also reminds the host model to use `pulse_graph_delta` for durable
semantic graph changes once that tool is available. Hooks still do not call an
LLM themselves.

The first archive-migration slice is also local and preview-only:

```bash
npx @zbs-gg/pulse migrate start --dir pulse-migrate --open
npx @zbs-gg/pulse migrate start --dir pulse-migrate --open --watch
npx @zbs-gg/pulse migrate concierge --html pulse-migrate.html --brief pulse-migrate-brief.md --open
npx @zbs-gg/pulse migrate guide chatgpt --open
npx @zbs-gg/pulse migrate guide claude
npx @zbs-gg/pulse migrate guide codex
npx @zbs-gg/pulse migrate guide claude-code
npx @zbs-gg/pulse migrate request chatgpt --open
npx @zbs-gg/pulse migrate request claude --open
npx @zbs-gg/pulse migrate request codex --open
npx @zbs-gg/pulse migrate request claude-code --open
npx @zbs-gg/pulse migrate wait-latest chatgpt --html pulse-preview.html --out pulse-preview.json --open
npx @zbs-gg/pulse migrate wait-latest claude --html pulse-preview.html --out pulse-preview.json --open
npx @zbs-gg/pulse migrate preview-latest chatgpt --html pulse-preview.html --out pulse-preview.json --open
npx @zbs-gg/pulse migrate preview-latest claude --html pulse-preview.html --out pulse-preview.json --open
npx @zbs-gg/pulse migrate preview ~/Downloads/chatgpt-export.zip
npx @zbs-gg/pulse migrate preview ~/Downloads/chatgpt-export
npx @zbs-gg/pulse migrate preview ~/Downloads/claude-export --json
npx @zbs-gg/pulse migrate commit pulse-preview.json --confirm "import pulse graph" --open
npx @zbs-gg/pulse migrate commit pulse-preview.json --confirm "import pulse graph" --privacy sensitive --open
npx @zbs-gg/pulse viewer --open
npx @zbs-gg/pulse viewer --base http://127.0.0.1:18789 --data-dir /path/to/pulse-data --thread-id archive-import --open
```

`migrate start` is the one-command hand-hold: it writes the concierge page and
`pulse-migrate-status.html`, opens the ChatGPT/Claude export pages with `--open`,
immediately previews local Codex/Claude Code history when present, and prints
the exact `wait-latest` commands for the remote archive zips. With `--watch`,
it keeps waiting for the ChatGPT/Claude zips and opens the preview when a
matching archive arrives. It does not import anything.
`migrate concierge` writes one local browser page that points the user to the
ChatGPT/Claude archive buttons, Codex/Claude Code local-history folders, and the
exact copyable preview/import/viewer commands; `--brief <file>` also writes a
short Markdown handoff for a reviewer or user. `migrate guide` walks the user to
one specific browser page or local history folder. `migrate request` is the main
hand-hold path. For ChatGPT/Claude, Pulse opens the archive settings page, tells
the human which button to press, waits for the export zip to appear in
`~/Downloads`, then opens the safe preview. For Codex/Claude Code, Pulse
previews local history immediately. By default it writes `pulse-preview.html`
and `pulse-preview.json`. `migrate wait-latest` is the same watcher without
opening the host page. `migrate preview-latest` is the shorter path when the
export zip is already downloaded; pass `--downloads <dir>` if the browser saved
it elsewhere.
If no archive is found yet, Pulse explains that the export may still be
preparing and points back to the request button. `migrate preview` is the manual path:
it scans a ChatGPT/Claude-like export zip, an extracted export folder, a
Codex/Claude Code JSONL history folder, or JSON/JSONL file, counts
conversations/messages, and shows safe people/thread candidates. It does not
write raw chat text or memory records. `--html <file>` writes a local browser
preview around candidate threads, decisions, open loops, do-not-repeat notes,
emotional anchors, people found, token economy, a private "why this may matter
now" preview hypothesis, and review decisions before committing anything into Pulse;
`--out <file>` writes the commit-ready JSON next
to it and adds copyable import/viewer commands inside the preview. Confirmed
commit writes only structured
`pulse.semantic_delta.v1` graph deltas; add `--open` to open the viewer
immediately after import. Imported graph items default to `private`; use
`--privacy sensitive` or `--privacy normal` only as an explicit local/operator
override. Zip auto-unpack rejects unsafe archive
paths before extraction. The local viewer then shows what Pulse will inject
next, saved decisions, open loops, emotional/state anchors, and reviewed people
context assembled from the graph. If the running daemon uses a temporary or
custom data directory, pass the same path to `pulse viewer --data-dir`; use
`--thread-id` to open the same continuity thread that the import wrote.

Archive migration is a product-facing migration and inspection layer, not a new
paper result. Keep it outside the evaluated retrieval claims until it has its
own migration-quality and privacy evaluation; see
[`docs/archive-migration-paper-boundary.md`](docs/archive-migration-paper-boundary.md).

```bash
# build
go build ./cmd/pulse

# run on 127.0.0.1:18789 with a data dir
go run ./cmd/pulse -addr 127.0.0.1:18789 -data-dir ~/.pulse

# test
go test ./...
```

Public-package wording: the current proof can show one Pulse-only Claude Code
two-session decision restore plus an 8/8 Context Restore Bench. Do not claim
"Auto Continuity v1 shipped" or nontechnical production readiness.

### Embeddings

Pulse picks an embedder at startup:

1. `COHERE_API_KEY` (env, or `~/.pulse/cohere-key.txt`) → Cohere `embed-v4.0`
2. `PULSE_LOCAL_EMBED_PYTHON` + `PULSE_LOCAL_EMBED_HELPER` +
   `PULSE_LOCAL_EMBED_MODEL`, all set and pointing at existing files →
   an optional local embedding helper subprocess
3. otherwise: retrieval-only mode (`/retrieve` and `/context/query` return
   `503` until an embedder is configured; previously-ingested data is still
   served)

An optional grounded query-expansion helper can be enabled with
`PULSE_QUERY_EXPAND=1` plus the matching `PULSE_QUERY_EXPAND_*` paths.

## MCP

`mcp/` is a thin Model Context Protocol server that hands queries to a
running Pulse HTTP engine. It exposes `pulse_remember` for strict
`pulse.memory_capsule.v1` writes, `pulse_graph_delta` for strict
`pulse.semantic_delta.v1` graph updates, and `pulse_resume` for startup
continuity; raw transcript ingestion is not part of the Pulse MCP v1 contract.
The hosted Cloudflare connector currently narrows `pulse_graph_delta` to
continuity checkpoints only; use local Pulse for full graph writes.
See `mcp/README.md`.

## License

AGPL-3.0 — see [LICENSE](LICENSE). Commercial dual-license available, see [`COMMERCIAL.md`](../COMMERCIAL.md).
