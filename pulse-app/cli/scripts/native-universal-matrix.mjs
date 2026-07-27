#!/usr/bin/env node

import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const scriptRoot = dirname(fileURLToPath(import.meta.url));
const matrixPath = join(scriptRoot, 'native-universal-matrix.json');
const TARGET = /^(?:darwin-(?:arm64|x64)|linux-(?:arm64|x64)-gnu|win32-(?:arm64|x64))$/;
const RUNNER = /^(?:macos-26|macos-26-intel|ubuntu-24\.04|ubuntu-24\.04-arm|windows-2025|windows-11-arm)$/;

export function loadNativeUniversalMatrix(path = matrixPath) {
  const value = JSON.parse(readFileSync(path, 'utf8'));
  assert.equal(value.schema, 'pulse.native_universal_matrix.v1');
  assert.match(value.node_version, /^\d+$/);
  assert.match(value.go_version, /^\d+\.\d+\.\d+$/);
  assert.deepEqual(value.harness, {
    host: 'codex', package: '@openai/codex', version: '0.136.0',
  });
  assert.equal(Array.isArray(value.targets), true);
  assert.equal(value.targets.length, 6);
  const targets = value.targets.map((target) => {
    assert.deepEqual(Object.keys(target).sort(),
      ['architecture', 'libc', 'platform', 'runner', 'target_id'].sort());
    assert.match(target.target_id, TARGET);
    assert.match(target.runner, RUNNER);
    assert.equal(['darwin', 'linux', 'win32'].includes(target.platform), true);
    assert.equal(['arm64', 'x64'].includes(target.architecture), true);
    assert.equal(target.libc, target.platform === 'linux' ? 'gnu' : null);
    assert.equal(target.target_id,
      target.platform === 'linux'
        ? `${target.platform}-${target.architecture}-${target.libc}`
        : `${target.platform}-${target.architecture}`);
    return Object.freeze({ ...target });
  });
  assert.deepEqual([...new Set(targets.map((target) => target.target_id))].sort(),
    targets.map((target) => target.target_id).sort());
  assert.deepEqual([...new Set(targets.map((target) => target.runner))].sort(),
    targets.map((target) => target.runner).sort());
  for (const platform of ['darwin', 'linux', 'win32']) {
    assert.deepEqual(targets.filter((target) => target.platform === platform)
      .map((target) => target.architecture).sort(), ['arm64', 'x64']);
  }
  return Object.freeze({ ...value, targets: Object.freeze(targets) });
}

export function githubNativeUniversalMatrix(value = loadNativeUniversalMatrix(), platform) {
  if (platform !== undefined && !['darwin', 'linux', 'win32'].includes(platform)) {
    throw new Error('native universal matrix platform is unsupported');
  }
  return {
    include: value.targets.filter((target) => platform === undefined || target.platform === platform).map((target) => ({
      ...target,
      host: value.harness.host,
      harness_package: value.harness.package,
      harness_version: value.harness.version,
      node_version: value.node_version,
      go_version: value.go_version,
    })),
  };
}

export function currentNativeTargetID({
  platform = process.platform,
  architecture = process.arch,
  report = process.report,
} = {}) {
  if (!['darwin', 'linux', 'win32'].includes(platform) || !['arm64', 'x64'].includes(architecture)) {
    throw new Error('native universal runner target is unsupported');
  }
  if (platform === 'linux') {
    if (!report?.getReport()?.header?.glibcVersionRuntime) {
      throw new Error('native universal runner requires GNU libc');
    }
    return `linux-${architecture}-gnu`;
  }
  return `${platform}-${architecture}`;
}

function main() {
  const matrix = loadNativeUniversalMatrix();
  const platformIndex = process.argv.indexOf('--github-platform');
  if (platformIndex >= 0) {
    const platform = process.argv[platformIndex + 1];
    if (process.argv.length !== 4 || !platform) throw new Error('native universal matrix arguments are invalid');
    process.stdout.write(JSON.stringify(githubNativeUniversalMatrix(matrix, platform)));
    return;
  }
  if (process.argv.includes('--github')) {
    process.stdout.write(JSON.stringify(githubNativeUniversalMatrix(matrix)));
    return;
  }
  if (process.argv.includes('--check')) {
    process.stdout.write(`Pulse universal matrix: ${matrix.targets.map((target) => target.target_id).join(', ')}\n`);
    return;
  }
  throw new Error('use --check or --github');
}

if (process.argv[1] && pathToFileURL(process.argv[1]).href === import.meta.url) main();
