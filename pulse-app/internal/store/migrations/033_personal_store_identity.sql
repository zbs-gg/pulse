-- Pulse Personal store identity and migration applicability. This is the
-- first migration after the published 0.6.7 schema.

CREATE TABLE schema_migration_manifest (
    version     INTEGER PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    sha256      TEXT NOT NULL CHECK(length(sha256) = 64),
    applied_at  TEXT NOT NULL
);

CREATE TRIGGER schema_migration_manifest_no_update
BEFORE UPDATE ON schema_migration_manifest
BEGIN SELECT RAISE(ABORT, 'migration manifest is immutable'); END;

CREATE TRIGGER schema_migration_manifest_no_delete
BEFORE DELETE ON schema_migration_manifest
BEGIN SELECT RAISE(ABORT, 'migration manifest is immutable'); END;

CREATE TABLE store_identity (
    singleton           INTEGER PRIMARY KEY CHECK(singleton = 1),
    store_id            TEXT NOT NULL UNIQUE,
    store_kind          TEXT NOT NULL CHECK(store_kind IN ('local-preview', 'personal')),
    min_reader_version  INTEGER NOT NULL CHECK(min_reader_version >= 33),
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
    store_kind          TEXT NOT NULL CHECK(store_kind IN ('local-preview', 'personal')),
    disposition         TEXT NOT NULL CHECK(disposition IN ('applied', 'skipped')),
    min_reader_version  INTEGER NOT NULL CHECK(min_reader_version >= 33),
    min_writer_version  INTEGER NOT NULL CHECK(min_writer_version >= min_reader_version),
    recorded_at         TEXT NOT NULL
);

CREATE TRIGGER schema_migration_applicability_no_update
BEFORE UPDATE ON schema_migration_applicability
BEGIN SELECT RAISE(ABORT, 'migration applicability is immutable'); END;

CREATE TRIGGER schema_migration_applicability_no_delete
BEFORE DELETE ON schema_migration_applicability
BEGIN SELECT RAISE(ABORT, 'migration applicability is immutable'); END;
