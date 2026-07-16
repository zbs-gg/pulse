-- Trusted Codex card-presentation and exact-OK approval leases. Personal and
-- Desk stores only. Rows are content-free and bind only identifiers/digests.

CREATE TABLE git_memory_hook_presentations (
    presentation_id       TEXT PRIMARY KEY,
    generation_id         TEXT NOT NULL UNIQUE REFERENCES git_memory_card_generations(generation_id) ON DELETE RESTRICT,
    batch_id              TEXT NOT NULL REFERENCES git_memory_review_batches(batch_id) ON DELETE RESTRICT,
    batch_generation      INTEGER NOT NULL CHECK(batch_generation >= 1),
    host                  TEXT NOT NULL CHECK(host='codex'),
    task_id               TEXT NOT NULL,
    session_ref           TEXT NOT NULL,
    turn_ref              TEXT NOT NULL,
    source_event_digest   TEXT NOT NULL,
    card_block_digest     TEXT NOT NULL,
    candidate_digests_json TEXT NOT NULL,
    state                 TEXT NOT NULL CHECK(state IN ('presented','approved','invalidated')),
    presented_at          TEXT NOT NULL,
    expires_at            TEXT NOT NULL,
    UNIQUE(host, session_ref, source_event_digest),
    CHECK(length(source_event_digest)=64 AND source_event_digest NOT GLOB '*[^a-f0-9]*'),
    CHECK(length(card_block_digest)=64 AND card_block_digest NOT GLOB '*[^a-f0-9]*')
);
CREATE INDEX idx_git_memory_hook_presentations_pending
    ON git_memory_hook_presentations(host, session_ref, state, expires_at, presentation_id);

CREATE TABLE git_memory_approval_leases (
    lease_id               TEXT PRIMARY KEY,
    presentation_id        TEXT NOT NULL UNIQUE REFERENCES git_memory_hook_presentations(presentation_id) ON DELETE RESTRICT,
    batch_id               TEXT NOT NULL UNIQUE REFERENCES git_memory_review_batches(batch_id) ON DELETE RESTRICT,
    batch_generation       INTEGER NOT NULL CHECK(batch_generation >= 1),
    session_ref            TEXT NOT NULL,
    prompt_event_digest    TEXT NOT NULL UNIQUE,
    authority_digest       TEXT NOT NULL,
    candidate_digests_json TEXT NOT NULL,
    state                  TEXT NOT NULL CHECK(state IN ('issued','consumed','expired')),
    issued_at              TEXT NOT NULL,
    expires_at             TEXT NOT NULL,
    consumed_at            TEXT,
    CHECK(length(prompt_event_digest)=64 AND prompt_event_digest NOT GLOB '*[^a-f0-9]*'),
    CHECK(length(authority_digest)=64 AND authority_digest NOT GLOB '*[^a-f0-9]*')
);
CREATE INDEX idx_git_memory_approval_leases_state
    ON git_memory_approval_leases(state, expires_at, lease_id);

CREATE TRIGGER git_memory_hook_presentations_immutable_identity
BEFORE UPDATE OF generation_id, batch_id, batch_generation, host, task_id,
                 session_ref, turn_ref, source_event_digest, card_block_digest,
                 candidate_digests_json, presented_at, expires_at
ON git_memory_hook_presentations
BEGIN SELECT RAISE(ABORT, 'git memory hook presentation identity is immutable'); END;

CREATE TRIGGER git_memory_approval_leases_immutable_identity
BEFORE UPDATE OF presentation_id, batch_id, batch_generation, session_ref,
                 prompt_event_digest, authority_digest, candidate_digests_json,
                 issued_at, expires_at
ON git_memory_approval_leases
BEGIN SELECT RAISE(ABORT, 'git memory approval lease identity is immutable'); END;

UPDATE store_identity
   SET min_reader_version = 49,
       min_writer_version = 49
 WHERE singleton = 1;
