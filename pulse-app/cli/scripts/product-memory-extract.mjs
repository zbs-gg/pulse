#!/usr/bin/env node

import { createHash } from 'node:crypto';
import {
  chmodSync,
  existsSync,
  lstatSync,
  mkdirSync,
  readFileSync,
  renameSync,
  writeFileSync,
} from 'node:fs';
import { basename, dirname, isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { runBenchmarkModel } from './benchmark-model-runner.mjs';
import { historicalCoverageRepairPrompt } from '../src/codex-subscription-runner.js';
import {
  assertHistoricalIngestManifest,
  codexHistoricalIngestOutputSchemaBytes,
  historicalCoverageRepairLocators,
  mergeHistoricalCoverageRepair,
  normalizeCodexHistoricalIngestManifest,
} from '../src/historical-ingest-protocol.js';

const OUTPUT_SCHEMA = 'pulse.benchmark_extracted_memory.v1';
const CHECKPOINT_SCHEMA = 'pulse.benchmark_extraction_checkpoint.v1';
const MAX_DATASET_BYTES = 512 * 1024 * 1024;
const DEFAULT_CHUNK_BYTES = 16 * 1024;

function fail(code, detail = '') {
  throw new Error(detail ? `${code}:${detail}` : code);
}

function sha256(value) {
  return createHash('sha256').update(value).digest('hex');
}

function readJSON(path, code, maximum = MAX_DATASET_BYTES) {
  let info;
  try { info = lstatSync(path); } catch { fail(code); }
  if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 || info.size < 1 || info.size > maximum) fail(code);
  try { return JSON.parse(readFileSync(path, 'utf8')); } catch { fail(code); }
}

function atomicWriteJSON(path, value) {
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  const temporary = `${path}.${process.pid}.${Date.now()}.new`;
  writeFileSync(temporary, `${JSON.stringify(value, null, 2)}\n`, { flag: 'wx', mode: 0o600 });
  renameSync(temporary, path);
  chmodSync(path, 0o600);
}

function parseArgs(argv) {
  const options = {
    model: 'gpt-5.4', effort: 'low', workers: 4, chunk_bytes: DEFAULT_CHUNK_BYTES,
    protocol: 'benchmark-v2', resume: false,
  };
  for (let index = 0; index < argv.length; index += 1) {
    const name = argv[index];
    if (name === '--resume') {
      options.resume = true;
      continue;
    }
    const value = argv[index + 1];
    if (!name.startsWith('--') || !value || value.startsWith('--')) fail('extract_option_invalid', name);
    options[name.slice(2).replaceAll('-', '_')] = value;
    index += 1;
  }
  for (const name of ['dataset', 'output', 'checkpoint_dir']) {
    if (!isAbsolute(options[name] ?? '') || resolve(options[name]) !== options[name]) fail('extract_path_invalid', name);
  }
  if (!new Set(['locomo', 'longmemeval', 'emobench']).has(options.suite)) fail('extract_suite_invalid');
  if (!new Set(['benchmark-v2', 'product-v3', 'product-v4', 'product-v5']).has(options.protocol)) fail('extract_protocol_invalid');
  options.workers = Number(options.workers);
  options.chunk_bytes = Number(options.chunk_bytes);
  if (!Number.isSafeInteger(options.workers) || options.workers < 1 || options.workers > 12 ||
      !Number.isSafeInteger(options.chunk_bytes) || options.chunk_bytes < 4 * 1024 || options.chunk_bytes > 512 * 1024) {
    fail('extract_limits_invalid');
  }
  if (options.max_cases !== undefined) {
    options.max_cases = Number(options.max_cases);
    if (!Number.isSafeInteger(options.max_cases) || options.max_cases < 1) fail('extract_max_cases_invalid');
  }
  return options;
}

function sessionEntries(conversation) {
  return Object.entries(conversation)
    .filter(([name, value]) => /^session_\d+$/.test(name) && Array.isArray(value))
    .sort((left, right) => Number(left[0].slice(8)) - Number(right[0].slice(8)));
}

