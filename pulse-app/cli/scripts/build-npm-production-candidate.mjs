#!/usr/bin/env node

import { createHash } from 'node:crypto';
import { createReadStream } from 'node:fs';
import {
  chmodSync, copyFileSync, existsSync, lstatSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync,
} from 'node:fs';
import { basename, dirname, isAbsolute, join, resolve } from 'node:path';
import { pipeline } from 'node:stream/promises';
import { fileURLToPath } from 'node:url';
import { createGunzip } from 'node:zlib';
import { extract } from 'tar-stream';

import { DESKTOP_TARGET_IDS } from '../src/desktop-target.js';
import { loadNativeUniversalMatrix } from './native-universal-matrix.mjs';
import { validateNativeEvidenceSet } from './validate-native-evidence-set.mjs';

const SHA40 = /^[a-f0-9]{40}$/;
const SHA256 = /^[a-f0-9]{64}$/;
const PACKAGE_NAME = '@zbs-gg/pulse';
const REPOSITORY = 'git+https://github.com/zbs-gg/pulse.git';
const SAFE_TARBALL = /^[A-Za-z0-9][A-Za-z0-9._-]{0,199}\.tgz$/;

function fail(code) {
  const error = new Error(code);
  error.code = code;
  throw error;
}

function canonical(value) {
  if (value === null) return 'null';
  if (typeof value === 'string' || typeof value === 'boolean') return JSON.stringify(value);
  if (typeof value === 'number' && Number.isSafeInteger(value) && !Object.is(value, -0)) return String(value);
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`;
  if (value && typeof value === 'object') {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(',')}}`;
  }
  fail('npm_production_candidate_value_invalid');
}

function regularFile(path, maximumBytes, minimumBytes = 1) {
  let info;
  try { info = lstatSync(path); } catch { fail('npm_production_candidate_input_missing'); }
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 ||
      info.size < minimumBytes || info.size > maximumBytes) {
    fail('npm_production_candidate_input_unsafe');
  }
  return info;
}

function readCanonicalJSON(path, schema, maximumBytes = 1024 * 1024) {
  regularFile(path, maximumBytes);
  const bytes = readFileSync(path, 'utf8');
  let value;
  try { value = JSON.parse(bytes); } catch { fail('npm_production_candidate_input_invalid'); }
  if (bytes !== `${canonical(value)}\n` || value?.schema !== schema) {
    fail('npm_production_candidate_input_invalid');
  }
  return Object.freeze({ bytes, value });
}

function digestFile(path) {
  regularFile(path, 1024 * 1024 * 1024);
  return createHash('sha256').update(readFileSync(path)).digest('hex');
}

function evidenceFiles(root) {
  const matrix = loadNativeUniversalMatrix();
  const expectedCount = matrix.harnesses.length * matrix.targets.length;
  let rootInfo;
  try { rootInfo = lstatSync(root); } catch { fail('npm_production_candidate_evidence_missing'); }
  if (!rootInfo.isDirectory() || rootInfo.isSymbolicLink()) fail('npm_production_candidate_evidence_unsafe');
  const files = [];
  const visit = (directory, depth) => {
    if (depth > 4) fail('npm_production_candidate_evidence_unsafe');
    for (const name of readdirSync(directory).sort()) {
      if (!name || name.includes('\0') || name === '.' || name === '..') fail('npm_production_candidate_evidence_unsafe');
      const path = join(directory, name);
      const info = lstatSync(path);
      if (info.isSymbolicLink()) fail('npm_production_candidate_evidence_unsafe');
      if (info.isDirectory()) visit(path, depth + 1);
      else if (info.isFile() && info.nlink === 1 && name.endsWith('.json')) files.push(path);
      else fail('npm_production_candidate_evidence_unsafe');
      if (files.length > expectedCount) fail('npm_production_candidate_evidence_ambiguous');
    }
  };
  visit(root, 0);
  if (files.length !== expectedCount) fail('npm_production_candidate_evidence_incomplete');
  return files;
}

function validateUniversalEvidence(root, commit) {
  evidenceFiles(root);
  try {
    const result = validateNativeEvidenceSet(root, { authority: 'production_candidate' });
    if (result.source_commit !== commit) fail('npm_production_candidate_evidence_invalid');
    return result;
  } catch (error) {
    if (error?.code?.startsWith('npm_production_candidate_')) throw error;
    fail('npm_production_candidate_evidence_invalid');
  }
}

