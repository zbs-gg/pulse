-- Admit Cursor as a truthful continuity delivery host while preserving the
-- immutable v46 receipt rows and keeping provider measurement sources closed
-- to the two hosts that expose trustworthy provider usage evidence.

PRAGMA defer_foreign_keys = ON;

-- These guards compile their SELECT against the parent table. Recreate them
-- around the bounded table swap so the schema is never left with a stale
-- trigger program.
DROP TRIGGER continuity_delivery_object_refs_insert_guard;
DROP TRIGGER continuity_delivery_evidence_refs_insert_guard;

CREATE TABLE continuity_delivery_receipts_v52 (
    receipt_seq                  INTEGER PRIMARY KEY AUTOINCREMENT,
    receipt_id                   TEXT NOT NULL UNIQUE CHECK(
                                     length(receipt_id) BETWEEN 1 AND 256
                                     AND substr(receipt_id, 1, 1) GLOB '[A-Za-z0-9]'
                                     AND receipt_id NOT GLOB '*[^A-Za-z0-9._:-]*'
                                 ),
    context_id                   TEXT NOT NULL CHECK(
                                     length(context_id) BETWEEN 1 AND 256
                                     AND substr(context_id, 1, 1) GLOB '[A-Za-z0-9]'
                                     AND context_id NOT GLOB '*[^A-Za-z0-9._:-]*'
                                 ),
    parent_receipt_id            TEXT REFERENCES continuity_delivery_receipts_v52(receipt_id) ON DELETE RESTRICT,
    receipt_state                TEXT NOT NULL CHECK(receipt_state IN (
                                     'offered_to_host', 'host_observed', 'provider_measurement'
                                 )),
    purpose                      TEXT NOT NULL CHECK(purpose IN ('session_start', 'subagent_start')),
    store_id                     TEXT NOT NULL REFERENCES store_identity(store_id),
    repository_id                TEXT NOT NULL CHECK(
                                     length(repository_id) BETWEEN 1 AND 256
                                     AND substr(repository_id, 1, 1) GLOB '[A-Za-z0-9]'
                                     AND repository_id NOT GLOB '*[^A-Za-z0-9._:-]*'
                                 ),
    binding_digest               TEXT NOT NULL CHECK(
                                     length(binding_digest) = 64
                                     AND binding_digest NOT GLOB '*[^a-f0-9]*'
                                 ),
    host                         TEXT NOT NULL CHECK(host IN ('codex', 'claude-code', 'cursor')),
    session_ref                  TEXT NOT NULL CHECK(
                                     length(session_ref) = 72
                                     AND substr(session_ref, 1, 8) = 'session:'
                                     AND substr(session_ref, 9) NOT GLOB '*[^a-f0-9]*'
                                 ),
    payload_digest               TEXT NOT NULL CHECK(
                                     length(payload_digest) = 64
                                     AND payload_digest NOT GLOB '*[^a-f0-9]*'
                                 ),
    object_ref_count             INTEGER NOT NULL CHECK(object_ref_count BETWEEN 0 AND 64),
    evidence_ref_count           INTEGER NOT NULL CHECK(evidence_ref_count BETWEEN 0 AND 64),
    refs_manifest_digest         TEXT NOT NULL CHECK(
                                     length(refs_manifest_digest) = 64
                                     AND refs_manifest_digest NOT GLOB '*[^a-f0-9]*'
                                 ),
    method_id                    TEXT NOT NULL CHECK(method_id = 'utf8_bytes_div4_ceil'),
    method_version               TEXT NOT NULL CHECK(method_version = '1'),
    rendered_bytes               INTEGER NOT NULL CHECK(rendered_bytes BETWEEN 1 AND 1048576),
    pulse_tokens                 INTEGER NOT NULL CHECK(
                                     pulse_tokens BETWEEN 1 AND 1048576
                                     AND pulse_tokens = ((rendered_bytes + 3) / 4)
                                 ),
    baseline_kind                TEXT CHECK(baseline_kind IS NULL OR baseline_kind = 'canonical_structured_resume_v1'),
    source_equivalent_tokens     INTEGER CHECK(
                                     source_equivalent_tokens IS NULL
                                     OR source_equivalent_tokens BETWEEN 0 AND 10485760
                                 ),
    coverage_counted             INTEGER NOT NULL DEFAULT 0 CHECK(coverage_counted BETWEEN 0 AND 1000000),
    coverage_total               INTEGER NOT NULL DEFAULT 0 CHECK(coverage_total BETWEEN 0 AND 1000000),
    source_event_digest          TEXT CHECK(
                                     source_event_digest IS NULL
                                     OR (length(source_event_digest) = 64
                                         AND source_event_digest NOT GLOB '*[^a-f0-9]*')
                                 ),
    provider_actual_input_tokens INTEGER CHECK(
                                     provider_actual_input_tokens IS NULL
                                     OR provider_actual_input_tokens BETWEEN 0 AND 10485760
                                 ),
    provider_actual_source       TEXT CHECK(provider_actual_source IS NULL OR provider_actual_source IN (
                                     'codex_provider_usage_v1', 'claude_provider_usage_v1'
                                 )),
    provider_evidence_digest     TEXT CHECK(
                                     provider_evidence_digest IS NULL
                                     OR (length(provider_evidence_digest) = 64
                                         AND provider_evidence_digest NOT GLOB '*[^a-f0-9]*')
                                 ),
    idempotency_key_hash         TEXT NOT NULL CHECK(
                                     length(idempotency_key_hash) = 64
                                     AND idempotency_key_hash NOT GLOB '*[^a-f0-9]*'
                                 ),
    operation_digest             TEXT NOT NULL CHECK(
                                     length(operation_digest) = 64
                                     AND operation_digest NOT GLOB '*[^a-f0-9]*'
                                 ),
    created_at                   TEXT NOT NULL CHECK(length(created_at) BETWEEN 20 AND 40),
    UNIQUE(context_id, receipt_state),
    UNIQUE(receipt_state, idempotency_key_hash),
    CHECK(
        (baseline_kind IS NULL AND source_equivalent_tokens IS NULL
         AND coverage_counted = 0 AND coverage_total = 0)
        OR
        (baseline_kind = 'canonical_structured_resume_v1' AND source_equivalent_tokens IS NOT NULL
         AND coverage_total >= 1 AND coverage_counted BETWEEN 1 AND coverage_total)
    ),
    CHECK(
        (receipt_state = 'offered_to_host' AND parent_receipt_id IS NULL
         AND source_event_digest IS NOT NULL
         AND provider_actual_input_tokens IS NULL AND provider_actual_source IS NULL
         AND provider_evidence_digest IS NULL)
        OR
        (receipt_state = 'host_observed' AND parent_receipt_id IS NOT NULL
         AND source_event_digest IS NOT NULL
         AND provider_actual_input_tokens IS NULL AND provider_actual_source IS NULL
         AND provider_evidence_digest IS NULL)
        OR
        (receipt_state = 'provider_measurement' AND parent_receipt_id IS NOT NULL
         AND source_event_digest IS NULL
         AND provider_actual_input_tokens IS NOT NULL AND provider_actual_source IS NOT NULL
         AND provider_evidence_digest IS NOT NULL)
    ),
    CHECK(
        receipt_state != 'provider_measurement'
        OR (host = 'codex' AND provider_actual_source = 'codex_provider_usage_v1')
        OR (host = 'claude-code' AND provider_actual_source = 'claude_provider_usage_v1')
    )
);

