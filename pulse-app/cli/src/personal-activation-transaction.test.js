import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const source = readFileSync(new URL('./cli.js', import.meta.url), 'utf8');

function between(start, end) {
  const from = source.indexOf(start);
  const to = source.indexOf(end, from + start.length);
  assert.notEqual(from, -1, `missing ${start}`);
  assert.notEqual(to, -1, `missing ${end}`);
  return source.slice(from, to);
}

test('host-neutral and legacy Codex activation share one Core transaction', () => {
  const publicCore = between(
    'async function activatePersonalInstallCore(binding)',
    'function personalInstallHostRegistry',
  );
  const legacyCodex = between(
    'async function connectCodexActivation',
    'async function disconnectCodex',
  );

  assert.match(source, /async function activatePersonalInstallCoreTransaction\(binding\)/);
  assert.match(publicCore, /activatePersonalInstallCoreTransaction\(binding\)/);
  assert.match(legacyCodex, /activatePersonalInstallCoreTransaction\(resolved\.binding\)/);
  for (const duplicateCoreMutation of [
    'ensureManagedProductRuntime(',
    'installCodexRuntime(',
    'startVaultRuntime(',
    'writeProductDaemonActivation(',
  ]) {
    assert.equal(
      legacyCodex.includes(duplicateCoreMutation),
      false,
      `legacy Codex adapter duplicated ${duplicateCoreMutation}`,
    );
  }
});

test('the shared Core transaction does not grant access to a host adapter', () => {
  const transaction = between(
    'async function activatePersonalInstallCoreTransaction(binding)',
    'async function activatePersonalInstallCore(binding)',
  );
  assert.doesNotMatch(transaction, /writeProductHostAccess\(/);
});

test('Codex adapter completes fallible file preflight before creating a plugin backup', () => {
  const registry = between(
    'function personalInstallHostRegistry(targets)',
    'function personalInstallCoreHealth',
  );
  const codexAdapter = registry.slice(registry.indexOf('codex: {'), registry.indexOf('cursor: {'));
  const filePreflight = codexAdapter.indexOf('const localFiles = snapshotLocalFiles(');
  const pluginSnapshot = codexAdapter.indexOf('const transaction = snapshotCodexHostActivation(');
  assert.notEqual(filePreflight, -1);
  assert.notEqual(pluginSnapshot, -1);
  assert.ok(filePreflight < pluginSnapshot, 'plugin backup was created before fallible file preflight');
});

test('Windows install prewarm budget stays separate from the first-value gate', () => {
  const prewarm = between(
    'function personalHostWorkerPrewarmTimeout',
    'function personalInstallHostRegistry',
  );
  assert.match(prewarm, /platform === 'win32' \? 180_000 : 60_000/);
  assert.match(prewarm, /content_free: true/);
  assert.match(prewarm, /hook_worker_prewarm_failure\.v1/);
});

test('install health verifies a one-shot Memory Home session before reporting ready', () => {
  const health = between(
    'async function personalInstallCoreHealth',
    'function personalInstallDependencies',
  );
  assert.match(health, /requestHomeSession\(/);
  assert.match(health, /readSecretFromDataDir\(dataDir\)/);
  assert.match(health, /projectPersonalLiveReadiness\(readinessChecks, new Date\(\)\)/);
  assert.match(health, /reason_code: 'memory_home_unavailable'/);
  assert.doesNotMatch(health, /openHomeBrowserURL\(/);
});

test('Claude adapter grants workspace access before enabling its MCP and rolls it back on failure', () => {
  const registry = between(
    'function personalInstallHostRegistry(targets)',
    'function personalInstallCoreHealth',
  );
  const claudeAdapter = registry.slice(
    registry.indexOf("'claude-code': {"),
    registry.indexOf('codex: {'),
  );
  const accessGrant = claudeAdapter.indexOf('writeProductHostAccess({');
  const captureGrant = claudeAdapter.indexOf('writeCaptureStateFiles({');
  const pluginActivation = claudeAdapter.indexOf('activateClaudePlugin(');
  assert.notEqual(accessGrant, -1);
  assert.notEqual(captureGrant, -1);
  assert.notEqual(pluginActivation, -1);
  assert.ok(
    accessGrant < pluginActivation,
    'Claude MCP was enabled before its workspace access existed',
  );
  assert.ok(
    captureGrant < pluginActivation,
    'Claude MCP was enabled before capture was enabled for its host',
  );
  assert.match(claudeAdapter, /catch \(error\) \{[\s\S]*restoreLocalFiles\(localFiles\)/);
});

test('Claude local MCP removal does not reject a same-name server from another scope', () => {
  const removal = between(
    'function removeClaudeCodeExternalRegistration',
    'function installClaudeCode',
  );
  assert.match(removal, /\['mcp', 'remove', 'pulse', '--scope', 'local'\]/);
  assert.doesNotMatch(removal, /\['mcp', 'get', 'pulse'\]/);
});
