import assert from 'node:assert/strict';
import test from 'node:test';

import { nativePackedFixtureAttestation } from './native-packed-fixture.js';

function fixture() {
  const root = '/private/tmp/pulse-native-packed';
  const cwd = `${root}/workspace`;
  const home = `${root}/home`;
  const dataDir = `${root}/data`;
  return {
    cwd,
    dataDir,
    home,
    env: {
      PULSE_NATIVE_PACKED_FIXTURE_ATTESTATION: '1',
      PULSE_NATIVE_PACKED_FIXTURE_ROOT: root,
      PULSE_NATIVE_PACKED_FIXTURE_PORT: '28789',
      PULSE_RELEASE_TEST_MODE: '1',
      PULSE_TRUST_MODE: 'test',
      PULSE_BINDING_REGISTRY_PATH: `${root}/trust/bindings.json`,
      PULSE_BINDING_PUBLIC_KEY_PATH: `${root}/trust/bindings.pub`,
      PULSE_BINDING_ANCHOR_PATH: `${root}/trust/bindings.anchor`,
      PULSE_RELEASE_MANIFEST_PATH: `${root}/release/manifest.json`,
      PULSE_RELEASE_TEST_ROOT_PATH: `${root}/release/root.pem`,
      PULSE_RELEASE_TEST_ASSET_ROOT: `${root}/release/assets`,
      PULSE_NATIVE_PACKED_FIXTURE_BINDING_KEY_PATH: `${root}/trust/bindings.key`,
    },
    plan: {
      schema: 'pulse.personal_install_plan.v2',
      contract_version: 2,
      detected: { workspace: { canonical_path: cwd } },
      release: {
        catalog_schema: 'pulse.personal_preview.release_catalog.v2',
        verification_profile: { fixture_id: 'native-darwin-arm64', kind: 'fixture', production: false },
      },
    },
  };
}

test('native packed fixture attestation is exact, isolated, and visibly non-production', () => {
  const input = fixture();
  assert.deepEqual(nativePackedFixtureAttestation(input), {
    fixture_id: 'native-darwin-arm64', port: 28789, root: '/private/tmp/pulse-native-packed',
  });
  assert.equal(nativePackedFixtureAttestation({
    ...input, dataDir: '/Users/person/.pulse',
  }), null);
  assert.equal(nativePackedFixtureAttestation({
    ...input, env: { ...input.env, PULSE_CODEX_MARKETPLACE_SOURCE: `${input.env.PULSE_NATIVE_PACKED_FIXTURE_ROOT}/fake` },
  }), null);
  assert.equal(nativePackedFixtureAttestation({
    ...input,
    plan: { ...input.plan, release: { ...input.plan.release,
      verification_profile: { fixture_id: 'native-darwin-arm64', kind: 'fixture', production: true } } },
  }), null);
});
