import { createHash } from 'node:crypto';
import { spawn } from 'node:child_process';
import { constants as fsConstants } from 'node:fs';
import { chmod, lstat, mkdir, mkdtemp, open, rm, writeFile } from 'node:fs/promises';
import { homedir, tmpdir } from 'node:os';
import path from 'node:path';

import {
  assertHistoricalIngestManifest,
  codexHistoricalIngestOutputSchemaBytes,
  contentFreeUnitReceipt,
  normalizeCodexHistoricalIngestManifest,
} from './historical-ingest-protocol.js';

export const CODEX_LUNA_MODEL = 'gpt-5.6-luna';
export const CODEX_LUNA_EFFORT = 'low';
export const QUALIFIED_CODEX_VERSION = 'codex-cli 0.144.6';
export const CODEX_DISABLED_FEATURES = Object.freeze([
  'shell_tool', 'unified_exec', 'code_mode_host', 'apps', 'plugins',
  'browser_use', 'browser_use_external', 'browser_use_full_cdp_access',
  'computer_use', 'in_app_browser', 'image_generation', 'multi_agent',
  'goals', 'workspace_dependencies', 'hooks', 'tool_suggest',
  'auth_elicitation', 'tool_call_mcp_elicitation', 'skill_mcp_dependency_install',
  'chronicle', 'memories',
]);

const API_ENV = Object.freeze([
  'OPENAI_API_KEY', 'OPENAI_BASE_URL', 'OPENAI_API_BASE', 'OPENAI_ORG_ID',
  'AZURE_OPENAI_API_KEY', 'AZURE_OPENAI_ENDPOINT', 'CODEX_API_KEY', 'CODEX_PROVIDER',
  'ANTHROPIC_API_KEY', 'COHERE_API_KEY', 'PULSE_API_KEY',
]);
const PROVIDER_ENV = /^(?:OPENAI|AZURE_OPENAI|ANTHROPIC|COHERE|CODEX_(?:API|PROVIDER)|PULSE_API_KEY)/;
const QUOTA = /(?:usage limit|rate limit|too many requests|quota (?:exceeded|exhausted)|credits? exhausted)/i;
const AUTH = /(?:401|not logged in|authentication|unauthorized|login required)/i;
const MODEL = /(?:model[^\n]{0,80}(?:unavailable|unsupported|not found|does not exist)|unknown model)/i;
const MAX_EVENT_BYTES = 8 * 1024 * 1024;
const MAX_OUTPUT_BYTES = 16 * 1024 * 1024;

export class CodexSubscriptionRunnerError extends Error {
  constructor(code) {
    super(code);
    this.name = 'CodexSubscriptionRunnerError';
    this.code = code;
  }
}

function fail(code) {
  throw new CodexSubscriptionRunnerError(code);
}

function featureDisableArgs() {
  return CODEX_DISABLED_FEATURES.flatMap((feature) => ['--disable', feature]);
}

export function buildCodexExecArgs({ cwd, schemaPath, outputPath, prompt }) {
  for (const [name, value] of Object.entries({ cwd, schemaPath, outputPath, prompt })) {
    if (typeof value !== 'string' || value.length === 0 || value.includes('\0')) fail(`invalid_${name}`);
  }
  return [
    'exec', '--ephemeral', '--ignore-user-config', '--ignore-rules', '--strict-config',
    '--skip-git-repo-check', '--sandbox', 'read-only', '--cd', cwd,
    '--model', CODEX_LUNA_MODEL,
    '--config', `model_reasoning_effort="${CODEX_LUNA_EFFORT}"`,
    '--config', 'approval_policy="never"',
    ...featureDisableArgs(),
    '--output-schema', schemaPath, '--output-last-message', outputPath,
    '--json', prompt,
  ];
}

export function scrubCodexEnvironment(environment, { isolatedHome, isolatedCodexHome }) {
  const clean = {};
  for (const [key, value] of Object.entries(environment ?? {})) {
    if (!PROVIDER_ENV.test(key) && key !== 'HOME' && key !== 'CODEX_HOME') clean[key] = value;
  }
  clean.HOME = isolatedHome;
  clean.CODEX_HOME = isolatedCodexHome;
  clean.NO_COLOR = '1';
  clean.CODEX_NON_INTERACTIVE = '1';
  return clean;
}

