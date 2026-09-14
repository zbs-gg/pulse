# Security Install Checklist

The 0.8.3 installer downloads signed runtime and model assets from the public
GitHub release in `zbs-gg/pulse`. GitHub may redirect downloads to
`release-assets.githubusercontent.com`; signatures, exact sizes and SHA-256
digests are still checked before activation. No paid release storage service
is required. Personal memory remains on the local machine.


Use this checklist before installing stable Pulse Personal 0.7.2 or the
explicit 0.8.3 preview for a user. OpenCode 1.18.x requires the 0.8.3
preview on Apple Silicon macOS.

## Required Checks

- [ ] No backend OpenAI, Anthropic, or Cohere key is required by default.
- [ ] Raw transcript capture is off by default.
- [ ] Emotional marks contain a short event description and metadata, not the
      exact user quote or full conversation.
- [ ] Emotional marks remain in Personal SQLite and cannot be published to
      Pulse Team.
- [ ] Old chat import is not run by default.
- [ ] The install source is real: npm reports `@zbs-gg/pulse@0.7.2` for stable
      or `@zbs-gg/pulse@0.8.3` for the selected preview, or the user explicitly
      provided an exact source bundle, tarball, or local checkout.
- [ ] The signed selected release advertises the exact OS, architecture and
      harness. The Apple Silicon preview has exact-package installation
      evidence; broader Gold claims require the separate native support ledger.
      Fixture evidence is never presented as physical acceptance.
- [ ] Local storage path is shown.
- [ ] The selected AI program's connection files are shown.
- [ ] Local viewer URL is shown.
- [ ] Wipe command is shown:
      `pulse wipe --confirm "wipe pulse memory"`.
- [ ] Disconnect command is shown:
      `pulse disconnect claude-code`.
- [ ] Stop command is shown:
      `pulse stop`.
- [ ] No secrets are printed.
- [ ] Install requires explicit user confirmation.
- [ ] The host advertises only `pulse_memory`; legacy write names are hidden.
- [ ] Prompt recall is temporary, capped at four memories / about 600 tokens,
      and does not persist the user's question.
- [ ] No session-start memory package, Stop interception, automatic
      continuation, or second model pass is installed.
- [ ] For OpenCode, the exact global JSON/JSONC path and plugin-list diff are
      shown before approval; providers, MCP servers, and unrelated plugins are
      preserved.
- [ ] The OpenCode loader is inert outside a signed Pulse project and exposes
      exactly one tool, `pulse_memory`.
- [ ] OpenCode fun facts are off unless `--fun-facts small-model` was selected.
      When enabled, only up to six approved IDs and texts leave Pulse; tools are
      disabled, output is capped at 32 tokens, and the receipt excludes content.

## Expected Install Plan

```bash
pulse init codex --dry-run
pulse init opencode --dry-run
```

The plan should say Pulse will install:

- local Pulse daemon;
- Pulse MCP server;
- connections for every selected compatible AI program;
- local viewer;
- private first memory proof.

The plan should say Pulse will not:

- import old chats;
- store raw transcripts by default;
- publish emotional marks or their causes to another person;
- call backend OpenAI/Anthropic/Cohere model APIs by default;
- print secrets;
- claim production readiness.

## Stop Conditions

Stop and explain if:

- a command asks for model API keys for the default path;
- npm returns 404 for the exact selected Pulse version and no source bundle/local
  checkout was provided;
- a command tries to import archives before the first proof;
- a command offers to send an emotional mark to Team or another user;
- a command prints a real secret;
- a command writes project `.mcp.json` without explicit user approval;
- the user has not confirmed installation.


## Complete moments in 0.8.3

Explicit memory requests preserve all meaningful parts and coexisting feelings.
Pulse validates the full structured set before accepting it locally, then
materializes individual parts with durable receipts. A `stored` result covers
all submitted parts; `pending` or `partial` does not. Canonical storage and
explicit moment reading do not wait for the background search index. Each part
keeps its own pending index receipt until indexing actually finishes. A lost response permits one
bounded replay of the identical operation. A validation refusal before admission
permits correcting the input. Stop does not create an automatic continuation.

A moment reference lets the agent read all currently eligible linked parts with
pagination, independently of the short automatic context budget. Deleted items
are excluded; current corrections and personal/project boundaries still apply.
The write transport envelope is 16 MiB, not a limit on the number of feelings.
Oversized input is explicitly refused before admission, never truncated.
Structured summaries longer than one storage row are split without dropping text.
Historical feelings remain remembered even after their current-state influence
fades; inferred emotions are identified as hypotheses about that moment.

Native Codex, Claude Code, Cursor and OpenCode adapters and the BB adapter share
this behavior. Raw transcripts, secrets, old-chat import and backend model calls
remain off by default. Build/tests are not an installed-runtime or public-release
claim; publication and owner-machine checks must use the exact signed archive.
