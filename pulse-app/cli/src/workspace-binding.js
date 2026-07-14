import { createHash, createPublicKey, verify } from 'node:crypto';
import { lstatSync, readFileSync, realpathSync, statSync } from 'node:fs';
import { homedir } from 'node:os';
import { isAbsolute, join, relative, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';

const REGISTRY_SCHEMA = 'pulse.workspace-binding-registry.v1';

export class BindingError extends Error {
  constructor(code, message) {
    super(message);
    this.name = 'BindingError';
    this.code = code;
  }
}

export function canonicalJSONStringify(value) {
  return JSON.stringify(canonicalValue(value));
}

function canonicalValue(value) {
  if (value === null || typeof value === 'boolean') return value;
  if (typeof value === 'string') return value.normalize('NFC');
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value)) {
      throw new BindingError('binding_canonical_invalid', 'binding numbers must be safe integers');
    }
    return value;
  }
  if (Array.isArray(value)) return value.map(canonicalValue);
  if (typeof value !== 'object') {
    throw new BindingError('binding_canonical_invalid', 'binding contains an unsupported value');
  }
  const out = {};
  for (const key of Object.keys(value).sort()) {
    if (!/^[a-z][a-z0-9_]*$/.test(key)) {
      throw new BindingError('binding_canonical_invalid', `binding key is not canonical: ${key}`);
    }
    out[key] = canonicalValue(value[key]);
  }
  return out;
}

function digest(label, ...parts) {
  const hash = createHash('sha256');
  hash.update(label);
  for (const part of parts) {
    hash.update('\0');
    hash.update(String(part));
  }
  return hash.digest('hex');
}

