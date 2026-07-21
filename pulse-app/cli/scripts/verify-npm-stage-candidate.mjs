#!/usr/bin/env node

import { createHash } from 'node:crypto';
import { createReadStream, lstatSync, readFileSync } from 'node:fs';
import { basename, dirname, isAbsolute, resolve } from 'node:path';
import { pipeline } from 'node:stream/promises';
import { fileURLToPath } from 'node:url';
import { createGunzip } from 'node:zlib';
import { extract } from 'tar-stream';

const TARGETS = Object.freeze([
  'darwin-arm64', 'darwin-x64', 'linux-arm64-gnu',
  'linux-x64-gnu', 'win32-arm64', 'win32-x64',
]);
const SHA40 = /^[a-f0-9]{40}$/;
const SHA256 = /^[a-f0-9]{64}$/;
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
  fail('npm_candidate_value_invalid');
}

function exactObject(value, keys) {
  return value && typeof value === 'object' && !Array.isArray(value) &&
    Object.keys(value).sort().join('\0') === [...keys].sort().join('\0');
}

function regularFile(path, { maximumBytes, minimumBytes = 1 }) {
  let info;
  try { info = lstatSync(path); } catch { fail('npm_candidate_file_missing'); }
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 ||
      info.size < minimumBytes || info.size > maximumBytes) {
    fail('npm_candidate_file_unsafe');
  }
  return info;
}

async function sha256File(path) {
  const hash = createHash('sha256');
  const stream = createReadStream(path);
  stream.on('data', (chunk) => hash.update(chunk));
  await new Promise((accept, reject) => {
    stream.once('end', accept);
    stream.once('error', reject);
  });
  return hash.digest('hex');
}

async function packageJSONFromTarball(path) {
  const unpack = extract();
  let packageJSON = null;
  let packageEntries = 0;
  unpack.on('entry', (header, stream, next) => {
    const chunks = [];
    let bytes = 0;
    stream.on('data', (chunk) => {
      bytes += chunk.length;
      if (header.name === 'package/package.json' && bytes <= 64 * 1024) chunks.push(Buffer.from(chunk));
    });
    stream.once('error', next);
    stream.once('end', () => {
      try {
        if (header.name === 'package/package.json') {
          packageEntries += 1;
          if (header.type !== 'file' || bytes < 2 || bytes > 64 * 1024 || packageEntries !== 1) {
            fail('npm_candidate_package_invalid');
          }
          packageJSON = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        }
        next();
      } catch (error) { next(error); }
    });
    stream.resume();
  });
  try { await pipeline(createReadStream(path), createGunzip(), unpack); }
  catch { fail('npm_candidate_tarball_invalid'); }
  if (packageEntries !== 1 || !packageJSON) fail('npm_candidate_package_invalid');
  return packageJSON;
}

function parseArgs(argv) {
  const values = {};
  for (let index = 0; index < argv.length; index += 2) {
    const name = argv[index];
    const value = argv[index + 1];
    if (!['--candidate', '--commit', '--sha256'].includes(name) || value === undefined || values[name]) {
      fail('npm_candidate_arguments_invalid');
    }
    values[name] = value;
  }
  if (Object.keys(values).length !== 3) fail('npm_candidate_arguments_invalid');
  return values;
}

export async function verifyNpmStageCandidate({ candidatePath, expectedCommit, expectedSHA256 }) {
  if (!isAbsolute(candidatePath) || resolve(candidatePath) !== candidatePath ||
      !SHA40.test(expectedCommit ?? '') || !SHA256.test(expectedSHA256 ?? '')) {
    fail('npm_candidate_arguments_invalid');
  }
  regularFile(candidatePath, { maximumBytes: 64 * 1024 });
  const raw = readFileSync(candidatePath, 'utf8');
  let candidate;
  try { candidate = JSON.parse(raw); } catch { fail('npm_candidate_receipt_invalid'); }
  if (raw !== `${canonical(candidate)}\n` || !exactObject(candidate, [
    'commit', 'package', 'production', 'schema', 'sha256', 'support_claim',
    'targets', 'tarball', 'universal_run_id', 'version',
  ]) || candidate.schema !== 'pulse.npm_production_candidate.v1' ||
      candidate.package !== '@zbs-gg/pulse' || candidate.commit !== expectedCommit ||
      candidate.sha256 !== expectedSHA256 || candidate.production !== true ||
      candidate.support_claim !== false || !SAFE_TARBALL.test(candidate.tarball ?? '') ||
      !Number.isSafeInteger(candidate.universal_run_id) || candidate.universal_run_id < 1 ||
      typeof candidate.version !== 'string' || !/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(candidate.version) ||
      !Array.isArray(candidate.targets) || JSON.stringify(candidate.targets) !== JSON.stringify(TARGETS)) {
    fail('npm_candidate_receipt_invalid');
  }
  const tarballPath = resolve(dirname(candidatePath), candidate.tarball);
  if (dirname(tarballPath) !== dirname(candidatePath) || basename(tarballPath) !== candidate.tarball) {
    fail('npm_candidate_tarball_path_invalid');
  }
  regularFile(tarballPath, { maximumBytes: 1024 * 1024 * 1024 });
  if (await sha256File(tarballPath) !== candidate.sha256) fail('npm_candidate_digest_mismatch');
  const packageJSON = await packageJSONFromTarball(tarballPath);
  if (packageJSON?.name !== candidate.package) fail('npm_candidate_package_name_mismatch');
  if (packageJSON?.version !== candidate.version) fail('npm_candidate_package_version_mismatch');
  if (packageJSON?.repository?.url !== 'git+https://github.com/zbs-gg/pulse.git') {
    fail('npm_candidate_package_repository_mismatch');
  }
  return Object.freeze({
    schema: 'pulse.npm_stage_verification.v1',
    commit: candidate.commit,
    package: candidate.package,
    sha256: candidate.sha256,
    stage_tag: 'preview',
    targets: candidate.targets,
    tarball: candidate.tarball,
    universal_run_id: candidate.universal_run_id,
    version: candidate.version,
  });
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  const values = parseArgs(process.argv.slice(2));
  const receipt = await verifyNpmStageCandidate({
    candidatePath: resolve(values['--candidate']),
    expectedCommit: values['--commit'],
    expectedSHA256: values['--sha256'],
  });
  process.stdout.write(`${canonical(receipt)}\n`);
}
