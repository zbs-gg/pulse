#!/usr/bin/env node
/**
 * Negative smoke for the BUILT team-remote gateway.
 *
 * This intentionally uses only synthetic configuration and a temporary data
 * directory. It proves unsafe startup combinations fail closed, the compiled
 * team tool/route surface contains no local fallback operations, malformed
 * JWT/Owner traffic is rejected before daemon dispatch, and failures disclose
 * neither IPC credentials nor local security-file paths.
 */
import { execFileSync, spawn } from 'node:child_process';
import { createPublicKey, generateKeyPairSync } from 'node:crypto';
import { once } from 'node:events';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { request as httpRequest } from 'node:http';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const mcpRoot = join(here, '..');
const entrypoint = join(mcpRoot, 'dist', 'index.js');
const contractsModule = join(mcpRoot, 'dist', 'team-contracts.js');
const tempRoot = mkdtempSync(join(tmpdir(), 'pulse-team-negative-smoke-'));
const dataDir = join(tempRoot, 'data');
const signingKeyPath = join(tempRoot, 'synthetic-gateway.pk8.pem');
const keyringPath = join(tempRoot, 'synthetic-keyring.json');
const enrollmentRegistryPath = join(tempRoot, 'synthetic-enrollments.json');
const secretSentinel = 'synthetic-ipc-secret-must-not-appear';
const publicOrigin = 'https://synthetic-pulse.example';
const allowedOrigin = 'https://synthetic-client.example';
const observations = [];
let validChild;

