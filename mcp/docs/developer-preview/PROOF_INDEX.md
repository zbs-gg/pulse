# Proof Index

These proofs support Pulse MCP Preview v0.4.2.

They support a partner/developer preview claim. They do not support production
claims.

## Release Claim

Safe claim:

```text
Pulse MCP Preview can keep one structured memory across Claude Code sessions,
show the next resume block in a local viewer, and run without backend
OpenAI/Anthropic/Cohere keys by default.
```

## 1. Clean Claude Code Two-Session Proof

Artifact:

```text
pulse/artifacts/proofs/claude-code-pulse-only-clean-e2e-20260607/
```

What it proves:

- Fresh Pulse data dir.
- `billing_mode=local-auto`.
- `backend_llm_enabled=false`.
- `raw_capture_enabled=false`.
- Session A stores a decision through `pulse_remember`.
- Session B prompt does not repeat the decision.
- SessionStart resume contains the decision.
- Claude Code answers from Pulse context.
- No claude-mem contamination in Session B.

Important note:

The original proof predates the Preview v0.3 activity-language fix. Use the
current test/verification output for human-readable dashboard activity.

## 2. Human-Readable Dashboard Activity

Current tests:

```text
pulse/pulse-app/internal/server/continuity_test.go
TestViewerDataActivityLogsAreHumanReadable
```

What it proves:

- legacy machine event codes are not shown in viewer activity.
- legacy lifecycle prefixes are not shown in the viewer resume/activity
  payload.
- Dashboard activity uses human labels such as `Prompt noticed` and
  `AI activity recorded`.
- Raw prompt/transcript/tool payloads remain hidden.

Live browser verification during v0.3 prep showed:

```text
Pulse recorded 2 local actions.
Prompt noticed: Pulse noticed a new prompt; raw text is hidden.
AI activity recorded: Pulse recorded a background model event for this session.
```

## 3. No-Key Default

Verification command:

```bash
cd pulse/pulse-app
env -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u COHERE_API_KEY go test ./...
```

Expected status:

```json
{
  "backend_llm_enabled": false,
  "raw_capture_enabled": false
}
```

## 4. First-Run Trust Viewer

Current tests:

```text
TestFirstRunViewerKeepsTrustBoundaries
TestViewerFirstMemoryPendingAndSavedStates
TestViewerFirstRunDelightKeepsEmotionOptionalAndEditable
TestViewerPageIncludesTrustControls
```

What it proves:

- First memory is `pending` until the daemon actually stores it.
- First memory is `saved` only when `/viewer/data` exposes a real memory ID.
- Emotion feedback is optional.
- The viewer says what Pulse will tell Claude next.
- Import remains secondary.
- Delete/wipe controls are visible.

## 5. Install Path Smoke

Current package-level proof:

```text
pulse/mcp/docs/developer-preview/INSTALL_BY_LINK_E2E_20260607.md
```

What it proves:

- `@zbs-gg/pulse` v0.4.2 can pack a runnable CLI tarball.
- The tarball contains the minimal Pulse daemon and MCP preview source.
- The tarball excludes tests, local databases, secrets, JSONL archives, and
  project-local Claude/MCP config.
- The packaged `pulse init claude-code` path builds the daemon, builds MCP,
  starts local Pulse, registers Claude Code MCP, writes hooks, and saves the
  first memory in a temporary smoke project.
- `pulse doctor`, `pulse demo`, `pulse stop`, and `pulse remove claude-code`
  make the first proof and rollback path visible in the CLI.

Boundary:

This is a package-level smoke with fake external `go`, `npm`, and `claude`
commands. It does not replace the real Claude Code two-session proof.

Older source-bundle script:

Script:

```text
pulse/pulse-app/scripts/preview-install-claude-code.sh
```

Clean-ish proof performed during v0.3 prep:

- Separate `HOME`.
- Separate `PULSE_DATA_DIR`.
- Separate local port.
- Script built local daemon.
- Script built MCP.
- Script started local daemon.
- Script configured Claude Code in the caller project directory.
- Script saved first install memory.
- Script printed the viewer URL.

## 6. Import Preview Boundary

Import proof artifacts exist, but import is not the first proof path.

Relevant artifacts:

```text
pulse/artifacts/proofs/import-preview-redesign-20260607/
pulse/artifacts/proofs/import-preview-reviewed-json-20260607/
pulse/artifacts/proofs/confirmed-review-relations-20260607/
```

Safe claim:

```text
Pulse can preview old sources as candidate threads and requires review before
committing graph/continuity memory.
```

Unsafe claim:

```text
Pulse automatically imports all old chats.
```

## Required Fresh Verification Before Sharing

Run:

```bash
cd pulse/pulse-app
env -u OPENAI_API_KEY -u ANTHROPIC_API_KEY -u COHERE_API_KEY go test ./...

cd pulse/pulse-app/cli
npm test

cd pulse/mcp
npm test
npm run build
npm pack --dry-run --json
```

Then assemble the bundle and scan it for:

- `secret.key`
- `PULSE_API_KEY`
- `sk-`
- `AKIA`
- Slack bot token patterns
- GitHub token patterns
- local user paths
- local file URLs
