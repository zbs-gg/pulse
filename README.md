# Pulse

## Copy this message to your AI agent

Copy the whole message below into Claude Code, Codex, Cursor, Windsurf, or
another AI agent that can inspect a repo and run terminal commands.

```text
Hi. Please check whether it is safe to install Pulse:
https://github.com/zbs-gg/pulse

Pulse is a local-first memory engine for AI agents. It stores structured
memories, not raw transcripts. It should be wipeable and it should not require
backend OpenAI, Anthropic, or Cohere keys by default.

Please install it carefully:

1. Read README.md, AGENTS.md, llms.txt, and docs/SECURITY_INSTALL_CHECKLIST.md.
2. Check the npm package:
   npm view @zbs-gg/pulse dist-tags
   The preview tag should be 0.6.2 or newer for this README.
3. Tell me which harness path fits my setup:
   - Claude Code full local preview; or
   - MCP/Safe Mode for Cursor, Windsurf, Claude Desktop/local MCP clients,
     Codex/OpenAI agents, Gemini CLI, or another MCP-compatible host.
4. Explain in plain English what Pulse will write, where memory is stored,
   what does NOT happen by default, and how I can erase it.
5. Ask me for confirmation before installing anything.
6. After I confirm, install the best supported path.
   For Claude Code full local preview:
     npx @zbs-gg/pulse@preview init claude-code
     pulse doctor
     pulse demo
   For MCP/Safe Mode:
     configure the host to run:
     npx -y @zbs-gg/pulse@preview mcp
     then tell me plainly that this is the compatibility fallback, not the full
     state-aware Pulse engine.
7. Show me:
   - whether backend model calls are off;
   - whether raw transcript capture is off;
   - what Pulse remembered;
   - what my next agent/session will receive;
   - the viewer URL if available;
   - the wipe/disconnect commands.

Important:
- do not import old chats without separate confirmation;
- do not store raw transcripts;
- do not print secrets;
- do not claim production readiness;
- stop and explain if anything looks unsafe.
```

## What Pulse is

Memory that knows what matters right now.

Pulse is a state-aware memory engine for AI agents. It installs locally,
retrieves the right remembered episode for *this* moment — not just the
closest text match — shows **why** that memory surfaced, shows what it will
tell your next agent, and wipes on one command.

Status: developer preview. Local-first, Claude Code-first. Not production,
not a consumer app yet.

If you are an agent asked to evaluate or install Pulse: read
[`AGENTS.md`](AGENTS.md) and [`llms.txt`](llms.txt). They are written for you.

<p align="center">
  <img src="https://raw.githubusercontent.com/zbs-gg/pulse/main/docs/assets/pulse-demo.gif" alt="pulse demo: one question, three user states, three different memories — with the reason on every line" width="820">
</p>

## Compatible Harnesses

Pulse ships as one npm package: `@zbs-gg/pulse`. The MCP server is bundled
inside it as `pulse mcp`. The older separate `@zbs-gg/pulse-mcp` package is
legacy and should not be the default install path.

| Harness | Current support | Recommended path |
|---|---|---|
| Claude Code | Primary target. Full Local Preview plus MCP/hooks/doctor/demo. | `npx @zbs-gg/pulse@preview init claude-code` |
| Claude Desktop / local Claude MCP clients | MCP-compatible Safe Mode today. Full connector/store path is not shipped. | Configure command: `npx -y @zbs-gg/pulse@preview mcp` |
| Cursor | MCP-compatible Safe Mode today. Full install automation is not first-class yet. | Configure command: `npx -y @zbs-gg/pulse@preview mcp` |
| Windsurf | MCP-compatible Safe Mode today. Full install automation is not first-class yet. | Configure command: `npx -y @zbs-gg/pulse@preview mcp` |
| Codex / OpenAI local agents | MCP-compatible when the host can run local MCP commands. Not a ChatGPT Store app. | Ask the agent to audit this repo and configure `npx -y @zbs-gg/pulse@preview mcp` |
| Gemini CLI | MCP-compatible if the CLI/harness supports local MCP command servers. Not first-class installer yet. | Use the same MCP command after agent audit |
| LangChain / CrewAI / custom agents | Integration surface, not a consumer installer. | Run Pulse MCP locally and call its tools from your framework |
| ChatGPT app / Claude Directory / Pulse Cloud | Future distribution surfaces. | Not shipped in this preview |

