package teamjobs

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamauth"
)

type projectionStoreFake struct {
	heartbeatMu     sync.Mutex
	claimResults    [][]store.TeamProjectionJobClaim
	claimErrors     []error
	failErrors      []error
	heartbeatErrors []error
	renewalErrors   []error

	claimRequests     []store.TeamProjectionClaimRequest
	failRequests      []store.TeamProjectionFailureRequest
	heartbeatRequests []store.TeamProjectionWorkerHeartbeatRequest
	renewalRequests   []store.TeamProjectionLeaseRenewalRequest
}

func (fake *projectionStoreFake) RenewTeamProjectionJobLease(
	_ context.Context,
	request store.TeamProjectionLeaseRenewalRequest,
) (store.TeamProjectionLeaseRenewalResult, error) {
	fake.renewalRequests = append(fake.renewalRequests, request)
	index := len(fake.renewalRequests) - 1
	if index < len(fake.renewalErrors) {
		return store.TeamProjectionLeaseRenewalResult{}, fake.renewalErrors[index]
	}
	return store.TeamProjectionLeaseRenewalResult{
		JobID: request.JobID, LeaseExpiresAt: time.Now().Add(request.LeaseTTL),
	}, nil
}

var _ ProjectionStore = (*store.Store)(nil)

func (fake *projectionStoreFake) ClaimTeamProjectionJobs(
	_ context.Context,
	request store.TeamProjectionClaimRequest,
) ([]store.TeamProjectionJobClaim, error) {
	fake.claimRequests = append(fake.claimRequests, request)
	index := len(fake.claimRequests) - 1
	if index < len(fake.claimErrors) && fake.claimErrors[index] != nil {
		return nil, fake.claimErrors[index]
	}
	if index < len(fake.claimResults) {
		return append([]store.TeamProjectionJobClaim(nil), fake.claimResults[index]...), nil
	}
	return nil, nil
}

func (fake *projectionStoreFake) FailTeamProjectionJob(
	_ context.Context,
	request store.TeamProjectionFailureRequest,
) error {
	fake.failRequests = append(fake.failRequests, request)
	index := len(fake.failRequests) - 1
	if index < len(fake.failErrors) {
		return fake.failErrors[index]
	}
	return nil
}

func (fake *projectionStoreFake) RecordTeamProjectionWorkerHeartbeat(
	_ context.Context,
	request store.TeamProjectionWorkerHeartbeatRequest,
) error {
	fake.heartbeatMu.Lock()
	defer fake.heartbeatMu.Unlock()
	fake.heartbeatRequests = append(fake.heartbeatRequests, request)
	index := len(fake.heartbeatRequests) - 1
	if index < len(fake.heartbeatErrors) {
		return fake.heartbeatErrors[index]
	}
	return nil
}

type projectionProcessorFake struct {
	errors           []error
	requests         []ProjectionProcessRequest
	dependencyState  string
	dependencyReason string
}

type projectionTestClock struct {
	mu  sync.RWMutex
	now time.Time
}

func (clock *projectionTestClock) Now() time.Time {
	clock.mu.RLock()
	defer clock.mu.RUnlock()
	return clock.now
}

func (clock *projectionTestClock) Set(now time.Time) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = now
}

type slowProjectionStore struct {
	now           time.Time
	queued        []store.TeamProjectionJobClaim
	claimRequests []store.TeamProjectionClaimRequest
	grantedAt     []time.Time
	expiresAt     []time.Time
}

func (storage *slowProjectionStore) ClaimTeamProjectionJobs(
	_ context.Context,
	request store.TeamProjectionClaimRequest,
) ([]store.TeamProjectionJobClaim, error) {
	storage.claimRequests = append(storage.claimRequests, request)
	count := request.Limit
	if count > len(storage.queued) {
		count = len(storage.queued)
	}
	claims := append([]store.TeamProjectionJobClaim(nil), storage.queued[:count]...)
	storage.queued = storage.queued[count:]
	for index := range claims {
		claims[index].LeaseExpiresAt = storage.now.Add(request.LeaseTTL)
		storage.grantedAt = append(storage.grantedAt, storage.now)
		storage.expiresAt = append(storage.expiresAt, claims[index].LeaseExpiresAt)
	}
	return claims, nil
}

