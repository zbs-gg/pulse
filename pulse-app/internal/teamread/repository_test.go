package teamread

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
)

func TestMapAuthorizedGraphCorpusPreservesContributionRootsAndGraphLineage(t *testing.T) {
	confidence := 0.9
	strength := 0.8
	objectText := "pre_retrieval"
	predicate := "authorization_boundary"
	entity := store.TeamGraphNode{
		ClientID: "node-a", Kind: "project", CanonicalName: "Pulse",
		Salience: &confidence, Domain: "real",
	}
	relation := store.TeamGraphEdge{
		From: "node-a", To: "node-b", Kind: "connects", Strength: &strength,
	}
	claim := store.TeamGraphFact{
		Node: "node-a", Text: "Pulse authorizes before ranking",
		Predicate: &predicate, ObjectText: &objectText, Confidence: &confidence, Domain: "real",
	}
	graphRows := []store.TeamAuthorizedGraphContribution{
		{RootObjectID: "root-entity-b", DerivativeObjectID: "entity-a", PartitionKey: "project:pulse", GraphKind: "graph_entity", Node: &entity},
		{RootObjectID: "root-entity-a", DerivativeObjectID: "entity-a", PartitionKey: "project:pulse", GraphKind: "graph_entity", Node: &entity},
		{RootObjectID: "root-relation-b", DerivativeObjectID: "relation-a", PartitionKey: "project:pulse", GraphKind: "graph_relation", Edge: &relation, ResolvedRefs: []string{"entity-a", "entity-b"}},
		{RootObjectID: "root-relation-a", DerivativeObjectID: "relation-a", PartitionKey: "project:pulse", GraphKind: "graph_relation", Edge: &relation, ResolvedRefs: []string{"entity-a", "entity-b"}},
	}
	assertionRows := []store.TeamAuthorizedAssertionContribution{
		{RootObjectID: "root-assertion-b", DerivativeObjectID: "assertion-a", SourceGraphDerivativeObjectID: "fact-a", PartitionKey: "project:pulse", ClaimSlotDigest: strings.Repeat("a", 64), Claim: claim, SourceRefs: []string{"entity-a"}, CreatedAt: "2026-07-11T05:00:00Z"},
		{RootObjectID: "root-assertion-a", DerivativeObjectID: "assertion-a", SourceGraphDerivativeObjectID: "fact-a", PartitionKey: "project:pulse", ClaimSlotDigest: strings.Repeat("a", 64), Claim: claim, SourceRefs: []string{"entity-a"}, CreatedAt: "2026-07-11T06:00:00Z"},
	}

	corpus, err := mapAuthorizedGraphCorpus(graphRows, assertionRows)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := contributionRoots(corpus.Entities), []string{"root-entity-a", "root-entity-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("entity contribution roots = %v, want %v", got, want)
	}
	if got, want := relationContributionRoots(corpus.Relations), []string{"root-relation-a", "root-relation-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("relation contribution roots = %v, want %v", got, want)
	}
	if got, want := assertionContributionRoots(corpus.Assertions), []string{"root-assertion-a", "root-assertion-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("assertion contribution roots = %v, want %v", got, want)
	}
	if len(corpus.GraphLinks) != 4 {
		t.Fatalf("graph links = %+v, want four relation-contribution links", corpus.GraphLinks)
	}
	for _, link := range corpus.GraphLinks {
		if link.RootObjectID != "root-relation-a" && link.RootObjectID != "root-relation-b" {
			t.Fatalf("non-graph contribution leaked into graph lineage: %+v", link)
		}
	}
}

