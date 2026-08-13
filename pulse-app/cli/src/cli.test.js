import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { createServer } from 'node:http';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import { createPlatformServices } from './platform-services.js';

const CLI = fileURLToPath(new URL('./cli.js', import.meta.url));
const REPO_ROOT = fileURLToPath(new URL('../..', import.meta.url));
const FIRST_PROOF_MEMORY =
	'Pulse keeps the thread: structured memories, never raw transcripts, deletion stays human-controlled.';

function productReceipt(overrides = {}) {
  return {
    schema: 'pulse.write_receipt.v1',
    receipt_id: 'receipt_test',
    ledger_id: 'turn_test',
    candidate_id: 'candidate_test',
    candidate_version: 1,
    status: 'pending',
    destination: 'desk',
    destination_store_id: 'store_desk_test',
    safe_provenance: {
      host: 'pulse-cli', session_id: 'session:test', turn_id: 'turn:test', source_event_key: 'event:test',
    },
    content_digest: 'a'.repeat(64),
    policy_epoch: 0,
    resolver_epoch: 0,
    measurement_method: 'host_structured_v1',
    created_at: '2026-07-14T09:00:00Z',
    ...overrides,
  };
}

function run(args, env = {}) {
  const home = mkdtempSync(join(tmpdir(), 'pulse-cli-test-home.'));
  const cwd = mkdtempSync(join(tmpdir(), 'pulse-cli-test-cwd.'));
  return {
    home,
    cwd,
    result: spawnSync(process.execPath, [CLI, ...args], {
      cwd,
      env: {
        ...process.env,
        HOME: home,
        PULSE_DATA_DIR: join(home, '.pulse'),
        PULSE_BASE_URL: 'http://127.0.0.1:18789',
        PULSE_VIEWER_SKIP_AUTH_CHECK: '1',
        PULSE_PREVIEW_RUNTIME_SETUP: '0',
        PATH: '/usr/bin:/bin',
        ...env,
      },
      encoding: 'utf8',
    }),
  };
}

function runInWorkspace(args, cwd, home, env = {}) {
  return spawnSync(process.execPath, [CLI, ...args], {
    cwd,
    env: {
      ...process.env,
      HOME: home,
      PULSE_DATA_DIR: join(home, '.pulse'),
      PULSE_BASE_URL: 'http://127.0.0.1:18789',
      PULSE_VIEWER_SKIP_AUTH_CHECK: '1',
      PULSE_PREVIEW_RUNTIME_SETUP: '0',
      PATH: '/usr/bin:/bin',
      ...env,
    },
    encoding: 'utf8',
  });
}

function runInWorkspaceAsync(args, cwd, home, env = {}, stdin = '') {
  return new Promise((resolve) => {
    const child = spawn(process.execPath, [CLI, ...args], {
      cwd,
      env: {
        ...process.env,
        HOME: home,
        PULSE_DATA_DIR: join(home, '.pulse'),
        PULSE_BASE_URL: 'http://127.0.0.1:18789',
        PULSE_VIEWER_SKIP_AUTH_CHECK: '1',
        PULSE_PREVIEW_RUNTIME_SETUP: '0',
        PATH: '/usr/bin:/bin',
        ...env,
      },
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (chunk) => {
      stdout += chunk;
    });
    child.stderr.on('data', (chunk) => {
      stderr += chunk;
    });
    child.on('close', (status) => {
      resolve({ status, stdout, stderr, cwd });
    });
    child.stdin.end(stdin);
  });
}

function writeExecutable(path, content) {
  writeFileSync(path, content);
  chmodSync(path, 0o755);
}

function runWithDetectedHost(args, host) {
  const home = mkdtempSync(join(tmpdir(), 'pulse-cli-detected-host-home.'));
  const cwd = mkdtempSync(join(tmpdir(), 'pulse-cli-detected-host-cwd.'));
  const executable = host === 'codex'
    ? join(home, '.local', 'bin', 'codex')
    : process.platform === 'darwin'
      ? join(home, 'Applications', 'Cursor.app', 'Contents', 'MacOS', 'Cursor')
      : join(home, '.local', 'bin', 'cursor');
  mkdirSync(dirname(executable), { recursive: true, mode: 0o700 });
  writeExecutable(executable, host === 'codex'
    ? '#!/bin/sh\nprintf "codex-cli 0.146.0\\n"\n'
    : '#!/bin/sh\nexit 0\n');
  return { home, cwd, result: runInWorkspace(args, cwd, home) };
}

test('destructive CLI refuses non-interactive agent and pipe execution', () => {
  const wipe = run(['wipe', '--confirm', 'wipe pulse memory']).result;
  assert.equal(wipe.status, 1);
  assert.match(wipe.stderr, /directly attached interactive terminal/);

  const deletion = run(['delete', '--id', 'pulse:test']).result;
  assert.equal(deletion.status, 1);
  assert.match(deletion.stderr, /directly attached interactive terminal/);
});

function runAsync(args, env = {}, stdin = '') {
  const home = mkdtempSync(join(tmpdir(), 'pulse-cli-test-home.'));
  const cwd = mkdtempSync(join(tmpdir(), 'pulse-cli-test-cwd.'));
  return new Promise((resolve) => {
    const child = spawn(process.execPath, [CLI, ...args], {
      cwd,
      env: {
        ...process.env,
        HOME: home,
        PULSE_DATA_DIR: join(home, '.pulse'),
        PULSE_VIEWER_SKIP_AUTH_CHECK: '1',
        PULSE_PREVIEW_RUNTIME_SETUP: '0',
        PATH: '/usr/bin:/bin',
        ...env,
      },
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (chunk) => {
      stdout += chunk;
    });
    child.stderr.on('data', (chunk) => {
      stderr += chunk;
    });
    child.on('close', (status) => {
      resolve({ status, stdout, stderr, cwd, home });
    });
    child.stdin.end(stdin);
  });
}

test('workspace binding CLI exposes only Personal creation and read-only inspection', () => {
  const help = run(['--help']).result;
  assert.equal(help.status, 0, help.stderr);
  assert.match(help.stdout, /pulse binding resolve/);

  const mutation = run(['binding', 'bind']).result;
  assert.notEqual(mutation.status, 0);
  assert.match(mutation.stderr, /supports resolve, status, or create-personal/);
});

function withPulseStub(handler) {
  const requests = [];
  const server = createServer(async (req, res) => {
    let body = '';
    for await (const chunk of req) body += chunk;
    const parsed = body ? JSON.parse(body) : undefined;
    requests.push({ method: req.method, url: req.url, body: parsed });
    let out;
    try {
      out = handler(req, parsed);
    } catch (error) {
      res.statusCode = 500;
      res.setHeader('Content-Type', 'application/json');
      res.end(JSON.stringify({ error: error.stack || String(error) }));
      return;
    }
    res.statusCode = out.status ?? 200;
    res.setHeader('Content-Type', 'application/json');
    res.end(JSON.stringify(out.body ?? { ok: true }));
  });
  return new Promise((resolve, reject) => {
    server.listen(0, '127.0.0.1', () => {
      const { port } = server.address();
      resolve({
        baseUrl: `http://127.0.0.1:${port}`,
        requests,
        close: () => new Promise((done) => server.close(done)),
      });
    });
    server.on('error', reject);
  });
}

test('legacy consolidate stays on its existing route', async () => {
  const stub = await withPulseStub((req, body) => {
    if (req.url === '/memory/consolidate') {
      assert.equal(req.method, 'POST');
      assert.deepEqual(body, { dry_run: true, threshold: 0.91 });
      return { body: { dry_run: true, groups: [] } };
    }
    return { status: 404, body: { error: 'not found' } };
  });
  try {
    const legacyResult = await runAsync(['consolidate', '--threshold', '0.91'], { PULSE_BASE_URL: stub.baseUrl });
    assert.equal(legacyResult.status, 0, legacyResult.stderr);
    assert.match(legacyResult.stdout, /"dry_run": true/);
    assert.match(legacyResult.stdout, /re-run with --apply/);
    assert.deepEqual(stub.requests.map((request) => request.url), ['/memory/consolidate']);
  } finally {
    await stub.close();
  }
});

function delay(ms) {
  return new Promise((resolve) => {
    setTimeout(resolve, ms);
  });
}

async function freePort() {
  const server = createServer();
  await new Promise((resolve, reject) => {
    server.listen(0, '127.0.0.1', resolve);
    server.on('error', reject);
  });
  const { port } = server.address();
  await new Promise((resolve) => server.close(resolve));
  return port;
}

async function startPulseServer(dataDir) {
  const port = await freePort();
  const baseUrl = `http://127.0.0.1:${port}`;
  mkdirSync(dataDir, { recursive: true });
  const binPath = join(dirname(dataDir), 'pulse-test-server');
  const build = spawnSync('go', ['build', '-o', binPath, './cmd/pulse'], {
    cwd: REPO_ROOT,
    env: {
      ...process.env,
      ANTHROPIC_API_KEY: '',
      COHERE_API_KEY: '',
    },
    encoding: 'utf8',
  });
  if (build.status !== 0) {
    throw new Error(`go build failed:\n${build.stdout}\n${build.stderr}`);
  }
  const child = spawn(binPath, ['-addr', `127.0.0.1:${port}`, '-data-dir', dataDir], {
    cwd: REPO_ROOT,
    env: {
      ...process.env,
      ANTHROPIC_API_KEY: '',
      COHERE_API_KEY: '',
      PULSE_MODE: 'local-auto',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let logs = '';
  child.stdout.on('data', (chunk) => {
    logs += chunk;
  });
  child.stderr.on('data', (chunk) => {
    logs += chunk;
  });

  let exited = false;
  child.on('exit', () => {
    exited = true;
  });

  const secretPath = join(dataDir, 'secret.key');
  for (let i = 0; i < 80; i += 1) {
    if (exited) {
      throw new Error(`Pulse server exited early:\n${logs}`);
    }
    if (existsSync(secretPath)) {
      const secret = readFileSync(secretPath, 'utf8').trim();
      try {
        const resp = await fetch(`${baseUrl}/memory/status`, {
          headers: { 'X-Pulse-Key': secret },
        });
        if (resp.ok) {
          return {
            baseUrl,
            secret,
            stop: () => stopChild(child),
          };
        }
      } catch {
        // Server has created the secret but is not listening yet.
      }
    }
    await delay(100);
  }
  child.kill('SIGKILL');
  throw new Error(`Pulse server did not become ready:\n${logs}`);
}

function stopChild(child) {
  return new Promise((resolve) => {
    if (child.exitCode !== null) {
      resolve();
      return;
    }
    const timer = setTimeout(() => {
      child.kill('SIGKILL');
    }, 1500);
    child.once('exit', () => {
      clearTimeout(timer);
      resolve();
    });
    child.kill('SIGTERM');
  });
}

test('daemon requires explicit Go binary path', () => {
  const { result } = run(['daemon']);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /PULSE_GO_BIN|--go-bin/);
});

test('why prints the Pulse reason without requiring a daemon', () => {
  const { result } = run(['--why']);

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /Because repeating yourself to machines is a terrible way to live/);
  assert.doesNotMatch(result.stdout, /PULSE_API_KEY|secret\.key|127\.0\.0\.1/);
});

test('install-plan claude-code --json returns a stable agent contract', () => {
  const { result } = run(['install-plan', 'claude-code', '--json']);

  assert.equal(result.status, 0, result.stderr);
  const plan = JSON.parse(result.stdout);
  assert.equal(plan.product, 'Pulse MCP Preview');
  assert.equal(plan.version, '0.8.1');
  assert.equal(plan.target_host, 'claude-code');
  assert.equal(plan.mode, 'developer_preview');
  assert.deepEqual(plan.will_install, [
    'local Pulse daemon',
    'Pulse MCP server',
    'Claude Code local connection',
    'Claude Code lifecycle hooks',
    'local viewer',
    'private first memory proof',
  ]);
  assert.deepEqual(plan.will_write, [
    '~/.pulse',
    'project .claude/settings.local.json',
    'project .mcp.json',
  ]);
  assert.match(plan.will_not_do.join('\n'), /import old chats/);
  assert.match(plan.will_not_do.join('\n'), /store raw transcripts/);
  assert.match(plan.will_not_do.join('\n'), /backend OpenAI\/Anthropic\/Cohere/);
  assert.match(plan.requires.join('\n'), /Node 20\+/);
  assert.match(plan.rollback.join('\n'), /pulse wipe --confirm "wipe pulse memory"/);
  assert.doesNotMatch(result.stdout, /PULSE_API_KEY|secret\.key|sk-|ghp_|xoxb-/);
});

test('install-plan claude-code prints human-readable trust plan', () => {
  const { result } = run(['install-plan', 'claude-code']);

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /Pulse install plan/);
  assert.match(result.stdout, /Target host: Claude Code/);
  assert.match(result.stdout, /Will install:/);
  assert.match(result.stdout, /Will write:/);
  assert.match(result.stdout, /Will not:/);
  assert.match(result.stdout, /Requires:/);
  assert.match(result.stdout, /Rollback:/);
  assert.match(result.stdout, /Run with --yes/);
  assert.doesNotMatch(result.stdout, /PULSE_API_KEY|secret\.key|sk-|ghp_|xoxb-/);
});

test('install-plan --json exposes the host-neutral Personal product contract without mutation', () => {
  const missingReleaseRoot = mkdtempSync(join(tmpdir(), 'pulse-cli-missing-release.'));
  const { home, result } = run(['install-plan', '--json'], {
    PULSE_RELEASE_TEST_MODE: '1',
    PULSE_RELEASE_MANIFEST_PATH: join(missingReleaseRoot, 'personal-preview-manifest.json'),
  });

  assert.equal(result.status, 0, result.stderr);
  const plan = JSON.parse(result.stdout);
  assert.equal(plan.schema, 'pulse.personal_install_plan.v2');
  assert.equal('target_host' in plan, false);
  assert.deepEqual(plan.supported_hosts, ['claude-code', 'codex', 'cursor']);
  assert.deepEqual(plan.detected.hosts.map((host) => host.host), ['claude-code', 'codex', 'cursor']);
  assert.equal(plan.stage, 'personal_stage_1');
  assert.equal(plan.privacy.raw_transcript_capture, 'off');
  assert.equal(plan.privacy.backend_model_calls, 'off');
  assert.match(plan.reason_codes.join('\n'), /workspace_not_git/);
  assert.equal(plan.release, null);
  assert.match(plan.reason_codes.join('\n'), /release_manifest_unavailable/);
  assert.equal(existsSync(join(home, '.pulse')), false);
});

test('non-interactive Personal install asks for an explicit non-interactive choice', () => {
  const { home, result } = run(['install']);

  assert.notEqual(result.status, 0);
  assert.match(result.stdout, /Found compatible AI apps:/);
  assert.match(result.stderr, /use --dry-run.*or --yes/i);
  assert.equal(existsSync(join(home, '.pulse')), false);
});

test('Personal install --json keeps stdout machine-readable', () => {
  const { result } = run(['install', '--json']);

  assert.notEqual(result.status, 0);
  const terminal = JSON.parse(result.stdout);
  assert.equal(terminal.schema, 'pulse.personal_install_result.v2');
  assert.equal(terminal.outcome, 'action_required');
  assert.match(result.stderr, /Pulse Personal install/);
  assert.doesNotMatch(result.stdout, /Pulse Personal install/);
});

test('complete pre-consent plan never executes a project-local Codex from PATH', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-cli-plan-safe-home.'));
  const cwd = mkdtempSync(join(tmpdir(), 'pulse-cli-plan-safe-cwd.'));
  const bin = join(cwd, 'bin');
  const marker = join(cwd, 'codex-executed');
  mkdirSync(bin);
  spawnSync('/usr/bin/git', ['init', '-q'], { cwd, encoding: 'utf8' });
  writeFileSync(join(bin, 'codex'), `#!/bin/sh\ntouch ${JSON.stringify(marker)}\n`, { mode: 0o700 });
  chmodSync(join(bin, 'codex'), 0o700);

  const result = runInWorkspace(['install-plan', '--json'], cwd, home, { PATH: `${bin}:/usr/bin:/bin` });

  assert.equal(result.status, 0, result.stderr);
  assert.equal(JSON.parse(result.stdout).schema, 'pulse.personal_install_plan.v2');
  assert.equal(existsSync(marker), false);
});

test('public Personal installer rejects a marketplace source override as synthetic authority', () => {
  const { result } = run(['install-plan', '--json'], {
    PULSE_CODEX_MARKETPLACE_SOURCE: '/tmp/untrusted-pulse-marketplace',
  });

  assert.equal(result.status, 0, result.stderr);
  const plan = JSON.parse(result.stdout);
  assert.equal(plan.outcome, 'action_required');
  assert.ok(plan.reason_codes.includes('synthetic_authority_forbidden'));
});

test('preflight derives resume evidence from a durable runtime journal after process interruption', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-cli-resume-home.'));
  const cwd = mkdtempSync(join(tmpdir(), 'pulse-cli-resume-cwd.'));
  const dataDir = join(home, 'pulse-data');
  mkdirSync(join(dataDir, 'runtime'), { recursive: true, mode: 0o700 });
  spawnSync('/usr/bin/git', ['init', '-q'], { cwd, encoding: 'utf8' });
  writeFileSync(join(dataDir, 'runtime', 'install-journal.json'), JSON.stringify({
    schema: 'pulse.personal_install_journal.v1',
    phase: 'downloading',
    manifest_digest: 'a'.repeat(64),
  }), { mode: 0o600 });

  const result = runInWorkspace(['install-plan', '--json'], cwd, home, { PULSE_DATA_DIR: dataDir });

  assert.equal(result.status, 0, result.stderr);
  assert.equal(JSON.parse(result.stdout).current_state.install_receipt, 'resumable');
});

test('a resumable release journal outranks a zero-step blocked receipt from an interrupted download', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-cli-resume-blocked-home.'));
  const cwd = mkdtempSync(join(tmpdir(), 'pulse-cli-resume-blocked-cwd.'));
  const dataDir = join(home, 'pulse-data');
  mkdirSync(join(dataDir, 'runtime'), { recursive: true, mode: 0o700 });
  spawnSync('/usr/bin/git', ['init', '-q'], { cwd, encoding: 'utf8' });
  writeFileSync(join(dataDir, 'runtime', 'install-journal.json'), JSON.stringify({
    schema: 'pulse.personal_install_journal.v1',
    phase: 'downloading',
    manifest_digest: 'a'.repeat(64),
  }), { mode: 0o600 });
  const initial = runInWorkspace(['install-plan', '--json'], cwd, home, { PULSE_DATA_DIR: dataDir });
  assert.equal(initial.status, 0, initial.stderr);
  const workspace = JSON.parse(initial.stdout).detected.workspace;
  mkdirSync(join(dataDir, 'receipts', 'install'), { recursive: true, mode: 0o700 });
  writeFileSync(join(dataDir, 'receipts', 'install', `${workspace.workspace_id}.json`), JSON.stringify({
    schema: 'pulse.personal_install_receipt.v1',
    outcome: 'blocked',
    workspace_id: workspace.workspace_id,
    repository_id: workspace.repository_id,
  }), { mode: 0o600 });

  const resumed = runInWorkspace(['install-plan', '--json'], cwd, home, { PULSE_DATA_DIR: dataDir });

  assert.equal(resumed.status, 0, resumed.stderr);
  assert.equal(JSON.parse(resumed.stdout).current_state.install_receipt, 'resumable');
});

test('preflight keeps a staged release generation resumable while host lifecycle completes', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-cli-staged-home.'));
  const cwd = mkdtempSync(join(tmpdir(), 'pulse-cli-staged-cwd.'));
  const dataDir = join(home, 'pulse-data');
  mkdirSync(join(dataDir, 'runtime'), { recursive: true, mode: 0o700 });
  spawnSync('/usr/bin/git', ['init', '-q'], { cwd, encoding: 'utf8' });
  writeFileSync(join(dataDir, 'runtime', 'install-journal.json'), JSON.stringify({
    schema: 'pulse.personal_install_journal.v1',
    phase: 'candidate_staged',
    manifest_digest: 'a'.repeat(64),
  }), { mode: 0o600 });

  const result = runInWorkspace(['install-plan', '--json'], cwd, home, { PULSE_DATA_DIR: dataDir });

  assert.equal(result.status, 0, result.stderr);
  assert.equal(JSON.parse(result.stdout).current_state.install_receipt, 'resumable');
});

