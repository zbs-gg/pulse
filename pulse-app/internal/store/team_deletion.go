package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

var (
	ErrTeamDeletionInvalid = errors.New("team_deletion_invalid")
	ErrTeamDeletionBarrier = errors.New("team_deletion_barrier_failed")
)

const (
	TeamDeletionStatusInProgress    = "deletion_in_progress"
	TeamDeletionStatusCleanupFailed = "cleanup_failed"
	TeamDeletionStatusComplete      = "complete"

	TeamDeletionFailureTemporary          = "temporary_failure"
	TeamDeletionFailureStorageUnavailable = "storage_unavailable"
	TeamDeletionFailureWorkerInterrupted  = "worker_interrupted"

	maxTeamDeletionClaimBatch = 64
	maxTeamDeletionLeaseTTL   = 5 * time.Minute
	maxTeamDeletionBackoff    = 24 * time.Hour
)

type TeamDeletionStartRequest struct {
	Authorization  TeamMutationAuthorizationRequest
	Writer         TeamWriterLeaseIdentity
	RequestID      string
	IdempotencyKey string
}

type TeamDeletionResult struct {
	OperationID  string
	ObjectID     string
	AuditEventID string
	Status       string
	Replayed     bool
}

type TeamDeletionStatusRequest struct {
	PrincipalID    string
	OAuthClientKey string
	Capabilities   []teamauth.Capability
	Context        teamauth.ActiveContext
	OperationID    string
}

type TeamDeletionStatus struct {
	OperationID   string
	ObjectID      string
	AuditEventID  string
	Status        string
	AttemptCount  int
	LastErrorCode string
	NextAttemptAt *time.Time
	StartedAt     time.Time
	UpdatedAt     time.Time
	CompletedAt   *time.Time
}

type TeamDeletionClaimRequest struct {
	WriterID    string
	WriterToken string
	Limit       int
	LeaseTTL    time.Duration
}

type TeamDeletionClaim struct {
	OperationID    string
	RootObjectID   string
	RootGeneration int64
	AttemptCount   int
	LeaseToken     string
	LeaseExpiresAt time.Time
}

type TeamDeletionFailureRequest struct {
	WriterID    string
	WriterToken string
	OperationID string
	LeaseToken  string
	ErrorCode   string
	Backoff     time.Duration
}

type TeamDeletionReapRequest struct {
	WriterID    string
	WriterToken string
	Limit       int
}

type TeamDeletionCompletionRequest struct {
	WriterID    string
	WriterToken string
	OperationID string
	LeaseToken  string
}

type normalizedTeamDeletionStart struct {
	authorization   TeamMutationAuthorizationRequest
	writer          TeamWriterLeaseIdentity
	requestID       string
	idempotencyHash string
	bodyDigest      string
}

type teamDeletionPrincipalAuthorization struct {
	info      TeamStoreInfo
	principal ResolvedTeamPrincipal
	actor     teamauth.Actor
}

type teamDeletionOperation struct {
	OperationID       string
	StoreID           string
	TeamID            string
	RootObjectID      string
	RootGeneration    int64
	ActorPrincipalID  string
	OAuthClientKey    string
	RequestID         string
	BodyDigest        string
	StartAuditEventID string
	State             string
	AttemptCount      int
	LeaseTokenHash    string
	LeaseExpiresAt    *time.Time
	NextAttemptAt     *time.Time
	LastErrorCode     string
	StartedAt         time.Time
	UpdatedAt         time.Time
	CompletedAt       *time.Time
}

// StartTeamDeletion is the specialized authorization/idempotency boundary for
// destructive team mutations. It authenticates the current principal first,
// then checks durable replay before consulting lifecycle. That ordering lets a
// response-loss retry return the original operation after tombstone without
// allowing a revoked binding or lost project grant to observe deletion state.
func (s *Store) StartTeamDeletion(ctx context.Context, request TeamDeletionStartRequest) (TeamDeletionResult, error) {
	return s.startTeamDeletionWithResponseBoundary(ctx, request, nil)
}

