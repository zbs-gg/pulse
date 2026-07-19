import {
	chmodSync,
  cpSync,
  existsSync,
  lstatSync,
  mkdirSync,
  readFileSync,
  readdirSync,
	realpathSync,
  renameSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { createHash } from 'node:crypto';
import { homedir } from 'node:os';
import { basename, dirname, isAbsolute, join, relative, resolve, sep } from 'node:path';

import { migrateLegacyPulseHookConfig } from './host-adapter.js';
import { defaultPlatformServices } from './platform-services.js';

const RUNTIME_MANIFEST = 'runtime-manifest.json';
const MARKETPLACE_SNAPSHOT_MANIFEST = 'snapshot.json';
const PRODUCT_HOSTS = Object.freeze(['claude-code', 'codex', 'cursor']);

export function codexProductActivationReady(report) {
	const required = [
		'presence_trust', 'authority', 'codex', 'host_access', 'plugin', 'marketplace', 'plugin_mcp',
		'mcp_shadow', 'legacy_hooks', 'binding', 'runtime', 'activation', 'vault', 'capture',
	];
	return required.every((name) => report?.checks?.[name]?.ok === true);
}

export function codexHomePath(env = process.env) {
  return resolve(env.CODEX_HOME || join(homedir(), '.codex'));
}

export function legacyCodexHookPaths({ cwd = process.cwd(), codexHome = codexHomePath() } = {}) {
  return [...new Set([
    join(codexHome, 'hooks.json'),
    resolve(cwd, '.codex', 'hooks.json'),
  ])];
}

function atomicWriteJSON(path, value) {
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  const temporary = `${path}.${process.pid}.${Date.now()}.new`;
  try {
    writeFileSync(temporary, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600, flag: 'wx' });
    renameSync(temporary, path);
  } finally {
    rmSync(temporary, { force: true });
  }
}

export function inspectLegacyPulseHookFiles(options = {}) {
  const files = [];
  let total = 0;
  for (const path of legacyCodexHookPaths(options)) {
    if (!existsSync(path)) continue;
    let raw;
    try {
      raw = JSON.parse(readFileSync(path, 'utf8'));
    } catch {
      throw new Error(`invalid Codex hook JSON: ${path}`);
    }
    const migrated = migrateLegacyPulseHookConfig(raw);
    total += migrated.removed;
    files.push({ path, removed: migrated.removed, config: migrated.config });
  }
  return { removed: total, files };
}

export function migrateLegacyPulseHookFiles(options = {}) {
  const inspection = inspectLegacyPulseHookFiles(options);
  for (const file of inspection.files) {
    if (file.removed > 0) atomicWriteJSON(file.path, file.config);
  }
  return inspection;
}

function hasPulseProductTable(text) {
  const name = '(?:pulse-product|"pulse-product"|\'pulse-product\')';
  return new RegExp(`^\\s*\\[\\s*mcp_servers\\s*\\.\\s*${name}\\s*\\]\\s*$`, 'm').test(text) ||
    new RegExp(`^\\s*mcp_servers\\s*\\.\\s*${name}\\s*=`, 'm').test(text);
}

export function pulseProductMcpShadowFiles({ cwd = process.cwd(), codexHome = codexHomePath() } = {}) {
  const paths = [...new Set([
    join(codexHome, 'config.toml'),
    resolve(cwd, '.codex', 'config.toml'),
  ])];
  return paths.filter((path) => existsSync(path) && hasPulseProductTable(readFileSync(path, 'utf8')));
}

export function parsePulsePluginList(output) {
	if (typeof output !== 'string') return { installed: false, enabled: false, path: undefined };
	for (const line of output.split(/\r?\n/)) {
		const match = line.match(/^pulse@zbs-gg\s{2,}(installed, enabled|installed, disabled)\s{2,}(\S+)\s{2,}(.+?)\s*$/);
		if (match) return { installed: true, enabled: match[1] === 'installed, enabled', version: match[2], path: match[3] };
	}
	return { installed: false, enabled: false, path: undefined };
}

export function parseCodexMarketplaceList(output, marketplace = 'zbs-gg') {
	if (typeof output !== 'string') return { configured: false, root: undefined };
	for (const line of output.split(/\r?\n/)) {
		const match = line.match(/^(\S+)\s{2,}(.+?)\s*$/);
		if (match?.[1] === marketplace) return { configured: true, root: match[2] };
	}
	return { configured: false, root: undefined };
}

export function sameCodexMarketplaceRoot(left, right, { platformServices = defaultPlatformServices } = {}) {
	try {
		const leftPath = platformServices.resolvePath(left);
		const rightPath = platformServices.resolvePath(right);
		if (!platformServices.isAbsolutePath(leftPath) || !platformServices.isAbsolutePath(rightPath)) return false;
		const leftIdentity = platformServices.inspectPathIdentity(leftPath, { kind: 'directory' });
		const rightIdentity = platformServices.inspectPathIdentity(rightPath, { kind: 'directory' });
		return leftIdentity.identity_token === rightIdentity.identity_token;
	} catch {
		return false;
	}
}

export function codexMarketplaceDoctorCheck({ exact, marketplace, snapshot }) {
	if (exact) return { ok: true, detail: 'exact activation-owned marketplace snapshot' };
	if (!snapshot?.ok) {
		return {
			ok: false,
			reason: snapshot?.reason ?? 'codex_marketplace_snapshot_mismatch',
			detail: snapshot?.detail ?? snapshot?.reason ?? 'Codex marketplace snapshot validation failed',
		};
	}
	if (!marketplace?.configured || !marketplace.root) {
		return {
			ok: false,
			reason: 'codex_marketplace_not_configured',
			detail: marketplace?.error ?? 'Codex marketplace zbs-gg is not configured',
		};
	}
	return {
		ok: false,
		reason: 'codex_marketplace_provenance_mismatch',
		detail: 'Codex marketplace zbs-gg points at a different source',
	};
}

