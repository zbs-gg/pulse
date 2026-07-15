import { createHash, randomBytes as cryptoRandomBytes } from 'node:crypto';

import {
  buildSenderConstrainedRemoteHeaders,
  createOSCredentialStore,
  refreshRemoteCredential,
} from './remote-auth.js';
import { boundedRemoteFetch, boundedRemoteRead } from './remote-auth-network.js';
import { acquireRemoteCredentialLock } from './team-remote-client.js';

const APPROVAL_PATH = '/owner/v1/approval';
const OWNER_PATHS = new Set([
  APPROVAL_PATH,
  '/owner/v1/members',
  '/owner/v1/bindings',
  '/owner/v1/projects',
  '/owner/v1/project-grants',
]);
const SAFE_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$/;
const DIGEST = /^[a-f0-9]{64}$/;
const UNSAFE_TEXT = /authorization:\s*bearer|token\s*=|api[_-]?key|password|private[_-]?key|begin private key|\bsk-[A-Za-z0-9_-]{12,}\b|\bghp_[A-Za-z0-9_]{12,}\b|\/(?:users|home|etc|var|private|volumes)\/|file:\/\/|(?:^|\s)~\/|(?:^|\s)[a-z]:\\|\\\\[^\\\s]+\\/i;
const MAX_RESPONSE_BYTES = 32 * 1024;
const DEFAULT_NETWORK_TIMEOUT_MS = 10_000;
const OWNER_OPERATION_STEP_UP_NAMESPACE = 'pulse.team.owner.operation.step_up.v1';

const OPERATIONS = Object.freeze({
  'membership.create': {
    path: '/owner/v1/members', schema: 'pulse.team.owner.members.v1', result: 'pulse.team.owner.members_result.v1',
    fields: ['issuer', 'subject', 'role'], target(input) {
      const identityKey = opaqueDigest('human-identity-v1', input.issuer, input.subject);
      return ['membership', identityKey, ownerDigest(input.action, identityKey, input.role)];
    },
  },
  'membership.revoke': {
    path: '/owner/v1/members', schema: 'pulse.team.owner.members.v1', result: 'pulse.team.owner.members_result.v1',
    fields: ['target_id'], target(input) {
      return ['membership', input.target_id, ownerDigest(input.action, input.target_id)];
    },
  },
  'agent_binding.create': {
    path: '/owner/v1/bindings', schema: 'pulse.team.owner.bindings.v1', result: 'pulse.team.owner.bindings_result.v1',
    fields: ['issuer', 'subject', 'client_id'], target(input) {
      const bindingKey = opaqueDigest('agent-binding-v1', input.issuer, input.subject, input.client_id);
      return ['agent_binding', bindingKey, ownerDigest(input.action, bindingKey)];
    },
  },
  'agent_binding.revoke': {
    path: '/owner/v1/bindings', schema: 'pulse.team.owner.bindings.v1', result: 'pulse.team.owner.bindings_result.v1',
    fields: ['target_id'], target(input) {
      return ['agent_binding', input.target_id, ownerDigest(input.action, input.target_id)];
    },
  },
  'project.create': {
    path: '/owner/v1/projects', schema: 'pulse.team.owner.projects.v1', result: 'pulse.team.owner.projects_result.v1',
    fields: ['name'], target(input) {
      const nameKey = ownerDigest('project-name-v1', input.name);
      return ['project', nameKey, ownerDigest(input.action, nameKey)];
    },
  },
  'project_grant.create': {
    path: '/owner/v1/project-grants', schema: 'pulse.team.owner.project_grants.v1', result: 'pulse.team.owner.project_grants_result.v1',
    fields: ['project_id', 'target_principal_id', 'access_level'], target(input) {
      const grantKey = ownerDigest('project-grant-v1', input.project_id, input.target_principal_id);
      return ['project_grant', grantKey, ownerDigest(
        input.action, input.project_id, input.target_principal_id, input.access_level,
      )];
    },
  },
  'project_grant.revoke': {
    path: '/owner/v1/project-grants', schema: 'pulse.team.owner.project_grants.v1', result: 'pulse.team.owner.project_grants_result.v1',
    fields: ['target_id'], target(input) {
      return ['project_grant', input.target_id, ownerDigest(input.action, input.target_id)];
    },
  },
});

