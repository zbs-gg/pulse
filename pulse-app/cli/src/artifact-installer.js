import { execFileSync, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  chmodSync, closeSync, constants, copyFileSync, createReadStream, existsSync, fstatSync, fsyncSync, lstatSync, mkdirSync, mkdtempSync, openSync, readFileSync,
  readSync, readdirSync, renameSync, rmSync, statfsSync, statSync, writeFileSync, writeSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, isAbsolute, join, relative, resolve, sep } from 'node:path';
import { Transform } from 'node:stream';
import { pipeline } from 'node:stream/promises';
import { createGunzip } from 'node:zlib';
import { extract } from 'tar-stream';

import { defaultPlatformServices } from './platform-services.js';

const SHA256 = /^[a-f0-9]{64}$/;
const SAFE_ID = /^[a-z0-9][a-z0-9._-]{0,127}$/;
const TREE_SCHEMA = 'pulse.artifact_tree.v1';
const POINTER_SCHEMA = 'pulse.artifact_activation.v2';
const INSTALLED_SCHEMA = 'pulse.installed_artifact.v1';
const INSTALLED_METADATA = 'activation.json';
const DOWNLOAD_SCHEMA = 'pulse.artifact_download.v1';
const ACTIVE_SET_SCHEMA = 'pulse.artifact_activation_set.v1';
const ACTIVE_SET_FILE = 'active-release.json';
const GENERATION_AUTHORITY_SCHEMA = 'pulse.artifact_generation_authority.v1';
const GENERATION_AUTHORITY_FILE = 'artifact-generation-authority.json';
const CARRIER_TREE_FILE = 'pulse-artifact-tree.json';
const OPTIONAL_CARRIER_CONTROL_FILES = new Set(['internal-manifest.json']);
const SAFE_DTYPES = new Set(['F16', 'BF16', 'F32', 'F64', 'I8', 'I16', 'I32', 'I64', 'U8', 'U16', 'U32', 'U64', 'BOOL']);
const DTYPE_BYTES = Object.freeze({ F16: 2, BF16: 2, F32: 4, F64: 8, I8: 1, I16: 2, I32: 4, I64: 8, U8: 1, U16: 2, U32: 4, U64: 8, BOOL: 1 });

export class ArtifactInstallerError extends Error {
  constructor(code) {
    super(code);
    this.name = 'ArtifactInstallerError';
    this.code = code;
  }
}

function fail(code) { throw new ArtifactInstallerError(code); }

function canonical(value) {
  if (value === null) return 'null';
  if (typeof value === 'string' || typeof value === 'boolean') return JSON.stringify(value);
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value) || Object.is(value, -0)) fail('artifact_metadata_invalid');
    return String(value);
  }
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`;
  if (value && typeof value === 'object') {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}`;
  }
  fail('artifact_metadata_invalid');
}

export function canonicalArtifactJSON(value) { return canonical(value); }

function ensurePrivateDirectory(path, platformServices = defaultPlatformServices) {
  try {
    platformServices.ensurePrivateDirectory(resolve(path));
  } catch { fail('artifact_directory_unsafe'); }
}

function assertPrivateState(path, kind, code, platformServices = defaultPlatformServices) {
  try { return platformServices.assertPrivateState(resolve(path), { kind }); } catch { fail(code); }
}

function inspectPathIdentity(path, kind, code, platformServices = defaultPlatformServices) {
  try { return platformServices.inspectPathIdentity(resolve(path), { kind }); } catch { fail(code); }
}

function atomicJSON(path, value, platformServices = defaultPlatformServices) {
  try {
    platformServices.atomicWritePrivateFile(resolve(path), `${canonical(value)}\n`, { maxBytes: 2 * 1024 * 1024 });
  } catch { fail('artifact_metadata_write_failed'); }
}

function validateDescriptor(artifact) {
  if (!artifact || typeof artifact !== 'object' || typeof artifact.id !== 'string' || !SAFE_ID.test(artifact.id) ||
      typeof artifact.kind !== 'string' || !SAFE_ID.test(artifact.kind) || typeof artifact.version !== 'string' ||
      !Number.isSafeInteger(artifact.epoch) || artifact.epoch < 1 || !Number.isSafeInteger(artifact.bytes) || artifact.bytes < 1 ||
      artifact.bytes > 64 * 1024 * 1024 * 1024 || typeof artifact.sha256 !== 'string' || !SHA256.test(artifact.sha256) ||
      (artifact.tree_digest !== undefined && (typeof artifact.tree_digest !== 'string' || !SHA256.test(artifact.tree_digest)))) {
    fail('artifact_descriptor_invalid');
  }
  let url;
  let origin;
  try { url = new URL(artifact.url); origin = new URL(artifact.origin); } catch { fail('artifact_url_invalid'); }
  if (url.protocol !== 'https:' || url.username || url.password || url.search || url.hash || url.origin !== artifact.origin ||
      origin.href !== `${artifact.origin}/` || url.pathname.split('/').some((part) => part === '.' || part === '..')) fail('artifact_url_invalid');
  return url;
}

function defaultAvailableBytes(path) {
  const stat = statfsSync(path);
  return Number(stat.bavail) * Number(stat.bsize);
}

function sha256File(path, platformServices = defaultPlatformServices) {
  const before = inspectPathIdentity(path, 'file', 'artifact_file_unsafe', platformServices);
  let fd;
  try { fd = openSync(path, constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0)); } catch { fail('artifact_file_unsafe'); }
  try {
    const stat = fstatSync(fd);
    if (!stat.isFile()) fail('artifact_file_unsafe');
    const hash = createHash('sha256');
    const buffer = Buffer.allocUnsafe(1024 * 1024);
    let offset = 0;
    while (offset < stat.size) {
      const count = readSync(fd, buffer, 0, Math.min(buffer.length, stat.size - offset), offset);
      if (count <= 0) fail('artifact_file_read_failed');
      hash.update(buffer.subarray(0, count));
      offset += count;
    }
    const after = inspectPathIdentity(path, 'file', 'artifact_file_unsafe', platformServices);
    if (after.identity_token !== before.identity_token) fail('artifact_file_unsafe');
    return { digest: hash.digest('hex'), stat };
  } finally { closeSync(fd); }
}

function securePartialSize(path, platformServices = defaultPlatformServices) {
  if (!existsSync(path)) return 0;
  assertPrivateState(path, 'file', 'artifact_partial_unsafe', platformServices);
  return statSync(path).size;
}

function openPartial(path, { append, expectedSize }, platformServices = defaultPlatformServices) {
  const absolute = resolve(path);
  if (!append) {
    try { platformServices.atomicWritePrivateFile(absolute, Buffer.alloc(0), { ensureParent: false, maxBytes: 1 }); } catch {
      fail('artifact_partial_unsafe');
    }
  }
  assertPrivateState(absolute, 'file', 'artifact_partial_unsafe', platformServices);
  const identity = inspectPathIdentity(absolute, 'file', 'artifact_partial_unsafe', platformServices);
  const flags = constants.O_WRONLY | (constants.O_NOFOLLOW ?? 0) | constants.O_APPEND;
  let fd;
  try { fd = openSync(absolute, flags); } catch { fail('artifact_partial_unsafe'); }
  const stat = fstatSync(fd);
  const after = inspectPathIdentity(absolute, 'file', 'artifact_partial_unsafe', platformServices);
  if (!stat.isFile() || stat.size !== expectedSize || after.identity_token !== identity.identity_token) {
    closeSync(fd);
    fail('artifact_partial_unsafe');
  }
  return fd;
}

function strongETag(value) {
  return typeof value === 'string' && /^"[^"\r\n]{1,200}"$/.test(value) ? value : null;
}

function readDownloadMetadata(path, artifact, platformServices = defaultPlatformServices) {
  try {
    const bytes = platformServices.readPrivateFile(resolve(path), { missing: true, maxBytes: 4096 });
    if (bytes === null) return null;
    const value = JSON.parse(bytes);
    if (bytes !== `${canonical(value)}\n` || value.schema !== DOWNLOAD_SCHEMA || value.url !== artifact.url ||
        value.sha256 !== artifact.sha256 || value.bytes !== artifact.bytes || !strongETag(value.etag)) return null;
    return value;
  } catch { return null; }
}

function awaitWithAbort(promise, signal) {
  return new Promise((resolveValue, rejectValue) => {
    if (signal?.aborted) {
      rejectValue(new ArtifactInstallerError('artifact_download_interrupted'));
      return;
    }
    let settled = false;
    const finish = (error, value) => {
      if (settled) return;
      settled = true;
      signal?.removeEventListener('abort', onAbort);
      if (error) rejectValue(error); else resolveValue(value);
    };
    const onAbort = () => finish(new ArtifactInstallerError('artifact_download_interrupted'));
    signal?.addEventListener('abort', onAbort, { once: true });
    Promise.resolve(promise).then(
      (value) => finish(null, value),
      (error) => finish(error),
    );
  });
}

