package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamauth"
)

const (
	testTeamGraphDeltaRoutePath    = "/team/v1/graph/delta"
	testTeamGraphDeltaResultSchema = "pulse.team.graph_delta_result.v1"
)

type decodedTeamGraphJob struct {
	Kind  string `json:"kind"`
	JobID string `json:"job_id"`
	State string `json:"state"`
}

type decodedTeamGraphResult struct {
	Schema          string                `json:"schema"`
	ObjectID        string                `json:"object_id"`
	AuditEventID    string                `json:"audit_event_id"`
	Status          string                `json:"status"`
	ProjectionState string                `json:"projection_state"`
	ProjectionJobs  []decodedTeamGraphJob `json:"projection_jobs"`
	FullyProjected  bool                  `json:"fully_projected"`
	Replayed        bool                  `json:"replayed"`
	Fallback        bool                  `json:"fallback"`
}

func TestTeamGraphDeltaStoresExactProjectResultWithoutLocalFallback(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	project, err := fixture.store.CreateTeamProject(context.Background(), fixture.ownerID, "Server U10 graph")
	if err != nil {
		t.Fatal(err)
	}
	active := store.TeamGraphActiveContext{
		ProjectID: project.ProjectID, SessionID: "session-server-u10",
	}
	target := &store.TeamGraphTarget{Type: teamauth.ScopeProject, ID: project.ProjectID}
	body := validTeamGraphBody(t, fixture.now, "graph-project-0001", active, target)
	legacyBefore := serverTeamGraphLegacyCounts(t, fixture.store)

	response := serveSignedTeamGraph(t, srv, fixture, "graph-project", "agent-client", body)
	if response.Code != http.StatusOK {
		t.Fatalf("graph response = %d %q", response.Code, response.Body.String())
	}
	result := decodeTeamGraphResult(t, response)
	if result.Schema != testTeamGraphDeltaResultSchema || result.Status != store.TeamObjectStatusStored ||
		result.ProjectionState != store.TeamProjectionStatePending || result.FullyProjected ||
		result.Replayed || result.Fallback || !safeOpaque(result.ObjectID, 255) ||
		!safeOpaque(result.AuditEventID, 255) {
		t.Fatalf("unexpected graph result: %+v", result)
	}
	requireTeamGraphJobs(t, result.ProjectionJobs, "claim", "continuity", "embedding", "graph")

	var objectKind, scopeType, scopeID, authorID string
	if err := fixture.store.DB().QueryRow(`
		SELECT object_kind, scope_type, scope_id, author_principal_id
		  FROM team_object_registry WHERE object_id = ?`, result.ObjectID).
		Scan(&objectKind, &scopeType, &scopeID, &authorID); err != nil {
		t.Fatal(err)
	}
	if objectKind != "graph_delta" || scopeType != string(teamauth.ScopeProject) ||
		scopeID != project.ProjectID || authorID != fixture.agentID {
		t.Fatalf("graph attribution = kind %q scope %q/%q author %q", objectKind, scopeType, scopeID, authorID)
	}
	if got := countServerTeamGraphInputs(t, fixture.store); got != 1 {
		t.Fatalf("team graph inputs = %d, want 1", got)
	}
	if legacyAfter := serverTeamGraphLegacyCounts(t, fixture.store); !reflect.DeepEqual(legacyAfter, legacyBefore) {
		t.Fatalf("team graph route touched local tables: before=%v after=%v", legacyBefore, legacyAfter)
	}
}

