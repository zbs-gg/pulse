-- Canonical policy and object-spine foundation for dedicated team stores.
-- This migration deliberately creates no content mutation operations. U5
-- owns atomic root/audit/idempotency/job writes over these guarded tables.

-- Gateway security-event ingress is aggregate and pre-principal. Preserve its
-- fixed classifications and multiplicity without opening metadata_json.
ALTER TABLE team_security_events
    ADD COLUMN method_class TEXT NOT NULL DEFAULT 'other'
    CHECK(method_class IN ('read', 'write', 'delete', 'other'));
ALTER TABLE team_security_events
    ADD COLUMN path_class TEXT NOT NULL DEFAULT 'unknown'
    CHECK(path_class IN ('mcp', 'oauth_metadata', 'principal', 'team_api', 'readiness', 'unknown'));
ALTER TABLE team_security_events
    ADD COLUMN aggregate_count INTEGER NOT NULL DEFAULT 1
    CHECK(aggregate_count BETWEEN 1 AND 1000);

-- Migration 033 made domain audit append-only, but timestamps alone cannot
-- establish a strict replay order. This auxiliary sequence preserves 033's
-- frozen bytes while assigning every existing and future event one order.
CREATE TABLE team_audit_event_order (
    audit_sequence  INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id        TEXT NOT NULL UNIQUE REFERENCES team_audit_events(event_id),
    store_id        TEXT NOT NULL REFERENCES team_stores(store_id)
);
CREATE INDEX idx_team_audit_event_order_store
    ON team_audit_event_order(store_id, audit_sequence);

INSERT INTO team_audit_event_order(event_id, store_id)
SELECT event_id, store_id
  FROM team_audit_events
 ORDER BY occurred_at, event_id;

CREATE TRIGGER team_audit_event_order_after_insert
AFTER INSERT ON team_audit_events
BEGIN
    INSERT INTO team_audit_event_order(event_id, store_id)
    VALUES (NEW.event_id, NEW.store_id);
END;

CREATE TRIGGER team_audit_event_order_no_update
BEFORE UPDATE ON team_audit_event_order
BEGIN SELECT RAISE(ABORT, 'audit ordering is immutable'); END;
CREATE TRIGGER team_audit_event_order_no_delete
BEFORE DELETE ON team_audit_event_order
BEGIN SELECT RAISE(ABORT, 'audit ordering is append-only'); END;

CREATE TABLE team_policy_metadata (
    store_id            TEXT PRIMARY KEY REFERENCES team_stores(store_id),
    team_id             TEXT NOT NULL UNIQUE REFERENCES team_stores(team_id),
    policy_version      INTEGER NOT NULL CHECK(policy_version >= 1),
    schema_version      INTEGER NOT NULL CHECK(schema_version >= 34),
    policy_epoch        INTEGER NOT NULL DEFAULT 1 CHECK(policy_epoch >= 1),
    global_epoch        INTEGER NOT NULL CHECK(global_epoch >= 1),
    real_content_state  TEXT NOT NULL DEFAULT 'blocked'
                        CHECK(real_content_state IN ('blocked', 'synthetic', 'active')),
    updated_at          TEXT NOT NULL
);

CREATE TRIGGER team_policy_metadata_identity_immutable
BEFORE UPDATE OF store_id, team_id ON team_policy_metadata
BEGIN SELECT RAISE(ABORT, 'team policy identity is immutable'); END;

CREATE TRIGGER team_policy_metadata_versions_monotonic
BEFORE UPDATE OF policy_version, schema_version, policy_epoch, global_epoch
ON team_policy_metadata
WHEN NEW.policy_version < OLD.policy_version
  OR NEW.schema_version < OLD.schema_version
  OR NEW.policy_epoch < OLD.policy_epoch
  OR NEW.global_epoch < OLD.global_epoch
BEGIN SELECT RAISE(ABORT, 'team policy epochs and versions are monotonic'); END;

CREATE TRIGGER team_policy_metadata_after_store_insert
AFTER INSERT ON team_stores
BEGIN
    INSERT INTO team_policy_metadata(
        store_id, team_id, policy_version, schema_version,
        policy_epoch, global_epoch, real_content_state, updated_at)
    VALUES (
        NEW.store_id, NEW.team_id, 1, 34,
        1, NEW.auth_epoch, 'blocked', NEW.created_at);
