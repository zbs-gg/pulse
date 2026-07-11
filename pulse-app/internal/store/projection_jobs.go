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
	"hash"
	"sort"
	"strings"
	"time"
)

var (
	ErrInvalidProjectionJobRequest     = errors.New("invalid projection job request")
	ErrProjectionMaterializationFailed = errors.New("projection materialization failed")
)

const (
	maxProjectionClaimBatch = 64
	maxProjectionLeaseTTL   = 5 * time.Minute
	maxProjectionBackoff    = 24 * time.Hour
	// A maximum graph delta contains 30 nodes, 50 edges, 50 facts, and
	// 20 events. Graph materialization emits one output and mapping per source.
	maxProjectionOutputs  = 150
	maxProjectionMappings = 150

	maxProjectionVectorDimensions      = 4096
	maxProjectionAggregateVectorValues = maxProjectionOutputs * maxProjectionVectorDimensions
)

const (
	TeamProjectionFailureDependencyTimeout     = "dependency_timeout"
	TeamProjectionFailureDependencyUnavailable = "dependency_unavailable"
	TeamProjectionFailureRateLimited           = "rate_limited"
	TeamProjectionFailureStorageUnavailable    = "storage_unavailable"
	TeamProjectionFailureWorkerInterrupted     = "worker_interrupted"
	TeamProjectionFailureMaterialization       = "materialization_failed"
	TeamProjectionFailureTemporary             = "temporary_failure"

	TeamProjectionCancellationRootTombstoned       = "root_tombstoned"
	TeamProjectionCancellationRootDeleted          = "root_deleted"
	TeamProjectionCancellationGenerationSuperseded = "generation_superseded"
)

type TeamProjectionClaimRequest struct {
	WriterID       string
	WriterToken    string
	ProjectionKind string
	Limit          int
	LeaseTTL       time.Duration
}

type TeamProjectionJobClaim struct {
	JobID          string
	StoreID        string
	TeamID         string
	RootObjectID   string
	RootGeneration int64
	ScopeType      string
	ScopeID        string
	ProjectionKind string
	AttemptCount   int
	LeaseToken     string
	LeaseExpiresAt time.Time
}

type TeamProjectionFailureRequest struct {
	WriterID    string
	WriterToken string
	JobID       string
	LeaseToken  string
	ErrorCode   string
	Backoff     time.Duration
}

type TeamProjectionStorageMapping struct {
	RepresentationKind string
	StorageKey         string
}

type TeamProjectionOutput struct {
	DerivativeObjectID   string
	DerivativeGeneration int64
	ObjectKind           string
	StorageMappings      []TeamProjectionStorageMapping
}

type TeamProjectionCompletionRequest struct {
	WriterID    string
	WriterToken string
	JobID       string
	LeaseToken  string
	Outputs     []TeamProjectionOutput
}

type TeamProjectionCompletionResult struct {
	JobID           string
	State           string
	AlreadyReady    bool
	OutputObjectIDs []string
}

// teamProjectionContentWriter intentionally omits Commit, Rollback, raw
// transaction access, and schema/connection operations. Domain projectors can
// materialize content inside the leased completion transaction, but only the
// projection spine owns the terminal ready transition.
type teamProjectionContentWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	InsertTeamMemoryEvent(context.Context, teamMemoryEventMaterialization) error
	InsertTeamMemoryEmbedding(context.Context, teamMemoryEmbeddingMaterialization) error
	InsertTeamSemanticMaterializations(context.Context, []teamSemanticMaterialization) error
	InsertTeamSemanticEmbedding(context.Context, teamSemanticEmbeddingMaterialization) error
	InsertTeamGraphMaterialization(context.Context, teamGraphMaterialization) error
	InsertTeamAssertionMaterialization(context.Context, teamAssertionMaterialization) error
	InsertTeamContinuityMaterialization(context.Context, teamContinuityMaterialization) error
}

type teamProjectionCompletionContext struct {
	JobID          string
	RootObjectID   string
	RootGeneration int64
	ScopeType      string
	ScopeID        string
	ProjectionKind string
	Outputs        []TeamProjectionOutput
}

type teamProjectionCompletionExtension func(
	context.Context,
	teamProjectionContentWriter,
	teamProjectionCompletionContext,
) error

type restrictedProjectionContentWriter struct {
	execContext                         func(context.Context, string, ...any) (sql.Result, error)
	queryRowContext                     func(context.Context, string, ...any) *sql.Row
	insertTeamMemoryEvent               func(context.Context, teamMemoryEventMaterialization) error
	insertTeamMemoryEmbedding           func(context.Context, teamMemoryEmbeddingMaterialization) error
	insertTeamSemanticMaterializations  func(context.Context, []teamSemanticMaterialization) error
	insertTeamSemanticEmbedding         func(context.Context, teamSemanticEmbeddingMaterialization) error
	insertTeamGraphMaterialization      func(context.Context, teamGraphMaterialization) error
	insertTeamAssertionMaterialization  func(context.Context, teamAssertionMaterialization) error
	insertTeamContinuityMaterialization func(context.Context, teamContinuityMaterialization) error
}

func (writer *restrictedProjectionContentWriter) ExecContext(ctx context.Context, statement string, args ...any) (sql.Result, error) {
	return writer.execContext(ctx, statement, args...)
}

func (writer *restrictedProjectionContentWriter) QueryRowContext(ctx context.Context, statement string, args ...any) *sql.Row {
	return writer.queryRowContext(ctx, statement, args...)
}

