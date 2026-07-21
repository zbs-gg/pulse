# Personal Pulse onboarding

Publication status: this host-neutral flow is branch-ready but is not a claim
about the current npm `preview` tag until the matching signed artifacts and
physical clean-machine attestation are published.

This is the Stage 1 product path: one person, one project-bound local vault,
at least one supported harness, a visible first memory, and continuity into a fresh task.

## Before the command

The supported release target is an Apple Silicon Mac with Node 20+, a Git
project, and at least one of Claude Code, Cursor, or Codex. Personal Pulse
does not require Go, Python, Make, Docker, or a model API key. It also requires no
manual host config editing.

The published `preview` package must contain a canonical signed release
manifest plus the exact notarized daemon, managed local embedding runtime,
data-only model, native plugin runtime, and presence helper. If any one is
missing or invalid, installation stops before identity, binding, or host
activation changes.

## One command

Run this inside the project:

```bash
npx -y @zbs-gg/pulse@preview install
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
5. start and verify one host-neutral Core and project vault;
6. attach every detected compatible harness through its native plugin;
7. prove managed full retrieval, write a content-free host matrix receipt,
   and open Memory Home.

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
pulse doctor claude-code  # when detected
pulse doctor cursor       # when detected
pulse doctor codex        # when detected
pulse home
```

The ready verdict is `Pulse <host> automatic lifecycle ready.` Memory Home shows:

- Personal readiness and the exact next action when incomplete;
- pending memory cards before their save delay starts;
- recent saved memories and their presentation/write receipts;
- the context offered to a fresh task and whether the host acknowledged it;
- token economy labeled `collecting`, `estimated`, or `measured` with its
  method and coverage, never a fabricated multiplier.

### Map memory already on this computer

Run one read-only report from any bound project:

```bash
pulse consolidate report
```

The same report appears in the terminal, Memory Home's **Memory ocean** section,
and the Pulse tool available to Claude Code, Cursor, and Codex. It identifies
the bound destination first, then shows content-free source aliases,
classifications, counts, blockers, and one next action. It does not import,
merge, delete, clean up, publish, or call a model. Paths and memory bodies stay
out of the portable report. Any later import is a separate preview and a
separate human approval.

## First-memory proof

1. Do normal work in a verified Claude Code, Cursor, or Codex task.
2. Let that harness propose one compact structured memory through Pulse.
3. Read the exact card in Memory Home. Edit, cancel, or save after the visible
   review delay.
4. Start a fresh task in the same project, optionally in another verified harness.
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
- `pulse consolidate report` inventories old stores without adopting them.
- Git-backed Team Memory receives only separately reviewed and exactly approved
  shared candidates.

Remove one host integration while preserving memory:

```bash
pulse disconnect claude-code
pulse disconnect cursor
pulse disconnect codex
```

Whole-vault wipe is separately protected by fresh macOS presence. Disconnect,
repair, and ordinary uninstall never imply permission to delete memory.

## What blocks publication

The deterministic gates run from `npm pack` and cover host-neutral orchestration,
interrupted-download resume, repair, packed Git Team modules, and no external
publication. They expose no Go or Python and are intentionally labeled
synthetic; they are not physical product-install or cross-host recall proof.

Before publishing the preview, a clean physical Apple Silicon Mac must also
pass the content-free release attestation against production authority, real
signed/notarized artifacts, native host lifecycle trust, one saved memory, a fresh
continuity task, Memory Home, and honest token evidence. Without that receipt,
`make release-verify` and `npm publish` fail closed.
