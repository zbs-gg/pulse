package retrieve

import (
	"math"
	"os"
	"sort"
	"strings"
)

// State-tag affinity boost — the state → which-item-wins channel.
//
// Convention: a capsule/event tag `state:<flag>` declares "this memory is for
// that user state" (e.g. state:deadline_pressure, state:job_insecurity,
// state:calm). Host-side extraction decides the tags; the daemon just matches
// them against `user_state.context_flags`.
//
// This is a separate RE-RANK of the final returned id list, applied strictly
// AFTER the frozen scoring pipeline (v2_pure base × v3 conditional boosts,
// RRF fusion, surfaceability) has produced its ranking — the same additive
// post-scoring overlay pattern as applyAssertionDemotion and the access-freq
// Phase B step. It never touches scoreEventsV3, v3 constants, or the scores
// the frozen path computed; it only re-sorts a COPY of positional scores
// derived from the final rank order.
//
// Default ON, gated by PULSE_STATE_TAG_BOOST (=off opts out). The default can
// be ON because neutrality without the signal is structural:
//   - no user_state on the request      → the step returns ids untouched;
//   - no candidate carries a state:* tag → every multiplier is exactly 1.0
//     and the input slice is returned unchanged;
//   - flags present but no tagged event matches (and calm doesn't apply)
//     → same: all multipliers 1.0, untouched;
//   - flag off → the step never runs.
//
// When it does fire: an event tagged `state:<k>` gets ×1.15 when
// context_flags[k] >= 0.5 (one boost max per event); an event tagged
// `state:calm` gets ×1.15 only when user_state is PRESENT and NO context
// flag is active. Re-ranked score_i = mult_i · ρ^rank_i with ρ=0.97, so a
// boosted event can climb at most ⌊ln(1.15)/ln(1/0.97)⌋ = 4 positions. The
// sort is stable with a strict (>) comparator, so an identical multiplier on
// every candidate preserves the original order exactly.
const (
	// stateTagBoostMult is the affinity multiplier for a state-tag match.
	stateTagBoostMult = 1.15
	// stateTagBoostRho is the positional-score decay for the re-rank
	// (score at rank r is ρ^r before the multiplier).
	stateTagBoostRho = 0.97
	// stateTagActiveFloor is the context_flags value at which a flag counts
	// as active.
	stateTagActiveFloor = 0.5
	// stateTagPrefix marks the tags this step reads; all other tags are
	// ignored.
	stateTagPrefix = "state:"
	// stateTagCalmFlag is the special flag matched when user_state is present
	// and no context flag is active.
	stateTagCalmFlag = "calm"
)

// stateTagBoostFlag reports whether the state-tag affinity re-rank is enabled.
// ON unless PULSE_STATE_TAG_BOOST is set to a falsy value ("0"/"false"/"no"/
// "off"). Default ON is safe: without a user_state + state:* tag pair the
// step is provably neutral (see file comment).
func stateTagBoostFlag() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PULSE_STATE_TAG_BOOST"))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// activeContextFlags returns the normalized (lowercased, trimmed) set of
// context flags with weight >= stateTagActiveFloor. Nil/empty map ⇒ empty set.
func activeContextFlags(state *UserState) map[string]bool {
	if state == nil || len(state.ContextFlags) == 0 {
		return nil
	}
	out := make(map[string]bool, len(state.ContextFlags))
	for k, v := range state.ContextFlags {
		if v < stateTagActiveFloor {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		if k != "" {
			out[k] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// stateTagMultiplier maps one event's tags to its affinity multiplier given
// the active flag set. One boost max: the first matching state:* tag wins.
// Non-state tags are ignored; unknown flags stay neutral.
func stateTagMultiplier(tags []string, active map[string]bool, statePresent bool) float64 {
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if !strings.HasPrefix(tag, stateTagPrefix) {
			continue
		}
		flag := strings.TrimPrefix(tag, stateTagPrefix)
		if flag == stateTagCalmFlag {
			// Calm is the "no load" affinity: only when a user_state came in
			// AND none of its context flags is active.
			if statePresent && len(active) == 0 {
				return stateTagBoostMult
			}
			continue
		}
		if active[flag] {
			return stateTagBoostMult
		}
	}
	return 1.0
}

// applyStateTagBoost re-ranks the final id list by state-tag affinity. Pure
// reorder: the returned slice holds exactly the input ids (never drops, adds,
// or rescores an id), and the second return value maps each id to its
// multiplier for breakdown annotation (nil when the step was provably
// neutral). Caller holds e.mu.RLock (eventIDs/eventTags reads).
func (e *Engine) applyStateTagBoost(ids []int64, state *UserState) ([]int64, map[int64]float64) {
	if state == nil || len(ids) < 2 || len(e.eventIDs) == 0 {
		return ids, nil
	}
	tagsOf := make(map[int64][]string, len(e.eventIDs))
	for i, id := range e.eventIDs {
		if i < len(e.eventTags) && len(e.eventTags[i]) > 0 {
			tagsOf[id] = e.eventTags[i]
		}
	}
	if len(tagsOf) == 0 {
		return ids, nil
	}
	active := activeContextFlags(state)
	mults := make(map[int64]float64, len(ids))
	boosted := false
	for _, id := range ids {
		m := stateTagMultiplier(tagsOf[id], active, true)
		mults[id] = m
		if m != 1.0 {
			boosted = true
		}
	}
	if !boosted {
		// Absent signal ⇒ every multiplier is exactly 1.0 ⇒ order unchanged
		// by construction. Return the input untouched.
		return ids, nil
	}
	scores := make([]float64, len(ids))
	for rank, id := range ids {
		scores[rank] = mults[id] * math.Pow(stateTagBoostRho, float64(rank))
	}
	idx := make([]int, len(ids))
	for i := range idx {
		idx[i] = i
	}
	// Stable + strict comparator: identical multipliers map onto strictly
	// decreasing ρ^rank, so ties keep the original order.
	sort.SliceStable(idx, func(a, b int) bool { return scores[idx[a]] > scores[idx[b]] })
	out := make([]int64, len(ids))
	for i, j := range idx {
		out[i] = ids[j]
	}
	return out, mults
}
