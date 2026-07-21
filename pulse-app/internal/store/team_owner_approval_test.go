package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

func TestOwnerApprovalIsRecentActionBoundSingleUseAndRestartDurable(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 12, 4, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "team.db")
	options := TeamOpenOptions{ExpectedBootstrapRoot: testBootstrapRoot(), Clock: func() time.Time { return now }}
	s, err := OpenTeam(path, options)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := s.BootstrapTeam(ctx, BootstrapTeamRequest{TeamName: "Synthetic Pilot", PresentedRoot: testBootstrapRoot()})
	if err != nil {
		t.Fatal(err)
	}
	lease := acquireReadyWriter(t, s)
	writer := TeamWriterLeaseIdentity{WriterID: lease.WriterID, Token: lease.Token}
	targetDigest := testOwnerTargetDigest("member-a")

	staleIssue := OwnerApprovalIssueRequest{
		OwnerPrincipalID: bootstrap.OwnerPrincipalID,
		StoreID:          bootstrap.StoreID, TeamID: bootstrap.TeamID,
		Action: OwnerActionMembershipCreate, TargetKind: "membership", TargetID: "membership_pending",
		TargetDigest: targetDigest, StepUpAt: now.Add(-6 * time.Minute), ExpiresAt: now.Add(time.Minute),
	}
	staleIssue.Writer = writer
	setTestOwnerAssertion(&staleIssue, "stale", now)
	if _, err := s.IssueOwnerApproval(ctx, staleIssue); !errors.Is(err, ErrOwnerStepUpStale) {
		t.Fatalf("stale step-up error = %v", err)
	}
	issue := OwnerApprovalIssueRequest{
		OwnerPrincipalID: bootstrap.OwnerPrincipalID,
		StoreID:          bootstrap.StoreID, TeamID: bootstrap.TeamID,
		Action: OwnerActionMembershipCreate, TargetKind: "membership", TargetID: "membership_pending",
		TargetDigest: targetDigest, StepUpAt: now.Add(-time.Minute), ExpiresAt: now.Add(4 * time.Minute),
	}
	issue.Writer = writer
	setTestOwnerAssertion(&issue, "membership-create", now)
	challenge, err := s.IssueOwnerApproval(ctx, issue)
	if err != nil {
		t.Fatal(err)
	}
	if challenge.Nonce == "" || challenge.ExpiresAt.IsZero() {
		t.Fatalf("incomplete challenge: %+v", challenge)
	}

	wrong := OwnerApprovalConsumeRequest{
		OwnerPrincipalID: bootstrap.OwnerPrincipalID,
		StoreID:          bootstrap.StoreID, TeamID: bootstrap.TeamID,
		Action: OwnerActionMembershipRevoke, TargetKind: "membership", TargetID: "membership_pending",
		TargetDigest: targetDigest, Nonce: challenge.Nonce, RequestID: "owner-request-wrong-action",
		ClientKey: issue.ClientKey, Writer: writer,
	}
	if _, err := s.consumeOwnerApproval(ctx, wrong); !errors.Is(err, ErrOwnerApprovalBindingMismatch) {
		t.Fatalf("wrong-action error = %v", err)
	}

	consume := wrong
	consume.Action = OwnerActionMembershipCreate
	consume.RequestID = "owner-request-consume"
	consumed, err := s.consumeOwnerApproval(ctx, consume)
	if err != nil {
		t.Fatal(err)
	}
	if consumed.AuditEventID == "" || consumed.ConsumedAt.IsZero() {
		t.Fatalf("incomplete consume result: %+v", consumed)
	}
	if _, err := s.consumeOwnerApproval(ctx, consume); !errors.Is(err, ErrOwnerApprovalReplay) {
		t.Fatalf("same-process replay error = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenTeam(path, options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.consumeOwnerApproval(ctx, consume); !errors.Is(err, ErrOwnerApprovalReplay) {
		t.Fatalf("post-restart replay error = %v", err)
	}
	var persistedNonce, auditAction, auditRequest string
	if err := reopened.DB().QueryRow(`SELECT nonce_hash FROM team_owner_approvals LIMIT 1`).Scan(&persistedNonce); err != nil {
		t.Fatal(err)
	}
	if persistedNonce == challenge.Nonce {
		t.Fatal("raw owner approval nonce was persisted")
	}
	if err := reopened.DB().QueryRow(`SELECT action, request_id FROM team_audit_events WHERE event_id = ?`, consumed.AuditEventID).Scan(&auditAction, &auditRequest); err != nil {
		t.Fatal(err)
	}
	if auditAction != OwnerActionMembershipCreate || auditRequest != consume.RequestID {
		t.Fatalf("consume audit = %q/%q", auditAction, auditRequest)
	}
}

