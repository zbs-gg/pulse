package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

type mutationAuthorizationActor struct {
	member    TeamMember
	binding   AgentBinding
	clientKey string
}

func addMutationAuthorizationActor(t *testing.T, s *Store, bootstrap BootstrapResult, subject, role string) mutationAuthorizationActor {
	t.Helper()
	member, err := s.AddTeamMember(context.Background(), AddTeamMemberRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example",
		Subject:          subject,
		Role:             role,
	})
	if err != nil {
		t.Fatalf("add %s: %v", role, err)
	}
	clientID := subject + "-agent"
	binding, err := s.RegisterAgentBinding(context.Background(), RegisterAgentBindingRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example",
		Subject:          subject,
		ClientID:         clientID,
	})
	if err != nil {
		t.Fatalf("bind %s: %v", role, err)
	}
	return mutationAuthorizationActor{
		member: member, binding: binding,
		clientKey: teamauth.OAuthClientKey("https://issuer.example", clientID),
	}
}

func mutationWriteRequest(bootstrap BootstrapResult, actor mutationAuthorizationActor) TeamMutationAuthorizationRequest {
	return TeamMutationAuthorizationRequest{
		PrincipalID:    actor.binding.AgentPrincipalID,
		OAuthClientKey: actor.clientKey,
		Action:         teamauth.ActionWrite,
		Capabilities:   []teamauth.Capability{teamauth.CapabilityWrite},
		Context:        teamauth.ActiveContext{TeamID: bootstrap.TeamID},
		ObjectKind:     "memory",
	}
}

func requirePermitRecheck(t *testing.T, s *Store, permit TeamMutationPermit) {
	t.Helper()
	tx, err := s.DB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := s.RecheckTeamMutationPermitTx(context.Background(), tx, permit); err != nil {
		t.Fatalf("recheck permit: %v", err)
	}
}

func TestAuthorizeTeamMutationDerivesMemberAndReviewerPersonalTargets(t *testing.T) {
	ctx := context.Background()
	for _, role := range []string{"member", "reviewer"} {
		t.Run(role, func(t *testing.T) {
			s, bootstrap := bootstrapTeamStore(t)
			defer s.Close()
			actor := addMutationAuthorizationActor(t, s, bootstrap, role+"-subject", role)
			request := mutationWriteRequest(bootstrap, actor)

			permit, err := s.AuthorizeTeamMutation(ctx, request)
			if err != nil {
				t.Fatalf("authorize %s: %v", role, err)
			}
			attribution := permit.Attribution()
			if attribution.StoreID != bootstrap.StoreID || attribution.TeamID != bootstrap.TeamID ||
				attribution.ActorPrincipalID != actor.binding.AgentPrincipalID ||
				attribution.HumanPrincipalID != actor.member.PrincipalID ||
				attribution.OAuthClientKey != actor.clientKey {
				t.Fatalf("attribution = %+v", attribution)
			}
			target := permit.EffectiveTarget()
			if target.TeamID != bootstrap.TeamID || target.Type != teamauth.ScopePersonal ||
				target.ID != actor.member.PrincipalID || target.OwnerPrincipalID != actor.member.PrincipalID ||
				target.Lifecycle != teamauth.LifecycleActive || target.Generation != 1 {
				t.Fatalf("derived target = %+v", target)
			}
			if permit.Action() != teamauth.ActionWrite || permit.ObjectKind() != "memory" ||
				permit.ExistingObjectID() != "" || permit.PolicyEpoch().Global == 0 {
				t.Fatalf("permit metadata is incomplete")
			}

			// Caller-owned request values and getter results cannot widen or mutate
			// the sealed permit used inside the write transaction.
			request.Capabilities[0] = teamauth.CapabilityRead
			request.Context.TeamID = "team-other"
			target.TeamID = "team-other"
			requirePermitRecheck(t, s, permit)

			withoutWrite := mutationWriteRequest(bootstrap, actor)
			withoutWrite.Capabilities = []teamauth.Capability{teamauth.CapabilityRead}
			if _, err := s.AuthorizeTeamMutation(ctx, withoutWrite); !errors.Is(err, ErrTeamPolicyDenied) {
				t.Fatalf("missing capability error = %v", err)
			}
			wrongClient := mutationWriteRequest(bootstrap, actor)
			wrongClient.OAuthClientKey = teamauth.OAuthClientKey("https://issuer.example", "unbound-client")
			if _, err := s.AuthorizeTeamMutation(ctx, wrongClient); !errors.Is(err, ErrTeamPolicyDenied) {
				t.Fatalf("unbound client attribution error = %v", err)
			}
		})
	}
}

