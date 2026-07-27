import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import test from 'node:test';

import { publishR2Release, releasePublicationObjects } from '../scripts/publish-r2-release.mjs';

const digest = (bytes) => createHash('sha256').update(bytes).digest('hex');
const acceptFixtureCatalog = () => {};

function catalogFixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'pulse-r2-publication.'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const paths = [
    'common/model.tar.gz', 'common/plugin-runtime.tar.gz',
    ...['darwin-arm64', 'darwin-x64', 'linux-arm64-gnu', 'linux-x64-gnu', 'win32-arm64', 'win32-x64']
      .flatMap((target) => [`${target}/daemon.tar.gz`, `${target}/embedder-runtime.tar.gz`]),
    'catalog/artifact-set.json',
  ];
  for (const suffix of paths) {
    const path = join(root, 'pulse', '0.7.0', 'epoch-8', suffix);
    mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
    writeFileSync(path, `fixture:${suffix}\n`, { mode: 0o600 });
  }
  const snapshotPath = join(root, 'pulse', '0.7.0', 'catalog', 'snapshot.json');
  mkdirSync(dirname(snapshotPath), { recursive: true, mode: 0o700 });
  writeFileSync(snapshotPath, 'fixture:snapshot\n', { mode: 0o600 });
  const artifactSetPath = join(root, 'pulse', '0.7.0', 'epoch-8', 'catalog', 'artifact-set.json');
  writeFileSync(join(root, 'catalog-build-receipt.json'), `${JSON.stringify({
    artifact_count: 14,
    artifact_set_digest: digest(readFileSync(artifactSetPath)),
    production_ready: true,
    release_epoch: 8,
    schema: 'pulse.personal_release_catalog_build.v3',
    snapshot_digest: digest(readFileSync(snapshotPath)),
  })}\n`, { mode: 0o600 });
  return resolve(root);
}

function fakeOrigin() {
  const objects = new Map();
  const puts = [];
  const client = {
    async get(key) { return Buffer.from(objects.get(key).bytes); },
    async head(key) {
      const value = objects.get(key);
      return value ? {
        bytes: value.bytes.length,
        cacheControl: value.cacheControl,
        contentType: value.contentType,
        etag: value.etag,
      } : null;
    },
    async put(object) {
      const bytes = readFileSync(object.path);
      const etag = digest(bytes).slice(0, 32);
      objects.set(object.key, {
        bytes, cacheControl: object.cacheControl, contentType: object.contentType, etag,
      });
      puts.push(object.key);
    },
  };
  const fetchImpl = async (url, init = {}) => {
    const key = new URL(url).pathname.slice(1);
    const value = objects.get(key);
    if (!value) return new Response('missing', { status: 404 });
    const headers = {
      'cache-control': value.cacheControl,
      etag: `"${value.etag}"`,
    };
    if (init.method === 'HEAD') return new Response(null, { status: 200, headers });
    if (init.headers?.Range === 'bytes=0-0') {
      return new Response(value.bytes.subarray(0, 1), {
        status: 206, headers: { ...headers, 'content-range': `bytes 0-0/${value.bytes.length}` },
      });
    }
    return new Response(value.bytes, { status: 200, headers });
  };
  return { client, fetchImpl, objects, puts };
}

test('R2 publication verifies all immutable bytes and exposes the snapshot last', async (t) => {
  const catalogRoot = catalogFixture(t);
  assert.throws(() => releasePublicationObjects(catalogRoot), { code: 'r2_release_catalog_invalid' });
  const origin = fakeOrigin();
  const receipt = await publishR2Release({
    catalogRoot, client: origin.client, fetchImpl: origin.fetchImpl, verifyCatalog: acceptFixtureCatalog,
  });
  assert.equal(receipt.object_count, 16);
  assert.equal(receipt.snapshot_published_last, true);
  assert.equal(origin.puts.at(-1), 'pulse/0.7.0/catalog/snapshot.json');
  assert.equal(origin.puts.length, 16);
  assert.equal(releasePublicationObjects(catalogRoot, { verifyCatalog: acceptFixtureCatalog }).immutable.length, 15);
  assert.equal(receipt.objects.every((object) => /^[a-f0-9]{64}$/.test(object.sha256)), true);
});

test('R2 publication reuses identical immutable objects and stops on byte drift before snapshot', async (t) => {
  const catalogRoot = catalogFixture(t);
  const origin = fakeOrigin();
  const objects = releasePublicationObjects(catalogRoot, { verifyCatalog: acceptFixtureCatalog });
  const first = objects.immutable[0];
  await origin.client.put(first);
  origin.puts.length = 0;
  const conflict = objects.immutable[1];
  origin.objects.set(conflict.key, {
    bytes: Buffer.from('drift\n'),
    cacheControl: conflict.cacheControl,
    contentType: conflict.contentType,
    etag: 'bad',
  });
  await assert.rejects(publishR2Release({
    catalogRoot, client: origin.client, fetchImpl: origin.fetchImpl, verifyCatalog: acceptFixtureCatalog,
  }), { code: 'r2_release_immutable_conflict' });
  assert.equal(origin.objects.has('pulse/0.7.0/catalog/snapshot.json'), false);
  assert.equal(origin.puts.length, 0);
});
