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

async function requestConsent({ input, output }) {
  if (!input.isTTY || !output.isTTY) return false;
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
  const consent = await consentPrompt({ input, output: json ? errorOutput : output });
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
