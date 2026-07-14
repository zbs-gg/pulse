import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  inspectCodexRuntime,
  installCodexRuntime,
  migrateLegacyPulseHookFiles,
  parsePulsePluginList,
  pulseProductMcpShadowFiles,
} from './codex-install.js';
import { recordCodexHookReadiness } from './codex-hooks.js';

test('legacy hook file migration is atomic and preserves unrelated handlers', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-codex-install-'));
  try {
    const codexHome = join(root, 'home');
    const workspace = join(root, 'workspace');
    mkdirSync(codexHome, { recursive: true });
    mkdirSync(join(workspace, '.codex'), { recursive: true });
    const path = join(codexHome, 'hooks.json');
    writeFileSync(path, JSON.stringify({ hooks: { Stop: [{ hooks: [
      { type: 'command', command: 'pulse hook stop' },
      { type: 'command', command: 'echo keep' },
    ] }] } }));
    const result = migrateLegacyPulseHookFiles({ cwd: workspace, codexHome });
    assert.equal(result.removed, 1);
    const saved = JSON.parse(readFileSync(path, 'utf8'));
    assert.equal(saved.hooks.Stop[0].hooks[0].command, 'echo keep');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('Codex config detects only an explicit pulse-product MCP shadow', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-codex-shadow-'));
  try {
    const codexHome = join(root, 'home');
    mkdirSync(codexHome, { recursive: true });
    writeFileSync(join(codexHome, 'config.toml'), '[mcp_servers.pulse]\ncommand = "keep"\n');
    assert.deepEqual(pulseProductMcpShadowFiles({ cwd: root, codexHome }), []);
    writeFileSync(join(codexHome, 'config.toml'), '[mcp_servers."pulse-product"]\ncommand = "shadow"\n');
    assert.deepEqual(pulseProductMcpShadowFiles({ cwd: root, codexHome }), [join(codexHome, 'config.toml')]);
    writeFileSync(join(codexHome, 'config.toml'), 'mcp_servers."pulse-product" = { command = "shadow" }\n');
    assert.deepEqual(pulseProductMcpShadowFiles({ cwd: root, codexHome }), [join(codexHome, 'config.toml')]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('plugin list parser accepts only enabled pulse from zbs-gg', () => {
  const parsed = parsePulsePluginList(
    'PLUGIN          STATUS              VERSION  PATH\n' +
    'pulse@zbs-gg   installed, enabled  0.7.0    /tmp/cache/pulse\n',
  );
  assert.deepEqual(parsed, { enabled: true, version: '0.7.0', path: '/tmp/cache/pulse' });
  assert.equal(parsePulsePluginList('pulse@other  installed, enabled  1.0.0  /tmp/no').enabled, false);
});

test('successful trusted hook writes a content-free global readiness receipt', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-codex-ready-'));
  try {
    assert.equal(recordCodexHookReadiness('SessionStart', {
      dataDir: root,
      hooksDigest: 'a'.repeat(64),
      now: new Date('2026-07-14T10:00:00Z'),
    }), true);
    assert.deepEqual(JSON.parse(readFileSync(join(root, 'codex-hook-readiness.json'), 'utf8')), {
      schema: 'pulse.codex_hook_readiness.v1',
      hooks_digest: 'a'.repeat(64),
      last_event: 'SessionStart',
      observed_at: '2026-07-14T10:00:00.000Z',
    });
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('Codex runtime install is local, atomic, and integrity checked', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-codex-runtime-'));
  try {
    const source = join(root, 'source');
    const dataDir = join(root, 'data');
    mkdirSync(join(source, 'src'), { recursive: true });
    mkdirSync(join(source, 'vendor', 'pulse-mcp-dist'), { recursive: true });
    mkdirSync(join(source, 'node_modules'), { recursive: true });
    writeFileSync(join(source, 'package.json'), JSON.stringify({ name: '@zbs-gg/pulse', version: '0.7.0' }));
    writeFileSync(join(source, 'src', 'cli.js'), '#!/usr/bin/env node\n');
    writeFileSync(join(source, 'vendor', 'pulse-mcp-dist', 'index.js'), 'export {};\n');
    const installed = installCodexRuntime(source, dataDir, { now: new Date('2026-07-14T10:00:00Z') });
    assert.equal(installed.ok, true);
    assert.match(installed.digest, /^[a-f0-9]{64}$/);
    writeFileSync(installed.path, '#!/usr/bin/env node\n// tampered\n');
    assert.equal(inspectCodexRuntime(dataDir).ok, false);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
