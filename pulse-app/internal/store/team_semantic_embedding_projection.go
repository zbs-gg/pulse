package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"time"
)

type TeamSemanticEmbeddingResult struct {
	IntentID string
	Vector   []float32
}

type TeamSemanticEmbeddingProjectionRequest struct {
	WriterID    string
	WriterToken string
	JobID       string
	LeaseToken  string
	Model       string
	Results     []TeamSemanticEmbeddingResult
}

type normalizedTeamSemanticEmbeddingResult struct {
	intent       teamSemanticProjectionIntent
	vectorJSON   string
	vectorDigest string
	dimensions   int
}

type teamSemanticEmbeddingMaterialization struct {
	IntentID             string
	JobID                string
	RootObjectID         string
	RootGeneration       int64
	StoreID              string
	TeamID               string
	ScopeType            string
	ScopeID              string
	DerivativeObjectID   string
	DerivativeGeneration int64
	ProjectionKind       string
	StorageKey           string
	Model                string
	Dimensions           int
	VectorJSON           string
	VectorDigest         string
	ContentDigest        string
	SemanticKeyDigest    string
	PolicyDigest         string
}

func (s *Store) CompleteTeamSemanticEmbeddingProjection(
	ctx context.Context,
	request TeamSemanticEmbeddingProjectionRequest,
) (TeamProjectionCompletionResult, error) {
	source, err := s.loadTeamSemanticProjectionSource(ctx, request.JobID, "embedding")
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	results, err := normalizeTeamSemanticEmbeddingResults(source.Intents, request.Model, request.Results)
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	outputs, err := teamSemanticProjectionOutputs(source, "semantic_embedding", request.Model)
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	common := teamSemanticMaterializationsForSource(source)
	embeddings := teamSemanticEmbeddingMaterializations(source, request.Model, results)
	if source.JobState == "ready" {
		if err := s.verifyTeamSemanticEmbeddingMaterializations(ctx, source.JobID, embeddings); err != nil {
			return TeamProjectionCompletionResult{}, err
		}
	}
	completion, err := s.completeTeamProjectionJobWithExtension(ctx, TeamProjectionCompletionRequest{
		WriterID: request.WriterID, WriterToken: request.WriterToken,
		JobID: request.JobID, LeaseToken: request.LeaseToken, Outputs: outputs,
	}, func(ctx context.Context, writer teamProjectionContentWriter, _ teamProjectionCompletionContext) error {
		if err := writer.InsertTeamSemanticMaterializations(ctx, common); err != nil {
			return err
		}
		for _, embedding := range embeddings {
			if err := writer.InsertTeamSemanticEmbedding(ctx, embedding); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	if err := s.verifyTeamSemanticEmbeddingMaterializations(ctx, source.JobID, embeddings); err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	return completion, nil
}

func normalizeTeamSemanticEmbeddingResults(
	intents []teamSemanticProjectionIntent,
	model string,
	results []TeamSemanticEmbeddingResult,
) ([]normalizedTeamSemanticEmbeddingResult, error) {
	if len(intents) < 1 || len(intents) > maxProjectionOutputs {
		return nil, ErrConcealedNotFound
	}
	if !validTeamClass(model, 64) || len(results) != len(intents) {
		return nil, ErrInvalidProjectionJobRequest
	}
	byIntent := make(map[string][]float32, len(results))
	dimensions := 0
	aggregateValues := 0
	for _, result := range results {
		if !validProjectionOpaque(result.IntentID, 255) || byIntent[result.IntentID] != nil ||
			len(result.Vector) < 1 || len(result.Vector) > maxProjectionVectorDimensions ||
			len(result.Vector) > maxProjectionAggregateVectorValues-aggregateValues {
			return nil, ErrInvalidProjectionJobRequest
		}
		aggregateValues += len(result.Vector)
		if dimensions == 0 {
			dimensions = len(result.Vector)
		} else if dimensions != len(result.Vector) {
			return nil, ErrInvalidProjectionJobRequest
		}
		vector := append([]float32(nil), result.Vector...)
		nonZero := false
		for _, value := range vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return nil, ErrInvalidProjectionJobRequest
			}
			if value != 0 {
				nonZero = true
			}
		}
		if !nonZero {
			return nil, ErrInvalidProjectionJobRequest
		}
		byIntent[result.IntentID] = vector
	}

	normalized := make([]normalizedTeamSemanticEmbeddingResult, 0, len(intents))
	seenIntents := make(map[string]bool, len(intents))
	for _, intent := range intents {
		if intent.ProjectionKind != "embedding" || intent.DerivativeKind != "embedding" ||
			!validTeamGraphProjectionSource(intent.SourceKind) || seenIntents[intent.IntentID] {
			return nil, ErrConcealedNotFound
		}
		seenIntents[intent.IntentID] = true
		vector, ok := byIntent[intent.IntentID]
		if !ok {
			return nil, ErrInvalidProjectionJobRequest
		}
		encoded, err := json.Marshal(vector)
		if err != nil {
			return nil, ErrInvalidProjectionJobRequest
		}
		digest := sha256.Sum256(encoded)
		normalized = append(normalized, normalizedTeamSemanticEmbeddingResult{
			intent:       intent,
			vectorJSON:   string(encoded),
			vectorDigest: hex.EncodeToString(digest[:]),
			dimensions:   dimensions,
		})
	}
	return normalized, nil
}

func teamSemanticEmbeddingStorageKey(intentID, model string) string {
	return teamSemanticProjectionStorageKey(intentID, model)
}

func teamSemanticEmbeddingMaterializations(
	source teamSemanticProjectionSource,
	model string,
	results []normalizedTeamSemanticEmbeddingResult,
) []teamSemanticEmbeddingMaterialization {
	materializations := make([]teamSemanticEmbeddingMaterialization, len(results))
	for index, result := range results {
		materializations[index] = teamSemanticEmbeddingMaterialization{
			IntentID: result.intent.IntentID, JobID: source.JobID,
			RootObjectID: source.RootObjectID, RootGeneration: source.RootGeneration,
			StoreID: source.StoreID, TeamID: source.TeamID,
			ScopeType: source.ScopeType, ScopeID: source.ScopeID,
			DerivativeObjectID:   result.intent.DerivativeObjectID,
			DerivativeGeneration: 1, ProjectionKind: "embedding",
			StorageKey: teamSemanticEmbeddingStorageKey(result.intent.IntentID, model),
			Model:      model, Dimensions: result.dimensions,
			VectorJSON: result.vectorJSON, VectorDigest: result.vectorDigest,
			ContentDigest:     result.intent.PayloadDigest,
			SemanticKeyDigest: result.intent.SemanticKeyDigest,
			PolicyDigest:      result.intent.PolicyDigest,
		}
	}
	return materializations
}

func validTeamSemanticEmbeddingMaterialization(
	job projectionCompletionJob,
	materialization teamSemanticEmbeddingMaterialization,
) bool {
	if job.ProjectionKind != "embedding" || materialization.JobID != job.JobID ||
		materialization.RootObjectID != job.RootObjectID ||
		materialization.RootGeneration != job.RootGeneration ||
		materialization.StoreID != job.StoreID || materialization.TeamID != job.TeamID ||
		materialization.ScopeType != job.ScopeType || materialization.ScopeID != job.ScopeID ||
		materialization.DerivativeGeneration != 1 || materialization.ProjectionKind != "embedding" ||
		!validProjectionOpaque(materialization.IntentID, 255) ||
		!validProjectionOpaque(materialization.DerivativeObjectID, 255) ||
		!validTeamClass(materialization.Model, 64) ||
		materialization.StorageKey != teamSemanticEmbeddingStorageKey(
			materialization.IntentID, materialization.Model,
		) || materialization.Dimensions < 1 || materialization.Dimensions > maxProjectionVectorDimensions ||
		!lowerHexDigest(materialization.VectorDigest) || !lowerHexDigest(materialization.ContentDigest) {
		return false
	}
	if !lowerHexDigest(materialization.SemanticKeyDigest) || !lowerHexDigest(materialization.PolicyDigest) {
		return false
	}
	var vector []float32
	if err := json.Unmarshal([]byte(materialization.VectorJSON), &vector); err != nil ||
		len(vector) != materialization.Dimensions {
		return false
	}
	nonZero := false
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
		if value != 0 {
			nonZero = true
		}
	}
	if !nonZero {
		return false
	}
	digest := sha256.Sum256([]byte(materialization.VectorJSON))
	return subtle.ConstantTimeCompare(
		[]byte(materialization.VectorDigest), []byte(hex.EncodeToString(digest[:])),
	) == 1
}

