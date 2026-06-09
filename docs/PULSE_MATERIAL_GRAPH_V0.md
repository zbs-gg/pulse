# Pulse Material Graph v0

Date: 2026-06-09
Status: Phase 1 schema and integration plan
Stage: technical-friend / investor-adjacent developer preview

## Product Definition

Pulse v0.5 should be built as:

```text
Material Graph + Salience Overlay + Continuity Pack
```

Where:

- Material Graph: what exists and how it connects.
- Salience Overlay: why a material object matters emotionally, strategically,
  or trust-wise.
- Continuity Pack: what the next AI session should receive.

This is not `Graphify + Pulse`. It is not a Graphify clone and not a Graphify
dependency. Graphify maps a corpus. Pulse maps the user's living work context
and turns reviewed, source-backed material into a compact resume.

Sharp comparison:

- Graphify builds a map of materials.
- Pulse builds a map of materials and shows what matters for the next
  conversation.
- Garden makes that memory usable, human, and governed.

If Pulse does not map what the conversation is about, emotions become
free-floating labels. If Pulse maps only structure without salience and resume,
it becomes a weaker Graphify. The product is the combination.

## Graphify Boundary

Hard rule: Pulse builds and owns its own Material Graph.

Canonical boundary: `docs/PULSE_GRAPHIFY_BOUNDARY.md`.

Graphify stays outside Pulse runtime. It can be used as a benchmark for
packaging, reporting, and corpus-map ergonomics, but it must not become the
implementation substrate.

Graphify must not be:

- a runtime dependency;
- the canonical graph store;
- the v0 extractor;
- part of Pulse onboarding;
- required for Pulse to work;
- a hidden substrate behind `pulse_graph_delta`;
- treated as source-backed evidence unless its output is imported through a
  normal reviewed Pulse source.

Allowed:

- compare Pulse output against Graphify output in Memory Arena or local evals;
- keep Graphify evaluation artifacts under `pulse/artifacts`;
- consider Graphify output later as an explicitly reviewed import source.

Forbidden:

- using `graphify-out/graph.json` as the Pulse Material Graph;
- making Pulse a wrapper around Graphify;
- adding `graphifyy` to Pulse install or onboarding;
- relying on Graphify to extract user/work context in v0.5.

## Ownership Boundary

Pulse owns:

- material graph;
- graph evidence;
- source-backed observations;
- memory capsules;
- retrieval;
- thread memory;
- continuity packs;
- resume blocks;
- source refs;
- corrections;
- reviewed import state.

Heart/Elle can turn memory into human conversation later, but must not become
the memory database.

Atlas can decide when and where to surface context later, but must not own
People Graph, Material Graph, or Thread Memory.

Garden Board owns intention, specs, provenance, and acceptance proof. It must
not become the runtime memory store.

Memory Arena can compare systems and adapters later. It must not contaminate
Pulse core.

## Allowed Claims

For v0.5 preview:

- Pulse MCP Preview is local-first.
- Pulse keeps structured memory across Claude Code sessions.
- Pulse can carry active reviewed threads into a resume block.
- Pulse shows what Claude/Codex will receive next.
- Pulse does not require backend model API keys by default.
- Raw transcript capture is off by default.
- Material Graph v0 is a reviewed/source-backed preview model.

Forbidden:

- production ready;
- Claude never forgets;
- automatic all-chat import;
- Pulse Cloud ready;
- ChatGPT or Claude Store connector ready;
- Pulse fully understands emotions;
- Pulse knows what the user should do next.

## Schema Shape

Material Graph v0 is a projection schema first. It can be backed by existing
Pulse tables before every object kind has a dedicated physical table.

```json
{
  "schema": "pulse.material_graph.v0",
  "source": {
    "host": "codex",
    "thread_id": "pulse-material-graph",
    "session_id": "optional-session-id",
    "project_id": "pulse",
    "timestamp": "2026-06-09T00:00:00Z"
  },
  "nodes": [],
  "edges": [],
  "threads": [],
  "continuity_pack": {}
}
```

