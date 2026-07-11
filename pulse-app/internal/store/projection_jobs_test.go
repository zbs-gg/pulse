package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

type projectionLifecycleFixture struct {
	store     *Store
	bootstrap BootstrapResult
	path      string
	root      teamauth.BootstrapRoot
	now       time.Time
}

func newProjectionLifecycleFixture(t *testing.T) *projectionLifecycleFixture {
	t.Helper()
	fixture := &projectionLifecycleFixture{
		path: filepath.Join(t.TempDir(), "team.db"), root: testBootstrapRoot(),
		now: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC),
	}
	fixture.reopen(t)
	result, err := fixture.store.BootstrapTeam(context.Background(), BootstrapTeamRequest{
		TeamName: "Projection lifecycle", PresentedRoot: fixture.root,
	})
	if err != nil {
		fixture.store.Close()
		t.Fatal(err)
	}
	fixture.bootstrap = result
	return fixture
}

func (fixture *projectionLifecycleFixture) reopen(t *testing.T) {
	t.Helper()
	store, err := OpenTeam(fixture.path, TeamOpenOptions{
		ExpectedBootstrapRoot: fixture.root,
		Clock:                 func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.store = store
}

func (fixture *projectionLifecycleFixture) acquireWriter(t *testing.T) TeamWriterLease {
	t.Helper()
	lease, err := fixture.store.AcquireTeamWriterLease(context.Background(), TeamWriterLeaseRequest{
		WriterID: "projection-worker", WriterVersion: teamauth.SchemaVersion, TTL: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func insertProjectionLifecycleRoot(t *testing.T, fixture *projectionLifecycleFixture, objectID string) {
	t.Helper()
	insertPolicyObject(t, fixture.store, fixture.bootstrap, objectID, "personal", fixture.bootstrap.OwnerPrincipalID, fixture.bootstrap.OwnerPrincipalID)
}

func insertProjectionSessionRoot(t *testing.T, fixture *projectionLifecycleFixture, objectID string, expires time.Time) {
	t.Helper()
	if _, err := fixture.store.DB().Exec(`
		INSERT INTO team_object_registry(
			object_id, store_id, team_id, object_kind, scope_type, scope_id,
			owner_principal_id, author_principal_id, privacy_tier, retention,
			lifecycle, generation, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, 'memory', 'session', ?, ?, ?, 'normal', 'session',
		        'active', 1, ?, ?, ?)`,
		objectID, fixture.bootstrap.StoreID, fixture.bootstrap.TeamID, objectID,
		fixture.bootstrap.OwnerPrincipalID, fixture.bootstrap.OwnerPrincipalID,
		expires.UTC().Format(time.RFC3339Nano), fixture.now.UTC().Format(time.RFC3339Nano),
		fixture.now.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert session root %s: %v", objectID, err)
	}
}

func insertProjectionLifecycleJob(t *testing.T, fixture *projectionLifecycleFixture, jobID, rootID, kind, state string, attempts int, due time.Time) {
	t.Helper()
	var scopeType, scopeID string
	if err := fixture.store.DB().QueryRow(`
		SELECT scope_type, scope_id FROM team_object_registry WHERE object_id = ?`, rootID).Scan(&scopeType, &scopeID); err != nil {
		t.Fatalf("load projection root %s: %v", rootID, err)
	}
	var nextAttempt any
	if state == "pending" || state == "failed" {
		nextAttempt = due.UTC().Format(time.RFC3339Nano)
	}
	var lastError any
	var terminalHash any
	if state == "failed" {
		lastError = TeamProjectionFailureTemporary
		terminalHash = strings.Repeat("b", 64)
	}
	if _, err := fixture.store.DB().Exec(`
		INSERT INTO team_projection_jobs(
			job_id, store_id, team_id, root_object_id, root_generation,
			scope_type, scope_id, projection_kind, state, attempt_count,
			lease_token_hash, terminal_lease_token_hash, lease_expires_at, next_attempt_at, last_error_code,
			created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, NULL, ?, NULL, ?, ?, ?, ?)`,
		jobID, fixture.bootstrap.StoreID, fixture.bootstrap.TeamID, rootID,
		scopeType, scopeID, kind, state, attempts, terminalHash, nextAttempt, lastError,
		fixture.now.UTC().Format(time.RFC3339Nano), fixture.now.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert projection job %s: %v", jobID, err)
	}
}

func installProjectionTestMaterialization(t *testing.T, fixture *projectionLifecycleFixture) {
	t.Helper()
	if _, err := fixture.store.DB().Exec(`
		CREATE TABLE IF NOT EXISTS projection_test_materialization(
			job_id TEXT NOT NULL,
			object_id TEXT NOT NULL,
			PRIMARY KEY(job_id, object_id)
		)`); err != nil {
		t.Fatal(err)
	}
}

func projectionTestMaterializer(
	ctx context.Context,
	writer teamProjectionContentWriter,
	completion teamProjectionCompletionContext,
) error {
	for _, output := range completion.Outputs {
		if _, err := writer.ExecContext(ctx, `
			INSERT INTO projection_test_materialization(job_id, object_id)
			VALUES (?, ?)`, completion.JobID, output.DerivativeObjectID); err != nil {
			return err
		}
	}
	return nil
}

func TestClaimTeamProjectionJobsIsAtomicBoundedAndStoresOnlyTokenHashes(t *testing.T) {
	fixture := newProjectionLifecycleFixture(t)
	defer fixture.store.Close()
	writer := fixture.acquireWriter(t)
	for index := 0; index < 8; index++ {
		rootID := "claim-root-" + string(rune('a'+index))
		jobID := "claim-job-" + string(rune('a'+index))
		insertProjectionLifecycleRoot(t, fixture, rootID)
		insertProjectionLifecycleJob(t, fixture, jobID, rootID, "embedding", "pending", 0, fixture.now.Add(-time.Second))
	}
	insertProjectionLifecycleRoot(t, fixture, "future-root")
	insertProjectionLifecycleJob(t, fixture, "future-job", "future-root", "embedding", "pending", 0, fixture.now.Add(time.Minute))

	request := TeamProjectionClaimRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		ProjectionKind: "embedding", Limit: 4, LeaseTTL: 30 * time.Second,
	}
	type result struct {
		claims []TeamProjectionJobClaim
		err    error
	}
	results := make(chan result, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			claims, err := fixture.store.ClaimTeamProjectionJobs(context.Background(), request)
			results <- result{claims: claims, err: err}
		}()
	}
	wait.Wait()
	close(results)
	seen := make(map[string]bool)
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if len(result.claims) != 4 {
			t.Fatalf("claim batch = %d, want 4", len(result.claims))
		}
		for _, claim := range result.claims {
			if seen[claim.JobID] {
				t.Fatalf("job %s claimed twice", claim.JobID)
			}
			seen[claim.JobID] = true
			if claim.LeaseToken == "" || claim.AttemptCount != 1 || !claim.LeaseExpiresAt.Equal(fixture.now.Add(30*time.Second)) {
				t.Fatalf("claim = %+v", claim)
			}
			var hash string
			if err := fixture.store.DB().QueryRow(`SELECT lease_token_hash FROM team_projection_jobs WHERE job_id = ?`, claim.JobID).Scan(&hash); err != nil {
				t.Fatal(err)
			}
			if len(hash) != 64 || hash == claim.LeaseToken || !projectionLeaseTokenMatches(hash, claim.LeaseToken) {
				t.Fatalf("unsafe lease persistence: raw=%q hash=%q", claim.LeaseToken, hash)
			}
		}
	}
	if len(seen) != 8 || seen["future-job"] {
		t.Fatalf("claimed jobs = %v", seen)
	}
	for _, invalid := range []TeamProjectionClaimRequest{
		{WriterID: writer.WriterID, WriterToken: writer.Token, Limit: 0, LeaseTTL: time.Second},
		{WriterID: writer.WriterID, WriterToken: writer.Token, Limit: 65, LeaseTTL: time.Second},
		{WriterID: writer.WriterID, WriterToken: writer.Token, Limit: 1, LeaseTTL: 0},
		{WriterID: writer.WriterID, WriterToken: writer.Token, ProjectionKind: "raw prompt", Limit: 1, LeaseTTL: time.Second},
	} {
		if _, err := fixture.store.ClaimTeamProjectionJobs(context.Background(), invalid); !errors.Is(err, ErrInvalidProjectionJobRequest) {
			t.Fatalf("invalid claim error = %v", err)
		}
	}
}

func TestProjectionLeaseTokenIsFreshRandom256BitAndNotWriterDerived(t *testing.T) {
	fixture := newProjectionLifecycleFixture(t)
	defer fixture.store.Close()
	writer := fixture.acquireWriter(t)
	insertProjectionLifecycleRoot(t, fixture, "random-lease-root")
	insertProjectionLifecycleJob(t, fixture, "random-lease-job", "random-lease-root", "embedding", "pending", 0, fixture.now.Add(-time.Second))

	claims, err := fixture.store.ClaimTeamProjectionJobs(context.Background(), TeamProjectionClaimRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		ProjectionKind: "embedding", Limit: 1, LeaseTTL: time.Second,
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim = %+v, %v", claims, err)
	}
	claim := claims[0]
	mac := hmac.New(sha256.New, []byte(writer.Token))
	_, _ = mac.Write([]byte("pulse-projection-job-lease-raw-v1\x00"))
	_, _ = mac.Write([]byte(claim.JobID))
	_, _ = mac.Write([]byte("\x00"))
	_, _ = mac.Write([]byte(strconv.Itoa(claim.AttemptCount)))
	predictable := "projection_lease_" + hex.EncodeToString(mac.Sum(nil))
	if claim.LeaseToken == predictable {
		t.Fatal("projection lease is derivable from the shared writer credential")
	}
	const prefix = "projection_lease_"
	if !strings.HasPrefix(claim.LeaseToken, prefix) {
		t.Fatalf("lease token prefix = %q", claim.LeaseToken)
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(claim.LeaseToken, prefix))
	if err != nil || len(raw) != 32 {
		t.Fatalf("lease entropy = %d bytes, %v", len(raw), err)
	}
	var stored string
	if err := fixture.store.DB().QueryRow(`SELECT lease_token_hash FROM team_projection_jobs WHERE job_id = ?`, claim.JobID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == claim.LeaseToken || !projectionLeaseTokenMatches(stored, claim.LeaseToken) {
		t.Fatalf("raw lease persisted: raw=%q stored=%q", claim.LeaseToken, stored)
	}
}

func TestProjectionClaimOrdersMixedRFC3339PrecisionByInstant(t *testing.T) {
	fixture := newProjectionLifecycleFixture(t)
	defer fixture.store.Close()
	writer := fixture.acquireWriter(t)
	insertProjectionLifecycleRoot(t, fixture, "order-root-exact")
	insertProjectionLifecycleRoot(t, fixture, "order-root-fractional")
	insertProjectionLifecycleJob(t, fixture, "order-job-exact", "order-root-exact", "ordering", "pending", 0,
		time.Date(2026, 7, 11, 11, 59, 59, 0, time.UTC))
	insertProjectionLifecycleJob(t, fixture, "order-job-fractional", "order-root-fractional", "ordering", "pending", 0,
		time.Date(2026, 7, 11, 11, 59, 59, 900_000_000, time.UTC))

	claims, err := fixture.store.ClaimTeamProjectionJobs(context.Background(), TeamProjectionClaimRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		ProjectionKind: "ordering", Limit: 2, LeaseTTL: time.Minute,
	})
	if err != nil || len(claims) != 2 {
		t.Fatalf("ordered claims = %+v, %v", claims, err)
	}
	if claims[0].JobID != "order-job-exact" || claims[1].JobID != "order-job-fractional" {
		t.Fatalf("claim order = %s, %s", claims[0].JobID, claims[1].JobID)
	}
}

func TestProjectionClaimRollsBackBatchWhenWriterLeaseExpiresBeforeCommit(t *testing.T) {
	fixture := newProjectionLifecycleFixture(t)
	defer fixture.store.Close()
	writer := fixture.acquireWriter(t)
	insertProjectionLifecycleRoot(t, fixture, "late-writer-root")
	insertProjectionLifecycleJob(t, fixture, "late-writer-job", "late-writer-root", "embedding", "pending", 0, fixture.now.Add(-time.Second))

	base := fixture.now
	clockCalls := 0
	fixture.store.clock = func() time.Time {
		clockCalls++
		if clockCalls >= 3 {
			return base.Add(6 * time.Minute)
		}
		return base
	}
	if _, err := fixture.store.ClaimTeamProjectionJobs(context.Background(), TeamProjectionClaimRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		ProjectionKind: "embedding", Limit: 1, LeaseTTL: time.Minute,
	}); !errors.Is(err, ErrTeamWriterLeaseMismatch) {
		t.Fatalf("late writer claim error = %v", err)
	}
	var state string
	var attempts int
	var hash any
	if err := fixture.store.DB().QueryRow(`
		SELECT state, attempt_count, lease_token_hash
		  FROM team_projection_jobs WHERE job_id = 'late-writer-job'`).Scan(&state, &attempts, &hash); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || attempts != 0 || hash != nil {
		t.Fatalf("late writer partially claimed job: state=%q attempts=%d hash=%v", state, attempts, hash)
	}
}

func TestProjectionLeaseFailureExpiryRetryAndRestart(t *testing.T) {
	fixture := newProjectionLifecycleFixture(t)
	writer := fixture.acquireWriter(t)
	insertProjectionLifecycleRoot(t, fixture, "retry-root")
	insertProjectionLifecycleJob(t, fixture, "retry-job", "retry-root", "claim", "pending", 0, fixture.now.Add(-time.Second))
	claimRequest := TeamProjectionClaimRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		ProjectionKind: "claim", Limit: 1, LeaseTTL: 20 * time.Second,
	}
	claims, err := fixture.store.ClaimTeamProjectionJobs(context.Background(), claimRequest)
	if err != nil || len(claims) != 1 {
		t.Fatalf("first claim = %+v, %v", claims, err)
	}
	first := claims[0]
	wrong := TeamProjectionFailureRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token, JobID: first.JobID,
		LeaseToken: "wrong-token", ErrorCode: "dependency_timeout", Backoff: 30 * time.Second,
	}
	if err := fixture.store.FailTeamProjectionJob(context.Background(), wrong); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("wrong lease failure error = %v", err)
	}
	invalidFailure := wrong
	invalidFailure.ErrorCode = "raw_prompt_path"
	if err := fixture.store.FailTeamProjectionJob(context.Background(), invalidFailure); !errors.Is(err, ErrInvalidProjectionJobRequest) {
		t.Fatalf("unbounded failure class error = %v", err)
	}
	invalidFailure.ErrorCode = "dependency_timeout"
	invalidFailure.Backoff = 0
	if err := fixture.store.FailTeamProjectionJob(context.Background(), invalidFailure); !errors.Is(err, ErrInvalidProjectionJobRequest) {
		t.Fatalf("zero retry backoff error = %v", err)
	}
	invalidFailure.Backoff = 25 * time.Hour
	if err := fixture.store.FailTeamProjectionJob(context.Background(), invalidFailure); !errors.Is(err, ErrInvalidProjectionJobRequest) {
		t.Fatalf("unbounded retry backoff error = %v", err)
	}
	wrong.LeaseToken = first.LeaseToken
	if err := fixture.store.FailTeamProjectionJob(context.Background(), wrong); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	var state string
	var leaseHash, leaseExpiry any
	var nextAttempt, errorCode string
	if err := fixture.store.DB().QueryRow(`
		SELECT state, lease_token_hash, lease_expires_at, next_attempt_at, last_error_code
		  FROM team_projection_jobs WHERE job_id = 'retry-job'`).Scan(
		&state, &leaseHash, &leaseExpiry, &nextAttempt, &errorCode); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || leaseHash != nil || leaseExpiry != nil || errorCode != "dependency_timeout" ||
		nextAttempt != fixture.now.Add(30*time.Second).Format(time.RFC3339Nano) {
		t.Fatalf("failed row = state=%s hash=%v expiry=%v next=%s error=%s", state, leaseHash, leaseExpiry, nextAttempt, errorCode)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.reopen(t)
	if err := fixture.store.FailTeamProjectionJob(context.Background(), wrong); err != nil {
		t.Fatalf("restart failure replay: %v", err)
	}
	if claims, err := fixture.store.ClaimTeamProjectionJobs(context.Background(), claimRequest); err != nil || len(claims) != 0 {
		t.Fatalf("early retry = %+v, %v", claims, err)
	}
	fixture.now = fixture.now.Add(31 * time.Second)
	claims, err = fixture.store.ClaimTeamProjectionJobs(context.Background(), claimRequest)
	if err != nil || len(claims) != 1 || claims[0].AttemptCount != 2 || claims[0].LeaseToken == first.LeaseToken {
		t.Fatalf("due retry = %+v, %v", claims, err)
	}
	second := claims[0]
	fixture.now = fixture.now.Add(21 * time.Second)
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.reopen(t)
	defer fixture.store.Close()
	claims, err = fixture.store.ClaimTeamProjectionJobs(context.Background(), claimRequest)
	if err != nil || len(claims) != 1 || claims[0].AttemptCount != 3 || claims[0].LeaseToken == second.LeaseToken {
		t.Fatalf("expired restart reclaim = %+v, %v", claims, err)
	}
}

