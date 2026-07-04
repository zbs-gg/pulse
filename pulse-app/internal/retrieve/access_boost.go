package retrieve

import (
	"math"
	"os"
	"sort"
	"strings"
)

// Access-frequency salience boost (Phase B; Phase A = migration 029 counters).
//
// A mild, bounded RE-RANK of the final returned id list, applied strictly
// AFTER the frozen scoring pipeline (v2_pure base × v3 conditional boosts,
// RRF fusion, surfaceability) has produced its ranking — same additive
// overlay pattern as applyAssertionDemotion. It never touches scoreEventsV3,
// v3 constants, or the scores the frozen path computed; it only re-sorts a
// COPY of positional scores derived from the final rank order.
//
// OFF by default: gated by PULSE_ACCESS_FREQ_BOOST (separate from the Phase A
// counter flag PULSE_ACCESS_FREQ). Neutrality is structural, not incidental:
//   - flag off  → the step never runs; ids pass through untouched;
//   - flag on + flat signal (all candidate counts equal, incl. all-zero, or
//     no counts loaded) → every multiplier is EXACTLY 1.0 and the step
//     returns the input slice unchanged;
//   - flag on + skewed counts → boosted score_i = mult_i · ρ^rank_i with
//     mult_i ∈ [1.0, 1+β) capped at accessBoostCap, so an event can climb
//     past an event k positions above it only when its multiplier advantage
//     exceeds (1/ρ)^k. With β=0.05 and ρ=0.97 that bounds displacement to
//     at most ⌊ln(1+β)/ln(1/ρ)⌋ = 1 position — a nudge, never a takeover.
//
// The sort is stable and the comparator is strict (>), so identical boosted
// scores — in particular the all-equal-multiplier case — preserve the
// original order exactly.
const (
	// accessBoostBeta scales the salience nudge. Multipliers live in
	// [1.0, 1+accessBoostBeta) before the hard cap.
	accessBoostBeta = 0.05
	// accessBoostCap is a hard safety ceiling on any multiplier, independent
	// of accessBoostBeta.
	accessBoostCap = 1.10
	// accessBoostRho is the positional-score decay for the re-rank
	// (score at rank r is ρ^r before the multiplier).
	accessBoostRho = 0.97
)

// accessFreqBoostFlag reports whether the Phase B re-rank is enabled. OFF
// unless PULSE_ACCESS_FREQ_BOOST is set to a truthy value ("1"/"true"/"yes"/
// "on"). Default OFF ⇒ the post-scoring step never runs ⇒ ranking unchanged.
func accessFreqBoostFlag() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PULSE_ACCESS_FREQ_BOOST"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// accessFreqMultiplier maps one event's access count to its salience
// multiplier, given the min/max counts across the candidate list:
//
//	mult = 1 + β · log1p(count−min) / (1 + log1p(max−min)),  capped.
//
// The corpus-flat baseline is the candidate minimum: count == min (and in
// particular ANY all-equal corpus, zero or not) yields EXACTLY 1.0. The
// log1p ratio is < 1, so mult < 1+β always; accessBoostCap is a second,
// hard ceiling on top.
func accessFreqMultiplier(count, minCount, maxCount int64) float64 {
	if maxCount <= minCount || count <= minCount {
		return 1.0
	}
	if count > maxCount {
		count = maxCount
	}
	rel := math.Log1p(float64(count - minCount))
	norm := 1.0 + math.Log1p(float64(maxCount-minCount))
	m := 1.0 + accessBoostBeta*rel/norm
	if m > accessBoostCap {
		m = accessBoostCap
	}
	return m
}

// applyAccessFreqBoost re-ranks the final id list by bounded access-frequency
// salience. Pure reorder: the returned slice holds exactly the input ids
// (never drops, adds, or rescoring an id), and the second return value maps
// each id to its multiplier for breakdown annotation (nil when the step was
// provably neutral). Caller holds e.mu.RLock (eventIDs/eventAccess reads).
func (e *Engine) applyAccessFreqBoost(ids []int64) ([]int64, map[int64]float64) {
	if len(ids) < 2 || len(e.eventIDs) == 0 {
		return ids, nil
	}
	countOf := make(map[int64]int64, len(e.eventIDs))
	for i, id := range e.eventIDs {
		if i < len(e.eventAccess) {
			countOf[id] = e.eventAccess[i]
		}
	}
	// Baseline stats over the candidate list itself. Ids without a loaded
	// count (e.g. surfaced by BM25 without an embedding row) stay neutral
	// and do not distort the baseline.
	var minC, maxC int64
	seen := false
	for _, id := range ids {
		c, ok := countOf[id]
		if !ok {
			continue
		}
		if !seen {
			minC, maxC = c, c
			seen = true
			continue
		}
		if c < minC {
			minC = c
		}
		if c > maxC {
			maxC = c
		}
	}
	if !seen || maxC == minC {
		// Absent or corpus-flat signal ⇒ every multiplier is exactly 1.0 ⇒
		// order unchanged by construction. Return the input untouched.
		return ids, nil
	}
	mults := make(map[int64]float64, len(ids))
	scores := make([]float64, len(ids))
	for rank, id := range ids {
		m := 1.0
		if c, ok := countOf[id]; ok {
			m = accessFreqMultiplier(c, minC, maxC)
		}
		mults[id] = m
		scores[rank] = m * math.Pow(accessBoostRho, float64(rank))
	}
	idx := make([]int, len(ids))
	for i := range idx {
		idx[i] = i
	}
	// Stable + strict comparator: equal boosted scores (identical multipliers
	// at adjacent ranks can never produce equal scores, but duplicates of the
	// multiplier map onto strictly decreasing ρ^rank) keep original order.
	sort.SliceStable(idx, func(a, b int) bool { return scores[idx[a]] > scores[idx[b]] })
	out := make([]int64, len(ids))
	for i, j := range idx {
		out[i] = ids[j]
	}
	return out, mults
}
