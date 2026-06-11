package retrieve

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

// Live ingest invariant: an event written via SaveSemanticDelta (with the new
// occurred_at / anchor / biometrics / emotions fields) becomes retrievable
// after EmbedAndIndexEvents with no daemon restart, and its anchor flag,
// sentiment label, and biometrics reach the in-memory index.
func TestEmbedAndIndexEvents_MakesDeltaEventsRetrievableLive(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	emb := &fakeEmbedder{dim: 5}
	now := time.Now()
	eng := New(Config{Store: s, Embedder: emb, ReferenceTime: &now})
	if err := eng.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if len(eng.eventIDs) != 0 {
		t.Fatalf("expected empty index, got %d events", len(eng.eventIDs))
	}

	hrv := 55.0
	sleep := 0.3
	occurred := now.AddDate(0, -3, 0).UTC().Format(time.RFC3339)
	res, err := s.SaveSemanticDelta(store.SemanticDelta{
		Schema: store.SemanticDeltaSchema,
		Source: store.SemanticDeltaSource{
			Host:              "claude-code",
			ConversationScope: "current_turn",
			Timestamp:         now.UTC().Format(time.RFC3339),
			ThreadID:          "live-ingest-test",
		},
		Events: []store.SemanticEvent{{
			ClientID:        "demo:crunch",
			Title:           "Deadline crunch broke the week",
			Summary:         "Shipped the preview while exhausted; sleep debt piled up.",
			Sentiment:       "burden",
			EmotionalWeight: 0.8,
			Confidence:      0.9,
			PrivacyTier:     "normal",
			OccurredAt:      occurred,
			Anchor:          true,
			Biometrics:      &store.SemanticBiometrics{HRV: &hrv, SleepQuality: &sleep},
			Emotions:        map[string]float64{"sadness": 0.7, "fear": 0.4},
		}},
		RawInputIncluded: false,
	})
	if err != nil {
		t.Fatalf("SaveSemanticDelta: %v", err)
	}
	if len(res.EventIDs) != 1 {
		t.Fatalf("expected 1 event id, got %#v", res)
	}

	docs := []IndexEventDoc{{EventID: res.EventIDs[0], Text: "Deadline crunch broke the week\nShipped the preview while exhausted."}}
	if err := eng.EmbedAndIndexEvents(context.Background(), docs); err != nil {
		t.Fatalf("EmbedAndIndexEvents: %v", err)
	}

	if len(eng.eventIDs) != 1 {
		t.Fatalf("expected 1 event in live index, got %d", len(eng.eventIDs))
	}
	if !eng.eventAnchor[0] {
		t.Fatalf("expected anchor flag to reach the index")
	}
	if eng.eventSentLabel[0] != "burden" {
		t.Fatalf("expected sentiment label 'burden', got %q", eng.eventSentLabel[0])
	}
	if eng.eventBio[0] == nil || eng.eventBio[0].HRV == nil || *eng.eventBio[0].HRV != 55 {
		t.Fatalf("expected biometrics to reach the index, got %#v", eng.eventBio[0])
	}
	// Backdated occurred_at must produce real day distance for decay.
	if eng.eventDays[0] < 80 {
		t.Fatalf("expected backdated event ~90 days old, got %.1f days", eng.eventDays[0])
	}

	resp, err := eng.Retrieve(context.Background(), RetrieveRequest{
		Query: "how did the deadline week go",
		Mode:  ModeEmpathic,
		TopK:  3,
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(resp.EventIDs) != 1 || resp.EventIDs[0] != res.EventIDs[0] {
		t.Fatalf("expected the live-ingested event to be retrievable, got %v", resp.EventIDs)
	}
	if resp.ScoreBreakdowns == nil {
		t.Fatalf("expected score breakdowns for empathic mode")
	}
}
