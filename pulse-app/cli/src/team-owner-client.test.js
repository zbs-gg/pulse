import assert from 'node:assert/strict';
import test from 'node:test';

import {
  buildTeamOwnerOperation,
  buildTeamOwnerStepUp,
  createTeamOwnerRemotePost,
  ownerCredentialRef,
  runTeamOwnerOperation,
} from './team-owner-client.js';

const STEP_UP = Object.freeze({
  idToken: 'header.payload.signature',
  operationChallenge: 'c'.repeat(43),
  authorizationStartedAt: 1_700_000_000,
});

function binding() {
  return {
    mode: 'team',
    fallback: false,
    principal_ref: 'principal_owner',
    commons: {
      resource: 'https://pulse.example.test/mcp',
      credential_ref: 'keychain:pulse/team/team_test/principal_owner',
      store_id: 'store_test',
      team_id: 'team_test',
    },
  };
}

test('Owner operations reproduce the Go approval target vectors and exact public routes', () => {
  assert.equal(
    ownerCredentialRef(binding()),
    'keychain:pulse/team-owner/team_test/principal_owner',
  );
  const vectors = [
    [
      { action: 'membership.create', issuer: 'https://issuer.example/', subject: 'member-subject', role: 'member' },
      '/owner/v1/members', 'membership',
      '2a64826aba83be9e5e12ed4343d2cf32cb06d3afa79a886282001fb3b81d9dea',
      'fd98db5ebe01bdbe6477c0226fbc46fb25ab98ed76a1afa048ff19f905523a31',
    ],
    [
      { action: 'membership.revoke', target_id: 'principal_01' },
      '/owner/v1/members', 'membership', 'principal_01',
      '9b0e7c22ab274a9eca3d2ce9486a9ad8085dc72e529c44f1b77b358a5817afe4',
    ],
    [
      { action: 'agent_binding.create', issuer: 'https://issuer.example/', subject: 'dima', client_id: 'codex-dima' },
      '/owner/v1/bindings', 'agent_binding',
      'd63360386642235e2a3058411110b773d20589e961695e5602cef25f5ecfbaff',
      '2961cfb7c064aa13b5bf2ecc0d485a54deab69d93a137d3f73e2097e35b19c09',
    ],
    [
      { action: 'agent_binding.revoke', target_id: 'binding_dima' },
      '/owner/v1/bindings', 'agent_binding', 'binding_dima',
      '80194ee5b27c394039f70288a1b115806d624a07985c80ec7a6f955fb9ce2980',
    ],
    [
      { action: 'project.create', name: 'Project Atlas' },
      '/owner/v1/projects', 'project',
      '89ded0187a2b7d5e0ef5c2f18b5e56c59a3eaea758d2422b133f5368b4dece0a',
      '159adc7fc56f22317ded949a569b56d32125d9ccea40691afb0e27a2f87ff4d6',
    ],
    [
      { action: 'project_grant.create', project_id: 'project_01', target_principal_id: 'principal_01', access_level: 'write' },
      '/owner/v1/project-grants', 'project_grant',
      '6aba93e98732c1f18b14f68e7bc3839a184aa1e302c373e88dbff9fdcce122c5',
      'e08ce57570e1d7da518c2e3dbc738b82d7159b82c79d8e7a0b599e189fb0961c',
    ],
    [
      { action: 'project_grant.revoke', target_id: 'grant_01' },
      '/owner/v1/project-grants', 'project_grant', 'grant_01',
      '2a3daacb2b51da532e4f0d6eabf67d73895909d9116e1a50b95edfe7431d3954',
    ],
  ];
  for (const [input, path, kind, id, digest] of vectors) {
    const operation = buildTeamOwnerOperation(binding(), input);
    assert.equal(operation.path, path);
    assert.equal(operation.approval.target_kind, kind);
    assert.equal(operation.approval.target_id, id);
    assert.equal(operation.approval.target_digest, digest);
    assert.equal(operation.approval.mutation.action, undefined);
    assert.deepEqual(operation.execution, {
      schema: operation.schema,
      action: input.action,
      approval_nonce: null,
      ...operation.approval.mutation,
    });
  }
});

