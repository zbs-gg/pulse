package store

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	_ "modernc.org/sqlite"
)

func TestEmbeddedMigrationManifestIsContiguousAndFingerprinted(t *testing.T) {
	migrations, err := loadMigrationSet(migrationsFS)
	if err != nil {
		t.Fatalf("load embedded migrations: %v", err)
	}
	if got := migrations[len(migrations)-1].Version; got != 33 {
		t.Fatalf("latest migration = %d, want 33", got)
	}
	for i, migration := range migrations {
		if migration.Version != i+1 {
			t.Fatalf("migration[%d] version = %d, want %d", i, migration.Version, i+1)
		}
		if migration.SHA256 == "" {
			t.Fatalf("migration %d has empty fingerprint", migration.Version)
		}
	}

	s, err := Open(filepath.Join(t.TempDir(), "pulse.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	for _, migration := range migrations {
		var name, fingerprint string
		if err := s.DB().QueryRow(`
			SELECT name, sha256
			  FROM schema_migration_manifest
			 WHERE version = ?`, migration.Version).Scan(&name, &fingerprint); err != nil {
			t.Fatalf("manifest row %d: %v", migration.Version, err)
		}
		if name != migration.Name || fingerprint != migration.SHA256 {
			t.Fatalf("manifest row %d = (%q, %q), want (%q, %q)", migration.Version, name, fingerprint, migration.Name, migration.SHA256)
		}
	}
}

func TestMigrationSetRejectsGapDuplicateAndReorder(t *testing.T) {
	cases := []struct {
		name       string
		migrations []migrationDescriptor
	}{
		{name: "gap", migrations: []migrationDescriptor{{Version: 1}, {Version: 3}}},
		{name: "duplicate", migrations: []migrationDescriptor{{Version: 1}, {Version: 1}}},
		{name: "reorder", migrations: []migrationDescriptor{{Version: 2}, {Version: 1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateMigrationSequence(tc.migrations); err == nil {
				t.Fatal("expected invalid migration sequence")
			}
		})
	}

	_, err := loadMigrationSet(fstest.MapFS{
		"migrations/001_one.sql":   &fstest.MapFile{Data: []byte("SELECT 1;")},
		"migrations/001_again.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
	})
	if err == nil || !strings.Contains(err.Error(), "migration sequence") {
		t.Fatalf("duplicate versions error = %v", err)
	}
}

func TestMigrationManifestFingerprintDriftFailsClosed(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "pulse.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	dbPath := s.DBPath()
	if _, err := s.DB().Exec(`DROP TRIGGER schema_migration_manifest_no_update`); err != nil {
		t.Fatalf("drop immutability trigger for corruption fixture: %v", err)
	}
	if _, err := s.DB().Exec(`UPDATE schema_migration_manifest SET sha256 = ? WHERE version = 1`, strings.Repeat("0", 64)); err != nil {
		t.Fatalf("corrupt fingerprint fixture: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(dbPath); err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("Open error = %v, want fingerprint failure", err)
	}
}

func TestMigration033FailureRollsBackAndRestartAppliesOnce(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pulse.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_meta (version INTEGER PRIMARY KEY, applied TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrationSet(migrationsFS)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:32] {
		if _, err := db.Exec(migration.SQL); err != nil {
			t.Fatalf("apply fixture migration %d: %v", migration.Version, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_meta(version, applied) VALUES (?, 'fixture')`, migration.Version); err != nil {
			t.Fatal(err)
		}
	}
	// Force 033 to fail after it has begun creating its schema.
	if _, err := db.Exec(`CREATE TABLE team_principals (conflict TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err == nil || !strings.Contains(err.Error(), "033_team_identity.sql") {
		t.Fatalf("migrate error = %v, want migration 033 failure", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='team_stores'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed migration left team_stores behind")
	}
	if _, err := db.Exec(`DROP TABLE team_principals`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("restart migration: %v", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM schema_meta WHERE version = 33`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("schema_meta rows for 033 = %d, want 1", count)
	}
	if err := db.QueryRow(`SELECT count(*) FROM schema_migration_manifest`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(migrations) {
		t.Fatalf("manifest rows = %d, want %d", count, len(migrations))
	}
}

func TestMigrationFingerprintUsesExactBytes(t *testing.T) {
	set, err := loadMigrationSet(fstest.MapFS{
		"migrations/001_one.sql": &fstest.MapFile{Data: []byte("SELECT 1;\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%x", sha256.Sum256([]byte("SELECT 1;\n")))
	if set[0].SHA256 != want {
		t.Fatalf("fingerprint = %q, want %q", set[0].SHA256, want)
	}
}
