package store

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestConcurrentIdentityAndGrantReplayIsIdempotent(t *testing.T) {
	ctx := context.Background()
	first, bootstrap := bootstrapTeamStore(t)
	defer first.Close()
	second, err := OpenTeam(first.DBPath(), reviewTeamOptions(testBootstrapRoot()))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	t.Run("member", func(t *testing.T) {
		request := AddTeamMemberRequest{
			ActorPrincipalID: bootstrap.OwnerPrincipalID,
			Issuer:           "https://issuer.example",
			Subject:          "concurrent-member",
			Role:             "member",
		}
		results := concurrently(t, first, second, func(s *Store) (TeamMember, error) {
			return s.AddTeamMember(ctx, request)
		})
		if results[0].err != nil || results[1].err != nil ||
			results[0].value.PrincipalID != results[1].value.PrincipalID ||
			results[0].value.MembershipID != results[1].value.MembershipID {
			t.Fatalf("concurrent member results = %+v", results)
		}
	})

	t.Run("agent binding", func(t *testing.T) {
		request := RegisterAgentBindingRequest{
			ActorPrincipalID: bootstrap.OwnerPrincipalID,
			Issuer:           "https://issuer.example",
			Subject:          testBootstrapRoot().Subject,
			ClientID:         "concurrent-agent-client",
		}
		results := concurrently(t, first, second, func(s *Store) (AgentBinding, error) {
			return s.RegisterAgentBinding(ctx, request)
		})
		if results[0].err != nil || results[1].err != nil ||
			results[0].value.BindingID != results[1].value.BindingID ||
			results[0].value.AgentPrincipalID != results[1].value.AgentPrincipalID {
			t.Fatalf("concurrent agent results = %+v", results)
		}
	})

	t.Run("service", func(t *testing.T) {
		request := RegisterServicePrincipalRequest{
			ActorPrincipalID: bootstrap.OwnerPrincipalID,
			Issuer:           "https://issuer.example",
			ClientID:         "concurrent-service-client",
		}
		results := concurrently(t, first, second, func(s *Store) (ServicePrincipal, error) {
			return s.RegisterServicePrincipal(ctx, request)
		})
		if results[0].err != nil || results[1].err != nil ||
			results[0].value.PrincipalID != results[1].value.PrincipalID ||
			results[0].value.MembershipID != results[1].value.MembershipID {
			t.Fatalf("concurrent service results = %+v", results)
		}
	})

	t.Run("project grant", func(t *testing.T) {
		service, err := first.RegisterServicePrincipal(ctx, RegisterServicePrincipalRequest{
			ActorPrincipalID: bootstrap.OwnerPrincipalID,
			Issuer:           "https://issuer.example",
			ClientID:         "grant-service-client",
		})
		if err != nil {
			t.Fatal(err)
		}
		project, err := first.CreateTeamProject(ctx, bootstrap.OwnerPrincipalID, "Concurrent Project")
		if err != nil {
			t.Fatal(err)
		}
		request := GrantProjectAccessRequest{
			ActorPrincipalID:  bootstrap.OwnerPrincipalID,
			ProjectID:         project.ProjectID,
			TargetPrincipalID: service.PrincipalID,
			AccessLevel:       "read",
		}
		results := concurrently(t, first, second, func(s *Store) (ProjectGrant, error) {
			return s.GrantProjectAccess(ctx, request)
		})
		if results[0].err != nil || results[1].err != nil || results[0].value.GrantID != results[1].value.GrantID {
			t.Fatalf("concurrent grant results = %+v", results)
		}
		request.AccessLevel = "write"
		if _, err := first.GrantProjectAccess(ctx, request); !errors.Is(err, ErrInvalidTeamIdentityMutation) {
			t.Fatalf("conflicting grant replay error = %v", err)
		}
	})
}

type concurrentResult[T any] struct {
	value T
	err   error
}

func concurrently[T any](t *testing.T, first, second *Store, operation func(*Store) (T, error)) [2]concurrentResult[T] {
	t.Helper()
	start := make(chan struct{})
	results := make(chan concurrentResult[T], 2)
	var wg sync.WaitGroup
	for _, candidate := range []*Store{first, second} {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			<-start
			value, err := operation(s)
			results <- concurrentResult[T]{value: value, err: err}
		}(candidate)
	}
	close(start)
	wg.Wait()
	close(results)
	var output [2]concurrentResult[T]
	i := 0
	for result := range results {
		output[i] = result
		i++
	}
	return output
}