test('init claude-code dry run prints install plan and writes nothing', () => {
  const { cwd, home, result } = run(['init', 'claude-code', '--dry-run']);

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /Pulse Personal install/);
  assert.match(result.stdout, /Downloads after approval/);
  assert.match(result.stdout, /Local writes:/);
  assert.match(result.stdout, /Activation: every compatible AI app found/);
  assert.match(result.stdout, /Dry run only\. Nothing was written\./);
  assert.equal(existsSync(join(home, '.pulse')), false);
  assert.equal(existsSync(join(cwd, '.claude')), false);
  assert.equal(existsSync(join(cwd, '.mcp.json')), false);
});

for (const host of ['codex', 'cursor']) {
  test(`init ${host} --only dry run limits the plan and writes nothing`, {
    skip: process.platform === 'win32' ? 'POSIX CLI fixture; Windows detection has native adapter coverage' : false,
  }, () => {
    const { cwd, home, result } = runWithDetectedHost(
      ['init', host, '--only', host, '--dry-run'], host,
    );

    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /Pulse Personal install/);
    assert.match(result.stdout, /Activation: only the selected compatible AI app/);
    assert.match(result.stdout, new RegExp(`- ${host}: will be connected`, 'i'));
    assert.match(result.stdout, /Dry run only\. Nothing was written\./);
    assert.equal(existsSync(join(home, '.pulse')), false);
    assert.equal(existsSync(join(cwd, '.cursor')), false);
  });
}

test('doctor --json reports machine-readable missing setup without a stack trace', () => {
  const { result } = run(['doctor', '--json'], { PATH: '/tmp/pulse-missing-tools' });

  assert.notEqual(result.status, 0);
  const report = JSON.parse(result.stdout);
  assert.equal(report.product, 'Pulse Local Preview');
  assert.equal(report.version, '0.8.1');
  assert.equal(report.target_host, 'claude-code');
  assert.equal(report.trust.backend_llm_enabled, false);
  assert.equal(report.trust.raw_capture_enabled, false);
  for (const key of ['node', 'npm', 'go', 'claude_code', 'daemon', 'port', 'mcp', 'hooks', 'viewer', 'first_memory']) {
    assert.ok(report.checks[key], `missing check ${key}`);
    assert.equal(typeof report.checks[key].ok, 'boolean');
    assert.equal(typeof report.checks[key].detail, 'string');
  }
  assert.match(report.next_steps.join('\n'), /pulse init claude-code/);
  assert.doesNotMatch(result.stderr + result.stdout, /at async|stack|PULSE_API_KEY|secret\.key/i);
});

test('doctor cursor --json reports the native Cursor product contract', () => {
  const { result } = run(['doctor', 'cursor', '--json'], {
    PATH: '/tmp/pulse-missing-tools',
    PULSE_TRUST_MODE: 'test',
  });

  assert.notEqual(result.status, 0);
  const report = JSON.parse(result.stdout);
  assert.equal(report.target_host, 'cursor');
  assert.equal(report.verdict, 'Pulse Cursor automatic lifecycle is not ready.');
  assert.deepEqual(report.authority_profile, {
    schema: 'pulse.personal_authority_profile.v1',
    version: 1,
    kind: 'portable',
    ordinary_ready: true,
    enhanced_presence: {
      schema: 'pulse.enhanced_presence.profile.v1',
      version: 1,
      kind: 'unavailable',
      available: false,
      protected_actions: [],
      reason_code: 'enhanced_presence_unavailable',
    },
  });
  assert.equal(report.checks.presence_trust.ok, true);
  assert.match(report.checks.presence_trust.detail, /optional/i);
  for (const key of ['presence_trust', 'binding', 'runtime', 'plugin', 'hooks', 'vault', 'capture', 'retrieval']) {
    assert.ok(report.checks[key], `missing check ${key}`);
    assert.equal(typeof report.checks[key].ok, 'boolean');
    assert.equal(typeof report.checks[key].detail, 'string');
  }
  assert.doesNotMatch(result.stderr + result.stdout, /at async|stack|PULSE_API_KEY|secret\.key/i);
});

test('doctor presents native presence as optional and scoped only to protected actions', () => {
  const { result } = run(['doctor', 'claude-code'], {
    PATH: '/tmp/pulse-missing-tools',
    PULSE_TRUST_MODE: 'test',
  });

  assert.notEqual(result.status, 0);
  assert.match(result.stdout, /Authority profile: pulse\.personal_authority_profile\.v1/);
  assert.match(result.stdout, /Ordinary Personal memory: ready without enhanced presence/i);
  assert.match(result.stdout, /Unavailable protected actions: binding\.change, vault\.wipe/i);
  assert.doesNotMatch(result.stdout, /Next: pulse trust install/);
  assert.match(result.stdout, /Optional setup for binding\.change and vault\.wipe only: pulse trust install --confirm "install pulse presence helper"/i);
});

test('Codex doctor exposes the same portable authority profile without making presence a readiness failure', () => {
  const { result } = run(['doctor', 'codex', '--json'], {
    PATH: '/tmp/pulse-missing-tools',
    PULSE_TRUST_MODE: 'test',
  });

  assert.notEqual(result.status, 0);
  const report = JSON.parse(result.stdout);
  assert.equal(report.target_host, 'codex');
  assert.equal(report.authority_profile.schema, 'pulse.personal_authority_profile.v1');
  assert.equal(report.authority_profile.ordinary_ready, true);
  assert.equal(report.authority_profile.enhanced_presence.available, false);
  assert.deepEqual(report.authority_profile.enhanced_presence.protected_actions, []);
  assert.equal(report.checks.presence_trust.ok, true);
  assert.notEqual(report.personal_live_readiness.reason_code, 'presence_required');
});

test('viewer --print-url prints only the authenticated URL', () => {
  const dataDir = mkdtempSync(join(tmpdir(), 'pulse-print-viewer-data.'));
  mkdirSync(dataDir, { recursive: true });
  writeFileSync(join(dataDir, 'secret.key'), 'print-secret');
  const { result } = run([
    'viewer',
    '--data-dir',
    dataDir,
    '--base',
    'http://127.0.0.1:18888',
    '--thread-id',
    'pulse-distribution',
    '--print-url',
  ]);

  assert.equal(result.status, 0, result.stderr);
  assert.equal(
    result.stdout.trim(),
    'http://127.0.0.1:18888/viewer?key=print-secret&thread_id=pulse-distribution',
  );
  assert.equal(result.stderr.trim(), '');
});

test('demo without a built daemon points at pulse init', () => {
  const { result } = run(['demo']);

  assert.equal(result.status, 1);
  const output = result.stdout + result.stderr;
  assert.match(output, /Pulse Local Preview daemon is not built yet/);
  assert.match(output, /pulse init claude-code --yes/);
  assert.doesNotMatch(output, /Claude never forgets|production ready/i);
});

test('demo --clean removes the isolated preview corpus dir', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-cli-test-home.'));
  const demoDir = join(home, '.pulse', 'preview-demo');
  mkdirSync(demoDir, { recursive: true });
  writeFileSync(join(demoDir, 'store-marker'), 'x');
  const result = runInWorkspace(['demo', '--clean'], mkdtempSync(join(tmpdir(), 'pulse-cli-test-cwd.')), home);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /preview corpus removed/);
  assert.equal(existsSync(demoDir), false);
});

test('demo corpus is simulated, bounded, and stateful-demo shaped', () => {
  const corpus = JSON.parse(
    readFileSync(new URL('./demo-corpus.json', import.meta.url), 'utf8'),
  );
  assert.match(corpus.label, /SIMULATED\. NOT YOUR DATA/);
  assert.ok(corpus.events.length >= 12 && corpus.events.length <= 20, 'corpus size 12..20');
  assert.equal(corpus.states.length, 3);
  const anchors = corpus.events.filter((event) => event.anchor);
  assert.ok(anchors.length >= 2, 'needs structural anchors');
  assert.ok(
    anchors.some((event) => event.occurred_days_ago >= 60),
    'anchors must be old enough to beat recent noise',
  );
  assert.ok(
    corpus.events.some((event) => event.biometrics && event.biometrics.stress_proxy >= 0.6),
    'needs depletion-typed episodes for state fit',
  );
  for (const event of corpus.events) {
    assert.ok(event.client_id.startsWith('demo:'), 'demo ids must be namespaced');
    assert.equal(event.privacy_tier, 'normal');
  }
  for (const state of corpus.states) {
    assert.ok(state.user_state.mood_vector, 'each state carries a mood vector');
  }
});

test('doctor reports human-readable zero-to-wow checks', async () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-cli-test-home.'));
  const cwd = mkdtempSync(join(tmpdir(), 'pulse-cli-test-cwd.'));
  const tools = mkdtempSync(join(tmpdir(), 'pulse-cli-test-tools.'));
  const dataDir = join(home, '.pulse');
  mkdirSync(dataDir, { recursive: true });
  writeFileSync(join(dataDir, 'secret.key'), 'doctor-secret');
  writeFileSync(join(dataDir, 'pulse-preview-daemon.pid'), `${process.pid}\n`);
  writeExecutable(join(tools, 'go'), `#!${process.execPath}
if (process.argv.includes('version')) {
  console.log('go version go1.25.0 darwin/arm64');
  process.exit(0);
}
process.exit(0);
`);
  writeExecutable(join(tools, 'npm'), `#!${process.execPath}
if (process.argv.includes('--version')) {
  console.log('10.0.0');
  process.exit(0);
}
process.exit(0);
`);
  writeExecutable(join(tools, 'claude'), `#!${process.execPath}
if (process.argv.includes('--version')) {
  console.log('Claude Code 1.0.0');
  process.exit(0);
}
if (process.argv.includes('mcp') && process.argv.includes('list')) {
  console.log('pulse');
  process.exit(0);
}
process.exit(0);
`);
  mkdirSync(join(cwd, '.claude'), { recursive: true });
  writeFileSync(join(cwd, '.claude', 'settings.local.json'), JSON.stringify({
    hooks: {
      UserPromptSubmit: [{ hooks: [{ type: 'command', command: 'pulse hook user-prompt-submit' }] }],
      PreToolUse: [{ hooks: [{ type: 'command', command: 'pulse hook pre-tool-use' }] }],
      PostToolUse: [{ hooks: [{ type: 'command', command: 'pulse hook post-tool-use' }] }],
    },
  }, null, 2));
  writeFileSync(join(cwd, '.mcp.json'), JSON.stringify({
    mcpServers: {
      pulse: { command: 'pulse-mcp' },
    },
  }, null, 2));
  const stub = await withPulseStub((req) => {
    if (req.method === 'GET' && req.url === '/memory/status') {
      return {
        body: {
          billing_mode: 'host-extracted',
          host: 'claude-code',
          backend_llm_enabled: false,
          raw_capture_enabled: false,
          storage_path: '<local>',
        },
      };
    }
    if (req.method === 'GET' && req.url?.startsWith('/viewer/data')) {
      return { body: { resume: { resume_markdown: '# Pulse Resume' } } };
    }
    return { status: 404, body: { error: 'not found' } };
  });

  try {
    const result = await runInWorkspaceAsync(['doctor'], cwd, home, {
      PATH: `${tools}:/usr/bin:/bin`,
      PULSE_BASE_URL: stub.baseUrl,
      PULSE_VIEWER_SKIP_AUTH_CHECK: '0',
    });

    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /\[pulse\] doctor/);
    assert.match(result.stdout, /Node: ok/);
    assert.match(result.stdout, /Go: ok/);
    assert.match(result.stdout, /Claude Code CLI: ok/);
    assert.match(result.stdout, /Pulse daemon: ok/);
    assert.match(result.stdout, /MCP: ok/);
    assert.match(result.stdout, /Hooks: ok/);
    assert.match(result.stdout, /Viewer: ok/);
    assert.match(result.stdout, /backend LLM off/);
    assert.match(result.stdout, /raw transcript capture off/);
    assert.match(result.stdout, /What Pulse will tell Claude next/);
    assert.doesNotMatch(result.stderr + result.stdout, /Error:|at async|stack/i);
  } finally {
    await stub.close();
  }
});

test('doctor explains missing setup without a stack trace', () => {
  const { result } = run(['doctor'], { PATH: '/tmp/pulse-missing-tools' });

  assert.notEqual(result.status, 0);
  assert.match(result.stdout, /\[pulse\] doctor/);
  assert.match(result.stdout, /Go: missing/);
  assert.match(result.stdout, /Claude Code CLI: missing/);
  assert.match(result.stdout, /Next:/);
  assert.doesNotMatch(result.stderr + result.stdout, /at async|stack/i);
});

test('stop removes the preview daemon pid file when process is absent', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-cli-test-home.'));
  const cwd = mkdtempSync(join(tmpdir(), 'pulse-cli-test-cwd.'));
  const dataDir = join(home, '.pulse');
  mkdirSync(dataDir, { recursive: true });
  writeFileSync(join(dataDir, 'pulse-preview-daemon.pid'), '999999\n');

  const result = runInWorkspace(['stop'], cwd, home);

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /No running Pulse preview daemon found/);
  assert.equal(existsSync(join(dataDir, 'pulse-preview-daemon.pid')), false);
});

test('product init fails closed before writing config when Claude Code is unavailable', () => {
  const { cwd, result } = run(['init', 'claude-code', '--only', 'claude-code', '--yes']);

  assert.notEqual(result.status, 0);
  assert.match(`${result.stdout}\n${result.stderr}`, /supported_harness_missing/);

  const list = spawnSync('find', [cwd, '-maxdepth', '1', '-name', '.mcp.json'], {
    encoding: 'utf8',
  });
  assert.equal(list.stdout.trim(), '');
});

test('connect before installation explains that init must run first', () => {
  const { result } = run(['connect', 'claude-code', '--dry-run']);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /First run "pulse init claude-code"/);
  assert.doesNotMatch(result.stderr, /ENOENT|workspace-bindings\.json/);
});

test('connect before installation never falls back to mutable preview npm registration', () => {
  const { result } = run(['connect', 'claude-code', '--dry-run'], {
    PULSE_MCP_ENTRYPOINT: '/tmp/pulse-mcp-missing/dist/index.js',
  });

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /First run "pulse init claude-code"/);
  assert.doesNotMatch(`${result.stdout}\n${result.stderr}`, /\bnpx\b|@zbs-gg\/pulse@preview/);
});

test('pulse mcp serves stdio MCP tools with the standalone store', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-cli-test-home.'));
  const requests = [
    JSON.stringify({
      jsonrpc: '2.0',
      id: 1,
      method: 'initialize',
      params: {
        protocolVersion: '2025-06-18',
        capabilities: {},
        clientInfo: { name: 'cli-test', version: '0' },
      },
    }),
    JSON.stringify({ jsonrpc: '2.0', method: 'notifications/initialized' }),
    JSON.stringify({ jsonrpc: '2.0', id: 2, method: 'tools/list' }),
    JSON.stringify({
      jsonrpc: '2.0',
      id: 3,
      method: 'tools/call',
      params: { name: 'pulse_status', arguments: {} },
    }),
  ].join('\n');
  const result = spawnSync(process.execPath, [CLI, 'mcp'], {
    env: {
      ...process.env,
      HOME: home,
      PULSE_DATA_DIR: join(home, '.pulse'),
      PULSE_MCP_MODE: 'standalone',
    },
    input: `${requests}\n`,
    encoding: 'utf8',
    timeout: 30_000,
  });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /pulse_graph_delta/);
  assert.match(result.stdout, /standalone_lite/);
  assert.match(result.stderr, /standalone lite store/);
  assert.equal(existsSync(join(home, '.pulse', 'standalone', 'store.json')), true);
});

test('connect remote-control also requires an existing installation', () => {
  const { result } = run(['connect', 'claude-code', '--remote-control', '--dry-run']);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /First run "pulse init claude-code"/);
});

test('disconnect claude-code removes Pulse hooks and project MCP fallback', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-cli-test-home.'));
  const cwd = mkdtempSync(join(tmpdir(), 'pulse-cli-test-cwd.'));
  const settingsPath = join(cwd, '.claude', 'settings.local.json');
  mkdirSync(join(cwd, '.claude'), { recursive: true });
  writeFileSync(settingsPath, JSON.stringify({
    permissions: { allow: ['Bash(git status)'] },
    hooks: {
      SessionStart: [{
        matcher: 'startup',
        hooks: [
          { type: 'command', command: 'echo keep-me', timeout: 10 },
          { type: 'command', command: 'pulse hook session-start', timeout: 60 },
        ],
      }],
      Stop: [{
        hooks: [{
          type: 'command',
          command: `PULSE_DATA_DIR='${join(home, '.pulse')}' '${process.execPath}' '${join(home, '.pulse', 'runtime', 'codex', 'current', 'src', 'cli.js')}' claude-hook Stop`,
          timeout: 60,
        }],
      }],
      Notification: [{ hooks: [{ type: 'command', command: 'echo keep-notification' }] }],
    },
  }, null, 2));
  writeFileSync(join(cwd, '.mcp.json'), JSON.stringify({
    mcpServers: {
      pulse: { command: 'pulse-mcp' },
      keep: { command: 'keep-mcp' },
    },
  }, null, 2));
	const dataDir = join(home, '.pulse');
	mkdirSync(dataDir, { recursive: true });
	writeFileSync(join(dataDir, 'memory.keep'), 'committed memory remains');

  const result = runInWorkspace(['disconnect', 'claude-code'], cwd, home);

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /Claude Code disconnected/);
  const settings = JSON.parse(readFileSync(settingsPath, 'utf8'));
  assert.deepEqual(settings.permissions, { allow: ['Bash(git status)'] });
  assert.deepEqual(settings.hooks.SessionStart[0].hooks.map((hook) => hook.command), ['echo keep-me']);
  assert.equal(settings.hooks.Stop, undefined);
  assert.equal(settings.hooks.Notification[0].hooks[0].command, 'echo keep-notification');
  const mcp = JSON.parse(readFileSync(join(cwd, '.mcp.json'), 'utf8'));
  assert.equal(mcp.mcpServers.pulse, undefined);
  assert.equal(mcp.mcpServers.keep.command, 'keep-mcp');
	const captureState = JSON.parse(readFileSync(join(dataDir, 'capture-state.json'), 'utf8'));
	assert.equal(captureState.schema, 'pulse.capture_state.v1');
	assert.equal(captureState.enabled, false);
	assert.equal(captureState.hosts['claude-code'].reason, 'host_disconnected');
	assert.equal(readFileSync(join(dataDir, 'memory.keep'), 'utf8'), 'committed memory remains');
});

test('disconnect claude-code never overwrites invalid hook JSON', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-cli-test-home.'));
  const cwd = mkdtempSync(join(tmpdir(), 'pulse-cli-test-cwd.'));
  const settingsPath = join(cwd, '.claude', 'settings.local.json');
  mkdirSync(join(cwd, '.claude'), { recursive: true });
  const invalid = '{"hooks":{"Stop":[';
  writeFileSync(settingsPath, invalid);

  const result = runInWorkspace(['disconnect', 'claude-code'], cwd, home);

  assert.notEqual(result.status, 0);
  assert.equal(readFileSync(settingsPath, 'utf8'), invalid);
});

test('viewer prints local authenticated viewer URL', () => {
  const { result } = run(['viewer']);

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /\/viewer\?key=/);
});

const testHomeRouteScope = Buffer.alloc(32).toString('base64url');
const testHomeRoutePath = `/home/s/${testHomeRouteScope}/`;