test('Owner browser nonce is bound to one exact canonical operation and random challenge', () => {
  const input = {
    action: 'membership.create', issuer: 'https://issuer.example/', subject: 'member-subject', role: 'member',
  };
  const first = buildTeamOwnerStepUp(binding(), input, { randomBytes: () => Buffer.alloc(32, 7) });
  const again = buildTeamOwnerStepUp(binding(), input, { randomBytes: () => Buffer.alloc(32, 7) });
  const changed = buildTeamOwnerStepUp(binding(), { ...input, role: 'reviewer' }, {
    randomBytes: () => Buffer.alloc(32, 7),
  });
  assert.equal(first.challenge, 'BwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwc');
  assert.equal(first.approvalText, JSON.stringify(first.operation.approval));
  assert.equal(first.nonce, 'aiAVs-kId_SG9kMUf4KlBj38BX-UTFmxO2R9BFLcfLE');
  assert.equal(first.nonce, again.nonce);
  assert.notEqual(first.nonce, changed.nonce);
  assert.match(first.nonce, /^[A-Za-z0-9_-]{43}$/);
  assert.throws(
    () => buildTeamOwnerOperation(binding(), { ...input, issuer: 'https://issuer.example' }),
    /team_owner_request_invalid/,
  );
});

test('Owner mutation is exact two-phase and returns a content-free receipt', async () => {
  const calls = [];
  const result = await runTeamOwnerOperation(binding(), {
    action: 'membership.create',
    issuer: 'https://issuer.example/',
    subject: 'private-member-subject',
    role: 'member',
  }, {
    stepUp: STEP_UP,
    post: async (path, body) => {
      calls.push({ path, body });
      if (path === '/owner/v1/approval') {
        return {
          schema: 'pulse.team.owner.approval_result.v1',
          approval_nonce: 'a'.repeat(64),
          action: body.action,
          store_id: body.store_id,
          team_id: body.team_id,
          target_kind: body.target_kind,
          target_id: body.target_id,
          expires_at: '2026-07-15T10:04:00.000Z',
          fallback: false,
        };
      }
      return {
        schema: 'pulse.team.owner.members_result.v1',
        action: 'membership.create',
        audit_event_id: 'audit_01',
        auth_epoch: 2,
        status: 'complete',
        member: {
          principal_id: 'principal_member', membership_id: 'membership_member',
          role: 'member', auth_epoch: 2,
        },
        fallback: false,
      };
    },
  });

  assert.equal(calls.length, 2);
  assert.equal(calls[0].path, '/owner/v1/approval');
  assert.deepEqual(calls[1], {
    path: '/owner/v1/members',
    body: {
      schema: 'pulse.team.owner.members.v1',
      action: 'membership.create',
      approval_nonce: 'a'.repeat(64),
      issuer: 'https://issuer.example/',
      subject: 'private-member-subject',
      role: 'member',
    },
  });
  assert.deepEqual(result, {
    schema: 'pulse.team.owner_cli_receipt.v1',
    status: 'complete',
    action: 'membership.create',
    audit_event_id: 'audit_01',
    auth_epoch: 2,
    target_kind: 'membership',
    target_id: 'membership_member',
    principal_id: 'principal_member',
    fallback: false,
  });
  assert.doesNotMatch(JSON.stringify(result), /issuer|subject|private-member|Project Atlas|name/);
});

test('Owner agent binding returns the agent principal needed for a project grant', async () => {
  const result = await runTeamOwnerOperation(binding(), {
    action: 'agent_binding.create',
    issuer: 'https://issuer.example/',
    subject: 'dima',
    client_id: 'codex-dima',
  }, {
    stepUp: STEP_UP,
    post: async (path, body) => path === '/owner/v1/approval'
      ? {
          schema: 'pulse.team.owner.approval_result.v1', approval_nonce: 'a'.repeat(64),
          action: body.action, store_id: body.store_id, team_id: body.team_id,
          target_kind: body.target_kind, target_id: body.target_id,
          expires_at: '2026-07-15T10:04:00.000Z', fallback: false,
        }
      : {
          schema: 'pulse.team.owner.bindings_result.v1', action: 'agent_binding.create',
          audit_event_id: 'audit_binding_01', auth_epoch: 3, status: 'complete',
          binding: {
            binding_id: 'binding_dima', human_principal_id: 'principal_dima',
            agent_principal_id: 'principal_codex_dima', auth_epoch: 3,
          },
          fallback: false,
        },
  });
  assert.deepEqual(result, {
    schema: 'pulse.team.owner_cli_receipt.v1', status: 'complete',
    action: 'agent_binding.create', audit_event_id: 'audit_binding_01', auth_epoch: 3,
    target_kind: 'agent_binding', target_id: 'binding_dima',
    principal_id: 'principal_codex_dima', fallback: false,
  });
});

