package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

const teamSemanticEmbeddingTestModel = "bge-m3:test"

func teamSemanticEmbeddingIntentIDs(t *testing.T, fixture *teamSemanticProjectionFixture) []string {
	t.Helper()
	rows, err := fixture.graph.object.store.DB().Query(`
		SELECT intent_id
		  FROM team_semantic_projection_intents
		 WHERE root_object_id = ? AND projection_kind = 'embedding'
		 ORDER BY source_kind, source_ordinal`, fixture.root.ObjectID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var intentIDs []string
	for rows.Next() {
		var intentID string
		if err := rows.Scan(&intentID); err != nil {
			t.Fatal(err)
		}
		intentIDs = append(intentIDs, intentID)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return intentIDs
}

func teamSemanticEmbeddingRequest(
	t *testing.T,
	fixture *teamSemanticProjectionFixture,
	claim TeamProjectionJobClaim,
	dimensions int,
) TeamSemanticEmbeddingProjectionRequest {
	t.Helper()
	intentIDs := teamSemanticEmbeddingIntentIDs(t, fixture)
	results := make([]TeamSemanticEmbeddingResult, len(intentIDs))
	for index, intentID := range intentIDs {
		vector := make([]float32, dimensions)
		for dimension := range vector {
			vector[dimension] = float32(index+dimension+1) / 100
		}
		results[len(intentIDs)-1-index] = TeamSemanticEmbeddingResult{
			IntentID: intentID,
			Vector:   vector,
		}
	}
	return TeamSemanticEmbeddingProjectionRequest{
		WriterID: fixture.graph.object.lease.WriterID, WriterToken: fixture.graph.object.lease.Token,
		JobID: claim.JobID, LeaseToken: claim.LeaseToken,
		Model: teamSemanticEmbeddingTestModel, Results: results,
	}
}

func teamSemanticEmbeddingClaim(
	t *testing.T,
	fixture *teamSemanticProjectionFixture,
	leaseTTL time.Duration,
) TeamProjectionJobClaim {
	t.Helper()
	claims, err := fixture.graph.object.store.ClaimTeamProjectionJobs(
		context.Background(), TeamProjectionClaimRequest{
			WriterID: fixture.graph.object.lease.WriterID, WriterToken: fixture.graph.object.lease.Token,
			ProjectionKind: "embedding", Limit: maxProjectionClaimBatch, LeaseTTL: leaseTTL,
		},
	)
	if err != nil {
		t.Fatalf("claim embedding projection: %v", err)
	}
	for _, claim := range claims {
		if claim.RootObjectID == fixture.root.ObjectID {
			return claim
		}
	}
	t.Fatalf("no embedding projection claim for root %s", fixture.root.ObjectID)
	return TeamProjectionJobClaim{}
}

func cloneTeamSemanticEmbeddingRequest(
	request TeamSemanticEmbeddingProjectionRequest,
) TeamSemanticEmbeddingProjectionRequest {
	cloned := request
	cloned.Results = make([]TeamSemanticEmbeddingResult, len(request.Results))
	for index, result := range request.Results {
		cloned.Results[index] = result
		cloned.Results[index].Vector = append([]float32(nil), result.Vector...)
	}
	return cloned
}

func TestCompleteTeamSemanticEmbeddingProjectionIsExactAtomicAndReplaySafe(t *testing.T) {
	ctx := context.Background()
	fixture := newTeamSemanticProjectionFixture(t)
	defer fixture.graph.object.store.Close()
	claim := fixture.claim(t, fixture.root.ObjectID, "embedding")
	request := teamSemanticEmbeddingRequest(t, fixture, claim, 1)
	original := cloneTeamSemanticEmbeddingRequest(request)
	legacyBefore := teamGraphLegacyCounts(t, fixture.graph.object.store)
	var localEmbeddingsBefore int
	if err := fixture.graph.object.store.DB().QueryRow(`SELECT count(*) FROM event_embeddings`).
		Scan(&localEmbeddingsBefore); err != nil {
		t.Fatal(err)
	}

	first, err := fixture.graph.object.store.CompleteTeamSemanticEmbeddingProjection(ctx, request)
	if err != nil {
		t.Fatalf("complete semantic embedding projection: %v", err)
	}
	if first.State != "ready" || first.AlreadyReady || len(first.OutputObjectIDs) != len(request.Results) ||
		!sort.StringsAreSorted(first.OutputObjectIDs) {
		t.Fatalf("first completion = %+v", first)
	}
	if !reflect.DeepEqual(request, original) {
		t.Fatal("completion mutated caller-owned result vectors or ordering")
	}
	replay, err := fixture.graph.object.store.CompleteTeamSemanticEmbeddingProjection(ctx, request)
	if err != nil || !replay.AlreadyReady || !reflect.DeepEqual(replay.OutputObjectIDs, first.OutputObjectIDs) {
		t.Fatalf("ready replay = %+v, %v; first=%+v", replay, err, first)
	}
	changed := cloneTeamSemanticEmbeddingRequest(request)
	changed.Results[0].Vector[0] += 1
	if _, err := fixture.graph.object.store.CompleteTeamSemanticEmbeddingProjection(
		ctx, changed,
	); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("changed terminal vector = %v, want concealed", err)
	}
	changedModel := cloneTeamSemanticEmbeddingRequest(request)
	changedModel.Model = "bge-m3:other"
	if _, err := fixture.graph.object.store.CompleteTeamSemanticEmbeddingProjection(
		ctx, changedModel,
	); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("changed terminal model = %v, want concealed", err)
	}

	var commonRows, embeddingRows, badRows, localEmbeddingsAfter int
	if err := fixture.graph.object.store.DB().QueryRow(`
		SELECT count(*) FROM team_semantic_materializations
		 WHERE job_id = ? AND projection_kind = 'embedding'`, claim.JobID).Scan(&commonRows); err != nil {
		t.Fatal(err)
	}
	if err := fixture.graph.object.store.DB().QueryRow(`
		SELECT count(*) FROM team_semantic_embeddings embedding
		JOIN team_semantic_materializations common USING(intent_id)
		WHERE common.job_id = ?`, claim.JobID).Scan(&embeddingRows); err != nil {
		t.Fatal(err)
	}
	if err := fixture.graph.object.store.DB().QueryRow(`
		SELECT count(*)
		  FROM team_semantic_embeddings embedding
		  JOIN team_semantic_materializations common USING(intent_id)
		 WHERE common.job_id = ?
		   AND (embedding.content_digest <> common.payload_digest
		        OR embedding.dimensions <> json_array_length(embedding.vector_json))`, claim.JobID).
		Scan(&badRows); err != nil {
		t.Fatal(err)
	}
	rows, err := fixture.graph.object.store.DB().Query(`
		SELECT vector_json, vector_digest FROM team_semantic_embeddings embedding
		JOIN team_semantic_materializations common USING(intent_id)
		WHERE common.job_id = ?`, claim.JobID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var vectorJSON, digest string
		if err := rows.Scan(&vectorJSON, &digest); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		got := sha256.Sum256([]byte(vectorJSON))
		if hex.EncodeToString(got[:]) != digest {
			rows.Close()
			t.Fatalf("vector digest mismatch for %s", vectorJSON)
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.graph.object.store.DB().QueryRow(`SELECT count(*) FROM event_embeddings`).
		Scan(&localEmbeddingsAfter); err != nil {
		t.Fatal(err)
	}
	if commonRows != len(request.Results) || embeddingRows != len(request.Results) || badRows != 0 ||
		localEmbeddingsAfter != localEmbeddingsBefore {
		t.Fatalf("materialization rows common=%d embedding=%d bad=%d local=%d->%d",
			commonRows, embeddingRows, badRows, localEmbeddingsBefore, localEmbeddingsAfter)
	}
	if after := teamGraphLegacyCounts(t, fixture.graph.object.store); !reflect.DeepEqual(after, legacyBefore) {
		t.Fatalf("embedding projection touched local graph: before=%v after=%v", legacyBefore, after)
	}
	if _, err := fixture.graph.object.store.CheckTeamPolicyReadiness(
		ctx, policyReadinessOptions(fixture.graph.object.bootstrap, fixture.graph.object.lease),
	); err != nil {
		t.Fatalf("embedding projection broke readiness: %v", err)
	}
}

func TestTeamSemanticEmbeddingProjectionFailureRollsBackAndRetries(t *testing.T) {
	fixture := newTeamSemanticProjectionFixture(t)
	defer fixture.graph.object.store.Close()
	claim := fixture.claim(t, fixture.root.ObjectID, "embedding")
	request := teamSemanticEmbeddingRequest(t, fixture, claim, 2)
	if _, err := fixture.graph.object.store.DB().Exec(`
		CREATE TRIGGER reject_semantic_embedding_test
		BEFORE INSERT ON team_semantic_embeddings
		BEGIN SELECT RAISE(ABORT, 'synthetic semantic embedding failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.graph.object.store.CompleteTeamSemanticEmbeddingProjection(
		context.Background(), request,
	); !errors.Is(err, ErrProjectionMaterializationFailed) {
		t.Fatalf("failed completion = %v, want %v", err, ErrProjectionMaterializationFailed)
	}
	for _, check := range []struct {
		query    string
		argument string
	}{
		{query: `SELECT count(*) FROM team_semantic_materializations WHERE job_id = ?`, argument: claim.JobID},
		{query: `SELECT count(*) FROM team_semantic_embeddings embedding JOIN team_semantic_materializations common USING(intent_id) WHERE common.job_id = ?`, argument: claim.JobID},
		{query: `SELECT count(*) FROM team_projection_outputs WHERE job_id = ?`, argument: claim.JobID},
		{query: `SELECT count(*) FROM team_object_contributions WHERE parent_object_id = ?`, argument: fixture.root.ObjectID},
	} {
		var count int
		if err := fixture.graph.object.store.DB().QueryRow(check.query, check.argument).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("failed completion left %d rows for %s", count, check.query)
		}
	}
	if _, err := fixture.graph.object.store.DB().Exec(`DROP TRIGGER reject_semantic_embedding_test`); err != nil {
		t.Fatal(err)
	}
	first, err := fixture.graph.object.store.CompleteTeamSemanticEmbeddingProjection(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := fixture.graph.object.store.CompleteTeamSemanticEmbeddingProjection(context.Background(), request)
	if err != nil || !replay.AlreadyReady || !reflect.DeepEqual(first.OutputObjectIDs, replay.OutputObjectIDs) {
		t.Fatalf("retry/replay = %+v, %v; first=%+v", replay, err, first)
	}
}

func TestTeamSemanticEmbeddingProjectionRejectsInvalidResultSetsAndVectors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*TeamSemanticEmbeddingProjectionRequest)
	}{
		{name: "duplicate intent", mutate: func(request *TeamSemanticEmbeddingProjectionRequest) {
			request.Results[1].IntentID = request.Results[0].IntentID
		}},
		{name: "missing intent", mutate: func(request *TeamSemanticEmbeddingProjectionRequest) {
			request.Results = request.Results[:len(request.Results)-1]
		}},
		{name: "extra intent", mutate: func(request *TeamSemanticEmbeddingProjectionRequest) {
			request.Results = append(request.Results, TeamSemanticEmbeddingResult{IntentID: "semantic_intent_extra", Vector: []float32{1, 2}})
		}},
		{name: "unknown intent", mutate: func(request *TeamSemanticEmbeddingProjectionRequest) {
			request.Results[0].IntentID = "semantic_intent_unknown"
		}},
		{name: "invalid model uppercase", mutate: func(request *TeamSemanticEmbeddingProjectionRequest) {
			request.Model = "BGE-M3"
		}},
		{name: "empty model", mutate: func(request *TeamSemanticEmbeddingProjectionRequest) {
			request.Model = ""
		}},
		{name: "invalid model whitespace", mutate: func(request *TeamSemanticEmbeddingProjectionRequest) {
			request.Model = " bge-m3"
		}},
		{name: "model over cap", mutate: func(request *TeamSemanticEmbeddingProjectionRequest) {
			request.Model = strings.Repeat("m", 65)
		}},
		{name: "empty vector", mutate: func(request *TeamSemanticEmbeddingProjectionRequest) {
			request.Results[0].Vector = nil
		}},
		{name: "dimension mismatch", mutate: func(request *TeamSemanticEmbeddingProjectionRequest) {
			request.Results[0].Vector = []float32{1}
		}},
		{name: "all zero vector", mutate: func(request *TeamSemanticEmbeddingProjectionRequest) {
			request.Results[0].Vector = []float32{0, 0}
		}},
		{name: "nan vector", mutate: func(request *TeamSemanticEmbeddingProjectionRequest) {
			request.Results[0].Vector[0] = float32(math.NaN())
		}},
		{name: "infinite vector", mutate: func(request *TeamSemanticEmbeddingProjectionRequest) {
			request.Results[0].Vector[0] = float32(math.Inf(1))
		}},
		{name: "over dimension cap", mutate: func(request *TeamSemanticEmbeddingProjectionRequest) {
			request.Results[0].Vector = make([]float32, maxProjectionVectorDimensions+1)
			request.Results[0].Vector[0] = 1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTeamSemanticProjectionFixture(t)
			defer fixture.graph.object.store.Close()
			claim := fixture.claim(t, fixture.root.ObjectID, "embedding")
			request := teamSemanticEmbeddingRequest(t, fixture, claim, 2)
			test.mutate(&request)
			if _, err := fixture.graph.object.store.CompleteTeamSemanticEmbeddingProjection(
				context.Background(), request,
			); !errors.Is(err, ErrInvalidProjectionJobRequest) {
				t.Fatalf("completion error = %v, want invalid request", err)
			}
			var rows int
			if err := fixture.graph.object.store.DB().QueryRow(`
				SELECT count(*) FROM team_semantic_materializations WHERE job_id = ?`, claim.JobID).
				Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if rows != 0 {
				t.Fatalf("invalid request wrote %d common rows", rows)
			}
		})
	}
}

func TestTeamSemanticEmbeddingProjectionSharesDerivativesWithinScopeAndIsolatesScopes(t *testing.T) {
	fixture := newTeamSemanticProjectionFixture(t)
	defer fixture.graph.object.store.Close()
	firstClaim := fixture.claim(t, fixture.root.ObjectID, "embedding")
	firstRequest := teamSemanticEmbeddingRequest(t, fixture, firstClaim, 2)
	if _, err := fixture.graph.object.store.CompleteTeamSemanticEmbeddingProjection(context.Background(), firstRequest); err != nil {
		t.Fatal(err)
	}

	secondWrite := baseTeamGraphDeltaWrite()
	secondWrite.IdempotencyKey = "graph-request-embedding-002"
	secondRoot := storeTeamSemanticProjectionRoot(t, fixture.graph, secondWrite, "embedding-002")
	secondFixture := &teamSemanticProjectionFixture{graph: fixture.graph, root: secondRoot}
	secondClaim := secondFixture.claim(t, secondRoot.ObjectID, "embedding")
	secondRequest := teamSemanticEmbeddingRequest(t, secondFixture, secondClaim, 2)
	if _, err := fixture.graph.object.store.CompleteTeamSemanticEmbeddingProjection(context.Background(), secondRequest); err != nil {
		t.Fatal(err)
	}
	firstDerivatives := semanticEmbeddingDerivatives(t, fixture.graph.object.store, fixture.root.ObjectID)
	secondDerivatives := semanticEmbeddingDerivatives(t, fixture.graph.object.store, secondRoot.ObjectID)
	if !reflect.DeepEqual(firstDerivatives, secondDerivatives) {
		t.Fatalf("same-scope derivatives differ: first=%v second=%v", firstDerivatives, secondDerivatives)
	}
	for _, derivativeID := range firstDerivatives {
		var contributions, embeddings int
		if err := fixture.graph.object.store.DB().QueryRow(`
			SELECT count(*) FROM team_object_contributions WHERE derivative_object_id = ?`, derivativeID).
			Scan(&contributions); err != nil {
			t.Fatal(err)
		}
		if err := fixture.graph.object.store.DB().QueryRow(`
			SELECT count(*) FROM team_semantic_embeddings WHERE derivative_object_id = ?`, derivativeID).
			Scan(&embeddings); err != nil {
			t.Fatal(err)
		}
		if contributions != 2 || embeddings != 2 {
			t.Fatalf("derivative %s contributions=%d embeddings=%d, want 2/2",
				derivativeID, contributions, embeddings)
		}
	}

	atlas := teamGraphFixtureForProject(t, fixture.graph, "project-embedding-atlas")
	atlasWrite := baseTeamGraphDeltaWrite()
	atlasWrite.IdempotencyKey = "graph-request-embedding-atlas"
	atlasWrite.ActiveContext.ProjectID = "project-embedding-atlas"
	atlasWrite.TargetScope.ID = "project-embedding-atlas"
	atlasRoot := storeTeamSemanticProjectionRoot(t, atlas, atlasWrite, "embedding-atlas")
	atlasFixture := &teamSemanticProjectionFixture{graph: atlas, root: atlasRoot}
	atlasClaim := atlasFixture.claim(t, atlasRoot.ObjectID, "embedding")
	if _, err := atlas.object.store.CompleteTeamSemanticEmbeddingProjection(
		context.Background(), teamSemanticEmbeddingRequest(t, atlasFixture, atlasClaim, 2),
	); err != nil {
		t.Fatal(err)
	}
	atlasDerivatives := semanticEmbeddingDerivatives(t, fixture.graph.object.store, atlasRoot.ObjectID)
	for index := range firstDerivatives {
		if firstDerivatives[index] == atlasDerivatives[index] {
			t.Fatalf("cross-scope derivative leaked at %d: %s", index, firstDerivatives[index])
		}
	}
	var pulseRows, atlasRows int
	if err := fixture.graph.object.store.DB().QueryRow(`
		SELECT count(*) FROM team_semantic_embeddings WHERE scope_id = 'project-pulse'`).Scan(&pulseRows); err != nil {
		t.Fatal(err)
	}
	if err := fixture.graph.object.store.DB().QueryRow(`
		SELECT count(*) FROM team_semantic_embeddings WHERE scope_id = 'project-embedding-atlas'`).Scan(&atlasRows); err != nil {
		t.Fatal(err)
	}
	if pulseRows != 2*len(firstDerivatives) || atlasRows != len(atlasDerivatives) {
		t.Fatalf("scope rows pulse=%d atlas=%d", pulseRows, atlasRows)
	}
}

func semanticEmbeddingDerivatives(t *testing.T, store *Store, rootID string) []string {
	t.Helper()
	rows, err := store.DB().Query(`
		SELECT derivative_object_id FROM team_semantic_projection_intents
		 WHERE root_object_id = ? AND projection_kind = 'embedding'
		 ORDER BY source_kind, source_ordinal`, rootID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var derivativeID string
		if err := rows.Scan(&derivativeID); err != nil {
			t.Fatal(err)
		}
		result = append(result, derivativeID)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestTeamSemanticEmbeddingProjectionRefusesStaleAndCorruptState(t *testing.T) {
	t.Run("tombstoned root", func(t *testing.T) {
		fixture := newTeamSemanticProjectionFixture(t)
		defer fixture.graph.object.store.Close()
		claim := fixture.claim(t, fixture.root.ObjectID, "embedding")
		request := teamSemanticEmbeddingRequest(t, fixture, claim, 2)
		if _, err := fixture.graph.object.store.DB().Exec(`
			UPDATE team_object_registry SET lifecycle = 'tombstoned', generation = generation + 1
			 WHERE object_id = ?`, fixture.root.ObjectID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.graph.object.store.CompleteTeamSemanticEmbeddingProjection(
			context.Background(), request,
		); !errors.Is(err, ErrConcealedNotFound) {
			t.Fatalf("stale completion = %v, want concealed", err)
		}
		var rows int
		if err := fixture.graph.object.store.DB().QueryRow(`
			SELECT count(*) FROM team_semantic_materializations WHERE job_id = ?`, claim.JobID).
			Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Fatalf("stale completion wrote %d rows", rows)
		}
	})

	t.Run("missing stored intent", func(t *testing.T) {
		fixture := newTeamSemanticProjectionFixture(t)
		defer fixture.graph.object.store.Close()
		claim := fixture.claim(t, fixture.root.ObjectID, "embedding")
		request := teamSemanticEmbeddingRequest(t, fixture, claim, 2)
		if _, err := fixture.graph.object.store.DB().Exec(`
			DELETE FROM team_semantic_projection_intents
			 WHERE intent_id = (
				SELECT intent_id FROM team_semantic_projection_intents
				 WHERE root_object_id = ? AND projection_kind = 'embedding' LIMIT 1
			 )`, fixture.root.ObjectID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.graph.object.store.CompleteTeamSemanticEmbeddingProjection(
			context.Background(), request,
		); !errors.Is(err, ErrConcealedNotFound) {
			t.Fatalf("corrupt source completion = %v, want concealed", err)
		}
	})

	t.Run("ready row deleted", func(t *testing.T) {
		fixture := newTeamSemanticProjectionFixture(t)
		defer fixture.graph.object.store.Close()
		claim := fixture.claim(t, fixture.root.ObjectID, "embedding")
		request := teamSemanticEmbeddingRequest(t, fixture, claim, 2)
		if _, err := fixture.graph.object.store.CompleteTeamSemanticEmbeddingProjection(
			context.Background(), request,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.graph.object.store.DB().Exec(`
			DELETE FROM team_semantic_embeddings
			 WHERE intent_id = (SELECT intent_id FROM team_semantic_materializations WHERE job_id = ? LIMIT 1)
			   AND model = ?`, claim.JobID, request.Model); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.graph.object.store.CompleteTeamSemanticEmbeddingProjection(
			context.Background(), request,
		); !errors.Is(err, ErrConcealedNotFound) {
			t.Fatalf("corrupt ready replay = %v, want concealed", err)
		}
		if _, err := fixture.graph.object.store.CheckTeamPolicyReadiness(
			context.Background(), policyReadinessOptions(fixture.graph.object.bootstrap, fixture.graph.object.lease),
		); !errors.Is(err, ErrTeamPolicyNotReady) {
			t.Fatalf("readiness after embedding deletion = %v, want not ready", err)
		}
	})

	t.Run("ready contribution deleted", func(t *testing.T) {
		fixture := newTeamSemanticProjectionFixture(t)
		defer fixture.graph.object.store.Close()
		claim := fixture.claim(t, fixture.root.ObjectID, "embedding")
		request := teamSemanticEmbeddingRequest(t, fixture, claim, 2)
		if _, err := fixture.graph.object.store.CompleteTeamSemanticEmbeddingProjection(
			context.Background(), request,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.graph.object.store.DB().Exec(`
			DELETE FROM team_object_contributions
			 WHERE parent_object_id = ? AND derivative_object_id = (
				SELECT derivative_object_id FROM team_semantic_materializations
				 WHERE job_id = ? LIMIT 1
			 )`, fixture.root.ObjectID, claim.JobID); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.graph.object.store.CompleteTeamSemanticEmbeddingProjection(
			context.Background(), request,
		); !errors.Is(err, ErrConcealedNotFound) {
			t.Fatalf("ready replay without contribution = %v, want concealed", err)
		}
		if _, err := fixture.graph.object.store.CheckTeamPolicyReadiness(
			context.Background(), policyReadinessOptions(fixture.graph.object.bootstrap, fixture.graph.object.lease),
		); !errors.Is(err, ErrTeamPolicyNotReady) {
			t.Fatalf("readiness without contribution = %v, want not ready", err)
		}
	})
}

func TestTeamSemanticEmbeddingProjectionAcceptsMaximumBatchAndDimensions(t *testing.T) {
	graph := newTeamGraphDeltaFixture(t)
	defer graph.object.store.Close()
	write := maximumTeamSemanticEmbeddingGraphWrite()
	root := storeTeamSemanticProjectionRoot(t, graph, write, "embedding-max")
	fixture := &teamSemanticProjectionFixture{graph: graph, root: root}
	claim := teamSemanticEmbeddingClaim(t, fixture, maxProjectionLeaseTTL)
	request := teamSemanticEmbeddingRequest(t, fixture, claim, maxProjectionVectorDimensions)
	if got := len(request.Results); got != maxProjectionOutputs {
		t.Fatalf("maximum fixture has %d embedding intents, want %d", got, maxProjectionOutputs)
	}
	result, err := graph.object.store.CompleteTeamSemanticEmbeddingProjection(context.Background(), request)
	if err != nil {
		t.Fatalf("complete maximum semantic embedding projection: %v", err)
	}
	if len(result.OutputObjectIDs) != maxProjectionOutputs {
		t.Fatalf("maximum completion outputs = %d, want %d", len(result.OutputObjectIDs), maxProjectionOutputs)
	}
	var rows int
	if err := graph.object.store.DB().QueryRow(`
		SELECT count(*) FROM team_semantic_embeddings embedding
		JOIN team_semantic_materializations common USING(intent_id)
		WHERE common.job_id = ? AND embedding.dimensions = ?`, claim.JobID, maxProjectionVectorDimensions).
		Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != maxProjectionOutputs {
		t.Fatalf("maximum materializations = %d, want %d", rows, maxProjectionOutputs)
	}
}

func maximumTeamSemanticEmbeddingGraphWrite() TeamGraphDeltaWrite {
	write := baseTeamGraphDeltaWrite()
	write.Nodes = make([]TeamGraphNode, 30)
	for index := range write.Nodes {
		write.Nodes[index] = TeamGraphNode{
			ClientID: fmt.Sprintf("node:%02d", index), Kind: "thing",
			CanonicalName: fmt.Sprintf("Embedding node %02d", index), Domain: "real",
		}
	}
	write.Edges = make([]TeamGraphEdge, 50)
	for index := range write.Edges {
		write.Edges[index] = TeamGraphEdge{
			From: fmt.Sprintf("node:%02d", index%30),
			To:   fmt.Sprintf("node:%02d", (index*7+1)%30),
			Kind: fmt.Sprintf("link_%02d", index),
		}
	}
	write.Facts = make([]TeamGraphFact, 50)
	for index := range write.Facts {
		write.Facts[index] = TeamGraphFact{
			Node: fmt.Sprintf("node:%02d", index%30), Text: fmt.Sprintf("Embedding fact %02d", index),
			Confidence: graphFloat(0.8), Domain: "real",
		}
	}
	write.Events = make([]TeamGraphEvent, 20)
	for index := range write.Events {
		write.Events[index] = TeamGraphEvent{
			ClientID: fmt.Sprintf("event:%02d", index), Title: fmt.Sprintf("Embedding event %02d", index),
			Summary: fmt.Sprintf("Embedding event summary %02d", index), EntityRefs: []string{},
			Confidence: graphFloat(0.8), Domain: "real", Emotions: map[string]*float64{},
		}
	}
	write.Continuity = nil
	write.IdempotencyKey = "graph-request-embedding-max"
	return write
}
