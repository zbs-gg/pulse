#!/usr/bin/env node

import { createHash, createPrivateKey, createPublicKey, sign } from 'node:crypto';
import { lstatSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { dirname, isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { canonicalReleaseJSON, pinnedReleaseKeyring, releaseKeyID } from '../src/release-manifest.js';

const CHECKPOINTS = Object.freeze([0, 24, 48, 72]);
const SHA256 = /^[a-f0-9]{64}$/;

function fail(code) {
  const error = new Error(code);
  error.code = code;
  throw error;
}

function regularFile(path, maximumBytes, privateFile = false) {
  let info;
  try { info = lstatSync(path); } catch { fail('gold_promotion_input_missing'); }
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.size < 1 || info.size > maximumBytes ||
      (privateFile && (info.mode & 0o077) !== 0)) fail('gold_promotion_input_unsafe');
  return info;
}

function canonicalFile(path, schema) {
  regularFile(path, 1024 * 1024);
  const bytes = readFileSync(path, 'utf8');
  let value;
  try { value = JSON.parse(bytes); } catch { fail('gold_promotion_input_invalid'); }
  if (bytes !== `${canonicalReleaseJSON(value)}\n` || value?.schema !== schema) fail('gold_promotion_input_invalid');
  return Object.freeze({ bytes, value });
}

function jsonFiles(root) {
  const files = [];
  const visit = (directory, depth) => {
    if (depth > 5) fail('gold_promotion_input_unsafe');
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name);
      if (entry.isDirectory()) visit(path, depth + 1);
      else if (entry.isFile() && entry.name.endsWith('.json')) files.push(path);
    }
  };
  visit(root, 0);
  return files.sort();
}

function rootAuthority(path, trustedKeys) {
  regularFile(path, 16 * 1024, true);
  let key;
  try { key = createPrivateKey(readFileSync(path)); } catch { fail('gold_promotion_key_invalid'); }
  if (key.asymmetricKeyType !== 'ed25519') fail('gold_promotion_key_invalid');
  const publicKey = createPublicKey(key).export({ type: 'spki', format: 'pem' });
  const keyID = releaseKeyID(publicKey);
  if (trustedKeys.length !== 1 || trustedKeys[0].key_id !== keyID) fail('gold_promotion_key_mismatch');
  return Object.freeze({ key, keyID });
}

export function buildGoldPromotionReceipt({
  candidatePath,
  outputPath,
  rootKeyPath,
  soakRoot,
  trustedKeys = pinnedReleaseKeyring(),
} = {}) {
  if (![candidatePath, outputPath, rootKeyPath, soakRoot].every((path) =>
    typeof path === 'string' && isAbsolute(path) && resolve(path) === path)) fail('gold_promotion_arguments_invalid');
  const candidate = canonicalFile(candidatePath, 'pulse.npm_production_candidate.v1').value;
  if (candidate.production !== true || candidate.production_ready !== true || candidate.support_claim !== false || candidate.release_epoch !== 8 ||
      candidate.package !== '@zbs-gg/pulse' || candidate.version !== '0.7.0' ||
      !SHA256.test(candidate.sha256 ?? '') || !SHA256.test(candidate.artifact_set_digest ?? '')) {
    fail('gold_promotion_candidate_invalid');
  }
  const receipts = [];
  for (const path of jsonFiles(soakRoot)) {
    let parsed;
    try { parsed = JSON.parse(readFileSync(path, 'utf8')); } catch { continue; }
    if (parsed?.schema !== 'pulse.public_registry_soak.v1') continue;
    const receipt = canonicalFile(path, 'pulse.public_registry_soak.v1');
    receipts.push(Object.freeze({
      digest: createHash('sha256').update(receipt.bytes).digest('hex'),
      value: receipt.value,
    }));
  }
  receipts.sort((left, right) => left.value.checkpoint_hours - right.value.checkpoint_hours);
  if (receipts.length !== 4 || JSON.stringify(receipts.map((receipt) => receipt.value.checkpoint_hours)) !== JSON.stringify(CHECKPOINTS)) {
    fail('gold_promotion_soak_incomplete');
  }
  const publishedAt = receipts[0].value.published_at;
  for (const receipt of receipts) {
    const value = receipt.value;
    if (value.authority !== 'public_registry' || value.support_claim !== true || value.evidence_count !== 18 ||
        value.commit !== candidate.commit || value.package !== candidate.package || value.version !== candidate.version ||
        value.package_sha256 !== candidate.sha256 || value.artifact_set_digest !== candidate.artifact_set_digest ||
        value.release_epoch !== candidate.release_epoch || value.published_at !== publishedAt ||
        !SHA256.test(value.snapshot_digest ?? '')) fail('gold_promotion_soak_mismatch');
  }
  const first = new Date(receipts[0].value.observed_at);
  const last = new Date(receipts[3].value.observed_at);
  if (Number.isNaN(first.valueOf()) || Number.isNaN(last.valueOf()) || last.valueOf() - first.valueOf() < 71 * 3_600_000) {
    fail('gold_promotion_soak_duration_invalid');
  }
  const authority = rootAuthority(rootKeyPath, trustedKeys);
  const payload = Object.freeze({
    artifact_set_digest: candidate.artifact_set_digest,
    candidate_sha256: candidate.sha256,
    checkpoints: receipts.map((receipt) => Object.freeze({
      checkpoint_hours: receipt.value.checkpoint_hours,
      observed_at: receipt.value.observed_at,
      receipt_sha256: receipt.digest,
      snapshot_digest: receipt.value.snapshot_digest,
    })),
    commit: candidate.commit,
    package: candidate.package,
    promotion_authorized: true,
    publication_performed: false,
    release_epoch: candidate.release_epoch,
    soak_completed_at: receipts[3].value.observed_at,
    support_claim: true,
    version: candidate.version,
  });
  const receipt = Object.freeze({
    payload,
    schema: 'pulse.gold_promotion_receipt.v1',
    signature: Object.freeze({
      algorithm: 'ed25519',
      key_id: authority.keyID,
      value: sign(null, Buffer.from(canonicalReleaseJSON(payload)), authority.key).toString('base64'),
    }),
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
    if (!['--candidate', '--output', '--root-key', '--soaks'].includes(name) ||
        !value || Object.hasOwn(values, name)) fail('gold_promotion_arguments_invalid');
    values[name] = value;
  }
  if (Object.keys(values).length !== 4) fail('gold_promotion_arguments_invalid');
  return values;
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  const values = args(process.argv.slice(2));
  const receipt = buildGoldPromotionReceipt({
    candidatePath: resolve(values['--candidate']),
    outputPath: resolve(values['--output']),
    rootKeyPath: resolve(values['--root-key']),
    soakRoot: resolve(values['--soaks']),
  });
  process.stdout.write(`${canonicalReleaseJSON(receipt)}\n`);
}
