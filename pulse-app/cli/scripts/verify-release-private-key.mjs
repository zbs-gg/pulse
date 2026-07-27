#!/usr/bin/env node

import { createPrivateKey, createPublicKey } from 'node:crypto';
import { lstatSync, readFileSync } from 'node:fs';
import { isAbsolute, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { pinnedReleaseKeyring, releaseKeyID } from '../src/release-manifest.js';

function fail(code) {
  const error = new Error(code);
  error.code = code;
  throw error;
}

export function verifyReleasePrivateRoot(privatePath, publicPath) {
  if (![privatePath, publicPath].every((path) => typeof path === 'string' && isAbsolute(path) && resolve(path) === path)) {
    fail('release_private_root_arguments_invalid');
  }
  let info;
  try { info = lstatSync(privatePath); } catch { fail('release_private_root_missing'); }
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.size < 1 ||
      info.size > 16 * 1024 || (info.mode & 0o077) !== 0) {
    fail('release_private_root_unsafe');
  }
  let privateKey;
  try { privateKey = createPrivateKey(readFileSync(privatePath)); } catch { fail('release_private_root_invalid'); }
  if (privateKey.asymmetricKeyType !== 'ed25519') fail('release_private_root_invalid');
  const derived = createPublicKey(privateKey).export({ type: 'spki', format: 'pem' });
  const pinned = pinnedReleaseKeyring(publicPath);
  const keyID = releaseKeyID(derived);
  if (pinned.length !== 1 || pinned[0].key_id !== keyID) fail('release_private_root_mismatch');
  return Object.freeze({ key_id: keyID, matches: true, schema: 'pulse.release_private_root_check.v1' });
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  if (process.argv.length !== 4) fail('release_private_root_arguments_invalid');
  const receipt = verifyReleasePrivateRoot(resolve(process.argv[2]), resolve(process.argv[3]));
  process.stdout.write(`${JSON.stringify(receipt)}\n`);
}
