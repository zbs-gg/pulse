import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  chmodSync, closeSync, constants, copyFileSync, cpSync, existsSync, fstatSync, lstatSync,
  mkdirSync, mkdtempSync, openSync, readSync, readdirSync, realpathSync, rmSync, renameSync, writeFileSync,
  writeSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { basename, dirname, isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  assertSafeArchivePaths,
  buildPulseArtifactTree,
  buildRuntimeInternalManifest,
  canonicalRuntimeJSON,
  loadManagedEmbedderSourceManifest,
} from '../src/managed-embedder-release.js';
import { downloadPinnedSource, ensurePrivateSourceCache } from '../src/managed-embedder-download.js';
import { acquireInstallLock } from '../src/install-journal.js';

const EXPECTED_TEAM_ID = '44N4NZ86S5';
const DEFAULT_IDENTITY = `Developer ID Application: Nikita Shilov (${EXPECTED_TEAM_ID})`;
const scriptRoot = dirname(fileURLToPath(import.meta.url));
const cliRoot = resolve(scriptRoot, '..');
const sourceRoot = join(cliRoot, 'runtime', 'embedder');
const nativeRoot = resolve(cliRoot, '..', 'native', 'pulse-embedder-runtime');
const outputDir = join(nativeRoot, 'dist');
const publishedRoot = join(outputDir, 'current');
const publishedCarrier = join(publishedRoot, 'gg.zbs.pulse.embedder-runtime.dmg');
const carrierIdentifier = 'gg.zbs.pulse.embedder-runtime';
const cacheRoot = resolve(process.env.PULSE_EMBEDDER_SOURCE_CACHE ?? join(nativeRoot, '.source-cache'));
const productionRelease = process.env.PULSE_PRODUCTION_RELEASE === '1';
const identity = process.env.PULSE_EMBEDDER_CODESIGN_IDENTITY ?? (productionRelease ? DEFAULT_IDENTITY : '-');
const notaryProfile = process.env.PULSE_NOTARYTOOL_PROFILE;
let sources;

function digestFile(path, maximum = 2 * 1024 * 1024 * 1024) {
  let descriptor;
  try {
    descriptor = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW);
    const stat = fstatSync(descriptor);
    if (!stat.isFile() || stat.nlink !== 1 || stat.size < 1 || stat.size > maximum) {
      throw new Error('digest input is not a bounded single-link regular file');
    }
    const hash = createHash('sha256');
    const buffer = Buffer.allocUnsafe(1024 * 1024);
    let bytes = 0;
    for (;;) {
      const count = readSync(descriptor, buffer, 0, buffer.length, null);
      if (count === 0) break;
      hash.update(buffer.subarray(0, count));
      bytes += count;
    }
    if (bytes !== stat.size) throw new Error('digest input changed while reading');
    return { bytes, digest: hash.digest('hex'), mode: stat.mode & 0o777, uid: stat.uid };
  } finally {
    if (descriptor !== undefined) closeSync(descriptor);
  }
}

async function fetchPinned(component) {
  const path = join(cacheRoot, `${component.id}.source`);
  if (existsSync(path)) {
    const stat = lstatSync(path);
    let verified = null;
    try { verified = stat.isFile() && !stat.isSymbolicLink() ? digestFile(path) : null; } catch { /* replace invalid cache input */ }
    const uid = typeof process.geteuid === 'function' ? process.geteuid() : stat.uid;
    if (verified && verified.bytes === component.bytes && verified.digest === component.sha256 &&
        verified.uid === uid && (verified.mode & 0o077) === 0) return path;
    rmSync(path, { force: true });
  }
  const partial = `${path}.partial`;
  rmSync(partial, { force: true });
  return downloadPinnedSource(component, {
    allowedOrigins: sources.allowed_origins,
    allowedRedirectOrigins: sources.allowed_redirect_origins,
    destination: path,
  });
}

function archiveEntries(command, args) {
  return execFileSync(command, args, { encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 })
    .split('\n').filter(Boolean).map((value) => value.endsWith('/') ? value.slice(0, -1) : value).filter(Boolean);
}

