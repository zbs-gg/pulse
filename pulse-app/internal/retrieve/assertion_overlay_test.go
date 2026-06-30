package retrieve

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

// addSupersededPair makes event `stale` the source of a claim that is later
// superseded by a claim sourced from event `current` (same subject+predicate).
func addSupersededPair(t *testing.T, s *store.Store, stale, current int64) {
	t.Helper()
	if _, err := s.SupersedeAssertion(store.Assertion{
		Subject: "effort", Predicate: "is", ObjectText: "old", SourceEventIDs: []int64{stale},
	}); err != nil {
		t.Fatalf("seed stale assertion: %v", err)
	}
	if _, err := s.SupersedeAssertion(store.Assertion{
		Subject: "effort", Predicate: "is", ObjectText: "new", SourceEventIDs: []int64{current},
	}); err != nil {
		t.Fatalf("seed current assertion: %v", err)
	}
}

func TestAssertionDemotion_MovesStaleBelowCurrent(t *testing.T) {
	s := setupTestStore(t)
	addSupersededPair(t, s, 1, 3) // event1 stale, event3 current
	now := time.Now()
	eng := New(Config{Store: s, Embedder: &fakeEmbedder{dim: 5}, ReferenceTime: &now, AssertionOverlay: true})

	// stale (1) ranked above current (3) -> demote 1 to just after 3.
	if got := eng.applyAssertionDemotion([]int64{1, 2, 3}); !reflect.DeepEqual(got, []int64{2, 3, 1}) {
		t.Errorf("demotion: got %v, want [2 3 1]", got)
	}
	// stale already below current -> unchanged.
	if got := eng.applyAssertionDemotion([]int64{3, 2, 1}); !reflect.DeepEqual(got, []int64{3, 2, 1}) {
		t.Errorf("already-below: got %v, want [3 2 1]", got)
	}
	// only one of the pair present -> unchanged (never drops/adds).
	if got := eng.applyAssertionDemotion([]int64{1, 2}); !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Errorf("partial pair: got %v, want [1 2]", got)
	}
}

func TestAssertionDemotion_NoPairsIsIdentity(t *testing.T) {
	s := setupTestStore(t) // no assertions added
	now := time.Now()
	eng := New(Config{Store: s, Embedder: &fakeEmbedder{dim: 5}, ReferenceTime: &now, AssertionOverlay: true})
	in := []int64{1, 2, 3}
	if got := eng.applyAssertionDemotion(in); !reflect.DeepEqual(got, in) {
		t.Errorf("no superseded pairs must be identity: got %v, want %v", got, in)
	}
}

// The load-bearing v3-neutrality proof the scoreEventsV3 golden parity test
// cannot give (it never calls Retrieve): even with a superseded pair present and
// the overlay ON, the empathic path's per-event SCORES are byte-identical and no
// id is dropped or added — the overlay only reorders the final list.
func TestAssertionOverlay_EmpathicScoresUnchanged(t *testing.T) {
	// One store + one fixed ReferenceTime shared by both engines, so the ONLY
	// difference is the overlay flag (otherwise recency uses different now()s).
	s := setupTestStore(t)
	addSupersededPair(t, s, 1, 3)
	ref := time.Now()
	mk := func(overlay bool) *RetrieveResponse {
		eng := New(Config{Store: s, Embedder: &fakeEmbedder{dim: 5}, ReferenceTime: &ref, AssertionOverlay: overlay})
		if err := eng.Init(context.Background()); err != nil {
			t.Fatalf("Init: %v", err)
		}
		resp, err := eng.Retrieve(context.Background(), RetrieveRequest{
			Query: "что-то эмоциональное", Mode: ModeEmpathic, TopK: 3,
		})
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		return resp
	}
	off, on := mk(false), mk(true)

	// 1. Per-event score breakdowns identical -> no rescoring, v3 untouched.
	if !reflect.DeepEqual(off.ScoreBreakdowns, on.ScoreBreakdowns) {
		t.Errorf("score breakdowns changed under overlay:\n off=%v\n on =%v", off.ScoreBreakdowns, on.ScoreBreakdowns)
	}
	// 2. Same SET of ids -> overlay never drops or adds.
	set := func(ids []int64) map[int64]bool {
		m := map[int64]bool{}
		for _, x := range ids {
			m[x] = true
		}
		return m
	}
	if !reflect.DeepEqual(set(off.EventIDs), set(on.EventIDs)) {
		t.Errorf("id set changed: off=%v on=%v", off.EventIDs, on.EventIDs)
	}
	// 3. With overlay on, the stale event (1) is never ranked above its
	//    correction (3).
	pos := func(ids []int64, v int64) int {
		for i, x := range ids {
			if x == v {
				return i
			}
		}
		return -1
	}
	if s1, s3 := pos(on.EventIDs, 1), pos(on.EventIDs, 3); s1 >= 0 && s3 >= 0 && s1 < s3 {
		t.Errorf("stale event 1 ranked above its correction 3 under overlay: %v", on.EventIDs)
	}
}
