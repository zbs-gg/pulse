# Install Pulse With Your AI Agent

Pulse MCP Preview v0.4.2 is meant to be installed trust-first.

The recommended path is not to hand-edit MCP config. Copy the prompt below into
Claude Code, Codex, or Cursor and ask your agent to inspect, explain, ask for
confirmation, install, prove one memory, and show the exit path.

Availability note: the public `npx @zbs-gg/pulse@preview` path works only after
the preview package is published. If npm returns 404, the agent should stop and
say that the public npm path is not ready, unless you explicitly provide a
source bundle, tarball, or local checkout to test.

## Copyable Agent Prompt

```text
Hi. Please inspect this repository:
https://github.com/zbs-gg/pulse

This is Pulse: local-first memory for Claude Code and MCP-compatible hosts.
Its goal is to help AI not start from zero in each new chat.

Please install it carefully:

1. First read README.md, AGENTS.md, docs/INSTALL_WITH_AGENT.md, and
   docs/SECURITY_INSTALL_CHECKLIST.md.
2. Check exactly what will be installed:
   - local Pulse daemon;
   - Pulse MCP server;
   - Claude Code MCP config;
   - Claude Code lifecycle hooks;
   - local viewer;
   - local SQLite storage.
3. Check safety:
   - whether OpenAI/Anthropic/Cohere API keys are required by default;
   - whether raw transcripts are stored by default;
   - whether there are suspicious postinstall scripts;
   - whether the npm preview packages are actually published, or whether this
     install must use a provided source bundle/local checkout;
   - where files are written;
   - how to wipe memory and disconnect MCP.
4. Show me the install plan: what will change and where.
5. Ask me for confirmation before installing.
6. After confirmation, install Pulse for the current project.
7. Run the checks:
   - pulse doctor;
   - pulse demo;
   - first memory proof;
   - viewer.
8. Show me:
   - what Pulse remembered;
   - what Claude will receive next time;
   - how to wipe memory and disconnect MCP.

Important:
- do not import old chats without separate confirmation;
- do not store raw transcript;
- do not print secrets;
- do not invent an npm install route if @zbs-gg/pulse@preview is not published;
- if anything looks unsafe, stop and explain.
```

## Pre-Publish Recipient Rehearsal

If the npm preview package is not published yet, test the recipient flow from a
provided source bundle or local checkout instead of the public `npx` path.

The recipient prompt should add one line:

```text
If @zbs-gg/pulse@preview is not published yet, use this provided source
bundle/local checkout for the rehearsal and tell me that the public npm path is
not ready.
```

## Agent Command Sequence

The agent should prefer this sequence after cloning or opening the repository:

```bash
pulse install-plan claude-code --json
pulse init claude-code --dry-run
pulse init claude-code --yes
pulse doctor
pulse demo
pulse viewer
```

If the published preview package is available and being used directly:

```bash
npx @zbs-gg/pulse@preview init claude-code --dry-run
npx @zbs-gg/pulse@preview init claude-code --yes
```

Bare `pulse init claude-code` still installs for manual compatibility. Agents
should use `--dry-run`, show the plan, ask confirmation, then use `--yes`.

## What Success Looks Like

The user should see:

- Pulse is breathing locally.
- Backend LLM calls are off by default.
- Raw transcript capture is off.
- A first private memory is saved or clearly shown as pending.
- `What Pulse will tell Claude next` is visible in the viewer.
- The user can wipe memory and disconnect Claude Code.

## What This Is Not

- Not production.
- Not a ChatGPT Store app.
- Not a Claude Directory connector.
- Not Pulse Cloud.
- Not automatic all-chat import.
- Not a promise that Claude never forgets.
