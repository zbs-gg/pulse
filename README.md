# Pulse Personal

Pulse is memory for AI tools. It gives Codex and Claude Code the knowledge they
need at the moment they need it, and stays silent when nothing relevant is
found. Version `0.8.0` can also remember emotions attached to a specific
moment. It does not turn repeated emotions into personality traits, save the
full conversation, or send Personal memory to a cloud server.

> **0.8.0 is published as an npm preview, not as the stable default.** It passed
> live owner-machine use in Codex and Claude Code on Apple Silicon. Cursor live
> acceptance and production readiness remain pending. Use 0.7.2 for the stable
> installation or select `@preview` explicitly to try 0.8.0.

The owner-machine migration and atomic database switch passed on 2026-08-09.
The old Pulse and Claude Mem sources, migration copies, and recovery files are
preserved in a verified encrypted external archive. On 2026-08-12, installed
local epoch 34 passed automatic semantic recall and one-call writing across
fresh Codex and Claude Code sessions. A cancelled search no longer disabled
full semantic retrieval for the next question. Personal memory crossed
projects, project memory stayed inside its repository, an unrelated question
received no memory, and both hosts kept working with Pulse stopped. The
owner-machine one-day dogfood is now running in those two hosts. One broad,
ambiguous Claude question still surfaced an unrelated old memory before a more
specific semantic question found the intended decision; this remains a
dogfood risk rather than a release claim. Cursor live acceptance remains
pending.

The first frozen large-vault baseline on 2026-08-12 did not pass its combined
practical bar. On a copy containing 76,795 real events, published `0.8.0` found
the expected Personal memory in 34 of 40 cases, but stayed silent in only 7 of
10 unrelated controls and returned two internal query errors. Warm p95 was
547 ms and the largest context was about 184 tokens. This makes noise and query
reliability the next defects to fix; it does not invalidate the narrower live
dogfood evidence above. [Method and aggregate](./docs/evals/2026-08-12-real-personal-memory-baseline.md).

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

Version `0.7.2` remains the stable release for Macs with Apple Silicon. Version
`0.8.0` is published under the npm `preview` tag for the same public target.
Its exact archive passed installation on a clean Apple Silicon GitHub runner
before publication. Intel Mac, Windows, and Linux are not public support claims
yet; fixture and packaging checks on those targets do not replace live product
acceptance.

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

The installer finds Codex, Claude Code, and Cursor, shows every file it will
change, and offers to connect all detected programs. To inspect the same plan
without changing anything:

```bash
pulse init codex --dry-run
pulse init claude-code --dry-run
pulse init cursor --dry-run

# Install into every detected program after reviewing the displayed changes:
pulse init codex --yes

# Or limit installation to one program:
pulse init codex --only codex
```

The installer keeps one local Personal store and connects every supported AI
program it finds. It does not need Docker or a cloud account. `pulse home`
opens Memory Home for inspecting, correcting, and deleting local memories.

## What is stored

In the 0.8 preview, Pulse exposes one memory tool, `pulse_memory`.
The AI program may call it during an ordinary working turn when a durable
decision, preference, open question, project state, correction, or emotional
moment appears. One call accepts at most three short items and does not create
a separate finalizing turn. The older `pulse_remember` and `pulse_graph_delta`
names remain compatibility aliases but are not advertised to the model.

Raw conversation capture and old-chat import are off by default. Secret-like,
transcript-like, and path-like payloads are rejected. No backend model call is
enabled as a hidden default.

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

The 0.8 preview does not preload a session memory package. Each user
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

Status: Personal 0.8.0 is published under the npm `preview` tag. Installed
local epoch 34 passed the
local-store migration, cancellation-safe semantic search, one-call writes,
cross-host recall, project boundary, irrelevant-query silence, and fail-open
operation in fresh Codex and Claude Code sessions. An owner-machine one-day
dogfood started in those two hosts on 2026-08-12; broad ambiguous questions
remain under observation and Cursor acceptance remains pending. A separate Claude Chat
remote-recall experiment passed with a visible tool call on every message, but
is not Personal sync or a supported install. Version 0.8 is not the stable
default or production ready.
