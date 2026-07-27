# npm staged preview release

Pulse never publishes npm bytes from a pull request. The protected
`Stage npm preview` workflow accepts only an exact production candidate built
for the same `main` commit after the required `Universal` run is green.

The workflow:

1. requires the protected `npm-preview` GitHub Environment and an exact typed
   confirmation;
2. downloads the content-addressed `pulse-npm-production-candidate` artifact
   from a successful `Production candidate` run on the same `main` commit;
3. checks the tarball SHA-256, package identity/version/repository, production
   receipt, all six native targets, each target's content-free consolidation
   proof, and the referenced green `Universal` run;
4. runs `npm stage publish <exact-tarball> --tag preview` through GitHub OIDC;
5. stops. A maintainer must inspect and approve the staged bytes with npm 2FA
   before they become public.

The npm trusted publisher must be configured for repository `zbs-gg/pulse`,
workflow `stage-npm-preview.yml`, environment `npm-preview`, and
`npm stage publish` only. Long-lived publication tokens are forbidden. The
package publishing setting should require 2FA and disallow tokens.

The workflow is intentionally unusable until a separate protected
`Production candidate` run has produced signed/notarized native artifacts and
the exact content-free candidate receipt. PR fixtures have
`production:false` and can never cross this boundary.

## Production candidate boundary

`.github/workflows/production-candidate.yml` is manual and main-only. It binds
one exact successful `Universal` push run, requires the typed confirmation
`build universal production candidate`, and uses the repository's exact native
matrix instead of a caller-selected platform list. Separate protected jobs
build macOS arm64/x64, Linux arm64/x64 GNU, and Windows arm64/x64. Apple and
Windows credentials are scoped to their own environments; the root/channel
catalog keys are scoped to `production-catalog`.

The final job verifies all six content-free native proofs, the 14-artifact
catalog receipt, and the signed manifest embedded in the npm tarball. It emits
`pulse-production-release-catalog` and `pulse-npm-production-candidate` GitHub
artifacts with short retention. It never uploads those assets to the release
origin and never invokes npm publishing or staging. Those remain distinct
reviewed actions.
