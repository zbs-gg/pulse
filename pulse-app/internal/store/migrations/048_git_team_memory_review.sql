-- Local Git-backed Team Memory review authority. Personal and Desk stores
-- only. Source rows retain attested metadata and digests, never source bytes.

CREATE TABLE git_memory_projects (
    portable_project_id TEXT PRIMARY KEY,
    repository_id       TEXT NOT NULL UNIQUE,
    binding_digest      TEXT NOT NULL,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    CHECK(length(binding_digest)=64 AND binding_digest NOT GLOB '*[^a-f0-9]*')
);

CREATE TABLE git_memory_sources (
    source_id              TEXT PRIMARY KEY,
    portable_project_id    TEXT NOT NULL REFERENCES git_memory_projects(portable_project_id) ON DELETE RESTRICT,
    source_kind            TEXT NOT NULL CHECK(source_kind='repository_text'),
    locator                TEXT NOT NULL,
    current_version_id     TEXT NOT NULL,
    current_version_digest TEXT NOT NULL,
    current_byte_count     INTEGER NOT NULL CHECK(current_byte_count BETWEEN 0 AND 8388608),
    current_observed_at    TEXT NOT NULL,
    registered_at          TEXT NOT NULL,
    updated_at             TEXT NOT NULL,
    processing_state       TEXT NOT NULL DEFAULT 'pending' CHECK(processing_state IN ('pending','reviewed')),
    UNIQUE(portable_project_id, source_kind, locator),
    CHECK(length(current_version_digest)=64 AND current_version_digest NOT GLOB '*[^a-f0-9]*')
);

CREATE TABLE git_memory_source_versions (
    version_id          TEXT PRIMARY KEY,
    source_id           TEXT NOT NULL REFERENCES git_memory_sources(source_id) ON DELETE RESTRICT,
    version_digest      TEXT NOT NULL,
    byte_count          INTEGER NOT NULL CHECK(byte_count BETWEEN 0 AND 8388608),
    observed_at         TEXT NOT NULL,
    registered_at       TEXT NOT NULL,
    processing_state    TEXT NOT NULL DEFAULT 'pending' CHECK(processing_state IN ('pending','reviewed')),
    UNIQUE(source_id, version_digest),
    CHECK(length(version_digest)=64 AND version_digest NOT GLOB '*[^a-f0-9]*')
);

CREATE TRIGGER git_memory_source_versions_immutable
BEFORE UPDATE ON git_memory_source_versions
BEGIN SELECT RAISE(ABORT, 'git memory source versions are immutable'); END;
CREATE TRIGGER git_memory_source_versions_no_delete
BEFORE DELETE ON git_memory_source_versions
BEGIN SELECT RAISE(ABORT, 'git memory source versions are append-only'); END;

CREATE TABLE git_memory_review_batches (
    batch_id               TEXT PRIMARY KEY,
    portable_project_id    TEXT NOT NULL REFERENCES git_memory_projects(portable_project_id) ON DELETE RESTRICT,
    source_id              TEXT NOT NULL REFERENCES git_memory_sources(source_id) ON DELETE RESTRICT,
    source_version_id      TEXT NOT NULL REFERENCES git_memory_source_versions(version_id) ON DELETE RESTRICT,
    source_version_digest  TEXT NOT NULL,
    host                   TEXT NOT NULL,
    task_id                TEXT NOT NULL,
    idempotency_key        TEXT NOT NULL,
    request_digest         TEXT NOT NULL,
    generation             INTEGER NOT NULL DEFAULT 1 CHECK(generation >= 1),
    state                  TEXT NOT NULL CHECK(state IN ('staged','rejected','approved','publishing','published_uncommitted','committed','indexed','failed')),
    created_at             TEXT NOT NULL,
    updated_at             TEXT NOT NULL,
    UNIQUE(portable_project_id, idempotency_key),
    CHECK(length(source_version_digest)=64 AND source_version_digest NOT GLOB '*[^a-f0-9]*'),
    CHECK(length(request_digest)=64 AND request_digest NOT GLOB '*[^a-f0-9]*')
);

