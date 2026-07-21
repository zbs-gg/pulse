package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrInvalidTeamReadQuery = errors.New("invalid team read query")

const (
	defaultAuthorizedTeamReadLimit = 100
	maxAuthorizedTeamReadLimit     = 1000
	maxAuthorizedTeamReadMatch     = 512
	// MaxTeamSemanticEmbeddingReadSources keeps two binds per exact key plus
	// policy/model binds below SQLite's conservative 999-variable ceiling.
	MaxTeamSemanticEmbeddingReadSources = 400
)

// TeamTextReadQuery is intentionally small and storage-oriented. Matching is
// a bounded case-insensitive substring over an already-authorized corpus; FTS,
// ranking, expansion, and cross-request caching belong to later layers.
type TeamTextReadQuery struct {
	Match string
	Limit int
}

type TeamSemanticEmbeddingReadKey struct {
	RootObjectID                  string
	SourceGraphDerivativeObjectID string
}

// TeamSemanticEmbeddingReadQuery is keyed by graph contributions already
// selected under the authorized graph predicate. Vector lookup cannot run an
// independent limit/order pass which might select unrelated graph sources.
type TeamSemanticEmbeddingReadQuery struct {
	Model   string
	Sources []TeamSemanticEmbeddingReadKey
}

type TeamMemoryCapsuleReadKey struct {
	RootObjectID string
	CapsuleID    string
}

// TeamMemoryEmbeddingReadQuery is keyed by the already-selected authorized
// recall rows. Memory vectors never run an independent limit/order pass which
// could select a disjoint capsule set.
type TeamMemoryEmbeddingReadQuery struct {
	Model    string
	Capsules []TeamMemoryCapsuleReadKey
}

type TeamAuthorizedMemoryCapsule struct {
	RootObjectID             string
	CapsuleID                string
	PartitionKey             string
	Kind                     string
	RedactedSummary          string
	Confidence               float64
	EvidenceHint             string
	PrivacyTier              string
	Retention                string
	Tags                     []string
	SourceHost               string
	ConversationScope        string
	SourceTimestamp          string
	CreatedAt                string
	PublicationID            string
	PublicationEventObjectID string
	PublicationCreatedAt     string
}

type TeamAuthorizedMemoryEmbedding struct {
	RootObjectID       string
	DerivativeObjectID string
	CapsuleID          string
	EmbeddingID        string
	PartitionKey       string
	PrivacyTier        string
	Retention          string
	Model              string
	Vector             []float32
	VectorDigest       string
	ContentDigest      string
	CreatedAt          string
}

type TeamAuthorizedGraphContribution struct {
	RootObjectID             string
	DerivativeObjectID       string
	IntentID                 string
	PartitionKey             string
	GraphKind                string
	Node                     *TeamGraphNode
	Edge                     *TeamGraphEdge
	Fact                     *TeamGraphFact
	Event                    *TeamGraphEvent
	ResolvedRefs             []string
	ContentDigest            string
	CreatedAt                string
	VisibleContributionCount int
}

type TeamAuthorizedAssertionContribution struct {
	RootObjectID                  string
	DerivativeObjectID            string
	SourceGraphDerivativeObjectID string
	IntentID                      string
	PartitionKey                  string
	ClaimSlotDigest               string
	Claim                         TeamGraphFact
	SourceRefs                    []string
	ContentDigest                 string
	CreatedAt                     string
	VisibleContributionCount      int
}

type TeamAuthorizedContinuityCheckpoint struct {
	RootObjectID             string
	DerivativeObjectID       string
	IntentID                 string
	PartitionKey             string
	ThreadSlotDigest         string
	SessionSlotDigest        string
	Checkpoint               TeamGraphContinuity
	ContentDigest            string
	CreatedAt                string
	VisibleContributionCount int
}

type TeamAuthorizedSemanticEmbedding struct {
	RootObjectID                  string
	DerivativeObjectID            string
	SourceGraphDerivativeObjectID string
	IntentID                      string
	PartitionKey                  string
	SourceKind                    string
	Model                         string
	Vector                        []float32
	VectorDigest                  string
	ContentDigest                 string
	SemanticKeyDigest             string
	PolicyDigest                  string
	CreatedAt                     string
	VisibleContributionCount      int
}

