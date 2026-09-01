import { spawnSync } from 'node:child_process';
import {
  createHash,
  createPublicKey,
  generateKeyPairSync,
  randomBytes,
  sign as cryptoSign,
} from 'node:crypto';
import {
  existsSync, mkdirSync, rmSync, readFileSync, writeFileSync,
} from 'node:fs';
import { homedir } from 'node:os';
import { dirname, isAbsolute, join, resolve } from 'node:path';

import {
  canonicalJSONStringify,
  canonicalizeWorkspace,
  bindingRegistryAnchor,
  defaultBindingPaths,
  verifyBindingRegistryAnchor,
  verifyBindingRegistry,
} from './workspace-binding.js';
import { createPlatformServices, PlatformServicesError } from './platform-services.js';

const HELPER_PATH = '/Library/PrivilegedHelperTools/gg.zbs.pulse.presence-helper';
const ROOT_PUBLIC_KEY_PATH = '/Library/Application Support/Pulse/trust/workspace-bindings.pub.pem';
const ROOT_ANCHOR_PATH = '/Library/Application Support/Pulse/trust/workspace-bindings.anchor.json';
const SAFE_VAULT_ID = /^[a-z][a-z0-9_]{2,127}$/;
const TRANSACTION_SCHEMA = 'pulse.workspace-binding-transaction.v1';
const defaultPlatformServices = createPlatformServices();

function fail(code) {
  const error = new Error(`binding_admin_${code}`);
  error.code = `binding_admin_${code}`;
  throw error;
}

function exactID(value, code) {
  if (typeof value !== 'string' || !SAFE_VAULT_ID.test(value)) fail(code);
  return value;
}

function exactPort(value) {
  if (value === undefined) return undefined;
  if (!Number.isInteger(value) || value < 1024 || value > 65535) fail('port_invalid');
  return value;
}

function bindingLocalPort(binding) {
  const raw = binding?.personal?.base_url;
  try {
    const value = Number.parseInt(new URL(raw).port, 10);
    return Number.isInteger(value) ? value : undefined;
  } catch {
    return undefined;
  }
}

function selectLocalPort(requested, bindings, workspaceID) {
  const occupied = new Set(bindings
    .filter((binding) => binding?.workspace?.workspace_id !== workspaceID)
    .map(bindingLocalPort)
    .filter(Number.isInteger));
  if (requested !== undefined) {
    if (occupied.has(requested)) fail('port_in_use');
    return requested;
  }
  for (let candidate = 18789; candidate <= 19788; candidate += 1) {
    if (!occupied.has(candidate)) return candidate;
  }
  fail('port_exhausted');
}

function exactPersonalTopology(personal, home) {
  if (!personal || typeof personal !== 'object' || Array.isArray(personal)) {
    fail('personal_store_invalid');
  }
  const storeID = exactID(personal.store_id, 'personal_store_invalid');
  const expected = {
    store_id: storeID,
    data_dir: join(home, '.pulse', 'vaults', 'personal', storeID),
    base_url: personal.base_url,
    credential_ref: `keychain:pulse/local/${storeID}`,
    cache_dir: join(home, '.pulse', 'caches', 'personal', storeID),
  };
  let port;
  try {
    const endpoint = new URL(personal.base_url);
    port = Number.parseInt(endpoint.port, 10);
    if (endpoint.protocol !== 'http:' || endpoint.hostname !== '127.0.0.1' ||
        endpoint.pathname !== '/' || endpoint.search || endpoint.hash ||
        !Number.isInteger(port) || port < 1024 || port > 65535) {
      fail('personal_store_invalid');
    }
  } catch (error) {
    if (error?.code === 'binding_admin_personal_store_invalid') throw error;
    fail('personal_store_invalid');
  }
  if (personal.data_dir !== expected.data_dir ||
      personal.credential_ref !== expected.credential_ref ||
      personal.cache_dir !== expected.cache_dir) {
    fail('personal_store_invalid');
  }
  return { personal: Object.freeze({ ...expected }), port };
}

function reusablePersonalTopology(bindings, principalID, home) {
  const candidates = bindings.filter((binding) =>
    binding?.mode === 'personal' && binding?.principal_ref === principalID);
  if (candidates.length === 0) return undefined;
  const topologies = candidates.map((binding) => exactPersonalTopology(binding.personal, home));
  const canonical = canonicalJSONStringify(topologies[0].personal);
  if (topologies.some((topology) => canonicalJSONStringify(topology.personal) !== canonical)) {
    fail('personal_store_fragmented');
  }
  return topologies[0];
}

