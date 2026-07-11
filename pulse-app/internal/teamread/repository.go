package teamread

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
)

type teamStore interface {
	BuildAuthorizedCandidateFilter(context.Context, store.CandidateFilterRequest) (store.AuthorizedCandidateFilter, error)
	QueryAuthorizedTeamMemoryCapsules(context.Context, store.AuthorizedCandidateFilter, store.TeamTextReadQuery) ([]store.TeamAuthorizedMemoryCapsule, error)
	QueryAuthorizedTeamMemoryEmbeddings(context.Context, store.AuthorizedCandidateFilter, store.TeamMemoryEmbeddingReadQuery) ([]store.TeamAuthorizedMemoryEmbedding, error)
	QueryAuthorizedTeamGraphContributions(context.Context, store.AuthorizedCandidateFilter, store.TeamTextReadQuery) ([]store.TeamAuthorizedGraphContribution, error)
	QueryAuthorizedTeamAssertionContributions(context.Context, store.AuthorizedCandidateFilter, store.TeamTextReadQuery) ([]store.TeamAuthorizedAssertionContribution, error)
	QueryAuthorizedTeamContinuityCheckpoints(context.Context, store.AuthorizedCandidateFilter, store.TeamTextReadQuery) ([]store.TeamAuthorizedContinuityCheckpoint, error)
	QueryAuthorizedTeamSemanticEmbeddings(context.Context, store.AuthorizedCandidateFilter, store.TeamSemanticEmbeddingReadQuery) ([]store.TeamAuthorizedSemanticEmbedding, error)
	RecheckAuthorizedCandidateRoots(context.Context, store.AuthorizedCandidateFilter, []string) error
}

type authorizedRepository struct {
	store  teamStore
	filter store.AuthorizedCandidateFilter
}

func (repository *authorizedRepository) LoadAuthorizedCorpus(
	ctx context.Context,
	query retrieve.TeamCorpusQuery,
) (retrieve.TeamAuthorizedCorpus, error) {
	if repository == nil || repository.store == nil || query.Limit < 1 {
		return retrieve.TeamAuthorizedCorpus{}, ErrInvalidRequest
	}
	switch query.Surface {
	case retrieve.TeamCorpusSurfaceRecall:
		return repository.loadRecallCorpus(ctx, query)
	case retrieve.TeamCorpusSurfaceContext:
		return repository.loadContextCorpus(ctx, query)
	default:
		return retrieve.TeamAuthorizedCorpus{}, ErrInvalidRequest
	}
}

func (repository *authorizedRepository) loadRecallCorpus(
	ctx context.Context,
	query retrieve.TeamCorpusQuery,
) (retrieve.TeamAuthorizedCorpus, error) {
	rows, err := repository.store.QueryAuthorizedTeamMemoryCapsules(
		ctx, repository.filter, store.TeamTextReadQuery{Limit: query.Limit},
	)
	if err != nil {
		return retrieve.TeamAuthorizedCorpus{}, err
	}
	embeddings := make(map[string]store.TeamAuthorizedMemoryEmbedding)
	if query.EmbeddingModel != "" {
		capsules := make([]store.TeamMemoryCapsuleReadKey, len(rows))
		for index, row := range rows {
			capsules[index] = store.TeamMemoryCapsuleReadKey{
				RootObjectID: row.RootObjectID, CapsuleID: row.CapsuleID,
			}
		}
		vectorRows, err := repository.store.QueryAuthorizedTeamMemoryEmbeddings(
			ctx, repository.filter, store.TeamMemoryEmbeddingReadQuery{
				Model: query.EmbeddingModel, Capsules: capsules,
			},
		)
		if err != nil {
			return retrieve.TeamAuthorizedCorpus{}, err
		}
		for _, vector := range vectorRows {
			key := teamMemoryDocumentKey(vector.RootObjectID, vector.CapsuleID)
			if current, exists := embeddings[key]; !exists || vector.EmbeddingID < current.EmbeddingID {
				embeddings[key] = vector
			}
		}
	}
	corpus := retrieve.TeamAuthorizedCorpus{
		Memories: make([]retrieve.TeamMemoryDocument, 0, len(rows)),
	}
	for _, row := range rows {
		document := retrieve.TeamMemoryDocument{
			DocumentID: row.CapsuleID, RootObjectID: row.RootObjectID,
			PartitionKey: row.PartitionKey, Kind: row.Kind,
			RedactedSummary: row.RedactedSummary, Confidence: row.Confidence,
			PrivacyTier: row.PrivacyTier, Retention: row.Retention,
			Tags: append([]string(nil), row.Tags...),
		}
		if vector, ok := embeddings[teamMemoryDocumentKey(row.RootObjectID, row.CapsuleID)]; ok {
			if vector.PartitionKey != row.PartitionKey || vector.PrivacyTier != row.PrivacyTier ||
				vector.Retention != row.Retention || vector.Model != query.EmbeddingModel {
				return retrieve.TeamAuthorizedCorpus{}, fmt.Errorf("%w: memory embedding partition mismatch", ErrInvalidRequest)
			}
			document.EmbeddingModel = vector.Model
			document.Embedding = append([]float32(nil), vector.Vector...)
		}
		corpus.Memories = append(corpus.Memories, document)
	}
	return corpus, nil
}