async function manualFetch(url, options, artifact, fetchImpl, redirects = 0) {
  if (redirects > 3) fail('artifact_redirect_limit');
  let response;
  try {
    response = await awaitWithAbort(fetchImpl(url, { ...options, redirect: 'manual' }), options.signal);
  } catch { fail('artifact_download_interrupted'); }
  if ([301, 302, 303, 307, 308].includes(response.status)) {
    const location = response.headers?.get?.('location');
    let next;
    try { next = new URL(location, url); } catch { fail('artifact_redirect_not_allowed'); }
    if (next.protocol !== 'https:' || next.origin !== artifact.origin || next.username || next.password || next.search || next.hash ||
        next.pathname.split('/').some((part) => part === '.' || part === '..')) fail('artifact_redirect_not_allowed');
    return manualFetch(next, options, artifact, fetchImpl, redirects + 1);
  }
  return response;
}

function nextBodyChunk(iterator, controller, idleTimeoutMs) {
  return new Promise((resolveChunk, rejectChunk) => {
    if (controller.signal.aborted) {
      rejectChunk(new ArtifactInstallerError('artifact_download_interrupted'));
      return;
    }
    let settled = false;
    const finish = (error, value) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      controller.signal.removeEventListener('abort', onAbort);
      if (error) rejectChunk(error); else resolveChunk(value);
    };
    const onAbort = () => finish(new ArtifactInstallerError('artifact_download_interrupted'));
    const timer = setTimeout(() => {
      controller.abort();
      finish(new ArtifactInstallerError('artifact_download_idle_timeout'));
    }, idleTimeoutMs);
    controller.signal.addEventListener('abort', onAbort, { once: true });
    Promise.resolve(iterator.next()).then(
      (value) => finish(null, value),
      () => finish(new ArtifactInstallerError('artifact_download_interrupted')),
    );
  });
}

export async function downloadVerifiedArtifact(artifact, {
  stagingRoot, fetchImpl = globalThis.fetch, availableBytes = defaultAvailableBytes, minimumFreeBytes = 256 * 1024 * 1024,
  overallTimeoutMs = 30 * 60 * 1000, idleTimeoutMs = 30 * 1000,
  platformServices = defaultPlatformServices,
} = {}) {
  const url = validateDescriptor(artifact);
  if (typeof stagingRoot !== 'string' || !stagingRoot || typeof fetchImpl !== 'function') fail('artifact_download_configuration_invalid');
  ensurePrivateDirectory(stagingRoot, platformServices);
  const partPath = join(stagingRoot, `${artifact.sha256}.part`);
  const metadataPath = join(stagingRoot, `${artifact.sha256}.download.json`);
  const verifiedPath = join(stagingRoot, `${artifact.sha256}.verified`);
  if (existsSync(verifiedPath)) {
    const stat = lstatSync(verifiedPath);
    if (stat.isFile() && !stat.isSymbolicLink() && stat.size === artifact.bytes &&
        assertPrivateState(verifiedPath, 'file', 'artifact_file_unsafe', platformServices) &&
        sha256File(verifiedPath, platformServices).digest === artifact.sha256) {
      return { path: verifiedPath, resumed_from: artifact.bytes, status: 'already_verified' };
    }
    rmSync(verifiedPath, { force: true });
  }
  let existing = securePartialSize(partPath, platformServices);
  const metadata = readDownloadMetadata(metadataPath, artifact, platformServices);
  if (!Number.isSafeInteger(existing) || existing < 0 || existing >= artifact.bytes || (existing > 0 && !metadata)) {
    rmSync(partPath, { force: true });
    rmSync(metadataPath, { force: true });
    existing = 0;
  }
  if (availableBytes(stagingRoot) < (artifact.bytes - existing) + minimumFreeBytes) fail('artifact_disk_insufficient');
  const headers = existing > 0 ? { Range: `bytes=${existing}-`, 'If-Range': metadata.etag } : {};
  if (!Number.isSafeInteger(overallTimeoutMs) || overallTimeoutMs < 1 || !Number.isSafeInteger(idleTimeoutMs) || idleTimeoutMs < 1) {
    fail('artifact_download_configuration_invalid');
  }
  const controller = new AbortController();
  const overallTimer = setTimeout(() => controller.abort(), overallTimeoutMs);
  overallTimer.unref?.();
  let response;
  try {
    response = await manualFetch(url, { headers, signal: controller.signal }, artifact, fetchImpl);
  } catch (error) {
    clearTimeout(overallTimer);
    throw error;
  }
  let append = existing > 0;
  if (append) {
    const contentRange = response.headers?.get?.('content-range') ?? '';
    const match = contentRange.match(/^bytes (\d+)-(\d+)\/(\d+)$/);
    if (response.status !== 206 || !match || Number(match[1]) !== existing ||
        Number(match[2]) !== artifact.bytes - 1 || Number(match[3]) !== artifact.bytes) {
      if (response.status !== 200) fail('artifact_range_invalid');
      if (availableBytes(stagingRoot) + securePartialSize(partPath, platformServices) < artifact.bytes + minimumFreeBytes) {
        fail('artifact_disk_insufficient');
      }
      append = false;
      existing = 0;
    }
  } else if (response.status !== 200) {
    fail('artifact_http_status');
  }
  const etag = strongETag(response.headers?.get?.('etag'));
  if (!etag) fail('artifact_etag_invalid');
  if (append && etag !== metadata.etag) fail('artifact_range_invalid');
  atomicJSON(metadataPath, { schema: DOWNLOAD_SCHEMA, url: artifact.url, sha256: artifact.sha256, bytes: artifact.bytes, etag }, platformServices);
  if (!append && existsSync(partPath)) rmSync(partPath, { force: true });
  const fd = openPartial(partPath, { append, expectedSize: existing }, platformServices);
  let written = existing;
  try {
    if (!response.body || typeof response.body[Symbol.asyncIterator] !== 'function') fail('artifact_body_invalid');
    const iterator = response.body[Symbol.asyncIterator]();
    while (true) {
      const chunk = await nextBodyChunk(iterator, controller, idleTimeoutMs);
      if (chunk.done) break;
      const bytes = Buffer.from(chunk.value);
      written += bytes.length;
      if (written > artifact.bytes) fail('artifact_size_mismatch');
      let offset = 0;
      while (offset < bytes.length) {
        const count = writeSync(fd, bytes, offset, bytes.length - offset);
        if (count < 1) fail('artifact_download_interrupted');
        offset += count;
      }
    }
    fsyncSync(fd);
  } catch (error) {
    if (error instanceof ArtifactInstallerError) throw error;
    fail('artifact_download_interrupted');
  } finally {
    clearTimeout(overallTimer);
    closeSync(fd);
  }
  if (written !== artifact.bytes) fail('artifact_size_mismatch');
  assertPrivateState(partPath, 'file', 'artifact_partial_unsafe', platformServices);
  const actual = sha256File(partPath, platformServices).digest;
  if (actual !== artifact.sha256) fail('artifact_digest_mismatch');
  renameSync(partPath, verifiedPath);
  assertPrivateState(verifiedPath, 'file', 'artifact_file_unsafe', platformServices);
  rmSync(metadataPath, { force: true });
  return { path: verifiedPath, resumed_from: existing, status: 'verified' };
}

function safeRelativePath(path) {
  return typeof path === 'string' && path.length > 0 && path.length <= 512 && !path.startsWith('/') &&
    !/^[A-Za-z]:\//.test(path) && !path.includes('\\') &&
    path.split('/').every((part) => part && part !== '.' && part !== '..' && !part.includes('\0'));
}

function validateTreeManifest(manifest) {
  if (!manifest || typeof manifest !== 'object' || Array.isArray(manifest) || manifest.schema !== TREE_SCHEMA ||
      Object.keys(manifest).sort().join('\0') !== 'files\0schema' || !Array.isArray(manifest.files) || manifest.files.length < 1) {
    fail('artifact_tree_manifest_invalid');
  }
  const entries = new Map();
  for (const entry of manifest.files) {
    if (!entry || Object.keys(entry).sort().join('\0') !== 'bytes\0executable\0mode\0path\0sha256' ||
        !safeRelativePath(entry.path) || entries.has(entry.path) || !Number.isSafeInteger(entry.bytes) || entry.bytes < 0 ||
        typeof entry.sha256 !== 'string' || !SHA256.test(entry.sha256) || !Number.isSafeInteger(entry.mode) ||
        entry.mode < 0o400 || entry.mode > 0o700 || typeof entry.executable !== 'boolean' ||
        entry.executable !== ((entry.mode & 0o111) !== 0)) fail('artifact_tree_manifest_invalid');
    entries.set(entry.path, entry);
  }
  return entries;
}

function assertArtifactTreeDigest(artifact, manifest) {
  if (artifact.tree_digest !== undefined &&
      createHash('sha256').update(canonical(manifest)).digest('hex') !== artifact.tree_digest) {
    fail('artifact_tree_digest_mismatch');
  }
}

