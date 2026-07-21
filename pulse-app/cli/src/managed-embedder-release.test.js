import assert from 'node:assert/strict';
import { chmodSync, mkdtempSync, mkdirSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  ManagedEmbedderReleaseError,
  assertSafeArchivePaths,
  buildPulseArtifactTree,
  buildRuntimeInternalManifest,
  isMachO,
  loadManagedEmbedderSourceManifest,
  managedEmbedderRuntimeContract,
} from './managed-embedder-release.js';

function expectCode(action, code) {
  assert.throws(action, (error) => error instanceof ManagedEmbedderReleaseError && error.code === code);
}

test('portable managed runtime contract is target-neutral and keeps MLX legacy-only', () => {
  assert.deepEqual(managedEmbedderRuntimeContract({ engine: 'transformers-js-onnx', platform: 'win32' }), {
    engine: 'transformers-js-onnx',
    runner_relative_path: 'bin/pulse-embedder.exe',
    vector_contract: {
      dimensions: 1024, model: 'bge-m3', normalized: true, opset: 17, pooling: 'cls',
      quantization: 'dynamic-int8',
      revision: '5617a9f61b028005a4858fdac845db406aefb181', source: 'BAAI/bge-m3',
    },
  });
  assert.deepEqual(managedEmbedderRuntimeContract({ engine: 'transformers-js-onnx', platform: 'linux' }), {
    engine: 'transformers-js-onnx',
    runner_relative_path: 'bin/pulse-embedder',
    vector_contract: {
      dimensions: 1024, model: 'bge-m3', normalized: true, opset: 17, pooling: 'cls',
      quantization: 'dynamic-int8',
      revision: '5617a9f61b028005a4858fdac845db406aefb181', source: 'BAAI/bge-m3',
    },
  });
  expectCode(() => managedEmbedderRuntimeContract({ engine: 'mlx', platform: 'darwin' }), 'managed_embedder_engine_legacy');
});

test('managed runtime sources are exact, data-only, permissive, and version pinned', () => {
  const value = loadManagedEmbedderSourceManifest();
  assert.equal(value.minimum_os, '13.5');
  assert.deepEqual(value.allowed_redirect_origins, [
    'https://cas-bridge.xethub.hf.co',
    'https://huggingface.co',
    'https://release-assets.githubusercontent.com',
  ]);
  assert.deepEqual(value.model_contract, {
    custom_code: false, data_only: true, dimensions: 1024, model: 'bge-m3', normalized: true, pooling: 'cls',
  });
  const byID = Object.fromEntries(value.components.map((item) => [item.id, item]));
  assert.equal(byID['cpython-standalone'].version, '3.12.9+20250212');
  assert.equal(byID.mlx.version, '0.29.3');
  assert.equal(byID['mlx-metal'].version, '0.29.3');
  assert.equal(byID.tokenizers.version, '0.21.1');
  assert.equal(byID['bge-m3-mlx-fp16'].runtime, false);
  assert.equal(byID['transformers-xlm-roberta-reference'].license, 'Apache-2.0');
  assert.deepEqual(value.quality_reference, {
    model: 'BAAI/bge-m3', model_revision: '5617a9f61b028005a4858fdac845db406aefb181',
    model_files: [
      { bytes: 2100674, path: 'colbert_linear.pt', sha256: '19bfbae397c2b7524158c919d0e9b19393c5639d098f0a66932c91ed8f5f9abb' },
      { bytes: 687, path: 'config.json', sha256: '26159e7ad065073448460117eb24b7a4572f6f4e78eadff65dc0a11c052449fa' },
      { bytes: 2271145830, path: 'pytorch_model.bin', sha256: 'b5e0ce3470abf5ef3831aa1bd5553b486803e83251590ab7ff35a117cf6aad38' },
      { bytes: 5069051, path: 'sentencepiece.bpe.model', sha256: 'cfc8146abe2a0488e9e2a0c56de7952f7c11ab059eca145a0a727afce0db2865' },
      { bytes: 3516, path: 'sparse_linear.pt', sha256: '45c93804d2142b8f6d7ec6914ae23a1eee9c6a1d27d83d908a20d2afb3595ad9' },
      { bytes: 964, path: 'special_tokens_map.json', sha256: '8c785abebea9ae3257b61681b4e6fd8365ceafde980c21970d001e834cf10835' },
      { bytes: 17098108, path: 'tokenizer.json', sha256: '21106b6d7dab2952c1d496fb21d5dc9db75c28ed361a05f5020bbba27810dd08' },
      { bytes: 444, path: 'tokenizer_config.json', sha256: 'a62b2b6784f990259fddef5f16388693a8043be4f69179e6a5257eeb3f9abac4' },
    ],
    package: 'FlagEmbedding', package_bytes: 247714,
    package_sha256: 'fb1856b312851591341cf4533187350e9ce43f66bbf195c66f25a73266ff7db9',
    package_url: 'https://files.pythonhosted.org/packages/73/70/3593ccb1299f8440369fd3c542ef2f5c09718eb8c7628614303e65dd38f8/flagembedding-1.4.0-py3-none-any.whl',
    package_version: '1.4.0',
  });
  const source = readFileSync(new URL('../runtime/embedder/pulse_embedder/xlm_roberta.py', import.meta.url), 'utf8');
  assert.doesNotMatch(source, /mlx_embeddings|mean_pool/i);
  assert.match(readFileSync(new URL('../runtime/embedder/helper.py', import.meta.url), 'utf8'), /hidden\[:, 0, :\]/);
});