func (repository *authorizedRepository) loadContextCorpus(
	ctx context.Context,
	query retrieve.TeamCorpusQuery,
) (retrieve.TeamAuthorizedCorpus, error) {
	graphLimit := query.Limit * 4 / 5
	if graphLimit < 1 {
		graphLimit = 1
	}
	assertionLimit := query.Limit - graphLimit
	graphRows, err := repository.store.QueryAuthorizedTeamGraphContributions(
		ctx, repository.filter, store.TeamTextReadQuery{Limit: graphLimit},
	)
	if err != nil {
		return retrieve.TeamAuthorizedCorpus{}, err
	}
	assertionRows := make([]store.TeamAuthorizedAssertionContribution, 0)
	if assertionLimit > 0 {
		assertionRows, err = repository.store.QueryAuthorizedTeamAssertionContributions(
			ctx, repository.filter, store.TeamTextReadQuery{Limit: assertionLimit},
		)
		if err != nil {
			return retrieve.TeamAuthorizedCorpus{}, err
		}
	}
	corpus, err := mapAuthorizedGraphCorpus(graphRows, assertionRows)
	if err != nil {
		return retrieve.TeamAuthorizedCorpus{}, err
	}
	if query.EmbeddingModel != "" {
		sources, err := selectedSemanticEmbeddingSources(graphRows)
		if err != nil {
			return retrieve.TeamAuthorizedCorpus{}, err
		}
		embeddings, err := repository.store.QueryAuthorizedTeamSemanticEmbeddings(
			ctx, repository.filter, store.TeamSemanticEmbeddingReadQuery{
				Model: query.EmbeddingModel, Sources: sources,
			},
		)
		if err != nil {
			return retrieve.TeamAuthorizedCorpus{}, err
		}
		corpus.SemanticEmbeddings, err = mapAuthorizedSemanticEmbeddings(
			embeddings, query.EmbeddingModel,
		)
		if err != nil {
			return retrieve.TeamAuthorizedCorpus{}, err
		}
	}
	return corpus, nil
}

func selectedSemanticEmbeddingSources(
	graphRows []store.TeamAuthorizedGraphContribution,
) ([]store.TeamSemanticEmbeddingReadKey, error) {
	seen := make(map[string]struct{}, len(graphRows))
	sources := make([]store.TeamSemanticEmbeddingReadKey, 0, len(graphRows))
	for _, row := range graphRows {
		key := row.RootObjectID + "\x00" + row.DerivativeObjectID
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		sources = append(sources, store.TeamSemanticEmbeddingReadKey{
			RootObjectID: row.RootObjectID, SourceGraphDerivativeObjectID: row.DerivativeObjectID,
		})
		if len(sources) > store.MaxTeamSemanticEmbeddingReadSources {
			return nil, fmt.Errorf("%w: too many selected graph sources", ErrInvalidRequest)
		}
	}
	sort.Slice(sources, func(left, right int) bool {
		if sources[left].RootObjectID != sources[right].RootObjectID {
			return sources[left].RootObjectID < sources[right].RootObjectID
		}
		return sources[left].SourceGraphDerivativeObjectID < sources[right].SourceGraphDerivativeObjectID
	})
	return sources, nil
}

