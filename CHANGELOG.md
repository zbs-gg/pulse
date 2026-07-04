# Changelog

All notable changes to Pulse.

## 0.6.7 — 2026-07-04

### Added
- **State-aware capsule retrieval** (#41, #42, #43) — remembered capsules are
  now projected into the retrieval graph (migration 032; normal-tier only,
  linked via `memory_capsules.event_id`, embed-indexed on write, idempotent
  startup backfill, delete/wipe cascade) and a new state channel selects
  WHICH memory wins: items tagged `state:<flag>` partition above the rest
  while `user_state.context_flags[flag]` is active, with a lexical
  term-coverage tie-break inside the group (and `state:calm` + thematic
  coherence when no flag is active). Measured on a 5-scenario × 3-state eval:
  15/15 state-appropriate-recall (factual mode; 12/15 auto) vs a 5/15 ceiling
  for state-blind systems. Mechanics validated on that set — treat external
  claims as pending a holdout. Opt-outs: `PULSE_CAPSULE_EVENTS=off`,
  `PULSE_STATE_TAG_BOOST=off`.
- **Assertion eval gate** (#32) — deterministic temporal-accuracy /
  staleness / contradiction sensor over the bitemporal assertion layer,
  hard-gated in tests so supersession regressions can't ship silently.
- **Paraphrase claim matching** (#38, off by default) — with
  `PULSE_PARAPHRASE_CLAIMS=1`, a reworded restatement whose claim_key has no
  exact match corroborates (identical object) or supersedes (changed object +
  change-cue) the embedding-nearest same-scope claim.
- **Assertion corroboration metadata** (#35) — `mention_count` /
  `last_mentioned_at` (migration 030); repeated confirmation now accumulates
  instead of vanishing.
- **Access-frequency salience** (#34 Phase A, #39 Phase B; both off by
  default) — per-event recall counters (migration 029) and a bounded re-rank
  (`PULSE_ACCESS_FREQ_BOOST=1`, max one-position climb) so frequently-recalled
  memories gain mild salience without touching the frozen v3 scoring.
- **Near-duplicate capsule consolidation** (#36, opt-in) —
  `POST /memory/consolidate` / CLI pass folds token-set-Jaccard near-dupes
  (migration 031), invalidate-not-delete, dry-run by default.
- **Procedural memory scaffold** (#33, #40) — inert `procedures` table
  (migration 028) with content-guarded, atomic store methods; nothing wired
  into retrieval yet.
- **MCP assertion validation** (#31) — `scope_type` / `visibility` /
  structured-assertion coherence checks at the MCP boundary.

### Fixed
- **Keyword recall ranked by relevance, not recency** (#30) — `pulse_recall`'s
  fallback ordered purely by `created_at`, so a fresh capsule matching one
  weak term outranked an older capsule matching every term. Now ranks by
  term coverage (recency only as tiebreak): consolidation set-recall@8
  0.55 → 0.85 on the reference corpus.
- Projected capsule events embed only the content summary — the constant
  kind prefix skewed content vectors (#43).
- Stale migration-number comments (#37).

## 0.6.6 — 2026-06-30

### Added
- **Live factual retrieval** — factual-mode queries now rank by clean cosine over
  event vectors instead of falling through to the empathic ranker, so direct
  fact lookups surface the answer event. Bitemporal assertions + precision-first
  claim resolution (change-cue + chronology guards) back supersession.
- **Temporal entity-graph retrieval** — the entity graph now feeds retrieval as a
  recall-injector. Default `anchored` on the live recall path (`/context/query`):
  entity-centric recall is improved with no regression on direct/lexical queries.
  `walk` (typed multi-hop relation traversal) is opt-in via `graph_mode`.
- **Cross-harness digest** — a new "Across your harnesses" section at the top of
  the session-start resume summarizes recent per-harness activity + a fun fact;
  the daemon viewer renders it as an animated panel. Honest empty-states.
- `GET /graph/export` (read-only graph JSON for the local graph editor).

### Unchanged
- v3 emotional scoring/gating constants are byte-identical (golden parity green).
  No raw transcript capture; negative-smoke still rejects all 15 dangerous payloads.

## 0.6.5 — 2026-06-30

### Fixed
- **Unicode-aware memory tags** — `safeTagPattern` now accepts `\p{L}\p{N}`,
  so non-ASCII tags (e.g. Cyrillic) validate on the first try. This removes the
  silent retry that added multi-second latency to `pulse_remember`. The
  secret / path / transcript guards are unchanged (negative-smoke still rejects
  all 15 dangerous payloads).

### Added
- **MCP: persistent OAuth tokens** — access/refresh tokens are written to disk
  and reloaded on start, so a daemon restart no longer logs out a hosted
  connector. Optional PIN gate on `/authorize` for hosted deployments.
- **CLI: hosted capture** — capture hooks can send a bearer
  (`PULSE_REMOTE_BEARER`) to write into a hosted store behind an auth proxy.
- Host-extracted semantic-delta capture script (`pulse-app/scripts/pulse_live_extract.py`).
- One-store multi-harness capture decision record (`docs/`).

### Docs
- GitHub presentation: README hero + badges + honest comparison table,
  `SECURITY.md`, `CONTRIBUTING.md`.
