# @zbs-gg/pulse-mcp

MCP server for Pulse host-extracted memory and continuity. Claude Code is the
first supported install target, but the product surface is Pulse MCP.

Pulse keeps the thread across AI chats.

Status: Pulse MCP package for Pulse MCP Preview. This package is shareable
with technical friends and partner/investor-adjacent reviewers as a Claude
Code-first preview. Since v0.4.0 it runs standalone: when no local Pulse Go
daemon is reachable, it stores memory in a built-in lite local store, so a
plain MCP install works with zero extra setup. It is still not a consumer
product or a store-safe public connector build yet.

Version boundary for this bundle:

- `@zbs-gg/pulse` v0.5.0 is THE package: MCP server (`pulse mcp`) + installer
  CLI in one, claude-mem style. Users install only this.
- `@zbs-gg/pulse-mcp` v0.4.0 is the internal server component (this
  directory). It is bundled prebuilt inside `@zbs-gg/pulse`; installing it
  directly is a low-level/dev path, not the recommended one.

## Zero-Config Install (Recommended Start)

One command, no daemon, no API keys:

```bash
claude mcp add pulse -- npx -y @zbs-gg/pulse@preview mcp
```

Availability check first: this path needs the `preview` dist-tag of
`@zbs-gg/pulse` to resolve to v0.5.0 or newer
(`npm view @zbs-gg/pulse dist-tags`). Older published versions do not carry
the `mcp` subcommand or the standalone lite store.

That is the whole install. On the first tool call Pulse creates a local store
at `~/.pulse/standalone/store.json` (override the root with `PULSE_DATA_DIR`)
and all eight tools work: `pulse_remember`, `pulse_recall`, `pulse_resume`,
`pulse_graph_delta`, `pulse_status`, `pulse_context_query`, `pulse_forget`,
`pulse_wipe`.

While the store is empty, `pulse_status` and `pulse_resume` return a
`first_run` block — a guided 3-minute lived demo written for the host agent:
save one real thing the user is working on, then open a different
Pulse-connected session or agent and ask "where did we leave off?". The agent
is the onboarding UI; no separate setup walkthrough is needed.

Engine selection (`PULSE_MCP_MODE`, default `auto`):

- `auto` — use the local Pulse daemon when it answers at `PULSE_BASE_URL`;
  otherwise fall back to the standalone lite store. A daemon that has answered
  in the current process is never silently downgraded.
- `daemon` — always require the daemon (pre-0.4.0 behavior).
- `standalone` — never touch the daemon, lite store only.

What the lite engine does NOT have: the full retrieval engine (typed graph
scoring, emotional retrieval), the local viewer, and the Claude Code lifecycle
hooks with automatic resume injection. For those, upgrade to the full local
engine later:

```bash
npx -y @zbs-gg/pulse@preview init claude-code
```

The standalone store keeps items in the daemon's export item shape, so
upgrading does not strand your memories: wrap the `items` array from
`store.json` as `{"schema": "pulse.memory_capsule.v1", "items": [...]}` and
feed it to `pulse import --file`.

Pulse does not call an LLM backend by default. Claude Code creates a minimal
`pulse.memory_capsule.v1` in tool arguments, and Pulse stores, recalls, resumes,
deletes, and wipes structured memory. Export/import and archive migration stay
CLI-only in v1.

For graph memory, the product direction is host-extracted too: the active host
model will call `pulse_graph_delta` with `pulse.semantic_delta.v1`, and Pulse
will validate/materialize the graph without backend LLM calls by default.
Reviewed import insights stay in a separate continuity lane: `review_insights`
and user-marked `active_threads` can appear in `pulse_resume`, but they are not
generic mood/state signals and they are not an autonomous prioritization claim.

## Preview v0.4.2 Install

For the partner preview, use the agent-first installer from the CLI package.
The MCP package is only the host bridge.

Availability rule: before using any `@zbs-gg/pulse@preview` or
`@zbs-gg/pulse-mcp@preview` command, verify that the package is published:

```bash
npm view @zbs-gg/pulse dist-tags
npm view @zbs-gg/pulse-mcp dist-tags
```

If npm returns 404, use the provided source bundle, packed tarball, or local
checkout for the rehearsal and state that the public npm path is not ready yet.

