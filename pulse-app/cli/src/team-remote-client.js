import { AsyncLocalStorage } from 'node:async_hooks';
import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, lstatSync, mkdirSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';

import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';

import {
  buildSenderConstrainedRemoteHeaders,
  createOSCredentialStore,
  refreshRemoteCredential,
} from './remote-auth.js';
import { RemoteAuthError } from './remote-auth-errors.js';
import { boundedRemoteFetch } from './remote-auth-network.js';

const DEFAULT_NETWORK_TIMEOUT_MS = 10_000;
const DEFAULT_OPERATION_TIMEOUT_MS = 20_000;
const MIN_TIMEOUT_MS = 10;
const MAX_NETWORK_TIMEOUT_MS = 30_000;
const MAX_OPERATION_TIMEOUT_MS = 60_000;
const MAX_PROCESS_TEAM_SESSIONS = 8;
const teamOperationContext = new AsyncLocalStorage();

const READ_ONLY_TEAM_TOOLS = new Set([
  'pulse_team_status',
  'pulse_team_recall',
  'pulse_team_context_query',
  'pulse_team_resume',
  'pulse_team_inspect',
]);

export class TeamRemoteClientError extends Error {
  constructor(code) {
    super(`team_remote_${code}`);
    this.name = 'TeamRemoteClientError';
    this.code = code;
  }
}

export class TeamRemoteDomainError extends TeamRemoteClientError {
  constructor(domainCode, toolResult) {
    super('domain_error');
    this.name = 'TeamRemoteDomainError';
    this.domainCode = domainCode;
    this.toolResult = toolResult;
  }
}

function fail(code) {
  throw new TeamRemoteClientError(code);
}

function boundedTimeout(value, maximum) {
  if (!Number.isInteger(value) || value < MIN_TIMEOUT_MS || value > maximum) fail('timeout_invalid');
  return value;
}

function operationTimeoutError() {
  return new TeamRemoteClientError('operation_timeout');
}

function assertOperationActive(operation = teamOperationContext.getStore()) {
  if (!operation) return undefined;
  if (operation.signal?.aborted || Date.now() >= operation.deadlineAt) {
    throw operation.signal?.reason instanceof TeamRemoteClientError
      ? operation.signal.reason
      : operationTimeoutError();
  }
  return operation;
}

function remainingOperationTimeout(defaultTimeout, operation = teamOperationContext.getStore()) {
  if (!operation) return boundedTimeout(defaultTimeout, MAX_NETWORK_TIMEOUT_MS);
  assertOperationActive(operation);
  const remaining = Math.ceil(operation.deadlineAt - Date.now());
  if (remaining < MIN_TIMEOUT_MS) throw operationTimeoutError();
  return boundedTimeout(Math.min(defaultTimeout, remaining), MAX_NETWORK_TIMEOUT_MS);
}

function assertRequestActive(operation, signal) {
  if (signal?.aborted) {
    if (signal.reason instanceof TeamRemoteClientError) throw signal.reason;
    fail('aborted');
  }
  return assertOperationActive(operation);
}

async function boundedOperationWait(work, { signal, deadlineAt, onAbort } = {}) {
  const pending = Promise.resolve(work);
  pending.catch(() => {});
  let abortListener;
  let timer;
  const contenders = [pending];
  if (signal) {
    contenders.push(new Promise((_resolve, reject) => {
      abortListener = () => {
        try { onAbort?.(); } catch { /* deadline remains authoritative */ }
        reject(signal.reason instanceof TeamRemoteClientError
          ? signal.reason
          : new TeamRemoteClientError('aborted'));
      };
      if (signal.aborted) abortListener();
      else signal.addEventListener('abort', abortListener, { once: true });
    }));
  }
  if (deadlineAt !== undefined) {
    const remaining = Math.ceil(deadlineAt - Date.now());
    contenders.push(new Promise((_resolve, reject) => {
      timer = setTimeout(() => {
        try { onAbort?.(); } catch { /* deadline remains authoritative */ }
        reject(operationTimeoutError());
      }, Math.max(0, remaining));
    }));
  }
  try {
    return await Promise.race(contenders);
  } finally {
    clearTimeout(timer);
    if (signal && abortListener) signal.removeEventListener('abort', abortListener);
  }
}