function extractPython(archive, staging) {
  const entries = archiveEntries('/usr/bin/tar', ['-tzf', archive]);
  assertSafeArchivePaths(entries);
  const verbose = execFileSync('/usr/bin/tar', ['-tvzf', archive], { encoding: 'utf8', maxBuffer: 32 * 1024 * 1024 });
  if (verbose.split('\n').filter(Boolean).some((line) => !['-', 'd', 'l'].includes(line[0]))) {
    throw new Error('python source archive contains unsupported entries');
  }
  const extracted = join(staging, '.python-extracted');
  mkdirSync(extracted, { mode: 0o700 });
  execFileSync('/usr/bin/tar', ['-xzf', archive, '-C', extracted, '--no-same-owner', '--no-same-permissions']);
  const pythonRoot = join(extracted, 'python');
  if (!lstatSync(pythonRoot).isDirectory()) throw new Error('python source archive root mismatch');
  cpSync(pythonRoot, join(staging, 'runtime'), { recursive: true, dereference: false });
  rmSync(extracted, { recursive: true, force: true });
  removeLinks(join(staging, 'runtime'));
}

function removeLinks(root) {
  for (const name of readdirSync(root)) {
    const path = join(root, name);
    const stat = lstatSync(path);
    if (stat.isSymbolicLink()) {
      rmSync(path, { force: true });
    } else if (stat.isDirectory()) {
      removeLinks(path);
    }
  }
}

function pruneGeneratedFiles(root) {
  for (const name of readdirSync(root)) {
    const path = join(root, name);
    const stat = lstatSync(path);
    if (name === '__pycache__' || name.endsWith('.pyc') || name === '.DS_Store') {
      rmSync(path, { recursive: true, force: true });
    } else if (stat.isDirectory() && !stat.isSymbolicLink()) {
      pruneGeneratedFiles(path);
    }
  }
}

function normalizePrivateModes(root) {
  for (const name of readdirSync(root)) {
    const path = join(root, name);
    const stat = lstatSync(path);
    if (stat.isSymbolicLink()) throw new Error('managed runtime still contains a symlink');
    if (stat.isDirectory()) {
      chmodSync(path, 0o700);
      normalizePrivateModes(path);
    } else if (stat.isFile()) {
      chmodSync(path, (stat.mode & 0o111) !== 0 ? 0o700 : 0o600);
    } else {
      throw new Error('managed runtime contains an unsupported entry');
    }
  }
  chmodSync(root, 0o700);
}

function extractWheel(archive, sitePackages) {
  const entries = archiveEntries('/usr/bin/unzip', ['-Z1', archive]);
  assertSafeArchivePaths(entries);
  execFileSync('/usr/bin/ditto', ['-x', '-k', archive, sitePackages]);
  removeLinks(sitePackages);
}

function copyPinnedReferenceFile(source, destination, expected) {
  const resolvedSource = realpathSync(source);
  mkdirSync(dirname(destination), { recursive: true, mode: 0o700 });
  let sourceDescriptor;
  let destinationDescriptor;
  try {
    sourceDescriptor = openSync(resolvedSource, constants.O_RDONLY | constants.O_NOFOLLOW);
    const stat = fstatSync(sourceDescriptor);
    if (!stat.isFile() || stat.size !== expected.bytes || stat.nlink !== 1) {
      throw new Error(`reference model file metadata mismatch: ${expected.path}`);
    }
    destinationDescriptor = openSync(
      destination,
      constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW,
      0o600,
    );
    const hash = createHash('sha256');
    const buffer = Buffer.allocUnsafe(1024 * 1024);
    let bytes = 0;
    for (;;) {
      const count = readSync(sourceDescriptor, buffer, 0, buffer.length, null);
      if (count === 0) break;
      hash.update(buffer.subarray(0, count));
      let written = 0;
      while (written < count) {
        const writeCount = writeSync(destinationDescriptor, buffer, written, count - written);
        if (writeCount < 1) throw new Error(`reference model file write stalled: ${expected.path}`);
        written += writeCount;
      }
      bytes += count;
    }
    if (bytes !== expected.bytes || hash.digest('hex') !== expected.sha256) {
      throw new Error(`reference model file digest mismatch: ${expected.path}`);
    }
  } catch (error) {
    rmSync(destination, { force: true });
    throw error;
  } finally {
    if (destinationDescriptor !== undefined) closeSync(destinationDescriptor);
    if (sourceDescriptor !== undefined) closeSync(sourceDescriptor);
  }
}

