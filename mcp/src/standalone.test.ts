import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';

import { Client, StreamableHTTPClientTransport } from '@modelcontextprotocol/client';

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
    assert.deepEqual(saved.results, [{ id: saved.ids[0], result: 'created' }]);
    assert.match(saved.ids[0], /^pulse:\d+:0:[0-9a-f]{16}$/);
    assert.ok(existsSync(join(dataDir, 'standalone', 'store.json')));

    const fromAnotherHost = sampleCapsule('Pulse MCP ships a standalone lite engine.');
    fromAnotherHost.source.host = 'codex';
    fromAnotherHost.source.timestamp = '2026-06-11T11:00:00Z';
    const repeated = store.remember(fromAnotherHost);
    assert.deepEqual(repeated.ids, saved.ids);
    assert.deepEqual(repeated.results, [{ id: saved.ids[0], result: 'deduplicated' }]);

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

test('first run returns a guided demo that disappears after the first memory', () => {
  const dataDir = tempDataDir();
  try {
    const store = new StandaloneStore(dataDir);

    const emptyStatus = store.status() as { first_run?: { guided_demo: string[] } };
    assert.ok(emptyStatus.first_run, 'empty store status must carry first_run');
    assert.equal(emptyStatus.first_run.guided_demo.length, 4);

    const emptyResume = store.resume({}) as {
      first_run?: unknown;
      resume_markdown: string;
    };
    assert.ok(emptyResume.first_run, 'empty store resume must carry first_run');
    assert.match(emptyResume.resume_markdown, /SAFE FALLBACK/);
    assert.match(emptyResume.resume_markdown, /pulse_remember/);

    store.remember(sampleCapsule('First real memory ends onboarding.'));
    const status = store.status() as { first_run?: unknown };
    assert.equal(status.first_run, undefined);
    const resume = store.resume({}) as { first_run?: unknown; resume_markdown: string };
    assert.equal(resume.first_run, undefined);
    assert.doesNotMatch(resume.resume_markdown, /SAFE FALLBACK/);
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

    // Daemon semantics: scope "user" means no retention filter.
    const userScope = store.recall({ query: 'pulse', privacy_ceiling: 'private', scope: 'user' });
    assert.equal(userScope.items.length, 2);

    const projectScope = store.recall({
      query: 'pulse',
      privacy_ceiling: 'private',
      scope: 'project',
    });
    assert.equal(projectScope.items.length, 1);
    assert.equal(projectScope.items[0].retention, 'project');
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
    assert.equal(resume.schema, 'pulse.continuity.v2');
    assert.deepEqual(resume.token_economy, {
      state: 'collecting_baseline',
      method_id: 'utf8_bytes_div4_ceil',
      method_version: '1',
      rendered_bytes: Buffer.byteLength(resume.resume_markdown, 'utf8'),
      pulse_tokens: resume.token_estimate,
      reason_code: 'comparable_receipt_required',
    });
    assert.equal('estimated_raw_tokens' in resume.token_economy, false);
    assert.equal('estimated_saved_tokens' in resume.token_economy, false);
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

test('standalone emotional event is canonical, deduplicated, and can receive one confirmed cause', () => {
  const dataDir = tempDataDir();
  try {
    const store = new StandaloneStore(dataDir);
    const now = new Date().toISOString();
    const delta = {
      schema: 'pulse.semantic_delta.v1',
      source: { host: 'codex', conversation_scope: 'current_turn', timestamp: now },
      events: [{
        client_id: 'event:emotion',
        title: 'A joyful product moment',
        summary: 'The first complete local flow worked.',
        emotions: { 'радость': 0.8 },
        emotion_derivation: 'explicit',
        emotion_confidence: 0.9,
        observed_label: 'радость',
        confidence: 0.9,
        privacy_tier: 'private',
      }],
      raw_input_included: false,
    };
    const first = store.graphDelta(delta) as Record<string, any>;
    assert.equal(first.event_results[0].result, 'created');
    assert.equal(first.emotion_question.question, 'Что именно сейчас вызвало эту эмоцию?');
    const second = store.graphDelta(delta) as Record<string, any>;
    assert.equal(second.event_results[0].result, 'deduplicated');
    assert.equal(second.event_results[0].id, first.event_results[0].id);

    store.graphDelta({
      schema: 'pulse.semantic_delta.v1',
      source: { host: 'codex', conversation_scope: 'current_turn', timestamp: new Date(Date.now() + 1000).toISOString() },
      emotion_answers: [{
        question_id: first.emotion_question.question_id,
        trigger: {
          summary: 'The end-to-end local flow finally worked.',
          derivation: 'user_confirmed',
          confidence: 1,
        },
      }],
      raw_input_included: false,
    });
    const resume = store.resume({}) as Record<string, any>;
    assert.equal(resume.current_emotional_context.items[0].emotion, 'joy');
    assert.equal(resume.current_emotional_context.items[0].trigger_confirmed, true);
  } finally {
    rmSync(dataDir, { recursive: true, force: true });
  }
});

test('standalone resume budgets non-ASCII text by UTF-8 bytes without splitting emoji', () => {
  const dataDir = tempDataDir();
  try {
    const store = new StandaloneStore(dataDir);
    store.graphDelta({
      schema: 'pulse.semantic_delta.v1',
      source: {
        host: 'claude-code',
        conversation_scope: 'current_turn',
        timestamp: '2026-06-10T10:00:00Z',
        thread_id: 'utf8-budget',
      },
      continuity: {
        summary: 'Память '.repeat(100) + '🌱'.repeat(250),
        open_loops: ['🌱'.repeat(600)],
      },
      raw_input_included: false,
    });

    const resume = store.resume({ thread_id: 'utf8-budget', token_budget: 400 }) as {
      token_budget: number;
      token_estimate: number;
      token_economy: {
        method_id: string;
        rendered_bytes: number;
        pulse_tokens: number;
      };
      resume_markdown: string;
    };
    const renderedBytes = Buffer.byteLength(resume.resume_markdown, 'utf8');
    const hasUnpairedSurrogate = [...resume.resume_markdown].some((character) => {
      const codePoint = character.charCodeAt(0);
      return character.length === 1 && codePoint >= 0xd800 && codePoint <= 0xdfff;
    });

    assert.equal(resume.token_economy.method_id, 'utf8_bytes_div4_ceil');
    assert.ok(renderedBytes <= resume.token_budget * 4);
    assert.equal(resume.token_estimate, Math.ceil(renderedBytes / 4));
    assert.equal(resume.token_economy.rendered_bytes, renderedBytes);
    assert.equal(resume.token_economy.pulse_tokens, resume.token_estimate);
    assert.equal(hasUnpairedSurrogate, false);
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

    const listed = await client.listTools();
    const rememberTool = listed.tools.find((tool) => tool.name === 'pulse_remember');
    const recallTool = listed.tools.find((tool) => tool.name === 'pulse_recall');
    assert.deepEqual(rememberTool?.outputSchema?.required, ['ok', 'ids', 'results']);
    assert.deepEqual(recallTool?.outputSchema?.required, ['items']);

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
    assert.equal(saved.results[0].result, 'created');

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
