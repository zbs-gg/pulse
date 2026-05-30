-- 008_consolidation.sql
-- Key-value store for consolidation pipeline state (skip-guard timestamps, etc.)

CREATE TABLE IF NOT EXISTS consolidation_metadata (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_open_questions_dedup
    ON open_questions(subject_entity_id, question_text)
    WHERE state = 'open';
