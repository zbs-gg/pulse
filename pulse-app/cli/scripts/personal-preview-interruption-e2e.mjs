#!/usr/bin/env node

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { pathToFileURL, fileURLToPath } from 'node:url';

const scriptRoot = dirname(fileURLToPath(import.meta.url));
const cliRoot = resolve(scriptRoot, '..');
const digest = (bytes) => createHash('sha256').update(bytes).digest('hex');

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: options.cwd,
    env: options.env ?? process.env,
    encoding: 'utf8',
    input: options.input,
    stdio: ['pipe', 'pipe', 'pipe'],
    timeout: options.timeout ?? 120_000,
  });
  if (result.status !== (options.status ?? 0)) {
    throw new Error(`${command} ${args.join(' ')} exited ${result.status}\n${result.stdout}\n${result.stderr}`);
  }
  return result;
}

function packedTarball(root) {
  const npmArgs = ['npm', 'pack', '--json', '--pack-destination', root];
  const [command, args] = process.platform === 'darwin'
    ? ['/usr/bin/lockf', ['-k', '-t', '300', '/tmp/pulse-product-pack.lock', ...npmArgs]]
    : ['/usr/bin/flock', ['-w', '300', '/tmp/pulse-product-pack.lock', ...npmArgs]];
  run(command, args, {
    cwd: cliRoot,
    timeout: 330_000,
    env: { ...process.env, PULSE_ALLOW_UNNOTARIZED_INTERNAL_PREVIEW: '1' },
  });
  const tarballs = readdirSync(root).filter((name) => name.endsWith('.tgz'));
  assert.equal(tarballs.length, 1);
  return join(root, tarballs[0]);
}

function artifact(bytes) {
  return {
    id: 'pulse-daemon', kind: 'daemon', version: '0.8.0', epoch: 7,
    url: 'https://releases.zbs.gg/pulse/0.8.0/daemon.dmg',
    origin: 'https://releases.zbs.gg', bytes: bytes.length, sha256: digest(bytes),
  };
}

