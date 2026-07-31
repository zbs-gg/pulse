import {
  createPrivateKey, createPublicKey, sign,
} from 'node:crypto';
import {
  chmodSync, lstatSync, readFileSync, renameSync, rmSync, writeFileSync,
} from 'node:fs';
import { isAbsolute, resolve } from 'node:path';

import {
  canonicalReleaseJSON,
  pinnedReleaseKeyring,
  releaseKeyID,
  verifyReleaseManifestEnvelope,
} from '../src/release-manifest.js';

function stop(message) {
  throw new Error(`release_signing_${message}`);
}

const [sourceArg, destinationArg] = process.argv.slice(2);
if (!sourceArg || !destinationArg) stop('usage');
const keyPath = process.env.PULSE_RELEASE_SIGNING_KEY_PATH;
if (!keyPath || !isAbsolute(keyPath)) stop('key_path_required');

const keyStat = lstatSync(keyPath);
if (!keyStat.isFile() || keyStat.isSymbolicLink() || keyStat.size > 16_384 || (keyStat.mode & 0o077) !== 0) {
  stop('key_file_invalid');
}
const privateKey = createPrivateKey(readFileSync(keyPath));
if (privateKey.asymmetricKeyType !== 'ed25519') stop('key_type_invalid');
const publicKey = createPublicKey(privateKey).export({ type: 'spki', format: 'pem' });
const rootPath = process.env.PULSE_RELEASE_TEST_ROOT_PATH;
if (rootPath && process.env.PULSE_RELEASE_TEST_MODE !== '1') stop('root_override_forbidden');
const trustedKeys = pinnedReleaseKeyring(rootPath);
const keyID = releaseKeyID(publicKey);
if (trustedKeys.length !== 1 || trustedKeys[0].key_id !== keyID) stop('key_not_pinned');

const sourcePath = resolve(sourceArg);
const destinationPath = resolve(destinationArg);
const sourceBytes = readFileSync(sourcePath, 'utf8');
const payload = JSON.parse(sourceBytes);
if (sourceBytes !== `${canonicalReleaseJSON(payload)}\n`) stop('payload_not_canonical');
if (payload?.release?.key_id !== keyID) stop('payload_key_mismatch');
const signature = sign(null, Buffer.from(canonicalReleaseJSON(payload)), privateKey).toString('base64');
const envelope = {
  payload,
  schema: 'pulse.release_envelope.v1',
  signature: { algorithm: 'ed25519', key_id: keyID, value: signature },
};

verifyReleaseManifestEnvelope(envelope, {
  architecture: 'arm64',
  minimumAcceptedEpoch: payload.release.epoch,
  now: new Date(),
  osVersion: process.env.PULSE_RELEASE_TARGET_MACOS ?? '13.0',
  packageVersion: payload.release.version,
  platform: 'darwin',
  trustedKeys,
});

const temporary = `${destinationPath}.new-${process.pid}`;
try {
  writeFileSync(temporary, `${canonicalReleaseJSON(envelope)}\n`, { encoding: 'utf8', flag: 'wx', mode: 0o644 });
  chmodSync(temporary, 0o644);
  renameSync(temporary, destinationPath);
} catch (error) {
  rmSync(temporary, { force: true });
  throw error;
}
console.error(`[pulse] signed release manifest: ${destinationPath}`);
