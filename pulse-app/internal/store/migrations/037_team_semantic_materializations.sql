-- Additive contribution-level materialization schema for team graph-delta
-- projection workers. A semantic derivative may be shared by multiple active
-- roots in one exact scope; every root therefore owns an immutable intent row
-- and an immutable materialization contribution instead of owning the
-- derivative's content row outright.

DROP TRIGGER team_policy_metadata_after_store_insert;

CREATE TRIGGER team_policy_metadata_after_store_insert
AFTER INSERT ON team_stores
BEGIN
    INSERT INTO team_policy_metadata(
        store_id, team_id, policy_version, schema_version,
        policy_epoch, global_epoch, real_content_state, updated_at)
    VALUES (
        NEW.store_id, NEW.team_id, 1, 37,
        1, NEW.auth_epoch, 'blocked', NEW.created_at);
END;

UPDATE team_policy_metadata
   SET schema_version = 37,
       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
 WHERE schema_version < 37;

-- Common content-free attachment spine. `derivative_object_id` is
-- intentionally non-unique: two roots in one scope may independently support
-- one semantic derivative and U7 may later remove either contribution.
CREATE TABLE team_semantic_materializations (
    intent_id              TEXT PRIMARY KEY
                           REFERENCES team_semantic_projection_intents(intent_id),
    job_id                 TEXT NOT NULL REFERENCES team_projection_jobs(job_id),
    root_object_id         TEXT NOT NULL REFERENCES team_object_registry(object_id),
    root_generation        INTEGER NOT NULL CHECK(root_generation >= 1),
    derivative_object_id   TEXT NOT NULL REFERENCES team_object_registry(object_id),
    derivative_generation  INTEGER NOT NULL CHECK(derivative_generation >= 1),
    store_id               TEXT NOT NULL REFERENCES team_stores(store_id),
    team_id                TEXT NOT NULL REFERENCES team_stores(team_id),
    scope_type             TEXT NOT NULL CHECK(
                               scope_type IN ('personal', 'project', 'repo', 'agent', 'session')
                           ),
    scope_id               TEXT NOT NULL CHECK(length(scope_id) BETWEEN 1 AND 255),
    projection_kind        TEXT NOT NULL CHECK(
                               projection_kind IN ('claim', 'continuity', 'embedding', 'graph')
                           ),
    semantic_key_digest    TEXT NOT NULL CHECK(
                               length(semantic_key_digest) = 64
                               AND semantic_key_digest NOT GLOB '*[^0-9a-f]*'
                           ),
    policy_digest          TEXT NOT NULL CHECK(
                               length(policy_digest) = 64
                               AND policy_digest NOT GLOB '*[^0-9a-f]*'
                           ),
    payload_digest         TEXT NOT NULL CHECK(
                               length(payload_digest) = 64
                               AND payload_digest NOT GLOB '*[^0-9a-f]*'
                           ),
    created_at             TEXT NOT NULL CHECK(length(created_at) BETWEEN 20 AND 40)
);

CREATE INDEX idx_team_semantic_materializations_scope
    ON team_semantic_materializations(
        store_id, team_id, scope_type, scope_id, projection_kind,
        derivative_object_id, root_object_id
    );
CREATE INDEX idx_team_semantic_materializations_derivative
    ON team_semantic_materializations(
        derivative_object_id, derivative_generation, root_object_id, root_generation
    );

