# Pulse Native Support Ledger

This ledger is the authority for public desktop support claims. Code paths,
cross-builds, unit tests, and unsigned PR fixtures are necessary evidence, but
none of them alone makes a target publicly supported.

## Evidence classes

1. **PR fixture** — the exact source commit builds a target fixture, packs the
   npm package, invokes the public install command shape, starts the native
   daemon and local fixture embedder, shows a Memory Home card, records a
   terminal receipt, recalls the same object in a fresh host session, repairs,
   produces one byte-preserving consolidation report through CLI, MCP, and
   Memory Home, and uploads content-free evidence. Its receipt says
   `production:false` and `support_claim:false`.
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
| `darwin-arm64` | `macos-26` | passed — run `29701975544` | pending | pending |
| `darwin-x64` | `macos-26-intel` | passed — run `29701975544` | pending | pending |
| `linux-arm64-gnu` | `ubuntu-24.04-arm` | passed — run `29701975544` | pending | pending |
| `linux-x64-gnu` | `ubuntu-24.04` | passed — run `29701975544` | pending | pending |
| `win32-arm64` | `windows-11-arm` | passed — run `29701975544` | pending | pending |
| `win32-x64` | `windows-2025` | passed — run `29701975544` | pending | pending |

## First complete PR fixture

The first complete six-target fixture passed on 2026-07-19 in
[Universal run `29701975544`](https://github.com/zbs-gg/pulse/actions/runs/29701975544).
The PR source head was `a4e04a28a8c45a997aa84b098c83dacf13e86c04`; GitHub tested merge checkout
`95250675486d33ae38b53d63a62e941ca221fb35`. `Full product suite`, every
native matrix job, and `Universal / required` completed successfully.

Every retained receipt is `pulse.native_universal_target_evidence.v1` with
`authority:pr-fixture`, `production:false`, and `support_claim:false`. Each
target installed the exact packed command path, reached full retrieval, showed
a visible first memory card, recalled the same object in a fresh Codex session,
and finished lifecycle readiness. Token economy remains honestly labeled
`collecting_baseline`; this fixture proves continuity, not a production release
or a token-savings claim.

New candidate receipts also carry a `consolidation` proof from the exact packed
tarball. It must be `report_ready`, match across CLI, MCP, and Memory Home,
preserve every synthetic source byte-for-byte, and state that no import, merge,
delete, or publish authority was exercised. Historical run `29701975544`
predates that field, so it remains continuity evidence only; a new green
six-target run is required before the consolidation report can enter a
production candidate.

The current `first_value` boundary is the moment a fresh host session receives
and verifies the exact saved memory (`fresh_session_context`). Prompt-context
lifecycle calibration still runs immediately afterward and remains required for
the target to pass. The historical values below used the stricter earlier
boundary through that fresh prompt, so they are conservative rather than
directly comparable to newer receipts.

| Target | Evidence artifact | First value | Packed package SHA-256 | Release manifest digest |
|---|---|---:|---|---|
| `darwin-arm64` | `pulse-native-darwin-arm64` | 13,058 ms | `70895172f1b46bf455dbdc2adbb8519fba71ab069e71b63a70322049d91056c8` | `c2cda1aeab2f81c32061a689e0ee2c45d524f70c60a4a4b33ba643e7ca66bcb0` |
| `darwin-x64` | `pulse-native-darwin-x64` | 17,571 ms | `70895172f1b46bf455dbdc2adbb8519fba71ab069e71b63a70322049d91056c8` | `606be8aefc82da40fc2dca0f857489d83f3c6b100a1affeac2f8808190db74df` |
| `linux-arm64-gnu` | `pulse-native-linux-arm64-gnu` | 12,882 ms | `70895172f1b46bf455dbdc2adbb8519fba71ab069e71b63a70322049d91056c8` | `f8a1cde14b23c19d4668fbd49e6a267826a8414dd0b3a7d9f23800f0a231ca71` |
| `linux-x64-gnu` | `pulse-native-linux-x64-gnu` | 13,305 ms | `70895172f1b46bf455dbdc2adbb8519fba71ab069e71b63a70322049d91056c8` | `15495d2f8c4fcbdf5b22213ca7aaf453bc2dcf8abf050355e15a7204cf707964` |
| `win32-arm64` | `pulse-native-win32-arm64` | 58,235 ms | `8a471d726e6aa888030cd3a29dd6ae180562415c6237aaeec1aab4a04609d76a` | `787e431c068664f86d8fe38984ec9d6945b15d2b3aa3328958b2092135c06a86` |
| `win32-x64` | `pulse-native-win32-x64` | 50,337 ms | `8a471d726e6aa888030cd3a29dd6ae180562415c6237aaeec1aab4a04609d76a` | `c67348b48e438323d0b2d0ffb09685b6ab5a31716fa83c9272f40a2b20e6a808` |

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
The fail-closed OIDC and human-approval contract is documented in
[`NPM_STAGED_PREVIEW.md`](NPM_STAGED_PREVIEW.md).
