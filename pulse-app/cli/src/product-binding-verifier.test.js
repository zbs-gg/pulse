import assert from 'node:assert/strict';
import { mkdtempSync, realpathSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { verifyProductBinding } from './product-binding-verifier.js';

function binding(workspace, overrides = {}) {
  return {
    binding_digest: 'a'.repeat(64), resolver_epoch: 7,
    workspace: { repository_id: 'repository_pulse', canonical_path: realpathSync(workspace) },
    ...overrides,
  };
}

test('Home verifier re-reads the signed binding and accepts only the exact runtime boundary', async () => {
  const workspace = mkdtempSync(join(tmpdir(), 'pulse-home-binding.'));
  let recovered = 0;
  await verifyProductBinding({
    workspace, bindingDigest: 'a'.repeat(64), repositoryID: 'repository_pulse', resolverEpoch: 7,
    recover: async () => { recovered += 1; }, resolveBinding: () => binding(workspace),
  });
  assert.equal(recovered, 1);
});

for (const [name, overrides] of [
  ['digest', { binding_digest: 'b'.repeat(64) }],
  ['repository', { workspace: { repository_id: 'repository_other' } }],
  ['epoch', { resolver_epoch: 8 }],
]) {
  test(`Home verifier fails closed after ${name} authority drift`, async () => {
    const workspace = mkdtempSync(join(tmpdir(), 'pulse-home-binding-drift.'));
    const current = binding(workspace, overrides);
    if (overrides.workspace) current.workspace.canonical_path = realpathSync(workspace);
    await assert.rejects(() => verifyProductBinding({
      workspace, bindingDigest: 'a'.repeat(64), repositoryID: 'repository_pulse', resolverEpoch: 7,
      recover: async () => {}, resolveBinding: () => current,
    }), /mismatch/);
  });
}
