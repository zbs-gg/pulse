import { createHash } from 'node:crypto';
import { existsSync, lstatSync, mkdirSync, readFileSync, readdirSync, realpathSync } from 'node:fs';
import * as nodeModule from 'node:module';
import { homedir } from 'node:os';
import { dirname, isAbsolute, join, parse, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { loadPluginWindowsAdapter } from './windows-platform-adapter.mjs';

const SHA256 = /^[a-f0-9]{64}$/;
const PRIVATE_JSON_LIMIT = 1024 * 1024;
const TREE_MAX_ENTRIES = 100_000;
const TREE_MAX_DEPTH = 128;
const TREE_MAX_BYTES = 16 * 1024 * 1024 * 1024;
// Windows process startup and ACL walks are expensive. A SessionStart still
// proves every signed tree and writes this owner-private bounded receipt.
// Events inside its short window avoid another native process, bind the exact
// workspace and authority files, and rehash the executable Pulse edge: our
// source, MCP distribution, plugin, and daemon. The large third-party
// node_modules tree remains covered by the short lease established by the full
// native proof. Any edge/authority drift or lease expiry falls back to it.
const WINDOWS_INTEGRITY_CACHE_TTL_MS = 2 * 60 * 1000;
const WINDOWS_INTEGRITY_CACHE_SCHEMA = 'pulse.windows_product_integrity_cache.v1';

export function enableProductCompileCache(environment, moduleServices = nodeModule) {
  if (typeof moduleServices.enableCompileCache !== 'function') return false;
  const dataDir = environment?.PULSE_DATA_DIR;
  const runtimeDigest = environment?.PULSE_RUNTIME_DIGEST;
  if (typeof dataDir !== 'string' || !isAbsolute(dataDir) || !SHA256.test(runtimeDigest ?? '')) return false;
  const cachePath = join(resolve(dataDir), 'runtime', 'node-compile-cache', runtimeDigest);
  try {
    mkdirSync(cachePath, { recursive: true, mode: 0o700 });
    const info = lstatSync(cachePath);
    if (!info.isDirectory() || info.isSymbolicLink()) return false;
    const result = moduleServices.enableCompileCache(cachePath);
    return result && typeof result === 'object' && result.status !== moduleServices.constants?.compileCacheStatus?.FAILED;
  } catch {
    return false;
  }
}

function exactObject(value, keys) {
  return value && !Array.isArray(value) && typeof value === 'object' &&
    Object.keys(value).sort().join('\0') === [...keys].sort().join('\0');
}

function nativePrivateBytes(proof, maxBytes = PRIVATE_JSON_LIMIT) {
  if (!exactObject(proof, ['bytes_base64']) || typeof proof.bytes_base64 !== 'string') {
    throw new Error('Pulse product private state is unsafe');
  }
  const bytes = Buffer.from(proof.bytes_base64, 'base64');
  if (bytes.length < 1 || bytes.length > maxBytes) {
    throw new Error('Pulse product private state is unsafe');
  }
  return bytes;
}

function nativeWorkspaceIdentity(proof) {
  if (!exactObject(proof, ['canonical_path', 'identity_token', 'kind', 'reparse_point']) ||
      proof.kind !== 'directory' || proof.reparse_point !== false ||
      typeof proof.canonical_path !== 'string' || !isAbsolute(proof.canonical_path) ||
      typeof proof.identity_token !== 'string' || proof.identity_token.length < 1 ||
      proof.identity_token.length > 1024) {
    throw new Error('Pulse workspace identity native proof is invalid');
  }
  return proof.identity_token;
}

function nativeTreeDigest(proof) {
  if (!exactObject(proof, ['bytes', 'files', 'tree_digest']) ||
      !Number.isSafeInteger(proof.bytes) || proof.bytes < 1 || proof.bytes > TREE_MAX_BYTES ||
      !Number.isSafeInteger(proof.files) || proof.files < 1 || proof.files > TREE_MAX_ENTRIES ||
      !SHA256.test(proof.tree_digest ?? '')) {
    throw new Error('Pulse trusted tree native digest proof is invalid');
  }
  return proof.tree_digest;
}

function nativeExecutableDigest(proof) {
  if (!exactObject(proof, [
    'canonical_path', 'executable', 'owner_only', 'regular_file', 'reparse_point', 'sha256',
  ]) || proof.executable !== true || proof.owner_only !== true || proof.regular_file !== true ||
      proof.reparse_point !== false || typeof proof.canonical_path !== 'string' ||
      !isAbsolute(proof.canonical_path) || !SHA256.test(proof.sha256 ?? '')) {
    throw new Error('Pulse product executable native proof is invalid');
  }
  return proof.sha256;
}

function bytesDigest(bytes) {
  return createHash('sha256').update(bytes).digest('hex');
}

function preliminaryPrivateBytes(path, maxBytes = PRIVATE_JSON_LIMIT) {
  const info = lstatSync(path);
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 ||
      info.size < 1 || info.size > maxBytes) {
    throw new Error('Pulse product bounded integrity receipt input is unsafe');
  }
  const bytes = readFileSync(path);
  if (bytes.length !== info.size) {
    throw new Error('Pulse product bounded integrity receipt input changed while reading');
  }
  return bytes;
}

function windowsWorkspaceWitness(path) {
  const canonical = realpathSync(resolve(path));
  const info = lstatSync(canonical, { bigint: true });
  if (!info.isDirectory() || info.isSymbolicLink()) {
    throw new Error('Pulse workspace bounded integrity witness is unsafe');
  }
  return `${canonical}\0${info.dev}:${info.ino}:${info.birthtimeNs}:${info.ctimeNs}`;
}

