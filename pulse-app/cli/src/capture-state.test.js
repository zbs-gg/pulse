import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { captureEnabledForHost, captureStatePaths, writeCaptureStateFiles } from './capture-state.js';

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
    globalDataDir, binding, host: 'codex', enabled: true, reason: 'host_connected',
    changedAt: new Date('2026-07-14T09:00:00Z'),
  });
  writeCaptureStateFiles({
    globalDataDir, binding, host: 'claude-code', enabled: true, reason: 'host_connected',
    changedAt: new Date('2026-07-14T09:01:00Z'),
  });
  writeCaptureStateFiles({
    globalDataDir, binding, host: 'claude-code', enabled: false, reason: 'host_disconnected',
    changedAt: new Date('2026-07-14T09:02:00Z'),
  });
  for (const path of paths) {
    const state = JSON.parse(readFileSync(path, 'utf8'));
    assert.equal(state.enabled, true);
    assert.equal(captureEnabledForHost(state, 'codex'), true);
    assert.equal(captureEnabledForHost(state, 'claude-code'), false);
    assert.equal(state.hosts['claude-code'].reason, 'host_disconnected');
  }
});

test('legacy enabled product capture migrates to Codex before Claude disconnects', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-capture-state-legacy.'));
  const globalDataDir = join(root, '.pulse');
  mkdirSync(globalDataDir, { recursive: true });
  writeFileSync(join(globalDataDir, 'capture-state.json'), JSON.stringify({
    schema: 'pulse.capture_state.v1', enabled: true, reason: 'codex_plugin_connected',
    changed_at: '2026-07-13T09:00:00.000Z',
  }));
  writeCaptureStateFiles({
    globalDataDir, host: 'claude-code', enabled: false, reason: 'host_disconnected',
    changedAt: new Date('2026-07-14T09:00:00Z'),
  });
  const state = JSON.parse(readFileSync(join(globalDataDir, 'capture-state.json'), 'utf8'));
  assert.equal(captureEnabledForHost(state, 'codex'), true);
  assert.equal(captureEnabledForHost(state, 'claude-code'), false);
  assert.equal(state.enabled, true);
});

test('unscoped legacy capture never enables an unproven host', () => {
  const legacy = { schema: 'pulse.capture_state.v1', enabled: true, reason: 'unknown_legacy_state' };
  assert.equal(captureEnabledForHost(legacy, 'codex'), false);
  assert.equal(captureEnabledForHost(legacy, 'claude-code'), false);
});
