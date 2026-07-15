import { createHash } from 'node:crypto';

import type { VerifiedOAuthIdentity } from './oauth-resource.js';
import {
  OWNER_STEP_UP_ASSERTION_VERSION,
  PrincipalSigner,
} from './principal-context.js';

export { OWNER_STEP_UP_ASSERTION_VERSION };

export const OWNER_APPROVAL_PUBLIC_PATH = '/owner/v1/approval';
export const OWNER_BOOTSTRAP_PUBLIC_PATH = '/owner/v1/bootstrap';
export const OWNER_ACTIVATE_PUBLIC_PATH = '/owner/v1/activate';
export const OWNER_MEMBERS_PUBLIC_PATH = '/owner/v1/members';
export const OWNER_BINDINGS_PUBLIC_PATH = '/owner/v1/bindings';
export const OWNER_SERVICES_PUBLIC_PATH = '/owner/v1/services';
export const OWNER_PROJECTS_PUBLIC_PATH = '/owner/v1/projects';
export const OWNER_PROJECT_GRANTS_PUBLIC_PATH = '/owner/v1/project-grants';
export const OWNER_SHARED_DELETE_PUBLIC_PATH = '/owner/v1/shared-delete';
export const OWNER_AUDIT_PUBLIC_PATH = '/owner/v1/audit';
export const OWNER_DELETION_STATUS_PUBLIC_PATH = '/owner/v1/deletion-status';

export const OWNER_APPROVAL_INTERNAL_PATH = '/team/v1/owner/approval';
export const OWNER_BOOTSTRAP_INTERNAL_PATH = '/team/v1/owner/bootstrap';
export const OWNER_ACTIVATE_INTERNAL_PATH = '/team/v1/owner/activate';
export const OWNER_MEMBERS_INTERNAL_PATH = '/team/v1/owner/members';
export const OWNER_BINDINGS_INTERNAL_PATH = '/team/v1/owner/bindings';
export const OWNER_SERVICES_INTERNAL_PATH = '/team/v1/owner/services';
export const OWNER_PROJECTS_INTERNAL_PATH = '/team/v1/owner/projects';
export const OWNER_PROJECT_GRANTS_INTERNAL_PATH = '/team/v1/owner/project-grants';
export const OWNER_SHARED_DELETE_INTERNAL_PATH = '/team/v1/owner/shared-delete';
export const OWNER_AUDIT_INTERNAL_PATH = '/team/v1/owner/audit';
export const OWNER_DELETION_STATUS_INTERNAL_PATH = '/team/v1/owner/deletion-status';

const OWNER_APPROVAL_SCHEMA = 'pulse.team.owner.approval.v1';
const OWNER_APPROVAL_RESULT_SCHEMA = 'pulse.team.owner.approval_result.v1';
const OWNER_BOOTSTRAP_SCHEMA = 'pulse.team.owner.bootstrap.v1';
const OWNER_BOOTSTRAP_RESULT_SCHEMA = 'pulse.team.owner.bootstrap_result.v1';
const OWNER_ACTIVATE_SCHEMA = 'pulse.team.owner.activate.v1';
const OWNER_ACTIVATE_RESULT_SCHEMA = 'pulse.team.owner.activate_result.v1';
const OWNER_MEMBERS_SCHEMA = 'pulse.team.owner.members.v1';
const OWNER_MEMBERS_RESULT_SCHEMA = 'pulse.team.owner.members_result.v1';
const OWNER_BINDINGS_SCHEMA = 'pulse.team.owner.bindings.v1';
const OWNER_BINDINGS_RESULT_SCHEMA = 'pulse.team.owner.bindings_result.v1';
const OWNER_SERVICES_SCHEMA = 'pulse.team.owner.services.v1';
const OWNER_SERVICES_RESULT_SCHEMA = 'pulse.team.owner.services_result.v1';
const OWNER_PROJECTS_SCHEMA = 'pulse.team.owner.projects.v1';
const OWNER_PROJECTS_RESULT_SCHEMA = 'pulse.team.owner.projects_result.v1';
const OWNER_PROJECT_GRANTS_SCHEMA = 'pulse.team.owner.project_grants.v1';
const OWNER_PROJECT_GRANTS_RESULT_SCHEMA = 'pulse.team.owner.project_grants_result.v1';
const OWNER_SHARED_DELETE_SCHEMA = 'pulse.team.owner.shared_delete.v1';
const OWNER_SHARED_DELETE_RESULT_SCHEMA = 'pulse.team.owner.shared_delete_result.v1';
const OWNER_AUDIT_SCHEMA = 'pulse.team.owner.audit.v1';
const OWNER_AUDIT_RESULT_SCHEMA = 'pulse.team.owner.audit_result.v1';
const OWNER_DELETION_STATUS_SCHEMA = 'pulse.team.owner.deletion_status.v1';
const OWNER_DELETION_STATUS_RESULT_SCHEMA = 'pulse.team.owner.deletion_status_result.v1';
const OWNER_MAX_BODY_BYTES = 64 * 1024;
const OWNER_MAX_RESPONSE_BYTES = 64 * 1024;
const OWNER_CAPABILITY = 'pulse:owner';
const OWNER_OPERATION_STEP_UP_NAMESPACE = 'pulse.team.owner.operation.step_up.v1';

const OWNER_PUBLIC_PATHS = new Set([
  OWNER_APPROVAL_PUBLIC_PATH,
  OWNER_BOOTSTRAP_PUBLIC_PATH,
  OWNER_ACTIVATE_PUBLIC_PATH,
  OWNER_MEMBERS_PUBLIC_PATH,
  OWNER_BINDINGS_PUBLIC_PATH,
  OWNER_SERVICES_PUBLIC_PATH,
  OWNER_PROJECTS_PUBLIC_PATH,
  OWNER_PROJECT_GRANTS_PUBLIC_PATH,
  OWNER_SHARED_DELETE_PUBLIC_PATH,
  OWNER_AUDIT_PUBLIC_PATH,
  OWNER_DELETION_STATUS_PUBLIC_PATH,
]);
const SAFE_OPAQUE = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$/;
const HEX_DIGEST = /^[0-9a-f]{64}$/;
const UNSAFE_SECRET = /(?:authorization:\s*bearer|api[_-]?key|password|private[_-]?key|begin private key|\bsk-[A-Za-z0-9_-]{12,}\b|\bghp_[A-Za-z0-9_]{12,}\b)/i;
const UNSAFE_PATH = /(?:\/(?:users|home|etc|var|private|volumes)\/|file:\/\/|(?:^|\s)~\/|(?:^|\s)[a-z]:\\|\\\\[^\\\s]+\\)/i;

export type OwnerPublicPath =
  | typeof OWNER_APPROVAL_PUBLIC_PATH
  | typeof OWNER_BOOTSTRAP_PUBLIC_PATH
  | typeof OWNER_ACTIVATE_PUBLIC_PATH
  | typeof OWNER_MEMBERS_PUBLIC_PATH
  | typeof OWNER_BINDINGS_PUBLIC_PATH
  | typeof OWNER_SERVICES_PUBLIC_PATH
  | typeof OWNER_PROJECTS_PUBLIC_PATH
  | typeof OWNER_PROJECT_GRANTS_PUBLIC_PATH
  | typeof OWNER_SHARED_DELETE_PUBLIC_PATH
  | typeof OWNER_AUDIT_PUBLIC_PATH
  | typeof OWNER_DELETION_STATUS_PUBLIC_PATH;

export type OwnerGatewayErrorCode =
  | 'invalid_owner_request'
  | 'owner_step_up_required'
  | 'owner_operation_denied'
  | 'owner_service_unavailable';

const OWNER_GATEWAY_ERRORS: Record<
  OwnerGatewayErrorCode,
  { message: string; status: 400 | 403 | 503 }
> = {
  invalid_owner_request: { message: 'Owner request is invalid', status: 400 },
  owner_step_up_required: { message: 'Recent browser approval is required', status: 403 },
  owner_operation_denied: { message: 'Owner operation was denied', status: 403 },
  owner_service_unavailable: { message: 'Owner service is unavailable', status: 503 },
};

export class OwnerGatewayError extends Error {
  readonly code: OwnerGatewayErrorCode;
  readonly status: 400 | 403 | 503;

