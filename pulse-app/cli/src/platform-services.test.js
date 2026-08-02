import assert from 'node:assert/strict';
import { chmodSync, existsSync, mkdtempSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  PlatformServicesError,
  createPlatformServices,
} from './platform-services.js';

test('portable services own port probing, OS version, host candidates, and containment', () => {
  const calls = [];
  const services = createPlatformServices({
    platform: 'linux',
    architecture: 'x64',
    home: '/home/pulse',
    osRelease: () => '6.8.0-31-generic',
    spawn: (command, args) => {
      calls.push({ command, args });
      return { status: 0, stdout: '', stderr: '' };
    },
  });

  assert.equal(services.path_delimiter, ':');
  assert.equal(services.desktopOSVersion(), '6.8.0');
  assert.equal(services.probePort(18789), 'free');
  assert.equal(calls[0].command, process.execPath);
  assert.equal(calls[0].args.at(-1), '18789');
  assert.equal(services.isPathInside('/home/pulse/project/file', '/home/pulse/project'), true);
  assert.equal(services.isPathInside('/home/pulse/project-other', '/home/pulse/project'), false);
  assert.ok(services.hostCandidates().claude.every((path) => path.startsWith('/')));
  assert.ok(services.hostCandidates().cursor.some((path) => path === '/opt/Cursor/cursor'));
});

test('macOS host discovery includes the Homebrew Codex executable', () => {
  const services = createPlatformServices({
    platform: 'darwin',
    architecture: 'arm64',
    home: '/Users/pulse',
  });

  assert.ok(services.hostCandidates().codex.includes('/opt/homebrew/bin/codex'));
});

test('native packed Codex calibration path is available only under the exact isolated attestation', () => {
  const executable = '/opt/native-codex/bin/codex';
  const attested = createPlatformServices({
    platform: 'linux', architecture: 'x64', home: '/home/pulse',
    env: {
      PULSE_TRUST_MODE: 'test',
      PULSE_NATIVE_PACKED_FIXTURE_ATTESTATION: '1',
      PULSE_NATIVE_PACKED_CODEX_EXECUTABLE: executable,
    },
  });
  assert.deepEqual(attested.hostCandidates().codex, [executable]);
  assert.deepEqual(attested.hostCandidates().claude, []);
  assert.deepEqual(attested.hostCandidates().cursor, []);
  const production = createPlatformServices({
    platform: 'linux', architecture: 'x64', home: '/home/pulse',
    env: { PULSE_NATIVE_PACKED_CODEX_EXECUTABLE: executable },
  });
  assert.equal(production.hostCandidates().codex.includes(executable), false);
});

test('protected vendor harness paths are accepted only inside a GitHub runner temp root', () => {
  const executable = '/runner/temp/vendor/codex';
  const services = createPlatformServices({
    platform: 'linux', architecture: 'x64', home: '/runner/temp/home',
    env: {
      CI: 'true',
      GITHUB_ACTIONS: 'true',
      PULSE_PROTECTED_HARNESS_AUTHORITY: 'production_candidate',
      PULSE_PROTECTED_HARNESS_CODEX_EXECUTABLE: executable,
      RUNNER_TEMP: '/runner/temp',
    },
  });
  assert.equal(services.hostCandidates().codex[0], executable);
  for (const env of [
    { CI: 'true', GITHUB_ACTIONS: 'true', PULSE_PROTECTED_HARNESS_AUTHORITY: 'fixture', RUNNER_TEMP: '/runner/temp' },
    { CI: 'true', GITHUB_ACTIONS: 'true', PULSE_PROTECTED_HARNESS_AUTHORITY: 'public_registry', RUNNER_TEMP: '/other' },
  ]) {
    const rejected = createPlatformServices({
      platform: 'linux', architecture: 'x64', home: '/runner/temp/home',
      env: { ...env, PULSE_PROTECTED_HARNESS_CODEX_EXECUTABLE: executable },
    });
    assert.equal(rejected.hostCandidates().codex.includes(executable), false);
  }
});

