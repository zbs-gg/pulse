#!/usr/bin/env node

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import {
  chmodSync, existsSync, lstatSync, mkdirSync, readFileSync, rmSync, writeFileSync,
} from 'node:fs';
import { basename, dirname, isAbsolute, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { vaultRuntimeFromBinding } from '../src/local-supervisor.js';
import { protectedHarnessApproval } from '../src/personal-install-command.js';
import { exactTarballPulseInvocation } from '../src/release-attestation.js';
import { canonicalReleaseJSON, pinnedReleaseKeyring, verifyPersonalReleaseArtifactSet } from '../src/release-manifest.js';
import { resolveWorkspaceBinding } from '../src/workspace-binding.js';
import { currentNativeTargetID, loadNativeUniversalMatrix } from './native-universal-matrix.mjs';

const SHA40 = /^[a-f0-9]{40}$/;
const SHA256 = /^[a-f0-9]{64}$/;
const AUTHORITIES = Object.freeze(['production_candidate', 'public_registry']);

function fail(code) {
  const error = new Error(code);
  error.code = code;
  throw error;
}

function argument(name) {
  const index = process.argv.indexOf(name);
  return index < 0 ? undefined : process.argv[index + 1];
}

function regularFile(path, maximum = 1024 * 1024 * 1024) {
  let info;
  try { info = lstatSync(path); } catch { fail('native_vendor_input_missing'); }
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.size < 1 || info.size > maximum) {
    fail('native_vendor_input_unsafe');
  }
  return Object.freeze({
    bytes: info.size,
    sha256: createHash('sha256').update(readFileSync(path)).digest('hex'),
  });
}

function run(command, args, { cwd, env, timeout = 15 * 60_000 } = {}) {
  const result = spawnSync(command, args, {
    cwd, env, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout,
    maxBuffer: 32 * 1024 * 1024,
    shell: process.platform === 'win32' && /\.(?:cmd|bat)$/i.test(command),
  });
  if (result.status !== 0) fail('native_vendor_command_failed');
  return result.stdout;
}

function npmCLI() {
  const value = process.env.npm_execpath;
  if (!value || !isAbsolute(value) || resolve(value) !== value) fail('native_vendor_npm_invalid');
  regularFile(value, 64 * 1024 * 1024);
  return value;
}

function packedPulse(tarball, args, options) {
  return run(process.execPath, [npmCLI(), ...exactTarballPulseInvocation(tarball, args)], options);
}

function exactJSON(value, code) {
  try { return JSON.parse(value); } catch { fail(code); }
}

function hostExecutable(host) {
  const name = {
    'claude-code': 'PULSE_PROTECTED_HARNESS_CLAUDE_EXECUTABLE',
    codex: 'PULSE_PROTECTED_HARNESS_CODEX_EXECUTABLE',
    cursor: 'PULSE_PROTECTED_HARNESS_CURSOR_APP',
  }[host];
  const value = process.env[name];
  if (!value || !isAbsolute(value)) fail('native_vendor_harness_missing');
  if (host === 'cursor' && process.platform === 'darwin') {
    return resolve(value, 'Contents', 'MacOS', 'Cursor');
  }
  return resolve(value);
}

function sessionExecutable(host) {
  if (host !== 'cursor') return hostExecutable(host);
  const value = process.env.PULSE_PROTECTED_HARNESS_CURSOR_SESSION_EXECUTABLE;
  if (!value || !isAbsolute(value)) fail('native_vendor_session_harness_missing');
  return resolve(value);
}

function vendorSession(host, prompt, { cwd, env, outputPath }) {
  const executable = sessionExecutable(host);
  let args;
  if (host === 'codex') {
    args = [
      'exec', '--ephemeral', '--dangerously-bypass-hook-trust', '--sandbox', 'read-only',
      '--cd', cwd, '--output-last-message', outputPath, prompt,
    ];
  } else if (host === 'claude-code') {
    args = [
      '--print', '--output-format', 'json', '--permission-mode', 'dontAsk',
      '--disallowedTools', 'Bash,Edit,Write,WebFetch,WebSearch', prompt,
    ];
  } else {
    args = ['-p', '--output-format', 'json', prompt];
  }
  const stdout = run(executable, args, { cwd, env, timeout: 10 * 60_000 });
  return existsSync(outputPath) ? readFileSync(outputPath, 'utf8') : stdout;
}

