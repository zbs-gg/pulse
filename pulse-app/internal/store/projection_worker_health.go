package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var (
	ErrTeamProjectionWorkerHeartbeatMissing = errors.New("team projection worker heartbeat is missing")
	ErrTeamProjectionWorkerHeartbeatStale   = errors.New("team projection worker heartbeat is stale")
	ErrTeamProjectionWorkerLeaseMismatch    = errors.New("team projection worker is not bound to the current writer lease")
	ErrTeamProjectionDependencyUnavailable  = errors.New("team projection dependency is unavailable")
	ErrTeamProjectionWorkerCycleFailed      = errors.New("team projection worker cycle failed")
)

const (
	teamProjectionWorkerStaleAfter   = 30 * time.Second
	teamProjectionWorkerMissingAfter = 60 * time.Second

	TeamProjectionDependencyReady    = "ready"
	TeamProjectionDependencyDegraded = "degraded"

	TeamProjectionWorkerErrorCycleFailed = "projection_cycle_failed"

	TeamProjectionWorkerStateReady    = "ready"
	TeamProjectionWorkerStateDegraded = "degraded"
	TeamProjectionWorkerStateStale    = "stale"
	TeamProjectionWorkerStateMissing  = "missing"

	TeamProjectionWorkerReasonHeartbeatMissing       = "worker_heartbeat_missing"
	TeamProjectionWorkerReasonHeartbeatStale         = "worker_heartbeat_stale"
	TeamProjectionWorkerReasonHeartbeatInvalid       = "worker_heartbeat_invalid"
	TeamProjectionWorkerReasonLeaseMismatch          = "worker_lease_mismatch"
	TeamProjectionWorkerReasonEmbeddingNotConfigured = "embedding_dependency_not_configured"
	TeamProjectionWorkerReasonCycleFailed            = "projection_cycle_failed"
)

type TeamProjectionWorkerHeartbeatRequest struct {
	WriterID         string
	WriterToken      string
	WorkerInstanceID string
	DependencyState  string
	DependencyReason string
	LastErrorCode    string
}

type TeamProjectionWorkerHealth struct {
	State            string
	Reason           string
	WriterID         string
	WorkerInstanceID string
	DependencyState  string
	DependencyReason string
	LastErrorCode    string
	StartedAt        time.Time
	HeartbeatAt      *time.Time
}

