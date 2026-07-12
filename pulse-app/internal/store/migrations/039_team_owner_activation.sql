-- Browser-step-up Owner approvals and the explicit synthetic-only activation
-- barrier. Approval rows deliberately do not foreign-key store/team/owner: a
-- bootstrap approval must be durable before those identities exist. The Go
-- store validates every binding again inside the consuming transaction.

DROP TRIGGER team_policy_metadata_after_store_insert;

CREATE TRIGGER team_policy_metadata_after_store_insert
AFTER INSERT ON team_stores
BEGIN
    INSERT INTO team_policy_metadata(
        store_id, team_id, policy_version, schema_version,
        policy_epoch, global_epoch, real_content_state, updated_at)
    VALUES (
        NEW.store_id, NEW.team_id, 1, 39,
        1, NEW.auth_epoch, 'blocked', NEW.created_at);
END;

UPDATE team_policy_metadata
   SET schema_version = 39,
       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
 WHERE schema_version < 39;

CREATE TABLE team_owner_approvals (
    nonce_hash             TEXT PRIMARY KEY CHECK(
                                length(nonce_hash) = 64
                                AND nonce_hash NOT GLOB '*[^0-9a-f]*'
                           ),
    store_id               TEXT NOT NULL CHECK(
                                length(store_id) BETWEEN 1 AND 255
                                AND store_id NOT GLOB '*[^A-Za-z0-9._:-]*'
                           ),
    team_id                TEXT NOT NULL CHECK(
                                length(team_id) BETWEEN 1 AND 255
                                AND team_id NOT GLOB '*[^A-Za-z0-9._:-]*'
                           ),
    owner_principal_id     TEXT NOT NULL CHECK(
                                length(owner_principal_id) BETWEEN 1 AND 255
                                AND owner_principal_id NOT GLOB '*[^A-Za-z0-9._:-]*'
                           ),
    client_key             TEXT NOT NULL CHECK(
                                length(client_key) = 64
                                AND client_key NOT GLOB '*[^0-9a-f]*'
                           ),
    writer_id              TEXT NOT NULL CHECK(
                                writer_id = '' OR (
                                    length(writer_id) BETWEEN 1 AND 255
                                    AND writer_id NOT GLOB '*[^A-Za-z0-9._:-]*'
                                )
                           ),
    writer_lease_token_hash TEXT NOT NULL CHECK(
                                writer_lease_token_hash = '' OR (
                                    length(writer_lease_token_hash) = 64
                                    AND writer_lease_token_hash NOT GLOB '*[^0-9a-f]*'
                                )
                           ),
    action                 TEXT NOT NULL CHECK(action IN (
                                'team.bootstrap',
                                'membership.create', 'membership.revoke',
                                'agent_binding.create', 'agent_binding.revoke',
                                'service_principal.create', 'service_principal.revoke',
                                'project.create',
                                'project_grant.create', 'project_grant.revoke',
                                'team.object.delete.shared',
                                'team.audit.inspect', 'team.deletion.status',
                                'team.activation.synthetic'
                           )),
    target_kind            TEXT NOT NULL CHECK(
                                length(target_kind) BETWEEN 1 AND 64
                                AND target_kind NOT GLOB '*[^A-Za-z0-9._:-]*'
                           ),
    target_id              TEXT NOT NULL CHECK(
                                length(target_id) BETWEEN 1 AND 255
                                AND target_id NOT GLOB '*[^A-Za-z0-9._:-]*'
                           ),
    target_digest          TEXT NOT NULL CHECK(
                                length(target_digest) = 64
                                AND target_digest NOT GLOB '*[^0-9a-f]*'
                           ),
    assertion_kid_hash     TEXT NOT NULL CHECK(
                                length(assertion_kid_hash) = 64
                                AND assertion_kid_hash NOT GLOB '*[^0-9a-f]*'
                           ),
    assertion_jti_hash     TEXT NOT NULL CHECK(
                                length(assertion_jti_hash) = 64
                                AND assertion_jti_hash NOT GLOB '*[^0-9a-f]*'
                           ),
    assertion_expires_at   TEXT NOT NULL CHECK(
                                length(assertion_expires_at) BETWEEN 20 AND 40
                           ),
    step_up_at             TEXT NOT NULL CHECK(length(step_up_at) BETWEEN 20 AND 40),
    issued_at              TEXT NOT NULL CHECK(length(issued_at) BETWEEN 20 AND 40),
    expires_at             TEXT NOT NULL CHECK(length(expires_at) BETWEEN 20 AND 40),
    consumed_at            TEXT CHECK(
                                consumed_at IS NULL OR length(consumed_at) BETWEEN 20 AND 40
                           ),
    consume_audit_event_id TEXT UNIQUE REFERENCES team_audit_events(event_id),
    CHECK(
        (consumed_at IS NULL AND consume_audit_event_id IS NULL)
        OR (consumed_at IS NOT NULL AND consume_audit_event_id IS NOT NULL)
    ),
    UNIQUE(assertion_kid_hash, assertion_jti_hash)
);

CREATE INDEX idx_team_owner_approvals_binding
    ON team_owner_approvals(store_id, team_id, owner_principal_id, action, expires_at);

CREATE TRIGGER team_owner_approvals_binding_immutable
BEFORE UPDATE OF nonce_hash, store_id, team_id, owner_principal_id, client_key,
                 writer_id, writer_lease_token_hash, action,
                 target_kind, target_id, target_digest, step_up_at, issued_at,
                 expires_at, assertion_kid_hash, assertion_jti_hash,
                 assertion_expires_at
ON team_owner_approvals
BEGIN SELECT RAISE(ABORT, 'owner approval binding is immutable'); END;

