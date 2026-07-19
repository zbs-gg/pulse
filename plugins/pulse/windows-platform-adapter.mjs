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
const defaultCatalogRoot = resolve(dirname(fileURLToPath(import.meta.url)), 'native', 'windows-bootstrap');

function fail(code) {
  const error = new Error(code);
  error.code = code;
  throw error;
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
  try { envelope = JSON.parse(result.stdout); } catch { fail('pulse_windows_plugin_adapter_protocol_invalid'); }
  if (!exactObject(envelope, envelope.ok ? ['ok', 'result', 'schema'] : ['error', 'ok', 'schema']) ||
      envelope.schema !== RESPONSE_SCHEMA || typeof envelope.ok !== 'boolean') {
    fail('pulse_windows_plugin_adapter_protocol_invalid');
  }
  if (result.status !== 0 || !envelope.ok) fail('pulse_windows_plugin_adapter_failed');
  return envelope.result;
}

function validatedBinary(catalogRoot, architecture) {
  if (!['arm64', 'x64'].includes(architecture) || typeof catalogRoot !== 'string' || !isAbsolute(catalogRoot)) {
    fail('pulse_windows_plugin_adapter_configuration_invalid');
  }
  let bytes;
  let catalog;
  try {
    bytes = readFileSync(join(catalogRoot, 'catalog.json'), 'utf8');
    catalog = JSON.parse(bytes);
  } catch { fail('pulse_windows_plugin_adapter_catalog_missing'); }
  if (!exactObject(catalog, ['adapters', 'protocol', 'schema']) || catalog.schema !== CATALOG_SCHEMA ||
      catalog.protocol !== 1 || bytes !== `${canonical(catalog)}\n` ||
      !catalog.adapters || Array.isArray(catalog.adapters) || typeof catalog.adapters !== 'object') {
    fail('pulse_windows_plugin_adapter_catalog_invalid');
  }
  const target = `win32-${architecture}`;
  const entry = catalog.adapters[target];
  if (!exactObject(entry, ['bytes', 'path', 'sha256', 'target']) || entry.target !== target ||
      !Number.isSafeInteger(entry.bytes) || entry.bytes < 1 || entry.bytes > 32 * 1024 * 1024 ||
      !SHA256.test(entry.sha256 ?? '') || entry.path !== `${target}/pulse-platform-adapter.exe`) {
    fail('pulse_windows_plugin_adapter_target_missing');
  }
  const binaryPath = resolve(catalogRoot, entry.path);
  let stat;
  try { stat = lstatSync(binaryPath); } catch { fail('pulse_windows_plugin_adapter_missing'); }
  if (!stat.isFile() || stat.isSymbolicLink() || stat.nlink !== 1 || stat.size !== entry.bytes) {
    fail('pulse_windows_plugin_adapter_unsafe');
  }
  const canonicalRoot = realpathSync(catalogRoot);
  const canonicalBinary = realpathSync(binaryPath);
  const child = relative(canonicalRoot, canonicalBinary);
  if (child === '' || child === '..' || child.startsWith('..\\') || isAbsolute(child) ||
      createHash('sha256').update(readFileSync(canonicalBinary)).digest('hex') !== entry.sha256) {
    fail('pulse_windows_plugin_adapter_unsafe');
  }
  return { binaryPath: canonicalBinary, target };
}

export function loadPluginWindowsAdapter({
  architecture = process.arch,
  catalogRoot = defaultCatalogRoot,
  invoke = invokeBinary,
} = {}) {
  const { binaryPath, target } = validatedBinary(catalogRoot, architecture);
  const call = (operation, payload = {}) => invoke({ binaryPath, operation, payload, target });
  const contract = call('contract');
  if (!exactObject(contract, ['operations', 'schema', 'target', 'version']) ||
      contract.schema !== CONTRACT_SCHEMA || contract.target !== target || contract.version !== 1 ||
      JSON.stringify(contract.operations) !== JSON.stringify(EXPECTED_OPERATIONS)) {
    fail('pulse_windows_plugin_adapter_contract_invalid');
  }
  return Object.freeze({
    readPrivateFile(path, { minBytes = 1, maxBytes = 1024 * 1024 } = {}) {
      const result = call('read_private_file', {
        encoding: '', maximum_bytes: maxBytes, minimum_bytes: minBytes, path,
      });
      if (!exactObject(result, ['bytes_base64']) || typeof result.bytes_base64 !== 'string') {
        fail('pulse_windows_plugin_adapter_protocol_invalid');
      }
      return Buffer.from(result.bytes_base64, 'base64');
    },
    inspectPrivateTree(path, { entries, maximumDepth, maximumEntries, maximumTotalBytes }) {
      return call('inspect_private_tree', {
        entries, maximum_depth: maximumDepth, maximum_entries: maximumEntries,
        maximum_total_bytes: maximumTotalBytes, path,
      });
    },
    inspectExecutable(path) { return call('inspect_executable', { path }); },
  });
}
