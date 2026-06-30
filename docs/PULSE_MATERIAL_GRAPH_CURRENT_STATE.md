# Pulse Material Graph Current State

Date: 2026-06-09
Status: Phase 0 audit
Scope: Pulse v0.5 planning only. No runtime behavior is changed by this
document.

## Verdict

Pulse already has most of the storage and transport pieces needed for a
Material Graph, but they are not yet named or exposed as one product concept.

The safest next step is not a Graphify clone, not a Graphify dependency, and
not a new broad extraction engine.
The safest next step is to formalize a Material Graph projection over existing
Pulse primitives:

- `pulse.semantic_delta.v1` for host-extracted graph writes.
- `entities`, `relations`, `facts`, `events`, and `event_entities` for material
  graph storage.
- `continuity_checkpoints` for decisions, open loops, do-not-repeat warnings,
  emotional anchors, state signals, active threads, review insights, and source
  refs.
- `/viewer/data` for what the next host will receive.
- import preview/review JSON for reviewed candidate threads and ignored entity
  gates.

Graphify is only a useful external benchmark for packaging and corpus mapping.
It answers: "what is connected to what in these materials?" Pulse must answer a
larger continuity question: "what is connected to what, why did it matter,
where did we stop, and what should the next AI session not lose?"

## Graphify Boundary

Hard rule: Pulse builds and owns its own Material Graph.

Canonical boundary: `docs/PULSE_GRAPHIFY_BOUNDARY.md`.

Graphify must not be:

- a runtime dependency;
- the canonical graph store;
- the v0 extractor;
- part of Pulse onboarding;
- required for Pulse to work;
- a hidden substrate behind `pulse_graph_delta`;
- treated as evidence unless its output is imported through normal reviewed
  Pulse sources.

Allowed use:

- external benchmark for graph/report ergonomics;
- optional one-off comparison artifact in `pulse/artifacts`;
- future reviewed import source, if explicitly selected and labeled.

Forbidden use:

- using `graphify-out/graph.json` as the Pulse database;
- making Pulse a wrapper around Graphify;
- asking users to install Graphify to get Pulse continuity;
- relying on Graphify to extract user/work context in v0.5.

## Existing Surfaces

| Surface | Current role | Material Graph fit | Gap |
|---|---|---|---|
| `pulse.memory_capsule.v1` | User-approved redacted memory item. | Good source for individual decisions, project state, open loops, corrections, do-not-repeat, and state signals. | It is item-based, not graph-shaped. |
| `pulse.semantic_delta.v1` | Host-extracted nodes, edges, facts, events, and continuity. | Closest current Material Graph write contract. | Node kinds are generic; source refs/status/salience are not first-class on every object. |
| `entities` | Canonical graph nodes with aliases, salience, emotional weight, description. | Good physical node store. | Current kind set does not include all v0 material object kinds. |
| `relations` | Entity-to-entity edges with kind, strength, context. | Good physical edge store. | No per-edge confidence, source refs, privacy tier, or review status columns. |
| `facts` | Entity-scoped claims with confidence/provenance/domain. | Still useful as the legacy graph fact projection. | New structured claims should flow into first-class `assertions`; source refs remain indirect on legacy facts. |
| `assertions` | Bitemporal, scoped claim identity store. | Canonical layer for stable claims, supersession, retraction, and future wiki projection. | Now populated from structured `semantic_delta.facts`; Material Graph and context-query reads still need assertion-backed projection. |
| `events` / `event_entities` | Time-bearing events connected to graph entities. | Good place for proof, review, and emotional/state anchors attached to objects. | Emotion/status/source-ref semantics are under-specified for product use. |
| `continuity_checkpoints` | Resume source for where we left off. | Already carries the Continuity Pack. | It stores strings, not references to material graph node IDs. |
| `BuildResume` | Builds `pulse.continuity.v1` resume markdown and sections. | Correct product center: "what Pulse will tell Claude next". | Does not yet expose a Material Graph node/edge projection for resume evidence. |
| import preview/review JSON | Candidate threads, review decisions, active threads, ignored entities. | Good review gate for Material Graph v0. | Review actions are preview-driven and not yet persisted as graph object status. |
| viewer graph profile | Shows people, memories, emotions, relationships, notes, profiles, hidden entities. | Useful first viewer slice. | It is profile-oriented, not a focused thread/material map with timeline. |
| MCP `pulse_graph_delta` | Host model writes structured graph deltas. | Correct transport for v0. | Schema is currently `additionalProperties: false`, so new fields must be added deliberately. |
| wipe/hide/restore | Deletes host-extracted memory/graph or hides noisy graph entities. | Correct trust mechanics. | Object-level correction/ignore/restore is not yet generalized. |

