import assert from 'node:assert/strict';
import { generateKeyPairSync, sign } from 'node:crypto';
import {
  chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

import { createWorkspaceBinding, recoverWorkspaceBindingTransaction } from './binding-admin.js';
import {
  BindingError, canonicalizeWorkspace, resolveWorkspaceBinding, verifyBindingRegistry,
} from './workspace-binding.js';

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

function teamOptions(setup, repository, overrides = {}) {
  return {
    ...setup.options,
    cwd: repository,
    mode: 'team',
    principalID: 'principal_nik',
    teamID: 'team_zbs',
    commonsProjectID: 'project_zbs',
    commonsStoreID: 'store_commons_zbs',
    commonsResource: 'https://pulse.example.test/mcp',
    ...overrides,
  };
}

test('Personal and Team onboarding creates physically separate exact topologies', async () => {
  const setup = fixture();
  const personalRepository = makeRepository(setup.home, 'personal-project');
  const teamRepository = makeRepository(setup.home, 'team-project');

  const personal = await createWorkspaceBinding({
    ...setup.options,
    cwd: personalRepository,
    mode: 'personal',
    principalID: 'principal_nik',
    port: 18801,
  });
  const team = await createWorkspaceBinding(teamOptions(setup, teamRepository, { port: 18802 }));

  assert.equal(personal.mode, 'personal');
  assert.ok(personal.personal);
  assert.equal(personal.desk, undefined);
  assert.equal(personal.commons, undefined);
  assert.match(personal.personal.data_dir, /\/\.pulse\/vaults\/personal\/store_personal_/);
  assert.match(personal.personal.cache_dir, /\/\.pulse\/caches\/personal\/store_personal_/);

  assert.equal(team.mode, 'team');
  assert.equal(team.desk.base_url, 'http://127.0.0.1:18802');
  assert.equal(team.commons.resource, 'https://pulse.example.test/mcp');
  assert.equal(team.commons.store_id, 'store_commons_zbs');
  assert.equal(team.commons.team_id, 'team_zbs');
  assert.equal(team.commons.project_id, 'project_zbs');
  assert.equal(team.commons.data_dir, undefined);
  assert.equal(team.commons.cache_dir, undefined);
  assert.notEqual(personal.personal.store_id, team.desk.store_id);
  assert.notEqual(personal.personal.data_dir, team.desk.data_dir);
  assert.notEqual(personal.personal.cache_dir, team.desk.cache_dir);
  assert.notEqual(personal.personal.credential_ref, team.desk.credential_ref);
  assert.notEqual(team.desk.credential_ref, team.commons.credential_ref);

  const registry = verifyBindingRegistry({
    registryPath: setup.registryPath,
    publicKeyPath: setup.publicKeyPath,
  });
  assert.equal(registry.epoch, 2);
  assert.equal(registry.bindings.length, 2);
  assert.deepEqual(registry.bindings.map((binding) => binding.resolver_epoch).sort(), [1, 2]);

  const resolvedPersonal = resolveWorkspaceBinding({
    cwd: personalRepository,
    registryPath: setup.registryPath,
    publicKeyPath: setup.publicKeyPath,
    anchorPath: setup.anchorPath,
    rootAnchor: false,
  });
  const resolvedTeam = resolveWorkspaceBinding({
    cwd: teamRepository,
    registryPath: setup.registryPath,
    publicKeyPath: setup.publicKeyPath,
    anchorPath: setup.anchorPath,
    rootAnchor: false,
  });
  assert.equal(resolvedPersonal.fallback, false);
  assert.equal(resolvedPersonal.resolver_epoch, 1);
  assert.equal(resolvedTeam.fallback, false);
  assert.equal(resolvedTeam.resolver_epoch, 2);
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
  const replacement = await createWorkspaceBinding(teamOptions(setup, repository, { port: 18802 }));

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

test('root anchor rejects replaying an older valid signed registry after a Team to Personal change', async () => {
  const setup = fixture();
  const repository = makeRepository(setup.home, 'project');
  await createWorkspaceBinding(teamOptions(setup, repository, { port: 18801 }));
  const oldSignedRegistry = readFileSync(setup.registryPath);
  await createWorkspaceBinding({
    ...setup.options, cwd: repository, mode: 'personal', principalID: 'principal_nik', port: 18802,
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
      ...teamOptions(setup, repository, { port: 18802 }),
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
      ...teamOptions(setup, repository),
      port: 18802,
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

test('parallel onboarding is serialized by lockf without a lost registry update', async () => {
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

test('Team onboarding accepts only canonical IDs, a local product port, and exact HTTPS /mcp', async () => {
  const setup = fixture();
  const repository = makeRepository(setup.home, 'project');
  const valid = {
    ...setup.options,
    cwd: repository,
    mode: 'team',
    principalID: 'principal_nik',
    teamID: 'team_zbs',
    commonsProjectID: 'project_zbs',
    commonsStoreID: 'store_commons_zbs',
    commonsResource: 'https://pulse.example.test/mcp',
    port: 18802,
  };
  const cases = [
    { principalID: '../private' },
    { teamID: 'TEAM-ZBS' },
    { commonsProjectID: 'workspace_pulse' },
    { commonsProjectID: 'project-' },
    { commonsStoreID: 'x' },
    { port: 80 },
    { port: 70000 },
    { commonsResource: 'http://pulse.example.test/mcp' },
    { commonsResource: 'https://localhost/mcp' },
    { commonsResource: 'https://pulse.example.test/team' },
    { commonsResource: 'https://pulse.example.test/mcp?team=zbs' },
    { commonsResource: 'https://pulse.example.test/mcp#fragment' },
  ];
  for (const overrides of cases) {
    await assert.rejects(createWorkspaceBinding({ ...valid, ...overrides }));
  }
  assert.equal(readFileSync(setup.publicKeyPath, 'utf8').includes('PUBLIC KEY'), true);
});

test('explicit local ports cannot collide across Personal or Team bindings', async () => {
  const setup = fixture();
  const firstRepository = makeRepository(setup.home, 'first-project');
  const secondRepository = makeRepository(setup.home, 'second-project');
  await createWorkspaceBinding({
    ...setup.options, cwd: firstRepository, mode: 'personal', principalID: 'principal_nik', port: 18801,
  });
  await assert.rejects(
    createWorkspaceBinding(teamOptions(setup, secondRepository, {
      principalID: 'principal_dima', port: 18801,
    })),
    /binding_admin_port_in_use/,
  );
});

test('a second workspace cannot alias the same Team Desk, while distinct teams remain supported', async () => {
  const setup = fixture();
  const firstRepository = makeRepository(setup.home, 'first-project');
  const secondRepository = makeRepository(setup.home, 'second-project');
  const thirdRepository = makeRepository(setup.home, 'third-project');
  const base = {
    ...setup.options, mode: 'team', principalID: 'principal_nik', teamID: 'team_zbs',
    commonsProjectID: 'project_zbs', commonsStoreID: 'store_commons_zbs',
    commonsResource: 'https://pulse.example.test/mcp',
  };
  await createWorkspaceBinding({ ...base, cwd: firstRepository, port: 18801 });
  await assert.rejects(
    createWorkspaceBinding({ ...base, cwd: secondRepository, port: 18802 }),
    /binding_admin_desk_binding_conflict/,
  );
  const other = await createWorkspaceBinding({
    ...base, cwd: thirdRepository, teamID: 'team_other', commonsStoreID: 'store_commons_other', port: 18803,
  });
  assert.equal(other.commons.team_id, 'team_other');
  assert.notEqual(other.desk.data_dir, join(setup.home, '.pulse', 'vaults', 'desks', 'team_zbs', 'principal_nik'));
});

test('binding transaction recovery closes every kill point under the registry lock', async () => {
  for (const [killPoint, expectedMode] of [
    ['journal_prepared', 'personal'],
    ['anchor_installed', 'team'],
    ['registry_replaced', 'team'],
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
        mode: 'team',
        principalID: 'principal_nik',
        teamID: 'team_zbs',
        commonsProjectID: 'project_zbs',
        commonsStoreID: 'store_commons_zbs',
        commonsResource: 'https://pulse.example.test/mcp',
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
