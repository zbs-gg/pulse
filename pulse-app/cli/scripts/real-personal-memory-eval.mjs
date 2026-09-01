#!/usr/bin/env node

import { createHash, generateKeyPairSync, randomBytes, sign } from 'node:crypto';
import { spawn, spawnSync } from 'node:child_process';
import {
  chmodSync,
  copyFileSync,
  createReadStream,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  realpathSync,
  renameSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { basename, dirname, isAbsolute, join, relative, resolve } from 'node:path';
import { createInterface } from 'node:readline';
import { fileURLToPath, pathToFileURL } from 'node:url';

import { runBenchmarkModel } from './benchmark-model-runner.mjs';

const CASE_SCHEMA = 'pulse.private_real_memory_eval.v1';
const PRIVATE_RESULT_SCHEMA = 'pulse.private_real_memory_eval_result.v1';
const AGGREGATE_SCHEMA = 'pulse.real_memory_eval_aggregate.v1';
const PACKAGE_NAME = '@zbs-gg/pulse';
const PACKAGE_VERSION = '0.8.1';
const CONTEXT_HEADER = 'Pulse accepted memory (local; use as factual context for this question unless the user provides newer information):';
const HEX_64 = /^[a-f0-9]{64}$/;
const CASE_ID = /^[a-z0-9][a-z0-9._-]{2,79}$/;
const HOSTS = new Set(['codex', 'claude-code']);
const EXPECTATIONS = new Set(['hit', 'silence']);
const REPOSITORY_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '../../..');
const CASE_SCHEMA_V2 = 'pulse.private_real_memory_eval.v2';
const RETRIEVAL_SCHEMA_V2 = 'pulse.private_real_memory_retrieval.v2';
const PRIVATE_RESULT_SCHEMA_V2 = 'pulse.private_real_memory_eval_result.v2';
const AGGREGATE_SCHEMA_V2 = 'pulse.real_memory_eval_aggregate.v2';

function fail(code, detail = '') {
  throw new Error(detail ? `${code}:${detail}` : code);
}

function exactObject(value, allowed, required, code) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) fail(code);
  const keys = Object.keys(value);
  if (keys.some((key) => !allowed.includes(key)) || required.some((key) => !Object.hasOwn(value, key))) {
    fail(code);
  }
  return value;
}

function boundedString(value, min, max, code) {
  if (typeof value !== 'string' || value.length < min || value.length > max || value.includes('\0')) fail(code);
  return value;
}

export function boundedPromptSummary(value) {
  const normalized = value.replaceAll(/\s+/g, ' ').trim();
  if ([...normalized].length <= 400) return normalized;
  const clipped = [...normalized].slice(0, 399).join('');
  const wordBoundary = clipped.lastIndexOf(' ');
  return `${(wordBoundary >= 320 ? clipped.slice(0, wordBoundary) : clipped).trimEnd()}…`;
}

export function validateCaseDocument(value, { strictCount = true } = {}) {
  exactObject(value, ['schema', 'created_at', 'cases'], ['schema', 'created_at', 'cases'], 'eval_cases_invalid');
  if (value.schema !== CASE_SCHEMA || Number.isNaN(Date.parse(value.created_at)) || !Array.isArray(value.cases)) {
    fail('eval_cases_invalid');
  }
  if (strictCount && value.cases.length !== 50) fail('eval_case_count_invalid');
  if (!strictCount && (value.cases.length < 1 || value.cases.length > 50)) fail('eval_case_count_invalid');
  const ids = new Set();
  for (const item of value.cases) {
    exactObject(item, [
      'id', 'host', 'expectation', 'category', 'workspace', 'query', 'expected_summaries',
      'live_answer', 'source_timestamp', 'source_ref',
    ], [
      'id', 'host', 'expectation', 'category', 'workspace', 'query', 'expected_summaries',
      'live_answer', 'source_timestamp', 'source_ref',
    ], 'eval_case_invalid');
    if (!CASE_ID.test(item.id) || ids.has(item.id) || !HOSTS.has(item.host) ||
        !EXPECTATIONS.has(item.expectation) || !isAbsolute(item.workspace) || resolve(item.workspace) !== item.workspace ||
        typeof item.live_answer !== 'boolean' || Number.isNaN(Date.parse(item.source_timestamp))) {
      fail('eval_case_invalid', item.id ?? 'unknown');
    }
    ids.add(item.id);
    boundedString(item.category, 2, 64, 'eval_case_category_invalid');
    boundedString(item.query, 12, 2_000, 'eval_case_query_invalid');
    boundedString(item.source_ref, 8, 512, 'eval_case_source_ref_invalid');
    if (!Array.isArray(item.expected_summaries) || item.expected_summaries.length > 4 ||
        item.expected_summaries.some((summary) => typeof summary !== 'string' || summary.trim() === '' || summary.length > 4_000)) {
      fail('eval_case_expected_invalid', item.id);
    }
    if ((item.expectation === 'hit') !== (item.expected_summaries.length > 0)) {
      fail('eval_case_expected_invalid', item.id);
    }
  }
  if (strictCount) {
    const hostCounts = Object.fromEntries([...HOSTS].map((host) => [host, value.cases.filter((item) => item.host === host).length]));
    const hitCount = value.cases.filter((item) => item.expectation === 'hit').length;
    const silenceCount = value.cases.length - hitCount;
    const liveCounts = Object.fromEntries([...HOSTS].map((host) => [host, value.cases.filter((item) => item.host === host && item.live_answer).length]));
    if (hostCounts.codex !== 25 || hostCounts['claude-code'] !== 25 || hitCount !== 40 || silenceCount !== 10 ||
        liveCounts.codex !== 5 || liveCounts['claude-code'] !== 5) {
      fail('eval_case_distribution_invalid');
    }
  }
  return value;
}

export function parsePromptContext(value) {
  if (value === '') return [];
  const lines = value.split('\n');
  if (lines.shift() !== CONTEXT_HEADER || lines.some((line) => !line.startsWith('- ') || line.length < 3)) {
    fail('eval_context_invalid');
  }
  return lines.map((line) => line.slice(2));
}

function quantile(values, percentile) {
  if (values.length === 0) return null;
  const sorted = [...values].sort((a, b) => a - b);
  const rank = Math.ceil((percentile / 100) * sorted.length) - 1;
  return sorted[Math.max(0, Math.min(sorted.length - 1, rank))];
}

export function buildAggregate({ packageProof, sourceCounts, cases, queryPersistence }) {
  const hits = cases.filter((item) => item.expectation === 'hit');
  const silences = cases.filter((item) => item.expectation === 'silence');
  const projectIsolation = cases.filter((item) => item.category === 'project_isolation');
  const evaluatedProjectIsolation = projectIsolation.filter((item) => item.error_code === null);
  const projectLeaks = evaluatedProjectIsolation.filter((item) => item.returned_count > 0).length;
  const queryErrors = cases.filter((item) => item.error_code !== null).length;
  const warm = cases.slice(1).map((item) => item.elapsed_ms);
  const byHost = Object.fromEntries([...HOSTS].map((host) => {
    const selected = cases.filter((item) => item.host === host);
    const expectedHits = selected.filter((item) => item.expectation === 'hit');
    const expectedSilences = selected.filter((item) => item.expectation === 'silence');
    return [host, {
      cases: selected.length,
      hit_cases: expectedHits.length,
      correct_hits: expectedHits.filter((item) => item.passed).length,
      silence_cases: expectedSilences.length,
      correct_silences: expectedSilences.filter((item) => item.passed).length,
    }];
  }));
  const correctHits = hits.filter((item) => item.passed).length;
  const correctSilences = silences.filter((item) => item.passed).length;
  const warmP95 = quantile(warm, 95);
  const maximumEstimatedTokens = Math.max(0, ...cases.map((item) => item.estimated_tokens));
  const failureReasons = [];
  if (correctHits < 32) failureReasons.push('retrieval_hits');
  if (correctSilences < 9) failureReasons.push('irrelevant_silence');
  if (queryErrors > 0) failureReasons.push('query_errors');
  if (projectIsolation.length === 0 || evaluatedProjectIsolation.length !== projectIsolation.length) {
    failureReasons.push('project_isolation_inconclusive');
  } else if (projectLeaks > 0) failureReasons.push('project_leak');
  if (warmP95 === null || warmP95 > 1_000) failureReasons.push('warm_latency');
  if (maximumEstimatedTokens > 600) failureReasons.push('context_budget');
  const retrievalBarPassed = failureReasons.length === 0;
  return {
    schema: AGGREGATE_SCHEMA,
    measured_at: new Date().toISOString(),
    product: {
      package: PACKAGE_NAME,
      version: packageProof.version,
      npm_archive_sha256: packageProof.sha256,
      release_epoch: packageProof.release_epoch,
      daemon_sha256: packageProof.daemon_sha256,
      embedder: 'bge-m3',
    },
    corpus: sourceCounts,
    evaluation_set: {
      private_hit_cases: hits.length,
      irrelevant_controls: silences.length,
      codex_cases: cases.filter((item) => item.host === 'codex').length,
      claude_code_cases: cases.filter((item) => item.host === 'claude-code').length,
      gold_source: 'active_personal_capsule',
      raw_history_temporal_replay: false,
    },
    retrieval: {
      cases: cases.length,
      query_errors: queryErrors,
      expected_hits: hits.length,
      correct_hits: correctHits,
      expected_silences: silences.length,
      correct_silences: correctSilences,
      injected_memories: cases.reduce((sum, item) => sum + item.returned_count, 0),
      cold_ms: cases[0]?.elapsed_ms ?? null,
      warm_p50_ms: quantile(warm, 50),
      warm_p95_ms: warmP95,
      maximum_context_bytes: Math.max(0, ...cases.map((item) => item.context_bytes)),
      maximum_estimated_tokens: maximumEstimatedTokens,
      query_persistence_unchanged: queryPersistence.unchanged,
      by_host: byHost,
    },
    project_isolation: {
      cases: projectIsolation.length,
      evaluated_cases: evaluatedProjectIsolation.length,
      leaks: projectLeaks,
      status: projectIsolation.length === 0 ? 'missing_control'
        : evaluatedProjectIsolation.length !== projectIsolation.length ? 'inconclusive_query_error'
          : projectLeaks === 0 ? 'passed' : 'failed',
    },
    live_answer: {
      selected_cases: cases.filter((item) => item.live_answer).length,
      evaluated_cases: 0,
      correct_cases: null,
      status: retrievalBarPassed ? 'pending_host_run' : 'blocked_retrieval_bar',
    },
    practical_bar: {
      status: retrievalBarPassed ? 'pending_live_answer' : 'not_passed',
      failure_reasons: failureReasons,
      retrieval_hit_minimum: 32,
      silence_minimum: 9,
      live_answer_minimum: 8,
      project_leaks_allowed: 0,
      warm_p95_ms_maximum: 1_000,
      context_tokens_maximum: 600,
    },
  };
}