async function packageDocuments(path) {
  const unpack = extract();
  const wanted = new Map([
    ['package/package.json', { maximum: 64 * 1024, values: [] }],
    ['package/release/personal-preview-manifest.json', { maximum: 1024 * 1024, values: [] }],
  ]);
  unpack.on('entry', (header, stream, next) => {
    const target = wanted.get(header.name);
    const chunks = [];
    let bytes = 0;
    stream.on('data', (chunk) => {
      bytes += chunk.length;
      if (target && bytes <= target.maximum) chunks.push(Buffer.from(chunk));
    });
    stream.once('error', next);
    stream.once('end', () => {
      try {
        if (target) {
          if (header.type !== 'file' || bytes < 2 || bytes > target.maximum || target.values.length !== 0) {
            fail('npm_production_candidate_package_invalid');
          }
          target.values.push(Buffer.concat(chunks));
        }
        next();
      } catch (error) { next(error); }
    });
    stream.resume();
  });
  try { await pipeline(createReadStream(path), createGunzip(), unpack); }
  catch { fail('npm_production_candidate_package_invalid'); }
  if ([...wanted.values()].some((target) => target.values.length !== 1)) {
    fail('npm_production_candidate_package_invalid');
  }
  let packageJSON;
  try { packageJSON = JSON.parse(wanted.get('package/package.json').values[0].toString('utf8')); }
  catch { fail('npm_production_candidate_package_invalid'); }
  return Object.freeze({
    manifestBytes: wanted.get('package/release/personal-preview-manifest.json').values[0].toString('utf8'),
    packageJSON,
  });
}

