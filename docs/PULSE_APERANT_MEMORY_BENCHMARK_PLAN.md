# Pulse Aperant Memory Benchmark Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the Aperant memory-system analysis into a source-verified Pulse roadmap without copying Aperant's product surface or overbuilding Pulse v0.5.

**Architecture:** Aperant is a benchmark and pattern source, not a dependency or substrate. Pulse keeps its own local-first memory, Material Graph, Salience Overlay, Continuity Pack, viewer, and trust gates. Work starts with a verified comparison map, then splits observer signals, promotion gates, retrieval scoring, viewer trust UX, and process commands into separate approval-ready tasks.

**Tech Stack:** Pulse Go app/storage, SQLite/FTS-style local storage, MCP package, Claude Code agent-first install flow, Garden Board approval gates, local docs/proofs.

---

## Source Status

This plan is based on a pasted external analysis of Aperant, not on a fresh
source audit in this session. Treat all Aperant implementation details as
`needs source verification` before using them as evidence.

## Discovery Brief

### Context Inspected

- `pulse/AGENTS.md`
- `pulse/README.md`
- `pulse/__board/PULSE_MCP_PREVIEW_V0_5_PRD.md`
- `pulse/docs/PULSE_MATERIAL_GRAPH_CURRENT_STATE.md`
- `pulse/docs/PULSE_MATERIAL_GRAPH_V0.md`
- `pulse/docs/PULSE_GRAPHIFY_BOUNDARY.md`
- `pulse/docs/SECURITY_INSTALL_CHECKLIST.md`
- Pasted Aperant analysis supplied by Nik on 2026-06-09

### Candidate Product Framings

| Option | User | Situation | Pain | 5-second understanding | Primary action | First proof | Trust risk | Non-goals |
|---|---|---|---|---|---|---|---|---|
| A | Nik / Pulse architect | Wants to mine Aperant's memory-layer ideas without derailing v0.5 | Lots of compelling patterns, no source-verified mapping to current Pulse | Aperant becomes a benchmark map for Pulse memory v0.6, not a copied architecture | Approve a source-verified gap map | One doc maps Aperant patterns to existing Pulse surfaces and staged Pulse tasks | Borrowed claims may sound proven when they are only pasted analysis | No Graphiti dependency, no Electron surface, no runtime schema change |
| B | Agentic workflow builder | Wants Pulse to dogfood better development loops | Current proof loops are manual and easy to forget after bundles | Pulse gets a small proposal for slash-command loops, gated by Garden Dev OS | Approve a commands proposal, not install commands yet | Proposal lists commands, triggers, owners, and tests | Commands may imply background automation or self-approval | No persistent loop, no global hooks, no runtime prompt edits |
| C | Reviewer / investor-adjacent audience | Wants trust UX and positioning sharpened | Viewer/security docs are strong but can learn from shipped agent-control products | Pulse can show memory health, citations, and install safety more clearly | Approve viewer/security backlog | One backlog links each UX/security idea to proof and stage | UX polish could overclaim production readiness | No Kanban clone, no desktop app, no production claim |

### Recommendation

- Recommended option: A first, then split B and C into separate proposal tasks.
- Why this beats the alternatives: A protects Pulse architecture before new
  features or commands are added. It converts the pasted research into a
  source-verified map that can feed Product Architect and Trust Reviewer.
- Main risk: treating Aperant's reported design as confirmed evidence before a
  fresh source audit.

### Brief Candidate For Approval

- User: Nik / Pulse architect.
- Situation: Nik has a strong external benchmark analysis for Aperant and wants
  to extract what helps Pulse.
- Pain: The useful ideas span memory architecture, dev loops, viewer UX,
  security, and positioning, so implementing directly would blur product,
  technical, and release work.
- One-sentence value: Pulse can learn from Aperant's agentic memory patterns
  while keeping Pulse's local-first emotional/relational continuity focus.
- Primary action: Approve a source-verified Aperant-to-Pulse memory gap map.
- First proof: A doc that maps each Aperant pattern to an existing Pulse
  primitive, a missing Pulse capability, a release-stage label, and a next task.