test('home exchanges the daemon secret internally and opens a one-shot credential-free relay', async () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-home-session-home.'));
  const cwd = mkdtempSync(join(tmpdir(), 'pulse-home-session-cwd.'));
  const dataDir = join(home, '.pulse');
  const daemonSecret = 'daemon-secret-must-never-escape';
  const cookieValue = 'browser-session-must-never-escape';
  mkdirSync(dataDir, { recursive: true });
  writeFileSync(join(dataDir, 'secret.key'), daemonSecret);
  const stub = await withPulseStub((req, body) => {
    assert.equal(req.method, 'POST');
    assert.equal(req.url, '/home/session');
    assert.equal(req.headers['x-pulse-key'], daemonSecret);
		assert.deepEqual(Object.keys(body), ['live_readiness']);
		assert.deepEqual(Object.keys(body.live_readiness).sort(), [
			'checked_at', 'next_action', 'outcome', 'reason_code', 'schema',
		]);
		assert.equal(body.live_readiness.schema, 'pulse.personal_live_readiness.v1');
		assert.match(body.live_readiness.checked_at, /^\d{4}-\d{2}-\d{2}T.*Z$/);
    return {
      body: {
        cookie_name: 'pulse_home',
        cookie_value: cookieValue,
        cookie_path: testHomeRoutePath,
        max_age_seconds: 30,
        target_url: `http://${req.headers.host}${testHomeRoutePath}`,
      },
    };
  });
	const startedAt = Date.now();

  try {
    const result = await runInWorkspaceAsync([
      'home', '--base', stub.baseUrl, '--data-dir', dataDir,
    ], cwd, home, {
      PULSE_OPEN_DRY_RUN: '1',
      PULSE_HOME_HANDOFF_TIMEOUT_MS: '1000',
			PULSE_HOME_DRY_RUN_NAVIGATION_TIMEOUT_MS: '100',
    });

    assert.equal(result.status, 0, result.stderr);
		assert.ok(Date.now() - startedAt < 2_000,
			'one-shot relay replay check must finish within its own navigation bound');
    assert.equal(result.stdout, '[pulse] Memory Home opened.\n');
    assert.equal(result.stderr, '');
    assert.equal(stub.requests.length, 1);
		assert.equal(stub.requests[0].method, 'POST');
		assert.equal(stub.requests[0].url, '/home/session');
		assert.equal(stub.requests[0].body.live_readiness.schema, 'pulse.personal_live_readiness.v1');
    assert.doesNotMatch(
      result.stdout + result.stderr,
      new RegExp(`${daemonSecret}|${cookieValue}|${testHomeRouteScope}|${stub.baseUrl.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}|key=|token=|session=|bootstrap`, 'i'),
    );
  } finally {
    await stub.close();
  }
});

test('home rejects print-url instead of exposing a bootstrap capability', () => {
  const { result } = run(['home', '--print-url']);

  assert.equal(result.status, 1);
  assert.match(result.stderr, /pulse home does not support --print-url/);
  assert.doesNotMatch(result.stdout + result.stderr, /https?:\/\/|key=|token=|secret/i);
});

test('home accepts only the three supported Personal harness selectors', () => {
  for (const args of [['home', '--host'], ['home', '--host', 'gemini']]) {
    const { result } = run(args);
    assert.equal(result.status, 1);
    assert.match(result.stderr, /pulse home --host must be claude-code, codex, or cursor/);
    assert.doesNotMatch(result.stdout + result.stderr, /https?:\/\/|key=|token=|secret/i);
  }
});

test('home rejects a daemon target with a query without echoing session credentials', async () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-home-target-home.'));
  const cwd = mkdtempSync(join(tmpdir(), 'pulse-home-target-cwd.'));
  const dataDir = join(home, '.pulse');
  mkdirSync(dataDir, { recursive: true });
  writeFileSync(join(dataDir, 'secret.key'), 'target-daemon-secret');
  const stub = await withPulseStub((req) => ({
    body: {
      cookie_name: 'pulse_home',
      cookie_value: 'target-browser-session',
      cookie_path: testHomeRoutePath,
      max_age_seconds: 30,
      target_url: `http://${req.headers.host}${testHomeRoutePath}?key=forbidden`,
    },
  }));

  try {
    const result = await runInWorkspaceAsync([
      'home', '--base', stub.baseUrl, '--data-dir', dataDir,
    ], cwd, home, { PULSE_OPEN_DRY_RUN: '1' });

    assert.equal(result.status, 1);
    assert.match(result.stderr, /exact queryless scoped \/home target/);
    assert.doesNotMatch(result.stdout + result.stderr, /target-daemon-secret|target-browser-session|key=forbidden/);
  } finally {
    await stub.close();
  }
});

test('home rejects non-canonical, mismatched, and broadened scoped handoffs', async () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-home-scoped-target-home.'));
  const cwd = mkdtempSync(join(tmpdir(), 'pulse-home-scoped-target-cwd.'));
  const dataDir = join(home, '.pulse');
  const daemonSecret = 'scoped-target-daemon-secret';
  const cookieValue = 'scoped-target-browser-session';
  const otherRoutePath = `/home/s/${Buffer.alloc(32, 1).toString('base64url')}/`;
  const nonCanonicalPath = `/home/s/${'A'.repeat(42)}B/`;
  mkdirSync(dataDir, { recursive: true });
  writeFileSync(join(dataDir, 'secret.key'), daemonSecret);
  const cases = [
    { cookiePath: testHomeRoutePath.slice(0, -1), targetPath: testHomeRoutePath },
    { cookiePath: testHomeRoutePath, targetPath: otherRoutePath },
    { cookiePath: nonCanonicalPath, targetPath: nonCanonicalPath },
    { cookiePath: testHomeRoutePath, targetPath: `${testHomeRoutePath}assets/home.js` },
    { cookiePath: testHomeRoutePath, targetPath: `${testHomeRoutePath}#fragment` },
  ];
  const stub = await withPulseStub((req) => {
    const current = cases[stub.requests.length - 1];
    return {
      body: {
        cookie_name: 'pulse_home', cookie_value: cookieValue,
        cookie_path: current.cookiePath, max_age_seconds: 30,
        target_url: `http://${req.headers.host}${current.targetPath}`,
      },
    };
  });

  try {
    for (const current of cases) {
      const result = await runInWorkspaceAsync([
        'home', '--base', stub.baseUrl, '--data-dir', dataDir,
      ], cwd, home, { PULSE_OPEN_DRY_RUN: '1' });
      assert.equal(result.status, 1, `accepted unsafe scoped handoff ${JSON.stringify(current)}`);
      assert.doesNotMatch(result.stdout + result.stderr, new RegExp(`${daemonSecret}|${cookieValue}`));
    }
  } finally {
    await stub.close();
  }
});

test('home stops reading an oversized daemon session response before JSON decoding', async () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-home-oversized-home.'));
  const cwd = mkdtempSync(join(tmpdir(), 'pulse-home-oversized-cwd.'));
  const dataDir = join(home, '.pulse');
  mkdirSync(dataDir, { recursive: true });
  writeFileSync(join(dataDir, 'secret.key'), 'oversized-daemon-secret');
  const stub = await withPulseStub(() => ({
    body: {
      cookie_name: 'pulse_home',
      cookie_value: 'oversized-browser-session',
      cookie_path: testHomeRoutePath,
      max_age_seconds: 30,
      target_url: `http://127.0.0.1:18789${testHomeRoutePath}`,
      padding: 'x'.repeat(20 * 1024),
    },
  }));

  try {
    const result = await runInWorkspaceAsync([
      'home', '--base', stub.baseUrl, '--data-dir', dataDir,
    ], cwd, home, { PULSE_OPEN_DRY_RUN: '1' });

    assert.equal(result.status, 1);
    assert.match(result.stderr, /oversized Memory Home session/);
    assert.doesNotMatch(result.stdout + result.stderr, /oversized-daemon-secret|oversized-browser-session/);
  } finally {
    await stub.close();
  }
});

test('home bounds an unresponsive daemon session exchange', async () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-home-timeout-home.'));
  const cwd = mkdtempSync(join(tmpdir(), 'pulse-home-timeout-cwd.'));
  const dataDir = join(home, '.pulse');
  mkdirSync(dataDir, { recursive: true });
  writeFileSync(join(dataDir, 'secret.key'), 'timeout-daemon-secret');
  const server = createServer(async (req) => {
    for await (const _chunk of req) { /* keep the response pending */ }
  });
  await new Promise((resolve, reject) => {
    server.listen(0, '127.0.0.1', resolve);
    server.on('error', reject);
  });
  const baseUrl = `http://127.0.0.1:${server.address().port}`;
  const startedAt = Date.now();

  try {
    const result = await runInWorkspaceAsync([
      'home', '--base', baseUrl, '--data-dir', dataDir,
    ], cwd, home, {
      PULSE_OPEN_DRY_RUN: '1',
      PULSE_HOME_REQUEST_TIMEOUT_MS: '50',
    });

    assert.equal(result.status, 1);
    assert.ok(Date.now() - startedAt < 2_000, 'home session request must fail within its bound');
    assert.match(result.stderr, /Memory Home session request timed out/);
    assert.doesNotMatch(result.stdout + result.stderr, /timeout-daemon-secret|key=|token=/i);
  } finally {
    server.closeAllConnections?.();
    await new Promise((resolve) => server.close(resolve));
  }
});

test('home does not wait on a binding update held by another local process', async () => {
	const home = mkdtempSync(join(tmpdir(), 'pulse-home-binding-lock-home.'));
	const cwd = mkdtempSync(join(tmpdir(), 'pulse-home-binding-lock-cwd.'));
	const trust = join(home, 'trust');
	const registryPath = join(trust, 'bindings.json');
	const publicKeyPath = join(trust, 'bindings.pub');
	const anchorPath = join(trust, 'bindings.anchor');
	mkdirSync(trust, { recursive: true, mode: 0o700 });
	writeFileSync(publicKeyPath, 'synthetic public key\n', { mode: 0o600 });
	writeFileSync(anchorPath, '{}\n', { mode: 0o600 });
	const release = createPlatformServices().acquirePrivateLock(`${registryPath}.lock`, {
		staleAfterMs: 0, timeoutMs: 0,
	});
	const startedAt = Date.now();

	try {
		const result = await runInWorkspaceAsync(['home'], cwd, home, {
			PULSE_OPEN_DRY_RUN: '1',
			PULSE_TRUST_MODE: 'test',
			PULSE_BINDING_REGISTRY_PATH: registryPath,
			PULSE_BINDING_PUBLIC_KEY_PATH: publicKeyPath,
			PULSE_BINDING_ANCHOR_PATH: anchorPath,
			PULSE_HOME_BINDING_LOCK_TIMEOUT_SECONDS: '1',
			PULSE_HOME_REQUEST_TIMEOUT_MS: '50',
		});

		assert.equal(result.status, 1);
		assert.ok(Date.now() - startedAt < 4_000,
			'Memory Home must not inherit the ordinary 30 second binding recovery wait');
		assert.doesNotMatch(result.stdout + result.stderr, /synthetic public key|bindings\.json|bindings\.anchor/);
	} finally {
		release();
	}
});

test('home opens without launching the external Codex program', async (t) => {
  if (process.platform === 'win32') {
    t.skip('portable hanging executable fixture uses a POSIX shebang');
    return;
  }
  const home = mkdtempSync(join(tmpdir(), 'pulse-home-slow-codex-home.'));
  const cwd = mkdtempSync(join(tmpdir(), 'pulse-home-slow-codex-cwd.'));
  const dataDir = join(home, '.pulse');
  const binDir = join(home, 'bin');
	const launchMarker = join(home, 'codex-was-launched');
  mkdirSync(dataDir, { recursive: true });
  mkdirSync(binDir, { recursive: true });
  writeFileSync(join(dataDir, 'secret.key'), 'slow-codex-daemon-secret');
  writeExecutable(join(binDir, 'codex'), [
		`#!${process.execPath}`,
		`require('node:fs').writeFileSync(${JSON.stringify(launchMarker)}, 'launched\\n');`,
		'Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 5000);',
		'',
	].join('\n'));
  const stub = await withPulseStub((req) => ({
    body: {
      cookie_name: 'pulse_home',
      cookie_value: 'slow-codex-browser-session',
      cookie_path: testHomeRoutePath,
      max_age_seconds: 30,
      target_url: `http://${req.headers.host}${testHomeRoutePath}`,
    },
  }));
  const startedAt = Date.now();

  try {
    const result = await runInWorkspaceAsync([
      'home', '--host', 'codex', '--base', stub.baseUrl, '--data-dir', dataDir,
    ], cwd, home, {
      PATH: `${binDir}:/usr/bin:/bin`,
      PULSE_OPEN_DRY_RUN: '1',
    });

    assert.equal(result.status, 0, result.stderr);
		assert.ok(Date.now() - startedAt < 2_000, 'Memory Home must not launch external Codex inspection');
		assert.equal(existsSync(launchMarker), false, 'pulse home launched the external Codex program');
    assert.equal(result.stdout, '[pulse] Memory Home opened.\n');
    assert.doesNotMatch(result.stdout + result.stderr, /slow-codex-daemon-secret|slow-codex-browser-session/);
  } finally {
    await stub.close();
  }
});

test('viewer fails closed when product activation evidence exists but binding trust is broken', () => {
	const home = mkdtempSync(join(tmpdir(), 'pulse-viewer-product-home.'));
	const cwd = mkdtempSync(join(tmpdir(), 'pulse-viewer-product-cwd.'));
	spawnSync('/usr/bin/git', ['init', '-q'], { cwd, encoding: 'utf8' });
	mkdirSync(join(cwd, '.claude'), { recursive: true });
	writeFileSync(join(cwd, '.claude', 'settings.local.json'), JSON.stringify({
		hooks: { Stop: [{ hooks: [{
			type: 'command',
			command: "'/Users/example/.pulse/runtime/codex/current/src/cli.js' claude-hook Stop",
		}] }] },
	}));

	const result = runInWorkspace(['viewer', '--print-url'], cwd, home);

	assert.equal(result.status, 1);
	assert.match(`${result.stdout}${result.stderr}`, /product activation exists, but its bound vault cannot be trusted/);
	assert.doesNotMatch(result.stdout, /127\.0\.0\.1:18789\/viewer/);
});

test('viewer keeps legacy Claude hooks on Local Preview when no product activation exists', () => {
	const home = mkdtempSync(join(tmpdir(), 'pulse-viewer-preview-home.'));
	const cwd = mkdtempSync(join(tmpdir(), 'pulse-viewer-preview-cwd.'));
	spawnSync('/usr/bin/git', ['init', '-q'], { cwd, encoding: 'utf8' });
	mkdirSync(join(cwd, '.claude'), { recursive: true });
	writeFileSync(join(cwd, '.claude', 'settings.local.json'), JSON.stringify({
		hooks: { Stop: [{ hooks: [{ type: 'command', command: 'pulse hook stop' }] }] },
	}));

	const result = runInWorkspace(['viewer', '--print-url'], cwd, home);

	assert.equal(result.status, 0, result.stderr);
	assert.match(result.stdout, /127\.0\.0\.1:18789\/viewer\?key=/);
});

test('viewer can target an explicit live server data dir and base URL', () => {
  const dataDir = mkdtempSync(join(tmpdir(), 'pulse-live-viewer-data.'));
  writeFileSync(join(dataDir, 'secret.key'), 'live-secret');

  const { result } = run([
    'viewer',
    '--base',
    'http://127.0.0.1:18888',
    '--data-dir',
    dataDir,
  ]);

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /http:\/\/127\.0\.0\.1:18888\/viewer\?key=live-secret/);
  assert.match(result.stdout, /data dir:/);
  assert.match(result.stdout, new RegExp(dataDir.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
});

test('viewer can target an explicit thread id', () => {
  const dataDir = mkdtempSync(join(tmpdir(), 'pulse-thread-viewer-data.'));
  writeFileSync(join(dataDir, 'secret.key'), 'thread-secret');

  const { result } = run([
    'viewer',
    '--base',
    'http://127.0.0.1:18890',
    '--data-dir',
    dataDir,
    '--thread-id',
    'archive-import',
  ]);

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /thread_id=archive-import/);
  assert.match(result.stdout, /thread id: archive-import/);
});

test('viewer can open the local memory browser page', () => {
  const dataDir = mkdtempSync(join(tmpdir(), 'pulse-open-viewer-data.'));
  writeFileSync(join(dataDir, 'secret.key'), 'open-secret');

  const { result } = run([
    'viewer',
    '--base',
    'http://127.0.0.1:18889',
    '--data-dir',
    dataDir,
    '--open',
  ], {
    PULSE_OPEN_DRY_RUN: '1',
  });

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /opened browser/);
  assert.match(result.stdout, /http:\/\/127\.0\.0\.1:18889\/viewer\?key=open-secret/);
});

test('viewer suggests detected daemon data dir when the live server rejects the local key', async () => {
  const liveDir = mkdtempSync(join(tmpdir(), 'pulse-live-daemon-data.'));
  const stub = await withPulseStub(() => ({
    status: 401,
    body: { error: 'unauthorized' },
  }));

  try {
    const result = await runAsync(['viewer', '--base', stub.baseUrl, '--thread-id', 'archive-import'], {
      PULSE_VIEWER_SKIP_AUTH_CHECK: '0',
      PULSE_RUNNING_PULSE_COMMAND: `pulse -addr ${new URL(stub.baseUrl).host} -data-dir ${liveDir}`,
    });

    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /viewer auth check failed/);
    assert.match(result.stdout, /Detected Pulse daemon data dir/);
    assert.match(result.stdout, new RegExp(liveDir.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
    assert.match(result.stdout, /pulse viewer --base/);
  } finally {
    await stub.close();
  }
});

test('hook session-start prints resume block from Pulse', async () => {
  const stub = await withPulseStub((req) => {
    assert.equal(req.url, '/continuity/resume');
    return {
      body: {
        resume_markdown: '# Pulse Resume\n## Where we left off\n- Continue Pulse continuity.',
      },
    };
  });
  try {
    const result = await runAsync(['hook', 'session-start'], {
      PULSE_BASE_URL: stub.baseUrl,
      PULSE_HOOK_STRICT: '1',
      PULSE_THREAD_ID: 'pulse-distribution',
    });

    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /# Pulse Resume/);
    assert.equal(stub.requests[0].body.thread_id, 'pulse-distribution');
  } finally {
    await stub.close();
  }
});

test('hook stop writes checkpoint with open loop and state signal', async () => {
  const stub = await withPulseStub((req, body) => {
    assert.equal(req.url, '/continuity/checkpoint');
    assert.equal(body.event_type, undefined);
    assert.equal(body.summary, 'We chose Pulse Auto Continuity as the v1 wedge.');
    assert.deepEqual(body.decisions, ['Pulse owns continuity.']);
    assert.deepEqual(body.open_loops, ['Harden viewer auth.']);
    assert.deepEqual(body.do_not_repeat, ['Do not pitch generic graph memory.']);
    assert.deepEqual(body.emotional_anchors, ['The Bob call changed the product wedge.']);
    return { body: { ok: true } };
  });
  try {
    const result = await runAsync(['hook', 'stop'], {
      PULSE_BASE_URL: stub.baseUrl,
      PULSE_HOOK_STRICT: '1',
      PULSE_THREAD_ID: 'pulse-distribution',
    }, JSON.stringify({
      summary: 'We chose Pulse Auto Continuity as the v1 wedge.',
      decisions: ['Pulse owns continuity.'],
      open_loops: ['Harden viewer auth.'],
      do_not_repeat: ['Do not pitch generic graph memory.'],
      emotional_anchors: ['The Bob call changed the product wedge.'],
      state_signals: ['User wants no re-explaining.'],
    }));

    assert.equal(result.status, 0, result.stderr);
    assert.equal(stub.requests.length, 1);
  } finally {
    await stub.close();
  }
});

test('post-tool hook preserves approved pulse_remember capsule summaries as redacted observations', async () => {
  const seen = [];
  const stub = await withPulseStub((req, body) => {
    assert.equal(req.url, '/continuity/observe');
    seen.push(body);
    assert.equal(body.event_type, 'PostToolUse');
    assert.equal(body.raw_ref, undefined);
    return { body: { ok: true } };
  });
  try {
    const result = await runAsync(['hook', 'post-tool-use'], {
      PULSE_BASE_URL: stub.baseUrl,
      PULSE_HOOK_STRICT: '1',
      PULSE_THREAD_ID: 'pulse-distribution',
    }, JSON.stringify({
      tool_name: 'mcp__pulse__pulse_remember',
      tool_input: {
        schema: 'pulse.memory_capsule.v1',
        source: {
          host: 'claude-code',
          conversation_scope: 'current_turn',
          timestamp: '2026-06-07T00:30:00Z',
        },
        items: [{
          kind: 'decision',
          redacted_summary: 'Pulse keeps the thread: structured memories, never raw transcripts, wipe always available.',
          confidence: 1,
          evidence_hint: 'current_turn',
          privacy_tier: 'private',
          retention: 'project',
          tags: ['model_e2e', 'atlas', 'people_graph'],
        }],
        raw_input_included: false,
      },
    }));

    assert.equal(result.status, 0, result.stderr);
    assert.equal(seen.length, 1);
    assert.equal(seen[0].redacted_summary, 'Decision: Pulse keeps the thread: structured memories, never raw transcripts, wipe always available.');
    assert.match(seen[0].source_ref, /^pulse:hook:PostToolUse:/);
  } finally {
    await stub.close();
  }
});

test('prompt and generic tool hooks do not persist raw prompt text', async () => {
  const seen = [];
  const stub = await withPulseStub((req, body) => {
    assert.equal(req.url, '/continuity/observe');
    seen.push(body);
    assert.doesNotMatch(body.redacted_summary, /viewer trust layer/);
    assert.doesNotMatch(body.redacted_summary, /Please remember/);
    assert.equal(body.raw_ref, undefined);
    return { body: { ok: true } };
  });
  try {
    for (const hook of ['user-prompt-submit', 'post-tool-use']) {
      const result = await runAsync(['hook', hook], {
        PULSE_BASE_URL: stub.baseUrl,
        PULSE_HOOK_STRICT: '1',
        PULSE_THREAD_ID: 'pulse-distribution',
      }, JSON.stringify({
        prompt: 'Please remember that the viewer trust layer must show what Pulse will inject next.',
        tool_name: 'Read',
        tool_input: { file_path: '/home/example/private.txt' },
      }));
      assert.equal(result.status, 0, result.stderr);
    }
    assert.deepEqual(seen.map((body) => body.event_type), ['UserPromptSubmit', 'PostToolUse']);
    assert.equal(seen[0].redacted_summary, 'Prompt noticed. Raw text is hidden by default.');
    assert.equal(seen[1].redacted_summary, 'Tool ran: Read. Raw tool input and output are hidden by default.');
  } finally {
    await stub.close();
  }
});

test('migrate preview scans a ChatGPT export without writing raw text', () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-chatgpt-export.'));
  writeFileSync(join(exportDir, 'conversations.json'), JSON.stringify([
    {
      title: 'Garden launch',
      create_time: 1780000000,
      mapping: {
        user: {
          message: {
            author: { role: 'user' },
            content: { parts: ['Remember Bob follow-up for Pulse packaging.'] },
          },
        },
        assistant: {
          message: {
            author: { role: 'assistant' },
            content: { parts: ['We should keep Pulse MCP, not Pulse Claude.'] },
          },
        },
      },
    },
  ]));

  const { result } = run(['migrate', 'preview', exportDir]);

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /\[pulse\] migration preview/);
  assert.match(result.stdout, /source: chatgpt/);
  assert.match(result.stdout, /conversations: 1/);
  assert.match(result.stdout, /messages: 2/);
  assert.match(result.stdout, /people found: Bob/);
  assert.match(result.stdout, /thread candidates: Garden launch/);
  assert.match(result.stdout, /raw text will not be written/);
});

