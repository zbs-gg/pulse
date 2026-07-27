-- Personal/Desk-only historical ingest authority. These tables stage reviewed
-- structured material and receipts; canonical memory remains in the existing
-- event/entity/assertion/continuity stores.

CREATE TABLE historical_ingest_jobs (
    job_id                   TEXT PRIMARY KEY CHECK(
                                 length(job_id) BETWEEN 20 AND 68
                                 AND substr(job_id, 1, 4) = 'job_'
                                 AND substr(job_id, 5) NOT GLOB '*[^a-f0-9]*'
                             ),
    store_id                 TEXT NOT NULL REFERENCES store_identity(store_id) ON DELETE RESTRICT,
    state                    TEXT NOT NULL CHECK(state IN (
                                 'preflight', 'snapshotting', 'awaiting_egress_consent',
                                 'extracting', 'paused_quota', 'extraction_failed',
                                 'manifest_ready', 'nothing_to_import', 'approval_ready',
                                 'approved', 'stale', 'applying', 'committed_indexing',
                                 'indexing_failed', 'retrieval_ready', 'canceled'
                             )),
    root_limit               INTEGER NOT NULL CHECK(root_limit = 50),
    cutoff_at                TEXT NOT NULL CHECK(length(cutoff_at) BETWEEN 20 AND 40),
    source_snapshot_digest   TEXT CHECK(
                                 source_snapshot_digest IS NULL
                                 OR (length(source_snapshot_digest) = 64
                                     AND source_snapshot_digest NOT GLOB '*[^a-f0-9]*')
                             ),
    parser_version           TEXT NOT NULL CHECK(length(parser_version) BETWEEN 1 AND 128),
    scrubber_version         TEXT NOT NULL CHECK(length(scrubber_version) BETWEEN 1 AND 128),
    prompt_version           TEXT NOT NULL CHECK(length(prompt_version) BETWEEN 1 AND 128),
    schema_digest            TEXT NOT NULL CHECK(
                                 length(schema_digest) = 64
                                 AND schema_digest NOT GLOB '*[^a-f0-9]*'
                             ),
    model_id                 TEXT NOT NULL CHECK(model_id = 'gpt-5.6-luna'),
    model_effort             TEXT NOT NULL CHECK(model_effort = 'low'),
    current_revision         INTEGER NOT NULL DEFAULT 0 CHECK(current_revision >= 0),
    current_manifest_digest  TEXT CHECK(
                                 current_manifest_digest IS NULL
                                 OR (length(current_manifest_digest) = 64
                                     AND current_manifest_digest NOT GLOB '*[^a-f0-9]*')
                             ),
    created_at               TEXT NOT NULL CHECK(length(created_at) BETWEEN 20 AND 40),
    updated_at               TEXT NOT NULL CHECK(length(updated_at) BETWEEN 20 AND 40),
    canceled_at              TEXT CHECK(canceled_at IS NULL OR length(canceled_at) BETWEEN 20 AND 40)
) WITHOUT ROWID;

CREATE TABLE historical_ingest_source_prefixes (
    job_id           TEXT NOT NULL REFERENCES historical_ingest_jobs(job_id) ON DELETE RESTRICT,
    source_alias     TEXT NOT NULL CHECK(
                         length(source_alias) BETWEEN 23 AND 71
                         AND substr(source_alias, 1, 7) = 'source_'
                         AND substr(source_alias, 8) NOT GLOB '*[^a-f0-9]*'
                     ),
    root_id          TEXT NOT NULL CHECK(length(root_id) BETWEEN 1 AND 256),
    captured_bytes   INTEGER NOT NULL CHECK(captured_bytes >= 0),
    prefix_digest    TEXT NOT NULL CHECK(
                         length(prefix_digest) = 64
                         AND prefix_digest NOT GLOB '*[^a-f0-9]*'
                     ),
    parser_version   TEXT NOT NULL CHECK(length(parser_version) BETWEEN 1 AND 128),
    record_count     INTEGER NOT NULL CHECK(record_count >= 0),
    included_count   INTEGER NOT NULL CHECK(included_count >= 0),
    excluded_count   INTEGER NOT NULL CHECK(excluded_count >= 0),
    blocking_count   INTEGER NOT NULL CHECK(blocking_count >= 0),
    captured_at      TEXT NOT NULL CHECK(length(captured_at) BETWEEN 20 AND 40),
    PRIMARY KEY(job_id, source_alias),
    CHECK(record_count = included_count + excluded_count + blocking_count)
) WITHOUT ROWID;

