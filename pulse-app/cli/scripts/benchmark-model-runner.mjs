#!/usr/bin/env node

import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  chmodSync,
  closeSync,
  copyFileSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  openSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { dirname, isAbsolute, join } from 'node:path';
import { pathToFileURL } from 'node:url';

const MAX_PROMPT_BYTES = 2 * 1024 * 1024;
const MAX_OUTPUT_BYTES = 2 * 1024 * 1024;
const DISABLED_FEATURES = Object.freeze([
  'shell_tool', 'unified_exec', 'apps', 'plugins',
  'browser_use', 'browser_use_external', 'browser_use_full_cdp_access',
  'computer_use', 'in_app_browser', 'image_generation', 'multi_agent',
  'goals', 'workspace_dependencies', 'hooks', 'tool_suggest',
  'auth_elicitation', 'tool_call_mcp_elicitation', 'skill_mcp_dependency_install',
  'chronicle', 'memories',
]);
const PROVIDER_ENV = /^(?:OPENAI|AZURE_OPENAI|ANTHROPIC|COHERE|CODEX_(?:API|PROVIDER)|PULSE_API_KEY)/;
const DISABLED_CODE_MODE_NOTICE = 'Code Mode is unavailable because code-mode host is disabled. Code mode will fail closed; enable `features.code_mode_host` and install `codex-code-mode-host`.';
const RESPONSE_RETRY_NOTICE = /^Reconnecting\.\.\. [1-5]\/5 \(/;
const HTTPS_FALLBACK_NOTICE = /^Falling back from WebSockets to HTTPS transport\./;

function fail(code, detail = '') {
  throw new Error(detail ? `${code}:${detail}` : code);
}

function privateFile(path, maximum) {
  const info = lstatSync(path);
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.size < 1 || info.size > maximum) {
    fail('benchmark_model_file_unsafe');
  }
  return readFileSync(path);
}

function sha256(value) {
  return createHash('sha256').update(value).digest('hex');
}

function cleanEnvironment(home, codexHome) {
  const clean = {};
  for (const [key, value] of Object.entries(process.env)) {
    if (!PROVIDER_ENV.test(key) && key !== 'HOME' && key !== 'CODEX_HOME') clean[key] = value;
  }
  return { ...clean, HOME: home, CODEX_HOME: codexHome, NO_COLOR: '1', CODEX_NON_INTERACTIVE: '1' };
}

function featureArgs() {
  return DISABLED_FEATURES.flatMap((feature) => ['--disable', feature]);
}

function run(command, args, { cwd, env, input = '', timeoutMs = 10 * 60_000 } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd, env, stdio: ['pipe', 'pipe', 'pipe'], shell: false });
    const stdout = [];
    const stderr = [];
    let bytes = 0;
    const collect = (target) => (chunk) => {
      bytes += chunk.length;
      if (bytes > MAX_OUTPUT_BYTES) child.kill('SIGKILL');
      else target.push(chunk);
    };
    child.stdout.on('data', collect(stdout));
    child.stderr.on('data', collect(stderr));
    const timer = setTimeout(() => child.kill('SIGKILL'), timeoutMs);
    child.once('error', reject);
    child.once('close', (status, signal) => {
      clearTimeout(timer);
      resolve({
        status, signal,
        stdout: Buffer.concat(stdout).toString('utf8'),
        stderr: Buffer.concat(stderr).toString('utf8'),
      });
    });
    child.stdin.end(input);
  });
}

function parseArgs(argv) {
  const options = { model: 'gpt-5.4', effort: 'low', timeout_ms: 10 * 60_000, format: 'text' };
  for (let index = 0; index < argv.length; index += 1) {
    const name = argv[index];
    const value = argv[index + 1];
    if (!name.startsWith('--') || !value || value.startsWith('--')) fail('benchmark_model_option_invalid', name);
    options[name.slice(2).replaceAll('-', '_')] = value;
    index += 1;
  }
  for (const name of ['prompt', 'output']) {
    if (!isAbsolute(options[name] ?? '')) fail('benchmark_model_path_invalid', name);
  }
  if (!new Set(['text', 'json']).has(options.format) ||
      (options.format === 'json' && !isAbsolute(options.schema ?? ''))) fail('benchmark_model_format_invalid');
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]{1,80}$/.test(options.model) ||
      !new Set(['low', 'medium', 'high', 'xhigh']).has(options.effort)) fail('benchmark_model_invalid');
  options.timeout_ms = Number(options.timeout_ms);
  if (!Number.isSafeInteger(options.timeout_ms) || options.timeout_ms < 10_000 || options.timeout_ms > 30 * 60_000) {
    fail('benchmark_model_timeout_invalid');
  }
  return options;
}

