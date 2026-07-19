import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import {
  existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import test from 'node:test';

import { __runtimeLocatorTest, enableProductCompileCache } from '../../../plugins/pulse/runtime-locator.mjs';
import { loadPluginWindowsAdapter } from '../../../plugins/pulse/windows-platform-adapter.mjs';
import { writeProductEdgeFixture } from '../scripts/product-release-fixture.mjs';

const WINDOWS_ADAPTER_OPERATIONS = [
  'acquire_private_lock', 'atomic_write_private_file', 'ensure_private_directory',
  'batch',
  'digest_private_tree', 'inspect_executable', 'inspect_path_identity', 'inspect_private_state', 'inspect_private_tree', 'inspect_process',
  'read_integrity_file', 'read_private_file', 'release_private_lock', 'remove_private_file',
  'terminate_process',
];

function localTreeDigest(root, excludeRootFile = '') {
  return __runtimeLocatorTest.trustedTreeDigest(root, {
    excludeRootFile,
    label: 'Pulse test bounded tree',
    trust: {
      assertTreeEntry(path, _relative, expectedKind) {
        const info = lstatSync(path);
        assert.equal(info.isSymbolicLink(), false);
        assert.equal(expectedKind === 'directory' ? info.isDirectory() : info.isFile(), true);
        return info;
      },
      validateTree() {},
    },
  });
}

test('plugin runtime locator finds the checkout root without invoking a platform Git path', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-plugin-workspace-'));
  try {
    mkdirSync(join(root, '.git'));
    const nested = join(root, 'with spaces', 'nested');
    mkdirSync(nested, { recursive: true });
    assert.equal(__runtimeLocatorTest.canonicalWorkspace(nested), realpathSync(resolve(root)));
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('product hook compile cache is release-bound and safely optional', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-product-compile-cache-'));
  try {
    const calls = [];
    const services = {
      constants: { compileCacheStatus: { FAILED: 2 } },
      enableCompileCache: (path) => { calls.push(path); return { status: 1 }; },
    };
    const digest = 'a'.repeat(64);
    assert.equal(enableProductCompileCache({ PULSE_DATA_DIR: root, PULSE_RUNTIME_DIGEST: digest }, services), true);
    assert.deepEqual(calls, [join(root, 'runtime', 'node-compile-cache', digest)]);
    assert.equal(enableProductCompileCache({ PULSE_DATA_DIR: root, PULSE_RUNTIME_DIGEST: 'unsafe' }, services), false);
    assert.equal(enableProductCompileCache({ PULSE_DATA_DIR: root, PULSE_RUNTIME_DIGEST: digest }, {}), false);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('plugin runtime locator delegates Windows private reads, trees, and executables to the native adapter', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-plugin-windows-trust-'));
  try {
    const file = join(root, 'runtime.mjs');
    const bytes = Buffer.from('export const ready = true;\n');
    writeFileSync(file, bytes);
    writeFileSync(join(root, 'runtime-manifest.json'), '{}\n');
    const activationPath = join(root, 'activation.json');
    writeFileSync(activationPath, `${JSON.stringify({ daemon_path: file })}\n`);
    const calls = [];
    const dynamicDigestRoots = new Set();
    const treeDigest = createHash('sha256').update('runtime.mjs').update('\0').update(bytes).update('\0').digest('hex');
    const adapter = {
      digestPrivateTree(path, options) {
        calls.push(['digest', path, options]);
        return { bytes: bytes.length + 3, files: 2, tree_digest: treeDigest };
      },
      inspectPathIdentity(path, options) {
        calls.push(['identity', path, options]);
        return {
          canonical_path: path, identity_token: 'volume:file', kind: options.kind,
          reparse_point: false,
        };
      },
      readPrivateFile(path, options) {
        calls.push(['read', path, options]);
        return readFileSync(path);
      },
      inspectExecutable(path) {
        calls.push(['executable', path]);
        return {
          canonical_path: path, executable: true, owner_only: true, regular_file: true,
          reparse_point: false, sha256: createHash('sha256').update(readFileSync(path)).digest('hex'),
        };
      },
      batch(requests) {
        calls.push(['batch', requests]);
        return requests.map(({ operation, payload }) => {
          if (operation === 'read_private_file') {
            return { bytes_base64: readFileSync(payload.path).toString('base64') };
          }
          if (operation === 'digest_private_tree') {
            return {
              bytes: bytes.length + 3, files: 2,
              tree_digest: dynamicDigestRoots.has(resolve(payload.path))
                ? localTreeDigest(payload.path, payload.exclude_root_file)
                : treeDigest,
            };
          }
          if (operation === 'inspect_executable') {
            return {
              canonical_path: payload.path, executable: true, owner_only: true, regular_file: true,
              reparse_point: false, sha256: createHash('sha256').update(readFileSync(payload.path)).digest('hex'),
            };
          }
          if (operation === 'inspect_path_identity') {
            return {
              canonical_path: payload.path, identity_token: 'volume:file', kind: payload.kind,
              reparse_point: false,
            };
          }
          if (operation === 'inspect_private_state') {
            return {
              canonical_path: payload.path, kind: payload.kind, owner_only: true,
              reparse_point: false,
            };
          }
          throw new Error(`unexpected batch operation: ${operation}`);
        });
      },
      atomicWritePrivateFile(path, data, { ensureParent }) {
        calls.push(['write', path]);
        if (ensureParent) mkdirSync(resolve(path, '..'), { recursive: true });
        writeFileSync(path, data);
      },
    };
    const trust = __runtimeLocatorTest.createTrustServices({ platform: 'win32', windowsAdapter: adapter });
    assert.equal(trust.workspaceIdentity(root), 'volume:file');
    assert.deepEqual(trust.readPrivateFile(file, 1024), bytes);
    assert.equal(__runtimeLocatorTest.trustedTreeDigest(root, {
      excludeRootFile: 'runtime-manifest.json', label: 'Pulse test runtime', trust,
    }), treeDigest);
    assert.equal(trust.executableDigest(file), createHash('sha256').update(bytes).digest('hex'));
    assert.deepEqual(calls.map(([operation]) => operation), ['identity', 'read', 'digest', 'executable']);
    assert.equal(calls[2][2].excludeRootFile, 'runtime-manifest.json');
    const locatorProof = trust.workspaceLocatorProof(file, root);
    assert.deepEqual(locatorProof.locatorBytes, bytes);
    assert.equal(locatorProof.workspaceIdentity, 'volume:file');
    assert.deepEqual(trust.readPrivateFiles([{ path: file, maxBytes: 1024 }]), [bytes]);
    const edge = trust.productEdgeProof({
      daemonPath: file, pluginRoot: root, runtimeManifestPath: join(root, 'runtime-manifest.json'), runtimeRoot: root,
    });
    assert.equal(edge.runtimeDigest, treeDigest);
    assert.equal(edge.pluginDigest, treeDigest);
    assert.equal(edge.daemonDigest, createHash('sha256').update(bytes).digest('hex'));
    assert.deepEqual(edge.runtimeManifestBytes, Buffer.from('{}\n'));
    const activationProof = trust.productActivationProof({
      activationPath, hostAccessPath: file, pluginRoot: root,
      runtimeManifestPath: join(root, 'runtime-manifest.json'), runtimeRoot: root,
    });
    assert.deepEqual(activationProof.hostAccessBytes, bytes);
    assert.deepEqual(activationProof.activationBytes, Buffer.from(`${JSON.stringify({ daemon_path: file })}\n`));
    assert.equal(activationProof.daemonPath, file);
    assert.equal(activationProof.runtimeDigest, treeDigest);
    const productHome = join(root, 'product-home');
    const dataDir = join(root, 'data');
    const runtimeRoot = join(dataDir, 'runtime', 'codex', 'current');
    mkdirSync(join(runtimeRoot, 'src'), { recursive: true });
    mkdirSync(join(runtimeRoot, 'vendor', 'pulse-mcp-dist'), { recursive: true });
    mkdirSync(join(runtimeRoot, 'node_modules', 'dependency'), { recursive: true });
    writeFileSync(join(runtimeRoot, 'src', 'cli.js'), 'export const ready = true;\n');
    writeFileSync(join(runtimeRoot, 'vendor', 'pulse-mcp-dist', 'index.js'), 'export const mcp = true;\n');
    writeFileSync(join(runtimeRoot, 'package.json'), '{"name":"@zbs-gg/pulse"}\n');
    const dependencyPath = join(runtimeRoot, 'node_modules', 'dependency', 'index.js');
    const dependencyBytes = Buffer.from('export const dependency = true;\n');
    writeFileSync(dependencyPath, dependencyBytes);
    writeFileSync(join(runtimeRoot, 'runtime-manifest.json'), '{}\n');
    const productPluginRoot = join(root, 'product-plugin');
    mkdirSync(productPluginRoot);
    writeFileSync(join(productPluginRoot, 'runtime.mjs'), bytes);
    dynamicDigestRoots.add(resolve(runtimeRoot));
    dynamicDigestRoots.add(resolve(productPluginRoot));
    const locatorKey = __runtimeLocatorTest.workspaceDigest(root);
    const locatorPath = join(root, 'product-locators.json');
    writeFileSync(locatorPath, `${JSON.stringify({ entries: { [locatorKey]: {
      data_dir: dataDir, workspace_digest: locatorKey, workspace_path: root,
    } } })}\n`);
    const productHostAccessPath = join(productHome, 'product-host-access', locatorKey, 'codex.json');
    mkdirSync(resolve(productHostAccessPath, '..'), { recursive: true });
    writeFileSync(productHostAccessPath, '{}\n');
    const productActivationPath = join(dataDir, 'runtime', 'product-daemon.json');
    writeFileSync(productActivationPath, `${JSON.stringify({ daemon_path: file })}\n`);
    const environmentProof = trust.productEnvironmentProof({
      host: 'codex', locatorPath, pluginRoot: productPluginRoot, productHome, workspacePath: root,
    });
    assert.equal(environmentProof.locatorKey, locatorKey);
    assert.equal(environmentProof.workspaceIdentity, 'volume:file');
    assert.equal(environmentProof.daemonPath, file);
    assert.equal(environmentProof.runtimeDigest, localTreeDigest(runtimeRoot, 'runtime-manifest.json'));
    assert.deepEqual(calls.slice(4).map(([operation]) => operation), ['batch', 'batch', 'batch', 'batch', 'batch']);
    trust.writeProductEnvironmentCache(environmentProof, 'codex');
    const cachedEnvironmentProof = trust.productEnvironmentCacheProof({
      host: 'codex', locatorPath, pluginRoot: productPluginRoot, productHome, workspacePath: root,
    });
    assert.equal(cachedEnvironmentProof.integrityCacheHit, true);
    assert.equal(cachedEnvironmentProof.runtimeDigest, environmentProof.runtimeDigest);
    assert.equal(cachedEnvironmentProof.pluginDigest, environmentProof.pluginDigest);
    assert.equal(cachedEnvironmentProof.daemonDigest, environmentProof.daemonDigest);
    assert.deepEqual(calls.slice(9).map(([operation]) => operation), ['write'],
      'a valid bounded receipt must avoid another native adapter invocation');
    trust.writeProductEnvironmentCache(environmentProof, 'codex');
    assert.equal(calls.filter(([operation]) => operation === 'write').length, 1,
      'a still-valid receipt must not spawn a second Windows writer');
    writeFileSync(dependencyPath, Buffer.concat([dependencyBytes, Buffer.from('// drift\n')]));
    const callsBeforeLeasedDependencyRead = calls.length;
    assert.equal(trust.productEnvironmentCacheProof({
      host: 'codex', locatorPath, pluginRoot: productPluginRoot, productHome, workspacePath: root,
    })?.runtimeDigest, environmentProof.runtimeDigest,
    'the bounded lease must avoid walking the large third-party tree on every event');
    assert.equal(calls.length, callsBeforeLeasedDependencyRead);
    assert.notEqual(trust.productEnvironmentProof({
      host: 'codex', locatorPath, pluginRoot: productPluginRoot, productHome, workspacePath: root,
    }).runtimeDigest, environmentProof.runtimeDigest,
    'the next full native proof must detect third-party tree drift');
    writeFileSync(dependencyPath, dependencyBytes);
    const runtimeEntrypoint = join(runtimeRoot, 'src', 'cli.js');
    const runtimeEntrypointBytes = readFileSync(runtimeEntrypoint);
    writeFileSync(runtimeEntrypoint, Buffer.concat([runtimeEntrypointBytes, Buffer.from('// drift\n')]));
    assert.equal(trust.productEnvironmentCacheProof({
      host: 'codex', locatorPath, pluginRoot: productPluginRoot, productHome, workspacePath: root,
    }), undefined, 'runtime drift must invalidate the bounded integrity receipt without native process startup');
    writeFileSync(runtimeEntrypoint, runtimeEntrypointBytes);
    writeFileSync(productActivationPath, `${JSON.stringify({ daemon_path: file, changed: true })}\n`);
    assert.equal(trust.productEnvironmentCacheProof({
      host: 'codex', locatorPath, pluginRoot: productPluginRoot, productHome, workspacePath: root,
    }), undefined, 'activation drift must invalidate the bounded integrity receipt');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('plugin runtime locator resolves Windows short and long workspace aliases by native identity', () => {
  const installedPath = 'C:\\Users\\runneradmin\\AppData\\Local\\Temp\\pulse-native\\workspace';
  const invokedPath = 'C:\\Users\\RUNNER~1\\AppData\\Local\\Temp\\pulse-native\\workspace';
  const identity = 'volume-7:file-42';
  const workspaceID = __runtimeLocatorTest.workspaceID(identity);
  const key = __runtimeLocatorTest.workspaceDigest(installedPath);
  const entry = {
    anchor_path: 'C:\\pulse\\anchor.json',
    data_dir: 'C:\\pulse\\data',
    public_key_path: 'C:\\pulse\\key.pem',
    registry_path: 'C:\\pulse\\registry.json',
    trust_mode: 'test',
    workspace_digest: key,
    workspace_id: workspaceID,
    workspace_path: installedPath,
  };
  const locator = { entries: { [key]: entry } };
  const trust = { workspaceIdentity: () => identity };

  assert.deepEqual(
    __runtimeLocatorTest.selectLocatorEntry(locator, invokedPath, trust),
    { entry, key },
  );
  assert.throws(
    () => __runtimeLocatorTest.selectLocatorEntry(locator, invokedPath, {
      workspaceIdentity: () => 'different-volume:different-file',
    }),
    /workspace identity/,
  );
});

test('plugin Windows adapter verifies its signed catalog and exact native protocol', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-plugin-adapter-catalog-'));
  try {
    const target = 'win32-x64';
    const directory = join(root, target);
    mkdirSync(directory, { recursive: true });
    const binary = Buffer.from('fixture Windows adapter');
    const binaryPath = join(directory, 'pulse-platform-adapter.exe');
    writeFileSync(binaryPath, binary);
    const sha256 = createHash('sha256').update(binary).digest('hex');
    writeFileSync(join(root, 'catalog.json'), `${JSON.stringify({
      adapters: { [target]: {
        bytes: binary.length, path: `${target}/pulse-platform-adapter.exe`, sha256, target,
      } },
      protocol: 1,
      schema: 'pulse.windows_bootstrap_adapter_catalog.v1',
    })}\n`);
    const calls = [];
    const adapter = loadPluginWindowsAdapter({
      architecture: 'x64', catalogRoot: root,
      invoke: ({ operation, payload }) => {
        calls.push({ operation, payload });
        if (operation === 'batch') return { results: payload.requests.map(({ operation: nested, request }) => {
          if (nested === 'contract') return {
            operations: WINDOWS_ADAPTER_OPERATIONS,
            schema: 'pulse.windows_bootstrap_adapter.contract.v1', target, version: 1,
          };
          if (nested === 'read_private_file') {
            return { bytes_base64: Buffer.from('private').toString('base64') };
          }
          if (nested === 'inspect_path_identity') return {
            canonical_path: request.path, identity_token: 'volume:file', kind: request.kind,
            reparse_point: false,
          };
          if (nested === 'digest_private_tree') return {
            bytes: 7, files: 1, tree_digest: 'a'.repeat(64),
          };
          throw new Error(`unexpected nested operation: ${nested}`);
        }) };
        if (operation === 'atomic_write_private_file') return { written: true };
        throw new Error(`unexpected operation: ${operation}`);
      },
    });
    assert.deepEqual(adapter.readPrivateFile('C:\\private.json'), Buffer.from('private'));
    assert.deepEqual(adapter.inspectPathIdentity('C:\\workspace', { kind: 'directory' }), {
      canonical_path: 'C:\\workspace', identity_token: 'volume:file', kind: 'directory',
      reparse_point: false,
    });
    assert.deepEqual(adapter.digestPrivateTree('C:\\runtime', {
      excludeRootFile: 'runtime-manifest.json', maximumDepth: 128,
      maximumEntries: 100000, maximumTotalBytes: 1024,
    }), { bytes: 7, files: 1, tree_digest: 'a'.repeat(64) });
    assert.deepEqual(adapter.batch([
      { operation: 'read_private_file', payload: { maximum_bytes: 1024, minimum_bytes: 1, path: 'C:\\private.json' } },
    ]), [{ bytes_base64: Buffer.from('private').toString('base64') }]);
    adapter.atomicWritePrivateFile('C:\\cache.json', Buffer.from('cache'), {
      ensureParent: true, maxBytes: 1024,
    });
    assert.deepEqual(calls.map(({ operation }) => operation), [
      'batch', 'batch', 'batch', 'batch', 'atomic_write_private_file',
    ]);
    assert.equal(calls[0].payload.requests[1].request.path, 'C:\\private.json');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('the signed product plugin snapshot carries the Windows trust adapter beside the launcher', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-plugin-adapter-edge-'));
  try {
    writeProductEdgeFixture(root);
    const adapterRoot = join(root, 'marketplace', 'plugins', 'pulse', 'native', 'windows-bootstrap');
    assert.equal(existsSync(join(adapterRoot, 'catalog.json')), true);
    assert.equal(existsSync(join(adapterRoot, 'win32-arm64', 'pulse-platform-adapter.exe')), true);
    assert.equal(existsSync(join(adapterRoot, 'win32-x64', 'pulse-platform-adapter.exe')), true);
    const runtimeAdapterRoot = join(root, 'runtime', 'runtime', 'windows-bootstrap');
    assert.equal(existsSync(join(runtimeAdapterRoot, 'catalog.json')), true);
    assert.equal(existsSync(join(runtimeAdapterRoot, 'win32-arm64', 'pulse-platform-adapter.exe')), true);
    assert.equal(existsSync(join(runtimeAdapterRoot, 'win32-x64', 'pulse-platform-adapter.exe')), true);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
