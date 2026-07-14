import assert from 'node:assert/strict';
import { generateKeyPairSync } from 'node:crypto';
import {
  chmodSync, copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import test from 'node:test';

import {
  INSTALL_CONFIRMATION,
  inspectPresenceTrust,
  installPresenceTrust,
} from './trust-helper.js';

const EXPECTED_IDENTIFIER = 'gg.zbs.pulse.presence-helper';
const EXPECTED_TEAM = '44N4NZ86S5';

function keyPair() {
  const pair = generateKeyPairSync('ec', { namedCurve: 'prime256v1' });
  return {
    publicKey: pair.publicKey.export({ type: 'spki', format: 'pem' }),
    otherPublicKey: generateKeyPairSync('ec', { namedCurve: 'prime256v1' })
      .publicKey.export({ type: 'spki', format: 'pem' }),
  };
}

function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'pulse-trust-helper-'));
  const paths = {
    helperPath: join(root, 'Library', 'PrivilegedHelperTools', EXPECTED_IDENTIFIER),
    publicKeyPath: join(root, 'Library', 'Application Support', 'Pulse', 'trust', 'workspace-bindings.pub.pem'),
    vendoredHelperPath: join(root, 'package', 'vendor', 'pulse-presence-helper', EXPECTED_IDENTIFIER),
  };
  const keys = keyPair();
  const calls = [];
  const signatureByPath = new Map();
  let failPublicKeyInstall = false;
  let raceHelperOnDirectoryFailure = false;
  let failInstalledHelperVerification = false;
  let failPostinstallVerification = false;
  let failHelperRollback = false;
  let failPublicKeyRollback = false;
  let failHelperReplaceAfterMove = false;

  function seedHelper(path = paths.helperPath, identity = { identifier: EXPECTED_IDENTIFIER, team: EXPECTED_TEAM }) {
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, 'signed-helper-binary');
    chmodSync(path, 0o755);
    signatureByPath.set(path, identity);
  }

  function seedPublicKey(pem = keys.publicKey) {
    mkdirSync(dirname(paths.publicKeyPath), { recursive: true });
    writeFileSync(paths.publicKeyPath, pem, { mode: 0o644 });
    chmodSync(paths.publicKeyPath, 0o644);
  }

  function result(status = 0, stdout = '', stderr = '') {
    return { status, stdout, stderr, signal: null, error: undefined };
  }

  function run(command, args, options = {}) {
    calls.push({ command, args: [...args], options });
    if (command === '/usr/bin/codesign') {
      const target = args.at(-1);
      let identity = signatureByPath.get(target);
      if (!identity && existsSync(target) && readFileSync(target, 'utf8') === 'signed-helper-binary') {
        identity = { identifier: EXPECTED_IDENTIFIER, team: EXPECTED_TEAM };
      }
      if (!identity) return result(1, '', 'code object is not signed at all');
      if (args.includes('--verify')) {
        if (target === paths.helperPath && failInstalledHelperVerification) return result(1, '', 'simulated verify failure');
        if (target === paths.helperPath && failPostinstallVerification && existsSync(paths.publicKeyPath)) {
          return result(1, '', 'simulated postinstall verify failure');
        }
        return result(0);
      }
      return result(0, '', `Identifier=${identity.identifier}\nTeamIdentifier=${identity.team}\n`);
    }
    if (command.endsWith(EXPECTED_IDENTIFIER) && args[0] === 'public-key') {
      return result(0, keys.publicKey, '');
    }
    if (command.endsWith(EXPECTED_IDENTIFIER) && args[0] === 'contract') {
      return result(0, `${JSON.stringify({
        schema: 'pulse.presence_helper.contract.v1',
        version: 2,
        capabilities: [
          'dpop-create', 'dpop-delete', 'dpop-proof', 'dpop-public',
          'prove', 'public-key', 'self-test', 'sign-binding-registry',
        ],
      })}\n`, '');
    }
    if (command.endsWith(EXPECTED_IDENTIFIER) && args[0] === 'self-test') {
      return result(0, '{"schema":"pulse.presence_helper.self_test.v1","status":"pass","vectors":13}\n', '');
    }
    if (command === '/usr/bin/sudo') {
      const [program, ...sudoArgs] = args;
      if (program === '/bin/mkdir') {
        if (raceHelperOnDirectoryFailure && sudoArgs.at(-1) === dirname(paths.helperPath)) {
          seedHelper();
          return result(1, '', 'simulated mkdir failure');
        }
        mkdirSync(sudoArgs.at(-1), { recursive: true });
        return result(0);
      }
      if (program === '/usr/bin/install') {
        const source = sudoArgs.at(-2);
        const target = sudoArgs.at(-1);
        if (target === paths.publicKeyPath && failPublicKeyInstall) return result(1, '', 'simulated install failure');
        mkdirSync(dirname(target), { recursive: true });
        copyFileSync(source, target);
        chmodSync(target, sudoArgs.includes('0755') ? 0o755 : 0o644);
        if (signatureByPath.has(source)) {
          signatureByPath.set(target, signatureByPath.get(source));
        } else if (['signed-helper-binary', 'old-signed-helper-binary'].includes(readFileSync(source, 'utf8'))) {
          signatureByPath.set(target, { identifier: EXPECTED_IDENTIFIER, team: EXPECTED_TEAM });
        }
        return result(0);
      }
      if (program === '/bin/mv') {
        const source = sudoArgs.at(-2);
        const target = sudoArgs.at(-1);
        mkdirSync(dirname(target), { recursive: true });
        const identity = signatureByPath.get(source);
        copyFileSync(source, target);
        rmSync(source, { force: true });
        signatureByPath.delete(source);
        if (identity) signatureByPath.set(target, identity);
        if (target === paths.helperPath && failHelperReplaceAfterMove) {
          failHelperReplaceAfterMove = false;
          return result(1, '', 'simulated replace acknowledgement loss');
        }
        return result(0);
      }
      if (program === '/bin/rm') {
        for (const target of sudoArgs.filter((value) => value.startsWith(root))) {
          if (target === paths.helperPath && failHelperRollback) return result(1, '', 'simulated rollback failure');
          if (target === paths.publicKeyPath && failPublicKeyRollback) return result(1, '', 'simulated rollback failure');
          rmSync(target, { force: true, recursive: false });
          signatureByPath.delete(target);
        }
        return result(0);
      }
      if (program === '/bin/rmdir') {
        rmSync(sudoArgs.at(-1), { recursive: false, force: true });
        return result(0);
      }
    }
    return result(1, '', `unexpected command: ${command}`);
  }

  return {
    root, paths, keys, calls, run, signatureByPath, seedHelper, seedPublicKey,
    setFailPublicKeyInstall(value) { failPublicKeyInstall = value; },
    setRaceHelperOnDirectoryFailure(value) { raceHelperOnDirectoryFailure = value; },
    setFailInstalledHelperVerification(value) { failInstalledHelperVerification = value; },
    setFailPostinstallVerification(value) { failPostinstallVerification = value; },
    setFailHelperRollback(value) { failHelperRollback = value; },
    setFailPublicKeyRollback(value) { failPublicKeyRollback = value; },
    setFailHelperReplaceAfterMove(value) { failHelperReplaceAfterMove = value; },
    options: {
      paths, run, expectedRootUID: process.getuid(), tempRoot: root,
      acquireLock: async () => ({ release: async () => {} }),
    },
    cleanup() { rmSync(root, { recursive: true, force: true }); },
  };
}

