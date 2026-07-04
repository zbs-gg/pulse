# State-aware capsule retrieval — wire memory capsules into the state-aware engine

Date: 2026-07-04 · Risk: medium · Verdict: **GO**

## Problem (grounded)

The B2B state eval (`ai-course-nik/eval/state-tasks.json`, 5 scenarios × 3
states) requires: the SAME query at DIFFERENT `user_state` returns a DIFFERENT
remembered item (the one whose `for_state` matches). Pulse is the only tested
system with a `user_state` input, but scored 0/15 because of two wiring gaps:

1. **Capsules are invisible to the engine.** `RememberCapsule`
   (`internal/store/memory_capsule.go:90`) writes ONLY to `memory_capsules`;
   no event row is created. `pulse_context_query` → `contextquery.Query` →
   retrieval engine reads `events JOIN event_embeddings`
   (`internal/retrieve/hybrid.go` Reload) → remembered capsules can never
   surface. The projection returned all-null on every eval pair.
2. **No state↔item matching channel.** The eval sent
   `deadline_pressure`/`job_insecurity`/`burnout`/`energy` inside
   `user_state`; `retrieve.UserState` (`internal/retrieve/state.go:5`) has no
   such fields — JSON decode silently drops them. The frozen v3 state boosts
   key off biometrics + Plutchik mood + (English) text heuristics
   (`state_fit.go`), which cannot select between three Russian-language advice
   capsules.

## Design

### A. Capsule → event projection (retrievability)

- Migration **032**: `ALTER TABLE memory_capsules ADD COLUMN event_id INTEGER`
  (link, NULL = not projected) and `ALTER TABLE events ADD COLUMN tags TEXT`
  (JSON array, NULL = none).
- In `RememberCapsule` (same tx): for items with `privacy_tier='normal'`
  (conservative — sensitive/private stay out of the graph until a
  privacy-floor follow-up), also INSERT an event: `title` = capsule kind,
  `description` = redacted_summary, `ts` = source timestamp (fallback
  created_at), `domain='real'`, `provenance='capsule'`, `tags` = capsule tags
  JSON; store the new event id in `memory_capsules.event_id`.
- `internal/server/memory.go` remember handler: after a successful store
  write, if the retrieval engine is present and ready, call
  `EmbedAndIndexEvents` with the new (event_id, text) docs — same pattern as
  the `/graph/delta` handler — so capsules are retrievable immediately.
  Engine nil/embedder off ⇒ skip silently (events exist, dark until embedder).
- `BackfillCapsuleEvents()` store method: idempotent
  (`WHERE event_id IS NULL AND privacy_tier='normal'`), called once at daemon
  startup (main.go) + embed-index the produced docs.
- Opt-out env `PULSE_CAPSULE_EVENTS=off` (default ON — this is the product
  promise: remembered memory must be reachable by the engine; Nik explicitly
  asked to make the default install pass the case).

### B. State-tag affinity boost (state → which item wins)

Convention: a capsule tag `state:<flag>` declares "this advice is for that
state" (e.g. `state:deadline_pressure`, `state:job_insecurity`,
`state:calm`). Host-side extraction decides the tags; the daemon just matches.

- `retrieve/state.go`: ADD optional field
  `ContextFlags map[string]float64 \`json:"context_flags,omitempty"\`` to
  UserState. Additive only — no existing method touched; absent field ⇒
  identity (old behavior byte-identical).
- New file `internal/retrieve/state_tag_boost.go`: a separate post-scoring
  re-rank step (same pattern as the assertion demotion overlay / access-freq
  Phase B), applied to the final candidate list in `Retrieve`:
  - active flags = `{k : v ≥ 0.5}` from `user_state.context_flags`
  - event with tag `state:k` where k active → score ×1.15 (one boost max)
  - event with tag `state:calm` → ×1.15 only when user_state present AND
    context_flags has no active keys
  - **exact neutrality**: no user_state → untouched list; no `state:*` tags →
    untouched; flags present but no tagged events → untouched. Multiplier
    identical for all candidates ⇒ order unchanged (stable sort).
  - env `PULSE_STATE_TAG_BOOST=off` opt-out (default ON — provably neutral
    without the signal).
  - v3 path untouched: operates on a copy of final scores AFTER
    `scoreEventsV3`/fusion, never inside them.
- `hybrid.go` Reload: load `events.tags` into `eventTags [][]string`
  (additive slice, scorer never reads it; only the new post-step does).
- `contextquery`: verify `user_state` JSON (incl. `context_flags`) reaches
  `retrieve.RetrieveRequest.UserState` intact; fix mapping if it's lossy.
- MCP (`mcp/src/index.ts`): `user_state` already passes arbitrary objects; add
  `context_flags` to the tool description so agents know the channel.

## Files

- `pulse-app/internal/store/migrations/032_capsule_events.sql`
- `pulse-app/internal/store/memory_capsule.go` (projection + backfill)
- `pulse-app/internal/server/memory.go` (embed-index after remember)
- `pulse-app/cmd/pulse/main.go` (startup backfill + env flags — env wiring
  only, no billing/network-path change)
- `pulse-app/internal/retrieve/state.go` (ContextFlags, additive)
- `pulse-app/internal/retrieve/state_tag_boost.go` (+ test)
- `pulse-app/internal/retrieve/hybrid.go` (load tags; call post-step)
- `pulse-app/internal/store/memory_capsule_test.go` (projection/backfill)
- `mcp/src/index.ts` (doc string)

## Tests

1. Projection: RememberCapsule(normal) creates linked event with tags;
   sensitive/private do NOT project; wipe still clean.
2. Backfill idempotent (second run = 0 new).
3. Boost unit: (a) no user_state ⇒ identical order; (b) flags but untagged
   events ⇒ identical; (c) `PULSE_STATE_TAG_BOOST=off` ⇒ identical;
   (d) deadline flag ⇒ `state:deadline_pressure` item outranks semantic
   near-ties; (e) calm (empty flags, state present) ⇒ `state:calm` wins.
4. Integration (eval-shaped): 3 capsules (checklist/triage/cover-yourself,
   NEUTRAL fixture text, no personal data) + fake embedder giving near-tie
   cosines → 3 states → 3 different correct top-1s.

## Red-line check

- `v3boosts.go`, `state_fit.go`, `scoreEventsV3` — byte-identical. New boost
  is a separate additive post-step collapsing to exact neutrality without its
  signal. UserState gains an optional field no frozen code reads.
- No LLM in daemon; projection copies the already-redacted summary (the store
  already rejects transcript/secret/path content at capsule write).
- Default-ON justification: projection is a wiring fix of the shipped promise
  (remember → retrievable); boost is provably neutral absent signal. Both have
  env opt-outs.
- Public repo: fixture texts neutral/synthetic (no Nik data).

## After merge (outside repo)

Rebuild + restart local daemon; re-seed the ai-course-nik eval with
`state:*` tags in its pulse adapter; re-run to target 15/15; log to eval
ledger (before: 0/15 context_query · 1/15 recall).
