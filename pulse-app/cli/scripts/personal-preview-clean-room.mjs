#!/usr/bin/env node

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptRoot = dirname(fileURLToPath(import.meta.url));
const productE2E = resolve(scriptRoot, 'codex-product-e2e.mjs');

const result = spawnSync(process.execPath, [productE2E], {
  cwd: resolve(scriptRoot, '..'),
  env: process.env,
  encoding: 'utf8',
  stdio: ['ignore', 'pipe', 'pipe'],
  timeout: 15 * 60_000,
  maxBuffer: 16 * 1024 * 1024,
});
if (result.status !== 0) {
  process.stderr.write(result.stderr ?? '');
  process.stderr.write(result.stdout ?? '');
  process.exit(result.status ?? 1);
}

const evidenceLine = String(result.stdout).split(/\r?\n/).find((line) => line.startsWith('{'));
assert.ok(evidenceLine, 'packed Codex E2E did not emit evidence');
const product = JSON.parse(evidenceLine);
assert.equal(product.schema, 'pulse.codex_product_e2e.v1');
assert.equal(product.authority, 'synthetic-test');
assert.equal(product.production_install_proof, false);
assert.equal(product.package_source, 'npm-pack');
assert.equal(product.system_go_exposed, false);
assert.equal(product.system_python_exposed, false);
assert.equal(product.personal_only_package, true);
assert.equal(product.external_publication_performed, false);
assert.equal(product.full_retrieval, true);
assert.equal(product.package_version, '0.7.2');
assert.match(product.packed_tarball_sha256, /^[a-f0-9]{64}$/);
assert.equal(Number.isInteger(product.packed_tarball_bytes) && product.packed_tarball_bytes > 0, true);
assert.equal(product.exact_tarball_bound, true);
assert.equal(product.tray_save_proof, false);
assert.equal(product.unassigned_assignment_proof, false);

const evidence = {
  schema: 'pulse.personal_preview_clean_room.v1',
  authority: 'synthetic-test',
  content_free: true,
  package_source: 'npm-pack',
  package_version: product.package_version,
  packed_tarball_sha256: product.packed_tarball_sha256,
  packed_tarball_bytes: product.packed_tarball_bytes,
  exact_tarball_bound: true,
  packed_runtime: true,
  runtime_path: { node: true, codex: true, git: true, go: false, python: false },
  full_retrieval: true,
  personal_only_package: true,
  remote_side_effects: false,
  production_install_proof: false,
  tray_save_proof: false,
  unassigned_assignment_proof: false,
  physical_attestation_required: true,
};
process.stdout.write(`${JSON.stringify(evidence)}\n`);
process.stdout.write('Pulse Personal packed clean-room proof passed; physical production attestation is still required.\n');
