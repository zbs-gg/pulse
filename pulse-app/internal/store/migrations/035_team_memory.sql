-- Additive team-memory storage and projection schema.
-- Migration 034 is frozen at its pre-U9 fingerprint; all U9 content tables
-- and the schema-floor transition live here.

DROP TRIGGER team_policy_metadata_after_store_insert;

CREATE TRIGGER team_policy_metadata_after_store_insert
AFTER INSERT ON team_stores
BEGIN
    INSERT INTO team_policy_metadata(
        store_id, team_id, policy_version, schema_version,
        policy_epoch, global_epoch, real_content_state, updated_at)
    VALUES (
        NEW.store_id, NEW.team_id, 1, 35,
        1, NEW.auth_epoch, 'blocked', NEW.created_at);
END;

UPDATE team_policy_metadata
   SET schema_version = 35,
       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
 WHERE schema_version < 35;

-- Team capsules are physically separate from the standalone/local v1 table.
-- Scope, policy, identity, lifecycle, and expiry remain canonical on the root;
-- these rows contain only validated host-extracted capsule fields.
CREATE TABLE team_memory_capsules (
    capsule_id          TEXT PRIMARY KEY CHECK(
                            length(capsule_id) BETWEEN 1 AND 255
                            AND capsule_id NOT GLOB '*[^A-Za-z0-9._:-]*'
                        ),
    root_object_id      TEXT NOT NULL,
    team_id             TEXT NOT NULL,
    scope_type          TEXT NOT NULL
                        CHECK(scope_type IN ('personal', 'project', 'repo', 'agent', 'session')),
    scope_id            TEXT NOT NULL CHECK(length(scope_id) BETWEEN 1 AND 255),
    root_generation     INTEGER NOT NULL CHECK(root_generation >= 1),
    item_ordinal        INTEGER NOT NULL CHECK(item_ordinal BETWEEN 0 AND 19),
    schema_version      TEXT NOT NULL CHECK(schema_version = 'pulse.team.memory.v1'),
    source_host         TEXT NOT NULL CHECK(length(source_host) BETWEEN 1 AND 64),
    conversation_scope  TEXT NOT NULL CHECK(length(conversation_scope) BETWEEN 1 AND 64),
    source_timestamp    TEXT NOT NULL CHECK(length(source_timestamp) BETWEEN 20 AND 30),
    kind                TEXT NOT NULL CHECK(length(kind) BETWEEN 1 AND 64),
    redacted_summary    TEXT NOT NULL CHECK(length(redacted_summary) BETWEEN 1 AND 1200),
    confidence          REAL NOT NULL CHECK(confidence BETWEEN 0.0 AND 1.0),
    evidence_hint       TEXT NOT NULL CHECK(length(evidence_hint) BETWEEN 1 AND 64),
    tags_json           TEXT NOT NULL DEFAULT '[]' CHECK(length(CAST(tags_json AS BLOB)) BETWEEN 2 AND 16384),
    created_at          TEXT NOT NULL,
    UNIQUE(root_object_id, item_ordinal),
    FOREIGN KEY(root_object_id) REFERENCES team_object_registry(object_id)
);
CREATE INDEX idx_team_memory_capsules_root
    ON team_memory_capsules(root_object_id, root_generation, item_ordinal);
CREATE INDEX idx_team_memory_capsules_scope
    ON team_memory_capsules(team_id, scope_type, scope_id, root_generation);

CREATE TRIGGER team_memory_capsules_generation_fence_insert
BEFORE INSERT ON team_memory_capsules
WHEN NOT EXISTS (
    SELECT 1 FROM team_object_registry root
     WHERE root.object_id = NEW.root_object_id
       AND root.team_id = NEW.team_id
       AND root.scope_type = NEW.scope_type
       AND root.scope_id = NEW.scope_id
       AND root.generation = NEW.root_generation
       AND root.object_kind = 'memory'
       AND root.lifecycle = 'active'
)
BEGIN SELECT RAISE(ABORT, 'team memory root generation is stale'); END;

CREATE TRIGGER team_memory_capsules_immutable
BEFORE UPDATE ON team_memory_capsules
BEGIN SELECT RAISE(ABORT, 'team memory capsules are immutable'); END;

