import { execFileSync, spawnSync } from 'node:child_process';
import { chmodSync, copyFileSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  EXPECTED_HELPER_CAPABILITIES,
  EXPECTED_HELPER_CONTRACT_VERSION,
  EXPECTED_HELPER_SELF_TEST,
} from '../src/trust-helper.js';

const EXPECTED_IDENTIFIER = 'gg.zbs.pulse.presence-helper';
const EXPECTED_TEAM_ID = '44N4NZ86S5';
const DEFAULT_IDENTITY = `Developer ID Application: Nikita Shilov (${EXPECTED_TEAM_ID})`;

const scriptDir = dirname(fileURLToPath(import.meta.url));
const cliRoot = resolve(scriptDir, '..');
const helperRoot = resolve(cliRoot, '..', 'native', 'pulse-presence-helper');
const source = join(helperRoot, 'main.swift');
const entitlements = join(helperRoot, 'pulse-presence-helper.entitlements');
const outputDir = join(helperRoot, 'dist');
const output = join(outputDir, EXPECTED_IDENTIFIER);
const carrier = `${output}.dmg`;
const carrierRoot = join(outputDir, '.presence-helper-carrier');
const architectures = Object.freeze(['arm64']);
const identity = process.env.PULSE_PRESENCE_CODESIGN_IDENTITY ?? DEFAULT_IDENTITY;
const productionRelease = process.env.PULSE_PRODUCTION_RELEASE === '1';
const notaryProfile = process.env.PULSE_NOTARYTOOL_PROFILE;

if (process.platform !== 'darwin') {
  throw new Error('the Pulse presence helper can only be built and signed on macOS');
}
if (productionRelease && (typeof notaryProfile !== 'string' || notaryProfile.length < 1 || notaryProfile.length > 128 || /[\r\n\0]/.test(notaryProfile))) {
  throw new Error('production presence-helper release requires an authorized PULSE_NOTARYTOOL_PROFILE');
}

mkdirSync(outputDir, { recursive: true, mode: 0o755 });
rmSync(output, { force: true });
const slices = architectures.map((architecture) => `${output}.${architecture}`);
try {
  for (const [index, architecture] of architectures.entries()) {
    rmSync(slices[index], { force: true });
    execFileSync('/usr/bin/swiftc', [
      '-parse-as-library',
      '-target', `${architecture}-apple-macos13.0`,
      source,
      '-o', slices[index],
      '-framework', 'AppKit',
      '-framework', 'LocalAuthentication',
      '-framework', 'Security',
      '-framework', 'CryptoKit',
    ], { stdio: 'inherit' });
  }
  execFileSync('/usr/bin/lipo', ['-create', ...slices, '-output', output], { stdio: 'inherit' });
} finally {
  for (const slice of slices) rmSync(slice, { force: true });
}

execFileSync('/usr/bin/codesign', [
  '--force',
  '--identifier', EXPECTED_IDENTIFIER,
  '--options', 'runtime',
  '--timestamp',
  '--entitlements', entitlements,
  '--sign', identity,
  output,
], { stdio: 'inherit' });
execFileSync('/usr/bin/codesign', ['--verify', '--strict', '--verbose=2', output], { stdio: 'inherit' });

const details = spawnSync('/usr/bin/codesign', ['-d', '--verbose=4', output], {
  encoding: 'utf8',
  stdio: ['ignore', 'pipe', 'pipe'],
});
const text = `${details.stdout ?? ''}\n${details.stderr ?? ''}`;
if (details.status !== 0 || !text.includes(`Identifier=${EXPECTED_IDENTIFIER}`) ||
    !text.includes(`TeamIdentifier=${EXPECTED_TEAM_ID}`)) {
  rmSync(output, { force: true });
  throw new Error('signed presence helper identity does not match the Pulse trust contract');
}
chmodSync(output, 0o755);

function helperJSON(command) {
  const result = spawnSync(output, [command], {
    encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 5_000,
  });
  if (result.status !== 0 || result.signal != null || typeof result.stdout !== 'string' || result.stdout.length > 4096) {
    rmSync(output, { force: true });
    throw new Error(`presence helper ${command} contract failed`);
  }
  try { return JSON.parse(result.stdout); } catch {
    rmSync(output, { force: true });
    throw new Error(`presence helper ${command} returned invalid JSON`);
  }
}

const contract = helperJSON('contract');
if (contract.schema !== 'pulse.presence_helper.contract.v1' ||
    contract.version !== EXPECTED_HELPER_CONTRACT_VERSION ||
    JSON.stringify(contract.capabilities) !== JSON.stringify(EXPECTED_HELPER_CAPABILITIES) ||
    JSON.stringify(contract.self_test) !== JSON.stringify(EXPECTED_HELPER_SELF_TEST)) {
  rmSync(output, { force: true });
  throw new Error('presence helper capability contract does not match the CLI verifier');
}
const selfTest = helperJSON('self-test');
if (selfTest.status !== 'pass' ||
    JSON.stringify(Object.fromEntries(Object.keys(EXPECTED_HELPER_SELF_TEST).map((key) => [key, selfTest[key]]))) !==
      JSON.stringify(EXPECTED_HELPER_SELF_TEST) ||
    Object.keys(selfTest).sort().join('\0') !== 'contract_version\0schema\0status\0suite\0vectors') {
  rmSync(output, { force: true });
  throw new Error('presence helper self-test contract does not match the CLI verifier');
}