const root = mkdtempSync(join(tmpdir(), 'pulse-personal-interruption.'));
try {
  const installRoot = join(root, 'packed-cli');
  mkdirSync(installRoot, { recursive: true, mode: 0o700 });
  const tarball = packedTarball(root);
  run('npm', ['install', '--ignore-scripts', '--no-audit', '--no-fund', '--prefix', installRoot, tarball], {
    cwd: root,
  });
  const packedRoot = join(installRoot, 'node_modules', '@zbs-gg', 'pulse');
  const installerModule = await import(pathToFileURL(join(packedRoot, 'src', 'artifact-installer.js')));
  const personalModuleURL = pathToFileURL(join(packedRoot, 'src', 'personal-install.js')).href;
  const tools = join(root, 'tools');
  mkdirSync(tools, { mode: 0o700 });
  symlinkSync(process.execPath, join(tools, 'node'));
  symlinkSync('/usr/bin/git', join(tools, 'git'));
  assert.equal(existsSync(join(tools, 'codex')), false);
  assert.equal(existsSync(join(tools, 'go')), false);
  assert.equal(existsSync(join(tools, 'python')), false);

  const bytes = Buffer.from('packed-personal-resumable-artifact');
  const stagingRoot = join(root, 'downloads');
  let fetchCalls = 0;
  const fetchImpl = async (_url, options) => {
    fetchCalls += 1;
    if (fetchCalls === 1) {
      async function* body() {
        yield bytes.subarray(0, 9);
        throw new Error('simulated network interruption');
      }
      return { status: 200, headers: new Headers({ etag: '"personal-7"' }), body: body() };
    }
    assert.equal(options.headers.Range, 'bytes=9-');
    assert.equal(options.headers['If-Range'], '"personal-7"');
    return new Response(bytes.subarray(9), {
      status: 206,
      headers: {
        etag: '"personal-7"',
        'content-range': `bytes 9-${bytes.length - 1}/${bytes.length}`,
      },
    });
  };
  await assert.rejects(installerModule.downloadVerifiedArtifact(artifact(bytes), {
    stagingRoot, fetchImpl, availableBytes: () => 1_000_000, minimumFreeBytes: 0,
  }), /artifact_download_interrupted/);
  assert.equal(existsSync(join(stagingRoot, `${digest(bytes)}.verified`)), false,
    'partial bytes must never be activated or labeled verified');
  const resumed = await installerModule.downloadVerifiedArtifact(artifact(bytes), {
    stagingRoot, fetchImpl, availableBytes: () => 1_000_000, minimumFreeBytes: 0,
  });
  assert.equal(resumed.resumed_from, 9);
  assert.deepEqual(readFileSync(resumed.path), bytes);

  const statePath = join(root, 'install-state.json');
  const receiptPath = join(root, 'install-receipt.json');
  const vaultMarker = join(root, 'personal-vault.marker');
  writeFileSync(vaultMarker, 'preserve-private-memory\n', { mode: 0o600 });
  const runner = String.raw`
    import fs from 'node:fs';
    const { runPersonalInstall } = await import(process.env.PULSE_INSTALL_MODULE);
    const statePath = process.env.PULSE_STATE_PATH;
    const receiptPath = process.env.PULSE_RECEIPT_PATH;
    const interrupted = process.env.PULSE_INTERRUPT === '1';
    const empty = { runtime:false, presence:false, principal:null, binding:null, core:false, activation:false, health:false, runtime_mutations:0, core_mutations:0 };
    const state = fs.existsSync(statePath) ? JSON.parse(fs.readFileSync(statePath, 'utf8')) : empty;
    const save = () => fs.writeFileSync(statePath, JSON.stringify(state), { mode:0o600 });
    const plan = { schema:'pulse.personal_install_plan.v2', contract_version: 2, outcome:'ready_to_install', reason_codes:[], detected:{ workspace:{ workspace_id:'workspace_packed', repository_id:'repository_packed', canonical_path:'/private/project' }, hosts:[{host:'cursor',detected:true,compatible:true,activation_target:true}] } };
    const dependencies = {
      inspectRuntime: async () => ({ ready:state.runtime }),
      provisionRuntime: async () => { state.runtime=true; state.runtime_mutations+=1; save(); },
      inspectPresence: async () => ({ ready:state.presence }),
      installPresence: async () => { state.presence=true; save(); },
      inspectPrincipal: async () => state.principal,
      createPrincipal: async () => { state.principal={ principal_id:'principal_0123456789abcdef0123456789abcdef' }; save(); return state.principal; },
      inspectBinding: async () => state.binding ? { ready:true, binding:state.binding } : { ready:false, status:'missing' },
      createBinding: async ({principal}) => { state.binding={ binding_id:'binding_packed', principal_ref:principal.principal_id }; save(); return state.binding; },
      inspectCore: async () => ({ ready:state.core, full_retrieval:state.core, context:{store_id:'store_personal_packed'} }),
      activateCore: async () => { state.core=true; state.core_mutations+=1; save(); },
      inspectActivation: async () => ({
        product_ready:state.activation,
        parity:state.activation?'complete':'blocked',
        hosts:[{
          host:'cursor', detected:true, compatible:true,
          installed:state.activation, mcp_ready:state.activation,
          activated:state.activation, verified:state.activation,
          lifecycle_ready:state.activation, reload_required:false,
          milestones:state.activation?['turn_capture']:[],
          reason_code:state.activation?'cursor_verified':'cursor_activation_required',
        }],
      }),
      activateHosts: async () => { state.activation=true; state.health=true; save(); },
      inspectHealth: async () => ({ ready:state.health, full_retrieval:state.health }),
      writeReceipt: async (receipt) => { fs.writeFileSync(receiptPath, JSON.stringify(receipt), {mode:0o600}); if (interrupted && receipt.reason_code === 'checkpoint_artifacts_staged') process.exit(73); return 'receipt_packed_interruption'; },
    };
    const prior = fs.existsSync(receiptPath) ? JSON.parse(fs.readFileSync(receiptPath, 'utf8')) : null;
    const result = await runPersonalInstall({ plan, consent:true, resumeEvidence:prior?.outcome === 'partial', dependencies });
    process.stdout.write(JSON.stringify(result));
  `;
  const environment = {
    ...process.env,
    PATH: tools,
    PULSE_INSTALL_MODULE: personalModuleURL,
    PULSE_STATE_PATH: statePath,
    PULSE_RECEIPT_PATH: receiptPath,
  };
  const interrupted = run(process.execPath, ['--input-type=module', '--eval', runner], {
    cwd: root, env: { ...environment, PULSE_INTERRUPT: '1' }, status: 73,
  });
  assert.equal(interrupted.stdout, '');
  assert.equal(JSON.parse(readFileSync(statePath, 'utf8')).runtime_mutations, 1);
  const repaired = run(process.execPath, ['--input-type=module', '--eval', runner], {
    cwd: root, env: environment,
  });
  const repairedResult = JSON.parse(repaired.stdout);
  assert.equal(repairedResult.outcome, 'ready');
  assert.equal(repairedResult.reason_code, 'resumed');
  assert.equal(JSON.parse(readFileSync(statePath, 'utf8')).runtime_mutations, 1);
  assert.equal(JSON.parse(readFileSync(statePath, 'utf8')).core_mutations, 1);
  assert.equal(readFileSync(vaultMarker, 'utf8'), 'preserve-private-memory\n');

  process.stdout.write(`${JSON.stringify({
    schema: 'pulse.personal_preview_interruption.v1',
    authority: 'synthetic-test',
    content_free: true,
    package_source: 'npm-pack',
    runtime_path: { node: true, codex: false, git: true, go: false, python: false },
    artifact_resume_offset: resumed.resumed_from,
    partial_activation: false,
    install_resumed: true,
    host_neutral_checkpoint: true,
    private_vault_preserved: true,
    production_install_proof: false,
  })}\n`);
  process.stdout.write('Pulse Personal packed interruption and repair proof passed.\n');
} finally {
  if (process.env.PULSE_KEEP_PERSONAL_INTERRUPTION_ROOT === '1') {
    process.stderr.write(`kept Personal interruption root: ${root}\n`);
  } else {
    rmSync(root, { recursive: true, force: true });
  }
}