INSERT INTO continuity_delivery_receipts_v52 (
    receipt_seq, receipt_id, context_id, parent_receipt_id, receipt_state, purpose,
    store_id, repository_id, binding_digest, host, session_ref, payload_digest,
    object_ref_count, evidence_ref_count, refs_manifest_digest, method_id, method_version,
    rendered_bytes, pulse_tokens, baseline_kind, source_equivalent_tokens,
    coverage_counted, coverage_total, source_event_digest, provider_actual_input_tokens,
    provider_actual_source, provider_evidence_digest, idempotency_key_hash,
    operation_digest, created_at
)
SELECT
    receipt_seq, receipt_id, context_id, parent_receipt_id, receipt_state, purpose,
    store_id, repository_id, binding_digest, host, session_ref, payload_digest,
    object_ref_count, evidence_ref_count, refs_manifest_digest, method_id, method_version,
    rendered_bytes, pulse_tokens, baseline_kind, source_equivalent_tokens,
    coverage_counted, coverage_total, source_event_digest, provider_actual_input_tokens,
    provider_actual_source, provider_evidence_digest, idempotency_key_hash,
    operation_digest, created_at
FROM continuity_delivery_receipts;

CREATE TABLE continuity_delivery_object_refs_v52 (
    receipt_id TEXT NOT NULL REFERENCES continuity_delivery_receipts_v52(receipt_id) ON DELETE RESTRICT,
    ordinal    INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 63),
    ref_id     TEXT NOT NULL CHECK(
                   length(ref_id) BETWEEN 1 AND 256
                   AND substr(ref_id, 1, 1) GLOB '[A-Za-z0-9]'
                   AND ref_id NOT GLOB '*[^A-Za-z0-9._:-]*'
               ),
    PRIMARY KEY(receipt_id, ordinal),
    UNIQUE(receipt_id, ref_id)
) WITHOUT ROWID;
INSERT INTO continuity_delivery_object_refs_v52(receipt_id, ordinal, ref_id)
SELECT receipt_id, ordinal, ref_id FROM continuity_delivery_object_refs;

