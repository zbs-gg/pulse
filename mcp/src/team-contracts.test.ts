import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { test } from 'node:test';

import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPServerTransport } from '@modelcontextprotocol/sdk/server/streamableHttp.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';

import { createPulseMcpServer } from './index.js';
import type { TeamPrincipalContext } from './principal-context.js';

import {
  isTeamToolName,
  requiredTeamCapabilities,
  TEAM_BASELINE_CAPABILITY,
  TEAM_TOOL_DESCRIPTORS,
  teamNotReadyResult,
} from './team-contracts.js';

const EXPECTED_TEAM_TOOLS = [
  ['pulse_team_status', 'pulse.team.status.v1'],
  ['pulse_team_remember', 'pulse.team.memory.v1'],
  ['pulse_team_graph_delta', 'pulse.team.graph_delta.v1'],
  ['pulse_team_recall', 'pulse.team.recall.v1'],
  ['pulse_team_context_query', 'pulse.team.context.v1'],
  ['pulse_team_resume', 'pulse.team.resume.v1'],
  ['pulse_team_inspect', 'pulse.team.inspect.v1'],
  ['pulse_team_audit', 'pulse.team.audit.v1'],
  ['pulse_team_delete', 'pulse.team.delete.v1'],
  ['pulse_team_delete_status', 'pulse.team.delete_status.v1'],
] as const;

test('team capabilities are derived from the allowlisted MCP operation', () => {
  assert.deepEqual(requiredTeamCapabilities({ method: 'initialize' }), [TEAM_BASELINE_CAPABILITY]);
  assert.deepEqual(requiredTeamCapabilities({ method: 'tools/list' }), [TEAM_BASELINE_CAPABILITY]);
  assert.deepEqual(requiredTeamCapabilities({
    method: 'tools/call', params: { name: 'pulse_team_status' },
  }), ['pulse:connect', 'pulse:status']);
  assert.deepEqual(requiredTeamCapabilities({
    method: 'tools/call', params: { name: 'pulse_team_remember' },
  }), ['pulse:connect', 'pulse:write']);
  assert.deepEqual(requiredTeamCapabilities({
    method: 'tools/call', params: { name: 'pulse_team_recall' },
  }), ['pulse:connect', 'pulse:read']);
  assert.deepEqual(requiredTeamCapabilities({
    method: 'tools/call', params: { name: 'pulse_team_audit' },
  }), ['pulse:audit', 'pulse:connect']);
  assert.deepEqual(requiredTeamCapabilities({
    method: 'tools/call', params: { name: 'pulse_team_delete' },
  }), ['pulse:connect', 'pulse:delete']);
  assert.throws(
    () => requiredTeamCapabilities({ method: 'tools/call', params: { name: 'pulse_status' } }),
    /unknown team tool/i,
  );
});

function teamContext(principalId: string): Readonly<TeamPrincipalContext> {
  return Object.freeze({
    version: 'pulse.team.principal_context.v1',
    request_id: `request-${principalId}`,
    store_id: 'store_test',
    team_id: 'team_test',
    principal_id: principalId,
    principal_kind: 'agent',
    oauth_client_key: 'a'.repeat(64),
    human_principal_id: `human-${principalId}`,
    agent_binding_id: `binding-${principalId}`,
    membership_id: `membership-${principalId}`,
    membership_role: 'member',
    team_auth_epoch: 1,
    principal_auth_epoch: 1,
    binding_auth_epoch: 1,
    membership_auth_epoch: 1,
    capabilities: ['pulse:connect', 'pulse:read'],
  });
}

