package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamauth"
	"github.com/nkkmnk/pulse/internal/teamread"
)

type fakeTeamReadApplication struct {
	recallAuthorization  []teamread.Authorization
	recallRequests       []teamread.RecallRequest
	contextAuthorization []teamread.Authorization
	contextRequests      []teamread.ContextRequest
	resumeAuthorization  []teamread.Authorization
	resumeRequests       []teamread.ResumeRequest
	recallResponse       retrieve.TeamRetrievalResponse
	contextResponse      retrieve.TeamContextResponse
	resumeResponse       retrieve.TeamResumeResponse
	err                  error
}

func (fake *fakeTeamReadApplication) Recall(
	_ context.Context,
	authorization teamread.Authorization,
	request teamread.RecallRequest,
) (retrieve.TeamRetrievalResponse, error) {
	fake.recallAuthorization = append(fake.recallAuthorization, authorization)
	fake.recallRequests = append(fake.recallRequests, request)
	return fake.recallResponse, fake.err
}

func (fake *fakeTeamReadApplication) Context(
	_ context.Context,
	authorization teamread.Authorization,
	request teamread.ContextRequest,
) (retrieve.TeamContextResponse, error) {
	fake.contextAuthorization = append(fake.contextAuthorization, authorization)
	fake.contextRequests = append(fake.contextRequests, request)
	return fake.contextResponse, fake.err
}

func (fake *fakeTeamReadApplication) Resume(
	_ context.Context,
	authorization teamread.Authorization,
	request teamread.ResumeRequest,
) (retrieve.TeamResumeResponse, error) {
	fake.resumeAuthorization = append(fake.resumeAuthorization, authorization)
	fake.resumeRequests = append(fake.resumeRequests, request)
	return fake.resumeResponse, fake.err
}

func TestTeamReadRoutesReturnExactAuthorizedOnlyShapes(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	fake := &fakeTeamReadApplication{
		recallResponse: retrieve.TeamRetrievalResponse{
			Items: []retrieve.TeamRetrievedItem{{
				RootObjectID: "root-memory", Kind: "decision",
				Text: "Filter before retrieval.", Confidence: 0.9,
				PrivacyTier: "normal", Retention: "project", Tags: []string{"pulse"},
			}}, ReturnedCount: 1,
		},
		contextResponse: retrieve.TeamContextResponse{
			Entities: []retrieve.TeamContextEntity{{
				RootObjectID: "root-entity", ObjectID: "entity-pulse",
				EntityKind: "project", Name: "Pulse", Summary: "Scoped team memory.",
				Confidence: 0.9, Score: 0.8,
			}},
			Facts: []retrieve.TeamContextFact{{
				RootObjectID: "root-fact", ObjectID: "fact-pulse", NodeObjectID: "entity-pulse",
				Text: "Authorization precedes candidate generation.", Confidence: 0.9,
				Domain: "real", Score: 0.95,
			}},
			Counts: retrieve.TeamContextCounts{Entities: 1, Facts: 1, Total: 2},
			Trace: []retrieve.TeamContextTrace{{
				RootObjectID: "root-fact", ObjectID: "fact-pulse", Kind: "fact",
				Lexical: 0.95, Score: 0.95,
			}},
		},
		resumeResponse: retrieve.TeamResumeResponse{
			WhereWeLeftOff: []retrieve.TeamResumeItem{{
				RootObjectID: "root-continuity", ObjectID: "continuity-pulse",
				Text: "Stopped after scoped retrieval.",
			}},
			SuggestedNextStep: []retrieve.TeamResumeItem{{
				RootObjectID: "root-continuity", ObjectID: "continuity-pulse",
				Text: "Wire the read routes.",
			}},
			ReturnedCount: 2,
		},
	}
	srv.cfg.ReadService = fake

	tests := []struct {
		name, jti, path string
		body            []byte
		wantSchema      string
		wantCount       int
	}{
		{
			name: "recall", jti: "read-recall", path: TeamRecallRoutePath,
			body:       []byte(`{"schema":"pulse.team.recall.v1","query":"authorization","active_context":{"project_id":"project-pulse"},"privacy_ceiling":"normal","limit":5}`),
			wantSchema: TeamRecallResultSchema, wantCount: 1,
		},
		{
			name: "context", jti: "read-context", path: TeamContextQueryRoutePath,
			body:       []byte(`{"schema":"pulse.team.context.v1","query":"authorization","active_context":{"project_id":"project-pulse"},"privacy_ceiling":"normal","limit":10,"include_trace":true,"graph_mode":"anchored"}`),
			wantSchema: TeamContextResultSchema, wantCount: 2,
		},
		{
			name: "resume", jti: "read-resume", path: TeamResumeRoutePath,
			body:       []byte(`{"schema":"pulse.team.resume.v1","active_context":{"project_id":"project-pulse"},"thread_id":"thread-pulse","limit":20}`),
			wantSchema: TeamResumeResultSchema, wantCount: 2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claims := fixture.claims(test.jti, "agent-client", test.body)
			claims["path"] = test.path
			assertion := signPrincipalAssertion(t, fixture.private, "active", nil, claims)
			response := serveTeamGraphRequest(
				srv.Handler(), assertion, "req-"+test.jti,
				bodyPathRequest{path: test.path, body: test.body},
			)
			if response.Code != http.StatusOK {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
			var value map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
				t.Fatal(err)
			}
			if value["schema"] != test.wantSchema || value["fallback"] != false {
				t.Fatalf("response shape = %v", value)
			}
			switch test.name {
			case "recall", "resume":
				if int(value["returned_count"].(float64)) != test.wantCount {
					t.Fatalf("returned count = %v", value)
				}
			case "context":
				counts := value["returned_counts"].(map[string]any)
				if int(counts["facts"].(float64)+counts["entities"].(float64)) != test.wantCount {
					t.Fatalf("returned counts = %v", value)
				}
			}
		})
	}

	if len(fake.recallAuthorization) != 1 || len(fake.contextAuthorization) != 1 ||
		len(fake.resumeAuthorization) != 1 {
		t.Fatalf("application calls = recall:%d context:%d resume:%d",
			len(fake.recallAuthorization), len(fake.contextAuthorization), len(fake.resumeAuthorization))
	}
	for _, authorization := range []teamread.Authorization{
		fake.recallAuthorization[0], fake.contextAuthorization[0], fake.resumeAuthorization[0],
	} {
		if authorization.PrincipalID != fixture.agentID || authorization.TeamID != fixture.teamID ||
			!reflect.DeepEqual(authorization.Capabilities, fixtureCapabilities()) {
			t.Fatalf("server-derived authorization = %+v", authorization)
		}
	}
}

