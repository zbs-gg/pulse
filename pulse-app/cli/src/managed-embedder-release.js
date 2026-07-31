import { createHash } from 'node:crypto';
import {
  lstatSync, readFileSync, readdirSync,
} from 'node:fs';
import { basename, join, relative, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const SOURCE_SCHEMA = 'pulse.embedder_runtime.sources.v1';
const INTERNAL_SCHEMA = 'pulse.embedder_runtime.internal_manifest.v1';
const EXPECTED_COMPONENTS = Object.freeze([
  'bge-m3-config',
  'bge-m3-mlx-fp16',
  'bge-m3-tokenizer',
  'cpython-standalone',
  'mlx',
  'mlx-metal',
  'tokenizers',
  'transformers-xlm-roberta-reference',
]);
const EXACT_VERSIONS = Object.freeze({
  'cpython-standalone': '3.12.9+20250212',
  mlx: '0.29.3',
  'mlx-metal': '0.29.3',
  tokenizers: '0.21.1',
});
const SHA256 = /^[a-f0-9]{64}$/;
const SAFE_ID = /^[a-z0-9][a-z0-9._-]{0,127}$/;
const EXPECTED_QUALITY_MODEL_FILES = Object.freeze([
  { bytes: 2100674, path: 'colbert_linear.pt', sha256: '19bfbae397c2b7524158c919d0e9b19393c5639d098f0a66932c91ed8f5f9abb' },
  { bytes: 687, path: 'config.json', sha256: '26159e7ad065073448460117eb24b7a4572f6f4e78eadff65dc0a11c052449fa' },
  { bytes: 2271145830, path: 'pytorch_model.bin', sha256: 'b5e0ce3470abf5ef3831aa1bd5553b486803e83251590ab7ff35a117cf6aad38' },
  { bytes: 5069051, path: 'sentencepiece.bpe.model', sha256: 'cfc8146abe2a0488e9e2a0c56de7952f7c11ab059eca145a0a727afce0db2865' },
  { bytes: 3516, path: 'sparse_linear.pt', sha256: '45c93804d2142b8f6d7ec6914ae23a1eee9c6a1d27d83d908a20d2afb3595ad9' },
  { bytes: 964, path: 'special_tokens_map.json', sha256: '8c785abebea9ae3257b61681b4e6fd8365ceafde980c21970d001e834cf10835' },
  { bytes: 17098108, path: 'tokenizer.json', sha256: '21106b6d7dab2952c1d496fb21d5dc9db75c28ed361a05f5020bbba27810dd08' },
  { bytes: 444, path: 'tokenizer_config.json', sha256: 'a62b2b6784f990259fddef5f16388693a8043be4f69179e6a5257eeb3f9abac4' },
]);
const MACH_O_MAGICS = new Set([
  'feedface', 'feedfacf', 'cefaedfe', 'cffaedfe',
  'cafebabe', 'bebafeca', 'cafebabf', 'bfbafeca',
]);
const VECTOR_CONTRACT = Object.freeze({
  dimensions: 1024,
  model: 'bge-m3',
  normalized: true,
  opset: 17,
  pooling: 'cls',
  quantization: 'dynamic-int8',
  revision: '5617a9f61b028005a4858fdac845db406aefb181',
  source: 'BAAI/bge-m3',
});

export class ManagedEmbedderReleaseError extends Error {
  constructor(code) {
    super(code);
    this.name = 'ManagedEmbedderReleaseError';
    this.code = code;
  }
}

function fail(code) {
  throw new ManagedEmbedderReleaseError(code);
}

function exactKeys(value, expected, code) {
  if (!value || Array.isArray(value) || typeof value !== 'object' ||
      Object.keys(value).sort().join('\0') !== [...expected].sort().join('\0')) fail(code);
}

export function managedEmbedderRuntimeContract({ engine = 'transformers-js-onnx', platform = process.platform } = {}) {
  if (engine === 'mlx') fail('managed_embedder_engine_legacy');
  if (!['darwin', 'linux', 'win32'].includes(platform) || engine !== 'transformers-js-onnx') {
    fail('managed_embedder_engine_invalid');
  }
  return Object.freeze({
    engine,
    runner_relative_path: platform === 'win32' ? 'bin/pulse-embedder.exe' : 'bin/pulse-embedder',
    vector_contract: VECTOR_CONTRACT,
  });
}

export function canonicalRuntimeJSON(value) {
  if (value === null) return 'null';
  if (typeof value === 'string' || typeof value === 'boolean') return JSON.stringify(value);
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value) || Object.is(value, -0)) fail('canonical_number_invalid');
    return String(value);
  }
  if (Array.isArray(value)) return `[${value.map(canonicalRuntimeJSON).join(',')}]`;
  if (value && typeof value === 'object') {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalRuntimeJSON(value[key])}`).join(',')}}`;
  }
  fail('canonical_value_invalid');
}