func TestBrowserApprovedBootstrapConsumesAssertionAndApprovalAtomically(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 12, 4, 30, 0, 0, time.UTC)
	root := testBootstrapRoot()
	s, err := OpenTeam(filepath.Join(t.TempDir(), "team.db"), TeamOpenOptions{
		ExpectedBootstrapRoot: root, Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	identity, err := s.ResolveOwnerStepUpIdentity(ctx, root)
	if err != nil || !identity.Bootstrap || identity.ClientKey == "" || identity.OwnerPrincipalID != "" {
		t.Fatalf("prebootstrap owner identity = %+v, %v", identity, err)
	}
	intent, err := s.PrepareTeamBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	digest := TeamBootstrapApprovalTargetDigest(intent, "Approved Pilot")
	issue := OwnerApprovalIssueRequest{
		OwnerPrincipalID: intent.OwnerPrincipalID, StoreID: intent.StoreID, TeamID: intent.TeamID,
		Action: OwnerActionTeamBootstrap, TargetKind: "team", TargetID: intent.TeamID,
		TargetDigest: digest, StepUpAt: now.Add(-time.Minute), ExpiresAt: now.Add(4 * time.Minute),
	}
	setTestOwnerAssertion(&issue, "bootstrap", now)
	challenge, err := s.IssueOwnerApproval(ctx, issue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.IssueOwnerApproval(ctx, issue); !errors.Is(err, ErrOwnerApprovalReplay) {
		t.Fatalf("browser assertion replay error = %v", err)
	}

	wrong := ApprovedBootstrapTeamRequest{
		TeamName: "Changed Pilot", PresentedRoot: root, Intent: intent,
		ApprovalNonce: challenge.Nonce, RequestID: "bootstrap-wrong-body", ClientKey: identity.ClientKey,
	}
	if _, err := s.BootstrapTeamWithApproval(ctx, wrong); !errors.Is(err, ErrOwnerApprovalBindingMismatch) {
		t.Fatalf("wrong bootstrap body error = %v", err)
	}
	var markers int
	if err := s.DB().QueryRow(`SELECT count(*) FROM team_stores`).Scan(&markers); err != nil || markers != 0 {
		t.Fatalf("failed approved bootstrap left marker=%d, err=%v", markers, err)
	}

	approved := wrong
	approved.TeamName = "Approved Pilot"
	approved.RequestID = "bootstrap-approved"
	result, err := s.BootstrapTeamWithApproval(ctx, approved)
	if err != nil {
		t.Fatal(err)
	}
	if result.StoreID != intent.StoreID || result.TeamID != intent.TeamID ||
		result.OwnerPrincipalID != intent.OwnerPrincipalID {
		t.Fatalf("approved bootstrap IDs = %+v, intent=%+v", result, intent)
	}
	identity, err = s.ResolveOwnerStepUpIdentity(ctx, root)
	if err != nil || identity.Bootstrap || identity.OwnerPrincipalID != intent.OwnerPrincipalID ||
		identity.StoreID != intent.StoreID || identity.TeamID != intent.TeamID {
		t.Fatalf("marked owner identity = %+v, %v", identity, err)
	}
	activation, err := s.ReadTeamActivationState(ctx)
	if err != nil || activation.ActivationState != TeamActivationInactive || activation.PublicEnabled {
		t.Fatalf("approved bootstrap auto-activated: %+v, %v", activation, err)
	}
}

func TestOwnerApprovalRejectsExpiredWrongStoreTeamTargetAndNonOwner(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 12, 5, 0, 0, 0, time.UTC)
	s, bootstrap := bootstrapTeamStoreWithClock(t, func() time.Time { return now })
	defer s.Close()
	lease := acquireReadyWriter(t, s)
	writer := TeamWriterLeaseIdentity{WriterID: lease.WriterID, Token: lease.Token}
	targetDigest := testOwnerTargetDigest("shared-delete")
	issue := OwnerApprovalIssueRequest{
		OwnerPrincipalID: bootstrap.OwnerPrincipalID,
		StoreID:          bootstrap.StoreID, TeamID: bootstrap.TeamID,
		Action: OwnerActionSharedDelete, TargetKind: "memory", TargetID: "object_shared",
		TargetDigest: targetDigest, StepUpAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute),
	}
	issue.Writer = writer
	setTestOwnerAssertion(&issue, "shared-delete", now)
	challenge, err := s.IssueOwnerApproval(ctx, issue)
	if err != nil {
		t.Fatal(err)
	}
	base := OwnerApprovalConsumeRequest{
		OwnerPrincipalID: issue.OwnerPrincipalID, StoreID: issue.StoreID, TeamID: issue.TeamID,
		Action: issue.Action, TargetKind: issue.TargetKind, TargetID: issue.TargetID,
		TargetDigest: issue.TargetDigest, Nonce: challenge.Nonce, RequestID: "owner-delete-shared",
		ClientKey: issue.ClientKey, Writer: writer,
	}
	for name, mutate := range map[string]func(*OwnerApprovalConsumeRequest){
		"store":  func(r *OwnerApprovalConsumeRequest) { r.StoreID = "store_wrong" },
		"team":   func(r *OwnerApprovalConsumeRequest) { r.TeamID = "team_wrong" },
		"target": func(r *OwnerApprovalConsumeRequest) { r.TargetDigest = testOwnerTargetDigest("other") },
	} {
		t.Run(name, func(t *testing.T) {
			request := base
			mutate(&request)
			if _, err := s.consumeOwnerApproval(ctx, request); !errors.Is(err, ErrOwnerApprovalBindingMismatch) {
				t.Fatalf("binding mismatch error = %v", err)
			}
		})
	}

	member, err := s.AddTeamMember(ctx, AddTeamMemberRequest{
		ActorPrincipalID: bootstrap.OwnerPrincipalID,
		Issuer:           "https://issuer.example", Subject: "ordinary-member", Role: "member",
	})
	if err != nil {
		t.Fatal(err)
	}
	issue.OwnerPrincipalID = member.PrincipalID
	setTestOwnerAssertion(&issue, "non-owner", now)
	if _, err := s.IssueOwnerApproval(ctx, issue); !errors.Is(err, ErrHumanOwnerRequired) {
		t.Fatalf("non-owner issue error = %v", err)
	}

	now = now.Add(2 * time.Minute)
	renewed := acquireReadyWriter(t, s)
	base.Writer = TeamWriterLeaseIdentity{WriterID: renewed.WriterID, Token: renewed.Token}
	if _, err := s.consumeOwnerApproval(ctx, base); !errors.Is(err, ErrOwnerApprovalExpired) {
		t.Fatalf("expired approval error = %v", err)
	}
}

