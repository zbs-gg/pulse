package retrieve

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/nkkmnk/pulse/internal/embed"
)

type fakeTeamAuthorizedRepository struct {
	corpus               TeamAuthorizedCorpus
	continuity           []TeamContinuityDocument
	recheckErr           error
	corpusQueries        []TeamCorpusQuery
	continuityQueries    []TeamResumeQuery
	recheckedRootSets    [][]string
	unreachableRootIDs   []string
	unauthorizedMemories []TeamMemoryDocument
	honorContinuityLimit bool
}

func (repository *fakeTeamAuthorizedRepository) LoadAuthorizedCorpus(
	_ context.Context,
	query TeamCorpusQuery,
) (TeamAuthorizedCorpus, error) {
	repository.corpusQueries = append(repository.corpusQueries, query)
	corpus := cloneTeamAuthorizedCorpus(repository.corpus)
	corpus.Memories = append(corpus.Memories, repository.unauthorizedMemories...)
	if len(repository.unreachableRootIDs) != 0 {
		denied := make(map[string]bool, len(repository.unreachableRootIDs))
		for _, rootObjectID := range repository.unreachableRootIDs {
			denied[rootObjectID] = true
		}
		visible := corpus.Memories[:0]
		for _, memory := range corpus.Memories {
			if !denied[memory.RootObjectID] {
				visible = append(visible, memory)
			}
		}
		corpus.Memories = visible
	}
	return corpus, nil
}

func (repository *fakeTeamAuthorizedRepository) LoadAuthorizedContinuity(
	_ context.Context,
	query TeamResumeQuery,
) ([]TeamContinuityDocument, error) {
	repository.continuityQueries = append(repository.continuityQueries, query)
	documents := cloneTeamContinuityDocuments(repository.continuity)
	if repository.honorContinuityLimit && len(documents) > query.Limit {
		documents = documents[:query.Limit]
	}
	return documents, nil
}

func (repository *fakeTeamAuthorizedRepository) RecheckAuthorizedRoots(
	_ context.Context,
	rootObjectIDs []string,
) error {
	repository.recheckedRootSets = append(
		repository.recheckedRootSets,
		append([]string(nil), rootObjectIDs...),
	)
	return repository.recheckErr
}

type fakeTeamQueryEmbedder struct {
	model  string
	vector []float32
}

func (embedder fakeTeamQueryEmbedder) Embed(
	_ context.Context,
	texts []string,
	_ embed.InputType,
) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for index := range result {
		result[index] = append([]float32(nil), embedder.vector...)
	}
	return result, nil
}

func (embedder fakeTeamQueryEmbedder) Model() string { return embedder.model }

type countingTeamQueryEmbedder struct {
	model  string
	vector []float32
	calls  *int
}

func (embedder countingTeamQueryEmbedder) Embed(
	_ context.Context,
	texts []string,
	_ embed.InputType,
) ([][]float32, error) {
	*embedder.calls += len(texts)
	result := make([][]float32, len(texts))
	for index := range result {
		result[index] = append([]float32(nil), embedder.vector...)
	}
	return result, nil
}

func (embedder countingTeamQueryEmbedder) Model() string { return embedder.model }

