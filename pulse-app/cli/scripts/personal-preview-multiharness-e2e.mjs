#!/usr/bin/env node

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readdirSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const scriptRoot = dirname(fileURLToPath(import.meta.url));
const cliRoot = resolve(scriptRoot, '..');
const HOSTS = ['claude-code', 'codex', 'cursor'];

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: options.cwd,
    env: options.env ?? process.env,
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
    timeout: options.timeout ?? 330_000,
  });
  if (result.status !== 0) {
    throw new Error(`${command} ${args.join(' ')} exited ${result.status}\n${result.stdout}\n${result.stderr}`);
  }
  return result;
}

function packedPackage(root) {
  const npmArgs = ['npm', 'pack', '--json', '--pack-destination', root];
  const [command, args] = process.platform === 'darwin'
    ? ['/usr/bin/lockf', ['-k', '-t', '300', '/tmp/pulse-product-pack.lock', ...npmArgs]]
    : ['/usr/bin/flock', ['-w', '300', '/tmp/pulse-product-pack.lock', ...npmArgs]];
  run(command, args, {
    cwd: cliRoot,
    env: { ...process.env, PULSE_ALLOW_UNNOTARIZED_INTERNAL_PREVIEW: '1' },
  });
  const tarballs = readdirSync(root).filter((name) => name.endsWith('.tgz'));
  assert.equal(tarballs.length, 1);
  const installRoot = join(root, 'packed');
  run('npm', [
    'install', '--ignore-scripts', '--no-audit', '--no-fund', '--prefix', installRoot, join(root, tarballs[0]),
  ], { cwd: root });
  return join(installRoot, 'node_modules', '@zbs-gg', 'pulse');
}

function gitRepository(root) {
  const path = join(root, 'project');
  mkdirSync(path, { recursive: true, mode: 0o700 });
  run('/usr/bin/git', ['init', '-q'], { cwd: path });
  run('/usr/bin/git', ['config', 'user.email', 'pulse-matrix@example.test'], { cwd: path });
  run('/usr/bin/git', ['config', 'user.name', 'Pulse Matrix'], { cwd: path });
  run('/usr/bin/git', ['commit', '--allow-empty', '-q', '-m', 'fixture'], { cwd: path });
  return path;
}

function verifiedRelease() {
  return {
    schema: 'pulse.verified_release_manifest.v1',
    version: '0.7.0',
    epoch: 7,
    manifest_digest: 'a'.repeat(64),
    artifacts: Object.fromEntries([
      ['daemon', 10], ['embedder-runtime', 20], ['model', 30], ['plugin-runtime', 40], ['presence-helper', 50],
    ].map(([kind, bytes]) => [kind, {
      bytes,
      id: `pulse-${kind}`,
      origin: 'https://releases.zbs.gg',
      url: `https://releases.zbs.gg/${kind}`,
    }])),
  };
}

const cleanState = {
  binding: 'missing', daemon: 'missing', embedder: 'missing', hook_trust: 'missing',
  install_receipt: 'missing', plugin: 'missing', presence: 'not_installed',
  principal: 'missing', runtime: 'missing', vault: 'missing',
};

function detector(host, selected, incompatible) {
  if (incompatible.includes(host)) {
    return () => ({
      available: false,
      executable_path: `/usr/local/bin/${host}`,
      reason_code: `${host.replaceAll('-', '_')}_version_invalid`,
    });
  }
  if (!selected.includes(host)) {
    return () => ({ available: false, reason_code: `${host.replaceAll('-', '_')}_missing` });
  }
  if (host === 'cursor') return () => ({ available: true, app_path: '/Applications/Cursor.app', reason_code: null });
  return () => ({
    available: true,
    executable_path: `/usr/local/bin/${host === 'claude-code' ? 'claude' : 'codex'}`,
    executable_sha256: 'b'.repeat(64),
    version: host === 'claude-code' ? '2.1.196' : '0.114.0',
    reason_code: null,
  });
}