function sourceID(sessionName, turn) {
  if (typeof turn?.dia_id === 'string' && /^D\d+:\d+$/.test(turn.dia_id)) return turn.dia_id;
  const session = Number(sessionName.slice(8));
  return `D${session}:0`;
}

export function imageContextForTurn(turn) {
  const query = typeof turn?.query === 'string' ? turn.query.trim() : '';
  const caption = typeof turn?.blip_caption === 'string' ? turn.blip_caption.trim() : '';
  if (query && caption) return `Sharing image - query: ${query}. The image shows: ${caption}`;
  if (query) return `Sharing image - query for: ${query}`;
  if (caption) return `Sharing image that shows: ${caption}`;
  return '';
}

function chunksForSample(sample, maximumBytes) {
  const chunks = [];
  let current = [];
  let currentBytes = 2;
  for (const [sessionName, turns] of sessionEntries(sample.conversation)) {
    const date = String(sample.conversation[`${sessionName}_date_time`] ?? '');
    for (const turn of turns) {
      const image = imageContextForTurn(turn);
      const record = {
        source_id: sourceID(sessionName, turn),
        date,
        speaker: String(turn.speaker ?? ''),
        text: String(turn.text ?? ''),
        ...(image ? { image } : {}),
      };
      const encodedBytes = Buffer.byteLength(JSON.stringify(record));
      if (encodedBytes > maximumBytes) fail('extract_record_too_large', record.source_id);
      if (current.length > 0 && currentBytes + encodedBytes + 1 > maximumBytes) {
        chunks.push(current);
        current = [];
        currentBytes = 2;
      }
      current.push(record);
      currentBytes += encodedBytes + 1;
    }
  }
  if (current.length > 0) chunks.push(current);
  return chunks;
}

function chunksForLongMemEval(sample, maximumBytes) {
  if (!Array.isArray(sample.haystack_sessions) || !Array.isArray(sample.haystack_session_ids) ||
      sample.haystack_sessions.length !== sample.haystack_session_ids.length) fail('extract_dataset_invalid');
  const chunks = [];
  let current = [];
  let currentBytes = 2;
  for (let sessionIndex = 0; sessionIndex < sample.haystack_sessions.length; sessionIndex += 1) {
    const messages = sample.haystack_sessions[sessionIndex];
    if (!Array.isArray(messages)) fail('extract_dataset_invalid');
    const date = String(sample.haystack_dates?.[sessionIndex] ?? '');
    for (let messageIndex = 0; messageIndex < messages.length; messageIndex += 1) {
      const message = messages[messageIndex];
      const text = String(message?.content ?? '').trim();
      // Match the installed Codex-history parser: oversized individual message
      // parts are not sent to the extraction model.
      if (text === '' || Buffer.byteLength(text) > 32 * 1024) continue;
      const record = {
        source_id: `L${sessionIndex + 1}:${messageIndex + 1}`,
        date,
        speaker: String(message?.role ?? ''),
        text,
      };
      const encodedBytes = Buffer.byteLength(JSON.stringify(record));
      if (encodedBytes > maximumBytes) fail('extract_record_too_large');
      if (current.length > 0 && currentBytes + encodedBytes + 1 > maximumBytes) {
        chunks.push(current);
        current = [];
        currentBytes = 2;
      }
      current.push(record);
      currentBytes += encodedBytes + 1;
    }
  }
  if (current.length > 0) chunks.push(current);
  return chunks;
}