test('migrate preview does not promote generic sentence starters as people', () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-generic-person-word.'));
  writeFileSync(join(exportDir, 'conversations.json'), JSON.stringify([
    {
      title: 'Simple reminder',
      mapping: {
        user: {
          message: {
            author: { role: 'user' },
            content: { parts: ['Please remind Alice about the review.'] },
          },
        },
      },
    },
  ]));

  const { result } = run(['migrate', 'preview', exportDir, '--json']);

  assert.equal(result.status, 0, result.stderr);
  const preview = JSON.parse(result.stdout);
  assert.deepEqual(preview.people_candidates.includes('Alice'), true);
  assert.deepEqual(preview.people_candidates.includes('Please'), false);
});

test('migrate preview can scan a zipped ChatGPT export directly', () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-chatgpt-zip-export.'));
  const archiveDir = join(exportDir, 'archive');
  mkdirSync(archiveDir, { recursive: true });
  writeFileSync(join(archiveDir, 'conversations.json'), JSON.stringify([
    {
      title: 'Pulse archive request',
      mapping: {
        user: {
          message: {
            content: { parts: ['Bob should see the one-click archive import flow.'] },
          },
        },
      },
    },
  ]));
  const zipPath = join(exportDir, 'chatgpt-export.zip');
  const zipped = spawnSync('zip', ['-qr', zipPath, '.'], {
    cwd: archiveDir,
    encoding: 'utf8',
  });
  assert.equal(zipped.status, 0, zipped.stderr);

  const { result } = run(['migrate', 'preview', zipPath, '--json']);

  assert.equal(result.status, 0, result.stderr);
  const preview = JSON.parse(result.stdout);
  assert.equal(preview.source, 'chatgpt');
  assert.equal(preview.conversations, 1);
  assert.equal(preview.messages, 1);
  assert.equal(preview.archive_was_unpacked, true);
  assert.deepEqual(preview.people_candidates.includes('Bob'), true);
  assert.deepEqual(preview.raw_text_written, false);
});

test('migrate preview json scans Claude-like exports and redacts secrets', () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-claude-export.'));
  writeFileSync(join(exportDir, 'conversations.json'), JSON.stringify([
    {
      name: 'Pulse Distribution',
      chat_messages: [
        { sender: 'human', text: 'Alice should review the Pulse graph viewer.' },
        { sender: 'assistant', text: 'Do not store /home/example/private or sk-secret in Pulse.' },
      ],
    },
  ]));

  const { result } = run(['migrate', 'preview', exportDir, '--json']);

  assert.equal(result.status, 0, result.stderr);
  assert.doesNotMatch(result.stdout, /\/home\/example|sk-secret/);
  const preview = JSON.parse(result.stdout);
  assert.equal(preview.ok, true);
  assert.equal(preview.source, 'claude');
  assert.equal(preview.conversations, 1);
  assert.equal(preview.messages, 2);
  assert.deepEqual(preview.raw_text_written, false);
  assert.deepEqual(preview.people_candidates.includes('Alice'), true);
  assert.deepEqual(preview.thread_candidates.includes('Pulse Distribution'), true);
});

test('migrate preview emits human-readable relationship labels', () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-readable-relationships.'));
  writeFileSync(join(exportDir, 'conversations.json'), JSON.stringify([
    {
      title: 'Pulse Dashboard',
      mapping: {
        a: {
          message: {
            content: { parts: ['Bob and Alice should review the Pulse Dashboard together.'] },
          },
        },
      },
    },
  ]));

  const { result } = run(['migrate', 'preview', exportDir, '--json']);

  assert.equal(result.status, 0, result.stderr);
  const preview = JSON.parse(result.stdout);
  assert.deepEqual(preview.relationship_candidates.includes('Bob - mentioned in - Pulse Dashboard'), true);
  assert.deepEqual(preview.relationship_candidates.includes('Bob - related to - Alice'), true);
  assert.doesNotMatch(JSON.stringify(preview.relationship_candidates), /->|<->|mentioned_in|related_to/);
});

test('migrate preview does not invent alias equivalence without a curated people graph', () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-alias-self-relationships.'));
  writeFileSync(join(exportDir, 'conversations.json'), JSON.stringify([
    {
      title: 'Alias cleanup',
      mapping: {
        a: {
          message: {
            content: { parts: ['Alex and Алекс are the same person. Alice is a separate person.'] },
          },
        },
      },
    },
  ]));

  const { result } = run(['migrate', 'preview', exportDir, '--json']);

  assert.equal(result.status, 0, result.stderr);
  const preview = JSON.parse(result.stdout);
  assert.deepEqual(preview.relationship_candidates.includes('Alex - related to - Alice'), true);
  assert.deepEqual(preview.relationship_candidates.includes('Alex - related to - Алекс'), true);
  assert.deepEqual(preview.relationship_candidates.includes('Алекс - related to - Alice'), true);
});

test('migrate preview scans Claude conversations exports larger than the old 50MB guard', () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-large-claude-export.'));
  const padding = 'x'.repeat((50 * 1024 * 1024) + 1024);
  writeFileSync(join(exportDir, 'conversations.json'), JSON.stringify([
    {
      name: 'Large Claude archive',
      ignored_padding: padding,
      chat_messages: [
        { sender: 'human', text: 'Bob should verify the large Claude archive preview.' },
      ],
    },
  ]));

  const { result } = run(['migrate', 'preview', exportDir, '--json']);

  assert.equal(result.status, 0, result.stderr);
  const preview = JSON.parse(result.stdout);
  assert.equal(preview.source, 'claude');
  assert.equal(preview.files_skipped.length, 0);
  assert.equal(preview.conversations, 1);
  assert.equal(preview.messages, 1);
  assert.deepEqual(preview.people_candidates.includes('Bob'), true);
  assert.deepEqual(preview.raw_text_written, false);
});

test('migrate preview keeps Claude as source when archive has unknown sidecar chats', () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-claude-sidecar-export.'));
  const sidecars = join(exportDir, 'design_chats');
  mkdirSync(sidecars, { recursive: true });
  writeFileSync(join(exportDir, 'conversations.json'), JSON.stringify([
    {
      name: 'Claude main archive',
      chat_messages: [
        { sender: 'human', text: 'Bob should review the Claude main archive.' },
      ],
    },
  ]));
  writeFileSync(join(sidecars, 'sidecar.json'), JSON.stringify({
    name: 'Design sidecar',
    messages: [
      { text: 'Alice appears in a sidecar chat without host metadata.' },
    ],
  }));

  const { result } = run(['migrate', 'preview', exportDir, '--json']);

  assert.equal(result.status, 0, result.stderr);
  const preview = JSON.parse(result.stdout);
  assert.equal(preview.source, 'claude');
  assert.equal(preview.conversations, 2);
  assert.equal(preview.messages, 2);
  assert.deepEqual(preview.people_candidates.includes('Bob'), true);
  assert.deepEqual(preview.people_candidates.includes('Alice'), true);
});

test('migrate preview fails clearly for a missing archive path', () => {
  const { result } = run(['migrate', 'preview', '/tmp/pulse-missing-export-folder']);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /archive path does not exist/);
});

test('migrate preview scans Nexus markdown conversations without importing reports or compiled duplicates', () => {
  const nexusDir = mkdtempSync(join(tmpdir(), 'pulse-nexus-history.'));
  const conversations = join(nexusDir, 'Conversations', 'chatgpt', '2025', '09');
  const reports = join(nexusDir, 'Reports', 'chatgpt');
  const compiled = join(nexusDir, 'Conversations', 'chatgpt', 'compiled_dialogs');
  mkdirSync(conversations, { recursive: true });
  mkdirSync(reports, { recursive: true });
  mkdirSync(compiled, { recursive: true });
  writeFileSync(join(conversations, 'Pulse MCP people graph.md'), `---
nexus: nexus-ai-chat-importer
provider: chatgpt
conversation_id: nexus-real-chat
create_time: 2025-09-01T10:00:00Z
---

# Title: Pulse MCP people graph

>[!nexus_user] **User** - 2025-09-01 10:00
> This raw Nexus sentence must stay hidden. Bob and Alice should inspect the people graph.

>[!nexus_agent] **Assistant** - 2025-09-01 10:01
> Pulse should keep the graph preview gentle and structured.
`);
  writeFileSync(join(reports, 'report.md'), `# Nexus AI Chat Importer Report

>[!nexus_user] **User**
> Browser Connector Qwen should not be imported from reports.
`);
  writeFileSync(join(compiled, 'all-dialogs.md'), `---
nexus: nexus-ai-chat-importer
provider: chatgpt
---

# Title: compiled duplicate

>[!nexus_user] **User**
> Oldperson should not be selected from compiled duplicates.
`);

  const { result } = run(['migrate', 'preview', nexusDir, '--json']);

  assert.equal(result.status, 0, result.stderr);
  assert.doesNotMatch(result.stdout, /This raw Nexus sentence must stay hidden/);
  const preview = JSON.parse(result.stdout);
  assert.equal(preview.source, 'chatgpt');
  assert.equal(preview.files_scanned, 1);
  assert.equal(preview.conversations, 1);
  assert.equal(preview.messages, 2);
  assert.deepEqual(preview.people_candidates.includes('Bob'), true);
  assert.deepEqual(preview.people_candidates.includes('Alice'), true);
  assert.deepEqual(preview.people_candidates.includes('Oldperson'), false);
  assert.deepEqual(preview.review_candidates.includes('Qwen'), false);
  assert.deepEqual(preview.thread_candidates.includes('Pulse MCP people graph'), true);
  assert.deepEqual(preview.raw_text_written, false);
});

test('migrate preview scans Codex JSONL session files', () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-codex-history.'));
  const nested = join(exportDir, '2026', '06', '03');
  mkdirSync(nested, { recursive: true });
  writeFileSync(join(nested, 'rollout-test.jsonl'), [
    JSON.stringify({ timestamp: '2026-06-03T00:00:00Z', type: 'session_meta', payload: { id: 'codex-session' } }),
    JSON.stringify({
      timestamp: '2026-06-03T00:01:00Z',
      type: 'response_item',
      payload: {
        type: 'message',
        role: 'user',
        content: [{ type: 'input_text', text: 'Ask Bob about the Pulse graph migrator.' }],
      },
    }),
    JSON.stringify({
      timestamp: '2026-06-03T00:02:00Z',
      type: 'response_item',
      payload: {
        type: 'message',
        role: 'assistant',
        content: [{ type: 'output_text', text: 'Keep the graph commit behind explicit confirmation.' }],
      },
    }),
  ].join('\n'));

  const { result } = run(['migrate', 'preview', exportDir, '--json']);

  assert.equal(result.status, 0, result.stderr);
  const preview = JSON.parse(result.stdout);
  assert.equal(preview.source, 'codex');
  assert.equal(preview.conversations, 1);
  assert.equal(preview.messages, 2);
  assert.deepEqual(preview.people_candidates.includes('Bob'), true);
  assert.deepEqual(preview.people_candidates.includes('Keep'), false);
  assert.deepEqual(preview.raw_text_written, false);
});

test('migrate preview filters harness words from person candidates', () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-codex-noisy-history.'));
  writeFileSync(join(exportDir, 'session.jsonl'), [
    JSON.stringify({
    type: 'response_item',
    payload: {
      type: 'message',
      role: 'user',
      content: [{
        type: 'input_text',
          text: 'Filesystem Network Approvals Default However Planning Task Agent Read Bash Updated Users-example Check Сейчас Use Phase Output File Opus Sonnet Asia Location Error Usage May Mem Hello Record Create Failed Invalid Full After Command Work Async Pro Pages Один Когда Три Сначала Только Потом Telegram Hermes Atlas Both Pass Python Project Bundle Confirmed Not Architecture Extraction Test Tests Russian Complete Correction Live Source All Conversation Added Verified Verify Section Waiting Apr June Prompt Server Context Heart Chat Demo Companion Evidence First Script Gateway Build Cloudflare Final Fix Run Running Files Port Worker Created History Primary Hearth Bitwarden Library Caches Applications Bench Content Map Module Акт Готово Просто Жду Через Всё После Пауза Запускаю Два Проверю Его Мне Цель Для Тихо Ещё Медленно Привет Карта Мои Напиши Приоритет Или Чтобы Now. Bob should review Pulse. Bob is the real person here.',
      }],
    },
  }),
    JSON.stringify({
      type: 'response_item',
      payload: {
        type: 'message',
        role: 'assistant',
        content: [{ type: 'output_text', text: 'Bob remains the relevant person candidate.' }],
      },
    }),
  ].join('\n'));

  const { result } = run(['migrate', 'preview', exportDir, '--json']);

  assert.equal(result.status, 0, result.stderr);
  const preview = JSON.parse(result.stdout);
  assert.equal(preview.people_candidates[0], 'Bob');
  for (const noise of ['Filesystem', 'Network', 'Approvals', 'Default', 'However', 'Planning', 'Task', 'Agent', 'Read', 'Bash', 'Import', 'Keep', 'Graph', 'Viewer', 'Preview', 'Updated', 'Users-example', 'Check', 'Сейчас', 'Use', 'Phase', 'Output', 'File', 'Opus', 'Sonnet', 'Asia', 'Location', 'Error', 'Usage', 'May', 'Mem', 'Hello', 'Record', 'Create', 'Failed', 'Invalid', 'Full', 'After', 'Command', 'Work', 'Async', 'Pro', 'Pages', 'Один', 'Когда', 'Три', 'Сначала', 'Только', 'Потом', 'Telegram', 'Hermes', 'Atlas', 'Both', 'Pass', 'Python', 'Project', 'Bundle', 'Confirmed', 'Not', 'Architecture', 'Extraction', 'Test', 'Tests', 'Russian', 'Complete', 'Correction', 'Live', 'Source', 'All', 'Conversation', 'Added', 'Verified', 'Verify', 'Section', 'Waiting', 'Apr', 'June', 'Prompt', 'Server', 'Context', 'Heart', 'Chat', 'Demo', 'Companion', 'Evidence', 'First', 'Script', 'Gateway', 'Build', 'Cloudflare', 'Final', 'Fix', 'Run', 'Running', 'Files', 'Port', 'Worker', 'Created', 'History', 'Primary', 'Hearth', 'Bitwarden', 'Library', 'Caches', 'Applications', 'Bench', 'Content', 'Map', 'Module', 'Акт', 'Готово', 'Просто', 'Жду', 'Через', 'Всё', 'После', 'Пауза', 'Запускаю', 'Два', 'Проверю', 'Его', 'Мне', 'Цель', 'Для', 'Тихо', 'Ещё', 'Медленно', 'Привет', 'Карта', 'Мои', 'Напиши', 'Приоритет', 'Или', 'Чтобы', 'Now']) {
    assert.equal(preview.people_candidates.includes(noise), false, `${noise} should be filtered`);
  }
  assert.deepEqual(preview.fun_fact_candidates.some((fact) => /^Bob appeared/.test(fact)), true);
});

test('migrate preview keeps repeated generic words out of review candidates', () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-generic-word-history.'));
  writeFileSync(join(exportDir, 'conversations.json'), JSON.stringify([
    {
      title: 'Generic entity cleanup',
      mapping: Object.fromEntries(Array.from({ length: 14 }, (_, index) => [
        `m${index}`,
        {
          message: {
            content: {
              parts: [
                'Google Data Step Background Real Process Before Total Two Three Choose Privacy Avoid Built Автор Explore Guidance Heavy Аудитория Курс Тематика Conversations Benchmark Consulting should not become review entities. Bob is real.',
              ],
            },
          },
        },
      ])),
    },
  ]));

  const { result } = run(['migrate', 'preview', exportDir, '--json']);

  assert.equal(result.status, 0, result.stderr);
  const preview = JSON.parse(result.stdout);
  assert.deepEqual(preview.people_candidates.includes('Bob'), true);
  for (const noise of ['Google', 'Data', 'Step', 'Background', 'Real', 'Process', 'Before', 'Total', 'Two', 'Three', 'Choose', 'Privacy', 'Avoid', 'Built', 'Автор', 'Explore', 'Guidance', 'Heavy', 'Аудитория', 'Курс', 'Тематика', 'Conversations', 'Benchmark', 'Consulting']) {
    assert.equal(preview.people_candidates.includes(noise), false, `${noise} should not be a person`);
    assert.equal(preview.review_candidates.includes(noise), false, `${noise} should not be review queue noise`);
  }
});

test('migrate preview scans Claude Code JSONL project files', () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-claude-code-history.'));
  writeFileSync(join(exportDir, 'session.jsonl'), [
    JSON.stringify({
      type: 'user',
      operation: 'message',
      timestamp: '2026-06-03T00:01:00Z',
      sessionId: 'claude-code-session',
      content: { text: 'Alice should review the beautiful Pulse people viewer.' },
    }),
    JSON.stringify({
      type: 'assistant',
      operation: 'message',
      timestamp: '2026-06-03T00:02:00Z',
      sessionId: 'claude-code-session',
      content: [{ type: 'text', text: 'The viewer should show fun facts and relationships.' }],
    }),
  ].join('\n'));

  const { result } = run(['migrate', 'preview', exportDir, '--json']);

  assert.equal(result.status, 0, result.stderr);
  const preview = JSON.parse(result.stdout);
  assert.equal(preview.source, 'claude-code');
  assert.equal(preview.conversations, 1);
  assert.equal(preview.messages, 2);
  assert.deepEqual(preview.people_candidates.includes('Alice'), true);
  assert.deepEqual(preview.thread_candidates.length, 1);
});

