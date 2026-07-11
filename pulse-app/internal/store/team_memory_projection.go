package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"math"
	"strconv"
	"time"
)

type TeamMemoryEventProjectionRequest struct {
	WriterID    string
	WriterToken string
	JobID       string
	LeaseToken  string
}

type TeamMemoryEmbeddingResult struct {
	CapsuleID string
	Vector    []float32
}

type TeamMemoryEmbeddingProjectionRequest struct {
	WriterID    string
	WriterToken string
	JobID       string
	LeaseToken  string
	Model       string
	Results     []TeamMemoryEmbeddingResult
}

type teamMemoryProjectionIdentity struct {
	DerivativeObjectID string
	StorageKey         string
}

type teamMemoryProjectionCapsule struct {
	CapsuleID       string
	Kind            string
	RedactedSummary string
	SourceTimestamp string
	TagsJSON        string
}

type teamMemoryProjectionSource struct {
	JobID          string
	JobState       string
	RootObjectID   string
	RootGeneration int64
	ProjectionKind string
	Capsules       []teamMemoryProjectionCapsule
}

type teamMemoryEventMaterialization struct {
	JobID              string
	RootObjectID       string
	RootGeneration     int64
	CapsuleID          string
	DerivativeObjectID string
	EventID            string
	ContentDigest      string
}

type teamMemoryEmbeddingMaterialization struct {
	JobID              string
	RootObjectID       string
	RootGeneration     int64
	CapsuleID          string
	DerivativeObjectID string
	EmbeddingID        string
	Model              string
	Dimensions         int
	VectorJSON         string
	VectorDigest       string
	ContentDigest      string
}

