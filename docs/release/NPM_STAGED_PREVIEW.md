# npm staged preview and Gold promotion

Pulse never publishes npm bytes from a pull request. Release work is split so
that fixtures cannot accidentally become production evidence.

## Candidate boundary

1. `Production candidate` binds one exact green `Universal` push run on
   `main`, builds the six signed native target sets, two common assets, signed
   epoch-8 catalog, security evidence, and npm tarball. Its output is
   `pulse.npm_production_inputs.v1` with `production_ready:false` and
   `support_claim:false`.
2. `Production origin` publishes all immutable R2 objects, verifies HEAD and a
   second SHA-256 download, then publishes the root-signed snapshot last.
3. `Seal production candidate` installs the same unpublished tarball and the
   public R2 assets in 18 real Codex, Claude Code, and Cursor sessions. Only
   after `18/18` does it emit `pulse.npm_production_candidate.v1` with
   `production:true`, `production_ready:true`, and `support_claim:false`.

All three workflows are manual, main-bound, protected by separate GitHub
Environments, retain content-free evidence for 30 days, and never publish npm
bytes.

## Preview publication

After a successful `Seal production candidate` run on `main`, GitHub
automatically starts `Stage npm preview` for that exact run, source commit, and
tarball SHA-256. A manual dispatch with the same checks and a typed confirmation
remains available for recovery. The workflow requires environment `npm-preview`
and npm Trusted Publisher OIDC. It runs
`npm stage publish <exact-tarball> --tag preview`, then stops. A maintainer must
inspect and approve the staged bytes with npm 2FA. Long-lived npm publication
tokens are forbidden.

The trusted publisher must be restricted to repository `zbs-gg/pulse` and the
reviewed publication workflow.

For the current Apple Silicon preview, `docs/release/PREVIEW_PUBLICATION.json`
is the single reviewed publication descriptor. It binds the package version,
signed epoch, exact archive and tree digests, artifact set, snapshot, and
GitHub prerelease notes. `npm run verify:preview-publication` refuses stale
README, llms, PRODUCT, installation documents, changelog, plugin versions, or
signed release metadata.

`.github/workflows/publish-npm.yml` downloads only those reviewed bytes,
installs them on a clean Apple Silicon runner, publishes the npm `preview` tag
through trusted publishing when requested, downloads the public registry bytes
again, and then creates the matching GitHub prerelease with the same archive
and checksum. The typed confirmation is required for either external change.
Running only one publication step is recoverable: a later run verifies the
already-public bytes before creating the missing counterpart.

The staged multi-target Gold path below remains separate from this bounded
Apple Silicon preview. It is required before moving unchanged bytes to npm
`latest`; a preview prerelease is not a Gold support claim.

## Public soak and Gold

`Public registry soak` runs at 0, 24, 48, and 72 hours. Each checkpoint:

- obtains `@zbs-gg/pulse@0.7.0` only from npm and requires its SHA-256 to equal
  the candidate;
- obtains the current snapshot and immutable artifact set only from
  `releases.zbs.gg`, with no redirects;
- probes range requests, ETag, interrupted download resume, and exact digest;
- runs `npm audit --omit=dev --audit-level=high` against registry dependencies;
- runs all 18 real vendor-session lifecycles with `authority:public_registry`.

`Authorize Gold promotion` requires the four successful timed runs and signs a
promotion receipt through `production-catalog`. The receipt explicitly says
`publication_performed:false`. It does not change npm, Git tags, GitHub
Releases, or documentation.

After a human separately promotes the unchanged `0.7.0` bytes to `latest` with
2FA and creates tag and release `v0.7.0`, `Verify Gold publication` checks that
`preview` and `latest` are byte-identical, the tag resolves to the source
commit, the GitHub Release is public, and the final support ledger is generated
from the signed promotion and 72-hour receipts.

The exact environment, credential, DNS, and operator sequence is in the
[`Pulse 0.7.0 Universal Gold runbook`](UNIVERSAL_GOLD_0_7_0.md).