## Install — Pulse Local Preview

```bash
npx @zbs-gg/pulse@preview init claude-code
pulse doctor
pulse demo
```

`pulse doctor` tells you honestly which mode this machine gets. `pulse demo`
runs only on the full engine — it never fakes the result on fallback.

### What the demo proves

The demo seeds an **isolated, clearly-labeled simulated corpus** (your data is
never touched) and shows the three things generic memory tools don't:

1. **Same query, different state → different memory.** One question — "where
   are we with the launch?" — asked in three user states (drained / restored /
   angry). Different episodes surface, and every line shows the reason:
   `state x1.15 · anchor x1.05 · emotion x1.15`.
2. **Old anchors beat recent noise.** A structural anchor from months ago
   outranks this week's standup notes, and the breakdown shows why.
3. **What your next agent gets.** The continuity pack — decisions, open
   loops, do-not-repeat, emotional context — exactly as it will be injected
   into the next session.

<p align="center">
  <img src="https://raw.githubusercontent.com/zbs-gg/pulse/main/docs/assets/three-states.png" alt="same query in the drained state: burden episodes surface with state and emotion boosts visible" width="700">
</p>

Then: `pulse demo --clean` removes the whole demo store.

### Requirements (measured by doctor, not promised)

| Mode | What you get | Needs |
|---|---|---|
| Pulse Local Preview | state-aware retrieval, why-this-surfaced reasons, viewer, resume injection | Node 20+, Go toolchain (preview builds from source), Claude Code CLI, an embedder (one of the two below) |
| — local embeddings | fully local retrieval path | Apple Silicon, 16GB+ RAM (32GB comfortable), bge-m3 model on disk (~2GB), MLX python env |
| — API embeddings | easier setup, smaller RAM | Cohere API key (`~/.pulse/cohere-key.txt`) — embedding calls leave the machine, and doctor says so |
| Safe Mode (fallback) | structured local memory, inspect/wipe, keyword recall | Node 18+, nothing else |

Numbers are conservative estimates; `pulse doctor` reports the real state of
your machine and refuses to call fallback "Pulse ready".

## Safe Mode — fallback, not the product

```bash
claude mcp add pulse -- npx -y @zbs-gg/pulse@preview mcp
```

With no daemon and no embedder this runs a keyword-recall local store. It is
honest about what it is: a compatibility/trust fallback so unsupported
machines still get structured memory, inspection, and wipe. **Do not judge
Pulse retrieval by this mode, and no benchmark claim applies to it.** The
engine's bench numbers (stateful R@3 0.419 vs cosine_state 0.314 on Emo.Bench
v3) are full-engine only.

## Ingest — consent first

- **Default:** host-extracted. Your agent (Claude Code / Cursor / Codex) calls
  Pulse tools with structured capsules it extracts in-session. No raw
  transcripts, no third-party backend, no model API keys.
- **Optional:** local LLM extraction for richer capsules — hardware-dependent
  preview, off unless you turn it on.
- **Never by default:** bulk import of your old chats. Import is a separate,
  labeled, preview-first command with explicit confirmation.

## Trust Boundaries

- No backend OpenAI/Anthropic/Cohere key is required by default; with the
  Cohere *embedding* option, doctor reports `external embedding API: on`.
- Raw transcript capture is off by default — structured capsules only.
- Memory is local and inspectable; wipe needs an exact confirmation phrase.
- `pulse doctor` is the source of truth: if full retrieval is off it says
  "Pulse MCP fallback is ready. Full retrieval is not enabled." — never
  "Pulse ready".

## Direction: Material Graph

```text
Material Graph + Salience Overlay + Continuity Pack
```

Source-backed graph memory feeding continuity/resume, without claiming
autonomous prioritization or production readiness. See
`docs/PULSE_MATERIAL_GRAPH_V0.md` and adjacent docs.

## Layout

- `pulse-app/` — local Go engine (daemon, storage, state-aware retrieval) +
  `cli/` (the `@zbs-gg/pulse` npm package: installer, doctor, demo, viewer).
- `mcp/` — the MCP server source (internal component, bundled prebuilt
  inside `@zbs-gg/pulse`; not published separately).
- `docs/` — install, security checklist, and design docs.

## License

MIT for the npm packages (see per-package LICENSE files).
