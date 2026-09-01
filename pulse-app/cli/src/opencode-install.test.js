import assert from 'node:assert/strict';
import { existsSync, mkdtempSync, mkdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import {
  inspectOpenCodeIntegration,
  installOpenCodeIntegration,
  OPENCODE_PLUGIN_ENTRY,
  opencodeConfigPaths,
  readOpenCodeOptions,
  parseOpenCodeConfig,
  previewOpenCodeIntegration,
  removeOpenCodeIntegration,
  writeOpenCodeOptions,
} from './opencode-install.js';

function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'pulse-opencode-install.'));
  const home = join(root, 'home');
  const source = join(root, 'plugin');
  mkdirSync(home, { recursive: true });
  mkdirSync(source, { recursive: true });
  writeFileSync(join(source, 'pulse.js'), 'export const Pulse = async () => ({});\n');
  writeFileSync(join(source, 'runtime-locator.mjs'), 'export {};\n');
  return { root, home, source };
}

test('OpenCode install preserves JSONC providers, MCP and existing plugins and is idempotent', () => {
  const fx = fixture();
  try {
    const paths = opencodeConfigPaths(fx.home);
    mkdirSync(paths.root, { recursive: true });
    const original = `{
  // keep provider and secret-shaped settings byte-for-byte
  "provider": { "local": { "options": { "apiKey": "do-not-print" } } },
  "mcp": { "graph": { "type": "local", "command": ["graph"] } },
  "plugin": [
    "existing-plugin",
  ],
}
`;
    writeFileSync(paths.jsonc, original, { mode: 0o600 });

    const preview = previewOpenCodeIntegration({ home: fx.home });
    assert.equal(preview.config_path, paths.jsonc);
    assert.deepEqual(preview.before_plugin, ['existing-plugin']);
    assert.deepEqual(preview.after_plugin, ['existing-plugin', OPENCODE_PLUGIN_ENTRY]);

    const first = installOpenCodeIntegration(fx.source, { home: fx.home });
    assert.equal(first.ready, true);
    assert.equal(first.reused, false);
    const installed = readFileSync(paths.jsonc, 'utf8');
    assert.match(installed, /do-not-print/);
    assert.deepEqual(parseOpenCodeConfig(installed).plugin, ['existing-plugin', OPENCODE_PLUGIN_ENTRY]);

    const second = installOpenCodeIntegration(fx.source, { home: fx.home });
    assert.equal(second.ready, true);
    assert.equal(second.reused, true);
    assert.equal(readFileSync(paths.jsonc, 'utf8'), installed);
  } finally {
    rmSync(fx.root, { recursive: true, force: true });
  }
});

test('OpenCode install creates JSON config and uninstall preserves unrelated settings', () => {
  const fx = fixture();
  try {
    const paths = opencodeConfigPaths(fx.home);
    installOpenCodeIntegration(fx.source, { home: fx.home });
    assert.equal(inspectOpenCodeIntegration({ home: fx.home }).ready, true);
    assert.equal(parseOpenCodeConfig(readFileSync(paths.json, 'utf8')).plugin[0], OPENCODE_PLUGIN_ENTRY);

    const removed = removeOpenCodeIntegration({ home: fx.home });
    assert.equal(removed.removed, true);
    assert.deepEqual(parseOpenCodeConfig(readFileSync(paths.json, 'utf8')).plugin, []);
    assert.equal(existsSync(paths.pluginRoot), false);
  } finally {
    rmSync(fx.root, { recursive: true, force: true });
  }
});

test('OpenCode install rejects conflicting global JSON and JSONC without mutation', () => {
  const fx = fixture();
  try {
    const paths = opencodeConfigPaths(fx.home);
    mkdirSync(paths.root, { recursive: true });
    writeFileSync(paths.json, '{}\n');
    writeFileSync(paths.jsonc, '{}\n');
    assert.throws(
      () => installOpenCodeIntegration(fx.source, { home: fx.home }),
      (error) => error.code === 'opencode_config_conflict',
    );
    assert.equal(existsSync(paths.pluginRoot), false);
    assert.equal(readFileSync(paths.json, 'utf8'), '{}\n');
    assert.equal(readFileSync(paths.jsonc, 'utf8'), '{}\n');
  } finally {
    rmSync(fx.root, { recursive: true, force: true });
  }
});

test('OpenCode install rejects invalid JSONC and missing loader source without writes', () => {
  const fx = fixture();
  try {
    const paths = opencodeConfigPaths(fx.home);
    mkdirSync(paths.root, { recursive: true });
    writeFileSync(paths.jsonc, '{ "plugin": [ }\n');
    assert.throws(() => installOpenCodeIntegration(fx.source, { home: fx.home }));
    assert.equal(existsSync(paths.pluginRoot), false);

    writeFileSync(paths.jsonc, '{}\n');
    rmSync(join(fx.source, 'pulse.js'));
    assert.throws(
      () => installOpenCodeIntegration(fx.source, { home: fx.home }),
      (error) => error.code === 'opencode_loader_source_missing',
    );
    assert.equal(readFileSync(paths.jsonc, 'utf8'), '{}\n');
  } finally {
    rmSync(fx.root, { recursive: true, force: true });
  }
});

test('OpenCode project options are private, exact, and reject extra content', () => {
  const fx = fixture();
  const workspaceDigest = 'a'.repeat(64);
  try {
    const written = writeOpenCodeOptions({
      productHome: join(fx.home, '.pulse'), workspaceDigest, funFacts: 'small-model',
    });
    assert.deepEqual(readOpenCodeOptions({
      productHome: join(fx.home, '.pulse'), workspaceDigest,
    }), { path: written.path, fun_facts: 'small-model', configured: true });
    assert.equal(statSync(written.path).mode & 0o077, 0);

    writeFileSync(written.path, `${JSON.stringify({
      schema: 'pulse.opencode_options.v1', workspace_digest: workspaceDigest,
      fun_facts: 'small-model', raw_prompt: 'forbidden',
    })}\n`, { mode: 0o600 });
    assert.throws(() => readOpenCodeOptions({
      productHome: join(fx.home, '.pulse'), workspaceDigest,
    }), (error) => error.code === 'opencode_options_invalid');
  } finally {
    rmSync(fx.root, { recursive: true, force: true });
  }
});
