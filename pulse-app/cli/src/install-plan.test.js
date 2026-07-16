import assert from 'node:assert/strict';
import {
  chmodSync, mkdirSync, mkdtempSync, readdirSync, realpathSync, rmSync, symlinkSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

import {
  buildPersonalInstallPlan,
  canonicalInstallPlanJSON,
  detectCodexCLI,
  formatPersonalInstallPlan,
} from './install-plan.js';

function git(cwd, ...args) {
  const result = spawnSync('/usr/bin/git', args, { cwd, encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
}

function repository(root, name = 'project') {
  const path = join(root, name);
  mkdirSync(path, { recursive: true });
  git(path, 'init', '-q');
  git(path, 'config', 'user.email', 'pulse-tests@example.test');
  git(path, 'config', 'user.name', 'Pulse Tests');
  git(path, 'commit', '--allow-empty', '-q', '-m', 'fixture');
  return path;
}

const codexReady = () => ({ available: true, version: '1.2.3', reason_code: null });
const hostMissing = (host) => () => ({ available: false, version: null, reason_code: `${host}_missing` });
const claudeReady = () => ({ available: true, version: '2.1.196', reason_code: null });
const cursorReady = () => ({ available: true, reason_code: null });

function verifiedRelease() {
  return {
    schema: 'pulse.verified_release_manifest.v1',
    version: '0.7.0',
    epoch: 7,
    manifest_digest: 'a'.repeat(64),
    artifacts: Object.fromEntries([
      ['daemon', 10],
      ['embedder-runtime', 20],
      ['model', 30],
      ['plugin-runtime', 40],
      ['presence-helper', 50],
    ].map(([kind, bytes]) => [kind, {
      bytes,
      id: `pulse-${kind}`,
      origin: kind === 'model' ? 'https://models.zbs.gg' : 'https://releases.zbs.gg',
      url: `${kind === 'model' ? 'https://models.zbs.gg' : 'https://releases.zbs.gg'}/${kind}`,
    }])),
  };
}

const ampleResources = {
  disk_free_bytes: 20 * 1024 ** 3,
  memory_total_bytes: 16 * 1024 ** 3,
  port_18789: 'free',
};

const cleanState = {
  binding: 'missing', daemon: 'missing', embedder: 'missing', hook_trust: 'missing',
  install_receipt: 'missing', plugin: 'missing', presence: 'not_installed',
  principal: 'missing', runtime: 'missing', vault: 'missing',
};

test('supported singleton-host Stage 1 plans are stable, explicit, and have no Go or Python requirement', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-install-plan.'));
  try {
    const home = join(root, 'home');
    const cwd = repository(root);
    mkdirSync(home);
    const before = readdirSync(home);
    const plans = [
      ['claude-code', { detectClaude: claudeReady, detectCodex: hostMissing('codex'), detectCursor: hostMissing('cursor') }],
      ['codex', { detectClaude: hostMissing('claude'), detectCodex: codexReady, detectCursor: hostMissing('cursor') }],
      ['cursor', { detectClaude: hostMissing('claude'), detectCodex: hostMissing('codex'), detectCursor: cursorReady }],
    ].map(([host, detectors]) => [host, buildPersonalInstallPlan({
      cwd, home, codexHome: join(home, '.codex'), platform: 'darwin', architecture: 'arm64',
      nodeVersion: '20.18.0', ...detectors,
      release: verifiedRelease(), detectResources: () => ampleResources, currentState: cleanState,
    })]);
    const plan = plans[1][1];

    assert.equal(plan.schema, 'pulse.personal_install_plan.v2');
    assert.equal(plan.contract_version, 2);
    assert.equal(plan.stage, 'personal_stage_1');
    assert.equal('target_host' in plan, false);
    assert.deepEqual(plan.supported_hosts, ['claude-code', 'codex', 'cursor']);
    assert.equal(plan.activation_policy, 'all_detected_supported_hosts');
    assert.equal(plan.outcome, 'ready_to_install');
    assert.deepEqual(plan.reason_codes, []);
    assert.equal(plan.detected.workspace.checkout_kind, 'primary');
    assert.equal(plan.detected.codex.version, '1.2.3');
    for (const [host, singleton] of plans) {
      assert.equal(singleton.outcome, 'ready_to_install', host);
      assert.deepEqual(singleton.detected.hosts.filter((entry) => entry.activation_target).map((entry) => entry.host), [host]);
      assert.deepEqual(singleton.reason_codes, []);
    }
    assert.equal(plan.release.total_download_bytes, 150);
    assert.equal(plan.release.artifacts.length, 5);
    assert.deepEqual(plan.release.origins, ['https://models.zbs.gg', 'https://releases.zbs.gg']);
    assert.equal(plan.resources.disk_free_bytes, ampleResources.disk_free_bytes);
    assert.equal(plan.resources.memory_total_bytes, ampleResources.memory_total_bytes);
    assert.equal(plan.resources.port_18789, 'free');
    assert.deepEqual(plan.current_state, cleanState);
    assert.equal(plan.privacy.raw_transcript_capture, 'off');
    assert.equal(plan.privacy.backend_model_calls, 'off');
    assert.ok(plan.local_writes.some((entry) => entry.path === join(home, '.pulse', 'identity', 'personal-principal.json')));
    assert.ok(plan.local_writes.some((entry) => entry.path === join(home, '.pulse', 'vaults', 'personal', 'store_personal_<generated>') && entry.preserved_on_uninstall));
    assert.deepEqual(
      plan.network_effects.find((entry) => entry.code === 'verified_release_downloads').destinations,
      ['https://models.zbs.gg', 'https://releases.zbs.gg'],
    );
    assert.ok(plan.required_human_approvals.every((entry) => entry.automatable_by_yes === false));
    assert.deepEqual(plan.next_action, {
      code: 'approve_install_disclosure',
      command: 'pulse install',
      requires_human_approval: true,
    });
    assert.equal(plan.rollback.preserve_vault, true);
    assert.equal(plan.rollback.runtime_uninstall, 'unavailable_in_u3');
    assert.equal(plan.rollback.remove_runtime, null);
    assert.doesNotMatch(canonicalInstallPlanJSON(plan), /Go|Python|go_toolchain|python/i);
    assert.deepEqual(readdirSync(home), before, 'plan detection must not create Pulse or Codex state');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('unsupported machine and no supported harness use stable reason codes without throwing', () => {
  const plan = buildPersonalInstallPlan({
    cwd: '/missing', home: '/tmp/pulse-plan-home', codexHome: '/tmp/pulse-plan-codex',
    platform: 'linux', architecture: 'x64', nodeVersion: '18.0.0',
    release: verifiedRelease(), detectResources: () => ampleResources,
    detectWorkspace: () => { const error = new Error('not git'); error.code = 'workspace_not_git'; throw error; },
    detectClaude: hostMissing('claude'), detectCodex: hostMissing('codex'), detectCursor: hostMissing('cursor'),
  });
  assert.equal(plan.outcome, 'unsupported');
  assert.deepEqual(plan.reason_codes, [
    'platform_unsupported', 'architecture_unsupported', 'node_unsupported', 'workspace_not_git', 'supported_harness_missing',
  ]);
});

test('detected but incompatible supported harnesses have a distinct no-mutation reason', () => {
  const plan = buildPersonalInstallPlan({
    cwd: '/project', home: '/tmp/pulse-plan-home', codexHome: '/tmp/pulse-plan-codex',
    platform: 'darwin', architecture: 'arm64', nodeVersion: '20.0.0', currentState: cleanState,
    detectWorkspace: () => ({ canonical_path: '/project', checkout_kind: 'primary', repository_id: 'repository_a', workspace_id: 'workspace_a' }),
    detectClaude: () => ({ available: false, executable_path: '/usr/local/bin/claude', reason_code: 'claude_version_invalid' }),
    detectCodex: hostMissing('codex'), detectCursor: hostMissing('cursor'),
    release: verifiedRelease(), detectResources: () => ampleResources,
  });
  assert.equal(plan.outcome, 'action_required');
  assert.deepEqual(plan.reason_codes, ['supported_harness_incompatible']);
  assert.equal(plan.next_action.code, 'supported_harness_incompatible');
});

test('workspace detection collapses symlinks and distinguishes Git worktrees', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-install-workspace.'));
  try {
    const home = join(root, 'home');
    mkdirSync(home);
    const primary = repository(root, 'primary');
    const alias = join(root, 'alias');
    symlinkSync(primary, alias);
    const linked = buildPersonalInstallPlan({ cwd: alias, home, platform: 'darwin', architecture: 'arm64', nodeVersion: '20.0.0', detectCodex: codexReady, release: verifiedRelease(), detectResources: () => ampleResources });
    assert.equal(linked.detected.workspace.canonical_path, realpathSync(primary));

    const worktree = join(root, 'worktree');
    git(primary, 'worktree', 'add', '--detach', '-q', worktree);
    const secondary = buildPersonalInstallPlan({ cwd: worktree, home, platform: 'darwin', architecture: 'arm64', nodeVersion: '20.0.0', detectCodex: codexReady, release: verifiedRelease(), detectResources: () => ampleResources });
    assert.equal(secondary.detected.workspace.checkout_kind, 'worktree');
    assert.equal(secondary.detected.workspace.repository_id, linked.detected.workspace.repository_id);
    assert.notEqual(secondary.detected.workspace.workspace_id, linked.detected.workspace.workspace_id);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('insufficient disk and memory are explicit preflight reasons', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-install-resources.'));
  try {
    const home = join(root, 'home');
    const cwd = repository(root);
    mkdirSync(home);
    const plan = buildPersonalInstallPlan({
      cwd, home, platform: 'darwin', architecture: 'arm64', nodeVersion: '20.0.0',
      detectCodex: codexReady, release: verifiedRelease(),
      detectResources: () => ({ disk_free_bytes: 100, memory_total_bytes: 1024, port_18789: 'occupied' }),
    });
    assert.equal(plan.outcome, 'action_required');
    assert.deepEqual(plan.reason_codes, ['disk_insufficient', 'memory_insufficient']);
    assert.equal(plan.resources.port_18789, 'occupied');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('Codex detection inspects absolute candidates and probes a version only when explicitly requested', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-codex-detect.'));
  try {
    const executable = join(root, 'codex');
    writeFileSync(executable, '#!/bin/sh\nexit 0\n', { mode: 0o700 });
    chmodSync(executable, 0o700);

    const inspected = detectCodexCLI({ candidates: [executable] });
    assert.equal(inspected.available, true);
    assert.equal(inspected.executable_path, realpathSync(executable));
    assert.match(inspected.executable_sha256, /^[a-f0-9]{64}$/);
    assert.equal(inspected.version, null);
    assert.equal(inspected.reason_code, null);
    const found = detectCodexCLI({
      codexPath: executable,
      versionProbe: (path) => {
        assert.equal(path, realpathSync(executable));
        return { status: 0, stdout: 'codex-cli 0.114.0\n', stderr: '' };
      },
    });
    assert.equal(found.available, true);
    assert.equal(found.executable_path, realpathSync(executable));
    assert.equal(found.executable_sha256, inspected.executable_sha256);
    assert.equal(found.version, '0.114.0');
    assert.equal(found.reason_code, null);
    writeFileSync(executable, '#!/bin/sh\n# replaced after consent\nexit 0\n', { mode: 0o700 });
    const replaced = detectCodexCLI({
      codexPath: executable,
      versionProbe: () => ({ status: 0, stdout: 'codex-cli 0.114.0\n', stderr: '' }),
    });
    assert.notEqual(replaced.executable_sha256, inspected.executable_sha256,
      'post-consent verification must be able to detect executable replacement');
    assert.deepEqual(detectCodexCLI({ candidates: [join(root, 'missing')] }), {
      available: false, executable_path: null, executable_sha256: null,
      version: null, reason_code: 'codex_missing',
    });
    assert.throws(() => detectCodexCLI({ codexPath: 'codex', versionProbe: () => ({ status: 0 }) }),
      /codex_path_invalid/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('pre-consent Codex detection ignores inherited PATH and never executes a project-local binary', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-codex-path.'));
  const previousPath = process.env.PATH;
  try {
    const projectBin = join(root, 'bin');
    const marker = join(root, 'executed');
    mkdirSync(projectBin);
    writeFileSync(join(projectBin, 'codex'), `#!/bin/sh\ntouch ${JSON.stringify(marker)}\n`, { mode: 0o700 });
    process.env.PATH = `${projectBin}:${previousPath ?? ''}`;

    const detected = detectCodexCLI({ candidates: [join(root, 'trusted', 'codex')] });
    assert.deepEqual(detected, {
      available: false, executable_path: null, executable_sha256: null,
      version: null, reason_code: 'codex_missing',
    });
    assert.equal(readdirSync(root).includes('executed'), false);
  } finally {
    if (previousPath === undefined) delete process.env.PATH;
    else process.env.PATH = previousPath;
    rmSync(root, { recursive: true, force: true });
  }
});

test('custom canonical data directory owns runtime, artifact, cache, vault, and receipt disclosures', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-install-data-dir.'));
  try {
    const home = join(root, 'home');
    const dataDir = join(root, 'pulse-data');
    const cwd = repository(root);
    mkdirSync(home);
    const plan = buildPersonalInstallPlan({
      cwd, home, dataDir, platform: 'darwin', architecture: 'arm64', nodeVersion: '20.0.0',
      detectCodex: codexReady, release: verifiedRelease(), detectResources: () => ampleResources,
      currentState: cleanState,
    });
    const writes = new Map(plan.local_writes.map((entry) => [entry.purpose, entry.path]));
    assert.equal(writes.get('verified_release_artifacts'), join(dataDir, 'artifacts'));
    assert.equal(writes.get('runtime_and_install_journal'), join(dataDir, 'runtime'));
    assert.equal(writes.get('private_memory_vault'), join(dataDir, 'vaults', 'personal', 'store_personal_<generated>'));
    assert.equal(writes.get('rebuildable_retrieval_cache'), join(dataDir, 'caches', 'personal', 'store_personal_<generated>'));
    assert.equal(writes.get('install_receipt'), join(dataDir, 'receipts', 'install', '<workspace_id>.json'));
    assert.equal(writes.get('device_local_principal'), join(home, '.pulse', 'identity', 'personal-principal.json'));
    assert.equal(writes.get('signed_workspace_binding_registry'), join(home, '.pulse', 'supervisor', 'workspace-bindings.json'));
    assert.equal(
      writes.get('codex_workspace_access'),
      join(home, '.pulse', 'product-host-access', '<workspace_digest>', 'codex.json'),
    );
    assert.throws(() => buildPersonalInstallPlan({ dataDir: 'relative' }), /install_plan_path_invalid/);
    assert.throws(() => buildPersonalInstallPlan({ dataDir: `${root}/nested/../pulse-data` }),
      /install_plan_path_invalid/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('unsafe prior state always becomes action_required before consent', () => {
  const cases = [
    ['principal', 'invalid', 'principal_repair_required'],
    ['binding', 'conflict', 'binding_conflict'],
    ['binding', 'legacy', 'binding_legacy'],
    ['binding', 'repair_required', 'binding_repair_required'],
    ['presence', 'synthetic_test_authority', 'synthetic_authority_forbidden'],
    ['presence', 'invalid', 'presence_invalid'],
    ['runtime', 'corrupt', 'runtime_corrupt'],
    ['daemon', 'unsafe', 'daemon_unsafe'],
  ];
  for (const [component, status, reason] of cases) {
    const state = { ...cleanState, [component]: status };
    const plan = buildPersonalInstallPlan({
      cwd: '/project', home: '/tmp/pulse-plan-home', codexHome: '/tmp/pulse-plan-codex',
      platform: 'darwin', architecture: 'arm64', nodeVersion: '20.0.0', currentState: state,
      detectWorkspace: () => ({ canonical_path: '/project', checkout_kind: 'primary', repository_id: 'repository_a', workspace_id: 'workspace_a' }),
      detectCodex: codexReady, release: verifiedRelease(), detectResources: () => ampleResources,
    });
    assert.equal(plan.outcome, 'action_required', `${component}:${status}`);
    assert.ok(plan.reason_codes.includes(reason), `${component}:${status}`);
    assert.equal(plan.next_action.code, reason, `${component}:${status}`);
  }
});

test('safe release failure codes remain visible while arbitrary codes are rejected', () => {
  const base = {
    cwd: '/project', home: '/tmp/pulse-plan-home', codexHome: '/tmp/pulse-plan-codex',
    platform: 'darwin', architecture: 'arm64', nodeVersion: '20.0.0', currentState: cleanState,
    detectWorkspace: () => ({ canonical_path: '/project', checkout_kind: 'primary', repository_id: 'repository_a', workspace_id: 'workspace_a' }),
    detectCodex: codexReady, detectResources: () => ampleResources,
  };
  const expired = buildPersonalInstallPlan({ ...base, releaseReasonCode: 'manifest_expired' });
  assert.ok(expired.reason_codes.includes('manifest_expired'));
  assert.equal(expired.reason_codes.includes('release_manifest_unavailable'), false);
  assert.throws(() => buildPersonalInstallPlan({ ...base, releaseReasonCode: 'secret_/tmp/leak' }),
    /install_plan_release_reason_invalid/);
});

test('human disclosure names the exact project, downloads, local writes, privacy, and human gates', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-install-format.'));
  try {
    const home = join(root, 'home');
    const cwd = repository(root);
    mkdirSync(home);
    const plan = buildPersonalInstallPlan({
      cwd, home, platform: 'darwin', architecture: 'arm64', nodeVersion: '20.0.0',
      detectCodex: codexReady, release: verifiedRelease(), detectResources: () => ampleResources,
    });
    const output = formatPersonalInstallPlan(plan);
    assert.match(output, /Pulse Personal install/);
    assert.match(output, /Current state:/);
    assert.match(output, /runtime: unknown/);
    assert.match(output, new RegExp(plan.detected.workspace.workspace_id));
    assert.match(output, /150 bytes/);
    assert.match(output, /https:\/\/models\.zbs\.gg/);
    assert.match(output, /raw transcript capture: off/i);
    assert.match(output, /backend model calls: off/i);
    assert.match(output, /macos_presence_and_binding/);
    assert.match(output, /signed binding and Personal vault are preserved/i);
    assert.match(output, /runtime uninstall: unavailable in this U3 build/i);
    assert.match(output, /wipe is separate/i);
    assert.doesNotMatch(output, /pulse uninstall/i);
    assert.doesNotMatch(canonicalInstallPlanJSON(plan), /pulse uninstall/i);
    assert.doesNotMatch(output, /--yes.*consent/i);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
