import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import test from 'node:test';

import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';

test('Codex product MCP exposes only closed local Git Team Memory tools and routes through host authority', async () => {
  const directory = mkdtempSync(join(tmpdir(), 'pulse-git-team-memory-mcp-'));
  const authorityPath = join(directory, 'authority.mjs');
  const workspace = process.cwd();
  writeFileSync(authorityPath, `
    export function resolveProductWorkspaceBinding() {
      return { binding_digest: '${'a'.repeat(64)}', resolver_epoch: 7, workspace: { canonical_path: ${JSON.stringify(workspace)} } };
    }
    export async function callBoundLocalProductTool(_resolved, host, name, input) {
      return { schema: 'pulse.test_local_product_tool.v1', host, name, input };
    }
  `, { mode: 0o600 });
  const keys = [
    'PULSE_HOST_ADAPTER', 'PULSE_PRODUCT_BINDING_MODE', 'PULSE_BINDING_DIGEST',
    'PULSE_RESOLVER_EPOCH', 'PULSE_HOST_WORKSPACE', 'PULSE_HOST_AUTHORITY_MODULE',
    'PULSE_HOST_RUNTIME_MODULE', 'PULSE_RUNTIME_MODE', 'PULSE_MCP_MODE', 'PULSE_DATA_DIR',
  ];
  const previous = Object.fromEntries(keys.map((key) => [key, process.env[key]]));
  Object.assign(process.env, {
    PULSE_HOST_ADAPTER: 'codex',
    PULSE_PRODUCT_BINDING_MODE: 'personal',
    PULSE_BINDING_DIGEST: 'a'.repeat(64), PULSE_RESOLVER_EPOCH: '7',
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
    const expected = new Set([
      'pulse_source_register', 'pulse_source_window', 'pulse_source_status',
      'pulse_shared_stage', 'pulse_shared_inspect', 'pulse_shared_edit',
      'pulse_shared_reject', 'pulse_shared_cards',
    ]);
    for (const name of expected) assert.ok(names.includes(name), `${name} missing`);
    assert.equal(names.some((name) => name.startsWith('pulse_team_')), false);
    for (const tool of listed.tools.filter(({ name }) => expected.has(name))) {
      const properties = Object.keys((tool.inputSchema.properties ?? {}) as Record<string, unknown>);
      assert.equal(properties.some((field) => [
        'portable_project_id', 'repository_id', 'binding_digest', 'host', 'task_id', 'approval',
      ].includes(field)), false, tool.name);
      assert.equal(tool.inputSchema.additionalProperties, false, tool.name);
    }
    const result = await client.callTool({
      name: 'pulse_source_window',
      arguments: { locator: 'notes/team.md', cursor: 0, max_bytes: 512 },
    });
    assert.equal(result.isError, undefined);
    const payload = JSON.parse(result.content[0]?.type === 'text' ? result.content[0].text : '{}');
    assert.equal(payload.name, 'pulse_source_window');
    assert.equal(payload.host, 'codex');
    assert.deepEqual(payload.input, { locator: 'notes/team.md', cursor: 0, max_bytes: 512 });
  } finally {
    await client?.close();
    for (const key of keys) {
      if (previous[key] === undefined) delete process.env[key];
      else process.env[key] = previous[key];
    }
  }
});
