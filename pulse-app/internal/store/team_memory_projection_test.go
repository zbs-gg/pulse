package store

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

type teamMemoryProjectionFixture struct {
	object teamObjectWriteFixture
	memory TeamMemoryWriteResult
	now    time.Time
}

func newTeamMemoryProjectionFixture(t *testing.T, mutate func(*TeamMemoryWrite)) *teamMemoryProjectionFixture {
	t.Helper()
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	options := reviewTeamOptions(testBootstrapRoot())
	options.Clock = func() time.Time { return now }
	object := newTeamObjectWriteFixtureWithOptions(t, filepath.Join(t.TempDir(), "team.db"), options)
	fixture := &teamMemoryProjectionFixture{object: object, now: now}
	object.store.clock = func() time.Time { return fixture.now }
	write := syntheticTeamMemoryWrite()
	if mutate != nil {
		mutate(&write)
	}
	memory, err := object.store.StoreTeamMemoryCapsule(
		context.Background(), object.permit, object.request.Writer, object.request.RequestID,
		object.actor.clientKey, write,
	)
	if err != nil {
		object.store.Close()
		t.Fatalf("store team memory: %v", err)
	}
	fixture.memory = memory
	return fixture
}

func (fixture *teamMemoryProjectionFixture) claim(t *testing.T, kind string) TeamProjectionJobClaim {
	t.Helper()
	claims, err := fixture.object.store.ClaimTeamProjectionJobs(context.Background(), TeamProjectionClaimRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		ProjectionKind: kind, Limit: 1, LeaseTTL: 45 * time.Second,
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim %s = %+v, %v", kind, claims, err)
	}
	return claims[0]
}

func eventProjectionRequest(fixture *teamMemoryProjectionFixture, claim TeamProjectionJobClaim) TeamMemoryEventProjectionRequest {
	return TeamMemoryEventProjectionRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		JobID: claim.JobID, LeaseToken: claim.LeaseToken,
	}
}

func TestCompleteTeamMemoryEventProjectionIsAtomicScopedAndReplayExact(t *testing.T) {
	ctx := context.Background()
	fixture := newTeamMemoryProjectionFixture(t, nil)
	defer fixture.object.store.Close()
	claim := fixture.claim(t, "event")
	request := eventProjectionRequest(fixture, claim)

	result, err := fixture.object.store.CompleteTeamMemoryEventProjection(ctx, request)
	if err != nil {
		t.Fatalf("complete event projection: %v", err)
	}
	if result.State != "ready" || result.AlreadyReady || len(result.OutputObjectIDs) != len(fixture.memory.CapsuleIDs) {
		t.Fatalf("event completion = %+v", result)
	}
	for _, capsuleID := range fixture.memory.CapsuleIDs {
		identity := deterministicTeamMemoryProjectionIdentity(
			fixture.memory.ObjectID, 1, capsuleID, "event", "",
		)
		if !containsString(result.OutputObjectIDs, identity.DerivativeObjectID) {
			t.Fatalf("missing deterministic derivative %s in %v", identity.DerivativeObjectID, result.OutputObjectIDs)
		}
		var rootID, derivativeID, eventID, scopeType, scopeID, summary string
		if err := fixture.object.store.DB().QueryRow(`
			SELECT root_object_id, derivative_object_id, event_id, scope_type, scope_id, redacted_summary
			  FROM team_memory_events WHERE capsule_id = ?`, capsuleID).Scan(
			&rootID, &derivativeID, &eventID, &scopeType, &scopeID, &summary,
		); err != nil {
			t.Fatal(err)
		}
		if rootID != fixture.memory.ObjectID || derivativeID != identity.DerivativeObjectID ||
			eventID != identity.StorageKey || scopeType != string(teamauth.ScopePersonal) ||
			scopeID != fixture.object.actor.member.PrincipalID || summary == "" {
			t.Fatalf("event materialization mismatch: root=%q derivative=%q event=%q scope=%q/%q summary=%q",
				rootID, derivativeID, eventID, scopeType, scopeID, summary)
		}
		assertTeamMemoryProjectionAttachments(t, fixture.object.store, claim.JobID,
			fixture.memory.ObjectID, identity.DerivativeObjectID, "memory_event", identity.StorageKey)
	}
	var state string
	if err := fixture.object.store.DB().QueryRow(`SELECT state FROM team_projection_jobs WHERE job_id = ?`, claim.JobID).Scan(&state); err != nil || state != "ready" {
		t.Fatalf("event job state = %q, %v", state, err)
	}
	replay, err := fixture.object.store.CompleteTeamMemoryEventProjection(ctx, request)
	if err != nil || !replay.AlreadyReady || !reflect.DeepEqual(replay.OutputObjectIDs, result.OutputObjectIDs) {
		t.Fatalf("terminal event replay = %+v, %v; first=%+v", replay, err, result)
	}
	var teamEvents, localEvents, localEmbeddings int
	for query, destination := range map[string]*int{
		`SELECT count(*) FROM team_memory_events`: &teamEvents,
		`SELECT count(*) FROM events`:             &localEvents,
		`SELECT count(*) FROM event_embeddings`:   &localEmbeddings,
	} {
		if err := fixture.object.store.DB().QueryRow(query).Scan(destination); err != nil {
			t.Fatal(err)
		}
	}
	if teamEvents != len(fixture.memory.CapsuleIDs) || localEvents != 0 || localEmbeddings != 0 {
		t.Fatalf("event table isolation = team:%d local:%d embeddings:%d", teamEvents, localEvents, localEmbeddings)
	}
	if _, err := fixture.object.store.CheckTeamPolicyReadiness(ctx, policyReadinessOptions(fixture.object.bootstrap, fixture.object.lease)); err != nil {
		t.Fatalf("event projection broke readiness: %v", err)
	}
}

