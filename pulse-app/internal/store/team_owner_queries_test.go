package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func issueTestOwnerQueryApproval(
	t *testing.T,
	s *Store,
	bootstrap BootstrapResult,
	writer TeamWriterLeaseIdentity,
	now time.Time,
	action string,
	targetKind string,
	targetID string,
	targetDigest string,
	suffix string,
) (string, string) {
	t.Helper()
	identity, err := s.ResolveOwnerStepUpIdentity(context.Background(), testBootstrapRoot())
	if err != nil {
		t.Fatal(err)
	}
	request := OwnerApprovalIssueRequest{
		OwnerPrincipalID: bootstrap.OwnerPrincipalID,
		StoreID:          bootstrap.StoreID, TeamID: bootstrap.TeamID,
		ClientKey: identity.ClientKey, Action: action,
		TargetKind: targetKind, TargetID: targetID, TargetDigest: targetDigest,
		StepUpAt: now.Add(-time.Minute), ExpiresAt: now.Add(4 * time.Minute), Writer: writer,
	}
	setTestOwnerAssertion(&request, suffix, now)
	challenge, err := s.IssueOwnerApproval(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return challenge.Nonce, identity.ClientKey
}

func TestApprovedOwnerAuditConsumesExactApprovalAndReadsAcrossPrincipals(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC)
	s, bootstrap := bootstrapTeamStoreWithClock(t, func() time.Time { return now })
	defer s.Close()
	lease := acquireReadyWriter(t, s)
	writer := TeamWriterLeaseIdentity{WriterID: lease.WriterID, Token: lease.Token}
	identity, err := s.ResolveOwnerStepUpIdentity(ctx, testBootstrapRoot())
	if err != nil {
		t.Fatal(err)
	}
	mutation := OwnerAdminMutation{
		Action: OwnerActionMembershipCreate,
		Issuer: "https://issuer.example", Subject: "owner-audit-member", Role: "member",
	}
	created := executeTestOwnerMutation(
		t, s, bootstrap, writer, identity.ClientKey, now, mutation, "owner-query-member",
	)
	if created.AuditEventID == "" {
		t.Fatal("owner mutation did not create an audit event")
	}

	nonce, clientKey := issueTestOwnerQueryApproval(
		t, s, bootstrap, writer, now,
		OwnerActionTeamAuditInspect, "team_audit", bootstrap.TeamID,
		OwnerAuditApprovalTargetDigest("", 10), "owner-audit-query",
	)
	page, err := s.ReadApprovedOwnerAudit(ctx, ApprovedOwnerAuditRequest{
		ApprovalNonce: nonce, RequestID: "owner-audit-query-request", ClientKey: clientKey,
		Writer: writer, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range page.Events {
		if event.EventID == created.AuditEventID && event.Action == OwnerActionMembershipCreate {
			found = true
		}
	}
	if !found {
		t.Fatalf("cross-principal Owner audit did not include mutation event: %+v", page)
	}
	if _, err := s.ReadApprovedOwnerAudit(ctx, ApprovedOwnerAuditRequest{
		ApprovalNonce: nonce, RequestID: "owner-audit-query-replay", ClientKey: clientKey,
		Writer: writer, Limit: 10,
	}); !errors.Is(err, ErrOwnerApprovalReplay) {
		t.Fatalf("owner audit replay error = %v", err)
	}
}

func TestApprovedOwnerAuditPaginatesWithoutOverlapAndBindsCursorAndLimit(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 15, 3, 30, 0, 0, time.UTC)
	s, bootstrap := bootstrapTeamStoreWithClock(t, func() time.Time { return now })
	defer s.Close()
	lease := acquireReadyWriter(t, s)
	writer := TeamWriterLeaseIdentity{WriterID: lease.WriterID, Token: lease.Token}
	identity, err := s.ResolveOwnerStepUpIdentity(ctx, testBootstrapRoot())
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		mutation := OwnerAdminMutation{
			Action: OwnerActionMembershipCreate, Issuer: "https://issuer.example",
			Subject: "owner-audit-page-member-" + string(rune('a'+index)), Role: "member",
		}
		executeTestOwnerMutation(
			t, s, bootstrap, writer, identity.ClientKey, now, mutation,
			"owner-audit-page-"+string(rune('a'+index)),
		)
	}

	const limit = 2
	firstNonce, firstClientKey := issueTestOwnerQueryApproval(
		t, s, bootstrap, writer, now,
		OwnerActionTeamAuditInspect, "team_audit", bootstrap.TeamID,
		OwnerAuditApprovalTargetDigest("", limit), "owner-audit-page-first",
	)
	first, err := s.ReadApprovedOwnerAudit(ctx, ApprovedOwnerAuditRequest{
		ApprovalNonce: firstNonce, RequestID: "owner-audit-page-first", ClientKey: firstClientKey,
		Writer: writer, Limit: limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != limit || first.NextCursor == "" {
		t.Fatalf("first Owner audit page = %+v", first)
	}

	secondNonce, secondClientKey := issueTestOwnerQueryApproval(
		t, s, bootstrap, writer, now,
		OwnerActionTeamAuditInspect, "team_audit", bootstrap.TeamID,
		OwnerAuditApprovalTargetDigest(first.NextCursor, limit), "owner-audit-page-second",
	)
	second, err := s.ReadApprovedOwnerAudit(ctx, ApprovedOwnerAuditRequest{
		ApprovalNonce: secondNonce, RequestID: "owner-audit-page-second", ClientKey: secondClientKey,
		Writer: writer, Cursor: first.NextCursor, Limit: limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) == 0 {
		t.Fatalf("second Owner audit page = %+v", second)
	}
	firstIDs := make(map[string]struct{}, len(first.Events))
	for _, event := range first.Events {
		firstIDs[event.EventID] = struct{}{}
	}
	for _, event := range second.Events {
		if _, overlaps := firstIDs[event.EventID]; overlaps {
			t.Fatalf("Owner audit pages overlap at event %q: first=%+v second=%+v",
				event.EventID, first, second)
		}
	}

	for _, test := range []struct {
		name   string
		cursor string
		limit  int
	}{
		{name: "cursor", cursor: "", limit: limit},
		{name: "limit", cursor: first.NextCursor, limit: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			nonce, clientKey := issueTestOwnerQueryApproval(
				t, s, bootstrap, writer, now,
				OwnerActionTeamAuditInspect, "team_audit", bootstrap.TeamID,
				OwnerAuditApprovalTargetDigest(first.NextCursor, limit),
				"owner-audit-binding-"+test.name,
			)
			if _, err := s.ReadApprovedOwnerAudit(ctx, ApprovedOwnerAuditRequest{
				ApprovalNonce: nonce, RequestID: "owner-audit-binding-" + test.name,
				ClientKey: clientKey, Writer: writer, Cursor: test.cursor, Limit: test.limit,
			}); !errors.Is(err, ErrOwnerApprovalBindingMismatch) {
				t.Fatalf("%s binding mismatch error = %v", test.name, err)
			}
			var consumed any
			if err := s.DB().QueryRow(
				`SELECT consumed_at FROM team_owner_approvals WHERE nonce_hash = ?`,
				ownerApprovalNonceHash(nonce),
			).Scan(&consumed); err != nil {
				t.Fatal(err)
			}
			if consumed != nil {
				t.Fatalf("%s binding mismatch consumed its Owner approval", test.name)
			}
		})
	}
}

func TestApprovedOwnerDeletionStatusIsBoundToExactOperation(t *testing.T) {
	ctx := context.Background()
	fixture := newTeamDeletionFixture(t)
	defer fixture.close(t)
	root := fixture.storeMemory(t, "owner-status")
	started, err := fixture.object.store.StartTeamDeletion(
		ctx, fixture.startRequest(root.ObjectID, "owner-status-delete-0001"),
	)
	if err != nil {
		t.Fatal(err)
	}
	writer := fixture.object.request.Writer
	nonce, clientKey := issueTestOwnerQueryApproval(
		t, fixture.object.store, fixture.object.bootstrap, writer, *fixture.now,
		OwnerActionDeletionStatus, "deletion_operation", started.OperationID,
		OwnerDeletionStatusApprovalTargetDigest(started.OperationID), "owner-deletion-status",
	)
	status, err := fixture.object.store.ReadApprovedOwnerDeletionStatus(
		ctx, ApprovedOwnerDeletionStatusRequest{
			ApprovalNonce: nonce, RequestID: "owner-deletion-status-request", ClientKey: clientKey,
			Writer: writer, OperationID: started.OperationID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if status.OperationID != started.OperationID || status.ObjectID != root.ObjectID ||
		status.Status != TeamDeletionStatusInProgress {
		t.Fatalf("owner deletion status = %+v", status)
	}

	wrongNonce, wrongClientKey := issueTestOwnerQueryApproval(
		t, fixture.object.store, fixture.object.bootstrap, writer, *fixture.now,
		OwnerActionDeletionStatus, "deletion_operation", started.OperationID,
		OwnerDeletionStatusApprovalTargetDigest(started.OperationID), "owner-deletion-status-wrong",
	)
	if _, err := fixture.object.store.ReadApprovedOwnerDeletionStatus(
		ctx, ApprovedOwnerDeletionStatusRequest{
			ApprovalNonce: wrongNonce, RequestID: "owner-deletion-status-wrong", ClientKey: wrongClientKey,
			Writer: writer, OperationID: "delete_operation_absent",
		},
	); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("wrong operation error = %v", err)
	}
	var consumed any
	if err := fixture.object.store.DB().QueryRow(
		`SELECT consumed_at FROM team_owner_approvals WHERE nonce_hash = ?`,
		ownerApprovalNonceHash(wrongNonce),
	).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed != nil {
		t.Fatal("failed Owner status query consumed its approval")
	}
}
