import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, realpathSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import test from 'node:test';

const mcpRoot = resolve(fileURLToPath(new URL('.', import.meta.url)), '..');

function runUnassignedMCP(inspectedReason: string, host = 'codex') {
  const workspace = realpathSync(mcpRoot);
  const fixture = realpathSync(mkdtempSync(join(tmpdir(), 'pulse-unassigned-mcp.')));
  const authorityPath = join(fixture, 'authority.mjs');
  writeFileSync(authorityPath, `
export function inspectProductWorkspaceBinding() {
  return {status: 'unassigned', reason: ${JSON.stringify(inspectedReason)}, workspace: {canonical_path: ${JSON.stringify(workspace)}}};
}
export function stageUnassignedProductCandidate(host, input, idempotencyKey) {
  return {
    schema: 'pulse.unassigned_stage_receipt.v1', status: 'staged', destination: 'unassigned_inbox',
    receipts: [{
      receipt_id: 'unassigned_receipt_${'a'.repeat(32)}', item_id: 'unassigned_${'b'.repeat(32)}',
      content_digest: '${'c'.repeat(64)}', action: 'stage', status: 'staged',
      destination: 'unassigned_inbox', created_at: '2026-07-17T10:00:00Z'
    }]
  };
}
`, { mode: 0o600 });
  const messages = [
    { jsonrpc: '2.0', id: 1, method: 'initialize', params: {
      protocolVersion: '2024-11-05', capabilities: {}, clientInfo: { name: 'pulse-test', version: '1' },
    } },
    { jsonrpc: '2.0', method: 'notifications/initialized', params: {} },
    { jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} },
    { jsonrpc: '2.0', id: 3, method: 'tools/call', params: {
      name: 'pulse_remember', arguments: {
        schema: 'pulse.memory_capsule.v1',
        source: { host, conversation_scope: 'current_turn', timestamp: '2026-07-17T10:00:00Z' },
        items: [{
          kind: 'decision', redacted_summary: 'Keep this local until a project is chosen.', confidence: 1,
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
      PULSE_PRODUCT_UNASSIGNED: 'binding_missing', PULSE_HOST_WORKSPACE: workspace,
      PULSE_HOST_AUTHORITY_MODULE: pathToFileURL(authorityPath).href,
      PULSE_HOST_RUNTIME_MODULE: pathToFileURL(authorityPath).href,
    },
    input: `${messages.map((message) => JSON.stringify(message)).join('\n')}\n`,
    encoding: 'utf8', timeout: 15_000,
  });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim().split(/\r?\n/).filter(Boolean).map((line) => JSON.parse(line));
}

for (const host of ['claude-code', 'cursor', 'codex']) {
  test(`unassigned ${host} MCP exposes host-bound remember and stages through its runtime`, () => {
    const messages = runUnassignedMCP('binding_missing', host);
    const tools = messages.find((message) => message.id === 2)?.result?.tools;
    assert.deepEqual(tools.map((tool: { name: string }) => tool.name), ['pulse_remember']);
    assert.deepEqual(tools[0].inputSchema.properties.source.properties.host, { type: 'string', const: host });
    assert.deepEqual(tools[0].outputSchema.required, ['schema', 'status', 'destination', 'receipts']);
    const stagedText = messages.find((message) => message.id === 3)?.result?.content?.[0]?.text;
    assert.doesNotMatch(stagedText, /^Tool error:/);
    const staged = JSON.parse(stagedText);
    assert.equal(staged.destination, 'unassigned_inbox');
    assert.equal(staged.receipts.length, 1);
    assert.equal(staged.receipts[0].status, 'staged');
  });
}

test('unassigned product MCP fails closed when the inspected binding state changes', () => {
  const messages = runUnassignedMCP('binding_ambiguous');
  const result = messages.find((message) => message.id === 3)?.result;
  assert.equal(result.isError, true);
  assert.match(result.content[0].text, /unassigned workspace state changed/);
});
