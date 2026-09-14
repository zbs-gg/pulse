# Pulse Native Support Ledger

This tracked file states the current public-support boundary. It must stay
`pending` until `Verify Gold publication` generates its replacement from the
signed promotion authorization and the final 18 public-registry receipts.
Rows must never be promoted by hand.

## Gold matrix contract

The calibrated matrix is `pulse.native_universal_matrix.v2`: three harnesses
times six native targets, with no allowed failures.

| Harness | Exact calibrated version | Vendor distribution | Targets | Candidate | Public support |
|---|---:|---|---:|---:|---:|
| Codex | `0.145.0` | `@openai/codex` | 6 | pending | pending |
| Claude Code | `2.1.220` | `@anthropic-ai/claude-code` | 6 | pending | pending |
| Cursor Desktop | `3.13` | signed vendor installers | 6 | pending | pending |

The six targets are `darwin-arm64`, `darwin-x64`, `linux-arm64-gnu`,
`linux-x64-gnu`, `win32-arm64`, and `win32-x64`. A newer uncalibrated harness
may still run, but doctor must report `harness_version_unverified`; it does not
extend this ledger.

## Unpublished OpenCode candidate

OpenCode is outside the historical Gold matrix above. Candidate 0.8.3 has one
bounded contract only: OpenCode `1.18.x` on `darwin-arm64`, physically anchored
to the locally installed `1.18.15`. Repository contract and temporary-HOME
tests are green; outside-source package installation, owner-machine live
recall/write/fun-fact/fail-open acceptance, and public support are pending.

| Harness | Version | Target | Contract tests | Physical candidate | Public support |
|---|---:|---|---:|---:|---:|
| OpenCode | `1.18.x` (`1.18.15` anchor) | `darwin-arm64` | pass | pending | unavailable |

## Evidence authority

- `fixture` proves repository orchestration only. It always has
  `support_claim:false`.
- `production_candidate` requires signed/notarized release assets and a real
  vendor session for every host-target pair. It still has
  `support_claim:false`.
- `public_registry` installs the exact npm bytes and R2 assets and is the only
  authority allowed to emit `support_claim:true`.

Every retained record uses `pulse.native_host_target_evidence.v2`, is
content-free, binds the source commit, package and catalog digests, vendor
executable and session executable digests, privacy defaults, lifecycle
milestones, and first-value stage durations. Fixture calls, direct hook calls,
and Safe Mode cannot produce public authority.

## Required gates

1. `Universal / required` passes on the exact `main` SHA.
2. Production inputs build six signed native target sets, two common assets,
   a signed epoch-8 artifact set and snapshot, security evidence, and the exact
   unpublished npm tarball.
3. The origin workflow publishes immutable assets before the snapshot and
   verifies each object from `releases.zbs.gg`.
4. `Seal production candidate` runs all 18 real vendor sessions and seals the
   npm candidate only after `18/18` production-candidate receipts pass.
5. npm `preview` is approved by a human through 2FA.
6. Public-registry matrices pass at 0, 24, 48, and 72 hours. Windows ARM64
   Codex must have five consecutive runs at or below 60 seconds and median at
   or below 55 seconds; every other first-value run is at or below 60 seconds.
7. `Authorize Gold promotion` signs the four-checkpoint receipt but performs
   no publication. A human moves the unchanged bytes to `latest`, creates tag
   and release `v0.7.0`, then `Verify Gold publication` generates the final
   ledger.

## Current state

All Gold columns are intentionally pending. The repository contains the gates,
but no production candidate, public-registry 72-hour evidence set, npm
promotion, Git tag, or GitHub Release has been produced by this branch.

Historical six-target Codex fixture run `29701975544` predates matrix v2 and
evidence v2. It remains useful engineering history, but it is not Gold support
evidence and cannot populate this table.