rmSync(carrier, { force: true });
rmSync(carrierRoot, { recursive: true, force: true });
try {
  mkdirSync(carrierRoot, { recursive: true, mode: 0o755 });
  const carrierHelper = join(carrierRoot, EXPECTED_IDENTIFIER);
  copyFileSync(output, carrierHelper);
  chmodSync(carrierHelper, 0o755);
  execFileSync('/usr/bin/hdiutil', [
    'create', '-volname', 'Pulse Presence Helper', '-srcfolder', carrierRoot,
    '-format', 'UDZO', '-ov', carrier,
  ], { stdio: 'inherit' });
} finally {
  rmSync(carrierRoot, { recursive: true, force: true });
}
execFileSync('/usr/bin/codesign', [
  '--force', '--timestamp', '--sign', identity, carrier,
], { stdio: 'inherit' });
execFileSync('/usr/bin/codesign', ['--verify', '--strict', '--verbose=2', carrier], { stdio: 'inherit' });

function verifyCarrierInner() {
  const verificationRoot = mkdtempSync(join(tmpdir(), 'pulse-presence-carrier-'));
  const mountPoint = join(verificationRoot, 'mount');
  mkdirSync(mountPoint, { mode: 0o700 });
  let attached = false;
  try {
    execFileSync('/usr/bin/hdiutil', [
      'attach', '-readonly', '-nobrowse', '-mountpoint', mountPoint, carrier,
    ], { stdio: 'inherit' });
    attached = true;
    const innerHelper = join(mountPoint, EXPECTED_IDENTIFIER);
    execFileSync('/usr/bin/codesign', ['--verify', '--strict', '--verbose=2', innerHelper], { stdio: 'inherit' });
    for (const command of ['contract', 'self-test']) {
      const expected = command === 'contract'
        ? { schema: 'pulse.presence_helper.contract.v1', version: EXPECTED_HELPER_CONTRACT_VERSION }
        : { ...EXPECTED_HELPER_SELF_TEST, status: 'pass' };
      const actual = spawnSync(innerHelper, [command], {
        encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 5_000,
      });
      if (actual.status !== 0 || actual.signal != null) throw new Error(`carrier inner helper ${command} failed`);
      const value = JSON.parse(actual.stdout);
      if (command === 'contract') {
        if (value.schema !== expected.schema || value.version !== expected.version ||
            JSON.stringify(value.capabilities) !== JSON.stringify(EXPECTED_HELPER_CAPABILITIES) ||
            JSON.stringify(value.self_test) !== JSON.stringify(EXPECTED_HELPER_SELF_TEST)) {
          throw new Error('carrier inner helper contract mismatch');
        }
      } else {
        if (Object.keys(value).sort().join('\0') !== 'contract_version\0schema\0status\0suite\0vectors' ||
            value.status !== 'pass' ||
            Object.keys(EXPECTED_HELPER_SELF_TEST).some((key) => value[key] !== EXPECTED_HELPER_SELF_TEST[key])) {
          throw new Error('carrier inner helper self-test mismatch');
        }
      }
    }
  } finally {
    if (attached) execFileSync('/usr/bin/hdiutil', ['detach', mountPoint], { stdio: 'inherit' });
    rmSync(verificationRoot, { recursive: true, force: true });
  }
}

if (productionRelease) {
  try {
    execFileSync('/usr/bin/xcrun', ['notarytool', 'submit', carrier, '--keychain-profile', notaryProfile, '--wait'], { stdio: 'inherit' });
    execFileSync('/usr/bin/xcrun', ['stapler', 'staple', carrier], { stdio: 'inherit' });
    execFileSync('/usr/bin/xcrun', ['stapler', 'validate', carrier], { stdio: 'inherit' });
    execFileSync('/usr/sbin/spctl', [
      '-a', '-t', 'open', '--context', 'context:primary-signature', '-vv', carrier,
    ], { stdio: 'inherit' });
    verifyCarrierInner();
  } catch (error) {
    rmSync(carrier, { force: true });
    throw error;
  }
} else {
  verifyCarrierInner();
}

const assessment = spawnSync('/usr/sbin/spctl', ['-a', '-vv', '-t', 'exec', output], {
  encoding: 'utf8',
  stdio: ['ignore', 'pipe', 'pipe'],
});
if ((productionRelease || process.env.PULSE_REQUIRE_NOTARIZED === '1') && assessment.status !== 0) {
  rmSync(carrier, { force: true });
  rmSync(output, { force: true });
  throw new Error(`presence helper is signed but not accepted by Gatekeeper: ${assessment.stderr.trim()}`);
}

console.error(`[pulse] signed presence helper: ${output}`);
console.error(`[pulse] presence helper carrier: ${carrier}`);
if (assessment.status !== 0) {
  console.error('[pulse] warning: Gatekeeper does not report a notarized ticket; internal preview installs must use an unquarantined npm package.');
}