function chunksForEmoBench(sample, maximumBytes) {
  if (!Array.isArray(sample?.records) || sample.records.length < 1) fail('extract_dataset_invalid');
  const chunks = [];
  let current = [];
  let currentBytes = 2;
  const seen = new Set();
  for (const raw of sample.records) {
    if (typeof raw?.source_id !== 'string' || !/^[A-Za-z0-9._-]{1,80}$/.test(raw.source_id) ||
        seen.has(raw.source_id) || typeof raw.date !== 'string' || typeof raw.speaker !== 'string' ||
        typeof raw.text !== 'string' || raw.text.trim() === '') fail('extract_dataset_invalid');
    seen.add(raw.source_id);
    const record = {
      source_id: raw.source_id,
      date: raw.date,
      speaker: raw.speaker,
      text: raw.text,
    };
    const encodedBytes = Buffer.byteLength(JSON.stringify(record));
    if (encodedBytes > maximumBytes) fail('extract_record_too_large', record.source_id);
    if (current.length > 0 && currentBytes + encodedBytes + 1 > maximumBytes) {
      chunks.push(current);
      current = [];
      currentBytes = 2;
    }
    current.push(record);
    currentBytes += encodedBytes + 1;
  }
  if (current.length > 0) chunks.push(current);
  return chunks;
}

function extractionPrompt(records) {
  return `You are the archive-to-memory stage of Pulse, a personal AI memory product. Convert the supplied conversation records into compact atomic memories.

Rules:
- Extract exhaustively rather than summarizing the chunk. Process every record and emit every non-trivial assertion that could answer a later question: identity, relationship or family status, likes and dislikes, personal history, dates, quantities, places, completed or planned events, durable decisions, corrections or updates, open questions, and durable project state.
- Keep distinct details as distinct memories even when they concern the same person or event. Do not omit a supported fact merely because a more general memory overlaps it.
- Each summary must express one independently retrievable fact in at most 400 characters.
- Every summary must name the person or entity it describes. Never use ambiguous pronouns such as "he", "she", "they", or "it" without the name.
- Preserve exact names, dates, quantities, titles, places, negation, and whether something was planned, attempted, completed, rejected, or corrected.
- Include the conversation date when it matters for ordering, updates, age, or temporal questions.
- When a later record in this chunk updates the same fact, emit the latest fact as a correction and do not repeat the stale version as current.
- Assistant advice is not a personal fact unless a later record says the person adopted or did it.
- Omit greetings, generic encouragement, repeated wording, transient small talk, and facts that are only common knowledge.
- Do not emit separate person, project, or relationship nodes. Pulse stores memory atoms; another product owns graphs.
- source_ids must cite only the supplied records that support the memory. Never invent a source id.
- Return only the JSON object required by the output schema.

Conversation records:
${JSON.stringify(records)}`;
}

function productTimestamp(value) {
  const input = String(value ?? '').trim();
  if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(input)) {
    const parsed = new Date(input);
    if (!Number.isNaN(parsed.valueOf())) return parsed.toISOString();
  }
  let match = /^(\d{1,2}):(\d{2})\s+(am|pm)\s+on\s+(\d{1,2})\s+([A-Za-z]+),\s+(\d{4})$/i.exec(input);
  if (match) {
    const months = new Map(['january','february','march','april','may','june','july','august','september','october','november','december']
      .map((name, index) => [name, index]));
    let hour = Number(match[1]) % 12;
    if (match[3].toLowerCase() === 'pm') hour += 12;
    const month = months.get(match[5].toLowerCase());
    if (month !== undefined) return new Date(Date.UTC(Number(match[6]), month, Number(match[4]), hour, Number(match[2]))).toISOString();
  }
  match = /^(\d{4})\/(\d{2})\/(\d{2})\s+\([A-Za-z]{3}\)\s+(\d{2}):(\d{2})$/.exec(input);
  if (match) return new Date(Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3]), Number(match[4]), Number(match[5]))).toISOString();
  fail('extract_timestamp_invalid');
}