func TestSyntheticActivationIsExplicitAtomicAndNeverEnablesRealContent(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 12, 6, 0, 0, 0, time.UTC)
	s, bootstrap := bootstrapTeamStoreWithClock(t, func() time.Time { return now })
	defer s.Close()
	lease := acquireReadyWriter(t, s)
	writer := TeamWriterLeaseIdentity{WriterID: lease.WriterID, Token: lease.Token}

	state, err := s.ReadTeamActivationState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActivationState != TeamActivationInactive || state.ContentBoundary != TeamContentSynthetic || state.PublicEnabled {
		t.Fatalf("bootstrap activation state = %+v", state)
	}
	if _, err := s.CheckSyntheticTeamReadiness(ctx, TeamReadinessOptions{
		ExpectedStoreID: bootstrap.StoreID, ExpectedTeamID: bootstrap.TeamID,
		ReaderVersion: teamauth.SchemaVersion, WriterVersion: teamauth.SchemaVersion,
	}); !errors.Is(err, ErrTeamRemoteInactive) {
		t.Fatalf("inactive readiness error = %v", err)
	}

	gateDigest := testOwnerTargetDigest("ae1-ae12-green")
	targetDigest := SyntheticActivationTargetDigest(bootstrap.StoreID, bootstrap.TeamID, gateDigest)
	issue := OwnerApprovalIssueRequest{
		OwnerPrincipalID: bootstrap.OwnerPrincipalID,
		StoreID:          bootstrap.StoreID, TeamID: bootstrap.TeamID,
		Action: OwnerActionSyntheticActivate, TargetKind: "team_activation", TargetID: bootstrap.TeamID,
		TargetDigest: targetDigest, StepUpAt: now.Add(-time.Minute), ExpiresAt: now.Add(4 * time.Minute),
	}
	issue.Writer = writer
	setTestOwnerAssertion(&issue, "synthetic-activate", now)
	challenge, err := s.IssueOwnerApproval(ctx, issue)
	if err != nil {
		t.Fatal(err)
	}
	activated, err := s.ActivateSyntheticTeamRemote(ctx, ActivateSyntheticTeamRequest{
		ApprovalNonce: challenge.Nonce, GateDigest: gateDigest,
		RequestID: "owner-activate-synthetic", ClientKey: issue.ClientKey, Writer: writer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if activated.ActivationState != TeamActivationActive || !activated.PublicEnabled || activated.ContentBoundary != TeamContentSynthetic || activated.AuditEventID == "" {
		t.Fatalf("activated state = %+v", activated)
	}
	ready, err := s.CheckSyntheticTeamReadiness(ctx, TeamReadinessOptions{
		ExpectedStoreID: bootstrap.StoreID, ExpectedTeamID: bootstrap.TeamID,
		ReaderVersion: teamauth.SchemaVersion, WriterVersion: teamauth.SchemaVersion,
	})
	if err != nil || !ready.Activation.PublicEnabled {
		t.Fatalf("synthetic readiness = %+v, %v", ready, err)
	}
	if _, err := s.DB().Exec(`UPDATE team_policy_metadata SET real_content_state = 'active'`); err == nil {
		t.Fatal("migration allowed real content activation")
	}
	if _, err := s.ActivateSyntheticTeamRemote(ctx, ActivateSyntheticTeamRequest{
		ApprovalNonce: challenge.Nonce, GateDigest: gateDigest,
		RequestID: "owner-activate-replay", ClientKey: issue.ClientKey, Writer: writer,
	}); !errors.Is(err, ErrOwnerApprovalReplay) {
		t.Fatalf("activation replay error = %v", err)
	}
}

func TestSharedDeletionRequiresAndAtomicallyLinksOwnerApproval(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 12, 7, 0, 0, 0, time.UTC)
	s, bootstrap := bootstrapTeamStoreWithClock(t, func() time.Time { return now })
	defer s.Close()
	insertPolicyObject(t, s, bootstrap, "shared-root", "team", bootstrap.TeamID, "")
	lease := acquireReadyWriter(t, s)

	plain := TeamDeletionStartRequest{
		Authorization: TeamMutationAuthorizationRequest{
			PrincipalID: bootstrap.OwnerPrincipalID, Action: teamauth.ActionDelete,
			Capabilities:     []teamauth.Capability{teamauth.CapabilityDelete},
			Context:          teamauth.ActiveContext{TeamID: bootstrap.TeamID},
			ExistingObjectID: "shared-root",
		},
		Writer:    TeamWriterLeaseIdentity{WriterID: lease.WriterID, Token: lease.Token},
		RequestID: "shared-delete-plain", IdempotencyKey: "shared-delete-plain",
	}
	if _, err := s.StartTeamDeletion(ctx, plain); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("shared delete without approval error = %v", err)
	}

	identity, err := s.ResolveOwnerStepUpIdentity(ctx, testBootstrapRoot())
	if err != nil {
		t.Fatal(err)
	}
	issue := OwnerApprovalIssueRequest{
		OwnerPrincipalID: bootstrap.OwnerPrincipalID,
		StoreID:          bootstrap.StoreID, TeamID: bootstrap.TeamID,
		Action: OwnerActionSharedDelete, TargetKind: "team_object", TargetID: "shared-root",
		TargetDigest: SharedDeletionApprovalTargetDigest("shared-root"),
		StepUpAt:     now.Add(-time.Minute), ExpiresAt: now.Add(4 * time.Minute),
	}
	issue.Writer = TeamWriterLeaseIdentity{WriterID: lease.WriterID, Token: lease.Token}
	setTestOwnerAssertion(&issue, "shared-root-delete", now)
	challenge, err := s.IssueOwnerApproval(ctx, issue)
	if err != nil {
		t.Fatal(err)
	}
	approved := TeamSharedDeletionStartRequest{
		ApprovalNonce: challenge.Nonce, ClientKey: identity.ClientKey,
		Writer:    TeamWriterLeaseIdentity{WriterID: lease.WriterID, Token: lease.Token},
		RequestID: "shared-delete-approved", IdempotencyKey: "shared-delete-approved",
		ObjectID: "shared-root",
	}
	started, err := s.StartTeamDeletionWithApproval(ctx, approved)
	if err != nil {
		t.Fatal(err)
	}
	if started.OperationID == "" || started.ObjectID != "shared-root" || started.Replayed {
		t.Fatalf("approved shared deletion = %+v", started)
	}
	var linkedHash, lifecycle string
	if err := s.DB().QueryRow(`
		SELECT operation.owner_approval_nonce_hash, object.lifecycle
		  FROM team_deletion_operations operation
		  JOIN team_object_registry object ON object.object_id = operation.root_object_id
		 WHERE operation.operation_id = ?`, started.OperationID).Scan(&linkedHash, &lifecycle); err != nil {
		t.Fatal(err)
	}
	if linkedHash == "" || linkedHash == challenge.Nonce || lifecycle != "tombstoned" {
		t.Fatalf("approval link/lifecycle = %q/%q", linkedHash, lifecycle)
	}
	replayed, err := s.StartTeamDeletionWithApproval(ctx, approved)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.OperationID != started.OperationID || replayed.AuditEventID != started.AuditEventID {
		t.Fatalf("approved deletion replay = %+v, first=%+v", replayed, started)
	}
}

