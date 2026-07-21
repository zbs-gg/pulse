const REPORT_SCHEMA = 'pulse.consolidation.report.v1';
const MAX_REPORT_BYTES = 1024 * 1024;
const ID_PATTERN = /^report_[a-zA-Z0-9._-]{1,128}$/;
const STORE_ID_PATTERN = /^store_[a-z0-9][a-z0-9_]{2,127}$/;
const REPOSITORY_PATTERN = /^[a-zA-Z0-9][a-zA-Z0-9._:-]{2,255}$/;
const CODE_PATTERN = /^[a-z][a-z0-9_]{0,127}$/;
const PHASES = new Set(['planned', 'inventory', 'deterministic_dedupe', 'report_ready', 'partial', 'stale', 'cancel_requested', 'canceled']);
const ABSOLUTE_PATH = /(?:^|[\s"'])\/(?:Users|home|var|private|Volumes|workspace)\/|[A-Za-z]:\\/;
const SENSITIVE = /(?:token\s*=|api[_-]?key|authorization\s*:|begin private key|ghp_|xox[baprs]-)/i;

export function reportRequestForArgs(args) {
  if (args[0] !== 'report') throw new Error('not a report command');
  const tail = args.slice(1);
  const action = tail[0] && !tail[0].startsWith('--') ? tail.shift() : 'start';
  let id;
  for (let index = 0; index < tail.length; index += 1) {
    const option = tail[index];
    if (option === '--json') continue;
    if (option !== '--id') throw new Error(`unknown report option: ${option}`);
    if (id !== undefined) throw new Error('duplicate report id');
    const value = tail[index + 1];
    if (value === undefined || value.startsWith('--')) throw new Error('report --id requires <report-id>');
    id = value;
    index += 1;
  }
  if (id !== undefined && !ID_PATTERN.test(id)) throw new Error('invalid report id');
  if (action === 'start') {
    if (id !== undefined) throw new Error('pulse consolidate report start does not accept --id');
    return { action, method: 'POST', path: '/memory/consolidation/reports' };
  }
  if (action === 'status') {
    return id
      ? { action, method: 'GET', path: `/memory/consolidation/reports/${id}` }
      : { action, method: 'GET', path: '/memory/consolidation/reports/latest' };
  }
  if (['cancel', 'resume', 'explain'].includes(action)) {
    if (!id) throw new Error(`pulse consolidate report ${action} requires --id <report-id>`);
    return {
      action,
      method: action === 'explain' ? 'GET' : 'POST',
      path: `/memory/consolidation/reports/${id}/${action}`,
    };
  }
  throw new Error(`unknown report action: ${action}`);
}

export async function requestConsolidationReport(args, { resolved, request }) {
  if (!resolved?.runtime || typeof request !== 'function') {
    throw new Error('Pulse consolidation report requires a bound project runtime');
  }
  const route = reportRequestForArgs(args);
  const result = await request(resolved, route.path, {
    method: route.method,
    body: route.method === 'POST' ? {} : undefined,
  });
  return {
    action: route.action,
    value: route.action === 'explain'
      ? assertConsolidationExplanation(result)
      : assertConsolidationReport(result),
  };
}

export function assertConsolidationReport(value) {
  let encoded;
  try {
    encoded = JSON.stringify(value);
  } catch {
    throw new Error('Pulse consolidation report is not serializable');
  }
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('Pulse consolidation report must be an object');
  if (Buffer.byteLength(encoded, 'utf8') > MAX_REPORT_BYTES || typeof value.next_action !== 'string' || value.next_action.length > 4096) {
    throw new Error('Pulse consolidation report is too large');
  }
  if (value.schema !== REPORT_SCHEMA || value.protocol_version !== 1) throw new Error('Pulse consolidation report schema mismatch');
  if (!ID_PATTERN.test(value.invocation_id ?? '') || !PHASES.has(value.phase) || !Number.isSafeInteger(value.generation) || value.generation < 1) {
    throw new Error('Pulse consolidation report lifecycle is invalid');
  }
  if (!/^[a-f0-9]{64}$/.test(value.input_digest ?? '') || !/^[a-f0-9]{64}$/.test(value.report_digest ?? '')) {
    throw new Error('Pulse consolidation report digest is invalid');
  }
  if (value.inventory_digest !== undefined && !/^[a-f0-9]{64}$/.test(value.inventory_digest)) {
    throw new Error('Pulse consolidation inventory digest is invalid');
  }
  if (!value.destination || !['personal', 'desk'].includes(value.destination.store_kind) ||
      !STORE_ID_PATTERN.test(value.destination.store_id ?? '') ||
      !/^[a-f0-9]{64}$/.test(value.destination.binding_digest ?? '') ||
      !REPOSITORY_PATTERN.test(value.destination.repository_id ?? '')) {
    throw new Error('Pulse consolidation report destination is invalid');
  }
  if (!validTotals(value.totals) || !validSources(value.sources) || !validCodes(value.blockers, 32) ||
      !validCodes(value.reason_codes ?? [], 32)) {
    throw new Error('Pulse consolidation report collections are invalid');
  }
  if (ABSOLUTE_PATH.test(encoded)) throw new Error('Pulse consolidation report contains a local path');
  if (SENSITIVE.test(encoded)) throw new Error('Pulse consolidation report contains sensitive material');
  return value;
}

function validTotals(totals) {
  return totals && ['already_represented', 'unique', 'ambiguous', 'excluded']
    .every((key) => Number.isSafeInteger(totals[key]) && totals[key] >= 0);
}

function validSources(sources) {
  return Array.isArray(sources) && sources.length <= 512 && sources.every((source) =>
    source && CODE_PATTERN.test(source.alias ?? '') && CODE_PATTERN.test(source.classification ?? '') &&
    CODE_PATTERN.test(source.reason_code ?? '') &&
    (source.counts === undefined || (Object.keys(source.counts).length <= 32 &&
      Object.entries(source.counts).every(([key, count]) => CODE_PATTERN.test(key) && Number.isSafeInteger(count) && count >= 0))));
}

function validCodes(values, maximum) {
  return Array.isArray(values) && values.length <= maximum && values.every((value) => CODE_PATTERN.test(value));
}

export function formatConsolidationReport(input) {
  const report = assertConsolidationReport(input);
  const totals = report.totals ?? {};
  const sources = report.sources.length === 0
    ? '  No sources inspected yet.'
    : report.sources.map((source) => `  ${source.alias} · ${source.classification} · ${source.reason_code}`).join('\n');
  const blockers = report.blockers.length === 0 ? 'none' : report.blockers.join(', ');
  return [
    'Where memory for this project is written',
    `  ${report.destination.store_kind} · ${report.destination.store_id} · ${report.destination.repository_id}`,
    '',
    'Which sources were inspected',
    sources,
    '',
    `Report health: ${report.phase}`,
    `Already represented: ${totals.already_represented ?? 0} · Unique: ${totals.unique ?? 0} · Ambiguous: ${totals.ambiguous ?? 0}`,
    `Active blockers: ${blockers}`,
    `Next: ${report.next_action}`,
    `Report ID: ${report.invocation_id}`,
  ].join('\n');
}

export function assertConsolidationExplanation(value) {
  if (!value || value.schema !== 'pulse.consolidation.explanation.v1' ||
      !ID_PATTERN.test(value.invocation_id ?? '') || !PHASES.has(value.phase) ||
      !validCodes(value.reason_codes, 32) || !validCodes(value.blockers, 32) ||
      typeof value.next_action !== 'string' || value.next_action.length > 4096) {
    throw new Error('Pulse consolidation explanation is invalid');
  }
  const encoded = JSON.stringify(value);
  if (Buffer.byteLength(encoded, 'utf8') > MAX_REPORT_BYTES || ABSOLUTE_PATH.test(encoded) || SENSITIVE.test(encoded)) {
    throw new Error('Pulse consolidation explanation is unsafe');
  }
  return value;
}

export function formatConsolidationExplanation(input) {
  const value = assertConsolidationExplanation(input);
  return [
    `Report ${value.invocation_id} · ${value.phase}`,
    `Reasons: ${value.reason_codes.length ? value.reason_codes.join(', ') : 'none yet'}`,
    `Blockers: ${value.blockers.length ? value.blockers.join(', ') : 'none'}`,
    `Next: ${value.next_action}`,
  ].join('\n');
}
