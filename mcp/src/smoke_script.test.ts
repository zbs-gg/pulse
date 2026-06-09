import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { createServer, type IncomingMessage } from 'node:http';
import { test } from 'node:test';

const ENTRYPOINT = new URL('./index.ts', import.meta.url);
const SMOKE_SCRIPT = new URL('../scripts/claude-connector-smoke.mjs', import.meta.url);

async function readJson(req: IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) {
    chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk));
  }
  if (chunks.length === 0) {
    return undefined;
  }
  return JSON.parse(Buffer.concat(chunks).toString('utf8'));
}

async function startFakePulseBackend() {
  let checkpointSummary = '';
  const server = createServer(async (req, res) => {
    if (req.method === 'GET' && req.url === '/memory/status') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        billing_mode: 'host-extracted',
        backend_llm_enabled: false,
        raw_capture_enabled: false,
        storage_path: '/home/example/.pulse/pulse.db',
      }));
      return;
    }
    if (req.method === 'POST' && req.url === '/graph/delta') {
      const body = await readJson(req);
      if (body && typeof body === 'object' && 'continuity' in body) {
        const continuity = (body as Record<string, unknown>).continuity;
        if (continuity && typeof continuity === 'object') {
          checkpointSummary = String((continuity as Record<string, unknown>).summary ?? '');
        }
      }
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ ok: true, checkpoint_saved: true }));
      return;
    }
    if (req.method === 'POST' && req.url === '/continuity/resume') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        ok: true,
        resume_markdown: `# Pulse Resume\n\n## Where we left off\n${checkpointSummary}\n\n## Open loops\nTry the same URL in Claude custom connector UI.`,
        token_estimate: 42,
      }));
      return;
    }
    res.writeHead(404, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ error: 'not found' }));
  });
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => resolve());
  });
  const address = server.address();
  assert.ok(address && typeof address === 'object');
  return {
    url: `http://127.0.0.1:${address.port}`,
    stop: async () => {
      await new Promise<void>((resolve) => server.close(() => resolve()));
    },
  };
}

async function startHttpServer(pulseBaseURL: string) {
  const child = spawn(process.execPath, ['--import', 'tsx', ENTRYPOINT.pathname, '--http', '--port', '0'], {
    env: {
      ...process.env,
      PULSE_BASE_URL: pulseBaseURL,
      PULSE_API_KEY: 'test-key',
      PULSE_REMOTE_BEARER: '',
      PULSE_REMOTE_PUBLIC_BASE_URL: 'https://pulse.example.com',
      PULSE_REMOTE_AUTH_ISSUER: 'https://pulse.example.com',
      PULSE_REMOTE_OAUTH_DEV: '1',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let output = '';
  child.stderr.on('data', (chunk) => {
    output += chunk.toString();
  });
  child.stdout.on('data', (chunk) => {
    output += chunk.toString();
  });
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    const match = output.match(/Streamable HTTP listening on (http:\/\/127\.0\.0\.1:\d+\/mcp)/);
    if (match) {
      return {
        url: match[1],
        stop: async () => {
          child.kill('SIGTERM');
          await once(child, 'exit');
        },
      };
    }
    if (child.exitCode !== null) {
      throw new Error(`pulse-mcp exited early (${child.exitCode}): ${output}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  child.kill('SIGTERM');
  throw new Error(`pulse-mcp did not print listening URL:\n${output}`);
}

test('Claude connector smoke script verifies OAuth, MCP, graph delta, and resume', async () => {
  const pulse = await startFakePulseBackend();
  const mcp = await startHttpServer(pulse.url);
  try {
    const child = spawn(process.execPath, [
      SMOKE_SCRIPT.pathname,
      '--base',
      mcp.url.replace(/\/mcp$/, ''),
      '--thread',
      'pulse-script-smoke',
      '--json',
    ], {
      cwd: new URL('..', import.meta.url).pathname,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (chunk) => {
      stdout += chunk.toString();
    });
    child.stderr.on('data', (chunk) => {
      stderr += chunk.toString();
    });
    const [code] = await once(child, 'exit');
    assert.equal(code, 0, stderr);
    const result = JSON.parse(stdout);
    assert.equal(result.ok, true);
    assert.equal(result.backendLLMEnabled, false);
    assert.equal(result.storagePath, '<local>');
    assert.equal(result.checkpointSaved, true);
    assert.equal(result.resumeHasSmoke, true);
    assert.ok(result.tools >= 8);
  } finally {
    await mcp.stop();
    await pulse.stop();
  }
});