## Material Scope

Pulse should model the things people actually live and work around:

- people;
- projects;
- repos;
- files;
- code symbols;
- features;
- bugs;
- PRs;
- docs;
- papers;
- ideas;
- decisions;
- events;
- deadlines;
- constraints;
- proofs;
- reviews;
- open loops.

Code is material too. A file or function matters not only because it imports
another symbol, but because it protected a boundary, fixed a repeated failure,
answered a review, or proved a claim.

## Node Kinds

Required v0 kinds:

- `person`
- `project`
- `repo`
- `file`
- `code_symbol`
- `feature`
- `bug`
- `pr`
- `doc`
- `paper`
- `decision`
- `open_loop`
- `proof`
- `review`
- `claim`
- `idea`
- `event`
- `deadline`
- `constraint`
- `emotion_anchor`
- `state_signal`
- `thread`

Implementation note:

Current `pulse.semantic_delta.v1` only accepts generic graph kinds such as
`person`, `project`, `org`, `product`, `concept`, `thing`, and `event_series`.
Material Graph v0 can project richer kinds from existing rows without widening
the inbound MCP schema immediately. Widening `SemanticNode.Kind` should be a
separate tested change.

## Node Fields

Every node in the projection should follow this shape:

```json
{
  "id": "decision:atlas-must-not-own-graph",
  "kind": "decision",
  "label": "Atlas must not own People Graph",
  "summary": "Pulse owns portable continuity memory and graph evidence; Atlas may consume context and decide when to surface it.",
  "source_refs": ["pulse:checkpoint:123", "pulse:semantic_delta:456"],
  "source_status": "source_backed",
  "privacy_tier": "private",
  "confidence": 0.92,
  "status": "reviewed",
  "salience": {
    "strategic": "high",
    "trust": "high",
    "emotional": "medium"
  },
  "resume_eligible": true
}
```

Allowed `source_status`:

- `source_backed`
- `user_confirmed`
- `reviewed`
- `hypothesis`
- `derived_from_reviewed_sources`

Allowed `status`:

- `hypothesis`
- `reviewed`
- `user_confirmed`
- `corrected`
- `ignored`

Hard rule:

`ignored` nodes are never eligible for resume, focused map default view, facts,
edges, insights, or Arena comparison output.

`derived_from_reviewed_sources` requires source refs on the parent reviewed
objects. A derived object is not resume-eligible unless the projection also
emits the parent source refs that make the derivation inspectable.

## Edge Kinds

Required v0 edge kinds:

- `mentions`
- `implements`
- `depends_on`
- `blocked_by`
- `fixed_by`
- `implemented_in`
- `reviewed_in`
- `proved_by`
- `changed_because`
- `related_to`
- `owned_by_layer`
- `do_not_repeat_for`
- `emotionally_salient_for`
- `part_of_thread`
- `will_resume`

Implementation note:

Current `SemanticEdge.Kind` accepts safe slugs, so the edge taxonomy can be
introduced as a validation/documentation layer before DB changes. Do not treat
unknown safe edge slugs as public product vocabulary.

## Edge Fields

Every edge in the projection should follow this shape:

```json
{
  "id": "edge:people-graph-owned-by-pulse",
  "from": "concept:people-graph",
  "to": "project:pulse",
  "kind": "owned_by_layer",
  "summary": "People Graph / Material Graph belongs in Pulse, not Atlas.",
  "source_refs": ["pulse:checkpoint:123"],
  "source_status": "reviewed",
  "privacy_tier": "private",
  "confidence": 0.88,
  "status": "reviewed"
}
```

Hard rule:

Edges must not survive if either endpoint is ignored.

## Privacy Propagation

For projection-only nodes and edges, `privacy_tier` is computed as the strictest
tier among:

- source objects;
- endpoint nodes;
- source refs;
- thread privacy;
- review decision privacy.

Order:

```text
private > sensitive > normal
```

