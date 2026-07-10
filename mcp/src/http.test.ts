import assert from 'node:assert/strict';
import { createHash, createPublicKey, generateKeyPairSync } from 'node:crypto';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { createServer, type IncomingMessage } from 'node:http';
import { createConnection } from 'node:net';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { after, test } from 'node:test';

import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';
import { exportJWK, SignJWT } from 'jose';

import { OAuthResourceVerifier } from './oauth-resource.js';
import {
  loadPrincipalSigner,
  TeamPrincipalClient,
  TeamRequestSecurity,
} from './principal-context.js';
import { drainSecurityReporterForShutdown, terminateStartedResponse } from './index.js';

const ENTRYPOINT = new URL('./index.ts', import.meta.url);
const DEFAULT_BEARER = 'dev-token';
const DIRECT_SPAWN_DATA_DIR = mkdtempSync(join(tmpdir(), 'pulse-http-direct-'));
after(() => rmSync(DIRECT_SPAWN_DATA_DIR, { recursive: true, force: true }));

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
  const dataDir = mkdtempSync(join(tmpdir(), 'pulse-http-test-'));
  const child = spawn(process.execPath, ['--import', 'tsx', ENTRYPOINT.pathname, '--http', '--port', '0'], {
    env: {
      ...process.env,
      PULSE_BASE_URL: 'http://127.0.0.1:1',
      PULSE_API_KEY: 'test-key',
      PULSE_REMOTE_BEARER: DEFAULT_BEARER,
      ...env,
      // Tests must never read or persist development OAuth tokens in ~/.pulse.
      PULSE_DATA_DIR: dataDir,
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
          if (child.exitCode === null) {
            child.kill('SIGTERM');
            await once(child, 'exit');
          }
          rmSync(dataDir, { recursive: true, force: true });
        },
      };
    }
    if (child.exitCode !== null) {
      rmSync(dataDir, { recursive: true, force: true });
      throw new Error(`pulse-mcp exited early (${child.exitCode}): ${stderr}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  child.kill('SIGTERM');
  await once(child, 'exit');
  rmSync(dataDir, { recursive: true, force: true });
  throw new Error(`pulse-mcp did not print listening URL:\n${stderr}`);
}

async function startTeamHttpServer() {
  const dir = mkdtempSync(join(tmpdir(), 'pulse-u3-http-'));
  const { privateKey } = generateKeyPairSync('ed25519');
  const privateKeyFile = join(dir, 'principal.pk8.pem');
  writeFileSync(privateKeyFile, privateKey.export({ format: 'pem', type: 'pkcs8' }), { mode: 0o600 });
  const publicJWK = createPublicKey(privateKey).export({ format: 'jwk' });
  assert.equal(typeof publicJWK.x, 'string');
  const keyringFile = join(dir, 'principal-keyring.json');
  writeFileSync(keyringFile, JSON.stringify({
    active: { kid: 'test-gateway-key', public_key: publicJWK.x }, previous: [],
  }), { mode: 0o600 });
  const child = spawn(process.execPath, ['--import', 'tsx', ENTRYPOINT.pathname, '--http', '--port', '0'], {
    env: {
      ...process.env,
      PULSE_RUNTIME_MODE: 'team-remote',
      PULSE_MCP_MODE: 'daemon',
      PULSE_BASE_URL: 'http://127.0.0.1:1',
      PULSE_API_KEY: 'synthetic-ipc-key',
      PULSE_DATA_DIR: dir,
      PULSE_REMOTE_PUBLIC_BASE_URL: 'https://pulse.example.com',
      PULSE_REMOTE_AUTH_ISSUER: 'https://auth.example.com',
      PULSE_REMOTE_ALLOWED_ORIGINS: 'https://allowed.example',
      PULSE_REMOTE_BEARER: '',
      PULSE_REMOTE_OAUTH_DEV: '',
      PULSE_REMOTE_AUTH_PROXY_MODE: '',
      PULSE_REMOTE_TRUST_AUTH_HEADER: '',
      PULSE_REMOTE_ALLOW_UNAUTHENTICATED: '',
      PULSE_TEAM_REMOTE_ACTIVATED: '',
      PULSE_TEAM_PRINCIPAL_SIGNING_KEY_FILE: privateKeyFile,
      PULSE_TEAM_PRINCIPAL_SIGNING_KID: 'test-gateway-key',
      PULSE_TEAM_PRINCIPAL_VERIFY_KEYRING_FILE: keyringFile,
      PULSE_TEAM_EXPECTED_STORE_ID: 'store_test',
      PULSE_TEAM_EXPECTED_TEAM_ID: 'team_test',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let output = '';
  child.stderr.on('data', (chunk) => { output += chunk.toString(); });
  child.stdout.on('data', (chunk) => { output += chunk.toString(); });
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    const match = output.match(/Streamable HTTP listening on (http:\/\/127\.0\.0\.1:\d+\/mcp)/);
    if (match) {
      return {
        url: match[1],
        stop: async () => {
          if (child.exitCode === null) {
            child.kill('SIGTERM');
            await once(child, 'exit');
          }
          rmSync(dir, { recursive: true, force: true });
        },
      };
    }
    if (child.exitCode !== null) {
      rmSync(dir, { recursive: true, force: true });
      throw new Error(`team pulse-mcp exited early (${child.exitCode}): ${output}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  child.kill('SIGTERM');
  rmSync(dir, { recursive: true, force: true });
  throw new Error(`team pulse-mcp did not start: ${output}`);
}

async function rawHttpRequest(serverURL: string, requestTarget: string, host: string): Promise<string> {
  const url = new URL(serverURL);
  return new Promise((resolve, reject) => {
    const socket = createConnection({ host: url.hostname, port: Number(url.port) });
    const chunks: Buffer[] = [];
    socket.once('error', reject);
    socket.on('data', (chunk) => chunks.push(Buffer.from(chunk)));
    socket.on('end', () => resolve(Buffer.concat(chunks).toString('latin1')));
    socket.once('connect', () => {
      socket.end(`GET ${requestTarget} HTTP/1.1\r\nHost: ${host}\r\nConnection: close\r\n\r\n`);
    });
  });
}

test('team HTTP keeps public surface minimal and authenticates before parsing every protected method', async () => {
  const server = await startTeamHttpServer();
  try {
    const health = await fetch(new URL('/health', server.url));
    assert.deepEqual(await health.json(), { ok: true });
    const metadata = await fetch(new URL('/.well-known/oauth-protected-resource/mcp', server.url));
    assert.deepEqual(await metadata.json(), {
      resource: 'https://pulse.example.com/mcp',
      authorization_servers: ['https://auth.example.com'],
      bearer_methods_supported: ['header'],
      scopes_supported: ['pulse:connect', 'pulse:status', 'pulse:read', 'pulse:write', 'pulse:audit', 'pulse:delete'],
    });

    const evilPreflight = await fetch(server.url, { method: 'OPTIONS', headers: { Origin: 'https://evil.example' } });
    assert.equal(evilPreflight.status, 403);
    const allowedPreflight = await fetch(server.url, { method: 'OPTIONS', headers: { Origin: 'https://allowed.example' } });
    assert.equal(allowedPreflight.status, 204);
    assert.equal(allowedPreflight.headers.get('access-control-allow-origin'), 'https://allowed.example');

    for (const method of ['POST', 'GET', 'DELETE']) {
      const denied = await fetch(server.url, {
        method,
        headers: {
          Origin: 'https://allowed.example',
          'Content-Type': 'application/json',
        },
        body: method === 'POST' ? '{ definitely-not-json' : undefined,
      });
      assert.equal(denied.status, 401, method);
      assert.deepEqual(await denied.json(), { error: 'invalid_token' });
      assert.equal(denied.headers.get('access-control-allow-origin'), 'https://allowed.example');
      assert.match(denied.headers.get('www-authenticate') ?? '', /resource_metadata=/);
    }
  } finally {
    await server.stop();
  }
});

test('team listener contains malformed Host and request-target failures', async () => {
  const server = await startTeamHttpServer();
  try {
    const absoluteTarget = await rawHttpRequest(server.url, 'http://evil.example/mcp', 'pulse.example.com');
    assert.match(absoluteTarget, /^HTTP\/1\.1 400 /);
    const backslashTarget = await rawHttpRequest(server.url, '/\\evil.example/mcp', 'pulse.example.com');
    assert.match(backslashTarget, /^HTTP\/1\.1 400 /);
    const malformedHost = await rawHttpRequest(server.url, '/mcp', '%%%');
    assert.match(malformedHost, /^HTTP\/1\.1 400 /);
    const health = await fetch(new URL('/health', server.url));
    assert.equal(health.status, 200);
  } finally {
    await server.stop();
  }
});

test('team partial-response errors destroy the response without a second write', () => {
  let destroyed = false;
  const partialResponse = {
    headersSent: true,
    destroy: () => { destroyed = true; },
    writeHead: () => { throw new Error('must not write after headers'); },
    end: () => { throw new Error('must not end a second payload'); },
  } as unknown as import('node:http').ServerResponse;
  assert.equal(terminateStartedResponse(partialResponse), true);
  assert.equal(destroyed, true);
});

test('team shutdown drains security events with a hard degraded deadline', async () => {
  const logs: string[] = [];
  assert.equal(await drainSecurityReporterForShutdown({ drain: async () => undefined }, 25, (line) => logs.push(line)), true);
  assert.equal(await drainSecurityReporterForShutdown({
    drain: () => new Promise<void>(() => undefined),
  }, 25, (line) => logs.push(line)), false);
  assert.deepEqual(logs, ['[pulse-mcp] team security audit degraded']);
});

test('in-process team request chain keeps concurrent JWT principals isolated through MCP dispatch', async () => {
  const now = 1_789_000_000;
  const issuer = 'https://auth.example.com';
  const resource = 'https://pulse.example.com/mcp';
  const { privateKey: issuerPrivate, publicKey: issuerPublic } = generateKeyPairSync('rsa', { modulusLength: 2048 });
  const issuerJWK = await exportJWK(issuerPublic);
  Object.assign(issuerJWK, { kid: 'issuer-key', alg: 'RS256', use: 'sig' });
  const verifier = new OAuthResourceVerifier({
    issuer,
    resource,
    now: () => now,
    resolveHost: async () => ['93.184.216.34'],
    fetch: async (input) => input.toString().endsWith('/jwks')
      ? Response.json({ keys: [issuerJWK] })
      : Response.json({ issuer, jwks_uri: `${issuer}/jwks`, code_challenge_methods_supported: ['S256'] }),
  });
  const dir = mkdtempSync(join(tmpdir(), 'pulse-u3-chain-'));
  const { privateKey: gatewayPrivate } = generateKeyPairSync('ed25519');
  const privateFile = join(dir, 'gateway.pk8.pem');
  writeFileSync(privateFile, gatewayPrivate.export({ format: 'pem', type: 'pkcs8' }), { mode: 0o600 });
  const gatewayJWK = createPublicKey(gatewayPrivate).export({ format: 'jwk' });
  const keyringFile = join(dir, 'gateway-keyring.json');
  writeFileSync(keyringFile, JSON.stringify({
    active: { kid: 'gateway-key', public_key: gatewayJWK.x }, previous: [],
  }), { mode: 0o600 });
  const signer = loadPrincipalSigner({
    privateKeyFile: privateFile,
    keyId: 'gateway-key',
    verifyKeyringFile: keyringFile,
    storeId: 'store_test',
    teamId: 'team_test',
    now: () => now,
  });
  const daemonRequests: Array<{ headers: Headers; body: string }> = [];
  const principalClient = new TeamPrincipalClient({
    daemonBaseURL: 'http://127.0.0.1:18789', signer, apiKey: () => 'ipc-key',
    fetch: async (_input, init) => {
      const body = String(init?.body);
      const headers = new Headers(init?.headers);
      daemonRequests.push({ headers, body });
      const parsed = JSON.parse(body);
      return Response.json({
        version: 'pulse.team.principal_context.v1', request_id: headers.get('x-pulse-request-id'),
        store_id: 'store_test', team_id: 'team_test', principal_id: `principal-${parsed.oauth_client_id}`,
        principal_kind: 'agent', oauth_client_key: 'a'.repeat(64), human_principal_id: `human-${parsed.oauth_subject}`,
        agent_binding_id: `binding-${parsed.oauth_client_id}`, membership_id: `membership-${parsed.oauth_subject}`,
        membership_role: 'member', team_auth_epoch: 1, principal_auth_epoch: 1,
        binding_auth_epoch: 1, membership_auth_epoch: 1, capabilities: parsed.capabilities,
      });
    },
  });
  const security = new TeamRequestSecurity({ verifier, principalClient });
  const makeToken = (subject: string, clientId: string, scope: string) => new SignJWT({
    iss: issuer, sub: subject, aud: resource, iat: now, exp: now + 300,
    jti: `jti-${clientId}`, client_id: clientId, scope,
  }).setProtectedHeader({ alg: 'RS256', kid: 'issuer-key', typ: 'at+jwt' }).sign(issuerPrivate);
  const dispatched: string[] = [];
  try {
    await Promise.all([
      ['human-a', 'client-a', 'pulse:connect pulse:read', 'pulse_team_recall'],
      ['human-b', 'client-b', 'pulse:connect pulse:write', 'pulse_team_remember'],
    ].map(async ([subject, clientId, scope, tool], index) => {
      const authorization = `Bearer ${await makeToken(subject, clientId, scope)}`;
      const baseline = await security.authenticateBeforeBody(authorization);
      const context = await security.resolveAfterBody({
        authorization, baseline,
        body: { method: 'tools/call', params: { name: tool } },
        requestId: `request-${index}`,
      });
      dispatched.push(`${context.principal_id}:${context.capabilities.join(',')}`);
    }));
    assert.deepEqual(dispatched.sort(), [
      'principal-client-a:pulse:connect,pulse:read',
      'principal-client-b:pulse:connect,pulse:write',
    ]);
    assert.equal(daemonRequests.length, 2);
    assert.ok(daemonRequests.every(({ headers }) => !headers.has('authorization')));
    assert.ok(daemonRequests.every(({ body }) => !body.includes('Bearer ')));
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

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
      PULSE_DATA_DIR: DIRECT_SPAWN_DATA_DIR,
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
      PULSE_DATA_DIR: DIRECT_SPAWN_DATA_DIR,
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
      PULSE_DATA_DIR: DIRECT_SPAWN_DATA_DIR,
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