func TestCompleteTeamProjectionJobAtomicallyAttachesMultipleOutputsAndIsRestartIdempotent(t *testing.T) {
	fixture := newProjectionLifecycleFixture(t)
	installProjectionTestMaterialization(t, fixture)
	writer := fixture.acquireWriter(t)
	insertProjectionLifecycleRoot(t, fixture, "complete-root")
	insertProjectionLifecycleJob(t, fixture, "complete-job", "complete-root", "graph", "pending", 0, fixture.now.Add(-time.Second))
	claims, err := fixture.store.ClaimTeamProjectionJobs(context.Background(), TeamProjectionClaimRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token, ProjectionKind: "graph", Limit: 1, LeaseTTL: time.Minute,
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim completion job = %+v, %v", claims, err)
	}
	completion := TeamProjectionCompletionRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		JobID: "complete-job", LeaseToken: claims[0].LeaseToken,
		Outputs: []TeamProjectionOutput{
			{DerivativeObjectID: "derivative-a", DerivativeGeneration: 1, ObjectKind: "entity", StorageMappings: []TeamProjectionStorageMapping{
				{RepresentationKind: "entity", StorageKey: "entity:101"},
				{RepresentationKind: "embedding", StorageKey: "embedding:101"},
			}},
			{DerivativeObjectID: "derivative-b", DerivativeGeneration: 1, ObjectKind: "relation", StorageMappings: []TeamProjectionStorageMapping{
				{RepresentationKind: "relation", StorageKey: "relation:202"},
			}},
		},
	}
	if _, err := fixture.store.CompleteTeamProjectionJob(context.Background(), completion); !errors.Is(err, ErrInvalidProjectionJobRequest) {
		t.Fatalf("public non-empty completion bypass error = %v", err)
	}
	if _, err := fixture.store.completeTeamProjectionJobWithExtension(context.Background(), completion,
		func(context.Context, teamProjectionContentWriter, teamProjectionCompletionContext) error { return nil }); !errors.Is(err, ErrProjectionMaterializationFailed) {
		t.Fatalf("no-op materializer error = %v", err)
	}
	wrong := completion
	wrong.LeaseToken = "wrong-token"
	if _, err := fixture.store.completeTeamProjectionJobWithExtension(context.Background(), wrong, projectionTestMaterializer); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("wrong completion token error = %v", err)
	}
	wrongWriter := completion
	wrongWriter.WriterToken = "wrong-writer-token"
	if _, err := fixture.store.completeTeamProjectionJobWithExtension(context.Background(), wrongWriter, projectionTestMaterializer); !errors.Is(err, ErrTeamWriterLeaseMismatch) {
		t.Fatalf("wrong writer fence error = %v", err)
	}
	invalidOutput := completion
	invalidOutput.Outputs = append([]TeamProjectionOutput(nil), completion.Outputs...)
	invalidOutput.Outputs[0].StorageMappings = []TeamProjectionStorageMapping{{RepresentationKind: "entity", StorageKey: "raw/transcript"}}
	if _, err := fixture.store.completeTeamProjectionJobWithExtension(context.Background(), invalidOutput, projectionTestMaterializer); !errors.Is(err, ErrInvalidProjectionJobRequest) {
		t.Fatalf("content-like storage key error = %v", err)
	}
	result, err := fixture.store.completeTeamProjectionJobWithExtension(context.Background(), completion, projectionTestMaterializer)
	if err != nil || result.State != "ready" || result.AlreadyReady {
		t.Fatalf("complete result = %+v, %v", result, err)
	}
	sort.Strings(result.OutputObjectIDs)
	if len(result.OutputObjectIDs) != 2 || result.OutputObjectIDs[0] != "derivative-a" || result.OutputObjectIDs[1] != "derivative-b" {
		t.Fatalf("completion output IDs = %v", result.OutputObjectIDs)
	}
	for table, want := range map[string]int{
		"team_projection_outputs":   2,
		"team_object_contributions": 2,
		"team_object_storage_map":   3,
	} {
		var count int
		if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s rows = %d, want %d", table, count, want)
		}
	}
	var state string
	var storedHash any
	if err := fixture.store.DB().QueryRow(`SELECT state, lease_token_hash FROM team_projection_jobs WHERE job_id = 'complete-job'`).Scan(&state, &storedHash); err != nil {
		t.Fatal(err)
	}
	if state != "ready" || storedHash != nil {
		t.Fatalf("completed job state=%s hash=%v", state, storedHash)
	}
	wrongReadyToken := completion
	wrongReadyToken.LeaseToken = "wrong-token-after-ready"
	if _, err := fixture.store.completeTeamProjectionJobWithExtension(context.Background(), wrongReadyToken, projectionTestMaterializer); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("ready replay accepted wrong token: %v", err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.reopen(t)
	defer fixture.store.Close()
	replayed, err := fixture.store.completeTeamProjectionJobWithExtension(context.Background(), completion, projectionTestMaterializer)
	if err != nil || !replayed.AlreadyReady || replayed.State != "ready" {
		t.Fatalf("restart completion replay = %+v, %v", replayed, err)
	}
}

