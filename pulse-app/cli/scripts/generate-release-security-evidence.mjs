#!/usr/bin/env node

import { createHash } from 'node:crypto';
import { lstatSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

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
  fail('release_security_value_invalid');
}

function regularFile(path, maximum = 1024 * 1024 * 1024) {
  let info;
  try { info = lstatSync(path); } catch { fail('release_security_input_missing'); }
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.size < 1 || info.size > maximum) {
    fail('release_security_input_unsafe');
  }
  return info;
}

function readJSON(path, maximum) {
  regularFile(path, maximum);
  try { return JSON.parse(readFileSync(path, 'utf8')); } catch { fail('release_security_input_invalid'); }
}

function packageIdentity(path, entry) {
  const packageJSON = readJSON(join(path, 'package.json'), 256 * 1024);
  if (typeof packageJSON.name !== 'string' || typeof packageJSON.version !== 'string' ||
      packageJSON.name !== entry.name || packageJSON.version !== entry.version) fail('release_security_package_invalid');
  const licenses = [];
  const value = packageJSON.license ?? packageJSON.licenses;
  if (typeof value === 'string' && value.length > 0) licenses.push(value);
  else if (Array.isArray(value)) {
    for (const item of value) {
      const license = typeof item === 'string' ? item : item?.type;
      if (typeof license === 'string' && license.length > 0) licenses.push(license);
    }
  }
  if (licenses.length === 0) licenses.push('NOASSERTION');
  return Object.freeze({
    licenses: [...new Set(licenses)].sort(),
    name: entry.name,
    version: entry.version,
  });
}

function productionPackages(installRoot) {
  const lock = readJSON(join(installRoot, 'package-lock.json'), 16 * 1024 * 1024);
  if (lock.lockfileVersion !== 3 || !lock.packages || Array.isArray(lock.packages)) fail('release_security_lock_invalid');
  const packages = [];
  for (const [relativePath, entry] of Object.entries(lock.packages)) {
    if (!relativePath.startsWith('node_modules/') || entry.dev === true || entry.link === true) continue;
    const path = resolve(installRoot, relativePath);
    if (!path.startsWith(`${resolve(installRoot)}/node_modules/`)) fail('release_security_lock_invalid');
    const identity = packageIdentity(path, entry);
    packages.push({
      ...identity,
      integrity: typeof entry.integrity === 'string' ? entry.integrity : null,
      optional: entry.optional === true,
    });
  }
  packages.sort((left, right) => `${left.name}\0${left.version}`.localeCompare(`${right.name}\0${right.version}`));
  if (packages.length < 2 || packages[0].name === undefined) fail('release_security_lock_invalid');
  return packages;
}

export function generateReleaseSecurityEvidence({ auditPath, installRoot, outputRoot, tarballPath } = {}) {
  if (![auditPath, installRoot, outputRoot, tarballPath].every((path) =>
    typeof path === 'string' && isAbsolute(path) && resolve(path) === path)) fail('release_security_arguments_invalid');
  const audit = readJSON(auditPath, 8 * 1024 * 1024);
  const vulnerabilities = audit.metadata?.vulnerabilities;
  if (!vulnerabilities || !Number.isSafeInteger(vulnerabilities.high) || !Number.isSafeInteger(vulnerabilities.critical) ||
      vulnerabilities.high !== 0 || vulnerabilities.critical !== 0) fail('release_security_audit_failed');
  const tarball = regularFile(tarballPath);
  const tarballSHA256 = createHash('sha256').update(readFileSync(tarballPath)).digest('hex');
  const packages = productionPackages(installRoot);
  const components = packages.map((entry) => ({
    'bom-ref': `pkg:npm/${encodeURIComponent(entry.name)}@${entry.version}`,
    licenses: entry.licenses.map((id) => ({ license: { id } })),
    name: entry.name,
    purl: `pkg:npm/${encodeURIComponent(entry.name)}@${entry.version}`,
    scope: entry.optional ? 'optional' : 'required',
    type: 'library',
    version: entry.version,
  }));
  const sbom = Object.freeze({
    bomFormat: 'CycloneDX',
    components,
    metadata: {
      component: {
        hashes: [{ alg: 'SHA-256', content: tarballSHA256 }],
        name: '@zbs-gg/pulse',
        type: 'application',
        version: '0.7.2',
      },
    },
    serialNumber: `urn:uuid:${tarballSHA256.slice(0, 8)}-${tarballSHA256.slice(8, 12)}-${tarballSHA256.slice(12, 16)}-${tarballSHA256.slice(16, 20)}-${tarballSHA256.slice(20, 32)}`,
    specVersion: '1.5',
    version: 1,
  });
  const licenses = Object.freeze({
    dependencies: packages.map(({ licenses: values, name, version }) => ({ licenses: values, name, version })),
    dependency_count: packages.length,
    package: '@zbs-gg/pulse',
    package_license: 'AGPL-3.0-only',
    schema: 'pulse.release_license_inventory.v1',
    version: '0.7.2',
  });
  const receipt = Object.freeze({
    audit: {
      command: 'npm audit --omit=dev --audit-level=high',
      critical: vulnerabilities.critical,
      high: vulnerabilities.high,
      total: vulnerabilities.total,
    },
    content_free: true,
    dependency_count: packages.length,
    license_inventory_sha256: createHash('sha256').update(`${canonical(licenses)}\n`).digest('hex'),
    package: '@zbs-gg/pulse',
    package_bytes: tarball.size,
    package_sha256: tarballSHA256,
    sbom_sha256: createHash('sha256').update(`${canonical(sbom)}\n`).digest('hex'),
    schema: 'pulse.release_dependency_receipt.v1',
    version: '0.7.2',
  });
  mkdirSync(outputRoot, { recursive: false, mode: 0o700 });
  for (const [name, value] of [
    ['sbom.cdx.json', sbom], ['licenses.json', licenses], ['dependency-receipt.json', receipt],
  ]) writeFileSync(join(outputRoot, name), `${canonical(value)}\n`, { encoding: 'utf8', flag: 'wx', mode: 0o600 });
  return receipt;
}

function args(argv) {
  const values = {};
  for (let index = 0; index < argv.length; index += 2) {
    const name = argv[index];
    const value = argv[index + 1];
    if (!['--audit', '--install-root', '--output', '--tarball'].includes(name) || !value || Object.hasOwn(values, name)) {
      fail('release_security_arguments_invalid');
    }
    values[name] = value;
  }
  if (Object.keys(values).length !== 4) fail('release_security_arguments_invalid');
  return Object.fromEntries(Object.entries(values).map(([name, value]) => [name, resolve(value)]));
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  const values = args(process.argv.slice(2));
  const receipt = generateReleaseSecurityEvidence({
    auditPath: values['--audit'],
    installRoot: values['--install-root'],
    outputRoot: values['--output'],
    tarballPath: values['--tarball'],
  });
  process.stdout.write(`${canonical({
    dependency_count: receipt.dependency_count,
    package_sha256: receipt.package_sha256,
    schema: receipt.schema,
  })}\n`);
}