  constructor(code: OwnerGatewayErrorCode) {
    const details = OWNER_GATEWAY_ERRORS[code];
    super(details.message);
    this.name = 'OwnerGatewayError';
    this.code = code;
    this.status = details.status;
  }
}

export interface OwnerBootstrapIntent {
  store_id: string;
  team_id: string;
  owner_principal_id: string;
  owner_membership_id: string;
}

export interface CleanOwnerBootstrapApproval {
  schema: typeof OWNER_APPROVAL_SCHEMA;
  action: 'team.bootstrap';
  store_id: string;
  team_id: string;
  target_kind: 'team';
  target_id: string;
  target_digest: string;
  team_name: string;
  bootstrap_intent: OwnerBootstrapIntent;
}

export interface CleanOwnerActivationApproval {
  schema: typeof OWNER_APPROVAL_SCHEMA;
  action: 'team.activation.synthetic';
  store_id: string;
  team_id: string;
  target_kind: 'team_activation';
  target_id: string;
  target_digest: string;
  gate_digest: string;
}

export type OwnerAdminAction =
  | 'membership.create'
  | 'membership.revoke'
  | 'agent_binding.create'
  | 'agent_binding.revoke'
  | 'service_principal.create'
  | 'service_principal.revoke'
  | 'project.create'
  | 'project_grant.create'
  | 'project_grant.revoke';

export type CleanOwnerAdminMutation =
  | { action: 'membership.create'; issuer: string; subject: string; role: 'owner' | 'member' | 'reviewer' }
  | { action: 'membership.revoke'; target_id: string }
  | { action: 'agent_binding.create'; issuer: string; subject: string; client_id: string }
  | { action: 'agent_binding.revoke'; target_id: string }
  | { action: 'service_principal.create'; issuer: string; client_id: string }
  | { action: 'service_principal.revoke'; target_id: string }
  | { action: 'project.create'; name: string }
  | {
      action: 'project_grant.create'; project_id: string;
      target_principal_id: string; access_level: 'read' | 'write' | 'admin';
    }
  | { action: 'project_grant.revoke'; target_id: string };

export interface CleanOwnerAdminApproval {
  schema: typeof OWNER_APPROVAL_SCHEMA;
  action: OwnerAdminAction;
  store_id: string;
  team_id: string;
  target_kind: string;
  target_id: string;
  target_digest: string;
  mutation: Omit<CleanOwnerAdminMutation, 'action'>;
}

export interface CleanOwnerSharedDeleteApproval {
  schema: typeof OWNER_APPROVAL_SCHEMA;
  action: 'team.object.delete.shared';
  store_id: string;
  team_id: string;
  target_kind: 'team_object';
  target_id: string;
  target_digest: string;
}

export interface CleanOwnerAuditApproval {
  schema: typeof OWNER_APPROVAL_SCHEMA;
  action: 'team.audit.inspect';
  store_id: string;
  team_id: string;
  target_kind: 'team_audit';
  target_id: string;
  target_digest: string;
  cursor?: string;
  limit: number;
}

export interface CleanOwnerDeletionStatusApproval {
  schema: typeof OWNER_APPROVAL_SCHEMA;
  action: 'team.deletion.status';
  store_id: string;
  team_id: string;
  target_kind: 'deletion_operation';
  target_id: string;
  target_digest: string;
  operation_id: string;
}

export type CleanOwnerApproval =
  | CleanOwnerBootstrapApproval
  | CleanOwnerActivationApproval
  | CleanOwnerAdminApproval
  | CleanOwnerSharedDeleteApproval
  | CleanOwnerAuditApproval
  | CleanOwnerDeletionStatusApproval;

export type CleanOwnerBootstrap =
  | { schema: typeof OWNER_BOOTSTRAP_SCHEMA; operation: 'prepare' }
  | {
      schema: typeof OWNER_BOOTSTRAP_SCHEMA;
      operation: 'execute';
      team_name: string;
      bootstrap_intent: OwnerBootstrapIntent;
      approval_nonce: string;
    };

export interface CleanOwnerActivate {
  schema: typeof OWNER_ACTIVATE_SCHEMA;
  approval_nonce: string;
  gate_digest: string;
}

export interface CleanOwnerSharedDelete {
  schema: typeof OWNER_SHARED_DELETE_SCHEMA;
  object_id: string;
  idempotency_key: string;
  approval_nonce: string;
}

export interface CleanOwnerAudit {
  schema: typeof OWNER_AUDIT_SCHEMA;
  approval_nonce: string;
  cursor?: string;
  limit: number;
}

export interface CleanOwnerDeletionStatus {
  schema: typeof OWNER_DELETION_STATUS_SCHEMA;
  approval_nonce: string;
  operation_id: string;
}

interface CanonicalOwnerBody<T> {
  value: T;
  text: string;
  bytes: Buffer;
}

export function isOwnerPublicPath(value: string): value is OwnerPublicPath {
  return OWNER_PUBLIC_PATHS.has(value);
}

export function isExactOwnerBrowserRequest(input: {
  origin: string;
  host: string;
  publicBaseURL: string;
  allowedOrigins: ReadonlySet<string>;
}): boolean {
  try {
    const base = new URL(input.publicBaseURL);
    const origin = new URL(input.origin);
    return input.publicBaseURL === base.origin && base.protocol === 'https:' &&
      input.host === base.host && input.origin === origin.origin &&
      input.allowedOrigins.has(input.origin);
  } catch {
    return false;
  }
}

