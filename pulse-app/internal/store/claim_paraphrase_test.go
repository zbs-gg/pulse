package store

import (
	"strings"
	"testing"
)

// paraStubEmbed maps phrasing variants of the same CONCEPT onto the same
// dimension ("residence"/"home"/"lives" are one concept), so a reworded
// restatement of a claim lands at very high cosine (~0.95) while a distinct
// attribute (coffee) lands low (~0.47) — deterministic, no model needed.
func paraStubEmbed(text string) ([]float32, error) {
	dims := map[string]int{
		"alex": 1, "residence": 0, "home": 0, "lives": 0, "coffee": 4, // concepts (w=3)
		"lisbon": 2, "bali": 3, "cortado": 5, // objects (w=1)
	}
	v := make([]float32, 6)
	lt := strings.ToLower(text)
	for tok, d := range dims {
		if strings.Contains(lt, tok) {
			if tok == "lisbon" || tok == "bali" || tok == "cortado" {
				v[d] = 1
			} else {
				v[d] = 3
			}
		}
	}
	return v, nil
}

// The three claim phrasings used across these tests. A and B share the
// residence concept but produce DIFFERENT claim_keys (subject+predicate both
// reworded), so exact and cross-key resolution can never match them.
func residenceClaim(obj string, cue bool) Assertion {
	return Assertion{Subject: "alex residence", Predicate: "lives in", ObjectText: obj, Scope: Scope{Type: "personal"}, ChangeCue: cue}
}

func rewordedResidenceClaim(obj string, cue bool) Assertion {
	return Assertion{Subject: "alex home city", Predicate: "is", ObjectText: obj, Scope: Scope{Type: "personal"}, ChangeCue: cue}
}

// (a) Flag off => exact-today behavior: the reworded restatement gets its own
// claim_key, is inserted as a sibling, and nothing is superseded or bumped.
func TestParaphrase_FlagOff_ExactTodayBehavior(t *testing.T) {
	s := openTestStore(t)
	s.EnableClaimResolution("on", 0.83, paraStubEmbed) // paraphrase NOT enabled
	s.ResolveClaim(residenceClaim("lisbon", false))
	d, err := s.ResolveClaim(rewordedResidenceClaim("bali", true))
	if err != nil || d.Action != "insert" {
		t.Fatalf("flag off: reworded claim must plain-insert, got action=%s err=%v", d.Action, err)
	}
	act, _ := s.ActiveAssertionsInScope(Scope{Type: "personal"}, 0)
	if len(act) != 2 {
		t.Fatalf("flag off: both phrasings must stay active, got %d (%+v)", len(act), act)
	}
	for _, a := range act {
		if a.MentionCount != 1 {
			t.Fatalf("flag off: mention_count must stay 1, got %d", a.MentionCount)
		}
	}
}

// (b) Flag on + paraphrase with a changed value => supersession fires across
// the claim_key boundary; exactly one residence claim stays current.
func TestParaphrase_RewordedRestatementSupersedes(t *testing.T) {
	s := openTestStore(t)
	s.EnableClaimResolution("on", 0.83, paraStubEmbed)
	s.EnableParaphraseClaims(0.90)
	s.ResolveClaim(residenceClaim("lisbon", false))
	d, err := s.ResolveClaim(rewordedResidenceClaim("bali", true))
	if err != nil {
		t.Fatalf("reworded claim: %v", err)
	}
	if d.Action != "supersede" || !strings.Contains(d.Reason, "paraphrase") {
		t.Fatalf("reworded restatement must paraphrase-supersede, got action=%s cos=%.3f (%s)", d.Action, d.Cosine, d.Reason)
	}
	act, _ := s.ActiveAssertionsInScope(Scope{Type: "personal"}, 0)
	if len(act) != 1 || act[0].ObjectText != "bali" {
		t.Fatalf("exactly [bali] should stay active after paraphrase supersede, got %+v", act)
	}
	var status string
	var supBy int64
	if err := s.DB().QueryRow(`SELECT status, superseded_by FROM assertions WHERE object_text='lisbon'`).Scan(&status, &supBy); err != nil {
		t.Fatalf("old row lookup: %v", err)
	}
	if status != "superseded" || supBy != act[0].ID {
		t.Fatalf("old claim must be superseded by the new row, got status=%s superseded_by=%d", status, supBy)
	}
}

// (c) Flag on + genuinely distinct claims (low cosine) => NOT merged.
func TestParaphrase_DistinctClaimsNotMerged(t *testing.T) {
	s := openTestStore(t)
	s.EnableClaimResolution("on", 0.83, paraStubEmbed)
	s.EnableParaphraseClaims(0.90)
	s.ResolveClaim(residenceClaim("lisbon", false))
	d, _ := s.ResolveClaim(Assertion{Subject: "alex coffee", Predicate: "preference", ObjectText: "cortado", Scope: Scope{Type: "personal"}, ChangeCue: true})
	if d.Action != "insert" {
		t.Fatalf("distinct claim must insert, got action=%s cos=%.3f (%s)", d.Action, d.Cosine, d.Reason)
	}
	act, _ := s.ActiveAssertionsInScope(Scope{Type: "personal"}, 0)
	if len(act) != 2 {
		t.Fatalf("distinct claims must both stay active, got %d (%+v)", len(act), act)
	}
}