func TestTeamGraphDeltaBindsAssertionToExactRouteAndRawBody(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	body := personalTeamGraphBody(t, fixture, "graph-binding-0001")
	claims := teamGraphClaims(fixture, "graph-binding", "agent-client", body)
	assertion := signPrincipalAssertion(t, fixture.private, "active", nil, claims)
	tampered := append(append([]byte(nil), body...), ' ')

	mismatch := serveTeamGraphRequest(srv.Handler(), assertion, "req-graph-binding", bodyPathRequest{
		path: testTeamGraphDeltaRoutePath, body: tampered,
	})
	requireTeamGraphError(t, mismatch, http.StatusUnauthorized, "principal_request_mismatch")

	accepted := serveTeamGraphRequest(srv.Handler(), assertion, "req-graph-binding", bodyPathRequest{
		path: testTeamGraphDeltaRoutePath, body: body,
	})
	if accepted.Code != http.StatusOK {
		t.Fatalf("exact body after mismatch = %d %q", accepted.Code, accepted.Body.String())
	}

	wrongPathClaims := teamGraphClaims(fixture, "graph-wrong-path", "agent-client", body)
	wrongPathClaims["path"] = TeamMemoryRememberRoutePath
	wrongPathAssertion := signPrincipalAssertion(t, fixture.private, "active", nil, wrongPathClaims)
	wrongPath := serveTeamGraphRequest(srv.Handler(), wrongPathAssertion, "req-graph-wrong-path", bodyPathRequest{
		path: testTeamGraphDeltaRoutePath, body: body,
	})
	requireTeamGraphError(t, wrongPath, http.StatusUnauthorized, "principal_request_mismatch")

	for index, path := range []string{
		testTeamGraphDeltaRoutePath + "/", "/team/v1/graph", "/graph/delta",
	} {
		invalidClaims := teamGraphClaims(fixture, "graph-invalid-path-"+string(rune('a'+index)), "agent-client", body)
		invalidClaims["path"] = path
		invalidAssertion := signPrincipalAssertion(t, fixture.private, "active", nil, invalidClaims)
		invalid := serveTeamGraphRequest(
			srv.Handler(), invalidAssertion, "req-graph-invalid-path-"+string(rune('a'+index)),
			bodyPathRequest{path: testTeamGraphDeltaRoutePath, body: body},
		)
		requireTeamGraphError(t, invalid, http.StatusUnauthorized, "invalid_principal")
	}
	queryBody := personalTeamGraphBody(t, fixture, "graph-query-0001")
	queryClaims := teamGraphClaims(fixture, "graph-query", "agent-client", queryBody)
	queryAssertion := signPrincipalAssertion(t, fixture.private, "active", nil, queryClaims)
	query := serveTeamGraphRequest(srv.Handler(), queryAssertion, "req-graph-query", bodyPathRequest{
		path: testTeamGraphDeltaRoutePath + "?unexpected=1", body: queryBody,
	})
	requireTeamGraphError(t, query, http.StatusBadRequest, "invalid_team_graph_delta")

	getClaims := teamGraphClaims(fixture, "graph-invalid-method", "agent-client", body)
	getClaims["method"] = http.MethodGet
	getAssertion := signPrincipalAssertion(t, fixture.private, "active", nil, getClaims)
	if _, err := srv.cfg.PrincipalVerifier.VerifyDomainRequest(
		context.Background(), getAssertion, "req-graph-invalid-method",
		http.MethodGet, testTeamGraphDeltaRoutePath, body,
	); !errors.Is(err, ErrPrincipalInvalid) {
		t.Fatalf("GET graph assertion error = %v", err)
	}
}

func TestTeamGraphDeltaFreshAssertionReplaysResultAfterResponseLoss(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	body := personalTeamGraphBody(t, fixture, "graph-response-loss-0001")
	claims := teamGraphClaims(fixture, "graph-response-loss-first", "agent-client", body)
	assertion := signPrincipalAssertion(t, fixture.private, "active", nil, claims)

	firstResponse := serveTeamGraphRequest(srv.Handler(), assertion, "req-graph-response-loss-first", bodyPathRequest{
		path: testTeamGraphDeltaRoutePath, body: body,
	})
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first response = %d %q", firstResponse.Code, firstResponse.Body.String())
	}
	first := decodeTeamGraphResult(t, firstResponse)

	replayedAssertion := serveTeamGraphRequest(srv.Handler(), assertion, "req-graph-response-loss-first", bodyPathRequest{
		path: testTeamGraphDeltaRoutePath, body: body,
	})
	requireTeamGraphError(t, replayedAssertion, http.StatusUnauthorized, "principal_replay")

	// The caller may lose the first response. A fresh assertion with the same
	// idempotency key must recover the original durable result without a rewrite.
	freshResponse := serveSignedTeamGraph(t, srv, fixture, "graph-response-loss-fresh", "agent-client", body)
	if freshResponse.Code != http.StatusOK {
		t.Fatalf("fresh retry = %d %q", freshResponse.Code, freshResponse.Body.String())
	}
	fresh := decodeTeamGraphResult(t, freshResponse)
	if !fresh.Replayed || first.ObjectID != fresh.ObjectID || first.AuditEventID != fresh.AuditEventID ||
		!reflect.DeepEqual(first.ProjectionJobs, fresh.ProjectionJobs) || countServerTeamGraphInputs(t, fixture.store) != 1 {
		t.Fatalf("fresh retry changed result:\nfirst=%+v\nfresh=%+v", first, fresh)
	}
}

