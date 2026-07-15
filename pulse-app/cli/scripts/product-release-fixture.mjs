import { createHash, sign } from 'node:crypto';
import {
  chmodSync, linkSync, mkdirSync, rmSync, writeFileSync,
} from 'node:fs';
import { dirname, join } from 'node:path';

import { canonicalReleaseJSON } from '../src/release-manifest.js';

const digest = (bytes) => createHash('sha256').update(bytes).digest('hex');

function treeManifest(files) {
  return {
    schema: 'pulse.artifact_tree.v1',
    files: files.map(([path, bytes, mode]) => ({
      path, bytes: bytes.length, sha256: digest(bytes), mode, executable: (mode & 0o111) !== 0,
    })),
  };
}

function tinySafetensors(salt = 0) {
  const header = Buffer.from(JSON.stringify({ embedding: { dtype: 'F32', shape: [1], data_offsets: [0, 4] } }));
  const prefix = Buffer.alloc(8);
  prefix.writeBigUInt64LE(BigInt(header.length));
  return Buffer.concat([prefix, header, Buffer.alloc(4, salt % 251)]);
}

function managedEmbedHelperFixture() {
  return Buffer.from(`
const fs = require('node:fs');
const readline = require('node:readline');
const model = process.argv[process.argv.indexOf('--model-file') + 1];
const support = process.argv[process.argv.indexOf('--support-dir') + 1];
fs.readFileSync(model); fs.readFileSync(support + '/config.json'); fs.readFileSync(support + '/tokenizer.json');
process.stdout.write(JSON.stringify({dimensions:1024,id:'__startup__',model:'bge-m3',normalized:true,ok:true,pooling:'cls',protocol:1,schema:'pulse.embedder.ready.v1'})+'\\n');
const lines = readline.createInterface({ input: process.stdin });
(async () => { for await (const line of lines) {
  const request = JSON.parse(line);
  if (request.schema !== 'pulse.embedder.request.v1' || !Array.isArray(request.texts)) process.exit(2);
  const embeddings = request.texts.map((text) => {
    const vector = Array(1024).fill(0);
    let hash = 2166136261;
    for (const char of Buffer.from(text)) { hash ^= char; hash = Math.imul(hash, 16777619); }
    vector[0] = 0.8; vector[1 + ((hash >>> 0) % 1023)] = 0.6;
    return vector;
  });
  process.stdout.write(JSON.stringify({embeddings,id:request.id,schema:'pulse.embedder.response.v1'})+'\\n');
} })().catch(() => process.exit(3));
`);
}

function managedEmbedRuntimeFixture() {
  return Buffer.from(`#!/bin/sh\nexec "${process.execPath.replaceAll('"', '\\"')}" "$@"\n`);
}