func (repository *authorizedRepository) LoadAuthorizedContinuity(
	ctx context.Context,
	query retrieve.TeamResumeQuery,
) ([]retrieve.TeamContinuityDocument, error) {
	match := query.ThreadID
	if match == "" {
		match = query.SessionID
	}
	rows, err := repository.store.QueryAuthorizedTeamContinuityCheckpoints(
		ctx, repository.filter, store.TeamTextReadQuery{Match: match, Limit: query.Limit},
	)
	if err != nil {
		return nil, err
	}
	documents := make([]retrieve.TeamContinuityDocument, 0, len(rows))
	for _, row := range rows {
		updatedAt, err := time.Parse(time.RFC3339Nano, row.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid continuity timestamp", ErrInvalidRequest)
		}
		suggested := ""
		if len(row.Checkpoint.ReviewInsights) > 0 {
			suggested = row.Checkpoint.ReviewInsights[0]
		}
		document := retrieve.TeamContinuityDocument{
			RootObjectID: row.RootObjectID, ObjectID: row.DerivativeObjectID,
			PartitionKey: row.PartitionKey, ThreadID: row.Checkpoint.ThreadID,
			ProjectID: query.ProjectID, SessionID: row.Checkpoint.SessionID,
			Summary:     row.Checkpoint.Summary,
			Decisions:   append([]string(nil), row.Checkpoint.Decisions...),
			OpenLoops:   append([]string(nil), row.Checkpoint.OpenLoops...),
			DoNotRepeat: append([]string(nil), row.Checkpoint.DoNotRepeat...),
			EmotionalStateContext: append(
				append([]string(nil), row.Checkpoint.EmotionalAnchors...),
				row.Checkpoint.StateSignals...,
			),
			SuggestedNextStep: suggested, UpdatedAtUnixMilli: updatedAt.UnixMilli(),
		}
		documents = append(documents, document)
	}
	sort.Slice(documents, func(left, right int) bool {
		if documents[left].UpdatedAtUnixMilli != documents[right].UpdatedAtUnixMilli {
			return documents[left].UpdatedAtUnixMilli > documents[right].UpdatedAtUnixMilli
		}
		if documents[left].ObjectID != documents[right].ObjectID {
			return documents[left].ObjectID < documents[right].ObjectID
		}
		return documents[left].RootObjectID < documents[right].RootObjectID
	})
	return documents, nil
}

func (repository *authorizedRepository) RecheckAuthorizedRoots(
	ctx context.Context,
	rootObjectIDs []string,
) error {
	return repository.store.RecheckAuthorizedCandidateRoots(ctx, repository.filter, rootObjectIDs)
}

func teamMemoryDocumentKey(rootObjectID, capsuleID string) string {
	return rootObjectID + "\x00" + capsuleID
}