func (s *Store) startTeamDeletionWithResponseBoundary(
	ctx context.Context,
	request TeamDeletionStartRequest,
	beforeResponseRecheck func(),
) (TeamDeletionResult, error) {
	normalized, err := normalizeTeamDeletionStart(request)
	if err != nil {
		return TeamDeletionResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TeamDeletionResult{}, err
	}
	defer tx.Rollback()
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, normalized.writer.WriterID, normalized.writer.Token); err != nil {
		return TeamDeletionResult{}, err
	}
	principal, err := authorizeTeamDeletionPrincipal(ctx, tx, normalized.authorization,
		teamauth.ActionDelete)
	if err != nil {
		return TeamDeletionResult{}, concealTeamDeletionAuthorization(err)
	}
	normalized.authorization, err = canonicalizeTeamDeletionAuthorization(principal, normalized.authorization)
	if err != nil {
		return TeamDeletionResult{}, ErrConcealedNotFound
	}
	normalized.bodyDigest = teamDeletionBodyDigest(normalized.authorization)

	replayedOperation, found, err := loadTeamDeletionReplay(ctx, tx, principal, normalized)
	if err != nil {
		return TeamDeletionResult{}, err
	}
	if found {
		if _, err := authorizeTeamDeletionObject(ctx, tx, principal, normalized.authorization,
			replayedOperation.RootObjectID, true); err != nil {
			return TeamDeletionResult{}, concealTeamDeletionAuthorization(err)
		}
		if err := s.RecheckTeamWriterLeaseTx(ctx, tx, normalized.writer.WriterID, normalized.writer.Token); err != nil {
			return TeamDeletionResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return TeamDeletionResult{}, err
		}
		replayedOperation, err = s.recheckTeamDeletionOperationAccess(
			ctx, normalized.authorization, replayedOperation.OperationID,
			normalized.bodyDigest, beforeResponseRecheck)
		if err != nil {
			return TeamDeletionResult{}, err
		}
		return TeamDeletionResult{
			OperationID:  replayedOperation.OperationID,
			ObjectID:     replayedOperation.RootObjectID,
			AuditEventID: replayedOperation.StartAuditEventID,
			Status:       deletionStartResultStatus(replayedOperation.State), Replayed: true,
		}, nil
	}

	object, err := authorizeTeamDeletionObject(ctx, tx, principal, normalized.authorization,
		normalized.authorization.ExistingObjectID, false)
	if err != nil {
		return TeamDeletionResult{}, concealTeamDeletionAuthorization(err)
	}
	// U7 deletion is a root operation. Deleting a derivative in place would
	// invalidate a still-active parent's ready projection without rebuilding or
	// requeueing that parent. Derivative correction/delete semantics belong to a
	// later contribution-aware review surface; conceal them here.
	var inboundContributions, pendingFrontiers int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM team_object_contributions
		 WHERE derivative_object_id = ?`, object.ObjectID).Scan(&inboundContributions); err != nil {
		return TeamDeletionResult{}, err
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*)
		  FROM team_deletion_frontier frontier
		  JOIN team_deletion_operations operation
		    ON operation.operation_id = frontier.operation_id
		 WHERE frontier.object_id = ? AND frontier.depth > 0
		   AND operation.state <> 'complete'`, object.ObjectID).Scan(&pendingFrontiers); err != nil {
		return TeamDeletionResult{}, err
	}
	if inboundContributions != 0 || pendingFrontiers != 0 {
		return TeamDeletionResult{}, ErrConcealedNotFound
	}
	now := s.clock().UTC()
	nowText := now.Format(time.RFC3339Nano)
	projectID := normalized.authorization.Context.ProjectID
	if projectID == "" && object.Scope.Type == teamauth.ScopeProject {
		projectID = object.Scope.ID
	}
	startAuditID, err := appendTeamDomainAudit(ctx, tx, teamDomainAuditEvent{
		StoreID: principal.info.StoreID, TeamID: principal.info.TeamID,
		ProjectID: projectID, ActorPrincipal: principal.principal.PrincipalID,
		OAuthClientKey: normalized.authorization.OAuthClientKey,
		RequestID:      normalized.requestID, Action: teamObjectDeleteStartAction,
		TargetKind: object.ObjectKind, TargetID: object.ObjectID,
		PolicyVersion: teamauth.PolicyVersion, AuthorizationAt: principal.info.AuthEpoch,
		ReasonCode: teamObjectDeleteStartedReason, OccurredAt: now,
	})
	if err != nil {
		return TeamDeletionResult{}, err
	}
	updated, err := tx.ExecContext(ctx, `
		UPDATE team_object_registry
		   SET lifecycle = 'tombstoned', generation = generation + 1, updated_at = ?
		 WHERE object_id = ? AND store_id = ? AND team_id = ?
		   AND lifecycle = 'active' AND generation = ?`,
		nowText, object.ObjectID, principal.info.StoreID, principal.info.TeamID,
		object.Scope.Generation)
	if err != nil {
		return TeamDeletionResult{}, err
	}
	if rows, rowsErr := updated.RowsAffected(); rowsErr != nil || rows != 1 {
		return TeamDeletionResult{}, ErrConcealedNotFound
	}
	operationID, err := newOpaqueID("delete_operation")
	if err != nil {
		return TeamDeletionResult{}, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO team_deletion_operations(
			operation_id, store_id, team_id, root_object_id, root_generation,
			actor_principal_id, oauth_client_key, request_id, idempotency_key_hash,
			body_digest, start_audit_event_id, state, attempt_count,
			lease_token_hash, lease_expires_at, next_attempt_at, last_error_code,
			started_at, updated_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', 0,
		        NULL, NULL, ?, NULL, ?, ?, NULL)`,
		operationID, principal.info.StoreID, principal.info.TeamID, object.ObjectID,
		object.Scope.Generation, principal.principal.PrincipalID,
		normalized.authorization.OAuthClientKey, normalized.requestID,
		normalized.idempotencyHash, normalized.bodyDigest, startAuditID,
		nowText, nowText, nowText)
	if err != nil {
		return TeamDeletionResult{}, err
	}
	if err := captureTeamDeletionFrontier(ctx, tx, operationID, object.ObjectID,
		object.Scope.Generation, now); err != nil {
		return TeamDeletionResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM team_object_contributions
		 WHERE parent_object_id = ? OR derivative_object_id = ?`,
		object.ObjectID, object.ObjectID); err != nil {
		return TeamDeletionResult{}, err
	}
	if _, err := s.CancelTeamProjectionJobsTx(ctx, tx, TeamProjectionCancellationRequest{
		WriterID: normalized.writer.WriterID, WriterToken: normalized.writer.Token,
		RootObjectID: object.ObjectID, RootGeneration: object.Scope.Generation,
		ReasonCode: TeamProjectionCancellationRootTombstoned,
	}); err != nil {
		return TeamDeletionResult{}, err
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, normalized.writer.WriterID, normalized.writer.Token); err != nil {
		return TeamDeletionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return TeamDeletionResult{}, err
	}
	operation, err := s.recheckTeamDeletionOperationAccess(
		ctx, normalized.authorization, operationID, normalized.bodyDigest, beforeResponseRecheck)
	if err != nil {
		return TeamDeletionResult{}, err
	}
	return TeamDeletionResult{
		OperationID: operation.OperationID, ObjectID: operation.RootObjectID,
		AuditEventID: operation.StartAuditEventID, Status: deletionStartResultStatus(operation.State),
	}, nil
}

func (s *Store) ReadTeamDeletionStatus(ctx context.Context, request TeamDeletionStatusRequest) (TeamDeletionStatus, error) {
	return s.readTeamDeletionStatusWithResponseBoundary(ctx, request, nil)
}

func (s *Store) readTeamDeletionStatusWithResponseBoundary(
	ctx context.Context,
	request TeamDeletionStatusRequest,
	beforeResponseRecheck func(),
) (TeamDeletionStatus, error) {
	if !validTeamDeletionStatusRequest(request) {
		return TeamDeletionStatus{}, ErrTeamDeletionInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TeamDeletionStatus{}, err
	}
	defer tx.Rollback()
	authorization := TeamMutationAuthorizationRequest{
		PrincipalID: request.PrincipalID, OAuthClientKey: request.OAuthClientKey,
		Action: teamauth.ActionRead, Capabilities: append([]teamauth.Capability(nil), request.Capabilities...),
		Context: request.Context,
	}
	principal, err := authorizeTeamDeletionPrincipal(ctx, tx, authorization, teamauth.ActionRead)
	if err != nil {
		return TeamDeletionStatus{}, concealTeamDeletionAuthorization(err)
	}
	authorization, err = canonicalizeTeamDeletionAuthorization(principal, authorization)
	if err != nil {
		return TeamDeletionStatus{}, ErrConcealedNotFound
	}
	operation, err := loadTeamDeletionOperation(ctx, tx, request.OperationID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TeamDeletionStatus{}, ErrConcealedNotFound
		}
		return TeamDeletionStatus{}, err
	}
	authorization.ExistingObjectID = operation.RootObjectID
	if operation.StoreID != principal.info.StoreID || operation.TeamID != principal.info.TeamID {
		return TeamDeletionStatus{}, ErrConcealedNotFound
	}
	if _, err := authorizeTeamDeletionObject(ctx, tx, principal, authorization,
		operation.RootObjectID, true); err != nil {
		return TeamDeletionStatus{}, concealTeamDeletionAuthorization(err)
	}
	if err := tx.Commit(); err != nil {
		return TeamDeletionStatus{}, err
	}
	operation, err = s.recheckTeamDeletionOperationAccess(
		ctx, authorization, request.OperationID, "", beforeResponseRecheck)
	if err != nil {
		return TeamDeletionStatus{}, err
	}
	return deletionStatusFromOperation(operation), nil
}

