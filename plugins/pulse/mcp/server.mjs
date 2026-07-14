import { spawn } from 'node:child_process';
import { existsSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';

const dataDir = process.env.PULSE_DATA_DIR || join(homedir(), '.pulse');
const cliPath = join(dataDir, 'runtime', 'codex', 'current', 'src', 'cli.js');
if (!existsSync(cliPath)) {
  throw new Error('Pulse trusted Codex runtime is missing; run `pulse connect codex` again.');
}
const child = spawn(process.execPath, [cliPath, 'codex-mcp'], { stdio: 'inherit', env: process.env });

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