test('Windows Git discovery includes the native bin executable used by hosted runners', () => {
  const services = createPlatformServices({
    platform: 'win32', architecture: 'x64', home: 'C:\\Users\\Pulse',
    env: { ProgramFiles: 'C:\\Program Files' }, nativeAdapter: {},
  });
  assert.equal(services.hostCandidates().git[0], 'C:\\Program Files\\Git\\bin\\git.exe');
});

test('port probing distinguishes occupied from unknown without a system utility', () => {
  const occupied = createPlatformServices({
    platform: 'darwin', spawn: () => ({ status: 2, stdout: '', stderr: '' }),
  });
  const unknown = createPlatformServices({
    platform: 'darwin', spawn: () => ({ status: 3, stdout: '', stderr: '' }),
  });
  assert.equal(occupied.probePort(18789), 'occupied');
  assert.equal(unknown.probePort(18789), 'unknown');
});

test('Windows trust and process operations fail closed without the native adapter', () => {
  const services = createPlatformServices({ platform: 'win32', home: 'C:\\Users\\Pulse' });
  for (const operation of [
    () => services.inspectExecutable('C:\\Program Files\\Git\\cmd\\git.exe'),
    () => services.assertPrivateState('C:\\Users\\Pulse\\.pulse\\secret.key', { kind: 'file' }),
    () => services.inspectProcess(1234),
    () => services.terminateProcess(1234),
  ]) {
    assert.throws(operation, (error) =>
      error instanceof PlatformServicesError && error.code === 'platform_native_adapter_unavailable');
  }
});

test('Windows native adapter must prove ACL ownership and reject reparse points', () => {
  const terminated = [];
  const privateOperations = [];
  const nativeAdapter = {
    inspectExecutable: (path) => ({
      canonical_path: path, executable: true, regular_file: true, reparse_point: false,
      owner_only: true, sha256: 'a'.repeat(64),
    }),
    inspectPrivateState: (path, { kind }) => ({
      canonical_path: path, kind, owner_only: true, reparse_point: false,
    }),
    inspectPrivateTree: (_path, { entries }) => ({
      bytes: entries.reduce((total, entry) => total + entry.bytes, 0), files: entries.length,
    }),
    inspectProcess: (pid) => ({ running: true, pid, command: 'pulse.exe -data-dir C:\\vault', identity_token: 'proc-1234' }),
    terminateProcess: (pid, options) => { terminated.push({ pid, options }); return true; },
    ensurePrivateDirectory: (path) => { privateOperations.push(['ensure', path]); },
    readPrivateFile: (path) => { privateOperations.push(['read', path]); return '{}\n'; },
    atomicWritePrivateFile: (path) => { privateOperations.push(['write', path]); },
    removePrivateFile: (path) => { privateOperations.push(['remove', path]); return true; },
    acquirePrivateLock: (path) => {
      privateOperations.push(['lock', path]);
      return () => privateOperations.push(['unlock', path]);
    },
  };
  const services = createPlatformServices({
    platform: 'win32', home: 'C:\\Users\\Pulse', nativeAdapter,
    randomBytes: () => Buffer.alloc(32, 7),
  });

  assert.equal(services.path_delimiter, ';');
  assert.equal(services.inspectExecutable('C:\\Program Files\\Git\\cmd\\git.exe').sha256, 'a'.repeat(64));
  assert.equal(services.assertPrivateState('C:\\Users\\Pulse\\.pulse\\secret.key', { kind: 'file' }).owner_only, true);
  assert.deepEqual(services.validatePrivateTree('C:\\Users\\Pulse\\.pulse\\runtime', {
    files: [{ bytes: 2, executable: false, path: 'state.json', sha256: 'a'.repeat(64) }],
  }), { bytes: 2, files: 1 });
  assert.equal(services.inspectProcess(1234).identity_token, 'proc-1234');
  assert.equal(services.terminateProcess(1234, { force: true }), true);
  assert.deepEqual(terminated, [{ pid: 1234, options: { force: true } }]);
  assert.equal(services.createStartupNonce(), '07'.repeat(32));
  assert.equal(services.isPathInside('C:\\Users\\Pulse\\Project\\file', 'c:\\users\\pulse\\project'), true);
  const privateRoot = 'C:\\Users\\Pulse\\.pulse';
  const privateFile = `${privateRoot}\\state.json`;
  services.ensurePrivateDirectory(privateRoot);
  services.atomicWritePrivateFile(privateFile, '{}\n');
  assert.equal(services.readPrivateFile(privateFile), '{}\n');
  assert.equal(services.removePrivateFile(privateFile), true);
  services.acquirePrivateLock(`${privateRoot}\\state.lock`)();
  assert.deepEqual(privateOperations.map(([operation]) => operation), [
    'ensure', 'write', 'read', 'remove', 'lock', 'unlock',
  ]);

  const unsafe = createPlatformServices({
    platform: 'win32',
    nativeAdapter: {
      ...nativeAdapter,
      inspectPrivateState: (path, { kind }) => ({
        canonical_path: path, kind, owner_only: true, reparse_point: true,
      }),
    },
  });
  assert.throws(
    () => unsafe.assertPrivateState('C:\\Users\\Pulse\\.pulse\\secret.key', { kind: 'file' }),
    (error) => error instanceof PlatformServicesError && error.code === 'platform_private_state_unsafe',
  );
});

