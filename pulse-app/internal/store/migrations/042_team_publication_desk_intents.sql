-- Immutable Desk disclosure intents for the human Airlock. This migration is
-- applied only to Desk stores. It creates no Commons write surface and grants
-- no approval authority to the CLI, MCP, host adapter, or model.

CREATE TABLE team_publication_intents (
    intent_id               TEXT PRIMARY KEY,
    source_object_id        TEXT NOT NULL REFERENCES private_memory_objects(object_id) ON DELETE RESTRICT,
    source_content_digest   TEXT NOT NULL CHECK(length(source_content_digest)=64 AND source_content_digest NOT GLOB '*[^a-f0-9]*'),
    deployment_id           TEXT NOT NULL,
    store_id                TEXT NOT NULL,
    team_id                 TEXT NOT NULL,
    policy_epoch            INTEGER NOT NULL CHECK(policy_epoch >= 0),
    writer_principal_id     TEXT NOT NULL,
    client_key              TEXT NOT NULL CHECK(length(client_key)=64 AND client_key NOT GLOB '*[^a-f0-9]*'),
    writer_id               TEXT NOT NULL,
    envelope_schema         TEXT NOT NULL CHECK(envelope_schema='pulse.team.airlock_envelope.v1'),
    envelope_json           TEXT NOT NULL,
    disclosure_purged_at    TEXT,
    envelope_digest         TEXT NOT NULL CHECK(length(envelope_digest)=64 AND envelope_digest NOT GLOB '*[^a-f0-9]*'),
    idempotency_key         TEXT NOT NULL UNIQUE,
    state                   TEXT NOT NULL CHECK(state IN (
                              'prepared','approved','in_flight',
                              'remote_committed_local_pending','reconciled',
                              'canceled','expired','failed')),
    approval_id             TEXT,
    approval_digest         TEXT,
    approved_at             TEXT,
    remote_object_id        TEXT,
    remote_receipt_id       TEXT,
    remote_audit_event_id   TEXT,
    remote_content_digest   TEXT,
    failure_code            TEXT,
    expires_at              TEXT NOT NULL,
    created_at              TEXT NOT NULL,
    updated_at              TEXT NOT NULL,
    terminal_at             TEXT,
    CHECK((approval_id IS NULL AND approval_digest IS NULL AND approved_at IS NULL)
       OR (approval_id IS NOT NULL AND approval_digest=envelope_digest AND approved_at IS NOT NULL)),
    CHECK((state IN ('approved','in_flight','remote_committed_local_pending','reconciled')
           AND approval_id IS NOT NULL AND approval_digest=envelope_digest AND approved_at IS NOT NULL)
       OR state IN ('prepared','canceled','expired','failed')),
    CHECK((state IN ('remote_committed_local_pending','reconciled')
           AND remote_object_id IS NOT NULL AND remote_receipt_id IS NOT NULL
           AND remote_audit_event_id IS NOT NULL AND remote_content_digest=envelope_digest)
       OR (state NOT IN ('remote_committed_local_pending','reconciled')
           AND remote_object_id IS NULL AND remote_receipt_id IS NULL
           AND remote_audit_event_id IS NULL AND remote_content_digest IS NULL)),
    CHECK((state IN ('reconciled','canceled','expired','failed') AND terminal_at IS NOT NULL)
       OR (state NOT IN ('reconciled','canceled','expired','failed') AND terminal_at IS NULL)),
    CHECK((disclosure_purged_at IS NULL AND envelope_json <> '{}')
       OR (disclosure_purged_at IS NOT NULL AND envelope_json = '{}'))
);

CREATE INDEX idx_team_publication_intents_state
    ON team_publication_intents(state, expires_at, intent_id);
CREATE INDEX idx_team_publication_intents_source
    ON team_publication_intents(source_object_id, created_at, intent_id);

CREATE TRIGGER team_publication_intent_envelope_immutable
BEFORE UPDATE OF source_object_id, source_content_digest, deployment_id, store_id, team_id,
                 policy_epoch, writer_principal_id, client_key, writer_id, envelope_schema,
                 envelope_digest, idempotency_key, expires_at, created_at
ON team_publication_intents
BEGIN SELECT RAISE(ABORT, 'Airlock disclosure envelope is immutable'); END;

CREATE TRIGGER team_publication_intent_disclosure_purge_once
BEFORE UPDATE OF envelope_json, disclosure_purged_at ON team_publication_intents
WHEN OLD.disclosure_purged_at IS NOT NULL
  OR NEW.envelope_json <> '{}'
  OR NEW.disclosure_purged_at IS NULL
  OR OLD.state IN ('in_flight','remote_committed_local_pending')
  OR (OLD.state IN ('prepared','approved') AND NEW.state <> 'canceled')
BEGIN SELECT RAISE(ABORT, 'Airlock disclosure purge is invalid'); END;

CREATE TRIGGER team_publication_intent_transition_guard
BEFORE UPDATE OF state ON team_publication_intents
WHEN NOT (
       (OLD.state='prepared' AND NEW.state IN ('approved','canceled','expired','failed'))
    OR (OLD.state='approved' AND NEW.state IN ('in_flight','canceled','expired','failed'))
    OR (OLD.state='in_flight' AND NEW.state IN ('remote_committed_local_pending','reconciled','failed'))
    OR (OLD.state='remote_committed_local_pending' AND NEW.state IN ('reconciled','failed'))
    OR (OLD.state='failed' AND NEW.state='in_flight'
        AND OLD.approval_id IS NOT NULL
        AND OLD.approval_digest=OLD.envelope_digest
        AND OLD.approved_at IS NOT NULL
        AND julianday(OLD.expires_at) > julianday('now'))
)
BEGIN SELECT RAISE(ABORT, 'invalid Airlock disclosure transition'); END;

CREATE TRIGGER team_publication_intent_approval_write_once
BEFORE UPDATE OF approval_id, approval_digest, approved_at ON team_publication_intents
WHEN OLD.approval_id IS NOT NULL
  OR OLD.approval_digest IS NOT NULL
  OR OLD.approved_at IS NOT NULL
  OR NEW.approval_id IS NULL
  OR NEW.approval_digest <> OLD.envelope_digest
  OR NEW.approved_at IS NULL
BEGIN SELECT RAISE(ABORT, 'Airlock approval is write-once and exact'); END;

CREATE TRIGGER team_publication_intent_remote_result_write_once
BEFORE UPDATE OF remote_object_id, remote_receipt_id, remote_audit_event_id, remote_content_digest
ON team_publication_intents
WHEN OLD.remote_object_id IS NOT NULL
  OR OLD.remote_receipt_id IS NOT NULL
  OR OLD.remote_audit_event_id IS NOT NULL
  OR OLD.remote_content_digest IS NOT NULL
  OR NEW.remote_object_id IS NULL
  OR NEW.remote_receipt_id IS NULL
  OR NEW.remote_audit_event_id IS NULL
  OR NEW.remote_content_digest <> OLD.envelope_digest
BEGIN SELECT RAISE(ABORT, 'Airlock remote result is write-once and exact'); END;

CREATE TRIGGER team_publication_intent_no_delete
BEFORE DELETE ON team_publication_intents
WHEN OLD.disclosure_purged_at IS NULL
  OR OLD.envelope_json <> '{}'
  OR OLD.state NOT IN ('reconciled','canceled','expired','failed')
BEGIN SELECT RAISE(ABORT, 'Airlock disclosure intent is not purgeable'); END;

UPDATE store_identity
   SET min_reader_version = 42,
       min_writer_version = 42
 WHERE singleton = 1;
