-- Durable, metadata-only deletion operations for contribution-aware team
-- cleanup. The synchronous mutation tombstones the root generation before it
-- creates an operation; leased workers may then retry cleanup without ever
-- making the root readable again.

DROP TRIGGER team_policy_metadata_after_store_insert;

CREATE TRIGGER team_policy_metadata_after_store_insert
AFTER INSERT ON team_stores
BEGIN
    INSERT INTO team_policy_metadata(
        store_id, team_id, policy_version, schema_version,
        policy_epoch, global_epoch, real_content_state, updated_at)
    VALUES (
        NEW.store_id, NEW.team_id, 1, 38,
        1, NEW.auth_epoch, 'blocked', NEW.created_at);
END;

UPDATE team_policy_metadata
   SET schema_version = 38,
       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
 WHERE schema_version < 38;

CREATE TABLE team_deletion_operations (
    operation_id              TEXT PRIMARY KEY CHECK(
                                  length(operation_id) BETWEEN 1 AND 255
                                  AND operation_id NOT GLOB '*[^A-Za-z0-9._:-]*'
                              ),
    store_id                 TEXT NOT NULL REFERENCES team_stores(store_id),
    team_id                  TEXT NOT NULL REFERENCES team_stores(team_id),
    root_object_id           TEXT NOT NULL REFERENCES team_object_registry(object_id),
    root_generation          INTEGER NOT NULL CHECK(root_generation >= 1),
    actor_principal_id       TEXT NOT NULL REFERENCES team_principals(principal_id),
    oauth_client_key         TEXT NOT NULL CHECK(
                                  oauth_client_key = '' OR (
                                      length(oauth_client_key) = 64
                                      AND oauth_client_key NOT GLOB '*[^0-9a-f]*'
                                  )
                              ),
    request_id               TEXT NOT NULL CHECK(
                                  length(request_id) BETWEEN 1 AND 255
                                  AND request_id NOT GLOB '*[^A-Za-z0-9._:-]*'
                              ),
    idempotency_key_hash     TEXT NOT NULL CHECK(
                                  length(idempotency_key_hash) = 64
                                  AND idempotency_key_hash NOT GLOB '*[^0-9a-f]*'
                              ),
    body_digest              TEXT NOT NULL CHECK(
                                  length(body_digest) = 64
                                  AND body_digest NOT GLOB '*[^0-9a-f]*'
                              ),
    start_audit_event_id     TEXT NOT NULL UNIQUE REFERENCES team_audit_events(event_id),
    completion_audit_event_id TEXT UNIQUE REFERENCES team_audit_events(event_id),
    state                    TEXT NOT NULL CHECK(
                                  state IN ('pending', 'leased', 'cleanup_failed', 'complete')
                              ),
    attempt_count            INTEGER NOT NULL DEFAULT 0 CHECK(attempt_count >= 0),
    lease_token_hash         TEXT CHECK(
                                  lease_token_hash IS NULL OR (
                                      length(lease_token_hash) = 64
                                      AND lease_token_hash NOT GLOB '*[^0-9a-f]*'
                                  )
                              ),
    lease_expires_at         TEXT CHECK(
                                  lease_expires_at IS NULL
                                  OR length(lease_expires_at) BETWEEN 20 AND 40
                              ),
    next_attempt_at          TEXT CHECK(
                                  next_attempt_at IS NULL
                                  OR length(next_attempt_at) BETWEEN 20 AND 40
                              ),
    last_error_code          TEXT CHECK(
                                  last_error_code IS NULL OR (
                                      length(last_error_code) BETWEEN 1 AND 64
                                      AND last_error_code NOT GLOB '*[^a-z0-9_.:-]*'
                                  )
                              ),
    started_at               TEXT NOT NULL CHECK(length(started_at) BETWEEN 20 AND 40),
    updated_at               TEXT NOT NULL CHECK(length(updated_at) BETWEEN 20 AND 40),
    completed_at             TEXT CHECK(
                                  completed_at IS NULL
                                  OR length(completed_at) BETWEEN 20 AND 40
                              ),
    UNIQUE(team_id, actor_principal_id, oauth_client_key, idempotency_key_hash),
    UNIQUE(root_object_id, root_generation),
    CHECK(
        completion_audit_event_id IS NULL
        OR completion_audit_event_id <> start_audit_event_id
    ),
    CHECK(
           (state = 'pending'
             AND attempt_count = 0
             AND lease_token_hash IS NULL AND lease_expires_at IS NULL
             AND next_attempt_at IS NOT NULL AND last_error_code IS NULL
             AND completion_audit_event_id IS NULL AND completed_at IS NULL)
        OR (state = 'leased'
             AND attempt_count >= 1
             AND lease_token_hash IS NOT NULL AND lease_expires_at IS NOT NULL
             AND next_attempt_at IS NULL AND last_error_code IS NULL
             AND completion_audit_event_id IS NULL AND completed_at IS NULL)
        OR (state = 'cleanup_failed'
             AND attempt_count >= 1
             AND lease_token_hash IS NULL AND lease_expires_at IS NULL
             AND next_attempt_at IS NOT NULL AND last_error_code IS NOT NULL
             AND completion_audit_event_id IS NULL AND completed_at IS NULL)
        OR (state = 'complete'
             AND attempt_count >= 1
             AND lease_token_hash IS NULL AND lease_expires_at IS NULL
             AND next_attempt_at IS NULL AND last_error_code IS NULL
             AND completion_audit_event_id IS NOT NULL AND completed_at IS NOT NULL)
    )
);

