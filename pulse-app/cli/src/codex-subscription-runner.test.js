import assert from 'node:assert/strict';
import { writeFile } from 'node:fs/promises';
import test from 'node:test';

import {
  CODEX_DISABLED_FEATURES,
  CODEX_LUNA_MODEL,
  buildCodexExecArgs,
  classifyCodexFailure,
  codexSubscriptionContractDigest,
  offlineCodexPreflight,
  parseCodexEventStream,
  runHistoricalIngestUnit,
  scrubCodexEnvironment,
} from './codex-subscription-runner.js';
import {
  assertHistoricalIngestManifest,
  codexHistoricalIngestOutputSchemaBytes,
  normalizeCodexHistoricalIngestManifest,
} from './historical-ingest-protocol.js';

const digest = 'a'.repeat(64);

function manifest(overrides = {}) {
  return {
    schema_version: 'https://zbs.gg/schemas/pulse/historical-ingest/v1',
    job_id: 'job_0123456789abcdef',
    revision: 1,
    source_snapshot_digest: digest,
    items: [],
    ...overrides,
  };
}

function successfulEvents(usage = {}) {
  return [
    JSON.stringify({ type: 'thread.started', thread_id: 'thread_canary' }),
    JSON.stringify({ type: 'turn.started' }),
    JSON.stringify({ type: 'item.completed', item: { id: 'item_0', type: 'agent_message', text: '{}' } }),
    JSON.stringify({
      type: 'turn.completed',
      usage: { input_tokens: 100, cached_input_tokens: 10, output_tokens: 20, reasoning_output_tokens: 2, ...usage },
    }),
  ].join('\n');
}

test('runner pins Luna low, isolation flags, closed schema, and every qualified tool disable', () => {
  const args = buildCodexExecArgs({
    cwd: '/private/stage',
    schemaPath: '/private/schema.json',
    outputPath: '/private/output.json',
    prompt: 'trusted instructions',
  });
  assert.deepEqual(args.slice(0, 2), ['exec', '--ephemeral']);
  assert.ok(args.includes('--ignore-user-config'));
  assert.ok(args.includes('--ignore-rules'));
  assert.ok(args.includes('--strict-config'));
  assert.ok(args.includes('--skip-git-repo-check'));
  assert.deepEqual(args.slice(args.indexOf('--sandbox'), args.indexOf('--sandbox') + 2), ['--sandbox', 'read-only']);
  assert.deepEqual(args.slice(args.indexOf('--model'), args.indexOf('--model') + 2), ['--model', CODEX_LUNA_MODEL]);
  assert.ok(args.includes('model_reasoning_effort="low"'));
  assert.ok(args.includes('approval_policy="never"'));
  assert.ok(args.includes('web_search="disabled"'));
  assert.equal(args.at(-1), 'trusted instructions');
  for (const feature of CODEX_DISABLED_FEATURES) {
    assert.ok(args.some((value, index) => value === '--disable' && args[index + 1] === feature), feature);
  }
  assert.equal(args.includes('cli_auth_credentials_store="keyring"'), false);
});

test('runner environment cannot select an API key or alternate provider', () => {
  const clean = scrubCodexEnvironment({
    PATH: '/bin', HOME: '/original', CODEX_HOME: '/original/.codex',
    OPENAI_API_KEY: 'secret', OPENAI_BASE_URL: 'https://other.invalid',
    ANTHROPIC_API_KEY: 'secret', COHERE_API_KEY: 'secret', CODEX_PROVIDER: 'custom',
  }, { isolatedHome: '/private/home', isolatedCodexHome: '/private/codex' });
  assert.deepEqual(clean, {
    PATH: '/bin', HOME: '/private/home', CODEX_HOME: '/private/codex',
    NO_COLOR: '1', CODEX_NON_INTERACTIVE: '1',
  });
});