func (s *Store) ClaimTeamDeletionJobs(ctx context.Context, request TeamDeletionClaimRequest) ([]TeamDeletionClaim, error) {
	if !validTeamDeletionClaimRequest(request) {
		return nil, ErrTeamDeletionInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.WriterID, request.WriterToken); err != nil {
		return nil, err
	}
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return nil, err
	}
	if _, err := readTeamPolicyState(ctx, tx); err != nil {
		return nil, err
	}
	now := s.clock().UTC()
	nowText := now.Format(time.RFC3339Nano)
	rows, err := tx.QueryContext(ctx, `
		SELECT operation.operation_id, operation.root_object_id,
		       operation.root_generation, operation.attempt_count, operation.state,
		       root.lifecycle
		  FROM team_deletion_operations operation
		  JOIN team_object_registry root ON root.object_id = operation.root_object_id
		 WHERE operation.store_id = ? AND operation.team_id = ?
		   AND operation.state IN ('pending', 'cleanup_failed')
		   AND julianday(operation.next_attempt_at) <= julianday(?)
		   AND root.store_id = operation.store_id AND root.team_id = operation.team_id
		   AND root.generation = operation.root_generation + 1
		   AND ((operation.state = 'pending' AND root.lifecycle = 'tombstoned')
		     OR (operation.state = 'cleanup_failed'
		         AND root.lifecycle IN ('cleanup_failed', 'tombstoned')))
		 ORDER BY julianday(operation.next_attempt_at), julianday(operation.started_at), operation.operation_id
		 LIMIT ?`, info.StoreID, info.TeamID, nowText, request.Limit)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		operationID, rootID, state, rootLifecycle string
		rootGeneration                            int64
		attempt                                   int
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.operationID, &item.rootID, &item.rootGeneration,
			&item.attempt, &item.state, &item.rootLifecycle); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	claims := make([]TeamDeletionClaim, 0, len(candidates))
	for _, candidate := range candidates {
		token, err := newTeamDeletionLeaseToken()
		if err != nil {
			return nil, err
		}
		expires := now.Add(request.LeaseTTL)
		result, err := tx.ExecContext(ctx, `
			UPDATE team_deletion_operations
			   SET state = 'leased', attempt_count = attempt_count + 1,
			       lease_token_hash = ?, lease_expires_at = ?, next_attempt_at = NULL,
			       last_error_code = NULL, updated_at = ?
			 WHERE operation_id = ? AND state = ? AND attempt_count = ?`,
			teamDeletionLeaseTokenHash(token), expires.Format(time.RFC3339Nano), nowText,
			candidate.operationID, candidate.state, candidate.attempt)
		if err != nil {
			return nil, err
		}
		if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
			return nil, ErrConcealedNotFound
		}
		result, err = tx.ExecContext(ctx, `
			UPDATE team_object_registry
			   SET lifecycle = 'cleaning', updated_at = ?
			 WHERE object_id = ? AND generation = ? AND lifecycle = ?`,
			nowText, candidate.rootID, candidate.rootGeneration+1, candidate.rootLifecycle)
		if err != nil {
			return nil, err
		}
		if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
			return nil, ErrConcealedNotFound
		}
		claims = append(claims, TeamDeletionClaim{
			OperationID: candidate.operationID, RootObjectID: candidate.rootID,
			RootGeneration: candidate.rootGeneration, AttemptCount: candidate.attempt + 1,
			LeaseToken: token, LeaseExpiresAt: expires,
		})
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.WriterID, request.WriterToken); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claims, nil
}

func (s *Store) FailTeamDeletion(ctx context.Context, request TeamDeletionFailureRequest) error {
	if !validTeamDeletionFailureRequest(request) {
		return ErrTeamDeletionInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.WriterID, request.WriterToken); err != nil {
		return err
	}
	operation, err := loadTeamDeletionOperation(ctx, tx, request.OperationID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConcealedNotFound
	}
	if err != nil {
		return err
	}
	if operation.State != "leased" || operation.LeaseExpiresAt == nil ||
		!operation.LeaseExpiresAt.After(s.clock().UTC()) ||
		!teamDeletionLeaseTokenMatches(operation.LeaseTokenHash, request.LeaseToken) {
		return ErrConcealedNotFound
	}
	now := s.clock().UTC()
	next := now.Add(request.Backoff)
	rootTransition, err := tx.ExecContext(ctx, `
		UPDATE team_object_registry SET lifecycle = 'cleanup_failed', updated_at = ?
		 WHERE object_id = ? AND generation = ? AND lifecycle = 'cleaning'`,
		now.Format(time.RFC3339Nano), operation.RootObjectID, operation.RootGeneration+1)
	if err != nil {
		return err
	}
	if affected, affectedErr := rootTransition.RowsAffected(); affectedErr != nil || affected != 1 {
		return ErrConcealedNotFound
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE team_deletion_operations
		   SET state = 'cleanup_failed', lease_token_hash = NULL, lease_expires_at = NULL,
		       next_attempt_at = ?, last_error_code = ?, updated_at = ?
		 WHERE operation_id = ? AND state = 'leased' AND lease_token_hash = ?`,
		next.Format(time.RFC3339Nano), request.ErrorCode, now.Format(time.RFC3339Nano),
		request.OperationID, operation.LeaseTokenHash)
	if err != nil {
		return err
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
		return ErrConcealedNotFound
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.WriterID, request.WriterToken); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ReapExpiredTeamDeletionLeases(ctx context.Context, request TeamDeletionReapRequest) (int64, error) {
	if !validTeamDeletionReapRequest(request) {
		return 0, ErrTeamDeletionInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.WriterID, request.WriterToken); err != nil {
		return 0, err
	}
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return 0, err
	}
	if _, err := readTeamPolicyState(ctx, tx); err != nil {
		return 0, err
	}
	now := s.clock().UTC()
	nowText := now.Format(time.RFC3339Nano)
	rows, err := tx.QueryContext(ctx, `
		SELECT operation_id, root_object_id, root_generation
		  FROM team_deletion_operations
		 WHERE store_id = ? AND team_id = ? AND state = 'leased'
		   AND julianday(lease_expires_at) <= julianday(?)
		 ORDER BY julianday(lease_expires_at), operation_id LIMIT ?`,
		info.StoreID, info.TeamID, nowText, request.Limit)
	if err != nil {
		return 0, err
	}
	type expired struct {
		operationID, rootID string
		generation          int64
	}
	var expiredOperations []expired
	for rows.Next() {
		var item expired
		if err := rows.Scan(&item.operationID, &item.rootID, &item.generation); err != nil {
			rows.Close()
			return 0, err
		}
		expiredOperations = append(expiredOperations, item)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, operation := range expiredOperations {
		rootTransition, err := tx.ExecContext(ctx, `
			UPDATE team_object_registry SET lifecycle = 'tombstoned', updated_at = ?
			 WHERE object_id = ? AND generation = ? AND lifecycle = 'cleaning'`,
			nowText, operation.rootID, operation.generation+1)
		if err != nil {
			return 0, err
		}
		if affected, affectedErr := rootTransition.RowsAffected(); affectedErr != nil || affected != 1 {
			return 0, ErrConcealedNotFound
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE team_deletion_operations
			   SET state = 'cleanup_failed', lease_token_hash = NULL, lease_expires_at = NULL,
			       next_attempt_at = ?, last_error_code = ?, updated_at = ?
			 WHERE operation_id = ? AND state = 'leased'
			   AND julianday(lease_expires_at) <= julianday(?)`,
			nowText, TeamDeletionFailureWorkerInterrupted, nowText,
			operation.operationID, nowText)
		if err != nil {
			return 0, err
		}
		if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
			return 0, ErrConcealedNotFound
		}
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.WriterID, request.WriterToken); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(expiredOperations)), nil
}