CREATE INDEX idx_team_deletion_operations_claim
    ON team_deletion_operations(
        team_id, state, next_attempt_at, lease_expires_at, started_at, operation_id
    );
CREATE INDEX idx_team_deletion_operations_root
    ON team_deletion_operations(root_object_id, root_generation, state);

CREATE TRIGGER team_deletion_operations_initial_state_insert
BEFORE INSERT ON team_deletion_operations
WHEN NEW.state <> 'pending'
BEGIN SELECT RAISE(ABORT, 'deletion operations must begin pending'); END;

CREATE TRIGGER team_deletion_operations_root_fence_insert
BEFORE INSERT ON team_deletion_operations
WHEN NOT EXISTS (
    SELECT 1
      FROM team_object_registry root
     WHERE root.object_id = NEW.root_object_id
       AND root.store_id = NEW.store_id
       AND root.team_id = NEW.team_id
       AND root.generation = NEW.root_generation + 1
       AND root.lifecycle = 'tombstoned'
)
OR EXISTS (
    SELECT 1 FROM team_object_contributions contribution
     WHERE contribution.derivative_object_id = NEW.root_object_id
)
OR EXISTS (
    SELECT 1
      FROM team_deletion_frontier frontier
      JOIN team_deletion_operations operation
        ON operation.operation_id = frontier.operation_id
     WHERE frontier.object_id = NEW.root_object_id
       AND frontier.depth > 0
       AND operation.state <> 'complete'
)
BEGIN SELECT RAISE(ABORT, 'deletion operation requires a freshly tombstoned root generation'); END;

CREATE TRIGGER team_deletion_operations_actor_client_contract_insert
BEFORE INSERT ON team_deletion_operations
WHEN NOT EXISTS (
    SELECT 1
      FROM team_principals actor
     WHERE actor.principal_id = NEW.actor_principal_id
       AND actor.store_id = NEW.store_id
       AND (
           (NEW.oauth_client_key = '' AND actor.kind = 'human')
           OR (
               NEW.oauth_client_key <> '' AND EXISTS (
                   SELECT 1
                     FROM team_oauth_clients client
                    WHERE client.oauth_client_key = NEW.oauth_client_key
                      AND client.team_id = NEW.team_id
                      AND client.principal_id = NEW.actor_principal_id
               )
           )
       )
)
BEGIN SELECT RAISE(ABORT, 'deletion actor or OAuth client is outside the operation store'); END;

