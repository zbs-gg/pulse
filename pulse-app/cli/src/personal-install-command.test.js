import assert from 'node:assert/strict';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { acquireInstallLock, writeInstallJournal } from './install-journal.js';
import {
  executePersonalInstallCommand,
  hasMatchingResumableInstallJournal,
  nativePackedFixtureApprovalDigest,
  protectedHarnessApproval,
  requestConsent,
} from './personal-install-command.js';

function sink() {
  let value = '';
  return {
    isTTY: false,
    write(chunk) { value += String(chunk); return true; },
    value() { return value; },
  };
}

function plan(overrides = {}) {
  return {
    schema: 'pulse.personal_install_plan.v2',
    contract_version: 2,
    outcome: 'ready_to_install',
    reason_codes: [],
    current_state: { install_receipt: 'missing' },
    local_writes: [],
    privacy: {
      backend_model_calls: 'off', memory_location: 'local_private_vault',
      old_chat_import: 'not_requested', raw_transcript_capture: 'off',
    },
    required_human_approvals: [],
    resources: {
      disk_free_bytes: null, memory_total_bytes: null, minimum_memory_bytes: 0,
      port_18789: 'unknown', required_disk_bytes: 0,
    },
    rollback: { remove_codex_connection: 'pulse disconnect codex' },
    detected: {
      hosts: [{ host: 'codex', activation_target: true }],
      workspace: {
        canonical_path: '/private/project',
        repository_id: 'repository_test',
        workspace_id: 'workspace_test',
      },
    },
    ...overrides,
  };
}

function resumableRelease(overrides = {}) {
  return {
    epoch: 7,
    manifest_digest: 'a'.repeat(64),
    version: '0.7.0',
    artifacts: [
      { id: 'pulse-0.7.0-plugin-runtime' },
      { id: 'pulse-0.7.0-darwin-arm64-daemon' },
    ],
    ...overrides,
  };
}

function writeResumableJournal(dataDir, release = resumableRelease()) {
  writeInstallJournal(join(dataDir, 'runtime', 'install-journal.json'), {
    artifact_ids: release.artifacts.map((artifact) => artifact.id).sort(),
    manifest_digest: release.manifest_digest,
    phase: 'candidate_staged',
    previous_activation_digest: null,
    release_epoch: release.epoch,
    release_version: release.version,
    schema: 'pulse.personal_install_journal.v1',
  });
}

test('repair reuses one disclosure consent only for the exact resumable release', () => {
  const dataDir = mkdtempSync(join(tmpdir(), 'pulse-personal-command-resume.'));
  try {
    const release = resumableRelease();
    writeResumableJournal(dataDir, release);
    const exactPlan = plan({
      current_state: { install_receipt: 'resumable' },
      release,
    });
    assert.equal(hasMatchingResumableInstallJournal({
      dataDir, mode: 'repair', plan: exactPlan,
    }), true);
    assert.equal(hasMatchingResumableInstallJournal({
      dataDir, mode: 'install', plan: exactPlan,
    }), false);
    assert.equal(hasMatchingResumableInstallJournal({
      dataDir,
      mode: 'repair',
      plan: { ...exactPlan, release: { ...release, manifest_digest: 'b'.repeat(64) } },
    }), false);
  } finally {
    rmSync(dataDir, { recursive: true, force: true });
  }
});

test('a non-TTY agent install uses a project-bound macOS human confirmation without internal IDs', async () => {
  const exactPlan = plan();
  const calls = [];
  const approved = await requestConsent({
    input: { isTTY: false },
    output: { isTTY: false },
    plan: exactPlan,
    platform: 'darwin',
    runDialog: (command, args, options) => {
      calls.push({ command, args, options });
      return { status: 0, stdout: 'button returned:Install\n', stderr: '' };
    },
  });
  assert.equal(approved, true);
  assert.equal(calls[0].command, '/usr/bin/osascript');
  assert.match(calls[0].args[1], /Pulse checked project and is ready/);
  assert.match(calls[0].args[1], /codex/);
  assert.match(calls[0].args[1], /saved automatically.*edited or deleted/i);
  assert.doesNotMatch(calls[0].args[1], /workspace_test|repository_test|Plan:/);

  const denied = await requestConsent({
    input: { isTTY: false }, output: { isTTY: false }, plan: exactPlan, platform: 'darwin',
    runDialog: () => ({ status: 1, stdout: '', stderr: 'User canceled.' }),
  });
  assert.equal(denied, false);
});