test('migrate preview hides technical session ids and noisy inflections from human graph', () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-claude-code-human-clean.'));
  const technicalSession = '09c3f230-a42f-4dc7-a27e-4e60cc4f01d4';
  writeFileSync(join(exportDir, `agent-a3d743d1a393f1e74.jsonl`), [
    JSON.stringify({
      type: 'user',
      operation: 'message',
      timestamp: '2026-06-03T00:01:00Z',
      sessionId: technicalSession,
      content: {
        text: 'Мария напомнила Александру: Назови движение иначе. Browser Connector Telethon Cron Timestamps True Move Core System Session Retrieval Observations are tools, not people. Александр and Bob are the actual people here.',
      },
    }),
    JSON.stringify({
      type: 'assistant',
      operation: 'message',
      timestamp: '2026-06-03T00:02:00Z',
      sessionId: technicalSession,
      content: [{ type: 'text', text: 'Мария and Bob should remain human candidates; Обращ, Движение, Browser, Connector, Telethon, Core, and System should not.' }],
    }),
  ].join('\n'));

  const { result } = run(['migrate', 'preview', exportDir, '--json']);

  assert.equal(result.status, 0, result.stderr);
  assert.doesNotMatch(result.stdout, new RegExp(technicalSession));
  const preview = JSON.parse(result.stdout);
  assert.equal(preview.source, 'claude-code');
  assert.deepEqual(preview.people_candidates.includes('Мария'), true);
  assert.deepEqual(preview.people_candidates.includes('Александр'), true);
  assert.deepEqual(preview.people_candidates.includes('Bob'), true);
  for (const noise of ['Марии', 'Александру', 'Назови', 'Движение', 'Обращ', 'Browser', 'Connector', 'Telethon', 'Cron', 'Timestamps', 'True', 'Move', 'Core', 'System', 'Session', 'Retrieval', 'Observations']) {
    assert.equal(preview.people_candidates.includes(noise), false, `${noise} should not be a promoted person`);
    assert.equal(preview.review_candidates.includes(noise), false, `${noise} should not be review noise`);
  }
  assert.deepEqual(preview.memory_candidates, ['Claude Code session: 2 source snippets']);
  assert.deepEqual(preview.relationship_candidates.every((item) => !item.includes(technicalSession) && !/->|<->/.test(item)), true);
});

test('migrate preview can write a safe browser HTML profile preview', () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-html-preview-export.'));
  const htmlPath = join(exportDir, 'pulse-preview.html');
  writeFileSync(join(exportDir, 'conversations.json'), JSON.stringify([
    {
      title: 'Garden launch',
      mapping: {
        user: {
          message: {
            author: { role: 'user' },
            content: {
              parts: [
                'Remember Bob and Alice are reviewing Pulse. Qwen, Cartographer, Cinema, and Dossier are not people. Alex felt relief after the archive migrator plan.',
              ],
            },
          },
        },
        assistant: {
          message: {
            author: { role: 'assistant' },
            content: { parts: ['We should keep raw chat text out of the browser preview.'] },
          },
        },
      },
    },
  ]));

  const { result } = run(['migrate', 'preview', exportDir, '--html', htmlPath]);

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /migration HTML preview/);
  assert.match(result.stdout, /pulse-preview\.html/);

  const html = readFileSync(htmlPath, 'utf8');
  assert.match(html, /Pulse Import Preview/);
  assert.match(html, /data-design="pulse-glass-v3"/);
  assert.match(html, /--pastel-a:/);
  assert.match(html, /backdrop-filter:blur/);
  assert.match(html, /glass-card/);
  assert.match(html, /Pulse Import Preview/);
  assert.match(html, /Thread preview/);
	assert.match(html, /Archive sizing/);
  assert.match(html, /Needs your decision/);
  assert.doesNotMatch(html, /Charter|Georgia|--paper:#f7f2e8|background-size:36px 36px|#151a1c/);
  assert.match(html, /What Pulse will turn into continuity/);
  assert.match(html, /Threads/);
  assert.match(html, /Decisions/);
  assert.match(html, /Open loops/);
  assert.match(html, /Do-not-repeat/);
  assert.match(html, /Emotional anchors/);
  assert.match(html, /People found/);
  assert.match(html, /Filter preview/);
  assert.match(html, /id="preview-filter"/);
	assert.match(html, /structured candidates<\/span>/);
	assert.doesNotMatch(html, /estimated saved|estimated raw/i);
  assert.match(html, /source snippets<\/span>/);
  assert.match(html, /person-card/);
  assert.match(html, /Evidence/);
  assert.match(html, /Related continuity/);
  assert.match(html, /Bob/);
  assert.match(html, /Alice/);
  assert.match(html, /Needs your decision/);
  assert.match(html, /Qwen/);
  assert.match(html, /Cartographer/);
  assert.match(html, /Cinema/);
  assert.match(html, /Dossier/);
  assert.match(html, /Review: Qwen/);
  assert.match(html, /Review: Cartographer/);
  assert.doesNotMatch(html, /<h3>Qwen<\/h3>/);
  assert.doesNotMatch(html, /<h3>Cartographer<\/h3>/);
  assert.doesNotMatch(html, /<h3>Cinema<\/h3>/);
  assert.doesNotMatch(html, /<h3>Dossier<\/h3>/);
  assert.doesNotMatch(html, /Qwen appeared in \d+ safe preview signal/);
  assert.doesNotMatch(html, /Cartographer appeared in \d+ safe preview signal/);
  assert.doesNotMatch(html, /Cinema appeared in \d+ safe preview signal/);
  assert.doesNotMatch(html, /Dossier appeared in \d+ safe preview signal/);
  assert.match(html, /Bob appeared in 1 bounded source snippet/);
  assert.match(html, /Bob - mentioned in - Garden launch/);
  assert.doesNotMatch(html, /Bob -&gt; Garden launch|Bob &lt;-&gt; Alice/);
  assert.match(html, /relief/i);
  assert.match(html, /Raw chat text was not written/);
  assert.doesNotMatch(html, /Alex felt relief after the archive migrator plan/);
  assert.doesNotMatch(html, /fun facts/i);
  assert.doesNotMatch(html, /people, memories, emotions, relationships/i);
  assert.doesNotMatch(html, /safe message signals/i);
});

test('migrate preview json exposes staged import flow', () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-staged-import-preview.'));
  writeFileSync(join(exportDir, 'conversations.json'), JSON.stringify([
    {
      title: 'Pulse MCP distribution',
      mapping: {
        user: {
          message: {
            author: { role: 'user' },
            content: { parts: ['Remember Bob should inspect the Pulse MCP developer preview before public claims.'] },
          },
        },
        assistant: {
          message: {
            author: { role: 'assistant' },
            content: { parts: ['Decision: keep import behind a review gate and do not store raw transcripts.'] },
          },
        },
        user2: {
          message: {
            author: { role: 'user' },
            content: { parts: ['Make the Pulse insight actionable: mark the active thread before import.'] },
          },
        },
      },
    },
    {
      title: 'Garden demo UX',
      mapping: {
        user: {
          message: {
            author: { role: 'user' },
            content: { parts: ['Open loop: redesign source scanner around sessions, candidate threads, review actions, and import gate.'] },
          },
        },
      },
    },
  ]));

  const { result } = run(['migrate', 'preview', exportDir, '--json']);

  assert.equal(result.status, 0, result.stderr);
  assert.doesNotMatch(result.stdout, /Remember Bob should inspect|Decision: keep import/);
  const preview = JSON.parse(result.stdout);
  assert.equal(preview.flow, 'pulse.import_preview.v2');
  assert.equal(preview.source_scan.status, 'scanned');
  assert.equal(preview.source_scan.source, 'chatgpt');
  assert.equal(preview.source_scan.files_scanned, 1);
  assert.equal(preview.source_scan.sessions_scanned, 2);
  assert.equal(preview.source_scan.messages_scanned, 4);
  assert.equal(preview.source_scan.raw_text_written, false);
  assert.equal(preview.scanned_sessions.length, 2);
  assert.deepEqual(preview.scanned_sessions.map((session) => session.title), ['Pulse MCP distribution', 'Garden demo UX']);
  assert.deepEqual(preview.scanned_sessions.map((session) => session.message_count), [3, 1]);
  assert.equal(preview.scanned_sessions.every((session) => session.evidence_ref && !session.evidence_ref.includes('/home/')), true);
  assert.equal(preview.candidate_threads.length, 2);
  assert.deepEqual(preview.candidate_threads.map((thread) => thread.title), ['Pulse MCP distribution', 'Garden demo UX']);
  assert.equal(preview.candidate_threads.every((thread) => thread.privacy_tier === 'private'), true);
	assert.equal(preview.candidate_threads.every((thread) => thread.preview_sizing.source_snippets >= 0), true);
	assert.equal(preview.candidate_threads.every((thread) => thread.preview_sizing.structured_candidates >= 1), true);
	assert.equal(preview.candidate_threads.every((thread) => thread.token_economy === undefined), true);
  assert.equal(preview.candidate_threads.some((thread) => thread.people_found.includes('Bob')), true);
  assert.ok(Array.isArray(preview.pulse_insights));
  assert.equal(preview.pulse_insights.length >= 1, true);
  const distributionInsight = preview.pulse_insights.find((insight) => insight.thread_title === 'Pulse MCP distribution');
  assert.ok(distributionInsight, JSON.stringify(preview.pulse_insights));
  assert.equal(distributionInsight.kind, 'why_this_matters_now');
  assert.equal(distributionInsight.privacy_tier, 'private');
  assert.match(distributionInsight.title, /Why this may matter now/);
  assert.match(distributionInsight.summary, /Pulse MCP distribution/);
  assert.equal(distributionInsight.reasons.some((reason) => /Bob.*Pulse MCP distribution|Pulse MCP distribution.*Bob/i.test(reason)), true);
  assert.equal(distributionInsight.reasons.some((reason) => /1 related person found/i.test(reason)), true);
  assert.doesNotMatch(JSON.stringify(distributionInsight), /1 people found/i);
  assert.match(distributionInsight.suggested_next_step, /Review this thread before import/);
  assert.equal(
    preview.candidate_threads.every((thread) => !thread.people_found.some((name) => ['People', 'Decision', 'Auto', 'Continuity', 'Open'].includes(name))),
    true,
  );
  assert.equal(
    preview.candidate_threads.every((thread) => !thread.people_found.some((name) => ['Make', 'Active'].includes(name))),
    true,
  );
  assert.equal(
    preview.pulse_insights.every((insight) => !insight.related_entities.some((name) => ['Make', 'Active'].includes(name))),
    true,
  );
  assert.equal(preview.import_gate.requires_confirmation, 'import pulse graph');
  assert.equal(preview.import_gate.default_privacy, 'private');
  assert.deepEqual(preview.import_gate.will_not_save.includes('raw_text'), true);
});

test('migrate preview HTML follows source-to-gate flow with local review actions', () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-staged-import-html.'));
  const htmlPath = join(exportDir, 'pulse-preview.html');
  writeFileSync(join(exportDir, 'conversations.json'), JSON.stringify([
    {
      title: 'Pulse import review',
      mapping: {
        user: {
          message: {
            author: { role: 'user' },
            content: { parts: ['Bob and Alice should review Cartographer before the import gate.'] },
          },
        },
        assistant: {
          message: {
            author: { role: 'assistant' },
            content: { parts: ['Pulse should show source scan, sessions, candidate threads, review actions, then import gate.'] },
          },
        },
      },
    },
  ]));

  const { result } = run(['migrate', 'preview', exportDir, '--html', htmlPath]);

  assert.equal(result.status, 0, result.stderr);
  const html = readFileSync(htmlPath, 'utf8');
  const flow = [
    'Sources scanned',
    'Scanned sessions',
    'Candidate threads',
    'Review actions',
    'Import gate',
  ];
  let previous = -1;
  for (const label of flow) {
    const index = html.indexOf(label);
    assert.notEqual(index, -1, `${label} section missing`);
    assert.ok(index > previous, `${label} appears out of order`);
    previous = index;
  }
  assert.match(html, /data-import-flow="pulse.import_preview.v2"/);
  assert.match(html, /flow: "pulse.import_preview.v2"/);
  assert.match(html, /Download reviewed JSON before import/);
  assert.match(html, /reviewed 0 of/);
  assert.match(html, /data-review-action="confirm"/);
  assert.match(html, /data-review-action="ignore"/);
  assert.match(html, /Download reviewed JSON/);
  assert.match(html, /Why this may matter now/);
  assert.doesNotMatch(html, /Why this matters right now/);
  assert.match(html, /Pulse insight/);
  assert.match(html, /Review this thread before import/);
  assert.match(html, /Make active/);
  assert.match(html, /data-active-thread=/);
  assert.match(html, /active_threads/);
  assert.match(html, /pulse-preview\.reviewed\.json/);
  assert.match(html, /review_decisions/);
  assert.match(html, /Nothing is imported until/);
  assert.doesNotMatch(html, /--data-dir\s+\S+/);
  assert.doesNotMatch(html, /\/Users\//);
  assert.match(html, /Will save/);
  assert.match(html, /Will not save/);
  assert.doesNotMatch(html, /Person profiles/);
  assert.doesNotMatch(html, /safe preview signal/i);
});

test('migrate preview HTML shows a seeded demo when source has no conversations yet', () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-empty-preview-export.'));
  const htmlPath = join(exportDir, 'pulse-preview.html');
  writeFileSync(join(exportDir, 'loose-notes.md'), [
    '# Loose notes',
    '',
    'This file is intentionally not a supported chat export.',
    'Pulse should explain the empty source instead of looking broken.',
  ].join('\n'));

  const { result } = run(['migrate', 'preview', exportDir, '--html', htmlPath]);

  assert.equal(result.status, 0, result.stderr);
  const html = readFileSync(htmlPath, 'utf8');
  assert.match(html, /No real data yet/);
  assert.match(html, /Here is how a real thread looks/);
  assert.match(html, /Thread: Pulse MCP preview/);
  assert.match(html, /Atlas must not own the People Graph/);
  assert.match(html, /run Claude Code E2E/);
  assert.match(html, /do not claim production readiness/);
  assert.match(html, /Demo only; not imported/);
  assert.doesNotMatch(html, /safe message signals/i);
  assert.doesNotMatch(html, /fun facts/i);
});

test('migrate preview can write HTML and commit-ready JSON together', () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-preview-out-export.'));
  const htmlPath = join(exportDir, 'pulse-preview.html');
  const jsonPath = join(exportDir, 'pulse-preview.json');
  writeFileSync(join(exportDir, 'conversations.json'), JSON.stringify([
    {
      title: 'Pulse archive flow',
      mapping: {
        user: {
          message: {
            author: { role: 'user' },
            content: { parts: ['Bob should inspect the Pulse people profile preview before import.'] },
          },
        },
      },
    },
  ]));

  const { result } = run([
    'migrate',
    'preview',
    exportDir,
    '--html',
    htmlPath,
    '--out',
    jsonPath,
  ]);

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /migration HTML preview/);
  assert.match(result.stdout, /migration JSON preview/);
  assert.match(result.stdout, /Next:/);
  assert.match(result.stdout, new RegExp(`pulse migrate commit ${jsonPath.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')} --confirm "import pulse graph"`));
  assert.equal(existsSync(htmlPath), true);
  assert.equal(existsSync(jsonPath), true);
  const preview = JSON.parse(readFileSync(jsonPath, 'utf8'));
  assert.equal(preview.raw_text_written, false);
  assert.equal(preview.html_path, htmlPath);
  assert.deepEqual(preview.people_candidates.includes('Bob'), true);
  const html = readFileSync(htmlPath, 'utf8');
  assert.match(html, /Import gate/);
  assert.match(html, /Nothing is imported until/);
  assert.match(html, /Import structured continuity/);
  assert.match(html, /threads, decisions, open loops, do-not-repeat, and emotional anchors into Pulse/);
  assert.match(html, /Open the memory viewer/);
  assert.match(html, /resume context, saved decisions, open loops, and review decisions/);
  assert.match(html, /Copy command/);
  assert.match(html, /pulse migrate commit/);
  assert.match(html, /import pulse graph/);
  assert.match(html, /import pulse graph&quot; --open|import pulse graph" --open/);
  assert.match(html, /pulse viewer/);
  assert.match(html, /data-copy=/);
});

test('migrate preview-latest finds newest host archive in downloads', () => {
  const downloads = mkdtempSync(join(tmpdir(), 'pulse-downloads.'));
  const oldDir = mkdtempSync(join(tmpdir(), 'pulse-old-archive.'));
  const newDir = mkdtempSync(join(tmpdir(), 'pulse-new-archive.'));
  const htmlPath = join(downloads, 'pulse-preview.html');
  const jsonPath = join(downloads, 'pulse-preview.json');
  writeFileSync(join(oldDir, 'conversations.json'), JSON.stringify([
    {
      title: 'Old export',
      mapping: {
        old: {
          message: {
            author: { role: 'user' },
            content: { parts: ['Oldperson should not be selected as latest export.'] },
          },
        },
      },
    },
  ]));
  writeFileSync(join(newDir, 'conversations.json'), JSON.stringify([
    {
      title: 'Latest export',
      mapping: {
        latest: {
          message: {
            author: { role: 'user' },
            content: { parts: ['Bob should be found from the newest ChatGPT archive.'] },
          },
        },
      },
    },
  ]));
  spawnSync('zip', ['-qr', join(downloads, 'chatgpt-export-old.zip'), '.'], { cwd: oldDir });
  const newZip = join(downloads, 'chatgpt-export-new.zip');
  spawnSync('zip', ['-qr', newZip, '.'], { cwd: newDir });

  const { result } = run([
    'migrate',
    'preview-latest',
    'chatgpt',
    '--downloads',
    downloads,
    '--html',
    htmlPath,
    '--out',
    jsonPath,
  ]);

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /latest chatgpt archive/);
  assert.match(result.stdout, /chatgpt-export-new\.zip/);
  assert.match(result.stdout, /migration HTML preview/);
  assert.match(result.stdout, /migration JSON preview/);
  const preview = JSON.parse(readFileSync(jsonPath, 'utf8'));
  assert.equal(preview.source, 'chatgpt');
  assert.match(preview.next, new RegExp(`pulse migrate commit ${jsonPath.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')} --confirm "import pulse graph"`));
  assert.deepEqual(preview.people_candidates.includes('Bob'), true);
  assert.deepEqual(preview.people_candidates.includes('Oldperson'), false);
  assert.match(readFileSync(htmlPath, 'utf8'), /Bob/);
});

test('migrate preview-latest preserves host label for Claude archives', () => {
  const downloads = mkdtempSync(join(tmpdir(), 'pulse-claude-downloads.'));
  const archiveDir = mkdtempSync(join(tmpdir(), 'pulse-claude-archive.'));
  const htmlPath = join(downloads, 'pulse-claude-preview.html');
  const jsonPath = join(downloads, 'pulse-claude-preview.json');
  writeFileSync(join(archiveDir, 'conversations.json'), JSON.stringify([
    {
      name: 'Claude archive export',
      messages: [
        { sender: 'human', text: 'Bob should review the Claude archive preview.' },
      ],
    },
  ]));
  spawnSync('zip', ['-qr', join(downloads, 'claude-export.zip'), '.'], { cwd: archiveDir });

  const { result } = run([
    'migrate',
    'preview-latest',
    'claude',
    '--downloads',
    downloads,
    '--html',
    htmlPath,
    '--out',
    jsonPath,
  ]);

  assert.equal(result.status, 0, result.stderr);
  const preview = JSON.parse(readFileSync(jsonPath, 'utf8'));
  assert.equal(preview.source, 'claude');
  assert.match(preview.next, new RegExp(`pulse migrate commit ${jsonPath.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')} --confirm "import pulse graph"`));
  assert.match(readFileSync(htmlPath, 'utf8'), />claude</);
});

test('migrate preview-latest explains when the archive is not downloaded yet', () => {
  const downloads = mkdtempSync(join(tmpdir(), 'pulse-empty-downloads.'));
  const { result } = run(['migrate', 'preview-latest', 'chatgpt', '--downloads', downloads]);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /No ChatGPT archive zip found/);
  assert.match(result.stderr, /Request archive/);
  assert.match(result.stderr, /still be preparing/);
  assert.match(result.stderr, /--downloads/);
  assert.doesNotMatch(result.stderr, /PULSE_API_KEY|secret\.key|token=/i);
});

