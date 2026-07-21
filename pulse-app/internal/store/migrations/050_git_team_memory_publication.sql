-- Exact approved Git publication metadata. Personal and Desk stores only.

ALTER TABLE git_memory_hook_presentations
    ADD COLUMN approver_label_digest TEXT NOT NULL DEFAULT '0000000000000000000000000000000000000000000000000000000000000000'
    CHECK(length(approver_label_digest)=64 AND approver_label_digest NOT GLOB '*[^a-f0-9]*');

ALTER TABLE git_memory_approval_leases
    ADD COLUMN approver_label_digest TEXT NOT NULL DEFAULT '0000000000000000000000000000000000000000000000000000000000000000'
    CHECK(length(approver_label_digest)=64 AND approver_label_digest NOT GLOB '*[^a-f0-9]*');

ALTER TABLE git_memory_publications ADD COLUMN approval_lease_id TEXT REFERENCES git_memory_approval_leases(lease_id);
ALTER TABLE git_memory_publications ADD COLUMN approver_label TEXT;
ALTER TABLE git_memory_publications ADD COLUMN authority_digest TEXT;

CREATE UNIQUE INDEX idx_git_memory_publications_lease
    ON git_memory_publications(approval_lease_id) WHERE approval_lease_id IS NOT NULL;

CREATE TABLE git_memory_publication_files (
    publication_id TEXT NOT NULL REFERENCES git_memory_publications(publication_id) ON DELETE RESTRICT,
    ordinal        INTEGER NOT NULL CHECK(ordinal >= 0),
    memory_id      TEXT,
    path           TEXT NOT NULL,
    content_sha256 TEXT NOT NULL,
    byte_count     INTEGER NOT NULL CHECK(byte_count >= 0),
    PRIMARY KEY(publication_id, ordinal),
    UNIQUE(publication_id, path),
    CHECK(length(content_sha256)=64 AND content_sha256 NOT GLOB '*[^a-f0-9]*')
);

CREATE TRIGGER git_memory_publication_files_immutable
BEFORE UPDATE ON git_memory_publication_files
BEGIN SELECT RAISE(ABORT, 'git memory publication files are immutable'); END;
CREATE TRIGGER git_memory_publication_files_no_delete
BEFORE DELETE ON git_memory_publication_files
BEGIN SELECT RAISE(ABORT, 'git memory publication files are append-only'); END;

CREATE TRIGGER git_memory_hook_approver_digest_immutable
BEFORE UPDATE OF approver_label_digest ON git_memory_hook_presentations
BEGIN SELECT RAISE(ABORT, 'git memory approver label digest is immutable'); END;
CREATE TRIGGER git_memory_lease_approver_digest_immutable
BEFORE UPDATE OF approver_label_digest ON git_memory_approval_leases
BEGIN SELECT RAISE(ABORT, 'git memory lease approver label digest is immutable'); END;

UPDATE store_identity
   SET min_reader_version = 50,
       min_writer_version = 50
 WHERE singleton = 1;
