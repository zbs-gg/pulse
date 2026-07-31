import { createHash } from 'node:crypto';
import { posix } from 'node:path';

export const LIFECYCLE_SCHEMA = 'pulse.lifecycle_event.v1' as const;
export const WRITE_RECEIPT_SCHEMA = 'pulse.write_receipt.v1' as const;
export const INJECTION_SCHEMA = 'pulse.context.v1' as const;
export const BINDING_SCHEMA = 'pulse.binding.v1' as const;
export const CONTEXT_LEASE_SCHEMA = 'pulse.context_lease.v1' as const;
export const CONSOLIDATION_REPORT_SCHEMA = 'pulse.consolidation.report.v1' as const;
export const CONSOLIDATION_EXPLANATION_SCHEMA = 'pulse.consolidation.explanation.v1' as const;

export type LifecycleHost = 'codex' | 'claude-code' | 'cursor';
export type LifecycleEventKind =
  | 'session_start'
  | 'turn_start'
  | 'tool_receipt'
  | 'pre_compact'
  | 'subagent_start'
  | 'subagent_stop'
  | 'turn_finalize'
  | 'session_resume';

export interface LifecycleEvent {
  schema: typeof LIFECYCLE_SCHEMA;
  host: LifecycleHost;
  event: LifecycleEventKind;
  session_id: string;
  turn_id: string;
  workspace: string;
  model: string;
  source: string;
  stop_hook_active: boolean;
}

