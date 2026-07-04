package retrieve

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// ── unit: multiplier math ──────────────────────────────────────────────────

// (d) Flat corpora — all-zero, all-equal, count-at-baseline — yield a
// multiplier of EXACTLY 1.0 (not approximately).
func TestAccessFreqMultiplier_FlatIsExactlyOne(t *testing.T) {
	cases := []struct{ count, min, max int64 }{
		{0, 0, 0}, // all-zero corpus
		{7, 7, 7}, // all-equal corpus
		{5, 5, 9}, // this event sits at the flat baseline
		{3, 5, 9}, // below baseline (defensive) — still neutral
		{4, 9, 2}, // degenerate stats (max < min) — neutral
	}
	for _, c := range cases {
		if m := accessFreqMultiplier(c.count, c.min, c.max); m != 1.0 {
			t.Errorf("accessFreqMultiplier(%d,%d,%d) = %v, want exactly 1.0", c.count, c.min, c.max, m)
		}
	}
}

// Skewed counts produce a multiplier that is >1, monotone in count, and
// bounded by both the β envelope and the hard cap.
func TestAccessFreqMultiplier_BoundedAndMonotone(t *testing.T) {
	m1 := accessFreqMultiplier(1, 0, 100)
	m50 := accessFreqMultiplier(50, 0, 100)
	m100 := accessFreqMultiplier(100, 0, 100)
	if !(1.0 < m1 && m1 < m50 && m50 < m100) {
		t.Errorf("expected 1 < m(1) < m(50) < m(100), got %v %v %v", m1, m50, m100)
	}
	// β envelope: log1p ratio < 1 ⇒ m < 1+β, even at extreme counts.
	if huge := accessFreqMultiplier(1<<40, 0, 1<<40); huge >= 1.0+accessBoostBeta {
		t.Errorf("multiplier %v breaches the 1+β envelope %v", huge, 1.0+accessBoostBeta)
	}
	// Hard cap holds regardless.
	for _, c := range []int64{1, 10, 1000, 1 << 50} {
		if m := accessFreqMultiplier(c, 0, c); m > accessBoostCap {
			t.Errorf("multiplier %v for count %d breaches hard cap %v", m, c, accessBoostCap)
		}
	}
}

// ── unit: the re-rank itself (pure function over engine indexes) ──────────

func boostEngine(ids, counts []int64) *Engine {
	return &Engine{eventIDs: ids, eventAccess: counts}
}

// (b, function level) Flat or absent signal is a strict identity: the input
// slice comes back unchanged (same order — ties keep stable order) and no
// multipliers are reported.
func TestApplyAccessFreqBoost_FlatOrAbsentIsIdentity(t *testing.T) {
	ranked := []int64{10, 20, 30, 40}
	cases := []struct {
		name   string
		counts []int64
	}{
		{"all zero", []int64{0, 0, 0, 0}},
		{"all equal nonzero", []int64{7, 7, 7, 7}},
	}
	for _, c := range cases {
		eng := boostEngine([]int64{10, 20, 30, 40}, c.counts)
		got, mults := eng.applyAccessFreqBoost(ranked)
		if !reflect.DeepEqual(got, ranked) {
			t.Errorf("%s: order changed: got %v, want %v", c.name, got, ranked)
		}
		if mults != nil {
			t.Errorf("%s: expected no multipliers on flat signal, got %v", c.name, mults)
		}
	}
	// Candidates unknown to the index (no loaded counts at all) — identity.
	eng := boostEngine(nil, nil)
	if got, mults := eng.applyAccessFreqBoost(ranked); !reflect.DeepEqual(got, ranked) || mults != nil {
		t.Errorf("absent index: got %v (mults %v), want identity", got, mults)
	}
	// Single-element and empty lists — identity.
	eng = boostEngine([]int64{10}, []int64{99})
	if got, _ := eng.applyAccessFreqBoost([]int64{10}); !reflect.DeepEqual(got, []int64{10}) {
		t.Errorf("single id must be identity, got %v", got)
	}
}

