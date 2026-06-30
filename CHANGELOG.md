# Changelog

All notable changes to Pulse.

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
