#!/usr/bin/env node

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { chmodSync, lstatSync, mkdirSync, readFileSync, readdirSync, realpathSync, writeFileSync } from 'node:fs';
import { dirname, isAbsolute, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  currentNativeTargetID, exactHarnessVersionPattern, loadNativeUniversalMatrix,
  nativeHarnessCommandUsesShell,
} from './native-universal-matrix.mjs';

const cliRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');

function argument(name) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : undefined;
}

function command(command, args, { env = process.env, timeout = 20 * 60_000 } = {}) {
  const result = spawnSync(command, args, {
    cwd: cliRoot, env, encoding: 'utf8', timeout,
    stdio: ['ignore', 'pipe', 'pipe'], maxBuffer: 64 * 1024 * 1024,
    shell: nativeHarnessCommandUsesShell(command),
  });
  if (result.status !== 0) {
    throw new Error(`${command} ${args.join(' ')} exited ${result.status}\n${result.stdout}\n${result.stderr}`);
  }
  return result.stdout.trim();
}

function exactOutputPath(value) {
  if (typeof value !== 'string' || value.length < 1 || isAbsolute(value)) {
    throw new Error('native universal evidence path must be relative to the package root');
  }
  const path = resolve(cliRoot, value);
  const local = relative(cliRoot, path);
  if (local === '..' || local.startsWith(`..${process.platform === 'win32' ? '\\' : '/'}`)) {
    throw new Error('native universal evidence path escapes the package root');
  }
  return path;
}

function nativeCodexExecutable(npmExecPath) {
  const globalRoot = command(process.execPath, [npmExecPath, 'root', '--global']);
  const packageRoot = join(globalRoot, '@openai', 'codex', 'node_modules', '@openai');
  const executableName = process.platform === 'win32' ? 'codex.exe' : 'codex';
  const matches = [];
  const visit = (path, depth = 0) => {
    if (depth > 8) return;
    for (const name of readdirSync(path)) {
      const child = join(path, name);
      const info = lstatSync(child);
      if (info.isSymbolicLink()) continue;
      if (info.isDirectory()) visit(child, depth + 1);
      else if (info.isFile() && name === executableName) matches.push(child);
    }
  };
  visit(packageRoot);
  assert.equal(matches.length, 1, `expected one native Codex executable under ${packageRoot}`);
  return resolve(matches[0]);
}

function fileSHA256(path) {
  const canonical = realpathSync(resolve(path));
  const info = lstatSync(canonical);
  assert.equal(info.isFile() && !info.isSymbolicLink() && info.size > 0, true, 'harness executable is unsafe');
  return { path: canonical, sha256: createHash('sha256').update(readFileSync(canonical)).digest('hex') };
}

function commandExecutable(name) {
  const locator = process.platform === 'win32' ? 'where.exe' : 'which';
  const located = command(locator, [name]).split(/\r?\n/).map((value) => value.trim()).filter(Boolean)[0];
  assert.equal(Boolean(located), true, `missing ${name} executable`);
  return fileSHA256(located);
}

const matrix = loadNativeUniversalMatrix();
const requestedTarget = argument('--target');
const requestedHost = argument('--host');
const repeat = Number(argument('--repeat') ?? '1');
const target = matrix.targets.find((value) => value.target_id === requestedTarget);
const harness = matrix.harnesses.find((value) => value.host === requestedHost);
assert.equal(Boolean(target), true, 'native universal target is not declared');
assert.equal(Boolean(harness), true, 'native universal host is not declared');
assert.equal(Number.isSafeInteger(repeat) && repeat >= 1 && repeat <= 5, true, 'native repeat is invalid');
assert.equal(repeat, harness.host === 'codex' && target.target_id === 'win32-arm64' ? 5 : 1);
assert.equal(harness.supported_targets.includes(target.target_id), true);
assert.equal(currentNativeTargetID(), target.target_id);