async function releaseCredentialLock(release, operation, signal) {
  const pending = Promise.resolve().then(release);
  return boundedOperationWait(pending, {
    signal,
    deadlineAt: operation?.deadlineAt,
  });
}

function exactTeamBinding(binding) {
  if (!binding || binding.mode !== 'team' || binding.fallback !== false || !binding.commons || !binding.desk) {
    fail('binding_required');
  }
  if (typeof binding.commons.credential_ref !== 'string' || binding.commons.credential_ref === '') {
    fail('credential_ref_required');
  }
  let resource;
  try { resource = new URL(binding.commons.resource); } catch { fail('resource_invalid'); }
  if (resource.protocol !== 'https:' || resource.username || resource.password || resource.search || resource.hash ||
      resource.pathname !== '/mcp') {
    fail('resource_invalid');
  }
  return { resource: resource.toString(), credentialRef: binding.commons.credential_ref };
}

function privateLockDirectory(path) {
  mkdirSync(path, { recursive: true, mode: 0o700 });
  const info = lstatSync(path);
  const uid = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
  if (!info.isDirectory() || info.isSymbolicLink() || info.uid !== uid || (info.mode & 0o077) !== 0) {
    fail('lock_directory_unsafe');
  }
}

export async function acquireRemoteCredentialLock(credentialRef, {
  lockRoot = join(homedir(), '.pulse', 'remote-auth-locks'),
  timeoutSeconds = 5,
  signal,
  deadlineAt,
} = {}) {
  if (typeof credentialRef !== 'string' || credentialRef === '' || credentialRef.length > 512 ||
      !Number.isInteger(timeoutSeconds) || timeoutSeconds < 1 || timeoutSeconds > 30) {
    fail('lock_configuration_invalid');
  }
  if (!existsSync('/usr/bin/lockf')) fail('lock_unavailable');
  privateLockDirectory(lockRoot);
  const digest = createHash('sha256').update('pulse-remote-credential-lock-v1\0').update(credentialRef).digest('hex');
  const lockPath = join(lockRoot, `${digest}.lock`);
  const helper = 'process.stdout.write("ready\\n");process.stdin.resume();process.stdin.on("end",()=>process.exit(0));';
  const child = spawn('/usr/bin/lockf', [
    '-k', '-t', String(timeoutSeconds), lockPath, process.execPath, '-e', helper,
  ], { stdio: ['pipe', 'pipe', 'pipe'] });
  const childExit = new Promise((resolve) => child.once('exit', resolve));
  await new Promise((resolve, reject) => {
    let settled = false;
    const finish = (action, value) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      signal?.removeEventListener('abort', abortListener);
      action(value);
    };
    const abortListener = () => {
      child.kill('SIGTERM');
      finish(reject, new TeamRemoteClientError('aborted'));
    };
    const timer = setTimeout(() => {
      child.kill('SIGTERM');
      finish(reject, new TeamRemoteClientError('lock_timeout'));
    }, (timeoutSeconds + 2) * 1000);
    if (signal?.aborted) {
      abortListener();
      return;
    }
    signal?.addEventListener('abort', abortListener, { once: true });
    child.stdout.setEncoding('utf8');
    child.stdout.once('data', (value) => {
      if (String(value).includes('ready')) finish(resolve);
      else finish(reject, new TeamRemoteClientError('lock_invalid'));
    });
    child.once('error', () => {
      finish(reject, new TeamRemoteClientError('lock_unavailable'));
    });
    child.once('exit', (status) => {
      finish(reject, new TeamRemoteClientError(status === 75 ? 'lock_timeout' : 'lock_failed'));
    });
  });
  let released = false;
  return async () => {
    if (released) return;
    released = true;
    child.stdin.end();
    await boundedOperationWait(childExit, {
      signal,
      deadlineAt,
      onAbort: () => child.kill('SIGTERM'),
    });
  };
}

function requestURL(input) {
  if (input instanceof URL) return input.toString();
  if (typeof input === 'string') return input;
  if (input && typeof input.url === 'string') return input.url;
  fail('request_invalid');
}

