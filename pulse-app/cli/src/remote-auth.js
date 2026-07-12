const LOOPBACK_HOSTS = new Set(['127.0.0.1', 'localhost', '[::1]', '::1']);

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
  if (ipcSecret && isLoopbackPulseBase(baseURL)) {
    headers['X-Pulse-Key'] = ipcSecret;
  }
  if (remoteBearer) {
    headers.Authorization = `Bearer ${remoteBearer}`;
  }
  return headers;
}
