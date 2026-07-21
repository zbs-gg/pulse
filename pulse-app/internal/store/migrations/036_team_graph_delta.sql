-- Atomic, scope-partitioned ingress for pulse.team.graph_delta.v1.
-- This migration stores the canonical source envelope and content-free
-- projection intents only. Materialization into graph/assertion/continuity
-- tables remains a separately leased worker responsibility.

DROP TRIGGER team_policy_metadata_after_store_insert;

CREATE TRIGGER team_policy_metadata_after_store_insert
AFTER INSERT ON team_stores
BEGIN
    INSERT INTO team_policy_metadata(
        store_id, team_id, policy_version, schema_version,
        policy_epoch, global_epoch, real_content_state, updated_at)
    VALUES (
        NEW.store_id, NEW.team_id, 1, 36,
        1, NEW.auth_epoch, 'blocked', NEW.created_at);
END;

UPDATE team_policy_metadata
   SET schema_version = 36,
       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
 WHERE schema_version < 36;

CREATE TABLE team_graph_delta_inputs (
    root_object_id      TEXT PRIMARY KEY REFERENCES team_object_registry(object_id),
    store_id            TEXT NOT NULL REFERENCES team_stores(store_id),
    team_id             TEXT NOT NULL REFERENCES team_stores(team_id),
    scope_type          TEXT NOT NULL
                        CHECK(scope_type IN ('personal', 'project', 'repo', 'agent', 'session')),
    scope_id            TEXT NOT NULL CHECK(length(scope_id) BETWEEN 1 AND 255),
    root_generation     INTEGER NOT NULL CHECK(root_generation >= 1),
    schema_version      TEXT NOT NULL CHECK(schema_version = 'pulse.team.graph_delta.v1'),
    source_host         TEXT NOT NULL CHECK(source_host IN (
                            'chatgpt', 'claude', 'codex', 'claude-code', 'gemini-cli',
                            'cursor', 'langchain', 'crewai', 'pulse-cli'
                        )),
    conversation_scope  TEXT NOT NULL CHECK(conversation_scope IN (
                            'current_turn', 'user_selected_excerpt',
                            'project_context', 'install_event'
                        )),
    source_timestamp    TEXT NOT NULL CHECK(
                            length(source_timestamp) = 24
                            AND source_timestamp GLOB
                                '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z'
                        ),
    canonical_json      TEXT NOT NULL CHECK(
                            json_valid(canonical_json) = 1
                            AND json_type(canonical_json) = 'object'
                            AND length(CAST(canonical_json AS BLOB)) BETWEEN 2 AND 262144
                            AND json_extract(canonical_json, '$.schema') IS 'pulse.team.graph_delta.v1'
                            AND json_type(canonical_json, '$.source') IS 'object'
                            AND json_extract(canonical_json, '$.source.host') IS source_host
                            AND json_extract(canonical_json, '$.source.conversation_scope') IS conversation_scope
                            AND json_extract(canonical_json, '$.source.timestamp') IS source_timestamp
                            AND json_type(canonical_json, '$.nodes') IS 'array'
                            AND json_array_length(canonical_json, '$.nodes') BETWEEN 0 AND 30
                            AND json_type(canonical_json, '$.edges') IS 'array'
                            AND json_array_length(canonical_json, '$.edges') BETWEEN 0 AND 50
                            AND json_type(canonical_json, '$.facts') IS 'array'
                            AND json_array_length(canonical_json, '$.facts') BETWEEN 0 AND 50
                            AND json_type(canonical_json, '$.events') IS 'array'
                            AND json_array_length(canonical_json, '$.events') BETWEEN 0 AND 20
                            AND (
                                json_array_length(canonical_json, '$.nodes') > 0
                                OR json_array_length(canonical_json, '$.edges') > 0
                                OR json_array_length(canonical_json, '$.facts') > 0
                                OR json_array_length(canonical_json, '$.events') > 0
                                OR json_type(canonical_json, '$.continuity') IS 'object'
                            )
                            AND (
                                json_type(canonical_json, '$.continuity') IS NULL
                                OR json_type(canonical_json, '$.continuity') IS 'object'
                            )
                            AND json_type(canonical_json, '$.raw_input_included') IS 'false'
                            AND json_type(canonical_json, '$.active_context') IS 'object'
                            AND (
                                json_type(canonical_json, '$.target_scope') IS NULL
                                OR json_type(canonical_json, '$.target_scope') IS 'object'
                            )
                            AND COALESCE(json_extract(canonical_json, '$.privacy_tier'), '')
                                IN ('normal', 'sensitive', 'private')
                            AND COALESCE(json_extract(canonical_json, '$.retention'), '')
                                IN ('session', 'project', 'long_term')
                            AND (
                                json_type(canonical_json, '$.expires_at') IS NULL
                                OR json_type(canonical_json, '$.expires_at') IS 'text'
                            )
                            AND json_type(canonical_json, '$.idempotency_key') IS NULL
                            AND json_type(canonical_json, '$.actor') IS NULL
                            AND json_type(canonical_json, '$.principal_id') IS NULL
                            AND json_type(canonical_json, '$.owner_principal_id') IS NULL
                            AND json_type(canonical_json, '$.team_id') IS NULL
                        ),
    content_digest      TEXT NOT NULL CHECK(
                            length(content_digest) = 64
                            AND content_digest NOT GLOB '*[^0-9a-f]*'
                        ),
    created_at          TEXT NOT NULL,
    UNIQUE(root_object_id, root_generation)
);