function windowsBoundedTreeTrust() {
  return Object.freeze({
    assertTreeEntry(path, relative, expectedKind) {
      const info = lstatSync(path);
      if (info.isSymbolicLink() ||
          (expectedKind === 'directory' ? !info.isDirectory() : !info.isFile())) {
        throw new Error(`Pulse bounded integrity tree entry is unsafe: ${relative}`);
      }
      return info;
    },
    validateTree() {},
  });
}

function windowsRuntimeEdgeDigest(runtimeRoot, trust = windowsBoundedTreeTrust()) {
  const parts = [
    ['src', join(runtimeRoot, 'src')],
    ['mcp', join(runtimeRoot, 'vendor', 'pulse-mcp-dist')],
  ];
  const hash = createHash('sha256');
  for (const [label, root] of parts) {
    const digest = trustedTreeDigest(root, {
      label: `Pulse trusted runtime ${label}`,
      trust,
    });
    hash.update(label);
    hash.update('\x00');
    hash.update(digest);
    hash.update('\x00');
  }
  const packageBytes = preliminaryPrivateBytes(join(runtimeRoot, 'package.json'));
  hash.update('package.json');
  hash.update('\x00');
  hash.update(packageBytes);
  hash.update('\x00');
  return hash.digest('hex');
}

function windowsIntegrityCachePath(productHome, locatorKey, host) {
  return join(productHome, 'integrity-cache', locatorKey, `${host}.json`);
}

function windowsIntegrityCacheRecord(proof, host, nowMs = Date.now()) {
  return {
    schema: WINDOWS_INTEGRITY_CACHE_SCHEMA,
    created_at_ms: nowMs,
    expires_at_ms: nowMs + WINDOWS_INTEGRITY_CACHE_TTL_MS,
    host,
    locator_key: proof.locatorKey,
    workspace_identity: proof.workspaceIdentity,
    workspace_witness: proof.workspaceWitness,
    data_dir: proof.dataDir,
    activation_path: proof.activationPath,
    host_access_path: proof.hostAccessPath,
    runtime_manifest_path: proof.runtimeManifestPath,
    runtime_root: proof.runtimeRoot,
    plugin_root: proof.pluginRoot,
    daemon_path: proof.daemonPath,
    expected_runtime_path: proof.expectedRuntimePath,
    locator_digest: bytesDigest(proof.locatorBytes),
    host_access_digest: bytesDigest(proof.hostAccessBytes),
    activation_digest: bytesDigest(proof.activationBytes),
    runtime_manifest_digest: bytesDigest(proof.runtimeManifestBytes),
    runtime_edge_digest: proof.runtimeEdgeDigest ?? windowsRuntimeEdgeDigest(proof.runtimeRoot),
    runtime_digest: proof.runtimeDigest,
    plugin_digest: proof.pluginDigest,
    daemon_digest: proof.daemonDigest,
  };
}

function validWindowsIntegrityCache(cache, expected, nowMs = Date.now()) {
  const keys = [
    'schema', 'created_at_ms', 'expires_at_ms', 'host', 'locator_key', 'workspace_identity', 'workspace_witness',
    'data_dir', 'activation_path', 'host_access_path', 'runtime_manifest_path', 'runtime_root',
    'plugin_root', 'daemon_path', 'expected_runtime_path', 'locator_digest', 'host_access_digest',
    'activation_digest', 'runtime_manifest_digest', 'runtime_edge_digest', 'runtime_digest', 'plugin_digest', 'daemon_digest',
  ];
  if (!exactObject(cache, keys) || cache.schema !== WINDOWS_INTEGRITY_CACHE_SCHEMA ||
      !Number.isSafeInteger(cache.created_at_ms) || !Number.isSafeInteger(cache.expires_at_ms) ||
      cache.created_at_ms > nowMs || cache.expires_at_ms <= nowMs ||
      cache.expires_at_ms - cache.created_at_ms !== WINDOWS_INTEGRITY_CACHE_TTL_MS ||
      cache.host !== expected.host || cache.locator_key !== expected.locatorKey ||
      typeof cache.workspace_identity !== 'string' || cache.workspace_identity.length < 1 ||
      cache.workspace_identity.length > 1024 || cache.workspace_witness !== expected.workspaceWitness ||
      cache.data_dir !== expected.dataDir || cache.activation_path !== expected.activationPath ||
      cache.host_access_path !== expected.hostAccessPath ||
      cache.runtime_manifest_path !== expected.runtimeManifestPath || cache.runtime_root !== expected.runtimeRoot ||
      cache.plugin_root !== expected.pluginRoot || cache.daemon_path !== expected.daemonPath ||
      cache.expected_runtime_path !== expected.expectedRuntimePath ||
      cache.locator_digest !== bytesDigest(expected.locatorBytes) ||
      cache.host_access_digest !== bytesDigest(expected.hostAccessBytes) ||
      cache.activation_digest !== bytesDigest(expected.activationBytes) ||
      cache.runtime_manifest_digest !== bytesDigest(expected.runtimeManifestBytes) ||
      cache.runtime_edge_digest !== expected.runtimeEdgeDigest ||
      ![cache.runtime_edge_digest, cache.runtime_digest, cache.plugin_digest, cache.daemon_digest]
        .every((value) => SHA256.test(value ?? ''))) {
    return false;
  }
  return true;
}

function preliminaryPrivateJSON(path) {
  try {
    const info = lstatSync(path);
    if (!info.isFile() || info.isSymbolicLink() || info.size < 1 || info.size > PRIVATE_JSON_LIMIT) {
      throw new Error('unsafe');
    }
    return JSON.parse(readFileSync(path, 'utf8'));
  } catch {
    return undefined;
  }
}