function secureTrustFile(path, {
  executable = false, root = false, platformServices = defaultPlatformServices,
} = {}) {
  const absolute = resolve(path);
  try {
    platformServices.readIntegrityFile(absolute, {
      owner: root ? 'root' : 'root-or-current', encoding: null, maxBytes: 64 * 1024 * 1024,
    });
    if (executable && !platformServices.inspectExecutable(absolute)) fail('trust_unavailable');
  } catch {
    fail('trust_unavailable');
  }
  return absolute;
}

export function signBindingRegistryWithPresence(payloadBytes, {
  helperPath = HELPER_PATH,
} = {}) {
  const helper = secureTrustFile(helperPath, { executable: true, root: true });
  const directory = join(homedir(), '.pulse', 'supervisor', 'presence');
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  const path = join(directory, `binding-${process.pid}-${Date.now()}-${randomBytes(8).toString('hex')}.json`);
  try {
    writeFileSync(path, payloadBytes, { mode: 0o600, flag: 'wx' });
    const result = spawnSync(helper, ['sign-binding-registry', '--payload', path], {
      encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 180_000,
    });
    if (result.status !== 0 || result.stdout.length > 4096) fail('presence_denied');
    let proof;
    try { proof = JSON.parse(result.stdout); } catch { fail('presence_invalid'); }
    if (!proof || Object.keys(proof).sort().join('\0') !== 'algorithm\0key_id\0signature' ||
        proof.algorithm !== 'es256' || !/^[a-f0-9]{64}$/.test(proof.key_id ?? '') ||
        typeof proof.signature !== 'string' || proof.signature.length < 32 || proof.signature.length > 1024) {
      fail('presence_invalid');
    }
    return { algorithm: 'es256', signature: proof.signature };
  } finally {
    rmSync(path, { force: true });
  }
}

function existingPayload(registryPath, publicKeyPath, rootPublicKey, platformServices = defaultPlatformServices) {
  if (!existsSync(registryPath)) {
    return { schema: 'pulse.workspace-binding-registry.v1', epoch: 0, bindings: [] };
  }
  return verifyBindingRegistry({ registryPath, publicKeyPath, rootPublicKey, platformServices });
}

function exactAnchorBytes(registryBytes, epoch) {
  return Buffer.from(`${canonicalJSONStringify(bindingRegistryAnchor(registryBytes, epoch))}\n`);
}

function transactionPath(registryPath) {
  return `${registryPath}.transaction.json`;
}

function durableReplace(path, bytes, platformServices = defaultPlatformServices) {
  platformServices.atomicWritePrivateFile(resolve(path), bytes, {
    ensureParent: true, maxBytes: 64 * 1024 * 1024,
  });
}

function durableRemove(path, platformServices = defaultPlatformServices) {
  platformServices.removePrivateFile(resolve(path), { missing: true });
}

function transactionBlob(bytes) {
  return {
    bytes_base64: bytes.toString('base64'),
    sha256: createHash('sha256').update(bytes).digest('hex'),
  };
}

function decodeTransactionBlob(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value) ||
      Object.keys(value).sort().join('\0') !== 'bytes_base64\0sha256' ||
      typeof value.bytes_base64 !== 'string' || !/^[a-f0-9]{64}$/.test(value.sha256 ?? '')) {
    fail('transaction_invalid');
  }
  const bytes = Buffer.from(value.bytes_base64, 'base64');
  if (bytes.length < 1 || bytes.toString('base64') !== value.bytes_base64 ||
      createHash('sha256').update(bytes).digest('hex') !== value.sha256) {
    fail('transaction_invalid');
  }
  return bytes;
}

