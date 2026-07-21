# Personal Preview release trust

`pulse-release-root.pem` is the pinned verification root consumed by the
installer. The matching private key is never stored in this repository.

The checked-in root is the public half of the protected Personal Preview
Ed25519 release key. The private half is kept outside the repository with mode
`0600`; it must be backed up through the release operator's protected secret
storage before publication. A canonical `personal-preview-manifest.json`
signed by that key is created with:

```sh
PULSE_RELEASE_SIGNING_KEY_PATH=/absolute/private/key.pem \
  npm run sign:release-manifest -- \
  release/personal-preview-manifest.payload.json \
  release/personal-preview-manifest.json
```

Production packaging rejects a missing, non-canonical, incorrectly signed, or
expired manifest and rejects a presence helper that does not match its signed
artifact record. Native executables are distributed only in notarized, stapled
`.dmg` carriers; the plugin runtime is an exact `.tar.gz`; the model is a
data-only `.safetensors` artifact with custom code forbidden. The unsigned
payload and generated signed manifest are release inputs/outputs; the protected
private key never enters npm or git.

The repeatable release ceremony is `npm run build:personal-release`. Run it
first with `-- --check`; this validates the protected Ed25519 key, pinned MLX
weights, pinned BAAI reference snapshot, release epoch, and Apple credential
without submitting anything. A real build additionally requires
`PULSE_PRODUCTION_RELEASE=1` and the exact
`PULSE_RELEASE_SUBMISSION_AUTHORIZATION=apple-notarization-approved` acknowledgement.
It builds all five manifest artifacts, submits the three native DMGs to Apple,
staples and re-materializes them through the production installer, signs the
canonical manifest, and leaves an unpublished receipt under `release/dist/`.
