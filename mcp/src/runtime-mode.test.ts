import assert from 'node:assert/strict';
import { createPublicKey, generateKeyPairSync } from 'node:crypto';
import { spawn, type ChildProcess } from 'node:child_process';
import { once } from 'node:events';
import { existsSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';

const ENTRYPOINT = new URL('./index.ts', import.meta.url);
const spawnedTempDataDirs: string[] = [];

const TEAM_BASELINE: Record<string, string> = {
  PULSE_RUNTIME_MODE: 'team-remote',
  PULSE_MCP_MODE: 'daemon',
  PULSE_BASE_URL: 'http://127.0.0.1:18789',
  PULSE_REMOTE_PUBLIC_BASE_URL: 'https://pulse.example.com',
  PULSE_REMOTE_AUTH_ISSUER: 'https://auth.example.com/',
  PULSE_REMOTE_BEARER: '',
  PULSE_REMOTE_OAUTH_DEV: '',
  PULSE_REMOTE_TRUST_AUTH_HEADER: '',
  PULSE_REMOTE_AUTH_PROXY_MODE: '',
  PULSE_REMOTE_ALLOW_UNAUTHENTICATED: '',
  PULSE_ALLOW_AUTHLESS_PUBLIC: '',
  PULSE_TEAM_REMOTE_ACTIVATED: '',
};

interface RunOptions {
  env?: Record<string, string>;
  host?: string;
  httpFlag?: boolean;
  extraArgs?: string[];
  nodeMajor?: number;
}

interface RunResult {
  started: boolean;
  code: number | null;
  output: string;
}

async function stop(child: ChildProcess): Promise<void> {
  if (child.exitCode === null && child.signalCode === null) {
    child.kill('SIGTERM');
    await once(child, 'exit');
  }
}

async function runHttp(options: RunOptions = {}): Promise<RunResult> {
  const ownedDataDir = options.env?.PULSE_DATA_DIR === undefined
    ? mkdtempSync(join(tmpdir(), 'pulse-u1-runtime-'))
    : undefined;
  if (ownedDataDir) {
    spawnedTempDataDirs.push(ownedDataDir);
  }
  const securityDir = ownedDataDir ?? mkdtempSync(join(tmpdir(), 'pulse-u1-security-'));
  const { privateKey } = generateKeyPairSync('ed25519');
  const signingKeyFile = join(securityDir, 'principal.pk8.pem');
  writeFileSync(signingKeyFile, privateKey.export({ format: 'pem', type: 'pkcs8' }), { mode: 0o600 });
  const publicJWK = createPublicKey(privateKey).export({ format: 'jwk' });
  const keyringFile = join(securityDir, 'principal-keyring.json');
  writeFileSync(keyringFile, JSON.stringify({
    active: { kid: 'runtime-test-key', public_key: publicJWK.x }, previous: [],
  }), { mode: 0o600 });
  const { publicKey: installationPublicKey } = generateKeyPairSync('ec', { namedCurve: 'P-256' });
  const enrollmentRegistryFile = join(securityDir, 'enrollments.json');
  writeFileSync(enrollmentRegistryFile, JSON.stringify({
    schema: 'pulse.team.installation_enrollment_registry.v1',
    issuer: options.env?.PULSE_REMOTE_AUTH_ISSUER ?? TEAM_BASELINE.PULSE_REMOTE_AUTH_ISSUER,
    enrollments: [{
      enrollment_id: 'enrollment_runtime_1', generation: 1,
      client_id: 'runtime-client', subject: 'runtime-subject', status: 'active',
      public_jwk: installationPublicKey.export({ format: 'jwk' }),
    }],
  }), { mode: 0o600 });
  const nodeArgs: string[] = [];
  if (options.nodeMajor !== undefined) {
    const patch = `Object.defineProperty(process.versions, 'node', { value: '${options.nodeMajor}.0.0' });`;
    nodeArgs.push('--import', `data:text/javascript,${encodeURIComponent(patch)}`);
  }
  nodeArgs.push('--import', 'tsx', ENTRYPOINT.pathname);
  if (options.httpFlag !== false) {
    nodeArgs.push('--http');
  }
  nodeArgs.push(
    '--host',
    options.host ?? '127.0.0.1',
    '--port',
    '0',
    ...(options.extraArgs ?? []),
  );
  const child = spawn(process.execPath, nodeArgs, {
    env: {
      ...process.env,
      ...TEAM_BASELINE,
      PULSE_DATA_DIR: options.env?.PULSE_DATA_DIR ?? ownedDataDir,
      PULSE_TEAM_PRINCIPAL_SIGNING_KEY_FILE: signingKeyFile,
      PULSE_TEAM_PRINCIPAL_SIGNING_KID: 'runtime-test-key',
      PULSE_TEAM_PRINCIPAL_VERIFY_KEYRING_FILE: keyringFile,
      PULSE_REMOTE_ENROLLMENT_REGISTRY_FILE: enrollmentRegistryFile,
      PULSE_TEAM_EXPECTED_STORE_ID: 'store_test',
      PULSE_TEAM_EXPECTED_TEAM_ID: 'team_test',
      ...options.env,
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let output = '';
  child.stdout?.on('data', (chunk) => {
    output += chunk.toString();
  });
  child.stderr?.on('data', (chunk) => {
    output += chunk.toString();
  });

  try {
    const deadline = Date.now() + 8_000;
    while (Date.now() < deadline) {
      if (/Streamable HTTP listening on http:\/\/[^;]+\/mcp/.test(output)) {
        await stop(child);
        return { started: true, code: 0, output };
      }
      if (child.exitCode !== null) {
        return { started: false, code: child.exitCode, output };
      }
      await new Promise((resolve) => setTimeout(resolve, 25));
    }
    await stop(child);
    throw new Error(`pulse-mcp neither started nor exited:\n${output}`);
  } finally {
    await stop(child);
    if (ownedDataDir) {
      rmSync(ownedDataDir, { recursive: true, force: true });
    } else {
      rmSync(securityDir, { recursive: true, force: true });
    }
  }
}

async function runStdio(runtimeMode: string): Promise<string> {
  const dataDir = mkdtempSync(join(tmpdir(), 'pulse-u1-runtime-'));
  spawnedTempDataDirs.push(dataDir);
  const child = spawn(process.execPath, ['--import', 'tsx', ENTRYPOINT.pathname], {
    env: {
      ...process.env,
      PULSE_RUNTIME_MODE: runtimeMode,
      PULSE_MCP_MODE: 'auto',
      PULSE_DATA_DIR: dataDir,
    },
    stdio: ['pipe', 'pipe', 'pipe'],
  });
  let output = '';
  child.stdout?.on('data', (chunk) => {
    output += chunk.toString();
  });
  child.stderr?.on('data', (chunk) => {
    output += chunk.toString();
  });
  try {
    const deadline = Date.now() + 5_000;
    while (Date.now() < deadline) {
      if (/host-extracted stdio connected/.test(output)) {
        await stop(child);
        return output;
      }
      if (child.exitCode !== null) {
        throw new Error(`pulse-mcp exited before stdio connected (${child.exitCode}): ${output}`);
      }
      await new Promise((resolve) => setTimeout(resolve, 25));
    }
    await stop(child);
    throw new Error(`pulse-mcp did not connect stdio:\n${output}`);
  } finally {
    await stop(child);
    rmSync(dataDir, { recursive: true, force: true });
  }
}

test('default and explicit local runtime preserve stdio behavior', async (t) => {
  for (const runtimeMode of ['', 'local-stdio']) {
    await t.test(runtimeMode || 'default', async () => {
      const output = await runStdio(runtimeMode);
      assert.match(output, /engine: auto/);
    });
  }
  assert.ok(spawnedTempDataDirs.every((path) => !existsSync(path)));
});

test('team-remote static runtime truth table', async (t) => {
  const cases: Array<{
    name: string;
    expectedStart: boolean;
    expectedError?: RegExp;
    options?: RunOptions;
  }> = [
    {
      name: 'valid loopback preflight',
      expectedStart: true,
    },
    {
      name: 'canonical issuer trailing slash is preserved',
      expectedStart: true,
      options: { env: { PULSE_REMOTE_AUTH_ISSUER: 'https://auth.example.com/tenant/' } },
    },
    {
      name: 'explicit team runtime selects HTTP without the legacy flag',
      expectedStart: true,
      options: { httpFlag: false },
    },
    {
      name: 'missing installation enrollment registry',
      expectedStart: false,
      expectedError: /requires exact non-empty PULSE_REMOTE_ENROLLMENT_REGISTRY_FILE/,
      options: { env: { PULSE_REMOTE_ENROLLMENT_REGISTRY_FILE: '' } },
    },
    {
      name: 'inferred development HTTP remains supported',
      expectedStart: true,
      options: {
        env: {
          PULSE_RUNTIME_MODE: '',
          PULSE_MCP_MODE: 'auto',
          PULSE_REMOTE_PUBLIC_BASE_URL: '',
          PULSE_REMOTE_AUTH_ISSUER: '',
          PULSE_REMOTE_BEARER: 'dev-token',
        },
      },
    },
    {
      name: 'explicit development HTTP remains supported',
      expectedStart: true,
      options: {
        httpFlag: false,
        env: {
          PULSE_RUNTIME_MODE: 'development-http',
          PULSE_MCP_MODE: 'auto',
          PULSE_REMOTE_PUBLIC_BASE_URL: '',
          PULSE_REMOTE_AUTH_ISSUER: '',
          PULSE_REMOTE_BEARER: 'dev-token',
        },
      },
    },
    {
      name: 'unknown runtime mode',
      expectedStart: false,
      expectedError: /invalid PULSE_RUNTIME_MODE/,
      options: { env: { PULSE_RUNTIME_MODE: 'remote-ish' } },
    },
    {
      name: 'local stdio cannot be combined with HTTP transport',
      expectedStart: false,
      expectedError: /local-stdio cannot use --http/,
      options: { env: { PULSE_RUNTIME_MODE: 'local-stdio' } },
    },
    {
      name: 'auto engine',
      expectedStart: false,
      expectedError: /requires PULSE_MCP_MODE=daemon/,
      options: { env: { PULSE_MCP_MODE: 'auto' } },
    },
    {
      name: 'standalone engine',
      expectedStart: false,
      expectedError: /requires PULSE_MCP_MODE=daemon/,
      options: { env: { PULSE_MCP_MODE: 'standalone' } },
    },
    {
      name: 'static bearer',
      expectedStart: false,
      expectedError: /refuses static PULSE_REMOTE_BEARER/,
      options: { env: { PULSE_REMOTE_BEARER: 'legacy-token' } },
    },
    {
      name: 'development OAuth',
      expectedStart: false,
      expectedError: /refuses PULSE_REMOTE_OAUTH_DEV/,
      options: { env: { PULSE_REMOTE_OAUTH_DEV: '1' } },
    },
    {
      name: 'trusted proxy bearer passthrough',
      expectedStart: false,
      expectedError: /refuses trusted proxy bearer passthrough/,
      options: {
        env: {
          PULSE_REMOTE_AUTH_PROXY_MODE: '1',
          PULSE_REMOTE_TRUST_AUTH_HEADER: '1',
        },
      },
    },
    {
      name: 'proxy auth mode alone',
      expectedStart: false,
      expectedError: /refuses trusted proxy bearer passthrough/,
      options: { env: { PULSE_REMOTE_AUTH_PROXY_MODE: '1' } },
    },
    {
      name: 'trusted auth header alone',
      expectedStart: false,
      expectedError: /refuses trusted proxy bearer passthrough/,
      options: { env: { PULSE_REMOTE_TRUST_AUTH_HEADER: '1' } },
    },
    {
      name: 'unauthenticated shortcut',
      expectedStart: false,
      expectedError: /refuses unauthenticated HTTP shortcuts/,
      options: { env: { PULSE_REMOTE_ALLOW_UNAUTHENTICATED: '1' } },
    },
    {
      name: 'authless-public shortcut',
      expectedStart: false,
      expectedError: /refuses unauthenticated HTTP shortcuts/,
      options: { env: { PULSE_ALLOW_AUTHLESS_PUBLIC: '1' } },
    },
    {
      name: 'unauthenticated CLI shortcut',
      expectedStart: false,
      expectedError: /refuses unauthenticated HTTP shortcuts/,
      options: { extraArgs: ['--allow-unauthenticated'] },
    },
    {
      name: 'non-HTTPS resource',
      expectedStart: false,
      expectedError: /PULSE_REMOTE_PUBLIC_BASE_URL.*HTTPS/,
      options: { env: { PULSE_REMOTE_PUBLIC_BASE_URL: 'http://pulse.example.com' } },
    },
    {
      name: 'non-HTTPS issuer',
      expectedStart: false,
      expectedError: /PULSE_REMOTE_AUTH_ISSUER.*HTTPS/,
      options: { env: { PULSE_REMOTE_AUTH_ISSUER: 'http://auth.example.com' } },
    },
    {
      name: 'public base path prefix',
      expectedStart: false,
      expectedError: /PULSE_REMOTE_PUBLIC_BASE_URL.*HTTPS URL/,
      options: { env: { PULSE_REMOTE_PUBLIC_BASE_URL: 'https://pulse.example.com/prefix' } },
    },
    {
      name: 'public base query',
      expectedStart: false,
      expectedError: /PULSE_REMOTE_PUBLIC_BASE_URL.*HTTPS URL/,
      options: { env: { PULSE_REMOTE_PUBLIC_BASE_URL: 'https://pulse.example.com?tenant=a' } },
    },
    {
      name: 'daemon path prefix',
      expectedStart: false,
      expectedError: /PULSE_BASE_URL.*loopback/,
      options: { env: { PULSE_BASE_URL: 'http://127.0.0.1:18789/prefix' } },
    },
    {
      name: 'daemon query',
      expectedStart: false,
      expectedError: /PULSE_BASE_URL.*loopback/,
      options: { env: { PULSE_BASE_URL: 'http://127.0.0.1:18789?tenant=a' } },
    },
    {
      name: 'public daemon address',
      expectedStart: false,
      expectedError: /PULSE_BASE_URL.*loopback/,
      options: { env: { PULSE_BASE_URL: 'http://10.0.0.8:18789' } },
    },
    {
      name: 'localhost daemon address',
      expectedStart: false,
      expectedError: /PULSE_BASE_URL.*numeric loopback/,
      options: { env: { PULSE_BASE_URL: 'http://localhost:18789' } },
    },
    {
      name: 'Node below 22',
      expectedStart: false,
      expectedError: /requires Node 22 or newer/,
      options: { nodeMajor: 21 },
    },
    {
      name: 'public listener before activation',
      expectedStart: false,
      expectedError: /numeric loopback/,
      options: { host: '0.0.0.0' },
    },
    {
      name: 'localhost listener before activation',
      expectedStart: false,
      expectedError: /numeric loopback/,
      options: { host: 'localhost' },
    },
    {
      name: 'activation cannot be forced in U1',
      expectedStart: false,
      expectedError: /public activation is unavailable/,
      options: { env: { PULSE_TEAM_REMOTE_ACTIVATED: '1' } },
    },
    {
      name: 'legacy SSE flag',
      expectedStart: false,
      expectedError: /Streamable HTTP only/,
      options: { extraArgs: ['--sse'] },
    },
  ];

  for (const entry of cases) {
    await t.test(entry.name, async () => {
      const result = await runHttp(entry.options);
      assert.equal(
        result.started,
        entry.expectedStart,
        `${entry.name}: exit=${result.code}\n${result.output}`,
      );
      if (!entry.expectedStart) {
        assert.notEqual(result.code, 0, result.output);
        assert.match(result.output, entry.expectedError!, entry.name);
      }
    });
  }
  assert.ok(spawnedTempDataDirs.every((path) => !existsSync(path)));
});
