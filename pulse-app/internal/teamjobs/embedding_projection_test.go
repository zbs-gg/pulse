package teamjobs

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/nkkmnk/pulse/internal/embed"
	"github.com/nkkmnk/pulse/internal/store"
)

type embeddingProjectionStoreFake struct {
	input            store.TeamEmbeddingProjectionInput
	readErr          error
	completionErr    error
	readRequests     []store.TeamEmbeddingProjectionInputRequest
	memoryRequests   []store.TeamMemoryEmbeddingProjectionRequest
	semanticRequests []store.TeamSemanticEmbeddingProjectionRequest
}

func (fake *embeddingProjectionStoreFake) ReadTeamEmbeddingProjectionInput(
	_ context.Context,
	request store.TeamEmbeddingProjectionInputRequest,
) (store.TeamEmbeddingProjectionInput, error) {
	fake.readRequests = append(fake.readRequests, request)
	return fake.input, fake.readErr
}

func (fake *embeddingProjectionStoreFake) CompleteTeamMemoryEmbeddingProjection(
	_ context.Context,
	request store.TeamMemoryEmbeddingProjectionRequest,
) (store.TeamProjectionCompletionResult, error) {
	fake.memoryRequests = append(fake.memoryRequests, request)
	return store.TeamProjectionCompletionResult{}, fake.completionErr
}

func (fake *embeddingProjectionStoreFake) CompleteTeamSemanticEmbeddingProjection(
	_ context.Context,
	request store.TeamSemanticEmbeddingProjectionRequest,
) (store.TeamProjectionCompletionResult, error) {
	fake.semanticRequests = append(fake.semanticRequests, request)
	return store.TeamProjectionCompletionResult{}, fake.completionErr
}

type projectionEmbedderFake struct {
	model     string
	vectors   [][]float32
	err       error
	texts     [][]string
	inputType []embed.InputType
}

type boundedBatchProjectionEmbedder struct {
	model   string
	batches [][]string
	next    int
}

func (embedder *boundedBatchProjectionEmbedder) Model() string { return embedder.model }

func (embedder *boundedBatchProjectionEmbedder) Embed(
	_ context.Context,
	texts []string,
	inputType embed.InputType,
) ([][]float32, error) {
	if inputType != embed.TypeSearchDocument {
		return nil, errors.New("unexpected embedding input type")
	}
	embedder.batches = append(embedder.batches, append([]string(nil), texts...))
	vectors := make([][]float32, len(texts))
	for index := range texts {
		embedder.next++
		vectors[index] = []float32{float32(embedder.next), 1}
	}
	return vectors, nil
}

func (fake *projectionEmbedderFake) Model() string { return fake.model }

func (fake *projectionEmbedderFake) Embed(
	_ context.Context,
	texts []string,
	inputType embed.InputType,
) ([][]float32, error) {
	fake.texts = append(fake.texts, append([]string(nil), texts...))
	fake.inputType = append(fake.inputType, inputType)
	vectors := make([][]float32, len(fake.vectors))
	for index := range fake.vectors {
		vectors[index] = append([]float32(nil), fake.vectors[index]...)
	}
	return vectors, fake.err
}

func embeddingProjectionRequest() ProjectionProcessRequest {
	return ProjectionProcessRequest{
		Writer: store.TeamWriterLeaseIdentity{WriterID: "writer_1", Token: "writer-token"},
		Claim: store.TeamProjectionJobClaim{
			JobID: "job_1", RootObjectID: "root_1", RootGeneration: 3,
			LeaseToken: "projection_lease_1", ProjectionKind: "embedding",
		},
	}
}

func embeddingInput(
	sourceKind string,
	documents []store.TeamEmbeddingProjectionDocument,
) store.TeamEmbeddingProjectionInput {
	return store.TeamEmbeddingProjectionInput{
		JobID: "job_1", RootObjectID: "root_1", RootGeneration: 3,
		SourceKind: sourceKind, Documents: documents,
	}
}

