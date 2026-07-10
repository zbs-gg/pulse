import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { existsSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';

import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';

const ENTRYPOINT = new URL('./index.ts', import.meta.url);

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

async function startTeamPreflight(dataDir: string) {
  const child = spawn(
    process.execPath,
    ['--import', 'tsx', ENTRYPOINT.pathname, '--http', '--port', '0'],
    {
      env: {
        ...process.env,
        PULSE_RUNTIME_MODE: 'team-remote',
        PULSE_MCP_MODE: 'daemon',
        PULSE_BASE_URL: 'http://127.0.0.1:1',
        PULSE_DATA_DIR: dataDir,
        PULSE_REMOTE_PUBLIC_BASE_URL: 'https://pulse.example.com',
        PULSE_REMOTE_AUTH_ISSUER: 'https://auth.example.com',
        PULSE_REMOTE_BEARER: '',
        PULSE_REMOTE_OAUTH_DEV: '',
        PULSE_REMOTE_AUTH_PROXY_MODE: '',
        PULSE_REMOTE_TRUST_AUTH_HEADER: '',
        PULSE_REMOTE_ALLOW_UNAUTHENTICATED: '',
        PULSE_TEAM_REMOTE_ACTIVATED: '',
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    },
  );
  let output = '';
  child.stdout.on('data', (chunk) => {
    output += chunk.toString();
  });
  child.stderr.on('data', (chunk) => {
    output += chunk.toString();
  });
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    const match = output.match(/Streamable HTTP listening on (http:\/\/127\.0\.0\.1:\d+\/mcp)/);
    if (match) {
      return {
        url: match[1],
        stop: async () => {
          if (child.exitCode === null) {
            child.kill('SIGTERM');
            await once(child, 'exit');
          }
        },
      };
    }
    if (child.exitCode !== null) {
      throw new Error(`pulse-mcp exited early (${child.exitCode}): ${output}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  child.kill('SIGTERM');
  throw new Error(`pulse-mcp did not print listening URL:\n${output}`);
}

function toolJSON(result: { content?: Array<{ type: string; text?: string }> }) {
  assert.equal(result.content?.[0]?.type, 'text');
  return JSON.parse(result.content?.[0]?.text ?? 'null');
}

test('team preflight exposes only exact team descriptors and stable stubs', async () => {
  const dataDir = mkdtempSync(join(tmpdir(), 'pulse-team-u1-'));
  const server = await startTeamPreflight(dataDir);
  let client: Client | undefined;
  try {
    client = new Client({ name: 'pulse-team-u1-test', version: '0.0.0' });
    const transport = new StreamableHTTPClientTransport(new URL(server.url));
    await client.connect(transport);

    const listed = await client.listTools();
    assert.deepEqual(
      listed.tools.map((tool) => tool.name),
      EXPECTED_TEAM_TOOLS.map(([name]) => name),
    );
    assert.ok(listed.tools.every((tool) => tool.inputSchema.type === 'object'));
    for (const [name, contract] of EXPECTED_TEAM_TOOLS) {
      const descriptor = listed.tools.find((tool) => tool.name === name);
      assert.ok(descriptor?.description?.includes(contract), `${name} must declare ${contract}`);
    }

    const status = await client.callTool({
      name: 'pulse_team_status',
      arguments: {},
    });
    assert.equal(status.isError, true);
    assert.deepEqual(toolJSON(status), {
      schema: 'pulse.team.not_ready.v1',
      error: 'team_remote_not_ready',
      mode: 'team-remote',
      tool: 'pulse_team_status',
      fallback: false,
    });

    const legacy = await client.callTool({ name: 'pulse_status', arguments: {} });
    assert.equal(legacy.isError, true);
    assert.match(legacy.content[0]?.type === 'text' ? legacy.content[0].text : '', /unknown team tool/i);
    assert.equal(existsSync(join(dataDir, 'standalone', 'store.json')), false);
  } finally {
    await client?.close().catch(() => undefined);
    await server.stop();
    rmSync(dataDir, { recursive: true, force: true });
  }
});
