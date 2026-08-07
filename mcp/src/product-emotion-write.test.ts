import assert from 'node:assert/strict';
import { createServer, type IncomingMessage } from 'node:http';
import { mkdtempSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
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

test('bound product graph writes one private emotional event through the governed turn', async () => {
	const root = mkdtempSync(join(tmpdir(), 'pulse-product-emotion-'));
	const workspace = realpathSync(join(process.cwd()));
	const requests: Record<string, unknown>[] = [];
	const backend = createServer(async (req, res) => {
		if (req.method !== 'POST' || req.url !== '/turn/finalize') {
			res.writeHead(404).end();
			return;
		}
		requests.push(await jsonBody(req));
		const provenance = {
			host: 'codex', session_id: 'session:opaque', turn_id: 'turn:opaque', source_event_key: 'event:opaque',
		};
		res.writeHead(200, { 'Content-Type': 'application/json' });
		res.end(JSON.stringify({
			ledger_id: 'turn_emotion_01', status: 'candidates',
			finalize_receipt: {
				schema: 'pulse.turn_finalize_receipt.v1', receipt_id: 'receipt_finalize_01',
				ledger_id: 'turn_emotion_01', status: 'candidates', destination: 'personal',
				destination_store_id: 'store_personal_test', safe_provenance: provenance,
				policy_epoch: 0, resolver_epoch: 7, created_at: '2026-08-06T10:00:00Z',
			},
			receipts: [{
				schema: 'pulse.write_receipt.v1', receipt_id: 'receipt_item_01', ledger_id: 'turn_emotion_01',
				candidate_id: 'candidate_emotion_01', candidate_version: 1, status: 'created', destination: 'personal',
				destination_store_id: 'store_personal_test', safe_provenance: provenance,
				content_digest: 'b'.repeat(64), object_id: 'semantic_emotion_01', policy_epoch: 0,
				resolver_epoch: 7, measurement_method: 'host_structured_v1', created_at: '2026-08-06T10:00:00Z',
			}],
			event_ids: [41], event_results: [{ client_id: 'moment:1', id: 41, result: 'created' }],
			emotion_question: {
				question_id: `emotion_question:${'c'.repeat(32)}`, event_id: 41, event_client_id: 'moment:1',
				question: 'Что именно сейчас вызвало эту эмоцию?', expires_at: '2026-08-13T10:00:00Z',
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
	writeFileSync(authorityPath, `
export function resolveProductWorkspaceBinding() {
  return {binding_digest:'${'a'.repeat(64)}',resolver_epoch:7,workspace:{canonical_path:${JSON.stringify(workspace)},repository_id:'repository_test'}};
}
export function consumeHostToolLease(_resolved, host, toolName) {
  if (host !== 'codex' || toolName !== 'pulse_graph_delta') throw new Error('wrong governed tool');
  return {host:'codex',session_id:'session-real',turn_id:'turn-real',source_event_key:'event_${'d'.repeat(64)}',idempotency_key:'lifecycle:${'e'.repeat(64)}',binding_digest:'${'a'.repeat(64)}',policy_epoch:0,resolver_epoch:7};
}
export function writeHostFinalizeMarker() {}
`, { mode: 0o600 });
	const client = new Client({ name: 'pulse-product-emotion-test', version: '1' });
	const transport = new StdioClientTransport({
		command: process.execPath,
		args: ['--import', 'tsx', ENTRYPOINT.pathname],
		env: {
			...process.env,
			PULSE_RUNTIME_MODE: 'local-stdio', PULSE_MCP_MODE: 'daemon', PULSE_HOST_ADAPTER: 'codex',
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
		const tools = await client.listTools();
		assert.ok(tools.tools.some((tool) => tool.name === 'pulse_graph_delta'));
		const result = await client.callTool({
			name: 'pulse_graph_delta',
			arguments: {
				schema: 'pulse.semantic_delta.v1',
				source: { host: 'codex', conversation_scope: 'current_turn', timestamp: '2026-08-06T10:00:00Z' },
				events: [{
					client_id: 'moment:1', title: 'A tense moment', summary: 'A short description without a quote.',
					emotions: { fear: 0.8 }, emotion_derivation: 'inferred', emotion_confidence: 0.9,
					confidence: 0.9, privacy_tier: 'private',
				}],
				raw_input_included: false,
			},
		});
		assert.equal(result.structuredContent?.event_results?.[0]?.result, 'created');
		assert.equal(result.structuredContent?.emotion_question?.event_id, 41);
		assert.equal(requests.length, 1);
		const candidate = (requests[0].candidates as Array<Record<string, unknown>>)[0];
		assert.equal(candidate.kind, 'semantic_delta');
		const delta = candidate.semantic_delta as Record<string, unknown>;
		assert.equal((delta.source as Record<string, unknown>).host, 'codex');
		assert.equal((delta.source as Record<string, unknown>).session_id, 'session-real');
		assert.equal(delta.raw_input_included, false);
	} finally {
		await client.close().catch(() => {});
		await new Promise<void>((resolve) => backend.close(() => resolve()));
		rmSync(root, { recursive: true, force: true });
	}
});
