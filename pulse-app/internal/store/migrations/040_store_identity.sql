-- Product store identity and migration applicability. This is the first
-- migration after the frozen Team foundation (033-039).

CREATE TABLE store_identity (
    singleton           INTEGER PRIMARY KEY CHECK(singleton = 1),
    store_id            TEXT NOT NULL UNIQUE,
    store_kind          TEXT NOT NULL CHECK(store_kind IN ('local-preview', 'personal', 'desk', 'commons')),
    min_reader_version  INTEGER NOT NULL CHECK(min_reader_version >= 40),
    min_writer_version  INTEGER NOT NULL CHECK(min_writer_version >= min_reader_version),
    created_at          TEXT NOT NULL
);

CREATE TRIGGER store_identity_immutable
BEFORE UPDATE OF store_id, store_kind, created_at ON store_identity
BEGIN SELECT RAISE(ABORT, 'store identity is immutable'); END;

CREATE TRIGGER store_identity_floors_monotonic
BEFORE UPDATE OF min_reader_version, min_writer_version ON store_identity
WHEN NEW.min_reader_version < OLD.min_reader_version
  OR NEW.min_writer_version < OLD.min_writer_version
BEGIN SELECT RAISE(ABORT, 'store schema floors are monotonic'); END;

CREATE TRIGGER store_identity_no_delete
BEFORE DELETE ON store_identity
BEGIN SELECT RAISE(ABORT, 'store identity is immutable'); END;

CREATE TABLE schema_migration_applicability (
    version             INTEGER PRIMARY KEY REFERENCES schema_migration_manifest(version),
    store_kind          TEXT NOT NULL CHECK(store_kind IN ('local-preview', 'personal', 'desk', 'commons')),
    disposition         TEXT NOT NULL CHECK(disposition IN ('applied', 'skipped')),
    min_reader_version  INTEGER NOT NULL CHECK(min_reader_version >= 40),
    min_writer_version  INTEGER NOT NULL CHECK(min_writer_version >= min_reader_version),
    recorded_at         TEXT NOT NULL
);

CREATE TRIGGER schema_migration_applicability_no_update
BEFORE UPDATE ON schema_migration_applicability
BEGIN SELECT RAISE(ABORT, 'migration applicability is immutable'); END;

CREATE TRIGGER schema_migration_applicability_no_delete
BEFORE DELETE ON schema_migration_applicability
BEGIN SELECT RAISE(ABORT, 'migration applicability is immutable'); END;

-- Existing initialized Team stores advance their compatibility floors. Empty
-- bootstrap candidates remain unowned and receive floors when bootstrap binds.
UPDATE team_stores
   SET min_reader_version = CASE WHEN min_reader_version < 40 THEN 40 ELSE min_reader_version END,
       min_writer_version = CASE WHEN min_writer_version < 40 THEN 40 ELSE min_writer_version END;

UPDATE team_policy_metadata
   SET schema_version = CASE WHEN schema_version < 40 THEN 40 ELSE schema_version END,
       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now');

DROP TRIGGER team_policy_metadata_after_store_insert;
CREATE TRIGGER team_policy_metadata_after_store_insert
AFTER INSERT ON team_stores
BEGIN
    INSERT INTO team_policy_metadata(
        store_id, team_id, policy_version, schema_version,
        policy_epoch, global_epoch, real_content_state, updated_at)
    VALUES (
        NEW.store_id, NEW.team_id, 1, 40,
        1, NEW.auth_epoch, 'blocked', NEW.created_at);
END;
