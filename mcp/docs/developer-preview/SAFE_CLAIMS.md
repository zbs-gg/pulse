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
- The public product path is Pulse Local Preview:
  `npx @zbs-gg/pulse@preview init claude-code` → `pulse doctor` →
  `pulse demo`. The demo seeds a clearly-labeled SIMULATED corpus and proves:
  same query under different user state returns different justified episodes
  (with visible per-line reasons), old anchors beat recent noise, and the
  next-agent continuity pack matches the retrieved evidence.
- Safe Mode (`claude mcp add pulse -- npx -y @zbs-gg/pulse@preview mcp`) is a
  fallback for unsupported machines: structured local memory, inspect, wipe,
  keyword recall. Call it "Safe Mode" or "fallback" — never "Lite Pulse",
  never a product tier, and never with bench numbers nearby.
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

## Forbidden (hard rejection criteria — reject copy/implementation containing these)

- "Pulse is ready" while full retrieval is disabled (doctor must say
  "Pulse MCP fallback is ready. Full retrieval is not enabled.").
- "Lite Pulse" as a public product choice.
- Bench numbers anywhere near the keyword fallback.
- "Detected context" without a visible scan result.
- "Saved memory" without an ID/path.
- "Local" while embeddings or extraction go to an external API by default
  (the Cohere embedding path must be reported by doctor).
- "Import your chats" as a first-run path.
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