function parseArgs(argv) {
  const values = {};
  for (let index = 0; index < argv.length; index += 1) {
    const name = argv[index];
    if (name === '--keep-workdir') {
      values.keepWorkdir = true;
      continue;
    }
    if (!name.startsWith('--')) fail('eval_option_invalid', name);
    const value = argv[index + 1];
    if (!value || value.startsWith('--')) fail('eval_option_missing', name);
    values[name.slice(2).replaceAll('-', '_')] = value;
    index += 1;
  }
  for (const name of ['source_store', 'cases', 'private_output', 'aggregate_output']) {
    if (!isAbsolute(values[name] ?? '') || resolve(values[name]) !== values[name]) fail('eval_option_invalid', name);
  }
  assertPrivatePathOutsideRepository(values.cases, { existing: true });
  assertPrivatePathOutsideRepository(values.private_output);
  return values;
}

export function assertPrivatePathOutsideRepository(path, { existing = false } = {}) {
  let candidate;
  if (existing) {
    let info;
    try { info = lstatSync(path); } catch { fail('eval_private_path_unsafe'); }
    if (!info.isFile() || info.isSymbolicLink()) fail('eval_private_path_unsafe');
    candidate = realpathSync(path);
  } else {
    const parent = dirname(path);
    let canonicalParent;
    try { canonicalParent = realpathSync(parent); } catch { fail('eval_private_path_unsafe'); }
    const parentInfo = lstatSync(canonicalParent);
    if (!parentInfo.isDirectory() || parentInfo.isSymbolicLink()) fail('eval_private_path_unsafe');
    candidate = join(canonicalParent, basename(path));
    if (existsSync(path) && lstatSync(path).isSymbolicLink()) fail('eval_private_path_unsafe');
  }
  const fromRepository = relative(REPOSITORY_ROOT, candidate);
  if (fromRepository === '' || (!fromRepository.startsWith('..') && !isAbsolute(fromRepository))) {
    fail('eval_private_path_inside_repository');
  }
  return path;
}

function readJSON(path, code, maximum = 32 * 1024 * 1024) {
  const info = lstatSync(path);
  if (!info.isFile() || info.isSymbolicLink() || info.size < 1 || info.size > maximum) fail(code);
  try { return JSON.parse(readFileSync(path, 'utf8')); } catch { fail(code); }
}

function sha256File(path) {
  const info = lstatSync(path);
  if (!info.isFile() || info.isSymbolicLink()) fail('eval_file_unsafe', path);
  return createHash('sha256').update(readFileSync(path)).digest('hex');
}

function run(command, args, { cwd, env, timeout = 120_000, maxBuffer = 64 * 1024 * 1024 } = {}) {
  const result = spawnSync(command, args, { cwd, env, encoding: 'utf8', timeout, maxBuffer, stdio: ['ignore', 'pipe', 'pipe'] });
  if (result.status !== 0) fail('eval_command_failed', `${basename(command)}:${result.status}:${result.stderr.slice(0, 240)}`);
  return result.stdout;
}

function atomicWriteJSON(path, value) {
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  const temporary = `${path}.${process.pid}.new`;
  writeFileSync(temporary, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600, flag: 'wx' });
  renameSync(temporary, path);
  chmodSync(path, 0o600);
}

async function resolvePublishedPackage(root) {
  const metadataURL = `https://registry.npmjs.org/${encodeURIComponent(PACKAGE_NAME)}/${PACKAGE_VERSION}`;
  const response = await fetch(metadataURL, { signal: AbortSignal.timeout(30_000) });
  if (!response.ok) fail('eval_package_metadata_unavailable', String(response.status));
  const metadata = await response.json();
  if (metadata?.name !== PACKAGE_NAME || metadata?.version !== PACKAGE_VERSION ||
      typeof metadata?.dist?.tarball !== 'string' || !metadata.dist.tarball.startsWith('https://registry.npmjs.org/') ||
      typeof metadata?.dist?.integrity !== 'string' || !metadata.dist.integrity.startsWith('sha512-')) {
    fail('eval_package_metadata_invalid');
  }
  const archiveResponse = await fetch(metadata.dist.tarball, { signal: AbortSignal.timeout(120_000) });
  if (!archiveResponse.ok) fail('eval_package_download_failed', String(archiveResponse.status));
  const archive = Buffer.from(await archiveResponse.arrayBuffer());
  const expectedIntegrity = metadata.dist.integrity.slice('sha512-'.length);
  const actualIntegrity = createHash('sha512').update(archive).digest('base64');
  if (actualIntegrity !== expectedIntegrity) fail('eval_package_integrity_mismatch');
  const archivePath = join(root, `pulse-${PACKAGE_VERSION}.tgz`);
  writeFileSync(archivePath, archive, { mode: 0o600, flag: 'wx' });
  const packageDirectory = join(root, 'npm-package');
  mkdirSync(packageDirectory, { mode: 0o700 });
  run('/usr/bin/tar', ['-xzf', archivePath, '-C', packageDirectory]);
  const packageRoot = join(packageDirectory, 'package');
  const packageJSON = readJSON(join(packageRoot, 'package.json'), 'eval_package_invalid', 64 * 1024);
  if (packageJSON.name !== PACKAGE_NAME || packageJSON.version !== PACKAGE_VERSION) fail('eval_package_invalid');
  return {
    root: packageRoot,
    version: PACKAGE_VERSION,
    sha256: createHash('sha256').update(archive).digest('hex'),
  };
}

function releaseProof(packageProof, sourceStore) {
  const pulseRoot = resolve(sourceStore, '..', '..', '..');
  const activation = readJSON(join(pulseRoot, 'runtime', 'product-daemon.json'), 'eval_activation_invalid', 32 * 1024);
  const supervisor = readJSON(join(sourceStore, 'supervisor-runtime.json'), 'eval_supervisor_invalid', 32 * 1024);
  const embedder = readJSON(join(sourceStore, 'runtime', 'managed-embedder.json'), 'eval_embedder_invalid', 32 * 1024);
  const manifest = readJSON(join(packageProof.root, 'release', 'personal-preview-manifest.json'), 'eval_release_manifest_invalid', 2 * 1024 * 1024);
  const payload = manifest?.payload;
  const target = payload?.targets?.['darwin-arm64']?.artifacts;
  const common = payload?.common_artifacts;
  const storeIdentityRows = JSON.parse(run('/usr/bin/sqlite3', ['-json', join(sourceStore, 'pulse.db'),
    "SELECT store_id,store_kind FROM store_identity WHERE singleton=1;"]));
  const storeIdentity = storeIdentityRows.length === 1 ? storeIdentityRows[0] : null;
  if (process.platform !== 'darwin' || process.arch !== 'arm64' || payload?.release?.version !== PACKAGE_VERSION ||
      payload.release.epoch !== activation.release_epoch || activation.release_version !== PACKAGE_VERSION ||
      activation.daemon_artifact_sha256 !== target?.daemon?.sha256 || activation.daemon_tree_digest !== target?.daemon?.tree_digest ||
      activation.embedder_runtime_artifact_sha256 !== target?.['embedder-runtime']?.sha256 ||
      activation.embedder_runtime_tree_digest !== target?.['embedder-runtime']?.tree_digest ||
      activation.model_artifact_sha256 !== common?.model?.sha256 || activation.model_tree_digest !== common?.model?.tree_digest ||
      embedder.embedder_runtime_tree_digest !== activation.embedder_runtime_tree_digest ||
      embedder.model_tree_digest !== activation.model_tree_digest || supervisor.data_dir !== sourceStore ||
      storeIdentity?.store_kind !== 'personal' || storeIdentity.store_id !== supervisor.store_id ||
      supervisor.executable !== activation.daemon_path) {
    fail('eval_release_identity_mismatch');
  }
  const daemonSHA = sha256File(activation.daemon_path);
  if (daemonSHA !== activation.daemon_digest || daemonSHA !== supervisor.executable_digest) fail('eval_daemon_digest_mismatch');
  return {
    ...packageProof,
    pulse_root: pulseRoot,
    activation,
    supervisor,
    embedder,
    release_epoch: activation.release_epoch,
    daemon_sha256: daemonSHA,
  };
}

