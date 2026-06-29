# Changelog

All notable changes to Pulse.

## 2026-06-29

### Fixed
- **Unicode-aware memory tags** — `safeTagPattern` now accepts `\p{L}\p{N}`,
  so non-ASCII tags (e.g. Cyrillic) validate on the first try. This removes the
  silent retry that added multi-second latency to `pulse_remember`. The
  secret / path / transcript guards are unchanged (negative-smoke still rejects
  all 15 dangerous payloads).

### Added
- **MCP: persistent OAuth tokens** — access/refresh tokens are written to disk
  and reloaded on start, so a daemon restart no longer logs out a hosted
  connector. Optional PIN gate on `/authorize` for hosted deployments.
- **CLI: hosted capture** — capture hooks can send a bearer
  (`PULSE_REMOTE_BEARER`) to write into a hosted store behind an auth proxy.
- Host-extracted semantic-delta capture script (`pulse-app/scripts/pulse_live_extract.py`).
- One-store multi-harness capture decision record (`docs/`).

### Docs
- GitHub presentation: README hero + badges + honest comparison table,
  `SECURITY.md`, `CONTRIBUTING.md`.