const SHA256 = /^[a-f0-9]{64}$/;
const SAFE_ARTIFACT_ID = /^[a-z0-9][a-z0-9._-]{0,127}$/;

function ownerControlledTreeDigest(root, label, {
	excludeRootFiles = [], platformServices = defaultPlatformServices,
} = {}) {
	const base = resolve(root);
	const excluded = new Set(excludeRootFiles);
	const windows = platformServices.platform === 'win32';
	const rootInfo = lstatSync(base);
	const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : rootInfo.uid;
	if (!rootInfo.isDirectory() || rootInfo.isSymbolicLink() || (!windows && (rootInfo.uid !== currentUID ||
		(rootInfo.mode & 0o022) !== 0))) {
		throw new Error(`${label}_unsafe`);
	}
	const hash = createHash('sha256');
	const proofFiles = [];
	const visit = (directory, prefix = '') => {
		for (const name of readdirSync(directory).sort()) {
			const excludedFromDigest = prefix === '' && excluded.has(name);
			const path = join(directory, name);
			const relative = prefix ? `${prefix}/${name}` : name;
			const info = lstatSync(path);
			if (info.isSymbolicLink() || (!windows && (info.uid !== currentUID || (info.mode & 0o022) !== 0 ||
				(info.isFile() && info.nlink !== 1)))) throw new Error(`${label}_unsafe`);
			if (info.isDirectory()) {
				visit(path, relative);
			} else if (info.isFile()) {
				if (windows && info.size > 64 * 1024 * 1024) throw new Error(`${label}_unsafe`);
				const bytes = readFileSync(path);
				if (windows) {
					proofFiles.push({
						bytes: bytes.length,
						executable: false,
						path: relative,
						sha256: createHash('sha256').update(bytes).digest('hex'),
					});
				}
				if (!excludedFromDigest) {
					hash.update(relative);
					hash.update('\x00');
					hash.update(bytes);
					hash.update('\x00');
				}
			} else {
				throw new Error(`${label}_unsafe`);
			}
		}
	};
	visit(base);
	if (windows) {
		try {
			// Prove the exact owner-only tree in one native process. Spawning the
			// Windows adapter once per runtime entry makes a real install take minutes.
			if (proofFiles.length > 0 && typeof platformServices.validatePrivateTree === 'function') {
				platformServices.validatePrivateTree(base, { files: proofFiles });
			} else {
				platformServices.assertPrivateState(base, { kind: 'directory' });
			}
		} catch { throw new Error(`${label}_unsafe`); }
	}
	return hash.digest('hex');
}

function requireProductEdgeFile(root, relative, label) {
	const path = join(root, relative);
	if (!existsSync(path)) throw new Error(`${label}_missing`);
	try {
		defaultPlatformServices.readIntegrityFile(path, {
			owner: 'root-or-current', encoding: null, maxBytes: 64 * 1024 * 1024,
		});
	} catch {
		throw new Error(`${label}_unsafe`);
	}
	return path;
}

export function resolveSignedCodexProductEdge({
	release, activation, platformServices = defaultPlatformServices,
} = {}) {
	if (!['pulse.verified_release_manifest.v1', 'pulse.verified_release_manifest.v2'].includes(release?.schema) ||
		!SHA256.test(release.manifest_digest ?? '') ||
		typeof release.version !== 'string' || release.version.length < 1 ||
		!Number.isSafeInteger(release.epoch) || release.epoch < 1) {
		throw new Error('codex_product_release_invalid');
	}
	if (!activation || activation.kind !== 'plugin-runtime' || !SAFE_ARTIFACT_ID.test(activation.artifact_id ?? '') ||
		![activation.sha256, activation.activation_digest, activation.tree_digest].every((value) => SHA256.test(value ?? '')) ||
		activation.version !== release.version || activation.epoch !== release.epoch ||
		typeof activation.version_path !== 'string' || !isAbsolute(activation.version_path)) {
		throw new Error('codex_plugin_runtime_activation_mismatch');
	}
	const versionPath = realpathSync(resolve(activation.version_path));
	const marketplaceRoot = join(versionPath, 'marketplace');
	const pluginRoot = join(marketplaceRoot, 'plugins', 'pulse');
	const runtimeRoot = join(versionPath, 'runtime');
	const marketplacePath = requireProductEdgeFile(
		marketplaceRoot, '.agents/plugins/marketplace.json', 'codex_marketplace_manifest',
	);
	const pluginManifestPath = requireProductEdgeFile(
		pluginRoot, '.codex-plugin/plugin.json', 'codex_plugin_manifest',
	);
	for (const [relative, label] of [
		['.mcp.json', 'codex_plugin_mcp'],
		['hooks/hooks.json', 'codex_plugin_hooks'],
		['hooks/pulse-hook.mjs', 'codex_plugin_hook_launcher'],
		['runtime-locator.mjs', 'codex_plugin_runtime_locator'],
		['mcp/server.mjs', 'codex_plugin_mcp_launcher'],
	]) requireProductEdgeFile(pluginRoot, relative, label);
	const runtimePackagePath = requireProductEdgeFile(runtimeRoot, 'package.json', 'codex_runtime_package');
	requireProductEdgeFile(runtimeRoot, 'src/cli.js', 'codex_runtime_entrypoint');
	requireProductEdgeFile(runtimeRoot, 'vendor/pulse-mcp-dist/index.js', 'codex_runtime_mcp');

	let marketplace;
	let pluginManifest;
	let runtimePackage;
	try {
		marketplace = JSON.parse(readFileSync(marketplacePath, 'utf8'));
		pluginManifest = JSON.parse(readFileSync(pluginManifestPath, 'utf8'));
		runtimePackage = JSON.parse(readFileSync(runtimePackagePath, 'utf8'));
	} catch { throw new Error('codex_product_edge_json_invalid'); }
	const plugins = marketplace?.plugins;
	if (marketplace?.name !== 'zbs-gg' || !Array.isArray(plugins) || plugins.length !== 1 ||
		plugins[0]?.name !== 'pulse' || plugins[0]?.source?.source !== 'local' ||
		plugins[0]?.source?.path !== './plugins/pulse') {
		throw new Error('codex_marketplace_snapshot_invalid');
	}
	if (pluginManifest?.name !== 'pulse' || pluginManifest.version !== release.version) {
		throw new Error('codex_plugin_version_mismatch');
	}
	if (runtimePackage?.name !== '@zbs-gg/pulse' || runtimePackage.version !== release.version) {
		throw new Error('codex_runtime_release_mismatch');
	}
	return Object.freeze({
		schema: 'pulse.codex_product_edge.v1',
		release_manifest_digest: release.manifest_digest,
		release_version: release.version,
		release_epoch: release.epoch,
		plugin_runtime_artifact_id: activation.artifact_id,
		plugin_runtime_artifact_sha256: activation.sha256,
		plugin_runtime_activation_digest: activation.activation_digest,
		plugin_runtime_tree_digest: activation.tree_digest,
		marketplace_root: marketplaceRoot,
		marketplace_tree_digest: ownerControlledTreeDigest(marketplaceRoot, 'codex_marketplace_snapshot', { platformServices }),
		plugin_root: pluginRoot,
		plugin_tree_digest: ownerControlledTreeDigest(pluginRoot, 'codex_plugin_snapshot', { platformServices }),
		runtime_root: runtimeRoot,
		runtime_tree_digest: ownerControlledTreeDigest(runtimeRoot, 'codex_runtime_snapshot', { platformServices }),
	});
}