test('trust status reports both fixed trust artifacts missing without invoking a process', () => {
  const f = fixture();
  try {
    const status = inspectPresenceTrust(f.options);
    assert.equal(status.schema, 'pulse.presence_trust_status.v1');
    assert.equal(status.status, 'not_installed');
    assert.equal(status.ready, false);
    assert.deepEqual(status.issues, ['helper_missing', 'public_key_missing']);
    assert.equal(status.helper.path, f.paths.helperPath);
    assert.equal(status.public_key.path, f.paths.publicKeyPath);
    assert.equal(status.public_key.matches_helper, null);
    assert.deepEqual(f.calls, []);
  } finally { f.cleanup(); }
});

test('deep trust status verifies root ownership, restrictive modes, code identity, and helper public-key match', () => {
  const f = fixture();
  try {
    f.seedHelper();
    f.seedPublicKey();
    const status = inspectPresenceTrust({ ...f.options, probePublicKey: true });
    assert.equal(status.status, 'ready');
    assert.equal(status.ready, true);
    assert.deepEqual(status.issues, []);
    assert.deepEqual(status.helper.code_signature, {
      valid: true, identifier: EXPECTED_IDENTIFIER, team_identifier: EXPECTED_TEAM,
    });
    assert.equal(status.helper.root_owned, true);
    assert.equal(status.helper.mode, '0755');
    assert.equal(status.public_key.root_owned, true);
    assert.equal(status.public_key.mode, '0644');
    assert.equal(status.public_key.valid, true);
    assert.equal(status.public_key.matches_helper, true);
  } finally { f.cleanup(); }
});

