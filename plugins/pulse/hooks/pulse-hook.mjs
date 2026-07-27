import { createHash } from 'node:crypto';
import { existsSync } from 'node:fs';
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
  // resolveProductEnvironment has already verified the exact activation,
  // runtime manifest, signed runtime tree, plugin tree, and daemon. Re-reading
  // one manifest here created a second platform-specific trust path without
  // adding authority.
  return {
    entrypointPath,
    hookDigest,
    productEnvironment,
    environmentPatch: { PULSE_PLUGIN_DATA: process.env.PLUGIN_DATA ?? '' },
  };
}

if (eventName === '--prewarm') {
  try {
    process.stdout.write(`${JSON.stringify(await prewarmHookWorker({
      host: 'codex', pluginRoot, workspacePath: process.cwd(), resolveEnvironment,
    }))}\n`);
  } catch (error) {
    const message = String(error?.message ?? '');
    const code = message.includes('product locator')
      ? 'codex_prewarm_locator_invalid'
      : message.includes('integration is disconnected')
        ? 'codex_prewarm_access_missing'
        : message.includes('activation is missing')
          ? 'codex_prewarm_activation_invalid'
          : message.includes('runtime and activation are out of sync')
            ? 'codex_prewarm_runtime_mismatch'
            : message.includes('installed plugin does not match')
              ? 'codex_prewarm_plugin_mismatch'
              : message.includes('hook runtime is missing')
                ? 'codex_prewarm_entrypoint_missing'
                : /^hook_worker_[a-z0-9_]+$/i.test(error?.code ?? '')
                  ? error.code
                  : 'codex_prewarm_unclassified';
    process.stderr.write(`${JSON.stringify({
      schema: 'pulse.hook_worker_prewarm_error.v1', content_free: true, code,
    })}\n`);
    process.exitCode = 1;
  }
} else {
  await runHookWorkerClient({
    host: 'codex',
    eventName,
    pluginRoot,
    pluginData: process.env.PLUGIN_DATA,
    resolveEnvironment,
  });
}
