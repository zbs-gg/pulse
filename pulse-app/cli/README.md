# @zbs-gg/pulse

CLI installer for Pulse MCP host-extracted memory and local-auto continuity.
Claude Code is the first supported install target, not the product name.

## Publish Status

Preview v0.4.2 candidate. Publish only with a preview tag after pack/smoke checks:

```bash
npm publish --access public --tag preview
```

Do not market this as the final consumer installer yet. It is a technical
Claude Code-first preview.

Current availability rule: before using or sharing any `npx @zbs-gg/pulse@preview`
command, verify that the package is actually published:

```bash
npm view @zbs-gg/pulse dist-tags
```

If npm returns 404, do not present the `npx` path as working. Use the source
bundle, packed tarball, or local checkout for recipient rehearsals and say that
the public npm path is not ready yet.

## Demo Flow

Use the vendored preview demo script in
[`vendor/pulse-preview-source/mcp/docs/developer-preview/60S_DEMO.md`](vendor/pulse-preview-source/mcp/docs/developer-preview/60S_DEMO.md)
when showing Pulse to technical friends. Keep the claim narrow: one saved
decision, one fresh resume, one recall, local/private by default.

## Install With Your Agent

Recommended preview path:

1. Copy the prompt from
   [`vendor/pulse-preview-source/docs/INSTALL_WITH_AGENT.md`](vendor/pulse-preview-source/docs/INSTALL_WITH_AGENT.md)
   into Claude Code, Codex, or Cursor. In the full repository, the same source
   document lives at `pulse/docs/INSTALL_WITH_AGENT.md`.
2. Let the agent inspect `README.md`, `AGENTS.md`, and the security checklist.
3. The agent shows an install plan and asks for confirmation.
4. After confirmation, it runs:

   ```bash
   pulse init claude-code --yes
   pulse doctor
   pulse demo
   pulse viewer
   ```

Agent-friendly plan commands:

```bash
pulse install-plan claude-code --json
pulse install-plan claude-code
pulse init claude-code --dry-run
```

Bare `pulse init claude-code` still installs for manual compatibility. Agents
should use `--dry-run`, ask for confirmation, then use `--yes`.

## Manual Install

Preferred preview command after publishing the preview tag:

```bash
npx @zbs-gg/pulse@preview init claude-code
```

This package vendors the minimal Pulse daemon + MCP source during `npm pack`.
The init command builds the local daemon, builds the local MCP server, starts
Pulse locally, configures Claude Code MCP/hooks in the directory where it is
run, saves the first memory when reachable, and prints the viewer URL.

Source-bundle fallback:

```bash
cd pulse/pulse-app
./scripts/preview-install-claude-code.sh
```

This quick proof configures Claude Code MCP + hooks in `pulse/pulse-app`, so
run Claude Code from that same directory.

For a real project, run the installer from the target project directory:

```bash
cd /path/to/your/project
/path/to/pulse-mcp-preview-v0.4.2-source-20260608/pulse/pulse-app/scripts/preview-install-claude-code.sh
```

The installer writes Claude Code config/hooks to the directory you run it from,
not globally across every project.

Future stable npm shape, once daemon distribution is production-grade:

```bash
npx @zbs-gg/pulse init claude-code
```

This creates a local Pulse secret and registers the `@zbs-gg/pulse-mcp` stdio
server with Claude Code via `claude mcp add-json` when the Claude CLI is
available. If the Claude CLI is unavailable, it refuses to write a project
`.mcp.json` by default because that file contains `PULSE_API_KEY`. Re-run with
`--write-project-mcp` only when you want a local project fallback; the command
adds `.mcp.json` to `.gitignore`.

## Commands