If privacy cannot be derived safely, default to `private`.

Projection must never downgrade privacy. For example:

```text
person:vitaly private + project:garden-launch private -> edge private
unknown source privacy -> private
```

## Salience Overlay

Emotions and state signals must attach to material objects. They are not
free-floating claims.

```json
{
  "id": "salience:graphify-threat",
  "kind": "emotion_anchor",
  "label": "Graphify felt like a competitor threat",
  "summary": "Graphify raised envy and urgency because it overlaps the public agent graph/memory category and exposes Pulse distribution weakness.",
  "attached_to": ["project:pulse", "claim:pulse-positioning", "product:graphify"],
  "emotion": {
    "label": "envy/threat",
    "status": "user_confirmed",
    "confidence": 0.95
  },
  "consequence": "Compare with facts; do not reassure without evidence.",
  "source_refs": ["pulse:checkpoint:graphify-comparison"],
  "privacy_tier": "private"
}
```

Rules:

- Emotion is a hypothesis until confirmed by the user.
- Confirmed emotion can shape salience and next-step framing.
- Corrected emotion replaces the old overlay.
- Ignored emotion is excluded from resume.
- Never render "Pulse knows you feel X" unless X is user-confirmed.

## Continuity Pack

The continuity pack is the subset of active/reviewed Material Graph that can be
shown to the next host.

```json
{
  "schema": "pulse.continuity_pack.v0",
  "thread_id": "pulse-material-graph",
  "where_we_left_off": [
    "Pulse v0.5 is being framed as Material Graph + Salience Overlay + Continuity Pack."
  ],
  "active_reviewed_threads": [
    "Material Graph v0"
  ],
  "active_decisions": [
    "Pulse owns Material Graph and continuity; Atlas consumes context but must not own the graph."
  ],
  "open_loops": [
    "Decide whether Material Graph v0 is a projection only or also widens semantic_delta node kinds."
  ],
  "do_not_repeat": [
    "Do not depend on Graphify, clone Graphify, or lead onboarding with a large graph UI."
  ],
  "relevant_emotional_state_context": [
    "Graphify comparison is competitor/positioning anxiety, not casual tool curiosity."
  ],
  "evidence_refs": [
    "pulse:checkpoint:123"
  ],
  "material_refs": [
    "decision:atlas-must-not-own-graph",
    "thread:material-graph-v0"
  ]
}
```

Rules:

- Inactive candidate threads stay out of the pack.
- Ignored entities stay out of the pack.
- Raw prompts/text stay out when raw capture is false.
- Estimated token savings must be labeled estimated.

## UI Projection

Do not lead with a giant graph. The viewer should keep the current first screen
hierarchy:

1. Pulse keeps the thread.
2. What Pulse will tell Claude next.
3. Active reviewed threads.
4. Focused material map.
5. Timeline and evidence.

Pulse onboarding remains:

```text
first memory -> what Claude gets next -> fresh session proof -> import later
```

Material Graph appears after the first memory/resume proof, not before it.

Material Graph v0 should support three bounded modes.

### 1. Thread Map

Shows the main active or candidate threads, for example:

- Garden Memory Arena.
- Pulse MCP Preview.
- Agent-first onboarding.
- Paper/Zep.
- Vitaly call.
- Material Graph.

This is a map of major lines, not every node.

### 2. Focused Neighborhood

Clicking one thread should show a bounded neighborhood:

- features;
- proofs;
- decisions;
- blockers;
- files;
- people;
- open loops;
- salience overlays;
- source refs.

Example:

```text
Pulse MCP Preview
-> feature: Auto Continuity
-> proof: Claude Code E2E
-> blocker: one-command install
-> decision: agent-first onboarding
-> file: internal/store/continuity.go
```

### 3. Timeline

For a selected thread, the timeline should show how understanding changed:

```text
Jun 07 - Pulse-only continuity proof accepted
Jun 07 - Context restore bench accepted
Jun 08 - Agent-first onboarding accepted
Jun 08 - Material Graph concern raised
```