function validateCodexProductEdgeIdentity(edge) {
	if (edge?.schema !== 'pulse.codex_product_edge.v1' ||
		!SAFE_ARTIFACT_ID.test(edge.plugin_runtime_artifact_id ?? '') ||
		typeof edge.release_version !== 'string' || edge.release_version.length < 1 ||
		!Number.isSafeInteger(edge.release_epoch) || edge.release_epoch < 1 ||
		![
			edge.release_manifest_digest,
			edge.plugin_runtime_artifact_sha256,
			edge.plugin_runtime_activation_digest,
			edge.plugin_runtime_tree_digest,
			edge.marketplace_tree_digest,
			edge.plugin_tree_digest,
			edge.runtime_tree_digest,
		].every((value) => SHA256.test(value ?? '')) ||
		![edge.marketplace_root, edge.plugin_root, edge.runtime_root]
			.every((value) => typeof value === 'string' && isAbsolute(value))) {
		throw new Error('codex_product_edge_invalid');
	}
}

function marketplaceSnapshotRoot(edge, dataDir) {
	validateCodexProductEdgeIdentity(edge);
	return join(
		resolve(dataDir), 'runtime', 'codex-marketplaces',
		edge.release_manifest_digest, edge.plugin_runtime_activation_digest,
	);
}

function marketplaceSnapshotManifest(edge) {
	return {
		schema: 'pulse.codex_marketplace_snapshot.v1',
		release_manifest_digest: edge.release_manifest_digest,
		release_version: edge.release_version,
		release_epoch: edge.release_epoch,
		plugin_runtime_artifact_id: edge.plugin_runtime_artifact_id,
		plugin_runtime_artifact_sha256: edge.plugin_runtime_artifact_sha256,
		plugin_runtime_activation_digest: edge.plugin_runtime_activation_digest,
		plugin_runtime_tree_digest: edge.plugin_runtime_tree_digest,
		marketplace_tree_digest: edge.marketplace_tree_digest,
		plugin_tree_digest: edge.plugin_tree_digest,
	};
}

export function normalizePrivateTree(root) {
	const visit = (path) => {
		const info = lstatSync(path);
		if (info.isSymbolicLink()) throw new Error('codex_marketplace_snapshot_unsafe');
		if (info.isDirectory()) {
			chmodSync(path, 0o700);
			if (defaultPlatformServices.platform === 'win32') {
				try { defaultPlatformServices.assertPrivateState(path, { kind: 'directory' }); } catch {
					throw new Error('codex_marketplace_snapshot_unsafe');
				}
			}
			for (const name of readdirSync(path)) visit(join(path, name));
			return;
		}
		if (!info.isFile()) throw new Error('codex_marketplace_snapshot_unsafe');
		chmodSync(path, (info.mode & 0o111) !== 0 ? 0o700 : 0o600);
		if (defaultPlatformServices.platform === 'win32') {
			try { defaultPlatformServices.assertPrivateState(path, { kind: 'file' }); } catch {
				throw new Error('codex_marketplace_snapshot_unsafe');
			}
		}
	};
	visit(root);
}

function verifyCodexMarketplaceSnapshotAt(edge, root) {
	validateCodexProductEdgeIdentity(edge);
	const declaredRoot = resolve(root);
	const declaredInfo = lstatSync(declaredRoot);
	if (!declaredInfo.isDirectory() || declaredInfo.isSymbolicLink()) {
		throw new Error('codex_marketplace_snapshot_unsafe');
	}
	const canonicalRoot = realpathSync(declaredRoot);
	ownerControlledTreeDigest(canonicalRoot, 'codex_marketplace_snapshot_root');
	const marketplaceRoot = join(canonicalRoot, 'marketplace');
	const pluginRoot = join(marketplaceRoot, 'plugins', 'pulse');
	const manifestPath = requireProductEdgeFile(
		canonicalRoot, MARKETPLACE_SNAPSHOT_MANIFEST, 'codex_marketplace_snapshot_manifest',
	);
	let manifest;
	try { manifest = JSON.parse(readFileSync(manifestPath, 'utf8')); } catch {
		throw new Error('codex_marketplace_snapshot_manifest_invalid');
	}
	const expected = marketplaceSnapshotManifest(edge);
	if (JSON.stringify(manifest) !== JSON.stringify(expected)) {
		throw new Error('codex_marketplace_snapshot_identity_mismatch');
	}
	const marketplaceDigest = ownerControlledTreeDigest(marketplaceRoot, 'codex_marketplace_snapshot');
	const pluginDigest = ownerControlledTreeDigest(pluginRoot, 'codex_plugin_snapshot');
	if (marketplaceDigest !== edge.marketplace_tree_digest || pluginDigest !== edge.plugin_tree_digest) {
		throw new Error('codex_marketplace_snapshot_mismatch');
	}
	return {
		root: canonicalRoot,
		marketplace_root: marketplaceRoot,
		plugin_root: pluginRoot,
		marketplace_tree_digest: marketplaceDigest,
		plugin_tree_digest: pluginDigest,
		manifest,
	};
}

