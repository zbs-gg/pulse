import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import {
  existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import test from 'node:test';

import { __runtimeLocatorTest } from '../../../plugins/pulse/runtime-locator.mjs';
import { loadPluginWindowsAdapter } from '../../../plugins/pulse/windows-platform-adapter.mjs';
import { writeProductEdgeFixture } from '../scripts/product-release-fixture.mjs';

const WINDOWS_ADAPTER_OPERATIONS = [
  'acquire_private_lock', 'atomic_write_private_file', 'ensure_private_directory',
  'inspect_executable', 'inspect_path_identity', 'inspect_private_state', 'inspect_private_tree', 'inspect_process',
  'read_integrity_file', 'read_private_file', 'release_private_lock', 'remove_private_file',
  'terminate_process',
];

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

test('plugin runtime locator delegates Windows private reads, trees, and executables to the native adapter', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-plugin-windows-trust-'));
  try {
    const file = join(root, 'runtime.mjs');
    const bytes = Buffer.from('export const ready = true;\n');
    writeFileSync(file, bytes);
    writeFileSync(join(root, 'runtime-manifest.json'), '{}\n');
    const calls = [];
    const adapter = {
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
      inspectPrivateTree(path, options) {
        calls.push(['tree', path, options]);
        return {
          bytes: options.entries.reduce((total, entry) => total + entry.bytes, 0),
          files: options.entries.length,
        };
      },
      inspectExecutable(path) {
        calls.push(['executable', path]);
        return {
          canonical_path: path, executable: true, owner_only: true, regular_file: true,
          reparse_point: false, sha256: createHash('sha256').update(readFileSync(path)).digest('hex'),
        };
      },
    };
    const trust = __runtimeLocatorTest.createTrustServices({ platform: 'win32', windowsAdapter: adapter });
    assert.equal(trust.workspaceIdentity(root), 'volume:file');
    assert.deepEqual(trust.readPrivateFile(file, 1024), bytes);
    assert.equal(__runtimeLocatorTest.trustedTreeDigest(root, {
      excludeRootFile: 'runtime-manifest.json', label: 'Pulse test runtime', trust,
    }), createHash('sha256').update('runtime.mjs').update('\0').update(bytes).update('\0').digest('hex'));
    assert.equal(trust.executableDigest(file), createHash('sha256').update(bytes).digest('hex'));
    assert.deepEqual(calls.map(([operation]) => operation), ['identity', 'read', 'tree', 'executable']);
    assert.deepEqual(calls[2][2].entries.map((entry) => entry.path), [
      'runtime-manifest.json', 'runtime.mjs',
    ]);
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
        if (operation === 'contract') return {
          operations: WINDOWS_ADAPTER_OPERATIONS,
          schema: 'pulse.windows_bootstrap_adapter.contract.v1', target, version: 1,
        };
        if (operation === 'read_private_file') return { bytes_base64: Buffer.from('private').toString('base64') };
        if (operation === 'inspect_path_identity') return {
          canonical_path: payload.path, identity_token: 'volume:file', kind: payload.kind,
          reparse_point: false,
        };
        throw new Error(`unexpected operation: ${operation}`);
      },
    });
    assert.deepEqual(adapter.readPrivateFile('C:\\private.json'), Buffer.from('private'));
    assert.deepEqual(adapter.inspectPathIdentity('C:\\workspace', { kind: 'directory' }), {
      canonical_path: 'C:\\workspace', identity_token: 'volume:file', kind: 'directory',
      reparse_point: false,
    });
    assert.deepEqual(calls.map(({ operation }) => operation), [
      'contract', 'read_private_file', 'inspect_path_identity',
    ]);
    assert.equal(calls[1].payload.path, 'C:\\private.json');
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
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