// (c, function level) One event with a much higher count climbs, but the
// displacement is hard-bounded: with β=0.05, ρ=0.97 the multiplier advantage
// (<1+β) cannot beat two positional steps ((1/ρ)² > 1+β), so it moves up AT
// MOST one position. The id set is untouched.
func TestApplyAccessFreqBoost_HighCountMovesUpBounded(t *testing.T) {
	ids := []int64{10, 20, 30, 40, 50}
	counts := []int64{0, 0, 0, 0, 100} // last-ranked event is recalled often
	eng := boostEngine(ids, counts)
	got, mults := eng.applyAccessFreqBoost([]int64{10, 20, 30, 40, 50})
	want := []int64{10, 20, 30, 50, 40} // climbed exactly one position
	if !reflect.DeepEqual(got, want) {
		t.Errorf("re-rank: got %v, want %v", got, want)
	}
	if m := mults[50]; !(m > 1.0 && m <= accessBoostCap) {
		t.Errorf("expected boosted multiplier for 50 in (1, cap], got %v", m)
	}
	if m := mults[10]; m != 1.0 {
		t.Errorf("baseline event must keep multiplier exactly 1.0, got %v", m)
	}
}

// ── end-to-end through Retrieve ────────────────────────────────────────────

// setCounts writes access_count directly (test-only) and reloads the index.
func setCounts(t *testing.T, eng *Engine, counts map[int64]int64) {
	t.Helper()
	for id, n := range counts {
		if _, err := eng.store.DB().Exec(`UPDATE events SET access_count = ? WHERE id = ?`, n, id); err != nil {
			t.Fatalf("set access_count for %d: %v", id, err)
		}
	}
	if err := eng.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
}