function preliminaryDaemonPath(activationPath) {
  try {
    const activation = preliminaryPrivateJSON(activationPath);
    if (typeof activation?.daemon_path !== 'string' || !isAbsolute(activation.daemon_path)) {
      throw new Error('unsafe');
    }
    return resolve(activation.daemon_path);
  } catch {
    throw new Error('Pulse product activation is missing or invalid; reconnect this workspace.');
  }
}

function workspaceDigest(canonicalPath) {
  return createHash('sha256')
    .update('pulse-codex-product-locator-v1\x00')
    .update(canonicalPath)
    .digest('hex');
}

function workspaceID(identity) {
  if (typeof identity !== 'string' || identity.length < 1 || identity.length > 1024) {
    throw new Error('Pulse workspace identity proof is invalid');
  }
  return `workspace_${createHash('sha256')
    .update('pulse-workspace-v1')
    .update('\x00')
    .update(identity)
    .digest('hex')
    .slice(0, 32)}`;
}

function selectLocatorEntry(locator, canonical, trust) {
  const currentWorkspaceID = workspaceID(trust.workspaceIdentity(canonical));
  const matches = Object.entries(locator?.entries ?? {}).filter(([key, entry]) =>
    entry?.workspace_id === currentWorkspaceID && entry.workspace_digest === key &&
    typeof entry.workspace_path === 'string' && workspaceDigest(entry.workspace_path) === key);
  if (matches.length !== 1) {
    throw new Error('Pulse product locator workspace identity is missing or ambiguous');
  }
  const [key, entry] = matches[0];
  return { entry, key };
}

function canonicalWorkspace(cwd) {
  let current = realpathSync(resolve(cwd));
  if (!lstatSync(current).isDirectory()) current = dirname(current);
  while (true) {
    const marker = join(current, '.git');
    if (existsSync(marker)) {
      const info = lstatSync(marker);
      if (!info.isSymbolicLink() && (info.isDirectory() || info.isFile())) return current;
      throw new Error('Pulse product locator requires a safe Git workspace');
    }
    const parent = dirname(current);
    if (parent === current || current === parse(current).root) break;
    current = parent;
  }
  throw new Error('Pulse product locator requires a Git workspace');
}