test('native packed fixture approval is digest-bound and confined to its disposable root', async () => {
  const root = '/private/tmp/pulse-native-command';
  const cwd = `${root}/workspace`;
  const dataDir = `${root}/data`;
  const home = `${root}/home`;
  const exactPlan = plan({
    detected: {
      hosts: [{ host: 'codex', activation_target: true }],
      workspace: { canonical_path: cwd, repository_id: 'repository_test', workspace_id: 'workspace_test' },
    },
    release: {
      catalog_schema: 'pulse.personal_preview.release_catalog.v2',
      verification_profile: { fixture_id: 'native-darwin-arm64', kind: 'fixture', production: false },
    },
  });
  const env = {
    PULSE_NATIVE_PACKED_FIXTURE_ATTESTATION: '1',
    PULSE_NATIVE_PACKED_FIXTURE_ROOT: root,
    PULSE_NATIVE_PACKED_FIXTURE_PORT: '28789',
    PULSE_NATIVE_PACKED_FIXTURE_APPROVAL: nativePackedFixtureApprovalDigest(exactPlan),
    PULSE_RELEASE_TEST_MODE: '1',
    PULSE_TRUST_MODE: 'test',
    PULSE_DATA_DIR: dataDir,
    PULSE_BINDING_REGISTRY_PATH: `${root}/trust/bindings.json`,
    PULSE_BINDING_PUBLIC_KEY_PATH: `${root}/trust/bindings.pub`,
    PULSE_BINDING_ANCHOR_PATH: `${root}/trust/bindings.anchor`,
    PULSE_RELEASE_MANIFEST_PATH: `${root}/release/manifest.json`,
    PULSE_RELEASE_TEST_ROOT_PATH: `${root}/release/root.pem`,
    PULSE_RELEASE_TEST_ASSET_ROOT: `${root}/release/assets`,
    PULSE_NATIVE_PACKED_FIXTURE_BINDING_KEY_PATH: `${root}/trust/bindings.key`,
  };
  assert.equal(await requestConsent({
    cwd, dataDir, env, home, input: { isTTY: false }, output: { isTTY: false }, plan: exactPlan, platform: 'linux',
  }), true);
  assert.equal(await requestConsent({
    cwd, dataDir, env: { ...env, PULSE_NATIVE_PACKED_FIXTURE_APPROVAL: '0'.repeat(64) }, home,
    input: { isTTY: false }, output: { isTTY: false }, plan: exactPlan, platform: 'linux',
  }), false);
  assert.equal(await requestConsent({
    cwd, dataDir: '/Users/person/.pulse', env, home,
    input: { isTTY: false }, output: { isTTY: false }, plan: exactPlan, platform: 'linux',
  }), false);
  const driftedTelemetry = {
    ...exactPlan,
    resources: { ...exactPlan.resources, disk_free_bytes: 1, memory_total_bytes: 2, port_18789: 'occupied' },
  };
  assert.equal(nativePackedFixtureApprovalDigest(driftedTelemetry), nativePackedFixtureApprovalDigest(exactPlan));
});

test('protected harness approval is exact-plan bound and confined to an ephemeral GitHub root', async (t) => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-protected-harness.'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const cwd = join(root, 'workspace');
  const dataDir = join(root, 'data');
  const home = join(root, 'home');
  const approvalPath = join(root, 'approval.json');
  for (const path of [cwd, dataDir, home]) mkdirSync(path, { mode: 0o700 });
  const exactPlan = plan({
    detected: {
      hosts: [{ host: 'codex', activation_target: true }],
      workspace: { canonical_path: cwd, repository_id: 'repository_test', workspace_id: 'workspace_test' },
    },
  });
  const sourceCommit = 'a'.repeat(40);
  const packageSHA256 = 'b'.repeat(64);
  const approval = protectedHarnessApproval(exactPlan, {
    authority: 'production_candidate', dataDir, packageSHA256, sourceCommit, workspace: cwd,
  });
  writeFileSync(approvalPath, `${JSON.stringify(approval)}\n`, { mode: 0o600 });
  const env = {
    CI: 'true', GITHUB_ACTIONS: 'true', RUNNER_TEMP: root,
    PULSE_PROTECTED_HARNESS_APPROVAL_PATH: approvalPath,
    PULSE_PROTECTED_HARNESS_AUTHORITY: 'production_candidate',
    PULSE_PROTECTED_HARNESS_PACKAGE_SHA256: packageSHA256,
    PULSE_PROTECTED_HARNESS_SOURCE_COMMIT: sourceCommit,
  };
  assert.equal(await requestConsent({
    cwd, dataDir, env, home, input: { isTTY: false }, output: { isTTY: false }, plan: exactPlan, platform: 'linux',
  }), true);
  assert.equal(await requestConsent({
    cwd, dataDir, env: { ...env, PULSE_PROTECTED_HARNESS_PACKAGE_SHA256: 'c'.repeat(64) }, home,
    input: { isTTY: false }, output: { isTTY: false }, plan: exactPlan, platform: 'linux',
  }), false);
  assert.equal(await requestConsent({
    cwd: '/tmp/outside', dataDir, env, home,
    input: { isTTY: false }, output: { isTTY: false }, plan: exactPlan, platform: 'linux',
  }), false);
});

