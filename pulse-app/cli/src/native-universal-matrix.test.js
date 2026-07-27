import assert from 'node:assert/strict';
import test from 'node:test';

import {
  currentNativeTargetID, githubNativeUniversalMatrix, loadNativeUniversalMatrix,
} from '../scripts/native-universal-matrix.mjs';

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
  assert.equal(github.include.filter((target) => target.stability_runs === 5).length, 1);
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
