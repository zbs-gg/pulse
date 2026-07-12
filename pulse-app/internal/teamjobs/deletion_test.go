package teamjobs

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

type deletionStoreFake struct {
	reapResults  []int64
	reapErrors   []error
	claimResults [][]store.TeamDeletionClaim
	claimErrors  []error
	completeErrs map[string]error
	failErrs     map[string]error

	reapRequests     []store.TeamDeletionReapRequest
	claimRequests    []store.TeamDeletionClaimRequest
	completeRequests []store.TeamDeletionCompletionRequest
	failRequests     []store.TeamDeletionFailureRequest
}

func (fake *deletionStoreFake) ReapExpiredTeamDeletionLeases(
	_ context.Context,
	request store.TeamDeletionReapRequest,
) (int64, error) {
	fake.reapRequests = append(fake.reapRequests, request)
	index := len(fake.reapRequests) - 1
	if index < len(fake.reapErrors) && fake.reapErrors[index] != nil {
		return 0, fake.reapErrors[index]
	}
	if index < len(fake.reapResults) {
		return fake.reapResults[index], nil
	}
	return 0, nil
}

func (fake *deletionStoreFake) ClaimTeamDeletionJobs(
	_ context.Context,
	request store.TeamDeletionClaimRequest,
) ([]store.TeamDeletionClaim, error) {
	fake.claimRequests = append(fake.claimRequests, request)
	index := len(fake.claimRequests) - 1
	if index < len(fake.claimErrors) && fake.claimErrors[index] != nil {
		return nil, fake.claimErrors[index]
	}
	if index < len(fake.claimResults) {
		return append([]store.TeamDeletionClaim(nil), fake.claimResults[index]...), nil
	}
	return nil, nil
}

func (fake *deletionStoreFake) CompleteTeamDeletion(
	_ context.Context,
	request store.TeamDeletionCompletionRequest,
) (store.TeamDeletionStatus, error) {
	fake.completeRequests = append(fake.completeRequests, request)
	if err := fake.completeErrs[request.OperationID]; err != nil {
		return store.TeamDeletionStatus{}, err
	}
	return store.TeamDeletionStatus{OperationID: request.OperationID, Status: store.TeamDeletionStatusComplete}, nil
}

func (fake *deletionStoreFake) FailTeamDeletion(
	_ context.Context,
	request store.TeamDeletionFailureRequest,
) error {
	fake.failRequests = append(fake.failRequests, request)
	return fake.failErrs[request.OperationID]
}

