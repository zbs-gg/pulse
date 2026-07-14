package teamjobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

const (
	projectionSerialClaimWindow = 1
	maxProjectionRenewInterval  = 30 * time.Second
)

// ProjectionStore is the content-free durable boundary owned by the worker.
// ClaimTeamProjectionJobs also reclaims expired leases atomically, so the
// worker does not need a separate reaper or any in-memory recovery state.
type ProjectionStore interface {
	ClaimTeamProjectionJobs(context.Context, store.TeamProjectionClaimRequest) ([]store.TeamProjectionJobClaim, error)
	RenewTeamProjectionJobLease(context.Context, store.TeamProjectionLeaseRenewalRequest) (store.TeamProjectionLeaseRenewalResult, error)
	FailTeamProjectionJob(context.Context, store.TeamProjectionFailureRequest) error
	RecordTeamProjectionWorkerHeartbeat(context.Context, store.TeamProjectionWorkerHeartbeatRequest) error
}

// ProjectionProcessRequest carries the writer lease separately from the job
// claim. A kind-specific processor uses both to call the matching generation-
// fenced store completion API; content and vectors never enter the durable job.
type ProjectionProcessRequest struct {
	Writer store.TeamWriterLeaseIdentity
	Claim  store.TeamProjectionJobClaim
}

// ProjectionProcessor owns kind-specific content reconstruction and vector
// production. The worker owns only claim, retry, and lease fencing.
type ProjectionProcessor interface {
	ProcessTeamProjection(context.Context, ProjectionProcessRequest) error
}

type ProjectionDependencyHealth struct {
	State  string
	Reason string
}

// ProjectionDependencyReporter exposes only bounded deployment state. A
// worker that owns all projection kinds must not report ready unless the
// embedding processor it may claim is actually configured.
type ProjectionDependencyReporter interface {
	ProjectionDependencyHealth() ProjectionDependencyHealth
}

type ProjectionProcessorFunc func(context.Context, ProjectionProcessRequest) error

func (process ProjectionProcessorFunc) ProcessTeamProjection(
	ctx context.Context,
	request ProjectionProcessRequest,
) error {
	return process(ctx, request)
}

// ProjectionFailureCoder lets a processor classify a dependency failure
// without exposing its raw error text to durable storage. Unknown or invalid
// classifications are persisted as temporary_failure.
type ProjectionFailureCoder interface {
	ProjectionFailureCode() string
}

// ProjectionProcessError is the standard classified processor error. Err is
// returned to callers for diagnostics but is never copied into a job row.
type ProjectionProcessError struct {
	Code string
	Err  error
}

func (failure ProjectionProcessError) Error() string {
	if failure.Err != nil {
		return failure.Err.Error()
	}
	return failure.Code
}

func (failure ProjectionProcessError) Unwrap() error {
	return failure.Err
}

func (failure ProjectionProcessError) ProjectionFailureCode() string {
	return failure.Code
}

type ProjectionWorkerConfig struct {
	Store             ProjectionStore
	Processor         ProjectionProcessor
	Writer            store.TeamWriterLeaseIdentity
	ProjectionKind    string
	ClaimLimit        int
	LeaseTTL          time.Duration
	PollInterval      time.Duration
	HeartbeatInterval time.Duration
	WorkerInstanceID  string
	BaseBackoff       time.Duration
	MaxBackoff        time.Duration
}

type ProjectionWorker struct {
	store             ProjectionStore
	processor         ProjectionProcessor
	writer            store.TeamWriterLeaseIdentity
	projectionKind    string
	claimLimit        int
	leaseTTL          time.Duration
	pollInterval      time.Duration
	heartbeatInterval time.Duration
	workerInstanceID  string
	baseBackoff       time.Duration
	maxBackoff        time.Duration
}

type ProjectionRunResult struct {
	Claimed    int
	Completed  int
	Failed     int
	Stale      int
	Duplicates int
}

func NewProjectionWorker(config ProjectionWorkerConfig) (*ProjectionWorker, error) {
	if config.Store == nil || config.Processor == nil ||
		config.Writer.WriterID == "" || config.Writer.Token == "" ||
		!validProjectionWorkerKind(config.ProjectionKind) ||
		config.ClaimLimit != projectionSerialClaimWindow ||
		config.LeaseTTL <= 0 || config.LeaseTTL > 5*time.Minute ||
		config.PollInterval <= 0 || config.HeartbeatInterval <= 0 ||
		config.HeartbeatInterval > 30*time.Second ||
		!validProjectionWorkerKind(config.WorkerInstanceID) || config.WorkerInstanceID == "" ||
		config.BaseBackoff < time.Second ||
		config.MaxBackoff < config.BaseBackoff || config.MaxBackoff > 24*time.Hour {
		return nil, errors.New("team projection worker: invalid configuration")
	}
	return &ProjectionWorker{
		store: config.Store, processor: config.Processor, writer: config.Writer,
		// Processing is serial. Claiming exactly one prevents later jobs from
		// aging behind slow earlier materialization while the current claim is
		// renewed independently during provider work. The constructor rejects
		// any other advertised limit, so callers cannot configure a value that
		// the worker silently ignores.
		projectionKind: config.ProjectionKind, claimLimit: config.ClaimLimit,
		leaseTTL: config.LeaseTTL, pollInterval: config.PollInterval,
		heartbeatInterval: config.HeartbeatInterval, workerInstanceID: config.WorkerInstanceID,
		baseBackoff: config.BaseBackoff, maxBackoff: config.MaxBackoff,
	}, nil
}

