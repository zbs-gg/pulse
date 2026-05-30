package contextquery

import (
	"context"
	"database/sql"
	"testing"

	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
)

type fakeRetrieval struct {
	ids         []int64
	mode        retrieve.QueryMode
	emotionRole retrieve.EmotionRole
}

func (f fakeRetrieval) Retrieve(_ context.Context, _ retrieve.RetrieveRequest) (*retrieve.RetrieveResponse, error) {
	mode := f.mode
	if mode == "" {
		mode = retrieve.ModeEmpathic
	}
	return &retrieve.RetrieveResponse{
		EventIDs:       f.ids,
		ModeUsed:       mode,
		RouterDecision: retrieve.RouteDecision{Mode: mode, Confidence: 0.8, Classifier: "test", Reasoning: "fixture"},
		EmotionRole:    retrieve.EmotionRoleDecision{Role: f.emotionRole, Classifier: "test", Reasoning: "fixture"},
	}, nil
}

func openContextTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir() + "/context.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return s
}

func seedContextGraph(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
INSERT INTO observations (id, source_kind, source_id, content_hash, version, scope, captured_at, observed_at, actors, content_text)
VALUES (1, 'test', 'obs1', 'hash1', 1, 'user', '2026-05-03T00:00:00Z', '2026-05-03T00:00:00Z', '[]', 'Alice talked about recognition at work');
INSERT INTO entities (id, canonical_name, kind, aliases, first_seen, last_seen, salience_score, emotional_weight, description_md)
VALUES (11, 'Alice', 'person', '["Alicia"]', '2026-05-03T00:00:00Z', '2026-05-03T00:00:00Z', 0.8, 0.9, 'User self entity');
INSERT INTO events (id, title, description, sentiment, emotional_weight, ts, belief_class, provenance, confidence_floor)
VALUES (101, 'Recognition theme', 'Alice names a need to feel valued without overproving', -0.4, 0.95, '2026-05-03T00:00:00Z', 'user_model', 'manual', 0.7);
INSERT INTO event_entities (event_id, entity_id) VALUES (101, 11);
INSERT INTO evidence (id, subject_kind, subject_id, observation_id, weight, created_at)
VALUES (201, 'event', 101, 1, 1.0, '2026-05-03T00:00:00Z');
INSERT INTO facts (id, entity_id, text, confidence, created_at, source_obs_id, belief_class, provenance, confidence_floor)
VALUES (301, 11, 'Alice wants to feel valued without overproving', 0.9, '2026-05-03T00:00:00Z', 1, 'user_model', 'manual', 0.7);
INSERT INTO open_questions (id, subject_entity_id, question_text, asked_at, ttl_expires_at, state)
VALUES (401, 11, 'Is this alive today or archive?', '2026-05-03T00:00:00Z', '2026-06-03T00:00:00Z', 'open');
`)
	if err != nil {
		t.Fatalf("seed graph: %v", err)
	}
}