function productFixture({ failHosts = [] } = {}) {
  const state = {
    runtime: false,
    presence: false,
    principal: null,
    binding: null,
    core: false,
    coreMutations: 0,
    active: new Set(),
    failHosts: new Set(failHosts),
    calls: [],
    receipts: [],
  };
  const context = {
    binding_id: 'binding_shared_product',
    edge_digest: 'c'.repeat(64),
    store_id: 'store_personal_shared',
  };
  const registry = Object.fromEntries(HOSTS.map((host) => [host, {
    inspect: async (actual) => {
      assert.equal(actual, context);
      state.calls.push(`inspect:${host}`);
      return state.active.has(host)
        ? { ready: true, lifecycle_ready: true, reason_code: `${host.replaceAll('-', '_')}_verified` }
        : { ready: false, lifecycle_ready: false, reason_code: `${host.replaceAll('-', '_')}_activation_required` };
    },
    activate: async (actual) => {
      assert.equal(actual, context);
      state.calls.push(`activate:${host}`);
      if (state.failHosts.has(host)) throw new Error(`${host}_fixture_failure`);
      state.active.add(host);
    },
  }]));
  return { state, context, registry };
}

const root = mkdtempSync(join(tmpdir(), 'pulse-personal-multiharness.'));
try {
  const packedRoot = packedPackage(root);
  const { buildPersonalInstallPlan } = await import(pathToFileURL(join(packedRoot, 'src', 'install-plan.js')));
  const { runPersonalInstall } = await import(pathToFileURL(join(packedRoot, 'src', 'personal-install.js')));
  const {
    activateDetectedPersonalHosts,
    inspectDetectedPersonalHosts,
  } = await import(pathToFileURL(join(packedRoot, 'src', 'personal-host-adapters.js')));
  const project = gitRepository(root);
  const home = join(root, 'home');
  mkdirSync(home, { mode: 0o700 });

  const planFor = (selected = [], incompatible = []) => buildPersonalInstallPlan({
    cwd: project,
    home,
    codexHome: join(home, '.codex'),
    platform: 'darwin',
    architecture: 'arm64',
    nodeVersion: '20.18.0',
    detectClaude: detector('claude-code', selected, incompatible),
    detectCodex: detector('codex', selected, incompatible),
    detectCursor: detector('cursor', selected, incompatible),
    release: verifiedRelease(),
    detectResources: () => ({
      disk_free_bytes: 20 * 1024 ** 3,
      memory_total_bytes: 16 * 1024 ** 3,
      port_18789: 'free',
    }),
    currentState: cleanState,
  });

  const install = async (plan, product, mode = 'install') => {
    const hosts = plan.detected.hosts;
    const dependencies = {
      inspectRuntime: async () => ({ ready: product.state.runtime }),
      provisionRuntime: async () => { product.state.runtime = true; },
      inspectPresence: async () => ({ ready: product.state.presence }),
      installPresence: async () => { product.state.presence = true; },
      inspectPrincipal: async () => product.state.principal,
      createPrincipal: async () => {
        product.state.principal = { principal_id: 'principal_0123456789abcdef0123456789abcdef' };
        return product.state.principal;
      },
      inspectBinding: async () => product.state.binding
        ? { ready: true, binding: product.state.binding }
        : { ready: false, status: 'missing' },
      createBinding: async ({ principal }) => {
        product.state.binding = { binding_id: product.context.binding_id, principal_ref: principal.principal_id };
      },
      inspectCore: async () => ({
        ready: product.state.core,
        full_retrieval: product.state.core,
        context: product.context,
      }),
      activateCore: async () => {
        product.state.core = true;
        product.state.coreMutations += 1;
      },
      inspectActivation: async () => inspectDetectedPersonalHosts({
        context: product.context, hosts, registry: product.registry,
      }),
      activateHosts: async ({ activation }) => activateDetectedPersonalHosts({
        context: product.context, hosts, registry: product.registry, prior: activation,
      }),
      inspectHealth: async ({ activation }) => ({
        ready: product.state.core && activation.product_ready,
        full_retrieval: product.state.core,
        reason_code: activation.hosts[0]?.reason_code ?? 'supported_harness_activation_failed',
      }),
      writeReceipt: async (receipt) => {
        product.state.receipts.push(structuredClone(receipt));
        return 'receipt_multiharness_e2e';
      },
    };
    return runPersonalInstall({ plan, consent: true, dependencies, mode });
  };

  for (const host of HOSTS) {
    const product = productFixture();
    const result = await install(planFor([host]), product);
    assert.equal(result.outcome, 'ready', host);
    assert.deepEqual([...product.state.active], [host], host);
    assert.deepEqual(
      [...new Set(product.state.calls.map((call) => call.split(':')[1]))],
      [host],
      `${host} singleton invoked an absent harness`,
    );
    assert.equal(product.state.coreMutations, 1, host);
  }

  const noHostProduct = productFixture();
  const noHostPlan = planFor([]);
  assert.equal(noHostPlan.reason_codes.includes('supported_harness_missing'), true);
  assert.equal((await install(noHostPlan, noHostProduct)).outcome, 'action_required');
  assert.equal(noHostProduct.state.coreMutations, 0);

  const incompatibleProduct = productFixture();
  const incompatiblePlan = planFor([], ['claude-code']);
  assert.deepEqual(incompatiblePlan.reason_codes, ['supported_harness_incompatible']);
  assert.equal((await install(incompatiblePlan, incompatibleProduct)).outcome, 'action_required');
  assert.equal(incompatibleProduct.state.coreMutations, 0);

  const allHosts = productFixture();
  const allResult = await install(planFor(HOSTS), allHosts);
  assert.equal(allResult.host_status.parity, 'complete');
  assert.deepEqual([...allHosts.state.active].sort(), [...HOSTS].sort());
  assert.equal(allHosts.context.store_id, 'store_personal_shared');

  const attachedLater = productFixture();
  await install(planFor(['claude-code']), attachedLater);
  const callsBeforeAttach = attachedLater.state.calls.length;
  const attachedResult = await install(planFor(['claude-code', 'cursor']), attachedLater, 'repair');
  const attachCalls = attachedLater.state.calls.slice(callsBeforeAttach);
  assert.equal(attachedResult.host_status.parity, 'complete');
  assert.equal(attachCalls.includes('activate:claude-code'), false);
  assert.equal(attachCalls.includes('activate:cursor'), true);
  assert.equal(attachedLater.state.coreMutations, 1);

  const degraded = productFixture({ failHosts: ['cursor'] });
  const degradedResult = await install(planFor(['claude-code', 'cursor']), degraded);
  assert.equal(degradedResult.outcome, 'ready');
  assert.equal(degradedResult.host_status.parity, 'degraded');
  degraded.state.failHosts.delete('cursor');
  const repaired = await install(planFor(['claude-code', 'cursor']), degraded, 'repair');
  assert.equal(repaired.host_status.parity, 'complete');
  assert.equal(degraded.state.coreMutations, 1);

  process.stdout.write(`${JSON.stringify({
    schema: 'pulse.personal_preview_multiharness_orchestration.v1',
    authority: 'synthetic-test',
    content_free: true,
    package_source: 'npm-pack',
    public_command_under_test: false,
    singleton_hosts: HOSTS,
    absent_harness_invocation: false,
    shared_binding: true,
    synthetic_shared_store_id: allHosts.context.store_id,
    daemon_backed_cross_host_recall: false,
    later_host_attachment: true,
    degraded_parity_repair: true,
    no_host_zero_mutation: true,
    incompatible_host_zero_mutation: true,
    system_go_exposed: false,
    system_python_exposed: false,
    production_install_proof: false,
  })}\n`);
  process.stdout.write('Pulse Personal packed host-neutral orchestration contract passed; physical install and cross-host recall remain release attestations.\n');
} finally {
  if (process.env.PULSE_KEEP_PERSONAL_MULTIHARNESS_ROOT === '1') {
    process.stderr.write(`kept Personal multiharness root: ${root}\n`);
  } else {
    rmSync(root, { recursive: true, force: true });
  }
}
