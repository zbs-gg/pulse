package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

type ApprovedOwnerAuditRequest struct {
	ApprovalNonce string
	RequestID     string
	ClientKey     string
	Writer        TeamWriterLeaseIdentity
	Cursor        string
	Limit         int
}

type ApprovedOwnerDeletionStatusRequest struct {
	ApprovalNonce string
	RequestID     string
	ClientKey     string
	Writer        TeamWriterLeaseIdentity
	OperationID   string
}

func OwnerAuditApprovalTargetDigest(cursor string, limit int) string {
	return ownerApprovalDigest(OwnerActionTeamAuditInspect, cursor, strconv.Itoa(limit))
}

func OwnerDeletionStatusApprovalTargetDigest(operationID string) string {
	return ownerApprovalDigest(OwnerActionDeletionStatus, operationID)
}

func (s *Store) ReadApprovedOwnerAudit(
	ctx context.Context,
	request ApprovedOwnerAuditRequest,
) (TeamAuditPage, error) {
	if request.Limit == 0 {
		request.Limit = 50
	}
	if !validOwnerNonce(request.ApprovalNonce) || !validOwnerOpaque(request.RequestID, 255) ||
		!validOwnerClientKey(request.ClientKey) ||
		(request.Cursor != "" && !validOwnerOpaque(request.Cursor, 255)) ||
		request.Limit < 1 || request.Limit > 50 ||
		!validTeamOpaque(request.Writer.WriterID, 1, 255) ||
		!validTeamOpaque(request.Writer.Token, 1, 255) {
		return TeamAuditPage{}, ErrOwnerApprovalInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TeamAuditPage{}, err
	}
	defer tx.Rollback()
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return TeamAuditPage{}, err
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.Writer.WriterID, request.Writer.Token); err != nil {
		return TeamAuditPage{}, err
	}
	consume := OwnerApprovalConsumeRequest{
		StoreID: info.StoreID, TeamID: info.TeamID,
		Action: OwnerActionTeamAuditInspect, TargetKind: "team_audit", TargetID: info.TeamID,
		TargetDigest: OwnerAuditApprovalTargetDigest(request.Cursor, request.Limit),
		Nonce:        request.ApprovalNonce, RequestID: request.RequestID, ClientKey: request.ClientKey,
		Writer: request.Writer,
	}
	if _, err := s.peekOwnerApprovalTx(ctx, tx, info, consume); err != nil {
		return TeamAuditPage{}, err
	}
	page, err := readOwnerAuditPageTx(ctx, tx, info.TeamID, request.Cursor, request.Limit)
	if err != nil {
		return TeamAuditPage{}, err
	}
	if _, err := s.consumeOwnerApprovalTxWithAuditTarget(
		ctx, tx, info, consume, "team_audit", info.TeamID,
	); err != nil {
		return TeamAuditPage{}, err
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.Writer.WriterID, request.Writer.Token); err != nil {
		return TeamAuditPage{}, err
	}
	if err := tx.Commit(); err != nil {
		if isConstraintError(err) {
			return TeamAuditPage{}, ErrOwnerApprovalReplay
		}
		return TeamAuditPage{}, err
	}
	return page, nil
}

