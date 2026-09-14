# Pulse Personal

The 0.8.3 installer downloads signed runtime and model assets from the public
GitHub release in `zbs-gg/pulse`. GitHub may redirect downloads to
`release-assets.githubusercontent.com`; signatures, exact sizes and SHA-256
digests are still checked before activation. No paid release storage service
is required. Personal memory remains on the local machine.


Pulse is memory for AI tools. It gives coding agents the knowledge they need at
the moment they need it, and stays silent when nothing relevant is found.
The 0.8.3 preview preserves complete meaningful moments: multiple feelings,
their names, causes, and the distinction between user-stated and inferred
emotions. Every submitted part receives a durable receipt. An interrupted
response can be replayed without duplication, and accepted writes recover
after a daemon restart.

The preview targets Apple Silicon macOS with Codex, Claude Code, Cursor, and
OpenCode 1.18.x. Stable `0.7.2` remains the default. The BB integration lives in
[`integrations/bb`](./integrations/bb); it uses the same local engine and adds
`pulse_moment` for reading all linked parts. Native hosts use `pulse_memory`
with a `moment_id` for the same paginated read.

The OpenCode loader stays inert outside a signed Pulse project. Its optional
`--fun-facts small-model` mode sends at most six approved short facts to the
configured smaller model once per session; it is off by default.

The publication workflow installs the exact signed archive on a clean Apple
Silicon runner and requires a real BGE-M3 semantic query before publishing npm
and finalizing the matching GitHub prerelease. That check does not establish one-day
owner-machine acceptance or production readiness. See the
[release notes](./docs/releases/v0.8.3.md) for this release's scope.

The first frozen large-vault baseline on 2026-08-12 did not pass its combined
practical bar. On a copy containing 76,795 real events, published `0.8.0` found
the expected Personal memory in 34 of 40 cases, but stayed silent in only 7 of
10 unrelated controls and returned two internal query errors. Warm p95 was
547 ms and the largest context was about 184 tokens. This makes noise and query
reliability the next defects to fix; it does not invalidate the narrower live
dogfood evidence above. [Method and aggregate](./docs/evals/2026-08-12-real-personal-memory-baseline.md).

The current product-path benchmark isolates the installed Pulse runtime rather
than a Python approximation. The 0.8.1 fix removed weak direct-capsule noise:
on the compact product set it recalled 12 of 15 useful memories and stayed
silent on all 5 unrelated controls, versus 13 of 15 and 2 of 5 for published
0.8.0. Full raw-history LoCoMo evidence retrieval reached 561 of 1,535 eligible
questions. A bounded LongMemEval-S run found an official answer turn for all 30
sampled questions, but that is an oracle retrieval ceiling, not official answer
accuracy. The existing emotional benchmark mostly tests hidden user state,
biometrics, and graph traversal that Pulse never receives; only 5 of its 35
questions currently test the Personal product. [Current benchmark report](./docs/evals/2026-08-14-product-memory-benchmarks.md).

The 0.8.2 preview candidate was also run end to end on all 1,535 eligible
LoCoMo questions with the same GPT-5.4 low extraction, answering, and judging
model used for equal-model Mem0. It scored 1,230 correct answers (80.13%) versus
Mem0's 1,231 (80.20%), up from the previous Pulse baseline of 959 (62.48%).
Median context was 120 estimated tokens, maximum context 198, and warm p95
retrieval 70.8 ms. This is strong development evidence, not a production
readiness claim. [Full candidate result](./docs/evals/2026-08-16-full-locomo-candidate.md).

A separate remote Claude Chat experiment passed on 2026-08-11 against an
isolated hosted store containing 21 memories. Automatic recall required a
short account-level **Instructions for Claude** rule and the Pulse connector in
**Always available** mode; MCP server instructions alone did not trigger the
tool. Each ordinary message therefore makes one visible `pulse_recall` call.
This proves the remote connector flow only: the hosted store is not synchronized
with the local Personal 0.8 vault and is not part of the published install.