async function productJSON(runtime, secret, path) {
  const response = await fetch(`${runtime.base_url}${path}`, {
    headers: { Accept: 'application/json', 'X-Pulse-Key': secret },
    redirect: 'manual', signal: AbortSignal.timeout(10_000),
  });
  if (!response.ok || response.redirected) fail('native_vendor_product_query_failed');
  return response.json();
}

async function waitForMemory(runtime, secret, host, marker) {
  for (let attempt = 0; attempt < 120; attempt += 1) {
    const viewer = await productJSON(runtime, secret,
      `/viewer/data?thread_id=protected-native-e2e&host=${host}&token_budget=900`);
    if (viewer?.first_memory?.status === 'saved' &&
        Array.isArray(viewer?.next_resume?.included_object_ids) &&
        viewer.next_resume.included_object_ids.length > 0 && JSON.stringify(viewer).includes(marker)) return viewer;
    await new Promise((accept) => setTimeout(accept, 500));
  }
  fail('native_vendor_memory_not_visible');
}

function environment(root, authority, packageSHA256, sourceCommit) {
  const home = join(root, 'home');
  const dataDir = join(root, 'data');
  const workspace = join(root, 'workspace');
  for (const path of [home, dataDir, workspace]) mkdirSync(path, { recursive: true, mode: 0o700 });
  return {
    cwd: workspace,
    dataDir,
    env: {
      ...process.env,
      HOME: home,
      ...(process.platform === 'win32' ? {
        APPDATA: join(home, 'AppData', 'Roaming'),
        LOCALAPPDATA: join(home, 'AppData', 'Local'),
        USERPROFILE: home,
      } : {}),
      CODEX_HOME: join(home, '.codex'),
      CURSOR_HOME: join(home, '.cursor'),
      PULSE_DATA_DIR: dataDir,
      PULSE_PROTECTED_HARNESS_AUTHORITY: authority,
      PULSE_PROTECTED_HARNESS_PACKAGE_SHA256: packageSHA256,
      PULSE_PROTECTED_HARNESS_SOURCE_COMMIT: sourceCommit,
    },
    home,
    workspace,
  };
}

function initializeWorkspace(workspace, env) {
  run('git', ['init', '-q'], { cwd: workspace, env });
  run('git', ['config', 'user.email', 'pulse-native@example.test'], { cwd: workspace, env });
  run('git', ['config', 'user.name', 'Pulse Native'], { cwd: workspace, env });
  run('git', ['commit', '--allow-empty', '-q', '-m', 'native-e2e'], { cwd: workspace, env });
}

function approve(plan, context, authority, packageSHA256, sourceCommit) {
  const path = join(dirname(context.workspace), 'install-approval.json');
  const receipt = protectedHarnessApproval(plan, {
    authority, dataDir: context.dataDir, packageSHA256, sourceCommit, workspace: context.workspace,
  });
  writeFileSync(path, `${JSON.stringify(receipt)}\n`, { mode: 0o600 });
  chmodSync(path, 0o600);
  context.env.PULSE_PROTECTED_HARNESS_APPROVAL_PATH = path;
}

