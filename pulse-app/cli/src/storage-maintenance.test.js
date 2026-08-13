import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, readFileSync, symlinkSync, writeFileSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import {
  __storageMaintenanceTest,
  cleanPulseStorage,
  inspectPulseStorage,
  writeStorageHomeSnapshot,
} from './storage-maintenance.js';

function file(path, bytes = 16) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, Buffer.alloc(bytes, 1), { mode: 0o600 });
}

function fixture() {
  const dataDir = mkdtempSync(join(tmpdir(), 'pulse-storage-'));
  const artifacts = join(dataDir, 'artifacts');
  const makeVersion = (artifact, digest, bytes) => {
    const path = join(artifacts, artifact, 'versions', digest);
    file(join(path, 'payload'), bytes);
    return path;
  };
  const active = makeVersion('pulse-0.8-daemon', 'a'.repeat(64), 100);
  const previous = makeVersion('pulse-0.8-daemon', 'b'.repeat(64), 90);
  const obsolete = makeVersion('pulse-0.8-daemon', 'c'.repeat(64), 80);
  const legacy = makeVersion('pulse-0.7-daemon', 'd'.repeat(64), 70);
  file(join(artifacts, 'downloads', 'download.verified'), 60);
  file(join(dataDir, 'dev-builds', 'candidate', 'runtime'), 50);
  file(join(dataDir, 'vaults', 'personal', 'pulse.db'), 500);
  const generation = (epoch, path) => ({
    schema: 'pulse.artifact_activation_set.v1', epoch, version: '0.8.1', manifest_digest: String(epoch).padStart(64, '0'),
    activations: { daemon: { version_path: path } },
  });
  writeFileSync(join(artifacts, 'artifact-generation-authority.json'), `${JSON.stringify({
    schema: 'pulse.artifact_generation_authority.v1', anti_rollback_floor: 22,
    active: generation(22, active), previous: generation(21, previous),
  })}\n`, { mode: 0o600 });
  return { dataDir, active, previous, obsolete, legacy };
}

test('storage report protects active memory and both release generations', () => {
  const value = fixture();
  const report = inspectPulseStorage({ dataDir: value.dataDir, commands: '' });
  assert.equal(report.active_release.epoch, 22);
  assert.equal(report.previous_release.epoch, 21);
  assert.equal(report.candidates.some((item) => item.path === value.active), false);
  assert.equal(report.candidates.some((item) => item.path === value.previous), false);
  assert.equal(report.candidates.some((item) => item.path === value.obsolete), true);
  assert.equal(report.candidates.some((item) => item.path.endsWith('pulse.db')), false);
  assert.ok(report.reclaimable_bytes >= 260);
});

test('storage report supports a clean first install without a previous release', () => {
  const value = fixture();
  const authorityPath = join(value.dataDir, 'artifacts', 'artifact-generation-authority.json');
  const authority = JSON.parse(readFileSync(authorityPath, 'utf8'));
  authority.previous = null;
  writeFileSync(authorityPath, `${JSON.stringify(authority)}\n`, { mode: 0o600 });
  const report = inspectPulseStorage({ dataDir: value.dataDir, commands: '' });
  assert.equal(report.previous_release, null);
  assert.equal(report.candidates.some((item) => item.path === value.active), false);
});

test('storage clean uses exact digest and rolls back when verification fails', async () => {
  const value = fixture();
  const report = inspectPulseStorage({ dataDir: value.dataDir, commands: '' });
  await assert.rejects(() => cleanPulseStorage({
    dataDir: value.dataDir, planDigest: '0'.repeat(64), commands: '',
  }), /storage_plan_changed/);
  await assert.rejects(() => cleanPulseStorage({
    dataDir: value.dataDir, planDigest: report.plan_digest, commands: '', verify: () => false,
  }), /storage_post_clean_verification_failed/);
  assert.equal(existsSync(value.obsolete), true);
  assert.equal(existsSync(value.legacy), true);
});

test('storage clean removes only generated candidates and writes a receipt', async () => {
  const value = fixture();
  const outside = mkdtempSync(join(tmpdir(), 'pulse-storage-linked-target-'));
  const outsideFile = join(outside, 'must-survive');
  file(outsideFile, 40);
  symlinkSync(outsideFile, join(value.dataDir, 'dev-builds', 'candidate', 'linked-tool'));
  const report = inspectPulseStorage({ dataDir: value.dataDir, commands: '' });
  const receipt = await cleanPulseStorage({
    dataDir: value.dataDir, planDigest: report.plan_digest, commands: '', verify: () => true,
  });
  assert.equal(existsSync(value.active), true);
  assert.equal(existsSync(value.previous), true);
  assert.equal(existsSync(value.obsolete), false);
  assert.equal(existsSync(value.legacy), false);
  assert.equal(existsSync(outsideFile), true);
  assert.ok(receipt.freed_bytes >= 260);
  assert.equal(JSON.parse(readFileSync(join(value.dataDir, 'receipts', 'latest-storage-clean.json'))).schema,
    'pulse.storage_clean_result.v1');
});

test('storage report does not follow symlinked generated roots', () => {
  const value = fixture();
  const outside = mkdtempSync(join(tmpdir(), 'pulse-storage-outside-'));
  symlinkSync(outside, join(value.dataDir, 'artifacts', 'pulse-unsafe'));
  const report = inspectPulseStorage({ dataDir: value.dataDir, commands: '' });
  assert.equal(report.candidates.some((item) => item.relative_path.includes('pulse-unsafe')), false);
});

test('Memory Home snapshot accepts a separate trusted vault but rejects a vault symlink', () => {
  const value = fixture();
  const vault = mkdtempSync(join(tmpdir(), 'pulse-storage-vault-'));
  const snapshot = writeStorageHomeSnapshot({ dataDir: value.dataDir, vaultDataDir: vault });
  assert.equal(snapshot.schema, 'pulse.storage_home.v1');
  assert.equal(JSON.parse(readFileSync(join(vault, 'storage-home.json'))).active_epoch, 22);

  const linkedVault = join(mkdtempSync(join(tmpdir(), 'pulse-storage-vault-link-')), 'vault');
  symlinkSync(vault, linkedVault);
  assert.throws(
    () => writeStorageHomeSnapshot({ dataDir: value.dataDir, vaultDataDir: linkedVault }),
    /storage_home_vault_invalid/,
  );
});

test('Memory Home does not interpret synthetic Windows modes as POSIX access', () => {
  assert.equal(__storageMaintenanceTest.privateMode(0o777, 'win32'), true);
  assert.equal(__storageMaintenanceTest.privateMode(0o666, 'win32'), true);
  assert.equal(__storageMaintenanceTest.privateMode(0o777, 'linux'), false);
  assert.equal(__storageMaintenanceTest.privateMode(0o666, 'linux'), false);
});
