package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/nkkmnk/pulse/internal/teamauth"
)

type teamGraphDeltaFixture struct {
	object teamObjectWriteFixture
	permit TeamMutationPermit
	write  TeamGraphDeltaWrite
}

const (
	teamGraphGatewayCanonicalSHA256 = "15b83a96a9b2ce3f4a286a2ca077dc1ea4f02d44dd47139af9695df62f65a0dc"
	teamGraphStoreBodySHA256        = "394ede8c7840129887eb825f8176c5375582e1d13b97016949e4a683ed52e18a"
)

func graphString(value string) *string  { return &value }
func graphFloat(value float64) *float64 { return &value }
func graphBool(value bool) *bool        { return &value }

func TestTeamGraphECMAScriptNFKCLowerGoldenVectors(t *testing.T) {
	// Frozen from Node.js String.prototype.normalize("NFKC").toLowerCase().
	tests := []struct {
		input string
		want  string
	}{
		{input: "İ", want: "i\u0307"},
		{input: "I\u0307", want: "i\u0307"},
		{input: "Aİ", want: "ai\u0307"},
		{input: "ΟΣ", want: "ος"},
		{input: "ΟΣΑ", want: "οσα"},
		{input: "Σ", want: "σ"},
	}
	for _, test := range tests {
		if got := teamGraphECMAScriptNFKCLower(test.input); got != test.want {
			t.Errorf("NFKC+toLowerCase(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func newTeamGraphDeltaFixture(t *testing.T) teamGraphDeltaFixture {
	t.Helper()
	return newTeamGraphDeltaFixtureAt(t, filepath.Join(t.TempDir(), "team.db"))
}

func newTeamGraphDeltaFixtureAt(t *testing.T, path string) teamGraphDeltaFixture {
	t.Helper()
	object := newTeamObjectWriteFixtureAt(t, path)
	if _, err := object.store.DB().Exec(`
		INSERT INTO team_projects(
			project_id, team_id, name, owner_principal_id,
			created_by_principal_id, created_at)
		VALUES ('project-pulse', ?, 'Pulse parity project', ?, ?,
			'2026-07-11T00:00:00.000Z')`,
		object.bootstrap.TeamID, object.bootstrap.OwnerPrincipalID,
		object.bootstrap.OwnerPrincipalID); err != nil {
		object.store.Close()
		t.Fatalf("insert exact project fixture: %v", err)
	}
	if _, err := object.store.GrantProjectAccess(context.Background(), GrantProjectAccessRequest{
		ActorPrincipalID: object.bootstrap.OwnerPrincipalID,
		ProjectID:        "project-pulse", TargetPrincipalID: object.actor.binding.AgentPrincipalID,
		AccessLevel: "write",
	}); err != nil {
		object.store.Close()
		t.Fatalf("grant graph fixture project: %v", err)
	}
	authorization := mutationWriteRequest(object.bootstrap, object.actor)
	authorization.ObjectKind = "graph_delta"
	authorization.Context = teamauth.ActiveContext{
		TeamID: object.bootstrap.TeamID, ProjectID: "project-pulse",
		RepoID: "repo-pulse", AgentID: "agent-bound", SessionID: "session-2026-07-11",
	}
	authorization.RequestedScope = &teamauth.CanonicalScope{
		Type: teamauth.ScopeProject, ID: "project-pulse",
	}
	permit, err := object.store.AuthorizeTeamMutation(context.Background(), authorization)
	if err != nil {
		object.store.Close()
		t.Fatalf("authorize graph fixture: %v", err)
	}
	return teamGraphDeltaFixture{object: object, permit: permit, write: baseTeamGraphDeltaWrite()}
}

func baseTeamGraphDeltaWrite() TeamGraphDeltaWrite {
	return TeamGraphDeltaWrite{
		Schema: TeamGraphDeltaSchema,
		Source: CapsuleSource{
			Host: "claude-code", ConversationScope: "current_turn",
			Timestamp: "2026-07-11T12:00:00+07:00",
		},
		Nodes: []TeamGraphNode{
			{
				ClientID: "person:alex", Kind: "person", CanonicalName: " Alex ",
				Summary:  graphString(" Works on the Pulse pilot. "),
				Aliases:  []string{" Alexander ", "Alexey"},
				Salience: graphFloat(0.8), EmotionalWeight: graphFloat(0.2), Domain: "real",
			},
			{
				ClientID: "project:pulse", Kind: "project", CanonicalName: "Pulse",
				Aliases: []string{}, Salience: graphFloat(0.9),
				EmotionalWeight: graphFloat(0), Domain: "real",
			},
		},
		Edges: []TeamGraphEdge{{
			From: "person:alex", To: "project:pulse", Kind: "works_on",
			Summary: graphString(" Alex contributes to Pulse. "), Strength: graphFloat(0.9),
		}},
		Facts: []TeamGraphFact{{
			Node: "person:alex", Text: " Alex is based in Lisbon. ",
			Predicate: graphString("home_base"), ObjectText: graphString(" Lisbon "),
			ValidFrom: graphString("2026-07-01T07:00:00+07:00"),
			ChangeCue: graphBool(true), SourceEventRefs: []string{"event:moved"},
			Confidence: graphFloat(0.9), Domain: "real",
		}},
		Events: []TeamGraphEvent{{
			ClientID: "event:moved", Title: " Alex moved ",
			Summary:    " Alex changed home base to Lisbon. ",
			EntityRefs: []string{"person:alex"}, Sentiment: graphString(" restoration "),
			EmotionalWeight: graphFloat(0.3), Confidence: graphFloat(0.9), Domain: "real",
			OccurredAt: graphString("2026-07-01T07:00:00+07:00"), Anchor: graphBool(false),
			Biometrics: &TeamGraphBiometrics{
				HRV: graphFloat(58), SleepQuality: graphFloat(0.8),
				StressProxy: graphFloat(0.2), HRTrend: graphString("stable"),
				HRVTrend: graphString("rising"), Workout: graphBool(true),
			},
			Emotions: map[string]*float64{"joy": graphFloat(0.3), "trust": graphFloat(0.6)},
		}},
		Continuity: &TeamGraphContinuity{
			ThreadID: "pulse-pilot", SessionID: "session-2026-07-11",
			Summary:     " Stopped after agreeing on scoped team storage. ",
			Decisions:   []string{" Use a dedicated team store. "},
			OpenLoops:   []string{" Wire the team graph gateway. "},
			DoNotRepeat: []string{}, EmotionalAnchors: []string{}, StateSignals: []string{},
			ActiveThreads: []string{"U10"}, ReviewInsights: []string{},
		},
		RawInputIncluded: false,
		ActiveContext: TeamGraphActiveContext{
			ProjectID: "project-pulse", RepoID: "repo-pulse",
			AgentID: "agent-bound", SessionID: "session-2026-07-11",
		},
		TargetScope: &TeamGraphTarget{Type: teamauth.ScopeProject, ID: "project-pulse"},
		PrivacyTier: "normal", Retention: "project",
		ExpiresAt:      graphString("2026-08-01T07:00:00+07:00"),
		IdempotencyKey: "graph-request-001",
	}
}

func storeFixtureTeamGraph(t *testing.T, fixture teamGraphDeltaFixture, write TeamGraphDeltaWrite) TeamObjectWriteResult {
	t.Helper()
	result, err := fixture.object.store.StoreTeamGraphDelta(
		context.Background(), fixture.permit, fixture.object.request.Writer,
		"request-team-graph-0001", fixture.object.actor.clientKey, write,
	)
	if err != nil {
		t.Fatalf("StoreTeamGraphDelta: %v", err)
	}
	return result
}

func TestNormalizeTeamGraphDeltaMatchesGatewayCanonicalEnvelopeAndStoreBody(t *testing.T) {
	fixture := newTeamGraphDeltaFixture(t)
	defer fixture.object.store.Close()
	original, err := marshalTeamGraphCanonical(fixture.write)
	if err != nil {
		t.Fatal(err)
	}

	normalized, err := normalizeTeamGraphDeltaWrite(fixture.permit, fixture.write)
	if err != nil {
		t.Fatal(err)
	}
	after, err := marshalTeamGraphCanonical(fixture.write)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, original) {
		t.Fatal("normalizer mutated caller-owned graph input")
	}
	if got, want := normalized.projectionKinds, []string{"claim", "continuity", "embedding", "graph"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("projection kinds = %v, want %v", got, want)
	}
	if strings.Contains(string(normalized.canonicalBody), "idempotency_key") ||
		strings.Contains(string(normalized.canonicalBody), fixture.write.IdempotencyKey) {
		t.Fatal("store content body persisted the raw idempotency key")
	}
	if !strings.Contains(string(normalized.canonicalWire), `"idempotency_key":"graph-request-001"`) {
		t.Fatal("gateway parity envelope omitted the idempotency key")
	}
	if !strings.Contains(string(normalized.canonicalWire),
		`"timestamp":"2026-07-11T05:00:00.000Z"`) ||
		!strings.Contains(string(normalized.canonicalWire),
			`"aliases":["Alexander","Alexey"]`) {
		t.Fatalf("canonical envelope did not match gateway defaults: %s", normalized.canonicalWire)
	}

	// These two hashes deliberately cover different byte strings. The gateway
	// signs the exact clean envelope including idempotency_key; the store hashes
	// the content body after removing that separately domain-hashed key.
	wireSHA := sha256.Sum256(normalized.canonicalWire)
	bodySHA := sha256.Sum256(normalized.canonicalBody)
	if got := hex.EncodeToString(wireSHA[:]); got != teamGraphGatewayCanonicalSHA256 {
		t.Fatalf("gateway canonical SHA = %s, want golden %s\ncanonical=%s",
			got, teamGraphGatewayCanonicalSHA256, normalized.canonicalWire)
	}
	if got := hex.EncodeToString(bodySHA[:]); got != teamGraphStoreBodySHA256 {
		t.Fatalf("store body SHA = %s, want golden %s\nbody=%s",
			got, teamGraphStoreBodySHA256, normalized.canonicalBody)
	}
	if normalized.bodyDigest != hex.EncodeToString(bodySHA[:]) {
		t.Fatalf("body digest = %q, want %x", normalized.bodyDigest, bodySHA)
	}
}

func TestNormalizeTeamGraphDeltaCanonicalizesNegativeZeroLikeJSONToPositiveZero(t *testing.T) {
	fixture := newTeamGraphDeltaFixture(t)
	defer fixture.object.store.Close()
	negative := baseTeamGraphDeltaWrite()
	positive := baseTeamGraphDeltaWrite()
	minusZero := math.Copysign(0, -1)
	for _, write := range []*TeamGraphDeltaWrite{&negative, &positive} {
		zero := 0.0
		if write == &negative {
			zero = minusZero
		}
		write.Nodes[0].Salience = graphFloat(zero)
		write.Nodes[0].EmotionalWeight = graphFloat(zero)
		write.Edges[0].Strength = graphFloat(zero)
		write.Facts[0].Confidence = graphFloat(zero)
		write.Events[0].EmotionalWeight = graphFloat(zero)
		write.Events[0].Confidence = graphFloat(zero)
		write.Events[0].Biometrics.HRV = graphFloat(zero)
		write.Events[0].Biometrics.SleepQuality = graphFloat(zero)
		write.Events[0].Biometrics.StressProxy = graphFloat(zero)
		write.Events[0].Emotions["joy"] = graphFloat(zero)
	}
	negativeNormalized, err := normalizeTeamGraphDeltaWrite(fixture.permit, negative)
	if err != nil {
		t.Fatal(err)
	}
	positiveNormalized, err := normalizeTeamGraphDeltaWrite(fixture.permit, positive)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(negativeNormalized.canonicalWire, positiveNormalized.canonicalWire) ||
		strings.Contains(string(negativeNormalized.canonicalWire), ":-0") ||
		strings.Contains(string(negativeNormalized.canonicalWire), ",-0") {
		t.Fatalf("negative zero canonical mismatch:\nnegative=%s\npositive=%s",
			negativeNormalized.canonicalWire, positiveNormalized.canonicalWire)
	}
}

func TestStoreTeamGraphDeltaCommitsAtomicRootInputIntentsAuditJobsAndReplay(t *testing.T) {
	ctx := context.Background()
	fixture := newTeamGraphDeltaFixture(t)
	defer fixture.object.store.Close()
	legacyBefore := teamGraphLegacyCounts(t, fixture.object.store)

	result := storeFixtureTeamGraph(t, fixture, fixture.write)
	if result.ObjectID == "" || result.AuditEventID == "" || result.Replayed ||
		result.Status != TeamObjectStatusStored || result.ProjectionState != TeamProjectionStatePending ||
		result.FullyProjected {
		t.Fatalf("unsafe graph result: %+v", result)
	}
	var kinds []string
	for _, job := range result.ProjectionJobs {
		kinds = append(kinds, job.Kind)
		if job.JobID == "" || job.State != TeamProjectionStatePending {
			t.Fatalf("invalid graph projection job: %+v", job)
		}
	}
	if want := []string{"claim", "continuity", "embedding", "graph"}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("jobs = %v, want %v", kinds, want)
	}

	var objectKind, scopeType, scopeID string
	if err := fixture.object.store.DB().QueryRowContext(ctx, `
		SELECT object_kind, scope_type, scope_id
		  FROM team_object_registry WHERE object_id = ?`, result.ObjectID).
		Scan(&objectKind, &scopeType, &scopeID); err != nil {
		t.Fatal(err)
	}
	if objectKind != "graph_delta" || scopeType != "project" || scopeID != "project-pulse" {
		t.Fatalf("graph root = kind=%q scope=%q/%q", objectKind, scopeType, scopeID)
	}

	var canonicalJSON, contentDigest, sourceTimestamp string
	if err := fixture.object.store.DB().QueryRowContext(ctx, `
		SELECT canonical_json, content_digest, source_timestamp
		  FROM team_graph_delta_inputs WHERE root_object_id = ?`, result.ObjectID).
		Scan(&canonicalJSON, &contentDigest, &sourceTimestamp); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(canonicalJSON, "idempotency_key") ||
		strings.Contains(canonicalJSON, fixture.write.IdempotencyKey) ||
		sourceTimestamp != "2026-07-11T05:00:00.000Z" || !lowerHexDigest(contentDigest) {
		t.Fatalf("unsafe graph input row: timestamp=%q digest=%q json=%s", sourceTimestamp, contentDigest, canonicalJSON)
	}
	computed := sha256.Sum256([]byte(canonicalJSON))
	if contentDigest != hex.EncodeToString(computed[:]) {
		t.Fatalf("content digest = %q, want %x", contentDigest, computed)
	}

	var intents, distinctIntentIDs, distinctDerivativeIDs int
	if err := fixture.object.store.DB().QueryRowContext(ctx, `
		SELECT count(*), count(DISTINCT intent_id), count(DISTINCT derivative_object_id)
		  FROM team_semantic_projection_intents WHERE root_object_id = ?`, result.ObjectID).
		Scan(&intents, &distinctIntentIDs, &distinctDerivativeIDs); err != nil {
		t.Fatal(err)
	}
	if intents != 12 || distinctIntentIDs != 12 || distinctDerivativeIDs != 12 {
		t.Fatalf("intent rows = total=%d ids=%d derivatives=%d", intents, distinctIntentIDs, distinctDerivativeIDs)
	}

	var auditMetadata, idempotencyHash string
	if err := fixture.object.store.DB().QueryRowContext(ctx,
		`SELECT metadata_json FROM team_audit_events WHERE event_id = ?`, result.AuditEventID).
		Scan(&auditMetadata); err != nil {
		t.Fatal(err)
	}
	if err := fixture.object.store.DB().QueryRowContext(ctx, `
		SELECT idempotency_key_hash FROM team_idempotency_records WHERE object_id = ?`, result.ObjectID).
		Scan(&idempotencyHash); err != nil {
		t.Fatal(err)
	}
	if auditMetadata != "{}" || !lowerHexDigest(idempotencyHash) ||
		idempotencyHash == fixture.write.IdempotencyKey {
		t.Fatalf("content leaked into audit/idempotency: metadata=%q key=%q", auditMetadata, idempotencyHash)
	}
	if after := teamGraphLegacyCounts(t, fixture.object.store); !reflect.DeepEqual(after, legacyBefore) {
		t.Fatalf("team graph ingress changed legacy content tables: before=%v after=%v", legacyBefore, after)
	}

	replay := storeFixtureTeamGraph(t, fixture, fixture.write)
	if !replay.Replayed {
		t.Fatalf("graph retry was not replayed: %+v", replay)
	}
	replay.Replayed = result.Replayed
	requireSameTeamObjectWriteIDs(t, replay, result)
	for table, want := range map[string]int{
		"team_object_registry": 1, "team_graph_delta_inputs": 1,
		"team_semantic_projection_intents": 12, "team_projection_jobs": 4,
	} {
		var count int
		query := `SELECT count(*) FROM ` + table
		if table == "team_object_registry" || table == "team_graph_delta_inputs" || table == "team_projection_jobs" || table == "team_semantic_projection_intents" {
			column := "root_object_id"
			if table == "team_object_registry" {
				column = "object_id"
			}
			query += ` WHERE ` + column + ` = ?`
		}
		if err := fixture.object.store.DB().QueryRowContext(ctx, query, result.ObjectID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s rows = %d, want %d", table, count, want)
		}
	}
}

func TestStoreTeamGraphDeltaRollbackInjectionLeavesNoPartialRows(t *testing.T) {
	fixture := newTeamGraphDeltaFixture(t)
	defer fixture.object.store.Close()
	before := teamGraphAtomicCounts(t, fixture.object.store)
	if _, err := fixture.object.store.DB().Exec(`
		CREATE TRIGGER reject_team_graph_fact_intent
		BEFORE INSERT ON team_semantic_projection_intents
		WHEN NEW.source_kind = 'fact'
		BEGIN SELECT RAISE(ABORT, 'synthetic graph intent failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.object.store.StoreTeamGraphDelta(
		context.Background(), fixture.permit, fixture.object.request.Writer,
		"request-team-graph-rollback", fixture.object.actor.clientKey, fixture.write,
	)
	if !errors.Is(err, ErrTeamObjectCommitFailed) {
		t.Fatalf("rollback injection error = %v, want %v", err, ErrTeamObjectCommitFailed)
	}
	after := teamGraphAtomicCounts(t, fixture.object.store)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("partial graph rows survived rollback: before=%v after=%v", before, after)
	}
}

func TestTeamGraphInputDDLRejectsMissingCanonicalFieldsInsteadOfAcceptingCheckNull(t *testing.T) {
	fixture := newTeamGraphDeltaFixture(t)
	defer fixture.object.store.Close()
	result := storeFixtureTeamGraph(t, fixture, fixture.write)
	if _, err := fixture.object.store.DB().Exec(
		`DELETE FROM team_graph_delta_inputs WHERE root_object_id = ?`, result.ObjectID,
	); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.object.store.DB().Exec(`
		INSERT INTO team_graph_delta_inputs(
			root_object_id, store_id, team_id, scope_type, scope_id,
			root_generation, schema_version, source_host, conversation_scope,
			source_timestamp, canonical_json, content_digest, created_at)
		SELECT object_id, store_id, team_id, scope_type, scope_id,
			generation, 'pulse.team.graph_delta.v1', 'claude-code', 'current_turn',
			'2026-07-11T05:00:00.000Z', '{}', ?, '2026-07-11T05:00:00.000Z'
		  FROM team_object_registry WHERE object_id = ?`,
		strings.Repeat("a", 64), result.ObjectID,
	)
	if err == nil {
		t.Fatal("canonical_json={} passed the migration 036 DDL contract")
	}
}

func TestPolicyReadinessRejectsBrokenTeamGraphIngress(t *testing.T) {
	for _, test := range []struct {
		name    string
		corrupt func(*testing.T, *Store, string)
	}{
		{name: "missing canonical input", corrupt: func(t *testing.T, store *Store, rootID string) {
			if _, err := store.DB().Exec(`DELETE FROM team_graph_delta_inputs WHERE root_object_id = ?`, rootID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "projection job without intent", corrupt: func(t *testing.T, store *Store, rootID string) {
			if _, err := store.DB().Exec(`
				DELETE FROM team_semantic_projection_intents
				 WHERE root_object_id = ? AND projection_kind = 'continuity'`, rootID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "partial intent deletion", corrupt: func(t *testing.T, store *Store, rootID string) {
			if _, err := store.DB().Exec(`
				DELETE FROM team_semantic_projection_intents
				 WHERE root_object_id = ? AND projection_kind = 'graph'
				   AND source_kind = 'node' AND source_ordinal = 0`, rootID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "phantom intent", corrupt: func(t *testing.T, store *Store, rootID string) {
			if _, err := store.DB().Exec(`
				INSERT INTO team_semantic_projection_intents(
					intent_id, root_object_id, store_id, team_id, scope_type, scope_id,
					root_generation, projection_kind, source_kind, source_ordinal,
					derivative_object_id, derivative_kind, semantic_key_digest,
					policy_digest, payload_digest, created_at)
				SELECT 'semantic_intent_phantom', root_object_id, store_id, team_id,
					scope_type, scope_id, root_generation, 'graph', 'node', 29,
					'semantic_object_phantom', 'graph_entity', ?, ?, ?,
					'2026-07-11T05:00:00.000Z'
				  FROM team_graph_delta_inputs WHERE root_object_id = ?`,
				strings.Repeat("a", 64), strings.Repeat("b", 64),
				strings.Repeat("c", 64), rootID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "intent payload digest drift", corrupt: func(t *testing.T, store *Store, rootID string) {
			if _, err := store.DB().Exec(`DROP TRIGGER team_semantic_projection_intents_immutable`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.DB().Exec(`
				UPDATE team_semantic_projection_intents SET payload_digest = ?
				 WHERE root_object_id = ? AND projection_kind = 'graph'
				   AND source_kind = 'node' AND source_ordinal = 0`,
				strings.Repeat("d", 64), rootID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "canonical content digest drift", corrupt: func(t *testing.T, store *Store, rootID string) {
			if _, err := store.DB().Exec(`DROP TRIGGER team_graph_delta_inputs_immutable`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.DB().Exec(`
				UPDATE team_graph_delta_inputs SET content_digest = ? WHERE root_object_id = ?`,
				strings.Repeat("e", 64), rootID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTeamGraphDeltaFixture(t)
			defer fixture.object.store.Close()
			result := storeFixtureTeamGraph(t, fixture, fixture.write)
			test.corrupt(t, fixture.object.store, result.ObjectID)
			if err := fixture.object.store.AuditTeamSemanticIntegrity(
				context.Background(),
			); !errors.Is(err, ErrTeamPolicyNotReady) {
				t.Fatalf("semantic audit error = %v, want %v", err, ErrTeamPolicyNotReady)
			}
		})
	}
}

func TestStoreTeamGraphDeltaReplaySurvivesResponseLossAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "team.db")
	fixture := newTeamGraphDeltaFixtureAt(t, path)
	result := storeFixtureTeamGraph(t, fixture, fixture.write)
	if err := fixture.object.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenTeam(path, reviewTeamOptions(testBootstrapRoot()))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replay, err := reopened.StoreTeamGraphDelta(
		context.Background(), fixture.permit, fixture.object.request.Writer,
		"request-team-graph-restart", fixture.object.actor.clientKey, fixture.write,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed {
		t.Fatalf("restart did not return durable graph replay: %+v", replay)
	}
	replay.Replayed = result.Replayed
	requireSameTeamObjectWriteIDs(t, replay, result)
}

func TestTeamGraphDerivativeIDsShareWithinScopeAndIsolateAcrossScopes(t *testing.T) {
	ctx := context.Background()
	fixture := newTeamGraphDeltaFixture(t)
	defer fixture.object.store.Close()
	first := storeFixtureTeamGraph(t, fixture, fixture.write)

	sameContent := baseTeamGraphDeltaWrite()
	sameContent.IdempotencyKey = "graph-request-002"
	second := storeFixtureTeamGraph(t, fixture, sameContent)
	firstIDs := teamGraphDerivativeIDs(t, fixture.object.store, first.ObjectID)
	secondIDs := teamGraphDerivativeIDs(t, fixture.object.store, second.ObjectID)
	if !reflect.DeepEqual(firstIDs, secondIDs) {
		t.Fatalf("same scoped semantic content did not share derivatives:\nfirst=%v\nsecond=%v", firstIDs, secondIDs)
	}

	conflictingClaim := baseTeamGraphDeltaWrite()
	conflictingClaim.IdempotencyKey = "graph-request-003"
	conflictingClaim.Facts[0].Text = "Alex is based in Porto."
	conflictingClaim.Facts[0].ObjectText = graphString("Porto")
	conflictingClaim.Facts[0].Predicate = graphString("ＨＯＭＥ＿ＢＡＳＥ")
	third := storeFixtureTeamGraph(t, fixture, conflictingClaim)
	thirdIDs := teamGraphDerivativeIDs(t, fixture.object.store, third.ObjectID)
	if firstIDs["claim/fact/0"] != thirdIDs["claim/fact/0"] {
		t.Fatalf("competing claim values split assertion derivative: first=%q third=%q",
			firstIDs["claim/fact/0"], thirdIDs["claim/fact/0"])
	}
	if firstIDs["graph/fact/0"] == thirdIDs["graph/fact/0"] {
		t.Fatal("different fact payloads incorrectly shared the graph-fact derivative")
	}
	caseVariantClaim := baseTeamGraphDeltaWrite()
	caseVariantClaim.IdempotencyKey = "graph-request-claim-case"
	caseVariantClaim.Facts[0].Predicate = graphString("ＨＯＭＥ＿ＢＡＳＥ")
	caseVariant := storeFixtureTeamGraph(t, fixture, caseVariantClaim)
	caseVariantIDs := teamGraphDerivativeIDs(t, fixture.object.store, caseVariant.ObjectID)
	if firstIDs["claim/fact/0"] != caseVariantIDs["claim/fact/0"] {
		t.Fatalf("NFKC/case predicate variant split assertion derivative: first=%q variant=%q",
			firstIDs["claim/fact/0"], caseVariantIDs["claim/fact/0"])
	}

	changedCheckpoint := baseTeamGraphDeltaWrite()
	changedCheckpoint.IdempotencyKey = "graph-request-004"
	changedCheckpoint.Continuity.Summary = "Now implementing the scoped team graph writer."
	fourth := storeFixtureTeamGraph(t, fixture, changedCheckpoint)
	fourthIDs := teamGraphDerivativeIDs(t, fixture.object.store, fourth.ObjectID)
	if firstIDs["continuity/continuity/0"] != fourthIDs["continuity/continuity/0"] {
		t.Fatal("successive checkpoints for one thread/session split the continuity derivative")
	}

	personalAuthorization := mutationWriteRequest(fixture.object.bootstrap, fixture.object.actor)
	personalAuthorization.ObjectKind = "graph_delta"
	personalAuthorization.Context = teamauth.ActiveContext{
		TeamID: fixture.object.bootstrap.TeamID, SessionID: "session-2026-07-11",
	}
	personalPermit, err := fixture.object.store.AuthorizeTeamMutation(ctx, personalAuthorization)
	if err != nil {
		t.Fatal(err)
	}
	personalWrite := baseTeamGraphDeltaWrite()
	personalWrite.ActiveContext = TeamGraphActiveContext{SessionID: "session-2026-07-11"}
	personalWrite.TargetScope = nil
	personalWrite.IdempotencyKey = "graph-request-005"
	personalResult, err := fixture.object.store.StoreTeamGraphDelta(
		ctx, personalPermit, fixture.object.request.Writer, "request-team-graph-personal",
		fixture.object.actor.clientKey, personalWrite,
	)
	if err != nil {
		t.Fatal(err)
	}
	personalIDs := teamGraphDerivativeIDs(t, fixture.object.store, personalResult.ObjectID)
	projectSet := make(map[string]struct{}, len(firstIDs))
	for _, id := range firstIDs {
		projectSet[id] = struct{}{}
	}
	for key, id := range personalIDs {
		if _, leaked := projectSet[id]; leaked {
			t.Fatalf("cross-scope derivative collision for %s: %s", key, id)
		}
	}
}

func TestTeamGraphPredicateDerivativeKeysMatchECMAScriptNFKCLower(t *testing.T) {
	tests := []struct {
		name       string
		first      string
		equivalent string
	}{
		{name: "dotted capital I", first: "İD", equivalent: "I\u0307D"},
		{name: "contextual final sigma", first: "ΟΣ", equivalent: "Ος"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTeamGraphDeltaFixture(t)
			defer fixture.object.store.Close()

			firstWrite := baseTeamGraphDeltaWrite()
			firstWrite.IdempotencyKey = "predicate-golden-first"
			firstWrite.Facts[0].Predicate = graphString(test.first)
			first := storeFixtureTeamGraph(t, fixture, firstWrite)

			equivalentWrite := baseTeamGraphDeltaWrite()
			equivalentWrite.IdempotencyKey = "predicate-golden-equivalent"
			equivalentWrite.Facts[0].Predicate = graphString(test.equivalent)
			equivalent := storeFixtureTeamGraph(t, fixture, equivalentWrite)

			firstIDs := teamGraphDerivativeIDs(t, fixture.object.store, first.ObjectID)
			equivalentIDs := teamGraphDerivativeIDs(t, fixture.object.store, equivalent.ObjectID)
			if firstIDs["claim/fact/0"] != equivalentIDs["claim/fact/0"] {
				t.Fatalf("ECMAScript-equivalent predicates split assertion derivative: first=%q equivalent=%q",
					firstIDs["claim/fact/0"], equivalentIDs["claim/fact/0"])
			}
		})
	}
}

func TestTeamGraphEmbeddingDerivativeIgnoresRootLocalReferenceNames(t *testing.T) {
	fixture := newTeamGraphDeltaFixture(t)
	defer fixture.object.store.Close()
	first := storeFixtureTeamGraph(t, fixture, fixture.write)
	renamed := baseTeamGraphDeltaWrite()
	renamed.IdempotencyKey = "graph-request-renamed"
	renamed.Nodes[0].ClientID = "person:local-renamed"
	renamed.Edges[0].From = "person:local-renamed"
	renamed.Facts[0].Node = "person:local-renamed"
	renamed.Events[0].EntityRefs = []string{"person:local-renamed"}
	renamed.Events[0].ClientID = "event:local-renamed"
	renamed.Facts[0].SourceEventRefs = []string{"event:local-renamed"}
	second := storeFixtureTeamGraph(t, fixture, renamed)
	firstIDs := teamGraphDerivativeIDs(t, fixture.object.store, first.ObjectID)
	secondIDs := teamGraphDerivativeIDs(t, fixture.object.store, second.ObjectID)
	for key, firstID := range firstIDs {
		if secondIDs[key] != firstID {
			t.Fatalf("root-local ref rename changed %s derivative: first=%q second=%q",
				key, firstID, secondIDs[key])
		}
	}
}

func TestDefaultSessionExpiryRootsDoNotShareDerivativesOrLeakKeys(t *testing.T) {
	fixture := newTeamGraphDeltaFixture(t)
	defer fixture.object.store.Close()
	firstWrite := baseTeamGraphDeltaWrite()
	firstWrite.Retention = "session"
	firstWrite.ExpiresAt = nil
	firstWrite.IdempotencyKey = "default-expiry-key-001"
	secondWrite := baseTeamGraphDeltaWrite()
	secondWrite.Retention = "session"
	secondWrite.ExpiresAt = nil
	secondWrite.IdempotencyKey = "default-expiry-key-002"
	first := storeFixtureTeamGraph(t, fixture, firstWrite)
	second := storeFixtureTeamGraph(t, fixture, secondWrite)
	firstIDs := teamGraphDerivativeIDs(t, fixture.object.store, first.ObjectID)
	secondIDs := teamGraphDerivativeIDs(t, fixture.object.store, second.ObjectID)
	for key, firstID := range firstIDs {
		if firstID == secondIDs[key] {
			t.Fatalf("default-expiry roots shared %s derivative %q", key, firstID)
		}
	}
	for _, rootID := range []string{first.ObjectID, second.ObjectID} {
		var canonicalJSON string
		if err := fixture.object.store.DB().QueryRow(`
			SELECT canonical_json FROM team_graph_delta_inputs WHERE root_object_id = ?`, rootID).
			Scan(&canonicalJSON); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(canonicalJSON, "default-expiry-key-") ||
			strings.Contains(canonicalJSON, "idempotency_key") {
			t.Fatalf("default expiry input leaked raw key: %s", canonicalJSON)
		}
	}
}

func TestStoreTeamGraphDeltaCreatesOnlyConditionalSortedJobs(t *testing.T) {
	fixture := newTeamGraphDeltaFixture(t)
	defer fixture.object.store.Close()
	tests := []struct {
		name  string
		write TeamGraphDeltaWrite
		want  []string
	}{
		{name: "continuity only", write: func() TeamGraphDeltaWrite {
			write := baseTeamGraphDeltaWrite()
			write.IdempotencyKey = "graph-jobs-continuity"
			write.Nodes = []TeamGraphNode{}
			write.Edges = []TeamGraphEdge{}
			write.Facts = []TeamGraphFact{}
			write.Events = []TeamGraphEvent{}
			return write
		}(), want: []string{"continuity"}},
		{name: "unstructured graph", write: func() TeamGraphDeltaWrite {
			write := baseTeamGraphDeltaWrite()
			write.IdempotencyKey = "graph-jobs-unstructured"
			write.Edges = []TeamGraphEdge{}
			write.Events = []TeamGraphEvent{}
			write.Continuity = nil
			write.Facts[0].Predicate = nil
			write.Facts[0].ObjectText = nil
			write.Facts[0].ValidFrom = nil
			write.Facts[0].ChangeCue = nil
			write.Facts[0].SourceEventRefs = nil
			return write
		}(), want: []string{"embedding", "graph"}},
		{name: "structured graph", write: func() TeamGraphDeltaWrite {
			write := baseTeamGraphDeltaWrite()
			write.IdempotencyKey = "graph-jobs-structured"
			write.Continuity = nil
			return write
		}(), want: []string{"claim", "embedding", "graph"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := storeFixtureTeamGraph(t, fixture, test.write)
			got := make([]string, 0, len(result.ProjectionJobs))
			for _, job := range result.ProjectionJobs {
				got = append(got, job.Kind)
			}
			if !reflect.DeepEqual(got, test.want) || !sort.StringsAreSorted(got) {
				t.Fatalf("jobs = %v, want sorted %v", got, test.want)
			}
		})
	}
}

func TestNormalizeTeamGraphDeltaRejectsUnsafeReferencesClaimsAndContinuity(t *testing.T) {
	fixture := newTeamGraphDeltaFixture(t)
	defer fixture.object.store.Close()
	tests := []struct {
		name   string
		mutate func(*TeamGraphDeltaWrite)
	}{
		{name: "raw input", mutate: func(write *TeamGraphDeltaWrite) { write.RawInputIncluded = true }},
		{name: "missing required array", mutate: func(write *TeamGraphDeltaWrite) { write.Nodes = nil }},
		{name: "unknown edge node", mutate: func(write *TeamGraphDeltaWrite) { write.Edges[0].From = "person:missing" }},
		{name: "duplicate node ref", mutate: func(write *TeamGraphDeltaWrite) { write.Nodes = append(write.Nodes, write.Nodes[0]) }},
		{name: "nfkc duplicate node", mutate: func(write *TeamGraphDeltaWrite) {
			write.Nodes = append(write.Nodes, TeamGraphNode{
				ClientID: "person:compat", Kind: "person", CanonicalName: "Ａｌｅｘ", Domain: "real",
			})
		}},
		{name: "ecmascript dotted I duplicate node", mutate: func(write *TeamGraphDeltaWrite) {
			write.Nodes[0].CanonicalName = "İ"
			write.Nodes = append(write.Nodes, TeamGraphNode{
				ClientID: "person:dotted-i", Kind: "person", CanonicalName: "I\u0307", Domain: "real",
			})
		}},
		{name: "ecmascript final sigma duplicate node", mutate: func(write *TeamGraphDeltaWrite) {
			write.Nodes[0].CanonicalName = "ΟΣ"
			write.Nodes = append(write.Nodes, TeamGraphNode{
				ClientID: "person:final-sigma", Kind: "person", CanonicalName: "Ος", Domain: "real",
			})
		}},
		{name: "missing fact confidence", mutate: func(write *TeamGraphDeltaWrite) { write.Facts[0].Confidence = nil }},
		{name: "missing event confidence", mutate: func(write *TeamGraphDeltaWrite) { write.Events[0].Confidence = nil }},
		{name: "half claim", mutate: func(write *TeamGraphDeltaWrite) { write.Facts[0].ObjectText = nil }},
		{name: "claim metadata without claim", mutate: func(write *TeamGraphDeltaWrite) {
			write.Facts[0].Predicate = nil
			write.Facts[0].ObjectText = nil
		}},
		{name: "unknown source event", mutate: func(write *TeamGraphDeltaWrite) {
			write.Facts[0].SourceEventRefs = []string{"event:missing"}
		}},
		{name: "continuity active mismatch", mutate: func(write *TeamGraphDeltaWrite) {
			write.Continuity.SessionID = "session-other"
		}},
		{name: "continuity target mismatch", mutate: func(write *TeamGraphDeltaWrite) {
			write.TargetScope = &TeamGraphTarget{Type: teamauth.ScopeSession, ID: "session-other"}
		}},
		{name: "unsafe path", mutate: func(write *TeamGraphDeltaWrite) {
			write.Facts[0].Text = "Read /Users/example/private/notes.txt."
		}},
		{name: "unsafe secret", mutate: func(write *TeamGraphDeltaWrite) {
			write.Events[0].Summary = "Bearer token=sk-ABCDEF0123456789 was copied."
		}},
		{name: "transcript", mutate: func(write *TeamGraphDeltaWrite) {
			write.Continuity.Summary = "user: one\nassistant: two\nuser: three\nassistant: four\nuser: five\nassistant: six"
		}},
		{name: "line separator parity", mutate: func(write *TeamGraphDeltaWrite) {
			write.Events[0].Summary = "unsafe\u2028separator"
		}},
		{name: "paragraph separator parity", mutate: func(write *TeamGraphDeltaWrite) {
			write.Continuity.Summary = "unsafe\u2029separator"
		}},
		{name: "biometric range", mutate: func(write *TeamGraphDeltaWrite) {
			write.Events[0].Biometrics.HRV = graphFloat(301)
		}},
		{name: "unknown emotion", mutate: func(write *TeamGraphDeltaWrite) {
			write.Events[0].Emotions["hope"] = graphFloat(0.5)
		}},
		{name: "unsafe idempotency", mutate: func(write *TeamGraphDeltaWrite) {
			write.IdempotencyKey = "secret=unsafe"
		}},
		{name: "canonical body over 256 KiB", mutate: func(write *TeamGraphDeltaWrite) {
			large := strings.Repeat("🫧", 1200)
			values := make([]string, 20)
			for index := range values {
				values[index] = large
			}
			write.Continuity.Decisions = append([]string(nil), values...)
			write.Continuity.OpenLoops = append([]string(nil), values...)
			write.Continuity.DoNotRepeat = append([]string(nil), values...)
			write.Continuity.EmotionalAnchors = append([]string(nil), values...)
			write.Continuity.StateSignals = append([]string(nil), values...)
			write.Continuity.ActiveThreads = append([]string(nil), values...)
			write.Continuity.ReviewInsights = append([]string(nil), values...)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			write := baseTeamGraphDeltaWrite()
			test.mutate(&write)
			if _, err := normalizeTeamGraphDeltaWrite(fixture.permit, write); !errors.Is(err, ErrTeamGraphDeltaInvalid) {
				t.Fatalf("normalize error = %v, want %v", err, ErrTeamGraphDeltaInvalid)
			}
		})
	}
}

func teamGraphAtomicCounts(t *testing.T, store *Store) map[string]int {
	t.Helper()
	tables := []string{
		"team_object_registry", "team_graph_delta_inputs", "team_semantic_projection_intents",
		"team_object_storage_map", "team_audit_events", "team_idempotency_records",
		"team_projection_jobs",
	}
	counts := make(map[string]int, len(tables))
	for _, table := range tables {
		var count int
		if err := store.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		counts[table] = count
	}
	return counts
}

func teamGraphDerivativeIDs(t *testing.T, store *Store, rootID string) map[string]string {
	t.Helper()
	rows, err := store.DB().Query(`
		SELECT projection_kind, source_kind, source_ordinal, derivative_object_id
		  FROM team_semantic_projection_intents
		 WHERE root_object_id = ?
		 ORDER BY projection_kind, source_kind, source_ordinal`, rootID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var projectionKind, sourceKind, derivativeID string
		var ordinal int
		if err := rows.Scan(&projectionKind, &sourceKind, &ordinal, &derivativeID); err != nil {
			t.Fatal(err)
		}
		result[projectionKind+"/"+sourceKind+"/"+strconv.Itoa(ordinal)] = derivativeID
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func teamGraphLegacyCounts(t *testing.T, store *Store) map[string]int {
	t.Helper()
	tables := []string{
		"entities", "relations", "facts", "events", "assertions",
		"continuity_threads", "continuity_sessions", "continuity_observations",
		"continuity_checkpoints", "memory_capsules",
	}
	counts := make(map[string]int, len(tables))
	for _, table := range tables {
		var count int
		if err := store.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = count
	}
	return counts
}