CREATE TRIGGER team_deletion_operations_audit_start_contract_insert
BEFORE INSERT ON team_deletion_operations
WHEN NOT EXISTS (
    SELECT 1
      FROM team_audit_events audit
      JOIN team_object_registry root
        ON root.object_id = NEW.root_object_id
      JOIN team_policy_metadata policy
        ON policy.store_id = NEW.store_id
       AND policy.team_id = NEW.team_id
     WHERE audit.event_id = NEW.start_audit_event_id
       AND audit.store_id = NEW.store_id
       AND audit.team_id = NEW.team_id
       AND audit.actor_principal_id = NEW.actor_principal_id
       AND audit.client_key = NEW.oauth_client_key
       AND audit.request_id = NEW.request_id
       AND audit.action = 'team.object.delete.start'
       AND audit.reason_code = 'deletion_started'
       AND audit.target_kind = root.object_kind
       AND audit.target_id = NEW.root_object_id
       AND audit.outcome = 'allowed'
       AND audit.policy_version = policy.policy_version
       AND audit.auth_epoch = policy.global_epoch
       AND audit.occurred_at = NEW.started_at
       AND (
           (root.scope_type = 'project' AND audit.project_id = root.scope_id)
           OR (
               root.scope_type <> 'project'
               AND (
                   audit.project_id IS NULL
                   OR EXISTS (
                       SELECT 1 FROM team_projects project
                        WHERE project.project_id = audit.project_id
                          AND project.team_id = NEW.team_id
                   )
               )
           )
       )
)
BEGIN SELECT RAISE(ABORT, 'deletion start audit does not match the operation'); END;

CREATE TRIGGER team_deletion_operations_audit_completion_contract_update
BEFORE UPDATE OF state, completion_audit_event_id ON team_deletion_operations
WHEN NEW.state = 'complete' AND NOT EXISTS (
    SELECT 1
      FROM team_audit_events audit
      JOIN team_audit_events start_audit
        ON start_audit.event_id = NEW.start_audit_event_id
      JOIN team_object_registry root
        ON root.object_id = NEW.root_object_id
      JOIN team_audit_event_order completion_order
        ON completion_order.event_id = audit.event_id
       AND completion_order.store_id = audit.store_id
      JOIN team_audit_event_order start_order
        ON start_order.event_id = NEW.start_audit_event_id
       AND start_order.store_id = NEW.store_id
     WHERE audit.event_id = NEW.completion_audit_event_id
       AND audit.store_id = NEW.store_id
       AND audit.team_id = NEW.team_id
       AND audit.actor_principal_id = NEW.actor_principal_id
       AND audit.client_key = NEW.oauth_client_key
       AND audit.request_id = NEW.request_id
       AND audit.action = 'team.object.delete.complete'
       AND audit.reason_code = 'deletion_complete'
       AND audit.target_kind = root.object_kind
       AND audit.target_id = NEW.root_object_id
       AND audit.outcome = 'allowed'
       AND audit.project_id IS start_audit.project_id
       AND audit.policy_version = start_audit.policy_version
       AND audit.auth_epoch = start_audit.auth_epoch
       AND audit.occurred_at = NEW.completed_at
       AND completion_order.audit_sequence > start_order.audit_sequence
)
BEGIN SELECT RAISE(ABORT, 'deletion completion audit does not match the operation'); END;

