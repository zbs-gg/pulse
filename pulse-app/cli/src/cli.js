#!/usr/bin/env node
import { spawnSync, spawn } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import {
  appendFileSync,
  existsSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  mkdtempSync,
  openSync,
  statSync,
  unlinkSync,
  writeFileSync,
} from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { basename, dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const DEFAULT_BASE_URL = process.env.PULSE_BASE_URL ?? 'http://127.0.0.1:18789';
const DATA_DIR = process.env.PULSE_DATA_DIR ?? join(homedir(), '.pulse');
const SECRET_PATH = join(DATA_DIR, 'secret.key');
const MODE_PATH = join(DATA_DIR, 'mode');
const CLI_PATH = fileURLToPath(import.meta.url);
const CLI_PACKAGE_ROOT = resolve(dirname(CLI_PATH), '..');
const PREVIEW_VERSION = '0.4.2';
const PUBLIC_REPO_URL = process.env.PULSE_REPO_URL ?? 'https://github.com/zbs-gg/pulse';
const MAX_MIGRATION_FILE_BYTES = positiveEnvInt('PULSE_MIGRATION_MAX_FILE_BYTES', 300 * 1024 * 1024);
const MAX_MIGRATION_FILES = positiveEnvInt('PULSE_MIGRATION_MAX_FILES', 3000);
const FIRST_PROOF_MEMORY =
  'Atlas must not own the People Graph; Pulse owns portable continuity memory.';
const FIRST_PROOF_REMEMBER_PROMPT = `Remember this in Pulse: ${FIRST_PROOF_MEMORY}`;
const FIRST_PROOF_RECALL_PROMPT = 'What did we decide about Atlas and the People Graph?';

const args = process.argv.slice(2);
const command = args[0] ?? '--help';

function usage() {
  console.log(`Pulse host-extracted memory

Usage:
  pulse --why
  pulse install-plan claude-code [--json]
  pulse init claude-code
  pulse init claude-code --dry-run
  pulse init claude-code --yes
  pulse doctor
  pulse doctor --json
  pulse demo
  pulse connect claude-code [--remote-control]
  pulse connect claude-chat --base <https-origin-or-mcp-url> [--open]
  pulse disconnect claude-code
  pulse stop
  pulse remove claude-code
  pulse daemon --go-bin <path> [-- <extra pulse server args>]
  pulse hook session-start|user-prompt-submit|post-tool-use|stop
  pulse migrate start [--dir <dir>] [--people-graph <path>] [--open] [--watch] [--downloads <dir>] [--timeout-ms <ms>] [--interval-ms <ms>]
  pulse migrate guide chatgpt|claude|codex|claude-code [--open]
  pulse migrate concierge [--html <file>] [--brief <file>] [--open]
  pulse migrate request chatgpt|claude|codex|claude-code [--downloads <dir>] [--timeout-ms <ms>] [--interval-ms <ms>] [--html <file>] [--out <file>] [--open]
  pulse migrate preview-latest chatgpt|claude [--downloads <dir>] [--html <file>] [--out <file>] [--open]
  pulse migrate wait-latest chatgpt|claude [--downloads <dir>] [--timeout-ms <ms>] [--interval-ms <ms>] [--html <file>] [--out <file>] [--open]
  pulse migrate preview <export-folder-or-json-or-zip> [--json] [--html <file>] [--out <file>] [--open]
  pulse migrate preview-people-graph <graph-dir-or-people-index> [--json] [--html <file>] [--out <file>] [--open]
  pulse migrate commit <preview-json-file> --confirm "import pulse graph" [--privacy private|sensitive|normal] [--open]
  pulse viewer [--base <url>] [--data-dir <path>] [--thread-id <id>] [--open] [--print-url]
  pulse status
  pulse export
  pulse import --file <path>
  pulse delete --id <pulse:id>
  pulse wipe --confirm "wipe pulse memory"

Environment:
  PULSE_BASE_URL   ${DEFAULT_BASE_URL}
  PULSE_DATA_DIR   ${DATA_DIR}
  PULSE_GO_BIN     explicit Pulse Go server binary for daemon command
`);
}

function positiveEnvInt(name, fallback) {
  const value = Number.parseInt(process.env[name] ?? '', 10);
  return Number.isFinite(value) && value > 0 ? value : fallback;
}

function installPlan(host = 'claude-code') {
  return {
    product: 'Pulse MCP Preview',
    version: PREVIEW_VERSION,
    repo_url: PUBLIC_REPO_URL,
    target_host: host,
    mode: 'developer_preview',
    will_install: [
      'local Pulse daemon',
      'Pulse MCP server',
      'Claude Code MCP config',
      'Claude Code lifecycle hooks',
      'local viewer',
      'private first memory proof',
    ],
    will_write: [
      '~/.pulse',
      'project .claude/settings.local.json',
      'Claude Code MCP config',
    ],
    will_not_do: [
      'import old chats',
      'store raw transcripts by default',
      'call backend OpenAI/Anthropic/Cohere model APIs by default',
      'print secrets',
      'claim production readiness',
    ],
    requires: [
      'Node 18+',
      'npm',
      'Go',
      'Claude Code CLI',
      'internet for npm install',
    ],
    rollback: [
      'pulse wipe --confirm "wipe pulse memory"',
      'pulse disconnect claude-code',
      'pulse stop',
    ],
  };
}

function printInstallPlan(host = 'claude-code', { json = false, dryRun = false } = {}) {
  const plan = installPlan(host);
  if (json) {
    console.log(JSON.stringify(plan, null, 2));
    return;
  }
  console.log(`Pulse install plan

Product: ${plan.product} v${plan.version}
Target host: ${harnessDisplayName(host)}
Mode: ${plan.mode}
Repository: ${plan.repo_url}

Will install:
${plan.will_install.map((item) => `- ${item}`).join('\n')}

Will write:
${plan.will_write.map((item) => `- ${item}`).join('\n')}

Will not:
${plan.will_not_do.map((item) => `- ${item}`).join('\n')}

Requires:
${plan.requires.map((item) => `- ${item}`).join('\n')}

Rollback:
${plan.rollback.map((item) => `- ${item}`).join('\n')}

${dryRun ? 'Dry run only. Nothing was written.\n' : ''}Run with --yes to install after the agent explains this plan and you confirm.`);
}

function readSecret({ create = false } = {}) {
  return readSecretFromDataDir(DATA_DIR, { create });
}

function readSecretFromDataDir(dataDir, { create = false } = {}) {
  const secretPath = join(dataDir, 'secret.key');
  if (!existsSync(secretPath) && create) {
    mkdirSync(dataDir, { recursive: true, mode: 0o700 });
    writeFileSync(secretPath, randomBytes(32).toString('hex'), { mode: 0o600 });
  }
  if (!existsSync(secretPath)) {
    throw new Error(
      `Pulse secret not found at ${secretPath}. Run "pulse init claude-code" or start the Go server once.`,
    );
  }
  return readFileSync(secretPath, 'utf8').trim();
}

async function pulseFetch(path, options = {}) {
  const headers = { 'Content-Type': 'application/json' };
  const secret = existsSync(SECRET_PATH) ? readSecret() : '';
  if (secret) {
    headers['X-Pulse-Key'] = secret;
  }
  const controller = options.timeoutMs ? new AbortController() : undefined;
  const timer = controller ? setTimeout(() => controller.abort(), options.timeoutMs) : undefined;
  let response;
  try {
    response = await fetch(`${DEFAULT_BASE_URL.replace(/\/$/, '')}${path}`, {
      method: options.method ?? 'POST',
      headers,
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      signal: controller?.signal,
    });
  } finally {
    if (timer) {
      clearTimeout(timer);
    }
  }
  if (!response.ok) {
    throw new Error(`Pulse HTTP ${response.status}: ${(await response.text()).slice(0, 500)}`);
  }
  if (response.status === 204) {
    return { ok: true };
  }
  return response.json();
}

function mcpConfig(secret) {
  const localEntrypoint = localMcpEntrypoint();
  const commandConfig = localEntrypoint
    ? { command: process.execPath, args: [localEntrypoint] }
    : { command: 'npx', args: ['-y', process.env.PULSE_MCP_PACKAGE ?? '@zbs-gg/pulse-mcp@preview'] };
  return {
    type: 'stdio',
    ...commandConfig,
    env: {
      PULSE_BASE_URL: DEFAULT_BASE_URL,
      PULSE_API_KEY: secret,
    },
  };
}

function localMcpEntrypoint() {
  if (process.env.PULSE_MCP_ENTRYPOINT) {
    return existsSync(process.env.PULSE_MCP_ENTRYPOINT) ? process.env.PULSE_MCP_ENTRYPOINT : undefined;
  }
  const candidate = resolve(dirname(CLI_PATH), '..', '..', '..', 'mcp', 'dist', 'index.js');
  return existsSync(candidate) ? candidate : undefined;
}

function commandOnPath(name) {
  const pathValue = process.env.PATH ?? '';
  for (const dir of pathValue.split(':')) {
    if (dir && existsSync(join(dir, name))) {
      return true;
    }
  }
  return false;
}

function requireCommand(name) {
  if (!commandOnPath(name)) {
    throw new Error(`missing required command: ${name}`);
  }
}

function previewSourceRoot() {
  const explicit = process.env.PULSE_PREVIEW_SOURCE_DIR
    ? resolve(process.env.PULSE_PREVIEW_SOURCE_DIR)
    : '';
  const candidates = [
    explicit,
    resolve(CLI_PACKAGE_ROOT, 'vendor', 'pulse-preview-source'),
  ].filter(Boolean);
  for (const candidate of candidates) {
    if (
      existsSync(join(candidate, 'pulse-app', 'cmd', 'pulse'))
      && existsSync(join(candidate, 'mcp', 'package.json'))
    ) {
      return candidate;
    }
  }
  return undefined;
}

function pulseDaemonAddr() {
  const url = new URL(DEFAULT_BASE_URL);
  if (url.protocol !== 'http:') {
    throw new Error('local preview daemon requires an http://127.0.0.1 base URL');
  }
  const host = url.hostname || '127.0.0.1';
  const port = url.port || '80';
  return `${host}:${port}`;
}

async function pulseStatusReady() {
  if (!existsSync(SECRET_PATH)) {
    return false;
  }
  const secret = readSecret();
  try {
    const response = await fetch(`${DEFAULT_BASE_URL.replace(/\/$/, '')}/memory/status`, {
      headers: { 'X-Pulse-Key': secret },
      signal: AbortSignal.timeout(500),
    });
    return response.ok;
  } catch {
    return false;
  }
}

async function pulseStatusDetails(timeoutMs = 700) {
  if (!existsSync(SECRET_PATH)) {
    return { ok: false, error: 'secret missing' };
  }
  const secret = readSecret();
  try {
    const response = await fetch(`${DEFAULT_BASE_URL.replace(/\/$/, '')}/memory/status`, {
      headers: { 'X-Pulse-Key': secret },
      signal: AbortSignal.timeout(timeoutMs),
    });
    if (!response.ok) {
      return { ok: false, error: `HTTP ${response.status}` };
    }
    return { ok: true, data: await response.json() };
  } catch (error) {
    return { ok: false, error: error instanceof Error ? error.message : String(error) };
  }
}

function runRequired(commandName, commandArgs, { cwd, env = {} } = {}) {
  const result = spawnSync(commandName, commandArgs, {
    cwd,
    env: { ...process.env, ...env },
    encoding: 'utf8',
  });
  if (result.status !== 0) {
    const detail = `${result.stdout ?? ''}\n${result.stderr ?? ''}`.trim().slice(0, 1200);
    throw new Error(`${commandName} ${commandArgs.join(' ')} failed${detail ? `:\n${detail}` : ''}`);
  }
  return result;
}

async function ensureVendoredPreviewRuntime() {
  if (process.env.PULSE_PREVIEW_RUNTIME_SETUP === '0') {
    return { enabled: false };
  }
  const sourceRoot = previewSourceRoot();
  if (!sourceRoot) {
    return { enabled: false };
  }

  requireCommand('go');
  requireCommand('npm');
  requireCommand('claude');

  const appDir = join(sourceRoot, 'pulse-app');
  const mcpDir = join(sourceRoot, 'mcp');
  const binDir = join(DATA_DIR, 'bin');
  const logDir = join(DATA_DIR, 'logs');
  const daemonBin = join(binDir, 'pulse-preview-daemon');
  const daemonLog = join(logDir, 'pulse-preview-daemon.log');
  const pidFile = join(DATA_DIR, 'pulse-preview-daemon.pid');
  const mcpEntrypoint = join(mcpDir, 'dist', 'index.js');

  mkdirSync(binDir, { recursive: true, mode: 0o700 });
  mkdirSync(logDir, { recursive: true, mode: 0o700 });

  console.log('[pulse] Building local Pulse daemon...');
  runRequired('go', ['build', '-o', daemonBin, './cmd/pulse'], { cwd: appDir });

  console.log('[pulse] Building local Pulse MCP package...');
  runRequired('npm', ['ci'], { cwd: mcpDir });
  runRequired('npm', ['run', 'build'], { cwd: mcpDir });
  process.env.PULSE_MCP_ENTRYPOINT = mcpEntrypoint;

  if (await pulseStatusReady()) {
    console.log(`[pulse] Pulse daemon: already running at ${DEFAULT_BASE_URL}`);
    return { enabled: true, alreadyRunning: true };
  }

  console.log(`[pulse] Starting Pulse daemon at ${DEFAULT_BASE_URL}`);
  const logFd = openSync(daemonLog, 'a');
  const child = spawn(daemonBin, ['-addr', pulseDaemonAddr(), '-data-dir', DATA_DIR], {
    cwd: appDir,
    detached: true,
    env: {
      ...process.env,
      PULSE_MODE: 'local-auto',
      ANTHROPIC_API_KEY: '',
      OPENAI_API_KEY: '',
      COHERE_API_KEY: '',
    },
    stdio: ['ignore', logFd, logFd],
  });
  child.unref();
  writeFileSync(pidFile, `${child.pid}\n`, { mode: 0o600 });

  for (let i = 0; i < 80; i += 1) {
    if (await pulseStatusReady()) {
      console.log('[pulse] Pulse daemon: running locally');
      return { enabled: true, started: true };
    }
    await sleep(150);
  }
  throw new Error(`Pulse daemon did not become ready. Log: ${daemonLog}`);
}

function mcpCommandLabel(config) {
  return [config.command, ...(Array.isArray(config.args) ? config.args : [])].join(' ');
}

function installClaudeCode() {
  mkdirSync(DATA_DIR, { recursive: true, mode: 0o700 });
  const secret = readSecret({ create: true });
  const config = mcpConfig(secret);
  const json = JSON.stringify(config);
  spawnSync('claude', ['mcp', 'remove', 'pulse', '--scope', 'local'], {
    stdio: 'pipe',
    encoding: 'utf8',
  });
  const claude = spawnSync('claude', ['mcp', 'add-json', '--scope', 'local', 'pulse', json], {
    stdio: 'pipe',
    encoding: 'utf8',
  });
  if (claude.status === 0) {
    console.log('[pulse] Claude Code MCP registered via claude mcp add-json');
    return;
  }

  if (!args.includes('--write-project-mcp')) {
    const detail = (claude.stderr || claude.stdout || claude.error?.message || '').trim().slice(0, 300);
    throw new Error(
      `Claude CLI registration failed${detail ? `: ${detail}` : ''}. Refusing to write project .mcp.json because it contains PULSE_API_KEY. Re-run with --write-project-mcp to write it locally and add .mcp.json to .gitignore.`,
    );
  }

  const path = resolve(process.cwd(), '.mcp.json');
  const current = existsSync(path)
    ? JSON.parse(readFileSync(path, 'utf8'))
    : { mcpServers: {} };
  current.mcpServers = current.mcpServers ?? {};
  current.mcpServers.pulse = config;
  writeFileSync(path, `${JSON.stringify(current, null, 2)}\n`, { mode: 0o600 });
  ensureGitignoreEntry('.mcp.json');
  console.log(`[pulse] Claude CLI registration failed; wrote project MCP config to ${path}`);
  console.log('[pulse] .mcp.json contains PULSE_API_KEY and was added to .gitignore');
}

function shellQuote(value) {
  return `'${String(value).replaceAll("'", "'\\''")}'`;
}

function pulseHookCommand(name) {
  return [
    `PULSE_BASE_URL=${shellQuote(DEFAULT_BASE_URL)}`,
    `PULSE_DATA_DIR=${shellQuote(DATA_DIR)}`,
    shellQuote(process.execPath),
    shellQuote(CLI_PATH),
    'hook',
    name,
  ].join(' ');
}

function hookConfig() {
  return {
    hooks: {
      SessionStart: [
        {
          matcher: 'startup|clear|compact',
          hooks: [
            {
              type: 'command',
              command: pulseHookCommand('session-start'),
              timeout: 60,
            },
          ],
        },
      ],
      UserPromptSubmit: [
        {
          hooks: [
            {
              type: 'command',
              command: pulseHookCommand('user-prompt-submit'),
              timeout: 60,
            },
          ],
        },
      ],
      PostToolUse: [
        {
          matcher: '*',
          hooks: [
            {
              type: 'command',
              command: pulseHookCommand('post-tool-use'),
              timeout: 60,
            },
          ],
        },
      ],
      Stop: [
        {
          hooks: [
            {
              type: 'command',
              command: pulseHookCommand('stop'),
              timeout: 60,
            },
          ],
        },
      ],
    },
  };
}

function installClaudeCodeHooks({ dryRun = false } = {}) {
  const hooks = hookConfig();
  if (dryRun) {
    console.log(JSON.stringify(hooks, null, 2));
    return;
  }
  const path = resolve(process.cwd(), '.claude', 'settings.local.json');
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  const current = existsSync(path) ? JSON.parse(readFileSync(path, 'utf8')) : {};
  current.hooks = mergeHookConfig(current.hooks ?? {}, hooks.hooks);
  writeFileSync(path, `${JSON.stringify(current, null, 2)}\n`, { mode: 0o600 });
  ensureGitignoreEntry('.claude/settings.local.json');
  console.log(`[pulse] Claude Code continuity hooks written to ${path}`);
  console.log('[pulse] .claude/settings.local.json was added to .gitignore');
}

function disconnectClaudeCode() {
  spawnSync('claude', ['mcp', 'remove', 'pulse', '--scope', 'local'], {
    stdio: 'pipe',
    encoding: 'utf8',
  });

  const settingsPath = resolve(process.cwd(), '.claude', 'settings.local.json');
  if (existsSync(settingsPath)) {
    const current = JSON.parse(readFileSync(settingsPath, 'utf8'));
    if (current.hooks && typeof current.hooks === 'object') {
      for (const [event, entries] of Object.entries(current.hooks)) {
        const kept = withoutPulseHookEntries(Array.isArray(entries) ? entries : []);
        if (kept.length > 0) {
          current.hooks[event] = kept;
        } else {
          delete current.hooks[event];
        }
      }
      if (Object.keys(current.hooks).length === 0) {
        delete current.hooks;
      }
    }
    writeFileSync(settingsPath, `${JSON.stringify(current, null, 2)}\n`, { mode: 0o600 });
  }

  const mcpPath = resolve(process.cwd(), '.mcp.json');
  if (existsSync(mcpPath)) {
    const current = JSON.parse(readFileSync(mcpPath, 'utf8'));
    if (current.mcpServers && typeof current.mcpServers === 'object') {
      delete current.mcpServers.pulse;
    }
    writeFileSync(mcpPath, `${JSON.stringify(current, null, 2)}\n`, { mode: 0o600 });
  }

  console.log('[pulse] Claude Code disconnected from Pulse in this project');
}

function safeReadJSON(path) {
  try {
    return JSON.parse(readFileSync(path, 'utf8'));
  } catch {
    return undefined;
  }
}

function checkCommandVersion(name, versionArgs = ['--version']) {
  if (!commandOnPath(name)) {
    return { ok: false, detail: 'missing' };
  }
  const result = spawnSync(name, versionArgs, {
    encoding: 'utf8',
    timeout: 1200,
  });
  if (result.status !== 0) {
    return { ok: false, detail: 'found but did not answer' };
  }
  return {
    ok: true,
    detail: `${result.stdout || result.stderr}`.trim().split(/\r?\n/)[0],
  };
}

function checkNodeRuntime() {
  const major = Number.parseInt(process.versions.node.split('.')[0], 10);
  return {
    ok: major >= 18,
    detail: `v${process.versions.node}${major >= 18 ? '' : ' (needs 18+)'}`,
  };
}

function checkClaudeMCPConfigured() {
  const projectMCP = safeReadJSON(resolve(process.cwd(), '.mcp.json'));
  if (projectMCP?.mcpServers?.pulse) {
    return { ok: true, detail: 'project MCP config has Pulse' };
  }
  if (commandOnPath('claude')) {
    const result = spawnSync('claude', ['mcp', 'list'], {
      encoding: 'utf8',
      timeout: 1500,
    });
    if (result.status === 0 && /\bpulse\b/i.test(`${result.stdout}\n${result.stderr}`)) {
      return { ok: true, detail: 'Claude Code lists Pulse MCP' };
    }
  }
  return { ok: false, detail: 'Pulse MCP not found in this project' };
}

function checkClaudeHooksConfigured() {
  const settings = safeReadJSON(resolve(process.cwd(), '.claude', 'settings.local.json'));
  const required = [
    ['SessionStart', 'session-start'],
    ['UserPromptSubmit', 'user-prompt-submit'],
    ['PostToolUse', 'post-tool-use'],
    ['Stop', 'stop'],
  ];
  const missing = [];
  for (const [event, hookName] of required) {
    const entries = Array.isArray(settings?.hooks?.[event]) ? settings.hooks[event] : [];
    const commands = entries.flatMap((entry) => Array.isArray(entry?.hooks)
      ? entry.hooks.map((hook) => String(hook?.command ?? ''))
      : []);
    if (!commands.some((cmd) => cmd.includes('pulse') && cmd.includes('hook') && cmd.includes(hookName))) {
      missing.push(event);
    }
  }
  if (missing.length > 0) {
    return { ok: false, detail: `missing ${missing.join(', ')}` };
  }
  return { ok: true, detail: 'SessionStart, prompt/tool, and Stop hooks installed' };
}

async function checkViewerReady() {
  if (!existsSync(SECRET_PATH)) {
    return { ok: false, detail: 'secret missing' };
  }
  const threadId = safeThreadID(localThreadContext().threadId);
  const access = await checkViewerAccess(DEFAULT_BASE_URL.replace(/\/$/, ''), readSecret(), threadId);
  if (access.status === 0) {
    return { ok: false, detail: 'viewer data endpoint not reachable' };
  }
  if (access.status >= 200 && access.status < 300) {
    return { ok: true, detail: 'viewer data reachable' };
  }
  return { ok: false, detail: `viewer returned HTTP ${access.status}` };
}

function printDoctorLine(label, check) {
  const status = check.ok ? 'ok' : (check.warn ? 'warn' : 'missing');
  const detail = check.detail ? ` - ${check.detail}` : '';
  console.log(`${label}: ${status}${detail}`);
}

async function doctorReport() {
  const daemon = await pulseStatusDetails();
  const checks = {
    node: checkNodeRuntime(),
    npm: checkCommandVersion('npm', ['--version']),
    go: checkCommandVersion('go', ['version']),
    claude_code: checkCommandVersion('claude', ['--version']),
    daemon: daemon.ok
      ? { ok: true, detail: DEFAULT_BASE_URL.replace(/\/$/, '') }
      : { ok: false, detail: daemon.error || 'not reachable' },
    port: daemon.ok
      ? { ok: true, detail: `serving ${DEFAULT_BASE_URL.replace(/\/$/, '')}` }
      : { ok: false, detail: `${DEFAULT_BASE_URL.replace(/\/$/, '')} not reachable` },
    mcp: checkClaudeMCPConfigured(),
    hooks: checkClaudeHooksConfigured(),
    viewer: await checkViewerReady(),
    first_memory: {
      ok: true,
      detail: Number.isFinite(daemon.data?.item_count) && daemon.data.item_count > 0
        ? `${daemon.data.item_count} stored memory item(s)`
        : 'pending until the first memory proof runs',
    },
  };
  const backendEnabled = daemon.ok ? Boolean(daemon.data?.backend_llm_enabled) : false;
  const rawEnabled = daemon.ok ? Boolean(daemon.data?.raw_capture_enabled) : false;
  return {
    product: 'Pulse MCP Preview',
    version: PREVIEW_VERSION,
    target_host: 'claude-code',
    mode: 'developer_preview',
    base_url: DEFAULT_BASE_URL.replace(/\/$/, ''),
    checks,
    trust: {
      backend_llm_enabled: backendEnabled,
      raw_capture_enabled: rawEnabled,
      old_chat_import_default: false,
    },
    next_steps: [
      'pulse init claude-code --yes',
      'pulse demo',
      'pulse viewer',
      'pulse wipe --confirm "wipe pulse memory"',
    ],
  };
}

async function runDoctor(rest = []) {
  const report = await doctorReport();
  if (rest.includes('--json')) {
    console.log(JSON.stringify(report, null, 2));
    if (Object.values(report.checks).some((check) => !check.ok)) {
      process.exitCode = 1;
    }
    return;
  }

  console.log('[pulse] doctor');
  console.log('Checking the Zero-to-Wow path for Claude Code.\n');

  const checks = [
    ['Node', report.checks.node],
    ['npm', report.checks.npm],
    ['Go', report.checks.go],
    ['Claude Code CLI', report.checks.claude_code],
    ['Pulse daemon', report.checks.daemon],
    ['Port', report.checks.port],
    ['MCP', report.checks.mcp],
    ['Hooks', report.checks.hooks],
    ['Viewer', report.checks.viewer],
    ['First memory', report.checks.first_memory],
  ];

  for (const [label, check] of checks) {
    printDoctorLine(label, check);
  }

  if (report.checks.daemon.ok) {
    const backend = report.trust.backend_llm_enabled ? 'on' : 'off';
    const raw = report.trust.raw_capture_enabled ? 'on' : 'off';
    console.log(`\nTrust: backend LLM ${backend}; raw transcript capture ${raw}`);
    console.log('What Pulse will tell Claude next: pulse viewer');
  } else {
    console.log('\nTrust: backend LLM unknown; raw transcript capture unknown until daemon answers.');
  }

  const failed = checks.filter(([, check]) => !check.ok);
  if (failed.length > 0) {
    console.log('\nNext:');
    console.log('  pulse init claude-code --yes');
    console.log('  pulse demo');
    console.log('  pulse viewer');
    process.exitCode = 1;
    return;
  }

  console.log('\nPulse is ready for the first memory proof.');
  console.log(`Ask Claude Code: "${FIRST_PROOF_REMEMBER_PROMPT}"`);
}

function stopPreviewDaemon() {
  const pidFile = join(DATA_DIR, 'pulse-preview-daemon.pid');
  if (!existsSync(pidFile)) {
    console.log('[pulse] No Pulse preview daemon pid found.');
    return;
  }
  const pid = Number.parseInt(readFileSync(pidFile, 'utf8').trim(), 10);
  let stopped = false;
  if (Number.isFinite(pid) && pid > 0) {
    try {
      process.kill(pid, 'SIGTERM');
      stopped = true;
    } catch {
      stopped = false;
    }
  }
  try {
    unlinkSync(pidFile);
  } catch {
    // Nothing useful to do; the next init will overwrite the pid file.
  }
  if (stopped) {
    console.log(`[pulse] Stopped Pulse preview daemon pid ${pid}.`);
  } else {
    console.log('[pulse] No running Pulse preview daemon found. Removed stale pid file.');
  }
}

function mergeHookConfig(existing, incoming) {
  const out = { ...existing };
  for (const [event, entries] of Object.entries(incoming)) {
    const existingEntries = Array.isArray(out[event]) ? out[event] : [];
    out[event] = [...withoutPulseHookEntries(existingEntries), ...entries];
  }
  return out;
}

function withoutPulseHookEntries(entries) {
  const kept = [];
  for (const entry of entries) {
    const hooks = Array.isArray(entry?.hooks)
      ? entry.hooks.filter((hook) => !isPulseHookCommand(hook?.command))
      : [];
    if (hooks.length > 0) {
      kept.push({ ...entry, hooks });
    }
  }
  return kept;
}

function isPulseHookCommand(command) {
  const text = String(command ?? '');
  const hookName = String.raw`(?:session-start|user-prompt-submit|post-tool-use|stop)`;
  return new RegExp(String.raw`\bpulse\s+hook\s+${hookName}\b`).test(text)
    || (
      text.includes('PULSE_BASE_URL=')
      && text.includes('PULSE_DATA_DIR=')
      && new RegExp(String.raw`\bhook\s+${hookName}\b`).test(text)
    );
}

function writeLocalAutoMode() {
	mkdirSync(DATA_DIR, { recursive: true, mode: 0o700 });
	writeFileSync(MODE_PATH, 'local-auto\n', { mode: 0o600 });
}

function harnessDisplayName(host) {
  switch (host) {
    case 'claude-code':
      return 'Claude Code';
    case 'codex':
      return 'Codex';
    case 'gemini-cli':
      return 'Gemini CLI';
    case 'cursor':
      return 'Cursor';
    default:
      return 'Claude Code';
  }
}

function harnessTag(host) {
  return String(host || 'claude-code').replace(/-/g, '_');
}

function firstRunViewerURL(host = 'claude-code', threadId = localThreadContext().threadId) {
  const baseURL = DEFAULT_BASE_URL.replace(/\/$/, '');
  const secret = readSecret({ create: true });
  const url = new URL(`${baseURL}/viewer`);
  url.searchParams.set('key', secret);
  url.searchParams.set('thread_id', safeThreadID(threadId));
  url.searchParams.set('first_run', '1');
  url.searchParams.set('host', host);
  return url.toString();
}

function shouldAnimatePulseConnect() {
  if (process.env.CI || process.env.NO_COLOR || args.includes('--no-animate')) {
    return false;
  }
  if (process.env.PULSE_CLI_ANIMATION === '0') {
    return false;
  }
  return process.env.PULSE_CLI_ANIMATION === '1' || Boolean(process.stdout.isTTY);
}

async function showPulseConnectAnimation() {
  if (!shouldAnimatePulseConnect()) {
    return;
  }
  const frames = [
    '[pulse]        .  :  o  ♡  o  :  .',
    '[pulse]        .  :  o  ♥  o  :  .',
    '[pulse]        .  :  O  ♥  O  :  .',
    '[pulse]        .  :  o  ♥  o  :  .',
  ];
  for (const frame of frames) {
    process.stdout.write(`\r${frame}`);
    await sleep(120);
  }
  process.stdout.write('\n');
}

async function rememberInstallMemory(host = 'claude-code') {
  const display = harnessDisplayName(host);
  const capsule = {
    schema: 'pulse.memory_capsule.v1',
    source: {
      host: 'pulse-cli',
      conversation_scope: 'install_event',
      timestamp: new Date().toISOString(),
    },
    items: [{
      kind: 'system_event',
      redacted_summary: `User installed Pulse MCP and connected it to ${display}.`,
      confidence: 1.0,
      evidence_hint: 'user_confirmed',
      privacy_tier: 'private',
      retention: 'project',
      tags: ['pulse_install', 'first_memory', harnessTag(host)],
    }],
    raw_input_included: false,
  };
  try {
    const out = await pulseFetch('/memory/remember', { body: capsule, timeoutMs: 2500 });
    return { ok: true, ids: Array.isArray(out.ids) ? out.ids : [] };
  } catch {
    return { ok: false, ids: [] };
  }
}

async function connectClaudeCode() {
	const remoteControl = args.includes('--remote-control');
	if (args.includes('--dry-run')) {
    const config = mcpConfig('<local-secret>');
		console.log('[pulse] Claude Code connect dry run');
		console.log(`[pulse] MCP command: ${mcpCommandLabel(config)}`);
    installClaudeCodeHooks({ dryRun: true });
    if (remoteControl) {
      printRemoteControlNextSteps();
    }
    return;
  }
  await ensureVendoredPreviewRuntime();
	writeLocalAutoMode();
	installClaudeCode();
	installClaudeCodeHooks();
  const firstMemory = await rememberInstallMemory('claude-code');
  const dashboard = firstRunViewerURL('claude-code');
  await showPulseConnectAnimation();
	console.log(`
[pulse] pulse  .  :  o  ♥  o  :  .
[pulse] memory proof first, import later

[pulse] Pulse is breathing locally.
Pulse wave:
  .  :  o  ♥  o  :  .
──────────────────────────────────

Thank you for installing Pulse MCP.
Pulse helps Claude Code remember what actually mattered:
1. Save one small memory.
2. Recall it in a fresh session.
3. Keep the thread across AI chats.

Claude Code:             connected
MCP:                     configured
Hooks:                   installed
backend LLM off
raw transcript capture off
Storage:                 local SQLite

No backend model is running by default.
No emotion is stored until you choose it.
Source import waits until after the first proof.

What Pulse will tell Claude next:
  pulse viewer

Try first memory:
  Ask Claude Code:
    "${FIRST_PROOF_REMEMBER_PROMPT}"
  Then start a fresh Claude Code session and ask:
    "${FIRST_PROOF_RECALL_PROMPT}"

Explore your universe and yourself with Claude Code + Pulse.

Dashboard:
  ${dashboard}

Import old chats later:
  pulse migrate start --open
`);
  if (firstMemory.ok) {
    console.log(`[pulse] First memory saved locally${firstMemory.ids.length > 0 ? `: ${firstMemory.ids[0]}` : '.'}`);
  } else {
    console.log('[pulse] First memory will save when the local Pulse daemon is running.');
  }
	if (remoteControl) {
		printRemoteControlNextSteps();
	}
}

function demoMemoryCapsule() {
  return {
    schema: 'pulse.memory_capsule.v1',
    source: {
      host: 'pulse-cli',
      conversation_scope: 'current_turn',
      timestamp: new Date().toISOString(),
    },
    items: [{
      kind: 'decision',
      redacted_summary: FIRST_PROOF_MEMORY,
      confidence: 1.0,
      evidence_hint: 'user_selected',
      privacy_tier: 'private',
      retention: 'project',
      tags: ['pulse_demo', 'first_proof', 'claude_code'],
    }],
    raw_input_included: false,
  };
}

function printDemoRitual({ daemonMissing = false } = {}) {
  console.log(`
[pulse] Pulse demo

Stop re-explaining your project to Claude Code.
Pulse keeps the thread.

This is a local-first developer preview:
- backend LLM off by default
- raw transcript capture off
- structured host-extracted memory
- viewer before import

${daemonMissing ? 'Pulse daemon is not reachable yet.\n' : ''}Without Pulse:
  A fresh Claude Code session would not know the Atlas decision.

First proof:
1. Run:
   pulse doctor
2. Ask Claude Code:
   "${FIRST_PROOF_REMEMBER_PROMPT}"
3. Open a fresh Claude Code session and ask:
   "${FIRST_PROOF_RECALL_PROMPT}"
4. Inspect what Claude will see next:
   pulse viewer

Control:
  pulse wipe --confirm "wipe pulse memory"
  pulse disconnect claude-code

Start with one memory first. Old chats can wait.
`);
}

async function printPulseDemo() {
  const status = await pulseStatusDetails(500);
  if (!status.ok) {
    printDemoRitual({ daemonMissing: true });
    console.log('Next: run `pulse init claude-code --yes`, then run `pulse demo` again.');
    return;
  }

  console.log(`
[pulse] Pulse demo

Stop re-explaining your project to Claude Code.
Pulse keeps the thread.

Without Pulse:
  A fresh Claude Code session would not know this decision.
`);
  try {
    const remembered = await pulseFetch('/memory/remember', {
      body: demoMemoryCapsule(),
      timeoutMs: 2500,
    });
    const recall = await pulseFetch('/memory/recall', {
      body: {
        query: FIRST_PROOF_RECALL_PROMPT,
        limit: 5,
        privacy_ceiling: 'private',
        include_evidence_refs: true,
      },
      timeoutMs: 2500,
    });
    const resume = await pulseFetch('/continuity/resume', {
      body: {
        thread_id: localThreadContext().threadId,
        project_id: localThreadContext().projectId,
        session_id: localThreadContext().sessionId,
        host: 'claude-code',
        token_budget: 1200,
      },
      timeoutMs: 2500,
    });
    const recalled = Array.isArray(recall.items) ? recall.items : [];
    const resumeMarkdown = String(resume?.resume_markdown ?? '');
    const viewer = firstRunViewerURL('claude-code');

    console.log(`With Pulse:
  Saved: ${(remembered.ids ?? []).join(', ') || 'pulse:demo:first-proof'}
  Recall found: ${recalled.length} item(s)
  Resume includes: ${resumeMarkdown.includes(FIRST_PROOF_MEMORY) ? FIRST_PROOF_MEMORY : 'open Pulse viewer to inspect next context'}

Viewer:
  ${viewer}

Control:
  pulse wipe --confirm "wipe pulse memory"
  pulse disconnect claude-code
`);
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    console.log(`Pulse daemon answered, but the live demo did not complete: ${message}`);
    console.log('Next: run `pulse doctor`, then open `pulse viewer` to inspect local state.');
  }
}

function printRemoteControlNextSteps() {
  console.log(`
[pulse] Claude Code + mobile continuity ready
Remote Control:    claude --remote-control "Pulse Memory"
Claude mobile:     open Claude app -> Code, or scan the QR shown by Claude Code
Local memory:      Pulse MCP + hooks installed
Graph ingestion:   host-extracted via pulse_graph_delta, backend LLM off
Viewer:            pulse viewer
`);
}

function normalizePublicOrigin(value) {
  const raw = value ?? process.env.PULSE_PUBLIC_ORIGIN ?? '';
  if (!raw) {
    throw new Error('pulse connect claude-chat requires --base <https-origin-or-mcp-url> or PULSE_PUBLIC_ORIGIN');
  }
  const url = new URL(raw);
  if (url.protocol !== 'https:') {
    throw new Error('Claude Chat custom connectors require a public HTTPS origin');
  }
  url.search = '';
  url.hash = '';
  url.pathname = url.pathname.replace(/\/mcp\/?$/, '');
  if (url.pathname === '/') {
    url.pathname = '';
  }
  return url.toString().replace(/\/$/, '');
}

function connectClaudeChat() {
  const publicOrigin = normalizePublicOrigin(getArg('--base'));
  const connectorURL = `${publicOrigin}/mcp`;
  const threadId = getArg('--thread') ?? 'pulse-live-claude-ui-smoke';

  console.log(`
[pulse] Claude Chat custom connector handoff
──────────────────────────────────────────

Connector URL:
  ${connectorURL}

Preflight:
  npx -p @zbs-gg/pulse-mcp pulse-mcp-claude-smoke -- \\
    --base ${publicOrigin} \\
    --thread ${threadId} \\
    --json

Claude UI:
  1. Open Claude settings -> Connectors.
  2. Add a custom connector.
  3. Paste the Connector URL above.
  4. Complete OAuth.

Important:
  The final install is a persistent Claude account/workspace change.
  Confirm it in the logged-in Claude UI only when you intentionally want Pulse connected.

Proof prompt 1:
  Use Pulse to save this thread decision: Pulse graph is owned by Pulse, but the current Claude harness extracts semantic deltas using the user's Claude subscription. Thread id: ${threadId}.

Expected tool:
  pulse_graph_delta

Proof prompt 2, in a fresh Claude chat:
  Use Pulse to resume thread ${threadId}. What did we decide?

Expected tool:
  pulse_resume
`);

  if (args.includes('--open')) {
    spawnSync('open', ['https://claude.ai/settings/connectors'], { stdio: 'ignore' });
  }
}

function daemon(extraArgs) {
  mkdirSync(DATA_DIR, { recursive: true, mode: 0o700 });
  const goBin = getArg('--go-bin');
  const bin = goBin ?? process.env.PULSE_GO_BIN;
  if (!bin) {
    throw new Error('pulse daemon requires --go-bin <path> or PULSE_GO_BIN. The npm "pulse" bin is the CLI, not the Go server.');
  }
  const serverArgs = ['-data-dir', DATA_DIR, '-addr', new URL(DEFAULT_BASE_URL).host, ...extraArgs.filter((arg, i) => arg !== '--go-bin' && extraArgs[i - 1] !== '--go-bin')];
  const child = spawn(bin, serverArgs, { stdio: 'inherit' });
  child.on('error', (err) => {
    console.error(`[pulse] failed to start Go server ${bin}: ${err.message}`);
    process.exit(1);
  });
  child.on('exit', (code, signal) => {
    if (signal) {
      process.kill(process.pid, signal);
      return;
    }
    process.exit(code ?? 0);
  });
}

function ensureGitignoreEntry(entry) {
  const path = resolve(process.cwd(), '.gitignore');
  const current = existsSync(path) ? readFileSync(path, 'utf8') : '';
  const lines = current.split(/\r?\n/).map((line) => line.trim());
  if (lines.includes(entry)) {
    return;
  }
  appendFileSync(path, `${current.endsWith('\n') || current === '' ? '' : '\n'}${entry}\n`, {
    mode: 0o600,
  });
}

function localThreadContext() {
  const host = process.env.PULSE_HOST ?? 'claude-code';
  const projectId = process.env.PULSE_PROJECT_ID ?? slug(basename(process.cwd()));
  const threadId = process.env.PULSE_THREAD_ID ?? projectId;
  const sessionId =
    process.env.PULSE_SESSION_ID ??
    process.env.CLAUDE_SESSION_ID ??
    process.env.CODEX_SESSION_ID ??
    `${host}:${threadId}:local`;
  return { host, projectId, threadId, sessionId };
}

function slug(value) {
  const out = String(value ?? '')
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9._:-]+/g, '-')
    .replace(/^[.:\-_]+|[.:\-_]+$/g, '')
    .slice(0, 96);
  return out || 'default';
}

