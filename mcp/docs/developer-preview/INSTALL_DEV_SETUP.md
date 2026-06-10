# Pulse MCP Preview v0.4.2 Install

This is the technical install reference for the partner preview.

It is technical. It is not the final consumer install.

## Fastest Path: Zero-Config Standalone (pulse-mcp v0.4.0+)

```bash
claude mcp add pulse -- npx -y @zbs-gg/pulse-mcp@preview
```

No Go toolchain, no daemon, no keys. The MCP server creates a standalone lite
store at `~/.pulse/standalone/store.json` on the first tool call. Memory,
recall, resume, graph deltas, status, forget, and wipe all work. The viewer,
lifecycle hooks, and the full retrieval engine require the full path below;
when a local daemon is later installed, the same MCP server prefers it
automatically (`PULSE_MCP_MODE=auto` is the default).

## Recommended Path: Install With Your Agent

The primary path is trust-first:

1. Copy the prompt from
   [`../../../docs/INSTALL_WITH_AGENT.md`](../../../docs/INSTALL_WITH_AGENT.md)
   into Claude Code, Codex, or Cursor.
2. Let the agent inspect `README.md`, `AGENTS.md`, and the security checklist.
3. The agent shows `pulse install-plan claude-code --json`.
4. The agent runs `pulse init claude-code --dry-run`.
5. The agent asks for confirmation.
6. After confirmation, the agent runs `pulse init claude-code --yes`.
7. The agent runs `pulse doctor`, `pulse demo`, and `pulse viewer`.

## Requirements

- macOS or Linux shell.
- Go toolchain.
- Node.js 18+ and npm.
- Claude Code CLI available as `claude`.
- Local checkout or preview bundle containing `pulse/pulse-app` and
  `pulse/mcp`.

No model API keys are required for the default path:

```text
OPENAI_API_KEY unset
ANTHROPIC_API_KEY unset
COHERE_API_KEY unset
```

## Manual Preview Command

After `@zbs-gg/pulse` is published with the preview tag, use:

```bash
npx @zbs-gg/pulse@preview init claude-code
```

Verify publication first:

```bash
npm view @zbs-gg/pulse dist-tags
```

If npm returns 404, do not use the `npx` path. Use the source-bundle fallback
or packed tarball and say that the public npm path is not ready yet.

Run it from the project you want Claude Code to use with Pulse.

The installer configures Claude Code MCP + hooks in the directory you run it
from.

Source-bundle fallback for reviewers:

```bash
cd pulse/pulse-app
./scripts/preview-install-claude-code.sh
```

This uses `pulse/pulse-app` as the Claude Code project for the first proof. Run
Claude Code from that same directory when testing the quick proof.

Real project proof for the source-bundle fallback:

```bash
cd /path/to/your/project
/path/to/pulse-mcp-preview-v0.4.2-source-20260608/pulse/pulse-app/scripts/preview-install-claude-code.sh
```

Use this form when you want Pulse connected to your actual project. If you run
the installer from the preview bundle directory and then open Claude Code in a
different project, that other project will not have the preview hooks/config.

The installer does the preview setup:

1. Verifies `go`, `node`, `npm`, and `claude`. The source-bundle shell fallback also checks `curl`.
2. Builds a local Pulse daemon into `~/.pulse/bin`.
3. Builds the local `@zbs-gg/pulse-mcp` package from bundled preview source.
4. Starts Pulse on `127.0.0.1:18789`.
5. Configures Claude Code MCP in the directory where you ran the installer.
6. Installs Claude Code lifecycle hooks in that same directory.
7. Saves the first install memory when the daemon is reachable.
8. Prints the local dashboard URL.
9. Lets you run `pulse doctor` to check Node, Go, Claude Code, daemon, MCP,
   hooks, viewer, and the trust boundary.

It refuses to silently write a project `.mcp.json` fallback containing
`PULSE_API_KEY`. If Claude Code CLI is missing, install Claude Code first.

Agent dry run:

```bash
npx @zbs-gg/pulse@preview init claude-code --dry-run
```

Confirmed agent install:

```bash
npx @zbs-gg/pulse@preview init claude-code --yes
```

## First Proof

After install, ask Claude Code:

```text
Remember this in Pulse: Atlas must not own the People Graph; Pulse owns portable continuity memory.
```

Then start a fresh Claude Code session and ask:

```text
What did we decide about Atlas and the People Graph?
```

Do not repeat the decision in Session B. The point is to prove resume/recall.

Before the proof, run:

```bash
pulse doctor
```

If a reviewer only wants the script for the proof ritual, run:

```bash
pulse demo
```

## Viewer

Open the dashboard URL printed by the installer.

The first screen must show:

- Pulse keeps the thread.
- backend LLM off.
- raw refs disabled.
- local SQLite.
- What Pulse will tell Claude next.
- first memory saved or honestly pending.
- delete/wipe controls.

## Manual Fallback

Use this only if the installer fails and you are debugging.

Start daemon:

```bash
cd pulse/pulse-app
go run ./cmd/pulse -addr 127.0.0.1:18789 -data-dir ~/.pulse
```

Build/install MCP:

```bash
cd pulse/mcp
npm ci
npm run build
npm i -g .
```

Configure Claude Code manually:

```json
{
  "mcpServers": {
    "pulse": {
      "command": "pulse-mcp",
      "env": {
        "PULSE_BASE_URL": "http://127.0.0.1:18789",
        "PULSE_API_KEY": "paste-your-local-secret-here"
      }
    }
  }
}
```

## Verification

Ask Claude Code to call:

- `pulse_status`
- `pulse_remember`
- `pulse_recall`
- `pulse_resume`

Expected status fields:

```json
{
  "billing_mode": "host-extracted",
  "backend_llm_enabled": false,
  "raw_capture_enabled": false,
  "storage_path": "<local>"
}
```

## Import Boundary

Archive import is optional and later:

```bash
pulse migrate start --open
```

Do not present import as required for the first proof.
