import assert from 'node:assert/strict';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  MacOSKeychainCredentialStore,
} from './remote-auth.js';
import { RemoteAuthError } from './remote-auth-errors.js';
import {
  acquireRemoteCredentialLock,
  callTeamRemoteTool,
  createTeamRemoteFetch,
  isReadOnlyTeamTool,
  resetTeamRemoteSessionsForTests,
  TeamRemoteClientError,
  TeamRemoteDomainError,
} from './team-remote-client.js';

function binding(overrides = {}) {
  return {
    mode: 'team',
    fallback: false,
    desk: { store_id: 'desk-nik' },
    commons: {
      resource: 'https://pulse.example.test/mcp',
      credential_ref: 'keychain:pulse/team/nik',
      ...overrides,
    },
  };
}

test('remote transport accepts only the fixed signed Commons MCP target and read-only Team tools', async () => {
  assert.equal(isReadOnlyTeamTool('pulse_team_resume'), true);
  assert.equal(isReadOnlyTeamTool('pulse_team_audit'), false);
  assert.equal(isReadOnlyTeamTool('pulse_team_remember'), false);
  assert.equal(isReadOnlyTeamTool('pulse_team_graph_delta'), false);
  assert.throws(
    () => createTeamRemoteFetch(binding({ resource: 'https://pulse.example.test/not-mcp' }), {}),
    /team_remote_resource_invalid/,
  );

  const calls = [];
  const remoteFetch = createTeamRemoteFetch(binding(), {
    credentialStore: {},
    refresh: async (_store, ref) => calls.push(`refresh:${ref}`),
    acquireLock: async () => {
      calls.push('lock');
      return async () => calls.push('unlock');
    },
    buildHeaders: (_url, { method }) => ({
      Authorization: 'DPoP token-sentinel',
      DPoP: `proof-${method}`,
      'X-Pulse-Enrollment': 'enrollment-1',
    }),
    now: () => 100,
    fetch: async (url, init) => {
      calls.push({ url, init });
      return new Response('{}');
    },
  });
  await remoteFetch('https://pulse.example.test/mcp', {
    method: 'POST', headers: { Accept: 'application/json, text/event-stream' }, body: '{}',
  });
  assert.deepEqual(calls.slice(0, 3), ['lock', 'refresh:keychain:pulse/team/nik', 'unlock']);
  const sent = calls[3];
  assert.equal(sent.url, 'https://pulse.example.test/mcp');
  assert.equal(sent.init.headers.get('Authorization'), 'DPoP token-sentinel');
  assert.equal(sent.init.headers.get('DPoP'), 'proof-POST');
  assert.equal(sent.init.headers.get('Accept'), 'application/json, text/event-stream');
  await assert.rejects(
    remoteFetch('https://other.example/mcp', { method: 'POST' }),
    /team_remote_request_target_mismatch/,
  );
  await assert.rejects(
    remoteFetch('https://pulse.example.test/mcp', {
      method: 'POST', headers: { Authorization: 'Bearer attacker' },
    }),
    /team_remote_caller_auth_forbidden/,
  );
});

test('per-credential lock serializes refresh-token rotation contenders', async () => {
  const lockRoot = mkdtempSync(join(tmpdir(), 'pulse-remote-lock-'));
  let active = 0;
  let maximum = 0;
  const contender = async () => {
    const release = await acquireRemoteCredentialLock('keychain:pulse/team/nik', { lockRoot });
    active++;
    maximum = Math.max(maximum, active);
    await new Promise((resolve) => setTimeout(resolve, 25));
    active--;
    await release();
  };
  await Promise.all([contender(), contender(), contender()]);
  assert.equal(maximum, 1);
});

test('remote read retries one marked refreshable 401 only after forced locked refresh with a fresh proof', async () => {
  const refreshes = [];
  const proofs = [];
  let sends = 0;
  const remoteFetch = createTeamRemoteFetch(binding(), {
    credentialStore: {},
    acquireLock: async () => async () => {},
    refresh: async (_store, _ref, options) => refreshes.push(options.force),
    buildHeaders: () => ({
      Authorization: `DPoP token-${proofs.length + 1}`,
      DPoP: `proof-${proofs.push(proofs.length + 1)}`,
      'X-Pulse-Enrollment': 'enrollment-1',
    }),
    now: () => 100,
    fetch: async (_url, init) => {
      sends++;
      assert.equal(init.headers.get('DPoP'), `proof-${sends}`);
      return sends === 1
        ? new Response('{}', {
          status: 401,
          headers: { 'WWW-Authenticate': 'DPoP error="invalid_token", pulse_reauth="refresh"' },
        })
        : new Response('{}');
    },
  });
  const response = await remoteFetch('https://pulse.example.test/mcp', { method: 'POST' });
  assert.equal(response.status, 200);
  assert.deepEqual(refreshes, [false, true]);
  assert.equal(sends, 2);
});