export function canonicalOwnerApprovalBody(input: unknown): CanonicalOwnerBody<CleanOwnerApproval> {
  const envelope = ownerRecord(input, [
    'schema', 'action', 'store_id', 'team_id', 'target_kind', 'target_id',
    'target_digest', 'team_name', 'bootstrap_intent', 'gate_digest', 'mutation',
    'cursor', 'limit', 'operation_id',
  ]);
  if (envelope.schema !== OWNER_APPROVAL_SCHEMA) invalidOwnerRequest();
  const action = envelope.action;
  const storeID = ownerOpaque(envelope.store_id);
  const teamID = ownerOpaque(envelope.team_id);
  const targetID = ownerOpaque(envelope.target_id);
  const targetDigest = ownerDigest(envelope.target_digest);
  if (action === 'team.bootstrap') {
    if (
      envelope.target_kind !== 'team' || envelope.gate_digest !== undefined ||
      envelope.mutation !== undefined || envelope.cursor !== undefined ||
      envelope.limit !== undefined || envelope.operation_id !== undefined || targetID !== teamID
    ) invalidOwnerRequest();
    const intent = ownerBootstrapIntent(envelope.bootstrap_intent);
    const teamName = ownerTeamName(envelope.team_name);
    if (
      intent.store_id !== storeID || intent.team_id !== teamID ||
      targetDigest !== ownerBootstrapTargetDigest(intent, teamName)
    ) invalidOwnerRequest();
    return canonicalOwnerBody({
      schema: OWNER_APPROVAL_SCHEMA,
      action,
      store_id: storeID,
      team_id: teamID,
      target_kind: 'team',
      target_id: targetID,
      target_digest: targetDigest,
      team_name: teamName,
      bootstrap_intent: intent,
    });
  }
  if (action === 'team.activation.synthetic') {
    if (
      envelope.target_kind !== 'team_activation' || envelope.team_name !== undefined ||
      envelope.bootstrap_intent !== undefined || envelope.mutation !== undefined ||
      envelope.cursor !== undefined || envelope.limit !== undefined ||
      envelope.operation_id !== undefined || targetID !== teamID
    ) invalidOwnerRequest();
    const gateDigest = ownerDigest(envelope.gate_digest);
    if (targetDigest !== ownerActivationTargetDigest(storeID, teamID, gateDigest)) {
      invalidOwnerRequest();
    }
    return canonicalOwnerBody({
      schema: OWNER_APPROVAL_SCHEMA,
      action,
      store_id: storeID,
      team_id: teamID,
      target_kind: 'team_activation',
      target_id: targetID,
      target_digest: targetDigest,
      gate_digest: gateDigest,
    });
  }
  if (action === 'team.object.delete.shared') {
    if (
      envelope.target_kind !== 'team_object' || envelope.team_name !== undefined ||
      envelope.bootstrap_intent !== undefined || envelope.gate_digest !== undefined ||
      envelope.mutation !== undefined || envelope.cursor !== undefined ||
      envelope.limit !== undefined || envelope.operation_id !== undefined ||
      targetDigest !== ownerSharedDeletionTargetDigest(targetID)
    ) invalidOwnerRequest();
    return canonicalOwnerBody({
      schema: OWNER_APPROVAL_SCHEMA,
      action,
      store_id: storeID,
      team_id: teamID,
      target_kind: 'team_object',
      target_id: targetID,
      target_digest: targetDigest,
    });
  }
  if (action === 'team.audit.inspect') {
    if (
      envelope.target_kind !== 'team_audit' || targetID !== teamID ||
      envelope.team_name !== undefined || envelope.bootstrap_intent !== undefined ||
      envelope.gate_digest !== undefined || envelope.mutation !== undefined ||
      envelope.operation_id !== undefined
    ) invalidOwnerRequest();
    const cursor = optionalOwnerOpaque(envelope.cursor);
    const limit = ownerLimit(envelope.limit);
    if (targetDigest !== ownerAuditTargetDigest(cursor ?? '', limit)) invalidOwnerRequest();
    return canonicalOwnerBody({
      schema: OWNER_APPROVAL_SCHEMA,
      action,
      store_id: storeID,
      team_id: teamID,
      target_kind: 'team_audit',
      target_id: targetID,
      target_digest: targetDigest,
      ...(cursor === undefined ? {} : { cursor }),
      limit,
    });
  }
  if (action === 'team.deletion.status') {
    if (
      envelope.target_kind !== 'deletion_operation' ||
      envelope.team_name !== undefined || envelope.bootstrap_intent !== undefined ||
      envelope.gate_digest !== undefined || envelope.mutation !== undefined ||
      envelope.cursor !== undefined || envelope.limit !== undefined
    ) invalidOwnerRequest();
    const operationID = ownerOpaque(envelope.operation_id);
    if (
      operationID !== targetID || targetDigest !== ownerDeletionStatusTargetDigest(operationID)
    ) invalidOwnerRequest();
    return canonicalOwnerBody({
      schema: OWNER_APPROVAL_SCHEMA,
      action,
      store_id: storeID,
      team_id: teamID,
      target_kind: 'deletion_operation',
      target_id: targetID,
      target_digest: targetDigest,
      operation_id: operationID,
    });
  }
  const mutation = ownerAdminMutation(action, envelope.mutation);
  if (
    envelope.team_name !== undefined || envelope.bootstrap_intent !== undefined ||
    envelope.gate_digest !== undefined || envelope.cursor !== undefined ||
    envelope.limit !== undefined || envelope.operation_id !== undefined
  ) invalidOwnerRequest();
  const target = ownerAdminMutationTarget(mutation.action, mutationPayload(mutation));
  if (
    envelope.target_kind !== target.kind || targetID !== target.id ||
    targetDigest !== target.digest
  ) invalidOwnerRequest();
  return canonicalOwnerBody({
    schema: OWNER_APPROVAL_SCHEMA,
    action: mutation.action,
    store_id: storeID,
    team_id: teamID,
    target_kind: target.kind,
    target_id: target.id,
    target_digest: target.digest,
    mutation: mutationPayload(mutation),
  });
}

export function ownerBootstrapTargetDigest(
  intent: OwnerBootstrapIntent,
  teamName: string,
): string {
  const cleanIntent = ownerBootstrapIntent(intent);
  const cleanTeamName = ownerTeamName(teamName);
  return ownerApprovalDigest(
    'team.bootstrap', cleanIntent.store_id, cleanIntent.team_id,
    cleanIntent.owner_principal_id, cleanIntent.owner_membership_id, cleanTeamName,
  );
}

export function ownerActivationTargetDigest(
  storeID: string,
  teamID: string,
  gateDigest: string,
): string {
  return ownerApprovalDigest(
    'team.activation.synthetic', ownerOpaque(storeID), ownerOpaque(teamID),
    ownerDigest(gateDigest),
  );
}

export function ownerSharedDeletionTargetDigest(objectID: string): string {
  return ownerApprovalDigest('team.object.delete.shared', ownerOpaque(objectID));
}

export function ownerAuditTargetDigest(cursor: string, limit: number): string {
  const cleanCursor = cursor === '' ? '' : ownerOpaque(cursor);
  return ownerApprovalDigest('team.audit.inspect', cleanCursor, String(ownerLimit(limit)));
}

export function ownerDeletionStatusTargetDigest(operationID: string): string {
  return ownerApprovalDigest('team.deletion.status', ownerOpaque(operationID));
}

export function ownerOperationStepUpNonce(input: unknown, challenge: string): string {
  if (!/^[A-Za-z0-9_-]{43}$/.test(challenge)) invalidOwnerRequest();
  const canonical = canonicalOwnerApprovalBody(input);
  return createHash('sha256')
    .update(OWNER_OPERATION_STEP_UP_NAMESPACE, 'utf8').update('\0')
    .update(challenge, 'utf8').update('\0').update(canonical.text, 'utf8')
    .digest('base64url');
}

export function ownerAdminMutationTarget(
  action: string,
  input: unknown,
): { kind: string; id: string; digest: string } {
  const mutation = ownerAdminMutation(action, input);
  switch (mutation.action) {
    case 'membership.create': {
      const id = ownerOpaqueKey('human-identity-v1', mutation.issuer, mutation.subject);
      return { kind: 'membership', id, digest: ownerApprovalDigest(mutation.action, id, mutation.role) };
    }
    case 'membership.revoke':
      return ownerTarget('membership', mutation.action, mutation.target_id);
    case 'agent_binding.create': {
      const id = ownerOpaqueKey(
        'agent-binding-v1', mutation.issuer, mutation.subject, mutation.client_id,
      );
      return { kind: 'agent_binding', id, digest: ownerApprovalDigest(mutation.action, id) };
    }
    case 'agent_binding.revoke':
      return ownerTarget('agent_binding', mutation.action, mutation.target_id);
    case 'service_principal.create': {
      const id = ownerOpaqueKey('service-identity-v1', mutation.issuer, mutation.client_id);
      return { kind: 'service_principal', id, digest: ownerApprovalDigest(mutation.action, id) };
    }
    case 'service_principal.revoke':
      return ownerTarget('service_principal', mutation.action, mutation.target_id);
    case 'project.create': {
      const id = ownerApprovalDigest('project-name-v1', mutation.name);
      return { kind: 'project', id, digest: ownerApprovalDigest(mutation.action, id) };
    }
    case 'project_grant.create': {
      const id = ownerApprovalDigest(
        'project-grant-v1', mutation.project_id, mutation.target_principal_id,
      );
      return {
        kind: 'project_grant', id,
        digest: ownerApprovalDigest(
          mutation.action, mutation.project_id, mutation.target_principal_id, mutation.access_level,
        ),
      };
    }
    case 'project_grant.revoke':
      return ownerTarget('project_grant', mutation.action, mutation.target_id);
  }
}

function ownerTarget(kind: string, action: OwnerAdminAction, id: string) {
  return { kind, id, digest: ownerApprovalDigest(action, id) };
}

function ownerOpaqueKey(namespace: string, ...parts: string[]): string {
  return ownerApprovalDigest(namespace, ...parts);
}

