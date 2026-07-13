package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

func TestApprovedOwnerAdminMutationsAreAtomicAndNonceDerived(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 12, 8, 0, 0, 0, time.UTC)
	s, bootstrap := bootstrapTeamStoreWithClock(t, func() time.Time { return now })
	defer s.Close()
	lease := acquireReadyWriter(t, s)
	writer := TeamWriterLeaseIdentity{WriterID: lease.WriterID, Token: lease.Token}
	clientKey, err := s.ResolveOwnerStepUpIdentity(ctx, testBootstrapRoot())
	if err != nil {
		t.Fatal(err)
	}

	memberMutation := OwnerAdminMutation{
		Action: OwnerActionMembershipCreate, Issuer: "https://issuer.example",
		Subject: "approved-member", Role: "member",
	}
	memberResult := executeTestOwnerMutation(t, s, bootstrap, writer, clientKey.ClientKey, now, memberMutation, "member")
	if memberResult.Member == nil || memberResult.Member.PrincipalID == "" || memberResult.AuditEventID == "" {
		t.Fatalf("member result = %+v", memberResult)
	}

	bindingMutation := OwnerAdminMutation{
		Action: OwnerActionAgentBindingCreate, Issuer: memberMutation.Issuer,
		Subject: memberMutation.Subject, ClientID: "approved-member-agent",
	}
	bindingResult := executeTestOwnerMutation(t, s, bootstrap, writer, clientKey.ClientKey, now, bindingMutation, "binding")
	if bindingResult.Binding == nil || bindingResult.Binding.HumanPrincipalID != memberResult.Member.PrincipalID {
		t.Fatalf("binding result = %+v", bindingResult)
	}

	serviceMutation := OwnerAdminMutation{
		Action: OwnerActionServicePrincipalCreate, Issuer: "https://issuer.example",
		ClientID: "approved-service",
	}
	serviceResult := executeTestOwnerMutation(t, s, bootstrap, writer, clientKey.ClientKey, now, serviceMutation, "service")
	if serviceResult.Service == nil || serviceResult.Service.PrincipalID == "" {
		t.Fatalf("service result = %+v", serviceResult)
	}

	projectMutation := OwnerAdminMutation{Action: OwnerActionProjectCreate, Name: "Approved Project"}
	projectResult := executeTestOwnerMutation(t, s, bootstrap, writer, clientKey.ClientKey, now, projectMutation, "project")
	if projectResult.Project == nil || projectResult.Project.OwnerPrincipalID != bootstrap.OwnerPrincipalID {
		t.Fatalf("project result = %+v", projectResult)
	}

	grantMutation := OwnerAdminMutation{
		Action: OwnerActionProjectGrantCreate, ProjectID: projectResult.Project.ProjectID,
		TargetPrincipalID: bindingResult.Binding.AgentPrincipalID, AccessLevel: "write",
	}
	grantResult := executeTestOwnerMutation(t, s, bootstrap, writer, clientKey.ClientKey, now, grantMutation, "grant")
	if grantResult.Grant == nil || grantResult.Grant.ProjectID != projectResult.Project.ProjectID {
		t.Fatalf("grant result = %+v", grantResult)
	}

	for _, operation := range []struct {
		suffix   string
		mutation OwnerAdminMutation
	}{
		{suffix: "grant-revoke", mutation: OwnerAdminMutation{
			Action: OwnerActionProjectGrantRevoke, TargetID: grantResult.Grant.GrantID,
		}},
		{suffix: "binding-revoke", mutation: OwnerAdminMutation{
			Action: OwnerActionAgentBindingRevoke, TargetID: bindingResult.Binding.BindingID,
		}},
		{suffix: "service-revoke", mutation: OwnerAdminMutation{
			Action: OwnerActionServicePrincipalRevoke, TargetID: serviceResult.Service.PrincipalID,
		}},
	} {
		result := executeTestOwnerMutation(
			t, s, bootstrap, writer, clientKey.ClientKey, now, operation.mutation, operation.suffix,
		)
		if result.AuditEventID == "" {
			t.Fatalf("%s missing audit: %+v", operation.suffix, result)
		}
	}
	membershipRevoke := OwnerAdminMutation{
		Action: OwnerActionMembershipRevoke, TargetID: memberResult.Member.PrincipalID,
	}
	executeTestOwnerMutation(t, s, bootstrap, writer, clientKey.ClientKey, now, membershipRevoke, "member-revoke")
	if _, err := s.ResolveTeamPrincipal(ctx, memberResult.Member.PrincipalID); !errors.Is(err, ErrPrincipalRevoked) {
		t.Fatalf("approved member remained active: %v", err)
	}

	lastOwner := OwnerAdminMutation{Action: OwnerActionMembershipRevoke, TargetID: bootstrap.OwnerPrincipalID}
	challenge := issueTestOwnerMutation(t, s, bootstrap, writer, now, lastOwner, "last-owner")
	if _, err := s.ExecuteApprovedOwnerAdminMutation(ctx, ApprovedOwnerAdminMutationRequest{
		Mutation: lastOwner, ApprovalNonce: challenge.Nonce,
		RequestID: "owner-admin-last-owner", ClientKey: clientKey.ClientKey, Writer: writer,
	}); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("last owner revoke error = %v", err)
	}
	var consumed any
	if err := s.DB().QueryRow(`SELECT consumed_at FROM team_owner_approvals WHERE nonce_hash = ?`,
		ownerApprovalNonceHash(challenge.Nonce)).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed != nil {
		t.Fatal("failed last-owner mutation consumed approval")
	}
}