export function validateArtifactTree(root, manifest, {
  maxFiles = 4096, maxTotalBytes = 4 * 1024 * 1024 * 1024, maxEntries = 8192, maxDepth = 32,
  platformServices = defaultPlatformServices,
} = {}) {
  const entries = validateTreeManifest(manifest);
  if (entries.size > maxFiles) fail('artifact_tree_too_many_files');
  const rootPath = resolve(root);
  assertPrivateState(rootPath, 'directory', 'artifact_tree_root_invalid', platformServices);
  const actual = [];
  const allowedDirectories = new Set(['']);
  for (const path of entries.keys()) {
    const parts = path.split('/');
    for (let count = 1; count < parts.length; count += 1) allowedDirectories.add(parts.slice(0, count).join('/'));
  }
  let visitedEntries = 0;
  const walk = (directory, directoryRelative = '', depth = 0) => {
    if (depth > maxDepth) fail('artifact_tree_too_deep');
    for (const name of readdirSync(directory)) {
      visitedEntries += 1;
      if (visitedEntries > maxEntries) fail('artifact_tree_too_many_entries');
      const path = join(directory, name);
      const stat = lstatSync(path);
      if (stat.isSymbolicLink()) fail('artifact_tree_link');
      if (stat.isDirectory()) {
        assertPrivateState(path, 'directory', 'artifact_tree_directory_unsafe', platformServices);
        const rel = directoryRelative ? `${directoryRelative}/${name}` : name;
        if (!allowedDirectories.has(rel)) fail('artifact_tree_unexpected');
        walk(path, rel, depth + 1);
        continue;
      }
      if (!stat.isFile()) fail('artifact_tree_special');
      try { platformServices.assertPrivateState(resolve(path), { kind: 'file' }); } catch { fail('artifact_tree_link'); }
      const rel = relative(rootPath, path).split(sep).join('/');
      if (!safeRelativePath(rel)) fail('artifact_tree_path_invalid');
      actual.push({ rel, path, stat });
      if (actual.length > maxFiles) fail('artifact_tree_too_many_files');
    }
  };
  walk(rootPath);
  if (actual.length !== entries.size || actual.some(({ rel }) => !entries.has(rel))) fail('artifact_tree_unexpected');
  let total = 0;
  for (const { rel, path, stat } of actual) {
    const expected = entries.get(rel);
    total += stat.size;
    if (total > maxTotalBytes) fail('artifact_tree_too_large');
    if (stat.size !== expected.bytes) fail('artifact_tree_file_invalid');
    if (platformServices.platform === 'win32') {
      if (expected.executable) {
        let proof;
        try { proof = platformServices.inspectExecutable(resolve(path)); } catch { fail('artifact_tree_file_invalid'); }
        if (!proof?.executable || !proof.owner_only) fail('artifact_tree_file_invalid');
      }
    } else if ((stat.mode & 0o777) !== expected.mode) {
      fail('artifact_tree_file_invalid');
    }
    if (sha256File(path, platformServices).digest !== expected.sha256) fail('artifact_tree_digest_mismatch');
  }
  return { files: actual.length, bytes: total };
}

function copyManifestFiles(sourceRoot, targetRoot, treeManifest, platformServices = defaultPlatformServices) {
  const resolvedSourceRoot = resolve(sourceRoot);
  const resolvedTargetRoot = resolve(targetRoot);
  for (const entry of treeManifest.files) {
    const source = resolve(resolvedSourceRoot, entry.path);
    const target = resolve(resolvedTargetRoot, entry.path);
    if (!source.startsWith(`${resolvedSourceRoot}${sep}`) || !target.startsWith(`${resolvedTargetRoot}${sep}`)) {
      fail('artifact_tree_path_invalid');
    }
    ensurePrivateDirectory(dirname(target), platformServices);
    try { copyFileSync(source, target, constants.COPYFILE_EXCL); } catch { fail('artifact_materialize_failed'); }
    if (platformServices.platform !== 'win32') chmodSync(target, entry.mode);
    assertPrivateState(target, 'file', 'artifact_materialize_failed', platformServices);
    if (entry.executable && platformServices.platform === 'win32') {
      let proof;
      try { proof = platformServices.inspectExecutable(resolve(target)); } catch { fail('artifact_materialize_failed'); }
      if (!proof?.executable || !proof.owner_only) fail('artifact_materialize_failed');
    }
  }
}

export async function materializeVerifiedTree(sourceRoot, targetRoot, _artifact, treeManifest, {
  platformServices = defaultPlatformServices,
} = {}) {
  validateArtifactTree(sourceRoot, treeManifest, { platformServices });
  copyManifestFiles(sourceRoot, targetRoot, treeManifest, platformServices);
}

function readCarrierTreeManifest(root, platformServices = defaultPlatformServices) {
  const path = join(root, CARRIER_TREE_FILE);
  let bytes;
  try {
    bytes = platformServices.readIntegrityFile(resolve(path), {
      owner: 'root-or-current', encoding: 'utf8', maxBytes: 2 * 1024 * 1024,
    });
  } catch { fail('artifact_carrier_manifest_unsafe'); }
  return parseCarrierTreeManifest(bytes);
}

function parseCarrierTreeManifest(bytes) {
  if (typeof bytes !== 'string' || Buffer.byteLength(bytes) > 2 * 1024 * 1024) fail('artifact_carrier_manifest_unsafe');
  let manifest;
  try { manifest = JSON.parse(bytes); } catch { fail('artifact_carrier_manifest_invalid'); }
  if (bytes !== `${canonical(manifest)}\n`) fail('artifact_carrier_manifest_invalid');
  validateTreeManifest(manifest);
  return manifest;
}

function validateCarrierDirectory(root, manifest, {
  maxEntries = 8192, maxDepth = 32, platformServices = defaultPlatformServices,
} = {}) {
  const payload = validateTreeManifest(manifest);
  const expectedFiles = new Set([...payload.keys(), CARRIER_TREE_FILE]);
  for (const optional of OPTIONAL_CARRIER_CONTROL_FILES) {
    if (existsSync(join(root, optional))) expectedFiles.add(optional);
  }
  const allowedDirectories = new Set(['']);
  for (const path of expectedFiles) {
    const parts = path.split('/');
    for (let count = 1; count < parts.length; count += 1) allowedDirectories.add(parts.slice(0, count).join('/'));
  }
  const rootPath = resolve(root);
  inspectPathIdentity(rootPath, 'directory', 'artifact_carrier_root_unsafe', platformServices);
  const seen = new Set();
  let visited = 0;
  const walk = (directory, prefix = '', depth = 0) => {
    if (depth > maxDepth) fail('artifact_carrier_too_deep');
    for (const name of readdirSync(directory)) {
      visited += 1;
      if (visited > maxEntries) fail('artifact_carrier_too_many_entries');
      const path = join(directory, name);
      const stat = lstatSync(path);
      if (stat.isSymbolicLink()) fail('artifact_carrier_entry_unsafe');
      const rel = prefix ? `${prefix}/${name}` : name;
      if (stat.isDirectory()) {
        inspectPathIdentity(path, 'directory', 'artifact_carrier_entry_unsafe', platformServices);
        if (!allowedDirectories.has(rel)) fail('artifact_carrier_unexpected');
        walk(path, rel, depth + 1);
        continue;
      }
      if (!stat.isFile() || !expectedFiles.has(rel)) fail('artifact_carrier_unexpected');
      inspectPathIdentity(path, 'file', 'artifact_carrier_entry_unsafe', platformServices);
      seen.add(rel);
      if (payload.has(rel)) {
        const expected = payload.get(rel);
        const actual = sha256File(path, platformServices);
        if (actual.stat.size !== expected.bytes || actual.digest !== expected.sha256) fail('artifact_carrier_digest_mismatch');
      } else if (stat.size > 2 * 1024 * 1024) {
        fail('artifact_carrier_control_too_large');
      } else {
        try {
          platformServices.readIntegrityFile(resolve(path), { owner: 'root-or-current', maxBytes: 2 * 1024 * 1024 });
        } catch { fail('artifact_carrier_entry_unsafe'); }
      }
    }
  };
  walk(rootPath);
  if (seen.size !== expectedFiles.size || [...expectedFiles].some((path) => !seen.has(path))) fail('artifact_carrier_missing');
}

export async function materializeVerifiedCarrierDirectory(sourceRoot, targetRoot, _artifact, {
  platformServices = defaultPlatformServices,
} = {}) {
  const treeManifest = readCarrierTreeManifest(sourceRoot, platformServices);
  validateCarrierDirectory(sourceRoot, treeManifest, { platformServices });
  copyManifestFiles(sourceRoot, targetRoot, treeManifest, platformServices);
  return { treeManifest };
}

export function parseCodesignIdentity(details) {
  if (typeof details !== 'string' || details.length > 1024 * 1024) return null;
  const identifiers = [];
  const teams = [];
  for (const line of details.split(/\r?\n/)) {
    if (line.startsWith('Identifier=')) identifiers.push(line.slice('Identifier='.length));
    if (line.startsWith('TeamIdentifier=')) teams.push(line.slice('TeamIdentifier='.length));
  }
  if (identifiers.length !== 1 || teams.length !== 1 || !SAFE_ID.test(identifiers[0]) || !/^[A-Z0-9]{10}$/.test(teams[0])) {
    return null;
  }
  return Object.freeze({ identifier: identifiers[0], teamIdentifier: teams[0] });
}

export function codesignIdentityMatches(details, { identifier, teamIdentifier }, { requireIdentifier = true } = {}) {
  const actual = parseCodesignIdentity(details);
  return actual !== null && actual.teamIdentifier === teamIdentifier &&
    (!requireIdentifier || actual.identifier === identifier);
}

