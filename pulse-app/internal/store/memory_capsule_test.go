package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRememberCapsuleStoresStrictItemsAndRecallFindsThem(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	capsule := MemoryCapsule{
		Schema: "pulse.memory_capsule.v1",
		Source: CapsuleSource{
			Host:              "claude-code",
			ConversationScope: "current_turn",
			Timestamp:         "2026-06-02T09:00:00Z",
		},
		Items: []MemoryCapsuleItem{{
			Kind:            "decision",
			RedactedSummary: "We chose Pulse MCP distribution with Claude Code as the first install target.",
			Confidence:      0.92,
			EvidenceHint:    "current_turn",
			PrivacyTier:     "normal",
			Retention:       "project",
			Tags:            []string{"distribution", "claude-code"},
		}},
		RawInputIncluded: false,
	}

	ids, err := s.RememberCapsule(capsule)
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	if len(ids) != 1 || ids[0] == "" {
		t.Fatalf("ids: %#v", ids)
	}

	items, err := s.RecallMemory(RecallMemoryQuery{
		Query:          "what did we choose for distribution",
		Scope:          "project",
		Limit:          5,
		PrivacyCeiling: "normal",
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 recall item, got %d", len(items))
	}
	if items[0].ID != ids[0] {
		t.Fatalf("expected id %q, got %q", ids[0], items[0].ID)
	}
	if !strings.Contains(items[0].Summary, "Claude Code") {
		t.Fatalf("summary was not returned: %#v", items[0])
	}
	if items[0].Source != "pulse" {
		t.Fatalf("expected source pulse, got %q", items[0].Source)
	}
}

// A fresh capsule matching a single weak term must not outrank an older
// capsule matching every query term. Recall ranks by term coverage, with
// recency only as a tiebreak — not the other way around.
func TestRecallRanksByTermCoverageNotRecency(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	insert := func(id, summary, createdAt string) {
		t.Helper()
		if _, err := s.db.Exec(`
			INSERT INTO memory_capsules
			  (id, schema_version, source_host, conversation_scope, source_timestamp,
			   kind, redacted_summary, confidence, evidence_hint, privacy_tier,
			   retention, tags, created_at)
			VALUES (?, 'pulse.memory_capsule.v1', 'claude-code', 'current_turn', ?,
			        'note', ?, 0.9, 'current_turn', 'normal', 'project', '[]', ?)`,
			id, createdAt, summary, createdAt); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	// Older, matches all three query terms.
	insert("cap-relevant", "The kubernetes migration rollback plan was approved by the team.", "2026-06-01T09:00:00Z")
	// Newer, matches only "kubernetes" — old recency-only ranking floated this to the top.
	insert("cap-noise", "Weekly kubernetes cluster cost review for the finance team.", "2026-06-30T09:00:00Z")

	// Terms are scattered so the exact-phrase primary path misses and the
	// term-coverage fallback path runs.
	items, err := s.RecallMemory(RecallMemoryQuery{
		Query:          "rollback migration kubernetes",
		Scope:          "project",
		Limit:          5,
		PrivacyCeiling: "normal",
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(items) < 2 {
		t.Fatalf("expected both capsules recalled, got %d: %#v", len(items), items)
	}
	if items[0].ID != "cap-relevant" {
		t.Fatalf("term-coverage ranking broken: expected cap-relevant first, got %q (recency drowned relevance)", items[0].ID)
	}
}

func TestRememberCapsuleRejectsRawOrTranscriptLikePayloads(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	valid := MemoryCapsule{
		Schema: "pulse.memory_capsule.v1",
		Source: CapsuleSource{
			Host:              "claude-code",
			ConversationScope: "current_turn",
			Timestamp:         time.Now().UTC().Format(time.RFC3339),
		},
		Items: []MemoryCapsuleItem{{
			Kind:            "fact",
			RedactedSummary: "Pulse stores only minimal structured capsules.",
			Confidence:      0.8,
			EvidenceHint:    "current_turn",
			PrivacyTier:     "normal",
			Retention:       "project",
		}},
	}

	raw := valid
	raw.RawInputIncluded = true
	if _, err := s.RememberCapsule(raw); err == nil {
		t.Fatal("expected raw_input_included=true to be rejected")
	}

	missingSummary := valid
	missingSummary.Items[0].RedactedSummary = ""
	if _, err := s.RememberCapsule(missingSummary); err == nil {
		t.Fatal("expected missing redacted_summary to be rejected")
	}

	transcript := valid
	transcript.Items[0].RedactedSummary = strings.Repeat("User: hello\nAssistant: hi\n", 80)
	if _, err := s.RememberCapsule(transcript); err == nil {
		t.Fatal("expected transcript-like payload to be rejected")
	}

	badTimestamp := valid
	badTimestamp.Source.Timestamp = "yesterday"
	if _, err := s.RememberCapsule(badTimestamp); err == nil {
		t.Fatal("expected non-RFC3339 timestamp to be rejected")
	}
}

func TestRememberCapsuleRejectsSecretPathAndUnsafeTags(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	valid := MemoryCapsule{
		Schema: "pulse.memory_capsule.v1",
		Source: CapsuleSource{
			Host:              "claude-code",
			ConversationScope: "current_turn",
			Timestamp:         "2026-06-02T09:00:00Z",
		},
		Items: []MemoryCapsuleItem{{
			Kind:            "fact",
			RedactedSummary: "Pulse stores minimal structured capsules.",
			Confidence:      0.8,
			EvidenceHint:    "current_turn",
			PrivacyTier:     "normal",
			Retention:       "project",
			Tags:            []string{"safe-tag"},
		}},
	}

	for _, summary := range []string{
		"User OpenAI key is sk-test",
		"token=abc123",
		"password is hunter2",
		"/Users/example/private/file.txt",
		"file:///Users/example/private/file.txt",
		"-----BEGIN PRIVATE KEY-----",
		"GitHub token ghp_abcdef",
	} {
		capsule := valid
		capsule.Items[0].RedactedSummary = summary
		if _, err := s.RememberCapsule(capsule); err == nil {
			t.Fatalf("expected secret/path-like summary to be rejected: %q", summary)
		}
	}

	for _, tag := range []string{
		"/Users/example",
		"token=abc",
		strings.Repeat("a", 65),
		"unsafe tag with spaces",
	} {
		capsule := valid
		capsule.Items[0].Tags = []string{tag}
		if _, err := s.RememberCapsule(capsule); err == nil {
			t.Fatalf("expected unsafe tag to be rejected: %q", tag)
		}
	}
}

func TestMemoryExportDeleteAndWipe(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	ids, err := s.RememberCapsule(MemoryCapsule{
		Schema: "pulse.memory_capsule.v1",
		Source: CapsuleSource{
			Host:              "claude-code",
			ConversationScope: "current_turn",
			Timestamp:         "2026-06-02T09:00:00Z",
		},
		Items: []MemoryCapsuleItem{{
			Kind:            "preference",
			RedactedSummary: "Narrow v1 to Claude Code before other ecosystems.",
			Confidence:      0.9,
			EvidenceHint:    "current_turn",
			PrivacyTier:     "normal",
			Retention:       "project",
		}},
	})
	if err != nil {
		t.Fatalf("remember: %v", err)
	}

	exported, err := s.ExportMemory()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(exported.Items) != 1 || exported.Items[0].ID != ids[0] {
		t.Fatalf("bad export: %#v", exported)
	}

	if err := s.DeleteMemory(ids[0]); err != nil {
		t.Fatalf("delete: %v", err)
	}
	afterDelete, err := s.ExportMemory()
	if err != nil {
		t.Fatalf("export after delete: %v", err)
	}
	if len(afterDelete.Items) != 0 {
		t.Fatalf("expected delete to remove item, got %d", len(afterDelete.Items))
	}

	if _, err := s.ImportMemory(exported); err != nil {
		t.Fatalf("import: %v", err)
	}
	if err := s.WipeMemory(); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	afterWipe, err := s.ExportMemory()
	if err != nil {
		t.Fatalf("export after wipe: %v", err)
	}
	if len(afterWipe.Items) != 0 {
		t.Fatalf("expected wipe to remove all items, got %d", len(afterWipe.Items))
	}
}
