import assert from 'node:assert/strict';
import { generateKeyPairSync, sign } from 'node:crypto';
import {
  chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

import {
  createInitialPersonalWorkspaceBinding,
  createWorkspaceBinding,
  recoverWorkspaceBindingTransaction,
} from './binding-admin.js';
import {
  bindingRegistryAnchor,
  BindingError,
  canonicalJSONStringify,
  canonicalizeWorkspace,
  resolveWorkspaceBinding,
  verifyBindingRegistry,
} from './workspace-binding.js';
import { createPlatformServices } from './platform-services.js';

function git(cwd, ...args) {
  const result = spawnSync('/usr/bin/git', args, { cwd, encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
}

function makeRepository(root, name) {
  const path = join(root, name);
  mkdirSync(path, { recursive: true });
  git(path, 'init', '-q');
  git(path, 'config', 'user.email', 'pulse-tests@example.test');
  git(path, 'config', 'user.name', 'Pulse Tests');
  git(path, 'commit', '--allow-empty', '-q', '-m', 'fixture');
  return path;
}

function fixture() {
  const home = mkdtempSync(join(tmpdir(), 'pulse-binding-admin.'));
  const trust = join(home, 'trust');
  mkdirSync(trust, { recursive: true, mode: 0o700 });
  const publicKeyPath = join(trust, 'workspace-bindings.pub.pem');
  const registryPath = join(home, '.pulse', 'supervisor', 'workspace-bindings.json');
  const anchorPath = join(trust, 'workspace-bindings.anchor.json');
  const { privateKey, publicKey } = generateKeyPairSync('ec', { namedCurve: 'P-256' });
  writeFileSync(publicKeyPath, publicKey.export({ type: 'spki', format: 'pem' }), { mode: 0o600 });
  chmodSync(publicKeyPath, 0o600);
  const signer = (bytes) => ({
    algorithm: 'es256',
    signature: sign('sha256', bytes, privateKey).toString('base64'),
  });
  const anchorInstaller = (bytes) => {
    writeFileSync(anchorPath, bytes, { mode: 0o600 });
    chmodSync(anchorPath, 0o600);
  };
  const anchorRemover = () => rmSync(anchorPath, { force: true });
  const options = {
    home, registryPath, publicKeyPath, anchorPath, rootPublicKey: false, rootAnchor: false,
    signer, anchorInstaller, anchorRemover,
  };
  return {
    home, registryPath, publicKeyPath, anchorPath, privateKey,
    signer, anchorInstaller, anchorRemover, options,
  };
}

function replaceSignedRegistry(setup, payload) {
  const payloadBytes = Buffer.from(canonicalJSONStringify(payload));
  const proof = setup.signer(payloadBytes);
  const registryBytes = Buffer.from(`${JSON.stringify({
    algorithm: proof.algorithm,
    payload,
    signature: proof.signature,
  })}\n`);
  writeFileSync(setup.registryPath, registryBytes, { mode: 0o600 });
  chmodSync(setup.registryPath, 0o600);
  setup.anchorInstaller(Buffer.from(`${canonicalJSONStringify(
    bindingRegistryAnchor(registryBytes, payload.epoch),
  )}\n`));
}

test('initial Personal binding uses an ephemeral portable signer and persists only its public key', async () => {
  const home = mkdtempSync(join(tmpdir(), 'pulse-binding-bootstrap.'));
  const repository = makeRepository(home, 'project');
  const trust = join(home, 'trust');
  const registryPath = join(home, '.pulse', 'supervisor', 'workspace-bindings.json');
  const publicKeyPath = join(trust, 'workspace-bindings.pub.pem');
  const anchorPath = join(trust, 'workspace-bindings.anchor.json');
  mkdirSync(trust, { recursive: true, mode: 0o700 });
  let publicKeyInstalls = 0;

  const binding = await createInitialPersonalWorkspaceBinding({
    cwd: repository,
    home,
    principalID: 'principal_nik',
    port: 18801,
    registryPath,
    publicKeyPath,
    anchorPath,
    rootPublicKey: false,
    rootAnchor: false,
    publicKeyInstaller: (bytes) => {
      publicKeyInstalls += 1;
      writeFileSync(publicKeyPath, bytes, { mode: 0o600 });
      chmodSync(publicKeyPath, 0o600);
    },
    publicKeyRemover: () => rmSync(publicKeyPath, { force: true }),
    anchorInstaller: (bytes) => {
      writeFileSync(anchorPath, bytes, { mode: 0o600 });
      chmodSync(anchorPath, 0o600);
    },
    anchorRemover: () => rmSync(anchorPath, { force: true }),
  });

  assert.equal(binding.mode, 'personal');
  assert.equal(publicKeyInstalls, 1);
  assert.equal(JSON.parse(readFileSync(registryPath, 'utf8')).algorithm, 'ed25519');
  assert.doesNotMatch(readFileSync(publicKeyPath, 'utf8'), /PRIVATE KEY/);
  assert.equal(resolveWorkspaceBinding({
    cwd: repository,
    registryPath,
    publicKeyPath,
    anchorPath,
    rootAnchor: false,
  }).binding_id, binding.binding_id);

  await assert.rejects(
    createInitialPersonalWorkspaceBinding({
      cwd: repository,
      home,
      principalID: 'principal_nik',
      registryPath,
      publicKeyPath,
      anchorPath,
      rootPublicKey: false,
      rootAnchor: false,
      publicKeyInstaller: () => { publicKeyInstalls += 1; },
      publicKeyRemover: () => {},
      anchorInstaller: () => {},
      anchorRemover: () => {},
    }),
    /binding_admin_initial_binding_exists/,
  );
  assert.equal(publicKeyInstalls, 1);
});

test('same-principal Personal projects reuse one exact vault while retaining separate bindings', async () => {
  const setup = fixture();
  const firstRepository = makeRepository(setup.home, 'first-personal-project');
  const secondRepository = makeRepository(setup.home, 'second-personal-project');

  const first = await createWorkspaceBinding({
    ...setup.options,
    cwd: firstRepository,
    mode: 'personal',
    principalID: 'principal_nik',
    port: 18801,
  });
  const second = await createWorkspaceBinding({
    ...setup.options,
    cwd: secondRepository,
    mode: 'personal',
    principalID: 'principal_nik',
  });

  assert.notEqual(first.binding_id, second.binding_id);
  assert.notEqual(first.workspace.workspace_id, second.workspace.workspace_id);
  assert.deepEqual(second.personal, first.personal);
  assert.equal(second.personal.base_url, 'http://127.0.0.1:18801');

  const registry = verifyBindingRegistry({
    registryPath: setup.registryPath,
    publicKeyPath: setup.publicKeyPath,
  });
  assert.equal(registry.bindings.length, 2);
  assert.equal(new Set(registry.bindings.map((binding) => binding.personal.store_id)).size, 1);
  assert.equal(resolveWorkspaceBinding({
    cwd: firstRepository,
    registryPath: setup.registryPath,
    publicKeyPath: setup.publicKeyPath,
    anchorPath: setup.anchorPath,
    rootAnchor: false,
  }).personal.store_id, first.personal.store_id);
  assert.equal(resolveWorkspaceBinding({
    cwd: secondRepository,
    registryPath: setup.registryPath,
    publicKeyPath: setup.publicKeyPath,
    anchorPath: setup.anchorPath,
    rootAnchor: false,
  }).personal.store_id, first.personal.store_id);
});

test('same-principal Personal reuse rejects a conflicting explicit daemon port', async () => {
  const setup = fixture();
  const firstRepository = makeRepository(setup.home, 'first-personal-project');
  const secondRepository = makeRepository(setup.home, 'second-personal-project');
  await createWorkspaceBinding({
    ...setup.options,
    cwd: firstRepository,
    mode: 'personal',
    principalID: 'principal_nik',
    port: 18801,
  });
  const before = readFileSync(setup.registryPath);

  await assert.rejects(
    createWorkspaceBinding({
      ...setup.options,
      cwd: secondRepository,
      mode: 'personal',
      principalID: 'principal_nik',
      port: 18802,
    }),
    /binding_admin_personal_store_port_mismatch/,
  );
  assert.deepEqual(readFileSync(setup.registryPath), before);
});

test('fragmented legacy Personal stores block canonical reuse instead of creating a third store', async () => {
  const setup = fixture();
  const firstRepository = makeRepository(setup.home, 'first-fragment');
  const secondRepository = makeRepository(setup.home, 'second-fragment');
  const thirdRepository = makeRepository(setup.home, 'third-project');
  await createWorkspaceBinding({
    ...setup.options,
    cwd: firstRepository,
    mode: 'personal',
    principalID: 'principal_nik',
    port: 18801,
  });
  await createWorkspaceBinding({
    ...setup.options,
    cwd: secondRepository,
    mode: 'personal',
    principalID: 'principal_dima',
    port: 18802,
  });
  const fragmented = verifyBindingRegistry({
    registryPath: setup.registryPath,
    publicKeyPath: setup.publicKeyPath,
  });
  fragmented.bindings.find((binding) =>
    binding.workspace.workspace_id === canonicalizeWorkspace(secondRepository).workspace_id
  ).principal_ref = 'principal_nik';
  replaceSignedRegistry(setup, fragmented);
  const before = readFileSync(setup.registryPath);

  await assert.rejects(
    createWorkspaceBinding({
      ...setup.options,
      cwd: thirdRepository,
      mode: 'personal',
      principalID: 'principal_nik',
    }),
    /binding_admin_personal_store_fragmented/,
  );
  assert.deepEqual(readFileSync(setup.registryPath), before);
});

test('replacing the same workspace bumps the epoch and leaves exactly one unambiguous binding', async () => {
  const setup = fixture();
  const repository = makeRepository(setup.home, 'project');
  const first = await createWorkspaceBinding({
    ...setup.options,
    cwd: repository,
    mode: 'personal',
    principalID: 'principal_nik',
    port: 18801,
  });
  const replacement = await createWorkspaceBinding({
    ...setup.options, cwd: repository, mode: 'personal', principalID: 'principal_nik', port: 18801,
  });

  assert.notEqual(first.binding_id, replacement.binding_id);
  const registry = verifyBindingRegistry({
    registryPath: setup.registryPath,
    publicKeyPath: setup.publicKeyPath,
  });
  const workspaceID = canonicalizeWorkspace(repository).workspace_id;
  assert.equal(registry.epoch, 2);
  assert.equal(registry.bindings.filter((item) => item.workspace.workspace_id === workspaceID).length, 1);
  assert.equal(registry.bindings[0].binding_id, replacement.binding_id);
  assert.equal(registry.bindings[0].resolver_epoch, 2);
  assert.equal(resolveWorkspaceBinding({
    cwd: repository,
    registryPath: setup.registryPath,
    publicKeyPath: setup.publicKeyPath,
    anchorPath: setup.anchorPath,
    rootAnchor: false,
  }).binding_id, replacement.binding_id);
});

test('root anchor rejects replaying an older valid signed registry after a Personal binding update', async () => {
  const setup = fixture();
  const repository = makeRepository(setup.home, 'project');
  await createWorkspaceBinding({
    ...setup.options, cwd: repository, mode: 'personal', principalID: 'principal_nik', port: 18801,
  });
  const oldSignedRegistry = readFileSync(setup.registryPath);
  await createWorkspaceBinding({
    ...setup.options, cwd: repository, mode: 'personal', principalID: 'principal_nik', port: 18801,
  });

  writeFileSync(setup.registryPath, oldSignedRegistry, { mode: 0o600 });
  assert.throws(
    () => resolveWorkspaceBinding({
      cwd: repository, registryPath: setup.registryPath, publicKeyPath: setup.publicKeyPath,
      anchorPath: setup.anchorPath, rootAnchor: false,
    }),
    (error) => error instanceof BindingError && error.code === 'binding_anchor_mismatch',
  );
});

test('anchor installation failure preserves the previous registry and anchor byte-for-byte', async () => {
  const setup = fixture();
  const repository = makeRepository(setup.home, 'project');
  await createWorkspaceBinding({
    ...setup.options, cwd: repository, mode: 'personal', principalID: 'principal_nik', port: 18801,
  });
  const registryBefore = readFileSync(setup.registryPath);
  const anchorBefore = readFileSync(setup.anchorPath);

  await assert.rejects(
    createWorkspaceBinding({
      ...setup.options, cwd: repository, mode: 'personal', principalID: 'principal_nik', port: 18801,
      anchorInstaller: () => { throw new Error('simulated_root_anchor_install_failure'); },
    }),
    /simulated_root_anchor_install_failure/,
  );
  assert.deepEqual(readFileSync(setup.registryPath), registryBefore);
  assert.deepEqual(readFileSync(setup.anchorPath), anchorBefore);
});

test('a bad presence signature is rejected before rename and preserves the trusted registry byte-for-byte', async () => {
  const setup = fixture();
  const repository = makeRepository(setup.home, 'project');
  const existing = await createWorkspaceBinding({
    ...setup.options,
    cwd: repository,
    mode: 'personal',
    principalID: 'principal_nik',
    port: 18801,
  });
  const before = readFileSync(setup.registryPath);

  await assert.rejects(
    createWorkspaceBinding({
      ...setup.options, cwd: repository, mode: 'personal', principalID: 'principal_nik', port: 18801,
      signer: () => ({ algorithm: 'es256', signature: Buffer.alloc(64, 7).toString('base64') }),
    }),
    (error) => error instanceof BindingError && error.code === 'binding_signature_invalid',
  );

  assert.deepEqual(readFileSync(setup.registryPath), before);
  const resolved = resolveWorkspaceBinding({
    cwd: repository,
    registryPath: setup.registryPath,
    publicKeyPath: setup.publicKeyPath,
    anchorPath: setup.anchorPath,
    rootAnchor: false,
  });
  assert.equal(resolved.binding_id, existing.binding_id);
  assert.equal(resolved.mode, 'personal');
});

test('parallel onboarding is serialized by the portable private lock without a lost registry update', async () => {
  const setup = fixture();
  const firstRepository = makeRepository(setup.home, 'first-project');
  const secondRepository = makeRepository(setup.home, 'second-project');

  await Promise.all([
    createWorkspaceBinding({
      ...setup.options, cwd: firstRepository, mode: 'personal', principalID: 'principal_nik',
    }),
    createWorkspaceBinding({
      ...setup.options, cwd: secondRepository, mode: 'personal', principalID: 'principal_dima',
    }),
  ]);

  const registry = verifyBindingRegistry({
    registryPath: setup.registryPath,
    publicKeyPath: setup.publicKeyPath,
  });
  assert.equal(registry.epoch, 2);
  assert.equal(registry.bindings.length, 2);
  assert.deepEqual(registry.bindings.map((binding) => binding.resolver_epoch).sort(), [1, 2]);
  assert.deepEqual(
    registry.bindings.map((binding) => binding.personal.base_url).sort(),
    ['http://127.0.0.1:18789', 'http://127.0.0.1:18790'],
  );
  assert.equal(resolveWorkspaceBinding({
    cwd: firstRepository, registryPath: setup.registryPath, publicKeyPath: setup.publicKeyPath,
    anchorPath: setup.anchorPath, rootAnchor: false,
  }).resolver_epoch, 1);
  assert.equal(resolveWorkspaceBinding({
    cwd: secondRepository, registryPath: setup.registryPath, publicKeyPath: setup.publicKeyPath,
    anchorPath: setup.anchorPath, rootAnchor: false,
  }).resolver_epoch, 2);
});

test('ordinary binding recovery does not take the registry lock when there is no journal', async () => {
  const setup = fixture();
  const repository = makeRepository(setup.home, 'no-recovery-needed');
  await createWorkspaceBinding({
    ...setup.options, cwd: repository, mode: 'personal', principalID: 'principal_nik', port: 18801,
  });
  const base = createPlatformServices();
  let lockAttempts = 0;
  const platformServices = {
    ...base,
    acquirePrivateLock(...args) {
      lockAttempts += 1;
      return base.acquirePrivateLock(...args);
    },
  };

  assert.deepEqual(await recoverWorkspaceBindingTransaction({
    ...setup.options,
    platformServices,
  }), { status: 'none' });
  assert.equal(lockAttempts, 0);
});

test('binding transaction recovery closes every kill point under the registry lock', async () => {
  for (const [killPoint, expectedMode] of [
    ['journal_prepared', 'personal'],
    ['anchor_installed', 'personal'],
    ['registry_replaced', 'personal'],
  ]) {
    const setup = fixture();
    const repository = makeRepository(setup.home, `project-${killPoint}`);
    await createWorkspaceBinding({
      ...setup.options, cwd: repository, mode: 'personal', principalID: 'principal_nik', port: 18801,
    });
    const modulePath = new URL('./binding-admin.js', import.meta.url).href;
    const child = spawnSync(process.execPath, ['--input-type=module', '-e', `
      import { createPrivateKey, sign } from 'node:crypto';
      import { chmodSync, rmSync, writeFileSync } from 'node:fs';
      const config = JSON.parse(process.env.PULSE_BINDING_KILL_FIXTURE);
      const { createWorkspaceBinding } = await import(config.modulePath);
      const privateKey = createPrivateKey(config.privateKey);
      await createWorkspaceBinding({
        home: config.home,
        registryPath: config.registryPath,
        publicKeyPath: config.publicKeyPath,
        anchorPath: config.anchorPath,
        rootPublicKey: false,
        rootAnchor: false,
        cwd: config.repository,
        mode: 'personal',
        principalID: 'principal_other',
        port: 18802,
        signer: (bytes) => ({
          algorithm: 'es256',
          signature: sign('sha256', bytes, privateKey).toString('base64'),
        }),
        anchorInstaller: (bytes) => {
          writeFileSync(config.anchorPath, bytes, { mode: 0o600 });
          chmodSync(config.anchorPath, 0o600);
        },
        anchorRemover: () => rmSync(config.anchorPath, { force: true }),
        onTransitionPhase: (phase) => {
          if (phase === config.killPoint) process.kill(process.pid, 'SIGKILL');
        },
      });
    `], {
      env: {
        ...process.env,
        PULSE_BINDING_KILL_FIXTURE: JSON.stringify({
          modulePath, killPoint, repository,
          home: setup.home,
          registryPath: setup.registryPath,
          publicKeyPath: setup.publicKeyPath,
          anchorPath: setup.anchorPath,
          privateKey: setup.privateKey.export({ type: 'pkcs8', format: 'pem' }),
        }),
      },
      encoding: 'utf8',
      timeout: 10_000,
    });
    assert.equal(child.signal, 'SIGKILL', `${killPoint}: ${child.stderr}`);
    const journalPath = `${setup.registryPath}.transaction.json`;
    assert.equal(existsSync(journalPath), true, `${killPoint} must leave a durable journal`);

    await recoverWorkspaceBindingTransaction(setup.options);

    assert.equal(existsSync(journalPath), false, `${killPoint} must clear the settled journal`);
    assert.equal(resolveWorkspaceBinding({
      cwd: repository,
      registryPath: setup.registryPath,
      publicKeyPath: setup.publicKeyPath,
      anchorPath: setup.anchorPath,
      rootAnchor: false,
    }).mode, expectedMode);
  }
});

test('binding recovery rejects a corrupted transaction journal without changing the trusted pair', async () => {
  const setup = fixture();
  const repository = makeRepository(setup.home, 'corrupt-journal-project');
  const original = await createWorkspaceBinding({
    ...setup.options, cwd: repository, mode: 'personal', principalID: 'principal_nik', port: 18801,
  });
  writeFileSync(`${setup.registryPath}.transaction.json`, '{}\n', { mode: 0o600 });

  await assert.rejects(
    recoverWorkspaceBindingTransaction(setup.options),
    /binding_admin_transaction_invalid/,
  );
  assert.equal(resolveWorkspaceBinding({
    cwd: repository,
    registryPath: setup.registryPath,
    publicKeyPath: setup.publicKeyPath,
    anchorPath: setup.anchorPath,
    rootAnchor: false,
  }).binding_id, original.binding_id);
});