export function inspectCodexMarketplaceSnapshot(edge, dataDir) {
	const root = marketplaceSnapshotRoot(edge, dataDir);
	if (!existsSync(root)) {
		return { ok: false, reason: 'codex_marketplace_snapshot_missing', detail: 'managed Codex marketplace snapshot is missing' };
	}
	try {
		return { ok: true, reason: 'codex_marketplace_snapshot_exact', ...verifyCodexMarketplaceSnapshotAt(edge, root) };
	} catch (error) {
		let reason = 'codex_marketplace_snapshot_mismatch';
		if (error.message.includes('_unsafe')) reason = 'codex_marketplace_snapshot_unsafe';
		return { ok: false, reason, detail: error.message };
	}
}

export function materializeCodexMarketplaceSnapshot(edge, dataDir) {
	validateCodexProductEdgeIdentity(edge);
	if (ownerControlledTreeDigest(edge.marketplace_root, 'codex_signed_marketplace') !== edge.marketplace_tree_digest ||
		ownerControlledTreeDigest(edge.plugin_root, 'codex_signed_plugin') !== edge.plugin_tree_digest) {
		throw new Error('codex_signed_marketplace_mismatch');
	}
	const existing = inspectCodexMarketplaceSnapshot(edge, dataDir);
	if (existing.ok) return { ...existing, reused: true, repaired: false };

	const destination = marketplaceSnapshotRoot(edge, dataDir);
	const parent = dirname(destination);
	const next = join(parent, `.next-${process.pid}-${Date.now()}`);
	const previous = join(parent, `.previous-${process.pid}-${Date.now()}`);
	mkdirSync(parent, { recursive: true, mode: 0o700 });
	rmSync(next, { recursive: true, force: true });
	rmSync(previous, { recursive: true, force: true });
	let previousMoved = false;
	try {
		mkdirSync(next, { mode: 0o700 });
		cpSync(edge.marketplace_root, join(next, 'marketplace'), { recursive: true, dereference: false });
		normalizePrivateTree(join(next, 'marketplace'));
		atomicWriteJSON(join(next, MARKETPLACE_SNAPSHOT_MANIFEST), marketplaceSnapshotManifest(edge));
		verifyCodexMarketplaceSnapshotAt(edge, next);
		if (ownerControlledTreeDigest(edge.marketplace_root, 'codex_signed_marketplace') !== edge.marketplace_tree_digest ||
			ownerControlledTreeDigest(edge.plugin_root, 'codex_signed_plugin') !== edge.plugin_tree_digest) {
			throw new Error('codex_signed_marketplace_mismatch');
		}

		if (existsSync(destination)) {
			renameSync(destination, previous);
			previousMoved = true;
		}
		try {
			renameSync(next, destination);
			const verified = verifyCodexMarketplaceSnapshotAt(edge, destination);
			rmSync(previous, { recursive: true, force: true });
			return { ok: true, reason: 'codex_marketplace_snapshot_exact', ...verified, reused: false, repaired: previousMoved };
		} catch (error) {
			rmSync(destination, { recursive: true, force: true });
			if (previousMoved && existsSync(previous)) renameSync(previous, destination);
			throw error;
		}
	} finally {
		rmSync(next, { recursive: true, force: true });
		if (!previousMoved || existsSync(destination)) rmSync(previous, { recursive: true, force: true });
	}
}

export function inspectCodexPluginCompatibility(plugin, edge) {
	if (!plugin?.installed) return { ok: false, reason: 'codex_plugin_missing', detail: 'pulse@zbs-gg is not installed' };
	if (!plugin.enabled) return { ok: false, reason: 'codex_plugin_disabled', detail: 'pulse@zbs-gg is disabled' };
	if (plugin.version !== edge?.release_version) {
		return { ok: false, reason: 'codex_plugin_version_mismatch', detail: 'installed plugin version does not match the signed release' };
	}
	let digest;
	try { digest = ownerControlledTreeDigest(plugin.path, 'codex_plugin_snapshot'); } catch (error) {
		return { ok: false, reason: 'codex_plugin_snapshot_unsafe', detail: error.message };
	}
	if (digest !== edge?.plugin_tree_digest) {
		return { ok: false, reason: 'codex_plugin_snapshot_mismatch', detail: 'installed plugin bytes do not match the signed release' };
	}
	return { ok: true, reason: 'codex_plugin_exact', detail: `pulse@zbs-gg ${plugin.version}`, digest };
}

function runtimeRoot(dataDir) {
  return join(dataDir, 'runtime', 'codex');
}

function codexProductWorkspaceDigest(canonicalPath) {
  return createHash('sha256')
    .update('pulse-codex-product-locator-v1\x00')
    .update(canonicalPath)
    .digest('hex');
}

