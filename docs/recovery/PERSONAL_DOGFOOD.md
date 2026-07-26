# Pulse Personal: Real Codex Dogfood Recovery

Updated: 2026-07-26

## Objective

Close the first real user loop in Codex:

1. normal project work produces one compact memory candidate through the
   user's Codex subscription;
2. deterministic product code validates and durably saves the structured
   memory without an approval timer or grace-period flow;
3. Memory Home shows the saved memory with edit, move, and delete controls;
4. a newly opened Codex task in the same project automatically receives the
   exact saved memory;
5. Memory Home shows write, delivery, acknowledgement, and honest token
   evidence.

This is the only implementation objective in this branch.

## Acceptance boundary

The proof must run through a real installed Codex lifecycle. A simulated
corpus, fixture embedder, test release manifest, manually authored capsule,
direct hook invocation, or direct MCP invocation may diagnose an internal
boundary, but cannot satisfy acceptance.

No model API billing is allowed. Semantic extraction should use the active
Codex subscription, preferably GPT-5.6 Luna when available. Retrieval
embeddings remain a separate local concern.

## Current real-lifecycle evidence

- Codex initially withheld `SessionStart` and `UserPromptSubmit` because the
  two changed hook definitions required review. The installed plugin UI showed
  the exact warning, and the user-authorized `Trust all` action cleared it.
- In real Codex task `019f9a68-a46a-72b2-a193-f0a4f98ed943`, an ordinary user
  turn automatically created the project memory `UI-726-AMBER`.
- Durable write receipt:
  `receipt_707c0e2520e05c33e0ffca108b5d5a1b`.
- Canonical object:
  `pulse:1785002145653301000:0:c77d071ee361d152`.
- Fresh real Codex task `019f9a6d-2b95-7bf3-aff7-e9f8e866f6a4`
  automatically received that object through lifecycle context and answered
  `UI-726-AMBER` without tools, file access, manual recall, or direct hook/MCP
  execution.
- The append-only delivery ledger contains matching `offered_to_host` and
  `host_observed` receipts for that object in the fresh task.
- Memory Home shows four canonical active records, `Context observed`, and an
  honestly unmeasured token-economy state: latest offer 487 Pulse tokens /
  1948 rendered bytes, `Collecting a comparable baseline`.
- The live daemon, canonical private vault, automatic capture, BGE-M3 full
  retrieval, and the real write/recall path are operating.
- The development plugin/runtime intentionally differs from the signed
  release edge, so product doctor remains action-required and this evidence
  is not a production-release attestation.

Re-verify these statements before relying on them after another activation or
runtime change.

## Implementation method

- Work from the earliest failing boundary in:

  `Codex lifecycle -> extraction -> validated automatic save -> visible card ->
  receipt -> new Codex task -> automatic recall`

- Fix only that boundary, rerun the real path, and proceed to the next one.
- Keep one plan with at most three vertical units.
- Use native repository tools by default.
- Maximum two subagents, each receiving a fresh bounded brief rather than the
  parent conversation.
- Stop and reassess after two failed hypotheses or 15 minutes without new
  executable evidence.
- Do not create another worktree, vault, daemon port, or competing runtime.
- Preserve unrelated dirty state and all existing memory.

## Architecture constraints

- The harness model emits permissive semantic candidates.
- Deterministic product code owns IDs, timestamps, project scope,
  normalization, validation, and receipts.
- Invalid candidate items are quarantined independently. One invalid item
  cannot terminate a batch or worker.
- Personal memory uses one physical local vault with project namespaces.
- Reads default to the current project. Cross-project reads require an
  explicit project policy.
- Team memory is a separate trust domain populated only through explicit
  promotion.

## Out of scope

- historical chat import;
- Team Remote or Git team memory;
- global dashboard redesign;
- additional host parity;
- public publishing, signing-policy redesign, or PR creation;
- destructive cleanup or migration.

## Required evidence at handoff

- exact boundary tested;
- command or user action used;
- observed result;
- files changed;
- receipt/object IDs with no secret material;
- whether a new real Codex task already receives the memory;
- one exact next action if acceptance is not yet green.
