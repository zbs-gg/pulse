import { randomBytes } from 'node:crypto';
import {
  chmodSync, closeSync, fsyncSync, lstatSync, mkdirSync, openSync, renameSync, rmSync, writeFileSync,
} from 'node:fs';
import { dirname, isAbsolute, join, resolve } from 'node:path';

import { isCanonicalRepositoryID } from './host-adapter.js';

const STEP = Object.freeze({
  artifacts: 'artifacts_staged',
  presence: 'presence_ready',
  principal: 'principal_ready',
  binding: 'binding_ready',
  core: 'core_ready',
  harnesses: 'harnesses_activated',
  retrieval: 'full_retrieval_ready',
});

export const PERSONAL_INSTALL_STEPS = Object.freeze(Object.values(STEP));

const ACTION_REQUIRED_CODES = new Set([
  'binding_conflict',
  'binding_legacy',
  'binding_repair_required',
	'codex_hook_lifecycle_required',
	'codex_native_lifecycle_attestation_unavailable',
  'codex_hook_trust_required',
  'codex_activation_incomplete',
  'codex_identity_changed',
  'codex_identity_invalid',
  'codex_login_required',
  'codex_probe_failed',
  'codex_version_invalid',
  'claude_activation_incomplete',
  'claude_identity_changed',
  'claude_identity_invalid',
  'claude_probe_failed',
  'claude_version_invalid',
  'cursor_activation_incomplete',
  'supported_harness_activation_failed',
  'supported_harness_incompatible',
  'supported_harness_missing',
  'presence_denied',
  'presence_required',
  'principal_repair_required',
  'runtime_repair_required',
]);

const SAFE_REASON_CODE = /^[a-z0-9][a-z0-9_]{0,127}$/;

const LATEST_INSTALL_RECEIPT_REF = 'pulse://receipts/install/latest';

export class PersonalInstallError extends Error {
  constructor(code) {
    super(code);
    this.name = 'PersonalInstallError';
    this.code = code;
  }
}

function fail(code) {
  throw new PersonalInstallError(code);
}

function exactDependencies(dependencies) {
  const required = [
    'inspectRuntime', 'provisionRuntime',
    'inspectPresence', 'installPresence',
    'inspectPrincipal', 'createPrincipal',
    'inspectBinding', 'createBinding',
    'inspectCore', 'activateCore',
    'inspectActivation', 'activateHosts',
    'inspectHealth', 'writeReceipt',
  ];
  if (!dependencies || required.some((name) => typeof dependencies[name] !== 'function')) {
    fail('dependencies_invalid');
  }
  return dependencies;
}

function exactPlan(plan) {
  const workspace = plan?.detected?.workspace;
  if (!plan || plan.schema !== 'pulse.personal_install_plan.v1' || plan.contract_version !== 1 ||
      !['ready_to_install', 'action_required', 'unsupported'].includes(plan.outcome) ||
      !Array.isArray(plan.reason_codes) || plan.reason_codes.some((code) => typeof code !== 'string' || code.length < 1) ||
      (plan.outcome === 'ready_to_install' && (!workspace || typeof workspace.workspace_id !== 'string' ||
        typeof workspace.repository_id !== 'string'))) {
    fail('plan_invalid');
  }
  return plan;
}

function planWorkspace(plan) {
  return plan.detected.workspace ?? {
    canonical_path: null,
    repository_id: 'repository_unavailable',
    workspace_id: 'workspace_unavailable',
  };
}

