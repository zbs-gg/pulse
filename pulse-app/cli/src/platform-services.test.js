import assert from 'node:assert/strict';
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
  const nativeAdapter = {
    inspectExecutable: (path) => ({
      canonical_path: path, executable: true, regular_file: true, reparse_point: false,
      owner_only: true, sha256: 'a'.repeat(64),
    }),
    inspectPrivateState: (path, { kind }) => ({
      canonical_path: path, kind, owner_only: true, reparse_point: false,
    }),
    inspectProcess: (pid) => ({ running: true, pid, command: 'pulse.exe -data-dir C:\\vault', identity_token: 'proc-1234' }),
    terminateProcess: (pid, options) => { terminated.push({ pid, options }); return true; },
  };
  const services = createPlatformServices({
    platform: 'win32', home: 'C:\\Users\\Pulse', nativeAdapter,
    randomBytes: () => Buffer.alloc(32, 7),
  });

  assert.equal(services.path_delimiter, ';');
  assert.equal(services.inspectExecutable('C:\\Program Files\\Git\\cmd\\git.exe').sha256, 'a'.repeat(64));
  assert.equal(services.assertPrivateState('C:\\Users\\Pulse\\.pulse\\secret.key', { kind: 'file' }).owner_only, true);
  assert.equal(services.inspectProcess(1234).identity_token, 'proc-1234');
  assert.equal(services.terminateProcess(1234, { force: true }), true);
  assert.deepEqual(terminated, [{ pid: 1234, options: { force: true } }]);
  assert.equal(services.createStartupNonce(), '07'.repeat(32));
  assert.equal(services.isPathInside('C:\\Users\\Pulse\\Project\\file', 'c:\\users\\pulse\\project'), true);

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