test('Owner remote post is same-origin HTTPS, DPoP constrained, and never sends IPC authority', async () => {
  const calls = [];
  const post = createTeamOwnerRemotePost(binding(), {
    credentialStore: {},
    acquireLock: async () => async () => {},
    refresh: async (_store, ref, options) => calls.push({ refresh: ref, force: options.force }),
    buildHeaders: (url, { method, credentialRef }) => ({
      Authorization: 'DPoP token-sentinel',
      DPoP: `proof:${method}:${url}`,
      'X-Pulse-Enrollment': credentialRef,
      'Content-Type': 'application/json',
    }),
    fetch: async (url, init) => {
      calls.push({ url, init });
      return Response.json({ ok: true });
    },
    now: () => 100,
  });
  assert.deepEqual(await post('/owner/v1/projects', { schema: 'test' }), { ok: true });
  assert.deepEqual(calls[0], {
    refresh: 'keychain:pulse/team-owner/team_test/principal_owner', force: false,
  });
  const sent = calls[1];
  assert.equal(sent.url, 'https://pulse.example.test/owner/v1/projects');
  assert.equal(sent.init.method, 'POST');
  assert.equal(sent.init.headers.get('Origin'), 'https://pulse.example.test');
  assert.equal(sent.init.headers.get('Authorization'), 'DPoP token-sentinel');
  assert.equal(sent.init.headers.get('DPoP'), 'proof:POST:https://pulse.example.test/owner/v1/projects');
  assert.equal(sent.init.headers.has('X-Pulse-Key'), false);
  assert.equal(sent.init.redirect, 'error');
  await post('/owner/v1/approval', { schema: 'test' }, STEP_UP);
  const approvalSent = calls[3];
  assert.equal(approvalSent.url, 'https://pulse.example.test/owner/v1/approval');
  assert.equal(approvalSent.init.headers.get('X-Pulse-Owner-ID-Token'), STEP_UP.idToken);
  assert.equal(
    approvalSent.init.headers.get('X-Pulse-Owner-Operation-Challenge'),
    STEP_UP.operationChallenge,
  );
  assert.equal(
    approvalSent.init.headers.get('X-Pulse-Owner-Authorization-Started-At'),
    String(STEP_UP.authorizationStartedAt),
  );
  await assert.rejects(
    post('/team/v1/owner/projects', {}),
    /team_owner_path_invalid/,
  );
});

test('Owner response validators reject smuggled content and stale browser step-up is actionable', async () => {
  await assert.rejects(runTeamOwnerOperation(binding(), {
    action: 'project.create', name: 'Project Atlas',
  }, {
    stepUp: STEP_UP,
    post: async (path, body) => path === '/owner/v1/approval'
      ? {
          schema: 'pulse.team.owner.approval_result.v1', approval_nonce: 'a'.repeat(64),
          action: body.action, store_id: body.store_id, team_id: body.team_id,
          target_kind: body.target_kind, target_id: body.target_id,
          expires_at: '2026-07-15T10:04:00.000Z', fallback: false,
        }
      : {
          schema: 'pulse.team.owner.projects_result.v1', action: 'project.create',
          audit_event_id: 'audit_01', auth_epoch: 2, status: 'complete', fallback: false,
          project: {
            project_id: 'project_01', team_id: 'team_test', name: 'Project Atlas',
            owner_principal_id: 'principal_owner', created_by_principal_id: 'principal_owner',
          },
          leaked_subject: 'private',
        },
  }), /team_owner_response_invalid/);

  const post = createTeamOwnerRemotePost(binding(), {
    credentialStore: {},
    acquireLock: async () => async () => {},
    refresh: async () => {},
    buildHeaders: () => ({ Authorization: 'DPoP x', DPoP: 'proof', 'X-Pulse-Enrollment': 'enroll_1' }),
    fetch: async () => Response.json({ error: 'owner_step_up_required', fallback: false }, { status: 403 }),
  });
  await assert.rejects(post('/owner/v1/approval', {}, STEP_UP), /team_owner_step_up_required/);
});
