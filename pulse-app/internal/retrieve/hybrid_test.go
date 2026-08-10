package retrieve

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/embed"
	"github.com/nkkmnk/pulse/internal/store"
)

// fakeEmbedder returns deterministic vectors based on text content. Used in
// unit tests to avoid hitting the real Cohere API.
type fakeEmbedder struct {
	dim int
}

func (f *fakeEmbedder) Model() string { return "fake-embed" }

func (f *fakeEmbedder) Embed(_ context.Context, texts []string,
	_ embed.InputType) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, f.dim)
		// Map first 10 unique characters to dimensions; tests can rig text
		// for predictable cosine ranking by sharing prefixes.
		for j, r := range t {
			if j >= f.dim {
				break
			}
			v[j] = float32(r%32) / 32.0
		}
		// L2 normalize
		var sumSq float32
		for _, x := range v {
			sumSq += x * x
		}
		if sumSq > 0 {
			inv := 1.0 / float32(sqrt32(sumSq))
			for k := range v {
				v[k] *= inv
			}
		}
		out[i] = v
	}
	return out, nil
}

type managedBGEProofEmbedder struct{}

func (*managedBGEProofEmbedder) Model() string { return "bge-m3" }
func (*managedBGEProofEmbedder) Embed(_ context.Context, texts []string, _ embed.InputType) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for index := range texts {
		out[index] = []float32{1, 0}
	}
	return out, nil
}

