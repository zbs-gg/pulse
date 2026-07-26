#!/usr/bin/env node

import {
  chmodSync, copyFileSync, cpSync, lstatSync, mkdirSync, readFileSync, readdirSync, writeFileSync,
} from 'node:fs';
import { dirname, isAbsolute, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const LOCKED_DEPENDENCIES = Object.freeze({
  '@huggingface/transformers': '4.2.0',
  'onnxruntime-node': '1.24.3',
});
const TARGETS = new Set(['darwin', 'linux', 'win32']);

function exactDependencies(value) {
  return value && Object.keys(value).sort().join('\0') === Object.keys(LOCKED_DEPENDENCIES).sort().join('\0') &&
    Object.entries(LOCKED_DEPENDENCIES).every(([name, version]) => value[name] === version);
}

function inspectTree(path) {
  const stat = lstatSync(path);
  if (stat.isSymbolicLink() || (!stat.isDirectory() && !stat.isFile())) throw new Error('portable target runtime contains an unsafe entry');
  if (stat.isDirectory()) for (const name of readdirSync(path)) inspectTree(join(path, name));
}

function copyProductionRuntime(sourceRoot, destinationRoot) {
  const root = lstatSync(sourceRoot);
  if (!root.isDirectory() || root.isSymbolicLink()) {
    throw new Error('portable target runtime contains an unsafe entry');
  }
  cpSync(sourceRoot, destinationRoot, {
    recursive: true,
    force: true,
    errorOnExist: false,
    dereference: false,
    filter: (sourcePath) => {
      const item = relative(sourceRoot, sourcePath);
      if (item.split(sep).includes('.bin')) return false;
      const stat = lstatSync(sourcePath);
      if (stat.isSymbolicLink()) throw new Error('portable target runtime contains an unsafe entry');
      return stat.isDirectory() || stat.isFile();
    },
  });
}

function normalizeRuntimeData(path) {
  const stat = lstatSync(path);
  if (stat.isSymbolicLink() || (!stat.isDirectory() && !stat.isFile())) {
    throw new Error('portable target runtime contains an unsafe entry');
  }
  if (stat.isDirectory()) {
    chmodSync(path, 0o700);
    for (const name of readdirSync(path)) normalizeRuntimeData(join(path, name));
    return;
  }
  chmodSync(path, 0o600);
}

function manifestAt(path) {
  let value;
  try { value = JSON.parse(readFileSync(path, 'utf8')); } catch { throw new Error('portable target-runtime manifest is invalid'); }
  if (value.private !== true || !exactDependencies(value.dependencies)) {
    throw new Error('portable target-runtime dependencies are not exactly pinned');
  }
  return value;
}

export function buildPortableEmbedderRuntime({
  fixture = false,
  outputRoot,
  platform,
  runnerInput,
  sourceRoot,
  targetRuntimeRoot,
} = {}) {
  if (!TARGETS.has(platform) || !isAbsolute(outputRoot) || resolve(outputRoot) !== outputRoot ||
      !isAbsolute(sourceRoot) || resolve(sourceRoot) !== sourceRoot) {
    throw new Error('portable embedder build arguments are invalid');
  }
  const sourceManifest = join(sourceRoot, 'target-runtime', 'package.json');
  const manifest = manifestAt(sourceManifest);
  mkdirSync(join(outputRoot, 'bin'), { recursive: true, mode: 0o755 });
  mkdirSync(join(outputRoot, 'runtime'), { recursive: true, mode: 0o755 });

  const runnerName = platform === 'win32' ? 'pulse-embedder.exe' : 'pulse-embedder';
  const runner = join(outputRoot, 'bin', runnerName);
  if (fixture) {
    if (platform === 'win32') {
      writeFileSync(runner, Buffer.from('MZ\0PULSE_SYNTHETIC_FIXTURE_ONLY\n', 'binary'), { mode: 0o700 });
    } else {
      copyFileSync(join(sourceRoot, 'runner.mjs'), runner);
      chmodSync(runner, 0o700);
    }
    copyFileSync(sourceManifest, join(outputRoot, 'runtime', 'package.json'));
  } else {
    if (!isAbsolute(runnerInput) || !isAbsolute(targetRuntimeRoot)) {
      throw new Error('real runner and native target-runtime release inputs are required');
    }
    const runnerStat = lstatSync(runnerInput);
    if (!runnerStat.isFile() || runnerStat.isSymbolicLink() || (runnerStat.mode & 0o111) === 0) {
      throw new Error('portable runner release input is invalid');
    }
    manifestAt(join(targetRuntimeRoot, 'package.json'));
    for (const [name, version] of Object.entries(LOCKED_DEPENDENCIES)) {
      const installed = JSON.parse(readFileSync(join(targetRuntimeRoot, 'node_modules', name, 'package.json'), 'utf8'));
      if (installed.version !== version) throw new Error(`portable target runtime has wrong ${name} version`);
    }
    copyFileSync(runnerInput, runner);
    chmodSync(runner, 0o700);
    copyFileSync(join(sourceRoot, 'runner.mjs'), join(outputRoot, 'runtime', 'runner.mjs'));
    chmodSync(join(outputRoot, 'runtime', 'runner.mjs'), 0o600);
    copyProductionRuntime(targetRuntimeRoot, join(outputRoot, 'runtime'));
    inspectTree(join(outputRoot, 'runtime'));
    normalizeRuntimeData(join(outputRoot, 'runtime'));
  }

  const evidence = {
    engine: 'transformers-js-onnx',
    fixture,
    production_ready: false,
    quality_gate: fixture ? 'synthetic-protocol-only' : 'pending-real-model-quality-gate',
    runner_relative_path: `bin/${runnerName}`,
    schema: 'pulse.portable_embedder.build.v1',
    target_runtime: { dependencies: manifest.dependencies },
  };
  writeFileSync(join(outputRoot, 'pulse-portable-embedder-build.json'), `${JSON.stringify(evidence, null, 2)}\n`, { mode: 0o600 });
  return evidence;
}

function parseCLI(argv) {
  const values = {};
  for (let index = 0; index < argv.length; index += 2) {
    if (!argv[index]?.startsWith('--') || argv[index + 1] === undefined) throw new Error('portable embedder build CLI arguments are invalid');
    values[argv[index].slice(2)] = argv[index + 1];
  }
  const scriptRoot = dirname(fileURLToPath(import.meta.url));
  return {
    fixture: values.fixture === '1',
    outputRoot: resolve(values.output ?? ''),
    platform: values.platform,
    runnerInput: values.runner ? resolve(values.runner) : undefined,
    sourceRoot: resolve(scriptRoot, '..', 'runtime', 'embedder-portable'),
    targetRuntimeRoot: values['target-runtime'] ? resolve(values['target-runtime']) : undefined,
  };
}

const invoked = process.argv[1] ? resolve(process.argv[1]) : '';
if (invoked === fileURLToPath(import.meta.url)) {
  const result = buildPortableEmbedderRuntime(parseCLI(process.argv.slice(2)));
  process.stdout.write(`${JSON.stringify(result)}\n`);
}
