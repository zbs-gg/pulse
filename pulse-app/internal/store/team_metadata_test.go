package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

func TestTeamMetadataStatusInspectAndOwnAuditAreScopedAndContentFree(t *testing.T) {
	ctx := context.Background()
	fixture := newTeamObjectWriteFixture(t)
	defer fixture.store.Close()
	first, err := fixture.store.StoreTeamObject(ctx, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := cloneTeamObjectWriteRequest(fixture.request)
	secondRequest.RequestID = "request-metadata-second"
	secondRequest.IdempotencyKey = "idempotency-metadata-second"
	secondRequest.Body = []byte("second synthetic body must not enter metadata")
	digest := sha256.Sum256(secondRequest.Body)
	secondRequest.BodyDigest = fmt.Sprintf("%x", digest)
	if _, err := fixture.store.StoreTeamObject(ctx, secondRequest); err != nil {
		t.Fatal(err)
	}

	status, err := fixture.store.ReadTeamStatusMetadata(ctx, fixture.actor.binding.AgentPrincipalID)
	if err != nil {
		t.Fatal(err)
	}
	if status.StoreID != fixture.bootstrap.StoreID || status.TeamID != fixture.bootstrap.TeamID ||
		status.PrincipalID != fixture.actor.binding.AgentPrincipalID || status.PrincipalKind != "agent" ||
		status.HumanPrincipalID != fixture.actor.member.PrincipalID ||
		status.BindingID != fixture.actor.binding.BindingID || status.PublicEnabled ||
		status.ActivationState != TeamActivationInactive {
		t.Fatalf("status metadata = %+v", status)
	}

	filterRequest := CandidateFilterRequest{
		PrincipalID:    fixture.actor.binding.AgentPrincipalID,
		Capabilities:   []teamauth.Capability{teamauth.CapabilityRead},
		Context:        teamauth.ActiveContext{TeamID: fixture.bootstrap.TeamID},
		PrivacyCeiling: "normal",
	}
	metadata, err := fixture.store.InspectAuthorizedTeamObject(ctx, filterRequest, first.ObjectID)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.ObjectID != first.ObjectID || metadata.ObjectKind != fixture.permit.ObjectKind() ||
		metadata.ScopeType != string(fixture.permit.EffectiveTarget().Type) ||
		metadata.ProjectionState != TeamProjectionStatePending || metadata.Lifecycle != "active" ||
		metadata.Generation != 1 || metadata.DeletionState != "" {
		t.Fatalf("inspect metadata = %+v", metadata)
	}
	ownerFilter := filterRequest
	ownerFilter.PrincipalID = fixture.bootstrap.OwnerPrincipalID
	if _, err := fixture.store.InspectAuthorizedTeamObject(ctx, ownerFilter, first.ObjectID); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("other owner inspect error = %v", err)
	}

	page, err := fixture.store.ReadOwnTeamAudit(ctx, fixture.actor.binding.AgentPrincipalID, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.NextCursor == "" ||
		page.Events[0].ActorPrincipalID != fixture.actor.binding.AgentPrincipalID {
		t.Fatalf("first own-audit page = %+v", page)
	}
	next, err := fixture.store.ReadOwnTeamAudit(ctx, fixture.actor.binding.AgentPrincipalID, page.NextCursor, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Events) != 1 || next.Events[0].EventID == page.Events[0].EventID {
		t.Fatalf("next own-audit page = %+v", next)
	}
	var ownerEventID string
	if err := fixture.store.DB().QueryRow(`
		SELECT event_id FROM team_audit_events
		 WHERE actor_principal_id = ? ORDER BY occurred_at LIMIT 1`,
		fixture.bootstrap.OwnerPrincipalID).Scan(&ownerEventID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ReadOwnTeamAudit(ctx, fixture.actor.binding.AgentPrincipalID, ownerEventID, 1); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("foreign audit cursor error = %v", err)
	}
}
