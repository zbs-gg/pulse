package retrieve

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

// seedAccessEvent writes one retrievable event and returns its id.
func seedAccessEvent(t *testing.T, s *store.Store, eng *Engine, clientID, title string) int64 {
	t.Helper()
	now := time.Now()
	res, err := s.SaveSemanticDelta(store.SemanticDelta{
		Schema: store.SemanticDeltaSchema,
		Source: store.SemanticDeltaSource{
			Host:              "claude-code",
			ConversationScope: "current_turn",
			Timestamp:         now.UTC().Format(time.RFC3339),
			ThreadID:          "access-freq-test",
		},
		Events: []store.SemanticEvent{{
			ClientID:        clientID,
			Title:           title,
			Summary:         title,
			Sentiment:       "neutral",
			EmotionalWeight: 0.5,
			Confidence:      0.9,
			PrivacyTier:     "normal",
			OccurredAt:      now.AddDate(0, -1, 0).UTC().Format(time.RFC3339),
		}},
		RawInputIncluded: false,
	})
	if err != nil {
		t.Fatalf("SaveSemanticDelta: %v", err)
	}
	if len(res.EventIDs) != 1 {
		t.Fatalf("expected 1 event id, got %#v", res)
	}
	id := res.EventIDs[0]
	if err := eng.EmbedAndIndexEvents(context.Background(), []IndexEventDoc{{EventID: id, Text: title}}); err != nil {
		t.Fatalf("EmbedAndIndexEvents: %v", err)
	}
	return id
}

func accessCount(t *testing.T, s *store.Store, id int64) int64 {
	t.Helper()
	var n int64
	if err := s.DB().QueryRow("SELECT access_count FROM events WHERE id = ?", id).Scan(&n); err != nil {
		t.Fatalf("read access_count: %v", err)
	}
	return n
}

// With PULSE_ACCESS_FREQ enabled, a Retrieve bumps the returned events'
// access_count by exactly 1, and the incremented value reaches the in-memory
// eventAccess slice after a Reload. The scorer never reads the slice, so this
// only proves data availability + instrumentation.
func TestRetrieve_AccessFreqEnabled_IncrementsCount(t *testing.T) {
	t.Setenv("PULSE_ACCESS_FREQ", "1")

	s, err := store.Open(filepath.Join(t.TempDir(), "freq_on.db"))
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
	if !eng.accessFreqEnabled {
		t.Fatalf("expected accessFreqEnabled=true when PULSE_ACCESS_FREQ=1")
	}

	id := seedAccessEvent(t, s, eng, "demo:one", "morning standup notes")

	// Freshly indexed event starts at 0 both in DB and in the loaded slice.
	if got := accessCount(t, s, id); got != 0 {
		t.Fatalf("expected initial access_count 0, got %d", got)
	}
	if len(eng.eventAccess) != 1 || eng.eventAccess[0] != 0 {
		t.Fatalf("expected eventAccess [0], got %v", eng.eventAccess)
	}

	resp, err := eng.Retrieve(context.Background(), RetrieveRequest{
		Query: "what were the standup notes",
		Mode:  ModeEmpathic,
		TopK:  3,
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(resp.EventIDs) != 1 || resp.EventIDs[0] != id {
		t.Fatalf("expected the seeded event to be retrieved, got %v", resp.EventIDs)
	}

	// Counter bumped in the DB for the returned id.
	if got := accessCount(t, s, id); got != 1 {
		t.Fatalf("expected access_count 1 after one Retrieve, got %d", got)
	}
	// A second Retrieve bumps it again — counts accumulate.
	if _, err := eng.Retrieve(context.Background(), RetrieveRequest{Query: "standup notes again", Mode: ModeEmpathic, TopK: 3}); err != nil {
		t.Fatalf("Retrieve #2: %v", err)
	}
	if got := accessCount(t, s, id); got != 2 {
		t.Fatalf("expected access_count 2 after two Retrieves, got %d", got)
	}

	// The accrued count reaches the in-memory slice only after a Reload
	// (same lag model as every other loaded field).
	if err := eng.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(eng.eventAccess) != 1 || eng.eventAccess[0] != 2 {
		t.Fatalf("expected eventAccess [2] after reload, got %v", eng.eventAccess)
	}
}

// With the flag OFF (default), Retrieve performs NO counter writes: access_count
// stays 0. This proves the feature is off-by-default and behavior-neutral.
func TestRetrieve_AccessFreqDisabled_NoWrites(t *testing.T) {
	t.Setenv("PULSE_ACCESS_FREQ", "") // force OFF regardless of ambient env

	s, err := store.Open(filepath.Join(t.TempDir(), "freq_off.db"))
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
	if eng.accessFreqEnabled {
		t.Fatalf("expected accessFreqEnabled=false when PULSE_ACCESS_FREQ is unset")
	}

	id := seedAccessEvent(t, s, eng, "demo:two", "evening retro notes")

	if _, err := eng.Retrieve(context.Background(), RetrieveRequest{
		Query: "retro notes",
		Mode:  ModeEmpathic,
		TopK:  3,
	}); err != nil {
		t.Fatalf("Retrieve: %v", err)
	}

	if got := accessCount(t, s, id); got != 0 {
		t.Fatalf("expected access_count to stay 0 with flag OFF, got %d", got)
	}
}

// IncrementAccessCounts is a no-op on an empty id slice and bumps exactly the
// named rows otherwise (unit test of the store helper, independent of the flag).
func TestIncrementAccessCounts(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "incr.db"))
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
	a := seedAccessEvent(t, s, eng, "demo:a", "alpha")
	b := seedAccessEvent(t, s, eng, "demo:b", "beta")

	// Empty slice: no error, no change.
	if err := s.IncrementAccessCounts(nil, now); err != nil {
		t.Fatalf("empty increment: %v", err)
	}
	if accessCount(t, s, a) != 0 || accessCount(t, s, b) != 0 {
		t.Fatalf("empty increment should not change any row")
	}

	// Only 'a' is bumped.
	if err := s.IncrementAccessCounts([]int64{a}, now); err != nil {
		t.Fatalf("increment a: %v", err)
	}
	if got := accessCount(t, s, a); got != 1 {
		t.Fatalf("expected a access_count 1, got %d", got)
	}
	if got := accessCount(t, s, b); got != 0 {
		t.Fatalf("expected b access_count 0 (untouched), got %d", got)
	}
}
