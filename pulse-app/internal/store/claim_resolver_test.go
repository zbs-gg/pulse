package store

import (
	"math"
	"testing"
)

func TestDecideClaim_AllGuardsTable(t *testing.T) {
	personal := Scope{Type: "personal"}
	const thr = 0.83
	cases := []struct {
		name    string
		inPred  string
		inObj   string
		inVF    string
		inScope Scope
		cue     bool
		cands   []ClaimCandidate
		want    string
	}{
		{"no candidates -> insert", "is", "xhigh", "", personal, true, nil, "insert"},
		{"true update (cue, newer) -> supersede", "is", "xhigh", "2026-06-02", personal, true,
			[]ClaimCandidate{{ID: 1, Predicate: "is", ObjectNorm: "medium", ValidFrom: "2026-06-01", Scope: personal, Cosine: 0.95}}, "supersede"},
		{"identical object -> noop", "is", "medium", "", personal, true,
			[]ClaimCandidate{{ID: 1, Predicate: "is", ObjectNorm: "medium", Scope: personal, Cosine: 0.99}}, "noop"},
		{"cosine below threshold -> insert (keep both)", "is", "9am", "", personal, true,
			[]ClaimCandidate{{ID: 1, Predicate: "is", ObjectNorm: "cortado", Scope: personal, Cosine: 0.40}}, "insert"},
		{"different predicate -> insert (never candidate)", "drinks-at", "9am", "", personal, true,
			[]ClaimCandidate{{ID: 1, Predicate: "is", ObjectNorm: "cortado", Scope: personal, Cosine: 0.99}}, "insert"},
		{"different scope -> insert", "is", "xhigh", "", Scope{Type: "repo", ID: "pulse"}, true,
			[]ClaimCandidate{{ID: 1, Predicate: "is", ObjectNorm: "medium", Scope: personal, Cosine: 0.99}}, "insert"},
		// Pro NO-GO adversarial cases:
		{"no change-cue (multi-valued sibling) -> insert", "is", "english", "", personal, false,
			[]ClaimCandidate{{ID: 1, Predicate: "is", ObjectNorm: "russian", Scope: personal, Cosine: 0.95}}, "insert"},
		{"backfill (older valid_from) -> insert, never replaces current", "is", "bali", "2025-01-01", personal, true,
			[]ClaimCandidate{{ID: 1, Predicate: "is", ObjectNorm: "lisbon", ValidFrom: "2026-06-01", Scope: personal, Cosine: 0.99}}, "insert"},
	}
	for _, tc := range cases {
		got := DecideClaim(tc.inPred, tc.inObj, tc.inVF, tc.inScope, tc.cue, tc.cands, thr)
		if got.Action != tc.want {
			t.Errorf("%s: got %q (%s), want %q", tc.name, got.Action, got.Reason, tc.want)
		}
	}
}

// The headline precision case: a "coffee preference = cortado" claim and a
// "coffee time = 9am" claim must NEVER merge — different predicate => different
// claim_key => they are never even candidates for each other.
func TestDecideClaim_CoffeePreferenceVsCoffeeTime_NeverMerge(t *testing.T) {
	personal := Scope{Type: "personal"}
	// Even handed a high cosine, the predicate guard keeps them apart.
	d := DecideClaim("time", "9am", "", personal, true,
		[]ClaimCandidate{{ID: 7, Predicate: "preference", ObjectNorm: "cortado", Scope: personal, Cosine: 0.99}}, 0.83)
	if d.Action != "insert" {
		t.Fatalf("coffee-time must not supersede coffee-preference: got %q (%s)", d.Action, d.Reason)
	}
}

func TestCosine(t *testing.T) {
	if c := cosine([]float32{1, 0, 0}, []float32{1, 0, 0}); math.Abs(c-1) > 1e-6 {
		t.Errorf("identical vectors cosine=%v want 1", c)
	}
	if c := cosine([]float32{1, 0}, []float32{0, 1}); math.Abs(c) > 1e-6 {
		t.Errorf("orthogonal cosine=%v want 0", c)
	}
	if c := cosine([]float32{1, 0}, []float32{0, 0}); c != 0 {
		t.Errorf("zero-norm cosine=%v want 0", c)
	}
}
