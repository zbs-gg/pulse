import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { lstatSync, readFileSync, realpathSync } from 'node:fs';
import { dirname, isAbsolute, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const CATALOG_SCHEMA = 'pulse.windows_bootstrap_adapter_catalog.v1';
const CONTRACT_SCHEMA = 'pulse.windows_bootstrap_adapter.contract.v1';
const REQUEST_SCHEMA = 'pulse.windows_bootstrap_adapter.request.v1';
const RESPONSE_SCHEMA = 'pulse.windows_bootstrap_adapter.response.v1';
const SHA256 = /^[a-f0-9]{64}$/;
const EXPECTED_OPERATIONS = Object.freeze([
  'acquire_private_lock', 'atomic_write_private_file', 'ensure_private_directory',
  'inspect_executable', 'inspect_path_identity', 'inspect_private_state', 'inspect_private_tree', 'inspect_process',
  'read_integrity_file', 'read_private_file', 'release_private_lock', 'remove_private_file',
  'terminate_process',
]);
const defaultCatalogRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', 'runtime', 'windows-bootstrap');

export class WindowsBootstrapAdapterError extends Error {
  constructor(code, message = code) {
    super(message);
    this.name = 'WindowsBootstrapAdapterError';
    this.code = code;
  }
}

function fail(code, message) {
  throw new WindowsBootstrapAdapterError(code, message);
}

function exactObject(value, keys) {
  return value && !Array.isArray(value) && typeof value === 'object' &&
    Object.keys(value).sort().join('\0') === [...keys].sort().join('\0');
}

function canonical(value) {
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`;
  if (value && typeof value === 'object') {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}`;
  }
  return JSON.stringify(value);
}

function invokeBinary({ binaryPath, operation, payload }) {
  const input = operation === 'contract' ? '' : `${canonical({ ...payload, schema: REQUEST_SCHEMA })}\n`;
  const result = spawnSync(binaryPath, [operation], {
    encoding: 'utf8', input, maxBuffer: 70 * 1024 * 1024,
    stdio: ['pipe', 'pipe', 'pipe'], timeout: 65_000, windowsHide: true,
  });
  let envelope;
  try { envelope = JSON.parse(result.stdout); } catch { fail('windows_bootstrap_adapter_protocol_invalid'); }
  if (!exactObject(envelope, envelope.ok ? ['ok', 'result', 'schema'] : ['error', 'ok', 'schema']) ||
      envelope.schema !== RESPONSE_SCHEMA || typeof envelope.ok !== 'boolean') {
    fail('windows_bootstrap_adapter_protocol_invalid');
  }
  if (result.status !== 0 || !envelope.ok) {
    const error = new WindowsBootstrapAdapterError(
      envelope.error === 'not_found' ? 'ENOENT' :
        envelope.error === 'lock_occupied' ? 'platform_lock_occupied' : 'platform_native_adapter_failed',
      `Windows bootstrap ${operation} failed: ${envelope.error}`,
    );
    throw error;
  }
  return envelope.result;
}

function validatedCatalog(catalogRoot, architecture) {
  if (!['arm64', 'x64'].includes(architecture) || typeof catalogRoot !== 'string' || !isAbsolute(catalogRoot)) {
    fail('windows_bootstrap_adapter_configuration_invalid');
  }
  let bytes;
  let catalog;
  try {
    bytes = readFileSync(join(catalogRoot, 'catalog.json'), 'utf8');
    catalog = JSON.parse(bytes);
  } catch { fail('windows_bootstrap_adapter_catalog_missing'); }
  if (!exactObject(catalog, ['adapters', 'protocol', 'schema']) || catalog.schema !== CATALOG_SCHEMA ||
      catalog.protocol !== 1 || bytes !== `${canonical(catalog)}\n` ||
      !catalog.adapters || Array.isArray(catalog.adapters) || typeof catalog.adapters !== 'object') {
    fail('windows_bootstrap_adapter_catalog_invalid');
  }
  const target = `win32-${architecture}`;
  const entry = catalog.adapters[target];
  if (!exactObject(entry, ['bytes', 'path', 'sha256', 'target']) || entry.target !== target ||
      !Number.isSafeInteger(entry.bytes) || entry.bytes < 1 || entry.bytes > 32 * 1024 * 1024 ||
      !SHA256.test(entry.sha256 ?? '') || entry.path !== `${target}/pulse-platform-adapter.exe`) {
    fail('windows_bootstrap_adapter_target_missing');
  }
  const binaryPath = resolve(catalogRoot, entry.path);
  let stat;
  try { stat = lstatSync(binaryPath); } catch { fail('windows_bootstrap_adapter_missing'); }
  if (!stat.isFile() || stat.isSymbolicLink() || stat.nlink !== 1) {
    fail('windows_bootstrap_adapter_unsafe');
  }
  if (stat.size !== entry.bytes) fail('windows_bootstrap_adapter_digest_mismatch');
  const canonicalRoot = realpathSync(catalogRoot);
  const canonicalBinary = realpathSync(binaryPath);
  const child = relative(canonicalRoot, canonicalBinary);
  if (child === '' || child === '..' || child.startsWith(`..${process.platform === 'win32' ? '\\' : '/'}`) || isAbsolute(child)) {
    fail('windows_bootstrap_adapter_unsafe');
  }
  if (createHash('sha256').update(readFileSync(canonicalBinary)).digest('hex') !== entry.sha256) {
    fail('windows_bootstrap_adapter_digest_mismatch');
  }
  return { binaryPath: canonicalBinary, target };
}