test('permanent enrollment 401 never forces refresh or retries', async () => {
  const refreshes = [];
  let sends = 0;
  const remoteFetch = createTeamRemoteFetch(binding(), {
    credentialStore: {},
    acquireLock: async () => async () => {},
    refresh: async (_store, _ref, options) => refreshes.push(options.force),
    buildHeaders: () => ({
      Authorization: 'DPoP token', DPoP: 'fresh-proof', 'X-Pulse-Enrollment': 'enrollment-1',
    }),
    now: () => 100,
    fetch: async () => {
      sends++;
      return new Response('{}', {
        status: 401,
        headers: { 'WWW-Authenticate': 'DPoP error="invalid_token"' },
      });
    },
  });
  const response = await remoteFetch('https://pulse.example.test/mcp', { method: 'POST' });
  assert.equal(response.status, 401);
  assert.deepEqual(refreshes, [false]);
  assert.equal(sends, 1);
});

test('process-scoped Team session reuses one client and transport for multiple reads', async () => {
  await resetTeamRemoteSessionsForTests();
  let clients = 0;
  let connects = 0;
  let calls = 0;
  let closes = 0;
  const credentialStore = {
    get: () => JSON.stringify({
      enrollment: { generation: 1 },
      installation: { keyThumbprint: 'a'.repeat(43) },
    }),
  };
  const options = {
    credentialStore,
    clientFactory: () => {
      clients++;
      return {
        async connect() { connects++; },
        async callTool({ name }) {
          calls++;
          return { content: [{ type: 'text', text: JSON.stringify({ name }) }] };
        },
        async close() { closes++; },
      };
    },
    transportFactory: () => ({}),
  };
  try {
    const first = await callTeamRemoteTool(binding(), 'pulse_team_status', {}, options);
    const second = await callTeamRemoteTool(binding(), 'pulse_team_recall', {}, options);
    assert.equal(first.name, 'pulse_team_status');
    assert.equal(second.name, 'pulse_team_recall');
    assert.deepEqual({ clients, connects, calls, closes }, { clients: 1, connects: 1, calls: 2, closes: 0 });
  } finally {
    await resetTeamRemoteSessionsForTests();
  }
  assert.equal(closes, 1);
});

test('parallel Team reads wait for the one shared connection before calling tools', async () => {
  await resetTeamRemoteSessionsForTests();
  let clients = 0;
  let connects = 0;
  let calls = 0;
  let connected = false;
  let releaseConnect;
  const credentialStore = {
    get: () => JSON.stringify({
      enrollment: { generation: 1 },
      installation: { keyThumbprint: 'h'.repeat(43) },
    }),
  };
  const options = {
    credentialStore,
    clientFactory: () => {
      clients++;
      return {
        connect() {
          connects++;
          return new Promise((resolve) => {
            releaseConnect = () => {
              connected = true;
              resolve();
            };
          });
        },
        async callTool({ name }) {
          assert.equal(connected, true);
          calls++;
          return { content: [{ type: 'text', text: JSON.stringify({ name }) }] };
        },
        async close() {},
      };
    },
    transportFactory: () => ({}),
  };
  try {
    const first = callTeamRemoteTool(binding(), 'pulse_team_status', {}, options);
    while (!releaseConnect) await Promise.resolve();
    const second = callTeamRemoteTool(binding(), 'pulse_team_recall', {}, options);
    await new Promise((resolve) => setImmediate(resolve));
    assert.deepEqual({ clients, connects, calls }, { clients: 1, connects: 1, calls: 0 });
    releaseConnect();
    assert.deepEqual(await Promise.all([first, second]), [
      { name: 'pulse_team_status' },
      { name: 'pulse_team_recall' },
    ]);
    assert.equal(calls, 2);
  } finally {
    await resetTeamRemoteSessionsForTests();
  }
});

