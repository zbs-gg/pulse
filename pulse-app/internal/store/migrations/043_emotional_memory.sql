-- Local emotional memory. This extends the existing per-event emotion vector
-- with provenance, a short cause, and one bounded follow-up question. Raw
-- prompts and full conversations are never stored here.

ALTER TABLE event_emotions ADD COLUMN derivation TEXT NOT NULL DEFAULT 'inferred'
    CHECK(derivation IN ('explicit','inferred','user_confirmed'));
ALTER TABLE event_emotions ADD COLUMN observed_label TEXT NOT NULL DEFAULT '';
ALTER TABLE event_emotions ADD COLUMN trigger_summary TEXT NOT NULL DEFAULT '';
ALTER TABLE event_emotions ADD COLUMN trigger_derivation TEXT NOT NULL DEFAULT ''
    CHECK(trigger_derivation IN ('','explicit','inferred','user_confirmed'));
ALTER TABLE event_emotions ADD COLUMN trigger_confidence REAL NOT NULL DEFAULT 0
    CHECK(trigger_confidence BETWEEN 0 AND 1);
ALTER TABLE event_emotions ADD COLUMN trigger_confirmed INTEGER NOT NULL DEFAULT 0
    CHECK(trigger_confirmed IN (0,1));
ALTER TABLE event_emotions ADD COLUMN emotion_key TEXT NOT NULL DEFAULT '';

ALTER TABLE events ADD COLUMN semantic_digest TEXT
    CHECK(semantic_digest IS NULL OR (
        length(semantic_digest) = 64
        AND semantic_digest NOT GLOB '*[^a-f0-9]*'
    ));
CREATE UNIQUE INDEX idx_events_semantic_digest
    ON events(semantic_digest)
    WHERE semantic_digest IS NOT NULL;

CREATE TABLE emotion_questions (
    question_id    TEXT PRIMARY KEY,
    event_id       INTEGER NOT NULL UNIQUE REFERENCES events(id) ON DELETE CASCADE,
    question_text  TEXT NOT NULL,
    asked_at       TEXT NOT NULL,
    expires_at     TEXT NOT NULL,
    delivered_at   TEXT,
    answered_at    TEXT,
    state          TEXT NOT NULL CHECK(state IN ('open','answered','expired')),
    answer_event_id INTEGER REFERENCES events(id) ON DELETE SET NULL
);
CREATE INDEX idx_emotion_questions_open
    ON emotion_questions(state, expires_at, asked_at)
    WHERE state='open';

-- Projection rebuilds replace event rows. Delivery lives outside that
-- projection so saving another memory cannot offer an old question again.
CREATE TABLE emotion_question_delivery (
    question_id  TEXT PRIMARY KEY,
    asked_at     TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    delivered_at TEXT,
    answered_at  TEXT
);

CREATE TABLE emotion_overrides (
    emotion_key TEXT PRIMARY KEY,
    action      TEXT NOT NULL CHECK(action IN ('update','delete')),
    payload_json TEXT NOT NULL DEFAULT '{}',
    updated_at  TEXT NOT NULL
);

UPDATE store_identity
   SET min_reader_version = 43,
       min_writer_version = 43
 WHERE singleton = 1;