function verifyNativeCarrier(carrier, artifact, platformServices = defaultPlatformServices) {
  const actual = sha256File(carrier, platformServices);
  if (actual.stat.size !== artifact.bytes || actual.digest !== artifact.sha256) fail('artifact_carrier_digest_mismatch');
  if (artifact.format !== 'dmg' || artifact.signing?.notarized !== true || artifact.signing?.stapled !== true ||
      artifact.signing?.gatekeeper !== true || artifact.signing?.scheme !== 'apple-developer-id') fail('artifact_carrier_policy_invalid');
  try {
    execFileSync('/usr/bin/xcrun', ['stapler', 'validate', carrier], { stdio: 'ignore' });
    execFileSync('/usr/sbin/spctl', ['-a', '-t', 'open', '--context', 'context:primary-signature', '-vv', carrier], { stdio: 'ignore' });
    execFileSync('/usr/bin/codesign', ['--verify', '--strict', '--verbose=2', carrier], { stdio: 'ignore' });
  } catch { fail('artifact_carrier_trust_invalid'); }
  const identity = spawnSync('/usr/bin/codesign', ['-d', '--verbose=4', carrier], {
    encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 5000,
  });
  const details = `${identity.stdout ?? ''}\n${identity.stderr ?? ''}`;
  if (identity.status !== 0 || !codesignIdentityMatches(details, {
    identifier: artifact.signing.identifier,
    teamIdentifier: artifact.signing.team_id,
  })) fail('artifact_carrier_identity_invalid');
}

function verifyMountedExecutables(root, artifact, platformServices = defaultPlatformServices) {
  const manifest = readCarrierTreeManifest(root, platformServices);
  for (const entry of manifest.files.filter((item) => item.executable)) {
    const path = join(root, entry.path);
    try { execFileSync('/usr/bin/codesign', ['--verify', '--strict', '--verbose=2', path], { stdio: 'ignore' }); } catch {
      fail('artifact_inner_signature_invalid');
    }
    const identity = spawnSync('/usr/bin/codesign', ['-d', '--verbose=4', path], {
      encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 5000,
    });
    const details = `${identity.stdout ?? ''}\n${identity.stderr ?? ''}`;
    if (identity.status !== 0 || !codesignIdentityMatches(details, {
      identifier: artifact.signing.identifier,
      teamIdentifier: artifact.signing.team_id,
    }, { requireIdentifier: false })) {
      fail('artifact_inner_identity_invalid');
    }
  }
}

export async function materializeVerifiedDmg(carrier, targetRoot, artifact, _treeManifest, {
  platformServices = defaultPlatformServices,
} = {}) {
  if (process.platform !== 'darwin') fail('artifact_carrier_platform_invalid');
  verifyNativeCarrier(carrier, artifact, platformServices);
  const work = mkdtempSync(join(tmpdir(), 'pulse-artifact-mount-'));
  const mount = join(work, 'mount');
  mkdirSync(mount, { mode: 0o700 });
  let attached = false;
  try {
    execFileSync('/usr/bin/hdiutil', ['attach', '-readonly', '-nobrowse', '-mountpoint', mount, carrier], { stdio: 'ignore' });
    attached = true;
    verifyMountedExecutables(mount, artifact, platformServices);
    const result = await materializeVerifiedCarrierDirectory(mount, targetRoot, artifact, { platformServices });
    if (sha256File(carrier, platformServices).digest !== artifact.sha256) fail('artifact_carrier_digest_mismatch');
    return result;
  } catch (error) {
    if (error instanceof ArtifactInstallerError) throw error;
    fail('artifact_carrier_mount_failed');
  } finally {
    if (attached) {
      try {
        execFileSync('/usr/bin/hdiutil', ['detach', mount], { stdio: 'ignore' });
        attached = false;
      } catch { /* read-only mount is left visible for manual repair */ }
    }
    if (!attached) rmSync(work, { recursive: true, force: true });
  }
}

function inflatedArchiveLimit(maxTotalBytes, maxFiles) {
  const value = maxTotalBytes + (maxFiles * 1024) + (4 * 1024 * 1024);
  return Number.isSafeInteger(value) ? value : Number.MAX_SAFE_INTEGER;
}

async function walkPortableArchive(carrier, onEntry, {
  maximumInflatedBytes, timeoutMs,
}) {
  let inflated = 0;
  const budget = new Transform({
    transform(chunk, _encoding, callback) {
      inflated += chunk.length;
      callback(inflated > maximumInflatedBytes
        ? new ArtifactInstallerError('artifact_archive_too_large')
        : null, chunk);
    },
  });
  const unpack = extract();
  unpack.on('entry', (header, stream, next) => {
    Promise.resolve(onEntry(header, stream)).then(() => next(), (error) => next(error));
  });
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  timer.unref?.();
  try {
    await pipeline(createReadStream(carrier), createGunzip(), budget, unpack, { signal: controller.signal });
  } catch (error) {
    if (error instanceof ArtifactInstallerError) throw error;
    if (controller.signal.aborted) fail('artifact_archive_preflight_timeout');
    fail('artifact_archive_invalid');
  } finally { clearTimeout(timer); }
}

function validatePortableHeader(header, seen, { maxEntries, maxDepth }) {
  if (header?.type === 'directory' && typeof header.name === 'string' && header.name.endsWith('/')) {
    header.name = header.name.slice(0, -1);
  }
  if (!header || typeof header.name !== 'string' || !safeRelativePath(header.name)) fail('artifact_archive_path_invalid');
  if (header.pax && Object.keys(header.pax).length > 0) fail('artifact_archive_path_override');
  if (seen.has(header.name)) fail('artifact_archive_path_invalid');
  seen.add(header.name);
  if (seen.size > maxEntries) fail('artifact_carrier_too_many_entries');
  if (header.name.split('/').length - 1 > maxDepth) fail('artifact_tree_too_deep');
  if (!Number.isSafeInteger(header.size) || header.size < 0) fail('artifact_archive_entry_invalid');
  if (header.type !== 'file' && header.type !== 'directory') fail('artifact_archive_entry_invalid');
  if (header.type === 'directory' && header.size !== 0) fail('artifact_archive_entry_invalid');
}

async function drainPortableEntry(stream, maximumBytes, collect = false) {
  let total = 0;
  const chunks = [];
  for await (const chunk of stream) {
    const bytes = Buffer.from(chunk);
    total += bytes.length;
    if (total > maximumBytes) fail('artifact_archive_too_large');
    if (collect) chunks.push(bytes);
  }
  return collect ? Buffer.concat(chunks, total) : total;
}

async function preflightPortableArchive(carrier, artifact, {
  maxFiles, maxTotalBytes, maxEntries, maxDepth, timeoutMs,
}) {
  const seen = new Set();
  const files = new Map();
  const directories = new Set();
  let manifestBytes = null;
  await walkPortableArchive(carrier, async (header, stream) => {
    validatePortableHeader(header, seen, { maxEntries, maxDepth });
    if (header.type === 'directory') {
      directories.add(header.name);
      await drainPortableEntry(stream, 0);
      return;
    }
    files.set(header.name, header.size);
    if (header.name === CARRIER_TREE_FILE) {
      manifestBytes = await drainPortableEntry(stream, 2 * 1024 * 1024, true);
      return;
    }
    const limit = OPTIONAL_CARRIER_CONTROL_FILES.has(header.name) ? 2 * 1024 * 1024 : maxTotalBytes;
    const actual = await drainPortableEntry(stream, limit);
    if (actual !== header.size) fail('artifact_archive_invalid');
  }, { maximumInflatedBytes: inflatedArchiveLimit(maxTotalBytes, maxFiles), timeoutMs });
  if (manifestBytes === null) fail('artifact_carrier_missing');
  const manifest = parseCarrierTreeManifest(manifestBytes.toString('utf8'));
  assertArtifactTreeDigest(artifact, manifest);
  if (manifest.files.length > maxFiles) fail('artifact_tree_too_many_files');
  let payloadBytes = 0;
  for (const entry of manifest.files) {
    if (entry.path.split('/').length - 1 > maxDepth) fail('artifact_tree_too_deep');
    payloadBytes += entry.bytes;
    if (!Number.isSafeInteger(payloadBytes) || payloadBytes > maxTotalBytes) fail('artifact_archive_too_large');
  }
  const expectedFiles = new Set([CARRIER_TREE_FILE, ...manifest.files.map((entry) => entry.path)]);
  if (files.has('internal-manifest.json')) expectedFiles.add('internal-manifest.json');
  if (files.size !== expectedFiles.size || [...expectedFiles].some((path) => !files.has(path))) fail('artifact_carrier_unexpected');
  const expectedByPath = new Map(manifest.files.map((entry) => [entry.path, entry]));
  for (const [path, size] of files) {
    if (expectedByPath.has(path) && expectedByPath.get(path).bytes !== size) fail('artifact_carrier_digest_mismatch');
  }
  const allowedDirectories = new Set();
  for (const path of expectedFiles) {
    const parts = path.split('/');
    for (let count = 1; count < parts.length; count += 1) allowedDirectories.add(parts.slice(0, count).join('/'));
  }
  if ([...directories].some((path) => !allowedDirectories.has(path))) fail('artifact_carrier_unexpected');
  return { allowedDirectories, expectedByPath, expectedFiles, manifest };
}