func (*slowProjectionStore) FailTeamProjectionJob(
	context.Context,
	store.TeamProjectionFailureRequest,
) error {
	return nil
}

func (*slowProjectionStore) RenewTeamProjectionJobLease(
	_ context.Context,
	request store.TeamProjectionLeaseRenewalRequest,
) (store.TeamProjectionLeaseRenewalResult, error) {
	return store.TeamProjectionLeaseRenewalResult{
		JobID: request.JobID, LeaseExpiresAt: time.Now().Add(request.LeaseTTL),
	}, nil
}

func (*slowProjectionStore) RecordTeamProjectionWorkerHeartbeat(
	context.Context,
	store.TeamProjectionWorkerHeartbeatRequest,
) error {
	return nil
}

func (fake *projectionProcessorFake) ProjectionDependencyHealth() ProjectionDependencyHealth {
	state := fake.dependencyState
	if state == "" {
		state = store.TeamProjectionDependencyReady
	}
	return ProjectionDependencyHealth{State: state, Reason: fake.dependencyReason}
}

func (fake *projectionProcessorFake) ProcessTeamProjection(
	_ context.Context,
	request ProjectionProcessRequest,
) error {
	fake.requests = append(fake.requests, request)
	index := len(fake.requests) - 1
	if index < len(fake.errors) {
		return fake.errors[index]
	}
	return nil
}

