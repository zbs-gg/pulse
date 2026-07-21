import { createHash } from 'node:crypto';
import { existsSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { runHookWorkerClient } from '../hook-worker-client.mjs';

const eventName = process.argv[2];
const hookRoot = dirname(fileURLToPath(import.meta.url));
const pluginRoot = resolve(hookRoot, '..');

async function resolveEnvironment() {
  const { resolveProductEnvironment } = await import('../runtime-locator.mjs');
  const productEnvironment = resolveProductEnvironment({
    edgeProfile: 'hook', host: 'claude-code', integrity: eventName === 'SessionStart' ? 'refresh' : 'reuse',
  });
  const cliPath = productEnvironment.PULSE_RUNTIME_PATH;
  const entrypointPath = join(resolve(cliPath, '..', '..'), 'src', 'product-hook-entrypoint.bundle.js');
  if (!existsSync(cliPath) || !existsSync(entrypointPath)) {
    throw new Error('Pulse trusted Claude Code hook runtime is missing; reconnect Pulse to Claude Code.');
  }
  const hookDigest = createHash('sha256')
    .update('pulse-product-hook-worker-v1\x00claude-code\x00')
    .update(productEnvironment.PULSE_PLUGIN_TREE_DIGEST)
    .update('\x00')
    .update(productEnvironment.PULSE_RUNTIME_DIGEST)
    .digest('hex');
  return {
    entrypointPath,
    hookDigest,
    productEnvironment,
    environmentPatch: { PULSE_PLUGIN_DATA: process.env.CLAUDE_PLUGIN_DATA ?? '' },
  };
}

await runHookWorkerClient({
  host: 'claude-code',
  eventName,
  pluginRoot,
  pluginData: process.env.CLAUDE_PLUGIN_DATA,
  resolveEnvironment,
});