function writePortableEntry(stream, destination, expected, platformServices) {
  return (async () => {
    ensurePrivateDirectory(dirname(destination), platformServices);
    let fd;
    try {
      fd = openSync(destination, constants.O_CREAT | constants.O_EXCL | constants.O_WRONLY | (constants.O_NOFOLLOW ?? 0), expected.mode);
    } catch { fail('artifact_archive_extract_failed'); }
    let total = 0;
    try {
      for await (const chunk of stream) {
        const bytes = Buffer.from(chunk);
        total += bytes.length;
        if (total > expected.bytes) fail('artifact_archive_too_large');
        let offset = 0;
        while (offset < bytes.length) {
          const written = writeSync(fd, bytes, offset, bytes.length - offset);
          if (written < 1) fail('artifact_archive_extract_failed');
          offset += written;
        }
      }
      if (total !== expected.bytes) fail('artifact_archive_invalid');
      fsyncSync(fd);
    } finally { closeSync(fd); }
    if (platformServices.platform !== 'win32') chmodSync(destination, expected.mode);
    assertPrivateState(destination, 'file', 'artifact_archive_extract_failed', platformServices);
  })();
}

async function extractPortableArchive(carrier, destination, preflight, options) {
  const seen = new Set();
  await walkPortableArchive(carrier, async (header, stream) => {
    validatePortableHeader(header, seen, options);
    if (header.type === 'directory') {
      if (!preflight.allowedDirectories.has(header.name)) fail('artifact_carrier_unexpected');
      ensurePrivateDirectory(join(destination, header.name), options.platformServices);
      await drainPortableEntry(stream, 0);
      return;
    }
    if (!preflight.expectedFiles.has(header.name)) fail('artifact_carrier_unexpected');
    const expected = preflight.expectedByPath.get(header.name) ?? {
      bytes: header.size, mode: 0o600, executable: false,
    };
    if (header.size !== expected.bytes) fail('artifact_carrier_digest_mismatch');
    await writePortableEntry(stream, join(destination, header.name), expected, options.platformServices);
  }, {
    maximumInflatedBytes: inflatedArchiveLimit(options.maxTotalBytes, options.maxFiles),
    timeoutMs: options.timeoutMs,
  });
}

export async function materializeVerifiedPortableArchive(carrier, targetRoot, artifact, _treeManifest, {
  platformServices = defaultPlatformServices, maxFiles = 4096, maxTotalBytes = 4 * 1024 * 1024 * 1024,
  maxEntries = 8192, maxDepth = 32, timeoutMs = 120_000,
  availableBytes = defaultAvailableBytes, minimumFreeBytes = 256 * 1024 * 1024,
} = {}) {
  const actual = sha256File(carrier, platformServices);
  if (artifact.format !== 'tar.gz' || actual.stat.size !== artifact.bytes || actual.digest !== artifact.sha256) {
    fail('artifact_carrier_digest_mismatch');
  }
  if (![maxFiles, maxTotalBytes, maxEntries, maxDepth, timeoutMs].every((value) => Number.isSafeInteger(value) && value > 0) ||
      typeof availableBytes !== 'function' || !Number.isSafeInteger(minimumFreeBytes) || minimumFreeBytes < 0) {
    fail('artifact_activation_configuration_invalid');
  }
  const options = { maxDepth, maxEntries, maxFiles, maxTotalBytes, platformServices, timeoutMs };
  const preflight = await preflightPortableArchive(carrier, artifact, options);
  const installedBytes = preflight.manifest.files.reduce((total, entry) => total + entry.bytes, 0);
  const requiredBytes = installedBytes + minimumFreeBytes;
  const freeBytes = availableBytes(existsSync(targetRoot) ? targetRoot : dirname(resolve(targetRoot)));
  if (!Number.isSafeInteger(installedBytes) || installedBytes < 1 || !Number.isSafeInteger(requiredBytes) ||
      !Number.isSafeInteger(freeBytes) || freeBytes < requiredBytes) {
    fail('artifact_disk_insufficient');
  }
  const work = mkdtempSync(join(tmpdir(), 'pulse-artifact-archive-'));
  ensurePrivateDirectory(work, platformServices);
  try {
    await extractPortableArchive(carrier, work, preflight, options);
    if (sha256File(carrier, platformServices).digest !== artifact.sha256) fail('artifact_carrier_digest_mismatch');
    validateCarrierDirectory(work, preflight.manifest, { maxEntries, maxDepth, platformServices });
    copyManifestFiles(work, targetRoot, preflight.manifest, platformServices);
    return { treeManifest: preflight.manifest };
  } catch (error) {
    if (error instanceof ArtifactInstallerError) throw error;
    fail('artifact_archive_extract_failed');
  } finally { rmSync(work, { recursive: true, force: true }); }
}

// Stable name retained for historical callers; tar.gz now always uses the
// portable streaming materializer above rather than a system archive tool.
export const materializeVerifiedTarGz = materializeVerifiedPortableArchive;

export async function materializeVerifiedModelFile(sourceFile, targetRoot, artifact, treeManifest, {
  platformServices = defaultPlatformServices,
} = {}) {
  const entries = validateTreeManifest(treeManifest);
  const entry = [...entries.values()][0];
  if (artifact.kind !== 'model' || entries.size !== 1 || !entry.path.endsWith('.safetensors') || entry.executable) {
    fail('artifact_model_tree_invalid');
  }
  const source = sha256File(sourceFile, platformServices);
  if (source.stat.size !== entry.bytes || source.digest !== entry.sha256 || source.digest !== artifact.sha256) {
    fail('artifact_model_file_invalid');
  }
  const target = resolve(targetRoot, entry.path);
  if (!target.startsWith(`${resolve(targetRoot)}${sep}`)) fail('artifact_tree_path_invalid');
  ensurePrivateDirectory(dirname(target), platformServices);
  try { copyFileSync(sourceFile, target, constants.COPYFILE_EXCL); } catch { fail('artifact_materialize_failed'); }
  if (platformServices.platform !== 'win32') chmodSync(target, entry.mode);
  assertPrivateState(target, 'file', 'artifact_materialize_failed', platformServices);
}

export function validateSafetensorsFile(path, {
  maxHeaderBytes = 16 * 1024 * 1024, maxTensors = 200_000, platformServices = defaultPlatformServices,
} = {}) {
  const before = inspectPathIdentity(path, 'file', 'safetensors_file_invalid', platformServices);
  let fd;
  try { fd = openSync(path, constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0)); } catch { fail('safetensors_file_invalid'); }
  let stat;
  let header;
  let headerLength;
  try {
    stat = fstatSync(fd);
    if (!stat.isFile() || stat.size < 10) fail('safetensors_file_invalid');
    const prefix = Buffer.alloc(8);
    if (readSync(fd, prefix, 0, 8, 0) !== 8) fail('safetensors_header_invalid');
    headerLength = Number(prefix.readBigUInt64LE(0));
    if (!Number.isSafeInteger(headerLength) || headerLength < 2 || headerLength > maxHeaderBytes || 8 + headerLength > stat.size) {
      fail('safetensors_header_invalid');
    }
    const headerBytes = Buffer.alloc(headerLength);
    if (readSync(fd, headerBytes, 0, headerLength, 8) !== headerLength) fail('safetensors_header_invalid');
    try { header = JSON.parse(headerBytes.toString('utf8').trimEnd()); } catch { fail('safetensors_header_invalid'); }
    const after = inspectPathIdentity(path, 'file', 'safetensors_file_invalid', platformServices);
    if (after.identity_token !== before.identity_token) fail('safetensors_file_invalid');
  } finally { closeSync(fd); }
  if (!header || Array.isArray(header) || typeof header !== 'object') fail('safetensors_header_invalid');
  const tensors = Object.entries(header).filter(([name]) => name !== '__metadata__');
  if (tensors.length < 1 || tensors.length > maxTensors) fail('safetensors_tensor_count_invalid');
  const dataBytes = stat.size - 8 - headerLength;
  const ranges = [];
  for (const [name, tensor] of tensors) {
    if (!name || name.length > 1024 || !tensor || Object.keys(tensor).sort().join('\0') !== 'data_offsets\0dtype\0shape' ||
        !SAFE_DTYPES.has(tensor.dtype)) fail('safetensors_dtype_invalid');
    if (!Array.isArray(tensor.shape) || tensor.shape.length > 16 || tensor.shape.some((value) => !Number.isSafeInteger(value) || value < 0)) {
      fail('safetensors_shape_invalid');
    }
    if (!Array.isArray(tensor.data_offsets) || tensor.data_offsets.length !== 2 ||
        tensor.data_offsets.some((value) => !Number.isSafeInteger(value) || value < 0) || tensor.data_offsets[0] > tensor.data_offsets[1] ||
        tensor.data_offsets[1] > dataBytes) fail('safetensors_offsets_invalid');
    const elements = tensor.shape.reduce((total, dimension) => total * BigInt(dimension), 1n);
    const expectedBytes = elements * BigInt(DTYPE_BYTES[tensor.dtype]);
    if (expectedBytes !== BigInt(tensor.data_offsets[1] - tensor.data_offsets[0])) fail('safetensors_shape_invalid');
    ranges.push(tensor.data_offsets);
  }
  ranges.sort((a, b) => a[0] - b[0]);
  if (ranges[0][0] !== 0 || ranges.at(-1)[1] !== dataBytes) fail('safetensors_offsets_invalid');
  for (let index = 1; index < ranges.length; index += 1) {
    if (ranges[index][0] !== ranges[index - 1][1]) fail('safetensors_offsets_invalid');
  }
  return { tensors: tensors.length, data_bytes: dataBytes };
}