CREATE TRIGGER team_deletion_operations_completion_barrier
BEFORE UPDATE OF state ON team_deletion_operations
WHEN NEW.state = 'complete' AND (
    NOT EXISTS (
        SELECT 1
          FROM team_object_registry root
         WHERE root.object_id = NEW.root_object_id
           AND root.store_id = NEW.store_id
           AND root.team_id = NEW.team_id
           AND root.generation = NEW.root_generation + 1
           AND root.lifecycle = 'complete'
    )
    OR EXISTS (
        SELECT 1
          FROM team_deletion_frontier unresolved
         WHERE unresolved.operation_id = NEW.operation_id
    )
    OR NOT EXISTS (
        SELECT 1
          FROM team_deletion_discharges root_discharge
         WHERE root_discharge.operation_id = NEW.operation_id
           AND root_discharge.object_id = NEW.root_object_id
           AND root_discharge.object_generation = NEW.root_generation
           AND root_discharge.depth = 0
           AND root_discharge.disposition = 'purged'
    )
    OR EXISTS (
        SELECT 1
          FROM team_deletion_discharges discharge
         WHERE discharge.operation_id = NEW.operation_id
           AND discharge.depth > 0
           AND (
               (
                   discharge.disposition = 'purged'
                   AND (
                       EXISTS (
                           SELECT 1 FROM team_object_registry object
                            WHERE object.object_id = discharge.object_id
                       )
                       OR EXISTS (
                           SELECT 1 FROM team_memory_capsules payload
                            WHERE payload.root_object_id = discharge.object_id
                       )
                       OR EXISTS (
                           SELECT 1 FROM team_memory_events payload
                            WHERE payload.root_object_id = discharge.object_id
                               OR payload.derivative_object_id = discharge.object_id
                       )
                       OR EXISTS (
                           SELECT 1 FROM team_memory_embeddings payload
                            WHERE payload.root_object_id = discharge.object_id
                               OR payload.derivative_object_id = discharge.object_id
                       )
                       OR EXISTS (
                           SELECT 1 FROM team_graph_delta_inputs payload
                            WHERE payload.root_object_id = discharge.object_id
                       )
                       OR EXISTS (
                           SELECT 1 FROM team_semantic_projection_intents payload
                            WHERE payload.root_object_id = discharge.object_id
                               OR payload.derivative_object_id = discharge.object_id
                       )
                       OR EXISTS (
                           SELECT 1 FROM team_semantic_materializations payload
                            WHERE payload.root_object_id = discharge.object_id
                               OR payload.derivative_object_id = discharge.object_id
                       )
                       OR EXISTS (
                           SELECT 1 FROM team_projection_jobs payload
                            WHERE payload.root_object_id = discharge.object_id
                       )
                       OR EXISTS (
                           SELECT 1 FROM team_projection_outputs payload
                            WHERE payload.derivative_object_id = discharge.object_id
                               OR EXISTS (
                                   SELECT 1 FROM team_projection_jobs job
                                    WHERE job.job_id = payload.job_id
                                      AND job.root_object_id = discharge.object_id
                               )
                       )
                       OR EXISTS (
                           SELECT 1 FROM team_object_storage_map payload
                            WHERE payload.object_id = discharge.object_id
                       )
                       OR EXISTS (
                           SELECT 1 FROM team_object_contributions payload
                            WHERE payload.parent_object_id = discharge.object_id
                               OR payload.derivative_object_id = discharge.object_id
                       )
                   )
               )
               OR (
                   discharge.disposition = 'preserved'
                   AND NOT EXISTS (
                       SELECT 1
                         FROM team_object_registry object
                         JOIN team_object_contributions contribution
                           ON contribution.derivative_object_id = object.object_id
                          AND contribution.derivative_generation = object.generation
                          AND contribution.team_id = object.team_id
                          AND contribution.scope_type = object.scope_type
                          AND contribution.scope_id = object.scope_id
                         JOIN team_object_registry parent
                           ON parent.object_id = contribution.parent_object_id
                          AND parent.team_id = contribution.team_id
                          AND parent.scope_type = contribution.scope_type
                          AND parent.scope_id = contribution.scope_id
                          AND parent.generation = contribution.parent_generation
                        WHERE object.object_id = discharge.object_id
                          AND object.store_id = NEW.store_id
                          AND object.team_id = NEW.team_id
                          AND object.generation = discharge.object_generation
                          AND object.lifecycle = 'active'
                          AND parent.lifecycle = 'active'
                          AND (
                              parent.expires_at IS NULL
                              OR julianday(parent.expires_at) > julianday(NEW.completed_at)
                          )
                   )
               )
           )
    )
    OR EXISTS (
        SELECT 1 FROM team_memory_capsules payload
         WHERE payload.root_object_id = NEW.root_object_id
    )
    OR EXISTS (
        SELECT 1 FROM team_memory_events payload
         WHERE payload.root_object_id = NEW.root_object_id
            OR payload.derivative_object_id = NEW.root_object_id
    )
    OR EXISTS (
        SELECT 1 FROM team_memory_embeddings payload
         WHERE payload.root_object_id = NEW.root_object_id
            OR payload.derivative_object_id = NEW.root_object_id
    )
    OR EXISTS (
        SELECT 1 FROM team_graph_delta_inputs payload
         WHERE payload.root_object_id = NEW.root_object_id
    )
    OR EXISTS (
        SELECT 1 FROM team_semantic_projection_intents payload
         WHERE payload.root_object_id = NEW.root_object_id
            OR payload.derivative_object_id = NEW.root_object_id
    )
    OR EXISTS (
        SELECT 1 FROM team_semantic_materializations payload
         WHERE payload.root_object_id = NEW.root_object_id
            OR payload.derivative_object_id = NEW.root_object_id
    )
    OR EXISTS (
        SELECT 1 FROM team_projection_jobs payload
         WHERE payload.root_object_id = NEW.root_object_id
    )
    OR EXISTS (
        SELECT 1 FROM team_projection_outputs payload
         WHERE payload.derivative_object_id = NEW.root_object_id
            OR EXISTS (
                SELECT 1 FROM team_projection_jobs job
                 WHERE job.job_id = payload.job_id
                   AND job.root_object_id = NEW.root_object_id
            )
    )
    OR EXISTS (
        SELECT 1 FROM team_object_storage_map payload
         WHERE payload.object_id = NEW.root_object_id
    )
    OR EXISTS (
        SELECT 1 FROM team_object_contributions payload
         WHERE payload.parent_object_id = NEW.root_object_id
            OR payload.derivative_object_id = NEW.root_object_id
    )
)
BEGIN SELECT RAISE(ABORT, 'deletion completion barrier is not satisfied'); END;

