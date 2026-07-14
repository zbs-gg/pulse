package server

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func newOwnerStepUpVerifierFixture(t *testing.T, fixture *principalFixture) *OwnerStepUpVerifier {
	t.Helper()
	verifier, err := NewOwnerStepUpVerifier(OwnerStepUpVerifierConfig{
		Store: fixture.store,
		Keyring: PrincipalVerifyKeyring{ActiveKid: "active", Keys: map[string]ed25519.PublicKey{
			"active": fixture.private.Public().(ed25519.PublicKey),
		}},
		ExpectedRoot: fixture.root,
		Clock:        func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func ownerStepUpClaims(fixture *principalFixture, jti string, body []byte) map[string]any {
	digest := sha256.Sum256(body)
	return map[string]any{
		"version":         OwnerStepUpAssertionVersion,
		"iss":             ownerStepUpAssertionIssuer,
		"aud":             ownerStepUpAssertionAudience,
		"iat":             fixture.now.Unix(),
		"nbf":             fixture.now.Unix(),
		"exp":             fixture.now.Add(25 * time.Second).Unix(),
		"jti":             jti,
		"request_id":      "req-" + jti,
		"method":          http.MethodPost,
		"path":            OwnerApprovalRoutePath,
		"body_sha256":     fmt.Sprintf("%x", digest),
		"action":          "team.activation.synthetic",
		"store_id":        fixture.storeID,
		"team_id":         fixture.teamID,
		"oauth_issuer":    fixture.root.Issuer,
		"oauth_subject":   fixture.root.Subject,
		"oauth_client_id": fixture.root.AdminClientID,
		"auth_time":       fixture.now.Add(-time.Minute).Unix(),
	}
}

func TestOwnerStepUpVerifierBindsRecentBrowserOwnerToExactApprovalBody(t *testing.T) {
	fixture := newPrincipalFixture(t)
	verifier := newOwnerStepUpVerifierFixture(t, fixture)
	body := []byte(`{"schema":"pulse.team.owner.approval.v1","action":"team.activation.synthetic","target_kind":"team_activation","target_id":"team-target","target_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	claims := ownerStepUpClaims(fixture, "owner-step-up-exact", body)
	assertion := signPrincipalAssertion(
		t, fixture.private, "active", map[string]any{"typ": OwnerStepUpAssertionVersion}, claims,
	)

	stepUp, err := verifier.VerifyApprovalRequest(
		context.Background(), assertion, "req-owner-step-up-exact",
		http.MethodPost, OwnerApprovalRoutePath, body,
		OwnerStepUpBinding{Action: "team.activation.synthetic", StoreID: fixture.storeID, TeamID: fixture.teamID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if stepUp.Identity.OwnerPrincipalID != fixture.ownerID || stepUp.Identity.StoreID != fixture.storeID ||
		stepUp.Identity.TeamID != fixture.teamID || stepUp.Identity.Bootstrap ||
		stepUp.Identity.ClientKey == "" || stepUp.AssertionKID != "active" ||
		stepUp.AssertionJTI != "owner-step-up-exact" ||
		!stepUp.AuthenticatedAt.Equal(fixture.now.Add(-time.Minute)) ||
		!stepUp.AssertionExpiresAt.Equal(fixture.now.Add(25*time.Second)) {
		t.Fatalf("step-up context = %+v", stepUp)
	}

	if _, err := verifier.VerifyApprovalRequest(
		context.Background(), assertion, "req-owner-step-up-exact",
		http.MethodPost, OwnerApprovalRoutePath, append(append([]byte(nil), body...), ' '),
		OwnerStepUpBinding{Action: "team.activation.synthetic", StoreID: fixture.storeID, TeamID: fixture.teamID},
	); !errors.Is(err, ErrOwnerStepUpRequestMismatch) {
		t.Fatalf("tampered body error = %v", err)
	}
}

func TestOwnerStepUpVerifierRejectsStaleWrongRootAndNearRouteAssertions(t *testing.T) {
	fixture := newPrincipalFixture(t)
	verifier := newOwnerStepUpVerifierFixture(t, fixture)
	body := []byte(`{"schema":"pulse.team.owner.approval.v1","action":"team.activation.synthetic","target_kind":"team_activation","target_id":"team-target","target_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)

	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"stale auth", func(claims map[string]any) { claims["auth_time"] = fixture.now.Add(-6 * time.Minute).Unix() }},
		{"wrong admin client", func(claims map[string]any) { claims["oauth_client_id"] = "other-admin-client" }},
		{"near route", func(claims map[string]any) { claims["path"] = OwnerApprovalRoutePath + "/" }},
		{"future auth", func(claims map[string]any) { claims["auth_time"] = fixture.now.Add(time.Minute).Unix() }},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			jti := fmt.Sprintf("owner-step-up-invalid-%d", index)
			claims := ownerStepUpClaims(fixture, jti, body)
			test.mutate(claims)
			assertion := signPrincipalAssertion(
				t, fixture.private, "active", map[string]any{"typ": OwnerStepUpAssertionVersion}, claims,
			)
			if _, err := verifier.VerifyApprovalRequest(
				context.Background(), assertion, "req-"+jti,
				http.MethodPost, OwnerApprovalRoutePath, body,
				OwnerStepUpBinding{Action: "team.activation.synthetic", StoreID: fixture.storeID, TeamID: fixture.teamID},
			); err == nil {
				t.Fatal("invalid owner step-up was accepted")
			}
		})
	}
}

func TestOwnerStepUpVerifierBindsAirlockAssertionToExactPublicationEnvelope(t *testing.T) {
	fixture := newPrincipalFixture(t)
	verifier := newOwnerStepUpVerifierFixture(t, fixture)
	body := []byte(`{"schema":"pulse.team.airlock_envelope.v1","action":"team.commons.publish"}`)
	digest := sha256.Sum256(body)
	claims := ownerStepUpClaims(fixture, "owner-step-up-publication", body)
	claims["path"] = TeamPublicationAirlockRoutePath
	claims["action"] = "team.commons.publish"
	claims["body_sha256"] = fmt.Sprintf("%x", digest)
	assertion := signPrincipalAssertion(
		t, fixture.private, "active", map[string]any{"typ": OwnerStepUpAssertionVersion}, claims,
	)
	request := TeamPublicationStepUpVerificationRequest{
		Assertion: assertion, RequestID: "req-owner-step-up-publication",
		Method: http.MethodPost, Path: TeamPublicationAirlockRoutePath,
		CanonicalEnvelope: body, EnvelopeDigest: fmt.Sprintf("%x", digest),
		StoreID: fixture.storeID, TeamID: fixture.teamID,
		PublisherPrincipalID: fixture.agentID,
	}

	stepUp, err := verifier.VerifyTeamPublication(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if stepUp.Identity.OwnerPrincipalID != fixture.ownerID || stepUp.AssertionJTI != "owner-step-up-publication" {
		t.Fatalf("publication step-up = %+v", stepUp)
	}

	tampered := request
	tampered.CanonicalEnvelope = append(append([]byte(nil), body...), ' ')
	if _, err := verifier.VerifyTeamPublication(context.Background(), tampered); !errors.Is(err, ErrOwnerStepUpRequestMismatch) {
		t.Fatalf("tampered publication error = %v", err)
	}
	wrongPath := request
	wrongPath.Path = OwnerApprovalRoutePath
	if _, err := verifier.VerifyTeamPublication(context.Background(), wrongPath); !errors.Is(err, ErrOwnerStepUpInvalid) {
		t.Fatalf("wrong publication path error = %v", err)
	}
}
