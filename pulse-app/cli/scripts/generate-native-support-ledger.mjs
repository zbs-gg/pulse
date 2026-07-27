#!/usr/bin/env node

import { createPublicKey, verify } from 'node:crypto';
import { lstatSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { dirname, isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { canonicalReleaseJSON, pinnedReleaseKeyring } from '../src/release-manifest.js';
import { validateNativeEvidenceSet } from './validate-native-evidence-set.mjs';

function fail(code) {
  const error = new Error(code);
  error.code = code;
  throw error;
}

function canonicalFile(path, schema) {
  let info;
  try { info = lstatSync(path); } catch { fail('support_ledger_input_missing'); }
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.size < 2 || info.size > 2 * 1024 * 1024) {
    fail('support_ledger_input_unsafe');
  }
  const bytes = readFileSync(path, 'utf8');
  let value;
  try { value = JSON.parse(bytes); } catch { fail('support_ledger_input_invalid'); }
  if (bytes !== `${canonicalReleaseJSON(value)}\n` || value?.schema !== schema) fail('support_ledger_input_invalid');
  return value;
}

function evidenceFiles(root) {
  const files = [];
  const visit = (directory) => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name);
      if (entry.isDirectory()) visit(path);
      else if (entry.isFile() && entry.name.endsWith('.json')) files.push(path);
    }
  };
  visit(root);
  return files.sort();
}

export function generateNativeSupportLedger({
  evidenceRoot,
  outputPath,
  promotionPath,
  trustedKeys = pinnedReleaseKeyring(),
} = {}) {
  if (![evidenceRoot, outputPath, promotionPath].every((path) =>
    typeof path === 'string' && isAbsolute(path) && resolve(path) === path)) fail('support_ledger_arguments_invalid');
  let set;
  try { set = validateNativeEvidenceSet(evidenceRoot, { authority: 'public_registry' }); }
  catch { fail('support_ledger_evidence_invalid'); }
  const promotion = canonicalFile(promotionPath, 'pulse.gold_promotion_receipt.v1');
  const key = trustedKeys.find((value) => value.key_id === promotion.signature?.key_id);
  if (!key || promotion.signature?.algorithm !== 'ed25519' ||
      !verify(null, Buffer.from(canonicalReleaseJSON(promotion.payload)), createPublicKey(key.public_key_pem),
        Buffer.from(promotion.signature?.value ?? '', 'base64'))) fail('support_ledger_signature_invalid');
  if (promotion.payload?.promotion_authorized !== true || promotion.payload?.publication_performed !== false ||
      promotion.payload?.support_claim !== true || promotion.payload?.candidate_sha256 !== set.package_sha256 ||
      promotion.payload?.artifact_set_digest !== set.artifact_set_digest || promotion.payload?.commit !== set.source_commit ||
      promotion.payload?.checkpoints?.length !== 4) fail('support_ledger_promotion_invalid');
  const records = evidenceFiles(evidenceRoot)
    .map((path) => JSON.parse(readFileSync(path, 'utf8')))
    .filter((value) => value?.schema === 'pulse.native_host_target_evidence.v2')
    .sort((left, right) => `${left.host}\0${left.target_id}`.localeCompare(`${right.host}\0${right.target_id}`));
  if (records.length !== 18) fail('support_ledger_evidence_invalid');
  const lines = [
    '# Pulse 0.7.0 Universal Gold support ledger',
    '',
    '> Generated only from signed promotion authorization and 18 public-registry receipts. Do not edit rows by hand.',
    '',
    `- Source commit: \`${set.source_commit}\``,
    `- npm tarball SHA-256: \`${set.package_sha256}\``,
    `- Artifact set SHA-256: \`${set.artifact_set_digest}\``,
    `- Soak completed: \`${promotion.payload.soak_completed_at}\``,
    '',
    '| Host | Version | Target | First value | Vendor executable SHA-256 | Public support |',
    '|---|---:|---|---:|---|---:|',
    ...records.map((record) =>
      `| ${record.host} | \`${record.host_version}\` | \`${record.target_id}\` | ${record.first_value.milliseconds} ms | \`${record.harness.executable_sha256}\` | yes |`),
    '',
  ];
  mkdirSync(dirname(outputPath), { recursive: true, mode: 0o700 });
  writeFileSync(outputPath, `${lines.join('\n')}\n`, { encoding: 'utf8', flag: 'wx', mode: 0o600 });
  return Object.freeze({ count: records.length, source_commit: set.source_commit });
}

function args(argv) {
  const values = {};
  for (let index = 0; index < argv.length; index += 2) {
    const name = argv[index];
    const value = argv[index + 1];
    if (!['--evidence', '--output', '--promotion'].includes(name) || !value || Object.hasOwn(values, name)) {
      fail('support_ledger_arguments_invalid');
    }
    values[name] = value;
  }
  if (Object.keys(values).length !== 3) fail('support_ledger_arguments_invalid');
  return values;
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  const values = args(process.argv.slice(2));
  const result = generateNativeSupportLedger({
    evidenceRoot: resolve(values['--evidence']),
    outputPath: resolve(values['--output']),
    promotionPath: resolve(values['--promotion']),
  });
  process.stdout.write(`${canonicalReleaseJSON({ ...result, schema: 'pulse.support_ledger_generation.v1' })}\n`);
}
