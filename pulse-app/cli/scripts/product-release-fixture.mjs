import { createHash, sign } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import {
  chmodSync, cpSync, existsSync, linkSync, lstatSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync,
} from 'node:fs';
import { dirname, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

import { includeRuntimePath, normalizePrivateTree } from '../src/codex-install.js';
import { canonicalReleaseJSON } from '../src/release-manifest.js';
import { buildAndInstallTargetFixture } from './target-release-fixture.mjs';
import { releaseTargetDefinition } from './release-builder-core.mjs';

const digest = (bytes) => createHash('sha256').update(bytes).digest('hex');
const scriptRoot = dirname(fileURLToPath(import.meta.url));
const cliRoot = resolve(scriptRoot, '..');
const repoRoot = resolve(cliRoot, '..', '..');
let portableFixtureGeneration = 0;

function treeManifest(files) {
  return {
    schema: 'pulse.artifact_tree.v1',
    files: files.map(([path, bytes, mode]) => ({
      path, bytes: bytes.length, sha256: digest(bytes), mode, executable: (mode & 0o111) !== 0,
    })),
  };
}

function treeManifestFromRoot(root) {
	const files = [];
	const visit = (directory, prefix = '') => {
		for (const name of readdirSync(directory).sort()) {
			const path = join(directory, name);
			const relative = prefix ? `${prefix}/${name}` : name;
			const info = lstatSync(path);
			if (info.isSymbolicLink()) throw new Error(`synthetic product edge contains a symlink: ${relative}`);
			if (info.isDirectory()) visit(path, relative);
			else if (info.isFile()) {
				const bytes = readFileSync(path);
				const mode = info.mode & 0o777;
				files.push({ path: relative, bytes: bytes.length, sha256: digest(bytes), mode, executable: (mode & 0o111) !== 0 });
			} else throw new Error(`synthetic product edge contains an unsupported entry: ${relative}`);
		}
	};
	visit(root);
	return { schema: 'pulse.artifact_tree.v1', files };
}

function pruneEmptyDirectories(root) {
	const visit = (directory, keep) => {
		for (const name of readdirSync(directory)) {
			const path = join(directory, name);
			if (lstatSync(path).isDirectory()) visit(path, false);
		}
		if (!keep && readdirSync(directory).length === 0) rmSync(directory, { recursive: true });
	};
	visit(root, true);
}

export function writeProductEdgeFixture(target, { runtimeNodeModulesRoot } = {}) {
	if (runtimeNodeModulesRoot !== undefined) {
		const info = lstatSync(runtimeNodeModulesRoot);
		if (!info.isDirectory() || info.isSymbolicLink()) {
			throw new Error('product edge runtime dependencies are invalid');
		}
	}
	mkdirSync(join(target, 'marketplace', '.agents', 'plugins'), { recursive: true, mode: 0o700 });
	mkdirSync(join(target, 'marketplace', '.claude-plugin'), { recursive: true, mode: 0o700 });
	cpSync(join(repoRoot, '.agents', 'plugins', 'marketplace.json'),
		join(target, 'marketplace', '.agents', 'plugins', 'marketplace.json'), {
			recursive: true, dereference: true,
		});
	cpSync(join(repoRoot, '.claude-plugin', 'marketplace.json'),
		join(target, 'marketplace', '.claude-plugin', 'marketplace.json'), { dereference: true });
	cpSync(join(repoRoot, 'plugins', 'pulse'), join(target, 'marketplace', 'plugins', 'pulse'), {
		recursive: true, dereference: true,
	});
	// The plugin launcher runs before it can trust or import the activated CLI
	// runtime. Keep the Windows ACL/reparse verifier beside that launcher and
	// inside the signed plugin tree so Windows never falls back to POSIX modes.
	cpSync(join(cliRoot, 'runtime', 'windows-bootstrap'),
		join(target, 'marketplace', 'plugins', 'pulse', 'native', 'windows-bootstrap'), {
			recursive: true, dereference: true,
		});
	cpSync(cliRoot, join(target, 'runtime'), {
		recursive: true, dereference: true,
		filter: (sourcePath) => {
			if (!includeRuntimePath(cliRoot, sourcePath)) return false;
			if (runtimeNodeModulesRoot === undefined) return true;
			const relative = sourcePath.slice(cliRoot.length + 1).split('\\').join('/');
			return relative !== 'node_modules' && !relative.startsWith('node_modules/');
		},
	});
	if (runtimeNodeModulesRoot !== undefined) {
		cpSync(runtimeNodeModulesRoot, join(target, 'runtime', 'node_modules'), {
			recursive: true, dereference: true,
			filter: (sourcePath) => {
				const relativePath = relative(runtimeNodeModulesRoot, sourcePath);
				if (relativePath.split(sep).includes('.bin')) return false;
				return !lstatSync(sourcePath).isSymbolicLink();
			},
		});
	}
	const mcpDist = join(repoRoot, 'mcp', 'dist');
	if (!existsSync(join(mcpDist, 'index.js'))) {
		throw new Error('synthetic product edge requires a built MCP distribution');
	}
	cpSync(mcpDist, join(target, 'runtime', 'vendor', 'pulse-mcp-dist'), {
		recursive: true, dereference: true,
		filter: (sourcePath) => !sourcePath.endsWith('.map') && !sourcePath.endsWith('.d.ts'),
	});
	pruneEmptyDirectories(target);
	normalizePrivateTree(target);
	return treeManifestFromRoot(target);
}

function cachedProductEdgeFixture(root) {
	const sourceRoot = join(root, 'product-edge-fixture');
	const manifestPath = join(root, 'product-edge-fixture-manifest.json');
	if (existsSync(sourceRoot) && existsSync(manifestPath)) {
		return { sourceRoot, manifest: JSON.parse(readFileSync(manifestPath, 'utf8')) };
	}
	rmSync(sourceRoot, { recursive: true, force: true });
	mkdirSync(sourceRoot, { recursive: true, mode: 0o700 });
	const manifest = writeProductEdgeFixture(sourceRoot);
	writeFileSync(manifestPath, `${JSON.stringify(manifest)}\n`, { mode: 0o600 });
	return { sourceRoot, manifest };
}

function tinySafetensors(salt = 0) {
  const header = Buffer.from(JSON.stringify({ embedding: { dtype: 'F32', shape: [1], data_offsets: [0, 4] } }));
  const prefix = Buffer.alloc(8);
  prefix.writeBigUInt64LE(BigInt(header.length));
  return Buffer.concat([prefix, header, Buffer.alloc(4, salt % 251)]);
}

function managedEmbedHelperFixture() {
  return Buffer.from(`#!/usr/bin/env node
const fs = require('node:fs');
const readline = require('node:readline');
const value = (name) => process.argv.includes(name) ? process.argv[process.argv.indexOf(name) + 1] : null;
const modelRoot = value('--model-root');
const support = value('--support-root') || value('--support-dir');
const model = value('--model-file') || (modelRoot && modelRoot + '/model_int8.onnx');
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

function currentTargetID() {
  const architecture = process.arch === 'arm64' ? 'arm64' : process.arch === 'x64' ? 'x64' : null;
  if (!architecture) throw new Error('synthetic product target is unsupported');
  if (process.platform === 'darwin') return `darwin-${architecture}`;
  if (process.platform === 'win32') return `win32-${architecture}`;
  if (process.platform === 'linux' && process.report?.getReport()?.header?.glibcVersionRuntime) {
    return `linux-${architecture}-gnu`;
  }
  throw new Error('synthetic product target is unsupported');
}

export async function writeSyntheticReleaseCatalogFixture(root, daemonBytes, epoch, {
  pluginRuntimeMarker,
} = {}) {
  if (!Buffer.isBuffer(daemonBytes) || daemonBytes.length < 1) {
    throw new Error('synthetic product daemon is invalid');
  }
  const targetID = currentTargetID();
  const outputRoot = join(root, 'release-catalog-fixtures', `${epoch}-${portableFixtureGeneration++}`);
  const fixture = await buildAndInstallTargetFixture({
    buildDaemon: ({ outputRoot: daemonRoot }) => {
      const executable = join(daemonRoot, 'bin', process.platform === 'win32' ? 'pulse.exe' : 'pulse');
      mkdirSync(dirname(executable), { recursive: true, mode: 0o700 });
      writeFileSync(executable, daemonBytes, { mode: 0o700 });
      chmodSync(executable, 0o700);
    },
    buildEmbedder: ({ outputRoot: embedderRoot, runnerName }) => {
      const target = releaseTargetDefinition(targetID);
      const executable = join(embedderRoot, 'bin', runnerName);
      const result = spawnSync('go', ['build', '-trimpath', '-o', executable, './cmd/pulse-fixture-embedder'], {
        cwd: join(repoRoot, 'pulse-app'),
        encoding: 'utf8',
        env: { ...process.env, CGO_ENABLED: '0', GOARCH: target.goarch, GOOS: target.goos },
        timeout: 180_000,
      });
      if (result.status !== 0) {
        throw new Error(`synthetic product embedder build failed: ${result.stderr || result.stdout || result.error?.message || result.status}`);
      }
      chmodSync(executable, 0o700);
    },
    buildPluginRuntime: ({ outputRoot: pluginRoot }) => {
      writeProductEdgeFixture(pluginRoot);
      if (pluginRuntimeMarker !== undefined) {
        if (typeof pluginRuntimeMarker !== 'string' || !/^[a-z0-9-]{1,64}$/.test(pluginRuntimeMarker)) {
          throw new Error('synthetic plugin runtime marker is invalid');
        }
        const hookPath = join(pluginRoot, 'marketplace', 'plugins', 'pulse', 'hooks', 'pulse-hook.mjs');
        writeFileSync(hookPath, Buffer.concat([
          readFileSync(hookPath), Buffer.from(`\n// fixture:${pluginRuntimeMarker}\n`),
        ]), { mode: 0o600 });
      }
    },
    epoch,
    nativeTargetID: targetID,
    now: new Date(),
    outputRoot,
    targetID,
  });
  const materializerPath = join(outputRoot, 'release-test-materializers.json');
  writeFileSync(materializerPath, `${canonicalReleaseJSON({
    artifacts: {}, schema: 'pulse.release_test_materializers.v1',
  })}\n`, { mode: 0o600 });
  return {
    assetsRoot: fixture.installer.asset_root,
    manifestPath: fixture.installer.manifest_path,
    materializerPath,
    rootPath: fixture.installer.root_key_path,
  };
}

