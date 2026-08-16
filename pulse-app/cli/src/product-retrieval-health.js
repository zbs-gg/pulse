const RETRIEVAL_HEALTH_QUERY = 'Pulse semantic retrieval health check';

function failureDetail(error) {
  const code = error?.name === 'TimeoutError' || error?.name === 'AbortError'
    ? 'timed out'
    : typeof error?.message === 'string' && error.message.startsWith('pulse_http_')
      ? error.message.split(':', 1)[0].replace('pulse_http_', 'returned HTTP ')
      : 'failed';
  return `semantic retrieval ${code}`;
}
export async function probeProductRetrieval(resolved, {
  request,
  timeoutMs = 5000,
} = {}) {
  if (typeof request !== 'function' || !resolved) {
    return { ok: false, detail: 'semantic retrieval is unavailable' };
  }
  const startedAt = Date.now();
  try {
    const result = await request(resolved, '/retrieve', {
      body: { query: RETRIEVAL_HEALTH_QUERY, mode: 'factual', top_k: 1 },
      timeoutMs,
    });
    if (!Array.isArray(result?.event_ids)) {
      return { ok: false, detail: 'semantic retrieval returned an invalid response' };
    }
    return {
      ok: true,
      detail: `semantic retrieval answered in ${Date.now() - startedAt} ms`,
    };
  } catch (error) {
    return { ok: false, detail: failureDetail(error) };
  }
}