func TestProjectionCompletionReplayBindsFullCanonicalOutputsAcrossWriterRotation(t *testing.T) {
	fixture := newProjectionLifecycleFixture(t)
	writer := fixture.acquireWriter(t)
	insertProjectionLifecycleRoot(t, fixture, "terminal-root")
	insertProjectionLifecycleJob(t, fixture, "terminal-job", "terminal-root", "graph", "pending", 0, fixture.now.Add(-time.Second))
	claims, err := fixture.store.ClaimTeamProjectionJobs(context.Background(), TeamProjectionClaimRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		ProjectionKind: "graph", Limit: 1, LeaseTTL: time.Minute,
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim = %+v, %v", claims, err)
	}
	completion := TeamProjectionCompletionRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		JobID: "terminal-job", LeaseToken: claims[0].LeaseToken,
		Outputs: []TeamProjectionOutput{
			{DerivativeObjectID: "terminal-b", DerivativeGeneration: 1, ObjectKind: "relation", StorageMappings: []TeamProjectionStorageMapping{
				{RepresentationKind: "relation", StorageKey: "relation:terminal-b"},
			}},
			{DerivativeObjectID: "terminal-a", DerivativeGeneration: 1, ObjectKind: "entity", StorageMappings: []TeamProjectionStorageMapping{
				{RepresentationKind: "embedding", StorageKey: "embedding:terminal-a"},
				{RepresentationKind: "entity", StorageKey: "entity:terminal-a"},
			}},
		},
	}
	if _, err := fixture.store.DB().Exec(`
		CREATE TABLE terminal_projection_content(
			job_id TEXT NOT NULL,
			object_id TEXT NOT NULL,
			PRIMARY KEY(job_id, object_id)
		)`); err != nil {
		t.Fatal(err)
	}
	materialize := func(ctx context.Context, writer teamProjectionContentWriter, completion teamProjectionCompletionContext) error {
		for _, output := range completion.Outputs {
			if _, err := writer.ExecContext(ctx, `
				INSERT INTO terminal_projection_content(job_id, object_id)
				VALUES (?, ?)`, completion.JobID, output.DerivativeObjectID); err != nil {
				return err
			}
		}
		return nil
	}
	if _, err := fixture.store.completeTeamProjectionJobWithExtension(context.Background(), completion, materialize); err != nil {
		t.Fatalf("complete: %v", err)
	}
	var activeHash any
	var terminalHash, completionDigest string
	if err := fixture.store.DB().QueryRow(`
		SELECT lease_token_hash, terminal_lease_token_hash, completion_digest
		  FROM team_projection_jobs WHERE job_id = 'terminal-job'`).Scan(
		&activeHash, &terminalHash, &completionDigest); err != nil {
		t.Fatal(err)
	}
	if activeHash != nil || len(terminalHash) != 64 || len(completionDigest) != 64 ||
		terminalHash == completion.LeaseToken || !projectionLeaseTokenMatches(terminalHash, completion.LeaseToken) {
		t.Fatalf("unsafe terminal material: active=%v terminal=%q digest=%q", activeHash, terminalHash, completionDigest)
	}

	if err := fixture.store.ReleaseTeamWriterLease(context.Background(), writer.WriterID, writer.Token); err != nil {
		t.Fatal(err)
	}
	rotated := fixture.acquireWriter(t)
	if rotated.Token == writer.Token {
		t.Fatal("writer lease did not rotate")
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.reopen(t)
	defer fixture.store.Close()

	replay := completion
	replay.WriterID, replay.WriterToken = rotated.WriterID, rotated.Token
	replay.Outputs = []TeamProjectionOutput{completion.Outputs[1], completion.Outputs[0]}
	replay.Outputs[0].StorageMappings = []TeamProjectionStorageMapping{
		completion.Outputs[1].StorageMappings[1], completion.Outputs[1].StorageMappings[0],
	}
	result, err := fixture.store.completeTeamProjectionJobWithExtension(context.Background(), replay, materialize)
	if err != nil || !result.AlreadyReady || result.State != "ready" ||
		!reflect.DeepEqual(result.OutputObjectIDs, []string{"terminal-a", "terminal-b"}) {
		t.Fatalf("rotated restart replay = %+v, %v", result, err)
	}

	differentMapping := replay
	differentMapping.Outputs = append([]TeamProjectionOutput(nil), replay.Outputs...)
	differentMapping.Outputs[0].StorageMappings = append([]TeamProjectionStorageMapping(nil), replay.Outputs[0].StorageMappings...)
	differentMapping.Outputs[0].StorageMappings[0].StorageKey = "entity:different"
	if _, err := fixture.store.completeTeamProjectionJobWithExtension(context.Background(), differentMapping, materialize); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("mapping-conflicting terminal replay error = %v", err)
	}
	if _, err := fixture.store.DB().Exec(`
		DELETE FROM team_object_storage_map
		 WHERE representation_kind = 'embedding' AND storage_key = 'embedding:terminal-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.completeTeamProjectionJobWithExtension(context.Background(), replay, materialize); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("missing persisted mapping replay error = %v", err)
	}
	if _, err := fixture.store.DB().Exec(`
		INSERT INTO team_object_storage_map(
			object_id, team_id, scope_type, scope_id, generation,
			representation_kind, storage_key, created_at)
		VALUES ('terminal-a', ?, 'personal', ?, 1,
		        'embedding', 'embedding:terminal-a', '2026-07-11T12:00:00Z')`,
		fixture.bootstrap.TeamID, fixture.bootstrap.OwnerPrincipalID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DB().Exec(`
		UPDATE team_object_registry
		   SET lifecycle = 'tombstoned', generation = generation + 1,
		       updated_at = '2026-07-11T12:01:00Z'
		 WHERE object_id = 'terminal-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.completeTeamProjectionJobWithExtension(context.Background(), replay, materialize); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("stale derivative terminal replay error = %v", err)
	}
}

