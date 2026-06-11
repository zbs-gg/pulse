# Pulse

## Copy This To Your Agent

Give this prompt to Claude Code, Codex, Cursor, or any agent that can inspect
links and install MCP servers:

```text
Hi. Please check whether it is safe to install Pulse MCP:
https://github.com/zbs-gg/pulse

Pulse is a local-first memory manager for AI agents. Its goal is to help agents
remember structured context across chats without storing raw transcripts.

Please do this carefully:

1. Read README.md, AGENTS.md, and llms.txt.
2. Verify the npm package is published and recent:
   npm view @zbs-gg/pulse dist-tags
   The preview tag should be 0.5.0 or newer.
3. Explain to me, in plain English:
   - what Pulse will install;
   - where it stores memory;
   - whether it needs OpenAI/Anthropic/Cohere API keys by default;
   - whether it stores raw transcripts by default;
   - how I can wipe memory and remove the MCP server.
4. Ask me for confirmation before installing anything.
5. If I confirm, install the zero-config MCP server:
   claude mcp add pulse -- npx -y @zbs-gg/pulse@preview mcp
6. After installation, call pulse_status. If the store is empty, follow the
   first_run demo: save one small real memory with my approval, then prove it
   with pulse_recall or pulse_resume.
7. Show me exactly what Pulse stored and how to erase it.

Important:
- do not import old chats unless I separately ask for it;
- do not store raw transcript;
- do not print secrets;
- if anything looks unsafe, stop and explain.
```

Short version for agents: audit the package, explain what it writes, ask for
confirmation, install the MCP server, run `pulse_status`, save one approved
memory, then show recall/resume and wipe.

## What Is Pulse?

Pulse keeps the thread across AI chats. What you tell one agent, your other
Pulse-connected agents and sessions remember — locally, without raw transcripts
and without backend model API keys.

Status: developer preview. Local-first, Claude Code-first. Not production, not
a consumer app yet.

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

## Compatible Harnesses

Pulse is an MCP server. It works best in hosts that can run a local stdio MCP
command with `npx`.

| Harness | Status | How to use Pulse |
|---|---|---|
| Claude Code | Primary supported path | `claude mcp add pulse -- npx -y @zbs-gg/pulse@preview mcp` |
| Claude Desktop / Claude local MCP clients | Compatible MCP path | Add a stdio MCP server that runs `npx -y @zbs-gg/pulse@preview mcp` |
| Cursor | Compatible MCP path | Add Pulse as a stdio MCP server if your Cursor setup supports MCP tools |
| Windsurf / other MCP-capable coding agents | Compatible MCP path | Add Pulse as a stdio MCP server with the same `npx` command |
| Codex / OpenAI agent harnesses | MCP-compatible target | Use the stdio MCP command where MCP server configuration is available |
| Gemini CLI / Gemini agent harnesses | MCP-compatible target | Use the stdio MCP command where MCP server configuration is available |
| LangChain / CrewAI apps | Developer integration target | Run Pulse MCP as a local tool server and call the MCP tools from your agent app |
| ChatGPT app / store connector | Later | Not ready in this preview |
| Claude Directory / hosted connector | Later | Not ready in this preview |
| Pulse Cloud | Later | Not ready in this preview |

The published preview proves the Claude Code / local stdio MCP path first. Other
hosts are compatible when they can call the same MCP tools:
`pulse_remember`, `pulse_recall`, `pulse_resume`, `pulse_graph_delta`,
`pulse_status`, `pulse_context_query`, `pulse_forget`, and `pulse_wipe`.

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