func TestAuthorizedRepositoryPreservesContinuityAndSemanticContributions(t *testing.T) {
	fake := &fakeTeamReadStore{
		continuity: []store.TeamAuthorizedContinuityCheckpoint{
			{RootObjectID: "root-old", DerivativeObjectID: "continuity-a", PartitionKey: "project:pulse", Checkpoint: store.TeamGraphContinuity{ThreadID: "thread-a", Summary: "Old"}, CreatedAt: "2026-07-11T05:00:00Z"},
			{RootObjectID: "root-new", DerivativeObjectID: "continuity-a", PartitionKey: "project:pulse", Checkpoint: store.TeamGraphContinuity{ThreadID: "thread-a", Summary: "New"}, CreatedAt: "2026-07-11T06:00:00Z"},
		},
	}
	repository := &authorizedRepository{store: fake}
	documents, err := repository.LoadAuthorizedContinuity(context.Background(), retrieve.TeamResumeQuery{
		ThreadID: "thread-a", ProjectID: "project-pulse", Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := continuityRoots(documents), []string{"root-new", "root-old"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("continuity contribution roots = %v, want %v", got, want)
	}

	embeddings, err := mapAuthorizedSemanticEmbeddings([]store.TeamAuthorizedSemanticEmbedding{
		{RootObjectID: "root-b", DerivativeObjectID: "embedding-b", SourceGraphDerivativeObjectID: "entity-a", PartitionKey: "project:pulse", Model: "bge-m3:test", Vector: []float32{0, 1}},
		{RootObjectID: "root-a", DerivativeObjectID: "embedding-a", SourceGraphDerivativeObjectID: "entity-a", PartitionKey: "project:pulse", Model: "bge-m3:test", Vector: []float32{1, 0}},
	}, "bge-m3:test")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := semanticRoots(embeddings), []string{"root-a", "root-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("semantic contribution roots = %v, want %v", got, want)
	}
}

func TestAuthorizedRepositoryPromotesProjectedAirlockPublicationIntoNewSessionContinuity(t *testing.T) {
	fake := &fakeTeamReadStore{memories: []store.TeamAuthorizedMemoryCapsule{{
		RootObjectID: "published-root", CapsuleID: "published-capsule",
		PartitionKey: "project:shared", Kind: "decision",
		RedactedSummary: "Use the approved shared continuity rule.",
		Confidence:      1, EvidenceHint: "user_confirmed", PrivacyTier: "normal", Retention: "long_term",
		SourceHost: "pulse-cli", ConversationScope: "user_selected_excerpt",
		SourceTimestamp: "2026-07-15T03:00:00Z", CreatedAt: "2026-07-15T03:00:00Z",
		PublicationID: "publication-approved", PublicationEventObjectID: "published-event",
		PublicationCreatedAt: "2026-07-15T03:00:01Z",
	}}}
	repository := &authorizedRepository{store: fake}
	documents, err := repository.LoadAuthorizedContinuity(context.Background(), retrieve.TeamResumeQuery{
		ThreadID: "repository-compositor-bound", ProjectID: "project-shared",
		SessionID: "session-new", Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 || documents[0].RootObjectID != "published-root" ||
		documents[0].ObjectID != "published-event" || documents[0].ProjectID != "project-shared" ||
		documents[0].ThreadID != "repository-compositor-bound" ||
		documents[0].SessionID != "session-new" ||
		documents[0].Summary != "Use the approved shared continuity rule." ||
		!reflect.DeepEqual(documents[0].Decisions, []string{"Use the approved shared continuity rule."}) {
		t.Fatalf("published continuity = %+v", documents)
	}
}

func TestAuthorizedRepositoryMergesNewPublicationWhenCheckpointsFillLimit(t *testing.T) {
	fake := &fakeTeamReadStore{
		continuity: []store.TeamAuthorizedContinuityCheckpoint{
			{RootObjectID: "checkpoint-new", DerivativeObjectID: "continuity-new", PartitionKey: "project:shared", Checkpoint: store.TeamGraphContinuity{ThreadID: "thread-new", Summary: "New checkpoint"}, CreatedAt: "2026-07-15T03:00:02Z"},
			{RootObjectID: "checkpoint-old", DerivativeObjectID: "continuity-old", PartitionKey: "project:shared", Checkpoint: store.TeamGraphContinuity{ThreadID: "thread-old", Summary: "Old checkpoint"}, CreatedAt: "2026-07-15T03:00:01Z"},
		},
		memories: []store.TeamAuthorizedMemoryCapsule{{
			RootObjectID: "publication-newest", CapsuleID: "publication-capsule",
			PartitionKey: "project:shared", Kind: "decision",
			RedactedSummary: "Newest approved publication.",
			PublicationID:   "publication-newest", PublicationEventObjectID: "publication-event",
			PublicationCreatedAt: "2026-07-15T03:00:03Z",
		}},
	}
	repository := &authorizedRepository{store: fake}
	documents, err := repository.LoadAuthorizedContinuity(context.Background(), retrieve.TeamResumeQuery{
		ProjectID: "project-shared", SessionID: "session-new", Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := continuityRoots(documents), []string{"publication-newest", "checkpoint-new"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("merged continuity roots = %v, want %v", got, want)
	}
	if len(fake.memoryQueries) != 1 || fake.memoryQueries[0].Limit != 2 {
		t.Fatalf("publication query = %+v, want independent limit 2", fake.memoryQueries)
	}
}

func TestAuthorizedRepositoryDeduplicatesMergedContinuityDeterministically(t *testing.T) {
	fake := &fakeTeamReadStore{continuity: []store.TeamAuthorizedContinuityCheckpoint{
		{RootObjectID: "root-duplicate", DerivativeObjectID: "continuity-duplicate", PartitionKey: "project:shared", Checkpoint: store.TeamGraphContinuity{ThreadID: "thread-duplicate", Summary: "Zulu summary"}, CreatedAt: "2026-07-15T03:00:00Z"},
		{RootObjectID: "root-duplicate", DerivativeObjectID: "continuity-duplicate", PartitionKey: "project:shared", Checkpoint: store.TeamGraphContinuity{ThreadID: "thread-duplicate", Summary: "Alpha summary"}, CreatedAt: "2026-07-15T03:00:00Z"},
	}}
	repository := &authorizedRepository{store: fake}
	documents, err := repository.LoadAuthorizedContinuity(context.Background(), retrieve.TeamResumeQuery{
		ProjectID: "project-shared", Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 || documents[0].Summary != "Alpha summary" {
		t.Fatalf("deduplicated continuity = %+v", documents)
	}
}

func TestAuthorizedRepositoryQueriesSemanticEmbeddingsForExactSelectedGraphSources(t *testing.T) {
	confidence := 0.9
	entity := store.TeamGraphNode{
		ClientID: "entity", Kind: "project", CanonicalName: "Pulse",
		Salience: &confidence, Domain: "real",
	}
	fake := &fakeTeamReadStore{graph: []store.TeamAuthorizedGraphContribution{
		{RootObjectID: "root-b", DerivativeObjectID: "entity-a", PartitionKey: "project:pulse", GraphKind: "graph_entity", Node: &entity},
		{RootObjectID: "root-a", DerivativeObjectID: "entity-a", PartitionKey: "project:pulse", GraphKind: "graph_entity", Node: &entity},
		{RootObjectID: "root-a", DerivativeObjectID: "entity-a", PartitionKey: "project:pulse", GraphKind: "graph_entity", Node: &entity},
		{RootObjectID: "root-c", DerivativeObjectID: "entity-z", PartitionKey: "project:pulse", GraphKind: "graph_entity", Node: &entity},
	}}
	repository := &authorizedRepository{store: fake}
	if _, err := repository.LoadAuthorizedCorpus(context.Background(), retrieve.TeamCorpusQuery{
		Surface: retrieve.TeamCorpusSurfaceContext, Limit: 10, EmbeddingModel: "bge-m3:test",
	}); err != nil {
		t.Fatal(err)
	}
	if len(fake.semanticQueries) != 1 {
		t.Fatalf("semantic queries = %+v", fake.semanticQueries)
	}
	want := store.TeamSemanticEmbeddingReadQuery{
		Model: "bge-m3:test",
		Sources: []store.TeamSemanticEmbeddingReadKey{
			{RootObjectID: "root-a", SourceGraphDerivativeObjectID: "entity-a"},
			{RootObjectID: "root-b", SourceGraphDerivativeObjectID: "entity-a"},
			{RootObjectID: "root-c", SourceGraphDerivativeObjectID: "entity-z"},
		},
	}
	if !reflect.DeepEqual(fake.semanticQueries[0], want) {
		t.Fatalf("semantic query = %+v, want %+v", fake.semanticQueries[0], want)
	}
}

func TestSelectedSemanticEmbeddingSourcesFailsClosedAboveStoreBound(t *testing.T) {
	rows := make([]store.TeamAuthorizedGraphContribution, store.MaxTeamSemanticEmbeddingReadSources+1)
	for index := range rows {
		rows[index] = store.TeamAuthorizedGraphContribution{
			RootObjectID:       "root-bound-" + string(rune(0x1000+index)),
			DerivativeObjectID: "source-bound-" + string(rune(0x5000+index)),
		}
	}
	if sources, err := selectedSemanticEmbeddingSources(rows); err == nil {
		t.Fatalf("over-bound sources = %d, want fail-closed error", len(sources))
	}
}

func contributionRoots(values []retrieve.TeamEntityDocument) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.RootObjectID
	}
	return result
}

func relationContributionRoots(values []retrieve.TeamRelationDocument) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.RootObjectID
	}
	return result
}

func assertionContributionRoots(values []retrieve.TeamAssertionDocument) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.RootObjectID
	}
	return result
}

func continuityRoots(values []retrieve.TeamContinuityDocument) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.RootObjectID
	}
	return result
}

func semanticRoots(values []retrieve.TeamSemanticEmbeddingDocument) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.RootObjectID
	}
	return result
}