func (worker *ProjectionWorker) RunOnce(ctx context.Context) (ProjectionRunResult, error) {
	var result ProjectionRunResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	claims, err := worker.store.ClaimTeamProjectionJobs(ctx, store.TeamProjectionClaimRequest{
		WriterID: worker.writer.WriterID, WriterToken: worker.writer.Token,
		ProjectionKind: worker.projectionKind, Limit: worker.claimLimit,
		LeaseTTL: worker.leaseTTL,
	})
	if err != nil {
		return result, fmt.Errorf("team projection claim: %w", err)
	}
	result.Claimed = len(claims)
	seen := make(map[string]struct{}, len(claims))
	for _, claim := range claims {
		claimKey := claim.JobID + "\x00" + claim.LeaseToken
		if _, duplicate := seen[claimKey]; duplicate {
			result.Duplicates++
			continue
		}
		seen[claimKey] = struct{}{}
		err := worker.processWithLeaseRenewal(ctx, ProjectionProcessRequest{
			Writer: worker.writer,
			Claim:  claim,
		})
		if err == nil {
			result.Completed++
			continue
		}
		if errors.Is(err, store.ErrTeamWriterLeaseMismatch) {
			return result, fmt.Errorf("team projection complete: %w", err)
		}
		// The completion APIs conceal all stale authority: expired job lease,
		// superseded generation, tombstoned/deleted root, and lost duplicate
		// races. None is safe to reclassify with the now-invalid lease.
		if errors.Is(err, store.ErrConcealedNotFound) {
			result.Stale++
			continue
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		failure := store.TeamProjectionFailureRequest{
			WriterID: worker.writer.WriterID, WriterToken: worker.writer.Token,
			JobID: claim.JobID, LeaseToken: claim.LeaseToken,
			ErrorCode: projectionFailureCode(err),
			Backoff:   worker.backoff(claim.AttemptCount),
		}
		if failErr := worker.store.FailTeamProjectionJob(ctx, failure); failErr != nil {
			switch {
			case errors.Is(failErr, store.ErrTeamWriterLeaseMismatch):
				return result, fmt.Errorf("team projection fail transition: %w", failErr)
			case errors.Is(failErr, store.ErrConcealedNotFound):
				// Completion may have committed while its response was lost, or
				// deletion/generation change may have won before this transition.
				result.Stale++
				continue
			default:
				return result, fmt.Errorf("team projection fail transition: %w", failErr)
			}
		}
		result.Failed++
	}
	return result, nil
}

func (worker *ProjectionWorker) processWithLeaseRenewal(
	ctx context.Context,
	request ProjectionProcessRequest,
) error {
	processCtx, cancelProcess := context.WithCancel(ctx)
	defer cancelProcess()
	renewalCtx, cancelRenewal := context.WithCancel(ctx)
	defer cancelRenewal()
	renewalResult := make(chan error, 1)
	go func() {
		interval := worker.leaseTTL / 3
		if interval <= 0 {
			interval = worker.leaseTTL
		}
		if interval > maxProjectionRenewInterval {
			interval = maxProjectionRenewInterval
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-renewalCtx.Done():
				renewalResult <- nil
				return
			case <-ticker.C:
				_, err := worker.store.RenewTeamProjectionJobLease(renewalCtx, store.TeamProjectionLeaseRenewalRequest{
					WriterID: worker.writer.WriterID, WriterToken: worker.writer.Token,
					JobID: request.Claim.JobID, LeaseToken: request.Claim.LeaseToken,
					LeaseTTL: worker.leaseTTL,
				})
				if err != nil {
					if renewalCtx.Err() != nil {
						renewalResult <- nil
						return
					}
					cancelProcess()
					renewalResult <- err
					return
				}
			}
		}
	}()

	processErr := worker.processor.ProcessTeamProjection(processCtx, request)
	cancelRenewal()
	renewalErr := <-renewalResult
	// A nil processor result means its fenced completion committed. A renewal
	// may then observe the now-terminal row and lose that harmless race.
	if processErr == nil {
		return nil
	}
	if renewalErr != nil {
		return fmt.Errorf("team projection lease renewal: %w", renewalErr)
	}
	return processErr
}