test('credential generation change closes the stale Team session before reconnecting', async () => {
  await resetTeamRemoteSessionsForTests();
  let generation = 1;
  let clients = 0;
  let closes = 0;
  const credentialStore = {
    get: () => JSON.stringify({
      enrollment: { generation },
      installation: { keyThumbprint: 'c'.repeat(43) },
    }),
  };
  const options = {
    credentialStore,
    clientFactory: () => {
      const id = ++clients;
      return {
        async connect() {},
        async callTool() { return { content: [{ type: 'text', text: JSON.stringify({ id }) }] }; },
        async close() { closes++; },
      };
    },
    transportFactory: () => ({}),
  };
  try {
    assert.equal((await callTeamRemoteTool(binding(), 'pulse_team_status', {}, options)).id, 1);
    generation = 2;
    assert.equal((await callTeamRemoteTool(binding(), 'pulse_team_status', {}, options)).id, 2);
    assert.equal(closes, 1);
  } finally {
    await resetTeamRemoteSessionsForTests();
  }
});

test('transport failure invalidates the session and the next read reconnects', async () => {
  await resetTeamRemoteSessionsForTests();
  let clients = 0;
  let closes = 0;
  const credentialStore = {
    get: () => JSON.stringify({
      enrollment: { generation: 1 },
      installation: { keyThumbprint: 'd'.repeat(43) },
    }),
  };
  const options = {
    credentialStore,
    clientFactory: () => {
      const id = ++clients;
      return {
        async connect() {},
        async callTool() {
          if (id === 1) throw new Error('socket closed');
          return { content: [{ type: 'text', text: '{"ok":true}' }] };
        },
        async close() { closes++; },
      };
    },
    transportFactory: () => ({}),
  };
  try {
    await assert.rejects(
      callTeamRemoteTool(binding(), 'pulse_team_status', {}, options),
      /team_remote_unavailable/,
    );
    assert.deepEqual(await callTeamRemoteTool(binding(), 'pulse_team_status', {}, options), { ok: true });
    assert.equal(clients, 2);
    assert.equal(closes, 1);
  } finally {
    await resetTeamRemoteSessionsForTests();
  }
});

test('operation deadline includes connect and invalidates a hung session', async () => {
  await resetTeamRemoteSessionsForTests();
  let closes = 0;
  const credentialStore = {
    get: () => JSON.stringify({
      enrollment: { generation: 1 },
      installation: { keyThumbprint: 'e'.repeat(43) },
    }),
  };
  try {
    await assert.rejects(
      callTeamRemoteTool(binding(), 'pulse_team_status', {}, {
        credentialStore,
        operationTimeoutMs: 20,
        clientFactory: () => ({
          connect(_transport, { signal }) {
            return new Promise((_resolve, reject) => {
              signal.addEventListener('abort', () => reject(signal.reason), { once: true });
            });
          },
          async callTool() { throw new Error('must not call'); },
          async close() { closes++; },
        }),
        transportFactory: () => ({}),
      }),
      /team_remote_operation_timeout/,
    );
    assert.equal(closes, 1);
  } finally {
    await resetTeamRemoteSessionsForTests();
  }
});

test('operation timeout returns within bounded cleanup when connect and close ignore abort', async () => {
  await resetTeamRemoteSessionsForTests();
  const credentialStore = {
    get: () => JSON.stringify({
      enrollment: { generation: 1 },
      installation: { keyThumbprint: 'g'.repeat(43) },
    }),
  };
  const started = Date.now();
  try {
    await assert.rejects(
      callTeamRemoteTool(binding(), 'pulse_team_status', {}, {
        credentialStore,
        operationTimeoutMs: 20,
        clientFactory: () => ({
          connect() { return new Promise(() => {}); },
          async callTool() { throw new Error('must not call'); },
          close() { return new Promise(() => {}); },
        }),
        transportFactory: () => ({}),
      }),
      /team_remote_operation_timeout/,
    );
    assert.ok(Date.now() - started < 500, `cleanup exceeded bound: ${Date.now() - started}ms`);
  } finally {
    await resetTeamRemoteSessionsForTests();
  }
});

