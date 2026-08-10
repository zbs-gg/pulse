import assert from 'node:assert/strict';
import {
  chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, renameSync, rmSync, statSync, writeFileSync,
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
	inspectManagedEmbedderConfig,
	startVaultRuntime,
	stopVaultRuntime,
	stopVaultRuntimeAndWait,
  vaultRuntimeFromBinding,
} from './local-supervisor.js';
import { activateArtifactVersion, readActivatedArtifact } from './artifact-installer.js';
import { createPlatformServices } from './platform-services.js';

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
  const portableRunner = Buffer.from('#!/bin/sh\nexit 0\n');
  const config = Buffer.from('{}\n');
  const tokenizer = Buffer.from('{}\n');
  const modelContract = Buffer.from('{}\n');
  const model = safetensorsFixture();
  const fixtures = [
    {
      descriptor: artifact('pulse-daemon', 'daemon', '1'.repeat(64)),
      files: [['bin/pulse', daemon, 0o700]],
    },
    {
      descriptor: artifact('pulse-embedder-runtime', 'embedder-runtime', '2'.repeat(64)),
      files: [['bin/pulse-embedder', portableRunner, 0o700]],
    },
    {
      descriptor: artifact('pulse-model', 'model-runtime', '3'.repeat(64)),
      files: [
        ['model_int8.onnx', model, 0o600], ['pulse-model-contract.json', modelContract, 0o600],
        ['support/config.json', config, 0o600], ['support/special_tokens_map.json', config, 0o600],
        ['support/tokenizer.json', tokenizer, 0o600], ['support/tokenizer_config.json', config, 0o600],
      ],
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
      model: readActivatedArtifact('pulse-model', { installRoot, expectedKind: 'model-runtime' }),
    },
  };
}

