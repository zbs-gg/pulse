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

test('missing enhanced presence stays visible but never blocks ordinary Personal readiness', () => {
  const checks = readyChecks();
  checks.presence_trust = { ok: false, detail: 'native helper is not installed' };

  const snapshot = readinessModule.projectPersonalLiveReadiness(checks, checkedAt);
  assert.equal(snapshot.reason_code, 'personal_live_ready');
  assert.deepEqual(readinessModule.projectPersonalAuthorityProfile(checks), {
    schema: 'pulse.personal_authority_profile.v1',
    version: 1,
    kind: 'portable',
    ordinary_ready: true,
    enhanced_presence: {
      schema: 'pulse.enhanced_presence.profile.v1',
      version: 1,
      kind: 'unavailable',
      available: false,
      protected_actions: [],
      reason_code: 'enhanced_presence_unavailable',
    },
  });
});

test('only an explicit verified enhanced adapter advertises protected actions', () => {
  const checks = readyChecks();
  checks.authority_profile = {
    schema: 'pulse.personal_authority_profile.v1',
    version: 1,
    kind: 'portable',
    ordinary_ready: true,
    enhanced_presence: {
      schema: 'pulse.enhanced_presence.profile.v1',
      version: 1,
      kind: 'macos_native',
      available: true,
      protected_actions: ['binding.change', 'vault.wipe'],
      reason_code: '',
    },
  };

  const profile = readinessModule.projectPersonalAuthorityProfile(checks);
  assert.equal(profile.enhanced_presence.available, true);
  assert.deepEqual(profile.enhanced_presence.protected_actions, ['binding.change', 'vault.wipe']);

  const forged = structuredClone(checks);
  forged.authority_profile.enhanced_presence.kind = 'unavailable';
  assert.throws(
    () => readinessModule.projectPersonalAuthorityProfile(forged),
    /enhanced-presence profile is invalid/,
  );
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

test('Claude Code and Cursor use one host-neutral readiness contract without Codex-only copy', () => {
  for (const host of ['claude-code', 'cursor']) {
    const checks = {
      presence_trust: { ok: false }, authority: { ok: true }, binding: { ok: true },
      runtime: { ok: true }, capture: { ok: true }, hooks: { ok: true }, vault: { ok: true },
      retrieval: { ok: true }, plugin: { ok: true }, host_access: { ok: true },
    };
    const ready = readinessModule.projectSupportedHostLiveReadiness(host, checks, checkedAt);
    assert.deepEqual(ready, {
      schema: 'pulse.supported_host_live_readiness.v1', target_host: host, outcome: 'ready',
      reason_code: 'supported_host_live_ready',
      next_action: { code: 'continue_working', label: 'Continue working' }, checked_at: checkedAt,
    });
    checks.hooks = { ok: false };
    const lifecycle = readinessModule.projectSupportedHostLiveReadiness(host, checks, checkedAt);
    assert.equal(lifecycle.reason_code, 'host_lifecycle_required');
    assert.deepEqual(lifecycle.next_action, {
      code: 'complete_host_lifecycle', label: 'Complete one normal agent turn',
    });
  }
});
