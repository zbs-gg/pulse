#!/usr/bin/env node

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { lstatSync, readdirSync } from 'node:fs';
import { dirname, isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const cliRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');

function command(commandName, args) {
  const value = spawnSync(commandName, args, {
    cwd: cliRoot, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 30_000,
  });
  if (value.status !== 0) throw new Error(`${commandName} ${args.join(' ')} failed`);
  return value.stdout.trim();
}

function nativeCodexExecutable() {
  const npmExecPath = process.env.npm_execpath;
  if (!npmExecPath || !isAbsolute(npmExecPath)) throw new Error('run packed consolidation proof through npm');
  const globalRoot = command(process.execPath, [npmExecPath, 'root', '--global']);
  const packageRoot = join(globalRoot, '@openai', 'codex', 'node_modules', '@openai');
  const executableName = process.platform === 'win32' ? 'codex.exe' : 'codex';
  const matches = [];
  const visit = (path, depth = 0) => {
    if (depth > 8) return;
    for (const name of readdirSync(path)) {
      const child = join(path, name);
      const info = lstatSync(child);
      if (info.isSymbolicLink()) continue;
      if (info.isDirectory()) visit(child, depth + 1);
      else if (info.isFile() && name === executableName) matches.push(child);
    }
  };
  visit(packageRoot);
  assert.equal(matches.length, 1, `expected one native Codex executable under ${packageRoot}`);
  return resolve(matches[0]);
}

const result = spawnSync(process.execPath, ['scripts/personal-native-packed-e2e.mjs'], {
  cwd: cliRoot,
  env: {
    ...process.env,
    PULSE_NATIVE_PACKED_CODEX_EXECUTABLE: process.env.PULSE_NATIVE_PACKED_CODEX_EXECUTABLE
      ?? nativeCodexExecutable(),
  },
  encoding: 'utf8',
  timeout: process.platform === 'win32' ? 8 * 60_000 : 20 * 60_000,
  stdio: ['ignore', 'pipe', 'pipe'],
  maxBuffer: 64 * 1024 * 1024,
});
if (result.status !== 0) {
  const tail = (value) => value.slice(-12_000);
  process.stderr.write(`personal native packed fixture exited ${result.status}\n`);
  if (result.stdout) process.stderr.write(`--- stdout tail ---\n${tail(result.stdout)}\n`);
  if (result.stderr) process.stderr.write(`--- stderr tail ---\n${tail(result.stderr)}\n`);
  process.exit(result.status ?? 1);
}

const receipts = result.stdout.split(/\r?\n/).map((line) => {
  try { return JSON.parse(line); } catch { return null; }
}).filter(Boolean);
const report = receipts.find((value) => value.schema === 'pulse.personal_consolidation_report_fixture.v1');
const product = receipts.find((value) => value.schema === 'pulse.native_packed_product_fixture.v1');
assert.ok(report, 'packed fixture did not emit a consolidation report receipt');
assert.ok(product, 'packed fixture did not emit the product receipt');
assert.equal(report.content_free, true);
assert.equal(report.target_id, product.target_id);
assert.equal(report.package_sha256, product.packed_tarball_sha256);
assert.equal(report.phase, 'report_ready');
assert.deepEqual(report.source_classifications, ['backup', 'canonical_vault', 'release_artifact']);
assert.equal(report.totals.excluded, 2);
for (const proof of [
  'cli_parity', 'mcp_parity', 'memory_home_visible', 'sources_byte_preserved',
]) assert.equal(report[proof], true, proof);
for (const mutation of ['imported', 'merged', 'deleted', 'published']) {
  assert.equal(report[mutation], false, mutation);
}
assert.match(report.report_digest, /^[a-f0-9]{64}$/);
assert.match(report.inventory_digest, /^[a-f0-9]{64}$/);

process.stdout.write(`${JSON.stringify(report)}\n`);
process.stdout.write('Pulse packed consolidation report preserved every synthetic source and matched CLI, MCP, and Memory Home.\n');