function verifyTransactionRegistryState(state, {
  publicKeyPath, rootPublicKey, registryPath, platformServices = defaultPlatformServices,
}) {
  if (!state || typeof state !== 'object' || Array.isArray(state) ||
      Object.keys(state).sort().join('\0') !== 'anchor\0epoch\0registry' ||
      !Number.isSafeInteger(state.epoch) || state.epoch < 1) {
    fail('transaction_invalid');
  }
  const registryBytes = decodeTransactionBlob(state.registry);
  const anchorBytes = decodeTransactionBlob(state.anchor);
  if (!anchorBytes.equals(exactAnchorBytes(registryBytes, state.epoch))) fail('transaction_invalid');
  const temporary = `${registryPath}.verify.${process.pid}.${randomBytes(8).toString('hex')}`;
  try {
    platformServices.atomicWritePrivateFile(resolve(temporary), registryBytes, {
      ensureParent: true, maxBytes: 64 * 1024 * 1024,
    });
    const payload = verifyBindingRegistry({
      registryPath: temporary, publicKeyPath, rootPublicKey, platformServices,
    });
    if (payload.epoch !== state.epoch) fail('transaction_invalid');
  } catch (error) {
    if (error?.message?.startsWith('binding_admin_')) throw error;
    fail('transaction_invalid');
  } finally {
    platformServices.removePrivateFile(resolve(temporary), { missing: true });
  }
  return { registryBytes, anchorBytes, epoch: state.epoch };
}

function readBindingTransaction(path, options) {
  try {
    const platformServices = options.platformServices ?? defaultPlatformServices;
    const bytes = platformServices.readPrivateFile(resolve(path), {
      encoding: null, minBytes: 1, maxBytes: 64 * 1024 * 1024,
    });
    let journal;
    try { journal = JSON.parse(bytes); } catch { fail('transaction_invalid'); }
    if (!journal || typeof journal !== 'object' || Array.isArray(journal) ||
        Object.keys(journal).sort().join('\0') !== 'candidate\0previous\0schema' ||
        journal.schema !== TRANSACTION_SCHEMA ||
        !bytes.equals(Buffer.from(`${canonicalJSONStringify(journal)}\n`))) {
      fail('transaction_invalid');
    }
    const candidate = verifyTransactionRegistryState(journal.candidate, options);
    const previous = journal.previous === null
      ? null
      : verifyTransactionRegistryState(journal.previous, options);
    if (candidate.epoch !== (previous?.epoch ?? 0) + 1) fail('transaction_invalid');
    return { candidate, previous };
  } catch (error) {
    if (error?.message?.startsWith('binding_admin_transaction_')) throw error;
    fail('transaction_invalid');
  }
}

function currentBytes(path, {
  root = false, platformServices = defaultPlatformServices,
} = {}) {
  if (!existsSync(path)) return null;
  return platformServices.readIntegrityFile(resolve(path), {
    owner: root ? 'root' : 'root-or-current', encoding: null, maxBytes: 64 * 1024 * 1024,
  });
}

function bytesEqual(left, right) {
  return left === null ? right === null : right !== null && left.equals(right);
}

function recoverBindingTransactionLocked({
  registryPath, publicKeyPath, anchorPath, rootPublicKey, rootAnchor,
  platformServices = defaultPlatformServices,
}) {
  const journalPath = transactionPath(registryPath);
  if (!existsSync(journalPath)) return { status: 'none' };
  const transaction = readBindingTransaction(journalPath, {
    publicKeyPath, rootPublicKey, registryPath, platformServices,
  });
  if (existsSync(registryPath)) secureTrustFile(registryPath, { platformServices });
  if (existsSync(anchorPath)) secureTrustFile(anchorPath, { root: rootAnchor, platformServices });
  const registryBytes = currentBytes(registryPath, { platformServices });
  const anchorBytes = currentBytes(anchorPath, { root: rootAnchor, platformServices });
  const allowedRegistry = [transaction.candidate.registryBytes, transaction.previous?.registryBytes ?? null];
  if (!allowedRegistry.some((value) => bytesEqual(registryBytes, value))) fail('transaction_state_invalid');

  let status;
  if (bytesEqual(anchorBytes, transaction.candidate.anchorBytes)) {
    durableReplace(registryPath, transaction.candidate.registryBytes, platformServices);
    status = 'completed';
  } else if (transaction.previous && bytesEqual(anchorBytes, transaction.previous.anchorBytes)) {
    durableReplace(registryPath, transaction.previous.registryBytes, platformServices);
    status = 'rolled_back';
  } else if (!transaction.previous && anchorBytes === null) {
    durableRemove(registryPath, platformServices);
    status = 'rolled_back';
  } else {
    fail('transaction_state_invalid');
  }

  if (status === 'completed' || transaction.previous) {
    const payload = verifyBindingRegistry({ registryPath, publicKeyPath, rootPublicKey, platformServices });
    verifyBindingRegistryAnchor({
      registryPath, anchorPath, registryEpoch: payload.epoch, rootAnchor, platformServices,
    });
  } else if (existsSync(registryPath) || existsSync(anchorPath)) {
    fail('transaction_state_invalid');
  }
  durableRemove(journalPath, platformServices);
  return { status };
}