func (s *Store) QueryAuthorizedTeamMemoryCapsules(
	ctx context.Context,
	filter AuthorizedCandidateFilter,
	query TeamTextReadQuery,
) ([]TeamAuthorizedMemoryCapsule, error) {
	match, limit, err := normalizeTeamTextReadQuery(query)
	if err != nil {
		return nil, err
	}
	predicate, predicateArgs, err := filter.sqlPredicateAt("root", s.clock().UTC())
	if err != nil {
		return nil, err
	}
	args := append([]any{}, predicateArgs...)
	args = append(args, match, strings.ToLower(match), limit)
	rows, err := s.db.QueryContext(ctx, `
		WITH authorized AS MATERIALIZED (
			SELECT capsule.root_object_id, capsule.capsule_id, capsule.team_id,
			       capsule.scope_type, capsule.scope_id, capsule.kind,
			       capsule.redacted_summary, capsule.confidence,
			       capsule.evidence_hint, root.privacy_tier, root.retention,
			       capsule.tags_json, capsule.source_host,
			       capsule.conversation_scope, capsule.source_timestamp,
			       capsule.created_at,
			       COALESCE(publication.publication_id, '') publication_id,
			       COALESCE(publication.event_object_id, '') event_object_id,
			       COALESCE(publication.publication_created_at, '') publication_created_at
			  FROM team_memory_capsules capsule
			  JOIN team_object_registry root
			    ON root.object_id = capsule.root_object_id
			   AND root.team_id = capsule.team_id
			   AND root.scope_type = capsule.scope_type
			   AND root.scope_id = capsule.scope_id
			   AND root.generation = capsule.root_generation
			   AND root.object_kind = 'memory'
			  LEFT JOIN (
			      SELECT receipt.object_id, receipt.capsule_id, receipt.publication_id,
			             receipt.created_at AS publication_created_at,
			             event.derivative_object_id AS event_object_id
			        FROM team_publication_receipts receipt
			        JOIN team_projection_jobs job
			          ON job.job_id = receipt.event_projection_job_id
			         AND job.root_object_id = receipt.object_id
			         AND job.projection_kind = 'event' AND job.state = 'ready'
			        JOIN team_memory_events event
			          ON event.job_id = job.job_id
			         AND event.root_object_id = receipt.object_id
			         AND event.capsule_id = receipt.capsule_id
			  ) publication
			    ON publication.object_id = capsule.root_object_id
			   AND publication.capsule_id = capsule.capsule_id
			 WHERE `+predicate+`
		)
		SELECT root_object_id, capsule_id, team_id, scope_type, scope_id,
		       kind, redacted_summary, confidence, evidence_hint,
		       privacy_tier, retention, tags_json,
		       source_host, conversation_scope, source_timestamp, created_at,
		       publication_id, event_object_id, publication_created_at
		  FROM authorized
		 WHERE ? = '' OR instr(
		       lower(redacted_summary || ' ' || kind || ' ' || tags_json), ?
		 ) > 0
		 ORDER BY confidence DESC, source_timestamp DESC, capsule_id
		 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]TeamAuthorizedMemoryCapsule, 0)
	rootIDs := make([]string, 0)
	for rows.Next() {
		var item TeamAuthorizedMemoryCapsule
		var teamID, scopeType, scopeID, tagsJSON string
		if err := rows.Scan(
			&item.RootObjectID, &item.CapsuleID, &teamID, &scopeType, &scopeID,
			&item.Kind, &item.RedactedSummary, &item.Confidence, &item.EvidenceHint,
			&item.PrivacyTier, &item.Retention, &tagsJSON,
			&item.SourceHost, &item.ConversationScope,
			&item.SourceTimestamp, &item.CreatedAt, &item.PublicationID,
			&item.PublicationEventObjectID, &item.PublicationCreatedAt,
		); err != nil {
			return nil, err
		}
		tags, ok := decodeAuthorizedStringList(tagsJSON)
		if !ok || !validProjectionOpaque(item.RootObjectID, 255) ||
			!validProjectionOpaque(item.CapsuleID, 255) ||
			item.Kind == "" || item.RedactedSummary == "" || item.EvidenceHint == "" ||
			!validPrivacyTier(item.PrivacyTier) || !validRetention(item.Retention) ||
			math.IsNaN(item.Confidence) || math.IsInf(item.Confidence, 0) ||
			item.Confidence < 0 || item.Confidence > 1 ||
			!validAuthorizedReadTime(item.SourceTimestamp) || !validAuthorizedReadTime(item.CreatedAt) {
			return nil, ErrTeamPolicyNotReady
		}
		if item.PublicationID != "" &&
			(!validProjectionOpaque(item.PublicationID, 255) ||
				!validProjectionOpaque(item.PublicationEventObjectID, 255) ||
				!validAuthorizedReadTime(item.PublicationCreatedAt)) {
			return nil, ErrTeamPolicyNotReady
		}
		item.Tags = tags
		item.PartitionKey = authorizedTeamPartitionKey(teamID, scopeType, scopeID)
		result = append(result, item)
		rootIDs = append(rootIDs, item.RootObjectID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.RecheckAuthorizedCandidateRoots(ctx, filter, rootIDs); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) QueryAuthorizedTeamMemoryEmbeddings(
	ctx context.Context,
	filter AuthorizedCandidateFilter,
	query TeamMemoryEmbeddingReadQuery,
) ([]TeamAuthorizedMemoryEmbedding, error) {
	model, capsules, err := normalizeTeamMemoryEmbeddingReadQuery(query)
	if err != nil {
		return nil, err
	}
	if len(capsules) == 0 {
		if err := s.RecheckAuthorizedCandidateFilter(ctx, filter); err != nil {
			return nil, err
		}
		return []TeamAuthorizedMemoryEmbedding{}, nil
	}
	predicate, predicateArgs, err := filter.sqlPredicateAt("root", s.clock().UTC())
	if err != nil {
		return nil, err
	}
	keyClauses := make([]string, len(capsules))
	args := append([]any{}, predicateArgs...)
	args = append(args, model)
	for index, capsule := range capsules {
		keyClauses[index] = "(embedding.root_object_id = ? AND embedding.capsule_id = ?)"
		args = append(args, capsule.RootObjectID, capsule.CapsuleID)
	}
	rows, err := s.db.QueryContext(ctx, `
		WITH authorized AS MATERIALIZED (
			SELECT embedding.root_object_id, embedding.derivative_object_id,
			       embedding.capsule_id, embedding.embedding_id,
			       embedding.team_id, embedding.scope_type, embedding.scope_id,
			       root.privacy_tier, root.retention, embedding.model,
			       embedding.dimensions, embedding.vector_json,
			       embedding.vector_digest, embedding.content_digest,
			       embedding.created_at
			  FROM team_memory_embeddings embedding
			  JOIN team_memory_capsules capsule
			    ON capsule.capsule_id = embedding.capsule_id
			   AND capsule.root_object_id = embedding.root_object_id
			   AND capsule.root_generation = embedding.root_generation
			   AND capsule.team_id = embedding.team_id
			   AND capsule.scope_type = embedding.scope_type
			   AND capsule.scope_id = embedding.scope_id
			  JOIN team_projection_jobs job
			    ON job.job_id = embedding.job_id AND job.state = 'ready'
			  JOIN team_object_registry root
			    ON root.object_id = embedding.root_object_id
			   AND root.team_id = embedding.team_id
			   AND root.scope_type = embedding.scope_type
			   AND root.scope_id = embedding.scope_id
			   AND root.generation = embedding.root_generation
			   AND root.object_kind = 'memory'
			  JOIN team_object_registry derivative
			    ON derivative.object_id = embedding.derivative_object_id
			   AND derivative.team_id = embedding.team_id
			   AND derivative.scope_type = embedding.scope_type
			   AND derivative.scope_id = embedding.scope_id
			   AND derivative.generation = 1
			   AND derivative.object_kind = 'embedding'
			   AND derivative.lifecycle = 'active'
			  JOIN team_projection_outputs output
			    ON output.job_id = embedding.job_id
			   AND output.derivative_object_id = embedding.derivative_object_id
			   AND output.derivative_generation = derivative.generation
			  JOIN team_object_contributions contribution
			    ON contribution.parent_object_id = embedding.root_object_id
			   AND contribution.derivative_object_id = embedding.derivative_object_id
			   AND contribution.parent_generation = embedding.root_generation
			   AND contribution.derivative_generation = derivative.generation
			   AND contribution.team_id = embedding.team_id
			   AND contribution.scope_type = embedding.scope_type
			   AND contribution.scope_id = embedding.scope_id
			 WHERE `+predicate+`
			   AND embedding.model = ?
			   AND (`+strings.Join(keyClauses, " OR ")+`)
		)
		SELECT root_object_id, derivative_object_id, capsule_id, embedding_id,
		       team_id, scope_type, scope_id, privacy_tier, retention, model,
		       dimensions, vector_json, vector_digest, content_digest, created_at
		  FROM authorized
		 ORDER BY root_object_id, capsule_id, derivative_object_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]TeamAuthorizedMemoryEmbedding, 0)
	rootIDs := make([]string, 0)
	for rows.Next() {
		var item TeamAuthorizedMemoryEmbedding
		var teamID, scopeType, scopeID, vectorJSON string
		var dimensions int
		if err := rows.Scan(
			&item.RootObjectID, &item.DerivativeObjectID, &item.CapsuleID,
			&item.EmbeddingID, &teamID, &scopeType, &scopeID,
			&item.PrivacyTier, &item.Retention, &item.Model, &dimensions,
			&vectorJSON, &item.VectorDigest, &item.ContentDigest, &item.CreatedAt,
		); err != nil {
			return nil, err
		}
		vector, ok := decodeAuthorizedVector(vectorJSON, item.VectorDigest, dimensions)
		if !ok || !validProjectionOpaque(item.RootObjectID, 255) ||
			!validProjectionOpaque(item.DerivativeObjectID, 255) ||
			!validProjectionOpaque(item.CapsuleID, 255) ||
			!validProjectionOpaque(item.EmbeddingID, 255) || item.Model != model ||
			!validPrivacyTier(item.PrivacyTier) || !validRetention(item.Retention) ||
			!lowerHexDigest(item.ContentDigest) || !validAuthorizedReadTime(item.CreatedAt) {
			return nil, ErrTeamPolicyNotReady
		}
		item.Vector = vector
		item.PartitionKey = authorizedTeamPartitionKey(teamID, scopeType, scopeID)
		result = append(result, item)
		rootIDs = append(rootIDs, item.RootObjectID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.RecheckAuthorizedCandidateRoots(ctx, filter, rootIDs); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) QueryAuthorizedTeamGraphContributions(
	ctx context.Context,
	filter AuthorizedCandidateFilter,
	query TeamTextReadQuery,
) ([]TeamAuthorizedGraphContribution, error) {
	match, limit, err := normalizeTeamTextReadQuery(query)
	if err != nil {
		return nil, err
	}
	predicate, predicateArgs, err := filter.sqlPredicateAt("root", s.clock().UTC())
	if err != nil {
		return nil, err
	}
	args := append([]any{}, predicateArgs...)
	args = append(args, match, strings.ToLower(match), limit)
	rows, err := s.db.QueryContext(ctx, `
		WITH authorized AS MATERIALIZED (
			SELECT common.root_object_id, common.derivative_object_id,
			       common.intent_id, common.team_id, common.scope_type,
			       common.scope_id, graph.graph_kind, graph.payload_json,
			       graph.resolved_refs_json, graph.content_digest, graph.created_at,
			       count(*) OVER (PARTITION BY common.derivative_object_id) visible_count
			  FROM team_semantic_materializations common
			  JOIN team_graph_materializations graph
			    ON graph.intent_id = common.intent_id
			   AND graph.store_id = common.store_id AND graph.team_id = common.team_id
			   AND graph.scope_type = common.scope_type AND graph.scope_id = common.scope_id
			   AND graph.derivative_object_id = common.derivative_object_id
			  JOIN team_projection_jobs job
			    ON job.job_id = common.job_id AND job.state = 'ready'
			  JOIN team_object_registry root
			    ON root.object_id = common.root_object_id
			   AND root.store_id = common.store_id AND root.team_id = common.team_id
			   AND root.scope_type = common.scope_type AND root.scope_id = common.scope_id
			   AND root.generation = common.root_generation
			   AND root.object_kind = 'graph_delta'
			  JOIN team_object_registry derivative
			    ON derivative.object_id = common.derivative_object_id
			   AND derivative.store_id = common.store_id AND derivative.team_id = common.team_id
			   AND derivative.scope_type = common.scope_type AND derivative.scope_id = common.scope_id
			   AND derivative.generation = common.derivative_generation
			   AND derivative.object_kind = graph.graph_kind AND derivative.lifecycle = 'active'
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
			 WHERE common.projection_kind = 'graph' AND `+predicate+`
		),
		selected_derivatives AS MATERIALIZED (
			SELECT derivative_object_id, max(created_at) selection_created_at
			  FROM authorized
			 WHERE ? = '' OR instr(lower(payload_json), ?) > 0
			 GROUP BY derivative_object_id
			 ORDER BY selection_created_at DESC, derivative_object_id
			 LIMIT ?
		)
		SELECT authorized.root_object_id, authorized.derivative_object_id,
		       authorized.intent_id, authorized.team_id, authorized.scope_type,
		       authorized.scope_id, authorized.graph_kind, authorized.payload_json,
		       authorized.resolved_refs_json, authorized.content_digest,
		       authorized.created_at, authorized.visible_count
		  FROM authorized
		  JOIN selected_derivatives selected
		    ON selected.derivative_object_id = authorized.derivative_object_id
		 ORDER BY selected.selection_created_at DESC, authorized.derivative_object_id,
		          authorized.created_at DESC, authorized.intent_id,
		          authorized.root_object_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]TeamAuthorizedGraphContribution, 0)
	rootIDs := make([]string, 0)
	for rows.Next() {
		var item TeamAuthorizedGraphContribution
		var teamID, scopeType, scopeID, payloadJSON, refsJSON string
		if err := rows.Scan(
			&item.RootObjectID, &item.DerivativeObjectID, &item.IntentID,
			&teamID, &scopeType, &scopeID, &item.GraphKind, &payloadJSON,
			&refsJSON, &item.ContentDigest, &item.CreatedAt,
			&item.VisibleContributionCount,
		); err != nil {
			return nil, err
		}
		refs, ok := decodeAuthorizedOpaqueReferences(refsJSON)
		if !ok || !decodeAuthorizedGraphPayload(payloadJSON, item.ContentDigest, &item) ||
			!validAuthorizedSemanticIdentity(item.RootObjectID, item.DerivativeObjectID, item.IntentID) ||
			!validAuthorizedReadTime(item.CreatedAt) || item.VisibleContributionCount < 1 {
			return nil, ErrTeamPolicyNotReady
		}
		item.ResolvedRefs = refs
		item.PartitionKey = authorizedTeamPartitionKey(teamID, scopeType, scopeID)
		result = append(result, item)
		rootIDs = append(rootIDs, item.RootObjectID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.RecheckAuthorizedCandidateRoots(ctx, filter, rootIDs); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) QueryAuthorizedTeamAssertionContributions(
	ctx context.Context,
	filter AuthorizedCandidateFilter,
	query TeamTextReadQuery,
) ([]TeamAuthorizedAssertionContribution, error) {
	match, limit, err := normalizeTeamTextReadQuery(query)
	if err != nil {
		return nil, err
	}
	predicate, predicateArgs, err := filter.sqlPredicateAt("root", s.clock().UTC())
	if err != nil {
		return nil, err
	}
	args := append([]any{}, predicateArgs...)
	args = append(args, match, strings.ToLower(match), limit)
	rows, err := s.db.QueryContext(ctx, `
		WITH authorized AS MATERIALIZED (
			SELECT common.root_object_id, common.derivative_object_id,
			       source_graph.derivative_object_id source_graph_derivative_object_id,
			       common.intent_id, common.team_id, common.scope_type,
			       common.scope_id, assertion.claim_slot_digest,
			       assertion.claim_json, assertion.source_refs_json,
			       assertion.content_digest, assertion.created_at,
			       count(*) OVER (PARTITION BY common.derivative_object_id) visible_count
			  FROM team_semantic_materializations common
			  JOIN team_assertion_materializations assertion
			    ON assertion.intent_id = common.intent_id
			   AND assertion.store_id = common.store_id AND assertion.team_id = common.team_id
			   AND assertion.scope_type = common.scope_type AND assertion.scope_id = common.scope_id
			   AND assertion.derivative_object_id = common.derivative_object_id
			  JOIN team_semantic_projection_intents claim_intent
			    ON claim_intent.intent_id = common.intent_id
			   AND claim_intent.root_object_id = common.root_object_id
			   AND claim_intent.root_generation = common.root_generation
			   AND claim_intent.store_id = common.store_id
			   AND claim_intent.team_id = common.team_id
			   AND claim_intent.scope_type = common.scope_type
			   AND claim_intent.scope_id = common.scope_id
			   AND claim_intent.projection_kind = 'claim'
			   AND claim_intent.source_kind = 'fact'
			  JOIN team_semantic_projection_intents source_graph
			    ON source_graph.root_object_id = claim_intent.root_object_id
			   AND source_graph.root_generation = claim_intent.root_generation
			   AND source_graph.store_id = claim_intent.store_id
			   AND source_graph.team_id = claim_intent.team_id
			   AND source_graph.scope_type = claim_intent.scope_type
			   AND source_graph.scope_id = claim_intent.scope_id
			   AND source_graph.source_kind = claim_intent.source_kind
			   AND source_graph.source_ordinal = claim_intent.source_ordinal
			   AND source_graph.projection_kind = 'graph'
			   AND source_graph.derivative_kind = 'graph_fact'
			  JOIN team_semantic_materializations source_common
			    ON source_common.intent_id = source_graph.intent_id
			   AND source_common.root_object_id = common.root_object_id
			   AND source_common.root_generation = common.root_generation
			   AND source_common.derivative_object_id = source_graph.derivative_object_id
			   AND source_common.projection_kind = 'graph'
			  JOIN team_graph_materializations source_fact
			    ON source_fact.intent_id = source_common.intent_id
			   AND source_fact.store_id = source_common.store_id
			   AND source_fact.team_id = source_common.team_id
			   AND source_fact.scope_type = source_common.scope_type
			   AND source_fact.scope_id = source_common.scope_id
			   AND source_fact.derivative_object_id = source_common.derivative_object_id
			   AND source_fact.graph_kind = 'graph_fact'
			  JOIN team_projection_jobs source_job
			    ON source_job.job_id = source_common.job_id AND source_job.state = 'ready'
			  JOIN team_object_registry source_derivative
			    ON source_derivative.object_id = source_common.derivative_object_id
			   AND source_derivative.store_id = source_common.store_id
			   AND source_derivative.team_id = source_common.team_id
			   AND source_derivative.scope_type = source_common.scope_type
			   AND source_derivative.scope_id = source_common.scope_id
			   AND source_derivative.generation = source_common.derivative_generation
			   AND source_derivative.object_kind = 'graph_fact'
			   AND source_derivative.lifecycle = 'active'
			  JOIN team_projection_outputs source_output
			    ON source_output.job_id = source_common.job_id
			   AND source_output.derivative_object_id = source_common.derivative_object_id
			   AND source_output.derivative_generation = source_common.derivative_generation
			  JOIN team_object_contributions source_contribution
			    ON source_contribution.parent_object_id = source_common.root_object_id
			   AND source_contribution.derivative_object_id = source_common.derivative_object_id
			   AND source_contribution.parent_generation = source_common.root_generation
			   AND source_contribution.derivative_generation = source_common.derivative_generation
			   AND source_contribution.team_id = source_common.team_id
			   AND source_contribution.scope_type = source_common.scope_type
			   AND source_contribution.scope_id = source_common.scope_id
			  JOIN team_projection_jobs job
			    ON job.job_id = common.job_id AND job.state = 'ready'
			  JOIN team_object_registry root
			    ON root.object_id = common.root_object_id
			   AND root.store_id = common.store_id AND root.team_id = common.team_id
			   AND root.scope_type = common.scope_type AND root.scope_id = common.scope_id
			   AND root.generation = common.root_generation
			   AND root.object_kind = 'graph_delta'
			  JOIN team_object_registry derivative
			    ON derivative.object_id = common.derivative_object_id
			   AND derivative.store_id = common.store_id AND derivative.team_id = common.team_id
			   AND derivative.scope_type = common.scope_type AND derivative.scope_id = common.scope_id
			   AND derivative.generation = common.derivative_generation
			   AND derivative.object_kind = 'assertion' AND derivative.lifecycle = 'active'
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
			 WHERE common.projection_kind = 'claim' AND `+predicate+`
		),
		selected_derivatives AS MATERIALIZED (
			SELECT derivative_object_id, max(created_at) selection_created_at
			  FROM authorized
			 WHERE ? = '' OR instr(lower(claim_json), ?) > 0
			 GROUP BY derivative_object_id
			 ORDER BY selection_created_at DESC, derivative_object_id
			 LIMIT ?
		)
		SELECT authorized.root_object_id, authorized.derivative_object_id,
		       authorized.source_graph_derivative_object_id, authorized.intent_id,
		       authorized.team_id, authorized.scope_type, authorized.scope_id,
		       authorized.claim_slot_digest, authorized.claim_json,
		       authorized.source_refs_json, authorized.content_digest,
		       authorized.created_at, authorized.visible_count
		  FROM authorized
		  JOIN selected_derivatives selected
		    ON selected.derivative_object_id = authorized.derivative_object_id
		 ORDER BY selected.selection_created_at DESC, authorized.derivative_object_id,
		          authorized.created_at DESC, authorized.intent_id,
		          authorized.root_object_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]TeamAuthorizedAssertionContribution, 0)
	rootIDs := make([]string, 0)
	for rows.Next() {
		var item TeamAuthorizedAssertionContribution
		var teamID, scopeType, scopeID, claimJSON, refsJSON string
		if err := rows.Scan(
			&item.RootObjectID, &item.DerivativeObjectID,
			&item.SourceGraphDerivativeObjectID, &item.IntentID,
			&teamID, &scopeType, &scopeID, &item.ClaimSlotDigest,
			&claimJSON, &refsJSON, &item.ContentDigest, &item.CreatedAt,
			&item.VisibleContributionCount,
		); err != nil {
			return nil, err
		}
		refs, ok := decodeAuthorizedOpaqueReferences(refsJSON)
		if !ok || !lowerHexDigest(item.ClaimSlotDigest) ||
			!decodeAuthorizedCanonicalObject(claimJSON, item.ContentDigest, &item.Claim) ||
			!validAuthorizedSemanticIdentity(item.RootObjectID, item.DerivativeObjectID, item.IntentID) ||
			!validProjectionOpaque(item.SourceGraphDerivativeObjectID, 255) ||
			!validAuthorizedReadTime(item.CreatedAt) || item.VisibleContributionCount < 1 {
			return nil, ErrTeamPolicyNotReady
		}
		item.SourceRefs = refs
		item.PartitionKey = authorizedTeamPartitionKey(teamID, scopeType, scopeID)
		result = append(result, item)
		rootIDs = append(rootIDs, item.RootObjectID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.RecheckAuthorizedCandidateRoots(ctx, filter, rootIDs); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) QueryAuthorizedTeamContinuityCheckpoints(
	ctx context.Context,
	filter AuthorizedCandidateFilter,
	query TeamTextReadQuery,
) ([]TeamAuthorizedContinuityCheckpoint, error) {
	match, limit, err := normalizeTeamTextReadQuery(query)
	if err != nil {
		return nil, err
	}
	predicate, predicateArgs, err := filter.sqlPredicateAt("root", s.clock().UTC())
	if err != nil {
		return nil, err
	}
	args := append([]any{}, predicateArgs...)
	args = append(args, match, strings.ToLower(match), limit)
	rows, err := s.db.QueryContext(ctx, `
		WITH authorized AS MATERIALIZED (
			SELECT common.root_object_id, common.derivative_object_id,
			       common.intent_id, common.team_id, common.scope_type,
			       common.scope_id, continuity.thread_slot_digest,
			       continuity.session_slot_digest, continuity.checkpoint_json,
			       continuity.content_digest, continuity.created_at,
			       count(*) OVER (PARTITION BY common.derivative_object_id) visible_count
			  FROM team_semantic_materializations common
			  JOIN team_continuity_materializations continuity
			    ON continuity.intent_id = common.intent_id
			   AND continuity.store_id = common.store_id AND continuity.team_id = common.team_id
			   AND continuity.scope_type = common.scope_type AND continuity.scope_id = common.scope_id
			   AND continuity.derivative_object_id = common.derivative_object_id
			  JOIN team_projection_jobs job
			    ON job.job_id = common.job_id AND job.state = 'ready'
			  JOIN team_object_registry root
			    ON root.object_id = common.root_object_id
			   AND root.store_id = common.store_id AND root.team_id = common.team_id
			   AND root.scope_type = common.scope_type AND root.scope_id = common.scope_id
			   AND root.generation = common.root_generation
			   AND root.object_kind = 'graph_delta'
			  JOIN team_object_registry derivative
			    ON derivative.object_id = common.derivative_object_id
			   AND derivative.store_id = common.store_id AND derivative.team_id = common.team_id
			   AND derivative.scope_type = common.scope_type AND derivative.scope_id = common.scope_id
			   AND derivative.generation = common.derivative_generation
			   AND derivative.object_kind = 'continuity_checkpoint' AND derivative.lifecycle = 'active'
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
			 WHERE common.projection_kind = 'continuity' AND `+predicate+`
		),
		selected_derivatives AS MATERIALIZED (
			SELECT derivative_object_id, max(created_at) selection_created_at
			  FROM authorized
			 WHERE ? = '' OR instr(lower(checkpoint_json), ?) > 0
			 GROUP BY derivative_object_id
			 ORDER BY selection_created_at DESC, derivative_object_id
			 LIMIT ?
		)
		SELECT authorized.root_object_id, authorized.derivative_object_id,
		       authorized.intent_id, authorized.team_id, authorized.scope_type,
		       authorized.scope_id, authorized.thread_slot_digest,
		       authorized.session_slot_digest, authorized.checkpoint_json,
		       authorized.content_digest, authorized.created_at,
		       authorized.visible_count
		  FROM authorized
		  JOIN selected_derivatives selected
		    ON selected.derivative_object_id = authorized.derivative_object_id
		 ORDER BY selected.selection_created_at DESC, authorized.derivative_object_id,
		          authorized.created_at DESC, authorized.intent_id,
		          authorized.root_object_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]TeamAuthorizedContinuityCheckpoint, 0)
	rootIDs := make([]string, 0)
	for rows.Next() {
		var item TeamAuthorizedContinuityCheckpoint
		var teamID, scopeType, scopeID, checkpointJSON string
		if err := rows.Scan(
			&item.RootObjectID, &item.DerivativeObjectID, &item.IntentID,
			&teamID, &scopeType, &scopeID, &item.ThreadSlotDigest,
			&item.SessionSlotDigest, &checkpointJSON, &item.ContentDigest,
			&item.CreatedAt, &item.VisibleContributionCount,
		); err != nil {
			return nil, err
		}
		if !lowerHexDigest(item.ThreadSlotDigest) || !lowerHexDigest(item.SessionSlotDigest) ||
			!decodeAuthorizedCanonicalObject(checkpointJSON, item.ContentDigest, &item.Checkpoint) ||
			!validAuthorizedSemanticIdentity(item.RootObjectID, item.DerivativeObjectID, item.IntentID) ||
			!validAuthorizedReadTime(item.CreatedAt) || item.VisibleContributionCount < 1 {
			return nil, ErrTeamPolicyNotReady
		}
		item.PartitionKey = authorizedTeamPartitionKey(teamID, scopeType, scopeID)
		result = append(result, item)
		rootIDs = append(rootIDs, item.RootObjectID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.RecheckAuthorizedCandidateRoots(ctx, filter, rootIDs); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) QueryAuthorizedTeamSemanticEmbeddings(
	ctx context.Context,
	filter AuthorizedCandidateFilter,
	query TeamSemanticEmbeddingReadQuery,
) ([]TeamAuthorizedSemanticEmbedding, error) {
	model, sources, err := normalizeTeamSemanticEmbeddingReadQuery(query)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		if err := s.RecheckAuthorizedCandidateFilter(ctx, filter); err != nil {
			return nil, err
		}
		return []TeamAuthorizedSemanticEmbedding{}, nil
	}
	predicate, predicateArgs, err := filter.sqlPredicateAt("root", s.clock().UTC())
	if err != nil {
		return nil, err
	}
	keyRows := make([]string, len(sources))
	args := make([]any, 0, 2*len(sources)+len(predicateArgs)+1)
	for index, source := range sources {
		keyRows[index] = "(?, ?)"
		args = append(args, source.RootObjectID, source.SourceGraphDerivativeObjectID)
	}
	args = append(args, predicateArgs...)
	args = append(args, model)
	rows, err := s.db.QueryContext(ctx, `
		WITH requested_sources(root_object_id, source_graph_derivative_object_id) AS (
			VALUES `+strings.Join(keyRows, ",")+`
		),
		authorized AS MATERIALIZED (
			SELECT common.root_object_id, common.derivative_object_id,
			       source_graph.derivative_object_id source_graph_derivative_object_id,
			       common.intent_id, common.team_id, common.scope_type,
			       common.scope_id, intent.source_kind, embedding.model,
			       embedding.dimensions, embedding.vector_json,
			       embedding.vector_digest, embedding.content_digest,
			       common.semantic_key_digest, common.policy_digest,
			       embedding.created_at,
			       count(*) OVER (
			           PARTITION BY common.derivative_object_id, embedding.model
			       ) visible_count
			  FROM team_semantic_materializations common
			  JOIN team_semantic_embeddings embedding
			    ON embedding.intent_id = common.intent_id
			   AND embedding.store_id = common.store_id AND embedding.team_id = common.team_id
			   AND embedding.scope_type = common.scope_type AND embedding.scope_id = common.scope_id
			   AND embedding.derivative_object_id = common.derivative_object_id
			  JOIN team_semantic_projection_intents intent
			    ON intent.intent_id = common.intent_id
			   AND intent.projection_kind = 'embedding'
			  JOIN team_semantic_projection_intents source_graph
			    ON source_graph.root_object_id = intent.root_object_id
			   AND source_graph.root_generation = intent.root_generation
			   AND source_graph.store_id = intent.store_id
			   AND source_graph.team_id = intent.team_id
			   AND source_graph.scope_type = intent.scope_type
			   AND source_graph.scope_id = intent.scope_id
			   AND source_graph.source_kind = intent.source_kind
			   AND source_graph.source_ordinal = intent.source_ordinal
			   AND source_graph.projection_kind = 'graph'
			  JOIN team_semantic_materializations source_common
			    ON source_common.intent_id = source_graph.intent_id
			   AND source_common.root_object_id = common.root_object_id
			   AND source_common.root_generation = common.root_generation
			   AND source_common.store_id = common.store_id
			   AND source_common.team_id = common.team_id
			   AND source_common.scope_type = common.scope_type
			   AND source_common.scope_id = common.scope_id
			   AND source_common.derivative_object_id = source_graph.derivative_object_id
			   AND source_common.projection_kind = 'graph'
			  JOIN team_graph_materializations source_materialization
			    ON source_materialization.intent_id = source_common.intent_id
			   AND source_materialization.store_id = source_common.store_id
			   AND source_materialization.team_id = source_common.team_id
			   AND source_materialization.scope_type = source_common.scope_type
			   AND source_materialization.scope_id = source_common.scope_id
			   AND source_materialization.derivative_object_id = source_common.derivative_object_id
			   AND source_materialization.graph_kind = source_graph.derivative_kind
			  JOIN team_projection_jobs source_job
			    ON source_job.job_id = source_common.job_id AND source_job.state = 'ready'
			  JOIN team_object_registry source_derivative
			    ON source_derivative.object_id = source_common.derivative_object_id
			   AND source_derivative.store_id = source_common.store_id
			   AND source_derivative.team_id = source_common.team_id
			   AND source_derivative.scope_type = source_common.scope_type
			   AND source_derivative.scope_id = source_common.scope_id
			   AND source_derivative.generation = source_common.derivative_generation
			   AND source_derivative.object_kind = source_graph.derivative_kind
			   AND source_derivative.lifecycle = 'active'
			  JOIN team_projection_outputs source_output
			    ON source_output.job_id = source_common.job_id
			   AND source_output.derivative_object_id = source_common.derivative_object_id
			   AND source_output.derivative_generation = source_common.derivative_generation
			  JOIN team_object_contributions source_contribution
			    ON source_contribution.parent_object_id = source_common.root_object_id
			   AND source_contribution.derivative_object_id = source_common.derivative_object_id
			   AND source_contribution.parent_generation = source_common.root_generation
			   AND source_contribution.derivative_generation = source_common.derivative_generation
			   AND source_contribution.team_id = source_common.team_id
			   AND source_contribution.scope_type = source_common.scope_type
			   AND source_contribution.scope_id = source_common.scope_id
			  JOIN team_projection_jobs job
			    ON job.job_id = common.job_id AND job.state = 'ready'
			  JOIN team_object_registry root
			    ON root.object_id = common.root_object_id
			   AND root.store_id = common.store_id AND root.team_id = common.team_id
			   AND root.scope_type = common.scope_type AND root.scope_id = common.scope_id
			   AND root.generation = common.root_generation
			   AND root.object_kind = 'graph_delta'
			  JOIN team_object_registry derivative
			    ON derivative.object_id = common.derivative_object_id
			   AND derivative.store_id = common.store_id AND derivative.team_id = common.team_id
			   AND derivative.scope_type = common.scope_type AND derivative.scope_id = common.scope_id
			   AND derivative.generation = common.derivative_generation
			   AND derivative.object_kind = 'embedding' AND derivative.lifecycle = 'active'
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
			 WHERE common.projection_kind = 'embedding' AND `+predicate+`
			   AND embedding.model = ?
		)
		SELECT authorized.root_object_id, authorized.derivative_object_id,
		       authorized.source_graph_derivative_object_id, authorized.intent_id,
		       authorized.team_id, authorized.scope_type, authorized.scope_id,
		       authorized.source_kind, authorized.model, authorized.dimensions,
		       authorized.vector_json, authorized.vector_digest,
		       authorized.content_digest, authorized.semantic_key_digest,
		       authorized.policy_digest, authorized.created_at,
		       authorized.visible_count
		  FROM authorized
		  JOIN requested_sources requested
		    ON requested.root_object_id = authorized.root_object_id
		   AND requested.source_graph_derivative_object_id = authorized.source_graph_derivative_object_id
		 ORDER BY authorized.source_graph_derivative_object_id,
		          authorized.root_object_id, authorized.derivative_object_id,
		          authorized.intent_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]TeamAuthorizedSemanticEmbedding, 0)
	rootIDs := make([]string, 0)
	for rows.Next() {
		var item TeamAuthorizedSemanticEmbedding
		var teamID, scopeType, scopeID, vectorJSON string
		var dimensions int
		if err := rows.Scan(
			&item.RootObjectID, &item.DerivativeObjectID,
			&item.SourceGraphDerivativeObjectID, &item.IntentID,
			&teamID, &scopeType, &scopeID, &item.SourceKind, &item.Model,
			&dimensions, &vectorJSON, &item.VectorDigest, &item.ContentDigest,
			&item.SemanticKeyDigest, &item.PolicyDigest, &item.CreatedAt,
			&item.VisibleContributionCount,
		); err != nil {
			return nil, err
		}
		vector, ok := decodeAuthorizedVector(vectorJSON, item.VectorDigest, dimensions)
		if !ok || !validAuthorizedSemanticIdentity(item.RootObjectID, item.DerivativeObjectID, item.IntentID) ||
			!validProjectionOpaque(item.SourceGraphDerivativeObjectID, 255) ||
			!validTeamGraphProjectionSource(item.SourceKind) || item.Model != model ||
			!lowerHexDigest(item.ContentDigest) || !lowerHexDigest(item.SemanticKeyDigest) ||
			!lowerHexDigest(item.PolicyDigest) || !validAuthorizedReadTime(item.CreatedAt) ||
			item.VisibleContributionCount < 1 {
			return nil, ErrTeamPolicyNotReady
		}
		item.Vector = vector
		item.PartitionKey = authorizedTeamPartitionKey(teamID, scopeType, scopeID)
		result = append(result, item)
		rootIDs = append(rootIDs, item.RootObjectID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.RecheckAuthorizedCandidateRoots(ctx, filter, rootIDs); err != nil {
		return nil, err
	}
	return result, nil
}

func normalizeTeamTextReadQuery(query TeamTextReadQuery) (string, int, error) {
	match := strings.TrimSpace(query.Match)
	if len(match) > maxAuthorizedTeamReadMatch || !utf8.ValidString(match) || strings.ContainsRune(match, '\x00') {
		return "", 0, ErrInvalidTeamReadQuery
	}
	limit, ok := normalizeAuthorizedTeamReadLimit(query.Limit)
	if !ok {
		return "", 0, ErrInvalidTeamReadQuery
	}
	return match, limit, nil
}

func normalizeTeamMemoryEmbeddingReadQuery(
	query TeamMemoryEmbeddingReadQuery,
) (string, []TeamMemoryCapsuleReadKey, error) {
	if !validTeamClass(query.Model, 64) || len(query.Capsules) > maxAuthorizedTeamReadLimit {
		return "", nil, ErrInvalidTeamReadQuery
	}
	seen := make(map[string]struct{}, len(query.Capsules))
	capsules := make([]TeamMemoryCapsuleReadKey, len(query.Capsules))
	for index, capsule := range query.Capsules {
		if !validProjectionOpaque(capsule.RootObjectID, 255) ||
			!validProjectionOpaque(capsule.CapsuleID, 255) {
			return "", nil, ErrInvalidTeamReadQuery
		}
		key := capsule.RootObjectID + "\x00" + capsule.CapsuleID
		if _, duplicate := seen[key]; duplicate {
			return "", nil, ErrInvalidTeamReadQuery
		}
		seen[key] = struct{}{}
		capsules[index] = capsule
	}
	return query.Model, capsules, nil
}

func normalizeTeamSemanticEmbeddingReadQuery(
	query TeamSemanticEmbeddingReadQuery,
) (string, []TeamSemanticEmbeddingReadKey, error) {
	if !validTeamClass(query.Model, 64) ||
		len(query.Sources) > MaxTeamSemanticEmbeddingReadSources {
		return "", nil, ErrInvalidTeamReadQuery
	}
	seen := make(map[string]struct{}, len(query.Sources))
	sources := make([]TeamSemanticEmbeddingReadKey, len(query.Sources))
	for index, source := range query.Sources {
		if !validProjectionOpaque(source.RootObjectID, 255) ||
			!validProjectionOpaque(source.SourceGraphDerivativeObjectID, 255) {
			return "", nil, ErrInvalidTeamReadQuery
		}
		key := source.RootObjectID + "\x00" + source.SourceGraphDerivativeObjectID
		if _, duplicate := seen[key]; duplicate {
			return "", nil, ErrInvalidTeamReadQuery
		}
		seen[key] = struct{}{}
		sources[index] = source
	}
	return query.Model, sources, nil
}

func normalizeAuthorizedTeamReadLimit(limit int) (int, bool) {
	if limit == 0 {
		return defaultAuthorizedTeamReadLimit, true
	}
	return limit, limit > 0 && limit <= maxAuthorizedTeamReadLimit
}

func authorizedTeamPartitionKey(teamID, scopeType, scopeID string) string {
	return teamGraphOpaqueDigestID(
		"team_partition", "pulse-team-read-partition-v1", teamID, scopeType, scopeID,
	)
}

func validAuthorizedSemanticIdentity(rootID, derivativeID, intentID string) bool {
	return validProjectionOpaque(rootID, 255) && validProjectionOpaque(derivativeID, 255) &&
		validProjectionOpaque(intentID, 255)
}

func validAuthorizedReadTime(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func decodeAuthorizedStringList(raw string) ([]string, bool) {
	var values []string
	if err := decodeAuthorizedJSON(raw, &values); err != nil || values == nil {
		return nil, false
	}
	for _, value := range values {
		if value == "" || !utf8.ValidString(value) {
			return nil, false
		}
	}
	return values, true
}

func decodeAuthorizedOpaqueReferences(raw string) ([]string, bool) {
	var values []string
	if err := decodeAuthorizedJSON(raw, &values); err != nil || values == nil {
		return nil, false
	}
	for _, value := range values {
		if !validProjectionOpaque(value, 255) {
			return nil, false
		}
	}
	return values, true
}

func decodeAuthorizedGraphPayload(
	raw, digest string,
	item *TeamAuthorizedGraphContribution,
) bool {
	switch item.GraphKind {
	case "graph_entity":
		var value TeamGraphNode
		if !decodeAuthorizedCanonicalObject(raw, digest, &value) {
			return false
		}
		item.Node = &value
	case "graph_relation":
		var value TeamGraphEdge
		if !decodeAuthorizedCanonicalObject(raw, digest, &value) {
			return false
		}
		item.Edge = &value
	case "graph_fact":
		var value TeamGraphFact
		if !decodeAuthorizedCanonicalObject(raw, digest, &value) {
			return false
		}
		item.Fact = &value
	case "graph_event":
		var value TeamGraphEvent
		if !decodeAuthorizedCanonicalObject(raw, digest, &value) {
			return false
		}
		item.Event = &value
	default:
		return false
	}
	return true
}

func decodeAuthorizedCanonicalObject(raw, digest string, target any) bool {
	if !lowerHexDigest(digest) || len(raw) < 2 || raw[0] != '{' || raw[len(raw)-1] != '}' {
		return false
	}
	hash := sha256.Sum256([]byte(raw))
	if hex.EncodeToString(hash[:]) != digest {
		return false
	}
	return decodeAuthorizedJSON(raw, target) == nil
}

func decodeAuthorizedVector(raw, digest string, dimensions int) ([]float32, bool) {
	if dimensions < 1 || dimensions > maxProjectionVectorDimensions || !lowerHexDigest(digest) {
		return nil, false
	}
	hash := sha256.Sum256([]byte(raw))
	if hex.EncodeToString(hash[:]) != digest {
		return nil, false
	}
	var vector []float32
	if err := decodeAuthorizedJSON(raw, &vector); err != nil || len(vector) != dimensions {
		return nil, false
	}
	nonZero := false
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, false
		}
		if value != 0 {
			nonZero = true
		}
	}
	return vector, nonZero
}

func decodeAuthorizedJSON(raw string, target any) error {
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