function ownerApprovalDigest(...parts: string[]): string {
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

export function canonicalOwnerBootstrapBody(input: unknown): CanonicalOwnerBody<CleanOwnerBootstrap> {
  const envelope = ownerRecord(input, [
    'schema', 'operation', 'team_name', 'bootstrap_intent', 'approval_nonce',
  ]);
  if (envelope.schema !== OWNER_BOOTSTRAP_SCHEMA) invalidOwnerRequest();
  if (envelope.operation === 'prepare') {
    if (
      envelope.team_name !== undefined || envelope.bootstrap_intent !== undefined ||
      envelope.approval_nonce !== undefined
    ) invalidOwnerRequest();
    return canonicalOwnerBody({ schema: OWNER_BOOTSTRAP_SCHEMA, operation: 'prepare' });
  }
  if (envelope.operation !== 'execute') invalidOwnerRequest();
  return canonicalOwnerBody({
    schema: OWNER_BOOTSTRAP_SCHEMA,
    operation: 'execute',
    team_name: ownerTeamName(envelope.team_name),
    bootstrap_intent: ownerBootstrapIntent(envelope.bootstrap_intent),
    approval_nonce: ownerDigest(envelope.approval_nonce),
  });
}

export function canonicalOwnerActivateBody(input: unknown): CanonicalOwnerBody<CleanOwnerActivate> {
  const envelope = ownerRecord(input, ['schema', 'approval_nonce', 'gate_digest']);
  if (envelope.schema !== OWNER_ACTIVATE_SCHEMA) invalidOwnerRequest();
  return canonicalOwnerBody({
    schema: OWNER_ACTIVATE_SCHEMA,
    approval_nonce: ownerDigest(envelope.approval_nonce),
    gate_digest: ownerDigest(envelope.gate_digest),
  });
}

type OwnerAdminPublicPath =
  | typeof OWNER_MEMBERS_PUBLIC_PATH
  | typeof OWNER_BINDINGS_PUBLIC_PATH
  | typeof OWNER_SERVICES_PUBLIC_PATH
  | typeof OWNER_PROJECTS_PUBLIC_PATH
  | typeof OWNER_PROJECT_GRANTS_PUBLIC_PATH;

const OWNER_ADMIN_ROUTES: Record<
  OwnerAdminPublicPath,
  { schema: string; resultSchema: string; actions: readonly OwnerAdminAction[] }
> = {
  [OWNER_MEMBERS_PUBLIC_PATH]: {
    schema: OWNER_MEMBERS_SCHEMA, resultSchema: OWNER_MEMBERS_RESULT_SCHEMA,
    actions: ['membership.create', 'membership.revoke'],
  },
  [OWNER_BINDINGS_PUBLIC_PATH]: {
    schema: OWNER_BINDINGS_SCHEMA, resultSchema: OWNER_BINDINGS_RESULT_SCHEMA,
    actions: ['agent_binding.create', 'agent_binding.revoke'],
  },
  [OWNER_SERVICES_PUBLIC_PATH]: {
    schema: OWNER_SERVICES_SCHEMA, resultSchema: OWNER_SERVICES_RESULT_SCHEMA,
    actions: ['service_principal.create', 'service_principal.revoke'],
  },
  [OWNER_PROJECTS_PUBLIC_PATH]: {
    schema: OWNER_PROJECTS_SCHEMA, resultSchema: OWNER_PROJECTS_RESULT_SCHEMA,
    actions: ['project.create'],
  },
  [OWNER_PROJECT_GRANTS_PUBLIC_PATH]: {
    schema: OWNER_PROJECT_GRANTS_SCHEMA, resultSchema: OWNER_PROJECT_GRANTS_RESULT_SCHEMA,
    actions: ['project_grant.create', 'project_grant.revoke'],
  },
};

export function canonicalOwnerAdminMutationBody(
  path: OwnerAdminPublicPath,
  input: unknown,
): CanonicalOwnerBody<Record<string, unknown>> {
  const route = OWNER_ADMIN_ROUTES[path];
  if (!route) invalidOwnerRequest();
  const envelope = ownerRecord(input, [
    'schema', 'action', 'approval_nonce', 'issuer', 'subject', 'client_id', 'role',
    'name', 'target_id', 'project_id', 'target_principal_id', 'access_level',
  ]);
  if (envelope.schema !== route.schema || typeof envelope.action !== 'string') invalidOwnerRequest();
  const mutation = ownerAdminMutation(envelope.action, envelope, true);
  if (!route.actions.includes(mutation.action)) invalidOwnerRequest();
  return canonicalOwnerBody({
    schema: route.schema,
    action: mutation.action,
    approval_nonce: ownerDigest(envelope.approval_nonce),
    ...mutationPayload(mutation),
  });
}

export function canonicalOwnerSharedDeleteBody(
  input: unknown,
): CanonicalOwnerBody<CleanOwnerSharedDelete> {
  const envelope = ownerRecord(input, [
    'schema', 'object_id', 'idempotency_key', 'approval_nonce',
  ]);
  if (envelope.schema !== OWNER_SHARED_DELETE_SCHEMA) invalidOwnerRequest();
  return canonicalOwnerBody({
    schema: OWNER_SHARED_DELETE_SCHEMA,
    object_id: ownerOpaque(envelope.object_id),
    idempotency_key: ownerIdempotencyKey(envelope.idempotency_key),
    approval_nonce: ownerDigest(envelope.approval_nonce),
  });
}

export function canonicalOwnerAuditBody(input: unknown): CanonicalOwnerBody<CleanOwnerAudit> {
  const envelope = ownerRecord(input, ['schema', 'approval_nonce', 'cursor', 'limit']);
  if (envelope.schema !== OWNER_AUDIT_SCHEMA) invalidOwnerRequest();
  const cursor = optionalOwnerOpaque(envelope.cursor);
  return canonicalOwnerBody({
    schema: OWNER_AUDIT_SCHEMA,
    approval_nonce: ownerDigest(envelope.approval_nonce),
    ...(cursor === undefined ? {} : { cursor }),
    limit: ownerLimit(envelope.limit),
  });
}

export function canonicalOwnerDeletionStatusBody(
  input: unknown,
): CanonicalOwnerBody<CleanOwnerDeletionStatus> {
  const envelope = ownerRecord(input, ['schema', 'approval_nonce', 'operation_id']);
  if (envelope.schema !== OWNER_DELETION_STATUS_SCHEMA) invalidOwnerRequest();
  return canonicalOwnerBody({
    schema: OWNER_DELETION_STATUS_SCHEMA,
    approval_nonce: ownerDigest(envelope.approval_nonce),
    operation_id: ownerOpaque(envelope.operation_id),
  });
}

function ownerAdminMutation(
  action: unknown,
  input: unknown,
  allowProtocolFields = false,
): CleanOwnerAdminMutation {
  if (typeof action !== 'string') invalidOwnerRequest();
  const value = ownerRecord(input, [
    'issuer', 'subject', 'client_id', 'role', 'name', 'target_id',
    'project_id', 'target_principal_id', 'access_level',
    ...(allowProtocolFields ? ['schema', 'action', 'approval_nonce'] : []),
  ]);
  switch (action) {
    case 'membership.create': {
      requireOwnerFields(value, ['issuer', 'subject', 'role']);
      const role = value.role;
      if (role !== 'owner' && role !== 'member' && role !== 'reviewer') invalidOwnerRequest();
      return {
        action, issuer: ownerIssuerValue(value.issuer),
        subject: ownerIdentityValue(value.subject), role,
      };
    }
    case 'membership.revoke':
      requireOwnerFields(value, ['target_id']);
      return { action, target_id: ownerOpaque(value.target_id) };
    case 'agent_binding.create':
      requireOwnerFields(value, ['issuer', 'subject', 'client_id']);
      return {
        action, issuer: ownerIssuerValue(value.issuer),
        subject: ownerIdentityValue(value.subject), client_id: ownerIdentityValue(value.client_id),
      };
    case 'agent_binding.revoke':
      requireOwnerFields(value, ['target_id']);
      return { action, target_id: ownerOpaque(value.target_id) };
    case 'service_principal.create':
      requireOwnerFields(value, ['issuer', 'client_id']);
      return {
        action, issuer: ownerIssuerValue(value.issuer),
        client_id: ownerIdentityValue(value.client_id),
      };
    case 'service_principal.revoke':
      requireOwnerFields(value, ['target_id']);
      return { action, target_id: ownerOpaque(value.target_id) };
    case 'project.create':
      requireOwnerFields(value, ['name']);
      return { action, name: ownerTeamName(value.name) };
    case 'project_grant.create': {
      requireOwnerFields(value, ['project_id', 'target_principal_id', 'access_level']);
      const accessLevel = value.access_level;
      if (accessLevel !== 'read' && accessLevel !== 'write' && accessLevel !== 'admin') {
        invalidOwnerRequest();
      }
      return {
        action, project_id: ownerOpaque(value.project_id),
        target_principal_id: ownerOpaque(value.target_principal_id), access_level: accessLevel,
      };
    }
    case 'project_grant.revoke':
      requireOwnerFields(value, ['target_id']);
      return { action, target_id: ownerOpaque(value.target_id) };
    default:
      invalidOwnerRequest();
  }
}

function mutationPayload(mutation: CleanOwnerAdminMutation): Record<string, string> {
  const { action: _action, ...payload } = mutation;
  return payload;
}

function requireOwnerFields(value: Record<string, unknown>, required: readonly string[]): void {
  const protocol = new Set(['schema', 'action', 'approval_nonce']);
  const wanted = new Set(required);
  for (const [key, entry] of Object.entries(value)) {
    if (!protocol.has(key) && !wanted.has(key) && entry !== undefined) invalidOwnerRequest();
  }
  if (required.some((key) => value[key] === undefined)) invalidOwnerRequest();
}

function canonicalOwnerBody<T>(value: T): CanonicalOwnerBody<T> {
  const text = JSON.stringify(value);
  const bytes = Buffer.from(text, 'utf8');
  if (bytes.byteLength > OWNER_MAX_BODY_BYTES) invalidOwnerRequest();
  return { value, text, bytes };
}

function ownerRecord(value: unknown, fields: readonly string[]): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) invalidOwnerRequest();
  const result = value as Record<string, unknown>;
  const allowed = new Set(fields);
  if (Object.keys(result).some((key) => !allowed.has(key)) || Object.values(result).some((entry) => entry === null)) {
    invalidOwnerRequest();
  }
  return result;
}

