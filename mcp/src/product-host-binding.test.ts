import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, realpathSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import test from 'node:test';

const mcpRoot = resolve(fileURLToPath(new URL('.', import.meta.url)), '..');

function runBoundMCP(host: 'claude-code' | 'cursor' | 'codex') {
  const workspace = realpathSync(mcpRoot);
  const fixture = realpathSync(mkdtempSync(join(tmpdir(), 'pulse-bound-host-mcp.')));
  const authorityPath = join(fixture, 'authority.mjs');
  writeFileSync(authorityPath, `
export function resolveProductWorkspaceBinding() {
  return {
    binding_digest: '${'a'.repeat(64)}', resolver_epoch: 7,
    workspace: {canonical_path: ${JSON.stringify(workspace)}, repository_id: 'repository_test'}
  };
}
export function consumeHostToolLease() {
  throw new Error('runtime lease must not be reached for a forged source host');
}
`, { mode: 0o600 });
  const messages = [
    { jsonrpc: '2.0', id: 1, method: 'initialize', params: {
      protocolVersion: '2024-11-05', capabilities: {}, clientInfo: { name: 'pulse-test', version: '1' },
    } },
    { jsonrpc: '2.0', method: 'notifications/initialized', params: {} },
    { jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} },
    { jsonrpc: '2.0', id: 3, method: 'tools/call', params: {
      name: 'pulse_memory', arguments: {
        items: [{ kind: 'decision', scope: 'project', summary: 'Bind memory provenance to the launcher host.' }],
        source: { host: host === 'codex' ? 'claude-code' : 'codex' },
      },
    } },
    { jsonrpc: '2.0', id: 4, method: 'tools/call', params: {
      name: 'pulse_memory', arguments: { query: 'What durable rule applies here?' },
    } },
  ];
  const result = spawnSync(process.execPath, ['--import', 'tsx', 'src/index.ts'], {
    cwd: mcpRoot,
    env: {
      ...process.env,
      PULSE_RUNTIME_MODE: 'local-stdio', PULSE_MCP_MODE: 'daemon', PULSE_HOST_ADAPTER: host,
      PULSE_BINDING_DIGEST: 'a'.repeat(64), PULSE_RESOLVER_EPOCH: '7',
      PULSE_REPOSITORY_ID: 'repository_test',
      PULSE_HOST_WORKSPACE: workspace,
      PULSE_HOST_AUTHORITY_MODULE: pathToFileURL(authorityPath).href,
      PULSE_HOST_RUNTIME_MODULE: pathToFileURL(authorityPath).href,
    },
    input: `${messages.map((message) => JSON.stringify(message)).join('\n')}\n`,
    encoding: 'utf8', timeout: 15_000,
  });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim().split(/\r?\n/).filter(Boolean).map((line) => JSON.parse(line));
}

for (const host of ['claude-code', 'cursor', 'codex'] as const) {
  test(`bound ${host} MCP exposes only compact pulse_memory and rejects caller provenance`, () => {
    const messages = runBoundMCP(host);
    const tools = messages.find((message) => message.id === 2)?.result?.tools;
    assert.deepEqual(tools.map((tool: { name: string }) => tool.name), ['pulse_memory']);
    const memory = tools[0];
    const writeSchema = memory.inputSchema;
    if (host === 'cursor') {
      assert.equal(memory.inputSchema.type, 'object');
      assert.equal(memory.inputSchema.oneOf.length, 2);
      assert.deepEqual(memory.inputSchema.oneOf[0].required, ['query']);
      assert.equal(memory.inputSchema.properties.query.maxLength, 400);
      assert.equal(memory.inputSchema.additionalProperties, false);
    }
    if (host === 'cursor') assert.deepEqual(writeSchema.oneOf[1].required, ['items']);
    else assert.deepEqual(writeSchema.required, ['items']);
    assert.equal(writeSchema.properties.items.maxItems, 3);
    const [durableItem, emotionalItem] = writeSchema.properties.items.items.oneOf;
    assert.deepEqual(durableItem.properties.kind.enum, [
      'decision', 'preference', 'open_loop', 'project_state', 'correction',
    ]);
    assert.equal(durableItem.properties.summary.maxLength, 400);
    assert.equal(durableItem.properties.emotion, undefined);
    assert.equal(emotionalItem.properties.kind.const, 'emotion');
    assert.equal(emotionalItem.properties.summary.maxLength, 400);
    assert.ok(emotionalItem.required.includes('emotion'));
    assert.equal(memory.outputSchema, undefined);
    const result = messages.find((message) => message.id === 3)?.result;
    assert.equal(result.isError, true);
    assert.match(result.content[0].text, /pulse_memory requires 1\.\.3 items/);
    assert.doesNotMatch(result.content[0].text, /runtime lease must not be reached/);
    const queryResult = messages.find((message) => message.id === 4)?.result;
    assert.equal(queryResult.isError, true);
    if (host === 'cursor') {
      assert.match(queryResult.content[0].text, /runtime lease must not be reached/);
    } else {
      assert.match(queryResult.content[0].text, /pulse_memory requires 1\.\.3 items/);
      assert.doesNotMatch(queryResult.content[0].text, /runtime lease must not be reached/);
    }
  });
}