Timeline entries are material events with source refs, not a decorative changelog.

## Code Context Example

Graphify can show that `continuity.go` is connected to resume logic. Pulse
should show why that connection matters:

```json
{
  "nodes": [
    {
      "id": "file:internal-store-continuity-go",
      "kind": "file",
      "label": "internal/store/continuity.go",
      "summary": "Builds Pulse resume sections and filters active review insights.",
      "source_refs": ["repo:pulse"]
    },
    {
      "id": "decision:active-only-resume",
      "kind": "decision",
      "label": "Only active reviewed threads enter resume",
      "summary": "Inactive candidate insights must not appear in pulse_resume.",
      "source_refs": ["proof:active-scope-redgreen"],
      "salience": {
        "strategic": "medium",
        "trust": "high",
        "emotional": "medium"
      },
      "resume_eligible": true
    }
  ],
  "edges": [
    {
      "from": "decision:active-only-resume",
      "to": "file:internal-store-continuity-go",
      "kind": "implemented_in",
      "summary": "BuildResume filters review insights through active threads."
    },
    {
      "from": "decision:active-only-resume",
      "to": "claim:pulse-trust-boundary",
      "kind": "changed_because",
      "summary": "Trust concern: inactive candidate material must not be injected into next context."
    }
  ]
}
```

## Minimal Proof Case

Given messy input:

```text
ну короче кажется атлас не должен держать граф, пусть это будет в пульсе, а атлас только решает когда это показывать
```

Material Graph v0 should project:

```json
{
  "nodes": [
    {
      "id": "project:atlas",
      "kind": "project",
      "label": "Atlas",
      "summary": "Layer that may decide when and where to surface context.",
      "status": "reviewed",
      "source_status": "source_backed",
      "source_refs": ["pulse:memory_capsule:atlas-ownership-demo"],
      "privacy_tier": "private",
      "confidence": 0.9
    },
    {
      "id": "project:pulse",
      "kind": "project",
      "label": "Pulse",
      "summary": "Layer that owns portable continuity memory and graph evidence.",
      "status": "reviewed",
      "source_status": "source_backed",
      "source_refs": ["pulse:memory_capsule:atlas-ownership-demo"],
      "privacy_tier": "private",
      "confidence": 0.9
    },
    {
      "id": "concept:people-graph",
      "kind": "claim",
      "label": "People Graph / Material Graph ownership",
      "summary": "Graph ownership belongs in Pulse, not Atlas.",
      "status": "reviewed",
      "source_status": "source_backed",
      "source_refs": ["pulse:memory_capsule:atlas-ownership-demo"],
      "privacy_tier": "private",
      "confidence": 0.86
    },
    {
      "id": "decision:atlas-must-not-own-graph",
      "kind": "decision",
      "label": "Atlas must not own the graph",
      "summary": "Atlas may consume Pulse context and decide when to surface it, but graph memory belongs to Pulse.",
      "status": "reviewed",
      "source_status": "source_backed",
      "source_refs": ["pulse:memory_capsule:atlas-ownership-demo"],
      "privacy_tier": "private",
      "confidence": 0.92,
      "resume_eligible": true
    },
    {
      "id": "open-loop:material-graph-v0",
      "kind": "open_loop",
      "label": "Define Material Graph v0 integration",
      "summary": "Specify how Pulse projects source-backed material graph nodes into resume without cloning Graphify.",
      "status": "reviewed",
      "source_status": "source_backed",
      "source_refs": ["pulse:memory_capsule:atlas-ownership-demo"],
      "privacy_tier": "private",
      "confidence": 0.84,
      "resume_eligible": true
    }
  ],
  "edges": [
    {
      "from": "concept:people-graph",
      "to": "project:pulse",
      "kind": "owned_by_layer",
      "summary": "People Graph / Material Graph is owned by Pulse.",
      "source_status": "source_backed",
      "source_refs": ["pulse:memory_capsule:atlas-ownership-demo"],
      "privacy_tier": "private",
      "confidence": 0.9,
      "status": "reviewed"
    },
    {
      "from": "project:atlas",
      "to": "project:pulse",
      "kind": "depends_on",
      "summary": "Atlas consumes context from Pulse before deciding when to surface it.",
      "source_status": "source_backed",
      "source_refs": ["pulse:memory_capsule:atlas-ownership-demo"],
      "privacy_tier": "private",
      "confidence": 0.82,
      "status": "reviewed"
    },
    {
      "from": "decision:atlas-must-not-own-graph",
      "to": "project:atlas",
      "kind": "do_not_repeat_for",
      "summary": "Do not create graph memory tables in Atlas.",
      "source_status": "source_backed",
      "source_refs": ["pulse:memory_capsule:atlas-ownership-demo"],
      "privacy_tier": "private",
      "confidence": 0.9,
      "status": "reviewed"
    },
    {
      "from": "decision:atlas-must-not-own-graph",
      "to": "thread:material-graph-v0",
      "kind": "will_resume",
      "summary": "Fresh sessions should receive this architecture boundary.",
      "source_status": "source_backed",
      "source_refs": ["pulse:memory_capsule:atlas-ownership-demo"],
      "privacy_tier": "private",
      "confidence": 0.88,
      "status": "reviewed"
    }
  ],
  "continuity_pack": {
    "active_decisions": [
      "Atlas must not own People Graph / Material Graph; Pulse owns portable continuity memory and graph evidence."
    ],
    "do_not_repeat": [
      "Do not create People Graph or Material Graph storage in Atlas."
    ],
    "open_loops": [
      "Implement Material Graph v0 as a Pulse projection before expanding extraction."
    ]
  }
}
```