export class TeamOwnerError extends Error {
  constructor(code) {
    super(`team_owner_${code}`);
    this.name = 'TeamOwnerError';
    this.code = code;
  }
}

function fail(code) {
  throw new TeamOwnerError(code);
}

function exactKeys(value, fields, code = 'request_invalid') {
  if (!value || typeof value !== 'object' || Array.isArray(value) ||
      Object.keys(value).sort().join('\0') !== [...fields].sort().join('\0')) fail(code);
  return value;
}

function safeID(value, code = 'request_invalid') {
  if (typeof value !== 'string' || !SAFE_ID.test(value)) fail(code);
  return value;
}

function exactIdentity(value) {
  if (typeof value !== 'string' || value === '' || value.length > 2048 || value.trim() !== value ||
      /[\u0000-\u001f\u007f]/.test(value)) fail('request_invalid');
  return value;
}

function exactIssuer(value) {
  exactIdentity(value);
  let parsed;
  try { parsed = new URL(value); } catch { fail('request_invalid'); }
  if (parsed.protocol !== 'https:' || parsed.username || parsed.password || parsed.search || parsed.hash) {
    fail('request_invalid');
  }
  const canonical = parsed.toString();
  if (canonical !== value) fail('request_invalid');
  return canonical;
}

function displayName(value) {
  if (typeof value !== 'string' || value === '' || value.trim() !== value ||
      [...value].length > 128 || value.normalize('NFC') !== value ||
      /[\u0000-\u001f\u007f]/.test(value) || UNSAFE_TEXT.test(value)) fail('request_invalid');
  return value;
}

function digestParts(parts) {
  const hash = createHash('sha256');
  for (const part of parts) {
    const bytes = Buffer.from(part, 'utf8');
    const size = Buffer.alloc(8);
    size.writeBigUInt64BE(BigInt(bytes.byteLength));
    hash.update(size);
    hash.update(bytes);
  }
  return hash.digest('hex');
}

function ownerDigest(...parts) {
  return digestParts(parts);
}

function opaqueDigest(namespace, ...parts) {
  return digestParts([namespace, ...parts]);
}

function exactTeamOwnerBinding(binding) {
  if (!binding || binding.mode !== 'team' || binding.fallback !== false || !binding.commons) {
    fail('binding_required');
  }
  const teamID = safeID(binding.commons.team_id, 'binding_required');
  const storeID = safeID(binding.commons.store_id, 'binding_required');
  const principalID = safeID(binding.principal_ref, 'binding_required');
  let resource;
  try { resource = new URL(binding.commons.resource); } catch { fail('binding_required'); }
  if (resource.protocol !== 'https:' || resource.username || resource.password || resource.search ||
      resource.hash || resource.pathname !== '/mcp') fail('binding_required');
  return Object.freeze({
    teamID, storeID, principalID,
    origin: resource.origin,
    credentialRef: `keychain:pulse/team-owner/${teamID}/${principalID}`,
  });
}

export function ownerCredentialRef(binding) {
  return exactTeamOwnerBinding(binding).credentialRef;
}

function cleanMutation(input, operation) {
  exactKeys(input, ['action', ...operation.fields]);
  const mutation = {};
  for (const field of operation.fields) mutation[field] = input[field];
  if (input.action === 'membership.create') {
    mutation.issuer = exactIssuer(mutation.issuer);
    mutation.subject = exactIdentity(mutation.subject);
    if (!['owner', 'member', 'reviewer'].includes(mutation.role)) fail('request_invalid');
  } else if (input.action === 'agent_binding.create') {
    mutation.issuer = exactIssuer(mutation.issuer);
    mutation.subject = exactIdentity(mutation.subject);
    mutation.client_id = exactIdentity(mutation.client_id);
  } else if (input.action === 'project.create') {
    mutation.name = displayName(mutation.name);
  } else if (input.action === 'project_grant.create') {
    mutation.project_id = safeID(mutation.project_id);
    mutation.target_principal_id = safeID(mutation.target_principal_id);
    if (!['read', 'write', 'admin'].includes(mutation.access_level)) fail('request_invalid');
  } else {
    mutation.target_id = safeID(mutation.target_id);
  }
  return Object.freeze(mutation);
}