CREATE TRIGGER team_deletion_operations_identity_immutable
BEFORE UPDATE OF operation_id, store_id, team_id, root_object_id, root_generation,
                 actor_principal_id, oauth_client_key, request_id,
                 idempotency_key_hash, body_digest, start_audit_event_id, started_at
ON team_deletion_operations
BEGIN SELECT RAISE(ABORT, 'deletion operation identity is immutable'); END;

CREATE TRIGGER team_deletion_operations_state_forward_only
BEFORE UPDATE OF state ON team_deletion_operations
WHEN NEW.state <> OLD.state AND NOT (
       (OLD.state = 'pending' AND NEW.state = 'leased')
    OR (OLD.state = 'leased' AND NEW.state IN ('cleanup_failed', 'complete'))
    OR (OLD.state = 'cleanup_failed' AND NEW.state = 'leased')
)
BEGIN SELECT RAISE(ABORT, 'deletion operation state cannot move backward'); END;

CREATE TRIGGER team_deletion_operations_attempt_contract
BEFORE UPDATE OF state, attempt_count, lease_token_hash ON team_deletion_operations
WHEN NEW.attempt_count < OLD.attempt_count
  OR (
      NEW.state = 'leased'
      AND (
          NEW.attempt_count <> OLD.attempt_count + 1
          OR (OLD.state = 'leased' AND NEW.lease_token_hash = OLD.lease_token_hash)
      )
  )
  OR (NEW.state <> 'leased' AND NEW.attempt_count <> OLD.attempt_count)
BEGIN SELECT RAISE(ABORT, 'deletion lease attempts must advance exactly once'); END;

CREATE TRIGGER team_deletion_operations_complete_immutable
BEFORE UPDATE ON team_deletion_operations
WHEN OLD.state = 'complete'
BEGIN SELECT RAISE(ABORT, 'completed deletion operations are immutable'); END;

CREATE TRIGGER team_deletion_operations_no_delete
BEFORE DELETE ON team_deletion_operations
BEGIN SELECT RAISE(ABORT, 'deletion operations are durable'); END;

CREATE TABLE team_deletion_frontier (
    operation_id      TEXT NOT NULL REFERENCES team_deletion_operations(operation_id),
    object_id         TEXT NOT NULL REFERENCES team_object_registry(object_id),
    object_generation INTEGER NOT NULL CHECK(object_generation >= 1),
    depth             INTEGER NOT NULL CHECK(depth >= 0),
    discovered_at     TEXT NOT NULL CHECK(length(discovered_at) BETWEEN 20 AND 40),
    PRIMARY KEY(operation_id, object_id)
) WITHOUT ROWID;

CREATE INDEX idx_team_deletion_frontier_object
    ON team_deletion_frontier(object_id, object_generation, operation_id);
CREATE INDEX idx_team_deletion_frontier_depth
    ON team_deletion_frontier(operation_id, depth, object_id);

-- Frontier rows are transient work claims, but their discharge proof is
-- durable. This prevents a buggy/future worker from deleting lineage evidence
-- and falsely declaring completion while a descendant still exists.
CREATE TABLE team_deletion_discharges (
    operation_id      TEXT NOT NULL REFERENCES team_deletion_operations(operation_id),
    object_id         TEXT NOT NULL CHECK(
                          length(object_id) BETWEEN 1 AND 255
                          AND object_id NOT GLOB '*[^A-Za-z0-9._:-]*'
                      ),
    object_generation INTEGER NOT NULL CHECK(object_generation >= 1),
    depth             INTEGER NOT NULL CHECK(depth >= 0),
    disposition       TEXT NOT NULL CHECK(disposition IN ('purged', 'preserved')),
    discharged_at     TEXT NOT NULL CHECK(
                          length(discharged_at) BETWEEN 20 AND 40
                          AND julianday(discharged_at) IS NOT NULL
                      ),
    PRIMARY KEY(operation_id, object_id)
) WITHOUT ROWID;

