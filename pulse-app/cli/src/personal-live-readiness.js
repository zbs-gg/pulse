const SCHEMA = 'pulse.personal_live_readiness.v1';

const CONTRACTS = Object.freeze({
  personal_live_ready: Object.freeze({
    outcome: 'ready', action: Object.freeze({ code: 'continue_working', label: 'Continue working' }),
  }),
  presence_required: Object.freeze({
    outcome: 'action_required', action: Object.freeze({ code: 'install_presence_trust', label: 'Install Pulse presence helper' }),
  }),
  binding_repair_required: Object.freeze({
    outcome: 'action_required', action: Object.freeze({ code: 'repair_binding', label: 'Run pulse repair' }),
  }),
  codex_activation_incomplete: Object.freeze({
    outcome: 'action_required', action: Object.freeze({ code: 'repair_codex_activation', label: 'Run pulse repair' }),
  }),
  codex_plugin_unavailable: Object.freeze({
    outcome: 'action_required', action: Object.freeze({ code: 'repair_codex_plugin', label: 'Run pulse repair' }),
  }),
  codex_hook_trust_required: Object.freeze({
    outcome: 'action_required', action: Object.freeze({ code: 'trust_codex_hooks', label: 'Trust the Pulse hook bundle' }),
  }),
  codex_hook_lifecycle_required: Object.freeze({
    outcome: 'action_required', action: Object.freeze({ code: 'complete_codex_lifecycle', label: 'Complete one normal Codex turn' }),
  }),
  codex_native_lifecycle_attestation_unavailable: Object.freeze({
    outcome: 'action_required', action: Object.freeze({ code: 'use_pulse_mcp', label: 'Use Pulse MCP tools explicitly' }),
  }),
  daemon_unavailable: Object.freeze({
    outcome: 'action_required', action: Object.freeze({ code: 'repair_daemon', label: 'Run pulse repair' }),
  }),
  full_retrieval_unavailable: Object.freeze({
    outcome: 'action_required', action: Object.freeze({ code: 'repair_retrieval', label: 'Run pulse repair' }),
  }),
  local_embedder_warming: Object.freeze({
    outcome: 'warming', action: Object.freeze({ code: 'wait_for_embedder', label: 'Keep Pulse open while the local model warms' }),
  }),
});

function canonicalCheckedAt(value) {
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) throw new TypeError('personal readiness checked_at is invalid');
  return date.toISOString().replace(/\.000Z$/, 'Z').replace(/(\.\d*?[1-9])0+Z$/, '$1Z');
}

function failed(checks, name) {
  return checks?.[name]?.ok !== true;
}

function reasonFromChecks(checks) {
  if (failed(checks, 'presence_trust')) return 'presence_required';
  if (failed(checks, 'authority') || failed(checks, 'binding')) return 'binding_repair_required';
  if (['codex', 'plugin', 'marketplace', 'plugin_mcp', 'mcp_shadow', 'legacy_hooks'].some((name) => failed(checks, name))) {
    return 'codex_plugin_unavailable';
  }
  if (failed(checks, 'native_hook_trust')) return 'codex_hook_trust_required';
  if (failed(checks, 'runtime') || failed(checks, 'activation') || failed(checks, 'capture')) {
    return 'codex_activation_incomplete';
  }
  if (failed(checks, 'hooks')) {
    return checks?.hooks?.reason === 'codex_native_lifecycle_attestation_unavailable'
      ? 'codex_native_lifecycle_attestation_unavailable'
      : 'codex_hook_lifecycle_required';
  }
  if (failed(checks, 'vault')) return 'daemon_unavailable';
  if (failed(checks, 'retrieval')) return 'full_retrieval_unavailable';
  return 'personal_live_ready';
}

export function projectPersonalLiveReadiness(checks, checkedAt = new Date()) {
  const reasonCode = reasonFromChecks(checks);
  const contract = CONTRACTS[reasonCode];
  return {
    schema: SCHEMA,
    outcome: contract.outcome,
    reason_code: reasonCode,
    next_action: { ...contract.action },
    checked_at: canonicalCheckedAt(checkedAt),
  };
}

export function personalInstallHealthFromReadiness(snapshot) {
  const contract = CONTRACTS[snapshot?.reason_code];
  if (!contract || snapshot?.schema !== SCHEMA || snapshot?.outcome !== contract.outcome ||
      snapshot?.next_action?.code !== contract.action.code || snapshot?.next_action?.label !== contract.action.label ||
      canonicalCheckedAt(snapshot?.checked_at) !== snapshot.checked_at) {
    throw new TypeError('personal readiness snapshot is invalid');
  }
  const ready = snapshot.reason_code === 'personal_live_ready';
  return {
    ready,
    full_retrieval: !['daemon_unavailable', 'full_retrieval_unavailable'].includes(snapshot.reason_code),
    warming: snapshot.reason_code === 'local_embedder_warming',
    outcome: snapshot.outcome,
    reason_code: snapshot.reason_code,
  };
}

export const PERSONAL_LIVE_READINESS_SCHEMA = SCHEMA;
