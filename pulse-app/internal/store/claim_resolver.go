package store

import (
	"fmt"
	"strings"
)

// EnableClaimResolution wires the embedder + mode the server resolved from env.
// mode: "off" (default — claims not written), "shadow" (decide+log, never
// supersede), "on" (full). threshold defaults to 0.83 when <= 0.
func (s *Store) EnableClaimResolution(mode string, threshold float64, embed func(string) ([]float32, error)) {
	s.claimMode = strings.TrimSpace(mode)
	if threshold <= 0 {
		threshold = 0.83
	}
	s.claimThreshold = threshold
	s.claimEmbed = embed
}

// claimResolutionEnabled reports whether incoming claims should be resolved+written.
func (s *Store) claimResolutionEnabled() bool {
	return s.claimEmbed != nil && (s.claimMode == "shadow" || s.claimMode == "on")
}

// EnableCrossKey turns on cross-key resolution: a new claim can supersede an
// existing one phrased with a DIFFERENT subject string, matched by context
// embedding. Higher threshold than same-key (precision-first).
func (s *Store) EnableCrossKey(threshold float64) {
	s.claimXKey = true
	if threshold <= 0 {
		threshold = 0.90
	}
	s.claimXThresh = threshold
}

// crossKeyTarget finds an active in-scope assertion for the SAME attribute (same
// predicate, different object) phrased with a different subject, by context
// similarity. Returns its id+cosine if it clears the cross-key threshold, else 0.
func (s *Store) crossKeyTarget(a Assertion, vec []float32) (int64, float64) {
	cands, err := s.ActiveAssertionsInScope(a.Scope, 0)
	if err != nil || len(vec) == 0 {
		return 0, 0
	}
	inPred := normalizeClaimComponent(a.Predicate)
	inObj := normalizeObject(a.ObjectText)
	bestID := int64(0)
	bestCos := s.claimXThresh
	for _, c := range cands {
		if c.ClaimKey == a.ClaimKey || normalizeClaimComponent(c.Predicate) != inPred {
			continue
		}
		if normalizeObject(c.ObjectText) == inObj {
			continue
		}
		if cos := cosine(vec, c.CtxVec); cos >= bestCos {
			bestCos = cos
			bestID = c.ID
		}
	}
	if bestID == 0 {
		return 0, 0
	}
	return bestID, bestCos
}

// ResolveClaim resolves an incoming claim against existing claims for the same
// claim_key+scope and writes the result. Embedding happens here (outside any
// delta transaction). Precision-first: a failed embed or low similarity falls
// back to insert (keep both) — never a wrong supersede.
func (s *Store) ResolveClaim(a Assertion) (ClaimDecision, error) {
	a = withDerivedKey(a)
	if s.claimEmbed == nil {
		_, err := s.SaveAssertion(a)
		return ClaimDecision{Action: "insert", Reason: "resolution disabled"}, err
	}
	vec, err := s.claimEmbed(strings.TrimSpace(a.Subject + " " + a.Predicate + " " + a.ObjectText))
	if err != nil || len(vec) == 0 {
		a.CtxVec = nil
		_, e := s.SaveAssertion(a)
		return ClaimDecision{Action: "insert", Reason: "embed unavailable — safe insert"}, e
	}
	a.CtxVec = vec
	cands, err := s.CurrentAssertions(a.ClaimKey, a.Scope)
	if err != nil {
		return ClaimDecision{}, err
	}
	cc := make([]ClaimCandidate, 0, len(cands))
	for _, c := range cands {
		cc = append(cc, ClaimCandidate{
			ID: c.ID, Predicate: c.Predicate, ObjectNorm: c.ObjectText,
			ValidFrom: c.ValidFrom, Scope: c.Scope, Cosine: cosine(vec, c.CtxVec),
		})
	}
	d := DecideClaim(a.Predicate, a.ObjectText, a.ValidFrom, a.Scope, a.ChangeCue, cc, s.claimThreshold)
	// Cross-key fallback: no same-key update, but the same attribute may exist
	// under a differently-phrased subject. High-threshold, precision-first.
	if d.Action == "insert" && s.claimXKey {
		if tid, cos := s.crossKeyTarget(a, vec); tid != 0 {
			d = ClaimDecision{Action: "supersede", TargetID: tid, Cosine: cos, Reason: "cross-key match"}
		}
	}
	if s.claimMode == "shadow" && d.Action == "supersede" {
		d.Action = "insert"
		d.Reason = "shadow: would supersede, inserting instead"
	}
	switch d.Action {
	case "noop":
		return d, nil
	case "supersede":
		_, err := s.supersedeTargetTx(d.TargetID, a, d.Cosine)
		return d, err
	default: // insert (d.ValidTo set => historical backfill insert, not current)
		if d.ValidTo != "" {
			a.ValidTo = d.ValidTo
		}
		_, err := s.SaveAssertion(a)
		return d, err
	}
}

