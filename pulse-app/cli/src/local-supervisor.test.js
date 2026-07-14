import assert from 'node:assert/strict';
import { chmodSync, existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  SupervisorError,
	assertVaultRuntimeHealthy,
	inspectVaultRuntime,
	startVaultRuntime,
	stopVaultRuntime,
	stopVaultRuntimeAndWait,
  vaultRuntimeFromBinding,
} from './local-supervisor.js';

function binding(mode, root) {
  const common = {
    binding_id: 'binding_demo', binding_digest: 'a'.repeat(64), resolver_epoch: 3, fallback: false,
  };
  if (mode === 'personal') {
    return {
      ...common, mode,
      personal: {
        store_id: 'store_personal_nik', data_dir: join(root, 'vaults', 'personal'),
        cache_dir: join(root, 'caches', 'personal'), base_url: 'http://127.0.0.1:18800',
      },
    };
  }
  return {
    ...common, mode: 'team',
    desk: {
      store_id: 'store_desk_nik_zbs', data_dir: join(root, 'vaults', 'desk'),
      cache_dir: join(root, 'caches', 'desk'), base_url: 'http://127.0.0.1:18801',
    },
    commons: { store_id: 'store_commons_zbs', resource: 'https://pulse.example.test' },
  };
}

test('supervisor derives physically separate Personal and Desk process descriptors', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-supervisor.'));
  const personal = vaultRuntimeFromBinding(binding('personal', root));
  const desk = vaultRuntimeFromBinding(binding('team', root));
  assert.equal(personal.runtime_mode, 'personal-local');
  assert.equal(desk.runtime_mode, 'desk-local');
  assert.notEqual(personal.data_dir, desk.data_dir);
  assert.notEqual(personal.cache_dir, desk.cache_dir);
  assert.notEqual(personal.addr, desk.addr);
  assert.equal(personal.fallback, false);
  assert.equal(desk.fallback, false);
  assert.equal(inspectVaultRuntime(personal).status, 'stopped');
});

test('supervisor rejects fallback and non-numeric-loopback runtime descriptors', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-supervisor-invalid.'));
  const fallback = binding('team', root);
  fallback.fallback = true;
  assert.throws(
    () => vaultRuntimeFromBinding(fallback),
    (error) => error instanceof SupervisorError && error.code === 'vault_runtime_invalid',
  );
  const localhost = binding('personal', root);
  localhost.personal.base_url = 'http://localhost:18800';
  assert.throws(() => vaultRuntimeFromBinding(localhost), SupervisorError);
});

async function freePort() {
  const server = createServer();
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  const { port } = server.address();
  await new Promise((resolve) => server.close(resolve));
  return port;
}

function writeFakeDaemon(path, marker) {
  writeFileSync(path, `#!${process.execPath}
// ${marker}
import { mkdirSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
const value = (name) => process.argv[process.argv.indexOf(name) + 1];
const dataDir = value('-data-dir');
const [host, port] = value('-addr').split(':');
mkdirSync(dataDir, { recursive: true, mode: 0o700 });
writeFileSync(dataDir + '/secret.key', 'a'.repeat(64), { mode: 0o600 });
const server = createServer((request, response) => {
  if (request.url === '/health' && request.headers['x-pulse-key'] === 'a'.repeat(64)) {
    response.end('{"status":"ok"}'); return;
  }
  response.statusCode = 401; response.end('denied');
});
server.listen(Number(port), host);
process.on('SIGTERM', () => server.close(() => process.exit(0)));
`, { mode: 0o700 });
  chmodSync(path, 0o700);
}

function writeUnhealthyDaemon(path) {
  writeFileSync(path, `#!${process.execPath}
import { mkdirSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
const value = (name) => process.argv[process.argv.indexOf(name) + 1];
const dataDir = value('-data-dir');
const [host, port] = value('-addr').split(':');
mkdirSync(dataDir, { recursive: true, mode: 0o700 });
writeFileSync(dataDir + '/secret.key', 'a'.repeat(64), { mode: 0o600 });
const server = createServer((_request, response) => {
  response.statusCode = 503; response.end('not ready');
});
server.listen(Number(port), host);
process.on('SIGTERM', () => setTimeout(() => server.close(() => process.exit(0)), 200));
`, { mode: 0o700 });
  chmodSync(path, 0o700);
}

