-- Explicit Personal memory eligibility. Personal and Desk stores keep one
-- canonical object head per logical memory. Project scope is the safe default;
-- Personal Global is opt-in and is not enabled by this migration alone.

ALTER TABLE private_memory_objects
    ADD COLUMN logical_memory_id TEXT;
ALTER TABLE private_memory_objects
    ADD COLUMN logical_generation INTEGER NOT NULL DEFAULT 1
        CHECK(logical_generation >= 1);
ALTER TABLE private_memory_objects
    ADD COLUMN project_namespace_id TEXT NOT NULL DEFAULT '';
ALTER TABLE private_memory_objects
    ADD COLUMN original_repository_id TEXT NOT NULL DEFAULT '';
ALTER TABLE private_memory_objects
    ADD COLUMN memory_scope TEXT NOT NULL DEFAULT 'project'
        CHECK(memory_scope IN ('project','personal_global'));
ALTER TABLE private_memory_objects
    ADD COLUMN modified_at TEXT NOT NULL DEFAULT '';
ALTER TABLE private_memory_objects
    ADD COLUMN capture_host TEXT NOT NULL DEFAULT '';
ALTER TABLE private_memory_objects
    ADD COLUMN capture_session_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE private_memory_objects
    ADD COLUMN captured_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memory_tray_candidates
    ADD COLUMN target_logical_generation INTEGER
        CHECK(target_logical_generation IS NULL OR target_logical_generation >= 1);

UPDATE private_memory_objects
   SET logical_memory_id = object_id,
       modified_at = COALESCE(NULLIF(modified_at, ''), created_at),
       capture_host = COALESCE(
           NULLIF(capture_host, ''),
           (SELECT receipt.provenance_host
              FROM memory_write_receipts receipt
             WHERE receipt.object_id=private_memory_objects.object_id
               AND receipt.status IN ('created','deduplicated')
             ORDER BY receipt.rowid ASC LIMIT 1),
           (SELECT ledger.host
              FROM memory_tray_candidates candidate
              JOIN turn_ledgers ledger ON ledger.ledger_id=candidate.ledger_id
             WHERE candidate.candidate_id=
                   private_memory_objects.created_from_candidate_id),
           ''
       ),
       capture_session_ref = COALESCE(
           NULLIF(capture_session_ref, ''),
           (SELECT receipt.provenance_session_id
              FROM memory_write_receipts receipt
             WHERE receipt.object_id=private_memory_objects.object_id
               AND receipt.status IN ('created','deduplicated')
             ORDER BY receipt.rowid ASC LIMIT 1),
           ''
       ),
       captured_at = COALESCE(
           NULLIF(captured_at, ''),
           (SELECT receipt.created_at
              FROM memory_write_receipts receipt
             WHERE receipt.object_id=private_memory_objects.object_id
               AND receipt.status IN ('created','deduplicated')
             ORDER BY receipt.rowid ASC LIMIT 1),
           created_at
       )
 WHERE logical_memory_id IS NULL;

DROP INDEX idx_private_memory_objects_active_digest;
CREATE UNIQUE INDEX idx_private_memory_objects_active_scope_digest
    ON private_memory_objects(
        memory_scope,
        CASE
            WHEN memory_scope='personal_global' THEN ''
            ELSE project_namespace_id
        END,
        candidate_kind,
        content_digest
    )
 WHERE lifecycle='active';
CREATE UNIQUE INDEX idx_private_memory_objects_active_logical_head
    ON private_memory_objects(logical_memory_id)
 WHERE lifecycle='active';
CREATE INDEX idx_private_memory_objects_scope_feed
    ON private_memory_objects(
        memory_scope, project_namespace_id, lifecycle, modified_at DESC, object_id
    );

-- Git-backed project memories share the same local retrieval corpus. Record the
-- exact content-free checkout authority that performed the index so a one-vault
-- reader can select only the current repository before ranking.
ALTER TABLE git_memory_shared_projection
    ADD COLUMN repository_id TEXT NOT NULL DEFAULT '';
ALTER TABLE git_memory_shared_projection
    ADD COLUMN binding_digest TEXT NOT NULL DEFAULT '';
UPDATE git_memory_shared_projection
   SET repository_id=COALESCE(
           (SELECT project.repository_id
              FROM git_memory_projects project
             WHERE project.portable_project_id=
                   git_memory_shared_projection.portable_project_id),
           repository_id
       ),
       binding_digest=COALESCE(
           (SELECT project.binding_digest
              FROM git_memory_projects project
             WHERE project.portable_project_id=
                   git_memory_shared_projection.portable_project_id),
           binding_digest
       )
 WHERE repository_id='' OR binding_digest='';
CREATE INDEX idx_git_memory_shared_projection_boundary
    ON git_memory_shared_projection(
        repository_id, binding_digest, status, event_id
    );

CREATE TABLE personal_memory_scope_state (
    singleton             INTEGER PRIMARY KEY CHECK(singleton = 1),
    eligibility_revision  INTEGER NOT NULL CHECK(eligibility_revision >= 1),
    updated_at            TEXT NOT NULL
);
INSERT INTO personal_memory_scope_state(singleton, eligibility_revision, updated_at)
VALUES (
    1,
    1,
    COALESCE(
        (SELECT MAX(modified_at) FROM private_memory_objects),
        '1970-01-01T00:00:00Z'
    )
);

UPDATE store_identity
   SET min_reader_version = 53,
       min_writer_version = 53
 WHERE singleton = 1;