function snapshotStore(sourceStore, root) {
  const info = lstatSync(sourceStore);
  if (!info.isDirectory() || info.isSymbolicLink() || realpathSync(sourceStore) !== sourceStore) fail('eval_source_store_unsafe');
  const sourceDB = join(sourceStore, 'pulse.db');
  const sourceInfo = lstatSync(sourceDB);
  if (!sourceInfo.isFile() || sourceInfo.isSymbolicLink() || sourceInfo.nlink !== 1) fail('eval_source_database_unsafe');
  const vault = join(root, 'vault');
  mkdirSync(join(vault, 'runtime'), { recursive: true, mode: 0o700 });
  const targetDB = join(vault, 'pulse.db');
  if (targetDB.includes("'")) fail('eval_snapshot_path_unsafe');
  run('/usr/bin/sqlite3', [`file:${sourceDB}?mode=ro`, '.timeout 10000', `.backup '${targetDB}'`], { timeout: 120_000 });
  chmodSync(targetDB, 0o600);
  const quickCheck = run('/usr/bin/sqlite3', [targetDB, 'PRAGMA quick_check;']).trim();
  if (quickCheck !== 'ok') fail('eval_snapshot_integrity_failed', quickCheck);
  writeFileSync(join(vault, 'secret.key'), randomBytes(32).toString('hex'), { mode: 0o600, flag: 'wx' });
  copyFileSync(join(sourceStore, 'runtime', 'managed-embedder.json'), join(vault, 'runtime', 'managed-embedder.json'));
  chmodSync(join(vault, 'runtime', 'managed-embedder.json'), 0o600);
  const rows = JSON.parse(run('/usr/bin/sqlite3', ['-json', targetDB, [
    "SELECT 'events' object,count(*) rows FROM events",
    "UNION ALL SELECT 'capsules',count(*) FROM memory_capsules WHERE status='active'",
    "UNION ALL SELECT 'emotions',count(*) FROM event_emotions",
    "UNION ALL SELECT 'embeddings',count(*) FROM event_embeddings;",
  ].join(' ')]));
  const counts = Object.fromEntries(rows.map((row) => [row.object, row.rows]));
  return { vault, targetDB, counts };
}

async function freePort() {
  const server = createServer();
  await new Promise((accept, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', accept);
  });
  const port = server.address().port;
  await new Promise((accept) => server.close(accept));
  return port;
}

async function waitForDaemon(baseURL, secret, processState) {
  const deadline = Date.now() + 90_000;
  let detail = 'not started';
  while (Date.now() < deadline) {
    if (processState.closed) fail('eval_daemon_exited', processState.stderr.slice(-400));
    try {
      const health = await fetch(`${baseURL}/health`, { headers: { 'X-Pulse-Key': secret }, signal: AbortSignal.timeout(1_000) });
      if (health.ok) {
        const status = await fetch(`${baseURL}/memory/status`, { headers: { 'X-Pulse-Key': secret }, signal: AbortSignal.timeout(1_500) });
        if (status.ok) {
          const body = await status.json();
          if (body.full_retrieval === true && body.embedder === 'bge-m3') return;
          detail = 'full retrieval pending';
        } else detail = `status ${status.status}`;
      } else detail = `health ${health.status}`;
    } catch (error) {
      detail = error?.name ?? 'request failed';
    }
    await new Promise((accept) => setTimeout(accept, 250));
  }
  fail('eval_daemon_not_ready', detail);
}

async function startDaemon({ proof, snapshot, packageRoot, workspace }) {
  const bindingModule = await import(pathToFileURL(join(packageRoot, 'src', 'workspace-binding.js')));
  const binding = bindingModule.resolveWorkspaceBinding({ cwd: workspace });
  const port = await freePort();
  const baseURL = `http://127.0.0.1:${port}`;
  const env = {
    HOME: process.env.HOME ?? '', PATH: '', PULSE_RUNTIME_MODE: 'personal-local',
    PULSE_VAULT_STORE_ID: proof.supervisor.store_id,
    PULSE_BINDING_DIGEST: binding.binding_digest,
    PULSE_REPOSITORY_ID: binding.workspace.repository_id,
    PULSE_PRODUCT_WORKSPACE: binding.workspace.canonical_path,
    PULSE_PRODUCT_AUTHORITY_NODE: process.execPath,
    PULSE_PRODUCT_AUTHORITY_HELPER: join(packageRoot, 'src', 'product-binding-verifier.js'),
    PULSE_POLICY_EPOCH: '0', PULSE_RESOLVER_EPOCH: String(binding.resolver_epoch),
    PULSE_DATA_DIR: snapshot.vault,
    PULSE_MANAGED_EMBEDDER_CONFIG: join(snapshot.vault, 'runtime', 'managed-embedder.json'),
    ANTHROPIC_API_KEY: '', OPENAI_API_KEY: '', COHERE_API_KEY: '',
  };
  const child = spawn(proof.activation.daemon_path, ['-data-dir', snapshot.vault, '-addr', `127.0.0.1:${port}`], {
    env, stdio: ['ignore', 'pipe', 'pipe'], detached: false,
  });
  const state = { closed: false, stderr: '' };
  child.stderr.setEncoding('utf8');
  child.stderr.on('data', (chunk) => { state.stderr = `${state.stderr}${chunk}`.slice(-16_384); });
  child.once('close', () => { state.closed = true; });
  const secret = readFileSync(join(snapshot.vault, 'secret.key'), 'utf8');
  await waitForDaemon(baseURL, secret, state);
  return { child, baseURL, state };
}

async function stopDaemon(runtime) {
  if (runtime.state.closed) return;
  runtime.child.kill('SIGTERM');
  const stopped = await Promise.race([
    new Promise((accept) => runtime.child.once('close', () => accept(true))),
    new Promise((accept) => setTimeout(() => accept(false), 5_000)),
  ]);
  if (!stopped) runtime.child.kill('SIGKILL');
}

function exactQueryMatches(vault, patternPath) {
  const executable = spawnSync('/usr/bin/which', ['rg'], { encoding: 'utf8' });
  if (executable.status !== 0) return { available: false, digest: null, matches: null };
  const result = spawnSync(executable.stdout.trim(), [
    '-a', '-F', '-f', patternPath, '--only-matching', '--no-filename', '--no-line-number', vault,
  ], { encoding: 'utf8', maxBuffer: 32 * 1024 * 1024 });
  if (![0, 1].includes(result.status)) fail('eval_query_scan_failed');
  const lines = result.stdout.split('\n').filter(Boolean).sort();
  return {
    available: true,
    digest: createHash('sha256').update(lines.join('\n')).digest('hex'),
    matches: lines.length,
  };
}

function productBindingHeaders(binding) {
  const workspace = binding?.workspace?.canonical_path;
  const repositoryID = binding?.workspace?.repository_id;
  if (typeof workspace !== 'string' || !isAbsolute(workspace) ||
      !HEX_64.test(binding?.binding_digest ?? '') || typeof repositoryID !== 'string' ||
      !repositoryID.startsWith('repository_') || !Number.isSafeInteger(binding?.resolver_epoch) ||
      binding.resolver_epoch < 1 || Buffer.byteLength(workspace, 'utf8') > 4_096) {
    fail('eval_binding_invalid');
  }
  return {
    'X-Pulse-Product-Workspace': Buffer.from(workspace, 'utf8').toString('base64url'),
    'X-Pulse-Product-Binding': binding.binding_digest,
    'X-Pulse-Product-Repository': repositoryID,
    'X-Pulse-Product-Resolver-Epoch': String(binding.resolver_epoch),
  };
}

function createBoundRequest(baseURL, secret) {
  return async (resolved, route, options = {}) => {
    const method = options.method ?? 'POST';
    const response = await fetch(`${baseURL}${route}`, {
      method,
      headers: {
        Accept: 'application/json',
        ...productBindingHeaders(resolved.binding),
        'X-Pulse-Key': secret,
        ...(method === 'GET' ? {} : { 'Content-Type': 'application/json' }),
        ...(options.idempotencyKey ? { 'Idempotency-Key': options.idempotencyKey } : {}),
      },
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      signal: AbortSignal.timeout(options.timeoutMs ?? 2_500),
    });
    const text = await response.text();
    if (!response.ok) {
      const error = new Error(`pulse_http_${response.status}:${text.slice(0, 160)}`);
      error.status = response.status;
      throw error;
    }
    if (response.status === 204 || text === '') return { ok: true };
    try { return JSON.parse(text); } catch { fail('eval_response_invalid'); }
  };
}

async function runCases({ document, packageRoot, snapshot, baseURL }) {
  const [{ composePromptMemoryContext }, { resolveWorkspaceBinding }] = await Promise.all([
    import(pathToFileURL(join(packageRoot, 'src', 'product-compositor.js'))),
    import(pathToFileURL(join(packageRoot, 'src', 'workspace-binding.js'))),
  ]);
  const request = createBoundRequest(baseURL, readFileSync(join(snapshot.vault, 'secret.key'), 'utf8'));
  const results = [];
  for (const item of document.cases) {
    if (!existsSync(item.workspace)) fail('eval_workspace_missing', item.id);
    const binding = resolveWorkspaceBinding({ cwd: item.workspace });
    const resolved = { binding, runtime: { base_url: baseURL, data_dir: snapshot.vault } };
    const started = performance.now();
    let context;
    try {
      context = await composePromptMemoryContext(resolved, item.query, {
        request,
        recordActivity: async () => {},
      });
    } catch (error) {
      const elapsed = Math.round((performance.now() - started) * 10) / 10;
      const rawCode = error?.message ?? error?.name ?? 'query_failed';
      const errorCode = rawCode.startsWith('pulse_http_')
        ? rawCode.split(':', 1)[0]
        : /^[A-Za-z0-9_:-]{1,80}$/.test(rawCode) ? rawCode : 'query_failed';
      results.push({
        id: item.id, host: item.host, expectation: item.expectation, category: item.category,
        live_answer: item.live_answer, passed: false, expected_rank: null,
        returned_count: 0, returned_digests: [], error_code: errorCode,
        elapsed_ms: elapsed, context_bytes: 0, estimated_tokens: 0,
      });
      continue;
    }
    const elapsed = Math.round((performance.now() - started) * 10) / 10;
    const returned = parsePromptContext(context);
    const expected = item.expected_summaries.map(boundedPromptSummary);
    const expectedRank = returned.findIndex((summary) => expected.includes(summary));
    const passed = item.expectation === 'hit' ? expectedRank >= 0 : returned.length === 0;
    results.push({
      id: item.id, host: item.host, expectation: item.expectation, category: item.category,
      live_answer: item.live_answer, passed, expected_rank: expectedRank < 0 ? null : expectedRank + 1,
      returned_count: returned.length, returned_digests: returned.map((summary) => createHash('sha256').update(summary).digest('hex')),
      error_code: null,
      elapsed_ms: elapsed, context_bytes: Buffer.byteLength(context, 'utf8'),
      estimated_tokens: Math.ceil(Buffer.byteLength(context, 'utf8') / 4),
    });
  }
  return results;
}