- Trust boundary: pasted claims are not treated as verified; no external
  dependency is added; no Graphiti/Electron/desktop surface is copied; no
  commands or runtime behavior are installed from this plan.
- Ownership: Pulse owns memory, graph, retrieval, viewer, continuity pack, and
  install trust; Garden Board owns approval/provenance; Memory Arena may later
  compare systems; Atlas and Heart do not own Pulse memory data.
- Release stage: internal scaffold / technical preview planning.
- Non-goals: no implementation of observer engine, no schema migration, no
  command install, no background loops, no public claim, no copied Aperant
  architecture.
- Proof required: source audit, Pulse gap map, claim boundary, Garden root
  hygiene, and Product Architect approval before implementation.

### Approval Ask

Approve Option A as the next Product Architect input, merge A with a small B
commands proposal, park this benchmark, or ask for another discovery pass.

Only Nik can turn this into an Approved Brief.

---

## Claim Boundary

| Claim | Evidence | Label | Allowed now? |
|---|---|---|---|
| Aperant is a useful benchmark for Pulse memory architecture | Pasted analysis plus later source audit | candidate / needs verification | yes |
| Pulse should copy Aperant's full V5 memory plan | no Pulse-specific proof | overreach | no |
| Pulse should add Graphiti as a dependency | conflicts with Pulse native graph boundary | forbidden for v0.5 | no |
| Observer-first memory signals may improve Pulse | Aperant analysis plus current Pulse gaps | hypothesis | yes |
| Promotion gates are important for emotional memory trust | Pulse trust model plus pasted analysis | product principle | yes |
| Viewer health/citation UX should be explored | current viewer goals plus benchmark ideas | backlog candidate | yes |
| Pulse is production-ready because similar products ship | no Pulse proof | false | no |

## Product Principles To Preserve

- First proof before import.
- What Claude gets next before graph.
- Wipe/delete path before advanced memory features.
- Material Graph + Salience Overlay + Continuity Pack stays Pulse's frame.
- Aperant, Graphify, and other systems can benchmark Pulse, but must not own
  Pulse's runtime memory substrate.
- Observer-derived memory must pass trust/promotion gates before entering
  continuity.
- Emotional or relational memory needs stricter evidence labels than coding
  gotchas because false intimacy is more harmful than a wrong code hint.

## Phased Implementation Plan

### Task 1: Source-Verified Aperant Memory Audit

**Files:**
- Create: `pulse/docs/research/APERANT_MEMORY_V5_AUDIT_20260609.md`
- Modify: none
- Test: manual source link/provenance check plus Garden root hygiene

- [ ] **Step 1: Create the research folder**

Run:

```bash
mkdir -p pulse/docs/research
```

Expected: `pulse/docs/research` exists under the Pulse project, not in the
Garden root.

- [ ] **Step 2: Audit primary sources**

Read Aperant primary sources only: README, Memory.md V5 design doc, CLAUDE.md,
security/release docs, and command/worktree docs. Do not rely on secondary
summaries for final claims.

Expected notes to capture:

```markdown
# Aperant Memory V5 Audit 20260609

## Source Status

| Source | URL or local ref | Date accessed | Relevant? | Notes |
|---|---|---:|---|---|

## Verified Patterns

| Pattern | Source evidence | Confidence | Pulse relevance | Do not copy |
|---|---|---|---|---|

## Rejected Or Deferred Patterns

| Pattern | Why not now | Pulse boundary |
|---|---|---|
```

- [ ] **Step 3: Label uncertainty**

Every source claim must be labeled `verified`, `reported`, `hypothesis`, or
`not applicable`.

Expected: no sentence says Aperant "does" something unless the source audit
found it directly.

- [ ] **Step 4: Verify root hygiene**

Run:

```bash
find . -maxdepth 1 \( -name '*.png' -o -name '*.zip' -o -name '*.tar.gz' -o -name '*.tgz' -o -name '*.sha256' -o -name '*.patch' -o -name '*.mp4' -o -name '.DS_Store' -o -name 'artifacts' -o -name 'review-bundles' -o -name 'release-archive' -o -name 'pulse-*' \) -print
```

Expected: no output.

### Task 2: Pulse Gap Map