func projectionWorkerForTest(
	t *testing.T,
	storage ProjectionStore,
	processor ProjectionProcessor,
) *ProjectionWorker {
	t.Helper()
	worker, err := NewProjectionWorker(ProjectionWorkerConfig{
		Store:     storage,
		Processor: processor,
		Writer: store.TeamWriterLeaseIdentity{
			WriterID: "projection-worker-1", Token: "writer-token-1",
		},
		ProjectionKind:    "graph",
		ClaimLimit:        1,
		LeaseTTL:          30 * time.Second,
		PollInterval:      time.Millisecond,
		HeartbeatInterval: time.Millisecond,
		WorkerInstanceID:  "projection-instance-test",
		BaseBackoff:       2 * time.Second,
		MaxBackoff:        10 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewProjectionWorker: %v", err)
	}
	return worker
}

func TestNewProjectionWorkerRejectsAdvertisedParallelClaimLimit(t *testing.T) {
	_, err := NewProjectionWorker(ProjectionWorkerConfig{
		Store:     &projectionStoreFake{},
		Processor: &projectionProcessorFake{},
		Writer: store.TeamWriterLeaseIdentity{
			WriterID: "projection-worker-1", Token: "writer-token-1",
		},
		ProjectionKind:    "graph",
		ClaimLimit:        2,
		LeaseTTL:          30 * time.Second,
		PollInterval:      time.Millisecond,
		HeartbeatInterval: time.Millisecond,
		WorkerInstanceID:  "projection-instance-test",
		BaseBackoff:       2 * time.Second,
		MaxBackoff:        10 * time.Second,
	})
	if err == nil {
		t.Fatal("projection worker accepted a claim limit that serial processing would ignore")
	}
}

func TestProjectionWorkerHeartbeatReportsDependencyAndCycleStateWithoutContent(t *testing.T) {
	storage := &projectionStoreFake{}
	processor := &projectionProcessorFake{
		dependencyState:  store.TeamProjectionDependencyDegraded,
		dependencyReason: store.TeamProjectionWorkerReasonEmbeddingNotConfigured,
	}
	worker := projectionWorkerForTest(t, storage, processor)
	worker.projectionKind = ""
	if err := worker.recordHeartbeat(context.Background(), store.TeamProjectionWorkerErrorCycleFailed); err != nil {
		t.Fatal(err)
	}
	if len(storage.heartbeatRequests) != 1 {
		t.Fatalf("heartbeat requests = %+v", storage.heartbeatRequests)
	}
	got := storage.heartbeatRequests[0]
	if got.WriterID != "projection-worker-1" || got.WriterToken != "writer-token-1" ||
		got.WorkerInstanceID != "projection-instance-test" ||
		got.DependencyState != store.TeamProjectionDependencyDegraded ||
		got.DependencyReason != store.TeamProjectionWorkerReasonEmbeddingNotConfigured ||
		got.LastErrorCode != store.TeamProjectionWorkerErrorCycleFailed {
		t.Fatalf("heartbeat = %+v", got)
	}
}

func TestProjectionWorkerRunStopsWhenHeartbeatLosesWriterLease(t *testing.T) {
	storage := &projectionStoreFake{heartbeatErrors: []error{
		nil,
		store.ErrTeamWriterLeaseMismatch,
	}}
	worker := projectionWorkerForTest(t, storage, &projectionProcessorFake{})
	err := worker.Run(context.Background())
	if !errors.Is(err, store.ErrTeamWriterLeaseMismatch) {
		t.Fatalf("Run error = %v, want writer lease mismatch", err)
	}
	if len(storage.heartbeatRequests) < 2 {
		t.Fatalf("heartbeat requests = %d, want initial plus lease-loss probe", len(storage.heartbeatRequests))
	}
}

func projectionClaim(jobID, leaseToken string, attempt int) store.TeamProjectionJobClaim {
	return store.TeamProjectionJobClaim{
		JobID: jobID, StoreID: "store-1", TeamID: "team-1",
		RootObjectID: "root-1", RootGeneration: 7,
		ScopeType: "project", ScopeID: "pulse", ProjectionKind: "graph",
		AttemptCount: attempt, LeaseToken: leaseToken,
		LeaseExpiresAt: time.Now().Add(time.Minute),
	}
}

func TestProjectionWorkerRunOnceClaimsProcessesAndDeduplicatesBoundLease(t *testing.T) {
	first := projectionClaim("projection-1", "lease-1", 2)
	duplicate := first
	second := projectionClaim("projection-2", "lease-2", 1)
	second.RootObjectID = "root-2"
	second.RootGeneration = 9
	storage := &projectionStoreFake{
		claimResults: [][]store.TeamProjectionJobClaim{{first, duplicate, second}},
	}
	processor := &projectionProcessorFake{}
	worker := projectionWorkerForTest(t, storage, processor)

	result, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Claimed != 3 || result.Completed != 2 || result.Duplicates != 1 ||
		result.Failed != 0 || result.Stale != 0 {
		t.Fatalf("result = %+v", result)
	}
	if !reflect.DeepEqual(storage.claimRequests, []store.TeamProjectionClaimRequest{{
		WriterID: "projection-worker-1", WriterToken: "writer-token-1",
		ProjectionKind: "graph", Limit: 1, LeaseTTL: 30 * time.Second,
	}}) {
		t.Fatalf("claim requests = %+v", storage.claimRequests)
	}
	want := []ProjectionProcessRequest{
		{Writer: store.TeamWriterLeaseIdentity{WriterID: "projection-worker-1", Token: "writer-token-1"}, Claim: first},
		{Writer: store.TeamWriterLeaseIdentity{WriterID: "projection-worker-1", Token: "writer-token-1"}, Claim: second},
	}
	if !reflect.DeepEqual(processor.requests, want) {
		t.Fatalf("processor requests = %+v", processor.requests)
	}
}

func TestProjectionWorkerClaimsOneSoSlowSerialWorkCannotExpireAQueuedLease(t *testing.T) {
	storage := &slowProjectionStore{
		now: time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC),
		queued: []store.TeamProjectionJobClaim{
			projectionClaim("projection-slow-first", "lease-slow-first", 1),
			projectionClaim("projection-slow-second", "lease-slow-second", 1),
		},
	}
	processor := ProjectionProcessorFunc(func(
		_ context.Context,
		_ ProjectionProcessRequest,
	) error {
		storage.now = storage.now.Add(45 * time.Second)
		return nil
	})
	worker := projectionWorkerForTest(t, storage, processor)

	first, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Claimed != 1 || first.Completed != 1 || len(storage.queued) != 1 {
		t.Fatalf("first slow cycle = %+v queued=%d", first, len(storage.queued))
	}
	second, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Claimed != 1 || second.Completed != 1 || len(storage.queued) != 0 {
		t.Fatalf("second slow cycle = %+v queued=%d", second, len(storage.queued))
	}
	if len(storage.grantedAt) != 2 || !storage.grantedAt[1].After(storage.expiresAt[0]) ||
		!storage.expiresAt[1].After(storage.grantedAt[1]) {
		t.Fatalf("lease windows overlap unsafe serial work: granted=%v expires=%v", storage.grantedAt, storage.expiresAt)
	}
}

