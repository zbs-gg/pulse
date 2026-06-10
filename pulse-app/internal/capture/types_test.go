package capture

import (
	"encoding/json"
	"testing"
	"time"
)

func TestObservationJSONRoundtrip(t *testing.T) {
	obs := Observation{
		SourceKind:  "claude_jsonl",
		SourceID:    "sess-abc:42",
		ContentHash: "sha256-fake",
		Version:     1,
		Scope:       "shared",
		CapturedAt:  time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
		ObservedAt:  time.Date(2026, 4, 15, 12, 0, 1, 0, time.UTC),
		Actors: []ActorRef{
			{Kind: "user", ID: "alice", Display: "Alice"},
			{Kind: "assistant", ID: "assistant", Display: "Assistant"},
		},
		ContentText: "hello",
		Metadata:    map[string]any{"model": "opus-4.6"},
	}

	b, err := json.Marshal(obs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed Observation
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if parsed.SourceKind != "claude_jsonl" {
		t.Errorf("SourceKind: got %s", parsed.SourceKind)
	}
	if len(parsed.Actors) != 2 {
		t.Errorf("Actors len: got %d", len(parsed.Actors))
	}
	if parsed.Actors[0].Kind != "user" {
		t.Errorf("Actor[0].Kind: got %s", parsed.Actors[0].Kind)
	}
}