function createTrustServices({
  platform = process.platform,
  architecture = process.arch,
  windowsAdapter,
} = {}) {
  if (!['darwin', 'linux', 'win32'].includes(platform)) {
    throw new Error('Pulse product platform is unsupported');
  }
  if (platform === 'win32') {
    let adapter = windowsAdapter;
    const nativeAdapter = () => {
      adapter ??= loadPluginWindowsAdapter({ architecture });
      return adapter;
    };
    return Object.freeze({
      platform,
      workspaceIdentity(path) {
        return nativeWorkspaceIdentity(nativeAdapter().inspectPathIdentity(path, { kind: 'directory' }));
      },
      workspaceLocatorProof(locatorPath, workspacePath) {
        const results = nativeAdapter().batch([
          {
            operation: 'read_private_file',
            payload: {
              encoding: '', maximum_bytes: PRIVATE_JSON_LIMIT, minimum_bytes: 1, path: locatorPath,
            },
          },
          { operation: 'inspect_path_identity', payload: { kind: 'directory', path: workspacePath } },
        ]);
        return Object.freeze({
          locatorBytes: nativePrivateBytes(results[0]),
          workspaceIdentity: nativeWorkspaceIdentity(results[1]),
        });
      },
      productEnvironmentCacheProof({ locatorPath, workspacePath, productHome, host, pluginRoot }) {
        try {
          const preliminaryLocator = preliminaryPrivateJSON(locatorPath);
          const locatorKey = workspaceDigest(workspacePath);
          const entry = preliminaryLocator?.entries?.[locatorKey];
          if (!entry || entry.workspace_digest !== locatorKey ||
              typeof entry.workspace_path !== 'string' || workspaceDigest(entry.workspace_path) !== locatorKey ||
              typeof entry.data_dir !== 'string' || !isAbsolute(entry.data_dir)) {
            return undefined;
          }
          const dataDir = resolve(entry.data_dir);
          const hostAccessPath = join(productHome, 'product-host-access', locatorKey, `${host}.json`);
          const activationPath = join(dataDir, 'runtime', 'product-daemon.json');
          const expectedRuntimePath = join(dataDir, 'runtime', 'codex', 'current', 'src', 'cli.js');
          const runtimeRoot = resolve(expectedRuntimePath, '..', '..');
          const runtimeManifestPath = join(runtimeRoot, 'runtime-manifest.json');
          const daemonPath = preliminaryDaemonPath(activationPath);
          const cachePath = windowsIntegrityCachePath(productHome, locatorKey, host);
          if (!existsSync(cachePath)) return undefined;
          const locatorBytes = preliminaryPrivateBytes(locatorPath);
          const hostAccessBytes = preliminaryPrivateBytes(hostAccessPath);
          const activationBytes = preliminaryPrivateBytes(activationPath);
          const runtimeManifestBytes = preliminaryPrivateBytes(runtimeManifestPath);
          const cacheBytes = preliminaryPrivateBytes(cachePath);
          const workspaceWitness = windowsWorkspaceWitness(workspacePath);
          const boundedTrust = windowsBoundedTreeTrust();
          const expected = {
            activationBytes, activationPath, daemonPath, dataDir, expectedRuntimePath,
            host, hostAccessBytes, hostAccessPath, locatorBytes, locatorKey, pluginRoot,
            runtimeManifestBytes, runtimeManifestPath, runtimeRoot, workspaceWitness,
            runtimeEdgeDigest: windowsRuntimeEdgeDigest(runtimeRoot, boundedTrust),
          };
          const cache = parsePrivateJSON(cacheBytes);
          if (!validWindowsIntegrityCache(cache, expected)) return undefined;
          const pluginDigest = pluginTreeDigest(pluginRoot, boundedTrust);
          const daemonDigest = bytesDigest(preliminaryPrivateBytes(daemonPath, 512 * 1024 * 1024));
          if (pluginDigest !== cache.plugin_digest ||
              daemonDigest !== cache.daemon_digest) return undefined;
          return Object.freeze({
            ...expected,
            cachePath,
            daemonDigest,
            integrityCacheHit: true,
            pluginDigest,
            runtimeDigest: cache.runtime_digest,
            workspaceIdentity: cache.workspace_identity,
          });
        } catch {
          return undefined;
        }
      },
      productEnvironmentProof({ locatorPath, workspacePath, productHome, host, pluginRoot }) {
        const preliminaryLocator = preliminaryPrivateJSON(locatorPath);
        const locatorKey = workspaceDigest(workspacePath);
        const entry = preliminaryLocator?.entries?.[locatorKey];
        if (!entry || entry.workspace_digest !== locatorKey ||
            typeof entry.workspace_path !== 'string' || workspaceDigest(entry.workspace_path) !== locatorKey ||
            typeof entry.data_dir !== 'string' || !isAbsolute(entry.data_dir)) {
          return undefined;
        }
        const dataDir = resolve(entry.data_dir);
        const hostAccessPath = join(productHome, 'product-host-access', locatorKey, `${host}.json`);
        const activationPath = join(dataDir, 'runtime', 'product-daemon.json');
        const expectedRuntimePath = join(dataDir, 'runtime', 'codex', 'current', 'src', 'cli.js');
        const runtimeRoot = resolve(expectedRuntimePath, '..', '..');
        const runtimeManifestPath = join(runtimeRoot, 'runtime-manifest.json');
        const daemonPath = preliminaryDaemonPath(activationPath);
        const results = nativeAdapter().batch([
          {
            operation: 'read_private_file',
            payload: { encoding: '', maximum_bytes: PRIVATE_JSON_LIMIT, minimum_bytes: 1, path: locatorPath },
          },
          { operation: 'inspect_path_identity', payload: { kind: 'directory', path: workspacePath } },
          {
            operation: 'read_private_file',
            payload: { encoding: '', maximum_bytes: PRIVATE_JSON_LIMIT, minimum_bytes: 1, path: hostAccessPath },
          },
          {
            operation: 'read_private_file',
            payload: { encoding: '', maximum_bytes: PRIVATE_JSON_LIMIT, minimum_bytes: 1, path: activationPath },
          },
          {
            operation: 'digest_private_tree',
            payload: {
              exclude_root_file: 'runtime-manifest.json', maximum_depth: TREE_MAX_DEPTH,
              maximum_entries: TREE_MAX_ENTRIES, maximum_total_bytes: TREE_MAX_BYTES, path: runtimeRoot,
            },
          },
          {
            operation: 'read_private_file',
            payload: { encoding: '', maximum_bytes: PRIVATE_JSON_LIMIT, minimum_bytes: 1, path: runtimeManifestPath },
          },
          {
            operation: 'digest_private_tree',
            payload: {
              exclude_root_file: '', maximum_depth: TREE_MAX_DEPTH,
              maximum_entries: TREE_MAX_ENTRIES, maximum_total_bytes: TREE_MAX_BYTES, path: pluginRoot,
            },
          },
          { operation: 'inspect_executable', payload: { path: daemonPath } },
        ]);
        return Object.freeze({
          activationBytes: nativePrivateBytes(results[3]),
          activationPath,
          cachePath: windowsIntegrityCachePath(productHome, locatorKey, host),
          daemonDigest: nativeExecutableDigest(results[7]),
          daemonPath,
          dataDir,
          expectedRuntimePath,
          hostAccessBytes: nativePrivateBytes(results[2]),
          hostAccessPath,
          locatorBytes: nativePrivateBytes(results[0]),
          locatorKey,
          integrityCacheHit: false,
          pluginDigest: nativeTreeDigest(results[6]),
          pluginRoot,
          runtimeDigest: nativeTreeDigest(results[4]),
          runtimeManifestBytes: nativePrivateBytes(results[5]),
          runtimeManifestPath,
          runtimeRoot,
          workspaceIdentity: nativeWorkspaceIdentity(results[1]),
          workspacePath,
          workspaceWitness: windowsWorkspaceWitness(workspacePath),
        });
      },
      writeProductEnvironmentCache(proof, host) {
        const nowMs = Date.now();
        const proofWithEdge = {
          ...proof,
          runtimeEdgeDigest: windowsRuntimeEdgeDigest(proof.runtimeRoot),
        };
        const existing = preliminaryPrivateJSON(proof.cachePath);
        if (validWindowsIntegrityCache(existing, { ...proofWithEdge, host }, nowMs) &&
            existing.expires_at_ms - nowMs > 30_000) {
          return;
        }
        const record = windowsIntegrityCacheRecord(proofWithEdge, host, nowMs);
        const bytes = Buffer.from(`${JSON.stringify(record)}\n`);
        nativeAdapter().atomicWritePrivateFile(proof.cachePath, bytes, {
          ensureParent: true, maxBytes: PRIVATE_JSON_LIMIT,
        });
      },
      treeDigest(path, excludeRootFile) {
        const proof = nativeAdapter().digestPrivateTree(path, {
          excludeRootFile, maximumDepth: TREE_MAX_DEPTH, maximumEntries: TREE_MAX_ENTRIES,
          maximumTotalBytes: TREE_MAX_BYTES,
        });
        return nativeTreeDigest(proof);
      },
      readPrivateFile(path, maxBytes = PRIVATE_JSON_LIMIT) {
        const bytes = nativeAdapter().readPrivateFile(path, { minBytes: 1, maxBytes });
        if (!Buffer.isBuffer(bytes) || bytes.length < 1 || bytes.length > maxBytes) {
          throw new Error('Pulse product private state is unsafe');
        }
        return bytes;
      },
      assertTreeEntry(path, relative, expectedKind) {
        const info = lstatSync(path);
        if (info.isSymbolicLink() ||
            (expectedKind === 'directory' ? !info.isDirectory() : !info.isFile())) {
          throw new Error(`Pulse trusted tree entry is unsafe: ${relative}`);
        }
        return info;
      },
      validateTree(path, entries, totalBytes) {
        const proof = nativeAdapter().inspectPrivateTree(path, {
          entries, maximumDepth: TREE_MAX_DEPTH, maximumEntries: TREE_MAX_ENTRIES,
          maximumTotalBytes: TREE_MAX_BYTES,
        });
        if (!exactObject(proof, ['bytes', 'files']) || proof.bytes !== totalBytes || proof.files !== entries.length) {
          throw new Error('Pulse trusted tree native proof is invalid');
        }
      },
      executableDigest(path) {
        return nativeExecutableDigest(nativeAdapter().inspectExecutable(path));
      },
      readPrivateFiles(requests) {
        const results = nativeAdapter().batch(requests.map(({ path, maxBytes = PRIVATE_JSON_LIMIT }) => ({
          operation: 'read_private_file',
          payload: { encoding: '', maximum_bytes: maxBytes, minimum_bytes: 1, path },
        })));
        return results.map((proof, index) => nativePrivateBytes(
          proof, requests[index].maxBytes ?? PRIVATE_JSON_LIMIT,
        ));
      },
      productActivationProof({
        hostAccessPath, activationPath, runtimeRoot, runtimeManifestPath, pluginRoot,
      }) {
        // This ordinary read supplies only the daemon path for routing the native
        // batch. The protected bytes returned below remain the sole authority.
        const daemonPath = preliminaryDaemonPath(activationPath);
        const results = nativeAdapter().batch([
          {
            operation: 'read_private_file',
            payload: { encoding: '', maximum_bytes: PRIVATE_JSON_LIMIT, minimum_bytes: 1, path: hostAccessPath },
          },
          {
            operation: 'read_private_file',
            payload: { encoding: '', maximum_bytes: PRIVATE_JSON_LIMIT, minimum_bytes: 1, path: activationPath },
          },
          {
            operation: 'digest_private_tree',
            payload: {
              exclude_root_file: 'runtime-manifest.json', maximum_depth: TREE_MAX_DEPTH,
              maximum_entries: TREE_MAX_ENTRIES, maximum_total_bytes: TREE_MAX_BYTES, path: runtimeRoot,
            },
          },
          {
            operation: 'read_private_file',
            payload: { encoding: '', maximum_bytes: PRIVATE_JSON_LIMIT, minimum_bytes: 1, path: runtimeManifestPath },
          },
          {
            operation: 'digest_private_tree',
            payload: {
              exclude_root_file: '', maximum_depth: TREE_MAX_DEPTH,
              maximum_entries: TREE_MAX_ENTRIES, maximum_total_bytes: TREE_MAX_BYTES, path: pluginRoot,
            },
          },
          { operation: 'inspect_executable', payload: { path: daemonPath } },
        ]);
        return Object.freeze({
          activationBytes: nativePrivateBytes(results[1]),
          daemonDigest: nativeExecutableDigest(results[5]),
          daemonPath,
          hostAccessBytes: nativePrivateBytes(results[0]),
          pluginDigest: nativeTreeDigest(results[4]),
          runtimeDigest: nativeTreeDigest(results[2]),
          runtimeManifestBytes: nativePrivateBytes(results[3]),
        });
      },
      productEdgeProof({ runtimeRoot, runtimeManifestPath, pluginRoot, daemonPath }) {
        const results = nativeAdapter().batch([
          {
            operation: 'digest_private_tree',
            payload: {
              exclude_root_file: 'runtime-manifest.json', maximum_depth: TREE_MAX_DEPTH,
              maximum_entries: TREE_MAX_ENTRIES, maximum_total_bytes: TREE_MAX_BYTES, path: runtimeRoot,
            },
          },
          {
            operation: 'read_private_file',
            payload: { encoding: '', maximum_bytes: PRIVATE_JSON_LIMIT, minimum_bytes: 1, path: runtimeManifestPath },
          },
          {
            operation: 'digest_private_tree',
            payload: {
              exclude_root_file: '', maximum_depth: TREE_MAX_DEPTH,
              maximum_entries: TREE_MAX_ENTRIES, maximum_total_bytes: TREE_MAX_BYTES, path: pluginRoot,
            },
          },
          { operation: 'inspect_executable', payload: { path: daemonPath } },
        ]);
        return Object.freeze({
          daemonDigest: nativeExecutableDigest(results[3]),
          pluginDigest: nativeTreeDigest(results[2]),
          runtimeDigest: nativeTreeDigest(results[0]),
          runtimeManifestBytes: nativePrivateBytes(results[1]),
        });
      },
    });
  }
  return Object.freeze({
    platform,
    workspaceIdentity(path) {
      const info = lstatSync(path);
      if (!info.isDirectory() || info.isSymbolicLink()) {
        throw new Error('Pulse workspace identity proof is invalid');
      }
      return `${info.dev}:${info.ino}`;
    },
    readPrivateFile(path, maxBytes = PRIVATE_JSON_LIMIT) {
      const info = lstatSync(path);
      const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
      if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.uid !== currentUID ||
          (info.mode & 0o077) !== 0 || info.size < 1 || info.size > maxBytes) {
        throw new Error('Pulse product private state is unsafe');
      }
      const bytes = readFileSync(path);
      if (bytes.length !== info.size) throw new Error('Pulse product private state changed while reading');
      return bytes;
    },
    assertTreeEntry(path, relative, expectedKind) {
      const info = lstatSync(path);
      const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
      if (info.isSymbolicLink() || info.uid !== currentUID || (info.mode & 0o022) !== 0 ||
          (expectedKind === 'directory' ? !info.isDirectory() : !info.isFile())) {
        throw new Error(`Pulse trusted tree entry is unsafe: ${relative}`);
      }
      return info;
    },
    validateTree() {},
    executableDigest(path) {
      const info = lstatSync(path);
      const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
      if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.uid !== currentUID ||
          (info.mode & 0o077) !== 0 || (info.mode & 0o111) === 0) {
        throw new Error('Pulse product executable is unsafe');
      }
      return createHash('sha256').update(readFileSync(path)).digest('hex');
    },
  });
}

