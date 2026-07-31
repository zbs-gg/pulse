import { createHash } from 'node:crypto';
import { isAbsolute, resolve } from 'node:path';

const SHA256 = /^[a-f0-9]{64}$/;

// Home moves an Inbox card into the ordinary Tray using the exact Inbox
// content digest as its idempotency key. The Go store protects that turn ID
// before exposing it in receipts; the physical release gate uses this public,
// content-free correlation to prove that the saved object came from the exact
// approved Inbox assignment.
export function unassignedAssignmentTurnRef(contentDigest) {
  if (!SHA256.test(contentDigest ?? '')) {
    throw new TypeError('unassigned assignment content digest is invalid');
  }
  const invocationDigest = createHash('sha256')
    .update('pulse-manual-invocation-v1\0unassigned\0')
    .update(contentDigest)
    .digest('hex');
  const rawTurnID = `unassigned_turn_unassigned_${invocationDigest.slice(0, 32)}`;
  return `turn:${createHash('sha256').update(`turn\x1f${rawTurnID}`).digest('hex')}`;
}

export function exactTarballPulseInvocation(tarballPath, args) {
  if (typeof tarballPath !== 'string' || !isAbsolute(tarballPath) || resolve(tarballPath) !== tarballPath ||
      !Array.isArray(args) || args.some((value) => typeof value !== 'string')) {
    throw new TypeError('physical attestation packed invocation is invalid');
  }
  return ['exec', '--yes', `--package=${tarballPath}`, '--', 'pulse', ...args];
}

export function physicalHostLifecycleEvidence(host) {
  if (host === 'codex') {
    return {
      kind: 'codex_native_hooks', ready: true,
      native_hook_trusted: true, trusted_hook_observed: true,
    };
  }
  if (host === 'claude-code') return { kind: 'claude_code_native_hooks', ready: true };
  if (host === 'cursor') return { kind: 'cursor_native_hooks', ready: true };
  throw new TypeError('physical attestation host is invalid');
}

function emptyList(value) { return value === null || (Array.isArray(value) && value.length === 0); }
function emptyMap(value) {
  return value === null || (value && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).length === 0);
}

export function contextQueryHasNoInfluence(result) {
  const retrieval = result?.trace?.retrieval;
  return result?.schema_version === 'pulse.context.v1' && retrieval &&
    ['facts', 'emotional_anchors', 'events', 'entities', 'relations', 'forbidden', 'private',
      'uncertainty', 'importance_questions'].every((name) => emptyList(result[name])) &&
    emptyList(retrieval.event_ids) && emptyMap(retrieval.score_breakdowns) &&
    emptyMap(retrieval.project_memory);
}
