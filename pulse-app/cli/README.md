# @zbs-gg/pulse

The package source for the unreleased Pulse Personal 0.8.2 candidate. It
installs one local memory engine,
Memory Home, and local connections for Codex, Claude Code, and Cursor. Emotional
memory stays local, describes a moment rather than a personality, and can be
confirmed, corrected, or deleted separately from the event.

Personal 0.8.1 remains the published preview, not the stable default. A later
owner-machine check found that its signed daemon could report BGE-M3 ready
while cold semantic queries timed out, so daily-use and Cursor acceptance
remain pending. The npm `latest` tag remains on 0.7.2. This 0.8.2 source must
complete the signed release flow, including a real semantic query, before the
`preview` tag moves.

```bash
npx -y @zbs-gg/pulse@preview init codex
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

License: AGPL-3.0-only. Status: 0.8.2 candidate unreleased; Personal 0.8.1
preview published; stable remains 0.7.2.
