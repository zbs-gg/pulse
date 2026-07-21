import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import test from 'node:test';

import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';

test('installed Team MCP keeps Desk tools and proxies only read-only Commons tools', async () => {
  const directory = mkdtempSync(join(tmpdir(), 'pulse-product-team-proxy-'));
  const authorityPath = join(directory, 'authority.mjs');
  const workspace = process.cwd();
  writeFileSync(authorityPath, `
    export function resolveProductWorkspaceBinding() {
      return { binding_digest: '${'a'.repeat(64)}', resolver_epoch: 7, workspace: { canonical_path: ${JSON.stringify(workspace)} } };
    }
    export async function callBoundTeamTool(_resolved, host, name, input) {
      if (input?.test_error === 'valid' || input?.test_error === 'invalid_contract') {
        const domainCode = input.test_error === 'valid' ? 'policy_denied' : 'invalid_team_contract';
        const error = new Error('closed Team domain error');
        error.name = 'TeamRemoteDomainError';
        error.code = 'domain_error';
        error.domainCode = domainCode;
        error.toolResult = {
          isError: true,
          content: [{ type: 'text', text: JSON.stringify({ error: domainCode, fallback: false }) }],
        };
        throw error;
      }
      if (input?.test_error === 'forged') {
        const error = new Error('forged Team domain error');
        error.name = 'TeamRemoteDomainError';
        error.code = 'domain_error';
        error.domainCode = 'secret_remote_failure';
        error.toolResult = {
          isError: true,
          content: [{ type: 'text', text: JSON.stringify({ error: 'secret_remote_failure', fallback: false }) }],
        };
        throw error;
      }
      return { schema: 'pulse.test_team_proxy.v1', host, name, input, fallback: false };
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
    PULSE_PRODUCT_BINDING_MODE: 'team',
    PULSE_BINDING_DIGEST: 'a'.repeat(64),
    PULSE_RESOLVER_EPOCH: '7',
    PULSE_HOST_WORKSPACE: workspace,
    PULSE_HOST_AUTHORITY_MODULE: pathToFileURL(authorityPath).href,
    PULSE_HOST_RUNTIME_MODULE: pathToFileURL(authorityPath).href,
    PULSE_RUNTIME_MODE: 'local-stdio',
    PULSE_MCP_MODE: 'daemon',
    PULSE_DATA_DIR: directory,
  });
  let client: Client | undefined;
  try {
    const moduleURL = new URL(`./index.ts?product-team-proxy=${Date.now()}`, import.meta.url).href;
    const pulse = await import(moduleURL) as typeof import('./index.js');
    const server = pulse.createPulseMcpServer();
    const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair();
    client = new Client({ name: 'product-team-proxy-test', version: '0.0.0' });
    await Promise.all([server.connect(serverTransport), client.connect(clientTransport)]);
    const tools = await client.listTools();
    const names = tools.tools.map(({ name }) => name);
    assert.ok(names.includes('pulse_remember'));
    assert.ok(names.includes('pulse_team_resume'));
    assert.equal(names.includes('pulse_team_audit'), false);
    assert.equal(names.includes('pulse_team_remember'), false);
    assert.equal(names.includes('pulse_team_delete'), false);

    const result = await client.callTool({
      name: 'pulse_team_status',
      arguments: { schema: 'pulse.team.status.v1', active_context: {} },
    });
    assert.equal(result.isError, undefined);
    const payload = JSON.parse(result.content[0]?.type === 'text' ? result.content[0].text : '{}');
    assert.equal(payload.name, 'pulse_team_status');
    assert.equal(payload.host, 'codex');

    const preservedDomainError = await client.callTool({
      name: 'pulse_team_status',
      arguments: { schema: 'pulse.team.status.v1', active_context: {}, test_error: 'valid' },
    });
    assert.equal(preservedDomainError.isError, true);
    assert.deepEqual(
      JSON.parse(preservedDomainError.content[0]?.type === 'text'
        ? preservedDomainError.content[0].text : '{}'),
      { error: 'policy_denied', fallback: false },
    );

    const preservedContractError = await client.callTool({
      name: 'pulse_team_status',
      arguments: {
        schema: 'pulse.team.status.v1', active_context: {}, test_error: 'invalid_contract',
      },
    });
    assert.equal(preservedContractError.isError, true);
    assert.deepEqual(
      JSON.parse(preservedContractError.content[0]?.type === 'text'
        ? preservedContractError.content[0].text : '{}'),
      { error: 'invalid_team_contract', fallback: false },
    );

    const forgedDomainError = await client.callTool({
      name: 'pulse_team_status',
      arguments: { schema: 'pulse.team.status.v1', active_context: {}, test_error: 'forged' },
    });
    assert.equal(forgedDomainError.isError, true);
    assert.match(
      forgedDomainError.content[0]?.type === 'text' ? forgedDomainError.content[0].text : '',
      /Tool error: forged Team domain error/,
    );

    const forbidden = await client.callTool({ name: 'pulse_team_remember', arguments: {} });
    assert.equal(forbidden.isError, true);
    const audit = await client.callTool({
      name: 'pulse_team_audit',
      arguments: { schema: 'pulse.team.audit.v1', active_context: {} },
    });
    assert.equal(audit.isError, true);
  } finally {
    await client?.close();
    for (const key of keys) {
      if (previous[key] === undefined) delete process.env[key];
      else process.env[key] = previous[key];
    }
  }
});
