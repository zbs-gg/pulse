# One store, many harnesses — capture & live extraction

**Status:** working on branch `feat/one-store-multiharness-capture` (2026-06-23).
This is the design + decision record for making Pulse stitch one topic's
scattered facts across many chats **and** many harnesses (Claude Code, Codex,
Claude.ai chat) into one local graph — and capture it automatically.

> Why this doc: so future sessions see *what* we chose and *why*, instead of
> re-deriving it. Decisions below are backed by an own-eval on our data, not
> public benchmarks.

---

## The claim we had to make true

"Pulse extracts and **combines** scattered facts about a single topic from one
or several chats, across one or several harnesses."

A read-only audit of the engine (file:line in the PR notes) found:

- **Cross-chat within one store — TRUE.** Retrieval has no chat/session
  dimension at all (`events` has no `session_id`; capsules none). A query spans
  the whole store by default. The graph layer (`store/semantic_delta.go`)
  resolves entities across chats by normalized (Cyrillic-aware) key + alias
  overlap and `MAX`-merges facts/relations — real consolidation, not top-k.
- **Multi-harness — TRUE in the data model, but capture was not wired.** One
  shared `memory_capsules` / graph store; `source_host` is a tag, never a
  retrieval filter. So if capsules from different harnesses *land*, they stitch
  automatically. Only Claude Code had live capture; Codex/Claude.ai did not.
- **Local daemon vs cloud — two separate stores, no sync.** Claude.ai talked to
  a separate hosted MCP; local Claude Code wrote to `~/.pulse`. Not one graph
  end-to-end.

So the engine already *combines*; the work was **wiring capture into ONE
store**, plus an automatic **fact-extraction** path.

---

## Decision 1 — the one store is LOCAL (`~/.pulse`)

Pulse is local-first / no-egress by identity. The single source of truth is the
local daemon (`127.0.0.1:18789`, SQLite `~/.pulse/pulse.db`). Everything funnels
there. Cloud-as-truth was rejected: it would send memory off-machine, against
the whole pitch.

## Decision 2 — embedder is local bge-m3 (MLX), not Cohere

Full retrieval needs an embedder. The Go engine spawns a Python helper
(`internal/embed/local.go` → `mlx_embed_helper.py`) loading **bge-m3** in MLX —
free, on-device, no egress. Cohere stays a documented fallback (only if a key is
present). Wiring (the 3 env vars + venv + model) is in
[[pulse-local-full-retrieval-bringup]] (memory) and the launchd units below.

## Decision 3 — extraction backend is local **qwen3-30b** (own eval)

The live extractor turns a session transcript into a graph delta. *Which model
extracts best?* We did not trust leaderboards — we ran an own-eval on **8 real
transcripts** (Claude Code + Codex), one shared local judge (`gemma-3-27b-it`,
a non-candidate so no self-bias), scoring completeness / correctness / salience
/ structure / domain. Harness: `~/elle/eval/eval_extract.py`; corpus builder:
`build_extract_corpus.py`; report: `~/elle/eval/results/extract-2026-06-23.md`;
ledger: `~/elle/eval/ledger.jsonl` (`eval: "extract-backend"`).

| backend | quality | avg lat / transcript | $/session (est) |
|---|---|---|---|
| **qwen3-30b-a3b-instruct (local)** | **0.945** | **38.6s** | **free** |
| sonnet (claude -p / Max) | 0.930 | 54.6s | ~$0.054 |
| gemma-4-e4b (local) | 0.925 | 43.9s | free |
| mistral-24b (local) | 0.918 | 122.4s (too slow) | free |
| haiku (claude -p / Max) | 0.915 | 54.8s | ~$0.038 |
| gemma-4-26b (local) | 0.238 | 62.4s | free (breaks JSON) |

**Pick: `qwen3-30b-a3b-instruct` via LM Studio** — best quality, fastest, free.

Honest reads:
- **Paying buys nothing here.** Sonnet (0.930) ≤ free qwen3 (0.945), at
  ~$0.04–0.05 per session — on per-session passive capture that is real money
  for zero gain. Local wins on quality, cost, and speed at once.
- gemma-4-26b is a reasoning model that **breaks JSON** (valid output ~2/8) →
  honest 0-penalty → 0.238. Unusable for extraction.
- Caveat: N=8 is directional, not bedrock. The judge is generous — working
  models cluster 0.915–0.945 (within noise), so "qwen3 > sonnet" is soft. The
  *robust* signals are: 26b breaks, mistral is too slow, **local == API**.
  `salience` is ~4.0 for everyone — a shared weakness, not a differentiator;
  worth a prompt iteration later.

## Decision 4 — per-harness wiring into the one local store

| harness | how it reaches `~/.pulse` | capture |
|---|---|---|
| Claude Code | project hooks → daemon (`/continuity/*` + `/graph/delta`) | automatic |
| Codex | `codex mcp add pulse` (daemon-mode MCP → `:18789`) | on-demand now; passive hooks = TODO |
| Claude.ai chat | `pulse mcp --http` (bearer) behind **tailscale Funnel** → daemon | on-demand (MCP); no hook surface on claude.ai |

Claude.ai is a hosted web app — it cannot reach `127.0.0.1`, so the bearer-gated
MCP-HTTP is exposed publicly via tailscale Funnel
(`https://node.<tailnet>.ts.net/mcp`). The raw daemon is **not** exposed; only
the bearer-gated MCP. The dead `*.loca.lt` connector was the prototype of this.

