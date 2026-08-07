import assert from 'node:assert/strict';
import { createHash, generateKeyPairSync } from 'node:crypto';
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import test from 'node:test';

import { buildPublicSoakReceipt } from '../scripts/build-public-soak-receipt.mjs';
import { buildGoldPromotionReceipt } from '../scripts/build-gold-promotion-receipt.mjs';
import { generateNativeSupportLedger } from '../scripts/generate-native-support-ledger.mjs';
import { loadNativeUniversalMatrix } from '../scripts/native-universal-matrix.mjs';
import { canonicalReleaseJSON, releaseKeyID } from './release-manifest.js';

const COMMIT = 'a'.repeat(40);
const ARTIFACT_SET = 'b'.repeat(64);
const SNAPSHOT = 'c'.repeat(64);

function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'pulse-public-soak-'));
  t.after(() => rmSync(root, { force: true, recursive: true }));
  const evidenceRoot = resolve(root, 'evidence');
  mkdirSync(evidenceRoot, { mode: 0o700 });
  const tarballPath = resolve(root, 'zbs-gg-pulse-0.8.0.tgz');
  const tarball = Buffer.from('exact-registry-package');
  writeFileSync(tarballPath, tarball, { mode: 0o600 });
  const packageSHA256 = createHash('sha256').update(tarball).digest('hex');
  const matrix = loadNativeUniversalMatrix();
  for (const harness of matrix.harnesses) {
    for (const target of matrix.targets) {
      const milliseconds = 1_200;
      const evidence = {
        authority: 'public_registry',
        consolidation: { mutation_authority_exercised: false },
        content_free: true,
        first_value: {
          boundary: 'fresh_session_context', degraded_lifecycle: false, measured: true, milliseconds,
          stability: harness.host === 'codex' && target.target_id === 'win32-arm64'
            ? { consecutive_runs: 5, median_ms: milliseconds, runs_ms: Array(5).fill(milliseconds) }
            : { consecutive_runs: 1, median_ms: milliseconds, runs_ms: [milliseconds] },
          stages_ms: { install: 400, fresh_session: 800 },
        },
        harness: {
          distribution: harness.distribution,
          download_url: harness.downloads[target.target_id],
          executable_kind: 'vendor_executable',
          executable_sha256: createHash('sha256').update(`${harness.host}:${target.target_id}`).digest('hex'),
          identity: harness.identity,
          session_executable_sha256: createHash('sha256').update(`session:${harness.host}:${target.target_id}`).digest('hex'),
          vendor: harness.vendor,
          vendor_source: harness.vendor_source,
        },
        host: harness.host,
        host_version: harness.version,
        milestones: {
          disconnect: true, fresh_recall: true, install: true, lifecycle: true,
          memory_home: true, repair: true, vendor_session: true,
          deduplicated: true, fail_open: true, memory_survived_restart: true,
          no_automatic_continuation: true, stop_and_goal_control_available: true,
        },
        package: { bytes: tarball.length, sha256: packageSHA256 },
        privacy_defaults: { backend_llm: false, old_chat_import: false, raw_transcripts: false },
        release: {
          artifact_ids: [], artifact_set_digest: ARTIFACT_SET,
          fixture_manifest_digest: null, snapshot_digest: SNAPSHOT,
        },
        runner: { image: target.runner, name: 'fixture' },
        schema: 'pulse.native_host_target_evidence.v2',
        source_commit: COMMIT,
        support_claim: true,
        target,
        target_id: target.target_id,
        token_economy: { state: 'collecting_baseline' },
      };
      writeFileSync(join(evidenceRoot, `${harness.host}-${target.target_id}.json`),
        `${canonicalReleaseJSON(evidence)}\n`, { mode: 0o600 });
    }
  }
  const candidate = {
    artifact_set_digest: ARTIFACT_SET,
    commit: COMMIT,
    dependency_count: 100,
    host_target_count: 18,
    hosts: ['claude-code', 'codex', 'cursor'],
    license_inventory_sha256: 'd'.repeat(64),
    package: '@zbs-gg/pulse',
    production: true,
    production_ready: true,
    release_epoch: 9,
    sbom_sha256: 'e'.repeat(64),
    schema: 'pulse.npm_production_candidate.v1',
    sha256: packageSHA256,
    snapshot_digest: SNAPSHOT,
    support_claim: false,
    targets: matrix.targets.map((target) => target.target_id).sort(),
    tarball: 'zbs-gg-pulse-0.8.0.tgz',
    universal_run_id: 123,
    version: '0.8.0',
  };
  const candidatePath = resolve(root, 'candidate.json');
  writeFileSync(candidatePath, `${canonicalReleaseJSON(candidate)}\n`, { mode: 0o600 });
  return { candidatePath, evidenceRoot, packageSHA256, root, tarballPath };
}