function ownerBootstrapIntent(value: unknown): OwnerBootstrapIntent {
  const intent = ownerRecord(value, [
    'store_id', 'team_id', 'owner_principal_id', 'owner_membership_id',
  ]);
  const result = {
    store_id: ownerOpaque(intent.store_id),
    team_id: ownerOpaque(intent.team_id),
    owner_principal_id: ownerOpaque(intent.owner_principal_id),
    owner_membership_id: ownerOpaque(intent.owner_membership_id),
  };
  if (
    !result.store_id.startsWith('store_') || !result.team_id.startsWith('team_') ||
    !result.owner_principal_id.startsWith('principal_') ||
    !result.owner_membership_id.startsWith('membership_')
  ) invalidOwnerRequest();
  return result;
}

function ownerOpaque(value: unknown): string {
  if (typeof value !== 'string' || !SAFE_OPAQUE.test(value) || unsafeOwnerText(value)) {
    invalidOwnerRequest();
  }
  return value;
}

function optionalOwnerOpaque(value: unknown): string | undefined {
  return value === undefined ? undefined : ownerOpaque(value);
}

function ownerLimit(value: unknown): number {
  if (!Number.isSafeInteger(value) || (value as number) < 1 || (value as number) > 50) {
    invalidOwnerRequest();
  }
  return value as number;
}

function ownerIdentityValue(value: unknown): string {
  if (
    typeof value !== 'string' || value.length === 0 || value.length > 1024 ||
    value.trim() !== value || /[\u0000-\u001f\u007f]/.test(value) || unsafeOwnerText(value)
  ) invalidOwnerRequest();
  return value;
}

function ownerIssuerValue(value: unknown): string {
  const issuer = ownerIdentityValue(value);
  let parsed: URL;
  try {
    parsed = new URL(issuer);
  } catch {
    invalidOwnerRequest();
  }
  if (
    parsed!.protocol !== 'https:' || parsed!.username !== '' || parsed!.password !== '' ||
    parsed!.search !== '' || parsed!.hash !== '' || parsed!.toString() !== issuer
  ) invalidOwnerRequest();
  return issuer;
}

function ownerIdempotencyKey(value: unknown): string {
  if (typeof value !== 'string' || value.length < 8 || !SAFE_OPAQUE.test(value) || unsafeOwnerText(value)) {
    invalidOwnerRequest();
  }
  return value;
}

function ownerDigest(value: unknown): string {
  if (typeof value !== 'string' || !HEX_DIGEST.test(value)) invalidOwnerRequest();
  return value;
}

function ownerTeamName(value: unknown): string {
  if (
    typeof value !== 'string' || value.trim() !== value || value.length === 0 ||
    Array.from(value).length > 128 || value.normalize('NFC') !== value ||
    /[\u0000-\u001f\u007f]/.test(value) || unsafeOwnerText(value)
  ) invalidOwnerRequest();
  return value;
}

function unsafeOwnerText(value: string): boolean {
  return UNSAFE_SECRET.test(value) || UNSAFE_PATH.test(value);
}

function invalidOwnerRequest(): never {
  throw new OwnerGatewayError('invalid_owner_request');
}

export interface OwnerApprovalGatewayOptions {
  daemonBaseURL: string;
  signer: PrincipalSigner;
  expectedOAuthIssuer: string;
  apiKey: () => string;
  fetch?: typeof fetch;
  now?: () => number;
  timeoutMs?: number;
  maxStepUpAgeSeconds?: number;
}

export class OwnerApprovalGateway {
  private readonly endpoints: Record<OwnerPublicPath, string>;
  private readonly signer: PrincipalSigner;
  private readonly expectedOAuthIssuer: string;
  private readonly apiKey: () => string;
  private readonly fetcher: typeof fetch;
  private readonly now: () => number;
  private readonly timeoutMs: number;
  private readonly maxStepUpAgeSeconds: number;

  constructor(options: OwnerApprovalGatewayOptions) {
    this.endpoints = {
      [OWNER_APPROVAL_PUBLIC_PATH]: ownerDaemonEndpoint(options.daemonBaseURL, OWNER_APPROVAL_INTERNAL_PATH),
      [OWNER_BOOTSTRAP_PUBLIC_PATH]: ownerDaemonEndpoint(options.daemonBaseURL, OWNER_BOOTSTRAP_INTERNAL_PATH),
      [OWNER_ACTIVATE_PUBLIC_PATH]: ownerDaemonEndpoint(options.daemonBaseURL, OWNER_ACTIVATE_INTERNAL_PATH),
      [OWNER_MEMBERS_PUBLIC_PATH]: ownerDaemonEndpoint(options.daemonBaseURL, OWNER_MEMBERS_INTERNAL_PATH),
      [OWNER_BINDINGS_PUBLIC_PATH]: ownerDaemonEndpoint(options.daemonBaseURL, OWNER_BINDINGS_INTERNAL_PATH),
      [OWNER_SERVICES_PUBLIC_PATH]: ownerDaemonEndpoint(options.daemonBaseURL, OWNER_SERVICES_INTERNAL_PATH),
      [OWNER_PROJECTS_PUBLIC_PATH]: ownerDaemonEndpoint(options.daemonBaseURL, OWNER_PROJECTS_INTERNAL_PATH),
      [OWNER_PROJECT_GRANTS_PUBLIC_PATH]: ownerDaemonEndpoint(
        options.daemonBaseURL, OWNER_PROJECT_GRANTS_INTERNAL_PATH,
      ),
      [OWNER_SHARED_DELETE_PUBLIC_PATH]: ownerDaemonEndpoint(
        options.daemonBaseURL, OWNER_SHARED_DELETE_INTERNAL_PATH,
      ),
      [OWNER_AUDIT_PUBLIC_PATH]: ownerDaemonEndpoint(
        options.daemonBaseURL, OWNER_AUDIT_INTERNAL_PATH,
      ),
      [OWNER_DELETION_STATUS_PUBLIC_PATH]: ownerDaemonEndpoint(
        options.daemonBaseURL, OWNER_DELETION_STATUS_INTERNAL_PATH,
      ),
    };
    this.signer = options.signer;
    this.expectedOAuthIssuer = ownerIssuerValue(options.expectedOAuthIssuer);
    this.apiKey = options.apiKey;
    this.fetcher = options.fetch ?? fetch;
    this.now = options.now ?? (() => Math.floor(Date.now() / 1000));
    this.timeoutMs = boundedInteger(options.timeoutMs ?? 3_000, 100, 5_000);
    this.maxStepUpAgeSeconds = boundedInteger(options.maxStepUpAgeSeconds ?? 300, 30, 300);
  }

