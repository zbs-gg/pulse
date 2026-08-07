#!/usr/bin/env node

import { createHash } from 'node:crypto';
import { lstatSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, isAbsolute, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { canonicalReleaseJSON } from '../src/release-manifest.js';
import { validateNativeEvidenceSet } from './validate-native-evidence-set.mjs';

const CHECKPOINTS = Object.freeze({
  0: Object.freeze([0, 4]),
  24: Object.freeze([23, 27]),
  48: Object.freeze([47, 51]),
  72: Object.freeze([71, 76]),
});
const SHA40 = /^[a-f0-9]{40}$/;
const SHA256 = /^[a-f0-9]{64}$/;

function fail(code) {
  const error = new Error(code);
  error.code = code;
  throw error;
}

function regularFile(path, maximumBytes) {
  let info;
  try { info = lstatSync(path); } catch { fail('public_soak_input_missing'); }
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.size < 1 || info.size > maximumBytes) {
    fail('public_soak_input_unsafe');
  }
  return info;
}

function canonicalFile(path, schema) {
  regularFile(path, 1024 * 1024);
  const bytes = readFileSync(path, 'utf8');
  let value;
  try { value = JSON.parse(bytes); } catch { fail('public_soak_input_invalid'); }
  if (bytes !== `${canonicalReleaseJSON(value)}\n` || value?.schema !== schema) fail('public_soak_input_invalid');
  return value;
}

function sha256File(path) {
  regularFile(path, 1024 * 1024 * 1024);
  return createHash('sha256').update(readFileSync(path)).digest('hex');
}

export function buildPublicSoakReceipt({
  candidatePath,
  checkpointHours,
  evidenceRoot,
  now = new Date(),
  outputPath,
  publishedAt,
  registryTarballPath,
} = {}) {
  if (![candidatePath, evidenceRoot, outputPath, registryTarballPath].every((path) =>
    typeof path === 'string' && isAbsolute(path) && resolve(path) === path) ||
      !Object.hasOwn(CHECKPOINTS, checkpointHours) || !(now instanceof Date) || Number.isNaN(now.valueOf()) ||
      !(publishedAt instanceof Date) || Number.isNaN(publishedAt.valueOf())) fail('public_soak_arguments_invalid');
  const elapsedHours = (now.valueOf() - publishedAt.valueOf()) / 3_600_000;
  const [minimum, maximum] = CHECKPOINTS[checkpointHours];
  if (elapsedHours < minimum || elapsedHours > maximum) fail('public_soak_checkpoint_window_invalid');

  const candidate = canonicalFile(candidatePath, 'pulse.npm_production_candidate.v1');
  if (candidate.production !== true || candidate.production_ready !== true || candidate.support_claim !== false || candidate.release_epoch !== 9 ||
      candidate.package !== '@zbs-gg/pulse' || candidate.version !== '0.8.0' ||
      candidate.host_target_count !== 18 || !SHA40.test(candidate.commit ?? '') ||
      !SHA256.test(candidate.sha256 ?? '') || !SHA256.test(candidate.artifact_set_digest ?? '') ||
      !SHA256.test(candidate.snapshot_digest ?? '')) fail('public_soak_candidate_invalid');
  const registrySHA256 = sha256File(registryTarballPath);
  if (registrySHA256 !== candidate.sha256) fail('public_soak_registry_digest_mismatch');
  let evidence;
  try { evidence = validateNativeEvidenceSet(evidenceRoot, { authority: 'public_registry' }); }
  catch { fail('public_soak_evidence_invalid'); }
  if (evidence.count !== 18 || evidence.source_commit !== candidate.commit ||
      evidence.package_sha256 !== candidate.sha256 || evidence.artifact_set_digest !== candidate.artifact_set_digest ||
      !SHA256.test(evidence.snapshot_digest ?? '')) fail('public_soak_evidence_mismatch');

  const receipt = Object.freeze({
    artifact_set_digest: evidence.artifact_set_digest,
    authority: 'public_registry',
    candidate_initial_snapshot_digest: candidate.snapshot_digest,
    checkpoint_hours: checkpointHours,
    commit: candidate.commit,
    evidence_count: evidence.count,
    observed_at: now.toISOString(),
    package: candidate.package,
    package_sha256: candidate.sha256,
    published_at: publishedAt.toISOString(),
    release_epoch: candidate.release_epoch,
    schema: 'pulse.public_registry_soak.v1',
    snapshot_digest: evidence.snapshot_digest,
    support_claim: true,
    version: candidate.version,
  });
  mkdirSync(dirname(outputPath), { recursive: true, mode: 0o700 });
  writeFileSync(outputPath, `${canonicalReleaseJSON(receipt)}\n`, { encoding: 'utf8', flag: 'wx', mode: 0o600 });
  return receipt;
}

function args(argv) {
  const values = {};
  for (let index = 0; index < argv.length; index += 2) {
    const name = argv[index];
    const value = argv[index + 1];
    if (!['--candidate', '--checkpoint', '--evidence', '--output', '--published-at', '--tarball'].includes(name) ||
        !value || Object.hasOwn(values, name)) fail('public_soak_arguments_invalid');
    values[name] = value;
  }
  if (Object.keys(values).length !== 6) fail('public_soak_arguments_invalid');
  return values;
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  const values = args(process.argv.slice(2));
  const receipt = buildPublicSoakReceipt({
    candidatePath: resolve(values['--candidate']),
    checkpointHours: Number(values['--checkpoint']),
    evidenceRoot: resolve(values['--evidence']),
    outputPath: resolve(values['--output']),
    publishedAt: new Date(values['--published-at']),
    registryTarballPath: resolve(values['--tarball']),
  });
  process.stdout.write(`${canonicalReleaseJSON(receipt)}\n`);
}