func TestProjectionWorkerPersistsOnlyClassifiedGenericFailuresWithBoundedBackoff(t *testing.T) {
	claims := []store.TeamProjectionJobClaim{
		projectionClaim("projection-timeout", "lease-timeout", 1),
		projectionClaim("projection-materialization", "lease-materialization", 3),
		projectionClaim("projection-unknown", "lease-unknown", 20),
	}
	storage := &projectionStoreFake{claimResults: [][]store.TeamProjectionJobClaim{claims}}
	processor := &projectionProcessorFake{errors: []error{
		ProjectionProcessError{
			Code: store.TeamProjectionFailureDependencyTimeout,
			Err:  errors.New("provider request included sensitive internal detail"),
		},
		store.ErrProjectionMaterializationFailed,
		errors.New("unclassified processor detail must not persist"),
	}}
	worker := projectionWorkerForTest(t, storage, processor)

	result, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Claimed != 3 || result.Failed != 3 || result.Completed != 0 || result.Stale != 0 {
		t.Fatalf("result = %+v", result)
	}
	want := []store.TeamProjectionFailureRequest{
		{WriterID: "projection-worker-1", WriterToken: "writer-token-1", JobID: "projection-timeout", LeaseToken: "lease-timeout", ErrorCode: store.TeamProjectionFailureDependencyTimeout, Backoff: 2 * time.Second},
		{WriterID: "projection-worker-1", WriterToken: "writer-token-1", JobID: "projection-materialization", LeaseToken: "lease-materialization", ErrorCode: store.TeamProjectionFailureMaterialization, Backoff: 8 * time.Second},
		{WriterID: "projection-worker-1", WriterToken: "writer-token-1", JobID: "projection-unknown", LeaseToken: "lease-unknown", ErrorCode: store.TeamProjectionFailureTemporary, Backoff: 10 * time.Second},
	}
	if !reflect.DeepEqual(storage.failRequests, want) {
		t.Fatalf("failure requests = %+v", storage.failRequests)
	}
}

func TestProjectionWorkerTreatsLeaseGenerationAndDeletionRacesAsStale(t *testing.T) {
	for _, name := range []string{"lease_expired", "generation_changed", "root_deleted"} {
		t.Run(name, func(t *testing.T) {
			claim := projectionClaim("projection-"+name, "lease-"+name, 1)
			storage := &projectionStoreFake{claimResults: [][]store.TeamProjectionJobClaim{{claim}}}
			processor := &projectionProcessorFake{errors: []error{store.ErrConcealedNotFound}}
			worker := projectionWorkerForTest(t, storage, processor)

			result, err := worker.RunOnce(context.Background())
			if err != nil {
				t.Fatalf("RunOnce: %v", err)
			}
			if result.Claimed != 1 || result.Stale != 1 || result.Completed != 0 || result.Failed != 0 {
				t.Fatalf("result = %+v", result)
			}
			if len(storage.failRequests) != 0 {
				t.Fatalf("stale claim was reclassified: %+v", storage.failRequests)
			}
		})
	}
}