async function oneRun({ authority, host, packageInfo, sourceCommit, tarball }, index) {
  const runRoot = join(process.env.PULSE_PROTECTED_HARNESS_ROOT, `run-${index}`);
  mkdirSync(runRoot, { mode: 0o700 });
  const context = environment(runRoot, authority, packageInfo.sha256, sourceCommit);
  initializeWorkspace(context.workspace, context.env);
  const stages = {};
  let boundary = Date.now();
  const stage = (name) => {
    const now = Date.now();
    stages[name] = now - boundary;
    boundary = now;
  };
  const plan = exactJSON(packedPulse(tarball, ['install-plan', '--json'], {
    cwd: context.workspace, env: context.env, timeout: 180_000,
  }), 'native_vendor_install_plan_invalid');
  assert.equal(plan.outcome, 'ready_to_install');
  approve(plan, context, authority, packageInfo.sha256, sourceCommit);
  const installed = exactJSON(packedPulse(tarball, ['install', '--json'], {
    cwd: context.workspace, env: context.env,
  }), 'native_vendor_install_invalid');
  assert.equal(installed.outcome, 'ready');
  assert.equal(installed.host_status?.hosts?.length, 1);
  assert.equal(installed.host_status.hosts[0].host, host);
  stage('install');
  const startedAt = Date.now();

  const marker = `PULSE-NATIVE-${randomBytes(12).toString('hex')}`;
  const firstOutput = join(runRoot, 'first-session.out');
  const memoryPrompt = [
    'Use the connected Pulse memory tool exactly once. Do not use shell commands or edit files.',
    'Store one bounded structured project memory capsule with schema pulse.memory_capsule.v1,',
    `redacted_summary exactly ${marker}, normal privacy, project retention, and one user_confirmed evidence hint.`,
    'Wait for the terminal receipt, then reply only SAVED.',
  ].join(' ');
  vendorSession(host, memoryPrompt, { cwd: context.workspace, env: context.env, outputPath: firstOutput });
  stage('vendor_write_session');

  Object.assign(process.env, context.env);
  const binding = resolveWorkspaceBinding({ cwd: context.workspace });
  const runtime = vaultRuntimeFromBinding(binding);
  const secret = readFileSync(join(runtime.data_dir, 'secret.key'), 'utf8').trim();
  if (!SHA256.test(secret)) fail('native_vendor_runtime_secret_invalid');
  const viewer = await waitForMemory(runtime, secret, host, marker);
  const objectID = viewer.next_resume.included_object_ids[0];
  if (typeof objectID !== 'string' || objectID.length < 1) fail('native_vendor_object_invalid');
  stage('terminal_receipt_and_home');

  const freshOutput = join(runRoot, 'fresh-session.out');
  const fresh = vendorSession(host, [
    'Without calling any tools, state the exact project memory automatically supplied to this fresh session.',
    'Reply with only that memory value.',
  ].join(' '), { cwd: context.workspace, env: context.env, outputPath: freshOutput });
  if (!fresh.includes(marker)) fail('native_vendor_fresh_recall_missing');
  stage('fresh_session');
  const firstValueMS = Date.now() - startedAt;

  const doctor = exactJSON(packedPulse(tarball, ['doctor', host, '--json'], {
    cwd: context.workspace, env: context.env,
  }), 'native_vendor_doctor_invalid');
  if (doctor.personal_live_readiness?.outcome !== 'ready' || doctor.trust?.raw_transcript_capture !== false ||
      doctor.trust?.backend_llm_enabled !== false || doctor.trust?.external_embedding_api !== false) {
    fail('native_vendor_lifecycle_degraded');
  }
  stage('doctor');

  const repairPlan = exactJSON(packedPulse(tarball, ['install-plan', '--json'], {
    cwd: context.workspace, env: context.env, timeout: 180_000,
  }), 'native_vendor_repair_plan_invalid');
  approve(repairPlan, context, authority, packageInfo.sha256, sourceCommit);
  const repaired = exactJSON(packedPulse(tarball, ['repair', '--json'], {
    cwd: context.workspace, env: context.env,
  }), 'native_vendor_repair_invalid');
  assert.equal(repaired.outcome, 'ready');
  stage('repair');

  packedPulse(tarball, ['disconnect', host], { cwd: context.workspace, env: context.env });
  const afterDisconnect = await productJSON(runtime, secret, '/memory/status');
  if (!Number.isSafeInteger(afterDisconnect.item_count) || afterDisconnect.item_count < 1) {
    fail('native_vendor_disconnect_lost_vault');
  }
  stage('disconnect');
  return Object.freeze({ firstValueMS, stages });
}

const authority = argument('--authority');
const host = argument('--host');
const targetID = argument('--target');
const tarball = resolve(argument('--tarball') ?? '');
const catalogRoot = resolve(argument('--catalog') ?? '');
const output = resolve(argument('--out') ?? '');
const sourceCommit = argument('--commit');
const repeat = Number(argument('--repeat') ?? '1');
if (!AUTHORITIES.includes(authority) || !SHA40.test(sourceCommit ?? '') ||
    ![tarball, catalogRoot, output].every(isAbsolute) || !Number.isSafeInteger(repeat) || repeat < 1 || repeat > 5 ||
    process.argv.length !== 18) fail('native_vendor_arguments_invalid');
const matrix = loadNativeUniversalMatrix();
const harness = matrix.harnesses.find((value) => value.host === host);
const target = matrix.targets.find((value) => value.target_id === targetID);
if (!harness || !target || currentNativeTargetID() !== targetID ||
    repeat !== (host === 'codex' && targetID === 'win32-arm64' ? 5 : 1)) fail('native_vendor_matrix_invalid');
