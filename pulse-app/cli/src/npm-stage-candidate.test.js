import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import test from 'node:test';
import { gzipSync } from 'node:zlib';
import { pack } from 'tar-stream';

import { verifyNpmStageCandidate } from '../scripts/verify-npm-stage-candidate.mjs';

const TARGETS = Object.freeze([
  'darwin-arm64', 'darwin-x64', 'linux-arm64-gnu',
  'linux-x64-gnu', 'win32-arm64', 'win32-x64',
]);
const COMMIT = 'a'.repeat(40);

function canonical(value) {
  if (value === null) return 'null';
  if (typeof value === 'string' || typeof value === 'boolean') return JSON.stringify(value);
  if (typeof value === 'number') return String(value);
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`;
  return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}`;
}

async function packageTarball(packageJSON) {
  const archive = pack();
  const chunks = [];
  const complete = new Promise((accept, reject) => {
    archive.on('data', (chunk) => chunks.push(Buffer.from(chunk)));
    archive.once('end', accept);
    archive.once('error', reject);
  });
  const contents = Buffer.from(`${JSON.stringify(packageJSON)}\n`);
  archive.entry({ name: 'package/package.json', size: contents.length, type: 'file' }, contents);
  archive.finalize();
  await complete;
  return gzipSync(Buffer.concat(chunks));
}

async function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'pulse-npm-stage-candidate-'));
  const tarball = await packageTarball({
    name: '@zbs-gg/pulse',
    version: '0.7.2',
    repository: { url: 'git+https://github.com/zbs-gg/pulse.git' },
  });
  const sha256 = createHash('sha256').update(tarball).digest('hex');
  writeFileSync(join(root, 'pulse-0.7.2.tgz'), tarball);
  const candidate = {
    artifact_set_digest: 'b'.repeat(64),
    commit: COMMIT,
    dependency_count: 100,
    host_target_count: 18,
    hosts: ['claude-code', 'codex', 'cursor'],
    license_inventory_sha256: 'c'.repeat(64),
    package: '@zbs-gg/pulse',
    production: true,
    production_ready: true,
    release_epoch: 9,
    sbom_sha256: 'd'.repeat(64),
    schema: 'pulse.npm_production_candidate.v1',
    sha256,
    snapshot_digest: 'e'.repeat(64),
    support_claim: false,
    targets: TARGETS,
    tarball: 'pulse-0.7.2.tgz',
    universal_run_id: 123,
    version: '0.7.2',
  };
  const candidatePath = resolve(root, 'candidate.json');
  writeFileSync(candidatePath, `${canonical(candidate)}\n`);
  return { candidate, candidatePath, root, sha256 };
}

test('npm stage verifier binds an exact production tarball to all native targets', async (t) => {
  const current = await fixture();
  t.after(() => rmSync(current.root, { force: true, recursive: true }));
  const receipt = await verifyNpmStageCandidate({
    candidatePath: current.candidatePath,
    expectedCommit: COMMIT,
    expectedSHA256: current.sha256,
  });
  assert.deepEqual(receipt.targets, TARGETS);
  assert.equal(receipt.tarball, current.candidate.tarball);
  assert.equal(receipt.stage_tag, 'preview');
});

test('npm stage verifier rejects PR fixtures and package identity drift', async (t) => {
  const current = await fixture();
  t.after(() => rmSync(current.root, { force: true, recursive: true }));
  current.candidate.production = false;
  writeFileSync(current.candidatePath, `${canonical(current.candidate)}\n`);
  await assert.rejects(
    verifyNpmStageCandidate({
      candidatePath: current.candidatePath,
      expectedCommit: COMMIT,
      expectedSHA256: current.sha256,
    }),
    { code: 'npm_candidate_receipt_invalid' },
  );

  const tarball = await packageTarball({
    name: '@someone-else/pulse',
    version: '0.7.2',
    repository: { url: 'git+https://github.com/zbs-gg/pulse.git' },
  });
  current.candidate.production = true;
  current.candidate.sha256 = createHash('sha256').update(tarball).digest('hex');
  writeFileSync(join(current.root, current.candidate.tarball), tarball);
  writeFileSync(current.candidatePath, `${canonical(current.candidate)}\n`);
  await assert.rejects(
    verifyNpmStageCandidate({
      candidatePath: current.candidatePath,
      expectedCommit: COMMIT,
      expectedSHA256: current.candidate.sha256,
    }),
    { code: 'npm_candidate_package_name_mismatch' },
  );
});
