#!/usr/bin/env node

import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  chmodSync, cpSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, isAbsolute, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

import { writeProductEdgeFixture } from './product-release-fixture.mjs';
import {
  packNormalizedArtifact, prepareNormalizedArtifact,
} from './release-builder-core.mjs';

const scriptPath = fileURLToPath(import.meta.url);
const scriptRoot = dirname(scriptPath);
const cliRoot = resolve(scriptRoot, '..');

function fail(code) {
  throw new Error(code);
}

function parseCLI(argv) {
  if (argv.length !== 2 || argv[0] !== '--output') fail('plugin_runtime_arguments_invalid');
  const outputRoot = resolve(argv[1]);
  if (!isAbsolute(outputRoot)) fail('plugin_runtime_arguments_invalid');
  return { outputRoot };
}

const NATIVE_MAGICS = new Set([
  '7f454c46',
  'feedface', 'feedfacf', 'cefaedfe', 'cffaedfe',
  'cafebabe', 'bebafeca', 'cafebabf', 'bfbafeca',
]);

function allowedWindowsAdapter(root, path, header) {
  if (header.subarray(0, 2).toString('ascii') !== 'MZ') return false;
  const item = relative(root, path).split(sep).join('/');
  return /^marketplace\/plugins\/pulse\/native\/windows-bootstrap\/win32-(?:arm64|x64)\/pulse-platform-adapter\.exe$/.test(item) ||
    /^runtime\/runtime\/windows-bootstrap\/win32-(?:arm64|x64)\/pulse-platform-adapter\.exe$/.test(item);
}

function normalizeCommonRuntime(path, root = path) {
  const info = lstatSync(path);
  if (info.isSymbolicLink() || (!info.isDirectory() && !info.isFile())) {
    fail('plugin_runtime_tree_unsafe');
  }
  if (info.isDirectory()) {
    chmodSync(path, 0o700);
    for (const name of readdirSync(path)) normalizeCommonRuntime(join(path, name), root);
    return;
  }
  const header = readFileSync(path).subarray(0, 4);
  if (NATIVE_MAGICS.has(header.toString('hex')) ||
      (header.subarray(0, 2).toString('ascii') === 'MZ' && !allowedWindowsAdapter(root, path, header))) {
    fail('plugin_runtime_native_binary_forbidden');
  }
  chmodSync(path, 0o600);
}

function verifyWindowsAdapterCatalog(root) {
  const catalogPath = join(root, 'catalog.json');
  const catalog = JSON.parse(readFileSync(catalogPath, 'utf8'));
  if (catalog?.schema !== 'pulse.windows_bootstrap_adapter_catalog.v1' || catalog.protocol !== 1 ||
      Object.keys(catalog.adapters ?? {}).sort().join('\0') !== 'win32-arm64\0win32-x64') {
    fail('plugin_runtime_windows_adapter_catalog_invalid');
  }
  for (const [target, entry] of Object.entries(catalog.adapters)) {
    if (entry?.target !== target || entry.path !== `${target}/pulse-platform-adapter.exe` ||
        !Number.isSafeInteger(entry.bytes) || entry.bytes < 1 || !/^[a-f0-9]{64}$/.test(entry.sha256 ?? '')) {
      fail('plugin_runtime_windows_adapter_catalog_invalid');
    }
    const path = join(root, entry.path);
    const bytes = readFileSync(path);
    if (bytes.length !== entry.bytes || createHash('sha256').update(bytes).digest('hex') !== entry.sha256) {
      fail('plugin_runtime_windows_adapter_catalog_invalid');
    }
  }
}

function productionDependencies(work) {
  const root = join(work, 'production-dependencies');
  mkdirSync(root, { mode: 0o700 });
  for (const name of ['package.json', 'package-lock.json']) {
    cpSync(join(cliRoot, name), join(root, name));
  }
  execFileSync('npm', [
    'ci', '--omit=dev', '--ignore-scripts', '--no-audit', '--no-fund',
  ], {
    cwd: root,
    env: { ...process.env, npm_config_update_notifier: 'false' },
    stdio: 'inherit',
    timeout: 10 * 60 * 1000,
  });
  const nodeModules = join(root, 'node_modules');
  if (!lstatSync(nodeModules).isDirectory() || lstatSync(nodeModules).isSymbolicLink() ||
      readdirSync(nodeModules).includes('esbuild') || readdirSync(nodeModules).includes('@esbuild')) {
    fail('plugin_runtime_production_dependencies_invalid');
  }
  return nodeModules;
}

