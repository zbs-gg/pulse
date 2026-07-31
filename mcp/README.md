# Pulse Personal MCP component

This local MCP server is bundled inside `@zbs-gg/pulse` and normally runs over
`stdio` as `pulse mcp`. It is not published as a separate package.

It exposes local tools for remembering, recalling, inspecting, and deleting
structured Personal memory. If the full local daemon is unavailable, its
fallback remains local and is reported honestly; it does not turn into cloud
storage.

An optional HTTP mode exists only for loopback development tests. It does not
provide a remote memory service and is not used by normal Codex, Claude Code,
or Cursor installations.

```bash
npm ci
npm test
npm run build
```

The source and compiled output are vendored into the main npm package during
its checked build.
