package retrieve

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

// Bridge world: Mara --works_on--> Atlas <--affected-- Tomas. The bridge event
// e1 ("Atlas launch caused a dispute") mentions ONLY Atlas — never Mara/Tomas —
// so cosine+BM25 on a "Mara … Tomas" query go blind to it. The typed relation
// walk (GraphMode="walk") reaches Atlas from both seeds and surfaces e1.
// GraphMode="" must NOT change behaviour (default-OFF parity).
func TestGraphRetrieval_WalkSurfacesBridgeEventCosineMisses(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "graph.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()
	now := time.Now()
	occ := now.Add(-72 * time.Hour).UTC().Format(time.RFC3339)

	res, err := s.SaveSemanticDelta(store.SemanticDelta{
		Schema: store.SemanticDeltaSchema,
		Source: store.SemanticDeltaSource{Host: "claude-code", ConversationScope: "current_turn",
			Timestamp: now.UTC().Format(time.RFC3339), ThreadID: "graph-bridge-test"},
		Nodes: []store.SemanticNode{
			{ClientID: "p:mara", Kind: "person", CanonicalName: "Mara", PrivacyTier: "normal"},
			{ClientID: "p:tomas", Kind: "person", CanonicalName: "Tomas", PrivacyTier: "normal"},
			{ClientID: "x:atlas", Kind: "project", CanonicalName: "Atlas", PrivacyTier: "normal"},
		},
		Edges: []store.SemanticEdge{
			{From: "p:mara", To: "x:atlas", Kind: "works_on", Strength: 0.9, PrivacyTier: "normal"},
			{From: "p:tomas", To: "x:atlas", Kind: "affected", Strength: 0.9, PrivacyTier: "normal"},
		},
		Events: []store.SemanticEvent{
			{ClientID: "e0", Title: "Mara built Atlas", Summary: "Mara spent the quarter building Atlas.",
				EntityRefs: []string{"p:mara", "x:atlas"}, Confidence: 0.9, PrivacyTier: "normal", OccurredAt: occ},
			{ClientID: "e1", Title: "Atlas launch caused a budget dispute",
				Summary:    "The Atlas launch set off a budget dispute across teams.",
				EntityRefs: []string{"x:atlas"}, Confidence: 0.9, PrivacyTier: "normal", OccurredAt: occ},
			{ClientID: "e2", Title: "Tomas joined a tense review", Summary: "Tomas was pulled into a tense review.",
				EntityRefs: []string{"p:tomas"}, Confidence: 0.9, PrivacyTier: "normal", OccurredAt: occ},
		},
		RawInputIncluded: false,
	})
	if err != nil {
		t.Fatalf("SaveSemanticDelta: %v", err)
	}
	if len(res.EventIDs) != 3 {
		t.Fatalf("expected 3 event ids, got %#v", res.EventIDs)
	}
	e0ID, e1ID := res.EventIDs[0], res.EventIDs[1]

	eng := New(Config{Store: s, Embedder: &fakeEmbedder{dim: 5}, ReferenceTime: &now})
	ctx := context.Background()
	query := "Why did Mara's work end up involving Tomas"
	has := func(ids []int64, want int64) bool {
		for _, id := range ids {
			if id == want {
				return true
			}
		}
		return false
	}

	// default-OFF: graph path produces no candidates at all.
	if got := eng.retrieveGraphCandidates(ctx, query, "", 5); got != nil {
		t.Fatalf("GraphMode off: expected nil candidates, got %v", got)
	}
	// anchored: only events directly linked to the seed entities (Mara→e0, Tomas→e2).
	// The bridge event e1 is linked ONLY to Atlas → anchored must NOT reach it.
	anchored := eng.retrieveGraphCandidates(ctx, query, "anchored", 5)
	if !has(anchored, e0ID) {
		t.Fatalf("anchored: expected Mara's direct event e0, got %v", anchored)
	}
	if has(anchored, e1ID) {
		t.Fatalf("anchored: did NOT expect bridge event e1 (it's only linked to Atlas, not a seed)")
	}
	// walk: relation walk reaches Atlas from the seeds → surfaces the bridge event e1.
	walk := eng.retrieveGraphCandidates(ctx, query, "walk", 5)
	if !has(walk, e1ID) {
		t.Fatalf("walk: expected bridge event e1 via the Mara→Atlas / Tomas→Atlas walk, got %v", walk)
	}
}
