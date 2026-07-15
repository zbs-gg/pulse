import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  PERSONAL_INSTALL_STEPS,
  PersonalInstallError,
  runPersonalInstall,
  writePersonalInstallReceipt,
} from './personal-install.js';

function supportedPlan(overrides = {}) {
  return {
    schema: 'pulse.personal_install_plan.v1',
    contract_version: 1,
    outcome: 'ready_to_install',
    reason_codes: [],
    detected: {
      workspace: {
        workspace_id: 'workspace_test',
        repository_id: 'repository_test',
        canonical_path: '/private/project',
        checkout_kind: 'primary',
      },
    },
    ...overrides,
  };
}

function harness(overrides = {}) {
  const state = {
    runtime: false,
    presence: false,
    principal: null,
    binding: null,
    activation: false,
    health: false,
  };
  const mutations = [];
  const receipts = [];
  const dependencies = {
    inspectRuntime: async () => ({ ready: state.runtime }),
    provisionRuntime: async () => {
      mutations.push('runtime');
      state.runtime = true;
      return { manifest_digest: 'a'.repeat(64), release_epoch: 1 };
    },
    inspectPresence: async () => ({ ready: state.presence, status: state.presence ? 'ready' : 'not_installed' }),
    installPresence: async () => {
      mutations.push('presence');
      state.presence = true;
    },
    inspectPrincipal: async () => state.principal,
    createPrincipal: async () => {
      mutations.push('principal');
      state.principal = { principal_id: 'principal_0123456789abcdef0123456789abcdef' };
      return state.principal;
    },
    inspectBinding: async ({ principal }) => state.binding && state.binding.principal_ref === principal.principal_id
      ? { ready: true, binding: state.binding }
      : { ready: false, status: 'missing' },
    createBinding: async ({ principal }) => {
      mutations.push('binding');
      state.binding = { binding_id: 'binding_test', principal_ref: principal.principal_id };
      return state.binding;
    },
    inspectActivation: async () => ({ ready: state.activation }),
    activateCodex: async () => {
      mutations.push('activation');
      state.activation = true;
      state.health = true;
    },
    inspectHealth: async () => ({ ready: state.health, full_retrieval: state.health }),
    writeReceipt: async (receipt) => { receipts.push(structuredClone(receipt)); },
    ...overrides,
  };
  return { state, mutations, receipts, dependencies };
}

test('cancel before disclosure consent performs no product mutation or journal write', async () => {
  const run = harness();
  const result = await runPersonalInstall({
    plan: supportedPlan(),
    consent: false,
    dependencies: run.dependencies,
  });

  assert.equal(result.outcome, 'action_required');
  assert.equal(result.reason_code, 'disclosure_consent_required');
  assert.deepEqual(result.completed_steps, []);
  assert.deepEqual(run.mutations, []);
  assert.deepEqual(run.receipts, []);
});

test('unsupported preflight fails before consent and mutation with one next action', async () => {
  const run = harness();
  const result = await runPersonalInstall({
    plan: supportedPlan({ outcome: 'unsupported', reason_codes: ['platform_unsupported'] }),
    consent: true,
    dependencies: run.dependencies,
  });

  assert.equal(result.outcome, 'blocked');
  assert.equal(result.reason_code, 'platform_unsupported');
  assert.equal(typeof result.next_action, 'string');
  assert.deepEqual(run.mutations, []);
  assert.deepEqual(run.receipts, []);
});

test('an unmet Codex or workspace prerequisite is action-required before consent and mutation', async () => {
  const run = harness();
  const result = await runPersonalInstall({
    plan: supportedPlan({ outcome: 'action_required', reason_codes: ['codex_missing'] }),
    consent: true,
    dependencies: run.dependencies,
  });

  assert.equal(result.outcome, 'action_required');
  assert.equal(result.reason_code, 'codex_missing');
  assert.deepEqual(run.mutations, []);
  assert.deepEqual(run.receipts, []);
});

test('new install rechecks every durable fact and executes the security ordering', async () => {
  const run = harness();
  const result = await runPersonalInstall({
    plan: supportedPlan(),
    consent: true,
    dependencies: run.dependencies,
  });

  assert.equal(result.outcome, 'ready');
  assert.equal(result.reason_code, 'installed');
  assert.deepEqual(run.mutations, ['runtime', 'presence', 'principal', 'binding', 'activation']);
  assert.deepEqual(result.completed_steps, PERSONAL_INSTALL_STEPS);
  assert.equal(result.preserved_data, true);
  assert.equal(result.receipt_ref, 'pulse://receipts/install/latest');
  assert.equal(run.receipts.at(-1).outcome, 'ready');
  assert.equal(run.receipts.at(-1).reason_code, 'installed');
  assert.deepEqual(
    run.receipts.slice(0, -1).map((receipt) => receipt.reason_code),
    PERSONAL_INSTALL_STEPS.slice(0, -1).map((step) => `checkpoint_${step}`),
  );
});

