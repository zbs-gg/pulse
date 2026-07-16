-- Committed Git Team Memory projection into the existing local retrieval corpus.
-- The current projection is mutable by a new trusted scan; version rows preserve
-- prior approved objects so a correction can remove stale retrieval without
-- destroying local history.

CREATE TABLE git_memory_shared_versions (
    portable_project_id TEXT NOT NULL,
    memory_id           TEXT NOT NULL,
    version             INTEGER NOT NULL CHECK(version >= 1),
    candidate_digest    TEXT NOT NULL,
    status              TEXT NOT NULL CHECK(status IN ('active','superseded','removed')),
    kind                TEXT NOT NULL,
    content             TEXT NOT NULL,
    confidence          REAL NOT NULL CHECK(confidence >= 0 AND confidence <= 1),
    approver_label      TEXT NOT NULL,
    approved_at         TEXT NOT NULL,
    authority_digest    TEXT NOT NULL,
    source_refs_json    TEXT NOT NULL,
    warnings_json       TEXT NOT NULL,
    file_path           TEXT NOT NULL,
    content_sha256      TEXT NOT NULL,
    publication_id      TEXT NOT NULL,
    publication_path    TEXT NOT NULL,
    object_commit_hash  TEXT NOT NULL,
    manifest_commit_hash TEXT NOT NULL,
    indexed_at          TEXT NOT NULL,
    PRIMARY KEY(portable_project_id, memory_id, version, candidate_digest),
    CHECK(length(candidate_digest)=64 AND candidate_digest NOT GLOB '*[^a-f0-9]*'),
    CHECK(length(authority_digest)=64 AND authority_digest NOT GLOB '*[^a-f0-9]*'),
    CHECK(length(content_sha256)=64 AND content_sha256 NOT GLOB '*[^a-f0-9]*')
);

CREATE TABLE git_memory_shared_projection (
    portable_project_id TEXT NOT NULL,
    memory_id           TEXT NOT NULL,
    version             INTEGER NOT NULL CHECK(version >= 1),
    candidate_digest    TEXT NOT NULL,
    status              TEXT NOT NULL CHECK(status IN ('active','superseded','removed')),
    kind                TEXT NOT NULL,
    content             TEXT NOT NULL,
    confidence          REAL NOT NULL CHECK(confidence >= 0 AND confidence <= 1),
    approver_label      TEXT NOT NULL,
    approved_at         TEXT NOT NULL,
    authority_digest    TEXT NOT NULL,
    source_refs_json    TEXT NOT NULL,
    warnings_json       TEXT NOT NULL,
    file_path           TEXT NOT NULL,
    content_sha256      TEXT NOT NULL,
    publication_id      TEXT NOT NULL,
    publication_path    TEXT NOT NULL,
    object_commit_hash  TEXT NOT NULL,
    manifest_commit_hash TEXT NOT NULL,
    event_id            INTEGER UNIQUE REFERENCES events(id) ON DELETE SET NULL,
    indexed_at          TEXT NOT NULL,
    PRIMARY KEY(portable_project_id, memory_id),
    CHECK(length(candidate_digest)=64 AND candidate_digest NOT GLOB '*[^a-f0-9]*'),
    CHECK(length(authority_digest)=64 AND authority_digest NOT GLOB '*[^a-f0-9]*'),
    CHECK(length(content_sha256)=64 AND content_sha256 NOT GLOB '*[^a-f0-9]*'),
    CHECK((status='active' AND event_id IS NOT NULL) OR (status!='active' AND event_id IS NULL))
);

CREATE INDEX idx_git_memory_shared_projection_event
    ON git_memory_shared_projection(event_id) WHERE event_id IS NOT NULL;
CREATE INDEX idx_git_memory_shared_projection_status
    ON git_memory_shared_projection(portable_project_id, status);

UPDATE store_identity
   SET min_reader_version = 51,
       min_writer_version = 51
 WHERE singleton = 1;
