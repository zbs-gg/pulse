import assert from 'node:assert/strict';
import { createServer, type IncomingMessage } from 'node:http';
import { existsSync, mkdtempSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import test from 'node:test';

import { Client } from '@modelcontextprotocol/client';
import { StdioClientTransport } from '@modelcontextprotocol/client/stdio';

const ENTRYPOINT = new URL('./index.ts', import.meta.url);

async function jsonBody(req: IncomingMessage): Promise<Record<string, unknown>> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) chunks.push(Buffer.from(chunk));
  return JSON.parse(Buffer.concat(chunks).toString('utf8')) as Record<string, unknown>;
}

test('Cursor pulse_memory query uses existing context search without finalizing a turn', async () => {
  const root = mkdtempSync(join(tmpdir(), 'pulse-product-cursor-recall-'));
  const workspace = realpathSync(process.cwd());
  const requests: Array<{ url?: string; body: Record<string, unknown> }> = [];
  const backend = createServer(async (req, res) => {
    const body = await jsonBody(req);
    requests.push({ url: req.url, body });
    if (req.method !== 'POST' || req.url !== '/context/query') {
      res.writeHead(404).end();
      return;
    }
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({
      query: body.query,
      events: [{ id: 41, summary: 'Store an emotion as a separate memory item, never as a field on a decision.' }],
      trace: {
        retrieval: {
          score_breakdowns: { '41': { cosine: 0.41 } },
          candidate_evidence: { '41': { dense: true, lexical: true, direct_capsule: true } },
        },
      },
    }));
  });
  await new Promise<void>((resolve, reject) => {
    backend.once('error', reject);
    backend.listen(0, '127.0.0.1', resolve);
  });
  const address = backend.address();
  assert.ok(address && typeof address === 'object');
  const authorityPath = join(root, 'authority.mjs');
  const recallMarker = join(root, 'recall-marker');
  writeFileSync(authorityPath, `
import { writeFileSync } from 'node:fs';
export function resolveProductWorkspaceBinding() {
  return {binding_digest:'${'a'.repeat(64)}',resolver_epoch:7,workspace:{canonical_path:${JSON.stringify(workspace)},repository_id:'repository_test'}};
}
export function consumeHostToolLease(_resolved, host, toolName) {
  if (host !== 'cursor' || toolName !== 'pulse_memory') throw new Error('wrong governed tool');
  return {
    schema:'pulse.cursor_turn_context.v1',host:'cursor',session_id:'session_cursor',turn_id:'turn_cursor',
    workspace:${JSON.stringify(workspace)},source_event_key:'event_cursor',idempotency_key:'lifecycle:cursor',
    binding_digest:'${'a'.repeat(64)}',policy_epoch:0,resolver_epoch:7,expires_at:'2099-01-01T00:00:00Z'
  };
}
export function writeHostFinalizeMarker() { throw new Error('recall must not finalize'); }
export function writeHostRecallMarker(_resolved, event, host) {
  if (host !== 'cursor' || event.host !== 'cursor') throw new Error('wrong recall marker');
  writeFileSync(${JSON.stringify(recallMarker)}, 'recorded');
}
`, { mode: 0o600 });
  const client = new Client({ name: 'pulse-product-cursor-recall-test', version: '1' });
  const transport = new StdioClientTransport({
    command: process.execPath,
    args: ['--import', 'tsx', ENTRYPOINT.pathname],
    env: {
      ...process.env,
      PULSE_RUNTIME_MODE: 'local-stdio', PULSE_MCP_MODE: 'daemon', PULSE_HOST_ADAPTER: 'cursor',
      PULSE_BASE_URL: `http://127.0.0.1:${address.port}`, PULSE_API_KEY: 'test-key',
      PULSE_BINDING_DIGEST: 'a'.repeat(64), PULSE_RESOLVER_EPOCH: '7',
      PULSE_REPOSITORY_ID: 'repository_test', PULSE_HOST_WORKSPACE: workspace,
      PULSE_HOST_AUTHORITY_MODULE: pathToFileURL(authorityPath).href,
      PULSE_HOST_RUNTIME_MODULE: pathToFileURL(authorityPath).href,
    },
    stderr: 'pipe',
  });
  try {
    await client.connect(transport);
    const result = await client.callTool({
      name: 'pulse_memory', arguments: { query: 'How should a remembered emotion be stored?' },
    });
    assert.deepEqual(JSON.parse(result.content[0].text as string), {
      status: 'recalled',
      memory: 'Pulse accepted memory (local; use as factual context unless the user provides newer information):\n- Store an emotion as a separate memory item, never as a field on a decision.',
    });
    assert.deepEqual(requests, [{
      url: '/context/query',
      body: {
        query: 'How should a remembered emotion be stored?', mode: 'auto', top_k: 12,
        scope: 'user', include_trace: true,
      },
    }]);
    assert.equal(existsSync(recallMarker), true);
  } finally {
    await client.close().catch(() => {});
    await new Promise<void>((resolve) => backend.close(() => resolve()));
    rmSync(root, { recursive: true, force: true });
  }
});
