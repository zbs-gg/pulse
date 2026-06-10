# Agent-First Install Proof, 2026-06-08

Status: pass with one boundary.

This proof supports Pulse MCP Preview v0.4.2 as a Claude Code-first
developer/partner preview. It verifies the agent-first command sequence through
the packed npm CLI in a clean temporary project.

## Claim Under Test

```text
Install with your agent.
Start with one memory.
See what Claude gets next.
Forget everything anytime.
```

## Boundary

This is a clean temp-project command proof through the packed CLI. It verifies
the install-plan, dry-run, confirmed install, doctor, live demo, viewer URL,
wipe, disconnect, and stop path.

It is not a transcript of a separate human pasting the install prompt into a
fresh Claude Code chat. That remains the next proof to record for a public
preview video.

## Agent / Host

- Host CLI available: Claude Code `2.1.168`.
- Installing agent sequence: Codex executed the documented command path.
- Repository target: `https://github.com/zbs-gg/pulse`.
- Package under test: `zbs-gg-pulse-0.4.2.tgz`.
- Project: `<temp>/project`.
- Data dir: `<temp>/pulse-data`.
- API keys: OpenAI, Anthropic, and Cohere unset.

## Confirmation Point

Before install, the sequence recorded:

```bash
pulse install-plan claude-code --json
pulse init claude-code --dry-run
```

The dry run printed the install plan and wrote no Pulse data, hooks, or MCP
config.

## Commands Recorded

Artifacts live in:

```text
pulse/artifacts/proofs/agent-install-v0.4.2-20260608/
```

Recorded files:

- `install-plan.txt`
- `dry-run.txt`
- `init.txt`
- `doctor.txt`
- `demo.txt`
- `viewer-url.txt`
- `wipe.txt`
- `disconnect.txt`
- `stop.txt`

Logs are redacted: no real local user path, no viewer key, no Pulse secret.

## Install Result

The packed CLI:

- built the local Pulse daemon;
- built the local Pulse MCP package;
- started Pulse locally;
- registered Claude Code MCP with `claude mcp add-json`;
- wrote Claude Code hooks into the temporary project;
- printed the breathing CLI;
- saved the first install memory;
- printed a redacted first-run viewer URL.

Representative redacted output:

```text
[pulse] Pulse is breathing locally.
Claude Code:             connected
MCP:                     configured
Hooks:                   installed
backend LLM off
raw transcript capture off
Dashboard:
  http://127.0.0.1:19892/viewer?key=<redacted>&thread_id=project&first_run=1&host=claude-code
[pulse] First memory saved locally: pulse:<id>
```

## Doctor Result

`pulse doctor` completed after install and reported the Zero-to-Wow path.

The important trust fields were:

```text
backend LLM off
raw transcript capture off
```

## First Memory / Demo Result

`pulse demo` seeded the safe Atlas decision, recalled it, and built a resume:

```text
Without Pulse:
  A fresh Claude Code session would not know this decision.

With Pulse:
  Recall found: 1 item(s)
  Resume includes: Atlas must not own the People Graph; Pulse owns portable continuity memory.
```

## Viewer

`pulse viewer --print-url` printed only the local viewer URL with the key
redacted in saved logs.

Screenshots for the agent-first preview page:

```text
pulse/artifacts/screenshots/preview-v0.4.2-agent-first/preview-agent-first-desktop.png
pulse/artifacts/screenshots/preview-v0.4.2-agent-first/preview-agent-first-mobile.png
```

## Wipe / Disconnect

The proof ran:

```bash
pulse wipe --confirm "wipe pulse memory"
pulse disconnect claude-code
pulse stop
```

`pulse remove claude-code` remains a non-destructive rollback wrapper:
disconnect + stop + show wipe command. Wipe still requires the exact
confirmation phrase.

## Known Issues

- This is not production.
- This is not a ChatGPT Store app, Claude Directory connector, or Pulse Cloud.
- This is not automatic all-chat import.
- A true pasted-prompt agent transcript should be recorded next.

