import { spawnSync } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import {
  chmodSync, closeSync, existsSync, fsyncSync, lstatSync, mkdirSync, openSync, renameSync, rmSync,
  readFileSync, statSync, writeFileSync,
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
const ROOT_ANCHOR_PATH = '/Library/Application Support/Pulse/trust/workspace-bindings.anchor.json';
const SAFE_VAULT_ID = /^[a-z][a-z0-9_]{2,127}$/;
const SAFE_PROJECT_ID = /^project_[a-z0-9][a-z0-9_]{0,119}$/;
const TRANSACTION_SCHEMA = 'pulse.workspace-binding-transaction.v1';
const defaultPlatformServices = createPlatformServices();

function fail(code) {
  throw new Error(`binding_admin_${code}`);
}

function exactID(value, code) {
  if (typeof value !== 'string' || !SAFE_VAULT_ID.test(value)) fail(code);
  return value;
}

function exactProjectID(value) {
  if (typeof value !== 'string' || !SAFE_PROJECT_ID.test(value)) fail('commons_project_invalid');
  return value;
}

function exactPort(value) {
  if (value === undefined) return undefined;
  if (!Number.isInteger(value) || value < 1024 || value > 65535) fail('port_invalid');
  return value;
}

function bindingLocalPort(binding) {
  const raw = binding?.mode === 'team' ? binding?.desk?.base_url : binding?.personal?.base_url;
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

function exactCommonsResource(value) {
  let url;
  try { url = new URL(value); } catch { fail('commons_resource_invalid'); }
  if (url.protocol !== 'https:' || url.username || url.password || url.search || url.hash ||
      url.pathname !== '/mcp' || ['localhost', '127.0.0.1', '[::1]', '::1'].includes(url.hostname)) {
    fail('commons_resource_invalid');
  }
  return url.toString();
}

function secureTrustFile(path, { executable = false, root = false } = {}) {
  const link = lstatSync(path);
  const info = statSync(path);
  const uid = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
  if (link.isSymbolicLink() || !info.isFile() || (info.mode & 0o022) !== 0 ||
      (root ? info.uid !== 0 : ![0, uid].includes(info.uid)) || (executable && (info.mode & 0o111) === 0)) {
    fail('trust_unavailable');
  }
  return resolve(path);
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

function existingPayload(registryPath, publicKeyPath, rootPublicKey) {
  if (!existsSync(registryPath)) {
    return { schema: 'pulse.workspace-binding-registry.v1', epoch: 0, bindings: [] };
  }
  return verifyBindingRegistry({ registryPath, publicKeyPath, rootPublicKey });
}

function exactAnchorBytes(registryBytes, epoch) {
  return Buffer.from(`${canonicalJSONStringify(bindingRegistryAnchor(registryBytes, epoch))}\n`);
}

function transactionPath(registryPath) {
  return `${registryPath}.transaction.json`;
}

function fsyncDirectory(path) {
  const descriptor = openSync(path, 'r');
  try { fsyncSync(descriptor); } finally { closeSync(descriptor); }
}

function durableReplace(path, bytes, mode = 0o600) {
  const directory = dirname(path);
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  const temporary = `${path}.${process.pid}.${Date.now()}.${randomBytes(8).toString('hex')}.new`;
  try {
    writeFileSync(temporary, bytes, { mode, flag: 'wx' });
    chmodSync(temporary, mode);
    const descriptor = openSync(temporary, 'r');
    try { fsyncSync(descriptor); } finally { closeSync(descriptor); }
    renameSync(temporary, path);
    fsyncDirectory(directory);
  } finally {
    rmSync(temporary, { force: true });
  }
}

function durableRemove(path) {
  if (!existsSync(path)) return;
  rmSync(path);
  fsyncDirectory(dirname(path));
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

function verifyTransactionRegistryState(state, { publicKeyPath, rootPublicKey, registryPath }) {
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
    writeFileSync(temporary, registryBytes, { mode: 0o600, flag: 'wx' });
    chmodSync(temporary, 0o600);
    const payload = verifyBindingRegistry({
      registryPath: temporary, publicKeyPath, rootPublicKey,
    });
    if (payload.epoch !== state.epoch) fail('transaction_invalid');
  } catch (error) {
    if (error?.message?.startsWith('binding_admin_')) throw error;
    fail('transaction_invalid');
  } finally {
    rmSync(temporary, { force: true });
  }
  return { registryBytes, anchorBytes, epoch: state.epoch };
}

function readBindingTransaction(path, options) {
  try {
    const safePath = secureTrustFile(path);
    const info = statSync(safePath);
    if ((info.mode & 0o077) !== 0) fail('transaction_invalid');
    const bytes = readFileSync(safePath);
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

function currentBytes(path) {
  return existsSync(path) ? readFileSync(path) : null;
}

function bytesEqual(left, right) {
  return left === null ? right === null : right !== null && left.equals(right);
}

function recoverBindingTransactionLocked({
  registryPath, publicKeyPath, anchorPath, rootPublicKey, rootAnchor,
}) {
  const journalPath = transactionPath(registryPath);
  if (!existsSync(journalPath)) return { status: 'none' };
  const transaction = readBindingTransaction(journalPath, {
    publicKeyPath, rootPublicKey, registryPath,
  });
  if (existsSync(registryPath)) secureTrustFile(registryPath);
  if (existsSync(anchorPath)) secureTrustFile(anchorPath, { root: rootAnchor });
  const registryBytes = currentBytes(registryPath);
  const anchorBytes = currentBytes(anchorPath);
  const allowedRegistry = [transaction.candidate.registryBytes, transaction.previous?.registryBytes ?? null];
  if (!allowedRegistry.some((value) => bytesEqual(registryBytes, value))) fail('transaction_state_invalid');

  let status;
  if (bytesEqual(anchorBytes, transaction.candidate.anchorBytes)) {
    durableReplace(registryPath, transaction.candidate.registryBytes);
    status = 'completed';
  } else if (transaction.previous && bytesEqual(anchorBytes, transaction.previous.anchorBytes)) {
    durableReplace(registryPath, transaction.previous.registryBytes);
    status = 'rolled_back';
  } else if (!transaction.previous && anchorBytes === null) {
    durableRemove(registryPath);
    status = 'rolled_back';
  } else {
    fail('transaction_state_invalid');
  }

  if (status === 'completed' || transaction.previous) {
    const payload = verifyBindingRegistry({ registryPath, publicKeyPath, rootPublicKey });
    verifyBindingRegistryAnchor({
      registryPath, anchorPath, registryEpoch: payload.epoch, rootAnchor,
    });
  } else if (existsSync(registryPath) || existsSync(anchorPath)) {
    fail('transaction_state_invalid');
  }
  durableRemove(journalPath);
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
  mode, workspace, epoch, home, port, principalID, teamID, commonsStoreID,
  commonsProjectID, commonsResource,
}) {
  const suffix = randomBytes(10).toString('hex');
  const base = {
    binding_id: `binding_${suffix}`,
    receipt_id: `receipt_${randomBytes(10).toString('hex')}`,
    resolver_epoch: epoch,
    workspace: { workspace_id: workspace.workspace_id, repository_id: workspace.repository_id },
    mode,
    principal_ref: principalID,
  };
  if (mode === 'personal') {
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
  const deskIdentity = createHash('sha256')
    .update('pulse-desk-v1\0')
    .update(teamID)
    .update('\0')
    .update(principalID)
    .digest('hex')
    .slice(0, 24);
  const deskStoreID = `store_desk_${deskIdentity}`;
  return {
    ...base,
    desk: {
      store_id: deskStoreID,
      data_dir: join(home, '.pulse', 'vaults', 'desks', teamID, principalID),
      base_url: `http://127.0.0.1:${port}`,
      credential_ref: `keychain:pulse/desk/${teamID}/${principalID}`,
      cache_dir: join(home, '.pulse', 'caches', 'desks', teamID, principalID),
    },
    commons: {
      store_id: commonsStoreID,
      team_id: teamID,
      project_id: commonsProjectID,
      resource: commonsResource,
      credential_ref: `keychain:pulse/team/${teamID}/${principalID}`,
      cache_partition: `commons:${teamID}:${principalID}`,
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
  secureTrustFile(publicKeyPath, { root: rootPublicKey });
  return withRegistryLock(registryPath, () => recoverBindingTransactionLocked({
    registryPath, publicKeyPath, anchorPath, rootPublicKey, rootAnchor,
  }), { platformServices, timeoutSeconds: lockTimeoutSeconds });
}

export async function createWorkspaceBinding({
  cwd = process.cwd(),
  mode,
  port,
  principalID,
  teamID,
  commonsStoreID,
  commonsProjectID,
  commonsResource,
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
  if (!['personal', 'team'].includes(mode) || typeof signer !== 'function' ||
      typeof anchorInstaller !== 'function' || typeof anchorRemover !== 'function' ||
      typeof onTransitionPhase !== 'function' || !isAbsolute(home)) {
    fail('request_invalid');
  }
  port = exactPort(port);
  exactID(principalID, 'principal_invalid');
  if (mode === 'team') {
    exactID(teamID, 'team_invalid');
    exactID(commonsStoreID, 'commons_store_invalid');
    commonsProjectID = exactProjectID(commonsProjectID);
    commonsResource = exactCommonsResource(commonsResource);
  }
  if (typeof rootPublicKey !== 'boolean' || typeof rootAnchor !== 'boolean') fail('request_invalid');
  secureTrustFile(publicKeyPath, { root: rootPublicKey });
  const workspace = canonicalizeWorkspace(cwd);
  return withRegistryLock(registryPath, async () => {
    recoverBindingTransactionLocked({
      registryPath, publicKeyPath, anchorPath, rootPublicKey, rootAnchor,
    });
    const hadRegistry = existsSync(registryPath);
    const hadAnchor = existsSync(anchorPath);
    if (hadRegistry !== hadAnchor) fail('anchor_state_inconsistent');
    const previous = existingPayload(registryPath, publicKeyPath, rootPublicKey);
    if (hadRegistry) {
      verifyBindingRegistryAnchor({
        registryPath, anchorPath, registryEpoch: previous.epoch, rootAnchor,
      });
    }
    if (mode === 'team' && previous.bindings.some((binding) =>
      binding?.workspace?.workspace_id !== workspace.workspace_id &&
      binding?.mode === 'team' && binding?.principal_ref === principalID &&
      binding?.commons?.team_id === teamID)) {
      fail('desk_binding_conflict');
    }
    const epoch = previous.epoch + 1;
    const selectedPort = selectLocalPort(port, previous.bindings, workspace.workspace_id);
    const replacement = nextBinding({
      mode, workspace, epoch, home: resolve(home), port: selectedPort, principalID,
      teamID, commonsStoreID, commonsProjectID, commonsResource,
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
    if (!proof || proof.algorithm !== 'es256' || typeof proof.signature !== 'string') fail('presence_invalid');
    const envelope = { algorithm: proof.algorithm, payload, signature: proof.signature };
    const temporary = `${registryPath}.${process.pid}.${Date.now()}.${randomBytes(8).toString('hex')}.new`;
    const previousRegistryBytes = hadRegistry ? readFileSync(registryPath) : undefined;
    const previousAnchorBytes = hadAnchor ? readFileSync(anchorPath) : undefined;
    try {
      const registryBytes = Buffer.from(`${JSON.stringify(envelope)}\n`);
      writeFileSync(temporary, registryBytes, { mode: 0o600, flag: 'wx' });
      chmodSync(temporary, 0o600);
      const descriptor = openSync(temporary, 'r');
      try { fsyncSync(descriptor); } finally { closeSync(descriptor); }

      // Verify the exact candidate before the atomic replacement. A denied or
      // malformed presence proof must never destroy the last trusted registry.
      const candidate = verifyBindingRegistry({
        registryPath: temporary, publicKeyPath, rootPublicKey,
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
      );
      await onTransitionPhase('journal_prepared');
      await anchorInstaller(anchorBytes, { anchorPath, epoch });
      verifyBindingRegistryAnchor({
        registryPath: temporary, anchorPath, registryEpoch: epoch, rootAnchor,
      });
      await onTransitionPhase('anchor_installed');
      renameSync(temporary, registryPath);
      fsyncDirectory(dirname(registryPath));
      verifyBindingRegistryAnchor({
        registryPath, anchorPath, registryEpoch: epoch, rootAnchor,
      });
      await onTransitionPhase('registry_replaced');
      durableRemove(transactionPath(registryPath));
    } catch (error) {
      if (existsSync(transactionPath(registryPath))) recoverBindingTransactionLocked({
        registryPath, publicKeyPath, anchorPath, rootPublicKey, rootAnchor,
      });
      throw error;
    } finally {
      rmSync(temporary, { force: true });
    }
    const verified = verifyBindingRegistry({ registryPath, publicKeyPath, rootPublicKey });
    verifyBindingRegistryAnchor({ registryPath, anchorPath, registryEpoch: verified.epoch, rootAnchor });
    const result = verified.bindings.find((binding) => binding.binding_id === replacement.binding_id);
    if (!result) fail('write_verification_failed');
    return result;
  }, { platformServices, timeoutSeconds: lockTimeoutSeconds });
}