END;

CREATE TRIGGER team_policy_global_epoch_sync
AFTER UPDATE OF auth_epoch ON team_stores
BEGIN
    UPDATE team_policy_metadata
       SET global_epoch = NEW.auth_epoch,
           updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
     WHERE store_id = NEW.store_id;
END;

INSERT OR IGNORE INTO team_policy_metadata(
    store_id, team_id, policy_version, schema_version,
    policy_epoch, global_epoch, real_content_state, updated_at)
SELECT store_id, team_id, 1, 34, 1, auth_epoch, 'blocked', created_at
  FROM team_stores;

CREATE TABLE team_object_registry (
    object_id           TEXT PRIMARY KEY,
    store_id            TEXT NOT NULL REFERENCES team_stores(store_id),
    team_id             TEXT NOT NULL REFERENCES team_stores(team_id),
    object_kind         TEXT NOT NULL CHECK(length(object_kind) BETWEEN 1 AND 64),
    scope_type          TEXT NOT NULL
                        CHECK(scope_type IN ('personal', 'team', 'project', 'repo', 'agent', 'session')),
    scope_id            TEXT NOT NULL CHECK(length(scope_id) BETWEEN 1 AND 255),
    owner_principal_id  TEXT REFERENCES team_principals(principal_id),
    author_principal_id TEXT NOT NULL REFERENCES team_principals(principal_id),
    privacy_tier        TEXT NOT NULL CHECK(privacy_tier IN ('normal', 'sensitive', 'private')),
    retention           TEXT NOT NULL CHECK(retention IN ('session', 'project', 'long_term')),
    lifecycle           TEXT NOT NULL DEFAULT 'active'
                        CHECK(lifecycle IN ('active', 'tombstoned', 'cleaning', 'cleanup_failed', 'complete')),
    generation          INTEGER NOT NULL DEFAULT 1 CHECK(generation >= 1),
    expires_at          TEXT CHECK(expires_at IS NULL OR length(expires_at) BETWEEN 20 AND 40),
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    CHECK(
        (scope_type = 'personal' AND owner_principal_id IS NOT NULL AND scope_id = owner_principal_id)
        OR (scope_type = 'team' AND owner_principal_id IS NULL AND scope_id = team_id)
        OR (scope_type IN ('project', 'repo', 'agent', 'session') AND owner_principal_id IS NOT NULL)
    ),
    CHECK(scope_type <> 'session' OR expires_at IS NOT NULL),
    CHECK(retention <> 'session' OR expires_at IS NOT NULL),
    UNIQUE(object_id, team_id, scope_type, scope_id, generation)
);

CREATE INDEX idx_team_object_scope_candidates
    ON team_object_registry(team_id, scope_type, scope_id, lifecycle, owner_principal_id);
CREATE INDEX idx_team_object_owner_candidates
    ON team_object_registry(team_id, owner_principal_id, lifecycle, scope_type);

CREATE TRIGGER team_object_registry_identity_immutable
BEFORE UPDATE OF object_id, store_id, team_id, object_kind, scope_type, scope_id,
                 owner_principal_id, author_principal_id, retention, expires_at, created_at
ON team_object_registry
BEGIN SELECT RAISE(ABORT, 'canonical object identity and visibility are immutable'); END;

CREATE TRIGGER team_object_registry_generation_contract
BEFORE UPDATE OF lifecycle, generation ON team_object_registry
WHEN (
       OLD.lifecycle = 'active'
   AND NEW.lifecycle = 'tombstoned'
   AND NEW.generation <> OLD.generation + 1
) OR (
       NOT (OLD.lifecycle = 'active' AND NEW.lifecycle = 'tombstoned')
   AND NEW.generation <> OLD.generation
)
BEGIN SELECT RAISE(ABORT, 'object generation must advance exactly once at tombstone'); END;

