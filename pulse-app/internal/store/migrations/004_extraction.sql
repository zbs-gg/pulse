CREATE TABLE extraction_jobs (
    id              INTEGER PRIMARY KEY,
    observation_ids TEXT NOT NULL,
    state           TEXT NOT NULL CHECK(state IN ('pending','running','done','failed','dlq')),
    attempts        INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT,
    triage_model    TEXT,
    extract_model   TEXT,
    triage_verdict  TEXT CHECK(triage_verdict IS NULL OR triage_verdict IN ('extract','skip','defer')),
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);
CREATE INDEX idx_extraction_state ON extraction_jobs(state, created_at);