func (writer *restrictedProjectionContentWriter) InsertTeamMemoryEvent(
	ctx context.Context,
	materialization teamMemoryEventMaterialization,
) error {
	return writer.insertTeamMemoryEvent(ctx, materialization)
}

func (writer *restrictedProjectionContentWriter) InsertTeamMemoryEmbedding(
	ctx context.Context,
	materialization teamMemoryEmbeddingMaterialization,
) error {
	return writer.insertTeamMemoryEmbedding(ctx, materialization)
}

func (writer *restrictedProjectionContentWriter) InsertTeamSemanticMaterializations(
	ctx context.Context,
	materializations []teamSemanticMaterialization,
) error {
	return writer.insertTeamSemanticMaterializations(ctx, materializations)
}

func (writer *restrictedProjectionContentWriter) InsertTeamSemanticEmbedding(
	ctx context.Context,
	materialization teamSemanticEmbeddingMaterialization,
) error {
	return writer.insertTeamSemanticEmbedding(ctx, materialization)
}

func (writer *restrictedProjectionContentWriter) InsertTeamGraphMaterialization(
	ctx context.Context,
	materialization teamGraphMaterialization,
) error {
	return writer.insertTeamGraphMaterialization(ctx, materialization)
}

func (writer *restrictedProjectionContentWriter) InsertTeamAssertionMaterialization(
	ctx context.Context,
	materialization teamAssertionMaterialization,
) error {
	return writer.insertTeamAssertionMaterialization(ctx, materialization)
}

func (writer *restrictedProjectionContentWriter) InsertTeamContinuityMaterialization(
	ctx context.Context,
	materialization teamContinuityMaterialization,
) error {
	return writer.insertTeamContinuityMaterialization(ctx, materialization)
}

type TeamProjectionCancellationRequest struct {
	WriterID       string
	WriterToken    string
	RootObjectID   string
	RootGeneration int64
	ReasonCode     string
}

func (s *Store) ClaimTeamProjectionJobs(ctx context.Context, request TeamProjectionClaimRequest) ([]TeamProjectionJobClaim, error) {
	if !validProjectionClaimRequest(request) {
		return nil, ErrInvalidProjectionJobRequest
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
		SELECT job.job_id, job.attempt_count
		  FROM team_projection_jobs job
		 WHERE job.store_id = ? AND job.team_id = ?
		   AND (? = '' OR job.projection_kind = ?)
		   AND (
		        (job.state IN ('pending', 'failed')
		         AND julianday(job.next_attempt_at) <= julianday(?))
		     OR (job.state = 'leased'
		         AND julianday(job.lease_expires_at) <= julianday(?))
		   )
		   AND EXISTS (
		       SELECT 1 FROM team_object_registry root
		        WHERE root.object_id = job.root_object_id
		          AND root.store_id = job.store_id AND root.team_id = job.team_id
		          AND root.scope_type = job.scope_type AND root.scope_id = job.scope_id
		          AND root.generation = job.root_generation AND root.lifecycle = 'active'
		          AND (root.expires_at IS NULL OR julianday(root.expires_at) > julianday(?))
		   )
		 ORDER BY julianday(COALESCE(job.next_attempt_at, job.lease_expires_at)),
		          julianday(job.created_at), job.job_id
		 LIMIT ?`, info.StoreID, info.TeamID, request.ProjectionKind, request.ProjectionKind,
		nowText, nowText, nowText, request.Limit)
	if err != nil {
		return nil, err
	}
	type claimCandidate struct {
		jobID        string
		attemptCount int
	}
	var candidates []claimCandidate
	for rows.Next() {
		var candidate claimCandidate
		if err := rows.Scan(&candidate.jobID, &candidate.attemptCount); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	claims := make([]TeamProjectionJobClaim, 0, len(candidates))
	for _, candidate := range candidates {
		token, err := newProjectionLeaseToken()
		if err != nil {
			return nil, err
		}
		expires := now.Add(request.LeaseTTL)
		var claim TeamProjectionJobClaim
		err = tx.QueryRowContext(ctx, `
			UPDATE team_projection_jobs
			   SET state = 'leased', attempt_count = attempt_count + 1,
			       lease_token_hash = ?, lease_expires_at = ?, next_attempt_at = NULL,
			       terminal_lease_token_hash = NULL, completion_digest = NULL,
			       last_error_code = NULL, updated_at = ?
			 WHERE job_id = ? AND store_id = ? AND team_id = ?
			   AND (
			        (state IN ('pending', 'failed') AND julianday(next_attempt_at) <= julianday(?))
			     OR (state = 'leased' AND julianday(lease_expires_at) <= julianday(?))
			   )
			   AND EXISTS (
			       SELECT 1 FROM team_object_registry root
			        WHERE root.object_id = team_projection_jobs.root_object_id
			          AND root.store_id = team_projection_jobs.store_id
			          AND root.team_id = team_projection_jobs.team_id
			          AND root.scope_type = team_projection_jobs.scope_type
			          AND root.scope_id = team_projection_jobs.scope_id
			          AND root.generation = team_projection_jobs.root_generation
			          AND root.lifecycle = 'active'
			          AND (root.expires_at IS NULL OR julianday(root.expires_at) > julianday(?))
			   )
			 RETURNING job_id, store_id, team_id, root_object_id, root_generation,
			           scope_type, scope_id, projection_kind, attempt_count`,
			projectionLeaseTokenHash(token), expires.Format(time.RFC3339Nano), nowText,
			candidate.jobID, info.StoreID, info.TeamID, nowText, nowText, nowText).Scan(
			&claim.JobID, &claim.StoreID, &claim.TeamID, &claim.RootObjectID,
			&claim.RootGeneration, &claim.ScopeType, &claim.ScopeID,
			&claim.ProjectionKind, &claim.AttemptCount)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		claim.LeaseToken = token
		claim.LeaseExpiresAt = expires
		claims = append(claims, claim)
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.WriterID, request.WriterToken); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claims, nil
}

func (s *Store) FailTeamProjectionJob(ctx context.Context, request TeamProjectionFailureRequest) error {
	if !validProjectionFailureRequest(request) {
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
	now := s.clock().UTC()
	job, err := loadProjectionFailureJob(ctx, tx, info, request.JobID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrConcealedNotFound
	}
	if err != nil {
		return err
	}
	if job.State == "failed" && job.LastErrorCode == request.ErrorCode &&
		projectionLeaseTokenMatches(job.TerminalLeaseTokenHash, request.LeaseToken) {
		return nil
	}
	if job.State != "leased" || job.RootLifecycle != "active" ||
		job.RootCurrentGeneration != job.RootGeneration || !job.LeaseExpiresAt.After(now) ||
		!projectionLeaseTokenMatches(job.LeaseTokenHash, request.LeaseToken) {
		return ErrConcealedNotFound
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE team_projection_jobs
		   SET state = 'failed', terminal_lease_token_hash = lease_token_hash,
		       completion_digest = NULL, lease_token_hash = NULL, lease_expires_at = NULL,
		       next_attempt_at = ?, last_error_code = ?, updated_at = ?
		 WHERE job_id = ? AND store_id = ? AND team_id = ?
		   AND state = 'leased' AND lease_token_hash = ?`,
		now.Add(request.Backoff).Format(time.RFC3339Nano), request.ErrorCode,
		now.Format(time.RFC3339Nano), request.JobID, info.StoreID, info.TeamID, job.LeaseTokenHash)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrConcealedNotFound
	}
	return tx.Commit()
}

