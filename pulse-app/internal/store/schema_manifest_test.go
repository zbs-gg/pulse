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

const frozenMigration034SHA256 = "fea59490f3117b2cd828aad7ca3c3f2930cc08bbd5cdc21c0414e703ba025822"
const frozenMigration035SHA256 = "4e474c2c7f015b8263681df457e8b5cdfdd7107af4aef10c97a788b1cdc67076"
const frozenMigration036SHA256 = "d5e574db7f1748f1a5f9518cb68fc7a5c5f0d06e6af9540ebbfb4233e6243b4a"
const frozenMigration037SHA256 = "36f9130df47e262700e62344e1f8ab3552d8a1ae046791db77d17999e20d8249"
const frozenMigration038SHA256 = "c9bdc775f1c22a05948c2ccb5d3f05866b98cc85533e55bfc9b64dc3d322a356"
const frozenMigration042SHA256 = "63dbb22a4c8eec43a2b8875eb3dcce2a02de81484963a441a393eb87df87b349"
const frozenMigration043SHA256 = "98d3d7ea68dfd282d9f1894d8a0108b2b4ff20dea773c6e56857660d320ac143"
const frozenMigration045SHA256 = "8f5d60553d7edffd23bafecc27e57a2d82614f266576d1d9c13e0e96b23923ed"
const frozenMigration046SHA256 = "7b2f0714a2f0235d73c388aa493ab3a770da8148c90a8419b8940d5edb14b72b"