func deletionWorkerForTest(t *testing.T, storage DeletionStore) *DeletionWorker {
	t.Helper()
	worker, err := NewDeletionWorker(DeletionWorkerConfig{
		Store: storage,
		Writer: store.TeamWriterLeaseIdentity{
			WriterID: "team-worker-1", Token: "writer-token-1",
		},
		ClaimLimit:   4,
		ReapLimit:    8,
		LeaseTTL:     30 * time.Second,
		PollInterval: time.Millisecond,
		BaseBackoff:  2 * time.Second,
		MaxBackoff:   10 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewDeletionWorker: %v", err)
	}
	return worker
}

func TestDeletionWorkerRunOnceReapsClaimsAndCompletesWithBoundWriterLease(t *testing.T) {
	fake := &deletionStoreFake{
		reapResults: []int64{2},
		claimResults: [][]store.TeamDeletionClaim{{
			{OperationID: "delete-op-1", LeaseToken: "lease-1", AttemptCount: 1},
			{OperationID: "delete-op-2", LeaseToken: "lease-2", AttemptCount: 2},
		}},
		completeErrs: map[string]error{}, failErrs: map[string]error{},
	}
	worker := deletionWorkerForTest(t, fake)

	result, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Reaped != 2 || result.Claimed != 2 || result.Completed != 2 || result.Failed != 0 {
		t.Fatalf("result = %+v", result)
	}
	if !reflect.DeepEqual(fake.reapRequests, []store.TeamDeletionReapRequest{{
		WriterID: "team-worker-1", WriterToken: "writer-token-1", Limit: 8,
	}}) {
		t.Fatalf("reap requests = %+v", fake.reapRequests)
	}
	if !reflect.DeepEqual(fake.claimRequests, []store.TeamDeletionClaimRequest{{
		WriterID: "team-worker-1", WriterToken: "writer-token-1", Limit: 4,
		LeaseTTL: 30 * time.Second,
	}}) {
		t.Fatalf("claim requests = %+v", fake.claimRequests)
	}
	wantCompletions := []store.TeamDeletionCompletionRequest{
		{WriterID: "team-worker-1", WriterToken: "writer-token-1", OperationID: "delete-op-1", LeaseToken: "lease-1"},
		{WriterID: "team-worker-1", WriterToken: "writer-token-1", OperationID: "delete-op-2", LeaseToken: "lease-2"},
	}
	if !reflect.DeepEqual(fake.completeRequests, wantCompletions) {
		t.Fatalf("completion requests = %+v", fake.completeRequests)
	}
}

func TestDeletionWorkerRecordsOnlyGenericFailureCodesWithBoundedExponentialBackoff(t *testing.T) {
	fake := &deletionStoreFake{
		claimResults: [][]store.TeamDeletionClaim{{
			{OperationID: "delete-barrier", LeaseToken: "lease-barrier", AttemptCount: 1},
			{OperationID: "delete-storage", LeaseToken: "lease-storage", AttemptCount: 3},
			{OperationID: "delete-interrupted", LeaseToken: "lease-interrupted", AttemptCount: 20},
		}},
		completeErrs: map[string]error{
			"delete-barrier":     store.ErrTeamDeletionBarrier,
			"delete-storage":     errors.New("synthetic internal detail that must not persist"),
			"delete-interrupted": context.DeadlineExceeded,
		},
		failErrs: map[string]error{},
	}
	worker := deletionWorkerForTest(t, fake)

	result, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Claimed != 3 || result.Completed != 0 || result.Failed != 3 {
		t.Fatalf("result = %+v", result)
	}
	want := []store.TeamDeletionFailureRequest{
		{WriterID: "team-worker-1", WriterToken: "writer-token-1", OperationID: "delete-barrier", LeaseToken: "lease-barrier", ErrorCode: store.TeamDeletionFailureTemporary, Backoff: 2 * time.Second},
		{WriterID: "team-worker-1", WriterToken: "writer-token-1", OperationID: "delete-storage", LeaseToken: "lease-storage", ErrorCode: store.TeamDeletionFailureStorageUnavailable, Backoff: 8 * time.Second},
		{WriterID: "team-worker-1", WriterToken: "writer-token-1", OperationID: "delete-interrupted", LeaseToken: "lease-interrupted", ErrorCode: store.TeamDeletionFailureWorkerInterrupted, Backoff: 10 * time.Second},
	}
	if !reflect.DeepEqual(fake.failRequests, want) {
		t.Fatalf("failure requests = %+v", fake.failRequests)
	}
}

func TestDeletionWorkerTreatsWriterLeaseLossAsFatalWithoutReclassifyingIt(t *testing.T) {
	for _, test := range []struct {
		name string
		fake *deletionStoreFake
	}{
		{
			name: "reap",
			fake: &deletionStoreFake{reapErrors: []error{store.ErrTeamWriterLeaseMismatch}},
		},
		{
			name: "claim",
			fake: &deletionStoreFake{claimErrors: []error{store.ErrTeamWriterLeaseMismatch}},
		},
		{
			name: "complete",
			fake: &deletionStoreFake{
				claimResults: [][]store.TeamDeletionClaim{{{
					OperationID: "delete-lease-lost", LeaseToken: "lease-lost", AttemptCount: 1,
				}}},
				completeErrs: map[string]error{"delete-lease-lost": store.ErrTeamWriterLeaseMismatch},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker := deletionWorkerForTest(t, test.fake)
			_, err := worker.RunOnce(context.Background())
			if !errors.Is(err, store.ErrTeamWriterLeaseMismatch) {
				t.Fatalf("RunOnce error = %v", err)
			}
			if len(test.fake.failRequests) != 0 {
				t.Fatalf("writer lease loss was reclassified: %+v", test.fake.failRequests)
			}
		})
	}
}

func TestDeletionWorkerRunRetriesTransientStoreFailureButReturnsWriterLeaseLoss(t *testing.T) {
	fake := &deletionStoreFake{
		claimErrors: []error{
			errors.New("synthetic transient outage"),
			store.ErrTeamWriterLeaseMismatch,
		},
	}
	worker := deletionWorkerForTest(t, fake)
	err := worker.Run(context.Background())
	if !errors.Is(err, store.ErrTeamWriterLeaseMismatch) {
		t.Fatalf("Run error = %v", err)
	}
	if len(fake.claimRequests) != 2 {
		t.Fatalf("claim attempts = %d, want 2", len(fake.claimRequests))
	}
}

func TestDeletionWorkerRunStopsCleanlyOnContextCancellation(t *testing.T) {
	fake := &deletionStoreFake{}
	worker := deletionWorkerForTest(t, fake)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("canceled Run error = %v", err)
	}
	if len(fake.claimRequests) != 0 || len(fake.reapRequests) != 0 {
		t.Fatalf("canceled worker touched store: reap=%d claim=%d", len(fake.reapRequests), len(fake.claimRequests))
	}
}