test('migrate wait-latest waits for host archive then previews it', { timeout: 10000 }, async () => {
  const downloads = mkdtempSync(join(tmpdir(), 'pulse-wait-downloads.'));
  const archiveDir = mkdtempSync(join(tmpdir(), 'pulse-wait-archive.'));
  const htmlPath = join(downloads, 'pulse-preview.html');
  const jsonPath = join(downloads, 'pulse-preview.json');
  const zipPath = join(downloads, 'chatgpt-export-wait.zip');
  writeFileSync(join(archiveDir, 'conversations.json'), JSON.stringify([
    {
      title: 'Waited export',
      mapping: {
        latest: {
          message: {
            author: { role: 'user' },
            content: { parts: ['Bob should appear after Pulse waits for the archive download.'] },
          },
        },
      },
    },
  ]));

  const resultPromise = runAsync([
    'migrate',
    'wait-latest',
    'chatgpt',
    '--downloads',
    downloads,
    '--timeout-ms',
    '3000',
    '--interval-ms',
    '100',
    '--html',
    htmlPath,
    '--out',
    jsonPath,
  ]);

  setTimeout(() => {
    spawnSync('zip', ['-qr', zipPath, '.'], { cwd: archiveDir });
  }, 200);

  const result = await resultPromise;

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /waiting for chatgpt archive/);
  assert.match(result.stdout, /latest chatgpt archive/);
  assert.match(result.stdout, /migration HTML preview/);
  assert.match(result.stdout, /migration JSON preview/);
  const preview = JSON.parse(readFileSync(jsonPath, 'utf8'));
  assert.deepEqual(preview.people_candidates.includes('Bob'), true);
  assert.match(readFileSync(htmlPath, 'utf8'), /Import gate/);
});

test('migrate request opens host archive page then waits for download preview', { timeout: 10000 }, async () => {
  const downloads = mkdtempSync(join(tmpdir(), 'pulse-request-downloads.'));
  const archiveDir = mkdtempSync(join(tmpdir(), 'pulse-request-archive.'));
  const htmlPath = join(downloads, 'pulse-preview.html');
  const jsonPath = join(downloads, 'pulse-preview.json');
  const zipPath = join(downloads, 'chatgpt-export-request.zip');
  writeFileSync(join(archiveDir, 'conversations.json'), JSON.stringify([
    {
      title: 'Request export',
      mapping: {
        latest: {
          message: {
            author: { role: 'user' },
            content: { parts: ['Bob should appear after Pulse opens the archive request page.'] },
          },
        },
      },
    },
  ]));

  const resultPromise = runAsync([
    'migrate',
    'request',
    'chatgpt',
    '--downloads',
    downloads,
    '--timeout-ms',
    '3000',
    '--interval-ms',
    '100',
    '--html',
    htmlPath,
    '--out',
    jsonPath,
  ], { PULSE_OPEN_DRY_RUN: '1' });

  setTimeout(() => {
    spawnSync('zip', ['-qr', zipPath, '.'], { cwd: archiveDir });
  }, 200);

  const result = await resultPromise;

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /opened browser: https:\/\/chatgpt\.com\/#settings\/DataControls/);
  assert.match(result.stdout, /Click "Request archive"/);
  assert.match(result.stdout, /waiting for chatgpt archive/);
  assert.match(result.stdout, /migration HTML preview/);
  const preview = JSON.parse(readFileSync(jsonPath, 'utf8'));
  assert.deepEqual(preview.people_candidates.includes('Bob'), true);
  assert.match(readFileSync(htmlPath, 'utf8'), /Open the memory viewer/);
});

test('migrate request defaults to browser preview files', { timeout: 10000 }, async () => {
  const downloads = mkdtempSync(join(tmpdir(), 'pulse-request-defaults-downloads.'));
  const archiveDir = mkdtempSync(join(tmpdir(), 'pulse-request-defaults-archive.'));
  const zipPath = join(downloads, 'chatgpt-export-defaults.zip');
  writeFileSync(join(archiveDir, 'conversations.json'), JSON.stringify([
    {
      title: 'Default preview',
      mapping: {
        latest: {
          message: {
            author: { role: 'user' },
            content: { parts: ['Alice should see the default Pulse preview without extra flags.'] },
          },
        },
      },
    },
  ]));

  const resultPromise = runAsync([
    'migrate',
    'request',
    'chatgpt',
    '--downloads',
    downloads,
    '--timeout-ms',
    '3000',
    '--interval-ms',
    '100',
    '--open',
  ], { PULSE_OPEN_DRY_RUN: '1' });

  setTimeout(() => {
    spawnSync('zip', ['-qr', zipPath, '.'], { cwd: archiveDir });
  }, 200);

  const result = await resultPromise;
  const realCwd = realpathSync(result.cwd);
  const htmlPath = join(realCwd, 'pulse-preview.html');
  const jsonPath = join(realCwd, 'pulse-preview.json');

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /opened browser: https:\/\/chatgpt\.com\/#settings\/DataControls/);
  assert.match(result.stdout, new RegExp(`opened browser: ${htmlPath.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}`));
  assert.match(result.stdout, /migration HTML preview/);
  assert.match(result.stdout, /migration JSON preview/);
  assert.equal(existsSync(htmlPath), true);
  assert.equal(existsSync(jsonPath), true);
  const preview = JSON.parse(readFileSync(jsonPath, 'utf8'));
  assert.equal(preview.html_path, htmlPath);
  assert.deepEqual(preview.people_candidates.includes('Alice'), true);
  assert.match(readFileSync(htmlPath, 'utf8'), /Import structured continuity/);
});

function writePeopleGraphFixture() {
  const graphDir = mkdtempSync(join(tmpdir(), 'pulse-people-graph.'));
  const peopleDir = join(graphDir, 'people');
  mkdirSync(peopleDir, { recursive: true });
  writeFileSync(join(peopleDir, 'INDEX.md'), [
    '# People Index',
    '',
    '| Name | Role | Close | Relevant | File |',
    '|------|------|-------|----------|------|',
    '| Bob Example | founder and warm product reviewer | 4 | pulse-distribution | bob-example.md |',
    '| Carol Example | AI governance connector | 3 | work | carol-example.md |',
  ].join('\n'));
  writeFileSync(join(peopleDir, 'bob-example.md'), [
    '# Bob Example',
    '',
    '**Status:** warm contact after product call',
    '**Telegram:** @bob_example',
    '**Aliases:** Robert Example, Боб Example',
    '',
    '## Summary',
    'Bob call changed the Pulse wedge toward memory and continuity instead of generic companion positioning.',
    '',
    '## Links',
    '- source:memory/contacts/personal-contacts.md',
    '- Introduced Pulse packaging critique',
    '- Can review first public MCP bundle',
  ].join('\n'));
  writeFileSync(join(peopleDir, 'carol-example.md'), [
    '# Carol Example',
    '',
    '**Status:** useful AI governance bridge',
    '',
    '## Summary',
    'Alexander can connect the AI governance course context to Pulse distribution.',
  ].join('\n'));
  return graphDir;
}

test('migrate preview-people-graph reads curated real people before archive candidates', () => {
  const graphDir = writePeopleGraphFixture();
  const htmlPath = join(graphDir, 'people-preview.html');
  const jsonPath = join(graphDir, 'people-preview.json');

  const { result } = run(['migrate', 'preview-people-graph', graphDir, '--html', htmlPath, '--out', jsonPath]);

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /people graph HTML preview/);
  assert.match(result.stdout, /people graph JSON preview/);
  const preview = JSON.parse(readFileSync(jsonPath, 'utf8'));
  assert.equal(preview.source, 'people-graph');
  assert.equal(preview.source_kind, 'curated_people_graph');
  assert.deepEqual(preview.people_candidates, ['Bob Example', 'Carol Example']);
  assert.deepEqual(preview.review_candidates, []);
  assert.equal(preview.raw_text_written, false);
  assert.match(preview.next, /pulse migrate commit/);
  assert.equal(Array.isArray(preview.person_profiles), true);
  const bobProfile = preview.person_profiles.find((profile) => profile.name === 'Bob Example');
  assert.ok(bobProfile, JSON.stringify(preview.person_profiles));
  assert.equal(bobProfile.role, 'founder and warm product reviewer');
  assert.equal(bobProfile.closeness, 4);
  assert.equal(bobProfile.relevance, 'pulse-distribution');
  assert.equal(bobProfile.status, 'warm contact after product call');
  assert.equal(bobProfile.evidence_ref, 'people/bob-example.md');
  assert.deepEqual(bobProfile.aliases, ['Robert Example', 'Боб Example']);
  assert.match(bobProfile.summary, /Pulse wedge/);
  assert.deepEqual(bobProfile.links.includes('Can review first public MCP bundle'), true);
  assert.deepEqual(preview.relationship_candidates.some((item) => /source:memory|people\/bob-example\.md/.test(item)), false);
  assert.doesNotMatch(JSON.stringify(preview), /Qwen|Cinema|Sophie|raw transcript/i);

  const html = readFileSync(htmlPath, 'utf8');
  assert.match(html, /Real people graph/);
  assert.match(html, /Curated people/);
  assert.match(html, /Bob Example/);
  assert.match(html, /founder and warm product reviewer/);
  assert.match(html, /warm contact after product call/);
  assert.match(html, /Personal contacts/);
  assert.doesNotMatch(html, /source:memory|people\/bob-example\.md/);
  assert.match(html, /pulse-distribution/);
  assert.doesNotMatch(html, /Qwen|Cinema|Sophie/);
});

test('migrate request previews local Codex history with default browser files', async () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-request-codex-home.'));
  const sessionDir = join(home, '.codex', 'sessions', '2026', '06', '03');
  mkdirSync(sessionDir, { recursive: true });
  writeFileSync(join(sessionDir, 'session.jsonl'), [
    JSON.stringify({
      type: 'response_item',
      payload: {
        type: 'message',
        role: 'user',
        content: [{ type: 'input_text', text: 'Bob should inspect local Codex memory relationships.' }],
      },
    }),
  ].join('\n'));

  const result = await runAsync([
    'migrate',
    'request',
    'codex',
    '--open',
  ], { HOME: home, PULSE_DATA_DIR: join(home, '.pulse'), PULSE_OPEN_DRY_RUN: '1' });
  const realCwd = realpathSync(result.cwd);
  const htmlPath = join(realCwd, 'pulse-preview.html');
  const jsonPath = join(realCwd, 'pulse-preview.json');

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /Codex local history/);
  assert.match(result.stdout, /\.codex\/sessions/);
  assert.match(result.stdout, /migration HTML preview/);
  assert.match(result.stdout, new RegExp(`opened browser: ${htmlPath.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}`));
  const preview = JSON.parse(readFileSync(jsonPath, 'utf8'));
  assert.equal(preview.source, 'codex');
  assert.deepEqual(preview.people_candidates.includes('Bob'), true);
  assert.match(readFileSync(htmlPath, 'utf8'), /People found/);
});

test('migrate request previews local Claude Code history with default browser files', async () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-request-claude-code-home.'));
  const projectDir = join(home, '.claude', 'projects', 'garden');
  mkdirSync(projectDir, { recursive: true });
  writeFileSync(join(projectDir, 'session.jsonl'), [
    JSON.stringify({
      type: 'user',
      operation: 'message',
      timestamp: '2026-06-03T00:01:00Z',
      sessionId: 'claude-code-local-request',
      content: { text: 'Alice should inspect local Claude Code memory profiles.' },
    }),
  ].join('\n'));

  const result = await runAsync([
    'migrate',
    'request',
    'claude-code',
    '--open',
  ], { HOME: home, PULSE_DATA_DIR: join(home, '.pulse'), PULSE_OPEN_DRY_RUN: '1' });
  const realCwd = realpathSync(result.cwd);
  const htmlPath = join(realCwd, 'pulse-preview.html');
  const jsonPath = join(realCwd, 'pulse-preview.json');

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /Claude Code local history/);
  assert.match(result.stdout, /\.claude\/projects/);
  assert.match(result.stdout, /migration JSON preview/);
  const preview = JSON.parse(readFileSync(jsonPath, 'utf8'));
  assert.equal(preview.source, 'claude-code');
  assert.deepEqual(preview.people_candidates.includes('Alice'), true);
  assert.match(readFileSync(htmlPath, 'utf8'), /Open the memory viewer/);
});

test('archive migration commits into real Pulse viewer graph profile', { timeout: 60000 }, async () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-e2e-home.'));
  const dataDir = join(home, '.pulse');
  const workDir = mkdtempSync(join(tmpdir(), 'pulse-e2e-work.'));
  const exportDir = join(workDir, 'chatgpt-export');
  mkdirSync(exportDir, { recursive: true });
  const htmlPath = join(workDir, 'pulse-preview.html');
  const jsonPath = join(workDir, 'pulse-preview.json');
  writeFileSync(join(exportDir, 'conversations.json'), JSON.stringify([
    {
      title: 'Garden launch',
      mapping: {
        user: {
          message: {
            author: { role: 'user' },
            content: {
              parts: [
                'Remember Bob and Alice are reviewing Pulse. Alex felt relief after the archive migrator plan.',
              ],
            },
          },
        },
        assistant: {
          message: {
            author: { role: 'assistant' },
            content: { parts: ['Keep raw chat text out of the browser preview.'] },
          },
        },
      },
    },
  ]));

  const pulse = await startPulseServer(dataDir);
  try {
    const env = {
      ...process.env,
      HOME: home,
      PULSE_BASE_URL: pulse.baseUrl,
      PULSE_DATA_DIR: dataDir,
      PULSE_THREAD_ID: 'archive-import',
      PULSE_PROJECT_ID: 'pulse-migration',
      PULSE_SESSION_ID: 'pulse-migration:e2e',
      ANTHROPIC_API_KEY: '',
      COHERE_API_KEY: '',
    };
    const previewResult = spawnSync(process.execPath, [
      CLI,
      'migrate',
      'preview',
      exportDir,
      '--html',
      htmlPath,
      '--out',
      jsonPath,
    ], { cwd: workDir, env, encoding: 'utf8' });
    assert.equal(previewResult.status, 0, previewResult.stderr);
    assert.match(previewResult.stdout, /migration HTML preview/);
    assert.match(previewResult.stdout, /migration JSON preview/);
    assert.equal(existsSync(htmlPath), true);
    assert.equal(existsSync(jsonPath), true);

    const commitResult = spawnSync(process.execPath, [
      CLI,
      'migrate',
      'commit',
      jsonPath,
      '--confirm',
      'import pulse graph',
    ], { cwd: workDir, env, encoding: 'utf8' });
    assert.equal(commitResult.status, 0, commitResult.stderr);
    assert.match(commitResult.stdout, /committed Pulse graph delta/);
    assert.match(commitResult.stdout, /pulse viewer --base/);
    assert.match(commitResult.stdout, /--thread-id archive-import/);

    const viewerDataResp = await fetch(`${pulse.baseUrl}/viewer/data?key=${pulse.secret}&thread_id=archive-import`);
    if (viewerDataResp.status !== 200) {
      assert.fail(await viewerDataResp.text());
    }
    const viewerData = await viewerDataResp.json();
    assert.match(viewerData.next_resume.resume_markdown, /Imported safe Pulse archive preview/);
    assert.deepEqual(viewerData.graph_profile.emotions.includes('relief'), true);
    const profiles = viewerData.graph_profile.person_profiles;
    assert.equal(Array.isArray(profiles), true);
    const bobProfile = profiles.find((profile) => profile.name === 'Bob');
    assert.ok(bobProfile, JSON.stringify(profiles));
    assert.match(bobProfile.summary, /Person candidate/);
    assert.match(bobProfile.facts.join('\n'), /Bob appeared in 1 bounded source snippet/);
    assert.match(bobProfile.relationships.join('\n'), /Bob mentioned in Garden launch/);
    assert.match(bobProfile.memories.join('\n'), /Pulse archive import preview committed/);

    const viewerResp = await fetch(`${pulse.baseUrl}/viewer?key=${pulse.secret}&thread_id=archive-import`);
    if (viewerResp.status !== 200) {
      assert.fail(await viewerResp.text());
    }
    const viewerHTML = await viewerResp.text();
    assert.match(viewerHTML, /People found in reviewed sources/);
    assert.match(viewerHTML, /graph-profile-cards/);
    assert.match(viewerHTML, /graph-emotions/);
    assert.match(viewerHTML, /person_profiles/);
    assert.doesNotMatch(JSON.stringify(viewerData), /Alex felt relief after the archive migrator plan/);
  } finally {
    await pulse.stop();
  }
});

test('migrate commit requires explicit graph import confirmation', () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-commit-preview.'));
  const previewPath = join(exportDir, 'preview.json');
  writeFileSync(previewPath, JSON.stringify({ ok: true, raw_text_written: false }));

  const { result } = run(['migrate', 'commit', previewPath]);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /--confirm "import pulse graph"/);
});

test('migrate commit rejects unsupported import preview flows before graph delivery', () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-commit-unsupported-preview.'));
  const previewPath = join(exportDir, 'preview.json');
  writeFileSync(previewPath, JSON.stringify({
    flow: 'pulse.import_preview.v999',
    ok: true,
    source: 'chatgpt',
    conversations: 1,
    messages: 1,
    thread_candidates: ['Garden launch'],
    memory_candidates: ['Garden launch: 1 bounded source snippet'],
    raw_text_written: false,
  }));

  const { result } = run([
    'migrate', 'commit', previewPath, '--confirm', 'import pulse graph',
  ]);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /unsupported Pulse import preview flow: pulse\.import_preview\.v999/);
});

test('migrate commit sends safe semantic delta to Pulse graph endpoint', async () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-commit-preview.'));
  const previewPath = join(exportDir, 'preview.json');
  writeFileSync(previewPath, JSON.stringify({
    flow: 'pulse.import_preview.v1',
    ok: true,
    source: 'chatgpt',
    conversations: 1,
    messages: 2,
    people_candidates: ['Bob', 'Qwen', 'Cinema', 'Alice'],
    thread_candidates: ['Garden launch'],
    memory_candidates: ['Garden launch: 2 safe message signals'],
    emotion_candidates: ['relief'],
    relationship_candidates: ['Bob -> Garden launch', 'Bob <-> Alice', 'Qwen <-> Cinema'],
    fun_fact_candidates: [
      'Bob appeared in 1 safe preview signal',
      'Qwen appeared in 1 safe preview signal',
      'Cinema appeared in 1 safe preview signal',
    ],
    raw_text_written: false,
  }));
  const stub = await withPulseStub((req, body) => {
    assert.equal(req.method, 'POST');
    assert.equal(req.url, '/graph/delta');
    assert.equal(body.schema, 'pulse.semantic_delta.v1');
    assert.equal(body.raw_input_included, false);
    assert.equal(body.source.host, 'chatgpt');
    assert.equal(body.source.conversation_scope, 'project_context');
    assert.equal(body.source.thread_id, 'archive-import');
    assert.match(body.source.timestamp, /^\d{4}-\d{2}-\d{2}T/);
    assert.deepEqual(body.nodes.map((node) => node.canonical_name), ['Bob', 'Alice', 'Garden launch']);
    assert.deepEqual(body.nodes.map((node) => node.kind), ['person', 'person', 'project']);
    assert.deepEqual(body.edges.map((edge) => edge.kind), ['mentioned_in', 'related_to']);
    assert.deepEqual(body.facts.map((fact) => fact.text), ['Bob appeared in 1 safe preview signal']);
    assert.deepEqual([...body.nodes, ...body.edges, ...body.facts, ...body.events].map((item) => item.privacy_tier), [
      'private',
      'private',
      'private',
      'private',
      'private',
      'private',
      'private',
      'private',
    ]);
    assert.match(body.events[0].title, /Pulse archive import preview/);
    assert.match(body.continuity.summary, /Imported safe Pulse archive preview/);
    assert.doesNotMatch(JSON.stringify(body), /Alex felt relief after the archive migrator plan|raw chat|PULSE_API_KEY|secret\.key/);
    return {
      body: {
        ok: true,
        nodes_upserted: body.nodes.length,
        edges_upserted: body.edges.length,
        facts_upserted: body.facts.length,
        events_inserted: body.events.length,
      },
    };
  });
  try {
    const result = await runAsync([
      'migrate',
      'commit',
      previewPath,
      '--confirm',
      'import pulse graph',
    ], {
      PULSE_BASE_URL: stub.baseUrl,
      PULSE_THREAD_ID: 'archive-import',
      PULSE_PROJECT_ID: 'pulse-migration',
      PULSE_SESSION_ID: 'pulse-migration:test',
    });

    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /committed Pulse graph delta/);
    assert.match(result.stdout, /nodes: 3/);
    assert.match(result.stdout, /Next:/);
    assert.match(result.stdout, /pulse viewer --base/);
    assert.match(result.stdout, /--thread-id archive-import/);
    assert.match(result.stdout, new RegExp(stub.baseUrl.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
    assert.equal(stub.requests.length, 1);
  } finally {
    await stub.close();
  }
});

