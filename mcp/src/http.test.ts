import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { createServer, type IncomingMessage } from 'node:http';
import { test } from 'node:test';

import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';

const ENTRYPOINT = new URL('./index.ts', import.meta.url);
const DEFAULT_BEARER = 'dev-token';

function pkceChallenge(verifier: string): string {
  return createHash('sha256')
    .update(verifier)
    .digest('base64url');
}

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
          open_loops: ['Finish OAuth before public Claude connector.'],
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
    await client.close();
  } finally {
    await server.stop();
    await pulse.stop();
  }
});

test('http mode exposes OAuth protected-resource metadata for Claude custom connectors', async () => {
  const server = await startHttpServer({
    PULSE_REMOTE_BEARER: '',
    PULSE_REMOTE_PUBLIC_BASE_URL: 'https://pulse.example.com',
    PULSE_REMOTE_AUTH_ISSUER: 'https://auth.example.com',
  });
  try {
    const metadataURL = new URL('/.well-known/oauth-protected-resource/mcp', server.url);
    const metadata = await fetch(metadataURL);
    assert.equal(metadata.status, 200);
    assert.deepEqual(await metadata.json(), {
      resource: 'https://pulse.example.com/mcp',
      authorization_servers: ['https://auth.example.com'],
      bearer_methods_supported: ['header'],
      scopes_supported: ['pulse:read', 'pulse:write'],
    });

    const client = new Client({ name: 'pulse-mcp-test', version: '0.0.0' });
    const transport = new StreamableHTTPClientTransport(new URL(server.url));
    await client.connect(transport);
    const tools = await client.listTools();
    assert.ok(tools.tools.some((tool) => tool.name === 'pulse_graph_delta'));
    await client.close();
  } finally {
    await server.stop();
  }
});

test('http OAuth mode returns transport-level 401 for protected tool calls', async () => {
  const server = await startHttpServer({
    PULSE_REMOTE_BEARER: '',
    PULSE_REMOTE_PUBLIC_BASE_URL: 'https://pulse.example.com',
    PULSE_REMOTE_AUTH_ISSUER: 'https://auth.example.com',
  });
  try {
    const denied = await fetch(server.url, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Accept: 'application/json, text/event-stream',
      },
      body: JSON.stringify({
        jsonrpc: '2.0',
        id: 1,
        method: 'tools/call',
        params: {
          name: 'pulse_graph_delta',
          arguments: {},
        },
      }),
    });
    assert.equal(denied.status, 401);
    const challenge = denied.headers.get('WWW-Authenticate') ?? '';
    assert.match(challenge, /Bearer/);
    assert.match(challenge, /error="invalid_token"/);
    assert.match(
      challenge,
      /resource_metadata="https:\/\/pulse\.example\.com\/\.well-known\/oauth-protected-resource\/mcp"/,
    );
    assert.match(challenge, /scope="pulse:read pulse:write"/);
    assert.deepEqual(await denied.json(), {
      error: 'invalid_token',
      error_description: 'Authentication required for Pulse MCP tools',
    });
  } finally {
    await server.stop();
  }
});