func (s *Store) CompleteTeamDeletion(ctx context.Context, request TeamDeletionCompletionRequest) (TeamDeletionStatus, error) {
	if !validTeamDeletionCompletionRequest(request) {
		return TeamDeletionStatus{}, ErrTeamDeletionInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TeamDeletionStatus{}, err
	}
	defer tx.Rollback()
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.WriterID, request.WriterToken); err != nil {
		return TeamDeletionStatus{}, err
	}
	operation, err := loadTeamDeletionOperation(ctx, tx, request.OperationID)
	if errors.Is(err, sql.ErrNoRows) {
		return TeamDeletionStatus{}, ErrConcealedNotFound
	}
	if err != nil {
		return TeamDeletionStatus{}, err
	}
	if operation.State == "complete" {
		if err := tx.Commit(); err != nil {
			return TeamDeletionStatus{}, err
		}
		return deletionStatusFromOperation(operation), nil
	}
	if operation.State != "leased" || operation.LeaseExpiresAt == nil ||
		!operation.LeaseExpiresAt.After(s.clock().UTC()) ||
		!teamDeletionLeaseTokenMatches(operation.LeaseTokenHash, request.LeaseToken) {
		return TeamDeletionStatus{}, ErrConcealedNotFound
	}
	var rootReady int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM team_object_registry
		 WHERE object_id = ? AND store_id = ? AND team_id = ?
		   AND generation = ? AND lifecycle = 'cleaning'`,
		operation.RootObjectID, operation.StoreID, operation.TeamID,
		operation.RootGeneration+1).Scan(&rootReady); err != nil {
		return TeamDeletionStatus{}, err
	}
	if rootReady != 1 {
		return TeamDeletionStatus{}, ErrConcealedNotFound
	}
	now := s.clock().UTC()
	if err := cleanupTeamDeletionFixedPoint(ctx, tx, operation, now); err != nil {
		return TeamDeletionStatus{}, err
	}
	if err := verifyTeamDeletionCompletionBarrier(ctx, tx, operation.RootObjectID); err != nil {
		return TeamDeletionStatus{}, err
	}
	nowText := now.Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
		UPDATE team_object_registry SET lifecycle = 'complete', updated_at = ?
		 WHERE object_id = ? AND generation = ? AND lifecycle = 'cleaning'`,
		nowText, operation.RootObjectID, operation.RootGeneration+1)
	if err != nil {
		return TeamDeletionStatus{}, err
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
		return TeamDeletionStatus{}, ErrTeamDeletionBarrier
	}
	if err := dischargeTeamDeletionRoot(ctx, tx, operation, now); err != nil {
		return TeamDeletionStatus{}, err
	}
	var targetKind, projectID string
	var startPolicyVersion int
	var authorizationEpoch int64
	if err := tx.QueryRowContext(ctx, `
		SELECT target_kind, COALESCE(project_id, ''), policy_version, auth_epoch
		  FROM team_audit_events WHERE event_id = ?`, operation.StartAuditEventID).
		Scan(&targetKind, &projectID, &startPolicyVersion, &authorizationEpoch); err != nil {
		return TeamDeletionStatus{}, err
	}
	completionAuditID, err := appendTeamDomainAudit(ctx, tx, teamDomainAuditEvent{
		StoreID: operation.StoreID, TeamID: operation.TeamID,
		ProjectID:      projectID,
		ActorPrincipal: operation.ActorPrincipalID, OAuthClientKey: operation.OAuthClientKey,
		RequestID: operation.RequestID, Action: teamObjectDeleteCompleteAction,
		TargetKind: targetKind, TargetID: operation.RootObjectID,
		PolicyVersion: startPolicyVersion, AuthorizationAt: authorizationEpoch,
		ReasonCode: teamObjectDeleteCompleteReason, OccurredAt: now,
	})
	if err != nil {
		return TeamDeletionStatus{}, err
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE team_deletion_operations
		   SET state = 'complete', lease_token_hash = NULL, lease_expires_at = NULL,
		       next_attempt_at = NULL, last_error_code = NULL,
		       completion_audit_event_id = ?, completed_at = ?, updated_at = ?
		 WHERE operation_id = ? AND state = 'leased' AND lease_token_hash = ?`,
		completionAuditID, nowText, nowText, operation.OperationID, operation.LeaseTokenHash)
	if err != nil {
		return TeamDeletionStatus{}, err
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
		return TeamDeletionStatus{}, ErrTeamDeletionBarrier
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.WriterID, request.WriterToken); err != nil {
		return TeamDeletionStatus{}, err
	}
	completed, err := loadTeamDeletionOperation(ctx, tx, operation.OperationID)
	if err != nil {
		return TeamDeletionStatus{}, err
	}
	if err := tx.Commit(); err != nil {
		return TeamDeletionStatus{}, err
	}
	return deletionStatusFromOperation(completed), nil
}

func normalizeTeamDeletionStart(request TeamDeletionStartRequest) (normalizedTeamDeletionStart, error) {
	authorization, err := normalizeTeamMutationRequest(request.Authorization)
	if err != nil || authorization.Action != teamauth.ActionDelete ||
		authorization.ExistingObjectID == "" || authorization.RequestedScope != nil ||
		!validTeamOpaque(request.Writer.WriterID, 1, 255) ||
		!validTeamOpaque(request.Writer.Token, 1, 255) ||
		!validTeamOpaque(request.RequestID, 8, 64) ||
		!validRawTeamIdempotencyKey(request.IdempotencyKey) {
		return normalizedTeamDeletionStart{}, ErrTeamDeletionInvalid
	}
	keyHash := sha256.Sum256([]byte("pulse-team-deletion-idempotency-v1\x00" + request.IdempotencyKey))
	return normalizedTeamDeletionStart{
		authorization: authorization, writer: request.Writer, requestID: request.RequestID,
		idempotencyHash: hex.EncodeToString(keyHash[:]), bodyDigest: teamDeletionBodyDigest(authorization),
	}, nil
}

func teamDeletionBodyDigest(authorization TeamMutationAuthorizationRequest) string {
	fields := []string{
		"pulse-team-deletion-operation-v1", string(authorization.Action),
		authorization.PrincipalID, authorization.OAuthClientKey,
		authorization.Context.TeamID, authorization.Context.ProjectID,
		authorization.Context.RepoID, authorization.Context.AgentID,
		authorization.Context.SessionID, authorization.ExistingObjectID,
	}
	digest := sha256.New()
	var size [8]byte
	for _, field := range fields {
		binary.BigEndian.PutUint64(size[:], uint64(len(field)))
		_, _ = digest.Write(size[:])
		_, _ = digest.Write([]byte(field))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func canonicalizeTeamDeletionAuthorization(
	principal teamDeletionPrincipalAuthorization,
	authorization TeamMutationAuthorizationRequest,
) (TeamMutationAuthorizationRequest, error) {
	if principal.actor.Kind != teamauth.PrincipalAgent {
		return authorization, nil
	}
	if principal.principal.BindingID == "" ||
		(authorization.Context.AgentID != "" &&
			authorization.Context.AgentID != principal.principal.BindingID) {
		return TeamMutationAuthorizationRequest{}, ErrConcealedNotFound
	}
	authorization.Context.AgentID = principal.principal.BindingID
	return authorization, nil
}

// recheckTeamDeletionOperationAccess opens a fresh snapshot after the mutation
// transaction has committed. This is the response boundary: a binding or exact
// project grant revoked between durable work and the caller response must
// suppress that response even though the deletion operation remains durable.
func (s *Store) recheckTeamDeletionOperationAccess(
	ctx context.Context,
	authorization TeamMutationAuthorizationRequest,
	operationID string,
	expectedBodyDigest string,
	beforeFinalEpochRecheck func(),
) (teamDeletionOperation, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return teamDeletionOperation{}, err
	}
	defer tx.Rollback()
	principal, err := authorizeTeamDeletionPrincipal(ctx, tx, authorization, authorization.Action)
	if err != nil {
		return teamDeletionOperation{}, concealTeamDeletionAuthorization(err)
	}
	authorization, err = canonicalizeTeamDeletionAuthorization(principal, authorization)
	if err != nil {
		return teamDeletionOperation{}, concealTeamDeletionAuthorization(err)
	}
	operation, err := loadTeamDeletionOperation(ctx, tx, operationID)
	if err != nil {
		return teamDeletionOperation{}, concealTeamDeletionAuthorization(err)
	}
	if operation.StoreID != principal.info.StoreID || operation.TeamID != principal.info.TeamID {
		return teamDeletionOperation{}, ErrConcealedNotFound
	}
	authorization.ExistingObjectID = operation.RootObjectID
	if _, err := authorizeTeamDeletionObject(ctx, tx, principal, authorization,
		operation.RootObjectID, true); err != nil {
		return teamDeletionOperation{}, concealTeamDeletionAuthorization(err)
	}
	if err := tx.Commit(); err != nil {
		return teamDeletionOperation{}, err
	}
	if beforeFinalEpochRecheck != nil {
		beforeFinalEpochRecheck()
	}
	if err := s.recheckTeamDeletionPrincipalSnapshot(ctx, principal, authorization.OAuthClientKey); err != nil {
		return teamDeletionOperation{}, concealTeamDeletionAuthorization(err)
	}
	if err := s.RecheckTeamPolicyEpoch(ctx, principal.actor.AuthorizedEpoch); err != nil {
		return teamDeletionOperation{}, concealTeamDeletionAuthorization(err)
	}
	if expectedBodyDigest != "" &&
		subtle.ConstantTimeCompare([]byte(operation.BodyDigest), []byte(expectedBodyDigest)) != 1 {
		return teamDeletionOperation{}, ErrTeamIdempotencyConflict
	}
	return operation, nil
}

func (s *Store) recheckTeamDeletionPrincipalSnapshot(
	ctx context.Context,
	expected teamDeletionPrincipalAuthorization,
	oauthClientKey string,
) error {
	info, err := readTeamStoreInfo(ctx, s.db)
	if err != nil {
		return err
	}
	current, err := resolveTeamPrincipal(ctx, s.db, info, expected.principal.PrincipalID)
	if err != nil {
		if errors.Is(err, ErrPrincipalRevoked) {
			return ErrTeamPolicyEpochChanged
		}
		return err
	}
	clientMatches, err := teamMutationClientMatches(ctx, s.db, current, oauthClientKey)
	if err != nil {
		return err
	}
	if info.StoreID != expected.info.StoreID || info.TeamID != expected.info.TeamID ||
		info.AuthEpoch != expected.actor.AuthorizedEpoch.Global ||
		current.StoreID != expected.principal.StoreID || current.TeamID != expected.principal.TeamID ||
		current.PrincipalID != expected.principal.PrincipalID || current.Kind != expected.principal.Kind ||
		current.HumanPrincipalID != expected.principal.HumanPrincipalID ||
		current.BindingID != expected.principal.BindingID ||
		current.MembershipID != expected.principal.MembershipID ||
		current.MembershipRole != expected.principal.MembershipRole ||
		current.PrincipalEpoch != expected.actor.AuthorizedEpoch.Principal ||
		current.MembershipEpoch != expected.actor.AuthorizedEpoch.Membership ||
		current.BindingEpoch != expected.actor.AuthorizedEpoch.Binding || !clientMatches {
		return ErrTeamPolicyEpochChanged
	}
	return nil
}

func authorizeTeamDeletionPrincipal(
	ctx context.Context,
	q queryer,
	request TeamMutationAuthorizationRequest,
	action teamauth.Action,
) (teamDeletionPrincipalAuthorization, error) {
	var result teamDeletionPrincipalAuthorization
	info, err := readTeamStoreInfo(ctx, q)
	if err != nil {
		return result, err
	}
	policy, err := readTeamPolicyState(ctx, q)
	if err != nil {
		return result, err
	}
	if info.StoreID != policy.StoreID || info.TeamID != policy.TeamID || info.AuthEpoch != policy.GlobalEpoch ||
		request.Context.TeamID != info.TeamID {
		return result, ErrTeamPolicyNotReady
	}
	principal, err := resolveTeamPrincipal(ctx, q, info, request.PrincipalID)
	if err != nil {
		return result, err
	}
	clientMatches, err := teamMutationClientMatches(ctx, q, principal, request.OAuthClientKey)
	if err != nil {
		return result, err
	}
	if !clientMatches {
		return result, ErrPrincipalRevoked
	}
	epoch := teamauth.EpochSnapshot{
		Global: info.AuthEpoch, Policy: policy.PolicyEpoch,
		Principal: principal.PrincipalEpoch, Membership: principal.MembershipEpoch,
		Binding: principal.BindingEpoch,
	}
	actor := teamauth.Actor{
		TeamID: info.TeamID, PrincipalID: principal.PrincipalID,
		HumanPrincipalID: principal.HumanPrincipalID,
		Kind:             teamauth.PrincipalKind(principal.Kind), Role: teamauth.Role(principal.MembershipRole),
		PrincipalActive:  principal.PrincipalStatus == "active",
		MembershipActive: principal.MembershipStatus == "active",
		BindingActive:    principal.Kind != string(teamauth.PrincipalAgent) || principal.BindingStatus == "active",
		AuthorizedEpoch:  epoch, CurrentEpoch: epoch,
	}
	if decision := teamauth.ValidatePrincipal(action, request.Capabilities, actor); !decision.Allowed {
		return result, teamMutationDenied(decision.Reason)
	}
	return teamDeletionPrincipalAuthorization{
		info: info, principal: principal, actor: actor,
	}, nil
}

func authorizeTeamDeletionObject(
	ctx context.Context,
	q queryer,
	principal teamDeletionPrincipalAuthorization,
	request TeamMutationAuthorizationRequest,
	objectID string,
	ignoreLifecycle bool,
) (TeamObject, error) {
	object, err := loadTeamMutationObject(ctx, q, objectID)
	if err != nil {
		return TeamObject{}, err
	}
	if object.StoreID != principal.info.StoreID || object.TeamID != principal.info.TeamID {
		return TeamObject{}, ErrConcealedNotFound
	}
	target := object.Scope
	if ignoreLifecycle {
		target.Lifecycle = teamauth.LifecycleActive
	} else if target.Lifecycle != teamauth.LifecycleActive {
		return TeamObject{}, ErrConcealedNotFound
	}
	grants, err := loadTeamMutationGrantFacts(ctx, q, principal.actor, target,
		object.ObjectKind, request.Action)
	if err != nil {
		return TeamObject{}, err
	}
	decision := teamauth.Authorize(teamauth.AuthorizationRequest{
		Action: request.Action, Capabilities: request.Capabilities,
		Actor: principal.actor, Context: request.Context, Target: &target,
		TargetAuthoritative: true, Grants: grants,
	})
	if !decision.Allowed {
		return TeamObject{}, ErrConcealedNotFound
	}
	return object, nil
}

func loadTeamDeletionReplay(
	ctx context.Context,
	tx *sql.Tx,
	principal teamDeletionPrincipalAuthorization,
	request normalizedTeamDeletionStart,
) (teamDeletionOperation, bool, error) {
	operation, err := loadTeamDeletionOperationByIdempotency(ctx, tx, principal.info.TeamID,
		principal.principal.PrincipalID, request.authorization.OAuthClientKey, request.idempotencyHash)
	if errors.Is(err, sql.ErrNoRows) {
		return teamDeletionOperation{}, false, nil
	}
	if err != nil {
		return teamDeletionOperation{}, false, err
	}
	return operation, true, nil
}

func captureTeamDeletionFrontier(
	ctx context.Context,
	tx *sql.Tx,
	operationID, rootID string,
	rootGeneration int64,
	now time.Time,
) error {
	_, err := tx.ExecContext(ctx, `
		WITH RECURSIVE reachable(object_id, depth) AS (
			SELECT ?, 0
			UNION
				SELECT contribution.derivative_object_id, reachable.depth + 1
				  FROM reachable
				  JOIN team_object_contributions contribution
				    ON contribution.parent_object_id = reachable.object_id
			), shallowest(object_id, depth) AS (
			SELECT object_id, MIN(depth) FROM reachable GROUP BY object_id
		)
		INSERT INTO team_deletion_frontier(
			operation_id, object_id, object_generation, depth, discovered_at)
		SELECT ?, object.object_id,
		       CASE WHEN object.object_id = ? THEN ? ELSE object.generation END,
		       shallowest.depth, ?
		  FROM shallowest
		  JOIN team_object_registry object ON object.object_id = shallowest.object_id
		 ORDER BY shallowest.depth, object.object_id`,
		rootID, operationID, rootID, rootGeneration,
		now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return nil
}

func loadTeamDeletionOperation(ctx context.Context, q queryer, operationID string) (teamDeletionOperation, error) {
	return scanTeamDeletionOperation(q.QueryRowContext(ctx, teamDeletionOperationSelect+`
		 WHERE operation_id = ?`, operationID))
}

func loadTeamDeletionOperationByIdempotency(
	ctx context.Context,
	q queryer,
	teamID, principalID, clientKey, idempotencyHash string,
) (teamDeletionOperation, error) {
	return scanTeamDeletionOperation(q.QueryRowContext(ctx, teamDeletionOperationSelect+`
		 WHERE team_id = ? AND actor_principal_id = ? AND oauth_client_key = ?
		   AND idempotency_key_hash = ?`, teamID, principalID, clientKey, idempotencyHash))
}

const teamDeletionOperationSelect = `
	SELECT operation_id, store_id, team_id, root_object_id, root_generation,
	       actor_principal_id, oauth_client_key, request_id, body_digest,
		       start_audit_event_id, state, attempt_count,
		       COALESCE(lease_token_hash, ''), lease_expires_at, next_attempt_at,
		       COALESCE(last_error_code, ''), started_at, updated_at, completed_at
	  FROM team_deletion_operations`

func scanTeamDeletionOperation(row teamObjectScanner) (teamDeletionOperation, error) {
	var operation teamDeletionOperation
	var leaseExpires, nextAttempt, completed sql.NullString
	var started, updated string
	if err := row.Scan(
		&operation.OperationID, &operation.StoreID, &operation.TeamID,
		&operation.RootObjectID, &operation.RootGeneration,
		&operation.ActorPrincipalID, &operation.OAuthClientKey, &operation.RequestID,
		&operation.BodyDigest, &operation.StartAuditEventID, &operation.State,
		&operation.AttemptCount, &operation.LeaseTokenHash, &leaseExpires, &nextAttempt,
		&operation.LastErrorCode, &started, &updated, &completed,
	); err != nil {
		return teamDeletionOperation{}, err
	}
	var err error
	if operation.StartedAt, err = time.Parse(time.RFC3339Nano, started); err != nil {
		return teamDeletionOperation{}, err
	}
	if operation.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return teamDeletionOperation{}, err
	}
	if leaseExpires.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, leaseExpires.String)
		if parseErr != nil {
			return teamDeletionOperation{}, parseErr
		}
		operation.LeaseExpiresAt = &parsed
	}
	if nextAttempt.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, nextAttempt.String)
		if parseErr != nil {
			return teamDeletionOperation{}, parseErr
		}
		operation.NextAttemptAt = &parsed
	}
	if completed.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, completed.String)
		if parseErr != nil {
			return teamDeletionOperation{}, parseErr
		}
		operation.CompletedAt = &parsed
	}
	return operation, nil
}

func deletionStatusFromOperation(operation teamDeletionOperation) TeamDeletionStatus {
	return TeamDeletionStatus{
		OperationID: operation.OperationID, ObjectID: operation.RootObjectID,
		AuditEventID: operation.StartAuditEventID, Status: deletionStatusValue(operation.State),
		AttemptCount: operation.AttemptCount, LastErrorCode: operation.LastErrorCode,
		NextAttemptAt: operation.NextAttemptAt,
		StartedAt:     operation.StartedAt, UpdatedAt: operation.UpdatedAt,
		CompletedAt: operation.CompletedAt,
	}
}

func deletionStatusValue(state string) string {
	switch state {
	case "complete":
		return TeamDeletionStatusComplete
	case "cleanup_failed":
		return TeamDeletionStatusCleanupFailed
	default:
		return TeamDeletionStatusInProgress
	}
}

func deletionStartResultStatus(state string) string {
	if state == "complete" {
		return TeamDeletionStatusComplete
	}
	return TeamDeletionStatusInProgress
}

func cleanupTeamDeletionFixedPoint(
	ctx context.Context,
	tx *sql.Tx,
	operation teamDeletionOperation,
	now time.Time,
) error {
	if err := purgeTeamObjectPayload(ctx, tx, operation.RootObjectID); err != nil {
		return err
	}
	for {
		var objectID string
		err := tx.QueryRowContext(ctx, `
			SELECT frontier.object_id
			  FROM team_deletion_frontier frontier
			  JOIN team_object_registry object ON object.object_id = frontier.object_id
			 WHERE frontier.operation_id = ? AND frontier.depth > 0
			   AND object.lifecycle = 'active'
			   AND NOT EXISTS (
			       SELECT 1
			         FROM team_object_contributions contribution
			         JOIN team_object_registry parent
			           ON parent.object_id = contribution.parent_object_id
			          AND parent.team_id = contribution.team_id
			          AND parent.scope_type = contribution.scope_type
			          AND parent.scope_id = contribution.scope_id
			          AND parent.generation = contribution.parent_generation
			        WHERE contribution.derivative_object_id = frontier.object_id
			          AND parent.lifecycle = 'active'
			          AND (parent.expires_at IS NULL OR julianday(parent.expires_at) > julianday(?))
			   )
			 ORDER BY frontier.depth, frontier.object_id LIMIT 1`,
			operation.OperationID, now.UTC().Format(time.RFC3339Nano)).Scan(&objectID)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return err
		}
		nowText := now.UTC().Format(time.RFC3339Nano)
		tombstone, err := tx.ExecContext(ctx, `
			UPDATE team_object_registry
			   SET lifecycle = 'tombstoned', generation = generation + 1, updated_at = ?
			 WHERE object_id = ? AND lifecycle = 'active'`, nowText, objectID)
		if err != nil {
			return err
		}
		if affected, affectedErr := tombstone.RowsAffected(); affectedErr != nil || affected != 1 {
			return ErrTeamDeletionBarrier
		}
		cleaning, err := tx.ExecContext(ctx, `
			UPDATE team_object_registry SET lifecycle = 'cleaning', updated_at = ?
			 WHERE object_id = ? AND lifecycle = 'tombstoned'`, nowText, objectID)
		if err != nil {
			return err
		}
		if affected, affectedErr := cleaning.RowsAffected(); affectedErr != nil || affected != 1 {
			return ErrTeamDeletionBarrier
		}
		if err := purgeTeamObjectPayload(ctx, tx, objectID); err != nil {
			return err
		}
		// Once no active parent supports an object, one cleaner owns the
		// physical purge for every operation that discovered it. Durable purged
		// discharges are written for every matching frontier before those
		// transient rows are removed, so concurrent root deletions converge
		// without losing completion evidence.
		if err := dischargePurgedTeamDeletionObject(ctx, tx, objectID, now); err != nil {
			return err
		}
		purged, err := tx.ExecContext(ctx, `DELETE FROM team_object_registry WHERE object_id = ?`, objectID)
		if err != nil {
			return err
		}
		if affected, affectedErr := purged.RowsAffected(); affectedErr != nil || affected != 1 {
			return ErrTeamDeletionBarrier
		}
	}
	return dischargePreservedTeamDeletionDescendants(ctx, tx, operation.OperationID, now)
}

func dischargePurgedTeamDeletionObject(
	ctx context.Context,
	tx *sql.Tx,
	objectID string,
	now time.Time,
) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_deletion_discharges(
			operation_id, object_id, object_generation, depth, disposition, discharged_at)
		SELECT frontier.operation_id, frontier.object_id, frontier.object_generation,
		       frontier.depth, 'purged', ?
		  FROM team_deletion_frontier frontier
		 WHERE frontier.object_id = ?
		   AND NOT EXISTS (
		       SELECT 1 FROM team_deletion_discharges discharge
		        WHERE discharge.operation_id = frontier.operation_id
		          AND discharge.object_id = frontier.object_id
		   )`, now.UTC().Format(time.RFC3339Nano), objectID); err != nil {
		return err
	}
	var unresolved int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*)
		  FROM team_deletion_frontier frontier
		 WHERE frontier.object_id = ?
		   AND NOT EXISTS (
		       SELECT 1 FROM team_deletion_discharges discharge
		        WHERE discharge.operation_id = frontier.operation_id
		          AND discharge.object_id = frontier.object_id
		          AND discharge.object_generation = frontier.object_generation
		          AND discharge.depth = frontier.depth
		          AND discharge.disposition = 'purged'
		   )`, objectID).Scan(&unresolved); err != nil {
		return err
	}
	if unresolved != 0 {
		return ErrTeamDeletionBarrier
	}
	_, err := tx.ExecContext(ctx, `
		DELETE FROM team_deletion_frontier WHERE object_id = ?`, objectID)
	return err
}

func dischargePreservedTeamDeletionDescendants(
	ctx context.Context,
	tx *sql.Tx,
	operationID string,
	now time.Time,
) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_deletion_discharges(
			operation_id, object_id, object_generation, depth, disposition, discharged_at)
		SELECT frontier.operation_id, frontier.object_id, frontier.object_generation,
		       frontier.depth, 'preserved', ?
		  FROM team_deletion_frontier frontier
		 WHERE frontier.operation_id = ? AND frontier.depth > 0
		   AND NOT EXISTS (
		       SELECT 1 FROM team_deletion_discharges discharge
		        WHERE discharge.operation_id = frontier.operation_id
		          AND discharge.object_id = frontier.object_id
		   )
		 ORDER BY frontier.depth, frontier.object_id`,
		now.UTC().Format(time.RFC3339Nano), operationID); err != nil {
		return err
	}
	var unresolved int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*)
		  FROM team_deletion_frontier frontier
		 WHERE frontier.operation_id = ? AND frontier.depth > 0
		   AND NOT EXISTS (
		       SELECT 1 FROM team_deletion_discharges discharge
		        WHERE discharge.operation_id = frontier.operation_id
		          AND discharge.object_id = frontier.object_id
		          AND discharge.object_generation = frontier.object_generation
		          AND discharge.depth = frontier.depth
		          AND discharge.disposition = 'preserved'
		   )`, operationID).Scan(&unresolved); err != nil {
		return err
	}
	if unresolved != 0 {
		return ErrTeamDeletionBarrier
	}
	_, err := tx.ExecContext(ctx, `
		DELETE FROM team_deletion_frontier
		 WHERE operation_id = ? AND depth > 0`, operationID)
	return err
}

func dischargeTeamDeletionRoot(
	ctx context.Context,
	tx *sql.Tx,
	operation teamDeletionOperation,
	now time.Time,
) error {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO team_deletion_discharges(
			operation_id, object_id, object_generation, depth, disposition, discharged_at)
		SELECT operation_id, object_id, object_generation, depth, 'purged', ?
		  FROM team_deletion_frontier
		 WHERE operation_id = ? AND object_id = ? AND object_generation = ? AND depth = 0`,
		now.UTC().Format(time.RFC3339Nano), operation.OperationID,
		operation.RootObjectID, operation.RootGeneration)
	if err != nil {
		return err
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
		return ErrTeamDeletionBarrier
	}
	result, err = tx.ExecContext(ctx, `
		DELETE FROM team_deletion_frontier
		 WHERE operation_id = ? AND object_id = ? AND depth = 0`,
		operation.OperationID, operation.RootObjectID)
	if err != nil {
		return err
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
		return ErrTeamDeletionBarrier
	}
	return nil
}