Agent-first path:

1. Read `pulse/docs/INSTALL_WITH_AGENT.md`.
2. Ask your agent to inspect the repo and show `pulse install-plan claude-code --json`.
3. Confirm only after the agent explains what will be written.
4. Install with `pulse init claude-code --yes`.

Manual source-bundle fallback:

```bash
cd pulse/pulse-app
./scripts/preview-install-claude-code.sh
```

It builds the local daemon, builds this MCP package, starts Pulse on
`127.0.0.1`, configures Claude Code MCP + hooks, saves the first install memory
when the daemon is reachable, and prints the local viewer URL.

See [README_DEV_PREVIEW.md](README_DEV_PREVIEW.md),
[INSTALL_WITH_AGENT.md](../docs/INSTALL_WITH_AGENT.md), and
[INSTALL_DEV_SETUP.md](docs/developer-preview/INSTALL_DEV_SETUP.md).

## Manual MCP Install

```bash
npm i -g @zbs-gg/pulse@preview
pulse mcp
```

Or run on demand:

```bash
npx -y @zbs-gg/pulse@preview mcp
```

(`@zbs-gg/pulse-mcp` still exists as the raw server package for low-level/dev
use; everything below applies to it equally.)

The MCP server prefers a local Pulse HTTP engine at `http://127.0.0.1:18789`
and falls back to the built-in standalone lite store when no daemon answers
(see Zero-Config Install above).

To use the full engine, start the local daemon before connecting a host:

```bash
go run ./cmd/pulse -addr 127.0.0.1:18789 -data-dir ~/.pulse
```

No backend model key is required for host-extracted memory. Claude Max,
ChatGPT Plus, or another host subscription can power extraction inside the
active host chat, but Pulse backend cannot spend that subscription directly.
Backend API calls are opt-in only through later BYOK/local/Pulse Cloud modes.

## Try It In 5 Minutes

Use the preview installer above when possible. The manual path below is for
debugging the low-level MCP package.

1. Start the local Pulse daemon:

   ```bash
   go run ./cmd/pulse -addr 127.0.0.1:18789 -data-dir ~/.pulse
   ```

2. Read the local IPC secret:

   ```bash
   PULSE_API_KEY="$(cat ~/.pulse/secret.key)"
   ```

   `PULSE_API_KEY` is for the MCP package process. The Pulse CLI hooks read the
   same secret through `PULSE_DATA_DIR` and `secret.key`, so custom hook smoke
   tests must point `PULSE_DATA_DIR` at the daemon's data directory.

3. Install the package:

   ```bash
   npm i -g @zbs-gg/pulse@preview
   ```

4. Add Pulse to Claude Code:

   ```json
   {
     "mcpServers": {
       "pulse": {
         "command": "pulse",
         "args": ["mcp"],
         "env": {
           "PULSE_BASE_URL": "http://127.0.0.1:18789",
           "PULSE_API_KEY": "paste-your-local-secret-here"
         }
       }
     }
   }
   ```

5. Ask Claude Code:

   ```text
   Remember in Pulse: Atlas must not own the People Graph; Pulse owns portable continuity memory.
   ```

6. In a fresh chat, ask:

   ```text
   What did we decide about Atlas and the People Graph?
   ```

For this preview, the host model must call `pulse_remember`,
`pulse_graph_delta`, or `pulse_resume`.

Current proof boundary:

- a clean Pulse-only Claude Code two-session proof recovered one architectural
  decision from Pulse Resume without the user re-explaining it;
- Context Restore Bench passed 8/8 structured continuity cases on a clean local
  daemon with backend LLM off and raw capture off;
- this is still developer-preview evidence, not a full Auto Continuity v1
  production claim.

## Claude Code

The `@zbs-gg/pulse` CLI package provides the v0.4.2 developer-preview installer.
Do not treat it as a production consumer installer yet:

```bash
npx @zbs-gg/pulse@preview init claude-code
npx @zbs-gg/pulse@preview install-plan claude-code --json
npx @zbs-gg/pulse connect claude-code --remote-control
```

`--remote-control` is the mobile-friendly Claude Code path: type from Claude
mobile or `claude.ai/code`, while the local Claude Code process still has Pulse
MCP and hooks.

