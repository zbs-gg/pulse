import { existsSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { randomBytes } from 'node:crypto';

import { vaultRuntimeFromBinding } from './local-supervisor.js';

export function captureStatePaths(globalDataDir, binding) {
  const paths = [join(globalDataDir, 'capture-state.json')];
  if (binding) {
    paths.push(join(vaultRuntimeFromBinding(binding).data_dir, 'capture-state.json'));
  }
  return [...new Set(paths)];
}

export function captureEnabledForHost(state, host) {
  if (state?.schema !== 'pulse.capture_state.v1') return false;
  if (state.hosts && typeof state.hosts === 'object') return state.hosts[host]?.enabled === true;
  return false;
}

export function writeCaptureStateFiles({
  globalDataDir, binding, host, enabled, globalEnabled, reason, changedAt = new Date(),
}) {
  if (!['codex', 'claude-code', 'cursor', 'opencode'].includes(host)) throw new Error('capture host is required');
	for (const path of captureStatePaths(globalDataDir, binding)) {
		const effectiveEnabled = path === join(globalDataDir, 'capture-state.json') && globalEnabled !== undefined
			? globalEnabled
			: enabled;
    let current = {};
    try {
      if (existsSync(path)) current = JSON.parse(readFileSync(path, 'utf8'));
    } catch {
      throw new Error(`invalid capture state: ${path}`);
    }
    const hosts = current?.schema === 'pulse.capture_state.v1' && current.hosts &&
      typeof current.hosts === 'object' ? { ...current.hosts } : {};
    if (Object.keys(hosts).length === 0 && current?.schema === 'pulse.capture_state.v1' &&
        current.enabled === true && ['codex_plugin_connected', 'claude_code_product_connected'].includes(current.reason)) {
      const legacyHost = current.reason === 'codex_plugin_connected' ? 'codex' : 'claude-code';
      hosts[legacyHost] = {
        enabled: true,
        reason: 'legacy_product_capture_migrated',
        changed_at: current.changed_at ?? changedAt.toISOString(),
      };
    }
		hosts[host] = { enabled: effectiveEnabled, reason, changed_at: changedAt.toISOString() };
    const body = `${JSON.stringify({
      schema: 'pulse.capture_state.v1',
      enabled: Object.values(hosts).some((entry) => entry?.enabled === true),
      hosts,
      changed_at: changedAt.toISOString(),
    }, null, 2)}\n`;
    const directory = dirname(path);
    mkdirSync(directory, { recursive: true, mode: 0o700 });
    const temporary = `${path}.${process.pid}.${randomBytes(8).toString('hex')}.new`;
    try {
      writeFileSync(temporary, body, { mode: 0o600, flag: 'wx' });
      renameSync(temporary, path);
    } finally {
      rmSync(temporary, { force: true });
    }
  }
  return captureStatePaths(globalDataDir, binding);
}