func TestTeamMemoryEmbeddingFailureRetryReadyAndTerminalReplay(t *testing.T) {
	ctx := context.Background()
	fixture := newTeamMemoryProjectionFixture(t, nil)
	defer fixture.object.store.Close()
	firstClaim := fixture.claim(t, "embedding")
	if err := fixture.object.store.FailTeamProjectionJob(ctx, TeamProjectionFailureRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		JobID: firstClaim.JobID, LeaseToken: firstClaim.LeaseToken,
		ErrorCode: TeamProjectionFailureDependencyUnavailable, Backoff: time.Second,
	}); err != nil {
		t.Fatalf("fail embedding job: %v", err)
	}
	var rows int
	if err := fixture.object.store.DB().QueryRow(`SELECT count(*) FROM team_memory_embeddings`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("failed job materialized embeddings = %d, %v", rows, err)
	}
	fixture.now = fixture.now.Add(2 * time.Second)
	retryClaim := fixture.claim(t, "embedding")
	if retryClaim.JobID != firstClaim.JobID || retryClaim.AttemptCount != 2 || retryClaim.LeaseToken == firstClaim.LeaseToken {
		t.Fatalf("embedding retry claim = %+v, first=%+v", retryClaim, firstClaim)
	}
	results := make([]TeamMemoryEmbeddingResult, 0, len(fixture.memory.CapsuleIDs))
	for index, capsuleID := range fixture.memory.CapsuleIDs {
		results = append(results, TeamMemoryEmbeddingResult{
			CapsuleID: capsuleID,
			Vector:    []float32{float32(index) + 0.1, float32(index) + 0.2, float32(index) + 0.3},
		})
	}
	request := TeamMemoryEmbeddingProjectionRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		JobID: retryClaim.JobID, LeaseToken: retryClaim.LeaseToken,
		Model: "synthetic_embed_v1", Results: results,
	}
	originalResults := cloneTeamMemoryEmbeddingResults(request.Results)
	ready, err := fixture.object.store.CompleteTeamMemoryEmbeddingProjection(ctx, request)
	if err != nil || ready.State != "ready" || ready.AlreadyReady || len(ready.OutputObjectIDs) != len(results) {
		t.Fatalf("embedding ready = %+v, %v", ready, err)
	}
	if !reflect.DeepEqual(request.Results, originalResults) {
		t.Fatalf("embedding completion mutated caller input: got=%v want=%v", request.Results, originalResults)
	}
	for _, embedded := range results {
		identity := deterministicTeamMemoryProjectionIdentity(
			fixture.memory.ObjectID, 1, embedded.CapsuleID, "embedding", request.Model,
		)
		var derivativeID, embeddingID, model string
		var dimensions int
		if err := fixture.object.store.DB().QueryRow(`
			SELECT derivative_object_id, embedding_id, model, dimensions
			  FROM team_memory_embeddings WHERE capsule_id = ?`, embedded.CapsuleID).Scan(
			&derivativeID, &embeddingID, &model, &dimensions,
		); err != nil {
			t.Fatal(err)
		}
		if derivativeID != identity.DerivativeObjectID || embeddingID != identity.StorageKey ||
			model != request.Model || dimensions != len(embedded.Vector) {
			t.Fatalf("embedding materialization = derivative:%q id:%q model:%q dim:%d",
				derivativeID, embeddingID, model, dimensions)
		}
		assertTeamMemoryProjectionAttachments(t, fixture.object.store, retryClaim.JobID,
			fixture.memory.ObjectID, derivativeID, "memory_embedding", embeddingID)
	}
	replay, err := fixture.object.store.CompleteTeamMemoryEmbeddingProjection(ctx, request)
	if err != nil || !replay.AlreadyReady || !reflect.DeepEqual(replay.OutputObjectIDs, ready.OutputObjectIDs) {
		t.Fatalf("embedding replay = %+v, %v; first=%+v", replay, err, ready)
	}
	changed := request
	changed.Results = cloneTeamMemoryEmbeddingResults(request.Results)
	changed.Results[0].Vector[0] += 0.5
	if _, err := fixture.object.store.CompleteTeamMemoryEmbeddingProjection(ctx, changed); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("changed terminal vector error = %v", err)
	}
	if err := fixture.object.store.DB().QueryRow(`SELECT count(*) FROM team_memory_embeddings`).Scan(&rows); err != nil || rows != len(results) {
		t.Fatalf("embedding replay duplicated rows = %d, %v", rows, err)
	}
	if _, err := fixture.object.store.CheckTeamPolicyReadiness(ctx, policyReadinessOptions(fixture.object.bootstrap, fixture.object.lease)); err != nil {
		t.Fatalf("embedding projection broke readiness: %v", err)
	}
}