export function readProductLocator({ productHome = join(homedir(), '.pulse'), binding }) {
	if (!binding?.workspace?.canonical_path) throw new Error('Product locator binding is invalid');
	const path = join(resolve(productHome), 'product-locators.json');
	const info = lstatSync(path);
	const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
	if (!info.isFile() || info.isSymbolicLink() || info.uid !== currentUID ||
		(info.mode & 0o077) !== 0 || info.size > 1024 * 1024) {
		throw new Error('Product locator is unsafe');
	}
	const locator = JSON.parse(readFileSync(path, 'utf8'));
	const workspaceDigest = codexProductWorkspaceDigest(binding.workspace.canonical_path);
	if (locator?.schema !== 'pulse.product_locators.v1' ||
		!locator.entries || typeof locator.entries !== 'object' || Array.isArray(locator.entries) ||
		Object.keys(locator).some((name) => !['schema', 'entries'].includes(name))) {
		throw new Error('Product locator is invalid');
	}
	const entry = locator.entries[workspaceDigest];
	const allowed = ['anchor_path', 'data_dir', 'registry_path', 'public_key_path', 'trust_mode', 'workspace_digest'];
	if (!entry) throw new Error('Product locator is missing for this workspace');
	if (entry.workspace_digest !== workspaceDigest ||
		Object.keys(entry).length !== allowed.length || Object.keys(entry).some((name) => !allowed.includes(name)) ||
		!['production', 'test'].includes(entry.trust_mode) ||
		![entry.data_dir, entry.registry_path, entry.public_key_path, entry.anchor_path]
			.every((value) => typeof value === 'string' && isAbsolute(value))) {
		throw new Error('Product locator is invalid for this workspace');
	}
	return { path, entry };
}

export function writeProductLocator({
	productHome = join(homedir(), '.pulse'), binding, dataDir, registryPath, publicKeyPath, anchorPath,
	trustMode = 'production',
}) {
	if (!binding?.workspace?.canonical_path || !['production', 'test'].includes(trustMode) ||
		![dataDir, registryPath, publicKeyPath, anchorPath]
			.every((value) => typeof value === 'string' && isAbsolute(value))) {
		throw new Error('Product locator input is invalid');
	}
	const path = join(resolve(productHome), 'product-locators.json');
	let current = { schema: 'pulse.product_locators.v1', entries: {} };
	if (existsSync(path)) {
		const info = lstatSync(path);
		const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
		if (!info.isFile() || info.isSymbolicLink() || info.uid !== currentUID || (info.mode & 0o077) !== 0) {
			throw new Error('Product locator is unsafe');
		}
		current = JSON.parse(readFileSync(path, 'utf8'));
		if (current?.schema !== 'pulse.product_locators.v1' ||
			!current.entries || typeof current.entries !== 'object' || Array.isArray(current.entries)) {
			throw new Error('Product locator is invalid');
		}
	}
	const workspaceDigest = codexProductWorkspaceDigest(binding.workspace.canonical_path);
	current.entries[workspaceDigest] = {
		workspace_digest: workspaceDigest,
		data_dir: resolve(dataDir),
		registry_path: resolve(registryPath),
		public_key_path: resolve(publicKeyPath),
		anchor_path: resolve(anchorPath),
		trust_mode: trustMode,
	};
	atomicWriteJSON(path, current);
	return path;
}

export function removeProductLocator({ productHome = join(homedir(), '.pulse'), binding }) {
	const { path } = readProductLocator({ productHome, binding });
	const locator = JSON.parse(readFileSync(path, 'utf8'));
	delete locator.entries[codexProductWorkspaceDigest(binding.workspace.canonical_path)];
	const remaining = Object.keys(locator.entries).length;
	if (remaining === 0) rmSync(path, { force: true });
	else atomicWriteJSON(path, locator);
	return { path, remaining };
}

export function productHostAccessPath({ productHome = join(homedir(), '.pulse'), binding, host }) {
	if (!binding?.workspace?.canonical_path || !PRODUCT_HOSTS.includes(host)) {
		throw new Error('Product host access input is invalid');
	}
	return join(
		resolve(productHome), 'product-host-access',
		codexProductWorkspaceDigest(binding.workspace.canonical_path), `${host}.json`,
	);
}

function requirePrivateDirectory(path) {
	mkdirSync(path, { recursive: true, mode: 0o700 });
	const info = lstatSync(path);
	const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
	if (!info.isDirectory() || info.isSymbolicLink() || info.uid !== currentUID || (info.mode & 0o077) !== 0) {
		throw new Error('Product host access directory is unsafe');
	}
}

function readProductHostAccessPath(path, workspaceDigest, host) {
	const info = lstatSync(path);
	const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
	if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.uid !== currentUID ||
		(info.mode & 0o077) !== 0 || info.size > 2048) {
		throw new Error('Product host access marker is unsafe');
	}
	const record = JSON.parse(readFileSync(path, 'utf8'));
	if (record?.schema !== 'pulse.product_host_access.v1' || record.host !== host ||
		record.workspace_digest !== workspaceDigest ||
		Object.keys(record).sort().join('\0') !== ['host', 'schema', 'workspace_digest'].sort().join('\0')) {
		throw new Error('Product host access marker is invalid');
	}
	return record;
}

export function readProductHostAccess({ productHome = join(homedir(), '.pulse'), binding, host }) {
	const path = productHostAccessPath({ productHome, binding, host });
	return {
		path,
		record: readProductHostAccessPath(path, codexProductWorkspaceDigest(binding.workspace.canonical_path), host),
	};
}

export function writeProductHostAccess({ productHome = join(homedir(), '.pulse'), binding, host }) {
	const path = productHostAccessPath({ productHome, binding, host });
	requirePrivateDirectory(resolve(productHome));
	requirePrivateDirectory(join(resolve(productHome), 'product-host-access'));
	requirePrivateDirectory(dirname(path));
	atomicWriteJSON(path, {
		schema: 'pulse.product_host_access.v1',
		workspace_digest: codexProductWorkspaceDigest(binding.workspace.canonical_path),
		host,
	});
	readProductHostAccess({ productHome, binding, host });
	return path;
}

