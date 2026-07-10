package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestOAuthClientRegistryPreventsReassignmentAndCrossKind(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	member, err := s.AddTeamMember(ctx, AddTeamMemberRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example",
		Subject:          "second-human",
		Role:             "member",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = member

	ownerBinding, err := s.RegisterAgentBinding(ctx, RegisterAgentBindingRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example",
		Subject:          testBootstrapRoot().Subject,
		ClientID:         "shared-client",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterAgentBinding(ctx, RegisterAgentBindingRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example",
		Subject:          "second-human",
		ClientID:         "shared-client",
	}); !errors.Is(err, ErrInvalidTeamIdentityMutation) {
		t.Fatalf("client reassignment error = %v", err)
	}
	if _, err := s.RegisterServicePrincipal(ctx, RegisterServicePrincipalRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example",
		ClientID:         "shared-client",
	}); !errors.Is(err, ErrInvalidTeamIdentityMutation) {
		t.Fatalf("agent-to-service client reassignment error = %v", err)
	}

	resolved, err := s.ResolveOAuthClient(ctx, "https://issuer.example", "shared-client")
	if err != nil {
		t.Fatalf("resolve OAuth client: %v", err)
	}
	if resolved.Kind != "agent" || resolved.BindingID != ownerBinding.BindingID || resolved.PrincipalID != ownerBinding.AgentPrincipalID {
		t.Fatalf("resolved OAuth client = %+v", resolved)
	}
	var persistedKey string
	if err := s.DB().QueryRow(`SELECT oauth_client_key FROM team_oauth_clients`).Scan(&persistedKey); err != nil {
		t.Fatal(err)
	}
	if len(persistedKey) != 64 || strings.Contains(persistedKey, "shared-client") || strings.Contains(persistedKey, "issuer.example") {
		t.Fatalf("OAuth registry persisted non-opaque key %q", persistedKey)
	}
}

func TestOAuthServiceClientCannotBecomeAgentClient(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	service, err := s.RegisterServicePrincipal(ctx, RegisterServicePrincipalRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example",
		ClientID:         "service-first-client",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterAgentBinding(ctx, RegisterAgentBindingRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example",
		Subject:          testBootstrapRoot().Subject,
		ClientID:         "service-first-client",
	}); !errors.Is(err, ErrInvalidTeamIdentityMutation) {
		t.Fatalf("service-to-agent client reassignment error = %v", err)
	}
	resolved, err := s.ResolveOAuthClient(ctx, "https://issuer.example", "service-first-client")
	if err != nil || resolved.Kind != "service" || resolved.PrincipalID != service.PrincipalID || resolved.BindingID != "" {
		t.Fatalf("resolved service OAuth client = %+v, %v", resolved, err)
	}
}

func TestTeamIdentityMutationRejectsSurroundingWhitespace(t *testing.T) {
	ctx := context.Background()
	s, bootstrap := bootstrapTeamStore(t)
	defer s.Close()
	if _, err := s.AddTeamMember(ctx, AddTeamMemberRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example",
		Subject:          " member-subject ",
		Role:             "member",
	}); !errors.Is(err, ErrInvalidTeamIdentityMutation) {
		t.Fatalf("member subject whitespace error = %v", err)
	}
	if _, err := s.RegisterAgentBinding(ctx, RegisterAgentBindingRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example",
		Subject:          testBootstrapRoot().Subject,
		ClientID:         " agent-client ",
	}); !errors.Is(err, ErrInvalidTeamIdentityMutation) {
		t.Fatalf("agent client whitespace error = %v", err)
	}
	if _, err := s.RegisterServicePrincipal(ctx, RegisterServicePrincipalRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example ",
		ClientID:         "service-client",
	}); !errors.Is(err, ErrInvalidTeamIdentityMutation) {
		t.Fatalf("service issuer whitespace error = %v", err)
	}
}