CREATE TABLE historical_ingest_work_units (
    unit_id               TEXT PRIMARY KEY CHECK(length(unit_id) BETWEEN 1 AND 256),
    job_id                TEXT NOT NULL REFERENCES historical_ingest_jobs(job_id) ON DELETE RESTRICT,
    root_id               TEXT NOT NULL CHECK(length(root_id) BETWEEN 1 AND 256),
    ordinal               INTEGER NOT NULL CHECK(ordinal >= 0),
    evidence_digest       TEXT NOT NULL CHECK(
                              length(evidence_digest) = 64
                              AND evidence_digest NOT GLOB '*[^a-f0-9]*'
                          ),
    state                 TEXT NOT NULL CHECK(state IN ('pending', 'leased', 'accepted', 'failed')),
    lease_id_hash         TEXT CHECK(
                              lease_id_hash IS NULL
                              OR (length(lease_id_hash) = 64
                                  AND lease_id_hash NOT GLOB '*[^a-f0-9]*')
                          ),
    lease_expires_at      TEXT CHECK(lease_expires_at IS NULL OR length(lease_expires_at) BETWEEN 20 AND 40),
    accepted_result_digest TEXT CHECK(
                              accepted_result_digest IS NULL
                              OR (length(accepted_result_digest) = 64
                                  AND accepted_result_digest NOT GLOB '*[^a-f0-9]*')
                          ),
    input_tokens          INTEGER CHECK(input_tokens IS NULL OR input_tokens >= 0),
    cached_input_tokens   INTEGER CHECK(cached_input_tokens IS NULL OR cached_input_tokens >= 0),
    output_tokens         INTEGER CHECK(output_tokens IS NULL OR output_tokens >= 0),
    reasoning_tokens      INTEGER CHECK(reasoning_tokens IS NULL OR reasoning_tokens >= 0),
    created_at            TEXT NOT NULL CHECK(length(created_at) BETWEEN 20 AND 40),
    updated_at            TEXT NOT NULL CHECK(length(updated_at) BETWEEN 20 AND 40),
    UNIQUE(job_id, root_id, ordinal),
    UNIQUE(job_id, evidence_digest)
) WITHOUT ROWID;

CREATE TABLE historical_ingest_accepted_results (
    result_digest   TEXT PRIMARY KEY CHECK(
                        length(result_digest) = 64
                        AND result_digest NOT GLOB '*[^a-f0-9]*'
                    ),
    job_id          TEXT NOT NULL REFERENCES historical_ingest_jobs(job_id) ON DELETE RESTRICT,
    unit_id         TEXT NOT NULL REFERENCES historical_ingest_work_units(unit_id) ON DELETE RESTRICT,
    schema_digest   TEXT NOT NULL CHECK(
                        length(schema_digest) = 64
                        AND schema_digest NOT GLOB '*[^a-f0-9]*'
                    ),
    structured_json BLOB NOT NULL CHECK(length(structured_json) BETWEEN 2 AND 4194304),
    accepted_at     TEXT NOT NULL CHECK(length(accepted_at) BETWEEN 20 AND 40),
    UNIQUE(job_id, unit_id)
) WITHOUT ROWID;

CREATE TABLE historical_ingest_manifests (
    job_id                 TEXT NOT NULL REFERENCES historical_ingest_jobs(job_id) ON DELETE RESTRICT,
    revision               INTEGER NOT NULL CHECK(revision >= 1),
    manifest_digest        TEXT NOT NULL UNIQUE CHECK(
                               length(manifest_digest) = 64
                               AND manifest_digest NOT GLOB '*[^a-f0-9]*'
                           ),
    source_snapshot_digest TEXT NOT NULL CHECK(
                               length(source_snapshot_digest) = 64
                               AND source_snapshot_digest NOT GLOB '*[^a-f0-9]*'
                           ),
    schema_digest          TEXT NOT NULL CHECK(
                               length(schema_digest) = 64
                               AND schema_digest NOT GLOB '*[^a-f0-9]*'
                           ),
    state                  TEXT NOT NULL CHECK(state IN (
                               'draft', 'review_complete', 'approval_ready', 'superseded'
                           )),
    item_count             INTEGER NOT NULL CHECK(item_count >= 0),
    manifest_json          BLOB NOT NULL CHECK(length(manifest_json) BETWEEN 2 AND 67108864),
    created_at             TEXT NOT NULL CHECK(length(created_at) BETWEEN 20 AND 40),
    PRIMARY KEY(job_id, revision)
) WITHOUT ROWID;