export function buildTeamOwnerOperation(binding, input) {
  const trusted = exactTeamOwnerBinding(binding);
  if (!input || typeof input.action !== 'string' || !OPERATIONS[input.action]) fail('action_invalid');
  const contract = OPERATIONS[input.action];
  const mutation = cleanMutation(input, contract);
  const normalized = { action: input.action, ...mutation };
  const [targetKind, targetID, targetDigest] = contract.target(normalized);
  return Object.freeze({
    path: contract.path,
    schema: contract.schema,
    resultSchema: contract.result,
    approval: Object.freeze({
      schema: 'pulse.team.owner.approval.v1',
      action: input.action,
      store_id: trusted.storeID,
      team_id: trusted.teamID,
      target_kind: targetKind,
      target_id: targetID,
      target_digest: targetDigest,
      mutation,
    }),
    execution: Object.freeze({
      schema: contract.schema,
      action: input.action,
      approval_nonce: null,
      ...mutation,
    }),
  });
}

export function buildTeamOwnerStepUp(binding, input, { randomBytes = cryptoRandomBytes } = {}) {
  if (typeof randomBytes !== 'function') fail('runtime_unavailable');
  const operation = buildTeamOwnerOperation(binding, input);
  let challenge;
  try { challenge = Buffer.from(randomBytes(32)).toString('base64url'); } catch { fail('runtime_unavailable'); }
  if (!/^[A-Za-z0-9_-]{43}$/.test(challenge)) fail('runtime_unavailable');
  const approvalText = JSON.stringify(operation.approval);
  const nonce = createHash('sha256')
    .update(OWNER_OPERATION_STEP_UP_NAMESPACE, 'utf8').update('\0')
    .update(challenge, 'utf8').update('\0').update(approvalText, 'utf8')
    .digest('base64url');
  return Object.freeze({ operation, approvalText, challenge, nonce });
}

function requireDigest(value) {
  if (typeof value !== 'string' || !DIGEST.test(value)) fail('response_invalid');
  return value;
}

function validateApproval(value, request) {
  exactKeys(value, [
    'schema', 'approval_nonce', 'action', 'store_id', 'team_id', 'target_kind',
    'target_id', 'expires_at', 'fallback',
  ], 'response_invalid');
  if (value.schema !== 'pulse.team.owner.approval_result.v1' || value.fallback !== false ||
      value.action !== request.action || value.store_id !== request.store_id || value.team_id !== request.team_id ||
      value.target_kind !== request.target_kind || value.target_id !== request.target_id ||
      typeof value.expires_at !== 'string' || !Number.isFinite(Date.parse(value.expires_at))) fail('response_invalid');
  return requireDigest(value.approval_nonce);
}

function commonResult(value, operation, extraField) {
  exactKeys(value, [
    'schema', 'action', 'audit_event_id', 'auth_epoch', 'status', extraField, 'fallback',
  ], 'response_invalid');
  if (value.schema !== operation.resultSchema || value.action !== operation.approval.action ||
      value.status !== 'complete' || value.fallback !== false || !Number.isSafeInteger(value.auth_epoch) ||
      value.auth_epoch < 1) fail('response_invalid');
  safeID(value.audit_event_id, 'response_invalid');
  return value;
}

