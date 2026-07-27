#!/usr/bin/env node

import { createHash, createPrivateKey, createPublicKey, sign } from 'node:crypto';
import { chmodSync, lstatSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  canonicalReleaseJSON, pinnedReleaseKeyring, releaseKeyID, verifyPersonalReleaseArtifactSet,
} from '../src/release-manifest.js';

function fail(code) {
  const error = new Error(code);
  error.code = code;
  throw error;
}

function canonicalFile(path, schema) {
  let info;
  try { info = lstatSync(path); } catch { fail('release_snapshot_refresh_input_missing'); }
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.size < 2 || info.size > 2 * 1024 * 1024) {
    fail('release_snapshot_refresh_input_unsafe');
  }
  const bytes = readFileSync(path, 'utf8');
  let value;
  try { value = JSON.parse(bytes); } catch { fail('release_snapshot_refresh_input_invalid'); }
  if (bytes !== `${canonicalReleaseJSON(value)}\n` || value.schema !== schema) fail('release_snapshot_refresh_input_invalid');
  return Object.freeze({ bytes, value });
}

function rootAuthority(path) {
  let info;
  try { info = lstatSync(path); } catch { fail('release_snapshot_refresh_root_missing'); }
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.size < 1 ||
      info.size > 16 * 1024 || (info.mode & 0o077) !== 0) fail('release_snapshot_refresh_root_unsafe');
  let key;
  try { key = createPrivateKey(readFileSync(path)); } catch { fail('release_snapshot_refresh_root_invalid'); }
  if (key.asymmetricKeyType !== 'ed25519') fail('release_snapshot_refresh_root_invalid');
  const publicKey = createPublicKey(key).export({ type: 'spki', format: 'pem' });
  return Object.freeze({ key, keyID: releaseKeyID(publicKey) });
}

function signature(payload, authority) {
  return Object.freeze({
    algorithm: 'ed25519',
    key_id: authority.keyID,
    value: sign(null, Buffer.from(canonicalReleaseJSON(payload)), authority.key).toString('base64'),
  });
}

export function refreshReleaseSnapshot({
  artifactSetPath,
  currentSnapshotPath,
  now = new Date(),
  outputRoot,
  rootKeyPath,
  trustedKeys = pinnedReleaseKeyring(),
} = {}) {
  if (![artifactSetPath, currentSnapshotPath, outputRoot, rootKeyPath].every((path) =>
    typeof path === 'string' && isAbsolute(path) && resolve(path) === path) ||
      !(now instanceof Date) || Number.isNaN(now.valueOf())) fail('release_snapshot_refresh_arguments_invalid');
  const artifactSet = canonicalFile(artifactSetPath, 'pulse.personal_release_artifact_set.v1');
  const current = canonicalFile(currentSnapshotPath, 'pulse.release_snapshot_envelope.v1');
  const root = rootAuthority(rootKeyPath);
  if (trustedKeys.length !== 1 || trustedKeys[0].key_id !== root.keyID) fail('release_snapshot_refresh_root_mismatch');
  const epoch = artifactSet.value.payload?.release?.epoch;
  const version = artifactSet.value.payload?.release?.version;
  const verified = verifyPersonalReleaseArtifactSet(artifactSet.value, current.value, {
    allowExpiredSnapshot: true,
    architecture: 'arm64',
    minimumAcceptedEpoch: epoch,
    now,
    osVersion: '26.2',
    packageVersion: version,
    platform: 'darwin',
    trustedKeys,
  });
  if (verified.epoch !== 8 || verified.version !== '0.7.0' ||
      current.value.payload.artifact_set.sha256 !== createHash('sha256').update(artifactSet.bytes).digest('hex')) {
    fail('release_snapshot_refresh_release_invalid');
  }
  const issuedAt = new Date(now.valueOf() - 60_000);
  const expiresAt = new Date(issuedAt.valueOf() + 30 * 24 * 60 * 60 * 1000);
  const payload = Object.freeze({
    ...current.value.payload,
    expires_at: expiresAt.toISOString(),
    issued_at: issuedAt.toISOString(),
  });
  const snapshot = Object.freeze({
    payload,
    schema: 'pulse.release_snapshot_envelope.v1',
    signature: signature(payload, root),
  });
  const refreshed = verifyPersonalReleaseArtifactSet(artifactSet.value, snapshot, {
    architecture: 'arm64', minimumAcceptedEpoch: 8, now, osVersion: '26.2',
    packageVersion: '0.7.0', platform: 'darwin', trustedKeys,
  });
  mkdirSync(outputRoot, { mode: 0o700 });
  const snapshotBytes = `${canonicalReleaseJSON(snapshot)}\n`;
  const snapshotPath = join(outputRoot, 'snapshot.json');
  writeFileSync(snapshotPath, snapshotBytes, { encoding: 'utf8', flag: 'wx', mode: 0o600 });
  chmodSync(snapshotPath, 0o600);
  const receipt = Object.freeze({
    artifact_set_digest: refreshed.manifest_digest,
    expires_at: payload.expires_at,
    issued_at: payload.issued_at,
    release_epoch: refreshed.epoch,
    schema: 'pulse.release_snapshot_refresh.v1',
    snapshot_digest: createHash('sha256').update(snapshotBytes).digest('hex'),
    version: refreshed.version,
  });
  writeFileSync(join(outputRoot, 'snapshot-refresh-receipt.json'), `${canonicalReleaseJSON(receipt)}\n`, {
    encoding: 'utf8', flag: 'wx', mode: 0o600,
  });
  return Object.freeze({ receipt, snapshotPath });
}

function args(argv) {
  const values = {};
  for (let index = 0; index < argv.length; index += 2) {
    const name = argv[index];
    const value = argv[index + 1];
    if (!['--artifact-set', '--current-snapshot', '--output', '--root-key'].includes(name) ||
        !value || Object.hasOwn(values, name)) fail('release_snapshot_refresh_arguments_invalid');
    values[name] = value;
  }
  if (Object.keys(values).length !== 4) fail('release_snapshot_refresh_arguments_invalid');
  return Object.fromEntries(Object.entries(values).map(([name, value]) => [name, resolve(value)]));
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  const values = args(process.argv.slice(2));
  const result = refreshReleaseSnapshot({
    artifactSetPath: values['--artifact-set'],
    currentSnapshotPath: values['--current-snapshot'],
    outputRoot: values['--output'],
    rootKeyPath: values['--root-key'],
  });
  process.stdout.write(`${canonicalReleaseJSON(result.receipt)}\n`);
}