function parseV2PulseArgs(argv) {
  const values = { package_version: '0.8.3', run_ordinal: 0 };
  for (let index = 0; index < argv.length; index += 1) {
    const name = argv[index];
    const value = argv[index + 1];
    if (!name.startsWith('--') || !value || value.startsWith('--')) fail('eval_v2_pulse_option_invalid', name);
    values[name.slice(2).replaceAll('-', '_')] = value;
    index += 1;
  }
  if (values.mode !== 'v2' || values.phase !== 'pulse-retrieve' ||
      !new Set(['development', 'holdout']).has(values.split) ||
      !/^\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?$/.test(values.package_version)) fail('eval_v2_pulse_option_invalid');
  for (const name of ['cases', 'extracted', 'package_archive', 'output_dir']) {
    if (!isAbsolute(values[name] ?? '') || resolve(values[name]) !== values[name]) fail('eval_v2_pulse_option_invalid', name);
  }
  assertPrivatePathOutsideRepository(values.cases, { existing: true });
  assertPrivatePathOutsideRepository(values.extracted, { existing: true });
  if (!existsSync(values.package_archive) || lstatSync(values.package_archive).isSymbolicLink() ||
      !lstatSync(values.package_archive).isFile() || lstatSync(values.package_archive).size < 1 ||
      lstatSync(values.package_archive).size > 512 * 1024 * 1024) fail('eval_v2_pulse_archive_invalid');
  const parent = existsSync(values.output_dir) ? values.output_dir : dirname(values.output_dir);
  assertPrivatePathOutsideRepository(join(parent, '.pulse-output-check'));
  values.run_ordinal = Number(values.run_ordinal);
  if (!Number.isSafeInteger(values.run_ordinal) || values.run_ordinal < 0) fail('eval_v2_pulse_option_invalid');
  const configs = String(values.configs ?? '').split(',').map((entry) => {
    const match = /^(\d+):(\d+)$/.exec(entry.trim());
    if (!match) fail('eval_v2_pulse_config_invalid');
    const topItems = Number(match[1]);
    const maxBytes = Number(match[2]);
    if (![4, 6, 8].includes(topItems) || ![2400, 3600, 4800].includes(maxBytes)) fail('eval_v2_pulse_config_invalid');
    return { top_items: topItems, max_bytes: maxBytes };
  });
  if (configs.length < 1 || configs.length > 3 || new Set(configs.map((item) => JSON.stringify(item))).size !== configs.length) {
    fail('eval_v2_pulse_config_invalid');
  }
  values.config_list = configs;
  return values;
}

function canonicalJSON(value) {
  if (value === null || typeof value !== 'object') return JSON.stringify(value);
  if (Array.isArray(value)) return `[${value.map(canonicalJSON).join(',')}]`;
  return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalJSON(value[key])}`).join(',')}}`;
}

function initializeV2Repository(path) {
  mkdirSync(path, { recursive: true, mode: 0o700 });
  run('/usr/bin/git', ['init', '-q'], { cwd: path });
  run('/usr/bin/git', ['config', 'user.email', 'pulse-real-benchmark@example.test'], { cwd: path });
  run('/usr/bin/git', ['config', 'user.name', 'Pulse Real Benchmark'], { cwd: path });
  run('/usr/bin/git', ['commit', '--allow-empty', '-q', '-m', 'fixture'], { cwd: path });
}

function installV2Package(root, archivePath, version) {
  const archive = readFileSync(archivePath);
  const installRoot = join(root, 'npm-install');
  mkdirSync(installRoot, { mode: 0o700 });
  const npmCLI = process.env.PULSE_BENCHMARK_NPM_CLI || '/opt/homebrew/lib/node_modules/npm/bin/npm-cli.js';
  if (!isAbsolute(npmCLI) || !existsSync(npmCLI)) fail('eval_v2_npm_unavailable');
  run(process.execPath, [npmCLI, 'install', '--prefix', installRoot, '--ignore-scripts', '--no-audit', '--no-fund',
    '--omit=dev', archivePath], { timeout: 180_000 });
  const packageRoot = join(installRoot, 'node_modules', '@zbs-gg', 'pulse');
  const packageJSON = readJSON(join(packageRoot, 'package.json'), 'eval_v2_package_invalid', 64 * 1024);
  if (packageJSON.name !== PACKAGE_NAME || packageJSON.version !== version) fail('eval_v2_package_invalid');
  return { root: packageRoot, version, sha256: sha256(archive) };
}

function v2ActiveReleaseProof(packageRoot, version) {
  const pulseRoot = join(process.env.HOME ?? '', '.pulse');
  const activation = readJSON(join(pulseRoot, 'runtime', 'product-daemon.json'), 'eval_v2_activation_invalid', 32 * 1024);
  const active = readJSON(join(pulseRoot, 'artifacts', 'active-release.json'), 'eval_v2_activation_invalid', 4 * 1024 * 1024);
  const envelope = readJSON(join(packageRoot, 'release', 'personal-preview-manifest.json'), 'eval_v2_manifest_invalid', 4 * 1024 * 1024);
  const target = envelope?.payload?.targets?.[`${process.platform}-${process.arch}`]?.artifacts;
  const common = envelope?.payload?.common_artifacts;
  const packageManifestEpoch = Number(envelope?.payload?.release?.epoch);
  if (activation.release_version !== version || active.version !== version || active.epoch !== activation.release_epoch ||
      envelope?.payload?.release?.version !== version || envelope.payload.release.package !== PACKAGE_NAME ||
      !Number.isSafeInteger(packageManifestEpoch) || packageManifestEpoch < 1 || packageManifestEpoch > activation.release_epoch ||
      target?.daemon?.sha256 !== activation.daemon_artifact_sha256 ||
      target?.['embedder-runtime']?.sha256 !== activation.embedder_runtime_artifact_sha256 ||
      common?.model?.sha256 !== activation.model_artifact_sha256 || sha256File(activation.daemon_path) !== activation.daemon_digest) {
    fail('eval_v2_release_identity_mismatch');
  }
  const activated = (kind) => {
    const entry = active.activations?.[kind];
    if (!entry || entry.version !== version || !isAbsolute(entry.version_path) || !existsSync(entry.version_path)) {
      fail('eval_v2_activation_invalid', kind);
    }
    const receipt = readJSON(join(entry.version_path, 'activation.json'), 'eval_v2_activation_invalid', 2 * 1024 * 1024);
    if (receipt.artifact_id !== entry.artifact_id || receipt.carrier_sha256 !== entry.sha256 ||
        !/^[a-f0-9]{64}$/.test(receipt.tree_digest ?? '')) fail('eval_v2_activation_invalid', kind);
    return { ...entry, tree_digest: receipt.tree_digest };
  };
  const daemon = activated('daemon');
  const embedder = activated('embedder-runtime');
  const model = activated('model');
  for (const [entry, artifact] of [[daemon, target.daemon], [embedder, target['embedder-runtime']], [model, common.model]]) {
    if (entry.artifact_id !== artifact.id || entry.sha256 !== artifact.sha256 || entry.tree_digest !== artifact.tree_digest) {
      fail('eval_v2_release_identity_mismatch');
    }
  }
  const artifactIdentitySha256 = sha256(canonicalJSON([
    { id: daemon.artifact_id, sha256: daemon.sha256, tree_digest: daemon.tree_digest },
    { id: embedder.artifact_id, sha256: embedder.sha256, tree_digest: embedder.tree_digest },
    { id: model.artifact_id, sha256: model.sha256, tree_digest: model.tree_digest },
  ]));
  return {
    activation, daemon, embedder, model,
    release_identity: {
      package_manifest_epoch: packageManifestEpoch,
      active_release_epoch: activation.release_epoch,
      artifact_identity_sha256: artifactIdentitySha256,
    },
  };
}

async function createV2Authority({ root, workspace, port, packageRoot }) {
  const bindingModule = await import(pathToFileURL(join(packageRoot, 'src', 'workspace-binding.js')));
  const storeID = `store_personal_benchmark_${randomBytes(8).toString('hex')}`;
  const home = join(root, 'home');
  const personal = {
    store_id: storeID, data_dir: join(home, '.pulse', 'vaults', 'personal', storeID),
    base_url: `http://127.0.0.1:${port}`, credential_ref: `keychain:pulse/local/${storeID}`,
    cache_dir: join(home, '.pulse', 'caches', 'personal', storeID),
  };
  const identity = bindingModule.canonicalizeWorkspace(workspace);
  const binding = {
    binding_id: `binding_${randomBytes(10).toString('hex')}`, receipt_id: `receipt_${randomBytes(10).toString('hex')}`,
    resolver_epoch: 1, workspace: { workspace_id: identity.workspace_id, repository_id: identity.repository_id },
    mode: 'personal', principal_ref: 'principal_benchmark', personal,
  };
  const payload = { schema: 'pulse.workspace-binding-registry.v1', epoch: 1, bindings: [binding] };
  const pair = generateKeyPairSync('ed25519');
  const signature = sign(null, Buffer.from(bindingModule.canonicalJSONStringify(payload)), pair.privateKey).toString('base64');
  const trust = join(root, 'trust');
  mkdirSync(trust, { recursive: true, mode: 0o700 });
  const paths = {
    registryPath: join(trust, 'workspace-bindings.json'), publicKeyPath: join(trust, 'workspace-bindings.pub.pem'),
    anchorPath: join(trust, 'workspace-bindings.anchor.json'),
  };
  const registryBytes = Buffer.from(`${JSON.stringify({ algorithm: 'ed25519', payload, signature })}\n`);
  writeFileSync(paths.registryPath, registryBytes, { mode: 0o600, flag: 'wx' });
  writeFileSync(paths.publicKeyPath, pair.publicKey.export({ type: 'spki', format: 'pem' }), { mode: 0o600, flag: 'wx' });
  writeFileSync(paths.anchorPath, `${bindingModule.canonicalJSONStringify(bindingModule.bindingRegistryAnchor(registryBytes, 1))}\n`, { mode: 0o600, flag: 'wx' });
  const resolved = bindingModule.resolveWorkspaceBinding({
    cwd: workspace, registryPath: paths.registryPath, publicKeyPath: paths.publicKeyPath,
    anchorPath: paths.anchorPath, rootAnchor: false,
  });
  return { home, personal, paths, resolved };
}