function apiEnvironmentPresent(environment) {
  return API_ENV.some((key) => typeof environment?.[key] === 'string' && environment[key].length > 0);
}

export function codexSubscriptionContractDigest() {
  return createHash('sha256').update(JSON.stringify({
    cli_version: QUALIFIED_CODEX_VERSION,
    model: CODEX_LUNA_MODEL,
    effort: CODEX_LUNA_EFFORT,
    disabled_features: CODEX_DISABLED_FEATURES,
    sandbox: 'read-only',
    ephemeral: true,
  })).digest('hex');
}

export async function offlineCodexPreflight({
  run = runCaptured,
  codexPath = 'codex',
  env = process.env,
  isolatedHome = homedir(),
  isolatedCodexHome = process.env.CODEX_HOME || path.join(homedir(), '.codex'),
} = {}) {
  if (apiEnvironmentPresent(env)) fail('api_environment_present');
  const childEnv = scrubCodexEnvironment(env, { isolatedHome, isolatedCodexHome });
  const invoke = (args) => run(codexPath, args, { env: childEnv, timeoutMs: 30_000 });
  const version = await invoke(['--version']);
  if (version.status !== 0 || version.stdout.trim() !== QUALIFIED_CODEX_VERSION) fail('cli_contract_mismatch');
  const login = await invoke(['login', 'status']);
  if (login.status !== 0 || `${login.stdout}${login.stderr}`.trim() !== 'Logged in using ChatGPT') fail('chatgpt_auth_required');
  const features = await invoke([...featureDisableArgs(), 'features', 'list']);
  if (features.status !== 0) fail('feature_probe_failed');
  const effective = new Map(features.stdout.split(/\r?\n/).filter(Boolean).map((line) => {
    const fields = line.trim().split(/\s+/);
    return [fields[0], fields.at(-1)];
  }));
  for (const feature of CODEX_DISABLED_FEATURES) {
    if (effective.get(feature) !== 'false') fail('nonempty_tool_surface');
  }
  const models = await invoke(['debug', 'models', '--bundled']);
  let catalog;
  try {
    catalog = JSON.parse(models.stdout);
  } catch {
    fail('model_catalog_invalid');
  }
  const luna = models.status === 0 && catalog?.models?.find((entry) => entry.slug === CODEX_LUNA_MODEL);
  if (!luna || !luna.supported_reasoning_levels?.some((entry) => entry.effort === CODEX_LUNA_EFFORT)) fail('model_contract_missing');
  return Object.freeze({
    ready: true,
    live_model_qualified: false,
    auth: 'chatgpt',
    cli_version: QUALIFIED_CODEX_VERSION,
    model: CODEX_LUNA_MODEL,
    effort: CODEX_LUNA_EFFORT,
    contract_digest: codexSubscriptionContractDigest(),
    model_tool_mode: luna.tool_mode ?? 'unknown',
  });
}

export function parseCodexEventStream(stdout) {
  if (typeof stdout !== 'string' || Buffer.byteLength(stdout, 'utf8') > MAX_EVENT_BYTES) fail('event_stream_invalid');
  const lines = stdout.split(/\r?\n/).filter(Boolean);
  let terminals = 0;
  let threads = 0;
  let usage;
  for (const line of lines) {
    let event;
    try {
      event = JSON.parse(line);
    } catch {
      fail('event_stream_invalid');
    }
    if (!event || typeof event.type !== 'string') fail('event_stream_invalid');
    if (event.type === 'thread.started') threads += 1;
    if (event.type === 'turn.failed' || event.type === 'error') fail('turn_failed');
    if (event.type.startsWith('item.')) {
      const allowed = new Set(['agent_message', 'reasoning', 'plan', 'todo_list']);
      if (!event.item || !allowed.has(event.item.type)) fail('tool_activity');
    }
    if (event.type === 'turn.completed') {
      terminals += 1;
      usage = event.usage;
    }
  }
  if (threads !== 1) fail('thread_event_count');
  if (terminals !== 1) fail('terminal_event_count');
  const keys = ['input_tokens', 'cached_input_tokens', 'output_tokens', 'reasoning_output_tokens'];
  if (!usage || keys.some((key) => !Number.isSafeInteger(usage[key]) || usage[key] < 0)) fail('usage_invalid');
  return Object.freeze({ usage: Object.freeze(Object.fromEntries(keys.map((key) => [key, usage[key]]))) });
}

