# Pulse — Instructions for AI Agents

Pulse is local-first, state-aware memory for AI agents. When a user asks you
to install it, your job is to audit first, explain the exact local changes,
obtain consent, run the one Personal command, and report the real readiness
state. Claude Code, Cursor, and Codex are equal release-gated bootstrap hosts.

Publication gate: these host-neutral instructions apply to the public npm path
only when `preview` contains `pulse.personal_install_plan.v2` and the matching
signed artifacts. Until then, report this branch as pending publication.

The product promise is concrete: a normal memory is shown before it is saved,
survives into a fresh task in a verified host, carries provenance and retrieval reasons,
and appears in Memory Home with honest token-economy evidence.

## 1. Audit before installing

1. Inspect this repository or the exact npm package.
2. Run `npm view @zbs-gg/pulse dist-tags --json`. The Personal path described
   here requires the `preview` release that contains version `0.7.0` or newer
   and emits `pulse.personal_install_plan.v2`.
3. Check for an Apple Silicon Mac, Node 20+, a Git project, and at least one of
   Claude Code, Cursor, or Codex.
4. Explain the install in plain language:
   - a verified Pulse-owned daemon, local embedding runtime, model, native
     plugins for every detected compatible host, and macOS presence helper are installed;
   - the daemon listens only on numeric loopback and memory stays in a private,
     project-bound local vault;
   - raw transcript capture and backend model calls are off by default;
   - old chats are not imported;
   - Personal memory never enters Git automatically;
   - Memory Home shows every pending write before the save delay starts.
5. Ask for explicit confirmation. Do not translate `--yes` into security or
   data approval.

## 2. Install Personal Pulse

```bash
npx -y @zbs-gg/pulse@preview install
```

This path does not require Go, Python, Make, Docker, a model API key, or manual
configuration editing. The wizard verifies the signed release before it
mutates product state. A missing, expired, downgraded, or invalid manifest is
a hard stop, not a reason to fall back silently.

The user may need to approve the macOS presence prompt, trust the exact Pulse
hook set in Codex, approve the Claude Code plugin, or reload Cursor. These
actions are resumable: rerun the same command or `pulse repair`. One verified
host makes Pulse usable; every unfinished detected host stays visible as
degraded parity until repaired.

## 3. Read the real verdict

```bash
pulse doctor claude-code  # when Claude Code was detected
pulse doctor cursor       # when Cursor was detected
pulse doctor codex        # when Codex was detected
pulse home
```

Only **`Pulse <host> automatic lifecycle ready.`** means that host's automatic
continuity is ready. Confirm that doctor reports:

- production authority;
- full retrieval through the managed local embedder;
- raw transcript capture off;
- backend model calls off;
- exact native plugin activation and one observed lifecycle in that host.

Memory Home is the user surface. It shows readiness, pending cards, recent
saved memories, what a fresh task received, acknowledgement state, and token
evidence labeled `collecting`, `estimated`, or `measured`. Never invent a
savings multiplier or convert `collecting` into a claim.

## 4. Prove one real memory

Continue normal work in a verified host. Let the active harness propose one compact,
structured memory. The user must see the card in Memory Home before the review
delay begins. After save, start a fresh task in the same project and
confirm that Pulse offers the memory with provenance and retrieval reasons.
Then open Memory Home again and point to the saved-memory and continuity
receipts. Do not use the simulated demo as a substitute for this proof.

## Repair and removal

```bash
pulse repair
pulse disconnect claude-code
pulse disconnect cursor
pulse disconnect codex
```

Disconnecting removes that host integration and preserves the local vault.
Whole-vault wipe remains separately presence-bound. Never delete `~/.pulse`
or another project vault as part of routine uninstall unless the user names
that destructive action and completes its exact confirmation flow.

## Compatibility fallback

For an unsupported host, Safe Mode remains available as a separate MCP path:

```bash
npx -y @zbs-gg/pulse@preview mcp
```

Say plainly: **`Pulse MCP fallback is ready. Full retrieval is not enabled.`**
It is keyword recall, not the state-aware product, and no retrieval benchmark
claim applies to it.

## Never

- claim the public Personal install works until the published preview contains
  the signed manifest and all exact release artifacts;
- call synthetic fixtures or source-checkout success a production install;
- store raw transcripts, secrets, or path-like private material;
- import old chats without separate preview-first consent;
- push Personal memory into Git;
- print an IPC secret, browser session credential, or release signing key;
- say “saved” without a receipt or “ready” without the doctor verdict.
