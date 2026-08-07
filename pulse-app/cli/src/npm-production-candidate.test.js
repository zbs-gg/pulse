import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import test from 'node:test';
import { gzipSync } from 'node:zlib';
import { pack } from 'tar-stream';

import { buildNpmProductionCandidate } from '../scripts/build-npm-production-candidate.mjs';
import { buildNpmProductionInputs } from '../scripts/build-npm-production-inputs.mjs';
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
  const securityRoot = join(root, 'security');
  mkdirSync(catalogRoot, { mode: 0o700 });
  mkdirSync(evidenceRoot, { mode: 0o700 });
  mkdirSync(securityRoot, { mode: 0o700 });
  const manifest = {
    payload: {
      release: { package: '@zbs-gg/pulse', version: '0.8.0' },
      targets: Object.fromEntries(DESKTOP_TARGET_IDS.map((targetID) => [targetID, {}])),
    },
    schema: 'pulse.personal_release_artifact_set.v1',
    signature: {},
  };
  const manifestBytes = `${canonical(manifest)}\n`;
  const artifactSetDigest = createHash('sha256').update(manifestBytes).digest('hex');
  const snapshot = {
    payload: { artifact_set: { sha256: artifactSetDigest }, release_epoch: 9 },
    schema: 'pulse.release_snapshot_envelope.v1',
    signature: {},
  };
  const snapshotBytes = `${canonical(snapshot)}\n`;
  const snapshotDigest = createHash('sha256').update(snapshotBytes).digest('hex');
  writeFileSync(join(catalogRoot, 'personal-preview-manifest.json'), manifestBytes, { mode: 0o600 });
  writeFileSync(join(catalogRoot, 'snapshot.json'), snapshotBytes, { mode: 0o600 });
  writeCanonical(join(catalogRoot, 'catalog-build-receipt.json'), {
    artifact_count: 14,
    artifact_set_digest: artifactSetDigest,
    artifact_set_url: 'https://releases.zbs.gg/pulse/0.8.0/epoch-9/catalog/artifact-set.json',
    channel_key_id: 'channel',
    host_target_count: 18,
    hosts: ['claude-code', 'codex', 'cursor'],
    manifest_digest: artifactSetDigest,
    production_ready: true,
    release_epoch: 9,
    root_key_id: 'root',
    schema: 'pulse.personal_release_catalog_build.v3',
    snapshot_digest: snapshotDigest,
    snapshot_expires_at: '2026-08-26T00:00:00.000Z',
    snapshot_url: 'https://releases.zbs.gg/pulse/0.8.0/catalog/snapshot.json',
    target_count: 6,
    target_ids: DESKTOP_TARGET_IDS,
  });
  const tarball = await packageTarball({
    name: '@zbs-gg/pulse',
    repository: { url: 'git+https://github.com/zbs-gg/pulse.git' },
    version: '0.8.0',
  }, manifestBytes);
  const tarballPath = resolve(root, 'zbs-gg-pulse-0.8.0.tgz');
  writeFileSync(tarballPath, tarball, { mode: 0o600 });
  const matrix = loadNativeUniversalMatrix();
  const packageSHA256 = createHash('sha256').update(tarball).digest('hex');
  const sbomBytes = `${canonical({ bomFormat: 'CycloneDX', components: [] })}\n`;
  const licenseBytes = `${canonical({ dependencies: [], schema: 'pulse.release_license_inventory.v1' })}\n`;
  writeFileSync(join(securityRoot, 'sbom.cdx.json'), sbomBytes, { mode: 0o600 });
  writeFileSync(join(securityRoot, 'licenses.json'), licenseBytes, { mode: 0o600 });
  writeCanonical(join(securityRoot, 'dependency-receipt.json'), {
    audit: { command: 'npm audit --omit=dev --audit-level=high', critical: 0, high: 0, total: 0 },
    content_free: true,
    dependency_count: 100,
    license_inventory_sha256: createHash('sha256').update(licenseBytes).digest('hex'),
    package: '@zbs-gg/pulse',
    package_bytes: tarball.length,
    package_sha256: packageSHA256,
    sbom_sha256: createHash('sha256').update(sbomBytes).digest('hex'),
    schema: 'pulse.release_dependency_receipt.v1',
    version: '0.8.0',
  });
  for (const harness of matrix.harnesses) {
    for (const target of matrix.targets) {
      writeFileSync(join(evidenceRoot, `${harness.host}-${target.target_id}.json`), `${JSON.stringify({
        schema: 'pulse.native_host_target_evidence.v2',
        authority: 'production_candidate',
        content_free: true,
        support_claim: false,
        source_commit: COMMIT,
        host: harness.host,
        host_version: harness.version,
        target_id: target.target_id,
        target,
        runner: { name: 'fixture', image: target.runner },
        harness: {
          vendor: harness.vendor,
          distribution: harness.distribution,
          identity: harness.identity,
          vendor_source: harness.vendor_source,
          download_url: harness.downloads[target.target_id],
          executable_kind: 'vendor_executable',
          executable_sha256: createHash('sha256').update(`${harness.host}:${target.target_id}`).digest('hex'),
          session_executable_sha256: createHash('sha256').update(`session:${harness.host}:${target.target_id}`).digest('hex'),
        },
        package: { bytes: tarball.length, sha256: packageSHA256 },
        release: {
          artifact_set_digest: artifactSetDigest, snapshot_digest: snapshotDigest,
          fixture_manifest_digest: null, artifact_ids: [],
        },
        milestones: {
          install: true, vendor_session: true, lifecycle: true, memory_home: true,
          fresh_recall: true, repair: true, disconnect: true,
          deduplicated: true, fail_open: true, memory_survived_restart: true,
          no_automatic_continuation: true, stop_and_goal_control_available: true,
        },
        first_value: {
          measured: true, boundary: 'fresh_session_context', milliseconds: 1200,
          stages_ms: { install: 400, lifecycle: 800 }, degraded_lifecycle: false,
          stability: harness.host === 'codex' && target.target_id === 'win32-arm64'
            ? { consecutive_runs: 5, runs_ms: [1200, 1200, 1200, 1200, 1200], median_ms: 1200 }
            : { consecutive_runs: 1, runs_ms: [1200], median_ms: 1200 },
        },
        privacy_defaults: { raw_transcripts: false, old_chat_import: false, backend_llm: false },
        token_economy: { state: 'collecting_baseline' },
        consolidation: { mutation_authority_exercised: false },
      })}\n`, { mode: 0o600 });
    }
  }
  return {
    catalogRoot: resolve(catalogRoot),
    evidenceRoot: resolve(evidenceRoot),
    outputRoot: resolve(root, 'candidate'),
    root,
    securityRoot: resolve(securityRoot),
    tarballPath,
  };
}