async function startTeamRegistryServer() {
  const seenPrincipals: string[] = [];
  const httpServer = createServer(async (req, res) => {
    const requestedPrincipal = typeof req.headers['x-test-principal'] === 'string'
      ? req.headers['x-test-principal']
      : 'missing';
    seenPrincipals.push(requestedPrincipal);
    const requestServer = createPulseMcpServer('team-remote', teamContext(requestedPrincipal));
    const transport = new StreamableHTTPServerTransport({ sessionIdGenerator: undefined });
    let cleaned = false;
    const cleanup = () => {
      if (cleaned) return;
      cleaned = true;
      void Promise.allSettled([transport.close(), requestServer.close()]);
    };
    try {
      await requestServer.connect(transport);
      res.once('close', cleanup);
      await transport.handleRequest(req, res);
    } catch {
      cleanup();
      if (!res.headersSent) res.writeHead(500);
      res.end();
    }
  });
  await new Promise<void>((resolve, reject) => {
    httpServer.once('error', reject);
    httpServer.listen(0, '127.0.0.1', () => resolve());
  });
  const address = httpServer.address();
  assert.ok(address && typeof address === 'object');
  return {
    seenPrincipals,
    url: `http://127.0.0.1:${address.port}/mcp`,
    stop: () => new Promise<void>((resolve) => httpServer.close(() => resolve())),
  };
}

function toolJSON(result: { content?: Array<{ type: string; text?: string }> }) {
  assert.equal(result.content?.[0]?.type, 'text');
  return JSON.parse(result.content?.[0]?.text ?? 'null');
}

test('team StreamableHTTP registry exposes only exact descriptors and request-local stubs', async () => {
  assert.deepEqual(
    TEAM_TOOL_DESCRIPTORS.map((tool) => tool.name),
    EXPECTED_TEAM_TOOLS.map(([name]) => name),
  );
  assert.ok(TEAM_TOOL_DESCRIPTORS.every((tool) => tool.inputSchema.type === 'object'));
  for (const [name, contract] of EXPECTED_TEAM_TOOLS) {
    const descriptor = TEAM_TOOL_DESCRIPTORS.find((tool) => tool.name === name);
    assert.ok(descriptor?.description?.includes(contract), `${name} must declare ${contract}`);
  }
  assert.equal(isTeamToolName('pulse_status'), false);
  assert.deepEqual(teamNotReadyResult('pulse_team_status'), {
    isError: true,
    content: [{
      type: 'text',
      text: JSON.stringify({
      schema: 'pulse.team.not_ready.v1',
      error: 'team_remote_not_ready',
      mode: 'team-remote',
      tool: 'pulse_team_status',
      fallback: false,
      }),
    }],
  });

  const server = await startTeamRegistryServer();
  const exercise = async (principalId: string) => {
    const client = new Client({ name: `team-${principalId}`, version: '0.0.0' });
    const transport = new StreamableHTTPClientTransport(new URL(server.url), {
      requestInit: { headers: { 'X-Test-Principal': principalId } },
    });
    await client.connect(transport);
    const tools = await client.listTools();
    assert.deepEqual(tools.tools.map(({ name }) => name), EXPECTED_TEAM_TOOLS.map(([name]) => name));
    assert.equal(tools.tools.some(({ name }) => name === 'pulse_status' || !name.startsWith('pulse_team_')), false);
    const status = await client.callTool({ name: 'pulse_team_status', arguments: {} });
    assert.deepEqual(toolJSON(status), {
      schema: 'pulse.team.not_ready.v1', error: 'team_remote_not_ready',
      mode: 'team-remote', tool: 'pulse_team_status', fallback: false,
    });
    const legacy = await client.callTool({ name: 'pulse_status', arguments: {} });
    assert.equal(legacy.isError, true);
    assert.doesNotMatch(legacy.content[0]?.type === 'text' ? legacy.content[0].text : '', /pulse_status/);
    await client.close();
  };
  try {
    await Promise.all([exercise('principal-a'), exercise('principal-b')]);
    assert.ok(server.seenPrincipals.includes('principal-a'));
    assert.ok(server.seenPrincipals.includes('principal-b'));
  } finally {
    await server.stop();
  }
});
