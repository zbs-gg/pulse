# Pulse

Pulse owns all Pulse-related code, MCP packages, review bundles, screenshots,
archives, and local proof artifacts.

Nothing Pulse-specific should live directly in the Garden workspace root.

## Current Preview

Pulse MCP Preview v0.4.2 is Claude Code-first and agent-first.

Recommended entry:

1. Give your AI agent `docs/INSTALL_WITH_AGENT.md`.
2. The agent reads `AGENTS.md` and `docs/SECURITY_INSTALL_CHECKLIST.md`.
3. The agent shows `pulse install-plan claude-code --json`.
4. The agent asks for confirmation.
5. The agent runs `pulse init claude-code --yes`, then `pulse doctor`,
   `pulse demo`, and `pulse viewer`.

Manual install remains available, but it is the fallback:

```bash
npx @zbs-gg/pulse@preview init claude-code
```

Availability note: the `@preview` npm commands are valid only after the preview
packages are published. Before sharing a recipient flow, verify with
`npm view @zbs-gg/pulse dist-tags`. If npm returns 404, use the local
source/tarball/review-bundle path and say explicitly that the public npm path is
not available yet.

`@zbs-gg/pulse` is the CLI installer and local preview wrapper.
`@zbs-gg/pulse-mcp` is the MCP server package used by Claude Code and later
hosts; it is not the whole product.

## Material Graph Direction

Pulse is moving toward a native Material Graph:

```text
Material Graph + Salience Overlay + Continuity Pack
```

Pulse does not use Graphify internally. Graphify is only a reference and
external benchmark for packaging, demos, and review discipline. The native
Pulse direction is source-backed graph memory that can feed continuity/resume
without claiming autonomous prioritization or production readiness.

See:

- `docs/PULSE_GRAPHIFY_BOUNDARY.md`
- `docs/PULSE_MATERIAL_GRAPH_V0.md`
- `docs/PULSE_MATERIAL_GRAPH_CURRENT_STATE.md`
- `docs/PULSE_MATERIAL_GRAPH_STORIES.md`
- `docs/PULSE_MATERIAL_GRAPH_PROSHA_POINT_AUDIT.md`

## Layout

- `pulse-app/` - Pulse local app, daemon, storage, server, CLI-adjacent app code.
- `mcp/` - Pulse MCP npm package.
- `artifacts/` - Pulse screenshots, viewer captures, demo pages, and local proof.
- `review-bundles/` - Pulse MCP / Pulse review handoff bundles.
- `archive/` - old Pulse backups, public-clean snapshots, and legacy exports.

## Rule

When creating a Pulse artifact, choose the destination inside this folder first:

- MCP package/review bundle: `review-bundles/mcp/`
- Prosha bundle for Pulse: `review-bundles/proshka/`
- Screenshots and browser captures: `artifacts/screenshots/<topic-or-date>/`
- Prototype/demo HTML: `artifacts/<topic>/`
- Old snapshots/backups: `archive/`
- Pulse app code: `pulse-app/`
- Pulse MCP code: `mcp/`
