import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';

import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js';

import { StandaloneStore } from './standalone.js';

const ENTRYPOINT = new URL('./index.ts', import.meta.url);
const DEFAULT_BEARER = 'dev-token';
const UNREACHABLE_PULSE = 'http://127.0.0.1:1';

function tempDataDir(): string {
  return mkdtempSync(join(tmpdir(), 'pulse-standalone-test-'));
}

function sampleCapsule(summary: string, extra: Partial<{ kind: string; tags: string[] }> = {}) {
  return {
    schema: 'pulse.memory_capsule.v1',
    source: {
      host: 'claude-code',
      conversation_scope: 'current_turn',
      timestamp: '2026-06-10T10:00:00Z',
    },
    items: [
      {
        kind: extra.kind ?? 'decision',
        redacted_summary: summary,
        confidence: 0.9,
        evidence_hint: 'current_turn',
        privacy_tier: 'normal',
        retention: 'project',
        tags: extra.tags ?? ['standalone-test'],
      },
    ],
    raw_input_included: false,
  };
}

test('standalone store remembers, recalls, forgets, and wipes locally', () => {
  const dataDir = tempDataDir();
  try {
    const store = new StandaloneStore(dataDir);

    const saved = store.remember(sampleCapsule('Pulse MCP ships a standalone lite engine.'));
    assert.equal(saved.ok, true);
    assert.equal(saved.ids.length, 1);
    assert.match(saved.ids[0], /^pulse:\d+:0:[0-9a-f]{16}$/);
    assert.ok(existsSync(join(dataDir, 'standalone', 'store.json')));

    const recalled = store.recall({ query: 'standalone lite engine' });
    assert.equal(recalled.items.length, 1);
    assert.equal(recalled.items[0].id, saved.ids[0]);
    assert.equal(recalled.items[0].summary, 'Pulse MCP ships a standalone lite engine.');
    assert.equal(recalled.items[0].source, 'pulse');

    const miss = store.recall({ query: 'completely unrelated quasar' });
    assert.equal(miss.items.length, 0);

    store.forget(saved.ids[0]);
    assert.equal(store.recall({ query: 'standalone lite engine' }).items.length, 0);
    assert.throws(() => store.forget(saved.ids[0]), /not found/);

    store.remember(sampleCapsule('Second memory for wipe test.'));
    assert.throws(() => store.wipe('nope'), /confirm/);
    store.wipe('wipe pulse memory');
    assert.equal(existsSync(join(dataDir, 'standalone', 'store.json')), false);
    assert.equal((store.status() as { item_count: number }).item_count, 0);
  } finally {
    rmSync(dataDir, { recursive: true, force: true });
  }
});

test('standalone recall respects privacy ceiling and scope filters', () => {
  const dataDir = tempDataDir();
  try {
    const store = new StandaloneStore(dataDir);
    store.remember({
      schema: 'pulse.memory_capsule.v1',
      source: {
        host: 'claude-code',
        conversation_scope: 'current_turn',
        timestamp: '2026-06-10T10:00:00Z',
      },
      items: [
        {
          kind: 'fact',
          redacted_summary: 'Public roadmap fact about pulse.',
          confidence: 0.8,
          evidence_hint: 'current_turn',
          privacy_tier: 'normal',
          retention: 'project',
        },
        {
          kind: 'relationship_note',
          redacted_summary: 'Private note about pulse feelings.',
          confidence: 0.8,
          evidence_hint: 'current_turn',
          privacy_tier: 'private',
          retention: 'long_term',
        },
      ],
      raw_input_included: false,
    });

    const defaultCeiling = store.recall({ query: 'pulse' });
    assert.equal(defaultCeiling.items.length, 1);
    assert.equal(defaultCeiling.items[0].privacy_tier, 'normal');

    const privateCeiling = store.recall({ query: 'pulse', privacy_ceiling: 'private' });
    assert.equal(privateCeiling.items.length, 2);

    const userScope = store.recall({ query: 'pulse', privacy_ceiling: 'private', scope: 'user' });
    assert.equal(userScope.items.length, 1);
    assert.equal(userScope.items[0].retention, 'long_term');
  } finally {
    rmSync(dataDir, { recursive: true, force: true });
  }
});