func TestTeamMemoryEmbeddingProjectionRejectsInvalidVectorsBeforeMutation(t *testing.T) {
	fixture := newTeamMemoryProjectionFixture(t, nil)
	defer fixture.object.store.Close()
	claim := fixture.claim(t, "embedding")
	valid := make([]TeamMemoryEmbeddingResult, 0, len(fixture.memory.CapsuleIDs))
	for index, capsuleID := range fixture.memory.CapsuleIDs {
		valid = append(valid, TeamMemoryEmbeddingResult{
			CapsuleID: capsuleID, Vector: []float32{float32(index) + 0.1, float32(index) + 0.2},
		})
	}
	tests := []struct {
		name   string
		mutate func(*TeamMemoryEmbeddingProjectionRequest)
	}{
		{name: "missing capsule", mutate: func(request *TeamMemoryEmbeddingProjectionRequest) {
			request.Results = request.Results[:1]
		}},
		{name: "duplicate capsule", mutate: func(request *TeamMemoryEmbeddingProjectionRequest) {
			request.Results[1].CapsuleID = request.Results[0].CapsuleID
		}},
		{name: "nan", mutate: func(request *TeamMemoryEmbeddingProjectionRequest) {
			request.Results[0].Vector[0] = float32(math.NaN())
		}},
		{name: "infinity", mutate: func(request *TeamMemoryEmbeddingProjectionRequest) {
			request.Results[0].Vector[0] = float32(math.Inf(1))
		}},
		{name: "zero vector", mutate: func(request *TeamMemoryEmbeddingProjectionRequest) {
			request.Results[0].Vector = []float32{0, 0}
		}},
		{name: "oversize", mutate: func(request *TeamMemoryEmbeddingProjectionRequest) {
			request.Results[0].Vector = make([]float32, 4097)
			request.Results[0].Vector[0] = 1
		}},
		{name: "dimension mismatch", mutate: func(request *TeamMemoryEmbeddingProjectionRequest) {
			request.Results[0].Vector = []float32{0.1, 0.2, 0.3}
		}},
		{name: "unsafe model", mutate: func(request *TeamMemoryEmbeddingProjectionRequest) {
			request.Model = "../../model"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := TeamMemoryEmbeddingProjectionRequest{
				WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
				JobID: claim.JobID, LeaseToken: claim.LeaseToken, Model: "synthetic_embed_v1",
				Results: cloneTeamMemoryEmbeddingResults(valid),
			}
			test.mutate(&request)
			if _, err := fixture.object.store.CompleteTeamMemoryEmbeddingProjection(
				context.Background(), request,
			); !errors.Is(err, ErrInvalidProjectionJobRequest) {
				t.Fatalf("invalid vector error = %v", err)
			}
			assertNoTeamMemoryProjectionRows(t, fixture.object.store, claim.JobID, fixture.memory.ObjectID)
		})
	}
}

