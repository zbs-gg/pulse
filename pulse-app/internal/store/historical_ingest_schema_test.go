package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigration053AppliesOnlyToPersonalAndDeskStores(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		kind        StoreKind
		wantApplied bool
	}{
		{name: "personal", kind: StoreKindPersonal, wantApplied: true},
		{name: "desk", kind: StoreKindDesk, wantApplied: true},
		{name: "commons", kind: StoreKindCommons, wantApplied: false},
		{name: "local preview", kind: StoreKindLocalPreview, wantApplied: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "store.db")+"?_pragma=foreign_keys(ON)")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()

			profile := storeOpenProfile{Kind: test.kind}
			if test.kind == StoreKindPersonal || test.kind == StoreKindDesk {
				profile.ExpectedStoreID = "store_history_contract"
			}
			if err := migrateForProfile(db, profile); err != nil {
				t.Fatalf("migrate %s: %v", test.kind, err)
			}

			var disposition string
			if err := db.QueryRow(`SELECT disposition FROM schema_migration_applicability WHERE version = 53`).Scan(&disposition); err != nil {
				t.Fatalf("migration 053 applicability: %v", err)
			}
			wantDisposition := "skipped"
			if test.wantApplied {
				wantDisposition = "applied"
			}
			if disposition != wantDisposition {
				t.Fatalf("migration 053 disposition = %q, want %q", disposition, wantDisposition)
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

func TestMigration053IsRestartSafe(t *testing.T) {
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
	if err := second.DB().QueryRow(`SELECT count(*) FROM schema_meta WHERE version = 53`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("schema_meta v53 rows = %d, want 1", count)
	}
}
