import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { captureStatePaths, writeCaptureStateFiles } from './capture-state.js';

test('capture state writes both preview and exact bound Personal vault markers', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-capture-state.'));
  const globalDataDir = join(root, '.pulse');
  const vaultDataDir = join(root, '.pulse', 'vaults', 'personal');
  const binding = {
    binding_id: 'binding_personal', binding_digest: 'a'.repeat(64), resolver_epoch: 4,
    fallback: false, mode: 'personal',
    personal: {
      store_id: 'store_personal_nik', data_dir: vaultDataDir,
      cache_dir: join(root, '.pulse', 'caches', 'personal'), base_url: 'http://127.0.0.1:18800',
    },
  };
  assert.deepEqual(captureStatePaths(globalDataDir, binding), [
    join(globalDataDir, 'capture-state.json'), join(vaultDataDir, 'capture-state.json'),
  ]);
  const paths = writeCaptureStateFiles({
    globalDataDir, binding, enabled: false, reason: 'host_disconnected',
    changedAt: new Date('2026-07-14T09:00:00Z'),
  });
  for (const path of paths) {
    const state = JSON.parse(readFileSync(path, 'utf8'));
    assert.equal(state.enabled, false);
    assert.equal(state.reason, 'host_disconnected');
  }
});
