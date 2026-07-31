import assert from 'node:assert/strict';
import { createHash, generateKeyPairSync, sign } from 'node:crypto';
import {
  chmodSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  realpathSync,
  renameSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

import {
  BindingError,
  bindingRegistryAnchor,
  canonicalJSONStringify,
  canonicalizeWorkspace,
  inspectWorkspaceBinding,
  resolveWorkspaceBinding,
  verifyBindingRegistry,
} from './workspace-binding.js';
import { createPlatformServices } from './platform-services.js';

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
  const anchorPath = join(supervisor, 'workspace-bindings.anchor.json');
  const signature = sign(
    algorithm === 'es256' ? 'sha256' : null,
    Buffer.from(canonicalJSONStringify(payload)),
    privateKey,
  ).toString('base64');
  const registryBytes = Buffer.from(`${JSON.stringify({ algorithm, payload, signature })}\n`);
  writeFileSync(registryPath, registryBytes, { mode: 0o600 });
  writeFileSync(anchorPath, `${canonicalJSONStringify(bindingRegistryAnchor(registryBytes, payload.epoch))}\n`, { mode: 0o600 });
  writeFileSync(publicKeyPath, publicKey.export({ type: 'spki', format: 'pem' }), { mode: 0o600 });
  chmodSync(registryPath, 0o600);
  chmodSync(publicKeyPath, 0o600);
  chmodSync(anchorPath, 0o600);
  return { registryPath, publicKeyPath, anchorPath, rootAnchor: false };
}

function personalBinding(workspace, overrides = {}) {
  return {
    binding_id: 'binding_demo',
    receipt_id: 'receipt_demo',
    resolver_epoch: 7,
    workspace: {
      workspace_id: workspace.workspace_id,
      repository_id: workspace.repository_id,
    },
    mode: 'personal',
    principal_ref: 'principal_dima',
    personal: {
      store_id: 'store_personal_dima',
      data_dir: '/private/pulse/vaults/personal/demo',
      base_url: 'http://127.0.0.1:18801',
      credential_ref: 'keychain:pulse/local/dima',
      cache_dir: '/private/pulse/caches/personal/demo',
    },
    ...overrides,
  };
}