CREATE TRIGGER team_object_registry_lifecycle_forward_only
BEFORE UPDATE OF lifecycle ON team_object_registry
WHEN NEW.lifecycle <> OLD.lifecycle AND NOT (
       (OLD.lifecycle = 'active' AND NEW.lifecycle = 'tombstoned')
    OR (OLD.lifecycle = 'tombstoned' AND NEW.lifecycle = 'cleaning')
    OR (OLD.lifecycle = 'cleaning' AND NEW.lifecycle IN ('tombstoned', 'cleanup_failed', 'complete'))
    OR (OLD.lifecycle = 'cleanup_failed' AND NEW.lifecycle = 'cleaning')
)
BEGIN SELECT RAISE(ABORT, 'object lifecycle cannot move backward'); END;

CREATE TRIGGER team_object_registry_project_scope_exists
BEFORE INSERT ON team_object_registry
WHEN NEW.scope_type = 'project' AND NOT EXISTS (
    SELECT 1 FROM team_projects
     WHERE project_id = NEW.scope_id AND team_id = NEW.team_id
)
BEGIN SELECT RAISE(ABORT, 'project scope is not in this team'); END;

CREATE TRIGGER team_object_registry_principals_match_store
BEFORE INSERT ON team_object_registry
WHEN NOT EXISTS (
    SELECT 1 FROM team_principals
     WHERE principal_id = NEW.author_principal_id AND store_id = NEW.store_id
) OR (
    NEW.owner_principal_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM team_principals
         WHERE principal_id = NEW.owner_principal_id AND store_id = NEW.store_id
    )
)
BEGIN SELECT RAISE(ABORT, 'object principals are not in this store'); END;

CREATE TABLE team_object_storage_map (
    object_id            TEXT NOT NULL,
    team_id              TEXT NOT NULL,
    scope_type           TEXT NOT NULL,
    scope_id             TEXT NOT NULL,
    generation           INTEGER NOT NULL CHECK(generation >= 1),
    representation_kind  TEXT NOT NULL CHECK(length(representation_kind) BETWEEN 1 AND 64),
    storage_key          TEXT NOT NULL CHECK(
                            length(storage_key) BETWEEN 1 AND 255
                            AND storage_key NOT GLOB '*[^A-Za-z0-9._:-]*'
                         ),
    created_at           TEXT NOT NULL,
    PRIMARY KEY(object_id, representation_kind, storage_key),
    UNIQUE(representation_kind, storage_key),
    FOREIGN KEY(object_id) REFERENCES team_object_registry(object_id)
);
CREATE INDEX idx_team_object_storage_partition
    ON team_object_storage_map(team_id, scope_type, scope_id, generation);

CREATE TRIGGER team_object_storage_map_generation_fence_insert
BEFORE INSERT ON team_object_storage_map
WHEN NOT EXISTS (
    SELECT 1 FROM team_object_registry object
     WHERE object.object_id = NEW.object_id
       AND object.team_id = NEW.team_id
       AND object.scope_type = NEW.scope_type
       AND object.scope_id = NEW.scope_id
       AND object.generation = NEW.generation
       AND object.lifecycle = 'active'
)
BEGIN SELECT RAISE(ABORT, 'storage attachment generation is stale'); END;

CREATE TRIGGER team_object_storage_map_generation_fence_update
BEFORE UPDATE ON team_object_storage_map
WHEN NOT EXISTS (
    SELECT 1 FROM team_object_registry object
     WHERE object.object_id = NEW.object_id
       AND object.team_id = NEW.team_id
       AND object.scope_type = NEW.scope_type
       AND object.scope_id = NEW.scope_id
       AND object.generation = NEW.generation
       AND object.lifecycle = 'active'
)
BEGIN SELECT RAISE(ABORT, 'storage attachment generation is stale'); END;

CREATE TABLE team_object_contributions (
    parent_object_id       TEXT NOT NULL,
    derivative_object_id   TEXT NOT NULL,
    team_id                TEXT NOT NULL,
    scope_type             TEXT NOT NULL,
    scope_id               TEXT NOT NULL,
    parent_generation      INTEGER NOT NULL CHECK(parent_generation >= 1),
    derivative_generation  INTEGER NOT NULL CHECK(derivative_generation >= 1),
    created_at             TEXT NOT NULL,
    PRIMARY KEY(parent_object_id, derivative_object_id),
    CHECK(parent_object_id <> derivative_object_id),
    FOREIGN KEY(parent_object_id) REFERENCES team_object_registry(object_id),
    FOREIGN KEY(derivative_object_id) REFERENCES team_object_registry(object_id)
);
CREATE INDEX idx_team_object_contributions_derivative
    ON team_object_contributions(derivative_object_id, team_id, scope_type, scope_id);