func TestEmbeddedMigrationManifestIsContiguousAndFingerprinted(t *testing.T) {
	migrations, err := loadMigrationSet(migrationsFS)
	if err != nil {
		t.Fatalf("load embedded migrations: %v", err)
	}
	if got := migrations[len(migrations)-1].Version; got != 46 {
		t.Fatalf("latest migration = %d, want 46", got)
	}
	for i, migration := range migrations {
		if migration.Version != i+1 {
			t.Fatalf("migration[%d] version = %d, want %d", i, migration.Version, i+1)
		}
		if migration.SHA256 == "" {
			t.Fatalf("migration %d has empty fingerprint", migration.Version)
		}
	}
	if migrations[41].Name != "042_team_publication_desk_intents.sql" ||
		migrations[41].SHA256 != frozenMigration042SHA256 ||
		migrations[42].Name != "043_team_publication_commons_receipts.sql" ||
		migrations[42].SHA256 != frozenMigration043SHA256 ||
		migrations[44].Name != "045_memory_presentation_receipts.sql" ||
		migrations[44].SHA256 != frozenMigration045SHA256 ||
		migrations[45].Name != "046_continuity_delivery_receipts.sql" ||
		migrations[45].SHA256 != frozenMigration046SHA256 {
		t.Fatalf("post-foundation migration fingerprints changed: 042=%q 043=%q 045=%q 046=%q",
			migrations[41].SHA256, migrations[42].SHA256, migrations[44].SHA256, migrations[45].SHA256)
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

func TestMigrations035Through039UpgradeFrozenV34TeamStoreWithoutFingerprintDrift(t *testing.T) {
	migrations, err := loadMigrationSet(migrationsFS)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 46 {
		t.Fatalf("migration count = %d, want 46", len(migrations))
	}
	if migrations[33].Name != "034_team_object_policy.sql" ||
		migrations[33].SHA256 != frozenMigration034SHA256 ||
		migrations[34].Name != "035_team_memory.sql" ||
		migrations[34].SHA256 != frozenMigration035SHA256 ||
		migrations[35].Name != "036_team_graph_delta.sql" ||
		migrations[35].SHA256 != frozenMigration036SHA256 ||
		migrations[36].Name != "037_team_semantic_materializations.sql" ||
		migrations[36].SHA256 != frozenMigration037SHA256 ||
		migrations[37].Name != "038_team_deletion.sql" ||
		migrations[37].SHA256 != frozenMigration038SHA256 ||
		migrations[38].Name != "039_team_owner_activation.sql" {
		t.Fatalf("frozen migration boundary changed: names=%q/%q/%q/%q/%q/%q",
			migrations[33].Name, migrations[34].Name, migrations[35].Name,
			migrations[36].Name, migrations[37].Name, migrations[38].Name)
	}

	path := filepath.Join(t.TempDir(), "team-v34.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_meta (version INTEGER PRIMARY KEY, applied TEXT NOT NULL)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	for _, migration := range migrations[:34] {
		if _, err := db.Exec(migration.SQL); err != nil {
			db.Close()
			t.Fatalf("apply v34 fixture migration %d: %v", migration.Version, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_meta(version, applied) VALUES (?, 'v34-fixture')`, migration.Version); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if migration.Version == 33 {
			for _, fingerprinted := range migrations[:33] {
				if _, err := db.Exec(`
					INSERT INTO schema_migration_manifest(version, name, sha256, applied_at)
					VALUES (?, ?, ?, 'v34-fixture')`,
					fingerprinted.Version, fingerprinted.Name, fingerprinted.SHA256); err != nil {
					db.Close()
					t.Fatal(err)
				}
			}
		}
		if migration.Version == 34 {
			if _, err := db.Exec(`
				INSERT INTO schema_migration_manifest(version, name, sha256, applied_at)
				VALUES (34, '034_team_object_policy.sql', ?, 'v34-fixture')`, frozenMigration034SHA256); err != nil {
				db.Close()
				t.Fatal(err)
			}
		}
	}

	root := testBootstrapRoot()
	rootFingerprint, err := root.Fingerprint()
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO team_stores(
			singleton, store_id, team_id, team_name, min_reader_version,
			min_writer_version, durability_profile, auth_epoch,
			bootstrap_root_fingerprint, bootstrap_consumed_at, created_at)
		VALUES (1, 'store-v34', 'team-v34', 'Frozen v34 fixture', 34, 34,
			'wal-full-fk', 1, ?, 'v34-fixture', 'v34-fixture')`, rootFingerprint); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var preUpgradeSchema int
	if err := db.QueryRow(`SELECT schema_version FROM team_policy_metadata`).Scan(&preUpgradeSchema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if preUpgradeSchema != 34 {
		db.Close()
		t.Fatalf("v34 fixture policy schema = %d, want 34", preUpgradeSchema)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := OpenTeam(path, reviewTeamOptions(root))
	if err != nil {
		t.Fatalf("open frozen v34 team store: %v", err)
	}
	defer upgraded.Close()

	var schemaVersion, minReader, minWriter, manifestRows int
	if err := upgraded.DB().QueryRow(`SELECT schema_version FROM team_policy_metadata`).Scan(&schemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.DB().QueryRow(`SELECT min_reader_version, min_writer_version FROM team_stores`).Scan(&minReader, &minWriter); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.DB().QueryRow(`SELECT count(*) FROM schema_migration_manifest`).Scan(&manifestRows); err != nil {
		t.Fatal(err)
	}
	if schemaVersion != 44 || minReader != 44 || minWriter != 44 || manifestRows != 46 {
		t.Fatalf("v39 upgrade state: policy=%d reader=%d writer=%d manifest=%d", schemaVersion, minReader, minWriter, manifestRows)
	}
	for _, table := range []string{
		"team_memory_capsules", "team_memory_events", "team_memory_embeddings",
		"team_graph_delta_inputs", "team_semantic_projection_intents",
		"team_semantic_materializations", "team_graph_materializations",
		"team_assertion_materializations", "team_continuity_materializations",
		"team_semantic_embeddings",
		"team_deletion_operations", "team_deletion_frontier", "team_deletion_discharges",
		"team_owner_approvals", "team_remote_activation",
	} {
		var exists int
		if err := upgraded.DB().QueryRow(`
			SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != 1 {
			t.Fatalf("v39 upgrade missing %s", table)
		}
	}
}

func TestMigrations036Through039UpgradeFrozenV35TeamStoreWithoutFingerprintDrift(t *testing.T) {
	migrations, err := loadMigrationSet(migrationsFS)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 46 || migrations[34].Name != "035_team_memory.sql" ||
		migrations[34].SHA256 != frozenMigration035SHA256 ||
		migrations[35].Name != "036_team_graph_delta.sql" ||
		migrations[35].SHA256 != frozenMigration036SHA256 ||
		migrations[36].Name != "037_team_semantic_materializations.sql" ||
		migrations[36].SHA256 != frozenMigration037SHA256 ||
		migrations[37].Name != "038_team_deletion.sql" ||
		migrations[37].SHA256 != frozenMigration038SHA256 ||
		migrations[38].Name != "039_team_owner_activation.sql" {
		t.Fatalf("frozen v35 boundary changed: names=%q/%q/%q/%q/%q",
			migrations[34].Name, migrations[35].Name, migrations[36].Name,
			migrations[37].Name, migrations[38].Name)
	}

	path := filepath.Join(t.TempDir(), "team-v35.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_meta (version INTEGER PRIMARY KEY, applied TEXT NOT NULL)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	for _, migration := range migrations[:35] {
		if _, err := db.Exec(migration.SQL); err != nil {
			db.Close()
			t.Fatalf("apply v35 fixture migration %d: %v", migration.Version, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_meta(version, applied) VALUES (?, 'v35-fixture')`, migration.Version); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if migration.Version == 33 {
			for _, fingerprinted := range migrations[:33] {
				if _, err := db.Exec(`
					INSERT INTO schema_migration_manifest(version, name, sha256, applied_at)
					VALUES (?, ?, ?, 'v35-fixture')`,
					fingerprinted.Version, fingerprinted.Name, fingerprinted.SHA256); err != nil {
					db.Close()
					t.Fatal(err)
				}
			}
		}
		if migration.Version >= 34 {
			if _, err := db.Exec(`
				INSERT INTO schema_migration_manifest(version, name, sha256, applied_at)
				VALUES (?, ?, ?, 'v35-fixture')`, migration.Version, migration.Name, migration.SHA256); err != nil {
				db.Close()
				t.Fatal(err)
			}
		}
	}

	root := testBootstrapRoot()
	rootFingerprint, err := root.Fingerprint()
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO team_stores(
			singleton, store_id, team_id, team_name, min_reader_version,
			min_writer_version, durability_profile, auth_epoch,
			bootstrap_root_fingerprint, bootstrap_consumed_at, created_at)
		VALUES (1, 'store-v35', 'team-v35', 'Frozen v35 fixture', 35, 35,
			'wal-full-fk', 1, ?, 'v35-fixture', 'v35-fixture')`, rootFingerprint); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var before int
	if err := db.QueryRow(`SELECT schema_version FROM team_policy_metadata`).Scan(&before); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if before != 35 {
		db.Close()
		t.Fatalf("v35 fixture policy schema = %d, want 35", before)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := OpenTeam(path, reviewTeamOptions(root))
	if err != nil {
		t.Fatalf("open frozen v35 team store: %v", err)
	}
	defer upgraded.Close()

	var schemaVersion, minReader, minWriter, manifestRows int
	if err := upgraded.DB().QueryRow(`SELECT schema_version FROM team_policy_metadata`).Scan(&schemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.DB().QueryRow(`SELECT min_reader_version, min_writer_version FROM team_stores`).Scan(&minReader, &minWriter); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.DB().QueryRow(`SELECT count(*) FROM schema_migration_manifest`).Scan(&manifestRows); err != nil {
		t.Fatal(err)
	}
	if schemaVersion != 44 || minReader != 44 || minWriter != 44 || manifestRows != 46 {
		t.Fatalf("v39 upgrade state: policy=%d reader=%d writer=%d manifest=%d", schemaVersion, minReader, minWriter, manifestRows)
	}
	for _, table := range []string{
		"team_graph_delta_inputs", "team_semantic_projection_intents",
		"team_semantic_materializations", "team_graph_materializations",
		"team_assertion_materializations", "team_continuity_materializations",
		"team_semantic_embeddings",
		"team_deletion_operations", "team_deletion_frontier", "team_deletion_discharges",
		"team_owner_approvals", "team_remote_activation",
	} {
		var exists int
		if err := upgraded.DB().QueryRow(`
			SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != 1 {
			t.Fatalf("v39 upgrade missing %s", table)
		}
	}
}

func TestMigrations038And039UpgradeFrozenV37TeamStoreWithoutFingerprintDrift(t *testing.T) {
	migrations, err := loadMigrationSet(migrationsFS)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 46 || migrations[36].Name != "037_team_semantic_materializations.sql" ||
		migrations[36].SHA256 != frozenMigration037SHA256 ||
		migrations[37].Name != "038_team_deletion.sql" ||
		migrations[37].SHA256 != frozenMigration038SHA256 ||
		migrations[38].Name != "039_team_owner_activation.sql" {
		t.Fatalf("frozen v37 boundary changed: names=%q/%q/%q",
			migrations[36].Name, migrations[37].Name, migrations[38].Name)
	}

	path := filepath.Join(t.TempDir(), "team-v37.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_meta (version INTEGER PRIMARY KEY, applied TEXT NOT NULL)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	for _, migration := range migrations[:37] {
		if _, err := db.Exec(migration.SQL); err != nil {
			db.Close()
			t.Fatalf("apply v37 fixture migration %d: %v", migration.Version, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_meta(version, applied) VALUES (?, 'v37-fixture')`, migration.Version); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if migration.Version == 33 {
			for _, fingerprinted := range migrations[:33] {
				if _, err := db.Exec(`
					INSERT INTO schema_migration_manifest(version, name, sha256, applied_at)
					VALUES (?, ?, ?, 'v37-fixture')`,
					fingerprinted.Version, fingerprinted.Name, fingerprinted.SHA256); err != nil {
					db.Close()
					t.Fatal(err)
				}
			}
		}
		if migration.Version >= 34 {
			if _, err := db.Exec(`
				INSERT INTO schema_migration_manifest(version, name, sha256, applied_at)
				VALUES (?, ?, ?, 'v37-fixture')`, migration.Version, migration.Name, migration.SHA256); err != nil {
				db.Close()
				t.Fatal(err)
			}
		}
	}

	root := testBootstrapRoot()
	rootFingerprint, err := root.Fingerprint()
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO team_stores(
			singleton, store_id, team_id, team_name, min_reader_version,
			min_writer_version, durability_profile, auth_epoch,
			bootstrap_root_fingerprint, bootstrap_consumed_at, created_at)
		VALUES (1, 'store-v37', 'team-v37', 'Frozen v37 fixture', 37, 37,
			'wal-full-fk', 1, ?, 'v37-fixture', 'v37-fixture')`, rootFingerprint); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var before int
	if err := db.QueryRow(`SELECT schema_version FROM team_policy_metadata`).Scan(&before); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if before != 37 {
		db.Close()
		t.Fatalf("v37 fixture policy schema = %d, want 37", before)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := OpenTeam(path, reviewTeamOptions(root))
	if err != nil {
		t.Fatalf("open frozen v37 team store: %v", err)
	}
	defer upgraded.Close()

	var schemaVersion, minReader, minWriter, manifestRows int
	if err := upgraded.DB().QueryRow(`SELECT schema_version FROM team_policy_metadata`).Scan(&schemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.DB().QueryRow(`SELECT min_reader_version, min_writer_version FROM team_stores`).Scan(&minReader, &minWriter); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.DB().QueryRow(`SELECT count(*) FROM schema_migration_manifest`).Scan(&manifestRows); err != nil {
		t.Fatal(err)
	}
	if schemaVersion != 44 || minReader != 44 || minWriter != 44 || manifestRows != 46 {
		t.Fatalf("v39 upgrade state: policy=%d reader=%d writer=%d manifest=%d", schemaVersion, minReader, minWriter, manifestRows)
	}
	for _, table := range []string{
		"team_deletion_operations", "team_deletion_frontier", "team_deletion_discharges",
		"team_owner_approvals", "team_remote_activation",
	} {
		var exists int
		if err := upgraded.DB().QueryRow(`
			SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != 1 {
			t.Fatalf("v39 upgrade missing %s", table)
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
