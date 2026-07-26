import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { homedir } from 'node:os';
import { basename, join } from 'node:path';
import { createInterface } from 'node:readline/promises';

import { acquireInstallLock, readInstallJournal } from './install-journal.js';
import {
  canonicalInstallPlanJSON,
  formatPersonalInstallIntroduction,
  formatPersonalInstallPlan,
} from './install-plan.js';
import { nativePackedFixtureAttestation } from './native-packed-fixture.js';
import { runPersonalInstall } from './personal-install.js';

function writeLine(stream, value = '') {
  stream.write(`${value}\n`);
}

function printResult(result, { json, stdout }) {
  if (json) {
    writeLine(stdout, JSON.stringify(result, null, 2));
    return;
  }
  if (result.outcome === 'ready') {
    writeLine(stdout, '\nPulse Personal is ready.');
    writeLine(stdout, 'Continue working in your AI app. Pulse will remember automatically.');
    writeLine(stdout, 'Optional: run pulse home to inspect or edit memory.');
  } else {
    writeLine(stdout, `\nPulse Personal could not finish (${result.reason_code}).`);
    writeLine(stdout, result.completed_steps.length > 0
      ? `Completed safely: ${result.completed_steps.join(' -> ')}`
      : 'No install step completed.');
    writeLine(stdout, 'Existing Personal memory and project bindings were preserved.');
    writeLine(stdout, `Next: ${result.next_action}`);
    writeLine(stdout, 'Technical details: pulse install-plan --json');
  }
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

export function nativePackedFixtureApprovalDigest(plan) {
  if (!plan || plan.schema !== 'pulse.personal_install_plan.v2' || plan.contract_version !== 2) {
    throw new TypeError('personal_install_approval_plan_invalid');
  }
  const stablePlan = {
    schema: plan.schema,
    contract_version: plan.contract_version,
    outcome: plan.outcome,
    reason_codes: plan.reason_codes ?? null,
    current_state: plan.current_state ?? null,
    detected: plan.detected ?? null,
    release: plan.release ?? null,
    local_writes: plan.local_writes ?? null,
    privacy: plan.privacy ?? null,
    authority_profile: plan.authority_profile ?? null,
    protected_actions: plan.protected_actions ?? null,
    required_human_approvals: plan.required_human_approvals ?? null,
    rollback: plan.rollback ?? null,
    resources: {
      minimum_memory_bytes: plan.resources?.minimum_memory_bytes ?? null,
      required_disk_bytes: plan.resources?.required_disk_bytes ?? null,
    },
  };
  return createHash('sha256')
    .update('pulse-native-packed-fixture-approval-v1\x1f')
    .update(canonicalInstallPlanJSON(stablePlan))
    .digest('hex');
}

function appleScriptString(value) {
  return `"${String(value).replaceAll('\\', '\\\\').replaceAll('"', '\\"')}"`;
}

export async function requestConsent({
  cwd,
  dataDir,
  env = process.env,
  home = homedir(),
  input,
  output,
  plan,
  platform = process.platform,
  runDialog = spawnSync,
}) {
  const fixture = nativePackedFixtureAttestation({
    cwd: cwd ?? plan?.detected?.workspace?.canonical_path,
    dataDir: dataDir ?? env.PULSE_DATA_DIR,
    env,
    home,
    plan,
  });
  if (fixture && env.PULSE_NATIVE_PACKED_FIXTURE_APPROVAL === nativePackedFixtureApprovalDigest(plan)) return true;
  if (!input.isTTY || !output.isTTY) {
    if (platform !== 'darwin') return false;
    const hosts = (plan.detected?.hosts ?? [])
      .filter((host) => host.activation_target === true)
      .map((host) => host.host)
      .join(', ');
    const project = basename(plan.detected?.workspace?.canonical_path ?? 'this project');
    const message = [
      `Pulse checked ${project} and is ready.`,
      `It will connect ${hosts || 'your compatible AI app'} automatically.`,
      'Useful structured memory is saved automatically and can be edited or deleted in Memory Home.',
      'No old-chat import, raw transcript storage, or paid model API calls.',
    ].join('  ');
    const script = `display dialog ${appleScriptString(message)} buttons {"Cancel", "Install"} default button "Cancel" cancel button "Cancel" with title "Pulse Personal"`;
    const result = runDialog('/usr/bin/osascript', ['-e', script], {
      encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 120_000, killSignal: 'SIGTERM',
    });
    return result?.status === 0 && /button returned:Install/.test(result.stdout ?? '');
  }
  const prompt = createInterface({ input, output });
  try {
    const answer = await prompt.question('\nInstall Pulse for this project? [y/N] ');
    return /^(?:y|yes)$/i.test(answer.trim());
  } finally {
    prompt.close();
  }
}

function lockContentionPlan(plan) {
  return { ...plan, outcome: 'action_required', reason_codes: ['install_in_progress'] };
}

export function hasMatchingResumableInstallJournal({
  dataDir,
  mode,
  plan,
  readJournal = readInstallJournal,
} = {}) {
  if (mode !== 'repair' || typeof dataDir !== 'string' ||
      plan?.current_state?.install_receipt !== 'resumable' ||
      typeof readJournal !== 'function') return false;
  const release = plan.release;
  if (!release || typeof release.manifest_digest !== 'string' ||
      !/^[a-f0-9]{64}$/.test(release.manifest_digest) ||
      typeof release.version !== 'string' ||
      !Number.isSafeInteger(release.epoch) || release.epoch < 1 ||
      !Array.isArray(release.artifacts)) return false;
  const expectedArtifactIDs = release.artifacts
    .map((artifact) => artifact?.id)
    .filter((id) => typeof id === 'string')
    .sort();
  if (expectedArtifactIDs.length !== release.artifacts.length) return false;
  try {
    const journal = readJournal(join(dataDir, 'runtime', 'install-journal.json'));
    return journal !== null &&
      journal.manifest_digest === release.manifest_digest &&
      journal.release_version === release.version &&
      journal.release_epoch === release.epoch &&
      journal.artifact_ids.join('\0') === expectedArtifactIDs.join('\0');
  } catch {
    return false;
  }
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
  output = process.stdout,
  errorOutput = process.stderr,
} = {}) {
  if (!['install', 'repair'].includes(mode) || !Array.isArray(argv) ||
      typeof buildDependencies !== 'function' || typeof buildPlan !== 'function' ||
      typeof consentPrompt !== 'function' ||
      typeof dataDir !== 'string' || !input ||
      typeof output?.write !== 'function' || typeof errorOutput?.write !== 'function' ||
      typeof lock !== 'function') {
    throw new TypeError('personal_install_command_invalid');
  }
  const json = argv.includes('--json');
  const plan = buildPlan();
  if (json) writeLine(errorOutput, formatPersonalInstallPlan(plan));
  else writeLine(output, formatPersonalInstallIntroduction(plan));

  if (plan.outcome !== 'ready_to_install') {
    const result = await runPersonalInstall({ plan, consent: false, mode });
    printResult(result, { json, stdout: output });
    return { exitCode: 1, result };
  }
  const resumeApproved = hasMatchingResumableInstallJournal({ dataDir, mode, plan });
  if (argv.includes('--yes') && !resumeApproved) {
    writeLine(json ? errorOutput : output,
      '\n[pulse] --yes cannot approve disclosure, macOS presence, binding replacement, hook trust, downgrade, or wipe.');
  }
  if (resumeApproved && !json) {
    writeLine(output, '\nResuming the installation already approved for this exact release.');
  }
  const consent = resumeApproved ||
    await consentPrompt({ input, output: json ? errorOutput : output, plan });
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
  return { exitCode: result.outcome === 'ready' ? 0 : 1, result };
}
