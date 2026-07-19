import { dirname, isAbsolute, relative, resolve, sep } from 'node:path';

import { defaultPlatformServices } from './platform-services.js';

const SAFE_FIXTURE_ID = /^[a-z0-9][a-z0-9_-]{0,127}$/;

function contained(root, path, platformServices) {
  if (typeof path !== 'string' || !isAbsolute(path)) return false;
  if (resolve(path) === path) {
    const value = relative(root, path);
    if (value !== '' && value !== '..' && !value.startsWith(`..${sep}`) && !isAbsolute(value)) return true;
  }
  // Windows can spell the same directory through short and long path forms.
  // When lexical containment disagrees, walk native, no-reparse directory
  // identities until the fixture root itself is proven as a strict ancestor.
  try {
    const rootIdentity = platformServices.inspectPathIdentity(root, { kind: 'directory' }).identity_token;
    let current = path;
    for (let depth = 0; depth < 256; depth += 1) {
      const identity = platformServices.inspectPathIdentity(current, { kind: 'directory' }).identity_token;
      if (depth > 0 && identity === rootIdentity) return true;
      const parent = dirname(current);
      if (parent === current) return false;
      current = parent;
    }
    return false;
  } catch {
    return false;
  }
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
  platformServices = defaultPlatformServices,
} = {}) {
  if (env.PULSE_NATIVE_PACKED_FIXTURE_ATTESTATION !== '1' ||
      env.PULSE_RELEASE_TEST_MODE !== '1' || env.PULSE_TRUST_MODE !== 'test' ||
      typeof env.PULSE_NATIVE_PACKED_FIXTURE_ROOT !== 'string' ||
      !isAbsolute(env.PULSE_NATIVE_PACKED_FIXTURE_ROOT)) return null;
  const root = resolve(env.PULSE_NATIVE_PACKED_FIXTURE_ROOT);
  if (root !== env.PULSE_NATIVE_PACKED_FIXTURE_ROOT ||
      !contained(root, cwd, platformServices) || !contained(root, dataDir, platformServices) ||
      !contained(root, home, platformServices)) return null;
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
  if (isolatedPaths.some((path) => !contained(root, path, platformServices)) ||
      env.PULSE_RELEASE_TEST_MATERIALIZER_SPEC || env.PULSE_CODEX_MARKETPLACE_SOURCE) return null;
  if (!plan || plan.schema !== 'pulse.personal_install_plan.v2' || plan.contract_version !== 2 ||
      plan.detected?.workspace?.canonical_path !== cwd ||
      plan.release?.catalog_schema !== 'pulse.personal_preview.release_catalog.v2' ||
      !exactFixtureProfile(plan.release?.verification_profile)) return null;
  return Object.freeze({ fixture_id: plan.release.verification_profile.fixture_id, port, root });
}
