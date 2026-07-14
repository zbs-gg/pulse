import { execFileSync, spawnSync } from 'node:child_process';
import { chmodSync, mkdirSync, rmSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

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
const architectures = Object.freeze(['arm64', 'x86_64']);
const identity = process.env.PULSE_PRESENCE_CODESIGN_IDENTITY ?? DEFAULT_IDENTITY;

if (process.platform !== 'darwin') {
  throw new Error('the Pulse presence helper can only be built and signed on macOS');
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

const assessment = spawnSync('/usr/sbin/spctl', ['-a', '-vv', '-t', 'exec', output], {
  encoding: 'utf8',
  stdio: ['ignore', 'pipe', 'pipe'],
});
if (process.env.PULSE_REQUIRE_NOTARIZED === '1' && assessment.status !== 0) {
  rmSync(output, { force: true });
  throw new Error(`presence helper is signed but not accepted by Gatekeeper: ${assessment.stderr.trim()}`);
}

console.error(`[pulse] signed presence helper: ${output}`);
if (assessment.status !== 0) {
  console.error('[pulse] warning: Gatekeeper does not report a notarized ticket; internal preview installs must use an unquarantined npm package.');
}