test('canonical workspace collapses nested paths and symlinks, survives rename, and separates clones', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-binding-workspace.'));
  const repository = makeRepository(root);
  const nested = canonicalizeWorkspace(join(repository, 'nested'));
  const legacyID = (label, path, prefix) => {
    const info = statSync(path, { bigint: true });
    const value = createHash('sha256').update(label).update('\0').update(`${info.dev}:${info.ino}`).digest('hex');
    return `${prefix}_${value.slice(0, 32)}`;
  };
  assert.equal(nested.workspace_id, legacyID('pulse-workspace-v1', repository, 'workspace'));
  assert.equal(nested.repository_id, legacyID('pulse-repository-v1', join(repository, '.git'), 'repository'));
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

test('workspace Git discovery is delegated to platform services', () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-platform-git.'));
  try {
    const repository = makeRepository(root);
    const base = createPlatformServices();
    const calls = [];
    const identities = [];
    const platformServices = {
      ...base,
      runGit(cwd, args) {
        calls.push({ cwd, args });
        return base.runGit(cwd, args);
      },
      inspectPathIdentity(path, options) {
        identities.push({ path, options });
        return base.inspectPathIdentity(path, options);
      },
    };
    const workspace = canonicalizeWorkspace(repository, { platformServices });
    assert.equal(workspace.canonical_path, realpathSync(repository));
    assert.deepEqual(calls.map(({ args }) => args), [
      ['rev-parse', '--show-toplevel'],
      ['rev-parse', '--git-dir'],
      ['rev-parse', '--git-common-dir'],
    ]);
    assert.deepEqual(identities.map(({ options }) => options), [
      { kind: 'directory' }, { kind: 'directory' },
    ]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('binding trust-file reads are delegated with exact owner policy', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-binding-integrity.'));
  try {
    const repository = makeRepository(home, 'work');
    const base = createPlatformServices();
    const workspace = canonicalizeWorkspace(repository, { platformServices: base });
    const paths = signedRegistry(home, {
      schema: 'pulse.workspace-binding-registry.v1', epoch: 7, bindings: [personalBinding(workspace)],
    });
    const reads = [];
    const platformServices = {
      ...base,
      readIntegrityFile(path, options) {
        reads.push({ path, options });
        return base.readIntegrityFile(path, options);
      },
    };
    assert.equal(resolveWorkspaceBinding({ cwd: repository, ...paths, platformServices }).mode, 'personal');
    assert.deepEqual(reads.map(({ options }) => options.owner), ['current', 'current', 'current', 'current']);
  } finally {
    rmSync(home, { recursive: true, force: true });
  }
});

test('unassigned inspection allows only no Git project or no registered binding', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-binding-unassigned.'));
  const repository = makeRepository(home, 'work');
  const paths = signedRegistry(home, {
    schema: 'pulse.workspace-binding-registry.v1', epoch: 1, bindings: [],
  });
  const missing = inspectWorkspaceBinding({ cwd: repository, ...paths });
  assert.equal(missing.status, 'unassigned');
  assert.equal(missing.reason, 'binding_missing');
  assert.equal(missing.workspace.canonical_path, realpathSync(repository));

  const ordinaryDirectory = join(home, 'ordinary');
  mkdirSync(ordinaryDirectory);
  const notGit = inspectWorkspaceBinding({ cwd: ordinaryDirectory, ...paths });
  assert.equal(notGit.status, 'unassigned');
  assert.equal(notGit.reason, 'workspace_not_git');
  assert.equal(notGit.workspace.canonical_path, realpathSync(ordinaryDirectory));

  const registry = JSON.parse(readFileSync(paths.registryPath, 'utf8'));
  registry.signature = Buffer.alloc(64).toString('base64');
  writeFileSync(paths.registryPath, `${JSON.stringify(registry)}\n`, { mode: 0o600 });
  assert.throws(
    () => inspectWorkspaceBinding({ cwd: repository, ...paths }),
    (error) => error instanceof BindingError && error.code === 'binding_signature_invalid',
  );
});

test('tampering, ambiguity, and repo-local registries fail before any Vault can be queried', () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-binding-tamper.'));
  const repository = makeRepository(home, 'work');
  const workspace = canonicalizeWorkspace(repository);
  const payload = {
    schema: 'pulse.workspace-binding-registry.v1',
    epoch: 7,
    bindings: [personalBinding(workspace), personalBinding(workspace, { binding_id: 'binding_other' })],
  };
  const paths = signedRegistry(home, payload);
  assert.throws(
    () => resolveWorkspaceBinding({ cwd: repository, ...paths }),
    (error) => error instanceof BindingError && error.code === 'binding_ambiguous',
  );

  const valid = signedRegistry(home, { ...payload, bindings: [personalBinding(workspace)] });
  writeFileSync(valid.registryPath, JSON.stringify({ algorithm: 'ed25519', payload: { ...payload, epoch: 8 }, signature: 'AAAA' }), {
    mode: 0o600,
  });
  assert.throws(
    () => resolveWorkspaceBinding({ cwd: repository, ...valid }),
    (error) => error instanceof BindingError && error.code === 'binding_signature_invalid',
  );

  const repoRegistry = join(repository, '.pulse-bindings.json');
  const repoKey = join(repository, '.pulse-bindings.pub.pem');
  const repoAnchor = join(repository, '.pulse-bindings.anchor.json');
  const outside = signedRegistry(home, { ...payload, bindings: [personalBinding(workspace)] });
  writeFileSync(repoRegistry, awaitRead(outside.registryPath), { mode: 0o600 });
  writeFileSync(repoKey, awaitRead(outside.publicKeyPath), { mode: 0o600 });
  writeFileSync(repoAnchor, awaitRead(outside.anchorPath), { mode: 0o600 });
  assert.throws(
    () => resolveWorkspaceBinding({
      cwd: repository, registryPath: repoRegistry, publicKeyPath: repoKey,
      anchorPath: repoAnchor, rootAnchor: false,
    }),
    (error) => error instanceof BindingError && error.code === 'binding_registry_in_workspace',
  );
});

function awaitRead(path) {
  return spawnSync('/bin/cat', [path]).stdout;
}

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
