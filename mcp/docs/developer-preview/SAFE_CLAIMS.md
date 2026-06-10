# Safe Claims

Use this page when writing README text, demo narration, tweets, partner DMs, or
review bundle briefs.

## Allowed

- Pulse MCP Preview v0.4.2 is a local-first partner/developer preview.
- Pulse keeps the thread across AI chats.
- Stop re-explaining your project to Claude Code.
- Host model extracts; Pulse validates, stores, recalls, and resumes.
- Pulse stores structured memory capsules locally.
- Pulse can recall/resume saved memories across Claude Code sessions.
- Pulse shows what it will tell Claude next.
- Pulse can be installed trust-first by asking an AI agent to inspect the repo,
  show the plan, ask confirmation, and run the preview installer.
- Pulse MCP v0.4.0+ installs with one command
  (`claude mcp add pulse -- npx -y @zbs-gg/pulse-mcp@preview`) and works
  without a local daemon through a standalone lite store. Use this claim only
  after the npm `preview` dist-tag resolves to v0.4.0+
  (`npm view @zbs-gg/pulse-mcp dist-tags`).
- Standalone lite recall is keyword ranking, not the full Pulse retrieval
  engine; say "lite store" when describing it, and never quote bench numbers
  for standalone mode.
- No backend OpenAI, Anthropic, or Cohere key is required by default.
- Raw transcript capture is off by default.
- Import preview is review-first and private by default.
- User-marked active reviewed threads can appear in the next resume block.
- Inactive reviewed threads are not injected by default.
- Token savings are estimates unless explicitly measured.

## Short Pitch

```text
Pulse MCP Preview helps Claude Code remember structured context across sessions.
It stores host-extracted memory locally, shows what will be resumed next, and
does not require backend model API keys by default.
```

## Forbidden

- Production ready.
- Claude never forgets.
- Works for everyone.
- One-click install for nontechnical users.
- Agent installs without user confirmation.
- Automatic all-chat import is ready.
- Pulse safely imports everything without review.
- ChatGPT app ready.
- Claude public connector ready.
- Pulse Cloud ready.
- Entity resolution is solved.
- Pulse knows what you should do next.
- Emotion-aware companion is fully shipped.

## Boundary Sentence

Use this when sharing broadly:

```text
This is not the final consumer app; it is a Claude Code-first preview that
proves the memory/resume loop.
```
