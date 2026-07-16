import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  mkdirSync, mkdtempSync, readFileSync, symlinkSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { publishGitTeamMemory } from './git-team-memory.js';

function git(root, ...args) {
  return execFileSync('git', args, { cwd: root, encoding: 'utf8' }).trim();
}

function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'pulse-git-team-memory.'));
  git(root, 'init', '-q');
  git(root, 'config', 'user.name', 'Pulse Test');
  git(root, 'config', 'user.email', 'pulse-test@example.invalid');
  writeFileSync(join(root, 'README.md'), 'baseline\n');
  writeFileSync(join(root, 'staged.txt'), 'staged baseline\n');
  writeFileSync(join(root, 'modified.txt'), 'modified baseline\n');
  git(root, 'add', 'README.md', 'staged.txt', 'modified.txt');
  git(root, 'commit', '-q', '-m', 'baseline');
  writeFileSync(join(root, 'staged.txt'), 'user staged change\n');
  git(root, 'add', 'staged.txt');
  writeFileSync(join(root, 'modified.txt'), 'user working change\n');
  const dataDir = join(root, '.pulse-test');
  mkdirSync(dataDir, { mode: 0o700 });
  return {
    root,
    dataDir,
    resolved: {
      binding: { workspace: { canonical_path: root } },
      runtime: { data_dir: dataDir },
    },
  };
}

function file(path, content, memoryID = '') {
  return {
    path, memory_id: memoryID, content,
    sha256: createHash('sha256').update(content).digest('hex'),
    bytes: Buffer.byteLength(content),
  };
}

function publicationReceipt(expectedParent) {
  const files = [
    file('pulse-memory/project.json', '{"schema":"pulse.git_team_memory.project.v1"}\n'),
    file('pulse-memory/memories/shared_memory_01.json', '{"content":"Use the approved brief."}\n', 'shared_memory_01'),
    file('pulse-memory/publications/review_batch_01.json', '{"batch_id":"review_batch_01"}\n'),
  ];
  const digest = createHash('sha256');
  for (const item of files) {
    digest.update(item.path); digest.update('\x00'); digest.update(item.sha256); digest.update('\x00');
  }
  return {
    schema: 'pulse.git_team_memory.publication_receipt.v1',
    publication_id: 'shared_publication_01', batch_id: 'review_batch_01', state: 'publishing',
    expected_parent: expectedParent, files_digest: digest.digest('hex'), files,
    created_at: '2026-07-16T12:00:00Z', updated_at: '2026-07-16T12:00:00Z',
  };
}

test('Git Team Memory publication preserves the user index and commits only exact Pulse files without hooks', async () => {
  const { root, resolved } = fixture();
  const parent = git(root, 'rev-parse', 'HEAD');
  const stagedPatchBefore = git(root, 'diff', '--cached', '--binary', '--', 'staged.txt');
  const workingPatchBefore = git(root, 'diff', '--binary', '--', 'modified.txt');
  const hookMarker = join(root, 'hook-ran');
  const hook = join(root, '.git', 'hooks', 'pre-commit');
  writeFileSync(hook, `#!/bin/sh\ntouch ${JSON.stringify(hookMarker)}\n`, { mode: 0o700 });
  const finalizes = [];
  const result = await publishGitTeamMemory(resolved, {
    approval_lease_id: 'approval_lease_01', approver_label: 'Nikita',
  }, {
    beginPublication: async (input) => publicationReceipt(input.expected_parent),
    finalizePublication: async (input) => {
      finalizes.push(input);
      return { schema: 'pulse.git_team_memory.publication_receipt.v1', state: input.outcome, commit_hash: input.commit_hash };
    },
  });
  assert.equal(result.state, 'committed');
  assert.match(result.commit_hash, /^[a-f0-9]{40,64}$/);
  assert.equal(git(root, 'diff', '--cached', '--binary', '--', 'staged.txt'), stagedPatchBefore);
  assert.equal(git(root, 'diff', '--binary', '--', 'modified.txt'), workingPatchBefore);
  assert.equal(readFileSync(join(root, 'staged.txt'), 'utf8'), 'user staged change\n');
  assert.equal(readFileSync(join(root, 'modified.txt'), 'utf8'), 'user working change\n');
  assert.equal(git(root, 'diff', '--cached', '--name-only'), 'staged.txt');
  assert.equal(git(root, 'diff', '--name-only'), 'modified.txt');
  assert.equal(readFileSync(join(root, 'pulse-memory', 'memories', 'shared_memory_01.json'), 'utf8'), '{"content":"Use the approved brief."}\n');
  const changed = git(root, 'diff-tree', '--no-commit-id', '--name-only', '-r', result.commit_hash).split('\n').sort();
  assert.deepEqual(changed, [
    'pulse-memory/memories/shared_memory_01.json',
    'pulse-memory/project.json',
    'pulse-memory/publications/review_batch_01.json',
  ]);
  assert.equal(finalizes[0].outcome, 'committed');
  assert.equal(git(root, 'rev-parse', `${result.commit_hash}^`), parent);
  assert.throws(() => readFileSync(hookMarker), /ENOENT/);
});

