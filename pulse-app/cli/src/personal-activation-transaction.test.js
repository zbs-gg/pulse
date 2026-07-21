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