function requestMethod(input, init) {
  const value = init?.method ?? input?.method ?? 'GET';
  if (typeof value !== 'string' || !['GET', 'POST', 'DELETE'].includes(value.toUpperCase())) fail('method_invalid');
  return value.toUpperCase();
}

export function createTeamRemoteFetch(binding, {
  credentialStore = createOSCredentialStore(),
  fetch: fetchFn = globalThis.fetch,
  refresh = refreshRemoteCredential,
  acquireLock = acquireRemoteCredentialLock,
  buildHeaders = buildSenderConstrainedRemoteHeaders,
  now = () => Math.floor(Date.now() / 1000),
  networkTimeoutMs = DEFAULT_NETWORK_TIMEOUT_MS,
  signal,
} = {}) {
  const trusted = exactTeamBinding(binding);
  if (typeof fetchFn !== 'function' || typeof refresh !== 'function' || typeof acquireLock !== 'function' ||
      typeof buildHeaders !== 'function' || typeof now !== 'function') fail('runtime_unavailable');
  return async (input, init = {}) => {
    const operation = assertOperationActive();
    const requestSignal = init.signal ?? operation?.signal ?? signal;
    assertRequestActive(operation, requestSignal);
    const url = requestURL(input);
    const method = requestMethod(input, init);
    if (url !== trusted.resource) fail('request_target_mismatch');
    const headers = new Headers(input?.headers ?? undefined);
    for (const [name, value] of new Headers(init.headers ?? undefined)) headers.set(name, value);
    for (const name of ['authorization', 'dpop', 'x-pulse-enrollment']) {
      if (headers.has(name)) fail('caller_auth_forbidden');
    }
    const send = async (forceRefresh) => {
      const requestTimeoutMs = remainingOperationTimeout(networkTimeoutMs, operation);
      const release = await acquireLock(trusted.credentialRef, {
        signal: requestSignal,
        deadlineAt: operation?.deadlineAt,
      });
      try {
        try {
          await refresh(credentialStore, trusted.credentialRef, {
            fetch: fetchFn, now: now(), force: forceRefresh,
            networkTimeoutMs: requestTimeoutMs, signal: requestSignal,
            deadlineAt: operation?.deadlineAt,
          });
        } catch (error) {
          if (error instanceof RemoteAuthError && ['network_timeout', 'network_aborted'].includes(error.code)) {
            fail(error.code === 'network_timeout' ? 'network_timeout' : 'aborted');
          }
          throw error;
        }
      } finally {
        await releaseCredentialLock(release, operation, requestSignal);
      }
      assertRequestActive(operation, requestSignal);
      const attemptHeaders = new Headers(headers);
      const senderHeaders = buildHeaders(url, {
        method,
        credentialStore,
        credentialRef: trusted.credentialRef,
        now: now(),
        signal: requestSignal,
        deadlineAt: operation?.deadlineAt,
      });
      assertRequestActive(operation, requestSignal);
      for (const [name, value] of Object.entries(senderHeaders)) attemptHeaders.set(name, value);
      try {
        return await boundedRemoteFetch(fetchFn, url, {
          ...init, method, headers: attemptHeaders, redirect: 'error',
        }, { timeoutMs: remainingOperationTimeout(networkTimeoutMs, operation), signal: requestSignal });
      } catch (error) {
        if (error instanceof RemoteAuthError && error.code === 'network_timeout') fail('network_timeout');
        if (error instanceof RemoteAuthError && error.code === 'network_aborted') fail('aborted');
        throw error;
      }
    };
    const response = await send(false);
    if (response?.status !== 401) return response;
    const challenge = response.headers?.get?.('www-authenticate') ?? '';
    if (!/(?:^|[,\s])pulse_reauth="refresh"(?:[,\s]|$)/.test(challenge)) return response;
    try { Promise.resolve(response.body?.cancel()).catch(() => {}); } catch { /* retry remains bounded and authoritative */ }
    return send(true);
  };
}