function gitValue(cwd, ...args) {
  const result = spawnSync('/usr/bin/git', ['-C', cwd, ...args], {
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  if (result.status !== 0) {
    throw new BindingError('workspace_not_git', 'workspace must be inside a Git checkout');
  }
  return result.stdout.trim();
}

function inodeIdentity(path) {
  const stat = statSync(path, { bigint: true });
  return `${stat.dev}:${stat.ino}`;
}

export function canonicalizeWorkspace(inputPath) {
  const requestedPath = realpathSync(resolve(inputPath));
  const topLevel = realpathSync(gitValue(requestedPath, 'rev-parse', '--show-toplevel'));
  const gitDirRaw = gitValue(topLevel, 'rev-parse', '--git-dir');
  const commonDirRaw = gitValue(topLevel, 'rev-parse', '--git-common-dir');
  const gitDir = realpathSync(isAbsolute(gitDirRaw) ? gitDirRaw : resolve(topLevel, gitDirRaw));
  const commonDir = realpathSync(isAbsolute(commonDirRaw) ? commonDirRaw : resolve(topLevel, commonDirRaw));
  const checkoutIdentity = inodeIdentity(topLevel);
  const repositoryIdentity = inodeIdentity(commonDir);
  return {
    schema: 'pulse.workspace-identity.v1',
    workspace_id: `workspace_${digest('pulse-workspace-v1', checkoutIdentity).slice(0, 32)}`,
    repository_id: `repository_${digest('pulse-repository-v1', repositoryIdentity).slice(0, 32)}`,
    canonical_path: topLevel,
    git_common_dir: commonDir,
    checkout_kind: gitDir === commonDir ? 'primary' : 'worktree',
  };
}

function pathInside(path, parent) {
  const rel = relative(parent, path);
  return rel === '' || (!rel.startsWith(`..${process.platform === 'win32' ? '\\' : '/'}`) && rel !== '..' && !isAbsolute(rel));
}

function requireOwnerIntegrityFile(path, code, { rootOnly = false } = {}) {
  const absolute = resolve(path);
  const linkInfo = lstatSync(absolute);
  if (linkInfo.isSymbolicLink() || !linkInfo.isFile()) {
    throw new BindingError(code, 'binding trust file must be a regular non-symlink file');
  }
  const info = statSync(absolute);
  const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
  const ownerAllowed = rootOnly ? info.uid === 0 : info.uid === currentUID;
  if ((info.mode & 0o022) !== 0 || !ownerAllowed) {
    throw new BindingError(code, 'binding trust file must be owner-controlled and not group/world writable');
  }
  return absolute;
}

export function defaultBindingPaths(home = homedir()) {
  return {
    registryPath: join(home, '.pulse', 'supervisor', 'workspace-bindings.json'),
    publicKeyPath: '/Library/Application Support/Pulse/trust/workspace-bindings.pub.pem',
  };
}

export function verifyBindingRegistry({ registryPath, publicKeyPath, rootPublicKey = false }) {
  const safeRegistryPath = requireOwnerIntegrityFile(registryPath, 'binding_registry_unsafe');
  const safePublicKeyPath = requireOwnerIntegrityFile(publicKeyPath, 'binding_key_unsafe', { rootOnly: rootPublicKey });
  let envelope;
  try {
    envelope = JSON.parse(readFileSync(safeRegistryPath, 'utf8'));
  } catch {
    throw new BindingError('binding_registry_invalid', 'binding registry is not valid JSON');
  }
  if (!envelope || typeof envelope !== 'object' || Array.isArray(envelope) ||
      typeof envelope.algorithm !== 'string' || typeof envelope.signature !== 'string' || !envelope.payload) {
    throw new BindingError('binding_registry_invalid', 'binding registry envelope is incomplete');
  }
  let publicKey;
  try {
    publicKey = createPublicKey(readFileSync(safePublicKeyPath));
  } catch {
    throw new BindingError('binding_key_invalid', 'binding verification key is invalid');
  }
  let signature;
  try {
    signature = Buffer.from(envelope.signature, 'base64');
  } catch {
    throw new BindingError('binding_signature_invalid', 'binding registry signature is invalid');
  }
  const bytes = Buffer.from(canonicalJSONStringify(envelope.payload));
  const validSignature = envelope.algorithm === 'ed25519'
    ? signature.length === 64 && verify(null, bytes, publicKey, signature)
    : envelope.algorithm === 'es256' && verify('sha256', bytes, publicKey, signature);
  if (!validSignature) {
    throw new BindingError('binding_signature_invalid', 'binding registry signature does not verify');
  }
  if (envelope.payload.schema !== REGISTRY_SCHEMA || !Number.isSafeInteger(envelope.payload.epoch) ||
      envelope.payload.epoch < 1 || !Array.isArray(envelope.payload.bindings)) {
    throw new BindingError('binding_registry_invalid', 'binding registry payload has an unsupported schema');
  }
  return envelope.payload;
}

function requireString(value, name) {
  if (typeof value !== 'string' || value.length < 1 || value.trim() !== value || /[\x00-\x1f\x7f]/.test(value)) {
    throw new BindingError('binding_topology_invalid', `binding ${name} is invalid`);
  }
  return value;
}

function requireLocalEndpoint(value) {
  const url = new URL(requireString(value, 'local base_url'));
  if (url.protocol !== 'http:' || !['127.0.0.1', '[::1]', '::1'].includes(url.hostname) || url.username || url.password) {
    throw new BindingError('binding_topology_invalid', 'Desk endpoint must be loopback HTTP');
  }
}

function requireRemoteEndpoint(value) {
  const url = new URL(requireString(value, 'Commons resource'));
  if (url.protocol !== 'https:' || ['127.0.0.1', '[::1]', '::1', 'localhost'].includes(url.hostname) || url.username || url.password) {
    throw new BindingError('binding_topology_invalid', 'Commons resource must be a dedicated HTTPS deployment');
  }
}

function validateBinding(binding, registryEpoch) {
  requireString(binding.binding_id, 'binding_id');
  requireString(binding.receipt_id, 'receipt_id');
  requireString(binding.principal_ref, 'principal_ref');
  if (!Number.isSafeInteger(binding.resolver_epoch) || binding.resolver_epoch < 1 || binding.resolver_epoch !== registryEpoch) {
    throw new BindingError('binding_topology_invalid', 'binding resolver epoch does not match the registry');
  }
  if (!binding.workspace || typeof binding.workspace !== 'object') {
    throw new BindingError('binding_topology_invalid', 'binding workspace identity is missing');
  }
  requireString(binding.workspace.workspace_id, 'workspace_id');
  requireString(binding.workspace.repository_id, 'repository_id');

  if (binding.mode === 'personal') {
    if (!binding.personal || binding.desk || binding.commons) {
      throw new BindingError('binding_topology_invalid', 'Personal binding must contain only a Personal Vault');
    }
    requireString(binding.personal.store_id, 'personal store_id');
    requireString(binding.personal.data_dir, 'personal data_dir');
    requireString(binding.personal.credential_ref, 'personal credential_ref');
    requireString(binding.personal.cache_dir, 'personal cache_dir');
    requireLocalEndpoint(binding.personal.base_url);
  } else if (binding.mode === 'team') {
    if (!binding.desk || !binding.commons || binding.personal) {
      throw new BindingError('binding_topology_invalid', 'Team binding must contain one Desk and one Commons only');
    }
    for (const name of ['store_id', 'data_dir', 'credential_ref', 'cache_dir']) requireString(binding.desk[name], `desk ${name}`);
    for (const name of ['store_id', 'team_id', 'credential_ref', 'cache_partition']) requireString(binding.commons[name], `commons ${name}`);
    requireLocalEndpoint(binding.desk.base_url);
    requireRemoteEndpoint(binding.commons.resource);
    if (binding.desk.store_id === binding.commons.store_id || binding.desk.credential_ref === binding.commons.credential_ref ||
        binding.desk.cache_dir === binding.commons.cache_partition) {
      throw new BindingError('binding_topology_invalid', 'Desk and Commons resources must be distinct');
    }
  } else {
    throw new BindingError('binding_topology_invalid', 'binding mode must be personal or team');
  }
  return binding;
}

export function resolveWorkspaceBinding({ cwd = process.cwd(), registryPath, publicKeyPath } = {}) {
  const workspace = canonicalizeWorkspace(cwd);
  const defaults = defaultBindingPaths();
  const selectedRegistry = realpathSync(resolve(registryPath ?? defaults.registryPath));
  const selectedPublicKey = realpathSync(resolve(publicKeyPath ?? defaults.publicKeyPath));
  if (pathInside(selectedRegistry, workspace.canonical_path) || pathInside(selectedPublicKey, workspace.canonical_path)) {
    throw new BindingError('binding_registry_in_workspace', 'binding registry and trust key must live outside the workspace');
  }
  const registry = verifyBindingRegistry({
    registryPath: selectedRegistry,
    publicKeyPath: selectedPublicKey,
    rootPublicKey: publicKeyPath === undefined,
  });
  const matches = registry.bindings.filter((item) => item?.workspace?.workspace_id === workspace.workspace_id);
  if (matches.length === 0) {
    throw new BindingError('binding_missing', 'workspace has no trusted Pulse binding');
  }
  if (matches.length !== 1) {
    throw new BindingError('binding_ambiguous', 'workspace has multiple trusted Pulse bindings');
  }
  const binding = validateBinding(matches[0], registry.epoch);
  if (binding.workspace.repository_id !== workspace.repository_id) {
    throw new BindingError('binding_workspace_mismatch', 'workspace repository identity changed');
  }
  const bindingDigest = digest('pulse-binding-v1', canonicalJSONStringify(binding));
  return {
    ...binding,
    workspace,
    binding_digest: bindingDigest,
    registry_epoch: registry.epoch,
    fallback: false,
  };
}