func TestTeamReadRoutesFailClosedWithoutDispatchOrFallback(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	fake := &fakeTeamReadApplication{err: store.ErrPrincipalRevoked}
	srv.cfg.ReadService = fake
	body := []byte(`{"schema":"pulse.team.recall.v1","query":"authorization","active_context":{},"privacy_ceiling":"normal","limit":5}`)
	claims := fixture.claims("read-revoked", "agent-client", body)
	claims["path"] = TeamRecallRoutePath
	assertion := signPrincipalAssertion(t, fixture.private, "active", nil, claims)
	response := serveTeamGraphRequest(
		srv.Handler(), assertion, "req-read-revoked",
		bodyPathRequest{path: TeamRecallRoutePath, body: body},
	)
	if response.Code != http.StatusForbidden ||
		response.Body.String() != "{\"error\":\"principal_revoked\",\"fallback\":false}\n" {
		t.Fatalf("revoked response = %d %q", response.Code, response.Body.String())
	}

	invalidBody := []byte(`{"schema":"pulse.team.recall.v1","query":"authorization","active_context":{},"privacy_ceiling":"normal","principal_id":"spoofed"}`)
	invalidClaims := fixture.claims("read-invalid", "agent-client", invalidBody)
	invalidClaims["path"] = TeamRecallRoutePath
	invalidAssertion := signPrincipalAssertion(t, fixture.private, "active", nil, invalidClaims)
	invalid := serveTeamGraphRequest(
		srv.Handler(), invalidAssertion, "req-read-invalid",
		bodyPathRequest{path: TeamRecallRoutePath, body: invalidBody},
	)
	if invalid.Code != http.StatusBadRequest ||
		invalid.Body.String() != "{\"error\":\"invalid_team_recall\",\"fallback\":false}\n" {
		t.Fatalf("invalid response = %d %q", invalid.Code, invalid.Body.String())
	}
	if len(fake.recallRequests) != 1 || !errors.Is(fake.err, store.ErrPrincipalRevoked) {
		t.Fatalf("invalid request reached application: calls=%d", len(fake.recallRequests))
	}
}