func purgeTeamObjectPayload(ctx context.Context, tx *sql.Tx, objectID string) error {
	statements := []string{
		`DELETE FROM team_semantic_embeddings WHERE intent_id IN (
			SELECT intent_id FROM team_semantic_materializations
			 WHERE root_object_id = ? OR derivative_object_id = ?)`,
		`DELETE FROM team_graph_materializations WHERE intent_id IN (
			SELECT intent_id FROM team_semantic_materializations
			 WHERE root_object_id = ? OR derivative_object_id = ?)`,
		`DELETE FROM team_assertion_materializations WHERE intent_id IN (
			SELECT intent_id FROM team_semantic_materializations
			 WHERE root_object_id = ? OR derivative_object_id = ?)`,
		`DELETE FROM team_continuity_materializations WHERE intent_id IN (
			SELECT intent_id FROM team_semantic_materializations
			 WHERE root_object_id = ? OR derivative_object_id = ?)`,
		`DELETE FROM team_memory_embeddings WHERE root_object_id = ? OR derivative_object_id = ?`,
		`DELETE FROM team_memory_events WHERE root_object_id = ? OR derivative_object_id = ?`,
		`DELETE FROM team_projection_outputs WHERE derivative_object_id = ? OR job_id IN (
			SELECT job_id FROM team_projection_jobs WHERE root_object_id = ?)`,
		`DELETE FROM team_semantic_materializations WHERE root_object_id = ? OR derivative_object_id = ?`,
		`DELETE FROM team_semantic_projection_intents WHERE root_object_id = ? OR derivative_object_id = ?`,
		`DELETE FROM team_graph_delta_inputs WHERE root_object_id = ?`,
		`DELETE FROM team_memory_capsules WHERE root_object_id = ?`,
		`DELETE FROM team_object_storage_map WHERE object_id = ?`,
		`DELETE FROM team_projection_jobs WHERE root_object_id = ?`,
		`DELETE FROM team_object_contributions WHERE parent_object_id = ? OR derivative_object_id = ?`,
	}
	for _, statement := range statements {
		count := strings.Count(statement, "?")
		args := make([]any, count)
		for index := range args {
			args[index] = objectID
		}
		if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
			return err
		}
	}
	return nil
}