[![npm](https://img.shields.io/npm/v/@zbs-gg/pulse/latest?label=%40zbs-gg%2Fpulse&color=050505)](https://www.npmjs.com/package/@zbs-gg/pulse)
[![license](https://img.shields.io/badge/license-AGPL--3.0-050505)](./LICENSE)
[![node](https://img.shields.io/badge/node-20%2B-050505)](#install)

## Install

Version `0.7.2` remains the stable release for Apple Silicon Macs. Select the
`0.8.3` preview explicitly. Intel Mac, Windows, and Linux are not public
support claims; fixture tests do not replace native acceptance.

Ask your AI agent to inspect this repository and explain the changes before it
installs anything. The current published Personal installation is:

```bash
npx -y @zbs-gg/pulse@0.7.2 init codex
pulse doctor
pulse home
```

To try the 0.8 preview explicitly:

```bash
npx -y @zbs-gg/pulse@preview init codex
```

The 0.8.3 installer finds Codex, Claude Code, Cursor, and compatible
OpenCode, shows every file it will change plus the OpenCode plugin-list diff,
and offers to connect all detected programs. To inspect the same plan without
changing anything:

```bash
pulse init codex --dry-run
pulse init claude-code --dry-run
pulse init cursor --dry-run
pulse init opencode --dry-run

# Install into every detected program after reviewing the displayed changes:
pulse init codex --yes

# Or limit installation to one program:
pulse init codex --only codex
pulse init opencode --only opencode --fun-facts small-model
```

The installer keeps one local Personal store and connects every supported AI
program it finds. It does not need Docker or a cloud account. `pulse home`
opens Memory Home for inspecting, correcting, and deleting local memories.

## What is stored

In the 0.8 preview, Pulse exposes one memory tool, `pulse_memory`.
The AI program may call it during an ordinary working turn when a durable
decision, preference, open question, project state, correction, or emotional
moment appears. A save includes every meaningful part, including coexisting emotions and their causes. Long summaries are split into linked parts without discarding meaning. The complete set is durably accepted before individual materialization; a separate finalizing agent turn is never created. The older `pulse_remember` and `pulse_graph_delta`
names remain compatibility aliases but are not advertised to the model.

Raw conversation capture and old-chat import are off by default. Secret-like,
transcript-like, and path-like payloads are rejected. No backend model call is
enabled as a hidden default. OpenCode fun facts are public opt-in; when enabled,
the service call uses the person's OpenCode model configuration and records
only model, latency, optional usage, and a candidate-set digest.

Memory stays in the local Pulse data directory. It is not committed to Git and
is not sent to a Pulse server. Disconnecting an AI program preserves memory;
`pulse wipe --confirm "wipe pulse memory"` is the separate destructive action.

An emotional mark records a short description of the moment, one or more
emotions, their strength, and whether the emotion or its cause came from you or
was inferred by Pulse. Its influence on the current answer halves every 24
hours and stops after seven days; the historical event remains until you edit
or delete it in Memory Home. If a strong emotion has no known cause, Pulse may
ask one short question inside the next ordinary answer. It never starts another
turn by itself.

To prepare a separate merged Personal database without changing old files:

```bash
pulse migrate local-stores --out local-memory-preview.json --open
pulse migrate local-status --json
```

Only actual contradictions require a choice in the local review page. The
final switch requires the exact confirmation shown by Pulse and keeps every
source database untouched.

## Local operation must remain available

Pulse memory is optional. If its daemon or activation is broken, the AI
program must still leave the terminal, files, Stop/Cancel, goal controls, and
normal session completion available. A failed memory attempt must not create
an automatic continuation. The repository tests this boundary, but a packaged
test is not a substitute for a fresh real session in each AI program.

The 0.8 candidate does not preload a session memory package. Each user
question triggers one temporary local relevance search; the question is not
saved. At most four memories and about 600 tokens are offered, and weak matches
produce no memory context. Stable rules remain in `AGENTS.md` or `CLAUDE.md`
instead of being duplicated by Pulse.

The same branch adds bounded storage maintenance. `pulse storage` reports
protected releases and generated files that can be removed. `pulse storage
clean` requires an exact confirmation, keeps the active release plus one
rollback, and removes only unchanged generated artifacts after a launch and
recall check. It never includes the active vault, keys, migration sources, or
unknown files.

## Existing databases

Official `0.6.7` and `0.7.1` Personal databases are upgraded in place without
losing their records. A database made by an unpublished shared-memory build is
refused with a clear error and is left byte-for-byte unchanged. Use the local
merge preview above instead of opening an old mixed database directly.

## Personal and Team

`@zbs-gg/pulse` is the open local Personal product. Pulse Team is a separate
private pilot and is not included in this repository or npm package. The
existing AGPL license of Personal code has not been changed by that split.

## Development

```bash
make verify
make release-verify
```

`make verify` builds, formats-checks, and tests the Go engine, local MCP server,
and Personal CLI in an isolated temporary data directory. `make
release-verify` adds the packaged Personal release checks. Neither command may
use `~/.pulse`.

The local MCP connection remains `stdio`. It accepts both the final
`2026-07-28` protocol and older clients. Stateless transport does not make the
memory temporary: the data remains in the local SQLite database.

Read [AGENTS.md](AGENTS.md) before an agent changes installation or global
harness configuration. Security and rollback details are in
[docs/SECURITY_INSTALL_CHECKLIST.md](docs/SECURITY_INSTALL_CHECKLIST.md).

Status: stable remains 0.7.2; 0.8.3 is an opt-in preview. Exact-archive
installation and real host recall/write checks are required separately from
source tests. One-day acceptance and production readiness are not implied.
Remote Claude Chat experiments are separate from the local Personal product.

## Complete moments in 0.8.3

Explicit memory requests preserve all meaningful parts and coexisting feelings.
Pulse validates the full structured set before accepting it locally, then
materializes individual parts with durable receipts. A `stored` result covers
all submitted parts; `pending` or `partial` does not. Canonical storage and
explicit moment reading do not wait for the background search index. Each part
keeps its own pending index receipt until indexing actually finishes. A lost response permits one
bounded replay of the identical operation. A validation refusal before admission
permits correcting the input. Stop does not create an automatic continuation.

A moment reference lets the agent read all currently eligible linked parts with
pagination, independently of the short automatic context budget. Deleted items
are excluded; current corrections and personal/project boundaries still apply.
The write transport envelope is 16 MiB, not a limit on the number of feelings.
Oversized input is explicitly refused before admission, never truncated.
Structured summaries longer than one storage row are split without dropping text.
Historical feelings remain remembered even after their current-state influence
fades; inferred emotions are identified as hypotheses about that moment.

Native Codex, Claude Code, Cursor and OpenCode adapters and the BB adapter share
this behavior. Raw transcripts, secrets, old-chat import and backend model calls
remain off by default. Build/tests are not an installed-runtime or public-release
claim; publication and owner-machine checks must use the exact signed archive.
