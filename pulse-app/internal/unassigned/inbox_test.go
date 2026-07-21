package unassigned

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

func writeInboxFixture(t *testing.T) (string, inboxItem) {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, ".pulse", "supervisor")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	candidate := candidateEnvelope{
		Kind: store.PrivateMemoryCandidateCapsule,
		Capsule: store.MemoryCapsule{
			Schema: store.MemoryCapsuleSchema,
			Source: store.CapsuleSource{
				Host: "codex", ConversationScope: "current_turn", Timestamp: "2026-07-17T10:00:00Z",
			},
			Items: []store.MemoryCapsuleItem{{
				Kind: "decision", RedactedSummary: "Assign this to the exact current project.", Confidence: 1,
				EvidenceHint: "user_confirmed", PrivacyTier: "normal", Retention: "project", Tags: []string{"pulse"},
			}},
			RawInputIncluded: false,
		},
	}
	rawCandidate, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := candidateDigest(rawCandidate)
	if err != nil {
		t.Fatal(err)
	}
	item := inboxItem{
		Schema: candidateSchema, ItemID: "unassigned_" + digest[:32], ContentDigest: digest,
		CreatedAt: "2026-07-17T10:00:00Z", Host: "codex", IdempotencyKey: "request_01", Candidate: rawCandidate,
	}
	value := inboxFile{
		Schema: inboxSchema, Items: []inboxItem{item}, Receipts: []receipt{{
			ReceiptID: "unassigned_receipt_" + strings.Repeat("a", 32), ItemID: item.ItemID,
			ContentDigest: digest, Action: "stage", Status: "staged", CreatedAt: item.CreatedAt,
		}},
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "unassigned-inbox.json")
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, item
}

func testDestination(digest string) Destination {
	return Destination{BindingDigest: digest, RepositoryID: "repository_pulse", StoreID: "store_personal_test"}
}

func TestListAssignAndRetryAreDigestBound(t *testing.T) {
	path, item := writeInboxFixture(t)
	cards, err := List(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[0].ItemID != item.ItemID || cards[0].ContentDigest != item.ContentDigest ||
		cards[0].Summary != "Assign this to the exact current project." {
		t.Fatalf("unexpected cards: %+v", cards)
	}
	calls := 0
	assign := func(candidate store.PrivateMemoryCandidate) error {
		calls++
		if candidate.Capsule == nil || candidate.Capsule.Items[0].Kind != "decision" {
			t.Fatalf("unexpected candidate: %+v", candidate)
		}
		return nil
	}
	now := time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC)
	destination := testDestination(strings.Repeat("a", 64))
	if err := Assign(path, item.ItemID, item.ContentDigest, destination, now, assign); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("assignment calls=%d, want 1", calls)
	}
	if cards, err = List(path); err != nil || len(cards) != 0 {
		t.Fatalf("cards after assignment=%+v err=%v", cards, err)
	}
	if err := Assign(path, item.ItemID, item.ContentDigest, destination, now.Add(time.Second), assign); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("idempotent retry called assignment again: %d", calls)
	}
}

