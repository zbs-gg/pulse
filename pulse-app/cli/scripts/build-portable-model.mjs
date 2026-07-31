#!/usr/bin/env node

import { execFileSync } from 'node:child_process';
import {
  lstatSync, mkdirSync, realpathSync, rmSync, writeFileSync,
} from 'node:fs';
import { dirname, isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  packNormalizedArtifact, prepareNormalizedArtifact,
} from './release-builder-core.mjs';

const scriptPath = fileURLToPath(import.meta.url);
const scriptRoot = dirname(scriptPath);
const cliRoot = resolve(scriptRoot, '..');
const SHA256 = /^[a-f0-9]{64}$/;

function fail(code, detail = '') {
  throw new Error(detail ? `${code}: ${detail}` : code);
}

function parseCLI(argv) {
  const values = {};
  for (let index = 0; index < argv.length; index += 2) {
    const key = argv[index];
    if (!key?.startsWith('--') || argv[index + 1] === undefined) fail('portable_model_arguments_invalid');
    const name = key.slice(2);
    if (Object.hasOwn(values, name)) fail('portable_model_arguments_invalid');
    values[name] = argv[index + 1];
  }
  const options = {
    outputRoot: values.output ? resolve(values.output) : null,
    python: values.python ? resolve(values.python) : null,
    sourceModel: values['source-model'] ? resolve(values['source-model']) : null,
  };
  if (Object.values(options).some((value) => !value || !isAbsolute(value))) {
    fail('portable_model_arguments_invalid');
  }
  return Object.freeze(options);
}

function validateConversion(value) {
  if (!value || value.schema !== 'pulse.portable_model_conversion.v1' ||
      value.production_ready !== true || !Number.isSafeInteger(value.model_bytes) || value.model_bytes < 1 ||
      !SHA256.test(value.model_sha256 ?? '') || !SHA256.test(value.quality_evidence_digest ?? '') ||
      value.vector_contract?.source !== 'BAAI/bge-m3' ||
      value.vector_contract?.revision !== '5617a9f61b028005a4858fdac845db406aefb181' ||
      value.vector_contract?.quantization !== 'dynamic-int8') {
    fail('portable_model_conversion_invalid');
  }
  return value;
}

export async function buildPortableModel({ outputRoot, python, sourceModel } = {}) {
  for (const [name, value] of Object.entries({ outputRoot, python, sourceModel })) {
    if (typeof value !== 'string' || !isAbsolute(value) || resolve(value) !== value) {
      fail('portable_model_arguments_invalid', name);
    }
  }
  let canonicalPython;
  try { canonicalPython = realpathSync(python); } catch { fail('portable_model_input_invalid'); }
  if (!lstatSync(canonicalPython).isFile() ||
      lstatSync(sourceModel).isSymbolicLink() || !lstatSync(sourceModel).isDirectory()) {
    fail('portable_model_input_invalid');
  }
  mkdirSync(dirname(outputRoot), { recursive: true, mode: 0o700 });
  mkdirSync(outputRoot, { mode: 0o700 });
  mkdirSync(join(outputRoot, 'materialized'), { mode: 0o700 });
  const modelRoot = join(outputRoot, 'materialized', 'model');
  let complete = false;
  try {
    const output = execFileSync(python, [
      join(scriptRoot, 'export-portable-model.py'),
      '--license', join(cliRoot, 'runtime', 'embedder', 'LICENSES', 'BGE-M3-MIT.txt'),
      '--output', modelRoot,
      '--source-model', sourceModel,
    ], {
      cwd: cliRoot,
      encoding: 'utf8',
      env: {
        ...process.env,
        HF_HUB_OFFLINE: '1',
        PYTHONDONTWRITEBYTECODE: '1',
        TOKENIZERS_PARALLELISM: 'false',
        TRANSFORMERS_OFFLINE: '1',
      },
      maxBuffer: 1024 * 1024,
      stdio: ['ignore', 'pipe', 'inherit'],
      timeout: 45 * 60 * 1000,
    });
    let conversion;
    try { conversion = validateConversion(JSON.parse(output)); } catch (error) {
      if (error?.message?.startsWith('portable_model_conversion_invalid')) throw error;
      fail('portable_model_conversion_invalid');
    }
    const prepared = prepareNormalizedArtifact(modelRoot);
    const carrier = await packNormalizedArtifact(modelRoot, join(outputRoot, 'model.tar.gz'));
    const fragment = Object.freeze({
      artifact: Object.freeze({
        ...carrier,
        filename: 'model.tar.gz',
        tree_digest: prepared.tree_digest,
      }),
      conversion,
      production_ready: true,
      schema: 'pulse.portable_model_build.v1',
    });
    writeFileSync(join(outputRoot, 'portable-model-fragment.json'), `${JSON.stringify(fragment)}\n`, {
      encoding: 'utf8', flag: 'wx', mode: 0o600,
    });
    complete = true;
    return fragment;
  } finally {
    if (!complete) rmSync(outputRoot, { recursive: true, force: true });
  }
}

if (process.argv[1] && resolve(process.argv[1]) === scriptPath) {
  const result = await buildPortableModel(parseCLI(process.argv.slice(2)));
  process.stdout.write(`${JSON.stringify(result)}\n`);
}
