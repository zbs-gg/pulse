package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The new-empty-chat resume should carry a cross-harness digest section listing
// per-harness recent activity (honest, from continuity_checkpoints.host).
func TestBuildResume_HarnessDigest(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "digest.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	now := time.Now().UTC()
	for _, h := range []struct {
		host, summary, ts string
	}{
		{"claude-code", "wiring graph retrieval into the live path", now.Add(-2 * time.Hour).Format(time.RFC3339)},
		{"codex", "drafting the harness digest spec", now.Add(-30 * time.Hour).Format(time.RFC3339)},
	} {
		if err := s.SaveCheckpoint(ContinuityCheckpoint{
			ThreadID: "t-" + h.host, SessionID: "sess-" + h.host, Host: h.host,
			Summary: h.summary, Confidence: 0.8, CreatedAt: h.ts,
		}); err != nil {
			t.Fatalf("SaveCheckpoint(%s): %v", h.host, err)
		}
	}

	rb, err := s.BuildResume(ResumeQuery{ThreadID: "t-claude-code"})
	if err != nil {
		t.Fatalf("BuildResume: %v", err)
	}
	md := rb.ResumeMarkdown
	for _, want := range []string{"## Across your harnesses", "claude-code", "codex", "ago"} {
		if !strings.Contains(md, want) {
			t.Fatalf("resume markdown missing %q:\n%s", want, md)
		}
	}
	if len(rb.Sections.HarnessActivity) < 2 {
		t.Fatalf("expected >=2 harness activity lines, got %v", rb.Sections.HarnessActivity)
	}
}
