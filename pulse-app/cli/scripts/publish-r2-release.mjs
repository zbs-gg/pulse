#!/usr/bin/env node

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { lstatSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, isAbsolute, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { pinnedReleaseKeyring, verifyPersonalReleaseArtifactSet } from '../src/release-manifest.js';

const VERSION = '0.8.0';
const EPOCH = 9;
const SHA256 = /^[a-f0-9]{64}$/;
const IMMUTABLE_CACHE = 'public, max-age=31536000, immutable';
const SNAPSHOT_CACHE = 'no-cache';
const TARGETS = Object.freeze([
  'darwin-arm64', 'darwin-x64', 'linux-arm64-gnu',
  'linux-x64-gnu', 'win32-arm64', 'win32-x64',
]);

function fail(code) {
  const error = new Error(code);
  error.code = code;
  throw error;
}

function canonical(value) {
  if (value === null) return 'null';
  if (typeof value === 'string' || typeof value === 'boolean') return JSON.stringify(value);
  if (typeof value === 'number' && Number.isSafeInteger(value) && !Object.is(value, -0)) return String(value);
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`;
  if (value && typeof value === 'object') {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}`;
  }
  fail('r2_release_value_invalid');
}

function fileReceipt(path) {
  let info;
  try { info = lstatSync(path); } catch { fail('r2_release_input_missing'); }
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.size < 1) fail('r2_release_input_unsafe');
  return Object.freeze({
    bytes: info.size,
    sha256: createHash('sha256').update(readFileSync(path)).digest('hex'),
  });
}

function catalogTargetIDs(artifactSet) {
  const targetIDs = Object.keys(artifactSet?.payload?.targets ?? {}).sort();
  if (targetIDs.length < 1 || targetIDs.some((targetID) => !TARGETS.includes(targetID))) {
    fail('r2_release_catalog_invalid');
  }
  return targetIDs;
}

function verifyReleaseCatalog(artifactSetPath, snapshotPath, artifactSetDigest, snapshotDigest, trustedKeys) {
  let artifactSet;
  let snapshot;
  try {
    artifactSet = JSON.parse(readFileSync(artifactSetPath, 'utf8'));
    snapshot = JSON.parse(readFileSync(snapshotPath, 'utf8'));
  } catch { fail('r2_release_catalog_invalid'); }
  for (const targetID of catalogTargetIDs(artifactSet)) {
    const [platform, architecture, libc] = targetID.split('-');
    let verified;
    try {
      verified = verifyPersonalReleaseArtifactSet(artifactSet, snapshot, {
        architecture, libc, minimumAcceptedEpoch: EPOCH, now: new Date(), osVersion: '26.2',
        packageVersion: VERSION, platform, trustedKeys,
      });
    } catch { fail('r2_release_catalog_invalid'); }
    if (verified.target_id !== targetID || verified.manifest_digest !== artifactSetDigest ||
        verified.authority.snapshot_digest !== snapshotDigest) fail('r2_release_catalog_invalid');
  }
}

export function releasePublicationObjects(catalogRoot, {
  trustedKeys = pinnedReleaseKeyring(),
  verifyCatalog = verifyReleaseCatalog,
} = {}) {
  if (typeof catalogRoot !== 'string' || !isAbsolute(catalogRoot) || resolve(catalogRoot) !== catalogRoot) {
    fail('r2_release_arguments_invalid');
  }
  const artifactSetPath = resolve(catalogRoot, `pulse/${VERSION}/epoch-${EPOCH}/catalog/artifact-set.json`);
  let artifactSet;
  try { artifactSet = JSON.parse(readFileSync(artifactSetPath, 'utf8')); } catch { fail('r2_release_catalog_invalid'); }
  const targetIDs = catalogTargetIDs(artifactSet);
  const immutable = [
    ['common/model.tar.gz', 'application/gzip'],
    ['common/plugin-runtime.tar.gz', 'application/gzip'],
    ...targetIDs.flatMap((target) => [
      [`${target}/daemon.tar.gz`, 'application/gzip'],
      [`${target}/embedder-runtime.tar.gz`, 'application/gzip'],
    ]),
    ['catalog/artifact-set.json', 'application/json'],
  ].map(([suffix, contentType]) => {
    const key = `pulse/${VERSION}/epoch-${EPOCH}/${suffix}`;
    const path = resolve(catalogRoot, key);
    if (relative(catalogRoot, path).startsWith('..')) fail('r2_release_input_unsafe');
    return Object.freeze({
      cacheControl: IMMUTABLE_CACHE, contentType, immutable: true, key, path, ...fileReceipt(path),
    });
  });
  const snapshotKey = `pulse/${VERSION}/catalog/snapshot.json`;
  const snapshotPath = resolve(catalogRoot, snapshotKey);
  const snapshot = Object.freeze({
    cacheControl: SNAPSHOT_CACHE,
    contentType: 'application/json',
    immutable: false,
    key: snapshotKey,
    path: snapshotPath,
    ...fileReceipt(snapshotPath),
  });
  assert.equal(immutable.length, 3 + targetIDs.length * 2);
  const receiptPath = join(catalogRoot, 'catalog-build-receipt.json');
  const receipt = JSON.parse(readFileSync(receiptPath, 'utf8'));
  if (receipt.schema !== 'pulse.personal_release_catalog_build.v3' || receipt.release_epoch !== EPOCH ||
      receipt.production_ready !== true || receipt.target_count !== targetIDs.length ||
      JSON.stringify(receipt.target_ids) !== JSON.stringify(targetIDs) ||
      receipt.artifact_count !== 2 + targetIDs.length * 2 ||
      receipt.artifact_set_digest !== immutable.at(-1).sha256 ||
      receipt.snapshot_digest !== snapshot.sha256) fail('r2_release_catalog_invalid');
  if (typeof verifyCatalog !== 'function') fail('r2_release_arguments_invalid');
  verifyCatalog(immutable.at(-1).path, snapshot.path, immutable.at(-1).sha256, snapshot.sha256, trustedKeys);
  return Object.freeze({ immutable: Object.freeze(immutable), snapshot });
}

