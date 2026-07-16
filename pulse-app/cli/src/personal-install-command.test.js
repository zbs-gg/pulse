import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { acquireInstallLock } from './install-journal.js';
import { executePersonalInstallCommand } from './personal-install-command.js';

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
    schema: 'pulse.personal_install_plan.v1',
    contract_version: 1,
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
      workspace: {
        canonical_path: '/private/project',
        repository_id: 'repository_test',
        workspace_id: 'workspace_test',
      },
    },
    ...overrides,
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
  assert.equal(JSON.parse(stdout.value()).schema, 'pulse.personal_install_result.v1');
  assert.doesNotMatch(stdout.value(), /Pulse Personal install/);
});

test('one-command orchestration carries the immutable plan into a healthy install result', async () => {
  const stdout = sink();
  const stderr = sink();
  const expectedPlan = plan();
  let receivedPlan;
  let homeOpened = false;
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
        inspectActivation: async () => ({ ready: true }), activateCodex: async () => {},
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
    openHome: async () => { homeOpened = true; },
    output: stdout,
  });

  assert.equal(receivedPlan, expectedPlan);
  assert.equal(executed.exitCode, 0);
  assert.equal(executed.result.reason_code, 'already_installed');
  assert.equal(executed.result.receipt_ref, 'receipt_command_ready');
  assert.equal(JSON.parse(stdout.value()).outcome, 'ready');
  assert.equal(homeOpened, false, 'machine-readable installs must not open a browser');
});

test('interactive one-command install opens Memory Home after releasing the install lock', async () => {
  const stdout = sink();
  const stderr = sink();
  const principal = { principal_id: 'principal_0123456789abcdef0123456789abcdef' };
  const binding = { binding_id: 'binding_test', principal_ref: principal.principal_id };
  let lockReleased = false;
  let homeOpened = false;
  const executed = await executePersonalInstallCommand({
    argv: [],
    buildDependencies: () => ({
      inspectRuntime: async () => ({ ready: true }), provisionRuntime: async () => {},
      inspectPresence: async () => ({ ready: true }), installPresence: async () => {},
      inspectPrincipal: async () => principal, createPrincipal: async () => principal,
      inspectBinding: async () => ({ ready: true, binding }), createBinding: async () => binding,
      inspectActivation: async () => ({ ready: true }), activateCodex: async () => {},
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
    openHome: async () => {
      assert.equal(lockReleased, true, 'Home must open only after the install transaction unlocks');
      homeOpened = true;
    },
    output: stdout,
  });

  assert.equal(executed.exitCode, 0);
  assert.equal(homeOpened, true);
  assert.match(executed.result.next_action, /Memory Home is opening/);
});
