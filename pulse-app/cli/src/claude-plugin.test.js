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
  assert.equal(manifest.hooks, './claude-hooks/hooks.json');
  assert.equal(manifest.mcpServers['pulse-product'].command, 'node');
  assert.deepEqual(manifest.mcpServers['pulse-product'].args, [
    '${CLAUDE_PLUGIN_ROOT}/mcp/claude-server.mjs',
  ]);

  const hooks = JSON.parse(readFileSync(join(pluginRoot, 'claude-hooks', 'hooks.json'), 'utf8'));
  assert.deepEqual(Object.keys(hooks.hooks).sort(), [
    'PostCompact', 'PostToolUse', 'PreCompact', 'PreToolUse', 'SessionStart', 'Stop',
    'SubagentStart', 'SubagentStop', 'UserPromptSubmit',
  ]);
  for (const [event, matchers] of Object.entries(hooks.hooks)) {
    for (const matcher of matchers) {
      for (const handler of matcher.hooks) {
        assert.equal(handler.type, 'command');
        assert.equal(handler.command, 'node');
        assert.deepEqual(handler.args, ['${CLAUDE_PLUGIN_ROOT}/hooks/claude-hook.mjs', event]);
      }
    }
  }
  assert.match(readFileSync(join(pluginRoot, 'hooks', 'claude-hook.mjs'), 'utf8'), /'claude-hook'/);
  assert.match(readFileSync(join(pluginRoot, 'mcp', 'claude-server.mjs'), 'utf8'), /'claude-mcp'/);
});

test('signed local marketplace exposes Pulse to Claude Code plugin installation', () => {
  const marketplace = JSON.parse(readFileSync(join(repoRoot, '.claude-plugin', 'marketplace.json'), 'utf8'));
  assert.equal(marketplace.name, 'zbs-gg');
  assert.deepEqual(marketplace.plugins.map((plugin) => plugin.name), ['pulse']);
  assert.equal(marketplace.plugins[0].source, './plugins/pulse');
});