// Flag on + paraphrase with the SAME value => corroboration through the
// existing mention_count path: no new row, counter bumps.
func TestParaphrase_SameObjectCorroborates(t *testing.T) {
	s := openTestStore(t)
	s.EnableClaimResolution("on", 0.83, paraStubEmbed)
	s.EnableParaphraseClaims(0.90)
	s.ResolveClaim(residenceClaim("lisbon", false))
	d, err := s.ResolveClaim(rewordedResidenceClaim("lisbon", false)) // no cue needed to corroborate
	if err != nil || d.Action != "corroborate" {
		t.Fatalf("reworded same-value claim must corroborate, got action=%s err=%v (%s)", d.Action, err, d.Reason)
	}
	var n int
	s.DB().QueryRow(`SELECT COUNT(*) FROM assertions`).Scan(&n)
	if n != 1 {
		t.Fatalf("corroboration must not insert a row, got %d rows", n)
	}
	cur, _ := s.CurrentAssertions(MakeClaimKey("alex residence", "lives in"), Scope{Type: "personal"})
	if len(cur) != 1 || cur[0].MentionCount != 2 || cur[0].LastMentionedAt == "" {
		t.Fatalf("mention_count must bump to 2 with a timestamp, got %+v", cur)
	}
}

// No change-cue + changed value => sibling protection holds even at high
// cosine (same invariant as DecideClaim guard 5 and the cross-key path).
func TestParaphrase_NoChangeCueKeepsBoth(t *testing.T) {
	s := openTestStore(t)
	s.EnableClaimResolution("on", 0.83, paraStubEmbed)
	s.EnableParaphraseClaims(0.90)
	s.ResolveClaim(residenceClaim("lisbon", false))
	d, _ := s.ResolveClaim(rewordedResidenceClaim("bali", false)) // no ChangeCue
	if d.Action != "insert" {
		t.Fatalf("no change-cue must keep both, got action=%s (%s)", d.Action, d.Reason)
	}
	act, _ := s.ActiveAssertionsInScope(Scope{Type: "personal"}, 0)
	if len(act) != 2 {
		t.Fatalf("both claims must stay active without a cue, got %d", len(act))
	}
}

// Shadow mode: a proposed paraphrase corroboration is logged but NOT applied —
// behavior stays byte-identical to today (plain insert, counter untouched).
func TestParaphrase_ShadowSuppressesCorroborate(t *testing.T) {
	s := openTestStore(t)
	s.EnableClaimResolution("shadow", 0.83, paraStubEmbed)
	s.EnableParaphraseClaims(0.90)
	s.ResolveClaim(residenceClaim("lisbon", false))
	d, _ := s.ResolveClaim(rewordedResidenceClaim("lisbon", false))
	if d.Action != "insert" {
		t.Fatalf("shadow must suppress corroborate to insert, got %s (%s)", d.Action, d.Reason)
	}
	cur, _ := s.CurrentAssertions(MakeClaimKey("alex residence", "lives in"), Scope{Type: "personal"})
	if len(cur) != 1 || cur[0].MentionCount != 1 {
		t.Fatalf("shadow must not bump mention_count, got %+v", cur)
	}
	var applied int
	if err := s.DB().QueryRow(`SELECT applied FROM claim_decisions WHERE action='corroborate' ORDER BY id DESC LIMIT 1`).Scan(&applied); err != nil {
		t.Fatalf("shadow corroborate must be recorded in the ledger: %v", err)
	}
	if applied != 0 {
		t.Fatalf("shadow corroborate must be logged as NOT applied, got applied=%d", applied)
	}
}

// Embedder absent: even with the flag on, resolution falls back to plain
// inserts — the paraphrase path is a strict no-op.
func TestParaphrase_NoEmbedderIsNoOp(t *testing.T) {
	s := openTestStore(t)
	s.EnableParaphraseClaims(0.90) // flag on, but no embedder wired
	s.ResolveClaim(residenceClaim("lisbon", false))
	d, err := s.ResolveClaim(rewordedResidenceClaim("bali", true))
	if err != nil || d.Action != "insert" {
		t.Fatalf("no embedder must plain-insert, got action=%s err=%v", d.Action, err)
	}
	var n int
	s.DB().QueryRow(`SELECT COUNT(*) FROM assertions WHERE status='active'`).Scan(&n)
	if n != 2 {
		t.Fatalf("no embedder must leave both rows active, got %d", n)
	}
}

// End-to-end through the ingest path: a paraphrase re-confirmation surfaces as
// claims_corroborated in the semantic-delta result.
func TestSaveSemanticDelta_ParaphraseCorroborationCount(t *testing.T) {
	s := openTestStore(t)
	s.EnableClaimResolution("on", 0.83, paraStubEmbed)
	s.EnableParaphraseClaims(0.90)
	mk := func(subject, predicate, obj string) SemanticDelta {
		return SemanticDelta{
			Schema: SemanticDeltaSchema,
			Source: SemanticDeltaSource{Host: "claude-code", ConversationScope: "project_context", Timestamp: "2026-05-01T09:00:00Z"},
			Events: []SemanticEvent{{
				ClientID: "ev:1", Title: "residence note", Summary: "Where Alex lives.",
				Confidence: 0.9, PrivacyTier: "normal", OccurredAt: "2026-05-01T09:00:00Z",
				Claims: []SemanticClaim{{Subject: subject, Predicate: predicate, Object: obj}},
			}},
		}
	}
	if _, err := s.SaveSemanticDelta(mk("alex residence", "lives in", "lisbon")); err != nil {
		t.Fatalf("delta A: %v", err)
	}
	res, err := s.SaveSemanticDelta(mk("alex home city", "is", "lisbon"))
	if err != nil {
		t.Fatalf("delta B: %v", err)
	}
	if res.ClaimsCorroborated != 1 || res.ClaimsInserted != 0 {
		t.Fatalf("reworded re-confirmation must count as corroborated, got %+v", res)
	}
}