test('missing default auth file falls back to native Codex credential authority without provider env', async () => {
  const expected = manifest();
  const qualification = {
    ready: true,
    live_model_qualified: true,
    auth: 'chatgpt',
    cli_version: 'codex-cli 0.144.6',
    model: CODEX_LUNA_MODEL,
    effort: 'low',
    contract_digest: codexSubscriptionContractDigest(),
  };
  let observed;
  let observedPreflight;
  await runHistoricalIngestUnit({
    prompt: 'trusted instructions',
    evidence: 'bounded private evidence',
    expectedJobID: expected.job_id,
    expectedSnapshotDigest: digest,
    egressAuthorized: true,
    qualification,
    environment: {
      PATH: '/bin', HOME: '/native/home', CODEX_HOME: '/native/codex',
      PULSE_TEST_MARKER: 'preserved',
    },
    invoke: async (request) => {
      observed = request;
      await writeFile(request.outputPath, JSON.stringify(expected), { mode: 0o600 });
      return { status: 0, signal: null, stdout: successfulEvents(), stderr: '' };
    },
    copyAuth: async () => {
      const error = new Error('missing');
      error.code = 'ENOENT';
      throw error;
    },
    preflight: async (options) => {
      observedPreflight = options;
      return { ...qualification, live_model_qualified: false };
    },
  });
  assert.equal(observed.env.HOME, '/native/home');
  assert.equal(observed.env.CODEX_HOME, '/native/codex');
  assert.equal(observed.env.PULSE_TEST_MARKER, 'preserved');
  assert.equal(observed.env.OPENAI_API_KEY, undefined);
  assert.equal(observed.env.CODEX_PROVIDER, undefined);
  assert.ok(observed.args.includes('cli_auth_credentials_store="keyring"'));
  assert.equal(observedPreflight.isolatedHome, '/native/home');
  assert.equal(observedPreflight.isolatedCodexHome, '/native/codex');
  assert.equal(observedPreflight.credentialStore, 'keyring');
});

test('explicit missing auth file never falls back to native credentials', async () => {
  let invoked = false;
  await assert.rejects(() => runHistoricalIngestUnit({
    prompt: 'trusted', evidence: 'private', expectedJobID: 'job_0123456789abcdef',
    expectedSnapshotDigest: digest, egressAuthorized: true, authFile: '/explicit/auth.json',
    qualification: { live_model_qualified: true, contract_digest: codexSubscriptionContractDigest() },
    copyAuth: async () => {
      const error = new Error('missing');
      error.code = 'ENOENT';
      throw error;
    },
    invoke: async () => { invoked = true; },
  }), /missing/);
  assert.equal(invoked, false);
});

test('offline preflight proves only pinned local CLI, ChatGPT auth, Luna catalog, and disabled features', async () => {
  const calls = [];
  const run = async (_command, args) => {
    calls.push(args);
    if (args[0] === '--version') return { status: 0, stdout: 'codex-cli 0.144.6\n', stderr: '' };
    if (args[0] === 'login') return { status: 0, stdout: 'Logged in using ChatGPT\n', stderr: '' };
    if (args.includes('features')) {
      return { status: 0, stdout: CODEX_DISABLED_FEATURES.map((name) => `${name} stable false`).join('\n'), stderr: '' };
    }
    return {
      status: 0,
      stdout: JSON.stringify({ models: [{ slug: CODEX_LUNA_MODEL, supported_reasoning_levels: [{ effort: 'low' }], tool_mode: 'code_mode_only' }] }),
      stderr: '',
    };
  };
  const result = await offlineCodexPreflight({ run, env: {}, isolatedHome: '/private/home', isolatedCodexHome: '/private/codex' });
  assert.equal(result.ready, true);
  assert.equal(result.live_model_qualified, false);
  assert.equal(result.auth, 'chatgpt');
  assert.equal(calls.length, 4);
});

