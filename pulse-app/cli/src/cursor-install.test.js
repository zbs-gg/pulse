import assert from 'node:assert/strict';
import { chmodSync, mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  inspectCursorNativeIntegration,
  inspectCursorPlugin,
  installCursorNativeIntegration,
  installCursorPlugin,
  removeCursorNativeIntegration,
  removeCursorPlugin,
  restoreCursorNativeIntegrationBackup,
} from './cursor-install.js';

function fixturePlugin(root, version = '0.7.0') {
  const plugin = join(root, `source-${version}`);
  mkdirSync(join(plugin, '.cursor-plugin'), { recursive: true });
  mkdirSync(join(plugin, 'hooks'), { recursive: true });
  mkdirSync(join(plugin, 'mcp'), { recursive: true });
  writeFileSync(join(plugin, '.cursor-plugin', 'plugin.json'), JSON.stringify({
    name: 'pulse', version, hooks: './cursor-hooks/hooks.json',
    mcpServers: { 'pulse-product': { command: 'node', args: ['${CURSOR_PLUGIN_ROOT}/mcp/cursor-server.mjs'] } },
  }));
  writeFileSync(join(plugin, 'hooks', 'cursor-hook.mjs'), 'export {};\n');
  writeFileSync(join(plugin, 'mcp', 'cursor-server.mjs'), 'export {};\n');
  return plugin;
}

