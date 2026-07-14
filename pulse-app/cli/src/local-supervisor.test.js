import assert from 'node:assert/strict';
import { chmodSync, mkdtempSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  SupervisorError,
  inspectVaultRuntime,
  startVaultRuntime,
  stopVaultRuntime,
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

  t.after(() => {
    try { stopVaultRuntime(runtime); } catch { /* already stopped */ }
  });
  const started = await startVaultRuntime(runtime, { daemonPath: daemon, timeoutMs: 5000 });
  assert.equal(started.status, 'running');
  assert.equal(started.fallback, false);
  assert.equal(inspectVaultRuntime(runtime).status, 'running');
  assert.equal(stopVaultRuntime(runtime).status, 'stopped');
});
