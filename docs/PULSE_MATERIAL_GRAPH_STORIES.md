# Pulse Material Graph Stories

Date: 2026-06-09
Status: implementation tracker

## Boundary

Pulse Material Graph is a native Pulse feature.

It must not call Graphify, depend on Graphify, import Graphify output by
default, or ask users to install Graphify. Graphify can remain an external
benchmark only.

Canonical product formula:

```text
Pulse = Material Graph + Salience Overlay + Continuity Pack
```

## Story Status

| ID | Story | Status | Acceptance |
|---|---|---|---|
| MG-00 | Story map exists before feature work. | Done | Each v0 story has owner surface, acceptance, and gap status. |
| MG-01 | Define native runtime projection types. | Done | Store exposes `pulse.material_graph.v0` nodes, edges, source refs, confidence, privacy tier, status, salience, and resume eligibility. |
| MG-02 | Build `Store.MaterialGraph`. | Done | Given an active thread, Pulse projects a bounded focused graph from continuity checkpoints and reviewed graph rows. |
| MG-03 | Prove the Atlas/Pulse ownership case. | Done | Messy input equivalent to "Atlas must not own the graph; Pulse owns it" becomes Atlas, Pulse, People Graph, decision, ownership edge, do-not-repeat, and resume entry. |
| MG-04 | Preserve review gates. | Done for v0 projection | Existing resume gates filter inactive review insights; Material Graph applies the same rule and does not leak hidden entities. Import ignored-entity persistence remains covered by the existing preview commit path. |
| MG-05 | Add material references to resume metadata. | Done | `pulse_resume` includes material node refs for active decisions, open loops, do-not-repeat warnings, active threads, and evidence-backed state. |
| MG-06 | Expose focused Material Graph in viewer data. | Done for API data | `/viewer/data` includes a bounded `material_graph` object next to `next_resume`, with source refs and confidence. Visual rendering remains a separate UI task. |
| MG-07 | Add timeline projection. | Not started | For a selected thread, viewer can show dated changes in decisions, proofs, blockers, and open loops. |
| MG-08 | Add Memory Arena graph comparison. | Not started | Arena can compare systems by extracted entities, decisions, open loops, do-not-repeat warnings, and source refs, not only recall text. |
| MG-09 | Add object-level correction workflow. | Not started | A user can mark a graph object or edge as corrected, ignored, restored, or confirmed, and that status affects resume and viewer output. |
| MG-10 | Implement full privacy propagation. | Missing | Runtime computes strictest privacy across endpoints, source objects, source refs, thread privacy, and review decisions; unknown privacy defaults to private. |
| MG-11 | Render Material Graph in viewer UI. | Missing | Viewer shows thread map and focused neighborhood after first memory/resume proof, not as the first onboarding screen. |
| MG-12 | Add source-ref drilldown. | Missing | A user can inspect why a node/edge exists, source refs, confidence, privacy tier, and whether it enters next resume. |
| MG-13 | Add explicit Material Graph API/MCP surface. | Missing | A host can request a bounded material graph without scraping `/viewer/data`. |
| MG-14 | Decide semantic node kind widening. | Missing | Decide whether `pulse.semantic_delta.v1` accepts richer material kinds or keeps generic kinds with richer projection only. |
| MG-15 | Add runtime/package guard against Graphify dependency. | Missing | A test or package check fails if Graphify/graphifyy becomes a runtime dependency or install requirement. |
| MG-16 | Expand timeline projection. | Not started | Selected thread shows dated changes in decisions, proofs, blockers, and open loops. |
| MG-17 | Add Arena graph comparison export. | Not started | Arena compares entity/decision/open-loop/do-not-repeat extraction, source refs, confidence, and recall prose. |
| MG-18 | Add persisted object correction workflow. | Not started | Nodes/edges can be confirmed, corrected, ignored, restored; status affects viewer, resume, and Arena output. |
| MG-19 | Add ignored-import Material Graph E2E test. | Missing | A preview ignored entity goes through commit/import and is absent from Material Graph, viewer, and resume. |
| MG-20 | Add assertion assimilation from semantic deltas. | Done for hardened runtime slice | Structured `facts[]` in `pulse.semantic_delta.v1` write first-class bitemporal assertions with strict non-personal scopes, exact confidence, per-fact source event ids, and supersession. Wiki remains a projection, not a store. |

## This Implementation Slice

MG-01 through MG-06 are the first runnable v0.

The slice is intentionally small:

- no Graphify dependency;
- no new broad extraction engine;
- no PDF, image, or video parsing;
- no 3D graph;
- no Atlas ownership;
- no giant hairball UI.

The first runtime proof is a focused map for active thread continuity and the
current reviewed graph store.

Implemented in this slice:

- native `MaterialGraph` projection in the Pulse Go store;
- Atlas/Pulse ownership proof test;
- inactive review-insight leakage test;
- hidden entity leakage test;
- material refs in `pulse_resume` metadata;
- `material_graph` in `/viewer/data`;
- full `go test ./...` verification for `pulse-app`.

## Remaining After This Slice

MG-07 through MG-19 remain separate stories because they require UI,
evaluation, correction workflow, API, privacy propagation, package guard, and
import-E2E decisions beyond the first safe runtime projection.

Additional follow-up:

- render the API-level `material_graph` in the viewer UI as thread map /
  focused neighborhood;
- add source-ref drilldown affordances;
- decide whether `SemanticNode.Kind` should be widened or kept generic with
  projection-only richer kinds.
- surface assertion-backed claims in Material Graph and `/context/query`, then
  add a human wiki export as a projection over assertions + graph.

See `docs/PULSE_MATERIAL_GRAPH_PROSHA_POINT_AUDIT.md` for the point-by-point
mapping from Prosha's review to completed, partial, and missing stories.
