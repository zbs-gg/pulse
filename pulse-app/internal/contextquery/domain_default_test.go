package contextquery

import "testing"

// Real life and fiction (e.g. a book being written) must not mix by default:
// with DefaultDomains=["real"], a query that omits DomainsAllowed returns real
// memory only; fiction surfaces solely when the caller asks for it explicitly.
func TestServiceQuery_DefaultDomainRealHidesFiction(t *testing.T) {
	s := openContextTestStore(t)
	defer s.Close()
	seedContextGraph(t, s.DB()) // real event 101 for entity 11, in 'user' scope
	// a fiction event, in the same 'user' scope so ONLY the domain filter can drop it
	if _, err := s.DB().Exec(`
INSERT INTO observations (id, source_kind, source_id, content_hash, version, scope, captured_at, observed_at, actors, content_text)
VALUES (3, 'test', 'obs3', 'hash3', 1, 'user', '2026-05-03T00:00:00Z', '2026-05-03T00:00:00Z', '[]', 'book scene draft');
INSERT INTO events (id, title, description, sentiment, emotional_weight, ts, belief_class, provenance, confidence_floor, domain)
VALUES (103, 'Book scene', 'Sonya walks into the airlock', 0.1, 0.7, '2026-05-03T00:00:00Z', 'self_model', 'manual', 0.5, 'fiction_content');
INSERT INTO event_entities (event_id, entity_id) VALUES (103, 11);
INSERT INTO evidence (id, subject_kind, subject_id, observation_id, weight, created_at)
VALUES (203, 'event', 103, 3, 1.0, '2026-05-03T00:00:00Z');
`); err != nil {
		t.Fatalf("seed fiction event: %v", err)
	}

	svc := New(ServiceConfig{DB: s.DB(),
		Retrieval:      fakeRetrieval{ids: []int64{101, 103}, mode: "empathic"},
		DefaultDomains: []string{"real"}})

	// default (no DomainsAllowed) → fiction is hidden
	got, err := svc.Query(t.Context(), ContextQueryRequest{Query: "x", Scope: "user"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, ev := range got.Events {
		if ev.ID == 103 {
			t.Fatalf("fiction event leaked into the default (real) query: %#v", ev)
		}
	}
	if len(got.Events) == 0 || got.Events[0].ID != 101 {
		t.Fatalf("expected the real event 101, got %#v", got.Events)
	}

	// explicit fiction request → the fiction event surfaces
	got2, err := svc.Query(t.Context(), ContextQueryRequest{
		Query: "x", Scope: "user", DomainsAllowed: []string{"fiction_content"}})
	if err != nil {
		t.Fatalf("Query fiction: %v", err)
	}
	found := false
	for _, ev := range got2.Events {
		if ev.ID == 103 {
			found = true
		}
	}
	if !found {
		t.Fatalf("explicit fiction request did not surface the fiction event: %#v", got2.Events)
	}
}