CREATE TRIGGER team_object_contributions_immutable
BEFORE UPDATE ON team_object_contributions
BEGIN SELECT RAISE(ABORT, 'contribution lineage is immutable'); END;

CREATE TRIGGER team_object_contributions_no_cycle
BEFORE INSERT ON team_object_contributions
WHEN EXISTS (
    WITH RECURSIVE descendants(object_id) AS (
        SELECT derivative_object_id
          FROM team_object_contributions
         WHERE parent_object_id = NEW.derivative_object_id
        UNION
        SELECT contribution.derivative_object_id
          FROM team_object_contributions contribution
          JOIN descendants ON contribution.parent_object_id = descendants.object_id
    )
    SELECT 1 FROM descendants WHERE object_id = NEW.parent_object_id
)
BEGIN SELECT RAISE(ABORT, 'contribution cycle is forbidden'); END;

CREATE TRIGGER team_object_contributions_generation_fence_insert
BEFORE INSERT ON team_object_contributions
WHEN NOT EXISTS (
    SELECT 1 FROM team_object_registry parent
     WHERE parent.object_id = NEW.parent_object_id
       AND parent.team_id = NEW.team_id
       AND parent.scope_type = NEW.scope_type
       AND parent.scope_id = NEW.scope_id
       AND parent.generation = NEW.parent_generation
       AND parent.lifecycle = 'active'
) OR NOT EXISTS (
    SELECT 1 FROM team_object_registry derivative
     WHERE derivative.object_id = NEW.derivative_object_id
       AND derivative.team_id = NEW.team_id
       AND derivative.scope_type = NEW.scope_type
       AND derivative.scope_id = NEW.scope_id
       AND derivative.generation = NEW.derivative_generation
       AND derivative.lifecycle = 'active'
)
BEGIN SELECT RAISE(ABORT, 'contribution generation or scope is stale'); END;

CREATE TRIGGER team_object_contributions_generation_fence_update
BEFORE UPDATE ON team_object_contributions
WHEN NOT EXISTS (
    SELECT 1 FROM team_object_registry parent
     WHERE parent.object_id = NEW.parent_object_id
       AND parent.team_id = NEW.team_id
       AND parent.scope_type = NEW.scope_type
       AND parent.scope_id = NEW.scope_id
       AND parent.generation = NEW.parent_generation
       AND parent.lifecycle = 'active'
) OR NOT EXISTS (
    SELECT 1 FROM team_object_registry derivative
     WHERE derivative.object_id = NEW.derivative_object_id
       AND derivative.team_id = NEW.team_id
       AND derivative.scope_type = NEW.scope_type
       AND derivative.scope_id = NEW.scope_id
       AND derivative.generation = NEW.derivative_generation
       AND derivative.lifecycle = 'active'
)
BEGIN SELECT RAISE(ABORT, 'contribution generation or scope is stale'); END;

CREATE TABLE team_service_object_grants (
    grant_id               TEXT PRIMARY KEY,
    team_id                TEXT NOT NULL REFERENCES team_stores(team_id),
    service_principal_id   TEXT NOT NULL REFERENCES team_principals(principal_id),
    object_kind            TEXT NOT NULL CHECK(length(object_kind) BETWEEN 1 AND 64),
    action                 TEXT NOT NULL CHECK(action IN ('read', 'write')),
    scope_type             TEXT NOT NULL
                           CHECK(scope_type IN ('personal', 'team', 'project', 'repo', 'agent', 'session')),
    scope_id               TEXT NOT NULL CHECK(length(scope_id) BETWEEN 1 AND 255),
    status                 TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active', 'revoked')),
    auth_epoch             INTEGER NOT NULL DEFAULT 1 CHECK(auth_epoch >= 1),
    created_at             TEXT NOT NULL,
    revoked_at             TEXT,
    CHECK(NOT (action = 'write' AND scope_type IN ('personal', 'team'))),
    UNIQUE(team_id, service_principal_id, object_kind, action, scope_type, scope_id)
);
CREATE INDEX idx_team_service_grants_lookup
    ON team_service_object_grants(team_id, service_principal_id, action, scope_type, scope_id, status, object_kind);

