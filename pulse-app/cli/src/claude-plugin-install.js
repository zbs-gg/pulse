import { spawnSync } from 'node:child_process';
import { isAbsolute, resolve } from 'node:path';

import { inspectCodexPluginCompatibility } from './codex-install.js';

export function parseClaudePluginList(value) {
  try {
    const list = typeof value === 'string' ? JSON.parse(value) : value;
    if (!Array.isArray(list)) return { installed: false, enabled: false };
    const plugin = list.find((entry) => entry?.id === 'pulse@zbs-gg');
    if (!plugin || typeof plugin.installPath !== 'string' || !isAbsolute(plugin.installPath) ||
        typeof plugin.version !== 'string') {
      return { installed: false, enabled: false };
    }
    return {
      installed: true,
      enabled: plugin.enabled === true,
      version: plugin.version,
      path: resolve(plugin.installPath),
    };
  } catch {
    return { installed: false, enabled: false };
  }
}

function claudePluginCommand(args, executable = 'claude') {
  return spawnSync(executable, ['plugin', ...args], {
    encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 30_000, killSignal: 'SIGTERM',
  });
}

function requireSuccess(result, action) {
  if (result?.status === 0) return;
  const detail = `${result?.stderr || result?.stdout || result?.error?.message || ''}`.trim().slice(0, 500);
  throw new Error(`claude_plugin_${action}_failed${detail ? `:${detail}` : ''}`);
}

export function activateClaudePlugin(edge, {
  executable = 'claude',
  run = (args) => claudePluginCommand(args, executable),
  inspect = inspectCodexPluginCompatibility,
} = {}) {
  if (!edge || typeof edge.marketplace_root !== 'string' || !isAbsolute(edge.marketplace_root) ||
      typeof edge.plugin_root !== 'string' || !isAbsolute(edge.plugin_root) ||
      !/^[a-f0-9]{64}$/.test(edge.plugin_tree_digest ?? '') ||
      typeof edge.release_version !== 'string' || edge.release_version.length < 1) {
    throw new Error('claude_plugin_edge_invalid');
  }
  const beforeResult = run(['list', '--json']);
  requireSuccess(beforeResult, 'list_before');
  const before = parseClaudePluginList(beforeResult.stdout);

  requireSuccess(run(['marketplace', 'add', edge.marketplace_root, '--scope', 'user']), 'marketplace_add');
  requireSuccess(run([
    before.installed ? 'update' : 'install', 'pulse@zbs-gg', '--scope', 'user',
  ]), before.installed ? 'update' : 'install');
  if (!before.enabled) {
    requireSuccess(run(['enable', 'pulse@zbs-gg', '--scope', 'user']), 'enable');
  }

  const afterResult = run(['list', '--json']);
  requireSuccess(afterResult, 'list_after');
  let after = parseClaudePluginList(afterResult.stdout);
  let verified = inspect(after, edge);
  let reinstalled = false;
  if (!verified?.ok && before.installed) {
    // Claude treats a same-version update as a no-op even when the signed
    // marketplace bytes changed. Replace only Pulse, preserve its data, and
    // prove the installed tree again instead of accepting the stale cache.
    requireSuccess(run([
      'uninstall', 'pulse@zbs-gg', '--scope', 'user', '--keep-data',
    ]), 'reinstall_uninstall');
    requireSuccess(run(['install', 'pulse@zbs-gg', '--scope', 'user']), 'reinstall_install');
    let reinstalledResult = run(['list', '--json']);
    requireSuccess(reinstalledResult, 'list_after_reinstall');
    after = parseClaudePluginList(reinstalledResult.stdout);
    if (!after.enabled) {
      requireSuccess(run(['enable', 'pulse@zbs-gg', '--scope', 'user']), 'reinstall_enable');
      reinstalledResult = run(['list', '--json']);
      requireSuccess(reinstalledResult, 'list_after_reinstall_enable');
      after = parseClaudePluginList(reinstalledResult.stdout);
    }
    verified = inspect(after, edge);
    reinstalled = true;
  }
  if (!verified?.ok) throw new Error(`claude_plugin_verification_failed:${verified?.reason ?? 'unknown'}`);
  return { ...verified, plugin: after, reinstalled, reused: before.installed && !reinstalled };
}

export function disableClaudePlugin({
  executable = 'claude',
  run = (args) => claudePluginCommand(args, executable),
} = {}) {
  const beforeResult = run(['list', '--json']);
  requireSuccess(beforeResult, 'list_before_disable');
  const before = parseClaudePluginList(beforeResult.stdout);
  if (!before.installed || !before.enabled) return { disabled: false, plugin: before };
  requireSuccess(run(['disable', 'pulse@zbs-gg', '--scope', 'user']), 'disable');
  const afterResult = run(['list', '--json']);
  requireSuccess(afterResult, 'list_after_disable');
  const after = parseClaudePluginList(afterResult.stdout);
  if (after.installed && after.enabled) throw new Error('claude_plugin_disable_verification_failed');
  return { disabled: true, plugin: after };
}