test('Windows executable discovery treats a missing bounded candidate as absent', () => {
  const services = createPlatformServices({
    platform: 'win32', architecture: 'x64', home: 'C:\\Users\\Pulse',
    nativeAdapter: {
      inspectExecutable: () => { throw Object.assign(new Error('missing'), { code: 'ENOENT' }); },
    },
  });
  assert.equal(services.inspectExecutable('C:\\Users\\Pulse\\.local\\bin\\codex.exe'), null);
});

test('Git discovery uses a bounded verified executable and never inherited PATH', () => {
  const calls = [];
  const services = createPlatformServices({
    platform: 'linux',
    inspectExecutable: (path) => path === '/usr/bin/git'
      ? { canonical_path: path, sha256: 'b'.repeat(64) }
      : null,
    spawn: (command, args, options) => {
      calls.push({ command, args, options });
      return { status: 0, stdout: '/repo\n', stderr: '' };
    },
  });
  assert.equal(services.runGit('/repo/nested', ['rev-parse', '--show-toplevel']), '/repo');
  assert.equal(calls[0].command, '/usr/bin/git');
  assert.deepEqual(calls[0].args, ['-C', '/repo/nested', 'rev-parse', '--show-toplevel']);
  assert.equal(calls[0].options.env.PATH, undefined);
});