const npmExecPath = process.env.npm_execpath;
if (!npmExecPath || !isAbsolute(npmExecPath)) throw new Error('run target proof through npm');
let harnessExecutable;
const stdoutRuns = [];
if (harness.host === 'codex') {
  const version = command(harness.executable, ['--version']);
  assert.match(version, exactHarnessVersionPattern(harness.version));
  const codexExecutable = nativeCodexExecutable(npmExecPath);
  harnessExecutable = { ...fileSHA256(codexExecutable), kind: 'vendor_executable' };
  for (let index = 0; index < repeat; index += 1) {
    stdoutRuns.push(command(process.execPath, [npmExecPath, 'run', '--silent', 'test:personal-native-packed'], {
      env: {
        ...process.env,
        PULSE_NATIVE_PACKED_HOST: harness.host,
        PULSE_NATIVE_PACKED_CODEX_EXECUTABLE: codexExecutable,
      },
    }));
  }
} else {
  if (harness.distribution === 'npm') {
    const version = command(harness.executable, ['--version']);
    assert.match(version, exactHarnessVersionPattern(harness.version));
    harnessExecutable = { ...commandExecutable(harness.executable), kind: 'vendor_executable' };
  } else {
    const cursorExecutable = process.env.PULSE_CURSOR_EXECUTABLE;
    assert.equal(typeof cursorExecutable === 'string' && isAbsolute(cursorExecutable), true,
      'Cursor proof requires the exact vendor Desktop executable');
    const version = command(cursorExecutable, ['--version']);
    assert.match(version, /(?:^|\s)3\.13(?:\.\d+)?(?:$|\s)/);
    harnessExecutable = { ...fileSHA256(cursorExecutable), kind: 'vendor_executable' };
  }
  stdoutRuns.push(command(process.execPath, [npmExecPath, 'run', '--silent', 'test:personal-multiharness']));
}
const stdout = stdoutRuns.join('\n');
process.stdout.write(`${stdout}\n`);
const receipts = stdout.split(/\r?\n/).map((line) => {
  try { return JSON.parse(line); } catch { return null; }
}).filter(Boolean);
const productReceipts = receipts.filter((value) => value.schema === 'pulse.native_packed_product_fixture.v1');
const consolidationReceipts = receipts.filter((value) => value.schema === 'pulse.personal_consolidation_report_fixture.v1');
const productReceipt = productReceipts.at(-1);
const consolidationReceipt = consolidationReceipts.at(-1);
const orchestrationReceipt = receipts.find((value) =>
  value.schema === 'pulse.personal_preview_multiharness_orchestration.v1');
if (harness.host === 'codex') {
  assert.equal(productReceipts.length, repeat);
  assert.equal(consolidationReceipts.length, repeat);
  assert.equal(new Set(productReceipts.map((value) => value.packed_tarball_sha256)).size, 1);
  assert.equal(productReceipt?.target_id, target.target_id);
  assert.equal(productReceipt?.host, harness.host);
  for (const field of [
    'exact_public_install_command', 'native_daemon', 'native_fixture_embedder', 'visible_memory_card',
    'first_memory_saved', 'fresh_session_context', 'host_observation', 'lifecycle_ready', 'repair_ready',
    'same_object_recalled',
  ]) assert.equal(productReceipt[field], true, field);
  assert.equal(productReceipt.production_ready, false);
  assert.equal(productReceipt.support_proven, false);
  assert.equal(productReceipt.first_value_boundary, 'fresh_session_context');
  assert.equal(productReceipt.first_value_ms <= 60_000, true);
  assert.equal(Object.keys(productReceipt.first_value_stages_ms ?? {}).length >= 10, true);
  assert.equal(Object.values(productReceipt.first_value_stages_ms).every((value) =>
    Number.isSafeInteger(value) && value >= 0), true);
  assert.match(productReceipt.packed_tarball_sha256, /^[a-f0-9]{64}$/);
  assert.match(productReceipt.release_manifest_digest, /^[a-f0-9]{64}$/);
  assert.equal(Array.isArray(productReceipt.release_artifact_ids), true);
  assert.equal(productReceipt.release_artifact_ids.length, target.platform === 'darwin' ? 5 : 4);
  for (const kind of ['daemon', 'embedder-runtime', 'model', 'plugin-runtime']) {
    assert.equal(productReceipt.release_artifact_ids.filter((id) => id.endsWith(`-${kind}`)).length, 1, kind);
  }
  assert.equal(
    productReceipt.release_artifact_ids.filter((id) => id.endsWith('-presence-helper')).length,
    target.platform === 'darwin' ? 1 : 0,
  );
  assert.equal(['collecting_baseline', 'estimated', 'measured'].includes(productReceipt.token_economy?.state), true);
  assert.equal(consolidationReceipt?.target_id, target.target_id);
  assert.equal(consolidationReceipt?.package_sha256, productReceipt.packed_tarball_sha256);
  assert.equal(consolidationReceipt?.phase, 'report_ready');
  assert.deepEqual(consolidationReceipt?.source_classifications, ['backup', 'canonical_vault', 'release_artifact']);
  for (const proof of ['cli_parity', 'mcp_parity', 'memory_home_visible', 'sources_byte_preserved']) {
    assert.equal(consolidationReceipt?.[proof], true, proof);
  }
  for (const mutation of ['imported', 'merged', 'deleted', 'published']) {
    assert.equal(consolidationReceipt?.[mutation], false, mutation);
  }
} else {
  assert.equal(orchestrationReceipt?.exact_tarball_bound, true);
  assert.equal(orchestrationReceipt?.singleton_hosts?.includes(harness.host), true);
  assert.equal(orchestrationReceipt?.production_install_proof, false);
}

