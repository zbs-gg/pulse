import { spawn, spawnSync } from 'node:child_process';
import {
  createHash, createPublicKey, verify as cryptoVerify,
} from 'node:crypto';
import {
  chmodSync, closeSync, constants, copyFileSync, existsSync, lstatSync, mkdirSync, mkdtempSync, openSync,
  readFileSync, renameSync, rmSync, statSync, writeFileSync,
} from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  bindingRegistryAnchor, canonicalJSONStringify,
} from './workspace-binding.js';

export const INSTALL_CONFIRMATION = 'install pulse presence helper';
export const EXPECTED_HELPER_IDENTIFIER = 'gg.zbs.pulse.presence-helper';
export const EXPECTED_HELPER_TEAM_IDENTIFIER = '44N4NZ86S5';
export const EXPECTED_HELPER_CONTRACT_VERSION = 3;
export const EXPECTED_HELPER_CAPABILITIES = Object.freeze([
  'dpop-create', 'dpop-delete', 'dpop-proof', 'dpop-public',
  'prove', 'public-key', 'self-test', 'sign-binding-registry',
]);
export const EXPECTED_HELPER_SELF_TEST = Object.freeze({
  contract_version: EXPECTED_HELPER_CONTRACT_VERSION,
  schema: 'pulse.presence_helper.self_test.v2',
  suite: 'es256-p1363-policy-v1',
  vectors: 29,
});

export const DEFAULT_TRUST_PATHS = Object.freeze({
  helperPath: '/Library/PrivilegedHelperTools/gg.zbs.pulse.presence-helper',
  publicKeyPath: '/Library/Application Support/Pulse/trust/workspace-bindings.pub.pem',
  anchorPath: '/Library/Application Support/Pulse/trust/workspace-bindings.anchor.json',
  registryPath: join(homedir(), '.pulse', 'supervisor', 'workspace-bindings.json'),
  migrationJournalPath: join(homedir(), '.pulse', 'supervisor', 'presence-trust-migration.json'),
  vendoredHelperPath: fileURLToPath(new URL(
    '../vendor/pulse-presence-helper/gg.zbs.pulse.presence-helper',
    import.meta.url,
  )),
});

const CODESIGN = '/usr/bin/codesign';
const SUDO = '/usr/bin/sudo';
const INSTALL = '/usr/bin/install';
const MV = '/bin/mv';
const MKDIR = '/bin/mkdir';
const RM = '/bin/rm';
const RMDIR = '/bin/rmdir';
const LOCKF = '/usr/bin/lockf';
const SH = '/bin/sh';
const INSTALL_LOCK_PATH = '/var/run/gg.zbs.pulse.presence-trust-install.lock';
const INSTALL_LOCK_READY = 'pulse-presence-install-lock-ready\n';
const BOOTSTRAP_MIGRATION_SCHEMA = 'pulse.presence_bootstrap_migration.v1';

function fail(code) {
  const error = new Error(`trust_${code}`);
  error.code = `trust_${code}`;
  throw error;
}

function defaultRun(command, args, options = {}) {
  return spawnSync(command, args, {
    encoding: 'utf8',
    timeout: options.timeout ?? 30_000,
    stdio: options.stdio ?? ['ignore', 'pipe', 'pipe'],
    maxBuffer: 1024 * 1024,
  });
}

function exactPaths(paths) {
  const merged = { ...DEFAULT_TRUST_PATHS, ...(paths ?? {}) };
  for (const key of [
    'anchorPath', 'helperPath', 'migrationJournalPath', 'publicKeyPath', 'registryPath',
    'vendoredHelperPath',
  ]) {
    if (typeof merged[key] !== 'string' || !merged[key].startsWith('/')) fail('paths_invalid');
  }
  return merged;
}

function exactResult(result) {
  return result && result.status === 0 && result.signal == null && !result.error;
}

function boundedOutput(result) {
  const stdout = typeof result?.stdout === 'string' ? result.stdout : '';
  const stderr = typeof result?.stderr === 'string' ? result.stderr : '';
  if (stdout.length > 1024 * 1024 || stderr.length > 1024 * 1024) return '';
  return `${stdout}\n${stderr}`;
}

function modeString(mode) {
  return (mode & 0o7777).toString(8).padStart(4, '0');
}

function inspectRegularFile(path, { expectedUID, expectedMode, executable = false } = {}) {
  if (!existsSync(path)) return { exists: false };
  try {
    const link = lstatSync(path);
    const info = statSync(path);
    const mode = modeString(info.mode);
    return {
      exists: true,
      regular: link.isFile() && info.isFile() && !link.isSymbolicLink(),
      root_owned: info.uid === expectedUID,
      mode,
      mode_valid: mode === expectedMode,
      executable: !executable || (info.mode & 0o111) !== 0,
    };
  } catch {
    return { exists: true, regular: false, root_owned: false, mode: null, mode_valid: false, executable: false };
  }
}

function inspectCodeSignature(path, run) {
  const verified = run(CODESIGN, ['--verify', '--strict', '--verbose=2', path], { timeout: 30_000 });
  if (!exactResult(verified)) {
    return { valid: false, identifier: null, team_identifier: null };
  }
  const described = run(CODESIGN, ['-d', '--verbose=4', path], { timeout: 30_000 });
  if (!exactResult(described)) {
    return { valid: false, identifier: null, team_identifier: null };
  }
  const output = boundedOutput(described);
  const identifier = output.match(/^Identifier=([^\r\n]+)$/m)?.[1] ?? null;
  const teamIdentifier = output.match(/^TeamIdentifier=([^\r\n]+)$/m)?.[1] ?? null;
  return {
    valid: identifier === EXPECTED_HELPER_IDENTIFIER && teamIdentifier === EXPECTED_HELPER_TEAM_IDENTIFIER,
    identifier,
    team_identifier: teamIdentifier,
  };
}

function publicKeyDER(value) {
  try {
    if (typeof value !== 'string' || value.length < 100 || value.length > 16_384) return null;
    const key = createPublicKey(value);
    if (key.asymmetricKeyType !== 'ec' || key.asymmetricKeyDetails?.namedCurve !== 'prime256v1') return null;
    return key.export({ type: 'spki', format: 'der' });
  } catch {
    return null;
  }
}