No raw transcript should be persisted by default. The original messy sentence can
be used as a test fixture input in code tests, but runtime storage should keep
only redacted/source-backed summaries unless raw capture is explicitly enabled.

## Memory Arena Compatibility

Garden Memory Arena should compare more than recall text. Material Graph v0
should expose enough structured output for future Arena adapters to compare:

- extracted entities;
- preserved decisions;
- preserved open loops;
- preserved do-not-repeat warnings;
- emotional/state anchors and whether they are hypothesis or confirmed;
- source refs and confidence;
- resume/context block each system would give the next host;
- misses and leaks.

For the messy Atlas/Pulse graph ownership input, Pulse's expected Arena result
is:

- entities: Atlas, Pulse, People Graph or Material Graph;
- decision: Atlas must not own People Graph;
- edge: People Graph owned_by_layer Pulse;
- reason: protect architecture boundary;
- do-not-repeat: do not put People Graph in Atlas;
- resume: active decision for future sessions.

Systems that only return a fact summary should score lower on continuity and
graph usefulness.

## Integration Plan

### Phase 1A - Schema documentation

Ship this document and keep it as the Product Intent / Claim Boundary anchor for
Material Graph v0.

### Phase 1B - Projection types

Add Go types for `MaterialGraph`, `MaterialNode`, `MaterialEdge`,
`MaterialThread`, `MaterialSalience`, and `ContinuityPack` in a focused package.

Recommended location:

```text
pulse/pulse-app/internal/store/material_graph.go
```

No storage migration is required for projection-only types.

### Phase 1C - Projection builder

Add a read-only builder:

```text
Store.MaterialGraph(threadID string, limit int) (MaterialGraph, error)
```

The builder should read from:

- latest continuity checkpoint;
- active reviewed threads;
- recent memory capsules;
- graph entities/relations/facts/events;
- reviewed import state.

The builder should respect hidden/sensitive actors:

- hidden/ignored entities are excluded from default projection and resume;
- sensitive/private entities are included only if the privacy ceiling allows;
- operator/debug projection may show hidden records only when explicitly
  requested.

It should exclude by default:

- ignored/hidden entities;
- inactive review insights;
- raw observations;
- unreviewed ambiguous preview material.

### Phase 1D - Viewer data extension