func verifyTeamDeletionCompletionBarrier(ctx context.Context, tx *sql.Tx, rootID string) error {
	queries := []string{
		`SELECT count(*) FROM team_memory_capsules WHERE root_object_id = ?`,
		`SELECT count(*) FROM team_memory_events WHERE root_object_id = ? OR derivative_object_id = ?`,
		`SELECT count(*) FROM team_memory_embeddings WHERE root_object_id = ? OR derivative_object_id = ?`,
		`SELECT count(*) FROM team_graph_delta_inputs WHERE root_object_id = ?`,
		`SELECT count(*) FROM team_semantic_projection_intents WHERE root_object_id = ? OR derivative_object_id = ?`,
		`SELECT count(*) FROM team_semantic_materializations WHERE root_object_id = ? OR derivative_object_id = ?`,
		`SELECT count(*) FROM team_projection_jobs WHERE root_object_id = ?`,
		`SELECT count(*) FROM team_projection_outputs WHERE derivative_object_id = ?`,
		`SELECT count(*) FROM team_object_storage_map WHERE object_id = ?`,
		`SELECT count(*) FROM team_object_contributions WHERE parent_object_id = ? OR derivative_object_id = ?`,
	}
	for _, query := range queries {
		count := strings.Count(query, "?")
		args := make([]any, count)
		for index := range args {
			args[index] = rootID
		}
		var remaining int
		if err := tx.QueryRowContext(ctx, query, args...).Scan(&remaining); err != nil {
			return err
		}
		if remaining != 0 {
			return ErrTeamDeletionBarrier
		}
	}
	return nil
}