function readPrivateJSON(trust, path, maxBytes = PRIVATE_JSON_LIMIT) {
  try {
    return JSON.parse(trust.readPrivateFile(path, maxBytes).toString('utf8'));
  } catch (error) {
    if (error instanceof SyntaxError) throw new Error('Pulse product private JSON is invalid');
    throw error;
  }
}

function parsePrivateJSON(bytes) {
  try {
    return JSON.parse(bytes.toString('utf8'));
  } catch (error) {
    if (error instanceof SyntaxError) throw new Error('Pulse product private JSON is invalid');
    throw error;
  }
}

// The plugin owns this verification because code inside the installed runtime
// cannot establish its own integrity after Node has already executed it.
function trustedTreeDigest(root, { label, excludeRootFile, trust = createTrustServices() } = {}) {
  if (typeof trust.treeDigest === 'function') return trust.treeDigest(root, excludeRootFile);
  trust.assertTreeEntry(root, '.', 'directory');
  const hash = createHash('sha256');
  const entries = [];
  let totalBytes = 0;
  let visited = 0;
  const visit = (directory, prefix = '', depth = 0) => {
    if (depth > TREE_MAX_DEPTH) throw new Error(`${label} exceeds the trusted tree depth limit`);
    for (const name of readdirSync(directory).sort()) {
      const excludedFromDigest = prefix === '' && name === excludeRootFile;
      visited++;
      if (visited > TREE_MAX_ENTRIES) throw new Error(`${label} exceeds the trusted tree entry limit`);
      const path = join(directory, name);
      const relative = prefix ? `${prefix}/${name}` : name;
      const preliminary = lstatSync(path);
      if (preliminary.isSymbolicLink()) throw new Error(`${label} contains a symlink: ${relative}`);
      if (preliminary.isDirectory()) {
        trust.assertTreeEntry(path, relative, 'directory');
        visit(path, relative, depth + 1);
      } else if (preliminary.isFile()) {
        trust.assertTreeEntry(path, relative, 'file');
        if (!Number.isSafeInteger(preliminary.size) || preliminary.size < 0 ||
            preliminary.size > 512 * 1024 * 1024 || totalBytes + preliminary.size > TREE_MAX_BYTES) {
          throw new Error(`${label} exceeds the trusted tree byte limit`);
        }
        const bytes = readFileSync(path);
        if (bytes.length !== preliminary.size) throw new Error(`${label} changed while reading: ${relative}`);
        totalBytes += bytes.length;
        if (!Number.isSafeInteger(totalBytes) || totalBytes > TREE_MAX_BYTES) {
          throw new Error(`${label} exceeds the trusted tree byte limit`);
        }
        const sha256 = createHash('sha256').update(bytes).digest('hex');
        entries.push({ path: relative, bytes: bytes.length, sha256, executable: false });
        if (!excludedFromDigest) {
          hash.update(relative);
          hash.update('\x00');
          hash.update(bytes);
          hash.update('\x00');
        }
      } else {
        throw new Error(`${label} contains an unsupported entry: ${relative}`);
      }
    }
  };
  visit(root);
  if (entries.length < 1) throw new Error(`${label} is empty`);
  trust.validateTree(root, entries, totalBytes);
  return hash.digest('hex');
}

