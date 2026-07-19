import { createHash } from 'node:crypto';
import { existsSync, lstatSync, readFileSync, readdirSync, realpathSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, isAbsolute, join, parse, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { loadPluginWindowsAdapter } from './windows-platform-adapter.mjs';

const SHA256 = /^[a-f0-9]{64}$/;
const PRIVATE_JSON_LIMIT = 1024 * 1024;
const TREE_MAX_ENTRIES = 100_000;
const TREE_MAX_DEPTH = 128;
const TREE_MAX_BYTES = 16 * 1024 * 1024 * 1024;

function exactObject(value, keys) {
  return value && !Array.isArray(value) && typeof value === 'object' &&
    Object.keys(value).sort().join('\0') === [...keys].sort().join('\0');
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
    const adapter = windowsAdapter ?? loadPluginWindowsAdapter({ architecture });
    return Object.freeze({
      platform,
      workspaceIdentity(path) {
        const proof = adapter.inspectPathIdentity(path, { kind: 'directory' });
        if (!exactObject(proof, ['canonical_path', 'identity_token', 'kind', 'reparse_point']) ||
            proof.kind !== 'directory' || proof.reparse_point !== false ||
            typeof proof.canonical_path !== 'string' || !isAbsolute(proof.canonical_path) ||
            typeof proof.identity_token !== 'string' || proof.identity_token.length < 1 ||
            proof.identity_token.length > 1024) {
          throw new Error('Pulse workspace identity native proof is invalid');
        }
        return proof.identity_token;
      },
      treeDigest(path, excludeRootFile) {
        const proof = adapter.digestPrivateTree(path, {
          excludeRootFile, maximumDepth: TREE_MAX_DEPTH, maximumEntries: TREE_MAX_ENTRIES,
          maximumTotalBytes: TREE_MAX_BYTES,
        });
        if (!exactObject(proof, ['bytes', 'files', 'tree_digest']) ||
            !Number.isSafeInteger(proof.bytes) || proof.bytes < 1 || proof.bytes > TREE_MAX_BYTES ||
            !Number.isSafeInteger(proof.files) || proof.files < 1 || proof.files > TREE_MAX_ENTRIES ||
            !SHA256.test(proof.tree_digest ?? '')) {
          throw new Error('Pulse trusted tree native digest proof is invalid');
        }
        return proof.tree_digest;
      },
      readPrivateFile(path, maxBytes = PRIVATE_JSON_LIMIT) {
        const bytes = adapter.readPrivateFile(path, { minBytes: 1, maxBytes });
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
        const proof = adapter.inspectPrivateTree(path, {
          entries, maximumDepth: TREE_MAX_DEPTH, maximumEntries: TREE_MAX_ENTRIES,
          maximumTotalBytes: TREE_MAX_BYTES,
        });
        if (!exactObject(proof, ['bytes', 'files']) || proof.bytes !== totalBytes || proof.files !== entries.length) {
          throw new Error('Pulse trusted tree native proof is invalid');
        }
      },
      executableDigest(path) {
        const proof = adapter.inspectExecutable(path);
        if (!exactObject(proof, [
          'canonical_path', 'executable', 'owner_only', 'regular_file', 'reparse_point', 'sha256',
        ]) || proof.executable !== true || proof.owner_only !== true || proof.regular_file !== true ||
            proof.reparse_point !== false || typeof proof.canonical_path !== 'string' ||
            !isAbsolute(proof.canonical_path) || !SHA256.test(proof.sha256 ?? '')) {
          throw new Error('Pulse product executable native proof is invalid');
        }
        return proof.sha256;
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
        const bytes = readFileSync(path);
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
  platform = process.platform,
  architecture = process.arch,
  windowsAdapter,
} = {}) {
  if (!['claude-code', 'codex', 'cursor'].includes(host)) {
    throw new Error('Pulse product host identity is missing or invalid.');
  }
  const trust = createTrustServices({ platform, architecture, windowsAdapter });
  const productHome = resolve(env.PULSE_HOME || join(homedir(), '.pulse'));
  const codexHome = resolve(env.CODEX_HOME || join(homedir(), '.codex'));
  const sharedPath = join(productHome, 'product-locators.json');
  const legacyCodexPath = join(codexHome, 'pulse', 'product-locators.json');
  const locatorPath = existsSync(sharedPath) ? sharedPath : legacyCodexPath;
  const locator = readPrivateJSON(trust, locatorPath);
  const canonical = canonicalWorkspace(cwd);
  const validLocatorSchema = locatorPath === sharedPath
    ? locator?.schema === 'pulse.product_locators.v1'
    : locator?.schema === 'pulse.codex_product_locators.v1';
  let selected;
  try { selected = validLocatorSchema ? selectLocatorEntry(locator, canonical, trust) : undefined; } catch {}
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
  const hostAccessPath = join(productHome, 'product-host-access', key, `${host}.json`);
  const hostAccess = readPrivateJSON(trust, hostAccessPath);
  if (hostAccess?.schema !== 'pulse.product_host_access.v1' || hostAccess.host !== host ||
      hostAccess.workspace_digest !== key ||
      Object.keys(hostAccess).sort().join('\0') !== ['host', 'schema', 'workspace_digest'].sort().join('\0')) {
    throw new Error(`Pulse ${host} integration is disconnected; run pulse install or pulse repair.`);
  }
  const activationPath = join(entry.data_dir, 'runtime', 'product-daemon.json');
  const activation = readPrivateJSON(trust, activationPath);
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
  const expectedRuntimePath = join(entry.data_dir, 'runtime', 'codex', 'current', 'src', 'cli.js');
  if (resolve(activation.runtime_path) !== resolve(expectedRuntimePath)) {
    throw new Error('Pulse product activation runtime does not match the shared runtime root.');
  }
  const runtimeRoot = resolve(activation.runtime_path, '..', '..');
  const actualRuntimeDigest = runtimeTreeDigest(runtimeRoot, trust);
  const runtimeManifest = readPrivateJSON(trust, join(runtimeRoot, 'runtime-manifest.json'));
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
  const pluginRoot = dirname(fileURLToPath(import.meta.url));
  if (pluginTreeDigest(pluginRoot, trust) !== activation.plugin_tree_digest) {
    throw new Error('Pulse installed plugin does not match the signed product activation.');
  }
  if (trust.executableDigest(resolve(activation.daemon_path)) !== activation.daemon_digest) {
    throw new Error('Pulse product daemon activation failed integrity validation.');
  }
  const productEnvironment = {
    PULSE_DATA_DIR: entry.data_dir,
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
