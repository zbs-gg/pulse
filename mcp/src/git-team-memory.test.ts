import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import test from 'node:test';

import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';

test('Personal product MCP keeps Git Team Memory unavailable until governed export exists', async () => {
  const directory = mkdtempSync(join(tmpdir(), 'pulse-git-team-memory-mcp-'));
  const authorityPath = join(directory, 'authority.mjs');
  const workspace = process.cwd();
  writeFileSync(authorityPath, `
    export function resolveProductWorkspaceBinding() {
      return { binding_digest: '${'a'.repeat(64)}', resolver_epoch: 7, workspace: { canonical_path: ${JSON.stringify(workspace)}, repository_id: 'repository_test' } };
    }
    export async function callBoundLocalProductTool(_resolved, host, name, input) {
      return { schema: 'pulse.test_local_product_tool.v1', host, name, input };
    }
  `, { mode: 0o600 });
  const keys = [
    'PULSE_HOST_ADAPTER', 'PULSE_PRODUCT_BINDING_MODE', 'PULSE_BINDING_DIGEST',
    'PULSE_RESOLVER_EPOCH', 'PULSE_REPOSITORY_ID', 'PULSE_HOST_WORKSPACE', 'PULSE_HOST_AUTHORITY_MODULE',
    'PULSE_HOST_RUNTIME_MODULE', 'PULSE_RUNTIME_MODE', 'PULSE_MCP_MODE', 'PULSE_DATA_DIR',
  ];
  const previous = Object.fromEntries(keys.map((key) => [key, process.env[key]]));
  Object.assign(process.env, {
    PULSE_HOST_ADAPTER: 'codex',
    PULSE_PRODUCT_BINDING_MODE: 'personal',
    PULSE_BINDING_DIGEST: 'a'.repeat(64), PULSE_RESOLVER_EPOCH: '7',
    PULSE_REPOSITORY_ID: 'repository_test',
    PULSE_HOST_WORKSPACE: workspace,
    PULSE_HOST_AUTHORITY_MODULE: pathToFileURL(authorityPath).href,
    PULSE_HOST_RUNTIME_MODULE: pathToFileURL(authorityPath).href,
    PULSE_RUNTIME_MODE: 'local-stdio', PULSE_MCP_MODE: 'daemon', PULSE_DATA_DIR: directory,
  });
  let client: Client | undefined;
  try {
    const moduleURL = new URL(`./index.ts?git-team-memory=${Date.now()}`, import.meta.url).href;
    const pulse = await import(moduleURL) as typeof import('./index.js');
    const server = pulse.createPulseMcpServer();
    const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair();
    client = new Client({ name: 'git-team-memory-test', version: '0.0.0' });
    await Promise.all([server.connect(serverTransport), client.connect(clientTransport)]);
    const listed = await client.listTools();
    const names = listed.tools.map(({ name }) => name);
    const unavailable = new Set([
      'pulse_source_register', 'pulse_source_window', 'pulse_source_status',
      'pulse_shared_stage', 'pulse_shared_inspect', 'pulse_shared_edit',
      'pulse_shared_reject', 'pulse_shared_cards',
      'pulse_shared_publish',
    ]);
    for (const name of unavailable) assert.equal(names.includes(name), false, `${name} leaked`);
    assert.ok(names.includes('pulse_remember'));
    assert.equal(names.some((name) => name.startsWith('pulse_team_')), false);
    const unavailableResult = await client.callTool({
      name: 'pulse_shared_publish', arguments: {
        approval_lease_id: 'lease_unavailable', approver_label: 'Reviewer',
      },
    });
    assert.equal(unavailableResult.isError, true);
    assert.match(
      unavailableResult.content[0]?.type === 'text' ? unavailableResult.content[0].text : '',
      /governed Git export|Unknown tool/i,
    );
  } finally {
    await client?.close();
    for (const key of keys) {
      if (previous[key] === undefined) delete process.env[key];
      else process.env[key] = previous[key];
    }
  }
});
