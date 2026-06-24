-- Claim-resolution metadata for safe, precision-first supersession.
-- Additive only; does NOT touch v3 scoring or the events/facts/emotions tables.
--
-- object_norm        : normalized object value (object-differs guard / duplicate detection)
-- ctx_vec            : JSON float32 vector of embed("<subject> <predicate> <object>"),
--                      so a later claim can be compared by meaning, not just by key string
-- resolution_cosine  : audit — the cosine that justified a supersede (NULL if not resolved)
ALTER TABLE assertions ADD COLUMN object_norm       TEXT;
ALTER TABLE assertions ADD COLUMN ctx_vec           TEXT;
ALTER TABLE assertions ADD COLUMN resolution_cosine REAL;

CREATE INDEX idx_assertions_objnorm ON assertions(claim_key, object_norm);
