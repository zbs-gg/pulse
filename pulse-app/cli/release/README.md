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

`npm run build:personal-catalog` combines one compatible production target,
the model, and the plugin runtime with the protected root and delegated
channel keys. It emits the signed preview manifest, content-addressed assets,
and a content-free receipt. Private key material is read only from explicit
absolute paths with private file permissions and is never copied into output.

These builders are release primitives, not publication authority. The npm
`preview` tag must remain unchanged until a reviewed `main` commit has a green
required native matrix, a protected production-candidate workflow has emitted
the exact candidate bytes, and the separately confirmed staging/2FA promotion
has accepted those unchanged bytes. Until that workflow exists and passes,
this checkout is pending publication.
