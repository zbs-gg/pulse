# Install Pulse With Your AI Agent

Pulse is meant to be installed trust-first: your agent audits before
anything is written. The agent-facing script is [`AGENTS.md`](../AGENTS.md)
— audit → explain → confirm → install the Local Preview → `pulse doctor` →
`pulse demo` (or say plainly that the machine only supports Safe Mode).

## Codex product path

The Codex product path is an installable repository marketplace plugin. It is
separate from the legacy generic MCP fallback. `pulse connect codex` installs
the marketplace plugin and an integrity-checked local runtime under
`~/.pulse/runtime/codex/current`; trusted hooks and MCP use that pinned local
copy and never run `npx` or fetch a moving package at execution time.

The plugin bundles native Codex lifecycle hooks and a collision-resistant
`pulse-product` stdio MCP launcher. It never configures `url` on that stdio
server and it never reads `transcript_path` or `agent_transcript_path`.

Before connecting real work, Pulse must already have a trusted workspace
binding and an owner-controlled product daemon at
`~/.pulse/bin/pulse-product-daemon`. Then run from the bound repository:

```bash
pulse connect codex
```

Start a new Codex task, open `/hooks`, inspect the exact Pulse commands, and
trust them. Codex records trust against the hook definition hash; Pulse does
not bypass that review. In `/mcp`, verify `pulse-product` is active. Automatic
mode is ready only after a trusted hook has actually run, the exact bound
Personal or Desk vault answers an authenticated live check, and doctor reports
full retrieval through local MLX or an explicitly configured Cohere embedder.

To disconnect without deleting memory:

```bash
pulse disconnect codex
```

For complete removal of the executable integration while preserving memory:

```bash
pulse disconnect codex
pulse supervisor stop
codex plugin marketplace remove zbs-gg
rm -rf ~/.pulse/runtime/codex
```

Memory remains in its bound Personal or Desk vault. Delete it only from a
directly attached interactive terminal with the separate exact-confirmation
command `pulse wipe --confirm "wipe pulse memory"`. Agent MCP tools never
expose forget or wipe in Codex product mode, and piped/non-interactive CLI
deletion is rejected.

The plugin injects remembered text only as `pulse.context.v1` inert evidence.
Human-approved practices remain a separate array. A model cannot choose the
Vault, Team, role, audience, visibility, principal, or destination. Private
writes first return a visible pending Memory Tray receipt and commit only after
the grace period unless the user edits or cancels them.

## Copyable prompt

The maintained copy of this prompt lives in the repo root
[README](../README.md#copy-this-message-to-your-ai-agent). Short form:

```text
Hi. Please check whether it is safe to install Pulse:
https://github.com/zbs-gg/pulse

Read README.md, AGENTS.md, llms.txt, and docs/SECURITY_INSTALL_CHECKLIST.md.
Check: npm view @zbs-gg/pulse dist-tags
Explain which harness path fits my setup, what Pulse writes, what it will not
do by default, and how I can erase it. Ask my confirmation before installing.

For Claude Code full local preview:
  npx @zbs-gg/pulse@preview init claude-code
  pulse doctor
  pulse demo

For other MCP-compatible hosts:
  configure the host to run:
  npx -y @zbs-gg/pulse@preview mcp
  and say plainly that this is Safe Mode/fallback, not the full state-aware
  Pulse engine.

No old-chat import without separate confirmation. No raw transcripts. No
secrets in output. Stop and explain if anything looks unsafe.
```

Security checklist for the agent: [`SECURITY_INSTALL_CHECKLIST.md`](SECURITY_INSTALL_CHECKLIST.md).