test('offline preflight rejects API env, stale auth, changed CLI, missing Luna, and an enabled capability', async () => {
  await assert.rejects(() => offlineCodexPreflight({ run: async () => ({ status: 0, stdout: '', stderr: '' }), env: { OPENAI_API_KEY: 'x' } }), /api_environment_present/);
  const fixtures = {
    version: { status: 0, stdout: 'codex-cli 0.145.0', stderr: '' },
    login: { status: 0, stdout: 'Logged in using API key', stderr: '' },
    features: { status: 0, stdout: CODEX_DISABLED_FEATURES.map((name, index) => `${name} stable ${index === 0 ? 'true' : 'false'}`).join('\n'), stderr: '' },
    models: { status: 0, stdout: JSON.stringify({ models: [] }), stderr: '' },
  };
  for (const [failure, replacement] of Object.entries(fixtures)) {
    let step = 0;
    const normal = [
      { status: 0, stdout: 'codex-cli 0.144.6', stderr: '' },
      { status: 0, stdout: 'Logged in using ChatGPT', stderr: '' },
      { status: 0, stdout: CODEX_DISABLED_FEATURES.map((name) => `${name} stable false`).join('\n'), stderr: '' },
      { status: 0, stdout: JSON.stringify({ models: [{ slug: CODEX_LUNA_MODEL, supported_reasoning_levels: [{ effort: 'low' }] }] }), stderr: '' },
    ];
    const at = { version: 0, login: 1, features: 2, models: 3 }[failure];
    normal[at] = replacement;
    await assert.rejects(() => offlineCodexPreflight({ run: async () => normal[step++], env: {} }));
  }
});

test('event parser requires one clean terminal turn and rejects any tool activity', () => {
  assert.deepEqual(parseCodexEventStream(successfulEvents()).usage, {
    input_tokens: 100, cached_input_tokens: 10, output_tokens: 20, reasoning_output_tokens: 2,
  });
  assert.deepEqual(parseCodexEventStream([
    JSON.stringify({ type: 'thread.started', thread_id: 'thread_1' }),
    JSON.stringify({ type: 'item.completed', item: { type: 'plan', text: 'synthetic plan metadata' } }),
    JSON.stringify({ type: 'turn.completed', usage: { input_tokens: 1, cached_input_tokens: 0, output_tokens: 1, reasoning_output_tokens: 0 } }),
  ].join('\n')).usage.input_tokens, 1);
  const tool = [
    JSON.stringify({ type: 'thread.started', thread_id: 'thread_1' }),
    JSON.stringify({ type: 'item.started', item: { type: 'command_execution', command: 'pwd' } }),
    JSON.stringify({ type: 'turn.completed', usage: {} }),
  ].join('\n');
  assert.throws(() => parseCodexEventStream(tool), /tool_activity/);
  assert.throws(() => parseCodexEventStream(`${successfulEvents()}\n${JSON.stringify({ type: 'turn.completed', usage: {} })}`), /terminal_event_count/);
  assert.throws(() => parseCodexEventStream(JSON.stringify({ type: 'turn.failed', error: { message: 'bad' } })), /turn_failed/);
  assert.throws(() => parseCodexEventStream('{broken'), /event_stream_invalid/);
});

test('only supported quota failures become paused_quota', () => {
  assert.equal(classifyCodexFailure({ status: 1, stderr: 'You have hit your usage limit. Try again later.' }), 'paused_quota');
  assert.equal(classifyCodexFailure({ status: 1, stderr: '401 authentication required' }), 'auth_failed');
  assert.equal(classifyCodexFailure({ status: 1, stderr: 'model gpt-5.6-luna unavailable' }), 'model_unavailable');
  assert.equal(classifyCodexFailure({ status: 1, stderr: 'internal failure' }), 'runner_failed');
});

