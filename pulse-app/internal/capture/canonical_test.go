package capture

import (
	"strings"
	"testing"
)

func TestCanonicalEnvelopeGolden(t *testing.T) {
	raw := []byte(`{"schema":"pulse.airlock_envelope.v1","metadata":{"z":2,"a":1},"content":"caf\u00e9"}`)
	got, digest, err := CanonicalizeEnvelopeJSON(raw, []string{"schema", "content", "metadata"})
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	want := `{"content":"café","metadata":{"a":1,"z":2},"schema":"pulse.airlock_envelope.v1"}`
	if string(got) != want {
		t.Fatalf("canonical bytes:\n got %s\nwant %s", got, want)
	}
	if digest != "sha256:56163ff416d5a5c84b2ecbe34b814f635a5973af615dd2051a2d01cf8eafdcbf" {
		t.Fatalf("unexpected digest: %s", digest)
	}
}

func TestCanonicalEnvelopeRejectsAmbiguity(t *testing.T) {
	for name, raw := range map[string]string{
		"duplicate":   `{"schema":"x","schema":"y"}`,
		"unknown":     `{"schema":"x","team_id":"team-attacker"}`,
		"control":     "{\"schema\":\"x\",\"content\":\"line\\nfeed\"}",
		"fraction":    `{"schema":"x","count":1.25}`,
		"unicode-key": `{"schema":"x","metadata":{"скрыто":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := CanonicalizeEnvelopeJSON([]byte(raw), []string{"schema", "content", "count", "metadata"})
			if err == nil || !strings.HasPrefix(err.Error(), "canonical_") {
				t.Fatalf("expected stable canonical error, got %v", err)
			}
		})
	}
}
