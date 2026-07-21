import { RemoteAuthError, remoteAuthFail as fail } from './remote-auth-errors.js';

const LOOPBACK_HOSTS = new Set(['127.0.0.1', 'localhost', '[::1]', '::1']);
export const DEFAULT_REMOTE_NETWORK_TIMEOUT_MS = 10_000;
const MIN_NETWORK_TIMEOUT_MS = 10;
const MAX_NETWORK_TIMEOUT_MS = 30_000;

function networkTimeout(value) {
  if (!Number.isInteger(value) || value < MIN_NETWORK_TIMEOUT_MS || value > MAX_NETWORK_TIMEOUT_MS) {
    fail('network_timeout_invalid');
  }
  return value;
}

export async function boundedRemoteFetch(fetchFn, url, init = {}, {
  timeoutMs = DEFAULT_REMOTE_NETWORK_TIMEOUT_MS,
  signal,
} = {}) {
  if (typeof fetchFn !== 'function') fail('http_client_unavailable');
  const deadline = networkTimeout(timeoutMs);
  const controller = new AbortController();
  const externalSignals = [signal, init.signal].filter(Boolean);
  const abortListeners = [];
  let timedOut = false;
  const abort = (source) => controller.abort(source?.reason ?? new RemoteAuthError('network_aborted'));
  for (const external of externalSignals) {
    if (external.aborted) abort(external);
    else {
      const listener = () => abort(external);
      abortListeners.push([external, listener]);
      external.addEventListener('abort', listener, { once: true });
    }
  }
  if (controller.signal.aborted) fail('network_aborted');
  let timer;
  const timeout = new Promise((_resolve, reject) => {
    timer = setTimeout(() => {
      timedOut = true;
      const error = new RemoteAuthError('network_timeout');
      controller.abort(error);
      reject(error);
    }, deadline);
  });
  try {
    return await Promise.race([
      Promise.resolve().then(() => fetchFn(url, { ...init, signal: controller.signal })),
      timeout,
    ]);
  } catch (error) {
    if (timedOut) fail('network_timeout');
    if (controller.signal.aborted) fail('network_aborted');
    throw error;
  } finally {
    clearTimeout(timer);
    for (const [external, listener] of abortListeners) external.removeEventListener('abort', listener);
  }
}

export async function boundedRemoteRead(read, {
  timeoutMs = DEFAULT_REMOTE_NETWORK_TIMEOUT_MS,
  signal,
  cancel,
} = {}) {
  if (typeof read !== 'function') fail('http_client_unavailable');
  const deadline = networkTimeout(timeoutMs);
  if (signal?.aborted) fail('network_aborted');
  let timer;
  let abortListener;
  const timeout = new Promise((_resolve, reject) => {
    timer = setTimeout(() => {
      try { Promise.resolve(cancel?.()).catch(() => {}); } catch { /* timeout remains authoritative */ }
      reject(new RemoteAuthError('network_timeout'));
    }, deadline);
  });
  const aborted = new Promise((_resolve, reject) => {
    if (!signal) return;
    abortListener = () => reject(new RemoteAuthError('network_aborted'));
    signal.addEventListener('abort', abortListener, { once: true });
  });
  try {
    return await Promise.race([Promise.resolve().then(read), timeout, aborted]);
  } finally {
    clearTimeout(timer);
    if (signal && abortListener) signal.removeEventListener('abort', abortListener);
  }
}

export function isLoopbackPulseBase(baseURL) {
  let parsed;
  try {
    parsed = new URL(baseURL);
  } catch {
    return false;
  }
  return (
    (parsed.protocol === 'http:' || parsed.protocol === 'https:') &&
    LOOPBACK_HOSTS.has(parsed.hostname.toLowerCase())
  );
}

export function requireLoopbackPulseIPC(baseURL) {
  if (!isLoopbackPulseBase(baseURL)) {
    throw new Error('Pulse IPC administration requires an explicit loopback base URL');
  }
  return new URL(baseURL);
}

export function buildPulseRequestHeaders(baseURL, { ipcSecret = '', remoteBearer = '' } = {}) {
  const headers = { 'Content-Type': 'application/json' };
  if (ipcSecret && isLoopbackPulseBase(baseURL)) headers['X-Pulse-Key'] = ipcSecret;
  if (remoteBearer) {
    if (!isLoopbackPulseBase(baseURL)) fail('static_bearer_forbidden');
    headers.Authorization = `Bearer ${remoteBearer}`;
  }
  return headers;
}
