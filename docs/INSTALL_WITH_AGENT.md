# Install Pulse With Your AI Agent

Pulse is installed trust-first: your agent audits the exact package, explains
the local changes, asks for consent, runs one command, and then proves one real
memory across fresh tasks in a verified host. The maintained agent procedure is
[`AGENTS.md`](../AGENTS.md).

## The command

From the Git project that should receive Personal memory:

```bash
npx -y @zbs-gg/pulse@preview install
```

The Personal installer verifies a signed release, provisions one shared Core,
and attaches every detected compatible Claude Code, Cursor, and Codex plugin
to the same project-bound vault. Any one of those hosts is sufficient.
It does not require Go, Python, Make, Docker, a model API key, or manual config
editing. Memory remains in a private project-bound local vault; raw transcript
capture, backend model calls, old-chat import, and Personal-to-Git publication
are off by default.

The wizard may pause for real human actions such as the macOS presence prompt,
Codex hook trust, Claude Code plugin approval, or a Cursor reload. Rerun the
same command or use `pulse repair`; verified Core and host work is reused.

## What the agent must show you

```bash
pulse doctor <installed-host>
pulse home
```

The only ready verdict is `Pulse <host> automatic lifecycle ready.` Memory Home
must show the pending card before save, the saved receipt afterwards, the
context offered to a fresh task, its acknowledgement state, and an honest
token state: `collecting`, `estimated`, or `measured`.

The proof is one normal memory from real work, then a fresh task in the
same project. A simulated corpus or source-checkout test is not a substitute.

## Repair, disconnect, and data

```bash
pulse repair
pulse disconnect <host>
```

Disconnect preserves memory. Whole-vault wipe is a separate OS-presence-bound
operation. The agent must not delete `~/.pulse`, another project vault, or any
memory merely because the integration was removed.

## Copyable prompt

```text
Please audit and install Pulse Personal for this project.

1. Read README.md, AGENTS.md, llms.txt, and docs/SECURITY_INSTALL_CHECKLIST.md.
2. Check `npm view @zbs-gg/pulse dist-tags --json`. Personal requires preview
   0.7.0 or newer with its signed manifest and exact release assets.
3. Explain every local write, privacy default, repair step, and removal step.
4. Ask me before installing.
5. After I approve, run:
     npx -y @zbs-gg/pulse@preview install
6. Read `pulse doctor <installed-host>` literally and open `pulse home`.
7. Help me save one visible memory, start a fresh task, and show the
   continuity and token-economy receipts in Memory Home.

Do not import old chats, store raw transcripts, print secrets, push Personal
memory to Git, or call fallback/synthetic evidence the product.
```

Unsupported hosts may run `npx -y @zbs-gg/pulse@preview mcp`, but the agent
must call it Safe Mode and say that full state-aware retrieval is not enabled.
