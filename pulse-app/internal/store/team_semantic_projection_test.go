package store

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

type teamSemanticProjectionFixture struct {
	graph teamGraphDeltaFixture
	root  TeamObjectWriteResult
}

func newTeamSemanticProjectionFixture(t *testing.T) *teamSemanticProjectionFixture {
	t.Helper()
	graph := newTeamGraphDeltaFixture(t)
	root := storeTeamSemanticProjectionRoot(t, graph, graph.write, "root-001")
	return &teamSemanticProjectionFixture{graph: graph, root: root}
}

func storeTeamSemanticProjectionRoot(
	t *testing.T,
	graph teamGraphDeltaFixture,
	write TeamGraphDeltaWrite,
	suffix string,
) TeamObjectWriteResult {
	t.Helper()
	result, err := graph.object.store.StoreTeamGraphDelta(
		context.Background(), graph.permit, graph.object.request.Writer,
		"request-team-semantic-"+suffix, graph.object.actor.clientKey, write,
	)
	if err != nil {
		t.Fatalf("store semantic projection root %s: %v", suffix, err)
	}
	return result
}

func (fixture *teamSemanticProjectionFixture) claim(
	t *testing.T,
	rootID, kind string,
) TeamProjectionJobClaim {
	t.Helper()
	claims, err := fixture.graph.object.store.ClaimTeamProjectionJobs(
		context.Background(), TeamProjectionClaimRequest{
			WriterID:       fixture.graph.object.lease.WriterID,
			WriterToken:    fixture.graph.object.lease.Token,
			ProjectionKind: kind, Limit: maxProjectionClaimBatch,
			LeaseTTL: 45 * time.Second,
		},
	)
	if err != nil {
		t.Fatalf("claim %s projection: %v", kind, err)
	}
	for _, claim := range claims {
		if claim.RootObjectID == rootID {
			return claim
		}
	}
	t.Fatalf("no %s projection claim for root %s in %+v", kind, rootID, claims)
	return TeamProjectionJobClaim{}
}

func semanticProjectionRequest(
	fixture *teamSemanticProjectionFixture,
	claim TeamProjectionJobClaim,
) TeamSemanticProjectionRequest {
	return TeamSemanticProjectionRequest{
		WriterID:    fixture.graph.object.lease.WriterID,
		WriterToken: fixture.graph.object.lease.Token,
		JobID:       claim.JobID, LeaseToken: claim.LeaseToken,
	}
}

func completeStructuredProjection(
	t *testing.T,
	fixture *teamSemanticProjectionFixture,
	kind string,
	request TeamSemanticProjectionRequest,
) TeamProjectionCompletionResult {
	t.Helper()
	var (
		result TeamProjectionCompletionResult
		err    error
	)
	switch kind {
	case "graph":
		result, err = fixture.graph.object.store.CompleteTeamGraphProjection(context.Background(), request)
	case "claim":
		result, err = fixture.graph.object.store.CompleteTeamClaimProjection(context.Background(), request)
	case "continuity":
		result, err = fixture.graph.object.store.CompleteTeamContinuityProjection(context.Background(), request)
	default:
		t.Fatalf("unsupported structured projection kind %q", kind)
	}
	if err != nil {
		t.Fatalf("complete %s projection: %v", kind, err)
	}
	return result
}