// RecordTeamProjectionWorkerHeartbeat persists only operational classifications
// and opaque runtime IDs. The exact active daemon writer lease is rechecked in
// the same transaction, so a restarted or partitioned process cannot keep a
// stale worker looking healthy.
func (s *Store) RecordTeamProjectionWorkerHeartbeat(
	ctx context.Context,
	request TeamProjectionWorkerHeartbeatRequest,
) error {
	if !validTeamProjectionWorkerHeartbeat(request) {
		return ErrInvalidProjectionJobRequest
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.WriterID, request.WriterToken); err != nil {
		return err
	}
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := readTeamPolicyState(ctx, tx); err != nil {
		return err
	}
	nowText := s.clock().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_worker_heartbeats(
			store_id, team_id, worker_kind, writer_id, worker_instance_id,
			dependency_state, dependency_reason, last_error_code, started_at, heartbeat_at
		) VALUES (?, ?, 'projection', ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(store_id, worker_kind) DO UPDATE SET
			writer_id = excluded.writer_id,
			worker_instance_id = excluded.worker_instance_id,
			dependency_state = excluded.dependency_state,
			dependency_reason = excluded.dependency_reason,
			last_error_code = excluded.last_error_code,
			started_at = CASE
				WHEN team_worker_heartbeats.writer_id <> excluded.writer_id
				  OR team_worker_heartbeats.worker_instance_id <> excluded.worker_instance_id
				THEN excluded.started_at
				ELSE team_worker_heartbeats.started_at
			END,
			heartbeat_at = excluded.heartbeat_at`,
		info.StoreID, info.TeamID, request.WriterID, request.WorkerInstanceID,
		request.DependencyState, request.DependencyReason, request.LastErrorCode,
		nowText, nowText); err != nil {
		return err
	}
	return tx.Commit()
}

// ReadTeamProjectionWorkerHealth derives readiness from the durable heartbeat,
// the current writer-lease holder, and the worker's explicit dependency state.
// It never reads projection content or exposes lease tokens.
func (s *Store) ReadTeamProjectionWorkerHealth(ctx context.Context) (TeamProjectionWorkerHealth, error) {
	info, err := readTeamStoreInfo(ctx, s.db)
	if err != nil {
		return TeamProjectionWorkerHealth{}, err
	}
	var health TeamProjectionWorkerHealth
	var startedText, heartbeatText string
	err = s.db.QueryRowContext(ctx, `
		SELECT writer_id, worker_instance_id, dependency_state, dependency_reason,
		       last_error_code, started_at, heartbeat_at
		  FROM team_worker_heartbeats
		 WHERE store_id = ? AND team_id = ? AND worker_kind = 'projection'`,
		info.StoreID, info.TeamID).Scan(
		&health.WriterID, &health.WorkerInstanceID, &health.DependencyState,
		&health.DependencyReason, &health.LastErrorCode, &startedText, &heartbeatText,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return TeamProjectionWorkerHealth{
			State: TeamProjectionWorkerStateMissing, Reason: TeamProjectionWorkerReasonHeartbeatMissing,
		}, nil
	}
	if err != nil {
		return TeamProjectionWorkerHealth{}, err
	}
	health.StartedAt, err = time.Parse(time.RFC3339Nano, startedText)
	if err != nil {
		health.State = TeamProjectionWorkerStateDegraded
		health.Reason = TeamProjectionWorkerReasonHeartbeatInvalid
		return health, nil
	}
	heartbeatAt, err := time.Parse(time.RFC3339Nano, heartbeatText)
	if err != nil {
		health.State = TeamProjectionWorkerStateDegraded
		health.Reason = TeamProjectionWorkerReasonHeartbeatInvalid
		return health, nil
	}
	health.HeartbeatAt = &heartbeatAt
	now := s.clock().UTC()
	age := now.Sub(heartbeatAt)
	if age < -5*time.Second {
		health.State = TeamProjectionWorkerStateDegraded
		health.Reason = TeamProjectionWorkerReasonHeartbeatInvalid
		return health, nil
	}
	if age >= teamProjectionWorkerMissingAfter {
		health.State = TeamProjectionWorkerStateMissing
		health.Reason = TeamProjectionWorkerReasonHeartbeatMissing
		return health, nil
	}
	if age >= teamProjectionWorkerStaleAfter {
		health.State = TeamProjectionWorkerStateStale
		health.Reason = TeamProjectionWorkerReasonHeartbeatStale
		return health, nil
	}
	var leaseWriterID, leaseExpiresText string
	err = s.db.QueryRowContext(ctx, `
		SELECT writer_id, expires_at
		  FROM team_writer_leases
		 WHERE store_id = ? AND team_id = ? AND runtime_mode = 'team-remote'`,
		info.StoreID, info.TeamID).Scan(&leaseWriterID, &leaseExpiresText)
	if errors.Is(err, sql.ErrNoRows) {
		health.State = TeamProjectionWorkerStateDegraded
		health.Reason = TeamProjectionWorkerReasonLeaseMismatch
		return health, nil
	}
	if err != nil {
		return TeamProjectionWorkerHealth{}, err
	}
	leaseExpires, parseErr := time.Parse(time.RFC3339Nano, leaseExpiresText)
	if parseErr != nil || leaseWriterID != health.WriterID || !leaseExpires.After(now) {
		health.State = TeamProjectionWorkerStateDegraded
		health.Reason = TeamProjectionWorkerReasonLeaseMismatch
		return health, nil
	}
	if health.DependencyState != TeamProjectionDependencyReady {
		health.State = TeamProjectionWorkerStateDegraded
		health.Reason = health.DependencyReason
		return health, nil
	}
	if health.LastErrorCode != "" {
		health.State = TeamProjectionWorkerStateDegraded
		health.Reason = TeamProjectionWorkerReasonCycleFailed
		return health, nil
	}
	health.State = TeamProjectionWorkerStateReady
	return health, nil
}

func projectionWorkerHealthError(health TeamProjectionWorkerHealth) error {
	switch health.Reason {
	case "":
		return nil
	case TeamProjectionWorkerReasonHeartbeatMissing:
		return ErrTeamProjectionWorkerHeartbeatMissing
	case TeamProjectionWorkerReasonHeartbeatStale, TeamProjectionWorkerReasonHeartbeatInvalid:
		return ErrTeamProjectionWorkerHeartbeatStale
	case TeamProjectionWorkerReasonLeaseMismatch:
		return ErrTeamProjectionWorkerLeaseMismatch
	case TeamProjectionWorkerReasonEmbeddingNotConfigured:
		return ErrTeamProjectionDependencyUnavailable
	case TeamProjectionWorkerReasonCycleFailed:
		return ErrTeamProjectionWorkerCycleFailed
	default:
		return ErrTeamProjectionDependencyUnavailable
	}
}

func validTeamProjectionWorkerHeartbeat(request TeamProjectionWorkerHeartbeatRequest) bool {
	if !validProjectionOpaque(request.WriterID, 255) || request.WriterToken == "" ||
		!validProjectionOpaque(request.WorkerInstanceID, 255) {
		return false
	}
	if request.LastErrorCode != "" && request.LastErrorCode != TeamProjectionWorkerErrorCycleFailed {
		return false
	}
	switch request.DependencyState {
	case TeamProjectionDependencyReady:
		return request.DependencyReason == ""
	case TeamProjectionDependencyDegraded:
		return request.DependencyReason == TeamProjectionWorkerReasonEmbeddingNotConfigured
	default:
		return false
	}
}
