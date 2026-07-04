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

// Part (b): repeated confirmation of the identical claim is a corroboration —
// it bumps mention_count on the single active row, never inserts a new row and
// never supersedes. Embedder-free (UpsertAssertion is the exact-key path).
func TestUpsertAssertionCorroborationBumpsMentionCount(t *testing.T) {
	s := openTestStore(t)
	scope := Scope{Type: "project", ID: "garden"}
	key := MakeClaimKey("effort level", "is")

	var firstID int64
	for i := 1; i <= 3; i++ {
		a := Assertion{
			Subject:    "effort level",
			Predicate:  "is",
			ObjectText: "high",
			SystemFrom: "2026-07-04T0" + string(rune('0'+i)) + ":00:00Z",
			Scope:      scope,
		}
		id, inserted, err := s.UpsertAssertion(a)
		if err != nil {
			t.Fatalf("UpsertAssertion #%d: %v", i, err)
		}
		if i == 1 {
			firstID = id
			if !inserted {
				t.Fatalf("first confirmation should insert (inserted=true)")
			}
		} else {
			if inserted {
				t.Fatalf("confirmation #%d must not insert a new row", i)
			}
			if id != firstID {
				t.Fatalf("confirmation #%d hit id=%d, want the original %d", i, id, firstID)
			}
		}
	}

	cur, err := s.CurrentAssertions(key, scope)
	if err != nil {
		t.Fatalf("CurrentAssertions: %v", err)
	}
	if len(cur) != 1 {
		t.Fatalf("expected exactly 1 active row after 3 confirmations, got %d (%+v)", len(cur), cur)
	}
	if cur[0].MentionCount != 3 {
		t.Fatalf("mention_count = %d, want 3", cur[0].MentionCount)
	}
	if cur[0].ObjectText != "high" || cur[0].Status != "active" {
		t.Fatalf("current row unexpected: %+v", cur[0])
	}
	if cur[0].LastMentionedAt == "" {
		t.Fatal("last_mentioned_at should be stamped after a corroboration")
	}

	// No superseded rows were created.
	var total int
	if err := s.DB().QueryRow("SELECT COUNT(*) FROM assertions").Scan(&total); err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected a single row total (no supersede), got %d", total)
	}
}

// Part (b) regression: a changed object is NOT a corroboration — it supersedes,
// and the fresh active row starts back at mention_count = 1.
func TestUpsertAssertionChangeSupersedesAndResetsCount(t *testing.T) {
	s := openTestStore(t)
	scope := Scope{Type: "project", ID: "garden"}
	key := MakeClaimKey("effort level", "is")

	// Confirm "high" twice (mention_count -> 2), then change to "low".
	for i := 0; i < 2; i++ {
		if _, _, err := s.UpsertAssertion(Assertion{Subject: "effort level", Predicate: "is", ObjectText: "high", Scope: scope}); err != nil {
			t.Fatalf("confirm high #%d: %v", i, err)
		}
	}
	_, inserted, err := s.UpsertAssertion(Assertion{
		Subject: "effort level", Predicate: "is", ObjectText: "low",
		ValidFrom: "2026-07-04T05:00:00Z", SystemFrom: "2026-07-04T05:00:00Z", Scope: scope,
	})
	if err != nil {
		t.Fatalf("change to low: %v", err)
	}
	if !inserted {
		t.Fatal("a changed object must insert (supersede path), inserted=true")
	}

	cur, err := s.CurrentAssertions(key, scope)
	if err != nil {
		t.Fatalf("CurrentAssertions: %v", err)
	}
	if len(cur) != 1 || cur[0].ObjectText != "low" {
		t.Fatalf("expected current=[low], got %+v", cur)
	}
	if cur[0].MentionCount != 1 {
		t.Fatalf("a change is not corroboration: new row mention_count = %d, want 1", cur[0].MentionCount)
	}

	// The prior "high" row is superseded, not deleted, and keeps its count of 2.
	var status string
	var mentions int64
	if err := s.DB().QueryRow(
		"SELECT status, mention_count FROM assertions WHERE object_text='high'",
	).Scan(&status, &mentions); err != nil {
		t.Fatalf("read superseded high row: %v", err)
	}
	if status != "superseded" {
		t.Fatalf("prior high row status = %q, want superseded", status)
	}
	if mentions != 2 {
		t.Fatalf("prior high row should retain mention_count=2, got %d", mentions)
	}
}