function productExtractionInput(task, protocol) {
  const alias = `source_${sha256(`source\0${task.case_id}`).slice(0, 32)}`;
  const prefixDigest = sha256(JSON.stringify(task.records));
  const jobID = `job_${sha256(`job\0${task.case_id}`).slice(0, 32)}`;
  const snapshotDigest = sha256(`snapshot\0${task.case_id}`);
  const evidence = {
    root_id: `root_${sha256(task.case_id).slice(0, 24)}`,
    ordinal: task.ordinal,
    sources: [{ alias, prefix_digest: prefixDigest }],
    records: task.records.map((record) => ({
      source_alias: alias,
      locator: record.source_id,
      timestamp: productTimestamp(record.date),
      kind: 'response_item',
      role: record.speaker,
      // LoCoMo provides a human-readable caption alongside some image turns.
      // It is evidence text, not an attachment byte stream, so pass it through
      // the same product input that imported textual descriptions use.
      text: record.image
        ? `${record.text}\n[Image description: ${record.image}]`
        : record.text,
    })),
  };
  const promptVersion = protocol === 'product-v5' ? 'v5' : protocol === 'product-v4' ? 'v4' : 'v3';
  const templatePath = resolve(dirname(fileURLToPath(import.meta.url)), `../../internal/historicalingest/historical_prompt_${promptVersion}.txt`);
  const template = readFileSync(templatePath, 'utf8');
  const prompt = template.replace('%q', JSON.stringify(jobID)).replace('%q', JSON.stringify(snapshotDigest));
  return {
    evidence: JSON.stringify(evidence), jobID, prompt, snapshotDigest,
    sourceRefsByLocator: new Map(task.records.map((record) => [record.source_id, {
      timestamp: productTimestamp(record.date),
      ref: { alias, prefix_digest: prefixDigest, record_locator: record.source_id },
    }])),
  };
}

function productMemorySummary(item) {
  const payload = item.payload ?? {};
  if (item.kind === 'event') {
    return payload.title && payload.summary && payload.title !== payload.summary
      ? `${payload.title} — ${payload.summary}`.trim() : String(payload.summary ?? '').trim();
  }
  if (item.kind === 'assertion') return String(payload.object_value ?? '').trim();
  return String(payload.summary ?? '').trim();
}

function productMemoryKind(item) {
  if (item.kind === 'decision') return 'decision';
  if (item.kind === 'assertion') return 'fact';
  if (item.kind === 'continuity' && item.payload?.continuity_status === 'open') return 'open_question';
  if (item.kind === 'event') return 'event';
  return 'project_state';
}

function normalizeProductManifest(value, product) {
  return assertHistoricalIngestManifest(normalizeCodexHistoricalIngestManifest(value, {
    sourceRefsByLocator: product.sourceRefsByLocator,
  }), {
    expectedJobID: product.jobID, expectedSnapshotDigest: product.snapshotDigest,
  });
}

function productMemoriesFromManifest(manifest, allowedSources) {
  return manifest.items.map((item) => {
    const summary = productMemorySummary(item).replaceAll(/\s+/g, ' ').trim();
    const sources = [...new Set(item.source_refs.map((ref) => ref.record_locator))];
    if (summary.length < 1 || [...summary].length > 1200 || sources.length < 1 ||
        sources.some((id) => !allowedSources.has(id))) fail('extract_output_invalid');
    return { kind: productMemoryKind(item), summary, source_ids: sources.sort() };
  });
}

async function runExtractionPass({ prompt, input, schemaPath, options }) {
  let result;
  let lastError;
  for (let attempt = 1; attempt <= 3; attempt += 1) {
    try {
      result = await runBenchmarkModel({
        prompt, input, schema: schemaPath,
        model: options.model, effort: options.effort, timeoutMs: 15 * 60_000,
      });
      break;
    } catch (error) {
      lastError = error;
    }
  }
  if (!result) throw lastError;
  return result;
}

function combinedExtractionReceipt(primary, repair, targetCount) {
  if (!repair) return primary;
  const usageKeys = ['input_tokens', 'cached_input_tokens', 'output_tokens', 'reasoning_output_tokens'];
  return {
    ...primary,
    output_digest: sha256(`pulse-coverage-repair-v1\0${primary.output_digest}\0${repair.output_digest}`),
    elapsed_ms: Number(primary.elapsed_ms ?? 0) + Number(repair.elapsed_ms ?? 0),
    usage: Object.fromEntries(usageKeys.map((key) => [key,
      Number(primary.usage?.[key] ?? 0) + Number(repair.usage?.[key] ?? 0),
    ])),
    coverage_repair: { version: 'unreferenced_evidence_v1', target_count: targetCount },
  };
}