CREATE TRIGGER team_service_object_grants_principal_kind
BEFORE INSERT ON team_service_object_grants
WHEN NOT EXISTS (
    SELECT 1
      FROM team_principals principal
      JOIN team_memberships membership
        ON membership.principal_id = principal.principal_id
       AND membership.team_id = NEW.team_id
       AND membership.status = 'active'
     WHERE principal.principal_id = NEW.service_principal_id
       AND principal.kind = 'service'
       AND principal.status = 'active'
)
BEGIN SELECT RAISE(ABORT, 'active service principal is required'); END;

CREATE TRIGGER team_service_object_grants_principal_kind_update
BEFORE UPDATE OF team_id, service_principal_id ON team_service_object_grants
WHEN NOT EXISTS (
    SELECT 1
      FROM team_principals principal
      JOIN team_memberships membership
        ON membership.principal_id = principal.principal_id
       AND membership.team_id = NEW.team_id
       AND membership.status = 'active'
     WHERE principal.principal_id = NEW.service_principal_id
       AND principal.kind = 'service'
       AND principal.status = 'active'
)
BEGIN SELECT RAISE(ABORT, 'active service principal is required'); END;

CREATE TRIGGER team_service_object_grants_auth_epoch_monotonic
BEFORE UPDATE OF auth_epoch ON team_service_object_grants
WHEN NEW.auth_epoch < OLD.auth_epoch
BEGIN SELECT RAISE(ABORT, 'auth epoch is monotonic'); END;

CREATE TRIGGER team_service_object_grants_scope_insert
BEFORE INSERT ON team_service_object_grants
WHEN (NEW.scope_type = 'team' AND NEW.scope_id <> NEW.team_id)
  OR (NEW.scope_type = 'project' AND NOT EXISTS (
      SELECT 1 FROM team_projects project
       WHERE project.project_id = NEW.scope_id AND project.team_id = NEW.team_id
  ))
  OR (NEW.scope_type = 'personal' AND NOT EXISTS (
      SELECT 1
        FROM team_principals principal
        JOIN team_memberships membership
          ON membership.principal_id = principal.principal_id
         AND membership.team_id = NEW.team_id
       WHERE principal.principal_id = NEW.scope_id
         AND principal.kind = 'human'
  ))
BEGIN SELECT RAISE(ABORT, 'service object grant scope is not canonical'); END;

CREATE TRIGGER team_service_object_grants_scope_update
BEFORE UPDATE OF team_id, scope_type, scope_id ON team_service_object_grants
WHEN (NEW.scope_type = 'team' AND NEW.scope_id <> NEW.team_id)
  OR (NEW.scope_type = 'project' AND NOT EXISTS (
      SELECT 1 FROM team_projects project
       WHERE project.project_id = NEW.scope_id AND project.team_id = NEW.team_id
  ))
  OR (NEW.scope_type = 'personal' AND NOT EXISTS (
      SELECT 1
        FROM team_principals principal
        JOIN team_memberships membership
          ON membership.principal_id = principal.principal_id
         AND membership.team_id = NEW.team_id
       WHERE principal.principal_id = NEW.scope_id
         AND principal.kind = 'human'
  ))
BEGIN SELECT RAISE(ABORT, 'service object grant scope is not canonical'); END;

CREATE TRIGGER team_service_object_grants_policy_insert
AFTER INSERT ON team_service_object_grants
BEGIN
    UPDATE team_policy_metadata
       SET policy_epoch = policy_epoch + 1,
           updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
     WHERE team_id = NEW.team_id;
    UPDATE team_stores SET auth_epoch = auth_epoch + 1 WHERE team_id = NEW.team_id;
END;

CREATE TRIGGER team_service_object_grants_policy_update
AFTER UPDATE ON team_service_object_grants
BEGIN
    UPDATE team_policy_metadata
       SET policy_epoch = policy_epoch + 1,
           updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
     WHERE team_id = NEW.team_id;
    UPDATE team_stores SET auth_epoch = auth_epoch + 1 WHERE team_id = NEW.team_id;
