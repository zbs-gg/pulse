-- Near-duplicate capsule consolidation (invalidate-not-delete).
-- Additive columns with safe defaults: every existing row is 'active', so this
-- migration changes no observable behavior until an explicit consolidation pass
-- marks a near-duplicate row 'merged'. Never DELETE — merged rows stay queryable
-- and still export (provenance retained).

ALTER TABLE memory_capsules ADD COLUMN status      TEXT NOT NULL DEFAULT 'active';
ALTER TABLE memory_capsules ADD COLUMN merged_into TEXT;   -- id of the kept capsule
ALTER TABLE memory_capsules ADD COLUMN merged_at   TEXT;   -- RFC3339 when merged

CREATE INDEX IF NOT EXISTS idx_memory_capsules_status ON memory_capsules(status);