function validateMemories(value, allowedSources) {
  if (!value || !Array.isArray(value.memories) || value.memories.length > 256) fail('extract_output_invalid');
  const allowedKinds = new Set(['fact', 'preference', 'event', 'decision', 'correction', 'open_question', 'project_state']);
  return value.memories.map((memory) => {
    const summary = String(memory?.summary ?? '').replaceAll(/\s+/g, ' ').trim();
    const sources = [...new Set(memory?.source_ids ?? [])];
    if (!allowedKinds.has(memory?.kind) || summary.length < 1 || [...summary].length > 400 ||
        sources.length < 1 || sources.length > 12 || sources.some((id) => !allowedSources.has(id))) {
      fail('extract_output_invalid');
    }
    return { kind: memory.kind, summary, source_ids: sources.sort() };
  });
}

function checkpointPath(root, caseID, ordinal) {
  return join(root, `${sha256(`${caseID}\0${ordinal}`).slice(0, 24)}.json`);
}

async function extractChunk({ task, options, schemaPath }) {
  const inputDigest = sha256(JSON.stringify(task.records));
  const product = options.protocol.startsWith('product-') ? productExtractionInput(task, options.protocol) : null;
  const prompt = product?.prompt ?? extractionPrompt(task.records);
  const promptDigest = sha256(`${prompt}\0${sha256(readFileSync(schemaPath))}`);
  const path = checkpointPath(options.checkpoint_dir, task.case_id, task.ordinal);
  if (options.resume && existsSync(path)) {
    const cached = readJSON(path, 'extract_checkpoint_invalid', 4 * 1024 * 1024);
    if (cached.schema !== CHECKPOINT_SCHEMA || cached.case_id !== task.case_id || cached.ordinal !== task.ordinal ||
        (cached.protocol ?? 'benchmark-v2') !== options.protocol ||
        cached.input_digest !== inputDigest || cached.prompt_digest !== promptDigest) fail('extract_checkpoint_stale');
    return cached;
  }
  const allowed = new Set(task.records.map((record) => record.source_id));
  const primary = await runExtractionPass({ prompt, input: product?.evidence ?? '', schemaPath, options });
  let memories;
  let receipt = primary.receipt;
  if (product) {
    let manifest = normalizeProductManifest(primary.value, product);
    const targets = new Set(['product-v4', 'product-v5']).has(options.protocol)
      ? historicalCoverageRepairLocators(manifest, [...allowed]) : [];
    if (targets.length > 0) {
      const repaired = await runExtractionPass({
        prompt: historicalCoverageRepairPrompt(prompt, targets),
        input: product.evidence,
        schemaPath,
        options,
      });
      manifest = mergeHistoricalCoverageRepair(
        manifest, normalizeProductManifest(repaired.value, product), targets,
      );
      receipt = combinedExtractionReceipt(primary.receipt, repaired.receipt, targets.length);
    }
    memories = productMemoriesFromManifest(manifest, allowed);
  } else {
    memories = validateMemories(primary.value, allowed);
  }
  const checkpoint = {
    schema: CHECKPOINT_SCHEMA,
    protocol: options.protocol,
    case_id: task.case_id,
    ordinal: task.ordinal,
    input_digest: inputDigest,
    prompt_digest: promptDigest,
    memories,
    receipt,
  };
  atomicWriteJSON(path, checkpoint);
  return checkpoint;
}

function productKind(kind) {
  if (kind === 'preference') return 'preference';
  if (kind === 'decision') return 'decision';
  if (kind === 'correction') return 'correction';
  if (kind === 'open_question') return 'open_question';
  return 'project_state';
}