function validateExecution(value, operation) {
  const action = operation.approval.action;
  let targetID;
  let principalID;
  if (action === 'membership.create') {
    const result = commonResult(value, operation, 'member');
    exactKeys(result.member, ['principal_id', 'membership_id', 'role', 'auth_epoch'], 'response_invalid');
    if (result.member.role !== operation.approval.mutation.role ||
        !Number.isSafeInteger(result.member.auth_epoch) || result.member.auth_epoch < 1) fail('response_invalid');
    principalID = safeID(result.member.principal_id, 'response_invalid');
    targetID = safeID(result.member.membership_id, 'response_invalid');
  } else if (action === 'membership.revoke' || action === 'agent_binding.revoke' ||
      action === 'project_grant.revoke') {
    const result = commonResult(value, operation, 'target_id');
    targetID = safeID(result.target_id, 'response_invalid');
    if (targetID !== operation.approval.mutation.target_id) fail('response_invalid');
  } else if (action === 'agent_binding.create') {
    const result = commonResult(value, operation, 'binding');
    exactKeys(result.binding, [
      'binding_id', 'human_principal_id', 'agent_principal_id', 'auth_epoch',
    ], 'response_invalid');
    if (!Number.isSafeInteger(result.binding.auth_epoch) || result.binding.auth_epoch < 1) {
      fail('response_invalid');
    }
    targetID = safeID(result.binding.binding_id, 'response_invalid');
    safeID(result.binding.human_principal_id, 'response_invalid');
    principalID = safeID(result.binding.agent_principal_id, 'response_invalid');
  } else if (action === 'project.create') {
    const result = commonResult(value, operation, 'project');
    exactKeys(result.project, [
      'project_id', 'team_id', 'name', 'owner_principal_id', 'created_by_principal_id',
    ], 'response_invalid');
    if (result.project.team_id !== operation.approval.team_id ||
        result.project.name !== operation.approval.mutation.name) fail('response_invalid');
    targetID = safeID(result.project.project_id, 'response_invalid');
    safeID(result.project.owner_principal_id, 'response_invalid');
    safeID(result.project.created_by_principal_id, 'response_invalid');
  } else {
    const result = commonResult(value, operation, 'grant');
    exactKeys(result.grant, [
      'grant_id', 'project_id', 'principal_id', 'access_level', 'auth_epoch',
    ], 'response_invalid');
    if (result.grant.project_id !== operation.approval.mutation.project_id ||
        result.grant.principal_id !== operation.approval.mutation.target_principal_id ||
        result.grant.access_level !== operation.approval.mutation.access_level ||
        !Number.isSafeInteger(result.grant.auth_epoch) || result.grant.auth_epoch < 1) fail('response_invalid');
    targetID = safeID(result.grant.grant_id, 'response_invalid');
    principalID = safeID(result.grant.principal_id, 'response_invalid');
  }
  return Object.freeze({
    schema: 'pulse.team.owner_cli_receipt.v1',
    status: 'complete',
    action,
    audit_event_id: value.audit_event_id,
    auth_epoch: value.auth_epoch,
    target_kind: operation.approval.target_kind,
    target_id: targetID,
    ...(principalID ? { principal_id: principalID } : {}),
    fallback: false,
  });
}

function exactStepUp(value) {
  exactKeys(value, ['idToken', 'operationChallenge', 'authorizationStartedAt'], 'step_up_required');
  if (typeof value.idToken !== 'string' || value.idToken.length < 16 || value.idToken.length > 16 * 1024 ||
      !/^[A-Za-z0-9_-]{43}$/.test(value.operationChallenge) ||
      !Number.isSafeInteger(value.authorizationStartedAt) || value.authorizationStartedAt < 1) {
    fail('step_up_required');
  }
  return value;
}

export async function runTeamOwnerOperation(binding, input, { post, stepUp } = {}) {
  if (typeof post !== 'function') fail('runtime_unavailable');
  const operation = buildTeamOwnerOperation(binding, input);
  const approvalResult = await post(APPROVAL_PATH, operation.approval, exactStepUp(stepUp));
  const approvalNonce = validateApproval(approvalResult, operation.approval);
  const execution = { ...operation.execution, approval_nonce: approvalNonce };
  const result = await post(operation.path, execution);
  return validateExecution(result, operation);
}

