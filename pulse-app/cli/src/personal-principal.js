import { randomBytes as cryptoRandomBytes } from 'node:crypto';
import { homedir } from 'node:os';
import { dirname, isAbsolute, join, resolve } from 'node:path';

import { acquireInstallLock, InstallJournalError } from './install-journal.js';
import { defaultPlatformServices, PlatformServicesError } from './platform-services.js';

const SCHEMA = 'pulse.personal_principal.v1';
const PRINCIPAL_ID = /^principal_[a-f0-9]{32}$/;
const LOCK_WAIT = new Int32Array(new SharedArrayBuffer(4));
const PRINCIPAL_LOCK_STALE_AFTER_MS = 5000;

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

function inspectPrivateDirectory(path, platformServices, { missing = false } = {}) {
  try { platformServices.assertPrivateState(path, { kind: 'directory' }); } catch (error) {
    if (missing && error?.code === 'ENOENT') return false;
    fail('principal_directory_unsafe');
  }
  return true;
}

function ensurePrivateDirectory(path, platformServices) {
  try { platformServices.ensurePrivateDirectory(path); } catch { fail('principal_directory_unsafe'); }
}

function acquirePrincipalCreationLock(path, platformServices) {
  const deadline = Date.now() + 5000;
  while (true) {
    try {
      return acquireInstallLock(path, {
        platformServices,
        // A contender can observe the lock between exclusive create and the
        // durable record write. Never quarantine that brand-new lock merely
        // because process inspection was momentarily unavailable.
        staleAfterMs: PRINCIPAL_LOCK_STALE_AFTER_MS,
      });
    } catch (error) {
      if (!(error instanceof InstallJournalError) ||
          (error.code !== 'install_locked' && error.code !== 'install_lock_unsafe')) {
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

function readRecord(path, platformServices) {
  let bytes;
  let record;
  try { bytes = platformServices.readPrivateFile(path, { missing: true, minBytes: 1, maxBytes: 4096 }); } catch (error) {
    if (error instanceof PlatformServicesError || error?.code === 'ENOENT') fail('principal_file_unsafe');
    fail('principal_file_unsafe');
  }
  if (bytes === null) return null;
  try { record = JSON.parse(bytes); } catch { fail('principal_invalid'); }
  if (!record || Array.isArray(record) || typeof record !== 'object' ||
      Object.keys(record).sort().join('\0') !== 'principal_id\0schema' ||
      record.schema !== SCHEMA || !PRINCIPAL_ID.test(record.principal_id ?? '')) {
    fail('principal_invalid');
  }
  if (bytes !== canonicalRecord(record)) fail('principal_noncanonical');
  return Object.freeze({ schema: record.schema, principal_id: record.principal_id });
}

export function readPersonalPrincipal({ home = homedir(), platformServices = defaultPlatformServices } = {}) {
  const path = personalPrincipalPath(home);
  const pulseDirectory = dirname(dirname(path));
  const identityDirectory = dirname(path);
  if (!inspectPrivateDirectory(pulseDirectory, platformServices, { missing: true })) return null;
  if (!inspectPrivateDirectory(identityDirectory, platformServices, { missing: true })) return null;
  return readRecord(path, platformServices);
}

export function ensurePersonalPrincipal({
  home = homedir(), consentGranted = false, randomBytes = cryptoRandomBytes,
  platformServices = defaultPlatformServices,
} = {}) {
  const existing = readPersonalPrincipal({ home, platformServices });
  if (existing) return existing;
  if (consentGranted !== true) fail('principal_consent_required');
  if (typeof randomBytes !== 'function') fail('principal_random_unavailable');

  const path = personalPrincipalPath(home);
  const pulseDirectory = dirname(dirname(path));
  const identityDirectory = dirname(path);
  ensurePrivateDirectory(pulseDirectory, platformServices);
  ensurePrivateDirectory(identityDirectory, platformServices);
  const releaseLock = acquirePrincipalCreationLock(join(identityDirectory, 'personal-principal.lock'), platformServices);
  try {
    const raced = readRecord(path, platformServices);
    if (raced) return raced;

    const entropy = randomBytes(16);
    if (!Buffer.isBuffer(entropy) || entropy.length !== 16) fail('principal_random_unavailable');
    const record = { schema: SCHEMA, principal_id: `principal_${entropy.toString('hex')}` };
    try {
      platformServices.atomicWritePrivateFile(path, canonicalRecord(record), {
        ensureParent: false, maxBytes: 4096,
      });
    } catch (error) {
      if (error instanceof PersonalPrincipalError) throw error;
      fail('principal_write_failed');
    }
  } finally {
    releaseLock();
  }
  const result = readRecord(path, platformServices);
  if (!result) fail('principal_write_failed');
  return result;
}