func TestProjectionWorkerResponseLossSettlesAsStaleWithoutDuplicateFailure(t *testing.T) {
	claim := projectionClaim("projection-response-loss", "lease-response-loss", 1)
	storage := &projectionStoreFake{
		claimResults: [][]store.TeamProjectionJobClaim{{claim}, nil},
		// Completion committed, but its response was lost. The attempted failure
		// transition therefore observes a terminal/stale lease.
		failErrors: []error{store.ErrConcealedNotFound},
	}
	firstProcessor := &projectionProcessorFake{errors: []error{errors.New("completion response lost")}}
	firstWorker := projectionWorkerForTest(t, storage, firstProcessor)

	first, err := firstWorker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	if first.Stale != 1 || first.Failed != 0 {
		t.Fatalf("first result = %+v", first)
	}

	secondProcessor := &projectionProcessorFake{}
	secondWorker := projectionWorkerForTest(t, storage, secondProcessor)
	second, err := secondWorker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("restart RunOnce: %v", err)
	}
	if second.Claimed != 0 || second.Completed != 0 || second.Failed != 0 || second.Stale != 0 {
		t.Fatalf("restart result = %+v", second)
	}
	if len(secondProcessor.requests) != 0 || len(storage.failRequests) != 1 {
		t.Fatalf("restart process=%+v fail=%+v", secondProcessor.requests, storage.failRequests)
	}
}

func TestProjectionWorkerRestartReclaimsExpiredLeaseWithFreshToken(t *testing.T) {
	oldClaim := projectionClaim("projection-restart", "lease-before-restart", 1)
	newClaim := projectionClaim("projection-restart", "lease-after-restart", 2)
	storage := &projectionStoreFake{
		claimResults: [][]store.TeamProjectionJobClaim{{oldClaim}, {newClaim}},
	}
	firstWorker := projectionWorkerForTest(t, storage, &projectionProcessorFake{
		errors: []error{store.ErrConcealedNotFound},
	})
	first, err := firstWorker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("expired lease RunOnce: %v", err)
	}
	if first.Stale != 1 || first.Completed != 0 || first.Failed != 0 {
		t.Fatalf("expired lease result = %+v", first)
	}

	restartProcessor := &projectionProcessorFake{}
	restarted := projectionWorkerForTest(t, storage, restartProcessor)
	second, err := restarted.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("restart RunOnce: %v", err)
	}
	if second.Completed != 1 || second.Failed != 0 || second.Stale != 0 {
		t.Fatalf("restart result = %+v", second)
	}
	if len(restartProcessor.requests) != 1 ||
		restartProcessor.requests[0].Claim.LeaseToken != "lease-after-restart" ||
		restartProcessor.requests[0].Claim.AttemptCount != 2 {
		t.Fatalf("restart processor requests = %+v", restartProcessor.requests)
	}
}