**Files:**
- Create: `pulse/docs/PULSE_APERANT_MEMORY_GAP_MAP.md`
- Modify: none
- Test: review against `pulse/docs/PULSE_MATERIAL_GRAPH_CURRENT_STATE.md`

- [ ] **Step 1: Map patterns to Pulse primitives**

Create:

```markdown
# Pulse Aperant Memory Gap Map

Date: 2026-06-09
Stage: internal scaffold / technical preview planning

## Rule

Aperant is a benchmark. Pulse keeps its own local-first Material Graph,
Salience Overlay, Continuity Pack, and trust gates.

## Pattern Map

| Aperant pattern | Pulse has today | Pulse gap | Release stage | Next task |
|---|---|---|---|---|
| Observer-first signals | semantic deltas, continuity checkpoints, events | no observer signal taxonomy | internal scaffold | Observer Signals v0 |
| Scratchpad to promotion | memory capsules and reviewed import gates | no shared promotion state for observed signals | internal scaffold | Promotion Gates v0 |
| Hybrid retrieval with graph boost | FTS/retrieval, entities, relations, resume builder | no graph-neighborhood boost spec | technical preview | Retrieval Scoring v0 |
| Contextual embeddings | source refs and project context in memory text | no explicit contextual prefix policy | technical preview | Context Prefix v0 |
| Phase-aware scoring | active threads, review insights, state signals | no phase classifier/scoring table | internal scaffold | Phase Scoring v0 |
| Citation/health UX | viewer data, evidence refs, wipe/hide/restore | no health dashboard/citation chip spec | technical preview | Viewer Trust UX backlog |
```

- [ ] **Step 2: Add hard non-goals**

Include:

```markdown
## Non-Goals

- Do not add Graphiti to Pulse runtime.
- Do not build an Electron desktop control plane.
- Do not install new Claude Code commands from this map.
- Do not change the MCP schema before Product Architect approval.
- Do not claim Aperant parity.
```

- [ ] **Step 3: Review against current state**

Run:

```bash
rg -n "semantic_delta|continuity_checkpoints|BuildResume|ViewerData|WipeMemory" pulse/docs/PULSE_MATERIAL_GRAPH_CURRENT_STATE.md
```

Expected: each mapped Pulse primitive appears in the current-state doc or is
explicitly marked as needing source verification.

### Task 3: Observer Signals v0 Spec

**Files:**
- Create: `pulse/docs/PULSE_OBSERVER_SIGNALS_V0.md`
- Modify: none
- Test: trust review for raw-capture and overclaim boundaries

- [ ] **Step 1: Define a small signal set**

Create only these v0 signals:

```markdown
# Pulse Observer Signals v0

## Stage

Internal scaffold.

## Rule

Observer signals are candidates. They do not enter continuity until promoted.

## v0 Signals

| Signal | Meaning | Evidence | Privacy risk | Promotion gate |
|---|---|---|---|---|
| repeated_search | agent searches the same concept repeatedly | bounded tool metadata | may expose private query wording | redact query, require task context |
| self_correction | agent reverses a prior assumption | bounded summary plus source refs | may imply false lesson | require source-backed correction |
| proof_blocker | test/package/proof fails and changes next action | command result summary | may leak path or secret | redact local paths/secrets |
| co_access | files/docs repeatedly used together | file ids or relative paths | low if relative paths only | require frequency threshold |
| handoff_need | session ends with unresolved next step | continuity checkpoint | may overstate urgency | user or reviewer confirmation |
```

- [ ] **Step 2: Add forbidden captures**

Include:

```markdown
## Forbidden In v0

- No unbounded prompt capture.
- No private message body capture.
- No secrets, local keys, or install tokens.
- No always-on global hook.
- No continuity injection from observer candidates before promotion.
```

### Task 4: Promotion Gates v0 Spec

**Files:**
- Create: `pulse/docs/PULSE_MEMORY_PROMOTION_GATES_V0.md`
- Modify: none
- Test: claim boundary review

- [ ] **Step 1: Define candidate to continuity states**

Create:

