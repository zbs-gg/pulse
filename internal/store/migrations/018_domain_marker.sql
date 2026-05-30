-- 018_domain_marker.sql
-- Phase H — Real-vs-fiction domain marker on facts and events.
--
-- Rationale (graph review):
--   Extraction was building graphs that conflated real life with work on
--   a creative project. Example: a fact "the author left line 642 unchanged"
--   (meta-authorial decision) sat in the same belief_class='operational'
--   as a real-life event. Retrieval/synthesis using such a graph could
--   pull fictional events into real-life contexts, violating the
--   safe-space rule.
--
--   Entity-level fiction kinds (fictional_character, narrative_device,
--   fictionalized_self) already exist but only mark WHO. They don't say
--   whether THIS FACT is about real life, fiction content, or authoring.
--
--   This migration adds `domain` to facts and events with values:
--     real            — actual life. DEFAULT.
--     fiction_content — canon (chapter content, fictional event)
--     fiction_meta    — work on the project (editing, canon decisions)
--     meta_authorial  — author entity context
--
-- Backward-compat: existing rows default to 'real' so old data stays
-- usable until re-extraction. The system is now domain-aware downstream
-- (retrieval, viewer, synthesis-boundary-checker).

ALTER TABLE facts ADD COLUMN domain TEXT NOT NULL DEFAULT 'real'
  CHECK (domain IN ('real', 'fiction_content', 'fiction_meta', 'meta_authorial'));

ALTER TABLE events ADD COLUMN domain TEXT NOT NULL DEFAULT 'real'
  CHECK (domain IN ('real', 'fiction_content', 'fiction_meta', 'meta_authorial'));

CREATE INDEX idx_facts_domain  ON facts(domain);
CREATE INDEX idx_events_domain ON events(domain);