func (s *Store) CompleteTeamProjectionJob(ctx context.Context, request TeamProjectionCompletionRequest) (TeamProjectionCompletionResult, error) {
	return s.completeTeamProjectionJobWithExtension(ctx, request, nil)
}

func (s *Store) completeTeamProjectionJobWithExtension(
	ctx context.Context,
	request TeamProjectionCompletionRequest,
	extension teamProjectionCompletionExtension,
) (TeamProjectionCompletionResult, error) {
	if !validProjectionCompletionRequest(request) {
		return TeamProjectionCompletionResult{}, ErrInvalidProjectionJobRequest
	}
	if len(request.Outputs) != 0 && extension == nil {
		return TeamProjectionCompletionResult{}, ErrInvalidProjectionJobRequest
	}
	completionDigest := projectionCompletionDigest(request.Outputs)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	defer tx.Rollback()
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.WriterID, request.WriterToken); err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	if _, err := readTeamPolicyState(ctx, tx); err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	job, err := loadProjectionCompletionJob(ctx, tx, info, request.JobID)
	if errors.Is(err, sql.ErrNoRows) {
		return TeamProjectionCompletionResult{}, ErrConcealedNotFound
	}
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	outputIDs := projectionOutputIDs(request.Outputs)
	now := s.clock().UTC()
	if job.State == "ready" {
		if !projectionRootActiveAt(job, now) ||
			!projectionLeaseTokenMatches(job.TerminalLeaseTokenHash, request.LeaseToken) ||
			!projectionDigestMatches(job.CompletionDigest, completionDigest) {
			return TeamProjectionCompletionResult{}, ErrConcealedNotFound
		}
		matches, err := readyProjectionMatches(ctx, tx, job, request.Outputs)
		if err != nil {
			return TeamProjectionCompletionResult{}, err
		}
		if !matches {
			return TeamProjectionCompletionResult{}, ErrConcealedNotFound
		}
		if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.WriterID, request.WriterToken); err != nil {
			return TeamProjectionCompletionResult{}, err
		}
		if !projectionRootActiveAt(job, s.clock().UTC()) {
			return TeamProjectionCompletionResult{}, ErrConcealedNotFound
		}
		return TeamProjectionCompletionResult{
			JobID: job.JobID, State: "ready", AlreadyReady: true, OutputObjectIDs: outputIDs,
		}, nil
	}
	if job.State != "leased" || !projectionRootActiveAt(job, now) ||
		!job.LeaseExpiresAt.After(now) || !projectionLeaseTokenMatches(job.LeaseTokenHash, request.LeaseToken) {
		return TeamProjectionCompletionResult{}, ErrConcealedNotFound
	}
	for _, output := range request.Outputs {
		if output.DerivativeObjectID == job.RootObjectID {
			return TeamProjectionCompletionResult{}, ErrInvalidProjectionJobRequest
		}
		if err := ensureProjectionDerivative(ctx, tx, job, output, now); err != nil {
			return TeamProjectionCompletionResult{}, err
		}
		if err := attachProjectionOutput(ctx, tx, job, output, now); err != nil {
			return TeamProjectionCompletionResult{}, err
		}
	}
	if extension != nil {
		completion := teamProjectionCompletionContext{
			JobID: job.JobID, RootObjectID: job.RootObjectID,
			RootGeneration: job.RootGeneration, ScopeType: job.ScopeType,
			ScopeID: job.ScopeID, ProjectionKind: job.ProjectionKind,
			Outputs: cloneProjectionOutputs(request.Outputs),
		}
		successfulMutations := 0
		writer := &restrictedProjectionContentWriter{
			execContext: func(ctx context.Context, statement string, args ...any) (sql.Result, error) {
				if !validProjectionMaterializationMutation(statement) {
					return nil, ErrProjectionMaterializationFailed
				}
				result, err := tx.ExecContext(ctx, statement, args...)
				if err == nil {
					successfulMutations++
				}
				return result, err
			},
			queryRowContext: func(ctx context.Context, statement string, args ...any) *sql.Row {
				if !validProjectionMaterializationQuery(statement) {
					return tx.QueryRowContext(ctx, `SELECT 1 WHERE 0`)
				}
				return tx.QueryRowContext(ctx, statement, args...)
			},
			insertTeamMemoryEvent: func(ctx context.Context, materialization teamMemoryEventMaterialization) error {
				err := insertTeamMemoryEventTx(ctx, tx, job, materialization, now)
				if err == nil {
					successfulMutations++
				}
				return err
			},
			insertTeamMemoryEmbedding: func(ctx context.Context, materialization teamMemoryEmbeddingMaterialization) error {
				err := insertTeamMemoryEmbeddingTx(ctx, tx, job, materialization, now)
				if err == nil {
					successfulMutations++
				}
				return err
			},
			insertTeamSemanticMaterializations: func(
				ctx context.Context,
				materializations []teamSemanticMaterialization,
			) error {
				err := insertTeamSemanticMaterializationsTx(ctx, tx, job, materializations, now)
				if err == nil {
					successfulMutations++
				}
				return err
			},
			insertTeamSemanticEmbedding: func(
				ctx context.Context,
				materialization teamSemanticEmbeddingMaterialization,
			) error {
				err := insertTeamSemanticEmbeddingTx(ctx, tx, job, materialization, now)
				if err == nil {
					successfulMutations++
				}
				return err
			},
			insertTeamGraphMaterialization: func(
				ctx context.Context,
				materialization teamGraphMaterialization,
			) error {
				err := insertTeamGraphMaterializationTx(ctx, tx, job, materialization, now)
				if err == nil {
					successfulMutations++
				}
				return err
			},
			insertTeamAssertionMaterialization: func(
				ctx context.Context,
				materialization teamAssertionMaterialization,
			) error {
				err := insertTeamAssertionMaterializationTx(ctx, tx, job, materialization, now)
				if err == nil {
					successfulMutations++
				}
				return err
			},
			insertTeamContinuityMaterialization: func(
				ctx context.Context,
				materialization teamContinuityMaterialization,
			) error {
				err := insertTeamContinuityMaterializationTx(ctx, tx, job, materialization, now)
				if err == nil {
					successfulMutations++
				}
				return err
			},
		}
		if err := extension(ctx, writer, completion); err != nil ||
			(len(request.Outputs) != 0 && successfulMutations == 0) {
			return TeamProjectionCompletionResult{}, ErrProjectionMaterializationFailed
		}
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.WriterID, request.WriterToken); err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	now = s.clock().UTC()
	result, err := tx.ExecContext(ctx, `
		UPDATE team_projection_jobs
		   SET state = 'ready', terminal_lease_token_hash = lease_token_hash,
		       completion_digest = ?, lease_token_hash = NULL, lease_expires_at = NULL,
		       next_attempt_at = NULL, last_error_code = NULL, updated_at = ?
		 WHERE job_id = ? AND store_id = ? AND team_id = ?
		   AND state = 'leased' AND lease_token_hash = ?
		   AND julianday(lease_expires_at) > julianday(?)
		   AND EXISTS (
		       SELECT 1 FROM team_object_registry root
		        WHERE root.object_id = team_projection_jobs.root_object_id
		          AND root.store_id = team_projection_jobs.store_id
		          AND root.team_id = team_projection_jobs.team_id
		          AND root.scope_type = team_projection_jobs.scope_type
		          AND root.scope_id = team_projection_jobs.scope_id
			          AND root.generation = team_projection_jobs.root_generation
			          AND root.lifecycle = 'active'
			          AND (root.expires_at IS NULL OR julianday(root.expires_at) > julianday(?))
			   )`, completionDigest, now.Format(time.RFC3339Nano), job.JobID, info.StoreID, info.TeamID,
		job.LeaseTokenHash, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	if changed != 1 {
		return TeamProjectionCompletionResult{}, ErrConcealedNotFound
	}
	if err := tx.Commit(); err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	return TeamProjectionCompletionResult{
		JobID: job.JobID, State: "ready", OutputObjectIDs: outputIDs,
	}, nil
}