async function readStdin() {
  if (process.stdin.isTTY) {
    return '';
  }
  let data = '';
  for await (const chunk of process.stdin) {
    data += chunk;
  }
  return data;
}

function parseHookPayload(raw) {
  raw = String(raw ?? '').trim();
  if (!raw) {
    return {};
  }
  try {
    return JSON.parse(raw);
  } catch {
    return { text: raw };
  }
}

function safeText(value, max = 260) {
  if (value === undefined || value === null) {
    return '';
  }
  let text = typeof value === 'string' ? value : JSON.stringify(value);
  text = text.replace(/\s+/g, ' ').trim();
  if (!text) {
    return '';
  }
  const lower = text.toLowerCase();
  for (const marker of [
    '/users/',
    'file://',
    'token=',
    'api_key',
    'apikey',
    'password',
    'secret',
    'private_key',
    'begin private key',
    'sk-',
    'akia',
    'xoxb-',
    'ghp_',
  ]) {
    if (lower.includes(marker)) {
      return '';
    }
  }
  return text.length > max ? `${text.slice(0, max - 1)}…` : text;
}

function firstSafeText(payload, keys) {
  for (const key of keys) {
    const value = payload?.[key];
    const text = safeText(value);
    if (text) {
      return text;
    }
  }
  return '';
}

function safeStringArray(value) {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.map((item) => safeText(item, 360)).filter(Boolean).slice(0, 20);
}

function assertSafeZipListing(zipPath) {
  const listed = spawnSync('unzip', ['-Z1', zipPath], {
    encoding: 'utf8',
    maxBuffer: 5 * 1024 * 1024,
  });
  if (listed.status !== 0) {
    throw new Error(`could not inspect zip archive: ${listed.stderr || listed.stdout}`);
  }
  const files = listed.stdout.split(/\r?\n/).filter(Boolean);
  if (files.length === 0) {
    throw new Error('zip archive is empty');
  }
  for (const file of files) {
    if (
      file.startsWith('/') ||
      file.startsWith('\\') ||
      file.includes('..') ||
      /^[A-Za-z]:[\\/]/.test(file)
    ) {
      throw new Error(`zip archive contains unsafe path: ${file}`);
    }
  }
}

function unpackZipArchive(zipPath) {
  if (!existsSync(zipPath)) {
    throw new Error(`archive path does not exist: ${zipPath}`);
  }
  if (spawnSync('unzip', ['-v'], { stdio: 'ignore' }).status !== 0) {
    throw new Error('zip preview requires the system "unzip" command');
  }
  assertSafeZipListing(zipPath);
  const outDir = mkdtempSync(join(tmpdir(), 'pulse-migrate-unzip.'));
  const unzipped = spawnSync('unzip', ['-qq', zipPath, '-d', outDir], {
    encoding: 'utf8',
    maxBuffer: 5 * 1024 * 1024,
  });
  if (unzipped.status !== 0) {
    throw new Error(`could not unpack zip archive: ${unzipped.stderr || unzipped.stdout}`);
  }
  return outDir;
}

function migrationSizeLimitLabel() {
  const mb = MAX_MIGRATION_FILE_BYTES / (1024 * 1024);
  return Number.isInteger(mb) ? `${mb}MB` : `${MAX_MIGRATION_FILE_BYTES} bytes`;
}

function shouldSkipMigrationEntry(path, entryName = basename(path)) {
  const name = entryName.toLowerCase();
  if (name === 'node_modules' || name === '__macosx' || name === '.git' || name === '.obsidian') {
    return true;
  }
  if (name === 'attachments' || name === 'reports' || name === 'compiled_dialogs') {
    return true;
  }
  if (name === '.ds_store') {
    return true;
  }
  return path
    .toLowerCase()
    .split(/[\\/]+/)
    .some((part) => part === 'attachments' || part === 'reports' || part === 'compiled_dialogs');
}

function isMigrationSourceFile(path) {
  const lowerPath = path.toLowerCase();
  return lowerPath.endsWith('.json') || lowerPath.endsWith('.jsonl') || lowerPath.endsWith('.md');
}