function nextAction(reasonCode) {
  const actions = {
    binding_conflict: 'Run pulse install-plan --json and review current_state.binding before approving any replacement.',
    binding_legacy: 'Run pulse install-plan --json and review current_state.binding before approving any replacement.',
    binding_repair_required: 'Run pulse install-plan --json and review current_state.binding before approving any replacement.',
    codex_activation_incomplete: 'Run pulse doctor codex --json and fix the first failed activation check.',
    codex_hook_lifecycle_required: 'Run pulse home to inspect the installed memory, then open and use a normal new Codex task so the Pulse lifecycle can run.',
    codex_native_lifecycle_attestation_unavailable: 'Run pulse home to inspect the installed memory. Use Pulse MCP tools explicitly for now; Codex 0.136 does not expose replayable native hook execution evidence to Pulse.',
    codex_hook_trust_required: 'Approve the Pulse hook in Codex, then run pulse install again.',
    codex_identity_changed: 'Run pulse install-plan --json again and review the changed Codex executable before retrying.',
    codex_identity_invalid: 'Install the Codex CLI in a supported system location, then run pulse install-plan --json.',
    codex_login_required: 'Sign in to Codex, then run pulse install again.',
    codex_probe_failed: 'Verify the detected Codex CLI runs, then run pulse install-plan --json again.',
    codex_version_invalid: 'Update the detected Codex CLI, then run pulse install-plan --json again.',
    claude_identity_changed: 'Run pulse install-plan --json again and review the changed Claude Code executable before retrying.',
    claude_identity_invalid: 'Install Claude Code in a supported location, then run pulse install-plan --json.',
    claude_probe_failed: 'Verify the detected Claude Code CLI runs, then run pulse install-plan --json again.',
    claude_version_invalid: 'Update Claude Code to a compatible version, then run pulse install again.',
    cursor_activation_incomplete: 'Reload Cursor, then run pulse repair.',
    disclosure_consent_required: 'Review the install plan and approve it in the interactive wizard.',
    platform_unsupported: 'Use an Apple Silicon Mac with macOS, Node 20+, a supported harness, and a Git project.',
    presence_denied: 'Approve the macOS security prompt, then run pulse install again.',
    presence_required: 'Complete the macOS security prompt, then run pulse install again.',
    principal_repair_required: 'Run pulse install-plan --json and review current_state.principal before changing the Personal identity.',
    runtime_repair_required: 'Run pulse install-plan --json and review current_state.runtime before changing the active runtime.',
    supported_harness_activation_failed: 'Fix the reported harness activation, then run pulse repair.',
    supported_harness_incompatible: 'Update Claude Code, Cursor, or Codex to a compatible version, then run pulse install again.',
    supported_harness_missing: 'Install Claude Code, Cursor, or Codex, then run pulse install again.',
  };
  return actions[reasonCode] ?? 'Fix the reported requirement, then run pulse install again.';
}

function terminalResult(plan, {
  completedSteps = [],
  hostStatus,
  outcome,
  reasonCode,
}) {
  const workspace = planWorkspace(plan);
  const normalizedHostStatus = normalizePersonalInstallHostStatus(hostStatus);
  return {
    schema: 'pulse.personal_install_result.v1',
    outcome,
    reason_code: reasonCode,
    completed_steps: [...completedSteps],
    current_project: {
      workspace_id: workspace.workspace_id,
      repository_id: workspace.repository_id,
      canonical_path: workspace.canonical_path,
    },
    preserved_data: true,
    host_status: normalizedHostStatus,
    next_action: outcome === 'ready'
      ? normalizedHostStatus.parity === 'degraded'
        ? 'Memory Home is opening now. Pulse is usable through a verified harness; run pulse repair to attach the remaining detected harnesses.'
        : 'Memory Home is opening now. Review the installed memory, then open a new task in a verified harness and create the first visible memory.'
      : nextAction(reasonCode),
  };
}

export function normalizePersonalInstallHostStatus(value) {
  if (value === undefined) return { product_ready: false, parity: 'blocked', hosts: [] };
  if (!value || typeof value !== 'object' || Array.isArray(value) || typeof value.product_ready !== 'boolean' ||
      !['blocked', 'complete', 'degraded'].includes(value.parity) || !Array.isArray(value.hosts) || value.hosts.length > 3) {
    fail('host_status_invalid');
  }
  const seen = new Set();
  const hosts = value.hosts.map((host) => {
    if (!host || typeof host !== 'object' || !['claude-code', 'codex', 'cursor'].includes(host.host) || seen.has(host.host) ||
        typeof host.activated !== 'boolean' || typeof host.verified !== 'boolean' ||
        typeof host.lifecycle_ready !== 'boolean' || !SAFE_REASON_CODE.test(host.reason_code ?? '')) {
      fail('host_status_invalid');
    }
    seen.add(host.host);
    return {
      host: host.host,
      activated: host.activated,
      verified: host.verified,
      lifecycle_ready: host.lifecycle_ready,
      reason_code: host.reason_code,
    };
  });
  return { product_ready: value.product_ready, parity: value.parity, hosts };
}

