-- Atomic remote half of the Desk-to-Commons Airlock saga. This migration is
-- applied only to Commons stores. It contains the exact approved outbound
-- envelope and paired remote receipt, never a Desk source identifier, digest,
-- path, transcript, or private lineage reference.

CREATE TABLE team_publication_approvals (
    nonce_hash                    TEXT PRIMARY KEY CHECK(length(nonce_hash)=64 AND nonce_hash NOT GLOB '*[^a-f0-9]*'),
    store_id                     TEXT NOT NULL REFERENCES team_stores(store_id),
    team_id                      TEXT NOT NULL REFERENCES team_stores(team_id),
    deployment_id                TEXT NOT NULL,
    shared_project_id            TEXT NOT NULL REFERENCES team_projects(project_id),
    envelope_digest              TEXT NOT NULL CHECK(length(envelope_digest)=64 AND envelope_digest NOT GLOB '*[^a-f0-9]*'),
    idempotency_key_hash         TEXT NOT NULL CHECK(length(idempotency_key_hash)=64 AND idempotency_key_hash NOT GLOB '*[^a-f0-9]*'),
    operation_digest             TEXT NOT NULL CHECK(length(operation_digest)=64 AND operation_digest NOT GLOB '*[^a-f0-9]*'),
    publisher_principal_id       TEXT NOT NULL REFERENCES team_principals(principal_id),
    publisher_membership_id      TEXT NOT NULL REFERENCES team_memberships(membership_id),
    publisher_client_key         TEXT NOT NULL CHECK(length(publisher_client_key)=64 AND publisher_client_key NOT GLOB '*[^a-f0-9]*'),
    publisher_binding_id         TEXT NOT NULL REFERENCES team_agent_bindings(binding_id),
    runtime_writer_id            TEXT NOT NULL,
    writer_lease_token_hash      TEXT NOT NULL CHECK(length(writer_lease_token_hash)=64 AND writer_lease_token_hash NOT GLOB '*[^a-f0-9]*'),
    approving_owner_principal_id TEXT NOT NULL REFERENCES team_principals(principal_id),
    approving_client_key         TEXT NOT NULL CHECK(length(approving_client_key)=64 AND approving_client_key NOT GLOB '*[^a-f0-9]*'),
    policy_epoch                 INTEGER NOT NULL CHECK(policy_epoch >= 1),
    global_epoch                 INTEGER NOT NULL CHECK(global_epoch >= 1),
    assertion_kid_hash           TEXT NOT NULL CHECK(length(assertion_kid_hash)=64 AND assertion_kid_hash NOT GLOB '*[^a-f0-9]*'),
    assertion_jti_hash           TEXT NOT NULL CHECK(length(assertion_jti_hash)=64 AND assertion_jti_hash NOT GLOB '*[^a-f0-9]*'),
    assertion_expires_at         TEXT NOT NULL,
    step_up_at                   TEXT NOT NULL,
    issued_at                    TEXT NOT NULL,
    expires_at                   TEXT NOT NULL,
    consumed_at                  TEXT,
    consume_audit_event_id       TEXT UNIQUE REFERENCES team_audit_events(event_id),
    CHECK((consumed_at IS NULL AND consume_audit_event_id IS NULL)
       OR (consumed_at IS NOT NULL AND consume_audit_event_id IS NOT NULL)),
    UNIQUE(assertion_kid_hash, assertion_jti_hash)
);

CREATE INDEX idx_team_publication_approvals_binding
    ON team_publication_approvals(store_id, team_id, approving_owner_principal_id, expires_at);
CREATE INDEX idx_team_publication_approvals_idempotency
    ON team_publication_approvals(store_id, team_id, deployment_id, idempotency_key_hash);

CREATE TRIGGER team_publication_approvals_binding_immutable
BEFORE UPDATE OF nonce_hash, store_id, team_id, deployment_id, shared_project_id,
                 envelope_digest, idempotency_key_hash, operation_digest,
                 publisher_principal_id, publisher_membership_id,
                 publisher_client_key, publisher_binding_id,
                 runtime_writer_id, writer_lease_token_hash,
                 approving_owner_principal_id, approving_client_key,
                 policy_epoch, global_epoch, assertion_kid_hash,
                 assertion_jti_hash, assertion_expires_at, step_up_at,
                 issued_at, expires_at
ON team_publication_approvals
BEGIN SELECT RAISE(ABORT, 'publication approval binding is immutable'); END;

CREATE TRIGGER team_publication_approvals_consume_once
BEFORE UPDATE OF consumed_at, consume_audit_event_id ON team_publication_approvals
WHEN OLD.consumed_at IS NOT NULL
  OR NEW.consumed_at IS NULL
  OR NEW.consume_audit_event_id IS NULL
BEGIN SELECT RAISE(ABORT, 'publication approval is single use'); END;

