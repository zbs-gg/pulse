import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash, generateKeyPairSync, sign } from 'node:crypto';
import {
  chmodSync,
	  closeSync,
	  copyFileSync,
	  existsSync,
	  fstatSync,
	  lstatSync,
	  mkdirSync,
  mkdtempSync,
	  openSync,
	  readFileSync,
	  readSync,
	  realpathSync,
  readdirSync,
	  rmSync,
	  symlinkSync,
	  writeFileSync,
} from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { dirname, isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  bindingRegistryAnchor, canonicalJSONStringify, canonicalizeWorkspace,
} from '../src/workspace-binding.js';
import { materializeVerifiedDmg, readCommittedArtifactSet } from '../src/artifact-installer.js';
import {
	inspectCodexMarketplaceSnapshot,
	parseCodexMarketplaceList,
	resolveSignedCodexProductEdge,
} from '../src/codex-install.js';
import { auditPublicPackageRoot } from './public-package-audit.mjs';
import {
	canonicalReleaseJSON, pinnedReleaseKeyring, releaseKeyID, verifyReleaseManifestEnvelope,
} from '../src/release-manifest.js';
import {
	writeSyntheticReleaseCatalogFixture, writeSyntheticReleaseFixture,
} from './product-release-fixture.mjs';

const scriptDir = dirname(fileURLToPath(import.meta.url));
const cliRoot = resolve(scriptDir, '..');
const repoRoot = resolve(cliRoot, '..', '..');
const pulseAppRoot = resolve(cliRoot, '..');
const requireRealMLX = process.env.PULSE_CODEX_E2E_REQUIRE_REAL_MLX === '1';
const realEmbedderDmg = process.env.PULSE_CODEX_E2E_REAL_EMBEDDER_DMG;
const realModelFile = process.env.PULSE_CODEX_E2E_REAL_MODEL;
const realReleaseManifest = process.env.PULSE_CODEX_E2E_REAL_RELEASE_MANIFEST;
const realReleaseRoot = process.env.PULSE_CODEX_E2E_REAL_RELEASE_ROOT;

if (requireRealMLX && (!realEmbedderDmg || !realModelFile || !realReleaseManifest || !realReleaseRoot)) {
	throw new Error('release real-MLX E2E requires the runtime DMG, model, signed release manifest, and pinned release root');
}

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: options.cwd,
    env: options.env ?? process.env,
    input: options.input,
    encoding: 'utf8',
    timeout: options.timeout ?? 60_000,
    stdio: ['pipe', 'pipe', 'pipe'],
  });
  if (result.status !== (options.status ?? 0)) {
    throw new Error([
      `${command} ${args.join(' ')} exited ${result.status}`,
      result.stdout,
      result.stderr,
    ].filter(Boolean).join('\n'));
  }
  return result;
}

async function freePort() {
  const server = createServer();
  await new Promise((resolveListen, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolveListen);
  });
  const { port } = server.address();
  await new Promise((resolveClose) => server.close(resolveClose));
  return port;
}

function personalPrincipalID(suffix) {
  return `principal_${createHash('sha256').update(`pulse-codex-e2e:${suffix}`).digest('hex').slice(0, 32)}`;
}

function writeSignedPersonalBindings(root, fixtures) {
  const supervisor = join(root, 'trust');
  mkdirSync(supervisor, { recursive: true, mode: 0o700 });
  const registryPath = join(supervisor, 'workspace-bindings.json');
  const publicKeyPath = join(supervisor, 'workspace-bindings.pub.pem');
  const anchorPath = join(supervisor, 'workspace-bindings.anchor.json');
  const payload = {
    schema: 'pulse.workspace-binding-registry.v1',
    epoch: 1,
		bindings: fixtures.map(({ workspace, port, suffix }) => ({
			binding_id: `binding_codex_e2e_${suffix}`,
			receipt_id: `receipt_codex_e2e_${suffix}`,
      resolver_epoch: 1,
      workspace: {
        workspace_id: workspace.workspace_id,
        repository_id: workspace.repository_id,
      },
      mode: 'personal',
			principal_ref: personalPrincipalID(suffix),
			personal: {
				store_id: `store_personal_codex_e2e_${suffix}`,
				data_dir: join(root, 'vaults', `personal-${suffix}`),
				base_url: `http://127.0.0.1:${port}`,
				credential_ref: `local:pulse/codex-e2e-${suffix}`,
				cache_dir: join(root, 'caches', `personal-${suffix}`),
			},
		})),
  };
  const { privateKey, publicKey } = generateKeyPairSync('ed25519');
  const signature = sign(null, Buffer.from(canonicalJSONStringify(payload)), privateKey).toString('base64');
  const registryBytes = Buffer.from(JSON.stringify({ algorithm: 'ed25519', payload, signature }));
  writeFileSync(registryPath, registryBytes, { mode: 0o600 });
  writeFileSync(publicKeyPath, publicKey.export({ type: 'spki', format: 'pem' }), { mode: 0o600 });
  writeFileSync(anchorPath, `${canonicalJSONStringify(bindingRegistryAnchor(registryBytes, payload.epoch))}\n`, { mode: 0o600 });
  chmodSync(registryPath, 0o600);
  chmodSync(publicKeyPath, 0o600);
  return { registryPath, publicKeyPath, anchorPath };
}

function initializeRepository(path) {
  mkdirSync(path, { recursive: true });
  run('/usr/bin/git', ['init', '-q'], { cwd: path });
  run('/usr/bin/git', ['config', 'user.email', 'pulse-e2e@example.test'], { cwd: path });
  run('/usr/bin/git', ['config', 'user.name', 'Pulse E2E'], { cwd: path });
  run('/usr/bin/git', ['commit', '--allow-empty', '-q', '-m', 'fixture'], { cwd: path });
}

function packedTarball(root) {
	const supplied = process.env.PULSE_PERSONAL_PACKED_TARBALL;
	if (supplied !== undefined) {
		if (!isAbsolute(supplied) || resolve(supplied) !== supplied) {
			throw new Error('PULSE_PERSONAL_PACKED_TARBALL must be an absolute canonical path');
		}
		const info = lstatSync(supplied);
		if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1) {
			throw new Error('PULSE_PERSONAL_PACKED_TARBALL must be one regular, non-linked file');
		}
		return supplied;
	}
	const npmArgs = ['npm', 'pack', '--json', '--pack-destination', root];
	const [command, args] = process.platform === 'darwin'
		? ['/usr/bin/lockf', ['-k', '-t', '300', '/tmp/pulse-product-pack.lock', ...npmArgs]]
		: ['/usr/bin/flock', ['-w', '300', '/tmp/pulse-product-pack.lock', ...npmArgs]];
	run(command, args, {
		cwd: cliRoot, timeout: 330_000,
		env: { ...process.env, PULSE_ALLOW_UNNOTARIZED_INTERNAL_PREVIEW: '1' },
	});
  const tarballs = readdirSync(root).filter((name) => name.endsWith('.tgz'));
  assert.equal(tarballs.length, 1);
  return join(root, tarballs[0]);
}