func TestTeamMemoryProjectionRollsBackAttachmentsWhenRootExpiresDuringCompletion(t *testing.T) {
	base := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	fixture := newTeamMemoryProjectionFixture(t, func(write *TeamMemoryWrite) {
		write.ExpiresAt = base.Add(10 * time.Second).Format(time.RFC3339)
	})
	defer fixture.object.store.Close()
	claim := fixture.claim(t, "event")
	clockCalls := 0
	fixture.object.store.clock = func() time.Time {
		clockCalls++
		if clockCalls >= 3 {
			return base.Add(20 * time.Second)
		}
		return base
	}
	_, err := fixture.object.store.CompleteTeamMemoryEventProjection(
		context.Background(), eventProjectionRequest(fixture, claim),
	)
	if !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("expiry-during-completion error = %v", err)
	}
	assertNoTeamMemoryProjectionRows(t, fixture.object.store, claim.JobID, fixture.memory.ObjectID)
	var state string
	if err := fixture.object.store.DB().QueryRow(`SELECT state FROM team_projection_jobs WHERE job_id = ?`, claim.JobID).Scan(&state); err != nil || state != "leased" {
		t.Fatalf("rolled-back expiry job state = %q, %v", state, err)
	}
}

func TestTeamMemoryProjectionRejectsTombstonedRootAfterClaim(t *testing.T) {
	fixture := newTeamMemoryProjectionFixture(t, nil)
	defer fixture.object.store.Close()
	claim := fixture.claim(t, "event")
	if _, err := fixture.object.store.DB().Exec(`
		UPDATE team_object_registry
		   SET lifecycle = 'tombstoned', generation = generation + 1
		 WHERE object_id = ?`, fixture.memory.ObjectID); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.object.store.CompleteTeamMemoryEventProjection(
		context.Background(), eventProjectionRequest(fixture, claim),
	)
	if !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("tombstoned completion error = %v", err)
	}
	assertNoTeamMemoryProjectionRows(t, fixture.object.store, claim.JobID, fixture.memory.ObjectID)
}

func TestTeamMemoryProjectionIdentitiesArePartitionedAcrossScopes(t *testing.T) {
	ctx := context.Background()
	fixture := newTeamMemoryProjectionFixture(t, nil)
	defer fixture.object.store.Close()
	project, err := fixture.object.store.CreateTeamProject(ctx, fixture.object.bootstrap.OwnerPrincipalID, "Projection partition")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.object.store.GrantProjectAccess(ctx, GrantProjectAccessRequest{
		ActorPrincipalID: fixture.object.bootstrap.OwnerPrincipalID, ProjectID: project.ProjectID,
		TargetPrincipalID: fixture.object.actor.binding.AgentPrincipalID, AccessLevel: "write",
	}); err != nil {
		t.Fatal(err)
	}
	authorization := mutationWriteRequest(fixture.object.bootstrap, fixture.object.actor)
	authorization.Context.ProjectID = project.ProjectID
	authorization.RequestedScope = &teamauth.CanonicalScope{Type: teamauth.ScopeProject, ID: project.ProjectID}
	permit, err := fixture.object.store.AuthorizeTeamMutation(ctx, authorization)
	if err != nil {
		t.Fatal(err)
	}
	projectWrite := syntheticTeamMemoryWrite()
	projectWrite.ActiveContext.ProjectID = project.ProjectID
	projectWrite.TargetScope = &TeamMemoryTarget{Type: teamauth.ScopeProject, ID: project.ProjectID}
	projectWrite.IdempotencyKey = "team-memory-project-projection-0002"
	projectMemory, err := fixture.object.store.StoreTeamMemoryCapsule(
		ctx, permit, fixture.object.request.Writer, "request-project-projection-0002",
		fixture.object.actor.clientKey, projectWrite,
	)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := fixture.object.store.ClaimTeamProjectionJobs(ctx, TeamProjectionClaimRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		ProjectionKind: "event", Limit: 2, LeaseTTL: 45 * time.Second,
	})
	if err != nil || len(claims) != 2 {
		t.Fatalf("cross-scope claims = %+v, %v", claims, err)
	}
	outputsByRoot := map[string][]string{}
	for _, claim := range claims {
		result, err := fixture.object.store.CompleteTeamMemoryEventProjection(ctx, eventProjectionRequest(fixture, claim))
		if err != nil {
			t.Fatal(err)
		}
		outputsByRoot[claim.RootObjectID] = result.OutputObjectIDs
	}
	personal := outputsByRoot[fixture.memory.ObjectID]
	projectOutputs := outputsByRoot[projectMemory.ObjectID]
	if len(personal) == 0 || len(projectOutputs) == 0 {
		t.Fatalf("missing cross-scope outputs: %v", outputsByRoot)
	}
	seen := map[string]bool{}
	for _, id := range personal {
		seen[id] = true
	}
	for _, id := range projectOutputs {
		if seen[id] {
			t.Fatalf("scopes shared derivative ID %q", id)
		}
	}
	var distinctKeys, totalKeys int
	if err := fixture.object.store.DB().QueryRow(`
		SELECT count(DISTINCT event_id), count(*) FROM team_memory_events`).Scan(&distinctKeys, &totalKeys); err != nil {
		t.Fatal(err)
	}
	if distinctKeys != totalKeys || totalKeys != len(personal)+len(projectOutputs) {
		t.Fatalf("scope storage keys collide: distinct=%d total=%d outputs=%d", distinctKeys, totalKeys, len(personal)+len(projectOutputs))
	}
	var scopes []string
	rows, err := fixture.object.store.DB().Query(`SELECT DISTINCT scope_type FROM team_memory_events ORDER BY scope_type`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var scope string
		if err := rows.Scan(&scope); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		scopes = append(scopes, scope)
	}
	rows.Close()
	if !reflect.DeepEqual(scopes, []string{"personal", "project"}) {
		t.Fatalf("materialized scopes = %v", scopes)
	}
}