CREATE TRIGGER team_semantic_materializations_generation_fence_insert
BEFORE INSERT ON team_semantic_materializations
WHEN NOT EXISTS (
    SELECT 1
      FROM team_semantic_projection_intents intent
      JOIN team_projection_jobs job
        ON job.job_id = NEW.job_id
       AND job.store_id = intent.store_id
       AND job.team_id = intent.team_id
       AND job.root_object_id = intent.root_object_id
       AND job.root_generation = intent.root_generation
       AND job.scope_type = intent.scope_type
       AND job.scope_id = intent.scope_id
       AND job.projection_kind = intent.projection_kind
       AND job.state = 'leased'
      JOIN team_object_registry root
        ON root.object_id = intent.root_object_id
       AND root.store_id = intent.store_id
       AND root.team_id = intent.team_id
       AND root.object_kind = 'graph_delta'
       AND root.scope_type = intent.scope_type
       AND root.scope_id = intent.scope_id
       AND root.generation = intent.root_generation
       AND root.lifecycle = 'active'
      JOIN team_object_registry derivative
        ON derivative.object_id = intent.derivative_object_id
       AND derivative.store_id = intent.store_id
       AND derivative.team_id = intent.team_id
       AND derivative.object_kind = intent.derivative_kind
       AND derivative.scope_type = intent.scope_type
       AND derivative.scope_id = intent.scope_id
       AND derivative.generation = NEW.derivative_generation
       AND derivative.lifecycle = 'active'
      JOIN team_projection_outputs output
        ON output.job_id = job.job_id
       AND output.derivative_object_id = derivative.object_id
       AND output.derivative_generation = derivative.generation
      JOIN team_object_contributions contribution
        ON contribution.parent_object_id = root.object_id
       AND contribution.derivative_object_id = derivative.object_id
       AND contribution.team_id = root.team_id
       AND contribution.scope_type = root.scope_type
       AND contribution.scope_id = root.scope_id
       AND contribution.parent_generation = root.generation
       AND contribution.derivative_generation = derivative.generation
     WHERE intent.intent_id = NEW.intent_id
       AND intent.root_object_id = NEW.root_object_id
       AND intent.root_generation = NEW.root_generation
       AND intent.derivative_object_id = NEW.derivative_object_id
       AND intent.store_id = NEW.store_id
       AND intent.team_id = NEW.team_id
       AND intent.scope_type = NEW.scope_type
       AND intent.scope_id = NEW.scope_id
       AND intent.projection_kind = NEW.projection_kind
       AND intent.semantic_key_digest = NEW.semantic_key_digest
       AND intent.policy_digest = NEW.policy_digest
       AND intent.payload_digest = NEW.payload_digest
)
BEGIN SELECT RAISE(ABORT, 'team semantic materialization is stale'); END;

CREATE TRIGGER team_semantic_materializations_immutable
BEFORE UPDATE ON team_semantic_materializations
BEGIN SELECT RAISE(ABORT, 'team semantic materializations are immutable'); END;

-- Graph contributions keep canonical source payload separate from resolved
-- derivative references. Both remain physically scope-partitioned.
CREATE TABLE team_graph_materializations (
    intent_id             TEXT PRIMARY KEY
                          REFERENCES team_semantic_materializations(intent_id),
    store_id              TEXT NOT NULL REFERENCES team_stores(store_id),
    team_id               TEXT NOT NULL REFERENCES team_stores(team_id),
    scope_type            TEXT NOT NULL CHECK(
                              scope_type IN ('personal', 'project', 'repo', 'agent', 'session')
                          ),
    scope_id              TEXT NOT NULL CHECK(length(scope_id) BETWEEN 1 AND 255),
    derivative_object_id  TEXT NOT NULL REFERENCES team_object_registry(object_id),
    graph_kind            TEXT NOT NULL CHECK(graph_kind IN (
                              'graph_entity', 'graph_relation', 'graph_fact', 'graph_event'
                          )),
    payload_json          TEXT NOT NULL CHECK(
                              json_valid(payload_json) = 1
                              AND json_type(payload_json) = 'object'
                              AND length(CAST(payload_json AS BLOB)) BETWEEN 2 AND 262144
                          ),
    resolved_refs_json    TEXT NOT NULL CHECK(
                              json_valid(resolved_refs_json) = 1
                              AND json_type(resolved_refs_json) = 'array'
                              AND length(CAST(resolved_refs_json AS BLOB)) BETWEEN 2 AND 65536
                          ),
    content_digest        TEXT NOT NULL CHECK(
                              length(content_digest) = 64
                              AND content_digest NOT GLOB '*[^0-9a-f]*'
                          ),
    created_at            TEXT NOT NULL CHECK(length(created_at) BETWEEN 20 AND 40)
);

CREATE INDEX idx_team_graph_materializations_scope
    ON team_graph_materializations(
        store_id, team_id, scope_type, scope_id, graph_kind, derivative_object_id
    );

