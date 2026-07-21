package consolidation

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func createSQLiteFixture(t *testing.T, path string, statements ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("fixture statement failed: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func pulseObservationSchema() string {
	return `CREATE TABLE observations (
		id INTEGER PRIMARY KEY,
		source_kind TEXT NOT NULL,
		source_id TEXT NOT NULL,
		content_text TEXT
	)`
}

func newInventoryFixture(t *testing.T, home, canonicalPath string, limits Limits) (*Manager, *Engine, Destination) {
	t.Helper()
	manager, err := NewManager(ManagerConfig{
		RootDir: filepath.Join(home, "reports"),
		Key:     []byte("0123456789abcdef0123456789abcdef"),
		Clock:   func() time.Time { return time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC) },
		NewID:   func() string { return "report_inventory" },
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(EngineConfig{
		Manager: manager, HomeDir: home, CanonicalPath: canonicalPath, Limits: limits,
		Clock: func() time.Time { return time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	destination := Destination{
		StoreKind: "personal", StoreID: "store_personal_inventory",
		BindingDigest: strings.Repeat("d", 64), RepositoryID: "repository_inventory",
	}
	return manager, engine, destination
}

func sourceByClass(t *testing.T, report Report, classification string) Source {
	t.Helper()
	for _, source := range report.Sources {
		if source.Classification == classification {
			return source
		}
	}
	t.Fatalf("missing source classification %q: %#v", classification, report.Sources)
	return Source{}
}

func TestInventoryIsReadOnlyAndSeparatesDeterministicOverlap(t *testing.T) {
	home := t.TempDir()
	canonical := filepath.Join(home, ".pulse", "current.db")
	legacy := filepath.Join(home, ".pulse-local", "legacy.db")
	claudeMem := filepath.Join(home, ".claude-mem", "claude-mem.db")
	createSQLiteFixture(t, canonical,
		`CREATE TABLE store_identity (singleton INTEGER PRIMARY KEY, store_id TEXT)`,
		pulseObservationSchema(),
		`INSERT INTO store_identity VALUES (1, 'store_personal_inventory')`,
		`INSERT INTO observations VALUES
			(1, 'claude-mem', 'claude-mem:obs:1', 'Use scoped project memory.'),
			(2, 'legacy', 'legacy-changed', 'Keep budget at 10.')`,
	)
	createSQLiteFixture(t, legacy,
		pulseObservationSchema(),
		`INSERT INTO observations VALUES
			(1, 'legacy', 'legacy-changed', 'Keep budget at 20.'),
			(2, 'legacy', 'legacy-unique', 'A unique legacy decision.'),
			(3, 'legacy', 'legacy-copy', 'Use scoped project memory.')`,
	)
	createSQLiteFixture(t, claudeMem,
		`CREATE TABLE schema_versions (version INTEGER PRIMARY KEY)`,
		`INSERT INTO schema_versions VALUES (32)`,
		`CREATE TABLE observations (id INTEGER PRIMARY KEY, project TEXT, text TEXT)`,
		`INSERT INTO observations VALUES (1, 'pulse', 'Use scoped project memory.')`,
		`CREATE TABLE session_summaries (
			id INTEGER PRIMARY KEY, project TEXT, request TEXT, investigated TEXT,
			learned TEXT, completed TEXT, next_steps TEXT, notes TEXT
		)`,
		`INSERT INTO session_summaries VALUES
			(7, 'pulse', 'Find old memory', NULL, 'A distinct Claude summary.', NULL, NULL, NULL)`,
		`CREATE TABLE user_prompts (id INTEGER PRIMARY KEY, prompt_text TEXT)`,
		`INSERT INTO user_prompts VALUES (1, 'raw prompt excluded')`,
	)

	beforeCanonical, _ := os.ReadFile(canonical)
	beforeLegacy, _ := os.ReadFile(legacy)
	beforeClaude, _ := os.ReadFile(claudeMem)
	manager, engine, destination := newInventoryFixture(t, home, canonical, DefaultLimits())
	started, _, err := manager.Start(destination)
	if err != nil {
		t.Fatal(err)
	}
	report, err := engine.Run(context.Background(), started.InvocationID, destination)
	if err != nil {
		t.Fatal(err)
	}
	if report.Phase != PhaseReportReady || report.InventoryDigest == "" {
		t.Fatalf("report not ready: %#v", report)
	}
	if report.Totals.AlreadyRepresented != 2 || report.Totals.Ambiguous != 1 || report.Totals.Unique != 2 || report.Totals.Excluded < 1 {
		t.Fatalf("unexpected deterministic totals: %#v", report.Totals)
	}
	canonicalSource := sourceByClass(t, report, ClassificationCanonicalVault)
	if canonicalSource.Counts["source_rows"] != 2 || canonicalSource.ReasonCode != "signed_bound_destination" {
		t.Fatalf("canonical counts: %#v", canonicalSource)
	}
	legacySource := sourceByClass(t, report, ClassificationLegacyPulseDB)
	if legacySource.Counts["changed_content"] != 1 || legacySource.Counts["same_normalized_content"] != 1 || legacySource.Counts["unique_material"] != 1 {
		t.Fatalf("legacy overlap classes: %#v", legacySource)
	}
	claudeSource := sourceByClass(t, report, ClassificationClaudeMem)
	if claudeSource.Counts["same_stable_source"] != 1 || claudeSource.Counts["unique_material"] != 1 || claudeSource.Counts["excluded_material"] < 1 {
		t.Fatalf("claude-mem overlap classes: %#v", claudeSource)
	}
	for path, before := range map[string][]byte{canonical: beforeCanonical, legacy: beforeLegacy, claudeMem: beforeClaude} {
		after, err := os.ReadFile(path)
		if err != nil || string(after) != string(before) {
			t.Fatalf("source mutated: %s err=%v", path, err)
		}
	}
	encoded, _ := os.ReadFile(filepath.Join(manager.RootDir(), "aliases-"+started.InvocationID+".json"))
	if len(encoded) == 0 {
		t.Fatal("missing owner-only alias sidecar")
	}
	if info, err := os.Stat(filepath.Join(manager.RootDir(), "aliases-"+started.InvocationID+".json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("alias sidecar mode: info=%v err=%v", info, err)
	}
	portable, _ := reportJSON(report)
	if strings.Contains(portable, home) || strings.Contains(portable, "raw prompt excluded") || strings.Contains(portable, "unique legacy") {
		t.Fatalf("portable report leaked private material: %s", portable)
	}
	changedDB, err := sql.Open("sqlite", legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := changedDB.Exec(`INSERT INTO observations VALUES (4, 'legacy', 'later', 'Changed after report.')`); err != nil {
		t.Fatal(err)
	}
	if err := changedDB.Close(); err != nil {
		t.Fatal(err)
	}
	stale, err := engine.EnsureFresh(report.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Phase != PhaseStale || !containsCode(stale.ReasonCodes, "source_changed") {
		t.Fatalf("changed source did not stale report: %#v", stale)
	}
}

func TestInventoryMakesUnsupportedClaudeSchemaAndSymlinkPartial(t *testing.T) {
	home := t.TempDir()
	canonical := filepath.Join(home, ".pulse", "current.db")
	unsupported := filepath.Join(home, ".claude-mem", "claude-mem.db")
	outside := filepath.Join(t.TempDir(), "outside.db")
	createSQLiteFixture(t, canonical,
		`CREATE TABLE store_identity (singleton INTEGER PRIMARY KEY, store_id TEXT)`, pulseObservationSchema(),
	)
	createSQLiteFixture(t, unsupported,
		`CREATE TABLE schema_versions (version INTEGER PRIMARY KEY)`,
		`INSERT INTO schema_versions VALUES (99)`,
		`CREATE TABLE observations (id INTEGER PRIMARY KEY, project TEXT, text TEXT)`,
		`CREATE TABLE session_summaries (id INTEGER PRIMARY KEY, project TEXT, request TEXT)`,
	)
	createSQLiteFixture(t, outside, `CREATE TABLE observations (id INTEGER PRIMARY KEY, source_id TEXT, content_text TEXT)`)
	if err := os.Symlink(outside, filepath.Join(home, ".pulse", "linked.db")); err != nil {
		t.Fatal(err)
	}
	beforeUnsupported, _ := os.ReadFile(unsupported)
	manager, engine, destination := newInventoryFixture(t, home, canonical, DefaultLimits())
	started, _, _ := manager.Start(destination)
	report, err := engine.Run(context.Background(), started.InvocationID, destination)
	if err != nil {
		t.Fatal(err)
	}
	if report.Phase != PhasePartial || len(report.Blockers) < 2 {
		t.Fatalf("unsafe sources did not yield partial: %#v", report)
	}
	if sourceByClass(t, report, ClassificationClaudeMem).ReasonCode != "unsupported_schema" {
		t.Fatalf("unsupported Claude schema not explicit: %#v", report.Sources)
	}
	afterUnsupported, _ := os.ReadFile(unsupported)
	if string(afterUnsupported) != string(beforeUnsupported) {
		t.Fatal("unsupported source changed")
	}
}

func TestInventoryResourceLimitStopsWithoutRetryOrMutation(t *testing.T) {
	home := t.TempDir()
	canonical := filepath.Join(home, ".pulse", "current.db")
	createSQLiteFixture(t, canonical,
		`CREATE TABLE store_identity (singleton INTEGER PRIMARY KEY, store_id TEXT)`, pulseObservationSchema(),
		`INSERT INTO observations VALUES (1, 'x', 'one', 'one'), (2, 'x', 'two', 'two')`,
	)
	limits := DefaultLimits()
	limits.MaxRowsPerSource = 1
	manager, engine, destination := newInventoryFixture(t, home, canonical, limits)
	started, _, _ := manager.Start(destination)
	report, err := engine.Run(context.Background(), started.InvocationID, destination)
	if err != nil {
		t.Fatal(err)
	}
	if report.Phase != PhasePartial || !containsCode(report.ReasonCodes, "resource_limit") {
		t.Fatalf("resource limit not surfaced once: %#v", report)
	}
}

func TestInventoryRefusesActiveLegacyWALWithoutTouchingIt(t *testing.T) {
	home := t.TempDir()
	canonical := filepath.Join(home, ".pulse", "current.db")
	legacy := filepath.Join(home, ".pulse-live", "live.db")
	createSQLiteFixture(t, canonical,
		`CREATE TABLE store_identity (singleton INTEGER PRIMARY KEY, store_id TEXT)`, pulseObservationSchema(),
	)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	live, err := sql.Open("sqlite", legacy)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if _, err := live.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := live.Exec(pulseObservationSchema()); err != nil {
		t.Fatal(err)
	}
	if _, err := live.Exec(`INSERT INTO observations VALUES (1, 'legacy', 'live', 'Live WAL item.')`); err != nil {
		t.Fatal(err)
	}
	walPath := legacy + "-wal"
	beforeWAL, err := os.ReadFile(walPath)
	if err != nil || len(beforeWAL) == 0 {
		t.Fatalf("fixture WAL unavailable: bytes=%d err=%v", len(beforeWAL), err)
	}
	manager, engine, destination := newInventoryFixture(t, home, canonical, DefaultLimits())
	started, _, _ := manager.Start(destination)
	report, err := engine.Run(context.Background(), started.InvocationID, destination)
	if err != nil {
		t.Fatal(err)
	}
	legacySource := sourceByClass(t, report, ClassificationLegacyPulseDB)
	if report.Phase != PhasePartial || legacySource.ReasonCode != "active_wal" {
		t.Fatalf("active WAL not reported safely: %#v", report)
	}
	afterWAL, err := os.ReadFile(walPath)
	if err != nil || string(afterWAL) != string(beforeWAL) {
		t.Fatalf("active WAL changed: err=%v", err)
	}
}

func reportJSON(report Report) (string, error) {
	encoded, err := json.Marshal(report)
	return string(encoded), err
}

func containsCode(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
