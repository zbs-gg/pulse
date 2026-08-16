import { chmodSync, existsSync, lstatSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';

import { CODEX_MEMORY_MODEL, runSyntheticMemoryCanary } from './codex-subscription-runner.js';
import { runHistoricalIngestWorker } from './historical-ingest-worker.js';

const JOB_ID = /^job_[a-f0-9]{16,64}$/;
const DIGEST = /^[a-f0-9]{64}$/;
const SESSION_ID = /^[a-f0-9-]{16,64}$/;
const TERMINAL = new Set(['manifest_ready', 'nothing_to_import', 'approval_ready', 'canceled', 'stale']);
const RUNNABLE = new Set(['extracting']);
const COMMITTED = new Set(['committed_indexing', 'indexing_failed', 'retrieval_ready']);
const SAFE_STATUS_KEYS = new Set([
  'schema', 'job_id', 'state', 'generation', 'total_units', 'accepted_units', 'pending_units',
  'leased_units', 'manifest_revision', 'manifest_digest', 'usage', 'reason_code', 'snapshot_digest',
  'runner_contract_digest', 'source_root_count', 'source_file_count', 'source_bytes', 'evidence_bytes', 'egress_authorized',
]);

export class HistoricalIngestCLIError extends Error {
  constructor(code) {
    super(code);
    this.name = 'HistoricalIngestCLIError';
    this.code = code;
  }
}

function fail(code) {
  throw new HistoricalIngestCLIError(code);
}

function exactStatus(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value) ||
      Object.keys(value).some((key) => !SAFE_STATUS_KEYS.has(key)) ||
      value.schema !== 'pulse.historical_ingest.status.v1' || !JOB_ID.test(value.job_id ?? '') ||
      typeof value.state !== 'string' || !DIGEST.test(value.snapshot_digest ?? '') ||
      !DIGEST.test(value.runner_contract_digest ?? '') || !Number.isSafeInteger(value.source_root_count) ||
      value.source_root_count < 1 || !Number.isSafeInteger(value.source_file_count) || value.source_file_count < 1 ||
      !Number.isSafeInteger(value.source_bytes) || value.source_bytes < 1 ||
      !Number.isSafeInteger(value.evidence_bytes) || value.evidence_bytes < 1 ||
      typeof value.egress_authorized !== 'boolean') {
    fail('historical_status_invalid');
  }
  return value;
}

function option(argv, name) {
  const index = argv.indexOf(name);
  if (index < 0) return undefined;
  const value = argv[index + 1];
  if (value === undefined || value.startsWith('--')) fail(`historical_${name.slice(2)}_missing`);
  return value;
}

function assertedJobID(value) {
  if (!JOB_ID.test(value ?? '')) fail('historical_job_id_invalid');
  return value;
}

function formatStatus(status) {
  const usage = status.usage ?? {};
  const memoryState = COMMITTED.has(status.state)
    ? '[pulse] Memory writes: committed through the reviewed Home receipt'
    : '[pulse] Memory writes: 0 (dry run)';
  return [
    `[pulse] History job ${status.job_id}: ${status.state}`,
    `[pulse] Snapshot: ${status.source_root_count} root trees · ${status.source_file_count} source prefixes · ${formatBytes(status.source_bytes)} captured`,
    `[pulse] Model egress: ${status.total_units} isolated ${CODEX_MEMORY_MODEL} turns · ${formatBytes(status.evidence_bytes)} normalized evidence`,
    `[pulse] Progress: ${status.accepted_units}/${status.total_units} accepted · ${status.pending_units} pending · ${status.leased_units} active`,
    `[pulse] Subscription usage: ${usage.input_tokens ?? 0} input · ${usage.cached_input_tokens ?? 0} cached · ${usage.output_tokens ?? 0} output · ${usage.reasoning_tokens ?? 0} reasoning`,
    memoryState,
  ].join('\n');
}

function formatBytes(value) {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  return `${size >= 10 || unit === 0 ? size.toFixed(0) : size.toFixed(1)} ${units[unit]}`;
}

export async function waitForHistoricalEgress({ jobID, request, sleep = defaultSleep, timeoutMs = 30 * 60_000, pollMs = 1_000 }) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const status = exactStatus(await request('GET', `/memory/historical-ingest/jobs/${jobID}`));
    if (status.state !== 'awaiting_egress_consent') return status;
    await sleep(pollMs);
  }
  fail('historical_egress_consent_timeout');
}

function defaultSleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function workerReceiptPath(dataDir, jobID) {
  return path.join(dataDir, 'historical-ingest', `worker-${jobID}.json`);
}

function writeWorkerReceipt(dataDir, jobID) {
  const file = workerReceiptPath(dataDir, jobID);
  mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 });
  writeFileSync(file, `${JSON.stringify({ schema: 'pulse.historical_ingest.worker_process.v1', job_id: jobID, pid: process.pid })}\n`, { flag: 'wx', mode: 0o600 });
  chmodSync(file, 0o600);
  return file;
}

function stopWorkerReceipt(dataDir, jobID, { kill = process.kill } = {}) {
  const file = workerReceiptPath(dataDir, jobID);
  if (!existsSync(file)) return false;
  const info = lstatSync(file);
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.size > 2048) fail('historical_worker_receipt_unsafe');
  let receipt;
  try { receipt = JSON.parse(readFileSync(file, 'utf8')); } catch { fail('historical_worker_receipt_invalid'); }
  if (receipt?.schema !== 'pulse.historical_ingest.worker_process.v1' || receipt.job_id !== jobID || !Number.isSafeInteger(receipt.pid) || receipt.pid <= 1) {
    fail('historical_worker_receipt_invalid');
  }
  try { kill(receipt.pid, 'SIGTERM'); } catch (error) {
    if (error?.code !== 'ESRCH') throw error;
  }
  return true;
}