CREATE TRIGGER team_graph_materializations_contract_insert
BEFORE INSERT ON team_graph_materializations
WHEN EXISTS (
    SELECT 1 FROM json_each(NEW.resolved_refs_json) WHERE type <> 'text'
) OR NOT EXISTS (
    SELECT 1
      FROM team_semantic_materializations materialization
      JOIN team_semantic_projection_intents intent
        ON intent.intent_id = materialization.intent_id
      JOIN team_projection_jobs job
        ON job.job_id = materialization.job_id
       AND job.state = 'leased'
      JOIN team_object_registry root
        ON root.object_id = materialization.root_object_id
       AND root.store_id = materialization.store_id
       AND root.team_id = materialization.team_id
       AND root.scope_type = materialization.scope_type
       AND root.scope_id = materialization.scope_id
       AND root.generation = materialization.root_generation
       AND root.lifecycle = 'active'
      JOIN team_object_registry derivative
        ON derivative.object_id = materialization.derivative_object_id
       AND derivative.store_id = materialization.store_id
       AND derivative.team_id = materialization.team_id
       AND derivative.scope_type = materialization.scope_type
       AND derivative.scope_id = materialization.scope_id
       AND derivative.generation = materialization.derivative_generation
       AND derivative.lifecycle = 'active'
     WHERE materialization.intent_id = NEW.intent_id
       AND materialization.store_id = NEW.store_id
       AND materialization.team_id = NEW.team_id
       AND materialization.scope_type = NEW.scope_type
       AND materialization.scope_id = NEW.scope_id
       AND materialization.derivative_object_id = NEW.derivative_object_id
       AND materialization.projection_kind = 'graph'
       AND materialization.payload_digest = NEW.content_digest
       AND intent.derivative_kind = NEW.graph_kind
)
BEGIN SELECT RAISE(ABORT, 'team graph materialization contract mismatch'); END;

CREATE TRIGGER team_graph_materializations_immutable
BEFORE UPDATE ON team_graph_materializations
BEGIN SELECT RAISE(ABORT, 'team graph materializations are immutable'); END;

-- Assertion rows are contribution evidence for a scope-partitioned claim
-- slot. The slot digest intentionally converges competing values while each
-- root's exact canonical claim remains separately removable.
CREATE TABLE team_assertion_materializations (
    intent_id             TEXT PRIMARY KEY
                          REFERENCES team_semantic_materializations(intent_id),
    store_id              TEXT NOT NULL REFERENCES team_stores(store_id),
    team_id               TEXT NOT NULL REFERENCES team_stores(team_id),
    scope_type            TEXT NOT NULL CHECK(
                              scope_type IN ('personal', 'project', 'repo', 'agent', 'session')
                          ),
    scope_id              TEXT NOT NULL CHECK(length(scope_id) BETWEEN 1 AND 255),
    derivative_object_id  TEXT NOT NULL REFERENCES team_object_registry(object_id),
    claim_slot_digest     TEXT NOT NULL CHECK(
                              length(claim_slot_digest) = 64
                              AND claim_slot_digest NOT GLOB '*[^0-9a-f]*'
                          ),
    claim_json            TEXT NOT NULL CHECK(
                              json_valid(claim_json) = 1
                              AND json_type(claim_json) = 'object'
                              AND length(CAST(claim_json AS BLOB)) BETWEEN 2 AND 262144
                          ),
    source_refs_json      TEXT NOT NULL CHECK(
                              json_valid(source_refs_json) = 1
                              AND json_type(source_refs_json) = 'array'
                              AND length(CAST(source_refs_json AS BLOB)) BETWEEN 2 AND 65536
                          ),
    content_digest        TEXT NOT NULL CHECK(
                              length(content_digest) = 64
                              AND content_digest NOT GLOB '*[^0-9a-f]*'
                          ),
    created_at            TEXT NOT NULL CHECK(length(created_at) BETWEEN 20 AND 40)
);

CREATE INDEX idx_team_assertion_materializations_scope_slot
    ON team_assertion_materializations(
        store_id, team_id, scope_type, scope_id, claim_slot_digest,
        derivative_object_id
    );

CREATE TRIGGER team_assertion_materializations_contract_insert
BEFORE INSERT ON team_assertion_materializations
WHEN EXISTS (
    SELECT 1 FROM json_each(NEW.source_refs_json) WHERE type <> 'text'
) OR NOT EXISTS (
    SELECT 1
      FROM team_semantic_materializations materialization
      JOIN team_semantic_projection_intents intent
        ON intent.intent_id = materialization.intent_id
      JOIN team_projection_jobs job
        ON job.job_id = materialization.job_id
       AND job.state = 'leased'
      JOIN team_object_registry root
        ON root.object_id = materialization.root_object_id
       AND root.store_id = materialization.store_id
       AND root.team_id = materialization.team_id
       AND root.scope_type = materialization.scope_type
       AND root.scope_id = materialization.scope_id
       AND root.generation = materialization.root_generation
       AND root.lifecycle = 'active'
      JOIN team_object_registry derivative
        ON derivative.object_id = materialization.derivative_object_id
       AND derivative.store_id = materialization.store_id
       AND derivative.team_id = materialization.team_id
       AND derivative.scope_type = materialization.scope_type
       AND derivative.scope_id = materialization.scope_id
       AND derivative.generation = materialization.derivative_generation
       AND derivative.lifecycle = 'active'
     WHERE materialization.intent_id = NEW.intent_id
       AND materialization.store_id = NEW.store_id
       AND materialization.team_id = NEW.team_id
       AND materialization.scope_type = NEW.scope_type
       AND materialization.scope_id = NEW.scope_id
       AND materialization.derivative_object_id = NEW.derivative_object_id
       AND materialization.projection_kind = 'claim'
       AND materialization.semantic_key_digest = NEW.claim_slot_digest
       AND materialization.payload_digest = NEW.content_digest
       AND intent.derivative_kind = 'assertion'
)
BEGIN SELECT RAISE(ABORT, 'team assertion materialization contract mismatch'); END;

