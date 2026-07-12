# mcp/ — internal MCP server component

This directory is the source of the MCP server that ships **prebuilt inside
the [`@zbs-gg/pulse`](https://www.npmjs.com/package/@zbs-gg/pulse) package**
and runs via `pulse mcp`. It is not published separately; the old
`@zbs-gg/pulse-mcp` npm package is deprecated.

For the product story, install paths, and trust boundaries see the
[repo root README](../README.md) and [AGENTS.md](../AGENTS.md).

## What it is

A stdio/HTTP MCP server exposing the Pulse tools (`pulse_remember`,
`pulse_recall`, `pulse_resume`, `pulse_graph_delta`, `pulse_status`,
`pulse_context_query`, `pulse_forget`, `pulse_wipe`).

Engine selection (`PULSE_MCP_MODE`, default `auto`):

- a running local Pulse daemon (the Local Preview engine) → thin proxy;
- no daemon → **Safe Fallback mode**: structured local memory at
  `~/.pulse/standalone/store.json` with keyword recall. The first_run block
  tells the host agent plainly that this is the fallback, not the Pulse
  engine, and routes to the Local Preview demo. No benchmark claims apply
  to fallback mode.

## Develop

```bash
npm ci
npm run build   # tsc → dist/
npm test        # stdio/http/standalone suites
```

The `@zbs-gg/pulse` package vendors `dist/` at pack time
(`pulse-app/cli/scripts/prepare-preview-vendor.mjs`).

HTTP/remote modes (`--http`, bearer/OAuth dev loop) are development
surfaces for connector experiments — see `docs/developer-preview/` for
boundaries and safe claims.

## Team remote foundation

The team-remote registry is a separate, fail-closed MCP surface. It advertises
only versioned `pulse_team_*` tools, resolves the current OAuth principal and
client binding on every request, and forwards a body-bound signed assertion to
the loopback Go daemon. It never falls back to the standalone store.

Owner administration is intentionally absent from the MCP registry. Browser
step-up, membership and grant changes, revocation, shared deletion, and public
activation use a separate approval surface. This is a synthetic verification
foundation, not a production connector; see
[`docs/TEAM_REMOTE_PILOT.md`](../docs/TEAM_REMOTE_PILOT.md).
