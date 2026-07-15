import { randomBytes as cryptoRandomBytes } from 'node:crypto';
import {
  closeSync, fsyncSync, lstatSync, mkdirSync, openSync, readFileSync, renameSync, rmSync, writeFileSync,
} from 'node:fs';
import { homedir } from 'node:os';
import { dirname, isAbsolute, join, resolve } from 'node:path';

import { acquireInstallLock, InstallJournalError } from './install-journal.js';

const SCHEMA = 'pulse.personal_principal.v1';
const PRINCIPAL_ID = /^principal_[a-f0-9]{32}$/;
const LOCK_WAIT = new Int32Array(new SharedArrayBuffer(4));

export class PersonalPrincipalError extends Error {
  constructor(code) {
    super(code);
    this.name = 'PersonalPrincipalError';
    this.code = code;
  }
}

function fail(code) { throw new PersonalPrincipalError(code); }

export function personalPrincipalPath(home = homedir()) {
  if (typeof home !== 'string' || !isAbsolute(home)) fail('principal_home_invalid');
  return join(resolve(home), '.pulse', 'identity', 'personal-principal.json');
}

function currentUID(stat) {
  return typeof process.geteuid === 'function' ? process.geteuid() : stat.uid;
}

function inspectPrivateDirectory(path, { missing = false } = {}) {
  let stat;
  try { stat = lstatSync(path); } catch (error) {
    if (missing && error?.code === 'ENOENT') return false;
    fail('principal_directory_unsafe');
  }
  if (!stat.isDirectory() || stat.isSymbolicLink() || stat.uid !== currentUID(stat) || (stat.mode & 0o077) !== 0) {
    fail('principal_directory_unsafe');
  }
  return true;
}

function syncDirectory(path) {
  const descriptor = openSync(path, 'r');
  try { fsyncSync(descriptor); } finally { closeSync(descriptor); }
}

function ensurePrivateDirectory(path) {
  try {
    mkdirSync(path, { mode: 0o700 });
    syncDirectory(dirname(path));
  } catch (error) {
    if (error?.code !== 'EEXIST') fail('principal_directory_unsafe');
  }
  inspectPrivateDirectory(path);
}

function acquirePrincipalCreationLock(path) {
  const deadline = Date.now() + 5000;
  while (true) {
    try {
      return acquireInstallLock(path);
    } catch (error) {
      let disappearedDuringInspection = false;
      if (error instanceof InstallJournalError && error.code === 'install_lock_unsafe') {
        try { lstatSync(path); } catch (statError) {
          disappearedDuringInspection = statError?.code === 'ENOENT';
        }
      }
      if (!(error instanceof InstallJournalError) ||
          (error.code !== 'install_locked' && !disappearedDuringInspection)) {
        fail('principal_write_failed');
      }
      if (Date.now() >= deadline) fail('principal_write_failed');
      Atomics.wait(LOCK_WAIT, 0, 0, 10);
    }
  }
}

function canonicalRecord(record) {
  return `${JSON.stringify({ principal_id: record.principal_id, schema: record.schema })}\n`;
}

function readRecord(path) {
  let stat;
  try { stat = lstatSync(path); } catch (error) {
    if (error?.code === 'ENOENT') return null;
    fail('principal_file_unsafe');
  }
  if (!stat.isFile() || stat.isSymbolicLink() || stat.nlink !== 1 || stat.uid !== currentUID(stat) ||
      (stat.mode & 0o077) !== 0 || stat.size < 1 || stat.size > 4096) {
    fail('principal_file_unsafe');
  }
  let bytes;
  let record;
  try { bytes = readFileSync(path, 'utf8'); record = JSON.parse(bytes); } catch { fail('principal_invalid'); }
  if (!record || Array.isArray(record) || typeof record !== 'object' ||
      Object.keys(record).sort().join('\0') !== 'principal_id\0schema' ||
      record.schema !== SCHEMA || !PRINCIPAL_ID.test(record.principal_id ?? '')) {
    fail('principal_invalid');
  }
  if (bytes !== canonicalRecord(record)) fail('principal_noncanonical');
  return Object.freeze({ schema: record.schema, principal_id: record.principal_id });
}

export function readPersonalPrincipal({ home = homedir() } = {}) {
  const path = personalPrincipalPath(home);
  const pulseDirectory = dirname(dirname(path));
  const identityDirectory = dirname(path);
  if (!inspectPrivateDirectory(pulseDirectory, { missing: true })) return null;
  if (!inspectPrivateDirectory(identityDirectory, { missing: true })) return null;
  return readRecord(path);
}

export function ensurePersonalPrincipal({
  home = homedir(), consentGranted = false, randomBytes = cryptoRandomBytes,
} = {}) {
  const existing = readPersonalPrincipal({ home });
  if (existing) return existing;
  if (consentGranted !== true) fail('principal_consent_required');
  if (typeof randomBytes !== 'function') fail('principal_random_unavailable');

  const path = personalPrincipalPath(home);
  const pulseDirectory = dirname(dirname(path));
  const identityDirectory = dirname(path);
  ensurePrivateDirectory(pulseDirectory);
  ensurePrivateDirectory(identityDirectory);
  const releaseLock = acquirePrincipalCreationLock(join(identityDirectory, 'personal-principal.lock'));
  try {
    const raced = readRecord(path);
    if (raced) return raced;

    const entropy = randomBytes(16);
    if (!Buffer.isBuffer(entropy) || entropy.length !== 16) fail('principal_random_unavailable');
    const record = { schema: SCHEMA, principal_id: `principal_${entropy.toString('hex')}` };
    const temporary = join(identityDirectory, `.personal-principal.${process.pid}.${Date.now()}.${cryptoRandomBytes(8).toString('hex')}.new`);
    try {
      writeFileSync(temporary, canonicalRecord(record), { encoding: 'utf8', flag: 'wx', mode: 0o600 });
      const descriptor = openSync(temporary, 'r');
      try { fsyncSync(descriptor); } finally { closeSync(descriptor); }
      renameSync(temporary, path);
      syncDirectory(identityDirectory);
    } catch (error) {
      if (error instanceof PersonalPrincipalError) throw error;
      fail('principal_write_failed');
    } finally {
      rmSync(temporary, { force: true });
    }
  } finally {
    releaseLock();
  }
  const result = readRecord(path);
  if (!result) fail('principal_write_failed');
  return result;
}