func (s *Store) CancelTeamProjectionJobsTx(ctx context.Context, tx *sql.Tx, request TeamProjectionCancellationRequest) (int64, error) {
	if tx == nil || !validProjectionCancellationRequest(request) {
		return 0, ErrInvalidProjectionJobRequest
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.WriterID, request.WriterToken); err != nil {
		return 0, err
	}
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return 0, err
	}
	var lifecycle string
	var currentGeneration int64
	if err := tx.QueryRowContext(ctx, `
		SELECT lifecycle, generation
		  FROM team_object_registry
		 WHERE object_id = ? AND store_id = ? AND team_id = ?`,
		request.RootObjectID, info.StoreID, info.TeamID).Scan(&lifecycle, &currentGeneration); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrConcealedNotFound
		}
		return 0, err
	}
	if lifecycle != "tombstoned" || currentGeneration != request.RootGeneration+1 {
		return 0, ErrConcealedNotFound
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE team_projection_jobs
		   SET state = 'cancelled', lease_token_hash = NULL, lease_expires_at = NULL,
		       terminal_lease_token_hash = NULL, completion_digest = NULL,
		       next_attempt_at = NULL, last_error_code = ?, updated_at = ?
		 WHERE store_id = ? AND team_id = ? AND root_object_id = ? AND root_generation = ?
		   AND state IN ('pending', 'failed', 'leased')`,
		request.ReasonCode, s.clock().UTC().Format(time.RFC3339Nano), info.StoreID, info.TeamID,
		request.RootObjectID, request.RootGeneration)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

type projectionCompletionJob struct {
	JobID                  string
	StoreID                string
	TeamID                 string
	RootObjectID           string
	RootGeneration         int64
	ScopeType              string
	ScopeID                string
	ProjectionKind         string
	State                  string
	AttemptCount           int
	LeaseTokenHash         string
	TerminalLeaseTokenHash string
	CompletionDigest       string
	LeaseExpiresAt         time.Time
	RootLifecycle          string
	RootCurrentGeneration  int64
	RootOwnerID            sql.NullString
	RootAuthorID           string
	RootPrivacyTier        string
	RootRetention          string
	RootExpiresAt          sql.NullString
}

type projectionFailureJob struct {
	State                  string
	AttemptCount           int
	LeaseTokenHash         string
	TerminalLeaseTokenHash string
	LeaseExpiresAt         time.Time
	LastErrorCode          string
	RootGeneration         int64
	RootLifecycle          string
	RootCurrentGeneration  int64
}

func loadProjectionFailureJob(ctx context.Context, tx *sql.Tx, info TeamStoreInfo, jobID string) (projectionFailureJob, error) {
	var job projectionFailureJob
	var hash, terminalHash, expiry, errorCode sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT job.state, job.attempt_count, job.lease_token_hash,
		       job.terminal_lease_token_hash, job.lease_expires_at,
		       job.last_error_code, job.root_generation, root.lifecycle, root.generation
		  FROM team_projection_jobs job
		  JOIN team_object_registry root ON root.object_id = job.root_object_id
		 WHERE job.job_id = ? AND job.store_id = ? AND job.team_id = ?`,
		jobID, info.StoreID, info.TeamID).Scan(
		&job.State, &job.AttemptCount, &hash, &terminalHash, &expiry, &errorCode,
		&job.RootGeneration, &job.RootLifecycle, &job.RootCurrentGeneration)
	if err != nil {
		return projectionFailureJob{}, err
	}
	job.LeaseTokenHash = hash.String
	job.TerminalLeaseTokenHash = terminalHash.String
	job.LastErrorCode = errorCode.String
	if expiry.Valid {
		job.LeaseExpiresAt, err = time.Parse(time.RFC3339Nano, expiry.String)
		if err != nil {
			return projectionFailureJob{}, ErrConcealedNotFound
		}
	}
	return job, nil
}

