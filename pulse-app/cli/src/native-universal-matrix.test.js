import assert from 'node:assert/strict';
import test from 'node:test';

import {
  calibratedCursorVersionPattern, currentNativeTargetID, exactHarnessVersionPattern, githubNativeUniversalMatrix,
  loadNativeUniversalMatrix, nativeHarnessCommandUsesShell,
} from '../scripts/native-universal-matrix.mjs';

test('calibrated version matching accepts vendor labels without accepting adjacent digits', () => {
  const claude = exactHarnessVersionPattern('2.1.220');
  assert.match('2.1.220 (Claude Code)', claude);
  assert.match('claude 2.1.220', claude);
  assert.doesNotMatch('12.1.220', claude);
  assert.doesNotMatch('2.1.2201', claude);
});

test('Cursor calibration accepts only its numeric patch and package revision family', () => {
  const cursor = calibratedCursorVersionPattern('3.13');
  assert.match('3.13', cursor);
  assert.match('3.13.10', cursor);
  assert.match('3.13.10-1784845440', cursor);
  assert.doesNotMatch('3.130.10', cursor);
  assert.doesNotMatch('3.14.0', cursor);
  assert.doesNotMatch('3.13.10-beta', cursor);
});

test('Windows uses a command shell only for the two pinned npm CLI shims', () => {
  assert.equal(nativeHarnessCommandUsesShell('codex', 'win32'), true);
  assert.equal(nativeHarnessCommandUsesShell('claude', 'win32'), true);
  assert.equal(nativeHarnessCommandUsesShell('cursor', 'win32'), false);
  assert.equal(nativeHarnessCommandUsesShell('claude', 'linux'), false);
});

test('required universal matrix has eighteen exact host-target pairs with no allowed failure', () => {
  const matrix = loadNativeUniversalMatrix();
  const github = githubNativeUniversalMatrix(matrix);
  assert.equal(github.include.length, 18);
  assert.equal(github.include.every((target) => target.node_version === '22' &&
    target.go_version === '1.25.6' && !('continue_on_error' in target)), true);
  assert.deepEqual([...new Set(github.include.map((target) => target.target_id))].sort(), [
    'darwin-arm64', 'darwin-x64', 'linux-arm64-gnu',
    'linux-x64-gnu', 'win32-arm64', 'win32-x64',
  ]);
  assert.deepEqual(Object.fromEntries(matrix.harnesses.map((harness) => [harness.host, harness.version])), {
    'claude-code': '2.1.220', codex: '0.145.0', cursor: '3.13',
  });
  for (const platform of ['darwin', 'linux', 'win32']) {
    assert.equal(githubNativeUniversalMatrix(matrix, platform).include.length, 6);
  }
  for (const host of ['claude-code', 'codex', 'cursor']) {
    assert.equal(githubNativeUniversalMatrix(matrix, undefined, host).include.length, 6);
  }
  const windowsArmCodex = github.include.find((target) =>
    target.host === 'codex' && target.target_id === 'win32-arm64');
  assert.equal(windowsArmCodex.stability_runs, 5);
  assert.equal(windowsArmCodex.job_timeout_minutes, 75);
  assert.equal(github.include.filter((target) => target.stability_runs === 5).length, 1);
  assert.equal(github.include.filter((target) => target.job_timeout_minutes === 75).length, 1);
  assert.equal(github.include.filter((target) => target.job_timeout_minutes === 45).length, 17);
  assert.throws(() => githubNativeUniversalMatrix(matrix, 'freebsd'), /platform is unsupported/);
  assert.throws(() => githubNativeUniversalMatrix(matrix, undefined, 'unknown'), /host is unsupported/);
});

test('native runner identity rejects musl and maps every supported OS family exactly', () => {
  assert.equal(currentNativeTargetID({ platform: 'darwin', architecture: 'arm64' }), 'darwin-arm64');
  assert.equal(currentNativeTargetID({ platform: 'win32', architecture: 'x64' }), 'win32-x64');
  assert.equal(currentNativeTargetID({
    platform: 'linux', architecture: 'arm64',
    report: { getReport: () => ({ header: { glibcVersionRuntime: '2.39' } }) },
  }), 'linux-arm64-gnu');
  assert.throws(() => currentNativeTargetID({
    platform: 'linux', architecture: 'x64', report: { getReport: () => ({ header: {} }) },
  }), /GNU libc/);
});