function digestFile(path) {
	const descriptor = openSync(path, 'r');
	try {
		const stat = fstatSync(descriptor);
		const hash = createHash('sha256');
		const buffer = Buffer.allocUnsafe(1024 * 1024);
		let offset = 0;
		while (offset < stat.size) {
			const count = readSync(descriptor, buffer, 0, Math.min(buffer.length, stat.size - offset), offset);
			if (count < 1) throw new Error(`short read while hashing ${path}`);
			hash.update(buffer.subarray(0, count));
			offset += count;
		}
		return { bytes: stat.size, sha256: hash.digest('hex') };
	} finally {
		closeSync(descriptor);
	}
}

function copyRegularTree(source, destination) {
	const info = lstatSync(source);
	if (info.isSymbolicLink()) throw new Error(`marketplace fixture source contains a symlink: ${source}`);
	if (info.isDirectory()) {
		mkdirSync(destination, { recursive: true, mode: info.mode & 0o777 });
		for (const name of readdirSync(source).sort()) {
			copyRegularTree(join(source, name), join(destination, name));
		}
		return;
	}
	if (!info.isFile()) throw new Error(`marketplace fixture source is not a regular file: ${source}`);
	mkdirSync(dirname(destination), { recursive: true, mode: 0o700 });
	writeFileSync(destination, readFileSync(source), { mode: info.mode & 0o777 });
}

function regularTreeManifest(root) {
	const files = [];
	function visit(directory, prefix = '') {
		for (const name of readdirSync(directory).sort()) {
			const path = join(directory, name);
			const relative = prefix ? `${prefix}/${name}` : name;
			const info = lstatSync(path);
			if (info.isSymbolicLink()) throw new Error(`installed marketplace plugin contains a symlink: ${relative}`);
			if (info.isDirectory()) visit(path, relative);
			else if (info.isFile()) files.push({ path: relative, sha256: digestFile(path).sha256 });
			else throw new Error(`installed marketplace plugin contains an unsafe entry: ${relative}`);
		}
	}
	visit(root);
	return files;
}

async function prepareRealMLXInputs(root) {
	const manifestBytes = readFileSync(realReleaseManifest, 'utf8');
	const envelope = JSON.parse(manifestBytes);
	if (manifestBytes !== `${canonicalReleaseJSON(envelope)}\n`) {
		throw new Error('real-MLX release manifest must be canonical');
	}
	const osVersion = run('/usr/bin/sw_vers', ['-productVersion']).stdout.trim();
	const release = verifyReleaseManifestEnvelope(envelope, {
		architecture: 'arm64', minimumAcceptedEpoch: envelope.payload.release.epoch,
		now: new Date(), osVersion, packageVersion: '0.7.0', platform: 'darwin',
		trustedKeys: pinnedReleaseKeyring(realReleaseRoot),
	});
	const runtimeDescriptor = release.artifacts['embedder-runtime'];
	const modelDescriptor = release.artifacts.model;
	const runtimeDigest = digestFile(realEmbedderDmg);
	const modelDigest = digestFile(realModelFile);
	if (runtimeDescriptor.id !== 'pulse-embedder-runtime' || runtimeDescriptor.bytes !== runtimeDigest.bytes ||
		runtimeDescriptor.sha256 !== runtimeDigest.sha256 || modelDescriptor.id !== 'pulse-model' ||
		modelDescriptor.bytes !== modelDigest.bytes || modelDescriptor.sha256 !== modelDigest.sha256) {
		throw new Error('real-MLX inputs do not match the signed release descriptor');
	}
	const runtimeRoot = join(root, 'verified-real-embedder-runtime');
	mkdirSync(runtimeRoot, { mode: 0o700 });
	const materialized = await materializeVerifiedDmg(realEmbedderDmg, runtimeRoot, runtimeDescriptor);
	const quality = JSON.parse(readFileSync(join(runtimeRoot, 'QUALITY.json'), 'utf8'));
	if (quality.schema !== 'pulse.embedder.quality_gate.v1' || quality.quality_claimed !== true ||
		quality.fixture !== 'pass' ||
		quality.reference?.model_manifest_sha256 !== 'fa4361447341e16d2a95095ce369e67eafad53cfb93eac741418d722dac5f5f8' ||
		quality.reference?.model_revision !== '5617a9f61b028005a4858fdac845db406aefb181' ||
		quality.reference?.package !== 'FlagEmbedding' || quality.reference?.version !== '1.4.0' ||
		quality.reference?.package_sha256 !== 'fb1856b312851591341cf4533187350e9ce43f66bbf195c66f25a73266ff7db9') {
		throw new Error('real-MLX runtime lacks the exact production quality receipt');
	}
	return {
		modelDescriptor, modelDigest, modelPath: realModelFile,
		runtimeDescriptor, runtimeDigest, runtimePath: realEmbedderDmg,
		runtimeRoot, runtimeTree: materialized.treeManifest,
	};
}

