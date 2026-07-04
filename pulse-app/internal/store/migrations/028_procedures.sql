-- Procedural memory: parameterized, reusable workflows ("skills"/reflexes).
-- A fourth memory type alongside episodic (memory_capsules), semantic
-- (assertions), and emotional signal (v3). Additive and inert: nothing in
-- retrieval or the write path reads or writes this table yet. Does NOT touch
-- v3 emotional scoring.
--
-- A procedure is identified by a normalized name_key (mirroring assertions'
-- claim_key) so a re-learned procedure with the same name supersedes cleanly
-- via upsert instead of duplicating. params_json/steps_json are opaque JSON
-- the engine never interprets, embeds, or executes.
CREATE TABLE procedures (
    id            INTEGER PRIMARY KEY,
    name          TEXT NOT NULL,               -- human-readable name
    name_key      TEXT NOT NULL,               -- normalized name: the upsert identity
    description   TEXT NOT NULL DEFAULT '',
    params_json   TEXT NOT NULL DEFAULT '{}',  -- JSON object: parameter schema/defaults
    steps_json    TEXT NOT NULL DEFAULT '[]',  -- JSON array: ordered steps
    success_count INTEGER NOT NULL DEFAULT 0,

    -- typed scope, same model as assertions: cross-harness recall is
    -- deliberate, not accidental bleed.
    scope_type    TEXT NOT NULL DEFAULT 'personal'
                    CHECK(scope_type IN ('personal','project','repo','agent','session')),
    scope_id      TEXT NOT NULL DEFAULT '',

    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE UNIQUE INDEX idx_procedures_key
    ON procedures(name_key, scope_type, scope_id);
CREATE INDEX idx_procedures_scope
    ON procedures(scope_type, scope_id);
