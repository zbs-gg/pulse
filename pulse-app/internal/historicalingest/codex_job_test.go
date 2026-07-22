package historicalingest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexSourceStoreFreezesExactJobAndRehydratesPathFreeEvidence(t *testing.T) {
	sourceRoot := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(sourceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCodexSession(t, sourceRoot, "root-a", "", "2026-07-20T01:00:00Z", messageRecord("one", "2026-07-20T01:01:00Z", "Remember the Pulse decision."))
	store, err := NewCodexSourceStore(CodexSourceStoreConfig{
		RootDir: filepath.Join(t.TempDir(), "private-index"), Key: []byte(strings.Repeat("s", 32)), SourceRoots: []string{sourceRoot},
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.Prepare("job_0123456789abcdef", CodexPrepareOptions{RootLimit: 1, Cutoff: time.Date(2026, 7, 22, 1, 0, 0, 0, time.UTC), MaxChunkBytes: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Snapshot.RootCount != 1 || len(prepared.Units) != 1 || prepared.Snapshot.Digest == "" {
		t.Fatalf("unexpected prepared job: %+v", prepared)
	}
	prompt, evidence, err := store.Load(prepared.Units[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt+evidence, sourceRoot) || strings.Contains(prompt+evidence, "/Users/") {
		t.Fatal("model payload leaked a local source path")
	}
	var payload map[string]any
	if json.Unmarshal([]byte(evidence), &payload) != nil || payload["root_id"] != "root-a" {
		t.Fatalf("unexpected evidence: %s", evidence)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "append-marker"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewCodexSourceStore(CodexSourceStoreConfig{
		RootDir: store.rootDir, Key: []byte(strings.Repeat("s", 32)), SourceRoots: []string{sourceRoot},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := reopened.Load(prepared.Units[0]); err != nil {
		t.Fatalf("restart rehydrate failed: %v", err)
	}
}

func TestCurrentMacCodexHistoryExact50ContentFreeGate(t *testing.T) {
	if os.Getenv("PULSE_REAL_CODEX_HISTORY") != "1" {
		t.Skip("set PULSE_REAL_CODEX_HISTORY=1 for the owner-only current-Mac gate")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	excluded := map[string]struct{}{}
	if sessionID := os.Getenv("CODEX_THREAD_ID"); sessionID != "" {
		excluded[sessionID] = struct{}{}
	}
	store, err := NewCodexSourceStore(CodexSourceStoreConfig{
		RootDir: filepath.Join(t.TempDir(), "private-index"), Key: []byte(strings.Repeat("r", 32)),
		SourceRoots: []string{filepath.Join(home, ".codex", "sessions")},
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.Prepare("job_0123456789abcdef", CodexPrepareOptions{RootLimit: 50, ExcludedSessionIDs: excluded})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Snapshot.RootCount != 50 || len(prepared.Units) < 50 {
		t.Fatalf("coverage roots=%d units=%d", prepared.Snapshot.RootCount, len(prepared.Units))
	}
	encoded, err := json.Marshal(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), home) || strings.Contains(string(encoded), "/Users/") {
		t.Fatal("content-free prepared receipt leaked a local path")
	}
	if err := store.Verify(prepared.Snapshot); err != nil {
		t.Fatal(err)
	}
	var sourceBytes, evidenceBytes int64
	for _, source := range prepared.Snapshot.Files {
		sourceBytes += source.CapturedBytes
	}
	for _, unit := range prepared.Units {
		evidenceBytes += unit.EvidenceBytes
	}
	t.Logf("exact-50 snapshot=%s files=%d units=%d source_bytes=%d evidence_bytes=%d", prepared.Snapshot.Digest, len(prepared.Snapshot.Files), len(prepared.Units), sourceBytes, evidenceBytes)
}
