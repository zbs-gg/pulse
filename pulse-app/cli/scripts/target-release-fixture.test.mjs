import assert from 'node:assert/strict';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { releaseTargetDefinition, releaseTargetIDs } from './release-builder-core.mjs';
import { buildAndInstallTargetFixture } from './target-release-fixture.mjs';

function fixtureDaemon({ outputRoot, targetID }) {
  const target = releaseTargetDefinition(targetID);
  const path = join(outputRoot, 'bin', target.daemon_name);
  mkdirSync(join(outputRoot, 'bin'), { recursive: true, mode: 0o700 });
  writeFileSync(path, target.platform === 'win32' ? 'MZ\0PULSE_FIXTURE\n' : '#!/bin/sh\nexit 0\n', { mode: 0o700 });
  chmodSync(path, 0o700);
  writeFileSync(join(outputRoot, 'pulse-target-build.json'), `${JSON.stringify({
    production_ready: false, schema: 'pulse.target_daemon_build.v2', target_id: targetID,
  })}\n`, { mode: 0o600 });
}

test('all six target fixture sets pass the signed fixture catalog and production portable installer', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-target-fixtures.'));
  try {
    for (const targetID of releaseTargetIDs()) {
      const result = await buildAndInstallTargetFixture({
        buildDaemon: fixtureDaemon,
        nativeTargetID: null,
        outputRoot: join(root, targetID),
        targetID,
      });
      assert.equal(result.release.schema, 'pulse.verified_release_manifest.v2');
      assert.equal(result.release.verification_profile.kind, 'fixture');
      assert.equal(result.receipt.artifact_count, targetID.startsWith('darwin-') ? 5 : 4);
      assert.equal(result.receipt.native_runner_match, false);
      assert.equal(result.receipt.production_ready, false);
      assert.equal(result.receipt.support_proven, false);
      assert.equal(existsSync(result.installer.manifest_path), true);
      assert.equal(existsSync(result.installer.root_key_path), true);
      assert.equal(existsSync(result.installer.asset_root), true);
    }
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('target fixture refuses an existing output directory without deleting its contents', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-target-fixture-existing.'));
  const marker = join(root, 'keep-me.txt');
  writeFileSync(marker, 'preserved\n', { mode: 0o600 });
  try {
    await assert.rejects(
      buildAndInstallTargetFixture({
        buildDaemon: fixtureDaemon,
        outputRoot: root,
        targetID: 'linux-x64-gnu',
      }),
      (error) => error?.code === 'target_fixture_output_exists',
    );
    assert.equal(readFileSync(marker, 'utf8'), 'preserved\n');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
