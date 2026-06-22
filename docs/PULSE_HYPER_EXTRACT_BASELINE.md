# Pulse × Hyper-Extract — Memory Arena Baseline Plan

**Stage:** internal scaffold / technical preview planning.
**Goal:** Add Hyper-Extract to the Memory Arena as a *real, runnable baseline* for
Pulse's Material Graph construction — to measure how well Pulse extracts "the
material" versus a strong external toolkit, and to keep Pulse honest about where
it actually wins.

**Architecture rule (unchanged):** Hyper-Extract is a **benchmark**, not a
dependency and not a substrate. Pulse keeps its own local-first Material Graph,
Salience Overlay, Continuity Pack, viewer, and trust gates. We do **not** embed
Hyper-Extract inside Pulse runtime. Same doctrine as
`PULSE_APERANT_MEMORY_BENCHMARK_PLAN.md`: *external systems can benchmark Pulse,
they must not own Pulse's runtime memory substrate.*

---

## Source Status (verified 2026-06-22)

Audited from primary sources — the GitHub repo README and Releases page — not a
pasted summary.

| Fact | Value | Source | Label |
|---|---|---|---|
| What it is | "Transform unstructured text into structured knowledge with LLMs. Graphs, hypergraphs, and spatio-temporal extractions — with one command." | repo README | verified |
| Typed outputs | 8 structures: Model, List, Set, Graph, Hypergraph, Temporal Graph, Spatial Graph, Spatio-Temporal Graph | README | verified |
| Extraction algorithms | KG-Gen, GraphRAG, LightRAG, Hyper-RAG, Cog-RAG, "and more" | README | verified |
| Presets | 80+ YAML templates across Finance, Legal, Medical, TCM, Industry, General | README | verified |
| Incremental merge | "Feed new documents anytime to expand and refine your knowledge base" (Incremental Evolution) | README | verified |
| Local inference | "Run Qwen3.5-9B + bge-m3 locally via vLLM. No data leaves your machine." | README | verified |
| Q&A shows retrieval context | `he search` exists; README does **not** explicitly confirm sources are shown in Q&A | README | **unverified — do not assume** |
| License | Apache-2.0 | repo | verified |
| Latest release | **v0.3.0 — 2026-06-19** (Anthropic Claude provider, Obsidian export, **MCP server**, `he clean`) | Releases | verified |
| Prior release | v0.2.0 — 2026-05-18 (unified provider: OpenAI / Bailian / local vLLM) | Releases | verified |
| Stars | ~2.1k | repo | verified (snapshot) |

**Correction to the earlier (Pro) read:** the project moved in the last weeks.
The earlier note had it at v0.2.0 / ~1.5k stars. As of 2026-06-22 it is
**v0.3.0 (2026-06-19), ~2.1k stars**, and v0.3.0 added an **MCP server** plus an
**Anthropic Claude provider** and a `he clean` command. That matters: it now
touches the MCP/agent surface and a wipe-ish command too — not only extraction.
Its momentum and surface overlap are slightly larger than the earlier read implied.

---

## Verdict (own read, not a yes-man echo)

1. **Relevant, yes — but as a meta-baseline, not a single competitor.** Hyper-Extract
   is an extraction *framework* that wraps a family of SOTA methods (KG-Gen,
   GraphRAG, LightRAG, Hyper-RAG, Cog-RAG). One adapter buys us a head-to-head
   against *five named extraction methods at once* on our corpus. Higher leverage
   than treating it as "one rival."

2. **It solves the first half of our pipeline:** material → typed graph
   (incremental merge, indexing, search). That overlaps exactly the
   "what is this material about / how are entities linked" layer of the Material Graph.

3. **It is not a replacement for Pulse.** No evidence of: salience/strategic
   weighting, review/ignore/correction states, active vs inactive threads,
   do-not-repeat, compact resume for the next AI session, cross-chat continuity,
   or source-backed trust/wipe as a first-class promise. Those are Pulse-unique.

4. **Do not embed it.** Use it strictly as a Memory Arena baseline. (Apache-2.0 is
   permissive, but our repo is AGPL and the doctrine is "benchmark, not dependency"
   — so the licence question is moot as long as we only *run it against the same
   corpus*, never link it into Pulse.)

5. **Hard privacy gate.** The corpus is real/redacted personal data. The bench MUST
   use the **local vLLM path only** (Qwen + bge-m3, "no data leaves your machine").
   No OpenAI / Bailian / Anthropic provider for any run on real data. Feasible on
   the Mac arsenal: bge-m3 already runs locally via MLX; Qwen via LM Studio/vLLM.

---

## Memory Arena adapter spec

Plugs Hyper-Extract into the existing bench harness (`scripts/bench_b_*.py`)
as one more **retriever/extractor** alongside Pulse and Mem0. Same corpus in,
same query set, same judge.

