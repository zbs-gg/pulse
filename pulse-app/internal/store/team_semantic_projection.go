package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sort"
	"time"
)

// TeamSemanticProjectionRequest deliberately contains only worker and lease
// authority. Structured graph, claim, and continuity content is reconstructed
// from the canonical ingress row inside the team store.
type TeamSemanticProjectionRequest struct {
	WriterID    string
	WriterToken string
	JobID       string
	LeaseToken  string
}

type teamSemanticProjectionSource struct {
	JobID          string
	JobState       string
	RootObjectID   string
	RootGeneration int64
	StoreID        string
	TeamID         string
	ScopeType      string
	ScopeID        string
	ProjectionKind string
	Normalized     normalizedTeamGraphDeltaWrite
	Intents        []teamSemanticProjectionIntent
}

type teamSemanticMaterialization struct {
	IntentID             string
	JobID                string
	RootObjectID         string
	RootGeneration       int64
	DerivativeObjectID   string
	DerivativeGeneration int64
	StoreID              string
	TeamID               string
	ScopeType            string
	ScopeID              string
	ProjectionKind       string
	SemanticKeyDigest    string
	PolicyDigest         string
	PayloadDigest        string
}

func (s *Store) loadTeamSemanticProjectionSource(
	ctx context.Context,
	jobID, projectionKind string,
) (teamSemanticProjectionSource, error) {
	return loadTeamSemanticProjectionSourceWithQueryer(ctx, s.db, jobID, projectionKind)
}