func TestTeamEmbeddingProjectionProcessorCompletesMemoryAndSemanticSearchDocuments(t *testing.T) {
	for _, test := range []struct {
		name       string
		sourceKind string
	}{
		{name: "memory", sourceKind: store.TeamEmbeddingProjectionSourceMemory},
		{name: "semantic", sourceKind: store.TeamEmbeddingProjectionSourceSemantic},
	} {
		t.Run(test.name, func(t *testing.T) {
			storage := &embeddingProjectionStoreFake{input: embeddingInput(
				test.sourceKind,
				[]store.TeamEmbeddingProjectionDocument{
					{ID: "document_1", Text: "first canonical document"},
					{ID: "document_2", Text: "second canonical document"},
				},
			)}
			embedder := &projectionEmbedderFake{
				model: "bge-m3:test", vectors: [][]float32{{1, 0}, {0, 1}},
			}
			processor, err := NewTeamEmbeddingProjectionProcessor(storage, embedder)
			if err != nil {
				t.Fatal(err)
			}
			if err := processor.ProcessTeamProjection(context.Background(), embeddingProjectionRequest()); err != nil {
				t.Fatalf("process: %v", err)
			}
			if len(storage.readRequests) != 1 || storage.readRequests[0].WriterToken != "writer-token" ||
				storage.readRequests[0].LeaseToken != "projection_lease_1" {
				t.Fatalf("read requests = %+v", storage.readRequests)
			}
			if len(embedder.texts) != 1 || len(embedder.texts[0]) != 2 ||
				embedder.inputType[0] != embed.TypeSearchDocument {
				t.Fatalf("embed calls = texts:%v input_type:%v", embedder.texts, embedder.inputType)
			}
			if test.sourceKind == store.TeamEmbeddingProjectionSourceMemory {
				if len(storage.memoryRequests) != 1 || len(storage.semanticRequests) != 0 ||
					storage.memoryRequests[0].Model != "bge-m3:test" ||
					storage.memoryRequests[0].Results[1].CapsuleID != "document_2" {
					t.Fatalf("memory completion = %+v semantic=%+v", storage.memoryRequests, storage.semanticRequests)
				}
			} else if len(storage.semanticRequests) != 1 || len(storage.memoryRequests) != 0 ||
				storage.semanticRequests[0].Model != "bge-m3:test" ||
				storage.semanticRequests[0].Results[1].IntentID != "document_2" {
				t.Fatalf("semantic completion = %+v memory=%+v", storage.semanticRequests, storage.memoryRequests)
			}
		})
	}
}

func TestTeamEmbeddingProjectionProcessorChunksMaximumDocumentSetAndPreservesOrder(t *testing.T) {
	documents := make([]store.TeamEmbeddingProjectionDocument, maxTeamEmbeddingBatchDocuments)
	for index := range documents {
		documents[index] = store.TeamEmbeddingProjectionDocument{
			ID:   fmt.Sprintf("document_%03d", index),
			Text: fmt.Sprintf("canonical document %03d", index),
		}
	}
	storage := &embeddingProjectionStoreFake{input: embeddingInput(
		store.TeamEmbeddingProjectionSourceMemory,
		documents,
	)}
	embedder := &boundedBatchProjectionEmbedder{model: "bge-m3:test"}
	processor, err := NewTeamEmbeddingProjectionProcessor(storage, embedder)
	if err != nil {
		t.Fatal(err)
	}
	if err := processor.ProcessTeamProjection(context.Background(), embeddingProjectionRequest()); err != nil {
		t.Fatalf("process maximum document set: %v", err)
	}
	if len(embedder.batches) < 2 {
		t.Fatalf("embedding calls = %d, want multiple provider-independent chunks", len(embedder.batches))
	}
	seen := 0
	for _, batch := range embedder.batches {
		if len(batch) == 0 || len(batch) > 64 {
			t.Fatalf("embedding batch size = %d, want 1..64", len(batch))
		}
		for _, text := range batch {
			if text != documents[seen].Text {
				t.Fatalf("embedding order at %d = %q, want %q", seen, text, documents[seen].Text)
			}
			seen++
		}
	}
	if seen != len(documents) {
		t.Fatalf("embedded documents = %d, want %d", seen, len(documents))
	}
	if len(storage.memoryRequests) != 1 || len(storage.memoryRequests[0].Results) != len(documents) {
		t.Fatalf("memory completion requests = %+v", storage.memoryRequests)
	}
	for index, result := range storage.memoryRequests[0].Results {
		if result.CapsuleID != documents[index].ID || len(result.Vector) != 2 ||
			result.Vector[0] != float32(index+1) {
			t.Fatalf("completion result %d = %+v", index, result)
		}
	}
}