function discoverMigrationFiles(target) {
  let root = resolve(target);
  let archiveWasUnpacked = false;
  if (!existsSync(root)) {
    throw new Error(`archive path does not exist: ${root}`);
  }
  if (statSync(root).isFile() && root.toLowerCase().endsWith('.zip')) {
    root = unpackZipArchive(root);
    archiveWasUnpacked = true;
  }
  const files = [];
  const skipped = [];

  function visit(path, depth) {
    if (files.length >= MAX_MIGRATION_FILES) {
      return;
    }
    const info = statSync(path);
    if (info.isDirectory()) {
      if (shouldSkipMigrationEntry(path)) {
        return;
      }
      if (depth > 8) {
        return;
      }
      for (const entry of readdirSync(path, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
        if (shouldSkipMigrationEntry(join(path, entry.name), entry.name)) {
          continue;
        }
        visit(join(path, entry.name), depth + 1);
      }
      return;
    }
    if (!info.isFile() || shouldSkipMigrationEntry(path) || !isMigrationSourceFile(path)) {
      return;
    }
    if (info.size > MAX_MIGRATION_FILE_BYTES) {
      skipped.push({ file: basename(path), reason: `larger than ${migrationSizeLimitLabel()} preview limit` });
      return;
    }
    files.push(path);
  }

  visit(root, 0);
  if (files.length === 0) {
    throw new Error(`no JSON/JSONL/Markdown export files found at archive path: ${root}`);
  }
  return { root, files, skipped, archiveWasUnpacked };
}

function conversationRoots(value) {
  if (Array.isArray(value)) {
    return value;
  }
  for (const key of ['conversations', 'chats', 'items']) {
    if (Array.isArray(value?.[key])) {
      return value[key];
    }
  }
  return [value];
}

function contentText(content) {
  if (typeof content === 'string') {
    return content;
  }
  if (Array.isArray(content?.parts)) {
    return content.parts.map(contentText).filter(Boolean).join(' ');
  }
  if (Array.isArray(content)) {
    return content.map(contentText).filter(Boolean).join(' ');
  }
  for (const key of ['text', 'value', 'content']) {
    if (typeof content?.[key] === 'string') {
      return content[key];
    }
  }
  return '';
}

function messageText(message) {
  if (!message || typeof message !== 'object') {
    return typeof message === 'string' ? message : '';
  }
  for (const key of ['text', 'content', 'message', 'prompt', 'response']) {
    const text = contentText(message[key]);
    if (text) {
      return text;
    }
  }
  return '';
}

function extractChatGPTMessages(record) {
  const mapping = record?.mapping;
  if (!mapping || typeof mapping !== 'object') {
    return [];
  }
  return Object.values(mapping)
    .map((node) => messageText(node?.message))
    .filter(Boolean);
}

function extractLinearMessages(record) {
  for (const key of ['chat_messages', 'messages', 'turns']) {
    if (Array.isArray(record?.[key])) {
      return record[key].map(messageText).filter(Boolean);
    }
  }
  return [];
}

function extractMigrationConversations(parsed, file) {
  const out = [];
  for (const record of conversationRoots(parsed)) {
    if (!record || typeof record !== 'object') {
      continue;
    }
    const chatgpt = extractChatGPTMessages(record);
    if (chatgpt.length > 0) {
      out.push({
        source: 'chatgpt',
        title: humanThreadTitle(record.title ?? record.name ?? basename(file, '.json'), 'chatgpt'),
        messages: chatgpt,
      });
      continue;
    }
    const linear = extractLinearMessages(record);
    if (linear.length > 0) {
      out.push({
        source: record.chat_messages ? 'claude' : 'unknown',
        title: humanThreadTitle(record.name ?? record.title ?? basename(file, '.json'), record.chat_messages ? 'claude' : 'unknown'),
        messages: linear,
      });
    }
  }
  return out;
}

function markdownFrontmatterValue(markdown, key) {
  const text = String(markdown ?? '');
  const frontmatter = text.match(/^---\r?\n([\s\S]*?)\r?\n---/);
  if (!frontmatter) {
    return '';
  }
  const escaped = key.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = frontmatter[1].match(new RegExp(`^${escaped}:\\s*(.+)$`, 'im'));
  return safeText((match?.[1] ?? '').replace(/^["']|["']$/g, ''), 120);
}

function inferMarkdownConversationSource(file, root, markdown) {
  const provider = markdownFrontmatterValue(markdown, 'provider').toLowerCase();
  if (provider === 'chatgpt' || provider === 'claude') {
    return provider;
  }
  const relative = relativeEvidenceRef(file, root).toLowerCase();
  if (/(^|\/)chatgpt(\/|$)/.test(relative)) {
    return 'chatgpt';
  }
  if (/(^|\/)claude(\/|$)/.test(relative)) {
    return 'claude';
  }
  return 'unknown';
}

function markdownConversationTitle(markdown, file, source) {
  const text = String(markdown ?? '');
  const explicitTitle = text.match(/^#\s+Title:\s*(.+)$/im)?.[1]
    ?? text.match(/^#\s+(.+)$/m)?.[1]
    ?? '';
  return humanThreadTitle(explicitTitle || basename(file, '.md'), source);
}

function isNexusMarkdownConversation(markdown) {
  const text = String(markdown ?? '');
  return (
    /nexus:\s*nexus-ai-chat-importer/i.test(text) ||
    /^\s*>\s*\[!nexus_(?:user|agent|assistant|system)\]/im.test(text)
  );
}

function extractNexusMarkdownMessages(markdown) {
  const messages = [];
  let current = [];
  const flush = () => {
    const text = current.join('\n').replace(/\n{3,}/g, '\n\n').trim();
    if (text) {
      messages.push(text);
    }
    current = [];
  };
  for (const line of String(markdown ?? '').split(/\r?\n/)) {
    if (/^\s*>\s*\[!nexus_(?:user|agent|assistant|system)\]/i.test(line)) {
      flush();
      continue;
    }
    if (current.length === 0 && !/^\s*>/.test(line)) {
      continue;
    }
    if (/^\s*>/.test(line)) {
      const text = line.replace(/^\s*>\s?/, '').trimEnd();
      if (!text.trim()) {
        continue;
      }
      if (/^\*\*(?:User|Human|Assistant|Claude|ChatGPT|System)\*\*\s*(?:[-:]\s*)?(?:\d{4}-\d{2}-\d{2}|\d{1,2}:\d{2}|$)/i.test(text)) {
        continue;
      }
      current.push(text);
      continue;
    }
    flush();
  }
  flush();
  return messages;
}

function extractMarkdownConversations(file, root) {
  const markdown = readFileSync(file, 'utf8');
  if (!isNexusMarkdownConversation(markdown)) {
    return [];
  }
  const messages = extractNexusMarkdownMessages(markdown).filter(Boolean);
  if (messages.length === 0) {
    return [];
  }
  const source = inferMarkdownConversationSource(file, root, markdown);
  return [{
    source,
    title: markdownConversationTitle(markdown, file, source),
    messages,
  }];
}

function jsonlSource(record) {
  if (record?.payload && (record.type === 'response_item' || record.type === 'session_meta')) {
    return 'codex';
  }
  if (record?.sessionId && Object.hasOwn(record, 'content')) {
    return 'claude-code';
  }
  return 'unknown';
}

function hostDisplayName(source) {
  if (source === 'claude-code') return 'Claude Code';
  if (source === 'chatgpt') return 'ChatGPT';
  if (source === 'claude') return 'Claude';
  if (source === 'codex') return 'Codex';
  return 'Pulse';
}

function isTechnicalRef(value) {
  const text = String(value ?? '').trim();
  return (
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(text) ||
    /^rollout-\d{4}-\d{2}-\d{2}t\d{2}-\d{2}-\d{2}-[0-9a-f-]{20,}$/i.test(text) ||
    /^session-[0-9a-f-]{12,}$/i.test(text) ||
    /^agent-[0-9a-f]{8,}$/i.test(text) ||
    /^source:memory\//i.test(text) ||
    /^people\/person-[0-9a-f]+\.md$/i.test(text)
  );
}

function humanThreadTitle(title, source) {
  const text = safeText(title, 120);
  if (!text || isTechnicalRef(text) || text === 'session') {
    return `${hostDisplayName(source)} session`;
  }
  if (/^rollout-/i.test(text)) {
    return `${hostDisplayName(source)} session`;
  }
  return text;
}

function isSpecificHumanThreadTitle(title, source) {
  const text = safeText(title, 120);
  return Boolean(text && text !== `${hostDisplayName(source)} session` && !isTechnicalRef(text));
}

function extractJSONLConversations(file) {
  const messages = [];
  const sourceCounts = { codex: 0, 'claude-code': 0, unknown: 0 };
  const lines = readFileSync(file, 'utf8').split(/\r?\n/);
  for (const line of lines) {
    if (!line.trim()) {
      continue;
    }
    let record;
    try {
      record = JSON.parse(line);
    } catch {
      continue;
    }
    const source = jsonlSource(record);
    sourceCounts[source] = (sourceCounts[source] ?? 0) + 1;
    const text =
      messageText(record.payload) ||
      messageText(record.message) ||
      messageText(record);
    if (text) {
      messages.push(text);
    }
  }
  if (messages.length === 0) {
    return [];
  }
  const source = dominantSource(sourceCounts);
  return [{
    source,
    title: humanThreadTitle(basename(file, '.jsonl'), source),
    messages,
  }];
}

function dominantSource(sourceCounts) {
  const active = Object.entries(sourceCounts).filter(([, count]) => count > 0);
  if (active.length === 0) {
    return 'unknown';
  }
  const known = active.filter(([source]) => source !== 'unknown');
  if (known.length === 1) {
    return known[0][0];
  }
  if (known.length > 1) {
    return 'mixed';
  }
  if (active.length === 1) {
    return active[0][0];
  }
  return 'mixed';
}

function collectPersonCandidates(text) {
  const safe = safeText(text, 800);
  if (!safe) {
    return [];
  }
  const blocked = new Set([
    'Accents', 'Action', 'Actionable', 'Actions', 'Active', 'Added', 'After', 'Agent', 'All', 'Always', 'Anthropic', 'Any', 'Applications', 'Approvals', 'Architecture', 'Archive', 'Apr', 'Asia', 'Assistant', 'Async', 'Atlas', 'Auto', 'Avoid',
    'Bangkok', 'Bash', 'Benchmark', 'Bench', 'Bitwarden', 'Both', 'Browser', 'Build', 'Built', 'Bundle',
    'Background', 'Before',
    'Caches', 'Chat', 'ChatGPT', 'Check', 'Click', 'Claude', 'Cloudflare', 'Code', 'Codex', 'Collaboration', 'Command', 'Commit', 'Companion', 'Complete', 'Confirmed', 'Content', 'Context', 'Conversation', 'Correction', 'Create', 'Created',
    'Choose', 'Continuity', 'Conversations', 'Consulting', 'Current', 'Data', 'Decision', 'Default', 'Demo', 'Download',
    'Emo', 'Error', 'Even', 'Evidence', 'Export', 'Extraction',
    'Exit', 'Explore', 'Failed', 'File', 'Files', 'Filesystem', 'Final', 'First', 'Fix', 'Full', 'Further',
    'Garden', 'Gateway', 'Google', 'Graph',
    'Connector', 'Cron',
    'Group', 'Guidance', 'Harness', 'Has', 'Hearth', 'Heart', 'Heavy', 'Hello', 'Hermes', 'History', 'However',
    'Import', 'Insight', 'Insights', 'Instead', 'Invalid',
    'June',
    'Keep', 'Known', 'Let', 'Library', 'Live',
    'Make', 'Making', 'Map', 'Mark', 'Marked', 'May', 'Mem', 'Memory', 'Mode', 'Module', 'Move',
    'Network', 'New', 'Newsreader', 'Not', 'Now',
    'Obsidian', 'Only', 'Open', 'OpenAI', 'Opus', 'Output',
    'Pages', 'Pass', 'People', 'Personal', 'Phase', 'Plan', 'Planning', 'Port', 'Preview', 'Primary', 'Privacy', 'Pro', 'Progression', 'Project', 'Prompt', 'Pulse', 'Python',
    'Process', 'Questions',
    'Read', 'Real', 'Record', 'Remember', 'Request', 'Review', 'Run', 'Running', 'Russian',
    'Script', 'Section', 'Server', 'Small', 'Sonnet', 'Source', 'State', 'Step',
    'Task', 'Telegram', 'Telethon', 'Terminal', 'Test', 'Tests', 'The', 'This', 'Thread', 'Threads', 'Three', 'Ticker', 'Timestamps', 'Tool', 'Total', 'True', 'Tweaks', 'Two',
    'Updated', 'Usage', 'Use', 'User', 'Users',
    'Verification', 'Verified', 'Verifier', 'Verify', 'Viewer',
    'Waiting', 'Worker',
    'Work', 'Write',
    'You', 'Your',
    'Акт', 'Аудитория', 'Автор', 'Без', 'Все', 'Вместо', 'Вообще', 'Вот', 'Всё', 'Готово', 'Два', 'Движение', 'Для', 'Его', 'Если', 'Ещё', 'Жду', 'Запускаю', 'Или', 'Как', 'Карта', 'Когда', 'Курс', 'Лекция', 'Медленно', 'Мне', 'Мои', 'Назови', 'Напиши', 'Обращ', 'Один', 'Она', 'Пауза', 'Плюс', 'После', 'Пока', 'Потом', 'Потому', 'Приоритет', 'Проверю', 'Привет', 'Просто', 'Пхукет', 'Сейчас', 'Сквозная', 'Сначала', 'Тайланд', 'Теперь', 'Тематика', 'Тело', 'Тихо', 'Только', 'Три', 'Цель', 'Через', 'Что', 'Чтобы', 'Это',
  ]);
  const out = [];
  const regex = /(?:^|[^\p{L}])(\p{Lu}[\p{Ll}\p{Mn}'-]{2,})(?=$|[^\p{L}])/gu;
  for (const match of safe.matchAll(regex)) {
    const candidate = normalizePersonCandidate(match[1]);
    if (
      !blocked.has(candidate)
      && !/[a-z]+-[a-z]+/i.test(candidate)
      && !candidate.includes("'")
      && !out.includes(candidate)
    ) {
      out.push(candidate);
    }
  }
  return out;
}

function normalizePersonCandidate(candidate) {
  const text = safeText(candidate, 80);
  const aliases = new Map([
    ['Эли', 'Элли'],
    ['Никите', 'Никита'],
    ['Никиты', 'Никита'],
    ['Никиту', 'Никита'],
    ['Никитой', 'Никита'],
    ['Сони', 'Соня'],
    ['Соню', 'Соня'],
    ['Соней', 'Соня'],
    ['Сонин', 'Соня'],
  ]);
  return aliases.get(text) ?? text;
}

function collectEmotionCandidates(text) {
  const safe = safeText(text, 800).toLowerCase();
  if (!safe) {
    return [];
  }
  const markers = [
    ['relief', ['relief', 'облегч']],
    ['hurt', ['hurt', 'pain', 'боль', 'задел']],
    ['anxiety', ['anxiety', 'anxious', 'тревог', 'страх']],
    ['joy', ['joy', 'happy', 'радост', 'счаст']],
    ['anger', ['anger', 'angry', 'злост', 'злюсь']],
    ['shame', ['shame', 'стыд']],
    ['excitement', ['excited', 'exciting', 'вдохнов', 'азарт']],
  ];
  const out = [];
  for (const [label, terms] of markers) {
    if (terms.some((term) => safe.includes(term))) {
      out.push(label);
    }
  }
  return out;
}

function addMigrationSignals(previewState, conversation) {
  const title = conversation.title || conversation.source || 'Untitled thread';
  previewState.memoryCounts.set(
    title,
    (previewState.memoryCounts.get(title) ?? 0) + conversation.messages.length,
  );
  if (!previewState.threadPeople.has(title)) {
    previewState.threadPeople.set(title, new Set());
  }
  if (!previewState.threadEmotions.has(title)) {
    previewState.threadEmotions.set(title, new Set());
  }
  for (const message of conversation.messages) {
    previewState.messages += 1;
    const safe = safeText(message, 800);
    if (!safe && String(message ?? '').trim()) {
      previewState.redactedFragments += 1;
      continue;
    }
    const peopleInMessage = collectPersonCandidates(safe);
    for (const candidate of peopleInMessage) {
      previewState.people.add(candidate);
      previewState.threadPeople.get(title).add(candidate);
      previewState.personCounts.set(candidate, (previewState.personCounts.get(candidate) ?? 0) + 1);
      if (isSpecificHumanThreadTitle(title, conversation.source)) {
        previewState.relationships.add(formatMentionedRelationship(candidate, title));
      }
    }
    for (let i = 0; i < peopleInMessage.length; i += 1) {
      for (let j = i + 1; j < peopleInMessage.length; j += 1) {
        previewState.relationships.add(formatRelatedRelationship(peopleInMessage[i], peopleInMessage[j]));
      }
    }
    for (const emotion of collectEmotionCandidates(safe)) {
      previewState.emotions.add(emotion);
      previewState.threadEmotions.get(title).add(emotion);
    }
  }
}

function registerMigrationConversation(state, conversation, file, root) {
  const title = conversation.title || conversation.source || 'Untitled thread';
  state.conversations += 1;
  state.sourceCounts[conversation.source] = (state.sourceCounts[conversation.source] ?? 0) + 1;
  if (title) {
    state.threads.add(title);
  }
  state.scannedSessions.push({
    id: `${conversation.source || 'unknown'}:${slug(title)}:${state.scannedSessions.length + 1}`,
    source: conversation.source || 'unknown',
    title,
    message_count: Array.isArray(conversation.messages) ? conversation.messages.length : 0,
    evidence_ref: relativeEvidenceRef(file, root),
  });
  addMigrationSignals(state, conversation);
}

function migrationPreview(target) {
  const discovered = discoverMigrationFiles(target);
  const state = {
    people: new Set(),
    personCounts: new Map(),
    threads: new Set(),
    threadPeople: new Map(),
    threadEmotions: new Map(),
    memoryCounts: new Map(),
    emotions: new Set(),
    relationships: new Set(),
    scannedSessions: [],
    sourceCounts: { chatgpt: 0, claude: 0, codex: 0, 'claude-code': 0, unknown: 0 },
    conversations: 0,
    messages: 0,
    redactedFragments: 0,
  };

  for (const file of discovered.files) {
    if (file.toLowerCase().endsWith('.md')) {
      for (const conversation of extractMarkdownConversations(file, discovered.root)) {
        registerMigrationConversation(state, conversation, file, discovered.root);
      }
      continue;
    }
    if (file.toLowerCase().endsWith('.jsonl')) {
      for (const conversation of extractJSONLConversations(file)) {
        registerMigrationConversation(state, conversation, file, discovered.root);
      }
      continue;
    }
    let parsed;
    try {
      parsed = JSON.parse(readFileSync(file, 'utf8'));
    } catch {
      discovered.skipped.push({ file: basename(file), reason: 'invalid JSON' });
      continue;
    }
    for (const conversation of extractMigrationConversations(parsed, file)) {
      registerMigrationConversation(state, conversation, file, discovered.root);
    }
  }

  const promotedPeople = [...state.personCounts.entries()]
    .filter(([name, count]) => isLikelyPreviewPersonCandidate(name, count, state.messages))
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
  const reviewCandidates = [...state.personCounts.entries()]
    .filter(([name, count]) => !isLikelyPreviewPersonCandidate(name, count, state.messages) && shouldShowReviewCandidate(name, count, state.messages))
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .slice(0, 24)
    .map(([name]) => name);
  const peopleCandidates = promotedPeople.slice(0, 24).map(([name]) => name);
  const peopleSet = new Set(peopleCandidates);
  const funFacts = promotedPeople
    .slice(0, 12)
    .map(([name, count]) => `${name} appeared in ${count} bounded source snippet${count === 1 ? '' : 's'}`);
  const previewPeopleGroups = groupImportPeople(peopleCandidates);
  const relationships = uniqueLimited([...state.relationships]
    .map((candidate) => normalizeRelationshipForPreview(candidate, previewPeopleGroups.canonicalByName))
    .filter(Boolean)
    .filter((candidate) => relationshipAllowedForPreview(candidate, peopleSet, state.threads))
    , 24);

  return attachImportPreviewFlow({
    ok: true,
    source: dominantSource(state.sourceCounts),
    path: '<local archive>',
    archive_was_unpacked: discovered.archiveWasUnpacked,
    files_scanned: discovered.files.length,
    files_skipped: discovered.skipped,
    conversations: state.conversations,
    messages: state.messages,
    people_candidates: peopleCandidates,
    review_candidates: reviewCandidates,
    thread_candidates: [...state.threads].slice(0, 24),
    memory_candidates: [...state.memoryCounts.entries()]
      .slice(0, 24)
      .map(([title, count]) => `${title}: ${count} source snippet${count === 1 ? '' : 's'}`),
    emotion_candidates: [...state.emotions].slice(0, 24),
    relationship_candidates: relationships,
    fun_fact_candidates: funFacts,
    redacted_fragments: state.redactedFragments,
    raw_text_written: false,
    next: 'pulse migrate commit <preview.json> --confirm "import pulse graph"',
  }, state);
}

function markdownCell(value) {
  return safeText(String(value ?? '')
    .replace(/\\\|/g, '|')
    .replace(/\*\*/g, '')
    .replace(/\[[^\]]+\]\(([^)]+)\)/g, '$1')
    .trim(), 420);
}

function relativeEvidenceRef(file, rootDir) {
  const absoluteFile = resolve(file);
  const absoluteRoot = resolve(rootDir);
  if (!absoluteFile.startsWith(absoluteRoot)) {
    return basename(absoluteFile);
  }
  return absoluteFile.slice(absoluteRoot.length).replace(/^\/+/, '');
}

function peopleGraphIndexPath(target) {
  const path = resolve(target);
  if (!existsSync(path)) {
    throw new Error(`people graph path does not exist: ${path}`);
  }
  const stat = statSync(path);
  if (stat.isFile()) {
    return path;
  }
  const candidates = [
    join(path, 'people', 'INDEX.md'),
    join(path, 'INDEX.md'),
  ];
  const found = candidates.find((candidate) => existsSync(candidate) && statSync(candidate).isFile());
  if (!found) {
    throw new Error(`people graph index not found under ${path}; expected people/INDEX.md or INDEX.md`);
  }
  return found;
}

function readMarkdownSection(markdown, title) {
  const lines = String(markdown ?? '').split(/\r?\n/);
  const heading = title.toLowerCase();
  const out = [];
  let active = false;
  for (const line of lines) {
    const match = line.match(/^#{2,6}\s+(.+?)\s*$/);
    if (match) {
      if (active) break;
      active = match[1].trim().toLowerCase() === heading;
      continue;
    }
    if (active) {
      out.push(line);
    }
  }
  return safeText(out.join(' ').replace(/\s+/g, ' ').trim(), 900);
}

function markdownMeta(markdown, key) {
  const escaped = key.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = String(markdown ?? '').match(new RegExp(`^\\*\\*${escaped}:\\*\\*\\s*(.+)$`, 'im'));
  return safeText(match?.[1] ?? '', 240);
}

function markdownLinks(markdown) {
  const section = readMarkdownSection(markdown, 'Links');
  if (!section) {
    return [];
  }
  return section
    .split(/\s+-\s+/)
    .map((item) => humanReferenceLabel(item.replace(/^-+\s*/, '')))
    .filter(Boolean)
    .slice(0, 6);
}

function humanReferenceLabel(value) {
  const text = safeText(value, 220);
  if (!text) {
    return '';
  }
  if (/^source:memory\/contacts\/personal-contacts\.md$/i.test(text)) {
    return 'Personal contacts';
  }
  if (/^source:memory\/contacts\/freeman-team\.md$/i.test(text)) {
    return 'Freeman team';
  }
  if (/^source:memory\/contacts\/krisp-people-graph\.md$/i.test(text)) {
    return 'Krisp people graph';
  }
  if (/^source:memory\/anchors\/core-priority-entities\.md$/i.test(text)) {
    return 'Core priority entities';
  }
  if (/^source:memory\/graph\.jsonl$/i.test(text)) {
    return 'Legacy memory graph';
  }
  if (/^source:memory\//i.test(text)) {
    return '';
  }
  if (/^people\/.+\.md$/i.test(text)) {
    return 'Curated people profile';
  }
  return text;
}

function parsePeopleGraphRows(indexText) {
  const rows = [];
  for (const line of String(indexText ?? '').split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed.startsWith('|') || /^\|\s*-+\s*\|/.test(trimmed)) {
      continue;
    }
    const cells = trimmed
      .slice(1, trimmed.endsWith('|') ? -1 : undefined)
      .split('|')
      .map(markdownCell);
    if (cells.length < 5 || cells[0].toLowerCase() === 'name') {
      continue;
    }
    const closeness = Number.parseInt(cells[2], 10);
    rows.push({
      name: cells[0],
      role: cells[1],
      closeness: Number.isFinite(closeness) ? closeness : 1,
      relevance: cells[3],
      file: cells[4],
    });
  }
  return rows.filter((row) => row.name);
}

function peopleGraphPreview(target) {
  const indexPath = peopleGraphIndexPath(target);
  const peopleDir = dirname(indexPath);
  const graphRoot = basename(peopleDir) === 'people' ? dirname(peopleDir) : peopleDir;
  const rows = parsePeopleGraphRows(readFileSync(indexPath, 'utf8'));
  const profiles = rows.slice(0, 96).map((row) => {
    const profilePath = row.file ? join(peopleDir, row.file) : '';
    const markdown = profilePath && existsSync(profilePath) && statSync(profilePath).isFile()
      ? readFileSync(profilePath, 'utf8')
      : '';
    const summary = readMarkdownSection(markdown, 'Summary') || row.role;
    return {
      name: row.name,
      role: row.role,
      closeness: row.closeness,
      relevance: row.relevance,
      status: markdownMeta(markdown, 'Status'),
      handle: markdownMeta(markdown, 'Telegram'),
      summary,
      links: markdownLinks(markdown),
      evidence_ref: profilePath ? relativeEvidenceRef(profilePath, graphRoot) : relativeEvidenceRef(indexPath, graphRoot),
    };
  });
  const threadCandidates = uniqueLimited(profiles.map((profile) => profile.relevance).filter((item) => item && item !== 'life'), 24);
  const relationshipCandidates = [];
  for (const profile of profiles) {
    const relationships = readMarkdownSection(
      profile.evidence_ref ? readFileSync(join(graphRoot, profile.evidence_ref), 'utf8') : '',
      'Relationships',
    );
    for (const relationship of relationships.split(/\s+-\s+/).map((item) => safeText(item.replace(/^-+\s*/, ''), 220)).filter(Boolean)) {
      if (!isTechnicalRef(relationship) && !relationship.includes('source:memory/')) {
        relationshipCandidates.push(`${profile.name} <-> ${relationship}`);
      }
    }
  }
  return attachImportPreviewFlow({
    ok: true,
    source: 'people-graph',
    source_kind: 'curated_people_graph',
    source_priority: 100,
    path: '<local people graph>',
    archive_was_unpacked: false,
    files_scanned: 1 + profiles.filter((profile) => profile.evidence_ref !== relativeEvidenceRef(indexPath, graphRoot)).length,
    files_skipped: [],
    conversations: 0,
    messages: profiles.length,
    people_candidates: profiles.map((profile) => profile.name).slice(0, 48),
    person_profiles: profiles,
    review_candidates: [],
    thread_candidates: threadCandidates,
    memory_candidates: profiles
      .map((profile) => `${profile.name}: ${profile.summary || profile.role}`)
      .slice(0, 48),
    emotion_candidates: [],
    relationship_candidates: uniqueLimited(relationshipCandidates, 72),
    fun_fact_candidates: profiles
      .map((profile) => `${profile.name}: ${[
        profile.role,
        profile.status,
        profile.relevance ? `relevance ${profile.relevance}` : '',
        `closeness ${profile.closeness}/5`,
      ].filter(Boolean).join('; ')}`)
      .slice(0, 48),
    redacted_fragments: 0,
    raw_text_written: false,
    next: 'pulse migrate commit <preview.json> --confirm "import pulse graph"',
  }, {
    scannedSessions: profiles.map((profile, index) => ({
      id: `people-graph:${slug(profile.name)}:${index + 1}`,
      source: 'people-graph',
      title: profile.name,
      message_count: 1,
      evidence_ref: profile.evidence_ref || `people:${index + 1}`,
    })),
  });
}

function printMigrationPreview(preview) {
  const people = preview.people_candidates.length > 0
    ? preview.people_candidates.join(', ')
    : 'none yet';
  const threads = preview.thread_candidates.length > 0
    ? preview.thread_candidates.join(', ')
    : 'none yet';
  console.log(`[pulse] migration preview
─────────────────────────

source: ${preview.source}
files scanned: ${preview.files_scanned}
conversations: ${preview.conversations}
messages: ${preview.messages}
people candidates: ${people}
thread candidates: ${threads}
redacted fragments: ${preview.redacted_fragments}

privacy:
  raw text will not be written by preview.
  commit will require confirmation and will write structured graph deltas only.

next:
  ${preview.next}
`);
}

function htmlEscape(value) {
  return String(value ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

function previewList(items, emptyText = 'No candidates yet.') {
  const safeItems = Array.isArray(items) ? items.filter(Boolean) : [];
  if (safeItems.length === 0) {
    return `<p class="empty">${htmlEscape(emptyText)}</p>`;
  }
  return `<ul>${safeItems.map((item) => `<li class="filter-item" data-search="${htmlEscape(item)}">${htmlEscape(item)}</li>`).join('')}</ul>`;
}

function continuityPreviewText(value) {
  return String(value ?? '')
    .replace(/safe message signals?/gi, 'source snippets')
    .replace(/\bmemories\b/gi, 'saved memory')
    .trim();
}

function previewThreadTitle(value, fallback = 'Archive thread') {
  const text = continuityPreviewText(value);
  const title = text.split(':')[0]?.trim();
  return title || fallback;
}

function estimatePreviewTokenEconomy(preview) {
  const conversations = Number.isFinite(preview.conversations) ? preview.conversations : 0;
  const messages = Number.isFinite(preview.messages) ? preview.messages : 0;
  const estimatedRaw = Math.max(messages * 220, conversations * 900, 0);
  const resumeTokens = Math.min(2000, Math.max(
    240,
    preview.memory_candidates.length * 70 +
      preview.emotion_candidates.length * 40 +
      preview.relationship_candidates.length * 28 +
      preview.people_candidates.length * 16,
  ));
  return {
    estimatedRaw,
    resumeTokens,
    estimatedSaved: Math.max(0, estimatedRaw - resumeTokens),
  };
}

function isEmptyMigrationPreview(preview) {
  return (preview.conversations || 0) === 0 &&
    (preview.messages || 0) === 0 &&
    safeArrayLength(preview.people_candidates) === 0 &&
    safeArrayLength(preview.review_candidates) === 0 &&
    safeArrayLength(preview.thread_candidates) === 0 &&
    safeArrayLength(preview.memory_candidates) === 0 &&
    safeArrayLength(preview.emotion_candidates) === 0 &&
    safeArrayLength(preview.relationship_candidates) === 0;
}

function previewSourceScan(preview) {
  return {
    status: isEmptyMigrationPreview(preview) ? 'no_supported_conversations' : 'scanned',
    source: preview.source || 'unknown',
    files_scanned: Number.isFinite(preview.files_scanned) ? preview.files_scanned : 0,
    files_skipped: Array.isArray(preview.files_skipped) ? preview.files_skipped : [],
    sessions_scanned: Number.isFinite(preview.conversations) ? preview.conversations : 0,
    messages_scanned: Number.isFinite(preview.messages) ? preview.messages : 0,
    archive_was_unpacked: Boolean(preview.archive_was_unpacked),
    raw_text_written: preview.raw_text_written === true,
  };
}

function defaultScannedSessions(preview) {
  const existing = Array.isArray(preview.scanned_sessions) ? preview.scanned_sessions : [];
  if (existing.length > 0) {
    return existing.map((session, index) => ({
      id: safeText(session.id, 120) || `${preview.source || 'source'}:session:${index + 1}`,
      source: safeText(session.source, 80) || preview.source || 'unknown',
      title: safeText(session.title, 160) || `${hostDisplayName(preview.source)} session`,
      message_count: Number.isFinite(session.message_count) ? session.message_count : 0,
      evidence_ref: safeText(session.evidence_ref, 220) || `source:${preview.source || 'unknown'}:${index + 1}`,
    }));
  }

  const titles = uniqueLimited([
    ...previewArray(preview, 'thread_candidates'),
    ...previewArray(preview, 'memory_candidates').map(previewThreadTitle),
  ], 24);
  const count = Math.max(1, titles.length);
  const messagesPerSession = Math.max(0, Math.ceil((preview.messages || 0) / count));
  return titles.map((title, index) => ({
    id: `${preview.source || 'source'}:${slug(title)}:${index + 1}`,
    source: preview.source || 'unknown',
    title,
    message_count: messagesPerSession,
    evidence_ref: `source:${preview.source || 'unknown'}:${index + 1}`,
  }));
}

function threadPeopleFromState(state) {
  const out = {};
  if (!state?.threadPeople) {
    return out;
  }
  for (const [title, people] of state.threadPeople.entries()) {
    out[title] = [...people].filter((name) => !isReviewEntityName(name)).slice(0, 12);
  }
  return out;
}

function threadEmotionsFromState(state) {
  const out = {};
  if (!state?.threadEmotions) {
    return out;
  }
  for (const [title, emotions] of state.threadEmotions.entries()) {
    out[title] = [...emotions].slice(0, 8);
  }
  return out;
}

function relationshipsForThread(relationships, title) {
  return relationships
    .filter((item) => {
      const parsed = parseRelationshipCandidate(item);
      return parsed?.right === title || parsed?.left === title;
    })
    .map(continuityPreviewText)
    .slice(0, 6);
}

function tokenEconomyForThread(messageCount, thread) {
  const estimatedRawTokens = Math.max(messageCount * 220, 220);
  const resumeTokens = Math.min(2000, Math.max(
    240,
    safeArrayLength(thread.decisions) * 70 +
      safeArrayLength(thread.open_loops) * 36 +
      safeArrayLength(thread.do_not_repeat) * 34 +
      safeArrayLength(thread.emotional_anchors) * 40 +
      safeArrayLength(thread.people_found) * 18,
  ));
  return {
    estimated_raw_tokens: estimatedRawTokens,
    resume_tokens: resumeTokens,
    estimated_saved_tokens: Math.max(0, estimatedRawTokens - resumeTokens),
  };
}

function buildPulseInsights(preview) {
  const threads = Array.isArray(preview.candidate_threads) ? preview.candidate_threads : [];
  return threads
    .slice(0, 6)
    .map((thread) => {
      const decisions = safeArrayLength(thread.decisions);
      const openLoops = safeArrayLength(thread.open_loops);
      const people = safeArrayLength(thread.people_found);
      const firstDecision = safeText((thread.decisions ?? [])[0], 150);
      const firstOpenLoop = safeText((thread.open_loops ?? [])[0], 150);
      const firstPerson = safeText((thread.people_found ?? [])[0], 90);
      const threadTitle = safeText(thread.title, 160);
      const saved = Number.isFinite(thread.token_economy?.estimated_saved_tokens)
        ? thread.token_economy.estimated_saved_tokens
        : 0;
      const reasons = [
        firstDecision ? `Decision candidate: ${firstDecision}` : '',
        firstPerson ? `${firstPerson} appears as a related person in ${threadTitle}.` : '',
        people > 0 ? `${people} related ${people === 1 ? 'person' : 'people'} found may make this thread easier to resume.` : '',
        firstOpenLoop ? `Open loop candidate: ${firstOpenLoop}` : '',
        decisions > 0 ? `${decisions} candidate decision${decisions === 1 ? '' : 's'} can become resume context.` : '',
        openLoops > 0 ? `${openLoops} open loop${openLoops === 1 ? '' : 's'} may need follow-up.` : '',
        saved > 0 ? `Approx. ${saved} tokens can be avoided by importing the structured resume instead of raw context.` : '',
      ].filter(Boolean);
      if (reasons.length === 0) {
        return null;
      }
      return {
        kind: 'why_this_matters_now',
        thread_title: threadTitle,
        title: `Why this may matter now: ${safeText(thread.title, 140)}`,
        summary: `Pulse found structured continuity in ${threadTitle}. Treat this as a private preview hypothesis: review it before import so the next Claude or Codex session can continue from a smaller, safer context block.`,
        reasons: reasons.slice(0, 4),
        suggested_next_step: 'Review this thread before import, then decide whether to make it an active Pulse thread.',
        related_entities: uniqueLimited([...(thread.people_found ?? []), thread.title].filter(Boolean), 8),
        privacy_tier: 'private',
        confidence: 0.6,
      };
    })
    .filter(Boolean);
}

function buildCandidateThreads(preview, scannedSessions) {
  const memoryItems = previewArray(preview, 'memory_candidates');
  const titles = uniqueLimited([
    ...previewArray(preview, 'thread_candidates'),
    ...memoryItems.map(previewThreadTitle),
    ...scannedSessions.map((session) => session.title),
  ], 24);
  const visibleRelationships = previewArray(preview, 'relationship_candidates')
    .filter((item) => !relationshipTouchesReviewEntity(item));
  const threadPeople = preview.thread_people && typeof preview.thread_people === 'object' ? preview.thread_people : {};
  const threadEmotions = preview.thread_emotions && typeof preview.thread_emotions === 'object' ? preview.thread_emotions : {};
  const reviewItems = previewArray(preview, 'review_candidates').slice(0, 8);

  return titles.map((title, index) => {
    const sessions = scannedSessions.filter((session) => session.title === title);
    const messageCount = sessions.reduce((sum, session) => sum + (session.message_count || 0), 0);
    const decisions = memoryItems
      .filter((item) => previewThreadTitle(item) === title)
      .map(continuityPreviewText);
    const thread = {
      thread_id: `thread:${slug(title)}:${index + 1}`,
      title,
      sources: uniqueLimited((sessions.length > 0 ? sessions : scannedSessions).map((session) => session.source), 8),
      source_sessions: sessions.map((session) => session.id),
      decisions: decisions.length > 0 ? decisions : [`${title}: ${messageCount || preview.messages || 0} source snippets`],
      open_loops: relationshipsForThread(visibleRelationships, title),
      do_not_repeat: ['raw_text_import_disabled'],
      emotional_anchors: uniqueLimited(threadEmotions[title] ?? previewArray(preview, 'emotion_candidates'), 6),
      people_found: uniqueLimited(threadPeople[title] ?? previewArray(preview, 'people_candidates'), 12)
        .filter((name) => !isReviewEntityName(name)),
      review_items: reviewItems,
      privacy_tier: 'private',
    };
    thread.token_economy = tokenEconomyForThread(messageCount, thread);
    return thread;
  });
}

function importGateForPreview(preview) {
  return {
    requires_confirmation: 'import pulse graph',
    default_privacy: 'private',
    will_save: [
      'candidate_threads',
      'decisions',
      'open_loops',
      'do_not_repeat_warnings',
      'confirmed_emotional_anchors',
    ],
    will_not_save: [
      'raw_text',
      'local_paths',
      'secrets_or_tokens',
      'unreviewed_ambiguous_people',
    ],
    raw_text_written: preview.raw_text_written === true,
  };
}

function attachImportPreviewFlow(preview, state = {}) {
  const withState = {
    ...preview,
    thread_people: preview.thread_people ?? threadPeopleFromState(state),
    thread_emotions: preview.thread_emotions ?? threadEmotionsFromState(state),
  };
  const scannedSessions = defaultScannedSessions({
    ...withState,
    scanned_sessions: state.scannedSessions ?? withState.scanned_sessions,
  });
  const staged = {
    ...withState,
    flow: 'pulse.import_preview.v1',
    source_scan: previewSourceScan(withState),
    scanned_sessions: scannedSessions,
  };
  const candidateThreads = buildCandidateThreads(staged, scannedSessions);
  const withThreads = {
    ...staged,
    candidate_threads: candidateThreads,
    import_gate: importGateForPreview(staged),
  };
  return {
    ...withThreads,
    pulse_insights: Array.isArray(preview.pulse_insights) && preview.pulse_insights.length > 0
      ? preview.pulse_insights
      : buildPulseInsights(withThreads),
  };
}

function overrideImportPreviewSource(preview, source) {
  if (!source) {
    return preview;
  }
  const updated = {
    ...preview,
    source,
    source_scan: preview.source_scan ? { ...preview.source_scan, source } : undefined,
    scanned_sessions: Array.isArray(preview.scanned_sessions)
      ? preview.scanned_sessions.map((session) => ({
        ...session,
        source: session.source === 'unknown' || session.source === 'mixed' ? source : session.source,
      }))
      : preview.scanned_sessions,
  };
  return attachImportPreviewFlow(updated);
}

function demoMigrationThread() {
  return {
    title: 'Thread: Pulse MCP preview',
    decisions: ['Atlas must not own the People Graph.'],
    openLoops: ['run Claude Code E2E without API retries blocking tool use.'],
    doNotRepeat: ['do not claim production readiness.'],
    emotionalAnchors: ['Trust comes from showing the exact resume block before injection.'],
  };
}

function renderEmptyPreviewNotice() {
  const demo = demoMigrationThread();
  return `<section class="trust" id="empty-preview-demo">
    <h2>No real data yet</h2>
    <p>This source did not contain a supported chat export, so Pulse has nothing real to import from it. Here is how a real thread looks when a Claude, Codex, ChatGPT, or Claude Code source is detected.</p>
    <div class="gate-step">
      <h3>Here is how a real thread looks</h3>
      <p><strong>${htmlEscape(demo.title)}</strong></p>
      <ul>
        <li>Decision: ${htmlEscape(demo.decisions[0])}</li>
        <li>Open loop: ${htmlEscape(demo.openLoops[0])}</li>
        <li>Do-not-repeat: ${htmlEscape(demo.doNotRepeat[0])}</li>
        <li>Emotional anchor: ${htmlEscape(demo.emotionalAnchors[0])}</li>
      </ul>
      <p class="empty">Demo only; not imported. Choose a real archive or local session source to save structured continuity.</p>
    </div>
  </section>`;
}

function renderPreviewThreadCards(preview) {
  if (isEmptyMigrationPreview(preview)) {
    const demo = demoMigrationThread();
    return `<div class="profile-grid"><article class="person-card filter-item" data-search="${htmlEscape(`${demo.title} ${demo.decisions.join(' ')} ${demo.openLoops.join(' ')} ${demo.doNotRepeat.join(' ')}`)}">
      <div class="person-head">
        <h3>${htmlEscape(demo.title)}</h3>
        <span>demo only</span>
      </div>
      <p class="empty">Demo only; not imported.</p>
      <div class="mini-block"><h4>Decisions</h4>${previewList(demo.decisions)}</div>
      <div class="mini-block"><h4>Open loops</h4>${previewList(demo.openLoops)}</div>
      <div class="mini-block"><h4>Do-not-repeat</h4>${previewList(demo.doNotRepeat)}</div>
      <div class="mini-block"><h4>Emotional anchors</h4>${previewList(demo.emotionalAnchors)}</div>
    </article></div>`;
  }
  const candidateThreads = Array.isArray(preview.candidate_threads) ? preview.candidate_threads.slice(0, 4) : [];
  if (candidateThreads.length > 0) {
    return `<div class="profile-grid">${candidateThreads.map((thread) => `<article class="person-card filter-item" data-search="${htmlEscape([
      thread.title,
      ...(thread.decisions ?? []),
      ...(thread.open_loops ?? []),
      ...(thread.do_not_repeat ?? []),
      ...(thread.emotional_anchors ?? []),
      ...(thread.people_found ?? []),
    ].join(' '))}">
      <div class="person-head">
        <h3>${htmlEscape(thread.title)}</h3>
        <span>${htmlEscape(thread.privacy_tier || 'private')} preview</span>
      </div>
      <div class="mini-block"><h4>Decisions</h4>${previewList((thread.decisions ?? []).slice(0, 3), 'No saved decisions yet.')}</div>
      <div class="mini-block"><h4>Open loops</h4>${previewList((thread.open_loops ?? []).slice(0, 3), 'No open loops detected yet.')}</div>
      <div class="mini-block"><h4>Do-not-repeat</h4>${previewList((thread.do_not_repeat ?? []).slice(0, 3).map(humanGateItem), 'No do-not-repeat warnings yet.')}</div>
      <div class="mini-block"><h4>Emotional anchors</h4>${previewList((thread.emotional_anchors ?? []).slice(0, 3), 'No emotional anchors yet.')}</div>
    </article>`).join('')}</div>`;
  }
  const memoryItems = Array.isArray(preview.memory_candidates) ? preview.memory_candidates : [];
  const threads = memoryItems.length > 0 ? memoryItems.slice(0, 4) : [`${preview.source || 'Archive'} thread: ${preview.messages || 0} source snippets`];
  return `<div class="profile-grid">${threads.map((item) => {
    const title = previewThreadTitle(item, `${preview.source || 'Archive'} thread`);
    const decisions = Math.max(1, preview.memory_candidates.length);
    const openLoops = preview.relationship_candidates.length > 0 ? 1 : 0;
    const anchors = preview.emotion_candidates.length;
    return `<article class="person-card filter-item" data-search="${htmlEscape(`${title} ${continuityPreviewText(item)}`)}">
      <div class="person-head">
        <h3>${htmlEscape(title)}</h3>
        <span>private preview</span>
      </div>
      <div class="mini-block"><h4>Decisions</h4>${previewList(preview.memory_candidates.map(continuityPreviewText).slice(0, 3), 'No saved decisions yet.')}</div>
      <div class="mini-block"><h4>Open loops</h4>${previewList(openLoops ? ['Review this source before importing it.'] : [], 'No open loops detected yet.')}</div>
      <div class="mini-block"><h4>Do-not-repeat</h4>${previewList(['Do not import raw transcript text.'])}</div>
      <div class="mini-block"><h4>Emotional anchors</h4>${previewList(preview.emotion_candidates.slice(0, 3), anchors ? 'No emotional anchors yet.' : 'No emotional anchors yet.')}</div>
    </article>`;
  }).join('')}</div>`;
}

function renderPreviewTimeline(preview) {
  const title = previewThreadTitle(preview.memory_candidates[0], preview.source || 'Archive');
  const rows = [
    `${hostDisplayName(preview.source)} source scanned: ${preview.conversations || 0} conversations`,
    `${title}: ${preview.memory_candidates.length || 0} candidate continuity items`,
    `Review gate: ${preview.review_candidates?.length || 0} items need a human decision`,
  ];
  return previewList(rows.map(continuityPreviewText));
}

function renderSourceScanFlow(preview) {
  const scan = preview.source_scan ?? previewSourceScan(preview);
  const skipped = Array.isArray(scan.files_skipped) ? scan.files_skipped : [];
  return `<section id="sources-scanned" class="flow-section">
    <h2>Sources scanned</h2>
    <p class="subhead">Pulse scans local/exported source files first and writes only bounded preview metadata. Raw transcript text stays out of the preview.</p>
    <div class="source-flow-grid">
      <article class="flow-card"><span>Status</span><strong>${htmlEscape(scan.status)}</strong></article>
      <article class="flow-card"><span>Source</span><strong>${htmlEscape(scan.source)}</strong></article>
      <article class="flow-card"><span>Files scanned</span><strong>${htmlEscape(scan.files_scanned)}</strong></article>
      <article class="flow-card"><span>Sessions scanned</span><strong>${htmlEscape(scan.sessions_scanned)}</strong></article>
      <article class="flow-card"><span>Messages scanned</span><strong>${htmlEscape(scan.messages_scanned)}</strong></article>
      <article class="flow-card"><span>Raw text written</span><strong>${scan.raw_text_written ? 'yes' : 'no'}</strong></article>
    </div>
    ${skipped.length > 0 ? `<p class="empty section-note">${htmlEscape(skipped.length)} files skipped by preview safety limits.</p>` : ''}
  </section>`;
}

function renderScannedSessionsFlow(preview) {
  const sessions = defaultScannedSessions(preview);
  if (sessions.length === 0) {
    return `<section id="scanned-sessions" class="flow-section">
      <h2>Scanned sessions</h2>
      <p class="empty">No supported chat sessions were found in this source yet.</p>
    </section>`;
  }
  return `<section id="scanned-sessions" class="flow-section">
    <h2>Scanned sessions</h2>
    <p class="subhead">Session rows show only source, title, count, and a local evidence reference. No raw messages are shown here.</p>
    <div class="session-grid">
      ${sessions.slice(0, 12).map((session) => {
        const evidence = humanReferenceLabel(session.evidence_ref) || 'Local source';
        return `<article class="session-card filter-item" data-search="${htmlEscape(`${session.source} ${session.title} ${evidence}`)}">
        <span>${htmlEscape(session.source)}</span>
        <h3>${htmlEscape(session.title)}</h3>
        <p>${htmlEscape(session.message_count)} source snippets · ${htmlEscape(evidence)}</p>
      </article>`;
      }).join('')}
    </div>
  </section>`;
}

function renderCandidateThreadsFlow(preview) {
  const threads = Array.isArray(preview.candidate_threads) ? preview.candidate_threads : buildCandidateThreads(preview, defaultScannedSessions(preview));
  if (threads.length === 0) {
    return `<section id="candidate-threads" class="flow-section">
      <h2>Candidate threads</h2>
      <p class="empty">No candidate threads yet. Choose a supported archive or local history source and preview again.</p>
    </section>`;
  }
  return `<section id="candidate-threads" class="flow-section">
    <h2>Candidate threads</h2>
    <p class="subhead">Pulse groups source sessions into thread-sized continuity candidates before showing people or graph details.</p>
    <div class="thread-flow-grid">
      ${threads.slice(0, 8).map((thread) => `<article class="thread-flow-card filter-item" data-search="${htmlEscape([
        thread.title,
        ...(thread.decisions ?? []),
        ...(thread.open_loops ?? []),
        ...(thread.do_not_repeat ?? []),
        ...(thread.emotional_anchors ?? []),
        ...(thread.people_found ?? []),
      ].join(' '))}">
        <div class="thread-flow-head">
          <h3>${htmlEscape(thread.title)}</h3>
          <span>${htmlEscape(thread.privacy_tier || 'private')}</span>
        </div>
        <div class="thread-flow-metrics">
          <span>${htmlEscape(safeArrayLength(thread.decisions))} decisions</span>
          <span>${htmlEscape(safeArrayLength(thread.open_loops))} open loops</span>
          <span>${htmlEscape(safeArrayLength(thread.do_not_repeat))} do-not-repeat</span>
          <span>${htmlEscape(thread.token_economy?.resume_tokens ?? 0)} resume tokens</span>
        </div>
        <div class="mini-block"><h4>Decisions</h4>${previewList((thread.decisions ?? []).slice(0, 3), 'No decisions detected yet.')}</div>
        <div class="mini-block"><h4>Open loops</h4>${previewList((thread.open_loops ?? []).slice(0, 3), 'No open loops detected yet.')}</div>
        <div class="mini-block"><h4>Do-not-repeat</h4>${previewList((thread.do_not_repeat ?? []).slice(0, 3).map(humanGateItem))}</div>
      </article>`).join('')}
    </div>
  </section>`;
}

function renderPulseInsightsFlow(preview) {
  const insights = Array.isArray(preview.pulse_insights) ? preview.pulse_insights.slice(0, 6) : [];
  if (insights.length === 0) {
    return `<section id="pulse-insights" class="flow-section">
      <h2>Why this may matter now</h2>
      <p class="empty">Pulse will show a short private preview hypothesis here after it finds a thread with decisions, open loops, people, or token savings.</p>
    </section>`;
  }
  return `<section id="pulse-insights" class="flow-section insight-section">
    <h2>Why this may matter now</h2>
    <p class="subhead">Pulse insight turns the graph into a review action. It is a private preview hypothesis, not an automatic claim.</p>
    <div class="insight-grid">
      ${insights.map((insight) => {
        const activeThreadTitle = safeText(insight.thread_title || insight.title || 'Pulse insight', 160);
        return `<article class="insight-card filter-item" data-search="${htmlEscape([
        insight.title,
        insight.summary,
        ...(insight.reasons ?? []),
        insight.suggested_next_step,
        ...(insight.related_entities ?? []),
      ].join(' '))}">
        <span>Pulse insight</span>
        <h3>${htmlEscape(insight.title || 'Why this may matter now')}</h3>
        <p>${htmlEscape(insight.summary || 'Pulse found continuity worth reviewing before import.')}</p>
        <div class="mini-block"><h4>Because</h4>${previewList((insight.reasons ?? []).slice(0, 4))}</div>
        <div class="mini-block"><h4>Next</h4>${previewList([insight.suggested_next_step || 'Review this thread before import.'])}</div>
        <p class="review-status" data-active-thread-status>Not active yet. Mark active only if this thread should appear in the next resume.</p>
        <div class="decision-actions">
          <button type="button" data-active-thread="${htmlEscape(activeThreadTitle)}" data-active-reason="User marked the Pulse insight as active during review.">Make active</button>
        </div>
      </article>`;
      }).join('')}
    </div>
  </section>`;
}