CREATE TRIGGER team_assertion_materializations_immutable
BEFORE UPDATE ON team_assertion_materializations
BEGIN SELECT RAISE(ABORT, 'team assertion materializations are immutable'); END;

-- Continuity contributions use opaque scope-bound thread and session slot
-- digests so no global content-bearing thread shell can bridge scopes.
CREATE TABLE team_continuity_materializations (
    intent_id             TEXT PRIMARY KEY
                          REFERENCES team_semantic_materializations(intent_id),
    store_id              TEXT NOT NULL REFERENCES team_stores(store_id),
    team_id               TEXT NOT NULL REFERENCES team_stores(team_id),
    scope_type            TEXT NOT NULL CHECK(
                              scope_type IN ('personal', 'project', 'repo', 'agent', 'session')
                          ),
    scope_id              TEXT NOT NULL CHECK(length(scope_id) BETWEEN 1 AND 255),
    derivative_object_id  TEXT NOT NULL REFERENCES team_object_registry(object_id),
    thread_slot_digest    TEXT NOT NULL CHECK(
                              length(thread_slot_digest) = 64
                              AND thread_slot_digest NOT GLOB '*[^0-9a-f]*'
                          ),
    session_slot_digest   TEXT NOT NULL CHECK(
                              length(session_slot_digest) = 64
                              AND session_slot_digest NOT GLOB '*[^0-9a-f]*'
                          ),
    checkpoint_json       TEXT NOT NULL CHECK(
                              json_valid(checkpoint_json) = 1
                              AND json_type(checkpoint_json) = 'object'
                              AND length(CAST(checkpoint_json AS BLOB)) BETWEEN 2 AND 262144
                          ),
    content_digest        TEXT NOT NULL CHECK(
                              length(content_digest) = 64
                              AND content_digest NOT GLOB '*[^0-9a-f]*'
                          ),
    created_at            TEXT NOT NULL CHECK(length(created_at) BETWEEN 20 AND 40)
);

CREATE INDEX idx_team_continuity_materializations_scope_thread
    ON team_continuity_materializations(
        store_id, team_id, scope_type, scope_id, thread_slot_digest,
        session_slot_digest, derivative_object_id
    );

CREATE TRIGGER team_continuity_materializations_contract_insert
BEFORE INSERT ON team_continuity_materializations
WHEN NOT EXISTS (
    SELECT 1
      FROM team_semantic_materializations materialization
      JOIN team_semantic_projection_intents intent
        ON intent.intent_id = materialization.intent_id
      JOIN team_projection_jobs job
        ON job.job_id = materialization.job_id
       AND job.state = 'leased'
      JOIN team_object_registry root
        ON root.object_id = materialization.root_object_id
       AND root.store_id = materialization.store_id
       AND root.team_id = materialization.team_id
       AND root.scope_type = materialization.scope_type
       AND root.scope_id = materialization.scope_id
       AND root.generation = materialization.root_generation
       AND root.lifecycle = 'active'
      JOIN team_object_registry derivative
        ON derivative.object_id = materialization.derivative_object_id
       AND derivative.store_id = materialization.store_id
       AND derivative.team_id = materialization.team_id
       AND derivative.scope_type = materialization.scope_type
       AND derivative.scope_id = materialization.scope_id
       AND derivative.generation = materialization.derivative_generation
       AND derivative.lifecycle = 'active'
     WHERE materialization.intent_id = NEW.intent_id
       AND materialization.store_id = NEW.store_id
       AND materialization.team_id = NEW.team_id
       AND materialization.scope_type = NEW.scope_type
       AND materialization.scope_id = NEW.scope_id
       AND materialization.derivative_object_id = NEW.derivative_object_id
       AND materialization.projection_kind = 'continuity'
       AND materialization.payload_digest = NEW.content_digest
       AND intent.derivative_kind = 'continuity_checkpoint'
)
BEGIN SELECT RAISE(ABORT, 'team continuity materialization contract mismatch'); END;

CREATE TRIGGER team_continuity_materializations_immutable
BEFORE UPDATE ON team_continuity_materializations
BEGIN SELECT RAISE(ABORT, 'team continuity materializations are immutable'); END;