func mapAuthorizedGraphCorpus(
	graphRows []store.TeamAuthorizedGraphContribution,
	assertionRows []store.TeamAuthorizedAssertionContribution,
) (retrieve.TeamAuthorizedCorpus, error) {
	corpus := retrieve.TeamAuthorizedCorpus{}
	links := make(map[string]retrieve.TeamGraphLink)

	for _, row := range graphRows {
		switch row.GraphKind {
		case "graph_entity":
			if row.Node == nil {
				return corpus, ErrInvalidRequest
			}
			summary := optionalString(row.Node.Summary)
			if summary == "" {
				summary = row.Node.CanonicalName
			}
			confidence := optionalScore(row.Node.Salience, 1)
			document := retrieve.TeamEntityDocument{
				RootObjectID: row.RootObjectID, ObjectID: row.DerivativeObjectID,
				PartitionKey: row.PartitionKey, EntityKind: row.Node.Kind,
				Name: row.Node.CanonicalName, Summary: summary, Confidence: confidence,
			}
			corpus.Entities = append(corpus.Entities, document)
		case "graph_relation":
			if row.Edge == nil || len(row.ResolvedRefs) != 2 {
				return corpus, ErrInvalidRequest
			}
			strength := optionalScore(row.Edge.Strength, 1)
			summary := optionalString(row.Edge.Summary)
			if summary == "" {
				summary = row.Edge.Kind
			}
			document := retrieve.TeamRelationDocument{
				RootObjectID: row.RootObjectID, ObjectID: row.DerivativeObjectID,
				PartitionKey: row.PartitionKey, FromObjectID: row.ResolvedRefs[0],
				ToObjectID: row.ResolvedRefs[1], RelationKind: row.Edge.Kind,
				Summary: summary, Strength: strength, Confidence: strength,
			}
			corpus.Relations = append(corpus.Relations, document)
			addTeamGraphLink(links, document.RootObjectID, document.PartitionKey, document.FromObjectID, document.ObjectID, strength)
			addTeamGraphLink(links, document.RootObjectID, document.PartitionKey, document.ObjectID, document.ToObjectID, strength)
		case "graph_fact":
			if row.Fact == nil || len(row.ResolvedRefs) < 1 || row.Fact.Confidence == nil {
				return corpus, ErrInvalidRequest
			}
			document := retrieve.TeamFactDocument{
				RootObjectID: row.RootObjectID, ObjectID: row.DerivativeObjectID,
				PartitionKey: row.PartitionKey, NodeObjectID: row.ResolvedRefs[0],
				Text: row.Fact.Text, Predicate: optionalString(row.Fact.Predicate),
				ObjectText: optionalString(row.Fact.ObjectText), Confidence: *row.Fact.Confidence,
				Domain: row.Fact.Domain,
			}
			corpus.Facts = append(corpus.Facts, document)
			addTeamGraphLink(links, document.RootObjectID, document.PartitionKey, document.NodeObjectID, document.ObjectID, 1)
		case "graph_event":
			if row.Event == nil || row.Event.Confidence == nil {
				return corpus, ErrInvalidRequest
			}
			document := retrieve.TeamEventDocument{
				RootObjectID: row.RootObjectID, ObjectID: row.DerivativeObjectID,
				PartitionKey: row.PartitionKey, Title: row.Event.Title,
				Summary: row.Event.Summary, Confidence: *row.Event.Confidence,
				Domain: row.Event.Domain, OccurredAt: optionalString(row.Event.OccurredAt),
			}
			corpus.Events = append(corpus.Events, document)
			for _, entityID := range row.ResolvedRefs {
				addTeamGraphLink(links, document.RootObjectID, document.PartitionKey, entityID, document.ObjectID, 1)
			}
		default:
			return corpus, ErrInvalidRequest
		}
	}
	for _, row := range assertionRows {
		if len(row.SourceRefs) < 1 || row.Claim.Confidence == nil ||
			row.SourceGraphDerivativeObjectID == "" {
			return corpus, ErrInvalidRequest
		}
		observedAt, err := time.Parse(time.RFC3339Nano, row.CreatedAt)
		if err != nil {
			return corpus, ErrInvalidRequest
		}
		document := retrieve.TeamAssertionDocument{
			RootObjectID: row.RootObjectID, ObjectID: row.DerivativeObjectID,
			PartitionKey:      row.PartitionKey,
			CandidateObjectID: row.SourceGraphDerivativeObjectID,
			SubjectObjectID:   row.SourceRefs[0], ClaimSlotDigest: row.ClaimSlotDigest,
			Text: row.Claim.Text, Predicate: optionalString(row.Claim.Predicate),
			ObjectText:          optionalString(row.Claim.ObjectText),
			ObservedAtUnixMilli: observedAt.UnixMilli(), Confidence: *row.Claim.Confidence,
		}
		corpus.Assertions = append(corpus.Assertions, document)
	}

	sortTeamGraphContributions(&corpus)
	corpus.GraphLinks = sortedTeamGraphLinks(links)
	return corpus, nil
}