test('OAuth mode refuses non-HTTPS public metadata URLs', async () => {
  const child = spawn(process.execPath, ['--import', 'tsx', ENTRYPOINT.pathname, '--http', '--port', '0'], {
    env: {
      ...process.env,
      PULSE_BASE_URL: 'http://127.0.0.1:1',
      PULSE_API_KEY: 'test-key',
      PULSE_REMOTE_BEARER: '',
      PULSE_REMOTE_PUBLIC_BASE_URL: 'http://pulse.example.com',
      PULSE_REMOTE_AUTH_ISSUER: 'https://auth.example.com',
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
  assert.match(output, /requires HTTPS/i);
});

test('OAuth mode does not trust arbitrary bearer headers without explicit proxy mode', async () => {
  const server = await startHttpServer({
    PULSE_REMOTE_BEARER: '',
    PULSE_REMOTE_PUBLIC_BASE_URL: 'https://pulse.example.com',
    PULSE_REMOTE_AUTH_ISSUER: 'https://auth.example.com',
    PULSE_REMOTE_TRUST_AUTH_HEADER: '1',
  });
  try {
    const denied = await fetch(server.url, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Accept: 'application/json, text/event-stream',
        Authorization: 'Bearer random-proxy-token',
      },
      body: JSON.stringify({
        jsonrpc: '2.0',
        id: 1,
        method: 'tools/call',
        params: {
          name: 'pulse_status',
          arguments: {},
        },
      }),
    });
    assert.equal(denied.status, 401);
  } finally {
    await server.stop();
  }
});

test('dev OAuth mode supports DCR, PKCE token exchange, and bearer tool calls', async () => {
  const pulse = await startFakePulseBackend();
  const server = await startHttpServer({
    PULSE_BASE_URL: pulse.url,
    PULSE_API_KEY: 'secret-key',
    PULSE_REMOTE_BEARER: '',
    PULSE_REMOTE_PUBLIC_BASE_URL: 'https://pulse.example.com',
    PULSE_REMOTE_AUTH_ISSUER: 'https://pulse.example.com',
    PULSE_REMOTE_OAUTH_DEV: '1',
  });
  try {
    const authMetadata = await fetch(new URL('/.well-known/oauth-authorization-server', server.url));
    assert.equal(authMetadata.status, 200);
    assert.deepEqual(await authMetadata.json(), {
      issuer: 'https://pulse.example.com',
      authorization_endpoint: 'https://pulse.example.com/authorize',
      token_endpoint: 'https://pulse.example.com/token',
      registration_endpoint: 'https://pulse.example.com/register',
      response_types_supported: ['code'],
      grant_types_supported: ['authorization_code', 'refresh_token'],
      token_endpoint_auth_methods_supported: ['none'],
      code_challenge_methods_supported: ['S256'],
      scopes_supported: ['pulse:read', 'pulse:write', 'offline_access'],
      client_id_metadata_document_supported: true,
    });

    const registration = await fetch(new URL('/register', server.url), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        client_name: 'Claude Pulse test',
        redirect_uris: ['https://claude.ai/api/mcp/auth_callback'],
      }),
    });
    assert.equal(registration.status, 201);
    const registered = await registration.json();
    assert.equal(registered.client_id, 'pulse-dev-client');
    assert.deepEqual(registered.redirect_uris, ['https://claude.ai/api/mcp/auth_callback']);

    const verifier = 'pulse-test-verifier-abcdefghijklmnopqrstuvwxyz';
    const authorizeURL = new URL('/authorize', server.url);
    authorizeURL.searchParams.set('response_type', 'code');
    authorizeURL.searchParams.set('client_id', registered.client_id);
    authorizeURL.searchParams.set('redirect_uri', 'https://claude.ai/api/mcp/auth_callback');
    authorizeURL.searchParams.set('code_challenge', pkceChallenge(verifier));
    authorizeURL.searchParams.set('code_challenge_method', 'S256');
    authorizeURL.searchParams.set('scope', 'pulse:read pulse:write');
    authorizeURL.searchParams.set('state', 'claude-state');

    const authorization = await fetch(authorizeURL, { redirect: 'manual' });
    assert.equal(authorization.status, 302);
    const callback = new URL(authorization.headers.get('Location') ?? '');
    assert.equal(callback.origin + callback.pathname, 'https://claude.ai/api/mcp/auth_callback');
    assert.equal(callback.searchParams.get('state'), 'claude-state');
    const code = callback.searchParams.get('code');
    assert.ok(code);

    const token = await fetch(new URL('/token', server.url), {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({
        grant_type: 'authorization_code',
        client_id: registered.client_id,
        redirect_uri: 'https://claude.ai/api/mcp/auth_callback',
        code,
        code_verifier: verifier,
      }),
    });
    assert.equal(token.status, 200);
    const tokenBody = await token.json();
    assert.equal(tokenBody.token_type, 'Bearer');
    assert.equal(tokenBody.scope, 'pulse:read pulse:write');
    assert.ok(tokenBody.access_token);
    assert.ok(tokenBody.refresh_token);

    const client = new Client({ name: 'pulse-mcp-test', version: '0.0.0' });
    const transport = new StreamableHTTPClientTransport(new URL(server.url), {
      requestInit: {
        headers: {
          Authorization: `Bearer ${tokenBody.access_token}`,
        },
      },
    });
    await client.connect(transport);
    const status = await client.callTool({
      name: 'pulse_status',
      arguments: {},
    });
    assert.equal(status.content[0]?.type, 'text');
    assert.equal(JSON.parse(status.content[0].text).backend_llm_enabled, false);
    await client.close();
  } finally {
    await server.stop();
    await pulse.stop();
  }
});

test('http mode refuses public authless bind by default', async () => {
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
  assert.match(output, /refusing authless public HTTP MCP bind/i);
});
