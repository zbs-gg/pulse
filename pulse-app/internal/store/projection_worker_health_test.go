package store

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

func TestProjectionWorkerHealthIsMissingUntilExactWriterHeartbeats(t *testing.T) {
	fixture := newProjectionLifecycleFixture(t)
	defer fixture.store.Close()
	lease := fixture.acquireWriter(t)

	health, err := fixture.store.ReadTeamProjectionWorkerHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.State != TeamProjectionWorkerStateMissing ||
		health.Reason != TeamProjectionWorkerReasonHeartbeatMissing ||
		health.HeartbeatAt != nil || health.WorkerInstanceID != "" {
		t.Fatalf("missing health = %+v", health)
	}
	if err := fixture.store.CheckTeamProjectionQueueReadiness(context.Background()); !errors.Is(err, ErrTeamProjectionWorkerHeartbeatMissing) {
		t.Fatalf("missing heartbeat readiness = %v", err)
	}

	request := TeamProjectionWorkerHeartbeatRequest{
		WriterID: lease.WriterID, WriterToken: lease.Token,
		WorkerInstanceID: "projection_instance_a",
		DependencyState:  TeamProjectionDependencyReady,
	}
	if err := fixture.store.RecordTeamProjectionWorkerHeartbeat(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	health, err = fixture.store.ReadTeamProjectionWorkerHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.State != TeamProjectionWorkerStateReady || health.Reason != "" ||
		health.WorkerInstanceID != request.WorkerInstanceID || health.WriterID != lease.WriterID ||
		health.DependencyState != TeamProjectionDependencyReady || health.HeartbeatAt == nil ||
		!health.HeartbeatAt.Equal(fixture.now) {
		t.Fatalf("ready health = %+v", health)
	}
	if err := fixture.store.CheckTeamProjectionQueueReadiness(context.Background()); err != nil {
		t.Fatalf("ready worker with empty queue = %v", err)
	}

	wrong := request
	wrong.WriterToken = "wrong-writer-token"
	if err := fixture.store.RecordTeamProjectionWorkerHeartbeat(context.Background(), wrong); !errors.Is(err, ErrTeamWriterLeaseMismatch) {
		t.Fatalf("wrong writer heartbeat = %v", err)
	}
}

func TestProjectionWorkerHealthDistinguishesStaleMissingDependencyAndCycleError(t *testing.T) {
	fixture := newProjectionLifecycleFixture(t)
	defer fixture.store.Close()
	lease := fixture.acquireWriter(t)
	request := TeamProjectionWorkerHeartbeatRequest{
		WriterID: lease.WriterID, WriterToken: lease.Token,
		WorkerInstanceID: "projection_instance_health",
		DependencyState:  TeamProjectionDependencyDegraded,
		DependencyReason: TeamProjectionWorkerReasonEmbeddingNotConfigured,
	}
	if err := fixture.store.RecordTeamProjectionWorkerHeartbeat(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	health, err := fixture.store.ReadTeamProjectionWorkerHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.State != TeamProjectionWorkerStateDegraded ||
		health.Reason != TeamProjectionWorkerReasonEmbeddingNotConfigured {
		t.Fatalf("dependency health = %+v", health)
	}
	if err := fixture.store.CheckTeamProjectionQueueReadiness(context.Background()); !errors.Is(err, ErrTeamProjectionDependencyUnavailable) {
		t.Fatalf("dependency readiness = %v", err)
	}

	request.DependencyState = TeamProjectionDependencyReady
	request.DependencyReason = ""
	request.LastErrorCode = TeamProjectionWorkerErrorCycleFailed
	if err := fixture.store.RecordTeamProjectionWorkerHeartbeat(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	health, err = fixture.store.ReadTeamProjectionWorkerHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.State != TeamProjectionWorkerStateDegraded ||
		health.Reason != TeamProjectionWorkerReasonCycleFailed {
		t.Fatalf("cycle error health = %+v", health)
	}

	request.LastErrorCode = ""
	if err := fixture.store.RecordTeamProjectionWorkerHeartbeat(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.now = fixture.now.Add(teamProjectionWorkerStaleAfter + time.Second)
	health, err = fixture.store.ReadTeamProjectionWorkerHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.State != TeamProjectionWorkerStateStale ||
		health.Reason != TeamProjectionWorkerReasonHeartbeatStale {
		t.Fatalf("stale health = %+v", health)
	}
	if err := fixture.store.CheckTeamProjectionQueueReadiness(context.Background()); !errors.Is(err, ErrTeamProjectionWorkerHeartbeatStale) {
		t.Fatalf("stale readiness = %v", err)
	}

	fixture.now = fixture.now.Add(teamProjectionWorkerMissingAfter - teamProjectionWorkerStaleAfter)
	health, err = fixture.store.ReadTeamProjectionWorkerHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.State != TeamProjectionWorkerStateMissing ||
		health.Reason != TeamProjectionWorkerReasonHeartbeatMissing {
		t.Fatalf("expired health = %+v", health)
	}
}

func TestProjectionWorkerRestartReplacesInstanceButCannotCrossWriterLease(t *testing.T) {
	fixture := newProjectionLifecycleFixture(t)
	defer fixture.store.Close()
	firstLease := fixture.acquireWriter(t)
	heartbeat := TeamProjectionWorkerHeartbeatRequest{
		WriterID: firstLease.WriterID, WriterToken: firstLease.Token,
		WorkerInstanceID: "projection_instance_before_restart",
		DependencyState:  TeamProjectionDependencyReady,
	}
	if err := fixture.store.RecordTeamProjectionWorkerHeartbeat(context.Background(), heartbeat); err != nil {
		t.Fatal(err)
	}
	heartbeat.WorkerInstanceID = "projection_instance_after_restart"
	if err := fixture.store.RecordTeamProjectionWorkerHeartbeat(context.Background(), heartbeat); err != nil {
		t.Fatal(err)
	}
	health, err := fixture.store.ReadTeamProjectionWorkerHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.WorkerInstanceID != heartbeat.WorkerInstanceID || !health.StartedAt.Equal(fixture.now) {
		t.Fatalf("restart health = %+v", health)
	}

	fixture.now = fixture.now.Add(6 * time.Minute)
	secondLease, err := fixture.store.AcquireTeamWriterLease(context.Background(), TeamWriterLeaseRequest{
		WriterID: "projection-worker-after-lease-loss", WriterVersion: teamauth.SchemaVersion,
		TTL: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.RecordTeamProjectionWorkerHeartbeat(context.Background(), heartbeat); !errors.Is(err, ErrTeamWriterLeaseMismatch) {
		t.Fatalf("stale writer heartbeat = %v", err)
	}
	heartbeat.WriterID = secondLease.WriterID
	heartbeat.WriterToken = secondLease.Token
	heartbeat.WorkerInstanceID = "projection_instance_new_writer"
	if err := fixture.store.RecordTeamProjectionWorkerHeartbeat(context.Background(), heartbeat); err != nil {
		t.Fatal(err)
	}
	health, err = fixture.store.ReadTeamProjectionWorkerHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.State != TeamProjectionWorkerStateReady || health.WriterID != secondLease.WriterID ||
		health.WorkerInstanceID != heartbeat.WorkerInstanceID {
		t.Fatalf("new writer health = %+v", health)
	}
}

func TestProjectionWorkerHeartbeatSchemaIsContentFreeAndBounded(t *testing.T) {
	s, _ := bootstrapTeamStore(t)
	defer s.Close()
	columns := teamTableColumns(t, s, "team_worker_heartbeats")
	got := make([]string, 0, len(columns))
	for column := range columns {
		got = append(got, column)
	}
	sort.Strings(got)
	want := []string{
		"dependency_reason", "dependency_state", "heartbeat_at", "last_error_code",
		"started_at", "store_id", "team_id", "worker_instance_id", "worker_kind", "writer_id",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("worker heartbeat columns = %v, want %v", got, want)
	}
	for _, forbidden := range []string{"content", "text", "transcript", "prompt", "path", "secret", "token"} {
		for _, column := range got {
			if column == forbidden {
				t.Fatalf("heartbeat persists forbidden column %q", column)
			}
		}
	}
}
