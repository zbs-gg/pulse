// Shared by native bridges and BB. Item count is not a memory selection rule.
import { createHash } from 'node:crypto';
const labels = new Set(['joy', 'sadness', 'anger', 'fear', 'trust', 'disgust', 'anticipation', 'surprise', 'shame', 'guilt']);
export function validateMomentItems(items) {
  if (!Array.isArray(items) || !items.length) throw new Error('memory_items_required_nothing_accepted');
  return items.map(item => {
    if (!item || typeof item !== 'object' || Array.isArray(item) ||
      Object.keys(item).some(k => !['kind', 'scope', 'summary', 'emotion', 'emotions', 'privacy', 'evidence'].includes(k)) ||
      !['decision', 'preference', 'open_loop', 'project_state', 'correction', 'emotion', 'fact', 'relationship_note', 'do_not_repeat'].includes(item.kind) ||
      !['personal', 'project', 'personal_global'].includes(item.scope) ||
      typeof item.summary !== 'string' || !item.summary.trim() || Buffer.byteLength(item.summary) > 1024 * 1024 ||
      (item.privacy !== undefined && !['normal', 'sensitive', 'private'].includes(item.privacy))) throw new Error('memory_item_invalid_nothing_accepted');
    const lower = item.summary.toLowerCase();
    if (['/users/', 'file://', 'token=', 'api_key', 'apikey', 'password', 'secret', 'private_key', 'begin private key', 'sk-', 'akia', 'xoxb-', 'ghp_'].some(marker => lower.includes(marker)) ||
      (lower.match(/user:/g) || []).length >= 3 || (lower.match(/assistant:/g) || []).length >= 3 || (lower.match(/\n/g) || []).length > 30) throw new Error('unsafe_memory_nothing_accepted');
    const emotions = item.emotions ?? (item.emotion ? [item.emotion] : []);
    if (item.kind === 'emotion') {
      if (!Array.isArray(emotions) || !emotions.length || (item.emotions && item.emotion)) throw new Error('memory_emotions_required_nothing_accepted');
      for (const e of emotions) if (!e || Object.keys(e).some(k => !['label', 'name', 'intensity', 'source', 'cause'].includes(k)) || !labels.has(e.label) || !Number.isFinite(e.intensity) || e.intensity < 0 || e.intensity > 1 || !['user', 'inferred'].includes(e.source) ||
        (e.name !== undefined && (typeof e.name !== 'string' || !e.name.trim() || Buffer.byteLength(e.name) > 120)) ||
        (e.cause !== undefined && (typeof e.cause !== 'string' || !e.cause.trim() || Buffer.byteLength(e.cause) > 360))) throw new Error('memory_emotion_invalid_nothing_accepted');
    } else if (item.emotion !== undefined || item.emotions !== undefined) throw new Error('memory_emotions_unexpected_nothing_accepted');
    return item;
  });
}
export function expandMomentItems(items) {
  return items.flatMap(item => {
    const parts = []; let part = '';
    for (const c of item.summary) { if (Buffer.byteLength(part + c) > 1100) { parts.push(part); part = ''; } part += c; }
    if (part) parts.push(part);
    const feelings = item.kind === 'emotion' ? item.emotions ?? [item.emotion] : [undefined];
    return parts.flatMap(summary => feelings.map(emotion => ({ ...item, summary: summary.trim(), emotion, emotions: undefined })));
  });
}
export function memoryMomentBody(input, context, now = new Date()) {
  const timestamp = now.toISOString();
  const candidates = expandMomentItems(validateMomentItems(input.items)).map((item, index) => {
    const scope = item.scope === 'project' ? 'project' : 'personal_global';
    const source = { host: context.host, conversation_scope: 'current_turn', timestamp };
    if (item.kind !== 'emotion') return {      
kind: 'memory_capsule', memory_scope: scope, capsule: {        
schema: 'pulse.memory_capsule.v1', source, raw_input_included: false, items: [{
          kind: item.kind, redacted_summary: item.summary, confidence: item.evidence === 'assistant_inferred' ? 0.6 : 1, evidence_hint: item.evidence === 'tool_result' ? 'tool_result' : item.evidence === 'assistant_inferred' ? 'assistant_inferred' : 'current_turn', privacy_tier: item.privacy ?? 'normal', retention: scope === 'personal_global' ? 'long_term' : 'project'        
}]      
}    
};
    const e = item.emotion, confidence = e.source === 'user' ? 1 : 0.8, derivation = e.source === 'user' ? 'explicit' : 'inferred';
    const clientID = 'emotion_' + createHash('sha256').update([context.source_event_key, String(index), item.summary, e.label, e.name ?? ''].join('\x1f')).digest('hex').slice(0, 24);
    return {      
kind: 'semantic_delta', memory_scope: scope, semantic_delta: {        
schema: 'pulse.semantic_delta.v1', source: { ...source, session_id: context.session_id }, raw_input_included: false, events: [{
          client_id: clientID, title: 'Emotional moment: ' + (e.name ?? e.label), summary: item.summary, emotional_weight: e.intensity, confidence, privacy_tier: item.privacy ?? 'sensitive', emotions: { [e.label]: e.intensity }, emotion_derivation: derivation, emotion_confidence: confidence, observed_label: e.name ?? e.label,
          ...(e.cause === undefined ? {} : { trigger: { summary: e.cause, derivation, confidence, confirmed: e.source === 'user' } })
        }]      
}    
};
  });
  return { schema: 'pulse.turn_finalize.v1', host: context.host, session_id: context.session_id, turn_id: context.turn_id, source_event_key: context.source_event_key, idempotency_key: context.idempotency_key, binding_digest: context.binding_digest, policy_epoch: context.policy_epoch, resolver_epoch: context.resolver_epoch, candidates };
}
export function compactMomentResult(result, expected) {
  const receipts = result?.receipts;
  if (!result?.ledger_id || !Array.isArray(receipts) || !receipts.length) throw new Error('pulse_write_receipt_invalid');
  const stored = receipts.filter(r => ['created', 'updated', 'deduplicated'].includes(r.status) && typeof r.object_id === 'string' && r.object_id);
  const failed = receipts.some(r => ['rejected', 'failed', 'canceled'].includes(r.status));
  return { status: failed ? 'rejected' : stored.length === receipts.length && (expected === undefined || receipts.length === expected) ? 'stored' : stored.length ? 'partial' : 'pending', moment_id: result.moment_id, accepted: receipts.length, stored: stored.length, ids: stored.map(r => r.object_id) };
}

// Replay the identical frozen body once if the transport lost its answer. The
// server's content-addressed moment identity prevents duplicate materialization.
export async function writeMemoryMoment(request, body, signal) {
  try { return await request('/memory/moments', { body, timeoutMs: 5000, idempotencyKey: body.idempotency_key }); }
  catch (error) {
    if (signal?.aborted || (error.status >= 400 && error.status < 500) || /(?:HTTP |http_)4\d\d/.test(error.message ?? '')) throw error;
    return request('/memory/moments', { body, timeoutMs: 5000, idempotencyKey: body.idempotency_key });
  }
}
