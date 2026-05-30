# @nikshilov/pulse-mcp

MCP server for [Pulse](https://github.com/nikshilov/pulse) — salience-aware empathic-memory engine. Drop-in install for any MCP-compatible client (Claude Desktop, Cursor, mcp-agent, …).

State-aware empathic memory for AI companions. Pulse adds conditional emotion / state / anchor / date boosts on top of a cosine x recency x belief base, and is evaluated on public memory benchmarks (LongMemEval, LoCoMo).

## Install

```bash
npm i -g @nikshilov/pulse-mcp
```

Or run on demand via `npx @nikshilov/pulse-mcp`. The MCP server is a thin wrapper — you also need a running Pulse engine. See [pulse server quickstart](https://github.com/nikshilov/pulse#quickstart). Once Pulse runs locally on `127.0.0.1:18789`, this MCP server hands queries to it.

## Configuration

### Environment variables

| Variable | Default | Required | Meaning |
|---|---|---|---|
| `PULSE_BASE_URL` | `http://127.0.0.1:18789` | no | URL of Pulse HTTP engine |
| `PULSE_API_KEY` | _(empty)_ | yes (in production) | Sent as `X-Pulse-Key` header. Match Pulse server's `IPC_SECRET`. |

### Claude Desktop

Edit `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS) or `%APPDATA%/Claude/claude_desktop_config.json` (Windows):

```json
{
  "mcpServers": {
    "pulse": {
      "command": "npx",
      "args": ["-y", "@nikshilov/pulse-mcp"],
      "env": {
        "PULSE_BASE_URL": "http://127.0.0.1:18789",
        "PULSE_API_KEY": "your-pulse-ipc-secret"
      }
    }
  }
}
```

Restart Claude Desktop. The three Pulse tools (`pulse_recall`, `pulse_ingest`, `pulse_state`) appear under MCP tools.

### Cursor / mcp-agent / other clients

Same `command + args + env` pattern. See [MCP docs](https://modelcontextprotocol.io/quickstart) for client-specific syntax.

## Tools

### `pulse_recall(query, mode?, top_k?, user_state?)`

Retrieve memories from Pulse. Returns `event_ids` ranked by hybrid retrieval.

| param | type | default | meaning |
|---|---|---|---|
| query | string | _required_ | The user query |
| mode | `auto` \| `factual` \| `empathic` \| `chain` | `auto` | Router decides if `auto`. Force one mode by name. |
| top_k | integer 1–50 | 5 | How many events to return |
| user_state | object | _none_ | Current user biometric/mood snapshot. Fields: `mood_vector` (Plutchik-10), `sleep_quality`, `hrv`, `hr_trend`, `hrv_trend`, `stress_proxy`, `recent_life_events_7d`, `snapshot_days_ago` |

**Use `mode='empathic'`** for "how was I feeling X days ago" / "what weighs on me right now" — Pulse's strength.
**Use `mode='factual'`** for date/name/list lookups (Pulse closes 50% of the gap to Mem0's atomic-fact extraction here).
**Use `mode='chain'`** for "why did X lead to Y" causal traces.

Returned `event_ids` are Pulse-internal stable identifiers. Pair with a separate `pulse_get_event` lookup (coming in v0.2) to fetch full event text.

### `pulse_ingest(text, ts?, scope?)`

Add an observation to Pulse's memory. Pulse asynchronously extracts entities, emotion tags, and (when configured) atomic facts.

| param | type | default | meaning |
|---|---|---|---|
| text | string | _required_ | Text content to ingest |
| ts | string (ISO8601) | now | Observation timestamp |
| scope | `user` \| `assistant` \| `shared` | `shared` | Memory scope partition |

### `pulse_state()` _(stub in v0.1)_

Returns the most recent biometric / mood snapshot. Currently returns `unimplemented` until Pulse server adds `GET /state` (Phase H follow-up). Track at [github.com/nikshilov/pulse/issues](https://github.com/nikshilov/pulse/issues).

## Why Pulse?

- **State-aware retrieval as a first-class signal.** Mem0 / Graphiti / Zep / LangMem / Letta / OpenAI Memory all treat state as a free-text tag (if at all). Pulse maps `mood_vector + biometric_snapshot` to retrieval-time boosts. Same query in different states returns different top-3.
- **Hybrid factual + empathic.** Pulse keeps full session text AND extracts atomic facts, dispatching by query type. Mem0's strength on factual recall is matched in `factual` mode; Pulse's strength on stateful queries (×2.75 vs Mem0+custom_instructions) holds.
- **Open-source, transparent**, MIT-licensed. Engine in Go, MCP wrapper in TypeScript. No SaaS dependency.

## Development

```bash
cd mcp/
npm install
npm run dev    # tsx hot reload from src/
npm run build  # tsc → dist/
```

To publish a new version: bump `version` in `package.json`, run `npm publish` (the `publishConfig.access: public` field handles scoping automatically).

## License

MIT — see [LICENSE](./LICENSE).