async function runQualifiedWorker({ status, request, qualify, runWorker, dataDir, stdout }) {
  if (!RUNNABLE.has(status.state) || !status.egress_authorized) fail('historical_worker_not_authorized');
  stdout(`[pulse] Verifying ${CODEX_MEMORY_MODEL} · low through your Codex/ChatGPT subscription…`);
  const qualification = await qualify({ egressAuthorized: true });
  if (!qualification?.live_model_qualified || qualification.contract_digest !== status.runner_contract_digest) {
    fail('historical_luna_qualification_failed');
  }
  const receiptPath = writeWorkerReceipt(dataDir, status.job_id);
  const controller = new AbortController();
  const stop = () => controller.abort(new Error('historical_worker_interrupted'));
  process.once('SIGINT', stop);
  process.once('SIGTERM', stop);
  try {
    return await runWorker({
      jobID: status.job_id, qualification, signal: controller.signal,
      request: (method, route, body) => request(method, route, body),
    });
  } finally {
    process.removeListener('SIGINT', stop);
    process.removeListener('SIGTERM', stop);
    rmSync(receiptPath, { force: true });
  }
}

export async function runHistoricalIngestCommand({
  argv,
  request,
  openHome,
  dataDir,
  currentSessionID = process.env.CODEX_THREAD_ID,
  stdout = console.log,
  qualify = runSyntheticMemoryCanary,
  runWorker = runHistoricalIngestWorker,
  waitForEgress = waitForHistoricalEgress,
  stopWorker = stopWorkerReceipt,
} = {}) {
  if (!Array.isArray(argv) || typeof request !== 'function' || typeof openHome !== 'function' || !path.isAbsolute(dataDir ?? '')) {
    fail('historical_cli_contract_invalid');
  }
  if (argv.some((entry) => ['--base', '--store', '--team', '--token', '--egress-token', '--apply'].includes(entry))) {
    fail('historical_forbidden_option');
  }
  const action = argv[0];
  if (action === 'ingest') {
    if (argv[1] !== 'codex') fail('historical_source_unsupported');
    const roots = Number(option(argv, '--roots') ?? '50');
    if (!Number.isSafeInteger(roots) || roots < 1 || roots > 200) fail('historical_roots_invalid');
    if (currentSessionID !== undefined && !SESSION_ID.test(currentSessionID)) fail('historical_current_session_invalid');
    const status = exactStatus(await request('POST', '/memory/historical-ingest/jobs', {
      source: 'codex', root_limit: roots,
      ...(currentSessionID ? { excluded_session_id: currentSessionID } : {}),
    }));
    stdout(formatStatus(status));
    stdout(`[pulse] Nothing has left this Mac. Memory Home will show exactly what ${CODEX_MEMORY_MODEL} would receive.`);
    await openHome();
    const authorized = await waitForEgress({ jobID: status.job_id, request });
    if (TERMINAL.has(authorized.state)) return authorized;
    const result = await runQualifiedWorker({ status: authorized, request, qualify, runWorker, dataDir, stdout });
    stdout(`[pulse] Dry run ${result.state}; ${result.accepted_units} chunks accepted. Memory writes: 0.`);
    await openHome();
    return result;
  }

  let jobID = option(argv, '--job');
  let status = exactStatus(await request('GET', jobID ? `/memory/historical-ingest/jobs/${assertedJobID(jobID)}` : '/memory/historical-ingest/jobs/latest'));
  jobID = status.job_id;
  if (action === 'status') {
    stdout(formatStatus(status));
    return status;
  }
  if (action === 'explain') {
    stdout(formatStatus(status));
    stdout(`[pulse] Source files are frozen by prefix digest. Only normalized, path-free records may reach ${CODEX_MEMORY_MODEL}. The manifest remains a dry run until you finish review and approve the exact Backup & Import action in Memory Home; the CLI cannot apply it.`);
    return status;
  }
  if (action === 'usage') {
    stdout(`[pulse] ${jobID}: ${JSON.stringify(status.usage ?? {})} · route=codex_chatgpt_subscription · model=${CODEX_MEMORY_MODEL} · effort=low · api=false`);
    return status.usage ?? {};
  }
  if (action === 'home') {
    await openHome();
    return status;
  }
  if (action === 'cancel') {
    status = exactStatus(await request('POST', `/memory/historical-ingest/jobs/${jobID}/cancel`, {}));
    stopWorker(dataDir, jobID);
    stdout(`[pulse] History job ${jobID} canceled. Accepted checkpoints remain private; memory writes: 0.`);
    return status;
  }
  if (action === 'resume') {
    if (status.state === 'awaiting_egress_consent') {
      await openHome();
      status = await waitForEgress({ jobID, request });
    } else if (status.state === 'paused_quota' || status.state === 'extraction_failed') {
      status = exactStatus(await request('POST', `/memory/historical-ingest/jobs/${jobID}/resume`, {}));
    }
    if (TERMINAL.has(status.state)) {
      stdout(formatStatus(status));
      return status;
    }
    const result = await runQualifiedWorker({ status, request, qualify, runWorker, dataDir, stdout });
    stdout(`[pulse] Dry run ${result.state}; ${result.accepted_units} chunks accepted. Memory writes: 0.`);
    await openHome();
    return result;
  }
  fail('historical_command_unsupported');
}