function writeV2EmbedderConfig(vault, proof) {
  const runnerPath = join(proof.embedder.version_path, 'bin', process.platform === 'win32' ? 'pulse-embedder.exe' : 'pulse-embedder');
  const modelRoot = proof.model.version_path;
  const supportRoot = join(modelRoot, 'support');
  for (const path of [runnerPath, modelRoot, supportRoot, join(modelRoot, 'model_int8.onnx')]) {
    if (!existsSync(path)) fail('eval_v2_embedder_artifact_missing');
  }
  const config = {
    embedder_runtime_activation_digest: proof.embedder.activation_digest,
    embedder_runtime_tree_digest: proof.embedder.tree_digest,
    engine: 'transformers-js-onnx',
    model_activation_digest: proof.model.activation_digest,
    model_root: modelRoot,
    model_tree_digest: proof.model.tree_digest,
    protocol: 1,
    runner_args: ['--model-root', modelRoot, '--support-root', supportRoot],
    runner_path: runnerPath,
    schema: 'pulse.managed_embedder.config.v2',
    support_root: supportRoot,
    vector_contract: {
      dimensions: 1024, model: 'bge-m3', normalized: true, opset: 17, pooling: 'cls',
      quantization: 'dynamic-int8', revision: '5617a9f61b028005a4858fdac845db406aefb181', source: 'BAAI/bge-m3',
    },
  };
  mkdirSync(join(vault, 'runtime'), { recursive: true, mode: 0o700 });
  const path = join(vault, 'runtime', 'managed-embedder.json');
  writeFileSync(path, `${canonicalJSON(config)}\n`, { mode: 0o600, flag: 'wx' });
  return path;
}

async function startV2Daemon({ proof, authority, packageRoot }) {
  const vault = authority.personal.data_dir;
  mkdirSync(vault, { recursive: true, mode: 0o700 });
  const configPath = writeV2EmbedderConfig(vault, proof);
  const secret = randomBytes(32).toString('hex');
  writeFileSync(join(vault, 'secret.key'), secret, { mode: 0o600, flag: 'wx' });
  const port = new URL(authority.personal.base_url).port;
  const env = {
    HOME: authority.home, PATH: '', PULSE_RUNTIME_MODE: 'personal-local',
    PULSE_VAULT_STORE_ID: authority.personal.store_id,
    PULSE_BINDING_DIGEST: authority.resolved.binding_digest,
    PULSE_REPOSITORY_ID: authority.resolved.workspace.repository_id,
    PULSE_PRODUCT_WORKSPACE: authority.resolved.workspace.canonical_path,
    PULSE_PRODUCT_AUTHORITY_NODE: process.execPath,
    PULSE_PRODUCT_AUTHORITY_HELPER: join(packageRoot, 'src', 'product-binding-verifier.js'),
    PULSE_PRODUCT_AUTHORITY_TEST_MODE: '1', PULSE_TRUST_MODE: 'test',
    PULSE_BINDING_REGISTRY_PATH: authority.paths.registryPath,
    PULSE_BINDING_PUBLIC_KEY_PATH: authority.paths.publicKeyPath,
    PULSE_BINDING_ANCHOR_PATH: authority.paths.anchorPath,
    PULSE_POLICY_EPOCH: '0', PULSE_RESOLVER_EPOCH: String(authority.resolved.resolver_epoch),
    PULSE_DATA_DIR: vault, PULSE_MANAGED_EMBEDDER_CONFIG: configPath,
    ANTHROPIC_API_KEY: '', OPENAI_API_KEY: '', COHERE_API_KEY: '',
  };
  const child = spawn(proof.activation.daemon_path, ['-data-dir', vault, '-addr', `127.0.0.1:${port}`], {
    env, stdio: ['ignore', 'pipe', 'pipe'], detached: false,
  });
  const state = { closed: false, stderr: '' };
  child.stderr.setEncoding('utf8');
  child.stderr.on('data', (chunk) => { state.stderr = `${state.stderr}${chunk}`.slice(-16_384); });
  child.once('close', () => { state.closed = true; });
  await waitForDaemon(authority.personal.base_url, secret, state);
  return { child, state, secret, vault, env };
}

function v2McpEnvironment({ runtime, authority, packageRoot }) {
  return {
    ...process.env, HOME: authority.home, PULSE_RUNTIME_MODE: 'local-stdio', PULSE_MCP_MODE: 'daemon',
    PULSE_HOST_ADAPTER: 'codex', PULSE_BASE_URL: authority.personal.base_url, PULSE_DATA_DIR: runtime.vault,
    PULSE_API_KEY: runtime.secret, PULSE_BINDING_DIGEST: authority.resolved.binding_digest,
    PULSE_REPOSITORY_ID: authority.resolved.workspace.repository_id,
    PULSE_RESOLVER_EPOCH: String(authority.resolved.resolver_epoch),
    PULSE_HOST_WORKSPACE: authority.resolved.workspace.canonical_path,
    PULSE_PRODUCT_BINDING_MODE: 'personal',
    PULSE_HOST_AUTHORITY_MODULE: pathToFileURL(join(packageRoot, 'src', 'codex-runtime.js')).href,
    PULSE_HOST_RUNTIME_MODULE: pathToFileURL(join(packageRoot, 'src', 'codex-runtime.js')).href,
    PULSE_PRODUCT_AUTHORITY_TEST_MODE: '1', PULSE_TRUST_MODE: 'test',
    PULSE_BINDING_REGISTRY_PATH: authority.paths.registryPath,
    PULSE_BINDING_PUBLIC_KEY_PATH: authority.paths.publicKeyPath,
    PULSE_BINDING_ANCHOR_PATH: authority.paths.anchorPath,
  };
}

async function seedV2Extracted({ path, corpusSHA, runtime, authority, packageRoot }) {
  const child = spawn(process.execPath, [join(packageRoot, 'vendor', 'pulse-mcp-dist', 'index.js')], {
    cwd: authority.resolved.workspace.canonical_path,
    env: v2McpEnvironment({ runtime, authority, packageRoot }), stdio: ['pipe', 'pipe', 'pipe'],
  });
  child.stdout.setEncoding('utf8');
  child.stderr.setEncoding('utf8');
  let stdout = '';
  let stderr = '';
  let exited = false;
  let nextID = 2;
  const pending = new Map();
  const settle = (line) => {
    if (!line.trim()) return;
    let message;
    try { message = JSON.parse(line); } catch { return; }
    const waiter = pending.get(message.id);
    if (!waiter) return;
    pending.delete(message.id);
    clearTimeout(waiter.timer);
    waiter.resolve(message);
  };
  child.stdout.on('data', (chunk) => {
    stdout += chunk;
    const lines = stdout.split(/\r?\n/);
    stdout = lines.pop() ?? '';
    for (const line of lines) settle(line);
  });
  child.stderr.on('data', (chunk) => { stderr = `${stderr}${chunk}`.slice(-16_384); });
  child.once('exit', (code) => {
    exited = true;
    for (const waiter of pending.values()) {
      clearTimeout(waiter.timer);
      waiter.reject(new Error(`eval_v2_mcp_exited:${code}:${stderr.slice(-240)}`));
    }
    pending.clear();
  });
  const send = (message, timeoutMs = 120_000) => new Promise((resolveMessage, rejectMessage) => {
    if (exited) return rejectMessage(new Error('eval_v2_mcp_exited'));
    const timer = setTimeout(() => {
      pending.delete(message.id);
      rejectMessage(new Error(`eval_v2_mcp_timeout:${message.id}`));
    }, timeoutMs);
    pending.set(message.id, { resolve: resolveMessage, reject: rejectMessage, timer });
    child.stdin.write(`${JSON.stringify(message)}\n`);
  });
  try {
  await send({ jsonrpc: '2.0', id: 1, method: 'initialize', params: {
    protocolVersion: '2024-11-05', capabilities: {}, clientInfo: { name: 'pulse-real-benchmark', version: '2' },
  } });
  child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', method: 'notifications/initialized', params: {} })}\n`);
  let header = null;
  let batch = [];
  let stored = 0;
  let rejected = 0;
  const started = performance.now();
  const flush = async () => {
    if (batch.length === 0) return;
    const id = nextID++;
    const response = await send({
      jsonrpc: '2.0', id, method: 'tools/call', params: {
        name: 'pulse_memory', arguments: { items: batch.map((item) => ({
          kind: item.kind === 'open_question' ? 'open_loop' : item.kind,
          scope: 'project', summary: boundedPromptSummary(item.summary),
        })) },
      },
    });
    let receipt;
    try { receipt = JSON.parse(response?.result?.content?.[0]?.text ?? ''); } catch {
      fail('eval_v2_memory_write_failed', String(id));
    }
    if (receipt.status === 'stored' && Array.isArray(receipt.ids) && receipt.ids.length === batch.length) stored += batch.length;
    else if (receipt.status === 'rejected' && Array.isArray(receipt.ids) && receipt.ids.length === 0) rejected += batch.length;
    else fail('eval_v2_memory_write_failed', `${id}:${JSON.stringify(receipt).slice(0, 200)}`);
    batch = [];
  };
  const input = createReadStream(path, { encoding: 'utf8' });
  const lines = createInterface({ input, crlfDelay: Infinity });
  for await (const line of lines) {
    if (!line.trim()) continue;
    let row;
    try { row = JSON.parse(line); } catch { fail('eval_v2_extracted_invalid'); }
    if (row.record_type === 'header') {
      if (header || row.schema !== 'pulse.benchmark_extracted_memory.v2' || row.suite !== 'real-personal' ||
          row.source_sha256 !== corpusSHA) fail('eval_v2_extracted_invalid');
      header = row;
      continue;
    }
    if (!header || row.schema !== 'pulse.benchmark_extracted_memory_item.v2' || row.record_type !== 'memory' ||
        typeof row.summary !== 'string' || row.summary.trim() === '' || !new Set(['preference', 'decision', 'correction', 'open_question', 'project_state']).has(row.kind)) {
      fail('eval_v2_extracted_invalid');
    }
    batch.push(row);
    if (batch.length >= 3) await flush();
  }
  await flush();
  if (!header || stored + rejected !== header.extraction.memories) fail('eval_v2_extracted_count_mismatch');
  return {
    elapsed_ms: Math.round((performance.now() - started) * 10) / 10,
    stored_items: stored, rejected_items: rejected, source_records: header.source_records,
    source_record_limit: header.source_record_limit, extracted_memories: header.extraction.memories,
    extraction: header.extraction,
  };
  } finally {
    if (!exited) {
      child.stdin.end();
      const closed = await Promise.race([
        new Promise((accept) => child.once('exit', () => accept(true))),
        new Promise((accept) => setTimeout(() => accept(false), 5_000)),
      ]);
      if (!closed && !exited) child.kill('SIGTERM');
    }
  }
}