  async call(
    path: OwnerPublicPath,
    identity: Readonly<VerifiedOAuthIdentity>,
    requestId: string,
    input: unknown,
    stepUp?: Readonly<{ assertionJTI: string }>,
  ): Promise<unknown> {
    if (!isOwnerPublicPath(path)) invalidOwnerRequest();
    this.requireRecentOwner(identity);
    if (identity.issuer !== this.expectedOAuthIssuer) {
      throw new OwnerGatewayError('owner_operation_denied');
    }
    const cleanRequestID = ownerRequestID(requestId);
    const canonical = canonicalOwnerRequest(path, input);
    if (path === OWNER_APPROVAL_PUBLIC_PATH) {
      const approval = canonical.value as CleanOwnerApproval;
      if (!stepUp || !/^owner_browser_[A-Za-z0-9_-]{43}$/.test(stepUp.assertionJTI)) {
        throw new OwnerGatewayError('owner_step_up_required');
      }
      if (
        approval.action !== 'team.bootstrap' &&
        (approval.store_id !== this.signer.storeId || approval.team_id !== this.signer.teamId)
      ) invalidOwnerRequest();
      if (
        (approval.action === 'membership.create' ||
          approval.action === 'agent_binding.create' ||
          approval.action === 'service_principal.create') &&
        'issuer' in approval.mutation && approval.mutation.issuer !== this.expectedOAuthIssuer
      ) invalidOwnerRequest();
    }

    const ipcKey = this.apiKey();
    if (ipcKey === '' || Buffer.byteLength(ipcKey, 'utf8') > 512) {
      throw new OwnerGatewayError('owner_service_unavailable');
    }
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      'X-Pulse-Key': ipcKey,
      'X-Pulse-Request-ID': cleanRequestID,
    };
    if (path === OWNER_APPROVAL_PUBLIC_PATH) {
      const approval = canonical.value as CleanOwnerApproval;
      try {
        headers['X-Pulse-Owner-Step-Up'] = await this.signer.signOwnerStepUp({
          requestId: cleanRequestID,
          path: OWNER_APPROVAL_INTERNAL_PATH,
          action: approval.action,
          body: canonical.bytes,
          storeId: approval.store_id,
          teamId: approval.team_id,
          oauthIssuer: identity.issuer,
          oauthSubject: identity.subject,
          oauthClientId: identity.clientId,
          authTime: identity.authTime as number,
          assertionJTI: stepUp!.assertionJTI,
        });
      } catch {
        throw new OwnerGatewayError('owner_step_up_required');
      }
    } else if (stepUp !== undefined) invalidOwnerRequest();

    let response: Response;
    try {
      response = await this.fetcher(this.endpoints[path], {
        method: 'POST',
        redirect: 'error',
        signal: AbortSignal.timeout(this.timeoutMs),
        headers,
        body: canonical.text,
      });
    } catch {
      throw new OwnerGatewayError('owner_service_unavailable');
    }
    if (response.status !== 200) {
      await response.body?.cancel().catch(() => undefined);
      if (response.status === 400) throw new OwnerGatewayError('invalid_owner_request');
      if ([401, 403, 404, 409].includes(response.status)) {
        throw new OwnerGatewayError('owner_operation_denied');
      }
      throw new OwnerGatewayError('owner_service_unavailable');
    }
    const value = await readOwnerResponse(response);
    try {
      if (path === OWNER_APPROVAL_PUBLIC_PATH) {
        return validateOwnerApprovalResult(
          value, canonical.value as CleanOwnerApproval, this.now(),
        );
      }
      if (path === OWNER_BOOTSTRAP_PUBLIC_PATH) {
        return validateOwnerBootstrapResult(value, canonical.value as CleanOwnerBootstrap);
      }
      if (path === OWNER_ACTIVATE_PUBLIC_PATH) {
        return validateOwnerActivateResult(
          value, canonical.value as CleanOwnerActivate, this.signer, this.now(),
        );
      }
      if (isOwnerAdminPublicPath(path)) {
        return validateOwnerAdminMutationResult(
          path, value, canonical.value as Record<string, unknown>, this.signer,
        );
      }
      if (path === OWNER_SHARED_DELETE_PUBLIC_PATH) {
        return validateOwnerSharedDeleteResult(
          value, canonical.value as CleanOwnerSharedDelete,
        );
      }
      if (path === OWNER_AUDIT_PUBLIC_PATH) {
        return validateOwnerAuditResult(value, canonical.value as CleanOwnerAudit, this.signer);
      }
      return validateOwnerDeletionStatusResult(
        value, canonical.value as CleanOwnerDeletionStatus,
      );
    } catch (error) {
      if (error instanceof OwnerGatewayError && error.code === 'owner_service_unavailable') throw error;
      throw new OwnerGatewayError('owner_service_unavailable');
    }
  }

  verifyRecentStepUp(identity: Readonly<VerifiedOAuthIdentity>): void {
    this.requireRecentOwner(identity);
  }

  private requireRecentOwner(identity: Readonly<VerifiedOAuthIdentity>): void {
    const now = this.now();
    if (
      !identity.capabilities.includes(OWNER_CAPABILITY) ||
      !boundedIdentity(identity.issuer) || !boundedIdentity(identity.subject) ||
      !boundedIdentity(identity.clientId) || !Number.isInteger(identity.authTime) ||
      (identity.authTime as number) <= 0 || (identity.authTime as number) > now ||
      now - (identity.authTime as number) > this.maxStepUpAgeSeconds
    ) {
      throw new OwnerGatewayError('owner_step_up_required');
    }
  }
}

function canonicalOwnerRequest(path: OwnerPublicPath, input: unknown): CanonicalOwnerBody<unknown> {
  if (path === OWNER_APPROVAL_PUBLIC_PATH) return canonicalOwnerApprovalBody(input);
  if (path === OWNER_BOOTSTRAP_PUBLIC_PATH) return canonicalOwnerBootstrapBody(input);
  if (path === OWNER_ACTIVATE_PUBLIC_PATH) return canonicalOwnerActivateBody(input);
  if (isOwnerAdminPublicPath(path)) return canonicalOwnerAdminMutationBody(path, input);
  if (path === OWNER_SHARED_DELETE_PUBLIC_PATH) return canonicalOwnerSharedDeleteBody(input);
  if (path === OWNER_AUDIT_PUBLIC_PATH) return canonicalOwnerAuditBody(input);
  if (path === OWNER_DELETION_STATUS_PUBLIC_PATH) return canonicalOwnerDeletionStatusBody(input);
  invalidOwnerRequest();
}

function isOwnerAdminPublicPath(path: OwnerPublicPath): path is OwnerAdminPublicPath {
  return path in OWNER_ADMIN_ROUTES;
}

function validateOwnerApprovalResult(
  value: unknown,
  request: CleanOwnerApproval,
  now: number,
): unknown {
  const result = exactOwnerResponse(value, [
    'schema', 'approval_nonce', 'action', 'store_id', 'team_id', 'target_kind',
    'target_id', 'expires_at', 'fallback',
  ]);
  if (
    result.schema !== OWNER_APPROVAL_RESULT_SCHEMA || result.fallback !== false ||
    result.action !== request.action || result.store_id !== request.store_id ||
    result.team_id !== request.team_id || result.target_kind !== request.target_kind ||
    result.target_id !== request.target_id || !isOwnerDigest(result.approval_nonce)
  ) invalidOwnerResponse();
  const expiresAt = ownerTimestamp(result.expires_at);
  const expiresUnix = Date.parse(expiresAt) / 1000;
  if (expiresUnix <= now || expiresUnix > now + 300) invalidOwnerResponse();
  return {
    schema: OWNER_APPROVAL_RESULT_SCHEMA,
    approval_nonce: result.approval_nonce,
    action: result.action,
    store_id: result.store_id,
    team_id: result.team_id,
    target_kind: result.target_kind,
    target_id: result.target_id,
    expires_at: expiresAt,
    fallback: false,
  };
}

