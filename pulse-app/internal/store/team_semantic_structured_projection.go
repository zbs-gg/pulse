package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"sort"
	"time"
)

type teamGraphMaterialization struct {
	IntentID, JobID, RootObjectID       string
	RootGeneration                      int64
	StoreID, TeamID, ScopeType, ScopeID string
	DerivativeObjectID, GraphKind       string
	PayloadJSON, ResolvedRefsJSON       string
	ContentDigest                       string
}

type teamAssertionMaterialization struct {
	IntentID, JobID, RootObjectID       string
	RootGeneration                      int64
	StoreID, TeamID, ScopeType, ScopeID string
	DerivativeObjectID, ClaimSlotDigest string
	ClaimJSON, SourceRefsJSON           string
	ContentDigest                       string
}

type teamContinuityMaterialization struct {
	IntentID, JobID, RootObjectID       string
	RootGeneration                      int64
	StoreID, TeamID, ScopeType, ScopeID string
	DerivativeObjectID                  string
	ThreadSlotDigest, SessionSlotDigest string
	CheckpointJSON, ContentDigest       string
}

func (s *Store) CompleteTeamGraphProjection(
	ctx context.Context,
	request TeamSemanticProjectionRequest,
) (TeamProjectionCompletionResult, error) {
	source, err := s.loadTeamSemanticProjectionSource(ctx, request.JobID, "graph")
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	outputs, err := teamSemanticProjectionOutputs(source, "semantic_graph", "")
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	common := teamSemanticMaterializationsForSource(source)
	graph, err := buildTeamGraphMaterializations(source)
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	if source.JobState == "ready" {
		if err := validateTeamSemanticCommonMaterializationsWithQueryer(ctx, s.db, source); err != nil {
			return TeamProjectionCompletionResult{}, err
		}
		if err := s.verifyTeamGraphMaterializations(ctx, source.JobID, graph); err != nil {
			return TeamProjectionCompletionResult{}, err
		}
	}
	result, err := s.completeTeamProjectionJobWithExtension(ctx, TeamProjectionCompletionRequest{
		WriterID: request.WriterID, WriterToken: request.WriterToken,
		JobID: request.JobID, LeaseToken: request.LeaseToken, Outputs: outputs,
	}, func(ctx context.Context, writer teamProjectionContentWriter, _ teamProjectionCompletionContext) error {
		if err := writer.InsertTeamSemanticMaterializations(ctx, common); err != nil {
			return err
		}
		for _, materialization := range graph {
			if err := writer.InsertTeamGraphMaterialization(ctx, materialization); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	if err := validateTeamSemanticCommonMaterializationsWithQueryer(ctx, s.db, source); err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	if err := s.verifyTeamGraphMaterializations(ctx, source.JobID, graph); err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	return result, nil
}

func (s *Store) CompleteTeamClaimProjection(
	ctx context.Context,
	request TeamSemanticProjectionRequest,
) (TeamProjectionCompletionResult, error) {
	source, err := s.loadTeamSemanticProjectionSource(ctx, request.JobID, "claim")
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	outputs, err := teamSemanticProjectionOutputs(source, "semantic_assertion", "")
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	common := teamSemanticMaterializationsForSource(source)
	assertions, err := buildTeamAssertionMaterializations(source)
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	if source.JobState == "ready" {
		if err := validateTeamSemanticCommonMaterializationsWithQueryer(ctx, s.db, source); err != nil {
			return TeamProjectionCompletionResult{}, err
		}
		if err := s.verifyTeamAssertionMaterializations(ctx, source.JobID, assertions); err != nil {
			return TeamProjectionCompletionResult{}, err
		}
	}
	result, err := s.completeTeamProjectionJobWithExtension(ctx, TeamProjectionCompletionRequest{
		WriterID: request.WriterID, WriterToken: request.WriterToken,
		JobID: request.JobID, LeaseToken: request.LeaseToken, Outputs: outputs,
	}, func(ctx context.Context, writer teamProjectionContentWriter, _ teamProjectionCompletionContext) error {
		if err := writer.InsertTeamSemanticMaterializations(ctx, common); err != nil {
			return err
		}
		for _, materialization := range assertions {
			if err := writer.InsertTeamAssertionMaterialization(ctx, materialization); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	if err := validateTeamSemanticCommonMaterializationsWithQueryer(ctx, s.db, source); err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	if err := s.verifyTeamAssertionMaterializations(ctx, source.JobID, assertions); err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	return result, nil
}

func (s *Store) CompleteTeamContinuityProjection(
	ctx context.Context,
	request TeamSemanticProjectionRequest,
) (TeamProjectionCompletionResult, error) {
	source, err := s.loadTeamSemanticProjectionSource(ctx, request.JobID, "continuity")
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	outputs, err := teamSemanticProjectionOutputs(source, "semantic_continuity", "")
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	common := teamSemanticMaterializationsForSource(source)
	continuity, err := buildTeamContinuityMaterializations(source)
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	if source.JobState == "ready" {
		if err := validateTeamSemanticCommonMaterializationsWithQueryer(ctx, s.db, source); err != nil {
			return TeamProjectionCompletionResult{}, err
		}
		if err := s.verifyTeamContinuityMaterializations(ctx, source.JobID, continuity); err != nil {
			return TeamProjectionCompletionResult{}, err
		}
	}
	result, err := s.completeTeamProjectionJobWithExtension(ctx, TeamProjectionCompletionRequest{
		WriterID: request.WriterID, WriterToken: request.WriterToken,
		JobID: request.JobID, LeaseToken: request.LeaseToken, Outputs: outputs,
	}, func(ctx context.Context, writer teamProjectionContentWriter, _ teamProjectionCompletionContext) error {
		if err := writer.InsertTeamSemanticMaterializations(ctx, common); err != nil {
			return err
		}
		for _, materialization := range continuity {
			if err := writer.InsertTeamContinuityMaterialization(ctx, materialization); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	if err := validateTeamSemanticCommonMaterializationsWithQueryer(ctx, s.db, source); err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	if err := s.verifyTeamContinuityMaterializations(ctx, source.JobID, continuity); err != nil {
		return TeamProjectionCompletionResult{}, err
	}
	return result, nil
}

func buildTeamGraphMaterializations(
	source teamSemanticProjectionSource,
) ([]teamGraphMaterialization, error) {
	materializations := make([]teamGraphMaterialization, 0, len(source.Intents))
	for _, intent := range source.Intents {
		payload, err := teamSemanticProjectionPayload(source, intent)
		if err != nil {
			return nil, err
		}
		refs, err := teamSemanticResolvedReferences(source, intent)
		if err != nil {
			return nil, err
		}
		refsJSON, err := json.Marshal(refs)
		if err != nil {
			return nil, ErrConcealedNotFound
		}
		materializations = append(materializations, teamGraphMaterialization{
			IntentID: intent.IntentID, JobID: source.JobID,
			RootObjectID: source.RootObjectID, RootGeneration: source.RootGeneration,
			StoreID: source.StoreID, TeamID: source.TeamID,
			ScopeType: source.ScopeType, ScopeID: source.ScopeID,
			DerivativeObjectID: intent.DerivativeObjectID, GraphKind: intent.DerivativeKind,
			PayloadJSON: string(payload), ResolvedRefsJSON: string(refsJSON),
			ContentDigest: intent.PayloadDigest,
		})
	}
	return materializations, nil
}

func buildTeamAssertionMaterializations(
	source teamSemanticProjectionSource,
) ([]teamAssertionMaterialization, error) {
	materializations := make([]teamAssertionMaterialization, 0, len(source.Intents))
	for _, intent := range source.Intents {
		if intent.SourceKind != "fact" || intent.DerivativeKind != "assertion" {
			return nil, ErrConcealedNotFound
		}
		payload, err := teamSemanticProjectionPayload(source, intent)
		if err != nil {
			return nil, err
		}
		refs, err := teamSemanticResolvedReferences(source, intent)
		if err != nil {
			return nil, err
		}
		refsJSON, err := json.Marshal(refs)
		if err != nil {
			return nil, ErrConcealedNotFound
		}
		materializations = append(materializations, teamAssertionMaterialization{
			IntentID: intent.IntentID, JobID: source.JobID,
			RootObjectID: source.RootObjectID, RootGeneration: source.RootGeneration,
			StoreID: source.StoreID, TeamID: source.TeamID,
			ScopeType: source.ScopeType, ScopeID: source.ScopeID,
			DerivativeObjectID: intent.DerivativeObjectID,
			ClaimSlotDigest:    intent.SemanticKeyDigest, ClaimJSON: string(payload),
			SourceRefsJSON: string(refsJSON), ContentDigest: intent.PayloadDigest,
		})
	}
	return materializations, nil
}

func buildTeamContinuityMaterializations(
	source teamSemanticProjectionSource,
) ([]teamContinuityMaterialization, error) {
	if source.Normalized.body.Continuity == nil || len(source.Intents) != 1 {
		return nil, ErrConcealedNotFound
	}
	intent := source.Intents[0]
	if intent.SourceKind != "continuity" || intent.SourceOrdinal != 0 ||
		intent.DerivativeKind != "continuity_checkpoint" {
		return nil, ErrConcealedNotFound
	}
	payload, err := teamSemanticProjectionPayload(source, intent)
	if err != nil {
		return nil, err
	}
	continuity := source.Normalized.body.Continuity
	return []teamContinuityMaterialization{{
		IntentID: intent.IntentID, JobID: source.JobID,
		RootObjectID: source.RootObjectID, RootGeneration: source.RootGeneration,
		StoreID: source.StoreID, TeamID: source.TeamID,
		ScopeType: source.ScopeType, ScopeID: source.ScopeID,
		DerivativeObjectID: intent.DerivativeObjectID,
		ThreadSlotDigest: teamGraphDigestParts(
			"pulse-team-continuity-thread-slot-v1", source.StoreID, source.TeamID,
			source.ScopeType, source.ScopeID, continuity.ThreadID,
		),
		SessionSlotDigest: teamGraphDigestParts(
			"pulse-team-continuity-session-slot-v1", source.StoreID, source.TeamID,
			source.ScopeType, source.ScopeID, continuity.SessionID,
		),
		CheckpointJSON: string(payload), ContentDigest: intent.PayloadDigest,
	}}, nil
}

func teamSemanticProjectionPayload(
	source teamSemanticProjectionSource,
	intent teamSemanticProjectionIntent,
) ([]byte, error) {
	var value any
	switch intent.SourceKind {
	case "node":
		if intent.SourceOrdinal < 0 || intent.SourceOrdinal >= len(source.Normalized.body.Nodes) {
			return nil, ErrConcealedNotFound
		}
		value = source.Normalized.body.Nodes[intent.SourceOrdinal]
	case "edge":
		if intent.SourceOrdinal < 0 || intent.SourceOrdinal >= len(source.Normalized.body.Edges) {
			return nil, ErrConcealedNotFound
		}
		value = source.Normalized.body.Edges[intent.SourceOrdinal]
	case "fact":
		if intent.SourceOrdinal < 0 || intent.SourceOrdinal >= len(source.Normalized.body.Facts) {
			return nil, ErrConcealedNotFound
		}
		value = source.Normalized.body.Facts[intent.SourceOrdinal]
	case "event":
		if intent.SourceOrdinal < 0 || intent.SourceOrdinal >= len(source.Normalized.body.Events) {
			return nil, ErrConcealedNotFound
		}
		value = source.Normalized.body.Events[intent.SourceOrdinal]
	case "continuity":
		if intent.SourceOrdinal != 0 || source.Normalized.body.Continuity == nil {
			return nil, ErrConcealedNotFound
		}
		value = *source.Normalized.body.Continuity
	default:
		return nil, ErrConcealedNotFound
	}
	digest, ok := teamGraphPayloadDigest(value)
	if !ok || digest != intent.PayloadDigest {
		return nil, ErrConcealedNotFound
	}
	payload, err := marshalTeamGraphCanonical(value)
	if err != nil {
		return nil, ErrConcealedNotFound
	}
	return payload, nil
}

func teamSemanticResolvedReferences(
	source teamSemanticProjectionSource,
	intent teamSemanticProjectionIntent,
) ([]string, error) {
	nodeDerivative := func(clientID string) (string, bool) {
		for ordinal, node := range source.Normalized.body.Nodes {
			if node.ClientID == clientID {
				return teamSemanticDescriptorDerivative(source, "graph", "node", ordinal)
			}
		}
		return "", false
	}
	eventDerivative := func(clientID string) (string, bool) {
		for ordinal, event := range source.Normalized.body.Events {
			if event.ClientID == clientID {
				return teamSemanticDescriptorDerivative(source, "graph", "event", ordinal)
			}
		}
		return "", false
	}

	refs := []string{}
	switch intent.SourceKind {
	case "node", "continuity":
		return refs, nil
	case "edge":
		if intent.SourceOrdinal < 0 || intent.SourceOrdinal >= len(source.Normalized.body.Edges) {
			return nil, ErrConcealedNotFound
		}
		edge := source.Normalized.body.Edges[intent.SourceOrdinal]
		from, fromOK := nodeDerivative(edge.From)
		to, toOK := nodeDerivative(edge.To)
		if !fromOK || !toOK {
			return nil, ErrConcealedNotFound
		}
		return []string{from, to}, nil
	case "fact":
		if intent.SourceOrdinal < 0 || intent.SourceOrdinal >= len(source.Normalized.body.Facts) {
			return nil, ErrConcealedNotFound
		}
		fact := source.Normalized.body.Facts[intent.SourceOrdinal]
		node, ok := nodeDerivative(fact.Node)
		if !ok {
			return nil, ErrConcealedNotFound
		}
		refs = append(refs, node)
		if fact.SourceEventRefs != nil {
			events := make([]string, 0, len(*fact.SourceEventRefs))
			for _, ref := range *fact.SourceEventRefs {
				derivative, ok := eventDerivative(ref)
				if !ok {
					return nil, ErrConcealedNotFound
				}
				events = append(events, derivative)
			}
			sort.Strings(events)
			refs = append(refs, events...)
		}
		return refs, nil
	case "event":
		if intent.SourceOrdinal < 0 || intent.SourceOrdinal >= len(source.Normalized.body.Events) {
			return nil, ErrConcealedNotFound
		}
		for _, ref := range source.Normalized.body.Events[intent.SourceOrdinal].EntityRefs {
			derivative, ok := nodeDerivative(ref)
			if !ok {
				return nil, ErrConcealedNotFound
			}
			refs = append(refs, derivative)
		}
		sort.Strings(refs)
		return refs, nil
	default:
		return nil, ErrConcealedNotFound
	}
}

func teamSemanticDescriptorDerivative(
	source teamSemanticProjectionSource,
	projectionKind, sourceKind string,
	ordinal int,
) (string, bool) {
	for _, descriptor := range source.Normalized.intentDescriptors {
		if descriptor.ProjectionKind == projectionKind && descriptor.SourceKind == sourceKind &&
			descriptor.SourceOrdinal == ordinal {
			return descriptor.DerivativeObjectID, true
		}
	}
	return "", false
}

func validTeamGraphMaterialization(
	job projectionCompletionJob,
	materialization teamGraphMaterialization,
) bool {
	if job.ProjectionKind != "graph" || materialization.JobID != job.JobID ||
		materialization.RootObjectID != job.RootObjectID ||
		materialization.RootGeneration != job.RootGeneration ||
		materialization.StoreID != job.StoreID || materialization.TeamID != job.TeamID ||
		materialization.ScopeType != job.ScopeType || materialization.ScopeID != job.ScopeID ||
		!validProjectionOpaque(materialization.IntentID, 255) ||
		!validProjectionOpaque(materialization.DerivativeObjectID, 255) ||
		!lowerHexDigest(materialization.ContentDigest) || !json.Valid([]byte(materialization.PayloadJSON)) ||
		!json.Valid([]byte(materialization.ResolvedRefsJSON)) {
		return false
	}
	switch materialization.GraphKind {
	case "graph_entity", "graph_relation", "graph_fact", "graph_event":
	default:
		return false
	}
	var payload map[string]any
	var refs []string
	return json.Unmarshal([]byte(materialization.PayloadJSON), &payload) == nil &&
		json.Unmarshal([]byte(materialization.ResolvedRefsJSON), &refs) == nil
}

func validTeamAssertionMaterialization(
	job projectionCompletionJob,
	materialization teamAssertionMaterialization,
) bool {
	if job.ProjectionKind != "claim" || materialization.JobID != job.JobID ||
		materialization.RootObjectID != job.RootObjectID ||
		materialization.RootGeneration != job.RootGeneration ||
		materialization.StoreID != job.StoreID || materialization.TeamID != job.TeamID ||
		materialization.ScopeType != job.ScopeType || materialization.ScopeID != job.ScopeID ||
		!validProjectionOpaque(materialization.IntentID, 255) ||
		!validProjectionOpaque(materialization.DerivativeObjectID, 255) ||
		!lowerHexDigest(materialization.ClaimSlotDigest) ||
		!lowerHexDigest(materialization.ContentDigest) || !json.Valid([]byte(materialization.ClaimJSON)) ||
		!json.Valid([]byte(materialization.SourceRefsJSON)) {
		return false
	}
	var claim map[string]any
	var refs []string
	return json.Unmarshal([]byte(materialization.ClaimJSON), &claim) == nil &&
		json.Unmarshal([]byte(materialization.SourceRefsJSON), &refs) == nil
}

func validTeamContinuityMaterialization(
	job projectionCompletionJob,
	materialization teamContinuityMaterialization,
) bool {
	if job.ProjectionKind != "continuity" || materialization.JobID != job.JobID ||
		materialization.RootObjectID != job.RootObjectID ||
		materialization.RootGeneration != job.RootGeneration ||
		materialization.StoreID != job.StoreID || materialization.TeamID != job.TeamID ||
		materialization.ScopeType != job.ScopeType || materialization.ScopeID != job.ScopeID ||
		!validProjectionOpaque(materialization.IntentID, 255) ||
		!validProjectionOpaque(materialization.DerivativeObjectID, 255) ||
		!lowerHexDigest(materialization.ThreadSlotDigest) ||
		!lowerHexDigest(materialization.SessionSlotDigest) ||
		!lowerHexDigest(materialization.ContentDigest) ||
		!json.Valid([]byte(materialization.CheckpointJSON)) {
		return false
	}
	var checkpoint map[string]any
	return json.Unmarshal([]byte(materialization.CheckpointJSON), &checkpoint) == nil
}

func insertTeamGraphMaterializationTx(
	ctx context.Context,
	tx *sql.Tx,
	job projectionCompletionJob,
	materialization teamGraphMaterialization,
	now time.Time,
) error {
	if !validTeamGraphMaterialization(job, materialization) {
		return ErrProjectionMaterializationFailed
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO team_graph_materializations(
			intent_id, store_id, team_id, scope_type, scope_id,
			derivative_object_id, graph_kind, payload_json, resolved_refs_json,
			content_digest, created_at)
		SELECT common.intent_id, common.store_id, common.team_id,
		       common.scope_type, common.scope_id, common.derivative_object_id,
		       intent.derivative_kind, ?, ?, common.payload_digest, ?
		  FROM team_semantic_materializations common
		  JOIN team_semantic_projection_intents intent ON intent.intent_id = common.intent_id
		 WHERE common.intent_id = ? AND common.job_id = ?
		   AND common.root_object_id = ? AND common.root_generation = ?
		   AND common.store_id = ? AND common.team_id = ?
		   AND common.scope_type = ? AND common.scope_id = ?
		   AND common.projection_kind = 'graph'
		   AND common.derivative_object_id = ? AND common.payload_digest = ?
		   AND intent.derivative_kind = ? AND intent.payload_digest = common.payload_digest`,
		materialization.PayloadJSON, materialization.ResolvedRefsJSON,
		now.UTC().Format(time.RFC3339Nano), materialization.IntentID,
		materialization.JobID, materialization.RootObjectID, materialization.RootGeneration,
		materialization.StoreID, materialization.TeamID, materialization.ScopeType,
		materialization.ScopeID, materialization.DerivativeObjectID,
		materialization.ContentDigest, materialization.GraphKind,
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

func insertTeamAssertionMaterializationTx(
	ctx context.Context,
	tx *sql.Tx,
	job projectionCompletionJob,
	materialization teamAssertionMaterialization,
	now time.Time,
) error {
	if !validTeamAssertionMaterialization(job, materialization) {
		return ErrProjectionMaterializationFailed
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO team_assertion_materializations(
			intent_id, store_id, team_id, scope_type, scope_id,
			derivative_object_id, claim_slot_digest, claim_json,
			source_refs_json, content_digest, created_at)
		SELECT common.intent_id, common.store_id, common.team_id,
		       common.scope_type, common.scope_id, common.derivative_object_id,
		       common.semantic_key_digest, ?, ?, common.payload_digest, ?
		  FROM team_semantic_materializations common
		  JOIN team_semantic_projection_intents intent ON intent.intent_id = common.intent_id
		 WHERE common.intent_id = ? AND common.job_id = ?
		   AND common.root_object_id = ? AND common.root_generation = ?
		   AND common.store_id = ? AND common.team_id = ?
		   AND common.scope_type = ? AND common.scope_id = ?
		   AND common.projection_kind = 'claim'
		   AND common.derivative_object_id = ?
		   AND common.semantic_key_digest = ? AND common.payload_digest = ?
		   AND intent.derivative_kind = 'assertion'
		   AND intent.payload_digest = common.payload_digest`,
		materialization.ClaimJSON, materialization.SourceRefsJSON,
		now.UTC().Format(time.RFC3339Nano), materialization.IntentID,
		materialization.JobID, materialization.RootObjectID, materialization.RootGeneration,
		materialization.StoreID, materialization.TeamID, materialization.ScopeType,
		materialization.ScopeID, materialization.DerivativeObjectID,
		materialization.ClaimSlotDigest, materialization.ContentDigest,
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

func insertTeamContinuityMaterializationTx(
	ctx context.Context,
	tx *sql.Tx,
	job projectionCompletionJob,
	materialization teamContinuityMaterialization,
	now time.Time,
) error {
	if !validTeamContinuityMaterialization(job, materialization) {
		return ErrProjectionMaterializationFailed
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO team_continuity_materializations(
			intent_id, store_id, team_id, scope_type, scope_id,
			derivative_object_id, thread_slot_digest, session_slot_digest,
			checkpoint_json, content_digest, created_at)
		SELECT common.intent_id, common.store_id, common.team_id,
		       common.scope_type, common.scope_id, common.derivative_object_id,
		       ?, ?, ?, common.payload_digest, ?
		  FROM team_semantic_materializations common
		  JOIN team_semantic_projection_intents intent ON intent.intent_id = common.intent_id
		 WHERE common.intent_id = ? AND common.job_id = ?
		   AND common.root_object_id = ? AND common.root_generation = ?
		   AND common.store_id = ? AND common.team_id = ?
		   AND common.scope_type = ? AND common.scope_id = ?
		   AND common.projection_kind = 'continuity'
		   AND common.derivative_object_id = ? AND common.payload_digest = ?
		   AND intent.derivative_kind = 'continuity_checkpoint'
		   AND intent.payload_digest = common.payload_digest`,
		materialization.ThreadSlotDigest, materialization.SessionSlotDigest,
		materialization.CheckpointJSON, now.UTC().Format(time.RFC3339Nano),
		materialization.IntentID, materialization.JobID, materialization.RootObjectID,
		materialization.RootGeneration, materialization.StoreID, materialization.TeamID,
		materialization.ScopeType, materialization.ScopeID,
		materialization.DerivativeObjectID, materialization.ContentDigest,
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

func (s *Store) verifyTeamGraphMaterializations(
	ctx context.Context,
	jobID string,
	want []teamGraphMaterialization,
) error {
	return verifyTeamGraphMaterializationsWithQueryer(ctx, s.db, jobID, want)
}

func verifyTeamGraphMaterializationsWithQueryer(
	ctx context.Context,
	q teamGraphIntegrityQueryer,
	jobID string,
	want []teamGraphMaterialization,
) error {
	rows, err := q.QueryContext(ctx, `
		SELECT common.intent_id, common.job_id, common.root_object_id,
		       common.root_generation, common.store_id, common.team_id,
		       common.scope_type, common.scope_id, common.derivative_object_id,
		       graph.graph_kind, graph.payload_json, graph.resolved_refs_json,
		       graph.content_digest
		  FROM team_semantic_materializations common
		  JOIN team_graph_materializations graph
		    ON graph.intent_id = common.intent_id
		   AND graph.store_id = common.store_id AND graph.team_id = common.team_id
		   AND graph.scope_type = common.scope_type AND graph.scope_id = common.scope_id
		   AND graph.derivative_object_id = common.derivative_object_id
		  JOIN team_projection_outputs output
		    ON output.job_id = common.job_id
		   AND output.derivative_object_id = common.derivative_object_id
		   AND output.derivative_generation = common.derivative_generation
		  JOIN team_object_contributions contribution
		    ON contribution.parent_object_id = common.root_object_id
		   AND contribution.derivative_object_id = common.derivative_object_id
		   AND contribution.parent_generation = common.root_generation
		   AND contribution.derivative_generation = common.derivative_generation
		   AND contribution.team_id = common.team_id
		   AND contribution.scope_type = common.scope_type
		   AND contribution.scope_id = common.scope_id
		  JOIN team_object_registry derivative
		    ON derivative.object_id = common.derivative_object_id
		   AND derivative.store_id = common.store_id AND derivative.team_id = common.team_id
		   AND derivative.scope_type = common.scope_type AND derivative.scope_id = common.scope_id
		   AND derivative.object_kind = graph.graph_kind
		   AND derivative.generation = common.derivative_generation
		   AND derivative.lifecycle = 'active'
		 WHERE common.job_id = ? ORDER BY common.intent_id`, jobID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var got []teamGraphMaterialization
	for rows.Next() {
		var item teamGraphMaterialization
		if err := rows.Scan(
			&item.IntentID, &item.JobID, &item.RootObjectID, &item.RootGeneration,
			&item.StoreID, &item.TeamID, &item.ScopeType, &item.ScopeID,
			&item.DerivativeObjectID, &item.GraphKind, &item.PayloadJSON,
			&item.ResolvedRefsJSON, &item.ContentDigest,
		); err != nil {
			return err
		}
		got = append(got, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sort.Slice(want, func(i, j int) bool { return want[i].IntentID < want[j].IntentID })
	if !reflect.DeepEqual(got, want) {
		return ErrConcealedNotFound
	}
	for _, materialization := range want {
		if err := verifyTeamStructuredStorageMapping(
			ctx, q, materialization.DerivativeObjectID,
			materialization.TeamID, materialization.ScopeType, materialization.ScopeID,
			"semantic_graph", materialization.IntentID,
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) verifyTeamAssertionMaterializations(
	ctx context.Context,
	jobID string,
	want []teamAssertionMaterialization,
) error {
	return verifyTeamAssertionMaterializationsWithQueryer(ctx, s.db, jobID, want)
}

func verifyTeamAssertionMaterializationsWithQueryer(
	ctx context.Context,
	q teamGraphIntegrityQueryer,
	jobID string,
	want []teamAssertionMaterialization,
) error {
	rows, err := q.QueryContext(ctx, `
		SELECT common.intent_id, common.job_id, common.root_object_id,
		       common.root_generation, common.store_id, common.team_id,
		       common.scope_type, common.scope_id, common.derivative_object_id,
		       assertion.claim_slot_digest, assertion.claim_json,
		       assertion.source_refs_json, assertion.content_digest
		  FROM team_semantic_materializations common
		  JOIN team_assertion_materializations assertion
		    ON assertion.intent_id = common.intent_id
		   AND assertion.store_id = common.store_id AND assertion.team_id = common.team_id
		   AND assertion.scope_type = common.scope_type AND assertion.scope_id = common.scope_id
		   AND assertion.derivative_object_id = common.derivative_object_id
		  JOIN team_projection_outputs output
		    ON output.job_id = common.job_id
		   AND output.derivative_object_id = common.derivative_object_id
		   AND output.derivative_generation = common.derivative_generation
		  JOIN team_object_contributions contribution
		    ON contribution.parent_object_id = common.root_object_id
		   AND contribution.derivative_object_id = common.derivative_object_id
		   AND contribution.parent_generation = common.root_generation
		   AND contribution.derivative_generation = common.derivative_generation
		   AND contribution.team_id = common.team_id
		   AND contribution.scope_type = common.scope_type
		   AND contribution.scope_id = common.scope_id
		  JOIN team_object_registry derivative
		    ON derivative.object_id = common.derivative_object_id
		   AND derivative.store_id = common.store_id AND derivative.team_id = common.team_id
		   AND derivative.scope_type = common.scope_type AND derivative.scope_id = common.scope_id
		   AND derivative.object_kind = 'assertion'
		   AND derivative.generation = common.derivative_generation
		   AND derivative.lifecycle = 'active'
		 WHERE common.job_id = ? ORDER BY common.intent_id`, jobID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var got []teamAssertionMaterialization
	for rows.Next() {
		var item teamAssertionMaterialization
		if err := rows.Scan(
			&item.IntentID, &item.JobID, &item.RootObjectID, &item.RootGeneration,
			&item.StoreID, &item.TeamID, &item.ScopeType, &item.ScopeID,
			&item.DerivativeObjectID, &item.ClaimSlotDigest, &item.ClaimJSON,
			&item.SourceRefsJSON, &item.ContentDigest,
		); err != nil {
			return err
		}
		got = append(got, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sort.Slice(want, func(i, j int) bool { return want[i].IntentID < want[j].IntentID })
	if !reflect.DeepEqual(got, want) {
		return ErrConcealedNotFound
	}
	for _, materialization := range want {
		if err := verifyTeamStructuredStorageMapping(
			ctx, q, materialization.DerivativeObjectID,
			materialization.TeamID, materialization.ScopeType, materialization.ScopeID,
			"semantic_assertion", materialization.IntentID,
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) verifyTeamContinuityMaterializations(
	ctx context.Context,
	jobID string,
	want []teamContinuityMaterialization,
) error {
	return verifyTeamContinuityMaterializationsWithQueryer(ctx, s.db, jobID, want)
}

func verifyTeamContinuityMaterializationsWithQueryer(
	ctx context.Context,
	q teamGraphIntegrityQueryer,
	jobID string,
	want []teamContinuityMaterialization,
) error {
	rows, err := q.QueryContext(ctx, `
		SELECT common.intent_id, common.job_id, common.root_object_id,
		       common.root_generation, common.store_id, common.team_id,
		       common.scope_type, common.scope_id, common.derivative_object_id,
		       continuity.thread_slot_digest, continuity.session_slot_digest,
		       continuity.checkpoint_json, continuity.content_digest
		  FROM team_semantic_materializations common
		  JOIN team_continuity_materializations continuity
		    ON continuity.intent_id = common.intent_id
		   AND continuity.store_id = common.store_id AND continuity.team_id = common.team_id
		   AND continuity.scope_type = common.scope_type AND continuity.scope_id = common.scope_id
		   AND continuity.derivative_object_id = common.derivative_object_id
		  JOIN team_projection_outputs output
		    ON output.job_id = common.job_id
		   AND output.derivative_object_id = common.derivative_object_id
		   AND output.derivative_generation = common.derivative_generation
		  JOIN team_object_contributions contribution
		    ON contribution.parent_object_id = common.root_object_id
		   AND contribution.derivative_object_id = common.derivative_object_id
		   AND contribution.parent_generation = common.root_generation
		   AND contribution.derivative_generation = common.derivative_generation
		   AND contribution.team_id = common.team_id
		   AND contribution.scope_type = common.scope_type
		   AND contribution.scope_id = common.scope_id
		  JOIN team_object_registry derivative
		    ON derivative.object_id = common.derivative_object_id
		   AND derivative.store_id = common.store_id AND derivative.team_id = common.team_id
		   AND derivative.scope_type = common.scope_type AND derivative.scope_id = common.scope_id
		   AND derivative.object_kind = 'continuity_checkpoint'
		   AND derivative.generation = common.derivative_generation
		   AND derivative.lifecycle = 'active'
		 WHERE common.job_id = ? ORDER BY common.intent_id`, jobID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var got []teamContinuityMaterialization
	for rows.Next() {
		var item teamContinuityMaterialization
		if err := rows.Scan(
			&item.IntentID, &item.JobID, &item.RootObjectID, &item.RootGeneration,
			&item.StoreID, &item.TeamID, &item.ScopeType, &item.ScopeID,
			&item.DerivativeObjectID, &item.ThreadSlotDigest, &item.SessionSlotDigest,
			&item.CheckpointJSON, &item.ContentDigest,
		); err != nil {
			return err
		}
		got = append(got, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sort.Slice(want, func(i, j int) bool { return want[i].IntentID < want[j].IntentID })
	if !reflect.DeepEqual(got, want) {
		return ErrConcealedNotFound
	}
	for _, materialization := range want {
		if err := verifyTeamStructuredStorageMapping(
			ctx, q, materialization.DerivativeObjectID,
			materialization.TeamID, materialization.ScopeType, materialization.ScopeID,
			"semantic_continuity", materialization.IntentID,
		); err != nil {
			return err
		}
	}
	return nil
}

func verifyTeamStructuredStorageMapping(
	ctx context.Context,
	q teamGraphIntegrityQueryer,
	derivativeObjectID, teamID, scopeType, scopeID, representationKind, intentID string,
) error {
	var count int
	if err := q.QueryRowContext(ctx, `
		SELECT count(*) FROM team_object_storage_map
		 WHERE object_id = ? AND team_id = ? AND scope_type = ? AND scope_id = ?
		   AND generation = 1 AND representation_kind = ? AND storage_key = ?`,
		derivativeObjectID, teamID, scopeType, scopeID, representationKind,
		teamSemanticProjectionStorageKey(intentID, ""),
	).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return ErrConcealedNotFound
	}
	return nil
}