func TestProjectionCompletionAllowsZeroOutputsAndReplaysExactly(t *testing.T) {
	fixture := newProjectionLifecycleFixture(t)
	defer fixture.store.Close()
	writer := fixture.acquireWriter(t)
	insertProjectionLifecycleRoot(t, fixture, "zero-output-root")
	insertProjectionLifecycleJob(t, fixture, "zero-output-job", "zero-output-root", "noop", "pending", 0, fixture.now.Add(-time.Second))
	claims, err := fixture.store.ClaimTeamProjectionJobs(context.Background(), TeamProjectionClaimRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		ProjectionKind: "noop", Limit: 1, LeaseTTL: time.Minute,
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim = %+v, %v", claims, err)
	}
	request := TeamProjectionCompletionRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		JobID: "zero-output-job", LeaseToken: claims[0].LeaseToken,
	}
	first, err := fixture.store.CompleteTeamProjectionJob(context.Background(), request)
	if err != nil || first.State != "ready" || first.AlreadyReady || len(first.OutputObjectIDs) != 0 {
		t.Fatalf("zero-output completion = %+v, %v", first, err)
	}
	replay, err := fixture.store.CompleteTeamProjectionJob(context.Background(), request)
	if err != nil || !replay.AlreadyReady || replay.State != "ready" || len(replay.OutputObjectIDs) != 0 {
		t.Fatalf("zero-output replay = %+v, %v", replay, err)
	}
}