func TestTeamGraphDeltaMapsIdempotencyConflictWithoutLeakingContent(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	body := personalTeamGraphBody(t, fixture, "graph-conflict-0001")
	first := serveSignedTeamGraph(t, srv, fixture, "graph-conflict-first", "agent-client", body)
	if first.Code != http.StatusOK {
		t.Fatalf("first response = %d %q", first.Code, first.Body.String())
	}
	changed := mutateTeamGraphBody(t, body, func(envelope map[string]any) {
		envelope["facts"].([]any)[0].(map[string]any)["text"] = "Different safe graph content."
	})
	conflict := serveSignedTeamGraph(t, srv, fixture, "graph-conflict-second", "agent-client", changed)
	requireTeamGraphError(t, conflict, http.StatusConflict, "idempotency_conflict")
	if strings.Contains(conflict.Body.String(), "Different safe graph content") {
		t.Fatalf("conflict leaked graph content: %q", conflict.Body.String())
	}
	if countServerTeamGraphInputs(t, fixture.store) != 1 {
		t.Fatal("idempotency conflict changed durable graph input count")
	}
}

func TestTeamGraphDeltaReturnsOnlyConditionalSortedJobs(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	tests := []struct {
		name     string
		mutate   func(map[string]any)
		expected []string
	}{
		{
			name: "continuity only",
			mutate: func(body map[string]any) {
				body["nodes"] = []any{}
				body["edges"] = []any{}
				body["facts"] = []any{}
				body["events"] = []any{}
			},
			expected: []string{"continuity"},
		},
		{
			name: "unstructured graph",
			mutate: func(body map[string]any) {
				delete(body, "continuity")
				fact := body["facts"].([]any)[0].(map[string]any)
				for _, field := range []string{"predicate", "object_text", "valid_from", "change_cue", "source_event_refs"} {
					delete(fact, field)
				}
			},
			expected: []string{"embedding", "graph"},
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := personalTeamGraphBody(t, fixture, "graph-jobs-"+string(rune('a'+index))+"-0001")
			body = mutateTeamGraphBody(t, body, test.mutate)
			response := serveSignedTeamGraph(
				t, srv, fixture, "graph-jobs-"+string(rune('a'+index)), "agent-client", body,
			)
			if response.Code != http.StatusOK {
				t.Fatalf("graph jobs response = %d %q", response.Code, response.Body.String())
			}
			result := decodeTeamGraphResult(t, response)
			requireTeamGraphJobs(t, result.ProjectionJobs, test.expected...)
		})
	}
}

