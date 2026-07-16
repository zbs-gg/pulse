import assert from 'node:assert/strict';
import {
  chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, statSync, writeFileSync,
} from 'node:fs';
import { createHash } from 'node:crypto';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  SupervisorError,
	assertVaultRuntimeHealthy,
	inspectVaultRuntime,
	resolveManagedRuntime,
	startVaultRuntime,
	stopVaultRuntime,
	stopVaultRuntimeAndWait,
  vaultRuntimeFromBinding,
} from './local-supervisor.js';
import { activateArtifactVersion, readActivatedArtifact } from './artifact-installer.js';

function sha256(bytes) {
  return createHash('sha256').update(bytes).digest('hex');
}

function treeEntry(path, bytes, mode) {
  return { path, bytes: bytes.length, sha256: sha256(bytes), mode, executable: (mode & 0o111) !== 0 };
}

function artifact(id, kind, sha, modelPolicy = null) {
  return {
    id, kind, version: '0.8.0', epoch: 8, bytes: 1, sha256: sha,
    origin: 'https://releases.zbs.gg', url: `https://releases.zbs.gg/${id}`,
    ...(kind === 'model' ? { model_policy: modelPolicy ?? { data_only: true, custom_code: false } } : {}),
  };
}

function safetensorsFixture() {
  const header = Buffer.from(JSON.stringify({ embedding: { dtype: 'F32', shape: [1], data_offsets: [0, 4] } }));
  const prefix = Buffer.alloc(8);
  prefix.writeBigUInt64LE(BigInt(header.length));
  return Buffer.concat([prefix, header, Buffer.alloc(4)]);
}

async function activateManagedRuntimeFixtures(root) {
  const installRoot = join(root, 'artifacts');
  const daemon = Buffer.from('#!/bin/sh\nexit 0\n');
  const python = Buffer.from('#!/bin/sh\nexit 0\n');
  const helper = Buffer.from('managed helper fixture\n');
  const config = Buffer.from('{}\n');
  const tokenizer = Buffer.from('{}\n');
  const model = safetensorsFixture();
  const fixtures = [
    {
      descriptor: artifact('pulse-daemon', 'daemon', '1'.repeat(64)),
      files: [['bin/pulse', daemon, 0o700]],
    },
    {
      descriptor: artifact('pulse-embedder-runtime', 'embedder-runtime', '2'.repeat(64)),
      files: [
        ['runtime/bin/python3.12', python, 0o700], ['helper.py', helper, 0o600],
        ['support/config.json', config, 0o600], ['support/tokenizer.json', tokenizer, 0o600],
      ],
    },
    {
      descriptor: artifact('pulse-model', 'model', '3'.repeat(64)),
      files: [['model.safetensors', model, 0o600]],
    },
  ];
  for (const fixture of fixtures) {
    const treeManifest = {
      schema: 'pulse.artifact_tree.v1',
      files: fixture.files.map(([path, bytes, mode]) => treeEntry(path, bytes, mode)),
    };
    await activateArtifactVersion(fixture.descriptor, join(root, 'unused'), {
      installRoot,
      treeManifest,
      testOnlyMaterializer: true,
      materialize: async (_staged, target) => {
        for (const [path, bytes, mode] of fixture.files) {
          const destination = join(target, path);
          mkdirSync(join(destination, '..'), { recursive: true, mode: 0o700 });
          writeFileSync(destination, bytes, { mode });
          chmodSync(destination, mode);
        }
      },
    });
  }
  return {
    installRoot,
    verifiedActivations: {
      daemon: readActivatedArtifact('pulse-daemon', { installRoot, expectedKind: 'daemon' }),
      embedderRuntime: readActivatedArtifact('pulse-embedder-runtime', { installRoot, expectedKind: 'embedder-runtime' }),
      model: readActivatedArtifact('pulse-model', { installRoot, expectedKind: 'model' }),
    },
  };
}

