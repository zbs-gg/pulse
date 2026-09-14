# Personal Preview release trust

`pulse-release-root.pem` is the pinned public verification root consumed by
the installer. Its private Ed25519 key and the delegated preview-channel key
must stay outside npm, Git, command output, and build logs.

The release pipeline is split into explicit builders. A target preflight is
structural and performs no signing, notarization, or publication:

```sh
npm run build:personal-release -- \
  --check --mode fixture --target darwin-arm64
```

An actual target build additionally requires an absolute `--output` path. A
production macOS build requires the exact native target plus
`PULSE_PRODUCTION_RELEASE=1`,
`PULSE_RELEASE_SUBMISSION_AUTHORIZATION=target-build-approved`, a Developer ID
identity in `PULSE_APPLE_SIGNING_IDENTITY`, and a bounded notarytool Keychain
profile name in `PULSE_NOTARYTOOL_PROFILE`. Production Windows builds require
the corresponding Authenticode publisher, certificate, timestamp URL, and
signtool inputs. The builders reject missing or mismatched authority.

Each target builder signs the complete native executable closure and emits
normalized `tar.gz` carriers plus a canonical target fragment. The portable
BGE-M3 model and host-neutral plugin runtime are separate exact-tree builds:

```sh
npm run build:portable-model -- \
  --output /absolute/model-output \
  --python /absolute/python \
  --source-model /absolute/pinned-bge-m3-snapshot
npm run build:plugin-runtime -- --output /absolute/plugin-output
```

`npm run build:personal-catalog` combines one or more compatible production
targets, plus the model and plugin runtime, with the protected root and
delegated channel keys. Mac Apple Silicon can be released first:

```sh
npm run build:personal-catalog -- \
  --epoch 1 \
  --origin https://releases.zbs.gg \
  --root-key /absolute/keys/offline-root.pem \
  --channel-key /absolute/keys/preview-channel.pem \
  --model /absolute/build/model \
  --plugin /absolute/build/plugin \
  --target darwin-arm64=/absolute/build/darwin-arm64 \
  --output /absolute/build/catalog
```

All native carriers in this catalog are normalized `tar.gz` archives. A
duplicate, mismatched, unsigned, or corrupt selected target fails the complete
catalog build and removes partial output. The successful build emits one
signed preview manifest, the selected content-addressed target assets, and a
content-free receipt. Other platforms are added only after their own native
build and installation run. Private key material is read only from explicit
absolute paths with private file permissions and is never copied into output.

The Apple Silicon Personal preview is published only through
`.github/workflows/publish-npm.yml`. Its reviewed
`docs/release/PREVIEW_PUBLICATION.json` binds the package version, epoch, exact
archive and tree hashes, signed artifact set, snapshot, and GitHub notes.
`npm run verify:preview-publication` checks that contract. The workflow installs
the exact archive, proves semantic retrieval, publishes the npm `preview` tag
through trusted publishing, and finalizes the matching GitHub prerelease.

Use `build-personal-catalog.mjs --origin https://github.com` for this release
line. It signs flat asset URLs under `zbs-gg/pulse/releases/download/v<version>`.
Stage all four runtime/model carriers, `catalog-artifact-set.json`,
`snapshot.json`, the hash-named npm archive and `SHA256SUMS` in one draft
prerelease whose tag points to the reviewed main commit. Uploads to this draft
are preparation; the existing publication workflow owns public release and npm
publication together. It verifies all staged bytes, exposes the signed assets
for the clean-Mac install, then publishes npm and finalizes the release notes.
If installation fails, the prerelease explicitly remains a candidate with npm
publication unconfirmed. The installer needs no GitHub account or token.

GitHub Releases replaces paid object storage for the new preview. The legacy
Google Cloud release bucket was retired on 2026-09-14, including all versions
of its release artifacts. Fresh installs of 0.7.2 and older previews that
reference that bucket are unavailable. Existing local installations and vaults
are preserved. Select the published 0.8.3 preview explicitly; npm `latest`
still points to 0.7.2 and has not been promoted to a preview.

The separate universal production-candidate workflows do not broaden this
Personal preview's supported platforms. Fixture and signing evidence alone
cannot establish native host acceptance.