function toolJSON(result) {
  if (!result || !Array.isArray(result.content) || result.content.length !== 1 ||
      result.content[0]?.type !== 'text' || typeof result.content[0].text !== 'string') {
    fail('tool_rejected');
  }
  let value;
  try { value = JSON.parse(result.content[0].text); } catch { fail('response_invalid'); }
  if (result.isError === true) {
    if (value && typeof value === 'object' && !Array.isArray(value) && value.fallback === false &&
        typeof value.error === 'string' && /^[a-z][a-z0-9_]{0,127}$/.test(value.error)) {
      throw new TeamRemoteDomainError(value.error, result);
    }
    fail('tool_rejected');
  }
  return value;
}

const teamSessions = new Map();
const teamCredentialStores = new Map();
let sessionSequence = 0;

function credentialGeneration(store, credentialRef, operation) {
  if (!store || typeof store.get !== 'function') fail('credential_store_unavailable');
  const serialized = store.get(credentialRef, {
    signal: operation?.signal,
    deadlineAt: operation?.deadlineAt,
  });
  assertOperationActive(operation);
  if (typeof serialized !== 'string') fail('credential_unavailable');
  let credential;
  try { credential = JSON.parse(serialized); } catch { fail('credential_invalid'); }
  const generation = credential?.enrollment?.generation;
  const thumbprint = credential?.installation?.keyThumbprint;
  if (!Number.isSafeInteger(generation) || generation < 1 ||
      typeof thumbprint !== 'string' || !/^[A-Za-z0-9_-]{43}$/.test(thumbprint)) {
    fail('credential_invalid');
  }
  return `${generation}:${thumbprint}`;
}

function bindingSessionPrefix(binding, trusted) {
  const digest = typeof binding.binding_digest === 'string' && /^[a-f0-9]{64}$/.test(binding.binding_digest)
    ? binding.binding_digest
    : [binding.commons?.team_id, binding.commons?.store_id, binding.principal_ref].join(':');
  return `${trusted.resource}\0${trusted.credentialRef}\0${digest}\0`;
}

async function closeSession(session) {
  if (!session || session.closed) return;
  session.closed = true;
  let timer;
  try {
    let closing;
    try { closing = Promise.resolve(session.client.close()); } catch { closing = Promise.resolve(); }
    await Promise.race([
      closing,
      new Promise((resolve) => { timer = setTimeout(resolve, 250); }),
    ]);
  } catch { /* invalidation is fail-closed */ }
  finally { clearTimeout(timer); }
}

async function sessionFor(binding, trusted, options) {
  const prefix = bindingSessionPrefix(binding, trusted);
  let credentialStore = options.credentialStore;
  if (!credentialStore) {
    credentialStore = teamCredentialStores.get(prefix);
    if (!credentialStore) {
      credentialStore = options.credentialStoreFactory?.() ?? createOSCredentialStore();
      teamCredentialStores.set(prefix, credentialStore);
    }
  }
  const operation = {
    signal: options.operationSignal,
    deadlineAt: options.operationDeadlineAt,
  };
  let generation;
  try {
    generation = credentialGeneration(credentialStore, trusted.credentialRef, operation);
  } catch (error) {
    if (teamCredentialStores.get(prefix) === credentialStore) teamCredentialStores.delete(prefix);
    throw error;
  }
  const key = `${prefix}${generation}`;
  for (const [candidateKey, candidate] of teamSessions) {
    if (candidateKey.startsWith(prefix) && candidateKey !== key) {
      teamSessions.delete(candidateKey);
      await closeSession(candidate);
    }
  }
  const existing = teamSessions.get(key);
  if (existing && !existing.closed) {
    existing.lastUsed = ++sessionSequence;
    await existing.connectPromise;
    if (existing.closed || teamSessions.get(key) !== existing) fail('unavailable');
    return existing;
  }
  while (teamSessions.size >= MAX_PROCESS_TEAM_SESSIONS) {
    const oldest = [...teamSessions.values()].sort((left, right) => left.lastUsed - right.lastUsed)[0];
    teamSessions.delete(oldest.key);
    await closeSession(oldest);
    if (![...teamSessions.values()].some((candidate) => candidate.prefix === oldest.prefix)) {
      teamCredentialStores.delete(oldest.prefix);
    }
  }
  const client = options.clientFactory?.() ?? new Client({ name: 'pulse-installed-compositor', version: '0.7.0' });
  const transport = options.transportFactory?.(trusted.resource, createTeamRemoteFetch(binding, {
    ...options, credentialStore, signal: undefined,
  })) ?? new StreamableHTTPClientTransport(new URL(trusted.resource), {
    fetch: createTeamRemoteFetch(binding, { ...options, credentialStore, signal: undefined }),
  });
  const session = {
    key, prefix, client, transport, credentialStore, closed: false, connected: false,
    connectPromise: null, lastUsed: ++sessionSequence,
  };
  session.connectPromise = Promise.resolve(client.connect(transport, {
    signal: options.operationSignal,
    timeout: Math.max(1, options.operationDeadlineAt - Date.now()),
  })).then(() => { session.connected = true; });
  teamSessions.set(key, session);
  try {
    await session.connectPromise;
    return session;
  } catch (error) {
    teamSessions.delete(key);
    await closeSession(session);
    if (teamCredentialStores.get(prefix) === credentialStore &&
        ![...teamSessions.values()].some((candidate) => candidate.prefix === prefix)) {
      teamCredentialStores.delete(prefix);
    }
    throw error;
  }
}