func TestProjectionWorkerIntegratesWithDurableClaimAndGenerationFencedCompletion(t *testing.T) {
	now := time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC)
	root := teamauth.BootstrapRoot{
		Issuer:        "https://projection-worker.example",
		Subject:       "projection-owner",
		AdminClientID: "projection-admin",
	}
	teamStore, err := store.OpenTeam(filepath.Join(t.TempDir(), "team.db"), store.TeamOpenOptions{
		ExpectedBootstrapRoot: root,
		Clock:                 func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("OpenTeam: %v", err)
	}
	defer teamStore.Close()
	bootstrap, err := teamStore.BootstrapTeam(context.Background(), store.BootstrapTeamRequest{
		TeamName: "Projection worker integration", PresentedRoot: root,
	})
	if err != nil {
		t.Fatalf("BootstrapTeam: %v", err)
	}
	lease, err := teamStore.AcquireTeamWriterLease(context.Background(), store.TeamWriterLeaseRequest{
		WriterID: "projection-integration-worker", WriterVersion: teamauth.SchemaVersion,
		TTL: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("AcquireTeamWriterLease: %v", err)
	}
	nowText := now.Format(time.RFC3339Nano)
	if _, err := teamStore.DB().Exec(`
		INSERT INTO team_object_registry(
			object_id, store_id, team_id, object_kind, scope_type, scope_id,
			owner_principal_id, author_principal_id, privacy_tier, retention,
			lifecycle, generation, created_at, updated_at)
		VALUES ('projection-root', ?, ?, 'memory', 'personal', ?, ?, ?,
		        'normal', 'project', 'active', 1, ?, ?)`,
		bootstrap.StoreID, bootstrap.TeamID, bootstrap.OwnerPrincipalID,
		bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID, nowText, nowText,
	); err != nil {
		t.Fatalf("insert root: %v", err)
	}
	if _, err := teamStore.DB().Exec(`
		INSERT INTO team_projection_jobs(
			job_id, store_id, team_id, root_object_id, root_generation,
			scope_type, scope_id, projection_kind, state, attempt_count,
			next_attempt_at, created_at, updated_at)
		VALUES ('projection-integration', ?, ?, 'projection-root', 1,
		        'personal', ?, 'graph', 'pending', 0, ?, ?, ?)`,
		bootstrap.StoreID, bootstrap.TeamID, bootstrap.OwnerPrincipalID,
		nowText, nowText, nowText,
	); err != nil {
		t.Fatalf("insert job: %v", err)
	}

	processor := ProjectionProcessorFunc(func(ctx context.Context, request ProjectionProcessRequest) error {
		_, err := teamStore.CompleteTeamProjectionJob(ctx, store.TeamProjectionCompletionRequest{
			WriterID: request.Writer.WriterID, WriterToken: request.Writer.Token,
			JobID: request.Claim.JobID, LeaseToken: request.Claim.LeaseToken,
		})
		return err
	})
	worker, err := NewProjectionWorker(ProjectionWorkerConfig{
		Store: teamStore, Processor: processor,
		Writer:         store.TeamWriterLeaseIdentity{WriterID: lease.WriterID, Token: lease.Token},
		ProjectionKind: "graph", ClaimLimit: 1, LeaseTTL: time.Minute,
		PollInterval: time.Millisecond, HeartbeatInterval: time.Millisecond,
		WorkerInstanceID: "projection-integration-instance",
		BaseBackoff:      time.Second, MaxBackoff: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewProjectionWorker: %v", err)
	}
	result, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Claimed != 1 || result.Completed != 1 || result.Failed != 0 || result.Stale != 0 {
		t.Fatalf("result = %+v", result)
	}
	var state string
	var generation int64
	if err := teamStore.DB().QueryRow(`
		SELECT job.state, root.generation
		  FROM team_projection_jobs job
		  JOIN team_object_registry root ON root.object_id = job.root_object_id
		 WHERE job.job_id = 'projection-integration'`).Scan(&state, &generation); err != nil {
		t.Fatalf("read completed job: %v", err)
	}
	if state != "ready" || generation != 1 {
		t.Fatalf("state=%q generation=%d", state, generation)
	}
	restartResult, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("restart RunOnce: %v", err)
	}
	if restartResult.Claimed != 0 {
		t.Fatalf("ready job reclaimed after restart: %+v", restartResult)
	}
}

func TestProjectionWorkerRenewsDurableLeaseDuringLongProcessing(t *testing.T) {
	base := time.Date(2026, 7, 15, 2, 0, 0, 0, time.UTC)
	clock := &projectionTestClock{now: base}
	root := teamauth.BootstrapRoot{
		Issuer:        "https://projection-renewal.example",
		Subject:       "projection-owner",
		AdminClientID: "projection-admin",
	}
	teamStore, err := store.OpenTeam(filepath.Join(t.TempDir(), "team.db"), store.TeamOpenOptions{
		ExpectedBootstrapRoot: root,
		Clock:                 clock.Now,
	})
	if err != nil {
		t.Fatalf("OpenTeam: %v", err)
	}
	defer teamStore.Close()
	bootstrap, err := teamStore.BootstrapTeam(context.Background(), store.BootstrapTeamRequest{
		TeamName: "Projection renewal", PresentedRoot: root,
	})
	if err != nil {
		t.Fatalf("BootstrapTeam: %v", err)
	}
	lease, err := teamStore.AcquireTeamWriterLease(context.Background(), store.TeamWriterLeaseRequest{
		WriterID: "projection-renewal-worker", WriterVersion: teamauth.SchemaVersion,
		TTL: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("AcquireTeamWriterLease: %v", err)
	}
	nowText := base.Format(time.RFC3339Nano)
	if _, err := teamStore.DB().Exec(`
		INSERT INTO team_object_registry(
			object_id, store_id, team_id, object_kind, scope_type, scope_id,
			owner_principal_id, author_principal_id, privacy_tier, retention,
			lifecycle, generation, created_at, updated_at)
		VALUES ('projection-renewal-root', ?, ?, 'memory', 'personal', ?, ?, ?,
		        'normal', 'project', 'active', 1, ?, ?)`,
		bootstrap.StoreID, bootstrap.TeamID, bootstrap.OwnerPrincipalID,
		bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID, nowText, nowText,
	); err != nil {
		t.Fatalf("insert root: %v", err)
	}
	if _, err := teamStore.DB().Exec(`
		INSERT INTO team_projection_jobs(
			job_id, store_id, team_id, root_object_id, root_generation,
			scope_type, scope_id, projection_kind, state, attempt_count,
			next_attempt_at, created_at, updated_at)
		VALUES ('projection-renewal-job', ?, ?, 'projection-renewal-root', 1,
		        'personal', ?, 'graph', 'pending', 0, ?, ?, ?)`,
		bootstrap.StoreID, bootstrap.TeamID, bootstrap.OwnerPrincipalID,
		nowText, nowText, nowText,
	); err != nil {
		t.Fatalf("insert job: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	processor := ProjectionProcessorFunc(func(ctx context.Context, request ProjectionProcessRequest) error {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		_, err := teamStore.CompleteTeamProjectionJob(ctx, store.TeamProjectionCompletionRequest{
			WriterID: request.Writer.WriterID, WriterToken: request.Writer.Token,
			JobID: request.Claim.JobID, LeaseToken: request.Claim.LeaseToken,
		})
		return err
	})
	const leaseTTL = 30 * time.Millisecond
	worker, err := NewProjectionWorker(ProjectionWorkerConfig{
		Store: teamStore, Processor: processor,
		Writer:         store.TeamWriterLeaseIdentity{WriterID: lease.WriterID, Token: lease.Token},
		ProjectionKind: "graph", ClaimLimit: 1, LeaseTTL: leaseTTL,
		PollInterval: time.Millisecond, HeartbeatInterval: time.Millisecond,
		WorkerInstanceID: "projection-renewal-instance",
		BaseBackoff:      time.Second, MaxBackoff: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewProjectionWorker: %v", err)
	}
	type runResult struct {
		result ProjectionRunResult
		err    error
	}
	done := make(chan runResult, 1)
	go func() {
		result, err := worker.RunOnce(context.Background())
		done <- runResult{result: result, err: err}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("processor did not start")
	}

	originalExpiry := base.Add(leaseTTL)
	clock.Set(base.Add(20 * time.Millisecond))
	renewed := false
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var expiryText string
		if err := teamStore.DB().QueryRow(`
			SELECT lease_expires_at FROM team_projection_jobs
			 WHERE job_id = 'projection-renewal-job'`).Scan(&expiryText); err != nil {
			t.Fatalf("read lease expiry: %v", err)
		}
		expires, err := time.Parse(time.RFC3339Nano, expiryText)
		if err != nil {
			t.Fatalf("parse lease expiry: %v", err)
		}
		if expires.After(originalExpiry) {
			renewed = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	clock.Set(base.Add(40 * time.Millisecond))
	close(release)
	outcome := <-done
	if !renewed {
		t.Fatal("durable projection lease was not renewed during processing")
	}
	if outcome.err != nil {
		t.Fatalf("RunOnce: %v", outcome.err)
	}
	if outcome.result.Completed != 1 || outcome.result.Stale != 0 || outcome.result.Failed != 0 {
		t.Fatalf("result after original lease window = %+v", outcome.result)
	}
}

func TestProjectionWorkerTreatsWriterLeaseLossAsFatal(t *testing.T) {
	for _, test := range []struct {
		name                   string
		storage                *projectionStoreFake
		processor              *projectionProcessorFake
		wantFailureTransitions int
	}{
		{
			name:      "claim",
			storage:   &projectionStoreFake{claimErrors: []error{store.ErrTeamWriterLeaseMismatch}},
			processor: &projectionProcessorFake{},
		},
		{
			name: "complete",
			storage: &projectionStoreFake{claimResults: [][]store.TeamProjectionJobClaim{{
				projectionClaim("projection-writer-lost", "lease-writer-lost", 1),
			}}},
			processor: &projectionProcessorFake{errors: []error{store.ErrTeamWriterLeaseMismatch}},
		},
		{
			name: "failure_transition",
			storage: &projectionStoreFake{
				claimResults: [][]store.TeamProjectionJobClaim{{
					projectionClaim("projection-fail-writer-lost", "lease-fail-writer-lost", 1),
				}},
				failErrors: []error{store.ErrTeamWriterLeaseMismatch},
			},
			processor:              &projectionProcessorFake{errors: []error{errors.New("projection failed")}},
			wantFailureTransitions: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker := projectionWorkerForTest(t, test.storage, test.processor)
			_, err := worker.RunOnce(context.Background())
			if !errors.Is(err, store.ErrTeamWriterLeaseMismatch) {
				t.Fatalf("RunOnce error = %v", err)
			}
			if len(test.storage.failRequests) != test.wantFailureTransitions {
				t.Fatalf("failure transitions = %+v, want %d", test.storage.failRequests, test.wantFailureTransitions)
			}
		})
	}
}

func TestProjectionWorkerRunRetriesTransientClaimFailureAndStopsOnWriterLeaseLoss(t *testing.T) {
	storage := &projectionStoreFake{claimErrors: []error{
		errors.New("synthetic transient outage"),
		store.ErrTeamWriterLeaseMismatch,
	}}
	worker := projectionWorkerForTest(t, storage, &projectionProcessorFake{})

	err := worker.Run(context.Background())
	if !errors.Is(err, store.ErrTeamWriterLeaseMismatch) {
		t.Fatalf("Run error = %v", err)
	}
	if len(storage.claimRequests) != 2 {
		t.Fatalf("claim attempts = %d, want 2", len(storage.claimRequests))
	}
}

func TestProjectionWorkerRunStopsCleanlyOnContextCancellation(t *testing.T) {
	storage := &projectionStoreFake{}
	processor := &projectionProcessorFake{}
	worker := projectionWorkerForTest(t, storage, processor)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := worker.Run(ctx); err != nil {
		t.Fatalf("canceled Run error = %v", err)
	}
	if len(storage.claimRequests) != 0 || len(processor.requests) != 0 {
		t.Fatalf("canceled worker touched dependencies: claim=%d process=%d", len(storage.claimRequests), len(processor.requests))
	}
}