export function classifyCodexFailure({ status, signal, stderr = '' }) {
  if (status === 0 && !signal) return 'none';
  if (QUOTA.test(stderr)) return 'paused_quota';
  if (AUTH.test(stderr)) return 'auth_failed';
  if (MODEL.test(stderr)) return 'model_unavailable';
  if (signal) return 'runner_signaled';
  return 'runner_failed';
}

async function copyAuthFile(source, destination) {
  const bytes = await readPrivateFile(source, 64 * 1024);
  await writeFile(destination, bytes, { mode: 0o600, flag: 'wx' });
  const after = await lstat(destination);
  if (!after.isFile() || after.isSymbolicLink() || after.nlink !== 1) fail('auth_copy_unsafe');
}

async function readPrivateFile(file, maximum) {
  const handle = await open(file, fsConstants.O_RDONLY | (fsConstants.O_NOFOLLOW ?? 0));
  try {
    const info = await handle.stat();
    if (!info.isFile() || info.nlink !== 1 || info.size < 2 || info.size > maximum) fail('output_file_unsafe');
    return await handle.readFile();
  } finally {
    await handle.close();
  }
}

async function invokeCodex({ command, args, cwd, env, stdin, timeoutMs = 10 * 60_000 }) {
  return runCaptured(command, args, { cwd, env, input: stdin, timeoutMs });
}

async function executeUnit({
  prompt,
  evidence,
  expectedJobID,
  expectedSnapshotDigest,
  egressAuthorized,
  qualification,
  requireLiveQualification,
  authFile,
  environment,
  codexPath,
  invoke,
  copyAuth,
  preflight,
  acceptResult,
}) {
  if (!egressAuthorized) fail('egress_not_authorized');
  if (typeof evidence !== 'string' || typeof prompt !== 'string' || prompt.length < 1) fail('unit_input_invalid');
  if (apiEnvironmentPresent(environment)) fail('api_environment_present');
  const tempRoot = await mkdtemp(path.join(tmpdir(), 'pulse-history-runner-'));
  await chmod(tempRoot, 0o700);
  try {
    const isolatedHome = path.join(tempRoot, 'home');
    const isolatedCodexHome = path.join(tempRoot, 'codex');
    const stage = path.join(tempRoot, 'stage');
    await mkdir(isolatedHome, { mode: 0o700 });
    await mkdir(isolatedCodexHome, { mode: 0o700 });
    await mkdir(stage, { mode: 0o700 });
    await copyAuth(authFile, path.join(isolatedCodexHome, 'auth.json'));
    const childEnv = scrubCodexEnvironment(environment, { isolatedHome, isolatedCodexHome });
    const actualPreflight = await preflight({
      codexPath, env: childEnv, isolatedHome, isolatedCodexHome,
    });
    if (!actualPreflight?.ready || actualPreflight.contract_digest !== codexSubscriptionContractDigest()) fail('preflight_contract_mismatch');
    if (requireLiveQualification && (!qualification?.live_model_qualified || qualification.contract_digest !== actualPreflight.contract_digest)) {
      fail('luna_not_qualified');
    }
    const schemaPath = path.join(tempRoot, 'schema.json');
    const outputPath = path.join(tempRoot, 'output.json');
    await writeFile(schemaPath, codexHistoricalIngestOutputSchemaBytes(), { mode: 0o600, flag: 'wx' });
    const args = buildCodexExecArgs({ cwd: stage, schemaPath, outputPath, prompt });
    const execution = await invoke({ command: codexPath, args, cwd: stage, env: childEnv, stdin: evidence, outputPath });
    if (execution.status !== 0 || execution.signal) fail(classifyCodexFailure(execution));
    const events = parseCodexEventStream(execution.stdout);
    const output = await readPrivateFile(outputPath, MAX_OUTPUT_BYTES);
    let parsed;
    try {
      parsed = JSON.parse(output);
    } catch {
      fail('output_json_invalid');
    }
    const validated = assertHistoricalIngestManifest(normalizeCodexHistoricalIngestManifest(parsed), { expectedJobID, expectedSnapshotDigest });
    const receipt = contentFreeUnitReceipt({
      manifest: validated,
      outputDigest: createHash('sha256').update(output).digest('hex'),
      usage: events.usage,
      model: CODEX_LUNA_MODEL,
      effort: CODEX_LUNA_EFFORT,
      cliVersion: QUALIFIED_CODEX_VERSION,
    });
    if (acceptResult !== undefined) {
      if (typeof acceptResult !== 'function') fail('result_acceptor_invalid');
      await acceptResult(validated, receipt);
    }
    return receipt;
  } finally {
    await rm(tempRoot, { recursive: true, force: true });
  }
}

