import { test } from 'node:test';
import assert from 'node:assert/strict';
import { DatabaseSync } from 'node:sqlite';
import { createHash } from 'node:crypto';
import { mkdtempSync, chmodSync, rmSync, realpathSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { scopeReadScript, scopedContext } from './scope-guard.mjs';

const namespace = repository => 'project_' + createHash('sha256')
  .update('pulse-personal-project-namespace-v1\x1f' + repository).digest('hex').slice(0, 32);
const read = (path, repository) => spawnSync(process.execPath,
  ['--input-type=module', '-e', scopeReadScript, path, repository, '[1,2,3,4,5,6,7,8]'], { encoding: 'utf8' });

test('only own or shared active memory reaches context, including semantic projections', () => {
  const dir = realpathSync(mkdtempSync(join(tmpdir(), 'bb-pulse-scope-'))), path = join(dir, 'scope.sqlite');
  try {
    const db = new DatabaseSync(path);
    db.exec(`CREATE TABLE private_memory_objects(object_id TEXT, lifecycle TEXT, memory_scope TEXT, project_namespace_id TEXT);
      CREATE TABLE memory_capsules(id TEXT, event_id INTEGER, status TEXT);
      CREATE TABLE private_semantic_projection_rows(object_id TEXT, row_kind TEXT, row_ref TEXT);`);
    const object = db.prepare('INSERT INTO private_memory_objects VALUES(?,?,?,?)');
    const capsule = db.prepare('INSERT INTO memory_capsules VALUES(?,?,?)');
    for (const [id, repo, scope, lifecycle, status] of [
      [1, 'repository_a', 'project', 'active', 'active'],
      [2, 'repository_b', 'project', 'active', 'active'],
      [3, 'repository_a', 'personal_global', 'active', 'active'],
      [4, 'repository_a', 'personal_global', 'deleted', 'active'],
      [5, 'repository_a', 'project', 'active', 'merged'],
    ]) {
      object.run(String(id), lifecycle, scope, namespace(repo));
      capsule.run(String(id), id, status);
    }
    object.run('projection_a', 'active', 'project', namespace('repository_a'));
    object.run('projection_b', 'active', 'project', namespace('repository_b'));
    db.exec(`INSERT INTO private_semantic_projection_rows VALUES('projection_a','event','6'),('projection_b','event','7');
      INSERT INTO memory_capsules VALUES('legacy_unassigned',8,'active');`);
    db.close(); chmodSync(path, 0o644); // installed Pulse uses a 0700 parent
    const a = read(path, 'repository_a'), b = read(path, 'repository_b');
    assert.equal(a.status, 0); assert.equal(b.status, 0);
    assert.deepEqual(JSON.parse(a.stdout), [1, 3, 6]);
    assert.deepEqual(JSON.parse(b.stdout), [2, 3, 7]);
    const result = scopedContext({ events: [{ id: 1, summary: 'private A' }, { id: 2, summary: 'private B' }],
      facts: ['must not pass'], trace: { retrieval: { candidate_evidence: { 1: {}, 2: {} }, score_breakdowns: { 1: {}, 2: {} } } } }, [1]);
    assert.deepEqual(result.events, [{ id: 1, summary: 'private A' }]);
    assert.equal(JSON.stringify(result).includes('private B'), false);
    assert.equal('2' in result.trace.retrieval.candidate_evidence, false);
    assert.equal('facts' in result, false);
    chmodSync(dir, 0o755);
    assert.equal(read(path, 'repository_a').status, 1);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('missing eligibility metadata denies reading and creates no database', () => {
  const dir = mkdtempSync(join(tmpdir(), 'bb-pulse-missing-'));
  try {
    const r = read(join(dir, 'missing.sqlite'), 'repository_a');
    assert.equal(r.status, 1); assert.equal(r.stdout, '');
    assert.equal(existsSync(join(dir, 'missing.sqlite')), false);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});
