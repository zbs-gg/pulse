import { dirname } from 'node:path';

import { defaultPlatformServices, PlatformServicesError } from './platform-services.js';

const SCHEMA = 'pulse.personal_install_journal.v1';
const FIELDS = Object.freeze([
  'artifact_ids', 'manifest_digest', 'phase', 'previous_activation_digest',
  'release_epoch', 'release_version', 'schema',
]);
const SAFE_ID = /^[a-z0-9][a-z0-9._-]{0,127}$/;
const SHA256 = /^[a-f0-9]{64}$/;
const PHASES = new Set(['planned', 'downloading', 'artifacts_staged', 'activating', 'candidate_staged', 'activated']);

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

export function writeInstallJournal(path, record, { platformServices = defaultPlatformServices } = {}) {
  validate(record);
  const directory = dirname(path);
  try { platformServices.ensurePrivateDirectory(directory); } catch {
    fail('install_journal_directory_unsafe');
  }
  try {
    platformServices.atomicWritePrivateFile(path, `${canonical(record)}\n`, {
      ensureParent: false, maxBytes: 16 * 1024,
    });
  } catch (error) {
    if (error instanceof InstallJournalError) throw error;
    fail('install_journal_write_failed');
  }
  return record;
}

export function readInstallJournal(path, { platformServices = defaultPlatformServices } = {}) {
  let bytes;
  let value;
  try {
    bytes = platformServices.readPrivateFile(path, { missing: true, maxBytes: 16 * 1024 });
    if (bytes === null) return null;
    value = JSON.parse(bytes);
  } catch (error) {
    if (error instanceof PlatformServicesError && error.code === 'platform_private_state_unsafe') {
      fail('install_journal_unsafe');
    }
    if (error instanceof SyntaxError) fail('install_journal_invalid');
    fail('install_journal_read_failed');
  }
  validate(value);
  if (bytes !== `${canonical(value)}\n`) fail('install_journal_noncanonical');
  return value;
}

export function clearInstallJournal(path, { platformServices = defaultPlatformServices } = {}) {
  try { platformServices.removePrivateFile(path, { missing: true }); } catch {
    fail('install_journal_write_failed');
  }
}

export function acquireInstallLock(lockPath, {
  platformServices = defaultPlatformServices, staleAfterMs = 0, timeoutMs = 0,
} = {}) {
  let release;
  try {
    release = platformServices.acquirePrivateLock(lockPath, { staleAfterMs, timeoutMs });
  } catch (error) {
    if (error instanceof PlatformServicesError) {
      if (error.code === 'platform_lock_identity_unavailable') fail('install_lock_process_identity_unavailable');
      if (error.code === 'platform_lock_occupied') fail('install_locked');
      if (error.code === 'platform_lock_unsafe' || error.code === 'platform_private_state_unsafe') fail('install_lock_unsafe');
    }
    fail('install_lock_failed');
  }
  let released = false;
  return () => {
    if (released) return;
    try {
      release();
    } catch (error) {
      if (error instanceof PlatformServicesError) {
        if (error.code === 'platform_unlock_not_owner') fail('install_unlock_not_owner');
        if (error.code === 'platform_lock_unsafe' || error.code === 'platform_private_state_unsafe') fail('install_lock_unsafe');
      }
      fail('install_unlock_failed');
    }
    released = true;
  };
}