CREATE TRIGGER team_publication_approvals_no_delete
BEFORE DELETE ON team_publication_approvals
BEGIN SELECT RAISE(ABORT, 'publication approvals are durable'); END;

CREATE TABLE team_publication_receipts (
    publication_id                TEXT PRIMARY KEY,
    store_id                     TEXT NOT NULL REFERENCES team_stores(store_id),
    team_id                      TEXT NOT NULL REFERENCES team_stores(team_id),
    deployment_id                TEXT NOT NULL,
    shared_project_id            TEXT NOT NULL REFERENCES team_projects(project_id),
    idempotency_key_hash         TEXT NOT NULL CHECK(length(idempotency_key_hash)=64 AND idempotency_key_hash NOT GLOB '*[^a-f0-9]*'),
    operation_digest             TEXT NOT NULL CHECK(length(operation_digest)=64 AND operation_digest NOT GLOB '*[^a-f0-9]*'),
    envelope_schema              TEXT NOT NULL CHECK(envelope_schema='pulse.team.airlock_envelope.v1'),
    envelope_digest              TEXT NOT NULL CHECK(length(envelope_digest)=64 AND envelope_digest NOT GLOB '*[^a-f0-9]*'),
    policy_epoch                 INTEGER NOT NULL CHECK(policy_epoch >= 1),
    global_epoch                 INTEGER NOT NULL CHECK(global_epoch >= 1),
    publisher_principal_id       TEXT NOT NULL REFERENCES team_principals(principal_id),
    publisher_membership_id      TEXT NOT NULL REFERENCES team_memberships(membership_id),
    publisher_client_key         TEXT NOT NULL CHECK(length(publisher_client_key)=64 AND publisher_client_key NOT GLOB '*[^a-f0-9]*'),
    publisher_binding_id         TEXT NOT NULL REFERENCES team_agent_bindings(binding_id),
    runtime_writer_id            TEXT NOT NULL,
    approving_owner_principal_id TEXT NOT NULL REFERENCES team_principals(principal_id),
    approval_nonce_hash          TEXT NOT NULL UNIQUE REFERENCES team_publication_approvals(nonce_hash),
    approval_audit_event_id      TEXT NOT NULL UNIQUE REFERENCES team_audit_events(event_id),
    object_id                    TEXT NOT NULL UNIQUE,
    capsule_id                   TEXT NOT NULL UNIQUE,
    object_audit_event_id        TEXT NOT NULL UNIQUE REFERENCES team_audit_events(event_id),
    event_projection_job_id      TEXT NOT NULL UNIQUE,
    embedding_projection_job_id  TEXT NOT NULL UNIQUE,
    receipt_digest               TEXT NOT NULL CHECK(length(receipt_digest)=64 AND receipt_digest NOT GLOB '*[^a-f0-9]*'),
    created_at                   TEXT NOT NULL,
    UNIQUE(store_id, team_id, deployment_id, idempotency_key_hash)
);

-- The exact reviewed disclosure is needed only while the published root is
-- live. It is intentionally separated from the immutable content-free receipt
-- so the deletion barrier can purge content without losing audit identity.
CREATE TABLE team_publication_receipt_payloads (
    publication_id TEXT PRIMARY KEY REFERENCES team_publication_receipts(publication_id),
    envelope_json  TEXT NOT NULL CHECK(length(CAST(envelope_json AS BLOB)) BETWEEN 2 AND 65536)
);

CREATE TRIGGER team_publication_receipt_payloads_immutable
BEFORE UPDATE ON team_publication_receipt_payloads
BEGIN SELECT RAISE(ABORT, 'Commons publication receipt payload is immutable'); END;

CREATE INDEX idx_team_publication_receipts_project
    ON team_publication_receipts(team_id, shared_project_id, created_at, publication_id);
CREATE INDEX idx_team_publication_receipts_publisher
    ON team_publication_receipts(team_id, publisher_principal_id, created_at, publication_id);

