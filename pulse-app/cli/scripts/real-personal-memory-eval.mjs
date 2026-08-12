#!/usr/bin/env node

import { createHash, randomBytes } from 'node:crypto';
import { spawn, spawnSync } from 'node:child_process';
import {
  chmodSync,
  copyFileSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  realpathSync,
  renameSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { basename, dirname, isAbsolute, join, relative, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const CASE_SCHEMA = 'pulse.private_real_memory_eval.v1';
const PRIVATE_RESULT_SCHEMA = 'pulse.private_real_memory_eval_result.v1';
const AGGREGATE_SCHEMA = 'pulse.real_memory_eval_aggregate.v1';
const PACKAGE_NAME = '@zbs-gg/pulse';
const PACKAGE_VERSION = '0.8.0';
const CONTEXT_HEADER = 'Pulse accepted memory (local; use as factual context for this question unless the user provides newer information):';
const HEX_64 = /^[a-f0-9]{64}$/;
const CASE_ID = /^[a-z0-9][a-z0-9._-]{2,79}$/;
const HOSTS = new Set(['codex', 'claude-code']);
const EXPECTATIONS = new Set(['hit', 'silence']);
const REPOSITORY_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '../../..');

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

export async function main(argv = process.argv.slice(2)) {
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
