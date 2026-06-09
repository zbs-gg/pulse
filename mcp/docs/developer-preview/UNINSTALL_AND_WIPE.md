# Uninstall And Wipe

Pulse MCP Preview v0.4.2 must be easy to leave.

## Wipe Local Memory

From the Pulse CLI:

```bash
pulse wipe --confirm "wipe pulse memory"
```

If you are running from the checkout:

```bash
cd pulse/pulse-app/cli
node src/cli.js wipe --confirm "wipe pulse memory"
```

The confirmation phrase is required on purpose.

## Delete One Memory

Use the viewer trust controls or CLI:

```bash
pulse delete --id <pulse:id>
```

From checkout:

```bash
cd pulse/pulse-app/cli
node src/cli.js delete --id <pulse:id>
```

## Stop Preview Daemon

The preview installer writes a pid file when it starts the daemon:

```bash
kill "$(cat ~/.pulse/pulse-preview-daemon.pid)"
```

If that pid is stale, find the local preview daemon:

```bash
ps aux | grep pulse-preview-daemon
```

Then kill that process.

## Remove Local Data

After wiping or when you intentionally want to delete the preview store:

```bash
rm -rf ~/.pulse
```

This removes the local SQLite database, secret, logs, and preview daemon binary.

## Remove Claude Code Hooks

The preview installer writes Claude Code hooks to the project where you ran it:

```text
.claude/settings.local.json
```

Remove Pulse hook entries from that file, or remove the file if it only exists
for Pulse preview.

The installer adds this file to `.gitignore` because it is local configuration.

## Remove MCP Config

If Claude Code CLI was used, inspect Claude Code MCP servers:

```bash
claude mcp list
```

Remove the Pulse server using the Claude Code command appropriate for your
version. If your Claude Code version does not provide a remove command, edit the
local Claude Code MCP config manually and remove the `pulse` server entry.

## Remove npm MCP Package

If you installed the MCP package globally:

```bash
npm uninstall -g @zbs-gg/pulse-mcp
```

## What Should Not Remain In A Project Repo

Do not commit:

- `.mcp.json` containing `PULSE_API_KEY`
- `.claude/settings.local.json`
- local `pulse.db`
- `secret.key`
- preview logs

## Exit Check

After uninstall/wipe:

```bash
curl -fsS http://127.0.0.1:18789/memory/status
```

This should fail unless another local Pulse daemon is intentionally running.
