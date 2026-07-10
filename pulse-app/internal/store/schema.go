package store

import (
	"crypto/sha256"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type migrationDescriptor struct {
	Version int
	Name    string
	SQL     string
	SHA256  string
}

func loadMigrationSet(fsys fs.FS) ([]migrationDescriptor, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	migrations := make([]migrationDescriptor, 0, len(names))
	for _, name := range names {
		var version int
		if _, err := fmt.Sscanf(name, "%03d_", &version); err != nil {
			return nil, fmt.Errorf("parse migration name %q: %w", name, err)
		}
		body, err := fs.ReadFile(fsys, "migrations/"+name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		digest := sha256.Sum256(body)
		migrations = append(migrations, migrationDescriptor{
			Version: version,
			Name:    name,
			SQL:     string(body),
			SHA256:  fmt.Sprintf("%x", digest),
		})
	}
	if err := validateMigrationSequence(migrations); err != nil {
		return nil, err
	}
	return migrations, nil
}

func validateMigrationSequence(migrations []migrationDescriptor) error {
	if len(migrations) == 0 {
		return fmt.Errorf("migration sequence is empty")
	}
	for i, migration := range migrations {
		want := i + 1
		if migration.Version != want {
			return fmt.Errorf("migration sequence at position %d has version %d, want %d", i, migration.Version, want)
		}
	}
	return nil
}

// migrate applies pending migrations in order and verifies the immutable
// fingerprint manifest introduced by migration 033.
func migrate(db *sql.DB) error {
	if _, err := db.Exec(`
        CREATE TABLE IF NOT EXISTS schema_meta (
            version INTEGER PRIMARY KEY,
            applied TEXT NOT NULL
        )
    `); err != nil {
		return fmt.Errorf("create schema_meta: %w", err)
	}

	migrations, err := loadMigrationSet(migrationsFS)
	if err != nil {
		return err
	}
	current, err := validateAppliedSequence(db, len(migrations))
	if err != nil {
		return err
	}
	if err := validateStoredManifest(db, migrations, current); err != nil {
		return err
	}

	for _, migration := range migrations {
		if migration.Version <= current {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin %s: %w", migration.Name, err)
		}
		if _, err := tx.Exec(migration.SQL); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply %s: %w", migration.Name, err)
		}
		appliedAt := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.Exec(
			"INSERT INTO schema_meta(version, applied) VALUES (?, ?)",
			migration.Version, appliedAt,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record %s: %w", migration.Name, err)
		}
		if migration.Version >= 33 {
			// Migration 033 adopts all earlier migrations into the immutable
			// fingerprint chain in the same transaction that creates it.
			start := migration.Version - 1
			if migration.Version == 33 {
				start = 0
			}
			for _, item := range migrations[start:migration.Version] {
				if _, err := tx.Exec(`
					INSERT INTO schema_migration_manifest(version, name, sha256, applied_at)
					VALUES (?, ?, ?, ?)`, item.Version, item.Name, item.SHA256, appliedAt); err != nil {
					_ = tx.Rollback()
					return fmt.Errorf("record migration fingerprint %s: %w", item.Name, err)
				}
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s: %w", migration.Name, err)
		}
		current = migration.Version
	}
	return validateStoredManifest(db, migrations, current)
}

func validateAppliedSequence(db *sql.DB, latest int) (int, error) {
	rows, err := db.Query(`SELECT version FROM schema_meta ORDER BY version`)
	if err != nil {
		return 0, fmt.Errorf("read schema versions: %w", err)
	}
	defer rows.Close()
	current := 0
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return 0, fmt.Errorf("scan schema version: %w", err)
		}
		if version != current+1 {
			return 0, fmt.Errorf("applied migration sequence has version %d after %d", version, current)
		}
		if version > latest {
			return 0, fmt.Errorf("database schema version %d is newer than binary version %d", version, latest)
		}
		current = version
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read schema versions: %w", err)
	}
	return current, nil
}

func validateStoredManifest(db *sql.DB, migrations []migrationDescriptor, current int) error {
	var exists int
	if err := db.QueryRow(`
		SELECT count(*) FROM sqlite_master
		 WHERE type = 'table' AND name = 'schema_migration_manifest'`).Scan(&exists); err != nil {
		return fmt.Errorf("inspect migration manifest: %w", err)
	}
	if exists == 0 {
		if current >= 33 {
			return fmt.Errorf("migration fingerprint manifest missing at schema version %d", current)
		}
		return nil
	}
	if current < 33 {
		return fmt.Errorf("migration fingerprint manifest exists before migration 033")
	}

	rows, err := db.Query(`SELECT version, name, sha256 FROM schema_migration_manifest ORDER BY version`)
	if err != nil {
		return fmt.Errorf("read migration fingerprint manifest: %w", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var version int
		var name, fingerprint string
		if err := rows.Scan(&version, &name, &fingerprint); err != nil {
			return fmt.Errorf("scan migration fingerprint: %w", err)
		}
		seen++
		if version != seen || version > current {
			return fmt.Errorf("migration fingerprint sequence invalid at version %d", version)
		}
		want := migrations[version-1]
		if name != want.Name || fingerprint != want.SHA256 {
			return fmt.Errorf("migration fingerprint mismatch at version %d", version)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read migration fingerprint manifest: %w", err)
	}
	if seen != current {
		return fmt.Errorf("migration fingerprint manifest has %d rows, want %d", seen, current)
	}
	return nil
}