export async function buildPluginRuntime({ outputRoot } = {}) {
  if (typeof outputRoot !== 'string' || !isAbsolute(outputRoot) || resolve(outputRoot) !== outputRoot) {
    fail('plugin_runtime_arguments_invalid');
  }
  mkdirSync(dirname(outputRoot), { recursive: true, mode: 0o700 });
  mkdirSync(outputRoot, { mode: 0o700 });
  const work = mkdtempSync(join(tmpdir(), 'pulse-plugin-runtime-'));
  const runtimeRoot = join(work, 'plugin-runtime');
  mkdirSync(runtimeRoot, { mode: 0o700 });
  let complete = false;
  try {
    const packageVersion = JSON.parse(readFileSync(join(cliRoot, 'package.json'), 'utf8')).version;
    writeProductEdgeFixture(runtimeRoot, { runtimeNodeModulesRoot: productionDependencies(work) });
    normalizeCommonRuntime(runtimeRoot);
    verifyWindowsAdapterCatalog(join(runtimeRoot, 'marketplace', 'plugins', 'pulse', 'native', 'windows-bootstrap'));
    verifyWindowsAdapterCatalog(join(runtimeRoot, 'runtime', 'runtime', 'windows-bootstrap'));
    const pluginManifest = JSON.parse(readFileSync(
      join(runtimeRoot, 'marketplace', 'plugins', 'pulse', '.codex-plugin', 'plugin.json'),
      'utf8',
    ));
    const runtimePackage = JSON.parse(readFileSync(join(runtimeRoot, 'runtime', 'package.json'), 'utf8'));
    const hookBundle = join(runtimeRoot, 'runtime', 'src', 'product-hook-entrypoint.bundle.js');
    const mcpRuntime = join(runtimeRoot, 'runtime', 'vendor', 'pulse-mcp-dist', 'index.js');
    if (pluginManifest.version !== packageVersion || runtimePackage.version !== packageVersion ||
        !lstatSync(hookBundle).isFile() || lstatSync(hookBundle).isSymbolicLink() ||
        !lstatSync(mcpRuntime).isFile() || lstatSync(mcpRuntime).isSymbolicLink()) {
      fail('plugin_runtime_product_edge_invalid');
    }
    const prepared = prepareNormalizedArtifact(runtimeRoot);
    const carrier = await packNormalizedArtifact(runtimeRoot, join(outputRoot, 'plugin-runtime.tar.gz'));
    const fragment = Object.freeze({
      artifact: Object.freeze({
        ...carrier,
        filename: 'plugin-runtime.tar.gz',
        tree_digest: prepared.tree_digest,
      }),
      package_version: packageVersion,
      production_ready: true,
      schema: 'pulse.plugin_runtime_build.v1',
    });
    writeFileSync(join(outputRoot, 'plugin-runtime-fragment.json'), `${JSON.stringify(fragment)}\n`, {
      encoding: 'utf8', flag: 'wx', mode: 0o600,
    });
    const materializedParent = join(outputRoot, 'materialized');
    mkdirSync(materializedParent, { mode: 0o700 });
    cpSync(runtimeRoot, join(materializedParent, 'plugin-runtime'), {
      recursive: true, dereference: false,
    });
    complete = true;
    return fragment;
  } finally {
    rmSync(work, { recursive: true, force: true });
    if (!complete) rmSync(outputRoot, { recursive: true, force: true });
  }
}

if (process.argv[1] && resolve(process.argv[1]) === scriptPath) {
  const result = await buildPluginRuntime(parseCLI(process.argv.slice(2)));
  process.stdout.write(`${JSON.stringify(result)}\n`);
}
