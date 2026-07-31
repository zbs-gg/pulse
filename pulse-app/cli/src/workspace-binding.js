import { createHash, createPublicKey, verify } from 'node:crypto';
import { realpathSync } from 'node:fs';
import { homedir } from 'node:os';
import { isAbsolute, join, resolve } from 'node:path';

import { defaultPlatformServices } from './platform-services.js';

const REGISTRY_SCHEMA = 'pulse.workspace-binding-registry.v1';
const ANCHOR_SCHEMA = 'pulse.workspace-binding-anchor.v1';

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

function gitValue(cwd, platformServices, ...args) {
  try {
    return platformServices.runGit(cwd, args);
  } catch {
    throw new BindingError('workspace_not_git', 'workspace must be inside a Git checkout');
  }
}

export function canonicalizeWorkspace(inputPath, { platformServices = defaultPlatformServices } = {}) {
  const requestedPath = realpathSync(resolve(inputPath));
  const topLevel = realpathSync(gitValue(requestedPath, platformServices, 'rev-parse', '--show-toplevel'));
  const gitDirRaw = gitValue(topLevel, platformServices, 'rev-parse', '--git-dir');
  const commonDirRaw = gitValue(topLevel, platformServices, 'rev-parse', '--git-common-dir');
  const gitDir = realpathSync(isAbsolute(gitDirRaw) ? gitDirRaw : resolve(topLevel, gitDirRaw));
  const commonDir = realpathSync(isAbsolute(commonDirRaw) ? commonDirRaw : resolve(topLevel, commonDirRaw));
  let checkoutIdentity;
  let repositoryIdentity;
  try {
    checkoutIdentity = platformServices.inspectPathIdentity(topLevel, { kind: 'directory' }).identity_token;
    repositoryIdentity = platformServices.inspectPathIdentity(commonDir, { kind: 'directory' }).identity_token;
  } catch {
    throw new BindingError('workspace_identity_unsafe', 'workspace filesystem identity cannot be proven');
  }
  return {
    schema: 'pulse.workspace-identity.v1',
    workspace_id: `workspace_${digest('pulse-workspace-v1', checkoutIdentity).slice(0, 32)}`,
    repository_id: `repository_${digest('pulse-repository-v1', repositoryIdentity).slice(0, 32)}`,
    canonical_path: topLevel,
    git_common_dir: commonDir,
    checkout_kind: gitDir === commonDir ? 'primary' : 'worktree',
  };
}

function readOwnerIntegrityFile(
  path, code, { rootOnly = false, encoding = null, maxBytes = 64 * 1024 * 1024,
    platformServices = defaultPlatformServices } = {},
) {
  const absolute = resolve(path);
  try {
    return {
      absolute,
      bytes: platformServices.readIntegrityFile(absolute, {
        owner: rootOnly ? 'root' : 'current', encoding, maxBytes,
      }),
    };
  } catch {
    throw new BindingError(code, 'binding trust file must be owner-controlled and not group/world writable');
  }
}

export function defaultBindingPaths(home = homedir()) {
  return {
    registryPath: join(home, '.pulse', 'supervisor', 'workspace-bindings.json'),
    publicKeyPath: '/Library/Application Support/Pulse/trust/workspace-bindings.pub.pem',
    anchorPath: '/Library/Application Support/Pulse/trust/workspace-bindings.anchor.json',
  };
}

export function bindingRegistryAnchor(registryBytes, epoch) {
  if (!Buffer.isBuffer(registryBytes) || registryBytes.length < 1 ||
      !Number.isSafeInteger(epoch) || epoch < 1) {
    throw new BindingError('binding_anchor_invalid', 'binding registry anchor input is invalid');
  }
  return {
    schema: ANCHOR_SCHEMA,
    epoch,
    registry_sha256: createHash('sha256').update(registryBytes).digest('hex'),
  };
}

export function verifyBindingRegistryAnchor({
  registryPath,
  anchorPath,
  registryEpoch,
  rootAnchor = false,
  platformServices = defaultPlatformServices,
}) {
  const { bytes: registryBytes } = readOwnerIntegrityFile(registryPath, 'binding_registry_unsafe', { platformServices });
  const { bytes: anchorBytes } = readOwnerIntegrityFile(anchorPath, 'binding_anchor_unsafe', {
    rootOnly: rootAnchor, platformServices,
  });
  let anchor;
  try {
    anchor = JSON.parse(anchorBytes);
  } catch {
    throw new BindingError('binding_anchor_invalid', 'binding registry anchor is not valid JSON');
  }
  const expectedKeys = ['epoch', 'registry_sha256', 'schema'];
  if (!anchor || typeof anchor !== 'object' || Array.isArray(anchor) ||
      Object.keys(anchor).sort().join('\0') !== expectedKeys.join('\0') ||
      anchor.schema !== ANCHOR_SCHEMA || !Number.isSafeInteger(anchor.epoch) || anchor.epoch < 1 ||
      !/^[a-f0-9]{64}$/.test(anchor.registry_sha256 ?? '')) {
    throw new BindingError('binding_anchor_invalid', 'binding registry anchor has an unsupported schema');
  }
  const canonicalBytes = Buffer.from(`${canonicalJSONStringify(anchor)}\n`);
  if (!anchorBytes.equals(canonicalBytes)) {
    throw new BindingError('binding_anchor_invalid', 'binding registry anchor is not canonical');
  }
  const expected = bindingRegistryAnchor(registryBytes, registryEpoch);
  if (anchor.epoch !== registryEpoch || anchor.registry_sha256 !== expected.registry_sha256) {
    throw new BindingError('binding_anchor_mismatch', 'binding registry does not match its root-owned anti-rollback anchor');
  }
  return anchor;
}

