import assert from 'node:assert/strict';
import { existsSync, mkdirSync, mkdtempSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  detectClaudeCodeCLI,
  detectCodexCLI,
  detectCursorInstallation,
  detectSupportedHosts,
  SUPPORTED_HOST_IDS,
} from './supported-hosts.js';
import { createPlatformServices } from './platform-services.js';

const available = (extra = {}) => ({ available: true, reason_code: null, ...extra });
const missing = (host) => ({ available: false, reason_code: `${host}_missing` });

test('supported host inventory is stable and any singleton becomes the activation target', () => {
  assert.deepEqual(SUPPORTED_HOST_IDS, ['claude-code', 'codex', 'cursor']);
  for (const selected of SUPPORTED_HOST_IDS) {
    const hosts = detectSupportedHosts({
      detectClaude: () => selected === 'claude-code' ? available() : missing('claude'),
      detectCodex: () => selected === 'codex' ? available() : missing('codex'),
      detectCursor: () => selected === 'cursor' ? available() : missing('cursor'),
    });
    assert.deepEqual(hosts.map((host) => host.host), SUPPORTED_HOST_IDS);
    assert.deepEqual(hosts.filter((host) => host.activation_target).map((host) => host.host), [selected]);
    assert.equal(hosts.find((host) => host.host === selected).compatible, true);
  }
});

test('detected but incompatible hosts are distinct from absent hosts', () => {
  const hosts = detectSupportedHosts({
    detectClaude: () => ({ available: false, executable_path: '/usr/local/bin/claude', reason_code: 'claude_version_invalid' }),
    detectCodex: () => missing('codex'),
    detectCursor: () => missing('cursor'),
  });
  assert.deepEqual(hosts.map(({ host, detected, compatible, activation_target }) => ({ host, detected, compatible, activation_target })), [
    { host: 'claude-code', detected: true, compatible: false, activation_target: false },
    { host: 'codex', detected: false, compatible: false, activation_target: false },
    { host: 'cursor', detected: false, compatible: false, activation_target: false },
  ]);
});

test('Claude detection ignores inherited PATH and hashes only bounded absolute candidates', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-claude-detect.'));
  const previousPath = process.env.PATH;
  try {
    const projectBin = join(root, 'project-bin');
    const trustedBin = join(root, 'trusted-bin');
    const marker = join(root, 'executed');
    mkdirSync(projectBin);
    mkdirSync(trustedBin);
    writeFileSync(join(projectBin, 'claude'), `#!/bin/sh\ntouch ${JSON.stringify(marker)}\n`, { mode: 0o700 });
    const trusted = join(trustedBin, 'claude');
    writeFileSync(trusted, '#!/bin/sh\nexit 0\n', { mode: 0o700 });
    process.env.PATH = `${projectBin}:${previousPath ?? ''}`;

    const detected = detectClaudeCodeCLI({ candidates: [trusted] });
    assert.equal(detected.available, true);
    assert.match(detected.executable_sha256, /^[a-f0-9]{64}$/);
    assert.equal(detected.version, null);
    assert.throws(() => detectClaudeCodeCLI({ candidates: ['claude'] }), /claude_path_invalid/);
    assert.equal(detected.executable_path, realpathSync(trusted));
    assert.equal(detected.reason_code, null);
    assert.equal(detected.executable_path.includes(projectBin), false);
    assert.equal(existsSync(marker), false);
  } finally {
    if (previousPath === undefined) delete process.env.PATH;
    else process.env.PATH = previousPath;
    rmSync(root, { recursive: true, force: true });
  }
});

test('Cursor detection accepts the trusted app surface without a Cursor CLI', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-cursor-detect.'));
  try {
    const app = join(root, 'Cursor.app');
    mkdirSync(app);
    assert.deepEqual(detectCursorInstallation({ appCandidates: [app] }), {
      available: true,
      app_path: realpathSync(app),
      reason_code: null,
    });
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('Windows host discovery uses bounded native-adapter candidates and never POSIX mode bits', () => {
  const codexPath = 'C:\\Users\\Pulse\\AppData\\Roaming\\npm\\codex.cmd';
  const services = createPlatformServices({
    platform: 'win32', home: 'C:\\Users\\Pulse',
    nativeAdapter: {
      inspectExecutable: (path) => ({
        canonical_path: path, executable: true, owner_only: true, regular_file: true,
        reparse_point: false, sha256: 'c'.repeat(64),
      }),
    },
  });
  const detected = detectCodexCLI({
    candidates: [codexPath], platformServices: services,
  });
  assert.equal(detected.available, true);
  assert.equal(detected.executable_path, codexPath);
  assert.equal(detected.executable_sha256, 'c'.repeat(64));
});