export function loadManagedEmbedderSourceManifest(
  path = fileURLToPath(new URL('../runtime/embedder/source-manifest.json', import.meta.url)),
) {
  const stat = lstatSync(path);
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size > 128 * 1024) fail('source_manifest_file_invalid');
  let value;
  try { value = JSON.parse(readFileSync(path, 'utf8')); } catch { fail('source_manifest_json_invalid'); }
  exactKeys(value, ['allowed_origins', 'allowed_redirect_origins', 'architecture', 'components', 'minimum_os', 'model_contract', 'platform', 'quality_reference', 'schema'], 'source_manifest_fields_invalid');
  if (value.schema !== SOURCE_SCHEMA || value.platform !== 'darwin' || value.architecture !== 'arm64' || value.minimum_os !== '13.5') {
    fail('source_manifest_platform_invalid');
  }
  if (!Array.isArray(value.allowed_origins) || value.allowed_origins.length < 1 || value.allowed_origins.length > 8 ||
      [...new Set(value.allowed_origins)].sort().join('\0') !== value.allowed_origins.join('\0')) fail('source_origins_invalid');
  const origins = new Set(value.allowed_origins);
  for (const origin of origins) {
    let parsed;
    try { parsed = new URL(origin); } catch { fail('source_origins_invalid'); }
    if (parsed.protocol !== 'https:' || parsed.origin !== origin || parsed.pathname !== '/' || parsed.search || parsed.hash || parsed.username || parsed.password) {
      fail('source_origins_invalid');
    }
  }
  if (!Array.isArray(value.allowed_redirect_origins) || value.allowed_redirect_origins.length < 1 ||
      value.allowed_redirect_origins.length > 8 ||
      [...new Set(value.allowed_redirect_origins)].sort().join('\0') !== value.allowed_redirect_origins.join('\0')) {
    fail('source_redirect_origins_invalid');
  }
  for (const origin of value.allowed_redirect_origins) {
    let parsed;
    try { parsed = new URL(origin); } catch { fail('source_redirect_origins_invalid'); }
    if (parsed.protocol !== 'https:' || parsed.origin !== origin || parsed.pathname !== '/' || parsed.search || parsed.hash ||
        parsed.username || parsed.password) fail('source_redirect_origins_invalid');
  }
  exactKeys(value.model_contract, ['custom_code', 'data_only', 'dimensions', 'model', 'normalized', 'pooling'], 'source_model_contract_invalid');
  if (value.model_contract.custom_code !== false || value.model_contract.data_only !== true ||
      value.model_contract.dimensions !== 1024 || value.model_contract.model !== 'bge-m3' ||
      value.model_contract.normalized !== true || value.model_contract.pooling !== 'cls') fail('source_model_contract_invalid');
  exactKeys(value.quality_reference, [
    'model', 'model_files', 'model_revision', 'package', 'package_bytes', 'package_sha256', 'package_url', 'package_version',
  ], 'quality_reference_invalid');
  const quality = value.quality_reference;
  if (quality.model !== 'BAAI/bge-m3' || quality.model_revision !== '5617a9f61b028005a4858fdac845db406aefb181' ||
      quality.package !== 'FlagEmbedding' || quality.package_version !== '1.4.0' || quality.package_bytes !== 247714 ||
      quality.package_sha256 !== 'fb1856b312851591341cf4533187350e9ce43f66bbf195c66f25a73266ff7db9') {
    fail('quality_reference_invalid');
  }
  if (canonicalRuntimeJSON(quality.model_files) !== canonicalRuntimeJSON(EXPECTED_QUALITY_MODEL_FILES)) {
    fail('quality_reference_invalid');
  }
  let qualityURL;
  try { qualityURL = new URL(quality.package_url); } catch { fail('quality_reference_invalid'); }
  if (qualityURL.protocol !== 'https:' || !origins.has(qualityURL.origin) || qualityURL.search || qualityURL.hash || qualityURL.username || qualityURL.password) {
    fail('quality_reference_invalid');
  }
  if (!Array.isArray(value.components) || value.components.length !== EXPECTED_COMPONENTS.length) fail('source_components_invalid');
  const ids = [];
  for (const component of value.components) {
    exactKeys(component, ['bytes', 'destination', 'id', 'license', 'runtime', 'sha256', 'url', 'version'], 'source_component_fields_invalid');
    if (typeof component.id !== 'string' || !SAFE_ID.test(component.id)) fail('source_component_id_invalid');
    ids.push(component.id);
    if (!Number.isSafeInteger(component.bytes) || component.bytes < 1 || component.bytes > 2 * 1024 * 1024 * 1024 ||
        typeof component.sha256 !== 'string' || !SHA256.test(component.sha256) ||
        typeof component.version !== 'string' || component.version.length > 128 ||
        typeof component.license !== 'string' || !['Apache-2.0', 'MIT', 'PSF-2.0'].includes(component.license) ||
        typeof component.runtime !== 'boolean' || typeof component.destination !== 'string') fail('source_component_value_invalid');
    let parsed;
    try { parsed = new URL(component.url); } catch { fail('source_component_url_invalid'); }
    if (parsed.protocol !== 'https:' || parsed.username || parsed.password || parsed.search || parsed.hash || !origins.has(parsed.origin) ||
        parsed.pathname.split('/').some((part) => part === '.' || part === '..')) fail('source_component_url_invalid');
  }
  if (ids.sort().join('\0') !== EXPECTED_COMPONENTS.join('\0')) fail('source_components_invalid');
  for (const [id, version] of Object.entries(EXACT_VERSIONS)) {
    if (value.components.find((item) => item.id === id)?.version !== version) fail('source_component_version_invalid');
  }
  const weights = value.components.find((item) => item.id === 'bge-m3-mlx-fp16');
  const reference = value.components.find((item) => item.id === 'transformers-xlm-roberta-reference');
  if (weights.runtime !== false || weights.destination !== 'separate-model-artifact' || weights.license !== 'MIT' ||
      reference.runtime !== false || reference.destination !== 'provenance-only' || reference.license !== 'Apache-2.0') {
    fail('source_component_policy_invalid');
  }
  return Object.freeze(value);
}

