import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { createServer, type IncomingMessage } from 'node:http';
import { test } from 'node:test';

import { Client, StreamableHTTPClientTransport } from '@modelcontextprotocol/client';

const ENTRYPOINT = new URL('./index.ts', import.meta.url);
const DEFAULT_BEARER = 'dev-token';

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
  const requests: Array<{
    method: string | undefined;
    url: string | undefined;
    pulseKey: string | undefined;
    idempotencyKey?: string;
    body?: unknown;
  }> = [];
  const server = createServer(async (req, res) => {
    if (req.method === 'GET' && req.url === '/memory/status') {
      requests.push({
        method: req.method,
        url: req.url,
        pulseKey: req.headers['x-pulse-key']?.toString(),
      });
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
      requests.push({
        method: req.method,
        url: req.url,
        pulseKey: req.headers['x-pulse-key']?.toString(),
        idempotencyKey: req.headers['idempotency-key']?.toString(),
        body,
      });
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        ok: true,
        checkpoint_saved: Boolean(
          body && typeof body === 'object' && 'continuity' in body,
        ),
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
    requests,
    url: `http://127.0.0.1:${address.port}`,
    stop: async () => {
      await new Promise<void>((resolve) => server.close(() => resolve()));
    },
  };
}

async function startHttpServer(env: Record<string, string> = {}) {
  const child = spawn(process.execPath, ['--import', 'tsx', ENTRYPOINT.pathname, '--http', '--port', '0'], {
    env: {
      ...process.env,
      PULSE_BASE_URL: 'http://127.0.0.1:1',
      PULSE_API_KEY: 'test-key',
      PULSE_REMOTE_BEARER: DEFAULT_BEARER,
      ...env,
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let stderr = '';
  child.stderr.on('data', (chunk) => {
    stderr += chunk.toString();
  });
  child.stdout.on('data', (chunk) => {
    stderr += chunk.toString();
  });
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    const match = stderr.match(/Streamable HTTP listening on (http:\/\/127\.0\.0\.1:\d+\/mcp)/);
    if (match) {
      return {
        child,
        url: match[1],
        stop: async () => {
          child.kill('SIGTERM');
          await once(child, 'exit');
        },
      };
    }
    if (child.exitCode !== null) {
      throw new Error(`pulse-mcp exited early (${child.exitCode}): ${stderr}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  child.kill('SIGTERM');
  throw new Error(`pulse-mcp did not print listening URL:\n${stderr}`);
}

test('http mode exposes Pulse tools over Streamable HTTP', async () => {
  const server = await startHttpServer();
  try {
    const client = new Client({ name: 'pulse-mcp-test', version: '0.0.0' });
    const transport = new StreamableHTTPClientTransport(new URL(server.url), {
      requestInit: {
        headers: {
          Authorization: `Bearer ${DEFAULT_BEARER}`,
        },
      },
    });
    await client.connect(transport);
    const tools = await client.listTools();
    const names = tools.tools.map((tool) => tool.name);
    assert.ok(names.includes('pulse_graph_delta'), names.join(', '));
    assert.ok(names.includes('pulse_resume'), names.join(', '));
    await client.close();
  } finally {
    await server.stop();
  }
});

test('http mode speaks the stateless 2026-07-28 protocol when the client requests it', async () => {
  const server = await startHttpServer();
  try {
    const client = new Client(
      { name: 'pulse-modern-test', version: '0.0.0' },
      { versionNegotiation: { mode: { pin: '2026-07-28' } } },
    );
    const transport = new StreamableHTTPClientTransport(new URL(server.url), {
      requestInit: { headers: { Authorization: `Bearer ${DEFAULT_BEARER}` } },
    });
    await client.connect(transport);
    assert.equal(client.getProtocolEra(), 'modern');
    assert.equal(client.getNegotiatedProtocolVersion(), '2026-07-28');
    const tools = await client.listTools();
    assert.ok(tools.tools.some((tool) => tool.name === 'pulse_resume'));
    await client.close();
  } finally {
    await server.stop();
  }
});

test('http mode refuses to start without a bearer by default', async () => {
  const child = spawn(process.execPath, ['--import', 'tsx', ENTRYPOINT.pathname, '--http', '--port', '0'], {
    env: {
      ...process.env,
      PULSE_BASE_URL: 'http://127.0.0.1:1',
      PULSE_API_KEY: 'test-key',
      PULSE_REMOTE_BEARER: '',
      PULSE_REMOTE_ALLOW_UNAUTHENTICATED: '',
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
  const [code] = await once(child, 'exit');
  assert.notEqual(code, 0);
  assert.match(output, /requires PULSE_REMOTE_BEARER/);
});

test('http mode can require bearer auth before MCP handshake', async () => {
  const server = await startHttpServer({ PULSE_REMOTE_BEARER: 'dev-token' });
  try {
    const preflight = await fetch(server.url, {
      method: 'OPTIONS',
      headers: { Origin: 'https://evil.example' },
    });
    assert.equal(preflight.status, 204);
    assert.equal(preflight.headers.get('Access-Control-Allow-Origin'), 'http://127.0.0.1');
    assert.match(
      preflight.headers.get('Access-Control-Allow-Headers') ?? '',
      /MCP-Protocol-Version/,
    );

    const denied = await fetch(server.url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/list' }),
    });
    assert.equal(denied.status, 401);
    assert.match(denied.headers.get('WWW-Authenticate') ?? '', /Bearer/);

    const client = new Client({ name: 'pulse-mcp-test', version: '0.0.0' });
    const transport = new StreamableHTTPClientTransport(new URL(server.url), {
      requestInit: {
        headers: {
          Authorization: 'Bearer dev-token',
        },
      },
    });
    await client.connect(transport);
    const tools = await client.listTools();
    assert.ok(tools.tools.some((tool) => tool.name === 'pulse_graph_delta'));
    await client.close();
  } finally {
    await server.stop();
  }
});

test('http mode can call Pulse backend tools over Streamable HTTP', async () => {
  const pulse = await startFakePulseBackend();
  const server = await startHttpServer({
    PULSE_BASE_URL: pulse.url,
    PULSE_API_KEY: 'secret-key',
  });
  try {
    const client = new Client({ name: 'pulse-mcp-test', version: '0.0.0' });
    const transport = new StreamableHTTPClientTransport(new URL(server.url), {
      requestInit: {
        headers: {
          Authorization: `Bearer ${DEFAULT_BEARER}`,
        },
      },
    });
    await client.connect(transport);

    const status = await client.callTool({
      name: 'pulse_status',
      arguments: {},
    });
    assert.equal(status.content[0]?.type, 'text');
    const statusBody = JSON.parse(status.content[0].text);
    assert.equal(statusBody.backend_llm_enabled, false);
    assert.equal(statusBody.storage, 'local_sqlite');
    assert.equal(statusBody.storage_path, '<local>');

    const graph = await client.callTool({
      name: 'pulse_graph_delta',
      arguments: {
        schema: 'pulse.semantic_delta.v1',
        source: {
          host: 'claude',
          conversation_scope: 'current_turn',
          timestamp: '2026-06-02T16:00:00Z',
          thread_id: 'pulse-distribution',
        },
        continuity: {
          summary: 'Claude saved a continuity-only checkpoint.',
          decisions: ['Allow continuity-only graph deltas.'],
          open_loops: ['Finish the local connector verification.'],
        },
        raw_input_included: false,
      },
    });
    assert.equal(graph.content[0]?.type, 'text');
    assert.deepEqual(JSON.parse(graph.content[0].text), {
      ok: true,
      checkpoint_saved: true,
    });

    assert.deepEqual(
      pulse.requests.map((request) => [request.method, request.url, request.pulseKey]),
      [
        ['GET', '/memory/status', 'secret-key'],
        ['POST', '/graph/delta', 'secret-key'],
      ],
    );
    assert.ok(
      pulse.requests.some((request) => request.url === '/graph/delta' && request.body),
    );
    assert.match(
      pulse.requests.find((request) => request.url === '/graph/delta')?.idempotencyKey ?? '',
      /^mcp_[a-f0-9]{64}$/,
    );
    await client.close();
  } finally {
    await server.stop();
    await pulse.stop();
  }
});

test('http mode refuses every non-loopback bind', async () => {
  const child = spawn(process.execPath, ['--import', 'tsx', ENTRYPOINT.pathname, '--http', '--host', '0.0.0.0', '--port', '0'], {
    env: {
      ...process.env,
      PULSE_BASE_URL: 'http://127.0.0.1:1',
      PULSE_API_KEY: 'test-key',
      PULSE_REMOTE_BEARER: '',
      PULSE_ALLOW_AUTHLESS_PUBLIC: '',
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

  const [code] = await once(child, 'exit');
  assert.notEqual(code, 0);
	assert.match(output, /development HTTP is local-only/i);
});