CREATE INDEX idx_team_graph_delta_inputs_scope
    ON team_graph_delta_inputs(team_id, scope_type, scope_id, root_generation);

CREATE TRIGGER team_graph_delta_inputs_generation_fence_insert
BEFORE INSERT ON team_graph_delta_inputs
WHEN NOT EXISTS (
    SELECT 1
      FROM team_object_registry root
     WHERE root.object_id = NEW.root_object_id
       AND root.store_id = NEW.store_id
       AND root.team_id = NEW.team_id
       AND root.object_kind = 'graph_delta'
       AND root.scope_type = NEW.scope_type
       AND root.scope_id = NEW.scope_id
       AND root.generation = NEW.root_generation
       AND root.lifecycle = 'active'
)
BEGIN SELECT RAISE(ABORT, 'team graph delta input root is stale'); END;

CREATE TRIGGER team_graph_delta_inputs_immutable
BEFORE UPDATE ON team_graph_delta_inputs
BEGIN SELECT RAISE(ABORT, 'team graph delta inputs are immutable'); END;

CREATE TABLE team_semantic_projection_intents (
    intent_id            TEXT PRIMARY KEY CHECK(
                             length(intent_id) BETWEEN 1 AND 255
                             AND intent_id NOT GLOB '*[^A-Za-z0-9._:-]*'
                         ),
    root_object_id       TEXT NOT NULL REFERENCES team_object_registry(object_id),
    store_id             TEXT NOT NULL REFERENCES team_stores(store_id),
    team_id              TEXT NOT NULL REFERENCES team_stores(team_id),
    scope_type           TEXT NOT NULL
                         CHECK(scope_type IN ('personal', 'project', 'repo', 'agent', 'session')),
    scope_id             TEXT NOT NULL CHECK(length(scope_id) BETWEEN 1 AND 255),
    root_generation      INTEGER NOT NULL CHECK(root_generation >= 1),
    projection_kind      TEXT NOT NULL CHECK(
                             projection_kind IN ('claim', 'continuity', 'embedding', 'graph')
                         ),
    source_kind          TEXT NOT NULL CHECK(
                             source_kind IN ('node', 'edge', 'fact', 'event', 'continuity')
                         ),
    source_ordinal       INTEGER NOT NULL CHECK(source_ordinal BETWEEN 0 AND 49),
    derivative_object_id TEXT NOT NULL CHECK(
                             length(derivative_object_id) BETWEEN 1 AND 255
                             AND derivative_object_id NOT GLOB '*[^A-Za-z0-9._:-]*'
                         ),
    derivative_kind      TEXT NOT NULL CHECK(derivative_kind IN (
                             'graph_entity', 'graph_relation', 'graph_fact', 'graph_event',
                             'embedding', 'assertion', 'continuity_checkpoint'
                         )),
    semantic_key_digest  TEXT NOT NULL CHECK(
                             length(semantic_key_digest) = 64
                             AND semantic_key_digest NOT GLOB '*[^0-9a-f]*'
                         ),
    policy_digest        TEXT NOT NULL CHECK(
                             length(policy_digest) = 64
                             AND policy_digest NOT GLOB '*[^0-9a-f]*'
                         ),
    payload_digest       TEXT NOT NULL CHECK(
                             length(payload_digest) = 64
                             AND payload_digest NOT GLOB '*[^0-9a-f]*'
                         ),
    created_at           TEXT NOT NULL,
    UNIQUE(root_object_id, root_generation, projection_kind, source_kind, source_ordinal),
    CHECK(
        (projection_kind = 'claim' AND source_kind = 'fact' AND derivative_kind = 'assertion')
        OR (projection_kind = 'continuity' AND source_kind = 'continuity'
            AND derivative_kind = 'continuity_checkpoint')
        OR (projection_kind = 'embedding' AND source_kind IN ('node', 'edge', 'fact', 'event')
            AND derivative_kind = 'embedding')
        OR (projection_kind = 'graph' AND (
               (source_kind = 'node' AND derivative_kind = 'graph_entity')
            OR (source_kind = 'edge' AND derivative_kind = 'graph_relation')
            OR (source_kind = 'fact' AND derivative_kind = 'graph_fact')
            OR (source_kind = 'event' AND derivative_kind = 'graph_event')
        ))
    )
);

