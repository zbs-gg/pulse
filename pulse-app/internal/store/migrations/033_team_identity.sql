-- Team-store foundation. This migration is additive: it creates no team
-- marker and assigns no ownership or scope to legacy local rows.

CREATE TABLE schema_migration_manifest (
    version     INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    sha256      TEXT NOT NULL CHECK(length(sha256) = 64),
    applied_at  TEXT NOT NULL
);

CREATE TRIGGER schema_migration_manifest_no_update
BEFORE UPDATE ON schema_migration_manifest
BEGIN
    SELECT RAISE(ABORT, 'migration manifest is immutable');
END;

CREATE TRIGGER schema_migration_manifest_no_delete
BEFORE DELETE ON schema_migration_manifest
BEGIN
    SELECT RAISE(ABORT, 'migration manifest is immutable');
END;

CREATE TABLE team_bootstrap_candidates (
    singleton           INTEGER PRIMARY KEY CHECK(singleton = 1),
    created_by_version  INTEGER NOT NULL CHECK(created_by_version = 33),
    created_at          TEXT NOT NULL
);

CREATE TABLE team_stores (
    singleton                    INTEGER PRIMARY KEY CHECK(singleton = 1),
    store_id                     TEXT NOT NULL UNIQUE,
    team_id                      TEXT NOT NULL UNIQUE,
    team_name                    TEXT NOT NULL,
    min_reader_version           INTEGER NOT NULL CHECK(min_reader_version >= 33),
    min_writer_version           INTEGER NOT NULL CHECK(min_writer_version >= min_reader_version),
    durability_profile           TEXT NOT NULL CHECK(durability_profile = 'wal-full-fk'),
    auth_epoch                   INTEGER NOT NULL DEFAULT 1 CHECK(auth_epoch >= 1),
    bootstrap_root_fingerprint   TEXT NOT NULL CHECK(length(bootstrap_root_fingerprint) = 64),
    bootstrap_consumed_at        TEXT NOT NULL,
    created_at                   TEXT NOT NULL
);

CREATE TRIGGER team_store_identity_immutable
BEFORE UPDATE OF store_id, team_id, bootstrap_root_fingerprint, bootstrap_consumed_at, created_at
ON team_stores
BEGIN
    SELECT RAISE(ABORT, 'team store identity is immutable');
END;

CREATE TRIGGER team_store_schema_floors_monotonic
BEFORE UPDATE OF min_reader_version, min_writer_version ON team_stores
WHEN NEW.min_reader_version < OLD.min_reader_version
  OR NEW.min_writer_version < OLD.min_writer_version
BEGIN
    SELECT RAISE(ABORT, 'team store schema floors are monotonic');
END;

CREATE TRIGGER team_stores_auth_epoch_monotonic
BEFORE UPDATE OF auth_epoch ON team_stores
WHEN NEW.auth_epoch < OLD.auth_epoch
BEGIN SELECT RAISE(ABORT, 'auth epoch is monotonic'); END;

CREATE TABLE team_principals (
    principal_id   TEXT PRIMARY KEY,
    store_id       TEXT NOT NULL REFERENCES team_stores(store_id),
    kind           TEXT NOT NULL CHECK(kind IN ('human', 'agent', 'service')),
    status         TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active', 'revoked')),
    auth_epoch     INTEGER NOT NULL DEFAULT 1 CHECK(auth_epoch >= 1),
    created_at     TEXT NOT NULL,
    revoked_at     TEXT
);
CREATE INDEX idx_team_principals_store_kind ON team_principals(store_id, kind, status);
CREATE TRIGGER team_principals_auth_epoch_monotonic
BEFORE UPDATE OF auth_epoch ON team_principals
WHEN NEW.auth_epoch < OLD.auth_epoch
BEGIN SELECT RAISE(ABORT, 'auth epoch is monotonic'); END;

CREATE TABLE team_human_identities (
    identity_key       TEXT PRIMARY KEY CHECK(length(identity_key) = 64),
    human_principal_id TEXT NOT NULL UNIQUE REFERENCES team_principals(principal_id),
    created_at         TEXT NOT NULL
);