test('production candidate binds the exact npm bytes to catalog and all 18 native proofs', async (t) => {
  const current = await fixture(t);
  const candidate = await buildNpmProductionCandidate({
    ...current,
    commit: COMMIT,
    universalRunID: 918273,
  });
  assert.deepEqual(candidate.targets, DESKTOP_TARGET_IDS);
  assert.equal(candidate.production, true);
  assert.equal(candidate.production_ready, true);
  assert.equal(candidate.support_claim, false);
  assert.equal(candidate.host_target_count, 18);
  assert.deepEqual(candidate.hosts, ['claude-code', 'codex', 'cursor']);
  assert.equal(candidate.release_epoch, 9);
  assert.equal(candidate.dependency_count, 100);
  assert.match(candidate.sbom_sha256, /^[a-f0-9]{64}$/);
  assert.match(candidate.license_inventory_sha256, /^[a-f0-9]{64}$/);
  assert.match(candidate.artifact_set_digest, /^[a-f0-9]{64}$/);
  assert.match(candidate.snapshot_digest, /^[a-f0-9]{64}$/);
  assert.equal(readFileSync(join(current.outputRoot, 'candidate.json'), 'utf8'), `${canonical(candidate)}\n`);
  const stage = await verifyNpmStageCandidate({
    candidatePath: join(current.outputRoot, 'candidate.json'),
    expectedCommit: COMMIT,
    expectedSHA256: candidate.sha256,
  });
  assert.equal(stage.universal_run_id, 918273);
  assert.deepEqual(stage.targets, DESKTOP_TARGET_IDS);
});

test('production inputs bind exact catalog, security, and tarball bytes without claiming readiness', async (t) => {
  const current = await fixture(t);
  const outputRoot = resolve(current.root, 'inputs');
  const inputs = await buildNpmProductionInputs({
    catalogRoot: current.catalogRoot,
    commit: COMMIT,
    outputRoot,
    securityRoot: current.securityRoot,
    tarballPath: current.tarballPath,
    universalRunID: 918273,
  });
  assert.equal(inputs.schema, 'pulse.npm_production_inputs.v1');
  assert.equal(inputs.production_ready, false);
  assert.equal(inputs.support_claim, false);
  assert.equal(inputs.package_sha256, createHash('sha256').update(readFileSync(current.tarballPath)).digest('hex'));
  assert.equal(readFileSync(join(outputRoot, 'candidate-inputs.json'), 'utf8'), `${canonical(inputs)}\n`);
});

test('production candidate rejects incomplete native proof and packaged manifest drift', async (t) => {
  const incomplete = await fixture(t);
  rmSync(join(incomplete.evidenceRoot, 'cursor-win32-arm64.json'));
  await assert.rejects(
    buildNpmProductionCandidate({ ...incomplete, commit: COMMIT, universalRunID: 11 }),
    { code: 'npm_production_candidate_evidence_incomplete' },
  );

  const drift = await fixture(t);
  const badTarball = await packageTarball({
    name: '@zbs-gg/pulse',
    repository: { url: 'git+https://github.com/zbs-gg/pulse.git' },
    version: '0.8.0',
  }, `${canonical({ schema: 'pulse.personal_release_artifact_set.v1', payload: { release: { version: '0.8.0' } } })}\n`);
  writeFileSync(drift.tarballPath, badTarball);
  await assert.rejects(
    buildNpmProductionCandidate({ ...drift, commit: COMMIT, universalRunID: 12 }),
    { code: 'npm_production_candidate_package_mismatch' },
  );
});