function reviewActionCandidates(preview) {
  const fromThreads = Array.isArray(preview.candidate_threads)
    ? preview.candidate_threads.flatMap((thread) => thread.review_items ?? [])
    : [];
  return uniqueLimited([
    ...fromThreads,
    ...previewArray(preview, 'review_candidates'),
  ], 12);
}

function renderReviewActionsFlow(preview) {
  const candidates = reviewActionCandidates(preview);
  if (candidates.length === 0) {
    return `<section id="review-actions" class="flow-section">
      <h2>Review actions</h2>
      <p class="empty">No ambiguous entities need a decision in this preview.</p>
      <p id="review-counter" class="review-counter" data-review-total="0">reviewed 0 of 0 · No reviewed JSON needed.</p>
    </section>`;
  }
  return `<section id="review-actions" class="flow-section">
    <h2>Review actions</h2>
    <p class="subhead">Resolve ambiguous models, tools, projects, or people before import. These buttons update a downloadable reviewed JSON file. Import still waits for you to download and commit that reviewed file.</p>
    <p id="review-counter" class="review-counter" data-review-total="${htmlEscape(candidates.length)}">reviewed 0 of ${htmlEscape(candidates.length)} · Download reviewed JSON before import.</p>
    <div class="decision-grid">
      ${candidates.slice(0, 8).map((item, index) => `<article class="decision-card filter-item" data-review-card data-review-item="${htmlEscape(item)}" data-search="${htmlEscape(item)}">
        <h3>Review: ${htmlEscape(item)}</h3>
        <p>Suggested action: decide whether this is a person, project/component, private context, or noise.</p>
        <p class="review-status" data-review-status>Needs your decision. Download reviewed JSON before import.</p>
        <div class="decision-actions">
          <button type="button" data-review-action="confirm" data-review-result="confirmed" data-review-kind="project">${index === 0 ? 'Confirm' : 'Confirm'}</button>
          <button type="button" data-review-action="edit" data-review-result="edit needed">Edit</button>
          <button type="button" data-review-action="ignore" data-review-result="ignored">Ignore</button>
          <button type="button" data-review-action="private" data-review-result="marked private" data-review-kind="project">Mark private</button>
        </div>
      </article>`).join('')}
    </div>
    <div class="review-export">
      <h3>Reviewed JSON</h3>
      <p>Download <code>pulse-preview.reviewed.json</code>, then run the reviewed import command in the import gate.</p>
      <button type="button" id="download-reviewed-json">Download reviewed JSON</button>
      <p id="review-download-status" class="review-status">review_decisions will be written into pulse-preview.reviewed.json.</p>
    </div>
  </section>`;
}

function renderPreviewPersonCards(preview) {
  const people = Array.isArray(preview.people_candidates)
    ? preview.people_candidates.filter(Boolean).slice(0, 12)
    : [];
  if (people.length === 0) {
    return '<p class="empty">No people found in this source yet.</p>';
  }
  const relationships = Array.isArray(preview.relationship_candidates) ? preview.relationship_candidates : [];
  const funFacts = Array.isArray(preview.fun_fact_candidates) ? preview.fun_fact_candidates : [];
  const memories = Array.isArray(preview.memory_candidates) ? preview.memory_candidates : [];
  const profiles = Array.isArray(preview.person_profiles) ? preview.person_profiles : [];
  const profileByName = new Map(profiles.map((profile) => [profile.name, profile]));
  return `<div class="profile-grid">${people.map((person) => {
    const profile = profileByName.get(person);
    const relatedThreads = relationships
      .filter((item) => item.startsWith(`${person} -> `))
      .map((item) => item.slice(`${person} -> `.length))
      .filter((item) => !isReviewEntityName(item))
      .slice(0, 4);
    const personRelationships = relationships
      .filter((item) => item.includes(person) && !item.startsWith(`${person} -> `) && !relationshipTouchesReviewEntity(item))
      .slice(0, 4);
    const personFacts = funFacts
      .filter((item) => item.startsWith(person))
      .slice(0, 3);
    const mentionText = personFacts[0]?.match(/appeared in (\d+) (?:safe preview signal|bounded source snippet|preview source snippet)/)?.[1] ?? '1';
    const profileFacts = profile ? [
      profile.role ? `Role: ${profile.role}` : '',
      profile.status ? `Status: ${profile.status}` : '',
      profile.relevance ? `Relevant: ${profile.relevance}` : '',
      Number.isFinite(profile.closeness) ? `Closeness: ${profile.closeness}/5` : '',
      profile.evidence_ref ? `Source: ${humanReferenceLabel(profile.evidence_ref) || 'Curated people graph'}` : '',
    ].filter(Boolean) : [];
    const searchText = [
      person,
      profile?.role,
      profile?.status,
      profile?.relevance,
      profile?.summary,
      ...personFacts,
      ...relatedThreads,
      ...personRelationships,
    ].join(' ');
    return `<article class="person-card filter-item" data-search="${htmlEscape(searchText)}">
      <div class="person-head">
        <h3>${htmlEscape(person)}</h3>
        <span>${profile ? 'curated' : `${htmlEscape(mentionText)} mentions`}</span>
      </div>
      ${profile?.summary ? `<p class="empty">${htmlEscape(profile.summary)}</p>` : ''}
      <div class="mini-block">
        <h4>${profile ? 'Profile' : 'Evidence'}</h4>
        ${previewList(profileFacts.length > 0 ? profileFacts : (personFacts.length > 0 ? personFacts : [`${person} appeared in ${mentionText} bounded source snippet${mentionText === '1' ? '' : 's'}`]))}
      </div>
      <div class="mini-block">
        <h4>${profile ? 'Context' : 'Related continuity'}</h4>
        ${previewList(profile?.links?.length > 0 ? profile.links : (relatedThreads.length > 0 ? relatedThreads : memories.map(continuityPreviewText).slice(0, 3)))}
      </div>
      <div class="mini-block">
        <h4>Relationships</h4>
        ${previewList(personRelationships)}
      </div>
    </article>`;
  }).join('')}</div>`;
}

function renderCopyCommand(command) {
  if (!command) {
    return '';
  }
  return `<div class="command"><code>${htmlEscape(command)}</code><button class="copy" data-copy="${htmlEscape(command)}">Copy command</button></div>`;
}

function reviewedCommitCommand(preview) {
  const openFlag = typeof preview.commit_command === 'string' && preview.commit_command.includes(' --open') ? ' --open' : '';
  return `pulse migrate commit pulse-preview.reviewed.json --confirm "import pulse graph"${openFlag}`;
}

