import { createHash, randomBytes } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const KEY = /^cli_[a-f0-9]{32}$/;

export function acquireCLIInvocation(dataDir, path, body, now = new Date()) {
  const directory = join(dataDir, 'cli-invocations');
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  const fingerprint = createHash('sha256')
    .update('pulse-cli-invocation-v1\x00')
    .update(path)
    .update('\x00')
    .update(JSON.stringify(body ?? null))
    .digest('hex');
  const journalPath = join(directory, `${fingerprint}.json`);
  if (existsSync(journalPath)) {
    try {
      const saved = JSON.parse(readFileSync(journalPath, 'utf8'));
      const age = now.valueOf() - Date.parse(saved.created_at);
      if (saved.schema === 'pulse.cli_invocation.v1' && KEY.test(saved.key) && age >= 0 && age < 24 * 60 * 60 * 1000) {
        return { key: saved.key, journalPath };
      }
    } catch { /* replace invalid or expired content-free journal */ }
    rmSync(journalPath, { force: true });
  }
  const key = `cli_${randomBytes(16).toString('hex')}`;
  const record = `${JSON.stringify({
    schema: 'pulse.cli_invocation.v1', key, created_at: now.toISOString(),
  })}\n`;
  try {
    writeFileSync(journalPath, record, { mode: 0o600, flag: 'wx' });
    return { key, journalPath };
  } catch (error) {
    if (error?.code !== 'EEXIST') throw error;
    const saved = JSON.parse(readFileSync(journalPath, 'utf8'));
    if (saved.schema !== 'pulse.cli_invocation.v1' || !KEY.test(saved.key)) throw error;
    return { key: saved.key, journalPath };
  }
}

export function completeCLIInvocation(invocation) {
  if (invocation?.journalPath) rmSync(invocation.journalPath, { force: true });
}

export async function consumeCLIResponse(response, invocation) {
  if (!response.ok) {
    const detail = (await response.text()).slice(0, 500);
    completeCLIInvocation(invocation);
    throw new Error(`Pulse HTTP ${response.status}: ${detail}`);
  }
  if (response.status === 204) {
    completeCLIInvocation(invocation);
    return { ok: true };
  }
  const payload = await response.json();
  completeCLIInvocation(invocation);
  return payload;
}
