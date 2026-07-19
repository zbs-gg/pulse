import { isAbsolute, relative, resolve, sep } from 'node:path';

const SAFE_FIXTURE_ID = /^[a-z0-9][a-z0-9_-]{0,127}$/;

function contained(root, path) {
  if (typeof path !== 'string' || !isAbsolute(path) || resolve(path) !== path) return false;
  const value = relative(root, path);
  return value !== '' && value !== '..' && !value.startsWith(`..${sep}`) && !isAbsolute(value);
}

function exactFixtureProfile(profile) {
  return profile && !Array.isArray(profile) && typeof profile === 'object' &&
    Object.keys(profile).sort().join('\0') === 'fixture_id\0kind\0production' &&
    profile.kind === 'fixture' && profile.production === false &&
    typeof profile.fixture_id === 'string' && SAFE_FIXTURE_ID.test(profile.fixture_id);
}

export function nativePackedFixtureAttestation({
  cwd,
  dataDir,
  env = process.env,
  home,
  plan,
} = {}) {
  if (env.PULSE_NATIVE_PACKED_FIXTURE_ATTESTATION !== '1' ||
      env.PULSE_RELEASE_TEST_MODE !== '1' || env.PULSE_TRUST_MODE !== 'test' ||
      typeof env.PULSE_NATIVE_PACKED_FIXTURE_ROOT !== 'string' ||
      !isAbsolute(env.PULSE_NATIVE_PACKED_FIXTURE_ROOT)) return null;
  const root = resolve(env.PULSE_NATIVE_PACKED_FIXTURE_ROOT);
  if (root !== env.PULSE_NATIVE_PACKED_FIXTURE_ROOT ||
      !contained(root, cwd) || !contained(root, dataDir) || !contained(root, home)) return null;
  const port = Number(env.PULSE_NATIVE_PACKED_FIXTURE_PORT);
  if (!Number.isSafeInteger(port) || port < 1024 || port > 65535 || String(port) !== env.PULSE_NATIVE_PACKED_FIXTURE_PORT) {
    return null;
  }
  const isolatedPaths = [
    env.PULSE_BINDING_REGISTRY_PATH,
    env.PULSE_BINDING_PUBLIC_KEY_PATH,
    env.PULSE_BINDING_ANCHOR_PATH,
    env.PULSE_RELEASE_MANIFEST_PATH,
    env.PULSE_RELEASE_TEST_ROOT_PATH,
    env.PULSE_RELEASE_TEST_ASSET_ROOT,
    env.PULSE_NATIVE_PACKED_FIXTURE_BINDING_KEY_PATH,
  ];
  if (isolatedPaths.some((path) => !contained(root, path)) ||
      env.PULSE_RELEASE_TEST_MATERIALIZER_SPEC || env.PULSE_CODEX_MARKETPLACE_SOURCE) return null;
  if (!plan || plan.schema !== 'pulse.personal_install_plan.v2' || plan.contract_version !== 2 ||
      plan.detected?.workspace?.canonical_path !== cwd ||
      plan.release?.catalog_schema !== 'pulse.personal_preview.release_catalog.v2' ||
      !exactFixtureProfile(plan.release?.verification_profile)) return null;
  return Object.freeze({ fixture_id: plan.release.verification_profile.fixture_id, port, root });
}