function preparePinnedQualityReference(work, referenceWheel) {
  const bootstrapPython = exactExecutable(
    process.env.PULSE_FLAGEMBEDDING_PYTHON,
    'PULSE_FLAGEMBEDDING_PYTHON',
  );
  const packageRoot = join(work, 'quality-reference-package');
  mkdirSync(packageRoot, { mode: 0o700 });
  extractWheel(referenceWheel, packageRoot);
  pruneGeneratedFiles(packageRoot);
  normalizePrivateModes(packageRoot);

  const suppliedModel = process.env.PULSE_BGE_M3_REFERENCE_MODEL;
  if (typeof suppliedModel !== 'string' || resolve(suppliedModel) !== suppliedModel) {
    throw new Error('production quality gate requires an absolute PULSE_BGE_M3_REFERENCE_MODEL snapshot');
  }
  const sourceModel = realpathSync(suppliedModel);
  if (!lstatSync(sourceModel).isDirectory()) {
    throw new Error('production quality gate reference model is not a directory');
  }
  const modelRoot = join(work, 'quality-reference-model');
  mkdirSync(modelRoot, { mode: 0o700 });
  for (const expected of sources.quality_reference.model_files) {
    copyPinnedReferenceFile(join(sourceModel, expected.path), join(modelRoot, expected.path), expected);
  }
  return { bootstrapPython, modelRoot, packageRoot };
}

function copyRuntimeSources(staging, downloaded) {
  copyFileSync(join(sourceRoot, 'helper.py'), join(staging, 'helper.py'));
  chmodSync(join(staging, 'helper.py'), 0o644);
  cpSync(join(sourceRoot, 'pulse_embedder'), join(staging, 'pulse_embedder'), { recursive: true, filter: (path) => !path.includes('__pycache__') && !path.endsWith('.pyc') });
  cpSync(join(sourceRoot, 'LICENSES'), join(staging, 'LICENSES'), { recursive: true });
  copyFileSync(join(sourceRoot, 'ATTRIBUTION.md'), join(staging, 'ATTRIBUTION.md'));
  for (const name of ['fixture-contract.json', 'quality_gate.py', 'source-manifest.json']) {
    copyFileSync(join(sourceRoot, name), join(staging, name));
  }
  const support = join(staging, 'support');
  mkdirSync(support, { mode: 0o755 });
  for (const id of ['bge-m3-config', 'bge-m3-tokenizer']) {
    const component = sources.components.find((item) => item.id === id);
    copyFileSync(downloaded.get(id), join(staging, basename(component.destination)));
  }
  // Correct destination after copy while keeping filenames exact.
  renameSync(join(staging, 'config.json'), join(support, 'config.json'));
  renameSync(join(staging, 'tokenizer.json'), join(support, 'tokenizer.json'));
  const sbom = {
    components: sources.components.map(({ id, license, runtime, sha256, url, version }) => ({ id, license, runtime, sha256, url, version })),
    schema: 'pulse.embedder_runtime.sbom.v1',
  };
  writeFileSync(join(staging, 'SBOM.json'), `${canonicalRuntimeJSON(sbom)}\n`, { mode: 0o644 });
}

function signAllMachO(staging) {
  let value = buildRuntimeInternalManifest(staging);
  const { machOPaths } = value;
  if (machOPaths.length < 3) throw new Error('managed runtime Mach-O inventory is unexpectedly small');
  for (const path of machOPaths) {
	const args = ['--force'];
	if (productionRelease) args.push('--options', 'runtime', '--timestamp');
    args.push('--sign', identity, path);
    execFileSync('/usr/bin/codesign', args, { stdio: 'inherit' });
    execFileSync('/usr/bin/codesign', ['--verify', '--strict', '--verbose=2', path], { stdio: 'inherit' });
  }
  value = buildRuntimeInternalManifest(staging);
  if (value.machOPaths.length !== machOPaths.length) throw new Error('Mach-O inventory changed during signing');
  for (const entry of value.manifest.entries) {
    chmodSync(join(staging, entry.path), entry.kind === 'directory' ? 0o700 : entry.mach_o ? 0o700 : 0o600);
  }
  chmodSync(staging, 0o700);
  value = buildRuntimeInternalManifest(staging);
  writeFileSync(join(staging, 'internal-manifest.json'), `${canonicalRuntimeJSON(value.manifest)}\n`, { mode: 0o644 });
  chmodSync(join(staging, 'internal-manifest.json'), 0o600);
  const artifactTree = buildPulseArtifactTree(staging);
  writeFileSync(join(staging, 'pulse-artifact-tree.json'), `${canonicalRuntimeJSON(artifactTree)}\n`, { mode: 0o600 });
  return { ...value, artifactTree };
}

function exactExecutable(path, label) {
  if (typeof path !== 'string' || !isAbsolute(path) || resolve(path) !== path) throw new Error(`${label} must be an absolute clean path`);
  const resolved = realpathSync(path);
  const stat = lstatSync(resolved);
  if (!stat.isFile() || stat.isSymbolicLink() || (stat.mode & 0o111) === 0 || (stat.mode & 0o022) !== 0) {
    throw new Error(`${label} is unavailable or unsafe`);
  }
  return resolved;
}

