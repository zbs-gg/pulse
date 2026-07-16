import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  realpathSync,
  renameSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { basename, dirname, isAbsolute, join, relative, resolve, sep } from 'node:path';

const DIGEST = /^[a-f0-9]{64}$/;
const STABLE_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$/;
const GIT_OID = /^(?:[a-f0-9]{40}|[a-f0-9]{64}|unborn)$/;
const ALLOWED_GIT_COMMANDS = new Set([
  'check-ignore', 'commit-tree', 'diff-tree', 'hash-object', 'read-tree', 'rev-parse',
  'status', 'symbolic-ref', 'update-index', 'update-ref', 'write-tree',
]);

function inside(root, target) {
  const rel = relative(root, target);
  return rel !== '' && rel !== '..' && !rel.startsWith(`..${sep}`) && !isAbsolute(rel);
}

function gitEnvironment(extra = {}) {
  return {
    ...process.env,
    GIT_OPTIONAL_LOCKS: '0',
    GIT_TERMINAL_PROMPT: '0',
    LC_ALL: 'C',
    ...extra,
  };
}

function runGitCommand(root, args, { input, env = {}, allowFailure = false } = {}) {
  if (!Array.isArray(args) || !ALLOWED_GIT_COMMANDS.has(args[0])) {
    throw new Error('git_team_memory_git_command_forbidden');
  }
  const result = spawnSync('git', args, {
    cwd: root,
    env: gitEnvironment(env),
    input,
    encoding: 'utf8',
    stdio: ['pipe', 'pipe', 'pipe'],
    timeout: 15_000,
  });
  if (result.error) throw result.error;
  if (result.status !== 0 && !allowFailure) {
    throw new Error(`git_team_memory_git_failed:${args[0]}:${String(result.stderr).trim().slice(0, 160)}`);
  }
  return { status: result.status, stdout: String(result.stdout), stderr: String(result.stderr) };
}

function currentGitParent(root, runGit) {
  const result = runGit(root, ['rev-parse', '--verify', 'HEAD'], { allowFailure: true });
  if (result.status !== 0) return 'unborn';
  const value = result.stdout.trim();
  if (!GIT_OID.test(value) || value === 'unborn') throw new Error('git_team_memory_head_invalid');
  return value;
}

function canonicalPublicationFiles(receipt) {
  if (!receipt || typeof receipt !== 'object' || Array.isArray(receipt) ||
      receipt.schema !== 'pulse.git_team_memory.publication_receipt.v1' ||
      !STABLE_ID.test(receipt.publication_id ?? '') || receipt.state !== 'publishing' ||
      !GIT_OID.test(receipt.expected_parent ?? '') || !DIGEST.test(receipt.files_digest ?? '') ||
      !Array.isArray(receipt.files) || receipt.files.length < 3 || receipt.files.length > 25) {
    throw new Error('git_team_memory_publication_receipt_invalid');
  }
  const seen = new Set();
  const files = receipt.files.map((file) => {
    if (!file || typeof file !== 'object' || Array.isArray(file) ||
        typeof file.path !== 'string' || !/^pulse-memory\/(?:project\.json|memories\/[A-Za-z0-9._:-]+\.json|publications\/[A-Za-z0-9._:-]+\.json)$/.test(file.path) ||
        seen.has(file.path) || typeof file.content !== 'string' || !DIGEST.test(file.sha256 ?? '') ||
        Buffer.byteLength(file.content) !== file.bytes ||
        createHash('sha256').update(file.content).digest('hex') !== file.sha256) {
      throw new Error('git_team_memory_publication_file_invalid');
    }
    seen.add(file.path);
    return Object.freeze({ ...file });
  });
  const digest = createHash('sha256');
  for (const file of files) {
    digest.update(file.path);
    digest.update('\x00');
    digest.update(file.sha256);
    digest.update('\x00');
  }
  if (digest.digest('hex') !== receipt.files_digest) {
    throw new Error('git_team_memory_publication_digest_mismatch');
  }
  return files;
}

