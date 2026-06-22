# Pulse — Instructions for AI Agents

Pulse is a local-first, state-aware memory engine for AI agents. A user will
typically send you this repo and ask: "check whether this is safe, then
install it." This file is your script for that job.

The one sentence that matters: Pulse retrieves the right remembered episode
for the user's current state — and shows why — instead of returning the
closest text match. Your job is to install it honestly and let the demo prove
that, or to say plainly that this machine only supports the fallback.

## Step 1 — Audit before installing

1. Inspect this repository (or the npm package) before running anything.
2. Verify the package: `npm view @zbs-gg/pulse dist-tags` — needs
   `preview` >= 0.6.0.
3. Tell the user, in plain words, what the Local Preview install does:
   - builds and starts a local Pulse daemon (Go, runs on 127.0.0.1);
   - registers the Pulse MCP server and Claude Code lifecycle hooks;
   - stores host-extracted structured capsules in `~/.pulse`; raw transcript
     capture is off by default (and the store rejects transcript/secret/path-like
     content);
   - makes no backend model calls by default; the optional Cohere
     *embedding* path sends embedding requests out, and doctor reports it;
   - is fully removable: `pulse wipe`, `pulse disconnect claude-code`,
     `claude mcp remove pulse`, `rm -rf ~/.pulse`.
4. Ask the user for explicit confirmation before installing.

## Step 2 — Install Pulse Local Preview

```bash
npx @zbs-gg/pulse@preview init claude-code
```

Requirements: Node 20+, Go toolchain, Claude Code CLI. For full retrieval the
engine needs an embedder — local MLX (Apple Silicon) or a Cohere key in
`~/.pulse/cohere-key.txt`.

## Step 3 — Doctor, then the real demo

```bash
pulse doctor
```

Read the verdict line out loud to the user. There are exactly two honest
outcomes:

- **"Pulse Local Preview ready."** → run `pulse demo` and narrate it:
  1. It seeds an isolated, labeled SIMULATED corpus (never the user's data).
  2. One query runs in three user states (drained / restored / angry) and
     different episodes surface, each with visible reasons
     (`state x1.15 · anchor x1.05 · emotion x1.15`).
  3. An old structural anchor outranks recent noise — point at the breakdown.
  4. The continuity pack shows exactly what the next agent session receives.
  5. `pulse demo --clean` wipes the demo store.
- **"Pulse MCP fallback is ready. Full retrieval is not enabled."** → say
  exactly that. Safe Mode still gives structured local memory, inspection,
  and wipe — but it is keyword recall, NOT the Pulse engine. Never present
  it as Pulse retrieval and never quote bench numbers next to it.

## Step 4 — Real context, only by consent

After the demo, offer (do not push): "Want Pulse on your actual project?
I'll extract structured capsules from our work as we go — host-extracted,
in-session, nothing leaves the machine." Bulk import of old chats is a
separate command, preview-first, and requires its own explicit confirmation.

## Safe Mode (fallback path)

```bash
claude mcp add pulse -- npx -y @zbs-gg/pulse@preview mcp
```

Use when the machine can't run the Local Preview (no Go, no embedder, low
RAM). Be explicit with the user: this is the fallback, not the product.

## Do not

- say "Pulse is ready" when full retrieval is disabled;
- present Safe Mode / keyword recall as Pulse or as "Lite Pulse";
- quote benchmark numbers anywhere near the fallback;
- say "saved" without an ID, or "detected" without a visible result;
- import old chats by default;
- store raw transcripts;
- print secrets, `PULSE_API_KEY`, or `secret.key`;
- claim production readiness, "Claude never forgets", or store/cloud
  availability.

## Trust boundary

- backend model calls off by default; embedding path reported by doctor;
- raw transcript capture off;
- old chat import explicit and secondary;
- memory local and inspectable (viewer on the full engine, plain JSON in
  Safe Mode);
- destructive wipe requires the exact confirmation phrase.
