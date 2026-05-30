-- 010_self_entity.sql
-- Marks the owner's self-entity so retrieval can exclude it from its own anchor boost
-- and BFS expansion ranking. Observation: self is frozen at seed values,
-- never decays, and dominates anchor contests. Without this flag, every retrieval
-- about anyone else still ranks the self-entity as top-1.
ALTER TABLE entities ADD COLUMN is_self INTEGER NOT NULL DEFAULT 0;