test('Cursor plugin installation is local, atomic, exact, and idempotent', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-cursor-plugin-'));
  try {
    const cursorHome = join(root, '.cursor');
    const source = fixturePlugin(root);
    const first = installCursorPlugin(source, { cursorHome });
    assert.equal(first.reused, false);
    assert.equal(first.path, join(cursorHome, 'plugins', 'local', 'pulse'));
    assert.equal(inspectCursorPlugin({ cursorHome }).ready, true);
    const second = installCursorPlugin(source, { cursorHome, expectedDigest: first.digest });
    assert.equal(second.reused, true);
    assert.equal(second.digest, first.digest);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('Cursor plugin installation rejects links and restores the last known good tree', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-cursor-plugin-unsafe-'));
  try {
    const cursorHome = join(root, '.cursor');
    const good = installCursorPlugin(fixturePlugin(root, '0.7.0'), { cursorHome });
    const bad = fixturePlugin(root, '0.8.1');
    chmodSync(join(bad, 'hooks', 'cursor-hook.mjs'), 0o777);
    assert.throws(() => installCursorPlugin(bad, { cursorHome }), /cursor_plugin_source_unsafe/);
    const current = inspectCursorPlugin({ cursorHome });
    assert.equal(current.ready, true);
    assert.equal(current.digest, good.digest);
    assert.equal(JSON.parse(readFileSync(join(current.path, '.cursor-plugin', 'plugin.json'))).version, '0.7.0');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('Cursor plugin removal is idempotent and leaves the containing Cursor home intact', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-cursor-plugin-remove-'));
  try {
    const cursorHome = join(root, '.cursor');
    installCursorPlugin(fixturePlugin(root), { cursorHome });
    assert.deepEqual(removeCursorPlugin({ cursorHome }), { removed: true });
    assert.equal(inspectCursorPlugin({ cursorHome }).reason, 'cursor_plugin_missing');
    assert.deepEqual(removeCursorPlugin({ cursorHome }), { removed: false });
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('Cursor native integration merges hooks and MCP for IDE and Agent CLI without replacing other tools', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-cursor-native-'));
  try {
    const cursorHome = join(root, '.cursor');
    const source = fixturePlugin(root, '0.8.1');
    mkdirSync(cursorHome, { recursive: true });
    writeFileSync(join(cursorHome, 'hooks.json'), JSON.stringify({
      version: 1,
      hooks: {
        beforeSubmitPrompt: [{ command: '/opt/orca/cursor-hook.sh', timeout: 10 }],
        stop: [{ command: '/opt/orca/cursor-hook.sh', timeout: 10 }],
      },
    }));
    writeFileSync(join(cursorHome, 'mcp.json'), JSON.stringify({
      mcpServers: {
        'blitz-iphone': { command: 'blitz-mcp' },
        pulse_local: { command: 'old-pulse' },
      },
    }));
    installCursorPlugin(source, { cursorHome });

    const installed = installCursorNativeIntegration(source, { cursorHome });
    assert.equal(installed.ready, true);
    assert.equal(installed.reused, false);
    assert.ok(installed.backup);
    assert.equal(inspectCursorPlugin({ cursorHome }).reason, 'cursor_plugin_missing');
    const hooks = JSON.parse(readFileSync(join(cursorHome, 'hooks.json'), 'utf8'));
    assert.equal(hooks.hooks.beforeSubmitPrompt[0].command, '/opt/orca/cursor-hook.sh');
    assert.equal(hooks.hooks.stop[0].command, '/opt/orca/cursor-hook.sh');
    assert.equal(hooks.hooks.beforeSubmitPrompt.filter((entry) =>
      entry.command.includes('/hooks/cursor-hook.mjs')).length, 1);
    assert.equal(hooks.hooks.preToolUse.length, 1);
    assert.equal(hooks.hooks.postToolUse.length, 1);
    const mcp = JSON.parse(readFileSync(join(cursorHome, 'mcp.json'), 'utf8'));
    assert.equal(mcp.mcpServers['blitz-iphone'].command, 'blitz-mcp');
    assert.equal(mcp.mcpServers.pulse_local, undefined);
    assert.deepEqual(mcp.mcpServers['pulse-product'], {
      command: 'node', args: [join(source, 'mcp', 'cursor-server.mjs')],
    });

    const second = installCursorNativeIntegration(source, { cursorHome, expectedDigest: installed.digest });
    assert.equal(second.reused, true);
    assert.equal(inspectCursorNativeIntegration({
      cursorHome, pluginRoot: source, expectedDigest: installed.digest,
    }).ready, true);
    assert.deepEqual(restoreCursorNativeIntegrationBackup(installed.backup, { cursorHome }), { restored: true });
    assert.equal(inspectCursorPlugin({ cursorHome }).ready, true);
    const restoredMCP = JSON.parse(readFileSync(join(cursorHome, 'mcp.json'), 'utf8'));
    assert.equal(restoredMCP.mcpServers.pulse_local.command, 'old-pulse');
    assert.equal(restoredMCP.mcpServers['pulse-product'], undefined);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('Cursor native disconnect removes only Pulse settings and preserves other hooks and MCP servers', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-cursor-native-remove-'));
  try {
    const cursorHome = join(root, '.cursor');
    const source = fixturePlugin(root, '0.8.1');
    mkdirSync(cursorHome, { recursive: true });
    writeFileSync(join(cursorHome, 'hooks.json'), JSON.stringify({
      version: 1, hooks: { beforeSubmitPrompt: [{ command: '/opt/orca/cursor-hook.sh' }] },
    }));
    writeFileSync(join(cursorHome, 'mcp.json'), JSON.stringify({
      mcpServers: { 'blitz-iphone': { command: 'blitz-mcp' } },
    }));
    installCursorNativeIntegration(source, { cursorHome });
    assert.deepEqual(removeCursorNativeIntegration({ cursorHome }), { removed: true });
    const hooks = JSON.parse(readFileSync(join(cursorHome, 'hooks.json'), 'utf8'));
    assert.deepEqual(hooks.hooks.beforeSubmitPrompt, [{ command: '/opt/orca/cursor-hook.sh' }]);
    assert.deepEqual(hooks.hooks.preToolUse, []);
    assert.deepEqual(hooks.hooks.postToolUse, []);
    const mcp = JSON.parse(readFileSync(join(cursorHome, 'mcp.json'), 'utf8'));
    assert.deepEqual(mcp.mcpServers, { 'blitz-iphone': { command: 'blitz-mcp' } });
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