Manual config uses the same MCP command:

```json
{
  "mcpServers": {
    "pulse": {
      "command": "npx",
      "args": ["-y", "@zbs-gg/pulse@preview", "mcp"],
      "env": {
        "PULSE_BASE_URL": "http://127.0.0.1:18789",
        "PULSE_API_KEY": "your-pulse-ipc-secret"
      }
    }
  }
}
```

Both env vars are optional since v0.4.0: `PULSE_BASE_URL` defaults to
`http://127.0.0.1:18789`, and when `PULSE_API_KEY` is unset the MCP server
reads `~/.pulse/secret.key` if it exists (so a daemon installed by the CLI
works without pasting secrets). With neither a daemon nor a secret, the
standalone lite store takes over.

## Streamable HTTP MCP

For ordinary Claude Chat/mobile connector experiments, `@zbs-gg/pulse-mcp`
can also run as a Streamable HTTP MCP server. The local/tunnel preview mode
uses a static bearer gate:

```bash
PULSE_BASE_URL=http://127.0.0.1:18789 \
PULSE_API_KEY=your-pulse-ipc-secret \
PULSE_REMOTE_BEARER=dev-token \
npx -y @zbs-gg/pulse@preview mcp --http --port 8787
```

Development endpoint:

```text
http://127.0.0.1:8787/mcp
```

For a local preview behind a tunnel, keep the bearer gate and bind publicly:

```bash
PULSE_REMOTE_BEARER=dev-token \
npx -y @zbs-gg/pulse@preview mcp --http --host 0.0.0.0 --port 8787
```

`--http` requires `PULSE_REMOTE_BEARER` by default and refuses authless public
binds. Static bearer auth is still only a development guard. A public Claude
Chat connector must use public HTTPS plus the host's required OAuth/auth review
path. The important architecture is unchanged: the active Claude harness
extracts memory and graph deltas with its own model/subscription; Pulse stores
and validates them without backend LLM calls by default.

For the Claude custom connector auth shape, run OAuth protected-resource
readiness mode:

```bash
PULSE_REMOTE_PUBLIC_BASE_URL=https://pulse.example.com \
PULSE_REMOTE_AUTH_ISSUER=https://auth.example.com \
npx -y @zbs-gg/pulse@preview mcp --http --host 0.0.0.0 --port 8787
```

This exposes:

```text
https://pulse.example.com/mcp
https://pulse.example.com/.well-known/oauth-protected-resource/mcp
```

In this mode `initialize` and `tools/list` are reachable, but protected
`tools/call` requests without a bearer token receive a transport-level
`401 WWW-Authenticate` challenge with `resource_metadata` and
`pulse:read pulse:write` scopes. Token issuance and production token
verification still belong to the real OAuth/auth layer in front of public
distribution.

For the first private Claude custom connector smoke test, enable the built-in
dev OAuth loop:

```bash
PULSE_REMOTE_PUBLIC_BASE_URL=https://pulse.example.com \
PULSE_REMOTE_AUTH_ISSUER=https://pulse.example.com \
PULSE_REMOTE_OAUTH_DEV=1 \
npx -y @zbs-gg/pulse@preview mcp --http --host 0.0.0.0 --port 8787
```

This serves authorization-server metadata, dynamic client registration,
authorization-code redirects, PKCE S256 token exchange, refresh-token exchange,
and in-memory bearer validation. It auto-consents and is only for private
smoke tests behind a tunnel or temporary domain. Do not use it as production
auth.

Then verify the connector path end-to-end:

```bash
npx -p @zbs-gg/pulse-mcp@preview pulse-mcp-claude-smoke -- \
  --base https://pulse.example.com \
  --thread pulse-live-connector-smoke
```

The smoke command performs the dev OAuth flow, connects to Streamable HTTP MCP,
checks `pulse_status`, writes a host-extracted `pulse_graph_delta`, and verifies
that `pulse_resume` returns the saved thread. It does not print OAuth access or
refresh tokens.

The Cloudflare Worker + D1 connector in this repo is narrower than the local Go
engine. For hosted v0, `PULSE_OWNER_CODE` is required and `pulse_graph_delta`
accepts continuity checkpoints only. Full graph nodes, edges, facts, events,
archive migration, and relationship review stay local until the Worker validator
matches the local Go validator.

