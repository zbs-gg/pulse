package contextquery

import (
	"testing"
)

func TestContextResultSchemaVersion(t *testing.T) {
	result := ContextResult{SchemaVersion: SchemaVersion}
	if result.SchemaVersion != "pulse.context.v1" {
		t.Fatalf("schema version = %q", result.SchemaVersion)
	}
}

func TestServiceQueryTraceIncludesEmotionRole(t *testing.T) {
	s := openContextTestStore(t)
	defer s.Close()
	seedContextGraph(t, s.DB())

	svc := New(ServiceConfig{
		DB: s.DB(),
		Retrieval: fakeRetrieval{
			ids:         []int64{101},
			mode:        "empathic",
			emotionRole: "soothe",
		},
	})
	got, err := svc.Query(t.Context(), ContextQueryRequest{
		Query:        "что поможет когда мне стыдно?",
		TopK:         5,
		Scope:        "user",
		IncludeTrace: true,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got.Trace == nil || got.Trace.Router["emotion_role"] != "soothe" {
		t.Fatalf("trace router = %#v, want emotion_role=soothe", got.Trace)
	}
}

func TestServiceQueryProjectsEventsEntitiesFactsAndQuestions(t *testing.T) {
	s := openContextTestStore(t)
	defer s.Close()
	seedContextGraph(t, s.DB())

	svc := New(ServiceConfig{DB: s.DB(), Retrieval: fakeRetrieval{ids: []int64{101}, mode: "empathic"}})
	got, err := svc.Query(t.Context(), ContextQueryRequest{Query: "что для меня важно", TopK: 5, Scope: "user"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Fatalf("schema = %q", got.SchemaVersion)
	}
	if got.ModeUsed != "empathic" {
		t.Fatalf("mode = %q", got.ModeUsed)
	}
	if len(got.Events) != 1 || got.Events[0].ID != 101 {
		t.Fatalf("events = %#v", got.Events)
	}
	if len(got.Entities) == 0 || got.Entities[0].CanonicalName != "Alice" {
		t.Fatalf("entities = %#v", got.Entities)
	}
	if len(got.Facts) == 0 || got.Facts[0].Text != "Alice wants to feel valued without overproving" {
		t.Fatalf("facts = %#v", got.Facts)
	}
	if len(got.ImportanceQuestions) != 1 || got.ImportanceQuestions[0].QuestionText == "" {
		t.Fatalf("importance questions = %#v", got.ImportanceQuestions)
	}
}

func TestServiceQueryFiltersEventsByObservationScope(t *testing.T) {
	s := openContextTestStore(t)
	defer s.Close()
	seedContextGraph(t, s.DB())
	_, err := s.DB().Exec(`
INSERT INTO observations (id, source_kind, source_id, content_hash, version, scope, captured_at, observed_at, actors, content_text)
VALUES (2, 'test', 'obs2', 'hash2', 1, 'assistant', '2026-05-03T00:00:00Z', '2026-05-03T00:00:00Z', '[]', 'assistant-only memory');
INSERT INTO events (id, title, description, sentiment, emotional_weight, ts, belief_class, provenance, confidence_floor)
VALUES (102, 'Assistant private event', 'This belongs to assistant scope only', 0.1, 0.7, '2026-05-03T00:00:00Z', 'self_model', 'manual', 0.5);
INSERT INTO event_entities (event_id, entity_id) VALUES (102, 11);
INSERT INTO evidence (id, subject_kind, subject_id, observation_id, weight, created_at)
VALUES (202, 'event', 102, 2, 1.0, '2026-05-03T00:00:00Z');
`)
	if err != nil {
		t.Fatalf("seed assistant event: %v", err)
	}

	svc := New(ServiceConfig{DB: s.DB(), Retrieval: fakeRetrieval{ids: []int64{101, 102}, mode: "empathic"}})
	got, err := svc.Query(t.Context(), ContextQueryRequest{Query: "scope check", Scope: "user"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got.Events) != 1 || got.Events[0].ID != 101 || got.Events[0].SourceScope != "user" {
		t.Fatalf("events = %#v", got.Events)
	}
	for _, ev := range got.Events {
		if ev.ID == 102 || ev.SourceScope == "assistant" {
			t.Fatalf("cross-scope event leaked: %#v", ev)
		}
	}
}

func TestServiceQueryRedactsSensitiveActorEntities(t *testing.T) {
	s := openContextTestStore(t)
	defer s.Close()
	seedContextGraph(t, s.DB())
	_, err := s.DB().Exec(`
INSERT INTO sensitive_actors (entity_id, policy, reason, added_at, added_by)
VALUES (11, 'summary_only', 'private actor', '2026-05-03T00:00:00Z', 'test');
`)
	if err != nil {
		t.Fatalf("seed sensitive actor: %v", err)
	}

	svc := New(ServiceConfig{DB: s.DB(), Retrieval: fakeRetrieval{ids: []int64{101}, mode: "empathic"}})
	got, err := svc.Query(t.Context(), ContextQueryRequest{Query: "privacy", Scope: "user"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got.Private) == 0 || got.Private[0].Policy != "summary_only" {
		t.Fatalf("private redactions = %#v", got.Private)
	}
}
func TestServiceQueryRedactsSafetyBoundaryEntities(t *testing.T) {
	s := openContextTestStore(t)
	defer s.Close()
	seedContextGraph(t, s.DB())
	_, err := s.DB().Exec(`
INSERT INTO entities (id, canonical_name, kind, first_seen, last_seen, salience_score, emotional_weight, description_md)
VALUES (12, 'Internal safety boundary marker', 'safety_boundary', '2026-05-03T00:00:00Z', '2026-05-03T00:00:00Z', 1, 1, 'Must not be surfaced as normal content');
INSERT INTO event_entities (event_id, entity_id) VALUES (101, 12);
`)
	if err != nil {
		t.Fatalf("seed boundary: %v", err)
	}

	svc := New(ServiceConfig{DB: s.DB(), Retrieval: fakeRetrieval{ids: []int64{101}, mode: "empathic"}})
	got, err := svc.Query(t.Context(), ContextQueryRequest{Query: "эль", Scope: "user"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, ent := range got.Entities {
		if ent.Kind == "safety_boundary" {
			t.Fatalf("safety boundary leaked as entity: %#v", ent)
		}
	}
	if len(got.Forbidden) == 0 || got.Forbidden[0].Policy != "never-default" {
		t.Fatalf("forbidden = %#v", got.Forbidden)
	}
}

func TestServiceQueryProjectsAtomicFactsAndRelations(t *testing.T) {
	s := openContextTestStore(t)
	defer s.Close()
	seedContextGraph(t, s.DB())
	_, err := s.DB().Exec(`
INSERT INTO atomic_facts (id, event_id, text, text_hash, attributed_to, extractor, confidence)
VALUES (501, 101, 'Alice noted a recognition theme in this event', 'atom501', 'user', 'manual', 0.88);
INSERT INTO entities (id, canonical_name, kind, first_seen, last_seen, salience_score, emotional_weight)
VALUES (13, 'Sam', 'person', '2026-05-03T00:00:00Z', '2026-05-03T00:00:00Z', 0.5, 0.4);
INSERT INTO relations (id, from_entity_id, to_entity_id, kind, strength, first_seen, last_seen, context)
VALUES (601, 11, 13, 'partner', 0.9, '2026-05-03T00:00:00Z', '2026-05-03T00:00:00Z', 'Alice and Sam are partners');
INSERT INTO evidence (id, subject_kind, subject_id, observation_id, weight, created_at)
VALUES (205, 'relation', 601, 1, 1.0, '2026-05-03T00:00:00Z');
`)
	if err != nil {
		t.Fatalf("seed atomic facts and relations: %v", err)
	}

	svc := New(ServiceConfig{DB: s.DB(), Retrieval: fakeRetrieval{ids: []int64{101}, mode: "factual"}})
	got, err := svc.Query(t.Context(), ContextQueryRequest{Query: "факт", Scope: "user"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	foundAtomic := false
	for _, fact := range got.Facts {
		if fact.ID == 501 && fact.Kind == "atomic_fact" && fact.Text == "Alice noted a recognition theme in this event" {
			foundAtomic = true
		}
	}
	if !foundAtomic {
		t.Fatalf("atomic fact not projected: %#v", got.Facts)
	}
	if len(got.Relations) != 1 || got.Relations[0].ID != 601 || got.Relations[0].Summary != "Alice and Sam are partners" {
		t.Fatalf("relations = %#v", got.Relations)
	}
}
func TestServiceQueryDoesNotLoadEntitiesFactsOrQuestionsFromHiddenScope(t *testing.T) {
	s := openContextTestStore(t)
	defer s.Close()
	seedContextGraph(t, s.DB())
	_, err := s.DB().Exec(`
INSERT INTO observations (id, source_kind, source_id, content_hash, version, scope, captured_at, observed_at, actors, content_text)
VALUES (2, 'test', 'obs2', 'hash2', 1, 'assistant', '2026-05-03T00:00:00Z', '2026-05-03T00:00:00Z', '[]', 'assistant-only memory');
INSERT INTO entities (id, canonical_name, kind, first_seen, last_seen, salience_score, emotional_weight, description_md)
VALUES (14, 'AssistantOnly', 'person', '2026-05-03T00:00:00Z', '2026-05-03T00:00:00Z', 1, 1, 'Should not leak');
INSERT INTO events (id, title, description, sentiment, emotional_weight, ts, belief_class, provenance, confidence_floor)
VALUES (103, 'Assistant only event', 'private event', 0.1, 0.7, '2026-05-03T00:00:00Z', 'self_model', 'manual', 0.5);
INSERT INTO event_entities (event_id, entity_id) VALUES (103, 14);
INSERT INTO evidence (id, subject_kind, subject_id, observation_id, weight, created_at)
VALUES (203, 'event', 103, 2, 1.0, '2026-05-03T00:00:00Z');
INSERT INTO facts (id, entity_id, text, confidence, created_at, source_obs_id, belief_class, provenance, confidence_floor)
VALUES (303, 14, 'Assistant-only fact should not leak', 0.9, '2026-05-03T00:00:00Z', 2, 'self_model', 'manual', 0.5);
INSERT INTO open_questions (id, subject_entity_id, question_text, asked_at, ttl_expires_at, state)
VALUES (402, 14, 'Assistant-only question?', '2026-05-03T00:00:00Z', '2026-06-03T00:00:00Z', 'open');
`)
	if err != nil {
		t.Fatalf("seed hidden scope: %v", err)
	}

	svc := New(ServiceConfig{DB: s.DB(), Retrieval: fakeRetrieval{ids: []int64{103}, mode: "empathic"}})
	got, err := svc.Query(t.Context(), ContextQueryRequest{Query: "scope", Scope: "user"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got.Events) != 0 || len(got.Entities) != 0 || len(got.Facts) != 0 {
		t.Fatalf("hidden scope leaked: events=%#v entities=%#v facts=%#v", got.Events, got.Entities, got.Facts)
	}
	for _, q := range got.ImportanceQuestions {
		if q.ID == 402 {
			t.Fatalf("hidden scope question leaked: %#v", q)
		}
	}
}

func TestServiceQueryDoesNotProjectRelationsWithoutScopedEvidence(t *testing.T) {
	s := openContextTestStore(t)
	defer s.Close()
	seedContextGraph(t, s.DB())
	_, err := s.DB().Exec(`
INSERT INTO observations (id, source_kind, source_id, content_hash, version, scope, captured_at, observed_at, actors, content_text)
VALUES (2, 'test', 'obs2', 'hash2', 1, 'assistant', '2026-05-03T00:00:00Z', '2026-05-03T00:00:00Z', '[]', 'assistant-only relation evidence');
INSERT INTO entities (id, canonical_name, kind, first_seen, last_seen, salience_score, emotional_weight)
VALUES (13, 'Sam', 'person', '2026-05-03T00:00:00Z', '2026-05-03T00:00:00Z', 0.5, 0.4);
INSERT INTO relations (id, from_entity_id, to_entity_id, kind, strength, first_seen, last_seen, context)
VALUES (602, 11, 13, 'hidden_relation', 0.9, '2026-05-03T00:00:00Z', '2026-05-03T00:00:00Z', 'Should not leak');
INSERT INTO evidence (id, subject_kind, subject_id, observation_id, weight, created_at)
VALUES (204, 'relation', 602, 2, 1.0, '2026-05-03T00:00:00Z');
`)
	if err != nil {
		t.Fatalf("seed hidden relation: %v", err)
	}

	svc := New(ServiceConfig{DB: s.DB(), Retrieval: fakeRetrieval{ids: []int64{101}, mode: "empathic"}})
	got, err := svc.Query(t.Context(), ContextQueryRequest{Query: "relation", Scope: "user"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, rel := range got.Relations {
		if rel.ID == 602 {
			t.Fatalf("hidden relation leaked: %#v", rel)
		}
	}
}
func TestServiceQueryDoesNotLeakFactsFromHiddenScopeOnVisibleEntity(t *testing.T) {
	s := openContextTestStore(t)
	defer s.Close()
	seedContextGraph(t, s.DB())
	_, err := s.DB().Exec(`
INSERT INTO observations (id, source_kind, source_id, content_hash, version, scope, captured_at, observed_at, actors, content_text)
VALUES (2, 'test', 'obs2', 'hash2', 1, 'assistant', '2026-05-03T00:00:00Z', '2026-05-03T00:00:00Z', '[]', 'assistant-only fact evidence');
INSERT INTO facts (id, entity_id, text, confidence, created_at, source_obs_id, belief_class, provenance, confidence_floor)
VALUES (304, 11, 'Hidden fact on visible entity', 0.9, '2026-05-03T00:00:00Z', 2, 'self_model', 'manual', 0.5);
`)
	if err != nil {
		t.Fatalf("seed hidden fact: %v", err)
	}

	svc := New(ServiceConfig{DB: s.DB(), Retrieval: fakeRetrieval{ids: []int64{101}, mode: "empathic"}})
	got, err := svc.Query(t.Context(), ContextQueryRequest{Query: "fact scope", Scope: "user"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, fact := range got.Facts {
		if fact.ID == 304 {
			t.Fatalf("hidden fact leaked: %#v", fact)
		}
	}
}

func TestServiceQueryRequiresRelationScopedEvidence(t *testing.T) {
	s := openContextTestStore(t)
	defer s.Close()
	seedContextGraph(t, s.DB())
	_, err := s.DB().Exec(`
INSERT INTO entities (id, canonical_name, kind, first_seen, last_seen, salience_score, emotional_weight)
VALUES (13, 'Sam', 'person', '2026-05-03T00:00:00Z', '2026-05-03T00:00:00Z', 0.5, 0.4);
INSERT INTO event_entities (event_id, entity_id) VALUES (101, 13);
INSERT INTO relations (id, from_entity_id, to_entity_id, kind, strength, first_seen, last_seen, context)
VALUES (603, 11, 13, 'unscoped_relation', 0.9, '2026-05-03T00:00:00Z', '2026-05-03T00:00:00Z', 'Both entities visible but relation itself has no scoped evidence');
`)
	if err != nil {
		t.Fatalf("seed relation: %v", err)
	}

	svc := New(ServiceConfig{DB: s.DB(), Retrieval: fakeRetrieval{ids: []int64{101}, mode: "empathic"}})
	got, err := svc.Query(t.Context(), ContextQueryRequest{Query: "relation scope", Scope: "user"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, rel := range got.Relations {
		if rel.ID == 603 {
			t.Fatalf("unscoped relation leaked: %#v", rel)
		}
	}
}

func TestServiceQueryDoesNotReturnSubjectlessOpenQuestions(t *testing.T) {
	s := openContextTestStore(t)
	defer s.Close()
	seedContextGraph(t, s.DB())
	_, err := s.DB().Exec(`
INSERT INTO open_questions (id, subject_entity_id, question_text, asked_at, ttl_expires_at, state)
VALUES (403, NULL, 'Global question should stay out of scoped projection', '2026-05-03T00:00:00Z', '2026-06-03T00:00:00Z', 'open');
`)
	if err != nil {
		t.Fatalf("seed subjectless question: %v", err)
	}

	svc := New(ServiceConfig{DB: s.DB(), Retrieval: fakeRetrieval{ids: []int64{101}, mode: "empathic"}})
	got, err := svc.Query(t.Context(), ContextQueryRequest{Query: "questions", Scope: "user"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, q := range got.ImportanceQuestions {
		if q.ID == 403 {
			t.Fatalf("subjectless question leaked: %#v", q)
		}
	}
}

func TestServiceQueryDoesNotProjectRelationsToSensitiveActors(t *testing.T) {
	s := openContextTestStore(t)
	defer s.Close()
	seedContextGraph(t, s.DB())
	_, err := s.DB().Exec(`
INSERT INTO entities (id, canonical_name, kind, first_seen, last_seen, salience_score, emotional_weight)
VALUES (13, 'SensitivePerson', 'person', '2026-05-03T00:00:00Z', '2026-05-03T00:00:00Z', 0.5, 0.4);
INSERT INTO sensitive_actors (entity_id, policy, reason, added_at, added_by)
VALUES (13, 'no_capture', 'private actor', '2026-05-03T00:00:00Z', 'test');
INSERT INTO relations (id, from_entity_id, to_entity_id, kind, strength, first_seen, last_seen, context)
VALUES (604, 11, 13, 'sensitive_relation', 0.9, '2026-05-03T00:00:00Z', '2026-05-03T00:00:00Z', 'Should not reveal sensitive endpoint');
INSERT INTO evidence (id, subject_kind, subject_id, observation_id, weight, created_at)
VALUES (206, 'relation', 604, 1, 1.0, '2026-05-03T00:00:00Z');
`)
	if err != nil {
		t.Fatalf("seed sensitive relation: %v", err)
	}

	svc := New(ServiceConfig{DB: s.DB(), Retrieval: fakeRetrieval{ids: []int64{101}, mode: "empathic"}})
	got, err := svc.Query(t.Context(), ContextQueryRequest{Query: "sensitive relation", Scope: "user"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, rel := range got.Relations {
		if rel.ID == 604 {
			t.Fatalf("sensitive relation leaked: %#v", rel)
		}
	}
}

func TestServiceQuerySharedScopeDoesNotBypassPrivateScopes(t *testing.T) {
	s := openContextTestStore(t)
	defer s.Close()
	seedContextGraph(t, s.DB())

	svc := New(ServiceConfig{DB: s.DB(), Retrieval: fakeRetrieval{ids: []int64{101}, mode: "empathic"}})
	got, err := svc.Query(t.Context(), ContextQueryRequest{Query: "shared", Scope: "shared"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got.Events) != 0 || len(got.Entities) != 0 || len(got.Facts) != 0 || len(got.ImportanceQuestions) != 0 {
		t.Fatalf("shared scope bypassed private data: events=%#v entities=%#v facts=%#v questions=%#v", got.Events, got.Entities, got.Facts, got.ImportanceQuestions)
	}
}

func TestServiceQueryDoesNotReturnFactsWithoutSourceObservation(t *testing.T) {
	s := openContextTestStore(t)
	defer s.Close()
	seedContextGraph(t, s.DB())
	_, err := s.DB().Exec(`
INSERT INTO facts (id, entity_id, text, confidence, created_at, source_obs_id, belief_class, provenance, confidence_floor)
VALUES (305, 11, 'Sourceless fact should not leak', 0.9, '2026-05-03T00:00:00Z', NULL, 'user_model', 'manual', 0.5);
`)
	if err != nil {
		t.Fatalf("seed sourceless fact: %v", err)
	}

	svc := New(ServiceConfig{DB: s.DB(), Retrieval: fakeRetrieval{ids: []int64{101}, mode: "empathic"}})
	got, err := svc.Query(t.Context(), ContextQueryRequest{Query: "source", Scope: "user"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, fact := range got.Facts {
		if fact.ID == 305 {
			t.Fatalf("sourceless fact leaked: %#v", fact)
		}
	}
}

func TestServiceQueryKeepsHypothesesOutOfFacts(t *testing.T) {
	s := openContextTestStore(t)
	defer s.Close()
	seedContextGraph(t, s.DB())
	_, err := s.DB().Exec(`
INSERT INTO facts (id, entity_id, text, confidence, created_at, source_obs_id, belief_class, provenance, confidence_floor)
VALUES (302, 11, 'Alice might be avoiding this through busyness', 0.4, '2026-05-03T00:00:00Z', 1, 'hypothesis', 'memory_pattern', 0);
`)
	if err != nil {
		t.Fatalf("seed hypothesis: %v", err)
	}

	svc := New(ServiceConfig{DB: s.DB(), Retrieval: fakeRetrieval{ids: []int64{101}, mode: "empathic"}})
	got, err := svc.Query(t.Context(), ContextQueryRequest{Query: "что происходит", Scope: "user"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, fact := range got.Facts {
		if fact.ID == 302 {
			t.Fatalf("hypothesis leaked as fact: %#v", fact)
		}
	}
	if len(got.Uncertainty) == 0 || got.Uncertainty[0].SubjectID != 302 {
		t.Fatalf("uncertainty = %#v", got.Uncertainty)
	}
}