test('trust status rejects the wrong signed identity before asking helper for a public key', () => {
  const f = fixture();
  try {
    f.seedHelper(f.paths.helperPath, { identifier: 'evil.helper', team: EXPECTED_TEAM });
    f.seedPublicKey();
    const status = inspectPresenceTrust({ ...f.options, probePublicKey: true });
    assert.equal(status.status, 'invalid');
    assert.equal(status.ready, false);
    assert.ok(status.issues.includes('helper_code_identity_invalid'));
    assert.equal(f.calls.some((call) => call.command === f.paths.helperPath), false);
  } finally { f.cleanup(); }
});

test('deep trust status rejects a valid but different public key', () => {
  const f = fixture();
  try {
    f.seedHelper();
    f.seedPublicKey(f.keys.otherPublicKey);
    const status = inspectPresenceTrust({ ...f.options, probePublicKey: true });
    assert.equal(status.status, 'invalid');
    assert.equal(status.ready, false);
    assert.ok(status.issues.includes('public_key_mismatch'));
    assert.equal(status.public_key.matches_helper, false);
  } finally { f.cleanup(); }
});

test('install refuses anything except the exact confirmation phrase before validation or sudo', async () => {
  const f = fixture();
  try {
    await assert.rejects(
      installPresenceTrust({ ...f.options, confirmation: 'yes' }),
      /trust_confirmation_required/,
    );
    assert.deepEqual(f.calls, []);
  } finally { f.cleanup(); }
});

test('install rejects an unsigned vendored helper before the first sudo command', async () => {
  const f = fixture();
  try {
    mkdirSync(dirname(f.paths.vendoredHelperPath), { recursive: true });
    writeFileSync(f.paths.vendoredHelperPath, 'unsigned', { mode: 0o755 });
    chmodSync(f.paths.vendoredHelperPath, 0o755);
    await assert.rejects(
      installPresenceTrust({ ...f.options, confirmation: INSTALL_CONFIRMATION }),
      /trust_vendored_signature_invalid/,
    );
    assert.equal(f.calls.some((call) => call.command === '/usr/bin/sudo'), false);
  } finally { f.cleanup(); }
});

