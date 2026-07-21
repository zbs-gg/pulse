package teamjobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

const maxDeletionBatch = 64

// DeletionStore is the complete durable boundary used by the deletion worker.
// It intentionally exposes no content-bearing reads.
type DeletionStore interface {
	ReapExpiredTeamDeletionLeases(context.Context, store.TeamDeletionReapRequest) (int64, error)
	ClaimTeamDeletionJobs(context.Context, store.TeamDeletionClaimRequest) ([]store.TeamDeletionClaim, error)
	CompleteTeamDeletion(context.Context, store.TeamDeletionCompletionRequest) (store.TeamDeletionStatus, error)
	FailTeamDeletion(context.Context, store.TeamDeletionFailureRequest) error
}

type DeletionWorkerConfig struct {
	Store        DeletionStore
	Writer       store.TeamWriterLeaseIdentity
	ClaimLimit   int
	ReapLimit    int
	LeaseTTL     time.Duration
	PollInterval time.Duration
	BaseBackoff  time.Duration
	MaxBackoff   time.Duration
}

type DeletionWorker struct {
	store        DeletionStore
	writer       store.TeamWriterLeaseIdentity
	claimLimit   int
	reapLimit    int
	leaseTTL     time.Duration
	pollInterval time.Duration
	baseBackoff  time.Duration
	maxBackoff   time.Duration
}

type DeletionRunResult struct {
	Reaped    int64
	Claimed   int
	Completed int
	Failed    int
}

func NewDeletionWorker(config DeletionWorkerConfig) (*DeletionWorker, error) {
	if config.Store == nil || config.Writer.WriterID == "" || config.Writer.Token == "" ||
		config.ClaimLimit < 1 || config.ClaimLimit > maxDeletionBatch ||
		config.ReapLimit < 1 || config.ReapLimit > maxDeletionBatch ||
		config.LeaseTTL <= 0 || config.LeaseTTL > 5*time.Minute ||
		config.PollInterval <= 0 || config.BaseBackoff <= 0 ||
		config.MaxBackoff < config.BaseBackoff || config.MaxBackoff > 24*time.Hour {
		return nil, errors.New("team deletion worker: invalid configuration")
	}
	return &DeletionWorker{
		store: config.Store, writer: config.Writer,
		claimLimit: config.ClaimLimit, reapLimit: config.ReapLimit,
		leaseTTL: config.LeaseTTL, pollInterval: config.PollInterval,
		baseBackoff: config.BaseBackoff, maxBackoff: config.MaxBackoff,
	}, nil
}

func (worker *DeletionWorker) RunOnce(ctx context.Context) (DeletionRunResult, error) {
	var result DeletionRunResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	reaped, err := worker.store.ReapExpiredTeamDeletionLeases(ctx, store.TeamDeletionReapRequest{
		WriterID: worker.writer.WriterID, WriterToken: worker.writer.Token,
		Limit: worker.reapLimit,
	})
	if err != nil {
		return result, fmt.Errorf("team deletion reap: %w", err)
	}
	result.Reaped = reaped
	claims, err := worker.store.ClaimTeamDeletionJobs(ctx, store.TeamDeletionClaimRequest{
		WriterID: worker.writer.WriterID, WriterToken: worker.writer.Token,
		Limit: worker.claimLimit, LeaseTTL: worker.leaseTTL,
	})
	if err != nil {
		return result, fmt.Errorf("team deletion claim: %w", err)
	}
	result.Claimed = len(claims)
	for _, claim := range claims {
		_, err := worker.store.CompleteTeamDeletion(ctx, store.TeamDeletionCompletionRequest{
			WriterID: worker.writer.WriterID, WriterToken: worker.writer.Token,
			OperationID: claim.OperationID, LeaseToken: claim.LeaseToken,
		})
		if err == nil {
			result.Completed++
			continue
		}
		if errors.Is(err, store.ErrTeamWriterLeaseMismatch) {
			return result, fmt.Errorf("team deletion complete: %w", err)
		}
		failure := store.TeamDeletionFailureRequest{
			WriterID: worker.writer.WriterID, WriterToken: worker.writer.Token,
			OperationID: claim.OperationID, LeaseToken: claim.LeaseToken,
			ErrorCode: deletionFailureCode(err),
			Backoff:   worker.backoff(claim.AttemptCount),
		}
		if failErr := worker.store.FailTeamDeletion(ctx, failure); failErr != nil {
			return result, fmt.Errorf("team deletion fail transition: %w", failErr)
		}
		result.Failed++
	}
	return result, nil
}

// Run retries transient storage failures on a fixed poll cadence. Writer lease
// loss is the only store failure that is terminal because continuing after it
// would permit two daemons to mutate one team store.
func (worker *DeletionWorker) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		_, err := worker.RunOnce(ctx)
		if err != nil && errors.Is(err, store.ErrTeamWriterLeaseMismatch) {
			return err
		}
		timer := time.NewTimer(worker.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func deletionFailureCode(err error) string {
	switch {
	case errors.Is(err, store.ErrTeamDeletionBarrier):
		return store.TeamDeletionFailureTemporary
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return store.TeamDeletionFailureWorkerInterrupted
	default:
		return store.TeamDeletionFailureStorageUnavailable
	}
}

func (worker *DeletionWorker) backoff(attempt int) time.Duration {
	delay := worker.baseBackoff
	for step := 1; step < attempt && delay < worker.maxBackoff; step++ {
		if delay > worker.maxBackoff/2 {
			return worker.maxBackoff
		}
		delay *= 2
	}
	if delay > worker.maxBackoff {
		return worker.maxBackoff
	}
	return delay
}