test('historical protocol is closed and preserves inference and provenance rules', () => {
  assert.deepEqual(assertHistoricalIngestManifest(manifest()), manifest());
  assert.throws(() => assertHistoricalIngestManifest(manifest({ extra: true })), /unknown_field/);
  assert.throws(() => assertHistoricalIngestManifest(manifest({ source_snapshot_digest: '/Users/example/history' })), /manifest_identity/);
  assert.throws(() => assertHistoricalIngestManifest(manifest({ items: [{
    candidate_id: 'candidate_0123456789abcdef', kind: 'state', confidence: 0.7, privacy: 'private',
    epistemic_status: 'explicit', derivation: 'inferred', valid_time: { from: '2026-07-22T00:00:00Z' },
    scope: { kind: 'global' }, source_refs: [{ alias: 'source_0123456789abcdef', prefix_digest: digest, record_locator: 'r:1' }],
    payload: { state_kind: 'emotion', summary: 'inferred state' },
  }] })), /inferred_explicit/);
  assert.throws(() => assertHistoricalIngestManifest(manifest({ items: [{
    candidate_id: 'candidate_0123456789abcdef', kind: 'assertion', confidence: 0.7, privacy: 'private',
    epistemic_status: 'explicit', derivation: 'direct', valid_time: { from: '2026-07-22T00:00:00Z' },
    scope: { kind: 'unassigned' }, source_refs: [{ alias: 'source_0123456789abcdef', prefix_digest: digest, record_locator: 'r:1' }],
    payload: { subject_id: 'not an opaque id', predicate: 'prefers', object_value: 'Pulse' },
  }] })), /payload_subject_id/);
  assert.throws(() => assertHistoricalIngestManifest(manifest({ items: [{
    candidate_id: 'candidate_0123456789abcdef', kind: 'decision', confidence: 0.7, privacy: 'private',
    epistemic_status: 'explicit', derivation: 'direct', valid_time: { from: '2026-07-22T00:00:00Z' },
    scope: { kind: 'unassigned' }, source_refs: [{ alias: 'source_0123456789abcdef', prefix_digest: digest, record_locator: 'r:1' }],
    payload: { summary: 'x'.repeat(4001) },
  }] })), /payload_summary/);
});

test('Codex output schema stays closed without unsupported conditionals and normalizes nullable optionals', () => {
  const schema = JSON.parse(codexHistoricalIngestOutputSchemaBytes());
  assert.equal(JSON.stringify(schema).includes('allOf'), false);
  assert.equal(schema.additionalProperties, false);
  assert.deepEqual(schema.$defs.payload.required.sort(), Object.keys(schema.$defs.payload.properties).sort());
  const value = manifest({ items: [] });
  assert.deepEqual(normalizeCodexHistoricalIngestManifest(value), value);
  const withIncompleteConditionalItem = manifest({ items: [
    {
      candidate_id: 'candidate_0123456789abcdef', kind: 'assertion', confidence: 0.7, privacy: 'private',
      epistemic_status: 'explicit', derivation: 'direct', valid_time: { from: '2026-07-22T00:00:00Z', to: null },
      scope: { kind: 'unassigned', project_id: null }, source_refs: [{ alias: 'source_0123456789abcdef', prefix_digest: digest, record_locator: 'r:1' }],
      payload: { title: null, summary: 'insufficient assertion', subject_id: null, predicate: null, object_value: null, object_id: null, entity_type: null, name: null, state_kind: null, intensity: null, continuity_status: null },
    },
    {
      candidate_id: 'candidate_fedcba9876543210', kind: 'decision', confidence: 0.8, privacy: 'private',
      epistemic_status: 'explicit', derivation: 'direct', valid_time: { from: '2026-07-22T00:00:00Z', to: null },
      scope: { kind: 'unassigned', project_id: null }, source_refs: [{ alias: 'source_0123456789abcdef', prefix_digest: digest, record_locator: 'r:2' }],
      payload: { title: null, summary: 'keep this decision', subject_id: null, predicate: null, object_value: null, object_id: null, entity_type: null, name: null, state_kind: null, intensity: null, continuity_status: null },
    },
    {
      candidate_id: 'candidate_aaaaaaaaaaaaaaaa', kind: 'relation', confidence: 0.7, privacy: 'private',
      epistemic_status: 'explicit', derivation: 'direct', valid_time: { from: '2026-07-22T00:00:00Z', to: null },
      scope: { kind: 'unassigned', project_id: null }, source_refs: [{ alias: 'source_0123456789abcdef', prefix_digest: digest, record_locator: 'r:3' }],
      payload: { title: null, summary: null, subject_id: 'Nikita Shilov', predicate: 'works_on', object_value: null, object_id: 'project:pulse', entity_type: null, name: null, state_kind: null, intensity: null, continuity_status: null },
    },
  ] });
  assert.deepEqual(normalizeCodexHistoricalIngestManifest(withIncompleteConditionalItem).items.map((item) => item.candidate_id), ['candidate_fedcba9876543210']);
});