CREATE TABLE continuity_delivery_evidence_refs_v52 (
    receipt_id TEXT NOT NULL REFERENCES continuity_delivery_receipts_v52(receipt_id) ON DELETE RESTRICT,
    ordinal    INTEGER NOT NULL CHECK(ordinal BETWEEN 0 AND 63),
    ref_id     TEXT NOT NULL CHECK(
                   length(ref_id) BETWEEN 1 AND 256
                   AND substr(ref_id, 1, 1) GLOB '[A-Za-z0-9]'
                   AND ref_id NOT GLOB '*[^A-Za-z0-9._:-]*'
               ),
    PRIMARY KEY(receipt_id, ordinal),
    UNIQUE(receipt_id, ref_id)
) WITHOUT ROWID;
INSERT INTO continuity_delivery_evidence_refs_v52(receipt_id, ordinal, ref_id)
SELECT receipt_id, ordinal, ref_id FROM continuity_delivery_evidence_refs;

CREATE TABLE continuity_delivery_ref_seals_v52 (
    receipt_id           TEXT PRIMARY KEY REFERENCES continuity_delivery_receipts_v52(receipt_id) ON DELETE RESTRICT,
    refs_manifest_digest TEXT NOT NULL CHECK(
                             length(refs_manifest_digest) = 64
                             AND refs_manifest_digest NOT GLOB '*[^a-f0-9]*'
                         )
) WITHOUT ROWID;
INSERT INTO continuity_delivery_ref_seals_v52(receipt_id, refs_manifest_digest)
SELECT receipt_id, refs_manifest_digest FROM continuity_delivery_ref_seals;

DROP TABLE continuity_delivery_ref_seals;
DROP TABLE continuity_delivery_object_refs;
DROP TABLE continuity_delivery_evidence_refs;
DROP TABLE continuity_delivery_receipts;
ALTER TABLE continuity_delivery_receipts_v52 RENAME TO continuity_delivery_receipts;
ALTER TABLE continuity_delivery_object_refs_v52 RENAME TO continuity_delivery_object_refs;
ALTER TABLE continuity_delivery_evidence_refs_v52 RENAME TO continuity_delivery_evidence_refs;
ALTER TABLE continuity_delivery_ref_seals_v52 RENAME TO continuity_delivery_ref_seals;

CREATE INDEX idx_continuity_delivery_latest
    ON continuity_delivery_receipts(repository_id, host, purpose, receipt_seq DESC);
CREATE INDEX idx_continuity_delivery_memory_home
    ON continuity_delivery_receipts(repository_id, binding_digest, context_id, receipt_seq DESC);
CREATE INDEX idx_continuity_delivery_memory_home_recent
    ON continuity_delivery_receipts(
        repository_id, binding_digest, purpose, receipt_state, receipt_seq DESC, context_id
    );

CREATE TRIGGER continuity_delivery_object_refs_insert_guard
BEFORE INSERT ON continuity_delivery_object_refs
WHEN EXISTS (
        SELECT 1 FROM continuity_delivery_ref_seals seal WHERE seal.receipt_id=NEW.receipt_id
    ) OR NOT EXISTS (
        SELECT 1 FROM continuity_delivery_receipts receipt
         WHERE receipt.receipt_id=NEW.receipt_id AND NEW.ordinal < receipt.object_ref_count
    )
BEGIN SELECT RAISE(ABORT, 'continuity delivery object refs are sealed or out of bounds'); END;

CREATE TRIGGER continuity_delivery_evidence_refs_insert_guard
BEFORE INSERT ON continuity_delivery_evidence_refs
WHEN EXISTS (
        SELECT 1 FROM continuity_delivery_ref_seals seal WHERE seal.receipt_id=NEW.receipt_id
    ) OR NOT EXISTS (
        SELECT 1 FROM continuity_delivery_receipts receipt
         WHERE receipt.receipt_id=NEW.receipt_id AND NEW.ordinal < receipt.evidence_ref_count
    )
BEGIN SELECT RAISE(ABORT, 'continuity delivery evidence refs are sealed or out of bounds'); END;

