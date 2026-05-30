-- Canonical entities (people, places, projects, orgs, things)
CREATE TABLE entities (
    id                INTEGER PRIMARY KEY,
    canonical_name    TEXT NOT NULL,
    kind              TEXT NOT NULL CHECK(kind IN ('person','place','project','org','thing','event_series')),
    aliases           TEXT,
    first_seen        TEXT NOT NULL,
    last_seen         TEXT NOT NULL,
    salience_score    REAL NOT NULL DEFAULT 0,
    emotional_weight  REAL NOT NULL DEFAULT 0,
    scorer_version    TEXT,
    description_md    TEXT
);
CREATE INDEX idx_entities_kind ON entities(kind);

-- One entity → many source identifiers
CREATE TABLE entity_identities (
    id           INTEGER PRIMARY KEY,
    entity_id    INTEGER NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    source_kind  TEXT NOT NULL,
    identifier   TEXT NOT NULL,
    confidence   REAL NOT NULL DEFAULT 1.0,
    first_seen   TEXT NOT NULL,
    UNIQUE(source_kind, identifier)
);
CREATE INDEX idx_identities_entity ON entity_identities(entity_id);

-- Entity merge proposals (confidence-gated)
CREATE TABLE entity_merge_proposals (
    id             INTEGER PRIMARY KEY,
    from_entity_id INTEGER NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    to_entity_id   INTEGER NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    confidence     REAL NOT NULL,
    evidence_md    TEXT NOT NULL,
    state          TEXT NOT NULL CHECK(state IN ('pending','approved','rejected','auto_merged')),
    proposed_at    TEXT NOT NULL,
    resolved_at    TEXT,
    resolved_by    TEXT
);
CREATE INDEX idx_merge_state ON entity_merge_proposals(state);

-- Sensitive actors allowlist
CREATE TABLE sensitive_actors (
    entity_id   INTEGER PRIMARY KEY REFERENCES entities(id) ON DELETE CASCADE,
    policy      TEXT NOT NULL CHECK(policy IN ('redact_content','summary_only','no_capture')),
    reason      TEXT,
    added_at    TEXT NOT NULL,
    added_by    TEXT NOT NULL
);

-- Relations between entities
CREATE TABLE relations (
    id                 INTEGER PRIMARY KEY,
    from_entity_id     INTEGER NOT NULL REFERENCES entities(id),
    to_entity_id       INTEGER NOT NULL REFERENCES entities(id),
    kind               TEXT NOT NULL,
    strength           REAL NOT NULL DEFAULT 0,
    first_seen         TEXT NOT NULL,
    last_seen          TEXT NOT NULL
);
CREATE INDEX idx_relations_from ON relations(from_entity_id);
CREATE INDEX idx_relations_to   ON relations(to_entity_id);

-- Facts (atomic claims about entities)
CREATE TABLE facts (
    id                 INTEGER PRIMARY KEY,
    entity_id          INTEGER NOT NULL REFERENCES entities(id),
    text               TEXT NOT NULL,
    confidence         REAL NOT NULL DEFAULT 1.0,
    scorer_version     TEXT,
    created_at         TEXT NOT NULL
);
CREATE INDEX idx_facts_entity ON facts(entity_id);

-- Events
CREATE TABLE events (
    id                 INTEGER PRIMARY KEY,
    title              TEXT NOT NULL,
    description        TEXT,
    sentiment          REAL,
    emotional_weight   REAL NOT NULL DEFAULT 0,
    scorer_version     TEXT,
    ts                 TEXT NOT NULL
);
CREATE INDEX idx_events_ts ON events(ts);

-- Normalized evidence
CREATE TABLE evidence (
    id               INTEGER PRIMARY KEY,
    subject_kind     TEXT NOT NULL CHECK(subject_kind IN ('relation','fact','event','entity')),
    subject_id       INTEGER NOT NULL,
    observation_id   INTEGER NOT NULL REFERENCES observations(id) ON DELETE CASCADE,
    weight           REAL NOT NULL DEFAULT 1.0,
    created_at       TEXT NOT NULL
);
CREATE INDEX idx_evidence_subject ON evidence(subject_kind, subject_id);
CREATE INDEX idx_evidence_obs     ON evidence(observation_id);

-- Score history
CREATE TABLE score_history (
    id               INTEGER PRIMARY KEY,
    subject_kind     TEXT NOT NULL,
    subject_id       INTEGER NOT NULL,
    salience         REAL,
    emotional_weight REAL,
    sentiment        REAL,
    scorer_version   TEXT NOT NULL,
    computed_at      TEXT NOT NULL
);
CREATE INDEX idx_score_subject ON score_history(subject_kind, subject_id, computed_at);

-- Open questions the assistant holds for the next sync
CREATE TABLE open_questions (
    id                INTEGER PRIMARY KEY,
    subject_entity_id INTEGER REFERENCES entities(id),
    question_text     TEXT NOT NULL,
    asked_at          TEXT NOT NULL,
    ttl_expires_at    TEXT NOT NULL,
    answered_at       TEXT,
    answer_text       TEXT,
    state             TEXT NOT NULL CHECK(state IN ('open','answered','expired','auto_closed'))
);
CREATE INDEX idx_questions_state ON open_questions(state, ttl_expires_at);
