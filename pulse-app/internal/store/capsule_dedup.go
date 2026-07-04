package store

import (
	"sort"
	"strings"
	"time"
	"unicode"
)

// ConsolidateOptions controls a near-duplicate capsule consolidation pass.
// This pass is explicit and opt-in — it is never called from the write path
// (RememberCapsule) or the retrieve hot path. It uses a pure-Go lexical measure
// (no embedder, no LLM, no transcript) and invalidate-not-delete semantics.
type ConsolidateOptions struct {
	// DryRun computes and returns the result without writing anything.
	DryRun bool `json:"dry_run"`
	// Threshold is the token-set Jaccard cut-off. Default 0.90; hard-clamped
	// to [0.85, 1.0]. Two capsules of the same kind whose token sets overlap at
	// or above this ratio are treated as near-duplicates.
	//
	// Jaccard is semantics-blind: it cannot tell negation or numeric drift apart
	// ("target 5M" vs "10M", "enabled" vs "disabled" differ by one token), so a
	// low threshold CAN fold two distinct claims. That is why the floor is 0.85,
	// the pass is opt-in + dry-run by default, and it is invalidate-not-delete —
	// always review a dry run before applying.
	Threshold float64 `json:"threshold"`
	// Scope, when non-empty, restricts consolidation to a single retention
	// bucket ("session" | "project" | "long_term"). "" = all retentions.
	Scope string `json:"scope"`
}

// MergedPair records one near-duplicate folded into a kept capsule.
type MergedPair struct {
	MergedID string `json:"merged_id"`
	KeptID   string `json:"kept_id"`
}

// ConsolidateResult summarizes a consolidation pass (or dry run).
type ConsolidateResult struct {
	DryRun      bool         `json:"dry_run"`
	Threshold   float64      `json:"threshold"`
	Scanned     int          `json:"scanned"`
	Clusters    int          `json:"clusters"`
	MergedCount int          `json:"merged_count"`
	KeptIDs     []string     `json:"kept_ids"`
	MergedPairs []MergedPair `json:"merged_pairs"`
}

const (
	defaultConsolidateThreshold = 0.90
	minConsolidateThreshold     = 0.85
	maxConsolidateThreshold     = 1.0
)

type consolidateRow struct {
	id      string
	kind    string
	summary string
	tokens  map[string]struct{}
	tokLen  int
}

// ConsolidateCapsules folds same-kind near-duplicate capsules into the earliest
// capsule of each cluster. Near-duplicates are UPDATE'd to status='merged' with
// merged_into pointing at the kept id — never DELETE'd, so provenance stays
// queryable and still exports. Recall already filters status='active', so a
// merged row simply stops surfacing once this explicit pass has run. Until a
// pass runs, every row is 'active' and behavior is unchanged.
func (s *Store) ConsolidateCapsules(opt ConsolidateOptions) (ConsolidateResult, error) {
	threshold := opt.Threshold
	if threshold == 0 {
		threshold = defaultConsolidateThreshold
	}
	if threshold < minConsolidateThreshold {
		threshold = minConsolidateThreshold
	}
	if threshold > maxConsolidateThreshold {
		threshold = maxConsolidateThreshold
	}

	scope := strings.TrimSpace(opt.Scope)
	rows, err := s.db.Query(`
		SELECT id, kind, redacted_summary
		  FROM memory_capsules
		 WHERE status = 'active'
		   AND (? = '' OR retention = ?)
		 ORDER BY created_at ASC, id ASC`, scope, scope)
	if err != nil {
		return ConsolidateResult{}, err
	}
	defer rows.Close()

	var loaded []consolidateRow
	for rows.Next() {
		var r consolidateRow
		if err := rows.Scan(&r.id, &r.kind, &r.summary); err != nil {
			return ConsolidateResult{}, err
		}
		r.tokens = tokenSet(r.summary)
		r.tokLen = len(r.tokens)
		loaded = append(loaded, r)
	}
	if err := rows.Err(); err != nil {
		return ConsolidateResult{}, err
	}

	result := ConsolidateResult{
		DryRun:      opt.DryRun,
		Threshold:   threshold,
		Scanned:     len(loaded),
		KeptIDs:     []string{},
		MergedPairs: []MergedPair{},
	}

	// Cluster reps per kind, in encounter (created_at ASC, id ASC) order — the
	// earliest capsule of a cluster is canonical and kept.
	repsByKind := map[string][]consolidateRow{}
	clusters := 0
	for _, r := range loaded {
		reps := repsByKind[r.kind]
		matched := ""
		for _, rep := range reps {
			// Length-ratio prefilter: Jaccard <= min/max token count, so a
			// pair whose lengths differ by more than the threshold can never
			// reach it. Cheap O(1) skip before the set intersection.
			if !lengthRatioAtLeast(r.tokLen, rep.tokLen, threshold) {
				continue
			}
			if jaccard(r.tokens, rep.tokens) >= threshold {
				matched = rep.id
				break
			}
		}
		if matched == "" {
			repsByKind[r.kind] = append(reps, r)
			result.KeptIDs = append(result.KeptIDs, r.id)
			clusters++
			continue
		}
		result.MergedPairs = append(result.MergedPairs, MergedPair{MergedID: r.id, KeptID: matched})
	}
	result.Clusters = clusters
	result.MergedCount = len(result.MergedPairs)

	sort.Strings(result.KeptIDs)

	if opt.DryRun || result.MergedCount == 0 {
		return result, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return ConsolidateResult{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, pair := range result.MergedPairs {
		if _, err := tx.Exec(`
			UPDATE memory_capsules
			   SET status = 'merged', merged_into = ?, merged_at = ?
			 WHERE id = ? AND status = 'active'`,
			pair.KeptID, now, pair.MergedID); err != nil {
			return ConsolidateResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ConsolidateResult{}, err
	}
	return result, nil
}

// tokenSet lowercases text and splits it into a set of Unicode letter/number
// runs — same normalization style as significantTerms / normalizeClaimComponent,
// but keeping every token (no length floor) so short paraphrases still compare.
func tokenSet(text string) map[string]struct{} {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	set := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		set[f] = struct{}{}
	}
	return set
}

// jaccard is |A∩B| / |A∪B| over two token sets. Empty-vs-empty is 1.0 (identical
// emptiness); empty-vs-nonempty is 0.0.
func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1.0
	}
	if len(a) == 0 || len(b) == 0 {
		return 0.0
	}
	small, large := a, b
	if len(small) > len(large) {
		small, large = large, small
	}
	inter := 0
	for tok := range small {
		if _, ok := large[tok]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0.0
	}
	return float64(inter) / float64(union)
}

// lengthRatioAtLeast reports whether min(a,b)/max(a,b) >= threshold. Because
// Jaccard(A,B) <= min(|A|,|B|)/max(|A|,|B|), a false here means the pair cannot
// reach the threshold and the set intersection can be skipped.
func lengthRatioAtLeast(a, b int, threshold float64) bool {
	if a == 0 && b == 0 {
		return true
	}
	if a == 0 || b == 0 {
		return false
	}
	lo, hi := a, b
	if lo > hi {
		lo, hi = hi, lo
	}
	return float64(lo) >= threshold*float64(hi)
}
