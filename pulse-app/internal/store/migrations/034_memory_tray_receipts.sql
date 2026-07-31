-- Private Memory Tray and truthful local write receipts. This migration is
-- applicable only to Personal stores; Local Preview remains unchanged.

CREATE TABLE turn_ledgers (
    ledger_id            TEXT PRIMARY KEY,
    finalize_receipt_id  TEXT NOT NULL UNIQUE,
    host                 TEXT NOT NULL,
    session_id           TEXT NOT NULL,
    turn_id              TEXT NOT NULL,
    source_event_key     TEXT NOT NULL UNIQUE,
    idempotency_key      TEXT NOT NULL UNIQUE,
    binding_digest       TEXT NOT NULL,
    destination_store_id TEXT NOT NULL,
    destination_class    TEXT NOT NULL CHECK(destination_class = 'personal'),
    policy_epoch         INTEGER NOT NULL CHECK(policy_epoch >= 0),
    resolver_epoch       INTEGER NOT NULL CHECK(resolver_epoch >= 0),
    request_digest       TEXT NOT NULL,
    state                TEXT NOT NULL CHECK(state IN ('candidates','no_change','rejected','failed','control')),
    created_at           TEXT NOT NULL,
    finalized_at         TEXT NOT NULL,
    UNIQUE(host, session_id, turn_id)
);

CREATE TABLE memory_tray_candidates (
    candidate_id         TEXT PRIMARY KEY,
    ledger_id            TEXT NOT NULL REFERENCES turn_ledgers(ledger_id) ON DELETE RESTRICT,
    candidate_kind       TEXT NOT NULL CHECK(candidate_kind IN ('memory_capsule','semantic_delta')),
    operation            TEXT NOT NULL DEFAULT 'create' CHECK(operation IN ('create','correct')),
    target_object_id     TEXT,
    target_content_digest TEXT,
    version              INTEGER NOT NULL CHECK(version >= 1),
    content_digest       TEXT NOT NULL,
    payload_json         TEXT NOT NULL,
    state                TEXT NOT NULL CHECK(state IN ('pending','committing','committed','canceled','failed')),
    grace_expires_at     TEXT NOT NULL,
    canonical_object_id TEXT,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL,
    terminal_at          TEXT,
    CHECK((operation='create' AND target_object_id IS NULL AND target_content_digest IS NULL) OR
          (operation='correct' AND target_object_id IS NOT NULL
                               AND length(target_content_digest)=64
                               AND target_content_digest NOT GLOB '*[^a-f0-9]*')),
    CHECK((state = 'committed' AND canonical_object_id IS NOT NULL) OR
          (state != 'committed' AND canonical_object_id IS NULL))
);
CREATE INDEX idx_memory_tray_due
    ON memory_tray_candidates(state, grace_expires_at, candidate_id);
CREATE INDEX idx_memory_tray_ledger
    ON memory_tray_candidates(ledger_id, candidate_id);

CREATE TABLE private_memory_objects (
    object_id            TEXT PRIMARY KEY,
    candidate_kind       TEXT NOT NULL CHECK(candidate_kind IN ('memory_capsule','semantic_delta')),
    content_digest       TEXT NOT NULL,
    created_from_candidate_id TEXT NOT NULL REFERENCES memory_tray_candidates(candidate_id),
    created_at           TEXT NOT NULL,
    lifecycle            TEXT NOT NULL DEFAULT 'active' CHECK(lifecycle IN ('active','deleted')),
    deleted_at           TEXT
);
CREATE UNIQUE INDEX idx_private_memory_objects_active_digest
    ON private_memory_objects(candidate_kind, content_digest)
 WHERE lifecycle='active';

CREATE TABLE memory_write_receipts (
    receipt_id           TEXT PRIMARY KEY,
    ledger_id            TEXT NOT NULL REFERENCES turn_ledgers(ledger_id) ON DELETE RESTRICT,
    candidate_id         TEXT REFERENCES memory_tray_candidates(candidate_id) ON DELETE RESTRICT,
    candidate_version    INTEGER NOT NULL DEFAULT 0 CHECK(candidate_version >= 0),
    status               TEXT NOT NULL CHECK(status IN (
                           'pending','created','deduplicated','updated',
                           'canceled','rejected','failed')),
    destination_class    TEXT NOT NULL CHECK(destination_class = 'personal'),
    destination_store_id TEXT NOT NULL,
    provenance_host      TEXT NOT NULL,
    provenance_session_id TEXT NOT NULL,
    provenance_turn_id   TEXT NOT NULL,
    provenance_source_event_key TEXT NOT NULL,
    content_digest       TEXT,
    object_id            TEXT,
    reason_code          TEXT,
    policy_epoch         INTEGER NOT NULL CHECK(policy_epoch >= 0),
    resolver_epoch       INTEGER NOT NULL CHECK(resolver_epoch >= 0),
    measurement_method   TEXT NOT NULL,
    created_at           TEXT NOT NULL,
    CHECK((status IN ('created','deduplicated','updated') AND object_id IS NOT NULL) OR
          (status NOT IN ('created','deduplicated','updated') AND object_id IS NULL))
);
CREATE INDEX idx_memory_write_receipts_candidate
    ON memory_write_receipts(candidate_id, created_at, receipt_id);
CREATE INDEX idx_memory_write_receipts_ledger
    ON memory_write_receipts(ledger_id, created_at, receipt_id);

CREATE TABLE memory_write_idempotency (
    operation            TEXT NOT NULL,
    idempotency_key      TEXT NOT NULL,
    request_digest       TEXT NOT NULL,
    receipt_id           TEXT NOT NULL REFERENCES memory_write_receipts(receipt_id),
    object_id            TEXT,
    created_at           TEXT NOT NULL,
    PRIMARY KEY(operation, idempotency_key)
);

CREATE TABLE memory_write_audit (
    audit_id             INTEGER PRIMARY KEY AUTOINCREMENT,
    ledger_id            TEXT NOT NULL REFERENCES turn_ledgers(ledger_id),
    candidate_id         TEXT REFERENCES memory_tray_candidates(candidate_id),
    receipt_id           TEXT NOT NULL REFERENCES memory_write_receipts(receipt_id),
    action               TEXT NOT NULL,
    outcome              TEXT NOT NULL,
    reason_code          TEXT,
    policy_epoch         INTEGER NOT NULL,
    resolver_epoch       INTEGER NOT NULL,
    created_at           TEXT NOT NULL
);

CREATE TABLE private_projection_outbox (
    projection_id        TEXT PRIMARY KEY,
    object_id            TEXT NOT NULL REFERENCES private_memory_objects(object_id),
    candidate_kind       TEXT NOT NULL CHECK(candidate_kind IN ('memory_capsule','semantic_delta')),
    status               TEXT NOT NULL CHECK(status IN ('pending','processing','complete','failed')),
    attempt_count        INTEGER NOT NULL DEFAULT 0 CHECK(attempt_count >= 0),
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL,
    UNIQUE(object_id, candidate_kind)
);

CREATE TABLE private_semantic_projection_rows (
    object_id   TEXT NOT NULL REFERENCES private_memory_objects(object_id) ON DELETE CASCADE,
    row_kind    TEXT NOT NULL CHECK(row_kind IN (
                  'entity','relation','fact','event','checkpoint','session','thread'
                )),
    row_ref     TEXT NOT NULL,
    PRIMARY KEY(object_id, row_kind, row_ref)
);

UPDATE store_identity
   SET min_reader_version = 34,
       min_writer_version = 34
 WHERE singleton = 1;
