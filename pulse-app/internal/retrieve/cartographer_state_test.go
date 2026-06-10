package retrieve

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/embed"
	"github.com/nkkmnk/pulse/internal/store"
)

func TestUserStateDecodesOldFormatWithoutCartographerFields(t *testing.T) {
	var state UserState
	body := []byte(`{
		"mood_vector": {"shame": 0.7},
		"sleep_quality": 0.8,
		"recent_life_events_7d": ["demo"]
	}`)

	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("unmarshal old user_state: %v", err)
	}

	if state.ShadowThemes != nil {
		t.Fatalf("expected nil shadow themes for old-format state, got %#v", state.ShadowThemes)
	}
	if ok, _, key := state.HasDominantEmotion(0.5); !ok || key != "shame" {
		t.Fatalf("old mood vector behavior changed: ok=%v key=%q", ok, key)
	}
}

func TestCartographerBoostsAreIdentityWhenSignalsAbsent(t *testing.T) {
	s := setupCartographerBoostStore(t)
	emb := &constantEmbedder{vec: []float32{1, 0, 0}}
	ref := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	eng := New(Config{Store: s, Embedder: emb, ReferenceTime: &ref})
	if err := eng.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	resp, err := eng.Retrieve(context.Background(), RetrieveRequest{
		Query: "same query",
		Mode:  ModeEmpathic,
		TopK:  2,
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}

	for _, id := range resp.EventIDs {
		got := resp.ScoreBreakdowns[id]
		if got.ShadowThemeBoost != nil || got.CuriositySignalBoost != nil || got.ActiveTriggerBoost != nil {
			t.Fatalf("expected identity cartographer boosts to be omitted, got %+v", got)
		}
	}
}

func TestCartographerSignalsChangeRankingAndScoreBreakdown(t *testing.T) {
	s := setupCartographerBoostStore(t)
	emb := &constantEmbedder{vec: []float32{1, 0, 0}}
	ref := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	eng := New(Config{Store: s, Embedder: emb, ReferenceTime: &ref})
	if err := eng.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	stateA := &UserState{ShadowThemes: []string{"invisibility"}}
	respA, err := eng.Retrieve(context.Background(), RetrieveRequest{
		Query:     "same query",
		Mode:      ModeEmpathic,
		TopK:      2,
		UserState: stateA,
	})
	if err != nil {
		t.Fatalf("Retrieve stateA: %v", err)
	}

	stateB := &UserState{ShadowThemes: []string{"betrayal"}}
	respB, err := eng.Retrieve(context.Background(), RetrieveRequest{
		Query:     "same query",
		Mode:      ModeEmpathic,
		TopK:      2,
		UserState: stateB,
	})
	if err != nil {
		t.Fatalf("Retrieve stateB: %v", err)
	}

	if respA.EventIDs[0] == respB.EventIDs[0] {
		t.Fatalf("expected top-1 to differ, got A=%v B=%v", respA.EventIDs, respB.EventIDs)
	}
	if respA.EventIDs[0] != 101 {
		t.Fatalf("stateA should prefer invisibility event 101, got %v", respA.EventIDs)
	}
	if respB.EventIDs[0] != 102 {
		t.Fatalf("stateB should prefer betrayal event 102, got %v", respB.EventIDs)
	}
	if respA.ScoreBreakdowns[101].ShadowThemeBoost == nil || *respA.ScoreBreakdowns[101].ShadowThemeBoost <= 1.0 {
		t.Fatalf("expected visible shadow boost for event 101, got %+v", respA.ScoreBreakdowns[101])
	}
}

type constantEmbedder struct {
	vec []float32
}

func (c *constantEmbedder) Model() string { return "cartographer-test-embed" }

func (c *constantEmbedder) Embed(_ context.Context, texts []string, _ embed.InputType) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = append([]float32(nil), c.vec...)
	}
	return out, nil
}

func setupCartographerBoostStore(t *testing.T) *store.Store {
	t.Helper()
	tmpFile := t.TempDir() + "/cartographer-boost.db"
	s, err := store.Open(tmpFile)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	db := s.DB()
	ts := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	events := []struct {
		id          int64
		title       string
		description string
	}{
		{101, "invisibility anchor", "A memory about being ignored in public."},
		{102, "betrayal anchor", "A memory about trust breaking and betrayal."},
	}
	for _, event := range events {
		if _, err := db.Exec(
			`INSERT INTO events(id, title, description, ts) VALUES (?, ?, ?, ?)`,
			event.id, event.title, event.description, ts,
		); err != nil {
			t.Fatalf("insert event %d: %v", event.id, err)
		}
		vecJSON, _ := json.Marshal([]float32{1, 0, 0})
		if _, err := db.Exec(
			`INSERT INTO event_embeddings(event_id, model, dim, vector_json, text_source, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			event.id, "cartographer-test-embed", 3, string(vecJSON), event.title, ts,
		); err != nil {
			t.Fatalf("insert embedding %d: %v", event.id, err)
		}
	}
	return s
}