// Run retries transient claim, processor, and failure-transition errors on a
// fixed poll cadence. Writer lease loss is terminal: continuing would allow
// two daemons to project the same Team store concurrently.
func (worker *ProjectionWorker) Run(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return nil
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := worker.recordHeartbeat(workerCtx, ""); err != nil {
		return fmt.Errorf("team projection heartbeat: %w", err)
	}
	var lastErrorCode atomic.Value
	lastErrorCode.Store("")
	heartbeatResult := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(worker.heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				heartbeatResult <- nil
				return
			case <-ticker.C:
				if err := worker.recordHeartbeat(workerCtx, lastErrorCode.Load().(string)); err != nil {
					heartbeatResult <- fmt.Errorf("team projection heartbeat: %w", err)
					cancel()
					return
				}
			}
		}
	}()
	for {
		if err := workerCtx.Err(); err != nil {
			if heartbeatErr := <-heartbeatResult; heartbeatErr != nil {
				return heartbeatErr
			}
			return nil
		}
		_, err := worker.RunOnce(workerCtx)
		if workerCtx.Err() != nil {
			heartbeatErr := <-heartbeatResult
			if heartbeatErr != nil {
				return heartbeatErr
			}
			return nil
		}
		if err != nil && errors.Is(err, store.ErrTeamWriterLeaseMismatch) {
			cancel()
			<-heartbeatResult
			return err
		}
		if err != nil {
			lastErrorCode.Store(store.TeamProjectionWorkerErrorCycleFailed)
			if heartbeatErr := worker.recordHeartbeat(workerCtx, store.TeamProjectionWorkerErrorCycleFailed); heartbeatErr != nil {
				cancel()
				<-heartbeatResult
				return fmt.Errorf("team projection heartbeat: %w", heartbeatErr)
			}
		} else {
			lastErrorCode.Store("")
		}
		timer := time.NewTimer(worker.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			cancel()
			<-heartbeatResult
			return nil
		case heartbeatErr := <-heartbeatResult:
			timer.Stop()
			return heartbeatErr
		case <-timer.C:
		}
	}
}

func (worker *ProjectionWorker) recordHeartbeat(ctx context.Context, lastErrorCode string) error {
	dependency := ProjectionDependencyHealth{State: store.TeamProjectionDependencyReady}
	if worker.projectionKind == "" || worker.projectionKind == "embedding" {
		reporter, ok := worker.processor.(ProjectionDependencyReporter)
		if !ok {
			dependency = ProjectionDependencyHealth{
				State:  store.TeamProjectionDependencyDegraded,
				Reason: store.TeamProjectionWorkerReasonEmbeddingNotConfigured,
			}
		} else {
			dependency = reporter.ProjectionDependencyHealth()
		}
	}
	return worker.store.RecordTeamProjectionWorkerHeartbeat(ctx, store.TeamProjectionWorkerHeartbeatRequest{
		WriterID: worker.writer.WriterID, WriterToken: worker.writer.Token,
		WorkerInstanceID: worker.workerInstanceID,
		DependencyState:  dependency.State, DependencyReason: dependency.Reason,
		LastErrorCode: lastErrorCode,
	})
}

func projectionFailureCode(err error) string {
	var classified ProjectionFailureCoder
	if errors.As(err, &classified) {
		if code := classified.ProjectionFailureCode(); validProjectionFailureCode(code) {
			return code
		}
		return store.TeamProjectionFailureTemporary
	}
	switch {
	case errors.Is(err, store.ErrProjectionMaterializationFailed),
		errors.Is(err, store.ErrInvalidProjectionJobRequest):
		return store.TeamProjectionFailureMaterialization
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return store.TeamProjectionFailureWorkerInterrupted
	default:
		return store.TeamProjectionFailureTemporary
	}
}

func validProjectionFailureCode(code string) bool {
	switch code {
	case store.TeamProjectionFailureDependencyTimeout,
		store.TeamProjectionFailureDependencyUnavailable,
		store.TeamProjectionFailureRateLimited,
		store.TeamProjectionFailureStorageUnavailable,
		store.TeamProjectionFailureWorkerInterrupted,
		store.TeamProjectionFailureMaterialization,
		store.TeamProjectionFailureTemporary:
		return true
	default:
		return false
	}
}

func validProjectionWorkerKind(kind string) bool {
	if kind == "" {
		return true
	}
	if len(kind) > 64 || strings.TrimSpace(kind) != kind {
		return false
	}
	for _, character := range kind {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			!strings.ContainsRune("._:-", character) {
			return false
		}
	}
	return true
}

func (worker *ProjectionWorker) backoff(attempt int) time.Duration {
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