test('install validates a fixed vendored helper, installs root artifacts, invokes human-presence public-key, and returns deep-ready status', async () => {
  const f = fixture();
  try {
    f.seedHelper(f.paths.vendoredHelperPath);
    const result = await installPresenceTrust({ ...f.options, confirmation: INSTALL_CONFIRMATION });
    assert.equal(result.installed, true);
    assert.equal(result.status.ready, true);
    assert.equal(readFileSync(f.paths.publicKeyPath, 'utf8'), f.keys.publicKey);

    const firstSudo = f.calls.findIndex((call) => call.command === '/usr/bin/sudo');
    const sourceCodeChecks = f.calls
      .slice(0, firstSudo)
      .filter((call) => call.command === '/usr/bin/codesign' && call.args.at(-1) === f.paths.vendoredHelperPath);
    assert.equal(sourceCodeChecks.length, 2);

    const helperInstall = f.calls.find((call) => call.command === '/usr/bin/sudo' &&
      call.args[0] === '/usr/bin/install' && call.args.at(-1) === f.paths.helperPath);
    assert.deepEqual(helperInstall.args.slice(0, 7), ['/usr/bin/install', '-o', 'root', '-g', 'wheel', '-m', '0755']);
    const presenceCall = f.calls.find((call) =>
      call.command.endsWith(EXPECTED_IDENTIFIER) && call.args[0] === 'public-key');
    assert.deepEqual(presenceCall.args, ['public-key']);
    const keyInstall = f.calls.find((call) => call.command === '/usr/bin/sudo' &&
      call.args[0] === '/usr/bin/install' && call.args.at(-1) === f.paths.publicKeyPath);
    assert.deepEqual(keyInstall.args.slice(0, 7), ['/usr/bin/install', '-o', 'root', '-g', 'wheel', '-m', '0644']);
  } finally { f.cleanup(); }
});

test('fresh install removes the new helper when public-key installation fails', async () => {
  const f = fixture();
  try {
    f.seedHelper(f.paths.vendoredHelperPath);
    f.setFailPublicKeyInstall(true);
    await assert.rejects(
      installPresenceTrust({ ...f.options, confirmation: INSTALL_CONFIRMATION }),
      /trust_public_key_install_failed/,
    );
    assert.equal(existsSync(f.paths.helperPath), false);
    assert.equal(existsSync(f.paths.publicKeyPath), false);
    assert.ok(f.calls.some((call) => call.command === '/usr/bin/sudo' &&
      call.args[0] === '/bin/rm' && call.args.includes(f.paths.helperPath)));
  } finally { f.cleanup(); }
});

test('failure after helper installation rolls back only the exact newly installed helper', async () => {
  const f = fixture();
  try {
    f.seedHelper(f.paths.vendoredHelperPath);
    f.setFailInstalledHelperVerification(true);
    await assert.rejects(
      installPresenceTrust({ ...f.options, confirmation: INSTALL_CONFIRMATION }),
      /trust_helper_install_verification_failed/,
    );
    assert.equal(existsSync(f.paths.helperPath), false);
    assert.equal(existsSync(f.paths.publicKeyPath), false);
  } finally { f.cleanup(); }
});

test('failure during postinstall verification rolls both exact staged artifacts back to not-installed', async () => {
  const f = fixture();
  try {
    f.seedHelper(f.paths.vendoredHelperPath);
    f.setFailPostinstallVerification(true);
    await assert.rejects(
      installPresenceTrust({ ...f.options, confirmation: INSTALL_CONFIRMATION }),
      /trust_postinstall_verification_failed/,
    );
    assert.equal(existsSync(f.paths.helperPath), false);
    assert.equal(existsSync(f.paths.publicKeyPath), false);
    assert.equal(inspectPresenceTrust(f.options).status, 'not_installed');
  } finally { f.cleanup(); }
});

test('a rollback failure is explicit and the next confirmed install repairs the hash-bound partial state', async () => {
  const f = fixture();
  try {
    f.seedHelper(f.paths.vendoredHelperPath);
    f.setFailInstalledHelperVerification(true);
    f.setFailHelperRollback(true);
    await assert.rejects(
      installPresenceTrust({ ...f.options, confirmation: INSTALL_CONFIRMATION }),
      (error) => {
        assert.match(error.message, /^trust_rollback_failed:trust_helper_install_verification_failed:/);
        assert.deepEqual(error.rollback_errors, ['helper_remove_failed']);
        assert.match(error.cause.message, /trust_helper_install_verification_failed/);
        return true;
      },
    );
    assert.equal(existsSync(f.paths.helperPath), true);
    assert.equal(existsSync(f.paths.publicKeyPath), false);

    f.setFailInstalledHelperVerification(false);
    f.setFailHelperRollback(false);
    const repaired = await installPresenceTrust({ ...f.options, confirmation: INSTALL_CONFIRMATION });
    assert.equal(repaired.installed, true);
    assert.equal(repaired.status.ready, true);
    const helperInstalls = f.calls.filter((call) => call.command === '/usr/bin/sudo' &&
      call.args[0] === '/usr/bin/install' && call.args.at(-1) === f.paths.helperPath);
    assert.equal(helperInstalls.length, 1);
  } finally { f.cleanup(); }
});

