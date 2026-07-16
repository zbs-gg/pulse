# Personal Pulse onboarding for Codex

This is the Stage 1 product path: one person, one project-bound local vault,
Codex, a visible first memory, and continuity into a fresh task.

## Before the command

The supported release target is an Apple Silicon Mac with Node 20+, Codex, and
a Git project. Personal Pulse does not require Go, Python, Make, Docker, or a
model API key. It does not ask the user to edit Codex configuration.

The published `preview` package must contain a canonical signed release
manifest plus the exact notarized daemon, managed local embedding runtime,
data-only model, Codex plugin runtime, and presence helper. If any one is
missing or invalid, installation stops before identity, binding, or Codex
activation changes.

## One command

Run this inside the project:

```bash
npx @zbs-gg/pulse@preview install
```

The first screen says what will be downloaded, how much disk it needs, every
local destination, what stays off, what removal preserves, and which actions
require the person. Consent happens once for that exact plan. `--yes` cannot
approve disclosure, macOS presence, binding replacement, hook trust, downgrade,
or wipe.

The install order is fixed:

1. verify and stage the complete compatible artifact set;
2. install and verify the macOS presence boundary;
3. create or reuse the device-local Personal principal;
4. create or reuse the exact project binding;
5. activate the pinned Codex plugin and runtime;
6. start the project vault and prove managed full retrieval;
7. write a content-free install receipt and open Memory Home.

An interrupted download resumes from verified bytes. A crash or canceled
security prompt does not make partial artifacts current. Run the same command
again or use:

```bash
pulse repair
```

Repair rechecks facts instead of trusting an optimistic journal flag. It keeps
the Personal vault and does not repeat already verified runtime work.

## The first screen after install: Memory Home

```bash
pulse doctor codex
pulse home
```

The ready verdict is `Pulse Codex automatic lifecycle ready.` Memory Home shows:

- Personal readiness and the exact next action when incomplete;
- pending memory cards before their save delay starts;
- recent saved memories and their presentation/write receipts;
- the context offered to a fresh task and whether the host acknowledged it;
- token economy labeled `collecting`, `estimated`, or `measured` with its
  method and coverage, never a fabricated multiplier.

## First-memory proof

1. Do normal work in Codex.
2. Let Codex propose one compact structured memory through Pulse.
3. Read the exact card in Memory Home. Edit, cancel, or save after the visible
   review delay.
4. Start a fresh Codex task in the same project.
5. Confirm that the memory appears automatically with provenance and retrieval
   reasons.
6. Open Memory Home and inspect the saved-memory, context-offer, and host
   acknowledgement receipts. Token evidence may still be `collecting`; that is
   honest until comparable receipts exist.

This is the product proof. `pulse demo` remains an optional isolated explainer
and cannot replace it.

## Privacy and removal

- Personal memory lives in the bound local vault, not Git.
- Raw transcripts and backend model calls are off by default.
- The managed local embedder uses no model API key.
- Old chats are imported only through a separate preview-first flow.
- Git-backed Team Memory receives only separately reviewed and exactly approved
  shared candidates.

Remove Codex integration while preserving memory:

```bash
pulse disconnect codex
```

Whole-vault wipe is separately protected by fresh macOS presence. Disconnect,
repair, and ordinary uninstall never imply permission to delete memory.

## What blocks publication

The deterministic gates run from `npm pack`, with a runtime PATH that exposes
Node, Codex, and Git but no Go or Python. They prove clean install choreography,
interrupted-download resume, repair, packed Git Team modules, and no external
publication. They are intentionally labeled synthetic.

Before publishing the preview, a clean physical Apple Silicon Mac must also
pass the content-free release attestation against production authority, real
signed/notarized artifacts, native Codex hook trust, one saved memory, a fresh
continuity task, Memory Home, and honest token evidence. Without that receipt,
`make release-verify` and `npm publish` fail closed.