CREATE INDEX idx_team_semantic_projection_intents_root
    ON team_semantic_projection_intents(root_object_id, root_generation, projection_kind,
                                        source_kind, source_ordinal);
CREATE INDEX idx_team_semantic_projection_intents_derivative
    ON team_semantic_projection_intents(store_id, team_id, scope_type, scope_id,
                                        policy_digest, derivative_kind, semantic_key_digest);

CREATE TRIGGER team_semantic_projection_intents_generation_fence_insert
BEFORE INSERT ON team_semantic_projection_intents
WHEN NOT EXISTS (
    SELECT 1
      FROM team_object_registry root
      JOIN team_graph_delta_inputs input
        ON input.root_object_id = root.object_id
       AND input.store_id = root.store_id
       AND input.team_id = root.team_id
       AND input.scope_type = root.scope_type
       AND input.scope_id = root.scope_id
       AND input.root_generation = root.generation
     WHERE root.object_id = NEW.root_object_id
       AND root.store_id = NEW.store_id
       AND root.team_id = NEW.team_id
       AND root.object_kind = 'graph_delta'
       AND root.scope_type = NEW.scope_type
       AND root.scope_id = NEW.scope_id
       AND root.generation = NEW.root_generation
       AND root.lifecycle = 'active'
)
BEGIN SELECT RAISE(ABORT, 'team semantic projection intent root is stale'); END;

CREATE TRIGGER team_semantic_projection_intents_immutable
BEFORE UPDATE ON team_semantic_projection_intents
BEGIN SELECT RAISE(ABORT, 'team semantic projection intents are immutable'); END;

-- Readers and writers older than the graph-delta ingress migration are not
-- valid rollback targets for a marked team store.
UPDATE team_stores
   SET min_reader_version = CASE WHEN min_reader_version < 36 THEN 36 ELSE min_reader_version END,
       min_writer_version = CASE WHEN min_writer_version < 36 THEN 36 ELSE min_writer_version END;