func TestPostBootstrapOwnerApprovalAndActivationRequireActiveWriterLease(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 12, 7, 30, 0, 0, time.UTC)
	s, bootstrap := bootstrapTeamStoreWithClock(t, func() time.Time { return now })
	defer s.Close()
	lease := acquireReadyWriter(t, s)
	writer := TeamWriterLeaseIdentity{WriterID: lease.WriterID, Token: lease.Token}
	wrongWriter := TeamWriterLeaseIdentity{WriterID: lease.WriterID, Token: "wrong-writer-token"}
	gateDigest := testOwnerTargetDigest("writer-fenced-gate")
	issue := OwnerApprovalIssueRequest{
		OwnerPrincipalID: bootstrap.OwnerPrincipalID,
		StoreID:          bootstrap.StoreID, TeamID: bootstrap.TeamID,
		Action: OwnerActionSyntheticActivate, TargetKind: "team_activation", TargetID: bootstrap.TeamID,
		TargetDigest: SyntheticActivationTargetDigest(bootstrap.StoreID, bootstrap.TeamID, gateDigest),
		StepUpAt:     now.Add(-time.Minute), ExpiresAt: now.Add(4 * time.Minute), Writer: wrongWriter,
	}
	setTestOwnerAssertion(&issue, "writer-fence-rejected", now)
	if _, err := s.IssueOwnerApproval(ctx, issue); !errors.Is(err, ErrTeamWriterLeaseMismatch) {
		t.Fatalf("wrong-writer issue error = %v", err)
	}
	var approvals int
	if err := s.DB().QueryRow(`SELECT count(*) FROM team_owner_approvals`).Scan(&approvals); err != nil || approvals != 0 {
		t.Fatalf("wrong-writer issue left approvals=%d, err=%v", approvals, err)
	}

	issue.Writer = writer
	setTestOwnerAssertion(&issue, "writer-fence-valid", now)
	challenge, err := s.IssueOwnerApproval(ctx, issue)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateSyntheticTeamRemote(ctx, ActivateSyntheticTeamRequest{
		ApprovalNonce: challenge.Nonce, GateDigest: gateDigest,
		RequestID: "writer-fence-activate", ClientKey: issue.ClientKey, Writer: wrongWriter,
	}); !errors.Is(err, ErrTeamWriterLeaseMismatch) {
		t.Fatalf("wrong-writer activation error = %v", err)
	}
	var consumed any
	if err := s.DB().QueryRow(`SELECT consumed_at FROM team_owner_approvals WHERE nonce_hash = ?`,
		ownerApprovalNonceHash(challenge.Nonce)).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed != nil {
		t.Fatal("wrong-writer activation consumed approval")
	}
	state, err := s.ReadTeamActivationState(ctx)
	if err != nil || state.PublicEnabled || state.ActivationState != TeamActivationInactive {
		t.Fatalf("wrong-writer activation changed state: %+v, %v", state, err)
	}
}

func bootstrapTeamStoreWithClock(t *testing.T, clock func() time.Time) (*Store, BootstrapResult) {
	t.Helper()
	s, err := OpenTeam(filepath.Join(t.TempDir(), "team.db"), TeamOpenOptions{
		ExpectedBootstrapRoot: testBootstrapRoot(), Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.BootstrapTeam(context.Background(), BootstrapTeamRequest{
		TeamName: "Synthetic Pilot", PresentedRoot: testBootstrapRoot(),
	})
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	return s, result
}

func testOwnerTargetDigest(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

func setTestOwnerAssertion(request *OwnerApprovalIssueRequest, suffix string, now time.Time) {
	root := testBootstrapRoot()
	request.ClientKey = teamauth.OAuthClientKey(root.Issuer, root.AdminClientID)
	request.AssertionKID = "owner-browser-key-" + suffix
	request.AssertionJTI = "owner-browser-jti-" + suffix
	request.AssertionExpiresAt = now.Add(20 * time.Second)
}