func TestTeamRetrieveHiddenHighScoreCannotAffectRankCountOrTrace(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{
		Embedder:       fakeTeamQueryEmbedder{model: "bge-m3:test", vector: []float32{1, 0}},
		CandidateLimit: 32,
	})
	visibleCorpus := TeamAuthorizedCorpus{Memories: []TeamMemoryDocument{
		{
			DocumentID: "memory-visible-a", RootObjectID: "root-visible-a",
			PartitionKey: "project:visible", Kind: "decision",
			RedactedSummary: "Pulse pilot decision", Confidence: 0.9,
			PrivacyTier: "normal", Retention: "project", Tags: []string{"pilot"},
			EmbeddingModel: "bge-m3:test", Embedding: []float32{1, 0},
		},
		{
			DocumentID: "memory-visible-b", RootObjectID: "root-visible-b",
			PartitionKey: "project:visible", Kind: "open_loop",
			RedactedSummary: "Pilot rollout notes", Confidence: 0.8,
			PrivacyTier: "normal", Retention: "project", Tags: []string{},
			EmbeddingModel: "bge-m3:test", Embedding: []float32{0.8, 0.2},
		},
	}}
	baselineRepository := &fakeTeamAuthorizedRepository{corpus: visibleCorpus}
	withHiddenRepository := &fakeTeamAuthorizedRepository{
		corpus: visibleCorpus,
		unauthorizedMemories: []TeamMemoryDocument{{
			DocumentID: "memory-hidden-perfect", RootObjectID: "root-hidden-perfect",
			PartitionKey: "project:hidden", Kind: "decision",
			RedactedSummary: "Pulse pilot", Confidence: 1,
			PrivacyTier: "normal", Retention: "project", Tags: []string{"hidden"},
			EmbeddingModel: "bge-m3:test", Embedding: []float32{1, 0},
		}},
		unreachableRootIDs: []string{"root-hidden-perfect"},
	}

	request := TeamRetrievalRequest{Query: "pulse pilot", TopK: 2}
	baseline, err := engine.Retrieve(context.Background(), baselineRepository, request)
	if err != nil {
		t.Fatal(err)
	}
	withHidden, err := engine.Retrieve(context.Background(), withHiddenRepository, request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(withHidden, baseline) {
		t.Fatalf("hidden candidate changed response:\nbaseline=%+v\nhidden=%+v", baseline, withHidden)
	}
	if withHidden.ReturnedCount != 2 || len(withHidden.Items) != 2 || len(withHidden.Trace) != 2 {
		t.Fatalf("response shape = %+v", withHidden)
	}
	for _, item := range withHidden.Items {
		if item.RootObjectID == "root-hidden-perfect" {
			t.Fatalf("hidden root escaped in item %+v", item)
		}
	}
	for _, trace := range withHidden.Trace {
		if trace.RootObjectID == "root-hidden-perfect" {
			t.Fatalf("hidden root escaped in trace %+v", trace)
		}
	}
	if got, want := withHiddenRepository.recheckedRootSets, [][]string{{"root-visible-a", "root-visible-b"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fresh root recheck = %v, want %v", got, want)
	}
	if len(withHiddenRepository.corpusQueries) != 1 ||
		withHiddenRepository.corpusQueries[0].Surface != TeamCorpusSurfaceRecall ||
		withHiddenRepository.corpusQueries[0].EmbeddingModel != "bge-m3:test" {
		t.Fatalf("recall corpus contract = %+v", withHiddenRepository.corpusQueries)
	}
}

func TestTeamContextAssertionReductionUsesVisibleSamePartitionContributions(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{CandidateLimit: 32})
	repository := &fakeTeamAuthorizedRepository{corpus: TeamAuthorizedCorpus{
		Entities: []TeamEntityDocument{
			{
				RootObjectID: "root-old", ObjectID: "entity-person", PartitionKey: "project:pulse",
				EntityKind: "person", Name: "Subject", Confidence: 0.9,
			},
			{
				RootObjectID: "root-current", ObjectID: "entity-person", PartitionKey: "project:pulse",
				EntityKind: "person", Name: "Subject", Confidence: 0.9,
			},
			{
				RootObjectID: "root-cross", ObjectID: "entity-person-atlas", PartitionKey: "project:atlas",
				EntityKind: "person", Name: "Atlas person", Confidence: 0.9,
			},
		},
		Facts: []TeamFactDocument{
			{
				RootObjectID: "root-old", ObjectID: "fact-old", PartitionKey: "project:pulse",
				NodeObjectID: "entity-person", Text: "Pulse location old", Predicate: "home_base",
				ObjectText: "Lisbon", Confidence: 0.9, Domain: "real",
			},
			{
				RootObjectID: "root-current", ObjectID: "fact-current", PartitionKey: "project:pulse",
				NodeObjectID: "entity-person", Text: "Pulse location current", Predicate: "home_base",
				ObjectText: "Bangkok", Confidence: 0.9, Domain: "real",
			},
			{
				RootObjectID: "root-cross", ObjectID: "fact-cross", PartitionKey: "project:atlas",
				NodeObjectID: "entity-person-atlas", Text: "Pulse location cross", Predicate: "home_base",
				ObjectText: "Tokyo", Confidence: 0.9, Domain: "real",
			},
		},
		Assertions: []TeamAssertionDocument{
			{
				RootObjectID: "root-old", ObjectID: "assertion-old", PartitionKey: "project:pulse",
				CandidateObjectID: "fact-old", SubjectObjectID: "entity-person", ClaimSlotDigest: "slot-home",
				Predicate: "home_base", ObjectText: "Lisbon",
				Text: "Old home base", ObservedAtUnixMilli: 100, Confidence: 0.9,
			},
			{
				RootObjectID: "root-current", ObjectID: "assertion-current", PartitionKey: "project:pulse",
				CandidateObjectID: "fact-current", SubjectObjectID: "entity-person", ClaimSlotDigest: "slot-home",
				Predicate: "home_base", ObjectText: "Bangkok",
				Text: "Current home base", ObservedAtUnixMilli: 200, Confidence: 0.9,
			},
			{
				RootObjectID: "root-hidden", ObjectID: "assertion-hidden", PartitionKey: "project:pulse",
				CandidateObjectID: "fact-hidden", SubjectObjectID: "entity-person", ClaimSlotDigest: "slot-home",
				Predicate: "home_base", ObjectText: "Hidden",
				Text: "Unauthorized newer home base", ObservedAtUnixMilli: 300, Confidence: 1,
			},
			{
				RootObjectID: "root-cross", ObjectID: "assertion-cross", PartitionKey: "project:atlas",
				CandidateObjectID: "fact-cross", SubjectObjectID: "entity-person-atlas", ClaimSlotDigest: "slot-home",
				Predicate: "home_base", ObjectText: "Tokyo",
				Text: "Other project home base", ObservedAtUnixMilli: 400, Confidence: 1,
			},
		},
	}}

	response, err := engine.Context(context.Background(), repository, TeamContextRequest{
		Query: "pulse location", TopK: 10, IncludeTrace: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := contextFactObjectIDs(response.Facts), []string{"fact-cross", "fact-current", "fact-old"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fact order = %v, want %v", got, want)
	}
	if response.Counts.Facts != 3 || response.Counts.Total != 3 || len(response.Trace) != 3 {
		t.Fatalf("context counts/trace = %+v trace=%+v", response.Counts, response.Trace)
	}
	states := make(map[string]string, len(response.Trace))
	for _, trace := range response.Trace {
		states[trace.ObjectID] = trace.AssertionState
		if trace.RootObjectID == "root-hidden" || trace.ObjectID == "fact-hidden" ||
			trace.ObjectID == "assertion-hidden" {
			t.Fatalf("hidden assertion influenced trace %+v", trace)
		}
	}
	if states["fact-old"] != "superseded" || states["fact-current"] != "current" ||
		states["fact-cross"] != "current" {
		t.Fatalf("partitioned assertion states = %v", states)
	}
	if got, want := repository.recheckedRootSets, [][]string{{"root-cross", "root-current", "root-old"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("context root recheck = %v, want %v", got, want)
	}
}

func TestTeamContextGraphTraversalStaysInsideAuthorizedPartition(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{CandidateLimit: 32})
	repository := &fakeTeamAuthorizedRepository{corpus: TeamAuthorizedCorpus{
		Entities: []TeamEntityDocument{{
			RootObjectID: "root-seed", ObjectID: "entity-alpha", PartitionKey: "project:pulse",
			EntityKind: "project", Name: "Alpha", Summary: "Alpha project",
		}},
		Events: []TeamEventDocument{
			{
				RootObjectID: "root-neighbor", ObjectID: "event-neighbor", PartitionKey: "project:pulse",
				Title: "Beta", Summary: "Unrelated neighboring event", Confidence: 0.8, Domain: "real",
			},
			{
				RootObjectID: "root-cross", ObjectID: "event-cross", PartitionKey: "project:atlas",
				Title: "Gamma", Summary: "Cross partition event", Confidence: 0.8, Domain: "real",
			},
		},
		GraphLinks: []TeamGraphLink{
			{RootObjectID: "root-neighbor", PartitionKey: "project:pulse", FromObjectID: "entity-alpha", ToObjectID: "event-neighbor", Strength: 1},
			{RootObjectID: "root-cross", PartitionKey: "project:pulse", FromObjectID: "entity-alpha", ToObjectID: "event-cross", Strength: 1},
			{RootObjectID: "root-hidden", PartitionKey: "project:pulse", FromObjectID: "entity-alpha", ToObjectID: "event-hidden", Strength: 1},
		},
	}}

	response, err := engine.Context(context.Background(), repository, TeamContextRequest{
		Query: "alpha", TopK: 4, GraphMode: TeamGraphModeAnchored, IncludeTrace: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := contextEntityObjectIDs(response.Entities), []string{"entity-alpha"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("entity results = %v, want %v", got, want)
	}
	if got, want := contextEventObjectIDs(response.Events), []string{"event-neighbor"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("event results = %v, want %v", got, want)
	}
	if response.Counts.Total != 2 || len(response.Trace) != 2 {
		t.Fatalf("graph context = %+v", response)
	}
	for _, trace := range response.Trace {
		if trace.ObjectID == "event-cross" || trace.ObjectID == "event-hidden" || trace.RootObjectID == "root-cross" {
			t.Fatalf("graph crossed authorization partition: %+v", trace)
		}
	}
}

func TestTeamContextRechecksNonReturnedGraphRootsThatInfluenceResults(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{CandidateLimit: 32})
	corpus := TeamAuthorizedCorpus{
		Entities: []TeamEntityDocument{
			{
				RootObjectID: "root-seed", ObjectID: "entity-seed", PartitionKey: "project:pulse",
				EntityKind: "project", Name: "Alpha", Summary: "Lexical seed", Confidence: 0.9,
			},
			{
				RootObjectID: "root-endpoint", ObjectID: "entity-endpoint", PartitionKey: "project:pulse",
				EntityKind: "person", Name: "Endpoint", Summary: "Graph-only result", Confidence: 0.9,
			},
		},
		Relations: []TeamRelationDocument{{
			RootObjectID: "root-bridge", ObjectID: "relation-bridge", PartitionKey: "project:pulse",
			FromObjectID: "entity-seed", ToObjectID: "entity-endpoint",
			RelationKind: "connects", Summary: "Graph bridge", Strength: 1, Confidence: 0.9,
		}},
		GraphLinks: []TeamGraphLink{
			{
				RootObjectID: "root-bridge", PartitionKey: "project:pulse",
				FromObjectID: "entity-seed", ToObjectID: "relation-bridge", Strength: 1,
			},
			{
				RootObjectID: "root-bridge", PartitionKey: "project:pulse",
				FromObjectID: "relation-bridge", ToObjectID: "entity-endpoint", Strength: 1,
			},
		},
	}
	anchoredRepository := &fakeTeamAuthorizedRepository{corpus: corpus}
	anchored, err := engine.Context(context.Background(), anchoredRepository, TeamContextRequest{
		Query: "alpha", TopK: 2, GraphMode: TeamGraphModeAnchored, IncludeTrace: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := contextEntityObjectIDs(anchored.Entities), []string{"entity-seed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("anchored mode crossed into indirect endpoint: got %v want %v; response=%+v", got, want, anchored)
	}
	if got, want := anchoredRepository.recheckedRootSets, [][]string{{"root-seed"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("anchored influence roots = %v, want %v", got, want)
	}

	repository := &fakeTeamAuthorizedRepository{corpus: corpus}
	response, err := engine.Context(context.Background(), repository, TeamContextRequest{
		Query: "alpha", TopK: 2, GraphMode: TeamGraphModeWalk, IncludeTrace: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := contextEntityObjectIDs(response.Entities), []string{"entity-seed", "entity-endpoint"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("graph-only endpoint result = %v, want %v; response=%+v", got, want, response)
	}
	if len(response.Relations) != 0 {
		t.Fatalf("bridge should influence without being returned: %+v", response.Relations)
	}
	if got, want := repository.recheckedRootSets, [][]string{{"root-bridge", "root-endpoint", "root-seed"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("graph influence roots = %v, want %v", got, want)
	}
}

func TestTeamContextDanglingRelationCannotSeedGraphInfluence(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{CandidateLimit: 32})
	visibleCorpus := TeamAuthorizedCorpus{Events: []TeamEventDocument{{
		RootObjectID: "root-visible", ObjectID: "event-visible", PartitionKey: "project:pulse",
		Title: "Visible event", Summary: "Unrelated text", Confidence: 0.9, Domain: "real",
	}}}
	withDangling := cloneTeamAuthorizedCorpus(visibleCorpus)
	withDangling.Relations = []TeamRelationDocument{{
		RootObjectID: "root-dangling", ObjectID: "relation-dangling", PartitionKey: "project:pulse",
		FromObjectID: "entity-missing", ToObjectID: "entity-also-missing",
		RelationKind: "needle", Summary: "Needle dangling relation", Strength: 1, Confidence: 0.9,
	}}
	withDangling.GraphLinks = []TeamGraphLink{{
		RootObjectID: "root-dangling", PartitionKey: "project:pulse",
		FromObjectID: "relation-dangling", ToObjectID: "event-visible", Strength: 1,
	}}
	baselineRepository := &fakeTeamAuthorizedRepository{corpus: visibleCorpus}
	danglingRepository := &fakeTeamAuthorizedRepository{corpus: withDangling}
	request := TeamContextRequest{Query: "needle", TopK: 1, GraphMode: TeamGraphModeWalk, IncludeTrace: true}

	baseline, err := engine.Context(context.Background(), baselineRepository, request)
	if err != nil {
		t.Fatal(err)
	}
	withDanglingResponse, err := engine.Context(context.Background(), danglingRepository, request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(withDanglingResponse, baseline) {
		t.Fatalf("dangling relation influenced graph:\nbaseline=%+v\ndangling=%+v", baseline, withDanglingResponse)
	}
	if got, want := danglingRepository.recheckedRootSets, [][]string{nil}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dangling graph roots rechecked = %v, want %v", got, want)
	}
}

func TestTeamResumeSelectsLatestAuthorizedContinuityAndCountsExactSections(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{CandidateLimit: 32})
	repository := &fakeTeamAuthorizedRepository{continuity: []TeamContinuityDocument{
		{
			RootObjectID: "root-older", ObjectID: "continuity-older", PartitionKey: "project:pulse",
			ThreadID: "thread-pilot", ProjectID: "project-pulse", SessionID: "session-old",
			Summary: "Older checkpoint", Decisions: []string{"Old decision"},
			UpdatedAtUnixMilli: 100,
		},
		{
			RootObjectID: "root-latest", ObjectID: "continuity-latest", PartitionKey: "project:pulse",
			ThreadID: "thread-pilot", ProjectID: "project-pulse", SessionID: "session-new",
			Summary:               "Stopped after the scoped retrieval contract.",
			Decisions:             []string{"Use a bound authorized repository.", "Return root IDs only."},
			OpenLoops:             []string{"Wire the store adapter."},
			DoNotRepeat:           []string{"Do not call local hybrid retrieval."},
			EmotionalStateContext: []string{"Calm after the design locked."},
			SuggestedNextStep:     "Run the synthetic isolation suite.",
			UpdatedAtUnixMilli:    200,
		},
	}}

	response, err := engine.Resume(context.Background(), repository, TeamResumeRequest{
		ThreadID: "thread-pilot", ProjectID: "project-pulse", SessionID: "session-new", Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resumeTexts(response.WhereWeLeftOff), []string{"Stopped after the scoped retrieval contract."}; !reflect.DeepEqual(got, want) {
		t.Fatalf("where we left off = %v, want %v", got, want)
	}
	if got, want := resumeTexts(response.ActiveDecisions), []string{"Use a bound authorized repository.", "Return root IDs only."}; !reflect.DeepEqual(got, want) {
		t.Fatalf("decisions = %v, want %v", got, want)
	}
	if response.ReturnedCount != 7 {
		t.Fatalf("returned_count = %d, want 7; response=%+v", response.ReturnedCount, response)
	}
	for _, section := range [][]TeamResumeItem{
		response.WhereWeLeftOff, response.ActiveDecisions, response.OpenLoops,
		response.DoNotRepeat, response.RelevantEmotionalStateContext, response.SuggestedNextStep,
	} {
		for _, item := range section {
			if item.RootObjectID != "root-latest" || item.ObjectID != "continuity-latest" {
				t.Fatalf("resume item lost authorized lineage: %+v", item)
			}
		}
	}
	if got, want := repository.recheckedRootSets, [][]string{{"root-latest"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resume root recheck = %v, want %v", got, want)
	}
}

func TestTeamContextCosineUsesOnlyAuthorizedSemanticEmbeddingsAndClampsTrace(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{
		Embedder:       fakeTeamQueryEmbedder{model: "bge-m3:test", vector: []float32{1, 0}},
		CandidateLimit: 32,
	})
	visibleCorpus := TeamAuthorizedCorpus{
		Entities: []TeamEntityDocument{
			{
				RootObjectID: "root-vector", ObjectID: "entity-vector", PartitionKey: "project:pulse",
				EntityKind: "project", Name: "Pulse needle", Summary: "Exact lexical and vector match", Confidence: 0.9,
			},
			{
				RootObjectID: "root-lexical", ObjectID: "entity-lexical", PartitionKey: "project:pulse",
				EntityKind: "project", Name: "Pulse only", Summary: "Lexical fallback", Confidence: 0.8,
			},
		},
		SemanticEmbeddings: []TeamSemanticEmbeddingDocument{
			{
				RootObjectID: "root-vector", EmbeddingObjectID: "embedding-vector",
				SourceObjectID: "entity-vector", PartitionKey: "project:pulse",
				Model: "bge-m3:test", Vector: []float32{1, 0},
			},
			{
				RootObjectID: "root-lexical", EmbeddingObjectID: "embedding-lexical",
				SourceObjectID: "entity-lexical", PartitionKey: "project:pulse",
				Model: "bge-m3:test", Vector: []float32{0, 1},
			},
		},
	}
	baselineRepository := &fakeTeamAuthorizedRepository{corpus: visibleCorpus}
	withHiddenCorpus := cloneTeamAuthorizedCorpus(visibleCorpus)
	withHiddenCorpus.SemanticEmbeddings = append(
		withHiddenCorpus.SemanticEmbeddings,
		TeamSemanticEmbeddingDocument{
			RootObjectID: "root-hidden-perfect-vector", EmbeddingObjectID: "embedding-hidden-perfect",
			SourceObjectID: "entity-lexical", PartitionKey: "project:pulse",
			Model: "bge-m3:test", Vector: []float32{1, 0},
		},
	)
	withHiddenRepository := &fakeTeamAuthorizedRepository{
		corpus: withHiddenCorpus, unreachableRootIDs: []string{"root-hidden-perfect-vector"},
	}
	request := TeamContextRequest{Query: "pulse needle", TopK: 2, IncludeTrace: true}
	baseline, err := engine.Context(context.Background(), baselineRepository, request)
	if err != nil {
		t.Fatal(err)
	}
	withHidden, err := engine.Context(context.Background(), withHiddenRepository, request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(withHidden, baseline) {
		t.Fatalf("hidden perfect vector changed context:\nbaseline=%+v\nhidden=%+v", baseline, withHidden)
	}
	if got, want := contextEntityObjectIDs(withHidden.Entities), []string{"entity-vector", "entity-lexical"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("vector/lexical order = %v, want %v", got, want)
	}
	if len(withHidden.Trace) != 2 || withHidden.Trace[0].Cosine < 0.99 ||
		withHidden.Trace[0].Score != 1 || withHidden.Entities[0].Score != 1 {
		t.Fatalf("cosine/clamped trace = %+v entities=%+v", withHidden.Trace, withHidden.Entities)
	}
	for _, trace := range withHidden.Trace {
		if trace.Score < 0 || trace.Score > 1 || trace.RootObjectID == "root-hidden-perfect-vector" {
			t.Fatalf("invalid or hidden trace score %+v", trace)
		}
	}
	if len(withHiddenRepository.corpusQueries) != 1 ||
		withHiddenRepository.corpusQueries[0].Surface != TeamCorpusSurfaceContext ||
		withHiddenRepository.corpusQueries[0].EmbeddingModel != "bge-m3:test" {
		t.Fatalf("context corpus contract = %+v", withHiddenRepository.corpusQueries)
	}
}

func TestTeamContextCosineUsesSelectedSharedDerivativeContribution(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{
		Embedder:       fakeTeamQueryEmbedder{model: "bge-m3:test", vector: []float32{1, 0}},
		CandidateLimit: 32,
	})
	repository := &fakeTeamAuthorizedRepository{corpus: TeamAuthorizedCorpus{
		Entities: []TeamEntityDocument{
			{
				RootObjectID: "root-z", ObjectID: "entity-shared", PartitionKey: "project:pulse",
				EntityKind: "project", Name: "Vector needle", Summary: "Shared derivative", Confidence: 0.9,
			},
			{
				RootObjectID: "root-a", ObjectID: "entity-shared", PartitionKey: "project:pulse",
				EntityKind: "project", Name: "Vector needle", Summary: "Shared derivative", Confidence: 0.9,
			},
		},
		SemanticEmbeddings: []TeamSemanticEmbeddingDocument{
			{
				RootObjectID: "root-z", EmbeddingObjectID: "embedding-shared",
				SourceObjectID: "entity-shared", PartitionKey: "project:pulse",
				Model: "bge-m3:test", Vector: []float32{0, 1},
			},
			{
				RootObjectID: "root-a", EmbeddingObjectID: "embedding-shared",
				SourceObjectID: "entity-shared", PartitionKey: "project:pulse",
				Model: "bge-m3:test", Vector: []float32{1, 0},
			},
		},
	}}

	response, err := engine.Context(context.Background(), repository, TeamContextRequest{
		Query: "vector needle", TopK: 1, IncludeTrace: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Entities) != 1 || response.Entities[0].RootObjectID != "root-a" ||
		len(response.Trace) != 1 || response.Trace[0].Cosine < 0.99 {
		t.Fatalf("selected shared contribution was not used: response=%+v", response)
	}
	if got, want := repository.recheckedRootSets, [][]string{{"root-a"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shared derivative recheck = %v, want %v", got, want)
	}
}

func TestTeamResumeLimitIsOneGlobalEntryBudget(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{CandidateLimit: 32})
	repository := &fakeTeamAuthorizedRepository{continuity: []TeamContinuityDocument{
		{
			RootObjectID: "root-resume", ObjectID: "continuity-resume", PartitionKey: "project:pulse",
			ThreadID: "thread-pilot", ProjectID: "project-pulse", Summary: "Checkpoint",
			Decisions: []string{"Decision one", "Decision two", "Decision three"},
			OpenLoops: []string{"Open one", "Open two"}, UpdatedAtUnixMilli: 200,
		},
		{
			RootObjectID: "root-unused", ObjectID: "continuity-unused", PartitionKey: "project:pulse",
			ThreadID: "thread-pilot", ProjectID: "project-pulse", Summary: "Unused older checkpoint",
			UpdatedAtUnixMilli: 100,
		},
	}}
	response, err := engine.Resume(context.Background(), repository, TeamResumeRequest{
		ThreadID: "thread-pilot", Limit: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.ReturnedCount != 3 || len(response.WhereWeLeftOff) != 1 ||
		len(response.ActiveDecisions) != 2 || len(response.OpenLoops) != 0 {
		t.Fatalf("global resume budget not enforced: %+v", response)
	}
	if got, want := repository.recheckedRootSets, [][]string{{"root-resume"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resume rechecked non-returned roots: got %v want %v", got, want)
	}
}

func TestTeamResumeDedupesSharedContinuityDerivativeBeforeBudgetAndRecheck(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{CandidateLimit: 32})
	shared := TeamContinuityDocument{
		ObjectID: "continuity-shared", PartitionKey: "project:pulse",
		ThreadID: "thread-pilot", ProjectID: "project-pulse", SessionID: "session-one",
		Summary: "Shared checkpoint", Decisions: []string{"Keep the scoped boundary."},
		UpdatedAtUnixMilli: 200,
	}
	rootZ := shared
	rootZ.RootObjectID = "root-z"
	rootA := shared
	rootA.RootObjectID = "root-a"
	repository := &fakeTeamAuthorizedRepository{continuity: []TeamContinuityDocument{rootZ, rootA}}

	response, err := engine.Resume(context.Background(), repository, TeamResumeRequest{
		ThreadID: "thread-pilot", Limit: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.ReturnedCount != 2 || len(response.WhereWeLeftOff) != 1 ||
		len(response.ActiveDecisions) != 1 {
		t.Fatalf("shared continuity was not deduped before budget: %+v", response)
	}
	for _, item := range append(response.WhereWeLeftOff, response.ActiveDecisions...) {
		if item.RootObjectID != "root-a" || item.ObjectID != "continuity-shared" {
			t.Fatalf("shared continuity lineage is not deterministic: %+v", item)
		}
	}
	if got, want := repository.recheckedRootSets, [][]string{{"root-a"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shared continuity recheck = %v, want %v", got, want)
	}
}

func TestTeamResumeSelectsLatestVisibleContributionForConvergedCheckpoint(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{CandidateLimit: 32})
	repository := &fakeTeamAuthorizedRepository{continuity: []TeamContinuityDocument{
		{
			RootObjectID: "root-a", ObjectID: "continuity-shared", PartitionKey: "project:pulse",
			ThreadID: "thread-pilot", ProjectID: "project-pulse", SessionID: "session-one",
			Summary: "Older checkpoint", Decisions: []string{"Old decision"}, UpdatedAtUnixMilli: 100,
		},
		{
			RootObjectID: "root-z", ObjectID: "continuity-shared", PartitionKey: "project:pulse",
			ThreadID: "thread-pilot", ProjectID: "project-pulse", SessionID: "session-one",
			Summary: "Latest checkpoint", Decisions: []string{"Current decision"}, UpdatedAtUnixMilli: 200,
		},
	}}

	response, err := engine.Resume(context.Background(), repository, TeamResumeRequest{
		ThreadID: "thread-pilot", SessionID: "session-one", Limit: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resumeTexts(response.WhereWeLeftOff), []string{"Latest checkpoint"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("converged checkpoint summary = %v, want %v", got, want)
	}
	if got, want := resumeTexts(response.ActiveDecisions), []string{"Current decision"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("converged checkpoint decisions = %v, want %v", got, want)
	}
	if got, want := repository.recheckedRootSets, [][]string{{"root-z"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("latest checkpoint recheck = %v, want %v", got, want)
	}
}

func TestTeamResumeLoadsCandidateBudgetBeforeDedupingGlobalEntryBudget(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{CandidateLimit: 32})
	shared := TeamContinuityDocument{
		ObjectID: "continuity-shared", PartitionKey: "project:pulse",
		ThreadID: "thread-pilot", Summary: "Shared checkpoint", UpdatedAtUnixMilli: 200,
	}
	rootZ, rootY, rootX := shared, shared, shared
	rootZ.RootObjectID = "root-z"
	rootY.RootObjectID = "root-y"
	rootX.RootObjectID = "root-x"
	repository := &fakeTeamAuthorizedRepository{
		honorContinuityLimit: true,
		continuity: []TeamContinuityDocument{
			rootZ, rootY, rootX,
			{
				RootObjectID: "root-unique", ObjectID: "continuity-unique", PartitionKey: "project:pulse",
				ThreadID: "thread-pilot", Summary: "Unique checkpoint", UpdatedAtUnixMilli: 100,
			},
		},
	}

	response, err := engine.Resume(context.Background(), repository, TeamResumeRequest{
		ThreadID: "thread-pilot", Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resumeTexts(response.WhereWeLeftOff), []string{"Shared checkpoint", "Unique checkpoint"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resume dedupe underfilled entry budget: got %v want %v", got, want)
	}
	if len(repository.continuityQueries) != 1 || repository.continuityQueries[0].Limit != 32 {
		t.Fatalf("continuity candidate query = %+v, want candidate limit 32", repository.continuityQueries)
	}
	if got, want := repository.recheckedRootSets, [][]string{{"root-unique", "root-x"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resume candidate roots = %v, want %v", got, want)
	}
}

func TestTeamContextDedupesSharedDerivativeAndClosesTypedEntityReferences(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{CandidateLimit: 32})
	repository := &fakeTeamAuthorizedRepository{corpus: TeamAuthorizedCorpus{
		Entities: []TeamEntityDocument{
			{
				RootObjectID: "root-z", ObjectID: "entity-shared", PartitionKey: "project:pulse",
				EntityKind: "person", Name: "Shared person", Summary: "Same visible derivative", Confidence: 0.9,
			},
			{
				RootObjectID: "root-a", ObjectID: "entity-shared", PartitionKey: "project:pulse",
				EntityKind: "person", Name: "Shared person", Summary: "Same visible derivative", Confidence: 0.9,
			},
			{
				RootObjectID: "root-fact", ObjectID: "entity-shared", PartitionKey: "project:pulse",
				EntityKind: "person", Name: "Shared person", Summary: "Same visible derivative", Confidence: 0.9,
			},
			{
				RootObjectID: "root-b", ObjectID: "entity-endpoint", PartitionKey: "project:pulse",
				EntityKind: "project", Name: "Endpoint", Summary: "Referenced entity", Confidence: 0.8,
			},
		},
		Relations: []TeamRelationDocument{{
			RootObjectID: "root-relation", ObjectID: "relation-visible", PartitionKey: "project:pulse",
			FromObjectID: "entity-shared", ToObjectID: "entity-endpoint",
			RelationKind: "connects", Summary: "Needle connection", Strength: 0.9, Confidence: 0.9,
		}},
		Facts: []TeamFactDocument{{
			RootObjectID: "root-fact", ObjectID: "fact-visible", PartitionKey: "project:pulse",
			NodeObjectID: "entity-shared", Text: "Needle fact", Predicate: "status",
			ObjectText: "active", Confidence: 0.9, Domain: "real",
		}},
		Assertions: []TeamAssertionDocument{{
			RootObjectID: "root-fact", ObjectID: "assertion-visible", PartitionKey: "project:pulse",
			CandidateObjectID: "fact-visible", SubjectObjectID: "entity-shared",
			ClaimSlotDigest: "slot-status", Text: "Needle assertion",
			Predicate: "status", ObjectText: "active", ObservedAtUnixMilli: 100, Confidence: 0.9,
		}},
	}}
	response, err := engine.Context(context.Background(), repository, TeamContextRequest{
		Query: "needle", TopK: 5, IncludeTrace: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Counts.Total != 5 || len(response.Entities) != 2 || len(response.Relations) != 1 ||
		len(response.Facts) != 1 || len(response.Assertions) != 1 {
		t.Fatalf("typed closure counts = %+v response=%+v", response.Counts, response)
	}
	if response.Entities[0].ObjectID != "entity-shared" || response.Entities[0].RootObjectID != "root-a" {
		t.Fatalf("shared derivative root was not deterministic: %+v", response.Entities)
	}
	returnedEntities := map[string]bool{}
	returnedObjects := map[string]bool{}
	for _, entity := range response.Entities {
		returnedEntities[entity.ObjectID] = true
		if returnedObjects[entity.ObjectID] {
			t.Fatalf("duplicate derivative %s", entity.ObjectID)
		}
		returnedObjects[entity.ObjectID] = true
	}
	for _, relation := range response.Relations {
		if !returnedEntities[relation.FromObjectID] || !returnedEntities[relation.ToObjectID] {
			t.Fatalf("relation endpoints absent: %+v entities=%v", relation, returnedEntities)
		}
		returnedObjects[relation.ObjectID] = true
	}
	for _, assertion := range response.Assertions {
		if !returnedEntities[assertion.SubjectObjectID] {
			t.Fatalf("assertion subject absent: %+v entities=%v", assertion, returnedEntities)
		}
		returnedObjects[assertion.ObjectID] = true
	}
	for _, fact := range response.Facts {
		returnedObjects[fact.ObjectID] = true
	}
	for _, trace := range response.Trace {
		if !returnedObjects[trace.ObjectID] {
			t.Fatalf("trace ID is not returned: %+v", trace)
		}
	}
}

func TestTeamContextCandidateLimitCountsUniqueDerivativesNotContributionRows(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{CandidateLimit: 2})
	shared := TeamEntityDocument{
		ObjectID: "entity-shared", PartitionKey: "project:pulse",
		EntityKind: "thing", Name: "Needle shared", Confidence: 0.9,
	}
	rootD, rootC, rootB, rootA := shared, shared, shared, shared
	rootD.RootObjectID = "root-d"
	rootC.RootObjectID = "root-c"
	rootB.RootObjectID = "root-b"
	rootA.RootObjectID = "root-a"
	repository := &fakeTeamAuthorizedRepository{corpus: TeamAuthorizedCorpus{
		Entities: []TeamEntityDocument{
			rootD, rootC, rootB, rootA,
			{
				RootObjectID: "root-unique", ObjectID: "entity-unique", PartitionKey: "project:pulse",
				EntityKind: "thing", Name: "Needle unique", Confidence: 0.9,
			},
		},
	}}

	response, err := engine.Context(context.Background(), repository, TeamContextRequest{
		Query: "needle", TopK: 2, IncludeTrace: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := contextEntityObjectIDs(response.Entities), []string{"entity-shared", "entity-unique"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unique derivative candidate limit = %v, want %v", got, want)
	}
	if response.Entities[0].RootObjectID != "root-a" {
		t.Fatalf("shared derivative root selection = %+v", response.Entities[0])
	}
	if got, want := repository.recheckedRootSets, [][]string{{"root-a", "root-unique"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unique derivative recheck = %v, want %v", got, want)
	}
}

func TestTeamEmptyAndNoMatchResponsesAreDeterministicallyIndistinguishable(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{CandidateLimit: 32})
	emptyRepository := &fakeTeamAuthorizedRepository{}
	noMatchRepository := &fakeTeamAuthorizedRepository{
		corpus: TeamAuthorizedCorpus{
			Memories: []TeamMemoryDocument{{
				DocumentID: "memory-unrelated", RootObjectID: "root-unrelated",
				PartitionKey: "project:pulse", Kind: "fact", RedactedSummary: "Completely unrelated",
				Confidence: 0.9, PrivacyTier: "normal", Retention: "project", Tags: []string{},
			}},
			Entities: []TeamEntityDocument{{
				RootObjectID: "root-unrelated", ObjectID: "entity-unrelated", PartitionKey: "project:pulse",
				EntityKind: "thing", Name: "Unrelated", Summary: "No semantic overlap", Confidence: 0.9,
			}},
		},
		continuity: []TeamContinuityDocument{{
			RootObjectID: "root-other-thread", ObjectID: "continuity-other", PartitionKey: "project:pulse",
			ThreadID: "other-thread", ProjectID: "project-pulse", Summary: "Other thread", UpdatedAtUnixMilli: 1,
		}},
	}
	recallRequest := TeamRetrievalRequest{Query: "needle", TopK: 5}
	emptyRecall, err := engine.Retrieve(context.Background(), emptyRepository, recallRequest)
	if err != nil {
		t.Fatal(err)
	}
	noMatchRecall, err := engine.Retrieve(context.Background(), noMatchRepository, recallRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(emptyRecall, noMatchRecall) || emptyRecall.Items == nil || emptyRecall.Trace == nil {
		t.Fatalf("empty/no-match recall differ: empty=%+v no-match=%+v", emptyRecall, noMatchRecall)
	}
	contextRequest := TeamContextRequest{Query: "needle", TopK: 5, IncludeTrace: true}
	emptyContext, err := engine.Context(context.Background(), emptyRepository, contextRequest)
	if err != nil {
		t.Fatal(err)
	}
	noMatchContext, err := engine.Context(context.Background(), noMatchRepository, contextRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(emptyContext, noMatchContext) || emptyContext.Entities == nil || emptyContext.Trace == nil {
		t.Fatalf("empty/no-match context differ: empty=%+v no-match=%+v", emptyContext, noMatchContext)
	}
	resumeRequest := TeamResumeRequest{ThreadID: "needle-thread", Limit: 5}
	emptyResume, err := engine.Resume(context.Background(), emptyRepository, resumeRequest)
	if err != nil {
		t.Fatal(err)
	}
	noMatchResume, err := engine.Resume(context.Background(), noMatchRepository, resumeRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(emptyResume, noMatchResume) || emptyResume.WhereWeLeftOff == nil {
		t.Fatalf("empty/no-match resume differ: empty=%+v no-match=%+v", emptyResume, noMatchResume)
	}
	for label, repository := range map[string]*fakeTeamAuthorizedRepository{
		"empty": emptyRepository, "no-match": noMatchRepository,
	} {
		if len(repository.recheckedRootSets) != 3 {
			t.Fatalf("%s empty-result rechecks = %v, want recall/context/resume", label, repository.recheckedRootSets)
		}
		for _, roots := range repository.recheckedRootSets {
			if len(roots) != 0 {
				t.Fatalf("%s empty-result recheck leaked roots: %v", label, repository.recheckedRootSets)
			}
		}
	}
}

func TestTeamContextEmptyAndVectorNoMatchUseSameEmbeddingPath(t *testing.T) {
	calls := 0
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{
		Embedder: countingTeamQueryEmbedder{
			model: "bge-m3:test", vector: []float32{1, 0}, calls: &calls,
		},
		CandidateLimit: 32,
	})
	emptyRepository := &fakeTeamAuthorizedRepository{}
	noMatchRepository := &fakeTeamAuthorizedRepository{corpus: TeamAuthorizedCorpus{
		Entities: []TeamEntityDocument{{
			RootObjectID: "root-orthogonal", ObjectID: "entity-orthogonal", PartitionKey: "project:pulse",
			EntityKind: "thing", Name: "Unrelated", Summary: "No lexical overlap", Confidence: 0.9,
		}},
		SemanticEmbeddings: []TeamSemanticEmbeddingDocument{{
			RootObjectID: "root-orthogonal", EmbeddingObjectID: "embedding-orthogonal",
			SourceObjectID: "entity-orthogonal", PartitionKey: "project:pulse",
			Model: "bge-m3:test", Vector: []float32{-1, 0},
		}},
	}}
	request := TeamContextRequest{Query: "needle", TopK: 5, IncludeTrace: true}

	empty, err := engine.Context(context.Background(), emptyRepository, request)
	if err != nil {
		t.Fatal(err)
	}
	noMatch, err := engine.Context(context.Background(), noMatchRepository, request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(empty, noMatch) {
		t.Fatalf("empty/vector-no-match shape differs: empty=%+v no-match=%+v", empty, noMatch)
	}
	if calls != 2 {
		t.Fatalf("embedding calls = %d, want one per context request", calls)
	}
	if got, want := emptyRepository.recheckedRootSets, [][]string{nil}; !reflect.DeepEqual(got, want) {
		t.Fatalf("empty context recheck = %v, want %v", got, want)
	}
	if got, want := noMatchRepository.recheckedRootSets, [][]string{nil}; !reflect.DeepEqual(got, want) {
		t.Fatalf("vector no-match recheck = %v, want %v", got, want)
	}
}

func TestTeamReadMethodsDiscardComputedResponsesWhenFreshAuthorizationFails(t *testing.T) {
	revoked := errors.New("principal_revoked")
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{CandidateLimit: 32})
	repository := &fakeTeamAuthorizedRepository{
		recheckErr: revoked,
		corpus: TeamAuthorizedCorpus{
			Memories: []TeamMemoryDocument{{
				DocumentID: "memory-visible", RootObjectID: "root-memory", PartitionKey: "project:pulse",
				Kind: "fact", RedactedSummary: "Needle memory", Confidence: 0.9,
				PrivacyTier: "normal", Retention: "project", Tags: []string{},
			}},
			Entities: []TeamEntityDocument{{
				RootObjectID: "root-entity", ObjectID: "entity-visible", PartitionKey: "project:pulse",
				EntityKind: "thing", Name: "Needle entity", Confidence: 0.9,
			}},
		},
		continuity: []TeamContinuityDocument{{
			RootObjectID: "root-continuity", ObjectID: "continuity-visible", PartitionKey: "project:pulse",
			ThreadID: "thread-visible", Summary: "Needle checkpoint", UpdatedAtUnixMilli: 1,
		}},
	}
	if response, err := engine.Retrieve(context.Background(), repository, TeamRetrievalRequest{
		Query: "needle", TopK: 5,
	}); !errors.Is(err, revoked) || response.ReturnedCount != 0 || len(response.Items) != 0 {
		t.Fatalf("revoked recall leaked response=%+v err=%v", response, err)
	}
	if response, err := engine.Context(context.Background(), repository, TeamContextRequest{
		Query: "needle", TopK: 5, IncludeTrace: true,
	}); !errors.Is(err, revoked) || response.Counts.Total != 0 || len(response.Trace) != 0 {
		t.Fatalf("revoked context leaked response=%+v err=%v", response, err)
	}
	if response, err := engine.Resume(context.Background(), repository, TeamResumeRequest{
		ThreadID: "thread-visible", Limit: 5,
	}); !errors.Is(err, revoked) || response.ReturnedCount != 0 {
		t.Fatalf("revoked resume leaked response=%+v err=%v", response, err)
	}
}

func TestTeamRetrievalEngineRetainsNoCorpusAcrossRequests(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{CandidateLimit: 32})
	request := TeamRetrievalRequest{Query: "needle", TopK: 1}
	repositoryA := &fakeTeamAuthorizedRepository{corpus: TeamAuthorizedCorpus{Memories: []TeamMemoryDocument{{
		DocumentID: "memory-a", RootObjectID: "root-a", PartitionKey: "project:a",
		Kind: "fact", RedactedSummary: "Needle A", Confidence: 1,
		PrivacyTier: "normal", Retention: "project", Tags: []string{},
	}}}}
	repositoryB := &fakeTeamAuthorizedRepository{corpus: TeamAuthorizedCorpus{Memories: []TeamMemoryDocument{{
		DocumentID: "memory-b", RootObjectID: "root-b", PartitionKey: "project:b",
		Kind: "fact", RedactedSummary: "Needle B", Confidence: 1,
		PrivacyTier: "normal", Retention: "project", Tags: []string{},
	}}}}
	first, err := engine.Retrieve(context.Background(), repositoryA, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := engine.Retrieve(context.Background(), repositoryB, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Items[0].RootObjectID != "root-a" || second.Items[0].RootObjectID != "root-b" {
		t.Fatalf("cross-request corpus leaked: first=%+v second=%+v", first, second)
	}
}

func TestTeamReadRejectsNonFiniteAuthorizedConfidence(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{CandidateLimit: 32})
	memoryRepository := &fakeTeamAuthorizedRepository{corpus: TeamAuthorizedCorpus{
		Memories: []TeamMemoryDocument{{
			DocumentID: "memory-nan", RootObjectID: "root-memory", PartitionKey: "project:pulse",
			Kind: "fact", RedactedSummary: "Needle memory", Confidence: math.NaN(),
			PrivacyTier: "normal", Retention: "project",
		}},
	}}
	if response, err := engine.Retrieve(context.Background(), memoryRepository, TeamRetrievalRequest{
		Query: "needle", TopK: 1,
	}); !errors.Is(err, ErrInvalidTeamAuthorizedCorpus) || response.ReturnedCount != 0 {
		t.Fatalf("non-finite memory confidence accepted: response=%+v err=%v", response, err)
	}
	if len(memoryRepository.recheckedRootSets) != 0 {
		t.Fatalf("invalid memory reached authorization recheck: %v", memoryRepository.recheckedRootSets)
	}

	contextRepository := &fakeTeamAuthorizedRepository{corpus: TeamAuthorizedCorpus{
		Entities: []TeamEntityDocument{{
			RootObjectID: "root-entity", ObjectID: "entity-nan", PartitionKey: "project:pulse",
			EntityKind: "thing", Name: "Needle entity", Confidence: math.NaN(),
		}},
	}}
	if response, err := engine.Context(context.Background(), contextRepository, TeamContextRequest{
		Query: "needle", TopK: 1, IncludeTrace: true,
	}); !errors.Is(err, ErrInvalidTeamAuthorizedCorpus) || response.Counts.Total != 0 {
		t.Fatalf("non-finite entity confidence accepted: response=%+v err=%v", response, err)
	}
	if len(contextRepository.recheckedRootSets) != 0 {
		t.Fatalf("invalid context reached authorization recheck: %v", contextRepository.recheckedRootSets)
	}
}

func TestTeamContextAssertionReductionRequiresSameRootContribution(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{CandidateLimit: 32})
	repository := &fakeTeamAuthorizedRepository{corpus: TeamAuthorizedCorpus{
		Entities: []TeamEntityDocument{
			{
				RootObjectID: "root-old", ObjectID: "entity-subject", PartitionKey: "project:pulse",
				EntityKind: "person", Name: "Subject", Confidence: 0.9,
			},
			{
				RootObjectID: "root-current", ObjectID: "entity-subject", PartitionKey: "project:pulse",
				EntityKind: "person", Name: "Subject", Confidence: 0.9,
			},
		},
		Facts: []TeamFactDocument{
			{
				RootObjectID: "root-old", ObjectID: "fact-old", PartitionKey: "project:pulse",
				NodeObjectID: "entity-subject", Text: "Needle old", Predicate: "home_base",
				ObjectText: "Lisbon", Confidence: 0.9, Domain: "real",
			},
			{
				RootObjectID: "root-current", ObjectID: "fact-current", PartitionKey: "project:pulse",
				NodeObjectID: "entity-subject", Text: "Needle current", Predicate: "home_base",
				ObjectText: "Bangkok", Confidence: 0.9, Domain: "real",
			},
		},
		Assertions: []TeamAssertionDocument{
			{
				RootObjectID: "root-old", ObjectID: "assertion-old", PartitionKey: "project:pulse",
				CandidateObjectID: "fact-old", SubjectObjectID: "entity-subject",
				ClaimSlotDigest: "slot-home", Text: "Old assertion",
				Predicate: "home_base", ObjectText: "Lisbon", ObservedAtUnixMilli: 100, Confidence: 0.9,
			},
			{
				RootObjectID: "root-current", ObjectID: "assertion-current", PartitionKey: "project:pulse",
				CandidateObjectID: "fact-current", SubjectObjectID: "entity-subject",
				ClaimSlotDigest: "slot-home", Text: "Current assertion",
				Predicate: "home_base", ObjectText: "Bangkok", ObservedAtUnixMilli: 200, Confidence: 0.9,
			},
			{
				RootObjectID: "root-misaligned", ObjectID: "assertion-misaligned", PartitionKey: "project:pulse",
				CandidateObjectID: "fact-old", SubjectObjectID: "entity-subject",
				ClaimSlotDigest: "slot-home", Text: "Forged newer assertion",
				Predicate: "home_base", ObjectText: "Lisbon", ObservedAtUnixMilli: 300, Confidence: 1,
			},
		},
	}}

	response, err := engine.Context(context.Background(), repository, TeamContextRequest{
		Query: "needle", TopK: 2, IncludeTrace: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, trace := range response.Trace {
		states[trace.ObjectID] = trace.AssertionState
	}
	if states["fact-old"] != "superseded" || states["fact-current"] != "current" {
		t.Fatalf("misaligned assertion contribution affected reduction: states=%v response=%+v", states, response)
	}
}

func TestTeamContextAssertionReductionIgnoresContributionWithoutAuthorizedSubject(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{CandidateLimit: 32})
	repository := &fakeTeamAuthorizedRepository{corpus: TeamAuthorizedCorpus{
		Facts: []TeamFactDocument{
			{
				RootObjectID: "root-old", ObjectID: "fact-old", PartitionKey: "project:pulse",
				NodeObjectID: "entity-missing", Text: "Needle old", Predicate: "status",
				ObjectText: "old", Confidence: 0.9, Domain: "real",
			},
			{
				RootObjectID: "root-current", ObjectID: "fact-current", PartitionKey: "project:pulse",
				NodeObjectID: "entity-missing", Text: "Needle current", Predicate: "status",
				ObjectText: "current", Confidence: 0.9, Domain: "real",
			},
		},
		Assertions: []TeamAssertionDocument{
			{
				RootObjectID: "root-old", ObjectID: "assertion-old", PartitionKey: "project:pulse",
				CandidateObjectID: "fact-old", SubjectObjectID: "entity-missing",
				ClaimSlotDigest: "slot-status", Text: "Needle old assertion",
				Predicate: "status", ObjectText: "old", ObservedAtUnixMilli: 100, Confidence: 0.9,
			},
			{
				RootObjectID: "root-current", ObjectID: "assertion-current", PartitionKey: "project:pulse",
				CandidateObjectID: "fact-current", SubjectObjectID: "entity-missing",
				ClaimSlotDigest: "slot-status", Text: "Needle current assertion",
				Predicate: "status", ObjectText: "current", ObservedAtUnixMilli: 200, Confidence: 0.9,
			},
		},
	}}

	response, err := engine.Context(context.Background(), repository, TeamContextRequest{
		Query: "needle", TopK: 2, IncludeTrace: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, trace := range response.Trace {
		if trace.AssertionState != "" || trace.Score != 1 {
			t.Fatalf("missing subject assertion affected fact: %+v", trace)
		}
	}
}

func TestTeamContextRechecksNonReturnedAssertionRootsThatInfluenceResults(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{CandidateLimit: 32})
	repository := &fakeTeamAuthorizedRepository{corpus: TeamAuthorizedCorpus{
		Entities: []TeamEntityDocument{{
			RootObjectID: "root-z", ObjectID: "entity-subject", PartitionKey: "project:pulse",
			EntityKind: "person", Name: "Subject", Confidence: 0.9,
		}},
		Facts: []TeamFactDocument{
			{
				RootObjectID: "root-z", ObjectID: "fact-shared", PartitionKey: "project:pulse",
				NodeObjectID: "entity-subject", Text: "Needle fact", Predicate: "status",
				ObjectText: "active", Confidence: 0.9, Domain: "real",
			},
			{
				RootObjectID: "root-a", ObjectID: "fact-shared", PartitionKey: "project:pulse",
				NodeObjectID: "entity-subject", Text: "Needle fact", Predicate: "status",
				ObjectText: "active", Confidence: 0.9, Domain: "real",
			},
		},
		Assertions: []TeamAssertionDocument{{
			RootObjectID: "root-z", ObjectID: "assertion-status", PartitionKey: "project:pulse",
			CandidateObjectID: "fact-shared", SubjectObjectID: "entity-subject",
			ClaimSlotDigest: "slot-status", Text: "Current status assertion",
			Predicate: "status", ObjectText: "active", ObservedAtUnixMilli: 100, Confidence: 0.9,
		}},
	}}

	response, err := engine.Context(context.Background(), repository, TeamContextRequest{
		Query: "needle", TopK: 1, IncludeTrace: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Facts) != 1 || response.Facts[0].RootObjectID != "root-a" ||
		len(response.Trace) != 1 || response.Trace[0].AssertionState != "current" {
		t.Fatalf("assertion-influenced fact = %+v", response)
	}
	if got, want := repository.recheckedRootSets, [][]string{{"root-a", "root-z"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("assertion influence roots = %v, want %v", got, want)
	}
}

func TestTeamContextIneligibleNewerClaimContributionCannotReplaceEligibleOne(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{CandidateLimit: 32})
	baseCorpus := TeamAuthorizedCorpus{
		Entities: []TeamEntityDocument{{
			RootObjectID: "root-a", ObjectID: "entity-subject", PartitionKey: "project:pulse",
			EntityKind: "person", Name: "Subject", Confidence: 0.9,
		}},
		Facts: []TeamFactDocument{{
			RootObjectID: "root-a", ObjectID: "fact-status", PartitionKey: "project:pulse",
			NodeObjectID: "entity-subject", Text: "Supporting fact", Predicate: "status",
			ObjectText: "active", Confidence: 0.9, Domain: "real",
		}},
		Assertions: []TeamAssertionDocument{{
			RootObjectID: "root-a", ObjectID: "assertion-status", PartitionKey: "project:pulse",
			CandidateObjectID: "fact-status", SubjectObjectID: "entity-subject",
			ClaimSlotDigest: "slot-status", Text: "Needle eligible assertion",
			Predicate: "status", ObjectText: "active", ObservedAtUnixMilli: 100, Confidence: 0.9,
		}},
	}
	withIneligible := cloneTeamAuthorizedCorpus(baseCorpus)
	withIneligible.Assertions = append(withIneligible.Assertions, TeamAssertionDocument{
		RootObjectID: "root-b", ObjectID: "assertion-status", PartitionKey: "project:pulse",
		CandidateObjectID: "fact-status", SubjectObjectID: "entity-subject",
		ClaimSlotDigest: "slot-status", Text: "Needle ineligible newer assertion",
		Predicate: "status", ObjectText: "inactive", ObservedAtUnixMilli: 200, Confidence: 1,
	})
	baselineRepository := &fakeTeamAuthorizedRepository{corpus: baseCorpus}
	ineligibleRepository := &fakeTeamAuthorizedRepository{corpus: withIneligible}
	request := TeamContextRequest{Query: "needle", TopK: 2, IncludeTrace: true}

	baseline, err := engine.Context(context.Background(), baselineRepository, request)
	if err != nil {
		t.Fatal(err)
	}
	withIneligibleResponse, err := engine.Context(context.Background(), ineligibleRepository, request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(withIneligibleResponse, baseline) {
		t.Fatalf("ineligible newer claim replaced eligible contribution:\nbaseline=%+v\nwith-ineligible=%+v", baseline, withIneligibleResponse)
	}
	if len(withIneligibleResponse.Assertions) != 1 ||
		withIneligibleResponse.Assertions[0].RootObjectID != "root-a" ||
		withIneligibleResponse.Assertions[0].ObjectText != "active" {
		t.Fatalf("eligible claim survivor = %+v", withIneligibleResponse.Assertions)
	}
}

func TestTeamContextReducesCompetingVisibleClaimContributionsToOneDerivative(t *testing.T) {
	engine := NewTeamRetrievalEngine(TeamRetrievalConfig{CandidateLimit: 32})
	repository := &fakeTeamAuthorizedRepository{corpus: TeamAuthorizedCorpus{
		Entities: []TeamEntityDocument{
			{
				RootObjectID: "root-old-fact", ObjectID: "entity-subject", PartitionKey: "project:pulse",
				EntityKind: "person", Name: "Subject", Confidence: 0.9,
			},
			{
				RootObjectID: "root-current-fact", ObjectID: "entity-subject", PartitionKey: "project:pulse",
				EntityKind: "person", Name: "Subject", Confidence: 0.9,
			},
		},
		Facts: []TeamFactDocument{
			{
				RootObjectID: "root-old-fact", ObjectID: "fact-old-claim", PartitionKey: "project:pulse",
				NodeObjectID: "entity-subject", Text: "Needle old claim", Predicate: "home_base",
				ObjectText: "Lisbon", Confidence: 0.9, Domain: "real",
			},
			{
				RootObjectID: "root-current-fact", ObjectID: "fact-current-claim", PartitionKey: "project:pulse",
				NodeObjectID: "entity-subject", Text: "Needle current claim", Predicate: "home_base",
				ObjectText: "Bangkok", Confidence: 0.9, Domain: "real",
			},
		},
		Assertions: []TeamAssertionDocument{
			{
				RootObjectID: "root-old-fact", ObjectID: "assertion-home-slot", PartitionKey: "project:pulse",
				CandidateObjectID: "fact-old-claim", SubjectObjectID: "entity-subject",
				ClaimSlotDigest: "slot-home", Text: "Needle old assertion",
				Predicate: "home_base", ObjectText: "Lisbon", ObservedAtUnixMilli: 100, Confidence: 0.9,
			},
			{
				RootObjectID: "root-current-fact", ObjectID: "assertion-home-slot", PartitionKey: "project:pulse",
				CandidateObjectID: "fact-current-claim", SubjectObjectID: "entity-subject",
				ClaimSlotDigest: "slot-home", Text: "Needle current assertion",
				Predicate: "home_base", ObjectText: "Bangkok", ObservedAtUnixMilli: 200, Confidence: 0.9,
			},
		},
	}}
	response, err := engine.Context(context.Background(), repository, TeamContextRequest{
		Query: "needle", TopK: 4, IncludeTrace: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Assertions) != 1 || response.Assertions[0].ObjectID != "assertion-home-slot" ||
		response.Assertions[0].ObjectText != "Bangkok" ||
		response.Assertions[0].RootObjectID != "root-current-fact" {
		t.Fatalf("claim derivative was not deterministically reduced: %+v", response.Assertions)
	}
	states := map[string]string{}
	for _, trace := range response.Trace {
		states[trace.ObjectID] = trace.AssertionState
	}
	if states["fact-old-claim"] != "superseded" || states["fact-current-claim"] != "current" {
		t.Fatalf("claim contribution states = %v", states)
	}
}

func resumeTexts(items []TeamResumeItem) []string {
	texts := make([]string, len(items))
	for index, item := range items {
		texts[index] = item.Text
	}
	return texts
}

func contextFactObjectIDs(facts []TeamContextFact) []string {
	ids := make([]string, len(facts))
	for index, fact := range facts {
		ids[index] = fact.ObjectID
	}
	return ids
}

func contextEntityObjectIDs(entities []TeamContextEntity) []string {
	ids := make([]string, len(entities))
	for index, entity := range entities {
		ids[index] = entity.ObjectID
	}
	return ids
}

func contextEventObjectIDs(events []TeamContextEvent) []string {
	ids := make([]string, len(events))
	for index, event := range events {
		ids[index] = event.ObjectID
	}
	return ids
}