// supersedeTargetTx closes a specific (already vetted) target assertion and
// inserts the new active one, atomically. Unlike SupersedeAssertion it acts on
// one id chosen by DecideClaim, not on every same-key row.
func (s *Store) supersedeTargetTx(targetID int64, a Assertion, cos float64) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	newID, err := insertAssertion(tx, a)
	if err != nil {
		return 0, err
	}
	validTo := strings.TrimSpace(a.ValidFrom)
	if validTo == "" {
		validTo = nowRFC3339()
	}
	if _, err := tx.Exec(`
		UPDATE assertions
		   SET status = 'superseded', valid_to = COALESCE(valid_to, ?),
		       superseded_by = ?, resolution_cosine = ?
		 WHERE id = ? AND status = 'active'`, validTo, newID, cos, targetID); err != nil {
		return 0, fmt.Errorf("supersede target %d: %w", targetID, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return newID, nil
}

// ClaimDecision is the outcome of resolving an incoming claim against the claims
// already in the store for the same claim_key+scope. It is intentionally
// conservative: the ONLY memory-mutating action is "supersede", and it fires
// only when all guards agree the incoming claim is the SAME fact with a CHANGED
// value. When in doubt we "insert" (keep both) — a missed merge is harmless,
// a wrong one corrupts memory.
type ClaimDecision struct {
	Action   string  // "insert" | "supersede" | "noop"
	TargetID int64   // candidate acted on (supersede/noop)
	Cosine   float64 // similarity that justified the decision
	ValidTo  string  // for a historical (backfill) insert: close the valid interval so it's not current
	Reason   string
}

// ClaimCandidate is an existing active assertion considered for supersession,
// with the cosine of its context vector against the incoming claim's.
type ClaimCandidate struct {
	ID         int64
	Predicate  string
	ObjectNorm string
	ValidFrom  string // event-time of the candidate (for the chronology guard)
	Scope      Scope
	Cosine     float64
}

// DecideClaim is the pure precision core. Guards (ALL required to supersede):
//  1. predicate-match  — same normalized predicate.
//  2. same-scope       — never merge across scopes (personal/repo/...).
//  3. object-differs   — identical normalized object => duplicate => noop.
//  4. high-cosine      — context similarity >= threshold; below => keep both.
//  5. change-cue       — the source must SIGNAL a change (now/changed/switched…).
//     Without it the two are sibling facts of a multi-valued slot (speaks
//     Russian + English, allergies, devices) — keep both, never overwrite.
//  6. chronology       — a record with an OLDER valid_from than the current one
//     is history (out-of-order backfill); it must not replace the current value.
func DecideClaim(inPredicate, inObjectNorm, inValidFrom string, inScope Scope, changeCue bool, cands []ClaimCandidate, threshold float64) ClaimDecision {
	inPred := normalizeClaimComponent(inPredicate)
	inScopeN := inScope.normalized()
	best := -1
	bestCos := -1.0
	for i, c := range cands {
		if normalizeClaimComponent(c.Predicate) != inPred { // guard 1
			continue
		}
		if c.Scope.normalized() != inScopeN { // guard 2
			continue
		}
		if c.Cosine > bestCos {
			bestCos = c.Cosine
			best = i
		}
	}
	if best < 0 {
		return ClaimDecision{Action: "insert", Reason: "no same-predicate same-scope candidate"}
	}
	c := cands[best]
	if normalizeObject(c.ObjectNorm) == normalizeObject(inObjectNorm) { // guard 3
		return ClaimDecision{Action: "noop", TargetID: c.ID, Cosine: c.Cosine, Reason: "identical object — duplicate"}
	}
	if c.Cosine < threshold { // guard 4
		return ClaimDecision{Action: "insert", Cosine: c.Cosine, Reason: "cosine below threshold — keep both, never overwrite"}
	}
	if !changeCue { // guard 5 — multi-valued sibling protection
		return ClaimDecision{Action: "insert", Cosine: c.Cosine, Reason: "no change-cue — sibling fact, keep both"}
	}
	if inValidFrom != "" && c.ValidFrom != "" && inValidFrom < c.ValidFrom { // guard 6 — backfill protection
		// Slot the older fact into HISTORY (closed valid interval) so it is kept
		// but never becomes current; the current value is untouched.
		return ClaimDecision{Action: "insert", ValidTo: c.ValidFrom, Cosine: c.Cosine, Reason: "older valid_from — historical, not current"}
	}
	return ClaimDecision{Action: "supersede", TargetID: c.ID, Cosine: c.Cosine, Reason: "same slot, signalled change, newer"}
}

// normalizeObject mirrors normalizeClaimComponent so the object-differs guard is
// Cyrillic-aware and punctuation/case-insensitive, exactly like claim_key parts.
func normalizeObject(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r >= 'а' && r <= 'я':
			b.WriteRune(r)
		case r == 'ё':
			b.WriteRune('е')
		}
	}
	return b.String()
}

// cosine of two equal-length L2-or-not vectors (defensive: handles zero norm).
func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (sqrtf(na) * sqrtf(nb))
}

func sqrtf(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 24; i++ {
		z = (z + x/z) / 2
	}
	return z
}
