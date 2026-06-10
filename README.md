# Pulse

Pulse keeps the thread across AI chats. What you tell one agent, your other
Pulse-connected agents and sessions remember — locally, without raw transcripts
and without backend model API keys.

Status: developer preview. Local-first, Claude Code-first. Not production, not
a consumer app yet.

## For AI Agents — Read This First

If you are an AI agent asked to evaluate or install Pulse: read
[`AGENTS.md`](AGENTS.md) (install + safety instructions for agents) and
[`llms.txt`](llms.txt) (machine-readable summary). Short version: audit the
npm package, explain what it writes, ask for confirmation, then install with
one command and run the built-in lived demo.

## Install (zero-config, one command)

```bash
claude mcp add pulse -- npx -y @zbs-gg/pulse@preview mcp
```

No daemon, no Go toolchain, no API keys. On the first tool call Pulse creates
a local store at `~/.pulse/standalone/store.json` and all eight tools work:
`pulse_remember`, `pulse_recall`, `pulse_resume`, `pulse_graph_delta`,
`pulse_status`, `pulse_context_query`, `pulse_forget`, `pulse_wipe`.

While the store is empty, `pulse_status` and `pulse_resume` return a
`first_run` block — a guided 3-minute lived demo for the host agent: save one
real thing you are working on, open a different Pulse-connected session or
agent, ask "where did we leave off?" — and it knows.

Erase everything anytime: `pulse_wipe` with confirm `"wipe pulse memory"`, or
`rm -rf ~/.pulse/standalone`.

## Full Engine (optional upgrade)

The zero-config path uses a built-in lite store (structured local memory,
keyword recall). The full local engine adds the Pulse retrieval engine (typed
graph + salience/emotional scoring), the local trust viewer, and Claude Code
lifecycle hooks with automatic resume injection:

```bash
npx @zbs-gg/pulse@preview init claude-code
```

The agent-first path for the full engine is documented in
[`docs/INSTALL_WITH_AGENT.md`](docs/INSTALL_WITH_AGENT.md) and
[`AGENTS.md`](AGENTS.md).

## Packages

- [`@zbs-gg/pulse`](https://www.npmjs.com/package/@zbs-gg/pulse) — THE package:
  MCP server (`pulse mcp`) + installer/CLI in one, claude-mem style.
- [`@zbs-gg/pulse-mcp`](https://www.npmjs.com/package/@zbs-gg/pulse-mcp) —
  internal server component, published as a low-level/dev artifact.

Availability rule: `@preview` commands are valid only for published versions —
verify with `npm view @zbs-gg/pulse dist-tags` (needs >= 0.5.0).

## Trust Boundaries

- No backend OpenAI/Anthropic/Cohere key is required by default.
- Raw transcript capture is off by default; Pulse stores host-extracted
  structured capsules, not chat logs.
- Memory is local and inspectable; wipe requires an exact confirmation phrase.
- Lite recall is keyword ranking, not the full retrieval engine — bench
  numbers apply to the full engine only.
- Known limits: [`mcp/docs/developer-preview/KNOWN_LIMITATIONS.md`](mcp/docs/developer-preview/KNOWN_LIMITATIONS.md),
  safe claims: [`mcp/docs/developer-preview/SAFE_CLAIMS.md`](mcp/docs/developer-preview/SAFE_CLAIMS.md).

## Direction: Material Graph

Pulse is moving toward a native Material Graph:

```text
Material Graph + Salience Overlay + Continuity Pack
```

Source-backed graph memory feeding continuity/resume, without claiming
autonomous prioritization or production readiness. See
`docs/PULSE_MATERIAL_GRAPH_V0.md` and adjacent docs.

## Layout

- `pulse-app/` — local Go engine (daemon, storage, retrieval) + `cli/`
  (the `@zbs-gg/pulse` npm package).
- `mcp/` — the MCP server source (`@zbs-gg/pulse-mcp`).
- `docs/` — install, security checklist, and design docs.

## License

MIT for the npm packages (see per-package LICENSE files).