func TestProjectionJobsRejectExpiredSessionRootsAtClaimCompletionAndReplay(t *testing.T) {
	ctx := context.Background()
	fixture := newProjectionLifecycleFixture(t)
	defer fixture.store.Close()
	writer := fixture.acquireWriter(t)

	insertProjectionSessionRoot(t, fixture, "expired-claim-root", fixture.now.Add(-time.Second))
	insertProjectionLifecycleJob(t, fixture, "expired-claim-job", "expired-claim-root", "session", "pending", 0, fixture.now.Add(-time.Second))
	claims, err := fixture.store.ClaimTeamProjectionJobs(ctx, TeamProjectionClaimRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		ProjectionKind: "session", Limit: 1, LeaseTTL: time.Minute,
	})
	if err != nil || len(claims) != 0 {
		t.Fatalf("expired root claims = %+v, %v", claims, err)
	}

	insertProjectionSessionRoot(t, fixture, "expiry-during-work-root", fixture.now.Add(10*time.Second))
	insertProjectionLifecycleJob(t, fixture, "expiry-during-work-job", "expiry-during-work-root", "session-work", "pending", 0, fixture.now.Add(-time.Second))
	claims, err = fixture.store.ClaimTeamProjectionJobs(ctx, TeamProjectionClaimRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		ProjectionKind: "session-work", Limit: 1, LeaseTTL: time.Minute,
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("future root claim = %+v, %v", claims, err)
	}
	fixture.now = fixture.now.Add(11 * time.Second)
	if _, err := fixture.store.CompleteTeamProjectionJob(ctx, TeamProjectionCompletionRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		JobID: claims[0].JobID, LeaseToken: claims[0].LeaseToken,
	}); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("expired in-flight root completion error = %v", err)
	}

	insertProjectionSessionRoot(t, fixture, "expiry-replay-root", fixture.now.Add(10*time.Second))
	insertProjectionLifecycleJob(t, fixture, "expiry-replay-job", "expiry-replay-root", "session-replay", "pending", 0, fixture.now.Add(-time.Second))
	claims, err = fixture.store.ClaimTeamProjectionJobs(ctx, TeamProjectionClaimRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		ProjectionKind: "session-replay", Limit: 1, LeaseTTL: time.Minute,
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("replay root claim = %+v, %v", claims, err)
	}
	request := TeamProjectionCompletionRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		JobID: claims[0].JobID, LeaseToken: claims[0].LeaseToken,
	}
	if _, err := fixture.store.CompleteTeamProjectionJob(ctx, request); err != nil {
		t.Fatalf("complete before root expiry: %v", err)
	}
	fixture.now = fixture.now.Add(11 * time.Second)
	if _, err := fixture.store.CompleteTeamProjectionJob(ctx, request); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("expired root terminal replay error = %v", err)
	}
}