function normalizedEvidence(values) {
  const ids = [];
  for (const raw of values ?? []) {
    ids.push(...(String(raw).replace(/D:(\d+):(\d+)/g, 'D$1:$2').match(/D\d+:\d+/g) ?? []));
  }
  return [...new Set(ids)];
}

function buildOutput(options, dataset, extracted) {
  const byCase = new Map();
  for (const checkpoint of extracted) {
    const list = byCase.get(checkpoint.case_id) ?? [];
    list.push(...checkpoint.memories);
    byCase.set(checkpoint.case_id, list);
  }
  const cases = dataset.map((sample) => {
    const seen = new Set();
    const items = [];
    for (const memory of byCase.get(sample.sample_id) ?? []) {
      const digest = sha256(`${memory.kind}\0${memory.summary}`);
      if (seen.has(digest)) continue;
      seen.add(digest);
      items.push({
        id: `atom-${digest.slice(0, 24)}`,
        kind: productKind(memory.kind),
        scope: 'project',
        summary: memory.summary,
        source_ids: memory.source_ids,
      });
    }
    const queries = [];
    for (let index = 0; index < sample.qa.length; index += 1) {
      const qa = sample.qa[index];
      if (![1, 2, 3, 4].includes(qa?.category)) continue;
      const evidence = normalizedEvidence(qa.evidence);
      queries.push({
        id: `${sample.sample_id}-q${index + 1}`,
        query: String(qa.question),
        category: String(qa.category),
        gold_answer: String(qa.answer),
        reference_date: sessionEntries(sample.conversation)
          .map(([name]) => String(sample.conversation[`${name}_date_time`] ?? '')).filter(Boolean).at(-1) ?? '',
        expected_ids: items.filter((item) => item.source_ids.some((id) => evidence.includes(id))).map((item) => item.id),
        evidence_ids: evidence,
      });
    }
    return { id: sample.sample_id, items, queries };
  });
  return {
    schema: OUTPUT_SCHEMA,
    suite: 'locomo',
    source_sha256: sha256(readFileSync(options.dataset)),
    extraction: {
      model: options.model, effort: options.effort,
      prompt_version: options.protocol === 'product-v5' ? 'historical_prompt_v5'
        : options.protocol === 'product-v4' ? 'historical_prompt_v4'
        : options.protocol === 'product-v3' ? 'historical_prompt_v3' : 'pulse_archive_atoms_v2',
      chunk_bytes: options.chunk_bytes,
      chunks: extracted.length,
      memories: cases.reduce((sum, item) => sum + item.items.length, 0),
      usage: extracted.reduce((sum, item) => {
        for (const key of ['input_tokens', 'cached_input_tokens', 'output_tokens', 'reasoning_output_tokens']) {
          sum[key] = (sum[key] ?? 0) + Number(item.receipt?.usage?.[key] ?? 0);
        }
        return sum;
      }, {}),
    },
    cases,
  };
}