function readyActivation() {
  return {
    product_ready: true,
    parity: 'complete',
    hosts: [{
      host: 'codex', detected: true, compatible: true, installed: true, mcp_ready: true,
      activated: true, verified: true, lifecycle_ready: true, reload_required: false,
      milestones: ['prompt_context', 'session_context', 'write_receipt'], reason_code: 'codex_verified',
    }],
  };
}

test('lock contention returns one stable machine-readable action without invoking installer dependencies', async () => {
  const dataDir = mkdtempSync(join(tmpdir(), 'pulse-personal-command-lock.'));
  const release = acquireInstallLock(join(dataDir, 'runtime', 'personal-install.lock'));
  const stdout = sink();
  const stderr = sink();
  let dependenciesBuilt = false;
  try {
    const executed = await executePersonalInstallCommand({
      argv: ['--json'],
      buildDependencies: () => { dependenciesBuilt = true; throw new Error('must not build'); },
      buildPlan: () => plan(),
      consentPrompt: async () => true,
      dataDir,
      errorOutput: stderr,
      input: { isTTY: false },
      mode: 'install',
      output: stdout,
    });

    assert.equal(executed.exitCode, 1);
    assert.equal(executed.result.outcome, 'action_required');
    assert.equal(executed.result.reason_code, 'install_in_progress');
    assert.equal(dependenciesBuilt, false);
    assert.equal(JSON.parse(stdout.value()).reason_code, 'install_in_progress');
    assert.match(stderr.value(), /Pulse Personal install/);
  } finally {
    release();
    rmSync(dataDir, { recursive: true, force: true });
  }
});

test('preflight failure remains JSON-only on stdout and never asks for consent', async () => {
  const stdout = sink();
  const stderr = sink();
  let prompted = false;
  const executed = await executePersonalInstallCommand({
    argv: ['--json'],
    buildDependencies: () => { throw new Error('must not build'); },
    buildPlan: () => plan({ outcome: 'action_required', reason_codes: ['binding_conflict'] }),
    consentPrompt: async () => { prompted = true; return true; },
    dataDir: '/tmp/pulse-personal-command',
    errorOutput: stderr,
    input: { isTTY: false },
    mode: 'repair',
    output: stdout,
  });

  assert.equal(executed.result.reason_code, 'binding_conflict');
  assert.equal(prompted, false);
  assert.equal(JSON.parse(stdout.value()).schema, 'pulse.personal_install_result.v2');
  assert.doesNotMatch(stdout.value(), /Pulse Personal install/);
});

test('one-command orchestration carries the immutable plan into a healthy install result', async () => {
  const stdout = sink();
  const stderr = sink();
  const expectedPlan = plan();
  let receivedPlan;
  const principal = { principal_id: 'principal_0123456789abcdef0123456789abcdef' };
  const binding = { binding_id: 'binding_test', principal_ref: principal.principal_id };
  const executed = await executePersonalInstallCommand({
    argv: ['--json'],
    buildDependencies: (actualPlan) => {
      receivedPlan = actualPlan;
      return {
        inspectRuntime: async () => ({ ready: true }), provisionRuntime: async () => {},
        inspectPresence: async () => ({ ready: true }), installPresence: async () => {},
        inspectPrincipal: async () => principal, createPrincipal: async () => principal,
        inspectBinding: async () => ({ ready: true, binding }), createBinding: async () => binding,
        inspectCore: async () => ({ ready: true, full_retrieval: true, context: {} }), activateCore: async () => {},
        inspectActivation: async () => readyActivation(), activateHosts: async () => readyActivation(),
        inspectHealth: async () => ({ ready: true, full_retrieval: true }),
        writeReceipt: async () => 'receipt_command_ready',
      };
    },
    buildPlan: () => expectedPlan,
    consentPrompt: async () => true,
    dataDir: '/tmp/pulse-personal-command',
    errorOutput: stderr,
    input: { isTTY: false },
    lock: () => () => {},
    mode: 'install',
    output: stdout,
  });

  assert.equal(receivedPlan, expectedPlan);
  assert.equal(executed.exitCode, 0);
  assert.equal(executed.result.reason_code, 'already_installed');
  assert.equal(executed.result.receipt_ref, 'receipt_command_ready');
  assert.equal(JSON.parse(stdout.value()).outcome, 'ready');
});