## What Already Works

1. Host-extracted graph writes exist.

`pulse_graph_delta` posts to `/graph/delta`, and the Go store materializes
nodes, edges, facts, events, and continuity through `SaveSemanticDelta`.

2. Raw transcript capture is rejected at the semantic layer.

`SemanticDelta.RawInputIncluded` must be false, timestamps must be RFC3339, and
semantic text rejects raw-looking transcript, secret, and path-like payloads.

3. Continuity and graph are already connected.

`SaveSemanticDelta` can save graph rows and then save a continuity checkpoint.
`BuildResume` reads checkpoints, observations, and recent memory capsules into
a compact resume.

3a. Structured facts now assimilate into assertions.

`SaveSemanticDelta` writes `facts[]` into the legacy `facts` table and the
first-class `assertions` table. Structured facts with `predicate/object_text`
use `subject + predicate` as the stable claim key and supersede changed current
values; unstructured facts become stable statement assertions with
`object_text = "true"`. Assertion assimilation now requires explicit
non-personal `scope_id`s, preserves semantic-delta confidence exactly, and links
source events per fact through `source_event_refs` instead of attaching every
event in the delta to every assertion.

4. Active reviewed threads are already gated.

`activeReviewInsights` filters review insights through `ActiveThreads`, and the
store tests assert that inactive review insights do not leak into the resume.

5. Ignored review entities are already considered during import commit.

`buildSemanticDeltaFromPreview` removes ignored review names from people,
relationships, facts, pulse insights, active threads, and review insights before
building the semantic delta.

6. The viewer already has graph inspection and correction primitives.

`ViewerData` returns `GraphProfile` plus `HiddenEntities`. Viewer actions can
hide and restore graph entities via `/graph/entity/hide` and
`/graph/entity/restore`.

7. Wipe covers host-extracted memory, assertions, and graph rows.

`WipeMemory` deletes memory capsules, continuity rows, semantic-delta
assertions, and host-extracted graph rows.

## Main Gaps

### 1. Material Graph is not an explicit product model

Today Pulse has graph tables and semantic deltas, but no named Material Graph
schema that says:

- what object kinds exist;
- which edge kinds are supported;
- what status means;
- where source refs live;
- how emotional/state salience attaches to material objects;
- which material graph nodes can enter `pulse_resume`.

### 2. Required v0 node kinds are not all first-class

The current semantic node kind validator accepts:

`person`, `place`, `project`, `org`, `product`, `community`, `skill`,
`concept`, `thing`, `event_series`.

The v0 brief needs:

`person`, `project`, `repo`, `file`, `code_symbol`, `feature`, `bug`,
`decision`, `open_loop`, `proof`, `review`, `claim`, `idea`, `event`,
`emotion_anchor`, `state_signal`, `thread`.

Some of these can be projected from existing data today:

- `decision`, `open_loop`, `do_not_repeat`, `emotion_anchor`, `state_signal`,
  and `thread` from continuity checkpoints and memory capsules;
- `event` and `proof` from events and source refs;
- `person` and `project` from entities.

But the schema should name the v0 object kinds even before every kind has a
dedicated database row.

### 3. Source refs are not attached everywhere

Continuity checkpoints have `source_refs`. Memory capsules expose evidence refs.
Facts/events have provenance-like fields. But Material Graph v0 needs source
refs on every node and edge, with a clear label when a source is hypothesis,
reviewed, user-confirmed, or derived.

### 4. Salience is too flat

The current graph supports numeric `salience` and `emotional_weight`. The v0
brief needs structured salience:

```json
{
  "strategic": "high",
  "trust": "high",
  "emotional": "medium"
}
```

This should be an overlay on a material object, not an emotion floating by
itself.

### 5. Review status is not a shared object state

Import review can stage confirmed/ignored/private decisions, and the commit path
honors ignored names. But graph objects do not yet carry a consistent status:

- `hypothesis`
- `reviewed`
- `user_confirmed`
- `corrected`
- `ignored`

### 6. Viewer output is not yet a focused material map

The current viewer leads with the right product question, but the graph section
is profile/gallery shaped:

- people;
- memories;
- emotions;
- relationships;
- low-stakes notes;
- person profiles.