Extend `/viewer/data` with:

```json
{
  "material_graph": {
    "focused_thread": {},
    "nodes": [],
    "edges": [],
    "timeline": []
  }
}
```

Keep `next_resume` first. The viewer should still lead with:

1. Pulse keeps the thread.
2. What Pulse will tell Claude next.
3. Active reviewed threads.
4. Focused material map.

### Phase 1E - Resume references

Extend `ResumeSections` or `ResumeBlock` with optional `material_refs`.

Do not replace existing markdown. Add references for evidence and viewer drill
down.

### Phase 1F - Optional semantic delta widening

Only after the projection is tested, consider widening `SemanticNode.Kind` and
the MCP schema to accept the richer v0 node kinds directly.

This must be one coordinated change:

- Go validator;
- MCP JSON schema;
- tests for rejected unsupported kinds;
- compatibility with existing generic kinds;
- claim boundary update.

## Storage Strategy

Recommended v0 storage path:

1. Projection-only first, using existing tables.
2. Add source/status/salience metadata columns only when the projection exposes
   a real missing field that cannot be derived safely.
3. Avoid a parallel graph database.

No external graph substrate in v0. Use the Pulse store as the source of truth.
`graphify-out/graph.json` may be a local benchmark artifact or later reviewed
import source, but never the canonical Pulse Material Graph.

Possible future migration, not Phase 1:

```text
025_material_graph_metadata.sql
```

Potential columns:

- `entities.source_refs_json`
- `entities.review_status`
- `entities.salience_json`
- `relations.source_refs_json`
- `relations.confidence`
- `relations.review_status`
- `facts.source_refs_json`
- `facts.review_status`
- `events.source_refs_json`
- `events.review_status`

Do not add these until projection tests prove the need.

## Tests To Add First

1. `TestMaterialGraphProjectsOwnershipDecision`

Asserts the Atlas/Pulse ownership input becomes nodes, edges, do-not-repeat, and
resume-eligible material refs.

2. `TestMaterialGraphExcludesIgnoredEntities`

Asserts ignored review decisions remove nodes, edges, facts, events, insights,
material refs, and resume mentions.

3. `TestMaterialGraphKeepsInactiveThreadsOutOfResume`

Asserts candidate threads can appear in preview/focused map while staying out of
`pulse_resume` until marked active.

4. `TestMaterialGraphEmotionIsHypothesisUntilConfirmed`

Asserts emotional anchors render with `status: "hypothesis"` unless confirmed.

5. `TestViewerDataIncludesFocusedMaterialGraphAfterResume`

Asserts `/viewer/data` includes `next_resume` and a bounded material graph
projection, with no raw transcript.

## Hard Rejection Criteria

Reject implementation if:

1. Atlas owns Material Graph or People Graph.
2. Heart stores graph data.
3. Nodes/edges lack source refs or explicit hypothesis labels.
4. Ignored entities leak into graph, viewer, Arena output, or resume.
5. Inactive candidate threads appear in resume.
6. Emotion is presented as fact.
7. UI leads with import or giant graph before first memory proof.
8. Raw transcripts are stored by default.
9. Token savings are shown as exact when estimated.
10. User cannot inspect, correct, forget, or wipe.
11. Product copy says production-ready or Claude never forgets.
12. Graph UI becomes a huge unreadable hairball.
13. Implementation depends on Graphify or treats Graphify output as canonical
    Pulse material graph.
14. Hidden/ignored entities appear in default Material Graph projection, viewer
    map, Arena output, or resume.

## Minimal v0 Acceptance

Material Graph v0 is acceptable when:

- canonical Graphify boundary doc exists and package dependency scan passes;
- schema is documented;
- projection types exist;
- messy Atlas/Pulse graph ownership proof passes;
- existing active-only resume tests keep passing unchanged;
- ignored entity leak tests pass;
- viewer exposes a focused material map after `next_resume`;
- wipe still removes host-extracted material;
- install/onboarding flow remains unchanged.