func mapAuthorizedSemanticEmbeddings(
	rows []store.TeamAuthorizedSemanticEmbedding,
	model string,
) ([]retrieve.TeamSemanticEmbeddingDocument, error) {
	result := make([]retrieve.TeamSemanticEmbeddingDocument, 0, len(rows))
	for _, row := range rows {
		if row.Model != model || row.SourceGraphDerivativeObjectID == "" || len(row.Vector) == 0 {
			return nil, ErrInvalidRequest
		}
		document := retrieve.TeamSemanticEmbeddingDocument{
			RootObjectID: row.RootObjectID, EmbeddingObjectID: row.DerivativeObjectID,
			SourceObjectID: row.SourceGraphDerivativeObjectID,
			PartitionKey:   row.PartitionKey, Model: row.Model,
			Vector: append([]float32(nil), row.Vector...),
		}
		result = append(result, document)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].SourceObjectID != result[right].SourceObjectID {
			return result[left].SourceObjectID < result[right].SourceObjectID
		}
		if result[left].RootObjectID != result[right].RootObjectID {
			return result[left].RootObjectID < result[right].RootObjectID
		}
		return result[left].EmbeddingObjectID < result[right].EmbeddingObjectID
	})
	return result, nil
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func optionalScore(value *float64, fallback float64) float64 {
	if value == nil {
		return fallback
	}
	return *value
}

func addTeamGraphLink(
	links map[string]retrieve.TeamGraphLink,
	rootObjectID, partitionKey, fromObjectID, toObjectID string,
	strength float64,
) {
	key := partitionKey + "\x00" + rootObjectID + "\x00" + fromObjectID + "\x00" + toObjectID
	if current, ok := links[key]; !ok || strength > current.Strength {
		links[key] = retrieve.TeamGraphLink{
			RootObjectID: rootObjectID, PartitionKey: partitionKey, FromObjectID: fromObjectID,
			ToObjectID: toObjectID, Strength: strength,
		}
	}
}

func sortTeamGraphContributions(corpus *retrieve.TeamAuthorizedCorpus) {
	sort.Slice(corpus.Entities, func(left, right int) bool {
		return teamContributionLess(corpus.Entities[left].ObjectID, corpus.Entities[left].RootObjectID,
			corpus.Entities[right].ObjectID, corpus.Entities[right].RootObjectID)
	})
	sort.Slice(corpus.Relations, func(left, right int) bool {
		return teamContributionLess(corpus.Relations[left].ObjectID, corpus.Relations[left].RootObjectID,
			corpus.Relations[right].ObjectID, corpus.Relations[right].RootObjectID)
	})
	sort.Slice(corpus.Facts, func(left, right int) bool {
		return teamContributionLess(corpus.Facts[left].ObjectID, corpus.Facts[left].RootObjectID,
			corpus.Facts[right].ObjectID, corpus.Facts[right].RootObjectID)
	})
	sort.Slice(corpus.Events, func(left, right int) bool {
		return teamContributionLess(corpus.Events[left].ObjectID, corpus.Events[left].RootObjectID,
			corpus.Events[right].ObjectID, corpus.Events[right].RootObjectID)
	})
	sort.Slice(corpus.Assertions, func(left, right int) bool {
		return teamContributionLess(corpus.Assertions[left].ObjectID, corpus.Assertions[left].RootObjectID,
			corpus.Assertions[right].ObjectID, corpus.Assertions[right].RootObjectID)
	})
}

func teamContributionLess(leftObjectID, leftRootObjectID, rightObjectID, rightRootObjectID string) bool {
	if leftObjectID != rightObjectID {
		return leftObjectID < rightObjectID
	}
	return leftRootObjectID < rightRootObjectID
}

func sortedTeamGraphLinks(values map[string]retrieve.TeamGraphLink) []retrieve.TeamGraphLink {
	result := make([]retrieve.TeamGraphLink, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].RootObjectID != result[right].RootObjectID {
			return result[left].RootObjectID < result[right].RootObjectID
		}
		if result[left].FromObjectID != result[right].FromObjectID {
			return result[left].FromObjectID < result[right].FromObjectID
		}
		return result[left].ToObjectID < result[right].ToObjectID
	})
	return result
}
