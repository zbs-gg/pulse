# Pulse Personal

Pulse keeps approved working memory for Codex, Claude Code, and Cursor on your
computer. Version `0.8.0` can also remember emotions attached to a specific
moment. It does not turn repeated emotions into personality traits, save the
full conversation, or send Personal memory to a cloud server.

> **0.8.0 is an unfinished branch, not a published package.** The commands and
> behavior below describe the intended next release. Use the published 0.7.2
> for ordinary installation until this branch passes a separate live review.

The owner-machine migration and atomic database switch passed on 2026-08-09,
with source databases and recovery copies preserved. A local epoch-22 candidate
then passed fresh automatic semantic recall in both Codex and Claude Code: each
used the exact stored rule that an emotional moment is a separate
`pulse_memory` item, without the question repeating the stored wording. The
owner-machine one-day dogfood started on 2026-08-10 with Pulse enabled in those
two hosts. Cursor live acceptance remains pending.

[![npm](https://img.shields.io/npm/v/@zbs-gg/pulse/latest?label=%40zbs-gg%2Fpulse&color=050505)](https://www.npmjs.com/package/@zbs-gg/pulse)
[![license](https://img.shields.io/badge/license-AGPL--3.0-050505)](./LICENSE)
[![node](https://img.shields.io/badge/node-20%2B-050505)](#install)

## Install

Version `0.7.2` is published and installs on Macs with Apple Silicon. The exact
npm package passed a clean installation on a separate Mac before publication.
Intel Mac, Windows, and Linux builds are not released yet; on those systems the
installer stops without changing files. Both npm tags, `latest` and `preview`,
point to `0.7.2`.

Ask your AI agent to inspect this repository and explain the changes before it
installs anything. The current published Personal installation is:

```bash
npx -y @zbs-gg/pulse@0.7.2 init codex
pulse doctor
pulse home
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

In the unfinished 0.8 branch, Pulse exposes one memory tool, `pulse_memory`.
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

The unfinished 0.8 branch does not preload a session memory package. Each user
question triggers one temporary local relevance search; the question is not
saved. At most four memories and about 600 tokens are offered, and weak matches
produce no memory context. Stable rules remain in `AGENTS.md` or `CLAUDE.md`
instead of being duplicated by Pulse.

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

Status: unfinished Personal 0.8 branch. The local-store migration, corrected
one-call Claude write, and fresh invisible recall in Codex and Claude Code
passed. An owner-machine one-day dogfood is running in those two hosts; Cursor
acceptance remains pending. Version 0.8 is not published or production ready.
