# Pulse

State-aware memory and retrieval engine, written in Go. Pulse stores
conversational events plus a lightweight knowledge graph in a single SQLite
file and retrieves them with a hybrid ranker that combines dense embeddings,
lexical search, and a set of *conditional* affective boosts.

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

Benchmark framing here is deliberately conservative: Pulse is built to be
evaluated on public memory benchmarks (e.g. LongMemEval, LoCoMo). This
repository ships the engine and its unit/parity tests, not any private
evaluation corpus.

## Layout

```
cmd/pulse/         HTTP server entrypoint
cmd/pulse-smoke/   minimal smoke-test client
internal/retrieve/ hybrid retrieval: router, factual/empathic/chain, v3 boosts
internal/store/    SQLite store + migrations (events, graph, emotions, FTS5)
internal/embed/    embedders (Cohere API + optional local subprocess)
internal/ingest/   observation ingest + sensitive-actor redaction
internal/contextquery/ scoped context projection over the graph
internal/server/   HTTP handlers
internal/...       prompt assembly, providers, erasure (GDPR), health, outbox
mcp/               MCP server (thin wrapper over the HTTP API)
```

## Build & run

Requires Go 1.25+. SQLite is pure-Go (`modernc.org/sqlite`), so no CGo and
no separate vector database.

```bash
# build
go build ./cmd/pulse

# run on 127.0.0.1:18789 with a data dir
go run ./cmd/pulse -addr 127.0.0.1:18789 -data-dir ~/.pulse

# test
go test ./...
```

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
running Pulse HTTP engine. It contains no memory engine itself. See
`mcp/README.md`.

## License

Pulse is licensed under the **GNU AGPL-3.0** — see [LICENSE](LICENSE).

For proprietary or closed-SaaS use without AGPL obligations, a commercial
license is available — see [COMMERCIAL.md](COMMERCIAL.md).

© 2026 Nikita Shilov · developed under the zbs.gg banner.