function buildLongMemEvalOutput(options, dataset, extracted) {
  const byCase = new Map();
  for (const checkpoint of extracted) {
    const list = byCase.get(checkpoint.case_id) ?? [];
    list.push(...checkpoint.memories);
    byCase.set(checkpoint.case_id, list);
  }
  const cases = dataset.map((sample) => {
    const seen = new Set();
    const items = [];
    for (const memory of byCase.get(sample.question_id) ?? []) {
      const digest = sha256(`${memory.kind}\0${memory.summary}`);
      if (seen.has(digest)) continue;
      seen.add(digest);
      items.push({
        id: `atom-${digest.slice(0, 24)}`,
        kind: productKind(memory.kind),
        scope: 'project',
        summary: memory.summary,
        source_ids: memory.source_ids,
      });
    }
    const answerIndices = new Set((sample.answer_session_ids ?? []).map((sessionID) =>
      sample.haystack_session_ids.indexOf(sessionID)).filter((index) => index >= 0));
    const expectedIDs = items.filter((item) => item.source_ids.some((sourceID) => {
      const match = /^L([0-9]+):/.exec(sourceID);
      return match && answerIndices.has(Number(match[1]) - 1);
    })).map((item) => item.id);
    return {
      id: sample.question_id,
      items,
      queries: [{
        id: sample.question_id,
        query: String(sample.question),
        category: String(sample.question_type),
        gold_answer: String(sample.answer),
        question_date: String(sample.question_date ?? ''),
        expected_ids: expectedIDs,
        evidence_ids: [...(sample.answer_session_ids ?? [])],
      }],
    };
  });
  return {
    schema: OUTPUT_SCHEMA,
    suite: 'longmemeval',
    source_sha256: sha256(readFileSync(options.dataset)),
    extraction: {
      model: options.model, effort: options.effort,
      prompt_version: options.protocol === 'product-v5' ? 'historical_prompt_v5'
        : options.protocol === 'product-v4' ? 'historical_prompt_v4'
        : options.protocol === 'product-v3' ? 'historical_prompt_v3' : 'pulse_archive_atoms_v2',
      chunk_bytes: options.chunk_bytes,
      chunks: extracted.length,
      memories: cases.reduce((sum, item) => sum + item.items.length, 0),
      usage: extracted.reduce((sum, item) => {
        for (const key of ['input_tokens', 'cached_input_tokens', 'output_tokens', 'reasoning_output_tokens']) {
          sum[key] = (sum[key] ?? 0) + Number(item.receipt?.usage?.[key] ?? 0);
        }
        return sum;
      }, {}),
    },
    cases,
  };
}

function buildEmoBenchOutput(options, dataset, extracted) {
  const byCase = new Map();
  for (const checkpoint of extracted) {
    const list = byCase.get(checkpoint.case_id) ?? [];
    list.push(...checkpoint.memories);
    byCase.set(checkpoint.case_id, list);
  }
  const cases = dataset.map((sample) => {
    if (!new Set(['development', 'holdout']).has(sample?.split) || !Array.isArray(sample.questions)) {
      fail('extract_dataset_invalid');
    }
    const seen = new Set();
    const items = [];
    for (const memory of byCase.get(sample.id) ?? []) {
      const digest = sha256(`${memory.kind}\0${memory.summary}`);
      if (seen.has(digest)) continue;
      seen.add(digest);
      items.push({
        id: `atom-${digest.slice(0, 24)}`,
        kind: productKind(memory.kind),
        scope: 'project',
        summary: memory.summary,
        source_ids: memory.source_ids,
      });
    }
    const sourceIDs = new Set(sample.records.map((record) => record.source_id));
    const queries = sample.questions.map((question) => {
      if (typeof question?.id !== 'string' || typeof question.question !== 'string' ||
          typeof question.gold_answer !== 'string' || !new Set(['positive', 'negative']).has(question.kind)) {
        fail('extract_dataset_invalid');
      }
      const evidence = [...new Set(question.evidence_ids ?? [])];
      if (question.kind === 'positive' && (evidence.length < 1 || evidence.some((id) => !sourceIDs.has(id)))) {
        fail('extract_dataset_invalid');
      }
      if (question.kind === 'negative' && evidence.length > 0) fail('extract_dataset_invalid');
      return {
        id: `${sample.id}-${question.id}`,
        query: question.question,
        category: `${sample.split}:${question.kind}`,
        split: sample.split,
        query_kind: question.kind,
        gold_answer: question.gold_answer,
        expectation: question.kind === 'positive' ? 'hit' : 'silence',
        expected_ids: question.kind === 'positive'
          ? items.filter((item) => item.source_ids.some((id) => evidence.includes(id))).map((item) => item.id)
          : [],
        evidence_ids: evidence,
      };
    });
    return { id: sample.id, split: sample.split, items, queries };
  });
  return {
    schema: OUTPUT_SCHEMA,
    suite: 'emobench',
    source_sha256: sha256(readFileSync(options.dataset)),
    extraction: {
      model: options.model, effort: options.effort,
      prompt_version: options.protocol === 'product-v5' ? 'historical_prompt_v5'
        : options.protocol === 'product-v4' ? 'historical_prompt_v4'
        : options.protocol === 'product-v3' ? 'historical_prompt_v3' : 'pulse_archive_atoms_v2',
      chunk_bytes: options.chunk_bytes,
      chunks: extracted.length,
      memories: cases.reduce((sum, item) => sum + item.items.length, 0),
      usage: extracted.reduce((sum, item) => {
        for (const key of ['input_tokens', 'cached_input_tokens', 'output_tokens', 'reasoning_output_tokens']) {
          sum[key] = (sum[key] ?? 0) + Number(item.receipt?.usage?.[key] ?? 0);
        }
        return sum;
      }, {}),
    },
    cases,
  };
}

