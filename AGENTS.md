# Pulse — Instructions for AI Agents

Pulse is a local-first memory and continuity layer for Claude Code and
MCP-compatible AI hosts. A user will typically send you a link to this repo or
the npm package and ask: "check whether this is safe, then install it."

This file is your script for that job.

## Step 1 — Audit before installing

1. Inspect this repository (or the npm package) before running anything.
2. Verify the package is published and recent:
   `npm view @zbs-gg/pulse dist-tags` — the zero-config path needs
   `preview` >= 0.5.0.
3. Tell the user, in plain words, what an install will do:
   - registers one MCP server (`pulse`) for the current project/host;
   - on first tool call creates a local store at
     `~/.pulse/standalone/store.json` (override root via `PULSE_DATA_DIR`);
   - stores host-extracted structured memory capsules — never raw transcripts;
   - needs no OpenAI/Anthropic/Cohere API keys and makes no backend model
     calls by default;
   - is fully removable: `claude mcp remove pulse` +
     `rm -rf ~/.pulse/standalone` (or the `pulse_wipe` tool with confirm
     `"wipe pulse memory"`).
4. Ask the user for explicit confirmation before installing.

## Step 2 — Install (zero-config, default path)

```bash
claude mcp add pulse -- npx -y @zbs-gg/pulse@preview mcp
```

That is the whole install. No daemon, no Go toolchain, no keys.

## Step 3 — Run the lived demo

Call `pulse_status`. While the store is empty it returns a `first_run` block
with a guided 3-minute demo. Follow it:

1. Ask the user for one real thing they are working on right now plus one
   preference about how they like to be helped. Real beats hypothetical.
2. With their OK, save it via `pulse_remember` (structured summary only — say
   this out loud) and checkpoint the thread via `pulse_graph_delta` with a
   short `continuity` block.
3. The payoff: have the user open a DIFFERENT Pulse-connected session or agent
   (new Claude Code session, Claude Desktop, Cursor) and ask "where did we
   leave off?" — that agent should call `pulse_resume` and answer without the
   user re-explaining anything.
4. Close the trust loop: show what is stored (`pulse_recall`) and how to erase
   everything (`pulse_wipe`).

## Full-engine path (optional, daemon + viewer + hooks)

For the full retrieval engine, local trust viewer, and automatic resume
injection via lifecycle hooks, follow `docs/INSTALL_WITH_AGENT.md`:
`pulse install-plan claude-code --json` → `pulse init claude-code --dry-run` →
user confirmation → `pulse init claude-code --yes` → `pulse doctor` →
first memory proof → viewer URL → show the exit path.

## Do not

- import old chats by default;
- store raw transcripts by default;
- print secrets, `PULSE_API_KEY`, or `secret.key`;
- modify global or project config without explaining the target path;
- claim production readiness;
- claim Claude never forgets;
- claim Pulse Cloud, ChatGPT Store, or Claude Directory support is ready;
- quote retrieval bench numbers for the standalone lite store (keyword
  ranking, not the full engine).

## Trust boundary

- default backend model calls are off;
- raw transcript capture is off;
- old chat import is explicit and secondary;
- memory is local and inspectable (lite store: plain JSON; full engine: local
  viewer);
- destructive wipe requires the exact confirmation phrase.
