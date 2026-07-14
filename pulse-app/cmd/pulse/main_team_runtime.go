package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

type teamRuntimeOptions struct {
	Signals              <-chan os.Signal
	RenewLease           func(context.Context) error
	RunBackgroundWorkers func(context.Context) error
	ShutdownTimeout      time.Duration
	QuiesceTimeout       time.Duration
}

type teamRuntimeWorker struct {
	Name string
	Run  func(context.Context) error
}

type teamRuntimeWorkerResult struct {
	Name string
	Err  error
}

// runTeamBackgroundWorkers keeps all writer-authorized workers under the same
// cancellation boundary. Any unexpected exit is terminal; during an ordinary
// runtime cancellation every worker must stop before this function returns.
func runTeamBackgroundWorkers(
	ctx context.Context,
	peerJoinTimeout time.Duration,
	workers ...teamRuntimeWorker,
) error {
	active := make([]teamRuntimeWorker, 0, len(workers))
	for _, worker := range workers {
		if worker.Name != "" && worker.Run != nil {
			active = append(active, worker)
		}
	}
	if len(active) == 0 {
		<-ctx.Done()
		return nil
	}
	workerCtx, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()
	results := make(chan teamRuntimeWorkerResult, len(active))
	for _, worker := range active {
		worker := worker
		go func() {
			results <- teamRuntimeWorkerResult{Name: worker.Name, Err: worker.Run(workerCtx)}
		}()
	}

	select {
	case <-ctx.Done():
		cancelWorkers()
		return joinTeamRuntimeWorkerResults(workerCtx, results, len(active), peerJoinTimeout, nil)
	case result := <-results:
		cancelWorkers()
		if ctx.Err() != nil {
			var joined error
			if err := normalizeStoppedLeaseError(workerCtx, result.Err); err != nil {
				joined = fmt.Errorf("%s worker: %w", result.Name, err)
			}
			return joinTeamRuntimeWorkerResults(
				workerCtx, results, len(active)-1, peerJoinTimeout, joined,
			)
		}
		if result.Err == nil {
			result.Err = errors.New("worker stopped unexpectedly")
		}
		joined := fmt.Errorf("%s worker: %w", result.Name, result.Err)
		return joinTeamRuntimeWorkerResults(
			workerCtx, results, len(active)-1, peerJoinTimeout, joined,
		)
	}
}

func joinTeamRuntimeWorkerResults(
	ctx context.Context,
	results <-chan teamRuntimeWorkerResult,
	remaining int,
	timeout time.Duration,
	joined error,
) error {
	if remaining <= 0 {
		return joined
	}
	if timeout <= 0 {
		return errors.Join(joined, errTeamBackgroundWorkerStopTimeout)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for remaining > 0 {
		select {
		case result := <-results:
			remaining--
			if err := normalizeStoppedLeaseError(ctx, result.Err); err != nil {
				joined = errors.Join(joined, fmt.Errorf("%s worker: %w", result.Name, err))
			}
		case <-timer.C:
			return errors.Join(joined, errTeamBackgroundWorkerStopTimeout)
		}
	}
	return joined
}

type teamRuntimeResult struct {
	Err          error
	ReleaseLease bool
}

type teamRequestTracker struct {
	mu        sync.Mutex
	accepting bool
	active    int
	drained   chan struct{}
}

func newTeamRequestTracker() *teamRequestTracker {
	return &teamRequestTracker{accepting: true, drained: make(chan struct{})}
}

func (tracker *teamRequestTracker) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracker.mu.Lock()
		if !tracker.accepting {
			tracker.mu.Unlock()
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		tracker.active++
		tracker.mu.Unlock()
		defer func() {
			tracker.mu.Lock()
			tracker.active--
			if !tracker.accepting && tracker.active == 0 {
				select {
				case <-tracker.drained:
				default:
					close(tracker.drained)
				}
			}
			tracker.mu.Unlock()
		}()
		next.ServeHTTP(w, r)
	})
}

func (tracker *teamRequestTracker) stop() {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.accepting = false
	if tracker.active == 0 {
		select {
		case <-tracker.drained:
		default:
			close(tracker.drained)
		}
	}
}

func (tracker *teamRequestTracker) wait(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-tracker.drained:
		return true
	case <-timer.C:
		return false
	}
}

