import { execFileSync, execSync, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  copyFileSync, cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  canonicalReleaseJSON,
  pinnedReleaseKeyring,
  verifyReleaseManifestEnvelope,
} from '../src/release-manifest.js';
import { publicMcpPackageManifest } from './public-package-audit.mjs';
import {
  EXPECTED_HELPER_CAPABILITIES,
  EXPECTED_HELPER_CONTRACT_VERSION,
  EXPECTED_HELPER_SELF_TEST,
} from '../src/trust-helper.js';

const scriptDir = dirname(fileURLToPath(import.meta.url));
const cliRoot = resolve(scriptDir, '..');
const appRoot = resolve(cliRoot, '..');
const pulseRoot = resolve(appRoot, '..');
const mcpRoot = join(pulseRoot, 'mcp');
const vendorRoot = join(cliRoot, 'vendor', 'pulse-preview-source');
const expectedHelperIdentifier = 'gg.zbs.pulse.presence-helper';
const expectedHelperTeamID = '44N4NZ86S5';
const nativeHelper = join(appRoot, 'native', 'pulse-presence-helper', 'dist', expectedHelperIdentifier);
const nativeHelperCarrier = `${nativeHelper}.dmg`;
const vendorHelperRoot = join(cliRoot, 'vendor', 'pulse-presence-helper');
const defaultReleaseManifest = join(cliRoot, 'release', 'personal-preview-manifest.json');
const productionPackaging = process.env.npm_lifecycle_event === 'prepublishOnly' ||
  process.env.PULSE_REQUIRE_RELEASE_MANIFEST === '1';

// npm pack runs prepack in every product E2E. Two packs must never race while
// npm ci replaces mcp/node_modules and the generated vendor trees. Re-exec the
// whole preparation under a kernel-held lock before touching either tree.
if (process.env.PULSE_PREVIEW_VENDOR_LOCKED !== '1') {
  const digest = createHash('sha256').update(pulseRoot).digest('hex').slice(0, 20);
  const lockPath = `/tmp/pulse-preview-vendor-${digest}.lock`;
  const lockTool = process.platform === 'darwin' ? '/usr/bin/lockf' : '/usr/bin/flock';
  const lockArgs = process.platform === 'darwin'
    ? ['-k', '-t', '300', lockPath, process.execPath, fileURLToPath(import.meta.url)]
    : ['-w', '300', lockPath, process.execPath, fileURLToPath(import.meta.url)];
  const result = spawnSync(lockTool, lockArgs, {
    env: { ...process.env, PULSE_PREVIEW_VENDOR_LOCKED: '1' },
    stdio: 'inherit',
    timeout: 330_000,
  });
  if (result.status !== 0) {
    throw new Error(`could not acquire the Pulse preview vendor lock (status ${result.status})`);
  }
  process.exit(0);
}

function copyFileList(fromRoot, toRoot, files) {
  mkdirSync(toRoot, { recursive: true });
  for (const file of files) {
    const src = join(fromRoot, file);
    const dest = join(toRoot, file);
    if (!existsSync(src)) {
      continue;
    }
    mkdirSync(dirname(dest), { recursive: true });
    cpSync(src, dest);
  }
}

function copyTree(from, to) {
  cpSync(from, to, {
    recursive: true,
    filter: (src) => {
      const name = src.split('/').pop() ?? '';
      return !(
        name === 'node_modules'
        || name === 'dist'
        || name === '.DS_Store'
        || name.endsWith('_test.go')
        || name.endsWith('.test.ts')
        || src.includes('/testdata/')
      );
    },
  });
}

function verifyHelperProtocol(helperPath) {
  const run = (command) => spawnSync(helperPath, [command], {
    encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 5_000,
  });
  const contractResult = run('contract');
  const selfTestResult = run('self-test');
  if (contractResult.status !== 0 || selfTestResult.status !== 0) return false;
  try {
    const contract = JSON.parse(contractResult.stdout);
    const selfTest = JSON.parse(selfTestResult.stdout);
    return contract.schema === 'pulse.presence_helper.contract.v1' &&
      contract.version === EXPECTED_HELPER_CONTRACT_VERSION &&
      JSON.stringify(contract.capabilities) === JSON.stringify(EXPECTED_HELPER_CAPABILITIES) &&
      JSON.stringify(contract.self_test) === JSON.stringify(EXPECTED_HELPER_SELF_TEST) &&
      Object.keys(selfTest).sort().join('\0') === 'contract_version\0schema\0status\0suite\0vectors' &&
      selfTest.status === 'pass' &&
      Object.keys(EXPECTED_HELPER_SELF_TEST).every((key) => selfTest[key] === EXPECTED_HELPER_SELF_TEST[key]);
  } catch {
    return false;
  }
}

rmSync(vendorRoot, { recursive: true, force: true });

