package retrieve

import (
	"reflect"
	"testing"
)

// Pro NO-GO #7: chain A→B→C must leave the CURRENT leaf (C) above both stales,
// not the oldest on top (the old pairwise reorder produced [A,C,B]).
func TestDemote_ChainPlacesCurrentLeafOnTop(t *testing.T) {
	got := demoteSupersededBelowCurrent([]int64{1, 2, 3}, [][2]int64{{1, 2}, {2, 3}})
	want := []int64{3, 1, 2} // current leaf 3 first; stales 1,2 after, in original order
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("chain demotion: got %v want %v", got, want)
	}
}

// Pro NO-GO #6: events touched by no pair keep their relative order; only the
// stale moves (to just after its current).
func TestDemote_OrderNeutralForUnrelated(t *testing.T) {
	got := demoteSupersededBelowCurrent([]int64{1, 2, 3, 4, 5}, [][2]int64{{2, 4}})
	want := []int64{1, 3, 4, 2, 5} // 1,3,5 keep order; 2 demoted to just after 4
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order-neutrality: got %v want %v", got, want)
	}
}

// Determinism: result must not depend on the order pairs arrive in.
func TestDemote_IndependentOfPairOrder(t *testing.T) {
	ids := []int64{1, 2, 3, 4}
	a := demoteSupersededBelowCurrent(ids, [][2]int64{{1, 2}, {3, 4}})
	b := demoteSupersededBelowCurrent(ids, [][2]int64{{3, 4}, {1, 2}})
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("pair-order dependence: %v vs %v", a, b)
	}
}

func TestDemote_NoPairsIsIdentity(t *testing.T) {
	ids := []int64{5, 3, 9, 1}
	if got := demoteSupersededBelowCurrent(ids, nil); !reflect.DeepEqual(got, ids) {
		t.Fatalf("no pairs must be identity: got %v want %v", got, ids)
	}
}