function runRootInstall(args, code) {
  const result = spawnSync('/usr/bin/sudo', args, {
    encoding: 'utf8', stdio: ['inherit', 'ignore', 'pipe'], timeout: 180_000,
  });
  if (result.status !== 0) fail(code);
}

export function installRootBindingAnchor(anchorBytes, {
  anchorPath = ROOT_ANCHOR_PATH,
} = {}) {
  if (!Buffer.isBuffer(anchorBytes) || anchorBytes.length < 1 || anchorBytes.length > 4096 ||
      anchorPath !== ROOT_ANCHOR_PATH) fail('anchor_install_invalid');
  const directory = join(homedir(), '.pulse', 'supervisor', 'presence');
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  const nonce = `${process.pid}-${Date.now()}-${randomBytes(8).toString('hex')}`;
  const source = join(directory, `anchor-${nonce}.json`);
  const privilegedTemporary = `${ROOT_ANCHOR_PATH}.${nonce}.new`;
  try {
    writeFileSync(source, anchorBytes, { mode: 0o600, flag: 'wx' });
    runRootInstall(['/bin/mkdir', '-p', dirname(ROOT_ANCHOR_PATH)], 'anchor_install_failed');
    runRootInstall(['/usr/bin/install', '-o', 'root', '-g', 'wheel', '-m', '0644', source, privilegedTemporary], 'anchor_install_failed');
    runRootInstall(['/bin/mv', '-f', privilegedTemporary, ROOT_ANCHOR_PATH], 'anchor_install_failed');
    runRootInstall(['/bin/sync'], 'anchor_install_failed');
  } catch (error) {
    spawnSync('/usr/bin/sudo', ['/bin/rm', '-f', privilegedTemporary], { stdio: 'ignore', timeout: 30_000 });
    throw error;
  } finally {
    rmSync(source, { force: true });
  }
}

export function installRootBindingPublicKey(publicKeyBytes, {
  publicKeyPath = ROOT_PUBLIC_KEY_PATH,
} = {}) {
  if (!Buffer.isBuffer(publicKeyBytes) || publicKeyBytes.length < 100 || publicKeyBytes.length > 16_384 ||
      publicKeyPath !== ROOT_PUBLIC_KEY_PATH) fail('public_key_install_invalid');
  let publicKey;
  try {
    publicKey = createPublicKey(publicKeyBytes);
  } catch {
    fail('public_key_install_invalid');
  }
  if (publicKey.asymmetricKeyType !== 'ed25519') fail('public_key_install_invalid');

  const directory = join(homedir(), '.pulse', 'supervisor', 'presence');
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  const nonce = `${process.pid}-${Date.now()}-${randomBytes(8).toString('hex')}`;
  const source = join(directory, `binding-public-key-${nonce}.pem`);
  const privilegedTemporary = `${ROOT_PUBLIC_KEY_PATH}.${nonce}.new`;
  try {
    writeFileSync(source, publicKeyBytes, { mode: 0o600, flag: 'wx' });
    runRootInstall(['/bin/mkdir', '-p', dirname(ROOT_PUBLIC_KEY_PATH)], 'public_key_install_failed');
    runRootInstall([
      '/usr/bin/install', '-o', 'root', '-g', 'wheel', '-m', '0644',
      source, privilegedTemporary,
    ], 'public_key_install_failed');
    runRootInstall(['/bin/mv', '-f', privilegedTemporary, ROOT_PUBLIC_KEY_PATH], 'public_key_install_failed');
    runRootInstall(['/bin/sync'], 'public_key_install_failed');
  } catch (error) {
    spawnSync('/usr/bin/sudo', ['/bin/rm', '-f', privilegedTemporary], { stdio: 'ignore', timeout: 30_000 });
    throw error;
  } finally {
    rmSync(source, { force: true });
  }
}

export function removeRootBindingPublicKey({ publicKeyPath = ROOT_PUBLIC_KEY_PATH } = {}) {
  if (publicKeyPath !== ROOT_PUBLIC_KEY_PATH) fail('public_key_install_invalid');
  runRootInstall(['/bin/rm', '-f', ROOT_PUBLIC_KEY_PATH], 'public_key_remove_failed');
}

