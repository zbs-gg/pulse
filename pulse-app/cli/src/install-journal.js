import { spawnSync } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import {
  chmodSync, closeSync, existsSync, fsyncSync, lstatSync, mkdirSync, openSync, readFileSync, renameSync, rmSync, writeFileSync,
} from 'node:fs';
import { dirname } from 'node:path';

const SCHEMA = 'pulse.personal_install_journal.v1';
const FIELDS = Object.freeze([
  'artifact_ids', 'manifest_digest', 'phase', 'previous_activation_digest',
  'release_epoch', 'release_version', 'schema',
]);
const SAFE_ID = /^[a-z0-9][a-z0-9._-]{0,127}$/;
const SHA256 = /^[a-f0-9]{64}$/;
const PHASES = new Set(['planned', 'downloading', 'artifacts_staged', 'activating', 'activated']);
const LOCK_SCHEMA = 'pulse.personal_install_lock.v1';
const LOCK_OWNER_FILE = 'owner.json';

export class InstallJournalError extends Error {
  constructor(code) {
    super(code);
    this.name = 'InstallJournalError';
    this.code = code;
  }
}

function fail(code) { throw new InstallJournalError(code); }

function canonical(value) {
  if (value === null) return 'null';
  if (typeof value === 'string' || typeof value === 'boolean') return JSON.stringify(value);
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value) || Object.is(value, -0)) fail('install_journal_value_invalid');
    return String(value);
  }
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`;
  if (value && typeof value === 'object') {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}`;
  }
  fail('install_journal_value_invalid');
}

function validate(record) {
  if (!record || Array.isArray(record) || typeof record !== 'object' ||
      Object.keys(record).sort().join('\0') !== [...FIELDS].sort().join('\0')) fail('install_journal_fields_invalid');
  if (record.schema !== SCHEMA || !Number.isSafeInteger(record.release_epoch) || record.release_epoch < 1 ||
      typeof record.release_version !== 'string' || !/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(record.release_version) ||
      typeof record.manifest_digest !== 'string' || !SHA256.test(record.manifest_digest) ||
      !PHASES.has(record.phase) ||
      (record.previous_activation_digest !== null &&
        (typeof record.previous_activation_digest !== 'string' || !SHA256.test(record.previous_activation_digest))) ||
      !Array.isArray(record.artifact_ids) || record.artifact_ids.length > 16 ||
      record.artifact_ids.some((id) => typeof id !== 'string' || !SAFE_ID.test(id)) ||
      [...new Set(record.artifact_ids)].sort().join('\0') !== record.artifact_ids.join('\0')) {
    fail('install_journal_value_invalid');
  }
  return record;
}

function ensurePrivateDirectory(path) {
  mkdirSync(path, { recursive: true, mode: 0o700 });
  const stat = lstatSync(path);
  const uid = typeof process.geteuid === 'function' ? process.geteuid() : stat.uid;
  if (!stat.isDirectory() || stat.isSymbolicLink() || stat.uid !== uid || (stat.mode & 0o077) !== 0) fail('install_journal_directory_unsafe');
}

function syncDirectory(path) {
  const fd = openSync(path, 'r');
  try { fsyncSync(fd); } finally { closeSync(fd); }
}

export function writeInstallJournal(path, record) {
  validate(record);
  const directory = dirname(path);
  ensurePrivateDirectory(directory);
  const temporary = `${path}.new-${process.pid}-${Date.now()}`;
  try {
    writeFileSync(temporary, `${canonical(record)}\n`, { encoding: 'utf8', flag: 'wx', mode: 0o600 });
    chmodSync(temporary, 0o600);
    const fd = openSync(temporary, 'r');
    try { fsyncSync(fd); } finally { closeSync(fd); }
    renameSync(temporary, path);
    syncDirectory(directory);
  } catch (error) {
    rmSync(temporary, { force: true });
    if (error instanceof InstallJournalError) throw error;
    fail('install_journal_write_failed');
  }
  return record;
}

export function readInstallJournal(path) {
  if (!existsSync(path)) return null;
  let stat;
  try { stat = lstatSync(path); } catch { fail('install_journal_read_failed'); }
  const uid = typeof process.geteuid === 'function' ? process.geteuid() : stat.uid;
  if (!stat.isFile() || stat.isSymbolicLink() || stat.uid !== uid || (stat.mode & 0o077) !== 0 || stat.size > 16 * 1024) {
    fail('install_journal_unsafe');
  }
  let bytes;
  let value;
  try {
    bytes = readFileSync(path, 'utf8');
    value = JSON.parse(bytes);
  } catch { fail('install_journal_invalid'); }
  validate(value);
  if (bytes !== `${canonical(value)}\n`) fail('install_journal_noncanonical');
  return value;
}

export function clearInstallJournal(path) {
  if (!existsSync(path)) return;
  rmSync(path, { force: true });
  syncDirectory(dirname(path));
}

function processStartIdentity(pid) {
  if (!Number.isSafeInteger(pid) || pid <= 1) return '';
  const result = spawnSync('/bin/ps', ['-ww', '-p', String(pid), '-o', 'lstart='], {
    encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'], timeout: 2000,
  });
  return result.status === 0 ? result.stdout.trim() : '';
}