test('portable private-state services own directory creation, bounded reads, atomic writes, removal, and locks', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-platform-private-'));
  try {
    const services = createPlatformServices();
    const directory = join(root, 'private');
    const file = join(directory, 'state.json');
    services.ensurePrivateDirectory(directory);
    services.atomicWritePrivateFile(file, '{"ok":true}\n', { maxBytes: 1024 });
    assert.equal(services.readPrivateFile(file, { maxBytes: 1024 }), '{"ok":true}\n');

    const release = services.acquirePrivateLock(`${file}.lock`, { staleAfterMs: 30_000 });
    assert.throws(
      () => services.acquirePrivateLock(`${file}.lock`, { staleAfterMs: 30_000 }),
      (error) => error instanceof PlatformServicesError && error.code === 'platform_lock_occupied',
    );
    release();
    services.acquirePrivateLock(`${file}.lock`, { staleAfterMs: 30_000 })();

    services.removePrivateFile(file);
    assert.equal(existsSync(file), false);
    assert.equal(services.readPrivateFile(file, { missing: true, maxBytes: 1024 }), null);

    writeFileSync(file, 'unsafe', { mode: 0o600 });
    chmodSync(file, 0o644);
    assert.throws(
      () => services.readPrivateFile(file, { maxBytes: 1024 }),
      (error) => error instanceof PlatformServicesError && error.code === 'platform_private_state_unsafe',
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('Windows private-state mutations and locks fail closed without native operations', () => {
  const services = createPlatformServices({ platform: 'win32', home: 'C:\\Users\\Pulse' });
  const root = 'C:\\Users\\Pulse\\.pulse';
  for (const operation of [
    () => services.ensurePrivateDirectory(root),
    () => services.readPrivateFile(`${root}\\state.json`, { missing: true }),
    () => services.atomicWritePrivateFile(`${root}\\state.json`, '{}\n'),
    () => services.removePrivateFile(`${root}\\state.json`),
    () => services.acquirePrivateLock(`${root}\\state.lock`),
  ]) {
    assert.throws(operation, (error) =>
      error instanceof PlatformServicesError && error.code === 'platform_native_adapter_unavailable');
  }
});

test('portable path identity is opaque, rename-stable, kind-bound, and clone-distinct', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-platform-identity-'));
  try {
    const services = createPlatformServices();
    const first = join(root, 'first');
    const moved = join(root, 'moved');
    const clone = join(root, 'clone');
    services.ensurePrivateDirectory(first);
    services.ensurePrivateDirectory(clone);
    const before = services.inspectPathIdentity(first, { kind: 'directory' });
    renameSync(first, moved);
    const after = services.inspectPathIdentity(moved, { kind: 'directory' });
    const separate = services.inspectPathIdentity(clone, { kind: 'directory' });
    assert.ok(before.identity_token.length > 0);
    assert.equal(after.identity_token, before.identity_token);
    assert.notEqual(separate.identity_token, before.identity_token);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('integrity reads allow non-writable owner or root authority without requiring owner-only mode', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-platform-integrity-'));
  try {
    const services = createPlatformServices();
    const file = join(root, 'registry.json');
    writeFileSync(file, '{}\n', { mode: 0o644 });
    assert.equal(services.readIntegrityFile(file, { owner: 'current', encoding: 'utf8' }), '{}\n');
    chmodSync(file, 0o666);
    assert.throws(
      () => services.readIntegrityFile(file, { owner: 'current' }),
      (error) => error instanceof PlatformServicesError && error.code === 'platform_integrity_state_unsafe',
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('Windows path identity and integrity reads require exact native proofs', () => {
  const root = 'C:\\Users\\Pulse\\Project';
  const file = 'C:\\ProgramData\\Pulse\\trust.json';
  const unavailable = createPlatformServices({ platform: 'win32', home: 'C:\\Users\\Pulse' });
  assert.throws(
    () => unavailable.inspectPathIdentity(root, { kind: 'directory' }),
    (error) => error instanceof PlatformServicesError && error.code === 'platform_native_adapter_unavailable',
  );
  assert.throws(
    () => unavailable.readIntegrityFile(file, { owner: 'root' }),
    (error) => error instanceof PlatformServicesError && error.code === 'platform_native_adapter_unavailable',
  );

  const services = createPlatformServices({
    platform: 'win32', home: 'C:\\Users\\Pulse',
    nativeAdapter: {
      inspectPathIdentity: (path, { kind }) => ({
        canonical_path: path, identity_token: 'native-volume-file-id', kind, reparse_point: false,
      }),
      readIntegrityFile: (path, { owner }) => ({
        bytes: '{}\n', canonical_path: path, owner, regular_file: true, reparse_point: false,
      }),
    },
  });
  assert.equal(services.inspectPathIdentity(root, { kind: 'directory' }).identity_token, 'native-volume-file-id');
  assert.equal(services.readIntegrityFile(file, { owner: 'root', encoding: 'utf8' }), '{}\n');
});
