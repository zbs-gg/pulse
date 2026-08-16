package historicalingest

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func completedApplyReviewFixture(t *testing.T) (*IngestManager, ReviewSnapshot) {
	t.Helper()
	manager, snapshot := reviewFixture(t)
	for {
		var blocking *ReviewItem
		for index := range snapshot.Items {
			if snapshot.Items[index].RequiresReview && snapshot.Items[index].Disposition == ReviewPending {
				blocking = &snapshot.Items[index]
				break
			}
		}
		if blocking == nil {
			break
		}
		var err error
		snapshot, err = manager.MutateReview(snapshot.JobID, ReviewMutation{
			ExpectedRevision: snapshot.Revision, ExpectedDigest: snapshot.ManifestDigest,
			CandidateID: blocking.Item.CandidateID, Disposition: ReviewKept,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	completed, err := manager.CompleteReview(snapshot.JobID, snapshot.Revision, snapshot.ManifestDigest,
		ReviewConfirmationDigest(snapshot.JobID, snapshot.Revision, snapshot.ManifestDigest), nil)
	if err != nil {
		t.Fatal(err)
	}
	return manager, completed
}

func TestApplyLifecyclePersistsWriteSetReceiptAndProjectionState(t *testing.T) {
	manager, review := completedApplyReviewFixture(t)
	source, err := manager.ApplySource(review.JobID, review.Revision, review.ManifestDigest)
	if err != nil || len(source.IncludedItems()) != review.WriteCount {
		t.Fatalf("source items=%d write_count=%d err=%v", len(source.IncludedItems()), review.WriteCount, err)
	}
	writeSetDigest := strings.Repeat("e", 64)
	if _, err := manager.RecordWriteSet(review.JobID, review.ManifestDigest, writeSetDigest, "store_personal_apply", 9); err != nil {
		t.Fatal(err)
	}
	refreshedDigest := strings.Repeat("f", 64)
	refreshed, err := manager.RecordWriteSet(review.JobID, review.ManifestDigest, refreshedDigest, "store_personal_apply", 10)
	if err != nil || refreshed.WriteSetDigest != refreshedDigest || refreshed.DestinationGeneration != 10 {
		t.Fatalf("refreshed=%+v err=%v", refreshed, err)
	}
	if _, err := manager.MarkApplying(review.JobID, review.ManifestDigest, writeSetDigest); !errors.Is(err, ErrApplyVersionConflict) {
		t.Fatalf("stale write set remained applicable: %v", err)
	}
	writeSetDigest = refreshedDigest
	if _, err := manager.MarkApplying(review.JobID, review.ManifestDigest, writeSetDigest); err != nil {
		t.Fatal(err)
	}
	committed, err := manager.MarkCommitted(review.JobID, review.ManifestDigest, writeSetDigest, "history_receipt_apply")
	if err != nil || committed.State != JobCommittedIndexing || committed.BatchReceiptID != "history_receipt_apply" {
		t.Fatalf("committed=%+v err=%v", committed, err)
	}
	ready, err := manager.MarkProjectionState(review.JobID, committed.BatchReceiptID, ProjectionReady)
	if err != nil || ready.State != JobRetrievalReady {
		t.Fatalf("ready=%+v err=%v", ready, err)
	}
	reloaded, err := NewIngestManager(IngestManagerConfig{RootDir: manager.rootDir, Key: manager.key, Clock: manager.clock})
	if err != nil {
		t.Fatal(err)
	}
	status, err := reloaded.Status(review.JobID)
	if err != nil || status.State != JobRetrievalReady || status.WriteSetDigest != writeSetDigest || status.BatchReceiptID != committed.BatchReceiptID {
		t.Fatalf("reloaded=%+v err=%v", status, err)
	}
}

func TestWriteSetRoundTripRejectsTargetDrift(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	set := WriteSet{
		Schema: WriteSetSchemaV1, JobID: "job_0123456789abcdef", Revision: 2,
		ManifestDigest: strings.Repeat("a", 64), SourceSnapshotDigest: strings.Repeat("b", 64),
		SchemaDigest: strings.Repeat("c", 64), RunnerContractDigest: strings.Repeat("d", 64),
		ParserVersion: "parser_v1", PromptVersion: "prompt_v1", ModelID: HistoricalMemoryModelID, ModelEffort: "low",
		DestinationStoreID: "store_personal_apply", DestinationGeneration: 4,
		DestinationBindingDigest: strings.Repeat("e", 64), RepositoryID: "repository_apply",
		PolicyEpoch: 2, ResolverEpoch: 3, MaterializerVersion: MaterializerVersionV1, DedupVersion: DedupVersionV1,
		Items: []CanonicalWriteItem{{
			CandidateID: "candidate_0123456789abcdef", MaterialKind: MaterialKindDecision,
			CapsuleKind: "decision", Summary: "Preserve the exact reviewed decision.", Confidence: .9,
			Scope: Scope{Kind: ScopeGlobal}, EpistemicStatus: EpistemicExplicit, Derivation: DerivationDirect, ValidTime: ValidTime{From: now},
			SourceRefs:    []SourceRef{{Alias: "source_0123456789abcdef", PrefixDigest: strings.Repeat("1", 64), RecordLocator: "record:1"}},
			ContentDigest: strings.Repeat("2", 64),
			Target:        WriteTarget{Outcome: ItemCreated, ObjectKind: "memory_capsule", ObjectID: "history_object", ObjectDigest: strings.Repeat("2", 64), LogicalGeneration: 1},
		}},
	}
	set.TargetVersionsDigest = TargetVersionsDigest(set.Items)
	encoded, digest, err := EncodeWriteSet(set)
	if err != nil {
		t.Fatal(err)
	}
	decoded, decodedDigest, err := DecodeWriteSet(encoded)
	if err != nil || decodedDigest != digest || decoded.Items[0].Target.ObjectID != set.Items[0].Target.ObjectID {
		t.Fatalf("decoded=%+v digest=%q err=%v", decoded, decodedDigest, err)
	}
	set.Items[0].Target.ObjectDigest = strings.Repeat("3", 64)
	if _, _, err := EncodeWriteSet(set); err == nil {
		t.Fatal("target digest drift was accepted")
	}
}
