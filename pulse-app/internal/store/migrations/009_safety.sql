-- 009_safety.sql
-- Structural opt-out for auto-probing and graph-expansion over sensitive entities.
-- `do_not_probe = 1` means:
--   • consolidation will NOT auto-generate "Что сейчас с X?" knowledge-gap questions
--   • retrieval BFS will NOT traverse through this entity during neighbor expansion
-- Default 0 = prior behaviour, so all existing rows pass through safety gates unchanged.

ALTER TABLE entities ADD COLUMN do_not_probe INTEGER NOT NULL DEFAULT 0;
