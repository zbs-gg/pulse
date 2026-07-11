package store

import (
	"context"
	"database/sql"
	"errors"
)

func validateTeamSemanticProjectionMaterializationIntegrity(
	ctx context.Context,
	q teamGraphIntegrityQueryer,
) error {
	var invalid int
	if err := q.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			  FROM team_semantic_materializations materialization
			  LEFT JOIN team_projection_jobs job ON job.job_id = materialization.job_id
			 WHERE job.job_id IS NULL OR job.state <> 'ready'
		)`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return ErrTeamPolicyNotReady
	}

	rows, err := q.QueryContext(ctx, `
		SELECT job.job_id, job.projection_kind
		  FROM team_projection_jobs job
		  JOIN team_object_registry root ON root.object_id = job.root_object_id
		 WHERE root.object_kind = 'graph_delta' AND root.lifecycle = 'active'
		   AND root.generation = job.root_generation AND job.state = 'ready'
		 ORDER BY job.job_id`)
	if err != nil {
		return err
	}
	var jobs [][2]string
	for rows.Next() {
		var job [2]string
		if err := rows.Scan(&job[0], &job[1]); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, job := range jobs {
		source, err := loadTeamSemanticProjectionSourceWithQueryer(ctx, q, job[0], job[1])
		if err != nil {
			return semanticReadinessError(err)
		}
		if err := validateTeamSemanticCommonMaterializationsWithQueryer(ctx, q, source); err != nil {
			return semanticReadinessError(err)
		}
		var outputs []TeamProjectionOutput
		switch source.ProjectionKind {
		case "graph":
			materializations, err := buildTeamGraphMaterializations(source)
			if err != nil {
				return semanticReadinessError(err)
			}
			if err := verifyTeamGraphMaterializationsWithQueryer(
				ctx, q, source.JobID, materializations,
			); err != nil {
				return semanticReadinessError(err)
			}
			outputs, err = teamSemanticProjectionOutputs(source, "semantic_graph", "")
			if err != nil {
				return semanticReadinessError(err)
			}
		case "claim":
			materializations, err := buildTeamAssertionMaterializations(source)
			if err != nil {
				return semanticReadinessError(err)
			}
			if err := verifyTeamAssertionMaterializationsWithQueryer(
				ctx, q, source.JobID, materializations,
			); err != nil {
				return semanticReadinessError(err)
			}
			outputs, err = teamSemanticProjectionOutputs(source, "semantic_assertion", "")
			if err != nil {
				return semanticReadinessError(err)
			}
		case "continuity":
			materializations, err := buildTeamContinuityMaterializations(source)
			if err != nil {
				return semanticReadinessError(err)
			}
			if err := verifyTeamContinuityMaterializationsWithQueryer(
				ctx, q, source.JobID, materializations,
			); err != nil {
				return semanticReadinessError(err)
			}
			outputs, err = teamSemanticProjectionOutputs(source, "semantic_continuity", "")
			if err != nil {
				return semanticReadinessError(err)
			}
		case "embedding":
			model, err := validateReadyTeamSemanticEmbeddingAndModelWithQueryer(ctx, q, source)
			if err != nil {
				return semanticReadinessError(err)
			}
			outputs, err = teamSemanticProjectionOutputs(source, "semantic_embedding", model)
			if err != nil {
				return semanticReadinessError(err)
			}
		default:
			return ErrTeamPolicyNotReady
		}
		if err := validateTeamSemanticProjectionAttachments(ctx, q, source, outputs); err != nil {
			return semanticReadinessError(err)
		}
	}
	return nil
}

func validateReadyTeamSemanticEmbeddingAndModelWithQueryer(
	ctx context.Context,
	q teamGraphIntegrityQueryer,
	source teamSemanticProjectionSource,
) (string, error) {
	intents := make(map[string]teamSemanticProjectionIntent, len(source.Intents))
	for _, intent := range source.Intents {
		intents[intent.IntentID] = intent
	}
	rows, err := q.QueryContext(ctx, `
		SELECT common.intent_id, common.job_id, common.root_object_id,
		       common.root_generation, common.store_id, common.team_id,
		       common.scope_type, common.scope_id, common.derivative_object_id,
		       common.derivative_generation, common.projection_kind,
		       embedding.model, embedding.dimensions, embedding.vector_json,
		       embedding.vector_digest, embedding.content_digest,
		       common.semantic_key_digest, common.policy_digest,
		       embedding.store_id, embedding.team_id, embedding.scope_type,
		       embedding.scope_id, embedding.derivative_object_id
		  FROM team_semantic_materializations common
		  JOIN team_semantic_embeddings embedding USING(intent_id)
		 WHERE common.job_id = ? ORDER BY common.intent_id, embedding.model`, source.JobID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	job := projectionCompletionJob{
		JobID: source.JobID, RootObjectID: source.RootObjectID,
		RootGeneration: source.RootGeneration, StoreID: source.StoreID,
		TeamID: source.TeamID, ScopeType: source.ScopeType, ScopeID: source.ScopeID,
		ProjectionKind: "embedding",
	}
	seen := make(map[string]struct{}, len(source.Intents))
	model := ""
	dimensions := 0
	aggregate := 0
	for rows.Next() {
		var materialization teamSemanticEmbeddingMaterialization
		var embeddingStoreID, embeddingTeamID, embeddingScopeType string
		var embeddingScopeID, embeddingDerivativeID string
		if err := rows.Scan(
			&materialization.IntentID, &materialization.JobID,
			&materialization.RootObjectID, &materialization.RootGeneration,
			&materialization.StoreID, &materialization.TeamID,
			&materialization.ScopeType, &materialization.ScopeID,
			&materialization.DerivativeObjectID, &materialization.DerivativeGeneration,
			&materialization.ProjectionKind, &materialization.Model,
			&materialization.Dimensions, &materialization.VectorJSON,
			&materialization.VectorDigest, &materialization.ContentDigest,
			&materialization.SemanticKeyDigest, &materialization.PolicyDigest,
			&embeddingStoreID, &embeddingTeamID, &embeddingScopeType,
			&embeddingScopeID, &embeddingDerivativeID,
		); err != nil {
			return "", err
		}
		materialization.StorageKey = teamSemanticEmbeddingStorageKey(
			materialization.IntentID, materialization.Model,
		)
		intent, ok := intents[materialization.IntentID]
		_, duplicate := seen[materialization.IntentID]
		if !ok || duplicate || !validTeamSemanticEmbeddingMaterialization(job, materialization) ||
			embeddingStoreID != materialization.StoreID || embeddingTeamID != materialization.TeamID ||
			embeddingScopeType != materialization.ScopeType || embeddingScopeID != materialization.ScopeID ||
			embeddingDerivativeID != materialization.DerivativeObjectID ||
			materialization.DerivativeObjectID != intent.DerivativeObjectID ||
			materialization.ContentDigest != intent.PayloadDigest ||
			materialization.SemanticKeyDigest != intent.SemanticKeyDigest ||
			materialization.PolicyDigest != intent.PolicyDigest {
			return "", ErrConcealedNotFound
		}
		if model == "" {
			model, dimensions = materialization.Model, materialization.Dimensions
		} else if materialization.Model != model || materialization.Dimensions != dimensions {
			return "", ErrConcealedNotFound
		}
		aggregate += materialization.Dimensions
		if aggregate > maxProjectionAggregateVectorValues {
			return "", ErrConcealedNotFound
		}
		seen[materialization.IntentID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if model == "" || len(seen) != len(source.Intents) {
		return "", ErrConcealedNotFound
	}
	return model, nil
}

func validateTeamSemanticProjectionAttachments(
	ctx context.Context,
	q teamGraphIntegrityQueryer,
	source teamSemanticProjectionSource,
	outputs []TeamProjectionOutput,
) error {
	var state, completionDigest string
	if err := q.QueryRowContext(ctx, `
		SELECT state, COALESCE(completion_digest, '')
		  FROM team_projection_jobs WHERE job_id = ?`, source.JobID).
		Scan(&state, &completionDigest); err != nil {
		return err
	}
	if state != "ready" || !projectionDigestMatches(
		completionDigest, projectionCompletionDigest(outputs),
	) {
		return ErrConcealedNotFound
	}
	var outputCount int
	if err := q.QueryRowContext(ctx, `
		SELECT count(*) FROM team_projection_outputs WHERE job_id = ?`, source.JobID).
		Scan(&outputCount); err != nil {
		return err
	}
	if outputCount != len(outputs) {
		return ErrConcealedNotFound
	}
	for _, output := range outputs {
		var present int
		if err := q.QueryRowContext(ctx, `
			SELECT count(*)
			  FROM team_projection_outputs projected
			  JOIN team_object_registry derivative
			    ON derivative.object_id = projected.derivative_object_id
			   AND derivative.generation = projected.derivative_generation
			   AND derivative.lifecycle = 'active'
			  JOIN team_object_contributions contribution
			    ON contribution.parent_object_id = ?
			   AND contribution.derivative_object_id = derivative.object_id
			   AND contribution.parent_generation = ?
			   AND contribution.derivative_generation = derivative.generation
			 WHERE projected.job_id = ? AND projected.derivative_object_id = ?
			   AND projected.derivative_generation = ?
			   AND derivative.store_id = ? AND derivative.team_id = ?
			   AND derivative.scope_type = ? AND derivative.scope_id = ?
			   AND derivative.object_kind = ?
			   AND contribution.team_id = ? AND contribution.scope_type = ?
			   AND contribution.scope_id = ?`,
			source.RootObjectID, source.RootGeneration, source.JobID,
			output.DerivativeObjectID, output.DerivativeGeneration,
			source.StoreID, source.TeamID, source.ScopeType, source.ScopeID,
			output.ObjectKind, source.TeamID, source.ScopeType, source.ScopeID,
		).Scan(&present); err != nil {
			return err
		}
		if present != 1 {
			return ErrConcealedNotFound
		}
		for _, mapping := range output.StorageMappings {
			if err := q.QueryRowContext(ctx, `
				SELECT count(*) FROM team_object_storage_map
				 WHERE object_id = ? AND team_id = ? AND scope_type = ? AND scope_id = ?
				   AND generation = ? AND representation_kind = ? AND storage_key = ?`,
				output.DerivativeObjectID, source.TeamID, source.ScopeType, source.ScopeID,
				output.DerivativeGeneration, mapping.RepresentationKind, mapping.StorageKey,
			).Scan(&present); err != nil {
				return err
			}
			if present != 1 {
				return ErrConcealedNotFound
			}
		}
	}
	return nil
}

func semanticReadinessError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrConcealedNotFound) ||
		errors.Is(err, ErrProjectionMaterializationFailed) ||
		errors.Is(err, ErrInvalidProjectionJobRequest) ||
		errors.Is(err, ErrTeamPolicyNotReady) || errors.Is(err, sql.ErrNoRows) {
		return ErrTeamPolicyNotReady
	}
	return err
}
