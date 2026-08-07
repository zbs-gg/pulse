#!/usr/bin/env node

import { spawnSync } from 'node:child_process';
import { lstatSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { auditPublicPackageRoot } from './public-package-audit.mjs';

const cliRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: options.cwd ?? cliRoot,
    env: options.env ?? process.env,
    encoding: 'utf8',
    timeout: options.timeout ?? 330_000,
    maxBuffer: 32 * 1024 * 1024,
  });
  if (result.status !== 0) {
    throw new Error(`${command} ${args.join(' ')} failed: ${String(result.stderr).trim()}`);
  }
  return result.stdout;
}

const root = mkdtempSync(join(tmpdir(), 'pulse-personal-package-verify.'));
try {
  const packedJSON = JSON.parse(run('npm', [
    'pack', '--json', '--silent', '--pack-destination', root,
  ]));
  if (!Array.isArray(packedJSON) || packedJSON.length !== 1 || !packedJSON[0]?.filename) {
    throw new Error('npm pack did not return exactly one archive');
  }
  const archive = resolve(root, packedJSON[0].filename);
  const archiveInfo = lstatSync(archive);
  if (!archiveInfo.isFile() || archiveInfo.isSymbolicLink() || archiveInfo.nlink !== 1) {
    throw new Error('packed archive is not one regular file');
  }

  const entries = run('/usr/bin/tar', ['-tzf', archive]).trim().split(/\r?\n/).filter(Boolean);
  if (entries.some((entry) => entry.startsWith('package/vendor/pulse-presence-helper/'))) {
    throw new Error('ordinary Personal npm archive must not contain the optional macOS presence helper');
  }
  process.stdout.write('Exact npm archive contents:\n');
  process.stdout.write(`${entries.join('\n')}\n`);

  const unpack = join(root, 'unpack');
  mkdirSync(unpack, { recursive: true, mode: 0o700 });
  run('/usr/bin/tar', ['-xzf', archive, '-C', unpack]);
  const packageRoot = join(unpack, 'package');
  const audit = auditPublicPackageRoot(packageRoot);

  const installRoot = join(root, 'installed');
  const home = join(root, 'home');
  const workspace = join(root, 'workspace');
  mkdirSync(home, { recursive: true, mode: 0o700 });
  mkdirSync(workspace, { recursive: true, mode: 0o700 });
  run('/usr/bin/git', ['init', '-q'], { cwd: workspace });
  run('npm', [
    'install', '--ignore-scripts', '--no-audit', '--no-fund', '--prefix', installRoot, archive,
  ], { cwd: root });

  const installedCLI = join(installRoot, 'node_modules', '@zbs-gg', 'pulse', 'src', 'cli.js');
  const isolatedEnv = {
    ...process.env,
    HOME: home,
    PULSE_DATA_DIR: join(home, '.pulse'),
  };
  delete isolatedEnv.PULSE_BASE_URL;
  delete isolatedEnv.PULSE_GO_BIN;
  run(process.execPath, [installedCLI, '--help'], { cwd: workspace, env: isolatedEnv });
  for (const host of ['codex', 'claude-code', 'cursor']) {
    const output = run(process.execPath, [installedCLI, 'init', host, '--dry-run'], {
      cwd: workspace, env: isolatedEnv,
    });
    if (!output.includes('Dry run only. Nothing was written.')) {
      throw new Error(`${host} dry run did not preserve the isolated environment`);
    }
  }
  const manifest = JSON.parse(readFileSync(join(packageRoot, 'package.json'), 'utf8'));
  if (manifest.name !== '@zbs-gg/pulse' || manifest.version !== '0.8.0' || !isAbsolute(archive)) {
    throw new Error('packed Personal identity mismatch');
  }
  process.stdout.write(`${JSON.stringify({
    ok: true,
    name: manifest.name,
    version: manifest.version,
    archive_bytes: archiveInfo.size,
    archive_entries: entries.length,
    ...audit,
    isolated_home: true,
  })}\n`);
} finally {
  rmSync(root, { recursive: true, force: true });
}
