#!/usr/bin/env node

import { once } from 'node:events';
import { lstatSync, readFileSync, realpathSync } from 'node:fs';
import { isAbsolute, join, relative, resolve, sep } from 'node:path';
import { pathToFileURL } from 'node:url';

const MAX_BATCH = 96;
const MAX_REQUEST_BYTES = 4 * 1024 * 1024;
const MAX_RESPONSE_BYTES = 8 * 1024 * 1024;
const MAX_TEXT_BYTES = 32 * 1024;
const REQUIRED_SUPPORT = Object.freeze([
  'config.json', 'special_tokens_map.json', 'tokenizer.json', 'tokenizer_config.json',
]);
const SHA256 = /^[a-f0-9]{64}$/;

export const LOCKED_VECTOR_CONTRACT = Object.freeze({
  dimensions: 1024,
  model: 'bge-m3',
  normalized: true,
  opset: 17,
  pooling: 'cls',
  quantization: 'dynamic-int8',
  revision: '5617a9f61b028005a4858fdac845db406aefb181',
  source: 'BAAI/bge-m3',
});

function exactKeys(value, keys) {
  return value !== null && !Array.isArray(value) && typeof value === 'object' &&
    Object.keys(value).sort().join('\0') === [...keys].sort().join('\0');
}

function canonical(value) {
  if (value === null || typeof value === 'string' || typeof value === 'boolean') return JSON.stringify(value);
  if (Number.isSafeInteger(value)) return String(value);
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`;
  if (value && typeof value === 'object') {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}`;
  }
  throw new Error('portable embedder contract contains an unsupported value');
}

function regularFile(path, maximum = Number.MAX_SAFE_INTEGER) {
  const stat = lstatSync(path);
  if (!stat.isFile() || stat.isSymbolicLink() || stat.size < 1 || stat.size > maximum) {
    throw new Error(`portable embedder file is invalid: ${path}`);
  }
  return path;
}

function strictDirectory(path) {
  const stat = lstatSync(path);
  if (!stat.isDirectory() || stat.isSymbolicLink()) throw new Error(`portable embedder directory is invalid: ${path}`);
  return realpathSync(path);
}

function contained(root, child) {
  const value = relative(root, child);
  return value !== '' && value !== '..' && !value.startsWith(`..${sep}`) && !isAbsolute(value);
}

function validateProductionContract(modelRoot, supportRoot) {
  const path = regularFile(join(modelRoot, 'pulse-model-contract.json'), 32 * 1024);
  let contract;
  try { contract = JSON.parse(readFileSync(path, 'utf8')); } catch { throw new Error('portable model contract is invalid JSON'); }
  if (!exactKeys(contract, ['engine', 'model_file', 'quality', 'schema', 'support_files', 'vector_contract']) ||
      contract.schema !== 'pulse.portable_embedder.model.v1' ||
      contract.engine !== 'transformers-js-onnx' || contract.model_file !== 'model_int8.onnx' ||
      canonical(contract.vector_contract) !== canonical(LOCKED_VECTOR_CONTRACT) ||
      canonical(contract.support_files) !== canonical(REQUIRED_SUPPORT)) {
    throw new Error('portable model contract mismatch');
  }
  if (!exactKeys(contract.quality, ['evidence_digest', 'kind']) || contract.quality.kind !== 'production' ||
      !SHA256.test(contract.quality.evidence_digest) || contract.quality.evidence_digest === '0'.repeat(64)) {
    throw new Error('production quality evidence is required');
  }
  regularFile(join(modelRoot, contract.model_file));
  for (const name of REQUIRED_SUPPORT) regularFile(join(supportRoot, name), name === 'tokenizer.json' ? 64 * 1024 * 1024 : 4 * 1024 * 1024);
  return contract;
}

function validateVectors(vectors, count) {
  if (!Array.isArray(vectors) || vectors.length !== count) throw new Error('portable embedder vector count mismatch');
  for (const vector of vectors) {
    if (!Array.isArray(vector) || vector.length !== LOCKED_VECTOR_CONTRACT.dimensions) {
      throw new Error('portable embedder vector dimensions mismatch');
    }
    let normSquared = 0;
    for (const value of vector) {
      if (typeof value !== 'number' || !Number.isFinite(value)) throw new Error('portable embedder vector value is invalid');
      normSquared += value * value;
    }
    if (Math.abs(Math.sqrt(normSquared) - 1) > 0.005) throw new Error('portable embedder vector is not normalized');
  }
  return vectors;
}

