import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';

import { Client } from '@modelcontextprotocol/client';
import { StdioClientTransport } from '@modelcontextprotocol/client/stdio';

const ENTRYPOINT = new URL('./index.ts', import.meta.url);

async function connectStdio(mode: 'legacy' | 'modern', dataDir: string) {
	const client = new Client(
		{ name: `pulse-${mode}-stdio-test`, version: '0.0.0' },
		mode === 'modern'
			? { versionNegotiation: { mode: { pin: '2026-07-28' } } }
			: undefined,
	);
	const transport = new StdioClientTransport({
		command: process.execPath,
		args: ['--import', 'tsx', ENTRYPOINT.pathname],
		env: {
			...process.env,
			PULSE_DATA_DIR: dataDir,
			PULSE_MCP_MODE: 'standalone',
			PULSE_RUNTIME_MODE: 'local-stdio',
		},
		stderr: 'pipe',
	});
	await client.connect(transport);
	return { client, transport };
}

for (const mode of ['legacy', 'modern'] as const) {
	test(`stdio serves ${mode === 'modern' ? '2026-07-28' : 'older'} clients from the same entrypoint`, async () => {
		const dataDir = mkdtempSync(join(tmpdir(), `pulse-${mode}-stdio-`));
		try {
			const { client } = await connectStdio(mode, dataDir);
			assert.equal(client.getProtocolEra(), mode);
			if (mode === 'modern') assert.equal(client.getNegotiatedProtocolVersion(), '2026-07-28');
			const tools = await client.listTools();
			assert.ok(tools.tools.some((tool) => tool.name === 'pulse_context_query'));
			await client.close();
		} finally {
			rmSync(dataDir, { recursive: true, force: true });
		}
	});
}