func TestCompleteTeamStructuredProjectionsUseStoredSourceAndReplayExactly(t *testing.T) {
	ctx := context.Background()
	fixture := newTeamSemanticProjectionFixture(t)
	defer fixture.graph.object.store.Close()
	legacyBefore := teamGraphLegacyCounts(t, fixture.graph.object.store)

	tests := []struct {
		kind          string
		materialTable string
		wantOutputs   int
	}{
		{kind: "graph", materialTable: "team_graph_materializations", wantOutputs: 5},
		{kind: "claim", materialTable: "team_assertion_materializations", wantOutputs: 1},
		{kind: "continuity", materialTable: "team_continuity_materializations", wantOutputs: 1},
	}
	for _, test := range tests {
		claim := fixture.claim(t, fixture.root.ObjectID, test.kind)
		request := semanticProjectionRequest(fixture, claim)
		first := completeStructuredProjection(t, fixture, test.kind, request)
		if first.State != "ready" || first.AlreadyReady || len(first.OutputObjectIDs) != test.wantOutputs {
			t.Fatalf("%s completion = %+v, want %d outputs", test.kind, first, test.wantOutputs)
		}
		if !sort.StringsAreSorted(first.OutputObjectIDs) {
			t.Fatalf("%s output IDs are not deterministic: %v", test.kind, first.OutputObjectIDs)
		}
		replay := completeStructuredProjection(t, fixture, test.kind, request)
		if !replay.AlreadyReady || !reflect.DeepEqual(replay.OutputObjectIDs, first.OutputObjectIDs) {
			t.Fatalf("%s replay = %+v, first = %+v", test.kind, replay, first)
		}

		var common, special int
		if err := fixture.graph.object.store.DB().QueryRow(`
			SELECT count(*) FROM team_semantic_materializations WHERE job_id = ?`, claim.JobID).
			Scan(&common); err != nil {
			t.Fatal(err)
		}
		if err := fixture.graph.object.store.DB().QueryRow(`
			SELECT count(*) FROM `+test.materialTable+` materialization
			JOIN team_semantic_materializations common USING(intent_id)
			WHERE common.job_id = ?`, claim.JobID).Scan(&special); err != nil {
			t.Fatal(err)
		}
		if common != test.wantOutputs || special != test.wantOutputs {
			t.Fatalf("%s materializations = common:%d special:%d, want %d",
				test.kind, common, special, test.wantOutputs)
		}
	}

	var invalidJSON, mismatchedDigests int
	if err := fixture.graph.object.store.DB().QueryRow(`
		SELECT count(*) FROM team_graph_materializations
		 WHERE json_valid(payload_json) <> 1 OR json_type(payload_json) <> 'object'
		    OR json_valid(resolved_refs_json) <> 1 OR json_type(resolved_refs_json) <> 'array'`).
		Scan(&invalidJSON); err != nil {
		t.Fatal(err)
	}
	if err := fixture.graph.object.store.DB().QueryRow(`
		SELECT count(*)
		  FROM team_semantic_materializations common
		  LEFT JOIN team_graph_materializations graph USING(intent_id)
		  LEFT JOIN team_assertion_materializations assertion USING(intent_id)
		  LEFT JOIN team_continuity_materializations continuity USING(intent_id)
		 WHERE (common.projection_kind = 'graph' AND graph.content_digest <> common.payload_digest)
		    OR (common.projection_kind = 'claim' AND (
		        assertion.content_digest <> common.payload_digest
		        OR assertion.claim_slot_digest <> common.semantic_key_digest))
		    OR (common.projection_kind = 'continuity'
		        AND continuity.content_digest <> common.payload_digest)`).Scan(&mismatchedDigests); err != nil {
		t.Fatal(err)
	}
	if invalidJSON != 0 || mismatchedDigests != 0 {
		t.Fatalf("invalid structured rows: json=%d digests=%d", invalidJSON, mismatchedDigests)
	}
	if after := teamGraphLegacyCounts(t, fixture.graph.object.store); !reflect.DeepEqual(after, legacyBefore) {
		t.Fatalf("structured projection touched local tables: before=%v after=%v", legacyBefore, after)
	}
	if _, err := fixture.graph.object.store.CheckTeamPolicyReadiness(
		ctx, policyReadinessOptions(fixture.graph.object.bootstrap, fixture.graph.object.lease),
	); err != nil {
		t.Fatalf("structured projections broke readiness: %v", err)
	}
}

