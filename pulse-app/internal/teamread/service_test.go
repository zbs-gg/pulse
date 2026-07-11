package teamread

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamauth"
)

type fakeTeamReadStore struct {
	filterRequests    []store.CandidateFilterRequest
	memoryQueries     []store.TeamTextReadQuery
	memoryEmbQueries  []store.TeamMemoryEmbeddingReadQuery
	graphQueries      []store.TeamTextReadQuery
	assertionQueries  []store.TeamTextReadQuery
	continuityQueries []store.TeamTextReadQuery
	semanticQueries   []store.TeamSemanticEmbeddingReadQuery
	recheckedRoots    [][]string

	memories           []store.TeamAuthorizedMemoryCapsule
	memoryEmbeddings   []store.TeamAuthorizedMemoryEmbedding
	graph              []store.TeamAuthorizedGraphContribution
	assertions         []store.TeamAuthorizedAssertionContribution
	continuity         []store.TeamAuthorizedContinuityCheckpoint
	semanticEmbeddings []store.TeamAuthorizedSemanticEmbedding
}

func (fake *fakeTeamReadStore) BuildAuthorizedCandidateFilter(
	_ context.Context,
	request store.CandidateFilterRequest,
) (store.AuthorizedCandidateFilter, error) {
	fake.filterRequests = append(fake.filterRequests, request)
	return store.AuthorizedCandidateFilter{}, nil
}

func (fake *fakeTeamReadStore) QueryAuthorizedTeamMemoryCapsules(
	_ context.Context,
	_ store.AuthorizedCandidateFilter,
	query store.TeamTextReadQuery,
) ([]store.TeamAuthorizedMemoryCapsule, error) {
	fake.memoryQueries = append(fake.memoryQueries, query)
	return append([]store.TeamAuthorizedMemoryCapsule(nil), fake.memories...), nil
}

func (fake *fakeTeamReadStore) QueryAuthorizedTeamMemoryEmbeddings(
	_ context.Context,
	_ store.AuthorizedCandidateFilter,
	query store.TeamMemoryEmbeddingReadQuery,
) ([]store.TeamAuthorizedMemoryEmbedding, error) {
	fake.memoryEmbQueries = append(fake.memoryEmbQueries, query)
	return append([]store.TeamAuthorizedMemoryEmbedding(nil), fake.memoryEmbeddings...), nil
}

func (fake *fakeTeamReadStore) QueryAuthorizedTeamGraphContributions(
	_ context.Context,
	_ store.AuthorizedCandidateFilter,
	query store.TeamTextReadQuery,
) ([]store.TeamAuthorizedGraphContribution, error) {
	fake.graphQueries = append(fake.graphQueries, query)
	return append([]store.TeamAuthorizedGraphContribution(nil), fake.graph...), nil
}

func (fake *fakeTeamReadStore) QueryAuthorizedTeamAssertionContributions(
	_ context.Context,
	_ store.AuthorizedCandidateFilter,
	query store.TeamTextReadQuery,
) ([]store.TeamAuthorizedAssertionContribution, error) {
	fake.assertionQueries = append(fake.assertionQueries, query)
	return append([]store.TeamAuthorizedAssertionContribution(nil), fake.assertions...), nil
}

func (fake *fakeTeamReadStore) QueryAuthorizedTeamContinuityCheckpoints(
	_ context.Context,
	_ store.AuthorizedCandidateFilter,
	query store.TeamTextReadQuery,
) ([]store.TeamAuthorizedContinuityCheckpoint, error) {
	fake.continuityQueries = append(fake.continuityQueries, query)
	return append([]store.TeamAuthorizedContinuityCheckpoint(nil), fake.continuity...), nil
}

