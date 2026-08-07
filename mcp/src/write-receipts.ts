import {
  validateWriteReceipt,
  type WriteReceipt as CanonicalWriteReceipt,
} from './lifecycle-contracts.js';
import { createHash } from 'node:crypto';

export interface WriteReceipt extends CanonicalWriteReceipt {
  ledger_id: string;
  candidate_id?: string;
  candidate_version?: number;
  destination_store_id: string;
  safe_provenance: {
    host: string;
    session_id: string;
    turn_id: string;
    source_event_key: string;
  };
  content_digest?: string;
  object_id?: string;
  reason_code?: string;
  policy_epoch: number;
  resolver_epoch: number;
  measurement_method: string;
  created_at: string;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

const STORE_ID = /^store_[a-z0-9][a-z0-9_]{2,127}$/;
const STABLE_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$/;
const DIGEST = /^[a-f0-9]{64}$/;
const RFC3339 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;

export function mcpRequestIdempotencyKey(sessionID: string | undefined, requestID: string | number): string {
  const material = `${sessionID ?? 'stdio'}\x1f${String(requestID)}`;
  return `mcp_${createHash('sha256').update('pulse-mcp-invocation-v1\x00').update(material).digest('hex')}`;
}

function exactKeys(value: Record<string, unknown>, allowed: readonly string[], label: string): void {
  const allow = new Set(allowed);
  if (Object.keys(value).some((key) => !allow.has(key))) {
    throw new Error(`${label} has unexpected fields`);
  }
}

// Product receipts are strict so MCP cannot render a pending or rejected
// attempt as saved merely because the daemon returned HTTP 200. Explicit
// Local Preview result shapes remain supported without being relabeled.
export function assertTruthfulWriteResponse(value: unknown): void {
  if (!isRecord(value)) {
    throw new Error('Pulse write response must be an object');
  }
  if (!('receipts' in value)) {
    if (value.ok === true && Array.isArray(value.ids)) {
      exactKeys(value, ['ok', 'ids', 'results'], 'Pulse preview remember response');
      if (value.ids.length === 0 || value.ids.some((id) => typeof id !== 'string' || !STABLE_ID.test(id))) {
        throw new Error('Pulse preview remember response has invalid IDs');
      }
      if (!Array.isArray(value.results) || value.results.length !== value.ids.length) {
        throw new Error('Pulse preview remember response has invalid results');
      }
      for (let index = 0; index < value.results.length; index += 1) {
        const result = value.results[index];
        if (!isRecord(result)) throw new Error('Pulse preview remember response has invalid results');
        exactKeys(result, ['id', 'result'], 'Pulse preview remember item result');
        if (result.id !== value.ids[index] ||
            (result.result !== 'created' && result.result !== 'deduplicated')) {
          throw new Error('Pulse preview remember response has invalid results');
        }
      }
      return;
    }
    if (value.ok === true && (
      typeof value.events_inserted === 'number' ||
      typeof value.checkpoint_saved === 'boolean'
    )) {
      exactKeys(value, [
        'ok', 'nodes_upserted', 'edges_upserted', 'facts_upserted', 'events_inserted',
        'event_ids', 'event_results', 'emotion_question', 'checkpoint_saved', 'claims_inserted', 'claims_superseded',
        'claims_skipped', 'claims_corroborated', 'events_indexed',
      ], 'Pulse preview graph response');
      for (const key of ['nodes_upserted', 'edges_upserted', 'facts_upserted', 'events_inserted',
        'claims_inserted', 'claims_superseded', 'claims_skipped', 'claims_corroborated']) {
        const count = value[key];
        if (count !== undefined && (!Number.isInteger(count) || Number(count) < 0)) {
          throw new Error('Pulse preview graph response has invalid counts');
        }
      }
      if (value.event_ids !== undefined && (!Array.isArray(value.event_ids) ||
          value.event_ids.some((id) => !Number.isInteger(id) || Number(id) < 1))) {
        throw new Error('Pulse preview graph response has invalid event IDs');
      }
      validateEventResults(value.event_results, value.event_ids);
      validateEmotionQuestion(value.emotion_question);
		if (value.events_indexed !== undefined && typeof value.events_indexed !== 'boolean') {
			throw new Error('Pulse preview graph response has invalid index status');
		}
      return;
    }
    throw new Error('Pulse write response has no durable receipt or explicit preview result');
  }
  if (typeof value.ledger_id !== 'string' || value.ledger_id.length === 0 ||
      !Array.isArray(value.receipts) || value.receipts.length === 0) {
    throw new Error('Pulse write receipt result is incomplete');
  }
  exactKeys(value, [
		'ledger_id', 'status', 'finalize_receipt', 'receipts',
		'event_ids', 'event_results', 'emotion_question',
	], 'Pulse product write response');
  if (!STABLE_ID.test(value.ledger_id) ||
      (value.status !== 'candidates' && value.status !== 'rejected') ||
      !isRecord(value.finalize_receipt)) {
    throw new Error('Pulse product write response is malformed');
  }
  validateFinalizeReceipt(value.finalize_receipt, value.ledger_id, value.status);
  for (const raw of value.receipts) {
    validateReceipt(raw);
    if (raw.ledger_id !== value.ledger_id) {
      throw new Error('Pulse item receipt is not bound to its finalize ledger');
    }
  }
	validateEventResults(value.event_results, value.event_ids);
	validateEmotionQuestion(value.emotion_question);
}

function validateEventResults(results: unknown, ids: unknown): void {
	if (results === undefined) return;
	if (!Array.isArray(results) || !Array.isArray(ids) || results.length !== ids.length) {
		throw new Error('Pulse graph response has invalid event results');
	}
	for (let index = 0; index < results.length; index += 1) {
		const result = results[index];
		if (!isRecord(result)) throw new Error('Pulse graph response has invalid event results');
		exactKeys(result, ['client_id', 'id', 'result'], 'Pulse graph event result');
		if (typeof result.client_id !== 'string' || !STABLE_ID.test(result.client_id) ||
			result.id !== ids[index] || !Number.isInteger(result.id) || Number(result.id) < 1 ||
			(result.result !== 'created' && result.result !== 'deduplicated')) {
			throw new Error('Pulse graph response has invalid event results');
		}
	}
}

function validateEmotionQuestion(question: unknown): void {
	if (question === undefined) return;
	if (!isRecord(question)) throw new Error('Pulse graph response has invalid emotion question');
	exactKeys(question, ['question_id', 'event_id', 'event_client_id', 'question', 'expires_at'], 'Pulse emotion question');
	if (typeof question.question_id !== 'string' || !/^emotion_question:[a-f0-9]{32}$/.test(question.question_id) ||
		!Number.isInteger(question.event_id) || Number(question.event_id) < 1 ||
		(question.event_client_id !== undefined && (typeof question.event_client_id !== 'string' || !STABLE_ID.test(question.event_client_id))) ||
		typeof question.question !== 'string' || question.question.length === 0 || question.question.length > 240 ||
		typeof question.expires_at !== 'string' || !RFC3339.test(question.expires_at) || Number.isNaN(Date.parse(question.expires_at))) {
		throw new Error('Pulse graph response has invalid emotion question');
	}
}

export function assertTruthfulDeletionReceipt(value: unknown, expectedObjectID?: string): void {
  if (isRecord(value) && value.ok === true && typeof value.deleted_id === 'string' &&
      Object.keys(value).length === 2) {
    if (expectedObjectID && value.deleted_id !== expectedObjectID) {
      throw new Error('Pulse legacy deletion response object mismatch');
    }
    return;
  }
  validateReceipt(value);
  if (value.status !== 'updated' || value.reason_code !== 'user_deleted' ||
      typeof value.object_id !== 'string' || value.object_id.length === 0) {
    throw new Error('Pulse deletion response has no truthful updated receipt');
  }
  if (expectedObjectID && value.object_id !== expectedObjectID) {
    throw new Error('Pulse deletion receipt object mismatch');
  }
}

function validateReceipt(value: unknown): asserts value is WriteReceipt {
  const canonical = validateWriteReceipt(value);
  if (!isRecord(value)) throw new Error('Pulse write receipt is malformed');
  exactKeys(value, [
    'schema', 'receipt_id', 'ledger_id', 'candidate_id', 'candidate_version', 'status',
    'destination', 'destination_store_id', 'safe_provenance', 'content_digest', 'object_id',
    'reason_code', 'policy_epoch', 'resolver_epoch', 'measurement_method', 'created_at',
    'actual_input_tokens', 'measurement',
  ], 'Pulse write receipt');
  if (typeof value.ledger_id !== 'string' || !STABLE_ID.test(value.ledger_id) ||
      typeof value.destination_store_id !== 'string' || !STORE_ID.test(value.destination_store_id) ||
      !isRecord(value.safe_provenance) ||
      typeof value.safe_provenance.host !== 'string' || value.safe_provenance.host.length === 0 ||
      typeof value.safe_provenance.session_id !== 'string' || !STABLE_ID.test(value.safe_provenance.session_id) ||
      typeof value.safe_provenance.turn_id !== 'string' || !STABLE_ID.test(value.safe_provenance.turn_id) ||
      typeof value.safe_provenance.source_event_key !== 'string' || !STABLE_ID.test(value.safe_provenance.source_event_key) ||
      !Number.isInteger(value.policy_epoch) || Number(value.policy_epoch) < 0 ||
      !Number.isInteger(value.resolver_epoch) || Number(value.resolver_epoch) < 0 ||
      typeof value.measurement_method !== 'string' || value.measurement_method.length === 0 ||
      typeof value.created_at !== 'string' || !RFC3339.test(value.created_at) || Number.isNaN(Date.parse(value.created_at))) {
    throw new Error('Pulse write receipt is malformed');
  }
  exactKeys(value.safe_provenance, ['host', 'session_id', 'turn_id', 'source_event_key'], 'Pulse write provenance');
  if (canonical.status === 'pending') {
    if (typeof value.candidate_id !== 'string' || !STABLE_ID.test(value.candidate_id) ||
        !Number.isInteger(value.candidate_version) || Number(value.candidate_version) < 1 ||
        typeof value.content_digest !== 'string' || !DIGEST.test(value.content_digest)) {
      throw new Error('pending receipt lacks candidate identity, version, or digest');
    }
  }
  if ((canonical.status === 'rejected' || canonical.status === 'failed' || canonical.status === 'canceled') &&
      (typeof value.reason_code !== 'string' || value.reason_code.length === 0)) {
    throw new Error(`${canonical.status} receipt lacks a content-free reason code`);
  }
}

function validateFinalizeReceipt(value: Record<string, unknown>, ledgerID: string, status: unknown): void {
  exactKeys(value, [
    'schema', 'receipt_id', 'ledger_id', 'status', 'destination', 'destination_store_id',
    'safe_provenance', 'policy_epoch', 'resolver_epoch', 'created_at',
  ], 'Pulse finalize receipt');
  if (value.schema !== 'pulse.turn_finalize_receipt.v1' ||
      typeof value.receipt_id !== 'string' || !STABLE_ID.test(value.receipt_id) ||
      value.ledger_id !== ledgerID || value.status !== status ||
		value.destination !== 'personal' ||
      typeof value.destination_store_id !== 'string' || !STORE_ID.test(value.destination_store_id) ||
      !isRecord(value.safe_provenance) ||
      typeof value.safe_provenance.host !== 'string' || value.safe_provenance.host.length === 0 ||
      typeof value.safe_provenance.session_id !== 'string' || !STABLE_ID.test(value.safe_provenance.session_id) ||
      typeof value.safe_provenance.turn_id !== 'string' || !STABLE_ID.test(value.safe_provenance.turn_id) ||
      typeof value.safe_provenance.source_event_key !== 'string' || !STABLE_ID.test(value.safe_provenance.source_event_key) ||
      !Number.isInteger(value.policy_epoch) || Number(value.policy_epoch) < 0 ||
      !Number.isInteger(value.resolver_epoch) || Number(value.resolver_epoch) < 0 ||
      typeof value.created_at !== 'string' || !RFC3339.test(value.created_at) || Number.isNaN(Date.parse(value.created_at))) {
    throw new Error('Pulse finalize receipt is malformed');
  }
  exactKeys(value.safe_provenance, ['host', 'session_id', 'turn_id', 'source_event_key'], 'Pulse finalize provenance');
}
