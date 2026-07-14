package teamjobs

import (
	"context"
	"errors"
	"testing"

	"github.com/nkkmnk/pulse/internal/store"
)

type recordingStructuredProjectionStore struct {
	kind        string
	writerID    string
	writerToken string
	jobID       string
	leaseToken  string
	err         error
}

func (recording *recordingStructuredProjectionStore) capture(kind, writerID, writerToken, jobID, leaseToken string) (store.TeamProjectionCompletionResult, error) {
	recording.kind = kind
	recording.writerID = writerID
	recording.writerToken = writerToken
	recording.jobID = jobID
	recording.leaseToken = leaseToken
	return store.TeamProjectionCompletionResult{}, recording.err
}

func (recording *recordingStructuredProjectionStore) CompleteTeamMemoryEventProjection(_ context.Context, request store.TeamMemoryEventProjectionRequest) (store.TeamProjectionCompletionResult, error) {
	return recording.capture("event", request.WriterID, request.WriterToken, request.JobID, request.LeaseToken)
}

func (recording *recordingStructuredProjectionStore) CompleteTeamGraphProjection(_ context.Context, request store.TeamSemanticProjectionRequest) (store.TeamProjectionCompletionResult, error) {
	return recording.capture("graph", request.WriterID, request.WriterToken, request.JobID, request.LeaseToken)
}

func (recording *recordingStructuredProjectionStore) CompleteTeamClaimProjection(_ context.Context, request store.TeamSemanticProjectionRequest) (store.TeamProjectionCompletionResult, error) {
	return recording.capture("claim", request.WriterID, request.WriterToken, request.JobID, request.LeaseToken)
}

func (recording *recordingStructuredProjectionStore) CompleteTeamContinuityProjection(_ context.Context, request store.TeamSemanticProjectionRequest) (store.TeamProjectionCompletionResult, error) {
	return recording.capture("continuity", request.WriterID, request.WriterToken, request.JobID, request.LeaseToken)
}

func TestStoreProjectionProcessorDispatchesStructuredKindsWithExactAuthority(t *testing.T) {
	for _, kind := range []string{"event", "graph", "claim", "continuity"} {
		t.Run(kind, func(t *testing.T) {
			recording := &recordingStructuredProjectionStore{}
			processor, err := NewStoreProjectionProcessor(recording, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = processor.ProcessTeamProjection(context.Background(), ProjectionProcessRequest{
				Writer: store.TeamWriterLeaseIdentity{WriterID: "writer_1", Token: "writer-token"},
				Claim: store.TeamProjectionJobClaim{
					JobID: "job_1", LeaseToken: "lease-token", ProjectionKind: kind,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if recording.kind != kind || recording.writerID != "writer_1" ||
				recording.writerToken != "writer-token" || recording.jobID != "job_1" ||
				recording.leaseToken != "lease-token" {
				t.Fatalf("unexpected dispatch: %+v", recording)
			}
		})
	}
}

func TestStoreProjectionProcessorClassifiesMissingEmbeddingDependency(t *testing.T) {
	processor, err := NewStoreProjectionProcessor(&recordingStructuredProjectionStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = processor.ProcessTeamProjection(context.Background(), ProjectionProcessRequest{
		Writer: store.TeamWriterLeaseIdentity{WriterID: "writer_1", Token: "writer-token"},
		Claim:  store.TeamProjectionJobClaim{JobID: "job_1", LeaseToken: "lease-token", ProjectionKind: "embedding"},
	})
	var classified ProjectionFailureCoder
	if !errors.As(err, &classified) || classified.ProjectionFailureCode() != store.TeamProjectionFailureDependencyUnavailable {
		t.Fatalf("embedding error = %v, want dependency_unavailable", err)
	}
}

func TestStoreProjectionProcessorDelegatesConfiguredEmbeddingProcessor(t *testing.T) {
	called := false
	embedding := ProjectionProcessorFunc(func(_ context.Context, request ProjectionProcessRequest) error {
		called = request.Claim.ProjectionKind == "embedding"
		return nil
	})
	processor, err := NewStoreProjectionProcessor(&recordingStructuredProjectionStore{}, embedding)
	if err != nil {
		t.Fatal(err)
	}
	if err := processor.ProcessTeamProjection(context.Background(), ProjectionProcessRequest{
		Claim: store.TeamProjectionJobClaim{ProjectionKind: "embedding"},
	}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("configured embedding processor was not called")
	}
}

func TestStoreProjectionProcessorReportsEmbeddingDependencyTruthfully(t *testing.T) {
	missing, err := NewStoreProjectionProcessor(&recordingStructuredProjectionStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	health := missing.ProjectionDependencyHealth()
	if health.State != store.TeamProjectionDependencyDegraded ||
		health.Reason != store.TeamProjectionWorkerReasonEmbeddingNotConfigured {
		t.Fatalf("missing embedding health = %+v", health)
	}

	configured, err := NewStoreProjectionProcessor(
		&recordingStructuredProjectionStore{},
		ProjectionProcessorFunc(func(context.Context, ProjectionProcessRequest) error { return nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	health = configured.ProjectionDependencyHealth()
	if health.State != store.TeamProjectionDependencyReady || health.Reason != "" {
		t.Fatalf("configured embedding health = %+v", health)
	}
}