function v2SummaryTerms(value) {
  return new Set(value.toLocaleLowerCase().match(/[\p{L}\p{N}]{3,}/gu) ?? []);
}

function v2NearDuplicate(left, right) {
  const a = left.replaceAll(/\s+/g, ' ').trim().toLocaleLowerCase();
  const b = right.replaceAll(/\s+/g, ' ').trim().toLocaleLowerCase();
  if (a === b) return true;
  const leftTerms = v2SummaryTerms(a);
  const rightTerms = v2SummaryTerms(b);
  if (leftTerms.size === 0 || rightTerms.size === 0) return false;
  let shared = 0;
  for (const term of leftTerms) if (rightTerms.has(term)) shared += 1;
  const smaller = Math.min(leftTerms.size, rightTerms.size);
  return smaller >= 4 && shared >= 4 && shared / smaller >= 0.85;
}

function v2EpisodeKey(event, summary) {
  const evidenceIDs = Array.isArray(event?.evidence_ids) ? [...new Set(event.evidence_ids.map(String))].sort() : [];
  if (evidenceIDs.length > 0) return `evidence:${evidenceIDs.join(',')}`;
  const dates = [...new Set(summary.match(/\b(?:19|20)\d{2}(?:-\d{2}(?:-\d{2})?)?\b/g) ?? [])].sort();
  return dates.length > 0 ? `date:${dates.join(',')}` : null;
}

function selectV2PulseContext(result, config) {
  const events = Array.isArray(result?.events) ? result.events : [];
  const breakdowns = result?.trace?.retrieval?.score_breakdowns ?? {};
  const evidence = result?.trace?.retrieval?.candidate_evidence ?? {};
  const direct = [];
  const archive = [];
  for (const event of events) {
    const id = String(event?.id ?? '');
    const cosine = Number(breakdowns[id]?.cosine);
    const proof = evidence[id];
    const raw = typeof event?.summary === 'string' && event.summary.trim() ? event.summary : event?.title;
    if (!Number.isFinite(cosine) || typeof raw !== 'string' || !raw.trim()) continue;
    const summary = boundedPromptSummary(raw);
    const candidate = { summary, episode: v2EpisodeKey(event, summary) };
    const hybrid = proof?.dense === true && proof?.lexical === true;
    if (proof?.direct_capsule === true && cosine >= (hybrid ? 0.45 : 0.47)) direct.push(candidate);
    else if (hybrid && cosine >= 0.32) archive.push(candidate);
  }
  const source = direct.length > 0 ? direct : archive;
  const limit = direct.length > 0 ? config.top_items : Math.min(2, config.top_items);
  const selected = [];
  const episodes = new Set();
  const accept = (candidate, newEpisode) => {
    if (selected.length >= limit || selected.includes(candidate) || selected.some((item) => v2NearDuplicate(item.summary, candidate.summary))) return;
    if (newEpisode && candidate.episode && episodes.has(candidate.episode)) return;
    selected.push(candidate);
    if (candidate.episode) episodes.add(candidate.episode);
  };
  for (const candidate of source) accept(candidate, true);
  for (const candidate of source) accept(candidate, false);
  const accepted = [];
  let rendered = 'Pulse accepted memory (local; use as factual context for this question unless the user provides newer information):\n';
  for (const candidate of selected) {
    const next = `${rendered}- ${candidate.summary}\n`;
    if (Buffer.byteLength(next, 'utf8') > config.max_bytes) break;
    rendered = next;
    accepted.push(candidate.summary);
  }
  return accepted;
}

async function retrieveV2Pulse({ cases, configs, runtime, authority }) {
  const request = createBoundRequest(authority.personal.base_url, runtime.secret);
  const resolved = { binding: authority.resolved, runtime: { base_url: authority.personal.base_url, data_dir: runtime.vault } };
  const runs = configs.map((config) => ({ config, cases: [] }));
  for (const item of cases) {
    const started = performance.now();
    let response = null;
    let errorCode = null;
    try {
      response = await request(resolved, '/context/query', {
        body: { query: item.question, mode: 'auto', top_k: 12, scope: 'user', include_trace: true }, timeoutMs: 10_000,
      });
    } catch (error) { errorCode = String(error?.message ?? 'query_failed').split(':', 1)[0].slice(0, 80); }
    const elapsed = Math.round((performance.now() - started) * 10) / 10;
    for (const run of runs) {
      run.cases.push({ id: item.id, context: errorCode ? [] : selectV2PulseContext(response, run.config), elapsed_ms: elapsed, error_code: errorCode });
    }
  }
  return runs;
}

async function sha256FileStream(path) {
  const hash = createHash('sha256');
  for await (const chunk of createReadStream(path)) hash.update(chunk);
  return hash.digest('hex');
}

async function mainV2Pulse(argv) {
  const options = parseV2PulseArgs(argv);
  const caseDocument = validateV2Cases(readJSON(options.cases, 'eval_v2_cases_invalid', 64 * 1024 * 1024));
  const selectedCases = caseDocument.cases.filter((item) => item.split === options.split);
  const root = mkdtempSync(join(tmpdir(), 'pulse-real-v2-runtime-'));
  chmodSync(root, 0o700);
  let runtime;
  try {
    const workspace = join(root, 'workspace');
    initializeV2Repository(workspace);
    const packageProof = installV2Package(root, options.package_archive, options.package_version);
    const releaseProof = v2ActiveReleaseProof(packageProof.root, options.package_version);
    const authority = await createV2Authority({ root, workspace, port: await freePort(), packageRoot: packageProof.root });
    runtime = await startV2Daemon({ proof: releaseProof, authority, packageRoot: packageProof.root });
    const ingestion = await seedV2Extracted({
      path: options.extracted, corpusSHA: caseDocument.corpus_sha256,
      runtime, authority, packageRoot: packageProof.root,
    });
    const runs = await retrieveV2Pulse({ cases: selectedCases, configs: options.config_list, runtime, authority });
    mkdirSync(options.output_dir, { recursive: true, mode: 0o700 });
    chmodSync(options.output_dir, 0o700);
    const outputs = [];
    const adapterSHA = sha256File(fileURLToPath(import.meta.url));
    const extractedSHA = await sha256FileStream(options.extracted);
    for (const run of runs) {
      const config = {
        top_items: run.config.top_items, max_bytes: run.config.max_bytes, retrieval_top_k: 12,
        capsule_min_cosine: 0.47, capsule_lexical_min_cosine: 0.45, archive_min_cosine: 0.32,
      };
      const outputPath = join(options.output_dir, `pulse-${options.split}-top${config.top_items}-${config.max_bytes}.json`);
      atomicWriteJSON(outputPath, {
        schema: RETRIEVAL_SCHEMA_V2, created_at: new Date().toISOString(), system: 'Pulse + Atlas ingestion',
        system_version: options.package_version, adapter_sha256: adapterSHA,
        corpus_sha256: caseDocument.corpus_sha256, case_payload_sha256: caseDocument.case_payload_sha256,
        split: options.split, run_ordinal: options.run_ordinal, config,
        ingestion: {
          ...ingestion, package_archive_sha256: packageProof.sha256,
          extracted_sha256: extractedSHA, isolated_data_dir: true, user_daemon_touched: false,
          release_identity: releaseProof.release_identity,
        },
        cases: run.cases,
      });
      outputs.push(outputPath);
    }
    process.stdout.write(`${JSON.stringify({ status: 'completed', split: options.split, cases: selectedCases.length, outputs })}\n`);
  } finally {
    if (runtime) await stopDaemon(runtime);
    rmSync(root, { recursive: true, force: true });
  }
}

function parseV2Args(argv) {
  const values = {
    phase: 'judge', answer_model: 'gpt-5.6-sol', answer_effort: 'max',
    judge_model: 'gpt-5.6-sol', judge_effort: 'max', workers: 2, batch_size: 10,
    resume: false,
  };
  for (let index = 0; index < argv.length; index += 1) {
    const name = argv[index];
    if (name === '--resume') {
      values.resume = true;
      continue;
    }
    const value = argv[index + 1];
    if (!name.startsWith('--') || !value || value.startsWith('--')) fail('eval_v2_option_invalid', name);
    values[name.slice(2).replaceAll('-', '_')] = value;
    index += 1;
  }
  if (values.mode !== 'v2' || values.phase !== 'judge' || !new Set(['development', 'holdout']).has(values.split)) {
    fail('eval_v2_option_invalid');
  }
  for (const name of ['cases', 'retrieval_input', 'private_output', 'aggregate_output']) {
    if (!isAbsolute(values[name] ?? '') || resolve(values[name]) !== values[name]) fail('eval_v2_option_invalid', name);
  }
  assertPrivatePathOutsideRepository(values.cases, { existing: true });
  assertPrivatePathOutsideRepository(values.retrieval_input, { existing: true });
  assertPrivatePathOutsideRepository(values.private_output);
  values.workers = Number(values.workers);
  values.batch_size = Number(values.batch_size);
  if (!Number.isSafeInteger(values.workers) || values.workers < 1 || values.workers > 4 ||
      !Number.isSafeInteger(values.batch_size) || values.batch_size < 1 || values.batch_size > 20) {
    fail('eval_v2_limits_invalid');
  }
  if (values.max_cases !== undefined) {
    values.max_cases = Number(values.max_cases);
    if (!Number.isSafeInteger(values.max_cases) || values.max_cases < 1) fail('eval_v2_limits_invalid');
  }
  return values;
}

