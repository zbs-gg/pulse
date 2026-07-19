import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, readFileSync, rmSync, truncateSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { Readable } from 'node:stream';
import test from 'node:test';
import { createGunzip } from 'node:zlib';
import { extract } from 'tar-stream';

import {
  buildDaemonTarget,
  packNormalizedArtifact,
  prepareNormalizedArtifact,
  releaseTargetDefinition,
} from './release-builder-core.mjs';

test('six desktop targets map to deterministic static Go build inputs', () => {
  assert.deepEqual(releaseTargetDefinition('darwin-arm64'), {
    architecture: 'arm64', daemon_name: 'pulse', goarch: 'arm64', goos: 'darwin',
    libc: null, platform: 'darwin', target_id: 'darwin-arm64',
  });
  assert.deepEqual(releaseTargetDefinition('win32-x64'), {
    architecture: 'x64', daemon_name: 'pulse.exe', goarch: 'amd64', goos: 'windows',
    libc: null, platform: 'win32', target_id: 'win32-x64',
  });
  assert.deepEqual(releaseTargetDefinition('linux-arm64-gnu'), {
    architecture: 'arm64', daemon_name: 'pulse', goarch: 'arm64', goos: 'linux',
    libc: 'gnu', platform: 'linux', target_id: 'linux-arm64-gnu',
  });
  assert.throws(() => releaseTargetDefinition('linux-x64-musl'), /release_target_invalid/);
});

test('normalized v2 carriers are portable tar.gz files with canonical private modes', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-release-carrier.'));
  const artifact = join(root, 'artifact');
  const carrier = join(root, 'daemon.tar.gz');
  mkdirSync(join(artifact, 'bin'), { recursive: true });
  writeFileSync(join(artifact, 'bin', 'pulse'), 'daemon', { mode: 0o755 });
  writeFileSync(join(artifact, 'NOTICE'), 'notice', { mode: 0o644 });
  try {
    const prepared = prepareNormalizedArtifact(artifact);
    assert.match(prepared.tree_digest, /^[a-f0-9]{64}$/);
    const packed = await packNormalizedArtifact(artifact, carrier);
    assert.equal(packed.format, 'tar.gz');
    const entries = [];
    const unpack = extract();
    unpack.on('entry', (header, stream, next) => {
      entries.push({ mode: header.mode, name: header.name, type: header.type });
      stream.on('end', next);
      stream.resume();
    });
    const complete = new Promise((accept, reject) => unpack.on('finish', accept).on('error', reject));
    Readable.from(readFileSync(carrier)).pipe(createGunzip()).pipe(unpack);
    await complete;
    assert.deepEqual(entries, [
      { mode: 0o600, name: 'NOTICE', type: 'file' },
      { mode: 0o700, name: 'bin/pulse', type: 'file' },
      { mode: 0o600, name: 'pulse-artifact-tree.json', type: 'file' },
    ]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('daemon builder emits a static target binary and content-free build evidence', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-release-builder.'));
  const calls = [];
  try {
    const result = buildDaemonTarget({
      appRoot: '/source/pulse-app',
      outputRoot: root,
      run(command, args, options) {
        calls.push({ args, command, options });
        const outputIndex = args.indexOf('-o');
        if (outputIndex >= 0) {
          const output = args[outputIndex + 1];
          writeFileSync(output, 'synthetic PE fixture');
        }
      },
      targetID: 'win32-x64',
    });
    assert.equal(result.executable_relative_path, 'bin/pulse.exe');
    assert.equal(result.cgo_enabled, false);
    assert.equal(calls[0].command, 'go');
    assert.deepEqual(calls[0].options.env, { CGO_ENABLED: '0', GOARCH: 'amd64', GOOS: 'windows' });
    assert.deepEqual(calls[0].args.slice(0, 5), ['build', '-trimpath', '-ldflags=-s -w -buildid=', '-o', join(root, 'bin', 'pulse.exe')]);
    assert.equal(JSON.parse(readFileSync(join(root, 'pulse-target-build.json'), 'utf8')).production_ready, false);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('large sparse payloads are digested and packed without a whole-file buffer contract', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-release-sparse.'));
  const artifact = join(root, 'artifact');
  const model = join(artifact, 'model_int8.onnx');
  mkdirSync(artifact);
  writeFileSync(model, 'onnx');
  truncateSync(model, 96 * 1024 * 1024);
  try {
    const prepared = prepareNormalizedArtifact(artifact);
    assert.equal(prepared.tree.files[0].bytes, 96 * 1024 * 1024);
    const carrier = await packNormalizedArtifact(artifact, join(root, 'model.tar.gz'));
    assert.match(carrier.sha256, /^[a-f0-9]{64}$/);
    assert.equal(carrier.bytes < 1024 * 1024, true);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('normalized artifact trees preserve declared empty files while carriers remain non-empty', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-release-empty-file.'));
  try {
    const artifact = join(root, 'artifact');
    mkdirSync(artifact, { mode: 0o700 });
    writeFileSync(join(artifact, 'empty-module.js'), '', { mode: 0o600 });
    const prepared = prepareNormalizedArtifact(artifact);
    assert.equal(prepared.tree.files[0].path, 'empty-module.js');
    assert.equal(prepared.tree.files[0].bytes, 0);
    const packed = await packNormalizedArtifact(artifact, join(root, 'artifact.tar.gz'));
    assert.ok(packed.bytes > 0);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
