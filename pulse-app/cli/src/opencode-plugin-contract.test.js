import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

import { createPulseOpenCodeHooks, selectSmallModel } from '../../../plugins/pulse/opencode-core.mjs';

test('OpenCode bridge uses Node instead of the native host executable', () => {
  const loader = readFileSync(new URL('../../../plugins/pulse/pulse.js', import.meta.url), 'utf8');
  assert.match(loader, /spawn\('node', \[runtimePath, 'opencode-bridge', action\]/);
  assert.doesNotMatch(loader, /spawn\(process\.execPath/);
});

function chain() {
  const value = {};
  for (const name of ['min', 'max', 'optional']) value[name] = () => value;
  return value;
}

function fakeTool(definition) { return definition; }
fakeTool.schema = {
  array: () => chain(),
  enum: () => chain(),
  number: () => chain(),
  object: () => chain(),
  string: () => chain(),
};

function message(sessionID, messageID, text = `question ${sessionID}`) {
  return [{ sessionID, messageID, model: { providerID: 'local', modelID: 'test' } }, {
    parts: [{ type: 'text', text }],
  }];
}

test('OpenCode injects recalled memory into the first model call of the same request', async () => {
  const calls = [];
  const hooks = createPulseOpenCodeHooks({
    directory: '/tmp/project', tool: fakeTool,
    bridge: async (action, input) => {
      calls.push({ action, input });
      return {
        schema: 'pulse.opencode_message.v1', context: `memory for ${input.session_id}`,
        source_event_key: `event_${'a'.repeat(64)}`, idempotency_key: `lifecycle:${'b'.repeat(64)}`,
      };
    },
  });
  await hooks['chat.message'](...message('session_a', 'message_a', 'raw question'));
  const output = { system: ['base'] };
  await hooks['experimental.chat.system.transform']({ sessionID: 'session_a', model: {} }, output);
  assert.deepEqual(output.system, ['base', 'memory for session_a']);
  assert.equal(calls.find((call) => call.action === 'message').input.query, 'raw question');
  const second = { system: ['base'] };
  await hooks['experimental.chat.system.transform']({ sessionID: 'session_a', model: {} }, second);
  assert.deepEqual(second.system, ['base']);
});

test('parallel OpenCode sessions never mix recall or pulse_memory authority', async () => {
  const memoryCalls = [];
  const hooks = createPulseOpenCodeHooks({
    directory: '/tmp/project', tool: fakeTool,
    bridge: async (action, input) => {
      if (action === 'memory') {
        memoryCalls.push(input);
        return { status: 'stored', ids: [] };
      }
      return {
        schema: 'pulse.opencode_message.v1', context: `memory ${input.session_id}`,
        source_event_key: `event_${createChar(input.session_id).repeat(64)}`,
        idempotency_key: `lifecycle:${createChar(input.turn_id).repeat(64)}`,
      };
    },
  });
  await Promise.all([
    hooks['chat.message'](...message('session_a', 'message_a')),
    hooks['chat.message'](...message('session_b', 'message_b')),
  ]);
  const left = { system: [] };
  const right = { system: [] };
  await hooks['experimental.chat.system.transform']({ sessionID: 'session_a', model: {} }, left);
  await hooks['experimental.chat.system.transform']({ sessionID: 'session_b', model: {} }, right);
  assert.deepEqual(left.system, ['memory session_a']);
  assert.deepEqual(right.system, ['memory session_b']);
  await hooks.tool.pulse_memory.execute({
    items: [{ kind: 'decision', scope: 'project', summary: 'A' }],
  }, { sessionID: 'session_b', messageID: 'tool_b', abort: new AbortController().signal });
  assert.equal(memoryCalls[0].session_id, 'session_b');
  assert.equal(memoryCalls[0].turn_id, 'message_b');
});

test('idle, cancellation and bridge errors stay fail-open without continuation', async () => {
  const hooks = createPulseOpenCodeHooks({
    directory: '/tmp/project', tool: fakeTool,
    bridge: async () => { throw new Error('offline'); },
  });
  await hooks['chat.message'](...message('session_error', 'message_error'));
  const output = { system: ['base'] };
  await hooks['experimental.chat.system.transform']({ sessionID: 'session_error', model: {} }, output);
  await hooks.event({ event: { type: 'session.idle', properties: { sessionID: 'session_error' } } });
  assert.deepEqual(output.system, ['base']);
  assert.equal(Object.keys(hooks).includes('session.prompt'), false);
  assert.deepEqual(Object.keys(hooks.tool), ['pulse_memory']);
});

test('small model uses explicit config or a provably cheaper active text model from the same provider', async () => {
  assert.deepEqual(await selectSmallModel({}, '/tmp/project', {}, 'other/tiny'), {
    providerID: 'other', modelID: 'tiny',
  });
  const client = { config: { providers: async () => ({ data: { providers: [{
    id: 'same', models: {
      main: { id: 'main', status: 'active', cost: { input: 3, output: 9 }, capabilities: { output: { text: true } } },
      cheap: { id: 'cheap', status: 'active', cost: { input: 1, output: 4 }, capabilities: { output: { text: true } } },
      expensive: { id: 'expensive', status: 'active', cost: { input: 4, output: 4 }, capabilities: { output: { text: true } } },
      deprecated: { id: 'deprecated', status: 'deprecated', cost: { input: 0, output: 0 }, capabilities: { output: { text: true } } },
    },
  }], default: {} } }) } };
  assert.deepEqual(await selectSmallModel(client, '/tmp/project', {
    providerID: 'same', modelID: 'main',
  }), { providerID: 'same', modelID: 'cheap' });
});

test('fun fact service sees only candidate IDs and texts, disables tools, and cannot invent a fact', async () => {
  const bridgeCalls = [];
  let promptBody;
  const client = {
    session: {
      create: async () => ({ data: { id: 'service_fact' } }),
      prompt: async (request) => {
        promptBody = request.body;
        return { data: { info: { tokens: { input: 4, output: 1 } }, parts: [{ type: 'text', text: 'invented' }] } };
      },
      abort: async () => ({ data: true }),
      delete: async () => ({ data: true }),
    },
  };
  const hooks = createPulseOpenCodeHooks({
    directory: '/tmp/project', client, tool: fakeTool,
    bridge: async (action, input) => {
      bridgeCalls.push({ action, input });
      if (action === 'fun-fact-candidates') return {
        schema: 'pulse.opencode_fun_fact_candidates.v1', enabled: true,
        candidate_digest: 'd'.repeat(64),
        candidates: [
          { id: `fact_${'a'.repeat(24)}`, text: 'Pulse stays local.' },
          { id: `fact_${'b'.repeat(24)}`, text: 'Memory is project-bound.' },
        ],
      };
      if (action === 'message') return {
        schema: 'pulse.opencode_message.v1', context: 'accepted memory',
        source_event_key: `event_${'c'.repeat(64)}`, idempotency_key: `lifecycle:${'e'.repeat(64)}`,
      };
      return { recorded: true };
    },
  });
  await hooks.config({ small_model: 'same/tiny' });
  await hooks['chat.message'](...message('session_fact', 'message_fact', 'SECRET USER QUERY'));
  await new Promise((resolve) => setImmediate(resolve));
  const output = { system: [] };
  await hooks['experimental.chat.system.transform']({ sessionID: 'session_fact', model: {} }, output);
  assert.equal(promptBody.tools['*'], false);
  assert.equal(JSON.stringify(promptBody).includes('SECRET USER QUERY'), false);
  assert.match(promptBody.parts[0].text, /Pulse stays local/);
  assert.match(output.system[1], /Pulse stays local/);
  assert.equal(output.system.join('\n').includes('invented'), false);
  const receipt = bridgeCalls.find((call) => call.action === 'fun-fact-receipt').input;
  assert.deepEqual(receipt.usage, { input: 4, output: 1, total: 5 });
  assert.equal(JSON.stringify(receipt).includes('Pulse stays local'), false);

  await hooks.event({ event: { type: 'session.idle', properties: { sessionID: 'session_fact' } } });
  await hooks['chat.message'](...message('session_fact', 'message_fact_two', 'Another normal request'));
  const secondOutput = { system: [] };
  await hooks['experimental.chat.system.transform']({ sessionID: 'session_fact', model: {} }, secondOutput);
  assert.equal(bridgeCalls.filter((call) => call.action === 'fun-fact-candidates').length, 1);
  assert.equal(secondOutput.system.some((line) => line.includes('Pulse session fact')), false);
});

function createChar(value) {
  return value.endsWith('a') ? 'a' : 'b';
}
