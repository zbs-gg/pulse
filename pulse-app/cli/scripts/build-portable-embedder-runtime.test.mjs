import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { Readable, Writable } from 'node:stream';
import test from 'node:test';

import { buildPortableEmbedderRuntime } from './build-portable-embedder-runtime.mjs';
import {
  LOCKED_VECTOR_CONTRACT,
  loadProductionBackend,
  runJSONLineProtocol,
} from '../runtime/embedder-portable/runner.mjs';

const portableRoot = resolve(import.meta.dirname, '..', 'runtime', 'embedder-portable');

function syntheticBackend() {
  return {
    async embed(texts) {
      return texts.map(() => [1, ...Array(1023).fill(0)]);
    },
  };
}

test('target runtime manifest pins the exact portable inference dependencies', () => {
  const manifest = JSON.parse(readFileSync(join(portableRoot, 'target-runtime', 'package.json'), 'utf8'));
  assert.deepEqual(manifest.dependencies, {
    '@huggingface/transformers': '4.2.0',
    'onnxruntime-node': '1.24.3',
  });
  assert.equal(manifest.private, true);
});

test('synthetic fixture proves the stable bounded JSON-line protocol only', async () => {
  const output = [];
  const sink = new Writable({
    write(chunk, _encoding, callback) { output.push(Buffer.from(chunk)); callback(); },
  });
  await runJSONLineProtocol({
    backend: syntheticBackend(),
    input: Readable.from([
      `${JSON.stringify({ id: 'r1', schema: 'pulse.embedder.request.v1', texts: ['hello', 'привет'] })}\n`,
    ]),
    output: sink,
  });
  const lines = Buffer.concat(output).toString('utf8').trim().split('\n').map(JSON.parse);
  assert.deepEqual(lines[0], {
    dimensions: 1024, id: '__startup__', model: 'bge-m3', normalized: true,
    ok: true, pooling: 'cls', protocol: 1, schema: 'pulse.embedder.ready.v1',
  });
  assert.equal(lines[1].schema, 'pulse.embedder.response.v1');
  assert.equal(lines[1].id, 'r1');
  assert.equal(lines[1].embeddings.length, 2);
  assert.equal(lines[1].embeddings[0].length, 1024);
});

test('synthetic model evidence cannot load as a production backend', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-portable-synthetic.'));
  const support = join(root, 'support');
  mkdirSync(support, { recursive: true });
  writeFileSync(join(root, 'model_int8.onnx'), 'synthetic');
  for (const name of ['config.json', 'tokenizer.json', 'tokenizer_config.json', 'special_tokens_map.json']) {
    writeFileSync(join(support, name), '{}\n');
  }
  writeFileSync(join(root, 'pulse-model-contract.json'), `${JSON.stringify({
    engine: 'transformers-js-onnx', model_file: 'model_int8.onnx',
    quality: { kind: 'synthetic', evidence_digest: '0'.repeat(64) },
    schema: 'pulse.portable_embedder.model.v1',
    support_files: ['config.json', 'special_tokens_map.json', 'tokenizer.json', 'tokenizer_config.json'],
    vector_contract: LOCKED_VECTOR_CONTRACT,
  })}\n`);
  await assert.rejects(
    loadProductionBackend({ modelRoot: root, supportRoot: support }),
    /production quality evidence is required/,
  );
  rmSync(root, { recursive: true, force: true });
});

test('fixture build has exact runner layout and is explicitly never production-ready', () => {
  const output = mkdtempSync(join(tmpdir(), 'pulse-portable-build.'));
  try {
    const result = buildPortableEmbedderRuntime({
      fixture: true,
      outputRoot: output,
      platform: process.platform,
      sourceRoot: portableRoot,
    });
    const runnerName = process.platform === 'win32' ? 'pulse-embedder.exe' : 'pulse-embedder';
    assert.equal(result.runner_relative_path, `bin/${runnerName}`);
    const evidence = JSON.parse(readFileSync(join(output, 'pulse-portable-embedder-build.json'), 'utf8'));
    assert.equal(evidence.fixture, true);
    assert.equal(evidence.production_ready, false);
    assert.equal(evidence.quality_gate, 'synthetic-protocol-only');
  } finally {
    rmSync(output, { recursive: true, force: true });
  }
});
