# @zbs-gg/pulse

## Copy this to your AI agent

```text
Hi. Please check whether it is safe to install Pulse:
https://github.com/zbs-gg/pulse

Pulse is a local-first memory engine for AI agents. It stores structured
memories, not raw transcripts. It should be wipeable and it should not require
backend OpenAI, Anthropic, or Cohere keys by default.

Please:
1. Read README.md, AGENTS.md, llms.txt, and docs/SECURITY_INSTALL_CHECKLIST.md.
2. Check: npm view @zbs-gg/pulse dist-tags
3. Explain which harness path fits my setup.
4. Explain what Pulse writes and how I can erase it.
5. Ask me for confirmation before installing anything.
6. After I confirm, install:
   - Claude Code full local preview:
     npx @zbs-gg/pulse@preview init claude-code
     pulse doctor
     pulse demo
   - Other MCP-compatible hosts:
     configure the host to run:
     npx -y @zbs-gg/pulse@preview mcp
     and tell me this is Safe Mode/fallback, not the full state-aware engine.
7. Show me what Pulse remembered, what the next agent will receive, and how to
   wipe/disconnect it.

Important: no old-chat import without separate confirmation; no raw
transcripts; no secrets in output; stop and explain if anything looks unsafe.
```

## What Pulse is

Memory that knows what matters right now.

Pulse is a state-aware memory engine for AI agents. It installs locally,
retrieves the right remembered episode for *this* moment — not just the
closest text match — shows **why** that memory surfaced, shows what it will
tell your next agent, and wipes on one command.

This is the one package: installer/CLI (`init`, `doctor`, `demo`, `viewer`)
plus the MCP server (`pulse mcp`, bundled prebuilt).

Status: developer preview. Local-first, Claude Code-first. Not production.

## Compatible harnesses

| Harness | Current support | Recommended path |
|---|---|---|
| Claude Code | Primary full Local Preview. | `npx @zbs-gg/pulse@preview init claude-code` |
| Claude Desktop / local Claude MCP clients | MCP-compatible Safe Mode today. | `npx -y @zbs-gg/pulse@preview mcp` |
| Cursor | MCP-compatible Safe Mode today. | `npx -y @zbs-gg/pulse@preview mcp` |
| Windsurf | MCP-compatible Safe Mode today. | `npx -y @zbs-gg/pulse@preview mcp` |
| Codex / OpenAI local agents | MCP-compatible when local MCP commands are supported. | Agent-audited `npx -y @zbs-gg/pulse@preview mcp` |
| Gemini CLI | MCP-compatible when local MCP command servers are supported. | Agent-audited `npx -y @zbs-gg/pulse@preview mcp` |
| LangChain / CrewAI / custom agents | Framework integration surface. | Run `pulse mcp` and call its tools |
| ChatGPT app / Claude Directory / Pulse Cloud | Future surfaces, not shipped in this preview. | Not available yet |

<p align="center">
  <img src="https://raw.githubusercontent.com/zbs-gg/pulse/main/docs/assets/pulse-demo.gif" alt="pulse demo: one question, three user states, three different memories — with the reason on every line" width="820">
</p>


## Install — Pulse Local Preview

```bash
npx @zbs-gg/pulse@preview init claude-code
pulse doctor
pulse demo
```

`pulse doctor` gives a binary, honest verdict: "Pulse Local Preview ready."
or "Pulse MCP fallback is ready. Full retrieval is not enabled." The demo
runs only on the full engine — it never fakes results on fallback.

### What `pulse demo` proves

It seeds an isolated, clearly-labeled SIMULATED corpus (never your data) and
shows the three things generic memory tools don't:

1. **Same query, different user state → different memory.** One question in
   three states (drained / restored / angry) — different episodes surface,
   each line with the reason: `state x1.15 · anchor x1.05 · emotion x1.15`.
2. **Old anchors beat recent noise** — and the score breakdown shows why.
3. **What your next agent gets** — the continuity pack as it will be
   injected into the next session.

`pulse demo --clean` removes the whole demo store.

Requirements: Node 20+, Go toolchain (preview builds from source), Claude
Code CLI, and an embedder — local MLX on Apple Silicon, or a Cohere key in
`~/.pulse/cohere-key.txt` (external embedding API; doctor reports it).

## Safe Mode — fallback, not the product

```bash
claude mcp add pulse -- npx -y @zbs-gg/pulse@preview mcp
```

For machines that can't run the engine: structured local memory with
inspect/wipe and keyword recall. No benchmark claim applies to this mode.

## Trust boundaries

- Host-extracted structured capsules only — never raw transcripts.
- No backend model API keys by default; no backend LLM calls.
- No old-chat import by default — ingest is explicit and consent-first.
- Memory is local and inspectable; wipe requires an exact confirmation
  phrase. Leave anytime: `pulse wipe`, `claude mcp remove pulse`.

Docs, agent install script, and source: https://github.com/zbs-gg/pulse
(see `AGENTS.md` — written for AI agents asked to vet this install).

MIT.
