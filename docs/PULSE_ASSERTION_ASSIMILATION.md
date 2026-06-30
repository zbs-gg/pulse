# Pulse Assertion Assimilation

Date: 2026-06-30
Status: implemented first runtime slice

## Verdict

Do not embed an external LLM-wiki system into Pulse as a second memory store.

Pulse should absorb the useful part of the LLM-wiki pattern: converting
host-extracted material into stable, source-backed claims that can be checked,
superseded, scoped, and projected into Material Graph / resume / future wiki
views.

Canonical formula:

```text
semantic_delta facts
-> assertions
-> material graph / continuity pack
-> optional human wiki projection
```

The wiki view is a projection. The source of truth remains Pulse storage:
`assertions`, `entities`, `relations`, `facts`, `events`, and
`continuity_checkpoints`.

## Why This Beats A Separate Wiki Store

- A markdown wiki is easy for an agent to write, but hard to trust once it
  drifts from sources.
- Pulse already has graph storage, continuity checkpoints, review gates,
  source refs, safety validation, wipe, and state-aware retrieval.
- First-class assertions add the missing claim identity layer:
  stable `claim_key`, valid-time, system-time, typed scope, visibility, and
  supersede/retract lifecycle.

## Runtime Contract

`pulse.semantic_delta.v1` facts now support optional structured assertion
fields:

```json
{
  "node": "project:pulse",
  "text": "Pulse canonical memory store is local-first.",
  "predicate": "canonical memory store",
  "object_text": "local-first Pulse store",
  "valid_from": "2026-06-30T00:00:00Z",
  "source_event_refs": ["event:pulse-store-decision"],
  "scope_type": "project",
  "scope_id": "garden",
  "visibility": "private",
  "confidence": 0.91,
  "privacy_tier": "private",
  "domain": "real"
}
```

Rules:

- `predicate` and `object_text` are paired. If either is present, both are
  required.
- `subject` is the canonical graph node resolved from `fact.node`.
- `claim_key` is `subject + predicate`, normalized by the existing assertion
  store.
- `valid_from` is valid-time. Empty means the semantic delta timestamp.
- `system_from` is the semantic delta timestamp.
- `scope_type/scope_id` default from the delta source: `project_id` -> project,
  `session_id` -> session, otherwise personal.
- If `scope_type` is explicitly set to `project`, `repo`, `agent`, or
  `session`, `scope_id` is required. `personal` may omit `scope_id`.
- `visibility` defaults to private.
- `source_event_refs` are optional semantic-delta event `client_id`s that prove
  this fact. When present, Pulse resolves them to `source_event_ids`; every ref
  must match an event in the same delta.
- For backward-compatible single-event deltas with no explicit
  `source_event_refs`, Pulse links the sole event. Multi-event deltas must use
  `source_event_refs` for precise provenance; otherwise assertions get no event
  ids instead of misleading all-event provenance.
- Semantic-delta assertion confidence is preserved as supplied; `0` is not
  inflated to `1.0`.
- Raw transcripts, secrets, local paths, invalid timestamps, unsupported scopes,
  missing non-personal `scope_id`s, unknown `source_event_refs`, and unsupported
  visibility values are rejected before persistence.

Unstructured facts still enter the assertion store as stable statements:

```text
predicate = fact.text
object_text = "true"
```

This makes them queryable as claims without causing unrelated free-text facts
on the same entity to supersede one another.

## Supersession Semantics

When a new structured fact arrives with the same `claim_key + scope`:

- same `object_text` -> no duplicate assertion row;
- different `object_text` -> old current assertion becomes `superseded`, with
  `valid_to` closed at the new assertion's `valid_from`; the new assertion
  becomes current.

This is the key difference from a wiki rewrite: history stays queryable, and
Pulse can distinguish "the world changed" from "we recorded it wrong".

## What Shipped In This Slice

- `SaveSemanticDelta` now writes assertions in the same transaction as graph
  facts/events.
- `SemanticDeltaResult` reports `assertions_upserted`.
- MCP `pulse_graph_delta` schema advertises assertion fields on `facts[]`.
- Safe Mode validation preserves clean assertion fields and rejects unsafe
  structured assertion payloads.
- Tests cover insertion, source event linkage, scoped current lookup, and
  supersession.
- `WipeMemory` clears semantic-delta assertions before host-extracted graph rows
  so entity/event foreign keys cannot leave ghost memory behind.
- Prosha hardening review fixes are included: non-personal scopes require ids,
  semantic assertion confidence stays exact, and per-fact source-event refs avoid
  over-attaching every event in a delta to every assertion.

## Follow-Ups

1. Add assertion-backed rows to `MaterialGraph` so claims can surface from the
   canonical assertion store, not only from legacy `facts`.
2. Add assertion lookup to `/context/query` for factual questions.
3. Add source-ref drilldown UI for assertion -> event/source evidence.
4. Add conflict/correction workflow: user-confirm, correct, retract, restore.
5. Add a human wiki export/view generated from assertions + Material Graph.
   This must remain a projection, not a second truth store.

## Verification

Targeted checks:

```bash
cd pulse-app
go test ./internal/store -run 'TestSaveSemanticDelta(AssimilatesStructuredFactsIntoAssertions|SupersedesChangedStructuredAssertion)' -count=1
```

```bash
npm --prefix mcp test -- --test-name-pattern 'structured assertion|unsafe structured'
```

Full gate:

```bash
make verify
```

Packaging check:

```bash
npm --prefix pulse-app/cli run prepack
```