function fail(message) {
  throw new Error(`[team-remote-negative-smoke] ${message}`);
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function stop(child) {
  if (child && child.exitCode === null && child.signalCode === null) {
    child.kill('SIGTERM');
    await once(child, 'exit');
  }
}

function spawnEnvironment(overrides = {}) {
  return {
    ...process.env,
    PULSE_RUNTIME_MODE: 'team-remote',
    PULSE_MCP_MODE: 'daemon',
    PULSE_BASE_URL: 'http://127.0.0.1:18789',
    PULSE_DATA_DIR: dataDir,
    PULSE_API_KEY: secretSentinel,
    PULSE_REMOTE_PUBLIC_BASE_URL: publicOrigin,
    PULSE_REMOTE_AUTH_ISSUER: 'https://synthetic-idp.example/',
    PULSE_REMOTE_ALLOWED_ORIGINS: allowedOrigin,
    PULSE_REMOTE_BEARER: '',
    PULSE_REMOTE_OAUTH_DEV: '',
    PULSE_REMOTE_AUTH_PROXY_MODE: '',
    PULSE_REMOTE_TRUST_AUTH_HEADER: '',
    PULSE_REMOTE_ALLOW_UNAUTHENTICATED: '',
    PULSE_ALLOW_AUTHLESS_PUBLIC: '',
    PULSE_TEAM_REMOTE_ACTIVATED: '',
    PULSE_MCP_TRANSPORT: 'streamable-http',
    PULSE_TEAM_PRINCIPAL_SIGNING_KEY_FILE: signingKeyPath,
    PULSE_TEAM_PRINCIPAL_SIGNING_KID: 'synthetic-gateway-key',
    PULSE_TEAM_PRINCIPAL_VERIFY_KEYRING_FILE: keyringPath,
    PULSE_REMOTE_ENROLLMENT_REGISTRY_FILE: enrollmentRegistryPath,
    PULSE_TEAM_EXPECTED_STORE_ID: 'store_synthetic_negative',
    PULSE_TEAM_EXPECTED_TEAM_ID: 'team_synthetic_negative',
    ...overrides,
  };
}

function launch({ env = {}, extraArgs = [], host = '127.0.0.1' } = {}) {
  const child = spawn(process.execPath, [
    entrypoint,
    '--http',
    '--host', host,
    '--port', '0',
    ...extraArgs,
  ], {
    cwd: mcpRoot,
    env: spawnEnvironment(env),
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let output = '';
  child.stdout.on('data', (chunk) => { output += chunk.toString(); });
  child.stderr.on('data', (chunk) => { output += chunk.toString(); });
  return { child, output: () => output };
}

async function expectStartupRejected(name, expected, options = {}) {
  const launched = launch(options);
  const deadline = Date.now() + 6_000;
  while (Date.now() < deadline && launched.child.exitCode === null) {
    if (/Streamable HTTP listening on /.test(launched.output())) break;
    await sleep(20);
  }
  const started = /Streamable HTTP listening on /.test(launched.output());
  if (launched.child.exitCode === null) await stop(launched.child);
  const output = launched.output();
  observations.push(output);
  if (started) fail(`${name}: unsafe gateway configuration started`);
  if (!expected.test(output)) fail(`${name}: missing fail-closed reason in ${JSON.stringify(output)}`);
  process.stdout.write(`  [ok] ${name}\n`);
}

async function startPreactivationGateway() {
  const launched = launch();
  const deadline = Date.now() + 8_000;
  while (Date.now() < deadline) {
    const match = launched.output().match(
      /Streamable HTTP listening on (http:\/\/127\.0\.0\.1:\d+)\/mcp/,
    );
    if (match) return { ...launched, baseURL: match[1] };
    if (launched.child.exitCode !== null) {
      fail(`valid loopback preactivation gateway exited: ${JSON.stringify(launched.output())}`);
    }
    await sleep(20);
  }
  await stop(launched.child);
  fail(`valid loopback preactivation gateway did not start: ${JSON.stringify(launched.output())}`);
}

function rawRequest(baseURL, { method = 'GET', path = '/', headers = {}, body = '' }) {
  const url = new URL(baseURL);
  return new Promise((resolve, reject) => {
    const request = httpRequest({
      host: url.hostname,
      port: Number(url.port),
      method,
      path,
      headers: {
        ...headers,
        ...(body === '' ? {} : { 'Content-Length': String(Buffer.byteLength(body)) }),
      },
    }, (response) => {
      const chunks = [];
      response.on('data', (chunk) => chunks.push(Buffer.from(chunk)));
      response.once('end', () => resolve({
        status: response.statusCode ?? 0,
        body: Buffer.concat(chunks).toString('utf8'),
      }));
    });
    request.once('error', reject);
    request.end(body);
  });
}

function assertNoSensitiveOutput() {
  const combined = observations.join('\n');
  for (const forbidden of [
    secretSentinel,
    tempRoot,
    dataDir,
    signingKeyPath,
    keyringPath,
    enrollmentRegistryPath,
    'BEGIN PRIVATE KEY',
    '~/.pulse',
  ]) {
    if (combined.includes(forbidden)) {
      fail(`output disclosed forbidden secret/path marker ${JSON.stringify(forbidden)}`);
    }
  }
}

try {
  process.stdout.write('[team-remote-negative-smoke] building shipped artifact...\n');
  execFileSync('npm', ['run', 'build'], { cwd: mcpRoot, stdio: 'inherit' });

  const { privateKey } = generateKeyPairSync('ed25519');
  writeFileSync(
    signingKeyPath,
    privateKey.export({ format: 'pem', type: 'pkcs8' }),
    { mode: 0o600 },
  );
  const publicJWK = createPublicKey(privateKey).export({ format: 'jwk' });
  writeFileSync(keyringPath, JSON.stringify({
    active: { kid: 'synthetic-gateway-key', public_key: publicJWK.x },
    previous: [],
  }), { mode: 0o600 });
  const { publicKey: installationPublicKey } = generateKeyPairSync('ec', { namedCurve: 'P-256' });
  writeFileSync(enrollmentRegistryPath, JSON.stringify({
    schema: 'pulse.team.installation_enrollment_registry.v1',
    issuer: 'https://synthetic-idp.example/',
    enrollments: [{
      enrollment_id: 'enrollment_negative_smoke_1',
      generation: 1,
      client_id: 'negative-smoke-client',
      subject: 'negative-smoke-subject',
      status: 'active',
      public_jwk: installationPublicKey.export({ format: 'jwk' }),
    }],
  }), { mode: 0o600 });

  process.stdout.write('[team-remote-negative-smoke] unsafe startup matrix:\n');
  await expectStartupRejected(
    'legacy static bearer auth',
    /refuses static PULSE_REMOTE_BEARER/,
    { env: { PULSE_REMOTE_BEARER: secretSentinel } },
  );
  await expectStartupRejected(
    'legacy development OAuth',
    /refuses PULSE_REMOTE_OAUTH_DEV/,
    { env: { PULSE_REMOTE_OAUTH_DEV: '1' } },
  );
  await expectStartupRejected(
    'trusted proxy identity',
    /refuses trusted proxy bearer passthrough/,
    { env: { PULSE_REMOTE_AUTH_PROXY_MODE: '1' } },
  );
  await expectStartupRejected(
    'unauthenticated shortcut',
    /refuses unauthenticated HTTP shortcuts/,
    { extraArgs: ['--allow-unauthenticated'] },
  );
  await expectStartupRejected(
    'automatic local fallback',
    /requires PULSE_MCP_MODE=daemon/,
    { env: { PULSE_MCP_MODE: 'auto' } },
  );
  await expectStartupRejected(
    'standalone local fallback',
    /requires PULSE_MCP_MODE=daemon/,
    { env: { PULSE_MCP_MODE: 'standalone' } },
  );
  await expectStartupRejected(
    'legacy SSE route flag',
    /Streamable HTTP only/,
    { extraArgs: ['--sse'] },
  );
  await expectStartupRejected(
    'non-loopback daemon base',
    /PULSE_BASE_URL.*loopback/,
    { env: { PULSE_BASE_URL: 'http://192.0.2.44:18789' } },
  );
  await expectStartupRejected(
    'public bind with missing activation',
    /numeric loopback before public activation/,
    { host: '0.0.0.0' },
  );

  const { TEAM_PRODUCT_TOOL_DESCRIPTORS } = await import(pathToFileURL(contractsModule).href);
  const toolNames = TEAM_PRODUCT_TOOL_DESCRIPTORS.map(({ name }) => name).sort();
  const expectedTools = [
    'pulse_team_audit',
    'pulse_team_context_query',
    'pulse_team_delete_status',
    'pulse_team_inspect',
    'pulse_team_recall',
    'pulse_team_resume',
    'pulse_team_status',
  ].sort();
  if (JSON.stringify(toolNames) !== JSON.stringify(expectedTools)) {
    fail(`built tool allowlist mismatch: ${JSON.stringify(toolNames)}`);
  }
  for (const localTool of ['pulse_remember', 'pulse_forget', 'pulse_wipe', 'pulse_status']) {
    if (toolNames.includes(localTool)) fail(`local fallback tool leaked: ${localTool}`);
  }
  for (const mutation of ['pulse_team_remember', 'pulse_team_graph_delta', 'pulse_team_delete']) {
    if (toolNames.includes(mutation)) fail(`agent-controlled Commons mutation leaked: ${mutation}`);
  }
  process.stdout.write('  [ok] compiled team tool allowlist excludes local fallback and agent-controlled Commons mutations\n');

  const gateway = await startPreactivationGateway();
  validChild = gateway.child;
  for (const path of ['/memory/status', '/memory/delete', '/memory/wipe', '/sse', '/mcp/']) {
    const response = await rawRequest(gateway.baseURL, { path });
    observations.push(response.body);
    if (response.status !== 404) fail(`legacy route ${path} returned ${response.status}`);
  }
  process.stdout.write('  [ok] legacy/local route surface is absent\n');

  const invalidAssertion = await rawRequest(gateway.baseURL, {
    method: 'POST',
    path: '/mcp',
    headers: {
      Host: 'synthetic-pulse.example',
      Origin: allowedOrigin,
      Authorization: 'Bearer definitely.not-a.valid-jwt',
      'Content-Type': 'application/json',
      Accept: 'application/json, text/event-stream',
    },
    body: '{ malformed-json-after-invalid-jwt',
  });
  observations.push(invalidAssertion.body);
  if (invalidAssertion.status !== 401 || !/invalid_token/.test(invalidAssertion.body)) {
    fail(`invalid assertion was not rejected before body/daemon: ${invalidAssertion.status}`);
  }
  process.stdout.write('  [ok] invalid JWT assertion is rejected before body parsing/daemon dispatch\n');

  const wrongBrowserBinding = await rawRequest(gateway.baseURL, {
    method: 'POST',
    path: '/owner/v1/approval',
    headers: {
      Host: new URL(gateway.baseURL).host,
      Origin: allowedOrigin,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ approval_nonce: secretSentinel }),
  });
  observations.push(wrongBrowserBinding.body);
  if (
    wrongBrowserBinding.status !== 403 ||
    JSON.stringify(JSON.parse(wrongBrowserBinding.body)) !==
      JSON.stringify({ error: 'owner_request_denied', fallback: false })
  ) {
    fail(`invalid Owner browser binding was not concealed: ${wrongBrowserBinding.status}`);
  }

  const invalidApprovalJWT = await rawRequest(gateway.baseURL, {
    method: 'POST',
    path: '/owner/v1/approval',
    headers: {
      Host: 'synthetic-pulse.example',
      Origin: allowedOrigin,
      Authorization: 'Bearer malformed.owner.jwt',
      'Content-Type': 'application/json',
    },
    body: '{ malformed-approval-after-invalid-jwt',
  });
  observations.push(invalidApprovalJWT.body);
  if (invalidApprovalJWT.status !== 401 || !/invalid_token/.test(invalidApprovalJWT.body)) {
    fail(`invalid Owner approval assertion was not rejected: ${invalidApprovalJWT.status}`);
  }
  process.stdout.write('  [ok] invalid Owner browser binding/approval assertion fail closed\n');

  observations.push(gateway.output());
  assertNoSensitiveOutput();
  process.stdout.write('  [ok] gateway output contains no IPC secret or local security path\n');

  process.stdout.write(
    '[team-remote-negative-smoke] PASS: unsafe modes, fallback surfaces, invalid assertions, and preactivation public bind all failed closed.\n',
  );
} catch (error) {
  process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
  process.exitCode = 1;
} finally {
  await stop(validChild).catch(() => undefined);
  rmSync(tempRoot, { recursive: true, force: true });
}