```
corpus (redacted chats + docs)
   ├─ Pulse        → Material Graph + Salience + resume   (our system under test)
   ├─ Mem0         → existing baseline
   └─ Hyper-Extract→ he build (local vLLM) → graph/hypergraph → he search   (new baseline)
                     run ≥2 algorithm presets: LightRAG + Hyper-RAG (cheap, strong)
```

Adapter contract (mirror of the existing bench retriever interface):
- `ingest(corpus_dir)` → builds the Hyper-Extract store locally (vLLM endpoint only).
- `query(q)` → returns ranked items **with their evidence/source refs** so the
  judge can compare apples-to-apples on retrieval context.
- `reset()` → wipes the store between corpora (use `he clean` if it does the job).

The adapter is **code that lives on the private bench line** (where the real
corpus and `scripts/bench_b_*.py` live), not on public `main`. This doc is the
public, source-verified plan; the adapter PR is a separate, corpus-touching change.

---

## Eval dimensions — split honestly

Marking which dimensions are a fair head-to-head versus which are Pulse-unique
capability (where Hyper-Extract simply has no equivalent, so "Pulse wins" is a
category statement, **not** a benchmark victory — per our own anti-overclaim rule).

| # | Dimension | Type | Notes |
|---|---|---|---|
| 1 | Entity/relation extraction quality (people/projects/decisions/events) | **head-to-head** | the core fair fight |
| 2 | Temporal links | **head-to-head** | Hyper-Extract has Temporal Graph |
| 3 | Retrieval-context / evidence visibility in answers | **head-to-head** (if HE shows sources) | flag: HE source-display unverified |
| 4 | Ignored-entity leakage (does removed/ignored material resurface?) | **head-to-head** | privacy-relevant for both |
| 5 | Emotional / strategic salience overlay | **Pulse-unique** | HE has no salience model — category claim |
| 6 | Fresh-session compact resume / continuity pack | **Pulse-unique** | HE has no "what the next AI gets" |
| 7 | Source-backed trust + wipe as a first-class promise | **Pulse-unique** | HE has `he clean`; not the same trust contract |

Report must state, per dimension, whether Pulse **won a measured fight** (1–4) or
**offers a capability the baseline does not attempt** (5–7). No blending the two.

---

## Claim Boundary

| Claim | Evidence | Label | Allowed now? |
|---|---|---|---|
| Hyper-Extract is a useful extraction baseline for the Material Graph | source-verified README + our bench plan | candidate | yes |
| Hyper-Extract should be a Pulse dependency / substrate | conflicts with native-graph boundary | forbidden | no |
| Pulse beats Hyper-Extract on extraction | needs a measured Arena run on the same corpus | hypothesis | no (until run) |
| Pulse offers salience/continuity Hyper-Extract does not attempt | feature comparison from sources | capability statement (not a win) | yes, labeled |
| Any run on real data may use a cloud LLM provider | violates privacy gate | forbidden | no |

---

## Non-Goals

- Do **not** add Hyper-Extract to Pulse runtime or link it as a dependency.
- Do **not** change the MCP schema or Pulse storage for this comparison.
- Do **not** run any real-data extraction through a cloud provider.
- Do **not** publish Arena numbers next to install/Safe-Mode copy, or claim
  production parity from a baseline run.
- Do **not** put the real corpus or adapter code on public `main`.

---

## Phased Plan

### Task 1 — This doc (public, source-verified plan) — *done in this PR*
Public planning artifact; no corpus, no code wiring. Mirrors the Aperant plan.

### Task 2 — Local Hyper-Extract smoke (private)
- [ ] Install Hyper-Extract; bring up the **local vLLM** path (Qwen + bge-m3 via MLX/LM Studio).
- [ ] `he build` on a tiny synthetic corpus; confirm "no data leaves machine" (offline check).
- [ ] Record which algorithm presets we run (LightRAG, Hyper-RAG to start).

### Task 3 — Arena adapter (private bench line)
- [ ] Implement the `ingest/query/reset` adapter against `scripts/bench_b_*.py`.
- [ ] Wire Hyper-Extract as a third system next to Pulse and Mem0.

### Task 4 — Same-corpus run + judge
- [ ] Run all three on the identical redacted corpus + query set.
- [ ] Score dims 1–4 head-to-head; document dims 5–7 as capability gaps.

### Task 5 — Honest report
- [ ] One results doc with the dimension split, per-dim verdicts, and labels.
- [ ] Feed wins/losses back into the Material Graph roadmap (loop-first: the run
      is the signal, the roadmap delta is the ingest).

---

## Acceptance Gate

- Privacy gate held: no real-data run touched a cloud provider (verifiable from config).
- Dimension split respected: measured wins (1–4) never blended with capability
  statements (5–7).
- Garden root hygiene clean; no corpus or secret committed to a public branch.
- Trust Reviewer checks any number before it appears in external-facing copy.