function canonicalEvidence(value) {
  if (value === null || typeof value === 'string' || typeof value === 'boolean') return JSON.stringify(value);
  if (typeof value === 'number') {
    if (!Number.isFinite(value) || Object.is(value, -0)) throw new Error('quality evidence contains an invalid number');
    return JSON.stringify(value);
  }
  if (Array.isArray(value)) return `[${value.map(canonicalEvidence).join(',')}]`;
  if (value && typeof value === 'object') {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonicalEvidence(value[key])}`).join(',')}}`;
  }
  throw new Error('quality evidence contains an invalid value');
}

function runQualityGate(staging, qualityReference) {
  const gate = join(staging, 'quality_gate.py');
  let python = join(staging, 'runtime', 'bin', 'python3.12');
  const args = [gate];
  if (productionRelease) {
    if (!qualityReference) throw new Error('production quality reference was not prepared');
    python = qualityReference.bootstrapPython;
    const mlxModel = process.env.PULSE_BGE_M3_MLX_MODEL;
    const weights = sources.components.find((item) => item.id === 'bge-m3-mlx-fp16');
    if (typeof mlxModel !== 'string' || resolve(mlxModel) !== mlxModel) {
      throw new Error('production quality gate requires PULSE_BGE_M3_MLX_MODEL');
    }
    const modelStat = lstatSync(mlxModel);
    if (!modelStat.isFile() || modelStat.isSymbolicLink() || modelStat.size !== weights.bytes || digestFile(mlxModel).digest !== weights.sha256) {
      throw new Error('production quality gate MLX model does not match the pinned data-only artifact');
    }
    args.push(
      '--managed-python', join(staging, 'runtime', 'bin', 'python3.12'),
      '--managed-helper', join(staging, 'helper.py'),
      '--model-file', mlxModel,
      '--support-directory', join(staging, 'support'),
      '--reference-model', qualityReference.modelRoot,
      '--reference-model-manifest-sha256', 'fa4361447341e16d2a95095ce369e67eafad53cfb93eac741418d722dac5f5f8',
      '--reference-package-root', qualityReference.packageRoot,
      '--reference-package-sha256', sources.quality_reference.package_sha256,
    );
  }
  const output = execFileSync(python, args, {
    encoding: 'utf8',
    env: productionRelease ? {
      ...process.env,
      HF_HUB_OFFLINE: '1',
      PYTHONDONTWRITEBYTECODE: '1',
      PYTHONNOUSERSITE: '1',
      PYTHONPATH: '',
      TRANSFORMERS_OFFLINE: '1',
    } : process.env,
    maxBuffer: 1024 * 1024,
    timeout: productionRelease ? 30 * 60 * 1000 : 30_000,
  });
  let evidence;
  try { evidence = JSON.parse(output); } catch { throw new Error('managed embedder quality gate emitted invalid evidence'); }
  if (evidence?.schema !== 'pulse.embedder.quality_gate.v1' || evidence.fixture !== 'pass' ||
      (productionRelease && evidence.quality_claimed !== true) || (!productionRelease && evidence.quality_claimed !== false)) {
    throw new Error('managed embedder quality gate did not satisfy the release policy');
  }
  const bytes = `${canonicalEvidence(evidence)}\n`;
  // QUALITY.json is hashed by pulse-artifact-tree.json, so an installed
  // runtime proves the exact gate evidence under which its carrier shipped.
  writeFileSync(join(staging, 'QUALITY.json'), bytes, { mode: 0o600 });
  return { evidence, receiptBytes: bytes };
}

