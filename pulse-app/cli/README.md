# @zbs-gg/pulse

The package source for the unpublished Pulse Personal 0.8.3 candidate. It
installs one local memory engine,
Memory Home, and local connections for Codex, Claude Code, Cursor, and OpenCode.
Emotional
memory stays local, describes a moment rather than a personality, and can be
confirmed, corrected, or deleted separately from the event.

Personal 0.8.2 is the preview, not the stable default. Its release flow installs
the exact archive on a clean Apple Silicon runner and makes a real BGE-M3
semantic query before publication. Owner-machine daily-use and Cursor
acceptance remain pending. The npm `latest` tag remains on 0.7.2.
OpenCode support is new in candidate 0.8.3 and is absent from both published
versions; no 0.8.3 npm or GitHub release exists yet.

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
pulse init opencode --dry-run
pulse init opencode --only opencode --fun-facts small-model
pulse init <harness> --yes
pulse init <harness> --only <harness>
```

The package contains no shared-memory server or cloud synchronization. Pulse
Team is a separate private pilot. Personal memory remains local, raw transcript
capture is off by default, and no backend model API is called by default.
OpenCode fun facts are separately opt-in and use the person's OpenCode model
configuration with a content-free technical receipt.

The local MCP server uses stdio and accepts both protocol `2026-07-28` and older
clients. Persistent memory remains in SQLite; it does not depend on a lasting
MCP session.

Disconnect with `pulse disconnect codex`, `pulse disconnect claude-code`,
`pulse disconnect cursor`, or `pulse disconnect opencode`. Remove all local
Pulse memory only with the separate confirmed command
`pulse wipe --confirm "wipe pulse memory"`.

License: AGPL-3.0-only. Status: 0.8.3 is unpublished; Personal 0.8.2 remains the
public preview and stable remains 0.7.2.