func loadProjectionCompletionJob(ctx context.Context, tx *sql.Tx, info TeamStoreInfo, jobID string) (projectionCompletionJob, error) {
	var job projectionCompletionJob
	var leaseHash, terminalHash, completionDigest, leaseExpiry sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT job.job_id, job.store_id, job.team_id, job.root_object_id,
		       job.root_generation, job.scope_type, job.scope_id, job.projection_kind,
		       job.state, job.attempt_count, job.lease_token_hash,
		       job.terminal_lease_token_hash, job.completion_digest, job.lease_expires_at,
		       root.lifecycle, root.generation, root.owner_principal_id,
		       root.author_principal_id, root.privacy_tier, root.retention, root.expires_at
		  FROM team_projection_jobs job
		  JOIN team_object_registry root ON root.object_id = job.root_object_id
		 WHERE job.job_id = ? AND job.store_id = ? AND job.team_id = ?`,
		jobID, info.StoreID, info.TeamID).Scan(
		&job.JobID, &job.StoreID, &job.TeamID, &job.RootObjectID,
		&job.RootGeneration, &job.ScopeType, &job.ScopeID, &job.ProjectionKind,
		&job.State, &job.AttemptCount, &leaseHash, &terminalHash, &completionDigest, &leaseExpiry,
		&job.RootLifecycle, &job.RootCurrentGeneration,
		&job.RootOwnerID, &job.RootAuthorID, &job.RootPrivacyTier, &job.RootRetention, &job.RootExpiresAt)
	if err != nil {
		return projectionCompletionJob{}, err
	}
	job.LeaseTokenHash = leaseHash.String
	job.TerminalLeaseTokenHash = terminalHash.String
	job.CompletionDigest = completionDigest.String
	if leaseExpiry.Valid {
		job.LeaseExpiresAt, err = time.Parse(time.RFC3339Nano, leaseExpiry.String)
		if err != nil {
			return projectionCompletionJob{}, ErrConcealedNotFound
		}
	}
	return job, nil
}

func ensureProjectionDerivative(ctx context.Context, tx *sql.Tx, job projectionCompletionJob, output TeamProjectionOutput, now time.Time) error {
	var storeID, teamID, objectKind, scopeType, scopeID, lifecycle, privacy, retention, expires string
	var generation int64
	err := tx.QueryRowContext(ctx, `
		SELECT store_id, team_id, object_kind, scope_type, scope_id, lifecycle,
		       generation, privacy_tier, retention, COALESCE(expires_at, '')
		  FROM team_object_registry WHERE object_id = ?`, output.DerivativeObjectID).Scan(
		&storeID, &teamID, &objectKind, &scopeType, &scopeID, &lifecycle,
		&generation, &privacy, &retention, &expires)
	if err == nil {
		if storeID != job.StoreID || teamID != job.TeamID || objectKind != output.ObjectKind ||
			scopeType != job.ScopeType || scopeID != job.ScopeID || lifecycle != "active" ||
			generation != output.DerivativeGeneration || privacy != job.RootPrivacyTier ||
			retention != job.RootRetention || expires != job.RootExpiresAt.String {
			return ErrConcealedNotFound
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if output.DerivativeGeneration != 1 {
		return ErrInvalidProjectionJobRequest
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO team_object_registry(
			object_id, store_id, team_id, object_kind, scope_type, scope_id,
			owner_principal_id, author_principal_id, privacy_tier, retention,
			lifecycle, generation, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', 1, ?, ?, ?)`,
		output.DerivativeObjectID, job.StoreID, job.TeamID, output.ObjectKind,
		job.ScopeType, job.ScopeID, nullableProjectionString(job.RootOwnerID), job.RootAuthorID,
		job.RootPrivacyTier, job.RootRetention, nullableProjectionString(job.RootExpiresAt),
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	return err
}