function ensureSafeParent(root, path, { create = true } = {}) {
  const relativePath = relative(root, path);
  if (relativePath === '' || relativePath.startsWith('..') || isAbsolute(relativePath)) {
    throw new Error('git_team_memory_path_escape');
  }
  let current = root;
  for (const part of relativePath.split(sep)) {
    current = join(current, part);
    if (existsSync(current)) {
      const info = lstatSync(current);
      if (!info.isDirectory() || info.isSymbolicLink()) throw new Error('git_team_memory_parent_unsafe');
    } else {
      if (!create) return;
      mkdirSync(current, { mode: 0o700 });
    }
  }
}

function preflightPublicationPaths(root, files, runGit) {
  for (const file of files) {
    const target = resolve(root, ...file.path.split('/'));
    if (!inside(root, target)) throw new Error('git_team_memory_path_escape');
    ensureSafeParent(root, dirname(target), { create: false });
    const ignored = runGit(root, ['check-ignore', '--no-index', '--quiet', '--', file.path], { allowFailure: true });
    if (ignored.status === 0) throw new Error('git_team_memory_target_ignored');
    if (ignored.status !== 1) throw new Error('git_team_memory_ignore_check_failed');
    ensureSafeParent(root, dirname(target));
    if (!existsSync(target)) continue;
    const info = lstatSync(target);
    if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 ||
        readFileSync(target, 'utf8') !== file.content) {
      throw new Error('git_team_memory_target_overlap');
    }
    const status = runGit(root, ['status', '--porcelain=v1', '--untracked-files=all', '--', file.path]).stdout.trim();
    if (status !== '' && status !== `?? ${file.path}`) throw new Error('git_team_memory_target_overlap');
  }
}

function writePublicationFiles(root, files) {
  for (const file of files) {
    const target = resolve(root, ...file.path.split('/'));
    if (existsSync(target)) continue;
    const temporary = join(dirname(target), `.${basename(target)}.${process.pid}.${Date.now()}.pulse-new`);
    try {
      writeFileSync(temporary, file.content, { mode: 0o600, flag: 'wx' });
      renameSync(temporary, target);
    } finally {
      rmSync(temporary, { force: true });
    }
  }
  for (const file of files) {
    const target = resolve(root, ...file.path.split('/'));
    const info = lstatSync(target);
    if (!info.isFile() || info.isSymbolicLink() || info.nlink !== 1 ||
        createHash('sha256').update(readFileSync(target)).digest('hex') !== file.sha256) {
      throw new Error('git_team_memory_written_file_mismatch');
    }
  }
}