export function sha256Bytes(data) {
  return createHash('sha256').update(data).digest('hex');
}

export function isMachO(data) {
  return Buffer.isBuffer(data) && data.length >= 4 && MACH_O_MAGICS.has(data.subarray(0, 4).toString('hex'));
}

function safeRelative(root, path) {
  const value = relative(root, path).split(sep).join('/');
  if (!value || value.startsWith('../') || value.includes('/../') || value.includes('\0')) fail('runtime_path_invalid');
  return value;
}

function walkRuntime(root, current, entries, machOPaths) {
  for (const name of readdirSync(current).sort()) {
    if (name === 'internal-manifest.json' || name === 'pulse-artifact-tree.json') continue;
    if (name === '__pycache__' || name.endsWith('.pyc') || name === '.DS_Store') fail('runtime_generated_file_forbidden');
    const path = join(current, name);
    const stat = lstatSync(path);
    const itemPath = safeRelative(root, path);
    if (stat.isSymbolicLink() || (!stat.isDirectory() && !stat.isFile())) fail('runtime_special_file_forbidden');
    if (stat.mode & 0o022) fail('runtime_writable_by_others');
    if (stat.isDirectory()) {
      entries.push(Object.freeze({ kind: 'directory', mode: stat.mode & 0o777, path: itemPath }));
      walkRuntime(root, path, entries, machOPaths);
      continue;
    }
    const data = readFileSync(path);
    const executable = isMachO(data);
    if (executable) machOPaths.push(path);
    entries.push(Object.freeze({
      bytes: stat.size,
      kind: 'file',
      mach_o: executable,
      mode: stat.mode & 0o777,
      path: itemPath,
      sha256: sha256Bytes(data),
    }));
  }
}

export function buildRuntimeInternalManifest(root) {
  const rootStat = lstatSync(root);
  if (!rootStat.isDirectory() || rootStat.isSymbolicLink()) fail('runtime_root_invalid');
  const entries = [];
  const machOPaths = [];
  walkRuntime(root, root, entries, machOPaths);
  const payload = {
    entries,
    schema: INTERNAL_SCHEMA,
  };
  const treeDigest = sha256Bytes(Buffer.from(canonicalRuntimeJSON(payload)));
  return Object.freeze({
    manifest: Object.freeze({ ...payload, tree_digest: treeDigest }),
    machOPaths: Object.freeze(machOPaths),
  });
}

// This is the carrier-to-installer contract. Unlike the provenance manifest,
// it contains files only and records the normalized private install modes.
// pulse-artifact-tree.json is intentionally not self-referential.
export function buildPulseArtifactTree(root) {
  const { manifest } = buildRuntimeInternalManifest(root);
  const files = manifest.entries.filter((entry) => entry.kind === 'file').map((entry) => {
    const mode = entry.mach_o ? 0o700 : 0o600;
    if (entry.mode !== mode) fail('artifact_tree_mode_not_normalized');
    return Object.freeze({
      bytes: entry.bytes,
      executable: entry.mach_o,
      mode,
      path: entry.path,
      sha256: entry.sha256,
    });
  });
  if (files.length < 1 || files.some((entry) => entry.path === 'pulse-artifact-tree.json')) fail('artifact_tree_invalid');
  return Object.freeze({ files: Object.freeze(files), schema: 'pulse.artifact_tree.v1' });
}

export function assertSafeArchivePaths(paths) {
  if (!Array.isArray(paths) || paths.length < 1 || paths.length > 50_000) fail('archive_entries_invalid');
  for (const value of paths) {
    if (typeof value !== 'string' || value.length < 1 || value.length > 1024 || value.startsWith('/') || value.includes('\\') ||
        value.split('/').some((part) => part === '.' || part === '..' || part === '') || basename(value) === '.DS_Store') fail('archive_path_invalid');
  }
  return true;
}