function pointerPath(artifactRoot, name) { return join(artifactRoot, name); }

function readPointer(path, platformServices = defaultPlatformServices) {
  let bytes;
  try {
    bytes = platformServices.readPrivateFile(resolve(path), { missing: true, maxBytes: 8192 });
  } catch { fail('artifact_activation_pointer_unsafe'); }
  if (bytes === null) return null;
  try {
    const value = JSON.parse(bytes);
    if (bytes !== `${canonical(value)}\n` || value.schema !== POINTER_SCHEMA ||
        Object.keys(value).sort().join('\0') !== 'activation_digest\0artifact_id\0epoch\0schema\0sha256\0version\0version_path' ||
        !SAFE_ID.test(value.artifact_id) || !SHA256.test(value.sha256) || !SHA256.test(value.activation_digest) ||
        !Number.isSafeInteger(value.epoch) || value.epoch < 1 || typeof value.version !== 'string' ||
        typeof value.version_path !== 'string' || !isAbsolute(value.version_path)) fail('artifact_activation_pointer_invalid');
    return value;
  } catch (error) {
    if (error instanceof ArtifactInstallerError) throw error;
    fail('artifact_activation_pointer_invalid');
  }
}

function installedMetadata(artifact, treeManifest) {
  const tree = JSON.parse(canonical(treeManifest));
  return {
    schema: INSTALLED_SCHEMA,
    artifact_id: artifact.id,
    kind: artifact.kind,
    version: artifact.version,
    epoch: artifact.epoch,
    carrier_sha256: artifact.sha256,
    tree_digest: createHash('sha256').update(canonical(tree)).digest('hex'),
    tree,
  };
}

function validateDataOnlyModel(root, artifact, treeManifest, platformServices = defaultPlatformServices) {
  if (artifact.kind !== 'model') return;
  if (!artifact.model_policy || artifact.model_policy.data_only !== true || artifact.model_policy.custom_code !== false) {
    fail('artifact_model_policy_invalid');
  }
  const files = treeManifest.files.filter((entry) => entry.path !== INSTALLED_METADATA);
  if (artifact.model_policy.required_files !== undefined) {
    const policyKeys = Object.keys(artifact.model_policy).sort().join('\0');
    const required = artifact.model_policy.required_files;
    if (policyKeys !== 'custom_code\0data_only\0engine\0model\0required_files\0revision' ||
        artifact.model_policy.engine !== 'transformers-js-onnx' || artifact.model_policy.model !== 'BAAI/bge-m3' ||
        !/^[a-f0-9]{40}$/.test(artifact.model_policy.revision ?? '') || !Array.isArray(required) || required.length < 1 ||
        new Set(required).size !== required.length || required.some((path) => !safeRelativePath(path)) ||
        files.some((entry) => entry.executable) ||
        [...files.map((entry) => entry.path)].sort().join('\0') !== [...required].sort().join('\0')) {
      fail('artifact_model_tree_invalid');
    }
    return;
  }
  if (Object.keys(artifact.model_policy).sort().join('\0') !== 'custom_code\0data_only' ||
      files.length !== 1 || !files[0].path.endsWith('.safetensors') || files[0].executable) {
    fail('artifact_model_tree_invalid');
  }
  validateSafetensorsFile(join(root, files[0].path), { platformServices });
}

function readInstalledMetadata(path, platformServices = defaultPlatformServices) {
  let bytes;
  let value;
  try {
    bytes = platformServices.readPrivateFile(resolve(path), { maxBytes: 2 * 1024 * 1024 });
  } catch { fail('artifact_activation_metadata_unsafe'); }
  try { value = JSON.parse(bytes); } catch { fail('artifact_activation_metadata_invalid'); }
  if (bytes !== `${canonical(value)}\n` || value.schema !== INSTALLED_SCHEMA ||
      Object.keys(value).sort().join('\0') !== 'artifact_id\0carrier_sha256\0epoch\0kind\0schema\0tree\0tree_digest\0version' ||
      !SAFE_ID.test(value.artifact_id) || !SAFE_ID.test(value.kind) || !SHA256.test(value.carrier_sha256) ||
      !Number.isSafeInteger(value.epoch) || value.epoch < 1 || typeof value.version !== 'string' || !SHA256.test(value.tree_digest) ||
      createHash('sha256').update(canonical(value.tree)).digest('hex') !== value.tree_digest) {
    fail('artifact_activation_metadata_invalid');
  }
  validateTreeManifest(value.tree);
  return { value, digest: createHash('sha256').update(bytes).digest('hex') };
}

function validateActivationPointer(artifactID, pointer, {
  installRoot, expectedKind, expectedSha256, platformServices = defaultPlatformServices,
} = {}) {
  const artifactRoot = join(resolve(installRoot), artifactID);
  if (!pointer || pointer.artifact_id !== artifactID || (expectedSha256 && pointer.sha256 !== expectedSha256)) {
    fail('artifact_activation_identity_mismatch');
  }
  const versionsRoot = join(artifactRoot, 'versions');
  const expectedVersionPath = join(versionsRoot, createHash('sha256').update(canonical({
    epoch: pointer.epoch, sha256: pointer.sha256, version: pointer.version,
  })).digest('hex'));
  if (resolve(pointer.version_path) !== expectedVersionPath || !existsSync(expectedVersionPath)) fail('artifact_activation_path_invalid');
  assertPrivateState(expectedVersionPath, 'directory', 'artifact_activation_path_invalid', platformServices);
  const metadata = readInstalledMetadata(join(expectedVersionPath, INSTALLED_METADATA), platformServices);
  if (metadata.digest !== pointer.activation_digest || metadata.value.artifact_id !== artifactID ||
      metadata.value.version !== pointer.version || metadata.value.epoch !== pointer.epoch ||
      metadata.value.carrier_sha256 !== pointer.sha256 ||
      (expectedKind && metadata.value.kind !== expectedKind)) fail('artifact_activation_identity_mismatch');
  validateArtifactTree(expectedVersionPath, {
    ...metadata.value.tree,
    files: [...metadata.value.tree.files, {
      path: INSTALLED_METADATA,
      bytes: statSync(join(expectedVersionPath, INSTALLED_METADATA)).size,
      sha256: metadata.digest,
      mode: 0o600,
      executable: false,
    }],
  }, { platformServices });
  return { ...pointer, kind: metadata.value.kind, epoch: metadata.value.epoch, tree_digest: metadata.value.tree_digest };
}

export function readActivatedArtifact(artifactID, {
  installRoot, expectedKind, expectedSha256, platformServices = defaultPlatformServices,
} = {}) {
  if (!SAFE_ID.test(artifactID ?? '') || typeof installRoot !== 'string') fail('artifact_activation_configuration_invalid');
  const authority = readGenerationAuthorityIfPresent(resolve(installRoot), platformServices);
  if (authority) {
    const pointer = Object.values(authority.active.activations).find((entry) => entry.artifact_id === artifactID);
    if (!pointer) fail('artifact_activation_identity_mismatch');
    return validateActivationPointer(artifactID, pointer, { installRoot, expectedKind, expectedSha256, platformServices });
  }
  const artifactRoot = join(resolve(installRoot), artifactID);
  const pointer = readPointer(join(artifactRoot, 'current.json'), platformServices);
  return validateActivationPointer(artifactID, pointer, { installRoot, expectedKind, expectedSha256, platformServices });
}

function activationPointerRecord(activation) {
  return {
    schema: POINTER_SCHEMA,
    activation_digest: activation.activation_digest,
    artifact_id: activation.artifact_id,
    epoch: activation.epoch,
    sha256: activation.sha256,
    version: activation.version,
    version_path: activation.version_path,
  };
}

function validateVerifiedRelease(release) {
  if (!release || release.schema !== 'pulse.verified_release_manifest.v2' ||
      typeof release.manifest_digest !== 'string' || !SHA256.test(release.manifest_digest) ||
      typeof release.version !== 'string' || !Number.isSafeInteger(release.epoch) || release.epoch < 1 ||
      !release.artifacts || Array.isArray(release.artifacts) || typeof release.artifacts !== 'object') {
    fail('artifact_release_invalid');
  }
  const kinds = Object.keys(release.artifacts).sort();
  if (kinds.length < 1 || kinds.length > 16 || kinds.some((kind) => {
    const artifact = release.artifacts[kind];
    return !artifact || typeof artifact.tree_digest !== 'string' || !SHA256.test(artifact.tree_digest);
  })) fail('artifact_release_invalid');
  return kinds;
}