async function main() {
  const buildLock = acquireInstallLock(join(nativeRoot, '.build-locks', 'embedder-runtime.lock'));
  let pendingRoot;
  let work;
  let published = false;
  try {
    mkdirSync(outputDir, { recursive: true, mode: 0o755 });
    rmSync(publishedRoot, { recursive: true, force: true });
    if (process.platform !== 'darwin' || process.arch !== 'arm64') {
      throw new Error('managed embedder runtime can only be built on Apple Silicon macOS');
    }
    if (productionRelease && (identity === '-' || !identity.includes(`(${EXPECTED_TEAM_ID})`))) {
      throw new Error('production embedder runtime requires the authorized Developer ID identity');
    }
    if (productionRelease && (typeof notaryProfile !== 'string' || notaryProfile.length < 1 ||
        notaryProfile.length > 128 || /[\r\n\0]/.test(notaryProfile))) {
      throw new Error('production embedder runtime requires an authorized PULSE_NOTARYTOOL_PROFILE');
    }
    if (productionRelease && ['PULSE_FLAGEMBEDDING_PYTHON', 'PULSE_BGE_M3_REFERENCE_MODEL', 'PULSE_BGE_M3_MLX_MODEL']
      .some((name) => typeof process.env[name] !== 'string' || !isAbsolute(process.env[name]))) {
      throw new Error('production embedder runtime requires pinned absolute quality-gate inputs');
    }
    sources = loadManagedEmbedderSourceManifest();
    ensurePrivateSourceCache(cacheRoot);

    const downloaded = new Map();
    for (const component of sources.components.filter((item) => item.runtime)) {
      downloaded.set(component.id, await fetchPinned(component));
    }
    const referenceWheel = productionRelease ? await fetchPinned({
      bytes: sources.quality_reference.package_bytes,
      id: 'flagembedding-reference',
      sha256: sources.quality_reference.package_sha256,
      url: sources.quality_reference.package_url,
    }) : null;
    work = mkdtempSync(join(tmpdir(), 'pulse-managed-embedder-build-'));
    const staging = join(work, 'Pulse Managed Embedder');
    pendingRoot = join(outputDir, `.generation.${process.pid}.${Date.now()}`);
    const pendingCarrier = join(pendingRoot, 'gg.zbs.pulse.embedder-runtime.dmg');
    const pendingReceipt = `${pendingCarrier}.quality.json`;
    mkdirSync(pendingRoot, { mode: 0o700 });
    mkdirSync(staging, { mode: 0o755 });
    extractPython(downloaded.get('cpython-standalone'), staging);
    const sitePackages = join(staging, 'runtime', 'lib', 'python3.12', 'site-packages');
    mkdirSync(sitePackages, { recursive: true, mode: 0o755 });
    for (const id of ['mlx', 'mlx-metal', 'tokenizers']) extractWheel(downloaded.get(id), sitePackages);
    copyRuntimeSources(staging, downloaded);
    // python-build-standalone and wheels ship caches. They are derived files,
    // not release inputs, and the runtime must remain writable-cache-free.
    pruneGeneratedFiles(staging);
    normalizePrivateModes(staging);
    const qualityReference = productionRelease
      ? preparePinnedQualityReference(work, referenceWheel)
      : null;
    const { evidence: quality, receiptBytes } = runQualityGate(staging, qualityReference);
    pruneGeneratedFiles(staging);
    normalizePrivateModes(staging);
    const { manifest, machOPaths } = signAllMachO(staging);
    execFileSync('/usr/bin/hdiutil', [
      'create', '-volname', 'Pulse Managed Embedder', '-srcfolder', staging, '-format', 'ULFO', '-ov', pendingCarrier,
    ], { stdio: 'inherit' });
    const carrierSignArgs = ['--force'];
    if (productionRelease) carrierSignArgs.push('--timestamp');
    carrierSignArgs.push('--identifier', carrierIdentifier, '--sign', identity, pendingCarrier);
    execFileSync('/usr/bin/codesign', carrierSignArgs, { stdio: 'inherit' });
    execFileSync('/usr/bin/codesign', ['--verify', '--strict', '--verbose=2', pendingCarrier], { stdio: 'inherit' });
    if (productionRelease) {
      execFileSync('/usr/bin/xcrun', ['notarytool', 'submit', pendingCarrier, '--keychain-profile', notaryProfile, '--wait'], { stdio: 'inherit' });
      execFileSync('/usr/bin/xcrun', ['stapler', 'staple', pendingCarrier], { stdio: 'inherit' });
      execFileSync('/usr/bin/xcrun', ['stapler', 'validate', pendingCarrier], { stdio: 'inherit' });
      execFileSync('/usr/sbin/spctl', ['-a', '-t', 'open', '--context', 'context:primary-signature', '-vv', pendingCarrier], { stdio: 'inherit' });
    }
    writeFileSync(pendingReceipt, receiptBytes, { flag: 'wx', mode: 0o600 });
    renameSync(pendingRoot, publishedRoot);
    published = true;
    console.error(`[pulse] managed embedder runtime: ${publishedCarrier}`);
    console.error(`[pulse] tree digest: ${manifest.tree_digest}; signed Mach-O files: ${machOPaths.length}`);
    console.error(`[pulse] quality gate: ${quality.quality_claimed ? 'production pass' : 'fixture only (no quality claim)'}`);
  } catch (error) {
    if (!published) rmSync(publishedRoot, { recursive: true, force: true });
    throw error;
  } finally {
    if (pendingRoot) rmSync(pendingRoot, { recursive: true, force: true });
    if (work) rmSync(work, { recursive: true, force: true });
    buildLock();
  }
}

await main();