func validTeamDeletionStatusRequest(request TeamDeletionStatusRequest) bool {
	if !exactMutationValue(request.PrincipalID, 255) ||
		(request.OAuthClientKey != "" && !lowerHexDigest(request.OAuthClientKey)) ||
		!validMutationCapabilities(request.Capabilities) ||
		!validMutationContext(request.Context) || !validTeamOpaque(request.OperationID, 1, 255) {
		return false
	}
	return true
}

func validTeamDeletionClaimRequest(request TeamDeletionClaimRequest) bool {
	return validTeamOpaque(request.WriterID, 1, 255) && validTeamOpaque(request.WriterToken, 1, 255) &&
		request.Limit > 0 && request.Limit <= maxTeamDeletionClaimBatch &&
		request.LeaseTTL > 0 && request.LeaseTTL <= maxTeamDeletionLeaseTTL
}

func validTeamDeletionFailureRequest(request TeamDeletionFailureRequest) bool {
	return validTeamOpaque(request.WriterID, 1, 255) && validTeamOpaque(request.WriterToken, 1, 255) &&
		validTeamOpaque(request.OperationID, 1, 255) && validTeamOpaque(request.LeaseToken, 1, 255) &&
		validTeamDeletionFailureCode(request.ErrorCode) && request.Backoff > 0 && request.Backoff <= maxTeamDeletionBackoff
}