async function activateWindowsManagedRuntimeFixtures(root) {
  const installRoot = join(root, 'artifacts');
  const daemon = Buffer.from('windows-daemon');
  const runner = Buffer.from('windows-embedder');
  const config = Buffer.from('{}\n');
  const model = safetensorsFixture();
  const fixtures = [
    { descriptor: artifact('pulse-daemon', 'daemon', '1'.repeat(64)), files: [['bin/pulse.exe', daemon, 0o700]] },
    { descriptor: artifact('pulse-embedder-runtime', 'embedder-runtime', '2'.repeat(64)), files: [['bin/pulse-embedder.exe', runner, 0o700]] },
    {
      descriptor: artifact('pulse-model', 'model-runtime', '3'.repeat(64)),
      files: [
        ['model_int8.onnx', model, 0o600], ['pulse-model-contract.json', config, 0o600],
        ['support/config.json', config, 0o600], ['support/special_tokens_map.json', config, 0o600],
        ['support/tokenizer.json', config, 0o600], ['support/tokenizer_config.json', config, 0o600],
      ],
    },
  ];
  for (const fixture of fixtures) {
    await activateArtifactVersion(fixture.descriptor, join(root, 'unused'), {
      installRoot,
      treeManifest: { schema: 'pulse.artifact_tree.v1', files: fixture.files.map(([path, bytes, mode]) => treeEntry(path, bytes, mode)) },
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
      model: readActivatedArtifact('pulse-model', { installRoot, expectedKind: 'model-runtime' }),
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
  assert.equal(disk.schema, 'pulse.managed_embedder.config.v2');
  assert.equal(disk.engine, 'transformers-js-onnx');
  assert.equal(disk.runner_path.endsWith('/bin/pulse-embedder'), true);
  assert.deepEqual(disk.runner_args, [
    '--model-root', verifiedActivations.model.version_path,
    '--support-root', join(verifiedActivations.model.version_path, 'support'),
  ]);
  assert.equal(disk.model_root, verifiedActivations.model.version_path);
  assert.equal(disk.support_root, join(verifiedActivations.model.version_path, 'support'));
  assert.deepEqual(disk.vector_contract, {
    dimensions: 1024, model: 'bge-m3', normalized: true, opset: 17, pooling: 'cls',
    quantization: 'dynamic-int8',
    revision: '5617a9f61b028005a4858fdac845db406aefb181', source: 'BAAI/bge-m3',
  });
  assert.equal('python_executable' in disk, false);
  assert.equal('helper_path' in disk, false);

  const basePlatformServices = createPlatformServices();
  const caseInsensitivePlatformServices = {
    ...basePlatformServices,
    inspectExecutable(path) {
      const proof = basePlatformServices.inspectExecutable(path);
      return proof ? { ...proof, canonical_path: proof.canonical_path.toUpperCase() } : null;
    },
    isPathInside(candidate, parent) {
      if (candidate.toLowerCase() === parent.toLowerCase()) return true;
      return basePlatformServices.isPathInside(candidate, parent);
    },
  };
  assert.equal(inspectManagedEmbedderConfig(runtime, managed.managed_embedder, {
    platformServices: caseInsensitivePlatformServices,
  }).config.runner_path, disk.runner_path);
});

test('managed product runtime does not load the already tree-verified model into a bounded integrity buffer', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-managed-large-model.'));
  try {
    const runtime = vaultRuntimeFromBinding(binding('personal', root));
    const { installRoot, verifiedActivations } = await activateManagedRuntimeFixtures(root);
    const modelPath = join(verifiedActivations.model.version_path, 'model_int8.onnx');
    const basePlatformServices = createPlatformServices();
    let modelContentRead = false;
    const platformServices = {
      ...basePlatformServices,
      readIntegrityFile(path, options) {
        if (path === modelPath) {
          modelContentRead = true;
          throw new Error('large model must not be loaded into an integrity buffer');
        }
        return basePlatformServices.readIntegrityFile(path, options);
      },
    };

    const managed = resolveManagedRuntime(runtime, {
      installRoot, verifiedActivations, platformServices,
    });

    assert.equal(managed.model.version_path, verifiedActivations.model.version_path);
    assert.equal(modelContentRead, false);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('managed product runtime selects pulse.exe for a Windows target', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-managed-runtime-windows.'));
  try {
    const runtime = vaultRuntimeFromBinding(binding('personal', root));
    const { installRoot, verifiedActivations } = await activateWindowsManagedRuntimeFixtures(root);
    const local = createPlatformServices();
    const platformServices = { ...local, platform: 'win32' };
    const managed = resolveManagedRuntime(runtime, { installRoot, verifiedActivations, platformServices });
    assert.equal(managed.daemon.path.endsWith('/bin/pulse.exe'), true);
    assert.equal(managed.managed_embedder.config.runner_path.endsWith('/bin/pulse-embedder.exe'), true);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('managed embedder v1 remains explicit legacy state and cannot satisfy portable readiness', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-managed-v1.'));
  const runtime = vaultRuntimeFromBinding(binding('personal', root));
  const configPath = join(runtime.data_dir, 'runtime', 'managed-embedder.json');
  const bytes = '{"schema":"pulse.managed_embedder.config.v1"}\n';
  try {
    mkdirSync(join(runtime.data_dir, 'runtime'), { recursive: true, mode: 0o700 });
    writeFileSync(configPath, bytes, { mode: 0o600 });
    assert.throws(
      () => inspectManagedEmbedderConfig(runtime, { config_path: configPath, config_digest: sha256(bytes) }),
      (error) => error instanceof SupervisorError && error.code === 'managed_embedder_config_legacy',
    );
  } finally { rmSync(root, { recursive: true, force: true }); }
});

function binding(mode, root) {
  const common = {
    binding_id: 'binding_demo', binding_digest: 'a'.repeat(64), resolver_epoch: 3, fallback: false,
		workspace: { repository_id: 'repository_pulse', canonical_path: root },
  };
  return {
    ...common, mode,
    personal: {
      store_id: 'store_personal_nik', data_dir: join(root, 'vaults', 'personal'),
      cache_dir: join(root, 'caches', 'personal'), base_url: 'http://127.0.0.1:18800',
    },
  };
}

test('supervisor derives the Personal process descriptor', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-supervisor.'));
  const personal = vaultRuntimeFromBinding(binding('personal', root));
  assert.equal(personal.runtime_mode, 'personal-local');
  assert.equal(personal.fallback, false);
	assert.equal(personal.repository_id, 'repository_pulse');
  assert.equal(inspectVaultRuntime(personal).status, 'stopped');
});

test('supervisor rejects fallback and non-numeric-loopback runtime descriptors', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-supervisor-invalid.'));
  const fallback = binding('personal', root);
  fallback.fallback = true;
  assert.throws(
    () => vaultRuntimeFromBinding(fallback),
    (error) => error instanceof SupervisorError && error.code === 'vault_runtime_invalid',
  );
  const localhost = binding('personal', root);
  localhost.personal.base_url = 'http://localhost:18800';
  assert.throws(() => vaultRuntimeFromBinding(localhost), SupervisorError);
  const missingWorkspace = binding('personal', root);
  delete missingWorkspace.workspace.canonical_path;
  assert.throws(() => vaultRuntimeFromBinding(missingWorkspace), SupervisorError);
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
    response.end(JSON.stringify({ status: 'ok', ...(process.env.PULSE_STARTUP_NONCE
      ? { startup_nonce: process.env.PULSE_STARTUP_NONCE } : {}) })); return;
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
writeFileSync(dataDir + '/secret.key.staged', 'a'.repeat(64), { mode: 0o600 });
renameSync(dataDir + '/secret.key.staged', dataDir + '/secret.key');
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
  if (request.url === '/health') {
    response.end(JSON.stringify({ status: 'ok', startup_nonce: process.env.PULSE_STARTUP_NONCE })); return;
  }
  if (request.url === '/memory/status') { response.end('{"full_retrieval":true,"embedder":"bge-m3"}'); return; }
  if (request.url === '/retrieve' && request.method === 'POST') {
    const authorityMatches =
      request.headers['x-pulse-product-workspace'] === Buffer.from(process.env.PULSE_PRODUCT_WORKSPACE, 'utf8').toString('base64url') &&
      request.headers['x-pulse-product-binding'] === process.env.PULSE_BINDING_DIGEST &&
      request.headers['x-pulse-product-repository'] === process.env.PULSE_REPOSITORY_ID &&
      request.headers['x-pulse-product-resolver-epoch'] === process.env.PULSE_RESOLVER_EPOCH;
    if (!authorityMatches) { response.statusCode = 403; response.end('{"error":"binding"}'); return; }
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
    response.end(JSON.stringify({ status: 'ok', startup_nonce: process.env.PULSE_STARTUP_NONCE }));
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

test('supervisor delegates process control and can bind readiness to a startup nonce', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-supervisor-platform-process.'));
  const port = await freePort();
  const selected = binding('personal', root);
  selected.personal.base_url = `http://127.0.0.1:${port}`;
  const runtime = vaultRuntimeFromBinding(selected);
  const daemon = join(root, 'pulse-daemon-platform.mjs');
  writeFakeDaemon(daemon, 'platform-daemon');
  const base = createPlatformServices();
  const calls = { inspect: 0, terminate: 0, ensure: 0, read: 0, write: 0, executable: 0, integrity: 0 };
  const platformServices = {
    ...base,
    createStartupNonce: () => 'd'.repeat(64),
    inspectProcess(pid) { calls.inspect += 1; return base.inspectProcess(pid); },
    terminateProcess(pid, options) { calls.terminate += 1; return base.terminateProcess(pid, options); },
    ensurePrivateDirectory(path) { calls.ensure += 1; return base.ensurePrivateDirectory(path); },
    readPrivateFile(path, options) { calls.read += 1; return base.readPrivateFile(path, options); },
    atomicWritePrivateFile(path, bytes, options) {
      calls.write += 1; return base.atomicWritePrivateFile(path, bytes, options);
    },
    inspectExecutable(path) { calls.executable += 1; return base.inspectExecutable(path); },
    readIntegrityFile(path, options) { calls.integrity += 1; return base.readIntegrityFile(path, options); },
  };
  try {
    const started = await startVaultRuntime(runtime, {
      daemonPath: daemon, timeoutMs: 5000, platformServices, requireStartupNonce: true,
    });
    assert.equal(started.status, 'running');
    assert.equal(JSON.parse(readFileSync(runtime.pid_file, 'utf8')).startup_nonce, 'd'.repeat(64));
    assert.equal(inspectVaultRuntime(runtime, { platformServices }).status, 'running');
    const nativeWindowsProofServices = {
      ...platformServices,
      platform: 'win32',
      readIntegrityFile() { throw new Error('native executable proof must not reread the whole binary'); },
    };
    assert.equal(inspectVaultRuntime(runtime, { platformServices: nativeWindowsProofServices }).status, 'running');
    assert.equal((await stopVaultRuntimeAndWait(runtime, { platformServices })).status, 'stopped');
    assert.ok(calls.inspect > 0);
    assert.ok(calls.terminate > 0);
    assert.ok(calls.ensure > 0);
    assert.ok(calls.read > 0);
    assert.ok(calls.write > 0);
    assert.ok(calls.executable > 0);
    assert.ok(calls.integrity > 0);
  } finally {
    try { await stopVaultRuntimeAndWait(runtime, { platformServices }); } catch { /* already stopped */ }
    rmSync(root, { recursive: true, force: true });
  }
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

test('supervisor adopts legacy authority and reuses one Personal daemon across signed project bindings', async (t) => {
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
		...adoptedReceipt, binding_digest: 'b'.repeat(64), repository_id: 'repository_other',
  }), { mode: 0o600 });
	assert.equal(inspectVaultRuntime(runtime).status, 'running');

	writeFileSync(runtime.pid_file, JSON.stringify({
		...adoptedReceipt, store_id: 'store_personal_wrong',
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