func TestTeamStructuredProjectionSharesSameScopeDerivativeButKeepsRootContributions(t *testing.T) {
	fixture := newTeamSemanticProjectionFixture(t)
	defer fixture.graph.object.store.Close()

	firstClaim := fixture.claim(t, fixture.root.ObjectID, "claim")
	completeStructuredProjection(t, fixture, "claim", semanticProjectionRequest(fixture, firstClaim))
	firstDerivative := semanticIntentDerivative(t, fixture.graph.object.store, fixture.root.ObjectID, "claim")

	secondWrite := baseTeamGraphDeltaWrite()
	secondWrite.IdempotencyKey = "graph-request-002"
	second := storeTeamSemanticProjectionRoot(t, fixture.graph, secondWrite, "root-002")
	secondClaim := fixture.claim(t, second.ObjectID, "claim")
	completeStructuredProjection(t, fixture, "claim", semanticProjectionRequest(fixture, secondClaim))
	secondDerivative := semanticIntentDerivative(t, fixture.graph.object.store, second.ObjectID, "claim")
	if secondDerivative != firstDerivative {
		t.Fatalf("same scope claim derivatives differ: first=%s second=%s", firstDerivative, secondDerivative)
	}

	var assertions, contributions int
	if err := fixture.graph.object.store.DB().QueryRow(`
		SELECT count(*) FROM team_assertion_materializations
		 WHERE derivative_object_id = ?`, firstDerivative).Scan(&assertions); err != nil {
		t.Fatal(err)
	}
	if err := fixture.graph.object.store.DB().QueryRow(`
		SELECT count(*) FROM team_object_contributions
		 WHERE derivative_object_id = ?`, firstDerivative).Scan(&contributions); err != nil {
		t.Fatal(err)
	}
	if assertions != 2 || contributions != 2 {
		t.Fatalf("shared derivative evidence = assertions:%d contributions:%d, want 2/2",
			assertions, contributions)
	}

	atlas := teamGraphFixtureForProject(t, fixture.graph, "project-atlas")
	atlasWrite := baseTeamGraphDeltaWrite()
	atlasWrite.IdempotencyKey = "graph-request-atlas"
	atlasWrite.ActiveContext.ProjectID = "project-atlas"
	atlasWrite.TargetScope.ID = "project-atlas"
	atlasRoot := storeTeamSemanticProjectionRoot(t, atlas, atlasWrite, "root-atlas")
	atlasFixture := &teamSemanticProjectionFixture{graph: atlas, root: atlasRoot}
	atlasClaim := atlasFixture.claim(t, atlasRoot.ObjectID, "claim")
	completeStructuredProjection(t, atlasFixture, "claim", semanticProjectionRequest(atlasFixture, atlasClaim))
	atlasDerivative := semanticIntentDerivative(t, fixture.graph.object.store, atlasRoot.ObjectID, "claim")
	if atlasDerivative == firstDerivative {
		t.Fatalf("different project scopes shared derivative %s", atlasDerivative)
	}
	var pulseRows, atlasRows int
	if err := fixture.graph.object.store.DB().QueryRow(`
		SELECT count(*) FROM team_assertion_materializations
		 WHERE scope_type = 'project' AND scope_id = 'project-pulse'`).Scan(&pulseRows); err != nil {
		t.Fatal(err)
	}
	if err := fixture.graph.object.store.DB().QueryRow(`
		SELECT count(*) FROM team_assertion_materializations
		 WHERE scope_type = 'project' AND scope_id = 'project-atlas'`).Scan(&atlasRows); err != nil {
		t.Fatal(err)
	}
	if pulseRows != 2 || atlasRows != 1 {
		t.Fatalf("scope partition rows = pulse:%d atlas:%d", pulseRows, atlasRows)
	}
}