CREATE TRIGGER team_owner_approvals_consume_once
BEFORE UPDATE OF consumed_at, consume_audit_event_id ON team_owner_approvals
WHEN OLD.consumed_at IS NOT NULL
  OR NEW.consumed_at IS NULL
  OR NEW.consume_audit_event_id IS NULL
BEGIN SELECT RAISE(ABORT, 'owner approval is single use'); END;

CREATE TRIGGER team_owner_approvals_no_delete
BEFORE DELETE ON team_owner_approvals
BEGIN SELECT RAISE(ABORT, 'owner approvals are durable'); END;

ALTER TABLE team_deletion_operations
    ADD COLUMN owner_approval_nonce_hash TEXT
    REFERENCES team_owner_approvals(nonce_hash)
    CHECK(
        owner_approval_nonce_hash IS NULL OR (
            length(owner_approval_nonce_hash) = 64
            AND owner_approval_nonce_hash NOT GLOB '*[^0-9a-f]*'
        )
    );

CREATE UNIQUE INDEX idx_team_deletion_operations_owner_approval
    ON team_deletion_operations(owner_approval_nonce_hash)
    WHERE owner_approval_nonce_hash IS NOT NULL;

CREATE TABLE team_remote_activation (
    singleton                 INTEGER PRIMARY KEY CHECK(singleton = 1),
    store_id                  TEXT NOT NULL UNIQUE REFERENCES team_stores(store_id),
    team_id                   TEXT NOT NULL UNIQUE REFERENCES team_stores(team_id),
    activation_state          TEXT NOT NULL DEFAULT 'inactive'
                              CHECK(activation_state IN ('inactive', 'active')),
    content_boundary          TEXT NOT NULL DEFAULT 'synthetic'
                              CHECK(content_boundary = 'synthetic'),
    public_enabled            INTEGER NOT NULL DEFAULT 0 CHECK(public_enabled IN (0, 1)),
    synthetic_gate_digest     TEXT CHECK(
                                  synthetic_gate_digest IS NULL OR (
                                      length(synthetic_gate_digest) = 64
                                      AND synthetic_gate_digest NOT GLOB '*[^0-9a-f]*'
                                  )
                              ),
    activated_by_principal_id TEXT REFERENCES team_principals(principal_id),
    activation_audit_event_id TEXT UNIQUE REFERENCES team_audit_events(event_id),
    activated_at              TEXT CHECK(
                                  activated_at IS NULL OR length(activated_at) BETWEEN 20 AND 40
                              ),
    created_at                TEXT NOT NULL CHECK(length(created_at) BETWEEN 20 AND 40),
    CHECK(
        (activation_state = 'inactive' AND public_enabled = 0
          AND synthetic_gate_digest IS NULL
          AND activated_by_principal_id IS NULL
          AND activation_audit_event_id IS NULL
          AND activated_at IS NULL)
        OR
        (activation_state = 'active' AND public_enabled = 1
          AND synthetic_gate_digest IS NOT NULL
          AND activated_by_principal_id IS NOT NULL
          AND activation_audit_event_id IS NOT NULL
          AND activated_at IS NOT NULL)
    )
);

INSERT INTO team_remote_activation(
    singleton, store_id, team_id, activation_state, content_boundary,
    public_enabled, created_at)
SELECT 1, store_id, team_id, 'inactive', 'synthetic', 0,
       strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
  FROM team_stores
 WHERE NOT EXISTS (SELECT 1 FROM team_remote_activation WHERE singleton = 1);

CREATE TRIGGER team_remote_activation_after_store_insert
AFTER INSERT ON team_stores
BEGIN
    INSERT INTO team_remote_activation(
        singleton, store_id, team_id, activation_state, content_boundary,
        public_enabled, created_at)
    VALUES (1, NEW.store_id, NEW.team_id, 'inactive', 'synthetic', 0, NEW.created_at);
END;

CREATE TRIGGER team_remote_activation_identity_immutable
BEFORE UPDATE OF singleton, store_id, team_id, content_boundary, created_at
ON team_remote_activation
BEGIN SELECT RAISE(ABORT, 'team activation identity is immutable'); END;

CREATE TRIGGER team_remote_activation_once
BEFORE UPDATE OF activation_state, public_enabled, synthetic_gate_digest,
                 activated_by_principal_id, activation_audit_event_id, activated_at
ON team_remote_activation
WHEN OLD.activation_state <> 'inactive'
  OR NEW.activation_state <> 'active'
  OR NEW.public_enabled <> 1
  OR NEW.synthetic_gate_digest IS NULL
  OR NEW.activated_by_principal_id IS NULL
  OR NEW.activation_audit_event_id IS NULL
  OR NEW.activated_at IS NULL
BEGIN SELECT RAISE(ABORT, 'team activation is one-way and explicit'); END;

-- This increment can expose only the synthetic contract. A later migration,
-- review, and explicit release must own any transition to real content.
CREATE TRIGGER team_policy_real_content_activation_deferred
BEFORE UPDATE OF real_content_state ON team_policy_metadata
WHEN NEW.real_content_state = 'active'
BEGIN SELECT RAISE(ABORT, 'real team content activation is deferred'); END;

CREATE TRIGGER team_store_v39_floor_on_insert
BEFORE INSERT ON team_stores
WHEN NEW.min_reader_version < 39 OR NEW.min_writer_version < 39
BEGIN SELECT RAISE(ABORT, 'team owner activation requires schema v39 readers and writers'); END;

UPDATE team_stores
   SET min_reader_version = CASE WHEN min_reader_version < 39 THEN 39 ELSE min_reader_version END,
       min_writer_version = CASE WHEN min_writer_version < 39 THEN 39 ELSE min_writer_version END;
