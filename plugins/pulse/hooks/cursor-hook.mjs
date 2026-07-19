import { spawn } from 'node:child_process';
import { existsSync } from 'node:fs';
import { resolveProductEnvironment } from '../runtime-locator.mjs';

const eventName = process.argv[2];
const productEnvironment = resolveProductEnvironment({
  host: 'cursor', integrity: eventName === 'SessionStart' ? 'refresh' : 'reuse',
});
const cliPath = productEnvironment.PULSE_RUNTIME_PATH;
if (!existsSync(cliPath)) {
  throw new Error('Pulse trusted runtime is missing; reconnect Pulse to Cursor.');
}

const child = spawn(process.execPath, [cliPath, 'cursor-hook', eventName], {
  stdio: ['inherit', 'inherit', 'inherit'],
  env: { ...process.env, ...productEnvironment },
});

child.on('error', (error) => {
  process.stderr.write(`[pulse] Cursor hook launcher failed: ${error.message}\n`);
  process.exitCode = 1;
});
child.on('exit', (code, signal) => {
  if (signal) process.kill(process.pid, signal);
  else process.exitCode = code ?? 1;
});
