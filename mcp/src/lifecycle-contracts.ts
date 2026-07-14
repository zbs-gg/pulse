import { createHash } from 'node:crypto';
import { posix } from 'node:path';

export const LIFECYCLE_SCHEMA = 'pulse.lifecycle_event.v1' as const;
export const WRITE_RECEIPT_SCHEMA = 'pulse.write_receipt.v1' as const;
export const INJECTION_SCHEMA = 'pulse.context.v1' as const;
export const BINDING_SCHEMA = 'pulse.binding.v1' as const;
export const CONTEXT_LEASE_SCHEMA = 'pulse.context_lease.v1' as const;

export type LifecycleHost = 'codex' | 'claude-code';
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
  'audience', 'principal', 'role', 'scope', 'team_id', 'vault', 'visibility', 'workspace',
]);
const STABLE_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$/;
const CONTROL = /[\u0000-\u001f\u007f]/;

function record(value: unknown, error: string): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(error);
  return value as Record<string, unknown>;
}

function requiredString(input: Record<string, unknown>, field: string, error: string): string {
  const value = input[field];
  if (typeof value !== 'string' || value.length === 0 || CONTROL.test(value)) throw new Error(error);
  return value;
}

export function normalizeLifecycleEvent(
  host: LifecycleHost,
  event: LifecycleEventKind,
  rawInput: unknown,
): LifecycleEvent {
  if (host !== 'codex' && host !== 'claude-code') throw new Error('unsupported_host');
  if (!SUPPORTED_EVENTS.has(event)) throw new Error('unsupported_event');
  const input = record(rawInput, 'invalid_lifecycle_input');
  for (const field of Object.keys(input).map((field) => field.trim().toLowerCase()).sort()) {
    if (AUTHORITY_FIELDS.has(field)) throw new Error(`authority_field_forbidden:${field}`);
  }
  const sessionId = requiredString(input, 'session_id', 'invalid_session_id');
  if (!STABLE_ID.test(sessionId)) throw new Error('invalid_session_id');
  const turnId = requiredString(input, 'turn_id', 'invalid_turn_id');
  if (!STABLE_ID.test(turnId)) throw new Error('invalid_turn_id');
  const cwd = requiredString(input, 'cwd', 'invalid_workspace');
  if (!cwd.startsWith('/')) throw new Error('invalid_workspace');
  const model = requiredString(input, 'model', 'invalid_model');
  const source = requiredString(input, 'source', 'invalid_source');
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
  destination: 'personal' | 'desk';
  object_id?: string;
  actual_input_tokens?: number;
  measurement?: { kind: 'estimated' | 'provider_actual'; source: string };
}

export function validateWriteReceipt(rawReceipt: unknown): WriteReceipt {
  const receipt = record(rawReceipt, 'invalid_receipt') as unknown as WriteReceipt;
  if (receipt.schema !== WRITE_RECEIPT_SCHEMA) throw new Error('invalid_receipt_schema');
  if (!STABLE_ID.test(receipt.receipt_id)) throw new Error('invalid_receipt_id');
  if (receipt.destination !== 'personal' && receipt.destination !== 'desk') throw new Error('invalid_destination');
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
  kind: 'personal' | 'team';
  team_deployment?: string;
  read_vaults: Array<'personal' | 'desk' | 'commons'>;
  write_destination: 'personal' | 'desk';
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
  if (binding.kind === 'personal') {
    if (binding.team_deployment || binding.write_destination !== 'personal' || !sameSet(binding.read_vaults, ['personal'])) {
      throw new Error('binding_destination_mismatch');
    }
  } else if (binding.kind === 'team') {
    if (!binding.team_deployment || !STABLE_ID.test(binding.team_deployment) || binding.write_destination !== 'desk'
      || !sameSet(binding.read_vaults, ['desk', 'commons'])) {
      throw new Error('binding_destination_mismatch');
    }
  } else {
    throw new Error('invalid_binding_kind');
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

export function validateAirlockApproval(preparedDigest: string, approvedDigest: string): void {
  if (!preparedDigest.startsWith('sha256:') || preparedDigest !== approvedDigest) throw new Error('airlock_digest_mismatch');
}

export function validateMandatoryApplication(active: boolean, evidenceIds: readonly string[]): void {
  if (!active) throw new Error('mandatory_inactive');
  if (evidenceIds.length === 0) throw new Error('mandatory_evidence_required');
  if (evidenceIds.some((evidenceId) => !STABLE_ID.test(evidenceId))) throw new Error('mandatory_evidence_invalid');
}

export type LifecycleState =
  | 'pending' | 'canceled' | 'committed_private' | 'corrected' | 'airlock_prepared'
  | 'airlock_approved' | 'airlock_expired' | 'airlock_canceled' | 'in_flight'
  | 'remote_committed_local_pending' | 'reconciled' | 'failed' | 'retrieved';

const TRANSITIONS: Partial<Record<LifecycleState, ReadonlySet<LifecycleState>>> = {
  pending: new Set(['pending', 'canceled', 'committed_private']),
  committed_private: new Set(['corrected', 'airlock_prepared']),
  corrected: new Set(['committed_private']),
  airlock_prepared: new Set(['airlock_approved', 'airlock_expired', 'airlock_canceled']),
  airlock_approved: new Set(['airlock_prepared', 'airlock_canceled', 'in_flight']),
  in_flight: new Set(['remote_committed_local_pending', 'reconciled', 'failed']),
  remote_committed_local_pending: new Set(['reconciled']),
};

export function canTransition(from: LifecycleState, to: LifecycleState): boolean {
  return TRANSITIONS[from]?.has(to) ?? false;
}

export interface ProvenanceRef {
  vault_class: 'personal' | 'desk' | 'commons' | 'airlock';
  object_id: string;
}

export function validateCommonsProvenance(refs: readonly ProvenanceRef[]): void {
  if (refs.length === 0) throw new Error('provenance_required');
  for (const ref of refs) {
    if (ref.vault_class !== 'commons' && ref.vault_class !== 'airlock') throw new Error('private_lineage_forbidden');
    if (!STABLE_ID.test(ref.object_id)) throw new Error('invalid_provenance_object_id');
  }
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