CREATE TABLE historical_ingest_manifest_items (
    job_id             TEXT NOT NULL,
    revision           INTEGER NOT NULL,
    candidate_id       TEXT NOT NULL CHECK(
                           length(candidate_id) BETWEEN 29 AND 74
                           AND substr(candidate_id, 1, 10) = 'candidate_'
                           AND substr(candidate_id, 11) NOT GLOB '*[^a-f0-9]*'
                       ),
    material_kind      TEXT NOT NULL CHECK(material_kind IN (
                           'event', 'decision', 'assertion', 'person', 'project',
                           'relation', 'state', 'continuity'
                       )),
    scope_kind         TEXT NOT NULL CHECK(scope_kind IN ('project', 'global', 'unassigned')),
    project_id         TEXT,
    epistemic_status   TEXT NOT NULL CHECK(epistemic_status IN ('explicit', 'hypothesis', 'conflict')),
    derivation         TEXT NOT NULL CHECK(derivation IN ('direct', 'inferred')),
    valid_from         TEXT NOT NULL CHECK(length(valid_from) BETWEEN 20 AND 40),
    valid_to           TEXT CHECK(valid_to IS NULL OR length(valid_to) BETWEEN 20 AND 40),
    confidence         REAL NOT NULL CHECK(confidence BETWEEN 0 AND 1),
    privacy            TEXT NOT NULL CHECK(privacy = 'private'),
    item_digest        TEXT NOT NULL CHECK(
                           length(item_digest) = 64
                           AND item_digest NOT GLOB '*[^a-f0-9]*'
                       ),
    item_json          BLOB NOT NULL CHECK(length(item_json) BETWEEN 2 AND 1048576),
    disposition        TEXT NOT NULL CHECK(disposition IN (
                           'pending', 'confirmed', 'rejected', 'corrected', 'excluded'
                       )),
    PRIMARY KEY(job_id, revision, candidate_id),
    FOREIGN KEY(job_id, revision) REFERENCES historical_ingest_manifests(job_id, revision) ON DELETE RESTRICT,
    CHECK((scope_kind = 'project' AND project_id IS NOT NULL) OR
          (scope_kind != 'project' AND project_id IS NULL)),
    CHECK(derivation != 'inferred' OR epistemic_status != 'explicit')
) WITHOUT ROWID;

CREATE TABLE historical_ingest_write_sets (
    job_id                    TEXT NOT NULL,
    revision                  INTEGER NOT NULL,
    manifest_digest           TEXT NOT NULL,
    write_set_digest          TEXT NOT NULL UNIQUE CHECK(
                                  length(write_set_digest) = 64
                                  AND write_set_digest NOT GLOB '*[^a-f0-9]*'
                              ),
    destination_store_id      TEXT NOT NULL REFERENCES store_identity(store_id) ON DELETE RESTRICT,
    destination_generation    INTEGER NOT NULL CHECK(destination_generation >= 0),
    materializer_version      TEXT NOT NULL CHECK(length(materializer_version) BETWEEN 1 AND 128),
    dedup_version             TEXT NOT NULL CHECK(length(dedup_version) BETWEEN 1 AND 128),
    target_versions_digest    TEXT NOT NULL CHECK(
                                  length(target_versions_digest) = 64
                                  AND target_versions_digest NOT GLOB '*[^a-f0-9]*'
                              ),
    write_set_json            BLOB NOT NULL CHECK(length(write_set_json) BETWEEN 2 AND 67108864),
    created_at                TEXT NOT NULL CHECK(length(created_at) BETWEEN 20 AND 40),
    PRIMARY KEY(job_id, revision),
    FOREIGN KEY(job_id, revision) REFERENCES historical_ingest_manifests(job_id, revision) ON DELETE RESTRICT
) WITHOUT ROWID;