func TestTeamGraphDeltaRejectsInvalidNullUnknownAndCrossContractBodies(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "unknown authority", mutate: func(body map[string]any) { body["principal_id"] = "spoofed" }},
		{name: "nested policy", mutate: func(body map[string]any) {
			body["nodes"].([]any)[0].(map[string]any)["membership_role"] = "owner"
		}},
		{name: "raw true", mutate: func(body map[string]any) { body["raw_input_included"] = true }},
		{name: "missing raw", mutate: func(body map[string]any) { delete(body, "raw_input_included") }},
		{name: "missing nodes", mutate: func(body map[string]any) { delete(body, "nodes") }},
		{name: "null events", mutate: func(body map[string]any) { body["events"] = nil }},
		{name: "null active context", mutate: func(body map[string]any) { body["active_context"] = nil }},
		{name: "empty active context field", mutate: func(body map[string]any) {
			body["active_context"].(map[string]any)["project_id"] = ""
		}},
		{name: "null optional target", mutate: func(body map[string]any) { body["target_scope"] = nil }},
		{name: "personal target with id field", mutate: func(body map[string]any) {
			body["target_scope"] = map[string]any{"type": "personal", "id": ""}
		}},
		{name: "too many nodes", mutate: func(body map[string]any) {
			node := body["nodes"].([]any)[0]
			nodes := make([]any, 31)
			for index := range nodes {
				nodes[index] = node
			}
			body["nodes"] = nodes
		}},
		{name: "missing confidence", mutate: func(body map[string]any) {
			delete(body["facts"].([]any)[0].(map[string]any), "confidence")
		}},
		{name: "null confidence", mutate: func(body map[string]any) {
			body["events"].([]any)[0].(map[string]any)["confidence"] = nil
		}},
		{name: "unknown edge ref", mutate: func(body map[string]any) {
			body["edges"].([]any)[0].(map[string]any)["from"] = "person:missing"
		}},
		{name: "continuity mismatch", mutate: func(body map[string]any) {
			body["continuity"].(map[string]any)["session_id"] = "session-other"
		}},
		{name: "unsafe path", mutate: func(body map[string]any) {
			body["facts"].([]any)[0].(map[string]any)["text"] = "Read /Users/example/private/notes.txt."
		}},
		{name: "unsafe secret", mutate: func(body map[string]any) {
			body["events"].([]any)[0].(map[string]any)["summary"] = "Authorization: Bearer secret-value"
		}},
		{name: "transcript", mutate: func(body map[string]any) {
			body["continuity"].(map[string]any)["summary"] = "user: one\nassistant: two\nuser: three\nassistant: four\nuser: five\nassistant: six"
		}},
		{name: "line separator", mutate: func(body map[string]any) {
			body["events"].([]any)[0].(map[string]any)["summary"] = "line\u2028separator"
		}},
		{name: "team target", mutate: func(body map[string]any) {
			body["target_scope"] = map[string]any{"type": "team", "id": fixture.teamID}
		}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := personalTeamGraphBody(t, fixture, "graph-invalid-"+string(rune('a'+index))+"-0001")
			body = mutateTeamGraphBody(t, body, test.mutate)
			response := serveSignedTeamGraph(
				t, srv, fixture, "graph-invalid-"+string(rune('a'+index)), "agent-client", body,
			)
			requireTeamGraphError(t, response, http.StatusBadRequest, "invalid_team_graph_delta")
			if strings.Contains(response.Body.String(), "spoofed") || strings.Contains(response.Body.String(), "membership_role") {
				t.Fatalf("invalid response leaked body content: %q", response.Body.String())
			}
		})
	}

	memory := validTeamMemoryBody(t, fixture.now, "memory-cross-0001", store.TeamMemoryActiveContext{}, nil)
	crossContract := serveSignedTeamGraph(t, srv, fixture, "graph-cross-contract", "agent-client", memory)
	requireTeamGraphError(t, crossContract, http.StatusBadRequest, "invalid_team_graph_delta")
	if got := countServerTeamGraphInputs(t, fixture.store); got != 0 {
		t.Fatalf("invalid graph bodies stored %d rows", got)
	}
}