-- Team event and embedding projections remain physically separate from the
-- standalone events/event_embeddings tables. Their canonical scope and
-- lifecycle are inherited from generation-fenced derivative roots.
CREATE TABLE team_memory_events (
    event_id              TEXT PRIMARY KEY CHECK(
                              length(event_id) BETWEEN 1 AND 255
                              AND event_id NOT GLOB '*[^A-Za-z0-9._:-]*'
                          ),
    derivative_object_id  TEXT NOT NULL UNIQUE REFERENCES team_object_registry(object_id),
    job_id                TEXT NOT NULL REFERENCES team_projection_jobs(job_id),
    root_object_id        TEXT NOT NULL REFERENCES team_object_registry(object_id),
    root_generation       INTEGER NOT NULL CHECK(root_generation >= 1),
    capsule_id            TEXT NOT NULL REFERENCES team_memory_capsules(capsule_id),
    team_id               TEXT NOT NULL,
    scope_type            TEXT NOT NULL CHECK(scope_type IN ('personal', 'project', 'repo', 'agent', 'session')),
    scope_id              TEXT NOT NULL CHECK(length(scope_id) BETWEEN 1 AND 255),
    kind                  TEXT NOT NULL CHECK(length(kind) BETWEEN 1 AND 64),
    redacted_summary      TEXT NOT NULL CHECK(length(redacted_summary) BETWEEN 1 AND 1200),
    source_timestamp      TEXT NOT NULL CHECK(length(source_timestamp) = 24),
    tags_json             TEXT NOT NULL CHECK(
                              json_valid(tags_json) = 1 AND json_type(tags_json) = 'array'
                          ),
    content_digest        TEXT NOT NULL CHECK(
                              length(content_digest) = 64
                              AND content_digest NOT GLOB '*[^0-9a-f]*'
                          ),
    created_at            TEXT NOT NULL,
    UNIQUE(root_object_id, root_generation, capsule_id)
);
CREATE INDEX idx_team_memory_events_scope
    ON team_memory_events(team_id, scope_type, scope_id, root_generation);

CREATE TRIGGER team_memory_events_generation_fence_insert
BEFORE INSERT ON team_memory_events
WHEN NOT EXISTS (
    SELECT 1
      FROM team_projection_jobs job
      JOIN team_object_registry root
        ON root.object_id = job.root_object_id
       AND root.team_id = job.team_id
       AND root.scope_type = job.scope_type
       AND root.scope_id = job.scope_id
       AND root.generation = job.root_generation
       AND root.lifecycle = 'active'
      JOIN team_memory_capsules capsule
        ON capsule.capsule_id = NEW.capsule_id
       AND capsule.root_object_id = root.object_id
       AND capsule.team_id = root.team_id
       AND capsule.scope_type = root.scope_type
       AND capsule.scope_id = root.scope_id
       AND capsule.root_generation = root.generation
      JOIN team_object_registry derivative
        ON derivative.object_id = NEW.derivative_object_id
       AND derivative.team_id = root.team_id
       AND derivative.scope_type = root.scope_type
       AND derivative.scope_id = root.scope_id
       AND derivative.generation = 1
       AND derivative.object_kind = 'event'
       AND derivative.lifecycle = 'active'
      JOIN team_projection_outputs output
        ON output.job_id = job.job_id
       AND output.derivative_object_id = derivative.object_id
       AND output.derivative_generation = derivative.generation
      JOIN team_object_contributions contribution
        ON contribution.parent_object_id = root.object_id
       AND contribution.derivative_object_id = derivative.object_id
       AND contribution.parent_generation = root.generation
       AND contribution.derivative_generation = derivative.generation
      JOIN team_object_storage_map storage
        ON storage.object_id = derivative.object_id
       AND storage.team_id = root.team_id
       AND storage.scope_type = root.scope_type
       AND storage.scope_id = root.scope_id
       AND storage.generation = derivative.generation
       AND storage.representation_kind = 'memory_event'
       AND storage.storage_key = NEW.event_id
     WHERE job.job_id = NEW.job_id
       AND job.projection_kind = 'event'
       AND job.state = 'leased'
       AND root.object_id = NEW.root_object_id
       AND root.generation = NEW.root_generation
       AND root.team_id = NEW.team_id
       AND root.scope_type = NEW.scope_type
       AND root.scope_id = NEW.scope_id
)
BEGIN SELECT RAISE(ABORT, 'team memory event projection is stale'); END;

CREATE TRIGGER team_memory_events_immutable
BEFORE UPDATE ON team_memory_events
BEGIN SELECT RAISE(ABORT, 'team memory events are immutable'); END;