function managedEmbedRuntimeFixture() {
  return Buffer.from(`#!/bin/sh\nexec "${process.execPath.replaceAll('"', '\\"')}" "$@"\n`);
}

export function writeSyntheticReleaseFixture(root, releaseKey, daemonBytes, epoch, {
	realInputs,
	pluginRuntimeMarker,
} = {}) {
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
		'plugin-runtime': null,
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
			if (kind === 'plugin-runtime') {
				const base = cachedProductEdgeFixture(root);
				sourceRoot = join(sourcesRoot, kind);
				cpSync(base.sourceRoot, sourceRoot, { recursive: true, dereference: true });
				if (pluginRuntimeMarker !== undefined) {
					if (typeof pluginRuntimeMarker !== 'string' || !/^[a-z0-9-]{1,64}$/.test(pluginRuntimeMarker)) {
						throw new Error('synthetic plugin runtime marker is invalid');
					}
					const hookPath = join(sourceRoot, 'marketplace', 'plugins', 'pulse', 'hooks', 'pulse-hook.mjs');
					writeFileSync(hookPath, Buffer.concat([
						readFileSync(hookPath), Buffer.from(`\n// fixture:${pluginRuntimeMarker}\n`),
					]), { mode: 0o600 });
				}
				normalizePrivateTree(sourceRoot);
				manifest = treeManifestFromRoot(sourceRoot);
			} else {
				sourceRoot = join(sourcesRoot, kind);
				mkdirSync(sourceRoot, { recursive: true, mode: 0o700 });
				for (const [path, bytes, mode] of entries) {
					const destination = join(sourceRoot, path);
					mkdirSync(dirname(destination), { recursive: true, mode: 0o700 });
					writeFileSync(destination, bytes, { mode });
					chmodSync(destination, mode);
				}
				manifest = treeManifest(entries);
			}
			const contentDigest = kind === 'model'
				? digest(entries[0][1])
				: digest(Buffer.from(canonicalReleaseJSON(manifest)));
			const carrierBytes = kind === 'model'
				? entries[0][1]
				: Buffer.from(`pulse-synthetic-carrier:${epoch}:${kind}:${contentDigest}`);
			writeFileSync(join(assetsRoot, filename), carrierBytes, { mode: 0o600 });
			carrierDigest = digest(carrierBytes);
			carrierSize = carrierBytes.length;
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
      url: `https://releases.zbs.gg/pulse/0.8.1/${filename}`, version: '0.8.1',
    };
  }
  const now = Date.now();
  const payload = {
    allowed_origins: ['https://releases.zbs.gg'], artifacts,
    release: {
      channel: 'preview', epoch,
      expires_at: new Date(now + 7 * 24 * 60 * 60 * 1000).toISOString(),
      issued_at: new Date(now - 60 * 1000).toISOString(),
      key_id: releaseKey.keyID, package: '@zbs-gg/pulse', version: '0.8.1',
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
