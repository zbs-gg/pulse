package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

type teamPublicationApprovalFixture struct {
	store     *Store
	bootstrap BootstrapResult
	publisher mutationAuthorizationActor
	project   TeamProject
	lease     TeamWriterLease
	now       *time.Time
	issue     TeamPublicationApprovalIssueRequest
}

func newTeamPublicationApprovalFixture(t *testing.T) teamPublicationApprovalFixture {
	t.Helper()
	now := time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC)
	root := testBootstrapRoot()
	s, err := OpenTeam(filepath.Join(t.TempDir(), "commons.db"), TeamOpenOptions{
		ExpectedBootstrapRoot: root,
		Clock:                 func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := s.BootstrapTeam(context.Background(), BootstrapTeamRequest{
		TeamName: "Publication Commons", PresentedRoot: root,
	})
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	publisher := addMutationAuthorizationActor(t, s, bootstrap, "publication-member", "member")
	project, err := s.CreateTeamProject(context.Background(), bootstrap.OwnerPrincipalID, "Shared Publication")
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	lease := acquireReadyWriter(t, s)
	var policyEpoch, globalEpoch int64
	if err := s.DB().QueryRow(`
		SELECT policy_epoch, global_epoch FROM team_policy_metadata
		 WHERE store_id = ? AND team_id = ?`, bootstrap.StoreID, bootstrap.TeamID,
	).Scan(&policyEpoch, &globalEpoch); err != nil {
		s.Close()
		t.Fatal(err)
	}
	digest := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
	issue := TeamPublicationApprovalIssueRequest{
		DeploymentID:          "deployment_publication_test",
		StoreID:               bootstrap.StoreID,
		TeamID:                bootstrap.TeamID,
		SharedProjectID:       project.ProjectID,
		EnvelopeDigest:        digest("canonical-envelope"),
		IdempotencyKeyHash:    digest("idempotency-key"),
		OperationDigest:       digest("publication-operation"),
		PublisherPrincipalID:  publisher.binding.AgentPrincipalID,
		PublisherMembershipID: publisher.member.MembershipID,
		PublisherClientKey:    publisher.clientKey,
		PublisherBindingID:    publisher.binding.BindingID,
		Writer: TeamWriterLeaseIdentity{
			WriterID: lease.WriterID, Token: lease.Token,
		},
		ApprovingOwnerPrincipalID: bootstrap.OwnerPrincipalID,
		ApprovingClientKey:        teamauth.OAuthClientKey(root.Issuer, root.AdminClientID),
		PolicyEpoch:               policyEpoch,
		GlobalEpoch:               globalEpoch,
		AssertionKID:              "publication-owner-browser-kid",
		AssertionJTI:              "publication-owner-browser-jti",
		AssertionExpiresAt:        now.Add(20 * time.Second),
		StepUpAt:                  now.Add(-time.Minute),
		ExpiresAt:                 now.Add(45 * time.Second),
	}
	return teamPublicationApprovalFixture{
		store: s, bootstrap: bootstrap, publisher: publisher, project: project,
		lease: lease, now: &now, issue: issue,
	}
}

func publicationApprovalConsume(
	issue TeamPublicationApprovalIssueRequest,
	challenge TeamPublicationApprovalChallenge,
) TeamPublicationApprovalConsumeRequest {
	return TeamPublicationApprovalConsumeRequest{
		Nonce: challenge.Nonce, RequestID: "request-publication-approval-0001",
		DeploymentID: issue.DeploymentID, StoreID: issue.StoreID, TeamID: issue.TeamID,
		SharedProjectID: issue.SharedProjectID, EnvelopeDigest: issue.EnvelopeDigest,
		IdempotencyKeyHash: issue.IdempotencyKeyHash, OperationDigest: issue.OperationDigest,
		PublisherPrincipalID:      issue.PublisherPrincipalID,
		PublisherMembershipID:     issue.PublisherMembershipID,
		PublisherClientKey:        issue.PublisherClientKey,
		PublisherBindingID:        issue.PublisherBindingID,
		Writer:                    issue.Writer,
		ApprovingOwnerPrincipalID: issue.ApprovingOwnerPrincipalID,
		ApprovingClientKey:        issue.ApprovingClientKey,
		PolicyEpoch:               issue.PolicyEpoch, GlobalEpoch: issue.GlobalEpoch,
	}
}

func consumePublicationApprovalForTest(
	t *testing.T,
	fixture teamPublicationApprovalFixture,
	request TeamPublicationApprovalConsumeRequest,
	commit bool,
) (TeamPublicationApprovalConsumption, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := fixture.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return TeamPublicationApprovalConsumption{}, err
	}
	defer tx.Rollback()
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return TeamPublicationApprovalConsumption{}, err
	}
	result, err := fixture.store.consumeTeamPublicationApprovalTx(ctx, tx, info, request)
	if err != nil {
		return TeamPublicationApprovalConsumption{}, err
	}
	if commit {
		if err := tx.Commit(); err != nil {
			return TeamPublicationApprovalConsumption{}, err
		}
	}
	return result, nil
}

