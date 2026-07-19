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
   receipt, all six native targets, and the referenced green `Universal` run;
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
