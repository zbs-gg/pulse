import { existsSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { enableProductCompileCache, resolveProductEnvironment } from '../runtime-locator.mjs';

const eventName = process.argv[2];
const productEnvironment = resolveProductEnvironment({
  edgeProfile: 'hook', host: 'cursor', integrity: eventName === 'SessionStart' ? 'refresh' : 'reuse',
});
const cliPath = productEnvironment.PULSE_RUNTIME_PATH;
if (!existsSync(cliPath)) {
  throw new Error('Pulse trusted runtime is missing; reconnect Pulse to Cursor.');
}

Object.assign(process.env, productEnvironment);
enableProductCompileCache(productEnvironment);
const entrypointPath = join(resolve(cliPath, '..', '..'), 'src', 'product-hook-entrypoint.bundle.js');
if (!existsSync(entrypointPath)) {
  throw new Error('Pulse trusted Cursor hook runtime is missing; reconnect Pulse to Cursor.');
}
const { runProductHookEntrypoint } = await import(pathToFileURL(entrypointPath).href);
await runProductHookEntrypoint('cursor', eventName);