if (!existsSync(nativeHelper)) {
  throw new Error('signed Pulse presence helper is missing; run npm run build:presence-helper before packing');
}
if (process.platform === 'darwin') {
  execFileSync('/usr/bin/codesign', ['--verify', '--strict', '--verbose=2', nativeHelper], {
    stdio: ['ignore', 'ignore', 'inherit'],
  });
  const signature = spawnSync('/usr/bin/codesign', ['-d', '--verbose=4', nativeHelper], {
    encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'],
  });
  const signatureText = `${signature.stdout ?? ''}\n${signature.stderr ?? ''}`;
  if (signature.status !== 0 || !signatureText.includes(`Identifier=${expectedHelperIdentifier}`) ||
      !signatureText.includes(`TeamIdentifier=${expectedHelperTeamID}`)) {
    throw new Error('Pulse presence helper has the wrong code-signing identity');
  }
  const buildVersion = spawnSync('/usr/bin/vtool', ['-show-build', nativeHelper], {
    encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'],
  });
  const architectures = spawnSync('/usr/bin/lipo', ['-archs', nativeHelper], {
    encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'],
  });
  const shippedArchitectures = new Set(architectures.stdout.trim().split(/\s+/).filter(Boolean));
  const minimumVersions = [...buildVersion.stdout.matchAll(/\bminos ([0-9.]+)/g)].map((match) => match[1]);
  const macOSPlatforms = [...buildVersion.stdout.matchAll(/\bplatform MACOS\b/g)];
  const architectureSetValid = productionPackaging
    ? shippedArchitectures.size === 1 && shippedArchitectures.has('arm64')
    : shippedArchitectures.has('arm64') &&
      [...shippedArchitectures].every((architecture) => architecture === 'arm64' || architecture === 'x86_64');
  if (architectures.status !== 0 || !architectureSetValid ||
      buildVersion.status !== 0 || macOSPlatforms.length !== shippedArchitectures.size ||
      minimumVersions.length !== shippedArchitectures.size ||
      minimumVersions.some((version) => version !== '13.0')) {
    throw new Error('Pulse presence helper must include Apple Silicon and target macOS 13.0; production release must be arm64-only');
  }
  const assessment = spawnSync('/usr/sbin/spctl', ['-a', '-vv', '-t', 'exec', nativeHelper], {
    encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'],
  });
  if (assessment.status !== 0) {
    if (process.env.npm_lifecycle_event === 'prepublishOnly') {
      throw new Error('refusing npm publish: Pulse presence helper has no accepted notarization ticket');
    }
    if (process.env.PULSE_ALLOW_UNNOTARIZED_INTERNAL_PREVIEW !== '1') {
      throw new Error('Pulse presence helper is not notarized; only an explicit unquarantined internal preview pack may continue');
    }
    console.error('[pulse] explicit internal-preview override: helper is signed but not notarized; do not redistribute this tarball');
  }
}

