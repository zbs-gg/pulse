import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  acquireCLIInvocation,
  completeCLIInvocation,
  consumeCLIResponse,
} from './cli-idempotency.js';

test('CLI lost-response journal reuses one key then releases it after a received response', () => {
  const dataDir = mkdtempSync(join(tmpdir(), 'pulse-cli-idempotency.'));
  const secretBody = { summary: 'private content that must not enter the journal' };
  const first = acquireCLIInvocation(dataDir, '/memory/remember', secretBody, new Date('2026-07-14T09:00:00Z'));
  const retry = acquireCLIInvocation(dataDir, '/memory/remember', secretBody, new Date('2026-07-14T09:01:00Z'));
  assert.equal(retry.key, first.key);
  assert.doesNotMatch(readFileSync(first.journalPath, 'utf8'), /private content/);
  completeCLIInvocation(first);
  const next = acquireCLIInvocation(dataDir, '/memory/remember', secretBody, new Date('2026-07-14T09:02:00Z'));
  assert.notEqual(next.key, first.key);
});

test('CLI keeps one invocation key when a committed success body is lost', async () => {
  const dataDir = mkdtempSync(join(tmpdir(), 'pulse-cli-idempotency.'));
  const body = { summary: 'same logical write' };
  const first = acquireCLIInvocation(dataDir, '/memory/remember', body);

  await assert.rejects(
    consumeCLIResponse({
      ok: true,
      status: 200,
      json: async () => { throw new Error('truncated response body'); },
    }, first),
    /truncated response body/,
  );

  const retry = acquireCLIInvocation(dataDir, '/memory/remember', body);
  assert.equal(retry.key, first.key);
});
