#!/usr/bin/env node

import { createHash } from 'node:crypto';
import { createReadStream } from 'node:fs';
import {
  chmodSync, copyFileSync, existsSync, lstatSync, mkdirSync, readFileSync, writeFileSync,
} from 'node:fs';
import { basename, isAbsolute, join, resolve } from 'node:path';
import { pipeline } from 'node:stream/promises';
import { fileURLToPath } from 'node:url';
import { createGunzip } from 'node:zlib';
import { extract } from 'tar-stream';

const SHA40 = /^[a-f0-9]{40}$/;
const SHA256 = /^[a-f0-9]{64}$/;

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
  fail('npm_production_inputs_value_invalid');
}

function file(path, maximum = 1024 * 1024 * 1024) {
  let info;
  try { info = lstatSync(path); } catch { fail('npm_production_inputs_missing'); }
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.size < 1 || info.size > maximum) {
    fail('npm_production_inputs_unsafe');
  }
  return Object.freeze({
    bytes: info.size,
    sha256: createHash('sha256').update(readFileSync(path)).digest('hex'),
  });
}

function canonicalJSON(path, schema, maximum = 2 * 1024 * 1024) {
  file(path, maximum);
  const bytes = readFileSync(path, 'utf8');
  let value;
  try { value = JSON.parse(bytes); } catch { fail('npm_production_inputs_invalid'); }
  if (bytes !== `${canonical(value)}\n` || value.schema !== schema) fail('npm_production_inputs_invalid');
  return Object.freeze({ bytes, value });
}

async function packageDocuments(path) {
  const unpack = extract();
  const wanted = new Map([
    ['package/package.json', { maximum: 64 * 1024, value: null }],
    ['package/release/personal-preview-manifest.json', { maximum: 2 * 1024 * 1024, value: null }],
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
          if (header.type !== 'file' || target.value !== null || bytes < 2 || bytes > target.maximum) {
            fail('npm_production_inputs_package_invalid');
          }
          target.value = Buffer.concat(chunks);
        }
        next();
      } catch (error) { next(error); }
    });
    stream.resume();
  });
  try { await pipeline(createReadStream(path), createGunzip(), unpack); }
  catch { fail('npm_production_inputs_package_invalid'); }
  if ([...wanted.values()].some((entry) => entry.value === null)) fail('npm_production_inputs_package_invalid');
  let packageJSON;
  try { packageJSON = JSON.parse(wanted.get('package/package.json').value.toString('utf8')); }
  catch { fail('npm_production_inputs_package_invalid'); }
  return Object.freeze({
    manifest: wanted.get('package/release/personal-preview-manifest.json').value.toString('utf8'),
    packageJSON,
  });
}

export async function buildNpmProductionInputs({
  catalogRoot, commit, outputRoot, securityRoot, tarballPath, universalRunID,
} = {}) {
  if (![catalogRoot, outputRoot, securityRoot, tarballPath].every((path) =>
    typeof path === 'string' && isAbsolute(path) && resolve(path) === path) ||
      !SHA40.test(commit ?? '') || !Number.isSafeInteger(universalRunID) || universalRunID < 1 || existsSync(outputRoot)) {
    fail('npm_production_inputs_arguments_invalid');
  }
  const catalog = canonicalJSON(join(catalogRoot, 'catalog-build-receipt.json'), 'pulse.personal_release_catalog_build.v3');
  const artifactSet = canonicalJSON(join(catalogRoot, 'personal-preview-manifest.json'), 'pulse.personal_release_artifact_set.v1');
  const snapshot = canonicalJSON(join(catalogRoot, 'snapshot.json'), 'pulse.release_snapshot_envelope.v1');
  const security = canonicalJSON(join(securityRoot, 'dependency-receipt.json'), 'pulse.release_dependency_receipt.v1');
  const tarball = file(tarballPath);
  const documents = await packageDocuments(tarballPath);
  if (catalog.value.production_ready !== true || catalog.value.release_epoch !== 9 ||
      catalog.value.host_target_count !== 18 || catalog.value.artifact_set_digest !== file(join(catalogRoot, 'personal-preview-manifest.json')).sha256 ||
      catalog.value.snapshot_digest !== file(join(catalogRoot, 'snapshot.json')).sha256 ||
      snapshot.value.payload?.artifact_set?.sha256 !== catalog.value.artifact_set_digest ||
      artifactSet.value.payload?.release?.package !== '@zbs-gg/pulse' ||
      artifactSet.value.payload?.release?.version !== '0.8.0' || documents.manifest !== artifactSet.bytes ||
      documents.packageJSON?.name !== '@zbs-gg/pulse' || documents.packageJSON?.version !== '0.8.0' ||
      security.value.package_sha256 !== tarball.sha256 || security.value.audit?.high !== 0 ||
      security.value.audit?.critical !== 0 || !SHA256.test(security.value.sbom_sha256 ?? '') ||
      !SHA256.test(security.value.license_inventory_sha256 ?? '')) fail('npm_production_inputs_invalid');
  const receipt = Object.freeze({
    artifact_set_digest: catalog.value.artifact_set_digest,
    commit,
    package: '@zbs-gg/pulse',
    package_bytes: tarball.bytes,
    package_sha256: tarball.sha256,
    production_ready: false,
    release_epoch: 9,
    schema: 'pulse.npm_production_inputs.v1',
    snapshot_digest: catalog.value.snapshot_digest,
    support_claim: false,
    tarball: basename(tarballPath),
    universal_run_id: universalRunID,
    version: '0.8.0',
  });
  mkdirSync(outputRoot, { mode: 0o700 });
  for (const [source, destination] of [
    [tarballPath, join(outputRoot, receipt.tarball)],
    ...['dependency-receipt.json', 'licenses.json', 'sbom.cdx.json']
      .map((name) => [join(securityRoot, name), join(outputRoot, name)]),
  ]) {
    file(source);
    copyFileSync(source, destination);
    chmodSync(destination, 0o600);
  }
  writeFileSync(join(outputRoot, 'candidate-inputs.json'), `${canonical(receipt)}\n`, { mode: 0o600, flag: 'wx' });
  return receipt;
}

function args(argv) {
  const values = {};
  for (let index = 0; index < argv.length; index += 2) {
    const name = argv[index];
    const value = argv[index + 1];
    if (!['--catalog', '--commit', '--output', '--security', '--tarball', '--universal-run-id'].includes(name) ||
        !value || Object.hasOwn(values, name)) fail('npm_production_inputs_arguments_invalid');
    values[name] = value;
  }
  if (Object.keys(values).length !== 6) fail('npm_production_inputs_arguments_invalid');
  return {
    catalogRoot: resolve(values['--catalog']), commit: values['--commit'], outputRoot: resolve(values['--output']),
    securityRoot: resolve(values['--security']), tarballPath: resolve(values['--tarball']),
    universalRunID: Number(values['--universal-run-id']),
  };
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  process.stdout.write(`${canonical(await buildNpmProductionInputs(args(process.argv.slice(2))))}\n`);
}