CREATE TABLE team_service_identities (
    service_key         TEXT PRIMARY KEY CHECK(length(service_key) = 64),
    client_key          TEXT NOT NULL CHECK(length(client_key) = 64),
    service_principal_id TEXT NOT NULL UNIQUE REFERENCES team_principals(principal_id),
    created_at          TEXT NOT NULL
);

CREATE TABLE team_memberships (
    membership_id      TEXT PRIMARY KEY,
    team_id            TEXT NOT NULL REFERENCES team_stores(team_id),
    principal_id       TEXT NOT NULL REFERENCES team_principals(principal_id),
    role               TEXT NOT NULL CHECK(role IN ('owner', 'member', 'reviewer')),
    status             TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active', 'revoked')),
    auth_epoch         INTEGER NOT NULL DEFAULT 1 CHECK(auth_epoch >= 1),
    created_at         TEXT NOT NULL,
    revoked_at         TEXT,
    UNIQUE(team_id, principal_id)
);
CREATE INDEX idx_team_memberships_active_role ON team_memberships(team_id, status, role);
CREATE TRIGGER team_memberships_auth_epoch_monotonic
BEFORE UPDATE OF auth_epoch ON team_memberships
WHEN NEW.auth_epoch < OLD.auth_epoch
BEGIN SELECT RAISE(ABORT, 'auth epoch is monotonic'); END;

CREATE TABLE team_projects (
    project_id            TEXT PRIMARY KEY,
    team_id               TEXT NOT NULL REFERENCES team_stores(team_id),
    name                  TEXT NOT NULL,
    owner_principal_id    TEXT NOT NULL REFERENCES team_principals(principal_id),
    created_by_principal_id TEXT NOT NULL REFERENCES team_principals(principal_id),
    created_at            TEXT NOT NULL,
    UNIQUE(team_id, name)
);

CREATE TABLE team_agent_bindings (
    binding_id          TEXT PRIMARY KEY,
    team_id             TEXT NOT NULL REFERENCES team_stores(team_id),
    human_principal_id  TEXT NOT NULL REFERENCES team_principals(principal_id),
    agent_principal_id  TEXT NOT NULL UNIQUE REFERENCES team_principals(principal_id),
    binding_key         TEXT NOT NULL UNIQUE CHECK(length(binding_key) = 64),
    client_key          TEXT NOT NULL CHECK(length(client_key) = 64),
    status              TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active', 'revoked')),
    auth_epoch          INTEGER NOT NULL DEFAULT 1 CHECK(auth_epoch >= 1),
    created_at          TEXT NOT NULL,
    revoked_at          TEXT
);
CREATE INDEX idx_team_agent_bindings_human ON team_agent_bindings(human_principal_id, status);
CREATE TRIGGER team_agent_bindings_auth_epoch_monotonic
BEFORE UPDATE OF auth_epoch ON team_agent_bindings
WHEN NEW.auth_epoch < OLD.auth_epoch
BEGIN SELECT RAISE(ABORT, 'auth epoch is monotonic'); END;

CREATE TABLE team_oauth_clients (
    oauth_client_key TEXT PRIMARY KEY CHECK(length(oauth_client_key) = 64),
    team_id          TEXT NOT NULL REFERENCES team_stores(team_id),
    kind             TEXT NOT NULL CHECK(kind IN ('agent', 'service')),
    principal_id     TEXT NOT NULL UNIQUE REFERENCES team_principals(principal_id),
    binding_id       TEXT UNIQUE REFERENCES team_agent_bindings(binding_id),
    created_at       TEXT NOT NULL,
    CHECK(
        (kind = 'agent' AND binding_id IS NOT NULL) OR
        (kind = 'service' AND binding_id IS NULL)
    )
);