function awsCommand(endpoint, args, { allowMissing = false } = {}) {
  const result = spawnSync('aws', ['--endpoint-url', endpoint, 's3api', ...args], {
    encoding: 'utf8', env: process.env, stdio: ['ignore', 'pipe', 'pipe'], timeout: 15 * 60_000,
  });
  if (allowMissing && result.status !== 0 && /(?:404|Not Found|NoSuchKey)/i.test(`${result.stdout}\n${result.stderr}`)) {
    return null;
  }
  if (result.status !== 0) fail('r2_release_s3_failed');
  try { return result.stdout.trim() ? JSON.parse(result.stdout) : {}; } catch { fail('r2_release_s3_invalid'); }
}

export function awsR2Client({ bucket, endpoint }) {
  if (typeof bucket !== 'string' || !/^[a-z0-9][a-z0-9-]{1,62}$/.test(bucket) ||
      typeof endpoint !== 'string' || !/^https:\/\/[a-f0-9]{32}\.r2\.cloudflarestorage\.com$/.test(endpoint)) {
    fail('r2_release_arguments_invalid');
  }
  return Object.freeze({
    async get(key) {
      const root = mkdtempSync(join(tmpdir(), 'pulse-r2-get.'));
      const path = join(root, 'object');
      try {
        awsCommand(endpoint, ['get-object', '--bucket', bucket, '--key', key, path]);
        return readFileSync(path);
      } finally { rmSync(root, { recursive: true, force: true }); }
    },
    async head(key) {
      const value = awsCommand(endpoint, ['head-object', '--bucket', bucket, '--key', key], { allowMissing: true });
      if (value === null) return null;
      return Object.freeze({
        bytes: value.ContentLength,
        cacheControl: value.CacheControl,
        contentType: value.ContentType,
        etag: String(value.ETag ?? '').replace(/^"|"$/g, ''),
      });
    },
    async put(object) {
      awsCommand(endpoint, [
        'put-object', '--bucket', bucket, '--key', object.key, '--body', object.path,
        '--cache-control', object.cacheControl, '--content-type', object.contentType,
      ]);
    },
  });
}

export async function publicObjectProof(object, { fetchImpl, origin, attempts = 8 }) {
  const url = `${origin}/${object.key}`;
  let lastError;
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    try {
      const head = await fetchImpl(url, { method: 'HEAD', redirect: 'manual' });
      if (head.status >= 300 && head.status < 400 || head.redirected === true || head.url && head.url !== url) {
        fail('r2_release_public_redirect');
      }
      if (head.status !== 200 || head.headers.get('cache-control') !== object.cacheControl) {
        fail('r2_release_public_head_invalid');
      }
      const response = await fetchImpl(url, { redirect: 'manual' });
      if (response.status !== 200 || response.redirected === true || response.url && response.url !== url) {
        fail('r2_release_public_download_invalid');
      }
      const bytes = Buffer.from(await response.arrayBuffer());
      if (bytes.length !== object.bytes || createHash('sha256').update(bytes).digest('hex') !== object.sha256) {
        fail('r2_release_public_digest_mismatch');
      }
      const range = await fetchImpl(url, { headers: { Range: 'bytes=0-0' }, redirect: 'manual' });
      if (range.status !== 206 || range.redirected === true ||
          range.headers.get('content-range') !== `bytes 0-0/${object.bytes}` ||
          Buffer.from(await range.arrayBuffer()).length !== 1) fail('r2_release_public_range_invalid');
      return Object.freeze({ etag: head.headers.get('etag')?.replace(/^"|"$/g, '') ?? null });
    } catch (error) {
      lastError = error;
      if (attempt + 1 < attempts) await new Promise((accept) => setTimeout(accept, 250 * (attempt + 1)));
    }
  }
  throw lastError;
}