test('managed product runtime resolves three verified activations and atomically pins a private embedder config', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-managed-runtime.'));
  const runtime = vaultRuntimeFromBinding(binding('personal', root));
  const { installRoot, verifiedActivations } = await activateManagedRuntimeFixtures(root);

  const managed = resolveManagedRuntime(runtime, { installRoot, verifiedActivations });

  assert.equal(managed.schema, 'pulse.managed_product_runtime.v1');
  assert.equal(managed.daemon.artifact_id, 'pulse-daemon');
  assert.equal(managed.embedder_runtime.artifact_id, 'pulse-embedder-runtime');
  assert.equal(managed.model.artifact_id, 'pulse-model');
  assert.equal(managed.daemon.path.endsWith('/bin/pulse'), true);
  assert.equal(managed.managed_embedder.config_path, join(runtime.data_dir, 'runtime', 'managed-embedder.json'));
  assert.match(managed.managed_embedder.config_digest, /^[a-f0-9]{64}$/);
  assert.equal(statSync(managed.managed_embedder.config_path).mode & 0o777, 0o600);
  const disk = JSON.parse(readFileSync(managed.managed_embedder.config_path, 'utf8'));
  assert.equal(disk.schema, 'pulse.managed_embedder.config.v1');
  assert.equal(disk.python_executable.endsWith('/runtime/bin/python3.12'), true);
  assert.equal(disk.helper_path.endsWith('/helper.py'), true);
  assert.equal(disk.model_file.endsWith('/model.safetensors'), true);
  assert.equal(disk.dimensions, 1024);
  assert.equal(disk.normalized, true);
});

