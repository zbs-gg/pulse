import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..', '..');
const pluginRoot = join(repoRoot, 'plugins', 'pulse');

test('Claude Code plugin is a native thin adapter over the shared Pulse runtime', () => {
  const manifest = JSON.parse(readFileSync(join(pluginRoot, '.claude-plugin', 'plugin.json'), 'utf8'));
  assert.equal(manifest.name, 'pulse');
  assert.equal(manifest.hooks, undefined);
  assert.equal(manifest.mcpServers['pulse-product'].command, '/bin/sh');
  assert.deepEqual(manifest.mcpServers['pulse-product'].args, [
    '-c', 'exec "$HOME/.local/bin/pulse" claude-mcp',
  ]);

  const hooks = JSON.parse(readFileSync(join(pluginRoot, 'hooks', 'hooks.json'), 'utf8'));
  assert.deepEqual(Object.keys(hooks.hooks).sort(), [
    'PostToolUse', 'PreToolUse', 'UserPromptSubmit',
  ]);
  for (const [event, matchers] of Object.entries(hooks.hooks)) {
    for (const matcher of matchers) {
      for (const handler of matcher.hooks) {
        assert.equal(handler.type, 'command');
        assert.equal(handler.args, undefined);
        assert.equal(handler.command, `exec "$HOME/.local/bin/pulse" product-hook ${event}`);
      }
    }
  }
  assert.match(readFileSync(join(repoRoot, 'pulse-app', 'cli', 'src', 'cli.js'), 'utf8'),
    /runProductHookCLI\(args\[1\]\)/);
});

test('signed local marketplace exposes Pulse to Claude Code plugin installation', () => {
  const marketplace = JSON.parse(readFileSync(join(repoRoot, '.claude-plugin', 'marketplace.json'), 'utf8'));
  assert.equal(marketplace.name, 'zbs-gg');
  assert.deepEqual(marketplace.plugins.map((plugin) => plugin.name), ['pulse']);
  assert.equal(marketplace.plugins[0].source, './plugins/pulse');
});
