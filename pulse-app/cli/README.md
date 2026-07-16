# @zbs-gg/pulse

Host-neutral Personal support in this branch is pending the matching signed
preview publication. Do not read the current npm tag as containing these
changes unless `install-plan --json` reports `pulse.personal_install_plan.v2`.

## Copy this to your AI agent

```text
Hi. Please check whether it is safe to install Pulse:
https://github.com/zbs-gg/pulse

Pulse is a local-first memory engine for AI agents. It stores structured
memories, not raw transcripts. It should be wipeable and it should not require
backend OpenAI, Anthropic, or Cohere keys by default.

Please:
1. Read README.md, AGENTS.md, llms.txt, and docs/SECURITY_INSTALL_CHECKLIST.md.
2. Check: npm view @zbs-gg/pulse dist-tags --json
   Host-neutral Personal requires preview 0.7.0 or newer and install-plan v2.
3. Check for Apple Silicon, Node 20+, a Git project, and Claude Code, Cursor,
   or Codex.
4. Explain what Pulse writes and how I can erase it.
5. Ask me for confirmation before installing anything.
6. After I confirm, install:
   - Personal Pulse:
     npx -y @zbs-gg/pulse@preview install
     pulse doctor claude-code  # when detected
     pulse doctor cursor       # when detected
     pulse doctor codex        # when detected
     pulse home
   - Other MCP-compatible hosts:
     configure the host to run:
     npx -y @zbs-gg/pulse@preview mcp
     and tell me this is Safe Mode/fallback, not the full state-aware engine.
7. Save one visible memory, start a fresh task, and show me its Memory
   Home receipts and honest collecting/estimated/measured token state.

Important: no old-chat import without separate confirmation; no raw
transcripts; no secrets in output; stop and explain if anything looks unsafe.
```

## What Pulse is

Memory that knows what matters right now.

Pulse is a state-aware memory engine for AI agents. It installs locally,
retrieves the right remembered episode for *this* moment — not just the
closest text match — shows **why** that memory surfaced, shows what it will
tell your next agent, and wipes on one command.

This is the one package: installer/CLI (`install`, `repair`, `doctor`, `home`)
plus the MCP server (`pulse mcp`, bundled prebuilt).

Status: developer preview. Local-first across Claude Code, Cursor, and Codex.
Not production.

## Compatible harnesses

| Harness | Current support | Recommended path |
|---|---|---|
| Codex / OpenAI local agents | Native Personal plugin, lifecycle, Memory Home, and continuity; pending matching signed preview. | Install after publication |
| Claude Code | Native Personal plugin and lifecycle through the shared Core and vault; pending matching signed preview. | Install after publication |
| Claude Desktop / local Claude MCP clients | MCP-compatible Safe Mode today. | `npx -y @zbs-gg/pulse@preview mcp` |
| Cursor | Native local plugin and lifecycle through the shared Core; no Cursor CLI required; pending matching signed preview. | Install after publication |
| Windsurf | MCP-compatible Safe Mode today. | `npx -y @zbs-gg/pulse@preview mcp` |
| Gemini CLI | MCP-compatible when local MCP command servers are supported. | Agent-audited `npx -y @zbs-gg/pulse@preview mcp` |
| LangChain / CrewAI / custom agents | Framework integration surface. | Run `pulse mcp` and call its tools |
| ChatGPT app / Claude Directory / Pulse Cloud | Future surfaces, not shipped in this preview. | Not available yet |

<p align="center">
  <img src="https://raw.githubusercontent.com/zbs-gg/pulse/main/docs/assets/pulse-demo.gif" alt="pulse demo: one question, three user states, three different memories — with the reason on every line" width="820">
</p>


## Install — Personal Pulse

```bash
npx -y @zbs-gg/pulse@preview install
pulse doctor claude-code  # when detected
pulse doctor cursor       # when detected
pulse doctor codex        # when detected
pulse home
```

The Personal path does not require Go, Python, Make, Docker, a model API key,
or manual configuration editing. The wizard verifies the complete signed
release, resumes interrupted downloads, provisions Core once, activates every
detected compatible native plugin, and opens Memory Home. It stops rather than silently falling back when production
authority or full retrieval is unavailable.

Only `Pulse <host> automatic lifecycle ready.` means that host is ready. The
real proof is one normal memory shown before save and then offered to a fresh
task with provenance, retrieval reasons, and receipts in Memory Home.

