import { spawn, spawnSync } from 'node:child_process';
import { createHash, createPublicKey } from 'node:crypto';
import {
  chmodSync, copyFileSync, existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

export const INSTALL_CONFIRMATION = 'install pulse presence helper';
export const EXPECTED_HELPER_IDENTIFIER = 'gg.zbs.pulse.presence-helper';
export const EXPECTED_HELPER_TEAM_IDENTIFIER = '44N4NZ86S5';
export const EXPECTED_HELPER_CONTRACT_VERSION = 2;
export const EXPECTED_HELPER_CAPABILITIES = Object.freeze([
  'dpop-create', 'dpop-delete', 'dpop-proof', 'dpop-public',
  'prove', 'public-key', 'self-test', 'sign-binding-registry',
]);

export const DEFAULT_TRUST_PATHS = Object.freeze({
  helperPath: '/Library/PrivilegedHelperTools/gg.zbs.pulse.presence-helper',
  publicKeyPath: '/Library/Application Support/Pulse/trust/workspace-bindings.pub.pem',
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

function fail(code) {
  throw new Error(`trust_${code}`);
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
  for (const key of ['helperPath', 'publicKeyPath', 'vendoredHelperPath']) {
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
        Object.keys(value).sort().join('\0') !== 'capabilities\0schema\0version' ||
        value.schema !== 'pulse.presence_helper.contract.v1' ||
        value.version !== EXPECTED_HELPER_CONTRACT_VERSION ||
        !Array.isArray(value.capabilities) ||
        value.capabilities.join('\0') !== EXPECTED_HELPER_CAPABILITIES.join('\0')) return null;
    return Object.freeze({ version: value.version, capabilities: Object.freeze([...value.capabilities]) });
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
    return value?.schema === 'pulse.presence_helper.self_test.v1' &&
      value.status === 'pass' && value.vectors === 13 &&
      Object.keys(value).sort().join('\0') === 'schema\0status\0vectors';
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
  if (publicKeyFile.exists && !installedKey.der) issues.push('public_key_invalid');

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

function defaultAcquireInstallLock({ timeout = 300_000 } = {}) {
  return new Promise((resolve, reject) => {
    const timeoutSeconds = Math.max(1, Math.ceil(timeout / 1000));
    const child = spawn(SUDO, [
      LOCKF, '-k', '-t', String(timeoutSeconds), INSTALL_LOCK_PATH,
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
} = {}) {
  if (confirmation !== INSTALL_CONFIRMATION) fail('confirmation_required');
  const trustPaths = exactPaths(paths);
  if (typeof run !== 'function' || !Number.isInteger(expectedRootUID) || expectedRootUID < 0 ||
      !Number.isInteger(expectedSourceUID) || expectedSourceUID < 0 || typeof tempRoot !== 'string' ||
      typeof acquireLock !== 'function') {
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

    lock = await acquireLock({ timeout: 300_000 });
    if (!lock || typeof lock.release !== 'function') fail('install_lock_invalid');

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
        (publicKeyExisted && !strictInstalledPublicKey(trustPaths.publicKeyPath, expectedRootUID, publicKeyHash))) {
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

      if (!publicKeyExisted) {
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
      result = { schema: 'pulse.presence_trust_install_result.v1', installed: true, status };
      return result;
    } catch (error) {
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