CREATE INDEX idx_team_deletion_discharges_object
    ON team_deletion_discharges(object_id, object_generation, operation_id);

CREATE TRIGGER team_deletion_discharges_contract_insert
BEFORE INSERT ON team_deletion_discharges
WHEN NOT EXISTS (
    SELECT 1
      FROM team_deletion_frontier frontier
      JOIN team_deletion_operations operation
        ON operation.operation_id = frontier.operation_id
     WHERE frontier.operation_id = NEW.operation_id
       AND frontier.object_id = NEW.object_id
       AND frontier.object_generation = NEW.object_generation
       AND frontier.depth = NEW.depth
       AND operation.state <> 'complete'
       AND (
           (
               NEW.depth = 0
               AND NEW.disposition = 'purged'
               AND NEW.object_id = operation.root_object_id
               AND NEW.object_generation = operation.root_generation
               AND EXISTS (
                   SELECT 1 FROM team_object_registry root
                    WHERE root.object_id = NEW.object_id
                      AND root.store_id = operation.store_id
                      AND root.team_id = operation.team_id
                      AND root.generation = NEW.object_generation + 1
                      AND root.lifecycle = 'complete'
               )
           )
           OR (
               NEW.depth > 0
               AND NEW.disposition = 'purged'
               AND EXISTS (
                   SELECT 1 FROM team_object_registry object
                    WHERE object.object_id = NEW.object_id
                      AND object.store_id = operation.store_id
                      AND object.team_id = operation.team_id
                      AND object.generation = NEW.object_generation + 1
                      AND object.lifecycle = 'cleaning'
               )
               AND NOT EXISTS (
                   SELECT 1
                     FROM team_object_contributions contribution
                     JOIN team_object_registry parent
                       ON parent.object_id = contribution.parent_object_id
                      AND parent.team_id = contribution.team_id
                      AND parent.scope_type = contribution.scope_type
                      AND parent.scope_id = contribution.scope_id
                      AND parent.generation = contribution.parent_generation
                    WHERE contribution.derivative_object_id = NEW.object_id
                      AND parent.lifecycle = 'active'
                      AND (
                          parent.expires_at IS NULL
                          OR julianday(parent.expires_at) > julianday(NEW.discharged_at)
                      )
               )
           )
           OR (
               NEW.depth > 0
               AND NEW.disposition = 'preserved'
               AND EXISTS (
                   SELECT 1
                     FROM team_object_registry object
                     JOIN team_object_contributions contribution
                       ON contribution.derivative_object_id = object.object_id
                      AND contribution.derivative_generation = object.generation
                      AND contribution.team_id = object.team_id
                      AND contribution.scope_type = object.scope_type
                      AND contribution.scope_id = object.scope_id
                     JOIN team_object_registry parent
                       ON parent.object_id = contribution.parent_object_id
                      AND parent.team_id = contribution.team_id
                      AND parent.scope_type = contribution.scope_type
                      AND parent.scope_id = contribution.scope_id
                      AND parent.generation = contribution.parent_generation
                    WHERE object.object_id = NEW.object_id
                      AND object.store_id = operation.store_id
                      AND object.team_id = operation.team_id
                      AND object.generation = NEW.object_generation
                      AND object.lifecycle = 'active'
                      AND parent.lifecycle = 'active'
                      AND (
                          parent.expires_at IS NULL
                          OR julianday(parent.expires_at) > julianday(NEW.discharged_at)
                      )
               )
           )
       )
)
BEGIN SELECT RAISE(ABORT, 'deletion discharge does not match a terminal frontier disposition'); END;

CREATE TRIGGER team_deletion_discharges_immutable
BEFORE UPDATE ON team_deletion_discharges
BEGIN SELECT RAISE(ABORT, 'deletion discharge is immutable'); END;

CREATE TRIGGER team_deletion_discharges_no_delete
BEFORE DELETE ON team_deletion_discharges
BEGIN SELECT RAISE(ABORT, 'deletion discharge is durable'); END;

CREATE TRIGGER team_deletion_frontier_discharge_before_delete
BEFORE DELETE ON team_deletion_frontier
WHEN NOT EXISTS (
    SELECT 1 FROM team_deletion_discharges discharge
     WHERE discharge.operation_id = OLD.operation_id
       AND discharge.object_id = OLD.object_id
       AND discharge.object_generation = OLD.object_generation
       AND discharge.depth = OLD.depth
)
BEGIN SELECT RAISE(ABORT, 'deletion frontier requires a durable discharge before removal'); END;