function isolatedGitCommit(root, dataDir, receipt, files, runGit) {
  if (currentGitParent(root, runGit) !== receipt.expected_parent) {
    return { outcome: 'published_uncommitted', commit_hash: '', reason_code: 'head_changed' };
  }
  const temporaryRoot = mkdtempSync(join(resolve(dataDir), '.git-team-memory-index.'));
  const indexPath = join(temporaryRoot, 'index');
  const env = { GIT_INDEX_FILE: indexPath };
  try {
    if (receipt.expected_parent === 'unborn') {
      runGit(root, ['read-tree', '--empty'], { env });
    } else {
      runGit(root, ['read-tree', `${receipt.expected_parent}^{tree}`], { env });
    }
    const blobIDs = new Map();
    for (const file of files) {
      const objectID = runGit(root, ['hash-object', '-w', '--stdin'], { input: file.content }).stdout.trim();
      if (!/^[a-f0-9]{40,64}$/.test(objectID)) throw new Error('git_team_memory_blob_invalid');
      blobIDs.set(file.path, objectID);
      runGit(root, ['update-index', '--add', '--cacheinfo', '100644', objectID, file.path], { env });
    }
    const tree = runGit(root, ['write-tree'], { env }).stdout.trim();
    if (!/^[a-f0-9]{40,64}$/.test(tree)) throw new Error('git_team_memory_tree_invalid');
    const commitArgs = ['commit-tree', tree, '-m', `chore(pulse): publish ${receipt.batch_id}`];
    if (receipt.expected_parent !== 'unborn') commitArgs.splice(2, 0, '-p', receipt.expected_parent);
    const commit = runGit(root, commitArgs, { allowFailure: true });
    if (commit.status !== 0) {
      return { outcome: 'published_uncommitted', commit_hash: '', reason_code: 'git_identity_unavailable' };
    }
    const commitHash = commit.stdout.trim();
    if (!/^[a-f0-9]{40,64}$/.test(commitHash)) throw new Error('git_team_memory_commit_invalid');
    const symbolic = runGit(root, ['symbolic-ref', '-q', 'HEAD'], { allowFailure: true });
    const targetRef = symbolic.status === 0 ? symbolic.stdout.trim() : 'HEAD';
    const expectedOld = receipt.expected_parent === 'unborn' ? '0'.repeat(commitHash.length) : receipt.expected_parent;
    const cas = runGit(root, ['update-ref', targetRef, commitHash, expectedOld], { allowFailure: true });
    if (cas.status !== 0) {
      return { outcome: 'published_uncommitted', commit_hash: '', reason_code: 'head_changed' };
    }
    // The commit was built from an isolated index. Align only the exact Pulse
    // paths in the caller index after the CAS so unrelated staged work remains
    // byte-for-byte the same while the new HEAD does not manufacture deletions.
    for (const file of files) {
      runGit(root, ['update-index', '--add', '--cacheinfo', '100644', blobIDs.get(file.path), file.path]);
    }
    const changed = runGit(root, ['diff-tree', '--no-commit-id', '--name-only', '-r', commitHash])
      .stdout.split('\n').map((item) => item.trim()).filter(Boolean);
    const allowed = new Set(files.map(({ path }) => path));
    if (changed.length === 0 || changed.some((path) => !allowed.has(path))) {
      throw new Error('git_team_memory_commit_scope_invalid');
    }
    return { outcome: 'committed', commit_hash: commitHash, reason_code: '' };
  } finally {
    rmSync(temporaryRoot, { recursive: true, force: true });
  }
}

export async function publishGitTeamMemory(resolved, input, {
  beginPublication,
  finalizePublication,
  runGit = runGitCommand,
} = {}) {
  if (!resolved?.binding?.workspace || !isAbsolute(resolved.binding.workspace.canonical_path ?? '') ||
      typeof resolved?.runtime?.data_dir !== 'string' || !isAbsolute(resolved.runtime.data_dir) ||
      typeof beginPublication !== 'function' || typeof finalizePublication !== 'function' ||
      !input || typeof input !== 'object' || Array.isArray(input) ||
      Object.keys(input).sort().join('\x00') !== 'approval_lease_id\x00approver_label' ||
      !STABLE_ID.test(input.approval_lease_id ?? '') || typeof input.approver_label !== 'string') {
    throw new Error('git_team_memory_publish_input_invalid');
  }
  const root = realpathSync(resolved.binding.workspace.canonical_path);
  const top = realpathSync(runGit(root, ['rev-parse', '--show-toplevel']).stdout.trim());
  if (top !== root) throw new Error('git_team_memory_repository_mismatch');
  const expectedParent = currentGitParent(root, runGit);
  const receipt = await beginPublication({
    approval_lease_id: input.approval_lease_id,
    approver_label: input.approver_label,
    expected_parent: expectedParent,
  });
  const files = canonicalPublicationFiles(receipt);
  if (receipt.expected_parent !== expectedParent) throw new Error('git_team_memory_publication_parent_mismatch');
  preflightPublicationPaths(root, files, runGit);
  writePublicationFiles(root, files);
  const result = isolatedGitCommit(root, resolved.runtime.data_dir, receipt, files, runGit);
  return finalizePublication({
    publication_id: receipt.publication_id,
    files_digest: receipt.files_digest,
    outcome: result.outcome,
    commit_hash: result.commit_hash,
  });
}
