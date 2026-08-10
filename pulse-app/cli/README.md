# @zbs-gg/pulse

The npm package for Pulse Personal 0.8.0. It installs one local memory engine,
Memory Home, and local connections for Codex, Claude Code, and Cursor. Emotional
memory stays local, describes a moment rather than a personality, and can be
confirmed, corrected, or deleted separately from the event.

Personal 0.8 is an unfinished branch and is not published. Use the published
0.7.2 package for ordinary installation until this branch passes live review.

```bash
npx -y @zbs-gg/pulse@0.7.2 init codex
pulse doctor
pulse home
```

The command finds every supported AI program, shows the exact changes, and
offers to connect all of them. To review without writing, or to limit the
installation to one program:

```bash
pulse init codex --dry-run
pulse init claude-code --dry-run
pulse init cursor --dry-run
pulse init <harness> --yes
pulse init <harness> --only <harness>
```

The package contains no shared-memory server or cloud synchronization. Pulse
Team is a separate private pilot. Personal memory remains local, raw transcript
capture is off by default, and no backend model API is called by default.

The local MCP server uses stdio and accepts both protocol `2026-07-28` and older
clients. Persistent memory remains in SQLite; it does not depend on a lasting
MCP session.

Disconnect with `pulse disconnect codex`, `pulse disconnect claude-code`, or
`pulse disconnect cursor`. Remove all local Pulse memory only with the separate
confirmed command `pulse wipe --confirm "wipe pulse memory"`.

License: AGPL-3.0-only. Status: unfinished 0.8 development branch.