function binding(mode, root) {
  const common = {
    binding_id: 'binding_demo', binding_digest: 'a'.repeat(64), resolver_epoch: 3, fallback: false,
		workspace: { repository_id: 'repository_pulse' },
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
	assert.equal(personal.repository_id, 'repository_pulse');
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
try {
  writeFileSync(dataDir + '/secret.key', 'a'.repeat(64), { mode: 0o600, flag: 'wx' });
} catch (error) {
  if (error?.code !== 'EEXIST') throw error;
}
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

function writeManagedSmokeDaemon(path) {
  writeFileSync(path, `#!${process.execPath}
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
const value = (name) => process.argv[process.argv.indexOf(name) + 1];
const dataDir = value('-data-dir');
const [host, port] = value('-addr').split(':');
mkdirSync(dataDir, { recursive: true, mode: 0o700 });
writeFileSync(dataDir + '/secret.key', 'a'.repeat(64), { mode: 0o600 });
writeFileSync(dataDir + '/embedder-env.json', JSON.stringify({
  managed: process.env.PULSE_MANAGED_EMBEDDER_CONFIG,
  python: process.env.PULSE_LOCAL_EMBED_PYTHON,
  helper: process.env.PULSE_LOCAL_EMBED_HELPER,
  model: process.env.PULSE_LOCAL_EMBED_MODEL,
  cohere: process.env.COHERE_API_KEY,
}), { mode: 0o600 });
writeFileSync(dataDir + '/retrieval-count.txt', '0', { mode: 0o600 });
const server = createServer((request, response) => {
  if (request.headers['x-pulse-key'] !== 'a'.repeat(64)) { response.statusCode = 401; response.end('denied'); return; }
  response.setHeader('content-type', 'application/json');
  if (request.url === '/health') { response.end('{"status":"ok"}'); return; }
  if (request.url === '/memory/status') { response.end('{"full_retrieval":true,"embedder":"bge-m3"}'); return; }
  if (request.url === '/retrieve' && request.method === 'POST') {
    const count = Number(readFileSync(dataDir + '/retrieval-count.txt', 'utf8')) + 1;
    writeFileSync(dataDir + '/retrieval-count.txt', String(count), { mode: 0o600 });
    request.resume(); request.on('end', () => response.end('{"event_ids":[]}')); return;
  }
  response.statusCode = 404; response.end('{}');
});
server.listen(Number(port), host);
process.on('SIGTERM', () => server.close(() => process.exit(0)));
`, { mode: 0o700 });
  chmodSync(path, 0o700);
}

test('managed launch passes one private config, clears public embedder env, and requires a real retrieval query', async (t) => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-supervisor-managed-process.'));
  const port = await freePort();
  const selected = binding('personal', root);
  selected.personal.base_url = `http://127.0.0.1:${port}`;
  const runtime = vaultRuntimeFromBinding(selected);
  const { installRoot, verifiedActivations } = await activateManagedRuntimeFixtures(root);
  const managed = resolveManagedRuntime(runtime, { installRoot, verifiedActivations });
  const daemon = join(root, 'managed-smoke-daemon.mjs');
  writeManagedSmokeDaemon(daemon);
  const old = {
    python: process.env.PULSE_LOCAL_EMBED_PYTHON,
    helper: process.env.PULSE_LOCAL_EMBED_HELPER,
    model: process.env.PULSE_LOCAL_EMBED_MODEL,
    cohere: process.env.COHERE_API_KEY,
  };
  Object.assign(process.env, {
    PULSE_LOCAL_EMBED_PYTHON: '/tmp/forbidden-python',
    PULSE_LOCAL_EMBED_HELPER: '/tmp/forbidden-helper',
    PULSE_LOCAL_EMBED_MODEL: '/tmp/forbidden-model',
    COHERE_API_KEY: 'must-not-cross-process-boundary',
  });
  t.after(async () => {
    for (const [name, value] of Object.entries({
      PULSE_LOCAL_EMBED_PYTHON: old.python, PULSE_LOCAL_EMBED_HELPER: old.helper,
      PULSE_LOCAL_EMBED_MODEL: old.model, COHERE_API_KEY: old.cohere,
    })) {
      if (value === undefined) delete process.env[name]; else process.env[name] = value;
    }
    try { await stopVaultRuntimeAndWait(runtime); } catch { /* already stopped */ }
  });

  await startVaultRuntime(runtime, {
    daemonPath: daemon, managedEmbedder: managed.managed_embedder, timeoutMs: 5000,
  });
  assert.deepEqual(JSON.parse(readFileSync(join(runtime.data_dir, 'embedder-env.json'), 'utf8')), {
    managed: managed.managed_embedder.config_path,
    python: '', helper: '', model: '', cohere: '',
  });
  const receipt = JSON.parse(readFileSync(runtime.pid_file, 'utf8'));
  assert.equal(receipt.managed_embedder_config_digest, managed.managed_embedder.config_digest);
	assert.equal(readFileSync(join(runtime.data_dir, 'retrieval-count.txt'), 'utf8'), '1');
	const running = inspectVaultRuntime(runtime);
	assert.equal((await assertVaultRuntimeHealthy(runtime, {
		status: running, fullRetrievalSmoke: false,
	})).status, 'running');
	assert.equal(readFileSync(join(runtime.data_dir, 'retrieval-count.txt'), 'utf8'), '1');
  assert.equal((await assertVaultRuntimeHealthy(runtime)).status, 'running');
	assert.equal(readFileSync(join(runtime.data_dir, 'retrieval-count.txt'), 'utf8'), '2');
});

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
	repository_id: process.env.PULSE_REPOSITORY_ID,
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
		binding_digest: 'a'.repeat(64), repository_id: 'repository_pulse', policy_epoch: '0', resolver_epoch: '3',
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

test('supervisor safely restarts a verified legacy receipt into repository authority and rejects wrong present authority', async (t) => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-supervisor-legacy-authority.'));
  const port = await freePort();
  const selected = binding('personal', root);
  selected.personal.base_url = `http://127.0.0.1:${port}`;
  const runtime = vaultRuntimeFromBinding(selected);
  const daemon = join(root, 'pulse-daemon.mjs');
  writeFakeDaemon(daemon, 'legacy-authority');
  t.after(async () => {
    try { await stopVaultRuntimeAndWait(runtime); } catch { /* already stopped */ }
  });

  const original = await startVaultRuntime(runtime, { daemonPath: daemon, timeoutMs: 5000 });
  const legacyReceipt = JSON.parse(readFileSync(runtime.pid_file, 'utf8'));
  delete legacyReceipt.repository_id;
  writeFileSync(runtime.pid_file, JSON.stringify(legacyReceipt), { mode: 0o600 });
  const legacy = inspectVaultRuntime(runtime);
  assert.equal(legacy.status, 'running');
  assert.equal(legacy.legacy_authority, true);

  const adopted = await startVaultRuntime(runtime, { daemonPath: daemon, timeoutMs: 5000 });
  assert.notEqual(adopted.pid, original.pid);
  const adoptedReceipt = JSON.parse(readFileSync(runtime.pid_file, 'utf8'));
  assert.equal(adoptedReceipt.repository_id, runtime.repository_id);
  assert.equal(inspectVaultRuntime(runtime).legacy_authority, false);

  writeFileSync(runtime.pid_file, JSON.stringify({
    ...adoptedReceipt, repository_id: 'repository_wrong',
  }), { mode: 0o600 });
  assert.equal(inspectVaultRuntime(runtime).status, 'stale_or_mismatched');
  await assert.rejects(
    startVaultRuntime(runtime, { daemonPath: daemon, timeoutMs: 5000 }),
    (error) => error instanceof SupervisorError && error.code === 'vault_runtime_receipt_mismatch',
  );
  writeFileSync(runtime.pid_file, JSON.stringify(adoptedReceipt), { mode: 0o600 });
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