const SUPPORTED_EVENTS = new Set<LifecycleEventKind>([
  'session_start', 'turn_start', 'tool_receipt', 'pre_compact',
  'subagent_start', 'subagent_stop', 'turn_finalize', 'session_resume',
]);
const AUTHORITY_FIELDS = new Set([
	'audience', 'principal', 'role', 'scope', 'vault', 'visibility', 'workspace',
]);
const STABLE_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$/;
const CONTROL = /[\u0000-\u001f\u007f]/;
const HEX_DIGEST = /^[a-f0-9]{64}$/;
const REPORT_ID = /^report_[A-Za-z0-9._-]{1,128}$/;
const SAFE_CODE = /^[a-z][a-z0-9_]{0,127}$/;
const LOCAL_PATH = /(?:^|[\s"'])\/(?:Users|home|var|private|Volumes|workspace)\/|[A-Za-z]:\\/;
const SECRET_MARKER = /(?:token\s*=|api[_-]?key|authorization\s*:|begin private key|ghp_|xox[baprs]-)/i;
const REPORT_PHASES = new Set([
  'planned', 'inventory', 'deterministic_dedupe', 'report_ready',
  'partial', 'stale', 'cancel_requested', 'canceled',
]);

function record(value: unknown, error: string): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(error);
  return value as Record<string, unknown>;
}

function requiredString(input: Record<string, unknown>, field: string, error: string): string {
  const value = input[field];
  if (typeof value !== 'string' || value.length === 0 || CONTROL.test(value)) throw new Error(error);
  return value;
}

function exactKeys(value: Record<string, unknown>, required: readonly string[], optional: readonly string[] = []): boolean {
  const allowed = new Set([...required, ...optional]);
  return required.every((key) => key in value) && Object.keys(value).every((key) => allowed.has(key));
}

function safeCodes(value: unknown, maximum: number): value is string[] {
  return Array.isArray(value) && value.length <= maximum && value.every((item) => typeof item === 'string' && SAFE_CODE.test(item));
}

export interface ConsolidationReport {
  schema: typeof CONSOLIDATION_REPORT_SCHEMA;
  protocol_version: 1;
  invocation_id: string;
  phase: string;
  input_digest: string;
  report_digest: string;
  inventory_digest?: string;
  generation: number;
	destination: { store_kind: 'personal'; store_id: string; binding_digest: string; repository_id: string };
  totals: { already_represented: number; unique: number; ambiguous: number; excluded: number };
  sources: Array<{ alias: string; classification: string; reason_code: string; counts?: Record<string, number> }>;
  blockers: string[];
  reason_codes?: string[];
  next_action: string;
  created_at: string;
  updated_at: string;
}

export function validateConsolidationReport(rawReport: unknown): ConsolidationReport {
  const report = record(rawReport, 'invalid_consolidation_report');
  const required = [
    'schema', 'protocol_version', 'invocation_id', 'phase', 'input_digest', 'report_digest', 'generation',
    'destination', 'totals', 'sources', 'blockers', 'next_action', 'created_at', 'updated_at',
  ];
  if (!exactKeys(report, required, ['inventory_digest', 'reason_codes']) ||
      report.schema !== CONSOLIDATION_REPORT_SCHEMA || report.protocol_version !== 1 ||
      !REPORT_ID.test(String(report.invocation_id ?? '')) || !REPORT_PHASES.has(String(report.phase ?? '')) ||
      !HEX_DIGEST.test(String(report.input_digest ?? '')) || !HEX_DIGEST.test(String(report.report_digest ?? '')) ||
      (report.inventory_digest !== undefined && !HEX_DIGEST.test(String(report.inventory_digest))) ||
      !Number.isSafeInteger(report.generation) || Number(report.generation) < 1 ||
      typeof report.next_action !== 'string' || report.next_action.length < 1 || report.next_action.length > 4096) {
    throw new Error('invalid_consolidation_report');
  }
  const destination = record(report.destination, 'invalid_consolidation_destination');
  if (!exactKeys(destination, ['store_kind', 'store_id', 'binding_digest', 'repository_id']) ||
			destination.store_kind !== 'personal' ||
      !/^store_[a-z0-9][a-z0-9_]{2,127}$/.test(String(destination.store_id ?? '')) ||
      !HEX_DIGEST.test(String(destination.binding_digest ?? '')) ||
      !/^[A-Za-z0-9][A-Za-z0-9._:-]{2,255}$/.test(String(destination.repository_id ?? ''))) {
    throw new Error('invalid_consolidation_destination');
  }
  const totals = record(report.totals, 'invalid_consolidation_totals');
  if (!exactKeys(totals, ['already_represented', 'unique', 'ambiguous', 'excluded']) ||
      !Object.values(totals).every((value) => Number.isSafeInteger(value) && Number(value) >= 0)) {
    throw new Error('invalid_consolidation_totals');
  }
  if (!Array.isArray(report.sources) || report.sources.length > 512 || !safeCodes(report.blockers, 32) ||
      !safeCodes(report.reason_codes ?? [], 32)) {
    throw new Error('invalid_consolidation_collections');
  }
  for (const rawSource of report.sources) {
    const source = record(rawSource, 'invalid_consolidation_source');
    if (!exactKeys(source, ['alias', 'classification', 'reason_code'], ['counts']) ||
        !SAFE_CODE.test(String(source.alias ?? '')) || !SAFE_CODE.test(String(source.classification ?? '')) ||
        !SAFE_CODE.test(String(source.reason_code ?? ''))) {
      throw new Error('invalid_consolidation_source');
    }
    if (source.counts !== undefined) {
      const counts = record(source.counts, 'invalid_consolidation_counts');
      if (Object.keys(counts).length > 32 || !Object.entries(counts).every(([key, value]) =>
        SAFE_CODE.test(key) && Number.isSafeInteger(value) && Number(value) >= 0)) {
        throw new Error('invalid_consolidation_counts');
      }
    }
  }
  const encoded = JSON.stringify(report);
  if (Buffer.byteLength(encoded, 'utf8') > 1024 * 1024 || LOCAL_PATH.test(encoded) || SECRET_MARKER.test(encoded)) {
    throw new Error('unsafe_consolidation_report');
  }
  return report as unknown as ConsolidationReport;
}

export function validateConsolidationExplanation(rawExplanation: unknown): Record<string, unknown> {
  const explanation = record(rawExplanation, 'invalid_consolidation_explanation');
  if (!exactKeys(explanation, ['schema', 'invocation_id', 'phase', 'reason_codes', 'blockers', 'next_action']) ||
      explanation.schema !== CONSOLIDATION_EXPLANATION_SCHEMA ||
      !REPORT_ID.test(String(explanation.invocation_id ?? '')) || !REPORT_PHASES.has(String(explanation.phase ?? '')) ||
      !safeCodes(explanation.reason_codes, 32) || !safeCodes(explanation.blockers, 32) ||
      typeof explanation.next_action !== 'string' || explanation.next_action.length < 1 || explanation.next_action.length > 4096) {
    throw new Error('invalid_consolidation_explanation');
  }
  const encoded = JSON.stringify(explanation);
  if (Buffer.byteLength(encoded, 'utf8') > 1024 * 1024 || LOCAL_PATH.test(encoded) || SECRET_MARKER.test(encoded)) {
    throw new Error('unsafe_consolidation_explanation');
  }
  return explanation;
}

export function normalizeLifecycleEvent(
  host: LifecycleHost,
  event: LifecycleEventKind,
  rawInput: unknown,
): LifecycleEvent {
  if (host !== 'codex' && host !== 'claude-code' && host !== 'cursor') throw new Error('unsupported_host');
  if (!SUPPORTED_EVENTS.has(event)) throw new Error('unsupported_event');
  const input = record(rawInput, 'invalid_lifecycle_input');
  for (const field of Object.keys(input).map((field) => field.trim().toLowerCase()).sort()) {
    if (AUTHORITY_FIELDS.has(field)) throw new Error(`authority_field_forbidden:${field}`);
  }
  const sessionId = requiredString(input, 'session_id', 'invalid_session_id');
  if (!STABLE_ID.test(sessionId)) throw new Error('invalid_session_id');
  const cwd = requiredString(input, 'cwd', 'invalid_workspace');
  if (!cwd.startsWith('/')) throw new Error('invalid_workspace');
  const model = requiredString(input, 'model', 'invalid_model');
  const source = requiredString(input, 'source', 'invalid_source');
  const rawTurnId = input.turn_id;
  const turnId = event === 'session_start' && rawTurnId === undefined
    ? `session_${createHash('sha256').update([
      'pulse-thread-scoped-turn-v1', host, sessionId, posix.normalize(cwd), source,
    ].join('\x1f')).digest('hex')}`
    : requiredString(input, 'turn_id', 'invalid_turn_id');
  if (!STABLE_ID.test(turnId)) throw new Error('invalid_turn_id');
  const stopHook = input.stop_hook_active ?? false;
  if (typeof stopHook !== 'boolean') throw new Error('invalid_stop_hook_active');
  return {
    schema: LIFECYCLE_SCHEMA,
    host,
    event,
    session_id: sessionId,
    turn_id: turnId,
    workspace: posix.normalize(cwd),
    model,
    source,
    stop_hook_active: stopHook,
  };
}

export function lifecycleIdempotencyKey(event: LifecycleEvent): string {
  const material = [
    event.schema, event.host, event.event, event.session_id, event.turn_id,
    event.workspace, event.source,
  ].join('\x1f');
  return `lifecycle:${createHash('sha256').update(material).digest('hex')}`;
}

type ReceiptStatus = 'pending' | 'created' | 'updated' | 'deduplicated' | 'canceled' | 'rejected' | 'failed';

export interface WriteReceipt {
  schema: typeof WRITE_RECEIPT_SCHEMA;
  status: ReceiptStatus;
  receipt_id: string;
	destination: 'personal';
  object_id?: string;
  actual_input_tokens?: number;
  measurement?: { kind: 'estimated' | 'provider_actual'; source: string };
}

export function validateWriteReceipt(rawReceipt: unknown): WriteReceipt {
  const receipt = record(rawReceipt, 'invalid_receipt') as unknown as WriteReceipt;
  if (receipt.schema !== WRITE_RECEIPT_SCHEMA) throw new Error('invalid_receipt_schema');
  if (!STABLE_ID.test(receipt.receipt_id)) throw new Error('invalid_receipt_id');
	if (receipt.destination !== 'personal') throw new Error('invalid_destination');
  if (!new Set<ReceiptStatus>(['pending', 'created', 'updated', 'deduplicated', 'canceled', 'rejected', 'failed']).has(receipt.status)) {
    throw new Error('invalid_receipt_status');
  }
  const requiresObject = ['created', 'updated', 'deduplicated'].includes(receipt.status);
  if (requiresObject && (!receipt.object_id || !STABLE_ID.test(receipt.object_id))) throw new Error('object_id_required');
  if (!requiresObject && receipt.object_id) throw new Error('object_id_forbidden');
  const actual = receipt.actual_input_tokens ?? 0;
  if (!Number.isInteger(actual) || actual < 0) throw new Error('invalid_actual_input_tokens');
  if (actual > 0 && (receipt.measurement?.kind !== 'provider_actual' || !receipt.measurement.source)) {
    throw new Error('provider_measurement_required');
  }
  return receipt;
}

export interface BindingDecision {
  schema: typeof BINDING_SCHEMA;
  workspace: string;
	kind: 'personal';
	read_vaults: Array<'personal'>;
	write_destination: 'personal';
}

function sameSet<T extends string>(actual: readonly T[], expected: readonly T[]): boolean {
  return actual.length === expected.length && expected.every((item) => actual.includes(item));
}

export function validateBindingDecision(rawBinding: unknown): BindingDecision {
  const binding = record(rawBinding, 'invalid_binding') as unknown as BindingDecision;
  if (binding.schema !== BINDING_SCHEMA) throw new Error('invalid_binding_schema');
  if (typeof binding.workspace !== 'string' || !binding.workspace.startsWith('/') || CONTROL.test(binding.workspace)) {
    throw new Error('invalid_binding_workspace');
  }
  if (!Array.isArray(binding.read_vaults)) throw new Error('invalid_binding_read_vaults');
	if (binding.kind !== 'personal') {
		throw new Error('invalid_binding_kind');
	}
	if (binding.write_destination !== 'personal' || !sameSet(binding.read_vaults, ['personal'])) {
		throw new Error('binding_destination_mismatch');
	}
  return binding;
}

export interface ContextLease {
  schema: typeof CONTEXT_LEASE_SCHEMA;
  binding_digest: string;
  policy_epoch: number;
  membership_generation: number;
  object_generation: number;
  expires_at: string;
}

export function validateContextLease(
  rawLease: unknown,
  now: Date,
  policyEpoch: number,
  membershipGeneration: number,
  objectGeneration: number,
): ContextLease {
  const lease = record(rawLease, 'invalid_context_lease') as unknown as ContextLease;
  if (lease.schema !== CONTEXT_LEASE_SCHEMA || !lease.binding_digest?.startsWith('sha256:')) {
    throw new Error('invalid_context_lease');
  }
  const expiresAt = new Date(lease.expires_at);
  if (Number.isNaN(expiresAt.valueOf()) || expiresAt <= now) throw new Error('context_lease_expired');
  if (lease.policy_epoch !== policyEpoch || lease.membership_generation !== membershipGeneration
    || lease.object_generation !== objectGeneration) {
    throw new Error('context_lease_stale');
  }
  return lease;
}

export function validateMandatoryApplication(active: boolean, evidenceIds: readonly string[]): void {
  if (!active) throw new Error('mandatory_inactive');
  if (evidenceIds.length === 0) throw new Error('mandatory_evidence_required');
  if (evidenceIds.some((evidenceId) => !STABLE_ID.test(evidenceId))) throw new Error('mandatory_evidence_invalid');
}

export type LifecycleState =
	| 'pending' | 'canceled' | 'committed_private' | 'corrected' | 'failed' | 'retrieved';

const TRANSITIONS: Partial<Record<LifecycleState, ReadonlySet<LifecycleState>>> = {
  pending: new Set(['pending', 'canceled', 'committed_private']),
	committed_private: new Set(['corrected']),
	corrected: new Set(['committed_private']),
};

export function canTransition(from: LifecycleState, to: LifecycleState): boolean {
  return TRANSITIONS[from]?.has(to) ?? false;
}

export interface InjectionPack {
  schema: typeof INJECTION_SCHEMA;
  evidence: string[];
  practices: string[];
}

export function renderInjection(pack: InjectionPack): string {
  if (pack.schema !== INJECTION_SCHEMA) throw new Error('invalid_injection_schema');
  return JSON.stringify(pack);
}
