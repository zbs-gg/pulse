-- Phase 1: data-consistency UNIQUEs, event↔entity junction, extraction checkpoint.
--
-- Non-breaking: pre-migration audit (scripts/phase1_audit.py) confirms no
-- existing duplicates in relations(from,to,kind) or facts(entity_id,text).
-- Resolver has been doing dedup at the application layer; this pins the
-- invariant at the schema layer.

CREATE UNIQUE INDEX idx_relations_unique ON relations(from_entity_id, to_entity_id, kind);
CREATE UNIQUE INDEX idx_facts_unique     ON facts(entity_id, text);
-- Intentionally NO UNIQUE on entities(canonical_name, kind): resolver owns
-- canonical-name dedup, and legitimate same-name-different-kind entities
-- ("Phoenix" person vs "Phoenix" place) must stay permissible. Phase 3 alias index
-- closes the resolver side properly.

-- Junction table: which entities an event involves. Replaces the in-memory
-- `entities_involved` list that Phase 0 used to reject orphan events.
CREATE TABLE event_entities (
    event_id   INTEGER NOT NULL REFERENCES events(id)   ON DELETE CASCADE,
    entity_id  INTEGER NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    PRIMARY KEY (event_id, entity_id)
);
CREATE INDEX idx_event_entities_entity ON event_entities(entity_id);

-- Checkpoint for two-stage extraction: persists triage verdicts and per-obs
-- extract results so a crashed job resumes without repeating LLM calls.
CREATE TABLE extraction_artifacts (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id       INTEGER NOT NULL REFERENCES extraction_jobs(id) ON DELETE CASCADE,
    kind         TEXT NOT NULL CHECK (kind IN ('triage','extract')),
    obs_id       INTEGER REFERENCES observations(id) ON DELETE CASCADE,  -- NULL for kind='triage'
    payload_json TEXT NOT NULL,
    model        TEXT NOT NULL,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
-- Partial UNIQUEs: one triage artifact per job, one extract artifact per (job, obs).
-- Plain composite UNIQUE(job_id,kind,obs_id) would allow duplicate triage rows
-- because SQLite treats NULLs as distinct.
CREATE UNIQUE INDEX idx_artifacts_triage_unique  ON extraction_artifacts(job_id)          WHERE kind = 'triage';
CREATE UNIQUE INDEX idx_artifacts_extract_unique ON extraction_artifacts(job_id, obs_id)  WHERE kind = 'extract';
CREATE INDEX        idx_artifacts_job            ON extraction_artifacts(job_id, kind);
