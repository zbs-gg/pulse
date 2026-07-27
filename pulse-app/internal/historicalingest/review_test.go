package historicalingest

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func reviewFixture(t *testing.T) (*IngestManager, ReviewSnapshot) {
	t.Helper()
	now := time.Date(2026, 7, 22, 4, 0, 0, 0, time.UTC)
	manager, _ := testIngestManager(t, func() time.Time { return now })
	jobID := "job_0123456789abcdef"
	unit := testUnits()[0]
	if _, err := manager.StartJob(jobID, testSnapshotOne(), []WorkUnit{unit}, testRunnerContract()); err != nil {
		t.Fatal(err)
	}
	lease, err := manager.LeaseNext(jobID)
	if err != nil {
		t.Fatal(err)
	}
	result := testUnitResult(unit, "Keep the accepted architecture small.")
	result.Items = append(result.Items,
		MaterialItem{
			CandidateID: "candidate_1111111111111111", Kind: MaterialKindState, Confidence: .7,
			Privacy: PrivacyPrivate, EpistemicStatus: EpistemicHypothesis, Derivation: DerivationInferred,
			ValidTime: ValidTime{From: now}, Scope: Scope{Kind: ScopeGlobal}, SourceRefs: result.Items[0].SourceRefs,
			Payload: MaterialPayload{StateKind: "emotion", Summary: "The owner may be frustrated.", Intensity: floatPointer(.7)},
		},
		MaterialItem{
			CandidateID: "candidate_2222222222222222", Kind: MaterialKindAssertion, Confidence: .8,
			Privacy: PrivacyPrivate, EpistemicStatus: EpistemicExplicit, Derivation: DerivationDirect,
			ValidTime: ValidTime{From: now}, Scope: Scope{Kind: ScopeUnassigned}, SourceRefs: result.Items[0].SourceRefs,
			Payload: MaterialPayload{SubjectID: "person_alex", Predicate: "works_on", ObjectValue: "project_pulse"},
		},
		MaterialItem{
			CandidateID: "candidate_3333333333333333", Kind: MaterialKindRelation, Confidence: .9,
			Privacy: PrivacyPrivate, EpistemicStatus: EpistemicExplicit, Derivation: DerivationDirect,
			ValidTime: ValidTime{From: now}, Scope: Scope{Kind: ScopeGlobal}, SourceRefs: result.Items[0].SourceRefs,
			Payload: MaterialPayload{SubjectID: "person_alex", Predicate: "works_on", ObjectID: "project_pulse"},
		},
	)
	if _, err := manager.SubmitResult(jobID, unit.ID, lease.Token, result, TokenUsage{InputTokens: 50, OutputTokens: 10}, testRunnerContract().Digest); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := manager.BuildManifest(jobID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.ReviewSnapshot(jobID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return manager, snapshot
}

func floatPointer(value float64) *float64 { return &value }

func TestReviewOrdersBlockersAndRequiresExplicitDispositionBeforeCompletion(t *testing.T) {
	manager, snapshot := reviewFixture(t)
	if snapshot.SourceRootCount != 1 || snapshot.CandidateCount != 4 || snapshot.WriteCount != 4 || snapshot.RemainingRequired != 2 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if !snapshot.Items[0].RequiresReview || !snapshot.Items[1].RequiresReview {
		t.Fatalf("blocking items were not first: %+v", snapshot.Items)
	}
	byReason := func(reason string) ReviewItem {
		t.Helper()
		for _, item := range snapshot.Items {
			for _, code := range item.RequirementCodes {
				if code == reason {
					return item
				}
			}
		}
		t.Fatalf("missing reason %s", reason)
		return ReviewItem{}
	}
	hypothesis := byReason("hypothesis")
	first, err := manager.MutateReview(snapshot.JobID, ReviewMutation{
		ExpectedRevision: snapshot.Revision, ExpectedDigest: snapshot.ManifestDigest,
		CandidateID: hypothesis.Item.CandidateID, Disposition: ReviewKept,
	})
	if err != nil || first.Revision != snapshot.Revision+1 || first.ManifestDigest == snapshot.ManifestDigest || first.RemainingRequired != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if _, err := manager.MutateReview(snapshot.JobID, ReviewMutation{
		ExpectedRevision: snapshot.Revision, ExpectedDigest: snapshot.ManifestDigest,
		CandidateID: hypothesis.Item.CandidateID, Disposition: ReviewKept,
	}); !errors.Is(err, ErrReviewVersionConflict) {
		t.Fatalf("stale mutation error=%v", err)
	}
	unassigned := reviewItemWithReason(t, first, "unassigned_scope")
	second, err := manager.MutateReview(first.JobID, ReviewMutation{
		ExpectedRevision: first.Revision, ExpectedDigest: first.ManifestDigest,
		CandidateID: unassigned.Item.CandidateID, Disposition: ReviewExcluded,
	})
	if err != nil || second.RemainingRequired != 0 || second.ExcludedCount != 1 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	ordinary := firstPendingOrdinary(t, second)
	unavailable := map[string]bool{ordinary.Item.CandidateID: true}
	confirmation := ReviewConfirmationDigest(second.JobID, second.Revision, second.ManifestDigest)
	if _, err := manager.CompleteReview(second.JobID, second.Revision, second.ManifestDigest, confirmation, unavailable); !errors.Is(err, ErrReviewIncomplete) {
		t.Fatalf("unavailable evidence completed review: %v", err)
	}
	third, err := manager.MutateReview(second.JobID, ReviewMutation{
		ExpectedRevision: second.Revision, ExpectedDigest: second.ManifestDigest,
		CandidateID: ordinary.Item.CandidateID, Disposition: ReviewKept,
	})
	if err != nil {
		t.Fatal(err)
	}
	confirmation = ReviewConfirmationDigest(third.JobID, third.Revision, third.ManifestDigest)
	completed, err := manager.CompleteReview(third.JobID, third.Revision, third.ManifestDigest, confirmation, unavailable)
	if err != nil || !completed.ReviewComplete || completed.State != JobApprovalReady || completed.ManifestDigest == third.ManifestDigest {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	reloaded, err := NewIngestManager(IngestManagerConfig{RootDir: manager.rootDir, Key: manager.key, Clock: manager.clock})
	if err != nil {
		t.Fatal(err)
	}
	reloadedReview, err := reloaded.ReviewSnapshot(completed.JobID, unavailable)
	if err != nil || !reloadedReview.ReviewComplete || reloadedReview.ManifestDigest != completed.ManifestDigest {
		t.Fatalf("reloaded=%+v err=%v", reloadedReview, err)
	}
}

func TestReviewEditPreservesProvenanceChangesDigestAndInvalidatesCompletion(t *testing.T) {
	manager, snapshot := reviewFixture(t)
	ordinary := firstPendingOrdinary(t, snapshot)
	replacement := ordinary.Item
	replacement.Payload.Summary = "Use the smallest accepted architecture and preserve review receipts."
	edited, err := manager.MutateReview(snapshot.JobID, ReviewMutation{
		ExpectedRevision: snapshot.Revision, ExpectedDigest: snapshot.ManifestDigest,
		CandidateID: ordinary.Item.CandidateID, Disposition: ReviewKept, Replacement: &replacement,
	})
	if err != nil || edited.ManifestDigest == snapshot.ManifestDigest || edited.Revision != snapshot.Revision+1 {
		t.Fatalf("edited=%+v err=%v", edited, err)
	}
	var found bool
	for _, item := range edited.Items {
		if item.Item.Payload.Summary == replacement.Payload.Summary {
			found = item.Item.CandidateID != ordinary.Item.CandidateID && sameSourceRefs(item.Item.SourceRefs, ordinary.Item.SourceRefs)
		}
	}
	if !found {
		t.Fatal("edited candidate did not receive a new identity with preserved provenance")
	}
	pathLike := replacement
	pathLike.Payload.Summary = "Read /Users/private/session.jsonl"
	if _, err := manager.MutateReview(edited.JobID, ReviewMutation{
		ExpectedRevision: edited.Revision, ExpectedDigest: edited.ManifestDigest,
		CandidateID: firstPendingOrdinary(t, edited).Item.CandidateID, Disposition: ReviewKept, Replacement: &pathLike,
	}); !errors.Is(err, ErrReviewInvalid) {
		t.Fatalf("path-like edit error=%v", err)
	}
	unsafe := replacement
	unsafe.SourceRefs[0].PrefixDigest = strings.Repeat("9", 64)
	if _, err := manager.MutateReview(edited.JobID, ReviewMutation{
		ExpectedRevision: edited.Revision, ExpectedDigest: edited.ManifestDigest,
		CandidateID: edited.Items[0].Item.CandidateID, Disposition: ReviewKept, Replacement: &unsafe,
	}); !errors.Is(err, ErrReviewInvalid) {
		t.Fatalf("changed provenance error=%v", err)
	}
}

func TestEntityMergeAndSplitRequireExactPreviewDigest(t *testing.T) {
	manager, snapshot := reviewFixture(t)
	preview, err := manager.PreviewEntityRewrite(snapshot.JobID, snapshot.Revision, snapshot.ManifestDigest, "merge", "person_alex", "person_alexander", nil)
	if err != nil || len(preview.Affected) != 2 || preview.PreviewDigest == "" {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if _, err := manager.ApplyEntityRewrite(snapshot.JobID, snapshot.Revision, snapshot.ManifestDigest, "merge", "person_alex", "person_alexander", nil, strings.Repeat("0", 64)); !errors.Is(err, ErrReviewVersionConflict) {
		t.Fatalf("wrong preview digest error=%v", err)
	}
	merged, err := manager.ApplyEntityRewrite(snapshot.JobID, snapshot.Revision, snapshot.ManifestDigest, "merge", "person_alex", "person_alexander", nil, preview.PreviewDigest)
	if err != nil || merged.Revision != snapshot.Revision+1 || merged.ReviewComplete {
		t.Fatalf("merged=%+v err=%v", merged, err)
	}
	for _, item := range merged.Items {
		if item.Item.Payload.SubjectID == "person_alex" || item.Item.Payload.ObjectID == "person_alex" {
			t.Fatalf("old entity survived merge: %+v", item.Item)
		}
	}
}

func TestEvidenceLookupReturnsFrozenUnitWithoutAPath(t *testing.T) {
	manager, snapshot := reviewFixture(t)
	unit, source, err := manager.EvidenceUnitForCandidate(snapshot.JobID, snapshot.Items[0].Item.CandidateID)
	if err != nil || unit.ID != "unit_a" || source.Digest != testSnapshotOne().Digest {
		t.Fatalf("unit=%+v source=%+v err=%v", unit, source, err)
	}
}

func reviewItemWithReason(t *testing.T, snapshot ReviewSnapshot, reason string) ReviewItem {
	t.Helper()
	for _, item := range snapshot.Items {
		for _, code := range item.RequirementCodes {
			if code == reason {
				return item
			}
		}
	}
	t.Fatalf("missing review reason %s", reason)
	return ReviewItem{}
}

func firstPendingOrdinary(t *testing.T, snapshot ReviewSnapshot) ReviewItem {
	t.Helper()
	for _, item := range snapshot.Items {
		if !item.RequiresReview && item.Disposition == ReviewPending {
			return item
		}
	}
	t.Fatal("missing ordinary pending item")
	return ReviewItem{}
}