// boostRetrieve runs the deterministic fixture query. With setupTestStore's
// orthogonal embeddings and fakeEmbedder("cab") the frozen ranking is [1 3 2]
// (cosine·recency; every v3 boost neutral; BM25 finds no match).
func boostRetrieve(t *testing.T, eng *Engine) *RetrieveResponse {
	t.Helper()
	if err := eng.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	resp, err := eng.Retrieve(context.Background(), RetrieveRequest{
		Query: "cab", Mode: ModeEmpathic, TopK: 3,
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	return resp
}

// (a) Flag OFF: even with wildly skewed counts in the store, the final
// ranking and every score breakdown are identical to the no-counts baseline.
func TestRetrieve_AccessFreqBoostOff_RankingIdentical(t *testing.T) {
	t.Setenv("PULSE_ACCESS_FREQ", "")       // no Phase A counter writes
	t.Setenv("PULSE_ACCESS_FREQ_BOOST", "") // Phase B off (default)

	s := setupTestStore(t)
	ref := time.Now()
	eng := New(Config{Store: s, Embedder: &fakeEmbedder{dim: 5}, ReferenceTime: &ref})
	if eng.accessFreqBoostEnabled {
		t.Fatalf("expected accessFreqBoostEnabled=false when PULSE_ACCESS_FREQ_BOOST is unset")
	}

	baseline := boostRetrieve(t, eng)
	if want := []int64{1, 3, 2}; !reflect.DeepEqual(baseline.EventIDs, want) {
		t.Fatalf("fixture ranking drifted: got %v, want %v", baseline.EventIDs, want)
	}

	setCounts(t, eng, map[int64]int64{2: 50}) // skew the counts hard
	after := boostRetrieve(t, eng)

	if !reflect.DeepEqual(after.EventIDs, baseline.EventIDs) {
		t.Errorf("flag OFF must ignore counts: got %v, want %v", after.EventIDs, baseline.EventIDs)
	}
	if !reflect.DeepEqual(after.ScoreBreakdowns, baseline.ScoreBreakdowns) {
		t.Errorf("flag OFF must leave breakdowns identical:\n before=%v\n after =%v",
			baseline.ScoreBreakdowns, after.ScoreBreakdowns)
	}
}

// (b) Flag ON + flat signal (all-zero, then all-equal counts): ranking and
// breakdowns are identical to the flag-OFF run — provable neutrality.
func TestRetrieve_AccessFreqBoostOn_FlatCountsIdentical(t *testing.T) {
	t.Setenv("PULSE_ACCESS_FREQ", "")
	s := setupTestStore(t)
	ref := time.Now()

	t.Setenv("PULSE_ACCESS_FREQ_BOOST", "")
	engOff := New(Config{Store: s, Embedder: &fakeEmbedder{dim: 5}, ReferenceTime: &ref})
	off := boostRetrieve(t, engOff)

	t.Setenv("PULSE_ACCESS_FREQ_BOOST", "1")
	engOn := New(Config{Store: s, Embedder: &fakeEmbedder{dim: 5}, ReferenceTime: &ref})
	if !engOn.accessFreqBoostEnabled {
		t.Fatalf("expected accessFreqBoostEnabled=true when PULSE_ACCESS_FREQ_BOOST=1")
	}

	// All-zero counts.
	onZero := boostRetrieve(t, engOn)
	if !reflect.DeepEqual(onZero.EventIDs, off.EventIDs) {
		t.Errorf("flag ON + all-zero counts must match OFF ranking: got %v, want %v", onZero.EventIDs, off.EventIDs)
	}
	if !reflect.DeepEqual(onZero.ScoreBreakdowns, off.ScoreBreakdowns) {
		t.Errorf("flag ON + all-zero counts must not annotate breakdowns:\n off=%v\n on =%v",
			off.ScoreBreakdowns, onZero.ScoreBreakdowns)
	}

	// All-equal nonzero counts — still corpus-flat, still identical.
	setCounts(t, engOn, map[int64]int64{1: 7, 2: 7, 3: 7})
	onEqual := boostRetrieve(t, engOn)
	if !reflect.DeepEqual(onEqual.EventIDs, off.EventIDs) {
		t.Errorf("flag ON + all-equal counts must match OFF ranking: got %v, want %v", onEqual.EventIDs, off.EventIDs)
	}
	if !reflect.DeepEqual(onEqual.ScoreBreakdowns, off.ScoreBreakdowns) {
		t.Errorf("flag ON + all-equal counts must not annotate breakdowns:\n off=%v\n on =%v",
			off.ScoreBreakdowns, onEqual.ScoreBreakdowns)
	}
}

// (c) Flag ON + one event with a much higher count: it moves up — bounded to
// one position — the id set is unchanged, and every FROZEN breakdown field
// stays byte-identical to the flag-OFF run (only the additive AccessFreqBoost
// annotation differs).
func TestRetrieve_AccessFreqBoostOn_HighCountReranks(t *testing.T) {
	t.Setenv("PULSE_ACCESS_FREQ", "")
	s := setupTestStore(t)
	ref := time.Now()

	t.Setenv("PULSE_ACCESS_FREQ_BOOST", "")
	engOff := New(Config{Store: s, Embedder: &fakeEmbedder{dim: 5}, ReferenceTime: &ref})

	t.Setenv("PULSE_ACCESS_FREQ_BOOST", "1")
	engOn := New(Config{Store: s, Embedder: &fakeEmbedder{dim: 5}, ReferenceTime: &ref})

	// Event 2 is the worst-ranked ([1 3 2] baseline) but heavily recalled.
	setCounts(t, engOn, map[int64]int64{2: 50})

	off := boostRetrieve(t, engOff) // sees the same store/counts, flag OFF
	on := boostRetrieve(t, engOn)

	if want := []int64{1, 3, 2}; !reflect.DeepEqual(off.EventIDs, want) {
		t.Fatalf("flag-OFF baseline drifted: got %v, want %v", off.EventIDs, want)
	}
	// Bounded climb: exactly one position (2 passes 3, never passes 1).
	if want := []int64{1, 2, 3}; !reflect.DeepEqual(on.EventIDs, want) {
		t.Errorf("flag ON re-rank: got %v, want %v", on.EventIDs, want)
	}
	// Same id SET — the overlay never drops or adds.
	set := func(ids []int64) map[int64]bool {
		m := map[int64]bool{}
		for _, x := range ids {
			m[x] = true
		}
		return m
	}
	if !reflect.DeepEqual(set(on.EventIDs), set(off.EventIDs)) {
		t.Errorf("id set changed: off=%v on=%v", off.EventIDs, on.EventIDs)
	}
	// Frozen v3 fields untouched; only the additive annotation may appear.
	for id, offBD := range off.ScoreBreakdowns {
		onBD, ok := on.ScoreBreakdowns[id]
		if !ok {
			t.Errorf("event %d missing from flag-ON breakdowns", id)
			continue
		}
		gotAnnotation := onBD.AccessFreqBoost
		onBD.AccessFreqBoost = nil
		if !reflect.DeepEqual(onBD, offBD) {
			t.Errorf("frozen breakdown fields changed for event %d:\n off=%+v\n on =%+v", id, offBD, onBD)
		}
		if id == 2 {
			if gotAnnotation == nil || !(*gotAnnotation > 1.0 && *gotAnnotation <= accessBoostCap) {
				t.Errorf("event 2 should carry an AccessFreqBoost annotation in (1, cap], got %v", gotAnnotation)
			}
		} else if gotAnnotation != nil {
			t.Errorf("event %d (baseline count) must have no annotation, got %v", id, *gotAnnotation)
		}
	}
}
