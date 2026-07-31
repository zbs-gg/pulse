import assert from 'node:assert/strict';
import { generateKeyPairSync } from 'node:crypto';
import { chmodSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { verifyReleasePrivateRoot } from '../scripts/verify-release-private-key.mjs';

function keyFiles(t) {
  const root = mkdtempSync(join(tmpdir(), 'pulse-root-check-'));
  t.after(() => rmSync(root, { force: true, recursive: true }));
  const pair = generateKeyPairSync('ed25519');
  const privatePath = join(root, 'root-private.pem');
  const publicPath = join(root, 'root-public.pem');
  writeFileSync(privatePath, pair.privateKey.export({ type: 'pkcs8', format: 'pem' }), { mode: 0o600 });
  writeFileSync(publicPath, pair.publicKey.export({ type: 'spki', format: 'pem' }), { mode: 0o600 });
  return { privatePath, publicPath };
}

test('release root check proves a matching Ed25519 pair without returning key bytes', (t) => {
  const fixture = keyFiles(t);
  const result = verifyReleasePrivateRoot(fixture.privatePath, fixture.publicPath);
  assert.deepEqual(Object.keys(result), ['key_id', 'matches', 'schema']);
  assert.equal(result.matches, true);
  assert.match(result.key_id, /^[a-f0-9]{64}$/);
});

test('release root check rejects mismatched and group-readable private roots', (t) => {
  const fixture = keyFiles(t);
  const other = keyFiles(t);
  assert.throws(
    () => verifyReleasePrivateRoot(fixture.privatePath, other.publicPath),
    /release_private_root_mismatch/,
  );
  chmodSync(fixture.privatePath, 0o640);
  assert.throws(
    () => verifyReleasePrivateRoot(fixture.privatePath, fixture.publicPath),
    /release_private_root_unsafe/,
  );
});