func (s *Store) CompleteTeamMemoryEventProjection(
	ctx context.Context,
	request TeamMemoryEventProjectionRequest,
) (TeamProjectionCompletionResult, error) {
	source, err := s.loadTeamMemoryProjectionSource(ctx, request.JobID, "event")
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	outputs := make([]TeamProjectionOutput, 0, len(source.Capsules))
	materializations := make([]teamMemoryEventMaterialization, 0, len(source.Capsules))
	for _, capsule := range source.Capsules {
		identity := deterministicTeamMemoryProjectionIdentity(
			source.RootObjectID, source.RootGeneration, capsule.CapsuleID, "event", "",
		)
		outputs = append(outputs, TeamProjectionOutput{
			DerivativeObjectID:   identity.DerivativeObjectID,
			DerivativeGeneration: 1,
			ObjectKind:           "event",
			StorageMappings: []TeamProjectionStorageMapping{{
				RepresentationKind: "memory_event", StorageKey: identity.StorageKey,
			}},
		})
		materializations = append(materializations, teamMemoryEventMaterialization{
			JobID: request.JobID, RootObjectID: source.RootObjectID,
			RootGeneration: source.RootGeneration, CapsuleID: capsule.CapsuleID,
			DerivativeObjectID: identity.DerivativeObjectID, EventID: identity.StorageKey,
			ContentDigest: teamMemoryProjectionContentDigest(capsule),
		})
	}
	if source.JobState == "ready" {
		if err := s.verifyTeamMemoryEventMaterializations(ctx, materializations); err != nil {
			return TeamProjectionCompletionResult{}, err
		}
	}
	result, err := s.completeTeamProjectionJobWithExtension(ctx, TeamProjectionCompletionRequest{
		WriterID: request.WriterID, WriterToken: request.WriterToken,
		JobID: request.JobID, LeaseToken: request.LeaseToken, Outputs: outputs,
	}, func(ctx context.Context, writer teamProjectionContentWriter, _ teamProjectionCompletionContext) error {
		for _, materialization := range materializations {
			if err := writer.InsertTeamMemoryEvent(ctx, materialization); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	if err := s.verifyTeamMemoryEventMaterializations(ctx, materializations); err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	return result, nil
}

func (s *Store) CompleteTeamMemoryEmbeddingProjection(
	ctx context.Context,
	request TeamMemoryEmbeddingProjectionRequest,
) (TeamProjectionCompletionResult, error) {
	source, err := s.loadTeamMemoryProjectionSource(ctx, request.JobID, "embedding")
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	results, err := normalizeTeamMemoryEmbeddingResults(source, request.Model, request.Results)
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	outputs := make([]TeamProjectionOutput, 0, len(results))
	materializations := make([]teamMemoryEmbeddingMaterialization, 0, len(results))
	for _, result := range results {
		identity := deterministicTeamMemoryProjectionIdentity(
			source.RootObjectID, source.RootGeneration, result.capsule.CapsuleID,
			"embedding", request.Model,
		)
		outputs = append(outputs, TeamProjectionOutput{
			DerivativeObjectID:   identity.DerivativeObjectID,
			DerivativeGeneration: 1,
			ObjectKind:           "embedding",
			StorageMappings: []TeamProjectionStorageMapping{{
				RepresentationKind: "memory_embedding", StorageKey: identity.StorageKey,
			}},
		})
		materializations = append(materializations, teamMemoryEmbeddingMaterialization{
			JobID: request.JobID, RootObjectID: source.RootObjectID,
			RootGeneration: source.RootGeneration, CapsuleID: result.capsule.CapsuleID,
			DerivativeObjectID: identity.DerivativeObjectID, EmbeddingID: identity.StorageKey,
			Model: request.Model, Dimensions: len(result.vector), VectorJSON: result.vectorJSON,
			VectorDigest:  result.vectorDigest,
			ContentDigest: teamMemoryProjectionContentDigest(result.capsule),
		})
	}
	if source.JobState == "ready" {
		if err := s.verifyTeamMemoryEmbeddingMaterializations(ctx, materializations); err != nil {
			return TeamProjectionCompletionResult{}, err
		}
	}
	result, err := s.completeTeamProjectionJobWithExtension(ctx, TeamProjectionCompletionRequest{
		WriterID: request.WriterID, WriterToken: request.WriterToken,
		JobID: request.JobID, LeaseToken: request.LeaseToken, Outputs: outputs,
	}, func(ctx context.Context, writer teamProjectionContentWriter, _ teamProjectionCompletionContext) error {
		for _, materialization := range materializations {
			if err := writer.InsertTeamMemoryEmbedding(ctx, materialization); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	if err := s.verifyTeamMemoryEmbeddingMaterializations(ctx, materializations); err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	return result, nil
}

type normalizedTeamMemoryEmbeddingResult struct {
	capsule      teamMemoryProjectionCapsule
	vector       []float32
	vectorJSON   string
	vectorDigest string
}

func normalizeTeamMemoryEmbeddingResults(
	source teamMemoryProjectionSource,
	model string,
	results []TeamMemoryEmbeddingResult,
) ([]normalizedTeamMemoryEmbeddingResult, error) {
	if !validTeamClass(model, 64) || len(results) != len(source.Capsules) || len(results) == 0 ||
		len(results) > maxProjectionOutputs {
		return nil, ErrInvalidProjectionJobRequest
	}
	byCapsule := make(map[string][]float32, len(results))
	dimensions := 0
	aggregateVectorValues := 0
	for _, result := range results {
		if !validProjectionOpaque(result.CapsuleID, 255) || len(result.Vector) == 0 ||
			len(result.Vector) > maxProjectionVectorDimensions ||
			len(result.Vector) > maxProjectionAggregateVectorValues-aggregateVectorValues ||
			byCapsule[result.CapsuleID] != nil {
			return nil, ErrInvalidProjectionJobRequest
		}
		aggregateVectorValues += len(result.Vector)
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
		byCapsule[result.CapsuleID] = vector
	}
	normalized := make([]normalizedTeamMemoryEmbeddingResult, 0, len(source.Capsules))
	for _, capsule := range source.Capsules {
		vector, ok := byCapsule[capsule.CapsuleID]
		if !ok {
			return nil, ErrInvalidProjectionJobRequest
		}
		encoded, err := json.Marshal(vector)
		if err != nil {
			return nil, ErrInvalidProjectionJobRequest
		}
		digest := sha256.Sum256(encoded)
		normalized = append(normalized, normalizedTeamMemoryEmbeddingResult{
			capsule: capsule, vector: vector, vectorJSON: string(encoded),
			vectorDigest: hex.EncodeToString(digest[:]),
		})
	}
	return normalized, nil
}

func cloneTeamMemoryEmbeddingResults(results []TeamMemoryEmbeddingResult) []TeamMemoryEmbeddingResult {
	cloned := make([]TeamMemoryEmbeddingResult, len(results))
	for index, result := range results {
		cloned[index] = result
		cloned[index].Vector = append([]float32(nil), result.Vector...)
	}
	return cloned
}

func (s *Store) loadTeamMemoryProjectionSource(
	ctx context.Context,
	jobID, projectionKind string,
) (teamMemoryProjectionSource, error) {
	if !validProjectionOpaque(jobID, 255) || (projectionKind != "event" && projectionKind != "embedding") {
		return teamMemoryProjectionSource{}, ErrInvalidProjectionJobRequest
	}
	var source teamMemoryProjectionSource
	err := s.db.QueryRowContext(ctx, `
		SELECT job.job_id, job.state, job.root_object_id, job.root_generation, job.projection_kind
		  FROM team_projection_jobs job
		  JOIN team_object_registry root ON root.object_id = job.root_object_id
		 WHERE job.job_id = ? AND job.projection_kind = ? AND root.object_kind = 'memory'`,
		jobID, projectionKind,
	).Scan(&source.JobID, &source.JobState, &source.RootObjectID, &source.RootGeneration, &source.ProjectionKind)
	if errors.Is(err, sql.ErrNoRows) {
		return teamMemoryProjectionSource{}, ErrConcealedNotFound
	}
	if err != nil {
		return teamMemoryProjectionSource{}, ErrProjectionMaterializationFailed
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT capsule_id, kind, redacted_summary, source_timestamp, tags_json
		  FROM team_memory_capsules
		 WHERE root_object_id = ? AND root_generation = ?
		 ORDER BY capsule_id`, source.RootObjectID, source.RootGeneration)
	if err != nil {
		return teamMemoryProjectionSource{}, ErrProjectionMaterializationFailed
	}
	defer rows.Close()
	for rows.Next() {
		var capsule teamMemoryProjectionCapsule
		if err := rows.Scan(
			&capsule.CapsuleID, &capsule.Kind, &capsule.RedactedSummary,
			&capsule.SourceTimestamp, &capsule.TagsJSON,
		); err != nil {
			return teamMemoryProjectionSource{}, ErrProjectionMaterializationFailed
		}
		source.Capsules = append(source.Capsules, capsule)
	}
	if err := rows.Err(); err != nil || len(source.Capsules) == 0 {
		return teamMemoryProjectionSource{}, ErrProjectionMaterializationFailed
	}
	return source, nil
}

func deterministicTeamMemoryProjectionIdentity(
	rootObjectID string,
	rootGeneration int64,
	capsuleID, projectionKind, model string,
) teamMemoryProjectionIdentity {
	fields := []string{
		rootObjectID, strconv.FormatInt(rootGeneration, 10), capsuleID, projectionKind, model,
	}
	derivative := teamMemoryProjectionDigest("pulse-team-memory-derivative-v1", fields...)
	storage := teamMemoryProjectionDigest("pulse-team-memory-storage-v1", fields...)
	return teamMemoryProjectionIdentity{
		DerivativeObjectID: "team_derivative_" + derivative,
		StorageKey:         "team_storage_" + storage,
	}
}

func teamMemoryProjectionContentDigest(capsule teamMemoryProjectionCapsule) string {
	return teamMemoryProjectionDigest(
		"pulse-team-memory-content-v1", capsule.CapsuleID, capsule.Kind,
		capsule.RedactedSummary, capsule.SourceTimestamp, capsule.TagsJSON,
	)
}

func teamMemoryProjectionDigest(domain string, fields ...string) string {
	digest := sha256.New()
	writeTeamMemoryProjectionDigestField(digest, domain)
	for _, field := range fields {
		writeTeamMemoryProjectionDigestField(digest, field)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func writeTeamMemoryProjectionDigestField(digest hash.Hash, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write([]byte(value))
}

func insertTeamMemoryEventTx(
	ctx context.Context,
	tx *sql.Tx,
	job projectionCompletionJob,
	materialization teamMemoryEventMaterialization,
	now time.Time,
) error {
	if !validTeamMemoryEventMaterialization(job, materialization) {
		return ErrProjectionMaterializationFailed
	}
	capsule, err := loadTeamMemoryProjectionCapsuleTx(ctx, tx, job, materialization.CapsuleID)
	if err != nil || !projectionDigestMatches(
		materialization.ContentDigest, teamMemoryProjectionContentDigest(capsule),
	) {
		return ErrProjectionMaterializationFailed
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO team_memory_events(
			event_id, derivative_object_id, job_id, root_object_id, root_generation,
			capsule_id, team_id, scope_type, scope_id, kind, redacted_summary,
			source_timestamp, tags_json, content_digest, created_at)
		SELECT ?, ?, ?, ?, ?, capsule.capsule_id, capsule.team_id, capsule.scope_type,
		       capsule.scope_id, capsule.kind, capsule.redacted_summary,
		       capsule.source_timestamp, capsule.tags_json, ?, ?
		  FROM team_memory_capsules capsule
		 WHERE capsule.capsule_id = ? AND capsule.root_object_id = ?
		   AND capsule.root_generation = ? AND capsule.team_id = ?
		   AND capsule.scope_type = ? AND capsule.scope_id = ?`,
		materialization.EventID, materialization.DerivativeObjectID, job.JobID,
		job.RootObjectID, job.RootGeneration, materialization.ContentDigest,
		now.UTC().Format(time.RFC3339Nano), materialization.CapsuleID,
		job.RootObjectID, job.RootGeneration, job.TeamID, job.ScopeType, job.ScopeID,
	)
	if err != nil {
		return ErrProjectionMaterializationFailed
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return ErrProjectionMaterializationFailed
	}
	return nil
}

func insertTeamMemoryEmbeddingTx(
	ctx context.Context,
	tx *sql.Tx,
	job projectionCompletionJob,
	materialization teamMemoryEmbeddingMaterialization,
	now time.Time,
) error {
	if !validTeamMemoryEmbeddingMaterialization(job, materialization) {
		return ErrProjectionMaterializationFailed
	}
	capsule, err := loadTeamMemoryProjectionCapsuleTx(ctx, tx, job, materialization.CapsuleID)
	if err != nil || !projectionDigestMatches(
		materialization.ContentDigest, teamMemoryProjectionContentDigest(capsule),
	) {
		return ErrProjectionMaterializationFailed
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO team_memory_embeddings(
			embedding_id, derivative_object_id, job_id, root_object_id, root_generation,
			capsule_id, team_id, scope_type, scope_id, model, dimensions, vector_json,
			vector_digest, content_digest, created_at)
		SELECT ?, ?, ?, ?, ?, capsule.capsule_id, capsule.team_id, capsule.scope_type,
		       capsule.scope_id, ?, ?, ?, ?, ?, ?
		  FROM team_memory_capsules capsule
		 WHERE capsule.capsule_id = ? AND capsule.root_object_id = ?
		   AND capsule.root_generation = ? AND capsule.team_id = ?
		   AND capsule.scope_type = ? AND capsule.scope_id = ?`,
		materialization.EmbeddingID, materialization.DerivativeObjectID, job.JobID,
		job.RootObjectID, job.RootGeneration, materialization.Model,
		materialization.Dimensions, materialization.VectorJSON,
		materialization.VectorDigest, materialization.ContentDigest,
		now.UTC().Format(time.RFC3339Nano), materialization.CapsuleID,
		job.RootObjectID, job.RootGeneration, job.TeamID, job.ScopeType, job.ScopeID,
	)
	if err != nil {
		return ErrProjectionMaterializationFailed
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return ErrProjectionMaterializationFailed
	}
	return nil
}

func validTeamMemoryEventMaterialization(job projectionCompletionJob, materialization teamMemoryEventMaterialization) bool {
	identity := deterministicTeamMemoryProjectionIdentity(
		job.RootObjectID, job.RootGeneration, materialization.CapsuleID, "event", "",
	)
	return job.ProjectionKind == "event" && materialization.JobID == job.JobID &&
		materialization.RootObjectID == job.RootObjectID && materialization.RootGeneration == job.RootGeneration &&
		validProjectionOpaque(materialization.CapsuleID, 255) &&
		materialization.DerivativeObjectID == identity.DerivativeObjectID &&
		materialization.EventID == identity.StorageKey && lowerHexDigest(materialization.ContentDigest)
}

func validTeamMemoryEmbeddingMaterialization(
	job projectionCompletionJob,
	materialization teamMemoryEmbeddingMaterialization,
) bool {
	identity := deterministicTeamMemoryProjectionIdentity(
		job.RootObjectID, job.RootGeneration, materialization.CapsuleID, "embedding", materialization.Model,
	)
	if job.ProjectionKind != "embedding" || materialization.JobID != job.JobID ||
		materialization.RootObjectID != job.RootObjectID || materialization.RootGeneration != job.RootGeneration ||
		!validProjectionOpaque(materialization.CapsuleID, 255) ||
		materialization.DerivativeObjectID != identity.DerivativeObjectID ||
		materialization.EmbeddingID != identity.StorageKey || !validTeamClass(materialization.Model, 64) ||
		materialization.Dimensions < 1 || materialization.Dimensions > 4096 ||
		!lowerHexDigest(materialization.VectorDigest) || !lowerHexDigest(materialization.ContentDigest) {
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

func loadTeamMemoryProjectionCapsuleTx(
	ctx context.Context,
	tx *sql.Tx,
	job projectionCompletionJob,
	capsuleID string,
) (teamMemoryProjectionCapsule, error) {
	var capsule teamMemoryProjectionCapsule
	err := tx.QueryRowContext(ctx, `
		SELECT capsule_id, kind, redacted_summary, source_timestamp, tags_json
		  FROM team_memory_capsules
		 WHERE capsule_id = ? AND root_object_id = ? AND root_generation = ?
		   AND team_id = ? AND scope_type = ? AND scope_id = ?`,
		capsuleID, job.RootObjectID, job.RootGeneration,
		job.TeamID, job.ScopeType, job.ScopeID,
	).Scan(
		&capsule.CapsuleID, &capsule.Kind, &capsule.RedactedSummary,
		&capsule.SourceTimestamp, &capsule.TagsJSON,
	)
	return capsule, err
}

func (s *Store) verifyTeamMemoryEventMaterializations(
	ctx context.Context,
	materializations []teamMemoryEventMaterialization,
) error {
	for _, materialization := range materializations {
		var derivativeID, eventID, contentDigest string
		err := s.db.QueryRowContext(ctx, `
			SELECT derivative_object_id, event_id, content_digest
			  FROM team_memory_events
			 WHERE job_id = ? AND root_object_id = ? AND root_generation = ? AND capsule_id = ?`,
			materialization.JobID, materialization.RootObjectID,
			materialization.RootGeneration, materialization.CapsuleID,
		).Scan(&derivativeID, &eventID, &contentDigest)
		if err != nil || derivativeID != materialization.DerivativeObjectID ||
			eventID != materialization.EventID || !projectionDigestMatches(contentDigest, materialization.ContentDigest) {
			return ErrConcealedNotFound
		}
	}
	return nil
}

func (s *Store) verifyTeamMemoryEmbeddingMaterializations(
	ctx context.Context,
	materializations []teamMemoryEmbeddingMaterialization,
) error {
	for _, materialization := range materializations {
		var derivativeID, embeddingID, model, vectorDigest, contentDigest string
		var dimensions int
		err := s.db.QueryRowContext(ctx, `
			SELECT derivative_object_id, embedding_id, model, dimensions, vector_digest, content_digest
			  FROM team_memory_embeddings
			 WHERE job_id = ? AND root_object_id = ? AND root_generation = ? AND capsule_id = ?`,
			materialization.JobID, materialization.RootObjectID,
			materialization.RootGeneration, materialization.CapsuleID,
		).Scan(&derivativeID, &embeddingID, &model, &dimensions, &vectorDigest, &contentDigest)
		if err != nil || derivativeID != materialization.DerivativeObjectID ||
			embeddingID != materialization.EmbeddingID || model != materialization.Model ||
			dimensions != materialization.Dimensions ||
			!projectionDigestMatches(vectorDigest, materialization.VectorDigest) ||
			!projectionDigestMatches(contentDigest, materialization.ContentDigest) {
			return ErrConcealedNotFound
		}
	}
	return nil
}
