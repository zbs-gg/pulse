# Pulse Personal: Real Codex Dogfood Recovery

Updated: 2026-07-23

## Objective

Close the first real user loop in Codex:

1. normal project work produces one compact memory candidate through the
   user's Codex subscription;
2. Memory Home shows the candidate before persistence;
3. the user approves it and receives a durable receipt;
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

## Verified starting point

- The local daemon, SQLite store, and BGE-M3 retrieval can operate.
- The isolated packed test can save one visible card and deliver it to a
  synthetic fresh-session lifecycle.
- That test explicitly reports that it is not production-ready.
- The installed live lifecycle is stale and does not capture current Codex
  work reliably.
- Existing project/global boundaries need verification before any new write.
- The public preview trails source and the production release manifest is not
  available through the normal install path.
- Historical ingest and Team work are separate lanes and must remain frozen.

Re-verify these statements before relying on them.

## Implementation method

- Work from the earliest failing boundary in:

  `Codex lifecycle -> extraction -> visible card -> approval -> receipt ->
  new Codex task -> automatic recall`

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