test('an exact helper-only partial install is repaired without replacing the helper', async () => {
  const f = fixture();
  try {
    f.seedHelper(f.paths.vendoredHelperPath);
    f.seedHelper();
    const repaired = await installPresenceTrust({ ...f.options, confirmation: INSTALL_CONFIRMATION });
    assert.equal(repaired.installed, true);
    assert.equal(repaired.status.ready, true);
    assert.equal(f.calls.some((call) => call.command === '/usr/bin/sudo' &&
      call.args[0] === '/usr/bin/install' && call.args.at(-1) === f.paths.helperPath), false);
  } finally { f.cleanup(); }
});

test('a hash-bound public-key-only rollback remainder repairs without replacing the public key', async () => {
  const f = fixture();
  try {
    f.seedHelper(f.paths.vendoredHelperPath);
    f.setFailPostinstallVerification(true);
    f.setFailPublicKeyRollback(true);
    await assert.rejects(
      installPresenceTrust({ ...f.options, confirmation: INSTALL_CONFIRMATION }),
      (error) => {
        assert.match(error.message, /^trust_rollback_failed:trust_postinstall_verification_failed:/);
        assert.deepEqual(error.rollback_errors, ['public_key_remove_failed']);
        return true;
      },
    );
    assert.equal(existsSync(f.paths.helperPath), false);
    assert.equal(existsSync(f.paths.publicKeyPath), true);

    f.setFailPostinstallVerification(false);
    f.setFailPublicKeyRollback(false);
    const repaired = await installPresenceTrust({ ...f.options, confirmation: INSTALL_CONFIRMATION });
    assert.equal(repaired.installed, true);
    assert.equal(repaired.status.ready, true);
    const keyInstalls = f.calls.filter((call) => call.command === '/usr/bin/sudo' &&
      call.args[0] === '/usr/bin/install' && call.args.at(-1) === f.paths.publicKeyPath);
    assert.equal(keyInstalls.length, 1);
  } finally { f.cleanup(); }
});

test('concurrent confirmed attempts serialize the full privileged mutation and install once', async () => {
  const f = fixture();
  let tail = Promise.resolve();
  let active = 0;
  let maxActive = 0;
  let acquisitions = 0;
  const acquireLock = async () => {
    let releaseNext;
    const previous = tail;
    tail = new Promise((resolve) => { releaseNext = resolve; });
    await previous;
    acquisitions += 1;
    active += 1;
    maxActive = Math.max(maxActive, active);
    return {
      async release() {
        active -= 1;
        releaseNext();
      },
    };
  };
  try {
    f.seedHelper(f.paths.vendoredHelperPath);
    const options = { ...f.options, acquireLock, confirmation: INSTALL_CONFIRMATION };
    const [first, second] = await Promise.all([
      installPresenceTrust(options),
      installPresenceTrust(options),
    ]);
    assert.deepEqual([first.installed, second.installed].sort(), [false, true]);
    assert.equal(acquisitions, 2);
    assert.equal(maxActive, 1);
    const installs = f.calls.filter((call) => call.command === '/usr/bin/sudo' &&
      call.args[0] === '/usr/bin/install');
    assert.equal(installs.length, 2);
  } finally { f.cleanup(); }
});

test('install refuses partial existing trust state without overwriting or sudo', async () => {
  const f = fixture();
  try {
    f.seedHelper(f.paths.vendoredHelperPath);
    f.seedHelper(f.paths.helperPath, { identifier: 'evil.helper', team: EXPECTED_TEAM });
    await assert.rejects(
      installPresenceTrust({ ...f.options, confirmation: INSTALL_CONFIRMATION }),
      /trust_existing_invalid/,
    );
    assert.equal(f.calls.some((call) => call.command === '/usr/bin/sudo'), false);
  } finally { f.cleanup(); }
});