test('same-release repair resumes without prompting for disclosure again', async () => {
  const dataDir = mkdtempSync(join(tmpdir(), 'pulse-personal-command-resume.'));
  const stdout = sink();
  const stderr = sink();
  const principal = { principal_id: 'principal_0123456789abcdef0123456789abcdef' };
  const binding = { binding_id: 'binding_test', principal_ref: principal.principal_id };
  const release = resumableRelease();
  writeResumableJournal(dataDir, release);
  let prompted = false;
  try {
    const executed = await executePersonalInstallCommand({
      argv: [],
      buildDependencies: () => ({
        inspectRuntime: async () => ({ ready: true }), provisionRuntime: async () => {},
        inspectPrincipal: async () => principal, createPrincipal: async () => principal,
        inspectBinding: async () => ({ ready: true, binding }), createBinding: async () => binding,
        inspectCore: async () => ({
          ready: true,
          full_retrieval: true,
          context: { edge: { release_manifest_digest: release.manifest_digest } },
        }),
        activateCore: async () => {},
        inspectActivation: async () => readyActivation(), activateHosts: async () => readyActivation(),
        inspectHealth: async () => ({ ready: true, full_retrieval: true }),
        writeReceipt: async () => 'receipt_command_ready',
      }),
      buildPlan: () => plan({
        current_state: { install_receipt: 'resumable' },
        release,
      }),
      consentPrompt: async () => { prompted = true; return false; },
      dataDir,
      errorOutput: stderr,
      input: { isTTY: true },
      lock: () => () => {},
      mode: 'repair',
      output: stdout,
    });

    assert.equal(executed.exitCode, 0);
    assert.equal(executed.result.reason_code, 'already_installed');
    assert.equal(prompted, false);
    assert.match(stdout.value(), /already approved for this exact release/i);
  } finally {
    rmSync(dataDir, { recursive: true, force: true });
  }
});

test('interactive one-command install ends with Continue working and does not force Memory Home', async () => {
  const stdout = sink();
  const stderr = sink();
  const principal = { principal_id: 'principal_0123456789abcdef0123456789abcdef' };
  const binding = { binding_id: 'binding_test', principal_ref: principal.principal_id };
  let lockReleased = false;
  const executed = await executePersonalInstallCommand({
    argv: [],
    buildDependencies: () => ({
      inspectRuntime: async () => ({ ready: true }), provisionRuntime: async () => {},
      inspectPresence: async () => ({ ready: true }), installPresence: async () => {},
      inspectPrincipal: async () => principal, createPrincipal: async () => principal,
      inspectBinding: async () => ({ ready: true, binding }), createBinding: async () => binding,
      inspectCore: async () => ({ ready: true, full_retrieval: true, context: {} }), activateCore: async () => {},
      inspectActivation: async () => readyActivation(), activateHosts: async () => readyActivation(),
      inspectHealth: async () => ({ ready: true, full_retrieval: true }),
      writeReceipt: async () => 'receipt_command_ready',
    }),
    buildPlan: () => plan(),
    consentPrompt: async () => true,
    dataDir: '/tmp/pulse-personal-command',
    errorOutput: stderr,
    input: { isTTY: true },
    lock: () => () => { lockReleased = true; },
    mode: 'install',
    output: stdout,
  });

  assert.equal(executed.exitCode, 0);
  assert.equal(lockReleased, true);
  assert.match(executed.result.next_action, /Continue working/);
  assert.match(executed.result.next_action, /Optional: run pulse home/);
  assert.match(stdout.value(), /Pulse turns useful context.*Memory Home/i);
  assert.match(stdout.value(), /saved automatically/i);
  assert.match(stdout.value(), /Pulse Personal is ready/);
  assert.match(stdout.value(), /Continue working/);
  assert.match(stdout.value(), /Optional: run pulse home/);
  assert.doesNotMatch(stdout.value(), /Memory Home will open/i);
  assert.doesNotMatch(stdout.value(), /workspace_test|repository_test|Current state:|Local writes:|Host parity:/);
});
