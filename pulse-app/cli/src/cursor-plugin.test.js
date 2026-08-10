import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..', '..');
const pluginRoot = join(repoRoot, 'plugins', 'pulse');

test('Cursor plugin is a native thin adapter over the shared Pulse runtime', () => {
  const manifest = JSON.parse(readFileSync(join(pluginRoot, '.cursor-plugin', 'plugin.json'), 'utf8'));
  assert.equal(manifest.name, 'pulse');
  assert.equal(manifest.hooks, './cursor-hooks/hooks.json');
  assert.equal(manifest.mcpServers['pulse-product'].command, 'node');
  assert.deepEqual(manifest.mcpServers['pulse-product'].args, [
    '${CURSOR_PLUGIN_ROOT}/mcp/cursor-server.mjs',
  ]);

  const hooks = JSON.parse(readFileSync(join(pluginRoot, 'cursor-hooks', 'hooks.json'), 'utf8'));
  assert.equal(hooks.version, 1);
  assert.deepEqual(Object.keys(hooks.hooks).sort(), [
    'beforeSubmitPrompt', 'postToolUse', 'preToolUse',
  ]);
  for (const [event, entries] of Object.entries(hooks.hooks)) {
    assert.equal(entries.length, 1);
    assert.equal(
      entries[0].command,
      `node \"\${CURSOR_PLUGIN_ROOT}/hooks/cursor-hook.mjs\" ${event}`,
    );
  }

  const launcher = readFileSync(join(pluginRoot, 'hooks', 'cursor-hook.mjs'), 'utf8');
  const server = readFileSync(join(pluginRoot, 'mcp', 'cursor-server.mjs'), 'utf8');
  assert.match(launcher, /runHookWorkerClient\(/);
  assert.match(launcher, /host: 'cursor'/);
  assert.doesNotMatch(launcher, /spawn\(/);
  assert.match(server, /'cursor-mcp'/);
  assert.match(launcher, /runtime-locator\.mjs/);
  assert.match(server, /runtime-locator\.mjs/);
});

test('published Pulse package carries the Cursor adapter and CLI entrypoints', () => {
  const packageJSON = JSON.parse(readFileSync(join(repoRoot, 'pulse-app', 'cli', 'package.json'), 'utf8'));
  assert.ok(packageJSON.files.includes('src/cursor-hooks.js'));
  assert.ok(packageJSON.files.includes('src/product-hook-entrypoint.js'));
  const cli = readFileSync(join(repoRoot, 'pulse-app', 'cli', 'src', 'cli.js'), 'utf8');
  assert.match(cli, /command === 'cursor-mcp'/);
  assert.match(cli, /command === 'cursor-hook'/);
});
