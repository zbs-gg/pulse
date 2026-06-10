-- Raw normalized events from all sources (append-only, edits → new version row)
CREATE TABLE observations (
    id            INTEGER PRIMARY KEY,
    source_kind   TEXT NOT NULL,
    source_id     TEXT NOT NULL,
    content_hash  TEXT NOT NULL,
    version       INTEGER NOT NULL DEFAULT 1,
    scope         TEXT NOT NULL CHECK(scope IN ('assistant','user','shared')),
    captured_at   TEXT NOT NULL,
    observed_at   TEXT NOT NULL,
    actors        TEXT NOT NULL,
    content_text  TEXT,
    media_refs    TEXT,
    metadata      TEXT,
    raw_json      TEXT,
    redacted      INTEGER NOT NULL DEFAULT 0,
    UNIQUE(source_kind, source_id, version)
);
CREATE INDEX idx_obs_captured ON observations(captured_at);
CREATE INDEX idx_obs_scope    ON observations(scope, captured_at);
CREATE INDEX idx_obs_sourceid ON observations(source_kind, source_id);

-- Edit history for observations
CREATE TABLE observation_revisions (
    observation_id INTEGER NOT NULL REFERENCES observations(id) ON DELETE CASCADE,
    version        INTEGER NOT NULL,
    prev_hash      TEXT,
    diff           TEXT,
    changed_at     TEXT NOT NULL,
    PRIMARY KEY (observation_id, version)
);

-- Per-provider cursor for periodic-pull sources
CREATE TABLE provider_cursors (
    source_kind  TEXT PRIMARY KEY,
    cursor       TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

-- Erasure audit log
CREATE TABLE erasure_log (
    id            INTEGER PRIMARY KEY,
    op_kind       TEXT NOT NULL CHECK(op_kind IN ('soft','hard','nuclear')),
    subject_kind  TEXT NOT NULL,
    subject_id    TEXT,
    initiated_by  TEXT NOT NULL,
    initiated_at  TEXT NOT NULL,
    completed_at  TEXT,
    note          TEXT
);
