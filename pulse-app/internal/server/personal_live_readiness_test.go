package server

import (
	"testing"

	"github.com/nkkmnk/pulse/internal/store"
)

func TestSupportedHostLiveReadinessValidatesAndBindsTargetHost(t *testing.T) {
	base := personalLiveReadinessSnapshot{
		Schema: supportedHostLiveReadinessSchema, TargetHost: "claude-code",
		Outcome: store.MemoryHomeReadinessActionRequired, ReasonCode: "host_lifecycle_required",
		NextAction: store.MemoryHomeNextAction{
			Code: "complete_host_lifecycle", Label: "Complete one normal agent turn",
		},
		CheckedAt: "2026-07-17T12:00:00Z",
	}
	if err := validatePersonalLiveReadiness(base); err != nil {
		t.Fatal(err)
	}
	claudeDigest, err := personalLiveReadinessDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	cursor := base
	cursor.TargetHost = "cursor"
	cursorDigest, err := personalLiveReadinessDigest(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if claudeDigest == cursorDigest {
		t.Fatal("supported-host readiness digest did not bind target_host")
	}
	invalid := base
	invalid.TargetHost = "gemini"
	if err := validatePersonalLiveReadiness(invalid); err == nil {
		t.Fatal("unsupported Home target accepted")
	}
	legacy := personalLiveReadinessForReason("personal_live_ready", base.CheckedAt)
	legacy.TargetHost = "codex"
	if err := validatePersonalLiveReadiness(legacy); err == nil {
		t.Fatal("v1 readiness silently widened with target_host")
	}
}