export function writeSyntheticReleaseFixture(root, releaseKey, daemonBytes, epoch, { realInputs } = {}) {
  const assetsRoot = join(root, 'release-assets');
  const sourcesRoot = join(root, 'release-materialized');
  const manifestPath = join(root, 'personal-preview-manifest.json');
  const materializerPath = join(root, 'release-test-materializers.json');
  rmSync(assetsRoot, { recursive: true, force: true });
  rmSync(sourcesRoot, { recursive: true, force: true });
  mkdirSync(assetsRoot, { recursive: true, mode: 0o700 });
  mkdirSync(sourcesRoot, { recursive: true, mode: 0o700 });
  const files = {
    daemon: [['bin/pulse', daemonBytes, 0o700]],
    'embedder-runtime': [
      ['runtime/bin/python3.12', managedEmbedRuntimeFixture(), 0o700],
      ['helper.py', managedEmbedHelperFixture(), 0o600],
      ['support/config.json', Buffer.from('{}\n'), 0o600],
      ['support/tokenizer.json', Buffer.from('{}\n'), 0o600],
    ],
    model: [['model.safetensors', tinySafetensors(epoch), 0o600]],
    'plugin-runtime': [['runtime/index.js', Buffer.from(`export const releaseEpoch = ${epoch};\n`), 0o600]],
    'presence-helper': [['bin/gg.zbs.pulse.presence-helper', Buffer.from(`#!/bin/sh\n# epoch ${epoch}\nexit 0\n`), 0o700]],
  };
  const artifacts = {};
  const materializers = {};
  for (const [kind, entries] of Object.entries(files)) {
    const executable = ['daemon', 'embedder-runtime', 'presence-helper'].includes(kind);
    const format = executable ? 'dmg' : kind === 'model' ? 'safetensors' : 'tar.gz';
    const filename = `${kind}.${format}`;
    let carrierDigest;
    let carrierSize;
    let sourceRoot;
    let manifest;
    if (realInputs && kind === 'embedder-runtime') {
      linkSync(realInputs.runtimePath, join(assetsRoot, filename));
      carrierDigest = realInputs.runtimeDigest.sha256;
      carrierSize = realInputs.runtimeDigest.bytes;
      // Deliberately omit a fixture materializer: the packed real-MLX gate
      // must select the production DMG verifier/materializer itself.
    } else if (realInputs && kind === 'model') {
      linkSync(realInputs.modelPath, join(assetsRoot, filename));
      carrierDigest = realInputs.modelDigest.sha256;
      carrierSize = realInputs.modelDigest.bytes;
    } else {
      const carrierBytes = kind === 'model'
        ? entries[0][1]
        : Buffer.from(`pulse-synthetic-carrier:${epoch}:${kind}:${digest(Buffer.concat(entries.map((entry) => entry[1])))}`);
      writeFileSync(join(assetsRoot, filename), carrierBytes, { mode: 0o600 });
      carrierDigest = digest(carrierBytes);
      carrierSize = carrierBytes.length;
      sourceRoot = join(sourcesRoot, kind);
      mkdirSync(sourceRoot, { recursive: true, mode: 0o700 });
      for (const [path, bytes, mode] of entries) {
        const destination = join(sourceRoot, path);
        mkdirSync(dirname(destination), { recursive: true, mode: 0o700 });
        writeFileSync(destination, bytes, { mode });
        chmodSync(destination, mode);
      }
      manifest = treeManifest(entries);
      materializers[kind] = { source_root: sourceRoot, tree_manifest: manifest };
    }
    const signedDescriptor = realInputs && kind === 'embedder-runtime'
      ? realInputs.runtimeDescriptor
      : realInputs && kind === 'model' ? realInputs.modelDescriptor : null;
    artifacts[kind] = {
      architecture: 'arm64', bytes: carrierSize, epoch, executable, format,
      id: `pulse-${kind}`, kind, minimum_os: '13.0',
      model_policy: kind === 'model' ? { custom_code: false, data_only: true } : null,
      origin: 'https://releases.zbs.gg', platform: 'darwin', sha256: carrierDigest,
      signing: signedDescriptor?.signing ?? (executable ? {
        gatekeeper: true, identifier: `gg.zbs.pulse.${kind}`, notarized: true,
        scheme: 'apple-developer-id', stapled: true, team_id: '44N4NZ86S5',
      } : {
        gatekeeper: false, identifier: null, notarized: false,
        scheme: 'release-manifest', stapled: false, team_id: null,
      }),
      url: `https://releases.zbs.gg/pulse/0.7.0/${filename}`, version: '0.7.0',
    };
  }
  const now = Date.now();
  const payload = {
    allowed_origins: ['https://releases.zbs.gg'], artifacts,
    release: {
      channel: 'preview', epoch,
      expires_at: new Date(now + 7 * 24 * 60 * 60 * 1000).toISOString(),
      issued_at: new Date(now - 60 * 1000).toISOString(),
      key_id: releaseKey.keyID, package: '@zbs-gg/pulse', version: '0.7.0',
    },
    schema: 'pulse.personal_preview.release_manifest.v1',
  };
  const envelope = {
    payload, schema: 'pulse.release_envelope.v1',
    signature: {
      algorithm: 'ed25519', key_id: releaseKey.keyID,
      value: sign(null, Buffer.from(canonicalReleaseJSON(payload)), releaseKey.privateKey).toString('base64'),
    },
  };
  writeFileSync(manifestPath, `${canonicalReleaseJSON(envelope)}\n`, { mode: 0o600 });
  writeFileSync(materializerPath, `${canonicalReleaseJSON({
    artifacts: materializers, schema: 'pulse.release_test_materializers.v1',
  })}\n`, { mode: 0o600 });
  return { assetsRoot, manifestPath, materializerPath };
}