func insertTeamSemanticEmbeddingTx(
	ctx context.Context,
	tx *sql.Tx,
	job projectionCompletionJob,
	materialization teamSemanticEmbeddingMaterialization,
	now time.Time,
) error {
	if !validTeamSemanticEmbeddingMaterialization(job, materialization) {
		return ErrProjectionMaterializationFailed
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO team_semantic_embeddings(
			intent_id, model, store_id, team_id, scope_type, scope_id,
			derivative_object_id, dimensions, vector_json, vector_digest,
			content_digest, created_at)
		SELECT common.intent_id, ?, common.store_id, common.team_id,
		       common.scope_type, common.scope_id, common.derivative_object_id,
		       ?, ?, ?, common.payload_digest, ?
		  FROM team_semantic_materializations common
		  JOIN team_semantic_projection_intents intent ON intent.intent_id = common.intent_id
		 WHERE common.intent_id = ? AND common.job_id = ?
		   AND common.root_object_id = ? AND common.root_generation = ?
		   AND common.store_id = ? AND common.team_id = ?
		   AND common.scope_type = ? AND common.scope_id = ?
		   AND common.projection_kind = 'embedding'
		   AND common.derivative_object_id = ?
		   AND common.derivative_generation = 1
		   AND common.payload_digest = ?
		   AND common.semantic_key_digest = ?
		   AND common.policy_digest = ?
		   AND intent.projection_kind = 'embedding'
		   AND intent.derivative_kind = 'embedding'
		   AND intent.derivative_object_id = common.derivative_object_id
		   AND intent.semantic_key_digest = common.semantic_key_digest
		   AND intent.policy_digest = common.policy_digest
		   AND intent.payload_digest = common.payload_digest`,
		materialization.Model, materialization.Dimensions, materialization.VectorJSON,
		materialization.VectorDigest, now.UTC().Format(time.RFC3339Nano),
		materialization.IntentID, materialization.JobID, materialization.RootObjectID,
		materialization.RootGeneration, materialization.StoreID, materialization.TeamID,
		materialization.ScopeType, materialization.ScopeID,
		materialization.DerivativeObjectID, materialization.ContentDigest,
		materialization.SemanticKeyDigest, materialization.PolicyDigest,
	)
	if err != nil {
		return ErrProjectionMaterializationFailed
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrProjectionMaterializationFailed
	}
	return nil
}

func (s *Store) verifyTeamSemanticEmbeddingMaterializations(
	ctx context.Context,
	jobID string,
	materializations []teamSemanticEmbeddingMaterialization,
) error {
	var commonCount, storedCount int
	if err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM team_semantic_materializations WHERE job_id = ?`, jobID).
		Scan(&commonCount); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT count(*)
		  FROM team_semantic_embeddings embedding
		  JOIN team_semantic_materializations common USING(intent_id)
		 WHERE common.job_id = ?`, jobID).Scan(&storedCount); err != nil {
		return err
	}
	if commonCount != len(materializations) || storedCount != len(materializations) {
		return ErrConcealedNotFound
	}
	for _, want := range materializations {
		var got teamSemanticEmbeddingMaterialization
		err := s.db.QueryRowContext(ctx, `
			SELECT common.intent_id, common.job_id, common.root_object_id,
			       common.root_generation, common.store_id, common.team_id,
			       common.scope_type, common.scope_id, common.derivative_object_id,
			       common.derivative_generation, common.projection_kind,
			       embedding.model, embedding.dimensions, embedding.vector_json,
			       embedding.vector_digest, embedding.content_digest,
			       common.semantic_key_digest, common.policy_digest
			  FROM team_semantic_materializations common
			  JOIN team_semantic_embeddings embedding USING(intent_id)
			  JOIN team_object_contributions contribution
			    ON contribution.parent_object_id = common.root_object_id
			   AND contribution.derivative_object_id = common.derivative_object_id
			   AND contribution.parent_generation = common.root_generation
			   AND contribution.derivative_generation = common.derivative_generation
			   AND contribution.team_id = common.team_id
			   AND contribution.scope_type = common.scope_type
			   AND contribution.scope_id = common.scope_id
			 WHERE common.intent_id = ? AND common.job_id = ? AND embedding.model = ?
			   AND embedding.store_id = common.store_id
			   AND embedding.team_id = common.team_id
			   AND embedding.scope_type = common.scope_type
			   AND embedding.scope_id = common.scope_id
			   AND embedding.derivative_object_id = common.derivative_object_id`,
			want.IntentID, want.JobID, want.Model,
		).Scan(
			&got.IntentID, &got.JobID, &got.RootObjectID, &got.RootGeneration,
			&got.StoreID, &got.TeamID, &got.ScopeType, &got.ScopeID,
			&got.DerivativeObjectID, &got.DerivativeGeneration, &got.ProjectionKind,
			&got.Model, &got.Dimensions, &got.VectorJSON,
			&got.VectorDigest, &got.ContentDigest, &got.SemanticKeyDigest, &got.PolicyDigest,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConcealedNotFound
		}
		if err != nil {
			return err
		}
		got.StorageKey = teamSemanticEmbeddingStorageKey(got.IntentID, got.Model)
		if got != want {
			return ErrConcealedNotFound
		}
	}
	return nil
}
