package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func embeddingProjectionInputRequest(
	writer TeamWriterLeaseIdentity,
	claim TeamProjectionJobClaim,
) TeamEmbeddingProjectionInputRequest {
	return TeamEmbeddingProjectionInputRequest{
		WriterID: writer.WriterID, WriterToken: writer.Token,
		JobID: claim.JobID, LeaseToken: claim.LeaseToken,
	}
}

func TestReadTeamEmbeddingProjectionInputReconstructsMemoryInsideExactLease(t *testing.T) {
	fixture := newTeamMemoryProjectionFixture(t, nil)
	defer fixture.object.store.Close()
	claim := fixture.claim(t, "embedding")
	request := embeddingProjectionInputRequest(fixture.object.request.Writer, claim)

	input, err := fixture.object.store.ReadTeamEmbeddingProjectionInput(context.Background(), request)
	if err != nil {
		t.Fatalf("read memory embedding input: %v", err)
	}
	if input.SourceKind != TeamEmbeddingProjectionSourceMemory ||
		input.JobID != claim.JobID || input.RootObjectID != claim.RootObjectID ||
		input.RootGeneration != claim.RootGeneration ||
		len(input.Documents) != len(fixture.memory.CapsuleIDs) {
		t.Fatalf("memory input = %+v", input)
	}
	wantCapsules := make(map[string]bool, len(fixture.memory.CapsuleIDs))
	for _, capsuleID := range fixture.memory.CapsuleIDs {
		wantCapsules[capsuleID] = true
	}
	for index, document := range input.Documents {
		if !wantCapsules[document.ID] || document.Text == "" {
			t.Fatalf("memory document[%d] = %+v", index, document)
		}
		delete(wantCapsules, document.ID)
	}
	if len(wantCapsules) != 0 {
		t.Fatalf("missing memory capsules = %v", wantCapsules)
	}

	wrongLease := request
	wrongLease.LeaseToken = "projection_lease_wrong"
	if _, err := fixture.object.store.ReadTeamEmbeddingProjectionInput(context.Background(), wrongLease); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("wrong job lease error = %v", err)
	}
	wrongWriter := request
	wrongWriter.WriterToken = "wrong-writer-token"
	if _, err := fixture.object.store.ReadTeamEmbeddingProjectionInput(context.Background(), wrongWriter); !errors.Is(err, ErrTeamWriterLeaseMismatch) {
		t.Fatalf("wrong writer lease error = %v", err)
	}

	if _, err := fixture.object.store.DB().Exec(`
		UPDATE team_projection_jobs SET lease_expires_at = ? WHERE job_id = ?`,
		fixture.now.Add(-time.Second).Format(time.RFC3339Nano), request.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.object.store.ReadTeamEmbeddingProjectionInput(context.Background(), request); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("expired job lease error = %v", err)
	}
}

func TestReadTeamEmbeddingProjectionInputReconstructsCanonicalSemanticDocuments(t *testing.T) {
	fixture := newTeamSemanticProjectionFixture(t)
	defer fixture.graph.object.store.Close()
	claim := fixture.claim(t, fixture.root.ObjectID, "embedding")
	request := embeddingProjectionInputRequest(fixture.graph.object.request.Writer, claim)

	input, err := fixture.graph.object.store.ReadTeamEmbeddingProjectionInput(context.Background(), request)
	if err != nil {
		t.Fatalf("read semantic embedding input: %v", err)
	}
	if input.SourceKind != TeamEmbeddingProjectionSourceSemantic ||
		input.JobID != claim.JobID || input.RootObjectID != fixture.root.ObjectID ||
		input.RootGeneration != claim.RootGeneration || len(input.Documents) == 0 {
		t.Fatalf("semantic input = %+v", input)
	}
	intentIDs := teamSemanticEmbeddingIntentIDs(t, fixture)
	if len(input.Documents) != len(intentIDs) {
		t.Fatalf("semantic documents = %d, want %d", len(input.Documents), len(intentIDs))
	}
	for index, document := range input.Documents {
		if document.ID != intentIDs[index] || !json.Valid([]byte(document.Text)) {
			t.Fatalf("semantic document[%d] = id %q valid_json=%v", index, document.ID, json.Valid([]byte(document.Text)))
		}
	}
}

func TestEmbeddingProjectionInputCannotSurviveGenerationOrDeletionRace(t *testing.T) {
	ctx := context.Background()
	fixture := newTeamMemoryProjectionFixture(t, nil)
	defer fixture.object.store.Close()
	claim := fixture.claim(t, "embedding")
	request := embeddingProjectionInputRequest(fixture.object.request.Writer, claim)
	if _, err := fixture.object.store.ReadTeamEmbeddingProjectionInput(ctx, request); err != nil {
		t.Fatalf("initial read: %v", err)
	}

	if _, err := fixture.object.store.DB().Exec(`
		UPDATE team_object_registry
		   SET lifecycle = 'tombstoned', generation = generation + 1,
		       updated_at = '2026-07-11T12:01:00Z'
		 WHERE object_id = ?`, claim.RootObjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.object.store.ReadTeamEmbeddingProjectionInput(ctx, request); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("post-tombstone input error = %v", err)
	}
	results := make([]TeamMemoryEmbeddingResult, len(fixture.memory.CapsuleIDs))
	for index, capsuleID := range fixture.memory.CapsuleIDs {
		results[index] = TeamMemoryEmbeddingResult{CapsuleID: capsuleID, Vector: []float32{1, 0}}
	}
	if _, err := fixture.object.store.CompleteTeamMemoryEmbeddingProjection(ctx, TeamMemoryEmbeddingProjectionRequest{
		WriterID: request.WriterID, WriterToken: request.WriterToken,
		JobID: request.JobID, LeaseToken: request.LeaseToken,
		Model: "bge-m3:test", Results: results,
	}); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("post-tombstone completion error = %v", err)
	}
}
