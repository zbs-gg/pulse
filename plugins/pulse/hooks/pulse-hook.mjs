import { createHash } from 'node:crypto';
import { existsSync, readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawn } from 'node:child_process';
import { resolveProductEnvironment } from '../runtime-locator.mjs';

const eventName = process.argv[2];
const hookRoot = dirname(fileURLToPath(import.meta.url));
const pluginRoot = resolve(hookRoot, '..');
const productEnvironment = resolveProductEnvironment({
  host: 'codex', integrity: eventName === 'SessionStart' ? 'refresh' : 'reuse',
});
const cliPath = productEnvironment.PULSE_RUNTIME_PATH;
const runtimeRoot = resolve(cliPath, '..', '..');
const runtimeManifest = JSON.parse(readFileSync(join(runtimeRoot, 'runtime-manifest.json'), 'utf8'));
if (runtimeManifest?.schema !== 'pulse.codex_runtime.v2' ||
		runtimeManifest.tree_digest !== productEnvironment.PULSE_RUNTIME_DIGEST) {
  throw new Error('Pulse trusted Codex runtime manifest is invalid; run `pulse connect codex` again.');
}
const digest = createHash('sha256');
for (const relative of [
	'.codex-plugin/plugin.json', '.mcp.json', 'runtime-locator.mjs', 'windows-platform-adapter.mjs',
	'hooks/hooks.json', 'hooks/pulse-hook.mjs', 'mcp/server.mjs',
]) {
  digest.update(relative);
  digest.update('\x00');
  digest.update(readFileSync(join(pluginRoot, relative)));
  digest.update('\x00');
}
digest.update('runtime-tree-digest\x00');
digest.update(productEnvironment.PULSE_RUNTIME_DIGEST);
const hooksDigest = digest.digest('hex');
if (!existsSync(cliPath)) {
  throw new Error('Pulse trusted Codex runtime is missing; run `pulse connect codex` again.');
}

const child = spawn(process.execPath, [cliPath, 'codex-hook', eventName], {
  stdio: ['inherit', 'inherit', 'inherit'],
  env: {
    ...process.env,
    ...productEnvironment,
    PULSE_PLUGIN_DATA: process.env.PLUGIN_DATA ?? '',
    PULSE_HOOK_BUNDLE_DIGEST: hooksDigest,
  },
});

child.on('error', (error) => {
  process.stderr.write(`[pulse] hook launcher failed: ${error.message}\n`);
  process.exitCode = 1;
});

child.on('exit', (code, signal) => {
  if (signal) {
    process.kill(process.pid, signal);
  } else {
    process.exitCode = code ?? 1;
  }
});
