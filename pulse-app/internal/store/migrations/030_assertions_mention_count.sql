-- Corroboration signal: how many times the same active claim (same claim_key,
-- scope, object_text) has been re-confirmed, and when it was last mentioned.
-- Additive metadata ONLY: two ALTER TABLE ADD COLUMN statements, SQLite-safe,
-- backfilling existing rows to mention_count = 1 / last_mentioned_at = NULL.
--
-- Does NOT touch v3 emotional scoring and is NOT read by scoreEventsV3. This is
-- pure metadata surfaced on read; it is never routed into event retrieval.
ALTER TABLE assertions ADD COLUMN mention_count INTEGER NOT NULL DEFAULT 1;
ALTER TABLE assertions ADD COLUMN last_mentioned_at TEXT; -- RFC3339, nullable