const root = mkdtempSync(join(tmpdir(), 'pulse-codex-product.'));
let runtimeStopped = false;
try {
  const home = join(root, 'home');
  const codexHome = join(root, 'codex');
  const workspace = join(root, 'workspace');
	const workspaceB = join(root, 'workspace-b');
  const installRoot = join(root, 'packed-cli');
	const pulseDataDir = join(root, 'pulse');
	const artifactRoot = join(pulseDataDir, 'artifacts');
  mkdirSync(home, { recursive: true });
  mkdirSync(codexHome, { recursive: true });
  mkdirSync(installRoot, { recursive: true });
  initializeRepository(workspace);
	initializeRepository(workspaceB);

  const port = await freePort();
	const portB = await freePort();
	const bindingPaths = {
		registryPath: join(root, 'trust', 'workspace-bindings.json'),
		publicKeyPath: join(root, 'trust', 'workspace-bindings.pub.pem'),
		anchorPath: join(root, 'trust', 'workspace-bindings.anchor.json'),
	};
	const daemon = join(root, 'pulse-product-daemon');
	run('go', ['build', '-o', daemon, './cmd/pulse'], { cwd: pulseAppRoot, timeout: 120_000 });
	chmodSync(daemon, 0o700);
	const daemonBytes = readFileSync(daemon);
	const unhealthyDaemon = join(root, 'pulse-product-daemon-unhealthy.mjs');
	writeFileSync(unhealthyDaemon, `#!${process.execPath}
const fs = require('node:fs');
const http = require('node:http');
const value = (name) => process.argv[process.argv.indexOf(name) + 1];
const dataDir = value('-data-dir');
const [host, port] = value('-addr').split(':');
fs.mkdirSync(dataDir, { recursive: true, mode: 0o700 });
fs.writeFileSync(dataDir + '/secret.key', 'a'.repeat(64), { mode: 0o600 });
const server = http.createServer((_request, response) => { response.statusCode = 503; response.end('not ready'); });
server.listen(Number(port), host);
process.on('SIGTERM', () => server.close(() => process.exit(0)));
`, { mode: 0o700 });
	chmodSync(unhealthyDaemon, 0o700);
	const releasePair = generateKeyPairSync('ed25519');
	const releasePublicKey = releasePair.publicKey.export({ type: 'spki', format: 'pem' });
	const releaseRootPath = join(root, 'release-root.pem');
	writeFileSync(releaseRootPath, releasePublicKey, { mode: 0o600 });
	const releaseKey = {
		privateKey: releasePair.privateKey,
		keyID: releaseKeyID(releasePublicKey),
	};
	const realInputs = requireRealMLX ? await prepareRealMLXInputs(root) : null;
	const productReleaseFixture = (daemonFixtureBytes, epoch, options = {}) => realInputs
		? writeSyntheticReleaseFixture(root, releaseKey, daemonFixtureBytes, epoch, { realInputs, ...options })
		: writeSyntheticReleaseCatalogFixture(root, daemonFixtureBytes, epoch, options);
	let releaseFixture = await productReleaseFixture(readFileSync(unhealthyDaemon), 8);
	const productEvidence = realInputs ? {
		authority: 'synthetic-test', production_install_proof: false,
		package_source: 'npm-pack', system_go_exposed: false, system_python_exposed: false,
		personal_only_package: true, external_publication_performed: false,
		embedder: 'real-mlx-bge-m3', full_retrieval: true,
		model_sha256: realInputs.modelDigest.sha256,
		runtime_sha256: realInputs.runtimeDigest.sha256,
		schema: 'pulse.codex_product_e2e.v1',
	} : {
		authority: 'synthetic-test', production_install_proof: false,
		package_source: 'npm-pack', system_go_exposed: false, system_python_exposed: false,
		personal_only_package: true, external_publication_performed: false,
		embedder: 'synthetic-protocol-fixture', full_retrieval: true,
		schema: 'pulse.codex_product_e2e.v1',
	};
	assert.equal(existsSync(artifactRoot), false, 'packed provisioning must start with an empty artifact root');

	const tarball = packedTarball(root);
	const tarballDigest = digestFile(tarball);
  run('npm', ['install', '--ignore-scripts', '--no-audit', '--no-fund', '--prefix', installRoot, tarball], {
    cwd: root,
    timeout: 120_000,
  });
  const packedCLI = join(installRoot, 'node_modules', '@zbs-gg', 'pulse', 'src', 'cli.js');
	const packedPackageRoot = resolve(packedCLI, '..', '..');
	const packedPackageJSON = JSON.parse(readFileSync(join(packedPackageRoot, 'package.json'), 'utf8'));
	assert.equal(packedPackageJSON.name, '@zbs-gg/pulse');
	assert.equal(packedPackageJSON.version, '0.7.0');
	const publicPackageAudit = auditPublicPackageRoot(packedPackageRoot);
	assert.equal(publicPackageAudit.content_free, true);
	Object.assign(productEvidence, {
		package_version: packedPackageJSON.version,
		packed_tarball_sha256: tarballDigest.sha256,
		packed_tarball_bytes: tarballDigest.bytes,
			exact_tarball_bound: true,
			real_daemon_started_from_signed_release_fixture: true,
			tray_save_proof: false,
			automatic_durable_write_proof: true,
			unassigned_assignment_proof: false,
	});
	assert.equal(existsSync(join(packedPackageRoot, 'vendor/pulse-mcp-dist/index.js')), true);
	const tools = join(root, 'tools');
	mkdirSync(tools, { mode: 0o700 });
	const codexExecutable = run('/usr/bin/which', ['codex']).stdout.trim();
	symlinkSync(process.execPath, join(tools, 'node'));
	symlinkSync(codexExecutable, join(tools, 'codex'));
	symlinkSync('/usr/bin/git', join(tools, 'git'));
	const isolatedBin = join(home, '.local', 'bin');
	mkdirSync(isolatedBin, { recursive: true, mode: 0o700 });
	copyFileSync(realpathSync(codexExecutable), join(isolatedBin, 'codex'));
	chmodSync(join(isolatedBin, 'codex'), 0o700);
  const env = {
    ...process.env,
    HOME: home,
    CODEX_HOME: codexHome,
		PATH: tools,
    PULSE_DATA_DIR: pulseDataDir,
    PULSE_BINDING_REGISTRY_PATH: bindingPaths.registryPath,
    PULSE_BINDING_PUBLIC_KEY_PATH: bindingPaths.publicKeyPath,
		PULSE_BINDING_ANCHOR_PATH: bindingPaths.anchorPath,
		PULSE_TRUST_MODE: 'test',
		PULSE_TEST_CODEX_LIFECYCLE_ATTESTOR: '1',
		PULSE_RELEASE_TEST_MODE: '1',
		PULSE_RELEASE_MANIFEST_PATH: releaseFixture.manifestPath,
		PULSE_RELEASE_TEST_ROOT_PATH: releaseFixture.rootPath,
		PULSE_RELEASE_TEST_ASSET_ROOT: releaseFixture.assetsRoot,
		PULSE_RELEASE_TEST_MATERIALIZER_SPEC: releaseFixture.materializerPath,
	};
	for (const name of [
		'PULSE_GO_BIN', 'PULSE_LOCAL_EMBED_PYTHON', 'PULSE_LOCAL_EMBED_HELPER', 'PULSE_LOCAL_EMBED_MODEL',
		'PULSE_MANAGED_EMBEDDER_CONFIG', 'COHERE_API_KEY',
	]) delete env[name];
	assert.equal(existsSync(join(tools, 'go')), false);
	assert.equal(existsSync(join(tools, 'python')), false);

	const personalPlan = JSON.parse(run(process.execPath, [packedCLI, 'install-plan', '--json'], {
		cwd: workspace, env,
	}).stdout);
	assert.equal(personalPlan.schema, 'pulse.personal_install_plan.v2');
	assert.equal(personalPlan.outcome, 'action_required');
	assert.deepEqual(personalPlan.reason_codes, ['synthetic_authority_forbidden']);
	assert.equal('target_host' in personalPlan, false);
	assert.deepEqual(personalPlan.supported_hosts, ['claude-code', 'codex', 'cursor']);
	assert.equal(personalPlan.detected.hosts.some((host) => host.host === 'codex' && host.activation_target), true);
	assert.equal(personalPlan.release.artifacts.length, 5);
	assert.equal(personalPlan.release.total_download_bytes > 0, true);
	assert.deepEqual(personalPlan.release.origins, ['https://fixtures.invalid']);
	const rejectedPublicInstall = run(process.execPath, [packedCLI, 'install', '--json'], {
		cwd: workspace, env, status: 1,
	});
	const rejectedPublicInstallResult = JSON.parse(rejectedPublicInstall.stdout);
	assert.equal(rejectedPublicInstallResult.outcome, 'action_required');
	assert.equal(rejectedPublicInstallResult.reason_code, 'synthetic_authority_forbidden');
	assert.deepEqual(rejectedPublicInstallResult.completed_steps, []);
	assert.equal(existsSync(pulseDataDir), false, 'synthetic public install rejection must not create product state');
	assert.equal(existsSync(join(home, '.pulse')), false, 'synthetic public install rejection must not create identity state');
	assert.deepEqual(readdirSync(codexHome), [], 'synthetic public install rejection must not mutate Codex');
	assert.deepEqual(writeSignedPersonalBindings(root, [
		{ workspace: canonicalizeWorkspace(workspace), port, suffix: 'a' },
		{ workspace: canonicalizeWorkspace(workspaceB), port: portB, suffix: 'b' },
	]), bindingPaths);

	const failedConnect = run(process.execPath, [packedCLI, 'connect', 'codex'], {
		cwd: workspace, env, status: 1, timeout: 120_000,
	});
	assert.match(`${failedConnect.stdout}${failedConnect.stderr}`, /managed full-retrieval smoke|did not become ready/);
	const pluginAfterFailure = run('codex', ['plugin', 'list', '--marketplace', 'zbs-gg'], { cwd: workspace, env });
	assert.match(pluginAfterFailure.stdout, /pulse@zbs-gg\s+not installed|No plugins found/);
	assert.equal(existsSync(join(root, 'pulse', 'runtime', 'codex', 'current')), false);
	assert.equal(existsSync(join(root, 'pulse', 'capture-state.json')), false);
	assert.equal(existsSync(join(codexHome, 'pulse', 'product-locators.json')), false);
	const healthyDaemonBytes = Buffer.concat([daemonBytes, Buffer.from('\nPULSE_HEALTHY_AFTER_FAILED_ACTIVATION\n')]);
	releaseFixture = await productReleaseFixture(healthyDaemonBytes, 8);
	Object.assign(env, {
		PULSE_RELEASE_MANIFEST_PATH: releaseFixture.manifestPath,
		PULSE_RELEASE_TEST_ROOT_PATH: releaseFixture.rootPath,
		PULSE_RELEASE_TEST_ASSET_ROOT: releaseFixture.assetsRoot,
		PULSE_RELEASE_TEST_MATERIALIZER_SPEC: releaseFixture.materializerPath,
	});

	const connected = run(process.execPath, [packedCLI, 'connect', 'codex'], {
		cwd: workspace, env, timeout: 120_000,
	});
	assert.match(connected.stdout, /pulse@|Codex plugin installed/);

  const nativeMcp = run('codex', ['mcp', 'get', 'pulse-product', '--json'], { cwd: workspace, env });
  const nativeMcpConfig = JSON.parse(nativeMcp.stdout);
  assert.equal(nativeMcpConfig.transport.type, 'stdio');
  assert.equal(nativeMcpConfig.transport.command, 'node');
  assert.deepEqual(nativeMcpConfig.transport.args.slice(0, 2), ['--input-type=module', '--eval']);
  assert.match(nativeMcpConfig.transport.args[2], /CODEX_HOME/);
  assert.deepEqual(nativeMcpConfig.transport.env_vars, ['CODEX_HOME']);
  assert.equal(nativeMcpConfig.transport.cwd, null);

  const cacheVersions = readdirSync(join(codexHome, 'plugins', 'cache', 'zbs-gg', 'pulse'));
  assert.equal(cacheVersions.length, 1);
	const pluginRoot = join(codexHome, 'plugins', 'cache', 'zbs-gg', 'pulse', cacheVersions[0]);
  assert.equal(existsSync(join(pluginRoot, 'mcp', 'server.mjs')), true);
	const committedRelease = readCommittedArtifactSet({ installRoot: artifactRoot });
	const signedMarketplaceRoot = join(committedRelease.activations['plugin-runtime'].version_path, 'marketplace');
	const signedPluginRoot = join(signedMarketplaceRoot, 'plugins', 'pulse');
	const configuredMarketplaces = run('codex', ['plugin', 'marketplace', 'list'], { cwd: workspace, env }).stdout;
	const signedEdge = resolveSignedCodexProductEdge({
		release: {
			schema: 'pulse.verified_release_manifest.v2',
			manifest_digest: committedRelease.record.manifest_digest,
			version: committedRelease.record.version,
			epoch: committedRelease.record.epoch,
		},
		activation: committedRelease.activations['plugin-runtime'],
	});
	const marketplaceSnapshot = inspectCodexMarketplaceSnapshot(signedEdge, join(root, 'pulse'));
	assert.equal(marketplaceSnapshot.ok, true);
	const configuredMarketplace = parseCodexMarketplaceList(configuredMarketplaces);
	assert.equal(configuredMarketplace.configured, true);
	assert.equal(realpathSync(configuredMarketplace.root), marketplaceSnapshot.marketplace_root,
		'Codex marketplace provenance must name the exact activation-owned snapshot');
	assert.notEqual(marketplaceSnapshot.marketplace_root, realpathSync(signedMarketplaceRoot),
		'Codex must never receive the immutable signed artifact tree directly');
	assert.equal(marketplaceSnapshot.marketplace_root.startsWith(
		realpathSync(join(root, 'pulse', 'runtime', 'codex-marketplaces')),
	), true);
	assert.equal(configuredMarketplaces.includes(repoRoot), false,
		'packed Codex lifecycle must not register the live repository as marketplace provenance');
	assert.deepEqual(regularTreeManifest(pluginRoot), regularTreeManifest(signedPluginRoot),
		'installed Codex plugin bytes must exactly match the signed plugin-runtime artifact');
	assert.deepEqual(regularTreeManifest(marketplaceSnapshot.plugin_root), regularTreeManifest(signedPluginRoot),
		'operational marketplace snapshot bytes must exactly match the signed plugin-runtime artifact');
	for (const relative of ['plugins', 'plugins/pulse', 'plugins/pulse/.codex-plugin', 'plugins/pulse/hooks', 'plugins/pulse/mcp']) {
		assert.equal(lstatSync(join(signedMarketplaceRoot, relative)).mode & 0o777, 0o700,
			`Codex must not change signed artifact directory mode: ${relative}`);
	}
  const hook = join(pluginRoot, 'hooks', 'pulse-hook.mjs');
  const sessionID = 'session-codex-e2e';
  const freshHostEnv = Object.fromEntries(
		Object.entries(env).filter(([name]) => !name.startsWith('PULSE_') ||
			['PULSE_TRUST_MODE', 'PULSE_TEST_CODEX_LIFECYCLE_ATTESTOR'].includes(name)),
  );
	const hookEnv = {
	  ...freshHostEnv,
	  PLUGIN_DATA: join(root, 'plugin-data'),
	};
	const activationPath = join(root, 'pulse', 'runtime', 'product-daemon.json');
	const activationBeforeMismatch = readFileSync(activationPath);
	const mismatchedActivation = JSON.parse(activationBeforeMismatch);
	mismatchedActivation.runtime_tree_digest = 'f'.repeat(64);
	writeFileSync(activationPath, JSON.stringify(mismatchedActivation), { mode: 0o600 });
	const rejectedMixedPair = run(process.execPath, [hook, 'SessionStart'], {
		cwd: workspace,
		env: hookEnv,
		status: 1,
		input: JSON.stringify({
			session_id: 'session-rejected-mixed-pair', cwd: workspace,
			hook_event_name: 'SessionStart', source: 'startup', permission_mode: 'default',
		}),
	});
	assert.match(
		`${rejectedMixedPair.stdout}${rejectedMixedPair.stderr}`,
		/runtime and activation are out of sync/,
	);
	writeFileSync(activationPath, activationBeforeMismatch, { mode: 0o600 });
	const installedRuntimeCLI = join(root, 'pulse', 'runtime', 'codex', 'current', 'src', 'cli.js');
	const installedRuntimeCLIBytes = readFileSync(installedRuntimeCLI);
	const installedRuntimeCLIMode = lstatSync(installedRuntimeCLI).mode & 0o777;
	writeFileSync(installedRuntimeCLI, Buffer.concat([
		installedRuntimeCLIBytes, Buffer.from('\n// untrusted-runtime-tamper\n'),
	]));
	const rejectedTamperedRuntime = run(process.execPath, [hook, 'SessionStart'], {
		cwd: workspace,
		env: hookEnv,
		status: 1,
		input: JSON.stringify({
			session_id: 'session-rejected-runtime-tamper', cwd: workspace,
			hook_event_name: 'SessionStart', source: 'startup', permission_mode: 'default',
		}),
	});
	assert.match(
		`${rejectedTamperedRuntime.stdout}${rejectedTamperedRuntime.stderr}`,
		/runtime and activation are out of sync/,
	);
	writeFileSync(installedRuntimeCLI, installedRuntimeCLIBytes, { mode: installedRuntimeCLIMode });

	const runtimeSymlinkTarget = join(root, 'untrusted-runtime-target.mjs');
	writeFileSync(runtimeSymlinkTarget, 'throw new Error("untrusted runtime executed");\n', { mode: 0o600 });
	rmSync(installedRuntimeCLI);
	symlinkSync(runtimeSymlinkTarget, installedRuntimeCLI);
	const rejectedRuntimeSymlink = run(process.execPath, [hook, 'SessionStart'], {
		cwd: workspace,
		env: hookEnv,
		status: 1,
		input: JSON.stringify({
			session_id: 'session-rejected-runtime-symlink', cwd: workspace,
			hook_event_name: 'SessionStart', source: 'startup', permission_mode: 'default',
		}),
	});
	assert.match(
		`${rejectedRuntimeSymlink.stdout}${rejectedRuntimeSymlink.stderr}`,
		/trusted runtime contains a symlink: src\/cli\.js/,
	);
	rmSync(installedRuntimeCLI);
	writeFileSync(installedRuntimeCLI, installedRuntimeCLIBytes, { mode: installedRuntimeCLIMode });
	const syntheticWithoutHostOptIn = { ...hookEnv };
	delete syntheticWithoutHostOptIn.PULSE_TRUST_MODE;
	const rejectedSyntheticLaunch = run(process.execPath, [hook, 'SessionStart'], {
		cwd: workspace,
		env: syntheticWithoutHostOptIn,
		status: 1,
		input: JSON.stringify({
			session_id: 'session-rejected-synthetic', cwd: workspace,
			hook_event_name: 'SessionStart', source: 'startup', permission_mode: 'default',
		}),
	});
	assert.match(`${rejectedSyntheticLaunch.stdout}${rejectedSyntheticLaunch.stderr}`, /synthetic test locator requires an explicitly test-mode host process/);
  const sessionStart = run(process.execPath, [hook, 'SessionStart'], {
    cwd: workspace,
    env: hookEnv,
    input: JSON.stringify({
      session_id: sessionID,
      transcript_path: join(root, 'must-not-be-read.jsonl'),
      cwd: workspace,
      hook_event_name: 'SessionStart',
      model: 'gpt-5',
      source: 'startup',
      permission_mode: 'default',
    }),
  });
  const sessionOutput = JSON.parse(sessionStart.stdout);
  assert.equal(sessionOutput.continue, true);
  assert.match(sessionOutput.hookSpecificOutput.additionalContext, /pulse.context.v1/);

  const prompt = run(process.execPath, [hook, 'UserPromptSubmit'], {
    cwd: workspace,
    env: hookEnv,
    input: JSON.stringify({
      session_id: sessionID,
      turn_id: 'turn-codex-e2e',
      transcript_path: join(root, 'must-not-be-read.jsonl'),
      cwd: workspace,
      hook_event_name: 'UserPromptSubmit',
      model: 'gpt-5',
      permission_mode: 'default',
      prompt: 'must not be stored',
    }),
  });
  const promptOutput = JSON.parse(prompt.stdout);
  assert.equal(promptOutput.continue, true);
  assert.match(promptOutput.hookSpecificOutput.additionalContext, /before the single final user-facing response/);
  assert.match(promptOutput.hookSpecificOutput.additionalContext, /ASCII safe slug/);

  const memoryArguments = {
    schema: 'pulse.memory_capsule.v1',
    source: { host: 'codex', conversation_scope: 'current_turn', timestamp: '2026-07-14T10:00:00Z' },
    items: [{
      kind: 'decision',
      redacted_summary: 'Use one trusted local runtime for Codex lifecycle memory.',
      confidence: 0.98,
      evidence_hint: 'current_turn',
      privacy_tier: 'normal',
      retention: 'project',
    }],
    raw_input_included: false,
  };
  const preTool = run(process.execPath, [hook, 'PreToolUse'], {
    cwd: workspace,
    env: hookEnv,
    input: JSON.stringify({
      session_id: sessionID,
      turn_id: 'turn-codex-e2e',
      transcript_path: join(root, 'must-not-be-read.jsonl'),
      cwd: workspace,
      hook_event_name: 'PreToolUse',
      model: 'gpt-5',
      permission_mode: 'default',
      tool_name: 'mcp__pulse-product__pulse_remember',
      tool_input: memoryArguments,
      tool_use_id: 'tool-codex-e2e-remember',
    }),
  });
  assert.deepEqual(JSON.parse(preTool.stdout), {});

  const mcpInput = [
    { jsonrpc: '2.0', id: 1, method: 'initialize', params: {
      protocolVersion: '2024-11-05', capabilities: {}, clientInfo: { name: 'pulse-e2e', version: '1' },
    } },
    { jsonrpc: '2.0', method: 'notifications/initialized', params: {} },
    { jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} },
    { jsonrpc: '2.0', id: 3, method: 'tools/call', params: {
      name: 'pulse_remember',
      arguments: memoryArguments,
    } },
  ].map((message) => JSON.stringify(message)).join('\n') + '\n';
  const mcp = run(process.execPath, [join(pluginRoot, 'mcp', 'server.mjs')], {
    cwd: workspace,
    env: hookEnv,
    input: mcpInput,
    timeout: 20_000,
  });
  const mcpMessages = mcp.stdout.trim().split(/\r?\n/).filter(Boolean).map((line) => JSON.parse(line));
  assert.equal(mcpMessages.find((message) => message.id === 1)?.result?.serverInfo?.name, 'pulse-mcp');
  assert.equal(Array.isArray(mcpMessages.find((message) => message.id === 2)?.result?.tools), true);
  const toolNames = mcpMessages.find((message) => message.id === 2).result.tools.map((tool) => tool.name);
  assert.equal(toolNames.includes('pulse_wipe'), false);
  assert.equal(toolNames.includes('pulse_forget'), false);
  assert.equal(toolNames.includes('pulse_tray'), true);
	  const remembered = JSON.parse(mcpMessages.find((message) => message.id === 3).result.content[0].text);
	  assert.equal(remembered.status, 'candidates');
	  assert.equal(remembered.receipts[0].status, 'created');
	  assert.match(remembered.receipts[0].object_id, /^pulse:/);
	  assert.equal(remembered.receipts[0].safe_provenance.host, 'codex');
  assert.match(remembered.receipts[0].safe_provenance.session_id, /^session:[a-f0-9]{64}$/);
  assert.match(remembered.receipts[0].safe_provenance.turn_id, /^turn:[a-f0-9]{64}$/);

	  // A separate MCP round trip proves ordinary Personal memory did not leave
	  // a review card behind after the durable write succeeded.
  const trayInput = [
    { jsonrpc: '2.0', id: 4, method: 'initialize', params: {
      protocolVersion: '2024-11-05', capabilities: {}, clientInfo: { name: 'pulse-e2e', version: '1' },
    } },
    { jsonrpc: '2.0', method: 'notifications/initialized', params: {} },
    { jsonrpc: '2.0', id: 5, method: 'tools/call', params: {
      name: 'pulse_tray', arguments: { limit: 10 },
    } },
  ].map((message) => JSON.stringify(message)).join('\n') + '\n';
  const trayMCP = run(process.execPath, [join(pluginRoot, 'mcp', 'server.mjs')], {
    cwd: workspace,
    env: hookEnv,
    input: trayInput,
    timeout: 20_000,
  });
  const trayMessages = trayMCP.stdout.trim().split(/\r?\n/).filter(Boolean).map((line) => JSON.parse(line));
  const tray = JSON.parse(trayMessages.find((message) => message.id === 5).result.content[0].text);
	  const committedCard = tray.candidates.find((candidate) =>
	    candidate.candidate_id === remembered.receipts[0].candidate_id);
	  assert.equal(committedCard.state, 'committed');
	  assert.equal(committedCard.current, true);
	  assert.equal(committedCard.canonical_object_id, remembered.receipts[0].object_id);
	  assert.equal(committedCard.latest_receipt.status, 'created');
	  assert.equal(committedCard.projection_status, 'complete');

  const postTool = run(process.execPath, [hook, 'PostToolUse'], {
    cwd: workspace,
    env: hookEnv,
    input: JSON.stringify({
      session_id: sessionID,
      turn_id: 'turn-codex-e2e',
      transcript_path: join(root, 'must-not-be-read.jsonl'),
      cwd: workspace,
      hook_event_name: 'PostToolUse',
      model: 'gpt-5',
      permission_mode: 'default',
      tool_name: 'mcp__pulse-product__pulse_remember',
      tool_input: memoryArguments,
      tool_use_id: 'tool-codex-e2e-remember',
      tool_response: mcpMessages.find((message) => message.id === 3).result,
    }),
  });
  const postToolOutput = JSON.parse(postTool.stdout);
  assert.deepEqual(postToolOutput, {});

  const stop = run(process.execPath, [hook, 'Stop'], {
    cwd: workspace,
    env: hookEnv,
    input: JSON.stringify({
      session_id: sessionID,
      turn_id: 'turn-codex-e2e',
      transcript_path: join(root, 'must-not-be-read.jsonl'),
      cwd: workspace,
      hook_event_name: 'Stop',
      model: 'gpt-5',
      permission_mode: 'default',
      stop_hook_active: false,
      last_assistant_message: 'must not be stored',
    }),
  });
  const firstStopOutput = JSON.parse(stop.stdout);
  assert.deepEqual(firstStopOutput, {});

  const recursiveStop = run(process.execPath, [hook, 'Stop'], {
    cwd: workspace,
    env: hookEnv,
    input: JSON.stringify({
      session_id: sessionID,
      turn_id: 'turn-codex-e2e',
      transcript_path: join(root, 'must-not-be-read.jsonl'),
      cwd: workspace,
      hook_event_name: 'Stop',
      model: 'gpt-5',
      permission_mode: 'default',
      stop_hook_active: true,
      last_assistant_message: 'must not be stored',
    }),
  });
  assert.deepEqual(JSON.parse(recursiveStop.stdout), {});

	const doctor = run(process.execPath, [packedCLI, 'doctor', 'codex', '--json'], {
		cwd: workspace, env, status: 1,
	});
  const report = JSON.parse(doctor.stdout);
	assert.equal(report.verdict, 'Pulse Codex automatic lifecycle is not ready.');
	assert.equal(report.trust.authority_mode, 'synthetic-test');
	assert.equal(report.checks.native_hook_trust.ok, false);
	assert.equal(report.checks.native_hook_trust.reason, 'codex_native_hook_trust_required',
		report.checks.native_hook_trust.detail);
	assert.equal(report.checks.hooks.ok, true,
		'direct hook fixtures may prove lifecycle behavior but cannot approve native Codex trust');
	assert.equal(Object.entries(report.checks)
		.filter(([name]) => name !== 'native_hook_trust')
		.every(([, check]) => check.ok), true);
  assert.equal(report.trust.raw_transcript_capture, false);
  assert.equal(report.trust.full_retrieval, true);
	assert.equal(report.trust.external_embedding_api, false);

	const cacheHookConfigPath = join(pluginRoot, 'hooks', 'hooks.json');
	const cacheHookConfigBytes = readFileSync(cacheHookConfigPath);
	const mutatingTools = join(root, 'mutating-tools');
	mkdirSync(mutatingTools, { mode: 0o700 });
	symlinkSync(process.execPath, join(mutatingTools, 'node'));
	const mutatingCodex = join(mutatingTools, 'codex');
	writeFileSync(mutatingCodex, `#!${process.execPath}
const fs = require('node:fs');
const { spawnSync } = require('node:child_process');
const readline = require('node:readline');
const args = process.argv.slice(2);
const realCodex = ${JSON.stringify(codexExecutable)};
const sourcePath = fs.realpathSync(${JSON.stringify(cacheHookConfigPath)});
const pluginRoot = fs.realpathSync(${JSON.stringify(pluginRoot)});
if (args[0] !== 'app-server') {
  const result = spawnSync(realCodex, args, { stdio: 'inherit', env: process.env });
  process.exit(result.status ?? 1);
}
const nativeEvents = new Map([
  ['PostCompact', 'postCompact'], ['PostToolUse', 'postToolUse'],
  ['PreCompact', 'preCompact'], ['PreToolUse', 'preToolUse'],
  ['SessionStart', 'sessionStart'], ['Stop', 'stop'],
  ['SubagentStart', 'subagentStart'], ['SubagentStop', 'subagentStop'],
  ['UserPromptSubmit', 'userPromptSubmit'],
]);
const config = JSON.parse(fs.readFileSync(sourcePath, 'utf8'));
process.on('SIGTERM', () => {
  fs.appendFileSync(sourcePath, ' ');
  process.exit(0);
});
readline.createInterface({ input: process.stdin }).on('line', (line) => {
  const message = JSON.parse(line);
  if (message.id === 1) {
    process.stdout.write(JSON.stringify({ id: 1, result: { userAgent: 'pulse-mutation-fixture' } }) + '\\n');
    return;
  }
  if (message.id !== 2) return;
  const hooks = Object.entries(config.hooks).map(([configuredEvent, groups], index) => {
    const group = groups[0];
    const handler = group.hooks[0];
    return {
      key: 'pulse@zbs-gg:' + configuredEvent + ':' + index,
      eventName: nativeEvents.get(configuredEvent), handlerType: 'command',
      matcher: group.matcher ?? null,
      command: handler.command.replaceAll('\${PLUGIN_ROOT}', pluginRoot),
      timeoutSec: handler.timeout, sourcePath, source: 'plugin', pluginId: 'pulse@zbs-gg',
      displayOrder: index, enabled: true, isManaged: false,
      currentHash: 'sha256:' + String(index + 1).padStart(64, '0'), trustStatus: 'untrusted',
    };
  });
  process.stdout.write(JSON.stringify({ id: 2, result: { data: [{
    cwd: message.params.cwds[0], hooks, warnings: [], errors: [],
  }] } }) + '\\n');
});
`, { mode: 0o700 });
	chmodSync(mutatingCodex, 0o700);
	const mutationDoctor = run(process.execPath, [packedCLI, 'doctor', 'codex', '--json'], {
		cwd: workspace, env: { ...env, PATH: `${mutatingTools}:${tools}` }, status: 1,
	});
	const mutationReport = JSON.parse(mutationDoctor.stdout);
	assert.equal(mutationReport.checks.plugin.reason, 'codex_product_state_changed_during_inspection');
	assert.equal(mutationReport.checks.native_hook_trust.reason, 'codex_product_state_changed_during_inspection');
	assert.equal(mutationReport.checks.hooks.reason, 'codex_product_state_changed_during_inspection');
	writeFileSync(cacheHookConfigPath, cacheHookConfigBytes);

	const receiptBeforeHelperCrash = JSON.parse(readFileSync(
		join(root, 'vaults', 'personal-a', 'supervisor-runtime.json'), 'utf8',
	));
	const helperChildren = run('/usr/bin/pgrep', ['-P', String(receiptBeforeHelperCrash.pid)]).stdout
		.trim().split(/\s+/).filter(Boolean).map(Number);
	assert.equal(helperChildren.length, 1, 'managed daemon must own exactly one embedder helper');
	process.kill(helperChildren[0], 'SIGKILL');
	const healedAfterHelperCrash = run(process.execPath, [join(pluginRoot, 'mcp', 'server.mjs')], {
		cwd: workspace, env: hookEnv, timeout: 30_000, input: mcpInput,
	});
	assert.match(healedAfterHelperCrash.stdout, /"serverInfo"/);
	const receiptAfterHelperCrash = JSON.parse(readFileSync(
		join(root, 'vaults', 'personal-a', 'supervisor-runtime.json'), 'utf8',
	));
	assert.notEqual(receiptAfterHelperCrash.pid, receiptBeforeHelperCrash.pid,
		'helper death must restart the exact managed daemon generation once');
	assert.equal(receiptAfterHelperCrash.executable_digest, receiptBeforeHelperCrash.executable_digest);
	assert.equal(
		receiptAfterHelperCrash.managed_embedder_config_digest,
		receiptBeforeHelperCrash.managed_embedder_config_digest,
	);

	const receiptBeforeFailedManagedUpgrade = JSON.parse(readFileSync(
		join(root, 'vaults', 'personal-a', 'supervisor-runtime.json'), 'utf8',
	));
	const activationBeforeFailedManagedUpgrade = readFileSync(
		join(root, 'pulse', 'runtime', 'product-daemon.json'),
	);
	const activeSetBeforeFailedManagedUpgrade = readFileSync(
		join(root, 'pulse', 'artifacts', 'active-release.json'),
	);
	const marketplaceBeforeFailedManagedUpgrade = parseCodexMarketplaceList(
		run('codex', ['plugin', 'marketplace', 'list'], { cwd: workspace, env }).stdout,
	).root;
	releaseFixture = await productReleaseFixture(readFileSync(unhealthyDaemon), 9);
	Object.assign(env, {
		PULSE_RELEASE_MANIFEST_PATH: releaseFixture.manifestPath,
		PULSE_RELEASE_TEST_ROOT_PATH: releaseFixture.rootPath,
		PULSE_RELEASE_TEST_ASSET_ROOT: releaseFixture.assetsRoot,
		PULSE_RELEASE_TEST_MATERIALIZER_SPEC: releaseFixture.materializerPath,
	});
	const failedManagedUpgrade = run(process.execPath, [packedCLI, 'connect', 'codex'], {
		cwd: workspace, env, status: 1, timeout: 120_000,
	});
	assert.match(
		`${failedManagedUpgrade.stdout}${failedManagedUpgrade.stderr}`,
		/managed full-retrieval smoke|did not become ready/,
	);
	const receiptAfterFailedManagedUpgrade = JSON.parse(readFileSync(
		join(root, 'vaults', 'personal-a', 'supervisor-runtime.json'), 'utf8',
	));
	for (const field of [
		'executable', 'executable_digest', 'managed_embedder_config_path', 'managed_embedder_config_digest',
		'embedder_runtime_activation_digest', 'embedder_runtime_tree_digest',
		'model_activation_digest', 'model_tree_digest',
	]) {
		assert.equal(
			receiptAfterFailedManagedUpgrade[field], receiptBeforeFailedManagedUpgrade[field],
			`failed managed upgrade must restore receipt field ${field}`,
		);
	}
	assert.deepEqual(
		readFileSync(join(root, 'pulse', 'runtime', 'product-daemon.json')),
		activationBeforeFailedManagedUpgrade,
	);
	assert.deepEqual(
		readFileSync(join(root, 'pulse', 'artifacts', 'active-release.json')),
		activeSetBeforeFailedManagedUpgrade,
		'failed managed upgrade must restore the previous atomic artifact set',
	);
	readCommittedArtifactSet({ installRoot: join(root, 'pulse', 'artifacts') });
	assert.equal(realpathSync(parseCodexMarketplaceList(
		run('codex', ['plugin', 'marketplace', 'list'], { cwd: workspace, env }).stdout,
	).root), realpathSync(marketplaceBeforeFailedManagedUpgrade),
	'failed managed upgrade must restore the previous marketplace provenance');
	releaseFixture = await productReleaseFixture(healthyDaemonBytes, 8);
	Object.assign(env, {
		PULSE_RELEASE_MANIFEST_PATH: releaseFixture.manifestPath,
		PULSE_RELEASE_TEST_ROOT_PATH: releaseFixture.rootPath,
		PULSE_RELEASE_TEST_ASSET_ROOT: releaseFixture.assetsRoot,
		PULSE_RELEASE_TEST_MATERIALIZER_SPEC: releaseFixture.materializerPath,
	});

	run(process.execPath, [packedCLI, 'connect', 'codex'], { cwd: workspaceB, env, timeout: 120_000 });
	const receiptBv1 = JSON.parse(readFileSync(join(root, 'vaults', 'personal-b', 'supervisor-runtime.json'), 'utf8'));
	writeFileSync(packedCLI, `${readFileSync(packedCLI, 'utf8')}\n// multi-workspace-runtime-v2\n`);
	const daemonV2Bytes = Buffer.concat([healthyDaemonBytes, Buffer.from('\nPULSE_MULTI_WORKSPACE_V2\n')]);
	releaseFixture = await productReleaseFixture(daemonV2Bytes, 8);
	Object.assign(env, {
		PULSE_RELEASE_MANIFEST_PATH: releaseFixture.manifestPath,
		PULSE_RELEASE_TEST_ROOT_PATH: releaseFixture.rootPath,
		PULSE_RELEASE_TEST_ASSET_ROOT: releaseFixture.assetsRoot,
		PULSE_RELEASE_TEST_MATERIALIZER_SPEC: releaseFixture.materializerPath,
	});
	run(process.execPath, [packedCLI, 'connect', 'codex'], { cwd: workspace, env, timeout: 120_000 });
	const activationV4 = JSON.parse(readFileSync(join(root, 'pulse', 'runtime', 'product-daemon.json'), 'utf8'));
	const activeReleaseV2 = readCommittedArtifactSet({ installRoot: join(root, 'pulse', 'artifacts') });
	const receiptAv2 = JSON.parse(readFileSync(join(root, 'vaults', 'personal-a', 'supervisor-runtime.json'), 'utf8'));
	assert.equal(activationV4.schema, 'pulse.product_activation.v4');
	assert.equal(activationV4.release_manifest_digest, activeReleaseV2.record.manifest_digest);
	assert.equal(activationV4.release_version, activeReleaseV2.record.version);
	assert.equal(activationV4.release_epoch, activeReleaseV2.record.epoch);
	assert.equal(activationV4.plugin_runtime_activation_digest,
		activeReleaseV2.activations['plugin-runtime'].activation_digest);
	assert.equal(activationV4.plugin_runtime_tree_digest,
		activeReleaseV2.activations['plugin-runtime'].tree_digest);
	assert.equal(receiptAv2.executable_digest, activationV4.daemon_digest);
	assert.match(receiptAv2.managed_embedder_config_digest, /^[a-f0-9]{64}$/);
	assert.equal('managed_embedder_config_path' in activationV4, false,
		'global activation must not pin one project vault config');
	assert.notEqual(receiptBv1.executable_digest, activationV4.daemon_digest);
	const workspaceBResume = run(process.execPath, [hook, 'SessionStart'], {
		cwd: workspaceB,
		env: hookEnv,
		input: JSON.stringify({
			session_id: 'session-workspace-b-after-upgrade', cwd: workspaceB,
			hook_event_name: 'SessionStart', source: 'resume', model: 'gpt-5', permission_mode: 'default',
		}),
		timeout: 30_000,
	});
	assert.match(JSON.parse(workspaceBResume.stdout).hookSpecificOutput.additionalContext, /pulse.context.v1/);
	const receiptBv2 = JSON.parse(readFileSync(join(root, 'vaults', 'personal-b', 'supervisor-runtime.json'), 'utf8'));
	assert.equal(receiptBv2.executable_digest, activationV4.daemon_digest,
		'first hook in another workspace must reconcile its vault before reading memory');
	assert.notEqual(receiptBv2.managed_embedder_config_path, receiptAv2.managed_embedder_config_path,
		'each project vault must keep its own private managed embedder config');

  const runtimeReceipt = JSON.parse(readFileSync(join(root, 'vaults', 'personal-a', 'supervisor-runtime.json'), 'utf8'));
  process.kill(runtimeReceipt.pid, 'SIGSTOP');
  try {
    const hungDoctor = run(process.execPath, [packedCLI, 'doctor', 'codex', '--json'], {
      cwd: workspace, env, status: 1,
    });
    const hungReport = JSON.parse(hungDoctor.stdout);
    assert.equal(hungReport.checks.vault.ok, false);
    assert.match(hungReport.checks.vault.detail, /timeout|unavailable|pulse/i);
  } finally {
    process.kill(runtimeReceipt.pid, 'SIGCONT');
  }

	run(process.execPath, [packedCLI, 'disconnect', 'codex'], { cwd: workspaceB, env });
  run(process.execPath, [packedCLI, 'disconnect', 'codex'], { cwd: workspace, env });
  const disconnected = run(process.execPath, [packedCLI, 'doctor', 'codex', '--json'], {
    cwd: workspace, env, status: 1,
  });
  const disconnectedReport = JSON.parse(disconnected.stdout);
  assert.equal(disconnectedReport.checks.plugin.ok, false);
  assert.equal(disconnectedReport.checks.capture.ok, false);

	process.stdout.write(`${JSON.stringify(productEvidence)}\n`);
	process.stdout.write(requireRealMLX
		? 'Pulse Codex packed-product synthetic-authority lifecycle with real MLX passed; this is not a production install proof.\n'
		: 'Pulse Codex packed-product synthetic-authority lifecycle passed; this is not a production install proof.\n');
} finally {
  if (!runtimeStopped) {
    const receiptPaths = [
		join(root, 'vaults', 'personal-a', 'supervisor-runtime.json'),
		join(root, 'vaults', 'personal-b', 'supervisor-runtime.json'),
    ];
    for (const path of receiptPaths) {
      try {
        const receipt = JSON.parse(readFileSync(path, 'utf8'));
        process.kill(receipt.pid, 'SIGTERM');
      } catch { /* no running fixture */ }
    }
  }
	if (process.env.PULSE_KEEP_CODEX_E2E_ROOT === '1') process.stderr.write(`kept Codex E2E root: ${root}\n`);
	else rmSync(root, { recursive: true, force: true });
}