test('migrate commit fails closed on a rejected terminal receipt', async () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-commit-rejected-preview.'));
  const previewPath = join(exportDir, 'preview.json');
  writeFileSync(previewPath, JSON.stringify({
    flow: 'pulse.import_preview.v2',
    ok: true,
    source: 'chatgpt',
    conversations: 1,
    messages: 2,
    people_candidates: ['Bob'],
    thread_candidates: ['Garden launch'],
    memory_candidates: ['Garden launch: 2 safe message signals'],
    relationship_candidates: [],
    raw_text_written: false,
  }));
  const stub = await withPulseStub(() => ({
    body: {
      ledger_id: 'turn_migrate_rejected',
      status: 'rejected',
      receipts: [productReceipt({
        receipt_id: 'receipt_migrate_rejected', status: 'rejected', reason_code: 'unsafe_payload',
      })],
    },
  }));
  try {
    const result = await runAsync([
      'migrate',
      'commit',
      previewPath,
      '--confirm',
      'import pulse graph',
    ], { PULSE_BASE_URL: stub.baseUrl });

    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /was not committed \(rejected: unsafe_payload; receipt receipt_migrate_rejected\)/);
    assert.doesNotMatch(result.stdout, /committed Pulse graph delta/);
  } finally {
    await stub.close();
  }
});

test('migrate commit applies persisted review decisions from reviewed JSON', async () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-commit-reviewed-preview.'));
  const previewPath = join(exportDir, 'preview.reviewed.json');
  writeFileSync(previewPath, JSON.stringify({
    ok: true,
    source: 'chatgpt',
    conversations: 1,
    messages: 3,
    people_candidates: ['Bob', 'Qwen'],
    review_candidates: ['Cartographer', 'Qwen'],
    thread_candidates: ['Garden launch'],
    memory_candidates: ['Garden launch: 3 source snippets'],
    relationship_candidates: ['Bob -> Garden launch', 'Qwen -> Garden launch', 'Cartographer -> Garden launch'],
    fun_fact_candidates: [
      'Bob appeared in 1 bounded source snippet',
      'Qwen appeared in 1 bounded source snippet',
    ],
    review_decisions: {
      Cartographer: { action: 'confirm', kind: 'project', privacy_tier: 'private' },
      Qwen: { action: 'ignore' },
    },
    raw_text_written: false,
  }));
  const stub = await withPulseStub((req, body) => {
    assert.equal(req.method, 'POST');
    assert.equal(req.url, '/graph/delta');
    const names = body.nodes.map((node) => node.canonical_name);
    assert.deepEqual(names, ['Bob', 'Garden launch', 'Cartographer']);
    assert.equal(JSON.stringify(body).includes('Qwen'), false);
    assert.deepEqual(body.facts.map((fact) => fact.text), ['Bob appeared in 1 bounded source snippet']);
    return { body: { ok: true } };
  });
  try {
    const result = await runAsync([
      'migrate',
      'commit',
      previewPath,
      '--confirm',
      'import pulse graph',
    ], {
      PULSE_BASE_URL: stub.baseUrl,
      PULSE_THREAD_ID: 'archive-import',
      PULSE_PROJECT_ID: 'pulse-migration',
      PULSE_SESSION_ID: 'pulse-migration:test',
    });

    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /committed Pulse graph delta/);
    assert.equal(stub.requests.length, 1);
  } finally {
    await stub.close();
  }
});

test('migrate commit includes safe facts and edges for confirmed review entities', async () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-commit-reviewed-relations.'));
  const previewPath = join(exportDir, 'preview.reviewed.json');
  writeFileSync(previewPath, JSON.stringify({
    ok: true,
    source: 'chatgpt',
    conversations: 1,
    messages: 4,
    people_candidates: ['Bob', 'Qwen'],
    review_candidates: ['Cartographer', 'Qwen'],
    thread_candidates: ['Garden launch'],
    memory_candidates: ['Garden launch: 4 source snippets'],
    relationship_candidates: [
      'Bob -> Garden launch',
      'Cartographer -> Garden launch',
      'Qwen -> Garden launch',
    ],
    fun_fact_candidates: [
      'Bob appeared in 1 bounded source snippet',
      'Cartographer appeared in 2 bounded source snippets',
      'Qwen appeared in 1 bounded source snippet',
    ],
    review_decisions: {
      Cartographer: { action: 'confirm', kind: 'project', privacy_tier: 'private' },
      Qwen: { action: 'ignore' },
    },
    raw_text_written: false,
  }));
  const stub = await withPulseStub((req, body) => {
    assert.equal(req.method, 'POST');
    assert.equal(req.url, '/graph/delta');
    const names = body.nodes.map((node) => node.canonical_name);
    assert.deepEqual(names, ['Bob', 'Garden launch', 'Cartographer']);
    assert.equal(JSON.stringify(body).includes('Qwen'), false);
    assert.deepEqual(body.facts.map((fact) => fact.text), [
      'Bob appeared in 1 bounded source snippet',
      'Cartographer appeared in 2 bounded source snippets',
    ]);
    const nodeByName = new Map(body.nodes.map((node) => [node.canonical_name, node.client_id]));
    assert.ok(body.edges.some((edge) => (
      edge.kind === 'mentioned_in' &&
      edge.from === nodeByName.get('Cartographer') &&
      edge.to === nodeByName.get('Garden launch')
    )), JSON.stringify(body.edges));
    assert.ok(body.edges.some((edge) => (
      edge.kind === 'mentioned_in' &&
      edge.from === nodeByName.get('Bob') &&
      edge.to === nodeByName.get('Garden launch')
    )), JSON.stringify(body.edges));
    return { body: { ok: true } };
  });
  try {
    const result = await runAsync([
      'migrate',
      'commit',
      previewPath,
      '--confirm',
      'import pulse graph',
    ], {
      PULSE_BASE_URL: stub.baseUrl,
      PULSE_THREAD_ID: 'archive-import',
      PULSE_PROJECT_ID: 'pulse-migration',
      PULSE_SESSION_ID: 'pulse-migration:test',
    });

    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /committed Pulse graph delta/);
    assert.equal(stub.requests.length, 1);
  } finally {
    await stub.close();
  }
});

test('migrate commit materializes Pulse insights without ignored review leakage', async () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-commit-reviewed-insights.'));
  const previewPath = join(exportDir, 'preview.reviewed.json');
  writeFileSync(previewPath, JSON.stringify({
    ok: true,
    source: 'chatgpt',
    conversations: 1,
    messages: 5,
    people_candidates: ['Bob', 'Qwen'],
    review_candidates: ['Cartographer', 'Qwen'],
    thread_candidates: ['Garden launch'],
    memory_candidates: [
      'Garden launch: 5 source snippets',
      'Codex entity hygiene session: 2 source snippets',
    ],
    relationship_candidates: [
      'Bob -> Garden launch',
      'Cartographer -> Garden launch',
      'Qwen -> Garden launch',
    ],
    fun_fact_candidates: [
      'Bob appeared in 1 bounded source snippet',
      'Cartographer appeared in 2 bounded source snippets',
      'Qwen appeared in 1 bounded source snippet',
    ],
    pulse_insights: [
      {
        kind: 'why_this_matters_now',
        thread_title: 'Garden launch',
        title: 'Why this may matter now: Garden launch',
        summary: 'Cartographer is now linked to Garden launch after review.',
        reasons: [
          'Cartographer appears in reviewed graph context.',
          'Qwen should stay ignored.',
        ],
        suggested_next_step: 'Review this thread before import, then decide whether to make it an active Pulse thread.',
        related_entities: ['Cartographer', 'Qwen'],
        privacy_tier: 'private',
        confidence: 0.6,
      },
    ],
    active_threads: [
      {
        thread_title: 'Garden launch',
        reason: 'User marked the Pulse insight as active during review.',
        source: 'pulse_insight',
      },
    ],
    review_decisions: {
      Cartographer: { action: 'confirm', kind: 'project', privacy_tier: 'private' },
      Qwen: { action: 'ignore' },
    },
    raw_text_written: false,
  }));
  const stub = await withPulseStub((req, body) => {
    assert.equal(req.method, 'POST');
    assert.equal(req.url, '/graph/delta');
    assert.equal(JSON.stringify(body).includes('Qwen'), false);
    const insightEvents = body.events.filter((event) => event.title.startsWith('Pulse insight:'));
    assert.equal(insightEvents.length, 1, JSON.stringify(body.events));
    assert.match(insightEvents[0].title, /Garden launch/);
    assert.match(insightEvents[0].summary, /Cartographer is now linked to Garden launch/);
    assert.match(insightEvents[0].summary, /Next: Review this thread before import/);
    assert.equal(insightEvents[0].privacy_tier, 'private');
    assert.equal(
      body.continuity.state_signals.some((signal) => /Pulse insight:/.test(signal)),
      false,
      JSON.stringify(body.continuity.state_signals),
    );
    assert.equal(
      body.continuity.state_signals.some((signal) => /Codex entity hygiene session/.test(signal)),
      false,
      JSON.stringify(body.continuity.state_signals),
    );
    assert.ok(Array.isArray(body.continuity.review_insights), JSON.stringify(body.continuity));
    assert.ok(
      body.continuity.review_insights.some((signal) => /Pulse insight: Why this may matter now: Garden launch/.test(signal)),
      JSON.stringify(body.continuity.review_insights),
    );
    assert.ok(Array.isArray(body.continuity.active_threads), JSON.stringify(body.continuity));
    assert.ok(
      body.continuity.active_threads.some((thread) => /Garden launch/.test(thread)),
      JSON.stringify(body.continuity.active_threads),
    );
    assert.ok(
      body.continuity.review_insights.some((signal) => /Active thread: Garden launch/.test(signal)),
      JSON.stringify(body.continuity.review_insights),
    );
    return { body: { ok: true } };
  });
  try {
    const result = await runAsync([
      'migrate',
      'commit',
      previewPath,
      '--confirm',
      'import pulse graph',
    ], {
      PULSE_BASE_URL: stub.baseUrl,
      PULSE_THREAD_ID: 'archive-import',
      PULSE_PROJECT_ID: 'pulse-migration',
      PULSE_SESSION_ID: 'pulse-migration:test',
    });

    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /committed Pulse graph delta/);
    assert.equal(stub.requests.length, 1);
  } finally {
    await stub.close();
  }
});

test('migrate commit allows explicit privacy override for developer smoke', async () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-commit-privacy-preview.'));
  const previewPath = join(exportDir, 'preview.json');
  writeFileSync(previewPath, JSON.stringify({
    ok: true,
    source: 'chatgpt',
    conversations: 1,
    messages: 2,
    people_candidates: ['Bob'],
    thread_candidates: ['Garden launch'],
    memory_candidates: ['Garden launch: 2 safe message signals'],
    relationship_candidates: ['Bob -> Garden launch'],
    raw_text_written: false,
  }));
  const stub = await withPulseStub((req, body) => {
    assert.equal(req.method, 'POST');
    assert.equal(req.url, '/graph/delta');
    assert.ok([...body.nodes, ...body.edges, ...body.events].every((item) => item.privacy_tier === 'normal'));
    return { body: { ok: true } };
  });
  try {
    const result = await runAsync([
      'migrate',
      'commit',
      previewPath,
      '--confirm',
      'import pulse graph',
      '--privacy',
      'normal',
    ], {
      PULSE_BASE_URL: stub.baseUrl,
      PULSE_THREAD_ID: 'archive-import',
      PULSE_PROJECT_ID: 'pulse-migration',
      PULSE_SESSION_ID: 'pulse-migration:test',
    });

    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /committed Pulse graph delta/);
  } finally {
    await stub.close();
  }
});

test('migrate commit rejects invalid privacy override', () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-commit-bad-privacy.'));
  const previewPath = join(exportDir, 'preview.json');
  writeFileSync(previewPath, JSON.stringify({
    ok: true,
    raw_text_written: false,
    memory_candidates: ['Garden launch: 2 safe message signals'],
  }));

  const { result } = run([
    'migrate',
    'commit',
    previewPath,
    '--confirm',
    'import pulse graph',
    '--privacy',
    'public',
  ]);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /--privacy must be normal, sensitive, or private/);
});

test('migrate commit groups obvious person aliases before graph import', async () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-commit-alias-preview.'));
  const previewPath = join(exportDir, 'preview.json');
  writeFileSync(previewPath, JSON.stringify({
    ok: true,
    source: 'chatgpt',
    conversations: 3,
    messages: 12,
    people_candidates: ['Alex', 'Alexander', 'Александр', 'Alice'],
    person_profiles: [{
      name: 'Alex',
      aliases: ['Alexander', 'Александр'],
    }],
    thread_candidates: ['Pulse Dashboard'],
    memory_candidates: ['Pulse Dashboard: 12 safe message signals'],
    emotion_candidates: ['relief'],
    relationship_candidates: ['Alexander -> Pulse Dashboard', 'Александр -> Pulse Dashboard', 'Alice -> Pulse Dashboard'],
    fun_fact_candidates: [
      'Alexander appeared in 8 safe preview signals',
      'Александр appeared in 5 safe preview signals',
      'Alice appeared in 2 safe preview signals',
    ],
    raw_text_written: false,
  }));
  const stub = await withPulseStub((req, body) => {
    assert.equal(req.method, 'POST');
    assert.equal(req.url, '/graph/delta');
    const people = body.nodes.filter((node) => node.kind === 'person');
    assert.deepEqual(people.map((node) => node.canonical_name), ['Alex', 'Alice']);
    assert.deepEqual(people[0].aliases, ['Alexander', 'Александр']);
    assert.equal(body.edges.filter((edge) => edge.from === people[0].client_id).length, 1);
    assert.equal(body.facts.filter((fact) => fact.node === people[0].client_id).length, 2);
    assert.doesNotMatch(JSON.stringify(body), /raw chat|PULSE_API_KEY|secret\.key/);
    return { body: { ok: true } };
  });
  try {
    const result = await runAsync([
      'migrate',
      'commit',
      previewPath,
      '--confirm',
      'import pulse graph',
    ], {
      PULSE_BASE_URL: stub.baseUrl,
      PULSE_THREAD_ID: 'archive-import',
      PULSE_PROJECT_ID: 'pulse-migration',
      PULSE_SESSION_ID: 'pulse-migration:test',
    });

    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /committed Pulse graph delta/);
    assert.equal(stub.requests.length, 1);
  } finally {
    await stub.close();
  }
});

test('migrate commit removes aliases shared by multiple curated people', async () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-commit-ambiguous-alias.'));
  const previewPath = join(exportDir, 'preview.json');
  writeFileSync(previewPath, JSON.stringify({
    ok: true,
    source: 'people-graph',
    conversations: 1,
    messages: 2,
    people_candidates: ['Alice Smith', 'Bob Jones'],
    person_profiles: [
      { name: 'Alice Smith', aliases: ['Alex'] },
      { name: 'Bob Jones', aliases: ['Alex'] },
    ],
    thread_candidates: [],
    memory_candidates: [],
    relationship_candidates: [],
    raw_text_written: false,
  }));
  const stub = await withPulseStub((req, body) => {
    assert.equal(req.method, 'POST');
    assert.equal(req.url, '/graph/delta');
    const people = body.nodes.filter((node) => node.kind === 'person');
    assert.deepEqual(people.map((node) => node.canonical_name), ['Alice Smith', 'Bob Jones']);
    assert.deepEqual(people.map((node) => node.aliases), [[], []]);
    return { body: { ok: true } };
  });
  try {
    const result = await runAsync([
      'migrate',
      'commit',
      previewPath,
      '--confirm',
      'import pulse graph',
    ], {
      PULSE_BASE_URL: stub.baseUrl,
      PULSE_THREAD_ID: 'archive-import',
      PULSE_PROJECT_ID: 'pulse-migration',
      PULSE_SESSION_ID: 'pulse-migration:test',
    });

    assert.equal(result.status, 0, result.stderr);
    assert.equal(stub.requests.length, 1);
  } finally {
    await stub.close();
  }
});

test('migrate commit keeps relationship target topics as graph nodes', async () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-commit-relationship-target.'));
  const previewPath = join(exportDir, 'preview.json');
  writeFileSync(previewPath, JSON.stringify({
    ok: true,
    source: 'chatgpt',
    conversations: 4,
    messages: 16,
    people_candidates: ['Bob'],
    thread_candidates: [
      'Thread A', 'Thread B', 'Thread C', 'Thread D',
      'Thread E', 'Thread F', 'Thread G', 'Thread H',
    ],
    memory_candidates: ['Important hidden topic: 16 safe message signals'],
    emotion_candidates: ['relief'],
    relationship_candidates: ['Bob -> Important hidden topic'],
    fun_fact_candidates: ['Bob appeared in 4 safe preview signals'],
    raw_text_written: false,
  }));
  const stub = await withPulseStub((req, body) => {
    assert.equal(req.method, 'POST');
    assert.equal(req.url, '/graph/delta');
    assert.ok(body.nodes.some((node) => node.kind === 'project' && node.canonical_name === 'Important hidden topic'), JSON.stringify(body.nodes));
    assert.ok(body.edges.some((edge) => edge.kind === 'mentioned_in'), JSON.stringify(body.edges));
    return { body: { ok: true } };
  });
  try {
    const result = await runAsync([
      'migrate',
      'commit',
      previewPath,
      '--confirm',
      'import pulse graph',
    ], {
      PULSE_BASE_URL: stub.baseUrl,
      PULSE_THREAD_ID: 'archive-import',
      PULSE_PROJECT_ID: 'pulse-migration',
      PULSE_SESSION_ID: 'pulse-migration:test',
    });

    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /committed Pulse graph delta/);
  } finally {
    await stub.close();
  }
});

test('migrate commit materializes memory candidates as timeline events', async () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-commit-timeline-events.'));
  const previewPath = join(exportDir, 'preview.json');
  writeFileSync(previewPath, JSON.stringify({
    ok: true,
    source: 'chatgpt',
    conversations: 3,
    messages: 9,
    people_candidates: ['Bob'],
    thread_candidates: ['Pulse Dashboard', 'Graph Cleanup'],
    memory_candidates: [
      'Pulse Dashboard: 5 safe message signals',
      'Graph Cleanup: 3 safe message signals',
      'Viewer polish: 1 safe message signal',
    ],
    emotion_candidates: ['relief', 'excitement'],
    relationship_candidates: ['Bob -> Pulse Dashboard'],
    fun_fact_candidates: ['Bob appeared in 3 safe preview signals'],
    raw_text_written: false,
  }));
  const stub = await withPulseStub((req, body) => {
    assert.equal(req.method, 'POST');
    assert.equal(req.url, '/graph/delta');
    assert.ok(body.events.length >= 4, JSON.stringify(body.events));
    assert.ok(body.events.some((event) => event.title === 'Pulse Dashboard'), JSON.stringify(body.events));
    assert.ok(body.events.some((event) => event.title === 'Graph Cleanup'), JSON.stringify(body.events));
    assert.ok(body.events.some((event) => event.title === 'Viewer polish'), JSON.stringify(body.events));
    assert.doesNotMatch(JSON.stringify(body.events), /raw chat|PULSE_API_KEY|secret\.key/);
    return { body: { ok: true } };
  });
  try {
    const result = await runAsync([
      'migrate',
      'commit',
      previewPath,
      '--confirm',
      'import pulse graph',
    ], {
      PULSE_BASE_URL: stub.baseUrl,
      PULSE_THREAD_ID: 'archive-import',
      PULSE_PROJECT_ID: 'pulse-migration',
      PULSE_SESSION_ID: 'pulse-migration:test',
    });

    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /committed Pulse graph delta/);
  } finally {
    await stub.close();
  }
});