-- Embeddings are model-specific content contributions. They reference an
-- intent rather than owning a global entity row, and never duplicate the text
-- source that produced the vector.
CREATE TABLE team_semantic_embeddings (
    intent_id             TEXT NOT NULL
                          REFERENCES team_semantic_materializations(intent_id),
    model                 TEXT NOT NULL CHECK(
                              length(model) BETWEEN 1 AND 64
                              AND model NOT GLOB '*[^a-z0-9._:-]*'
                          ),
    store_id              TEXT NOT NULL REFERENCES team_stores(store_id),
    team_id               TEXT NOT NULL REFERENCES team_stores(team_id),
    scope_type            TEXT NOT NULL CHECK(
                              scope_type IN ('personal', 'project', 'repo', 'agent', 'session')
                          ),
    scope_id              TEXT NOT NULL CHECK(length(scope_id) BETWEEN 1 AND 255),
    derivative_object_id  TEXT NOT NULL REFERENCES team_object_registry(object_id),
    dimensions            INTEGER NOT NULL CHECK(dimensions BETWEEN 1 AND 4096),
    vector_json           TEXT NOT NULL CHECK(
                              json_valid(vector_json) = 1
                              AND json_type(vector_json) = 'array'
                              AND json_array_length(vector_json) = dimensions
                              AND length(CAST(vector_json AS BLOB)) BETWEEN 3 AND 1048576
                          ),
    vector_digest         TEXT NOT NULL CHECK(
                              length(vector_digest) = 64
                              AND vector_digest NOT GLOB '*[^0-9a-f]*'
                          ),
    content_digest        TEXT NOT NULL CHECK(
                              length(content_digest) = 64
                              AND content_digest NOT GLOB '*[^0-9a-f]*'
                          ),
    created_at            TEXT NOT NULL CHECK(length(created_at) BETWEEN 20 AND 40),
    PRIMARY KEY(intent_id, model)
);

CREATE INDEX idx_team_semantic_embeddings_scope_model
    ON team_semantic_embeddings(
        store_id, team_id, scope_type, scope_id, model, derivative_object_id
    );

CREATE TRIGGER team_semantic_embeddings_contract_insert
BEFORE INSERT ON team_semantic_embeddings
WHEN EXISTS (
    SELECT 1 FROM json_each(NEW.vector_json) WHERE type NOT IN ('integer', 'real')
) OR NOT EXISTS (
    SELECT 1
      FROM team_semantic_materializations materialization
      JOIN team_semantic_projection_intents intent
        ON intent.intent_id = materialization.intent_id
      JOIN team_projection_jobs job
        ON job.job_id = materialization.job_id
       AND job.state = 'leased'
      JOIN team_object_registry root
        ON root.object_id = materialization.root_object_id
       AND root.store_id = materialization.store_id
       AND root.team_id = materialization.team_id
       AND root.scope_type = materialization.scope_type
       AND root.scope_id = materialization.scope_id
       AND root.generation = materialization.root_generation
       AND root.lifecycle = 'active'
      JOIN team_object_registry derivative
        ON derivative.object_id = materialization.derivative_object_id
       AND derivative.store_id = materialization.store_id
       AND derivative.team_id = materialization.team_id
       AND derivative.scope_type = materialization.scope_type
       AND derivative.scope_id = materialization.scope_id
       AND derivative.generation = materialization.derivative_generation
       AND derivative.lifecycle = 'active'
     WHERE materialization.intent_id = NEW.intent_id
       AND materialization.store_id = NEW.store_id
       AND materialization.team_id = NEW.team_id
       AND materialization.scope_type = NEW.scope_type
       AND materialization.scope_id = NEW.scope_id
       AND materialization.derivative_object_id = NEW.derivative_object_id
       AND materialization.projection_kind = 'embedding'
       AND materialization.payload_digest = NEW.content_digest
       AND intent.derivative_kind = 'embedding'
)
BEGIN SELECT RAISE(ABORT, 'team semantic embedding contract mismatch'); END;

CREATE TRIGGER team_semantic_embeddings_immutable
BEFORE UPDATE ON team_semantic_embeddings
BEGIN SELECT RAISE(ABORT, 'team semantic embeddings are immutable'); END;

-- Readers and writers older than the scoped materialization migration cannot
-- safely interpret contribution-level team semantic state.
UPDATE team_stores
   SET min_reader_version = CASE WHEN min_reader_version < 37 THEN 37 ELSE min_reader_version END,
       min_writer_version = CASE WHEN min_writer_version < 37 THEN 37 ELSE min_writer_version END;