export async function publishR2SnapshotRefresh({
  artifactSetPath,
  client,
  fetchImpl = globalThis.fetch,
  origin = 'https://releases.zbs.gg',
  snapshotPath,
  trustedKeys = pinnedReleaseKeyring(),
} = {}) {
  if (![artifactSetPath, snapshotPath].every((path) =>
    typeof path === 'string' && isAbsolute(path) && resolve(path) === path) ||
      !client || typeof client.head !== 'function' || typeof client.get !== 'function' ||
      typeof client.put !== 'function' || typeof fetchImpl !== 'function' || origin !== 'https://releases.zbs.gg') {
    fail('r2_snapshot_refresh_arguments_invalid');
  }
  const artifactSetFile = fileReceipt(artifactSetPath);
  const snapshotFile = fileReceipt(snapshotPath);
  let artifactSet;
  let snapshot;
  try {
    artifactSet = JSON.parse(readFileSync(artifactSetPath, 'utf8'));
    snapshot = JSON.parse(readFileSync(snapshotPath, 'utf8'));
  } catch { fail('r2_snapshot_refresh_input_invalid'); }
  const verified = verifyPersonalReleaseArtifactSet(artifactSet, snapshot, {
    architecture: 'arm64', minimumAcceptedEpoch: EPOCH, now: new Date(), osVersion: '26.2',
    packageVersion: VERSION, platform: 'darwin', trustedKeys,
  });
  if (verified.manifest_digest !== artifactSetFile.sha256 ||
      verified.authority.snapshot_digest !== snapshotFile.sha256) fail('r2_snapshot_refresh_input_invalid');
  const artifactSetKey = `pulse/${VERSION}/epoch-${EPOCH}/catalog/artifact-set.json`;
  const existing = await client.head(artifactSetKey);
  if (existing === null) fail('r2_snapshot_refresh_artifact_set_missing');
  const existingBytes = await client.get(artifactSetKey);
  if (existingBytes.length !== artifactSetFile.bytes ||
      createHash('sha256').update(existingBytes).digest('hex') !== artifactSetFile.sha256) {
    fail('r2_snapshot_refresh_artifact_set_mismatch');
  }
  const object = Object.freeze({
    bytes: snapshotFile.bytes,
    cacheControl: SNAPSHOT_CACHE,
    contentType: 'application/json',
    immutable: false,
    key: `pulse/${VERSION}/catalog/snapshot.json`,
    path: snapshotPath,
    sha256: snapshotFile.sha256,
  });
  await client.put(object);
  const head = await client.head(object.key);
  if (head === null || head.bytes !== object.bytes || head.cacheControl !== object.cacheControl ||
      head.contentType !== object.contentType) fail('r2_snapshot_refresh_head_invalid');
  const stored = await client.get(object.key);
  if (stored.length !== object.bytes || createHash('sha256').update(stored).digest('hex') !== object.sha256) {
    fail('r2_snapshot_refresh_digest_mismatch');
  }
  const publicProof = await publicObjectProof(object, { fetchImpl, origin });
  if (head.etag && publicProof.etag && head.etag !== publicProof.etag) fail('r2_release_etag_mismatch');
  return Object.freeze({
    artifact_set_digest: artifactSetFile.sha256,
    origin,
    release_epoch: EPOCH,
    schema: 'pulse.r2_snapshot_publication.v1',
    snapshot_digest: object.sha256,
    version: VERSION,
  });
}

