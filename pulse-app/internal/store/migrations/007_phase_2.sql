-- 007_phase_2.sql
-- Phase 2: Expanded entity kinds (10), relation context, fact provenance, extraction metrics

PRAGMA foreign_keys = OFF;

CREATE TABLE entities_new (
    id                INTEGER PRIMARY KEY,
    canonical_name    TEXT NOT NULL,
    kind              TEXT NOT NULL CHECK(kind IN (
        'person','place','project','org','product',
        'community','skill','concept','thing','event_series'
    )),
    aliases           TEXT,
    first_seen        TEXT NOT NULL,
    last_seen         TEXT NOT NULL,
    salience_score    REAL NOT NULL DEFAULT 0,
    emotional_weight  REAL NOT NULL DEFAULT 0,
    scorer_version    TEXT,
    description_md    TEXT,
    extractor_version TEXT NOT NULL DEFAULT 'v1'
);

INSERT INTO entities_new (id, canonical_name, kind, aliases, first_seen, last_seen,
    salience_score, emotional_weight, scorer_version, description_md, extractor_version)
SELECT id, canonical_name, kind, aliases, first_seen, last_seen,
    salience_score, emotional_weight, scorer_version, description_md, 'v1'
FROM entities;

DROP TABLE entities;
ALTER TABLE entities_new RENAME TO entities;

CREATE INDEX idx_entities_kind ON entities(kind);

PRAGMA foreign_keys = ON;

ALTER TABLE relations ADD COLUMN context TEXT;

ALTER TABLE facts ADD COLUMN verified INTEGER NOT NULL DEFAULT 0;
ALTER TABLE facts ADD COLUMN verified_by TEXT;
ALTER TABLE facts ADD COLUMN source_obs_id INTEGER REFERENCES observations(id);
ALTER TABLE facts ADD COLUMN extractor_version TEXT NOT NULL DEFAULT 'v1';

CREATE TABLE extraction_metrics (
    id            INTEGER PRIMARY KEY,
    job_id        INTEGER NOT NULL REFERENCES extraction_jobs(id),
    model         TEXT NOT NULL,
    input_tokens  INTEGER,
    output_tokens INTEGER,
    cost_usd      REAL,
    latency_ms    INTEGER,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