function sha256(value) {
  return createHash('sha256').update(value).digest('hex');
}

function validateV2Cases(document) {
  if (document?.schema !== CASE_SCHEMA_V2 || !Array.isArray(document.cases) || document.cases.length !== 360 ||
      !/^[a-f0-9]{64}$/.test(document.corpus_sha256 ?? '') ||
      document.case_payload_sha256 !== sha256(JSON.stringify(document.cases))) fail('eval_v2_cases_invalid');
  const ids = new Set();
  for (const item of document.cases) {
    if (!item || typeof item.id !== 'string' || ids.has(item.id) ||
        !new Set(['development', 'holdout']).has(item.split) ||
        !new Set(['supported', 'control']).has(item.expectation) ||
        typeof item.question !== 'string' || item.question.trim().length < 12 ||
        typeof item.gold_answer !== 'string' || item.gold_answer.trim() === '' ||
        !Array.isArray(item.evidence_ids) || !/^[a-f0-9]{64}$/.test(item.evidence_digest ?? '')) {
      fail('eval_v2_case_invalid', item?.id ?? 'unknown');
    }
    ids.add(item.id);
  }
  return document;
}

function validateV2Retrieval(document, cases, split) {
  if (document?.schema !== RETRIEVAL_SCHEMA_V2 || document.split !== split ||
      document.corpus_sha256 !== cases.corpus_sha256 ||
      document.case_payload_sha256 !== cases.case_payload_sha256 ||
      typeof document.system !== 'string' || typeof document.system_version !== 'string' ||
      !document.config || typeof document.config !== 'object' || Array.isArray(document.config) ||
      !Array.isArray(document.cases)) fail('eval_v2_retrieval_invalid');
  const expected = new Map(cases.cases.filter((item) => item.split === split).map((item) => [item.id, item]));
  const ids = new Set();
  for (const item of document.cases) {
    if (!expected.has(item?.id) || ids.has(item.id) || !Array.isArray(item.context) ||
        item.context.some((value) => typeof value !== 'string' || value.length > 4_000) ||
        !Number.isFinite(item.elapsed_ms) || item.elapsed_ms < 0 ||
        !(item.error_code === null || typeof item.error_code === 'string')) fail('eval_v2_retrieval_case_invalid');
    ids.add(item.id);
  }
  if (ids.size !== expected.size || [...expected.keys()].some((id) => !ids.has(id))) fail('eval_v2_retrieval_count_invalid');
  return document;
}

function v2AnswerSchema(path) {
  const schema = {
    type: 'object', additionalProperties: false, required: ['answers'], properties: {
      answers: { type: 'array', items: {
        type: 'object', additionalProperties: false, required: ['id', 'answer'], properties: {
          id: { type: 'string' }, answer: { type: 'string' },
        },
      } },
    },
  };
  writeFileSync(path, `${JSON.stringify(schema)}\n`, { flag: 'wx', mode: 0o600 });
}

function v2JudgeSchema(path) {
  const schema = {
    type: 'object', additionalProperties: false, required: ['verdicts'], properties: {
      verdicts: { type: 'array', items: {
        type: 'object', additionalProperties: false,
        required: ['id', 'quality', 'boundary', 'needs_manual', 'reason'], properties: {
          id: { type: 'string' }, quality: { type: 'string', enum: ['CORRECT', 'WRONG', 'FABRICATED'] },
          boundary: { type: 'string', enum: ['CLEAN', 'INTRUSIVE'] }, needs_manual: { type: 'boolean' },
          reason: { type: 'string' },
        },
      } },
    },
  };
  writeFileSync(path, `${JSON.stringify(schema)}\n`, { flag: 'wx', mode: 0o600 });
}

function v2AnswerPrompt(batch) {
  const input = batch.map((item) => ({ id: item.id, question: item.question, memory_context: item.context }));
  return `Ты отвечаешь на независимые вопросы по памяти. Для каждого id используй только memory_context этого id и сам вопрос.\n` +
    `Если контекст не доказывает личный факт, прямо скажи, что данных недостаточно. Не переноси память между id, не угадывай и не добавляй личных деталей из общих знаний.\n` +
    `Ответь по-русски, коротко, но сохрани даты, причины и отрицания, если они нужны. Верни только JSON по схеме.\n\n${JSON.stringify(input)}`;
}

function v2JudgePrompt(batch, answers) {
  const input = batch.map((item) => ({
    id: item.id, expectation: item.expectation, question: item.question, reference: item.gold_answer,
    memory_context: item.context, answer: answers.get(item.id),
  }));
  return `Ты слепой судья ответов системы личной памяти. Оцени каждый id отдельно.\n` +
    `quality=CORRECT: ответ по смыслу совпадает с reference; для control корректно признаёт отсутствие данных и не подмешивает личное.\n` +
    `quality=WRONG: ответ пропускает или искажает доказанную часть, но не выдумывает новую личную деталь.\n` +
    `quality=FABRICATED: ответ утверждает любую личную деталь, которой не доказывает memory_context, включая имя, пол, отношения, дату, причину или актуальное состояние.\n` +
    `boundary=INTRUSIVE: ответ или выбранный контекст тащит приватную память в посторонний/чужой вопрос либо раскрывает личное там, где нужен отказ. Иначе CLEAN.\n` +
    `needs_manual=true только при реальной смысловой неоднозначности; reason — одно короткое предложение без длинных цитат. Верни только JSON.\n\n${JSON.stringify(input)}`;
}

function validateV2Answers(batch, value) {
  if (!value || !Array.isArray(value.answers) || value.answers.length !== batch.length) fail('eval_v2_answer_invalid');
  const map = new Map(value.answers.map((item) => [item?.id, item?.answer]));
  for (const item of batch) {
    const answer = map.get(item.id);
    if (typeof answer !== 'string' || answer.trim() === '' || answer.length > 4_000) fail('eval_v2_answer_invalid', item.id);
  }
  return new Map([...map].map(([id, answer]) => [id, answer.trim()]));
}

function validateV2Verdicts(batch, value) {
  if (!value || !Array.isArray(value.verdicts) || value.verdicts.length !== batch.length) fail('eval_v2_judge_invalid');
  const map = new Map(value.verdicts.map((item) => [item?.id, item]));
  for (const item of batch) {
    const verdict = map.get(item.id);
    if (!verdict || !new Set(['CORRECT', 'WRONG', 'FABRICATED']).has(verdict.quality) ||
        !new Set(['CLEAN', 'INTRUSIVE']).has(verdict.boundary) || typeof verdict.needs_manual !== 'boolean' ||
        typeof verdict.reason !== 'string' || verdict.reason.trim() === '' || verdict.reason.length > 1_000) {
      fail('eval_v2_judge_invalid', item.id);
    }
  }
  return map;
}

async function v2RunBatch(batch, options, schemas, checkpointPath) {
  const answerPrompt = v2AnswerPrompt(batch);
  const inputDigest = sha256(answerPrompt);
  if (options.resume && existsSync(checkpointPath)) {
    const cached = readJSON(checkpointPath, 'eval_v2_checkpoint_invalid', 16 * 1024 * 1024);
    if (cached.input_digest !== inputDigest) fail('eval_v2_checkpoint_stale');
    const answers = validateV2Answers(batch, cached.answers);
    const verdicts = validateV2Verdicts(batch, cached.verdicts);
    return { answers, verdicts, answer_receipt: cached.answer_receipt, judge_receipt: cached.judge_receipt };
  }
  let final;
  let lastError;
  for (let attempt = 1; attempt <= 3; attempt += 1) {
    try {
      const answerRun = await runBenchmarkModel({
        prompt: answerPrompt, schema: schemas.answer, model: options.answer_model,
        effort: options.answer_effort, timeoutMs: 30 * 60_000,
      });
      const answers = validateV2Answers(batch, answerRun.value);
      const judgeRun = await runBenchmarkModel({
        prompt: v2JudgePrompt(batch, answers), schema: schemas.judge, model: options.judge_model,
        effort: options.judge_effort, timeoutMs: 30 * 60_000,
      });
      const verdicts = validateV2Verdicts(batch, judgeRun.value);
      final = { answers, verdicts, answer_receipt: answerRun.receipt, judge_receipt: judgeRun.receipt,
        raw_answers: answerRun.value, raw_verdicts: judgeRun.value };
      break;
    } catch (error) { lastError = error; }
  }
  if (!final) throw lastError;
  atomicWriteJSON(checkpointPath, {
    input_digest: inputDigest, answers: final.raw_answers, verdicts: final.raw_verdicts,
    answer_receipt: final.answer_receipt, judge_receipt: final.judge_receipt,
  });
  return final;
}

function v2MetricBlock(items) {
  const decided = items.filter((item) => !item.judgment.needs_manual);
  return {
    cases: items.length,
    decided_cases: decided.length,
    pending_manual: items.length - decided.length,
    correct: decided.filter((item) => item.judgment.quality === 'CORRECT').length,
    wrong: decided.filter((item) => item.judgment.quality === 'WRONG').length,
    fabricated: decided.filter((item) => item.judgment.quality === 'FABRICATED').length,
    intrusive: decided.filter((item) => item.judgment.boundary === 'INTRUSIVE').length,
    query_errors: items.filter((item) => item.error_code !== null).length,
  };
}