func (fake *fakeTeamReadStore) QueryAuthorizedTeamSemanticEmbeddings(
	_ context.Context,
	_ store.AuthorizedCandidateFilter,
	query store.TeamSemanticEmbeddingReadQuery,
) ([]store.TeamAuthorizedSemanticEmbedding, error) {
	fake.semanticQueries = append(fake.semanticQueries, query)
	return append([]store.TeamAuthorizedSemanticEmbedding(nil), fake.semanticEmbeddings...), nil
}

func (fake *fakeTeamReadStore) RecheckAuthorizedCandidateRoots(
	_ context.Context,
	_ store.AuthorizedCandidateFilter,
	rootObjectIDs []string,
) error {
	fake.recheckedRoots = append(fake.recheckedRoots, append([]string(nil), rootObjectIDs...))
	return nil
}

func TestServiceRecallCompilesAuthorizationBeforeLoadingOnlyMemoryCorpus(t *testing.T) {
	fake := &fakeTeamReadStore{memories: []store.TeamAuthorizedMemoryCapsule{{
		RootObjectID: "root-visible", CapsuleID: "capsule-visible",
		PartitionKey: "partition-visible", Kind: "decision",
		RedactedSummary: "Use scoped retrieval", Confidence: 0.9,
		PrivacyTier: "sensitive", Retention: "project", Tags: []string{"pulse"},
	}}}
	service := newService(fake, retrieve.NewTeamRetrievalEngine(retrieve.TeamRetrievalConfig{
		CandidateLimit: 32,
	}))

	response, err := service.Recall(context.Background(), Authorization{
		PrincipalID: "principal-agent", TeamID: "team-pulse",
		Capabilities: []teamauth.Capability{teamauth.CapabilityRead},
	}, RecallRequest{
		Query: "scoped retrieval", Limit: 5, PrivacyCeiling: "sensitive",
		Retention: "project", ActiveContext: ActiveContext{
			ProjectID: "project-pulse", RepoID: "repo-pulse",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.filterRequests) != 1 {
		t.Fatalf("filter requests = %d", len(fake.filterRequests))
	}
	filter := fake.filterRequests[0]
	if filter.PrincipalID != "principal-agent" ||
		!reflect.DeepEqual(filter.Capabilities, []teamauth.Capability{teamauth.CapabilityRead}) ||
		filter.Context.TeamID != "team-pulse" || filter.Context.ProjectID != "project-pulse" ||
		filter.Context.RepoID != "repo-pulse" || filter.PrivacyCeiling != "sensitive" ||
		filter.Retention != "project" {
		t.Fatalf("compiled filter request = %+v", filter)
	}
	if len(fake.memoryQueries) != 1 || len(fake.graphQueries) != 0 ||
		len(fake.assertionQueries) != 0 || len(fake.continuityQueries) != 0 ||
		len(fake.semanticQueries) != 0 {
		t.Fatalf("recall repository calls = memory:%d graph:%d assertions:%d continuity:%d semantic:%d",
			len(fake.memoryQueries), len(fake.graphQueries), len(fake.assertionQueries),
			len(fake.continuityQueries), len(fake.semanticQueries))
	}
	if response.ReturnedCount != 1 || len(response.Items) != 1 ||
		response.Items[0].RootObjectID != "root-visible" ||
		response.Items[0].PrivacyTier != "sensitive" || response.Items[0].Retention != "project" {
		t.Fatalf("recall response = %+v", response)
	}
	if got, want := fake.recheckedRoots, [][]string{{"root-visible"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("final root recheck = %v, want %v", got, want)
	}
}

func TestServiceContextMapsVisibleContributionsAndDedupesSharedDerivative(t *testing.T) {
	confidence := 0.9
	strength := 0.8
	entityA := store.TeamGraphNode{
		ClientID: "node-a", Kind: "project", CanonicalName: "Pulse Alpha",
		Summary: graphString("Scoped memory project"), Salience: &confidence, Domain: "real",
	}
	entityB := store.TeamGraphNode{
		ClientID: "node-b", Kind: "person", CanonicalName: "Pulse Builder",
		Salience: &confidence, Domain: "real",
	}
	relation := store.TeamGraphEdge{
		From: "node-a", To: "node-b", Kind: "built_by",
		Summary: graphString("Pulse Alpha is built by Pulse Builder"), Strength: &strength,
	}
	fact := store.TeamGraphFact{
		Node: "node-a", Text: "Pulse uses pre-retrieval authorization",
		Predicate: graphString("authorization_boundary"), ObjectText: graphString("pre_retrieval"),
		Confidence: &confidence, Domain: "real",
	}
	event := store.TeamGraphEvent{
		ClientID: "event-a", Title: "Pulse U6 started",
		Summary: "Pulse scoped read implementation began", EntityRefs: []string{"node-a"},
		Confidence: &confidence, Domain: "real",
	}
	fake := &fakeTeamReadStore{
		graph: []store.TeamAuthorizedGraphContribution{
			{RootObjectID: "root-b", DerivativeObjectID: "entity-a", IntentID: "intent-b", PartitionKey: "partition-pulse", GraphKind: "graph_entity", Node: &entityA},
			{RootObjectID: "root-a", DerivativeObjectID: "entity-a", IntentID: "intent-a", PartitionKey: "partition-pulse", GraphKind: "graph_entity", Node: &entityA},
			{RootObjectID: "root-entity-b", DerivativeObjectID: "entity-b", IntentID: "intent-entity-b", PartitionKey: "partition-pulse", GraphKind: "graph_entity", Node: &entityB},
			{RootObjectID: "root-relation", DerivativeObjectID: "entity-a", IntentID: "intent-relation-entity-a", PartitionKey: "partition-pulse", GraphKind: "graph_entity", Node: &entityA},
			{RootObjectID: "root-relation", DerivativeObjectID: "entity-b", IntentID: "intent-relation-entity-b", PartitionKey: "partition-pulse", GraphKind: "graph_entity", Node: &entityB},
			{RootObjectID: "root-relation", DerivativeObjectID: "relation-a", IntentID: "intent-relation", PartitionKey: "partition-pulse", GraphKind: "graph_relation", Edge: &relation, ResolvedRefs: []string{"entity-a", "entity-b"}},
			{RootObjectID: "root-fact", DerivativeObjectID: "entity-a", IntentID: "intent-fact-entity-a", PartitionKey: "partition-pulse", GraphKind: "graph_entity", Node: &entityA},
			{RootObjectID: "root-fact", DerivativeObjectID: "fact-a", IntentID: "intent-fact", PartitionKey: "partition-pulse", GraphKind: "graph_fact", Fact: &fact, ResolvedRefs: []string{"entity-a"}},
			{RootObjectID: "root-event", DerivativeObjectID: "event-a", IntentID: "intent-event", PartitionKey: "partition-pulse", GraphKind: "graph_event", Event: &event, ResolvedRefs: []string{"entity-a"}},
		},
		assertions: []store.TeamAuthorizedAssertionContribution{{
			RootObjectID: "root-fact", DerivativeObjectID: "assertion-a",
			SourceGraphDerivativeObjectID: "fact-a", IntentID: "intent-assertion",
			PartitionKey: "partition-pulse", ClaimSlotDigest: strings.Repeat("a", 64),
			Claim: fact, SourceRefs: []string{"entity-a"},
			CreatedAt: "2026-07-11T05:00:00Z", VisibleContributionCount: 1,
		}},
	}
	service := newService(fake, retrieve.NewTeamRetrievalEngine(retrieve.TeamRetrievalConfig{
		CandidateLimit: 64,
	}))

	response, err := service.Context(context.Background(), Authorization{
		PrincipalID: "principal-agent", TeamID: "team-pulse",
		Capabilities: []teamauth.Capability{teamauth.CapabilityRead},
	}, ContextRequest{
		Query: "pulse", Limit: 10, PrivacyCeiling: "normal",
		ActiveContext: ActiveContext{ProjectID: "project-pulse"},
		IncludeTrace:  true, GraphMode: "anchored",
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Counts.Entities != 2 || response.Counts.Relations != 1 ||
		response.Counts.Facts != 1 || response.Counts.Events != 1 ||
		response.Counts.Assertions != 1 || response.Counts.Total != 6 {
		t.Fatalf("context counts = %+v response=%+v", response.Counts, response)
	}
	if response.Entities[0].ObjectID != "entity-a" || response.Entities[0].RootObjectID != "root-a" {
		t.Fatalf("shared derivative selection = %+v", response.Entities)
	}
	if response.Relations[0].FromObjectID != "entity-a" || response.Relations[0].ToObjectID != "entity-b" ||
		response.Assertions[0].CandidateObjectID != "fact-a" ||
		response.Assertions[0].SubjectObjectID != "entity-a" {
		t.Fatalf("resolved context references = relation:%+v assertion:%+v",
			response.Relations, response.Assertions)
	}
	if len(fake.graphQueries) != 1 || len(fake.assertionQueries) != 1 ||
		len(fake.memoryQueries) != 0 || len(fake.continuityQueries) != 0 {
		t.Fatalf("context repository calls = memory:%d graph:%d assertions:%d continuity:%d",
			len(fake.memoryQueries), len(fake.graphQueries), len(fake.assertionQueries), len(fake.continuityQueries))
	}
}

func TestServiceResumeBuildsAuthorizedContinuityWithoutLocalState(t *testing.T) {
	fake := &fakeTeamReadStore{continuity: []store.TeamAuthorizedContinuityCheckpoint{{
		RootObjectID: "root-continuity", DerivativeObjectID: "continuity-a",
		IntentID: "intent-continuity", PartitionKey: "partition-pulse",
		Checkpoint: store.TeamGraphContinuity{
			ThreadID: "thread-pulse", SessionID: "session-pulse",
			Summary:          "Stopped after scoped retrieval.",
			Decisions:        []string{"Filter before ranking."},
			OpenLoops:        []string{"Wire server routes."},
			DoNotRepeat:      []string{"Do not use local hybrid retrieval."},
			EmotionalAnchors: []string{"Calm after the boundary locked."},
			ReviewInsights:   []string{"Verify the real request chain."},
		},
		CreatedAt: "2026-07-11T05:00:00Z", VisibleContributionCount: 1,
	}}}
	service := newService(fake, retrieve.NewTeamRetrievalEngine(retrieve.TeamRetrievalConfig{
		CandidateLimit: 32,
	}))

	response, err := service.Resume(context.Background(), Authorization{
		PrincipalID: "principal-agent", TeamID: "team-pulse",
		Capabilities: []teamauth.Capability{teamauth.CapabilityRead},
	}, ResumeRequest{
		ThreadID: "thread-pulse", Limit: 20,
		ActiveContext: ActiveContext{ProjectID: "project-pulse", SessionID: "session-pulse"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.ReturnedCount != 6 || len(response.WhereWeLeftOff) != 1 ||
		response.WhereWeLeftOff[0].RootObjectID != "root-continuity" ||
		len(response.SuggestedNextStep) != 1 ||
		response.SuggestedNextStep[0].Text != "Verify the real request chain." {
		t.Fatalf("resume response = %+v", response)
	}
	if len(fake.filterRequests) != 1 || fake.filterRequests[0].PrivacyCeiling != "normal" ||
		fake.filterRequests[0].Context.SessionID != "session-pulse" ||
		len(fake.continuityQueries) != 1 || len(fake.graphQueries) != 0 ||
		len(fake.memoryQueries) != 0 {
		t.Fatalf("resume authorization/calls = filter:%+v continuity:%v",
			fake.filterRequests, fake.continuityQueries)
	}
}

func graphString(value string) *string { return &value }
