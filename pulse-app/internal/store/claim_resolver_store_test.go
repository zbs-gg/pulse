package store

import (
	"strings"
	"testing"
)

// stubEmbed: crude bag-of-keywords where subject/predicate-ish tokens dominate,
// so a value-update of the SAME attribute lands high-cosine and DISTINCT
// attributes land low — mimicking a real sentence embedder closely enough to
// exercise the resolver deterministically (no model needed).
func stubEmbed(text string) ([]float32, error) {
	dims := map[string]int{
		"reasoning": 0, "effort": 1, "coffee": 2, "code": 3, "editor": 4,
		"home": 5, "base": 6, "is": 7, "preference": 8, "time": 9, // 0..9 = subject/predicate
		"medium": 10, "xhigh": 11, "cortado": 12, "9am": 13, "vscode": 14, "neovim": 15, // 10.. = objects
	}
	v := make([]float32, 16)
	lt := strings.ToLower(text)
	for tok, d := range dims {
		if strings.Contains(lt, tok) {
			if d <= 9 {
				v[d] = 3 // subject/predicate tokens dominate the vector
			} else {
				v[d] = 1
			}
		}
	}
	return v, nil
}

func TestResolveClaim_TrueUpdateSupersedes(t *testing.T) {
	s := openTestStore(t)
	s.EnableClaimResolution("on", 0.83, stubEmbed)
	personal := Scope{Type: "personal"}
	if d, err := s.ResolveClaim(Assertion{Subject: "alex reasoning effort", Predicate: "is", ObjectText: "medium", Scope: personal}); err != nil || d.Action != "insert" {
		t.Fatalf("first effort claim: action=%s err=%v (want insert)", d.Action, err)
	}
	d, err := s.ResolveClaim(Assertion{Subject: "alex reasoning effort", Predicate: "is", ObjectText: "xhigh", Scope: personal})
	if err != nil {
		t.Fatalf("second effort claim err: %v", err)
	}
	if d.Action != "supersede" {
		t.Fatalf("effort medium->xhigh: action=%s cos=%.3f (%s), want supersede", d.Action, d.Cosine, d.Reason)
	}
	cur, _ := s.CurrentAssertions(MakeClaimKey("alex reasoning effort", "is"), personal)
	if len(cur) != 1 || cur[0].ObjectText != "xhigh" {
		t.Fatalf("current effort should be exactly [xhigh], got %+v", cur)
	}
}

func TestResolveClaim_CoffeePreferenceVsTime_KeepsBoth(t *testing.T) {
	s := openTestStore(t)
	s.EnableClaimResolution("on", 0.83, stubEmbed)
	personal := Scope{Type: "personal"}
	s.ResolveClaim(Assertion{Subject: "alex coffee", Predicate: "preference", ObjectText: "cortado", Scope: personal})
	s.ResolveClaim(Assertion{Subject: "alex coffee", Predicate: "time", ObjectText: "9am", Scope: personal})
	// Different predicate => different claim_key => never merged; cortado stays current.
	pref, _ := s.CurrentAssertions(MakeClaimKey("alex coffee", "preference"), personal)
	tm, _ := s.CurrentAssertions(MakeClaimKey("alex coffee", "time"), personal)
	if len(pref) != 1 || pref[0].ObjectText != "cortado" {
		t.Fatalf("coffee preference should stay [cortado], got %+v", pref)
	}
	if len(tm) != 1 || tm[0].ObjectText != "9am" {
		t.Fatalf("coffee time should be [9am], got %+v", tm)
	}
}

func TestResolveClaim_CrossKeyMergesDriftNotDifferentAttribute(t *testing.T) {
	s := openTestStore(t)
	s.EnableClaimResolution("on", 0.83, stubEmbed)
	s.EnableCrossKey(0.75) // stub gives ~0.82 for same attribute, ~0.40 across attributes
	personal := Scope{Type: "personal"}
	// Same attribute, DIFFERENT subject phrasing (drift) — should merge cross-key.
	s.ResolveClaim(Assertion{Subject: "alex home base", Predicate: "is", ObjectText: "bali", Scope: personal})
	d, _ := s.ResolveClaim(Assertion{Subject: "alex home", Predicate: "is", ObjectText: "lisbon", Scope: personal})
	if d.Action != "supersede" {
		t.Fatalf("home drift (home base -> home) should cross-key merge, got %s (%s, cos %.2f)", d.Action, d.Reason, d.Cosine)
	}
	// A different attribute must NOT be swept in.
	s.ResolveClaim(Assertion{Subject: "alex coffee", Predicate: "preference", ObjectText: "cortado", Scope: personal})
	cur, _ := s.CurrentAssertions(MakeClaimKey("alex coffee", "preference"), personal)
	if len(cur) != 1 || cur[0].ObjectText != "cortado" {
		t.Fatalf("coffee preference must be untouched by cross-key, got %+v", cur)
	}
	// Home: only the current value remains active across both keys.
	act, _ := s.ActiveAssertionsInScope(personal, 0)
	homeActive := 0
	for _, a := range act {
		if a.ClaimKey == MakeClaimKey("alex home base", "is") || a.ClaimKey == MakeClaimKey("alex home", "is") {
			homeActive++
		}
	}
	if homeActive != 1 {
		t.Fatalf("exactly one home assertion should stay active after cross-key merge, got %d", homeActive)
	}
}

func TestResolveClaim_ShadowNeverSupersedes(t *testing.T) {
	s := openTestStore(t)
	s.EnableClaimResolution("shadow", 0.83, stubEmbed)
	personal := Scope{Type: "personal"}
	s.ResolveClaim(Assertion{Subject: "alex home base", Predicate: "is", ObjectText: "bali", Scope: personal})
	d, _ := s.ResolveClaim(Assertion{Subject: "alex home base", Predicate: "is", ObjectText: "lisbon", Scope: personal})
	if d.Action == "supersede" {
		t.Fatalf("shadow mode must never supersede, got %+v", d)
	}
}

func TestSaveSemanticDelta_ClaimResolutionOff_WritesNoAssertions(t *testing.T) {
	s := openTestStore(t) // EnableClaimResolution NOT called => off
	delta := SemanticDelta{
		Schema: SemanticDeltaSchema,
		Source: SemanticDeltaSource{Host: "claude-code", ConversationScope: "project_context", Timestamp: "2026-05-01T09:00:00Z"},
		Events: []SemanticEvent{{
			ClientID: "ev:1", Title: "effort note", Summary: "Alex effort is medium.",
			Confidence: 0.9, PrivacyTier: "normal", OccurredAt: "2026-05-01T09:00:00Z",
			Claims: []SemanticClaim{{Subject: "alex reasoning effort", Predicate: "is", Object: "medium"}},
		}},
	}
	if _, err := s.SaveSemanticDelta(delta); err != nil {
		t.Fatalf("SaveSemanticDelta: %v", err)
	}
	var n int
	if err := s.DB().QueryRow("SELECT COUNT(*) FROM assertions").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("claim resolution off must write 0 assertions, got %d", n)
	}
}