function validateActivationSetRecord(value) {
  if (!value || Array.isArray(value) ||
      Object.keys(value).sort().join('\0') !== 'activations\0epoch\0manifest_digest\0schema\0version' ||
      value.schema !== ACTIVE_SET_SCHEMA || typeof value.manifest_digest !== 'string' || !SHA256.test(value.manifest_digest) ||
      typeof value.version !== 'string' || !Number.isSafeInteger(value.epoch) || value.epoch < 1 ||
      !value.activations || Array.isArray(value.activations) || typeof value.activations !== 'object') {
    fail('artifact_activation_set_invalid');
  }
  const kinds = Object.keys(value.activations);
  if (kinds.length < 1 || kinds.length > 16 || kinds.some((kind) => !SAFE_ID.test(kind))) {
    fail('artifact_activation_set_invalid');
  }
  return value;
}

function readActivationSetRecord(path, platformServices = defaultPlatformServices) {
  let bytes;
  let value;
  try {
    bytes = platformServices.readPrivateFile(resolve(path), { maxBytes: 64 * 1024 });
  } catch { fail('artifact_activation_set_unsafe'); }
  try { value = JSON.parse(bytes); } catch { fail('artifact_activation_set_invalid'); }
  if (bytes !== `${canonical(value)}\n`) fail('artifact_activation_set_invalid');
  return validateActivationSetRecord(value);
}

function validateGenerationAuthority(value) {
  if (!value || Array.isArray(value) ||
      Object.keys(value).sort().join('\0') !== 'active\0anti_rollback_floor\0previous\0schema' ||
      value.schema !== GENERATION_AUTHORITY_SCHEMA || !Number.isSafeInteger(value.anti_rollback_floor) ||
      value.anti_rollback_floor < 1) fail('artifact_release_authority_invalid');
  validateActivationSetRecord(value.active);
  if (value.anti_rollback_floor < value.active.epoch) fail('artifact_release_authority_invalid');
  if (value.previous !== null) {
    validateActivationSetRecord(value.previous);
    if (value.previous.manifest_digest === value.active.manifest_digest ||
        value.previous.epoch > value.anti_rollback_floor) fail('artifact_release_authority_invalid');
  }
  return value;
}

function readGenerationAuthorityIfPresent(installRoot, platformServices = defaultPlatformServices) {
  let bytes;
  try {
    bytes = platformServices.readPrivateFile(join(resolve(installRoot), GENERATION_AUTHORITY_FILE), {
      missing: true, maxBytes: 128 * 1024,
    });
  } catch { fail('artifact_release_authority_unsafe'); }
  if (bytes === null) return null;
  let value;
  try { value = JSON.parse(bytes); } catch { fail('artifact_release_authority_invalid'); }
  if (bytes !== `${canonical(value)}\n`) fail('artifact_release_authority_invalid');
  return validateGenerationAuthority(value);
}

export function readArtifactGenerationAuthority({ installRoot, platformServices = defaultPlatformServices } = {}) {
  if (typeof installRoot !== 'string') fail('artifact_activation_configuration_invalid');
  const authority = readGenerationAuthorityIfPresent(resolve(installRoot), platformServices);
  if (!authority) fail('artifact_release_authority_unavailable');
  return Object.freeze(authority);
}

export function readArtifactGenerationFloor({ installRoot, platformServices = defaultPlatformServices } = {}) {
  if (typeof installRoot !== 'string') fail('artifact_activation_configuration_invalid');
  return readGenerationAuthorityIfPresent(resolve(installRoot), platformServices)?.anti_rollback_floor ?? 0;
}

function validateGeneration(record, { installRoot, release, platformServices = defaultPlatformServices } = {}) {
  validateActivationSetRecord(record);
  const kinds = Object.keys(record.activations).sort();
  if (release) {
    const releaseKinds = validateVerifiedRelease(release);
    if (record.manifest_digest !== release.manifest_digest || record.version !== release.version ||
        record.epoch !== release.epoch || kinds.join('\0') !== releaseKinds.join('\0')) {
      fail('artifact_activation_set_identity_mismatch');
    }
  }
  const activations = {};
  for (const kind of kinds) {
    const pointer = record.activations[kind];
    const artifact = release?.artifacts[kind];
    if (!pointer || typeof pointer.artifact_id !== 'string' || !SAFE_ID.test(pointer.artifact_id)) {
      fail('artifact_activation_set_invalid');
    }
    if (artifact && (artifact.id !== pointer.artifact_id || artifact.kind !== kind ||
        artifact.version !== release.version || artifact.epoch !== release.epoch)) fail('artifact_release_invalid');
    const activation = validateActivationPointer(pointer.artifact_id, pointer, {
      installRoot, expectedKind: kind, expectedSha256: artifact?.sha256, platformServices,
    });
    if (activation.version !== record.version || activation.epoch !== record.epoch ||
        (artifact && activation.tree_digest !== artifact.tree_digest)) fail('artifact_activation_set_identity_mismatch');
    activations[kind] = activation;
  }
  return Object.freeze({ activations: Object.freeze(activations), record: Object.freeze(record) });
}

export function readActivatedArtifactSet(release, { installRoot, platformServices = defaultPlatformServices } = {}) {
  if (typeof installRoot !== 'string') fail('artifact_activation_configuration_invalid');
  const authority = readGenerationAuthorityIfPresent(resolve(installRoot), platformServices);
  const record = authority?.active ?? readActivationSetRecord(join(resolve(installRoot), ACTIVE_SET_FILE), platformServices);
  return validateGeneration(record, { installRoot, release, platformServices });
}

// Resolve the last atomically committed compatibility set without consulting
// mutable per-artifact current pointers. Lazy recovery uses this generation
// while a newer install may still be switching individual artifacts.
export function readCommittedArtifactSet({ installRoot, platformServices = defaultPlatformServices } = {}) {
  if (typeof installRoot !== 'string') fail('artifact_activation_configuration_invalid');
  const authority = readGenerationAuthorityIfPresent(resolve(installRoot), platformServices);
  const record = authority?.active ?? readActivationSetRecord(join(resolve(installRoot), ACTIVE_SET_FILE), platformServices);
  return validateGeneration(record, { installRoot, platformServices });
}

function installedActivationFor(artifact, { installRoot, platformServices = defaultPlatformServices } = {}) {
  validateDescriptor(artifact);
  const versionPath = join(resolve(installRoot), artifact.id, 'versions', createHash('sha256').update(canonical({
    epoch: artifact.epoch, sha256: artifact.sha256, version: artifact.version,
  })).digest('hex'));
  const metadata = readInstalledMetadata(join(versionPath, INSTALLED_METADATA), platformServices);
  const pointer = {
    schema: POINTER_SCHEMA, activation_digest: metadata.digest, artifact_id: artifact.id,
    epoch: artifact.epoch, sha256: artifact.sha256, version: artifact.version, version_path: versionPath,
  };
  return validateActivationPointer(artifact.id, pointer, {
    installRoot, expectedKind: artifact.kind, expectedSha256: artifact.sha256, platformServices,
  });
}

export function readStagedArtifactSet(release, { installRoot, platformServices = defaultPlatformServices } = {}) {
  const kinds = validateVerifiedRelease(release);
  if (typeof installRoot !== 'string') fail('artifact_activation_configuration_invalid');
  const activations = {};
  for (const kind of kinds) {
    const artifact = release.artifacts[kind];
    const activation = installedActivationFor(artifact, { installRoot, platformServices });
    if (activation.tree_digest !== artifact.tree_digest) fail('artifact_activation_set_identity_mismatch');
    activations[kind] = activationPointerRecord(activation);
  }
  const record = {
    activations, epoch: release.epoch, manifest_digest: release.manifest_digest,
    schema: ACTIVE_SET_SCHEMA, version: release.version,
  };
  return validateGeneration(record, { installRoot, release, platformServices });
}

function refreshDerivedActivationCaches(authority, installRoot, platformServices) {
  atomicJSON(join(installRoot, ACTIVE_SET_FILE), authority.active, platformServices);
  for (const [kind, pointer] of Object.entries(authority.active.activations)) {
    const artifactRoot = join(installRoot, pointer.artifact_id);
    ensurePrivateDirectory(artifactRoot, platformServices);
    const previous = authority.previous?.activations[kind];
    if (previous?.artifact_id === pointer.artifact_id) atomicJSON(join(artifactRoot, 'previous.json'), previous, platformServices);
    atomicJSON(join(artifactRoot, 'current.json'), pointer, platformServices);
  }
}

