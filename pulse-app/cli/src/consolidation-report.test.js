import assert from 'node:assert/strict';
import test from 'node:test';

import {
  assertConsolidationReport,
  formatConsolidationExplanation,
  formatConsolidationReport,
  reportRequestForArgs,
} from './consolidation-report.js';

function report(overrides = {}) {
  return {
    schema: 'pulse.consolidation.report.v1',
    protocol_version: 1,
    invocation_id: 'report_01',
    phase: 'planned',
    input_digest: 'a'.repeat(64),
    report_digest: 'b'.repeat(64),
    generation: 1,
    destination: {
      store_kind: 'personal',
      store_id: 'store_personal_contract',
      binding_digest: 'c'.repeat(64),
      repository_id: 'repository_pulse',
    },
    totals: { already_represented: 0, unique: 0, ambiguous: 0, excluded: 0 },
    sources: [],
    blockers: [],
    next_action: 'Wait for inventory.',
    created_at: '2026-07-21T08:00:00Z',
    updated_at: '2026-07-21T08:00:00Z',
    ...overrides,
  };
}

test('report command routing is isolated from the legacy consolidate command', () => {
  assert.deepEqual(reportRequestForArgs(['report']), { action: 'start', method: 'POST', path: '/memory/consolidation/reports' });
  assert.deepEqual(reportRequestForArgs(['report', 'status']), { action: 'status', method: 'GET', path: '/memory/consolidation/reports/latest' });
  assert.deepEqual(reportRequestForArgs(['report', 'cancel', '--id', 'report_01']), {
    action: 'cancel', method: 'POST', path: '/memory/consolidation/reports/report_01/cancel',
  });
  assert.throws(() => reportRequestForArgs(['--threshold', '0.9']), /not a report command/);
  assert.throws(() => reportRequestForArgs(['report', 'cancel']), /requires --id/);
  assert.throws(() => reportRequestForArgs(['report', 'unknown']), /unknown report action/);
});

test('plain explanation stays content-free and actionable', () => {
  const output = formatConsolidationExplanation({
    schema: 'pulse.consolidation.explanation.v1', invocation_id: 'report_01', phase: 'partial',
    reason_codes: ['source_locked'], blockers: ['claude_mem_locked'], next_action: 'Close the source and resume.',
  });
  assert.match(output, /^Report report_01 · partial/);
  assert.match(output, /Reasons: source_locked/);
  assert.match(output, /Next: Close the source and resume/);
});

test('portable report validator rejects wrong schemas, oversized values, and path leaks', () => {
  assert.deepEqual(assertConsolidationReport(report()), report());
  assert.throws(() => assertConsolidationReport(report({ schema: 'pulse.other.v1' })), /schema/);
  assert.throws(() => assertConsolidationReport(report({ next_action: 'x'.repeat(4097) })), /too large/);
  assert.throws(() => assertConsolidationReport(report({ next_action: '/Users/example/.pulse/private.db' })), /local path/);
  assert.throws(() => assertConsolidationReport(report({ next_action: 'token=ghp_example' })), /sensitive/);
});

test('plain output leads with destination and inspected sources', () => {
  const output = formatConsolidationReport(report());
  assert.match(output, /^Where memory for this project is written/m);
  assert.match(output, /personal · store_personal_contract · repository_pulse/);
  assert.match(output, /^Which sources were inspected/m);
  assert.match(output, /No sources inspected yet/);
  assert.match(output, /Report health: planned/);
  assert.doesNotMatch(output, /cccccccccccccccc/);
});