if (productionPackaging) {
  const releaseManifestPath = process.env.PULSE_RELEASE_MANIFEST_PATH ?? defaultReleaseManifest;
  const releaseRootPath = process.env.PULSE_RELEASE_TEST_ROOT_PATH;
  if ((process.env.PULSE_RELEASE_MANIFEST_PATH || releaseRootPath) && process.env.PULSE_RELEASE_TEST_MODE !== '1') {
    throw new Error('release manifest/root overrides are forbidden outside explicit test mode');
  }
  if (!existsSync(releaseManifestPath)) {
    throw new Error('refusing production packaging: canonical signed Personal release manifest is missing');
  }
  const manifestBytes = readFileSync(releaseManifestPath, 'utf8');
  const envelope = JSON.parse(manifestBytes);
  if (manifestBytes !== `${canonicalReleaseJSON(envelope)}\n`) {
    throw new Error('refusing production packaging: release manifest envelope is not canonical');
  }
  const packageJSON = JSON.parse(readFileSync(join(cliRoot, 'package.json'), 'utf8'));
  const macOS = spawnSync('/usr/bin/sw_vers', ['-productVersion'], {
    encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'],
  });
  if (macOS.status !== 0 || !/^\d+\.\d+(?:\.\d+)?\n?$/.test(macOS.stdout)) {
    throw new Error('refusing production packaging: cannot determine the macOS compatibility version');
  }
  const verified = verifyReleaseManifestEnvelope(envelope, {
    architecture: 'arm64',
    minimumAcceptedEpoch: envelope?.payload?.release?.epoch,
    now: new Date(),
    osVersion: macOS.stdout.trim(),
    packageVersion: packageJSON.version,
    platform: 'darwin',
    trustedKeys: pinnedReleaseKeyring(releaseRootPath),
  });
  const helperArtifact = verified.artifacts['presence-helper'];
  if (!existsSync(nativeHelperCarrier)) {
    throw new Error('refusing production packaging: stapled presence-helper DMG is missing');
  }
  execFileSync('/usr/bin/xcrun', ['stapler', 'validate', nativeHelperCarrier], { stdio: 'inherit' });
  execFileSync('/usr/sbin/spctl', [
    '-a', '-t', 'open', '--context', 'context:primary-signature', '-vv', nativeHelperCarrier,
  ], { stdio: 'inherit' });
  const carrierDigest = createHash('sha256').update(readFileSync(nativeHelperCarrier)).digest('hex');
  if (helperArtifact.format !== 'dmg' || helperArtifact.bytes !== statSync(nativeHelperCarrier).size || helperArtifact.sha256 !== carrierDigest ||
      helperArtifact.signing.identifier !== expectedHelperIdentifier ||
      helperArtifact.signing.team_id !== expectedHelperTeamID) {
    throw new Error('refusing production packaging: presence helper does not match the signed release manifest');
  }
  const verificationRoot = mkdtempSync(join(tmpdir(), 'pulse-package-carrier-'));
  const mountPoint = join(verificationRoot, 'mount');
  mkdirSync(mountPoint, { mode: 0o700 });
  let attached = false;
  try {
    execFileSync('/usr/bin/hdiutil', [
      'attach', '-readonly', '-nobrowse', '-mountpoint', mountPoint, nativeHelperCarrier,
    ], { stdio: 'inherit' });
    attached = true;
    const carrierHelper = join(mountPoint, 'bin', expectedHelperIdentifier);
    execFileSync('/usr/bin/codesign', ['--verify', '--strict', '--verbose=2', carrierHelper], { stdio: 'inherit' });
    const innerDigest = createHash('sha256').update(readFileSync(carrierHelper)).digest('hex');
    const sourceDigest = createHash('sha256').update(readFileSync(nativeHelper)).digest('hex');
    if (innerDigest !== sourceDigest || !verifyHelperProtocol(carrierHelper)) {
      throw new Error('refusing production packaging: presence-helper DMG inner binary is incompatible');
    }
  } finally {
    if (attached) execFileSync('/usr/bin/hdiutil', ['detach', mountPoint], { stdio: 'inherit' });
    rmSync(verificationRoot, { recursive: true, force: true });
  }
}
rmSync(vendorHelperRoot, { recursive: true, force: true });
mkdirSync(vendorHelperRoot, { recursive: true });
cpSync(nativeHelper, join(vendorHelperRoot, expectedHelperIdentifier));

const vendorApp = join(vendorRoot, 'pulse-app');
copyFileList(appRoot, vendorApp, [
  'go.mod',
  'go.sum',
  'LICENSE',
]);
copyTree(join(appRoot, 'cmd', 'pulse'), join(vendorApp, 'cmd', 'pulse'));
copyTree(join(appRoot, 'internal'), join(vendorApp, 'internal'));

const vendorMcp = join(vendorRoot, 'mcp');
copyFileList(mcpRoot, vendorMcp, [
  'package.json',
  'package-lock.json',
  'LICENSE',
  'README.md',
  'README_DEV_PREVIEW.md',
  'tsconfig.json',
]);
const sourceMcpPackageJSON = JSON.parse(readFileSync(join(mcpRoot, 'package.json'), 'utf8'));
writeFileSync(
  join(vendorMcp, 'package.json'),
  `${JSON.stringify(publicMcpPackageManifest(sourceMcpPackageJSON), null, 2)}\n`,
);
// Whole src tree (minus tests): index.ts alone no longer builds since standalone.ts.
copyTree(join(mcpRoot, 'src'), join(vendorMcp, 'src'));
mkdirSync(join(vendorMcp, 'scripts'), { recursive: true });
copyFileSync(
  join(cliRoot, 'scripts', 'connector-smoke.mjs'),
  join(vendorMcp, 'scripts', 'claude-connector-smoke.mjs'),
);

// Prebuilt MCP server so `pulse mcp` works from the published package with no
// TS toolchain on the user machine.
const vendorMcpDist = join(cliRoot, 'vendor', 'pulse-mcp-dist');
rmSync(vendorMcpDist, { recursive: true, force: true });
execSync('npm ci', { cwd: mcpRoot, stdio: ['ignore', 'inherit', 'inherit'] });
execSync('npm run build', { cwd: mcpRoot, stdio: ['ignore', 'inherit', 'inherit'] });
cpSync(join(mcpRoot, 'dist'), vendorMcpDist, { recursive: true });

writeFileSync(join(vendorRoot, 'README.md'), `# Pulse preview vendor source

This directory is generated by \`scripts/prepare-preview-vendor.mjs\` during
\`npm pack\` / \`npm publish\`.

It contains the minimal source needed for the Pulse preview CLI to build and
start the local Pulse daemon and MCP package.

It intentionally excludes tests, documentation archives, agent instructions,
node_modules, dist, local databases, secrets, raw archives, and private
operator artifacts.
`);

console.error(`[pulse] prepared preview vendor source at ${vendorRoot}`);
