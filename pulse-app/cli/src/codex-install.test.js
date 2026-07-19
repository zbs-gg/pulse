import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import {
	chmodSync, cpSync, existsSync, linkSync, lstatSync, mkdtempSync, mkdirSync,
	readFileSync, realpathSync, rmSync, symlinkSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
	codexProductActivationReady,
	codexMarketplaceDoctorCheck,
  finalizeCodexRuntimeInstall,
  inspectCodexRuntime,
	inspectCodexMarketplaceSnapshot,
	inspectCodexPluginCompatibility,
  installCodexRuntime,
	materializeCodexMarketplaceSnapshot,
	migrateLegacyPulseHookFiles,
	parseCodexMarketplaceList,
	parsePulsePluginList,
	pulseProductMcpShadowFiles,
	productHostAccessPath,
	readCodexProductLocator,
	readProductHostAccess,
	readProductLocator,
	removeCodexProductLocator,
	removeProductHostAccess,
	removeProductLocator,
	resolveSignedCodexProductEdge,
	rollbackCodexRuntimeInstall,
	sameCodexMarketplaceRoot,
	writeCodexProductLocator,
	writeProductHostAccess,
	writeProductLocator,
} from './codex-install.js';
import { recordCodexHookReadiness } from './codex-hooks.js';

test('Codex product activation is not ready when exact marketplace provenance fails', () => {
	const checks = Object.fromEntries([
		'presence_trust', 'authority', 'codex', 'host_access', 'plugin', 'marketplace', 'plugin_mcp',
		'mcp_shadow', 'legacy_hooks', 'binding', 'runtime', 'activation', 'vault', 'capture',
	].map((name) => [name, { ok: true }]));
	assert.equal(codexProductActivationReady({ checks }), true);
	checks.marketplace.ok = false;
	assert.equal(codexProductActivationReady({ checks }), false);
});

test('doctor reports missing marketplace registration after validating the snapshot itself', () => {
	assert.deepEqual(codexMarketplaceDoctorCheck({
		exact: false,
		marketplace: { configured: false },
		snapshot: { ok: true, reason: 'codex_marketplace_snapshot_exact' },
	}), {
		ok: false,
		reason: 'codex_marketplace_not_configured',
		detail: 'Codex marketplace zbs-gg is not configured',
	});
});

test('doctor reports marketplace provenance mismatch after validating the snapshot itself', () => {
	assert.deepEqual(codexMarketplaceDoctorCheck({
		exact: false,
		marketplace: { configured: true, root: '/tmp/wrong-marketplace' },
		snapshot: { ok: true, reason: 'codex_marketplace_snapshot_exact' },
	}), {
		ok: false,
		reason: 'codex_marketplace_provenance_mismatch',
		detail: 'Codex marketplace zbs-gg points at a different source',
	});
});

function writeProductActivation(dataDir, runtime, runtimeDigest = runtime.digest) {
	const path = join(dataDir, 'runtime', 'product-daemon.json');
	mkdirSync(join(dataDir, 'runtime'), { recursive: true, mode: 0o700 });
	writeFileSync(path, `${JSON.stringify({
		schema: 'pulse.product_activation.v2',
		runtime_path: runtime.path,
		runtime_tree_digest: runtimeDigest,
		daemon_path: join(dataDir, 'bin', 'pulse-product-daemon-test'),
		daemon_digest: 'd'.repeat(64),
		activated_at: '2026-07-14T10:00:00Z',
	})}\n`, { mode: 0o600 });
}