func TestProjectionCompletionExtensionIsRestrictedAtomicAndRedactsRawErrors(t *testing.T) {
	ctx := context.Background()
	fixture := newProjectionLifecycleFixture(t)
	defer fixture.store.Close()
	writer := fixture.acquireWriter(t)
	if _, err := fixture.store.DB().Exec(`
		CREATE TABLE synthetic_projection_content(
			job_id TEXT PRIMARY KEY,
			content_digest TEXT NOT NULL
		);
		CREATE TRIGGER synthetic_projection_content_requires_lease
		BEFORE INSERT ON synthetic_projection_content
		WHEN (SELECT state FROM team_projection_jobs WHERE job_id = NEW.job_id) <> 'leased'
		BEGIN SELECT RAISE(ABORT, 'content must materialize while leased'); END;
		CREATE TRIGGER synthetic_projection_ready_requires_content
		BEFORE UPDATE OF state ON team_projection_jobs
		WHEN NEW.job_id = 'extension-job' AND NEW.state = 'ready'
		 AND NOT EXISTS (SELECT 1 FROM synthetic_projection_content WHERE job_id = NEW.job_id)
		BEGIN SELECT RAISE(ABORT, 'content must exist before ready'); END;
	`); err != nil {
		t.Fatal(err)
	}

	claim := func(jobID, rootID string) TeamProjectionJobClaim {
		insertProjectionLifecycleRoot(t, fixture, rootID)
		insertProjectionLifecycleJob(t, fixture, jobID, rootID, "extension", "pending", 0, fixture.now.Add(-time.Second))
		claims, err := fixture.store.ClaimTeamProjectionJobs(ctx, TeamProjectionClaimRequest{
			WriterID: writer.WriterID, WriterToken: writer.Token,
			ProjectionKind: "extension", Limit: 1, LeaseTTL: time.Minute,
		})
		if err != nil || len(claims) != 1 {
			t.Fatalf("claim %s = %+v, %v", jobID, claims, err)
		}
		return claims[0]
	}

	claimed := claim("extension-job", "extension-root")
	request := TeamProjectionCompletionRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		JobID: claimed.JobID, LeaseToken: claimed.LeaseToken,
		Outputs: []TeamProjectionOutput{{DerivativeObjectID: "extension-derivative", DerivativeGeneration: 1, ObjectKind: "event"}},
	}
	result, err := fixture.store.completeTeamProjectionJobWithExtension(ctx, request,
		func(ctx context.Context, writer teamProjectionContentWriter, completion teamProjectionCompletionContext) error {
			if _, exposed := any(writer).(*sql.Tx); exposed {
				t.Fatal("completion extension received a raw commit-capable transaction")
			}
			writerType := reflect.TypeOf(writer)
			for _, forbidden := range []string{"Commit", "Rollback"} {
				if _, exposed := writerType.MethodByName(forbidden); exposed {
					t.Fatalf("completion writer exposes %s", forbidden)
				}
			}
			concrete := writerType
			if concrete.Kind() == reflect.Pointer {
				concrete = concrete.Elem()
			}
			for index := 0; index < concrete.NumField(); index++ {
				if concrete.Field(index).Type == reflect.TypeOf((*sql.Tx)(nil)) {
					t.Fatal("completion writer concrete value contains a raw transaction")
				}
			}
			for _, denied := range []string{
				"COMMIT",
				"DELETE FROM team_writer_leases",
				"INSERT INTO synthetic_projection_content(job_id, content_digest) SELECT job_id, 'x' FROM team_projection_jobs LIMIT 1",
				"INSERT INTO synthetic_projection_content(job_id, content_digest) VALUES ('escape', 'x'); COMMIT",
			} {
				if _, err := writer.ExecContext(ctx, denied); !errors.Is(err, ErrProjectionMaterializationFailed) {
					t.Fatalf("completion writer accepted denied statement %q: %v", denied, err)
				}
			}
			var leaked string
			if err := writer.QueryRowContext(ctx, `SELECT writer_id FROM team_writer_leases LIMIT 1`).Scan(&leaked); err == nil {
				t.Fatalf("completion writer read a control table: %q", leaked)
			}
			if completion.JobID != request.JobID || len(completion.Outputs) != 1 {
				t.Fatalf("completion context = %+v", completion)
			}
			_, err := writer.ExecContext(ctx, `
				INSERT INTO synthetic_projection_content(job_id, content_digest)
				VALUES (?, ?)`, completion.JobID, strings.Repeat("a", 64))
			return err
		})
	if err != nil || result.State != "ready" {
		t.Fatalf("extension completion = %+v, %v", result, err)
	}
	var contentRows int
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM synthetic_projection_content WHERE job_id = 'extension-job'`).Scan(&contentRows); err != nil || contentRows != 1 {
		t.Fatalf("materialized rows = %d, %v", contentRows, err)
	}

	failedClaim := claim("extension-failure-job", "extension-failure-root")
	failedRequest := TeamProjectionCompletionRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		JobID: failedClaim.JobID, LeaseToken: failedClaim.LeaseToken,
		Outputs: []TeamProjectionOutput{{DerivativeObjectID: "extension-rollback-derivative", DerivativeGeneration: 1, ObjectKind: "event"}},
	}
	rawSecret := "raw prompt /Users/private/transcript must never escape"
	_, err = fixture.store.completeTeamProjectionJobWithExtension(ctx, failedRequest,
		func(context.Context, teamProjectionContentWriter, teamProjectionCompletionContext) error {
			return errors.New(rawSecret)
		})
	if !errors.Is(err, ErrProjectionMaterializationFailed) || strings.Contains(err.Error(), rawSecret) {
		t.Fatalf("extension error leaked raw detail: %v", err)
	}
	var derivatives int
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM team_object_registry WHERE object_id = 'extension-rollback-derivative'`).Scan(&derivatives); err != nil || derivatives != 0 {
		t.Fatalf("extension failure left derivative rows = %d, %v", derivatives, err)
	}
	var state string
	if err := fixture.store.DB().QueryRow(`SELECT state FROM team_projection_jobs WHERE job_id = ?`, failedClaim.JobID).Scan(&state); err != nil || state != "leased" {
		t.Fatalf("extension failure job state = %q, %v", state, err)
	}
}