CREATE TABLE historical_ingest_authorizations (
    authorization_id       TEXT PRIMARY KEY CHECK(length(authorization_id) BETWEEN 1 AND 256),
    audit_id               TEXT NOT NULL UNIQUE CHECK(length(audit_id) BETWEEN 1 AND 256),
    job_id                 TEXT NOT NULL REFERENCES historical_ingest_jobs(job_id) ON DELETE RESTRICT,
    authorization_kind     TEXT NOT NULL CHECK(authorization_kind IN ('provider_egress', 'canonical_apply')),
    bound_digest           TEXT NOT NULL CHECK(
                               length(bound_digest) = 64
                               AND bound_digest NOT GLOB '*[^a-f0-9]*'
                           ),
    capability_hash        TEXT NOT NULL UNIQUE CHECK(
                               length(capability_hash) = 64
                               AND capability_hash NOT GLOB '*[^a-f0-9]*'
                           ),
    destination_generation INTEGER CHECK(destination_generation IS NULL OR destination_generation >= 0),
    expires_at             TEXT NOT NULL CHECK(length(expires_at) BETWEEN 20 AND 40),
    consumed_at            TEXT CHECK(consumed_at IS NULL OR length(consumed_at) BETWEEN 20 AND 40),
    created_at             TEXT NOT NULL CHECK(length(created_at) BETWEEN 20 AND 40),
    CHECK((authorization_kind = 'provider_egress' AND destination_generation IS NULL) OR
          (authorization_kind = 'canonical_apply' AND destination_generation IS NOT NULL))
) WITHOUT ROWID;

CREATE TABLE historical_ingest_batch_receipts (
    receipt_id               TEXT PRIMARY KEY CHECK(length(receipt_id) BETWEEN 1 AND 256),
    job_id                   TEXT NOT NULL REFERENCES historical_ingest_jobs(job_id) ON DELETE RESTRICT,
    manifest_digest          TEXT NOT NULL,
    write_set_digest         TEXT NOT NULL,
    destination_store_id     TEXT NOT NULL REFERENCES store_identity(store_id) ON DELETE RESTRICT,
    destination_generation   INTEGER NOT NULL CHECK(destination_generation >= 0),
    created_count            INTEGER NOT NULL CHECK(created_count >= 0),
    deduplicated_count       INTEGER NOT NULL CHECK(deduplicated_count >= 0),
    committed_at             TEXT NOT NULL CHECK(length(committed_at) BETWEEN 20 AND 40),
    UNIQUE(job_id, manifest_digest, write_set_digest)
) WITHOUT ROWID;

CREATE TABLE historical_ingest_item_receipts (
    receipt_id       TEXT NOT NULL REFERENCES historical_ingest_batch_receipts(receipt_id) ON DELETE RESTRICT,
    candidate_id     TEXT NOT NULL,
    outcome          TEXT NOT NULL CHECK(outcome IN ('created', 'deduplicated')),
    object_kind      TEXT NOT NULL CHECK(length(object_kind) BETWEEN 1 AND 128),
    object_id        TEXT NOT NULL CHECK(length(object_id) BETWEEN 1 AND 256),
    object_digest    TEXT NOT NULL CHECK(
                         length(object_digest) = 64
                         AND object_digest NOT GLOB '*[^a-f0-9]*'
                     ),
    PRIMARY KEY(receipt_id, candidate_id),
    UNIQUE(receipt_id, object_kind, object_id, candidate_id)
) WITHOUT ROWID;

CREATE TABLE historical_ingest_object_metadata (
    store_id          TEXT NOT NULL REFERENCES store_identity(store_id) ON DELETE RESTRICT,
    object_kind       TEXT NOT NULL CHECK(length(object_kind) BETWEEN 1 AND 128),
    object_id         TEXT NOT NULL CHECK(length(object_id) BETWEEN 1 AND 256),
    scope_kind        TEXT NOT NULL CHECK(scope_kind IN ('project', 'global', 'unassigned')),
    project_id        TEXT,
    epistemic_status  TEXT NOT NULL CHECK(epistemic_status IN ('explicit', 'hypothesis', 'conflict')),
    valid_from        TEXT NOT NULL CHECK(length(valid_from) BETWEEN 20 AND 40),
    valid_to          TEXT CHECK(valid_to IS NULL OR length(valid_to) BETWEEN 20 AND 40),
    manifest_digest   TEXT NOT NULL,
    candidate_id      TEXT NOT NULL,
    metadata_digest   TEXT NOT NULL CHECK(
                          length(metadata_digest) = 64
                          AND metadata_digest NOT GLOB '*[^a-f0-9]*'
                      ),
    PRIMARY KEY(store_id, object_kind, object_id),
    CHECK((scope_kind = 'project' AND project_id IS NOT NULL) OR
          (scope_kind != 'project' AND project_id IS NULL))
) WITHOUT ROWID;