export function removeRootBindingAnchor({ anchorPath = ROOT_ANCHOR_PATH } = {}) {
  if (anchorPath !== ROOT_ANCHOR_PATH) fail('anchor_install_invalid');
  runRootInstall(['/bin/rm', '-f', ROOT_ANCHOR_PATH], 'anchor_remove_failed');
}

async function withRegistryLock(registryPath, action, {
  platformServices = defaultPlatformServices,
  timeoutSeconds = 30,
} = {}) {
  if (typeof action !== 'function' || !Number.isInteger(timeoutSeconds) || timeoutSeconds < 1 ||
      timeoutSeconds > 300 || typeof platformServices?.acquirePrivateLock !== 'function') fail('request_invalid');
  const lockPath = `${registryPath}.lock`;
  const deadline = Date.now() + timeoutSeconds * 1000;
  let release;
  while (!release) {
    try {
      release = platformServices.acquirePrivateLock(lockPath, { staleAfterMs: 0, timeoutMs: 0 });
    } catch (error) {
      if (!(error instanceof PlatformServicesError) || error.code !== 'platform_lock_occupied' ||
          Date.now() >= deadline) fail('registry_lock_unavailable');
      // Yield so a concurrent transaction in this process can finish its
      // durable anchor write and release the same portable lock.
      await new Promise((resolveWait) => setTimeout(resolveWait, 10));
    }
  }

  try {
    return await action();
  } finally {
    release();
  }
}

function nextBinding({
  workspace, epoch, home, port, principalID, personalTopology,
}) {
  const suffix = randomBytes(10).toString('hex');
  const base = {
    binding_id: `binding_${suffix}`,
    receipt_id: `receipt_${randomBytes(10).toString('hex')}`,
    resolver_epoch: epoch,
    workspace: { workspace_id: workspace.workspace_id, repository_id: workspace.repository_id },
    mode: 'personal',
    principal_ref: principalID,
  };
  if (personalTopology) {
    return { ...base, personal: { ...personalTopology } };
  }
  const storeID = `store_personal_${suffix}`;
  return {
    ...base,
    personal: {
      store_id: storeID,
      data_dir: join(home, '.pulse', 'vaults', 'personal', storeID),
      base_url: `http://127.0.0.1:${port}`,
      credential_ref: `keychain:pulse/local/${storeID}`,
      cache_dir: join(home, '.pulse', 'caches', 'personal', storeID),
    },
  };
}

export async function recoverWorkspaceBindingTransaction({
  home = homedir(),
  registryPath = defaultBindingPaths(home).registryPath,
  publicKeyPath = defaultBindingPaths(home).publicKeyPath,
  anchorPath = defaultBindingPaths(home).anchorPath,
  rootPublicKey = true,
  rootAnchor = true,
  platformServices = defaultPlatformServices,
  lockTimeoutSeconds = 30,
} = {}) {
  if (!isAbsolute(home) || typeof rootPublicKey !== 'boolean' || typeof rootAnchor !== 'boolean') {
    fail('request_invalid');
  }
  secureTrustFile(publicKeyPath, { root: rootPublicKey, platformServices });
  // Normal product startup has no interrupted binding transaction to repair.
  // Avoid making every MCP server and SessionStart hook queue on the same
  // registry lock just to discover that there is no journal.
  if (!existsSync(transactionPath(registryPath))) return { status: 'none' };
  return withRegistryLock(registryPath, () => recoverBindingTransactionLocked({
    registryPath, publicKeyPath, anchorPath, rootPublicKey, rootAnchor, platformServices,
  }), { platformServices, timeoutSeconds: lockTimeoutSeconds });
}

export function personalWorkspaceBindingCreationMode({
  home = homedir(),
  registryPath = defaultBindingPaths(home).registryPath,
  anchorPath = defaultBindingPaths(home).anchorPath,
} = {}) {
  if (!isAbsolute(home) || !isAbsolute(registryPath) || !isAbsolute(anchorPath)) {
    fail('request_invalid');
  }
  const registryPresent = existsSync(registryPath);
  const anchorPresent = existsSync(anchorPath);
  if (registryPresent !== anchorPresent) fail('anchor_state_inconsistent');
  return registryPresent ? 'attach' : 'initial';
}