func TestTeamMemoryProjectionReadinessRejectsContentDrift(t *testing.T) {
	fixture := newTeamMemoryProjectionFixture(t, nil)
	defer fixture.object.store.Close()
	claim := fixture.claim(t, "event")
	if _, err := fixture.object.store.CompleteTeamMemoryEventProjection(
		context.Background(), eventProjectionRequest(fixture, claim),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.object.store.DB().Exec(`DROP TRIGGER team_memory_events_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.object.store.DB().Exec(`
		UPDATE team_memory_events SET redacted_summary = 'drifted synthetic content'
		 WHERE job_id = ?`, claim.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.object.store.CheckTeamPolicyReadiness(
		context.Background(), policyReadinessOptions(fixture.object.bootstrap, fixture.object.lease),
	); !errors.Is(err, ErrTeamPolicyNotReady) {
		t.Fatalf("drifted event readiness error = %v", err)
	}
}

func assertTeamMemoryProjectionAttachments(
	t *testing.T,
	store *Store,
	jobID, rootID, derivativeID, representationKind, storageKey string,
) {
	t.Helper()
	checks := []struct {
		query string
		args  []any
	}{
		{`SELECT count(*) FROM team_projection_outputs WHERE job_id = ? AND derivative_object_id = ?`, []any{jobID, derivativeID}},
		{`SELECT count(*) FROM team_object_contributions WHERE parent_object_id = ? AND derivative_object_id = ?`, []any{rootID, derivativeID}},
		{`SELECT count(*) FROM team_object_storage_map WHERE object_id = ? AND representation_kind = ? AND storage_key = ?`, []any{derivativeID, representationKind, storageKey}},
	}
	for _, check := range checks {
		var rows int
		if err := store.DB().QueryRow(check.query, check.args...).Scan(&rows); err != nil || rows != 1 {
			t.Fatalf("missing projection attachment for derivative %s: rows=%d err=%v query=%s", derivativeID, rows, err, check.query)
		}
	}
}

func assertNoTeamMemoryProjectionRows(t *testing.T, store *Store, jobID, rootID string) {
	t.Helper()
	checks := []struct {
		table     string
		predicate string
		arg       string
	}{
		{"team_memory_events", "root_object_id = ?", rootID},
		{"team_memory_embeddings", "root_object_id = ?", rootID},
		{"team_projection_outputs", "job_id = ?", jobID},
		{"team_object_contributions", "parent_object_id = ?", rootID},
	}
	for _, check := range checks {
		var rows int
		if err := store.DB().QueryRow(`SELECT count(*) FROM `+check.table+` WHERE `+check.predicate, check.arg).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("rollback left %s rows=%d err=%v", check.table, rows, err)
		}
	}
	var derivativeMappings int
	if err := store.DB().QueryRow(`
		SELECT count(*) FROM team_object_storage_map map
		JOIN team_object_contributions contribution ON contribution.derivative_object_id = map.object_id
		WHERE contribution.parent_object_id = ?`, rootID).Scan(&derivativeMappings); err != nil || derivativeMappings != 0 {
		t.Fatalf("rollback left derivative mappings=%d err=%v", derivativeMappings, err)
	}
	var derivativeRoots int
	if err := store.DB().QueryRow(`
		SELECT count(*) FROM team_object_registry
		 WHERE object_id <> ? AND object_kind IN ('event', 'embedding')`, rootID).Scan(&derivativeRoots); err != nil || derivativeRoots != 0 {
		t.Fatalf("rollback left derivative roots=%d err=%v", derivativeRoots, err)
	}
}
