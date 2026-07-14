package teamjobs

import (
	"context"
	"errors"

	"github.com/nkkmnk/pulse/internal/store"
)

// StructuredProjectionStore is the trusted materialization boundary for
// projections whose content can be reconstructed entirely inside the Team
// store. Embeddings are deliberately excluded because producing a vector is
// an explicit deployment dependency, not a property of the durable store.
type StructuredProjectionStore interface {
	CompleteTeamMemoryEventProjection(context.Context, store.TeamMemoryEventProjectionRequest) (store.TeamProjectionCompletionResult, error)
	CompleteTeamGraphProjection(context.Context, store.TeamSemanticProjectionRequest) (store.TeamProjectionCompletionResult, error)
	CompleteTeamClaimProjection(context.Context, store.TeamSemanticProjectionRequest) (store.TeamProjectionCompletionResult, error)
	CompleteTeamContinuityProjection(context.Context, store.TeamSemanticProjectionRequest) (store.TeamProjectionCompletionResult, error)
}

// StoreProjectionProcessor dispatches a claimed job to the exact generation-
// fenced store completion API. It never accepts projection content from the
// caller: each store API reconstructs content from canonical Team ingress.
type StoreProjectionProcessor struct {
	store     StructuredProjectionStore
	embedding ProjectionProcessor
}

func NewStoreProjectionProcessor(projectionStore StructuredProjectionStore, embedding ProjectionProcessor) (*StoreProjectionProcessor, error) {
	if projectionStore == nil {
		return nil, errors.New("team projection processor: store is required")
	}
	return &StoreProjectionProcessor{store: projectionStore, embedding: embedding}, nil
}

func (processor *StoreProjectionProcessor) ProjectionDependencyHealth() ProjectionDependencyHealth {
	if processor.embedding == nil {
		return ProjectionDependencyHealth{
			State:  store.TeamProjectionDependencyDegraded,
			Reason: store.TeamProjectionWorkerReasonEmbeddingNotConfigured,
		}
	}
	return ProjectionDependencyHealth{State: store.TeamProjectionDependencyReady}
}

func (processor *StoreProjectionProcessor) ProcessTeamProjection(
	ctx context.Context,
	request ProjectionProcessRequest,
) error {
	switch request.Claim.ProjectionKind {
	case "event":
		_, err := processor.store.CompleteTeamMemoryEventProjection(ctx, store.TeamMemoryEventProjectionRequest{
			WriterID: request.Writer.WriterID, WriterToken: request.Writer.Token,
			JobID: request.Claim.JobID, LeaseToken: request.Claim.LeaseToken,
		})
		return err
	case "graph", "claim", "continuity":
		completion := store.TeamSemanticProjectionRequest{
			WriterID: request.Writer.WriterID, WriterToken: request.Writer.Token,
			JobID: request.Claim.JobID, LeaseToken: request.Claim.LeaseToken,
		}
		switch request.Claim.ProjectionKind {
		case "graph":
			_, err := processor.store.CompleteTeamGraphProjection(ctx, completion)
			return err
		case "claim":
			_, err := processor.store.CompleteTeamClaimProjection(ctx, completion)
			return err
		default:
			_, err := processor.store.CompleteTeamContinuityProjection(ctx, completion)
			return err
		}
	case "embedding":
		if processor.embedding != nil {
			return processor.embedding.ProcessTeamProjection(ctx, request)
		}
		return ProjectionProcessError{
			Code: store.TeamProjectionFailureDependencyUnavailable,
			Err:  errors.New("team embedding projection dependency is not configured"),
		}
	default:
		return ProjectionProcessError{
			Code: store.TeamProjectionFailureMaterialization,
			Err:  errors.New("unsupported team projection kind"),
		}
	}
}
