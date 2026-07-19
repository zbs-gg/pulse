#!/usr/bin/env node

import { realpathSync } from 'node:fs';
import { isAbsolute, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

import { recoverWorkspaceBindingTransaction } from './binding-admin.js';
import { resolveWorkspaceBinding } from './workspace-binding.js';

const HEX_DIGEST = /^[a-f0-9]{64}$/;
const SAFE_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$/;

function testAuthorityPaths(environment = process.env) {
  if (environment.PULSE_PRODUCT_AUTHORITY_TEST_MODE !== '1' || environment.PULSE_TRUST_MODE !== 'test') {
    return null;
  }
  const paths = {
    registryPath: environment.PULSE_BINDING_REGISTRY_PATH,
    publicKeyPath: environment.PULSE_BINDING_PUBLIC_KEY_PATH,
    anchorPath: environment.PULSE_BINDING_ANCHOR_PATH,
  };
  if (Object.values(paths).some((value) => typeof value !== 'string' || !isAbsolute(value))) {
    throw new Error('product_binding_verifier_test_authority_invalid');
  }
  return Object.fromEntries(Object.entries(paths).map(([name, value]) => [name, resolve(value)]));
}

export async function verifyProductBinding({
  workspace, bindingDigest, repositoryID, resolverEpoch,
  recover = recoverWorkspaceBindingTransaction,
  resolveBinding = resolveWorkspaceBinding,
} = {}) {
  if (typeof workspace !== 'string' || !isAbsolute(workspace) ||
      !HEX_DIGEST.test(bindingDigest ?? '') || !SAFE_ID.test(repositoryID ?? '') ||
      !Number.isSafeInteger(resolverEpoch) || resolverEpoch < 1) {
    throw new Error('product_binding_verifier_input_invalid');
  }
  await recover();
  const binding = resolveBinding({ cwd: workspace });
  if (binding.binding_digest !== bindingDigest || binding.workspace.repository_id !== repositoryID ||
      binding.resolver_epoch !== resolverEpoch ||
      realpathSync(binding.workspace.canonical_path) !== realpathSync(resolve(workspace))) {
    throw new Error('product_binding_verifier_mismatch');
  }
}

async function main() {
  const [workspace, bindingDigest, repositoryID, resolverEpochText] = process.argv.slice(2);
  const resolverEpoch = Number(resolverEpochText);
  const authority = testAuthorityPaths();
  await verifyProductBinding({
    workspace, bindingDigest, repositoryID, resolverEpoch,
    ...(authority ? {
      recover: () => recoverWorkspaceBindingTransaction({
        home: process.env.HOME, ...authority, rootPublicKey: false, rootAnchor: false,
      }),
      resolveBinding: ({ cwd }) => resolveWorkspaceBinding({
        cwd, ...authority, rootAnchor: false,
      }),
    } : {}),
  });
}

const invokedAsMain = process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url;
if (invokedAsMain) {
  main().catch(() => {
    process.stderr.write('Pulse product binding is no longer current.\n');
    process.exitCode = 1;
  });
}
