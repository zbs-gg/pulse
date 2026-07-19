import { createHash } from 'node:crypto';
import { existsSync, readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { enableProductCompileCache, resolveProductEnvironment } from '../runtime-locator.mjs';

const eventName = process.argv[2];
const hookRoot = dirname(fileURLToPath(import.meta.url));
const pluginRoot = resolve(hookRoot, '..');
const productEnvironment = resolveProductEnvironment({
  edgeProfile: 'hook', host: 'codex', integrity: eventName === 'SessionStart' ? 'refresh' : 'reuse',
});
const cliPath = productEnvironment.PULSE_RUNTIME_PATH;
const runtimeRoot = resolve(cliPath, '..', '..');
const runtimeManifest = JSON.parse(readFileSync(join(runtimeRoot, 'runtime-manifest.json'), 'utf8'));
if (runtimeManifest?.schema !== 'pulse.codex_runtime.v2' ||
		runtimeManifest.tree_digest !== productEnvironment.PULSE_RUNTIME_DIGEST) {
  throw new Error('Pulse trusted Codex runtime manifest is invalid; run `pulse connect codex` again.');
}
const hooksDigest = createHash('sha256')
  .update('pulse-codex-hook-contract-v2\x00')
  .update(productEnvironment.PULSE_PLUGIN_TREE_DIGEST)
  .update('\x00')
  .update(productEnvironment.PULSE_RUNTIME_DIGEST)
  .digest('hex');
if (!existsSync(cliPath)) {
  throw new Error('Pulse trusted Codex runtime is missing; run `pulse connect codex` again.');
}

Object.assign(process.env, productEnvironment, {
  PULSE_PLUGIN_DATA: process.env.PLUGIN_DATA ?? '',
  PULSE_HOOK_BUNDLE_DIGEST: hooksDigest,
});
enableProductCompileCache(productEnvironment);
const entrypointPath = join(runtimeRoot, 'src', 'product-hook-entrypoint.bundle.js');
if (!existsSync(entrypointPath)) {
  throw new Error('Pulse trusted Codex hook runtime is missing; run `pulse connect codex` again.');
}
const { runProductHookEntrypoint } = await import(pathToFileURL(entrypointPath).href);
await runProductHookEntrypoint('codex', eventName);
