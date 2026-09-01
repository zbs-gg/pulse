import { spawn } from 'node:child_process';

import { tool } from '@opencode-ai/plugin';

import { createPulseOpenCodeHooks } from './opencode-core.mjs';
import { resolveProductEnvironment } from './runtime-locator.mjs';

function runBridge(runtimePath, environment, directory, action, input, {
  signal, timeoutMs = 4_000,
} = {}) {
  return new Promise((resolve, reject) => {
    // OpenCode 1.18 ships as a native executable, so process.execPath points
    // back to `opencode`, not to a JavaScript runtime. Pulse itself is
    // installed through Node and invokes the signed CLI bridge with that
    // runtime explicitly.
    const child = spawn('node', [runtimePath, 'opencode-bridge', action], {
      cwd: directory,
      env: { ...process.env, ...environment },
      stdio: ['pipe', 'pipe', 'ignore'],
      windowsHide: true,
    });
    const chunks = [];
    let bytes = 0;
    let settled = false;
    const finish = (error, value) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      signal?.removeEventListener('abort', abort);
      if (error) reject(error); else resolve(value);
    };
    const abort = () => {
      child.kill('SIGTERM');
      finish(new Error('opencode_bridge_aborted'));
    };
    const timer = setTimeout(() => {
      child.kill('SIGTERM');
      finish(new Error('opencode_bridge_timeout'));
    }, timeoutMs);
    signal?.addEventListener('abort', abort, { once: true });
    if (signal?.aborted) return abort();
    child.stdout.on('data', (chunk) => {
      bytes += chunk.length;
      if (bytes > 64 * 1024) return abort();
      chunks.push(chunk);
    });
    child.once('error', (error) => finish(error));
    child.stdin.once('error', (error) => finish(error));
    child.once('close', (code) => {
      if (code !== 0) return finish(new Error('opencode_bridge_failed'));
      try { finish(undefined, JSON.parse(Buffer.concat(chunks).toString('utf8'))); }
      catch { finish(new Error('opencode_bridge_response_invalid')); }
    });
    child.stdin.end(`${JSON.stringify(input)}\n`);
  });
}

export const Pulse = async ({ directory, client }) => {
  let environment;
  try {
    environment = resolveProductEnvironment({ cwd: directory, host: 'opencode', integrity: 'refresh' });
  } catch {
    // The global loader is intentionally inert outside signed Pulse projects.
    return {};
  }
  const runtimePath = environment.PULSE_RUNTIME_PATH;
  const bridge = (action, input, options) => runBridge(
    runtimePath, environment, directory, action, input, options,
  );
  return createPulseOpenCodeHooks({ directory, client, tool, bridge });
};
