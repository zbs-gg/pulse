package teamjobs

import (
	"context"
	"errors"
	"math"
	"net"
	"strings"

	"github.com/nkkmnk/pulse/internal/embed"
	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
)

const (
	maxTeamEmbeddingBatchDimensions = 4096
	maxTeamEmbeddingBatchDocuments  = 150
	maxTeamEmbeddingProviderBatch   = 64
)

// TeamEmbeddingProjectionStore exposes only lease-fenced canonical input and
// the two exact embedding completion paths. The processor cannot query raw
// tables or provide caller-controlled content to either completion method.
type TeamEmbeddingProjectionStore interface {
	ReadTeamEmbeddingProjectionInput(context.Context, store.TeamEmbeddingProjectionInputRequest) (store.TeamEmbeddingProjectionInput, error)
	CompleteTeamMemoryEmbeddingProjection(context.Context, store.TeamMemoryEmbeddingProjectionRequest) (store.TeamProjectionCompletionResult, error)
	CompleteTeamSemanticEmbeddingProjection(context.Context, store.TeamSemanticEmbeddingProjectionRequest) (store.TeamProjectionCompletionResult, error)
}

var _ TeamEmbeddingProjectionStore = (*store.Store)(nil)

// TeamEmbeddingProjectionProcessor embeds only the canonical documents that
// the Team store reconstructs for the exact claimed lease. Dependency errors
// are deliberately replaced with bounded classifications because providers
// may echo submitted text in error bodies.
type TeamEmbeddingProjectionProcessor struct {
	store    TeamEmbeddingProjectionStore
	embedder retrieve.Embedder
	model    string
}

func NewTeamEmbeddingProjectionProcessor(
	projectionStore TeamEmbeddingProjectionStore,
	embedder retrieve.Embedder,
) (*TeamEmbeddingProjectionProcessor, error) {
	if projectionStore == nil || embedder == nil || !validTeamEmbeddingModel(embedder.Model()) {
		return nil, errors.New("team embedding projection processor: invalid configuration")
	}
	return &TeamEmbeddingProjectionProcessor{
		store: projectionStore, embedder: embedder, model: embedder.Model(),
	}, nil
}

func (processor *TeamEmbeddingProjectionProcessor) ProjectionDependencyHealth() ProjectionDependencyHealth {
	return ProjectionDependencyHealth{State: store.TeamProjectionDependencyReady}
}

func (processor *TeamEmbeddingProjectionProcessor) ProcessTeamProjection(
	ctx context.Context,
	request ProjectionProcessRequest,
) error {
	if request.Claim.ProjectionKind != "embedding" {
		return embeddingMaterializationError()
	}
	input, err := processor.store.ReadTeamEmbeddingProjectionInput(ctx, store.TeamEmbeddingProjectionInputRequest{
		WriterID: request.Writer.WriterID, WriterToken: request.Writer.Token,
		JobID: request.Claim.JobID, LeaseToken: request.Claim.LeaseToken,
	})
	if err != nil {
		return err
	}
	if input.JobID != request.Claim.JobID ||
		input.RootObjectID != request.Claim.RootObjectID ||
		input.RootGeneration != request.Claim.RootGeneration ||
		len(input.Documents) < 1 || len(input.Documents) > maxTeamEmbeddingBatchDocuments {
		return embeddingMaterializationError()
	}
	texts := make([]string, len(input.Documents))
	for index, document := range input.Documents {
		if document.ID == "" || document.Text == "" {
			return embeddingMaterializationError()
		}
		texts[index] = document.Text
	}
	vectors := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += maxTeamEmbeddingProviderBatch {
		end := min(start+maxTeamEmbeddingProviderBatch, len(texts))
		batch, embedErr := processor.embedder.Embed(ctx, texts[start:end], embed.TypeSearchDocument)
		if embedErr != nil {
			return classifiedEmbeddingDependencyError(embedErr)
		}
		if !validTeamEmbeddingVectors(batch, end-start) {
			return embeddingMaterializationError()
		}
		vectors = append(vectors, batch...)
	}
	if !validTeamEmbeddingVectors(vectors, len(input.Documents)) {
		return embeddingMaterializationError()
	}

	switch input.SourceKind {
	case store.TeamEmbeddingProjectionSourceMemory:
		results := make([]store.TeamMemoryEmbeddingResult, len(input.Documents))
		for index, document := range input.Documents {
			results[index] = store.TeamMemoryEmbeddingResult{
				CapsuleID: document.ID, Vector: append([]float32(nil), vectors[index]...),
			}
		}
		_, err = processor.store.CompleteTeamMemoryEmbeddingProjection(ctx, store.TeamMemoryEmbeddingProjectionRequest{
			WriterID: request.Writer.WriterID, WriterToken: request.Writer.Token,
			JobID: request.Claim.JobID, LeaseToken: request.Claim.LeaseToken,
			Model: processor.model, Results: results,
		})
		return err
	case store.TeamEmbeddingProjectionSourceSemantic:
		results := make([]store.TeamSemanticEmbeddingResult, len(input.Documents))
		for index, document := range input.Documents {
			results[index] = store.TeamSemanticEmbeddingResult{
				IntentID: document.ID, Vector: append([]float32(nil), vectors[index]...),
			}
		}
		_, err = processor.store.CompleteTeamSemanticEmbeddingProjection(ctx, store.TeamSemanticEmbeddingProjectionRequest{
			WriterID: request.Writer.WriterID, WriterToken: request.Writer.Token,
			JobID: request.Claim.JobID, LeaseToken: request.Claim.LeaseToken,
			Model: processor.model, Results: results,
		})
		return err
	default:
		return embeddingMaterializationError()
	}
}

func validTeamEmbeddingModel(model string) bool {
	if model == "" || len(model) > 64 {
		return false
	}
	for index := range len(model) {
		character := model[index]
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("_.:-", rune(character)) {
			continue
		}
		return false
	}
	return true
}

func validTeamEmbeddingVectors(vectors [][]float32, documents int) bool {
	if documents < 1 || len(vectors) != documents {
		return false
	}
	dimensions := 0
	aggregate := 0
	for _, vector := range vectors {
		if len(vector) < 1 || len(vector) > maxTeamEmbeddingBatchDimensions ||
			len(vector) > maxTeamEmbeddingBatchDocuments*maxTeamEmbeddingBatchDimensions-aggregate {
			return false
		}
		aggregate += len(vector)
		if dimensions == 0 {
			dimensions = len(vector)
		} else if len(vector) != dimensions {
			return false
		}
		nonZero := false
		for _, value := range vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return false
			}
			if value != 0 {
				nonZero = true
			}
		}
		if !nonZero {
			return false
		}
	}
	return true
}

func classifiedEmbeddingDependencyError(err error) error {
	code := store.TeamProjectionFailureDependencyUnavailable
	var networkError net.Error
	switch {
	case errors.Is(err, context.Canceled):
		code = store.TeamProjectionFailureWorkerInterrupted
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &networkError) && networkError.Timeout():
		code = store.TeamProjectionFailureDependencyTimeout
	case strings.Contains(strings.ToLower(err.Error()), "status 429"),
		strings.Contains(strings.ToLower(err.Error()), "rate limit"):
		code = store.TeamProjectionFailureRateLimited
	}
	return ProjectionProcessError{
		Code: code, Err: errors.New("team embedding dependency failed"),
	}
}

func embeddingMaterializationError() error {
	return ProjectionProcessError{
		Code: store.TeamProjectionFailureMaterialization,
		Err:  errors.New("team embedding response is invalid"),
	}
}
