package consolidation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReportKeepsRequiredCollectionsAsJSONArrays(t *testing.T) {
	manager, err := NewManager(ManagerConfig{
		RootDir: filepath.Join(t.TempDir(), "reports"),
		Key:     []byte("0123456789abcdef0123456789abcdef"),
		NewID:   func() string { return "report_arrays" },
	})
	if err != nil {
		t.Fatal(err)
	}
	report, _, err := manager.Start(testDestination())
	if err != nil {
		t.Fatal(err)
	}
	report, err = manager.Advance(
		report.InvocationID, PhaseInventory, Totals{}, []Source{}, nil, nil,
		"Inspecting recognized local memory sources.", "",
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, `"sources":null`) || strings.Contains(text, `"blockers":null`) {
		t.Fatalf("required report collections must be arrays: %s", text)
	}
}

func testDestination() Destination {
	return Destination{
		StoreKind:     "personal",
		StoreID:       "store_personal_contract",
		BindingDigest: strings.Repeat("a", 64),
		RepositoryID:  "repository_pulse",
	}
}

func TestManagerStartIsLeasedAndPortable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "reports")
	clock := time.Date(2026, 7, 21, 8, 0, 0, 0, time.UTC)
	manager, err := NewManager(ManagerConfig{
		RootDir: root,
		Key:     []byte("0123456789abcdef0123456789abcdef"),
		Clock:   func() time.Time { return clock },
		NewID:   func() string { return "report_01" },
	})
	if err != nil {
		t.Fatal(err)
	}

	first, reused, err := manager.Start(testDestination())
	if err != nil {
		t.Fatal(err)
	}
	if reused || first.Schema != ReportSchema || first.Phase != PhasePlanned || first.InvocationID != "report_01" {
		t.Fatalf("unexpected first report: %#v reused=%v", first, reused)
	}
	second, reused, err := manager.Start(testDestination())
	if err != nil {
		t.Fatal(err)
	}
	if !reused || second.InvocationID != first.InvocationID || second.InputDigest != first.InputDigest {
		t.Fatalf("same input did not reuse lease: %#v %#v reused=%v", first, second, reused)
	}
	if first.Destination.BindingDigest == "" || first.ReportDigest == "" || first.InputDigest == "" {
		t.Fatalf("missing authority digest: %#v", first)
	}
	if len(first.Sources) != 0 || first.Totals != (Totals{}) {
		t.Fatalf("new contract invented inventory: %#v", first)
	}

	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("report root mode=%o", info.Mode().Perm())
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("checkpoint count=%d", len(entries))
	}
	checkpoint, err := os.Stat(filepath.Join(root, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Mode().Perm() != 0o600 {
		t.Fatalf("checkpoint mode=%o", checkpoint.Mode().Perm())
	}
}

func TestManagerCancelTargetsInvocationAndResumeUsesCommittedGeneration(t *testing.T) {
	ids := []string{"report_old", "report_new"}
	manager, err := NewManager(ManagerConfig{
		RootDir: filepath.Join(t.TempDir(), "reports"),
		Key:     []byte("0123456789abcdef0123456789abcdef"),
		Clock:   func() time.Time { return time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC) },
		NewID: func() string {
			id := ids[0]
			ids = ids[1:]
			return id
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := manager.Start(testDestination())
	if err != nil {
		t.Fatal(err)
	}
	old, err = manager.Advance(old.InvocationID, PhaseInventory, Totals{}, []Source{{
		Alias: "canonical_vault_01", Classification: ClassificationCanonicalVault,
		ReasonCode: "signed_bound_destination", Counts: map[string]int64{"source_rows": 1},
	}}, nil, []string{"adapter_pulse_v1"}, "One source inspected.", "")
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := manager.Cancel(old.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if canceled.Phase != PhaseCanceled || canceled.Generation <= old.Generation {
		t.Fatalf("cancel did not commit generation: old=%#v canceled=%#v", old, canceled)
	}
	resumed, err := manager.Resume(old.InvocationID, testDestination())
	if err != nil {
		t.Fatal(err)
	}
	if resumed.InvocationID != "report_new" || resumed.Phase != PhasePlanned || resumed.Generation <= canceled.Generation {
		t.Fatalf("resume did not create replacement generation: %#v", resumed)
	}
	if len(resumed.Sources) != 1 || resumed.Sources[0].Alias != "canonical_vault_01" ||
		!containsCode(resumed.ReasonCodes, "resume_from_committed_generation") {
		t.Fatalf("resume did not carry the last committed content-free generation: %#v", resumed)
	}
	if _, err := manager.Cancel(old.InvocationID); err != ErrStaleInvocation {
		t.Fatalf("stale cancel err=%v", err)
	}

	reopened, err := NewManager(ManagerConfig{
		RootDir: manager.RootDir(),
		Key:     []byte("0123456789abcdef0123456789abcdef"),
		Clock:   func() time.Time { return time.Date(2026, 7, 21, 9, 1, 0, 0, time.UTC) },
		NewID:   func() string { return "unused" },
	})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := reopened.Latest(testDestination())
	if err != nil {
		t.Fatal(err)
	}
	if latest.InvocationID != resumed.InvocationID || latest.Generation != resumed.Generation {
		t.Fatalf("reopen ignored latest committed generation: %#v", latest)
	}
}

func TestManagerIdempotentResumeReplaysAfterRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "reports")
	key := []byte("0123456789abcdef0123456789abcdef")
	manager, err := NewManager(ManagerConfig{
		RootDir: root, Key: key, NewID: func() string { return "report_original" },
	})
	if err != nil {
		t.Fatal(err)
	}
	original, _, err := manager.Start(testDestination())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Cancel(original.InvocationID); err != nil {
		t.Fatal(err)
	}
	const idempotencyKey = "mcp_0123456789abcdef0123456789abcdef"
	first, err := manager.ResumeIdempotent(original.InvocationID, testDestination(), idempotencyKey)
	if err != nil {
		t.Fatal(err)
	}

	reopened, err := NewManager(ManagerConfig{RootDir: root, Key: key, NewID: func() string { return "unused" }})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := reopened.ResumeIdempotent(original.InvocationID, testDestination(), idempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.InvocationID != first.InvocationID || replayed.ReportDigest != first.ReportDigest {
		t.Fatalf("durable resume replay changed result: first=%#v replayed=%#v", first, replayed)
	}
	if _, err := reopened.Cancel(first.InvocationID); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.ResumeIdempotent(first.InvocationID, testDestination(), idempotencyKey); err != ErrIdempotencyConflict {
		t.Fatalf("divergent idempotency reuse err=%v", err)
	}
}

func TestManagerRejectsInvalidAuthorityAndTamperedCheckpoint(t *testing.T) {
	root := filepath.Join(t.TempDir(), "reports")
	manager, err := NewManager(ManagerConfig{
		RootDir: root,
		Key:     []byte("0123456789abcdef0123456789abcdef"),
		NewID:   func() string { return "report_01" },
	})
	if err != nil {
		t.Fatal(err)
	}
	bad := testDestination()
	bad.BindingDigest = "agent-selected"
	if _, _, err := manager.Start(bad); err != ErrInvalidAuthority {
		t.Fatalf("invalid authority err=%v", err)
	}
	if _, _, err := manager.Start(testDestination()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, entries[0].Name())
	if err := os.WriteFile(path, []byte(`{"report":{"schema":"forged"},"integrity":"forged"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(ManagerConfig{RootDir: root, Key: []byte("0123456789abcdef0123456789abcdef")}); err != ErrCheckpointIntegrity {
		t.Fatalf("tampered checkpoint err=%v", err)
	}
}
