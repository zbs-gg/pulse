import { createHash } from 'node:crypto';
import { existsSync, readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { prewarmHookWorker, runHookWorkerClient } from '../hook-worker-client.mjs';

const eventName = process.argv[2];
const hookRoot = dirname(fileURLToPath(import.meta.url));
const pluginRoot = resolve(hookRoot, '..');

async function resolveEnvironment() {
  const { resolveProductEnvironment } = await import('../runtime-locator.mjs');
  const productEnvironment = resolveProductEnvironment({
    edgeProfile: 'hook', host: 'codex',
    integrity: eventName === 'SessionStart' || eventName === '--prewarm' ? 'refresh' : 'reuse',
  });
  const cliPath = productEnvironment.PULSE_RUNTIME_PATH;
  const runtimeRoot = resolve(cliPath, '..', '..');
  const runtimeManifest = JSON.parse(readFileSync(join(runtimeRoot, 'runtime-manifest.json'), 'utf8'));
  if (runtimeManifest?.schema !== 'pulse.codex_runtime.v2' ||
      runtimeManifest.tree_digest !== productEnvironment.PULSE_RUNTIME_DIGEST) {
    throw new Error('Pulse trusted Codex runtime manifest is invalid; run `pulse connect codex` again.');
  }
  const hookDigest = createHash('sha256')
    .update('pulse-codex-hook-contract-v2\x00')
    .update(productEnvironment.PULSE_PLUGIN_TREE_DIGEST)
    .update('\x00')
    .update(productEnvironment.PULSE_RUNTIME_DIGEST)
    .digest('hex');
  const entrypointPath = join(runtimeRoot, 'src', 'product-hook-entrypoint.bundle.js');
  if (!existsSync(cliPath) || !existsSync(entrypointPath)) {
    throw new Error('Pulse trusted Codex hook runtime is missing; run `pulse connect codex` again.');
  }
  return {
    entrypointPath,
    hookDigest,
    productEnvironment,
    environmentPatch: { PULSE_PLUGIN_DATA: process.env.PLUGIN_DATA ?? '' },
  };
}

if (eventName === '--prewarm') {
  process.stdout.write(`${JSON.stringify(await prewarmHookWorker({
    host: 'codex', pluginRoot, workspacePath: process.cwd(), resolveEnvironment,
  }))}\n`);
} else {
  await runHookWorkerClient({
    host: 'codex',
    eventName,
    pluginRoot,
    pluginData: process.env.PLUGIN_DATA,
    resolveEnvironment,
  });
}