## Decision 5 — durability via launchd

Daemon + MCP-HTTP run as user LaunchAgents (`RunAtLoad` + `KeepAlive`) so
capture survives reboot/crash. The bge-m3 model was **copied to local disk**
(`~/.pulse/models/bge-m3-mlx-fp16`) so the daemon no longer depends on the
external Celeste volume being mounted.

## Decision 6 — live semantic-delta extraction (the auto "combine")

On session-stop the host flattens its transcript, **qwen3-30b (local)** extracts
a knowledge-graph delta, and we `POST /graph/delta`. The daemon merges entities
across sessions and indexes events for retrieval. **Raw transcript never leaves
the host** — only the structured delta (the daemon also rejects
transcript/secret/path-like payloads). Extraction runs **backgrounded** so
session-end never blocks on the ~40s call.

Mapping gotchas (extractor output → `store.SemanticDelta`), learned the hard way:
- `client_id` must match `^[A-Za-z0-9][A-Za-z0-9._:-]{1,95}$` (ASCII) → we slug
  to ASCII + a hash suffix; the daemon merges by `canonical_name` anyway.
- entity `kind` is restricted to 10 values
  (person/place/project/org/product/community/skill/concept/thing/event_series);
  richer kinds (ai_persona, fictional_character, …) are mapped down.
- text fields are rejected if they look like a transcript or contain
  secret/path markers (`secret`, `/users/`, `token=`, `api_key`, `sk-`, …) → we
  pre-filter the same markers and drop/blank offending fields.
- `events.sentiment` is a **string** label, not the extractor's float.

## Decision 7 — the delight layer (fun-facts + statusline)

- **Status line:** `~/.pulse/bin/pulse-statusline-segment.sh` appends a Pulse
  micro-stat to the global Claude Code status line — `🌱 <capsules> <breath> on`
  — non-blocking (short cache + async refresh), breath glyph cycles with the
  clock, silent if the daemon is down.
- **Fun-facts:** `~/.pulse/bin/pulse-funfacts.mjs` renders five **honest**
  facts from real store data (stats, a derived pattern, a surfaced memory, a
  real graph node, + a date-seeded "fortune roll"), wired into session-start.
  Empty sources degrade gracefully ("fills once the extractor lands"); nothing
  is fabricated.

---

## Artifact map

Repo (this branch):
- `pulse-app/scripts/pulse_live_extract.py` — transcript → qwen3 → SemanticDelta → `/graph/delta` (stdlib only).
- `docs/one_store_multiharness_capture.md` — this doc.

Outside the repo (host config — not committed):
- `~/.pulse/bin/` — `start-daemon.sh`, `start-mcp-http.sh`, `pulse-extract-hook.sh`, `pulse-funfacts.mjs`, `pulse-statusline-segment.sh`, `mlx_embed_helper.py`.
- `~/.pulse/embed-venv/` — uv venv (mlx, mlx-embeddings, numpy) for the embedder.
- `~/.pulse/models/bge-m3-mlx-fp16` — local embedder model.
- `~/Library/LaunchAgents/com.nikshilov.pulse-{daemon,mcp-http}.plist` — durability.
- `~/.pulse/{secret.key, remote-bearer.txt}` — daemon IPC key + claude.ai bearer (0600).
- project `.claude/settings.local.json` — Claude Code hooks (capture + funfacts + extract).
- `~/elle/eval/eval_extract.py` + `results/` + `ledger.jsonl` — the extraction eval.

## Operate / revert

```bash
# health
curl -s -H "X-Pulse-Key: $(cat ~/.pulse/secret.key)" http://127.0.0.1:18789/memory/status
curl -s http://127.0.0.1:8787/health
tailscale funnel status
node ~/.pulse/bin/pulse-funfacts.mjs

# stop / revert
launchctl bootout gui/$(id -u) ~/Library/LaunchAgents/com.nikshilov.pulse-daemon.plist
launchctl bootout gui/$(id -u) ~/Library/LaunchAgents/com.nikshilov.pulse-mcp-http.plist
tailscale funnel --https=443 off
codex mcp remove pulse
# Claude Code project hooks/MCP: pulse disconnect claude-code (or edit .claude/settings.local.json)

# re-run the extraction eval (own-eval on our data)
cd ~/elle/eval && python3 build_extract_corpus.py 8 && python3 eval_extract.py --backends pulse-set --n 8 --judge gemma-3-27b-it-qat
```

## Known limits (honest)

- Extraction eval N=8; the winner edge over runner-ups is within judge noise —
  the firm conclusions are "26b breaks JSON / mistral too slow / local == API".
- `/context/query` semantic retrieval over freshly-extracted events is
  query-sensitive; the graph (entities/facts/relations) populates and
  consolidates regardless. Retrieval quality is a separate axis.
- Live extraction depends on LM Studio having qwen3-30b loadable; it JIT-loads
  on the first call. Backgrounded, so it never blocks session-end, but a session
  with LM Studio down simply skips capture (logged, not fatal).
- Codex passive (hook-based) capture is not wired yet — only on-demand MCP.
- Daemon is durable; the **tunnel** stays up via tailscaled. If the bearer
  leaks, rotate `~/.pulse/remote-bearer.txt` and restart the MCP-HTTP agent.