function readPublicKey(path) {
  try {
    const value = readFileSync(path, 'utf8');
    return { value, der: publicKeyDER(value) };
  } catch {
    return { value: null, der: null };
  }
}

function bootstrapPublicKeyDER(value) {
  try {
    if (typeof value !== 'string' || value.length < 100 || value.length > 16_384) return null;
    const key = createPublicKey(value);
    if (key.asymmetricKeyType !== 'ed25519') return null;
    return key.export({ type: 'spki', format: 'der' });
  } catch {
    return null;
  }
}

function readBootstrapPublicKey(path) {
  try {
    const value = readFileSync(path, 'utf8');
    return { value, der: bootstrapPublicKeyDER(value) };
  } catch {
    return { value: null, der: null };
  }
}

function exactSignedRegistry(registryBytes, publicKeyValue, algorithm) {
  let envelope;
  try { envelope = JSON.parse(registryBytes); } catch { fail('bootstrap_registry_invalid'); }
  if (!envelope || Array.isArray(envelope) || typeof envelope !== 'object' ||
      Object.keys(envelope).sort().join('\0') !== 'algorithm\0payload\0signature' ||
      envelope.algorithm !== algorithm || typeof envelope.signature !== 'string' ||
      !envelope.payload || Array.isArray(envelope.payload) || typeof envelope.payload !== 'object' ||
      Object.keys(envelope.payload).sort().join('\0') !== 'bindings\0epoch\0schema' ||
      envelope.payload.schema !== 'pulse.workspace-binding-registry.v1' ||
      !Number.isSafeInteger(envelope.payload.epoch) || envelope.payload.epoch < 1 ||
      !Array.isArray(envelope.payload.bindings) || envelope.payload.bindings.length < 1 ||
      envelope.payload.bindings.length > 128 ||
      !registryBytes.equals(Buffer.from(`${JSON.stringify(envelope)}\n`))) {
    fail('bootstrap_registry_invalid');
  }
  let signature;
  try { signature = Buffer.from(envelope.signature, 'base64'); } catch { fail('bootstrap_signature_invalid'); }
  if (signature.length < 1 || signature.toString('base64') !== envelope.signature) {
    fail('bootstrap_signature_invalid');
  }
  let publicKey;
  try { publicKey = createPublicKey(publicKeyValue); } catch { fail('bootstrap_key_invalid'); }
  const payloadBytes = Buffer.from(canonicalJSONStringify(envelope.payload));
  const valid = algorithm === 'ed25519'
    ? signature.length === 64 && cryptoVerify(null, payloadBytes, publicKey, signature)
    : algorithm === 'es256' && cryptoVerify('sha256', payloadBytes, publicKey, signature);
  if (!valid) fail('bootstrap_signature_invalid');
  return Object.freeze({ envelope, payloadBytes });
}

function exactRegistryAnchor(anchorBytes, registryBytes, epoch) {
  let anchor;
  try { anchor = JSON.parse(anchorBytes); } catch { fail('bootstrap_anchor_invalid'); }
  const expected = bindingRegistryAnchor(registryBytes, epoch);
  if (!anchor || Array.isArray(anchor) || typeof anchor !== 'object' ||
      Object.keys(anchor).sort().join('\0') !== 'epoch\0registry_sha256\0schema' ||
      !anchorBytes.equals(Buffer.from(`${canonicalJSONStringify(anchor)}\n`)) ||
      canonicalJSONStringify(anchor) !== canonicalJSONStringify(expected)) {
    fail('bootstrap_anchor_invalid');
  }
  return anchor;
}

function legacyBootstrapState(trustPaths, expectedRootUID, expectedSourceUID) {
  const keyFile = inspectRegularFile(trustPaths.publicKeyPath, {
    expectedUID: expectedRootUID, expectedMode: '0644', executable: false,
  });
  const anchorFile = inspectRegularFile(trustPaths.anchorPath, {
    expectedUID: expectedRootUID, expectedMode: '0644', executable: false,
  });
  const registryFile = inspectRegularFile(trustPaths.registryPath, {
    expectedUID: expectedSourceUID, expectedMode: '0600', executable: false,
  });
  if (!keyFile.exists || !keyFile.regular || !keyFile.root_owned || !keyFile.mode_valid ||
      !anchorFile.exists || !anchorFile.regular || !anchorFile.root_owned || !anchorFile.mode_valid ||
      !registryFile.exists || !registryFile.regular || !registryFile.root_owned || !registryFile.mode_valid) {
    return null;
  }
  const publicKey = readBootstrapPublicKey(trustPaths.publicKeyPath);
  if (!publicKey.der) return null;
  const registryBytes = readFileSync(trustPaths.registryPath);
  const anchorBytes = readFileSync(trustPaths.anchorPath);
  const verified = exactSignedRegistry(registryBytes, publicKey.value, 'ed25519');
  exactRegistryAnchor(anchorBytes, registryBytes, verified.envelope.payload.epoch);
  return Object.freeze({
    anchorBytes,
    epoch: verified.envelope.payload.epoch,
    payload: verified.envelope.payload,
    publicKeyBytes: Buffer.from(publicKey.value),
    registryBytes,
  });
}

