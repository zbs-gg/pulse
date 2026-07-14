-- Content-free operational health for the Team projection worker. The daemon
-- writer lease proves authority; this row proves the projection loop itself is
-- alive and that every required deployment dependency is configured.
CREATE TABLE team_worker_heartbeats (
    store_id           TEXT NOT NULL REFERENCES team_stores(store_id),
    team_id            TEXT NOT NULL REFERENCES team_stores(team_id),
    worker_kind        TEXT NOT NULL CHECK(worker_kind = 'projection'),
    writer_id          TEXT NOT NULL CHECK(
                           length(writer_id) BETWEEN 1 AND 255
                           AND writer_id NOT GLOB '*[^A-Za-z0-9._:-]*'
                       ),
    worker_instance_id TEXT NOT NULL CHECK(
                           length(worker_instance_id) BETWEEN 1 AND 255
                           AND worker_instance_id NOT GLOB '*[^A-Za-z0-9._:-]*'
                       ),
    dependency_state   TEXT NOT NULL CHECK(dependency_state IN ('ready', 'degraded')),
    dependency_reason  TEXT NOT NULL DEFAULT '' CHECK(
                           dependency_reason IN ('', 'embedding_dependency_not_configured')
                       ),
    last_error_code    TEXT NOT NULL DEFAULT '' CHECK(
                           last_error_code IN ('', 'projection_cycle_failed')
                       ),
    started_at         TEXT NOT NULL CHECK(length(started_at) BETWEEN 20 AND 40),
    heartbeat_at       TEXT NOT NULL CHECK(length(heartbeat_at) BETWEEN 20 AND 40),
    PRIMARY KEY(store_id, worker_kind),
    CHECK(
          (dependency_state = 'ready' AND dependency_reason = '')
       OR (dependency_state = 'degraded' AND dependency_reason <> '')
    )
) WITHOUT ROWID;

CREATE INDEX idx_team_worker_heartbeats_health
    ON team_worker_heartbeats(team_id, worker_kind, heartbeat_at);

CREATE TRIGGER team_worker_heartbeats_identity_insert
BEFORE INSERT ON team_worker_heartbeats
WHEN NOT EXISTS (
    SELECT 1 FROM team_stores
     WHERE store_id = NEW.store_id AND team_id = NEW.team_id
)
BEGIN SELECT RAISE(ABORT, 'worker heartbeat store/team identity mismatch'); END;

CREATE TRIGGER team_worker_heartbeats_identity_immutable
BEFORE UPDATE OF store_id, team_id, worker_kind ON team_worker_heartbeats
BEGIN SELECT RAISE(ABORT, 'worker heartbeat identity is immutable'); END;

CREATE TRIGGER team_worker_heartbeats_identity_update
BEFORE UPDATE ON team_worker_heartbeats
WHEN NOT EXISTS (
    SELECT 1 FROM team_stores
     WHERE store_id = NEW.store_id AND team_id = NEW.team_id
)
BEGIN SELECT RAISE(ABORT, 'worker heartbeat store/team identity mismatch'); END;

CREATE TRIGGER team_worker_heartbeats_no_delete
BEFORE DELETE ON team_worker_heartbeats
BEGIN SELECT RAISE(ABORT, 'worker heartbeat is durable'); END;

UPDATE team_stores
   SET min_reader_version = 44,
       min_writer_version = 44
 WHERE singleton = 1;

UPDATE store_identity
   SET min_reader_version = CASE WHEN min_reader_version < 44 THEN 44 ELSE min_reader_version END,
       min_writer_version = CASE WHEN min_writer_version < 44 THEN 44 ELSE min_writer_version END;

UPDATE team_policy_metadata
   SET schema_version = CASE WHEN schema_version < 44 THEN 44 ELSE schema_version END,
       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

DROP TRIGGER team_policy_metadata_after_store_insert;
CREATE TRIGGER team_policy_metadata_after_store_insert
AFTER INSERT ON team_stores
BEGIN
    INSERT INTO team_policy_metadata(
        store_id, team_id, policy_version, schema_version,
        policy_epoch, global_epoch, real_content_state, updated_at)
    VALUES (
        NEW.store_id, NEW.team_id, 1, 44,
        1, NEW.auth_epoch, 'blocked', NEW.created_at);
END;
