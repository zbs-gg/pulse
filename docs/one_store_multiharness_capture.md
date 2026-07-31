# One bound vault, many harnesses

**Status:** product contract implemented for Codex and Claude Code.

Pulse does not use one global store. The unit of isolation is a signed workspace
binding:

- Personal workspaces resolve only to a Personal vault.
- Team workspaces resolve to a private Desk vault plus a separately governed
  Commons service.
- Codex and Claude Code may share the same Personal or Desk vault only when the
  signed binding resolves them to it.
- Agent-provided paths, store IDs, team IDs, environment overrides, and MCP
  arguments cannot select another vault.

This document describes the automatic local product lifecycle. It supersedes
the old `~/.pulse` global-store, transcript-extractor, `npx`, and
`local-auto` checkpoint design.

## Durable lifecycle

Both harnesses use the same domain contract:

1. Session start requests a bounded continuity pack from the resolved vault.
2. The first user prompt establishes a content-free turn envelope.
3. A model may propose only typed, redacted memory candidates through
   `pulse_remember`.
4. The host creates an exact-argument, single-use lease before the MCP call.
5. The MCP re-resolves the signed binding on every call and finalizes the turn
   through `/turn/finalize`.
6. Every candidate appears in Memory Tray with a receipt before commit.
7. A short grace period permits edit or cancel; unsafe payloads are rejected
   before SQLite or its WAL.
8. A recursive Stop without candidates closes the same ledger as
   `no_change`. It never invents a decision, open loop, state signal, or
   checkpoint.

The stored payload retains its real host provenance. Canonical object identity
does not include host, timestamp, session, or prompt identity, so the same
durable item proposed by Claude Code and Codex converges on one object.

## Claude Code edge contract

Automatic mode requires Claude Code 2.1.196 or newer. Claude Code's native
`prompt_id` is the turn identity for prompt-, tool-, compact-, subagent-, and
Stop-events. Pulse never hashes or stores the prompt and never mints a surrogate
turn ID.

Installed events:

- `SessionStart` with `startup|resume|clear|compact`
- `UserPromptSubmit`
- `PreToolUse`
- `PostToolUse` for the exact `mcp__pulse__pulse_remember` tool
- `PreCompact` and `PostCompact`
- `SubagentStart` and `SubagentStop`
- `Stop`

Every event in one prompt is reduced to one canonical Stop envelope using the
trusted repository root, not the hook's mutable `cwd`. Active background tasks
defer finalization without storing their descriptions, commands, or prompts.
Session crons are future prompts and do not block the current `prompt_id`.

Claude Code registration is one secret-free stdio server:

```text
node <immutable-local-runtime>/src/cli.js claude-mcp
```

The project hook file contains machine-local runtime paths and is kept out of
Git. Updating or disconnecting Pulse removes only recognized Pulse handlers and
preserves unrelated Claude settings and hooks.

## Codex edge contract

Codex uses the bundled `pulse@zbs-gg` plugin and the same installed immutable
runtime. Current Codex stdio MCP does not expose a trusted thread identifier to
the server, so the host hook creates a 30-second exact-argument single-use
lease. The model cannot choose the session, turn, workspace, binding, or
destination.

The Codex and Claude Code edges differ only in native payload parsing and native
hook output. Their Memory Tray ledger, candidate validation, object identity,
continuity resume, authorization, and vault process are shared.

## Daemon and retrieval

One bound Personal/Desk daemon can serve both harnesses. Its status host is
adapter-neutral (`pulse-product`); each write receipt carries the actual
`codex` or `claude-code` provenance.

Backend language-model calls are off by default. Full retrieval requires an
embedder:

- local MLX on Apple Silicon, or
- explicit Cohere embeddings.

Doctor reports the active embedding path. Without an embedder, structured local
memory and inspection remain available, but doctor reports fallback retrieval
and automatic product readiness remains false.

## Install and verify from this checkout

A signed workspace binding and the local embedder must already be provisioned.

```bash
pulse-app/scripts/preview-install-claude-code.sh
pulse doctor claude-code
pulse doctor codex
```

The installer builds the product daemon and MCP package, creates an immutable
runtime snapshot, registers Claude Code, and starts the exact bound vault.
After one real prompt produces a visible Memory Tray receipt and finalizes,
run the product doctor; it will not claim lifecycle readiness before those
workspace-bound milestones exist. The installer does not expose an IPC secret, write a secret into MCP
configuration, import old chats, or enable transcript capture.

## Acceptance evidence

The packed Claude Code E2E installs the npm tarball in a clean HOME and proves:

- signed Personal binding resolution;
- pinned secret-free Claude MCP registration;
- all nine installed native hooks;
- Claude candidate to pending receipt to committed object;
- the packed Codex hook/MCP adapter launched from a nested directory receives
  the Claude-only decision through continuity resume;
- a fresh Claude session receives a distinct Codex-only decision;
- a fresh MCP process retains its custom data and binding authority without
  inheriting installer environment variables;
- doctor rejects tampered MCP authority paths and a poisoned daemon target;
- raw prompt and assistant text are absent from vault files;
- doctor detects daemon outage;
- disconnect removes only Claude MCP/hooks and leaves independently connected
  Codex capture running without deleting the vault.

Run it with:

```bash
cd pulse-app/cli
npm run test:claude-product
```

## Honest limits

- Claude Code versions below 2.1.196 cannot use automatic lifecycle because
  they do not provide native `prompt_id`. Pulse fails closed and asks for an
  update; it never hashes prompt text as a workaround.
- Local and remote Team stores are separate trust domains. Sharing with Commons
  requires the governed Team promotion path; local multi-harness continuity
  never implies Team publication.
- Bulk old-chat import remains a separate preview-first, explicitly confirmed
  operation.