test('managed runtime sources reject a substituted official reference snapshot', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-embed-sources-'));
  try {
    const value = JSON.parse(readFileSync(new URL('../runtime/embedder/source-manifest.json', import.meta.url), 'utf8'));
    value.quality_reference.model_files[2].sha256 = '0'.repeat(64);
    const path = join(root, 'source-manifest.json');
    writeFileSync(path, JSON.stringify(value), { mode: 0o600 });
    expectCode(() => loadManagedEmbedderSourceManifest(path), 'quality_reference_invalid');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('managed runtime sources reject an unsafe redirect origin', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-embed-redirects-'));
  try {
    const value = JSON.parse(readFileSync(new URL('../runtime/embedder/source-manifest.json', import.meta.url), 'utf8'));
    value.allowed_redirect_origins[0] = 'http://cas-bridge.xethub.hf.co';
    const path = join(root, 'source-manifest.json');
    writeFileSync(path, JSON.stringify(value), { mode: 0o600 });
    expectCode(() => loadManagedEmbedderSourceManifest(path), 'source_redirect_origins_invalid');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('deterministic fixture encodes CLS then L2 and explicitly disclaims quality', () => {
  const fixture = JSON.parse(readFileSync(new URL('../runtime/embedder/fixture-contract.json', import.meta.url), 'utf8'));
  const norm = Math.hypot(...fixture.case.cls_prefix);
  assert.deepEqual(fixture.case.cls_prefix.map((value) => value / norm), fixture.case.expected_prefix);
  assert.match(fixture.meaning, /not a model quality result/i);
  const gate = readFileSync(new URL('../runtime/embedder/quality_gate.py', import.meta.url), 'utf8');
  assert.match(gate, /FlagEmbedding import BGEM3FlagModel/);
  assert.match(gate, /"quality_claimed": False/);
  assert.match(gate, /"ndcg_at_10_delta_max": 0\.005/);
  assert.match(gate, /"minimum_cosine": 0\.999/);
  assert.match(gate, /"peak_rss_bytes_max": 2_500_000_000/);
  assert.match(gate, /DOCUMENTS =/);
  assert.match(gate, /QUERIES =/);
});

test('internal manifest is an exact regular-file tree with Mach-O inventory', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-embed-runtime-'));
  try {
    mkdirSync(join(root, 'runtime'), { mode: 0o755 });
    writeFileSync(join(root, 'runtime', 'plain.txt'), 'ok', { mode: 0o644 });
    writeFileSync(join(root, 'runtime', 'native'), Buffer.from('feedfacf00000000', 'hex'), { mode: 0o755 });
    const value = buildRuntimeInternalManifest(root);
    assert.match(value.manifest.tree_digest, /^[a-f0-9]{64}$/);
    assert.equal(value.machOPaths.length, 1);
    assert.equal(value.manifest.entries.find((item) => item.path === 'runtime/native').mach_o, true);
    assert.equal(isMachO(Buffer.from('feedfacf', 'hex')), true);
    assert.equal(isMachO(Buffer.from('notmacho')), false);
    assert.throws(() => buildPulseArtifactTree(root), /artifact_tree_mode_not_normalized/);
    chmodSync(join(root, 'runtime', 'plain.txt'), 0o600);
    chmodSync(join(root, 'runtime', 'native'), 0o700);
    writeFileSync(join(root, 'QUALITY.json'), '{"quality_claimed":true}\n', { mode: 0o600 });
    const carrierTree = buildPulseArtifactTree(root);
    assert.equal(carrierTree.schema, 'pulse.artifact_tree.v1');
    assert.equal(carrierTree.files.some((entry) => entry.path === 'runtime/native' && entry.mode === 0o700 && entry.executable), true);
    assert.equal(carrierTree.files.some((entry) => entry.path === 'runtime/plain.txt' && entry.mode === 0o600 && !entry.executable), true);
    assert.equal(carrierTree.files.some((entry) => entry.path === 'QUALITY.json' && /^[a-f0-9]{64}$/.test(entry.sha256)), true);
    symlinkSync(join(root, 'runtime', 'plain.txt'), join(root, 'runtime', 'link'));
    expectCode(() => buildRuntimeInternalManifest(root), 'runtime_special_file_forbidden');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('archive path contract rejects traversal and platform junk', () => {
  assert.equal(assertSafeArchivePaths(['python/bin/python3.12', 'python/LICENSE']), true);
  for (const value of [['../escape'], ['/absolute'], ['a/./b'], ['a//b'], ['a\\b'], ['.DS_Store']]) {
    expectCode(() => assertSafeArchivePaths(value), 'archive_path_invalid');
  }
});

test('release builder contains sign-all, DMG, and production notarization gates', () => {
  const builder = readFileSync(new URL('../scripts/build-embedder-runtime.mjs', import.meta.url), 'utf8');
  assert.match(builder, /buildRuntimeInternalManifest/);
  assert.match(builder, /buildPulseArtifactTree/);
  assert.match(builder, /pulse-artifact-tree\.json/);
  assert.match(builder, /for \(const path of machOPaths\)/);
  assert.match(builder, /hdiutil/);
  assert.match(builder, /PULSE_PRODUCTION_RELEASE/);
  assert.match(builder, /notarytool[^\n]+submit/);
  assert.match(builder, /stapler[^\n]+staple/);
  assert.match(builder, /stapler[^\n]+validate/);
  assert.match(builder, /--identifier[^\n]+carrierIdentifier/);
  assert.match(builder, /pendingCarrier/);
  assert.match(builder, /acquireInstallLock/);
  assert.match(builder, /renameSync\(pendingRoot, publishedRoot\)/);
  assert.ok(builder.indexOf('rmSync(publishedRoot, { recursive: true, force: true })') <
    builder.indexOf('managed embedder runtime can only be built on Apple Silicon macOS'));
  assert.match(builder, /PULSE_BGE_M3_REFERENCE_MODEL/);
  assert.match(builder, /flagembedding-reference/);
  assert.match(builder, /copyPinnedReferenceFile/);
  assert.match(builder, /quality_claimed !== true/);
  assert.match(builder, /QUALITY\.json/);
  assert.match(builder, /readSync\(descriptor/);
  assert.doesNotMatch(builder, /digestFile[\s\S]{0,200}readFileSync/);
});