func TestTeamPublicationApprovalBindsExactAuthorityAndConsumesOnce(t *testing.T) {
	fixture := newTeamPublicationApprovalFixture(t)
	defer fixture.store.Close()
	challenge, err := fixture.store.IssueTeamPublicationApproval(context.Background(), fixture.issue)
	if err != nil {
		t.Fatal(err)
	}
	if challenge.Nonce == "" || challenge.ExpiresAt != fixture.issue.ExpiresAt {
		t.Fatalf("incomplete challenge: %+v", challenge)
	}
	var storedNonce, writerTokenHash, assertionKIDHash, assertionJTIHash string
	if err := fixture.store.DB().QueryRow(`
		SELECT nonce_hash, writer_lease_token_hash, assertion_kid_hash, assertion_jti_hash
		  FROM team_publication_approvals`,
	).Scan(&storedNonce, &writerTokenHash, &assertionKIDHash, &assertionJTIHash); err != nil {
		t.Fatal(err)
	}
	if storedNonce == challenge.Nonce || writerTokenHash == fixture.issue.Writer.Token ||
		assertionKIDHash == fixture.issue.AssertionKID || assertionJTIHash == fixture.issue.AssertionJTI {
		t.Fatal("publication approval persisted a raw nonce, writer token, or assertion identifier")
	}
	request := publicationApprovalConsume(fixture.issue, challenge)
	consumed, err := consumePublicationApprovalForTest(t, fixture, request, true)
	if err != nil {
		t.Fatal(err)
	}
	if consumed.NonceHash != storedNonce || consumed.OwnerPrincipalID != fixture.bootstrap.OwnerPrincipalID ||
		consumed.AuditEventID == "" || consumed.ConsumedAt.IsZero() {
		t.Fatalf("incomplete consumption: %+v", consumed)
	}
	var action, actor, client, project, target, requestID, reason string
	if err := fixture.store.DB().QueryRow(`
		SELECT action, actor_principal_id, client_key, project_id, target_id, request_id, reason_code
		  FROM team_audit_events WHERE event_id = ?`, consumed.AuditEventID,
	).Scan(&action, &actor, &client, &project, &target, &requestID, &reason); err != nil {
		t.Fatal(err)
	}
	if action != teamPublicationApprovalAction || actor != fixture.bootstrap.OwnerPrincipalID ||
		client != fixture.issue.ApprovingClientKey || project != fixture.project.ProjectID ||
		target != fixture.issue.EnvelopeDigest || requestID != request.RequestID ||
		reason != teamPublicationApprovalReason {
		t.Fatalf("publication approval audit mismatch: %q %q %q %q %q %q %q",
			action, actor, client, project, target, requestID, reason)
	}
	if _, err := consumePublicationApprovalForTest(t, fixture, request, true); !errors.Is(err, ErrTeamPublicationApprovalReplay) {
		t.Fatalf("replay error=%v", err)
	}
}

func TestTeamPublicationApprovalRejectsEveryChangedBindingWithoutBurningNonce(t *testing.T) {
	fixture := newTeamPublicationApprovalFixture(t)
	defer fixture.store.Close()
	challenge, err := fixture.store.IssueTeamPublicationApproval(context.Background(), fixture.issue)
	if err != nil {
		t.Fatal(err)
	}
	base := publicationApprovalConsume(fixture.issue, challenge)
	wrongDigest := strings.Repeat("a", 64)
	if wrongDigest == base.EnvelopeDigest {
		wrongDigest = strings.Repeat("b", 64)
	}
	tests := map[string]func(*TeamPublicationApprovalConsumeRequest){
		"deployment":       func(r *TeamPublicationApprovalConsumeRequest) { r.DeploymentID = "deployment_wrong" },
		"store":            func(r *TeamPublicationApprovalConsumeRequest) { r.StoreID = "store_wrong" },
		"team":             func(r *TeamPublicationApprovalConsumeRequest) { r.TeamID = "team_wrong" },
		"project":          func(r *TeamPublicationApprovalConsumeRequest) { r.SharedProjectID = "project_wrong" },
		"envelope":         func(r *TeamPublicationApprovalConsumeRequest) { r.EnvelopeDigest = wrongDigest },
		"idempotency":      func(r *TeamPublicationApprovalConsumeRequest) { r.IdempotencyKeyHash = wrongDigest },
		"operation":        func(r *TeamPublicationApprovalConsumeRequest) { r.OperationDigest = wrongDigest },
		"publisher":        func(r *TeamPublicationApprovalConsumeRequest) { r.PublisherPrincipalID = "principal_wrong" },
		"membership":       func(r *TeamPublicationApprovalConsumeRequest) { r.PublisherMembershipID = "membership_wrong" },
		"publisher client": func(r *TeamPublicationApprovalConsumeRequest) { r.PublisherClientKey = wrongDigest },
		"binding":          func(r *TeamPublicationApprovalConsumeRequest) { r.PublisherBindingID = "binding_wrong" },
		"writer id":        func(r *TeamPublicationApprovalConsumeRequest) { r.Writer.WriterID = "writer_wrong" },
		"writer token":     func(r *TeamPublicationApprovalConsumeRequest) { r.Writer.Token = "writer_wrong_token" },
		"owner":            func(r *TeamPublicationApprovalConsumeRequest) { r.ApprovingOwnerPrincipalID = "principal_wrong" },
		"owner client":     func(r *TeamPublicationApprovalConsumeRequest) { r.ApprovingClientKey = wrongDigest },
		"policy epoch":     func(r *TeamPublicationApprovalConsumeRequest) { r.PolicyEpoch++ },
		"global epoch":     func(r *TeamPublicationApprovalConsumeRequest) { r.GlobalEpoch++ },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			request := base
			mutate(&request)
			if _, err := consumePublicationApprovalForTest(t, fixture, request, true); !errors.Is(err, ErrTeamPublicationApprovalBindingMismatch) {
				t.Fatalf("changed binding error=%v", err)
			}
		})
	}
	if _, err := consumePublicationApprovalForTest(t, fixture, base, true); err != nil {
		t.Fatalf("wrong binding attempt burned approval: %v", err)
	}
}