CREATE TABLE team_memory_embeddings (
    embedding_id          TEXT PRIMARY KEY CHECK(
                              length(embedding_id) BETWEEN 1 AND 255
                              AND embedding_id NOT GLOB '*[^A-Za-z0-9._:-]*'
                          ),
    derivative_object_id  TEXT NOT NULL UNIQUE REFERENCES team_object_registry(object_id),
    job_id                TEXT NOT NULL REFERENCES team_projection_jobs(job_id),
    root_object_id        TEXT NOT NULL REFERENCES team_object_registry(object_id),
    root_generation       INTEGER NOT NULL CHECK(root_generation >= 1),
    capsule_id            TEXT NOT NULL REFERENCES team_memory_capsules(capsule_id),
    team_id               TEXT NOT NULL,
    scope_type            TEXT NOT NULL CHECK(scope_type IN ('personal', 'project', 'repo', 'agent', 'session')),
    scope_id              TEXT NOT NULL CHECK(length(scope_id) BETWEEN 1 AND 255),
    model                 TEXT NOT NULL CHECK(
                              length(model) BETWEEN 1 AND 64
                              AND model NOT GLOB '*[^a-z0-9._:-]*'
                          ),
    dimensions            INTEGER NOT NULL CHECK(dimensions BETWEEN 1 AND 4096),
    vector_json           TEXT NOT NULL CHECK(
                              json_valid(vector_json) = 1 AND json_type(vector_json) = 'array'
                          ),
    vector_digest         TEXT NOT NULL CHECK(
                              length(vector_digest) = 64
                              AND vector_digest NOT GLOB '*[^0-9a-f]*'
                          ),
    content_digest        TEXT NOT NULL CHECK(
                              length(content_digest) = 64
                              AND content_digest NOT GLOB '*[^0-9a-f]*'
                          ),
    created_at            TEXT NOT NULL,
    UNIQUE(root_object_id, root_generation, capsule_id, model)
);
CREATE INDEX idx_team_memory_embeddings_scope_model
    ON team_memory_embeddings(team_id, scope_type, scope_id, root_generation, model);

CREATE TRIGGER team_memory_embeddings_generation_fence_insert
BEFORE INSERT ON team_memory_embeddings
WHEN NOT EXISTS (
    SELECT 1
      FROM team_projection_jobs job
      JOIN team_object_registry root
        ON root.object_id = job.root_object_id
       AND root.team_id = job.team_id
       AND root.scope_type = job.scope_type
       AND root.scope_id = job.scope_id
       AND root.generation = job.root_generation
       AND root.lifecycle = 'active'
      JOIN team_memory_capsules capsule
        ON capsule.capsule_id = NEW.capsule_id
       AND capsule.root_object_id = root.object_id
       AND capsule.team_id = root.team_id
       AND capsule.scope_type = root.scope_type
       AND capsule.scope_id = root.scope_id
       AND capsule.root_generation = root.generation
      JOIN team_object_registry derivative
        ON derivative.object_id = NEW.derivative_object_id
       AND derivative.team_id = root.team_id
       AND derivative.scope_type = root.scope_type
       AND derivative.scope_id = root.scope_id
       AND derivative.generation = 1
       AND derivative.object_kind = 'embedding'
       AND derivative.lifecycle = 'active'
      JOIN team_projection_outputs output
        ON output.job_id = job.job_id
       AND output.derivative_object_id = derivative.object_id
       AND output.derivative_generation = derivative.generation
      JOIN team_object_contributions contribution
        ON contribution.parent_object_id = root.object_id
       AND contribution.derivative_object_id = derivative.object_id
       AND contribution.parent_generation = root.generation
       AND contribution.derivative_generation = derivative.generation
      JOIN team_object_storage_map storage
        ON storage.object_id = derivative.object_id
       AND storage.team_id = root.team_id
       AND storage.scope_type = root.scope_type
       AND storage.scope_id = root.scope_id
       AND storage.generation = derivative.generation
       AND storage.representation_kind = 'memory_embedding'
       AND storage.storage_key = NEW.embedding_id
     WHERE job.job_id = NEW.job_id
       AND job.projection_kind = 'embedding'
       AND job.state = 'leased'
       AND root.object_id = NEW.root_object_id
       AND root.generation = NEW.root_generation
       AND root.team_id = NEW.team_id
       AND root.scope_type = NEW.scope_type
       AND root.scope_id = NEW.scope_id
)
BEGIN SELECT RAISE(ABORT, 'team memory embedding projection is stale'); END;

CREATE TRIGGER team_memory_embeddings_immutable
BEFORE UPDATE ON team_memory_embeddings
BEGIN SELECT RAISE(ABORT, 'team memory embeddings are immutable'); END;

-- Once the team-memory schema is present, readers and writers older than this
-- migration are never valid rollback targets for a marked team store.
UPDATE team_stores
   SET min_reader_version = CASE WHEN min_reader_version < 35 THEN 35 ELSE min_reader_version END,
       min_writer_version = CASE WHEN min_writer_version < 35 THEN 35 ELSE min_writer_version END;