```bash
pulse --why
pulse install-plan claude-code --json
pulse install-plan claude-code
pulse init claude-code
pulse init claude-code --dry-run
pulse init claude-code --yes
pulse doctor
pulse doctor --json
pulse demo
pulse connect claude-code
pulse connect claude-code --remote-control
pulse connect claude-chat --base https://pulse.example.com
pulse disconnect claude-code
pulse stop
pulse remove claude-code
pulse daemon --go-bin /path/to/pulse-go
pulse hook session-start
pulse hook user-prompt-submit
pulse hook post-tool-use
pulse hook stop
pulse migrate start --dir pulse-migrate --open
pulse migrate start --dir pulse-migrate --open --watch
pulse migrate concierge --html pulse-migrate.html --brief pulse-migrate-brief.md --open
pulse migrate guide chatgpt --open
pulse migrate guide claude
pulse migrate guide codex
pulse migrate guide claude-code
pulse migrate request chatgpt --open
pulse migrate request claude --open
pulse migrate request codex --open
pulse migrate request claude-code --open
pulse migrate wait-latest chatgpt --html pulse-preview.html --out pulse-preview.json --open
pulse migrate wait-latest claude --html pulse-preview.html --out pulse-preview.json --open
pulse migrate preview-latest chatgpt --html pulse-preview.html --out pulse-preview.json --open
pulse migrate preview-latest claude --html pulse-preview.html --out pulse-preview.json --open
pulse migrate preview ./chatgpt-export --json
pulse migrate preview ./chatgpt-export --html pulse-preview.html --out pulse-preview.json --open
pulse migrate commit ./pulse-preview.json --confirm "import pulse graph" [--privacy private|sensitive|normal] --open
pulse viewer
pulse viewer --print-url
pulse viewer --base http://127.0.0.1:18789 --data-dir /tmp/pulse-live --thread-id archive-import --open
pulse status
pulse export
pulse import --file pulse-export.json
pulse delete --id <pulse:id>
pulse wipe --confirm "wipe pulse memory"
```

Use `pulse demo` when a reviewer wants the 60-second proof. If the daemon is
running, it seeds the safe Atlas decision, recalls it, builds a resume, and
prints the viewer URL. If the daemon is not reachable, it prints the short
proof ritual and the next command to run. Use `pulse doctor` after install to check Node, Go, Claude
Code, daemon, MCP, hooks, viewer, and the trust boundary in one short report.
Use `pulse stop` to stop the preview daemon and `pulse remove claude-code` for a
local preview rollback (`disconnect claude-code` plus daemon stop).

Use `--no-animate`, `NO_COLOR=1`, or `CI=1` when you want the connect output
without the short breathing-heart animation.

`pulse daemon` requires `--go-bin` or `PULSE_GO_BIN`. It will not try to spawn a
generic `pulse` binary because the npm `pulse` bin is this CLI, not the Go
server.

## Host-Extracted Mode

Pulse does not call an LLM backend by default. Claude Code creates a minimal
`pulse.memory_capsule.v1` in MCP tool arguments; Pulse stores and recalls it.

The intended graph direction is the same: the current harness extracts meaning,
Pulse owns the graph, and API keys are only for optional background work. The
next public graph write tool is `pulse_graph_delta` with
`pulse.semantic_delta.v1`.

## Local-Auto Continuity

`pulse connect claude-code` installs MCP plus Claude Code lifecycle hooks:

- `SessionStart` prints a `pulse_resume` block.
- `UserPromptSubmit` and `PostToolUse` store redacted local observations.
- `Stop` writes a checkpoint.
- `pulse viewer` prints a local URL showing what Pulse will tell Claude next
  time; use `--open` to open it, `--data-dir` when the running server uses a
  non-default local store, and `--thread-id` to inspect the same thread that
  an archive import wrote.

Raw refs are disabled by default. Enable only with `PULSE_LOCAL_AUTO_RAW_REFS=1`
or `PULSE_RAW_REFS=1`.

`pulse connect claude-code` also writes `~/.pulse/mode` with `local-auto`, so
the Go daemon can enforce that checkpoint/observe writes are local-auto only.

Hook payloads are parsed into bounded redacted observations. `pulse hook stop`
uses structured checkpoint fields when the host supplies them; otherwise it
writes only a lifecycle checkpoint.