func TestTeamPublicationApprovalRejectsExpiryAssertionReplayAndWrongWriter(t *testing.T) {
	t.Run("wrong writer on issue", func(t *testing.T) {
		fixture := newTeamPublicationApprovalFixture(t)
		defer fixture.store.Close()
		fixture.issue.Writer.Token = "wrong_writer_token"
		if _, err := fixture.store.IssueTeamPublicationApproval(context.Background(), fixture.issue); !errors.Is(err, ErrTeamWriterLeaseMismatch) {
			t.Fatalf("wrong writer issue error=%v", err)
		}
		var approvals int
		if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM team_publication_approvals`).Scan(&approvals); err != nil || approvals != 0 {
			t.Fatalf("wrong writer persisted approvals=%d err=%v", approvals, err)
		}
	})

	t.Run("assertion replay", func(t *testing.T) {
		fixture := newTeamPublicationApprovalFixture(t)
		defer fixture.store.Close()
		if _, err := fixture.store.IssueTeamPublicationApproval(context.Background(), fixture.issue); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.IssueTeamPublicationApproval(context.Background(), fixture.issue); !errors.Is(err, ErrTeamPublicationApprovalReplay) {
			t.Fatalf("assertion replay error=%v", err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		fixture := newTeamPublicationApprovalFixture(t)
		defer fixture.store.Close()
		challenge, err := fixture.store.IssueTeamPublicationApproval(context.Background(), fixture.issue)
		if err != nil {
			t.Fatal(err)
		}
		*fixture.now = fixture.issue.ExpiresAt.Add(time.Nanosecond)
		if _, err := consumePublicationApprovalForTest(t, fixture, publicationApprovalConsume(fixture.issue, challenge), true); !errors.Is(err, ErrTeamPublicationApprovalExpired) {
			t.Fatalf("expired consume error=%v", err)
		}
	})

	t.Run("runtime writer replaced", func(t *testing.T) {
		fixture := newTeamPublicationApprovalFixture(t)
		defer fixture.store.Close()
		challenge, err := fixture.store.IssueTeamPublicationApproval(context.Background(), fixture.issue)
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.ReleaseTeamWriterLease(context.Background(), fixture.lease.WriterID, fixture.lease.Token); err != nil {
			t.Fatal(err)
		}
		if _, err := consumePublicationApprovalForTest(t, fixture, publicationApprovalConsume(fixture.issue, challenge), true); !errors.Is(err, ErrTeamWriterLeaseMismatch) {
			t.Fatalf("replaced writer consume error=%v", err)
		}
		var consumed any
		if err := fixture.store.DB().QueryRow(`
			SELECT consumed_at FROM team_publication_approvals
			 WHERE nonce_hash = ?`, teamPublicationApprovalNonceHash(challenge.Nonce),
		).Scan(&consumed); err != nil {
			t.Fatal(err)
		}
		if consumed != nil {
			t.Fatal("stale runtime writer consumed publication approval")
		}
	})
}

func TestTeamPublicationApprovalFailsClosedAfterPublisherRevocation(t *testing.T) {
	fixture := newTeamPublicationApprovalFixture(t)
	defer fixture.store.Close()
	challenge, err := fixture.store.IssueTeamPublicationApproval(context.Background(), fixture.issue)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.RevokeAgentBinding(
		context.Background(), fixture.bootstrap.OwnerPrincipalID, fixture.publisher.binding.BindingID,
	); err != nil {
		t.Fatal(err)
	}
	request := publicationApprovalConsume(fixture.issue, challenge)
	if _, err := consumePublicationApprovalForTest(t, fixture, request, true); !errors.Is(err, ErrTeamPublicationApprovalBindingMismatch) {
		t.Fatalf("revoked publisher consume error=%v", err)
	}
	var consumed any
	if err := fixture.store.DB().QueryRow(`
		SELECT consumed_at FROM team_publication_approvals
		 WHERE nonce_hash = ?`, teamPublicationApprovalNonceHash(challenge.Nonce),
	).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed != nil {
		t.Fatal("revoked publisher consumed publication approval")
	}
}