test('supervisor launches and stops one exact bound local process without fallback', async (t) => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-supervisor-process.'));
  const port = await freePort();
  const selected = binding('personal', root);
  selected.personal.base_url = `http://127.0.0.1:${port}`;
  const runtime = vaultRuntimeFromBinding(selected);
  const daemon = join(root, 'fake-pulse-daemon.mjs');
  writeFileSync(daemon, `#!${process.execPath}
import { mkdirSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
const value = (name) => process.argv[process.argv.indexOf(name) + 1];
const dataDir = value('-data-dir');
const [host, port] = value('-addr').split(':');
mkdirSync(dataDir, { recursive: true, mode: 0o700 });
writeFileSync(dataDir + '/secret.key', 'a'.repeat(64), { mode: 0o600 });
writeFileSync(dataDir + '/authority.json', JSON.stringify({
  binding_digest: process.env.PULSE_BINDING_DIGEST,
  policy_epoch: process.env.PULSE_POLICY_EPOCH,
  resolver_epoch: process.env.PULSE_RESOLVER_EPOCH,
}), { mode: 0o600 });
const server = createServer((request, response) => {
  if (request.url === '/health' && request.headers['x-pulse-key'] === 'a'.repeat(64)) {
    response.end('{"status":"ok"}');
    return;
  }
  response.statusCode = 401;
  response.end('denied');
});

server.listen(Number(port), host);
process.on('SIGTERM', () => server.close(() => process.exit(0)));
`, { mode: 0o700 });
  chmodSync(daemon, 0o700);

	t.after(async () => {
		try { await stopVaultRuntimeAndWait(runtime); } catch { /* already stopped */ }
  });
  const started = await startVaultRuntime(runtime, { daemonPath: daemon, timeoutMs: 5000 });
  assert.equal(started.status, 'running');
  assert.equal(started.fallback, false);
  assert.deepEqual(JSON.parse(readFileSync(join(runtime.data_dir, 'authority.json'), 'utf8')), {
    binding_digest: 'a'.repeat(64), policy_epoch: '0', resolver_epoch: '3',
  });
	assert.equal(inspectVaultRuntime(runtime).status, 'running');
	assert.equal(stopVaultRuntime(runtime).status, 'stopping');
	assert.equal(existsSync(runtime.pid_file), true, 'stop receipt must survive until the daemon exits');
	assert.equal((await stopVaultRuntimeAndWait(runtime)).status, 'stopped');
	assert.equal(existsSync(runtime.pid_file), false);
});

test('supervisor recovers an exact crashed receipt and atomically restarts on daemon upgrade', async (t) => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-supervisor-recovery.'));
  const port = await freePort();
  const selected = binding('personal', root);
  selected.personal.base_url = `http://127.0.0.1:${port}`;
  const runtime = vaultRuntimeFromBinding(selected);
  const daemonA = join(root, 'pulse-daemon-a.mjs');
  const daemonB = join(root, 'pulse-daemon-b.mjs');
  writeFakeDaemon(daemonA, 'daemon-a');
  writeFakeDaemon(daemonB, 'daemon-b');
	t.after(async () => {
		try { await stopVaultRuntimeAndWait(runtime); } catch { /* already stopped */ }
  });

  const first = await startVaultRuntime(runtime, { daemonPath: daemonA, timeoutMs: 5000 });
  const firstReceipt = JSON.parse(readFileSync(runtime.pid_file, 'utf8'));
  process.kill(first.pid, 'SIGKILL');
  const deadline = Date.now() + 3000;
  while (inspectVaultRuntime(runtime).status !== 'crashed' && Date.now() < deadline) {
    await new Promise((resolveDelay) => setTimeout(resolveDelay, 25));
  }
  assert.equal(inspectVaultRuntime(runtime).status, 'crashed');
  const recovered = await startVaultRuntime(runtime, { daemonPath: daemonA, timeoutMs: 5000 });
  assert.notEqual(recovered.pid, first.pid);

  const upgraded = await startVaultRuntime(runtime, { daemonPath: daemonB, timeoutMs: 5000 });
  const upgradedReceipt = JSON.parse(readFileSync(runtime.pid_file, 'utf8'));
  assert.notEqual(upgraded.pid, recovered.pid);
  assert.notEqual(upgradedReceipt.executable_digest, firstReceipt.executable_digest);
  assert.equal(inspectVaultRuntime(runtime).executable.endsWith('/pulse-daemon-b.mjs'), true);
});

test('failed daemon upgrade waits for the new process to exit and restores the healthy previous daemon', async (t) => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-supervisor-rollback.'));
  const port = await freePort();
  const selected = binding('personal', root);
  selected.personal.base_url = `http://127.0.0.1:${port}`;
  const runtime = vaultRuntimeFromBinding(selected);
  const daemonA = join(root, 'pulse-daemon-a.mjs');
  const daemonB = join(root, 'pulse-daemon-unhealthy.mjs');
  writeFakeDaemon(daemonA, 'daemon-a');
  writeUnhealthyDaemon(daemonB);
	t.after(async () => {
		try { await stopVaultRuntimeAndWait(runtime); } catch { /* already stopped */ }
  });

  await startVaultRuntime(runtime, { daemonPath: daemonA, timeoutMs: 5000 });
  await assert.rejects(
    startVaultRuntime(runtime, { daemonPath: daemonB, timeoutMs: 350 }),
    (error) => error instanceof SupervisorError && error.code === 'vault_start_timeout',
  );
  const restored = await assertVaultRuntimeHealthy(runtime, { timeoutMs: 3000 });
	assert.equal(restored.executable.endsWith('/pulse-daemon-a.mjs'), true);
});
