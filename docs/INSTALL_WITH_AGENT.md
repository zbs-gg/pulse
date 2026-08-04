# Install Pulse With Your AI Agent

The host-neutral flow below becomes a public install path only after the
matching signed preview is published. Before then, use it to audit this branch,
not to claim the current npm preview already contains the feature.

Pulse is installed trust-first: your agent audits the exact package, explains
the local changes, asks for consent, runs one command, and then proves one real
memory across fresh tasks in a verified host. The maintained agent procedure is
[`AGENTS.md`](../AGENTS.md).

## The command

From the Git project that should receive Personal memory:

```bash
npx -y @zbs-gg/pulse@0.7.1 init codex
```

The Personal installer verifies a signed release, provisions one shared Core,
and attaches every detected compatible Claude Code, Cursor, and Codex plugin
to the same project-bound vault. Any one of those hosts is sufficient.
It does not require Go, Python, Make, Docker, a model API key, or manual config
editing. Memory remains in a private project-bound local vault; raw transcript
capture, backend model calls, old-chat import, and Personal-to-Git publication
are off by default.

The intended target set is macOS, Windows, and GNU/Linux on arm64 and x64.
Before presenting the command as supported, the agent must find the exact
target and harness in the [native support ledger](release/NATIVE_SUPPORT_LEDGER.md).
PR fixture evidence alone does not authorize a public support claim.

The wizard may pause for real human actions such as protected-action presence,
Codex hook trust, Claude Code plugin approval, or a Cursor reload. Rerun the
same command or use `pulse repair`; verified Core and host work is reused.

## What the agent must show you

```bash
pulse doctor claude-code  # when detected
pulse doctor cursor       # when detected
pulse doctor codex        # when detected
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

## Repair, disconnect, and data

```bash
pulse repair
pulse disconnect claude-code
pulse disconnect cursor
pulse disconnect codex
```

Disconnect preserves memory. Whole-vault wipe is a separate OS-presence-bound
operation. The agent must not delete `~/.pulse`, another project vault, or any
memory merely because the integration was removed.

## Copyable prompt

```text
Please audit and install Pulse Personal for this project.

1. Read README.md, AGENTS.md, llms.txt, and docs/SECURITY_INSTALL_CHECKLIST.md.
2. Check `npm view @zbs-gg/pulse dist-tags --json`. Host-neutral Personal
   requires 0.7.1 or newer, `pulse.personal_install_plan.v2`, and its
   signed manifest plus exact release assets.
3. Explain every local write, privacy default, repair step, and removal step.
4. Ask me before installing.
5. After I approve, run:
     npx -y @zbs-gg/pulse@0.7.1 init codex
6. For every host returned in `host_status.hosts`, run its exact doctor command:
   `pulse doctor claude-code`, `pulse doctor cursor`, or `pulse doctor codex`.
   Then run `pulse home`.
7. Help me create one visible automatically saved memory, start a fresh task, and show the
   continuity and token-economy receipts in Memory Home.
8. Run `pulse consolidate report`. Show me the destination, source classes,
   totals, blockers, and next action. Do not import, merge, delete, publish, or
   clean up any source.

Do not import old chats, store raw transcripts, print secrets, push Personal
memory to Git, or call fallback/synthetic evidence the product.
```

Unsupported hosts may run `npx -y @zbs-gg/pulse@preview mcp`, but the agent
must call it Safe Mode and say that full state-aware retrieval is not enabled.