function readHelperPublicKey(helperPath, run) {
  const result = run(helperPath, ['public-key'], {
    timeout: 180_000,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  if (!exactResult(result) || typeof result.stdout !== 'string' || result.stdout.length > 16_384) return null;
  return publicKeyDER(result.stdout) ? result.stdout : null;
}

function readHelperContract(helperPath, run) {
  const result = run(helperPath, ['contract'], {
    timeout: 5_000,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  if (!exactResult(result) || typeof result.stdout !== 'string' || result.stdout.length > 4096) return null;
  try {
    const value = JSON.parse(result.stdout);
    if (!value || Array.isArray(value) || typeof value !== 'object' ||
        Object.keys(value).sort().join('\0') !== 'capabilities\0schema\0self_test\0version' ||
        value.schema !== 'pulse.presence_helper.contract.v1' ||
        value.version !== EXPECTED_HELPER_CONTRACT_VERSION ||
        !Array.isArray(value.capabilities) ||
        value.capabilities.join('\0') !== EXPECTED_HELPER_CAPABILITIES.join('\0') ||
        !value.self_test || Array.isArray(value.self_test) ||
        Object.keys(value.self_test).sort().join('\0') !== 'contract_version\0schema\0suite\0vectors' ||
        Object.keys(EXPECTED_HELPER_SELF_TEST).some((key) => value.self_test[key] !== EXPECTED_HELPER_SELF_TEST[key])) return null;
    return Object.freeze({
      version: value.version,
      capabilities: Object.freeze([...value.capabilities]),
      self_test: EXPECTED_HELPER_SELF_TEST,
    });
  } catch {
    return null;
  }
}

function helperSelfTest(helperPath, run) {
  const result = run(helperPath, ['self-test'], {
    timeout: 5_000,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  if (!exactResult(result) || typeof result.stdout !== 'string' || result.stdout.length > 4096) return false;
  try {
    const value = JSON.parse(result.stdout);
    return value && !Array.isArray(value) &&
      Object.keys(value).sort().join('\0') === 'contract_version\0schema\0status\0suite\0vectors' &&
      value.status === 'pass' &&
      Object.keys(EXPECTED_HELPER_SELF_TEST).every((key) => value[key] === EXPECTED_HELPER_SELF_TEST[key]);
  } catch {
    return false;
  }
}

function samePublicKey(left, right) {
  const leftDER = publicKeyDER(left);
  const rightDER = publicKeyDER(right);
  return Boolean(leftDER && rightDER && leftDER.equals(rightDER));
}

export function inspectPresenceTrust({
  paths,
  run = defaultRun,
  expectedRootUID = 0,
  probePublicKey = false,
  probeCapabilities,
  expectedPublicKey,
} = {}) {
  const trustPaths = exactPaths(paths);
  if (typeof run !== 'function' || !Number.isInteger(expectedRootUID) || expectedRootUID < 0 ||
      typeof probePublicKey !== 'boolean' ||
      (probeCapabilities !== undefined && typeof probeCapabilities !== 'boolean')) fail('request_invalid');

  const issues = [];
  const helperFile = inspectRegularFile(trustPaths.helperPath, {
    expectedUID: expectedRootUID, expectedMode: '0755', executable: true,
  });
  const publicKeyFile = inspectRegularFile(trustPaths.publicKeyPath, {
    expectedUID: expectedRootUID, expectedMode: '0644', executable: false,
  });

  if (!helperFile.exists) issues.push('helper_missing');
  if (!publicKeyFile.exists) issues.push('public_key_missing');

  if (helperFile.exists) {
    if (!helperFile.regular) issues.push('helper_not_regular');
    if (!helperFile.root_owned) issues.push('helper_not_root_owned');
    if (!helperFile.mode_valid || !helperFile.executable) issues.push('helper_mode_invalid');
  }
  if (publicKeyFile.exists) {
    if (!publicKeyFile.regular) issues.push('public_key_not_regular');
    if (!publicKeyFile.root_owned) issues.push('public_key_not_root_owned');
    if (!publicKeyFile.mode_valid) issues.push('public_key_mode_invalid');
  }

  let codeSignature = { valid: false, identifier: null, team_identifier: null };
  if (helperFile.exists && helperFile.regular && helperFile.root_owned && helperFile.mode_valid && helperFile.executable) {
    codeSignature = inspectCodeSignature(trustPaths.helperPath, run);
    if (!codeSignature.valid) issues.push('helper_code_identity_invalid');
  }

  const installedKey = publicKeyFile.exists && publicKeyFile.regular ? readPublicKey(trustPaths.publicKeyPath) : { value: null, der: null };
  const bootstrapKey = publicKeyFile.exists && publicKeyFile.regular
    ? readBootstrapPublicKey(trustPaths.publicKeyPath)
    : { value: null, der: null };
  if (publicKeyFile.exists && !installedKey.der) {
    issues.push(bootstrapKey.der ? 'bootstrap_migration_required' : 'public_key_invalid');
  }

  let matchesHelper = null;
  const canCompare = codeSignature.valid && Boolean(installedKey.der);
  if (canCompare && typeof expectedPublicKey === 'string') {
    matchesHelper = samePublicKey(installedKey.value, expectedPublicKey);
  } else if (canCompare && probePublicKey) {
    const helperKey = readHelperPublicKey(trustPaths.helperPath, run);
    if (!helperKey) {
      issues.push('helper_public_key_unavailable');
    } else {
      matchesHelper = samePublicKey(installedKey.value, helperKey);
    }
  }
  if (matchesHelper === false) issues.push('public_key_mismatch');

  const shouldProbeCapabilities = probeCapabilities ?? (probePublicKey || typeof expectedPublicKey === 'string');
  let contract = null;
  if (codeSignature.valid && shouldProbeCapabilities) {
    contract = readHelperContract(trustPaths.helperPath, run);
    if (!contract) issues.push('helper_contract_invalid');
  }

  const bothMissing = !helperFile.exists && !publicKeyFile.exists;
  const ready = issues.length === 0 && matchesHelper === true;
  const status = bothMissing
    ? 'not_installed'
    : ready
      ? 'ready'
      : issues.length === 0
        ? 'installed_unverified'
        : 'invalid';

  return {
    schema: 'pulse.presence_trust_status.v1',
    status,
    ready,
    helper: {
      path: trustPaths.helperPath,
      exists: Boolean(helperFile.exists),
      root_owned: helperFile.root_owned ?? false,
      mode: helperFile.mode ?? null,
      code_signature: codeSignature,
      contract,
    },
    public_key: {
      path: trustPaths.publicKeyPath,
      exists: Boolean(publicKeyFile.exists),
      root_owned: publicKeyFile.root_owned ?? false,
      mode: publicKeyFile.mode ?? null,
      valid: Boolean(installedKey.der),
      bootstrap_legacy: Boolean(bootstrapKey.der),
      matches_helper: matchesHelper,
    },
    issues,
  };
}

function sha256File(path) {
  return createHash('sha256').update(readFileSync(path)).digest('hex');
}

function assertVendoredHelper(path, run, expectedUID) {
  const file = inspectRegularFile(path, { expectedUID, expectedMode: '0755', executable: true });
  if (!file.exists || !file.regular || !file.root_owned || !file.mode_valid || !file.executable) {
    fail('vendored_helper_invalid');
  }
  if (!inspectCodeSignature(path, run).valid) fail('vendored_signature_invalid');
  if (!readHelperContract(path, run) || !helperSelfTest(path, run)) fail('vendored_contract_invalid');
}

function runRequired(run, command, args, code, options = {}) {
  const result = run(command, args, options);
  if (!exactResult(result)) fail(code);
  return result;
}

function sudo(run, args, code) {
  return runRequired(run, SUDO, args, code, {
    timeout: 120_000,
    // sudo may need to ask the human for an administrator password. Hiding
    // stderr makes a real install look frozen while the prompt is waiting.
    stdio: 'inherit',
  });
}

function authorizeSudo(run) {
  if (run !== defaultRun) {
    sudo(run, ['-v'], 'sudo_authorization_failed');
    return;
  }
  let terminal;
  try {
    terminal = openSync('/dev/tty', constants.O_RDWR);
  } catch {
    fail('user_terminal_required');
  }
  try {
    runRequired(run, SUDO, ['-v'], 'sudo_authorization_failed', {
      timeout: 120_000,
      stdio: [terminal, terminal, terminal],
    });
  } finally {
    closeSync(terminal);
  }
}

function defaultAcquireInstallLock({ timeout = 300_000 } = {}) {
  return new Promise((resolve, reject) => {
    const timeoutSeconds = Math.max(1, Math.ceil(timeout / 1000));
    const child = spawn(SUDO, [
      '-n', LOCKF, '-k', '-t', String(timeoutSeconds), INSTALL_LOCK_PATH,
      SH, '-c', `printf '${INSTALL_LOCK_READY}'; IFS= read -r _`,
    ], {
      stdio: ['pipe', 'pipe', 'inherit'],
    });
    let settled = false;
    let output = '';
    const timer = setTimeout(() => {
      if (settled) return;
      settled = true;
      child.kill('SIGTERM');
      reject(new Error('trust_install_lock_timeout'));
    }, timeout);

    const rejectBeforeReady = () => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      reject(new Error('trust_install_lock_failed'));
    };
    child.once('error', rejectBeforeReady);
    child.once('exit', rejectBeforeReady);
    child.stdout.on('data', (chunk) => {
      if (settled) return;
      output += chunk.toString('utf8');
      if (output.length > 1024 || !output.includes(INSTALL_LOCK_READY)) return;
      settled = true;
      clearTimeout(timer);
      child.removeListener('error', rejectBeforeReady);
      child.removeListener('exit', rejectBeforeReady);
      resolve({
        async release() {
          await new Promise((releaseResolve, releaseReject) => {
            const releaseTimer = setTimeout(() => {
              child.kill('SIGTERM');
              releaseReject(new Error('trust_install_lock_release_failed'));
            }, 10_000);
            child.once('error', () => {
              clearTimeout(releaseTimer);
              releaseReject(new Error('trust_install_lock_release_failed'));
            });
            child.once('exit', (code, signal) => {
              clearTimeout(releaseTimer);
              if (code === 0 && signal == null) releaseResolve();
              else releaseReject(new Error('trust_install_lock_release_failed'));
            });
            child.stdin.end('release\n');
          });
        },
      });
    });
  });
}

function strictInstalledHelper(path, run, expectedRootUID, expectedHash) {
  const file = inspectRegularFile(path, {
    expectedUID: expectedRootUID, expectedMode: '0755', executable: true,
  });
  if (!file.exists || !file.regular || !file.root_owned || !file.mode_valid || !file.executable) return false;
  try {
    return inspectCodeSignature(path, run).valid && sha256File(path) === expectedHash;
  } catch {
    return false;
  }
}

function strictInstalledPublicKey(path, expectedRootUID, expectedHash) {
  const file = inspectRegularFile(path, {
    expectedUID: expectedRootUID, expectedMode: '0644', executable: false,
  });
  if (!file.exists || !file.regular || !file.root_owned || !file.mode_valid) return false;
  try {
    return sha256File(path) === expectedHash && Boolean(readPublicKey(path).der);
  } catch {
    return false;
  }
}

function sha256Bytes(bytes) {
  return createHash('sha256').update(bytes).digest('hex');
}

function strictRootDataFile(path, expectedRootUID, expectedHash, { publicKey = null } = {}) {
  const file = inspectRegularFile(path, {
    expectedUID: expectedRootUID, expectedMode: '0644', executable: false,
  });
  if (!file.exists || !file.regular || !file.root_owned || !file.mode_valid) return false;
  try {
    if (sha256File(path) !== expectedHash) return false;
    if (publicKey === 'p256') return Boolean(readPublicKey(path).der);
    if (publicKey === 'ed25519') return Boolean(readBootstrapPublicKey(path).der);
    return true;
  } catch {
    return false;
  }
}

function atomicReplaceRootData(run, source, target, expectedRootUID, expectedHash, options = {}) {
  const stagedTarget = join(dirname(target), `.${target.split('/').at(-1)}.new-${expectedHash.slice(0, 16)}`);
  try {
    sudo(run, [INSTALL, '-o', 'root', '-g', 'wheel', '-m', '0644', source, stagedTarget], 'migration_stage_failed');
    if (!strictRootDataFile(stagedTarget, expectedRootUID, expectedHash, options)) {
      fail('migration_stage_verification_failed');
    }
    sudo(run, [MV, '-f', stagedTarget, target], 'migration_replace_failed');
  } finally {
    if (existsSync(stagedTarget)) {
      try { sudo(run, [RM, '-f', stagedTarget], 'migration_stage_cleanup_failed'); } catch { /* primary failure wins */ }
    }
  }
  if (!strictRootDataFile(target, expectedRootUID, expectedHash, options)) {
    fail('migration_replace_verification_failed');
  }
}

function atomicReplacePrivateData(path, bytes) {
  const temporary = `${path}.${process.pid}.${Date.now()}.new`;
  try {
    writeFileSync(temporary, bytes, { flag: 'wx', mode: 0o600 });
    chmodSync(temporary, 0o600);
    renameSync(temporary, path);
  } finally {
    rmSync(temporary, { force: true });
  }
  if (!readFileSync(path).equals(bytes)) fail('migration_replace_verification_failed');
}

function exactMigrationJournal(trustPaths, expectedSourceUID) {
  const file = inspectRegularFile(trustPaths.migrationJournalPath, {
    expectedUID: expectedSourceUID, expectedMode: '0600', executable: false,
  });
  if (!file.exists || !file.regular || !file.root_owned || !file.mode_valid) {
    fail('migration_journal_invalid');
  }
  const bytes = readFileSync(trustPaths.migrationJournalPath);
  let journal;
  try { journal = JSON.parse(bytes); } catch { fail('migration_journal_invalid'); }
  const validState = (state) => state && !Array.isArray(state) && typeof state === 'object' &&
    Object.keys(state).sort().join('\0') === 'anchor_sha256\0epoch\0public_key_sha256\0registry_sha256' &&
    Number.isSafeInteger(state.epoch) && state.epoch >= 1 &&
    ['anchor_sha256', 'public_key_sha256', 'registry_sha256']
      .every((key) => /^[a-f0-9]{64}$/.test(state[key] ?? ''));
  if (!journal || Array.isArray(journal) || typeof journal !== 'object' ||
      Object.keys(journal).sort().join('\0') !== 'new\0old\0schema' ||
      journal.schema !== BOOTSTRAP_MIGRATION_SCHEMA || !validState(journal.old) ||
      !validState(journal.new) || journal.new.epoch !== journal.old.epoch + 1 ||
      !bytes.equals(Buffer.from(`${canonicalJSONStringify(journal)}\n`))) {
    fail('migration_journal_invalid');
  }
  return journal;
}

function migrationBackupPaths(trustPaths, journal) {
  return Object.freeze({
    anchor: `${trustPaths.anchorPath}.bootstrap-${journal.old.anchor_sha256.slice(0, 16)}`,
    publicKey: `${trustPaths.publicKeyPath}.bootstrap-${journal.old.public_key_sha256.slice(0, 16)}`,
    registry: `${trustPaths.registryPath}.bootstrap-${journal.old.registry_sha256.slice(0, 16)}`,
  });
}

function exactPrivateFile(path, expectedSourceUID, expectedHash) {
  const file = inspectRegularFile(path, {
    expectedUID: expectedSourceUID, expectedMode: '0600', executable: false,
  });
  return Boolean(file.exists && file.regular && file.root_owned && file.mode_valid &&
    sha256File(path) === expectedHash);
}

function removeExactRootData(run, path, expectedRootUID, expectedHash) {
  if (!existsSync(path)) return;
  if (!strictRootDataFile(path, expectedRootUID, expectedHash)) fail('migration_backup_changed');
  sudo(run, [RM, '-f', path], 'migration_cleanup_failed');
  if (existsSync(path)) fail('migration_cleanup_failed');
}

function cleanupMigrationArtifacts({ trustPaths, journal, backups, run, expectedRootUID, expectedSourceUID }) {
  removeExactRootData(run, backups.publicKey, expectedRootUID, journal.old.public_key_sha256);
  removeExactRootData(run, backups.anchor, expectedRootUID, journal.old.anchor_sha256);
  if (existsSync(backups.registry)) {
    if (!exactPrivateFile(backups.registry, expectedSourceUID, journal.old.registry_sha256)) {
      fail('migration_backup_changed');
    }
    rmSync(backups.registry, { force: true });
  }
  if (!exactPrivateFile(
    trustPaths.migrationJournalPath,
    expectedSourceUID,
    sha256Bytes(Buffer.from(`${canonicalJSONStringify(journal)}\n`)),
  )) fail('migration_journal_invalid');
  rmSync(trustPaths.migrationJournalPath, { force: true });
}

function currentMigrationStateMatches(trustPaths, state, expectedRootUID, expectedSourceUID, algorithm) {
  if (!strictRootDataFile(
    trustPaths.publicKeyPath,
    expectedRootUID,
    state.public_key_sha256,
    { publicKey: algorithm === 'ed25519' ? 'ed25519' : 'p256' },
  ) || !strictRootDataFile(trustPaths.anchorPath, expectedRootUID, state.anchor_sha256) ||
      !exactPrivateFile(trustPaths.registryPath, expectedSourceUID, state.registry_sha256)) return false;
  try {
    const key = readFileSync(trustPaths.publicKeyPath, 'utf8');
    const registry = readFileSync(trustPaths.registryPath);
    const verified = exactSignedRegistry(registry, key, algorithm);
    exactRegistryAnchor(readFileSync(trustPaths.anchorPath), registry, verified.envelope.payload.epoch);
    return verified.envelope.payload.epoch === state.epoch;
  } catch {
    return false;
  }
}

function recoverBootstrapTrustMigration({
  trustPaths, run, expectedRootUID, expectedSourceUID, forceRollback = false,
}) {
  if (!existsSync(trustPaths.migrationJournalPath)) return { recovered: false };
  const journal = exactMigrationJournal(trustPaths, expectedSourceUID);
  const backups = migrationBackupPaths(trustPaths, journal);
  const oldCurrent = currentMigrationStateMatches(
    trustPaths, journal.old, expectedRootUID, expectedSourceUID, 'ed25519',
  );
  const newCurrent = currentMigrationStateMatches(
    trustPaths, journal.new, expectedRootUID, expectedSourceUID, 'es256',
  );
  if (!forceRollback && newCurrent) {
    cleanupMigrationArtifacts({
      trustPaths, journal, backups, run, expectedRootUID, expectedSourceUID,
    });
    return { recovered: true, state: 'completed' };
  }
  if (!oldCurrent) {
    if (!strictRootDataFile(
      backups.publicKey, expectedRootUID, journal.old.public_key_sha256, { publicKey: 'ed25519' },
    ) || !strictRootDataFile(backups.anchor, expectedRootUID, journal.old.anchor_sha256) ||
        !exactPrivateFile(backups.registry, expectedSourceUID, journal.old.registry_sha256)) {
      fail('migration_recovery_failed');
    }
    atomicReplaceRootData(
      run, backups.publicKey, trustPaths.publicKeyPath, expectedRootUID,
      journal.old.public_key_sha256, { publicKey: 'ed25519' },
    );
    atomicReplaceRootData(
      run, backups.anchor, trustPaths.anchorPath, expectedRootUID, journal.old.anchor_sha256,
    );
    atomicReplacePrivateData(trustPaths.registryPath, readFileSync(backups.registry));
  }
  if (!currentMigrationStateMatches(
    trustPaths, journal.old, expectedRootUID, expectedSourceUID, 'ed25519',
  )) fail('migration_recovery_failed');
  cleanupMigrationArtifacts({
    trustPaths, journal, backups, run, expectedRootUID, expectedSourceUID,
  });
  return { recovered: true, state: 'rolled_back' };
}

function helperRegistryProof(helperPath, payloadBytes, helperPublicKey, run) {
  const directory = mkdtempSync(join(tmpdir(), 'pulse-binding-sign-'));
  const path = join(directory, 'payload.json');
  try {
    writeFileSync(path, payloadBytes, { mode: 0o600, flag: 'wx' });
    const result = run(helperPath, ['sign-binding-registry', '--payload', path], {
      timeout: 180_000, stdio: ['ignore', 'pipe', 'pipe'],
    });
    if (!exactResult(result) || typeof result.stdout !== 'string' || result.stdout.length > 4096) {
      fail('migration_presence_denied');
    }
    let proof;
    try { proof = JSON.parse(result.stdout); } catch { fail('migration_presence_invalid'); }
    const publicDER = publicKeyDER(helperPublicKey);
    if (!proof || Array.isArray(proof) || typeof proof !== 'object' ||
        Object.keys(proof).sort().join('\0') !== 'algorithm\0key_id\0signature' ||
        proof.algorithm !== 'es256' || !publicDER ||
        proof.key_id !== sha256Bytes(publicDER) || typeof proof.signature !== 'string') {
      fail('migration_presence_invalid');
    }
    return proof;
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
}

async function migrateBootstrapTrust({
  trustPaths, helperPublicKey, stagedPublicKey, run, expectedRootUID, expectedSourceUID,
  onMigrationPhase,
}) {
  const legacy = legacyBootstrapState(trustPaths, expectedRootUID, expectedSourceUID);
  if (!legacy) fail('bootstrap_migration_invalid');
  const payload = { ...legacy.payload, epoch: legacy.epoch + 1 };
  const payloadBytes = Buffer.from(canonicalJSONStringify(payload));
  const proof = helperRegistryProof(trustPaths.helperPath, payloadBytes, helperPublicKey, run);
  const registryBytes = Buffer.from(`${JSON.stringify({
    algorithm: 'es256', payload, signature: proof.signature,
  })}\n`);
  const anchorBytes = Buffer.from(`${canonicalJSONStringify(
    bindingRegistryAnchor(registryBytes, payload.epoch),
  )}\n`);
  exactSignedRegistry(registryBytes, helperPublicKey, 'es256');
  exactRegistryAnchor(anchorBytes, registryBytes, payload.epoch);

  const journal = {
    schema: BOOTSTRAP_MIGRATION_SCHEMA,
    old: {
      anchor_sha256: sha256Bytes(legacy.anchorBytes),
      epoch: legacy.epoch,
      public_key_sha256: sha256Bytes(legacy.publicKeyBytes),
      registry_sha256: sha256Bytes(legacy.registryBytes),
    },
    new: {
      anchor_sha256: sha256Bytes(anchorBytes),
      epoch: payload.epoch,
      public_key_sha256: sha256File(stagedPublicKey),
      registry_sha256: sha256Bytes(registryBytes),
    },
  };
  const journalBytes = Buffer.from(`${canonicalJSONStringify(journal)}\n`);
  const backups = migrationBackupPaths(trustPaths, journal);
  if (existsSync(trustPaths.migrationJournalPath)) fail('migration_journal_exists');
  writeFileSync(trustPaths.migrationJournalPath, journalBytes, { flag: 'wx', mode: 0o600 });
  chmodSync(trustPaths.migrationJournalPath, 0o600);
  try {
    if (!existsSync(backups.publicKey)) {
      sudo(run, [INSTALL, '-o', 'root', '-g', 'wheel', '-m', '0644', trustPaths.publicKeyPath, backups.publicKey], 'migration_backup_failed');
    }
    if (!existsSync(backups.anchor)) {
      sudo(run, [INSTALL, '-o', 'root', '-g', 'wheel', '-m', '0644', trustPaths.anchorPath, backups.anchor], 'migration_backup_failed');
    }
    if (!existsSync(backups.registry)) {
      writeFileSync(backups.registry, legacy.registryBytes, { flag: 'wx', mode: 0o600 });
      chmodSync(backups.registry, 0o600);
    }
    if (!strictRootDataFile(
      backups.publicKey, expectedRootUID, journal.old.public_key_sha256, { publicKey: 'ed25519' },
    ) || !strictRootDataFile(backups.anchor, expectedRootUID, journal.old.anchor_sha256) ||
        !exactPrivateFile(backups.registry, expectedSourceUID, journal.old.registry_sha256)) {
      fail('migration_backup_failed');
    }

    const candidateAnchor = join(dirname(stagedPublicKey), 'workspace-bindings.anchor.json');
    writeFileSync(candidateAnchor, anchorBytes, { flag: 'wx', mode: 0o600 });
    atomicReplaceRootData(
      run, stagedPublicKey, trustPaths.publicKeyPath, expectedRootUID,
      journal.new.public_key_sha256, { publicKey: 'p256' },
    );
    await onMigrationPhase('public_key_replaced');
    atomicReplaceRootData(
      run, candidateAnchor, trustPaths.anchorPath, expectedRootUID, journal.new.anchor_sha256,
    );
    await onMigrationPhase('anchor_replaced');
    atomicReplacePrivateData(trustPaths.registryPath, registryBytes);
    await onMigrationPhase('registry_replaced');
    if (!currentMigrationStateMatches(
      trustPaths, journal.new, expectedRootUID, expectedSourceUID, 'es256',
    )) fail('migration_postinstall_verification_failed');
    cleanupMigrationArtifacts({
      trustPaths, journal, backups, run, expectedRootUID, expectedSourceUID,
    });
    return { epoch: payload.epoch, migrated: true };
  } catch (error) {
    try {
      recoverBootstrapTrustMigration({
        trustPaths, run, expectedRootUID, expectedSourceUID, forceRollback: true,
      });
    } catch (recoveryError) {
      const wrapped = new Error(`trust_migration_rollback_failed:${error.message}:${recoveryError.message}`);
      wrapped.cause = error;
      wrapped.recovery_error = recoveryError;
      throw wrapped;
    }
    throw error;
  }
}

function atomicReplaceHelper(run, source, target, expectedRootUID, expectedHash) {
  const stagedTarget = join(dirname(target), `.${EXPECTED_HELPER_IDENTIFIER}.new-${expectedHash.slice(0, 16)}`);
  try {
    sudo(run, [INSTALL, '-o', 'root', '-g', 'wheel', '-m', '0755', source, stagedTarget], 'helper_stage_failed');
    if (!strictInstalledHelper(stagedTarget, run, expectedRootUID, expectedHash)) {
      fail('helper_stage_verification_failed');
    }
    sudo(run, [MV, '-f', stagedTarget, target], 'helper_replace_failed');
  } finally {
    if (existsSync(stagedTarget)) {
      try { sudo(run, [RM, '-f', stagedTarget], 'helper_stage_cleanup_failed'); } catch { /* primary failure wins */ }
    }
  }
  if (!strictInstalledHelper(target, run, expectedRootUID, expectedHash)) {
    fail('helper_install_verification_failed');
  }
}

function rollbackCreatedArtifacts(run, trustPaths, {
  helperCreated, publicKeyCreated, trustDirectoryCreated, helperHash, publicKeyHash,
}) {
  const errors = [];
  const removeExact = (path, expectedHash, label) => {
    if (!existsSync(path)) return;
    let exact = false;
    try {
      const link = lstatSync(path);
      exact = link.isFile() && !link.isSymbolicLink() && sha256File(path) === expectedHash;
    } catch {
      exact = false;
    }
    if (!exact) {
      errors.push(`${label}_changed`);
      return;
    }
    try {
      sudo(run, [RM, '-f', path], 'rollback_failed');
      if (existsSync(path)) errors.push(`${label}_remove_incomplete`);
    } catch {
      errors.push(`${label}_remove_failed`);
    }
  };

  if (publicKeyCreated) removeExact(trustPaths.publicKeyPath, publicKeyHash, 'public_key');
  if (helperCreated) removeExact(trustPaths.helperPath, helperHash, 'helper');
  if (trustDirectoryCreated && !existsSync(trustPaths.publicKeyPath)) {
    try {
      sudo(run, [RMDIR, dirname(trustPaths.publicKeyPath)], 'rollback_failed');
    } catch {
      errors.push('trust_directory_remove_failed');
    }
  }
  return errors;
}

function rollbackFailure(original, errors) {
  const error = new Error(`trust_rollback_failed:${original?.message ?? 'trust_install_failed'}:${errors.join(',')}`);
  error.cause = original;
  error.rollback_errors = errors;
  return error;
}

export async function installPresenceTrust({
  confirmation,
  paths,
  run = defaultRun,
  expectedRootUID = 0,
  expectedSourceUID = typeof process.getuid === 'function' ? process.getuid() : 0,
  tempRoot = tmpdir(),
  acquireLock = defaultAcquireInstallLock,
  onMigrationPhase = () => {},
} = {}) {
  if (confirmation !== INSTALL_CONFIRMATION) fail('confirmation_required');
  const trustPaths = exactPaths(paths);
  if (typeof run !== 'function' || !Number.isInteger(expectedRootUID) || expectedRootUID < 0 ||
      !Number.isInteger(expectedSourceUID) || expectedSourceUID < 0 || typeof tempRoot !== 'string' ||
      typeof acquireLock !== 'function' || typeof onMigrationPhase !== 'function') {
    fail('request_invalid');
  }

  assertVendoredHelper(trustPaths.vendoredHelperPath, run, expectedSourceUID);

  const temporaryDirectory = mkdtempSync(join(tempRoot, 'pulse-presence-install-'));
  const stagedHelper = join(temporaryDirectory, EXPECTED_HELPER_IDENTIFIER);
  const stagedPublicKey = join(temporaryDirectory, 'workspace-bindings.pub.pem');
  let lock = null;
  let operationError = null;
  let result = null;

  try {
    copyFileSync(trustPaths.vendoredHelperPath, stagedHelper);
    chmodSync(stagedHelper, 0o755);
    if (sha256File(stagedHelper) !== sha256File(trustPaths.vendoredHelperPath)) fail('vendored_copy_mismatch');
    assertVendoredHelper(stagedHelper, run, expectedSourceUID);
    const helperHash = sha256File(stagedHelper);

    // The lock holder needs its stdin pipe as a private release channel, so it
    // cannot also collect an administrator password. Authenticate once through
    // the caller's real terminal before starting the non-interactive holder.
    // Exact already-ready state remains idempotent and never prompts.
    const preflight = inspectPresenceTrust({ paths: trustPaths, run, expectedRootUID });
    if (preflight.helper.exists && !preflight.public_key.exists && !strictInstalledHelper(
      trustPaths.helperPath, run, expectedRootUID, helperHash,
    )) fail('existing_invalid');
    if (!existsSync(trustPaths.migrationJournalPath) && preflight.helper.exists && preflight.public_key.exists) {
      const verifiedPreflight = inspectPresenceTrust({
        paths: trustPaths, run, expectedRootUID, probePublicKey: true,
      });
      if (verifiedPreflight.ready && strictInstalledHelper(
        trustPaths.helperPath, run, expectedRootUID, helperHash,
      )) {
        result = { schema: 'pulse.presence_trust_install_result.v1', installed: false, status: verifiedPreflight };
        return result;
      }
    }
    authorizeSudo(run);

    lock = await acquireLock({ timeout: 300_000 });
    if (!lock || typeof lock.release !== 'function') fail('install_lock_invalid');

    recoverBootstrapTrustMigration({
      trustPaths, run, expectedRootUID, expectedSourceUID,
    });

    const existing = inspectPresenceTrust({ paths: trustPaths, run, expectedRootUID });
    if (existing.helper.exists && existing.public_key.exists) {
      const verifiedExisting = inspectPresenceTrust({
        paths: trustPaths, run, expectedRootUID, probePublicKey: true,
      });
      if (verifiedExisting.ready && strictInstalledHelper(
        trustPaths.helperPath, run, expectedRootUID, helperHash,
      )) {
        result = { schema: 'pulse.presence_trust_install_result.v1', installed: false, status: verifiedExisting };
        return result;
      }
    }

    const helperPublicKey = readHelperPublicKey(stagedHelper, run);
    if (!helperPublicKey) fail('helper_public_key_failed');
    writeFileSync(stagedPublicKey, helperPublicKey, { mode: 0o600, flag: 'wx' });
    chmodSync(stagedPublicKey, 0o600);
    const publicKeyHash = sha256File(stagedPublicKey);

    const helperExisted = existing.helper.exists;
    const publicKeyExisted = existing.public_key.exists;
    const exactCurrentHelper = helperExisted && strictInstalledHelper(
      trustPaths.helperPath, run, expectedRootUID, helperHash,
    );
    const bootstrapMigration = Boolean(
      publicKeyExisted && readBootstrapPublicKey(trustPaths.publicKeyPath).der &&
      legacyBootstrapState(trustPaths, expectedRootUID, expectedSourceUID) &&
      (!helperExisted || exactCurrentHelper),
    );
    const legacyTrust = helperExisted && publicKeyExisted
      ? inspectPresenceTrust({
        paths: trustPaths, run, expectedRootUID, probePublicKey: true, probeCapabilities: false,
      })
      : null;
    const installedPublicKey = publicKeyExisted ? readPublicKey(trustPaths.publicKeyPath).value : null;
    const helperUpgrade = Boolean(
      helperExisted && publicKeyExisted && !exactCurrentHelper && legacyTrust?.ready &&
      samePublicKey(installedPublicKey, helperPublicKey),
    );
    if ((helperExisted && !exactCurrentHelper && !helperUpgrade) ||
        (publicKeyExisted && !bootstrapMigration &&
          !strictInstalledPublicKey(trustPaths.publicKeyPath, expectedRootUID, publicKeyHash))) {
      fail('existing_invalid');
    }

    const previousHelper = helperUpgrade ? join(temporaryDirectory, 'previous-helper') : null;
    const previousHelperHash = helperUpgrade ? sha256File(trustPaths.helperPath) : null;
    if (helperUpgrade) {
      copyFileSync(trustPaths.helperPath, previousHelper);
      chmodSync(previousHelper, 0o755);
      if (sha256File(previousHelper) !== previousHelperHash) fail('existing_invalid');
    }

    let helperCreated = false;
    let helperReplaced = false;
    let publicKeyCreated = false;
    let trustDirectoryCreated = false;
    let bootstrapMigrated = false;
    try {
      if (!helperExisted) {
        sudo(run, [MKDIR, '-p', dirname(trustPaths.helperPath)], 'helper_directory_install_failed');
        helperCreated = true;
        sudo(run, [INSTALL, '-o', 'root', '-g', 'wheel', '-m', '0755', stagedHelper, trustPaths.helperPath], 'helper_install_failed');
      } else if (helperUpgrade) {
        helperReplaced = true;
        atomicReplaceHelper(run, stagedHelper, trustPaths.helperPath, expectedRootUID, helperHash);
      }
      if (!strictInstalledHelper(trustPaths.helperPath, run, expectedRootUID, helperHash)) {
        fail('helper_install_verification_failed');
      }

      if (bootstrapMigration) {
        await migrateBootstrapTrust({
          trustPaths, helperPublicKey, stagedPublicKey, run, expectedRootUID, expectedSourceUID,
          onMigrationPhase,
        });
        bootstrapMigrated = true;
      } else if (!publicKeyExisted) {
        const trustDirectoryExisted = existsSync(dirname(trustPaths.publicKeyPath));
        trustDirectoryCreated = !trustDirectoryExisted;
        sudo(run, [MKDIR, '-p', dirname(trustPaths.publicKeyPath)], 'public_key_directory_install_failed');
        publicKeyCreated = true;
        sudo(run, [INSTALL, '-o', 'root', '-g', 'wheel', '-m', '0644', stagedPublicKey, trustPaths.publicKeyPath], 'public_key_install_failed');
      }

      const status = inspectPresenceTrust({
        paths: trustPaths, run, expectedRootUID, expectedPublicKey: helperPublicKey,
      });
      if (!status.ready || !strictInstalledHelper(trustPaths.helperPath, run, expectedRootUID, helperHash) ||
          !strictInstalledPublicKey(trustPaths.publicKeyPath, expectedRootUID, publicKeyHash)) {
        fail('postinstall_verification_failed');
      }
      result = {
        schema: 'pulse.presence_trust_install_result.v1',
        installed: true,
        ...(bootstrapMigrated ? { migrated_bootstrap: true } : {}),
        status,
      };
      return result;
    } catch (error) {
      // Once the signed registry, root key, and anti-rollback anchor have all
      // committed, removing the helper would make the coherent new authority
      // unusable. A retry can re-verify this exact committed generation.
      if (bootstrapMigrated) throw error;
      const replacementErrors = [];
      if (helperReplaced) {
        try {
          atomicReplaceHelper(
            run, previousHelper, trustPaths.helperPath, expectedRootUID, previousHelperHash,
          );
        } catch {
          replacementErrors.push('helper_restore_failed');
        }
      }
      const rollbackErrors = rollbackCreatedArtifacts(run, trustPaths, {
        helperCreated, publicKeyCreated, trustDirectoryCreated, helperHash, publicKeyHash,
      });
      rollbackErrors.push(...replacementErrors);
      if (rollbackErrors.length > 0) throw rollbackFailure(error, rollbackErrors);
      throw error;
    }
  } catch (error) {
    operationError = error;
    throw error;
  } finally {
    let releaseError = null;
    if (lock) {
      try {
        await lock.release();
      } catch (error) {
        releaseError = error;
        if (operationError) operationError.lock_release_error = error;
      }
    }
    rmSync(temporaryDirectory, { recursive: true, force: true });
    if (releaseError && !operationError) throw releaseError;
  }
}