export async function createWorkspaceBinding({
  cwd = process.cwd(),
  mode,
  port,
  principalID,
  home = homedir(),
  registryPath = defaultBindingPaths(home).registryPath,
  publicKeyPath = defaultBindingPaths(home).publicKeyPath,
  anchorPath = defaultBindingPaths(home).anchorPath,
  signer = signBindingRegistryWithPresence,
  anchorInstaller = installRootBindingAnchor,
  anchorRemover = removeRootBindingAnchor,
  rootPublicKey = true,
  rootAnchor = true,
  platformServices = defaultPlatformServices,
  lockTimeoutSeconds = 30,
  onTransitionPhase = () => {},
} = {}) {
  if (mode !== 'personal' || typeof signer !== 'function' ||
      typeof anchorInstaller !== 'function' || typeof anchorRemover !== 'function' ||
      typeof onTransitionPhase !== 'function' || !isAbsolute(home)) {
    fail('request_invalid');
  }
  port = exactPort(port);
  exactID(principalID, 'principal_invalid');
  if (typeof rootPublicKey !== 'boolean' || typeof rootAnchor !== 'boolean') fail('request_invalid');
  secureTrustFile(publicKeyPath, { root: rootPublicKey, platformServices });
  const workspace = canonicalizeWorkspace(cwd, { platformServices });
  return withRegistryLock(registryPath, async () => {
    recoverBindingTransactionLocked({
      registryPath, publicKeyPath, anchorPath, rootPublicKey, rootAnchor, platformServices,
    });
    const hadRegistry = existsSync(registryPath);
    const hadAnchor = existsSync(anchorPath);
    if (hadRegistry !== hadAnchor) fail('anchor_state_inconsistent');
    const previous = existingPayload(registryPath, publicKeyPath, rootPublicKey, platformServices);
    if (hadRegistry) {
      verifyBindingRegistryAnchor({
        registryPath, anchorPath, registryEpoch: previous.epoch, rootAnchor, platformServices,
      });
    }
    const epoch = previous.epoch + 1;
    const reusablePersonal = reusablePersonalTopology(previous.bindings, principalID, resolve(home));
    if (reusablePersonal && port !== undefined && port !== reusablePersonal.port) {
      fail('personal_store_port_mismatch');
    }
    const selectedPort = reusablePersonal?.port ??
      selectLocalPort(port, previous.bindings, workspace.workspace_id);
    const replacement = nextBinding({
      workspace, epoch, home: resolve(home), port: selectedPort, principalID,
      personalTopology: reusablePersonal?.personal,
    });
    const bindings = previous.bindings
      .filter((binding) => binding?.workspace?.workspace_id !== workspace.workspace_id);
    bindings.push(replacement);
    bindings.sort((left, right) => left.workspace.workspace_id.localeCompare(right.workspace.workspace_id));
    const payload = {
      schema: 'pulse.workspace-binding-registry.v1',
      epoch,
      bindings,
    };
    const bytes = Buffer.from(canonicalJSONStringify(payload));
    const proof = signer(bytes, { payload });
    if (!proof || !['ed25519', 'es256'].includes(proof.algorithm) ||
        typeof proof.signature !== 'string') fail('presence_invalid');
    const envelope = { algorithm: proof.algorithm, payload, signature: proof.signature };
    const temporary = `${registryPath}.${process.pid}.${Date.now()}.${randomBytes(8).toString('hex')}.new`;
    const previousRegistryBytes = hadRegistry ? readFileSync(registryPath) : undefined;
    const previousAnchorBytes = hadAnchor ? readFileSync(anchorPath) : undefined;
    try {
      const registryBytes = Buffer.from(`${JSON.stringify(envelope)}\n`);
      platformServices.atomicWritePrivateFile(resolve(temporary), registryBytes, {
        ensureParent: true, maxBytes: 64 * 1024 * 1024,
      });

      // Verify the exact candidate before the atomic replacement. A denied or
      // malformed presence proof must never destroy the last trusted registry.
      const candidate = verifyBindingRegistry({
        registryPath: temporary, publicKeyPath, rootPublicKey, platformServices,
      });
      const candidateResult = candidate.bindings.find(
        (binding) => binding.binding_id === replacement.binding_id,
      );
      if (!candidateResult || candidate.epoch !== epoch) fail('write_verification_failed');
      const anchorBytes = exactAnchorBytes(registryBytes, epoch);
      const journal = {
        schema: TRANSACTION_SCHEMA,
        previous: hadRegistry ? {
          epoch: previous.epoch,
          registry: transactionBlob(previousRegistryBytes),
          anchor: transactionBlob(previousAnchorBytes),
        } : null,
        candidate: {
          epoch,
          registry: transactionBlob(registryBytes),
          anchor: transactionBlob(anchorBytes),
        },
      };
      durableReplace(
        transactionPath(registryPath),
        Buffer.from(`${canonicalJSONStringify(journal)}\n`),
        platformServices,
      );
      await onTransitionPhase('journal_prepared');
      await anchorInstaller(anchorBytes, { anchorPath, epoch });
      verifyBindingRegistryAnchor({
        registryPath: temporary, anchorPath, registryEpoch: epoch, rootAnchor, platformServices,
      });
      await onTransitionPhase('anchor_installed');
      durableReplace(registryPath, registryBytes, platformServices);
      verifyBindingRegistryAnchor({
        registryPath, anchorPath, registryEpoch: epoch, rootAnchor, platformServices,
      });
      await onTransitionPhase('registry_replaced');
      durableRemove(transactionPath(registryPath), platformServices);
    } catch (error) {
      if (existsSync(transactionPath(registryPath))) recoverBindingTransactionLocked({
        registryPath, publicKeyPath, anchorPath, rootPublicKey, rootAnchor, platformServices,
      });
      throw error;
    } finally {
      platformServices.removePrivateFile(resolve(temporary), { missing: true });
    }
    const verified = verifyBindingRegistry({ registryPath, publicKeyPath, rootPublicKey, platformServices });
    verifyBindingRegistryAnchor({
      registryPath, anchorPath, registryEpoch: verified.epoch, rootAnchor, platformServices,
    });
    const result = verified.bindings.find((binding) => binding.binding_id === replacement.binding_id);
    if (!result) fail('write_verification_failed');
    return result;
  }, { platformServices, timeoutSeconds: lockTimeoutSeconds });
}