function parseEventStream(stdout) {
  const events = stdout.split(/\r?\n/).filter(Boolean).map((line) => {
    try { return JSON.parse(line); } catch { fail('benchmark_model_event_invalid'); }
  });
  const completed = events.filter((event) => event?.type === 'turn.completed');
  const benignTransportError = (event) =>
    event?.type === 'error' && RESPONSE_RETRY_NOTICE.test(String(event?.message ?? ''));
  const benignItemError = (event) => event?.type === 'item.completed' && event?.item?.type === 'error' &&
    (event.item.message === DISABLED_CODE_MODE_NOTICE ||
      HTTPS_FALLBACK_NOTICE.test(String(event.item.message ?? '')));
  const unsafeItem = events.some((event) => event?.type?.startsWith('item.') &&
    !benignItemError(event) &&
    !new Set(['agent_message', 'reasoning', 'plan', 'todo_list']).has(event?.item?.type));
  if (events.filter((event) => event?.type === 'thread.started').length !== 1 || completed.length !== 1 ||
      events.some((event) => event?.type === 'turn.failed' ||
        (event?.type === 'error' && !benignTransportError(event))) ||
      unsafeItem) {
    const shape = events.map((event) => ({ type: event?.type, item_type: event?.item?.type }));
    fail('benchmark_model_event_invalid', JSON.stringify(shape).slice(0, 4000));
  }
  const usage = completed[0].usage;
  const fields = ['input_tokens', 'cached_input_tokens', 'output_tokens', 'reasoning_output_tokens'];
  if (!usage || fields.some((field) => !Number.isSafeInteger(usage[field]) || usage[field] < 0)) {
    fail('benchmark_model_usage_invalid');
  }
  return Object.fromEntries(fields.map((field) => [field, usage[field]]));
}

export async function runBenchmarkModel({
  prompt,
  input = '',
  schema,
  model = 'gpt-5.4',
  effort = 'low',
  codexPath = '/Applications/ChatGPT.app/Contents/Resources/codex',
  authFile = join(homedir(), '.codex', 'auth.json'),
  timeoutMs = 10 * 60_000,
} = {}) {
  if (typeof prompt !== 'string' || typeof input !== 'string' || Buffer.byteLength(prompt, 'utf8') < 1 ||
      Buffer.byteLength(prompt, 'utf8') > MAX_PROMPT_BYTES ||
      Buffer.byteLength(input, 'utf8') > MAX_PROMPT_BYTES ||
      (schema !== undefined && !isAbsolute(schema)) ||
      !isAbsolute(codexPath) || !existsSync(codexPath)) fail('benchmark_model_contract_invalid');
  if (schema !== undefined) privateFile(schema, 256 * 1024);
  const root = mkdtempSync(join(tmpdir(), 'pulse-benchmark-model-'));
  const home = join(root, 'home');
  const codexHome = join(root, 'codex');
  const outputPath = join(root, 'answer.json');
  mkdirSync(home, { recursive: true, mode: 0o700 });
  mkdirSync(codexHome, { recursive: true, mode: 0o700 });
  const fileCredentials = existsSync(authFile);
  if (fileCredentials) {
    copyFileSync(authFile, join(codexHome, 'auth.json'));
    chmodSync(join(codexHome, 'auth.json'), 0o600);
  }
  const args = [
    'exec', '--ephemeral', '--ignore-user-config', '--ignore-rules', '--strict-config',
    '--skip-git-repo-check', '--sandbox', 'read-only', '--cd', root,
    '--model', model, '--config', `model_reasoning_effort="${effort}"`,
    '--config', 'approval_policy="never"', '--config', 'web_search="disabled"',
    '--config', 'skills.include_instructions=false',
    ...(!fileCredentials ? ['--config', 'cli_auth_credentials_store="keyring"'] : []),
    ...featureArgs(), '--enable', 'code_mode_host',
    ...(schema ? ['--output-schema', schema] : []), '--output-last-message', outputPath,
    '--json', prompt,
  ];
  const started = performance.now();
  try {
    const result = await run(codexPath, args, {
      cwd: root,
      env: cleanEnvironment(fileCredentials ? home : homedir(), fileCredentials ? codexHome : join(homedir(), '.codex')),
      input,
      timeoutMs,
    });
    if (result.status !== 0 || result.signal) {
      fail('benchmark_model_failed', `${result.stderr.slice(-300)}:${result.stdout.slice(-600)}`);
    }
    const usage = parseEventStream(result.stdout);
    const outputBytes = privateFile(outputPath, MAX_OUTPUT_BYTES);
    let value = outputBytes.toString('utf8').trim();
    if (schema) {
      try { value = JSON.parse(outputBytes); } catch { fail('benchmark_model_output_invalid'); }
    }
    return {
      value,
      receipt: {
        model, effort, usage,
        elapsed_ms: Math.round((performance.now() - started) * 10) / 10,
        prompt_digest: sha256(prompt), output_digest: sha256(outputBytes),
      },
    };
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}

async function main() {
  const options = parseArgs(process.argv.slice(2));
  const prompt = privateFile(options.prompt, MAX_PROMPT_BYTES).toString('utf8');
  const result = await runBenchmarkModel({
    prompt, schema: options.format === 'json' ? options.schema : undefined,
    model: options.model, effort: options.effort,
    timeoutMs: options.timeout_ms,
  });
  mkdirSync(dirname(options.output), { recursive: true, mode: 0o700 });
  const descriptor = openSync(options.output, 'wx', 0o600);
  try { writeFileSync(descriptor, `${JSON.stringify(result, null, 2)}\n`); } finally { closeSync(descriptor); }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((error) => {
    process.stderr.write(`${error?.message ?? error}\n`);
    process.exitCode = 1;
  });
}