export function removeProductHostAccess({ productHome = join(homedir(), '.pulse'), binding, host }) {
	const path = productHostAccessPath({ productHome, binding, host });
	if (existsSync(path)) readProductHostAccess({ productHome, binding, host });
	rmSync(path, { force: true });
	const root = join(resolve(productHome), 'product-host-access');
	const workspaceDirectory = dirname(path);
	const workspaceMarkers = existsSync(workspaceDirectory)
		? readdirSync(workspaceDirectory).filter((name) => PRODUCT_HOSTS.some((candidate) => name === `${candidate}.json`))
		: [];
	if (workspaceMarkers.length === 0) rmSync(workspaceDirectory, { recursive: true, force: true });
	let remainingForHost = 0;
	if (existsSync(root)) {
		for (const workspaceDigest of readdirSync(root)) {
			if (!/^[a-f0-9]{64}$/.test(workspaceDigest)) throw new Error('Product host access directory is invalid');
			const candidate = join(root, workspaceDigest, `${host}.json`);
			if (!existsSync(candidate)) continue;
			readProductHostAccessPath(candidate, workspaceDigest, host);
			remainingForHost += 1;
		}
		if (readdirSync(root).length === 0) rmSync(root, { recursive: true, force: true });
	}
	return {
		path,
		remaining_for_host: remainingForHost,
		remaining_for_workspace: workspaceMarkers.length,
	};
}

export function readCodexProductLocator({ codexHome, binding }) {
	if (!binding?.workspace?.canonical_path) throw new Error('Codex product locator binding is invalid');
	const path = join(resolve(codexHome), 'pulse', 'product-locators.json');
	const info = lstatSync(path);
	const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
	if (!info.isFile() || info.isSymbolicLink() || info.uid !== currentUID ||
		(info.mode & 0o077) !== 0 || info.size > 1024 * 1024) {
		throw new Error('Codex product locator is unsafe');
	}
	const locator = JSON.parse(readFileSync(path, 'utf8'));
	const workspaceDigest = codexProductWorkspaceDigest(binding.workspace.canonical_path);
	if (locator?.schema !== 'pulse.codex_product_locators.v1' ||
		!locator.entries || typeof locator.entries !== 'object' || Array.isArray(locator.entries) ||
		Object.keys(locator).some((name) => !['schema', 'entries'].includes(name))) {
		throw new Error('Codex product locator is invalid');
	}
	const entry = locator.entries[workspaceDigest];
	const allowed = ['anchor_path', 'data_dir', 'registry_path', 'public_key_path', 'trust_mode', 'workspace_digest'];
	if (!entry) throw new Error('Codex product locator is missing for this workspace');
	if (entry.workspace_digest !== workspaceDigest ||
		Object.keys(entry).length !== allowed.length || Object.keys(entry).some((name) => !allowed.includes(name)) ||
		!['production', 'test'].includes(entry.trust_mode) ||
		![entry.data_dir, entry.registry_path, entry.public_key_path, entry.anchor_path]
			.every((value) => typeof value === 'string' && isAbsolute(value))) {
		throw new Error('Codex product locator is invalid for this workspace');
	}
	return { path, entry };
}

export function writeCodexProductLocator({
	codexHome, binding, dataDir, registryPath, publicKeyPath, anchorPath, trustMode = 'production',
}) {
	if (!binding?.workspace?.canonical_path || !['production', 'test'].includes(trustMode) ||
		![dataDir, registryPath, publicKeyPath, anchorPath].every((value) => typeof value === 'string' && isAbsolute(value))) {
    throw new Error('Codex product locator input is invalid');
  }
  const directory = join(resolve(codexHome), 'pulse');
  const path = join(directory, 'product-locators.json');
  let current = { schema: 'pulse.codex_product_locators.v1', entries: {} };
  if (existsSync(path)) {
    const info = lstatSync(path);
    const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
    if (!info.isFile() || info.isSymbolicLink() || info.uid !== currentUID || (info.mode & 0o077) !== 0) {
      throw new Error('Codex product locator is unsafe');
    }
    current = JSON.parse(readFileSync(path, 'utf8'));
    if (current?.schema !== 'pulse.codex_product_locators.v1' ||
        !current.entries || typeof current.entries !== 'object' || Array.isArray(current.entries)) {
      throw new Error('Codex product locator is invalid');
    }
  }
  const workspaceDigest = codexProductWorkspaceDigest(binding.workspace.canonical_path);
  current.entries[workspaceDigest] = {
    workspace_digest: workspaceDigest,
    data_dir: resolve(dataDir),
		registry_path: resolve(registryPath),
		public_key_path: resolve(publicKeyPath),
		anchor_path: resolve(anchorPath),
		trust_mode: trustMode,
  };
  atomicWriteJSON(path, current);
  return path;
}

export function removeCodexProductLocator({ codexHome, binding }) {
	const { path } = readCodexProductLocator({ codexHome, binding });
	const locator = JSON.parse(readFileSync(path, 'utf8'));
	const workspaceDigest = codexProductWorkspaceDigest(binding.workspace.canonical_path);
	delete locator.entries[workspaceDigest];
	const remaining = Object.keys(locator.entries).length;
	if (remaining === 0) {
		rmSync(path, { force: true });
	} else {
		atomicWriteJSON(path, locator);
	}
	return { path, remaining };
}

function runtimeTreeDigest(root) {
	return ownerControlledTreeDigest(root, 'codex_runtime', {
		excludeRootFiles: [RUNTIME_MANIFEST],
	});
}

export function includeRuntimePath(sourceRoot, sourcePath) {
  const nativeRelative = relative(resolve(sourceRoot), resolve(sourcePath));
  if (nativeRelative === '') return true;
  if (nativeRelative === '..' || nativeRelative.startsWith(`..${sep}`) || isAbsolute(nativeRelative)) return false;
  const normalized = nativeRelative.split(sep).join('/');
  const top = normalized.split('/')[0];
  if (!new Set(['src', 'vendor', 'node_modules', 'package.json', 'LICENSE']).has(top)) return false;
  if (top === 'vendor' && normalized !== 'vendor' && !normalized.startsWith('vendor/pulse-mcp-dist')) return false;
  return !normalized.endsWith('.test.js') && !normalized.endsWith('.map') && !normalized.endsWith('.d.ts');
}