test('public soak receipt binds one timed checkpoint to exact registry bytes and 18 public proofs', (t) => {
  const current = fixture(t);
  const publishedAt = new Date('2026-07-27T00:00:00.000Z');
  const receipt = buildPublicSoakReceipt({
    candidatePath: current.candidatePath,
    checkpointHours: 24,
    evidenceRoot: current.evidenceRoot,
    now: new Date('2026-07-28T00:10:00.000Z'),
    outputPath: resolve(current.root, 'soak.json'),
    publishedAt,
    registryTarballPath: current.tarballPath,
  });
  assert.equal(receipt.evidence_count, 18);
  assert.equal(receipt.package_sha256, current.packageSHA256);
  assert.equal(receipt.support_claim, true);
});

test('public soak receipt rejects an early checkpoint and registry byte drift', (t) => {
  const current = fixture(t);
  const base = {
    candidatePath: current.candidatePath,
    checkpointHours: 72,
    evidenceRoot: current.evidenceRoot,
    now: new Date('2026-07-28T00:00:00.000Z'),
    outputPath: resolve(current.root, 'early.json'),
    publishedAt: new Date('2026-07-27T00:00:00.000Z'),
    registryTarballPath: current.tarballPath,
  };
  assert.throws(() => buildPublicSoakReceipt(base), { code: 'public_soak_checkpoint_window_invalid' });
  writeFileSync(current.tarballPath, 'drift');
  assert.throws(() => buildPublicSoakReceipt({
    ...base, checkpointHours: 24, outputPath: resolve(current.root, 'drift.json'),
  }), { code: 'public_soak_registry_digest_mismatch' });
});

test('Gold authorization requires four exact public checkpoints and signs without publishing', (t) => {
  const current = fixture(t);
  const soaks = resolve(current.root, 'soaks');
  mkdirSync(soaks, { mode: 0o700 });
  const publishedAt = new Date('2026-07-27T00:00:00.000Z');
  for (const checkpoint of [0, 24, 48, 72]) {
    buildPublicSoakReceipt({
      candidatePath: current.candidatePath,
      checkpointHours: checkpoint,
      evidenceRoot: current.evidenceRoot,
      now: new Date(publishedAt.valueOf() + (checkpoint === 0 ? 1 : checkpoint) * 3_600_000),
      outputPath: resolve(soaks, `public-soak-${checkpoint}.json`),
      publishedAt,
      registryTarballPath: current.tarballPath,
    });
  }
  const { privateKey, publicKey } = generateKeyPairSync('ed25519');
  const rootKeyPath = resolve(current.root, 'root.pem');
  writeFileSync(rootKeyPath, privateKey.export({ type: 'pkcs8', format: 'pem' }), { mode: 0o600 });
  chmodSync(rootKeyPath, 0o600);
  const publicPEM = publicKey.export({ type: 'spki', format: 'pem' });
  const receipt = buildGoldPromotionReceipt({
    candidatePath: current.candidatePath,
    outputPath: resolve(current.root, 'promotion.json'),
    rootKeyPath,
    soakRoot: soaks,
    trustedKeys: [{ key_id: releaseKeyID(publicPEM), public_key_pem: publicPEM }],
  });
  assert.equal(receipt.payload.promotion_authorized, true);
  assert.equal(receipt.payload.publication_performed, false);
  assert.equal(receipt.payload.checkpoints.length, 4);
  assert.equal(JSON.parse(readFileSync(resolve(current.root, 'promotion.json'))).schema,
    'pulse.gold_promotion_receipt.v1');
  const ledgerPath = resolve(current.root, 'ledger.md');
  const generated = generateNativeSupportLedger({
    evidenceRoot: current.evidenceRoot,
    outputPath: ledgerPath,
    promotionPath: resolve(current.root, 'promotion.json'),
    trustedKeys: [{ key_id: releaseKeyID(publicPEM), public_key_pem: publicPEM }],
  });
  assert.equal(generated.count, 18);
  assert.match(readFileSync(ledgerPath, 'utf8'), /Generated only from signed promotion authorization/);
});
