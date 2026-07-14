import { execFileSync, execSync, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { cpSync, existsSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDir = dirname(fileURLToPath(import.meta.url));
const cliRoot = resolve(scriptDir, '..');
const appRoot = resolve(cliRoot, '..');
const pulseRoot = resolve(appRoot, '..');
const mcpRoot = join(pulseRoot, 'mcp');
const vendorRoot = join(cliRoot, 'vendor', 'pulse-preview-source');
const expectedHelperIdentifier = 'gg.zbs.pulse.presence-helper';
const expectedHelperTeamID = '44N4NZ86S5';
const nativeHelper = join(appRoot, 'native', 'pulse-presence-helper', 'dist', expectedHelperIdentifier);
const vendorHelperRoot = join(cliRoot, 'vendor', 'pulse-presence-helper');

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
  if (architectures.status !== 0 || !shippedArchitectures.has('arm64') ||
      [...shippedArchitectures].some((name) => name !== 'arm64' && name !== 'x86_64') ||
      buildVersion.status !== 0 || macOSPlatforms.length !== shippedArchitectures.size ||
      minimumVersions.length !== shippedArchitectures.size ||
      minimumVersions.some((version) => version !== '13.0')) {
    throw new Error('Pulse presence helper must include Apple Silicon and target macOS 13.0');
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
// Whole src tree (minus tests): index.ts alone no longer builds since standalone.ts.
copyTree(join(mcpRoot, 'src'), join(vendorMcp, 'src'));
copyTree(join(mcpRoot, 'scripts'), join(vendorMcp, 'scripts'));
copyTree(join(mcpRoot, 'docs'), join(vendorMcp, 'docs'));

// Prebuilt MCP server so `pulse mcp` works from the published package with no
// TS toolchain on the user machine.
const vendorMcpDist = join(cliRoot, 'vendor', 'pulse-mcp-dist');
rmSync(vendorMcpDist, { recursive: true, force: true });
execSync('npm ci', { cwd: mcpRoot, stdio: ['ignore', 'inherit', 'inherit'] });
execSync('npm run build', { cwd: mcpRoot, stdio: ['ignore', 'inherit', 'inherit'] });
cpSync(join(mcpRoot, 'dist'), vendorMcpDist, { recursive: true });

copyFileList(pulseRoot, vendorRoot, [
  'AGENTS.md',
]);
copyTree(join(pulseRoot, 'docs'), join(vendorRoot, 'docs'));

writeFileSync(join(vendorRoot, 'README.md'), `# Pulse preview vendor source

This directory is generated by \`scripts/prepare-preview-vendor.mjs\` during
\`npm pack\` / \`npm publish\`.

It contains the minimal source needed for the Pulse preview CLI to build and
start the local Pulse daemon and MCP package.

It intentionally excludes tests, node_modules, dist, local databases, secrets,
raw archives, and private operator artifacts.
`);

console.error(`[pulse] prepared preview vendor source at ${vendorRoot}`);