test('operation deadline caps a blocking Keychain credential generation read', async () => {
  await resetTeamRemoteSessionsForTests();
  const waits = new Int32Array(new SharedArrayBuffer(4));
  const observedTimeouts = [];
  const credentialStore = new MacOSKeychainCredentialStore({
    trustMode: 'test',
    spawnSync: (_command, _args, options) => {
      observedTimeouts.push(options.timeout);
      Atomics.wait(waits, 0, 0, Math.min(options.timeout + 5, 120));
      return { status: null, signal: 'SIGTERM', error: { code: 'ETIMEDOUT' }, stdout: '', stderr: '' };
    },
  });
  const started = Date.now();
  try {
    await assert.rejects(
      callTeamRemoteTool(binding(), 'pulse_team_status', {}, {
        credentialStore,
        operationTimeoutMs: 30,
        clientFactory: () => { throw new Error('must not create client'); },
      }),
      /team_remote_operation_timeout/,
    );
    assert.ok(Date.now() - started < 90, `credential lookup exceeded operation bound: ${Date.now() - started}ms`);
    assert.ok(observedTimeouts.length > 0 && observedTimeouts.every((value) => value <= 30), observedTimeouts);
  } finally {
    await resetTeamRemoteSessionsForTests();
  }
});

test('cached Team transport propagates each call abort into refresh and prevents late mutation', async () => {
  await resetTeamRemoteSessionsForTests();
  let transport;
  let lateMutations = 0;
  let refreshSignal;
  let networkCalls = 0;
  const credentialStore = {
    get: () => JSON.stringify({
      enrollment: { generation: 1 },
      installation: { keyThumbprint: 'i'.repeat(43) },
    }),
  };
  try {
    const started = Date.now();
    await assert.rejects(
      callTeamRemoteTool(binding(), 'pulse_team_status', {}, {
        credentialStore,
        operationTimeoutMs: 50,
        acquireLock: async () => async () => {},
        refresh: async (_store, _ref, options) => {
          refreshSignal = options.signal;
          await new Promise((resolve) => {
            const timer = setTimeout(() => {
              if (!options.signal?.aborted) lateMutations++;
              resolve();
            }, 120);
            options.signal?.addEventListener('abort', () => {
              clearTimeout(timer);
              resolve();
            }, { once: true });
          });
        },
        buildHeaders: () => ({
          Authorization: 'DPoP token', DPoP: 'proof', 'X-Pulse-Enrollment': 'enrollment-1',
        }),
        fetch: async () => { networkCalls++; return new Response('{}'); },
        transportFactory: (_resource, remoteFetch) => remoteFetch,
        clientFactory: () => ({
          async connect(nextTransport) { transport = nextTransport; },
          async callTool(_request, _schema, { signal }) {
            await transport('https://pulse.example.test/mcp', { method: 'POST', signal });
            return { content: [{ type: 'text', text: '{"ok":true}' }] };
          },
          async close() {},
        }),
      }),
      /team_remote_operation_timeout/,
    );
    assert.ok(Date.now() - started < 100, `refresh timeout exceeded operation bound: ${Date.now() - started}ms`);
    await new Promise((resolve) => setTimeout(resolve, 150));
    assert.equal(refreshSignal?.aborted, true);
    assert.equal(lateMutations, 0);
    assert.equal(networkCalls, 0);
  } finally {
    await resetTeamRemoteSessionsForTests();
  }
});

test('failed distinct session connects do not retain credential stores past the session cap', async () => {
  await resetTeamRemoteSessionsForTests();
  let stores = 0;
  let closes = 0;
  const failedBinding = (index) => binding({
    resource: `https://failed-${index}.example.test/mcp`,
    credential_ref: `keychain:pulse/team/failed-${index}`,
    team_id: `team_failed_${index}`,
    store_id: `store_failed_${index}`,
  });
  const options = {
    credentialStoreFactory: () => {
      stores++;
      return {
        get: () => JSON.stringify({
          enrollment: { generation: 1 },
          installation: { keyThumbprint: 'j'.repeat(43) },
        }),
      };
    },
    clientFactory: () => ({
      async connect() { throw new Error('synthetic connect failure'); },
      async callTool() { throw new Error('must not call'); },
      async close() { closes++; },
    }),
    transportFactory: () => ({}),
  };
  try {
    for (let index = 0; index < 9; index++) {
      await assert.rejects(
        callTeamRemoteTool(failedBinding(index), 'pulse_team_status', {}, options),
        /team_remote_unavailable/,
      );
    }
    assert.equal(stores, 9);
    await assert.rejects(
      callTeamRemoteTool(failedBinding(0), 'pulse_team_status', {}, options),
      /team_remote_unavailable/,
    );
    assert.equal(stores, 10, 'failed prefix retained its credential store');
    assert.equal(closes, 10);
  } finally {
    await resetTeamRemoteSessionsForTests();
  }
});

