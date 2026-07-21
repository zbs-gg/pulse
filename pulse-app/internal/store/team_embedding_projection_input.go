package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	TeamEmbeddingProjectionSourceMemory   = "memory"
	TeamEmbeddingProjectionSourceSemantic = "semantic"

	maxTeamEmbeddingDocumentRunes = 8000
)

// TeamEmbeddingProjectionInputRequest carries only the two exact leases that
// authorize a worker to reconstruct content: the daemon writer lease and the
// claimed projection-job lease. No caller-supplied text crosses this boundary.
type TeamEmbeddingProjectionInputRequest struct {
	WriterID    string
	WriterToken string
	JobID       string
	LeaseToken  string
}

type TeamEmbeddingProjectionDocument struct {
	ID   string
	Text string
}

// TeamEmbeddingProjectionInput is a bounded, canonical snapshot of one
// claimed embedding job. Document IDs are capsule IDs for memory roots and
// projection-intent IDs for graph roots. Text is reconstructed from the
// authoritative Team store and is never accepted from the worker caller.
type TeamEmbeddingProjectionInput struct {
	JobID          string
	RootObjectID   string
	RootGeneration int64
	SourceKind     string
	Documents      []TeamEmbeddingProjectionDocument
}

// ReadTeamEmbeddingProjectionInput reconstructs the search-document batch for
// one live embedding lease. Completion performs the same fences again after
// the external embedding dependency returns, so deletion, generation change,
// lease expiry, or writer rotation during that call cannot commit stale output.
func (s *Store) ReadTeamEmbeddingProjectionInput(
	ctx context.Context,
	request TeamEmbeddingProjectionInputRequest,
) (TeamEmbeddingProjectionInput, error) {
	if request.WriterID == "" || request.WriterToken == "" ||
		!validProjectionOpaque(request.JobID, 255) || request.LeaseToken == "" {
		return TeamEmbeddingProjectionInput{}, ErrInvalidProjectionJobRequest
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TeamEmbeddingProjectionInput{}, err
	}
	defer tx.Rollback()
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.WriterID, request.WriterToken); err != nil {
		return TeamEmbeddingProjectionInput{}, err
	}
	info, err := readTeamStoreInfo(ctx, tx)
	if err != nil {
		return TeamEmbeddingProjectionInput{}, err
	}
	if _, err := readTeamPolicyState(ctx, tx); err != nil {
		return TeamEmbeddingProjectionInput{}, err
	}
	job, err := loadProjectionCompletionJob(ctx, tx, info, request.JobID)
	if errors.Is(err, sql.ErrNoRows) {
		return TeamEmbeddingProjectionInput{}, ErrConcealedNotFound
	}
	if err != nil {
		return TeamEmbeddingProjectionInput{}, err
	}
	now := s.clock().UTC()
	if job.ProjectionKind != "embedding" || job.State != "leased" ||
		!projectionRootActiveAt(job, now) || !job.LeaseExpiresAt.After(now) ||
		!projectionLeaseTokenMatches(job.LeaseTokenHash, request.LeaseToken) {
		return TeamEmbeddingProjectionInput{}, ErrConcealedNotFound
	}

	var rootKind string
	if err := tx.QueryRowContext(ctx, `
		SELECT object_kind
		  FROM team_object_registry
		 WHERE object_id = ? AND store_id = ? AND team_id = ?
		   AND scope_type = ? AND scope_id = ? AND generation = ?
		   AND lifecycle = 'active'`,
		job.RootObjectID, job.StoreID, job.TeamID, job.ScopeType, job.ScopeID,
		job.RootGeneration,
	).Scan(&rootKind); errors.Is(err, sql.ErrNoRows) {
		return TeamEmbeddingProjectionInput{}, ErrConcealedNotFound
	} else if err != nil {
		return TeamEmbeddingProjectionInput{}, err
	}

	input := TeamEmbeddingProjectionInput{
		JobID: request.JobID, RootObjectID: job.RootObjectID,
		RootGeneration: job.RootGeneration,
	}
	switch rootKind {
	case "memory":
		input.SourceKind = TeamEmbeddingProjectionSourceMemory
		input.Documents, err = readTeamMemoryEmbeddingDocuments(ctx, tx, job)
	case "graph_delta":
		input.SourceKind = TeamEmbeddingProjectionSourceSemantic
		input.Documents, err = readTeamSemanticEmbeddingDocuments(ctx, tx, job)
	default:
		return TeamEmbeddingProjectionInput{}, ErrConcealedNotFound
	}
	if err != nil {
		return TeamEmbeddingProjectionInput{}, err
	}
	if len(input.Documents) < 1 || len(input.Documents) > maxProjectionOutputs {
		return TeamEmbeddingProjectionInput{}, ErrProjectionMaterializationFailed
	}
	for _, document := range input.Documents {
		if !validProjectionOpaque(document.ID, 255) ||
			strings.TrimSpace(document.Text) == "" ||
			utf8.RuneCountInString(document.Text) > maxTeamEmbeddingDocumentRunes {
			return TeamEmbeddingProjectionInput{}, ErrProjectionMaterializationFailed
		}
	}
	if err := s.RecheckTeamWriterLeaseTx(ctx, tx, request.WriterID, request.WriterToken); err != nil {
		return TeamEmbeddingProjectionInput{}, err
	}
	if err := tx.Commit(); err != nil {
		return TeamEmbeddingProjectionInput{}, err
	}
	return input, nil
}

func readTeamMemoryEmbeddingDocuments(
	ctx context.Context,
	tx *sql.Tx,
	job projectionCompletionJob,
) ([]TeamEmbeddingProjectionDocument, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT capsule_id, redacted_summary
		  FROM team_memory_capsules
		 WHERE root_object_id = ? AND root_generation = ?
		 ORDER BY capsule_id`, job.RootObjectID, job.RootGeneration)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	documents := make([]TeamEmbeddingProjectionDocument, 0)
	for rows.Next() {
		var document TeamEmbeddingProjectionDocument
		if err := rows.Scan(&document.ID, &document.Text); err != nil {
			return nil, err
		}
		documents = append(documents, document)
		if len(documents) > maxProjectionOutputs {
			return nil, ErrProjectionMaterializationFailed
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return documents, nil
}

func readTeamSemanticEmbeddingDocuments(
	ctx context.Context,
	tx *sql.Tx,
	job projectionCompletionJob,
) ([]TeamEmbeddingProjectionDocument, error) {
	source, err := loadTeamSemanticProjectionSourceWithQueryer(ctx, tx, job.JobID, "embedding")
	if err != nil {
		return nil, err
	}
	if source.RootObjectID != job.RootObjectID || source.RootGeneration != job.RootGeneration ||
		source.StoreID != job.StoreID || source.TeamID != job.TeamID ||
		source.ScopeType != job.ScopeType || source.ScopeID != job.ScopeID {
		return nil, ErrConcealedNotFound
	}
	documents := make([]TeamEmbeddingProjectionDocument, 0, len(source.Intents))
	for _, intent := range source.Intents {
		payload, err := teamSemanticProjectionPayload(source, intent)
		if err != nil {
			return nil, err
		}
		documents = append(documents, TeamEmbeddingProjectionDocument{
			ID: intent.IntentID, Text: string(payload),
		})
	}
	return documents, nil
}