OAuth modes require HTTPS values for `PULSE_REMOTE_PUBLIC_BASE_URL` and
`PULSE_REMOTE_AUTH_ISSUER`. If Pulse MCP is deployed behind a verified auth
proxy that validates tokens before forwarding requests, set both
`PULSE_REMOTE_AUTH_PROXY_MODE=1` and `PULSE_REMOTE_TRUST_AUTH_HEADER=1`.
Never set those for a raw public tunnel.

## Tools

### `pulse_remember`

Saves a minimal, user-approved `pulse.memory_capsule.v1`.

Rules enforced by the server:

- no raw full transcript
- no arbitrary chat history
- no `raw_input_included=true`
- no missing `redacted_summary`
- no missing `privacy_tier`
- no unsupported host/source scope

### `pulse_recall`

Returns structured memory summaries and `pulse:<id>` evidence refs.

### `pulse_context_query`

Uses the existing Pulse `pulse.context.v1` projection for richer graph/retrieval
contexts when the local retrieval engine is configured.

This is a local/developer-preview tool, not a store-safe public connector
surface yet. Public builds should hide or narrow it until `include_trace`,
free-form `user_state`, and privacy controls are constrained.

### `pulse_graph_delta`

Writes a host-extracted `pulse.semantic_delta.v1` graph delta. Use it when the
current host model has identified durable semantic nodes, relations, facts,
events, decisions, open loops, do-not-repeat items, or emotional/state anchors.

In the Cloudflare Worker connector, this tool is intentionally continuity-only
for now. Send full graph payloads to the local Pulse engine, not the hosted v0
Worker.

Rules enforced by the server:

- no raw full transcript
- no `raw_input_included=true`
- no secrets, credentials, local paths, or unsafe refs
- RFC3339 source timestamp required
- unknown graph refs rejected
- bounded nodes, edges, facts, events, and continuity fields

### `pulse_resume`

Returns a small startup continuity block for the current thread:

- where we left off
- active decisions
- open loops
- do-not-repeat
- relevant emotional/state context
- suggested next step
- evidence refs

Default budget is 1200 tokens; hard max is 2000.

### `pulse_status`

Shows storage mode, billing mode, host, backend LLM state, raw capture state,
schema, item count, and last write. Filesystem paths are redacted in MCP
responses. Use the CLI for verbose local diagnostics.

### `pulse_forget` / `pulse_wipe`

`pulse_forget` deletes one host-extracted memory capsule item by id. It does
not delete continuity checkpoints or graph rows.

`pulse_wipe` deletes all host-extracted memory capsules, continuity
checkpoints/observations/sessions/threads, and host-extracted graph rows after
explicit confirmation.

## CLI-Only Trust Controls

Export/import are intentionally not exposed as MCP v1 tools. Use
`@zbs-gg/pulse` CLI for portable backup/import flows after local confirmation.
Archive migration is also CLI/local-viewer only: `pulse migrate concierge`,
`pulse migrate request`, `pulse migrate wait-latest`, `pulse migrate preview`, and
`pulse migrate commit --confirm "import pulse graph"`. Public MCP should not
advertise an "ingest all chats" tool. Treat migration/viewer as a product
extension, not as part of the evaluated retrieval claims; see
`docs/archive-migration-paper-boundary.md` in the repository root.

Archive commit defaults imported graph items to `private`. Use
`--privacy sensitive` or `--privacy normal` only as an explicit local/operator
override for developer smoke tests.

Hook-only local-auto capture (`pulse_observe` / `pulse_checkpoint`) is not
advertised as a generic remote MCP capture surface. The backing HTTP server
rejects checkpoint/observe writes unless Pulse is running in `local-auto` mode.

This package should not claim full Auto Continuity until a full Claude Code
model-level two-session run completes: Session A stores a concrete
decision/open loop, Stop writes it, Session B receives `pulse_resume`, and
Claude continues without re-explanation. The current developer-preview proof
may show the local hook-level Stop -> SessionStart resume flow separately.

## Development

```bash
cd mcp/
npm install
npm run dev
npm run build
```

## License

MIT