func readOwnerAuditPageTx(
	ctx context.Context,
	tx *sql.Tx,
	teamID string,
	cursor string,
	limit int,
) (TeamAuditPage, error) {
	beforeSequence := int64(0)
	if cursor != "" {
		err := tx.QueryRowContext(ctx, `
			SELECT ordered.audit_sequence
			  FROM team_audit_event_order ordered
			  JOIN team_audit_events audit ON audit.event_id = ordered.event_id
			 WHERE audit.event_id = ? AND audit.team_id = ?`, cursor, teamID).
			Scan(&beforeSequence)
		if errors.Is(err, sql.ErrNoRows) {
			return TeamAuditPage{}, ErrConcealedNotFound
		}
		if err != nil {
			return TeamAuditPage{}, err
		}
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT ordered.audit_sequence, audit.event_id, audit.occurred_at,
		       audit.action, audit.outcome, COALESCE(audit.actor_principal_id, ''),
		       COALESCE(audit.client_key, ''), audit.team_id,
		       COALESCE(audit.project_id, ''), audit.target_kind,
		       COALESCE(audit.target_id, ''), COALESCE(audit.request_id, ''),
		       audit.policy_version, audit.auth_epoch, audit.reason_code
		  FROM team_audit_event_order ordered
		  JOIN team_audit_events audit ON audit.event_id = ordered.event_id
		 WHERE audit.team_id = ? AND (? = 0 OR ordered.audit_sequence < ?)
		 ORDER BY ordered.audit_sequence DESC
		 LIMIT ?`, teamID, beforeSequence, beforeSequence, limit+1)
	if err != nil {
		return TeamAuditPage{}, err
	}
	defer rows.Close()
	events := make([]TeamAuditMetadata, 0, limit+1)
	for rows.Next() {
		var sequence int64
		var occurredText string
		var event TeamAuditMetadata
		if err := rows.Scan(
			&sequence, &event.EventID, &occurredText, &event.Action, &event.Outcome,
			&event.ActorPrincipalID, &event.ClientKey, &event.TeamID, &event.ProjectID,
			&event.TargetKind, &event.TargetID, &event.RequestID, &event.PolicyVersion,
			&event.AuthEpoch, &event.ReasonCode,
		); err != nil {
			return TeamAuditPage{}, err
		}
		event.OccurredAt, err = time.Parse(time.RFC3339Nano, occurredText)
		if err != nil {
			return TeamAuditPage{}, ErrTeamPolicyNotReady
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return TeamAuditPage{}, err
	}
	page := TeamAuditPage{Events: events}
	if len(page.Events) > limit {
		page.Events = page.Events[:limit]
		page.NextCursor = page.Events[len(page.Events)-1].EventID
	}
	return page, nil
}

func (s *Store) ReadApprovedOwnerDeletionStatus(
	ctx context.Context,
	request ApprovedOwnerDeletionStatusRequest,
) (TeamDeletionStatus, error) {
	if !validOwnerNonce(request.ApprovalNonce) || !validOwnerOpaque(request.RequestID, 255) ||
		!validOwnerClientKey(request.ClientKey) || !validOwnerOpaque(request.OperationID, 255) ||
		!validTeamOpaque(request.Writer.WriterID, 1, 255) ||
		!validTeamOpaque(request.Writer.Token, 1, 255) {
		return TeamDeletionStatus{}, ErrOwnerApprovalInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TeamDeletionStatus{}, err
	}
	defer tx.Rollback()
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return TeamDeletionStatus{}, err
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.Writer.WriterID, request.Writer.Token); err != nil {
		return TeamDeletionStatus{}, err
	}
	operation, err := loadTeamDeletionOperation(ctx, tx, request.OperationID)
	if errors.Is(err, sql.ErrNoRows) {
		return TeamDeletionStatus{}, ErrConcealedNotFound
	}
	if err != nil {
		return TeamDeletionStatus{}, err
	}
	if operation.StoreID != info.StoreID || operation.TeamID != info.TeamID {
		return TeamDeletionStatus{}, ErrConcealedNotFound
	}
	consume := OwnerApprovalConsumeRequest{
		StoreID: info.StoreID, TeamID: info.TeamID,
		Action: OwnerActionDeletionStatus, TargetKind: "deletion_operation", TargetID: request.OperationID,
		TargetDigest: OwnerDeletionStatusApprovalTargetDigest(request.OperationID),
		Nonce:        request.ApprovalNonce, RequestID: request.RequestID, ClientKey: request.ClientKey,
		Writer: request.Writer,
	}
	if _, err := s.consumeOwnerApprovalTxWithAuditTarget(
		ctx, tx, info, consume, "deletion_operation", request.OperationID,
	); err != nil {
		return TeamDeletionStatus{}, err
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.Writer.WriterID, request.Writer.Token); err != nil {
		return TeamDeletionStatus{}, err
	}
	if err := tx.Commit(); err != nil {
		if isConstraintError(err) {
			return TeamDeletionStatus{}, ErrOwnerApprovalReplay
		}
		return TeamDeletionStatus{}, err
	}
	return deletionStatusFromOperation(operation), nil
}