export function commitArtifactGeneration(release, {
  installRoot, minimumAcceptedEpoch = 0, platformServices = defaultPlatformServices,
} = {}) {
  if (typeof installRoot !== 'string') fail('artifact_activation_configuration_invalid');
  if (!Number.isSafeInteger(minimumAcceptedEpoch) || minimumAcceptedEpoch < 0) fail('artifact_generation_floor_invalid');
  const root = resolve(installRoot);
  const candidate = readStagedArtifactSet(release, { installRoot: root, platformServices }).record;
  const existingAuthority = readGenerationAuthorityIfPresent(root, platformServices);
  let current = existingAuthority;
  const floor = Math.max(current?.anti_rollback_floor ?? 0, minimumAcceptedEpoch);
  if (release.epoch < floor) fail('artifact_generation_rollback');
  if (!current && existsSync(join(root, ACTIVE_SET_FILE))) {
    const legacyActive = readActivationSetRecord(join(root, ACTIVE_SET_FILE), platformServices);
    validateGeneration(legacyActive, { installRoot: root, platformServices });
    current = {
      active: legacyActive, anti_rollback_floor: Math.max(minimumAcceptedEpoch, legacyActive.epoch),
      previous: null, schema: GENERATION_AUTHORITY_SCHEMA,
    };
  }
  if (existingAuthority?.active.manifest_digest === candidate.manifest_digest) {
    try { refreshDerivedActivationCaches(existingAuthority, root, platformServices); } catch { /* repair is best-effort */ }
    return validateGeneration(existingAuthority.active, { installRoot: root, release, platformServices });
  }
  ensurePrivateDirectory(root, platformServices);
  const authority = {
    active: candidate,
    anti_rollback_floor: Math.max(floor, release.epoch),
    previous: current?.active.manifest_digest === candidate.manifest_digest ? null : (current?.active ?? null),
    schema: GENERATION_AUTHORITY_SCHEMA,
  };
  validateGenerationAuthority(authority);
  atomicJSON(join(root, GENERATION_AUTHORITY_FILE), authority, platformServices);
  try { refreshDerivedActivationCaches(authority, root, platformServices); } catch { /* caches never outrank authority */ }
  return validateGeneration(authority.active, { installRoot: root, release, platformServices });
}

export function recoverCommittedArtifactGeneration({ installRoot, platformServices = defaultPlatformServices } = {}) {
  if (typeof installRoot !== 'string') fail('artifact_activation_configuration_invalid');
  const root = resolve(installRoot);
  const current = readGenerationAuthorityIfPresent(root, platformServices);
  if (!current?.previous) fail('artifact_generation_recovery_unavailable');
  const authority = {
    active: current.previous,
    anti_rollback_floor: current.anti_rollback_floor,
    previous: current.active,
    schema: GENERATION_AUTHORITY_SCHEMA,
  };
  validateGeneration(authority.active, { installRoot: root, platformServices });
  validateGenerationAuthority(authority);
  atomicJSON(join(root, GENERATION_AUTHORITY_FILE), authority, platformServices);
  try { refreshDerivedActivationCaches(authority, root, platformServices); } catch { /* caches never outrank authority */ }
  return validateGeneration(authority.active, { installRoot: root, platformServices });
}

export function writeActivatedArtifactSet(release, { installRoot, platformServices = defaultPlatformServices } = {}) {
  return commitArtifactGeneration(release, { installRoot, platformServices });
}

function defaultMaterializerFor(artifact) {
  if (artifact.format === 'tar.gz') return materializeVerifiedPortableArchive;
  if (artifact.kind === 'model') return materializeVerifiedModelFile;
  if (artifact.format === 'dmg') return materializeVerifiedDmg;
  return materializeVerifiedTree;
}

export async function activateArtifactVersion(artifact, stagedPath, {
  installRoot, materialize, treeManifest, testOnlyMaterializer = false,
  publishDerivedPointers = true,
  availableBytes = defaultAvailableBytes, minimumFreeBytes = 256 * 1024 * 1024,
  platformServices = defaultPlatformServices,
} = {}) {
  validateDescriptor(artifact);
  const selectedMaterializer = materialize ?? defaultMaterializerFor(artifact);
  if (typeof installRoot !== 'string' || typeof selectedMaterializer !== 'function') fail('artifact_activation_configuration_invalid');
  if (![materializeVerifiedTree, materializeVerifiedModelFile, materializeVerifiedDmg, materializeVerifiedPortableArchive]
    .includes(selectedMaterializer) && !testOnlyMaterializer) {
    fail('artifact_materializer_not_allowed');
  }
  const artifactRoot = join(resolve(installRoot), artifact.id);
  const versionsRoot = join(artifactRoot, 'versions');
  ensurePrivateDirectory(versionsRoot, platformServices);
  const currentPath = pointerPath(artifactRoot, 'current.json');
  const previousPath = pointerPath(artifactRoot, 'previous.json');
  let current = null;
  let currentPointer = null;
  if (publishDerivedPointers && existsSync(currentPath)) {
    try {
      currentPointer = readPointer(currentPath, platformServices);
      current = validateActivationPointer(artifact.id, currentPointer, {
        installRoot, expectedKind: artifact.kind, platformServices,
      });
    } catch { fail('artifact_activation_current_invalid'); }
    const expectedTreeDigest = treeManifest === undefined ? null : createHash('sha256').update(canonical(treeManifest)).digest('hex');
    if (current.sha256 === artifact.sha256 && current.version === artifact.version && current.epoch === artifact.epoch) {
      if (expectedTreeDigest !== null && current.tree_digest !== expectedTreeDigest) fail('artifact_activation_identity_mismatch');
      if (artifact.tree_digest !== undefined && current.tree_digest !== artifact.tree_digest) fail('artifact_tree_digest_mismatch');
      return current;
    }
  }
  const target = join(versionsRoot, createHash('sha256').update(canonical({
    epoch: artifact.epoch, sha256: artifact.sha256, version: artifact.version,
  })).digest('hex'));
  let activationTree = treeManifest;
  if (!existsSync(target)) {
    const materializedBytes = treeManifest?.files?.reduce((total, entry) => total + entry.bytes, 0) ?? artifact.bytes;
    const requiredBytes = materializedBytes + minimumFreeBytes;
    const freeBytes = typeof availableBytes === 'function' ? availableBytes(versionsRoot) : null;
    if (typeof availableBytes !== 'function' || !Number.isSafeInteger(minimumFreeBytes) || minimumFreeBytes < 0 ||
        !Number.isSafeInteger(materializedBytes) || materializedBytes < 1 ||
        !Number.isSafeInteger(requiredBytes) || !Number.isSafeInteger(freeBytes) || freeBytes < requiredBytes) {
      fail('artifact_disk_insufficient');
    }
    const temporary = `${target}.new-${process.pid}-${Date.now()}`;
    ensurePrivateDirectory(temporary, platformServices);
    try {
      const materialized = await selectedMaterializer(stagedPath, temporary, artifact, treeManifest, {
        availableBytes, minimumFreeBytes, platformServices,
      });
      activationTree = materialized?.treeManifest ?? treeManifest;
      assertArtifactTreeDigest(artifact, activationTree);
      validateArtifactTree(temporary, activationTree, { platformServices });
      validateDataOnlyModel(temporary, artifact, activationTree, platformServices);
      const metadata = installedMetadata(artifact, activationTree);
      atomicJSON(join(temporary, INSTALLED_METADATA), metadata, platformServices);
      renameSync(temporary, target);
      assertPrivateState(target, 'directory', 'artifact_activation_path_invalid', platformServices);
    } catch (error) {
      rmSync(temporary, { recursive: true, force: true });
      throw error;
    }
  } else {
    const metadata = readInstalledMetadata(join(target, INSTALLED_METADATA), platformServices);
    activationTree = treeManifest ?? metadata.value.tree;
    assertArtifactTreeDigest(artifact, activationTree);
    if (canonical(metadata.value) !== canonical(installedMetadata(artifact, activationTree))) fail('artifact_activation_identity_mismatch');
    validateArtifactTree(target, {
      ...activationTree,
      files: [...activationTree.files, {
        path: INSTALLED_METADATA, bytes: statSync(join(target, INSTALLED_METADATA)).size,
        sha256: metadata.digest, mode: 0o600, executable: false,
      }],
    }, { platformServices });
    validateDataOnlyModel(target, artifact, activationTree, platformServices);
  }
  const activationDigest = sha256File(join(target, INSTALLED_METADATA), platformServices).digest;
  const next = {
    schema: POINTER_SCHEMA, artifact_id: artifact.id, epoch: artifact.epoch,
    sha256: artifact.sha256, version: artifact.version,
    version_path: target, activation_digest: activationDigest,
  };
  if (publishDerivedPointers) {
    if (currentPointer) atomicJSON(previousPath, currentPointer, platformServices);
    atomicJSON(currentPath, next, platformServices);
  }
  return next;
}

export function recoverArtifactActivation(artifactID, {
  installRoot, platformServices = defaultPlatformServices,
} = {}) {
  if (!SAFE_ID.test(artifactID ?? '') || typeof installRoot !== 'string') fail('artifact_activation_configuration_invalid');
  const artifactRoot = join(resolve(installRoot), artifactID);
  const currentPath = pointerPath(artifactRoot, 'current.json');
  const previousPath = pointerPath(artifactRoot, 'previous.json');
  try {
    const current = readPointer(currentPath, platformServices);
    if (current) return {
      status: 'current',
      activation: validateActivationPointer(artifactID, current, { installRoot, platformServices }),
    };
  } catch { /* try previous */ }
  let previous;
  let validatedPrevious;
  try {
    previous = readPointer(previousPath, platformServices);
    validatedPrevious = validateActivationPointer(artifactID, previous, { installRoot, platformServices });
  } catch { fail('artifact_activation_unrecoverable'); }
  atomicJSON(currentPath, previous, platformServices);
  return { status: 'rolled_back', activation: validatedPrevious };
}
