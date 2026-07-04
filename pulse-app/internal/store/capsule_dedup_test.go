package store

import (
	"path/filepath"
	"testing"
)

// insertCapsuleRow inserts a memory_capsule with an explicit id + created_at so
// tests control the deterministic "earliest is kept" ordering. status defaults
// to 'active' via the migration.
func insertCapsuleRow(t *testing.T, s *Store, id, kind, summary, createdAt string) {
	t.Helper()
	_, err := s.DB().Exec(`
		INSERT INTO memory_capsules
		  (id, schema_version, source_host, conversation_scope, source_timestamp,
		   kind, redacted_summary, confidence, evidence_hint, privacy_tier,
		   retention, tags, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, MemoryCapsuleSchema, "claude-code", "current_turn", createdAt,
		kind, summary, 0.9, "current_turn", "normal", "project", "[]", createdAt)
	if err != nil {
		t.Fatalf("insert capsule %s: %v", id, err)
	}
}

func countCapsulesByStatus(t *testing.T, s *Store, status string) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM memory_capsules WHERE status=?`, status).Scan(&n); err != nil {
		t.Fatalf("count status %s: %v", status, err)
	}
	return n
}

// TestConsolidateCapsulesCollapsesNearDuplicates is the end-to-end proof: a
// re-emitted capsule (near-identical text, same kind) collapses to one active
// row, provenance is retained (invalidate-not-delete), distinct capsules are
// untouched, and a second pass is a no-op (idempotent).
func TestConsolidateCapsulesCollapsesNearDuplicates(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	// Cluster: the SAME preference capsule re-emitted across sessions, with
	// only punctuation/case/hyphen drift — the exact bloat the audit names.
	insertCapsuleRow(t, s, "cap:1", "preference", "Nik prefers plain-text chat replies with no markdown.", "2026-07-01T09:00:00Z")
	insertCapsuleRow(t, s, "cap:2", "preference", "Nik prefers plain-text chat replies, with no markdown", "2026-07-02T09:00:00Z")
	insertCapsuleRow(t, s, "cap:3", "preference", "nik prefers plain text chat replies with no markdown", "2026-07-03T09:00:00Z")
	// Genuinely distinct, same kind — must never merge.
	insertCapsuleRow(t, s, "cap:4", "preference", "Nik records weekly demo videos every Friday afternoon.", "2026-07-01T10:00:00Z")
	insertCapsuleRow(t, s, "cap:5", "preference", "Default retrieval mode should remain factual for queries.", "2026-07-01T11:00:00Z")

	const seeded = 5
	status, err := s.MemoryStatus()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.ItemCount != seeded {
		t.Fatalf("expected %d capsules before, got %d", seeded, status.ItemCount)
	}

	// Before: recall for the shared topic returns the redundant cluster (bloat).
	before, err := s.RecallMemory(RecallMemoryQuery{Query: "markdown", Limit: 10, PrivacyCeiling: "normal"})
	if err != nil {
		t.Fatalf("recall before: %v", err)
	}
	if len(before) != 3 {
		t.Fatalf("expected 3 redundant recall rows before consolidation, got %d", len(before))
	}

	// Dry run: reports the merges but writes nothing.
	dry, err := s.ConsolidateCapsules(ConsolidateOptions{DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dry.MergedCount != 2 {
		t.Fatalf("dry run: expected MergedCount 2, got %d", dry.MergedCount)
	}
	if countCapsulesByStatus(t, s, "merged") != 0 {
		t.Fatalf("dry run must not write: found merged rows")
	}
	if countCapsulesByStatus(t, s, "active") != seeded {
		t.Fatalf("dry run must not write: active count changed")
	}

	// Apply: the cluster collapses to the earliest (cap:1); cap:2/cap:3 merge.
	res, err := s.ConsolidateCapsules(ConsolidateOptions{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.MergedCount != 2 {
		t.Fatalf("apply: expected MergedCount 2, got %d", res.MergedCount)
	}
	if res.Threshold != defaultConsolidateThreshold {
		t.Fatalf("apply: expected default threshold, got %v", res.Threshold)
	}
	for _, pair := range res.MergedPairs {
		if pair.KeptID != "cap:1" {
			t.Fatalf("expected cap:1 kept (earliest), got kept=%q for merged=%q", pair.KeptID, pair.MergedID)
		}
		if pair.MergedID == "cap:1" {
			t.Fatalf("earliest capsule must not be merged")
		}
	}
	// merged_into is set on the merged rows, pointing at the kept id.
	for _, id := range []string{"cap:2", "cap:3"} {
		var st, into string
		if err := s.DB().QueryRow(`SELECT status, COALESCE(merged_into,'') FROM memory_capsules WHERE id=?`, id).Scan(&st, &into); err != nil {
			t.Fatalf("select %s: %v", id, err)
		}
		if st != "merged" || into != "cap:1" {
			t.Fatalf("%s: expected merged->cap:1, got status=%q merged_into=%q", id, st, into)
		}
	}
	// Distinct rows untouched.
	for _, id := range []string{"cap:1", "cap:4", "cap:5"} {
		if countActiveID(t, s, id) != 1 {
			t.Fatalf("%s should stay active", id)
		}
	}

	// Bloat gone: recall now returns exactly one active row for the topic.
	after, err := s.RecallMemory(RecallMemoryQuery{Query: "markdown", Limit: 10, PrivacyCeiling: "normal"})
	if err != nil {
		t.Fatalf("recall after: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("expected 1 recall row after consolidation, got %d", len(after))
	}
	if after[0].ID != "cap:1" {
		t.Fatalf("expected cap:1 to survive recall, got %q", after[0].ID)
	}

	// Invalidate-not-delete: merged rows still present, ItemCount unchanged.
	if countCapsulesByStatus(t, s, "merged") != 2 {
		t.Fatalf("expected 2 merged rows retained, got %d", countCapsulesByStatus(t, s, "merged"))
	}
	status, _ = s.MemoryStatus()
	if status.ItemCount != seeded {
		t.Fatalf("ItemCount must be unchanged (rows retained), got %d", status.ItemCount)
	}

	// Bloat measurement (logged for the ingest signal).
	activeAfter := countCapsulesByStatus(t, s, "active")
	t.Logf("bloat: active_before=%d active_after=%d reduction=%.2f",
		seeded, activeAfter, float64(res.MergedCount)/float64(seeded))

	// Idempotence: a second pass merges nothing new.
	again, err := s.ConsolidateCapsules(ConsolidateOptions{})
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if again.MergedCount != 0 {
		t.Fatalf("idempotence: expected 0 new merges, got %d", again.MergedCount)
	}
}

func countActiveID(t *testing.T, s *Store, id string) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM memory_capsules WHERE id=? AND status='active'`, id).Scan(&n); err != nil {
		t.Fatalf("count active %s: %v", id, err)
	}
	return n
}

// TestConsolidateCapsulesRespectsKindBucketing proves identical text in
// different kinds never merges — bucketing is by kind.
func TestConsolidateCapsulesRespectsKindBucketing(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	insertCapsuleRow(t, s, "k:1", "preference", "Nik prefers plain-text chat replies with no markdown.", "2026-07-01T09:00:00Z")
	insertCapsuleRow(t, s, "k:2", "fact", "Nik prefers plain-text chat replies with no markdown.", "2026-07-02T09:00:00Z")

	res, err := s.ConsolidateCapsules(ConsolidateOptions{})
	if err != nil {
		t.Fatalf("consolidate: %v", err)
	}
	if res.MergedCount != 0 {
		t.Fatalf("cross-kind identical text must not merge, got MergedCount %d", res.MergedCount)
	}
}

// TestConsolidateCapsulesThresholdGuards proves distinct capsules below the
// threshold never merge, and the threshold is hard-clamped to [0.80, 1.0].
func TestConsolidateCapsulesThresholdGuards(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	insertCapsuleRow(t, s, "t:1", "preference", "Nik prefers plain-text chat replies with no markdown.", "2026-07-01T09:00:00Z")
	insertCapsuleRow(t, s, "t:2", "preference", "Nik records weekly demo videos every Friday afternoon.", "2026-07-02T09:00:00Z")

	res, err := s.ConsolidateCapsules(ConsolidateOptions{DryRun: true})
	if err != nil {
		t.Fatalf("consolidate: %v", err)
	}
	if res.MergedCount != 0 {
		t.Fatalf("distinct capsules must not merge, got MergedCount %d", res.MergedCount)
	}

	// Clamp below range → 0.80.
	low, err := s.ConsolidateCapsules(ConsolidateOptions{DryRun: true, Threshold: 0.5})
	if err != nil {
		t.Fatalf("low threshold: %v", err)
	}
	if low.Threshold != minConsolidateThreshold {
		t.Fatalf("expected threshold clamped to %v, got %v", minConsolidateThreshold, low.Threshold)
	}
	// Clamp above range → 1.0.
	high, err := s.ConsolidateCapsules(ConsolidateOptions{DryRun: true, Threshold: 2.0})
	if err != nil {
		t.Fatalf("high threshold: %v", err)
	}
	if high.Threshold != maxConsolidateThreshold {
		t.Fatalf("expected threshold clamped to %v, got %v", maxConsolidateThreshold, high.Threshold)
	}
}

// TestConsolidateNeutralByDefault proves the recall status filter is a no-op
// until a pass runs: with no consolidation, every seeded row still recalls
// (mirrors shadow-boost gating collapsing to neutral).
func TestConsolidateNeutralByDefault(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	insertCapsuleRow(t, s, "n:1", "preference", "Nik prefers plain-text chat replies with no markdown.", "2026-07-01T09:00:00Z")
	insertCapsuleRow(t, s, "n:2", "preference", "Nik prefers plain-text chat replies, with no markdown", "2026-07-02T09:00:00Z")

	// No consolidation pass has run → both rows are active and both recall.
	got, err := s.RecallMemory(RecallMemoryQuery{Query: "markdown", Limit: 10, PrivacyCeiling: "normal"})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("neutral-by-default: expected both rows to recall, got %d", len(got))
	}
}

// TestConsolidateExportImportRoundTrip proves merged provenance survives an
// export → wipe → import cycle.
func TestConsolidateExportImportRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	insertCapsuleRow(t, s, "e:1", "preference", "Nik prefers plain-text chat replies with no markdown.", "2026-07-01T09:00:00Z")
	insertCapsuleRow(t, s, "e:2", "preference", "Nik prefers plain-text chat replies, with no markdown", "2026-07-02T09:00:00Z")
	if _, err := s.ConsolidateCapsules(ConsolidateOptions{}); err != nil {
		t.Fatalf("consolidate: %v", err)
	}

	exported, err := s.ExportMemory()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var sawMerged bool
	for _, item := range exported.Items {
		if item.ID == "e:2" {
			if item.Status != "merged" || item.MergedInto != "e:1" {
				t.Fatalf("export lost provenance: %+v", item)
			}
			sawMerged = true
		}
	}
	if !sawMerged {
		t.Fatalf("merged row missing from export")
	}

	if err := s.WipeMemory(); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	if _, err := s.ImportMemory(exported); err != nil {
		t.Fatalf("import: %v", err)
	}
	var st, into string
	if err := s.DB().QueryRow(`SELECT status, COALESCE(merged_into,'') FROM memory_capsules WHERE id='e:2'`).Scan(&st, &into); err != nil {
		t.Fatalf("select after import: %v", err)
	}
	if st != "merged" || into != "e:1" {
		t.Fatalf("import lost provenance: status=%q merged_into=%q", st, into)
	}
}
