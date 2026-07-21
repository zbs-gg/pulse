import assert from 'node:assert/strict';
import test from 'node:test';

import {
  currentNativeTargetID, githubNativeUniversalMatrix, loadNativeUniversalMatrix,
} from '../scripts/native-universal-matrix.mjs';

test('required universal matrix has six exact native targets with no allowed failure', () => {
  const matrix = loadNativeUniversalMatrix();
  const github = githubNativeUniversalMatrix(matrix);
  assert.equal(github.include.length, 6);
  assert.equal(github.include.every((target) =>
    target.host === 'codex' && target.harness_version === '0.136.0' &&
    target.node_version === '22' && target.go_version === '1.25.6' &&
    !('continue_on_error' in target)), true);
  assert.deepEqual(github.include.map((target) => target.target_id).sort(), [
    'darwin-arm64', 'darwin-x64', 'linux-arm64-gnu',
    'linux-x64-gnu', 'win32-arm64', 'win32-x64',
  ]);
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