CREATE TRIGGER continuity_delivery_observed_parent
BEFORE INSERT ON continuity_delivery_receipts
WHEN NEW.receipt_state = 'host_observed' AND NOT EXISTS (
    SELECT 1 FROM continuity_delivery_receipts parent
     WHERE parent.receipt_id = NEW.parent_receipt_id
       AND parent.receipt_state = 'offered_to_host'
       AND parent.context_id = NEW.context_id
       AND parent.purpose = NEW.purpose
       AND parent.store_id = NEW.store_id
       AND parent.repository_id = NEW.repository_id
       AND parent.binding_digest = NEW.binding_digest
       AND parent.host = NEW.host
       AND parent.session_ref = NEW.session_ref
       AND parent.payload_digest = NEW.payload_digest
       AND parent.object_ref_count = NEW.object_ref_count
       AND parent.evidence_ref_count = NEW.evidence_ref_count
       AND parent.refs_manifest_digest = NEW.refs_manifest_digest
       AND parent.method_id = NEW.method_id
       AND parent.method_version = NEW.method_version
       AND parent.rendered_bytes = NEW.rendered_bytes
       AND parent.pulse_tokens = NEW.pulse_tokens
       AND parent.baseline_kind IS NEW.baseline_kind
       AND parent.source_equivalent_tokens IS NEW.source_equivalent_tokens
       AND parent.coverage_counted = NEW.coverage_counted
       AND parent.coverage_total = NEW.coverage_total
)
BEGIN SELECT RAISE(ABORT, 'host observation does not match offered delivery'); END;

CREATE TRIGGER continuity_delivery_provider_parent
BEFORE INSERT ON continuity_delivery_receipts
WHEN NEW.receipt_state = 'provider_measurement' AND NOT EXISTS (
    SELECT 1 FROM continuity_delivery_receipts parent
     WHERE parent.receipt_id = NEW.parent_receipt_id
       AND parent.receipt_state = 'host_observed'
       AND parent.context_id = NEW.context_id
       AND parent.purpose = NEW.purpose
       AND parent.store_id = NEW.store_id
       AND parent.repository_id = NEW.repository_id
       AND parent.binding_digest = NEW.binding_digest
       AND parent.host = NEW.host
       AND parent.session_ref = NEW.session_ref
       AND parent.payload_digest = NEW.payload_digest
       AND parent.object_ref_count = NEW.object_ref_count
       AND parent.evidence_ref_count = NEW.evidence_ref_count
       AND parent.refs_manifest_digest = NEW.refs_manifest_digest
       AND parent.method_id = NEW.method_id
       AND parent.method_version = NEW.method_version
       AND parent.rendered_bytes = NEW.rendered_bytes
       AND parent.pulse_tokens = NEW.pulse_tokens
       AND parent.baseline_kind IS NEW.baseline_kind
       AND parent.source_equivalent_tokens IS NEW.source_equivalent_tokens
       AND parent.coverage_counted = NEW.coverage_counted
       AND parent.coverage_total = NEW.coverage_total
)
BEGIN SELECT RAISE(ABORT, 'provider measurement does not match host observation'); END;

CREATE TRIGGER continuity_delivery_receipts_no_update
BEFORE UPDATE ON continuity_delivery_receipts
BEGIN SELECT RAISE(ABORT, 'continuity delivery receipts are immutable'); END;

CREATE TRIGGER continuity_delivery_receipts_no_delete
BEFORE DELETE ON continuity_delivery_receipts
BEGIN SELECT RAISE(ABORT, 'continuity delivery receipts are append-only'); END;

UPDATE store_identity
   SET min_reader_version = 52,
       min_writer_version = 52
 WHERE singleton = 1;

CREATE TRIGGER continuity_delivery_object_refs_no_update
BEFORE UPDATE ON continuity_delivery_object_refs
BEGIN SELECT RAISE(ABORT, 'continuity delivery object refs are immutable'); END;

CREATE TRIGGER continuity_delivery_object_refs_no_delete
BEFORE DELETE ON continuity_delivery_object_refs
BEGIN SELECT RAISE(ABORT, 'continuity delivery object refs are append-only'); END;

CREATE TRIGGER continuity_delivery_evidence_refs_no_update
BEFORE UPDATE ON continuity_delivery_evidence_refs
BEGIN SELECT RAISE(ABORT, 'continuity delivery evidence refs are immutable'); END;

CREATE TRIGGER continuity_delivery_evidence_refs_no_delete
BEFORE DELETE ON continuity_delivery_evidence_refs
BEGIN SELECT RAISE(ABORT, 'continuity delivery evidence refs are append-only'); END;

CREATE TRIGGER continuity_delivery_ref_seals_no_update
BEFORE UPDATE ON continuity_delivery_ref_seals
BEGIN SELECT RAISE(ABORT, 'continuity delivery ref seals are immutable'); END;

CREATE TRIGGER continuity_delivery_ref_seals_no_delete
BEFORE DELETE ON continuity_delivery_ref_seals
BEGIN SELECT RAISE(ABORT, 'continuity delivery ref seals are append-only'); END;