export async function buildNpmProductionCandidate({
  catalogRoot, commit, evidenceRoot, outputRoot, securityRoot, tarballPath, universalRunID,
} = {}) {
  if (![catalogRoot, evidenceRoot, outputRoot, securityRoot, tarballPath].every((path) =>
    typeof path === 'string' && isAbsolute(path) && resolve(path) === path) ||
      !SHA40.test(commit ?? '') || !Number.isSafeInteger(universalRunID) || universalRunID < 1 ||
      existsSync(outputRoot)) {
    fail('npm_production_candidate_arguments_invalid');
  }
  const tarballName = basename(tarballPath);
  if (!SAFE_TARBALL.test(tarballName)) fail('npm_production_candidate_arguments_invalid');
  const receipt = readCanonicalJSON(
    join(catalogRoot, 'catalog-build-receipt.json'),
    'pulse.personal_release_catalog_build.v3',
  );
  const manifest = readCanonicalJSON(
    join(catalogRoot, 'personal-preview-manifest.json'),
    'pulse.personal_release_artifact_set.v1',
  );
  const snapshot = readCanonicalJSON(
    join(catalogRoot, 'snapshot.json'),
    'pulse.release_snapshot_envelope.v1',
  );
  if (receipt.value.production_ready !== true || receipt.value.target_count !== DESKTOP_TARGET_IDS.length ||
      receipt.value.artifact_count !== 2 + DESKTOP_TARGET_IDS.length * 2 ||
      receipt.value.host_target_count !== 18 || receipt.value.release_epoch !== 9 ||
      canonical(receipt.value.target_ids) !== canonical(DESKTOP_TARGET_IDS) ||
      Object.keys(manifest.value.payload?.targets ?? {}).sort().join('\0') !== DESKTOP_TARGET_IDS.join('\0') ||
      manifest.value.payload?.release?.package !== PACKAGE_NAME ||
      snapshot.value.payload?.artifact_set?.sha256 !== receipt.value.artifact_set_digest ||
      snapshot.value.payload?.release_epoch !== receipt.value.release_epoch ||
      digestFile(join(catalogRoot, 'personal-preview-manifest.json')) !== receipt.value.artifact_set_digest ||
      digestFile(join(catalogRoot, 'snapshot.json')) !== receipt.value.snapshot_digest) {
    fail('npm_production_candidate_catalog_invalid');
  }
  const evidence = validateUniversalEvidence(evidenceRoot, commit);
  const packageDocumentsValue = await packageDocuments(tarballPath);
  const packageJSON = packageDocumentsValue.packageJSON;
  if (packageDocumentsValue.manifestBytes !== manifest.bytes || packageJSON?.name !== PACKAGE_NAME ||
      packageJSON?.version !== manifest.value.payload.release.version || packageJSON?.repository?.url !== REPOSITORY) {
    fail('npm_production_candidate_package_mismatch');
  }
  const sha256 = digestFile(tarballPath);
  const security = readCanonicalJSON(
    join(securityRoot, 'dependency-receipt.json'),
    'pulse.release_dependency_receipt.v1',
  );
  if (security.value.package !== PACKAGE_NAME || security.value.version !== packageJSON.version ||
      security.value.package_sha256 !== sha256 || security.value.audit?.high !== 0 ||
      security.value.audit?.critical !== 0 || !Number.isSafeInteger(security.value.dependency_count) ||
      security.value.dependency_count < 1 ||
      digestFile(join(securityRoot, 'sbom.cdx.json')) !== security.value.sbom_sha256 ||
      digestFile(join(securityRoot, 'licenses.json')) !== security.value.license_inventory_sha256) {
    fail('npm_production_candidate_security_invalid');
  }
  if (evidence.package_sha256 !== sha256 ||
      evidence.artifact_set_digest !== receipt.value.artifact_set_digest ||
      evidence.snapshot_digest !== receipt.value.snapshot_digest) fail('npm_production_candidate_evidence_invalid');
  const matrix = loadNativeUniversalMatrix();
  const candidate = Object.freeze({
    artifact_set_digest: receipt.value.artifact_set_digest,
    commit,
    host_target_count: matrix.harnesses.length * matrix.targets.length,
    hosts: matrix.harnesses.map((harness) => harness.host).sort(),
    dependency_count: security.value.dependency_count,
    license_inventory_sha256: security.value.license_inventory_sha256,
    package: PACKAGE_NAME,
    production: true,
    production_ready: true,
    release_epoch: receipt.value.release_epoch,
    schema: 'pulse.npm_production_candidate.v1',
    sha256,
    sbom_sha256: security.value.sbom_sha256,
    snapshot_digest: receipt.value.snapshot_digest,
    support_claim: false,
    targets: [...DESKTOP_TARGET_IDS],
    tarball: tarballName,
    universal_run_id: universalRunID,
    version: packageJSON.version,
  });
  mkdirSync(dirname(outputRoot), { recursive: true, mode: 0o700 });
  mkdirSync(outputRoot, { mode: 0o700 });
  let complete = false;
  try {
    const destinationTarball = join(outputRoot, tarballName);
    copyFileSync(tarballPath, destinationTarball);
    chmodSync(destinationTarball, 0o600);
    if (digestFile(destinationTarball) !== sha256) fail('npm_production_candidate_copy_mismatch');
    writeFileSync(join(outputRoot, 'candidate.json'), `${canonical(candidate)}\n`, {
      encoding: 'utf8', flag: 'wx', mode: 0o600,
    });
    for (const name of ['dependency-receipt.json', 'licenses.json', 'sbom.cdx.json']) {
      copyFileSync(join(securityRoot, name), join(outputRoot, name));
      chmodSync(join(outputRoot, name), 0o600);
    }
    complete = true;
    return candidate;
  } finally {
    if (!complete) rmSync(outputRoot, { recursive: true, force: true });
  }
}

function parseCLI(argv) {
  const values = {};
  for (let index = 0; index < argv.length; index += 2) {
    const name = argv[index];
    const value = argv[index + 1];
    if (!['--catalog', '--commit', '--evidence', '--output', '--security', '--tarball', '--universal-run-id'].includes(name) ||
        value === undefined || Object.hasOwn(values, name)) {
      fail('npm_production_candidate_arguments_invalid');
    }
    values[name] = value;
  }
  if (Object.keys(values).length !== 7 ||
      ![values['--catalog'], values['--evidence'], values['--output'], values['--security'], values['--tarball']].every(isAbsolute)) {
    fail('npm_production_candidate_arguments_invalid');
  }
  const universalRunID = Number(values['--universal-run-id']);
  return Object.freeze({
    catalogRoot: resolve(values['--catalog']),
    commit: values['--commit'],
    evidenceRoot: resolve(values['--evidence']),
    outputRoot: resolve(values['--output']),
    securityRoot: resolve(values['--security']),
    tarballPath: resolve(values['--tarball']),
    universalRunID,
  });
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  const candidate = await buildNpmProductionCandidate(parseCLI(process.argv.slice(2)));
  process.stdout.write(`${canonical(candidate)}\n`);
}