function runtimeTreeDigest(root, trust) {
  return trustedTreeDigest(root, {
    label: 'Pulse trusted runtime', excludeRootFile: 'runtime-manifest.json', trust,
  });
}

function pluginTreeDigest(root, trust) {
  return trustedTreeDigest(root, { label: 'Pulse trusted plugin', trust });
}

export function resolveProductEnvironment({
  cwd = process.cwd(),
  env = process.env,
  host,
  integrity = 'refresh',
  platform = process.platform,
  architecture = process.arch,
  windowsAdapter,
} = {}) {
  if (!['claude-code', 'codex', 'cursor'].includes(host)) {
    throw new Error('Pulse product host identity is missing or invalid.');
  }
  if (!['refresh', 'reuse'].includes(integrity)) {
    throw new Error('Pulse product integrity mode is missing or invalid.');
  }
  const trust = createTrustServices({ platform, architecture, windowsAdapter });
  const productHome = resolve(env.PULSE_HOME || join(homedir(), '.pulse'));
  const codexHome = resolve(env.CODEX_HOME || join(homedir(), '.codex'));
  const sharedPath = join(productHome, 'product-locators.json');
  const legacyCodexPath = join(codexHome, 'pulse', 'product-locators.json');
  const locatorPath = existsSync(sharedPath) ? sharedPath : legacyCodexPath;
  const canonical = canonicalWorkspace(cwd);
  const pluginRoot = dirname(fileURLToPath(import.meta.url));
  const proofInput = {
      host, locatorPath, pluginRoot, productHome, workspacePath: canonical,
  };
  const cachedEnvironmentProof = integrity === 'reuse' &&
    typeof trust.productEnvironmentCacheProof === 'function'
    ? trust.productEnvironmentCacheProof(proofInput)
    : undefined;
  const environmentProof = cachedEnvironmentProof ?? (typeof trust.productEnvironmentProof === 'function'
    ? trust.productEnvironmentProof(proofInput)
    : undefined);
  const locatorProof = environmentProof ?? (typeof trust.workspaceLocatorProof === 'function'
    ? trust.workspaceLocatorProof(locatorPath, canonical)
    : undefined);
  const locator = locatorProof
    ? parsePrivateJSON(locatorProof.locatorBytes)
    : readPrivateJSON(trust, locatorPath);
  const validLocatorSchema = locatorPath === sharedPath
    ? locator?.schema === 'pulse.product_locators.v1'
    : locator?.schema === 'pulse.codex_product_locators.v1';
  let selected;
  const locatorTrust = locatorProof
    ? { workspaceIdentity: () => locatorProof.workspaceIdentity }
    : trust;
  try { selected = validLocatorSchema ? selectLocatorEntry(locator, canonical, locatorTrust) : undefined; } catch {}
  const entry = selected?.entry;
  const key = selected?.key;
  const allowed = [
    'anchor_path', 'data_dir', 'registry_path', 'public_key_path', 'trust_mode',
    'workspace_digest', 'workspace_id', 'workspace_path',
  ];
  if (!entry || entry.workspace_digest !== key || workspaceDigest(entry.workspace_path) !== key ||
      Object.keys(entry).length !== allowed.length || Object.keys(entry).some((name) => !allowed.includes(name)) ||
      !['production', 'test'].includes(entry.trust_mode) ||
      !/^workspace_[a-f0-9]{32}$/.test(entry.workspace_id ?? '') ||
      ![entry.workspace_path, entry.data_dir, entry.registry_path, entry.public_key_path, entry.anchor_path]
        .every((value) => typeof value === 'string' && isAbsolute(value))) {
    throw new Error('Pulse product locator is missing or invalid for this workspace; run `pulse install` again.');
  }
  if (entry.trust_mode === 'test' && env.PULSE_TRUST_MODE !== 'test') {
    throw new Error('Pulse synthetic test locator requires an explicitly test-mode host process; production trust is not active.');
  }
  if (entry.trust_mode === 'production' && env.PULSE_TRUST_MODE === 'test') {
    throw new Error('Pulse host trust mode does not match the production product locator.');
  }
  if (environmentProof && (key !== environmentProof.locatorKey ||
      resolve(entry.data_dir) !== environmentProof.dataDir)) {
    throw new Error('Pulse product locator changed during native integrity validation.');
  }
  const dataDir = resolve(entry.data_dir);
  const hostAccessPath = join(productHome, 'product-host-access', key, `${host}.json`);
  const activationPath = join(dataDir, 'runtime', 'product-daemon.json');
  const expectedRuntimePath = join(dataDir, 'runtime', 'codex', 'current', 'src', 'cli.js');
  const runtimeRoot = resolve(expectedRuntimePath, '..', '..');
  const runtimeManifestPath = join(runtimeRoot, 'runtime-manifest.json');
  const activationProof = environmentProof ?? (typeof trust.productActivationProof === 'function'
    ? trust.productActivationProof({
      activationPath, hostAccessPath, pluginRoot, runtimeManifestPath, runtimeRoot,
    })
    : undefined);
  if (environmentProof && (activationPath !== environmentProof.activationPath ||
      hostAccessPath !== environmentProof.hostAccessPath ||
      expectedRuntimePath !== environmentProof.expectedRuntimePath ||
      runtimeManifestPath !== environmentProof.runtimeManifestPath ||
      runtimeRoot !== environmentProof.runtimeRoot)) {
    throw new Error('Pulse product environment changed during native integrity validation.');
  }
  const [hostAccess, activation] = activationProof
    ? [parsePrivateJSON(activationProof.hostAccessBytes), parsePrivateJSON(activationProof.activationBytes)]
    : typeof trust.readPrivateFiles === 'function'
    ? trust.readPrivateFiles([
      { path: hostAccessPath, maxBytes: PRIVATE_JSON_LIMIT },
      { path: activationPath, maxBytes: PRIVATE_JSON_LIMIT },
    ]).map(parsePrivateJSON)
    : [readPrivateJSON(trust, hostAccessPath), readPrivateJSON(trust, activationPath)];
  if (hostAccess?.schema !== 'pulse.product_host_access.v1' || hostAccess.host !== host ||
      hostAccess.workspace_digest !== key ||
      Object.keys(hostAccess).sort().join('\0') !== ['host', 'schema', 'workspace_digest'].sort().join('\0')) {
    throw new Error(`Pulse ${host} integration is disconnected; run pulse install or pulse repair.`);
  }
  const activationKeys = [
    'activated_at',
    'daemon_activation_digest', 'daemon_artifact_id', 'daemon_artifact_sha256', 'daemon_digest', 'daemon_path', 'daemon_tree_digest',
    'embedder_runtime_activation_digest', 'embedder_runtime_artifact_id', 'embedder_runtime_artifact_sha256', 'embedder_runtime_tree_digest',
    'model_activation_digest', 'model_artifact_id', 'model_artifact_sha256', 'model_tree_digest',
    'plugin_runtime_activation_digest', 'plugin_runtime_artifact_id', 'plugin_runtime_artifact_sha256', 'plugin_runtime_tree_digest',
    'plugin_tree_digest', 'release_epoch', 'release_manifest_digest', 'release_version',
    'runtime_path', 'runtime_tree_digest', 'schema',
  ];
  const activationDigests = [
    'daemon_activation_digest', 'daemon_artifact_sha256', 'daemon_digest', 'daemon_tree_digest',
    'embedder_runtime_activation_digest', 'embedder_runtime_artifact_sha256', 'embedder_runtime_tree_digest',
    'model_activation_digest', 'model_artifact_sha256', 'model_tree_digest',
    'plugin_runtime_activation_digest', 'plugin_runtime_artifact_sha256', 'plugin_runtime_tree_digest',
    'plugin_tree_digest', 'release_manifest_digest', 'runtime_tree_digest',
  ];
  const artifactIDs = ['daemon_artifact_id', 'embedder_runtime_artifact_id', 'model_artifact_id', 'plugin_runtime_artifact_id'];
  if (activation?.schema !== 'pulse.product_activation.v4' ||
      Object.keys(activation).length !== activationKeys.length ||
      Object.keys(activation).some((name) => !activationKeys.includes(name)) ||
      ![activation.daemon_path, activation.runtime_path]
        .every((value) => typeof value === 'string' && isAbsolute(value)) ||
      !activationDigests.every((name) => SHA256.test(activation[name] ?? '')) ||
      !artifactIDs.every((name) => /^[a-z0-9][a-z0-9._-]{0,127}$/.test(activation[name] ?? '')) ||
      typeof activation.release_version !== 'string' || activation.release_version.length < 1 ||
      !Number.isSafeInteger(activation.release_epoch) || activation.release_epoch < 1 ||
      typeof activation.activated_at !== 'string' || Number.isNaN(Date.parse(activation.activated_at))) {
    throw new Error('Pulse product activation is missing or invalid; reconnect this workspace.');
  }
  if (resolve(activation.runtime_path) !== resolve(expectedRuntimePath)) {
    throw new Error('Pulse product activation runtime does not match the shared runtime root.');
  }
  if (activationProof && resolve(activation.daemon_path) !== activationProof.daemonPath) {
    throw new Error('Pulse product activation changed during native integrity validation.');
  }
  const edgeProof = activationProof ?? (typeof trust.productEdgeProof === 'function'
    ? trust.productEdgeProof({
      daemonPath: resolve(activation.daemon_path), pluginRoot, runtimeManifestPath, runtimeRoot,
    })
    : undefined);
  const actualRuntimeDigest = edgeProof?.runtimeDigest ?? runtimeTreeDigest(runtimeRoot, trust);
  const runtimeManifest = edgeProof
    ? parsePrivateJSON(edgeProof.runtimeManifestBytes)
    : readPrivateJSON(trust, runtimeManifestPath);
  if (runtimeManifest?.schema !== 'pulse.codex_runtime.v2' ||
      runtimeManifest.entrypoint !== 'src/cli.js' ||
      runtimeManifest.tree_digest !== activation.runtime_tree_digest ||
      runtimeManifest.release_manifest_digest !== activation.release_manifest_digest ||
      runtimeManifest.release_version !== activation.release_version ||
      runtimeManifest.release_epoch !== activation.release_epoch ||
      runtimeManifest.plugin_runtime_artifact_id !== activation.plugin_runtime_artifact_id ||
      runtimeManifest.plugin_runtime_artifact_sha256 !== activation.plugin_runtime_artifact_sha256 ||
      runtimeManifest.plugin_runtime_activation_digest !== activation.plugin_runtime_activation_digest ||
      runtimeManifest.plugin_runtime_tree_digest !== activation.plugin_runtime_tree_digest ||
      runtimeManifest.plugin_tree_digest !== activation.plugin_tree_digest ||
      actualRuntimeDigest !== activation.runtime_tree_digest) {
    throw new Error('Pulse product runtime and activation are out of sync; retry after activation completes.');
  }
  const actualPluginDigest = edgeProof?.pluginDigest ?? pluginTreeDigest(pluginRoot, trust);
  if (actualPluginDigest !== activation.plugin_tree_digest) {
    throw new Error('Pulse installed plugin does not match the signed product activation.');
  }
  const actualDaemonDigest = edgeProof?.daemonDigest ?? trust.executableDigest(resolve(activation.daemon_path));
  if (actualDaemonDigest !== activation.daemon_digest) {
    throw new Error('Pulse product daemon activation failed integrity validation.');
  }
  const productEnvironment = {
    PULSE_DATA_DIR: dataDir,
    PULSE_RUNTIME_PATH: activation.runtime_path,
    PULSE_RUNTIME_DIGEST: activation.runtime_tree_digest,
    PULSE_PLUGIN_TREE_DIGEST: activation.plugin_tree_digest,
    PULSE_RELEASE_MANIFEST_DIGEST: activation.release_manifest_digest,
  };
  if (entry.trust_mode === 'test') {
    productEnvironment.PULSE_TRUST_MODE = 'test';
    productEnvironment.PULSE_BINDING_REGISTRY_PATH = entry.registry_path;
    productEnvironment.PULSE_BINDING_PUBLIC_KEY_PATH = entry.public_key_path;
    productEnvironment.PULSE_BINDING_ANCHOR_PATH = entry.anchor_path;
  }
  if (environmentProof && environmentProof.integrityCacheHit === false &&
      typeof trust.writeProductEnvironmentCache === 'function') {
    trust.writeProductEnvironmentCache(environmentProof, host);
  }
  return productEnvironment;
}

export const __runtimeLocatorTest = Object.freeze({
  canonicalWorkspace,
  createTrustServices,
  selectLocatorEntry,
  trustedTreeDigest,
  workspaceDigest,
  workspaceID,
});
