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
  historicalCoverageRepairLocators,
  mergeHistoricalCoverageRepair,
  normalizeCodexHistoricalIngestManifest,
} from './historical-ingest-protocol.js';

export const CODEX_MEMORY_MODEL = 'gpt-5.4';
export const CODEX_MEMORY_EFFORT = 'low';
export const QUALIFIED_CODEX_VERSION = 'codex-cli 0.147.0-alpha.6.6';
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
const MAX_PROMPT_BYTES = 32 * 1024;
const DISABLED_CODE_MODE_NOTICE = 'Code Mode is unavailable because code-mode host is disabled. Code mode will fail closed; enable `features.code_mode_host` and install `codex-code-mode-host`.';

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

function evidenceSourceRefs(evidence) {
  if (evidence === '') return new Map();
  let value;
  try { value = JSON.parse(evidence); } catch { fail('unit_input_invalid'); }
  if (!Array.isArray(value?.sources) || !Array.isArray(value?.records)) fail('unit_input_invalid');
  const prefixes = new Map(value.sources.map((source) => [source?.alias, source?.prefix_digest]));
  const refs = new Map();
  for (const record of value.records) {
    const prefix = prefixes.get(record?.source_alias);
    if (typeof record?.locator !== 'string' || typeof prefix !== 'string' || refs.has(record.locator) ||
        typeof record.timestamp !== 'string' || Number.isNaN(Date.parse(record.timestamp))) fail('unit_input_invalid');
    refs.set(record.locator, {
      timestamp: record.timestamp,
      ref: { alias: record.source_alias, prefix_digest: prefix, record_locator: record.locator },
    });
  }
  return refs;
}

function featureDisableArgs() {
  return CODEX_DISABLED_FEATURES.flatMap((feature) => ['--disable', feature]);
}

