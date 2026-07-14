import assert from 'node:assert/strict';
import { existsSync, mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  finalizeCodexRuntimeInstall,
  inspectCodexRuntime,
  installCodexRuntime,
  migrateLegacyPulseHookFiles,
	parsePulsePluginList,
	pulseProductMcpShadowFiles,
	readCodexProductLocator,
	removeCodexProductLocator,
	rollbackCodexRuntimeInstall,
	writeCodexProductLocator,
} from './codex-install.js';
import { recordCodexHookReadiness } from './codex-hooks.js';

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

test('product locators are exact and workspace removal preserves other connections', () => {
	const root = mkdtempSync(join(tmpdir(), 'pulse-codex-locators-'));
	try {
		const codexHome = join(root, 'codex');
		const bindingA = { workspace: { canonical_path: join(root, 'workspace-a') } };
		const bindingB = { workspace: { canonical_path: join(root, 'workspace-b') } };
		const locatorArgs = (binding, suffix) => ({
			codexHome, binding, dataDir: join(root, `data-${suffix}`),
			registryPath: join(root, `registry-${suffix}.json`), publicKeyPath: join(root, `key-${suffix}.pem`),
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
      ['UserPromptSubmit', 'prompt_context'], ['PostToolUse', 'write_receipt'], ['Stop', 'turn_finalize'],
    ]) {
      assert.equal(recordCodexHookReadiness(event, resolved, {
        dataDir: root, hooksDigest: 'a'.repeat(64), turnProof: 'c'.repeat(64), milestone,
        now: new Date('2026-07-14T10:00:00Z'),
      }), true);
    }
    const receipt = JSON.parse(readFileSync(join(root, 'codex-hook-readiness.json'), 'utf8'));
    assert.equal(receipt.schema, 'pulse.codex_hook_readiness.v1');
    assert.equal(receipt.binding_digest, 'b'.repeat(64));
    assert.equal(receipt.repository_id, 'repository-ready');
    assert.equal(receipt.turn_proof, 'c'.repeat(64));
    assert.deepEqual(Object.keys(receipt.milestones).sort(), [
      'prompt_context', 'turn_finalize', 'write_receipt',
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