func TestApprovedOwnerAdminMutationRollsBackWhenPrivilegedAuditFails(t *testing.T) {
	now := time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC)
	s, bootstrap := bootstrapTeamStoreWithClock(t, func() time.Time { return now })
	defer s.Close()
	lease := acquireReadyWriter(t, s)
	writer := TeamWriterLeaseIdentity{WriterID: lease.WriterID, Token: lease.Token}
	identity, err := s.ResolveOwnerStepUpIdentity(context.Background(), testBootstrapRoot())
	if err != nil {
		t.Fatal(err)
	}
	mutation := OwnerAdminMutation{
		Action: OwnerActionMembershipCreate, Issuer: "https://issuer.example",
		Subject: "must-rollback-approved-member", Role: "member",
	}
	challenge := issueTestOwnerMutation(t, s, bootstrap, writer, now, mutation, "rollback")
	if _, err := s.DB().Exec(`
		CREATE TRIGGER reject_approved_owner_audit
		BEFORE INSERT ON team_audit_events
		WHEN NEW.action = 'membership.create'
		BEGIN SELECT RAISE(ABORT, 'approved audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecuteApprovedOwnerAdminMutation(context.Background(), ApprovedOwnerAdminMutationRequest{
		Mutation: mutation, ApprovalNonce: challenge.Nonce,
		RequestID: "owner-admin-rollback", ClientKey: identity.ClientKey, Writer: writer,
	}); err == nil {
		t.Fatal("approved mutation succeeded without privileged audit")
	}
	var members int
	if err := s.DB().QueryRow(`
		SELECT count(*) FROM team_human_identities
		 WHERE identity_key = ?`,
		teamauth.HumanIdentityKey(mutation.Issuer, mutation.Subject)).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if members != 0 {
		t.Fatalf("failed approved mutation left %d member identities", members)
	}
	var consumed any
	if err := s.DB().QueryRow(`SELECT consumed_at FROM team_owner_approvals WHERE nonce_hash = ?`,
		ownerApprovalNonceHash(challenge.Nonce)).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed != nil {
		t.Fatal("failed approved mutation consumed approval")
	}
}

func executeTestOwnerMutation(
	t *testing.T,
	s *Store,
	bootstrap BootstrapResult,
	writer TeamWriterLeaseIdentity,
	clientKey string,
	now time.Time,
	mutation OwnerAdminMutation,
	suffix string,
) OwnerAdminMutationResult {
	t.Helper()
	challenge := issueTestOwnerMutation(t, s, bootstrap, writer, now, mutation, suffix)
	result, err := s.ExecuteApprovedOwnerAdminMutation(context.Background(), ApprovedOwnerAdminMutationRequest{
		Mutation: mutation, ApprovalNonce: challenge.Nonce,
		RequestID: "owner-admin-" + suffix, ClientKey: clientKey, Writer: writer,
	})
	if err != nil {
		t.Fatalf("execute %s: %v", suffix, err)
	}
	return result
}

func issueTestOwnerMutation(
	t *testing.T,
	s *Store,
	bootstrap BootstrapResult,
	writer TeamWriterLeaseIdentity,
	now time.Time,
	mutation OwnerAdminMutation,
	suffix string,
) OwnerApprovalChallenge {
	t.Helper()
	targetKind, targetID, digest, err := OwnerAdminMutationTarget(mutation)
	if err != nil {
		t.Fatal(err)
	}
	request := OwnerApprovalIssueRequest{
		OwnerPrincipalID: bootstrap.OwnerPrincipalID,
		StoreID:          bootstrap.StoreID, TeamID: bootstrap.TeamID,
		Action: mutation.Action, TargetKind: targetKind, TargetID: targetID, TargetDigest: digest,
		StepUpAt: now.Add(-time.Minute), ExpiresAt: now.Add(4 * time.Minute), Writer: writer,
	}
	setTestOwnerAssertion(&request, "admin-"+suffix, now)
	challenge, err := s.IssueOwnerApproval(context.Background(), request)
	if err != nil {
		t.Fatalf("issue %s: %v", suffix, err)
	}
	return challenge
}