func TestTeamRecallRealChainExcludesTextIdenticalHiddenRootBeforeRanking(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	member, err := fixture.store.AddTeamMember(context.Background(), store.AddTeamMemberRequest{
		ActorPrincipalID: fixture.ownerID, Issuer: fixture.issuer,
		Subject: "hidden-member", Role: "member",
	})
	if err != nil {
		t.Fatal(err)
	}
	insertServerTeamReadMemory(t, fixture, "root-visible-real", "capsule-visible-real",
		fixture.ownerID, 0.1)
	insertServerTeamReadMemory(t, fixture, "root-hidden-real", "capsule-hidden-real",
		member.PrincipalID, 1.0)
	srv.cfg.ReadService = teamread.New(
		fixture.store,
		retrieve.NewTeamRetrievalEngine(retrieve.TeamRetrievalConfig{CandidateLimit: 32}),
	)
	body := []byte(`{"schema":"pulse.team.recall.v1","query":"identical security decision","active_context":{},"privacy_ceiling":"normal","limit":5}`)
	serve := func(jti string) *httptest.ResponseRecorder {
		claims := fixture.claims(jti, "agent-client", body)
		claims["path"] = TeamRecallRoutePath
		assertion := signPrincipalAssertion(t, fixture.private, "active", nil, claims)
		return serveTeamGraphRequest(
			srv.Handler(), assertion, "req-"+jti,
			bodyPathRequest{path: TeamRecallRoutePath, body: body},
		)
	}
	response := serve("real-read-visible")
	if response.Code != http.StatusOK {
		t.Fatalf("real recall = %d %q", response.Code, response.Body.String())
	}
	var result teamRecallResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ReturnedCount != 1 || len(result.Items) != 1 ||
		result.Items[0].ObjectID != "root-visible-real" ||
		result.Items[0].RedactedSummary != "identical security decision" {
		t.Fatalf("hidden root influenced real recall: %+v", result)
	}

	if err := fixture.store.RevokeAgentBinding(
		context.Background(), fixture.ownerID, fixture.bindingID,
	); err != nil {
		t.Fatal(err)
	}
	revoked := serve("real-read-revoked")
	if revoked.Code != http.StatusForbidden ||
		revoked.Body.String() != "{\"error\":\"principal_revoked\",\"fallback\":false}\n" {
		t.Fatalf("revoked real read = %d %q", revoked.Code, revoked.Body.String())
	}
}

func insertServerTeamReadMemory(
	t *testing.T,
	fixture *principalFixture,
	rootObjectID, capsuleID, ownerPrincipalID string,
	confidence float64,
) {
	t.Helper()
	if _, err := fixture.store.DB().Exec(`
		INSERT INTO team_object_registry(
			object_id, store_id, team_id, object_kind, scope_type, scope_id,
			owner_principal_id, author_principal_id, privacy_tier, retention,
			lifecycle, generation, created_at, updated_at)
		VALUES (?, ?, ?, 'memory', 'personal', ?, ?, ?, 'normal', 'project',
			'active', 1, '2026-07-11T05:00:00Z', '2026-07-11T05:00:00Z')`,
		rootObjectID, fixture.storeID, fixture.teamID, ownerPrincipalID,
		ownerPrincipalID, ownerPrincipalID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DB().Exec(`
		INSERT INTO team_memory_capsules(
			capsule_id, root_object_id, team_id, scope_type, scope_id,
			root_generation, item_ordinal, schema_version, source_host,
			conversation_scope, source_timestamp, kind, redacted_summary,
			confidence, evidence_hint, tags_json, created_at)
		VALUES (?, ?, ?, 'personal', ?, 1, 0, 'pulse.team.memory.v1', 'codex',
			'current_turn', '2026-07-11T05:00:00.000Z', 'decision',
			'identical security decision', ?, 'user_confirmed', '["security"]',
			'2026-07-11T05:00:00Z')`,
		capsuleID, rootObjectID, fixture.teamID, ownerPrincipalID, confidence,
	); err != nil {
		t.Fatal(err)
	}
}

func fixtureCapabilities() []teamauth.Capability {
	return []teamauth.Capability{teamauth.CapabilityConnect, teamauth.CapabilityRead}
}