export async function loadProductionBackend({ modelRoot, supportRoot }) {
  if (!isAbsolute(modelRoot) || !isAbsolute(supportRoot) || resolve(modelRoot) !== modelRoot || resolve(supportRoot) !== supportRoot) {
    throw new Error('portable embedder roots must be absolute and clean');
  }
  const verifiedModelRoot = strictDirectory(modelRoot);
  const verifiedSupportRoot = strictDirectory(supportRoot);
  if (!contained(verifiedModelRoot, verifiedSupportRoot)) throw new Error('portable support root must be inside model root');
  validateProductionContract(verifiedModelRoot, verifiedSupportRoot);

  // Both library policy and per-load options forbid remote resolution. The
  // fetch trap makes an accidental future fallback fail closed as well.
  const transformers = await import('@huggingface/transformers');
  const ort = await import('onnxruntime-node');
  transformers.env.allowLocalModels = true;
  transformers.env.allowRemoteModels = false;
  transformers.env.localModelPath = verifiedSupportRoot;
  transformers.env.useFSCache = false;
  transformers.env.useCustomCache = false;
  transformers.env.fetch = async () => { throw new Error('portable embedder network access is disabled'); };

  const tokenizer = await transformers.AutoTokenizer.from_pretrained(verifiedSupportRoot, {
    local_files_only: true,
  });
  const session = await ort.InferenceSession.create(join(verifiedModelRoot, 'model_int8.onnx'), {
    executionProviders: ['cpu'],
    graphOptimizationLevel: 'all',
  });
  if (!session.inputNames.includes('input_ids') || !session.inputNames.includes('attention_mask') ||
      !session.outputNames.includes('last_hidden_state') ||
      session.inputNames.some((name) => !['input_ids', 'attention_mask', 'token_type_ids'].includes(name))) {
    throw new Error('portable ONNX input/output contract mismatch');
  }

  return {
    async embed(texts) {
      const tokenized = await tokenizer(texts, { padding: true, truncation: true, max_length: 8192 });
      const feeds = {};
      for (const name of session.inputNames) {
        const tensor = tokenized[name];
        if (!tensor || !Array.isArray(tensor.dims) || tensor.dims.length !== 2) {
          throw new Error(`portable tokenizer output is missing ${name}`);
        }
        feeds[name] = new ort.Tensor(tensor.type ?? 'int64', tensor.data, tensor.dims);
      }
      const outputs = await session.run(feeds);
      const hidden = outputs.last_hidden_state;
      if (!hidden || hidden.dims.length !== 3 || hidden.dims[0] !== texts.length ||
          hidden.dims[2] !== LOCKED_VECTOR_CONTRACT.dimensions) {
        throw new Error('portable ONNX output shape mismatch');
      }
      const [, sequence, dimensions] = hidden.dims;
      const vectors = [];
      for (let batch = 0; batch < texts.length; batch += 1) {
        const offset = batch * sequence * dimensions;
        const vector = Array.from(hidden.data.subarray(offset, offset + dimensions), Number);
        const norm = Math.sqrt(vector.reduce((sum, value) => sum + value * value, 0));
        if (!Number.isFinite(norm) || norm <= 1e-12) throw new Error('portable ONNX CLS vector is invalid');
        vectors.push(vector.map((value) => value / norm));
      }
      return validateVectors(vectors, texts.length);
    },
  };
}

function validateRequest(value) {
  if (!exactKeys(value, ['id', 'schema', 'texts']) || value.schema !== 'pulse.embedder.request.v1' ||
      typeof value.id !== 'string' || !/^r[1-9][0-9]{0,19}$/.test(value.id) ||
      !Array.isArray(value.texts) || value.texts.length < 1 || value.texts.length > MAX_BATCH) {
    throw new Error('portable embedder request contract mismatch');
  }
  for (const text of value.texts) {
    const bytes = typeof text === 'string' ? Buffer.byteLength(text, 'utf8') : 0;
    if (bytes < 1 || bytes > MAX_TEXT_BYTES) throw new Error('portable embedder request text is invalid');
  }
  return value;
}

async function writeBounded(output, value, maximum) {
  const line = `${JSON.stringify(value)}\n`;
  if (Buffer.byteLength(line) > maximum) throw new Error('portable embedder response exceeds protocol limit');
  if (!output.write(line)) await once(output, 'drain');
}

async function* boundedLines(input) {
  let pending = Buffer.alloc(0);
  for await (const chunk of input) {
    pending = Buffer.concat([pending, Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)]);
    if (pending.length > MAX_REQUEST_BYTES && pending.indexOf(0x0a) === -1) {
      throw new Error('portable embedder request exceeds protocol limit');
    }
    let newline;
    while ((newline = pending.indexOf(0x0a)) !== -1) {
      const line = pending.subarray(0, newline);
      pending = pending.subarray(newline + 1);
      if (line.length < 1 || line.length + 1 > MAX_REQUEST_BYTES) throw new Error('portable embedder request line is invalid');
      yield line;
    }
    if (pending.length > MAX_REQUEST_BYTES) throw new Error('portable embedder request exceeds protocol limit');
  }
  if (pending.length !== 0) throw new Error('portable embedder request must end with newline');
}

export async function runJSONLineProtocol({ backend, input = process.stdin, output = process.stdout }) {
  await writeBounded(output, {
    dimensions: 1024, id: '__startup__', model: 'bge-m3', normalized: true,
    ok: true, pooling: 'cls', protocol: 1, schema: 'pulse.embedder.ready.v1',
  }, 4096);
  for await (const line of boundedLines(input)) {
    let request;
    try { request = validateRequest(JSON.parse(line.toString('utf8'))); } catch { throw new Error('portable embedder request is invalid'); }
    const embeddings = validateVectors(await backend.embed(request.texts), request.texts.length);
    await writeBounded(output, {
      embeddings, id: request.id, schema: 'pulse.embedder.response.v1',
    }, MAX_RESPONSE_BYTES);
  }
}

function parseArguments(argv) {
  if (argv.length !== 4 || argv[0] !== '--model-root' || argv[2] !== '--support-root') {
    throw new Error('usage: pulse-embedder --model-root ABSOLUTE --support-root ABSOLUTE');
  }
  return { modelRoot: argv[1], supportRoot: argv[3] };
}

async function main() {
  const backend = await loadProductionBackend(parseArguments(process.argv.slice(2)));
  await runJSONLineProtocol({ backend });
}

const invokedPath = process.argv[1] ? pathToFileURL(resolve(process.argv[1])).href : '';
if (import.meta.url === invokedPath) {
  main().catch((error) => {
    process.stderr.write(`pulse-embedder: ${error instanceof Error ? error.message : 'fatal error'}\n`);
    process.exitCode = 1;
  });
}
