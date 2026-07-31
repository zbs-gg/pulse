import { createHash } from 'node:crypto';
import {
  closeSync, constants, fsyncSync, lstatSync, mkdirSync, openSync, renameSync, rmSync, writeSync,
} from 'node:fs';

const REDIRECT_STATUSES = new Set([301, 302, 303, 307, 308]);
const SHA256 = /^[a-f0-9]{64}$/;

function checkedOrigins(values) {
  if (!Array.isArray(values) || values.length > 8 || new Set(values).size !== values.length) {
    throw new Error('source_download_configuration_invalid');
  }
  const origins = new Set();
  for (const value of values) {
    let parsed;
    try { parsed = new URL(value); } catch { throw new Error('source_download_configuration_invalid'); }
    if (parsed.protocol !== 'https:' || parsed.origin !== value || parsed.pathname !== '/' || parsed.search || parsed.hash ||
        parsed.username || parsed.password) throw new Error('source_download_configuration_invalid');
    origins.add(value);
  }
  return origins;
}

function checkedURL(value, allowedOrigins, { allowQuery = false } = {}) {
  let parsed;
  try { parsed = new URL(value); } catch { throw new Error('source_download_url_invalid'); }
  if (parsed.protocol !== 'https:' || parsed.username || parsed.password || (!allowQuery && parsed.search) || parsed.hash ||
      !allowedOrigins.has(parsed.origin)) throw new Error('source_download_origin_invalid');
  return parsed;
}

function writeAll(descriptor, data) {
  let offset = 0;
  while (offset < data.length) {
    const count = writeSync(descriptor, data, offset, data.length - offset);
    if (count < 1) throw new Error('source_download_write_failed');
    offset += count;
  }
}

export function ensurePrivateSourceCache(path) {
  if (typeof path !== 'string' || path.length < 1) throw new Error('source_cache_invalid');
  mkdirSync(path, { recursive: true, mode: 0o700 });
  const stat = lstatSync(path);
  const uid = typeof process.geteuid === 'function' ? process.geteuid() : stat.uid;
  if (!stat.isDirectory() || stat.isSymbolicLink() || stat.uid !== uid || (stat.mode & 0o077) !== 0) {
    throw new Error('source_cache_unsafe');
  }
  return path;
}

export async function downloadPinnedSource(component, {
  allowedOrigins,
  allowedRedirectOrigins = [],
  destination,
  fetchImpl = globalThis.fetch,
  maximumRedirects = 5,
  timeoutMs = 30 * 60 * 1000,
} = {}) {
  if (!component || typeof component.url !== 'string' || !Number.isSafeInteger(component.bytes) || component.bytes < 1 ||
      typeof component.sha256 !== 'string' || !SHA256.test(component.sha256) || typeof destination !== 'string' ||
      !Array.isArray(allowedOrigins) || allowedOrigins.length < 1 || !Array.isArray(allowedRedirectOrigins) ||
      typeof fetchImpl !== 'function' ||
      !Number.isSafeInteger(maximumRedirects) || maximumRedirects < 0 || maximumRedirects > 10 ||
      !Number.isSafeInteger(timeoutMs) || timeoutMs < 1) throw new Error('source_download_configuration_invalid');

  const origins = checkedOrigins(allowedOrigins);
  const redirectOrigins = checkedOrigins(allowedRedirectOrigins);

  const partial = `${destination}.partial`;
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(new Error('source_download_timeout')), timeoutMs);
  timeout.unref?.();
  let descriptor;
  let committed = false;
  try {
    rmSync(partial, { force: true });
    let current = checkedURL(component.url, origins);
    let response;
    for (let redirects = 0; ; redirects += 1) {
      response = await fetchImpl(current, { redirect: 'manual', signal: controller.signal });
      if (REDIRECT_STATUSES.has(response?.status)) {
        if (redirects >= maximumRedirects) throw new Error('source_download_redirect_limit');
        const location = response.headers?.get?.('location');
        if (typeof location !== 'string' || location.length < 1) throw new Error('source_download_redirect_invalid');
        await response.body?.cancel?.().catch(() => {});
        current = checkedURL(new URL(location, current), redirectOrigins, { allowQuery: true });
        continue;
      }
      if (!response?.ok || response.body == null) throw new Error('source_download_failed');
      break;
    }

    const contentLength = response.headers?.get?.('content-length');
    if (contentLength !== null && contentLength !== undefined) {
      if (!/^(?:0|[1-9]\d*)$/.test(contentLength) || Number(contentLength) > component.bytes) {
        throw new Error('source_download_size_exceeded');
      }
    }

    descriptor = openSync(partial, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
    const hash = createHash('sha256');
    let received = 0;
    for await (const value of response.body) {
      const chunk = Buffer.from(value);
      received += chunk.length;
      if (received > component.bytes) {
        controller.abort(new Error('source_download_size_exceeded'));
        throw new Error('source_download_size_exceeded');
      }
      writeAll(descriptor, chunk);
      hash.update(chunk);
    }
    if (received !== component.bytes || hash.digest('hex') !== component.sha256) {
      throw new Error('source_download_verification_failed');
    }
    fsyncSync(descriptor);
    closeSync(descriptor);
    descriptor = undefined;
    renameSync(partial, destination);
    committed = true;
    return destination;
  } finally {
    clearTimeout(timeout);
    if (descriptor !== undefined) closeSync(descriptor);
    if (!committed) rmSync(partial, { force: true });
  }
}