func loadTeamSemanticProjectionSourceWithQueryer(
	ctx context.Context,
	q teamGraphIntegrityQueryer,
	jobID, projectionKind string,
) (teamSemanticProjectionSource, error) {
	if !validProjectionOpaque(jobID, 255) || !validTeamSemanticProjectionKind(projectionKind) {
		return teamSemanticProjectionSource{}, ErrInvalidProjectionJobRequest
	}

	var source teamSemanticProjectionSource
	err := q.QueryRowContext(ctx, `
		SELECT job.job_id, job.state, job.root_object_id, job.root_generation,
		       job.store_id, job.team_id, job.scope_type, job.scope_id,
		       job.projection_kind
		  FROM team_projection_jobs job
		  JOIN team_object_registry root
		    ON root.object_id = job.root_object_id
		   AND root.store_id = job.store_id
		   AND root.team_id = job.team_id
		   AND root.scope_type = job.scope_type
		   AND root.scope_id = job.scope_id
		   AND root.generation = job.root_generation
		   AND root.object_kind = 'graph_delta'
		   AND root.lifecycle = 'active'
		 WHERE job.job_id = ? AND job.projection_kind = ?`, jobID, projectionKind).Scan(
		&source.JobID, &source.JobState, &source.RootObjectID, &source.RootGeneration,
		&source.StoreID, &source.TeamID, &source.ScopeType, &source.ScopeID,
		&source.ProjectionKind,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return teamSemanticProjectionSource{}, ErrConcealedNotFound
	}
	if err != nil {
		return teamSemanticProjectionSource{}, err
	}
	if source.JobState != "leased" && source.JobState != "ready" {
		return teamSemanticProjectionSource{}, ErrConcealedNotFound
	}

	root, err := loadTeamGraphIntegrityRoot(ctx, q, source.RootObjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return teamSemanticProjectionSource{}, ErrConcealedNotFound
	}
	if err != nil {
		return teamSemanticProjectionSource{}, err
	}
	if root.storeID != source.StoreID || root.teamID != source.TeamID ||
		string(root.scopeType) != source.ScopeType || root.scopeID != source.ScopeID ||
		root.generation != source.RootGeneration {
		return teamSemanticProjectionSource{}, ErrConcealedNotFound
	}
	normalized, err := loadValidatedTeamGraphIngressDescriptorSet(ctx, q, root)
	if errors.Is(err, ErrTeamPolicyNotReady) || errors.Is(err, sql.ErrNoRows) {
		return teamSemanticProjectionSource{}, ErrConcealedNotFound
	}
	if err != nil {
		return teamSemanticProjectionSource{}, err
	}
	source.Normalized = normalized

	rows, err := q.QueryContext(ctx, `
		SELECT intent_id, projection_kind, source_kind, source_ordinal,
		       derivative_object_id, derivative_kind, semantic_key_digest,
		       policy_digest, payload_digest, store_id, team_id, scope_type,
		       scope_id, root_generation
		  FROM team_semantic_projection_intents
		 WHERE root_object_id = ? AND root_generation = ? AND projection_kind = ?
		 ORDER BY source_kind, source_ordinal, intent_id`,
		source.RootObjectID, source.RootGeneration, projectionKind)
	if err != nil {
		return teamSemanticProjectionSource{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var intent teamSemanticProjectionIntent
		var storeID, teamID, scopeType, scopeID string
		var generation int64
		if err := rows.Scan(
			&intent.IntentID, &intent.ProjectionKind, &intent.SourceKind,
			&intent.SourceOrdinal, &intent.DerivativeObjectID, &intent.DerivativeKind,
			&intent.SemanticKeyDigest, &intent.PolicyDigest, &intent.PayloadDigest,
			&storeID, &teamID, &scopeType, &scopeID, &generation,
		); err != nil {
			return teamSemanticProjectionSource{}, err
		}
		if storeID != source.StoreID || teamID != source.TeamID ||
			scopeType != source.ScopeType || scopeID != source.ScopeID ||
			generation != source.RootGeneration || intent.ProjectionKind != projectionKind {
			return teamSemanticProjectionSource{}, ErrConcealedNotFound
		}
		source.Intents = append(source.Intents, intent)
	}
	if err := rows.Err(); err != nil {
		return teamSemanticProjectionSource{}, err
	}
	if len(source.Intents) == 0 || len(source.Intents) > maxProjectionOutputs {
		return teamSemanticProjectionSource{}, ErrConcealedNotFound
	}
	return source, nil
}

func validTeamSemanticProjectionKind(kind string) bool {
	switch kind {
	case "claim", "continuity", "embedding", "graph":
		return true
	default:
		return false
	}
}

func teamSemanticProjectionOutputs(
	source teamSemanticProjectionSource,
	representationKind, model string,
) ([]TeamProjectionOutput, error) {
	if !validProjectionOpaque(representationKind, 64) ||
		(model != "" && !validTeamClass(model, 64)) {
		return nil, ErrInvalidProjectionJobRequest
	}
	byDerivative := make(map[string]*TeamProjectionOutput, len(source.Intents))
	seenMappings := make(map[string]struct{}, len(source.Intents))
	for _, intent := range source.Intents {
		storageKey := teamSemanticProjectionStorageKey(intent.IntentID, model)
		mappingKey := representationKind + "\x00" + storageKey
		if _, duplicate := seenMappings[mappingKey]; duplicate {
			return nil, ErrConcealedNotFound
		}
		seenMappings[mappingKey] = struct{}{}
		output := byDerivative[intent.DerivativeObjectID]
		if output == nil {
			output = &TeamProjectionOutput{
				DerivativeObjectID:   intent.DerivativeObjectID,
				DerivativeGeneration: 1,
				ObjectKind:           intent.DerivativeKind,
			}
			byDerivative[intent.DerivativeObjectID] = output
		} else if output.ObjectKind != intent.DerivativeKind {
			return nil, ErrConcealedNotFound
		}
		output.StorageMappings = append(output.StorageMappings, TeamProjectionStorageMapping{
			RepresentationKind: representationKind,
			StorageKey:         storageKey,
		})
	}
	outputs := make([]TeamProjectionOutput, 0, len(byDerivative))
	for _, output := range byDerivative {
		sort.Slice(output.StorageMappings, func(i, j int) bool {
			left, right := output.StorageMappings[i], output.StorageMappings[j]
			if left.RepresentationKind != right.RepresentationKind {
				return left.RepresentationKind < right.RepresentationKind
			}
			return left.StorageKey < right.StorageKey
		})
		outputs = append(outputs, *output)
	}
	sort.Slice(outputs, func(i, j int) bool {
		return outputs[i].DerivativeObjectID < outputs[j].DerivativeObjectID
	})
	if len(outputs) == 0 || len(outputs) > maxProjectionOutputs {
		return nil, ErrConcealedNotFound
	}
	return outputs, nil
}

func teamSemanticProjectionStorageKey(intentID, model string) string {
	return teamGraphOpaqueDigestID(
		"semantic_storage", "pulse-team-semantic-storage-v1", intentID, model,
	)
}

func teamSemanticMaterializationsForSource(
	source teamSemanticProjectionSource,
) []teamSemanticMaterialization {
	materializations := make([]teamSemanticMaterialization, len(source.Intents))
	for index, intent := range source.Intents {
		materializations[index] = teamSemanticMaterialization{
			IntentID: intent.IntentID, JobID: source.JobID,
			RootObjectID: source.RootObjectID, RootGeneration: source.RootGeneration,
			DerivativeObjectID: intent.DerivativeObjectID, DerivativeGeneration: 1,
			StoreID: source.StoreID, TeamID: source.TeamID,
			ScopeType: source.ScopeType, ScopeID: source.ScopeID,
			ProjectionKind:    source.ProjectionKind,
			SemanticKeyDigest: intent.SemanticKeyDigest,
			PolicyDigest:      intent.PolicyDigest, PayloadDigest: intent.PayloadDigest,
		}
	}
	return materializations
}

func validateTeamSemanticCommonMaterializationsWithQueryer(
	ctx context.Context,
	q teamGraphIntegrityQueryer,
	source teamSemanticProjectionSource,
) error {
	want := teamSemanticMaterializationsForSource(source)
	sort.Slice(want, func(i, j int) bool { return want[i].IntentID < want[j].IntentID })
	rows, err := q.QueryContext(ctx, `
		SELECT intent_id, job_id, root_object_id, root_generation,
		       derivative_object_id, derivative_generation, store_id, team_id,
		       scope_type, scope_id, projection_kind, semantic_key_digest,
		       policy_digest, payload_digest
		  FROM team_semantic_materializations
		 WHERE job_id = ? ORDER BY intent_id`, source.JobID)
	if err != nil {
		return err
	}
	defer rows.Close()
	got := make([]teamSemanticMaterialization, 0, len(want))
	for rows.Next() {
		var item teamSemanticMaterialization
		if err := rows.Scan(
			&item.IntentID, &item.JobID, &item.RootObjectID, &item.RootGeneration,
			&item.DerivativeObjectID, &item.DerivativeGeneration,
			&item.StoreID, &item.TeamID, &item.ScopeType, &item.ScopeID,
			&item.ProjectionKind, &item.SemanticKeyDigest,
			&item.PolicyDigest, &item.PayloadDigest,
		); err != nil {
			return err
		}
		got = append(got, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !reflect.DeepEqual(got, want) {
		return ErrConcealedNotFound
	}
	return nil
}

func insertTeamSemanticMaterializationsTx(
	ctx context.Context,
	tx *sql.Tx,
	job projectionCompletionJob,
	materializations []teamSemanticMaterialization,
	now time.Time,
) error {
	if len(materializations) == 0 || len(materializations) > maxProjectionOutputs ||
		!validTeamSemanticProjectionKind(job.ProjectionKind) {
		return ErrProjectionMaterializationFailed
	}
	source, err := loadTeamSemanticProjectionSourceWithQueryer(
		ctx, tx, job.JobID, job.ProjectionKind,
	)
	if err != nil || source.RootObjectID != job.RootObjectID ||
		source.RootGeneration != job.RootGeneration || source.StoreID != job.StoreID ||
		source.TeamID != job.TeamID || source.ScopeType != job.ScopeType ||
		source.ScopeID != job.ScopeID || len(materializations) != len(source.Intents) {
		return ErrProjectionMaterializationFailed
	}
	expected := teamSemanticMaterializationsForSource(source)
	for index := range expected {
		if materializations[index] != expected[index] {
			return ErrProjectionMaterializationFailed
		}
	}
	nowText := now.UTC().Format(time.RFC3339Nano)
	for _, materialization := range materializations {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO team_semantic_materializations(
				intent_id, job_id, root_object_id, root_generation,
				derivative_object_id, derivative_generation, store_id, team_id,
				scope_type, scope_id, projection_kind, semantic_key_digest,
				policy_digest, payload_digest, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			materialization.IntentID, materialization.JobID,
			materialization.RootObjectID, materialization.RootGeneration,
			materialization.DerivativeObjectID, materialization.DerivativeGeneration,
			materialization.StoreID, materialization.TeamID,
			materialization.ScopeType, materialization.ScopeID,
			materialization.ProjectionKind, materialization.SemanticKeyDigest,
			materialization.PolicyDigest, materialization.PayloadDigest, nowText,
		)
		if err != nil {
			return ErrProjectionMaterializationFailed
		}
		rows, err := result.RowsAffected()
		if err != nil || rows != 1 {
			return ErrProjectionMaterializationFailed
		}
	}
	return nil
}
