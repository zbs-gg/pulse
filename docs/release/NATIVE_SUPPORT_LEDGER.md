# Pulse Native Support Ledger

This ledger is the authority for public desktop support claims. Code paths,
cross-builds, unit tests, and unsigned PR fixtures are necessary evidence, but
none of them alone makes a target publicly supported.

## Evidence classes

1. **PR fixture** — the exact source commit builds a target fixture, packs the
   npm package, invokes the public install command shape, starts the native
   daemon and local fixture embedder, shows a Memory Home card, records a
   terminal receipt, recalls the same object in a fresh host session, repairs,
   and uploads content-free evidence. Its receipt says `production:false` and
   `support_claim:false`.
2. **Production candidate** — signed/notarized target artifacts, the real
   portable model quality and resource gates, npm OIDC provenance, and the
   exact registry-published candidate bytes pass the same native flow.
3. **Public support** — the unchanged candidate digest is promoted to
   `preview`, its evidence is retained, and this ledger names the target and
   harness as supported.

## Native target gate

The required workflow is [`.github/workflows/verify.yml`](../../.github/workflows/verify.yml).
It runs the complete Go, MCP, and CLI suite on the reference macOS host, then
builds and proves the exact packed Personal product separately on every native
target below. It has no allowed failures and aggregates both evidence classes
to the `Universal / required` check.

| Target | GitHub runner | Required PR fixture | Production candidate | Public support |
|---|---|---:|---:|---:|
| `darwin-arm64` | `macos-26` | required, first green run pending | pending | pending |
| `darwin-x64` | `macos-26-intel` | required, first green run pending | pending | pending |
| `linux-arm64-gnu` | `ubuntu-24.04-arm` | required, first green run pending | pending | pending |
| `linux-x64-gnu` | `ubuntu-24.04` | required, first green run pending | pending | pending |
| `win32-arm64` | `windows-11-arm` | required, first green run pending | pending | pending |
| `win32-x64` | `windows-2025` | required, first green run pending | pending | pending |

## Harness calibration

| Harness | Native PR coverage | Public support |
|---|---|---:|
| Codex `0.136.0` | required singleton proof on all six targets | pending production candidate |
| Claude Code | shared Core/adapter contract is tested; six-target native lifecycle calibration pending | pending |
| Cursor | shared Core/adapter contract is tested; six-target native lifecycle calibration pending | pending |

The table is intentionally conservative. A green Codex fixture cannot be used
to claim that Claude Code or Cursor is calibrated on the same target. Safe Mode
remains separately labeled and never upgrades a missing native product claim.

## Promotion rule

`preview` must remain unchanged when any target, harness, artifact signature,
model quality/resource gate, npm provenance check, or package digest check is
missing or red. Publishing requires an explicitly authorized protected release
workflow; the PR workflow never publishes npm packages or production assets.