const firstValueRuns = productReceipts.map((receipt) => receipt.first_value_ms).sort((left, right) => left - right);
const firstValueMedian = firstValueRuns.length === 0
  ? null
  : firstValueRuns[Math.floor(firstValueRuns.length / 2)];
if (repeat === 5) {
  assert.equal(firstValueRuns.length, 5);
  assert.equal(firstValueRuns.every((milliseconds) => milliseconds <= 60_000), true);
  assert.equal(firstValueMedian <= 55_000, true);
}

const commit = process.env.GITHUB_SHA || command('git', ['rev-parse', 'HEAD']);
assert.match(commit, /^[a-f0-9]{40}$/);
const evidence = {
  schema: 'pulse.native_host_target_evidence.v2',
  authority: 'fixture',
  content_free: true,
  support_claim: false,
  source_commit: commit,
  host: harness.host,
  host_version: harness.version,
  target_id: target.target_id,
  target,
  runner: {
    name: process.env.RUNNER_NAME || 'local',
    image: process.env.ImageOS || process.env.RUNNER_OS || process.platform,
  },
  harness: {
    vendor: harness.vendor,
    distribution: harness.distribution,
    identity: harness.identity,
    vendor_source: harness.vendor_source,
    download_url: harness.downloads[target.target_id],
    executable_kind: harnessExecutable.kind,
    executable_sha256: harnessExecutable.sha256,
  },
  package: {
    sha256: productReceipt?.packed_tarball_sha256 ?? orchestrationReceipt.packed_tarball_sha256,
    bytes: productReceipt?.packed_tarball_bytes ?? orchestrationReceipt.packed_tarball_bytes,
  },
  release: {
    artifact_set_digest: null,
    snapshot_digest: null,
    fixture_manifest_digest: productReceipt?.release_manifest_digest ?? null,
    artifact_ids: productReceipt?.release_artifact_ids ?? [],
  },
  milestones: {
    install: true,
    vendor_session: false,
    lifecycle: productReceipt?.lifecycle_ready === true,
    memory_home: productReceipt?.visible_memory_card === true,
    fresh_recall: productReceipt?.same_object_recalled === true,
    repair: productReceipt?.repair_ready === true,
    disconnect: false,
  },
  first_value: {
    measured: productReceipt !== undefined,
    boundary: productReceipt?.first_value_boundary ?? null,
    milliseconds: productReceipt?.first_value_ms ?? null,
    stages_ms: productReceipt?.first_value_stages_ms ?? {},
    degraded_lifecycle: false,
    stability: {
      consecutive_runs: firstValueRuns.length,
      runs_ms: firstValueRuns,
      median_ms: firstValueMedian,
    },
  },
  privacy_defaults: {
    raw_transcripts: false,
    old_chat_import: false,
    backend_llm: false,
  },
  token_economy: productReceipt?.token_economy ?? null,
  consolidation: {
    phase: consolidationReceipt?.phase ?? null,
    report_digest: consolidationReceipt?.report_digest ?? null,
    inventory_digest: consolidationReceipt?.inventory_digest ?? null,
    source_classifications: consolidationReceipt?.source_classifications ?? [],
    totals: consolidationReceipt?.totals ?? null,
    cli_parity: consolidationReceipt?.cli_parity ?? false,
    mcp_parity: consolidationReceipt?.mcp_parity ?? false,
    memory_home_visible: consolidationReceipt?.memory_home_visible ?? false,
    sources_byte_preserved: consolidationReceipt?.sources_byte_preserved ?? false,
    mutation_authority_exercised: false,
  },
};
const output = exactOutputPath(argument('--out'));
mkdirSync(dirname(output), { recursive: true, mode: 0o700 });
writeFileSync(output, `${JSON.stringify(evidence)}\n`, { mode: 0o600 });
chmodSync(output, 0o600);
assert.deepEqual(JSON.parse(readFileSync(output, 'utf8')), evidence);
process.stdout.write(`${JSON.stringify(evidence)}\n`);
