import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  chmodSync, existsSync, linkSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, readlinkSync, rmSync, symlinkSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { gzipSync, gunzipSync } from 'node:zlib';
import { pack } from 'tar-stream';

import {
  activateArtifactVersion,
  canonicalArtifactJSON,
  codesignIdentityMatches,
  downloadVerifiedArtifact,
  materializeVerifiedCarrierDirectory,
  materializeVerifiedPortableArchive,
  materializeVerifiedTarGz,
  parseCodesignIdentity,
  readActivatedArtifact,
  readCommittedArtifactSet,
  recoverArtifactActivation,
  validateArtifactTree,
  validateSafetensorsFile,
  writeActivatedArtifactSet,
} from './artifact-installer.js';

const digest = (bytes) => createHash('sha256').update(bytes).digest('hex');

async function portableArchive(entries) {
  const archive = pack();
  const chunks = [];
  const completed = new Promise((resolve, reject) => {
    archive.on('data', (chunk) => chunks.push(Buffer.from(chunk)));
    archive.once('end', resolve);
    archive.once('error', reject);
  });
  for (const entry of entries) {
    const bytes = Buffer.from(entry.bytes ?? '');
    archive.entry({
      name: entry.path,
      mode: entry.mode ?? 0o600,
      type: entry.type ?? 'file',
      ...(entry.linkname ? { linkname: entry.linkname } : {}),
      ...(entry.pax ? { pax: entry.pax } : {}),
    }, bytes);
  }
  archive.finalize();
  await completed;
  return gzipSync(Buffer.concat(chunks));
}

function portableArtifact(bytes, overrides = {}) {
  return artifact(bytes, {
    format: 'tar.gz',
    url: 'https://releases.zbs.gg/pulse/0.8.0/daemon.tar.gz',
    ...overrides,
  });
}

function withSocketType(carrier) {
  const tar = gunzipSync(carrier);
  tar[156] = 's'.charCodeAt(0);
  tar.fill(0x20, 148, 156);
  const checksum = [...tar.subarray(0, 512)].reduce((total, byte) => total + byte, 0);
  const field = Buffer.from(`${checksum.toString(8).padStart(6, '0')}\0 `);
  field.copy(tar, 148);
  return gzipSync(tar);
}

test('codesign identity parsing requires one exact identifier and team', () => {
  assert.deepEqual(parseCodesignIdentity([
    'Executable=/tmp/carrier',
    'Identifier=gg.zbs.pulse.embedder-runtime',
    'TeamIdentifier=44N4NZ86S5',
  ].join('\n')), {
    identifier: 'gg.zbs.pulse.embedder-runtime',
    teamIdentifier: '44N4NZ86S5',
  });
  assert.equal(parseCodesignIdentity('Identifier=evil\nIdentifier=gg.zbs.pulse.embedder-runtime\nTeamIdentifier=44N4NZ86S5\n'), null);
  assert.equal(parseCodesignIdentity('Identifier=gg.zbs.pulse.embedder-runtime\nTeamIdentifier=44N4NZ86S5EVIL\n'), null);
  const expected = { identifier: 'gg.zbs.pulse.embedder-runtime', teamIdentifier: '44N4NZ86S5' };
  assert.equal(codesignIdentityMatches(
    'Identifier=gg.zbs.pulse.embedder-runtime.evil\nTeamIdentifier=44N4NZ86S5\n', expected,
  ), false, 'the carrier identifier is compared exactly rather than as a substring');
  assert.equal(codesignIdentityMatches(
    'Identifier=python3.12\nTeamIdentifier=44N4NZ86S5\n', expected, { requireIdentifier: false },
  ), true, 'inner Mach-O files share the signed team but have their own identifiers');
});

function sandbox() { return mkdtempSync(join(tmpdir(), 'pulse-artifact-installer-')); }

function artifact(bytes, overrides = {}) {
  return {
    id: 'pulse-daemon', kind: 'daemon', version: '0.8.0', epoch: 7,
    url: 'https://releases.zbs.gg/pulse/0.8.0/daemon.dmg',
    origin: 'https://releases.zbs.gg', bytes: bytes.length, sha256: digest(bytes),
    ...overrides,
  };
}