END;

CREATE TRIGGER team_service_object_grants_policy_delete
AFTER DELETE ON team_service_object_grants
BEGIN
    UPDATE team_policy_metadata
       SET policy_epoch = policy_epoch + 1,
           updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
     WHERE team_id = OLD.team_id;
    UPDATE team_stores SET auth_epoch = auth_epoch + 1 WHERE team_id = OLD.team_id;
END;

CREATE TABLE team_writer_leases (
    store_id        TEXT PRIMARY KEY REFERENCES team_stores(store_id),
    team_id         TEXT NOT NULL UNIQUE REFERENCES team_stores(team_id),
    writer_id       TEXT NOT NULL CHECK(length(writer_id) BETWEEN 1 AND 255),
    runtime_mode    TEXT NOT NULL CHECK(runtime_mode IN ('team-remote', 'local-legacy')),
    writer_version  INTEGER NOT NULL CHECK(writer_version >= 1),
    lease_token_hash TEXT NOT NULL UNIQUE
                     CHECK(length(lease_token_hash) = 64 AND lease_token_hash NOT GLOB '*[^0-9a-f]*'),
    acquired_at     TEXT NOT NULL,
    heartbeat_at    TEXT NOT NULL,
    expires_at      TEXT NOT NULL
);
CREATE INDEX idx_team_writer_leases_expiry ON team_writer_leases(expires_at);

CREATE TABLE team_idempotency_records (
    team_id              TEXT NOT NULL REFERENCES team_stores(team_id),
    principal_id         TEXT NOT NULL REFERENCES team_principals(principal_id),
    client_key           TEXT NOT NULL
                         CHECK(length(client_key) = 64 AND client_key NOT GLOB '*[^0-9a-f]*'),
    action               TEXT NOT NULL CHECK(length(action) BETWEEN 1 AND 64),
    idempotency_key_hash TEXT NOT NULL CHECK(
                            length(idempotency_key_hash) = 64
                            AND idempotency_key_hash NOT GLOB '*[^0-9a-f]*'
                         ),
    body_digest          TEXT NOT NULL CHECK(
                            length(body_digest) = 64
                            AND body_digest NOT GLOB '*[^0-9a-f]*'
                         ),
    state                TEXT NOT NULL CHECK(state IN ('pending', 'stored', 'failed')),
    object_id            TEXT REFERENCES team_object_registry(object_id),
    audit_event_id       TEXT REFERENCES team_audit_events(event_id),
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL,
    CHECK(
           (state = 'pending' AND object_id IS NULL AND audit_event_id IS NULL)
        OR (state = 'stored' AND object_id IS NOT NULL AND audit_event_id IS NOT NULL)
        OR (state = 'failed' AND object_id IS NULL AND audit_event_id IS NOT NULL)
    ),
    PRIMARY KEY(team_id, principal_id, client_key, action, idempotency_key_hash)
) WITHOUT ROWID;

