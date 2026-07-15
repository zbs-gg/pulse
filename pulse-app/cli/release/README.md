# Personal Preview release trust

`pulse-release-root.pem` is the pinned verification root consumed by the
installer. The matching private key is never stored in this repository.

The checked-in root is a fail-closed bootstrap root: its generated private key
was not retained. Before the first distributable preview, an authorized release
ceremony must replace it with the public half of a protected Ed25519 release
key. That audited package change establishes the npm trust root. A canonical
`personal-preview-manifest.json` signed by that key must then be created with:

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
