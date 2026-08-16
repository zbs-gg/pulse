-- Migration 041 pinned its model_id column to the only qualified model at the
-- time. Keep that immutable compatibility field for old databases and record
-- the model that actually executed each job in a new authoritative column.
-- ALTER TABLE preserves every historical job and all of its foreign keys.

ALTER TABLE historical_ingest_jobs
    ADD COLUMN execution_model_id TEXT NOT NULL DEFAULT 'gpt-5.6-luna'
    CHECK(execution_model_id IN ('gpt-5.6-luna', 'gpt-5.4'));

UPDATE store_identity
   SET min_reader_version = 44,
       min_writer_version = 44
 WHERE singleton = 1
   AND store_kind = 'personal';
