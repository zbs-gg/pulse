import { spawn } from 'node:child_process';
import { existsSync } from 'node:fs';
import { resolveProductEnvironment } from '../runtime-locator.mjs';

const productEnvironment = resolveProductEnvironment({ host: 'codex' });
const cliPath = productEnvironment.PULSE_RUNTIME_PATH;
if (!existsSync(cliPath)) {
  throw new Error('Pulse trusted Codex runtime is missing; run `pulse connect codex` again.');
}
const child = spawn(process.execPath, [cliPath, 'codex-mcp'], {
  stdio: 'inherit', env: { ...process.env, ...productEnvironment },
});

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => child.kill(signal));
}
child.on('error', (error) => {
  process.stderr.write(`[pulse] MCP launcher failed: ${error.message}\n`);
  process.exitCode = 1;
});
child.on('exit', (code, signal) => {
  if (signal) process.kill(process.pid, signal);
  else process.exitCode = code ?? 1;
});