test('already healthy install is idempotent and finishes with an already-installed receipt', async () => {
  const run = harness();
  run.state.runtime = true;
  run.state.presence = true;
  run.state.principal = { principal_id: 'principal_0123456789abcdef0123456789abcdef' };
  run.state.binding = { binding_id: 'binding_test', principal_ref: run.state.principal.principal_id };
  run.state.activation = true;
  run.state.health = true;

  const result = await runPersonalInstall({
    plan: supportedPlan(),
    consent: true,
    resumeEvidence: true,
    dependencies: run.dependencies,
  });

  assert.equal(result.outcome, 'ready');
  assert.equal(result.reason_code, 'already_installed');
  assert.deepEqual(run.mutations, []);
  assert.deepEqual(result.completed_steps, PERSONAL_INSTALL_STEPS);
  assert.equal(run.receipts.at(-1).reason_code, 'already_installed');
});

test('reused prerequisites do not label a fresh install as resumed', async () => {
  const run = harness();
  run.state.runtime = true;
  run.state.presence = true;
  const result = await runPersonalInstall({
    plan: supportedPlan(),
    consent: true,
    mode: 'repair',
    dependencies: run.dependencies,
  });

  assert.equal(result.outcome, 'ready');
  assert.equal(result.reason_code, 'installed');
  assert.deepEqual(run.mutations, ['principal', 'binding', 'activation']);
});

test('resume label requires explicit durable resume evidence and still rechecks facts', async () => {
  const run = harness();
  run.state.runtime = true;
  run.state.presence = true;
  const result = await runPersonalInstall({
    plan: supportedPlan(),
    consent: true,
    resumeEvidence: true,
    dependencies: run.dependencies,
  });

  assert.equal(result.outcome, 'ready');
  assert.equal(result.reason_code, 'resumed');
  assert.deepEqual(run.mutations, ['principal', 'binding', 'activation']);
});

