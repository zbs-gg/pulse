# Pulse MCP Preview v0.4.2: 60-90 Second Demo

Goal: prove one thing clearly.

```text
Pulse keeps the thread across Claude Code sessions.
```

## Demo Script

```text
Every AI-heavy person has this pain:
you explain your project in one Claude Code session,
then open a new session,
and the model has no idea what you already decided.

Pulse MCP is a local-first memory layer for that.

Here I save one decision:
"Atlas must not own the People Graph; Pulse owns portable continuity memory."

Pulse stores it as a structured memory capsule.
No raw transcript.
No backend OpenAI or Anthropic key.

Now I open a fresh Claude Code session and ask:
"What did we decide about Atlas and the People Graph?"

I do not repeat the decision.
Pulse injects a small resume block,
and Claude answers from that context.

The viewer shows exactly what Pulse will tell Claude next,
so this is not black-box memory.

That is the preview:
Pulse keeps the thread across AI chats.
```

## Live Flow

1. Install with your agent.

   Copy the prompt from:

   ```text
   pulse/docs/INSTALL_WITH_AGENT.md
   ```

   The agent should show the install plan, ask confirmation, install, run
   `pulse doctor`, and open or print the viewer URL.

   Manual fallback after the preview package is published:

   ```bash
   npx @zbs-gg/pulse@preview init claude-code
   ```

   If npm returns 404, use the source-bundle fallback below and say the public
   npm path is not ready yet.

   Then check the path:

   ```bash
   pulse doctor
   ```

   Source-bundle fallback:

   ```bash
   cd pulse/pulse-app
   ./scripts/preview-install-claude-code.sh
   ```

   For the quick proof, start Claude Code from `pulse/pulse-app`. To demo a real
   project instead, run the installer from that project directory:

   ```bash
   cd /path/to/your/project
   /path/to/pulse-mcp-preview-v0.4.2-source-20260608/pulse/pulse-app/scripts/preview-install-claude-code.sh
   ```

2. In Claude Code, ask:

   ```text
   Remember this in Pulse: Atlas must not own the People Graph; Pulse owns portable continuity memory.
   ```

3. Open a fresh Claude Code session.

4. Ask without repeating the decision:

   ```text
   What did we decide about Atlas and the People Graph?
   ```

5. Open the dashboard URL printed by the installer.

6. Point at the card:

   ```text
   What Pulse will tell Claude next
   ```

7. Show wipe/exit:

   ```bash
   pulse wipe --confirm "wipe pulse memory"
   ```

## Expected Viewer Signals

- `backend LLM off`
- `raw refs disabled`
- `local SQLite`
- `What Pulse will tell Claude next`
- first memory saved or honestly pending
- recent activity in human words
- delete/wipe controls

## Say This

```text
This is Pulse MCP Preview v0.4.2.
It is local-first and Claude Code-first.
It proves one memory can survive a fresh session with no backend model API key.
```

## Do Not Say This

```text
Claude never forgets.
This is production.
Automatic all-chat import is ready.
This works for every user and every host.
```