CREATE TABLE git_memory_review_candidates (
    candidate_id        TEXT PRIMARY KEY,
    batch_id            TEXT NOT NULL REFERENCES git_memory_review_batches(batch_id) ON DELETE RESTRICT,
    ordinal             INTEGER NOT NULL CHECK(ordinal >= 0),
    current_version     INTEGER NOT NULL CHECK(current_version >= 1),
    current_digest      TEXT NOT NULL,
    state               TEXT NOT NULL CHECK(state IN ('staged','rejected','approved','publishing','published','superseded','removed')),
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    terminal_at         TEXT,
    UNIQUE(batch_id, ordinal),
    CHECK(length(current_digest)=64 AND current_digest NOT GLOB '*[^a-f0-9]*')
);

CREATE TABLE git_memory_candidate_versions (
    candidate_id        TEXT NOT NULL REFERENCES git_memory_review_candidates(candidate_id) ON DELETE RESTRICT,
    version             INTEGER NOT NULL CHECK(version >= 1),
    candidate_kind      TEXT NOT NULL,
    statement           TEXT NOT NULL,
    audience            TEXT NOT NULL CHECK(audience='project'),
    confidence          REAL NOT NULL CHECK(confidence BETWEEN 0 AND 1),
    source_refs_json    TEXT NOT NULL,
    warnings_json       TEXT NOT NULL,
    content_digest      TEXT NOT NULL,
    created_at          TEXT NOT NULL,
    PRIMARY KEY(candidate_id, version),
    CHECK(length(content_digest)=64 AND content_digest NOT GLOB '*[^a-f0-9]*')
);

CREATE TRIGGER git_memory_candidate_versions_immutable
BEFORE UPDATE ON git_memory_candidate_versions
BEGIN SELECT RAISE(ABORT, 'git memory candidate versions are immutable'); END;
CREATE TRIGGER git_memory_candidate_versions_no_delete
BEFORE DELETE ON git_memory_candidate_versions
BEGIN SELECT RAISE(ABORT, 'git memory candidate versions are append-only'); END;

-- U2 records only ordered digests and host-event authority here. Card text and
-- raw assistant/user messages are deliberately absent.
CREATE TABLE git_memory_card_generations (
    generation_id           TEXT PRIMARY KEY,
    batch_id                TEXT NOT NULL REFERENCES git_memory_review_batches(batch_id) ON DELETE RESTRICT,
    batch_generation        INTEGER NOT NULL CHECK(batch_generation >= 1),
    card_block_digest       TEXT NOT NULL,
    candidate_digests_json TEXT NOT NULL,
    authority_kind          TEXT NOT NULL CHECK(authority_kind IN ('codex_stop','memory_home')),
    state                   TEXT NOT NULL CHECK(state IN ('created','presented','invalidated')),
    created_at              TEXT NOT NULL,
    presented_at            TEXT,
    CHECK(length(card_block_digest)=64 AND card_block_digest NOT GLOB '*[^a-f0-9]*')
);

CREATE TABLE git_memory_review_decisions (
    decision_id         TEXT PRIMARY KEY,
    batch_id            TEXT NOT NULL REFERENCES git_memory_review_batches(batch_id) ON DELETE RESTRICT,
    candidate_id        TEXT REFERENCES git_memory_review_candidates(candidate_id) ON DELETE RESTRICT,
    candidate_version   INTEGER NOT NULL CHECK(candidate_version >= 0),
    candidate_digest    TEXT,
    decision            TEXT NOT NULL CHECK(decision IN ('rejected','approved','canceled')),
    reason_code         TEXT NOT NULL CHECK(reason_code IN ('user_rejected','user_canceled','trusted_exact_ok')),
    authority_digest    TEXT,
    created_at          TEXT NOT NULL,
    CHECK(candidate_digest IS NULL OR (length(candidate_digest)=64 AND candidate_digest NOT GLOB '*[^a-f0-9]*')),
    CHECK(authority_digest IS NULL OR (length(authority_digest)=64 AND authority_digest NOT GLOB '*[^a-f0-9]*'))
);
CREATE TRIGGER git_memory_review_decisions_immutable
BEFORE UPDATE ON git_memory_review_decisions
BEGIN SELECT RAISE(ABORT, 'git memory review decisions are immutable'); END;
CREATE TRIGGER git_memory_review_decisions_no_delete
BEFORE DELETE ON git_memory_review_decisions
BEGIN SELECT RAISE(ABORT, 'git memory review decisions are append-only'); END;

