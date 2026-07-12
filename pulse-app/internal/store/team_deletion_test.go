package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

type teamDeletionFixture struct {
	object *teamObjectWriteFixture
	now    *time.Time
	path   string
}

func newTeamDeletionFixture(t *testing.T) *teamDeletionFixture {
	t.Helper()
	now := time.Date(2026, 7, 11, 18, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "team.db")
	object := newTeamObjectWriteFixtureWithOptions(t, path, TeamOpenOptions{
		ExpectedBootstrapRoot: testBootstrapRoot(),
		Clock:                 func() time.Time { return now },
	})
	lease, err := object.store.AcquireTeamWriterLease(context.Background(), TeamWriterLeaseRequest{
		WriterID: object.lease.WriterID, WriterVersion: teamauth.SchemaVersion,
		Token: object.lease.Token, TTL: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	object.lease = lease
	object.request.Writer = TeamWriterLeaseIdentity{WriterID: lease.WriterID, Token: lease.Token}
	return &teamDeletionFixture{object: &object, now: &now, path: path}
}

func (fixture *teamDeletionFixture) close(t *testing.T) {
	t.Helper()
	if fixture.object != nil && fixture.object.store != nil {
		if err := fixture.object.store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func (fixture *teamDeletionFixture) reopen(t *testing.T) {
	t.Helper()
	store, err := OpenTeam(fixture.path, TeamOpenOptions{
		ExpectedBootstrapRoot: testBootstrapRoot(),
		Clock:                 func() time.Time { return *fixture.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.object.store = store
}

func (fixture *teamDeletionFixture) storeMemory(t *testing.T, suffix string) TeamMemoryWriteResult {
	t.Helper()
	write := syntheticTeamMemoryWrite()
	write.IdempotencyKey = "team-memory-delete-" + suffix
	result, err := fixture.object.store.StoreTeamMemoryCapsule(
		context.Background(), fixture.object.permit, fixture.object.request.Writer,
		"request-team-delete-write-"+suffix, fixture.object.actor.clientKey, write,
	)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (fixture *teamDeletionFixture) startRequest(objectID, key string) TeamDeletionStartRequest {
	return TeamDeletionStartRequest{
		Authorization: TeamMutationAuthorizationRequest{
			PrincipalID:      fixture.object.actor.binding.AgentPrincipalID,
			OAuthClientKey:   fixture.object.actor.clientKey,
			Action:           teamauth.ActionDelete,
			Capabilities:     []teamauth.Capability{teamauth.CapabilityDelete},
			Context:          teamauth.ActiveContext{TeamID: fixture.object.bootstrap.TeamID},
			ExistingObjectID: objectID,
		},
		Writer:         fixture.object.request.Writer,
		RequestID:      "request-team-delete-0001",
		IdempotencyKey: key,
	}
}

func (fixture *teamDeletionFixture) statusRequest(operationID string) TeamDeletionStatusRequest {
	return TeamDeletionStatusRequest{
		PrincipalID:    fixture.object.actor.binding.AgentPrincipalID,
		OAuthClientKey: fixture.object.actor.clientKey,
		Capabilities:   []teamauth.Capability{teamauth.CapabilityRead},
		Context:        teamauth.ActiveContext{TeamID: fixture.object.bootstrap.TeamID},
		OperationID:    operationID,
	}
}

func (fixture *teamDeletionFixture) claimRequest() TeamDeletionClaimRequest {
	return TeamDeletionClaimRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		Limit: 1, LeaseTTL: time.Minute,
	}
}

func requireSameDeletionIdentity(t *testing.T, got, want TeamDeletionResult) {
	t.Helper()
	if got.OperationID != want.OperationID || got.ObjectID != want.ObjectID ||
		got.AuditEventID != want.AuditEventID || got.Status != want.Status {
		t.Fatalf("deletion result did not replay exact identity:\n got  %+v\n want %+v", got, want)
	}
}

func TestStartTeamDeletionTombstonesAtomicallyAndReplaysAfterResponseLossAndRestart(t *testing.T) {
	fixture := newTeamDeletionFixture(t)
	root := fixture.storeMemory(t, "response-loss")
	otherRoot := fixture.storeMemory(t, "response-loss-conflict")
	request := fixture.startRequest(root.ObjectID, "delete-response-loss-0001")

	first, err := fixture.object.store.StartTeamDeletion(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.OperationID == "" || first.AuditEventID == "" || first.ObjectID != root.ObjectID ||
		first.Status != TeamDeletionStatusInProgress || first.Replayed {
		t.Fatalf("start result = %+v", first)
	}
	var lifecycle string
	var generation int64
	if err := fixture.object.store.DB().QueryRow(`
		SELECT lifecycle, generation FROM team_object_registry WHERE object_id = ?`,
		root.ObjectID).Scan(&lifecycle, &generation); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "tombstoned" || generation != 2 {
		t.Fatalf("root lifecycle=%q generation=%d", lifecycle, generation)
	}
	var cancelled, activeJobs int
	if err := fixture.object.store.DB().QueryRow(`
		SELECT count(*) FILTER (WHERE state = 'cancelled'),
		       count(*) FILTER (WHERE state IN ('pending','failed','leased'))
		  FROM team_projection_jobs WHERE root_object_id = ?`, root.ObjectID).
		Scan(&cancelled, &activeJobs); err != nil {
		t.Fatal(err)
	}
	if cancelled != 2 || activeJobs != 0 {
		t.Fatalf("cancelled=%d active=%d", cancelled, activeJobs)
	}

	replay, err := fixture.object.store.StartTeamDeletion(context.Background(), request)
	if err != nil {
		t.Fatalf("response-loss replay: %v", err)
	}
	if !replay.Replayed {
		t.Fatalf("response-loss replay not marked: %+v", replay)
	}
	replay.Replayed = first.Replayed
	requireSameDeletionIdentity(t, replay, first)
	conflict := fixture.startRequest(otherRoot.ObjectID, request.IdempotencyKey)
	if _, err := fixture.object.store.StartTeamDeletion(context.Background(), conflict); !errors.Is(err, ErrTeamIdempotencyConflict) {
		t.Fatalf("same key with different object = %v", err)
	}

	fixture.close(t)
	fixture.reopen(t)
	defer fixture.close(t)
	restarted, err := fixture.object.store.StartTeamDeletion(context.Background(), request)
	if err != nil {
		t.Fatalf("restart replay: %v", err)
	}
	if !restarted.Replayed {
		t.Fatalf("restart replay not marked: %+v", restarted)
	}
	restarted.Replayed = first.Replayed
	requireSameDeletionIdentity(t, restarted, first)
	claims, err := fixture.object.store.ClaimTeamDeletionJobs(context.Background(), fixture.claimRequest())
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim after restart = %+v, %v", claims, err)
	}
	if _, err := fixture.object.store.CompleteTeamDeletion(context.Background(), TeamDeletionCompletionRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		OperationID: first.OperationID, LeaseToken: claims[0].LeaseToken,
	}); err != nil {
		t.Fatal(err)
	}
	afterComplete, err := fixture.object.store.StartTeamDeletion(context.Background(), request)
	if err != nil {
		t.Fatalf("delete replay after complete: %v", err)
	}
	if !afterComplete.Replayed || afterComplete.Status != TeamDeletionStatusComplete {
		t.Fatalf("delete replay after complete = %+v", afterComplete)
	}
	afterComplete.Replayed = first.Replayed
	afterComplete.Status = first.Status
	requireSameDeletionIdentity(t, afterComplete, first)

	var operations, startAudits int
	if err := fixture.object.store.DB().QueryRow(`SELECT count(*) FROM team_deletion_operations WHERE root_object_id = ?`, root.ObjectID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := fixture.object.store.DB().QueryRow(`SELECT count(*) FROM team_audit_events WHERE action = 'team.object.delete.start' AND target_id = ?`, root.ObjectID).Scan(&startAudits); err != nil {
		t.Fatal(err)
	}
	if operations != 1 || startAudits != 1 {
		t.Fatalf("replay duplicated state: operations=%d start_audits=%d", operations, startAudits)
	}
}

func TestStartTeamDeletionRequiresCurrentAuthorityBeforeReplayAndConcealsConflicts(t *testing.T) {
	fixture := newTeamDeletionFixture(t)
	defer fixture.close(t)
	root := fixture.storeMemory(t, "grant-loss")
	request := fixture.startRequest(root.ObjectID, "delete-grant-loss-0001")
	if _, err := fixture.object.store.StartTeamDeletion(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	if err := fixture.object.store.RevokeAgentBinding(context.Background(),
		fixture.object.bootstrap.OwnerPrincipalID, fixture.object.actor.binding.BindingID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.object.store.StartTeamDeletion(context.Background(), request); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("replay after authority loss = %v", err)
	}

	absent := fixture.startRequest("object-does-not-exist", "delete-absent-0001")
	if _, err := fixture.object.store.StartTeamDeletion(context.Background(), absent); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("absent delete = %v", err)
	}
}

func TestStartTeamDeletionReplayFailsClosedAfterExactProjectGrantLoss(t *testing.T) {
	fixture := newTeamDeletionFixture(t)
	defer fixture.close(t)
	project, err := fixture.object.store.CreateTeamProject(context.Background(),
		fixture.object.bootstrap.OwnerPrincipalID, "Deletion grant loss")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := fixture.object.store.GrantProjectAccess(context.Background(), GrantProjectAccessRequest{
		ActorPrincipalID:  fixture.object.bootstrap.OwnerPrincipalID,
		ProjectID:         project.ProjectID,
		TargetPrincipalID: fixture.object.actor.binding.AgentPrincipalID,
		AccessLevel:       "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	writeAuthorization := mutationWriteRequest(fixture.object.bootstrap, fixture.object.actor)
	writeAuthorization.Context.ProjectID = project.ProjectID
	writeAuthorization.RequestedScope = &teamauth.CanonicalScope{
		Type: teamauth.ScopeProject, ID: project.ProjectID,
	}
	permit, err := fixture.object.store.AuthorizeTeamMutation(context.Background(), writeAuthorization)
	if err != nil {
		t.Fatal(err)
	}
	write := syntheticTeamMemoryWrite()
	write.IdempotencyKey = "project-grant-loss-write"
	write.ActiveContext.ProjectID = project.ProjectID
	write.TargetScope = &TeamMemoryTarget{Type: teamauth.ScopeProject, ID: project.ProjectID}
	root, err := fixture.object.store.StoreTeamMemoryCapsule(
		context.Background(), permit, fixture.object.request.Writer,
		"request-project-grant-loss-write", fixture.object.actor.clientKey, write,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := fixture.startRequest(root.ObjectID, "delete-project-grant-loss")
	request.Authorization.Context.ProjectID = project.ProjectID
	if _, err := fixture.object.store.StartTeamDeletion(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := fixture.object.store.RevokeProjectGrant(context.Background(),
		fixture.object.bootstrap.OwnerPrincipalID, grant.GrantID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.object.store.StartTeamDeletion(context.Background(), request); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("replay after project grant loss = %v", err)
	}
}

func TestStartTeamDeletionRechecksExactProjectGrantAtResponseBoundary(t *testing.T) {
	fixture := newTeamDeletionFixture(t)
	defer fixture.close(t)
	project, err := fixture.object.store.CreateTeamProject(context.Background(),
		fixture.object.bootstrap.OwnerPrincipalID, "Deletion response boundary")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := fixture.object.store.GrantProjectAccess(context.Background(), GrantProjectAccessRequest{
		ActorPrincipalID:  fixture.object.bootstrap.OwnerPrincipalID,
		ProjectID:         project.ProjectID,
		TargetPrincipalID: fixture.object.actor.binding.AgentPrincipalID,
		AccessLevel:       "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	writeAuthorization := mutationWriteRequest(fixture.object.bootstrap, fixture.object.actor)
	writeAuthorization.Context.ProjectID = project.ProjectID
	writeAuthorization.RequestedScope = &teamauth.CanonicalScope{
		Type: teamauth.ScopeProject, ID: project.ProjectID,
	}
	permit, err := fixture.object.store.AuthorizeTeamMutation(context.Background(), writeAuthorization)
	if err != nil {
		t.Fatal(err)
	}
	write := syntheticTeamMemoryWrite()
	write.IdempotencyKey = "project-response-boundary-write"
	write.ActiveContext.ProjectID = project.ProjectID
	write.TargetScope = &TeamMemoryTarget{Type: teamauth.ScopeProject, ID: project.ProjectID}
	root, err := fixture.object.store.StoreTeamMemoryCapsule(
		context.Background(), permit, fixture.object.request.Writer,
		"request-project-response-boundary-write", fixture.object.actor.clientKey, write,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := fixture.startRequest(root.ObjectID, "delete-project-response-boundary")
	request.Authorization.Context.ProjectID = project.ProjectID
	_, err = fixture.object.store.startTeamDeletionWithResponseBoundary(
		context.Background(), request, func() {
			if revokeErr := fixture.object.store.RevokeProjectGrant(context.Background(),
				fixture.object.bootstrap.OwnerPrincipalID, grant.GrantID); revokeErr != nil {
				t.Fatal(revokeErr)
			}
		})
	if !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("start response after exact grant revocation = %v", err)
	}
	var lifecycle, operationState string
	if err := fixture.object.store.DB().QueryRow(`
		SELECT lifecycle FROM team_object_registry WHERE object_id = ?`, root.ObjectID).
		Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if err := fixture.object.store.DB().QueryRow(`
		SELECT state FROM team_deletion_operations WHERE root_object_id = ?`, root.ObjectID).
		Scan(&operationState); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "tombstoned" || operationState != "pending" {
		t.Fatalf("durable deletion after withheld response: lifecycle=%q operation=%q",
			lifecycle, operationState)
	}
}

func TestStartTeamDeletionConflictRechecksBindingAtResponseBoundary(t *testing.T) {
	fixture := newTeamDeletionFixture(t)
	defer fixture.close(t)
	first := fixture.storeMemory(t, "response-conflict-first")
	second := fixture.storeMemory(t, "response-conflict-second")
	request := fixture.startRequest(first.ObjectID, "delete-response-conflict")
	if _, err := fixture.object.store.StartTeamDeletion(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	conflict := fixture.startRequest(second.ObjectID, request.IdempotencyKey)
	_, err := fixture.object.store.startTeamDeletionWithResponseBoundary(
		context.Background(), conflict, func() {
			if revokeErr := fixture.object.store.RevokeAgentBinding(context.Background(),
				fixture.object.bootstrap.OwnerPrincipalID,
				fixture.object.actor.binding.BindingID); revokeErr != nil {
				t.Fatal(revokeErr)
			}
		})
	if !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("conflict response after binding revocation = %v, want concealed", err)
	}
}

func TestReadTeamDeletionStatusRechecksBindingAtResponseBoundary(t *testing.T) {
	fixture := newTeamDeletionFixture(t)
	defer fixture.close(t)
	root := fixture.storeMemory(t, "status-response-boundary")
	started, err := fixture.object.store.StartTeamDeletion(context.Background(),
		fixture.startRequest(root.ObjectID, "delete-status-response-boundary"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.object.store.readTeamDeletionStatusWithResponseBoundary(
		context.Background(), fixture.statusRequest(started.OperationID), func() {
			if revokeErr := fixture.object.store.RevokeAgentBinding(context.Background(),
				fixture.object.bootstrap.OwnerPrincipalID,
				fixture.object.actor.binding.BindingID); revokeErr != nil {
				t.Fatal(revokeErr)
			}
		})
	if !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("status response after binding revocation = %v", err)
	}
}

func TestTeamDeletionCanonicalizesAgentScopeToAuthenticatedBinding(t *testing.T) {
	fixture := newTeamDeletionFixture(t)
	defer fixture.close(t)
	secondBinding, err := fixture.object.store.RegisterAgentBinding(context.Background(), RegisterAgentBindingRequest{
		ActorPrincipalID: fixture.object.bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example", Subject: "object-writer",
		ClientID: "object-writer-second-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondClientKey := teamauth.OAuthClientKey("https://issuer.example", "object-writer-second-agent")
	writeAuthorization := mutationWriteRequest(fixture.object.bootstrap, fixture.object.actor)
	writeAuthorization.Context.AgentID = fixture.object.actor.binding.BindingID
	writeAuthorization.RequestedScope = &teamauth.CanonicalScope{
		Type: teamauth.ScopeAgent, ID: fixture.object.actor.binding.BindingID,
	}
	permit, err := fixture.object.store.AuthorizeTeamMutation(context.Background(), writeAuthorization)
	if err != nil {
		t.Fatal(err)
	}
	write := syntheticTeamMemoryWrite()
	write.IdempotencyKey = "agent-scope-delete-write"
	write.ActiveContext.AgentID = fixture.object.actor.binding.BindingID
	write.TargetScope = &TeamMemoryTarget{
		Type: teamauth.ScopeAgent, ID: fixture.object.actor.binding.BindingID,
	}
	root, err := fixture.object.store.StoreTeamMemoryCapsule(
		context.Background(), permit, fixture.object.request.Writer,
		"request-agent-scope-delete-write", fixture.object.actor.clientKey, write,
	)
	if err != nil {
		t.Fatal(err)
	}
	otherDelete := fixture.startRequest(root.ObjectID, "delete-agent-scope-other")
	otherDelete.Authorization.PrincipalID = secondBinding.AgentPrincipalID
	otherDelete.Authorization.OAuthClientKey = secondClientKey
	if _, err := fixture.object.store.StartTeamDeletion(context.Background(), otherDelete); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("second binding deleted first binding scope: %v", err)
	}
	ownerDelete := fixture.startRequest(root.ObjectID, "delete-agent-scope-owner")
	started, err := fixture.object.store.StartTeamDeletion(context.Background(), ownerDelete)
	if err != nil {
		t.Fatal(err)
	}
	otherStatus := TeamDeletionStatusRequest{
		PrincipalID: secondBinding.AgentPrincipalID, OAuthClientKey: secondClientKey,
		Capabilities: []teamauth.Capability{teamauth.CapabilityRead},
		Context:      teamauth.ActiveContext{TeamID: fixture.object.bootstrap.TeamID},
		OperationID:  started.OperationID,
	}
	if _, err := fixture.object.store.ReadTeamDeletionStatus(context.Background(), otherStatus); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("second binding read first binding deletion: %v", err)
	}
	spoofedReplay := ownerDelete
	spoofedReplay.Authorization.Context.AgentID = secondBinding.BindingID
	if _, err := fixture.object.store.StartTeamDeletion(context.Background(), spoofedReplay); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("spoofed agent context replay = %v", err)
	}
}

func TestTeamDeletionStatusAuthorizesCanonicalObjectIgnoringLifecycleOnly(t *testing.T) {
	fixture := newTeamDeletionFixture(t)
	defer fixture.close(t)
	root := fixture.storeMemory(t, "status")
	started, err := fixture.object.store.StartTeamDeletion(context.Background(), fixture.startRequest(root.ObjectID, "delete-status-0001"))
	if err != nil {
		t.Fatal(err)
	}
	status, err := fixture.object.store.ReadTeamDeletionStatus(context.Background(), fixture.statusRequest(started.OperationID))
	if err != nil {
		t.Fatal(err)
	}
	if status.OperationID != started.OperationID || status.ObjectID != root.ObjectID ||
		status.AuditEventID != started.AuditEventID || status.Status != TeamDeletionStatusInProgress ||
		status.StartedAt.IsZero() || status.UpdatedAt.IsZero() || status.NextAttemptAt == nil ||
		!status.NextAttemptAt.Equal(*fixture.now) || status.CompletedAt != nil {
		t.Fatalf("status = %+v", status)
	}

	wrongCapability := fixture.statusRequest(started.OperationID)
	wrongCapability.Capabilities = []teamauth.Capability{teamauth.CapabilityDelete}
	if _, err := fixture.object.store.ReadTeamDeletionStatus(context.Background(), wrongCapability); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("status without read capability = %v", err)
	}
	absent := fixture.statusRequest("delete_operation_absent")
	if _, err := fixture.object.store.ReadTeamDeletionStatus(context.Background(), absent); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("absent status = %v", err)
	}
	other := addMutationAuthorizationActor(t, fixture.object.store, fixture.object.bootstrap,
		"deletion-status-other-member", "member")
	unauthorizedStatus := TeamDeletionStatusRequest{
		PrincipalID:    other.binding.AgentPrincipalID,
		OAuthClientKey: other.clientKey,
		Capabilities:   []teamauth.Capability{teamauth.CapabilityRead},
		Context:        teamauth.ActiveContext{TeamID: fixture.object.bootstrap.TeamID},
		OperationID:    started.OperationID,
	}
	if _, err := fixture.object.store.ReadTeamDeletionStatus(context.Background(), unauthorizedStatus); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("unauthorized status = %v", err)
	}
	unauthorizedDelete := fixture.startRequest(root.ObjectID, "delete-other-member")
	unauthorizedDelete.Authorization.PrincipalID = other.binding.AgentPrincipalID
	unauthorizedDelete.Authorization.OAuthClientKey = other.clientKey
	if _, err := fixture.object.store.StartTeamDeletion(context.Background(), unauthorizedDelete); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("unauthorized delete = %v", err)
	}
}

func TestTeamDeletionLeaseFailureReapAndRestartAreRetryable(t *testing.T) {
	fixture := newTeamDeletionFixture(t)
	root := fixture.storeMemory(t, "lease")
	started, err := fixture.object.store.StartTeamDeletion(context.Background(), fixture.startRequest(root.ObjectID, "delete-lease-0001"))
	if err != nil {
		t.Fatal(err)
	}
	claims, err := fixture.object.store.ClaimTeamDeletionJobs(context.Background(), fixture.claimRequest())
	if err != nil || len(claims) != 1 || claims[0].OperationID != started.OperationID ||
		claims[0].LeaseToken == "" || claims[0].AttemptCount != 1 {
		t.Fatalf("claim = %+v, %v", claims, err)
	}
	first := claims[0]
	var storedHash string
	if err := fixture.object.store.DB().QueryRow(`SELECT lease_token_hash FROM team_deletion_operations WHERE operation_id = ?`, first.OperationID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if len(storedHash) != 64 || storedHash == first.LeaseToken || strings.Contains(storedHash, first.LeaseToken) {
		t.Fatalf("unsafe lease persistence raw=%q stored=%q", first.LeaseToken, storedHash)
	}
	if err := fixture.object.store.FailTeamDeletion(context.Background(), TeamDeletionFailureRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		OperationID: first.OperationID, LeaseToken: first.LeaseToken,
		ErrorCode: TeamDeletionFailureTemporary, Backoff: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	status, err := fixture.object.store.ReadTeamDeletionStatus(context.Background(), fixture.statusRequest(first.OperationID))
	if err != nil || status.Status != TeamDeletionStatusCleanupFailed ||
		status.LastErrorCode != TeamDeletionFailureTemporary || status.NextAttemptAt == nil ||
		!status.NextAttemptAt.Equal(fixture.now.Add(time.Minute)) {
		t.Fatalf("failed status = %+v, %v", status, err)
	}
	if claims, err := fixture.object.store.ClaimTeamDeletionJobs(context.Background(), fixture.claimRequest()); err != nil || len(claims) != 0 {
		t.Fatalf("early retry = %+v, %v", claims, err)
	}
	*fixture.now = fixture.now.Add(time.Minute)
	claims, err = fixture.object.store.ClaimTeamDeletionJobs(context.Background(), fixture.claimRequest())
	if err != nil || len(claims) != 1 || claims[0].AttemptCount != 2 || claims[0].LeaseToken == first.LeaseToken {
		t.Fatalf("due retry = %+v, %v", claims, err)
	}
	second := claims[0]

	*fixture.now = fixture.now.Add(2 * time.Minute)
	fixture.close(t)
	fixture.reopen(t)
	defer fixture.close(t)
	reaped, err := fixture.object.store.ReapExpiredTeamDeletionLeases(context.Background(), TeamDeletionReapRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token, Limit: 16,
	})
	if err != nil || reaped != 1 {
		t.Fatalf("reap expired %s = %d, %v", second.OperationID, reaped, err)
	}
	var reapedLifecycle string
	if err := fixture.object.store.DB().QueryRow(`
		SELECT lifecycle FROM team_object_registry WHERE object_id = ?`, root.ObjectID).
		Scan(&reapedLifecycle); err != nil {
		t.Fatal(err)
	}
	if reapedLifecycle != "tombstoned" {
		t.Fatalf("expired cleanup lifecycle = %q, want tombstoned", reapedLifecycle)
	}
	claims, err = fixture.object.store.ClaimTeamDeletionJobs(context.Background(), fixture.claimRequest())
	if err != nil || len(claims) != 1 || claims[0].AttemptCount != 3 {
		t.Fatalf("restart reclaim = %+v, %v", claims, err)
	}
}

func TestTeamDeletionWorkerPathsPropagateCorruptOperationTimestamps(t *testing.T) {
	for _, test := range []struct {
		name string
		call func(*teamDeletionFixture, TeamDeletionResult, TeamDeletionClaim) error
	}{
		{
			name: "fail",
			call: func(fixture *teamDeletionFixture, started TeamDeletionResult, claim TeamDeletionClaim) error {
				return fixture.object.store.FailTeamDeletion(context.Background(), TeamDeletionFailureRequest{
					WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
					OperationID: started.OperationID, LeaseToken: claim.LeaseToken,
					ErrorCode: TeamDeletionFailureTemporary, Backoff: time.Second,
				})
			},
		},
		{
			name: "complete",
			call: func(fixture *teamDeletionFixture, started TeamDeletionResult, claim TeamDeletionClaim) error {
				_, err := fixture.object.store.CompleteTeamDeletion(context.Background(), TeamDeletionCompletionRequest{
					WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
					OperationID: started.OperationID, LeaseToken: claim.LeaseToken,
				})
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTeamDeletionFixture(t)
			defer fixture.close(t)
			root := fixture.storeMemory(t, "corrupt-timestamp-"+test.name)
			started, err := fixture.object.store.StartTeamDeletion(context.Background(),
				fixture.startRequest(root.ObjectID, "delete-corrupt-timestamp-"+test.name))
			if err != nil {
				t.Fatal(err)
			}
			claims, err := fixture.object.store.ClaimTeamDeletionJobs(
				context.Background(), fixture.claimRequest())
			if err != nil || len(claims) != 1 {
				t.Fatalf("claim = %+v, %v", claims, err)
			}
			if _, err := fixture.object.store.DB().Exec(`
				UPDATE team_deletion_operations SET updated_at = '2026-99-99T99:99:99Z'
				 WHERE operation_id = ?`, started.OperationID); err != nil {
				t.Fatal(err)
			}
			err = test.call(fixture, started, claims[0])
			if err == nil || errors.Is(err, ErrConcealedNotFound) {
				t.Fatalf("corrupt timestamp error = %v, want operational parse failure", err)
			}
		})
	}
}

func TestCompetingTeamDeletionCleanersClaimOneLease(t *testing.T) {
	fixture := newTeamDeletionFixture(t)
	defer fixture.close(t)
	root := fixture.storeMemory(t, "competing-cleaners")
	started, err := fixture.object.store.StartTeamDeletion(context.Background(),
		fixture.startRequest(root.ObjectID, "delete-competing-cleaners"))
	if err != nil {
		t.Fatal(err)
	}
	const cleaners = 8
	type claimResult struct {
		claims []TeamDeletionClaim
		err    error
	}
	results := make(chan claimResult, cleaners)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for range cleaners {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			claims, err := fixture.object.store.ClaimTeamDeletionJobs(
				context.Background(), fixture.claimRequest())
			results <- claimResult{claims: claims, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	claimed := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("competing claim: %v", result.err)
		}
		claimed += len(result.claims)
		for _, claim := range result.claims {
			if claim.OperationID != started.OperationID {
				t.Fatalf("claimed wrong operation: %+v", claim)
			}
		}
	}
	if claimed != 1 {
		t.Fatalf("leases claimed = %d, want 1", claimed)
	}
}

func TestTeamDeletionCleanupCrashRollsBackAndRetryCompletes(t *testing.T) {
	fixture := newTeamDeletionFixture(t)
	defer fixture.close(t)
	root := fixture.storeMemory(t, "cleanup-crash")
	started, err := fixture.object.store.StartTeamDeletion(context.Background(),
		fixture.startRequest(root.ObjectID, "delete-cleanup-crash"))
	if err != nil {
		t.Fatal(err)
	}
	claims, err := fixture.object.store.ClaimTeamDeletionJobs(context.Background(), fixture.claimRequest())
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim = %+v, %v", claims, err)
	}
	first := claims[0]
	if _, err := fixture.object.store.DB().Exec(`
		CREATE TRIGGER reject_team_deletion_cleanup_test
		BEFORE DELETE ON team_memory_capsules
		BEGIN SELECT RAISE(ABORT, 'synthetic cleanup crash'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.object.store.CompleteTeamDeletion(context.Background(), TeamDeletionCompletionRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		OperationID: started.OperationID, LeaseToken: first.LeaseToken,
	}); err == nil {
		t.Fatal("cleanup crash injection did not fail")
	}
	var operationState, lifecycle string
	var capsules int
	if err := fixture.object.store.DB().QueryRow(`
		SELECT state FROM team_deletion_operations WHERE operation_id = ?`,
		started.OperationID).Scan(&operationState); err != nil {
		t.Fatal(err)
	}
	if err := fixture.object.store.DB().QueryRow(`
		SELECT lifecycle FROM team_object_registry WHERE object_id = ?`, root.ObjectID).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if err := fixture.object.store.DB().QueryRow(`
		SELECT count(*) FROM team_memory_capsules WHERE root_object_id = ?`, root.ObjectID).Scan(&capsules); err != nil {
		t.Fatal(err)
	}
	if operationState != "leased" || lifecycle != "cleaning" || capsules != len(root.CapsuleIDs) {
		t.Fatalf("partial cleanup escaped rollback: operation=%q lifecycle=%q capsules=%d",
			operationState, lifecycle, capsules)
	}
	if _, err := fixture.object.store.DB().Exec(`DROP TRIGGER reject_team_deletion_cleanup_test`); err != nil {
		t.Fatal(err)
	}
	if err := fixture.object.store.FailTeamDeletion(context.Background(), TeamDeletionFailureRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		OperationID: started.OperationID, LeaseToken: first.LeaseToken,
		ErrorCode: TeamDeletionFailureTemporary, Backoff: time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	*fixture.now = (*fixture.now).Add(2 * time.Second)
	retry, err := fixture.object.store.ClaimTeamDeletionJobs(context.Background(), fixture.claimRequest())
	if err != nil || len(retry) != 1 || retry[0].AttemptCount != 2 {
		t.Fatalf("retry = %+v, %v", retry, err)
	}
	completed, err := fixture.object.store.CompleteTeamDeletion(context.Background(), TeamDeletionCompletionRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		OperationID: started.OperationID, LeaseToken: retry[0].LeaseToken,
	})
	if err != nil || completed.Status != TeamDeletionStatusComplete {
		t.Fatalf("retry complete = %+v, %v", completed, err)
	}
}

func TestStartTeamDeletionFencesAndCancelsAlreadyLeasedProjection(t *testing.T) {
	fixture := newTeamDeletionFixture(t)
	defer fixture.close(t)
	root := fixture.storeMemory(t, "late-projection")
	claims, err := fixture.object.store.ClaimTeamProjectionJobs(context.Background(), TeamProjectionClaimRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		ProjectionKind: "event", Limit: 1, LeaseTTL: time.Minute,
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim event = %+v, %v", claims, err)
	}
	claim := claims[0]
	if _, err := fixture.object.store.StartTeamDeletion(context.Background(),
		fixture.startRequest(root.ObjectID, "delete-late-projection-0001")); err != nil {
		t.Fatal(err)
	}
	var state, reason string
	if err := fixture.object.store.DB().QueryRow(`
		SELECT state, last_error_code FROM team_projection_jobs WHERE job_id = ?`,
		claim.JobID).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "cancelled" || reason != TeamProjectionCancellationRootTombstoned {
		t.Fatalf("leased projection after tombstone state=%q reason=%q", state, reason)
	}
	if _, err := fixture.object.store.CompleteTeamMemoryEventProjection(context.Background(), TeamMemoryEventProjectionRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		JobID: claim.JobID, LeaseToken: claim.LeaseToken,
	}); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("late projection completion = %v", err)
	}
	var events int
	if err := fixture.object.store.DB().QueryRow(`
		SELECT count(*) FROM team_memory_events WHERE root_object_id = ?`, root.ObjectID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("late projection resurrected %d events", events)
	}
}

func TestStartTeamDeletionRejectsDerivativeWithoutBreakingSupportingRoot(t *testing.T) {
	fixture := newTeamDeletionFixture(t)
	defer fixture.close(t)
	root := fixture.storeMemory(t, "derivative-target")
	claims, err := fixture.object.store.ClaimTeamProjectionJobs(context.Background(), TeamProjectionClaimRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		ProjectionKind: "event", Limit: 1, LeaseTTL: time.Minute,
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim event = %+v, %v", claims, err)
	}
	if _, err := fixture.object.store.CompleteTeamMemoryEventProjection(context.Background(), TeamMemoryEventProjectionRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		JobID: claims[0].JobID, LeaseToken: claims[0].LeaseToken,
	}); err != nil {
		t.Fatal(err)
	}
	var derivativeID string
	if err := fixture.object.store.DB().QueryRow(`
		SELECT derivative_object_id FROM team_memory_events
		 WHERE root_object_id = ? ORDER BY derivative_object_id LIMIT 1`, root.ObjectID).
		Scan(&derivativeID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.object.store.StartTeamDeletion(context.Background(),
		fixture.startRequest(derivativeID, "delete-derivative-target")); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("derivative delete = %v", err)
	}
	var rootLifecycle, derivativeLifecycle, jobState string
	var contribution int
	if err := fixture.object.store.DB().QueryRow(`SELECT lifecycle FROM team_object_registry WHERE object_id = ?`, root.ObjectID).Scan(&rootLifecycle); err != nil {
		t.Fatal(err)
	}
	if err := fixture.object.store.DB().QueryRow(`SELECT lifecycle FROM team_object_registry WHERE object_id = ?`, derivativeID).Scan(&derivativeLifecycle); err != nil {
		t.Fatal(err)
	}
	if err := fixture.object.store.DB().QueryRow(`SELECT state FROM team_projection_jobs WHERE job_id = ?`, claims[0].JobID).Scan(&jobState); err != nil {
		t.Fatal(err)
	}
	if err := fixture.object.store.DB().QueryRow(`
		SELECT count(*) FROM team_object_contributions
		 WHERE parent_object_id = ? AND derivative_object_id = ?`, root.ObjectID, derivativeID).
		Scan(&contribution); err != nil {
		t.Fatal(err)
	}
	if rootLifecycle != "active" || derivativeLifecycle != "active" || jobState != "ready" || contribution != 1 {
		t.Fatalf("supporting projection changed: root=%q derivative=%q job=%q contribution=%d",
			rootLifecycle, derivativeLifecycle, jobState, contribution)
	}
	if _, err := fixture.object.store.CheckTeamPolicyReadiness(context.Background(),
		policyReadinessOptions(fixture.object.bootstrap, fixture.object.lease)); err != nil {
		t.Fatalf("derivative delete attempt broke readiness: %v", err)
	}
}

func TestStartTeamDeletionRejectsDescendantAlreadyOwnedByPendingRootDeletion(t *testing.T) {
	fixture := newTeamDeletionFixture(t)
	defer fixture.close(t)
	root := fixture.storeMemory(t, "overlapping-root")
	const derivativeID = "overlapping-root-derivative"
	insertDeletionDerivative(t, fixture, derivativeID, "graph_entity")
	linkDeletionContribution(t, fixture, root.ObjectID, derivativeID)
	started, err := fixture.object.store.StartTeamDeletion(context.Background(),
		fixture.startRequest(root.ObjectID, "delete-overlapping-root"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.object.store.StartTeamDeletion(context.Background(),
		fixture.startRequest(derivativeID, "delete-overlapping-descendant")); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("overlapping descendant delete = %v", err)
	}
	claims, err := fixture.object.store.ClaimTeamDeletionJobs(
		context.Background(), fixture.claimRequest())
	if err != nil || len(claims) != 1 || claims[0].OperationID != started.OperationID {
		t.Fatalf("root deletion claim = %+v, %v", claims, err)
	}
	if _, err := fixture.object.store.CompleteTeamDeletion(context.Background(),
		TeamDeletionCompletionRequest{
			WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
			OperationID: started.OperationID, LeaseToken: claims[0].LeaseToken,
		}); err != nil {
		t.Fatal(err)
	}
	var descendants int
	if err := fixture.object.store.DB().QueryRow(`
		SELECT count(*) FROM team_object_registry WHERE object_id = ?`, derivativeID).
		Scan(&descendants); err != nil {
		t.Fatal(err)
	}
	if descendants != 0 {
		t.Fatalf("root completed with %d descendant rows remaining", descendants)
	}
}

func TestCompleteTeamDeletionPurgesRootDomainRowsAndLeavesLocalMemoryUntouched(t *testing.T) {
	fixture := newTeamDeletionFixture(t)
	defer fixture.close(t)
	root := fixture.storeMemory(t, "complete")
	eventClaims, err := fixture.object.store.ClaimTeamProjectionJobs(context.Background(), TeamProjectionClaimRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		ProjectionKind: "event", Limit: 1, LeaseTTL: time.Minute,
	})
	if err != nil || len(eventClaims) != 1 {
		t.Fatalf("claim event = %+v, %v", eventClaims, err)
	}
	if _, err := fixture.object.store.CompleteTeamMemoryEventProjection(context.Background(), TeamMemoryEventProjectionRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		JobID: eventClaims[0].JobID, LeaseToken: eventClaims[0].LeaseToken,
	}); err != nil {
		t.Fatalf("complete event: %v", err)
	}
	embeddingClaims, err := fixture.object.store.ClaimTeamProjectionJobs(context.Background(), TeamProjectionClaimRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		ProjectionKind: "embedding", Limit: 1, LeaseTTL: time.Minute,
	})
	if err != nil || len(embeddingClaims) != 1 {
		t.Fatalf("claim embedding = %+v, %v", embeddingClaims, err)
	}
	results := make([]TeamMemoryEmbeddingResult, 0, len(root.CapsuleIDs))
	for index, capsuleID := range root.CapsuleIDs {
		results = append(results, TeamMemoryEmbeddingResult{
			CapsuleID: capsuleID,
			Vector:    []float32{float32(index) + 0.25, float32(index) + 0.75},
		})
	}
	if _, err := fixture.object.store.CompleteTeamMemoryEmbeddingProjection(context.Background(), TeamMemoryEmbeddingProjectionRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		JobID: embeddingClaims[0].JobID, LeaseToken: embeddingClaims[0].LeaseToken,
		Model: "synthetic_delete_v1", Results: results,
	}); err != nil {
		t.Fatalf("complete embedding: %v", err)
	}
	if _, err := fixture.object.store.RememberCapsule(MemoryCapsule{
		Schema: MemoryCapsuleSchema,
		Source: CapsuleSource{Host: "codex", ConversationScope: "current_turn", Timestamp: "2026-07-11T18:00:00Z"},
		Items:  []MemoryCapsuleItem{{Kind: "decision", RedactedSummary: "A local synthetic row must remain untouched.", Confidence: 1, EvidenceHint: "current_turn", PrivacyTier: "normal", Retention: "long_term"}},
	}); err != nil {
		t.Fatal(err)
	}
	started, err := fixture.object.store.StartTeamDeletion(context.Background(), fixture.startRequest(root.ObjectID, "delete-complete-0001"))
	if err != nil {
		t.Fatal(err)
	}
	claims, err := fixture.object.store.ClaimTeamDeletionJobs(context.Background(), fixture.claimRequest())
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim = %+v, %v", claims, err)
	}
	completed, err := fixture.object.store.CompleteTeamDeletion(context.Background(), TeamDeletionCompletionRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		OperationID: started.OperationID, LeaseToken: claims[0].LeaseToken,
	})
	if err != nil || completed.Status != TeamDeletionStatusComplete || completed.CompletedAt == nil {
		t.Fatalf("complete = %+v, %v", completed, err)
	}

	for table, column := range map[string]string{
		"team_memory_capsules":             "root_object_id",
		"team_memory_events":               "root_object_id",
		"team_memory_embeddings":           "root_object_id",
		"team_graph_delta_inputs":          "root_object_id",
		"team_semantic_projection_intents": "root_object_id",
		"team_semantic_materializations":   "root_object_id",
		"team_projection_jobs":             "root_object_id",
		"team_object_storage_map":          "object_id",
		"team_object_contributions":        "parent_object_id",
	} {
		var count int
		if err := fixture.object.store.DB().QueryRow(`SELECT count(*) FROM `+table+` WHERE `+column+` = ?`, root.ObjectID).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s retains %d root rows", table, count)
		}
	}
	var lifecycle string
	var generation int64
	if err := fixture.object.store.DB().QueryRow(`SELECT lifecycle, generation FROM team_object_registry WHERE object_id = ?`, root.ObjectID).Scan(&lifecycle, &generation); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "complete" || generation != 2 {
		t.Fatalf("terminal root lifecycle=%q generation=%d", lifecycle, generation)
	}
	var derivativeRoots int
	if err := fixture.object.store.DB().QueryRow(`
		SELECT count(*) FROM team_object_registry WHERE object_id <> ?`, root.ObjectID).Scan(&derivativeRoots); err != nil {
		t.Fatal(err)
	}
	if derivativeRoots != 0 {
		t.Fatalf("unsupported derivative registry rows = %d", derivativeRoots)
	}
	fixture.close(t)
	fixture.reopen(t)
	statusAfterRestart, err := fixture.object.store.ReadTeamDeletionStatus(
		context.Background(), fixture.statusRequest(started.OperationID))
	if err != nil || statusAfterRestart.Status != TeamDeletionStatusComplete ||
		statusAfterRestart.CompletedAt == nil {
		t.Fatalf("restart status = %+v, %v", statusAfterRestart, err)
	}
	if _, err := fixture.object.store.CheckTeamPolicyReadiness(context.Background(),
		policyReadinessOptions(fixture.object.bootstrap, fixture.object.lease)); !errors.Is(err, ErrLegacyLocalData) {
		t.Fatalf("local sentinel was not preserved across restart: %v", err)
	}
	local, err := fixture.object.store.ExportMemory()
	if err != nil || len(local.Items) != 1 || local.Items[0].RedactedSummary != "A local synthetic row must remain untouched." {
		t.Fatalf("local memory changed = %+v, %v", local, err)
	}
	var startAudits, completeAudits int
	if err := fixture.object.store.DB().QueryRow(`SELECT count(*) FROM team_audit_events WHERE action = 'team.object.delete.start' AND target_id = ?`, root.ObjectID).Scan(&startAudits); err != nil {
		t.Fatal(err)
	}
	if err := fixture.object.store.DB().QueryRow(`SELECT count(*) FROM team_audit_events WHERE action = 'team.object.delete.complete' AND target_id = ?`, root.ObjectID).Scan(&completeAudits); err != nil {
		t.Fatal(err)
	}
	if startAudits != 1 || completeAudits != 1 {
		t.Fatalf("audit pair start=%d complete=%d", startAudits, completeAudits)
	}
}

func TestCompletedTeamDeletionSurvivesRestartAndPassesReadiness(t *testing.T) {
	fixture := newTeamDeletionFixture(t)
	root := fixture.storeMemory(t, "restart-ready")
	completeDeletionForRoot(t, fixture, root.ObjectID, "delete-restart-ready")
	fixture.close(t)
	fixture.reopen(t)
	defer fixture.close(t)
	if _, err := fixture.object.store.CheckTeamPolicyReadiness(context.Background(),
		policyReadinessOptions(fixture.object.bootstrap, fixture.object.lease)); err != nil {
		t.Fatalf("completed deletion failed restart readiness: %v", err)
	}
}

func insertDeletionDerivative(t *testing.T, fixture *teamDeletionFixture, objectID, objectKind string) {
	t.Helper()
	target := fixture.object.permit.EffectiveTarget()
	now := fixture.now.UTC().Format(time.RFC3339Nano)
	if _, err := fixture.object.store.DB().Exec(`
		INSERT INTO team_object_registry(
			object_id, store_id, team_id, object_kind, scope_type, scope_id,
			owner_principal_id, author_principal_id, privacy_tier, retention,
			lifecycle, generation, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'normal', 'long_term', 'active', 1, ?, ?)`,
		objectID, fixture.object.bootstrap.StoreID, fixture.object.bootstrap.TeamID,
		objectKind, target.Type, target.ID, target.OwnerPrincipalID,
		fixture.object.actor.binding.AgentPrincipalID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.object.store.DB().Exec(`
		INSERT INTO team_object_storage_map(
			object_id, team_id, scope_type, scope_id, generation,
			representation_kind, storage_key, created_at)
		VALUES (?, ?, ?, ?, 1, ?, ?, ?)`, objectID, fixture.object.bootstrap.TeamID,
		target.Type, target.ID, objectKind, objectKind+":"+objectID, now); err != nil {
		t.Fatal(err)
	}
}

func linkDeletionContribution(t *testing.T, fixture *teamDeletionFixture, parent, derivative string) {
	t.Helper()
	target := fixture.object.permit.EffectiveTarget()
	if _, err := fixture.object.store.DB().Exec(`
		INSERT INTO team_object_contributions(
			parent_object_id, derivative_object_id, team_id, scope_type, scope_id,
			parent_generation, derivative_generation, created_at)
		VALUES (?, ?, ?, ?, ?, 1, 1, ?)`, parent, derivative,
		fixture.object.bootstrap.TeamID, target.Type, target.ID,
		fixture.now.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}

func completeDeletionForRoot(t *testing.T, fixture *teamDeletionFixture, objectID, key string) TeamDeletionStatus {
	t.Helper()
	started, err := fixture.object.store.StartTeamDeletion(context.Background(), fixture.startRequest(objectID, key))
	if err != nil {
		t.Fatal(err)
	}
	claims, err := fixture.object.store.ClaimTeamDeletionJobs(context.Background(), fixture.claimRequest())
	if err != nil || len(claims) != 1 || claims[0].OperationID != started.OperationID {
		t.Fatalf("claim %s = %+v, %v", started.OperationID, claims, err)
	}
	status, err := fixture.object.store.CompleteTeamDeletion(context.Background(), TeamDeletionCompletionRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		OperationID: started.OperationID, LeaseToken: claims[0].LeaseToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func TestTeamDeletionFrontierSupportsMoreThanSixtyFourContributionLevels(t *testing.T) {
	fixture := newTeamDeletionFixture(t)
	defer fixture.close(t)
	root := fixture.storeMemory(t, "deep-frontier")
	parent := root.ObjectID
	const derivativeLevels = 65
	for index := 1; index <= derivativeLevels; index++ {
		objectID := fmt.Sprintf("deep-deletion-%03d", index)
		insertDeletionDerivative(t, fixture, objectID, "graph_entity")
		linkDeletionContribution(t, fixture, parent, objectID)
		parent = objectID
	}
	started, err := fixture.object.store.StartTeamDeletion(context.Background(),
		fixture.startRequest(root.ObjectID, "delete-deep-frontier"))
	if err != nil {
		t.Fatal(err)
	}
	var frontierCount, maxDepth int
	if err := fixture.object.store.DB().QueryRow(`
		SELECT count(*), max(depth) FROM team_deletion_frontier WHERE operation_id = ?`,
		started.OperationID).Scan(&frontierCount, &maxDepth); err != nil {
		t.Fatal(err)
	}
	if frontierCount != derivativeLevels+1 || maxDepth != derivativeLevels {
		t.Fatalf("deep frontier count=%d max_depth=%d", frontierCount, maxDepth)
	}
	claims, err := fixture.object.store.ClaimTeamDeletionJobs(
		context.Background(), fixture.claimRequest())
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim = %+v, %v", claims, err)
	}
	if _, err := fixture.object.store.CompleteTeamDeletion(context.Background(),
		TeamDeletionCompletionRequest{
			WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
			OperationID: started.OperationID, LeaseToken: claims[0].LeaseToken,
		}); err != nil {
		t.Fatal(err)
	}
	var derivatives int
	if err := fixture.object.store.DB().QueryRow(`
		SELECT count(*) FROM team_object_registry WHERE object_id LIKE 'deep-deletion-%'`).
		Scan(&derivatives); err != nil {
		t.Fatal(err)
	}
	if derivatives != 0 {
		t.Fatalf("deep deletion retained %d derivative objects", derivatives)
	}
}

func TestTeamDeletionFixedPointPreservesSharedDerivativeThenPurgesFinalParent(t *testing.T) {
	fixture := newTeamDeletionFixture(t)
	defer fixture.close(t)
	first := fixture.storeMemory(t, "shared-first")
	second := fixture.storeMemory(t, "shared-second")
	insertDeletionDerivative(t, fixture, "shared-derivative", "graph_entity")
	linkDeletionContribution(t, fixture, first.ObjectID, "shared-derivative")
	linkDeletionContribution(t, fixture, second.ObjectID, "shared-derivative")

	firstCompletion := completeDeletionForRoot(t, fixture, first.ObjectID, "delete-shared-first")
	var derivative, contributions int
	if err := fixture.object.store.DB().QueryRow(`SELECT count(*) FROM team_object_registry WHERE object_id = 'shared-derivative'`).Scan(&derivative); err != nil {
		t.Fatal(err)
	}
	if err := fixture.object.store.DB().QueryRow(`SELECT count(*) FROM team_object_contributions WHERE derivative_object_id = 'shared-derivative'`).Scan(&contributions); err != nil {
		t.Fatal(err)
	}
	if derivative != 1 || contributions != 1 {
		t.Fatalf("shared survivor derivative=%d contributions=%d", derivative, contributions)
	}
	var preservedDischarges int
	if err := fixture.object.store.DB().QueryRow(`
		SELECT count(*) FROM team_deletion_discharges
		 WHERE operation_id = ? AND object_id = 'shared-derivative'
		   AND disposition = 'preserved'`, firstCompletion.OperationID).
		Scan(&preservedDischarges); err != nil {
		t.Fatal(err)
	}
	if preservedDischarges != 1 {
		t.Fatalf("shared derivative preserved discharges=%d", preservedDischarges)
	}

	secondCompletion := completeDeletionForRoot(t, fixture, second.ObjectID, "delete-shared-second")
	if err := fixture.object.store.DB().QueryRow(`SELECT count(*) FROM team_object_registry WHERE object_id = 'shared-derivative'`).Scan(&derivative); err != nil {
		t.Fatal(err)
	}
	if derivative != 0 {
		t.Fatalf("final unsupported derivative rows=%d", derivative)
	}
	var purgedDischarges int
	if err := fixture.object.store.DB().QueryRow(`
		SELECT count(*) FROM team_deletion_discharges
		 WHERE operation_id = ? AND object_id = 'shared-derivative'
		   AND disposition = 'purged'`, secondCompletion.OperationID).
		Scan(&purgedDischarges); err != nil {
		t.Fatal(err)
	}
	if purgedDischarges != 1 {
		t.Fatalf("final derivative purged discharges=%d", purgedDischarges)
	}
}

func TestConcurrentDeletionFrontiersConvergeWhenSharedDerivativeLosesFinalSupport(t *testing.T) {
	fixture := newTeamDeletionFixture(t)
	defer fixture.close(t)
	first := fixture.storeMemory(t, "concurrent-first")
	second := fixture.storeMemory(t, "concurrent-second")
	insertDeletionDerivative(t, fixture, "concurrent-derivative", "graph_entity")
	linkDeletionContribution(t, fixture, first.ObjectID, "concurrent-derivative")
	linkDeletionContribution(t, fixture, second.ObjectID, "concurrent-derivative")
	firstStart, err := fixture.object.store.StartTeamDeletion(context.Background(),
		fixture.startRequest(first.ObjectID, "delete-concurrent-first"))
	if err != nil {
		t.Fatal(err)
	}
	secondStart, err := fixture.object.store.StartTeamDeletion(context.Background(),
		fixture.startRequest(second.ObjectID, "delete-concurrent-second"))
	if err != nil {
		t.Fatal(err)
	}
	claims, err := fixture.object.store.ClaimTeamDeletionJobs(context.Background(), TeamDeletionClaimRequest{
		WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
		Limit: 2, LeaseTTL: time.Minute,
	})
	if err != nil || len(claims) != 2 {
		t.Fatalf("claims = %+v, %v", claims, err)
	}
	byOperation := make(map[string]TeamDeletionClaim, len(claims))
	for _, claim := range claims {
		byOperation[claim.OperationID] = claim
	}
	for _, started := range []TeamDeletionResult{firstStart, secondStart} {
		claim, ok := byOperation[started.OperationID]
		if !ok {
			t.Fatalf("missing claim for %s", started.OperationID)
		}
		if _, err := fixture.object.store.CompleteTeamDeletion(context.Background(), TeamDeletionCompletionRequest{
			WriterID: fixture.object.lease.WriterID, WriterToken: fixture.object.lease.Token,
			OperationID: started.OperationID, LeaseToken: claim.LeaseToken,
		}); err != nil {
			t.Fatalf("complete %s: %v", started.OperationID, err)
		}
	}
	var derivative, completeOperations int
	if err := fixture.object.store.DB().QueryRow(`
		SELECT count(*) FROM team_object_registry WHERE object_id = 'concurrent-derivative'`).Scan(&derivative); err != nil {
		t.Fatal(err)
	}
	if err := fixture.object.store.DB().QueryRow(`
		SELECT count(*) FROM team_deletion_operations WHERE state = 'complete'`).Scan(&completeOperations); err != nil {
		t.Fatal(err)
	}
	if derivative != 0 || completeOperations != 2 {
		t.Fatalf("converged derivative=%d complete_operations=%d", derivative, completeOperations)
	}
}

func TestTeamDeletionFrontierPreservesRecursiveExternalSupport(t *testing.T) {
	fixture := newTeamDeletionFixture(t)
	defer fixture.close(t)
	deleted := fixture.storeMemory(t, "recursive-deleted")
	external := fixture.storeMemory(t, "recursive-external")
	insertDeletionDerivative(t, fixture, "recursive-middle", "graph_entity")
	insertDeletionDerivative(t, fixture, "recursive-leaf", "graph_fact")
	linkDeletionContribution(t, fixture, deleted.ObjectID, "recursive-middle")
	linkDeletionContribution(t, fixture, external.ObjectID, "recursive-middle")
	linkDeletionContribution(t, fixture, "recursive-middle", "recursive-leaf")

	completeDeletionForRoot(t, fixture, deleted.ObjectID, "delete-recursive-root")
	rows, err := fixture.object.store.DB().Query(`
		SELECT object_id FROM team_object_registry
		 WHERE object_id IN ('recursive-middle','recursive-leaf') ORDER BY object_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if !reflect.DeepEqual(got, []string{"recursive-leaf", "recursive-middle"}) {
		t.Fatalf("recursive externally supported objects = %v", got)
	}
	var rootEdge, externalEdge, childEdge int
	for query, dest := range map[string]*int{
		`SELECT count(*) FROM team_object_contributions WHERE parent_object_id = '` + deleted.ObjectID + `'`:                                                &rootEdge,
		`SELECT count(*) FROM team_object_contributions WHERE parent_object_id = '` + external.ObjectID + `' AND derivative_object_id = 'recursive-middle'`: &externalEdge,
		`SELECT count(*) FROM team_object_contributions WHERE parent_object_id = 'recursive-middle' AND derivative_object_id = 'recursive-leaf'`:            &childEdge,
	} {
		if err := fixture.object.store.DB().QueryRow(query).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	if rootEdge != 0 || externalEdge != 1 || childEdge != 1 {
		t.Fatalf("edges root=%d external=%d child=%d", rootEdge, externalEdge, childEdge)
	}
}

func TestCompleteTeamDeletionRemovesEverySemanticMaterializationAndDerivative(t *testing.T) {
	fixture := newTeamSemanticProjectionFixture(t)
	defer fixture.graph.object.store.Close()
	for _, kind := range []string{"graph", "claim", "continuity"} {
		claim := fixture.claim(t, fixture.root.ObjectID, kind)
		completeStructuredProjection(t, fixture, kind, semanticProjectionRequest(fixture, claim))
	}
	embeddingClaim := fixture.claim(t, fixture.root.ObjectID, "embedding")
	if _, err := fixture.graph.object.store.CompleteTeamSemanticEmbeddingProjection(
		context.Background(), teamSemanticEmbeddingRequest(t, fixture, embeddingClaim, 2)); err != nil {
		t.Fatalf("complete semantic embeddings: %v", err)
	}
	materializationTables := []string{
		"team_graph_delta_inputs", "team_semantic_projection_intents",
		"team_semantic_materializations", "team_graph_materializations",
		"team_assertion_materializations", "team_continuity_materializations",
		"team_semantic_embeddings", "team_projection_outputs",
		"team_object_contributions", "team_object_storage_map", "team_projection_jobs",
	}
	for _, table := range materializationTables {
		var before int
		if err := fixture.graph.object.store.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if before == 0 {
			t.Fatalf("semantic fixture did not populate %s", table)
		}
	}
	deleteRequest := TeamDeletionStartRequest{
		Authorization: TeamMutationAuthorizationRequest{
			PrincipalID:    fixture.graph.object.actor.binding.AgentPrincipalID,
			OAuthClientKey: fixture.graph.object.actor.clientKey,
			Action:         teamauth.ActionDelete,
			Capabilities:   []teamauth.Capability{teamauth.CapabilityDelete},
			Context: teamauth.ActiveContext{
				TeamID:    fixture.graph.object.bootstrap.TeamID,
				ProjectID: fixture.graph.write.ActiveContext.ProjectID,
				RepoID:    fixture.graph.write.ActiveContext.RepoID,
				SessionID: fixture.graph.write.ActiveContext.SessionID,
			},
			ExistingObjectID: fixture.root.ObjectID,
		},
		Writer:         fixture.graph.object.request.Writer,
		RequestID:      "request-delete-semantic-root",
		IdempotencyKey: "delete-semantic-root-0001",
	}
	started, err := fixture.graph.object.store.StartTeamDeletion(context.Background(), deleteRequest)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := fixture.graph.object.store.ClaimTeamDeletionJobs(context.Background(), TeamDeletionClaimRequest{
		WriterID:    fixture.graph.object.lease.WriterID,
		WriterToken: fixture.graph.object.lease.Token,
		Limit:       1, LeaseTTL: time.Minute,
	})
	if err != nil || len(claims) != 1 || claims[0].OperationID != started.OperationID {
		t.Fatalf("deletion claim = %+v, %v", claims, err)
	}
	if _, err := fixture.graph.object.store.CompleteTeamDeletion(context.Background(), TeamDeletionCompletionRequest{
		WriterID:    fixture.graph.object.lease.WriterID,
		WriterToken: fixture.graph.object.lease.Token,
		OperationID: started.OperationID, LeaseToken: claims[0].LeaseToken,
	}); err != nil {
		t.Fatal(err)
	}
	for _, table := range materializationTables {
		var after int
		if err := fixture.graph.object.store.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after != 0 {
			t.Fatalf("%s retains %d rows after complete deletion", table, after)
		}
	}
	var registryRows int
	if err := fixture.graph.object.store.DB().QueryRow(`SELECT count(*) FROM team_object_registry`).Scan(&registryRows); err != nil {
		t.Fatal(err)
	}
	if registryRows != 1 {
		t.Fatalf("registry rows after semantic deletion = %d, want terminal root only", registryRows)
	}
}