test('Codex normalization discards an invalid optional end time before canonical validation', () => {
  const value = manifest({ items: [{
    candidate_id: 'candidate_fedcba9876543210', kind: 'decision', confidence: 0.8, privacy: 'private',
    epistemic_status: 'explicit', derivation: 'direct',
    valid_time: { from: '2026-07-22T00:00:00Z', to: '2026-07-21T00:00:00Z' },
    scope: { kind: 'unassigned', project_id: null },
    source_refs: [{ alias: 'source_0123456789abcdef', prefix_digest: digest, record_locator: 'r:2' }],
    payload: { title: null, summary: 'keep this decision', subject_id: null, predicate: null, object_value: null, object_id: null, entity_type: null, name: null, state_kind: null, intensity: null, continuity_status: null },
  }] });
  const normalized = normalizeCodexHistoricalIngestManifest(value);
  assert.equal(Object.hasOwn(normalized.items[0].valid_time, 'to'), false);
  assert.deepEqual(assertHistoricalIngestManifest(normalized), normalized);
});

test('unit run sends evidence only on stdin and returns a content-free receipt', async () => {
  const expected = manifest();
  const qualification = {
    ready: true,
    live_model_qualified: true,
    auth: 'chatgpt',
    cli_version: 'codex-cli 0.144.6',
    model: CODEX_LUNA_MODEL,
    effort: 'low',
    contract_digest: codexSubscriptionContractDigest(),
  };
  let observed;
  const result = await runHistoricalIngestUnit({
    prompt: 'trusted instructions',
    evidence: 'bounded private evidence',
    expectedJobID: expected.job_id,
    expectedSnapshotDigest: digest,
    egressAuthorized: true,
    qualification,
    authFile: '/private/source/auth.json',
    invoke: async (request) => {
      observed = request;
      await writeFile(request.outputPath, JSON.stringify(expected), { mode: 0o600 });
      return { status: 0, signal: null, stdout: successfulEvents(), stderr: '' };
    },
    copyAuth: async () => {},
    preflight: async () => ({ ...qualification, live_model_qualified: false }),
  });
  assert.equal(observed.stdin, 'bounded private evidence');
  assert.equal(observed.args.at(-1), 'trusted instructions');
  assert.equal(result.model, CODEX_LUNA_MODEL);
  assert.equal(result.output_digest.length, 64);
  assert.equal(result.source_snapshot_digest, digest);
  assert.equal(result.item_count, 0);
  assert.equal(result.usage.input_tokens, 100);
  assert.doesNotMatch(JSON.stringify(result), /bounded private evidence|trusted instructions|\/private\//);
});

test('unit run refuses missing consent and does not invoke Codex', async () => {
  let invoked = false;
  await assert.rejects(() => runHistoricalIngestUnit({
    prompt: 'trusted', evidence: 'private', expectedJobID: 'job_0123456789abcdef',
    expectedSnapshotDigest: digest, egressAuthorized: false, authFile: '/private/auth.json',
    invoke: async () => { invoked = true; }, copyAuth: async () => {},
  }), /egress_not_authorized/);
  assert.equal(invoked, false);
});