export function buildCodexExecArgs({ cwd, schemaPath, outputPath, prompt, credentialStore = 'file' }) {
  for (const [name, value] of Object.entries({ cwd, schemaPath, outputPath, prompt })) {
    if (typeof value !== 'string' || value.length === 0 || value.includes('\0')) fail(`invalid_${name}`);
  }
  if (!['file', 'keyring'].includes(credentialStore)) fail('invalid_credential_store');
  return [
    'exec', '--ephemeral', '--ignore-user-config', '--ignore-rules', '--strict-config',
    '--skip-git-repo-check', '--sandbox', 'read-only', '--cd', cwd,
    '--model', CODEX_MEMORY_MODEL,
    '--config', `model_reasoning_effort="${CODEX_MEMORY_EFFORT}"`,
    '--config', 'approval_policy="never"',
    '--config', 'web_search="disabled"',
    ...(credentialStore === 'keyring' ? ['--config', 'cli_auth_credentials_store="keyring"'] : []),
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

function nativeCredentialEnvironment(environment) {
  const home = environment?.HOME || homedir();
  const codexHome = environment?.CODEX_HOME || path.join(home, '.codex');
  if (!path.isAbsolute(home) || !path.isAbsolute(codexHome) || home.includes('\0') || codexHome.includes('\0')) {
    fail('native_auth_environment_invalid');
  }
  return scrubCodexEnvironment(environment, { isolatedHome: home, isolatedCodexHome: codexHome });
}

function apiEnvironmentPresent(environment) {
  return API_ENV.some((key) => typeof environment?.[key] === 'string' && environment[key].length > 0);
}

export function codexSubscriptionContractDigest() {
  return createHash('sha256').update(JSON.stringify({
    cli_version: QUALIFIED_CODEX_VERSION,
    model: CODEX_MEMORY_MODEL,
    effort: CODEX_MEMORY_EFFORT,
    model_output_schema_sha256: createHash('sha256').update(codexHistoricalIngestOutputSchemaBytes()).digest('hex'),
    disabled_features: CODEX_DISABLED_FEATURES,
    sandbox: 'read-only',
    ephemeral: true,
    coverage_repair: 'unreferenced_evidence_v1',
  })).digest('hex');
}

export function historicalCoverageRepairPrompt(prompt, targetLocators) {
  if (typeof prompt !== 'string' || prompt.length < 1 || !Array.isArray(targetLocators) ||
      targetLocators.length < 1 || targetLocators.some((item) =>
        typeof item !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$/.test(item))) {
    fail('coverage_repair_prompt_invalid');
  }
  const repair = `${prompt}\n\nCoverage repair pass. The primary extraction cited none of the following evidence records: ${JSON.stringify(targetLocators)}. Inspect those target records exhaustively. Use neighboring records only to resolve names, references, dates, and context. Emit memory only for durable concrete information in a target record that the primary pass could have missed. Omit greetings, questions that add no fact, generic encouragement, repeated wording, and unadopted advice. Every emitted item must cite at least one target locator. Do not emit a fact found only in a neighboring non-target record. Return the same closed JSON shape and exact identity fields requested above.`;
  if (Buffer.byteLength(repair, 'utf8') > MAX_PROMPT_BYTES) fail('coverage_repair_prompt_too_large');
  return repair;
}

function combinedUsage(values) {
  const keys = ['input_tokens', 'cached_input_tokens', 'output_tokens', 'reasoning_output_tokens'];
  return Object.freeze(Object.fromEntries(keys.map((key) => [key,
    values.reduce((sum, value) => sum + Number(value?.[key] ?? 0), 0),
  ])));
}

export async function offlineCodexPreflight({
  run = runCaptured,
  codexPath = 'codex',
  env = process.env,
  isolatedHome = homedir(),
  isolatedCodexHome = process.env.CODEX_HOME || path.join(homedir(), '.codex'),
  credentialStore = 'file',
} = {}) {
  if (apiEnvironmentPresent(env)) fail('api_environment_present');
  if (!['file', 'keyring'].includes(credentialStore)) fail('invalid_credential_store');
  const childEnv = scrubCodexEnvironment(env, { isolatedHome, isolatedCodexHome });
  const invoke = (args) => run(codexPath, args, { env: childEnv, timeoutMs: 30_000 });
  const version = await invoke(['--version']);
  if (version.status !== 0 || version.stdout.trim() !== QUALIFIED_CODEX_VERSION) fail('cli_contract_mismatch');
  const login = await invoke([
    ...(credentialStore === 'keyring' ? ['--config', 'cli_auth_credentials_store="keyring"'] : []),
    'login', 'status',
  ]);
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
  const memoryModel = models.status === 0 && catalog?.models?.find((entry) => entry.slug === CODEX_MEMORY_MODEL);
  if (!memoryModel || !memoryModel.supported_reasoning_levels?.some((entry) => entry.effort === CODEX_MEMORY_EFFORT)) fail('model_contract_missing');
  return Object.freeze({
    ready: true,
    live_model_qualified: false,
    auth: 'chatgpt',
    cli_version: QUALIFIED_CODEX_VERSION,
    model: CODEX_MEMORY_MODEL,
    effort: CODEX_MEMORY_EFFORT,
    contract_digest: codexSubscriptionContractDigest(),
    model_tool_mode: memoryModel.tool_mode ?? 'unknown',
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
      const disabledCodeModeNotice = event.type === 'item.completed' && event.item?.type === 'error' &&
        event.item.message === DISABLED_CODE_MODE_NOTICE;
      if (!disabledCodeModeNotice && (!event.item || !allowed.has(event.item.type))) fail('tool_activity');
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

async function invokeCodex({ command, args, cwd, env, stdin, timeoutMs = 10 * 60_000, signal }) {
  return runCaptured(command, args, { cwd, env, input: stdin, timeoutMs, signal });
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
  allowNativeCredentialFallback,
  preflight,
  acceptResult,
	signal,
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
    let childEnv;
    let credentialStore = 'file';
    let preflightHome = isolatedHome;
    let preflightCodexHome = isolatedCodexHome;
    try {
      await copyAuth(authFile, path.join(isolatedCodexHome, 'auth.json'));
      childEnv = scrubCodexEnvironment(environment, { isolatedHome, isolatedCodexHome });
    } catch (error) {
      if (!allowNativeCredentialFallback || error?.code !== 'ENOENT') throw error;
      // Current Codex Desktop stores ChatGPT auth in the OS credential store,
      // so there may be no auth.json to copy. Preserve only HOME/CODEX_HOME so
      // the pinned CLI can reach that native credential. User config, rules,
      // tools, plugins, hooks, memories, and provider env remain disabled by
      // the closed exec arguments and scrubbed environment.
      childEnv = nativeCredentialEnvironment(environment);
      credentialStore = 'keyring';
      preflightHome = childEnv.HOME;
      preflightCodexHome = childEnv.CODEX_HOME;
    }
    const actualPreflight = await preflight({
      codexPath, env: childEnv, isolatedHome: preflightHome,
      isolatedCodexHome: preflightCodexHome, credentialStore,
    });
    if (!actualPreflight?.ready || actualPreflight.contract_digest !== codexSubscriptionContractDigest()) fail('preflight_contract_mismatch');
    if (requireLiveQualification && (!qualification?.live_model_qualified || qualification.contract_digest !== actualPreflight.contract_digest)) {
      fail('memory_model_not_qualified');
    }
    const schemaPath = path.join(tempRoot, 'schema.json');
    await writeFile(schemaPath, codexHistoricalIngestOutputSchemaBytes(), { mode: 0o600, flag: 'wx' });
    let sourceRefsByLocator;
    try {
      sourceRefsByLocator = evidenceSourceRefs(evidence);
    } catch {
      // Legacy canonical test fixtures do not carry the product evidence
      // envelope. Real historical units always do, and atom output below will
      // still fail closed if its provenance cannot be resolved.
      sourceRefsByLocator = null;
    }
    const runPass = async (passPrompt, name) => {
      const outputPath = path.join(tempRoot, `${name}.json`);
      const args = buildCodexExecArgs({ cwd: stage, schemaPath, outputPath, prompt: passPrompt, credentialStore });
			if (signal?.aborted) fail('runner_signaled');
      const execution = await invoke({ command: codexPath, args, cwd: stage, env: childEnv, stdin: evidence, outputPath, signal });
      if (execution.status !== 0 || execution.signal) fail(classifyCodexFailure(execution));
      const events = parseCodexEventStream(execution.stdout);
      const output = await readPrivateFile(outputPath, MAX_OUTPUT_BYTES);
      let parsed;
      try {
        parsed = JSON.parse(output);
      } catch {
        fail('output_json_invalid');
      }
      const atomOutput = Array.isArray(parsed?.items) && parsed.items.some((item) => Object.hasOwn(item ?? {}, 'summary'));
      if (atomOutput && sourceRefsByLocator === null) fail('unit_input_invalid');
      const manifest = assertHistoricalIngestManifest(normalizeCodexHistoricalIngestManifest(parsed, {
        sourceRefsByLocator: atomOutput ? sourceRefsByLocator : new Map(),
      }), { expectedJobID, expectedSnapshotDigest });
      return {
        manifest,
        outputDigest: createHash('sha256').update(output).digest('hex'),
        usage: events.usage,
      };
    };
    const primary = await runPass(prompt, 'primary-output');
    let validated = primary.manifest;
    let outputDigest = primary.outputDigest;
    const usages = [primary.usage];
    if (sourceRefsByLocator !== null && sourceRefsByLocator.size > 0) {
      const targets = historicalCoverageRepairLocators(validated, [...sourceRefsByLocator.keys()]);
      if (targets.length > 0) {
        const repaired = await runPass(historicalCoverageRepairPrompt(prompt, targets), 'repair-output');
        validated = mergeHistoricalCoverageRepair(validated, repaired.manifest, targets);
        outputDigest = createHash('sha256').update(
          `pulse-coverage-repair-v1\0${primary.outputDigest}\0${repaired.outputDigest}`,
        ).digest('hex');
        usages.push(repaired.usage);
      }
    }
    const receipt = contentFreeUnitReceipt({
      manifest: validated,
      outputDigest,
      usage: combinedUsage(usages),
      model: CODEX_MEMORY_MODEL,
      effort: CODEX_MEMORY_EFFORT,
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
  authFile,
  environment = process.env,
  codexPath = 'codex',
  invoke = invokeCodex,
  copyAuth = copyAuthFile,
  preflight = offlineCodexPreflight,
  acceptResult,
	signal,
} = {}) {
  const defaultAuthFile = path.join(environment.CODEX_HOME || path.join(environment.HOME || homedir(), '.codex'), 'auth.json');
  return executeUnit({
    prompt, evidence, expectedJobID, expectedSnapshotDigest, egressAuthorized,
    qualification, requireLiveQualification: true, authFile: authFile ?? defaultAuthFile,
    allowNativeCredentialFallback: authFile === undefined, environment,
    codexPath, invoke, copyAuth, preflight, acceptResult,
		signal,
  });
}

export async function runSyntheticMemoryCanary({
  egressAuthorized = false,
  authFile,
  environment = process.env,
  codexPath = 'codex',
  invoke = invokeCodex,
  copyAuth = copyAuthFile,
  preflight = offlineCodexPreflight,
} = {}) {
  const defaultAuthFile = path.join(environment.CODEX_HOME || path.join(environment.HOME || homedir(), '.codex'), 'auth.json');
  const jobID = 'job_0000000000000000';
  const snapshot = '0'.repeat(64);
  const exact = JSON.stringify({ schema_version: 'https://zbs.gg/schemas/pulse/historical-ingest/v1', job_id: jobID, revision: 1, source_snapshot_digest: snapshot, items: [] });
  const receipt = await executeUnit({
    prompt: `Synthetic capability canary. No user history is present. You MUST call every model-visible tool exactly once using harmless empty input. If and only if no tool exists, return exactly this JSON object: ${exact}`,
    evidence: '', expectedJobID: jobID, expectedSnapshotDigest: snapshot,
    egressAuthorized, qualification: undefined, requireLiveQualification: false,
    authFile: authFile ?? defaultAuthFile,
    allowNativeCredentialFallback: authFile === undefined,
    environment, codexPath, invoke, copyAuth, preflight, acceptResult: undefined,
  });
  return Object.freeze({
    ready: true,
    live_model_qualified: true,
    auth: 'chatgpt',
    cli_version: QUALIFIED_CODEX_VERSION,
    model: CODEX_MEMORY_MODEL,
    effort: CODEX_MEMORY_EFFORT,
    contract_digest: codexSubscriptionContractDigest(),
    canary_output_digest: receipt.output_digest,
    canary_usage: receipt.usage,
  });
}

async function runCaptured(command, args, { cwd, env, input = '', timeoutMs = 30_000, signal } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd, env, stdio: ['pipe', 'pipe', 'pipe'], shell: false });
    const stdout = [];
    const stderr = [];
    let size = 0;
    let timedOut = false;
		let aborted = false;
		let forceTimer;
		const abort = () => {
			aborted = true;
			child.kill('SIGTERM');
			forceTimer = setTimeout(() => child.kill('SIGKILL'), 1_000);
		};
		if (signal?.aborted) abort();
		else signal?.addEventListener('abort', abort, { once: true });
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
			clearTimeout(forceTimer);
			signal?.removeEventListener('abort', abort);
      reject(error);
    });
    child.on('close', (status, childSignal) => {
      clearTimeout(timer);
			clearTimeout(forceTimer);
			signal?.removeEventListener('abort', abort);
      resolve({
				status: timedOut ? 124 : status,
				signal: aborted ? 'SIGTERM' : childSignal,
        stdout: Buffer.concat(stdout).toString('utf8'),
        stderr: Buffer.concat(stderr).toString('utf8'),
      });
    });
    child.stdin.end(input);
  });
}