CREATE TRIGGER team_publication_receipts_contract_insert
BEFORE INSERT ON team_publication_receipts
WHEN NOT EXISTS (
    SELECT 1
      FROM team_publication_approvals approval
      JOIN team_projects project
        ON project.project_id = NEW.shared_project_id
       AND project.team_id = NEW.team_id
       AND project.owner_principal_id = NEW.approving_owner_principal_id
      JOIN team_object_registry object
        ON object.object_id = NEW.object_id
       AND object.store_id = NEW.store_id
       AND object.team_id = NEW.team_id
       AND object.object_kind = 'memory'
       AND object.scope_type = 'project'
       AND object.scope_id = NEW.shared_project_id
       AND object.owner_principal_id = NEW.approving_owner_principal_id
       AND object.author_principal_id = NEW.publisher_principal_id
       AND object.lifecycle = 'active'
       AND object.generation = 1
      JOIN team_memory_capsules capsule
        ON capsule.capsule_id = NEW.capsule_id
       AND capsule.root_object_id = NEW.object_id
       AND capsule.team_id = NEW.team_id
       AND capsule.scope_type = 'project'
       AND capsule.scope_id = NEW.shared_project_id
       AND capsule.root_generation = 1
      JOIN team_audit_events approval_audit
        ON approval_audit.event_id = NEW.approval_audit_event_id
       AND approval_audit.store_id = NEW.store_id
       AND approval_audit.team_id = NEW.team_id
       AND approval_audit.project_id = NEW.shared_project_id
       AND approval_audit.action = 'team.commons.publish.approval'
       AND approval_audit.outcome = 'allowed'
       AND approval_audit.actor_principal_id = NEW.approving_owner_principal_id
       AND approval_audit.client_key = approval.approving_client_key
       AND approval_audit.target_kind = 'publication_envelope'
       AND approval_audit.target_id = NEW.envelope_digest
       AND approval_audit.reason_code = 'publication_approval_consumed'
      JOIN team_audit_events object_audit
        ON object_audit.event_id = NEW.object_audit_event_id
       AND object_audit.store_id = NEW.store_id
       AND object_audit.team_id = NEW.team_id
       AND object_audit.project_id = NEW.shared_project_id
       AND object_audit.action = 'team.object.write'
       AND object_audit.outcome = 'allowed'
       AND object_audit.actor_principal_id = NEW.publisher_principal_id
       AND object_audit.client_key = NEW.publisher_client_key
       AND object_audit.target_kind = 'memory'
       AND object_audit.target_id = NEW.object_id
       AND object_audit.reason_code = 'object_stored'
      JOIN team_projection_jobs event_job
        ON event_job.job_id = NEW.event_projection_job_id
       AND event_job.root_object_id = NEW.object_id
       AND event_job.root_generation = 1
       AND event_job.projection_kind = 'event'
      JOIN team_projection_jobs embedding_job
        ON embedding_job.job_id = NEW.embedding_projection_job_id
       AND embedding_job.root_object_id = NEW.object_id
       AND embedding_job.root_generation = 1
       AND embedding_job.projection_kind = 'embedding'
     WHERE approval.nonce_hash = NEW.approval_nonce_hash
       AND approval.store_id = NEW.store_id
       AND approval.team_id = NEW.team_id
       AND approval.deployment_id = NEW.deployment_id
       AND approval.shared_project_id = NEW.shared_project_id
       AND approval.envelope_digest = NEW.envelope_digest
       AND approval.idempotency_key_hash = NEW.idempotency_key_hash
       AND approval.operation_digest = NEW.operation_digest
       AND approval.publisher_principal_id = NEW.publisher_principal_id
       AND approval.publisher_membership_id = NEW.publisher_membership_id
       AND approval.publisher_client_key = NEW.publisher_client_key
       AND approval.publisher_binding_id = NEW.publisher_binding_id
       AND approval.runtime_writer_id = NEW.runtime_writer_id
       AND approval.approving_owner_principal_id = NEW.approving_owner_principal_id
       AND approval.policy_epoch = NEW.policy_epoch
       AND approval.global_epoch = NEW.global_epoch
       AND approval.consumed_at IS NOT NULL
       AND approval.consume_audit_event_id = NEW.approval_audit_event_id
)
BEGIN SELECT RAISE(ABORT, 'Commons publication receipt contract mismatch'); END;

CREATE TRIGGER team_publication_receipts_immutable
BEFORE UPDATE ON team_publication_receipts
BEGIN SELECT RAISE(ABORT, 'Commons publication receipts are immutable'); END;

CREATE TRIGGER team_publication_receipts_no_delete
BEFORE DELETE ON team_publication_receipts
BEGIN SELECT RAISE(ABORT, 'Commons publication receipts are append-only'); END;

UPDATE store_identity
   SET min_reader_version = 43,
       min_writer_version = 43
 WHERE singleton = 1;

UPDATE team_stores
   SET min_reader_version = CASE WHEN min_reader_version < 43 THEN 43 ELSE min_reader_version END,
       min_writer_version = CASE WHEN min_writer_version < 43 THEN 43 ELSE min_writer_version END;

UPDATE team_policy_metadata
   SET schema_version = CASE WHEN schema_version < 43 THEN 43 ELSE schema_version END,
       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

DROP TRIGGER team_policy_metadata_after_store_insert;
CREATE TRIGGER team_policy_metadata_after_store_insert
AFTER INSERT ON team_stores
BEGIN
    INSERT INTO team_policy_metadata(
        store_id, team_id, policy_version, schema_version,
        policy_epoch, global_epoch, real_content_state, updated_at)
    VALUES (
        NEW.store_id, NEW.team_id, 1, 43,
        1, NEW.auth_epoch, 'blocked', NEW.created_at);
END;