function receiptFor(result) {
  return {
    schema: 'pulse.personal_install_receipt.v2',
    outcome: result.outcome,
    reason_code: result.reason_code,
    completed_steps: [...result.completed_steps],
    workspace_id: result.current_project.workspace_id,
    repository_id: result.current_project.repository_id,
    preserved_data: true,
    host_status: normalizePersonalInstallHostStatus(result.host_status),
  };
}

function requirePrivateDirectory(path) {
  let info;
  try {
    mkdirSync(path, { recursive: true, mode: 0o700 });
    info = lstatSync(path);
  } catch { fail('install_receipt_directory_unsafe'); }
  const uid = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
  if (!info.isDirectory() || info.isSymbolicLink() || info.uid !== uid || (info.mode & 0o077) !== 0) {
    fail('install_receipt_directory_unsafe');
  }
}

function syncDirectory(path) {
  const descriptor = openSync(path, 'r');
  try { fsyncSync(descriptor); } finally { closeSync(descriptor); }
}

export function writePersonalInstallReceipt(receipt, {
  dataDir,
  now = new Date(),
} = {}) {
  if (typeof dataDir !== 'string' || !isAbsolute(dataDir) || resolve(dataDir) !== dataDir ||
      !receipt || receipt.schema !== 'pulse.personal_install_receipt.v2' ||
      !['ready', 'warming', 'action_required', 'partial', 'blocked'].includes(receipt.outcome) ||
      !/^[a-z0-9][a-z0-9_]{0,127}$/.test(receipt.reason_code ?? '') ||
      !/^workspace_[a-z0-9][a-z0-9_]{0,127}$/.test(receipt.workspace_id ?? '') ||
      !isCanonicalRepositoryID(receipt.repository_id) ||
      receipt.preserved_data !== true || !Array.isArray(receipt.completed_steps) ||
      receipt.completed_steps.some((step, index) => step !== PERSONAL_INSTALL_STEPS[index]) ||
      Number.isNaN(now.valueOf())) {
    fail('install_receipt_invalid');
  }
  const hostStatus = normalizePersonalInstallHostStatus(receipt.host_status);
  const root = resolve(dataDir);
  const directory = join(root, 'receipts', 'install');
  requirePrivateDirectory(root);
  requirePrivateDirectory(dirname(directory));
  requirePrivateDirectory(directory);
  const record = {
    completed_steps: [...receipt.completed_steps],
    created_at: now.toISOString(),
    outcome: receipt.outcome,
    preserved_data: true,
    host_status: hostStatus,
    reason_code: receipt.reason_code,
    repository_id: receipt.repository_id,
    schema: receipt.schema,
    workspace_id: receipt.workspace_id,
  };
  const path = join(directory, `${receipt.workspace_id}.json`);
  const temporary = join(directory, `.${receipt.workspace_id}.${process.pid}.${randomBytes(8).toString('hex')}.new`);
  try {
    writeFileSync(temporary, `${JSON.stringify(record)}\n`, { encoding: 'utf8', flag: 'wx', mode: 0o600 });
    chmodSync(temporary, 0o600);
    const descriptor = openSync(temporary, 'r');
    try { fsyncSync(descriptor); } finally { closeSync(descriptor); }
    renameSync(temporary, path);
    syncDirectory(directory);
  } catch (error) {
    if (error instanceof PersonalInstallError) throw error;
    fail('install_receipt_write_failed');
  } finally {
    rmSync(temporary, { force: true });
  }
  return path;
}

async function emitTerminal(dependencies, result) {
  const written = await writeReceiptOnce(dependencies, receiptFor(result));
  return { ...result, receipt_ref: stableReceiptReference(written) };
}

async function emitCheckpoint(dependencies, plan, completedSteps) {
  const step = completedSteps.at(-1);
  const checkpoint = terminalResult(plan, {
    completedSteps,
    outcome: 'partial',
    reasonCode: `checkpoint_${step}`,
  });
  await writeReceiptOnce(dependencies, receiptFor(checkpoint));
}