function viewerCommandForPreview(preview) {
  const source = safeText(preview.viewer_command, 600);
  if (!source) {
    return '';
  }
  return source
    .replace(/\s+--data-dir\s+(?:"[^"]+"|'[^']+'|\S+)/g, '')
    .replace(/\s+--base\s+http:\/\/127\.0\.0\.1:\d+/g, ' --base http://127.0.0.1:<pulse-port>');
}

function reviewedPreviewPayload(preview) {
  return {
    ok: preview.ok,
    source: preview.source,
    source_kind: preview.source_kind,
    conversations: preview.conversations,
    messages: preview.messages,
    people_candidates: previewArray(preview, 'people_candidates'),
    review_candidates: previewArray(preview, 'review_candidates'),
    thread_candidates: previewArray(preview, 'thread_candidates'),
    memory_candidates: previewArray(preview, 'memory_candidates'),
    emotion_candidates: previewArray(preview, 'emotion_candidates'),
    relationship_candidates: previewArray(preview, 'relationship_candidates'),
    fun_fact_candidates: previewArray(preview, 'fun_fact_candidates'),
    candidate_threads: Array.isArray(preview.candidate_threads) ? preview.candidate_threads.map((thread) => ({
      title: safeText(thread.title, 160),
      decisions: Array.isArray(thread.decisions) ? thread.decisions.map((item) => safeText(item, 240)).filter(Boolean) : [],
      open_loops: Array.isArray(thread.open_loops) ? thread.open_loops.map((item) => safeText(item, 240)).filter(Boolean) : [],
      do_not_repeat: Array.isArray(thread.do_not_repeat) ? thread.do_not_repeat.map((item) => safeText(item, 240)).filter(Boolean) : [],
      emotional_anchors: Array.isArray(thread.emotional_anchors) ? thread.emotional_anchors.map((item) => safeText(item, 240)).filter(Boolean) : [],
      review_items: Array.isArray(thread.review_items) ? thread.review_items.map((item) => safeText(item, 120)).filter(Boolean) : [],
      privacy_tier: safeText(thread.privacy_tier, 40) || 'private',
    })) : [],
    pulse_insights: Array.isArray(preview.pulse_insights) ? preview.pulse_insights.map((insight) => ({
      kind: safeText(insight.kind, 80) || 'why_this_matters_now',
      thread_title: safeText(insight.thread_title, 160),
      title: safeText(insight.title, 180),
      summary: safeText(insight.summary, 360),
      reasons: Array.isArray(insight.reasons) ? insight.reasons.map((item) => safeText(item, 220)).filter(Boolean) : [],
      suggested_next_step: safeText(insight.suggested_next_step, 220),
      related_entities: Array.isArray(insight.related_entities) ? insight.related_entities.map((item) => safeText(item, 120)).filter(Boolean) : [],
      privacy_tier: safeText(insight.privacy_tier, 40) || 'private',
      confidence: Number.isFinite(insight.confidence) ? Math.max(0, Math.min(1, insight.confidence)) : 0.6,
    })).filter((insight) => insight.title || insight.summary) : [],
    active_threads: Array.isArray(preview.active_threads) ? preview.active_threads.map((thread) => {
      if (typeof thread === 'string') {
        return { thread_title: safeText(thread, 160), source: 'pulse_review' };
      }
      return {
        thread_title: safeText(thread?.thread_title ?? thread?.title, 160),
        reason: safeText(thread?.reason, 240),
        source: safeText(thread?.source, 80) || 'pulse_review',
      };
    }).filter((thread) => thread.thread_title) : [],
    raw_text_written: preview.raw_text_written,
    review_decisions: preview.review_decisions && typeof preview.review_decisions === 'object' && !Array.isArray(preview.review_decisions)
      ? preview.review_decisions
      : {},
  };
}

function humanGateItem(item) {
  const labels = {
    candidate_threads: 'candidate threads',
    open_loops: 'open loops',
    do_not_repeat_warnings: 'do-not-repeat warnings',
    confirmed_emotional_anchors: 'confirmed emotional anchors',
    raw_text: 'raw transcript',
    raw_text_import_disabled: 'Do not import raw transcript text.',
    local_paths: 'local paths',
    secrets_or_tokens: 'secrets or tokens',
    unreviewed_ambiguous_people: 'unreviewed ambiguous people',
  };
  return labels[item] ?? String(item ?? '').replace(/_/g, ' ');
}

function renderPreviewImportGate(preview) {
  const gate = preview.import_gate ?? importGateForPreview(preview);
  const willSave = Array.isArray(gate.will_save) ? gate.will_save.map(humanGateItem) : [];
  const willNotSave = Array.isArray(gate.will_not_save) ? gate.will_not_save.map(humanGateItem) : [];
  const hasReview = reviewActionCandidates(preview).length > 0;
  const commitCommand = hasReview ? reviewedCommitCommand(preview) : preview.commit_command;
  if (isEmptyMigrationPreview(preview)) {
    return `<section class="trust">
      <h2>Import gate</h2>
      <p>No real source items were found yet, so there is nothing to import from this preview. The demo thread above is only an example and will not be written into Pulse.</p>
      <div class="gate-summary-grid">
        <article class="gate-summary"><h3>Will save</h3>${previewList([], 'Nothing yet.')}</article>
        <article class="gate-summary"><h3>Will not save</h3>${previewList(willNotSave)}</article>
      </div>
      <div class="gate-step">
        <h3>Next</h3>
        <p>Choose a supported ChatGPT/Claude archive or local Codex/Claude Code history folder, then preview again before importing.</p>
      </div>
    </section>`;
  }
  if (!preview.commit_command) {
    return `<section class="trust">
      <h2>Import gate</h2>
      <p>Raw chat text was not written. This file shows bounded candidates only. Nothing is imported until you create a JSON preview and run the exact confirmation phrase.</p>
      <div class="gate-summary-grid">
        <article class="gate-summary"><h3>Will save</h3>${previewList(willSave)}</article>
        <article class="gate-summary"><h3>Will not save</h3>${previewList(willNotSave)}</article>
      </div>
      <div class="gate-step">
        <h3>Make this importable</h3>
        <p>${hasReview ? 'Download reviewed JSON above, or re-run preview with <code>--out pulse-preview.json</code> to make a copyable import command.' : 'Re-run preview with <code>--out pulse-preview.json</code> to make a copyable import command.'}</p>
        ${hasReview ? renderCopyCommand(commitCommand) : ''}
      </div>
    </section>`;
  }
  return `<section class="trust">
      <h2>Import gate</h2>
      <p>Raw chat text was not written. This file shows bounded candidates only. Nothing is imported until you run the command with the exact confirmation phrase.</p>
      <div class="gate-summary-grid">
        <article class="gate-summary"><h3>Will save</h3>${previewList(willSave)}</article>
        <article class="gate-summary"><h3>Will not save</h3>${previewList(willNotSave)}</article>
      </div>
      ${hasReview ? `<div class="gate-step">
        <h3>1. Download reviewed JSON</h3>
        <p>Review actions write decisions into <code>pulse-preview.reviewed.json</code>. The import command below uses that reviewed file.</p>
      </div>` : ''}
      <div class="gate-step">
        <h3>${hasReview ? '2' : '1'}. Import structured continuity and open viewer</h3>
        <p>This writes threads, decisions, open loops, do-not-repeat, and emotional anchors into Pulse, then opens the memory viewer.</p>
        ${renderCopyCommand(commitCommand)}
      </div>
      <div class="gate-step">
        <h3>${hasReview ? '3' : '2'}. Open the memory viewer again later</h3>
        <p>This shows resume context, saved decisions, open loops, and review decisions before the next session.</p>
        ${renderCopyCommand(viewerCommandForPreview(preview))}
      </div>
    </section>`;
}

function renderMigrationPreviewHTML(preview) {
  const generatedAt = new Date().toISOString();
  const isPeopleGraph = preview.source_kind === 'curated_people_graph';
  const previewTitle = isPeopleGraph ? 'Real people graph' : 'Pulse Import Preview';
  const previewSubtitle = isPeopleGraph
    ? 'Curated people from an existing local graph. Pulse treats this as higher-trust real-person context, not noisy chat extraction.'
    : 'Preview candidate threads first. Pulse imports only structured continuity after you confirm.';
  const previewPeople = splitGalleryPersonCandidates(preview.people_candidates);
  const reviewCandidates = uniqueLimited([
    ...previewPeople.review,
    ...(Array.isArray(preview.review_candidates) ? preview.review_candidates : []),
  ], 96);
  const visibleRelationships = preview.relationship_candidates.filter((item) => !relationshipTouchesReviewEntity(item));
  const visibleFunFacts = preview.fun_fact_candidates.filter((item) => !funFactTouchesReviewEntity(item));
  const tokenEconomy = estimatePreviewTokenEconomy(preview);
  const emptyPreview = isEmptyMigrationPreview(preview);
  const profilePreview = {
    ...preview,
    people_candidates: previewPeople.confident,
    relationship_candidates: visibleRelationships,
    fun_fact_candidates: visibleFunFacts,
  };
  return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Pulse Import Preview</title>
  <style>
    :root { color-scheme: light; --bg:#fbf7f4; --ink:#463b3d; --muted:#8f8180; --line:rgba(136,116,112,.14); --panel:rgba(255,255,255,.54); --rail:rgba(255,255,255,.34); --rail-muted:#9a8c89; --accent:#d99a8f; --accent-soft:#f7cbc2; --good:#8bb79f; --soft:rgba(255,255,255,.46); --pastel-a:#f8d9d0; --pastel-b:#dceaf7; --pastel-c:#e7e0fa; --pastel-d:#e4f2df; }
    * { box-sizing: border-box; }
    body { margin:0; min-height:100vh; font-family:ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; color:var(--ink); background:
      radial-gradient(circle at 8% 8%, rgba(248,217,208,.92), transparent 32%),
      radial-gradient(circle at 82% 12%, rgba(220,234,247,.86), transparent 30%),
      radial-gradient(circle at 68% 72%, rgba(231,224,250,.72), transparent 36%),
      linear-gradient(145deg,#fffaf7 0%,#f6fbff 48%,#fff7fb 100%); }
    .workbench { min-height:100vh; display:grid; grid-template-columns:260px minmax(0,1fr); }
    .rail { position:sticky; top:0; height:100vh; padding:26px 22px; background:var(--rail); color:var(--ink); display:flex; flex-direction:column; gap:26px; border-right:1px solid rgba(255,255,255,.55); backdrop-filter:blur(24px) saturate(1.35); -webkit-backdrop-filter:blur(24px) saturate(1.35); }
    .brand { display:grid; gap:8px; }
    .brand b { font-size:22px; letter-spacing:0; font-weight:680; color:#5a4d51; }
    .brand span, .rail small { color:var(--rail-muted); line-height:1.45; }
    .rail-nav { display:grid; gap:8px; }
    .rail-nav a, .rail-pill { color:var(--ink); text-decoration:none; border:1px solid rgba(255,255,255,.58); border-radius:14px; padding:10px 12px; background:rgba(255,255,255,.42); box-shadow:0 10px 26px rgba(115,92,84,.08); backdrop-filter:blur(16px); -webkit-backdrop-filter:blur(16px); }
    .rail-pill { color:var(--rail-muted); }
    .workspace { min-width:0; padding:34px clamp(18px,4vw,54px) 54px; }
    header { display:grid; grid-template-columns:minmax(0,1fr) minmax(190px,280px); gap:24px; align-items:end; padding-bottom:24px; border-bottom:1px solid var(--line); }
    h1 { margin:0; font-size:46px; line-height:1.08; letter-spacing:0; font-weight:680; color:#4a3f42; max-width:760px; }
    h2 { margin:0 0 12px; font-size:18px; letter-spacing:0; }
    h3 { margin:0 0 6px; font-size:16px; letter-spacing:0; }
    p { margin:0; line-height:1.48; }
    .subhead { margin:12px 0 0; max-width:760px; color:var(--muted); font-size:16px; line-height:1.5; }
    .glass-card, .stamp, section, .person-card, .metric, .decision-card { background:var(--panel); border:1px solid rgba(255,255,255,.62); border-radius:16px; box-shadow:0 18px 50px rgba(125,102,94,.12), inset 0 1px 0 rgba(255,255,255,.74); backdrop-filter:blur(22px) saturate(1.24); -webkit-backdrop-filter:blur(22px) saturate(1.24); }
    .stamp { padding:18px; }
    .stamp strong { display:block; font-size:22px; letter-spacing:0; font-weight:680; line-height:1; }
    .stamp span { display:block; margin-top:8px; color:var(--muted); font-size:13px; line-height:1.35; }
    .metrics { display:grid; grid-template-columns:repeat(auto-fit,minmax(130px,1fr)); gap:12px; margin:18px 0; }
    .metric { min-height:88px; padding:14px 14px 14px 16px; position:relative; overflow:hidden; }
    .metric::before { content:""; position:absolute; inset:0 auto 0 0; width:4px; background:linear-gradient(180deg,var(--pastel-a),var(--pastel-c)); }
    .metric b { display:block; font-size:28px; line-height:1.05; letter-spacing:0; font-weight:680; }
    .metric span { display:block; margin-top:8px; color:var(--muted); font-size:13px; }
    section { padding:18px; margin-top:16px; }
    .filter-panel { margin-top:16px; }
    .filter-panel p { margin:0 0 12px; color:var(--muted); }
    .filter-row { display:grid; grid-template-columns:minmax(0,1fr) auto; gap:10px; align-items:center; }
    .filter-row input { min-height:42px; width:100%; border:1px solid rgba(255,255,255,.68); border-radius:999px; padding:9px 14px; font:inherit; background:rgba(255,255,255,.62); color:var(--ink); backdrop-filter:blur(14px); -webkit-backdrop-filter:blur(14px); }
    .filter-row button, .copy { min-height:40px; padding:9px 13px; border:1px solid rgba(217,154,143,.34); border-radius:999px; background:linear-gradient(135deg,rgba(255,255,255,.9),rgba(247,203,194,.58)); color:#6d5653; cursor:pointer; font:inherit; white-space:nowrap; box-shadow:0 10px 24px rgba(160,126,118,.11); }
    .filter-row button:hover, .copy:hover { transform:translateY(-1px); }
    #preview-filter-status { margin-top:10px; color:var(--muted); font-size:14px; }
    .source-flow-grid, .session-grid, .thread-flow-grid, .gate-summary-grid, .insight-grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(220px,1fr)); gap:12px; }
    .flow-card, .session-card, .thread-flow-card, .gate-summary, .insight-card { background:rgba(255,255,255,.50); border:1px solid rgba(255,255,255,.62); border-radius:14px; padding:14px; box-shadow:inset 0 1px 0 rgba(255,255,255,.66); }
    .flow-card span, .session-card span, .thread-flow-head span { display:block; color:var(--muted); font-size:12px; letter-spacing:.08em; text-transform:uppercase; }
    .flow-card strong { display:block; margin-top:7px; font-size:22px; font-weight:680; overflow-wrap:anywhere; }
    .session-card h3 { margin-top:7px; font-size:18px; }
    .session-card p { color:var(--muted); overflow-wrap:anywhere; }
    .thread-flow-grid { grid-template-columns:repeat(auto-fit,minmax(280px,1fr)); }
    .insight-grid { grid-template-columns:repeat(auto-fit,minmax(300px,1fr)); }
    .insight-section { background:linear-gradient(145deg,rgba(255,250,247,.66),rgba(232,240,251,.50)); }
    .insight-card { border-color:rgba(217,154,143,.30); }
    .insight-card.active-thread { border-color:rgba(139,183,159,.52); background:rgba(244,252,247,.62); }
    .insight-card > span { display:inline-flex; margin-bottom:9px; border:1px solid rgba(217,154,143,.30); background:rgba(255,255,255,.50); border-radius:999px; padding:5px 9px; color:#8b6d67; font-size:12px; letter-spacing:.06em; text-transform:uppercase; }
    .insight-card h3 { font-size:20px; }
    .insight-card p { color:#745f5b; margin-bottom:12px; }
    .thread-flow-head { display:flex; justify-content:space-between; gap:10px; align-items:baseline; border-bottom:1px solid var(--line); padding-bottom:10px; margin-bottom:12px; }
    .thread-flow-head h3 { margin:0; font-size:21px; }
    .thread-flow-metrics { display:flex; flex-wrap:wrap; gap:7px; margin:0 0 12px; }
    .thread-flow-metrics span, .review-counter { border:1px solid rgba(255,255,255,.62); background:rgba(255,255,255,.48); border-radius:999px; padding:6px 9px; color:var(--muted); font-size:13px; }
    .review-counter { display:inline-flex; margin:0 0 12px; }
    .review-status { margin-top:10px; color:var(--muted); }
    .decision-card.reviewed { border-color:rgba(139,183,159,.48); background:rgba(244,252,247,.66); }
    .decision-card.ignored { opacity:.72; }
    .section-note { margin-top:10px; }
    .profile-grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(250px,1fr)); gap:12px; }
    .person-card { padding:16px; border-top:1px solid rgba(217,154,143,.36); }
    .person-head { display:flex; justify-content:space-between; gap:10px; align-items:baseline; border-bottom:1px solid var(--line); padding-bottom:10px; margin-bottom:12px; }
    .person-head h3 { margin:0; font-size:22px; letter-spacing:0; font-weight:680; }
    .person-head span { color:var(--muted); font-size:13px; white-space:nowrap; }
    .mini-block + .mini-block { margin-top:13px; }
    .mini-block h4 { margin:0 0 7px; color:var(--muted); font-size:12px; letter-spacing:.08em; text-transform:uppercase; }
    .grid { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:12px; }
    .decision-grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(240px,1fr)); gap:12px; }
    .decision-card { padding:15px; }
    .decision-actions { display:flex; flex-wrap:wrap; gap:8px; margin-top:12px; }
    .decision-actions button { min-height:34px; padding:7px 11px; border:1px solid rgba(217,154,143,.34); border-radius:999px; background:linear-gradient(135deg,rgba(255,255,255,.9),rgba(247,203,194,.54)); color:#6d5653; cursor:pointer; font:inherit; font-size:13px; line-height:1; white-space:nowrap; box-shadow:0 8px 18px rgba(160,126,118,.10); }
    .decision-actions button:hover { transform:translateY(-1px); }
    .review-export { margin-top:14px; border-top:1px solid rgba(232,137,119,.22); padding-top:12px; }
    .review-export button { min-height:38px; margin-top:10px; padding:8px 12px; border:1px solid rgba(217,154,143,.34); border-radius:999px; background:linear-gradient(135deg,rgba(255,255,255,.9),rgba(247,203,194,.58)); color:#6d5653; cursor:pointer; font:inherit; box-shadow:0 10px 24px rgba(160,126,118,.11); }
    .wide { grid-column:span 2; }
    ul { list-style:none; padding:0; margin:0; display:grid; gap:8px; }
    li { border:1px solid rgba(255,255,255,.62); padding:10px 11px; background:rgba(255,255,255,.46); border-radius:12px; line-height:1.35; overflow-wrap:anywhere; }
    .empty { color:var(--muted); }
    .graph-stage { background:linear-gradient(145deg,rgba(255,255,255,.64),rgba(232,240,251,.48)); color:var(--ink); border-color:rgba(255,255,255,.72); }
    .graph-stage .subhead, .graph-stage .empty { color:var(--muted); }
    .graph-stage .grid section { background:rgba(255,255,255,.44); border-color:rgba(255,255,255,.62); color:var(--ink); box-shadow:inset 0 1px 0 rgba(255,255,255,.72); }
    .graph-stage li { background:rgba(255,255,255,.42); border-color:rgba(255,255,255,.62); color:var(--ink); }
    .graph-stage ul { max-height:380px; overflow:auto; padding-right:4px; }
    #review-queue ul { max-height:260px; overflow:auto; padding-right:4px; }
    .trust { border-color:rgba(217,154,143,.24); background:rgba(255,246,242,.56); color:#6d5653; }
    .trust p { color:#846a65; }
    .gate-step { margin-top:14px; border-top:1px solid rgba(232,137,119,.22); padding-top:12px; }
    .command { display:grid; grid-template-columns:minmax(0,1fr) auto; gap:8px; align-items:start; margin-top:10px; }
    code { display:block; padding:11px 12px; border:1px solid rgba(255,255,255,.62); border-radius:12px; background:rgba(255,255,255,.46); color:#4b5558; overflow-wrap:anywhere; font-family:ui-monospace,SFMono-Regular,Menlo,monospace; font-size:12px; line-height:1.45; }
    .review-export p code, .gate-step p code { display:inline; padding:2px 6px; border-radius:7px; font-size:13px; line-height:1.3; }
    .copy:focus-visible, .filter-row button:focus-visible, .decision-actions button:focus-visible, input:focus-visible, .rail-nav a:focus-visible { outline:3px solid rgba(232,137,119,.32); outline-offset:2px; }
    @media (max-width:900px) { .workbench { grid-template-columns:1fr; } .rail { position:relative; height:auto; } header, .grid, .filter-row, .command { grid-template-columns:1fr; } .wide { grid-column:auto; } h1 { font-size:34px; line-height:1.08; } }
  </style>
</head>
<body data-design="pulse-glass-v3" data-import-flow="pulse.import_preview.v1">
  <main class="workbench">
    <aside class="rail" aria-label="Pulse preview navigation">
      <div class="brand">
        <b>Pulse Import</b>
        <span>Thread preview before continuity import.</span>
      </div>
      <nav class="rail-nav">
        <a href="#sources-scanned">Scan</a>
        <a href="#scanned-sessions">Sessions</a>
        <a href="#candidate-threads">Threads</a>
        <a href="#pulse-insights">Insight</a>
        <a href="#review-actions">Review</a>
        <a href="#import-gate">Gate</a>
      </nav>
      <div class="rail-pill">Review first. Import later.</div>
      <small>No raw transcript is written by this preview.</small>
    </aside>
    <div class="workspace">
    <header id="source-preview">
      <div>
        <h1>${htmlEscape(previewTitle)}</h1>
        <p class="subhead">${htmlEscape(previewSubtitle)}</p>
      </div>
      <div class="stamp">
        <strong>${htmlEscape(isPeopleGraph ? 'Curated people' : preview.source)}</strong>
        <span>Generated ${htmlEscape(generatedAt)}</span>
      </div>
    </header>

    ${renderSourceScanFlow(preview)}
    ${renderScannedSessionsFlow(preview)}
    ${renderCandidateThreadsFlow(preview)}
    ${renderPulseInsightsFlow(preview)}
    ${renderReviewActionsFlow(preview)}

    <div class="metrics" aria-label="Migration preview metrics">
      <div class="metric"><b>${htmlEscape(preview.conversations)}</b><span>conversations</span></div>
      <div class="metric"><b>${htmlEscape(preview.messages)}</b><span>source snippets</span></div>
      <div class="metric"><b>${htmlEscape(Math.max(1, preview.memory_candidates.length))}</b><span>threads</span></div>
      <div class="metric"><b>${htmlEscape(preview.memory_candidates.length)}</b><span>decisions</span></div>
      <div class="metric"><b>${htmlEscape(visibleRelationships.length > 0 ? 1 : 0)}</b><span>open loops</span></div>
      <div class="metric"><b>${htmlEscape(reviewCandidates.length)}</b><span>needs decision</span></div>
      <div class="metric"><b>${htmlEscape(preview.emotion_candidates.length)}</b><span>emotional anchors</span></div>
      <div class="metric"><b>${htmlEscape(previewPeople.confident.length)}</b><span>people found</span></div>
      <div class="metric"><b>${htmlEscape(tokenEconomy.estimatedSaved)}</b><span>estimated saved</span></div>
      <div class="metric"><b>${htmlEscape(preview.redacted_fragments)}</b><span>redacted fragments</span></div>
    </div>

    ${emptyPreview ? renderEmptyPreviewNotice() : ''}

    <section id="thread-preview">
      <h2>Thread preview</h2>
      <p class="subhead">What Pulse will turn into continuity. Start here, then inspect people and graph details only if needed.</p>
      ${renderPreviewThreadCards(preview)}
    </section>

    <section id="token-economy">
      <h2>Token economy</h2>
      <div class="metrics" aria-label="Estimated token economy">
        <div class="metric"><b>${htmlEscape(tokenEconomy.estimatedRaw)}</b><span>estimated raw</span></div>
        <div class="metric"><b>${htmlEscape(tokenEconomy.resumeTokens)}</b><span>resume budget</span></div>
        <div class="metric"><b>${htmlEscape(tokenEconomy.estimatedSaved)}</b><span>estimated saved</span></div>
      </div>
      <p class="empty">These are estimates for preview. Pulse stores the smaller continuity shape, not the raw archive text.</p>
    </section>

    <section class="filter-panel">
      <h2>Filter preview</h2>
      <p>Search locally across threads, decisions, open loops, people found, and review items before importing anything.</p>
      <div class="filter-row">
        <input id="preview-filter" placeholder="Type a thread, person, decision, or review item">
        <button id="preview-filter-clear">Clear</button>
      </div>
      <div id="preview-filter-status">Showing all preview candidates.</div>
    </section>

    <section id="person-profiles">
      <h2>People found</h2>
      ${renderPreviewPersonCards(profilePreview)}
    </section>

    <section id="review-queue">
      <h2>Needs your decision</h2>
      <p class="empty"><strong>Review before import:</strong> these may be models, tools, projects, archive labels, or ambiguous entities. Pulse keeps them out of people/context memory until you decide.</p>
      <div class="decision-grid">
        ${(reviewCandidates.length > 0 ? reviewCandidates.slice(0, 8) : ['No ambiguous people candidates yet.']).map((item) => `<article class="decision-card filter-item" data-search="${htmlEscape(item)}"><h3>Review: ${htmlEscape(item)}</h3><p>Suggested action: review before import.</p><div class="decision-actions"><button type="button">Confirm</button><button type="button">Edit</button><button type="button">Ignore</button><button type="button">Mark private</button></div></article>`).join('')}
      </div>
    </section>

    <section class="graph-stage" id="thread-map">
      <h2>Thread map and source details</h2>
      <p class="subhead">Lower-level details from this selected source. They stay behind review and do not become raw memory by default.</p>
      <div class="grid">
        <section>
          <h2>Threads</h2>
          ${renderPreviewTimeline(preview)}
        </section>
        <section>
          <h2>Decisions</h2>
          ${previewList(preview.memory_candidates.map(continuityPreviewText))}
        </section>
        <section>
          <h2>Open loops</h2>
          ${previewList(visibleRelationships.slice(0, 12).map(continuityPreviewText), 'No open loops yet.')}
        </section>
        <section>
          <h2>Do-not-repeat</h2>
          ${previewList(['Do not import raw transcript text.'])}
        </section>
        <section>
          <h2>Emotional anchors</h2>
          ${previewList(preview.emotion_candidates)}
        </section>
        <section>
          <h2>People found</h2>
          ${previewList(previewPeople.confident)}
        </section>
      </div>
    </section>

    <div id="import-gate">${renderPreviewImportGate(preview)}</div>
    </div>
  </main>
  <script type="application/json" id="reviewed-preview-data">${htmlEscape(JSON.stringify(reviewedPreviewPayload(preview)))}</script>
  <script>
    async function copyCommand(button) {
      const command = button.getAttribute("data-copy") || "";
      try {
        await navigator.clipboard.writeText(command);
        button.textContent = "Copied";
      } catch {
        const textarea = document.createElement("textarea");
        textarea.value = command;
        document.body.appendChild(textarea);
        textarea.select();
        document.execCommand("copy");
        textarea.remove();
        button.textContent = "Copied";
      }
      setTimeout(() => { button.textContent = "Copy command"; }, 1400);
    }
    for (const button of document.querySelectorAll("[data-copy]")) {
      button.addEventListener("click", () => copyCommand(button));
    }
    const reviewDecisions = {};
    const activeThreads = {};
    function updateReviewCounter() {
      const counter = document.getElementById("review-counter");
      if (!counter) return;
      const total = Number(counter.getAttribute("data-review-total") || 0);
      const reviewed = document.querySelectorAll("[data-review-card].reviewed").length;
      counter.textContent = "reviewed " + reviewed + " of " + total + " · Download reviewed JSON before import.";
    }
    function reviewedPreviewBase() {
      const node = document.getElementById("reviewed-preview-data");
      try {
        return JSON.parse(node ? node.textContent || "{}" : "{}");
      } catch {
        return {};
      }
    }
    function seedActiveThreads() {
      const base = reviewedPreviewBase();
      const raw = Array.isArray(base.active_threads) ? base.active_threads : [];
      for (const thread of raw) {
        const title = typeof thread === "string" ? thread : thread && (thread.thread_title || thread.title);
        if (!title) continue;
        activeThreads[title] = typeof thread === "string"
          ? { thread_title: title, source: "pulse_review" }
          : { ...thread, thread_title: title };
      }
    }
    function downloadReviewedJSON() {
      const reviewed = {
        ...reviewedPreviewBase(),
        review_decisions: reviewDecisions,
        active_threads: Object.values(activeThreads),
        reviewed_at: new Date().toISOString(),
        flow: "pulse.import_preview.v1"
      };
      const blob = new Blob([JSON.stringify(reviewed, null, 2) + "\\n"], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = "pulse-preview.reviewed.json";
      document.body.appendChild(link);
      link.click();
      link.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
      const status = document.getElementById("review-download-status");
      if (status) status.textContent = "pulse-preview.reviewed.json downloaded with review_decisions and active_threads.";
    }
    seedActiveThreads();
    for (const button of document.querySelectorAll("[data-active-thread]")) {
      button.addEventListener("click", () => {
        const title = button.getAttribute("data-active-thread") || "";
        const reason = button.getAttribute("data-active-reason") || "User marked this Pulse insight as active during review.";
        if (!title) return;
        activeThreads[title] = {
          thread_title: title,
          reason,
          source: "pulse_insight",
          reviewed_at: new Date().toISOString()
        };
        const card = button.closest(".insight-card");
        if (card) card.classList.add("active-thread");
        const status = card ? card.querySelector("[data-active-thread-status]") : null;
        if (status) status.textContent = "Active thread staged for reviewed JSON. Download before import.";
        const downloadStatus = document.getElementById("review-download-status");
        if (downloadStatus) downloadStatus.textContent = "active_threads updated. Download pulse-preview.reviewed.json before import.";
      });
    }
    for (const button of document.querySelectorAll("[data-review-action]")) {
      button.addEventListener("click", () => {
        const card = button.closest("[data-review-card]");
        if (!card) return;
        const status = card.querySelector("[data-review-status]");
        const item = card.getAttribute("data-review-item") || "";
        const result = button.getAttribute("data-review-result") || button.getAttribute("data-review-action") || "reviewed";
        const action = button.getAttribute("data-review-action") || "reviewed";
        const kind = button.getAttribute("data-review-kind") || "unknown";
        if (item) {
          reviewDecisions[item] = {
            action,
            result,
            kind,
            privacy_tier: "private",
            reviewed_at: new Date().toISOString()
          };
        }
        card.classList.add("reviewed");
        card.classList.toggle("ignored", result === "ignored");
        if (status) {
          status.textContent = "Review decision staged for reviewed JSON: " + result + ". Download before import.";
        }
        const downloadStatus = document.getElementById("review-download-status");
        if (downloadStatus) downloadStatus.textContent = "review_decisions updated. Download pulse-preview.reviewed.json before import.";
        updateReviewCounter();
      });
    }
    const reviewedDownload = document.getElementById("download-reviewed-json");
    if (reviewedDownload) reviewedDownload.addEventListener("click", downloadReviewedJSON);
    updateReviewCounter();
    function applyPreviewFilter() {
      const input = document.getElementById("preview-filter");
      if (!input) return;
      const query = input.value.trim().toLowerCase();
      let shown = 0;
      let total = 0;
      for (const item of document.querySelectorAll(".filter-item")) {
        total += 1;
        const text = (item.getAttribute("data-search") || item.textContent || "").toLowerCase();
        const match = !query || text.includes(query);
        item.hidden = !match;
        if (match) shown += 1;
      }
      const status = document.getElementById("preview-filter-status");
      if (status) {
        status.textContent = query ? "Showing " + shown + " of " + total + " preview items for " + JSON.stringify(query) + "." : "Showing all preview candidates.";
      }
    }
    const previewFilter = document.getElementById("preview-filter");
    if (previewFilter) {
      previewFilter.addEventListener("input", applyPreviewFilter);
    }
    const previewFilterClear = document.getElementById("preview-filter-clear");
    if (previewFilterClear) {
      previewFilterClear.addEventListener("click", () => {
        document.getElementById("preview-filter").value = "";
        applyPreviewFilter();
      });
    }
  </script>
</body>
</html>
`;
}

function renderMigrationConciergeHTML() {
  const generatedAt = new Date().toISOString();
  return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Pulse Archive Concierge</title>
  <style>
    :root { color-scheme: light; --bg:#fbf7f4; --ink:#463b3d; --muted:#8f8180; --line:rgba(136,116,112,.14); --panel:rgba(255,255,255,.54); --rail:rgba(255,255,255,.34); --rail-muted:#9a8c89; --accent:#d99a8f; --accent-soft:#f7cbc2; --good:#8bb79f; --warn:#d6a95f; --soft:rgba(255,255,255,.46); --pastel-a:#f8d9d0; --pastel-b:#dceaf7; --pastel-c:#e7e0fa; --pastel-d:#e4f2df; }
    * { box-sizing: border-box; }
    body { margin:0; min-height:100vh; font-family:ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; color:var(--ink); background:
      radial-gradient(circle at 8% 8%, rgba(248,217,208,.92), transparent 32%),
      radial-gradient(circle at 82% 12%, rgba(220,234,247,.86), transparent 30%),
      radial-gradient(circle at 68% 72%, rgba(231,224,250,.72), transparent 36%),
      linear-gradient(145deg,#fffaf7 0%,#f6fbff 48%,#fff7fb 100%); }
    .workbench { min-height:100vh; display:grid; grid-template-columns:260px minmax(0,1fr); }
    .rail { position:sticky; top:0; height:100vh; padding:26px 22px; background:var(--rail); color:var(--ink); display:flex; flex-direction:column; gap:26px; border-right:1px solid rgba(255,255,255,.55); backdrop-filter:blur(24px) saturate(1.35); -webkit-backdrop-filter:blur(24px) saturate(1.35); }
    .brand { display:grid; gap:8px; }
    .brand b { font-size:22px; letter-spacing:0; font-weight:680; color:#5a4d51; }
    .brand span, .rail small { color:var(--rail-muted); line-height:1.45; }
    .rail-nav { display:grid; gap:8px; }
    .rail-nav a, .rail-pill { color:var(--ink); text-decoration:none; border:1px solid rgba(255,255,255,.58); border-radius:14px; padding:10px 12px; background:rgba(255,255,255,.42); box-shadow:0 10px 26px rgba(115,92,84,.08); backdrop-filter:blur(16px); -webkit-backdrop-filter:blur(16px); }
    .rail-pill { color:var(--rail-muted); }
    .workspace { min-width:0; padding:34px clamp(18px,4vw,54px) 54px; }
    header { display:grid; grid-template-columns:minmax(0,1fr) minmax(190px,280px); gap:24px; align-items:end; padding-bottom:24px; border-bottom:1px solid var(--line); }
    h1 { margin:0; font-size:46px; line-height:1.08; letter-spacing:0; font-weight:680; color:#4a3f42; max-width:760px; }
    h2 { margin: 0 0 12px; font-size:18px; letter-spacing:0; }
    p { margin: 0; }
    .subhead { margin:12px 0 0; color:var(--muted); font-size:16px; line-height:1.5; max-width:760px; }
    .glass-card, .stamp, section, .path-step { background:var(--panel); border:1px solid rgba(255,255,255,.62); border-radius:16px; box-shadow:0 18px 50px rgba(125,102,94,.12), inset 0 1px 0 rgba(255,255,255,.74); backdrop-filter:blur(22px) saturate(1.24); -webkit-backdrop-filter:blur(22px) saturate(1.24); }
    .stamp { padding:18px; }
    .stamp strong { display:block; font-size:22px; letter-spacing:0; font-weight:680; line-height:1; }
    .stamp span { display:block; margin-top:8px; color:var(--muted); font-size:13px; line-height:1.35; }
    .grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap:12px; margin-top:16px; }
    .path {
      display: grid;
      grid-template-columns: repeat(4, minmax(0, 1fr));
      gap: 12px;
      margin-top:16px;
    }
    .path-step {
      padding:16px;
      min-height: 124px;
    }
    .path-step b {
      display: inline-grid;
      place-items: center;
      width: 28px;
      height: 28px;
      border-radius: 999px;
      background: linear-gradient(135deg,var(--pastel-a),var(--pastel-c));
      color:#5f4039;
      margin-bottom: 10px;
    }
    .path-step strong {
      display: block;
      margin-bottom: 6px;
      font-size: 17px;
    }
    .path-step span {
      color:var(--muted);
      display:block;
      line-height:1.4;
    }
    section {
      padding: 18px;
      min-height: 230px;
    }
    .grid section { position:relative; overflow:hidden; }
    .grid section::before { content:""; position:absolute; inset:0 auto 0 0; width:4px; background:linear-gradient(180deg,var(--pastel-a),var(--pastel-c)); }
    .grid section:nth-child(3)::before, .grid section:nth-child(4)::before { background:var(--good); }
    .action {
      display: inline-flex;
      align-items: center;
      justify-content:center;
      min-height: 38px;
      padding:9px 13px;
      margin: 12px 0 10px;
      border: 1px solid rgba(232,137,119,.42);
      border-radius:999px;
      color:#5f4039;
      text-decoration: none;
      background:linear-gradient(135deg,rgba(255,255,255,.86),rgba(248,217,208,.76));
      box-shadow:0 10px 24px rgba(160,126,118,.11);
    }
    ol, ul { margin: 10px 0 0; padding-left: 20px; }
    li { margin: 7px 0; }
    code {
      display: block;
      padding:11px 12px;
      border:1px solid rgba(255,255,255,.62);
      border-radius:12px;
      background:rgba(255,255,255,.46);
      color:#4b5558;
      overflow-wrap: anywhere;
      font-family:ui-monospace,SFMono-Regular,Menlo,monospace;
      font-size:12px;
      line-height:1.45;
    }
    .command {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 8px;
      align-items: start;
      margin-top: 10px;
    }
    .copy {
      min-height:40px;
      padding:9px 13px;
      border:1px solid rgba(232,137,119,.42);
      border-radius:999px;
      background:linear-gradient(135deg,rgba(255,255,255,.86),rgba(248,217,208,.76));
      color:#5f4039;
      box-shadow:0 10px 24px rgba(160,126,118,.11);
      cursor: pointer;
      font: inherit;
      white-space:nowrap;
    }
    .copy:hover, .action:hover { transform:translateY(-1px); }
    .copy:focus-visible, .action:focus-visible {
      outline:3px solid rgba(232,137,119,.32);
      outline-offset: 2px;
    }
    .trust {
      margin-top: 14px;
      border-color:rgba(232,137,119,.32);
      background:rgba(255,244,239,.62);
      color:#5f4039;
      min-height:auto;
    }
    @media (max-width: 900px) {
      .workbench { grid-template-columns:1fr; }
      .rail { position:relative; height:auto; }
      header, .grid, .path, .command { grid-template-columns:1fr; }
      h1 { font-size:34px; line-height:1.08; }
    }
  </style>
</head>
<body data-design="pulse-glass-v3">
  <main class="workbench">
    <aside class="rail" aria-label="Pulse archive navigation">
      <div class="brand">
        <b>Pulse Import</b>
        <span>Archive migration without blind import.</span>
      </div>
      <nav class="rail-nav">
        <a href="#archive-request">Archive request</a>
        <a href="#host-pages">Host pages</a>
        <a href="#local-sources">Local sources</a>
        <a href="#import-gate">Import gate</a>
      </nav>
      <div class="rail-pill">Human click first. Graph approval last.</div>
      <small>Pulse opens the right places. You request the archives.</small>
    </aside>
    <div class="workspace">
    <header id="archive-request">
      <div>
        <h1>Pulse Archive Concierge</h1>
        <p class="subhead">Ask each host for an archive, preview it locally, then import only the structured graph that you approve.</p>
      </div>
      <div class="stamp">
        <strong>Local review</strong>
        <span>Generated ${htmlEscape(generatedAt)}</span>
      </div>
    </header>

    <div class="path" aria-label="Pulse archive migration path">
      <div class="path-step"><b>1</b><strong>Open the host page</strong><span>Pulse can open ChatGPT Data Controls or Claude Privacy for you.</span></div>
      <div class="path-step"><b>2</b><strong>Human click only</strong><span>You click Request archive / Export data. Pulse never bypasses consent.</span></div>
      <div class="path-step"><b>3</b><strong>Pulse waits for the zip</strong><span>Run request and keep working; Pulse previews the archive when it lands.</span></div>
      <div class="path-step"><b>4</b><strong>Review gate</strong><span>Inspect threads, decisions, open loops, people found, and review items before import.</span></div>
    </div>

    <div class="grid" id="host-pages">
      <section>
        <h2>ChatGPT</h2>
        <p>Open Data Controls, click Request archive, then let Pulse wait for the export zip in Downloads.</p>
        <a class="action" href="https://chatgpt.com/#settings/DataControls" target="_blank" rel="noreferrer">Open ChatGPT Data Controls</a>
        <ol>
          <li>Click Request archive.</li>
          <li>Download the archive into Downloads.</li>
          <li>Pulse previews it before importing.</li>
        </ol>
        <div class="command"><code>pulse migrate request chatgpt --open</code><button class="copy" data-copy="pulse migrate request chatgpt --open">Copy command</button></div>
      </section>

      <section>
        <h2>Claude</h2>
        <p>Open Privacy settings, click Export data, then let Pulse wait for the export zip in Downloads.</p>
        <a class="action" href="https://claude.ai/settings/privacy" target="_blank" rel="noreferrer">Open Claude Privacy</a>
        <ol>
          <li>Click Export data.</li>
          <li>Download the archive into Downloads.</li>
          <li>Pulse previews it before importing.</li>
        </ol>
        <div class="command"><code>pulse migrate request claude --open</code><button class="copy" data-copy="pulse migrate request claude --open">Copy command</button></div>
      </section>

      <section id="local-sources">
        <h2>Codex local history</h2>
        <p>Codex sessions are already local on this Mac. Pulse can preview them immediately.</p>
        <div class="command"><code>pulse migrate request codex --open</code><button class="copy" data-copy="pulse migrate request codex --open">Copy command</button></div>
      </section>

      <section>
        <h2>Claude Code local history</h2>
        <p>Claude Code project sessions are already local on this Mac. Pulse can preview them immediately.</p>
        <div class="command"><code>pulse migrate request claude-code --open</code><button class="copy" data-copy="pulse migrate request claude-code --open">Copy command</button></div>
      </section>
    </div>

    <section class="trust" id="import-gate">
      <h2>Import gate</h2>
      <p>Raw chat text is not written by preview. Import still needs an explicit graph confirmation.</p>
      <div class="command"><code>pulse migrate commit pulse-preview.json --confirm "import pulse graph" --open</code><button class="copy" data-copy='pulse migrate commit pulse-preview.json --confirm "import pulse graph" --open'>Copy command</button></div>
      <div class="command"><code>pulse viewer --thread-id archive-import --open</code><button class="copy" data-copy="pulse viewer --thread-id archive-import --open">Copy command</button></div>
    </section>
    <section>
      <h2>Paper boundary</h2>
      <p>This migration/viewer flow is a product extension, not an evaluated paper result. It belongs in Future Work or productization notes until migration quality and privacy have their own evaluation.</p>
    </section>
    </div>
  </main>
  <script>
    async function copyCommand(button) {
      const command = button.getAttribute("data-copy") || "";
      try {
        await navigator.clipboard.writeText(command);
        button.textContent = "Copied";
      } catch {
        const textarea = document.createElement("textarea");
        textarea.value = command;
        document.body.appendChild(textarea);
        textarea.select();
        document.execCommand("copy");
        textarea.remove();
        button.textContent = "Copied";
      }
      setTimeout(() => { button.textContent = "Copy command"; }, 1400);
    }
    for (const button of document.querySelectorAll("[data-copy]")) {
      button.addEventListener("click", () => copyCommand(button));
    }
  </script>
</body>
</html>
`;
}

function renderMigrationConciergeBrief() {
  return `# Pulse Archive Migration Hand-Hold

This handoff is for importing existing AI chat history into Pulse without writing raw chat text into Pulse memory.

## What the user does

1. ChatGPT: open Data Controls and click Request archive.
2. Claude: open Privacy settings and click Export data.
3. Codex: preview local history from ~/.codex/sessions.
4. Claude Code: preview local history from ~/.claude/projects.

## Human path

Pulse opens the right page when possible. The human clicks the archive request
button. Pulse waits for the zip, builds a local preview, and imports only after
the explicit graph confirmation.

## Commands

\`\`\`bash
pulse migrate request chatgpt --open
pulse migrate request claude --open
pulse migrate request codex --open
pulse migrate request claude-code --open
pulse migrate commit pulse-preview.json --confirm "import pulse graph" --open
pulse viewer --thread-id archive-import --open
\`\`\`

## What Pulse shows

The preview and viewer are meant to make imported continuity inspectable: candidate threads, decisions, open loops, do-not-repeat notes, emotional anchors, people found, and review cards.

## Trust boundary

Preview is local and offline. Raw chat text is not written by preview. Commit requires the exact confirmation phrase and writes structured graph deltas, not a full transcript.

## Paper impact

This is a product-facing migration and inspection layer, not a new paper result. It should stay outside the evaluated retrieval claims until it has its own migration-quality and privacy evaluation. Repository note: docs/archive-migration-paper-boundary.md.
`;
}

function openExternalURL(url) {
  if (process.env.PULSE_OPEN_DRY_RUN === '1') {
    console.log(`[pulse] opened browser: ${url}`);
    return;
  }
  let opener;
  let openerArgs;
  if (process.platform === 'darwin') {
    opener = 'open';
    openerArgs = [url];
  } else if (process.platform === 'win32') {
    opener = 'cmd';
    openerArgs = ['/c', 'start', '', url];
  } else {
    opener = 'xdg-open';
    openerArgs = [url];
  }
  const result = spawnSync(opener, openerArgs, { stdio: 'ignore' });
  if (result.status !== 0) {
    throw new Error(`could not open browser automatically; open this URL manually: ${url}`);
  }
}

function parsePulseDataDirFromCommand(command) {
  const text = String(command ?? '');
  const match = text.match(/(?:^|\s)-{1,2}data-dir(?:=|\s+)(?:"([^"]+)"|'([^']+)'|(\S+))/);
  return match?.[1] ?? match?.[2] ?? match?.[3] ?? '';
}

function isLoopbackBaseURL(baseURL) {
  try {
    const { hostname } = new URL(baseURL);
    return hostname === '127.0.0.1' || hostname === 'localhost' || hostname === '::1';
  } catch {
    return false;
  }
}

function detectRunningPulseDataDir(baseURL) {
  const injected = parsePulseDataDirFromCommand(process.env.PULSE_RUNNING_PULSE_COMMAND);
  if (injected) return injected;
  if (!isLoopbackBaseURL(baseURL)) return '';

  let port;
  try {
    const url = new URL(baseURL);
    port = url.port || (url.protocol === 'https:' ? '443' : '80');
  } catch {
    return '';
  }

  const lsofArgs = ['-nP', `-iTCP:${port}`, '-sTCP:LISTEN', '-t'];
  const lsofCandidates = ['lsof', '/usr/sbin/lsof'];
  let pid = '';
  for (const candidate of lsofCandidates) {
    const result = spawnSync(candidate, lsofArgs, { encoding: 'utf8', timeout: 500 });
    if (result.status === 0 && result.stdout.trim()) {
      pid = result.stdout.trim().split(/\s+/)[0];
      break;
    }
  }
  if (!pid) return '';

  const ps = spawnSync('ps', ['-p', pid, '-o', 'command='], { encoding: 'utf8', timeout: 500 });
  if (ps.status !== 0) return '';
  return parsePulseDataDirFromCommand(ps.stdout);
}

async function checkViewerAccess(baseURL, secret, threadId) {
  if (process.env.PULSE_VIEWER_SKIP_AUTH_CHECK === '1') {
    return { status: 0, text: '' };
  }
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 500);
  try {
    const dataURL = new URL(`${baseURL.replace(/\/$/, '')}/viewer/data`);
    dataURL.searchParams.set('key', secret);
    dataURL.searchParams.set('thread_id', threadId);
    const response = await fetch(dataURL, { method: 'GET', signal: controller.signal });
    return { status: response.status, text: await response.text() };
  } catch {
    return { status: 0, text: '' };
  } finally {
    clearTimeout(timer);
  }
}

async function runViewer(rest) {
  const dataDir = resolve(getRestArg(rest, '--data-dir') ?? DATA_DIR);
  const baseURL = (getRestArg(rest, '--base') ?? DEFAULT_BASE_URL).replace(/\/$/, '');
  const secret = readSecretFromDataDir(dataDir, { create: true });
  const ctx = localThreadContext();
  const threadId = safeThreadID(getRestArg(rest, '--thread-id') ?? ctx.threadId);
  const url = `${baseURL}/viewer?key=${encodeURIComponent(secret)}&thread_id=${encodeURIComponent(threadId)}`;

  if (rest.includes('--print-url')) {
    console.log(url);
    return;
  }

  const access = await checkViewerAccess(baseURL, secret, threadId);
  if (access.status === 401 || access.status === 403) {
    console.log(`[pulse] viewer auth check failed for data dir: ${dataDir}`);
    console.log('[pulse] The running Pulse daemon is using a different local secret.');
    const detectedDataDir = detectRunningPulseDataDir(baseURL);
    if (detectedDataDir && resolve(detectedDataDir) !== dataDir) {
      console.log(`[pulse] Detected Pulse daemon data dir: ${detectedDataDir}`);
      console.log('[pulse] Try:');
      console.log(`  pulse viewer --base ${shellArg(baseURL)} --data-dir ${shellArg(detectedDataDir)} --thread-id ${shellArg(threadId)} --open`);
    } else {
      console.log('[pulse] Start the daemon and viewer with the same --data-dir, or pass --data-dir from the running daemon.');
    }
    return;
  }

  if (rest.includes('--open')) {
    openExternalURL(url);
  }
  console.log(`[pulse] local viewer: ${url}`);
  console.log(`[pulse] data dir: ${dataDir}`);
  console.log(`[pulse] thread id: ${threadId}`);
  console.log('[pulse] Shows what Pulse will tell Claude next time.');
}

function safeThreadID(value) {
  const thread = String(value ?? '').trim();
  if (!/^[A-Za-z0-9][A-Za-z0-9._:-]{0,95}$/.test(thread)) {
    throw new Error('thread id must start with a letter/number and contain only letters, numbers, dot, underscore, colon, or dash');
  }
  return thread;
}

function shellArg(value) {
  if (/^[A-Za-z0-9_./:-]+$/.test(value)) {
    return value;
  }
  return `'${String(value).replace(/'/g, `'\\''`)}'`;
}

function viewerNextStepCommand(threadId = localThreadContext().threadId) {
  const baseURL = DEFAULT_BASE_URL.replace(/\/$/, '');
  const dataDir = process.env.PULSE_VIEWER_SKIP_AUTH_CHECK === '1'
    ? DATA_DIR
    : (detectRunningPulseDataDir(baseURL) || DATA_DIR);
  return `pulse viewer --base ${shellArg(baseURL)} --data-dir ${shellArg(dataDir)} --thread-id ${shellArg(safeThreadID(threadId))} --open`;
}

function remoteArchiveInfo(source) {
  if (source === 'chatgpt') {
    return {
      source,
      label: 'ChatGPT',
      url: 'https://chatgpt.com/#settings/DataControls',
      buttonText: 'Request archive',
    };
  }
  if (source === 'claude') {
    return {
      source,
      label: 'Claude',
      url: 'https://claude.ai/settings/privacy',
      buttonText: 'Export data',
    };
  }
  throw new Error('pulse migrate request supports: chatgpt, claude, codex, claude-code');
}

function localHistoryInfo(source) {
  if (source === 'codex') {
    return {
      source,
      label: 'Codex',
      path: join(homedir(), '.codex', 'sessions'),
      displayPath: '~/.codex/sessions',
    };
  }
  if (source === 'claude-code') {
    return {
      source,
      label: 'Claude Code',
      path: join(homedir(), '.claude', 'projects'),
      displayPath: '~/.claude/projects',
    };
  }
  return null;
}

function printRemoteArchiveGuide(source, label, url, buttonText) {
  console.log(`[pulse] ${label} archive handoff
────────────────────────────────

Browser:
  ${url}

What to do:
  1. Open the page above.
  2. Sign in if the host asks.
  3. Click "${buttonText}".
  4. Keep Pulse waiting for the zip:
     pulse migrate request ${source} --open
  5. If the zip is already downloaded:
     pulse migrate preview-latest ${source} --html pulse-preview.html --out pulse-preview.json --open
  6. Manual path:
     pulse migrate preview <${source}-export.zip-or-folder> --html pulse-preview.html --out pulse-preview.json --open

Want Pulse to open the page for you:
  pulse migrate guide ${source} --open

Privacy:
  preview shows counts and safe candidates only.
  raw chat text is not written by preview.
`);
}

function printLocalHistoryGuide(source, label, path) {
  console.log(`[pulse] ${label} local history handoff
─────────────────────────────────────

Local history:
  ${path}

What to do:
  1. Keep this local. No archive request is needed.
  2. Run:
     pulse migrate request ${source} --open
  3. Review people/thread candidates before any future commit.

Manual path:
  pulse migrate preview ${path} --html pulse-preview.html --out pulse-preview.json --open

Privacy:
  preview shows counts and safe candidates only.
  raw prompt or transcript text is not written by preview.
`);
}

function migrationGuide(source) {
  if (source === 'chatgpt') {
    const info = remoteArchiveInfo(source);
    if (args.includes('--open')) {
      openExternalURL(info.url);
    }
    printRemoteArchiveGuide(info.source, info.label, info.url, info.buttonText);
    return;
  }
  if (source === 'claude') {
    const info = remoteArchiveInfo(source);
    if (args.includes('--open')) {
      openExternalURL(info.url);
    }
    printRemoteArchiveGuide(info.source, info.label, info.url, info.buttonText);
    return;
  }
  if (source === 'codex') {
    const info = localHistoryInfo(source);
    printLocalHistoryGuide(info.source, info.label, info.displayPath);
    return;
  }
  if (source === 'claude-code') {
    const info = localHistoryInfo(source);
    printLocalHistoryGuide(info.source, info.label, info.displayPath);
    return;
  }
  throw new Error('pulse migrate guide supports: chatgpt, claude, codex, claude-code');
}

function runMigrationConcierge(rest) {
  const htmlPath = resolve(restArg(rest, '--html') ?? 'pulse-migrate-concierge.html');
  writeFileSync(htmlPath, renderMigrationConciergeHTML(), { mode: 0o600 });
  const briefArg = restArg(rest, '--brief');
  if (briefArg) {
    const briefPath = resolve(briefArg);
    writeFileSync(briefPath, renderMigrationConciergeBrief(), { mode: 0o600 });
    console.log(`[pulse] migration brief: ${briefPath}`);
  }
  if (rest.includes('--open')) {
    openExternalURL(htmlPath);
  }
  console.log(`[pulse] migration concierge: ${htmlPath}`);
  console.log('[pulse] Open it in a browser, request archives, then run request/wait-latest/preview before commit.');
}

function renderMigrationStatus(status) {
  const lines = [
    '# Pulse Import Status',
    '',
    `Generated: ${new Date().toISOString()}`,
    `Output directory: ${status.outDir}`,
    `Downloads directory: ${resolve(status.downloadsDir)}`,
    '',
    '## Open First',
    '',
    `- Concierge: ${status.conciergeHTML}`,
    `- Brief: ${status.conciergeBrief}`,
    ...(status.galleryHTML ? [`- Memory gallery: ${status.galleryHTML}`] : []),
    '',
    '## Remote Archives',
    '',
    `- ChatGPT archive: ${status.chatgpt}`,
    `- Claude archive: ${status.claude}`,
    '',
    '## Local Previews',
    '',
    ...(status.peopleGraph ? [`- People Graph preview: ${status.peopleGraph}`] : []),
    `- Codex preview: ${status.codex}`,
    `- Claude Code preview: ${status.claudeCode}`,
    '',
    '## Next Commands',
    '',
    '```bash',
    `pulse migrate wait-latest chatgpt --downloads ${shellArg(resolve(status.downloadsDir))} --html ${shellArg(join(status.outDir, 'pulse-chatgpt-preview.html'))} --out ${shellArg(join(status.outDir, 'pulse-chatgpt-preview.json'))} --open`,
    `pulse migrate wait-latest claude --downloads ${shellArg(resolve(status.downloadsDir))} --html ${shellArg(join(status.outDir, 'pulse-claude-preview.html'))} --out ${shellArg(join(status.outDir, 'pulse-claude-preview.json'))} --open`,
    '```',
    '',
    'Nothing has been imported yet. Review preview pages first, then use the explicit commit command shown inside the preview.',
    '',
  ];
  return `${lines.join('\n')}\n`;
}

function statusBadge(value) {
  const text = String(value ?? '');
  if (/preview ready|ready:/i.test(text)) return 'ready';
  if (/not ready|waiting/i.test(text)) return 'waiting';
  return 'idle';
}

function statusTitle(value) {
  const text = String(value ?? '');
  if (/preview ready/i.test(text)) return 'Preview ready';
  if (/ready:/i.test(text)) return 'Ready';
  if (/not ready/i.test(text)) return 'Not ready yet';
  if (/waiting for Request archive/i.test(text)) return 'Waiting for Request archive';
  if (/waiting for Export data/i.test(text)) return 'Waiting for Export data';
  return 'Pending';
}

function previewPathFromStatus(value) {
  const text = String(value ?? '');
  const match = text.match(/(?:ready|preview ready):\s+(.+)$/i);
  return match?.[1] ?? '';
}

function previewJsonPathFromHTML(previewPath) {
  if (!previewPath || !/\.html$/i.test(previewPath)) return '';
  return previewPath.replace(/\.html$/i, '.json');
}

function safeArrayLength(value) {
  return Array.isArray(value) ? value.length : 0;
}

function readPreviewGraphSummary(previewPath) {
  const jsonPath = previewJsonPathFromHTML(previewPath);
  if (!jsonPath || !existsSync(jsonPath)) return null;
  try {
    const preview = JSON.parse(readFileSync(jsonPath, 'utf8'));
    return {
      conversations: Number.isFinite(preview.conversations) ? preview.conversations : 0,
      messages: Number.isFinite(preview.messages) ? preview.messages : 0,
      people: safeArrayLength(preview.people_candidates),
      memories: safeArrayLength(preview.memory_candidates),
      emotions: safeArrayLength(preview.emotion_candidates),
      relationships: safeArrayLength(preview.relationship_candidates),
      funFacts: safeArrayLength(preview.fun_fact_candidates),
    };
  } catch {
    return null;
  }
}

function renderPreviewGraphSummary(summary) {
  if (!summary) return '';
  const metrics = [
    [Math.max(1, summary.memories), 'threads'],
    [summary.memories, 'decisions'],
    [summary.relationships, 'open loops'],
    [summary.emotions, 'emotional anchors'],
    [summary.people, 'people found'],
  ];
  return `<div class="graph-summary" aria-label="Continuity summary">
    <strong>Continuity summary</strong>
    <div class="mini-metrics">${metrics.map(([value, label]) => `<span aria-label="${htmlEscape(value)} ${htmlEscape(label)}"><b>${htmlEscape(value)}</b> ${htmlEscape(label)}</span>`).join('')}</div>
    <small>${htmlEscape(summary.conversations)} conversations, ${htmlEscape(summary.messages)} messages scanned</small>
  </div>`;
}

function uniqueLimited(items, limit = 48) {
  const out = [];
  const seen = new Set();
  for (const item of Array.isArray(items) ? items : []) {
    const text = String(item ?? '').trim();
    if (!text || seen.has(text)) continue;
    seen.add(text);
    out.push(text);
    if (out.length >= limit) break;
  }
  return out;
}

function readyPreviewEntries(status) {
  return [
    ['People Graph', status.peopleGraph],
    ['ChatGPT', status.chatgpt],
    ['Claude', status.claude],
    ['Codex', status.codex],
    ['Claude Code', status.claudeCode],
  ]
    .map(([label, value]) => ({ label, htmlPath: previewPathFromStatus(value), jsonPath: previewJsonPathFromHTML(previewPathFromStatus(value)) }))
    .filter((entry) => entry.htmlPath && entry.jsonPath && existsSync(entry.jsonPath));
}

function readPreviewJSON(jsonPath) {
  try {
    return JSON.parse(readFileSync(jsonPath, 'utf8'));
  } catch {
    return null;
  }
}

function buildGalleryPreview(entries) {
  const previews = entries
    .map((entry) => ({ ...entry, preview: readPreviewJSON(entry.jsonPath) }))
    .filter((entry) => entry.preview);
  const combined = {
    source: 'combined',
    conversations: 0,
    messages: 0,
    people_candidates: [],
    review_candidates: [],
    person_profiles: [],
    memory_candidates: [],
    emotion_candidates: [],
    relationship_candidates: [],
    fun_fact_candidates: [],
    redacted_fragments: 0,
  };
  for (const entry of previews) {
    const preview = entry.preview;
    combined.conversations += Number.isFinite(preview.conversations) ? preview.conversations : 0;
    combined.messages += Number.isFinite(preview.messages) ? preview.messages : 0;
    combined.redacted_fragments += Number.isFinite(preview.redacted_fragments) ? preview.redacted_fragments : 0;
    combined.people_candidates.push(...(Array.isArray(preview.people_candidates) ? preview.people_candidates : []));
    combined.review_candidates.push(...(Array.isArray(preview.review_candidates) ? preview.review_candidates : []));
    combined.person_profiles.push(...(Array.isArray(preview.person_profiles) ? preview.person_profiles : []));
    combined.memory_candidates.push(...(Array.isArray(preview.memory_candidates) ? preview.memory_candidates.map((item) => `${entry.label}: ${item}`) : []));
    combined.emotion_candidates.push(...(Array.isArray(preview.emotion_candidates) ? preview.emotion_candidates : []));
    combined.relationship_candidates.push(...(Array.isArray(preview.relationship_candidates) ? preview.relationship_candidates.map((item) => `${entry.label}: ${item}`) : []));
    combined.fun_fact_candidates.push(...(Array.isArray(preview.fun_fact_candidates) ? preview.fun_fact_candidates.map((item) => `${entry.label}: ${item}`) : []));
  }
  combined.people_candidates = uniqueLimited(combined.people_candidates, 72);
  combined.review_candidates = uniqueLimited(combined.review_candidates, 72);
  combined.person_profiles = combined.person_profiles.filter((profile, index, all) => (
    profile?.name && all.findIndex((candidate) => candidate?.name === profile.name) === index
  )).slice(0, 48);
  combined.memory_candidates = uniqueLimited(combined.memory_candidates, 72);
  combined.emotion_candidates = uniqueLimited(combined.emotion_candidates, 32);
  combined.relationship_candidates = uniqueLimited(combined.relationship_candidates, 72);
  combined.fun_fact_candidates = uniqueLimited(combined.fun_fact_candidates, 72);
  return { combined, previews };
}

const GALLERY_REVIEW_ENTITIES = new Set([
  'Benchmark',
  'Accents',
  'Avoid',
  'Background',
  'Cartographer',
  'Chat',
  'Cinema',
  'Conversation',
  'Conversations',
  'Before',
  'Built',
  'Choose',
  'Consulting',
  'Current',
  'Data',
  'Declare',
  'Density',
  'Direction',
  'Dossier',
  'Emo',
  'Empathic',
  'Explore',
  'Export',
  'Fonts',
  'Foreground',
  'Google',
  'Guidance',
  'Harness',
  'Heavy',
  'Let',
  'Memory',
  'Newsreader',
  'Only',
  'Progression',
  'Grok',
  'Kimi',
  'Qwen',
  'Questions',
  'Tide',
  'Paper',
  'Process',
  'Real',
  'Total',
  'Two',
  'Three',
  'Step',
  'Files',
  'Port',
  'Pasted',
  'Running',
  'Worker',
  'Bitwarden',
  'Created',
  'History',
  'Primary',
  'Privacy',
  'Source',
  'Terminal',
  'Ticker',
  'Tweaks',
  'Verifier',
  'Hearth',
  'Автор',
  'Вместо',
  'Вообще',
  'Курс',
  'Лекция',
  'Плюс',
  'Пхукет',
  'Сквозная',
  'Тайланд',
  'Тематика',
  'Аудитория',
]);

const KNOWN_SINGLE_PERSON_NAMES = new Set([
  'Anya',
  'Daria',
  'Dan',
  'Drew',
  'Egor',
  'Elle',
  'Elena',
  'Ilya',
  'Katya',
  'Maria',
  'Mila',
  'Nik',
  'Nikita',
  'Paul',
  'Pavel',
  'Sonya',
  'Vitaly',
  'Анжелика',
  'Аня',
  'Виталий',
  'Вова',
  'Граф',
  'Дан',
  'Даша',
  'Ева',
  'Егор',
  'Игорь',
  'Катя',
  'Лада',
  'Лея',
  'Мама',
  'Мария',
  'Мика',
  'Мила',
  'Ник',
  'Никита',
  'Павел',
  'Папа',
  'Сема',
  'Соня',
  'Федя',
  'Элли',
]);

function isReviewEntityName(name) {
  return GALLERY_REVIEW_ENTITIES.has(String(name ?? '').trim());
}

function stripGallerySourcePrefix(value) {
  return String(value ?? '').replace(/^[A-Z][A-Za-z ]{1,24}:\s+/, '');
}

function formatMentionedRelationship(person, topic) {
  return `${safeText(person, 120)} - mentioned in - ${safeText(topic, 160)}`;
}

function formatRelatedRelationship(left, right) {
  return `${safeText(left, 120)} - related to - ${safeText(right, 120)}`;
}

function parseRelationshipCandidate(candidate) {
  const text = stripGallerySourcePrefix(candidate);
  let match = text.match(/^(.+?)\s+-\s+mentioned in\s+-\s+(.+)$/i);
  if (match) {
    return {
      kind: 'mentioned_in',
      left: safeText(match[1].trim(), 120),
      right: safeText(match[2].trim(), 160),
    };
  }
  match = text.match(/^(.+?)\s+-\s+related to\s+-\s+(.+)$/i);
  if (match) {
    return {
      kind: 'related_to',
      left: safeText(match[1].trim(), 120),
      right: safeText(match[2].trim(), 120),
    };
  }
  if (text.includes('<->')) {
    const [left, right] = text.split('<->').map((part) => safeText(part.trim(), 120));
    return { kind: 'related_to', left, right };
  }
  if (text.includes('->')) {
    const [left, right] = text.split('->').map((part) => safeText(part.trim(), 120));
    return { kind: 'mentioned_in', left, right };
  }
  return null;
}

function normalizeRelationshipForPreview(candidate, canonicalByName = new Map()) {
  const parsed = parseRelationshipCandidate(candidate);
  if (!parsed) {
    return '';
  }
  const left = canonicalByName.get(parsed.left) ?? parsed.left;
  const right = canonicalByName.get(parsed.right) ?? parsed.right;
  if (isSamePersonAlias(left, right)) {
    return '';
  }
  if (parsed.kind === 'related_to') {
    return formatRelatedRelationship(left, right);
  }
  if (parsed.kind === 'mentioned_in') {
    return formatMentionedRelationship(left, right);
  }
  return '';
}

function relationshipTouchesReviewEntity(item) {
  const parsed = parseRelationshipCandidate(item);
  if (!parsed) {
    return false;
  }
  return isReviewEntityName(parsed.left) || isReviewEntityName(parsed.right);
}

function funFactTouchesReviewEntity(item) {
  const text = stripGallerySourcePrefix(item);
  const subject = text.split(' appeared in ')[0];
  return isReviewEntityName(subject);
}

function isLikelyPreviewPersonCandidate(name, count, totalMessages) {
  const trimmed = String(name ?? '').trim();
  if (!trimmed || isReviewEntityName(trimmed)) {
    return false;
  }
  if (!looksLikePreviewPersonName(trimmed)) {
    return false;
  }
  if (trimmed.includes('-')) {
    return false;
  }
  if (totalMessages > 12 && count < 2) {
    return false;
  }
  return true;
}

function looksLikePreviewPersonName(name) {
  const trimmed = safeText(name, 120);
  if (!trimmed || isTechnicalRef(trimmed)) {
    return false;
  }
  if (KNOWN_SINGLE_PERSON_NAMES.has(trimmed)) {
    return true;
  }
  const words = trimmed
    .split(/\s+/)
    .map((word) => word.replace(/[(),]/g, '').trim())
    .filter(Boolean);
  if (words.length >= 2 && words.length <= 4) {
    return words.every((word) => (
      KNOWN_SINGLE_PERSON_NAMES.has(word) ||
      /^[A-Z][a-z]{2,}$/.test(word) ||
      /^[А-ЯЁ][а-яё]{2,}$/.test(word)
    ));
  }
  return false;
}

function shouldShowReviewCandidate(name, count, totalMessages) {
  const trimmed = String(name ?? '').trim();
  if (!trimmed) {
    return false;
  }
  if (isReviewEntityName(trimmed)) {
    return true;
  }
  if (!looksLikePreviewPersonName(trimmed)) {
    return false;
  }
  if (totalMessages <= 12) {
    return false;
  }
  return count > 1;
}

function relationshipAllowedForPreview(candidate, peopleSet, threadSet) {
  if (relationshipTouchesReviewEntity(candidate)) {
    return false;
  }
  const parsed = parseRelationshipCandidate(candidate);
  if (!parsed) {
    return false;
  }
  if (isSamePersonAlias(parsed.left, parsed.right)) {
    return false;
  }
  if (parsed.kind === 'related_to') {
    return peopleSet.has(parsed.left) && peopleSet.has(parsed.right);
  }
  if (parsed.kind === 'mentioned_in') {
    return peopleSet.has(parsed.left) && !isReviewEntityName(parsed.right) && (peopleSet.has(parsed.right) || threadSet.has(parsed.right));
  }
  return false;
}

function splitGalleryPersonCandidates(people) {
  const confident = [];
  const review = [];
  for (const name of uniqueLimited(people, 96)) {
    if (isReviewEntityName(name)) {
      review.push(name);
    } else {
      confident.push(name);
    }
  }
  return { confident, review };
}

function renderGallerySourceCards(previews) {
  if (previews.length === 0) {
    return '<p class="empty">No preview sources are ready yet.</p>';
  }
  return `<div class="source-grid">${previews.map(({ label, htmlPath, preview }) => {
    const sourceTitle = preview.source_kind === 'curated_people_graph' ? 'Curated people graph' : label;
    const sourceSubtitle = preview.source_kind === 'curated_people_graph' ? 'Real people graph' : label;
    return `<article class="source-card filter-item" data-search="${htmlEscape(label)} ${htmlEscape(sourceTitle)} ${htmlEscape((preview.people_candidates ?? []).join(' '))}">
    <div class="source-label">${htmlEscape(label)}</div>
    <strong>${htmlEscape(sourceTitle)}</strong>
    <span>${htmlEscape(sourceSubtitle)} · ${htmlEscape(preview.people_candidates?.length ?? 0)} people candidates</span>
    <span>${htmlEscape(preview.memory_candidates?.length ?? 0)} decisions · ${htmlEscape(preview.emotion_candidates?.length ?? 0)} emotional anchors · ${htmlEscape(preview.relationship_candidates?.length ?? 0)} open loops</span>
    <a class="button" href="${htmlEscape(htmlPath)}">Open source preview</a>
  </article>`;
  }).join('')}</div>`;
}

function renderMemoryGalleryHTML(status) {
  const generatedAt = new Date().toISOString();
  const { combined, previews } = buildGalleryPreview(readyPreviewEntries(status));
  const galleryPeople = splitGalleryPersonCandidates(combined.people_candidates);
  const galleryReviewCandidates = uniqueLimited([
    ...galleryPeople.review,
    ...(Array.isArray(combined.review_candidates) ? combined.review_candidates : []),
  ], 96);
  const visibleRelationships = combined.relationship_candidates.filter((item) => !relationshipTouchesReviewEntity(item));
  const visibleFunFacts = combined.fun_fact_candidates.filter((item) => !funFactTouchesReviewEntity(item));
  const profilePreview = {
    ...combined,
    people_candidates: galleryPeople.confident,
    relationship_candidates: visibleRelationships,
    fun_fact_candidates: visibleFunFacts,
  };
  return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Pulse Thread Gallery</title>
  <style>
    :root { color-scheme: light; --bg:#fbf7f4; --ink:#463b3d; --muted:#8f8180; --line:rgba(136,116,112,.14); --panel:rgba(255,255,255,.54); --rail:rgba(255,255,255,.34); --rail-muted:#9a8c89; --accent:#d99a8f; --accent-soft:#f7cbc2; --good:#8bb79f; --soft:rgba(255,255,255,.46); --pastel-a:#f8d9d0; --pastel-b:#dceaf7; --pastel-c:#e7e0fa; --pastel-d:#e4f2df; }
    * { box-sizing:border-box; }
    body { margin:0; min-height:100vh; font-family:ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; color:var(--ink); background:
      radial-gradient(circle at 8% 8%, rgba(248,217,208,.92), transparent 32%),
      radial-gradient(circle at 82% 12%, rgba(220,234,247,.86), transparent 30%),
      radial-gradient(circle at 68% 72%, rgba(231,224,250,.72), transparent 36%),
      linear-gradient(145deg,#fffaf7 0%,#f6fbff 48%,#fff7fb 100%); }
    .workbench { min-height:100vh; display:grid; grid-template-columns:260px minmax(0,1fr); }
    .rail { position:sticky; top:0; height:100vh; padding:26px 22px; background:var(--rail); color:var(--ink); display:flex; flex-direction:column; gap:26px; border-right:1px solid rgba(255,255,255,.55); backdrop-filter:blur(24px) saturate(1.35); -webkit-backdrop-filter:blur(24px) saturate(1.35); }
    .brand { display:grid; gap:8px; }
    .brand b { font-size:22px; letter-spacing:0; font-weight:680; color:#5a4d51; }
    .brand span, .rail small { color:var(--rail-muted); line-height:1.45; }
    .rail-nav { display:grid; gap:8px; }
    .rail-nav a, .rail-pill { color:var(--ink); text-decoration:none; border:1px solid rgba(255,255,255,.58); border-radius:14px; padding:10px 12px; background:rgba(255,255,255,.42); box-shadow:0 10px 26px rgba(115,92,84,.08); backdrop-filter:blur(16px); -webkit-backdrop-filter:blur(16px); }
    .rail-pill { color:var(--rail-muted); }
    .workspace { min-width:0; padding:34px clamp(18px,4vw,54px) 54px; }
    header { display:grid; grid-template-columns:minmax(0,1fr) minmax(180px,260px); gap:24px; align-items:end; padding-bottom:24px; border-bottom:1px solid var(--line); }
    h1 { margin:0; font-size:46px; line-height:1.08; letter-spacing:0; font-weight:680; color:#4a3f42; max-width:800px; }
    h2 { margin:0 0 12px; font-size:18px; letter-spacing:0; }
    .subhead { margin:14px 0 0; max-width:760px; color:var(--muted); font-size:16px; line-height:1.5; }
    .glass-card, .stamp, section, .source-card, .person-card, .metric { background:var(--panel); border:1px solid rgba(255,255,255,.62); border-radius:16px; box-shadow:0 18px 50px rgba(125,102,94,.12), inset 0 1px 0 rgba(255,255,255,.74); backdrop-filter:blur(22px) saturate(1.24); -webkit-backdrop-filter:blur(22px) saturate(1.24); }
    .stamp { padding:18px; }
    .stamp strong { display:block; font-size:36px; letter-spacing:0; line-height:1.05; }
    .stamp span { display:block; margin-top:8px; color:var(--muted); font-size:13px; line-height:1.35; }
    .metrics { display:grid; grid-template-columns:repeat(auto-fit,minmax(138px,1fr)); gap:12px; margin:18px 0; }
    .metric { min-height:96px; padding:16px; position:relative; overflow:hidden; }
    .metric::before { content:""; position:absolute; inset:0 auto 0 0; width:4px; background:linear-gradient(180deg,var(--pastel-a),var(--pastel-c)); }
    .metric b { display:block; font-size:30px; line-height:1.05; letter-spacing:0; font-weight:680; }
    .metric span { display:block; margin-top:8px; color:var(--muted); font-size:13px; }
    section { padding:18px; margin-top:16px; }
    .source-grid, .profile-grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(250px,1fr)); gap:12px; }
    .source-card { padding:16px; }
    .source-label { color:var(--muted); font-size:12px; text-transform:uppercase; letter-spacing:.12em; margin-bottom:8px; }
    .source-card strong { display:block; font-size:22px; letter-spacing:0; font-weight:680; }
    .source-card span { display:block; margin-top:6px; color:var(--muted); line-height:1.4; }
    .button, button { display:inline-flex; align-items:center; justify-content:center; min-height:40px; margin-top:12px; padding:9px 13px; border:1px solid rgba(217,154,143,.34); border-radius:999px; background:linear-gradient(135deg,rgba(255,255,255,.9),rgba(247,203,194,.58)); color:#6d5653; text-decoration:none; cursor:pointer; font:inherit; white-space:nowrap; box-shadow:0 10px 24px rgba(160,126,118,.11); }
    .button:hover, button:hover { transform:translateY(-1px); }
    .filter-row { display:grid; grid-template-columns:minmax(0,1fr) auto; gap:10px; }
    input { min-height:42px; width:100%; border:1px solid rgba(255,255,255,.68); border-radius:999px; padding:9px 14px; font:inherit; background:rgba(255,255,255,.62); color:var(--ink); backdrop-filter:blur(14px); -webkit-backdrop-filter:blur(14px); }
    #gallery-filter-status { margin-top:10px; color:var(--muted); font-size:14px; }
    .person-card { padding:16px; border-top:1px solid rgba(217,154,143,.36); }
    .person-head { display:flex; justify-content:space-between; gap:10px; align-items:baseline; border-bottom:1px solid var(--line); padding-bottom:10px; margin-bottom:12px; }
    .person-head h3 { margin:0; font-size:22px; letter-spacing:0; font-weight:680; }
    .person-head span { color:var(--muted); font-size:13px; white-space:nowrap; }
    .mini-block + .mini-block { margin-top:13px; }
    .mini-block h4 { margin:0 0 7px; color:var(--muted); font-size:12px; letter-spacing:.12em; text-transform:uppercase; }
    .grid { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:12px; }
    .wide { grid-column:span 2; }
    ul { list-style:none; padding:0; margin:0; display:grid; gap:8px; }
    li { border:1px solid rgba(255,255,255,.62); padding:10px 11px; background:rgba(255,255,255,.46); border-radius:12px; line-height:1.35; overflow-wrap:anywhere; }
    .empty { color:var(--muted); }
    .trust { border-color:rgba(217,154,143,.24); background:rgba(255,246,242,.56); color:#6d5653; }
    .graph-stage { background:linear-gradient(145deg,rgba(255,255,255,.64),rgba(232,240,251,.48)); color:var(--ink); border-color:rgba(255,255,255,.72); }
    .graph-stage .subhead, .graph-stage .empty { color:var(--muted); }
    .graph-stage .grid section { background:rgba(255,255,255,.44); border-color:rgba(255,255,255,.62); color:var(--ink); box-shadow:inset 0 1px 0 rgba(255,255,255,.72); }
    .graph-stage li { background:rgba(255,255,255,.42); border-color:rgba(255,255,255,.62); color:var(--ink); }
    .graph-stage ul { max-height:380px; overflow:auto; padding-right:4px; }
    #review-queue ul { max-height:260px; overflow:auto; padding-right:4px; }
    @media (max-width:900px) { .workbench { grid-template-columns:1fr; } .rail { position:relative; height:auto; } header, .grid, .filter-row { grid-template-columns:1fr; } .wide { grid-column:auto; } h1 { font-size:34px; line-height:1.08; } }
  </style>
</head>
<body data-design="pulse-glass-v3">
  <main class="workbench">
    <aside class="rail" aria-label="Pulse memory navigation">
      <div class="brand">
        <b>Pulse Import</b>
        <span>Thread gallery before continuity import.</span>
      </div>
      <nav class="rail-nav">
        <a href="#memory-graph">Thread map</a>
        <a href="#person-profiles">People found</a>
        <a href="#review-queue">Needs your decision</a>
        <a href="#sources">Sources</a>
      </nav>
      <div class="rail-pill">Review first. Import later.</div>
      <small>No raw transcript is written by this preview.</small>
    </aside>
    <div class="workspace">
    <header>
      <div>
        <h1>Pulse Thread Gallery</h1>
        <p class="subhead">What Pulse can turn into continuity after review. Start with threads, decisions, open loops, and what will be injected next.</p>
      </div>
      <div class="stamp">
        <strong>${htmlEscape(previews.length)}</strong>
        <span>ready sources · Generated ${htmlEscape(generatedAt)}</span>
      </div>
    </header>
    <div class="metrics" aria-label="Pulse memory gallery metrics">
      <div class="metric"><b>${htmlEscape(previews.length)}</b><span>sources ready</span></div>
      <div class="metric"><b>${htmlEscape(Math.max(1, combined.memory_candidates.length))}</b><span>threads</span></div>
      <div class="metric"><b>${htmlEscape(combined.memory_candidates.length)}</b><span>decisions</span></div>
      <div class="metric"><b>${htmlEscape(visibleRelationships.length)}</b><span>open loops</span></div>
      <div class="metric"><b>${htmlEscape(combined.emotion_candidates.length)}</b><span>emotional anchors</span></div>
      <div class="metric"><b>${htmlEscape(galleryPeople.confident.length)}</b><span>people found</span></div>
      <div class="metric"><b>${htmlEscape(galleryReviewCandidates.length)}</b><span>needs decision</span></div>
    </div>
    <section id="sources">
      <h2>Sources</h2>
      ${renderGallerySourceCards(previews)}
    </section>
    <section>
      <h2>Filter gallery</h2>
      <div class="filter-row">
        <input id="gallery-filter" placeholder="Type a thread, person, decision, or review item">
        <button id="gallery-filter-clear">Clear</button>
      </div>
      <div id="gallery-filter-status">Showing all gallery candidates.</div>
    </section>
    <section id="person-profiles">
      <h2>People found in reviewed sources</h2>
      ${renderPreviewPersonCards(profilePreview)}
    </section>
    <section id="review-queue">
      <h2>Needs your decision</h2>
      <p class="empty"><strong>Review before import:</strong> these may be models, tools, projects, or ambiguous entities. Pulse keeps them out of people/context memory until you decide.</p>
      ${previewList(galleryReviewCandidates, 'No ambiguous people candidates yet.')}
    </section>
    <section class="graph-stage" id="memory-graph">
      <h2>Thread map</h2>
      <p class="subhead">What Pulse understood across ready sources, expressed as continuity objects instead of parser categories.</p>
      <div class="grid">
        <section><h2>Threads</h2>${previewList(combined.memory_candidates.map(previewThreadTitle))}</section>
        <section class="wide"><h2>Decisions</h2>${previewList(combined.memory_candidates.map(continuityPreviewText))}</section>
        <section><h2>Open loops</h2>${previewList(visibleRelationships.map(continuityPreviewText), 'No open loops yet.')}</section>
        <section><h2>Emotional anchors</h2>${previewList(combined.emotion_candidates)}</section>
        <section><h2>People found</h2>${previewList(galleryPeople.confident)}</section>
      </div>
    </section>
    <section class="trust">
      <h2>Nothing imported yet</h2>
      <p>This gallery is local preview only. Pulse writes structured continuity only after you open a source preview and run its explicit import command.</p>
    </section>
    </div>
  </main>
  <script>
    const filter = document.getElementById("gallery-filter");
    const clear = document.getElementById("gallery-filter-clear");
    const status = document.getElementById("gallery-filter-status");
    const items = Array.from(document.querySelectorAll(".filter-item"));
    function applyFilter() {
      const query = filter.value.trim().toLowerCase();
      let shown = 0;
      for (const item of items) {
        const text = (item.getAttribute("data-search") || item.textContent || "").toLowerCase();
        const visible = !query || text.includes(query);
        item.hidden = !visible;
        if (visible) shown += 1;
      }
      status.textContent = query ? "Showing " + shown + " matching gallery candidates." : "Showing all gallery candidates.";
    }
    filter.addEventListener("input", applyFilter);
    clear.addEventListener("click", () => { filter.value = ""; applyFilter(); filter.focus(); });
  </script>
</body>
</html>
`;
}

function renderStatusCard(title, value) {
  const kind = statusBadge(value);
  const previewPath = previewPathFromStatus(value);
  const graphSummary = renderPreviewGraphSummary(readPreviewGraphSummary(previewPath));
  const previewLink = previewPath
    ? `<a class="button" href="${htmlEscape(previewPath)}">Open preview</a>`
    : '';
  return `<article class="status-card ${kind}">
    <div class="status-label">${htmlEscape(title)}</div>
    <h2>${htmlEscape(statusTitle(value))}</h2>
    <p>${htmlEscape(value)}</p>
    ${graphSummary}
    ${previewLink}
  </article>`;
}

function renderMigrationStatusHTML(status) {
  const chatgptCommand = `pulse migrate wait-latest chatgpt --downloads ${shellArg(resolve(status.downloadsDir))} --html ${shellArg(join(status.outDir, 'pulse-chatgpt-preview.html'))} --out ${shellArg(join(status.outDir, 'pulse-chatgpt-preview.json'))} --open`;
  const claudeCommand = `pulse migrate wait-latest claude --downloads ${shellArg(resolve(status.downloadsDir))} --html ${shellArg(join(status.outDir, 'pulse-claude-preview.html'))} --out ${shellArg(join(status.outDir, 'pulse-claude-preview.json'))} --open`;
  const liveWaitCommand = `pulse migrate start --dir ${shellArg(resolve(status.outDir))} --downloads ${shellArg(resolve(status.downloadsDir))} --open --watch`;
  const refreshMeta = status.autoRefresh ? '  <meta http-equiv="refresh" content="5">\n' : '';
  return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
${refreshMeta}  <meta name="referrer" content="no-referrer">
  <title>Pulse Import Status</title>
  <style>
    :root { color-scheme: light; --bg:#fbf7f4; --ink:#463b3d; --muted:#8f8180; --line:rgba(136,116,112,.14); --panel:rgba(255,255,255,.54); --rail:rgba(255,255,255,.34); --rail-muted:#9a8c89; --accent:#d99a8f; --accent-soft:#f7cbc2; --good:#8bb79f; --warn:#d6a95f; --soft:rgba(255,255,255,.46); --pastel-a:#f8d9d0; --pastel-b:#dceaf7; --pastel-c:#e7e0fa; --pastel-d:#e4f2df; }
    * { box-sizing: border-box; }
    body { margin:0; min-height:100vh; font-family: ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; color:var(--ink); background:
      radial-gradient(circle at 8% 8%, rgba(248,217,208,.92), transparent 32%),
      radial-gradient(circle at 82% 12%, rgba(220,234,247,.86), transparent 30%),
      radial-gradient(circle at 68% 72%, rgba(231,224,250,.72), transparent 36%),
      linear-gradient(145deg,#fffaf7 0%,#f6fbff 48%,#fff7fb 100%); }
    .workbench { min-height:100vh; display:grid; grid-template-columns:260px minmax(0,1fr); }
    .rail { position:sticky; top:0; height:100vh; padding:26px 22px; background:var(--rail); color:var(--ink); display:flex; flex-direction:column; gap:26px; border-right:1px solid rgba(255,255,255,.55); backdrop-filter:blur(24px) saturate(1.35); -webkit-backdrop-filter:blur(24px) saturate(1.35); }
    .brand { display:grid; gap:8px; }
    .brand b { font-size:22px; letter-spacing:0; font-weight:680; color:#5a4d51; }
    .brand span, .rail small { color:var(--rail-muted); line-height:1.45; }
    .rail-nav { display:grid; gap:8px; }
    .rail-nav a, .rail-pill { color:var(--ink); text-decoration:none; border:1px solid rgba(255,255,255,.58); border-radius:14px; padding:10px 12px; background:rgba(255,255,255,.42); box-shadow:0 10px 26px rgba(115,92,84,.08); backdrop-filter:blur(16px); -webkit-backdrop-filter:blur(16px); }
    .rail-pill { color:var(--rail-muted); }
    .workspace { min-width:0; padding:34px clamp(18px,4vw,54px) 54px; }
    header { display:grid; grid-template-columns:minmax(0,1fr) minmax(220px,340px); gap:24px; align-items:end; padding-bottom:24px; border-bottom:1px solid var(--line); }
    h1 { margin:0; font-size:46px; line-height:1.08; letter-spacing:0; font-weight:680; color:#4a3f42; max-width:800px; }
    h2 { margin:0 0 12px; font-size:18px; letter-spacing:0; }
    p { margin:0; line-height:1.48; }
    .meta { color:var(--muted); line-height:1.45; }
    .grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(230px,1fr)); gap:12px; margin:16px 0; }
    .glass-card, section, .status-card { background:var(--panel); border:1px solid rgba(255,255,255,.62); border-radius:16px; padding:18px; box-shadow:0 18px 50px rgba(125,102,94,.12), inset 0 1px 0 rgba(255,255,255,.74); backdrop-filter:blur(22px) saturate(1.24); -webkit-backdrop-filter:blur(22px) saturate(1.24); }
    section { margin-top:16px; }
    .status-card { min-height:182px; position:relative; overflow:hidden; }
    .status-card::before { content:""; position:absolute; inset:0 auto 0 0; width:4px; background:linear-gradient(180deg,var(--pastel-a),var(--pastel-c)); }
    .status-card.ready::before { background:var(--good); }
    .status-card.waiting::before { background:var(--warn); }
    .status-label { color:var(--muted); font-size:12px; text-transform:uppercase; letter-spacing:.12em; }
    .button, button { display:inline-flex; align-items:center; justify-content:center; min-height:40px; margin-top:12px; padding:9px 13px; border:1px solid rgba(217,154,143,.34); border-radius:999px; background:linear-gradient(135deg,rgba(255,255,255,.9),rgba(247,203,194,.58)); color:#6d5653; text-decoration:none; cursor:pointer; font:inherit; white-space:nowrap; box-shadow:0 10px 24px rgba(160,126,118,.11); }
    .button:hover, button:hover { transform:translateY(-1px); }
    .graph-summary { margin-top:12px; padding:12px; border:1px solid var(--line); border-radius:14px; background:var(--soft); }
    .graph-summary strong, .graph-summary small { display:block; }
    .graph-summary small { margin-top:8px; color:var(--muted); }
    .mini-metrics { display:flex; flex-wrap:wrap; gap:6px; margin-top:8px; }
    .mini-metrics span { display:inline-flex; gap:4px; align-items:baseline; padding:5px 8px; border-radius:999px; background:rgba(255,255,255,.56); border:1px solid rgba(255,255,255,.62); font-size:13px; }
    .now { border-color:rgba(255,255,255,.72); background:linear-gradient(145deg,rgba(255,255,255,.66),rgba(232,240,251,.5)); color:var(--ink); }
    .now p { color:var(--muted); max-width:760px; }
    .now-grid { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:12px; margin-top:16px; }
    .now-step { border:1px solid rgba(255,255,255,.62); border-radius:16px; padding:16px; background:rgba(255,255,255,.42); box-shadow:inset 0 1px 0 rgba(255,255,255,.72); }
    .now-step strong { display:block; font-size:17px; margin-bottom:6px; }
    .now-step span { color:var(--muted); display:block; line-height:1.4; }
    .now .button, .now button { border-color:rgba(232,137,119,.42); background:linear-gradient(135deg,rgba(255,255,255,.9),rgba(248,217,208,.72)); color:#5f4039; }
    .command { display:grid; grid-template-columns:minmax(0,1fr) auto; gap:8px; align-items:start; margin-top:10px; }
    .now-step .command { grid-template-columns:1fr; }
    .now-step .command .button, .now-step .command button { justify-self:start; }
    code { display:block; padding:11px 12px; border:1px solid rgba(255,255,255,.62); border-radius:12px; background:rgba(255,255,255,.46); color:#4b5558; overflow-wrap:anywhere; font-family:ui-monospace, SFMono-Regular, Menlo, monospace; font-size:12px; line-height:1.45; }
    .now code { border-color:rgba(255,255,255,.62); background:rgba(255,255,255,.46); color:#4b5558; }
    .trust { border-color:rgba(217,154,143,.24); background:rgba(255,246,242,.56); color:#6d5653; }
    ul { margin:0; padding-left:18px; }
    li + li { margin-top:6px; }
    @media (max-width:900px) { .workbench { grid-template-columns:1fr; } .rail { position:relative; height:auto; } header, .now-grid, .command { grid-template-columns:1fr; } h1 { font-size:34px; line-height:1.08; } }
  </style>
</head>
<body data-design="pulse-glass-v3">
  <main class="workbench">
    <aside class="rail" aria-label="Pulse migration navigation">
      <div class="brand">
        <b>Pulse Import</b>
        <span>Archive migration without blind import.</span>
      </div>
      <nav class="rail-nav">
        <a href="#archive-request">Archive request</a>
        <a href="#local-sources">Local sources</a>
        <a href="${htmlEscape(status.galleryHTML ?? '#')}">Thread gallery</a>
      </nav>
      <div class="rail-pill">Nothing imported until graph approval.</div>
      <small>Local preview only. Raw chat text is not written by this page.</small>
    </aside>
    <div class="workspace">
    <header>
      <div>
        <h1>Pulse Import Status</h1>
        <p class="meta">One place to see what is ready, what still needs a human click, and what Pulse will import only after review.</p>
      </div>
      <div class="meta">Generated ${htmlEscape(new Date().toISOString())}<br>Output: ${htmlEscape(status.outDir)}</div>
    </header>
    <section class="now" id="archive-request">
      <h2>What to do right now</h2>
      <p>Click only these two host buttons if you want ChatGPT and Claude archives. Pulse has already prepared the local previews and will not import anything until you approve the graph.</p>
      <div class="now-grid">
        <div class="now-step">
          <strong>ChatGPT request page</strong>
          <span>Open Data Controls, then press Request archive inside ChatGPT.</span>
          <a class="button" href="https://chatgpt.com/#settings/DataControls" target="_blank" rel="noreferrer">Open ChatGPT Data Controls</a>
        </div>
        <div class="now-step">
          <strong>Claude export page</strong>
          <span>Open Privacy settings, then press Export data inside Claude.</span>
          <a class="button" href="https://claude.ai/settings/privacy" target="_blank" rel="noreferrer">Open Claude Privacy</a>
        </div>
        <div class="now-step">
          <strong>Ready to browse now</strong>
          <span>Codex and Claude Code are local, so their thread preview can be inspected immediately.</span>
          ${status.galleryHTML ? `<a class="button" href="${htmlEscape(status.galleryHTML)}">Preview thread gallery</a>` : ''}
        </div>
        <div class="now-step">
          <strong>After you clicked</strong>
          <span>When the zip appears in Downloads, Pulse will show candidate threads, decisions, open loops, and review items before import.</span>
          <div class="command"><code>${htmlEscape(liveWaitCommand)}</code><button class="copy" data-copy="${htmlEscape(liveWaitCommand)}">Start live waiting</button></div>
        </div>
      </div>
    </section>
    <section>
      <h2>Human click</h2>
      <ul>
        <li>ChatGPT: click Request archive in Data Controls.</li>
        <li>Claude: click Export data in Privacy settings.</li>
      </ul>
      <p>
        <a class="button" href="https://chatgpt.com/#settings/DataControls" target="_blank" rel="noreferrer">Open ChatGPT Data Controls</a>
        <a class="button" href="https://claude.ai/settings/privacy" target="_blank" rel="noreferrer">Open Claude Privacy</a>
      </p>
    </section>
    <div class="grid" id="local-sources">
      ${renderStatusCard('ChatGPT archive', status.chatgpt)}
      ${renderStatusCard('Claude archive', status.claude)}
      ${status.peopleGraph ? renderStatusCard('People Graph preview', status.peopleGraph) : ''}
      ${renderStatusCard('Codex preview', status.codex)}
      ${renderStatusCard('Claude Code preview', status.claudeCode)}
    </div>
    <section>
      <h2>Open first</h2>
      <p><a class="button" href="${htmlEscape(status.conciergeHTML)}">Open concierge</a> <a class="button" href="${htmlEscape(status.conciergeBrief)}">Open brief</a>${status.galleryHTML ? ` <a class="button" href="${htmlEscape(status.galleryHTML)}">Preview thread gallery</a>` : ''}</p>
    </section>
    <section>
      <h2>Wait for remote archives</h2>
      <div class="command"><code>${htmlEscape(chatgptCommand)}</code><button class="copy" data-copy="${htmlEscape(chatgptCommand)}">Copy command</button></div>
      <div class="command"><code>${htmlEscape(claudeCommand)}</code><button class="copy" data-copy="${htmlEscape(claudeCommand)}">Copy command</button></div>
    </section>
    <section class="trust">
      <h2>Nothing imported</h2>
      <p>Pulse has only built preview files. Review candidate threads, decisions, open loops, people found, and review cards before running the explicit commit command shown inside a preview.</p>
    </section>
    <section>
      <h2>Paper boundary</h2>
      <p>This migration/viewer flow is a product extension, not an evaluated paper result. It can be mentioned as Future Work or productization, but it does not expand the paper's evaluated retrieval claims.</p>
    </section>
    </div>
  </main>
  <script>
    async function copyCommand(button) {
      const command = button.getAttribute("data-copy") || "";
      try {
        await navigator.clipboard.writeText(command);
        button.textContent = "Copied";
      } catch {
        const textarea = document.createElement("textarea");
        textarea.value = command;
        document.body.appendChild(textarea);
        textarea.select();
        document.execCommand("copy");
        textarea.remove();
        button.textContent = "Copied";
      }
      setTimeout(() => { button.textContent = "Copy command"; }, 1400);
    }
    for (const button of document.querySelectorAll("[data-copy]")) {
      button.addEventListener("click", () => copyCommand(button));
    }
  </script>
</body>
</html>
`;
}

function writeMigrationStatus(statusPath, status) {
  if (status.galleryHTML) {
    writeFileSync(status.galleryHTML, renderMemoryGalleryHTML(status), { mode: 0o600 });
    console.log(`[pulse] memory gallery: ${status.galleryHTML}`);
  }
  writeFileSync(statusPath, renderMigrationStatus(status), { mode: 0o600 });
  if (status.statusHTML) {
    writeFileSync(status.statusHTML, renderMigrationStatusHTML(status), { mode: 0o600 });
    console.log(`[pulse] migration status page: ${status.statusHTML}`);
  }
  console.log(`[pulse] migration status: ${statusPath}`);
}

async function runMigrationStart(rest) {
  const outDir = resolve(restArg(rest, '--dir') ?? 'pulse-migrate');
  mkdirSync(outDir, { recursive: true, mode: 0o700 });
  const shouldOpen = rest.includes('--open');
  const openRest = shouldOpen ? ['--open'] : [];
  const downloadsDir = restArg(rest, '--downloads') ?? join(homedir(), 'Downloads');
  const peopleGraphPath = restArg(rest, '--people-graph');
  const timeoutMs = positiveIntArg(rest, '--timeout-ms', 15 * 60 * 1000);
  const intervalMs = positiveIntArg(rest, '--interval-ms', 2000);
  const conciergeHTML = join(outDir, 'pulse-migrate-concierge.html');
  const conciergeBrief = join(outDir, 'pulse-migrate-brief.md');
  const statusPath = join(outDir, 'pulse-migrate-status.md');
  const statusHTML = join(outDir, 'pulse-migrate-status.html');
  const galleryHTML = join(outDir, 'pulse-memory-gallery.html');
  const migrationStatus = {
    outDir,
    downloadsDir,
    autoRefresh: rest.includes('--watch'),
    conciergeHTML,
    conciergeBrief,
    statusHTML,
    galleryHTML,
    chatgpt: 'waiting for Request archive',
    claude: 'waiting for Export data',
    peopleGraph: '',
    codex: 'not found',
    claudeCode: 'not found',
  };

  runMigrationConcierge([
    '--html',
    conciergeHTML,
    '--brief',
    conciergeBrief,
    ...openRest,
  ]);

  for (const source of ['chatgpt', 'claude']) {
    const info = remoteArchiveInfo(source);
    if (shouldOpen) {
      openExternalURL(info.url);
    }
    console.log(`[pulse] ${info.label}: click "${info.buttonText}" in the opened browser page.`);
  }

  if (peopleGraphPath) {
    console.log('[pulse] People Graph local graph selected');
    console.log(`[pulse] Local graph: ${resolve(peopleGraphPath)}`);
    emitPeopleGraphPreview(peopleGraphPath, [
      '--html',
      join(outDir, 'pulse-people-graph-preview.html'),
      '--out',
      join(outDir, 'pulse-people-graph-preview.json'),
      ...openRest,
    ]);
    migrationStatus.peopleGraph = `ready: ${join(outDir, 'pulse-people-graph-preview.html')}`;
  }

  for (const source of ['codex', 'claude-code']) {
    const info = localHistoryInfo(source);
    if (!existsSync(info.path)) {
      console.log(`[pulse] ${info.label} local history not found at ${info.displayPath}; skipping local preview.`);
      continue;
    }
    requestLocalHistoryPreview(source, [
      '--html',
      join(outDir, `pulse-${source}-preview.html`),
      '--out',
      join(outDir, `pulse-${source}-preview.json`),
      ...openRest,
    ]);
    if (source === 'codex') {
      migrationStatus.codex = `ready: ${join(outDir, 'pulse-codex-preview.html')}`;
    } else {
      migrationStatus.claudeCode = `ready: ${join(outDir, 'pulse-claude-code-preview.html')}`;
    }
  }

  writeMigrationStatus(statusPath, migrationStatus);
  if (shouldOpen) {
    openExternalURL(statusHTML);
  }

  console.log(`
[pulse] Pulse archive migration started
──────────────────────────────────────

Open files:
  ${conciergeHTML}
  ${conciergeBrief}
  ${statusPath}
  ${statusHTML}
  ${galleryHTML}

Human click:
  ChatGPT: click "Request archive"
  Claude: click "Export data"

When the zips arrive:
  pulse migrate wait-latest chatgpt --downloads ${shellArg(resolve(downloadsDir))} --html ${shellArg(join(outDir, 'pulse-chatgpt-preview.html'))} --out ${shellArg(join(outDir, 'pulse-chatgpt-preview.json'))} --open
  pulse migrate wait-latest claude --downloads ${shellArg(resolve(downloadsDir))} --html ${shellArg(join(outDir, 'pulse-claude-preview.html'))} --out ${shellArg(join(outDir, 'pulse-claude-preview.json'))} --open

Nothing has been imported yet. Review preview pages first, then use the explicit commit command.
`);

  if (!rest.includes('--watch')) {
    return;
  }

  console.log(`[pulse] watching for ChatGPT and Claude archives in ${resolve(downloadsDir)} (${timeoutMs}ms timeout each, in parallel)`);
  await Promise.all(['chatgpt', 'claude'].map(async (source) => {
    try {
      const latest = await waitForLatestMigrationArchive(source, downloadsDir, timeoutMs, intervalMs);
      console.log(`[pulse] latest ${source} archive: ${latest}`);
      emitMigrationPreview(latest, [
        '--html',
        join(outDir, `pulse-${source}-preview.html`),
        '--out',
        join(outDir, `pulse-${source}-preview.json`),
        '--source',
        source,
        ...openRest,
      ]);
      if (source === 'chatgpt') {
        migrationStatus.chatgpt = `preview ready: ${join(outDir, 'pulse-chatgpt-preview.html')}`;
      } else {
        migrationStatus.claude = `preview ready: ${join(outDir, 'pulse-claude-preview.html')}`;
      }
      writeMigrationStatus(statusPath, migrationStatus);
    } catch (err) {
      const label = source === 'chatgpt' ? 'ChatGPT' : 'Claude';
      if (err instanceof Error && err.message.includes('without seeing a matching archive')) {
        console.log(`[pulse] ${label} archive not ready yet. Keep the export email/download open, then run wait-latest later.`);
        if (source === 'chatgpt') {
          migrationStatus.chatgpt = 'not ready yet';
        } else {
          migrationStatus.claude = 'not ready yet';
        }
        writeMigrationStatus(statusPath, migrationStatus);
        return;
      }
      throw err;
    }
  }));
}

function validMigrationHost(host) {
  return [
    'chatgpt',
    'claude',
    'codex',
    'claude-code',
    'gemini-cli',
    'cursor',
    'langchain',
    'crewai',
  ].includes(host);
}

function migrationPrivacyTier(value) {
  const tier = String(value ?? 'private').trim();
  if (tier === 'normal' || tier === 'sensitive' || tier === 'private') {
    return tier;
  }
  throw new Error('pulse migrate commit --privacy must be normal, sensitive, or private');
}

function semanticClientID(kind, name, index) {
  const base = slug(name);
  return `${kind}:${base === 'default' ? index : base}`;
}

function previewArray(preview, key) {
  const value = preview?.[key];
  if (!Array.isArray(value)) {
    return [];
  }
  return value.map((item) => safeText(item, 240)).filter(Boolean);
}

const PERSON_ALIAS_GROUPS = [
  ['Nik', 'Nikita', 'Ник', 'Никита'],
  ['Anya', 'Аня'],
  ['Elle', 'Элли', 'Эли'],
  ['Sonya', 'Соня'],
  ['Fedya', 'Fedor', 'Федя', 'Фёдор'],
];

function aliasKey(value) {
  return safeText(value, 120)
    .toLowerCase()
    .replace(/ё/g, 'е')
    .replace(/[^\p{L}\p{N}]+/gu, '');
}

function aliasGroupForPerson(name) {
  const key = aliasKey(name);
  if (!key) {
    return [safeText(name, 120)];
  }
  return PERSON_ALIAS_GROUPS.find((group) => group.some((item) => aliasKey(item) === key)) ?? [safeText(name, 120)];
}

function aliasGroupKeyForPerson(name) {
  return aliasGroupForPerson(name).map(aliasKey).filter(Boolean).sort().join('|');
}

function isSamePersonAlias(left, right) {
  const leftKey = aliasKey(left);
  const rightKey = aliasKey(right);
  if (!leftKey || !rightKey) {
    return false;
  }
  if (leftKey === rightKey) {
    return true;
  }
  const leftGroup = aliasGroupKeyForPerson(left);
  const rightGroup = aliasGroupKeyForPerson(right);
  return Boolean(leftGroup && rightGroup && leftGroup === rightGroup);
}

function groupImportPeople(people) {
  const canonicalByGroup = new Map();
  const aliasesByCanonical = new Map();
  const canonicalByName = new Map();
  const groupedPeople = [];

  for (const person of people) {
    const name = safeText(person, 120);
    if (!name) {
      continue;
    }
    const groupKey = aliasGroupForPerson(name).map(aliasKey).filter(Boolean).sort().join('|') || aliasKey(name);
    let canonical = canonicalByGroup.get(groupKey);
    if (!canonical) {
      canonical = name;
      canonicalByGroup.set(groupKey, canonical);
      aliasesByCanonical.set(canonical, []);
      groupedPeople.push(canonical);
    } else if (aliasKey(name) !== aliasKey(canonical)) {
      const aliases = aliasesByCanonical.get(canonical);
      if (!aliases.some((alias) => aliasKey(alias) === aliasKey(name))) {
        aliases.push(name);
      }
    }
    canonicalByName.set(name, canonical);
  }

  return {
    people: groupedPeople,
    aliasesByCanonical,
    canonicalByName,
  };
}

function relationshipTargetTopics(candidates, canonicalByName) {
  const topics = [];
  for (const candidate of candidates) {
    if (relationshipTouchesReviewEntity(candidate)) {
      continue;
    }
    const parsed = parseRelationshipCandidate(candidate);
    if (!parsed) {
      continue;
    }
    const { left, right } = parsed;
    const leftIsPerson = canonicalByName.has(left);
    const rightIsPerson = canonicalByName.has(right);
    if (leftIsPerson && !rightIsPerson && right) {
      topics.push(right);
    } else if (rightIsPerson && !leftIsPerson && left) {
      topics.push(left);
    }
  }
  return topics;
}

function memoryCandidateTitle(candidate) {
  const text = safeText(candidate, 160);
  return text.replace(/:\s*\d+\s+(?:safe message signals?|source snippets?|bounded source snippets?|preview source snippets?)$/i, '').trim() || text;
}

function normalizedReviewDecisions(preview) {
  const raw = preview?.review_decisions;
  const decisions = new Map();
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) {
    return decisions;
  }
  for (const [name, value] of Object.entries(raw)) {
    const key = safeText(name, 120);
    if (!key) {
      continue;
    }
    if (typeof value === 'string') {
      decisions.set(key, { action: value });
    } else if (value && typeof value === 'object') {
      decisions.set(key, {
        ...value,
        action: safeText(value.action ?? value.result ?? '', 40),
      });
    }
  }
  return decisions;
}

function reviewActionName(decision) {
  return safeText(decision?.action ?? '', 40).toLowerCase().replace(/\s+/g, '_');
}

function ignoredReviewNames(decisions) {
  return new Set([...decisions.entries()]
    .filter(([, decision]) => ['ignore', 'ignored'].includes(reviewActionName(decision)))
    .map(([name]) => name));
}

function confirmedReviewProjectNames(decisions, ignored) {
  return confirmedReviewEntities(decisions, ignored)
    .filter((entity) => entity.kind === 'project')
    .map((entity) => entity.name);
}

function confirmedReviewEntities(decisions, ignored) {
  const confirmedActions = new Set(['confirm', 'confirmed', 'private', 'mark_private', 'marked_private']);
  return [...decisions.entries()]
    .filter(([name, decision]) => !ignored.has(name) && confirmedActions.has(reviewActionName(decision)))
    .map(([name, decision]) => {
      const kind = safeText(decision.kind, 40).toLowerCase() === 'person' ? 'person' : 'project';
      return { name, kind };
    });
}

function relationshipTouchesIgnoredReviewEntity(item, ignored) {
  const parsed = parseRelationshipCandidate(item);
  if (!parsed) {
    return false;
  }
  return ignored.has(parsed.left) || ignored.has(parsed.right);
}

function relationshipTouchesConfirmedReviewEntity(item, confirmed) {
  const parsed = parseRelationshipCandidate(item);
  if (!parsed) {
    return false;
  }
  return confirmed.has(parsed.left) || confirmed.has(parsed.right);
}

function funFactSubject(item) {
  const text = stripGallerySourcePrefix(item);
  return text.split(' appeared in ')[0];
}

function funFactTouchesIgnoredReviewEntity(item, ignored) {
  return ignored.has(funFactSubject(item));
}

function textMentionsAny(text, names) {
  const value = String(text ?? '').toLowerCase();
  return [...names].some((name) => {
    const needle = String(name ?? '').trim().toLowerCase();
    return needle && value.includes(needle);
  });
}

function materializedPulseInsights(preview, ignoredReviews, nodeIDs, privacyTier) {
  const insights = Array.isArray(preview.pulse_insights) ? preview.pulse_insights : [];
  return insights
    .slice(0, 8)
    .map((insight, index) => {
      const title = safeText(insight.title, 180);
      const summary = safeText(insight.summary, 520);
      const suggested = safeText(insight.suggested_next_step, 240);
      if (!title || !summary || textMentionsAny(title, ignoredReviews) || textMentionsAny(summary, ignoredReviews) || textMentionsAny(suggested, ignoredReviews)) {
        return null;
      }
      const reasons = Array.isArray(insight.reasons)
        ? insight.reasons
          .map((item) => safeText(item, 220))
          .filter((item) => item && !textMentionsAny(item, ignoredReviews))
          .slice(0, 4)
        : [];
      const relatedEntities = Array.isArray(insight.related_entities)
        ? insight.related_entities
          .map((item) => safeText(item, 120))
          .filter((item) => item && !textMentionsAny(item, ignoredReviews))
          .slice(0, 8)
        : [];
      const entityRefs = relatedEntities
        .map((name) => nodeIDs.get(name))
        .filter(Boolean)
        .slice(0, 8);
      const body = [
        summary,
        reasons.length > 0 ? `Because: ${reasons.join(' ')}` : '',
        suggested ? `Next: ${suggested}` : '',
      ].filter(Boolean).join(' ');
      return {
        event: {
          client_id: semanticClientID('event', `pulse-insight-${insight.thread_title || insight.title || index}`, index),
          title: `Pulse insight: ${title}`,
          summary: body,
          entity_refs: entityRefs,
          sentiment: '',
          emotional_weight: 0.24,
          confidence: Number.isFinite(insight.confidence) ? Math.max(0, Math.min(1, insight.confidence)) : 0.6,
          privacy_tier: privacyTier,
          domain: 'real',
        },
        reviewInsight: `Pulse insight: ${title}. ${suggested ? `Next: ${suggested}` : summary}`,
      };
    })
    .filter(Boolean);
}

function materializedActiveThreads(preview, ignoredReviews) {
  const raw = Array.isArray(preview?.active_threads) ? preview.active_threads : [];
  const threads = [];
  const seen = new Set();
  for (const item of raw) {
    const title = typeof item === 'string'
      ? safeText(item, 160)
      : safeText(item?.thread_title ?? item?.title, 160);
    if (!title || textMentionsAny(title, ignoredReviews)) {
      continue;
    }
    const key = title.toLowerCase();
    if (seen.has(key)) {
      continue;
    }
    seen.add(key);
    const reason = typeof item === 'string'
      ? 'User marked this thread as active during review.'
      : safeText(item?.reason, 240) || 'User marked this thread as active during review.';
    if (textMentionsAny(reason, ignoredReviews)) {
      continue;
    }
    threads.push({ title, reason });
  }
  return threads.slice(0, 8);
}

function activeScopedContinuityItems(items, activeThreads) {
  if (!Array.isArray(items) || items.length === 0) {
    return [];
  }
  if (!Array.isArray(activeThreads) || activeThreads.length === 0) {
    return items;
  }
  const activeTitles = activeThreads
    .map((thread) => String(thread?.title ?? '').trim().toLowerCase())
    .filter(Boolean);
  if (activeTitles.length === 0) {
    return items;
  }
  return items.filter((item) => {
    const text = String(item).toLowerCase();
    return activeTitles.some((title) => text.includes(title));
  });
}

function buildSemanticDeltaFromPreview(preview, options = {}) {
  if (!preview || typeof preview !== 'object') {
    throw new Error('preview JSON must be an object');
  }
  if (preview.raw_text_written !== false) {
    throw new Error('preview raw_text_written must be false before commit');
  }

  const ctx = localThreadContext();
  const privacyTier = migrationPrivacyTier(options.privacy);
  const sourceHost = validMigrationHost(preview.source) ? preview.source : ctx.host;
  const reviewDecisions = normalizedReviewDecisions(preview);
  const ignoredReviews = ignoredReviewNames(reviewDecisions);
  const confirmedReviewItems = confirmedReviewEntities(reviewDecisions, ignoredReviews);
  const confirmedReviews = new Set(confirmedReviewItems.map((entity) => entity.name));
  const confirmedReviewPeople = confirmedReviewItems
    .filter((entity) => entity.kind === 'person')
    .map((entity) => entity.name);
  const confirmedProjects = confirmedReviewProjectNames(reviewDecisions, ignoredReviews);
  const peopleSource = previewArray(preview, 'people_candidates')
    .filter((name) => !ignoredReviews.has(name));
  const grouped = groupImportPeople(splitGalleryPersonCandidates(peopleSource).confident.slice(0, 18));
  const people = uniqueLimited([...grouped.people, ...confirmedReviewPeople], 18);
  const relationshipCandidates = previewArray(preview, 'relationship_candidates')
    .filter((candidate) => !relationshipTouchesIgnoredReviewEntity(candidate, ignoredReviews))
    .slice(0, 24);
  const memoryCandidates = previewArray(preview, 'memory_candidates').slice(0, 12);
  const projectLimit = Math.max(0, 30 - people.length);
  const threads = uniqueLimited([
    ...relationshipTargetTopics(relationshipCandidates, grouped.canonicalByName),
    ...previewArray(preview, 'thread_candidates'),
    ...memoryCandidates.map(memoryCandidateTitle),
    ...confirmedProjects,
  ], projectLimit);
  const nodeIDs = new Map();
  const nodes = [];

  people.forEach((name, index) => {
    const id = semanticClientID('person', name, index);
    nodeIDs.set(name, id);
    nodes.push({
      client_id: id,
      kind: 'person',
      canonical_name: name,
      aliases: grouped.aliasesByCanonical.get(name) ?? [],
      summary: 'Person candidate from safe Pulse archive import preview.',
      salience: 0.55,
      emotional_weight: 0.35,
      privacy_tier: privacyTier,
      domain: 'real',
    });
  });

  threads.forEach((name, index) => {
    const id = semanticClientID('project', name, index);
    nodeIDs.set(name, id);
    nodes.push({
      client_id: id,
      kind: 'project',
      canonical_name: name,
      summary: 'Thread candidate from safe Pulse archive import preview.',
      salience: 0.5,
      emotional_weight: 0.25,
      privacy_tier: privacyTier,
      domain: 'real',
    });
  });

  const edges = [];
  for (const candidate of relationshipCandidates) {
    if (relationshipTouchesReviewEntity(candidate) && !relationshipTouchesConfirmedReviewEntity(candidate, confirmedReviews)) {
      continue;
    }
    const parsed = parseRelationshipCandidate(candidate);
    if (!parsed) {
      continue;
    }
    if (parsed.kind === 'related_to') {
      const { left, right } = parsed;
      const canonicalLeft = grouped.canonicalByName.get(left) ?? left;
      const canonicalRight = grouped.canonicalByName.get(right) ?? right;
      if (nodeIDs.has(canonicalLeft) && nodeIDs.has(canonicalRight) && canonicalLeft !== canonicalRight) {
        edges.push({
          from: nodeIDs.get(canonicalLeft),
          to: nodeIDs.get(canonicalRight),
          kind: 'related_to',
          summary: `Safe archive preview linked ${canonicalLeft} and ${canonicalRight}.`,
          strength: 0.45,
          privacy_tier: privacyTier,
        });
      }
      continue;
    }
    if (parsed.kind === 'mentioned_in') {
      const { left, right } = parsed;
      const canonicalLeft = grouped.canonicalByName.get(left) ?? left;
      const canonicalRight = grouped.canonicalByName.get(right) ?? right;
      if (nodeIDs.has(canonicalLeft) && nodeIDs.has(canonicalRight) && canonicalLeft !== canonicalRight) {
        edges.push({
          from: nodeIDs.get(canonicalLeft),
          to: nodeIDs.get(canonicalRight),
          kind: 'mentioned_in',
          summary: `Safe archive preview placed ${canonicalLeft} in ${canonicalRight}.`,
          strength: 0.4,
          privacy_tier: privacyTier,
        });
      }
    }
  }

  const facts = [];
  for (const fact of previewArray(preview, 'fun_fact_candidates').slice(0, 20)) {
    const factSubject = funFactSubject(fact);
    if (funFactTouchesIgnoredReviewEntity(fact, ignoredReviews)) {
      continue;
    }
    if (funFactTouchesReviewEntity(fact) && !confirmedReviews.has(factSubject)) {
      continue;
    }
    const matched = [...grouped.canonicalByName.keys()].find((person) => fact.startsWith(person));
    const canonical = matched ? grouped.canonicalByName.get(matched) : (confirmedReviews.has(factSubject) ? factSubject : '');
    if (canonical && nodeIDs.has(canonical)) {
      facts.push({
        node: nodeIDs.get(canonical),
        text: fact,
        confidence: 0.55,
        privacy_tier: privacyTier,
        domain: 'real',
      });
    }
  }

  const entityRefs = [...new Set([...edges.flatMap((edge) => [edge.from, edge.to]), ...facts.map((fact) => fact.node)])]
    .filter((ref) => nodes.some((node) => node.client_id === ref))
    .slice(0, 12);
  const dedupedEdges = [];
  const seenEdges = new Set();
  for (const edge of edges) {
    const key = `${edge.from}|${edge.to}|${edge.kind}`;
    if (seenEdges.has(key)) {
      continue;
    }
    seenEdges.add(key);
    dedupedEdges.push(edge);
  }
  const emotions = previewArray(preview, 'emotion_candidates').slice(0, 8);
  const insights = materializedPulseInsights(preview, ignoredReviews, nodeIDs, privacyTier);
  const activeThreads = materializedActiveThreads(preview, ignoredReviews);
  const continuityMemoryCandidates = activeScopedContinuityItems(memoryCandidates, activeThreads).slice(0, 8);
  const reviewInsights = [
    ...insights.map((insight) => insight.reviewInsight),
    ...activeThreads.map((thread) => `Active thread: ${thread.title}. ${thread.reason}`),
  ].slice(0, 8);
  const events = [];
  if (nodes.length > 0) {
    events.push({
      client_id: 'event:archive-import-preview',
      title: 'Pulse archive import preview committed',
      summary: `Committed ${preview.conversations ?? 0} conversations and ${preview.messages ?? 0} source snippets from a Pulse archive preview.`,
      entity_refs: entityRefs,
      sentiment: emotions.join(', '),
      emotional_weight: emotions.length > 0 ? 0.45 : 0.2,
      confidence: 0.6,
      privacy_tier: privacyTier,
      domain: 'real',
    });
    memoryCandidates.forEach((candidate, index) => {
      const title = memoryCandidateTitle(candidate);
      const refs = nodeIDs.has(title) ? [nodeIDs.get(title)] : entityRefs.slice(0, 4);
      events.push({
        client_id: semanticClientID('event', title, index),
        title,
        summary: candidate,
        entity_refs: refs,
        sentiment: emotions.join(', '),
        emotional_weight: emotions.length > 0 ? 0.35 : 0.2,
        confidence: 0.55,
        privacy_tier: privacyTier,
        domain: 'real',
      });
    });
  }
  for (const insight of insights) {
    events.push(insight.event);
  }

  return {
    schema: 'pulse.semantic_delta.v1',
    source: {
      host: sourceHost,
      conversation_scope: 'project_context',
      timestamp: new Date().toISOString(),
      thread_id: ctx.threadId,
      session_id: ctx.sessionId,
      project_id: ctx.projectId,
    },
    nodes,
    edges: dedupedEdges.slice(0, 50),
    facts,
    events,
    continuity: {
      summary: `Imported safe Pulse archive preview with ${preview.conversations ?? 0} conversations and ${preview.messages ?? 0} source snippets.`,
      emotional_anchors: emotions.map((emotion) => `Archive preview emotion candidate: ${emotion}`),
      state_signals: continuityMemoryCandidates,
      active_threads: activeThreads.map((thread) => thread.title),
      review_insights: reviewInsights,
    },
    raw_input_included: false,
  };
}

async function commitMigrationPreview(previewPath, rest) {
  if (restArg(rest, '--confirm') !== 'import pulse graph') {
    throw new Error('pulse migrate commit requires --confirm "import pulse graph"');
  }
  if (!previewPath) {
    throw new Error('pulse migrate commit requires <preview-json-file>');
  }
  const preview = JSON.parse(readFileSync(resolve(previewPath), 'utf8'));
  const delta = buildSemanticDeltaFromPreview(preview, {
    privacy: restArg(rest, '--privacy'),
  });
  if (
    delta.nodes.length === 0 &&
    delta.events.length === 0 &&
    delta.continuity.state_signals.length === 0 &&
    delta.continuity.active_threads.length === 0 &&
    delta.continuity.review_insights.length === 0
  ) {
    throw new Error('preview has no safe graph candidates to commit');
  }
  const out = await pulseFetch('/graph/delta', { body: delta });
  console.log('[pulse] committed Pulse graph delta');
  console.log(`nodes: ${delta.nodes.length}`);
  console.log(`edges: ${delta.edges.length}`);
  console.log(`facts: ${delta.facts.length}`);
  console.log(`events: ${delta.events.length}`);
  console.log(JSON.stringify(out, null, 2));
  console.log('Next:');
  console.log(`  ${viewerNextStepCommand()}`);
  if (rest.includes('--open')) {
    runViewer(['--open']);
  }
}

function hostArchivePattern(host) {
  if (host === 'chatgpt') {
    return /(?:chatgpt|openai).*\.zip$/i;
  }
  if (host === 'claude') {
    return /claude.*\.zip$/i;
  }
  throw new Error('pulse migrate preview-latest supports: chatgpt, claude');
}

function latestArchiveMissingMessage(host, dir) {
  if (host === 'chatgpt') {
    return `No ChatGPT archive zip found in ${dir}.

What to do:
  1. Open ChatGPT Data Controls.
  2. Click "Request archive".
  3. Wait for the email/download. The archive may still be preparing.
  4. Save the zip into Downloads, or re-run with --downloads <dir>.`;
  }
  if (host === 'claude') {
    return `No Claude archive zip found in ${dir}.

What to do:
  1. Open Claude Privacy settings.
  2. Click "Export data".
  3. Wait for the email/download. The archive may still be preparing.
  4. Save the zip into Downloads, or re-run with --downloads <dir>.`;
  }
  return `No ${host} archive zip found in ${dir}. Re-run with --downloads <dir>.`;
}

function findLatestMigrationArchive(host, downloadsDir) {
  const dir = resolve(downloadsDir);
  if (!existsSync(dir)) {
    throw new Error(`downloads path does not exist: ${dir}`);
  }
  const pattern = hostArchivePattern(host);
  const candidates = readdirSync(dir, { withFileTypes: true })
    .filter((entry) => entry.isFile() && pattern.test(entry.name))
    .map((entry) => {
      const path = join(dir, entry.name);
      return { path, name: entry.name, mtimeMs: statSync(path).mtimeMs };
    })
    .sort((a, b) => b.mtimeMs - a.mtimeMs || b.name.localeCompare(a.name));
  if (candidates.length === 0) {
    throw new Error(latestArchiveMissingMessage(host, dir));
  }
  return candidates[0].path;
}

function tryFindLatestMigrationArchive(host, downloadsDir) {
  try {
    return findLatestMigrationArchive(host, downloadsDir);
  } catch (err) {
    if (err instanceof Error && /^No (ChatGPT|Claude) archive zip found/.test(err.message)) {
      return '';
    }
    throw err;
  }
}

function positiveIntArg(rest, name, fallback) {
  const raw = restArg(rest, name);
  if (raw === undefined) {
    return fallback;
  }
  const value = Number.parseInt(raw, 10);
  if (!Number.isFinite(value) || value <= 0) {
    throw new Error(`${name} must be a positive integer`);
  }
  return value;
}

function sleep(ms) {
  return new Promise((resolveSleep) => setTimeout(resolveSleep, ms));
}

async function waitForLatestMigrationArchive(host, downloadsDir, timeoutMs, intervalMs) {
  const dir = resolve(downloadsDir);
  const started = Date.now();
  while (Date.now() - started <= timeoutMs) {
    const latest = tryFindLatestMigrationArchive(host, dir);
    if (latest) {
      return latest;
    }
    await sleep(intervalMs);
  }
  throw new Error(`${latestArchiveMissingMessage(host, dir)}

Waited ${timeoutMs}ms without seeing a matching archive.`);
}

function requestPreviewRest(rest) {
  const out = [...rest];
  if (!out.includes('--json') && restArg(out, '--html') === undefined) {
    out.push('--html', 'pulse-preview.html');
  }
  if (!out.includes('--json') && restArg(out, '--out') === undefined) {
    out.push('--out', 'pulse-preview.json');
  }
  return out;
}

async function requestLatestMigrationArchive(source, rest) {
  const info = remoteArchiveInfo(source);
  const previewRest = requestPreviewRest(rest);
  const downloadsDir = restArg(rest, '--downloads') ?? join(homedir(), 'Downloads');
  const timeoutMs = positiveIntArg(rest, '--timeout-ms', 15 * 60 * 1000);
  const intervalMs = positiveIntArg(rest, '--interval-ms', 2000);
  openExternalURL(info.url);
  console.log(`[pulse] ${info.label} archive page opened`);
  console.log(`[pulse] Click "${info.buttonText}". Pulse is waiting for the export zip in ${resolve(downloadsDir)}.`);
  console.log(`[pulse] waiting for ${info.source} archive in ${resolve(downloadsDir)} (${timeoutMs}ms timeout)`);
  const latest = await waitForLatestMigrationArchive(info.source, downloadsDir, timeoutMs, intervalMs);
  console.log(`[pulse] latest ${info.source} archive: ${latest}`);
  emitMigrationPreview(latest, [...previewRest, '--source', info.source]);
}

function requestLocalHistoryPreview(source, rest) {
  const info = localHistoryInfo(source);
  if (!info) {
    return false;
  }
  const previewRest = requestPreviewRest(rest);
  console.log(`[pulse] ${info.label} local history selected`);
  console.log(`[pulse] Local history: ${info.displayPath}`);
  console.log('[pulse] No archive request is needed. Previewing local history now.');
  emitMigrationPreview(info.path, [...previewRest, '--source', info.source]);
  return true;
}

async function requestMigrationSource(source, rest) {
  if (requestLocalHistoryPreview(source, rest)) {
    return;
  }
  await requestLatestMigrationArchive(source, rest);
}

function emitPeopleGraphPreview(target, rest) {
  const preview = peopleGraphPreview(target);
  const htmlPath = restArg(rest, '--html');
  const jsonOutPath = restArg(rest, '--out');
  const resolvedJsonOutPath = jsonOutPath ? resolve(jsonOutPath) : '';
  const htmlPreview = {
    ...preview,
    commit_command: resolvedJsonOutPath
      ? `pulse migrate commit ${shellArg(resolvedJsonOutPath)} --confirm "import pulse graph" --open`
      : '',
    viewer_command: viewerNextStepCommand(),
  };
  if (htmlPath) {
    const outPath = resolve(htmlPath);
    writeFileSync(outPath, renderMigrationPreviewHTML(htmlPreview), { mode: 0o600 });
    console.log(`[pulse] people graph HTML preview: ${outPath}`);
    if (rest.includes('--open')) {
      openExternalURL(outPath);
    }
  }
  if (jsonOutPath) {
    const outPath = resolvedJsonOutPath;
    const jsonPreview = {
      ...preview,
      html_path: htmlPath ? resolve(htmlPath) : undefined,
      next: htmlPreview.commit_command || preview.next,
      commit_command: htmlPreview.commit_command || undefined,
      viewer_command: htmlPreview.viewer_command,
    };
    writeFileSync(outPath, `${JSON.stringify(jsonPreview, null, 2)}\n`, { mode: 0o600 });
    console.log(`[pulse] people graph JSON preview: ${outPath}`);
    console.log('Next:');
    console.log(`  ${htmlPreview.commit_command}`);
  }
  if (rest.includes('--json')) {
    console.log(JSON.stringify({ ...preview, html_path: htmlPath ? resolve(htmlPath) : undefined }, null, 2));
    return;
  }
  if (htmlPath || jsonOutPath) {
    return;
  }
  printMigrationPreview(preview);
}

function emitMigrationPreview(target, rest) {
  const sourceHint = restArg(rest, '--source');
  const basePreview = migrationPreview(target);
  const preview = overrideImportPreviewSource(basePreview, sourceHint);
  const htmlPath = restArg(rest, '--html');
  const jsonOutPath = restArg(rest, '--out');
  const resolvedJsonOutPath = jsonOutPath ? resolve(jsonOutPath) : '';
  const htmlPreview = {
    ...preview,
    commit_command: resolvedJsonOutPath
      ? `pulse migrate commit ${shellArg(resolvedJsonOutPath)} --confirm "import pulse graph" --open`
      : '',
    viewer_command: viewerNextStepCommand(),
  };
  if (htmlPath) {
    const outPath = resolve(htmlPath);
    writeFileSync(outPath, renderMigrationPreviewHTML(htmlPreview), { mode: 0o600 });
    console.log(`[pulse] migration HTML preview: ${outPath}`);
    if (rest.includes('--open')) {
      openExternalURL(outPath);
    }
  }
  if (jsonOutPath) {
    const outPath = resolvedJsonOutPath;
    const jsonPreview = {
      ...preview,
      html_path: htmlPath ? resolve(htmlPath) : undefined,
      next: htmlPreview.commit_command || preview.next,
      commit_command: htmlPreview.commit_command || undefined,
      viewer_command: htmlPreview.viewer_command,
    };
    writeFileSync(outPath, `${JSON.stringify(jsonPreview, null, 2)}\n`, { mode: 0o600 });
    console.log(`[pulse] migration JSON preview: ${outPath}`);
    console.log('Next:');
    console.log(`  ${htmlPreview.commit_command}`);
  }
  if (rest.includes('--json')) {
    console.log(JSON.stringify({ ...preview, html_path: htmlPath ? resolve(htmlPath) : undefined }, null, 2));
    return;
  }
  if (htmlPath || jsonOutPath) {
    return;
  }
  printMigrationPreview(preview);
}

async function runMigrate(subcommand, rest) {
  if (subcommand === 'start') {
    await runMigrationStart(rest);
    return;
  }
  if (subcommand === 'concierge') {
    runMigrationConcierge(rest);
    return;
  }
  if (subcommand === 'guide') {
    const source = rest.find((arg) => !arg.startsWith('--'));
    if (!source) {
      throw new Error('pulse migrate guide requires chatgpt, claude, codex, or claude-code');
    }
    migrationGuide(source);
    return;
  }
  if (subcommand === 'commit') {
    await commitMigrationPreview(rest.find((arg) => !arg.startsWith('--')), rest);
    return;
  }
  if (subcommand === 'request') {
    const source = rest.find((arg) => !arg.startsWith('--'));
    if (!source) {
      throw new Error('pulse migrate request requires chatgpt, claude, codex, or claude-code');
    }
    await requestMigrationSource(source, rest);
    return;
  }
  if (subcommand === 'preview-latest') {
    const source = rest.find((arg) => !arg.startsWith('--'));
    if (!source) {
      throw new Error('pulse migrate preview-latest requires chatgpt or claude');
    }
    const downloadsDir = restArg(rest, '--downloads') ?? join(homedir(), 'Downloads');
    const latest = findLatestMigrationArchive(source, downloadsDir);
    console.log(`[pulse] latest ${source} archive: ${latest}`);
    emitMigrationPreview(latest, [...rest, '--source', source]);
    return;
  }
  if (subcommand === 'preview-people-graph') {
    const target = rest.find((arg) => !arg.startsWith('--'));
    if (!target) {
      throw new Error('pulse migrate preview-people-graph requires <graph-dir-or-people-index>');
    }
    emitPeopleGraphPreview(target, rest);
    return;
  }
  if (subcommand === 'wait-latest') {
    const source = rest.find((arg) => !arg.startsWith('--'));
    if (!source) {
      throw new Error('pulse migrate wait-latest requires chatgpt or claude');
    }
    const downloadsDir = restArg(rest, '--downloads') ?? join(homedir(), 'Downloads');
    const timeoutMs = positiveIntArg(rest, '--timeout-ms', 15 * 60 * 1000);
    const intervalMs = positiveIntArg(rest, '--interval-ms', 2000);
    console.log(`[pulse] waiting for ${source} archive in ${resolve(downloadsDir)} (${timeoutMs}ms timeout)`);
    const latest = await waitForLatestMigrationArchive(source, downloadsDir, timeoutMs, intervalMs);
    console.log(`[pulse] latest ${source} archive: ${latest}`);
    emitMigrationPreview(latest, [...rest, '--source', source]);
    return;
  }
  if (subcommand !== 'preview') {
    throw new Error('pulse migrate supports: start, guide, concierge, request, wait-latest, preview-latest, preview-people-graph, preview, commit');
  }
  const target = rest.find((arg) => !arg.startsWith('--'));
  if (!target) {
    throw new Error('pulse migrate preview requires <export-folder-or-json>');
  }
  emitMigrationPreview(target, rest);
}

function restArg(rest, name) {
  const i = rest.indexOf(name);
  return i >= 0 ? rest[i + 1] : undefined;
}

function observationSummary(eventType, payload) {
  if (eventType === 'UserPromptSubmit') {
    return 'Prompt noticed. Raw text is hidden by default.';
  }

  const toolName = safeText(payload?.tool_name ?? payload?.tool);
  if (eventType === 'PostToolUse' && toolName) {
    return `Tool ran: ${toolName}. Raw tool input and output are hidden by default.`;
  }

  const text = firstSafeText(payload, [
    'summary',
    'description',
  ]);
  if (text) {
    return `Session note: ${text}`;
  }
  if (toolName) {
    return `Tool ran: ${toolName}. Raw tool input and output are hidden by default.`;
  }
  return 'Local activity captured. Raw prompt and tool content are hidden by default.';
}

function parseObjectLike(value) {
  if (!value) {
    return {};
  }
  if (typeof value === 'string') {
    try {
      const parsed = JSON.parse(value);
      return parsed && typeof parsed === 'object' ? parsed : {};
    } catch {
      return {};
    }
  }
  return typeof value === 'object' ? value : {};
}

function hookToolName(payload) {
  return safeText(
    payload?.tool_name ??
    payload?.toolName ??
    payload?.tool ??
    payload?.name ??
    payload?.hook_name,
    160,
  );
}

function hookToolInput(payload) {
  for (const candidate of [
    payload?.tool_input,
    payload?.toolInput,
    payload?.input,
    payload?.tool?.input,
    payload?.tool_use?.input,
    payload?.toolUse?.input,
  ]) {
    const parsed = parseObjectLike(candidate);
    if (Object.keys(parsed).length > 0) {
      return parsed;
    }
  }
  return {};
}

function memoryKindLabel(kind) {
  switch (kind) {
    case 'decision':
      return 'Decision';
    case 'open_loop':
      return 'Open loop';
    case 'do_not_repeat':
      return 'Do-not-repeat';
    case 'preference':
      return 'Preference';
    case 'project_state':
      return 'Project state';
    case 'correction':
      return 'Correction';
    case 'relationship_note':
      return 'Relationship note';
    case 'state_signal':
      return 'State signal';
    case 'system_event':
      return 'System event';
    case 'fact':
      return 'Fact';
    default:
      return '';
  }
}

function pulseRememberObservationSummaries(payload) {
  const toolName = hookToolName(payload);
  if (!toolName.includes('pulse_remember')) {
    return [];
  }
  const capsule = hookToolInput(payload);
  if (capsule.schema !== 'pulse.memory_capsule.v1' || capsule.raw_input_included !== false || !Array.isArray(capsule.items)) {
    return [];
  }
  return capsule.items
    .map((item) => {
      const label = memoryKindLabel(item?.kind);
      const summary = safeText(item?.redacted_summary, 720);
      return label && summary ? `${label}: ${summary}` : '';
    })
    .filter(Boolean)
    .slice(0, 20);
}

function observationSummaries(eventType, payload) {
  if (eventType === 'PostToolUse') {
    const capsuleSummaries = pulseRememberObservationSummaries(payload);
    if (capsuleSummaries.length > 0) {
      return capsuleSummaries;
    }
  }
  return [observationSummary(eventType, payload)];
}

async function runHook(kind) {
  const strict = process.env.PULSE_HOOK_STRICT === '1';
  try {
    if (kind === 'session-start') {
      await hookSessionStart();
      return;
    }
    if (kind === 'user-prompt-submit') {
      await hookObserve('UserPromptSubmit');
      return;
    }
    if (kind === 'post-tool-use') {
      await hookObserve('PostToolUse');
      return;
    }
    if (kind === 'stop') {
      await hookStop();
      return;
    }
    throw new Error('pulse hook supports: session-start, user-prompt-submit, post-tool-use, stop');
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    if (strict) {
      throw err;
    }
    console.error(`[pulse] hook ${kind} skipped: ${message}`);
  }
}

async function hookSessionStart() {
  const ctx = localThreadContext();
  const resume = await pulseFetch('/continuity/resume', {
    body: {
      thread_id: ctx.threadId,
      project_id: ctx.projectId,
      session_id: ctx.sessionId,
      host: ctx.host,
      token_budget: Number(process.env.PULSE_RESUME_TOKENS ?? 1200),
    },
  });
  if (resume?.resume_markdown) {
    console.log(resume.resume_markdown);
  }
  console.log(`
# Pulse Graph Guidance
- When the user makes a durable decision, open loop, correction, preference, relationship note, project-state change, or emotional/state anchor, call pulse_graph_delta with pulse.semantic_delta.v1.
- Do not send raw transcript, secrets, credentials, local file paths, or store-everything payloads.
`);
}

async function hookObserve(eventType) {
  const payload = parseHookPayload(await readStdin());
  const ctx = localThreadContext();
  const summaries = observationSummaries(eventType, payload);
  const stamp = new Date().toISOString();
  for (let i = 0; i < summaries.length; i += 1) {
    await pulseFetch('/continuity/observe', {
      body: {
        thread_id: ctx.threadId,
        project_id: ctx.projectId,
        session_id: ctx.sessionId,
        host: ctx.host,
        event_type: eventType,
        redacted_summary: summaries[i],
        source_ref: `pulse:hook:${eventType}:${stamp}${summaries.length > 1 ? `:${i}` : ''}`,
      },
    });
  }
}

async function hookStop() {
  const payload = parseHookPayload(await readStdin());
  const ctx = localThreadContext();
  const summary =
    firstSafeText(payload, ['summary', 'session_summary', 'checkpoint_summary']) ||
    `Session ended for ${ctx.threadId}. Pulse recorded a local-auto checkpoint.`;
  await pulseFetch('/continuity/checkpoint', {
    body: {
      thread_id: ctx.threadId,
      project_id: ctx.projectId,
      session_id: ctx.sessionId,
      host: ctx.host,
      summary,
      decisions: safeStringArray(payload.decisions),
      open_loops: safeStringArray(payload.open_loops).length > 0
        ? safeStringArray(payload.open_loops)
        : ['Review what changed since the last Pulse checkpoint.'],
      do_not_repeat: safeStringArray(payload.do_not_repeat),
      emotional_anchors: safeStringArray(payload.emotional_anchors),
      state_signals: safeStringArray(payload.state_signals).length > 0
        ? safeStringArray(payload.state_signals)
        : ['Local-auto continuity is enabled for this project.'],
      source_refs: [`pulse:hook:Stop:${new Date().toISOString()}`],
      confidence: 0.6,
    },
  });
}

function getArg(name) {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
}

function getRestArg(rest, name) {
  const i = rest.indexOf(name);
  return i >= 0 ? rest[i + 1] : undefined;
}

async function main() {
  if (command === '--why' || command === 'why') {
    console.log('Because repeating yourself to machines is a terrible way to live.');
    return;
  }

  if (command === '--help' || command === '-h' || command === 'help') {
    usage();
    return;
  }

  if (command === 'install-plan') {
    const target = args[1];
    if (target !== 'claude-code') {
      throw new Error('v1 supports only: pulse install-plan claude-code');
    }
    printInstallPlan(target, { json: args.includes('--json') });
    return;
  }

  if (command === 'demo') {
    await printPulseDemo();
    return;
  }

  if (command === 'doctor') {
    await runDoctor(args.slice(1));
    return;
  }

  if (command === 'init') {
    const target = args[1];
    if (target !== 'claude-code') {
      throw new Error('v1 supports only: pulse init claude-code');
    }
    if (args.includes('--dry-run')) {
      printInstallPlan(target, { dryRun: true });
      return;
    }
    await connectClaudeCode();
    return;
  }

  if (command === 'connect') {
	    const target = args[1];
	    if (target === 'claude-code') {
	      await connectClaudeCode();
	      return;
	    }
    if (target === 'claude-chat') {
      connectClaudeChat();
      return;
    }
    throw new Error('v1 supports only: pulse connect claude-code or pulse connect claude-chat');
  }

  if (command === 'disconnect') {
    const target = args[1];
    if (target !== 'claude-code') {
      throw new Error('v1 supports only: pulse disconnect claude-code');
    }
    disconnectClaudeCode();
    return;
  }

  if (command === 'stop') {
    stopPreviewDaemon();
    return;
  }

  if (command === 'remove') {
    const target = args[1];
    if (target !== 'claude-code') {
      throw new Error('v1 supports only: pulse remove claude-code');
    }
    disconnectClaudeCode();
    stopPreviewDaemon();
    console.log('[pulse] Local memory was not wiped.');
    console.log('[pulse] To wipe memory, run: pulse wipe --confirm "wipe pulse memory"');
    return;
  }

  if (command === 'hook') {
    await runHook(args[1]);
    return;
  }

  if (command === 'migrate') {
    await runMigrate(args[1], args.slice(2));
    return;
  }

  if (command === 'viewer') {
    await runViewer(args.slice(1));
    return;
  }

  if (command === 'daemon') {
    const sep = args.indexOf('--');
    daemon(sep >= 0 ? args.slice(sep + 1) : args.slice(1));
    return;
  }

  if (command === 'status') {
    console.log(JSON.stringify(await pulseFetch('/memory/status', { method: 'GET' }), null, 2));
    return;
  }

  if (command === 'export') {
    console.log(JSON.stringify(await pulseFetch('/memory/export', { method: 'GET' }), null, 2));
    return;
  }

  if (command === 'import') {
    const file = getArg('--file');
    if (!file) throw new Error('pulse import requires --file <path>');
    const payload = JSON.parse(readFileSync(resolve(file), 'utf8'));
    console.log(JSON.stringify(await pulseFetch('/memory/import', { body: payload }), null, 2));
    return;
  }

  if (command === 'delete') {
    const id = getArg('--id');
    if (!id) throw new Error('pulse delete requires --id <pulse:id>');
    await pulseFetch('/memory/delete', { body: { id } });
    console.log(`[pulse] deleted ${id}`);
    return;
  }

  if (command === 'wipe') {
    if (getArg('--confirm') !== 'wipe pulse memory') {
      throw new Error('pulse wipe requires --confirm "wipe pulse memory"');
    }
    await pulseFetch('/memory/wipe', { body: { confirm: 'wipe pulse memory' } });
    console.log('[pulse] wiped host-extracted memory');
    return;
  }

  usage();
  throw new Error(`unknown command: ${command}`);
}

main().catch((err) => {
  console.error(`[pulse] ${err instanceof Error ? err.message : String(err)}`);
  process.exit(1);
});