CREATE TABLE historical_ingest_source_refs (
    store_id         TEXT NOT NULL,
    object_kind      TEXT NOT NULL,
    object_id        TEXT NOT NULL,
    ordinal          INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 255),
    source_alias     TEXT NOT NULL,
    prefix_digest    TEXT NOT NULL CHECK(
                         length(prefix_digest) = 64
                         AND prefix_digest NOT GLOB '*[^a-f0-9]*'
                     ),
    record_locator   TEXT NOT NULL CHECK(length(record_locator) BETWEEN 1 AND 256),
    PRIMARY KEY(store_id, object_kind, object_id, ordinal),
    FOREIGN KEY(store_id, object_kind, object_id)
        REFERENCES historical_ingest_object_metadata(store_id, object_kind, object_id) ON DELETE RESTRICT
) WITHOUT ROWID;

CREATE TABLE historical_ingest_projection_outbox (
    outbox_id          INTEGER PRIMARY KEY AUTOINCREMENT,
    receipt_id         TEXT NOT NULL REFERENCES historical_ingest_batch_receipts(receipt_id) ON DELETE RESTRICT,
    projection_kind    TEXT NOT NULL CHECK(projection_kind IN ('retrieval', 'material_graph', 'continuity')),
    generation         INTEGER NOT NULL CHECK(generation >= 1),
    state              TEXT NOT NULL CHECK(state IN ('pending', 'leased', 'ready', 'failed')),
    attempt_count      INTEGER NOT NULL DEFAULT 0 CHECK(attempt_count >= 0),
    lease_token_hash   TEXT CHECK(
                           lease_token_hash IS NULL
                           OR (length(lease_token_hash) = 64
                               AND lease_token_hash NOT GLOB '*[^a-f0-9]*')
                       ),
    lease_expires_at   TEXT CHECK(lease_expires_at IS NULL OR length(lease_expires_at) BETWEEN 20 AND 40),
    last_error_code    TEXT CHECK(last_error_code IS NULL OR length(last_error_code) BETWEEN 1 AND 128),
    created_at         TEXT NOT NULL CHECK(length(created_at) BETWEEN 20 AND 40),
    updated_at         TEXT NOT NULL CHECK(length(updated_at) BETWEEN 20 AND 40),
    UNIQUE(receipt_id, projection_kind, generation)
);

CREATE INDEX idx_historical_ingest_jobs_state
    ON historical_ingest_jobs(store_id, state, updated_at DESC);
CREATE INDEX idx_historical_ingest_work_pending
    ON historical_ingest_work_units(job_id, state, ordinal);
CREATE INDEX idx_historical_ingest_review_queue
    ON historical_ingest_manifest_items(job_id, revision, disposition, scope_kind, material_kind);
CREATE INDEX idx_historical_ingest_projection_pending
    ON historical_ingest_projection_outbox(state, updated_at, outbox_id);

CREATE TRIGGER historical_ingest_accepted_results_no_update
BEFORE UPDATE ON historical_ingest_accepted_results
BEGIN SELECT RAISE(ABORT, 'historical ingest accepted results are immutable'); END;

CREATE TRIGGER historical_ingest_accepted_results_no_delete
BEFORE DELETE ON historical_ingest_accepted_results
BEGIN SELECT RAISE(ABORT, 'historical ingest accepted results are immutable'); END;

CREATE TRIGGER historical_ingest_manifest_bytes_no_update
BEFORE UPDATE OF manifest_digest, source_snapshot_digest, schema_digest, item_count, manifest_json
ON historical_ingest_manifests
BEGIN SELECT RAISE(ABORT, 'historical ingest manifest bytes are immutable'); END;

CREATE TRIGGER historical_ingest_write_sets_no_update
BEFORE UPDATE ON historical_ingest_write_sets
BEGIN SELECT RAISE(ABORT, 'historical ingest write sets are immutable'); END;

CREATE TRIGGER historical_ingest_receipts_no_update
BEFORE UPDATE ON historical_ingest_batch_receipts
BEGIN SELECT RAISE(ABORT, 'historical ingest receipts are immutable'); END;

CREATE TRIGGER historical_ingest_item_receipts_no_update
BEFORE UPDATE ON historical_ingest_item_receipts
BEGIN SELECT RAISE(ABORT, 'historical ingest item receipts are immutable'); END;

UPDATE store_identity
   SET min_reader_version = 55,
       min_writer_version = 55
 WHERE singleton = 1
   AND store_kind IN ('personal', 'desk');