function lockOwner(path) {
  const lockStat = lstatSync(path);
  const uid = typeof process.geteuid === 'function' ? process.geteuid() : lockStat.uid;
  if (!lockStat.isDirectory() || lockStat.isSymbolicLink() || lockStat.uid !== uid || (lockStat.mode & 0o077) !== 0) {
    fail('install_lock_unsafe');
  }
  const ownerPath = `${path}/${LOCK_OWNER_FILE}`;
  const stat = lstatSync(ownerPath);
  if (!stat.isFile() || stat.isSymbolicLink() || stat.nlink !== 1 || stat.uid !== uid || (stat.mode & 0o077) !== 0 || stat.size > 2048) {
    fail('install_lock_unsafe');
  }
  let bytes;
  let value;
  try { bytes = readFileSync(ownerPath, 'utf8'); value = JSON.parse(bytes); } catch { fail('install_lock_unsafe'); }
  if (bytes !== `${canonical(value)}\n` || Object.keys(value).sort().join('\0') !== 'pid\0process_start\0schema\0token' ||
      value.schema !== LOCK_SCHEMA || !Number.isSafeInteger(value.pid) || value.pid <= 1 ||
      typeof value.process_start !== 'string' || value.process_start.length < 1 || value.process_start.length > 128 ||
      typeof value.token !== 'string' || !/^[a-f0-9]{64}$/.test(value.token)) fail('install_lock_unsafe');
  return value;
}

function createOwnedLock(lockPath, owner) {
  const candidate = `${lockPath}.candidate-${owner.pid}-${owner.token}`;
  try {
    mkdirSync(candidate, { mode: 0o700 });
    const ownerPath = `${candidate}/${LOCK_OWNER_FILE}`;
    writeFileSync(ownerPath, `${canonical(owner)}\n`, { encoding: 'utf8', flag: 'wx', mode: 0o600 });
    const fd = openSync(ownerPath, 'r');
    try { fsyncSync(fd); } finally { closeSync(fd); }
    syncDirectory(candidate);
    try { renameSync(candidate, lockPath); } catch (error) {
      if (error?.code === 'EEXIST' || error?.code === 'ENOTEMPTY') {
        const occupied = new Error('install lock occupied');
        occupied.code = 'EEXIST';
        throw occupied;
      }
      throw error;
    }
    syncDirectory(dirname(lockPath));
  } finally {
    rmSync(candidate, { recursive: true, force: true });
  }
}

export function acquireInstallLock(lockPath) {
  ensurePrivateDirectory(dirname(lockPath));
  const processStart = processStartIdentity(process.pid);
  if (!processStart) fail('install_lock_process_identity_unavailable');
  const owner = {
    schema: LOCK_SCHEMA, pid: process.pid, process_start: processStart,
    token: randomBytes(32).toString('hex'),
  };
  try {
    createOwnedLock(lockPath, owner);
  } catch (error) {
    if (error?.code !== 'EEXIST') {
      if (error instanceof InstallJournalError) throw error;
      fail('install_lock_failed');
    }
    let stale;
    try { stale = lockOwner(lockPath); } catch { fail('install_lock_unsafe'); }
    const liveStart = processStartIdentity(stale.pid);
    if (liveStart && liveStart === stale.process_start) fail('install_locked');
    const quarantine = `${lockPath}.stale-${randomBytes(12).toString('hex')}`;
    try { renameSync(lockPath, quarantine); } catch { fail('install_locked'); }
    let moved;
    try { moved = lockOwner(quarantine); } catch {
      try { renameSync(quarantine, lockPath); } catch { /* preserve ambiguous quarantine for inspection */ }
      fail('install_lock_unsafe');
    }
    if (moved.token !== stale.token || moved.pid !== stale.pid || moved.process_start !== stale.process_start) {
      try { renameSync(quarantine, lockPath); } catch { /* preserve ambiguous quarantine for inspection */ }
      fail('install_locked');
    }
    rmSync(quarantine, { recursive: true, force: true });
    syncDirectory(dirname(lockPath));
    try { createOwnedLock(lockPath, owner); } catch { fail('install_locked'); }
  }
  let released = false;
  return () => {
    if (released) return;
    const current = lockOwner(lockPath);
    if (current.token !== owner.token || current.pid !== owner.pid || current.process_start !== owner.process_start) fail('install_unlock_not_owner');
    const quarantine = `${lockPath}.release-${owner.token}`;
    try {
      renameSync(lockPath, quarantine);
      const moved = lockOwner(quarantine);
      if (moved.token !== owner.token) {
        try { renameSync(quarantine, lockPath); } catch { /* preserve quarantine instead of deleting another owner's lock */ }
        fail('install_unlock_not_owner');
      }
      rmSync(quarantine, { recursive: true, force: true });
      syncDirectory(dirname(lockPath));
    } catch (error) {
      if (error instanceof InstallJournalError) throw error;
      fail('install_unlock_failed');
    }
    released = true;
  };
}
