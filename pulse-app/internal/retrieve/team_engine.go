package retrieve

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"

	"github.com/nkkmnk/pulse/internal/embed"
)

var (
	ErrInvalidTeamRetrievalRequest = errors.New("invalid team retrieval request")
	ErrInvalidTeamAuthorizedCorpus = errors.New("invalid team authorized corpus")
)

const (
	defaultTeamCandidateLimit = 200
	maxTeamCandidateLimit     = 1000
	maxTeamTopK               = 100
)

type TeamRetrievalConfig struct {
	Embedder       Embedder
	CandidateLimit int
}

// TeamRetrievalEngine owns no corpus or query cache. Every method loads one
// already-authorized request corpus and discards it before returning.
type TeamRetrievalEngine struct {
	embedder       Embedder
	candidateLimit int
}

func NewTeamRetrievalEngine(config TeamRetrievalConfig) *TeamRetrievalEngine {
	limit := config.CandidateLimit
	if limit <= 0 {
		limit = defaultTeamCandidateLimit
	}
	if limit > maxTeamCandidateLimit {
		limit = maxTeamCandidateLimit
	}
	return &TeamRetrievalEngine{embedder: config.Embedder, candidateLimit: limit}
}

func (engine *TeamRetrievalEngine) EmbeddingModel() string {
	if engine == nil || engine.embedder == nil {
		return ""
	}
	return engine.embedder.Model()
}

type scoredTeamMemory struct {
	document TeamMemoryDocument
	lexical  float64
	cosine   float64
	score    float64
}

func (engine *TeamRetrievalEngine) Retrieve(
	ctx context.Context,
	repository TeamAuthorizedRepository,
	request TeamRetrievalRequest,
) (TeamRetrievalResponse, error) {
	empty := emptyTeamRetrievalResponse()
	if engine == nil || repository == nil || strings.TrimSpace(request.Query) == "" ||
		request.TopK < 0 || request.TopK > maxTeamTopK {
		return empty, ErrInvalidTeamRetrievalRequest
	}
	topK := request.TopK
	if topK == 0 {
		topK = 5
	}
	corpus, err := repository.LoadAuthorizedCorpus(ctx, TeamCorpusQuery{
		Query: request.Query, Limit: engine.candidateLimit,
		EmbeddingModel: engine.EmbeddingModel(),
		Surface:        TeamCorpusSurfaceRecall,
	})
	if err != nil {
		return empty, err
	}
	if len(corpus.Memories) > engine.candidateLimit {
		return empty, ErrInvalidTeamAuthorizedCorpus
	}
	queryVector, err := engine.embedTeamQuery(ctx, request.Query)
	if err != nil {
		return empty, err
	}
	queryTokens := teamSearchTokens(request.Query)
	seenDocuments := make(map[string]bool, len(corpus.Memories))
	scored := make([]scoredTeamMemory, 0, len(corpus.Memories))
	for _, memory := range corpus.Memories {
		if !validTeamMemoryDocument(memory) || seenDocuments[memory.DocumentID] {
			return empty, ErrInvalidTeamAuthorizedCorpus
		}
		seenDocuments[memory.DocumentID] = true
		lexical := teamLexicalScore(queryTokens, memory.RedactedSummary)
		cosine := 0.0
		if len(memory.Embedding) != 0 {
			if engine.embedder == nil || memory.EmbeddingModel != engine.embedder.Model() ||
				len(queryVector) != len(memory.Embedding) {
				return empty, ErrInvalidTeamAuthorizedCorpus
			}
			var ok bool
			cosine, ok = teamCosine(queryVector, memory.Embedding)
			if !ok {
				return empty, ErrInvalidTeamAuthorizedCorpus
			}
			if cosine < 0 {
				cosine = 0
			}
		}
		score := lexical + cosine
		if score > 0 {
			scored = append(scored, scoredTeamMemory{
				document: memory, lexical: lexical, cosine: cosine, score: score,
			})
		}
	}
	sort.SliceStable(scored, func(left, right int) bool {
		if scored[left].score != scored[right].score {
			return scored[left].score > scored[right].score
		}
		if scored[left].document.RootObjectID != scored[right].document.RootObjectID {
			return scored[left].document.RootObjectID < scored[right].document.RootObjectID
		}
		return scored[left].document.DocumentID < scored[right].document.DocumentID
	})
	if len(scored) > topK {
		scored = scored[:topK]
	}
	rootIDs := teamUniqueSortedRootsFromMemories(scored)
	if err := repository.RecheckAuthorizedRoots(ctx, rootIDs); err != nil {
		return empty, err
	}
	response := empty
	for _, result := range scored {
		memory := result.document
		response.Items = append(response.Items, TeamRetrievedItem{
			RootObjectID: memory.RootObjectID, Kind: memory.Kind,
			Text: memory.RedactedSummary, Confidence: memory.Confidence,
			PrivacyTier: memory.PrivacyTier, Retention: memory.Retention,
			Tags: append([]string(nil), memory.Tags...), Score: clampTeamScore(result.score),
		})
		response.Trace = append(response.Trace, TeamRetrievalTrace{
			RootObjectID: memory.RootObjectID, Lexical: result.lexical,
			Cosine: result.cosine, Score: clampTeamScore(result.score),
		})
	}
	response.ReturnedCount = len(response.Items)
	return response, nil
}