function stableReceiptReference(written) {
  const candidate = typeof written === 'string' ? written : written?.receipt_ref;
  if (typeof candidate === 'string' &&
      (/^receipt_[a-z0-9][a-z0-9_.:-]{0,127}$/.test(candidate) ||
       /^pulse:\/\/receipts\/[a-z0-9][a-z0-9/_-]{0,191}$/.test(candidate)) &&
      !candidate.includes('..') && !candidate.includes('//', 'pulse://'.length)) {
    return candidate;
  }
  return LATEST_INSTALL_RECEIPT_REF;
}

async function writeReceiptOnce(dependencies, receipt) {
  try {
    return await dependencies.writeReceipt(receipt);
  } catch {
    fail('install_receipt_write_failed');
  }
}

function failureReason(error) {
  const code = error instanceof PersonalInstallError ? error.code : undefined;
  if (typeof code === 'string' && SAFE_REASON_CODE.test(code)) return code;
  return 'install_failed';
}

function failureResult(plan, completedSteps, error) {
  const reasonCode = failureReason(error);
  return terminalResult(plan, {
    completedSteps,
    outcome: ACTION_REQUIRED_CODES.has(reasonCode)
      ? 'action_required'
      : completedSteps.length > 0 ? 'partial' : 'blocked',
    reasonCode,
  });
}

async function verifiedStep({
  inspect,
  mutate,
  completedSteps,
  step,
  verificationCode,
}) {
  const before = await inspect();
  if (before?.ready === true) {
    completedSteps.push(step);
    return { mutated: false, value: before };
  }
  await mutate(before);
  const after = await inspect();
  if (after?.ready !== true) fail(verificationCode);
  completedSteps.push(step);
  return { mutated: true, value: after };
}

