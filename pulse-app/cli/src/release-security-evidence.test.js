import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import test from 'node:test';

import { generateReleaseSecurityEvidence } from '../scripts/generate-release-security-evidence.mjs';

function writeJSON(path, value) {
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  writeFileSync(path, `${JSON.stringify(value)}\n`, { mode: 0o600 });
}

function fixture(t, { high = 0 } = {}) {
  const root = mkdtempSync(join(tmpdir(), 'pulse-release-security.'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const installRoot = join(root, 'install');
  const packages = {
    'node_modules/@zbs-gg/pulse': { name: '@zbs-gg/pulse', version: '0.8.0', license: 'AGPL-3.0-only' },
    'node_modules/example': { name: 'example', version: '1.2.3', license: 'MIT' },
  };
  for (const [path, packageJSON] of Object.entries(packages)) {
    writeJSON(join(installRoot, path, 'package.json'), packageJSON);
  }
  writeJSON(join(installRoot, 'package-lock.json'), {
    lockfileVersion: 3,
    packages: Object.fromEntries(Object.entries(packages).map(([path, value]) => [path, {
      name: value.name, version: value.version,
    }])),
  });
  const auditPath = join(root, 'audit.json');
  writeJSON(auditPath, { metadata: { vulnerabilities: { critical: 0, high, total: high } } });
  const tarballPath = join(root, 'zbs-gg-pulse-0.8.0.tgz');
  writeFileSync(tarballPath, 'package bytes\n', { mode: 0o600 });
  return {
    auditPath: resolve(auditPath), installRoot: resolve(installRoot),
    outputRoot: resolve(root, 'output'), tarballPath: resolve(tarballPath),
  };
}

test('release security evidence is content-free and binds SBOM, licenses, audit, and tarball', (t) => {
  const current = fixture(t);
  const receipt = generateReleaseSecurityEvidence(current);
  assert.equal(receipt.audit.high, 0);
  assert.equal(receipt.audit.critical, 0);
  assert.equal(receipt.dependency_count, 2);
  assert.equal(receipt.package_sha256, createHash('sha256').update(readFileSync(current.tarballPath)).digest('hex'));
  assert.equal(JSON.parse(readFileSync(join(current.outputRoot, 'licenses.json'))).dependencies[1].licenses[0], 'MIT');
  assert.match(receipt.sbom_sha256, /^[a-f0-9]{64}$/);
});

test('release security evidence refuses any high shipped vulnerability', (t) => {
  assert.throws(() => generateReleaseSecurityEvidence(fixture(t, { high: 1 })), {
    code: 'release_security_audit_failed',
  });
});
