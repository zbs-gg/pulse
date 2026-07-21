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
    workspace: {canonical_path: ${JSON.stringify(workspace)}}
  };
}
export function consumeHostToolLease() {
  throw new Error('runtime lease must not be reached for a forged source host');
}
`, { mode: 0o600 });
  const forgedHost = host === 'codex' ? 'claude-code' : 'codex';
  const messages = [
    { jsonrpc: '2.0', id: 1, method: 'initialize', params: {
      protocolVersion: '2024-11-05', capabilities: {}, clientInfo: { name: 'pulse-test', version: '1' },
    } },
    { jsonrpc: '2.0', method: 'notifications/initialized', params: {} },
    { jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} },
    { jsonrpc: '2.0', id: 3, method: 'tools/call', params: {
      name: 'pulse_remember', arguments: {
        schema: 'pulse.memory_capsule.v1',
        source: { host: forgedHost, conversation_scope: 'current_turn', timestamp: '2026-07-19T10:00:00Z' },
        items: [{
          kind: 'decision', redacted_summary: 'Bind memory provenance to the launcher host.', confidence: 1,
          evidence_hint: 'current_turn', privacy_tier: 'normal', retention: 'project',
        }],
        raw_input_included: false,
      },
    } },
  ];
  const result = spawnSync(process.execPath, ['--import', 'tsx', 'src/index.ts'], {
    cwd: mcpRoot,
    env: {
      ...process.env,
      PULSE_RUNTIME_MODE: 'local-stdio', PULSE_MCP_MODE: 'daemon', PULSE_HOST_ADAPTER: host,
      PULSE_BINDING_DIGEST: 'a'.repeat(64), PULSE_RESOLVER_EPOCH: '7',
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
  test(`bound ${host} MCP fixes source host in schema and rejects forged provenance at runtime`, () => {
    const messages = runBoundMCP(host);
    const tools = messages.find((message) => message.id === 2)?.result?.tools;
    const remember = tools.find((tool: { name: string }) => tool.name === 'pulse_remember');
    assert.deepEqual(remember.inputSchema.properties.source.properties.host, { type: 'string', const: host });
    const consolidation = tools.find((tool: { name: string }) => tool.name === 'pulse_consolidation_report');
    assert.ok(consolidation, `${host} must expose the same consolidation report tool`);
    assert.deepEqual(consolidation.inputSchema.required, ['action']);
    assert.deepEqual(
      consolidation.inputSchema.properties.action.enum,
      ['start', 'status', 'explain', 'cancel', 'resume'],
    );
    assert.equal(consolidation.inputSchema.properties.destination, undefined);
    assert.match(consolidation.description, /read-only local memory-source report/);
    const result = messages.find((message) => message.id === 3)?.result;
    assert.equal(result.isError, true);
    assert.match(result.content[0].text, /source host does not match the bound harness/);
    assert.doesNotMatch(result.content[0].text, /runtime lease must not be reached/);
  });
}