test('migrate commit can open the memory viewer after import', async () => {
  const exportDir = mkdtempSync(join(tmpdir(), 'pulse-commit-open-preview.'));
  const previewPath = join(exportDir, 'preview.json');
  writeFileSync(previewPath, JSON.stringify({
    ok: true,
    source: 'chatgpt',
    conversations: 1,
    messages: 1,
    people_candidates: ['Bob'],
    thread_candidates: ['Garden launch'],
    memory_candidates: ['Garden launch: 1 safe message signal'],
    emotion_candidates: ['relief'],
    relationship_candidates: ['Bob -> Garden launch'],
    fun_fact_candidates: ['Bob appeared in 1 safe preview signal'],
    raw_text_written: false,
  }));
  const stub = await withPulseStub((req, body) => {
    assert.equal(req.method, 'POST');
    assert.equal(req.url, '/graph/delta');
    assert.equal(body.raw_input_included, false);
    return { body: { ok: true } };
  });
  try {
    const result = await runAsync([
      'migrate',
      'commit',
      previewPath,
      '--confirm',
      'import pulse graph',
      '--open',
    ], {
      PULSE_BASE_URL: stub.baseUrl,
      PULSE_THREAD_ID: 'archive-import',
      PULSE_PROJECT_ID: 'pulse-migration',
      PULSE_SESSION_ID: 'pulse-migration:test',
      PULSE_OPEN_DRY_RUN: '1',
    });

    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /committed Pulse graph delta/);
    assert.match(result.stdout, /opened browser:/);
    assert.match(result.stdout, /\/viewer\?key=/);
    assert.match(result.stdout, /thread_id=archive-import/);
		assert.match(result.stdout, /Shows the bounded continuity pack for the next connected harness session/);
    assert.equal(stub.requests.length, 1);
  } finally {
    await stub.close();
  }
});

test('migrate guide chatgpt walks the user to the archive request page', () => {
  const { result } = run(['migrate', 'guide', 'chatgpt']);

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /\[pulse\] ChatGPT archive handoff/);
  assert.match(result.stdout, /https:\/\/chatgpt\.com\/#settings\/DataControls/);
  assert.match(result.stdout, /Request archive/);
  assert.match(result.stdout, /pulse migrate request chatgpt --open/);
  assert.match(result.stdout, /pulse migrate preview/);
  assert.match(result.stdout, /--open/);
  assert.doesNotMatch(result.stdout, /PULSE_API_KEY|secret\.key|token=/i);
});

test('migrate guide claude walks the user to Claude privacy export', () => {
  const { result } = run(['migrate', 'guide', 'claude']);

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /\[pulse\] Claude archive handoff/);
  assert.match(result.stdout, /https:\/\/claude\.ai\/settings\/privacy/);
  assert.match(result.stdout, /Export data/);
  assert.match(result.stdout, /pulse migrate request claude --open/);
  assert.match(result.stdout, /pulse migrate preview/);
});

test('migrate guide local coding hosts points at local history folders', () => {
  const codex = run(['migrate', 'guide', 'codex']).result;
  const claudeCode = run(['migrate', 'guide', 'claude-code']).result;

  assert.equal(codex.status, 0, codex.stderr);
  assert.match(codex.stdout, /\[pulse\] Codex local history handoff/);
  assert.match(codex.stdout, /\.codex\/sessions/);
  assert.match(codex.stdout, /pulse migrate request codex --open/);
  assert.match(codex.stdout, /pulse migrate preview/);

  assert.equal(claudeCode.status, 0, claudeCode.stderr);
  assert.match(claudeCode.stdout, /\[pulse\] Claude Code local history handoff/);
  assert.match(claudeCode.stdout, /\.claude\/projects/);
  assert.match(claudeCode.stdout, /pulse migrate request claude-code --open/);
  assert.match(claudeCode.stdout, /pulse migrate preview/);
});

test('migrate concierge writes a browser hand-hold page', () => {
  const htmlPath = join(mkdtempSync(join(tmpdir(), 'pulse-concierge.')), 'pulse-migrate.html');
  const briefPath = join(dirname(htmlPath), 'pulse-migrate-brief.md');
  const { result } = run(['migrate', 'concierge', '--html', htmlPath, '--brief', briefPath, '--open'], {
    PULSE_OPEN_DRY_RUN: '1',
  });

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /migration concierge/);
  assert.match(result.stdout, /migration brief/);
  assert.match(result.stdout, /opened browser:/);
  assert.match(result.stdout, /pulse-migrate\.html/);
  assert.equal(existsSync(htmlPath), true);
  assert.equal(existsSync(briefPath), true);
  const html = readFileSync(htmlPath, 'utf8');
  assert.match(html, /Pulse Archive Concierge/);
  assert.match(html, /Open ChatGPT Data Controls/);
  assert.match(html, /Open Claude Privacy/);
  assert.match(html, /Request archive/);
  assert.match(html, /Export data/);
  assert.match(html, /Human click only/);
  assert.match(html, /Pulse waits for the zip/);
  assert.match(html, /Review gate/);
  assert.match(html, /Codex local history/);
  assert.match(html, /Claude Code local history/);
  assert.match(html, /pulse migrate request chatgpt --open/);
  assert.match(html, /pulse migrate request claude --open/);
  assert.match(html, /pulse migrate request codex --open/);
  assert.match(html, /pulse migrate request claude-code --open/);
  assert.match(html, /pulse migrate commit/);
  assert.match(html, /pulse viewer/);
  assert.match(html, /Copy command/);
  assert.match(html, /data-copy=/);
  assert.match(html, /copyCommand/);
  assert.match(html, /Raw chat text is not written/);
  assert.match(html, /Paper boundary/);
  assert.match(html, /product extension, not an evaluated paper result/);
  assert.doesNotMatch(html, /PULSE_API_KEY|secret\.key|token=/i);

  const brief = readFileSync(briefPath, 'utf8');
  assert.match(brief, /# Pulse Archive Migration Hand-Hold/);
  assert.match(brief, /ChatGPT: open Data Controls and click Request archive/);
  assert.match(brief, /Claude: open Privacy settings and click Export data/);
  assert.match(brief, /Codex: preview local history/);
  assert.match(brief, /Claude Code: preview local history/);
  assert.match(brief, /Pulse waits for the zip/);
  assert.match(brief, /explicit graph confirmation/);
  assert.match(brief, /pulse migrate request chatgpt --open/);
  assert.match(brief, /pulse migrate request claude --open/);
  assert.match(brief, /pulse migrate request codex --open/);
  assert.match(brief, /pulse migrate request claude-code --open/);
  assert.match(brief, /pulse migrate commit/);
  assert.match(brief, /pulse viewer/);
  assert.match(brief, /candidate threads, decisions, open loops, do-not-repeat notes, emotional anchors, people found, and review cards/);
  assert.match(brief, /paper impact/i);
  assert.doesNotMatch(brief, /PULSE_API_KEY|secret\.key|token=/i);
});

test('migrate start opens archive pages and builds local previews in one command', async () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-start-home.'));
  const outDir = mkdtempSync(join(tmpdir(), 'pulse-start-out.'));
  const codexDir = join(home, '.codex', 'sessions', '2026', '06', '03');
  const claudeDir = join(home, '.claude', 'projects', 'garden');
  mkdirSync(codexDir, { recursive: true });
  mkdirSync(claudeDir, { recursive: true });
  writeFileSync(join(codexDir, 'session.jsonl'), [
    JSON.stringify({
      type: 'response_item',
      payload: {
        type: 'message',
        role: 'user',
        content: [{ type: 'input_text', text: 'Bob should inspect the one command Pulse start flow. Qwen and Cartographer are tool names, not people.' }],
      },
    }),
  ].join('\n'));
  writeFileSync(join(claudeDir, 'session.jsonl'), [
    JSON.stringify({
      type: 'user',
      operation: 'message',
      timestamp: '2026-06-03T00:01:00Z',
      sessionId: 'claude-code-start',
      content: { text: 'Alice should inspect the one command Pulse start flow.' },
    }),
  ].join('\n'));

  const result = await runAsync([
    'migrate',
    'start',
    '--dir',
    outDir,
    '--open',
  ], { HOME: home, PULSE_DATA_DIR: join(home, '.pulse'), PULSE_OPEN_DRY_RUN: '1' });

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /Pulse archive migration started/);
  assert.match(result.stdout, /opened browser: .*pulse-migrate-concierge\.html/);
  assert.match(result.stdout, /opened browser: .*pulse-migrate-status\.html/);
  assert.match(result.stdout, /opened browser: https:\/\/chatgpt\.com\/#settings\/DataControls/);
  assert.match(result.stdout, /opened browser: https:\/\/claude\.ai\/settings\/privacy/);
  assert.match(result.stdout, /Codex local history selected/);
  assert.match(result.stdout, /Claude Code local history selected/);
  assert.match(result.stdout, /pulse migrate wait-latest chatgpt/);
  assert.match(result.stdout, /pulse migrate wait-latest claude/);
  assert.equal(existsSync(join(outDir, 'pulse-migrate-concierge.html')), true);
  assert.equal(existsSync(join(outDir, 'pulse-migrate-brief.md')), true);
  assert.equal(existsSync(join(outDir, 'pulse-migrate-status.md')), true);
  assert.equal(existsSync(join(outDir, 'pulse-migrate-status.html')), true);
  assert.equal(existsSync(join(outDir, 'pulse-memory-gallery.html')), true);
  assert.equal(existsSync(join(outDir, 'pulse-codex-preview.html')), true);
  assert.equal(existsSync(join(outDir, 'pulse-codex-preview.json')), true);
  assert.equal(existsSync(join(outDir, 'pulse-claude-code-preview.html')), true);
  assert.equal(existsSync(join(outDir, 'pulse-claude-code-preview.json')), true);
  const status = readFileSync(join(outDir, 'pulse-migrate-status.md'), 'utf8');
  assert.match(status, /# Pulse Import Status/);
  assert.match(status, /ChatGPT archive: waiting for Request archive/);
  assert.match(status, /Claude archive: waiting for Export data/);
  assert.match(status, /Codex preview: ready/);
  assert.match(status, /Claude Code preview: ready/);
  assert.match(status, /Nothing has been imported yet/);
  assert.doesNotMatch(status, /PULSE_API_KEY|secret\.key|token=/i);
  const statusHTML = readFileSync(join(outDir, 'pulse-migrate-status.html'), 'utf8');
  assert.match(statusHTML, /Pulse Import Status/);
  assert.match(statusHTML, /data-design="pulse-glass-v3"/);
  assert.match(statusHTML, /backdrop-filter:blur/);
  assert.match(statusHTML, /Pulse Import/);
  assert.match(statusHTML, /Archive request/);
  assert.match(statusHTML, /Local sources/);
  assert.doesNotMatch(statusHTML, /Charter|Georgia|--paper:#f7f2e8|background-size:36px 36px/);
  assert.match(statusHTML, /What to do right now/);
  assert.match(statusHTML, /Click only these two host buttons/);
  assert.match(statusHTML, /ChatGPT request page/);
  assert.match(statusHTML, /Claude export page/);
  assert.match(statusHTML, /Ready to browse now/);
  assert.match(statusHTML, /Codex and Claude Code are local/);
  assert.match(statusHTML, /After you clicked/);
  assert.match(statusHTML, /Start live waiting/);
  assert.match(statusHTML, /pulse migrate start/);
  assert.match(statusHTML, /--watch/);
  assert.match(statusHTML, /Human click/);
  assert.match(statusHTML, /Open ChatGPT Data Controls/);
  assert.match(statusHTML, /https:\/\/chatgpt\.com\/#settings\/DataControls/);
  assert.match(statusHTML, /Open Claude Privacy/);
  assert.match(statusHTML, /https:\/\/claude\.ai\/settings\/privacy/);
  assert.match(statusHTML, /Waiting for Request archive/);
  assert.match(statusHTML, /Waiting for Export data/);
  assert.match(statusHTML, /Codex preview/);
  assert.match(statusHTML, /Claude Code preview/);
  assert.match(statusHTML, /Preview thread gallery/);
  assert.match(statusHTML, /pulse-memory-gallery\.html/);
  assert.match(statusHTML, /Open preview/);
  assert.match(statusHTML, /Continuity summary/);
  assert.match(statusHTML, /1 people/);
  assert.match(statusHTML, /1 decisions/);
  assert.match(statusHTML, /0 emotional anchors/);
  assert.match(statusHTML, /0 open loops/);
  assert.doesNotMatch(statusHTML, /fun facts/i);
  assert.match(statusHTML, /Copy command/);
  assert.match(statusHTML, /Nothing imported/);
  assert.match(statusHTML, /Paper boundary/);
  assert.match(statusHTML, /product extension, not an evaluated paper result/);
  assert.doesNotMatch(statusHTML, /PULSE_API_KEY|secret\.key|token=/i);
  const conciergeHTML = readFileSync(join(outDir, 'pulse-migrate-concierge.html'), 'utf8');
  assert.match(conciergeHTML, /Pulse Archive Concierge/);
  assert.match(conciergeHTML, /data-design="pulse-glass-v3"/);
  assert.match(conciergeHTML, /--pastel-a:/);
  assert.match(conciergeHTML, /Pulse Import/);
  assert.match(conciergeHTML, /Archive request/);
  assert.match(conciergeHTML, /Import gate/);
  assert.match(conciergeHTML, /Paper boundary/);
  assert.match(conciergeHTML, /product extension, not an evaluated paper result/);
  assert.doesNotMatch(conciergeHTML, /Charter|Georgia|--paper:#f7f2e8|background-size:36px 36px/);
  const galleryHTML = readFileSync(join(outDir, 'pulse-memory-gallery.html'), 'utf8');
  assert.match(galleryHTML, /Pulse Thread Gallery/);
  assert.match(galleryHTML, /data-design="pulse-glass-v3"/);
  assert.match(galleryHTML, /--pastel-a:/);
  assert.match(galleryHTML, /backdrop-filter:blur/);
  assert.match(galleryHTML, /glass-card/);
  assert.match(galleryHTML, /Pulse Import/);
  assert.match(galleryHTML, /Thread map/);
  assert.match(galleryHTML, /Needs your decision/);
  assert.doesNotMatch(galleryHTML, /Charter|Georgia|--paper:#f7f2e8|background-size:38px 38px|#151a1c/);
  assert.match(galleryHTML, /What Pulse can turn into continuity after review/);
  assert.match(galleryHTML, /People/);
  assert.match(galleryHTML, /People found/);
  assert.match(galleryHTML, /Decisions/);
  assert.match(galleryHTML, /Emotional anchors/);
  assert.match(galleryHTML, /Open loops/);
  assert.doesNotMatch(galleryHTML, /Fun facts/i);
  assert.match(galleryHTML, /Bob/);
  assert.match(galleryHTML, /Alice/);
  assert.match(galleryHTML, /Needs your decision/);
  assert.match(galleryHTML, /Qwen/);
  assert.match(galleryHTML, /Cartographer/);
  assert.doesNotMatch(galleryHTML, /<h3>Qwen<\/h3>/);
  assert.doesNotMatch(galleryHTML, /<h3>Cartographer<\/h3>/);
  assert.match(galleryHTML, /Codex/);
  assert.match(galleryHTML, /Claude Code/);
  assert.match(galleryHTML, /Filter gallery/);
  assert.match(galleryHTML, /Nothing imported yet/);
  assert.doesNotMatch(galleryHTML, /PULSE_API_KEY|secret\.key|token=/i);
  assert.match(readFileSync(join(outDir, 'pulse-codex-preview.html'), 'utf8'), /Filter preview/);
  assert.deepEqual(JSON.parse(readFileSync(join(outDir, 'pulse-codex-preview.json'), 'utf8')).raw_text_written, false);
});

test('migrate start puts curated people graph above local archive previews', async () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-start-people-home.'));
  const outDir = mkdtempSync(join(tmpdir(), 'pulse-start-people-out.'));
  const graphDir = writePeopleGraphFixture();
  const codexDir = join(home, '.codex', 'sessions', '2026', '06', '03');
  mkdirSync(codexDir, { recursive: true });
  writeFileSync(join(codexDir, 'session.jsonl'), [
    JSON.stringify({
      type: 'response_item',
      payload: {
        type: 'message',
        role: 'user',
        content: [{ type: 'input_text', text: 'Sophie and Qwen are fiction/model review noise. Bob is real.' }],
      },
    }),
  ].join('\n'));

  const result = await runAsync([
    'migrate',
    'start',
    '--dir',
    outDir,
    '--people-graph',
    graphDir,
    '--open',
  ], { HOME: home, PULSE_DATA_DIR: join(home, '.pulse'), PULSE_OPEN_DRY_RUN: '1' });

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /People Graph local graph selected/);
  assert.equal(existsSync(join(outDir, 'pulse-people-graph-preview.html')), true);
  assert.equal(existsSync(join(outDir, 'pulse-people-graph-preview.json')), true);
  const status = readFileSync(join(outDir, 'pulse-migrate-status.md'), 'utf8');
  assert.match(status, /People Graph preview: ready/);
  const galleryHTML = readFileSync(join(outDir, 'pulse-memory-gallery.html'), 'utf8');
  assert.match(galleryHTML, /Real people graph/);
  assert.match(galleryHTML, /Curated people graph/);
  assert.match(galleryHTML, /Bob Example/);
  assert.match(galleryHTML, /founder and warm product reviewer/);
  assert.match(galleryHTML, /pulse-distribution/);
  assert.match(galleryHTML, /Codex/);
  assert.ok(galleryHTML.indexOf('People Graph') < galleryHTML.indexOf('Codex'));
  assert.ok(galleryHTML.indexOf('Bob Example') < galleryHTML.indexOf('Sophie'));
  assert.doesNotMatch(galleryHTML, /<h3>Qwen<\/h3>/);
  const preview = JSON.parse(readFileSync(join(outDir, 'pulse-people-graph-preview.json'), 'utf8'));
  assert.equal(preview.source_kind, 'curated_people_graph');
  assert.equal(preview.raw_text_written, false);
});

test('migrate start watch previews arrived remote archive and keeps waiting path human-safe', { timeout: 10000 }, async () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-start-watch-home.'));
  const outDir = mkdtempSync(join(tmpdir(), 'pulse-start-watch-out.'));
  const downloads = mkdtempSync(join(tmpdir(), 'pulse-start-watch-downloads.'));
  const archiveDir = mkdtempSync(join(tmpdir(), 'pulse-start-watch-chatgpt-export.'));
  writeFileSync(join(archiveDir, 'conversations.json'), JSON.stringify([
    {
      title: 'Pulse watch flow',
      mapping: {
        user: {
          message: {
            author: { role: 'user' },
            content: { parts: ['Bob should inspect the Pulse watch flow preview.'] },
          },
        },
      },
    },
  ]));
  spawnSync('zip', ['-qr', join(downloads, 'chatgpt-export.zip'), '.'], { cwd: archiveDir });

  const result = await runAsync([
    'migrate',
    'start',
    '--dir',
    outDir,
    '--downloads',
    downloads,
    '--watch',
    '--timeout-ms',
    '250',
    '--interval-ms',
    '50',
  ], { HOME: home, PULSE_DATA_DIR: join(home, '.pulse'), PULSE_OPEN_DRY_RUN: '1' });

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /watching for ChatGPT and Claude archives/);
  assert.match(result.stdout, /in parallel/);
  assert.match(result.stdout, /latest chatgpt archive/);
  assert.match(result.stdout, /Claude archive not ready yet/);
  assert.equal(existsSync(join(outDir, 'pulse-chatgpt-preview.html')), true);
  assert.equal(existsSync(join(outDir, 'pulse-chatgpt-preview.json')), true);
  assert.equal(existsSync(join(outDir, 'pulse-claude-preview.html')), false);
  assert.match(readFileSync(join(outDir, 'pulse-migrate-status.md'), 'utf8'), /ChatGPT archive: preview ready/);
  assert.match(readFileSync(join(outDir, 'pulse-migrate-status.md'), 'utf8'), /Claude archive: not ready yet/);
  assert.match(readFileSync(join(outDir, 'pulse-migrate-status.html'), 'utf8'), /ChatGPT archive/);
  assert.match(readFileSync(join(outDir, 'pulse-migrate-status.html'), 'utf8'), /Preview ready/);
  assert.match(readFileSync(join(outDir, 'pulse-migrate-status.html'), 'utf8'), /Not ready yet/);
  assert.match(readFileSync(join(outDir, 'pulse-migrate-status.html'), 'utf8'), /http-equiv="refresh" content="5"/);
  assert.deepEqual(JSON.parse(readFileSync(join(outDir, 'pulse-chatgpt-preview.json'), 'utf8')).people_candidates.includes('Bob'), true);
});

test('migrate guide fails clearly for unknown sources', () => {
  const { result } = run(['migrate', 'guide', 'gemini']);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /supports: chatgpt, claude, codex, claude-code/);
});
