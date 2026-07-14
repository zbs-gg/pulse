import assert from 'node:assert/strict';
import test from 'node:test';

import { composeBoundResumeEvidence, renderCommonsResume } from './product-compositor.js';
import { TeamRemoteClientError } from './team-remote-client.js';

const WORKSPACE = {
  workspace_id: 'workspace_pulse', repository_id: 'repository_pulse', canonical_path: '/repo',
};

function resolved(mode = 'team') {
  return {
    binding: {
      mode, fallback: false, principal_ref: 'principal_nik', workspace: WORKSPACE,
      ...(mode === 'team'
        ? { desk: { store_id: 'desk_nik' }, commons: {
          project_id: 'project_zbs', resource: 'https://pulse.example/mcp',
        } }
        : { personal: { store_id: 'personal_nik' } }),
    },
    runtime: { data_dir: '/private/desk' },
  };
}

function remoteResume(text = 'Use the approved deployment checklist.') {
  return {
    schema: 'pulse.team.resume_result.v1',
    thread_id: 'repository_pulse',
    sections: {
      where_we_left_off: [{ object_id: 'commons_1', text }],
      active_decisions: [], open_loops: [], do_not_repeat: [],
      relevant_emotional_state_context: [], suggested_next_step: [],
    },
    returned_count: 1,
    fallback: false,
  };
}

test('team session composes private Desk and authorized Commons with separate provenance and budgets', async () => {
  const calls = [];
  const result = await composeBoundResumeEvidence(resolved(), {
    session_id: 'session_1', idempotency_key: 'event_1',
  }, {
    host: 'codex',
    localTokenBudget: 700,
    commonsLimit: 6,
    request: async (_resolved, path, options) => {
      calls.push({ path, body: options.body });
      return { resume_markdown: 'Desk decision stays private.' };
    },
    teamRequest: async (binding, tool, args) => {
      calls.push({ binding, tool, args });
      return remoteResume();
    },
  });
  assert.equal(result.commons.status, 'active');
  assert.equal(result.commons.fallback, false);
  assert.match(result.evidence[0], /^Private Desk continuity \(local, private\):/);
  assert.match(result.evidence[1], /^Team Commons \[where_we_left_off\].*shared, authorized Commons evidence/s);
  assert.equal(calls[0].body.token_budget, 700);
  assert.equal(calls[1].tool, 'pulse_team_resume');
  assert.equal(calls[1].args.limit, 6);
  assert.deepEqual(calls[1].args.active_context, {
    project_id: 'project_zbs', repo_id: 'repository_pulse',
    agent_id: 'principal_nik', session_id: 'session_1',
  });
});

test('Commons outage degrades loudly without replacing private Desk or querying a fallback', async () => {
  let remoteCalls = 0;
  const result = await composeBoundResumeEvidence(resolved(), {
    session_id: 'session_1', idempotency_key: 'event_1',
  }, {
    host: 'codex',
    request: async () => ({ resume_markdown: 'Private continuity.' }),
    teamRequest: async () => {
      remoteCalls++;
      throw new TeamRemoteClientError('revoked');
    },
  });
  assert.equal(remoteCalls, 1);
  assert.deepEqual(result.commons, { status: 'degraded', reason_code: 'revoked', fallback: false });
  assert.equal(result.evidence.length, 2);
  assert.match(result.evidence[1], /No fallback store was queried/);
});

test('Personal binding never creates a Commons client', async () => {
  let remoteCalls = 0;
  const result = await composeBoundResumeEvidence(resolved('personal'), {
    session_id: 'session_1', idempotency_key: 'event_1',
  }, {
    host: 'claude-code',
    request: async () => ({ resume_markdown: 'Personal continuity.' }),
    teamRequest: async () => { remoteCalls++; },
  });
  assert.equal(remoteCalls, 0);
  assert.equal(result.commons.status, 'not_applicable');
  assert.match(result.evidence[0], /^Personal Vault continuity/);
});

test('Commons response parser rejects count drift and malformed object provenance', () => {
  assert.throws(() => renderCommonsResume({ ...remoteResume(), returned_count: 0 }, 8), /commons_resume_invalid/);
  const malformed = remoteResume();
  malformed.sections.where_we_left_off[0].object_id = '../private/desk';
  assert.throws(() => renderCommonsResume(malformed, 8), /commons_resume_invalid/);
});
