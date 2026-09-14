# Install Pulse With Your AI Agent

The 0.8.3 installer downloads signed runtime and model assets from the public
GitHub release in `zbs-gg/pulse`. GitHub may redirect downloads to
`release-assets.githubusercontent.com`; signatures, exact sizes and SHA-256
digests are still checked before activation. No paid release storage service
is required. Personal memory remains on the local machine.

The legacy Google Cloud release bucket was retired on 2026-09-14. Fresh
installs of 0.7.2 and older previews that use that bucket are unavailable.
Existing local installations and vaults are preserved. npm `latest` still
points to 0.7.2; select `@zbs-gg/pulse@0.8.3` explicitly for a new install.
Version 0.8.3 remains an opt-in preview, not a stable promotion.

> Personal 0.8.3 is an opt-in Apple Silicon preview. The historical stable release is 0.7.2.
> The preview includes complete-moment memory and OpenCode 1.18.x support.
> Verify the exact selected version and signed artifact set before installation.

The publication workflow installs the exact archive on a clean Apple Silicon
runner and requires a real BGE-M3 semantic query. A successful workflow does
not replace owner-machine host checks or one-day acceptance.

Pulse is installed trust-first: your agent audits the exact package, explains
the local changes, asks for consent, runs one command, and then proves one real
memory across fresh tasks in a verified host. The maintained agent procedure is
[`AGENTS.md`](../AGENTS.md).

## The command

To select the preview explicitly, run from the Git project that should
receive Personal memory:

```bash
npx -y @zbs-gg/pulse@0.8.3 init codex
```

The Personal installer verifies a signed release, provisions one shared Core,
and attaches every detected compatible Claude Code, Cursor, Codex, and
OpenCode plugin to the same project-bound vault. Any one of
those hosts is sufficient within its documented release target.
It does not require Go, Python, Make, Docker, a model API key, or manual config
editing. Memory remains in a private project-bound local vault; raw transcript
capture, backend model calls, old-chat import, and Personal-to-Git publication
are off by default.

The intended target set is macOS, Windows, and GNU/Linux on arm64 and x64.
The selected Apple Silicon preview must advertise the host in its signed
release policy and have exact-package installation evidence. The separate
[native support ledger](release/NATIVE_SUPPORT_LEDGER.md) governs broader Gold
platform claims. PR fixture evidence alone does not authorize those claims.

The wizard may pause for real human actions such as protected-action presence,
Codex hook trust, Claude Code plugin approval, or a Cursor reload. Rerun the
same command or use `pulse repair`; verified Core and host work is reused.

For OpenCode, the plan must show the exact existing `opencode.json` or
`opencode.jsonc`, the plugin-list diff, `~/.config/opencode/pulse/pulse.js`, and
the project option file before consent. Existing providers, MCP servers, and
plugins remain. A conflicting pair of JSON and JSONC configs stops installation
without mutation.

## What the agent must show you

```bash
pulse doctor claude-code  # when detected
pulse doctor cursor       # when detected
pulse doctor codex        # when detected
pulse doctor opencode     # 0.8.3, when detected
pulse home
```

The only ready verdict is `Pulse <host> automatic lifecycle ready.` Memory Home
must show the automatically saved memory and write receipt, the context offered
to a fresh task, its acknowledgement state, and an honest token state:
`collecting`, `estimated`, or `measured`.

To map existing local memory without importing it, run:

```bash
pulse consolidate report
```

The agent must show the same content-free report in the terminal and Memory
Home, and state explicitly that the command did not import, merge, delete,
publish, or clean up anything. A partial or active-conflict result is a blocker,
not permission to repair or remove another store.

The proof is one normal memory from real work, then a fresh task in the
same project. A simulated corpus or source-checkout test is not a substitute.

For the 0.8 preview behavior, the host should advertise only
`pulse_memory`. Prompt-time recall must stay within four memories and about 600
tokens, while weak matches add nothing. The install must not add a session-start
memory package, a Stop hook, or a second model turn for saving memory.

OpenCode must expose exactly one Pulse tool, `pulse_memory`, and keep its global
loader inert outside signed Pulse projects. Optional fun facts require the
visible `--fun-facts small-model` flag. The service request receives only up to
six approved candidate IDs and texts, disables tools, is capped at 32 output
tokens, and must not receive the user prompt or the rest of memory.

## Repair, disconnect, and data

```bash
pulse repair
pulse disconnect claude-code
pulse disconnect cursor
pulse disconnect codex
pulse disconnect opencode
```

Disconnect preserves memory. Whole-vault wipe is a separate OS-presence-bound
operation. The agent must not delete `~/.pulse`, another project vault, or any
memory merely because the integration was removed.

## Copyable prompt

```text
Please audit and install Pulse Personal for this project.

1. Read README.md, AGENTS.md, llms.txt, and docs/SECURITY_INSTALL_CHECKLIST.md.
2. Check `npm view @zbs-gg/pulse dist-tags --json`. Host-neutral Personal
   requires the explicitly selected published 0.8.3 preview,
   `pulse.personal_install_plan.v2`, and its signed manifest plus exact
   release assets. Do not use `latest`: its legacy installer is retired.
3. Explain every local write, privacy default, repair step, and removal step.
4. Ask me before installing.
5. After I approve, run:
     npx -y @zbs-gg/pulse@0.8.3 init codex
6. For every host returned in `host_status.hosts`, run its exact doctor command:
   `pulse doctor claude-code`, `pulse doctor cursor`, `pulse doctor codex`, or
   `pulse doctor opencode`.
   Then run `pulse home`.
7. Help me create one visible automatically saved memory, start a fresh task, and show the
   continuity and token-economy receipts in Memory Home.
8. Run `pulse consolidate report`. Show me the destination, source classes,
   totals, blockers, and next action. Do not import, merge, delete, publish, or
   clean up any source.

Do not import old chats, store raw transcripts, print secrets, push Personal
memory to Git, or call fallback/synthetic evidence the product.
```

Do not install Pulse on an unsupported operating system or architecture. Wait
for a separately proven native release instead of presenting a fallback as the
product.


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