function response(status, bytes, headers = {}) {
  return new Response(bytes, { status, headers });
}

test('download resumes a verified range and never activates partial bytes', async () => {
  const root = sandbox();
  const bytes = Buffer.from('managed-pulse-daemon');
  let calls = 0;
  const fetchImpl = async (_url, options) => {
    calls += 1;
    if (calls === 1) {
      const failure = new Error('connection reset');
      async function* body() { yield bytes.subarray(0, 7); throw failure; }
      return { status: 200, headers: new Headers({ etag: '"release-7"' }), body: body() };
    }
    assert.equal(options.headers.Range, 'bytes=7-');
    assert.equal(options.headers['If-Range'], '"release-7"');
    return response(206, bytes.subarray(7), {
      etag: '"release-7"', 'content-range': `bytes 7-${bytes.length - 1}/${bytes.length}`,
    });
  };
  try {
    await assert.rejects(downloadVerifiedArtifact(artifact(bytes), {
      stagingRoot: root, fetchImpl, availableBytes: () => 10_000, minimumFreeBytes: 0,
    }), /artifact_download_interrupted/);
    const result = await downloadVerifiedArtifact(artifact(bytes), {
      stagingRoot: root, fetchImpl, availableBytes: () => bytes.length - 7, minimumFreeBytes: 0,
    });
    assert.deepEqual(readFileSync(result.path), bytes);
    assert.equal(result.resumed_from, 7);
    assert.equal(calls, 2);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('partial download path is no-follow and cannot overwrite a same-user symlink target', async () => {
  const root = sandbox();
  const bytes = Buffer.from('artifact');
  const victim = join(root, 'victim');
  const descriptor = artifact(bytes);
  try {
    writeFileSync(victim, 'keep-me', { mode: 0o600 });
    symlinkSync(victim, join(root, `${descriptor.sha256}.part`));
    await assert.rejects(downloadVerifiedArtifact(descriptor, {
      stagingRoot: root, availableBytes: () => 10_000, minimumFreeBytes: 0,
      fetchImpl: async () => response(200, bytes, { etag: '"release-7"' }),
    }), /artifact_partial_unsafe/);
    assert.equal(readFileSync(victim, 'utf8'), 'keep-me');
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('download rejects insufficient disk, cross-origin redirects, size, and digest mismatch', async () => {
  const root = sandbox();
  const bytes = Buffer.from('artifact');
  try {
    await assert.rejects(downloadVerifiedArtifact(artifact(bytes), {
      stagingRoot: root, availableBytes: () => bytes.length - 1,
      fetchImpl: async () => response(200, bytes, { etag: '"release-7"' }), minimumFreeBytes: 0,
    }), /artifact_disk_insufficient/);
    await assert.rejects(downloadVerifiedArtifact(artifact(bytes), {
      stagingRoot: root, availableBytes: () => 10_000,
      fetchImpl: async () => response(302, null, { location: 'https://evil.example/payload.dmg' }), minimumFreeBytes: 0,
    }), /artifact_redirect_not_allowed/);
    await assert.rejects(downloadVerifiedArtifact(artifact(bytes, { bytes: bytes.length + 1 }), {
      stagingRoot: root, availableBytes: () => 10_000,
      fetchImpl: async () => response(200, bytes, { etag: '"release-7"' }), minimumFreeBytes: 0,
    }), /artifact_size_mismatch/);
    await assert.rejects(downloadVerifiedArtifact(artifact(bytes, { sha256: 'a'.repeat(64) }), {
      stagingRoot: root, availableBytes: () => 10_000,
      fetchImpl: async () => response(200, bytes, { etag: '"release-7"' }), minimumFreeBytes: 0,
    }), /artifact_digest_mismatch/);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('same-origin redirect is allowed and a server that ignores Range causes a safe full restart', async () => {
  const root = sandbox();
  const bytes = Buffer.from('restart-the-whole-artifact');
  let call = 0;
  const fetchImpl = async (url, options) => {
    call += 1;
    if (call === 1) {
      async function* body() { yield bytes.subarray(0, 5); throw new Error('offline'); }
      return { status: 200, headers: new Headers({ etag: '"old"' }), body: body() };
    }
    if (call === 2) {
      assert.equal(options.headers.Range, 'bytes=5-');
      return response(307, null, { location: '/immutable/daemon.dmg' });
    }
    assert.equal(new URL(url).pathname, '/immutable/daemon.dmg');
    return response(200, bytes, { etag: '"new"' });
  };
  try {
    await assert.rejects(downloadVerifiedArtifact(artifact(bytes), {
      stagingRoot: root, fetchImpl, availableBytes: () => 10_000, minimumFreeBytes: 0,
    }), /artifact_download_interrupted/);
    const result = await downloadVerifiedArtifact(artifact(bytes), {
      stagingRoot: root, fetchImpl, availableBytes: () => 10_000, minimumFreeBytes: 0,
    });
    assert.equal(result.resumed_from, 0);
    assert.deepEqual(readFileSync(result.path), bytes);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('Range fallback preserves the partial when capacity for a full restart disappears', async () => {
  const root = sandbox();
  const bytes = Buffer.from('preserve-partial-progress');
  let fetchCall = 0;
  const fetchImpl = async () => {
    fetchCall += 1;
    if (fetchCall === 1) {
      async function* body() { yield bytes.subarray(0, 5); throw new Error('offline'); }
      return { status: 200, headers: new Headers({ etag: '"old"' }), body: body() };
    }
    return response(200, bytes, { etag: '"new"' });
  };
  try {
    await assert.rejects(downloadVerifiedArtifact(artifact(bytes), {
      stagingRoot: root, fetchImpl, availableBytes: () => 10_000, minimumFreeBytes: 0,
    }), /artifact_download_interrupted/);
    let checks = 0;
    await assert.rejects(downloadVerifiedArtifact(artifact(bytes), {
      stagingRoot: root, fetchImpl, minimumFreeBytes: 0,
      availableBytes: () => (checks++ === 0 ? bytes.length - 5 : 0),
    }), /artifact_disk_insufficient/);
    assert.equal(lstatSync(join(root, `${artifact(bytes).sha256}.part`)).size, 5);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('download deadlines abort a connection that never resolves and a body that stalls', async () => {
  const root = sandbox();
  const bytes = Buffer.from('deadline-payload');
  try {
    await assert.rejects(downloadVerifiedArtifact(artifact(bytes), {
      stagingRoot: root, availableBytes: () => 10_000, minimumFreeBytes: 0,
      overallTimeoutMs: 25, idleTimeoutMs: 25,
      fetchImpl: async () => new Promise(() => {}),
    }), /artifact_download_interrupted/);

    async function* stalledBody() {
      yield bytes.subarray(0, 4);
      await new Promise(() => {});
    }
    await assert.rejects(downloadVerifiedArtifact(artifact(bytes), {
      stagingRoot: root, availableBytes: () => 10_000, minimumFreeBytes: 0,
      overallTimeoutMs: 500, idleTimeoutMs: 25,
      fetchImpl: async () => ({
        status: 200, headers: new Headers({ etag: '"deadline"' }), body: stalledBody(),
      }),
    }), /artifact_download_idle_timeout|artifact_download_interrupted/);
    assert.equal(lstatSync(join(root, `${artifact(bytes).sha256}.part`)).size, 4);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

function treeManifest(files) {
  return { schema: 'pulse.artifact_tree.v1', files: files.map(({ path, bytes, mode = 0o600, executable = false }) => ({
    path, bytes: bytes.length, sha256: digest(bytes), mode, executable,
  })) };
}

test('exact artifact tree accepts declared files and rejects links, hardlinks, unexpected files, and size bombs', () => {
  const root = sandbox();
  const payload = Buffer.from('daemon');
  try {
    mkdirSync(join(root, 'bin'), { mode: 0o700 });
    writeFileSync(join(root, 'bin', 'pulse'), payload, { mode: 0o700 });
    const manifest = treeManifest([{ path: 'bin/pulse', bytes: payload, mode: 0o700, executable: true }]);
    assert.equal(validateArtifactTree(root, manifest).files, 1);
    writeFileSync(join(root, 'unexpected'), 'x', { mode: 0o600 });
    assert.throws(() => validateArtifactTree(root, manifest), /artifact_tree_unexpected/);
    rmSync(join(root, 'unexpected'));
    symlinkSync('pulse', join(root, 'bin', 'linked'));
    assert.throws(() => validateArtifactTree(root, treeManifest([
      { path: 'bin/pulse', bytes: payload, mode: 0o700, executable: true },
      { path: 'bin/linked', bytes: payload, mode: 0o700, executable: true },
    ])), /artifact_tree_link/);
    rmSync(join(root, 'bin', 'linked'));
    linkSync(join(root, 'bin', 'pulse'), join(root, 'bin', 'hard'));
    assert.throws(() => validateArtifactTree(root, manifest), /artifact_tree_link/);
    rmSync(join(root, 'bin', 'hard'));
    assert.throws(() => validateArtifactTree(root, manifest, { maxTotalBytes: 3 }), /artifact_tree_too_large/);
    chmodSync(join(root, 'bin'), 0o755);
    assert.throws(() => validateArtifactTree(root, manifest), /artifact_tree_directory_unsafe/);
    chmodSync(join(root, 'bin'), 0o700);
    mkdirSync(join(root, 'empty'), { mode: 0o700 });
    assert.throws(() => validateArtifactTree(root, manifest), /artifact_tree_unexpected/);
    rmSync(join(root, 'empty'), { recursive: true });
    assert.throws(() => validateArtifactTree(root, treeManifest([
      { path: '../outside', bytes: payload, mode: 0o700, executable: true },
    ])), /artifact_tree_manifest_invalid/);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

function safetensors(path, tensors) {
  const header = Buffer.from(JSON.stringify(tensors).padEnd(120, ' '));
  const prefix = Buffer.alloc(8);
  prefix.writeBigUInt64LE(BigInt(header.length));
  writeFileSync(path, Buffer.concat([prefix, header, Buffer.alloc(16)]), { mode: 0o600 });
}

test('safetensors validation is data-only and bounds dtype, shape, and offsets', () => {
  const root = sandbox();
  const path = join(root, 'model.safetensors');
  try {
    safetensors(path, { weight: { dtype: 'F16', shape: [2, 4], data_offsets: [0, 16] } });
    assert.equal(validateSafetensorsFile(path).tensors, 1);
    safetensors(path, { weight: { dtype: 'OBJECT', shape: [1], data_offsets: [0, 16] } });
    assert.throws(() => validateSafetensorsFile(path), /safetensors_dtype_invalid/);
    safetensors(path, { weight: { dtype: 'F16', shape: [2, 4], data_offsets: [0, 17] } });
    assert.throws(() => validateSafetensorsFile(path), /safetensors_offsets_invalid/);
    safetensors(path, { weight: { dtype: 'F16', shape: [2, 3], data_offsets: [0, 16] } });
    assert.throws(() => validateSafetensorsFile(path), /safetensors_shape_invalid/);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('model activation permits one non-executable safetensors file and rejects custom-code policy', async () => {
  const root = sandbox();
  const source = join(root, 'source.safetensors');
  const installRoot = join(root, 'installed');
  try {
    safetensors(source, { weight: { dtype: 'F16', shape: [2, 4], data_offsets: [0, 16] } });
    const bytes = readFileSync(source);
    const modelArtifact = artifact(bytes, {
      id: 'pulse-model', kind: 'model', url: 'https://releases.zbs.gg/pulse/0.8.0/model.safetensors',
      model_policy: { data_only: true, custom_code: false },
    });
    const manifest = treeManifest([{ path: 'model.safetensors', bytes, mode: 0o600, executable: false }]);
    await activateArtifactVersion(modelArtifact, source, { installRoot, treeManifest: manifest });
    assert.equal(readActivatedArtifact('pulse-model', { installRoot, expectedKind: 'model' }).kind, 'model');
    await assert.rejects(activateArtifactVersion({
      ...modelArtifact, id: 'pulse-model-unsafe', model_policy: { data_only: true, custom_code: true },
    }, source, { installRoot, treeManifest: manifest }), /artifact_model_policy_invalid/);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('activation switches an atomic regular-file pointer and recovery restores previous verified version', async () => {
  const root = sandbox();
  const installRoot = join(root, 'installed');
  const first = Buffer.from('first');
  const second = Buffer.from('second');
  const materialize = async (source, target) => {
    mkdirSync(join(target, 'bin'), { recursive: true, mode: 0o700 });
    const bytes = readFileSync(source);
    writeFileSync(join(target, 'bin', 'pulse'), bytes, { mode: 0o700 });
  };
  try {
    const onePath = join(root, 'one.dmg'); writeFileSync(onePath, first);
    const twoPath = join(root, 'two.dmg'); writeFileSync(twoPath, second);
    const one = artifact(first, { sha256: digest(first) });
    const two = artifact(second, { sha256: digest(second), version: '0.8.1', epoch: 8 });
    await activateArtifactVersion(one, onePath, {
      installRoot, materialize, testOnlyMaterializer: true,
      treeManifest: treeManifest([{ path: 'bin/pulse', bytes: first, mode: 0o700, executable: true }]),
    });
    await activateArtifactVersion(two, twoPath, {
      installRoot, materialize, testOnlyMaterializer: true,
      treeManifest: treeManifest([{ path: 'bin/pulse', bytes: second, mode: 0o700, executable: true }]),
    });
    const current = JSON.parse(readFileSync(join(installRoot, 'pulse-daemon', 'current.json'), 'utf8'));
    assert.equal(current.sha256, digest(second));
    assert.equal(readActivatedArtifact('pulse-daemon', {
      installRoot, expectedKind: 'daemon', expectedSha256: digest(second),
    }).tree_digest.length, 64);
    const linkedPointer = join(root, 'linked-current.json');
    linkSync(join(installRoot, 'pulse-daemon', 'current.json'), linkedPointer);
    assert.throws(
      () => readActivatedArtifact('pulse-daemon', { installRoot }),
      /artifact_activation_pointer_unsafe/,
      'activation pointers with an extra hard link are not private state',
    );
    rmSync(linkedPointer);
    await activateArtifactVersion(two, twoPath, {
      installRoot, materialize, testOnlyMaterializer: true,
      treeManifest: treeManifest([{ path: 'bin/pulse', bytes: second, mode: 0o700, executable: true }]),
    });
    assert.equal(JSON.parse(readFileSync(join(installRoot, 'pulse-daemon', 'previous.json'), 'utf8')).sha256, digest(first));
    writeFileSync(join(current.version_path, 'bin', 'pulse'), 'tampered', { mode: 0o700 });
    assert.throws(() => readActivatedArtifact('pulse-daemon', { installRoot }), /artifact_tree_file_invalid|artifact_tree_digest_mismatch/);
    rmSync(current.version_path, { recursive: true, force: true });
    const recovered = recoverArtifactActivation('pulse-daemon', { installRoot });
    assert.equal(recovered.status, 'rolled_back');
    assert.equal(JSON.parse(readFileSync(join(installRoot, 'pulse-daemon', 'current.json'), 'utf8')).sha256, digest(first));
    assert.throws(() => readlinkSync(join(installRoot, 'pulse-daemon', 'current.json')));
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('activation keeps distinct release identities when carrier bytes are reused', async () => {
  const root = sandbox();
  const installRoot = join(root, 'installed');
  const source = join(root, 'daemon.dmg');
  const bytes = Buffer.from('same signed carrier across releases');
  const manifest = treeManifest([{ path: 'bin/pulse', bytes, mode: 0o700, executable: true }]);
  const materialize = async (_source, target) => {
    mkdirSync(join(target, 'bin'), { recursive: true, mode: 0o700 });
    writeFileSync(join(target, 'bin', 'pulse'), bytes, { mode: 0o700 });
  };
  try {
    writeFileSync(source, bytes, { mode: 0o600 });
    const releaseOne = artifact(bytes, { version: '0.8.0', epoch: 7 });
    const releaseTwo = artifact(bytes, { version: '0.8.1', epoch: 8 });
    const first = await activateArtifactVersion(releaseOne, source, {
      installRoot, materialize, testOnlyMaterializer: true, treeManifest: manifest,
    });
    const second = await activateArtifactVersion(releaseTwo, source, {
      installRoot, materialize, testOnlyMaterializer: true, treeManifest: manifest,
    });
    assert.notEqual(first.version_path, second.version_path);
    assert.equal(second.sha256, first.sha256);
    assert.equal(second.epoch, 8);
    const previous = JSON.parse(readFileSync(join(installRoot, 'pulse-daemon', 'previous.json'), 'utf8'));
    assert.equal(previous.epoch, 7);
    assert.equal(previous.version_path, first.version_path);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('committed set survives a partial pointer switch and reused carriers still preflight materialization space', async () => {
  const root = sandbox();
  const installRoot = join(root, 'installed');
  const source = join(root, 'daemon.dmg');
  const firstBytes = Buffer.from('first generation');
  const secondBytes = Buffer.from('second generation');
  const materialize = async (carrier, target) => {
    const bytes = readFileSync(carrier);
    mkdirSync(join(target, 'bin'), { recursive: true, mode: 0o700 });
    writeFileSync(join(target, 'bin', 'pulse'), bytes, { mode: 0o700 });
  };
  try {
    writeFileSync(source, firstBytes, { mode: 0o600 });
    const first = artifact(firstBytes);
    const firstTree = treeManifest([{ path: 'bin/pulse', bytes: firstBytes, mode: 0o700, executable: true }]);
    await activateArtifactVersion(first, source, {
      installRoot, materialize, testOnlyMaterializer: true, treeManifest: firstTree,
    });
    const signedFirst = { ...first, tree_digest: digest(Buffer.from(canonicalArtifactJSON(firstTree))) };
    writeActivatedArtifactSet({
      schema: 'pulse.verified_release_manifest.v2', manifest_digest: 'a'.repeat(64),
      version: first.version, epoch: first.epoch, artifacts: { daemon: signedFirst },
    }, { installRoot });

    writeFileSync(source, secondBytes, { mode: 0o600 });
    const second = artifact(secondBytes);
    await activateArtifactVersion(second, source, {
      installRoot, materialize, testOnlyMaterializer: true,
      treeManifest: treeManifest([{ path: 'bin/pulse', bytes: secondBytes, mode: 0o700, executable: true }]),
    });
    assert.equal(readActivatedArtifact('pulse-daemon', { installRoot }).sha256, second.sha256);
    assert.equal(readCommittedArtifactSet({ installRoot }).activations.daemon.sha256, first.sha256);

    writeFileSync(source, firstBytes, { mode: 0o600 });
    await assert.rejects(activateArtifactVersion({ ...first, version: '0.8.1', epoch: 8 }, source, {
      installRoot, materialize, testOnlyMaterializer: true, treeManifest: firstTree,
      availableBytes: () => firstBytes.length - 1, minimumFreeBytes: 0,
    }), /artifact_disk_insufficient/);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('production activation refuses arbitrary materializers before they can write outside staging', async () => {
  const root = sandbox();
  const source = join(root, 'source');
  const outside = join(root, 'outside');
  const bytes = Buffer.from('payload');
  try {
    writeFileSync(source, bytes, { mode: 0o600 });
    const malicious = async () => writeFileSync(outside, 'escaped');
    await assert.rejects(activateArtifactVersion(artifact(bytes), source, {
      installRoot: join(root, 'installed'), materialize: malicious,
      treeManifest: treeManifest([{ path: 'payload', bytes, mode: 0o600 }]),
    }), /artifact_materializer_not_allowed/);
    assert.equal(existsSync(outside), false);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('carrier materializer copies only the canonical allowlisted tree with normalized private modes', async () => {
  const root = sandbox();
  const carrier = join(root, 'carrier');
  const target = join(root, 'target');
  const bytes = Buffer.from('signed-daemon');
  try {
    mkdirSync(join(carrier, 'bin'), { recursive: true, mode: 0o700 });
    mkdirSync(target, { mode: 0o700 });
    writeFileSync(join(carrier, 'bin', 'pulse'), bytes, { mode: 0o600 });
    const manifest = treeManifest([{ path: 'bin/pulse', bytes, mode: 0o700, executable: true }]);
    writeFileSync(join(carrier, 'pulse-artifact-tree.json'), `${canonicalArtifactJSON(manifest)}\n`, { mode: 0o600 });
    const result = await materializeVerifiedCarrierDirectory(carrier, target, artifact(bytes));
    assert.deepEqual(result.treeManifest, manifest);
    assert.deepEqual(readFileSync(join(target, 'bin', 'pulse')), bytes);
    assert.equal(lstatSync(join(target, 'bin', 'pulse')).mode & 0o777, 0o700);
    writeFileSync(join(carrier, 'unexpected'), 'x', { mode: 0o600 });
    await assert.rejects(materializeVerifiedCarrierDirectory(carrier, target, artifact(bytes)), /artifact_carrier_unexpected/);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('tar carrier aborts an oversized declared payload before extracting to disk', async () => {
  const root = sandbox();
  const target = join(root, 'target');
  try {
    mkdirSync(target, { mode: 0o700 });
    const manifest = { schema: 'pulse.artifact_tree.v1', files: [{
      path: 'runtime.js', bytes: 4 * 1024 * 1024 * 1024 + 1, mode: 0o600,
      executable: false, sha256: '0'.repeat(64),
    }] };
    const carrierBytes = await portableArchive([{
      path: 'pulse-artifact-tree.json', bytes: `${canonicalArtifactJSON(manifest)}\n`,
    }]);
    const carrier = join(root, 'plugin.tar.gz');
    writeFileSync(carrier, carrierBytes, { mode: 0o600 });
    await assert.rejects(materializeVerifiedPortableArchive(carrier, target, portableArtifact(carrierBytes)),
      /artifact_archive_too_large/);
    assert.deepEqual(readdirSync(target), []);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('portable archive streams the exact canonical tree without system extraction', async () => {
  const root = sandbox();
  const carrier = join(root, 'runtime.tar.gz');
  const target = join(root, 'target');
  const payload = Buffer.from('portable-runtime');
  const manifest = treeManifest([{ path: 'bin/pulse', bytes: payload, mode: 0o700, executable: true }]);
  const manifestBytes = `${canonicalArtifactJSON(manifest)}\n`;
  const carrierBytes = await portableArchive([
    { path: 'bin/', type: 'directory' },
    { path: 'pulse-artifact-tree.json', bytes: manifestBytes },
    { path: 'bin/pulse', bytes: payload, mode: 0o777 },
  ]);
  try {
    mkdirSync(target, { mode: 0o700 });
    writeFileSync(carrier, carrierBytes, { mode: 0o600 });
    const result = await materializeVerifiedPortableArchive(carrier, target, portableArtifact(carrierBytes, {
      tree_digest: digest(Buffer.from(canonicalArtifactJSON(manifest))),
    }));
    assert.deepEqual(result.treeManifest, manifest);
    assert.deepEqual(readFileSync(join(target, 'bin', 'pulse')), payload);
    assert.equal(lstatSync(join(target, 'bin', 'pulse')).mode & 0o777, 0o700);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('portable archive rejects paths, duplicates, links, special entries, and PAX path overrides', async () => {
  const root = sandbox();
  const target = join(root, 'target');
  const manifest = treeManifest([{ path: 'payload', bytes: Buffer.from('ok'), mode: 0o600 }]);
  const manifestEntry = { path: 'pulse-artifact-tree.json', bytes: `${canonicalArtifactJSON(manifest)}\n` };
  const cases = [
    ['absolute', [{ path: '/payload', bytes: 'ok' }], /artifact_archive_path_invalid/],
    ['drive-absolute', [{ path: 'C:/payload', bytes: 'ok' }], /artifact_archive_path_invalid/],
    ['dot', [{ path: './payload', bytes: 'ok' }], /artifact_archive_path_invalid/],
    ['traversal', [{ path: '../payload', bytes: 'ok' }], /artifact_archive_path_invalid/],
    ['duplicate', [manifestEntry, { path: 'payload', bytes: 'ok' }, { path: 'payload', bytes: 'ok' }], /artifact_archive_path_invalid/],
    ['pax', [manifestEntry, { path: 'ignored', pax: { path: 'payload' }, bytes: 'ok' }], /artifact_archive_path_override/],
    ['undeclared', [manifestEntry, { path: 'payload', bytes: 'ok' }, { path: 'extra', bytes: 'x' }], /artifact_carrier_unexpected/],
    ['tampered', [manifestEntry, { path: 'payload', bytes: 'no' }], /artifact_carrier_digest_mismatch/],
  ];
  for (const [name, entries, expected] of cases) {
    const bytes = await portableArchive(entries);
    const carrier = join(root, `${name}.tar.gz`);
    writeFileSync(carrier, bytes, { mode: 0o600 });
    await assert.rejects(materializeVerifiedPortableArchive(carrier, target, portableArtifact(bytes)), expected, name);
    assert.equal(existsSync(target), false, `${name} must not mutate target`);
  }
  for (const type of ['link', 'symlink', 'block-device', 'character-device', 'fifo', 'contiguous-file']) {
    const bytes = await portableArchive([{ path: 'unsafe', type, linkname: type.includes('link') ? 'payload' : undefined }]);
    const carrier = join(root, `${type}.tar.gz`);
    writeFileSync(carrier, bytes, { mode: 0o600 });
    await assert.rejects(materializeVerifiedPortableArchive(carrier, target, portableArtifact(bytes)),
      /artifact_archive_entry_invalid/, type);
  }
  const ordinary = await portableArchive([{ path: 'unsafe', bytes: '' }]);
  const socket = withSocketType(ordinary);
  const socketCarrier = join(root, 'socket.tar.gz');
  writeFileSync(socketCarrier, socket, { mode: 0o600 });
  await assert.rejects(materializeVerifiedPortableArchive(socketCarrier, target, portableArtifact(socket)),
    /artifact_archive_entry_invalid/);
  rmSync(root, { recursive: true, force: true });
});

test('portable archive rejects file-count, depth, and signed tree-digest confusion before writing', async () => {
  const root = sandbox();
  const target = join(root, 'target');
  try {
    const cases = [
      ['count', treeManifest(Array.from({ length: 4097 }, (_, index) => ({
        path: `f${index}`, bytes: Buffer.alloc(0), mode: 0o600,
      }))), /artifact_tree_too_many_files/, {}],
      ['depth', treeManifest([{ path: `${Array.from({ length: 34 }, () => 'd').join('/')}/f`,
        bytes: Buffer.alloc(0), mode: 0o600 }]), /artifact_tree_too_deep/, {}],
      ['digest', treeManifest([{ path: 'payload', bytes: Buffer.from('ok'), mode: 0o600 }]),
        /artifact_tree_digest_mismatch/, { tree_digest: 'f'.repeat(64) }],
    ];
    for (const [name, manifest, expected, overrides] of cases) {
      const bytes = await portableArchive([{
        path: 'pulse-artifact-tree.json', bytes: `${canonicalArtifactJSON(manifest)}\n`,
      }]);
      const carrier = join(root, `${name}.tar.gz`);
      writeFileSync(carrier, bytes, { mode: 0o600 });
      await assert.rejects(materializeVerifiedPortableArchive(carrier, target, portableArtifact(bytes, overrides)), expected, name);
      assert.equal(existsSync(target), false, `${name} must not mutate target`);
    }
  } finally { rmSync(root, { recursive: true, force: true }); }
});