func TestTeamGraphDeltaPolicyRevocationReadinessAndRouterIsolation(t *testing.T) {
	t.Run("service without target is denied", func(t *testing.T) {
		srv, fixture, _ := newReadyTeamServer(t)
		body := personalTeamGraphBody(t, fixture, "graph-service-0001")
		response := serveSignedTeamGraph(t, srv, fixture, "graph-service", "service-client", body)
		requireTeamGraphError(t, response, http.StatusForbidden, "policy_denied")
		if countServerTeamGraphInputs(t, fixture.store) != 0 {
			t.Fatal("denied service stored a graph delta")
		}
	})

	t.Run("revocation after verified context is rechecked", func(t *testing.T) {
		srv, fixture, _ := newReadyTeamServer(t)
		body := personalTeamGraphBody(t, fixture, "graph-revoke-context-0001")
		claims := teamGraphClaims(fixture, "graph-revoke-context", "agent-client", body)
		assertion := signPrincipalAssertion(t, fixture.private, "active", nil, claims)
		principal, err := srv.cfg.PrincipalVerifier.VerifyDomainRequest(
			context.Background(), assertion, "req-graph-revoke-context",
			http.MethodPost, testTeamGraphDeltaRoutePath, body,
		)
		if err != nil {
			t.Fatalf("verify graph principal: %v", err)
		}
		if err := fixture.store.RevokeAgentBinding(context.Background(), fixture.ownerID, fixture.bindingID); err != nil {
			t.Fatal(err)
		}
		capabilities := make([]teamauth.Capability, len(principal.Capabilities))
		for index, capability := range principal.Capabilities {
			capabilities[index] = teamauth.Capability(capability)
		}
		_, err = fixture.store.AuthorizeTeamMutation(context.Background(), store.TeamMutationAuthorizationRequest{
			PrincipalID: principal.PrincipalID, OAuthClientKey: principal.OAuthClientKey,
			Action: teamauth.ActionWrite, Capabilities: capabilities,
			Context:    teamauth.ActiveContext{TeamID: principal.TeamID, SessionID: "session-server-u10"},
			ObjectKind: "graph_delta",
		})
		if !errors.Is(err, store.ErrTeamPolicyDenied) {
			t.Fatalf("authorization after revocation error = %v", err)
		}
		if countServerTeamGraphInputs(t, fixture.store) != 0 {
			t.Fatal("revoked context stored a graph delta")
		}
		freshBody := personalTeamGraphBody(t, fixture, "graph-revoke-context-0002")
		fresh := serveSignedTeamGraph(t, srv, fixture, "graph-revoke-fresh", "agent-client", freshBody)
		requireTeamGraphError(t, fresh, http.StatusForbidden, "principal_revoked")
	})

	t.Run("team route is readiness gated and local route is absent", func(t *testing.T) {
		srv, fixture, _ := newReadyTeamServer(t)
		body := personalTeamGraphBody(t, fixture, "graph-router-0001")
		local, err := New(Config{IPCSecret: "local-secret"})
		if err != nil {
			t.Fatal(err)
		}
		localResponse := serveTeamRequest(
			local.Handler(), http.MethodPost, testTeamGraphDeltaRoutePath,
			"local-secret", "127.0.0.1:49000", body,
		)
		if localResponse.Code != http.StatusNotFound {
			t.Fatalf("local handler exposed team graph route: %d", localResponse.Code)
		}
		legacyResponse := serveTeamRequest(
			srv.Handler(), http.MethodPost, "/graph/delta",
			testTeamIPCSecret, "127.0.0.1:49000", body,
		)
		if legacyResponse.Code != http.StatusNotFound {
			t.Fatalf("team handler exposed local graph route: %d", legacyResponse.Code)
		}
		if _, err := fixture.store.DB().Exec(`UPDATE team_writer_leases SET expires_at = '2020-01-01T00:00:00Z'`); err != nil {
			t.Fatal(err)
		}
		readiness := serveTeamRequest(
			srv.Handler(), http.MethodPost, testTeamGraphDeltaRoutePath,
			testTeamIPCSecret, "127.0.0.1:49000", body,
		)
		requireTeamGraphError(t, readiness, http.StatusServiceUnavailable, "shared_memory_unavailable")
	})
}