test('install is idempotent for a deep-verified existing trust pair and never invokes sudo', async () => {
  const f = fixture();
  try {
    f.seedHelper(f.paths.vendoredHelperPath);
    f.seedHelper();
    f.seedPublicKey();
    const result = await installPresenceTrust({ ...f.options, confirmation: INSTALL_CONFIRMATION });
    assert.equal(result.installed, false);
    assert.equal(result.status.ready, true);
    assert.equal(f.calls.some((call) => call.command === '/usr/bin/sudo'), false);
  } finally { f.cleanup(); }
});

test('install atomically upgrades an older signed helper after exact capability probing', async () => {
  const f = fixture();
  try {
    f.seedHelper(f.paths.vendoredHelperPath);
    f.seedHelper();
    writeFileSync(f.paths.helperPath, 'old-signed-helper-binary');
    chmodSync(f.paths.helperPath, 0o755);
    f.seedPublicKey();
    const originalRun = f.options.run;
    const run = (command, args, options) => {
      if (command === f.paths.helperPath && args[0] === 'contract' &&
          readFileSync(f.paths.helperPath, 'utf8') === 'old-signed-helper-binary') {
        return { status: 1, signal: null, stdout: '', stderr: 'unsupported', error: undefined };
      }
      return originalRun(command, args, options);
    };
    const result = await installPresenceTrust({
      ...f.options, run, confirmation: INSTALL_CONFIRMATION,
    });
    assert.equal(result.installed, true);
    assert.equal(result.status.ready, true);
    assert.equal(readFileSync(f.paths.helperPath, 'utf8'), 'signed-helper-binary');
    assert.ok(f.calls.some((call) => call.command === '/usr/bin/sudo' && call.args[0] === '/bin/mv' &&
      call.args.at(-1) === f.paths.helperPath));
  } finally { f.cleanup(); }
});

test('old signed helper is restored when atomic replacement loses its acknowledgement', async () => {
  const f = fixture();
  try {
    f.seedHelper(f.paths.vendoredHelperPath);
    f.seedHelper();
    writeFileSync(f.paths.helperPath, 'old-signed-helper-binary');
    f.seedPublicKey();
    f.setFailHelperReplaceAfterMove(true);
    const originalRun = f.options.run;
    const run = (command, args, options) => {
      if (command === f.paths.helperPath && args[0] === 'contract' &&
          readFileSync(f.paths.helperPath, 'utf8') === 'old-signed-helper-binary') {
        return { status: 1, signal: null, stdout: '', stderr: 'unsupported', error: undefined };
      }
      return originalRun(command, args, options);
    };
    await assert.rejects(
      installPresenceTrust({ ...f.options, run, confirmation: INSTALL_CONFIRMATION }),
      /trust_helper_replace_failed/,
    );
    assert.equal(readFileSync(f.paths.helperPath, 'utf8'), 'old-signed-helper-binary');
    assert.equal(readFileSync(f.paths.publicKeyPath, 'utf8'), f.keys.publicKey);
  } finally { f.cleanup(); }
});

test('rollback never deletes a helper created by another actor before our install attempt', async () => {
  const f = fixture();
  try {
    f.seedHelper(f.paths.vendoredHelperPath);
    f.setRaceHelperOnDirectoryFailure(true);
    await assert.rejects(
      installPresenceTrust({ ...f.options, confirmation: INSTALL_CONFIRMATION }),
      /trust_helper_directory_install_failed/,
    );
    assert.equal(existsSync(f.paths.helperPath), true);
    assert.equal(f.calls.some((call) => call.command === '/usr/bin/sudo' &&
      call.args[0] === '/bin/rm' && call.args.includes(f.paths.helperPath)), false);
  } finally { f.cleanup(); }
});