async function invalidateSession(session) {
  if (!session) return;
  if (teamSessions.get(session.key) === session) teamSessions.delete(session.key);
  await closeSession(session);
}

async function invalidateBindingSessions(binding, trusted) {
  const prefix = bindingSessionPrefix(binding, trusted);
  const matching = [...teamSessions.entries()].filter(([key]) => key.startsWith(prefix));
  for (const [key] of matching) teamSessions.delete(key);
  await Promise.all(matching.map(([, session]) => closeSession(session)));
}

export async function resetTeamRemoteSessionsForTests() {
  const sessions = [...teamSessions.values()];
  teamSessions.clear();
  teamCredentialStores.clear();
  await Promise.all(sessions.map(closeSession));
}

export async function callTeamRemoteTool(binding, name, args, options = {}) {
  if (!READ_ONLY_TEAM_TOOLS.has(name)) fail('write_forbidden');
  const trusted = exactTeamBinding(binding);
  const operationTimeoutMs = boundedTimeout(
    options.operationTimeoutMs ?? DEFAULT_OPERATION_TIMEOUT_MS,
    MAX_OPERATION_TIMEOUT_MS,
  );
  const controller = new AbortController();
  const deadlineAt = Date.now() + operationTimeoutMs;
  let operationTimer;
  const operationTimeout = new Promise((_resolve, reject) => {
    operationTimer = setTimeout(() => {
      const error = operationTimeoutError();
      controller.abort(error);
      reject(error);
    }, operationTimeoutMs);
  });
  let session;
  let operation;
  try {
    operation = teamOperationContext.run({ signal: controller.signal, deadlineAt }, async () => {
      session = await sessionFor(binding, trusted, {
        ...options, operationSignal: controller.signal, operationTimeoutMs,
        operationDeadlineAt: deadlineAt,
      });
      assertOperationActive();
      return toolJSON(await session.client.callTool(
        { name, arguments: args }, undefined, {
          signal: controller.signal,
          timeout: Math.max(1, deadlineAt - Date.now()),
        },
      ));
    });
    return await Promise.race([operation, operationTimeout]);
  } catch (error) {
    if (error instanceof TeamRemoteDomainError) throw error;
    if (controller.signal.aborted || Date.now() >= deadlineAt) {
      operation?.catch(() => {});
      void invalidateBindingSessions(binding, trusted).catch(() => {});
      throw controller.signal.reason instanceof TeamRemoteClientError
        ? controller.signal.reason
        : operationTimeoutError();
    }
    if (error instanceof TeamRemoteClientError) {
      if (error.code === 'operation_timeout') {
        operation?.catch(() => {});
        void invalidateBindingSessions(binding, trusted).catch(() => {});
      }
      throw error;
    }
    if (error instanceof RemoteAuthError && error.code === 'refresh_reauth_required') {
      await invalidateSession(session);
      throw new TeamRemoteClientError('reauth_required');
    }
    await invalidateSession(session);
    fail('unavailable');
  } finally {
    clearTimeout(operationTimer);
  }
}

export function isReadOnlyTeamTool(name) {
  return READ_ONLY_TEAM_TOOLS.has(name);
}
