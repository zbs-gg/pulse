import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { createInterface } from 'node:readline/promises';
import { join } from 'node:path';

import { acquireInstallLock } from './install-journal.js';
import { formatPersonalInstallPlan } from './install-plan.js';
import { runPersonalInstall } from './personal-install.js';

function writeLine(stream, value = '') {
  stream.write(`${value}\n`);
}

function printResult(result, { json, stdout }) {
  if (json) {
    writeLine(stdout, JSON.stringify(result, null, 2));
    return;
  }
  writeLine(stdout, `\nPulse install result: ${result.outcome} (${result.reason_code})`);
  writeLine(stdout, `Project: ${result.current_project.workspace_id}`);
  writeLine(stdout, `Completed: ${result.completed_steps.length > 0 ? result.completed_steps.join(' -> ') : 'none'}`);
  writeLine(stdout, `Host parity: ${result.host_status?.parity ?? 'blocked'}`);
  for (const host of result.host_status?.hosts ?? []) {
    writeLine(stdout, `  - ${host.host}: ${host.verified ? 'verified' : host.reason_code}`);
  }
  writeLine(stdout, 'Personal vault and signed binding: preserved');
  if (result.receipt_ref) writeLine(stdout, `Receipt: ${result.receipt_ref}`);
  writeLine(stdout, `Next: ${result.next_action}`);
}

export function installPlanApprovalDigest(plan) {
  if (!plan || plan.schema !== 'pulse.personal_install_plan.v2' || plan.contract_version !== 2) {
    throw new TypeError('personal_install_approval_plan_invalid');
  }
  return createHash('sha256')
    .update('pulse-personal-install-approval-v1\x1f')
    .update(JSON.stringify(plan))
    .digest('hex');
}

function appleScriptString(value) {
  return `"${String(value).replaceAll('\\', '\\\\').replaceAll('"', '\\"')}"`;
}

export async function requestConsent({
  input,
  output,
  plan,
  platform = process.platform,
  runDialog = spawnSync,
}) {
  if (!input.isTTY || !output.isTTY) {
    if (platform !== 'darwin') return false;
    const digest = installPlanApprovalDigest(plan);
    const hosts = (plan.detected?.hosts ?? [])
      .filter((host) => host.activation_target === true)
      .map((host) => host.host)
      .join(', ');
    const message = [
      'Install Pulse Personal for this exact project?',
      `Project: ${plan.detected?.workspace?.workspace_id ?? 'unknown'}`,
      `Hosts: ${hosts || 'none'}`,
      `Plan: ${digest.slice(0, 16)}`,
      'Memory stays local. Raw transcript capture and backend model calls stay off.',
    ].join('  ');
    const script = `display dialog ${appleScriptString(message)} buttons {"Cancel", "Install"} default button "Cancel" cancel button "Cancel" with title "Pulse Personal"`;
    const result = runDialog('/usr/bin/osascript', ['-e', script], {
      encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 120_000, killSignal: 'SIGTERM',
    });
    return result?.status === 0 && /button returned:Install/.test(result.stdout ?? '');
  }
  const prompt = createInterface({ input, output });
  try {
    const answer = await prompt.question('\nInstall Pulse Personal for this exact project? [y/N] ');
    return /^(?:y|yes)$/i.test(answer.trim());
  } finally {
    prompt.close();
  }
}

function lockContentionPlan(plan) {
  return { ...plan, outcome: 'action_required', reason_codes: ['install_in_progress'] };
}

export async function executePersonalInstallCommand({
  argv = [],
  buildDependencies,
  buildPlan,
  consentPrompt = requestConsent,
  dataDir,
  input = process.stdin,
  lock = acquireInstallLock,
  mode,
  openHome = async () => {},
  output = process.stdout,
  errorOutput = process.stderr,
} = {}) {
  if (!['install', 'repair'].includes(mode) || !Array.isArray(argv) ||
      typeof buildDependencies !== 'function' || typeof buildPlan !== 'function' ||
      typeof consentPrompt !== 'function' ||
      typeof dataDir !== 'string' || !input ||
      typeof output?.write !== 'function' || typeof errorOutput?.write !== 'function' ||
      typeof lock !== 'function' || typeof openHome !== 'function') {
    throw new TypeError('personal_install_command_invalid');
  }
  const json = argv.includes('--json');
  const plan = buildPlan();
  if (json) writeLine(errorOutput, formatPersonalInstallPlan(plan));
  else writeLine(output, formatPersonalInstallPlan(plan));

  if (plan.outcome !== 'ready_to_install') {
    const result = await runPersonalInstall({ plan, consent: false, mode });
    printResult(result, { json, stdout: output });
    return { exitCode: 1, result };
  }
  if (argv.includes('--yes')) {
    writeLine(json ? errorOutput : output,
      '\n[pulse] --yes cannot approve disclosure, macOS presence, binding replacement, hook trust, downgrade, or wipe.');
  }
  const consent = await consentPrompt({ input, output: json ? errorOutput : output, plan });
  if (!consent) {
    const result = await runPersonalInstall({ plan, consent: false, mode });
    printResult(result, { json, stdout: output });
    return { exitCode: 1, result };
  }

  let releaseLock;
  try {
    releaseLock = lock(join(dataDir, 'runtime', 'personal-install.lock'));
  } catch (error) {
    if (error?.code !== 'install_locked') throw error;
    const result = await runPersonalInstall({
      plan: lockContentionPlan(plan), consent: false, mode,
    });
    printResult(result, { json, stdout: output });
    return { exitCode: 1, result };
  }
  let result;
  try {
    result = await runPersonalInstall({
      plan,
      consent: true,
      mode,
      dependencies: buildDependencies(plan),
      resumeEvidence: plan.current_state.install_receipt === 'resumable',
    });
    printResult(result, { json, stdout: output });
  } finally {
    releaseLock();
  }
  if (result.outcome === 'ready' && !json) await openHome();
  return { exitCode: result.outcome === 'ready' ? 0 : 1, result };
}