CREATE TABLE team_projection_jobs (
    job_id           TEXT PRIMARY KEY,
    store_id         TEXT NOT NULL REFERENCES team_stores(store_id),
    team_id          TEXT NOT NULL,
    root_object_id   TEXT NOT NULL,
    root_generation  INTEGER NOT NULL CHECK(root_generation >= 1),
    scope_type       TEXT NOT NULL,
    scope_id         TEXT NOT NULL,
    projection_kind  TEXT NOT NULL CHECK(length(projection_kind) BETWEEN 1 AND 64),
    state            TEXT NOT NULL DEFAULT 'pending'
                     CHECK(state IN ('pending', 'leased', 'ready', 'failed', 'cancelled')),
    attempt_count    INTEGER NOT NULL DEFAULT 0 CHECK(attempt_count >= 0),
    lease_token_hash TEXT CHECK(
                         lease_token_hash IS NULL OR (
                             length(lease_token_hash) = 64
                             AND lease_token_hash NOT GLOB '*[^0-9a-f]*'
                         )
                     ),
    terminal_lease_token_hash TEXT CHECK(
                         terminal_lease_token_hash IS NULL OR (
                             length(terminal_lease_token_hash) = 64
                             AND terminal_lease_token_hash NOT GLOB '*[^0-9a-f]*'
                         )
                     ),
    completion_digest TEXT CHECK(
                         completion_digest IS NULL OR (
                             length(completion_digest) = 64
                             AND completion_digest NOT GLOB '*[^0-9a-f]*'
                         )
                     ),
    lease_expires_at TEXT CHECK(lease_expires_at IS NULL OR length(lease_expires_at) BETWEEN 20 AND 40),
    next_attempt_at  TEXT CHECK(next_attempt_at IS NULL OR length(next_attempt_at) BETWEEN 20 AND 40),
    last_error_code  TEXT CHECK(
        last_error_code IS NULL OR (
            length(last_error_code) BETWEEN 1 AND 64
            AND last_error_code NOT GLOB '*[^a-z0-9_.:-]*'
        )
    ),
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    FOREIGN KEY(root_object_id) REFERENCES team_object_registry(object_id),
    UNIQUE(root_object_id, root_generation, projection_kind),
    CHECK(
           (state = 'leased' AND attempt_count >= 1
             AND lease_token_hash IS NOT NULL AND lease_expires_at IS NOT NULL
             AND terminal_lease_token_hash IS NULL AND completion_digest IS NULL
             AND next_attempt_at IS NULL AND last_error_code IS NULL)
        OR (state = 'pending'
             AND lease_token_hash IS NULL AND lease_expires_at IS NULL
             AND terminal_lease_token_hash IS NULL AND completion_digest IS NULL
             AND next_attempt_at IS NOT NULL AND last_error_code IS NULL)
        OR (state = 'failed' AND attempt_count >= 1
             AND lease_token_hash IS NULL AND lease_expires_at IS NULL
             AND terminal_lease_token_hash IS NOT NULL AND completion_digest IS NULL
             AND next_attempt_at IS NOT NULL
             AND last_error_code IN (
                 'dependency_timeout', 'dependency_unavailable', 'rate_limited',
                 'storage_unavailable', 'worker_interrupted',
                 'materialization_failed', 'temporary_failure'
             ))
        OR (state = 'ready'
             AND lease_token_hash IS NULL AND lease_expires_at IS NULL
             AND terminal_lease_token_hash IS NOT NULL AND completion_digest IS NOT NULL
             AND next_attempt_at IS NULL AND last_error_code IS NULL)
        OR (state = 'cancelled'
             AND lease_token_hash IS NULL AND lease_expires_at IS NULL
             AND terminal_lease_token_hash IS NULL AND completion_digest IS NULL
             AND next_attempt_at IS NULL
             AND last_error_code IN (
                 'root_tombstoned', 'root_deleted', 'generation_superseded'
             ))
    )
);
CREATE INDEX idx_team_projection_jobs_claim
    ON team_projection_jobs(team_id, state, projection_kind, next_attempt_at, lease_expires_at, created_at);
CREATE INDEX idx_team_projection_jobs_root
    ON team_projection_jobs(root_object_id, root_generation, state);

CREATE TRIGGER team_projection_jobs_generation_fence_insert
BEFORE INSERT ON team_projection_jobs
WHEN NOT EXISTS (
    SELECT 1 FROM team_object_registry root
     WHERE root.object_id = NEW.root_object_id
       AND root.store_id = NEW.store_id
       AND root.team_id = NEW.team_id
       AND root.scope_type = NEW.scope_type
       AND root.scope_id = NEW.scope_id
       AND root.generation = NEW.root_generation
       AND root.lifecycle = 'active'
)
BEGIN SELECT RAISE(ABORT, 'projection job generation is stale'); END;

CREATE TRIGGER team_projection_jobs_active_generation_on_state
BEFORE UPDATE OF state, lease_token_hash, lease_expires_at ON team_projection_jobs
WHEN NEW.state IN ('leased', 'ready') AND NOT EXISTS (
    SELECT 1 FROM team_object_registry root
     WHERE root.object_id = NEW.root_object_id
       AND root.store_id = NEW.store_id
       AND root.team_id = NEW.team_id
       AND root.scope_type = NEW.scope_type
       AND root.scope_id = NEW.scope_id
       AND root.generation = NEW.root_generation
       AND root.lifecycle = 'active'
)
BEGIN SELECT RAISE(ABORT, 'projection job cannot lease or complete a stale root'); END;

