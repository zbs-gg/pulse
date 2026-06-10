# Known Limitations

Pulse MCP Preview v0.4.2 is shareable with technical people. It is not a broad
consumer release.

## Product Boundary

- Claude Code-first preview.
- Local daemon optional since `@zbs-gg/pulse-mcp` v0.4.0: without a daemon the
  MCP server uses a standalone lite store (plain local JSON). The lite engine
  has no full retrieval engine (typed graph scoring, emotional retrieval), no
  viewer, and no lifecycle hooks with automatic resume injection — resume works
  only when the host calls `pulse_resume`.
- Lite recall is keyword-overlap ranking, not the Pulse retrieval engine. Do
  not quote bench numbers for standalone mode.
- No signed binaries or auto-update.
- `@zbs-gg/pulse` CLI is still not a broad npm consumer installer, even though
  v0.4.2 adds agent-first `install-plan`, `init --dry-run`, `doctor --json`,
  live `pulse demo`, and preview rollback commands.
- Cloudflare/remote connector remains private smoke, not Pulse Cloud.
- ChatGPT Store, Claude Directory, Gemini, LangChain, and CrewAI are later
  distribution surfaces.

## Install Boundary

- The zero-config path is
  `claude mcp add pulse -- npx -y @zbs-gg/pulse-mcp@preview`: standalone lite
  store, no daemon, no keys. It covers memory + continuity tools only.
- The full-engine path stays agent-first: copy the prompt from
  `pulse/docs/INSTALL_WITH_AGENT.md`, let the agent audit the repo, show the
  plan, ask confirmation, then run `pulse init claude-code --yes`.
- The source-bundle script remains a fallback for reviewers.
- The future public full-engine command is `npx @zbs-gg/pulse init claude-code`,
  but daemon distribution must be solved before calling that consumer-ready.
- Claude Code CLI must be installed.
- If Claude CLI is unavailable, Pulse refuses to silently write `.mcp.json`
  because it contains `PULSE_API_KEY`.

## Memory Boundary

- Host-extracted mode relies on the host model to call Pulse tools.
- Pulse backend cannot spend a Claude Max, ChatGPT Plus, or Gemini
  subscription.
- Backend LLM compression is opt-in later through BYOK, local model, or Pulse
  Cloud.
- `pulse_resume` is a bounded startup block, not a full memory dump.

## Import Boundary

- Old chat/archive import is preview/review/commit, not automatic ingestion.
- Import is secondary to first memory proof.
- Entity resolution is conservative and still needs user review.
- Ambiguous entities must be confirmed, edited, ignored, merged, or marked
  private.
- Raw transcripts are not returned through public MCP recall/resume by default.

## UX Boundary

- Viewer is the local trust layer, not the final Garden app.
- Some import-review controls are operator-grade.
- Edit/mark-important/change-emotion controls must be treated as preview-only
  unless the current screen confirms persistence.
- Token savings are estimates unless explicitly measured.

## Security Boundary

- Local secrets stay local.
- Shared bundles must not include real `secret.key`, `PULSE_API_KEY`,
  local user paths, local file URLs, or raw transcripts.
- npm audit currently reports transitive dependency advisories; do not call this
  production until dependency/security review is done.