### What `pulse demo` proves

It seeds an isolated, clearly-labeled SIMULATED corpus (never your data) and
shows the three things generic memory tools don't:

1. **Same query, different user state → different memory.** One question in
   three states (drained / restored / angry) — different episodes surface,
   each line with the reason: `state x1.15 · anchor x1.05 · emotion x1.15`.
2. **Old anchors beat recent noise** — and the score breakdown shows why.
3. **What your next agent gets** — the continuity pack as it will be
   injected into the next session.

`pulse demo --clean` removes the whole demo store.

`pulse demo` remains an optional isolated explainer. It cannot substitute for
the real first-memory proof above.

## Safe Mode — fallback, not the product

```bash
claude mcp add pulse -- npx -y @zbs-gg/pulse@preview mcp
```

For machines that can't run the engine: structured local memory with
inspect/wipe and keyword recall. No benchmark claim applies to this mode.

## Trust boundaries

- Host-extracted structured capsules only — raw transcript capture is off by
  default, and the store rejects transcript/secret/path-like content.
- No backend model API keys by default; no backend LLM calls.
- No old-chat import by default — ingest is explicit and consent-first.
- Personal memory is local and project-bound; it never enters Git without a
  separate exact shared-memory approval.
- Disconnect preserves memory: use `pulse disconnect claude-code`,
  `pulse disconnect cursor`, or `pulse disconnect codex`. Whole-vault wipe is
  separately protected by fresh macOS presence.

Docs, agent install script, and source: https://github.com/zbs-gg/pulse
(see `AGENTS.md` — written for AI agents asked to vet this install).

## Team remote boundary

Team remote is a separate synthetic foundation, not another Local Preview
install target. The CLI never sends the local `X-Pulse-Key` to a configurable
remote base; that header is allowed only for an exact loopback host. The
default-off Owner operator contour uses a separate least-privilege
`pulse:owner` credential, fresh browser step-up, and exact two-phase HTTPS
approval for member and project administration; it never upgrades the
installed read-only credential. A root-controlled IdP profile, accepted Owner
enrollment, and a verified deployment are still required, so this is not a
claim that real team onboarding is live in the public preview. See
[`docs/TEAM_REMOTE_PILOT.md`](../../docs/TEAM_REMOTE_PILOT.md).

Before using the Owner CLI, a deployment operator must create the exact
root-owned, operator-readable profile documented in
[`deploy/team/README.md`](../../deploy/team/README.md) at
`/etc/pulse-team/team-owner-profile.json`. The profile contains no client secret;
it pins the public Owner client, expected human subject, issuer endpoints, exact
`/mcp` audience, and scopes
`openid offline_access pulse:connect pulse:owner`. The bounded operator flow
then begins as follows:

```bash
pulse team owner login --profile /etc/pulse-team/team-owner-profile.json
# A human operator installs the emitted request into the protected deployment registry.
pulse team owner member create --profile /etc/pulse-team/team-owner-profile.json --issuer https://issuer.example/ --subject user-id --role member
pulse team owner binding create --profile /etc/pulse-team/team-owner-profile.json --issuer https://issuer.example/ --subject user-id --client-id codex-user
pulse team owner project create --profile /etc/pulse-team/team-owner-profile.json --name "Project Atlas"
pulse team owner project grant --profile /etc/pulse-team/team-owner-profile.json --project-id project_... --principal-id principal_... --access write
pulse team owner project revoke-grant --profile /etc/pulse-team/team-owner-profile.json --grant-id grant_...
```

The first login emits a separate enrollment request. This package does not yet
install or approve that request: the deployment operator must verify its digest
and atomically update the service-owned registry. A second login reports that
acceptance is unverified rather than claiming readiness. Every mutation starts
its own platform-WebAuthn browser flow; the ID-token nonce is bound to the exact
canonical action and random challenge, and the resulting assertion ID is
consumed durably once. Mutation commands return opaque IDs and audit metadata,
never the supplied subject, issuer, or project name. The composed acceptance
gate exercises the production authorization core with synthetic HTTP,
CLI-built approval bytes, DPoP, and signed tokens; it does not claim external
TLS, live IdP behavior, protected registry installation, native helper
execution, or a packaged fresh-machine CLI install.

AGPL-3.0 — see [LICENSE](LICENSE). Commercial dual-license available (`COMMERCIAL.md`).
