// Executed by the explicitly configured Node, not BB's embedded runtime.
// No text bodies are read; the signed binding supplies the DB and repository.
export const scopeReadScript = `
import { DatabaseSync } from 'node:sqlite';
import { createHash } from 'node:crypto';
import { statSync, realpathSync } from 'node:fs';
import { dirname } from 'node:path';
const [path, repository, encoded] = process.argv.slice(1);
let db;
try {
  const st = statSync(path), parent = statSync(dirname(path)), ids = JSON.parse(encoded);
  // Pulse creates a 0644 DB inside its 0700 vault. The private directory is
  // the access boundary; reject shared/writable parent directories.
  if (!st.isFile() || st.uid !== process.getuid() || (st.mode & 0o022) ||
      !parent.isDirectory() || parent.uid !== process.getuid() || (parent.mode & 0o077) ||
      realpathSync(path) !== path || !/^repository_[a-zA-Z0-9_]+$/.test(repository) ||
      !Array.isArray(ids) || ids.length > 48 || !ids.every(id => Number.isSafeInteger(id) && id > 0)) throw new Error();
  const namespace = 'project_' + createHash('sha256')
    .update('pulse-personal-project-namespace-v1\\x1f' + repository).digest('hex').slice(0, 32);
  db = new DatabaseSync(path, { readOnly: true });
  db.exec('PRAGMA busy_timeout=800');
  const rows = db.prepare(\`
    WITH eligible AS (
      SELECT object_id FROM private_memory_objects
      WHERE lifecycle='active' AND (memory_scope='personal_global' OR
        (memory_scope='project' AND project_namespace_id=?))
    ), requested AS (SELECT value AS event_id FROM json_each(?))
    SELECT c.event_id FROM eligible o JOIN memory_capsules c ON c.id=o.object_id
      JOIN requested r ON r.event_id=c.event_id WHERE c.status='active'
    UNION
    SELECT r.event_id FROM eligible o JOIN private_semantic_projection_rows p ON p.object_id=o.object_id
      JOIN requested r ON CAST(r.event_id AS TEXT)=p.row_ref WHERE p.row_kind='event'
  \`).all(namespace, JSON.stringify(ids));
  process.stdout.write(JSON.stringify(rows.map(row => row.event_id)));
} catch { process.exitCode = 1; }
finally { db?.close(); }
`;

export function scopedContext(result, allowedIDs) {
  const allowed = new Set(allowedIDs);
  const events = result.events.filter(event => allowed.has(event.id));
  const retrieval = result.trace?.retrieval ?? {};
  const pick = values => Object.fromEntries(Object.entries(values ?? {}).filter(([id]) => allowed.has(Number(id))));
  return { events, trace: { retrieval: {
    event_ids: events.map(event => event.id),
    score_breakdowns: pick(retrieval.score_breakdowns),
    candidate_evidence: pick(retrieval.candidate_evidence),
  } } };
}