CREATE TABLE git_memory_publications (
    publication_id      TEXT PRIMARY KEY,
    batch_id            TEXT NOT NULL UNIQUE REFERENCES git_memory_review_batches(batch_id) ON DELETE RESTRICT,
    state               TEXT NOT NULL CHECK(state IN ('publishing','published_uncommitted','committed','failed')),
    expected_parent     TEXT,
    files_digest        TEXT,
    commit_hash         TEXT,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

CREATE TABLE git_memory_index_state (
    portable_project_id TEXT PRIMARY KEY REFERENCES git_memory_projects(portable_project_id) ON DELETE RESTRICT,
    head_commit         TEXT,
    pack_digest         TEXT,
    state               TEXT NOT NULL CHECK(state IN ('pending','indexed','failed')),
    updated_at          TEXT NOT NULL
);

-- Receipts and audit are content-free: identifiers, digests, bounded enums and
-- timestamps only. They never carry candidate/source/prompt text.
CREATE TABLE git_memory_receipts (
    receipt_id          TEXT PRIMARY KEY,
    batch_id            TEXT REFERENCES git_memory_review_batches(batch_id) ON DELETE RESTRICT,
    candidate_id        TEXT REFERENCES git_memory_review_candidates(candidate_id) ON DELETE RESTRICT,
    candidate_version   INTEGER NOT NULL DEFAULT 0 CHECK(candidate_version >= 0),
    content_digest      TEXT,
    status              TEXT NOT NULL CHECK(status IN ('staged','edited','rejected','presented','approved','publishing','published_uncommitted','committed','indexed','failed')),
    created_at          TEXT NOT NULL,
    CHECK(content_digest IS NULL OR (length(content_digest)=64 AND content_digest NOT GLOB '*[^a-f0-9]*'))
);
CREATE TRIGGER git_memory_receipts_immutable
BEFORE UPDATE ON git_memory_receipts
BEGIN SELECT RAISE(ABORT, 'git memory receipts are immutable'); END;
CREATE TRIGGER git_memory_receipts_no_delete
BEFORE DELETE ON git_memory_receipts
BEGIN SELECT RAISE(ABORT, 'git memory receipts are append-only'); END;

CREATE TABLE git_memory_audit (
    audit_id            TEXT PRIMARY KEY,
    batch_id            TEXT REFERENCES git_memory_review_batches(batch_id) ON DELETE RESTRICT,
    candidate_id        TEXT REFERENCES git_memory_review_candidates(candidate_id) ON DELETE RESTRICT,
    action              TEXT NOT NULL CHECK(action IN ('stage','edit','reject','present','approve','publish','index')),
    outcome             TEXT NOT NULL CHECK(outcome IN ('accepted','rejected','failed')),
    reason_code         TEXT CHECK(reason_code IS NULL OR reason_code IN ('user_rejected','user_canceled','unsafe_payload','stale_source','version_conflict','authority_mismatch','internal_error')),
    created_at          TEXT NOT NULL
);
CREATE TRIGGER git_memory_audit_immutable
BEFORE UPDATE ON git_memory_audit
BEGIN SELECT RAISE(ABORT, 'git memory audit is immutable'); END;
CREATE TRIGGER git_memory_audit_no_delete
BEFORE DELETE ON git_memory_audit
BEGIN SELECT RAISE(ABORT, 'git memory audit is append-only'); END;

UPDATE store_identity
   SET min_reader_version = 48,
       min_writer_version = 48
 WHERE singleton = 1;