A clean Pulse-only Claude Code two-session proof now exists for one decision,
and Context Restore Bench passed 8/8 structured continuity cases. Describe this
as developer-preview continuity evidence rather than full Auto Continuity.

## Archive Migrator Preview

`pulse migrate start` is the one-command hand-hold. It opens the local
concierge page, opens the ChatGPT and Claude export pages when `--open` is
present, builds local Codex and Claude Code previews when those history folders
exist, writes `pulse-migrate-status.html` / `.md`, and prints the exact
`wait-latest` commands for the ChatGPT/Claude zips:

```bash
pulse migrate start --dir pulse-migrate --open
```

Add `--watch` when you want Pulse to keep waiting for the ChatGPT/Claude zips
after opening the pages:

```bash
pulse migrate start --dir pulse-migrate --open --watch
```

Nothing is imported by `start`.
Open `pulse-migrate-status.html` when you want a browser dashboard showing
ready previews, waiting archives, and copyable next commands.

`pulse migrate concierge` writes a local browser hand-hold page with the
ChatGPT/Claude export buttons, Codex/Claude Code local-history paths, and the
copyable preview/import/viewer commands. Add `--brief <file>` to also write a
short Markdown handoff for a reviewer or user:

```bash
pulse migrate concierge --html pulse-migrate.html --brief pulse-migrate-brief.md --open
```

`pulse migrate guide` is the focused hand-hold step before import. For ChatGPT
and Claude it prints the exact browser page, the button to press, and the next
preview command. Add `--open` when you want Pulse to open the browser page for
you:

```bash
pulse migrate guide chatgpt --open
pulse migrate guide claude
pulse migrate guide codex
pulse migrate guide claude-code
```

`pulse migrate request` is the main hand-hold path. For ChatGPT/Claude it opens
the archive settings page, tells the human which button to press, waits for the
export zip to appear in `~/Downloads`, then writes the same safe preview
automatically. For Codex/Claude Code it previews local history immediately.
By default it writes `pulse-preview.html` and `pulse-preview.json`; `--open`
opens the preview. Use `--downloads <dir>` for a different browser download
folder.

`pulse migrate wait-latest` is the same watcher without opening the host page.
Use it when the user already clicked Request archive / Export data.

`pulse migrate preview-latest` is the shorter path when the export zip is
already downloaded. If no archive is found yet, Pulse explains that the export
may still be preparing and points back to the request button.

`pulse migrate preview` is the manual path:
it scans a ChatGPT/Claude-like export zip, an extracted export folder, a
Codex/Claude Code JSONL history folder, or a single JSON/JSONL file, and prints
a safe preview:

```bash
pulse migrate request chatgpt --open
pulse migrate request claude --open
pulse migrate request codex --open
pulse migrate request claude-code --open
pulse migrate wait-latest chatgpt --html pulse-preview.html --out pulse-preview.json --open
pulse migrate wait-latest claude --html pulse-preview.html --out pulse-preview.json --open
pulse migrate preview-latest chatgpt --html pulse-preview.html --out pulse-preview.json --open
pulse migrate preview-latest claude --html pulse-preview.html --out pulse-preview.json --open
pulse migrate preview ~/Downloads/chatgpt-export.zip
pulse migrate preview ~/Downloads/chatgpt-export
pulse migrate preview ~/Downloads/claude-export --json
pulse migrate commit pulse-preview.json --confirm "import pulse graph" --open
pulse migrate commit pulse-preview.json --confirm "import pulse graph" --privacy sensitive --open
pulse viewer
```

The preview is offline and does not call the Pulse server. It counts
conversations/messages, detects likely people and thread names, and redacts
secret/path-like fragments before anything is shown. It does not write raw chat
text or memory records.

Use `--html <file>` to write a local browser preview around candidate threads,
decisions, open loops, do-not-repeat notes, emotional anchors, people found,
token economy, and review decisions. Add `--out <file>` to write the
commit-ready JSON next to it and add copyable import/viewer commands inside the
preview. Add `--open` to open that preview after writing it, before committing
anything into Pulse.

