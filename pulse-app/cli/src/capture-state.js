import { mkdirSync, renameSync, rmSync, writeFileSync } from 'node:fs';
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

export function writeCaptureStateFiles({ globalDataDir, binding, enabled, reason, changedAt = new Date() }) {
  const body = `${JSON.stringify({
    schema: 'pulse.capture_state.v1',
    enabled,
    reason,
    changed_at: changedAt.toISOString(),
  }, null, 2)}\n`;
  for (const path of captureStatePaths(globalDataDir, binding)) {
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