func attachProjectionOutput(ctx context.Context, tx *sql.Tx, job projectionCompletionJob, output TeamProjectionOutput, now time.Time) error {
	nowText := now.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_projection_outputs(job_id, derivative_object_id, derivative_generation, created_at)
		VALUES (?, ?, ?, ?)`, job.JobID, output.DerivativeObjectID, output.DerivativeGeneration, nowText); err != nil {
		return err
	}
	var parentGeneration, derivativeGeneration int64
	var teamID, scopeType, scopeID string
	err := tx.QueryRowContext(ctx, `
		SELECT parent_generation, derivative_generation, team_id, scope_type, scope_id
		  FROM team_object_contributions
		 WHERE parent_object_id = ? AND derivative_object_id = ?`,
		job.RootObjectID, output.DerivativeObjectID).Scan(
		&parentGeneration, &derivativeGeneration, &teamID, &scopeType, &scopeID)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO team_object_contributions(
				parent_object_id, derivative_object_id, team_id, scope_type, scope_id,
				parent_generation, derivative_generation, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			job.RootObjectID, output.DerivativeObjectID, job.TeamID, job.ScopeType, job.ScopeID,
			job.RootGeneration, output.DerivativeGeneration, nowText)
	} else if err == nil && (parentGeneration != job.RootGeneration || derivativeGeneration != output.DerivativeGeneration ||
		teamID != job.TeamID || scopeType != job.ScopeType || scopeID != job.ScopeID) {
		return ErrConcealedNotFound
	}
	if err != nil {
		return err
	}
	for _, mapping := range output.StorageMappings {
		var objectID, existingTeamID, existingScopeType, existingScopeID string
		var generation int64
		err := tx.QueryRowContext(ctx, `
			SELECT object_id, team_id, scope_type, scope_id, generation
			  FROM team_object_storage_map
			 WHERE representation_kind = ? AND storage_key = ?`,
			mapping.RepresentationKind, mapping.StorageKey).Scan(
			&objectID, &existingTeamID, &existingScopeType, &existingScopeID, &generation)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx, `
				INSERT INTO team_object_storage_map(
					object_id, team_id, scope_type, scope_id, generation,
					representation_kind, storage_key, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				output.DerivativeObjectID, job.TeamID, job.ScopeType, job.ScopeID,
				output.DerivativeGeneration, mapping.RepresentationKind, mapping.StorageKey, nowText)
		} else if err == nil && (objectID != output.DerivativeObjectID || existingTeamID != job.TeamID ||
			existingScopeType != job.ScopeType || existingScopeID != job.ScopeID || generation != output.DerivativeGeneration) {
			return ErrConcealedNotFound
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func readyProjectionMatches(ctx context.Context, tx *sql.Tx, job projectionCompletionJob, outputs []TeamProjectionOutput) (bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT output.derivative_object_id, output.derivative_generation,
		       derivative.object_kind, derivative.lifecycle, derivative.generation,
		       derivative.store_id, derivative.team_id, derivative.scope_type, derivative.scope_id
		  FROM team_projection_outputs output
		  JOIN team_object_registry derivative ON derivative.object_id = output.derivative_object_id
		 WHERE output.job_id = ?`, job.JobID)
	if err != nil {
		return false, err
	}
	stored := make(map[string]struct {
		generation int64
		kind       string
		active     bool
	})
	for rows.Next() {
		var id, kind, lifecycle, storeID, teamID, scopeType, scopeID string
		var outputGeneration, currentGeneration int64
		if err := rows.Scan(&id, &outputGeneration, &kind, &lifecycle, &currentGeneration,
			&storeID, &teamID, &scopeType, &scopeID); err != nil {
			rows.Close()
			return false, err
		}
		stored[id] = struct {
			generation int64
			kind       string
			active     bool
		}{
			generation: outputGeneration, kind: kind,
			active: lifecycle == "active" && currentGeneration == outputGeneration &&
				storeID == job.StoreID && teamID == job.TeamID &&
				scopeType == job.ScopeType && scopeID == job.ScopeID,
		}
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	if len(stored) != len(outputs) {
		return false, nil
	}
	for _, output := range outputs {
		got, ok := stored[output.DerivativeObjectID]
		if !ok || !got.active || got.generation != output.DerivativeGeneration || got.kind != output.ObjectKind {
			return false, nil
		}
		for _, mapping := range output.StorageMappings {
			var present int
			if err := tx.QueryRowContext(ctx, `
				SELECT count(*)
				  FROM team_object_storage_map
				 WHERE object_id = ? AND team_id = ? AND scope_type = ? AND scope_id = ?
				   AND generation = ? AND representation_kind = ? AND storage_key = ?`,
				output.DerivativeObjectID, job.TeamID, job.ScopeType, job.ScopeID,
				output.DerivativeGeneration, mapping.RepresentationKind, mapping.StorageKey,
			).Scan(&present); err != nil {
				return false, err
			}
			if present != 1 {
				return false, nil
			}
		}
	}
	return true, nil
}

func projectionRootActiveAt(job projectionCompletionJob, now time.Time) bool {
	if job.RootLifecycle != "active" || job.RootCurrentGeneration != job.RootGeneration {
		return false
	}
	if !job.RootExpiresAt.Valid {
		return true
	}
	expires, err := time.Parse(time.RFC3339Nano, job.RootExpiresAt.String)
	return err == nil && expires.After(now)
}

func validProjectionClaimRequest(request TeamProjectionClaimRequest) bool {
	return request.WriterID != "" && request.WriterToken != "" && request.Limit >= 1 &&
		request.Limit <= maxProjectionClaimBatch && request.LeaseTTL > 0 &&
		request.LeaseTTL <= maxProjectionLeaseTTL &&
		(request.ProjectionKind == "" || validProjectionOpaque(request.ProjectionKind, 64))
}

func validProjectionFailureRequest(request TeamProjectionFailureRequest) bool {
	return request.WriterID != "" && request.WriterToken != "" &&
		validProjectionOpaque(request.JobID, 255) && request.LeaseToken != "" &&
		validProjectionFailureCode(request.ErrorCode) && request.Backoff >= time.Second && request.Backoff <= maxProjectionBackoff
}

func validProjectionCompletionRequest(request TeamProjectionCompletionRequest) bool {
	if request.WriterID == "" || request.WriterToken == "" || !validProjectionOpaque(request.JobID, 255) ||
		request.LeaseToken == "" || len(request.Outputs) > maxProjectionOutputs {
		return false
	}
	seenOutputs := make(map[string]bool, len(request.Outputs))
	seenMappings := make(map[string]bool)
	mappings := 0
	for _, output := range request.Outputs {
		if !validProjectionOpaque(output.DerivativeObjectID, 255) || seenOutputs[output.DerivativeObjectID] ||
			output.DerivativeGeneration < 1 || !validProjectionOpaque(output.ObjectKind, 64) {
			return false
		}
		seenOutputs[output.DerivativeObjectID] = true
		mappings += len(output.StorageMappings)
		if mappings > maxProjectionMappings {
			return false
		}
		for _, mapping := range output.StorageMappings {
			if !validProjectionOpaque(mapping.RepresentationKind, 64) ||
				!validProjectionOpaque(mapping.StorageKey, 255) {
				return false
			}
			key := mapping.RepresentationKind + "\x00" + mapping.StorageKey
			if seenMappings[key] {
				return false
			}
			seenMappings[key] = true
		}
	}
	return true
}

func validProjectionCancellationRequest(request TeamProjectionCancellationRequest) bool {
	return request.WriterID != "" && request.WriterToken != "" &&
		validProjectionOpaque(request.RootObjectID, 255) && request.RootGeneration >= 1 &&
		validProjectionCancellationReason(request.ReasonCode)
}

func validProjectionOpaque(value string, maximum int) bool {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && !strings.ContainsRune("._:-", character) {
			return false
		}
	}
	return true
}

func validProjectionFailureCode(value string) bool {
	switch value {
	case TeamProjectionFailureDependencyTimeout,
		TeamProjectionFailureDependencyUnavailable,
		TeamProjectionFailureRateLimited,
		TeamProjectionFailureStorageUnavailable,
		TeamProjectionFailureWorkerInterrupted,
		TeamProjectionFailureMaterialization,
		TeamProjectionFailureTemporary:
		return true
	default:
		return false
	}
}

func validProjectionCancellationReason(value string) bool {
	switch value {
	case TeamProjectionCancellationRootTombstoned,
		TeamProjectionCancellationRootDeleted,
		TeamProjectionCancellationGenerationSuperseded:
		return true
	default:
		return false
	}
}

func projectionLeaseTokenHash(token string) string {
	digest := sha256.Sum256([]byte("pulse-projection-job-lease-v1\x00" + token))
	return hex.EncodeToString(digest[:])
}

func newProjectionLeaseToken() (string, error) {
	material := make([]byte, 32)
	if _, err := rand.Read(material); err != nil {
		return "", err
	}
	return "projection_lease_" + hex.EncodeToString(material), nil
}

func projectionLeaseTokenMatches(storedHash, token string) bool {
	if storedHash == "" || token == "" {
		return false
	}
	want := projectionLeaseTokenHash(token)
	return subtle.ConstantTimeCompare([]byte(storedHash), []byte(want)) == 1
}

func projectionDigestMatches(expected, actual string) bool {
	if len(expected) != 64 || len(actual) != 64 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

func projectionOutputIDs(outputs []TeamProjectionOutput) []string {
	ids := make([]string, len(outputs))
	for index, output := range outputs {
		ids[index] = output.DerivativeObjectID
	}
	sort.Strings(ids)
	return ids
}

func projectionCompletionDigest(outputs []TeamProjectionOutput) string {
	canonical := cloneProjectionOutputs(outputs)
	for index := range canonical {
		sort.Slice(canonical[index].StorageMappings, func(left, right int) bool {
			if canonical[index].StorageMappings[left].RepresentationKind != canonical[index].StorageMappings[right].RepresentationKind {
				return canonical[index].StorageMappings[left].RepresentationKind < canonical[index].StorageMappings[right].RepresentationKind
			}
			return canonical[index].StorageMappings[left].StorageKey < canonical[index].StorageMappings[right].StorageKey
		})
	}
	sort.Slice(canonical, func(left, right int) bool {
		if canonical[left].DerivativeObjectID != canonical[right].DerivativeObjectID {
			return canonical[left].DerivativeObjectID < canonical[right].DerivativeObjectID
		}
		if canonical[left].DerivativeGeneration != canonical[right].DerivativeGeneration {
			return canonical[left].DerivativeGeneration < canonical[right].DerivativeGeneration
		}
		return canonical[left].ObjectKind < canonical[right].ObjectKind
	})

	digest := sha256.New()
	writeProjectionDigestString(digest, "pulse-projection-completion-v1")
	writeProjectionDigestUint64(digest, uint64(len(canonical)))
	for _, output := range canonical {
		writeProjectionDigestString(digest, output.DerivativeObjectID)
		writeProjectionDigestUint64(digest, uint64(output.DerivativeGeneration))
		writeProjectionDigestString(digest, output.ObjectKind)
		writeProjectionDigestUint64(digest, uint64(len(output.StorageMappings)))
		for _, mapping := range output.StorageMappings {
			writeProjectionDigestString(digest, mapping.RepresentationKind)
			writeProjectionDigestString(digest, mapping.StorageKey)
		}
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func writeProjectionDigestString(digest hash.Hash, value string) {
	writeProjectionDigestUint64(digest, uint64(len(value)))
	_, _ = digest.Write([]byte(value))
}

func writeProjectionDigestUint64(digest hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = digest.Write(encoded[:])
}

func cloneProjectionOutputs(outputs []TeamProjectionOutput) []TeamProjectionOutput {
	cloned := make([]TeamProjectionOutput, len(outputs))
	for index, output := range outputs {
		cloned[index] = output
		cloned[index].StorageMappings = append([]TeamProjectionStorageMapping(nil), output.StorageMappings...)
	}
	return cloned
}

func validProjectionMaterializationMutation(statement string) bool {
	fields, ok := projectionMaterializationStatementFields(statement)
	if !ok || len(fields) < 2 {
		return false
	}
	var table string
	switch strings.ToUpper(fields[0]) {
	case "INSERT":
		for index := 1; index+1 < len(fields); index++ {
			if strings.EqualFold(fields[index], "INTO") {
				table = fields[index+1]
				break
			}
		}
	case "UPDATE":
		table = fields[1]
		if strings.EqualFold(table, "OR") && len(fields) >= 4 {
			table = fields[3]
		}
	case "DELETE":
		if !strings.EqualFold(fields[1], "FROM") || len(fields) < 3 {
			return false
		}
		table = fields[2]
	default:
		return false
	}
	if parenthesis := strings.IndexByte(table, '('); parenthesis >= 0 {
		table = table[:parenthesis]
	}
	if !validProjectionSQLIdentifier(table) {
		return false
	}
	lower := strings.ToLower(table)
	return !strings.HasPrefix(lower, "team_") &&
		!strings.HasPrefix(lower, "schema_") &&
		!strings.HasPrefix(lower, "sqlite_")
}

func validProjectionMaterializationQuery(statement string) bool {
	fields, ok := projectionMaterializationStatementFields(statement)
	return ok && len(fields) != 0 && strings.EqualFold(fields[0], "SELECT")
}

func projectionMaterializationStatementFields(statement string) ([]string, bool) {
	trimmed := strings.TrimSpace(statement)
	if trimmed == "" || strings.Contains(trimmed, ";") ||
		strings.Contains(trimmed, "--") || strings.Contains(trimmed, "/*") || strings.Contains(trimmed, "*/") {
		return nil, false
	}
	fields := strings.Fields(trimmed)
	if projectionStatementReferencesControlTable(fields) {
		return nil, false
	}
	return fields, true
}

func projectionStatementReferencesControlTable(fields []string) bool {
	for _, field := range fields {
		for _, identifier := range strings.FieldsFunc(field, func(character rune) bool {
			return (character < 'a' || character > 'z') &&
				(character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') && character != '_'
		}) {
			lower := strings.ToLower(identifier)
			if strings.HasPrefix(lower, "team_") || strings.HasPrefix(lower, "schema_") ||
				strings.HasPrefix(lower, "sqlite_") {
				return true
			}
		}
	}
	return false
}

func validProjectionSQLIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			character == '_' || (index > 0 && character >= '0' && character <= '9') {
			continue
		}
		return false
	}
	return true
}

func nullableProjectionString(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}
