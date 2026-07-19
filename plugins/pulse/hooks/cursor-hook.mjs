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
    edgeProfile: 'hook', host: 'cursor', integrity: eventName === 'sessionStart' ? 'refresh' : 'reuse',
  });
  const cliPath = productEnvironment.PULSE_RUNTIME_PATH;
  const entrypointPath = join(resolve(cliPath, '..', '..'), 'src', 'product-hook-entrypoint.bundle.js');
  if (!existsSync(cliPath) || !existsSync(entrypointPath)) {
    throw new Error('Pulse trusted Cursor hook runtime is missing; reconnect Pulse to Cursor.');
  }
  const hookDigest = createHash('sha256')
    .update('pulse-product-hook-worker-v1\x00cursor\x00')
    .update(productEnvironment.PULSE_PLUGIN_TREE_DIGEST)
    .update('\x00')
    .update(productEnvironment.PULSE_RUNTIME_DIGEST)
    .digest('hex');
  return { entrypointPath, hookDigest, productEnvironment };
}

await runHookWorkerClient({
  host: 'cursor',
  eventName,
  pluginRoot,
  pluginData: process.env.CURSOR_PLUGIN_DATA,
  resolveEnvironment,
});
