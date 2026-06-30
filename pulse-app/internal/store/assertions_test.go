package store

import (
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMakeClaimKeyStableAcrossPunctuationAndCase(t *testing.T) {
	a := MakeClaimKey("Effort level", "is")
	b := MakeClaimKey("  effort-level ", "Is")
	if a != b {
		t.Fatalf("claim_key not stable: %q vs %q", a, b)
	}
	if a == "|" {
		t.Fatal("claim_key collapsed to empty")
	}
}

func TestAssertionLifecycleSupersedeAndRetract(t *testing.T) {
	s := openTestStore(t)
	scope := Scope{Type: "project", ID: "garden"}
	key := MakeClaimKey("effort level", "is")

	// 1. Record the first belief: effort = medium.
	first := Assertion{
		Subject:    "effort level",
		Predicate:  "is",
		ObjectText: "medium",
		ValidFrom:  "2026-06-23T00:00:00Z",
		SystemFrom: "2026-06-23T00:00:00Z",
		Scope:      scope,
	}
	firstID, err := s.SaveAssertion(first)
	if err != nil {
		t.Fatalf("SaveAssertion: %v", err)
	}
	cur, err := s.CurrentAssertions(key, scope)
	if err != nil {
		t.Fatalf("CurrentAssertions: %v", err)
	}
	if len(cur) != 1 || cur[0].ObjectText != "medium" {
		t.Fatalf("expected current=[medium], got %+v", cur)
	}

	// 2. The world changed: effort = xhigh. The old belief is superseded, not
	//    deleted; only xhigh is current now.
	second := Assertion{
		Subject:    "effort level",
		Predicate:  "is",
		ObjectText: "xhigh",
		ValidFrom:  "2026-06-24T00:00:00Z",
		SystemFrom: "2026-06-24T00:00:00Z",
		Scope:      scope,
	}
	secondID, err := s.SupersedeAssertion(second)
	if err != nil {
		t.Fatalf("SupersedeAssertion: %v", err)
	}
	cur, err = s.CurrentAssertions(key, scope)
	if err != nil {
		t.Fatalf("CurrentAssertions after supersede: %v", err)
	}
	if len(cur) != 1 || cur[0].ObjectText != "xhigh" {
		t.Fatalf("expected current=[xhigh], got %+v", cur)
	}

	// The old row still exists, marked superseded, valid interval closed, linked.
	var status, validTo string
	var supBy *int64
	if err := s.DB().QueryRow(
		`SELECT status, COALESCE(valid_to,''), superseded_by FROM assertions WHERE id=?`, firstID,
	).Scan(&status, &validTo, &supBy); err != nil {
		t.Fatalf("read superseded row: %v", err)
	}
	if status != "superseded" {
		t.Fatalf("old assertion status = %q, want superseded", status)
	}
	if validTo == "" {
		t.Fatal("old assertion valid_to should be closed")
	}
	if supBy == nil || *supBy != secondID {
		t.Fatalf("old assertion superseded_by = %v, want %d", supBy, secondID)
	}

	// 3. We recorded it wrong: retract xhigh. Now nothing is currently believed.
	if err := s.RetractAssertion(secondID, "2026-06-24T01:00:00Z"); err != nil {
		t.Fatalf("RetractAssertion: %v", err)
	}
	cur, err = s.CurrentAssertions(key, scope)
	if err != nil {
		t.Fatalf("CurrentAssertions after retract: %v", err)
	}
	if len(cur) != 0 {
		t.Fatalf("expected no current belief after retract, got %+v", cur)
	}
}

func TestAssertionScopeIsolation(t *testing.T) {
	s := openTestStore(t)
	key := MakeClaimKey("home city", "is")

	personal := Assertion{Subject: "home city", Predicate: "is", ObjectText: "Bali", Scope: Scope{Type: "personal"}}
	repo := Assertion{Subject: "home city", Predicate: "is", ObjectText: "see-readme", Scope: Scope{Type: "repo", ID: "pulse"}}
	if _, err := s.SaveAssertion(personal); err != nil {
		t.Fatalf("save personal: %v", err)
	}
	if _, err := s.SaveAssertion(repo); err != nil {
		t.Fatalf("save repo: %v", err)
	}

	cur, err := s.CurrentAssertions(key, Scope{Type: "personal"})
	if err != nil {
		t.Fatalf("CurrentAssertions personal: %v", err)
	}
	if len(cur) != 1 || cur[0].ObjectText != "Bali" {
		t.Fatalf("scope bled: personal query returned %+v", cur)
	}
}