export async function createInitialPersonalWorkspaceBinding({
  cwd = process.cwd(),
  port,
  principalID,
  home = homedir(),
  registryPath = defaultBindingPaths(home).registryPath,
  publicKeyPath = defaultBindingPaths(home).publicKeyPath,
  anchorPath = defaultBindingPaths(home).anchorPath,
  publicKeyInstaller = installRootBindingPublicKey,
  publicKeyRemover = removeRootBindingPublicKey,
  anchorInstaller = installRootBindingAnchor,
  anchorRemover = removeRootBindingAnchor,
  rootPublicKey = true,
  rootAnchor = true,
  platformServices = defaultPlatformServices,
  lockTimeoutSeconds = 30,
  onTransitionPhase = () => {},
} = {}) {
  if (typeof publicKeyInstaller !== 'function' || typeof publicKeyRemover !== 'function' ||
      typeof anchorInstaller !== 'function' || typeof anchorRemover !== 'function' ||
      typeof onTransitionPhase !== 'function' || !isAbsolute(home) ||
      typeof rootPublicKey !== 'boolean' || typeof rootAnchor !== 'boolean') {
    fail('request_invalid');
  }
  exactID(principalID, 'principal_invalid');
  exactPort(port);
  platformServices.ensurePrivateDirectory(dirname(registryPath));

  return withRegistryLock(`${registryPath}.initial-bootstrap`, async () => {
    if (existsSync(registryPath) || existsSync(anchorPath)) fail('initial_binding_exists');

    const { privateKey, publicKey } = generateKeyPairSync('ed25519');
    const publicKeyBytes = Buffer.from(publicKey.export({ type: 'spki', format: 'pem' }));
    await publicKeyInstaller(publicKeyBytes, { publicKeyPath });
    try {
      return await createWorkspaceBinding({
        cwd,
        mode: 'personal',
        port,
        principalID,
        home,
        registryPath,
        publicKeyPath,
        anchorPath,
        signer: (bytes) => ({
          algorithm: 'ed25519',
          signature: cryptoSign(null, bytes, privateKey).toString('base64'),
        }),
        anchorInstaller,
        anchorRemover,
        rootPublicKey,
        rootAnchor,
        platformServices,
        lockTimeoutSeconds,
        onTransitionPhase,
      });
    } catch (error) {
      if (!existsSync(registryPath) && !existsSync(anchorPath)) {
        await publicKeyRemover({ publicKeyPath });
      }
      throw error;
    }
  }, { platformServices, timeoutSeconds: lockTimeoutSeconds });
}