func validTeamDeletionReapRequest(request TeamDeletionReapRequest) bool {
	return validTeamOpaque(request.WriterID, 1, 255) && validTeamOpaque(request.WriterToken, 1, 255) &&
		request.Limit > 0 && request.Limit <= maxTeamDeletionClaimBatch
}

func validTeamDeletionCompletionRequest(request TeamDeletionCompletionRequest) bool {
	return validTeamOpaque(request.WriterID, 1, 255) && validTeamOpaque(request.WriterToken, 1, 255) &&
		validTeamOpaque(request.OperationID, 1, 255) && validTeamOpaque(request.LeaseToken, 1, 255)
}

func validTeamDeletionFailureCode(value string) bool {
	switch value {
	case TeamDeletionFailureTemporary, TeamDeletionFailureStorageUnavailable,
		TeamDeletionFailureWorkerInterrupted:
		return true
	default:
		return false
	}
}

func concealTeamDeletionAuthorization(err error) error {
	if errors.Is(err, ErrTeamPolicyDenied) || errors.Is(err, ErrPrincipalRevoked) ||
		errors.Is(err, ErrConcealedNotFound) || errors.Is(err, sql.ErrNoRows) ||
		errors.Is(err, ErrTeamPolicyEpochChanged) {
		return ErrConcealedNotFound
	}
	return err
}

func newTeamDeletionLeaseToken() (string, error) {
	material := make([]byte, 32)
	if _, err := rand.Read(material); err != nil {
		return "", err
	}
	return "deletion_lease_" + hex.EncodeToString(material), nil
}

func teamDeletionLeaseTokenHash(token string) string {
	digest := sha256.Sum256([]byte("pulse-team-deletion-lease-v1\x00" + token))
	return hex.EncodeToString(digest[:])
}

func teamDeletionLeaseTokenMatches(storedHash, token string) bool {
	expected := teamDeletionLeaseTokenHash(token)
	return len(storedHash) == len(expected) &&
		subtle.ConstantTimeCompare([]byte(storedHash), []byte(expected)) == 1
}
