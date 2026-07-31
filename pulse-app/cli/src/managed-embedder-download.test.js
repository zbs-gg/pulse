import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { chmodSync, existsSync, mkdtempSync, mkdirSync, readFileSync, rmSync, symlinkSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { downloadPinnedSource, ensurePrivateSourceCache } from './managed-embedder-download.js';

const sha256 = (value) => createHash('sha256').update(value).digest('hex');

test('pinned source download follows only allowlisted redirects and commits exact bytes', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-managed-download.'));
  const destination = join(root, 'component.source');
  const bytes = Buffer.from('pinned source');
  let calls = 0;
  try {
    const result = await downloadPinnedSource({
      bytes: bytes.length, sha256: sha256(bytes), url: 'https://sources.example/component',
    }, {
      allowedOrigins: ['https://sources.example'],
      allowedRedirectOrigins: ['https://cdn.example'], destination,
      fetchImpl: async () => {
        calls += 1;
        return calls === 1
          ? new Response(null, { status: 307, headers: { location: 'https://cdn.example/immutable/component?X-Signed=1' } })
          : new Response(bytes, { status: 200, headers: { 'content-length': String(bytes.length) } });
      },
    });
    assert.equal(result, destination);
    assert.deepEqual(readFileSync(destination), bytes);
    assert.equal(calls, 2);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('pinned source download rejects redirects outside the source allowlist', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-managed-download-origin.'));
  const destination = join(root, 'component.source');
  const bytes = Buffer.from('pinned source');
  try {
    await assert.rejects(downloadPinnedSource({
      bytes: bytes.length, sha256: sha256(bytes), url: 'https://sources.example/component',
    }, {
      allowedOrigins: ['https://sources.example'], allowedRedirectOrigins: ['https://trusted-cdn.example'], destination,
      fetchImpl: async () => new Response(null, { status: 302, headers: { location: 'https://evil.example/component' } }),
    }), /source_download_origin_invalid/);
    assert.equal(existsSync(destination), false);
    assert.equal(existsSync(`${destination}.partial`), false);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('source cache must be a private owned directory, never a shared directory or symlink', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-managed-cache.'));
  const privateCache = join(root, 'private');
  const sharedCache = join(root, 'shared');
  const cacheLink = join(root, 'link');
  try {
    assert.equal(ensurePrivateSourceCache(privateCache), privateCache);
    mkdirSync(sharedCache, { mode: 0o755 });
    chmodSync(sharedCache, 0o755);
    assert.throws(() => ensurePrivateSourceCache(sharedCache), /source_cache_unsafe/);
    symlinkSync(privateCache, cacheLink);
    assert.throws(() => ensurePrivateSourceCache(cacheLink), /source_cache_unsafe/);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('pinned source download aborts an oversized stream and removes partial bytes', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-managed-download-size.'));
  const destination = join(root, 'component.source');
  const expected = Buffer.from('small');
  const oversized = Buffer.from('larger-than-declared');
  try {
    await assert.rejects(downloadPinnedSource({
      bytes: expected.length, sha256: sha256(expected), url: 'https://sources.example/component',
    }, {
      allowedOrigins: ['https://sources.example'], destination,
      fetchImpl: async () => new Response(new ReadableStream({
        start(controller) { controller.enqueue(oversized); controller.close(); },
      }), { status: 200 }),
    }), /source_download_size_exceeded/);
    assert.equal(existsSync(destination), false);
    assert.equal(existsSync(`${destination}.partial`), false);
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('pinned source download removes a digest-mismatched partial', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-managed-download-digest.'));
  const destination = join(root, 'component.source');
  const bytes = Buffer.from('pinned source');
  try {
    await assert.rejects(downloadPinnedSource({
      bytes: bytes.length, sha256: '0'.repeat(64), url: 'https://sources.example/component',
    }, {
      allowedOrigins: ['https://sources.example'], destination,
      fetchImpl: async () => new Response(bytes, { status: 200 }),
    }), /source_download_verification_failed/);
    assert.equal(existsSync(destination), false);
    assert.equal(existsSync(`${destination}.partial`), false);
  } finally { rmSync(root, { recursive: true, force: true }); }
});