func TestProjectionCompletionExtensionRollsBackWhenWriterExpiresDuringMaterialization(t *testing.T) {
	ctx := context.Background()
	fixture := newProjectionLifecycleFixture(t)
	defer fixture.store.Close()
	writer := fixture.acquireWriter(t)
	insertProjectionLifecycleRoot(t, fixture, "late-extension-root")
	insertProjectionLifecycleJob(t, fixture, "late-extension-job", "late-extension-root", "extension", "pending", 0, fixture.now.Add(-time.Second))
	if _, err := fixture.store.DB().Exec(`
		CREATE TABLE late_projection_content(
			job_id TEXT PRIMARY KEY,
			content_digest TEXT NOT NULL
		)`); err != nil {
		t.Fatal(err)
	}
	claims, err := fixture.store.ClaimTeamProjectionJobs(ctx, TeamProjectionClaimRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		ProjectionKind: "extension", Limit: 1, LeaseTTL: 5 * time.Minute,
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim = %+v, %v", claims, err)
	}
	request := TeamProjectionCompletionRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		JobID: claims[0].JobID, LeaseToken: claims[0].LeaseToken,
		Outputs: []TeamProjectionOutput{{DerivativeObjectID: "late-extension-derivative", DerivativeGeneration: 1, ObjectKind: "event"}},
	}
	_, err = fixture.store.completeTeamProjectionJobWithExtension(ctx, request,
		func(ctx context.Context, content teamProjectionContentWriter, completion teamProjectionCompletionContext) error {
			if _, err := content.ExecContext(ctx, `
				INSERT INTO late_projection_content(job_id, content_digest)
				VALUES (?, ?)`, completion.JobID, strings.Repeat("a", 64)); err != nil {
				return err
			}
			fixture.now = fixture.now.Add(6 * time.Minute)
			return nil
		})
	if !errors.Is(err, ErrTeamWriterLeaseMismatch) {
		t.Fatalf("late extension error = %v", err)
	}
	for table, predicate := range map[string]string{
		"late_projection_content": "job_id = 'late-extension-job'",
		"team_object_registry":    "object_id = 'late-extension-derivative'",
		"team_projection_outputs": "job_id = 'late-extension-job'",
	} {
		var rows int
		if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM ` + table + ` WHERE ` + predicate).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Fatalf("late writer left %d rows in %s", rows, table)
		}
	}
	var state string
	if err := fixture.store.DB().QueryRow(`SELECT state FROM team_projection_jobs WHERE job_id = 'late-extension-job'`).Scan(&state); err != nil || state != "leased" {
		t.Fatalf("late extension job state = %q, %v", state, err)
	}
}

func TestProjectionCancellationRequiresTombstoneAndFixedReason(t *testing.T) {
	ctx := context.Background()
	fixture := newProjectionLifecycleFixture(t)
	defer fixture.store.Close()
	writer := fixture.acquireWriter(t)
	insertProjectionLifecycleRoot(t, fixture, "cancel-guard-root")
	insertProjectionLifecycleJob(t, fixture, "cancel-guard-job", "cancel-guard-root", "embedding", "pending", 0, fixture.now.Add(-time.Second))

	if _, err := fixture.store.DB().Exec(`
		UPDATE team_projection_jobs
		   SET state = 'cancelled', next_attempt_at = NULL,
		       last_error_code = 'root_tombstoned'
		 WHERE job_id = 'cancel-guard-job'`); err == nil {
		t.Fatal("schema allowed cancellation while the root was active")
	}
	tx, err := fixture.store.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CancelTeamProjectionJobsTx(ctx, tx, TeamProjectionCancellationRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		RootObjectID: "cancel-guard-root", RootGeneration: 1, ReasonCode: "root_tombstoned",
	}); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("active root cancellation error = %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	tx, err = fixture.store.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		UPDATE team_object_registry
		   SET lifecycle = 'tombstoned', generation = generation + 1,
		       updated_at = '2026-07-11T12:01:00Z'
		 WHERE object_id = 'cancel-guard-root'`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CancelTeamProjectionJobsTx(ctx, tx, TeamProjectionCancellationRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		RootObjectID: "cancel-guard-root", RootGeneration: 1, ReasonCode: "manual_override",
	}); !errors.Is(err, ErrInvalidProjectionJobRequest) {
		t.Fatalf("unbounded cancellation reason error = %v", err)
	}
	cancelled, err := fixture.store.CancelTeamProjectionJobsTx(ctx, tx, TeamProjectionCancellationRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		RootObjectID: "cancel-guard-root", RootGeneration: 1, ReasonCode: "root_tombstoned",
	})
	if err != nil || cancelled != 1 {
		t.Fatalf("tombstone cancellation = %d, %v", cancelled, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectionCompletionScopeMismatchRollsBackAndTombstoneCancelsWork(t *testing.T) {
	ctx := context.Background()
	fixture := newProjectionLifecycleFixture(t)
	installProjectionTestMaterialization(t, fixture)
	defer fixture.store.Close()
	insertProjectionLifecycleRoot(t, fixture, "stale-root")
	insertProjectionLifecycleJob(t, fixture, "stale-job", "stale-root", "event", "pending", 0, fixture.now.Add(-time.Second))
	insertProjectionLifecycleJob(t, fixture, "pending-stale-job", "stale-root", "embedding", "pending", 0, fixture.now.Add(-time.Second))
	project, err := fixture.store.CreateTeamProject(ctx, fixture.bootstrap.OwnerPrincipalID, "Cross scope")
	if err != nil {
		t.Fatal(err)
	}
	insertPolicyObject(t, fixture.store, fixture.bootstrap, "cross-scope-derivative", "project", project.ProjectID, fixture.bootstrap.OwnerPrincipalID)
	writer := fixture.acquireWriter(t)
	claims, err := fixture.store.ClaimTeamProjectionJobs(ctx, TeamProjectionClaimRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token, ProjectionKind: "event", Limit: 1, LeaseTTL: time.Minute,
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim stale job = %+v, %v", claims, err)
	}
	completion := TeamProjectionCompletionRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token, JobID: "stale-job", LeaseToken: claims[0].LeaseToken,
		Outputs: []TeamProjectionOutput{
			{DerivativeObjectID: "rolled-back-derivative", DerivativeGeneration: 1, ObjectKind: "event", StorageMappings: []TeamProjectionStorageMapping{{RepresentationKind: "event", StorageKey: "event:rolled-back"}}},
			{DerivativeObjectID: "cross-scope-derivative", DerivativeGeneration: 1, ObjectKind: "memory"},
		},
	}
	if _, err := fixture.store.completeTeamProjectionJobWithExtension(ctx, completion, projectionTestMaterializer); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("cross-scope completion error = %v", err)
	}
	var rolledBack int
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM team_object_registry WHERE object_id = 'rolled-back-derivative'`).Scan(&rolledBack); err != nil || rolledBack != 0 {
		t.Fatalf("partial derivative rows = %d, %v", rolledBack, err)
	}
	tx, err := fixture.store.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		UPDATE team_object_registry
		   SET lifecycle = 'tombstoned', generation = generation + 1,
		       updated_at = '2026-07-11T12:01:00Z'
		 WHERE object_id = 'stale-root'`); err != nil {
		t.Fatal(err)
	}
	cancelled, err := fixture.store.CancelTeamProjectionJobsTx(ctx, tx, TeamProjectionCancellationRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		RootObjectID: "stale-root", RootGeneration: 1, ReasonCode: "root_tombstoned",
	})
	if err != nil || cancelled != 2 {
		t.Fatalf("cancel stale jobs in tombstone tx = %d, %v", cancelled, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	completion.Outputs = completion.Outputs[:1]
	if _, err := fixture.store.completeTeamProjectionJobWithExtension(ctx, completion, projectionTestMaterializer); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("stale-root completion error = %v", err)
	}
	if claims, err := fixture.store.ClaimTeamProjectionJobs(ctx, TeamProjectionClaimRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token, ProjectionKind: "embedding", Limit: 1, LeaseTTL: time.Minute,
	}); err != nil || len(claims) != 0 {
		t.Fatalf("tombstoned pending claim = %+v, %v", claims, err)
	}
	var remaining int
	if err := fixture.store.DB().QueryRow(`
		SELECT count(*) FROM team_projection_jobs
		 WHERE root_object_id = 'stale-root' AND (
		       state <> 'cancelled' OR lease_token_hash IS NOT NULL
		       OR lease_expires_at IS NOT NULL OR next_attempt_at IS NOT NULL)`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("uncleared cancelled jobs = %d, %v", remaining, err)
	}
}
