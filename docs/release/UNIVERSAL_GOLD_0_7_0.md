# Pulse 0.7.0 Universal Gold runbook

This is an operator runbook, not evidence that Gold has happened. Every
external mutation remains separately consent-gated.

## Fixed release identity

- package: `@zbs-gg/pulse@0.7.0`;
- release epoch: `8`;
- origin: `https://releases.zbs.gg` backed by R2 bucket
  `pulse-releases-prod`;
- rollout: `preview` → four public matrices over 72 hours → unchanged bytes in
  `latest`;
- matrix: Codex `0.145.0`, Claude Code `2.1.220`, Cursor Desktop `3.13` on
  macOS, GNU/Linux, and Windows arm64/x64.

## Protected environments

Create mandatory-reviewer GitHub Environments named `production-linux`,
`production-apple`, `production-windows`, `production-model`,
`production-catalog`, `production-candidate`, `production-origin`,
`production-harness-e2e`, `npm-preview`, and `npm-gold`. Protect `main` and keep
human review mandatory; auto-merge stays disabled.

Keep credentials separated exactly by environment. Apple signing and
notarization belong only to `production-apple`; Authenticode only to
`production-windows`; root/channel keys only to `production-catalog`; R2 parent
authority only to `production-origin`; restricted vendor test credentials only
to `production-harness-e2e`. npm uses Trusted Publisher OIDC, never a long-lived
token.

## Origin prerequisites

Create the R2 bucket and attach `releases.zbs.gg` as its custom domain. Disable
the public `r2.dev` endpoint and bucket listing. Immutable versioned objects use
`Cache-Control: public, max-age=31536000, immutable`; snapshot uses
`Cache-Control: no-cache`. Confirm TLS, no redirects, byte ranges, and ETag from
macOS, Linux, and Windows before authorizing a candidate.

The publication workflow mints a one-hour credential restricted to
`pulse/0.7.0/`, refuses overwrite with different bytes, uploads the snapshot
last, and re-downloads every object for SHA-256 verification.

## Ordered execution

1. Merge the implementation through human-reviewed PRs.
2. Record the green `Universal` push run ID for the exact `main` SHA.
3. Run `Production candidate` with epoch `8` and its typed confirmation.
4. Separately authorize `Production origin` for those exact inputs.
5. Run `Seal production candidate`; any missing real vendor session blocks it.
6. Configure npm Trusted Publisher. A successful seal automatically starts
   `Stage npm preview`; inspect the staged bytes and approve them with 2FA.
7. Run `Public registry soak` at 0, 24, 48, and 72 hours. Do not reuse a failed
   release version or overwrite published bytes.
8. Run `Authorize Gold promotion` with all four run IDs.
9. After inspecting the signed receipt, separately move the same digest to
   npm `latest`, create Git tag and GitHub Release `v0.7.0`.
10. Run `Verify Gold publication`. Use its generated ledger in a documentation
    PR; only after that PR is merged may the README lose its pending label.

## Deliberate blockers

The workflows fail rather than downgrade to a fixture, direct hook call, Safe
Mode, unsigned carrier, stale snapshot, partial origin, different npm bytes,
or uncalibrated harness. Existing local vaults must remain intact across repair,
disconnect, and upgrade; wipe remains an independent explicit consent action.
