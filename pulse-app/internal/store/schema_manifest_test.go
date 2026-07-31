package store

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestEmbeddedPersonalMigrationManifestIsContiguousAndFingerprinted(t *testing.T) {
	migrations, err := loadMigrationSet(migrationsFS)
	if err != nil {
		t.Fatalf("load embedded migrations: %v", err)
	}
	if got := migrations[len(migrations)-1].Version; got != latestSchemaVersion {
		t.Fatalf("latest migration = %d, want %d", got, latestSchemaVersion)
	}
	for index, migration := range migrations {
		if migration.Version != index+1 {
			t.Fatalf("migration[%d] version = %d, want %d", index, migration.Version, index+1)
		}
		if migration.SHA256 == "" {
			t.Fatalf("migration %d has empty fingerprint", migration.Version)
		}
		if migration.Version >= firstPersonalMigrationVersion && !migration.Policy.StoreKinds[StoreKindPersonal] {
			t.Fatalf("Personal migration %d does not apply to Personal", migration.Version)
		}
	}
}

func TestPublishedV067DatabaseUpgradesWithoutLosingMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "personal-v067.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_meta (version INTEGER PRIMARY KEY, applied TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrationSet(migrationsFS)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:32] {
		if _, err := db.Exec(migration.SQL); err != nil {
			t.Fatalf("apply published migration %s: %v", migration.Name, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_meta(version, applied) VALUES (?, ?)`, migration.Version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	const memoryID = "pulse:legacy-v067-memory"
	const memoryText = "A published 0.6.7 memory survives the Personal upgrade."
	if _, err := db.Exec(`
		INSERT INTO memory_capsules(
		  id, schema_version, source_host, conversation_scope, source_timestamp,
		  kind, redacted_summary, confidence, evidence_hint, privacy_tier,
		  retention, tags, created_at, status)
		VALUES (?, 'pulse.memory_capsule.v1', 'codex', 'project_context', ?,
		        'decision', ?, 1, 'user_confirmed', 'normal', 'long_term', '[]', ?, 'active')`,
		memoryID, time.Now().UTC().Format(time.RFC3339), memoryText, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	personal, err := OpenVault(path, StoreKindPersonal, "store_personal_upgrade_test")
	if err != nil {
		t.Fatalf("open upgraded Personal database: %v", err)
	}
	defer personal.Close()
	var version int
	if err := personal.DB().QueryRow(`SELECT max(version) FROM schema_meta`).Scan(&version); err != nil || version != latestSchemaVersion {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
	var gotText, gotScope string
	if err := personal.DB().QueryRow(`
		SELECT capsule.redacted_summary, object.memory_scope
		  FROM memory_capsules capsule
		  JOIN private_memory_objects object ON object.object_id=capsule.id
		 WHERE capsule.id=?`, memoryID).Scan(&gotText, &gotScope); err != nil {
		t.Fatalf("read adopted memory: %v", err)
	}
	if gotText != memoryText || gotScope != MemoryScopePersonalGlobal {
		t.Fatalf("adopted memory=(%q,%q), want (%q,%q)", gotText, gotScope, memoryText, MemoryScopePersonalGlobal)
	}
}

func TestUnpublishedTeamDatabaseIsRefusedWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unpublished.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE team_stores(id TEXT PRIMARY KEY); INSERT INTO team_stores(id) VALUES ('unpublished')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeDigest := sha256.Sum256(before)
	if _, err := OpenVault(path, StoreKindPersonal, "store_personal_refusal_test"); !errors.Is(err, ErrUnsupportedTeamDatabase) {
		t.Fatalf("open error=%v, want ErrUnsupportedTeamDatabase", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if afterDigest := sha256.Sum256(after); afterDigest != beforeDigest {
		t.Fatal("refused database was modified")
	}
}
