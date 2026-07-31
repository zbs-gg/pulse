import assert from 'node:assert/strict';
import { chmodSync, mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { inspectCursorPlugin, installCursorPlugin, removeCursorPlugin } from './cursor-install.js';

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
    const bad = fixturePlugin(root, '0.8.0');
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
