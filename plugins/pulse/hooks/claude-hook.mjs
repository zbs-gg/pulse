import { existsSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { resolveProductEnvironment } from '../runtime-locator.mjs';

const eventName = process.argv[2];
const productEnvironment = resolveProductEnvironment({
  host: 'claude-code', integrity: eventName === 'SessionStart' ? 'refresh' : 'reuse',
});
const cliPath = productEnvironment.PULSE_RUNTIME_PATH;
if (!existsSync(cliPath)) {
  throw new Error('Pulse trusted runtime is missing; reconnect Pulse to Claude Code.');
}

Object.assign(process.env, productEnvironment, {
  PULSE_PLUGIN_DATA: process.env.CLAUDE_PLUGIN_DATA ?? '',
});
const entrypointPath = join(resolve(cliPath, '..', '..'), 'src', 'product-hook-entrypoint.js');
if (!existsSync(entrypointPath)) {
  throw new Error('Pulse trusted Claude Code hook runtime is missing; reconnect Pulse to Claude Code.');
}
const { runProductHookEntrypoint } = await import(pathToFileURL(entrypointPath).href);
await runProductHookEntrypoint('claude-code', eventName);
