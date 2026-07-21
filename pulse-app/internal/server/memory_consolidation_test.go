package server

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkkmnk/pulse/internal/consolidation"
	"github.com/nkkmnk/pulse/internal/store"
)

func TestConsolidationReportRouteRunsReadOnlyInventoryForBoundVault(t *testing.T) {
	home := t.TempDir()
	canonicalPath := filepath.Join(home, ".pulse", "current.db")
	if err := os.MkdirAll(filepath.Dir(canonicalPath), 0o700); err != nil {
		t.Fatal(err)
	}
	vault, err := store.OpenVault(canonicalPath, store.StoreKindPersonal, "store_personal_inventory_server")
	if err != nil {
		t.Fatal(err)
	}
	binding := strings.Repeat("e", 64)
	if err := vault.ConfigureProductRuntimeAuthority(binding, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := vault.ConfigureContinuityDeliveryAuthority(binding, "repository_inventory_server"); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(home, ".pulse-local", "legacy.db")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	legacyDB, err := sql.Open("sqlite", legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacyDB.Exec(`
		CREATE TABLE observations (id INTEGER PRIMARY KEY, source_kind TEXT, source_id TEXT, content_text TEXT);
		INSERT INTO observations VALUES (1, 'legacy', 'legacy_server', 'A legacy source item.');
	`); err != nil {
		t.Fatal(err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := consolidation.NewManager(consolidation.ManagerConfig{
		RootDir: filepath.Join(home, "report-test"),
		Key:     []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := consolidation.NewEngine(consolidation.EngineConfig{
		Manager: manager, HomeDir: home, CanonicalPath: canonicalPath, CanonicalDB: vault.DB(),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{
		IPCSecret: "secret", Store: vault,
		ConsolidationReports: manager, ConsolidationInventory: engine,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = vault.Close()
	})
	report := decodeConsolidationReport(t, pulseJSON(t, ts, http.MethodPost, "/memory/consolidation/reports", map[string]any{}))
	if report.Phase != consolidation.PhaseReportReady || report.Totals.Unique != 1 || report.InventoryDigest == "" {
		t.Fatalf("inventory report mismatch: %#v", report)
	}
	after, err := os.ReadFile(legacyPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("legacy source mutated: err=%v", err)
	}
}
