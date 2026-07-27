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

function exactObject(value, keys) {
  return value && typeof value === 'object' && !Array.isArray(value) &&
    Object.keys(value).sort().join('\0') === [...keys].sort().join('\0');
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
      if (files.length > DESKTOP_TARGET_IDS.length) fail('npm_production_candidate_evidence_ambiguous');
    }
  };
  visit(root, 0);
  if (files.length !== DESKTOP_TARGET_IDS.length) fail('npm_production_candidate_evidence_incomplete');
  return files;
}

function validTokenEconomy(value) {
  return value && typeof value === 'object' && !Array.isArray(value) &&
    ['collecting_baseline', 'estimated', 'measured'].includes(value.state);
}

function validateUniversalEvidence(root, commit) {
  const matrix = loadNativeUniversalMatrix();
  const declared = new Map(matrix.targets.map((target) => [target.target_id, target]));
  const seen = new Set();
  for (const path of evidenceFiles(root)) {
    regularFile(path, 512 * 1024);
    let evidence;
    try { evidence = JSON.parse(readFileSync(path, 'utf8')); } catch { fail('npm_production_candidate_evidence_invalid'); }
    const targetID = evidence?.target?.target_id;
    const target = declared.get(targetID);
    if (!target || seen.has(targetID) ||
        evidence.schema !== 'pulse.native_universal_target_evidence.v1' ||
        evidence.authority !== 'pr-fixture' || evidence.production !== false || evidence.support_claim !== false ||
        evidence.commit !== commit || !exactObject(evidence.target, ['architecture', 'libc', 'platform', 'runner', 'target_id']) ||
        canonical(evidence.target) !== canonical(target) ||
        evidence.harness?.host !== matrix.harness.host || evidence.harness?.package !== matrix.harness.package ||
        evidence.harness?.version !== matrix.harness.version ||
        !SHA256.test(evidence.package?.sha256 ?? '') || !Number.isSafeInteger(evidence.package?.bytes) || evidence.package.bytes < 1 ||
        evidence.runtime?.full_retrieval !== true || evidence.runtime?.lifecycle_ready !== true ||
        evidence.first_value?.boundary !== 'fresh_session_context' || evidence.first_value?.visible_card !== true ||
        evidence.first_value?.same_object_recalled !== true || !Number.isSafeInteger(evidence.first_value?.milliseconds) ||
        evidence.first_value.milliseconds < 0 || evidence.first_value.milliseconds > 60_000 ||
        !validTokenEconomy(evidence.token_economy) ||
        evidence.consolidation?.phase !== 'report_ready' || evidence.consolidation?.cli_parity !== true ||
        evidence.consolidation?.mcp_parity !== true || evidence.consolidation?.memory_home_visible !== true ||
        evidence.consolidation?.sources_byte_preserved !== true ||
        evidence.consolidation?.mutation_authority_exercised !== false) {
      fail('npm_production_candidate_evidence_invalid');
    }
    const kinds = ['daemon', 'embedder-runtime', 'model', 'plugin-runtime'];
    if (!Array.isArray(evidence.release?.artifact_ids) ||
        kinds.some((kind) => evidence.release.artifact_ids.filter((id) =>
          typeof id === 'string' && id.endsWith(`-${kind}`)).length !== 1) ||
        evidence.release.artifact_ids.filter((id) => typeof id === 'string' && id.endsWith('-presence-helper')).length !==
          (target.platform === 'darwin' ? 1 : 0)) {
      fail('npm_production_candidate_evidence_invalid');
    }
    seen.add(targetID);
  }
  if (canonical([...seen].sort()) !== canonical(DESKTOP_TARGET_IDS)) {
    fail('npm_production_candidate_evidence_incomplete');
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
  catalogRoot, commit, evidenceRoot, outputRoot, tarballPath, universalRunID,
} = {}) {
  if (![catalogRoot, evidenceRoot, outputRoot, tarballPath].every((path) =>
    typeof path === 'string' && isAbsolute(path) && resolve(path) === path) ||
      !SHA40.test(commit ?? '') || !Number.isSafeInteger(universalRunID) || universalRunID < 1 ||
      existsSync(outputRoot)) {
    fail('npm_production_candidate_arguments_invalid');
  }
  const tarballName = basename(tarballPath);
  if (!SAFE_TARBALL.test(tarballName)) fail('npm_production_candidate_arguments_invalid');
  const receipt = readCanonicalJSON(
    join(catalogRoot, 'catalog-build-receipt.json'),
    'pulse.personal_release_catalog_build.v2',
  );
  const manifest = readCanonicalJSON(
    join(catalogRoot, 'personal-preview-manifest.json'),
    'pulse.release_catalog_envelope.v2',
  );
  if (receipt.value.production_ready !== true || receipt.value.target_count !== DESKTOP_TARGET_IDS.length ||
      receipt.value.artifact_count !== 2 + DESKTOP_TARGET_IDS.length * 2 ||
      canonical(receipt.value.target_ids) !== canonical(DESKTOP_TARGET_IDS) ||
      Object.keys(manifest.value.payload?.targets ?? {}).sort().join('\0') !== DESKTOP_TARGET_IDS.join('\0') ||
      manifest.value.payload?.release?.package !== PACKAGE_NAME) {
    fail('npm_production_candidate_catalog_invalid');
  }
  validateUniversalEvidence(evidenceRoot, commit);
  const packageDocumentsValue = await packageDocuments(tarballPath);
  const packageJSON = packageDocumentsValue.packageJSON;
  if (packageDocumentsValue.manifestBytes !== manifest.bytes || packageJSON?.name !== PACKAGE_NAME ||
      packageJSON?.version !== manifest.value.payload.release.version || packageJSON?.repository?.url !== REPOSITORY) {
    fail('npm_production_candidate_package_mismatch');
  }
  const sha256 = digestFile(tarballPath);
  const candidate = Object.freeze({
    commit,
    package: PACKAGE_NAME,
    production: true,
    schema: 'pulse.npm_production_candidate.v1',
    sha256,
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
    if (!['--catalog', '--commit', '--evidence', '--output', '--tarball', '--universal-run-id'].includes(name) ||
        value === undefined || Object.hasOwn(values, name)) {
      fail('npm_production_candidate_arguments_invalid');
    }
    values[name] = value;
  }
  if (Object.keys(values).length !== 6 ||
      ![values['--catalog'], values['--evidence'], values['--output'], values['--tarball']].every(isAbsolute)) {
    fail('npm_production_candidate_arguments_invalid');
  }
  const universalRunID = Number(values['--universal-run-id']);
  return Object.freeze({
    catalogRoot: resolve(values['--catalog']),
    commit: values['--commit'],
    evidenceRoot: resolve(values['--evidence']),
    outputRoot: resolve(values['--output']),
    tarballPath: resolve(values['--tarball']),
    universalRunID,
  });
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  const candidate = await buildNpmProductionCandidate(parseCLI(process.argv.slice(2)));
  process.stdout.write(`${canonical(candidate)}\n`);
}
