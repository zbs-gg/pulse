package store

import (
	"context"
	"errors"
	"testing"
)

func TestRevocationInvalidatesDependentProjectGrants(t *testing.T) {
	ctx := context.Background()

	t.Run("agent binding", func(t *testing.T) {
		s, bootstrap := bootstrapTeamStore(t)
		defer s.Close()
		binding, err := s.RegisterAgentBinding(ctx, RegisterAgentBindingRequest{
			ActorPrincipalID: bootstrap.OwnerPrincipalID,
			Issuer:           testBootstrapRoot().Issuer,
			Subject:          testBootstrapRoot().Subject,
			ClientID:         "granted-agent",
		})
		if err != nil {
			t.Fatal(err)
		}
		project, err := s.CreateTeamProject(ctx, bootstrap.OwnerPrincipalID, "Agent Project")
		if err != nil {
			t.Fatal(err)
		}
		grant, err := s.GrantProjectAccess(ctx, GrantProjectAccessRequest{
			ActorPrincipalID: bootstrap.OwnerPrincipalID, ProjectID: project.ProjectID,
			TargetPrincipalID: binding.AgentPrincipalID, AccessLevel: "write",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.RevokeAgentBinding(ctx, bootstrap.OwnerPrincipalID, binding.BindingID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ResolveProjectGrant(ctx, project.ProjectID, binding.AgentPrincipalID); !errors.Is(err, ErrProjectGrantRequired) {
			t.Fatalf("agent grant after binding revoke = %v", err)
		}
		assertGrantStatus(t, s, grant.GrantID, "revoked")
	})

	t.Run("human membership", func(t *testing.T) {
		s, bootstrap := bootstrapTeamStore(t)
		defer s.Close()
		member, err := s.AddTeamMember(ctx, AddTeamMemberRequest{
			ActorPrincipalID: bootstrap.OwnerPrincipalID,
			Issuer:           testBootstrapRoot().Issuer,
			Subject:          "granted-human",
			Role:             "member",
		})
		if err != nil {
			t.Fatal(err)
		}
		project, err := s.CreateTeamProject(ctx, bootstrap.OwnerPrincipalID, "Human Project")
		if err != nil {
			t.Fatal(err)
		}
		grant, err := s.GrantProjectAccess(ctx, GrantProjectAccessRequest{
			ActorPrincipalID: bootstrap.OwnerPrincipalID, ProjectID: project.ProjectID,
			TargetPrincipalID: member.PrincipalID, AccessLevel: "read",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.RevokeMembership(ctx, bootstrap.OwnerPrincipalID, member.PrincipalID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ResolveProjectGrant(ctx, project.ProjectID, member.PrincipalID); !errors.Is(err, ErrProjectGrantRequired) {
			t.Fatalf("human grant after membership revoke = %v", err)
		}
		assertGrantStatus(t, s, grant.GrantID, "revoked")
	})

	t.Run("service principal", func(t *testing.T) {
		s, bootstrap := bootstrapTeamStore(t)
		defer s.Close()
		service, err := s.RegisterServicePrincipal(ctx, RegisterServicePrincipalRequest{
			ActorPrincipalID: bootstrap.OwnerPrincipalID,
			Issuer:           testBootstrapRoot().Issuer,
			ClientID:         "granted-service",
		})
		if err != nil {
			t.Fatal(err)
		}
		project, err := s.CreateTeamProject(ctx, bootstrap.OwnerPrincipalID, "Service Project")
		if err != nil {
			t.Fatal(err)
		}
		grant, err := s.GrantProjectAccess(ctx, GrantProjectAccessRequest{
			ActorPrincipalID: bootstrap.OwnerPrincipalID, ProjectID: project.ProjectID,
			TargetPrincipalID: service.PrincipalID, AccessLevel: "read",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.RevokeServicePrincipal(ctx, bootstrap.OwnerPrincipalID, service.PrincipalID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ResolveProjectGrant(ctx, project.ProjectID, service.PrincipalID); !errors.Is(err, ErrProjectGrantRequired) {
			t.Fatalf("service grant after principal revoke = %v", err)
		}
		assertGrantStatus(t, s, grant.GrantID, "revoked")
	})

	t.Run("agent human principal must stay active", func(t *testing.T) {
		s, bootstrap := bootstrapTeamStore(t)
		defer s.Close()
		binding, err := s.RegisterAgentBinding(ctx, RegisterAgentBindingRequest{
			ActorPrincipalID: bootstrap.OwnerPrincipalID,
			Issuer:           testBootstrapRoot().Issuer,
			Subject:          testBootstrapRoot().Subject,
			ClientID:         "human-state-agent",
		})
		if err != nil {
			t.Fatal(err)
		}
		project, err := s.CreateTeamProject(ctx, bootstrap.OwnerPrincipalID, "Human State Project")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.GrantProjectAccess(ctx, GrantProjectAccessRequest{
			ActorPrincipalID: bootstrap.OwnerPrincipalID, ProjectID: project.ProjectID,
			TargetPrincipalID: binding.AgentPrincipalID, AccessLevel: "read",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().Exec(`
			UPDATE team_principals SET status = 'revoked' WHERE principal_id = ?`, bootstrap.OwnerPrincipalID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ResolveProjectGrant(ctx, project.ProjectID, binding.AgentPrincipalID); !errors.Is(err, ErrProjectGrantRequired) {
			t.Fatalf("agent grant ignored revoked human principal: %v", err)
		}
	})
}

func TestAgentRevocationRollsBackWhenAuditCannotAppend(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	binding, err := s.RegisterAgentBinding(ctx, RegisterAgentBindingRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           testBootstrapRoot().Issuer,
		Subject:          testBootstrapRoot().Subject,
		ClientID:         "rollback-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	project, err := s.CreateTeamProject(ctx, bootstrap.OwnerPrincipalID, "Rollback Project")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := s.GrantProjectAccess(ctx, GrantProjectAccessRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID, ProjectID: project.ProjectID,
		TargetPrincipalID: binding.AgentPrincipalID, AccessLevel: "read",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		CREATE TRIGGER reject_agent_revoke_audit
		BEFORE INSERT ON team_audit_events
		WHEN NEW.action = 'agent_binding.revoke'
		BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeAgentBinding(ctx, bootstrap.OwnerPrincipalID, binding.BindingID); err == nil {
		t.Fatal("agent revoke succeeded without audit")
	}
	if _, err := s.ResolveAgentBinding(ctx, testBootstrapRoot().Issuer, testBootstrapRoot().Subject, "rollback-agent"); err != nil {
		t.Fatalf("binding changed after rollback: %v", err)
	}
	if _, err := s.ResolveProjectGrant(ctx, project.ProjectID, binding.AgentPrincipalID); err != nil {
		t.Fatalf("grant changed after rollback: %v", err)
	}
	assertGrantStatus(t, s, grant.GrantID, "active")
}

func TestProjectCreationAndOwnershipRequireHumanOwner(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	member, err := s.AddTeamMember(ctx, AddTeamMemberRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           testBootstrapRoot().Issuer,
		Subject:          "ordinary-member",
		Role:             "member",
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := s.RegisterServicePrincipal(ctx, RegisterServicePrincipalRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           testBootstrapRoot().Issuer,
		ClientID:         "project-service",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTeamProject(ctx, member.PrincipalID, "Member Owned"); !errors.Is(err, ErrMembershipRequired) {
		t.Fatalf("member project creation error = %v", err)
	}
	if _, err := s.CreateTeamProject(ctx, service.PrincipalID, "Service Owned"); !errors.Is(err, ErrMembershipRequired) {
		t.Fatalf("service project creation error = %v", err)
	}

	secondOwner, err := s.AddTeamMember(ctx, AddTeamMemberRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           testBootstrapRoot().Issuer,
		Subject:          "second-owner",
		Role:             "owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = secondOwner
	project, err := s.CreateTeamProject(ctx, bootstrap.OwnerPrincipalID, "Owned Project")
	if err != nil {
		t.Fatal(err)
	}
	if project.OwnerPrincipalID != bootstrap.OwnerPrincipalID {
		t.Fatalf("project owner = %q", project.OwnerPrincipalID)
	}
	if err := s.RevokeMembership(ctx, secondOwner.PrincipalID, bootstrap.OwnerPrincipalID); !errors.Is(err, ErrProjectOwnershipTransferRequired) {
		t.Fatalf("project owner revoke error = %v", err)
	}
	if _, err := s.ResolveTeamPrincipal(ctx, bootstrap.OwnerPrincipalID); err != nil {
		t.Fatalf("project owner changed after blocked revoke: %v", err)
	}

	if err := s.RevokeMembership(ctx, bootstrap.OwnerPrincipalID, service.PrincipalID); !errors.Is(err, ErrHumanPrincipalRequired) {
		t.Fatalf("service via membership revoke error = %v", err)
	}
	if _, err := s.ResolveServiceIdentity(ctx, testBootstrapRoot().Issuer, "project-service"); err != nil {
		t.Fatalf("service changed by human membership revoke: %v", err)
	}
}

func assertGrantStatus(t *testing.T, s *Store, grantID, want string) {
	t.Helper()
	var status string
	if err := s.DB().QueryRow(`SELECT status FROM team_project_grants WHERE grant_id = ?`, grantID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != want {
		t.Fatalf("grant %s status = %q, want %q", grantID, status, want)
	}
}
