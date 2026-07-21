import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  InstallJournalError,
  acquireInstallLock,
  clearInstallJournal,
  readInstallJournal,
  writeInstallJournal,
} from './install-journal.js';

function sandbox() {
  const root = mkdtempSync(join(tmpdir(), 'pulse-install-journal-'));
  return { root, journal: join(root, 'install.json'), lock: join(root, 'install.lock') };
}

function record(overrides = {}) {
  return {
    schema: 'pulse.personal_install_journal.v1',
    release_epoch: 7,
    release_version: '0.8.0',
    manifest_digest: 'a'.repeat(64),
    phase: 'artifacts_staged',
    artifact_ids: ['pulse-daemon', 'pulse-model'],
    previous_activation_digest: null,
    ...overrides,
  };
}

test('journal is canonical, private, content-free, and durable enough to resume', () => {
  const setup = sandbox();
  try {
    writeInstallJournal(setup.journal, record());
    const bytes = readFileSync(setup.journal, 'utf8');
    assert.equal(bytes.endsWith('\n'), true);
    assert.equal(bytes.includes('/Users/'), false);
    assert.deepEqual(readInstallJournal(setup.journal), record());
  } finally { rmSync(setup.root, { recursive: true, force: true }); }
});

test('journal rejects content/path fields and non-canonical or unsafe files', () => {
  const setup = sandbox();
  try {
    assert.throws(() => writeInstallJournal(setup.journal, {
      ...record(), raw_prompt: 'private words',
    }), (error) => error instanceof InstallJournalError && error.code === 'install_journal_fields_invalid');
    writeFileSync(setup.journal, `${JSON.stringify(record())}\n`, { mode: 0o600 });
    assert.throws(() => readInstallJournal(setup.journal), /install_journal_noncanonical/);
    writeInstallJournal(setup.journal, record());
    chmodSync(setup.journal, 0o644);
    assert.throws(() => readInstallJournal(setup.journal), /install_journal_unsafe/);
  } finally { rmSync(setup.root, { recursive: true, force: true }); }
});

test('cross-process lock fails closed for a concurrent installer and releases explicitly', () => {
  const setup = sandbox();
  try {
    const release = acquireInstallLock(setup.lock);
    assert.throws(() => acquireInstallLock(setup.lock), (error) =>
      error instanceof InstallJournalError && error.code === 'install_locked');
    release();
    const releaseAgain = acquireInstallLock(setup.lock);
    releaseAgain();
  } finally { rmSync(setup.root, { recursive: true, force: true }); }
});

test('lock acquisition forwards a bounded stale-recovery floor to the platform', () => {
  let received;
  let released = false;
  const release = acquireInstallLock('/private/install.lock', {
    staleAfterMs: 5000,
    timeoutMs: 250,
    platformServices: {
      acquirePrivateLock(path, options) {
        received = { path, options };
        return () => { released = true; };
      },
    },
  });
  assert.deepEqual(received, {
    path: '/private/install.lock',
    options: { staleAfterMs: 5000, timeoutMs: 250 },
  });
  release();
  assert.equal(released, true);
});

test('a crashed installer lock is recovered only after its exact process instance is gone', () => {
  const setup = sandbox();
  try {
    const moduleURL = new URL('./install-journal.js', import.meta.url).href;
    const child = spawnSync(process.execPath, ['--input-type=module', '-e', `
      import { acquireInstallLock } from ${JSON.stringify(moduleURL)};
      acquireInstallLock(${JSON.stringify(setup.lock)});
    `], { encoding: 'utf8' });
    assert.equal(child.status, 0, child.stderr);
    const release = acquireInstallLock(setup.lock);
    release();
  } finally { rmSync(setup.root, { recursive: true, force: true }); }
});

test('clear is idempotent and removes a settled journal', () => {
  const setup = sandbox();
  try {
    writeInstallJournal(setup.journal, record());
    clearInstallJournal(setup.journal);
    clearInstallJournal(setup.journal);
    assert.equal(existsSync(setup.journal), false);
    assert.equal(readInstallJournal(setup.journal), null);
  } finally { rmSync(setup.root, { recursive: true, force: true }); }
});
