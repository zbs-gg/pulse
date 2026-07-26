import assert from 'node:assert/strict';
import test from 'node:test';

import { activateClaudePlugin, disableClaudePlugin, parseClaudePluginList } from './claude-plugin-install.js';

test('Claude plugin list parser returns only the exact Pulse marketplace identity', () => {
  const parsed = parseClaudePluginList(JSON.stringify([
    { id: 'pulse@other', version: '0.7.0', enabled: true, installPath: '/other' },
    { id: 'pulse@zbs-gg', version: '0.7.0', enabled: true, installPath: '/signed/pulse' },
  ]));
  assert.deepEqual(parsed, {
    installed: true, enabled: true, version: '0.7.0', path: '/signed/pulse',
  });
  assert.equal(parseClaudePluginList('not-json').installed, false);
});

test('Claude native activation adds the signed marketplace, installs, enables, and verifies exact bytes', () => {
  const calls = [];
  let listed = 0;
  const edge = {
    marketplace_root: '/signed/marketplace', plugin_root: '/signed/marketplace/plugins/pulse',
    plugin_tree_digest: 'a'.repeat(64), release_version: '0.7.0',
  };
  const result = activateClaudePlugin(edge, {
    run: (args) => {
      calls.push(args);
      if (args[0] === 'list') {
        listed++;
        return { status: 0, stdout: JSON.stringify(listed === 1 ? [] : [{
          id: 'pulse@zbs-gg', version: '0.7.0', enabled: true, installPath: '/cache/pulse',
        }]), stderr: '' };
      }
      return { status: 0, stdout: '', stderr: '' };
    },
    inspect: (plugin, expected) => ({
      ok: plugin.path === '/cache/pulse' && expected === edge,
      reason: 'claude_plugin_exact', digest: edge.plugin_tree_digest,
    }),
  });
  assert.equal(result.ok, true);
  assert.deepEqual(calls, [
    ['list', '--json'],
    ['marketplace', 'add', '/signed/marketplace', '--scope', 'user'],
    ['install', 'pulse@zbs-gg', '--scope', 'user'],
    ['enable', 'pulse@zbs-gg', '--scope', 'user'],
    ['list', '--json'],
  ]);
});

test('Claude native activation does not re-enable an already enabled plugin', () => {
  const calls = [];
  const edge = {
    marketplace_root: '/signed/marketplace', plugin_root: '/signed/marketplace/plugins/pulse',
    plugin_tree_digest: 'a'.repeat(64), release_version: '0.7.0',
  };
  const result = activateClaudePlugin(edge, {
    run: (args) => {
      calls.push(args);
      if (args[0] === 'list') {
        return { status: 0, stdout: JSON.stringify([{
          id: 'pulse@zbs-gg', version: '0.7.0', enabled: true, installPath: '/cache/pulse',
        }]), stderr: '' };
      }
      if (args[0] === 'enable') {
        return { status: 1, stdout: '', stderr: 'Plugin is already enabled at user scope' };
      }
      return { status: 0, stdout: '', stderr: '' };
    },
    inspect: () => ({ ok: true, reason: 'claude_plugin_exact', digest: edge.plugin_tree_digest }),
  });
  assert.equal(result.ok, true);
  assert.equal(calls.some((args) => args[0] === 'enable'), false);
});

test('Claude native activation repairs same-version byte drift with one data-preserving reinstall', () => {
  const calls = [];
  let listed = 0;
  const edge = {
    marketplace_root: '/signed/marketplace', plugin_root: '/signed/marketplace/plugins/pulse',
    plugin_tree_digest: 'a'.repeat(64), release_version: '0.7.0',
  };
  const result = activateClaudePlugin(edge, {
    run: (args) => {
      calls.push(args);
      if (args[0] === 'list') {
        listed += 1;
        const exact = listed >= 3;
        return { status: 0, stdout: JSON.stringify([{
          id: 'pulse@zbs-gg',
          version: '0.7.0',
          enabled: true,
          installPath: exact ? '/cache/pulse-exact' : '/cache/pulse-stale',
        }]), stderr: '' };
      }
      return { status: 0, stdout: '', stderr: '' };
    },
    inspect: (plugin) => ({
      ok: plugin.path === '/cache/pulse-exact',
      reason: plugin.path === '/cache/pulse-exact' ? 'claude_plugin_exact' : 'claude_plugin_tree_mismatch',
      digest: plugin.path === '/cache/pulse-exact' ? edge.plugin_tree_digest : '',
    }),
  });

  assert.equal(result.ok, true);
  assert.equal(result.reinstalled, true);
  assert.equal(result.reused, false);
  assert.deepEqual(calls, [
    ['list', '--json'],
    ['marketplace', 'add', '/signed/marketplace', '--scope', 'user'],
    ['update', 'pulse@zbs-gg', '--scope', 'user'],
    ['list', '--json'],
    ['uninstall', 'pulse@zbs-gg', '--scope', 'user', '--keep-data'],
    ['install', 'pulse@zbs-gg', '--scope', 'user'],
    ['list', '--json'],
  ]);
});

test('Claude native disconnect disables the unused user plugin and verifies the result', () => {
  const calls = [];
  let listed = 0;
  const result = disableClaudePlugin({
    run: (args) => {
      calls.push(args);
      if (args[0] === 'list') {
        listed += 1;
        return { status: 0, stdout: JSON.stringify([{
          id: 'pulse@zbs-gg', version: '0.7.0', enabled: listed === 1, installPath: '/cache/pulse',
        }]), stderr: '' };
      }
      return { status: 0, stdout: '', stderr: '' };
    },
  });
  assert.equal(result.disabled, true);
  assert.deepEqual(calls, [
    ['list', '--json'],
    ['disable', 'pulse@zbs-gg', '--scope', 'user'],
    ['list', '--json'],
  ]);
});