func serveTeamRuntime(httpSrv *http.Server, listener net.Listener, options teamRuntimeOptions) teamRuntimeResult {
	tracker := newTeamRequestTracker()
	handler := httpSrv.Handler
	if handler == nil {
		handler = http.DefaultServeMux
	}
	httpSrv.Handler = tracker.wrap(handler)
	requestsCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	httpSrv.BaseContext = func(net.Listener) context.Context { return requestsCtx }

	leaseCtx, cancelLease := context.WithCancel(context.Background())
	leaseResult := make(chan error, 1)
	go func() { leaseResult <- options.RenewLease(leaseCtx) }()
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	runBackgroundWorkers := options.RunBackgroundWorkers
	if runBackgroundWorkers == nil {
		runBackgroundWorkers = func(ctx context.Context) error {
			<-ctx.Done()
			return nil
		}
	}
	workerResult := make(chan error, 1)
	go func() { workerResult <- runBackgroundWorkers(workerCtx) }()
	serveResult := make(chan error, 1)
	go func() {
		err := httpSrv.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveResult <- err
	}()

	select {
	case leaseErr := <-leaseResult:
		tracker.stop()
		cancelRequests()
		_ = httpSrv.Close()
		<-serveResult
		_, _ = stopTeamBackgroundWorkers(workerCtx, cancelWorker, workerResult, options.QuiesceTimeout)
		cancelLease()
		tracker.wait(options.QuiesceTimeout)
		if leaseErr == nil {
			leaseErr = errors.New("team writer lease renewal stopped")
		}
		return teamRuntimeResult{Err: fmt.Errorf("team writer lease renewal: %w", leaseErr)}
	case workerErr := <-workerResult:
		tracker.stop()
		cancelRequests()
		_ = httpSrv.Close()
		<-serveResult
		cancelWorker()
		cancelLease()
		_ = normalizeStoppedLeaseError(leaseCtx, <-leaseResult)
		tracker.wait(options.QuiesceTimeout)
		if workerErr == nil {
			workerErr = errors.New("team background workers stopped")
		}
		return teamRuntimeResult{Err: fmt.Errorf("team background workers: %w", workerErr)}
	case serveErr := <-serveResult:
		tracker.stop()
		cancelRequests()
		_ = httpSrv.Close()
		workerErr, _ := stopTeamBackgroundWorkers(
			workerCtx, cancelWorker, workerResult, options.QuiesceTimeout,
		)
		cancelLease()
		leaseErr := normalizeStoppedLeaseError(leaseCtx, <-leaseResult)
		quiesced := tracker.wait(options.QuiesceTimeout)
		if workerErr != nil {
			return teamRuntimeResult{Err: fmt.Errorf("team background workers: %w", workerErr)}
		}
		if leaseErr != nil {
			return teamRuntimeResult{Err: fmt.Errorf("team writer lease renewal: %w", leaseErr)}
		}
		return teamRuntimeResult{Err: serveErr, ReleaseLease: quiesced}
	case <-options.Signals:
		tracker.stop()
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), options.ShutdownTimeout)
		shutdownResult := make(chan error, 1)
		go func() { shutdownResult <- httpSrv.Shutdown(shutdownCtx) }()
		select {
		case leaseErr := <-leaseResult:
			cancelRequests()
			_ = httpSrv.Close()
			<-shutdownResult
			cancelShutdown()
			<-serveResult
			_, _ = stopTeamBackgroundWorkers(
				workerCtx, cancelWorker, workerResult, options.QuiesceTimeout,
			)
			cancelLease()
			tracker.wait(options.QuiesceTimeout)
			if leaseErr == nil {
				leaseErr = errors.New("team writer lease renewal stopped")
			}
			return teamRuntimeResult{Err: fmt.Errorf("team writer lease renewal: %w", leaseErr)}
		case workerErr := <-workerResult:
			cancelRequests()
			_ = httpSrv.Close()
			<-shutdownResult
			cancelShutdown()
			<-serveResult
			cancelWorker()
			cancelLease()
			_ = normalizeStoppedLeaseError(leaseCtx, <-leaseResult)
			tracker.wait(options.QuiesceTimeout)
			if workerErr == nil {
				workerErr = errors.New("team background workers stopped")
			}
			return teamRuntimeResult{Err: fmt.Errorf("team background workers: %w", workerErr)}
		case shutdownErr := <-shutdownResult:
			cancelShutdown()
			if shutdownErr != nil {
				cancelRequests()
				_ = httpSrv.Close()
			}
			<-serveResult
			workerErr, _ := stopTeamBackgroundWorkers(
				workerCtx, cancelWorker, workerResult, options.QuiesceTimeout,
			)
			cancelLease()
			leaseErr := normalizeStoppedLeaseError(leaseCtx, <-leaseResult)
			quiesced := tracker.wait(options.QuiesceTimeout)
			if workerErr != nil {
				return teamRuntimeResult{Err: fmt.Errorf("team background workers: %w", workerErr)}
			}
			if leaseErr != nil {
				return teamRuntimeResult{Err: fmt.Errorf("team writer lease renewal: %w", leaseErr)}
			}
			return teamRuntimeResult{Err: shutdownErr, ReleaseLease: quiesced}
		}
	}
}

func normalizeStoppedLeaseError(ctx context.Context, err error) error {
	if err == nil || (ctx.Err() != nil && errors.Is(err, context.Canceled)) {
		return nil
	}
	return err
}

func stopTeamBackgroundWorkers(
	ctx context.Context,
	cancel context.CancelFunc,
	result <-chan error,
	timeout time.Duration,
) (error, bool) {
	cancel()
	if timeout <= 0 {
		return errTeamBackgroundWorkerStopTimeout, false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-result:
		return normalizeStoppedLeaseError(ctx, err), true
	case <-timer.C:
		return errTeamBackgroundWorkerStopTimeout, false
	}
}
