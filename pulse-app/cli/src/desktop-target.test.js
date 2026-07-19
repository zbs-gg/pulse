import assert from 'node:assert/strict';
import test from 'node:test';

import {
  DESKTOP_TARGET_IDS,
  DesktopTargetError,
  detectDesktopLibc,
  resolveDesktopTarget,
  selectDesktopTarget,
} from './desktop-target.js';

test('desktop target authority contains exactly the six universal targets', () => {
  assert.deepEqual(DESKTOP_TARGET_IDS, [
    'darwin-arm64',
    'darwin-x64',
    'linux-arm64-gnu',
    'linux-x64-gnu',
    'win32-arm64',
    'win32-x64',
  ]);
});

test('desktop libc detection admits glibc and fails closed for musl or unavailable reports', () => {
  assert.equal(detectDesktopLibc({
    platform: 'linux', report: { getReport: () => ({ header: { glibcVersionRuntime: '2.39' } }) },
  }), 'gnu');
  assert.equal(detectDesktopLibc({ platform: 'linux', report: { getReport: () => ({ header: {} }) } }), null);
  assert.equal(detectDesktopLibc({ platform: 'linux', report: { getReport: () => { throw new Error('unavailable'); } } }), null);
  assert.equal(detectDesktopLibc({ platform: 'darwin' }), null);
});

test('host facts resolve to one exact desktop target and distinguish GNU from musl', () => {
  assert.equal(resolveDesktopTarget({ platform: 'darwin', architecture: 'arm64' }).id, 'darwin-arm64');
  assert.equal(resolveDesktopTarget({ platform: 'darwin', architecture: 'x64' }).id, 'darwin-x64');
  assert.equal(resolveDesktopTarget({ platform: 'win32', architecture: 'arm64' }).id, 'win32-arm64');
  assert.equal(resolveDesktopTarget({ platform: 'win32', architecture: 'x64' }).id, 'win32-x64');
  assert.equal(resolveDesktopTarget({ platform: 'linux', architecture: 'arm64', libc: 'gnu' }).id, 'linux-arm64-gnu');
  assert.equal(resolveDesktopTarget({ platform: 'linux', architecture: 'x64', libc: 'gnu' }).id, 'linux-x64-gnu');
  for (const libc of [undefined, 'musl']) {
    assert.throws(
      () => resolveDesktopTarget({ platform: 'linux', architecture: 'x64', libc }),
      (error) => error instanceof DesktopTargetError && error.code === 'release_target_unavailable',
    );
  }
});

test('catalog selection returns only the exact target and missing targets fail closed', () => {
  const targets = Object.fromEntries(DESKTOP_TARGET_IDS.map((id) => [id, { id }]));
  assert.deepEqual(selectDesktopTarget(targets, {
    platform: 'linux', architecture: 'x64', libc: 'gnu',
  }), { id: 'linux-x64-gnu' });
  delete targets['linux-x64-gnu'];
  assert.throws(
    () => selectDesktopTarget(targets, { platform: 'linux', architecture: 'x64', libc: 'gnu' }),
    (error) => error instanceof DesktopTargetError && error.code === 'release_target_unavailable',
  );
});
