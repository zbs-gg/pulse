#!/usr/bin/env node

import { spawn, spawnSync } from 'node:child_process';
import { createHash, generateKeyPairSync, randomBytes, sign } from 'node:crypto';
import {
  chmodSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  realpathSync,
  renameSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { basename, dirname, isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const PACKAGE_NAME = '@zbs-gg/pulse';
const DEFAULT_PACKAGE_VERSION = '0.8.1';
const RESULT_SCHEMA = 'pulse.product_memory_benchmark_result.v1';
const CONTEXT_HEADER = 'Pulse accepted memory (local; use as factual context for this question unless the user provides newer information):';
const SILENCE_QUERIES = [
  ['silence-01', 'How many minutes should I boil an egg so the yolk stays runny?'],
  ['silence-02', 'What is the weather usually like in Lisbon in October?'],
  ['silence-03', 'Explain why the sky looks blue in one sentence.'],
  ['silence-04', 'Give me a simple recipe for banana pancakes.'],
  ['silence-05', 'What is the capital of New Zealand?'],
  ['silence-06', 'Convert 37 degrees Celsius to Fahrenheit.'],
  ['silence-07', 'Write a regular expression that matches a six digit postal code.'],
  ['silence-08', 'What causes ocean tides?'],
  ['silence-09', 'Name three common ingredients in hummus.'],
  ['silence-10', 'How do I center a div with modern CSS?'],
];
const LOCAL_COMPOSITOR = resolve(dirname(fileURLToPath(import.meta.url)), '..', 'src', 'product-compositor.js');

function fail(code, detail = '') {
  throw new Error(detail ? `${code}:${detail}` : code);
}

function parseArgs(argv) {
  const out = { package_version: DEFAULT_PACKAGE_VERSION, keep_workdir: false };
  for (let index = 0; index < argv.length; index += 1) {
    const name = argv[index];
    if (name === '--keep-workdir' || name === '--candidate-compositor' ||
        name === '--compatible-active-runtime') {
      out[name.slice(2).replaceAll('-', '_')] = true;
      continue;
    }
    if (!name.startsWith('--')) fail('benchmark_option_invalid', name);
    const value = argv[index + 1];
    if (!value || value.startsWith('--')) fail('benchmark_option_missing', name);
    out[name.slice(2).replaceAll('-', '_')] = value;
    index += 1;
  }
  if (![
    'own-v1', 'emobench-v3-product', 'longmemeval-s-retrieval-30', 'longmemeval-atoms',
    'locomo-retrieval', 'locomo-atoms', 'emobench-atoms',
  ].includes(out.suite)) fail('benchmark_suite_invalid');
  for (const name of ['dataset', 'output']) {
    if (!isAbsolute(out[name] ?? '') || resolve(out[name]) !== out[name]) fail('benchmark_path_invalid', name);
  }
  if (out.e2e_input !== undefined && (!isAbsolute(out.e2e_input) || resolve(out.e2e_input) !== out.e2e_input)) {
    fail('benchmark_path_invalid', 'e2e_input');
  }
  if (out.candidate_daemon !== undefined &&
      (!isAbsolute(out.candidate_daemon) || resolve(out.candidate_daemon) !== out.candidate_daemon ||
       !existsSync(out.candidate_daemon) || !lstatSync(out.candidate_daemon).isFile() ||
       lstatSync(out.candidate_daemon).isSymbolicLink())) {
    fail('benchmark_path_invalid', 'candidate_daemon');
  }
  if (out.max_questions !== undefined) {
    out.max_questions = Number(out.max_questions);
    if (!Number.isSafeInteger(out.max_questions) || out.max_questions < 1) fail('benchmark_max_questions_invalid');
  }
  if (!/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(out.package_version)) fail('benchmark_version_invalid');
  return out;
}

function run(command, args, { cwd, env, input, timeout = 120_000, expectedStatus = 0 } = {}) {
  const result = spawnSync(command, args, {
    cwd,
    env,
    input,
    encoding: 'utf8',
    timeout,
    maxBuffer: 64 * 1024 * 1024,
    stdio: ['pipe', 'pipe', 'pipe'],
  });
  if (result.status !== expectedStatus) {
    fail('benchmark_command_failed', `${basename(command)}:${result.status}:${result.stderr.slice(0, 400)}`);
  }
  return result;
}

function readJSON(path, code, maximum = 64 * 1024 * 1024) {
  let info;
  try { info = lstatSync(path); } catch { fail(code); }
  if (!info.isFile() || info.isSymbolicLink() || info.size < 1 || info.size > maximum) fail(code);
  try { return JSON.parse(readFileSync(path, 'utf8')); } catch { fail(code); }
}

function atomicWriteJSON(path, value) {
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  const temporary = `${path}.${process.pid}.${Date.now()}.new`;
  writeFileSync(temporary, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600, flag: 'wx' });
  renameSync(temporary, path);
  chmodSync(path, 0o600);
}

function sha256(value) {
  return createHash('sha256').update(value).digest('hex');
}

function sha256File(path) {
  return sha256(readFileSync(path));
}

function physicalTreeBytes(path) {
  const info = lstatSync(path);
  if (info.isSymbolicLink()) return info.blocks * 512;
  if (info.isFile()) return info.blocks * 512;
  if (!info.isDirectory()) return 0;
  return info.blocks * 512 + readdirSync(path).reduce(
    (sum, name) => sum + physicalTreeBytes(join(path, name)), 0,
  );
}

function boundedSummary(value) {
  const normalized = value.replaceAll(/\s+/g, ' ').trim();
  let characters = [...normalized];
  let clipped = false;
  if (characters.length > 400) {
    characters = characters.slice(0, 399);
    clipped = true;
  }
  while (Buffer.byteLength(`${characters.join('')}${clipped ? '…' : ''}`, 'utf8') > 1200) {
    characters.pop();
    clipped = true;
  }
  let output = characters.join('');
  if (clipped) {
    const boundary = output.lastIndexOf(' ');
    if (boundary >= Math.min(320, Math.max(1, output.length - 40))) output = output.slice(0, boundary);
    output = `${output.trimEnd()}…`;
  }
  return output;
}

function productKind(value) {
  if (value === 'preference') return 'preference';
  if (value === 'correction' || value === 'do_not_repeat') return 'correction';
  if (value === 'project_state') return 'project_state';
  return 'project_state';
}

function loadOwnSuite(path) {
  const document = readJSON(path, 'benchmark_dataset_invalid');
  if (!Array.isArray(document.items) || document.items.length < 1 || !Array.isArray(document.queries)) {
    fail('benchmark_dataset_invalid');
  }
  const items = new Map();
  for (const item of document.items) {
    if (!item || typeof item.id !== 'string' || !/^[a-z0-9._-]{2,80}$/.test(item.id) || items.has(item.id) ||
        typeof item.text !== 'string' || item.text.trim() === '' || item.text.length > 4_000) {
      fail('benchmark_dataset_item_invalid');
    }
    items.set(item.id, {
      id: item.id,
      kind: productKind(item.kind),
      scope: 'project',
      summary: boundedSummary(item.text),
    });
  }
  const queries = document.queries.filter((item) => item?.kind !== 'consolidation').map((item) => {
    if (!item || typeof item.id !== 'string' || typeof item.query !== 'string' ||
        typeof item.expected_item_id !== 'string' || !items.has(item.expected_item_id)) {
      fail('benchmark_dataset_query_invalid');
    }
    return {
      id: item.id,
      query: item.query,
      expectation: 'hit',
      expected_ids: [item.expected_item_id],
    };
  });
  for (const [id, query] of SILENCE_QUERIES) {
    queries.push({ id, query, expectation: 'silence', expected_ids: [] });
  }
  return {
    cases: [{ id: 'own', items: [...items.values()], queries, batch_size: 3 }],
    excluded: [],
  };
}

function loadEmoBenchSuite(path) {
  const document = readJSON(path, 'benchmark_dataset_invalid');
  if (!Array.isArray(document.events) || !Array.isArray(document.tests)) fail('benchmark_dataset_invalid');
  const allowedEmotions = new Set([
    'joy', 'sadness', 'anger', 'fear', 'trust', 'disgust', 'anticipation', 'surprise', 'shame', 'guilt',
  ]);
  const items = document.events.map((event) => {
    if (!Number.isSafeInteger(event?.id) || typeof event.text !== 'string' || !event.emotion_tags ||
        typeof event.emotion_tags !== 'object' || Array.isArray(event.emotion_tags)) {
      fail('benchmark_dataset_item_invalid');
    }
    const ranked = Object.entries(event.emotion_tags)
      .filter(([label, intensity]) => allowedEmotions.has(label) && Number.isFinite(intensity))
      .sort((left, right) => right[1] - left[1] || left[0].localeCompare(right[0]));
    if (ranked.length === 0 || ranked[0][1] < 0 || ranked[0][1] > 1) fail('benchmark_dataset_item_invalid');
    return {
      id: `event-${event.id}`,
      kind: 'emotion',
      scope: 'project',
      summary: boundedSummary(event.text),
      emotion: { label: ranked[0][0], intensity: ranked[0][1], source: 'inferred' },
    };
  });
  const core = document.tests.filter((item) => item?.test_type === 'core');
  const queries = core.map((item) => {
    if (typeof item.id !== 'string' || typeof item.user_query !== 'string' ||
        !Array.isArray(item.ideal_top_3_event_ids) || item.ideal_top_3_event_ids.length !== 3) {
      fail('benchmark_dataset_query_invalid');
    }
    return {
      id: item.id,
      query: item.user_query,
      expectation: 'hit',
      expected_ids: item.ideal_top_3_event_ids.map((id) => `event-${id}`),
    };
  });
  for (const [id, query] of SILENCE_QUERIES) {
    queries.push({ id, query, expectation: 'silence', expected_ids: [] });
  }
  return {
    cases: [{ id: 'emobench', items, queries, batch_size: 1 }],
    excluded: [
      { cases: document.tests.filter((item) => item?.test_type === 'stateful').length, reason: 'hidden_user_state' },
      { cases: document.tests.filter((item) => item?.test_type === 'multi_signal').length, reason: 'hidden_biometrics' },
      { cases: document.tests.filter((item) => item?.test_type === 'chain').length, reason: 'atlas_graph_chain' },
    ],
  };
}

function turnSummary({ date = '', role = '', content = '' }) {
  const prefix = [date, role].filter(Boolean).join(' — ');
  return boundedSummary(`${prefix ? `${prefix}: ` : ''}${content}`);
}

function loadLongMemEvalSuite(path) {
  const document = readJSON(path, 'benchmark_dataset_invalid', 512 * 1024 * 1024);
  if (!Array.isArray(document)) fail('benchmark_dataset_invalid');
  const grouped = new Map();
  for (const item of document) {
    if (typeof item?.question_type !== 'string') fail('benchmark_dataset_query_invalid');
    const bucket = grouped.get(item.question_type) ?? [];
    bucket.push(item);
    grouped.set(item.question_type, bucket);
  }
  const selected = [...grouped.entries()].flatMap(([, items]) =>
    items.sort((left, right) => String(left.question_id).localeCompare(String(right.question_id))).slice(0, 5));
  const cases = selected.map((entry) => {
    if (typeof entry.question_id !== 'string' || typeof entry.question !== 'string' ||
        !Array.isArray(entry.haystack_sessions) || !Array.isArray(entry.haystack_session_ids) ||
        !Array.isArray(entry.haystack_dates) || !Array.isArray(entry.answer_session_ids) ||
        entry.haystack_sessions.length !== entry.haystack_session_ids.length ||
        entry.haystack_sessions.length !== entry.haystack_dates.length) {
      fail('benchmark_dataset_query_invalid');
    }
    const answerSessions = new Set(entry.answer_session_ids);
    const distractorIDs = entry.haystack_session_ids
      .filter((id) => !answerSessions.has(id))
      .sort((left, right) => sha256(`${entry.question_id}\0${left}`).localeCompare(sha256(`${entry.question_id}\0${right}`)))
      .slice(0, 4);
    const included = new Set([...answerSessions, ...distractorIDs]);
    const items = [];
    const expectedIDs = [];
    for (let sessionIndex = 0; sessionIndex < entry.haystack_sessions.length; sessionIndex += 1) {
      const sessionID = entry.haystack_session_ids[sessionIndex];
      if (!included.has(sessionID)) continue;
      const session = entry.haystack_sessions[sessionIndex];
      if (!Array.isArray(session)) fail('benchmark_dataset_item_invalid');
      for (let turnIndex = 0; turnIndex < session.length; turnIndex += 1) {
        const turn = session[turnIndex];
        if (!turn || typeof turn.role !== 'string' || typeof turn.content !== 'string') {
          fail('benchmark_dataset_item_invalid');
        }
        const id = `${sessionID}:${turnIndex}`;
        items.push({
          id, kind: 'project_state', scope: 'project',
          summary: turnSummary({ date: entry.haystack_dates[sessionIndex], role: turn.role, content: turn.content }),
        });
        if (answerSessions.has(sessionID)) expectedIDs.push(id);
      }
    }
    if (expectedIDs.length === 0) fail('benchmark_dataset_expected_missing');
    return {
      id: entry.question_id,
      items,
      batch_size: 3,
      queries: [{
        id: entry.question_id,
        query: entry.question,
        expectation: 'hit',
        expected_ids: expectedIDs,
        category: entry.question_type,
        gold_answer: String(entry.answer ?? ''),
        question_date: String(entry.question_date ?? ''),
      }],
    };
  });
  return {
    cases,
    excluded: [{
      cases: document.length - cases.length,
      reason: 'bounded_stratified_retrieval_run',
    }, {
      sessions_per_case: 'all_answer_sessions_plus_4_distractor_sessions',
      reason: 'oracle_extraction_ceiling_not_end_to_end_answer_accuracy',
    }],
  };
}

function numericSessionEntries(conversation) {
  return Object.entries(conversation)
    .filter(([name, value]) => /^session_\d+$/.test(name) && Array.isArray(value))
    .sort((left, right) => Number(left[0].slice(8)) - Number(right[0].slice(8)));
}

function normalizedLoCoMoEvidence(values) {
  const ids = [];
  for (const raw of values) {
    const repaired = String(raw).replace(/D:(\d+):(\d+)/g, 'D$1:$2');
    ids.push(...(repaired.match(/D\d+:\d+/g) ?? []));
  }
  return [...new Set(ids)];
}

function loadLoCoMoSuite(path) {
  const document = readJSON(path, 'benchmark_dataset_invalid');
  if (!Array.isArray(document)) fail('benchmark_dataset_invalid');
  let excludedWithoutEvidence = 0;
  let excludedMissingEvidence = 0;
  const cases = document.map((sample) => {
    if (typeof sample?.sample_id !== 'string' || !sample.conversation || !Array.isArray(sample.qa)) {
      fail('benchmark_dataset_invalid');
    }
    const items = [];
    const itemIDs = new Set();
    for (const [sessionName, turns] of numericSessionEntries(sample.conversation)) {
      const date = String(sample.conversation[`${sessionName}_date_time`] ?? '');
      for (const turn of turns) {
        if (typeof turn?.dia_id !== 'string' || typeof turn.speaker !== 'string' || typeof turn.text !== 'string') {
          fail('benchmark_dataset_item_invalid');
        }
        if (itemIDs.has(turn.dia_id)) fail('benchmark_dataset_item_invalid');
        itemIDs.add(turn.dia_id);
        const imageContext = typeof turn.blip_caption === 'string' && turn.blip_caption.trim() !== ''
          ? ` Image: ${turn.blip_caption.trim()}` : '';
        items.push({
          id: turn.dia_id,
          kind: 'project_state',
          scope: 'project',
          summary: turnSummary({ date, role: turn.speaker, content: `${turn.text}${imageContext}` }),
        });
      }
    }
    const queries = [];
    const datedSessions = numericSessionEntries(sample.conversation)
      .map(([sessionName]) => String(sample.conversation[`${sessionName}_date_time`] ?? ''))
      .filter(Boolean);
    const referenceDate = datedSessions.at(-1) ?? '';
    for (let index = 0; index < sample.qa.length; index += 1) {
      const qa = sample.qa[index];
      if (![1, 2, 3, 4].includes(qa?.category) || typeof qa.question !== 'string' || !Array.isArray(qa.evidence)) continue;
      const evidence = normalizedLoCoMoEvidence(qa.evidence);
      if (evidence.length === 0) {
        excludedWithoutEvidence += 1;
        continue;
      }
      const existing = evidence.filter((id) => itemIDs.has(id));
      if (existing.length === 0) {
        excludedMissingEvidence += 1;
        continue;
      }
      queries.push({
        id: `${sample.sample_id}-q${index + 1}`,
        query: qa.question,
        expectation: 'hit',
        expected_ids: existing,
        category: String(qa.category),
        gold_answer: String(qa.answer ?? ''),
        reference_date: referenceDate,
      });
    }
    return { id: sample.sample_id, items, queries, batch_size: 3 };
  });
  return {
    cases,
    excluded: [
      { cases: document.reduce((sum, sample) => sum + sample.qa.filter((qa) => qa.category === 5).length, 0), reason: 'adversarial_category_5_outside_published_1540_set' },
      { cases: excludedWithoutEvidence, reason: 'official_evidence_missing' },
      { cases: excludedMissingEvidence, reason: 'official_evidence_id_not_found' },
      { reason: 'raw_turn_retrieval_not_end_to_end_answer_accuracy' },
    ],
  };
}

function loadExtractedLoCoMoSuite(path) {
  const document = readJSON(path, 'benchmark_dataset_invalid', 512 * 1024 * 1024);
  if (document?.schema !== 'pulse.benchmark_extracted_memory.v1' || document.suite !== 'locomo' ||
      !Array.isArray(document.cases)) fail('benchmark_dataset_invalid');
  const cases = document.cases.map((entry) => {
    if (typeof entry?.id !== 'string' || !Array.isArray(entry.items) || !Array.isArray(entry.queries)) {
      fail('benchmark_dataset_invalid');
    }
    const itemIDs = new Set();
    const items = entry.items.map((item) => {
      if (typeof item?.id !== 'string' || itemIDs.has(item.id) || typeof item.summary !== 'string' ||
          item.summary.trim() === '' || [...item.summary].length > 400 || !Array.isArray(item.source_ids)) {
        fail('benchmark_dataset_item_invalid');
      }
      itemIDs.add(item.id);
      return {
        id: item.id, kind: productKind(item.kind), scope: 'project', summary: item.summary,
        source_ids: item.source_ids,
      };
    });
    const queries = entry.queries.map((query) => {
      if (typeof query?.id !== 'string' || typeof query.query !== 'string' || !Array.isArray(query.expected_ids) ||
          query.expected_ids.some((id) => !itemIDs.has(id))) fail('benchmark_dataset_query_invalid');
      return {
        id: query.id,
        query: query.query,
        expectation: 'hit',
        expected_ids: query.expected_ids,
        category: String(query.category),
        gold_answer: String(query.gold_answer ?? ''),
        reference_date: String(query.reference_date ?? ''),
        evidence_ids: query.evidence_ids ?? [],
      };
    });
    return { id: entry.id, items, queries, batch_size: 3 };
  });
  return {
    cases,
    excluded: [{ reason: 'model_extracted_atomic_memory_product_path' }],
    extraction: document.extraction,
  };
}

function loadExtractedLongMemEvalSuite(path) {
  const document = readJSON(path, 'benchmark_dataset_invalid', 512 * 1024 * 1024);
  if (document?.schema !== 'pulse.benchmark_extracted_memory.v1' || document.suite !== 'longmemeval' ||
      !Array.isArray(document.cases)) fail('benchmark_dataset_invalid');
  const cases = document.cases.map((entry) => {
    if (typeof entry?.id !== 'string' || !Array.isArray(entry.items) || !Array.isArray(entry.queries) ||
        entry.queries.length !== 1) fail('benchmark_dataset_invalid');
    const itemIDs = new Set();
    const items = entry.items.map((item) => {
      if (typeof item?.id !== 'string' || itemIDs.has(item.id) || typeof item.summary !== 'string' ||
          item.summary.trim() === '' || [...item.summary].length > 400 || !Array.isArray(item.source_ids)) {
        fail('benchmark_dataset_item_invalid');
      }
      itemIDs.add(item.id);
      return {
        id: item.id, kind: productKind(item.kind), scope: 'project', summary: item.summary,
        source_ids: item.source_ids,
      };
    });
    const queries = entry.queries.map((query) => {
      if (typeof query?.id !== 'string' || typeof query.query !== 'string' || !Array.isArray(query.expected_ids) ||
          query.expected_ids.some((id) => !itemIDs.has(id))) fail('benchmark_dataset_query_invalid');
      return {
        id: query.id, query: query.query, expectation: 'hit', expected_ids: query.expected_ids,
        category: String(query.category), gold_answer: String(query.gold_answer ?? ''),
        question_date: String(query.question_date ?? ''), evidence_ids: query.evidence_ids ?? [],
      };
    });
    return { id: entry.id, items, queries, batch_size: 3 };
  });
  return {
    cases, excluded: [{ reason: 'model_extracted_atomic_memory_product_path' }], extraction: document.extraction,
  };
}

function loadExtractedEmoBenchSuite(path) {
  const document = readJSON(path, 'benchmark_dataset_invalid', 64 * 1024 * 1024);
  if (document?.schema !== 'pulse.benchmark_extracted_memory.v1' || document.suite !== 'emobench' ||
      !Array.isArray(document.cases)) fail('benchmark_dataset_invalid');
  const cases = document.cases.map((entry) => {
    if (typeof entry?.id !== 'string' || !new Set(['development', 'holdout']).has(entry.split) ||
        !Array.isArray(entry.items) || !Array.isArray(entry.queries)) fail('benchmark_dataset_invalid');
    const itemIDs = new Set();
    const items = entry.items.map((item) => {
      if (typeof item?.id !== 'string' || itemIDs.has(item.id) || typeof item.summary !== 'string' ||
          item.summary.trim() === '' || [...item.summary].length > 400 || !Array.isArray(item.source_ids)) {
        fail('benchmark_dataset_item_invalid');
      }
      itemIDs.add(item.id);
      return {
        id: item.id, kind: productKind(item.kind), scope: 'project', summary: item.summary,
        source_ids: item.source_ids,
      };
    });
    const queries = entry.queries.map((query) => {
      if (typeof query?.id !== 'string' || typeof query.query !== 'string' ||
          typeof query.gold_answer !== 'string' || query.split !== entry.split ||
          !new Set(['positive', 'negative']).has(query.query_kind) ||
          !new Set(['hit', 'silence']).has(query.expectation) || !Array.isArray(query.expected_ids) ||
          query.expected_ids.some((id) => !itemIDs.has(id)) ||
          (query.query_kind === 'positive') !== (query.expectation === 'hit')) {
        fail('benchmark_dataset_query_invalid');
      }
      return {
        id: query.id, query: query.query, expectation: query.expectation,
        expected_ids: query.expected_ids, category: query.category,
        gold_answer: query.gold_answer, split: query.split, query_kind: query.query_kind,
      };
    });
    return { id: entry.id, items, queries, batch_size: 3 };
  });
  return {
    cases,
    excluded: [{ reason: 'synthetic_fixed_development_and_holdout_without_hidden_labels_in_search' }],
    extraction: document.extraction,
  };
}

function loadSuite(options) {
  if (options.suite === 'own-v1') return loadOwnSuite(options.dataset);
  if (options.suite === 'emobench-v3-product') return loadEmoBenchSuite(options.dataset);
  if (options.suite === 'longmemeval-s-retrieval-30') return loadLongMemEvalSuite(options.dataset);
  if (options.suite === 'longmemeval-atoms') return loadExtractedLongMemEvalSuite(options.dataset);
  if (options.suite === 'locomo-atoms') return loadExtractedLoCoMoSuite(options.dataset);
  if (options.suite === 'emobench-atoms') return loadExtractedEmoBenchSuite(options.dataset);
  return loadLoCoMoSuite(options.dataset);
}

function limitSuite(suite, maximum) {
  if (maximum === undefined) return suite;
  let remaining = maximum;
  return {
    ...suite,
    cases: suite.cases.map((entry) => {
      const queries = entry.queries.slice(0, Math.max(0, remaining));
      remaining -= queries.length;
      return { ...entry, queries };
    }).filter((entry) => entry.queries.length > 0),
    excluded: [...suite.excluded, { cases: 'remaining', reason: `bounded_to_${maximum}_questions` }],
  };
}

async function freePort() {
  const server = createServer();
  await new Promise((accept, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', accept);
  });
  const port = server.address().port;
  await new Promise((accept) => server.close(accept));
  return port;
}

function initializeRepository(path) {
  mkdirSync(path, { recursive: true, mode: 0o700 });
  run('/usr/bin/git', ['init', '-q'], { cwd: path });
  run('/usr/bin/git', ['config', 'user.email', 'pulse-benchmark@example.test'], { cwd: path });
  run('/usr/bin/git', ['config', 'user.name', 'Pulse Benchmark'], { cwd: path });
  run('/usr/bin/git', ['commit', '--allow-empty', '-q', '-m', 'fixture'], { cwd: path });
}

async function resolvePublishedPackage(root, version) {
  const metadataURL = `https://registry.npmjs.org/${encodeURIComponent(PACKAGE_NAME)}/${version}`;
  const response = await fetch(metadataURL, { signal: AbortSignal.timeout(30_000) });
  if (!response.ok) fail('benchmark_package_metadata_unavailable', String(response.status));
  const metadata = await response.json();
  if (metadata?.name !== PACKAGE_NAME || metadata?.version !== version ||
      typeof metadata?.dist?.tarball !== 'string' || !metadata.dist.tarball.startsWith('https://registry.npmjs.org/') ||
      typeof metadata?.dist?.integrity !== 'string' || !metadata.dist.integrity.startsWith('sha512-')) {
    fail('benchmark_package_metadata_invalid');
  }
  const archiveResponse = await fetch(metadata.dist.tarball, { signal: AbortSignal.timeout(120_000) });
  if (!archiveResponse.ok) fail('benchmark_package_download_failed', String(archiveResponse.status));
  const archive = Buffer.from(await archiveResponse.arrayBuffer());
  if (createHash('sha512').update(archive).digest('base64') !== metadata.dist.integrity.slice(7)) {
    fail('benchmark_package_integrity_mismatch');
  }
  const archivePath = join(root, `pulse-${version}.tgz`);
  writeFileSync(archivePath, archive, { mode: 0o600, flag: 'wx' });
  const installRoot = join(root, 'npm-install');
  mkdirSync(installRoot, { mode: 0o700 });
  const npmCLI = process.env.PULSE_BENCHMARK_NPM_CLI || '/opt/homebrew/lib/node_modules/npm/bin/npm-cli.js';
  if (!isAbsolute(npmCLI) || !existsSync(npmCLI)) fail('benchmark_npm_unavailable');
  const installStarted = performance.now();
  run(process.execPath, [npmCLI, 'install', '--prefix', installRoot, '--ignore-scripts', '--no-audit', '--no-fund',
    '--omit=dev', archivePath], { timeout: 180_000 });
  const installElapsed = Math.round((performance.now() - installStarted) * 10) / 10;
  const packageRoot = join(installRoot, 'node_modules', '@zbs-gg', 'pulse');
  const packageJSON = readJSON(join(packageRoot, 'package.json'), 'benchmark_package_invalid', 64 * 1024);
  if (packageJSON.name !== PACKAGE_NAME || packageJSON.version !== version) fail('benchmark_package_invalid');
  return {
    root: packageRoot,
    archive_bytes: archive.length,
    archive_sha256: sha256(archive),
    unpacked_physical_bytes: physicalTreeBytes(packageRoot),
    installed_dependency_tree_physical_bytes: physicalTreeBytes(join(installRoot, 'node_modules')),
    npm_install_elapsed_ms: installElapsed,
    version,
  };
}

function activeReleaseProof(packageRoot, version, { compatibleActiveRuntime = false } = {}) {
  const pulseRoot = join(process.env.HOME ?? '', '.pulse');
  const activation = readJSON(join(pulseRoot, 'runtime', 'product-daemon.json'), 'benchmark_activation_invalid');
  const activeSet = readJSON(join(pulseRoot, 'artifacts', 'active-release.json'), 'benchmark_release_set_invalid');
  const envelope = readJSON(join(packageRoot, 'release', 'personal-preview-manifest.json'), 'benchmark_manifest_invalid', 2 * 1024 * 1024);
  const payload = envelope?.payload;
  const target = payload?.targets?.[`${process.platform}-${process.arch}`]?.artifacts;
  const common = payload?.common_artifacts;
  const packageIdentityValid = process.platform === 'darwin' && process.arch === 'arm64' &&
      payload?.release?.version === version && Number.isSafeInteger(payload?.release?.epoch);
  const exactActivation = activation.release_version === version && activeSet.version === version &&
      activeSet.epoch === activation.release_epoch && payload?.release?.epoch === activation.release_epoch;
  const compatibleActivation = compatibleActiveRuntime && !exactActivation &&
      target?.daemon?.sha256 === activation.daemon_artifact_sha256 &&
      target?.['embedder-runtime']?.sha256 === activation.embedder_runtime_artifact_sha256 &&
      common?.model?.sha256 === activation.model_artifact_sha256;
  if (!packageIdentityValid || (!exactActivation && !compatibleActivation) ||
      target?.daemon?.sha256 !== activation.daemon_artifact_sha256 ||
      target?.['embedder-runtime']?.sha256 !== activation.embedder_runtime_artifact_sha256 ||
      common?.model?.sha256 !== activation.model_artifact_sha256 ||
      (exactActivation && common?.['plugin-runtime']?.sha256 !== activation.plugin_runtime_artifact_sha256) ||
      sha256File(activation.daemon_path) !== activation.daemon_digest) {
    fail('benchmark_release_identity_mismatch');
  }
  const required = exactActivation
    ? ['daemon', 'embedder-runtime', 'model', 'plugin-runtime']
    : ['daemon', 'embedder-runtime', 'model'];
  const activePaths = required.map((kind) => activeSet.activations?.[kind]?.version_path);
  if (activePaths.some((path) => typeof path !== 'string' || !isAbsolute(path) || !existsSync(path))) {
    fail('benchmark_release_artifact_missing');
  }
  return {
    activation: exactActivation ? activation : {
      ...activation,
      release_epoch: payload.release.epoch,
      release_version: version,
    },
    runtime_proof: exactActivation ? {
      kind: 'exact_active_release', active_version: activation.release_version,
      active_epoch: activation.release_epoch,
    } : {
      kind: 'published_package_with_byte_identical_active_native_artifacts',
      active_version: activation.release_version,
      active_epoch: activation.release_epoch,
      unused_package_artifact: 'plugin-runtime',
    },
    active_runtime_physical_bytes: activePaths.reduce((sum, path) => sum + physicalTreeBytes(path), 0),
    artifacts: Object.fromEntries(required.map((kind, index) => [kind, {
      sha256: activeSet.activations[kind].sha256,
      physical_bytes: physicalTreeBytes(activePaths[index]),
    }])),
  };
}

async function createFixtureAuthority({ root, workspaces, port, packageRoot }) {
  const bindingModule = await import(pathToFileURL(join(packageRoot, 'src', 'workspace-binding.js')));
  const storeID = `store_personal_benchmark_${randomBytes(8).toString('hex')}`;
  const home = join(root, 'home');
  const personal = {
    store_id: storeID,
    data_dir: join(home, '.pulse', 'vaults', 'personal', storeID),
    base_url: `http://127.0.0.1:${port}`,
    credential_ref: `keychain:pulse/local/${storeID}`,
    cache_dir: join(home, '.pulse', 'caches', 'personal', storeID),
  };
  const bindings = workspaces.map(({ id, path }) => {
    const identity = bindingModule.canonicalizeWorkspace(path);
    return { id, binding: {
      binding_id: `binding_${randomBytes(10).toString('hex')}`,
      receipt_id: `receipt_${randomBytes(10).toString('hex')}`,
      resolver_epoch: 1,
      workspace: { workspace_id: identity.workspace_id, repository_id: identity.repository_id },
      mode: 'personal',
      principal_ref: 'principal_benchmark',
      personal,
    } };
  });
  const payload = {
    schema: 'pulse.workspace-binding-registry.v1', epoch: 1,
    bindings: bindings.map((entry) => entry.binding)
      .sort((left, right) => left.workspace.workspace_id.localeCompare(right.workspace.workspace_id)),
  };
  const pair = generateKeyPairSync('ed25519');
  const signature = sign(null, Buffer.from(bindingModule.canonicalJSONStringify(payload)), pair.privateKey).toString('base64');
  const trust = join(root, 'trust');
  mkdirSync(trust, { recursive: true, mode: 0o700 });
  const registryPath = join(trust, 'workspace-bindings.json');
  const publicKeyPath = join(trust, 'workspace-bindings.pub.pem');
  const anchorPath = join(trust, 'workspace-bindings.anchor.json');
  const registryBytes = Buffer.from(`${JSON.stringify({ algorithm: 'ed25519', payload, signature })}\n`);
  writeFileSync(registryPath, registryBytes, { mode: 0o600, flag: 'wx' });
  writeFileSync(publicKeyPath, pair.publicKey.export({ type: 'spki', format: 'pem' }), { mode: 0o600, flag: 'wx' });
  const anchor = bindingModule.bindingRegistryAnchor(registryBytes, 1);
  writeFileSync(anchorPath, `${bindingModule.canonicalJSONStringify(anchor)}\n`, { mode: 0o600, flag: 'wx' });
  const resolvedByID = new Map(bindings.map(({ id }, index) => [id, bindingModule.resolveWorkspaceBinding({
    cwd: workspaces[index].path, registryPath, publicKeyPath, anchorPath, rootAnchor: false,
  })]));
  return {
    home, paths: { registryPath, publicKeyPath, anchorPath },
    resolved: resolvedByID.get(workspaces[0].id), resolved_by_id: resolvedByID,
  };
}

function writeManagedEmbedderConfig(vault, releaseProof) {
  const sourceStore = join(process.env.HOME ?? '', '.pulse', 'vaults', 'personal');
  let selected;
  for (const name of readdirSync(sourceStore)) {
    const path = join(sourceStore, name, 'runtime', 'managed-embedder.json');
    if (!existsSync(path)) continue;
    const config = readJSON(path, 'benchmark_embedder_config_invalid', 32 * 1024);
    if (config.embedder_runtime_activation_digest === releaseProof.activation.embedder_runtime_activation_digest &&
        config.model_activation_digest === releaseProof.activation.model_activation_digest) {
      selected = config;
      break;
    }
  }
  if (!selected) fail('benchmark_embedder_config_missing');
  mkdirSync(join(vault, 'runtime'), { recursive: true, mode: 0o700 });
  const path = join(vault, 'runtime', 'managed-embedder.json');
  writeFileSync(path, `${JSON.stringify(selected)}\n`, { mode: 0o600, flag: 'wx' });
  return path;
}

async function waitForDaemon(baseURL, secret, state) {
  const deadline = Date.now() + 90_000;
  let detail = 'not_started';
  while (Date.now() < deadline) {
    if (state.closed) fail('benchmark_daemon_exited', state.stderr.slice(-400));
    try {
      const response = await fetch(`${baseURL}/memory/status`, {
        headers: { 'X-Pulse-Key': secret }, signal: AbortSignal.timeout(1_500),
      });
      if (response.ok) {
        const status = await response.json();
        if (status.full_retrieval === true && status.embedder === 'bge-m3') return;
        detail = 'full_retrieval_pending';
      } else detail = `status_${response.status}`;
    } catch (error) {
      detail = error?.name ?? 'request_failed';
    }
    await new Promise((accept) => setTimeout(accept, 250));
  }
  fail('benchmark_daemon_not_ready', detail);
}

async function startDaemon({ releaseProof, authority, packageRoot, candidateDaemon }) {
  const resolved = authority.resolved;
  const vault = resolved.personal.data_dir;
  mkdirSync(vault, { recursive: true, mode: 0o700 });
  const embedderConfig = writeManagedEmbedderConfig(vault, releaseProof);
  const secret = randomBytes(32).toString('hex');
  writeFileSync(join(vault, 'secret.key'), secret, { mode: 0o600, flag: 'wx' });
  const port = new URL(resolved.personal.base_url).port;
  const env = {
    HOME: authority.home,
    PATH: '',
    PULSE_RUNTIME_MODE: 'personal-local',
    PULSE_VAULT_STORE_ID: resolved.personal.store_id,
    PULSE_BINDING_DIGEST: resolved.binding_digest,
    PULSE_REPOSITORY_ID: resolved.workspace.repository_id,
    PULSE_PRODUCT_WORKSPACE: resolved.workspace.canonical_path,
    PULSE_PRODUCT_AUTHORITY_NODE: process.execPath,
    PULSE_PRODUCT_AUTHORITY_HELPER: join(packageRoot, 'src', 'product-binding-verifier.js'),
    PULSE_PRODUCT_AUTHORITY_TEST_MODE: '1',
    PULSE_TRUST_MODE: 'test',
    PULSE_BINDING_REGISTRY_PATH: authority.paths.registryPath,
    PULSE_BINDING_PUBLIC_KEY_PATH: authority.paths.publicKeyPath,
    PULSE_BINDING_ANCHOR_PATH: authority.paths.anchorPath,
    PULSE_POLICY_EPOCH: '0',
    PULSE_RESOLVER_EPOCH: String(resolved.resolver_epoch),
    PULSE_DATA_DIR: vault,
    PULSE_MANAGED_EMBEDDER_CONFIG: embedderConfig,
    ANTHROPIC_API_KEY: '', OPENAI_API_KEY: '', COHERE_API_KEY: '',
  };
  const daemonPath = candidateDaemon ?? releaseProof.activation.daemon_path;
  const child = spawn(daemonPath, ['-data-dir', vault, '-addr', `127.0.0.1:${port}`], {
    env, stdio: ['ignore', 'pipe', 'pipe'], detached: false,
  });
  const state = { closed: false, stderr: '' };
  child.stderr.setEncoding('utf8');
  child.stderr.on('data', (chunk) => { state.stderr = `${state.stderr}${chunk}`.slice(-16_384); });
  child.once('close', () => { state.closed = true; });
  await waitForDaemon(resolved.personal.base_url, secret, state);
  return { child, state, secret, vault, env };
}

async function stopDaemon(runtime) {
  if (runtime.state.closed) return;
  runtime.child.kill('SIGTERM');
  const stopped = await Promise.race([
    new Promise((accept) => runtime.child.once('close', () => accept(true))),
    new Promise((accept) => setTimeout(() => accept(false), 5_000)),
  ]);
  if (!stopped) runtime.child.kill('SIGKILL');
}

function mcpEnvironment({ runtime, authority, packageRoot }) {
  const resolved = authority.resolved;
  return {
    ...process.env,
    HOME: authority.home,
    PULSE_RUNTIME_MODE: 'local-stdio',
    PULSE_MCP_MODE: 'daemon',
    PULSE_HOST_ADAPTER: 'codex',
    PULSE_BASE_URL: resolved.personal.base_url,
    PULSE_DATA_DIR: runtime.vault,
    PULSE_API_KEY: runtime.secret,
    PULSE_BINDING_DIGEST: resolved.binding_digest,
    PULSE_REPOSITORY_ID: resolved.workspace.repository_id,
    PULSE_RESOLVER_EPOCH: String(resolved.resolver_epoch),
    PULSE_HOST_WORKSPACE: resolved.workspace.canonical_path,
    PULSE_PRODUCT_BINDING_MODE: 'personal',
    PULSE_HOST_AUTHORITY_MODULE: pathToFileURL(join(packageRoot, 'src', 'codex-runtime.js')).href,
    PULSE_HOST_RUNTIME_MODULE: pathToFileURL(join(packageRoot, 'src', 'codex-runtime.js')).href,
    PULSE_PRODUCT_AUTHORITY_TEST_MODE: '1',
    PULSE_TRUST_MODE: 'test',
    PULSE_BINDING_REGISTRY_PATH: authority.paths.registryPath,
    PULSE_BINDING_PUBLIC_KEY_PATH: authority.paths.publicKeyPath,
    PULSE_BINDING_ANCHOR_PATH: authority.paths.anchorPath,
  };
}

async function seedThroughPulseMemory({ items, batchSize, runtime, authority, packageRoot }) {
  const calls = [];
  for (let index = 0; index < items.length; index += batchSize) {
    const id = 2 + calls.length;
    const sourceItems = items.slice(index, index + batchSize);
    const body = { items: sourceItems.map(({ kind, scope, summary, emotion }) => ({
      kind, scope, summary, ...(emotion ? { emotion } : {}),
    })) };
    calls.push({ id, body, source_ids: sourceItems.map((item) => item.id) });
  }
  const started = performance.now();
  const child = spawn(process.execPath, [join(packageRoot, 'vendor', 'pulse-mcp-dist', 'index.js')], {
    cwd: authority.resolved.workspace.canonical_path,
    env: mcpEnvironment({ runtime, authority, packageRoot }),
    stdio: ['pipe', 'pipe', 'pipe'],
  });
  child.stdout.setEncoding('utf8');
  child.stderr.setEncoding('utf8');
  let stdout = '';
  let stderr = '';
  let exited = false;
  let exitCode = null;
  const pending = new Map();
  const settleLine = (line) => {
    if (!line.trim()) return;
    let message;
    try { message = JSON.parse(line); } catch { return; }
    const waiter = pending.get(message.id);
    if (!waiter) return;
    pending.delete(message.id);
    clearTimeout(waiter.timer);
    waiter.resolve(message);
  };
  child.stdout.on('data', (chunk) => {
    stdout += chunk;
    const lines = stdout.split(/\r?\n/);
    stdout = lines.pop() ?? '';
    for (const line of lines) settleLine(line);
  });
  child.stderr.on('data', (chunk) => { stderr = `${stderr}${chunk}`.slice(-16_384); });
  child.once('exit', (code) => {
    exited = true;
    exitCode = code;
    for (const waiter of pending.values()) {
      clearTimeout(waiter.timer);
      waiter.reject(new Error(`benchmark_mcp_exited:${code}:${stderr.slice(-240)}`));
    }
    pending.clear();
  });
  const send = (message, timeoutMs = 30_000) => new Promise((resolveMessage, rejectMessage) => {
    if (exited) {
      rejectMessage(new Error(`benchmark_mcp_exited:${exitCode}`));
      return;
    }
    const timer = setTimeout(() => {
      pending.delete(message.id);
      rejectMessage(new Error(`benchmark_mcp_timeout:${message.id}`));
    }, timeoutMs);
    pending.set(message.id, { resolve: resolveMessage, reject: rejectMessage, timer });
    child.stdin.write(`${JSON.stringify(message)}\n`, (error) => {
      if (!error) return;
      clearTimeout(timer);
      pending.delete(message.id);
      rejectMessage(error);
    });
  });
  await send({ jsonrpc: '2.0', id: 1, method: 'initialize', params: {
    protocolVersion: '2024-11-05', capabilities: {}, clientInfo: { name: 'pulse-product-benchmark', version: '1' },
  } });
  child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', method: 'notifications/initialized', params: {} })}\n`);
  const storedIDs = [];
  const rejectedIDs = [];
  for (const call of calls) {
    const response = await send({
      jsonrpc: '2.0', id: call.id, method: 'tools/call',
      params: { name: 'pulse_memory', arguments: call.body },
    });
    if (response?.error || !response?.result?.content?.[0]?.text) {
      const detail = response?.error?.message ?? response?.result?.content?.[0]?.text ?? String(call.id);
      fail('benchmark_memory_write_failed', `${call.id}:${String(detail).slice(0, 240)}`);
    }
    let receipt;
    try { receipt = JSON.parse(response.result.content[0].text); } catch {
      fail('benchmark_memory_write_failed', `${call.id}:${response.result.content[0].text.slice(0, 240)}`);
    }
    if (receipt.status === 'rejected' && Array.isArray(receipt.ids) && receipt.ids.length === 0) {
      rejectedIDs.push(...call.source_ids);
      continue;
    }
    if (receipt.status !== 'stored' || !Array.isArray(receipt.ids) || receipt.ids.length !== call.body.items.length) {
      fail('benchmark_memory_write_failed', `${call.id}:${JSON.stringify(receipt).slice(0, 240)}`);
    }
    storedIDs.push(...receipt.ids);
  }
  child.stdin.end();
  await Promise.race([
    new Promise((accept) => child.once('exit', accept)),
    new Promise((accept) => setTimeout(() => { child.kill('SIGTERM'); accept(); }, 5_000)),
  ]);
  const elapsed = Math.round((performance.now() - started) * 10) / 10;
  return {
    elapsed_ms: elapsed, calls: calls.length, stored_items: storedIDs.length,
    rejected_items: rejectedIDs.length, rejected_ids: rejectedIDs,
  };
}

function bindingHeaders(binding) {
  return {
    'X-Pulse-Product-Workspace': Buffer.from(binding.workspace.canonical_path, 'utf8').toString('base64url'),
    'X-Pulse-Product-Binding': binding.binding_digest,
    'X-Pulse-Product-Repository': binding.workspace.repository_id,
    'X-Pulse-Product-Resolver-Epoch': String(binding.resolver_epoch),
  };
}

function boundRequest(runtime) {
  return async (resolved, route, options = {}) => {
    const method = options.method ?? 'POST';
    const response = await fetch(`${resolved.runtime.base_url}${route}`, {
      method,
      headers: {
        Accept: 'application/json',
        'X-Pulse-Key': runtime.secret,
        ...bindingHeaders(resolved.binding),
        ...(method === 'GET' ? {} : { 'Content-Type': 'application/json' }),
        ...(options.idempotencyKey ? { 'Idempotency-Key': options.idempotencyKey } : {}),
      },
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      signal: AbortSignal.timeout(options.timeoutMs ?? 2_500),
    });
    const text = await response.text();
    if (!response.ok) fail('benchmark_product_request_failed', `${response.status}:${text.slice(0, 160)}`);
    return text === '' ? { ok: true } : JSON.parse(text);
  };
}

function parseContext(value) {
  if (value === '') return [];
  const lines = value.split('\n');
  if (lines.shift() !== CONTEXT_HEADER || lines.some((line) => !line.startsWith('- '))) {
    fail('benchmark_context_invalid');
  }
  return lines.map((line) => line.slice(2));
}

function quantile(values, percentile) {
  if (values.length === 0) return null;
  const sorted = [...values].sort((left, right) => left - right);
  const index = Math.ceil(percentile / 100 * sorted.length) - 1;
  return sorted[Math.max(0, Math.min(sorted.length - 1, index))];
}

async function querySuite({ suite, runtime, authority, packageRoot, compositorPath }) {
  const { composePromptMemoryContext } = await import(pathToFileURL(compositorPath));
  const resolved = {
    binding: authority.resolved,
    runtime: { base_url: authority.resolved.personal.base_url, data_dir: runtime.vault },
  };
  const request = boundRequest(runtime);
  const summaries = new Map(suite.items.map((item) => [item.id, item.summary]));
  const results = [];
  for (const item of suite.queries) {
    const started = performance.now();
    let context = '';
    let errorCode = null;
    let rawQuery = null;
    try {
      const observingRequest = async (...args) => {
        const value = await request(...args);
        if (args[1] === '/context/query') rawQuery = value;
        return value;
      };
      context = await composePromptMemoryContext(resolved, item.query, {
        request: observingRequest, recordActivity: async () => {},
      });
    } catch (error) {
      errorCode = String(error?.message ?? 'query_failed').split(':', 1)[0].slice(0, 80);
    }
    const elapsed = Math.round((performance.now() - started) * 10) / 10;
    const returned = errorCode ? [] : parseContext(context);
    const expected = item.expected_ids.map((id) => summaries.get(id));
    const expectedRank = returned.findIndex((summary) => expected.includes(summary));
    const passed = errorCode === null && (item.expectation === 'silence' ? returned.length === 0 : expectedRank >= 0);
    const events = Array.isArray(rawQuery?.events) ? rawQuery.events : [];
    const breakdowns = rawQuery?.trace?.retrieval?.score_breakdowns ?? {};
    const evidence = rawQuery?.trace?.retrieval?.candidate_evidence ?? {};
    const selected = returned.map((summary) => {
      const event = events.find((candidate) => boundedSummary(candidate.summary || candidate.title || '') === summary);
      const id = String(event?.id ?? '');
      return {
        id_digest: sha256(id),
        cosine: Number.isFinite(Number(breakdowns[id]?.cosine)) ? Number(breakdowns[id].cosine) : null,
        direct_capsule: evidence[id]?.direct_capsule === true,
        dense: evidence[id]?.dense === true,
        lexical: evidence[id]?.lexical === true,
      };
    });
    results.push({
      id: item.id,
      category: item.category ?? null,
      expectation: item.expectation,
      passed,
      expected_candidate_count: item.expected_ids.length,
      retrieval_expected_hit: expectedRank >= 0,
      expected_rank: expectedRank < 0 ? null : expectedRank + 1,
      returned_count: returned.length,
      returned_digests: returned.map((summary) => sha256(summary)),
      selected_evidence: selected,
      elapsed_ms: elapsed,
      context_bytes: Buffer.byteLength(context, 'utf8'),
      estimated_tokens: Math.ceil(Buffer.byteLength(context, 'utf8') / 4),
      error_code: errorCode,
      _e2e: {
        question: item.query,
        gold_answer: item.gold_answer ?? null,
        split: item.split ?? null,
        query_kind: item.query_kind ?? null,
        question_date: item.question_date ?? null,
        reference_date: item.reference_date ?? null,
        context,
      },
    });
  }
  return results;
}

function exactQueryMatches(vault, queriesPath) {
  const which = spawnSync('/usr/bin/which', ['rg'], { encoding: 'utf8' });
  if (which.status !== 0) return { available: false, matches: null, digest: null };
  const result = spawnSync(which.stdout.trim(), [
    '-a', '-F', '-f', queriesPath, '--only-matching', '--no-filename', '--no-line-number', vault,
  ], { encoding: 'utf8', maxBuffer: 32 * 1024 * 1024 });
  if (![0, 1].includes(result.status)) fail('benchmark_query_scan_failed');
  const lines = result.stdout.split('\n').filter(Boolean).sort();
  return { available: true, matches: lines.length, digest: sha256(lines.join('\n')) };
}

function sqliteCounts(database) {
  const sql = [
    "SELECT 'events' object,count(*) rows FROM events",
    "UNION ALL SELECT 'capsules',count(*) FROM memory_capsules WHERE status='active'",
    "UNION ALL SELECT 'emotions',count(*) FROM event_emotions",
    "UNION ALL SELECT 'embeddings',count(*) FROM event_embeddings;",
  ].join(' ');
  const rows = JSON.parse(run('/usr/bin/sqlite3', ['-json', database, sql]).stdout);
  return Object.fromEntries(rows.map((row) => [row.object, row.rows]));
}

function aggregateResult({ options, packageProof, releaseProof, suite, writes, results, runtime, persistence, compositorPath }) {
  const hits = results.filter((item) => item.expectation === 'hit');
  const silences = results.filter((item) => item.expectation === 'silence');
  const warm = results.slice(1).map((item) => item.elapsed_ms);
  const categoryNames = [...new Set(results.map((item) => item.category).filter(Boolean))].sort();
  const byCategory = Object.fromEntries(categoryNames.map((category) => {
    const selected = results.filter((item) => item.category === category);
    return [category, {
      cases: selected.length,
      passed: selected.filter((item) => item.passed).length,
      query_errors: selected.filter((item) => item.error_code !== null).length,
    }];
  }));
  const database = join(runtime.vault, 'pulse.db');
  return {
    schema: RESULT_SCHEMA,
    measured_at: new Date().toISOString(),
    product: {
      package: PACKAGE_NAME,
      version: packageProof.version,
      npm_archive_sha256: packageProof.archive_sha256,
      release_epoch: releaseProof.activation.release_epoch,
      daemon_sha256: releaseProof.activation.daemon_digest,
      embedder: 'bge-m3',
      path: 'pulse_memory -> Personal daemon -> automatic prompt context',
      compositor: options.candidate_compositor ? 'source_candidate' : 'published_package',
      compositor_sha256: sha256File(compositorPath),
      runtime_proof: releaseProof.runtime_proof,
    },
    suite: {
      id: options.suite,
      source_sha256: sha256File(options.dataset),
      cases: suite.cases.length,
      source_items: suite.cases.reduce((sum, item) => sum + item.items.length, 0),
      stored_items: writes.stored_items,
      hit_queries: hits.length,
      silence_queries: silences.length,
      excluded: suite.excluded,
    },
    writing: writes,
    retrieval: {
      correct_hits: hits.filter((item) => item.passed).length,
      expected_hits: hits.length,
      evidence_unlinked_hits: hits.filter((item) => item.expected_candidate_count === 0).length,
      retrieval_misses_with_stored_evidence: hits.filter((item) =>
        item.expected_candidate_count > 0 && !item.retrieval_expected_hit).length,
      correct_silences: silences.filter((item) => item.passed).length,
      expected_silences: silences.length,
      query_errors: results.filter((item) => item.error_code !== null).length,
      cold_ms: results[0]?.elapsed_ms ?? null,
      warm_p50_ms: quantile(warm, 50),
      warm_p95_ms: quantile(warm, 95),
      maximum_context_bytes: Math.max(0, ...results.map((item) => item.context_bytes)),
      maximum_estimated_tokens: Math.max(0, ...results.map((item) => item.estimated_tokens)),
      by_category: byCategory,
      cases: results.map(({ _e2e, ...item }) => item),
    },
    storage: {
      npm_archive_bytes: packageProof.archive_bytes,
      npm_unpacked_physical_bytes: packageProof.unpacked_physical_bytes,
      npm_installed_dependency_tree_physical_bytes: packageProof.installed_dependency_tree_physical_bytes,
      npm_install_elapsed_ms: packageProof.npm_install_elapsed_ms,
      shared_runtime_physical_bytes: releaseProof.active_runtime_physical_bytes,
      shared_runtime_artifacts: releaseProof.artifacts,
      benchmark_vault_physical_bytes: physicalTreeBytes(runtime.vault),
      benchmark_database_logical_bytes: lstatSync(database).size,
      database_counts: sqliteCounts(database),
    },
    privacy: {
      query_persistence_checked: persistence.available,
      query_matches_before: persistence.before.matches,
      query_matches_after: persistence.after.matches,
      query_persistence_unchanged: persistence.available &&
        persistence.before.matches === persistence.after.matches &&
        persistence.before.digest === persistence.after.digest,
    },
  };
}

export async function main(argv = process.argv.slice(2)) {
  const options = parseArgs(argv);
  const suite = limitSuite(loadSuite(options), options.max_questions);
  const root = mkdtempSync(join(tmpdir(), 'pulse-product-benchmark-'));
  chmodSync(root, 0o700);
  let runtime;
  try {
    const workspaces = suite.cases.map((item, index) => ({
      id: item.id,
      path: join(root, 'workspaces', `${String(index + 1).padStart(3, '0')}-${sha256(item.id).slice(0, 12)}`),
    }));
    for (const workspace of workspaces) initializeRepository(workspace.path);
    const packageProof = await resolvePublishedPackage(root, options.package_version);
    const releaseProof = activeReleaseProof(packageProof.root, options.package_version, {
      compatibleActiveRuntime: options.compatible_active_runtime === true,
    });
    const port = await freePort();
    const authority = await createFixtureAuthority({ root, workspaces, port, packageRoot: packageProof.root });
    runtime = await startDaemon({
      releaseProof, authority, packageRoot: packageProof.root,
      candidateDaemon: options.candidate_daemon,
    });
    const queriesPath = join(root, 'queries.txt');
    writeFileSync(queriesPath, `${suite.cases.flatMap((item) => item.queries).map((item) => item.query).join('\n')}\n`, {
      mode: 0o600, flag: 'wx',
    });
    const writeResults = [];
    const results = [];
    const compositorPath = options.candidate_compositor
      ? LOCAL_COMPOSITOR
      : join(packageProof.root, 'src', 'product-compositor.js');
    for (const item of suite.cases) {
      const caseAuthority = { ...authority, resolved: authority.resolved_by_id.get(item.id) };
      const writeResult = await seedThroughPulseMemory({
        items: item.items, batchSize: item.batch_size, runtime,
        authority: caseAuthority, packageRoot: packageProof.root,
      });
      writeResults.push({ case_id: item.id, ...writeResult });
    }
    const before = exactQueryMatches(runtime.vault, queriesPath);
    for (const item of suite.cases) {
      const caseAuthority = { ...authority, resolved: authority.resolved_by_id.get(item.id) };
      const caseResults = await querySuite({
        suite: item, runtime, authority: caseAuthority, packageRoot: packageProof.root, compositorPath,
      });
      results.push(...caseResults.map((result) => ({ case_id: item.id, ...result })));
    }
    const after = exactQueryMatches(runtime.vault, queriesPath);
    const writes = {
      elapsed_ms: Math.round(writeResults.reduce((sum, item) => sum + item.elapsed_ms, 0) * 10) / 10,
      calls: writeResults.reduce((sum, item) => sum + item.calls, 0),
      stored_items: writeResults.reduce((sum, item) => sum + item.stored_items, 0),
      rejected_items: writeResults.reduce((sum, item) => sum + item.rejected_items, 0),
      rejected_ids: writeResults.flatMap((item) => item.rejected_ids.map((id) => `${item.case_id}:${id}`)),
    };
    const result = aggregateResult({
      options, packageProof, releaseProof, suite, writes, results, runtime, compositorPath,
      persistence: { available: before.available && after.available, before, after },
    });
    if (options.e2e_input) {
      atomicWriteJSON(options.e2e_input, {
        schema: 'pulse.product_memory_e2e_input.v1',
        suite: options.suite,
        source_sha256: sha256File(options.dataset),
        product: result.product,
        cases: results.map((item) => ({
          question_id: item.id,
          case_id: item.case_id,
          category: item.category,
          question: item._e2e.question,
          gold_answer: item._e2e.gold_answer,
          split: item._e2e.split,
          query_kind: item._e2e.query_kind,
          question_date: item._e2e.question_date,
          reference_date: item._e2e.reference_date,
          memories: parseContext(item._e2e.context),
          evidence_linked_candidate_count: item.expected_candidate_count,
          retrieval_expected_hit: item.retrieval_expected_hit,
          context_bytes: item.context_bytes,
          estimated_tokens: item.estimated_tokens,
          retrieval_error: item.error_code,
        })),
      });
    }
    atomicWriteJSON(options.output, result);
    process.stdout.write(`${JSON.stringify({
      status: 'completed',
      correct_hits: result.retrieval.correct_hits,
      expected_hits: result.retrieval.expected_hits,
      correct_silences: result.retrieval.correct_silences,
      expected_silences: result.retrieval.expected_silences,
      query_errors: result.retrieval.query_errors,
      warm_p95_ms: result.retrieval.warm_p95_ms,
      output: options.output,
    })}\n`);
  } finally {
    if (runtime) await stopDaemon(runtime);
    if (options.keep_workdir) process.stderr.write(`[pulse-benchmark] kept ${root}\n`);
    else rmSync(root, { recursive: true, force: true });
  }
}

const invokedAsMain = process.argv[1] && pathToFileURL(resolve(process.argv[1])).href === import.meta.url;
if (invokedAsMain) {
  main().catch((error) => {
    process.stderr.write(`[pulse-benchmark] ${error?.message ?? 'failed'}\n`);
    process.exitCode = 1;
  });
}