CREATE TRIGGER team_projection_jobs_cancel_only_tombstoned
BEFORE UPDATE OF state ON team_projection_jobs
WHEN NEW.state = 'cancelled' AND NOT EXISTS (
    SELECT 1 FROM team_object_registry root
     WHERE root.object_id = NEW.root_object_id
       AND root.store_id = NEW.store_id
       AND root.team_id = NEW.team_id
       AND root.scope_type = NEW.scope_type
       AND root.scope_id = NEW.scope_id
       AND root.lifecycle = 'tombstoned'
       AND root.generation = NEW.root_generation + 1
)
BEGIN SELECT RAISE(ABORT, 'projection job cancellation requires its tombstoned root'); END;

CREATE TRIGGER team_projection_jobs_terminal_immutable
BEFORE UPDATE ON team_projection_jobs
WHEN OLD.state IN ('ready', 'cancelled')
BEGIN SELECT RAISE(ABORT, 'terminal projection job is immutable'); END;

-- A job row is the durable unresolved projection intent. Outputs are attached
-- only when a worker materializes canonical same-scope derivatives.
CREATE TABLE team_projection_outputs (
    job_id                 TEXT NOT NULL REFERENCES team_projection_jobs(job_id),
    derivative_object_id   TEXT NOT NULL REFERENCES team_object_registry(object_id),
    derivative_generation  INTEGER NOT NULL CHECK(derivative_generation >= 1),
    created_at              TEXT NOT NULL,
    PRIMARY KEY(job_id, derivative_object_id)
);
CREATE INDEX idx_team_projection_outputs_derivative
    ON team_projection_outputs(derivative_object_id, derivative_generation, job_id);

CREATE TRIGGER team_projection_outputs_generation_fence_insert
BEFORE INSERT ON team_projection_outputs
WHEN NOT EXISTS (
    SELECT 1
      FROM team_projection_jobs job
      JOIN team_object_registry root
        ON root.object_id = job.root_object_id
       AND root.store_id = job.store_id
       AND root.team_id = job.team_id
       AND root.scope_type = job.scope_type
       AND root.scope_id = job.scope_id
       AND root.generation = job.root_generation
       AND root.lifecycle = 'active'
      JOIN team_object_registry derivative
        ON derivative.object_id = NEW.derivative_object_id
       AND derivative.object_id <> job.root_object_id
       AND derivative.team_id = job.team_id
       AND derivative.scope_type = job.scope_type
       AND derivative.scope_id = job.scope_id
       AND derivative.generation = NEW.derivative_generation
       AND derivative.lifecycle = 'active'
     WHERE job.job_id = NEW.job_id
	   AND job.state = 'leased'
)
BEGIN SELECT RAISE(ABORT, 'projection output generation or scope is stale'); END;

CREATE TRIGGER team_projection_outputs_immutable
BEFORE UPDATE ON team_projection_outputs
BEGIN SELECT RAISE(ABORT, 'projection output lineage is immutable'); END;

CREATE TRIGGER team_projection_jobs_generation_fence_update
BEFORE UPDATE OF store_id, team_id, root_object_id, root_generation, scope_type, scope_id
ON team_projection_jobs
WHEN NOT EXISTS (
    SELECT 1 FROM team_object_registry root
     WHERE root.object_id = NEW.root_object_id
       AND root.store_id = NEW.store_id
       AND root.team_id = NEW.team_id
       AND root.scope_type = NEW.scope_type
       AND root.scope_id = NEW.scope_id
       AND root.generation = NEW.root_generation
       AND root.lifecycle = 'active'
)
BEGIN SELECT RAISE(ABORT, 'projection job generation is stale'); END;

-- Once the canonical policy schema is present, readers and writers older than
-- this migration are never valid rollback targets for a marked team store.
UPDATE team_stores
   SET min_reader_version = CASE WHEN min_reader_version < 34 THEN 34 ELSE min_reader_version END,
       min_writer_version = CASE WHEN min_writer_version < 34 THEN 34 ELSE min_writer_version END;
