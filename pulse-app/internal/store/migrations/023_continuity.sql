CREATE TABLE continuity_threads (
    thread_id   TEXT PRIMARY KEY,
    project_id  TEXT,
    title       TEXT,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE continuity_sessions (
    session_id  TEXT PRIMARY KEY,
    thread_id   TEXT NOT NULL REFERENCES continuity_threads(thread_id) ON DELETE CASCADE,
    host        TEXT NOT NULL,
    project_id  TEXT,
    started_at  TEXT NOT NULL,
    ended_at    TEXT,
    status      TEXT NOT NULL CHECK(status IN ('active','checkpointed','archived'))
);
CREATE INDEX idx_continuity_sessions_thread ON continuity_sessions(thread_id, started_at DESC);
CREATE INDEX idx_continuity_sessions_status ON continuity_sessions(status);

CREATE TABLE continuity_observations (
    id                INTEGER PRIMARY KEY,
    session_id        TEXT NOT NULL,
    thread_id         TEXT NOT NULL,
    host              TEXT NOT NULL,
    event_type        TEXT NOT NULL,
    redacted_summary  TEXT NOT NULL,
    raw_ref           TEXT,
    source_ref        TEXT,
    created_at        TEXT NOT NULL
);
CREATE INDEX idx_continuity_observations_thread ON continuity_observations(thread_id, created_at DESC);
CREATE INDEX idx_continuity_observations_session ON continuity_observations(session_id, created_at DESC);

CREATE TABLE continuity_checkpoints (
    id                       INTEGER PRIMARY KEY,
    thread_id                TEXT NOT NULL REFERENCES continuity_threads(thread_id) ON DELETE CASCADE,
    session_id               TEXT NOT NULL,
    host                     TEXT NOT NULL,
    project_id               TEXT,
    summary                  TEXT NOT NULL,
    decisions_json           TEXT NOT NULL,
    open_loops_json          TEXT NOT NULL,
    do_not_repeat_json       TEXT NOT NULL,
    emotional_anchors_json   TEXT NOT NULL,
    state_signals_json       TEXT NOT NULL,
    source_refs_json         TEXT NOT NULL,
    confidence               REAL NOT NULL DEFAULT 0,
    created_at               TEXT NOT NULL
);
CREATE INDEX idx_continuity_checkpoints_thread ON continuity_checkpoints(thread_id, created_at DESC);
CREATE INDEX idx_continuity_checkpoints_session ON continuity_checkpoints(session_id);