function committedRuntimeDigest(dataDir) {
	const path = join(dataDir, 'runtime', 'product-daemon.json');
	if (!existsSync(path)) {
		throw new Error('Codex runtime activation journal is missing; refusing ambiguous recovery');
	}
	const info = lstatSync(path);
	const currentUID = typeof process.geteuid === 'function' ? process.geteuid() : info.uid;
	if (!info.isFile() || info.isSymbolicLink() || info.uid !== currentUID ||
		(info.mode & 0o077) !== 0 || info.size > 8192) {
		throw new Error('Codex runtime activation journal is unsafe; refusing ambiguous recovery');
	}
	const activation = JSON.parse(readFileSync(path, 'utf8'));
	const allowedV2 = ['activated_at', 'daemon_digest', 'daemon_path', 'runtime_path', 'runtime_tree_digest', 'schema'];
	const allowedV3 = [
		'activated_at',
		'daemon_activation_digest', 'daemon_artifact_id', 'daemon_artifact_sha256', 'daemon_digest', 'daemon_path', 'daemon_tree_digest',
		'embedder_runtime_activation_digest', 'embedder_runtime_artifact_id', 'embedder_runtime_artifact_sha256', 'embedder_runtime_tree_digest',
		'model_activation_digest', 'model_artifact_id', 'model_artifact_sha256', 'model_tree_digest',
		'runtime_path', 'runtime_tree_digest', 'schema',
	];
	const allowedV4 = [
		'activated_at',
		'daemon_activation_digest', 'daemon_artifact_id', 'daemon_artifact_sha256', 'daemon_digest', 'daemon_path', 'daemon_tree_digest',
		'embedder_runtime_activation_digest', 'embedder_runtime_artifact_id', 'embedder_runtime_artifact_sha256', 'embedder_runtime_tree_digest',
		'model_activation_digest', 'model_artifact_id', 'model_artifact_sha256', 'model_tree_digest',
		'plugin_runtime_activation_digest', 'plugin_runtime_artifact_id', 'plugin_runtime_artifact_sha256', 'plugin_runtime_tree_digest',
		'plugin_tree_digest', 'release_epoch', 'release_manifest_digest', 'release_version',
		'runtime_path', 'runtime_tree_digest', 'schema',
	];
	const allowedBySchema = {
		'pulse.product_activation.v2': allowedV2,
		'pulse.product_activation.v3': allowedV3,
		'pulse.product_activation.v4': allowedV4,
	};
	const allowed = allowedBySchema[activation?.schema];
	const expectedRuntimePath = join(runtimeRoot(dataDir), 'current', 'src', 'cli.js');
	if (!allowed || !activation || typeof activation !== 'object' || Array.isArray(activation) ||
		Object.keys(activation).length !== allowed.length ||
		Object.keys(activation).some((name) => !allowed.includes(name)) ||
		resolve(activation.runtime_path ?? '') !== resolve(expectedRuntimePath) ||
		!isAbsolute(activation.daemon_path ?? '') ||
		![activation.runtime_tree_digest, activation.daemon_digest]
			.every((value) => /^[a-f0-9]{64}$/.test(value ?? '')) ||
		typeof activation.activated_at !== 'string' || Number.isNaN(Date.parse(activation.activated_at))) {
		throw new Error('Codex runtime activation journal is invalid; refusing ambiguous recovery');
	}
	return activation.runtime_tree_digest;
}

function recoverInterruptedRuntimeInstall(dataDir) {
	const root = runtimeRoot(dataDir);
	const current = join(root, 'current');
	const previous = join(root, 'previous');
	if (!existsSync(previous)) return { action: 'none' };

	const committedDigest = committedRuntimeDigest(dataDir);
	const currentRuntime = inspectRuntimeRoot(current);
	const previousRuntime = inspectRuntimeRoot(previous);
	if (currentRuntime.ok && currentRuntime.digest === committedDigest) {
		// Activation publication is the commit point. A crash before finalize only
		// left an obsolete rollback tree behind; never replace the committed tree.
		rmSync(previous, { recursive: true, force: true });
		return { action: 'finalized_current', runtime: currentRuntime };
	}
	if (previousRuntime.ok && previousRuntime.digest === committedDigest) {
		// The process died before publishing the staged current tree. Restore the
		// exact tree still named by the durable activation receipt.
		const abandoned = join(root, `abandoned-${process.pid}-${Date.now()}`);
		if (existsSync(current)) renameSync(current, abandoned);
		try {
			renameSync(previous, current);
		} catch (error) {
			if (existsSync(abandoned) && !existsSync(current)) renameSync(abandoned, current);
			throw error;
		}
		rmSync(abandoned, { recursive: true, force: true });
		return { action: 'rolled_back_previous', runtime: previousRuntime };
	}
	throw new Error('Codex runtime activation journal matches neither current nor previous; refusing ambiguous recovery');
}

