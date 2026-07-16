import assert from 'node:assert/strict';
import test from 'node:test';

const readinessModule = await import('./personal-live-readiness.js').catch(() => ({}));

const checkedAt = '2026-07-16T08:00:00Z';
const checkNames = [
  'presence_trust', 'authority', 'codex', 'plugin', 'marketplace', 'plugin_mcp',
  'mcp_shadow', 'legacy_hooks', 'native_hook_trust', 'binding', 'runtime',
  'activation', 'vault', 'capture', 'retrieval', 'hooks',
];

function readyChecks() {
  return Object.fromEntries(checkNames.map((name) => [name, { ok: true, detail: `${name} ready` }]));
}

test('ready doctor checks project one exact versioned Personal readiness snapshot', () => {
  assert.equal(typeof readinessModule.projectPersonalLiveReadiness, 'function');
  const snapshot = readinessModule.projectPersonalLiveReadiness(readyChecks(), checkedAt);
  assert.deepEqual(snapshot, {
    schema: 'pulse.personal_live_readiness.v1',
    outcome: 'ready',
    reason_code: 'personal_live_ready',
    next_action: { code: 'continue_working', label: 'Continue working' },
    checked_at: checkedAt,
  });
  assert.deepEqual(readinessModule.projectPersonalLiveReadiness(readyChecks(), checkedAt), snapshot);
});

test('plugin and lifecycle removal have stable doctor reasons reused by installer health', () => {
  assert.equal(typeof readinessModule.projectPersonalLiveReadiness, 'function');
  assert.equal(typeof readinessModule.personalInstallHealthFromReadiness, 'function');

  const pluginChecks = readyChecks();
  pluginChecks.plugin = { ok: false, detail: 'plugin removed' };
  const plugin = readinessModule.projectPersonalLiveReadiness(pluginChecks, checkedAt);
  assert.deepEqual(plugin, {
    schema: 'pulse.personal_live_readiness.v1',
    outcome: 'action_required',
    reason_code: 'codex_plugin_unavailable',
    next_action: { code: 'repair_codex_plugin', label: 'Run pulse repair' },
    checked_at: checkedAt,
  });
  assert.deepEqual(readinessModule.personalInstallHealthFromReadiness(plugin), {
    ready: false,
    full_retrieval: true,
    warming: false,
    outcome: 'action_required',
    reason_code: 'codex_plugin_unavailable',
  });

  const lifecycleChecks = readyChecks();
  lifecycleChecks.hooks = { ok: false, reason: 'lifecycle_receipt_missing', detail: 'removed' };
  const lifecycle = readinessModule.projectPersonalLiveReadiness(lifecycleChecks, checkedAt);
  assert.equal(lifecycle.reason_code, 'codex_hook_lifecycle_required');
  assert.equal(lifecycle.next_action.code, 'complete_codex_lifecycle');
  assert.equal(readinessModule.personalInstallHealthFromReadiness(lifecycle).reason_code, lifecycle.reason_code);
});