test('Git Team Memory publication refuses a symlinked pack before writing outside the repository', async () => {
  const { root, resolved } = fixture();
  const outside = mkdtempSync(join(tmpdir(), 'pulse-git-team-memory-outside.'));
  symlinkSync(outside, join(root, 'pulse-memory'));
  await assert.rejects(
    publishGitTeamMemory(resolved, {
      approval_lease_id: 'approval_lease_02', approver_label: 'Nikita',
    }, {
      beginPublication: async (input) => publicationReceipt(input.expected_parent),
      finalizePublication: async () => { throw new Error('must not finalize'); },
    }),
    /parent_unsafe/,
  );
  assert.throws(() => readFileSync(join(outside, 'project.json')), /ENOENT/);
});

test('Git Team Memory publication refuses an ignored pack before creating its directory', async () => {
  const { root, resolved } = fixture();
  writeFileSync(join(root, '.gitignore'), 'pulse-memory/\n');
  await assert.rejects(
    publishGitTeamMemory(resolved, {
      approval_lease_id: 'approval_lease_ignored', approver_label: 'Nikita',
    }, {
      beginPublication: async (input) => publicationReceipt(input.expected_parent),
      finalizePublication: async () => { throw new Error('must not finalize'); },
    }),
    /target_ignored/,
  );
  assert.throws(() => readFileSync(join(root, 'pulse-memory', 'project.json')), /ENOENT/);
});

test('Git Team Memory publication reports published_uncommitted when Git identity is unavailable', async () => {
  const { root, resolved } = fixture();
  const parent = git(root, 'rev-parse', 'HEAD');
  git(root, 'config', 'user.name', '');
  git(root, 'config', 'user.email', '');
  const result = await publishGitTeamMemory(resolved, {
    approval_lease_id: 'approval_lease_03', approver_label: 'Nikita',
  }, {
    beginPublication: async (input) => publicationReceipt(input.expected_parent),
    finalizePublication: async (input) => ({ state: input.outcome, commit_hash: input.commit_hash }),
  });
  assert.deepEqual(result, { state: 'published_uncommitted', commit_hash: '' });
  assert.equal(git(root, 'rev-parse', 'HEAD'), parent);
  assert.equal(readFileSync(join(root, 'pulse-memory', 'project.json'), 'utf8'), '{"schema":"pulse.git_team_memory.project.v1"}\n');
});

test('Git Team Memory publication advances a detached local HEAD without creating a branch or remote action', async () => {
  const { root, resolved } = fixture();
  const parent = git(root, 'rev-parse', 'HEAD');
  git(root, 'checkout', '--detach', '-q');
  const result = await publishGitTeamMemory(resolved, {
    approval_lease_id: 'approval_lease_04', approver_label: 'Nikita',
  }, {
    beginPublication: async (input) => publicationReceipt(input.expected_parent),
    finalizePublication: async (input) => ({ state: input.outcome, commit_hash: input.commit_hash }),
  });
  assert.equal(result.state, 'committed');
  assert.equal(git(root, 'rev-parse', '--abbrev-ref', 'HEAD'), 'HEAD');
  assert.equal(git(root, 'rev-parse', `${result.commit_hash}^`), parent);
});