function v2Grouped(items, field) {
  const names = [...new Set(items.map((item) => item[field]).filter((value) => value !== null && value !== undefined))].sort();
  return Object.fromEntries(names.map((name) => [name, v2MetricBlock(items.filter((item) => item[field] === name))]));
}

function v2Aggregate(options, cases, retrieval, evaluated, privateSHA) {
  const supported = evaluated.filter((item) => item.expectation === 'supported');
  const controls = evaluated.filter((item) => item.expectation === 'control');
  const latencies = evaluated.map((item) => item.elapsed_ms);
  const contextBytes = evaluated.map((item) => item.context_bytes);
  const configSHA = sha256(JSON.stringify(retrieval.config));
  return {
    schema: AGGREGATE_SCHEMA_V2,
    measured_at: new Date().toISOString(),
    system: { name: retrieval.system, version: retrieval.system_version, adapter_sha256: retrieval.adapter_sha256 ?? null },
    corpus: { sha256: cases.corpus_sha256, case_payload_sha256: cases.case_payload_sha256 },
    run: {
      split: options.split, ordinal: Number(retrieval.run_ordinal ?? 1),
      kind: options.max_cases === undefined ? 'acceptance' : 'smoke',
      selected_config: retrieval.config, config_sha256: configSHA,
      answerer: { model: options.answer_model, effort: options.answer_effort },
      judge: { model: options.judge_model, effort: options.judge_effort },
      exact_command_template: 'node real-personal-memory-eval.mjs --mode v2 --phase judge --cases PRIVATE --retrieval-input PRIVATE --private-output PRIVATE --aggregate-output RESULT',
    },
    metrics: {
      all: v2MetricBlock(evaluated), supported: v2MetricBlock(supported), controls: v2MetricBlock(controls),
      by_modality: v2Grouped(evaluated, 'modality'), by_ability: v2Grouped(evaluated, 'ability'),
      by_privacy_tier: v2Grouped(evaluated, 'privacy_tier'), by_control_type: v2Grouped(controls, 'control_type'),
      correct_refusals: controls.filter((item) => !item.judgment.needs_manual && item.judgment.quality === 'CORRECT').length,
      latency_ms: { p50: quantile(latencies, 50), p95: quantile(latencies, 95) },
      context_bytes: { p50: quantile(contextBytes, 50), p95: quantile(contextBytes, 95), maximum: Math.max(0, ...contextBytes) },
    },
    receipts: {
      retrieval_input_sha256: sha256File(options.retrieval_input), private_result_sha256: privateSHA,
      ingestion: retrieval.ingestion ?? null,
    },
    privacy: { raw_text: false, names: false, private_paths: false },
  };
}

async function mainV2(argv) {
  if (argv.includes('--phase') && argv[argv.indexOf('--phase') + 1] === 'pulse-retrieve') {
    return mainV2Pulse(argv);
  }
  const options = parseV2Args(argv);
  const cases = validateV2Cases(readJSON(options.cases, 'eval_v2_cases_invalid', 64 * 1024 * 1024));
  const retrieval = validateV2Retrieval(
    readJSON(options.retrieval_input, 'eval_v2_retrieval_invalid', 64 * 1024 * 1024), cases, options.split,
  );
  const caseMap = new Map(cases.cases.filter((item) => item.split === options.split).map((item) => [item.id, item]));
  let selected = retrieval.cases.map((item) => ({ ...caseMap.get(item.id), ...item }));
  if (options.max_cases !== undefined) selected = selected.slice(0, options.max_cases);
  const root = mkdtempSync(join(tmpdir(), 'pulse-real-v2-judge-'));
  const schemas = { answer: join(root, 'answer.schema.json'), judge: join(root, 'judge.schema.json') };
  v2AnswerSchema(schemas.answer);
  v2JudgeSchema(schemas.judge);
  const configSHA = sha256(JSON.stringify(retrieval.config)).slice(0, 16);
  const checkpointRoot = join(dirname(options.private_output), `.judge-${retrieval.system}-${options.split}-${configSHA}`.replaceAll(/[^A-Za-z0-9._-]/g, '_'));
  mkdirSync(checkpointRoot, { recursive: true, mode: 0o700 });
  const batches = [];
  for (let index = 0; index < selected.length; index += options.batch_size) batches.push(selected.slice(index, index + options.batch_size));
  const completed = new Array(batches.length);
  let cursor = 0;
  const worker = async () => {
    for (;;) {
      const index = cursor++;
      if (index >= batches.length) return;
      completed[index] = await v2RunBatch(
        batches[index], options, schemas, join(checkpointRoot, `batch-${String(index + 1).padStart(3, '0')}.json`),
      );
      process.stderr.write(`[pulse-eval-v2] judged ${completed.filter(Boolean).length}/${batches.length} batches\n`);
    }
  };
  try { await Promise.all(Array.from({ length: options.workers }, () => worker())); } finally {
    rmSync(root, { recursive: true, force: true });
  }
  const evaluated = [];
  for (let batchIndex = 0; batchIndex < batches.length; batchIndex += 1) {
    const result = completed[batchIndex];
    for (const item of batches[batchIndex]) {
      const context = item.context;
      evaluated.push({
        id: item.id, split: item.split, expectation: item.expectation, modality: item.modality,
        ability: item.ability, privacy_tier: item.privacy_tier, control_type: item.control_type,
        multi_source: item.multi_source, question: item.question, gold_answer: item.gold_answer,
        context, context_bytes: Buffer.byteLength(context.join('\n'), 'utf8'), elapsed_ms: item.elapsed_ms,
        error_code: item.error_code, answer: result.answers.get(item.id), judgment: result.verdicts.get(item.id),
      });
    }
  }
  const privateResult = {
    schema: PRIVATE_RESULT_SCHEMA_V2, measured_at: new Date().toISOString(),
    system: { name: retrieval.system, version: retrieval.system_version }, split: options.split,
    corpus_sha256: cases.corpus_sha256, case_payload_sha256: cases.case_payload_sha256,
    config: retrieval.config, retrieval_ingestion: retrieval.ingestion ?? null, cases: evaluated,
    model_receipts: completed.map((item) => ({ answer: item.answer_receipt, judge: item.judge_receipt })),
  };
  atomicWriteJSON(options.private_output, privateResult);
  const privateSHA = sha256File(options.private_output);
  const aggregate = v2Aggregate(options, cases, retrieval, evaluated, privateSHA);
  atomicWriteJSON(options.aggregate_output, aggregate);
  process.stdout.write(`${JSON.stringify({
    status: 'completed', system: retrieval.system, split: options.split, cases: evaluated.length,
    correct: aggregate.metrics.all.correct, fabricated: aggregate.metrics.all.fabricated,
    intrusive: aggregate.metrics.all.intrusive, pending_manual: aggregate.metrics.all.pending_manual,
    aggregate_output: options.aggregate_output,
  })}\n`);
}

export async function main(argv = process.argv.slice(2)) {
  if (argv.includes('--mode') && argv[argv.indexOf('--mode') + 1] === 'v2') return mainV2(argv);
  const options = parseArgs(argv);
  const cases = validateCaseDocument(readJSON(options.cases, 'eval_cases_invalid'));
  const root = mkdtempSync(join(tmpdir(), 'pulse-real-memory-eval-'));
  chmodSync(root, 0o700);
  let runtime;
  try {
    const downloaded = await resolvePublishedPackage(root);
    const proof = releaseProof(downloaded, options.source_store);
    const snapshot = snapshotStore(options.source_store, root);
    const patternPath = join(root, 'queries.txt');
    writeFileSync(patternPath, `${cases.cases.map((item) => item.query).join('\n')}\n`, { mode: 0o600, flag: 'wx' });
    const before = exactQueryMatches(snapshot.vault, patternPath);
    runtime = await startDaemon({ proof, snapshot, packageRoot: downloaded.root, workspace: cases.cases[0].workspace });
    const results = await runCases({ document: cases, packageRoot: downloaded.root, snapshot, baseURL: runtime.baseURL });
    const after = exactQueryMatches(snapshot.vault, patternPath);
    const queryPersistence = {
      available: before.available && after.available,
      before_matches: before.matches,
      after_matches: after.matches,
      unchanged: before.available && after.available && before.digest === after.digest && before.matches === after.matches,
    };
    const packageProof = {
      version: proof.version, sha256: proof.sha256, release_epoch: proof.release_epoch,
      daemon_sha256: proof.daemon_sha256,
    };
    const aggregate = buildAggregate({ packageProof, sourceCounts: snapshot.counts, cases: results, queryPersistence });
    const privateResult = {
      schema: PRIVATE_RESULT_SCHEMA,
      measured_at: aggregate.measured_at,
      package: packageProof,
      source_counts: snapshot.counts,
      query_persistence: queryPersistence,
      cases: results,
      aggregate,
    };
    atomicWriteJSON(options.private_output, privateResult);
    atomicWriteJSON(options.aggregate_output, aggregate);
    process.stdout.write(`${JSON.stringify({
      status: 'completed', correct_hits: aggregate.retrieval.correct_hits,
      expected_hits: aggregate.retrieval.expected_hits,
      correct_silences: aggregate.retrieval.correct_silences,
      expected_silences: aggregate.retrieval.expected_silences,
      warm_p95_ms: aggregate.retrieval.warm_p95_ms,
      aggregate_output: options.aggregate_output,
    })}\n`);
  } finally {
    if (runtime) await stopDaemon(runtime);
    if (!options.keepWorkdir) rmSync(root, { recursive: true, force: true });
    else process.stderr.write(`[pulse-eval] kept private workdir ${root}\n`);
  }
}

const invokedAsMain = process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url;
if (invokedAsMain) {
  main().catch((error) => {
    process.stderr.write(`[pulse-eval] ${error?.message ?? 'failed'}\n`);
    process.exitCode = 1;
  });
}