func (engine *TeamRetrievalEngine) embedTeamQuery(ctx context.Context, query string) ([]float32, error) {
	if engine.embedder == nil {
		return nil, nil
	}
	vectors, err := engine.embedder.Embed(ctx, []string{query}, embed.TypeSearchQuery)
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 || len(vectors[0]) == 0 {
		return nil, ErrInvalidTeamAuthorizedCorpus
	}
	return append([]float32(nil), vectors[0]...), nil
}

func emptyTeamRetrievalResponse() TeamRetrievalResponse {
	return TeamRetrievalResponse{
		Items: make([]TeamRetrievedItem, 0), Trace: make([]TeamRetrievalTrace, 0),
	}
}

func validTeamMemoryDocument(memory TeamMemoryDocument) bool {
	return memory.DocumentID != "" && memory.RootObjectID != "" && memory.PartitionKey != "" &&
		memory.Kind != "" && strings.TrimSpace(memory.RedactedSummary) != "" &&
		validTeamUnitFloat(memory.Confidence) &&
		memory.PrivacyTier != "" && memory.Retention != ""
}

func validTeamUnitFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func teamSearchTokens(text string) []string {
	raw := graphTokenRe.FindAllString(strings.ToLower(text), -1)
	seen := make(map[string]bool, len(raw))
	tokens := make([]string, 0, len(raw))
	for _, token := range raw {
		if len([]rune(token)) < 2 || seen[token] {
			continue
		}
		seen[token] = true
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	return tokens
}

func teamLexicalScore(queryTokens []string, text string) float64 {
	if len(queryTokens) == 0 {
		return 0
	}
	documentTokens := teamSearchTokens(text)
	documentSet := make(map[string]bool, len(documentTokens))
	for _, token := range documentTokens {
		documentSet[token] = true
	}
	matches := 0
	for _, token := range queryTokens {
		if documentSet[token] {
			matches++
		}
	}
	return float64(matches) / float64(len(queryTokens))
}

func teamCosine(left, right []float32) (float64, bool) {
	if len(left) == 0 || len(left) != len(right) {
		return 0, false
	}
	dot, leftNorm, rightNorm := 0.0, 0.0, 0.0
	for index := range left {
		leftValue, rightValue := float64(left[index]), float64(right[index])
		if math.IsNaN(leftValue) || math.IsInf(leftValue, 0) ||
			math.IsNaN(rightValue) || math.IsInf(rightValue, 0) {
			return 0, false
		}
		dot += leftValue * rightValue
		leftNorm += leftValue * leftValue
		rightNorm += rightValue * rightValue
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0, false
	}
	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm)), true
}

func teamUniqueSortedRootsFromMemories(memories []scoredTeamMemory) []string {
	seen := make(map[string]bool, len(memories))
	roots := make([]string, 0, len(memories))
	for _, memory := range memories {
		if !seen[memory.document.RootObjectID] {
			seen[memory.document.RootObjectID] = true
			roots = append(roots, memory.document.RootObjectID)
		}
	}
	sort.Strings(roots)
	return roots
}