func TestTeamEmbeddingProjectionProcessorRejectsInvalidVectorBatches(t *testing.T) {
	tests := []struct {
		name    string
		vectors [][]float32
	}{
		{name: "count", vectors: [][]float32{{1, 0}}},
		{name: "dimensions", vectors: [][]float32{{1, 0}, {1}}},
		{name: "zero", vectors: [][]float32{{1, 0}, {0, 0}}},
		{name: "non_finite", vectors: [][]float32{{1, 0}, {float32(math.NaN()), 1}}},
		{name: "too_wide", vectors: [][]float32{{1, 0}, make([]float32, 4097)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			storage := &embeddingProjectionStoreFake{input: embeddingInput(
				store.TeamEmbeddingProjectionSourceMemory,
				[]store.TeamEmbeddingProjectionDocument{
					{ID: "document_1", Text: "first"}, {ID: "document_2", Text: "second"},
				},
			)}
			processor, err := NewTeamEmbeddingProjectionProcessor(storage, &projectionEmbedderFake{
				model: "bge-m3:test", vectors: test.vectors,
			})
			if err != nil {
				t.Fatal(err)
			}
			err = processor.ProcessTeamProjection(context.Background(), embeddingProjectionRequest())
			var classified ProjectionFailureCoder
			if !errors.As(err, &classified) || classified.ProjectionFailureCode() != store.TeamProjectionFailureMaterialization {
				t.Fatalf("invalid vectors error = %v", err)
			}
			if len(storage.memoryRequests)+len(storage.semanticRequests) != 0 {
				t.Fatal("invalid vectors reached completion")
			}
		})
	}
}

func TestTeamEmbeddingProjectionProcessorClassifiesDependencyFailureWithoutContent(t *testing.T) {
	private := "private-capsule-token=never-log-this"
	storage := &embeddingProjectionStoreFake{input: embeddingInput(
		store.TeamEmbeddingProjectionSourceMemory,
		[]store.TeamEmbeddingProjectionDocument{{ID: "document_1", Text: private}},
	)}
	processor, err := NewTeamEmbeddingProjectionProcessor(storage, &projectionEmbedderFake{
		model: "bge-m3:test", err: errors.New("provider echoed " + private),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = processor.ProcessTeamProjection(context.Background(), embeddingProjectionRequest())
	var classified ProjectionFailureCoder
	if !errors.As(err, &classified) || classified.ProjectionFailureCode() != store.TeamProjectionFailureDependencyUnavailable {
		t.Fatalf("dependency error = %v", err)
	}
	if strings.Contains(err.Error(), private) || len(storage.memoryRequests) != 0 {
		t.Fatalf("dependency failure leaked content or completed: %q", err)
	}
}

func TestTeamEmbeddingProjectionProcessorPreservesStaleLeaseAndGenerationRace(t *testing.T) {
	embedder := &projectionEmbedderFake{model: "bge-m3:test", vectors: [][]float32{{1, 0}}}
	storage := &embeddingProjectionStoreFake{readErr: store.ErrConcealedNotFound}
	processor, err := NewTeamEmbeddingProjectionProcessor(storage, embedder)
	if err != nil {
		t.Fatal(err)
	}
	if err := processor.ProcessTeamProjection(context.Background(), embeddingProjectionRequest()); !errors.Is(err, store.ErrConcealedNotFound) {
		t.Fatalf("stale read error = %v", err)
	}
	if len(embedder.texts) != 0 {
		t.Fatal("stale lease reached embedder")
	}

	storage.readErr = nil
	storage.input = embeddingInput(
		store.TeamEmbeddingProjectionSourceMemory,
		[]store.TeamEmbeddingProjectionDocument{{ID: "document_1", Text: "safe"}},
	)
	storage.completionErr = store.ErrConcealedNotFound
	if err := processor.ProcessTeamProjection(context.Background(), embeddingProjectionRequest()); !errors.Is(err, store.ErrConcealedNotFound) {
		t.Fatalf("generation race completion error = %v", err)
	}
}

func TestTeamEmbeddingProjectionProcessorReportsConfiguredDependencyReady(t *testing.T) {
	processor, err := NewTeamEmbeddingProjectionProcessor(
		&embeddingProjectionStoreFake{}, &projectionEmbedderFake{model: "bge-m3:test"},
	)
	if err != nil {
		t.Fatal(err)
	}
	health := processor.ProjectionDependencyHealth()
	if health.State != store.TeamProjectionDependencyReady || health.Reason != "" {
		t.Fatalf("health = %+v", health)
	}
}