func TestAssignmentIntentSurvivesFailureAndRejectsAnotherProject(t *testing.T) {
	path, item := writeInboxFixture(t)
	now := time.Date(2026, 7, 17, 11, 30, 0, 0, time.UTC)
	alpha := Destination{
		BindingDigest: strings.Repeat("a", 64), RepositoryID: "repository_alpha", StoreID: "store_personal_alpha",
	}
	beta := Destination{
		BindingDigest: strings.Repeat("b", 64), RepositoryID: "repository_beta", StoreID: "store_personal_beta",
	}
	rejected := errors.New("destination rejected candidate")
	if err := Assign(path, item.ItemID, item.ContentDigest, alpha, now, func(store.PrivateMemoryCandidate) error {
		return rejected
	}); !errors.Is(err, rejected) {
		t.Fatalf("first assignment error=%v", err)
	}
	if cards, err := List(path); err != nil || len(cards) != 1 {
		t.Fatalf("rejected assignment removed card: cards=%+v err=%v", cards, err)
	}
	betaCalls := 0
	if err := Assign(path, item.ItemID, item.ContentDigest, beta, now.Add(time.Second), func(store.PrivateMemoryCandidate) error {
		betaCalls++
		return nil
	}); !errors.Is(err, ErrDestinationConflict) {
		t.Fatalf("cross-project retry error=%v, want %v", err, ErrDestinationConflict)
	}
	if betaCalls != 0 {
		t.Fatalf("cross-project retry reached destination callback: %d", betaCalls)
	}
	if err := Assign(path, item.ItemID, item.ContentDigest, alpha, now.Add(2*time.Second), func(store.PrivateMemoryCandidate) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Assign(path, item.ItemID, item.ContentDigest, beta, now.Add(3*time.Second), func(store.PrivateMemoryCandidate) error {
		return nil
	}); !errors.Is(err, ErrDestinationConflict) {
		t.Fatalf("terminal cross-project retry error=%v, want %v", err, ErrDestinationConflict)
	}
}

func TestAssignmentIntentFencesAnotherProjectAfterDestinationCommit(t *testing.T) {
	path, item := writeInboxFixture(t)
	alpha, err := store.OpenVault(filepath.Join(t.TempDir(), "alpha.db"), store.StoreKindPersonal, "store_personal_alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer alpha.Close()
	alphaBinding := strings.Repeat("a", 64)
	if err := alpha.ConfigureProductRuntimeAuthority(alphaBinding, 1, 1); err != nil {
		t.Fatal(err)
	}
	alphaDestination := Destination{
		BindingDigest: alphaBinding, RepositoryID: "repository_alpha", StoreID: alpha.StoreID(),
	}
	betaDestination := Destination{
		BindingDigest: strings.Repeat("b", 64), RepositoryID: "repository_beta", StoreID: "store_personal_beta",
	}
	now := time.Date(2026, 7, 17, 11, 45, 0, 0, time.UTC)
	interrupted := errors.New("simulated crash after destination commit")
	if err := Assign(path, item.ItemID, item.ContentDigest, alphaDestination, now, func(candidate store.PrivateMemoryCandidate) error {
		result, err := alpha.PrepareUnassignedMemoryCapsuleWithInvocation(
			*candidate.Capsule, item.ContentDigest, now, time.Second,
		)
		if err != nil || result.Status != store.TurnFinalizedCandidates {
			t.Fatalf("Alpha prepare result=%+v err=%v", result, err)
		}
		return interrupted
	}); !errors.Is(err, interrupted) {
		t.Fatalf("interrupted assignment error=%v", err)
	}
	if pending, err := alpha.ListPendingMemoryTrayCandidates(10); err != nil || len(pending) != 1 {
		t.Fatalf("Alpha committed pending=%+v err=%v", pending, err)
	}
	if err := Assign(path, item.ItemID, item.ContentDigest, betaDestination, now.Add(time.Second), func(store.PrivateMemoryCandidate) error {
		t.Fatal("Beta callback ran after Alpha destination commit")
		return nil
	}); !errors.Is(err, ErrDestinationConflict) {
		t.Fatalf("Beta crash-window retry error=%v, want %v", err, ErrDestinationConflict)
	}
	if err := Assign(path, item.ItemID, item.ContentDigest, alphaDestination, now.Add(2*time.Second), func(candidate store.PrivateMemoryCandidate) error {
		result, err := alpha.PrepareUnassignedMemoryCapsuleWithInvocation(
			*candidate.Capsule, item.ContentDigest, now, time.Second,
		)
		if err != nil || result.Status != store.TurnFinalizedCandidates {
			t.Fatalf("Alpha retry result=%+v err=%v", result, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if pending, err := alpha.ListPendingMemoryTrayCandidates(10); err != nil || len(pending) != 1 {
		t.Fatalf("Alpha retry duplicated pending candidates=%+v err=%v", pending, err)
	}
}

func TestDigestDriftAndUnsafeFilesFailClosed(t *testing.T) {
	path, item := writeInboxFixture(t)
	if err := Assign(path, item.ItemID, strings.Repeat("b", 64), testDestination(strings.Repeat("a", 64)), time.Now(), func(store.PrivateMemoryCandidate) error {
		return nil
	}); err == nil {
		t.Fatal("digest drift unexpectedly assigned")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := List(path); !errors.Is(err, errUnsafe) {
		t.Fatalf("unsafe file error=%v, want %v", err, errUnsafe)
	}
}

func TestNonContentionLockFailureReturnsWithoutRetryLoop(t *testing.T) {
	path, item := writeInboxFixture(t)
	directory := filepath.Dir(path)
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
	started := time.Now()
	err := Assign(
		path, item.ItemID, item.ContentDigest, testDestination(strings.Repeat("a", 64)), time.Now(),
		func(store.PrivateMemoryCandidate) error { return nil },
	)
	if !errors.Is(err, errUnsafe) {
		t.Fatalf("lock creation error=%v, want %v", err, errUnsafe)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("non-contention lock failure retried for %s", elapsed)
	}
}

func TestAssignmentInfluencesOnlyTheChosenProjectVault(t *testing.T) {
	path, item := writeInboxFixture(t)
	alpha, err := store.OpenVault(filepath.Join(t.TempDir(), "alpha.db"), store.StoreKindPersonal, "store_personal_alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer alpha.Close()
	beta, err := store.OpenVault(filepath.Join(t.TempDir(), "beta.db"), store.StoreKindPersonal, "store_personal_beta")
	if err != nil {
		t.Fatal(err)
	}
	defer beta.Close()
	alphaBinding := strings.Repeat("a", 64)
	betaBinding := strings.Repeat("b", 64)
	if err := alpha.ConfigureProductRuntimeAuthority(alphaBinding, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := beta.ConfigureProductRuntimeAuthority(betaBinding, 1, 1); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	if err := Assign(path, item.ItemID, item.ContentDigest, Destination{
		BindingDigest: alphaBinding, RepositoryID: "repository_alpha", StoreID: alpha.StoreID(),
	}, now, func(candidate store.PrivateMemoryCandidate) error {
		_, err := alpha.PrepareUnassignedMemoryCapsuleWithInvocation(
			*candidate.Capsule, item.ContentDigest, now, time.Second,
		)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := alpha.ListPendingMemoryTrayCandidates(10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("Alpha pending=%+v err=%v", pending, err)
	}
	if betaPending, err := beta.ListPendingMemoryTrayCandidates(10); err != nil || len(betaPending) != 0 {
		t.Fatalf("Beta observed Alpha pending candidate=%+v err=%v", betaPending, err)
	}
	if _, err := alpha.PresentMemoryTrayCandidate(store.MemoryPresentationRequest{
		CandidateID: pending[0].CandidateID, CandidateVersion: pending[0].Version,
		ContentDigest: pending[0].ContentDigest, BindingDigest: alphaBinding,
		TrustedSurfaceKind: "memory_home", TrustedSurfaceInstance: "surface_alpha",
	}, now, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := alpha.CommitMemoryTrayCandidate(pending[0].CandidateID, pending[0].Version, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	alphaRecall, err := alpha.RecallMemory(store.RecallMemoryQuery{Query: "exact current project", Limit: 10})
	if err != nil || len(alphaRecall) != 1 {
		t.Fatalf("Alpha recall=%+v err=%v", alphaRecall, err)
	}
	betaRecall, err := beta.RecallMemory(store.RecallMemoryQuery{Query: "exact current project", Limit: 10})
	if err != nil || len(betaRecall) != 0 {
		t.Fatalf("Beta received Alpha memory=%+v err=%v", betaRecall, err)
	}
	betaCapsule := store.MemoryCapsule{
		Schema: store.MemoryCapsuleSchema,
		Source: store.CapsuleSource{Host: "cursor", ConversationScope: "current_turn", Timestamp: "2026-07-17T12:05:00Z"},
		Items: []store.MemoryCapsuleItem{{
			Kind: "project_state", RedactedSummary: "Beta belongs only to the Cursor-bound project.", Confidence: 1,
			EvidenceHint: "current_turn", PrivacyTier: "normal", Retention: "project",
		}},
	}
	betaPrepared, err := beta.PrepareUnassignedMemoryCapsuleWithInvocation(
		betaCapsule, strings.Repeat("c", 64), now.Add(5*time.Minute), time.Second,
	)
	if err != nil || len(betaPrepared.Receipts) != 1 {
		t.Fatalf("Beta prepare=%+v err=%v", betaPrepared, err)
	}
	betaPending, err := beta.ListPendingMemoryTrayCandidates(10)
	if err != nil || len(betaPending) != 1 {
		t.Fatalf("Beta pending=%+v err=%v", betaPending, err)
	}
	if _, err := beta.PresentMemoryTrayCandidate(store.MemoryPresentationRequest{
		CandidateID: betaPending[0].CandidateID, CandidateVersion: betaPending[0].Version,
		ContentDigest: betaPending[0].ContentDigest, BindingDigest: betaBinding,
		TrustedSurfaceKind: "memory_home", TrustedSurfaceInstance: "surface_beta",
	}, now.Add(5*time.Minute), time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := beta.CommitMemoryTrayCandidate(
		betaPending[0].CandidateID, betaPending[0].Version, now.Add(5*time.Minute+time.Second),
	); err != nil {
		t.Fatal(err)
	}
	if betaRecall, err = beta.RecallMemory(store.RecallMemoryQuery{Query: "Cursor-bound project", Limit: 10}); err != nil || len(betaRecall) != 1 {
		t.Fatalf("Beta cursor recall=%+v err=%v", betaRecall, err)
	}
	alphaBetaRecall, err := alpha.RecallMemory(store.RecallMemoryQuery{Query: "Cursor-bound project", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, recalled := range alphaBetaRecall {
		if recalled.Summary == "Beta belongs only to the Cursor-bound project." {
			t.Fatalf("Alpha received Beta Cursor memory=%+v", alphaBetaRecall)
		}
	}
}
