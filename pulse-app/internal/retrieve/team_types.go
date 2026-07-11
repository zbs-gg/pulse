package retrieve

import "context"

type TeamCorpusSurface string

const (
	TeamCorpusSurfaceRecall  TeamCorpusSurface = "recall"
	TeamCorpusSurfaceContext TeamCorpusSurface = "context"
)

type TeamCorpusQuery struct {
	Query          string
	Limit          int
	EmbeddingModel string
	Surface        TeamCorpusSurface
}

type TeamResumeQuery struct {
	ThreadID  string
	ProjectID string
	SessionID string
	Limit     int
}

// TeamAuthorizedRepository is bound to one compiled authorization filter for
// one request. Implementations must apply that filter before search, ordering,
// or limiting, and must recheck the exact returned roots with a fresh clock.
type TeamAuthorizedRepository interface {
	LoadAuthorizedCorpus(context.Context, TeamCorpusQuery) (TeamAuthorizedCorpus, error)
	LoadAuthorizedContinuity(context.Context, TeamResumeQuery) ([]TeamContinuityDocument, error)
	RecheckAuthorizedRoots(context.Context, []string) error
}

type TeamAuthorizedCorpus struct {
	Memories           []TeamMemoryDocument
	Entities           []TeamEntityDocument
	Relations          []TeamRelationDocument
	Facts              []TeamFactDocument
	Events             []TeamEventDocument
	Assertions         []TeamAssertionDocument
	SemanticEmbeddings []TeamSemanticEmbeddingDocument
	GraphLinks         []TeamGraphLink
}

type TeamMemoryDocument struct {
	DocumentID      string
	RootObjectID    string
	PartitionKey    string
	Kind            string
	RedactedSummary string
	Confidence      float64
	PrivacyTier     string
	Retention       string
	Tags            []string
	EmbeddingModel  string
	Embedding       []float32
}

type TeamContinuityDocument struct {
	RootObjectID          string
	ObjectID              string
	PartitionKey          string
	ThreadID              string
	ProjectID             string
	SessionID             string
	Summary               string
	Decisions             []string
	OpenLoops             []string
	DoNotRepeat           []string
	EmotionalStateContext []string
	SuggestedNextStep     string
	UpdatedAtUnixMilli    int64
}

type TeamEntityDocument struct {
	RootObjectID string
	ObjectID     string
	PartitionKey string
	EntityKind   string
	Name         string
	Summary      string
	Confidence   float64
}

type TeamRelationDocument struct {
	RootObjectID string
	ObjectID     string
	PartitionKey string
	FromObjectID string
	ToObjectID   string
	RelationKind string
	Summary      string
	Strength     float64
	Confidence   float64
}

type TeamFactDocument struct {
	RootObjectID string
	ObjectID     string
	PartitionKey string
	NodeObjectID string
	Text         string
	Predicate    string
	ObjectText   string
	Confidence   float64
	Domain       string
}

type TeamEventDocument struct {
	RootObjectID string
	ObjectID     string
	PartitionKey string
	Title        string
	Summary      string
	OccurredAt   string
	Confidence   float64
	Domain       string
}

type TeamAssertionDocument struct {
	RootObjectID        string
	ObjectID            string
	PartitionKey        string
	CandidateObjectID   string
	SubjectObjectID     string
	ClaimSlotDigest     string
	Text                string
	Predicate           string
	ObjectText          string
	ObservedAtUnixMilli int64
	Confidence          float64
}

type TeamGraphLink struct {
	RootObjectID string
	PartitionKey string
	FromObjectID string
	ToObjectID   string
	Strength     float64
}

type TeamSemanticEmbeddingDocument struct {
	RootObjectID      string
	EmbeddingObjectID string
	SourceObjectID    string
	PartitionKey      string
	Model             string
	Vector            []float32
}

type TeamRetrievalRequest struct {
	Query string
	TopK  int
	Graph bool
}

type TeamRetrievedItem struct {
	RootObjectID string
	Kind         string
	Text         string
	Confidence   float64
	PrivacyTier  string
	Retention    string
	Tags         []string
	Score        float64
}

type TeamRetrievalTrace struct {
	RootObjectID   string
	Lexical        float64
	Cosine         float64
	Graph          float64
	Score          float64
	AssertionState string
}

type TeamRetrievalResponse struct {
	Items         []TeamRetrievedItem
	ReturnedCount int
	Trace         []TeamRetrievalTrace
}

type TeamGraphMode string

