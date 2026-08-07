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

These builders are release primitives, not publication authority.
`.github/workflows/production-candidate.yml` is the protected orchestrator. It
accepts only a reviewed `main` commit with a green `Universal` push run, builds
on all six native runners, signs/notarizes Apple and Windows code in separate
protected environments, exports the common model, and creates one root-signed
catalog. It then packs one npm candidate whose embedded manifest must match
that catalog byte-for-byte. The candidate carries `support_claim:false`; a
green build proves release bytes, not a rollout support claim.

The workflow requires reviewer-gated GitHub Environments named
`production-linux`, `production-apple`, `production-windows`,
`production-model`, `production-catalog`, and `production-candidate`. Signing
and catalog key values live only in their corresponding Environment secrets;
the workflow checks that they exist without printing them and deletes its
ephemeral keychains/key files. It emits retained GitHub artifacts but does not
upload release assets, change an npm tag, publish, or approve a staged package.

The npm `preview` tag must remain unchanged until that exact production
candidate is separately accepted by `stage-npm-preview.yml` and then approved
with npm 2FA. A local unsigned build, a DMG by itself, or the PR fixture matrix
can never cross this boundary.