export function installCodexRuntime(packageRoot, dataDir, options = {}) {
  const root = runtimeRoot(dataDir);
  const current = join(root, 'current');
  const previous = join(root, 'previous');
  const next = join(root, `next-${process.pid}-${Date.now()}`);
  mkdirSync(root, { recursive: true, mode: 0o700 });
	// `previous` is an activation journal entry. If the installer process died
	// before finalize/rollback, the durable product activation decides which
	// tree committed. Ambiguous state is preserved and rejected, never guessed.
	recoverInterruptedRuntimeInstall(dataDir);
  try {
		if (options.signedEdge) {
			if (options.signedEdge.schema !== 'pulse.codex_product_edge.v1' ||
				realpathSync(resolve(packageRoot)) !== realpathSync(resolve(options.signedEdge.runtime_root)) ||
				ownerControlledTreeDigest(packageRoot, 'codex_runtime_snapshot') !== options.signedEdge.runtime_tree_digest) {
				throw new Error('codex_runtime_snapshot_mismatch');
			}
		}
    cpSync(packageRoot, next, {
      recursive: true,
      dereference: true,
      filter: (sourcePath) => includeRuntimePath(packageRoot, sourcePath),
    });
    if (!existsSync(join(next, 'node_modules'))) {
      const hoisted = resolve(packageRoot, '..', '..');
      if (basename(hoisted) !== 'node_modules' || !existsSync(hoisted)) {
        throw new Error('Codex runtime dependencies are unavailable');
      }
      mkdirSync(join(next, 'node_modules'), { recursive: true, mode: 0o700 });
      for (const name of readdirSync(hoisted)) {
        if (name === '.bin' || name.startsWith('@')) continue;
        cpSync(join(hoisted, name), join(next, 'node_modules', name), {
          recursive: true,
          dereference: true,
        });
      }
      for (const scope of readdirSync(hoisted).filter((name) => name.startsWith('@') && name !== '@zbs-gg')) {
        cpSync(join(hoisted, scope), join(next, 'node_modules', scope), {
          recursive: true,
          dereference: true,
        });
      }
    }
    const entrypoint = join(next, 'src', 'cli.js');
    if (!existsSync(entrypoint) || !existsSync(join(next, 'vendor', 'pulse-mcp-dist', 'index.js'))) {
      throw new Error('Codex runtime package is incomplete');
    }
    const digest = runtimeTreeDigest(next);
    const packageJSON = JSON.parse(readFileSync(join(next, 'package.json'), 'utf8'));
		if (options.signedEdge && (digest !== options.signedEdge.runtime_tree_digest ||
			packageJSON.version !== options.signedEdge.release_version)) {
			throw new Error('codex_runtime_snapshot_mismatch');
		}
		atomicWriteJSON(join(next, RUNTIME_MANIFEST), options.signedEdge ? {
			schema: 'pulse.codex_runtime.v2',
			package_version: packageJSON.version,
			tree_digest: digest,
			installed_at: (options.now ?? new Date()).toISOString(),
			entrypoint: 'src/cli.js',
			release_manifest_digest: options.signedEdge.release_manifest_digest,
			release_version: options.signedEdge.release_version,
			release_epoch: options.signedEdge.release_epoch,
			plugin_runtime_artifact_id: options.signedEdge.plugin_runtime_artifact_id,
			plugin_runtime_artifact_sha256: options.signedEdge.plugin_runtime_artifact_sha256,
			plugin_runtime_activation_digest: options.signedEdge.plugin_runtime_activation_digest,
			plugin_runtime_tree_digest: options.signedEdge.plugin_runtime_tree_digest,
			plugin_tree_digest: options.signedEdge.plugin_tree_digest,
		} : {
			schema: 'pulse.codex_runtime.v1',
			package_version: packageJSON.version,
			tree_digest: digest,
			installed_at: (options.now ?? new Date()).toISOString(),
			entrypoint: 'src/cli.js',
		});
    rmSync(previous, { recursive: true, force: true });
    if (existsSync(current)) renameSync(current, previous);
    try {
      renameSync(next, current);
    } catch (error) {
      if (existsSync(previous) && !existsSync(current)) renameSync(previous, current);
      throw error;
    }
    if (options.keepPrevious !== true) rmSync(previous, { recursive: true, force: true });
    return inspectCodexRuntime(dataDir);
  } finally {
    rmSync(next, { recursive: true, force: true });
  }
}

export function finalizeCodexRuntimeInstall(dataDir) {
  rmSync(join(runtimeRoot(dataDir), 'previous'), { recursive: true, force: true });
}

export function rollbackCodexRuntimeInstall(dataDir) {
  const root = runtimeRoot(dataDir);
  const current = join(root, 'current');
  const previous = join(root, 'previous');
  rmSync(current, { recursive: true, force: true });
  if (existsSync(previous)) {
    renameSync(previous, current);
    return inspectCodexRuntime(dataDir);
  }
	return {
		ok: true,
		restored: 'absent',
		detail: 'the first staged runtime was removed; no prior runtime existed',
	};
}

function inspectRuntimeRoot(root) {
	const manifestPath = join(root, RUNTIME_MANIFEST);
  if (!existsSync(manifestPath)) return { ok: false, detail: 'trusted Codex runtime is not installed' };
  try {
    const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
		const v1 = manifest?.schema === 'pulse.codex_runtime.v1';
		const v2 = manifest?.schema === 'pulse.codex_runtime.v2' &&
			SHA256.test(manifest.release_manifest_digest ?? '') &&
			typeof manifest.release_version === 'string' && manifest.release_version === manifest.package_version &&
			Number.isSafeInteger(manifest.release_epoch) && manifest.release_epoch > 0 &&
			SAFE_ARTIFACT_ID.test(manifest.plugin_runtime_artifact_id ?? '') &&
			[
				manifest.plugin_runtime_artifact_sha256,
				manifest.plugin_runtime_activation_digest,
				manifest.plugin_runtime_tree_digest,
				manifest.plugin_tree_digest,
			].every((value) => SHA256.test(value ?? ''));
		if ((!v1 && !v2) || !SHA256.test(manifest.tree_digest ?? '') ||
				manifest.entrypoint !== 'src/cli.js') {
      throw new Error('runtime manifest is invalid');
    }
		const actual = runtimeTreeDigest(root);
    if (actual !== manifest.tree_digest) throw new Error('runtime integrity digest mismatch');
    return {
      ok: true,
      detail: `local immutable runtime ${manifest.package_version}`,
			path: join(root, manifest.entrypoint),
      digest: manifest.tree_digest,
      manifest,
    };
  } catch (error) {
    return { ok: false, detail: error.message };
  }
}

export function inspectCodexRuntime(dataDir) {
	return inspectRuntimeRoot(join(runtimeRoot(dataDir), 'current'));
}

export function inspectCodexRuntimeAt(runtimePath) {
	if (typeof runtimePath !== 'string' || !isAbsolute(runtimePath) ||
		basename(runtimePath) !== 'cli.js' || basename(dirname(runtimePath)) !== 'src') {
		return { ok: false, detail: 'trusted Codex runtime path is invalid' };
	}
	const inspected = inspectRuntimeRoot(dirname(dirname(resolve(runtimePath))));
	if (inspected.ok && inspected.path !== resolve(runtimePath)) {
		return { ok: false, detail: 'trusted Codex runtime entrypoint is invalid' };
	}
	return inspected;
}