export function verifyBindingRegistry({
  registryPath, publicKeyPath, rootPublicKey = false, platformServices = defaultPlatformServices,
}) {
  const { bytes: registryBytes } = readOwnerIntegrityFile(registryPath, 'binding_registry_unsafe', {
    encoding: 'utf8', platformServices,
  });
  const { bytes: publicKeyBytes } = readOwnerIntegrityFile(publicKeyPath, 'binding_key_unsafe', {
    rootOnly: rootPublicKey, platformServices,
  });
  let envelope;
  try {
    envelope = JSON.parse(registryBytes);
  } catch {
    throw new BindingError('binding_registry_invalid', 'binding registry is not valid JSON');
  }
  if (!envelope || typeof envelope !== 'object' || Array.isArray(envelope) ||
      typeof envelope.algorithm !== 'string' || typeof envelope.signature !== 'string' || !envelope.payload) {
    throw new BindingError('binding_registry_invalid', 'binding registry envelope is incomplete');
  }
  let publicKey;
  try {
    publicKey = createPublicKey(publicKeyBytes);
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
    throw new BindingError('binding_topology_invalid', 'Personal endpoint must be loopback HTTP');
  }
}

function validateBinding(binding, registryEpoch) {
  requireString(binding.binding_id, 'binding_id');
  requireString(binding.receipt_id, 'receipt_id');
  requireString(binding.principal_ref, 'principal_ref');
  if (!Number.isSafeInteger(binding.resolver_epoch) || binding.resolver_epoch < 1 || binding.resolver_epoch > registryEpoch) {
    throw new BindingError('binding_topology_invalid', 'binding resolver epoch is outside the registry history');
  }
  if (!binding.workspace || typeof binding.workspace !== 'object') {
    throw new BindingError('binding_topology_invalid', 'binding workspace identity is missing');
  }
  requireString(binding.workspace.workspace_id, 'workspace_id');
  requireString(binding.workspace.repository_id, 'repository_id');

  if (binding.mode !== 'personal' || !binding.personal || binding.desk || binding.commons) {
    throw new BindingError('binding_topology_invalid', 'Pulse Personal accepts only a Personal binding');
  }
  requireString(binding.personal.store_id, 'personal store_id');
  requireString(binding.personal.data_dir, 'personal data_dir');
  requireString(binding.personal.credential_ref, 'personal credential_ref');
  requireString(binding.personal.cache_dir, 'personal cache_dir');
  requireLocalEndpoint(binding.personal.base_url);
  return binding;
}

export function resolveWorkspaceBinding({
  cwd = process.cwd(),
  registryPath,
  publicKeyPath,
  anchorPath,
  rootAnchor = anchorPath === undefined,
  platformServices = defaultPlatformServices,
} = {}) {
  const workspace = canonicalizeWorkspace(cwd, { platformServices });
  const defaults = defaultBindingPaths();
  const selectedRegistry = realpathSync(resolve(registryPath ?? defaults.registryPath));
  const selectedPublicKey = realpathSync(resolve(publicKeyPath ?? defaults.publicKeyPath));
  const selectedAnchor = realpathSync(resolve(anchorPath ?? defaults.anchorPath));
  if (platformServices.isPathInside(selectedRegistry, workspace.canonical_path) ||
      platformServices.isPathInside(selectedPublicKey, workspace.canonical_path) ||
      platformServices.isPathInside(selectedAnchor, workspace.canonical_path)) {
    throw new BindingError('binding_registry_in_workspace', 'binding registry, trust key, and anti-rollback anchor must live outside the workspace');
  }
  const registry = verifyBindingRegistry({
    registryPath: selectedRegistry,
    publicKeyPath: selectedPublicKey,
    rootPublicKey: publicKeyPath === undefined,
    platformServices,
  });
  verifyBindingRegistryAnchor({
    registryPath: selectedRegistry,
    anchorPath: selectedAnchor,
    registryEpoch: registry.epoch,
    rootAnchor,
    platformServices,
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

export function inspectWorkspaceBinding(options = {}) {
  const cwd = options.cwd ?? process.cwd();
  const platformServices = options.platformServices ?? defaultPlatformServices;
  try {
    return { status: 'bound', binding: resolveWorkspaceBinding(options) };
  } catch (error) {
    if (!(error instanceof BindingError) ||
        (error.code !== 'binding_missing' && error.code !== 'workspace_not_git')) {
      throw error;
    }
    const workspace = error.code === 'binding_missing'
      ? canonicalizeWorkspace(cwd, { platformServices })
      : { canonical_path: realpathSync(resolve(cwd)) };
    return {
      status: 'unassigned',
      reason: error.code,
      workspace,
    };
  }
}