function validateOwnerBootstrapResult(value: unknown, request: CleanOwnerBootstrap): unknown {
  if (request.operation === 'prepare') {
    const result = exactOwnerResponse(value, ['schema', 'operation', 'bootstrap_intent', 'fallback']);
    if (
      result.schema !== OWNER_BOOTSTRAP_RESULT_SCHEMA || result.operation !== 'prepared' ||
      result.fallback !== false
    ) invalidOwnerResponse();
    return {
      schema: OWNER_BOOTSTRAP_RESULT_SCHEMA,
      operation: 'prepared',
      bootstrap_intent: ownerBootstrapIntentResponse(result.bootstrap_intent),
      fallback: false,
    };
  }
  const result = exactOwnerResponse(value, [
    'schema', 'operation', 'store_id', 'team_id', 'owner_principal_id',
    'owner_membership_id', 'activation_state', 'content_boundary', 'public_enabled', 'fallback',
  ]);
  const intent = request.bootstrap_intent;
  if (
    result.schema !== OWNER_BOOTSTRAP_RESULT_SCHEMA || result.operation !== 'complete' ||
    result.store_id !== intent.store_id || result.team_id !== intent.team_id ||
    result.owner_principal_id !== intent.owner_principal_id ||
    result.owner_membership_id !== intent.owner_membership_id ||
    result.activation_state !== 'inactive' || result.content_boundary !== 'synthetic' ||
    result.public_enabled !== false || result.fallback !== false
  ) invalidOwnerResponse();
  return {
    schema: OWNER_BOOTSTRAP_RESULT_SCHEMA,
    operation: 'complete',
    store_id: result.store_id,
    team_id: result.team_id,
    owner_principal_id: result.owner_principal_id,
    owner_membership_id: result.owner_membership_id,
    activation_state: 'inactive',
    content_boundary: 'synthetic',
    public_enabled: false,
    fallback: false,
  };
}

function validateOwnerActivateResult(
  value: unknown,
  request: CleanOwnerActivate,
  signer: PrincipalSigner,
  now: number,
): unknown {
  const result = exactOwnerResponse(value, [
    'schema', 'store_id', 'team_id', 'activation_state', 'content_boundary',
    'public_enabled', 'gate_digest', 'activated_by_principal_id', 'audit_event_id',
    'activated_at', 'fallback',
  ]);
  if (
    result.schema !== OWNER_ACTIVATE_RESULT_SCHEMA || result.store_id !== signer.storeId ||
    result.team_id !== signer.teamId || result.activation_state !== 'active' ||
    result.content_boundary !== 'synthetic' || result.public_enabled !== true ||
    result.gate_digest !== request.gate_digest || !isOwnerOpaque(result.activated_by_principal_id) ||
    !isOwnerOpaque(result.audit_event_id) || result.fallback !== false
  ) invalidOwnerResponse();
  const activatedAt = ownerTimestamp(result.activated_at);
  const activatedUnix = Date.parse(activatedAt) / 1000;
  if (activatedUnix > now + 30 || activatedUnix < now - 300) invalidOwnerResponse();
  return {
    schema: OWNER_ACTIVATE_RESULT_SCHEMA,
    store_id: result.store_id,
    team_id: result.team_id,
    activation_state: 'active',
    content_boundary: 'synthetic',
    public_enabled: true,
    gate_digest: result.gate_digest,
    activated_by_principal_id: result.activated_by_principal_id,
    audit_event_id: result.audit_event_id,
    activated_at: activatedAt,
    fallback: false,
  };
}

function validateOwnerAdminMutationResult(
  path: OwnerAdminPublicPath,
  value: unknown,
  request: Record<string, unknown>,
  signer: PrincipalSigner,
): unknown {
  const route = OWNER_ADMIN_ROUTES[path];
  const action = request.action;
  if (typeof action !== 'string' || !route.actions.includes(action as OwnerAdminAction)) {
    invalidOwnerResponse();
  }
  const resultField = action.endsWith('.revoke')
    ? 'target_id'
    : action === 'membership.create' ? 'member'
      : action === 'agent_binding.create' ? 'binding'
        : action === 'service_principal.create' ? 'service'
          : action === 'project.create' ? 'project'
            : 'grant';
  const result = exactOwnerResponse(value, [
    'schema', 'action', 'audit_event_id', 'auth_epoch', 'status', resultField, 'fallback',
  ]);
  if (
    result.schema !== route.resultSchema || result.action !== action ||
    result.status !== 'complete' || result.fallback !== false ||
    !isOwnerOpaque(result.audit_event_id) || !isPositiveOwnerInteger(result.auth_epoch)
  ) invalidOwnerResponse();
  const common = {
    schema: route.resultSchema,
    action,
    audit_event_id: result.audit_event_id,
    auth_epoch: result.auth_epoch,
    status: 'complete',
  };
  if (resultField === 'target_id') {
    if (!isOwnerOpaque(result.target_id) || result.target_id !== request.target_id) {
      invalidOwnerResponse();
    }
    return { ...common, target_id: result.target_id, fallback: false };
  }
  if (resultField === 'member') {
    const member = exactOwnerResponse(result.member, [
      'principal_id', 'membership_id', 'role', 'auth_epoch',
    ]);
    if (
      !isOwnerOpaque(member.principal_id) || !isOwnerOpaque(member.membership_id) ||
      member.role !== request.role || !['owner', 'member', 'reviewer'].includes(String(member.role)) ||
      !isPositiveOwnerInteger(member.auth_epoch)
    ) invalidOwnerResponse();
    return { ...common, member: { ...member }, fallback: false };
  }
  if (resultField === 'binding') {
    const binding = exactOwnerResponse(result.binding, [
      'binding_id', 'human_principal_id', 'agent_principal_id', 'auth_epoch',
    ]);
    if (
      !isOwnerOpaque(binding.binding_id) || !isOwnerOpaque(binding.human_principal_id) ||
      !isOwnerOpaque(binding.agent_principal_id) || !isPositiveOwnerInteger(binding.auth_epoch)
    ) invalidOwnerResponse();
    return { ...common, binding: { ...binding }, fallback: false };
  }
  if (resultField === 'service') {
    const service = exactOwnerResponse(result.service, [
      'principal_id', 'membership_id', 'auth_epoch',
    ]);
    if (
      !isOwnerOpaque(service.principal_id) || !isOwnerOpaque(service.membership_id) ||
      !isPositiveOwnerInteger(service.auth_epoch)
    ) invalidOwnerResponse();
    return { ...common, service: { ...service }, fallback: false };
  }
  if (resultField === 'project') {
    const project = exactOwnerResponse(result.project, [
      'project_id', 'team_id', 'name', 'owner_principal_id', 'created_by_principal_id',
    ]);
    if (
      !isOwnerOpaque(project.project_id) || project.team_id !== signer.teamId ||
      project.name !== request.name || !isOwnerDisplayName(project.name) ||
      !isOwnerOpaque(project.owner_principal_id) || !isOwnerOpaque(project.created_by_principal_id)
    ) invalidOwnerResponse();
    return { ...common, project: { ...project }, fallback: false };
  }
  const grant = exactOwnerResponse(result.grant, [
    'grant_id', 'project_id', 'principal_id', 'access_level', 'auth_epoch',
  ]);
  if (
    !isOwnerOpaque(grant.grant_id) || grant.project_id !== request.project_id ||
    grant.principal_id !== request.target_principal_id || grant.access_level !== request.access_level ||
    !isPositiveOwnerInteger(grant.auth_epoch)
  ) invalidOwnerResponse();
  return { ...common, grant: { ...grant }, fallback: false };
}

function validateOwnerSharedDeleteResult(
  value: unknown,
  request: CleanOwnerSharedDelete,
): unknown {
  const result = exactOwnerResponse(value, [
    'schema', 'operation_id', 'object_id', 'audit_event_id', 'status', 'replayed', 'fallback',
  ]);
  if (
    result.schema !== OWNER_SHARED_DELETE_RESULT_SCHEMA ||
    !isOwnerOpaque(result.operation_id) || result.object_id !== request.object_id ||
    !isOwnerOpaque(result.audit_event_id) ||
    (result.status !== 'deletion_in_progress' && result.status !== 'complete') ||
    typeof result.replayed !== 'boolean' || result.fallback !== false
  ) invalidOwnerResponse();
  return {
    schema: OWNER_SHARED_DELETE_RESULT_SCHEMA,
    operation_id: result.operation_id,
    object_id: result.object_id,
    audit_event_id: result.audit_event_id,
    status: result.status,
    replayed: result.replayed,
    fallback: false,
  };
}

