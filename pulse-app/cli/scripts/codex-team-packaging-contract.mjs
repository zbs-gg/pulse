import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, mkdtempSync, mkdirSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { pathToFileURL, fileURLToPath } from 'node:url';

import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';

const scriptDir = dirname(fileURLToPath(import.meta.url));
const cliRoot = resolve(scriptDir, '..');

function packArgs(destination) {
  const npmArgs = ['npm', 'pack', '--json', '--pack-destination', destination];
  return process.platform === 'darwin'
    ? ['/usr/bin/lockf', ['-k', '-t', '300', '/tmp/pulse-product-pack.lock', ...npmArgs]]
    : ['/usr/bin/flock', ['-w', '300', '/tmp/pulse-product-pack.lock', ...npmArgs]];
}

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: options.cwd,
    env: options.env ?? process.env,
    encoding: 'utf8',
    timeout: options.timeout ?? 180_000,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  if (result.status !== 0) {
    throw new Error(`${command} ${args.join(' ')} failed\n${result.stdout}\n${result.stderr}`);
  }
  return result;
}

const root = mkdtempSync(join(tmpdir(), 'pulse-codex-team-product.'));
const originalCwd = process.cwd();
let client;
try {
  const installRoot = join(root, 'install');
  mkdirSync(installRoot, { recursive: true });
  const [packCommand, packCommandArgs] = packArgs(root);
  run(packCommand, packCommandArgs, {
    cwd: cliRoot, timeout: 330_000,
    env: { ...process.env, PULSE_ALLOW_UNNOTARIZED_INTERNAL_PREVIEW: '1' },
  });
  const tarballs = readdirSync(root).filter((name) => name.endsWith('.tgz'));
  assert.equal(tarballs.length, 1);
  run('npm', [
    'install', '--ignore-scripts', '--no-audit', '--no-fund', '--prefix', installRoot,
    join(root, tarballs[0]),
  ], { cwd: root });

  const packedRoot = join(installRoot, 'node_modules', '@zbs-gg', 'pulse');
  const packedHelper = join(
    packedRoot, 'vendor', 'pulse-presence-helper', 'gg.zbs.pulse.presence-helper',
  );
  assert.equal(existsSync(packedHelper), true, 'signed macOS presence helper must ship in the package');
  if (process.platform === 'darwin') {
    run('/usr/bin/codesign', ['--verify', '--strict', '--verbose=2', packedHelper]);
  }
  const compositor = await import(pathToFileURL(join(packedRoot, 'src', 'product-compositor.js')));
  const remoteClient = await import(pathToFileURL(join(packedRoot, 'src', 'team-remote-client.js')));
  const teamStatus = await import(pathToFileURL(join(packedRoot, 'src', 'team-status.js')));
  const workspace = join(root, 'workspace');
  mkdirSync(workspace, { recursive: true });
  const binding = Object.freeze({
    mode: 'team', fallback: false, principal_ref: 'principal_codex_team_e2e',
    workspace: {
      workspace_id: 'workspace_codex_team_e2e',
      repository_id: 'repository_codex_team_e2e',
      canonical_path: workspace,
    },
    desk: { store_id: 'store_desk_codex_team_e2e' },
    commons: {
      store_id: 'store_commons_codex_team_e2e', team_id: 'team_codex_team_e2e',
      project_id: 'project_codex_team_e2e',
      resource: 'https://pulse-team.example.test/mcp',
      credential_ref: 'keychain:pulse/team/team_codex_team_e2e/principal_codex_team_e2e',
    },
  });
  for (const host of ['codex', 'claude-code']) {
    const composed = await compositor.composeBoundResumeEvidence(
      { binding, runtime: { store_id: binding.desk.store_id } },
      { session_id: 'session_codex_team_e2e', idempotency_key: `idem_${host}` },
      {
      host,
      request: async () => ({ resume_markdown: 'Desk-only private decision.' }),
      teamRequest: async (_binding, name, input) => {
        assert.equal(name, 'pulse_team_resume');
        assert.equal(input.active_context.project_id, binding.commons.project_id);
        assert.equal(input.active_context.repo_id, binding.workspace.repository_id);
        return {
          schema: 'pulse.team.resume_result.v1', fallback: false,
          returned_count: 1,
          sections: {
            where_we_left_off: [{ object_id: 'object_commons_1', text: 'Authorized team rule.' }],
            active_decisions: [], open_loops: [], do_not_repeat: [],
            relevant_emotional_state_context: [], suggested_next_step: [],
          },
        };
      },
      },
    );
    assert.match(composed.evidence[0], /Private Desk continuity \(local, private\)/);
    assert.match(composed.evidence[1], /Team Commons .*shared, authorized Commons evidence/);
    assert.equal(composed.commons.fallback, false);
  }

  await assert.rejects(
    remoteClient.callTeamRemoteTool(binding, 'pulse_team_remember', {}),
    (error) => error?.code === 'write_forbidden',
  );
  const installation = await teamStatus.inspectTeamInstallation(binding, {
    teamRequest: async () => ({
      schema: 'pulse.team.status_result.v1', mode: 'team-remote', fallback: false,
      team_id: binding.commons.team_id, store_id: binding.commons.store_id,
      membership_role: 'member',
      active_context: {
        project_id: binding.commons.project_id,
        repo_id: binding.workspace.repository_id,
        agent_id: binding.principal_ref,
      },
      effective_capabilities: ['pulse:connect', 'pulse:status', 'pulse:read', 'pulse:audit'],
      projection_state: 'ready', degraded: false, degraded_reasons: [],
    }),
  });
  assert.equal(installation.commons_agent_writes, false);
  assert.equal(installation.airlock_only_publication, true);
  assert.equal(installation.refresh_configured, true);
  assert.equal(installation.status, 'ready');
  assert.equal(installation.ready, true);

  const authority = join(root, 'team-authority.mjs');
  writeFileSync(authority, `
    export function resolveProductWorkspaceBinding() {
      return { binding_digest: '${'a'.repeat(64)}', resolver_epoch: 9, workspace: { canonical_path: ${JSON.stringify(workspace)} } };
    }
    export async function callBoundTeamTool(_resolved, host, name, input) {
      return { schema: 'pulse.packed_team_proxy.v1', host, name, input, fallback: false };
    }
  `, { mode: 0o600 });
  process.chdir(workspace);
  Object.assign(process.env, {
    PULSE_HOST_ADAPTER: 'codex',
    PULSE_PRODUCT_BINDING_MODE: 'team',
    PULSE_BINDING_DIGEST: 'a'.repeat(64),
    PULSE_RESOLVER_EPOCH: '9',
    PULSE_HOST_WORKSPACE: workspace,
    PULSE_HOST_AUTHORITY_MODULE: pathToFileURL(authority).href,
    PULSE_HOST_RUNTIME_MODULE: pathToFileURL(authority).href,
    PULSE_RUNTIME_MODE: 'local-stdio',
    PULSE_MCP_MODE: 'daemon',
    PULSE_DATA_DIR: join(root, 'data'),
  });
  const packedMcp = await import(`${pathToFileURL(join(packedRoot, 'vendor', 'pulse-mcp-dist', 'index.js')).href}?e2e=${Date.now()}`);
  const server = packedMcp.createPulseMcpServer();
  const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair();
  client = new Client({ name: 'pulse-packed-team-e2e', version: '1' });
  await Promise.all([server.connect(serverTransport), client.connect(clientTransport)]);
  const tools = await client.listTools();
  const names = tools.tools.map(({ name }) => name);
  assert.ok(names.includes('pulse_remember'));
  assert.ok(names.includes('pulse_team_resume'));
  assert.equal(names.includes('pulse_team_remember'), false);
  assert.equal(names.includes('pulse_team_graph_delta'), false);
  assert.equal(names.includes('pulse_team_delete'), false);
  const status = await client.callTool({
    name: 'pulse_team_status',
    arguments: { schema: 'pulse.team.status.v1', active_context: {} },
  });
  assert.equal(status.isError, undefined, JSON.stringify(status));
  const payload = JSON.parse(status.content[0].text);
  assert.equal(payload.name, 'pulse_team_status');
  assert.equal(payload.host, 'codex');

  console.log('[pulse] packed Codex + Claude Team packaging contract passed: exact signed Commons project + Desk + read-only Commons registry + Airlock-only publication flag; ready=true; fallback=false');
} finally {
  await client?.close();
  process.chdir(originalCwd);
  rmSync(root, { recursive: true, force: true });
}