const (
	TeamGraphModeOff      TeamGraphMode = "off"
	TeamGraphModeAnchored TeamGraphMode = "anchored"
	TeamGraphModeWalk     TeamGraphMode = "walk"
)

type TeamContextRequest struct {
	Query        string
	TopK         int
	GraphMode    TeamGraphMode
	IncludeTrace bool
}

type TeamContextEntity struct {
	RootObjectID string
	ObjectID     string
	EntityKind   string
	Name         string
	Summary      string
	Confidence   float64
	Score        float64
}

type TeamContextRelation struct {
	RootObjectID string
	ObjectID     string
	FromObjectID string
	ToObjectID   string
	RelationKind string
	Summary      string
	Strength     float64
	Confidence   float64
	Score        float64
}

type TeamContextFact struct {
	RootObjectID string
	ObjectID     string
	NodeObjectID string
	Text         string
	Predicate    string
	ObjectText   string
	Confidence   float64
	Domain       string
	Score        float64
}

type TeamContextEvent struct {
	RootObjectID string
	ObjectID     string
	Title        string
	Summary      string
	OccurredAt   string
	Confidence   float64
	Domain       string
	Score        float64
}

type TeamContextAssertion struct {
	RootObjectID      string
	ObjectID          string
	CandidateObjectID string
	SubjectObjectID   string
	ClaimSlotDigest   string
	Text              string
	Predicate         string
	ObjectText        string
	Confidence        float64
	Score             float64
}

type TeamContextCounts struct {
	Entities   int
	Relations  int
	Facts      int
	Events     int
	Assertions int
	Total      int
}

type TeamContextTrace struct {
	RootObjectID   string
	ObjectID       string
	Kind           string
	Lexical        float64
	Cosine         float64
	Graph          float64
	Score          float64
	AssertionState string
}

type TeamContextResponse struct {
	Entities   []TeamContextEntity
	Relations  []TeamContextRelation
	Facts      []TeamContextFact
	Events     []TeamContextEvent
	Assertions []TeamContextAssertion
	Counts     TeamContextCounts
	Trace      []TeamContextTrace
}

type TeamResumeRequest struct {
	ThreadID  string
	ProjectID string
	SessionID string
	Limit     int
}

type TeamResumeItem struct {
	RootObjectID string
	ObjectID     string
	Text         string
}

type TeamResumeResponse struct {
	WhereWeLeftOff                []TeamResumeItem
	ActiveDecisions               []TeamResumeItem
	OpenLoops                     []TeamResumeItem
	DoNotRepeat                   []TeamResumeItem
	RelevantEmotionalStateContext []TeamResumeItem
	SuggestedNextStep             []TeamResumeItem
	ReturnedCount                 int
}

func cloneTeamAuthorizedCorpus(corpus TeamAuthorizedCorpus) TeamAuthorizedCorpus {
	cloned := TeamAuthorizedCorpus{
		Memories:           append([]TeamMemoryDocument(nil), corpus.Memories...),
		Entities:           append([]TeamEntityDocument(nil), corpus.Entities...),
		Relations:          append([]TeamRelationDocument(nil), corpus.Relations...),
		Facts:              append([]TeamFactDocument(nil), corpus.Facts...),
		Events:             append([]TeamEventDocument(nil), corpus.Events...),
		Assertions:         append([]TeamAssertionDocument(nil), corpus.Assertions...),
		SemanticEmbeddings: append([]TeamSemanticEmbeddingDocument(nil), corpus.SemanticEmbeddings...),
		GraphLinks:         append([]TeamGraphLink(nil), corpus.GraphLinks...),
	}
	for index, embedding := range corpus.SemanticEmbeddings {
		cloned.SemanticEmbeddings[index].Vector = append([]float32(nil), embedding.Vector...)
	}
	for index, memory := range corpus.Memories {
		cloned.Memories[index].Tags = append([]string(nil), memory.Tags...)
		cloned.Memories[index].Embedding = append([]float32(nil), memory.Embedding...)
	}
	return cloned
}

func cloneTeamContinuityDocuments(documents []TeamContinuityDocument) []TeamContinuityDocument {
	cloned := append([]TeamContinuityDocument(nil), documents...)
	for index, document := range documents {
		cloned[index].Decisions = append([]string(nil), document.Decisions...)
		cloned[index].OpenLoops = append([]string(nil), document.OpenLoops...)
		cloned[index].DoNotRepeat = append([]string(nil), document.DoNotRepeat...)
		cloned[index].EmotionalStateContext = append([]string(nil), document.EmotionalStateContext...)
	}
	return cloned
}