export async function main(argv = process.argv.slice(2)) {
  const options = parseArgs(argv);
  const document = readJSON(options.dataset, 'extract_dataset_invalid');
  let dataset = options.suite === 'emobench'
    ? document?.schema === 'pulse.emotional_memory_benchmark.v1' && Array.isArray(document.cases) ? document.cases : null
    : document;
  if (!Array.isArray(dataset)) fail('extract_dataset_invalid');
  if (options.max_cases !== undefined) dataset = dataset.slice(0, options.max_cases);
  mkdirSync(options.checkpoint_dir, { recursive: true, mode: 0o700 });
  const tasks = dataset.flatMap((sample) => {
    const caseID = options.suite === 'locomo' ? sample?.sample_id
      : options.suite === 'longmemeval' ? sample?.question_id : sample?.id;
    if (typeof caseID !== 'string' || (options.suite === 'locomo' && (!sample.conversation || !Array.isArray(sample.qa)))) {
      fail('extract_dataset_invalid');
    }
    const chunks = options.suite === 'locomo'
      ? chunksForSample(sample, options.chunk_bytes)
      : options.suite === 'longmemeval'
        ? chunksForLongMemEval(sample, options.chunk_bytes)
        : chunksForEmoBench(sample, options.chunk_bytes);
    return chunks.map((records, ordinal) => ({
      case_id: caseID, ordinal, records,
    }));
  });
  let schemaPath = join(dirname(fileURLToPath(import.meta.url)), 'schemas', 'benchmark-memory-extraction.schema.json');
  if (options.protocol.startsWith('product-')) {
    schemaPath = join(options.checkpoint_dir, 'product-historical-output.schema.json');
    const schemaBytes = codexHistoricalIngestOutputSchemaBytes();
    if (existsSync(schemaPath)) {
      if (!readFileSync(schemaPath).equals(schemaBytes)) fail('extract_product_schema_changed');
    } else {
      writeFileSync(schemaPath, schemaBytes, { mode: 0o600, flag: 'wx' });
    }
  }
  const extracted = [];
  let cursor = 0;
  const workers = Array.from({ length: Math.min(options.workers, tasks.length) }, async () => {
    while (cursor < tasks.length) {
      const task = tasks[cursor++];
      extracted.push(await extractChunk({ task, options, schemaPath }));
    }
  });
  await Promise.all(workers);
  extracted.sort((left, right) => left.case_id.localeCompare(right.case_id) || left.ordinal - right.ordinal);
  const output = options.suite === 'locomo'
    ? buildOutput(options, dataset, extracted)
    : options.suite === 'longmemeval'
      ? buildLongMemEvalOutput(options, dataset, extracted)
      : buildEmoBenchOutput(options, dataset, extracted);
  atomicWriteJSON(options.output, output);
  process.stdout.write(`${JSON.stringify({
    status: 'completed', cases: output.cases.length, chunks: output.extraction.chunks,
    memories: output.extraction.memories, output: options.output,
  })}\n`);
}

if (process.argv[1] && basename(process.argv[1]) === basename(fileURLToPath(import.meta.url))) {
  main().catch((error) => {
    process.stderr.write(`[pulse-extract] ${error?.message ?? error}\n`);
    process.exitCode = 1;
  });
}
