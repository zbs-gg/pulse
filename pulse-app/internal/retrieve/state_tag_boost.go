package retrieve

import (
	"os"
	"sort"
	"strings"
	"unicode"
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
// When it does fire, the re-rank is a stable PARTITION with an in-group
// tie-break, not a soft multiplier (a ×1.15-style positional boost measurably
// under-delivered: a matching item sitting a few ranks down stayed buried
// while the host had EXPLICITLY labeled it for the current state):
//
//  1. Partition: candidates whose state:* tag matches the current state (an
//     event tagged `state:<k>` matches when context_flags[k] >= 0.5; an event
//     tagged `state:calm` matches only when user_state is PRESENT and NO
//     context flag is active) move, as a group, above all other candidates.
//     The host's explicit "this memory is for that state" label dominates;
//     everything else keeps the frozen ranking's relative order.
//  2. Inside the matched group, when a specific flag is active: lexical
//     tie-break — matched items are stably re-sorted by how many significant
//     query terms (>=4 runes, prefix-tolerant) their text contains. Same
//     principle as the store's keyword-recall term-coverage ranking.
//  3. Inside the matched group, on the calm path (state present, no active
//     flag): thematic-coherence tie-break — matched items are stably
//     re-sorted by embedding similarity to the frozen ranking's OWN top-1
//     (the anchor). Calm advice should be about the same topic the frozen
//     ranking judged most relevant; a calm-tagged memory from an unrelated
//     topic must not win just because it is calm-tagged.
//
// Both tie-breaks are stable: equal keys preserve the frozen order, and when
// texts/vectors are unavailable the group keeps the frozen order untouched.
const (
	// stateTagBoostMult marks a state-tag match in the score-breakdown
	// annotation (the re-rank itself is a partition, not a multiplication).
	stateTagBoostMult = 1.15
	// stateTagActiveFloor is the context_flags value at which a flag counts
	// as active.
	stateTagActiveFloor = 0.5
	// stateTagPrefix marks the tags this step reads; all other tags are
	// ignored.
	stateTagPrefix = "state:"
	// stateTagCalmFlag is the special flag matched when user_state is present
	// and no context flag is active.
	stateTagCalmFlag = "calm"
	// stateTagMinTermRunes is the minimum rune length of a significant query
	// term for the lexical tie-break.
	stateTagMinTermRunes = 4
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

// significantQueryTerms extracts lowercase letter/digit runs of at least
// stateTagMinTermRunes runes — the same notion of "significant term" the
// store's keyword recall uses — for the lexical tie-break.
func significantQueryTerms(s string) []string {
	var terms []string
	var run []rune
	flush := func() {
		if len(run) >= stateTagMinTermRunes {
			terms = append(terms, string(run))
		}
		run = run[:0]
	}
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			run = append(run, r)
			continue
		}
		flush()
	}
	flush()
	return terms
}

// lexTermCoverage counts how many of the query terms appear in text,
// prefix-tolerant in both directions so Russian/English inflections still
// match ("задачам" ~ "задач", "release" ~ "releases"). text is expected
// lowercased (e.eventTexts is stored lowercased).
func lexTermCoverage(queryTerms []string, text string) int {
	textTerms := significantQueryTerms(text)
	n := 0
	for _, q := range queryTerms {
		for _, t := range textTerms {
			if strings.HasPrefix(t, q) || strings.HasPrefix(q, t) {
				n++
				break
			}
		}
	}
	return n
}

// applyStateTagBoost re-ranks the final id list by state-tag affinity
// (partition + in-group tie-break; see the file comment). Pure reorder: the
// returned slice holds exactly the input ids (never drops, adds, or rescores
// an id), and the second return value maps each id to its match marker for
// breakdown annotation (nil when the step was provably neutral). Caller
// holds e.mu.RLock (eventIDs/eventTags/eventTexts/eventVecs reads).
func (e *Engine) applyStateTagBoost(ids []int64, state *UserState, query string) ([]int64, map[int64]float64) {
	if state == nil || len(ids) < 2 || len(e.eventIDs) == 0 {
		return ids, nil
	}
	idxOf := make(map[int64]int, len(e.eventIDs))
	for i, id := range e.eventIDs {
		idxOf[id] = i
	}
	tagsOf := func(id int64) []string {
		if i, ok := idxOf[id]; ok && i < len(e.eventTags) {
			return e.eventTags[i]
		}
		return nil
	}
	anyTagged := false
	for _, id := range ids {
		if len(tagsOf(id)) > 0 {
			anyTagged = true
			break
		}
	}
	if !anyTagged {
		return ids, nil
	}
	active := activeContextFlags(state)
	matched := make([]int64, 0, len(ids))
	rest := make([]int64, 0, len(ids))
	mults := make(map[int64]float64, len(ids))
	for _, id := range ids {
		m := stateTagMultiplier(tagsOf(id), active, true)
		mults[id] = m
		if m != 1.0 {
			matched = append(matched, id)
		} else {
			rest = append(rest, id)
		}
	}
	if len(matched) == 0 {
		// Absent signal ⇒ nothing matches ⇒ order unchanged by construction.
		return ids, nil
	}

	if len(active) > 0 {
		// Specific state: lexical tie-break inside the matched group.
		if qterms := significantQueryTerms(query); len(qterms) > 0 {
			coverage := make(map[int64]int, len(matched))
			for _, id := range matched {
				if i, ok := idxOf[id]; ok && i < len(e.eventTexts) {
					coverage[id] = lexTermCoverage(qterms, e.eventTexts[i])
				}
			}
			sort.SliceStable(matched, func(a, b int) bool {
				return coverage[matched[a]] > coverage[matched[b]]
			})
		}
	} else {
		// Calm path: thematic-coherence tie-break to the frozen ranking's
		// own top-1. Vectors are unit-normalized at index time, so the dot
		// product is the cosine.
		if ai, ok := idxOf[ids[0]]; ok && ai < len(e.eventVecs) && len(e.eventVecs[ai]) > 0 {
			anchor := e.eventVecs[ai]
			coherence := make(map[int64]float64, len(matched))
			for _, id := range matched {
				if i, ok := idxOf[id]; ok && i < len(e.eventVecs) && len(e.eventVecs[i]) == len(anchor) {
					coherence[id] = float64(dotF32(anchor, e.eventVecs[i]))
				}
			}
			sort.SliceStable(matched, func(a, b int) bool {
				return coherence[matched[a]] > coherence[matched[b]]
			})
		}
	}

	out := make([]int64, 0, len(ids))
	out = append(out, matched...)
	out = append(out, rest...)
	return out, mults
}