function validateOwnerAuditResult(
  value: unknown,
  request: CleanOwnerAudit,
  signer: PrincipalSigner,
): unknown {
  const result = exactOwnerResponseOptional(
    value, ['schema', 'events', 'own_actions_only', 'fallback'], ['next_cursor'],
  );
  if (
    result.schema !== OWNER_AUDIT_RESULT_SCHEMA || result.own_actions_only !== false ||
    result.fallback !== false || !Array.isArray(result.events) ||
    result.events.length > request.limit ||
    (result.next_cursor !== undefined && !isOwnerOpaque(result.next_cursor))
  ) invalidOwnerResponse();
  const events = result.events.map((entry) => {
    const event = exactOwnerResponse(entry, [
      'event_id', 'occurred_at', 'action', 'outcome', 'actor_principal_id', 'client_key',
      'team_id', 'project_id', 'target_kind', 'target_id', 'request_id',
      'policy_version', 'mode', 'reason_code',
    ]);
    if (
      !isOwnerOpaque(event.event_id) || !isOwnerTimestamp(event.occurred_at) ||
      !isOwnerClass(event.action) || !['allowed', 'denied', 'error'].includes(String(event.outcome)) ||
      !isOwnerOpaque(event.actor_principal_id) || !isOwnerClientKey(event.client_key) ||
      event.team_id !== signer.teamId || !isNullableOwnerOpaque(event.project_id) ||
      !isOwnerClass(event.target_kind) || !isNullableOwnerOpaque(event.target_id) ||
      !isNullableOwnerOpaque(event.request_id) || !isPositiveOwnerInteger(event.policy_version) ||
      event.mode !== 'team-remote' || !isOwnerClass(event.reason_code)
    ) invalidOwnerResponse();
    return { ...event };
  });
  return {
    schema: OWNER_AUDIT_RESULT_SCHEMA,
    events,
    ...(result.next_cursor === undefined ? {} : { next_cursor: result.next_cursor }),
    own_actions_only: false,
    fallback: false,
  };
}

function validateOwnerDeletionStatusResult(
  value: unknown,
  request: CleanOwnerDeletionStatus,
): unknown {
  const result = exactOwnerResponseOptional(
    value,
    ['schema', 'operation_id', 'object_id', 'audit_event_id', 'status', 'attempts', 'fallback'],
    ['next_attempt_at', 'completed_at'],
  );
  if (
    result.schema !== OWNER_DELETION_STATUS_RESULT_SCHEMA ||
    result.operation_id !== request.operation_id || !isOwnerOpaque(result.object_id) ||
    !isOwnerOpaque(result.audit_event_id) || !Number.isSafeInteger(result.attempts) ||
    (result.attempts as number) < 0 || (result.attempts as number) > 1_000_000 ||
    result.fallback !== false
  ) invalidOwnerResponse();
  const nextAttemptAt = optionalOwnerTimestampResponse(result.next_attempt_at);
  const completedAt = optionalOwnerTimestampResponse(result.completed_at);
  if (
    (result.status === 'deletion_in_progress' && completedAt === undefined) ||
    (result.status === 'cleanup_failed' && completedAt === undefined && nextAttemptAt !== undefined) ||
    (result.status === 'complete' && nextAttemptAt === undefined && completedAt !== undefined)
  ) {
    return {
      schema: OWNER_DELETION_STATUS_RESULT_SCHEMA,
      operation_id: result.operation_id,
      object_id: result.object_id,
      audit_event_id: result.audit_event_id,
      status: result.status,
      attempts: result.attempts,
      ...(nextAttemptAt === undefined ? {} : { next_attempt_at: nextAttemptAt }),
      ...(completedAt === undefined ? {} : { completed_at: completedAt }),
      fallback: false,
    };
  }
  invalidOwnerResponse();
}

function isPositiveOwnerInteger(value: unknown): value is number {
  return Number.isSafeInteger(value) && (value as number) >= 1;
}

function isOwnerClass(value: unknown): value is string {
  return typeof value === 'string' && value.length >= 1 && value.length <= 64 &&
    /^[A-Za-z0-9][A-Za-z0-9._:-]*$/.test(value) && !unsafeOwnerText(value);
}

function isOwnerClientKey(value: unknown): value is string {
  return value === '' || isOwnerDigest(value);
}

function isNullableOwnerOpaque(value: unknown): value is string | null {
  return value === null || isOwnerOpaque(value);
}

function isOwnerTimestamp(value: unknown): value is string {
  try {
    ownerTimestamp(value);
    return true;
  } catch {
    return false;
  }
}

function optionalOwnerTimestampResponse(value: unknown): string | undefined {
  return value === undefined ? undefined : ownerTimestamp(value);
}

function isOwnerDisplayName(value: unknown): value is string {
  if (typeof value !== 'string') return false;
  try {
    ownerTeamName(value);
    return true;
  } catch {
    return false;
  }
}

function exactOwnerResponse(value: unknown, fields: readonly string[]): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) invalidOwnerResponse();
  const result = value as Record<string, unknown>;
  const actual = Object.keys(result).sort();
  const expected = [...fields].sort();
  if (actual.length !== expected.length || actual.some((key, index) => key !== expected[index])) {
    invalidOwnerResponse();
  }
  return result;
}

function exactOwnerResponseOptional(
  value: unknown,
  required: readonly string[],
  optional: readonly string[],
): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) invalidOwnerResponse();
  const result = value as Record<string, unknown>;
  const allowed = new Set([...required, ...optional]);
  if (
    required.some((field) => !(field in result)) ||
    Object.keys(result).some((field) => !allowed.has(field)) ||
    Object.keys(result).length > allowed.size
  ) invalidOwnerResponse();
  return result;
}

function ownerBootstrapIntentResponse(value: unknown): OwnerBootstrapIntent {
  try {
    return ownerBootstrapIntent(value);
  } catch {
    invalidOwnerResponse();
  }
}

function isOwnerOpaque(value: unknown): value is string {
  return typeof value === 'string' && SAFE_OPAQUE.test(value) && !unsafeOwnerText(value);
}

function isOwnerDigest(value: unknown): value is string {
  return typeof value === 'string' && HEX_DIGEST.test(value);
}

function ownerTimestamp(value: unknown): string {
  if (typeof value !== 'string' || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/.test(value)) {
    invalidOwnerResponse();
  }
  const parsed = Date.parse(value);
  if (!Number.isFinite(parsed)) invalidOwnerResponse();
  return new Date(parsed).toISOString();
}

function invalidOwnerResponse(): never {
  throw new OwnerGatewayError('owner_service_unavailable');
}

async function readOwnerResponse(response: Response): Promise<unknown> {
  if (response.headers.get('content-type')?.split(';', 1)[0]?.trim().toLowerCase() !== 'application/json') {
    await response.body?.cancel().catch(() => undefined);
    invalidOwnerResponse();
  }
  const declared = response.headers.get('content-length');
  if (declared !== null && (!/^\d+$/.test(declared) || Number(declared) > OWNER_MAX_RESPONSE_BYTES)) {
    await response.body?.cancel().catch(() => undefined);
    invalidOwnerResponse();
  }
  let bytes: Uint8Array;
  try {
    bytes = new Uint8Array(await response.arrayBuffer());
  } catch {
    invalidOwnerResponse();
  }
  if (bytes.byteLength === 0 || bytes.byteLength > OWNER_MAX_RESPONSE_BYTES) invalidOwnerResponse();
  try {
    return JSON.parse(Buffer.from(bytes).toString('utf8'));
  } catch {
    invalidOwnerResponse();
  }
}

function ownerDaemonEndpoint(baseURL: string, path: string): string {
  let base: URL;
  try {
    base = new URL(baseURL);
  } catch {
    throw new Error('Owner daemon URL is invalid');
  }
  const host = base.hostname.toLowerCase();
  if (
    !['http:', 'https:'].includes(base.protocol) ||
    (host !== '127.0.0.1' && host !== '::1' && host !== '[::1]') ||
    base.username !== '' || base.password !== '' || base.search !== '' || base.hash !== ''
  ) throw new Error('Owner daemon URL must be numeric loopback');
  base.pathname = `${base.pathname.replace(/\/$/, '')}${path}`;
  return base.toString();
}

function ownerRequestID(value: string): string {
  if (!/^[A-Za-z0-9][A-Za-z0-9._:-]{7,63}$/.test(value)) invalidOwnerRequest();
  return value;
}

function boundedIdentity(value: unknown): value is string {
  return typeof value === 'string' && value.length >= 1 && value.length <= 512 &&
    value.trim() === value && !/[\u0000-\u001f\u007f]/.test(value);
}

function boundedInteger(value: number, minimum: number, maximum: number): number {
  if (!Number.isInteger(value) || value < minimum || value > maximum) {
    throw new Error('Owner gateway bound is invalid');
  }
  return value;
}
