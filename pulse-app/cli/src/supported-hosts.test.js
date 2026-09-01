import assert from 'node:assert/strict';
import { existsSync, mkdirSync, mkdtempSync, readdirSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  detectClaudeCodeCLI,
  detectCodexCLI,
  detectCursorInstallation,
  detectOpenCodeCLI,
  detectSupportedHosts,
  probeHostVersion,
  SUPPORTED_HOST_IDS,
} from './supported-hosts.js';
import { createPlatformServices } from './platform-services.js';

const available = (extra = {}) => ({ available: true, reason_code: null, ...extra });
const missing = (host) => ({ available: false, reason_code: `${host}_missing` });

test('supported host inventory is stable and any singleton becomes the activation target', () => {
  assert.deepEqual(SUPPORTED_HOST_IDS, ['claude-code', 'codex', 'cursor', 'opencode']);
  for (const selected of SUPPORTED_HOST_IDS) {
    const hosts = detectSupportedHosts({
      detectClaude: () => selected === 'claude-code' ? available() : missing('claude'),
      detectCodex: () => selected === 'codex' ? available() : missing('codex'),
      detectCursor: () => selected === 'cursor' ? available() : missing('cursor'),
      detectOpenCode: () => selected === 'opencode' ? available() : missing('opencode'),
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
    detectOpenCode: () => missing('opencode'),
  });
  assert.deepEqual(hosts.map(({ host, detected, compatible, activation_target }) => ({ host, detected, compatible, activation_target })), [
    { host: 'claude-code', detected: true, compatible: false, activation_target: false },
    { host: 'codex', detected: false, compatible: false, activation_target: false },
    { host: 'cursor', detected: false, compatible: false, activation_target: false },
    { host: 'opencode', detected: false, compatible: false, activation_target: false },
  ]);
});

test('OpenCode detection supports only 1.18.x on Apple Silicon macOS', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-opencode-detect.'));
  try {
    const executable = join(root, 'opencode');
    writeFileSync(executable, '#!/bin/sh\nexit 0\n', { mode: 0o700 });
    const services = createPlatformServices({ platform: 'darwin', architecture: 'arm64' });
    const supported = detectOpenCodeCLI({
      candidates: [executable], platformServices: services,
      versionProbe: () => ({ status: 0, stdout: '1.18.15' }),
    });
    assert.equal(supported.available, true);
    assert.equal(supported.compatible, true);
    assert.equal(supported.version, '1.18.15');

    const future = detectOpenCodeCLI({
      candidates: [executable], platformServices: services,
      versionProbe: () => ({ status: 0, stdout: '1.19.0' }),
    });
    assert.equal(future.detected, true);
    assert.equal(future.compatible, false);
    assert.equal(future.reason_code, 'opencode_version_incompatible');

    const intel = detectOpenCodeCLI({
      candidates: [executable],
      platformServices: createPlatformServices({ platform: 'darwin', architecture: 'x64' }),
      versionProbe: () => ({ status: 0, stdout: '1.18.15' }),
    });
    assert.equal(intel.compatible, false);
    assert.equal(intel.reason_code, 'opencode_platform_incompatible');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
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
    const executable = join(app, 'Contents', 'MacOS', 'Cursor');
    mkdirSync(join(app, 'Contents', 'MacOS'), { recursive: true });
    writeFileSync(executable, '#!/bin/sh\nexit 0\n', { mode: 0o700 });
    const first = detectCursorInstallation({ appCandidates: [app] });
    const firstDigest = first.executable_sha256;
    assert.deepEqual(detectCursorInstallation({ appCandidates: [app] }), {
      available: true,
      app_path: realpathSync(app),
      executable_path: realpathSync(executable),
      executable_sha256: firstDigest,
      reason_code: null,
    });
    writeFileSync(executable, '#!/bin/sh\nexit 7\n', { mode: 0o700 });
    const changed = detectCursorInstallation({ appCandidates: [app] });
    assert.notEqual(changed.executable_sha256, firstDigest);
    const emptyApp = join(root, 'Empty.app');
    mkdirSync(emptyApp);
    assert.equal(detectCursorInstallation({ appCandidates: [emptyApp] }).available, false);
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
		versionProbe: () => ({ status: 0, stdout: 'codex-cli 0.8.1' }),
  });
  assert.equal(detected.available, true);
  assert.equal(detected.executable_path, codexPath);
  assert.equal(detected.executable_sha256, 'c'.repeat(64));
});

test('Codex detection skips a broken CLI and selects the next working bounded candidate', () => {
	const root = mkdtempSync(join(tmpdir(), 'pulse-codex-detect.'));
	try {
		const broken = join(root, 'broken-codex');
		const desktop = join(root, 'desktop-codex');
		writeFileSync(broken, '#!/bin/sh\nexit 127\n', { mode: 0o700 });
		writeFileSync(desktop, '#!/bin/sh\nexit 0\n', { mode: 0o700 });
		const detected = detectCodexCLI({
			candidates: [broken, desktop],
			versionProbe: (path) => path === realpathSync(broken)
				? { status: 127, stderr: 'broken runtime' }
				: { status: 0, stdout: 'codex-cli 0.146.0-alpha.9.2' },
		});
		assert.equal(detected.available, true);
		assert.equal(detected.executable_path, realpathSync(desktop));
		assert.equal(detected.version, '0.146.0-alpha.9.2');
	} finally {
		rmSync(root, { recursive: true, force: true });
	}
});

test('host version probe runs an absolute executable without PATH lookup', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-host-version-'));
  const protectedCodexHome = join(root, 'protected-codex-home');
  const protectedHome = join(root, 'protected-home');
  const previousCodexHome = process.env.CODEX_HOME;
  const previousHome = process.env.HOME;
  try {
    mkdirSync(protectedCodexHome);
    mkdirSync(protectedHome);
    const executable = join(root, 'codex-desktop');
    writeFileSync(executable, '#!/bin/sh\ntouch "$CODEX_HOME/probed"\ntouch "$HOME/probed"\nprintf "codex-cli 0.146.0\\n"\n', { mode: 0o700 });
    process.env.CODEX_HOME = protectedCodexHome;
    process.env.HOME = protectedHome;
    const result = probeHostVersion(executable);
    assert.equal(result.status, 0);
    assert.match(result.stdout, /0\.146\.0/);
    assert.deepEqual(readdirSync(protectedCodexHome), []);
    assert.deepEqual(readdirSync(protectedHome), []);
  } finally {
    if (previousCodexHome === undefined) delete process.env.CODEX_HOME;
    else process.env.CODEX_HOME = previousCodexHome;
    if (previousHome === undefined) delete process.env.HOME;
    else process.env.HOME = previousHome;
    rmSync(root, { recursive: true, force: true });
  }
});
