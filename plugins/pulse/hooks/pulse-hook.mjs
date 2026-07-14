import { createHash } from 'node:crypto';
import { existsSync, readFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawn } from 'node:child_process';

const eventName = process.argv[2];
const hookRoot = dirname(fileURLToPath(import.meta.url));
const pluginRoot = resolve(hookRoot, '..');
const dataDir = process.env.PULSE_DATA_DIR || join(homedir(), '.pulse');
const runtimeManifest = JSON.parse(readFileSync(join(dataDir, 'runtime', 'codex', 'current', 'runtime-manifest.json'), 'utf8'));
if (runtimeManifest?.schema !== 'pulse.codex_runtime.v1' || !/^[a-f0-9]{64}$/.test(runtimeManifest.tree_digest ?? '')) {
  throw new Error('Pulse trusted Codex runtime manifest is invalid; run `pulse connect codex` again.');
}
const digest = createHash('sha256');
for (const relative of ['.mcp.json', 'hooks/hooks.json', 'hooks/pulse-hook.mjs', 'mcp/server.mjs']) {
  digest.update(relative);
  digest.update('\x00');
  digest.update(readFileSync(join(pluginRoot, relative)));
  digest.update('\x00');
}
digest.update('runtime-tree-digest\x00');
digest.update(runtimeManifest.tree_digest);
const hooksDigest = digest.digest('hex');
const cliPath = join(dataDir, 'runtime', 'codex', 'current', 'src', 'cli.js');
if (!existsSync(cliPath)) {
  throw new Error('Pulse trusted Codex runtime is missing; run `pulse connect codex` again.');
}

const child = spawn(process.execPath, [cliPath, 'codex-hook', eventName], {
  stdio: ['inherit', 'inherit', 'inherit'],
  env: {
    ...process.env,
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
