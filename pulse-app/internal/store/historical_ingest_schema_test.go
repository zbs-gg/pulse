package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestHistoricalModelMigrationPreservesExistingJobsAndForeignKeys(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "store.db")+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
CREATE TABLE store_identity (
  singleton INTEGER PRIMARY KEY, store_id TEXT NOT NULL UNIQUE, store_kind TEXT NOT NULL,
  min_reader_version INTEGER NOT NULL, min_writer_version INTEGER NOT NULL, created_at TEXT NOT NULL
);
INSERT INTO store_identity VALUES (1, 'store_history_migration', 'personal', 43, 43, '2026-08-14T00:00:00Z');
CREATE TABLE historical_ingest_jobs (
  job_id TEXT PRIMARY KEY, store_id TEXT NOT NULL REFERENCES store_identity(store_id) ON DELETE RESTRICT,
  state TEXT NOT NULL, root_limit INTEGER NOT NULL, cutoff_at TEXT NOT NULL,
  source_snapshot_digest TEXT, parser_version TEXT NOT NULL, scrubber_version TEXT NOT NULL,
  prompt_version TEXT NOT NULL, schema_digest TEXT NOT NULL,
  model_id TEXT NOT NULL CHECK(model_id = 'gpt-5.6-luna'), model_effort TEXT NOT NULL,
  current_revision INTEGER NOT NULL, current_manifest_digest TEXT,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL, canceled_at TEXT
) WITHOUT ROWID;
CREATE INDEX idx_historical_ingest_jobs_state ON historical_ingest_jobs(store_id, state, updated_at DESC);
CREATE TABLE historical_ingest_children (
  child_id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL REFERENCES historical_ingest_jobs(job_id) ON DELETE RESTRICT
);
INSERT INTO historical_ingest_jobs VALUES (
  'job_0123456789abcdef', 'store_history_migration', 'retrieval_ready', 50,
  '2026-08-14T00:00:00Z', NULL, 'parser_v1', 'scrubber_v1', 'prompt_v1',
  '` + strings.Repeat("a", 64) + `', 'gpt-5.6-luna', 'low', 1, NULL,
  '2026-08-14T00:00:00Z', '2026-08-14T00:00:00Z', NULL
);
INSERT INTO historical_ingest_children VALUES ('child_1', 'job_0123456789abcdef');`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrationSet(migrationsFS)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(migrations[43].SQL); err != nil {
		_ = tx.Rollback()
		t.Fatalf("apply migration 044: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit migration 044: %v", err)
	}
	var modelID, executionModelID string
	if err := db.QueryRow(`SELECT model_id, execution_model_id FROM historical_ingest_jobs WHERE job_id='job_0123456789abcdef'`).Scan(&modelID, &executionModelID); err != nil || modelID != "gpt-5.6-luna" || executionModelID != "gpt-5.6-luna" {
		t.Fatalf("legacy job models=(%q,%q) err=%v", modelID, executionModelID, err)
	}
	if _, err := db.Exec(`
INSERT INTO historical_ingest_jobs(
  job_id, store_id, state, root_limit, cutoff_at, source_snapshot_digest,
  parser_version, scrubber_version, prompt_version, schema_digest, model_id,
  model_effort, current_revision, current_manifest_digest, created_at, updated_at,
  canceled_at, execution_model_id
) VALUES (
  'job_fedcba9876543210', 'store_history_migration', 'preflight', 50, ?, NULL,
  'parser_v1', 'scrubber_v1', 'prompt_v2', ?, 'gpt-5.6-luna', 'low', 0, NULL, ?, ?, NULL, 'gpt-5.4'
)`, time.Now().UTC().Format(time.RFC3339Nano), strings.Repeat("b", 64), time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert GPT-5.4 job: %v", err)
	}
	var foreignKeyErrors int
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		foreignKeyErrors++
	}
	_ = rows.Close()
	if foreignKeyErrors != 0 {
		t.Fatalf("foreign key errors=%d", foreignKeyErrors)
	}
	var floor int
	if err := db.QueryRow(`SELECT min_writer_version FROM store_identity WHERE singleton=1`).Scan(&floor); err != nil || floor != 44 {
		t.Fatalf("writer floor=%d err=%v", floor, err)
	}
}

func TestPersonalHistoricalIngestMigrationAppliesOnlyToPersonal(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		kind        StoreKind
		wantApplied bool
	}{
		{name: "personal", kind: StoreKindPersonal, wantApplied: true},
		{name: "local preview", kind: StoreKindLocalPreview, wantApplied: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "store.db")+"?_pragma=foreign_keys(ON)")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			profile := storeOpenProfile{Kind: test.kind}
			if test.kind == StoreKindPersonal {
				profile.ExpectedStoreID = "store_history_contract"
			}
			if err := migrateForProfile(db, profile); err != nil {
				t.Fatalf("migrate %s: %v", test.kind, err)
			}

			var disposition string
			if err := db.QueryRow(`SELECT disposition FROM schema_migration_applicability WHERE version = 41`).Scan(&disposition); err != nil {
				t.Fatalf("migration 041 applicability: %v", err)
			}
			wantDisposition := "skipped"
			if test.wantApplied {
				wantDisposition = "applied"
			}
			if disposition != wantDisposition {
				t.Fatalf("migration 041 disposition = %q, want %q", disposition, wantDisposition)
			}

			var tableCount int
			if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'historical_ingest_jobs'`).Scan(&tableCount); err != nil {
				t.Fatal(err)
			}
			if got := tableCount == 1; got != test.wantApplied {
				t.Fatalf("historical_ingest_jobs present = %v, want %v", got, test.wantApplied)
			}
		})
	}
}

func TestPersonalHistoricalIngestMigrationIsRestartSafe(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "personal.db")
	first, err := OpenVault(path, StoreKindPersonal, "store_history_restart")
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := OpenVault(path, StoreKindPersonal, "store_history_restart")
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer second.Close()

	var count int
	if err := second.DB().QueryRow(`SELECT count(*) FROM schema_meta WHERE version = 41`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("schema_meta v41 rows = %d, want 1", count)
	}
}
