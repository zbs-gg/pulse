#!/usr/bin/env node

import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const scriptRoot = dirname(fileURLToPath(import.meta.url));
const matrixPath = join(scriptRoot, 'native-universal-matrix.json');
const TARGET = /^(?:darwin-(?:arm64|x64)|linux-(?:arm64|x64)-gnu|win32-(?:arm64|x64))$/;
const RUNNER = /^(?:macos-26|macos-26-intel|ubuntu-24\.04|ubuntu-24\.04-arm|windows-2025|windows-11-arm)$/;
const HOSTS = Object.freeze(['claude-code', 'codex', 'cursor']);

export function loadNativeUniversalMatrix(path = matrixPath) {
  const value = JSON.parse(readFileSync(path, 'utf8'));
  assert.equal(value.schema, 'pulse.native_universal_matrix.v2');
  assert.match(value.node_version, /^\d+$/);
  assert.match(value.go_version, /^\d+\.\d+\.\d+$/);
  assert.equal(Array.isArray(value.harnesses), true);
  assert.equal(value.harnesses.length, 3);
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
  const targetIDs = targets.map((target) => target.target_id).sort();
  const harnesses = value.harnesses.map((harness) => {
    assert.deepEqual(Object.keys(harness).sort(), [
      'distribution', 'downloads', 'executable', 'executable_digest_policy', 'host', 'identity',
      'supported_targets', 'vendor', 'vendor_source', 'version',
    ].sort());
    assert.equal(HOSTS.includes(harness.host), true);
    assert.equal(['npm', 'vendor-desktop-installer'].includes(harness.distribution), true);
    assert.equal(typeof harness.identity, 'string');
    assert.match(harness.version, /^\d+\.\d+(?:\.\d+)?$/);
    assert.match(harness.executable, /^[a-z][a-z0-9-]*$/);
    assert.equal(new URL(harness.vendor_source).protocol, 'https:');
    assert.equal(harness.executable_digest_policy, 'native_evidence_sha256');
    assert.deepEqual([...harness.supported_targets].sort(), targetIDs);
    assert.deepEqual(Object.keys(harness.downloads ?? {}).sort(), targetIDs);
    for (const targetID of targetIDs) {
      const download = new URL(harness.downloads[targetID]);
      assert.equal(download.protocol, 'https:');
      if (harness.distribution === 'npm') assert.equal(download.origin, 'https://registry.npmjs.org');
      else assert.equal(download.origin, 'https://api2.cursor.sh');
    }
    if (harness.distribution === 'npm') assert.match(harness.identity, /^@[^/]+\/[^/]+$/);
    return Object.freeze({
      ...harness,
      downloads: Object.freeze({ ...harness.downloads }),
      supported_targets: Object.freeze([...harness.supported_targets]),
    });
  });
  assert.deepEqual(harnesses.map((harness) => harness.host).sort(), [...HOSTS].sort());
  return Object.freeze({
    ...value,
    harnesses: Object.freeze(harnesses),
    targets: Object.freeze(targets),
  });
}

export function githubNativeUniversalMatrix(value = loadNativeUniversalMatrix(), platform, host) {
  if (platform !== undefined && !['darwin', 'linux', 'win32'].includes(platform)) {
    throw new Error('native universal matrix platform is unsupported');
  }
  if (host !== undefined && !HOSTS.includes(host)) {
    throw new Error('native universal matrix host is unsupported');
  }
  return {
    include: value.harnesses
      .filter((harness) => host === undefined || harness.host === host)
      .flatMap((harness) => value.targets
        .filter((target) => platform === undefined || target.platform === platform)
        .filter((target) => harness.supported_targets.includes(target.target_id))
        .map((target) => ({
          ...target,
          host: harness.host,
          harness_distribution: harness.distribution,
          harness_identity: harness.identity,
          harness_executable: harness.executable,
          harness_download_url: harness.downloads[target.target_id],
          harness_version: harness.version,
          harness_vendor_source: harness.vendor_source,
          stability_runs: harness.host === 'codex' && target.target_id === 'win32-arm64' ? 5 : 1,
          node_version: value.node_version,
          go_version: value.go_version,
        }))),
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
  const hostIndex = process.argv.indexOf('--github-host');
  if (platformIndex >= 0 || hostIndex >= 0) {
    const platform = platformIndex >= 0 ? process.argv[platformIndex + 1] : undefined;
    const host = hostIndex >= 0 ? process.argv[hostIndex + 1] : undefined;
    const expectedLength = 2 + (platform === undefined ? 0 : 2) + (host === undefined ? 0 : 2);
    if (process.argv.length !== expectedLength ||
        (platformIndex >= 0 && !platform) || (hostIndex >= 0 && !host)) {
      throw new Error('native universal matrix arguments are invalid');
    }
    process.stdout.write(JSON.stringify(githubNativeUniversalMatrix(matrix, platform, host)));
    return;
  }
  if (process.argv.includes('--github')) {
    process.stdout.write(JSON.stringify(githubNativeUniversalMatrix(matrix)));
    return;
  }
  if (process.argv.includes('--check')) {
    process.stdout.write(`Pulse universal matrix: ${matrix.harnesses.length * matrix.targets.length} host-target pairs (${matrix.harnesses.map((harness) => harness.host).join(', ')})\n`);
    return;
  }
  throw new Error('use --check or --github');
}

if (process.argv[1] && pathToFileURL(process.argv[1]).href === import.meta.url) main();