const packageInfo = regularFile(tarball);
const artifactSetPath = join(catalogRoot, 'personal-preview-manifest.json');
const snapshotPath = join(catalogRoot, 'snapshot.json');
const artifactSetInfo = regularFile(artifactSetPath, 2 * 1024 * 1024);
const snapshotInfo = regularFile(snapshotPath, 2 * 1024 * 1024);
const artifactSet = exactJSON(readFileSync(artifactSetPath, 'utf8'), 'native_vendor_catalog_invalid');
const snapshot = exactJSON(readFileSync(snapshotPath, 'utf8'), 'native_vendor_catalog_invalid');
const verified = verifyPersonalReleaseArtifactSet(artifactSet, snapshot, {
  architecture: target.architecture, libc: target.libc, minimumAcceptedEpoch: 8, now: new Date(),
  osVersion: '26.2', packageVersion: '0.7.0', platform: target.platform, trustedKeys: pinnedReleaseKeyring(),
});
if (verified.target_id !== targetID || verified.manifest_digest !== artifactSetInfo.sha256 ||
    verified.authority.snapshot_digest !== snapshotInfo.sha256) fail('native_vendor_catalog_invalid');
const harnessExecutable = regularFile(hostExecutable(host));
const sessionHarness = regularFile(sessionExecutable(host));
if (!process.env.PULSE_PROTECTED_HARNESS_ROOT || !isAbsolute(process.env.PULSE_PROTECTED_HARNESS_ROOT) ||
    !relative(process.env.RUNNER_TEMP, process.env.PULSE_PROTECTED_HARNESS_ROOT) ||
    relative(process.env.RUNNER_TEMP, process.env.PULSE_PROTECTED_HARNESS_ROOT).startsWith('..')) {
  fail('native_vendor_root_invalid');
}
const runs = [];
try {
  for (let index = 0; index < repeat; index += 1) runs.push(await oneRun({
    authority, host, packageInfo, sourceCommit, tarball,
  }, index));
  const values = runs.map((runValue) => runValue.firstValueMS).sort((left, right) => left - right);
  const median = values[Math.floor(values.length / 2)];
  if (values.some((value) => value > 60_000) || (repeat === 5 && median > 55_000)) fail('native_vendor_first_value_failed');
  const evidence = Object.freeze({
    authority,
    content_free: true,
    first_value: {
      boundary: 'fresh_session_context',
      degraded_lifecycle: false,
      measured: true,
      milliseconds: values.at(-1),
      stability: { consecutive_runs: values.length, median_ms: median, runs_ms: values },
      stages_ms: runs.at(-1).stages,
    },
    harness: {
      distribution: harness.distribution,
      download_url: harness.downloads[targetID],
      executable_kind: 'vendor_executable',
      executable_sha256: harnessExecutable.sha256,
      identity: harness.identity,
      session_executable_sha256: sessionHarness.sha256,
      vendor: harness.vendor,
      vendor_source: harness.vendor_source,
    },
    host,
    host_version: harness.version,
    milestones: {
      disconnect: true, fresh_recall: true, install: true, lifecycle: true,
      memory_home: true, repair: true, vendor_session: true,
    },
    package: packageInfo,
    privacy_defaults: { backend_llm: false, old_chat_import: false, raw_transcripts: false },
    release: {
      artifact_ids: Object.values(verified.artifacts).map((artifact) => artifact.id).sort(),
      artifact_set_digest: artifactSetInfo.sha256,
      fixture_manifest_digest: null,
      snapshot_digest: snapshotInfo.sha256,
    },
    runner: { image: process.env.ImageOS || process.env.RUNNER_OS || process.platform, name: process.env.RUNNER_NAME || 'local' },
    schema: 'pulse.native_host_target_evidence.v2',
    source_commit: sourceCommit,
    support_claim: authority === 'public_registry',
    target,
    target_id: targetID,
    token_economy: { state: 'collecting_baseline' },
    consolidation: { mutation_authority_exercised: false },
  });
  mkdirSync(dirname(output), { recursive: true, mode: 0o700 });
  writeFileSync(output, `${canonicalReleaseJSON(evidence)}\n`, { mode: 0o600, flag: 'wx' });
  process.stdout.write(`${canonicalReleaseJSON({
    authority, host, package_sha256: packageInfo.sha256, schema: evidence.schema, target_id: targetID,
  })}\n`);
} finally {
  rmSync(process.env.PULSE_PROTECTED_HARNESS_ROOT, { recursive: true, force: true });
}