func TestAuthorizeTeamMutationUsesCanonicalProjectAndExactServiceGrants(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	member := addMutationAuthorizationActor(t, s, bootstrap, "project-member", "member")
	reviewer := addMutationAuthorizationActor(t, s, bootstrap, "project-reviewer", "reviewer")
	project, err := s.CreateTeamProject(ctx, bootstrap.OwnerPrincipalID, "Foreign owner project")
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []mutationAuthorizationActor{member, reviewer} {
		if _, err := s.GrantProjectAccess(ctx, GrantProjectAccessRequest{
			ActorPrincipalID:  bootstrap.OwnerPrincipalID,
			ProjectID:         project.ProjectID,
			TargetPrincipalID: actor.binding.AgentPrincipalID,
			AccessLevel:       "write",
		}); err != nil {
			t.Fatal(err)
		}
		request := mutationWriteRequest(bootstrap, actor)
		request.Context.ProjectID = project.ProjectID
		request.RequestedScope = &teamauth.CanonicalScope{Type: teamauth.ScopeProject, ID: project.ProjectID}
		permit, err := s.AuthorizeTeamMutation(ctx, request)
		if err != nil {
			t.Fatalf("project grant authorize: %v", err)
		}
		if got := permit.EffectiveTarget().OwnerPrincipalID; got != actor.member.PrincipalID {
			t.Fatalf("new project owner = %q, want delegated human %q", got, actor.member.PrincipalID)
		}
		wrongContext := request
		wrongContext.Context.ProjectID = "project-other"
		if _, err := s.AuthorizeTeamMutation(ctx, wrongContext); !errors.Is(err, ErrTeamPolicyDenied) {
			t.Fatalf("matching grant widened mismatched context: %v", err)
		}
	}

	insertPolicyObject(t, s, bootstrap, "foreign-owned-project-object", "project", project.ProjectID, bootstrap.OwnerPrincipalID)
	existing := mutationWriteRequest(bootstrap, member)
	existing.ObjectKind = ""
	existing.ExistingObjectID = "foreign-owned-project-object"
	existing.Context.ProjectID = project.ProjectID
	permit, err := s.AuthorizeTeamMutation(ctx, existing)
	if err != nil {
		t.Fatalf("authoritative existing project object: %v", err)
	}
	if got := permit.EffectiveTarget().OwnerPrincipalID; got != bootstrap.OwnerPrincipalID {
		t.Fatalf("authoritative owner changed to %q", got)
	}

	spoofed := mutationWriteRequest(bootstrap, member)
	spoofed.Context.ProjectID = project.ProjectID
	spoofed.RequestedScope = &teamauth.CanonicalScope{
		Type: teamauth.ScopeProject, ID: project.ProjectID,
		OwnerPrincipalID: bootstrap.OwnerPrincipalID,
	}
	if _, err := s.AuthorizeTeamMutation(ctx, spoofed); !errors.Is(err, ErrTeamPolicyDenied) {
		t.Fatalf("spoofed owner error = %v", err)
	}

	service, err := s.RegisterServicePrincipal(ctx, RegisterServicePrincipalRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example",
		ClientID:         "projection-service",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantProjectAccess(ctx, GrantProjectAccessRequest{
		ActorPrincipalID:  bootstrap.OwnerPrincipalID,
		ProjectID:         project.ProjectID,
		TargetPrincipalID: service.PrincipalID,
		AccessLevel:       "write",
	}); err != nil {
		t.Fatal(err)
	}
	serviceRequest := TeamMutationAuthorizationRequest{
		PrincipalID:    service.PrincipalID,
		OAuthClientKey: teamauth.OAuthClientKey("https://issuer.example", "projection-service"),
		Action:         teamauth.ActionWrite,
		Capabilities:   []teamauth.Capability{teamauth.CapabilityWrite},
		Context: teamauth.ActiveContext{
			TeamID: bootstrap.TeamID, ProjectID: project.ProjectID,
		},
		ObjectKind: "memory",
		RequestedScope: &teamauth.CanonicalScope{
			Type: teamauth.ScopeProject, ID: project.ProjectID,
		},
	}
	if _, err := s.AuthorizeTeamMutation(ctx, serviceRequest); !errors.Is(err, ErrTeamPolicyDenied) {
		t.Fatalf("service without object grant error = %v", err)
	}
	insertServiceMutationGrant(t, s, bootstrap.TeamID, service.PrincipalID, "graph", project.ProjectID, "service-graph")
	if _, err := s.AuthorizeTeamMutation(ctx, serviceRequest); !errors.Is(err, ErrTeamPolicyDenied) {
		t.Fatalf("wrong-kind service grant error = %v", err)
	}
	insertServiceMutationGrant(t, s, bootstrap.TeamID, service.PrincipalID, "memory", project.ProjectID, "service-memory")
	servicePermit, err := s.AuthorizeTeamMutation(ctx, serviceRequest)
	if err != nil {
		t.Fatalf("exact service grants authorize: %v", err)
	}
	if got := servicePermit.EffectiveTarget().OwnerPrincipalID; got != service.PrincipalID {
		t.Fatalf("service target owner = %q, want %q", got, service.PrincipalID)
	}
	requirePermitRecheck(t, s, servicePermit)
}

func insertServiceMutationGrant(t *testing.T, s *Store, teamID, principalID, objectKind, projectID, grantID string) {
	t.Helper()
	if _, err := s.DB().Exec(`
		INSERT INTO team_service_object_grants(
			grant_id, team_id, service_principal_id, object_kind, action,
			scope_type, scope_id, status, auth_epoch, created_at)
		VALUES (?, ?, ?, ?, 'write', 'project', ?, 'active', 1,
			'2026-07-10T00:00:00Z')`, grantID, teamID, principalID, objectKind, projectID); err != nil {
		t.Fatalf("insert service object grant: %v", err)
	}
}

func TestAuthorizeTeamMutationConcealsAbsentInaccessibleAndCrossTeamIDs(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	member := addMutationAuthorizationActor(t, s, bootstrap, "conceal-member", "member")
	insertPolicyObject(t, s, bootstrap, "owner-private", "personal", bootstrap.OwnerPrincipalID, bootstrap.OwnerPrincipalID)

	request := mutationWriteRequest(bootstrap, member)
	request.ObjectKind = ""
	request.ExistingObjectID = "owner-private"
	_, inaccessibleErr := s.AuthorizeTeamMutation(ctx, request)
	request.ExistingObjectID = "absent-object"
	_, absentErr := s.AuthorizeTeamMutation(ctx, request)
	if !errors.Is(inaccessibleErr, ErrConcealedNotFound) || !errors.Is(absentErr, ErrConcealedNotFound) ||
		inaccessibleErr.Error() != absentErr.Error() {
		t.Fatalf("concealment differs: inaccessible=%v absent=%v", inaccessibleErr, absentErr)
	}

	crossTeam := mutationWriteRequest(bootstrap, member)
	crossTeam.RequestedScope = &teamauth.CanonicalScope{
		TeamID: "team-other", Type: teamauth.ScopePersonal, ID: member.member.PrincipalID,
	}
	if _, err := s.AuthorizeTeamMutation(ctx, crossTeam); !errors.Is(err, ErrTeamPolicyDenied) {
		t.Fatalf("cross-team target error = %v", err)
	}
	crossContext := mutationWriteRequest(bootstrap, member)
	crossContext.Context.TeamID = "team-other"
	if _, err := s.AuthorizeTeamMutation(ctx, crossContext); !errors.Is(err, ErrTeamPolicyDenied) {
		t.Fatalf("cross-team context error = %v", err)
	}
}

func TestRecheckTeamMutationPermitFailsAfterRevocation(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	member := addMutationAuthorizationActor(t, s, bootstrap, "revoked-member", "member")
	permit, err := s.AuthorizeTeamMutation(ctx, mutationWriteRequest(bootstrap, member))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeMembership(ctx, bootstrap.OwnerPrincipalID, member.member.PrincipalID); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := s.RecheckTeamMutationPermitTx(ctx, tx, permit); !errors.Is(err, ErrTeamPolicyEpochChanged) {
		t.Fatalf("revoked permit recheck error = %v", err)
	}
}

func TestRecheckTeamMutationPermitValidatesEveryEpochLayer(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *Store, BootstrapResult, mutationAuthorizationActor)
	}{
		{name: "global", mutate: func(t *testing.T, s *Store, _ BootstrapResult, _ mutationAuthorizationActor) {
			if _, err := s.DB().Exec(`UPDATE team_stores SET auth_epoch = auth_epoch + 1 WHERE singleton = 1`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "policy", mutate: func(t *testing.T, s *Store, _ BootstrapResult, _ mutationAuthorizationActor) {
			if _, err := s.DB().Exec(`UPDATE team_policy_metadata SET policy_epoch = policy_epoch + 1`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "principal", mutate: func(t *testing.T, s *Store, _ BootstrapResult, actor mutationAuthorizationActor) {
			if _, err := s.DB().Exec(`UPDATE team_principals SET auth_epoch = auth_epoch + 1 WHERE principal_id = ?`, actor.binding.AgentPrincipalID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "membership", mutate: func(t *testing.T, s *Store, _ BootstrapResult, actor mutationAuthorizationActor) {
			if _, err := s.DB().Exec(`UPDATE team_memberships SET auth_epoch = auth_epoch + 1 WHERE principal_id = ?`, actor.member.PrincipalID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "binding", mutate: func(t *testing.T, s *Store, _ BootstrapResult, actor mutationAuthorizationActor) {
			if _, err := s.DB().Exec(`UPDATE team_agent_bindings SET auth_epoch = auth_epoch + 1 WHERE binding_id = ?`, actor.binding.BindingID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, bootstrap := bootstrapTeamStore(t)
			defer s.Close()
			actor := addMutationAuthorizationActor(t, s, bootstrap, "epoch-"+test.name, "member")
			permit, err := s.AuthorizeTeamMutation(context.Background(), mutationWriteRequest(bootstrap, actor))
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, s, bootstrap, actor)
			tx, err := s.DB().BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := s.RecheckTeamMutationPermitTx(context.Background(), tx, permit); !errors.Is(err, ErrTeamPolicyEpochChanged) {
				t.Fatalf("stale %s epoch error = %v", test.name, err)
			}
		})
	}
}

func TestRecheckTeamMutationPermitReloadsExactProjectGrant(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	member := addMutationAuthorizationActor(t, s, bootstrap, "grant-recheck-member", "member")
	project, err := s.CreateTeamProject(ctx, bootstrap.OwnerPrincipalID, "Grant recheck")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := s.GrantProjectAccess(ctx, GrantProjectAccessRequest{
		ActorPrincipalID:  bootstrap.OwnerPrincipalID,
		ProjectID:         project.ProjectID,
		TargetPrincipalID: member.binding.AgentPrincipalID,
		AccessLevel:       "write",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := mutationWriteRequest(bootstrap, member)
	request.Context.ProjectID = project.ProjectID
	request.RequestedScope = &teamauth.CanonicalScope{Type: teamauth.ScopeProject, ID: project.ProjectID}
	permit, err := s.AuthorizeTeamMutation(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	// Model a legacy/direct grant revocation that changed the exact grant but
	// did not bump the global epoch. The transaction recheck still fails closed.
	if _, err := s.DB().Exec(`
		UPDATE team_project_grants
		   SET status = 'revoked', auth_epoch = auth_epoch + 1,
		       revoked_at = '2026-07-10T00:01:00Z'
		 WHERE grant_id = ?`, grant.GrantID); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := s.RecheckTeamMutationPermitTx(ctx, tx, permit); !errors.Is(err, ErrTeamPolicyEpochChanged) {
		t.Fatalf("revoked exact project grant error = %v", err)
	}
}

func TestTeamMutationPermitSurvivesStoreRestart(t *testing.T) {
	ctx := context.Background()
	root := testBootstrapRoot()
	path := filepath.Join(t.TempDir(), "team.db")
	s, err := OpenTeam(path, reviewTeamOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := s.BootstrapTeam(ctx, BootstrapTeamRequest{
		TeamName: "Restart team", PresentedRoot: root,
	})
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	member := addMutationAuthorizationActor(t, s, bootstrap, "restart-member", "member")
	permit, err := s.AuthorizeTeamMutation(ctx, mutationWriteRequest(bootstrap, member))
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenTeam(path, reviewTeamOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	requirePermitRecheck(t, reopened, permit)
}

func TestRecheckTeamMutationPermitRequiresTransaction(t *testing.T) {
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	member := addMutationAuthorizationActor(t, s, bootstrap, "nil-tx-member", "member")
	permit, err := s.AuthorizeTeamMutation(context.Background(), mutationWriteRequest(bootstrap, member))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecheckTeamMutationPermitTx(context.Background(), (*sql.Tx)(nil), permit); !errors.Is(err, ErrTeamPolicyEpochChanged) {
		t.Fatalf("nil transaction error = %v", err)
	}
}