test('a fresh process resumes from durable inspected facts without repeating the runtime mutation', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-personal-restart.'));
  const statePath = join(root, 'state.json');
  const receiptPath = join(root, 'receipt.json');
  const vaultPath = join(root, 'vault.marker');
  writeFileSync(vaultPath, 'private-memory-survives\n', { mode: 0o600 });
  const runner = String.raw`
    import fs from 'node:fs';
    const { runPersonalInstall } = await import(process.env.PULSE_INSTALL_MODULE);
    const statePath = process.env.PULSE_STATE_PATH;
    const receiptPath = process.env.PULSE_RECEIPT_PATH;
    const interrupted = process.env.PULSE_INTERRUPT === '1';
    const initial = { runtime: false, presence: false, principal: null, binding: null, activation: false, health: false, runtime_mutations: 0 };
    const state = fs.existsSync(statePath) ? JSON.parse(fs.readFileSync(statePath, 'utf8')) : initial;
    const save = () => fs.writeFileSync(statePath, JSON.stringify(state), { mode: 0o600 });
    const plan = { schema: 'pulse.personal_install_plan.v1', contract_version: 1, outcome: 'ready_to_install', reason_codes: [], detected: { workspace: { workspace_id: 'workspace_test', repository_id: 'repository_test', canonical_path: '/private/project' } } };
    const dependencies = {
      inspectRuntime: async () => ({ ready: state.runtime }),
      provisionRuntime: async () => { state.runtime = true; state.runtime_mutations += 1; save(); },
      inspectPresence: async () => ({ ready: state.presence }),
      installPresence: async () => { state.presence = true; save(); },
      inspectPrincipal: async () => state.principal,
      createPrincipal: async () => { state.principal = { principal_id: 'principal_0123456789abcdef0123456789abcdef' }; save(); return state.principal; },
      inspectBinding: async () => state.binding ? { ready: true, binding: state.binding } : { ready: false, status: 'missing' },
      createBinding: async ({ principal }) => { state.binding = { binding_id: 'binding_test', principal_ref: principal.principal_id }; save(); },
      inspectActivation: async () => ({ ready: state.activation }),
      activateCodex: async () => { state.activation = true; state.health = true; save(); },
      inspectHealth: async () => ({ ready: state.health, full_retrieval: state.health }),
      writeReceipt: async (receipt) => {
        fs.writeFileSync(receiptPath, JSON.stringify(receipt), { mode: 0o600 });
        if (interrupted && receipt.reason_code === 'checkpoint_artifacts_staged') process.exit(73);
        return 'receipt_restart_test';
      },
    };
    const prior = fs.existsSync(receiptPath) ? JSON.parse(fs.readFileSync(receiptPath, 'utf8')) : null;
    const result = await runPersonalInstall({ plan, consent: true, resumeEvidence: prior?.outcome === 'partial', dependencies });
    process.stdout.write(JSON.stringify(result));
  `;
  const environment = {
    ...process.env,
    PULSE_INSTALL_MODULE: new URL('./personal-install.js', import.meta.url).href,
    PULSE_STATE_PATH: statePath,
    PULSE_RECEIPT_PATH: receiptPath,
  };
  try {
    const interrupted = spawnSync(process.execPath, ['--input-type=module', '--eval', runner], {
      env: { ...environment, PULSE_INTERRUPT: '1' }, encoding: 'utf8',
    });
    assert.equal(interrupted.status, 73, interrupted.stderr);
    assert.equal(JSON.parse(readFileSync(statePath, 'utf8')).runtime_mutations, 1);
    assert.equal(JSON.parse(readFileSync(receiptPath, 'utf8')).reason_code, 'checkpoint_artifacts_staged');

    const resumed = spawnSync(process.execPath, ['--input-type=module', '--eval', runner], {
      env: environment, encoding: 'utf8',
    });
    assert.equal(resumed.status, 0, resumed.stderr);
    const result = JSON.parse(resumed.stdout);
    assert.equal(result.outcome, 'ready');
    assert.equal(result.reason_code, 'resumed');
    assert.equal(result.receipt_ref, 'receipt_restart_test');
    assert.equal(JSON.parse(readFileSync(statePath, 'utf8')).runtime_mutations, 1);
    assert.equal(readFileSync(vaultPath, 'utf8'), 'private-memory-survives\n');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('terminal result exposes a safe writer reference and rejects path-like references', async () => {
  const safe = harness();
  safe.dependencies.writeReceipt = async (receipt) => {
    safe.receipts.push(structuredClone(receipt));
    return 'receipt_install_test';
  };
  const safeResult = await runPersonalInstall({
    plan: supportedPlan(), consent: true, dependencies: safe.dependencies,
  });
  assert.equal(safeResult.receipt_ref, 'receipt_install_test');

  const pathLike = harness();
  pathLike.dependencies.writeReceipt = async (receipt) => {
    pathLike.receipts.push(structuredClone(receipt));
    return '/Users/person/.pulse/receipts/install/workspace_test.json';
  };
  const pathResult = await runPersonalInstall({
    plan: supportedPlan(), consent: true, dependencies: pathLike.dependencies,
  });
  assert.equal(pathResult.receipt_ref, 'pulse://receipts/install/latest');
});

test('presence cancellation stops before identity or binding mutation', async () => {
  const run = harness({
    installPresence: async () => {
      run.mutations.push('presence_attempt');
      throw new PersonalInstallError('presence_denied');
    },
  });
  const result = await runPersonalInstall({
    plan: supportedPlan(),
    consent: true,
    dependencies: run.dependencies,
  });

  assert.equal(result.outcome, 'action_required');
  assert.equal(result.reason_code, 'presence_denied');
  assert.deepEqual(run.mutations, ['runtime', 'presence_attempt']);
  assert.deepEqual(result.completed_steps, ['artifacts_staged']);
  assert.equal(run.receipts.at(-1).outcome, 'action_required');
});

test('healthy retrieval with incomplete activation reports the exact activation reason', async () => {
  const run = harness({
    inspectHealth: async () => ({
      ready: false, full_retrieval: true, reason_code: 'codex_activation_incomplete',
    }),
  });
  const result = await runPersonalInstall({
    plan: supportedPlan(), consent: true, dependencies: run.dependencies,
  });

  assert.equal(result.outcome, 'action_required');
  assert.equal(result.reason_code, 'codex_activation_incomplete');
  assert.match(result.next_action, /pulse doctor codex --json/);
  assert.equal(run.receipts.at(-1).reason_code, 'codex_activation_incomplete');
});

test('unsafe bindings stop on an existing read-only review action without replacement or repair recursion', async () => {
  for (const status of ['conflict', 'legacy', 'repair_required']) {
    const run = harness({
      inspectBinding: async () => ({ ready: false, status, reason_code: `binding_${status}` }),
    });
    const result = await runPersonalInstall({
      plan: supportedPlan(),
      consent: true,
      mode: 'repair',
      dependencies: run.dependencies,
    });

    assert.equal(result.outcome, 'action_required');
    assert.equal(result.reason_code, `binding_${status}`);
    assert.match(result.next_action, /pulse install-plan --json/);
    assert.doesNotMatch(result.next_action, /pulse repair/);
    assert.deepEqual(run.mutations, ['runtime', 'presence', 'principal']);
    assert.equal(run.mutations.includes('activation'), false);
  }
});

test('principal repair stops on read-only plan inspection without rerunning repair', async () => {
  const run = harness({
    inspectPrincipal: async () => { throw new PersonalInstallError('principal_repair_required'); },
  });
  const result = await runPersonalInstall({
    plan: supportedPlan(), consent: true, mode: 'repair', dependencies: run.dependencies,
  });

  assert.equal(result.outcome, 'action_required');
  assert.equal(result.reason_code, 'principal_repair_required');
  assert.match(result.next_action, /pulse install-plan --json/);
  assert.doesNotMatch(result.next_action, /pulse repair/);
});

test('verification failure after mutation returns a stable blocked result and durable terminal receipt', async () => {
  const run = harness({
    provisionRuntime: async () => { run.mutations.push('runtime'); },
  });
  const result = await runPersonalInstall({
    plan: supportedPlan(), consent: true, dependencies: run.dependencies,
  });

  assert.equal(result.outcome, 'blocked');
  assert.equal(result.reason_code, 'runtime_verification_failed');
  assert.deepEqual(result.completed_steps, []);
  assert.deepEqual(run.mutations, ['runtime']);
  assert.equal(run.receipts.length, 1);
  assert.equal(run.receipts[0].reason_code, 'runtime_verification_failed');
  assert.equal(result.receipt_ref, 'pulse://receipts/install/latest');
});

test('generic failure after a completed step is sanitized and writes one terminal partial receipt', async () => {
  const run = harness({
    installPresence: async () => {
      run.mutations.push('presence_attempt');
      throw new Error('secret path: /Users/person/private');
    },
  });
  const result = await runPersonalInstall({
    plan: supportedPlan(), consent: true, dependencies: run.dependencies,
  });

  assert.equal(result.outcome, 'partial');
  assert.equal(result.reason_code, 'install_failed');
  assert.deepEqual(result.completed_steps, ['artifacts_staged']);
  assert.equal(run.receipts.filter((receipt) => receipt.reason_code === 'install_failed').length, 1);
  assert.doesNotMatch(`${result.reason_code} ${result.next_action}`, /\/Users|private|secret/i);
});

test('receipt writer failure returns a stable partial result without retrying the writer', async () => {
  const run = harness();
  let attempts = 0;
  run.dependencies.writeReceipt = async () => {
    attempts += 1;
    throw new Error('disk failure');
  };
  const result = await runPersonalInstall({
    plan: supportedPlan(), consent: true, dependencies: run.dependencies,
  });

  assert.equal(result.outcome, 'partial');
  assert.equal(result.reason_code, 'install_receipt_write_failed');
  assert.deepEqual(result.completed_steps, ['artifacts_staged']);
  assert.equal('receipt_ref' in result, false);
  assert.equal(attempts, 1);
});

test('terminal install receipt is durable, private, path-free, and content-free', () => {
  const dataDir = mkdtempSync(join(tmpdir(), 'pulse-personal-install-receipt.'));
  try {
    const path = writePersonalInstallReceipt({
      schema: 'pulse.personal_install_receipt.v1',
      outcome: 'ready',
      reason_code: 'installed',
      completed_steps: [...PERSONAL_INSTALL_STEPS],
      workspace_id: 'workspace_test',
      repository_id: 'repository_test',
      preserved_data: true,
    }, { dataDir, now: new Date('2026-07-15T10:00:00.000Z') });
    assert.equal(existsSync(path), true);
    assert.equal(statSync(path).mode & 0o777, 0o600);
    const bytes = readFileSync(path, 'utf8');
    assert.match(bytes, /pulse\.personal_install_receipt\.v1/);
    assert.doesNotMatch(bytes, /\/Users\/|prompt|transcript|secret/i);
    assert.equal(JSON.parse(bytes).created_at, '2026-07-15T10:00:00.000Z');
  } finally {
    rmSync(dataDir, { recursive: true, force: true });
  }
});

test('receipt writer rejects an unsafe existing directory without repairing it silently', () => {
  const dataDir = mkdtempSync(join(tmpdir(), 'pulse-personal-install-unsafe.'));
  const receipts = join(dataDir, 'receipts');
  mkdirSync(receipts, { mode: 0o755 });
  chmodSync(receipts, 0o755);
  try {
    assert.throws(() => writePersonalInstallReceipt({
      schema: 'pulse.personal_install_receipt.v1', outcome: 'ready', reason_code: 'installed',
      completed_steps: [...PERSONAL_INSTALL_STEPS], workspace_id: 'workspace_test',
      repository_id: 'repository_test', preserved_data: true,
    }, { dataDir }), /install_receipt_directory_unsafe/);
    assert.equal(statSync(receipts).mode & 0o777, 0o755);
  } finally {
    rmSync(dataDir, { recursive: true, force: true });
  }
});