export async function runPersonalInstall({
  plan,
  consent = false,
  dependencies,
  mode = 'install',
  resumeEvidence = false,
} = {}) {
  const exact = exactPlan(plan);
  if (!['install', 'repair'].includes(mode) || typeof consent !== 'boolean' ||
      typeof resumeEvidence !== 'boolean') fail('request_invalid');

  if (exact.outcome !== 'ready_to_install') {
    const reasonCode = exact.reason_codes[0] ?? 'preflight_incomplete';
    return terminalResult(exact, {
      outcome: exact.outcome === 'unsupported' ? 'blocked' : 'action_required',
      reasonCode,
    });
  }
  if (!consent) {
    return terminalResult(exact, {
      outcome: 'action_required',
      reasonCode: 'disclosure_consent_required',
    });
  }

  const deps = exactDependencies(dependencies);
  const completedSteps = [];
  let mutated = false;
  try {
    const runtime = await verifiedStep({
      inspect: deps.inspectRuntime,
      mutate: deps.provisionRuntime,
      completedSteps,
      step: STEP.artifacts,
      verificationCode: 'runtime_verification_failed',
    });
    mutated ||= runtime.mutated;
    await emitCheckpoint(deps, exact, completedSteps);

    const presence = await verifiedStep({
      inspect: deps.inspectPresence,
      mutate: deps.installPresence,
      completedSteps,
      step: STEP.presence,
      verificationCode: 'presence_verification_failed',
    });
    mutated ||= presence.mutated;
    await emitCheckpoint(deps, exact, completedSteps);

    let principal = await deps.inspectPrincipal();
    if (!principal) {
      principal = await deps.createPrincipal();
      const verifiedPrincipal = await deps.inspectPrincipal();
      if (!verifiedPrincipal || verifiedPrincipal.principal_id !== principal?.principal_id) {
        fail('principal_verification_failed');
      }
      principal = verifiedPrincipal;
      mutated = true;
    }
    completedSteps.push(STEP.principal);
    await emitCheckpoint(deps, exact, completedSteps);

    let bindingStatus = await deps.inspectBinding({ principal, plan: exact });
    if (bindingStatus?.status === 'conflict' || bindingStatus?.status === 'legacy' ||
        bindingStatus?.status === 'repair_required') {
      const expectedCode = `binding_${bindingStatus.status}`;
      const code = bindingStatus.reason_code === expectedCode ? bindingStatus.reason_code : expectedCode;
      return await emitTerminal(deps, terminalResult(exact, {
        completedSteps,
        outcome: 'action_required',
        reasonCode: code,
      }));
    }
    if (bindingStatus?.ready !== true) {
      await deps.createBinding({ principal, plan: exact });
      bindingStatus = await deps.inspectBinding({ principal, plan: exact });
      if (bindingStatus?.ready !== true) fail('binding_verification_failed');
      mutated = true;
    }
    completedSteps.push(STEP.binding);
    await emitCheckpoint(deps, exact, completedSteps);

    const core = await verifiedStep({
      inspect: () => deps.inspectCore({ binding: bindingStatus.binding, plan: exact }),
      mutate: () => deps.activateCore({ binding: bindingStatus.binding, plan: exact }),
      completedSteps,
      step: STEP.core,
      verificationCode: 'core_activation_verification_failed',
    });
    mutated ||= core.mutated;
    await emitCheckpoint(deps, exact, completedSteps);

    let activation = await deps.inspectActivation({
      binding: bindingStatus.binding, core: core.value, plan: exact,
    });
    const beforeReady = activation?.product_ready === true || activation?.ready === true;
    const hostParityIncomplete = activation?.product_ready === true && activation?.parity !== 'complete';
    if (!beforeReady || hostParityIncomplete) {
      const activated = await deps.activateHosts({
        binding: bindingStatus.binding, core: core.value, plan: exact, activation,
      });
      activation = activated?.product_ready === true || activated?.product_ready === false
        ? activated
        : await deps.inspectActivation({
            binding: bindingStatus.binding, core: core.value, plan: exact,
          });
      mutated = true;
    }
    const activationReady = activation?.product_ready === true || activation?.ready === true;
    if (!activationReady) fail('supported_harness_activation_failed');
    const hostStatus = activation?.product_ready === true
      ? normalizePersonalInstallHostStatus(activation)
      : { product_ready: true, parity: 'complete', hosts: [] };
    completedSteps.push(STEP.harnesses);
    await emitCheckpoint(deps, exact, completedSteps);

    const health = await deps.inspectHealth({
      binding: bindingStatus.binding,
      core: core.value,
      plan: exact,
      activation,
    });
    if (health?.ready !== true || health?.full_retrieval !== true) {
      const reasonCode = health?.reason_code ?? (health?.warming ? 'model_warming' : 'full_retrieval_unavailable');
      if (![
        'presence_required', 'binding_repair_required', 'codex_activation_incomplete',
        'codex_plugin_unavailable', 'codex_hook_lifecycle_required', 'codex_hook_trust_required',
        'codex_native_lifecycle_attestation_unavailable', 'daemon_unavailable',
        'full_retrieval_unavailable', 'local_embedder_warming', 'model_warming',
      ].includes(reasonCode) && !/^(?:claude|codex|cursor|supported_harness)_[a-z0-9_]{1,96}$/.test(reasonCode)) {
        fail('health_status_invalid');
      }
      const outcome = health?.outcome ?? (health?.warming ? 'warming' : 'action_required');
      if (!['warming', 'action_required'].includes(outcome)) fail('health_status_invalid');
      return await emitTerminal(deps, terminalResult(exact, {
        completedSteps,
        hostStatus,
        outcome,
        reasonCode,
      }));
    }
    completedSteps.push(STEP.retrieval);

    let reasonCode = 'installed';
    if (!mutated) reasonCode = 'already_installed';
    else if (resumeEvidence) reasonCode = 'resumed';
    return await emitTerminal(deps, terminalResult(exact, {
      completedSteps,
      hostStatus,
      outcome: 'ready',
      reasonCode,
    }));
  } catch (error) {
    const result = failureResult(exact, completedSteps, error);
    if (result.reason_code === 'install_receipt_write_failed') return result;
    try {
      return await emitTerminal(deps, result);
    } catch (receiptError) {
      if (failureReason(receiptError) !== 'install_receipt_write_failed') throw receiptError;
      return failureResult(exact, completedSteps, receiptError);
    }
  }
}
