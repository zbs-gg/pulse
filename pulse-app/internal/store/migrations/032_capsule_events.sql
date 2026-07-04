-- 032_capsule_events.sql
-- Capsule → event projection: make remembered memory reachable by the
-- state-aware retrieval engine.
--
-- Before this migration RememberCapsule wrote ONLY to memory_capsules; the
-- retrieval engine reads events JOIN event_embeddings, so remembered capsules
-- could never surface through /retrieve or /context/query. This migration
-- adds the two additive columns the projection needs:
--
--   memory_capsules.event_id — link to the projected event row
--                              (NULL = not projected; only privacy_tier
--                              'normal' items are projected). This link is
--                              the authoritative capsule-origin marker: the
--                              event row itself uses the CHECK-allowed
--                              provenance 'interactive_memory' (migration
--                              014's value set can't be widened without a
--                              full events-table rebuild).
--   events.tags              — JSON array of the capsule's tags
--                              (NULL = none). Read ONLY by the post-scoring
--                              state-tag affinity step; the frozen v3 scorer
--                              never sees it.
--
-- Additive / backward-compatible (same ALTER pattern as migrations 029/031):
-- existing rows get NULL for both columns, which means "no projection" and
-- "no tags" — no observable behavior change until a capsule is written or the
-- startup backfill runs.

ALTER TABLE memory_capsules ADD COLUMN event_id INTEGER;
ALTER TABLE events ADD COLUMN tags TEXT;

CREATE INDEX IF NOT EXISTS idx_memory_capsules_event ON memory_capsules(event_id);