CREATE TABLE team_project_grants (
    grant_id       TEXT PRIMARY KEY,
    project_id     TEXT NOT NULL REFERENCES team_projects(project_id),
    principal_id   TEXT NOT NULL REFERENCES team_principals(principal_id),
    access_level   TEXT NOT NULL CHECK(access_level IN ('read', 'write', 'admin')),
    status         TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active', 'revoked')),
    auth_epoch     INTEGER NOT NULL DEFAULT 1 CHECK(auth_epoch >= 1),
    created_at     TEXT NOT NULL,
    revoked_at     TEXT,
    UNIQUE(project_id, principal_id)
);
CREATE TRIGGER team_project_grants_auth_epoch_monotonic
BEFORE UPDATE OF auth_epoch ON team_project_grants
WHEN NEW.auth_epoch < OLD.auth_epoch
BEGIN SELECT RAISE(ABORT, 'auth epoch is monotonic'); END;

CREATE TABLE team_audit_events (
    event_id            TEXT PRIMARY KEY,
    store_id            TEXT NOT NULL REFERENCES team_stores(store_id),
    occurred_at         TEXT NOT NULL,
    action              TEXT NOT NULL,
    outcome             TEXT NOT NULL CHECK(outcome IN ('allowed', 'denied', 'error')),
    actor_principal_id  TEXT,
    client_key          TEXT,
    team_id             TEXT NOT NULL REFERENCES team_stores(team_id),
    project_id          TEXT,
    target_kind         TEXT NOT NULL,
    target_id           TEXT,
    request_id          TEXT,
    policy_version      INTEGER NOT NULL,
    mode                TEXT NOT NULL CHECK(mode = 'team-remote'),
    auth_epoch          INTEGER NOT NULL,
    reason_code         TEXT NOT NULL,
    metadata_json       TEXT NOT NULL DEFAULT '{}' CHECK(metadata_json = '{}')
);
CREATE INDEX idx_team_audit_events_time ON team_audit_events(store_id, occurred_at);

CREATE TRIGGER team_audit_events_no_update BEFORE UPDATE ON team_audit_events
BEGIN SELECT RAISE(ABORT, 'audit events are append-only'); END;
CREATE TRIGGER team_audit_events_no_delete BEFORE DELETE ON team_audit_events
BEGIN SELECT RAISE(ABORT, 'audit events are append-only'); END;

CREATE TABLE team_security_events (
    event_id            TEXT PRIMARY KEY,
    store_id            TEXT NOT NULL REFERENCES team_stores(store_id),
    occurred_at         TEXT NOT NULL,
    event_type          TEXT NOT NULL,
    outcome             TEXT NOT NULL CHECK(outcome IN ('allowed', 'denied', 'error')),
    principal_id        TEXT,
    client_key          TEXT,
    team_id             TEXT NOT NULL REFERENCES team_stores(team_id),
    project_id          TEXT,
    request_id          TEXT,
    policy_version      INTEGER NOT NULL,
    mode                TEXT NOT NULL CHECK(mode = 'team-remote'),
    reason_code         TEXT NOT NULL,
    metadata_json       TEXT NOT NULL DEFAULT '{}' CHECK(metadata_json = '{}')
);
CREATE INDEX idx_team_security_events_time ON team_security_events(store_id, occurred_at);

CREATE TRIGGER team_security_events_no_update BEFORE UPDATE ON team_security_events
BEGIN SELECT RAISE(ABORT, 'security events are append-only'); END;
CREATE TRIGGER team_security_events_no_delete BEFORE DELETE ON team_security_events
BEGIN SELECT RAISE(ABORT, 'security events are append-only'); END;

-- A consumed assertion ID is durable across gateway restarts. kid/jti are
-- stored as domain-separated SHA-256 values by the Go store API, not as raw
-- token identifiers or claims.
CREATE TABLE team_assertion_replay (
    store_id     TEXT NOT NULL REFERENCES team_stores(store_id),
    kid          TEXT NOT NULL CHECK(length(kid) = 64),
    jti          TEXT NOT NULL CHECK(length(jti) = 64),
    expires_at   TEXT NOT NULL,
    consumed_at  TEXT NOT NULL,
    PRIMARY KEY(store_id, kid, jti)
) WITHOUT ROWID;
CREATE INDEX idx_team_assertion_replay_expiry ON team_assertion_replay(store_id, expires_at);