export async function runHistoricalIngestUnit({
  prompt,
  evidence,
  expectedJobID,
  expectedSnapshotDigest,
  egressAuthorized = false,
  qualification,
  authFile = path.join(process.env.CODEX_HOME || path.join(homedir(), '.codex'), 'auth.json'),
  environment = process.env,
  codexPath = 'codex',
  invoke = invokeCodex,
  copyAuth = copyAuthFile,
  preflight = offlineCodexPreflight,
  acceptResult,
} = {}) {
  return executeUnit({
    prompt, evidence, expectedJobID, expectedSnapshotDigest, egressAuthorized,
    qualification, requireLiveQualification: true, authFile, environment,
    codexPath, invoke, copyAuth, preflight, acceptResult,
  });
}

export async function runSyntheticLunaCanary({
  egressAuthorized = false,
  authFile = path.join(process.env.CODEX_HOME || path.join(homedir(), '.codex'), 'auth.json'),
  environment = process.env,
  codexPath = 'codex',
  invoke = invokeCodex,
  copyAuth = copyAuthFile,
  preflight = offlineCodexPreflight,
} = {}) {
  const jobID = 'job_0000000000000000';
  const snapshot = '0'.repeat(64);
  const exact = JSON.stringify({ schema_version: 'https://zbs.gg/schemas/pulse/historical-ingest/v1', job_id: jobID, revision: 1, source_snapshot_digest: snapshot, items: [] });
  const receipt = await executeUnit({
    prompt: `Synthetic capability canary. No user history is present. You MUST call every model-visible tool exactly once using harmless empty input. If and only if no tool exists, return exactly this JSON object: ${exact}`,
    evidence: '', expectedJobID: jobID, expectedSnapshotDigest: snapshot,
    egressAuthorized, qualification: undefined, requireLiveQualification: false,
    authFile, environment, codexPath, invoke, copyAuth, preflight, acceptResult: undefined,
  });
  return Object.freeze({
    ready: true,
    live_model_qualified: true,
    auth: 'chatgpt',
    cli_version: QUALIFIED_CODEX_VERSION,
    model: CODEX_LUNA_MODEL,
    effort: CODEX_LUNA_EFFORT,
    contract_digest: codexSubscriptionContractDigest(),
    canary_output_digest: receipt.output_digest,
    canary_usage: receipt.usage,
  });
}

async function runCaptured(command, args, { cwd, env, input = '', timeoutMs = 30_000 } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd, env, stdio: ['pipe', 'pipe', 'pipe'], shell: false });
    const stdout = [];
    const stderr = [];
    let size = 0;
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      child.kill('SIGKILL');
    }, timeoutMs);
    const collect = (target) => (chunk) => {
      size += chunk.length;
      if (size > MAX_EVENT_BYTES) child.kill('SIGKILL');
      else target.push(chunk);
    };
    child.stdout.on('data', collect(stdout));
    child.stderr.on('data', collect(stderr));
    child.on('error', (error) => {
      clearTimeout(timer);
      reject(error);
    });
    child.on('close', (status, signal) => {
      clearTimeout(timer);
      resolve({
        status: timedOut ? 124 : status,
        signal,
        stdout: Buffer.concat(stdout).toString('utf8'),
        stderr: Buffer.concat(stderr).toString('utf8'),
      });
    });
    child.stdin.end(input);
  });
}