func TestTeamGraphDeltaRejectsOversizedBodyBeforePrincipalResolution(t *testing.T) {
	srv, _, _ := newReadyTeamServer(t)
	body := bytes.Repeat([]byte("x"), (256<<10)+1)
	response := serveTeamRequest(
		srv.Handler(), http.MethodPost, testTeamGraphDeltaRoutePath,
		testTeamIPCSecret, "127.0.0.1:49000", body,
	)
	requireTeamGraphError(t, response, http.StatusRequestEntityTooLarge, "invalid_team_graph_delta")
}

func personalTeamGraphBody(t *testing.T, fixture *principalFixture, idempotencyKey string) []byte {
	t.Helper()
	return validTeamGraphBody(t, fixture.now, idempotencyKey, store.TeamGraphActiveContext{
		SessionID: "session-server-u10",
	}, nil)
}

func validTeamGraphBody(
	t *testing.T,
	timestamp time.Time,
	idempotencyKey string,
	active store.TeamGraphActiveContext,
	target *store.TeamGraphTarget,
) []byte {
	t.Helper()
	body, err := json.Marshal(store.TeamGraphDeltaWrite{
		Schema: store.TeamGraphDeltaSchema,
		Source: store.CapsuleSource{
			Host: "claude-code", ConversationScope: "current_turn",
			Timestamp: timestamp.UTC().Format(time.RFC3339Nano),
		},
		Nodes: []store.TeamGraphNode{
			{
				ClientID: "person:alex", Kind: "person", CanonicalName: " Alex ",
				Summary: serverGraphString(" Works on the Pulse pilot. "),
				Aliases: []string{" Alexander ", "Alexey"}, Salience: serverGraphFloat(0.8),
				EmotionalWeight: serverGraphFloat(0.2), Domain: "real",
			},
			{
				ClientID: "project:pulse", Kind: "project", CanonicalName: "Pulse",
				Aliases: []string{}, Salience: serverGraphFloat(0.9),
				EmotionalWeight: serverGraphFloat(0), Domain: "real",
			},
		},
		Edges: []store.TeamGraphEdge{{
			From: "person:alex", To: "project:pulse", Kind: "works_on",
			Summary: serverGraphString(" Alex contributes to Pulse. "), Strength: serverGraphFloat(0.9),
		}},
		Facts: []store.TeamGraphFact{{
			Node: "person:alex", Text: " Alex is based in Lisbon. ",
			Predicate: serverGraphString("home_base"), ObjectText: serverGraphString(" Lisbon "),
			ValidFrom: serverGraphString(timestamp.UTC().Format(time.RFC3339Nano)),
			ChangeCue: serverGraphBool(true), SourceEventRefs: []string{"event:moved"},
			Confidence: serverGraphFloat(0.9), Domain: "real",
		}},
		Events: []store.TeamGraphEvent{{
			ClientID: "event:moved", Title: " Alex moved ", Summary: " Alex changed home base to Lisbon. ",
			EntityRefs: []string{"person:alex"}, Sentiment: serverGraphString(" restoration "),
			EmotionalWeight: serverGraphFloat(0.3), Confidence: serverGraphFloat(0.9), Domain: "real",
			OccurredAt: serverGraphString(timestamp.UTC().Format(time.RFC3339Nano)), Anchor: serverGraphBool(false),
			Biometrics: &store.TeamGraphBiometrics{
				HRV: serverGraphFloat(58), SleepQuality: serverGraphFloat(0.8),
				StressProxy: serverGraphFloat(0.2), HRTrend: serverGraphString("stable"),
				HRVTrend: serverGraphString("rising"), Workout: serverGraphBool(true),
			},
			Emotions: map[string]*float64{"joy": serverGraphFloat(0.3), "trust": serverGraphFloat(0.6)},
		}},
		Continuity: &store.TeamGraphContinuity{
			ThreadID: "pulse-pilot", SessionID: active.SessionID,
			Summary:     " Stopped after agreeing on scoped team storage. ",
			Decisions:   []string{" Use a dedicated team store. "},
			OpenLoops:   []string{" Wire the team graph gateway. "},
			DoNotRepeat: []string{}, EmotionalAnchors: []string{}, StateSignals: []string{},
			ActiveThreads: []string{"U10"}, ReviewInsights: []string{},
		},
		RawInputIncluded: false, ActiveContext: active, TargetScope: target,
		PrivacyTier: "normal", Retention: "project", IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func teamGraphClaims(fixture *principalFixture, jti, clientID string, body []byte) map[string]any {
	claims := fixture.claims(jti, clientID, body)
	claims["path"] = testTeamGraphDeltaRoutePath
	claims["capabilities"] = []string{"pulse:connect", "pulse:write"}
	return claims
}

func serveSignedTeamGraph(
	t *testing.T,
	srv *TeamServer,
	fixture *principalFixture,
	jti, clientID string,
	body []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	assertion := signPrincipalAssertion(t, fixture.private, "active", nil, teamGraphClaims(fixture, jti, clientID, body))
	return serveTeamGraphRequest(srv.Handler(), assertion, "req-"+jti, bodyPathRequest{
		path: testTeamGraphDeltaRoutePath, body: body,
	})
}

type bodyPathRequest struct {
	path string
	body []byte
}

func serveTeamGraphRequest(
	handler http.Handler,
	assertion, requestID string,
	requestBody bodyPathRequest,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, requestBody.path, bytes.NewReader(requestBody.body))
	request.RemoteAddr = "127.0.0.1:48000"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Pulse-Key", testTeamIPCSecret)
	request.Header.Set("X-Pulse-Principal", assertion)
	request.Header.Set("X-Pulse-Request-ID", requestID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeTeamGraphResult(t *testing.T, response *httptest.ResponseRecorder) decodedTeamGraphResult {
	t.Helper()
	var exact map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &exact); err != nil {
		t.Fatalf("decode exact graph result: %v (%q)", err, response.Body.String())
	}
	if len(exact) != 9 {
		t.Fatalf("graph result has extra or missing fields: %v", exact)
	}
	var result decodedTeamGraphResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode graph result: %v (%q)", err, response.Body.String())
	}
	return result
}

func requireTeamGraphJobs(t *testing.T, jobs []decodedTeamGraphJob, expected ...string) {
	t.Helper()
	if len(jobs) != len(expected) {
		t.Fatalf("projection jobs = %+v, want %v", jobs, expected)
	}
	kinds := make([]string, len(jobs))
	for index, job := range jobs {
		if !safeOpaque(job.JobID, 255) || job.State != store.TeamProjectionStatePending {
			t.Fatalf("invalid projection job: %+v", job)
		}
		kinds[index] = job.Kind
	}
	if !sort.StringsAreSorted(kinds) || !reflect.DeepEqual(kinds, expected) {
		t.Fatalf("projection job kinds = %v, want %v", kinds, expected)
	}
}

func requireTeamGraphError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("graph error response = %d %q, want %d %q", response.Code, response.Body.String(), status, code)
	}
	var exact map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &exact); err != nil {
		t.Fatalf("decode graph error: %v (%q)", err, response.Body.String())
	}
	if len(exact) != 2 || exact["error"] != code || exact["fallback"] != false {
		t.Fatalf("graph error envelope = %v", exact)
	}
}

func mutateTeamGraphBody(t *testing.T, raw []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	mutate(body)
	changed, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return changed
}

func countServerTeamGraphInputs(t *testing.T, database *store.Store) int {
	t.Helper()
	var count int
	if err := database.DB().QueryRow(`SELECT count(*) FROM team_graph_delta_inputs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func serverTeamGraphLegacyCounts(t *testing.T, database *store.Store) map[string]int {
	t.Helper()
	tables := []string{
		"entities", "relations", "facts", "events", "assertions",
		"continuity_threads", "continuity_sessions", "continuity_observations",
		"continuity_checkpoints", "memory_capsules",
	}
	counts := make(map[string]int, len(tables))
	for _, table := range tables {
		var count int
		if err := database.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = count
	}
	return counts
}

func serverGraphString(value string) *string  { return &value }
func serverGraphFloat(value float64) *float64 { return &value }
func serverGraphBool(value bool) *bool        { return &value }