test('refresh reauth marker remains an actionable closed Team error', async () => {
  await resetTeamRemoteSessionsForTests();
  try {
    await assert.rejects(
      callTeamRemoteTool(binding(), 'pulse_team_status', {}, {
        credentialStore: {
          get() { throw new RemoteAuthError('refresh_reauth_required'); },
        },
        clientFactory: () => { throw new Error('must not create client'); },
      }),
      (error) => error instanceof TeamRemoteClientError && error.code === 'reauth_required' &&
        error.message === 'team_remote_reauth_required',
    );
  } finally {
    await resetTeamRemoteSessionsForTests();
  }
});

test('blocked credential lock release obeys the request abort instead of wedging refresh', async () => {
  const controller = new AbortController();
  let releases = 0;
  let networkCalls = 0;
  const remoteFetch = createTeamRemoteFetch(binding(), {
    credentialStore: {},
    acquireLock: async () => async () => {
      releases++;
      return new Promise(() => {});
    },
    refresh: async () => {},
    buildHeaders: () => ({
      Authorization: 'DPoP token', DPoP: 'proof', 'X-Pulse-Enrollment': 'enrollment-1',
    }),
    fetch: async () => { networkCalls++; return new Response('{}'); },
  });
  const pending = remoteFetch('https://pulse.example.test/mcp', {
    method: 'POST', signal: controller.signal,
  });
  setTimeout(() => controller.abort(new Error('operation deadline')), 20);
  const outcome = await Promise.race([
    pending.then(() => 'resolved', (error) => error),
    new Promise((resolve) => setTimeout(() => resolve('wedged'), 100)),
  ]);
  assert.notEqual(outcome, 'wedged');
  assert.match(String(outcome), /team_remote_aborted/);
  assert.equal(releases, 1);
  assert.equal(networkCalls, 0);
});

test('process Team session cache evicts the least-recently-used binding at its fixed cap', async () => {
  await resetTeamRemoteSessionsForTests();
  let closes = 0;
  const credentialStore = {
    get: () => JSON.stringify({
      enrollment: { generation: 1 },
      installation: { keyThumbprint: 'f'.repeat(43) },
    }),
  };
  const options = {
    credentialStore,
    clientFactory: () => ({
      async connect() {},
      async callTool() { return { content: [{ type: 'text', text: '{"ok":true}' }] }; },
      async close() { closes++; },
    }),
    transportFactory: () => ({}),
  };
  try {
    for (let index = 0; index < 9; index++) {
      await callTeamRemoteTool(binding({
        resource: `https://pulse-${index}.example.test/mcp`,
        credential_ref: `keychain:pulse/team/${index}`,
        team_id: `team_${index}`,
        store_id: `store_${index}`,
      }), 'pulse_team_status', {}, options);
    }
    assert.equal(closes, 1);
  } finally {
    await resetTeamRemoteSessionsForTests();
  }
});

test('structured MCP domain error remains available to the installed agent proxy', async () => {
  await resetTeamRemoteSessionsForTests();
  const credentialStore = {
    get: () => JSON.stringify({
      enrollment: { generation: 1 },
      installation: { keyThumbprint: 'b'.repeat(43) },
    }),
  };
  try {
    await assert.rejects(
      callTeamRemoteTool(binding(), 'pulse_team_recall', {}, {
        credentialStore,
        clientFactory: () => ({
          async connect() {},
          async callTool() {
            return {
              isError: true,
              content: [{ type: 'text', text: '{"error":"projection_unavailable","fallback":false}' }],
            };
          },
          async close() {},
        }),
        transportFactory: () => ({}),
      }),
      (error) => error instanceof TeamRemoteDomainError &&
        error.domainCode === 'projection_unavailable' && error.toolResult.isError === true,
    );
  } finally {
    await resetTeamRemoteSessionsForTests();
  }
});

test('a hanging Commons MCP fetch is aborted at the bounded network deadline', async () => {
  let aborted = false;
  const remoteFetch = createTeamRemoteFetch(binding(), {
    credentialStore: {},
    acquireLock: async () => async () => {},
    refresh: async () => {},
    buildHeaders: () => ({
      Authorization: 'DPoP token', DPoP: 'proof', 'X-Pulse-Enrollment': 'enrollment-1',
    }),
    networkTimeoutMs: 20,
    fetch: async (_url, init) => new Promise((_resolve, reject) => {
      init.signal.addEventListener('abort', () => {
        aborted = true;
        reject(init.signal.reason);
      }, { once: true });
    }),
  });
  await assert.rejects(
    remoteFetch('https://pulse.example.test/mcp', { method: 'POST' }),
    /team_remote_network_timeout/,
  );
  assert.equal(aborted, true);
});