func TestTeamStructuredProjectionFailureRollsBackAndRetryCreatesNoDuplicates(t *testing.T) {
	fixture := newTeamSemanticProjectionFixture(t)
	defer fixture.graph.object.store.Close()
	claim := fixture.claim(t, fixture.root.ObjectID, "graph")
	request := semanticProjectionRequest(fixture, claim)
	if _, err := fixture.graph.object.store.DB().Exec(`
		CREATE TRIGGER reject_graph_projection_test
		BEFORE INSERT ON team_graph_materializations
		BEGIN SELECT RAISE(ABORT, 'synthetic graph materialization failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.graph.object.store.CompleteTeamGraphProjection(context.Background(), request); !errors.Is(err, ErrProjectionMaterializationFailed) {
		t.Fatalf("graph failure = %v, want %v", err, ErrProjectionMaterializationFailed)
	}
	for _, query := range []string{
		`SELECT count(*) FROM team_semantic_materializations WHERE job_id = ?`,
		`SELECT count(*) FROM team_projection_outputs WHERE job_id = ?`,
		`SELECT count(*) FROM team_object_contributions WHERE parent_object_id = ?`,
	} {
		argument := claim.JobID
		if strings.Contains(query, "parent_object_id") {
			argument = fixture.root.ObjectID
		}
		var count int
		if err := fixture.graph.object.store.DB().QueryRow(query, argument).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("failed projection left %d rows for %s", count, query)
		}
	}
	if _, err := fixture.graph.object.store.DB().Exec(`DROP TRIGGER reject_graph_projection_test`); err != nil {
		t.Fatal(err)
	}
	first := completeStructuredProjection(t, fixture, "graph", request)
	replay := completeStructuredProjection(t, fixture, "graph", request)
	if !replay.AlreadyReady || !reflect.DeepEqual(first.OutputObjectIDs, replay.OutputObjectIDs) {
		t.Fatalf("retry/replay mismatch: first=%+v replay=%+v", first, replay)
	}
}

func TestTeamGraphProjectionAcceptsMaximumContractBatch(t *testing.T) {
	graph := newTeamGraphDeltaFixture(t)
	defer graph.object.store.Close()
	write := maximumTeamSemanticEmbeddingGraphWrite()
	write.IdempotencyKey = "graph-request-structured-max"
	root := storeTeamSemanticProjectionRoot(t, graph, write, "structured-max")
	fixture := &teamSemanticProjectionFixture{graph: graph, root: root}
	claim := fixture.claim(t, root.ObjectID, "graph")
	result := completeStructuredProjection(
		t, fixture, "graph", semanticProjectionRequest(fixture, claim),
	)
	if len(result.OutputObjectIDs) != maxProjectionOutputs {
		t.Fatalf("maximum graph outputs = %d, want %d", len(result.OutputObjectIDs), maxProjectionOutputs)
	}
	for table, want := range map[string]int{
		"team_semantic_materializations": maxProjectionOutputs,
		"team_graph_materializations":    maxProjectionOutputs,
	} {
		var count int
		query := `SELECT count(*) FROM ` + table
		if table == "team_semantic_materializations" {
			query += ` WHERE job_id = ?`
		} else {
			query += ` WHERE intent_id IN (SELECT intent_id FROM team_semantic_materializations WHERE job_id = ?)`
		}
		if err := graph.object.store.DB().QueryRow(query, claim.JobID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s rows = %d, want %d", table, count, want)
		}
	}
}

func TestTeamStructuredProjectionRefusesTombstonedGenerationWithoutLocalWrites(t *testing.T) {
	fixture := newTeamSemanticProjectionFixture(t)
	defer fixture.graph.object.store.Close()
	legacyBefore := teamGraphLegacyCounts(t, fixture.graph.object.store)
	claim := fixture.claim(t, fixture.root.ObjectID, "continuity")
	if _, err := fixture.graph.object.store.DB().Exec(`
		UPDATE team_object_registry
		   SET lifecycle = 'tombstoned', generation = generation + 1
		 WHERE object_id = ?`, fixture.root.ObjectID); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.graph.object.store.CompleteTeamContinuityProjection(
		context.Background(), semanticProjectionRequest(fixture, claim),
	)
	if !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("stale continuity completion = %v, want concealed", err)
	}
	var common int
	if err := fixture.graph.object.store.DB().QueryRow(`
		SELECT count(*) FROM team_semantic_materializations WHERE job_id = ?`, claim.JobID).
		Scan(&common); err != nil {
		t.Fatal(err)
	}
	if common != 0 {
		t.Fatalf("stale completion left %d common rows", common)
	}
	if after := teamGraphLegacyCounts(t, fixture.graph.object.store); !reflect.DeepEqual(after, legacyBefore) {
		t.Fatalf("stale completion touched local tables: before=%v after=%v", legacyBefore, after)
	}
}

func TestTombstonedSemanticMaterializationsDoNotBlockReadinessBeforeCleanup(t *testing.T) {
	fixture := newTeamSemanticProjectionFixture(t)
	defer fixture.graph.object.store.Close()
	claim := fixture.claim(t, fixture.root.ObjectID, "claim")
	request := semanticProjectionRequest(fixture, claim)
	completeStructuredProjection(t, fixture, "claim", request)
	if _, err := fixture.graph.object.store.DB().Exec(`
		UPDATE team_object_registry
		   SET lifecycle = 'tombstoned', generation = generation + 1
		 WHERE object_id = ?`, fixture.root.ObjectID); err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.graph.object.store.DB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.graph.object.store.CancelTeamProjectionJobsTx(
		context.Background(), tx, TeamProjectionCancellationRequest{
			WriterID:     fixture.graph.object.lease.WriterID,
			WriterToken:  fixture.graph.object.lease.Token,
			RootObjectID: fixture.root.ObjectID, RootGeneration: 1,
			ReasonCode: TeamProjectionCancellationRootTombstoned,
		},
	); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.graph.object.store.CompleteTeamClaimProjection(
		context.Background(), request,
	); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("tombstoned replay = %v, want concealed", err)
	}
	if _, err := fixture.graph.object.store.CheckTeamPolicyReadiness(
		context.Background(),
		policyReadinessOptions(fixture.graph.object.bootstrap, fixture.graph.object.lease),
	); err != nil {
		t.Fatalf("tombstoned rows blocked readiness before cleanup: %v", err)
	}
	if err := fixture.graph.object.store.AuditTeamSemanticIntegrity(context.Background()); err != nil {
		t.Fatalf("tombstoned rows blocked explicit semantic audit before cleanup: %v", err)
	}
}

func TestTeamStructuredProjectionConcealsMissingSpuriousAndCorruptIntents(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *teamSemanticProjectionFixture)
	}{
		{name: "missing", mutate: func(t *testing.T, fixture *teamSemanticProjectionFixture) {
			if _, err := fixture.graph.object.store.DB().Exec(`
				DELETE FROM team_semantic_projection_intents
				 WHERE root_object_id = ? AND projection_kind = 'graph'
				   AND source_kind = 'node' AND source_ordinal = 0`, fixture.root.ObjectID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "spurious", mutate: func(t *testing.T, fixture *teamSemanticProjectionFixture) {
			if _, err := fixture.graph.object.store.DB().Exec(`
				INSERT INTO team_semantic_projection_intents(
					intent_id, root_object_id, store_id, team_id, scope_type, scope_id,
					root_generation, projection_kind, source_kind, source_ordinal,
					derivative_object_id, derivative_kind, semantic_key_digest,
					policy_digest, payload_digest, created_at)
				SELECT 'semantic_intent_spurious', root_object_id, store_id, team_id,
				       scope_type, scope_id, root_generation, 'graph', 'event', 49,
				       'semantic_object_spurious', 'graph_event',
				       ?, policy_digest, ?, created_at
				  FROM team_semantic_projection_intents
				 WHERE root_object_id = ? LIMIT 1`, strings.Repeat("a", 64),
				strings.Repeat("b", 64), fixture.root.ObjectID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "corrupt", mutate: func(t *testing.T, fixture *teamSemanticProjectionFixture) {
			if _, err := fixture.graph.object.store.DB().Exec(`
				DROP TRIGGER team_semantic_projection_intents_immutable`); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.graph.object.store.DB().Exec(`
				UPDATE team_semantic_projection_intents SET payload_digest = ?
				 WHERE root_object_id = ? AND projection_kind = 'graph'
				   AND source_kind = 'node' AND source_ordinal = 0`,
				strings.Repeat("c", 64), fixture.root.ObjectID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTeamSemanticProjectionFixture(t)
			defer fixture.graph.object.store.Close()
			claim := fixture.claim(t, fixture.root.ObjectID, "graph")
			test.mutate(t, fixture)
			_, err := fixture.graph.object.store.CompleteTeamGraphProjection(
				context.Background(), semanticProjectionRequest(fixture, claim),
			)
			if !errors.Is(err, ErrConcealedNotFound) {
				t.Fatalf("completion error = %v, want concealed", err)
			}
			var rows int
			if err := fixture.graph.object.store.DB().QueryRow(`
				SELECT count(*) FROM team_semantic_materializations WHERE job_id = ?`, claim.JobID).
				Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if rows != 0 {
				t.Fatalf("concealed completion wrote %d rows", rows)
			}
		})
	}
}

func TestReadyStructuredProjectionRequiresExactSpecialRowsForReplayAndReadiness(t *testing.T) {
	fixture := newTeamSemanticProjectionFixture(t)
	defer fixture.graph.object.store.Close()
	claim := fixture.claim(t, fixture.root.ObjectID, "claim")
	request := semanticProjectionRequest(fixture, claim)
	completeStructuredProjection(t, fixture, "claim", request)
	if _, err := fixture.graph.object.store.DB().Exec(`
		DELETE FROM team_assertion_materializations
		 WHERE intent_id = (
			SELECT intent_id FROM team_semantic_materializations WHERE job_id = ? LIMIT 1
		)`, claim.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.graph.object.store.CompleteTeamClaimProjection(context.Background(), request); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("ready replay after special-row deletion = %v, want concealed", err)
	}
	if err := fixture.graph.object.store.AuditTeamSemanticIntegrity(
		context.Background(),
	); !errors.Is(err, ErrTeamPolicyNotReady) {
		t.Fatalf("semantic audit after special-row deletion = %v, want not ready", err)
	}
}

func TestReadyStructuredProjectionRequiresExactContributionForReplayAndReadiness(t *testing.T) {
	fixture := newTeamSemanticProjectionFixture(t)
	defer fixture.graph.object.store.Close()
	claim := fixture.claim(t, fixture.root.ObjectID, "claim")
	request := semanticProjectionRequest(fixture, claim)
	completeStructuredProjection(t, fixture, "claim", request)
	if _, err := fixture.graph.object.store.DB().Exec(`
		DELETE FROM team_object_contributions
		 WHERE parent_object_id = ?
		   AND derivative_object_id = (
		       SELECT derivative_object_id FROM team_semantic_materializations
		        WHERE job_id = ? LIMIT 1
		   )`, fixture.root.ObjectID, claim.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.graph.object.store.CompleteTeamClaimProjection(
		context.Background(), request,
	); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("ready replay after contribution deletion = %v, want concealed", err)
	}
	if err := fixture.graph.object.store.AuditTeamSemanticIntegrity(
		context.Background(),
	); !errors.Is(err, ErrTeamPolicyNotReady) {
		t.Fatalf("semantic audit after contribution deletion = %v, want not ready", err)
	}
}

func TestReadyStructuredProjectionRejectsCorruptCommonDigestOnReplayAndReadiness(t *testing.T) {
	fixture := newTeamSemanticProjectionFixture(t)
	defer fixture.graph.object.store.Close()
	claim := fixture.claim(t, fixture.root.ObjectID, "claim")
	request := semanticProjectionRequest(fixture, claim)
	completeStructuredProjection(t, fixture, "claim", request)
	if _, err := fixture.graph.object.store.DB().Exec(`
		DROP TRIGGER team_semantic_materializations_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.graph.object.store.DB().Exec(`
		UPDATE team_semantic_materializations SET policy_digest = ?
		 WHERE job_id = ?`, strings.Repeat("d", 64), claim.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.graph.object.store.CompleteTeamClaimProjection(
		context.Background(), request,
	); !errors.Is(err, ErrConcealedNotFound) {
		t.Fatalf("ready replay after common corruption = %v, want concealed", err)
	}
	if err := fixture.graph.object.store.AuditTeamSemanticIntegrity(
		context.Background(),
	); !errors.Is(err, ErrTeamPolicyNotReady) {
		t.Fatalf("semantic audit after common corruption = %v, want not ready", err)
	}
}

func semanticIntentDerivative(t *testing.T, store *Store, rootID, kind string) string {
	t.Helper()
	var derivative string
	if err := store.DB().QueryRow(`
		SELECT derivative_object_id FROM team_semantic_projection_intents
		 WHERE root_object_id = ? AND projection_kind = ?
		 ORDER BY source_kind, source_ordinal LIMIT 1`, rootID, kind).Scan(&derivative); err != nil {
		t.Fatal(err)
	}
	return derivative
}

func teamGraphFixtureForProject(
	t *testing.T,
	base teamGraphDeltaFixture,
	projectID string,
) teamGraphDeltaFixture {
	t.Helper()
	if _, err := base.object.store.DB().Exec(`
		INSERT INTO team_projects(
			project_id, team_id, name, owner_principal_id,
			created_by_principal_id, created_at)
		VALUES (?, ?, 'Projection isolation project', ?, ?,
			'2026-07-11T00:00:00.000Z')`,
		projectID, base.object.bootstrap.TeamID, base.object.bootstrap.OwnerPrincipalID,
		base.object.bootstrap.OwnerPrincipalID); err != nil {
		t.Fatal(err)
	}
	if _, err := base.object.store.GrantProjectAccess(context.Background(), GrantProjectAccessRequest{
		ActorPrincipalID: base.object.bootstrap.OwnerPrincipalID,
		ProjectID:        projectID, TargetPrincipalID: base.object.actor.binding.AgentPrincipalID,
		AccessLevel: "write",
	}); err != nil {
		t.Fatal(err)
	}
	authorization := mutationWriteRequest(base.object.bootstrap, base.object.actor)
	authorization.ObjectKind = "graph_delta"
	authorization.Context = teamauth.ActiveContext{
		TeamID: base.object.bootstrap.TeamID, ProjectID: projectID,
		RepoID: "repo-pulse", AgentID: "agent-bound", SessionID: "session-2026-07-11",
	}
	authorization.RequestedScope = &teamauth.CanonicalScope{Type: teamauth.ScopeProject, ID: projectID}
	permit, err := base.object.store.AuthorizeTeamMutation(context.Background(), authorization)
	if err != nil {
		t.Fatal(err)
	}
	base.permit = permit
	return base
}