Material Graph v0 needs a focused thread map:

- selected thread;
- decisions;
- open loops;
- do-not-repeat warnings;
- proofs/evidence refs;
- related people/projects/files;
- timeline;
- salience and confidence.

### 7. Resume has sections but not material references

`pulse_resume` already returns active decisions, active reviewed threads,
review insights, open loops, do-not-repeat, emotional/state context, suggested
next step, and evidence refs.

The missing v0 link is: which material graph nodes/edges justified each resume
item?

### 8. Code is visible structurally, but not causally

Pulse can already store graph entities and events, but it does not yet expose
the reason a code surface matters. For code work, the graph should not stop at:

```text
file imports function
```

It should also preserve:

- why the file changed;
- which trust boundary the change protected;
- which blocker or review failure caused it;
- which proof verified it;
- which do-not-repeat rule came out of it.

Example target projection:

```text
file:internal/store/continuity.go
-> implements: active-only resume
-> changed_because: inactive candidate insight leaked into resume
-> do_not_repeat_for: pulse_resume
-> proved_by: active-scope regression test
```

This is the practical difference between a codebase map and Pulse continuity.

## Safest Integration Point

Use the existing `pulse_graph_delta` -> `/graph/delta` -> `SaveSemanticDelta`
path.

Do not add a new ingestion path in v0. Do not parse arbitrary folders, PDFs,
videos, or images. Do not add Graphify or any Graphify-powered extractor.

The first implementation should add a Material Graph projection layer over
existing storage:

```text
semantic_delta + memory_capsules + continuity_checkpoints + reviewed_import
-> material_graph_v0 projection
-> focused viewer map
-> resume evidence references
```

This lets Pulse improve the product model without destabilizing install, first
memory proof, wipe, or existing MCP hosts.

## Source Ladder

Use a staged source ladder. Do not jump straight to multimodal import.

Phase 1 sources:

- Claude Code sessions through existing Pulse hooks/MCP calls.
- Codex sessions when available through host-extracted deltas.
- Pulse/Garden repo docs and handoff files only when explicitly selected.
- Reviewed import JSON.
- Review bundles and proof manifests as source-backed material.

Later sources:

- PDFs.
- Images.
- Video.
- Krisp/call transcripts.
- Telegram.
- LinkedIn.

Every new source must preserve the same trust rule: no raw transcript by
default, source-backed or hypothesis-labeled graph material only.

## Suggested Next Tests

Add tests before runtime changes:

1. Messy ownership input becomes material graph output.

Input summary:

```text
atlas should not hold the graph; it belongs in Pulse; Atlas only decides when
to surface it
```

Expected:

- nodes: Atlas, Pulse, People Graph or Material Graph;
- decision: Atlas must not own the graph;
- edge: graph owned_by_layer Pulse;
- do-not-repeat: do not create People Graph tables in Atlas;
- continuity pack includes the decision;
- no raw transcript stored.

2. Ignored entity cannot appear in Material Graph projection.

Expected:

- ignored entity absent from nodes, edges, facts, events, insights, viewer map,
  and resume.

3. Inactive candidate thread cannot enter resume.

Expected:

- candidate thread visible as candidate in preview/material map;
- not present in `pulse_resume` until active/reviewed.

4. Emotion is salience, not fact.

Expected:

- "maybe curiosity" is `status: "hypothesis"`;
- confirmed/corrected/ignored states change the overlay;
- resume labels hypothesis vs confirmed.

5. Source refs required in projection.

Expected:

- every projected node and edge has at least one source ref or is labeled
  `source_status: "derived_from_reviewed_sources"` with inspectable parent
  source refs.

## What Not To Change Yet

- Do not make Material Graph the first onboarding step.
- Do not add automatic all-chat import.
- Do not add backend model requirements.
- Do not add cloud or store connector claims.
- Do not move graph ownership to Atlas or Heart.
- Do not add Graphify or any Graphify-powered extractor.
- Do not build a general codebase graph extractor.
- Do not create a large graph UI before the focused thread map exists.

## Phase 0 Conclusion

Pulse already has the primitives. The v0.5 work is mainly a product/model
tightening:

1. Name the Material Graph schema.
2. Lock the Graphify boundary as a canonical Pulse doc.
3. Add source/status/salience semantics.
4. Project active reviewed material into resume.
5. Show focused material maps in the viewer.
6. Preserve agent-first install, first memory proof, wipe, and active-only
   trust gates.