Zip auto-unpack uses the system `unzip` command and rejects unsafe archive
paths before extraction. `pulse migrate commit` requires explicit confirmation
and writes only structured `pulse.semantic_delta.v1` graph deltas, not raw
transcript. Imported graph items default to `private`; use
`--privacy sensitive` or `--privacy normal` only as an explicit local/operator
override. Add `--open` to open the viewer immediately after import. After
commit, `pulse viewer` shows the next resume block, saved decisions, open
loops, emotional/state anchors, reviewed people context, and local trust
controls assembled from the graph.

Paper boundary: archive migration and the viewer are a product-facing extension,
not part of the evaluated retrieval claims until they have their own
migration-quality and privacy evaluation. See
[`docs/archive-migration-paper-boundary.md`](../docs/archive-migration-paper-boundary.md).

If the running Pulse daemon uses a non-default data directory, point the viewer
at that same store:

```bash
pulse viewer --base http://127.0.0.1:18789 --data-dir /path/to/pulse-data --thread-id archive-import --open
```

If `pulse viewer` detects that the running local daemon rejects the current
secret, it prints the likely daemon `--data-dir` and the exact viewer command to
retry. This keeps the browser hand-hold from silently opening an unauthorized
local page.

## Claude Code Remote Control

Use this when you want to type from Claude mobile or `claude.ai/code` while the
actual Claude Code session keeps local Pulse MCP, hooks, project files, and
SQLite memory:

```bash
pulse connect claude-code --remote-control
claude --remote-control "Pulse Memory"
```

Remote Control is not a Pulse Cloud connector. It is the first practical
mobile-friendly path because the local Claude Code process still owns the MCP
tooling. Ordinary Claude Chat needs a later remote MCP custom connector with
public HTTPS and OAuth. The `@zbs-gg/pulse-mcp` package already has a
development Streamable HTTP mode for that connector path:

```bash
PULSE_REMOTE_BEARER=dev-token npx -y @zbs-gg/pulse-mcp --http --port 8787
```

HTTP mode requires `PULSE_REMOTE_BEARER` by default and refuses authless public
binds. Static bearer is still only for local/tunneled previews. Public
distribution needs the host's real HTTPS + OAuth/auth review path.

For the OAuth protected-resource readiness shape used by Claude custom
connectors:

```bash
PULSE_REMOTE_PUBLIC_BASE_URL=https://pulse.example.com \
PULSE_REMOTE_AUTH_ISSUER=https://auth.example.com \
npx -y @zbs-gg/pulse-mcp --http --host 0.0.0.0 --port 8787
```

This is still MCP package behavior, not the CLI's daemon distribution story.
For a private connector smoke test, add `PULSE_REMOTE_OAUTH_DEV=1` on a
temporary HTTPS origin to enable dev-only DCR, PKCE token exchange, refresh
tokens, and in-memory bearer validation. It auto-consents and must not be used
as production auth.
Do not use `PULSE_REMOTE_TRUST_AUTH_HEADER=1` unless
`PULSE_REMOTE_AUTH_PROXY_MODE=1` is also set behind a verified token-validating
proxy.

## Claude Chat Custom Connector Handoff

Once `@zbs-gg/pulse-mcp` is running behind a temporary HTTPS origin, use:

```bash
pulse connect claude-chat --base "$PULSE_PUBLIC_ORIGIN"
```

This does not mutate Claude by itself. It prints the exact custom connector URL,
the reusable smoke command, and the two proof prompts:

- save a host-extracted graph delta with `pulse_graph_delta`;
- start a fresh Claude Chat and resume the same thread with `pulse_resume`.

Use `--open` only when you want the CLI to open Claude connector settings for
the logged-in operator. The final connector install is a persistent Claude
account/workspace change and should be confirmed in Claude UI.

## License

MIT
