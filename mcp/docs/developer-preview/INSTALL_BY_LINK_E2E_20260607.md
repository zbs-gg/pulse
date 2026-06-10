# Install-By-Link Package Smoke, 2026-06-07

Status: package-level pass for Pulse MCP Preview v0.4.

This proof checks the new `@zbs-gg/pulse` install-by-link shape before npm
publication. It does not replace the real Claude Code two-session proof; it
checks that the packaged CLI can run the setup path from a tarball.

## Command Shape

Future published command:

```bash
npx @zbs-gg/pulse@preview init claude-code
```

Local package-smoke equivalent:

```bash
npm exec --yes --package zbs-gg-pulse-0.4.0.tgz -- pulse init claude-code
```

## Test Boundary

The smoke used a temporary home directory, temporary project directory, and
temporary Pulse data directory. `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, and
`COHERE_API_KEY` were unset.

External commands were replaced with local test doubles for this package smoke:

- `go` recorded the build command and wrote a tiny local HTTP daemon.
- `npm` recorded `ci` / `run build` inside the bundled MCP source.
- `claude` recorded the MCP registration command without touching a real
  Claude Code project.

This avoids modifying the operator's real Claude Code configuration while still
exercising the packaged installer path.

## Verified

- `npm pack` generated `zbs-gg-pulse-0.4.0.tgz`.
- The tarball contains `src/cli.js`.
- The tarball contains `vendor/pulse-preview-source/pulse-app/cmd/pulse`.
- The tarball contains the bundled MCP source under
  `vendor/pulse-preview-source/mcp`.
- The tarball does not contain `node_modules`, `dist`, test files, local
  databases, `.mcp.json`, `.claude`, `.DS_Store`, JSONL archives, or
  `secret.key`.
- `pulse init claude-code` built the local Pulse daemon.
- `pulse init claude-code` built the bundled MCP package.
- `pulse init claude-code` started a local daemon.
- `pulse init claude-code` registered Claude Code MCP through `claude mcp
  add-json`.
- `pulse init claude-code` wrote Claude Code lifecycle hooks into the caller
  project directory.
- The project-local `.mcp.json` fallback was not written.
- The first install memory was saved by the local daemon.
- The CLI printed the first-run dashboard URL.

Representative redacted output:

```text
[pulse] Pulse daemon: running locally
[pulse] Claude Code MCP registered via claude mcp add-json
[pulse] Claude Code continuity hooks written to <project>/.claude/settings.local.json
[pulse] memory proof first, import later
Dashboard:
  http://127.0.0.1:<port>/viewer?key=<redacted>&thread_id=project&first_run=1&host=claude-code
[pulse] First memory saved locally: pulse:first-memory
```

## Still Required Before Wider Sharing

- Publish `@zbs-gg/pulse` with the `preview` tag.
- Run the same command from a real target project with a real Claude Code CLI.
- Record the real two-session Claude Code flow:
  session A remember, session B fresh resume, viewer inspection, wipe.