async function ensureImmutable(object, client) {
  const existing = await client.head(object.key);
  if (existing !== null) {
    const bytes = await client.get(object.key);
    const sha256 = createHash('sha256').update(bytes).digest('hex');
    if (bytes.length !== object.bytes || sha256 !== object.sha256 ||
        existing.cacheControl !== object.cacheControl || existing.contentType !== object.contentType) {
      fail('r2_release_immutable_conflict');
    }
    return Object.freeze({ reused: true, s3Etag: existing.etag });
  }
  await client.put(object);
  const uploaded = await client.head(object.key);
  if (uploaded === null || uploaded.bytes !== object.bytes || uploaded.cacheControl !== object.cacheControl ||
      uploaded.contentType !== object.contentType) fail('r2_release_s3_head_invalid');
  const bytes = await client.get(object.key);
  if (bytes.length !== object.bytes || createHash('sha256').update(bytes).digest('hex') !== object.sha256) {
    fail('r2_release_s3_digest_mismatch');
  }
  return Object.freeze({ reused: false, s3Etag: uploaded.etag });
}

export async function publishR2Release({
  catalogRoot,
  client,
  fetchImpl = globalThis.fetch,
  origin = 'https://releases.zbs.gg',
  trustedKeys = pinnedReleaseKeyring(),
  verifyCatalog = verifyReleaseCatalog,
} = {}) {
  if (!client || typeof client.head !== 'function' || typeof client.get !== 'function' ||
      typeof client.put !== 'function' || typeof fetchImpl !== 'function' || origin !== 'https://releases.zbs.gg') {
    fail('r2_release_arguments_invalid');
  }
  const objects = releasePublicationObjects(catalogRoot, { trustedKeys, verifyCatalog });
  const published = [];
  for (const object of objects.immutable) {
    const stored = await ensureImmutable(object, client);
    const publicProof = await publicObjectProof(object, { fetchImpl, origin });
    if (stored.s3Etag && publicProof.etag && stored.s3Etag !== publicProof.etag) fail('r2_release_etag_mismatch');
    published.push({ bytes: object.bytes, etag: publicProof.etag, key: object.key, reused: stored.reused, sha256: object.sha256 });
  }
  let snapshotHead = await client.head(objects.snapshot.key);
  let snapshotReused = false;
  if (snapshotHead !== null) {
    const existing = await client.get(objects.snapshot.key);
    if (existing.length !== objects.snapshot.bytes ||
        createHash('sha256').update(existing).digest('hex') !== objects.snapshot.sha256 ||
        snapshotHead.cacheControl !== SNAPSHOT_CACHE || snapshotHead.contentType !== 'application/json') {
      fail('r2_release_snapshot_conflict');
    }
    snapshotReused = true;
  } else {
    await client.put(objects.snapshot);
    snapshotHead = await client.head(objects.snapshot.key);
  }
  if (snapshotHead === null || snapshotHead.bytes !== objects.snapshot.bytes ||
      snapshotHead.cacheControl !== SNAPSHOT_CACHE || snapshotHead.contentType !== 'application/json') {
    fail('r2_release_snapshot_head_invalid');
  }
  const snapshotPublic = await publicObjectProof(objects.snapshot, { fetchImpl, origin });
  if (snapshotHead.etag && snapshotPublic.etag && snapshotHead.etag !== snapshotPublic.etag) fail('r2_release_etag_mismatch');
  published.push({
    bytes: objects.snapshot.bytes, etag: snapshotPublic.etag, key: objects.snapshot.key,
    reused: snapshotReused, sha256: objects.snapshot.sha256,
  });
  return Object.freeze({
    object_count: published.length,
    objects: published,
    origin,
    release_epoch: EPOCH,
    schema: 'pulse.r2_release_publication.v1',
    snapshot_published_last: true,
    version: VERSION,
  });
}

function args(argv) {
  const values = {};
  for (let index = 0; index < argv.length; index += 2) {
    const name = argv[index];
    const value = argv[index + 1];
    if (!['--bucket', '--catalog', '--endpoint', '--out'].includes(name) || !value || Object.hasOwn(values, name)) {
      fail('r2_release_arguments_invalid');
    }
    values[name] = value;
  }
  if (Object.keys(values).length !== 4 || !isAbsolute(values['--catalog']) || !isAbsolute(values['--out'])) {
    fail('r2_release_arguments_invalid');
  }
  return values;
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  const values = args(process.argv.slice(2));
  const receipt = await publishR2Release({
    catalogRoot: resolve(values['--catalog']),
    client: awsR2Client({ bucket: values['--bucket'], endpoint: values['--endpoint'] }),
  });
  const output = resolve(values['--out']);
  if (relative(dirname(output), output).startsWith('..') || !SHA256.test(receipt.objects[0].sha256)) {
    fail('r2_release_arguments_invalid');
  }
  writeFileSync(output, `${canonical(receipt)}\n`, { encoding: 'utf8', flag: 'wx', mode: 0o600 });
  process.stdout.write(`${canonical({ object_count: receipt.object_count, schema: receipt.schema })}\n`);
}
