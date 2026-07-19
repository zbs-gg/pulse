import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  mkdtempSync, readFileSync, rmSync, statSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import test from 'node:test';

import {
  WindowsBootstrapAdapterError,
  loadBundledWindowsAdapter,
} from './windows-bootstrap-adapter.js';
import { buildWindowsBootstrapAdapters } from '../scripts/build-windows-bootstrap-adapter.mjs';

const cliRoot = resolve(new URL('..', import.meta.url).pathname);
const appRoot = resolve(cliRoot, '..');
const operations = Object.freeze([
  'acquire_private_lock',
  'atomic_write_private_file',
  'ensure_private_directory',
  'batch',
  'digest_private_tree',
  'inspect_executable',
  'inspect_path_identity',
  'inspect_private_state',
  'inspect_private_tree',
  'inspect_process',
  'read_integrity_file',
  'read_private_file',
  'release_private_lock',
  'remove_private_file',
  'terminate_process',
]);

function digest(path) {
  return createHash('sha256').update(readFileSync(path)).digest('hex');
}

test('Windows bootstrap adapters cross-compile reproducibly for x64 and arm64', { timeout: 120_000 }, () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-windows-bootstrap-build.'));
  try {
    const first = join(root, 'first');
    const second = join(root, 'second');
    const catalogA = buildWindowsBootstrapAdapters({ appRoot, outputRoot: first });
    const catalogB = buildWindowsBootstrapAdapters({ appRoot, outputRoot: second });

    assert.equal(catalogA.schema, 'pulse.windows_bootstrap_adapter_catalog.v1');
    assert.deepEqual(catalogA, catalogB);
    for (const target of ['win32-arm64', 'win32-x64']) {
      const entry = catalogA.adapters[target];
      const binaryA = join(first, entry.path);
      const binaryB = join(second, catalogB.adapters[target].path);
      assert.equal(readFileSync(binaryA, { encoding: null, flag: 'r' }).subarray(0, 2).toString(), 'MZ');
      assert.equal(statSync(binaryA).size, entry.bytes);
      assert.equal(digest(binaryA), entry.sha256);
      assert.equal(digest(binaryB), entry.sha256);
    }
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('bundled selector verifies the exact digest and exposes the complete native contract', () => {
  const catalogRoot = resolve(cliRoot, 'runtime', 'windows-bootstrap');
  const calls = [];
  const invoke = ({ operation, payload, target }) => {
    calls.push({ operation, payload, target });
    if (operation === 'contract') {
      return {
        operations: [...operations],
        schema: 'pulse.windows_bootstrap_adapter.contract.v1',
        target,
        version: 1,
      };
    }
    if (operation === 'inspect_private_state') {
      return { canonical_path: payload.path, kind: payload.kind, owner_only: true, reparse_point: false };
    }
    if (operation === 'read_integrity_file') {
      return {
        bytes_base64: Buffer.from('trusted').toString('base64'), canonical_path: payload.path,
        owner: payload.owner, regular_file: true, reparse_point: false,
      };
    }
    if (operation === 'inspect_private_tree') {
      return { bytes: payload.entries.reduce((total, entry) => total + entry.bytes, 0), files: payload.entries.length };
    }
    if (operation === 'digest_private_tree') {
      return { bytes: 7, files: 1, tree_digest: 'a'.repeat(64) };
    }
    if (operation === 'batch') {
      return { results: payload.requests.map(({ operation: nested }) =>
        nested === 'read_private_file'
          ? { bytes_base64: Buffer.from('trusted').toString('base64') }
          : { bytes: 7, files: 1, tree_digest: 'a'.repeat(64) }) };
    }
    if (operation === 'inspect_process') {
      return { command: 'node.exe', identity_token: 'process-identity', pid: payload.pid, running: true };
    }
    if (operation === 'acquire_private_lock') return { lease: 'lease-1' };
    if (operation === 'release_private_lock') return { released: true };
    throw new Error(`unexpected operation ${operation}`);
  };
  const adapter = loadBundledWindowsAdapter({ architecture: 'x64', catalogRoot, invoke });
  const path = 'C:\\Users\\Pulse\\.pulse';

  assert.equal(adapter.schema, 'pulse.windows_bootstrap_adapter.v1');
  assert.deepEqual(adapter.inspectPrivateState(path, { kind: 'directory' }), {
    canonical_path: path, kind: 'directory', owner_only: true, reparse_point: false,
  });
  assert.deepEqual(adapter.readIntegrityFile(`${path}\\catalog.json`, {
    maxBytes: 1024, owner: 'current',
  }), {
    bytes: Buffer.from('trusted'), canonical_path: `${path}\\catalog.json`, owner: 'current',
    regular_file: true, reparse_point: false,
  });
  assert.deepEqual(adapter.inspectPrivateTree(`${path}\\runtime`, {
    entries: [{ bytes: 7, executable: false, path: 'index.js', sha256: 'a'.repeat(64) }],
    maximumDepth: 32, maximumEntries: 8192, maximumTotalBytes: 4096,
  }), { bytes: 7, files: 1 });
  assert.deepEqual(adapter.digestPrivateTree(`${path}\\runtime`, {
    excludeRootFile: 'runtime-manifest.json', maximumDepth: 32,
    maximumEntries: 8192, maximumTotalBytes: 4096,
  }), { bytes: 7, files: 1, tree_digest: 'a'.repeat(64) });
  assert.deepEqual(adapter.batch([
    { operation: 'read_private_file', payload: { maximum_bytes: 1024, minimum_bytes: 1, path: `${path}\\state.json` } },
    { operation: 'digest_private_tree', payload: { maximum_depth: 32, path: `${path}\\runtime` } },
  ]), [
    { bytes_base64: Buffer.from('trusted').toString('base64') },
    { bytes: 7, files: 1, tree_digest: 'a'.repeat(64) },
  ]);
  assert.throws(
    () => adapter.batch([{ operation: 'remove_private_file', payload: { path: `${path}\\state.json` } }]),
    (error) => error instanceof WindowsBootstrapAdapterError &&
      error.code === 'windows_bootstrap_adapter_batch_invalid',
  );
  const release = adapter.acquirePrivateLock(`${path}\\install.lock`, { staleAfterMs: 1000, timeoutMs: 0 });
  release();
  assert.deepEqual(calls.map(({ operation }) => operation), [
    'contract', 'inspect_private_state', 'read_integrity_file', 'inspect_private_tree', 'digest_private_tree', 'batch',
    'inspect_process', 'acquire_private_lock', 'release_private_lock',
  ]);
});

test('bundled selector fails closed when the catalog binary is missing or tampered', () => {
  const sourceRoot = resolve(cliRoot, 'runtime', 'windows-bootstrap');
  const root = mkdtempSync(join(tmpdir(), 'pulse-windows-bootstrap-tamper.'));
  try {
    const catalog = JSON.parse(readFileSync(join(sourceRoot, 'catalog.json'), 'utf8'));
    const entry = catalog.adapters['win32-x64'];
    const targetRoot = join(root, 'copy');
    execFileSync('cp', ['-R', sourceRoot, targetRoot]);
    const binary = join(targetRoot, entry.path);
    writeFileSync(binary, Buffer.concat([readFileSync(binary), Buffer.from('tamper')]));
    assert.throws(
      () => loadBundledWindowsAdapter({ architecture: 'x64', catalogRoot: targetRoot, invoke: () => ({}) }),
      (error) => error instanceof WindowsBootstrapAdapterError &&
        error.code === 'windows_bootstrap_adapter_digest_mismatch',
    );
    rmSync(binary);
    assert.throws(
      () => loadBundledWindowsAdapter({ architecture: 'x64', catalogRoot: targetRoot, invoke: () => ({}) }),
      (error) => error instanceof WindowsBootstrapAdapterError &&
        error.code === 'windows_bootstrap_adapter_missing',
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('published npm file list contains both bootstrap binaries and their digest catalog', () => {
  const packed = JSON.parse(execFileSync('npm', ['pack', '--dry-run', '--ignore-scripts', '--json'], {
    cwd: cliRoot, encoding: 'utf8', timeout: 60_000,
  }));
  const files = new Set(packed[0].files.map(({ path }) => path));
  assert.equal(files.has('runtime/windows-bootstrap/catalog.json'), true);
  assert.equal(files.has('runtime/windows-bootstrap/win32-arm64/pulse-platform-adapter.exe'), true);
  assert.equal(files.has('runtime/windows-bootstrap/win32-x64/pulse-platform-adapter.exe'), true);
});