func TestManagedBGEReadsLegacyMigrationBridgeWhileBackfillIsPending(t *testing.T) {
	vault, err := store.Open(t.TempDir() + "/legacy-bridge.db")
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	result, err := vault.DB().Exec(`INSERT INTO events(title,description,scorer_version,ts) VALUES('old decision','preserved meaning','host-extracted','2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	eventID, _ := result.LastInsertId()
	if _, err := vault.DB().Exec(`INSERT INTO event_embeddings(event_id,model,dim,vector_json,text_source) VALUES(?, 'bge-m3-mlx-fp16', 2, '[1,0]', 'old decision')`, eventID); err != nil {
		t.Fatal(err)
	}
	engine := New(Config{Store: vault, Embedder: &managedBGEProofEmbedder{}})
	if err := engine.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(engine.eventIDs) != 1 || engine.eventIDs[0] != eventID {
		t.Fatalf("legacy migration bridge was not searchable: ids=%v", engine.eventIDs)
	}
}

func sqrt32(x float32) float64 {
	return float64Sqrt(float64(x))
}

// avoid math import dependency cycle in tests
func float64Sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 20; i++ {
		z = (z + x/z) / 2
	}
	return z
}

// setupTestStore creates an in-memory SQLite DB with all migrations applied
// plus a tiny seed (3 events, 3 embeddings, 1 fact, 1 fact embedding).
// Returns the store; caller closes via t.Cleanup.
func setupTestStore(t *testing.T) *store.Store {
	t.Helper()
	tmpFile := t.TempDir() + "/test.db"
	s, err := store.Open(tmpFile)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	db := s.DB()
	now := time.Now().UTC()
	rows := []struct {
		id      int64
		title   string
		ts      time.Time
		vec     []float32
		factTxt string
	}{
		{1, "первая встреча с Алексом",
			now.Add(-30 * 24 * time.Hour), []float32{1, 0, 0, 0, 0}, ""},
		{2, "хайк со страхом перед публикой",
			now.Add(-7 * 24 * time.Hour), []float32{0, 1, 0, 0, 0},
			"Alice went on a hike where she felt fear of public speaking."},
		{3, "вчерашний ship-day",
			now.Add(-1 * 24 * time.Hour), []float32{0, 0, 1, 0, 0}, ""},
	}
	for _, r := range rows {
		_, err = db.Exec(
			`INSERT INTO events(id, title, ts) VALUES (?, ?, ?)`,
			r.id, r.title, r.ts.Format(time.RFC3339))
		if err != nil {
			t.Fatalf("insert event %d: %v", r.id, err)
		}
		vecJSON, _ := json.Marshal(r.vec)
		_, err = db.Exec(
			`INSERT INTO event_embeddings(event_id, model, dim, vector_json, text_source, updated_at)
             VALUES (?, ?, ?, ?, ?, ?)`,
			r.id, "fake-embed", len(r.vec), string(vecJSON),
			r.title, time.Now().Format(time.RFC3339))
		if err != nil {
			t.Fatalf("insert embedding %d: %v", r.id, err)
		}
	}

	// One fact, linked to event 2 (the fear-on-hike event)
	res, err := db.Exec(
		`INSERT INTO atomic_facts(event_id, text, text_hash, attributed_to,
            fear, extractor, extracted_at)
         VALUES (?, ?, ?, ?, ?, ?, ?)`,
		2, "Alice experienced fear during a hike with public speaking.",
		"abc123", "user", 0.7, "test", time.Now().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("insert fact: %v", err)
	}
	factID, _ := res.LastInsertId()
	factVec := []float32{0, 0.9, 0.4, 0, 0}
	factVecJSON, _ := json.Marshal(factVec)
	_, err = db.Exec(
		`INSERT INTO atomic_fact_embeddings(fact_id, model, dim, vector_json,
            text_source, updated_at)
         VALUES (?, ?, ?, ?, ?, ?)`,
		factID, "fake-embed", len(factVec), string(factVecJSON),
		"fact text", time.Now().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("insert fact embedding: %v", err)
	}

	return s
}

func TestEngine_E2E_EmpathicMode(t *testing.T) {
	s := setupTestStore(t)
	emb := &fakeEmbedder{dim: 5}
	now := time.Now()
	eng := New(Config{Store: s, Embedder: emb, ReferenceTime: &now})
	if err := eng.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got := len(eng.eventIDs); got != 3 {
		t.Fatalf("expected 3 events loaded, got %d", got)
	}
	if got := len(eng.factIDs); got != 1 {
		t.Fatalf("expected 1 fact loaded, got %d", got)
	}

	// Empathic query should retrieve top-K events
	resp, err := eng.Retrieve(context.Background(), RetrieveRequest{
		Query: "что-то эмоциональное", // fallback empathic mode
		Mode:  ModeEmpathic,
		TopK:  3,
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if got := len(resp.EventIDs); got != 3 {
		t.Errorf("expected 3 event ids, got %d (%v)", got, resp.EventIDs)
	}
	if resp.ModeUsed != ModeEmpathic {
		t.Errorf("expected mode=empathic, got %s", resp.ModeUsed)
	}
}

func TestEngine_E2E_FactualMode(t *testing.T) {
	s := setupTestStore(t)
	emb := &fakeEmbedder{dim: 5}
	now := time.Now()
	eng := New(Config{Store: s, Embedder: emb, ReferenceTime: &now})
	if err := eng.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	resp, err := eng.Retrieve(context.Background(), RetrieveRequest{
		Query: "tell me about Alice's fear", // factual mode via "tell me about"
		Mode:  ModeAuto,
		TopK:  3,
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if resp.ModeUsed != ModeFactual {
		t.Errorf("expected router → factual, got %s (reasoning=%s)",
			resp.ModeUsed, resp.RouterDecision.Reasoning)
	}
	if len(resp.EventIDs) == 0 {
		t.Error("expected at least 1 event id from factual retrieval")
	}
	// Top result should be event 2 (the only fact's parent event)
	if resp.EventIDs[0] != 2 {
		t.Errorf("expected event 2 first (fear-fact parent); got %v",
			resp.EventIDs)
	}
}

func TestEngine_E2E_RouterAuto(t *testing.T) {
	s := setupTestStore(t)
	emb := &fakeEmbedder{dim: 5}
	now := time.Now()
	eng := New(Config{Store: s, Embedder: emb, ReferenceTime: &now})
	if err := eng.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	cases := []struct {
		query        string
		expectedMode QueryMode
	}{
		{"когда был мой первый митап?", ModeFactual},
		{"почему я тогда злился?", ModeChain},
		{"что я тогда чувствовал в страхе перед публикой", ModeEmpathic}, // emotion keyword → empathic
		{"random unrelated query", ModeEmpathic},                         // default
	}
	for _, tc := range cases {
		resp, err := eng.Retrieve(context.Background(), RetrieveRequest{
			Query: tc.query, Mode: ModeAuto, TopK: 3,
		})
		if err != nil {
			t.Errorf("Retrieve(%q): %v", tc.query, err)
			continue
		}
		if resp.ModeUsed != tc.expectedMode {
			t.Errorf("query %q: want mode %s, got %s (reasoning=%s)",
				tc.query, tc.expectedMode, resp.ModeUsed,
				resp.RouterDecision.Reasoning)
		}
	}
}

func TestEngine_RetrieveIncludesEmotionRole(t *testing.T) {
	s := setupTestStore(t)
	emb := &fakeEmbedder{dim: 5}
	now := time.Now()
	eng := New(Config{Store: s, Embedder: emb, ReferenceTime: &now})
	if err := eng.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	resp, err := eng.Retrieve(context.Background(), RetrieveRequest{
		Query:     "что поможет когда мне стыдно?",
		Mode:      ModeAuto,
		UserState: &UserState{MoodVector: map[string]float64{"shame": 0.8}},
		TopK:      3,
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if resp.EmotionRole.Role != EmotionRoleSoothe {
		t.Fatalf("emotion_role = %#v, want %s", resp.EmotionRole, EmotionRoleSoothe)
	}
	if resp.EmotionRole.StateEmotion != "shame" {
		t.Fatalf("state emotion = %q, want shame", resp.EmotionRole.StateEmotion)
	}
}

func TestEngine_FragileStateDoesNotReturnOnlyPainWithoutExplicitIntent(t *testing.T) {
	s := setupTestStore(t)
	db := s.DB()
	if _, err := db.Exec(`
INSERT INTO event_emotions(event_id, sadness, shame, tagger)
VALUES (1, 0.9, 0.8, 'manual'), (2, 0.8, 0.7, 'manual');
INSERT INTO event_emotions(event_id, trust, joy, tagger)
VALUES (3, 0.8, 0.5, 'manual');
`); err != nil {
		t.Fatalf("seed event emotions: %v", err)
	}

	emb := &fakeEmbedder{dim: 5}
	qVecs, err := emb.Embed(context.Background(), []string{"мне стыдно и плохо"}, embed.TypeSearchQuery)
	if err != nil {
		t.Fatalf("embed query fixture: %v", err)
	}
	painVecJSON, _ := json.Marshal(qVecs[0])
	repairVecJSON, _ := json.Marshal([]float32{0, 0, 0, 0, 0})
	if _, err := db.Exec(`
UPDATE event_embeddings SET vector_json=? WHERE event_id IN (1, 2);
UPDATE event_embeddings SET vector_json=? WHERE event_id=3;
`, string(painVecJSON), string(repairVecJSON)); err != nil {
		t.Fatalf("seed ranked vectors: %v", err)
	}
	now := time.Now()
	eng := New(Config{Store: s, Embedder: emb, ReferenceTime: &now})
	if err := eng.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	lowSleep := 0.2
	role := InferEmotionRole("мне стыдно и плохо", &UserState{
		MoodVector:   map[string]float64{"shame": 0.8},
		SleepQuality: &lowSleep,
	})
	guarded, action := eng.applyFragileSurfaceability([]int64{1, 2}, 2, role)
	if len(guarded) != 2 {
		t.Fatalf("expected 2 guarded events, got %v", guarded)
	}
	if allPainEventIDs(guarded, map[int64][]float32{
		1: {0, 0.9, 0, 0, 0, 0, 0, 0, 0.8, 0},
		2: {0, 0.8, 0, 0, 0, 0, 0, 0, 0.7, 0},
		3: {0.5, 0, 0, 0, 0.8, 0, 0, 0, 0, 0},
	}) {
		t.Fatalf("fragile retrieval returned only pain memories: %v", guarded)
	}
	if action != SurfaceabilityPairWithRepairAnchor {
		t.Fatalf("surfaceability action = %q, want %q (events=%v)",
			action, SurfaceabilityPairWithRepairAnchor, guarded)
	}
}

// Sanity: missing atomic_facts table (fresh DB without 017 applied) → engine
// still works in empathic mode without panicking.
func TestEngine_HandlesMissingFactsTable(t *testing.T) {
	tmpFile := t.TempDir() + "/no-facts.db"
	s, err := store.Open(tmpFile)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	// Drop the atomic_facts table to simulate older schema
	if _, err := s.DB().Exec("DROP TABLE IF EXISTS atomic_facts"); err != nil {
		t.Fatalf("drop atomic_facts: %v", err)
	}
	if _, err := s.DB().Exec("DROP TABLE IF EXISTS atomic_fact_embeddings"); err != nil {
		t.Fatalf("drop atomic_fact_embeddings: %v", err)
	}

	emb := &fakeEmbedder{dim: 5}
	eng := New(Config{Store: s, Embedder: emb})
	if err := eng.Init(context.Background()); err != nil {
		t.Fatalf("Init should tolerate missing facts table: %v", err)
	}
	if len(eng.factIDs) != 0 {
		t.Errorf("expected 0 facts (table missing); got %d", len(eng.factIDs))
	}
}

// expandChainFromSeeds must order a linear chain root → leaf and respect the
// edge direction loaded from event_chains (parent_id = ancestor, child_id =
// descendant). Mirrors retrieval_v3.py expand_chain_from_seeds semantics.
func TestExpandChainFromSeeds_RootBeforeLeaf(t *testing.T) {
	eng := New(Config{})
	// Chain: 22 → 33 → 44 (22 is the causal root). Distinct days_ago so the
	// ancestor-depth ordering is unambiguous.
	eng.eventIDs = []int64{22, 33, 44}
	eng.eventDays = []float64{1000, 500, 10}
	eng.parentToChild = map[int64][]int64{22: {33}, 33: {44}}
	eng.childToParent = map[int64][]int64{33: {22}, 44: {33}}

	got := eng.expandChainFromSeeds([]int64{44}, 4) // seed at leaf, expand back
	want := []int64{22, 33, 44}                     // root first, leaf last
	if len(got) != len(want) {
		t.Fatalf("expandChainFromSeeds len = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expandChainFromSeeds = %v, want %v (root→leaf order)", got, want)
		}
	}
}

// depth must bound the BFS: with depth=1 from a leaf we reach only its direct
// neighbour, not the whole chain.
func TestExpandChainFromSeeds_DepthBound(t *testing.T) {
	eng := New(Config{})
	eng.eventIDs = []int64{22, 33, 44}
	eng.eventDays = []float64{1000, 500, 10}
	eng.parentToChild = map[int64][]int64{22: {33}, 33: {44}}
	eng.childToParent = map[int64][]int64{33: {22}, 44: {33}}

	got := eng.expandChainFromSeeds([]int64{44}, 1) // only 44 and its parent 33
	if len(got) != 2 {
		t.Fatalf("depth=1 from leaf should reach 2 nodes, got %v", got)
	}
	if got[0] != 33 || got[1] != 44 {
		t.Fatalf("depth=1 expansion = %v, want [33 44]", got)
	}
}

// connectedComponent returns the candidates reachable via chain edges.
func TestConnectedComponent_ReachesCandidates(t *testing.T) {
	eng := New(Config{})
	eng.eventIDs = []int64{22, 33, 99}
	eng.parentToChild = map[int64][]int64{22: {33}}
	eng.childToParent = map[int64][]int64{33: {22}}

	cands := map[int64]bool{22: true, 33: true, 99: true}
	reach := eng.connectedComponent(22, cands)
	if !reach[22] || !reach[33] {
		t.Fatalf("connectedComponent(22) should reach {22,33}, got %v", reach)
	}
	if reach[99] {
		t.Fatalf("connectedComponent(22) must not reach disconnected 99, got %v", reach)
	}
}

// retrieveChain over a DB-backed chain (22→33) must surface both chain members
// with the root before the child, exercising the loadChains predecessor→parent
// mapping end to end.
func TestRetrieveChain_DBBacked_RootBeforeChild(t *testing.T) {
	tmpFile := t.TempDir() + "/chain.db"
	s, err := store.Open(tmpFile)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	db := s.DB()
	now := time.Now().UTC()

	// Two events that both match the query strongly, plus chain edge 22→33.
	events := []struct {
		id  int64
		ttl string
		ago time.Duration
		vec []float32
	}{
		{22, "early disappointment shame anchor", 90 * 24 * time.Hour, []float32{1, 1, 0, 0, 0}},
		{33, "later shame echo event", 30 * 24 * time.Hour, []float32{1, 1, 0, 0, 0}},
	}
	for _, ev := range events {
		if _, err := db.Exec(`INSERT INTO events(id, title, ts) VALUES (?, ?, ?)`,
			ev.id, ev.ttl, now.Add(-ev.ago).Format(time.RFC3339)); err != nil {
			t.Fatalf("insert event %d: %v", ev.id, err)
		}
		vecJSON, _ := json.Marshal(ev.vec)
		if _, err := db.Exec(
			`INSERT INTO event_embeddings(event_id, model, dim, vector_json, text_source, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			ev.id, "fake-embed", len(ev.vec), string(vecJSON), ev.ttl,
			time.Now().Format(time.RFC3339)); err != nil {
			t.Fatalf("insert embedding %d: %v", ev.id, err)
		}
	}
	// parent_id=22 (ancestor) → child_id=33 (descendant).
	if _, err := db.Exec(
		`INSERT INTO event_chains(parent_id, child_id, strength, kind) VALUES (?,?,?,?)`,
		22, 33, 1.0, "causal"); err != nil {
		t.Fatalf("insert chain edge: %v", err)
	}

	emb := &fakeEmbedder{dim: 5}
	eng := New(Config{Store: s, Embedder: emb})
	if err := eng.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	qVec, err := eng.embedQuery(context.Background(), "shame anchor echo")
	if err != nil {
		t.Fatalf("embedQuery: %v", err)
	}
	ids := eng.retrieveChain(qVec, "shame anchor echo", 3, nil)
	idx := func(id int64) int {
		for i, v := range ids {
			if v == id {
				return i
			}
		}
		return -1
	}
	if idx(22) < 0 || idx(33) < 0 {
		t.Fatalf("retrieveChain should include both 22 and 33, got %v", ids)
	}
	if idx(22) >= idx(33) {
		t.Fatalf("root 22 must precede child 33, got %v", ids)
	}
}

// Confirm Cohere client implements the Embedder interface (compile-time guard)
var _ Embedder = (*embed.CohereClient)(nil)

// Avoid unused-import warnings in skinny test files
var _ = fmt.Sprintf
var _ = sql.ErrNoRows