test('standalone graph delta saves checkpoints that resume can replay', () => {
  const dataDir = tempDataDir();
  try {
    const store = new StandaloneStore(dataDir);
    const out = store.graphDelta({
      schema: 'pulse.semantic_delta.v1',
      source: {
        host: 'claude-code',
        conversation_scope: 'current_turn',
        timestamp: '2026-06-10T10:00:00Z',
        thread_id: 'pulse-distribution',
      },
      nodes: [
        {
          client_id: 'project:pulse',
          kind: 'project',
          canonical_name: 'Pulse',
          privacy_tier: 'normal',
        },
      ],
      continuity: {
        summary: 'Shipped standalone lite engine for zero-config installs.',
        decisions: ['MCP falls back to a local JSON store when no daemon answers.'],
        open_loops: ['Publish 0.4.0 to npm under the preview dist-tag.'],
      },
      raw_input_included: false,
    }) as Record<string, unknown>;
    assert.equal(out.ok, true);
    assert.equal(out.nodes_upserted, 1);
    assert.equal(out.checkpoint_saved, true);

    const resume = store.resume({ thread_id: 'pulse-distribution' }) as {
      schema: string;
      thread_id: string;
      resume_markdown: string;
      sections: { open_loops: string[]; suggested_next_step: string[] };
    };
    assert.equal(resume.schema, 'pulse.continuity.v1');
    assert.equal(resume.thread_id, 'pulse-distribution');
    assert.match(resume.resume_markdown, /standalone lite engine/);
    assert.match(resume.resume_markdown, /## Open loops/);
    assert.equal(
      resume.sections.suggested_next_step[0],
      'Continue with: Publish 0.4.0 to npm under the preview dist-tag.',
    );
  } finally {
    rmSync(dataDir, { recursive: true, force: true });
  }
});

async function startHttpServer(env: Record<string, string> = {}) {
  const child = spawn(
    process.execPath,
    ['--import', 'tsx', ENTRYPOINT.pathname, '--http', '--port', '0'],
    {
      env: {
        ...process.env,
        PULSE_BASE_URL: UNREACHABLE_PULSE,
        PULSE_API_KEY: 'test-key',
        PULSE_REMOTE_BEARER: DEFAULT_BEARER,
        ...env,
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    },
  );
  let output = '';
  child.stderr.on('data', (chunk) => {
    output += chunk.toString();
  });
  child.stdout.on('data', (chunk) => {
    output += chunk.toString();
  });
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    const match = output.match(/Streamable HTTP listening on (http:\/\/127\.0\.0\.1:\d+\/mcp)/);
    if (match) {
      return {
        child,
        url: match[1],
        stop: async () => {
          child.kill('SIGTERM');
          await once(child, 'exit');
        },
      };
    }
    if (child.exitCode !== null) {
      throw new Error(`pulse-mcp exited early (${child.exitCode}): ${output}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  child.kill('SIGTERM');
  throw new Error(`pulse-mcp did not print listening URL:\n${output}`);
}

async function connectedClient(serverURL: string) {
  const client = new Client({ name: 'pulse-standalone-test', version: '0.0.0' });
  const transport = new StreamableHTTPClientTransport(new URL(serverURL), {
    requestInit: {
      headers: {
        Authorization: `Bearer ${DEFAULT_BEARER}`,
      },
    },
  });
  await client.connect(transport);
  return client;
}

function toolJSON(result: { content?: Array<{ type: string; text?: string }> }) {
  assert.equal(result.content?.[0]?.type, 'text');
  return JSON.parse(result.content![0].text ?? 'null');
}

test('auto mode falls back to standalone store when no daemon is reachable', async () => {
  const dataDir = tempDataDir();
  const server = await startHttpServer({ PULSE_DATA_DIR: dataDir });
  try {
    const client = await connectedClient(server.url);

    const status = toolJSON(await client.callTool({ name: 'pulse_status', arguments: {} }));
    assert.equal(status.engine, 'standalone_lite');
    assert.equal(status.storage, 'local_json');
    assert.equal(status.storage_path, '<local>');
    assert.equal(status.backend_llm_enabled, false);
    assert.equal(status.raw_capture_enabled, false);

    const saved = toolJSON(
      await client.callTool({
        name: 'pulse_remember',
        arguments: sampleCapsule('Zero-config install works without a daemon.'),
      }),
    );
    assert.equal(saved.ok, true);
    assert.equal(saved.ids.length, 1);

    const recalled = toolJSON(
      await client.callTool({
        name: 'pulse_recall',
        arguments: { query: 'zero-config install daemon' },
      }),
    );
    assert.equal(recalled.items.length, 1);
    assert.equal(recalled.items[0].summary, 'Zero-config install works without a daemon.');

    const storeFile = join(dataDir, 'standalone', 'store.json');
    assert.ok(existsSync(storeFile));
    const raw = readFileSync(storeFile, 'utf8');
    assert.match(raw, /pulse\.standalone_store\.v1/);
    assert.doesNotMatch(raw, /test-key/);

    await client.close();
  } finally {
    await server.stop();
    rmSync(dataDir, { recursive: true, force: true });
  }
});

test('daemon mode keeps hard errors when the daemon is unreachable', async () => {
  const dataDir = tempDataDir();
  const server = await startHttpServer({
    PULSE_DATA_DIR: dataDir,
    PULSE_MCP_MODE: 'daemon',
  });
  try {
    const client = await connectedClient(server.url);
    const status = await client.callTool({ name: 'pulse_status', arguments: {} });
    assert.equal(status.isError, true);
    assert.equal(existsSync(join(dataDir, 'standalone', 'store.json')), false);
    await client.close();
  } finally {
    await server.stop();
    rmSync(dataDir, { recursive: true, force: true });
  }
});