function ownerPath(value) {
  if (typeof value !== 'string' || !OWNER_PATHS.has(value)) fail('path_invalid');
  return value;
}

async function readResponseJSON(response) {
  let bytes;
  try {
    bytes = Buffer.from(await boundedRemoteRead(() => response.arrayBuffer(), {
      timeoutMs: DEFAULT_NETWORK_TIMEOUT_MS,
      cancel: () => response.body?.cancel(),
    }));
  } catch {
    fail('response_invalid');
  }
  if (bytes.length < 2 || bytes.length > MAX_RESPONSE_BYTES) fail('response_invalid');
  try { return JSON.parse(bytes.toString('utf8')); } catch { fail('response_invalid'); }
}

export function createTeamOwnerRemotePost(binding, {
  credentialStore = createOSCredentialStore(),
  fetch: fetchFn = globalThis.fetch,
  refresh = refreshRemoteCredential,
  buildHeaders = buildSenderConstrainedRemoteHeaders,
  acquireLock = acquireRemoteCredentialLock,
  now = () => Math.floor(Date.now() / 1000),
  networkTimeoutMs = DEFAULT_NETWORK_TIMEOUT_MS,
  signal,
} = {}) {
  const trusted = exactTeamOwnerBinding(binding);
  if (typeof fetchFn !== 'function' || typeof refresh !== 'function' || typeof buildHeaders !== 'function' ||
      typeof acquireLock !== 'function' || typeof now !== 'function') fail('runtime_unavailable');
  return async (path, body, stepUp) => {
    const endpoint = `${trusted.origin}${ownerPath(path)}`;
    let serialized;
    try { serialized = JSON.stringify(body); } catch { fail('request_invalid'); }
    if (typeof serialized !== 'string' || Buffer.byteLength(serialized) > 32 * 1024) fail('request_invalid');
    const send = async (force) => {
      const release = await acquireLock(trusted.credentialRef, { signal });
      try {
        await refresh(credentialStore, trusted.credentialRef, {
          fetch: fetchFn, now: now(), force, networkTimeoutMs, signal,
        });
      } finally {
        await release();
      }
      const sender = buildHeaders(endpoint, {
        method: 'POST', credentialStore, credentialRef: trusted.credentialRef, now: now(), signal,
      });
      const headers = new Headers(sender);
      if (headers.has('X-Pulse-Key')) fail('caller_auth_forbidden');
      headers.set('Content-Type', 'application/json');
      headers.set('Origin', trusted.origin);
      if (path === APPROVAL_PATH) {
        const proof = exactStepUp(stepUp);
        headers.set('X-Pulse-Owner-ID-Token', proof.idToken);
        headers.set('X-Pulse-Owner-Operation-Challenge', proof.operationChallenge);
        headers.set('X-Pulse-Owner-Authorization-Started-At', String(proof.authorizationStartedAt));
      } else if (stepUp !== undefined) {
        fail('step_up_invalid');
      }
      return boundedRemoteFetch(fetchFn, endpoint, {
        method: 'POST', headers, body: serialized, redirect: 'error', signal,
      }, { timeoutMs: networkTimeoutMs, signal });
    };
    let response = await send(false);
    if (response.status === 401 && /(?:^|[,\s])pulse_reauth="refresh"(?:[,\s]|$)/.test(
      response.headers.get('www-authenticate') ?? '',
    )) {
      await response.body?.cancel().catch(() => undefined);
      response = await send(true);
    }
    const value = await readResponseJSON(response);
    if (response.status !== 200) {
      const code = value && typeof value === 'object' && !Array.isArray(value) ? value.error : undefined;
      if (code === 'owner_step_up_required') fail('step_up_required');
      if (code === 'owner_operation_denied') fail('operation_denied');
      if (code === 'invalid_owner_request') fail('request_invalid');
      fail('service_unavailable');
    }
    return value;
  };
}
