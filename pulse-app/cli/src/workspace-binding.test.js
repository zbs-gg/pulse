import assert from 'node:assert/strict';
import { generateKeyPairSync, sign } from 'node:crypto';
import {
  chmodSync,
  mkdirSync,
  mkdtempSync,
  renameSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

import {
  BindingError,
  canonicalJSONStringify,
  canonicalizeWorkspace,
  resolveWorkspaceBinding,
  verifyBindingRegistry,
} from './workspace-binding.js';

function git(cwd, ...args) {
  const result = spawnSync('/usr/bin/git', args, { cwd, encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
}

function makeRepository(root, name = 'project') {
  const path = join(root, name);
  mkdirSync(path, { recursive: true });
  git(path, 'init', '-q');
	git(path, 'config', 'user.email', 'pulse-tests@example.test');
	git(path, 'config', 'user.name', 'Pulse Tests');
	git(path, 'commit', '--allow-empty', '-q', '-m', 'fixture');
  mkdirSync(join(path, 'nested'));
  return path;
}

function signedRegistry(home, payload, { algorithm = 'ed25519' } = {}) {
  const { privateKey, publicKey } = algorithm === 'es256'
    ? generateKeyPairSync('ec', { namedCurve: 'P-256' })
    : generateKeyPairSync('ed25519');
  const supervisor = join(home, '.pulse', 'supervisor');
  mkdirSync(supervisor, { recursive: true, mode: 0o700 });
  chmodSync(supervisor, 0o700);
  const registryPath = join(supervisor, 'workspace-bindings.json');
  const publicKeyPath = join(supervisor, 'workspace-bindings.pub.pem');
  const signature = sign(
    algorithm === 'es256' ? 'sha256' : null,
    Buffer.from(canonicalJSONStringify(payload)),
    privateKey,
  ).toString('base64');
  writeFileSync(registryPath, JSON.stringify({ algorithm, payload, signature }), { mode: 0o600 });
  writeFileSync(publicKeyPath, publicKey.export({ type: 'spki', format: 'pem' }), { mode: 0o600 });
  chmodSync(registryPath, 0o600);
  chmodSync(publicKeyPath, 0o600);
  return { registryPath, publicKeyPath };
}

function teamBinding(workspace, overrides = {}) {
  return {
    binding_id: 'binding_demo',
    receipt_id: 'receipt_demo',
    resolver_epoch: 7,
    workspace: {
      workspace_id: workspace.workspace_id,
      repository_id: workspace.repository_id,
    },
    mode: 'team',
    principal_ref: 'principal_dima',
    desk: {
      store_id: 'store_desk_dima',
      data_dir: '/Users/dima/.pulse/vaults/desks/team_demo',
      base_url: 'http://127.0.0.1:18801',
      credential_ref: 'keychain:pulse/desk/dima',
      cache_dir: '/Users/dima/.pulse/caches/desk/team_demo',
    },
    commons: {
      store_id: 'store_commons_demo',
      team_id: 'team_demo',
      resource: 'https://pulse.example.test/team_demo',
      credential_ref: 'keychain:pulse/team/demo/dima',
      cache_partition: 'commons:team_demo:principal_dima',
    },
    ...overrides,
  };
}

test('canonical workspace collapses nested paths and symlinks, survives rename, and separates clones', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-binding-workspace.'));
  const repository = makeRepository(root);
  const nested = canonicalizeWorkspace(join(repository, 'nested'));
  const symlink = join(root, 'project-link');
  symlinkSync(repository, symlink);
  assert.equal(canonicalizeWorkspace(symlink).workspace_id, nested.workspace_id);

  const renamed = join(root, 'renamed-project');
  renameSync(repository, renamed);
  assert.equal(canonicalizeWorkspace(renamed).workspace_id, nested.workspace_id);

  const clone = makeRepository(root, 'clone');
  assert.notEqual(canonicalizeWorkspace(clone).workspace_id, nested.workspace_id);

  const worktree = join(root, 'worktree');
  git(renamed, 'worktree', 'add', '--detach', '-q', worktree);
  const worktreeIdentity = canonicalizeWorkspace(worktree);
  assert.notEqual(worktreeIdentity.workspace_id, nested.workspace_id);
  assert.equal(worktreeIdentity.repository_id, nested.repository_id);
  assert.equal(worktreeIdentity.checkout_kind, 'worktree');
});

test('signed registry resolves exactly one immutable Team topology', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-binding-home.'));
  const repository = makeRepository(home, 'work');
  const workspace = canonicalizeWorkspace(repository);
  const payload = {
    schema: 'pulse.workspace-binding-registry.v1',
    epoch: 7,
    bindings: [teamBinding(workspace)],
  };
  const paths = signedRegistry(home, payload, { algorithm: 'es256' });

  const resolved = resolveWorkspaceBinding({ cwd: repository, ...paths });
  assert.equal(resolved.mode, 'team');
  assert.equal(resolved.principal_ref, 'principal_dima');
  assert.equal(resolved.desk.store_id, 'store_desk_dima');
  assert.equal(resolved.commons.store_id, 'store_commons_demo');
  assert.equal(resolved.fallback, false);
  assert.match(resolved.binding_digest, /^[a-f0-9]{64}$/);
});

test('tampering, ambiguity, and repo-local registries fail before any Vault can be queried', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-binding-tamper.'));
  const repository = makeRepository(home, 'work');
  const workspace = canonicalizeWorkspace(repository);
  const payload = {
    schema: 'pulse.workspace-binding-registry.v1',
    epoch: 7,
    bindings: [teamBinding(workspace), teamBinding(workspace, { binding_id: 'binding_other' })],
  };
  const paths = signedRegistry(home, payload);
  assert.throws(
    () => resolveWorkspaceBinding({ cwd: repository, ...paths }),
    (error) => error instanceof BindingError && error.code === 'binding_ambiguous',
  );

  const valid = signedRegistry(home, { ...payload, bindings: [teamBinding(workspace)] });
  writeFileSync(valid.registryPath, JSON.stringify({ algorithm: 'ed25519', payload: { ...payload, epoch: 8 }, signature: 'AAAA' }), {
    mode: 0o600,
  });
  assert.throws(
    () => resolveWorkspaceBinding({ cwd: repository, ...valid }),
    (error) => error instanceof BindingError && error.code === 'binding_signature_invalid',
  );

  const repoRegistry = join(repository, '.pulse-bindings.json');
  const repoKey = join(repository, '.pulse-bindings.pub.pem');
  const outside = signedRegistry(home, { ...payload, bindings: [teamBinding(workspace)] });
  writeFileSync(repoRegistry, awaitRead(outside.registryPath), { mode: 0o600 });
  writeFileSync(repoKey, awaitRead(outside.publicKeyPath), { mode: 0o600 });
  assert.throws(
    () => resolveWorkspaceBinding({ cwd: repository, registryPath: repoRegistry, publicKeyPath: repoKey }),
    (error) => error instanceof BindingError && error.code === 'binding_registry_in_workspace',
  );
});

function awaitRead(path) {
  return spawnSync('/bin/cat', [path]).stdout;
}

test('binding topology rejects Personal substitution and non-loopback Desk endpoints', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-binding-topology.'));
  const repository = makeRepository(home, 'work');
  const workspace = canonicalizeWorkspace(repository);
  const invalid = teamBinding(workspace, {
    personal: { store_id: 'store_personal' },
    desk: {
      ...teamBinding(workspace).desk,
      base_url: 'https://desk.example.test',
    },
  });
  const paths = signedRegistry(home, {
    schema: 'pulse.workspace-binding-registry.v1',
    epoch: 7,
    bindings: [invalid],
  });
  assert.throws(
    () => resolveWorkspaceBinding({ cwd: repository, ...paths }),
    (error) => error instanceof BindingError && error.code === 'binding_topology_invalid',
  );
});

test('production trust mode rejects a same-UID replacement verification key', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-binding-root-trust.'));
  const paths = signedRegistry(home, {
    schema: 'pulse.workspace-binding-registry.v1', epoch: 1, bindings: [],
  });
  assert.throws(
    () => verifyBindingRegistry({ ...paths, rootPublicKey: true }),
    (error) => error instanceof BindingError && error.code === 'binding_key_unsafe',
  );
});
