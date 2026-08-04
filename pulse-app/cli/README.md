# @zbs-gg/pulse

The npm package for Pulse Personal 0.7.1. It installs one local memory engine,
Memory Home, and local connections for Codex, Claude Code, and Cursor.

```bash
npx -y @zbs-gg/pulse@0.7.1 init codex
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

Disconnect with `pulse disconnect codex`, `pulse disconnect claude-code`, or
`pulse disconnect cursor`. Remove all local Pulse memory only with the separate
confirmed command `pulse wipe --confirm "wipe pulse memory"`.

License: AGPL-3.0-only. Status: developer preview.
