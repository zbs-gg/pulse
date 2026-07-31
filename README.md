# Pulse Personal

Pulse keeps approved working memory for Codex, Claude Code, and Cursor on your
computer. Version `0.7.0` is Personal only: it has no shared-memory server, no
cloud synchronization, and no command for publishing memory to other people.

[![npm](https://img.shields.io/npm/v/@zbs-gg/pulse/latest?label=%40zbs-gg%2Fpulse&color=050505)](https://www.npmjs.com/package/@zbs-gg/pulse)
[![license](https://img.shields.io/badge/license-AGPL--3.0-050505)](./LICENSE)
[![node](https://img.shields.io/badge/node-20%2B-050505)](#install)

## Install

Ask your AI agent to inspect this repository and explain the changes before it
installs anything. The normal Personal installation is:

```bash
npx -y @zbs-gg/pulse@0.7.0 install
pulse doctor
pulse home
```

You can also inspect and connect one program at a time:

```bash
pulse init codex --dry-run
pulse init claude-code --dry-run
pulse init cursor --dry-run

# Run only after reviewing the displayed changes:
pulse init codex --yes
pulse init claude-code --yes
pulse init cursor --yes
```

The installer keeps one local Personal store and connects every supported AI
program it finds. It does not need Docker or a cloud account. `pulse home`
opens Memory Home for inspecting, correcting, and deleting local memories.

## What is stored

Pulse accepts small structured memory capsules. Raw conversation capture and
old-chat import are off by default. Secret-like, transcript-like, and
path-like payloads are rejected. No backend model call is enabled as a hidden
default.

Memory stays in the local Pulse data directory. It is not committed to Git and
is not sent to a Pulse server. Disconnecting an AI program preserves memory;
`pulse wipe --confirm "wipe pulse memory"` is the separate destructive action.

## Local operation must remain available

Pulse memory is optional. If its daemon or activation is broken, the AI
program must still leave the terminal, files, Stop/Cancel, goal controls, and
normal session completion available. A failed memory attempt must not create
an automatic continuation. The repository tests this boundary, but a packaged
test is not a substitute for a fresh real session in each AI program.

## Existing databases

An official `0.6.7` Personal database is upgraded in place without losing its
records. A database made by an unpublished shared-memory build is refused with
a clear error and is left byte-for-byte unchanged. Test `0.7.0` with a separate
data directory when you have used such a build.

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

Read [AGENTS.md](AGENTS.md) before an agent changes installation or global
harness configuration. Security and rollback details are in
[docs/SECURITY_INSTALL_CHECKLIST.md](docs/SECURITY_INSTALL_CHECKLIST.md).

Status: developer preview, not a production-readiness claim.
