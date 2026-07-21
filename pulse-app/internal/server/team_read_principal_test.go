package server

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestPrincipalVerifierAcceptsOnlyExactTeamReadRoutes(t *testing.T) {
	fixture := newPrincipalFixture(t)
	body := []byte(`{"schema":"pulse.team.read.test.v1"}`)
	paths := []string{
		"/team/v1/recall",
		"/team/v1/context/query",
		"/team/v1/resume",
	}

	for index, path := range paths {
		jti := fmt.Sprintf("team-read-route-%d", index)
		claims := fixture.claims(jti, "agent-client", body)
		claims["path"] = path
		assertion := signPrincipalAssertion(t, fixture.private, "active", nil, claims)
		if _, err := fixture.verifier.VerifyDomainRequest(
			context.Background(), assertion, "req-"+jti,
			http.MethodPost, path, body,
		); err != nil {
			t.Fatalf("exact read route %q rejected: %v", path, err)
		}
	}

	for index, path := range []string{
		"/team/v1/recall/",
		"/team/v1/context",
		"/team/v1/resume?thread=hidden",
	} {
		jti := fmt.Sprintf("team-read-near-route-%d", index)
		claims := fixture.claims(jti, "agent-client", body)
		claims["path"] = path
		assertion := signPrincipalAssertion(t, fixture.private, "active", nil, claims)
		if _, err := fixture.verifier.VerifyDomainRequest(
			context.Background(), assertion, "req-"+jti,
			http.MethodPost, path, body,
		); err == nil {
			t.Fatalf("near read route %q accepted", path)
		}
	}
}