```markdown
# Pulse Memory Promotion Gates v0

## States

| State | Meaning | Can enter resume? |
|---|---|---|
| observed_candidate | captured as bounded signal | no |
| source_backed | evidence refs exist | no by default |
| reviewed | passed trust review | yes if relevant |
| user_confirmed | user explicitly accepted | yes |
| corrected | corrected after feedback | yes with correction note |
| ignored | user/system excluded it | no |
```

- [ ] **Step 2: Define promotion checklist**

Include:

```markdown
## Promotion Checklist

- [ ] Source refs exist.
- [ ] Privacy tier is assigned.
- [ ] Confidence is assigned.
- [ ] Claim is not raw capture.
- [ ] Claim is not inferred intimacy without user confirmation.
- [ ] Delete/wipe path applies.
- [ ] Resume wording is user-job language, not pipeline language.
```

### Task 5: Retrieval Scoring v0 Note

**Files:**
- Create: `pulse/docs/PULSE_RETRIEVAL_SCORING_V0.md`
- Modify: none
- Test: no implementation until benchmark acceptance

- [ ] **Step 1: Define scoring as a spec only**

Create:

```markdown
# Pulse Retrieval Scoring v0

## Rule

This is a scoring proposal, not runtime behavior.

## Candidate Inputs

| Input | Why it matters | Risk |
|---|---|---|
| text_match | finds direct memory text | keyword overfit |
| recency | keeps recent work available | recency bias |
| graph_neighborhood | boosts nearby material objects | graph noise |
| salience_trust | protects proof/decision boundaries | overweights scary items |
| salience_emotional | preserves relational continuity | false intimacy |
| phase | weights define/implement/validate/reflect differently | wrong phase guess |
```

- [ ] **Step 2: Add hard labels**

Include: `estimated`, `hypothesis`, and `source_backed` labels for any score
shown in UI or docs.

### Task 6: Process Commands Proposal

**Files:**
- Create: `pulse/docs/PULSE_AGENT_LOOP_COMMANDS_PROPOSAL.md`
- Modify: none
- Test: Garden Dev OS discovery/product gate before installing commands

- [ ] **Step 1: Propose commands without installing them**

Create:

```markdown
# Pulse Agent Loop Commands Proposal

## Stage

Internal proposal. No commands are installed by this file.

## Candidate Commands

| Command | Use when | Must not do | Proof |
|---|---|---|---|
| /first-experience-tighten | onboarding proof regresses | add new features | install/viewer/wipe proof |
| /affective-parity-step | evaluating emotional continuity quality | claim emotion understanding | before/after recall evidence |
| /recipient-verify | sharing a preview | skip clean recipient path | fresh-agent proof |
| /update-dev-memory | repeated proof lesson appears twice | edit runtime prompts silently | Board proposal or reviewed memory note |
```

- [ ] **Step 2: Route to Garden gates**

State that each command requires Product Architect or Release Manager before it
becomes active runtime material.

### Task 7: Viewer And Security Backlog

**Files:**
- Create: `pulse/docs/PULSE_VIEWER_TRUST_UX_BACKLOG.md`
- Modify: `pulse/docs/SECURITY_INSTALL_CHECKLIST.md` only after approval
- Test: trust review for UI claims

- [ ] **Step 1: Create backlog**

Create:

```markdown
# Pulse Viewer Trust UX Backlog

## Candidate Improvements

| Idea | User value | Backing data required | Stage |
|---|---|---|---|
| Memory health panel | see whether Pulse is safe to trust | daemon status, storage status, last proof | technical preview |
| Citation chips | inspect why memory appears | source refs per resume item | technical preview |
| Session-end review | accept/edit/discard observed candidates | promotion states | internal scaffold |
| Teach Pulse entry | explicit user correction | correction row and undo path | technical preview |
| Security install proof row | see install source and wipe path | install plan, doctor, wipe command | technical preview |
```

- [ ] **Step 2: Keep claims narrow**

Each UX idea must say whether it is `real`, `planned`, `preview`, or
`hypothesis`.

## Acceptance Gate For This Plan

- Product Architect approves the A-first framing.
- Aperant source audit exists before any doc claims are upgraded from
  `reported` to `verified`.
- Trust Reviewer checks any viewer/security copy before sharing.
- Release Manager checks any bundle before external handoff.
- Garden root hygiene returns no forbidden artifacts.

