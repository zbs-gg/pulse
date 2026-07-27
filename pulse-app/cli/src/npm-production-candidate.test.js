import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import test from 'node:test';
import { gzipSync } from 'node:zlib';
import { pack } from 'tar-stream';

import { buildNpmProductionCandidate } from '../scripts/build-npm-production-candidate.mjs';
import { loadNativeUniversalMatrix } from '../scripts/native-universal-matrix.mjs';
import { DESKTOP_TARGET_IDS } from './desktop-target.js';
import { verifyNpmStageCandidate } from '../scripts/verify-npm-stage-candidate.mjs';

const COMMIT = 'b'.repeat(40);

function canonical(value) {
  if (value === null) return 'null';
  if (typeof value === 'string' || typeof value === 'boolean') return JSON.stringify(value);
  if (typeof value === 'number') return String(value);
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`;
  return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}`;
}

function writeCanonical(path, value) {
  writeFileSync(path, `${canonical(value)}\n`, { mode: 0o600 });
}

async function packageTarball(packageJSON, manifestBytes) {
  const archive = pack();
  const chunks = [];
  const complete = new Promise((accept, reject) => {
    archive.on('data', (chunk) => chunks.push(Buffer.from(chunk)));
    archive.once('end', accept);
    archive.once('error', reject);
  });
  for (const [name, bytes] of [
    ['package/package.json', Buffer.from(`${JSON.stringify(packageJSON)}\n`)],
    ['package/release/personal-preview-manifest.json', Buffer.from(manifestBytes)],
  ]) {
    archive.entry({ name, size: bytes.length, type: 'file' }, bytes);
  }
  archive.finalize();
  await complete;
  return gzipSync(Buffer.concat(chunks));
}

async function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'pulse-npm-production-candidate.'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const catalogRoot = join(root, 'catalog');
  const evidenceRoot = join(root, 'evidence');
  mkdirSync(catalogRoot, { mode: 0o700 });
  mkdirSync(evidenceRoot, { mode: 0o700 });
  const manifest = {
    authority: { payload: {}, schema: 'pulse.release_authority_envelope.v1', signature: {} },
    payload: {
      release: { package: '@zbs-gg/pulse', version: '0.7.0' },
      targets: Object.fromEntries(DESKTOP_TARGET_IDS.map((targetID) => [targetID, {}])),
    },
    schema: 'pulse.release_catalog_envelope.v2',
    signature: {},
  };
  const manifestBytes = `${canonical(manifest)}\n`;
  writeFileSync(join(catalogRoot, 'personal-preview-manifest.json'), manifestBytes, { mode: 0o600 });
  writeCanonical(join(catalogRoot, 'catalog-build-receipt.json'), {
    artifact_count: 14,
    channel_key_id: 'channel',
    manifest_digest: 'a'.repeat(64),
    production_ready: true,
    root_key_id: 'root',
    schema: 'pulse.personal_release_catalog_build.v2',
    target_count: 6,
    target_ids: DESKTOP_TARGET_IDS,
  });
  const matrix = loadNativeUniversalMatrix();
  for (const target of matrix.targets) {
    const kinds = ['daemon', 'embedder-runtime', 'model', 'plugin-runtime'];
    if (target.platform === 'darwin') kinds.push('presence-helper');
    writeFileSync(join(evidenceRoot, `${target.target_id}.json`), `${JSON.stringify({
      authority: 'pr-fixture',
      commit: COMMIT,
      consolidation: {
        cli_parity: true,
        mcp_parity: true,
        memory_home_visible: true,
        mutation_authority_exercised: false,
        phase: 'report_ready',
        sources_byte_preserved: true,
      },
      first_value: {
        boundary: 'fresh_session_context',
        milliseconds: 1200,
        same_object_recalled: true,
        visible_card: true,
      },
      harness: matrix.harness,
      package: { bytes: 1234, sha256: createHash('sha256').update(target.target_id).digest('hex') },
      production: false,
      release: { artifact_ids: kinds.map((kind) => `pulse-0.7.0-${target.target_id}-${kind}`) },
      runtime: { full_retrieval: true, lifecycle_ready: true },
      schema: 'pulse.native_universal_target_evidence.v1',
      support_claim: false,
      target,
      token_economy: { state: 'collecting_baseline' },
    })}\n`, { mode: 0o600 });
  }
  const tarball = await packageTarball({
    name: '@zbs-gg/pulse',
    repository: { url: 'git+https://github.com/zbs-gg/pulse.git' },
    version: '0.7.0',
  }, manifestBytes);
  const tarballPath = resolve(root, 'zbs-gg-pulse-0.7.0.tgz');
  writeFileSync(tarballPath, tarball, { mode: 0o600 });
  return {
    catalogRoot: resolve(catalogRoot),
    evidenceRoot: resolve(evidenceRoot),
    outputRoot: resolve(root, 'candidate'),
    root,
    tarballPath,
  };
}

test('production candidate binds the exact npm bytes to catalog and all six native proofs', async (t) => {
  const current = await fixture(t);
  const candidate = await buildNpmProductionCandidate({
    ...current,
    commit: COMMIT,
    universalRunID: 918273,
  });
  assert.deepEqual(candidate.targets, DESKTOP_TARGET_IDS);
  assert.equal(candidate.production, true);
  assert.equal(candidate.support_claim, false);
  assert.equal(readFileSync(join(current.outputRoot, 'candidate.json'), 'utf8'), `${canonical(candidate)}\n`);
  const stage = await verifyNpmStageCandidate({
    candidatePath: join(current.outputRoot, 'candidate.json'),
    expectedCommit: COMMIT,
    expectedSHA256: candidate.sha256,
  });
  assert.equal(stage.universal_run_id, 918273);
  assert.deepEqual(stage.targets, DESKTOP_TARGET_IDS);
});

test('production candidate rejects incomplete native proof and packaged manifest drift', async (t) => {
  const incomplete = await fixture(t);
  rmSync(join(incomplete.evidenceRoot, 'win32-arm64.json'));
  await assert.rejects(
    buildNpmProductionCandidate({ ...incomplete, commit: COMMIT, universalRunID: 11 }),
    { code: 'npm_production_candidate_evidence_incomplete' },
  );

  const drift = await fixture(t);
  const badTarball = await packageTarball({
    name: '@zbs-gg/pulse',
    repository: { url: 'git+https://github.com/zbs-gg/pulse.git' },
    version: '0.7.0',
  }, `${canonical({ schema: 'pulse.release_catalog_envelope.v2', payload: { release: { version: '0.7.0' } } })}\n`);
  writeFileSync(drift.tarballPath, badTarball);
  await assert.rejects(
    buildNpmProductionCandidate({ ...drift, commit: COMMIT, universalRunID: 12 }),
    { code: 'npm_production_candidate_package_mismatch' },
  );
});
