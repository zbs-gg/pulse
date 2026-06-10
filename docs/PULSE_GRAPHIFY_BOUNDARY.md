# Pulse Graphify Boundary

Date: 2026-06-09
Status: canonical boundary for Pulse v0.5 planning
Scope: Pulse Material Graph, install/onboarding, runtime dependencies, and
review/eval artifacts.

## Direct Verdict

Do not build Pulse on top of Graphify.

Pulse must build and own its own native Material Graph using existing Pulse
primitives:

- `pulse.semantic_delta.v1`
- `entities`
- `relations`
- `facts`
- `events` and `event_entities`
- `continuity_checkpoints`
- reviewed import JSON
- active threads
- `pulse_resume`
- `/viewer/data`

The allowed pipeline is:

```text
Pulse semantic_delta / entities / facts / events / continuity
-> Material Graph projection
-> resume / viewer / Arena
```

The forbidden pipeline is:

```text
Pulse -> Graphify -> graph.json -> resume
```

## Allowed Use

Graphify may be used only as:

- an external benchmark;
- a UI/report reference;
- an optional future reviewed import source;
- a Memory Arena comparison target;
- a local eval artifact under `pulse/artifacts`.

If Graphify output is ever imported, it must enter Pulse as a normal reviewed
source, with source refs, privacy tier, confidence, and review status. It must
not silently become Pulse memory.

## Forbidden Use

Graphify must not be:

- a runtime dependency;
- a package dependency;
- the canonical graph store;
- the extraction engine;
- an onboarding requirement;
- a storage layer;
- a hidden substrate behind `pulse_graph_delta`;
- the reason `pulse_resume` can work;
- treated as source-backed evidence without review.

Forbidden implementation shapes:

- `graphifyy` in Pulse install scripts or package manifests;
- runtime imports or subprocess calls to Graphify from Pulse;
- using `graphify-out/graph.json` as the Pulse Material Graph;
- making Pulse a wrapper around Graphify;
- requiring users to install Graphify before Pulse can preserve continuity;
- building Material Graph v0 by copying Graphify output into Pulse tables.

## Product Boundary

Graphify maps a corpus.

Pulse maps the user's living work context and turns reviewed, source-backed
material into continuity.

Pulse v0.5 is:

```text
Material Graph + Salience Overlay + Continuity Pack
```

Where:

- Material Graph = what exists and how it connects.
- Salience Overlay = why it matters emotionally, strategically, or trust-wise.
- Continuity Pack = what the next AI session receives.

The product is not a graph of files. The product is what this work/life thread
is about, why it mattered, and what the next Claude/Codex session must not
lose.

## Native Implementation Rule

Do not start with a new extraction engine.

Start by formalizing a read/projection layer over existing Pulse data:

```text
existing Pulse data
-> pulse.material_graph.v0 projection
-> focused viewer map
-> resume evidence linkage
-> Memory Arena output
```

The first implementation should be read-only projection over existing storage
unless tests prove a missing field cannot be derived safely.

## Ownership Boundary

Pulse owns:

- Material Graph;
- source-backed graph evidence;
- reviewed graph writes;
- continuity packs;
- resume blocks;
- corrections;
- source refs.

Heart / Elle owns companion voice and emotional narrative. It may use the graph
to speak humanly, but must not store Material Graph data.

Atlas owns triggers, routing, evaluator behavior, delivery, and policy. It may
consume Pulse context, but must not own Material Graph or People Graph.

Garden UI owns visible inspection, correction, delete/wipe, and focused
graph/timeline viewer.

Memory Arena owns comparison. It must not contaminate Pulse core.

Hard rules:

- Atlas must not own Material Graph.
- Graphify must not own Material Graph.
- Heart must not store Material Graph.

## Review And Source Rules

Every Material Graph node/edge must have source refs or be explicitly labeled
`hypothesis`.

Ignored entities must not appear in:

- graph nodes;
- graph edges;
- facts;
- insights;
- focused maps;
- Arena output;
- `pulse_resume`.

Inactive threads may appear in preview/focused map, but must not appear in
`pulse_resume`.

Emotional/state salience is an overlay on material objects. It must not be
stored as free-floating trivia, and it must not be presented as fact unless
user-confirmed.

Raw transcript capture remains off by default.

## Acceptance Check

Phase 0 is acceptable only when:

1. This file exists.
2. Material Graph docs link back to this file as the canonical boundary.
3. Current Pulse package manifests do not include `graphify` or `graphifyy`.
4. Runtime Pulse code does not import Graphify, shell out to Graphify, or read
   `graphify-out/graph.json` as canonical graph input.
5. Any Graphify eval artifacts remain under `pulse/artifacts` and are labeled
   benchmark/eval only.

Useful verification commands:

```sh
rg -n -i "graphify|graphifyy" \
  pulse/package.json pulse/package-lock.json \
  pulse/mcp/package.json pulse/mcp/package-lock.json \
  pulse/pulse-app/package.json pulse/pulse-app/cli/package.json \
  pulse/pulse-app/cloudflare/package.json

rg -n -i "graphify|graphifyy|graphify-out" \
  pulse/pulse-app pulse/mcp \
  -g '!**/node_modules/**' \
  -g '!**/artifacts/**' \
  -g '!**/review-bundles/**'
```

Expected result: no runtime/package dependency hits. Documentation and
`pulse/artifacts` benchmark mentions are allowed.

## Hard Rejection Criteria

Reject implementation if:

1. Graphify becomes a runtime dependency.
2. Graphify becomes a package dependency.
3. Graphify output becomes canonical Pulse graph.
4. Pulse onboarding asks the user to install Graphify.
5. `pulse_graph_delta` delegates to Graphify.
6. `pulse_resume` depends on Graphify output.
7. `graphify-out/graph.json` is read as canonical source.
8. Atlas owns Material Graph or People Graph.
9. Heart stores Material Graph.
10. Nodes/edges lack source refs or hypothesis labels.
11. Ignored entities leak after review.
12. Inactive threads appear in resume.
13. Emotions are presented as facts.
14. UI leads with a giant graph before first memory/resume.
15. Raw transcript is stored by default.
16. Product copy says production-ready or "Claude never forgets."
