# Pulse Material Graph Prosha Point Audit

Date: 2026-06-09
Source: Prosha pasted review brief
Verdict reviewed: `accept with fixes`

## Summary

The original Prosha blocker was not "build a big graph UI." It was:

```text
Projection proof.
```

That first proof is now implemented as a native Pulse read-only projection:

- no Graphify runtime dependency;
- no new extractor;
- no new graph storage;
- `Store.MaterialGraph(...)`;
- material refs in `pulse_resume`;
- `material_graph` in `/viewer/data`;
- source refs on projected nodes and edges;
- inactive review insight exclusion;
- hidden entity exclusion.

This does not make the full Material Graph product complete. It closes the
Phase 1B/1C proof and leaves UI, timeline, Arena, correction, and richer
privacy/source modeling as explicit stories.

## Point-by-Point Status

| Prosha point | Current status | Evidence | Story |
|---|---|---|---|
| Graphify boundary is clear. | Done in docs and upheld in runtime slice. | `PULSE_GRAPHIFY_BOUNDARY.md`; code search finds no runtime `graphify` / `graphifyy` dependency outside test text. | MG-00 |
| Native Material Graph model exists. | Done for docs and first runtime projection. | `pulse.material_graph.v0` types and `Store.MaterialGraph(...)` exist in `pulse-app/internal/store/material_graph.go`. | MG-01, MG-02 |
| Ownership boundaries are preserved. | Done for docs and Atlas/Pulse proof. | `TestMaterialGraphProjectsAtlasPulseOwnershipDecision` asserts Atlas, Pulse, People Graph, ownership edge, do-not-repeat edge, and resume-eligible refs. | MG-03 |
| Implementation order is sane: projection before storage/extractor. | Done. | No new migrations or extractor path were added; implementation is a read-only projection over checkpoints and existing graph rows. | MG-02 |
| Fix 1: Minimal Proof Case needs source refs on source-backed nodes/edges. | Done in docs and runtime tests. | `PULSE_MATERIAL_GRAPH_V0.md` Minimal Proof Case includes `source_refs`; store tests assert every node/edge has source refs. | MG-01, MG-03 |
| Fix 2: Define privacy propagation for projected edges. | Done in docs; v0 runtime uses conservative private default. | `PULSE_MATERIAL_GRAPH_V0.md` defines privacy propagation; `MaterialGraph` nodes/edges include `privacy_tier`. Full strictest-tier propagation remains a follow-up because current relation rows do not persist per-edge privacy/source refs. | MG-10 missing |
| Fix 3: Hidden/ignored entities must be excluded by default. | Done for hidden graph entities in runtime; ignored import decisions are still covered by existing preview commit path, not by a dedicated Material Graph E2E test. | `TestMaterialGraphExcludesHiddenEntities`; existing preview builder removes ignored review names before semantic delta creation. | MG-04, MG-19 missing |
| Rename `derived_without_direct_ref`. | Done. | Docs and runtime use `derived_from_reviewed_sources`; tests allow only that or `source_backed`. | MG-01 |
| Material Graph is not first onboarding screen. | Done in docs; UI still needs graph rendering after first proof. | `PULSE_MATERIAL_GRAPH_V0.md` states first memory -> resume proof -> fresh session -> import later; graph appears after proof. | MG-11 missing |
| Existing active-only resume proof must keep passing. | Done. | `go test ./...` passes; `TestMaterialGraphKeepsInactiveReviewInsightsOut` adds Material Graph-specific active insight guard. | MG-04 |
| Docs/planning GO after three fixes. | Done. | The three doc fixes are present in `PULSE_MATERIAL_GRAPH_V0.md`. | MG-00 |
| Implementation Phase 1B/1C GO after fixes. | Done. | Projection types and builder are implemented; store/server tests are green. | MG-01..MG-06 |
| Product/demo claims are not yet allowed. | Still true. | Material Graph exists as API/data projection, but UI rendering, timeline, Arena export, and correction workflow are not complete. | MG-07..MG-15 |
| Next blocker: Projection proof. | Done. | `Store.MaterialGraph(threadID)` returns the required Atlas/Pulse ownership proof and leakage gates pass. | MG-03, MG-04 |

## Missing Stories

These are the stories that were not explicit enough in the first MG-00..MG-09
list or were only partially covered.

| ID | Story | Status | Acceptance |
|---|---|---|---|
| MG-10 | Full privacy propagation in runtime. | Missing | Material Graph computes strictest privacy across endpoint nodes, source objects, source refs, thread privacy, and review decision privacy. Unknown privacy defaults to private and tests prove no downgrade. |
| MG-11 | Viewer Material Graph rendering. | Missing | The viewer renders the existing `/viewer/data.material_graph` as thread map and focused neighborhood after first memory/resume proof, not as first-run hero. |
| MG-12 | Source-ref drilldown. | Missing | A user can inspect why a node/edge exists, which source refs support it, confidence, privacy tier, and whether it enters next resume. |
| MG-13 | Material Graph API/MCP surface. | Missing | A host can request a bounded Material Graph explicitly without scraping `/viewer/data`; output remains source-backed and review-gated. |
| MG-14 | Decide semantic kind widening. | Missing | Product/engineering decision says whether inbound `pulse.semantic_delta.v1` accepts richer kinds or stays generic with rich projection only. Tests lock the decision. |
| MG-15 | CI/runtime guard against Graphify dependency. | Missing | A test or package check fails if Graphify/graphifyy becomes a runtime dependency or install requirement. Docs/test fixtures can still mention it as benchmark. |
| MG-16 | Timeline projection. | Not started | Selected thread shows dated changes in decisions, proofs, blockers, and open loops. This is the expanded version of MG-07. |
| MG-17 | Arena graph comparison export. | Not started | Arena compares entity/decision/open-loop/do-not-repeat extraction, source refs, and confidence, not only recall prose. This is the expanded version of MG-08. |
| MG-18 | Object-level correction workflow. | Not started | Nodes/edges can be confirmed, corrected, ignored, restored; status affects viewer, resume, and Arena output. This is the expanded version of MG-09. |
| MG-19 | Import ignored-entity Material Graph E2E. | Missing | A preview ignored entity goes through commit/import path and is absent from Material Graph nodes, edges, facts, events, review insights, viewer, and resume. |

## Claim Boundary

Allowed now:

```text
Pulse has a native Material Graph v0 projection proof.
```

Still not allowed:

```text
Pulse Material Graph product is complete.
Pulse has a finished graph UI.
Pulse understands every file/person/project automatically.
Pulse replaces Graphify as a full corpus analyzer.
```

The correct current wording is:

```text
Pulse now has a source-backed, review-gated Material Graph projection layer
that connects active continuity to resume/viewer data. The richer UI, timeline,
Arena, correction, and privacy propagation stories remain open.
```