-- An operation may remove a lineage edge only after it has durably captured
-- the exact derivative generation. This turns recursive frontier completeness
-- from an application convention into a database invariant.
CREATE TRIGGER team_object_contributions_deletion_frontier_complete
BEFORE DELETE ON team_object_contributions
WHEN EXISTS (
    SELECT 1
      FROM (
          SELECT operation_id, object_id, object_generation
            FROM team_deletion_frontier
          UNION
          SELECT operation_id, object_id, object_generation
            FROM team_deletion_discharges
      ) parent_evidence
      JOIN team_deletion_operations operation
        ON operation.operation_id = parent_evidence.operation_id
     WHERE parent_evidence.object_id = OLD.parent_object_id
       AND parent_evidence.object_generation = OLD.parent_generation
       AND operation.state <> 'complete'
       AND NOT EXISTS (
           SELECT 1 FROM team_deletion_frontier child_frontier
            WHERE child_frontier.operation_id = parent_evidence.operation_id
              AND child_frontier.object_id = OLD.derivative_object_id
              AND child_frontier.object_generation = OLD.derivative_generation
       )
       AND NOT EXISTS (
           SELECT 1 FROM team_deletion_discharges child_discharge
            WHERE child_discharge.operation_id = parent_evidence.operation_id
              AND child_discharge.object_id = OLD.derivative_object_id
              AND child_discharge.object_generation = OLD.derivative_generation
       )
)
BEGIN SELECT RAISE(ABORT, 'deletion lineage edge was not captured in the operation frontier'); END;

CREATE TRIGGER team_deletion_frontier_root_contract
BEFORE INSERT ON team_deletion_frontier
WHEN NOT EXISTS (
    SELECT 1
      FROM team_deletion_operations operation
     WHERE operation.operation_id = NEW.operation_id
       AND (
           (NEW.depth = 0
             AND NEW.object_id = operation.root_object_id
             AND NEW.object_generation = operation.root_generation)
           OR (
               NEW.depth > 0
               AND NEW.object_id <> operation.root_object_id
               AND EXISTS (
                   SELECT 1
                     FROM team_deletion_frontier parent_frontier
                     JOIN team_object_contributions contribution
                       ON contribution.parent_object_id = parent_frontier.object_id
                      AND contribution.parent_generation = parent_frontier.object_generation
                      AND contribution.derivative_object_id = NEW.object_id
                      AND contribution.derivative_generation = NEW.object_generation
                      AND contribution.team_id = operation.team_id
                    WHERE parent_frontier.operation_id = NEW.operation_id
                      AND parent_frontier.depth = NEW.depth - 1
               )
           )
       )
)
BEGIN SELECT RAISE(ABORT, 'deletion frontier root or depth is invalid'); END;

CREATE TRIGGER team_deletion_frontier_generation_fence_insert
BEFORE INSERT ON team_deletion_frontier
WHEN NOT EXISTS (
    SELECT 1
      FROM team_deletion_operations operation
      JOIN team_object_registry object
        ON object.object_id = NEW.object_id
       AND object.store_id = operation.store_id
       AND object.team_id = operation.team_id
     WHERE operation.operation_id = NEW.operation_id
       AND operation.state <> 'complete'
       AND (
           (NEW.object_id = operation.root_object_id
             AND object.generation = NEW.object_generation + 1
             AND object.lifecycle IN ('tombstoned', 'cleaning', 'cleanup_failed'))
           OR (NEW.object_id <> operation.root_object_id
             AND object.generation = NEW.object_generation
             AND object.lifecycle = 'active')
       )
)
BEGIN SELECT RAISE(ABORT, 'deletion frontier generation is stale'); END;

CREATE TRIGGER team_deletion_frontier_immutable
BEFORE UPDATE ON team_deletion_frontier
BEGIN SELECT RAISE(ABORT, 'deletion frontier is immutable'); END;

-- A reader or writer that predates the tombstone/cleanup operation contract
-- cannot safely participate in the dedicated team store.
UPDATE team_stores
   SET min_reader_version = CASE WHEN min_reader_version < 38 THEN 38 ELSE min_reader_version END,
       min_writer_version = CASE WHEN min_writer_version < 38 THEN 38 ELSE min_writer_version END;