test('legacy hook file migration is atomic and preserves unrelated handlers', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-codex-install-'));
  try {
    const codexHome = join(root, 'home');
    const workspace = join(root, 'workspace');
    mkdirSync(codexHome, { recursive: true });
    mkdirSync(join(workspace, '.codex'), { recursive: true });
    const path = join(codexHome, 'hooks.json');
    writeFileSync(path, JSON.stringify({ hooks: { Stop: [{ hooks: [
      { type: 'command', command: 'pulse hook stop' },
      { type: 'command', command: 'echo keep' },
    ] }] } }));
    const result = migrateLegacyPulseHookFiles({ cwd: workspace, codexHome });
    assert.equal(result.removed, 1);
    const saved = JSON.parse(readFileSync(path, 'utf8'));
    assert.equal(saved.hooks.Stop[0].hooks[0].command, 'echo keep');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('Codex config detects only an explicit pulse-product MCP shadow', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-codex-shadow-'));
  try {
    const codexHome = join(root, 'home');
    mkdirSync(codexHome, { recursive: true });
    writeFileSync(join(codexHome, 'config.toml'), '[mcp_servers.pulse]\ncommand = "keep"\n');
    assert.deepEqual(pulseProductMcpShadowFiles({ cwd: root, codexHome }), []);
    writeFileSync(join(codexHome, 'config.toml'), '[mcp_servers."pulse-product"]\ncommand = "shadow"\n');
    assert.deepEqual(pulseProductMcpShadowFiles({ cwd: root, codexHome }), [join(codexHome, 'config.toml')]);
    writeFileSync(join(codexHome, 'config.toml'), 'mcp_servers."pulse-product" = { command = "shadow" }\n');
    assert.deepEqual(pulseProductMcpShadowFiles({ cwd: root, codexHome }), [join(codexHome, 'config.toml')]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('plugin list parser accepts only enabled pulse from zbs-gg', () => {
  const parsed = parsePulsePluginList(
    'PLUGIN          STATUS              VERSION  PATH\n' +
    'pulse@zbs-gg   installed, enabled  0.7.0    /tmp/cache/pulse\n',
  );
	assert.deepEqual(parsed, { installed: true, enabled: true, version: '0.7.0', path: '/tmp/cache/pulse' });
	assert.deepEqual(
		parsePulsePluginList('pulse@zbs-gg  installed, disabled  0.7.0  /tmp/cache/pulse\n'),
		{ installed: true, enabled: false, version: '0.7.0', path: '/tmp/cache/pulse' },
	);
  assert.equal(parsePulsePluginList('pulse@other  installed, enabled  1.0.0  /tmp/no').enabled, false);
});

test('marketplace parser exposes exact local provenance instead of accepting a floating source', () => {
	assert.deepEqual(parseCodexMarketplaceList(
		'MARKETPLACE  ROOT\nzbs-gg      /private/var/pulse/artifacts/pulse-plugin-runtime/versions/signed/marketplace\n',
	), {
		configured: true,
		root: '/private/var/pulse/artifacts/pulse-plugin-runtime/versions/signed/marketplace',
	});
	assert.deepEqual(parseCodexMarketplaceList('MARKETPLACE  ROOT\nopenai  /tmp/openai\n'), {
		configured: false, root: undefined,
	});
});

test('marketplace roots compare filesystem identity instead of Windows path spelling', () => {
	const identities = new Map([
		['C:\\Users\\RUNNER~1\\marketplace', 'volume:file-id'],
		['C:\\Users\\runneradmin\\marketplace', 'volume:file-id'],
		['C:\\Users\\runneradmin\\other', 'volume:other-id'],
	]);
	const calls = [];
	const platformServices = {
		resolvePath: (value) => value,
		isAbsolutePath: (value) => identities.has(value),
		inspectPathIdentity: (path, options) => {
			calls.push({ path, options });
			return { identity_token: identities.get(path) };
		},
	};
	assert.equal(sameCodexMarketplaceRoot(
		'C:\\Users\\RUNNER~1\\marketplace', 'C:\\Users\\runneradmin\\marketplace', { platformServices },
	), true);
	assert.equal(sameCodexMarketplaceRoot(
		'C:\\Users\\RUNNER~1\\marketplace', 'C:\\Users\\runneradmin\\other', { platformServices },
	), false);
	assert.equal(sameCodexMarketplaceRoot('relative', 'also-relative', { platformServices }), false);
	assert.deepEqual(calls.map(({ options }) => options), [
		{ kind: 'directory' }, { kind: 'directory' },
		{ kind: 'directory' }, { kind: 'directory' },
	]);
});

function writeSignedProductEdgeFixture(root, { releaseVersion = '0.7.0', pluginVersion = releaseVersion } = {}) {
	const versionPath = join(root, 'pulse-plugin-runtime', 'versions', 'signed-fixture');
	const marketplaceRoot = join(versionPath, 'marketplace');
	const pluginRoot = join(marketplaceRoot, 'plugins', 'pulse');
	const runtimeRoot = join(versionPath, 'runtime');
	mkdirSync(join(marketplaceRoot, '.agents', 'plugins'), { recursive: true, mode: 0o700 });
	mkdirSync(join(pluginRoot, '.codex-plugin'), { recursive: true, mode: 0o700 });
	mkdirSync(join(pluginRoot, 'hooks'), { recursive: true, mode: 0o700 });
	mkdirSync(join(pluginRoot, 'mcp'), { recursive: true, mode: 0o700 });
	mkdirSync(join(runtimeRoot, 'src'), { recursive: true, mode: 0o700 });
	mkdirSync(join(runtimeRoot, 'vendor', 'pulse-mcp-dist'), { recursive: true, mode: 0o700 });
	mkdirSync(join(runtimeRoot, 'node_modules'), { recursive: true, mode: 0o700 });
	writeFileSync(join(marketplaceRoot, '.agents', 'plugins', 'marketplace.json'), JSON.stringify({
		name: 'zbs-gg', plugins: [{ name: 'pulse', source: { source: 'local', path: './plugins/pulse' } }],
	}));
	writeFileSync(join(pluginRoot, '.codex-plugin', 'plugin.json'), JSON.stringify({
		name: 'pulse', version: pluginVersion, mcpServers: './.mcp.json',
	}));
	writeFileSync(join(pluginRoot, '.mcp.json'), JSON.stringify({
		mcpServers: { 'pulse-product': { command: 'node', args: ['${PLUGIN_ROOT}/mcp/server.mjs'] } },
	}));
	writeFileSync(join(pluginRoot, 'hooks', 'hooks.json'), JSON.stringify({ hooks: {} }));
	writeFileSync(join(pluginRoot, 'hooks', 'pulse-hook.mjs'), 'export {}\n');
	writeFileSync(join(pluginRoot, 'runtime-locator.mjs'), 'export {}\n');
	writeFileSync(join(pluginRoot, 'mcp', 'server.mjs'), 'export {}\n');
	writeFileSync(join(runtimeRoot, 'package.json'), JSON.stringify({ name: '@zbs-gg/pulse', version: releaseVersion }));
	writeFileSync(join(runtimeRoot, 'src', 'cli.js'), '#!/usr/bin/env node\n');
	writeFileSync(join(runtimeRoot, 'vendor', 'pulse-mcp-dist', 'index.js'), 'export {};\n');
	const release = {
		schema: 'pulse.verified_release_manifest.v1', manifest_digest: 'a'.repeat(64),
		version: releaseVersion, epoch: 8, artifacts: {},
	};
	const activation = {
		artifact_id: 'pulse-plugin-runtime', sha256: 'b'.repeat(64),
		activation_digest: 'c'.repeat(64), tree_digest: 'd'.repeat(64),
		version: releaseVersion, epoch: 8, version_path: versionPath, kind: 'plugin-runtime',
	};
	return { activation, marketplaceRoot, pluginRoot, release, runtimeRoot, versionPath };
}

test('signed Codex product edge accepts only the exact release-owned plugin and runtime snapshot', () => {
	const root = mkdtempSync(join(tmpdir(), 'pulse-codex-signed-edge-'));
	try {
		const fixture = writeSignedProductEdgeFixture(root);
		const edge = resolveSignedCodexProductEdge({
			release: fixture.release,
			activation: fixture.activation,
		});
		assert.equal(edge.marketplace_root, realpathSync(fixture.marketplaceRoot));
		assert.equal(edge.runtime_root, realpathSync(fixture.runtimeRoot));
		assert.equal(edge.release_manifest_digest, fixture.release.manifest_digest);
		assert.equal(edge.release_version, '0.7.0');
		assert.match(edge.plugin_tree_digest, /^[a-f0-9]{64}$/);
		assert.match(edge.marketplace_tree_digest, /^[a-f0-9]{64}$/);
		assert.match(edge.runtime_tree_digest, /^[a-f0-9]{64}$/);
		const catalogEdge = resolveSignedCodexProductEdge({
			release: { ...fixture.release, schema: 'pulse.verified_release_manifest.v2' },
			activation: fixture.activation,
		});
		assert.equal(catalogEdge.release_manifest_digest, edge.release_manifest_digest);

		const installedPlugin = join(root, 'installed-plugin');
		cpSync(fixture.pluginRoot, installedPlugin, { recursive: true });
		assert.equal(inspectCodexPluginCompatibility({
			installed: true, enabled: true, version: '0.7.0', path: installedPlugin,
		}, edge).ok, true);
		writeFileSync(join(installedPlugin, '.mcp.json'), '{"tampered":true}\n');
		assert.equal(inspectCodexPluginCompatibility({
			installed: true, enabled: true, version: '0.7.0', path: installedPlugin,
		}, edge).reason, 'codex_plugin_snapshot_mismatch');

		const wrongVersion = writeSignedProductEdgeFixture(join(root, 'wrong'), { pluginVersion: '0.7.1' });
		assert.throws(
			() => resolveSignedCodexProductEdge({ release: wrongVersion.release, activation: wrongVersion.activation }),
			/codex_plugin_version_mismatch/,
		);
	} finally {
		rmSync(root, { recursive: true, force: true });
	}
});

test('Windows proves each signed Codex product tree in one native operation', () => {
	const root = mkdtempSync(join(tmpdir(), 'pulse-codex-windows-edge-'));
	try {
		const fixture = writeSignedProductEdgeFixture(root);
		const proofs = [];
		const platformServices = {
			platform: 'win32',
			assertPrivateState: () => { throw new Error('per-entry directory proof is forbidden'); },
			readPrivateFile: () => { throw new Error('per-entry file proof is forbidden'); },
			validatePrivateTree: (path, { files }) => {
				for (const file of files) {
					const bytes = readFileSync(join(path, file.path));
					assert.equal(file.bytes, bytes.length);
					assert.equal(file.executable, false);
					assert.equal(file.sha256, createHash('sha256').update(bytes).digest('hex'));
				}
				proofs.push({ path, files: files.length });
				return { bytes: files.reduce((total, file) => total + file.bytes, 0), files: files.length };
			},
		};
		const edge = resolveSignedCodexProductEdge({
			release: fixture.release,
			activation: fixture.activation,
			platformServices,
		});
		assert.match(edge.runtime_tree_digest, /^[a-f0-9]{64}$/);
		assert.deepEqual(proofs.map(({ path }) => path), [
			realpathSync(fixture.marketplaceRoot),
			realpathSync(fixture.pluginRoot),
			realpathSync(fixture.runtimeRoot),
		]);
	} finally {
		rmSync(root, { recursive: true, force: true });
	}
});

test('managed Codex marketplace snapshot isolates host mutations from the signed artifact and repairs byte drift', () => {
	const root = mkdtempSync(join(tmpdir(), 'pulse-codex-marketplace-snapshot-'));
	try {
		const fixture = writeSignedProductEdgeFixture(root);
		const edge = resolveSignedCodexProductEdge({ release: fixture.release, activation: fixture.activation });
		const dataDir = join(root, 'data');
		const signedPluginMode = lstatSync(fixture.pluginRoot).mode & 0o777;

		const created = materializeCodexMarketplaceSnapshot(edge, dataDir);
		assert.equal(created.reused, false);
		assert.notEqual(created.marketplace_root, fixture.marketplaceRoot);
		assert.equal(inspectCodexMarketplaceSnapshot(edge, dataDir).ok, true);

		for (const relative of ['', 'hooks', 'mcp', '.codex-plugin']) {
			chmodSync(join(created.plugin_root, relative), 0o755);
		}
		assert.equal(inspectCodexMarketplaceSnapshot(edge, dataDir).ok, true,
			'Codex may widen read/execute bits on its disposable snapshot');
		assert.equal(lstatSync(fixture.pluginRoot).mode & 0o777, signedPluginMode,
			'the immutable signed artifact must never be passed to or mutated by Codex');

		const reused = materializeCodexMarketplaceSnapshot(edge, dataDir);
		assert.equal(reused.reused, true);
		writeFileSync(join(created.plugin_root, '.mcp.json'), '{"tampered":true}\n');
		assert.equal(inspectCodexMarketplaceSnapshot(edge, dataDir).reason, 'codex_marketplace_snapshot_mismatch');

		const repaired = materializeCodexMarketplaceSnapshot(edge, dataDir);
		assert.equal(repaired.reused, false);
		assert.equal(repaired.repaired, true);
		assert.equal(inspectCodexMarketplaceSnapshot(edge, dataDir).ok, true);
		assert.equal(lstatSync(fixture.pluginRoot).mode & 0o777, signedPluginMode);

		chmodSync(repaired.plugin_root, 0o777);
		assert.equal(inspectCodexMarketplaceSnapshot(edge, dataDir).reason, 'codex_marketplace_snapshot_unsafe');
		chmodSync(repaired.plugin_root, 0o755);
		const hardlink = join(root, 'snapshot-hardlink');
		linkSync(join(repaired.plugin_root, '.mcp.json'), hardlink);
		assert.equal(inspectCodexMarketplaceSnapshot(edge, dataDir).reason, 'codex_marketplace_snapshot_unsafe');
		rmSync(hardlink);
		rmSync(join(repaired.plugin_root, '.mcp.json'));
		symlinkSync(join(fixture.pluginRoot, '.mcp.json'), join(repaired.plugin_root, '.mcp.json'));
		assert.equal(inspectCodexMarketplaceSnapshot(edge, dataDir).reason, 'codex_marketplace_snapshot_unsafe');
	} finally {
		rmSync(root, { recursive: true, force: true });
	}
});

test('signed Codex runtime records the complete release edge and preserves last-known-good on drift', () => {
	const root = mkdtempSync(join(tmpdir(), 'pulse-codex-signed-runtime-'));
	try {
		const fixture = writeSignedProductEdgeFixture(root);
		const edge = resolveSignedCodexProductEdge({ release: fixture.release, activation: fixture.activation });
		const dataDir = join(root, 'data');
		const installed = installCodexRuntime(fixture.runtimeRoot, dataDir, {
			now: new Date('2026-07-15T10:00:00Z'), signedEdge: edge,
		});
		assert.equal(installed.manifest.schema, 'pulse.codex_runtime.v2');
		assert.equal(installed.manifest.release_manifest_digest, fixture.release.manifest_digest);
		assert.equal(installed.manifest.release_version, fixture.release.version);
		assert.equal(installed.manifest.release_epoch, fixture.release.epoch);
		assert.equal(installed.manifest.plugin_runtime_activation_digest, fixture.activation.activation_digest);
		assert.equal(installed.manifest.plugin_runtime_tree_digest, fixture.activation.tree_digest);
		assert.equal(installed.manifest.plugin_tree_digest, edge.plugin_tree_digest);

		const before = installed.digest;
		writeFileSync(join(fixture.runtimeRoot, 'src', 'cli.js'), '#!/usr/bin/env node\n// drifted after signing\n');
		assert.throws(
			() => installCodexRuntime(fixture.runtimeRoot, dataDir, { keepPrevious: true, signedEdge: edge }),
			/codex_runtime_snapshot_mismatch/,
		);
		assert.equal(inspectCodexRuntime(dataDir).digest, before);
	} finally {
		rmSync(root, { recursive: true, force: true });
	}
});

test('product locators are exact and workspace removal preserves other connections', () => {
	const root = mkdtempSync(join(tmpdir(), 'pulse-codex-locators-'));
	try {
		const codexHome = join(root, 'codex');
		const bindingA = { workspace: { canonical_path: join(root, 'workspace-a') } };
		const bindingB = { workspace: { canonical_path: join(root, 'workspace-b') } };
		const locatorArgs = (binding, suffix) => ({
			codexHome, binding, dataDir: join(root, `data-${suffix}`),
			registryPath: join(root, `registry-${suffix}.json`), publicKeyPath: join(root, `key-${suffix}.pem`),
			anchorPath: join(root, `anchor-${suffix}.json`),
			trustMode: 'test',
		});
		writeCodexProductLocator(locatorArgs(bindingA, 'a'));
		const path = writeCodexProductLocator(locatorArgs(bindingB, 'b'));
		assert.equal(removeCodexProductLocator({ codexHome, binding: bindingA }).remaining, 1);
		assert.equal(readCodexProductLocator({ codexHome, binding: bindingB }).entry.data_dir, join(root, 'data-b'));

		const tampered = JSON.parse(readFileSync(path, 'utf8'));
		const entry = Object.values(tampered.entries)[0];
		entry.extra_authority = 'unsafe';
		writeFileSync(path, JSON.stringify(tampered), { mode: 0o600 });
		assert.throws(
			() => readCodexProductLocator({ codexHome, binding: bindingB }),
			/Codex product locator is invalid/,
		);
	} finally {
		rmSync(root, { recursive: true, force: true });
	}
});

test('host-neutral product locator carries one workspace authority across all harnesses', () => {
	const root = mkdtempSync(join(tmpdir(), 'pulse-product-locators-'));
	try {
		const productHome = join(root, '.pulse');
		const binding = { workspace: { canonical_path: join(root, 'workspace') } };
		const path = writeProductLocator({
			productHome, binding, dataDir: join(root, 'data'),
			registryPath: join(root, 'registry.json'), publicKeyPath: join(root, 'key.pem'),
			anchorPath: join(root, 'anchor.json'), trustMode: 'test',
		});
		assert.equal(path, join(productHome, 'product-locators.json'));
		assert.equal(readProductLocator({ productHome, binding }).entry.data_dir, join(root, 'data'));
		assert.equal(JSON.parse(readFileSync(path, 'utf8')).schema, 'pulse.product_locators.v1');
		assert.equal(removeProductLocator({ productHome, binding }).remaining, 0);
		assert.equal(existsSync(path), false);
	} finally {
		rmSync(root, { recursive: true, force: true });
	}
});

test('workspace host-access markers disconnect one harness without breaking other workspaces', () => {
	const root = mkdtempSync(join(tmpdir(), 'pulse-product-host-access-'));
	try {
		const productHome = join(root, '.pulse');
		const bindingA = { workspace: { canonical_path: join(root, 'workspace-a') } };
		const bindingB = { workspace: { canonical_path: join(root, 'workspace-b') } };
		for (const [binding, host] of [
			[bindingA, 'claude-code'], [bindingA, 'cursor'], [bindingB, 'claude-code'],
		]) writeProductHostAccess({ productHome, binding, host });

		assert.equal(readProductHostAccess({ productHome, binding: bindingA, host: 'claude-code' }).record.host, 'claude-code');
		assert.equal(existsSync(productHostAccessPath({ productHome, binding: bindingA, host: 'cursor' })), true);
		const first = removeProductHostAccess({ productHome, binding: bindingA, host: 'claude-code' });
		assert.equal(first.remaining_for_workspace, 1);
		assert.equal(first.remaining_for_host, 1);
		assert.equal(readProductHostAccess({ productHome, binding: bindingA, host: 'cursor' }).record.host, 'cursor');
		const second = removeProductHostAccess({ productHome, binding: bindingA, host: 'cursor' });
		assert.equal(second.remaining_for_workspace, 0);
		assert.equal(second.remaining_for_host, 0);
		const final = removeProductHostAccess({ productHome, binding: bindingB, host: 'claude-code' });
		assert.equal(final.remaining_for_host, 0);
	} finally {
		rmSync(root, { recursive: true, force: true });
	}
});

test('host-access markers delegate Windows ACL proof, atomic write, and removal to platform services', () => {
	const root = mkdtempSync(join(tmpdir(), 'pulse-product-host-access-platform-'));
	try {
		const productHome = join(root, '.pulse');
		const binding = { workspace: { canonical_path: join(root, 'workspace') } };
		const calls = [];
		const platformServices = {
			resolvePath: (path) => path,
			ensurePrivateDirectory: (path) => {
				calls.push(`directory:${path}`);
				mkdirSync(path, { recursive: true, mode: 0o700 });
			},
			atomicWritePrivateFile: (path, bytes, options) => {
				calls.push(`write:${path}:${options.ensureParent}:${options.maxBytes}`);
				writeFileSync(path, bytes, { mode: 0o600 });
			},
			readPrivateFile: (path, options) => {
				calls.push(`read:${path}:${options.maxBytes}`);
				return readFileSync(path, options.encoding);
			},
			removePrivateFile: (path, options) => {
				calls.push(`remove:${path}:${options.missing}`);
				rmSync(path, { force: true });
			},
		};
		const path = writeProductHostAccess({ productHome, binding, host: 'codex', platformServices });
		assert.equal(readProductHostAccess({ productHome, binding, host: 'codex', platformServices }).record.host, 'codex');
		removeProductHostAccess({ productHome, binding, host: 'codex', platformServices });
		assert.deepEqual(calls.filter((call) => call.startsWith('directory:')).length, 3);
		assert.equal(calls.some((call) => call === `write:${path}:false:2048`), true);
		assert.equal(calls.filter((call) => call === `read:${path}:2048`).length, 3);
		assert.equal(calls.includes(`remove:${path}:true`), true);
	} finally {
		rmSync(root, { recursive: true, force: true });
	}
});

test('successful trusted hook writes a content-free global readiness receipt', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-codex-ready-'));
  try {
    const resolved = {
      binding: {
        binding_digest: 'b'.repeat(64), resolver_epoch: 4,
        workspace: { repository_id: 'repository-ready', canonical_path: '/workspace/ready' },
      },
      runtime: { data_dir: '/vault/ready' },
    };
    for (const [event, milestone] of [
		  ['SessionStart', 'session_context'], ['UserPromptSubmit', 'prompt_context'],
		  ['PostToolUse', 'write_receipt'], ['Stop', 'turn_finalize'],
    ]) {
      assert.equal(recordCodexHookReadiness(event, resolved, {
			  dataDir: root, hooksDigest: 'a'.repeat(64), sessionProof: 'e'.repeat(64),
			  turnProof: 'c'.repeat(64), milestone,
			  environment: { PULSE_TRUST_MODE: 'test', PULSE_TEST_CODEX_LIFECYCLE_ATTESTOR: '1' },
        now: new Date('2026-07-14T10:00:00Z'),
      }), true);
    }
    const receipt = JSON.parse(readFileSync(join(root, 'codex-hook-readiness.json'), 'utf8'));
		assert.equal(receipt.schema, 'pulse.codex_hook_readiness.v2');
    assert.equal(receipt.binding_digest, 'b'.repeat(64));
    assert.equal(receipt.repository_id, 'repository-ready');
		assert.equal(receipt.turn_proof, 'c'.repeat(64));
		assert.equal(receipt.session_proof, 'e'.repeat(64));
		assert.deepEqual(Object.keys(receipt.milestones).sort(), [
		  'prompt_context', 'session_context', 'turn_finalize', 'write_receipt',
    ]);
    assert.doesNotMatch(JSON.stringify(receipt), /\/workspace\/ready/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('Codex runtime install is local, atomic, and integrity checked', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-codex-runtime-'));
  try {
    const source = join(root, 'source');
    const dataDir = join(root, 'data');
    mkdirSync(join(source, 'src'), { recursive: true });
    mkdirSync(join(source, 'vendor', 'pulse-mcp-dist'), { recursive: true });
    mkdirSync(join(source, 'node_modules'), { recursive: true });
    writeFileSync(join(source, 'package.json'), JSON.stringify({ name: '@zbs-gg/pulse', version: '0.7.0' }));
    writeFileSync(join(source, 'src', 'cli.js'), '#!/usr/bin/env node\n');
    writeFileSync(join(source, 'vendor', 'pulse-mcp-dist', 'index.js'), 'export {};\n');
    const installed = installCodexRuntime(source, dataDir, { now: new Date('2026-07-14T10:00:00Z') });
    assert.equal(installed.ok, true);
    assert.match(installed.digest, /^[a-f0-9]{64}$/);
		chmodSync(installed.path, 0o720);
		assert.equal(inspectCodexRuntime(dataDir).ok, false);
		chmodSync(installed.path, 0o700);
		assert.equal(inspectCodexRuntime(dataDir).ok, true);
    writeFileSync(installed.path, '#!/usr/bin/env node\n// tampered\n');
    assert.equal(inspectCodexRuntime(dataDir).ok, false);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('runtime upgrade keeps a last-known-good tree until activation commits', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-codex-runtime-rollback-'));
  try {
    const source = join(root, 'source');
    const dataDir = join(root, 'data');
    mkdirSync(join(source, 'src'), { recursive: true });
    mkdirSync(join(source, 'vendor', 'pulse-mcp-dist'), { recursive: true });
    mkdirSync(join(source, 'node_modules'), { recursive: true });
    writeFileSync(join(source, 'package.json'), JSON.stringify({ name: '@zbs-gg/pulse', version: '0.7.0' }));
    writeFileSync(join(source, 'src', 'cli.js'), '#!/usr/bin/env node\n// v1\n');
    writeFileSync(join(source, 'vendor', 'pulse-mcp-dist', 'index.js'), 'export {};\n');
    const first = installCodexRuntime(source, dataDir);

    writeFileSync(join(source, 'src', 'cli.js'), '#!/usr/bin/env node\n// v2\n');
    const second = installCodexRuntime(source, dataDir, { keepPrevious: true });
    assert.notEqual(second.digest, first.digest);
    const rolledBack = rollbackCodexRuntimeInstall(dataDir);
    assert.equal(rolledBack.digest, first.digest);

    installCodexRuntime(source, dataDir, { keepPrevious: true });
    finalizeCodexRuntimeInstall(dataDir);
    assert.equal(inspectCodexRuntime(dataDir).ok, true);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('first runtime activation can roll back to the original absent state', () => {
	const root = mkdtempSync(join(tmpdir(), 'pulse-codex-runtime-first-rollback-'));
	try {
		const source = join(root, 'source');
		const dataDir = join(root, 'data');
		mkdirSync(join(source, 'src'), { recursive: true });
		mkdirSync(join(source, 'vendor', 'pulse-mcp-dist'), { recursive: true });
		mkdirSync(join(source, 'node_modules'), { recursive: true });
		writeFileSync(join(source, 'package.json'), JSON.stringify({ name: '@zbs-gg/pulse', version: '0.7.0' }));
		writeFileSync(join(source, 'src', 'cli.js'), '#!/usr/bin/env node\n// first staged runtime\n');
		writeFileSync(join(source, 'vendor', 'pulse-mcp-dist', 'index.js'), 'export {};\n');

		installCodexRuntime(source, dataDir, { keepPrevious: true });
		const rolledBack = rollbackCodexRuntimeInstall(dataDir);

		assert.equal(rolledBack.ok, true);
		assert.equal(rolledBack.restored, 'absent');
		assert.equal(inspectCodexRuntime(dataDir).ok, false);
	} finally {
		rmSync(root, { recursive: true, force: true });
	}
});

test('runtime install recovers an interrupted activation before staging another tree', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-codex-runtime-recover-'));
  try {
    const source = join(root, 'source');
    const dataDir = join(root, 'data');
    mkdirSync(join(source, 'src'), { recursive: true });
    mkdirSync(join(source, 'vendor', 'pulse-mcp-dist'), { recursive: true });
    mkdirSync(join(source, 'node_modules'), { recursive: true });
    writeFileSync(join(source, 'package.json'), JSON.stringify({ name: '@zbs-gg/pulse', version: '0.7.0' }));
    writeFileSync(join(source, 'vendor', 'pulse-mcp-dist', 'index.js'), 'export {};\n');

		writeFileSync(join(source, 'src', 'cli.js'), '#!/usr/bin/env node\n// v1\n');
		const first = installCodexRuntime(source, dataDir);
		writeProductActivation(dataDir, first);
		writeFileSync(join(source, 'src', 'cli.js'), '#!/usr/bin/env node\n// v2 interrupted\n');
		const interrupted = installCodexRuntime(source, dataDir, { keepPrevious: true });
		assert.notEqual(interrupted.digest, first.digest);

    writeFileSync(join(source, 'src', 'cli.js'), '#!/usr/bin/env node\n// v3\n');
    installCodexRuntime(source, dataDir, { keepPrevious: true });
    const rolledBack = rollbackCodexRuntimeInstall(dataDir);
    assert.equal(rolledBack.digest, first.digest);
  } finally {
    rmSync(root, { recursive: true, force: true });
	}
});

test('runtime install preserves a committed activation after a crash before finalize', () => {
	const root = mkdtempSync(join(tmpdir(), 'pulse-codex-runtime-committed-recover-'));
	try {
		const source = join(root, 'source');
		const dataDir = join(root, 'data');
		mkdirSync(join(source, 'src'), { recursive: true });
		mkdirSync(join(source, 'vendor', 'pulse-mcp-dist'), { recursive: true });
		mkdirSync(join(source, 'node_modules'), { recursive: true });
		writeFileSync(join(source, 'package.json'), JSON.stringify({ name: '@zbs-gg/pulse', version: '0.7.0' }));
		writeFileSync(join(source, 'vendor', 'pulse-mcp-dist', 'index.js'), 'export {};\n');

		writeFileSync(join(source, 'src', 'cli.js'), '#!/usr/bin/env node\n// v1\n');
		const first = installCodexRuntime(source, dataDir);
		writeProductActivation(dataDir, first);
		writeFileSync(join(source, 'src', 'cli.js'), '#!/usr/bin/env node\n// v2 committed before crash\n');
		const committed = installCodexRuntime(source, dataDir, { keepPrevious: true });
		writeProductActivation(dataDir, committed);

		// Simulate the next activation failing after it staged v3. Its rollback
		// must return to committed v2, not the obsolete v1 journal tree.
		writeFileSync(join(source, 'src', 'cli.js'), '#!/usr/bin/env node\n// v3 fails later\n');
		const staged = installCodexRuntime(source, dataDir, { keepPrevious: true });
		assert.notEqual(staged.digest, committed.digest);
		const rolledBack = rollbackCodexRuntimeInstall(dataDir);
		assert.equal(rolledBack.ok, true);
		assert.equal(rolledBack.digest, committed.digest);
		assert.notEqual(rolledBack.digest, first.digest);
	} finally {
		rmSync(root, { recursive: true, force: true });
	}
});

test('runtime install preserves both trees when activation recovery is ambiguous', () => {
	const root = mkdtempSync(join(tmpdir(), 'pulse-codex-runtime-ambiguous-recover-'));
	try {
		const source = join(root, 'source');
		const dataDir = join(root, 'data');
		mkdirSync(join(source, 'src'), { recursive: true });
		mkdirSync(join(source, 'vendor', 'pulse-mcp-dist'), { recursive: true });
		mkdirSync(join(source, 'node_modules'), { recursive: true });
		writeFileSync(join(source, 'package.json'), JSON.stringify({ name: '@zbs-gg/pulse', version: '0.7.0' }));
		writeFileSync(join(source, 'vendor', 'pulse-mcp-dist', 'index.js'), 'export {};\n');

		writeFileSync(join(source, 'src', 'cli.js'), '#!/usr/bin/env node\n// v1\n');
		const first = installCodexRuntime(source, dataDir);
		writeProductActivation(dataDir, first);
		writeFileSync(join(source, 'src', 'cli.js'), '#!/usr/bin/env node\n// v2\n');
		const second = installCodexRuntime(source, dataDir, { keepPrevious: true });
		writeProductActivation(dataDir, second, 'e'.repeat(64));
		const currentPath = join(dataDir, 'runtime', 'codex', 'current', 'src', 'cli.js');
		const previousPath = join(dataDir, 'runtime', 'codex', 'previous', 'src', 'cli.js');
		const currentBefore = readFileSync(currentPath);
		const previousBefore = readFileSync(previousPath);

		assert.throws(
			() => installCodexRuntime(source, dataDir, { keepPrevious: true }),
			/matches neither current nor previous/,
		);
		assert.equal(existsSync(currentPath), true);
		assert.equal(existsSync(previousPath), true);
		assert.deepEqual(readFileSync(currentPath), currentBefore);
		assert.deepEqual(readFileSync(previousPath), previousBefore);
	} finally {
		rmSync(root, { recursive: true, force: true });
	}
});