export function loadBundledWindowsAdapter({
  architecture = process.arch,
  catalogRoot = defaultCatalogRoot,
  invoke = invokeBinary,
} = {}) {
  const { binaryPath, target } = validatedCatalog(catalogRoot, architecture);
  const call = (operation, payload = {}) => invoke({ binaryPath, operation, payload, target });
  const contract = call('contract');
  if (!exactObject(contract, ['operations', 'schema', 'target', 'version']) ||
      contract.schema !== CONTRACT_SCHEMA || contract.target !== target || contract.version !== 1 ||
      JSON.stringify(contract.operations) !== JSON.stringify(EXPECTED_OPERATIONS)) {
    fail('windows_bootstrap_adapter_contract_invalid');
  }
  const bytes = (result) => {
    if (!exactObject(result, ['bytes_base64'])) fail('platform_native_adapter_failed');
    return Buffer.from(result.bytes_base64, 'base64');
  };
  return Object.freeze({
    schema: 'pulse.windows_bootstrap_adapter.v1',
    target,
    inspectExecutable: (path) => call('inspect_executable', { path }),
    inspectPrivateState: (path, { kind }) => call('inspect_private_state', { kind, path }),
    inspectPrivateTree: (path, { entries, maximumDepth, maximumEntries, maximumTotalBytes }) =>
      call('inspect_private_tree', {
        entries, maximum_depth: maximumDepth, maximum_entries: maximumEntries,
        maximum_total_bytes: maximumTotalBytes, path,
      }),
    inspectPathIdentity: (path, { kind }) => call('inspect_path_identity', { kind, path }),
    readIntegrityFile(path, { owner, maxBytes }) {
      const result = call('read_integrity_file', { maximum_bytes: maxBytes, owner, path });
      const { bytes_base64: encoded, ...proof } = result;
      return { ...proof, bytes: Buffer.from(encoded, 'base64') };
    },
    ensurePrivateDirectory(path) { call('ensure_private_directory', { path }); },
    readPrivateFile(path, { encoding, minBytes, maxBytes }) {
      const raw = bytes(call('read_private_file', {
        encoding: encoding ?? '', maximum_bytes: maxBytes, minimum_bytes: minBytes, path,
      }));
      return encoding === null ? raw : raw.toString(encoding ?? 'utf8');
    },
    atomicWritePrivateFile(path, data, { ensureParent, maxBytes }) {
      call('atomic_write_private_file', {
        bytes_base64: Buffer.from(data).toString('base64'), ensure_parent: ensureParent, maximum_bytes: maxBytes, path,
      });
    },
    removePrivateFile(path, { missing }) {
      return call('remove_private_file', { missing, path }).removed;
    },
    inspectProcess(pid) { return call('inspect_process', { pid }); },
    terminateProcess(pid) {
      const proof = call('inspect_process', { pid });
      if (!proof.running) return false;
      return call('terminate_process', { identity_token: proof.identity_token, pid }).terminated;
    },
    acquirePrivateLock(path, { staleAfterMs, timeoutMs }) {
      const owner = call('inspect_process', { pid: process.pid });
      if (!owner.running || typeof owner.identity_token !== 'string') fail('platform_lock_identity_unavailable');
      const acquired = call('acquire_private_lock', {
        identity_token: owner.identity_token, path, pid: process.pid,
        stale_after_ms: staleAfterMs, timeout_ms: timeoutMs,
      });
      let released = false;
      return () => {
        if (released) return;
        call('release_private_lock', {
          identity_token: owner.identity_token, lease: acquired.lease, path, pid: process.pid,
        });
        released = true;
      };
    },
  });
}
