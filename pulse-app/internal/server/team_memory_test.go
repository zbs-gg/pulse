package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func TestTeamMemoryRememberStoresPersonalCapsuleFromVerifiedPrincipal(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	body := validTeamMemoryBody(t, fixture.now, "memory-personal-0001", store.TeamMemoryActiveContext{}, nil)
	response := serveSignedTeamMemory(t, srv, fixture, "remember-personal", "agent-client", body)
	if response.Code != http.StatusOK {
		t.Fatalf("remember response = %d %q", response.Code, response.Body.String())
	}

	result := decodeTeamMemoryResult(t, response)
	if result.Schema != TeamMemoryResultSchema || result.Status != store.TeamObjectStatusStored ||
		result.ProjectionState != store.TeamProjectionStatePending || result.FullyProjected || result.Replayed || result.Fallback {
		t.Fatalf("unexpected result state: %+v", result)
	}
	if !safeOpaque(result.ObjectID, 255) || !safeOpaque(result.AuditEventID, 255) || len(result.CapsuleIDs) != 1 ||
		!safeOpaque(result.CapsuleIDs[0], 255) {
		t.Fatalf("result did not contain opaque durable IDs: %+v", result)
	}
	requirePendingProjectionJobs(t, result.ProjectionJobs)

	var scopeType, scopeID, ownerID, authorID string
	if err := fixture.store.DB().QueryRow(`
		SELECT scope_type, scope_id, COALESCE(owner_principal_id, ''), author_principal_id
		  FROM team_object_registry WHERE object_id = ?`, result.ObjectID).
		Scan(&scopeType, &scopeID, &ownerID, &authorID); err != nil {
		t.Fatalf("read stored root: %v", err)
	}
	if scopeType != string(teamauth.ScopePersonal) || scopeID != fixture.ownerID || ownerID != fixture.ownerID || authorID != fixture.agentID {
		t.Fatalf("personal attribution = scope %q/%q owner %q author %q", scopeType, scopeID, ownerID, authorID)
	}
	if got := countTeamMemoryRows(t, fixture.store); got != 1 {
		t.Fatalf("team memory rows = %d, want 1", got)
	}
}

func TestTeamMemoryRememberStoresExplicitOwnedProjectScope(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	project, err := fixture.store.CreateTeamProject(context.Background(), fixture.ownerID, "Server U9 project")
	if err != nil {
		t.Fatal(err)
	}
	active := store.TeamMemoryActiveContext{ProjectID: project.ProjectID}
	target := &store.TeamMemoryTarget{Type: teamauth.ScopeProject, ID: project.ProjectID}
	body := validTeamMemoryBody(t, fixture.now, "memory-project-0001", active, target)
	response := serveSignedTeamMemory(t, srv, fixture, "remember-project", "agent-client", body)
	if response.Code != http.StatusOK {
		t.Fatalf("remember response = %d %q", response.Code, response.Body.String())
	}
	result := decodeTeamMemoryResult(t, response)

	var scopeType, scopeID, authorID string
	if err := fixture.store.DB().QueryRow(`
		SELECT scope_type, scope_id, author_principal_id
		  FROM team_object_registry WHERE object_id = ?`, result.ObjectID).
		Scan(&scopeType, &scopeID, &authorID); err != nil {
		t.Fatal(err)
	}
	if scopeType != string(teamauth.ScopeProject) || scopeID != project.ProjectID || authorID != fixture.agentID {
		t.Fatalf("project attribution = scope %q/%q author %q", scopeType, scopeID, authorID)
	}
}

func TestTeamMemoryRememberDeniesServiceWithoutExplicitTarget(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	body := validTeamMemoryBody(t, fixture.now, "memory-service-0001", store.TeamMemoryActiveContext{}, nil)
	response := serveSignedTeamMemory(t, srv, fixture, "remember-service", "service-client", body)
	requireTeamMemoryError(t, response, http.StatusForbidden, teamMemoryErrorPolicyDenied)
	if got := countTeamMemoryRows(t, fixture.store); got != 0 {
		t.Fatalf("denied service stored %d memory rows", got)
	}
	if got := countTeamMemoryObjects(t, fixture.store); got != 0 {
		t.Fatalf("denied service stored %d memory roots", got)
	}
}

func TestTeamMemoryRememberRejectsSpoofedAndUnknownFieldsWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "principal identity", mutate: func(body map[string]any) { body["principal_id"] = "principal_spoof" }},
		{name: "target owner", mutate: func(body map[string]any) {
			body["target_scope"] = map[string]any{"type": "personal", "owner_principal_id": "principal_spoof"}
		}},
		{name: "item role", mutate: func(body map[string]any) {
			items := body["items"].([]any)
			items[0].(map[string]any)["membership_role"] = "owner"
		}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv, fixture, _ := newReadyTeamServer(t)
			raw := validTeamMemoryBody(t, fixture.now, fmt.Sprintf("memory-spoof-%04d", index), store.TeamMemoryActiveContext{}, nil)
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			test.mutate(body)
			raw, _ = json.Marshal(body)
			response := serveSignedTeamMemory(t, srv, fixture, fmt.Sprintf("remember-spoof-%d", index), "agent-client", raw)
			requireTeamMemoryError(t, response, http.StatusBadRequest, teamMemoryErrorInvalid)
			if strings.Contains(response.Body.String(), "principal_spoof") || strings.Contains(response.Body.String(), "membership_role") {
				t.Fatalf("error leaked rejected input: %q", response.Body.String())
			}
			if got := countTeamMemoryRows(t, fixture.store); got != 0 {
				t.Fatalf("rejected request stored %d memory rows", got)
			}
		})
	}
}

func TestTeamMemoryRememberBindsAssertionToExactBody(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	body := validTeamMemoryBody(t, fixture.now, "memory-binding-0001", store.TeamMemoryActiveContext{}, nil)
	claims := teamMemoryClaims(fixture, "remember-binding", "agent-client", body)
	assertion := signPrincipalAssertion(t, fixture.private, "active", nil, claims)
	tampered := append(append([]byte(nil), body...), ' ')
	response := serveTeamMemoryRequest(srv.Handler(), assertion, "req-remember-binding", tampered)
	requireTeamMemoryError(t, response, http.StatusUnauthorized, teamMemoryErrorPrincipalRequestMismatch)
	if got := countTeamMemoryRows(t, fixture.store); got != 0 {
		t.Fatalf("request-mismatched assertion stored %d memory rows", got)
	}
}

func TestTeamMemoryRememberRequiresFreshAssertionAndFreshRetryReplaysDurableResult(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	body := validTeamMemoryBody(t, fixture.now, "memory-replay-0001", store.TeamMemoryActiveContext{}, nil)
	claims := teamMemoryClaims(fixture, "remember-replay-first", "agent-client", body)
	assertion := signPrincipalAssertion(t, fixture.private, "active", nil, claims)
	firstResponse := serveTeamMemoryRequest(srv.Handler(), assertion, "req-remember-replay-first", body)
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first response = %d %q", firstResponse.Code, firstResponse.Body.String())
	}
	first := decodeTeamMemoryResult(t, firstResponse)

	replayedAssertion := serveTeamMemoryRequest(srv.Handler(), assertion, "req-remember-replay-first", body)
	requireTeamMemoryError(t, replayedAssertion, http.StatusUnauthorized, teamMemoryErrorPrincipalReplay)
	if got := countTeamMemoryObjects(t, fixture.store); got != 1 {
		t.Fatalf("replayed assertion changed memory roots: %d", got)
	}

	freshResponse := serveSignedTeamMemory(t, srv, fixture, "remember-replay-fresh", "agent-client", body)
	if freshResponse.Code != http.StatusOK {
		t.Fatalf("fresh retry = %d %q", freshResponse.Code, freshResponse.Body.String())
	}
	fresh := decodeTeamMemoryResult(t, freshResponse)
	if !fresh.Replayed || first.ObjectID != fresh.ObjectID || first.AuditEventID != fresh.AuditEventID ||
		!reflect.DeepEqual(first.CapsuleIDs, fresh.CapsuleIDs) || !reflect.DeepEqual(first.ProjectionJobs, fresh.ProjectionJobs) {
		t.Fatalf("fresh idempotent retry changed result:\nfirst=%+v\nfresh=%+v", first, fresh)
	}
}

func TestTeamMemoryRememberMapsIdempotencyConflictWithoutLeakingContent(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	body := validTeamMemoryBody(t, fixture.now, "memory-conflict-0001", store.TeamMemoryActiveContext{}, nil)
	first := serveSignedTeamMemory(t, srv, fixture, "remember-conflict-first", "agent-client", body)
	if first.Code != http.StatusOK {
		t.Fatalf("first response = %d %q", first.Code, first.Body.String())
	}
	var changed map[string]any
	if err := json.Unmarshal(body, &changed); err != nil {
		t.Fatal(err)
	}
	changed["items"].([]any)[0].(map[string]any)["redacted_summary"] = "Different safe memory content."
	body, _ = json.Marshal(changed)
	conflict := serveSignedTeamMemory(t, srv, fixture, "remember-conflict-second", "agent-client", body)
	requireTeamMemoryError(t, conflict, http.StatusConflict, teamMemoryErrorIdempotencyConflict)
	if strings.Contains(conflict.Body.String(), "Different safe memory content") {
		t.Fatalf("conflict leaked content: %q", conflict.Body.String())
	}
}

func TestTeamMemoryRememberKeepsTranscriptSecretAndPathGuardsAtGoBoundary(t *testing.T) {
	unsafeSummaries := []string{
		"Read /Users/alice/private/notes.txt for the answer.",
		"The credential was token=supersecret and should be retained.",
		"user: first\nassistant: one\nuser: second\nassistant: two\nuser: third\nassistant: three",
	}
	for index, summary := range unsafeSummaries {
		t.Run(fmt.Sprintf("unsafe-%d", index), func(t *testing.T) {
			srv, fixture, _ := newReadyTeamServer(t)
			body := validTeamMemoryBody(t, fixture.now, fmt.Sprintf("memory-unsafe-%04d", index), store.TeamMemoryActiveContext{}, nil)
			var changed map[string]any
			if err := json.Unmarshal(body, &changed); err != nil {
				t.Fatal(err)
			}
			changed["items"].([]any)[0].(map[string]any)["redacted_summary"] = summary
			body, _ = json.Marshal(changed)
			response := serveSignedTeamMemory(t, srv, fixture, fmt.Sprintf("remember-unsafe-%d", index), "agent-client", body)
			requireTeamMemoryError(t, response, http.StatusBadRequest, teamMemoryErrorInvalid)
			if strings.Contains(response.Body.String(), summary) {
				t.Fatalf("error leaked unsafe content: %q", response.Body.String())
			}
			if got := countTeamMemoryRows(t, fixture.store); got != 0 {
				t.Fatalf("unsafe request stored %d memory rows", got)
			}
		})
	}
}

func TestTeamMemoryRememberUsesUnicodeCharacterLimitAtBoundary(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	body := validTeamMemoryBody(t, fixture.now, "memory-unicode-0001", store.TeamMemoryActiveContext{}, nil)
	var changed map[string]any
	if err := json.Unmarshal(body, &changed); err != nil {
		t.Fatal(err)
	}
	changed["items"].([]any)[0].(map[string]any)["redacted_summary"] = strings.Repeat("я", 1200)
	body, _ = json.Marshal(changed)
	accepted := serveSignedTeamMemory(t, srv, fixture, "remember-unicode-limit", "agent-client", body)
	if accepted.Code != http.StatusOK {
		t.Fatalf("1200 Cyrillic characters = %d %q", accepted.Code, accepted.Body.String())
	}

	changed["items"].([]any)[0].(map[string]any)["redacted_summary"] = strings.Repeat("я", 1201)
	changed["idempotency_key"] = "memory-unicode-0002"
	body, _ = json.Marshal(changed)
	rejected := serveSignedTeamMemory(t, srv, fixture, "remember-unicode-over-limit", "agent-client", body)
	requireTeamMemoryError(t, rejected, http.StatusBadRequest, teamMemoryErrorInvalid)
	if got := countTeamMemoryRows(t, fixture.store); got != 1 {
		t.Fatalf("Unicode over-limit request changed capsule count: %d", got)
	}
}

func TestTeamMemoryRememberMasksRawStoreFailuresAndRollsBack(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	const rawFailure = "synthetic /Users/private/secret store failure"
	if _, err := fixture.store.DB().Exec(`
		CREATE TRIGGER fail_team_memory_server_test
		BEFORE INSERT ON team_memory_capsules
		BEGIN
			SELECT RAISE(ABORT, 'synthetic /Users/private/secret store failure');
		END`); err != nil {
		t.Fatal(err)
	}
	body := validTeamMemoryBody(t, fixture.now, "memory-failure-0001", store.TeamMemoryActiveContext{}, nil)
	response := serveSignedTeamMemory(t, srv, fixture, "remember-store-failure", "agent-client", body)
	requireTeamMemoryError(t, response, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
	if strings.Contains(response.Body.String(), rawFailure) || strings.Contains(response.Body.String(), "/Users/") {
		t.Fatalf("store failure leaked internal detail: %q", response.Body.String())
	}
	if got := countTeamMemoryObjects(t, fixture.store); got != 0 {
		t.Fatalf("failed transaction stored %d roots", got)
	}
	if got := countTeamMemoryRows(t, fixture.store); got != 0 {
		t.Fatalf("failed transaction stored %d capsules", got)
	}
}

func TestTeamMemoryRememberRejectsRevokedPrincipalOnNextRequestWithoutDomainMutation(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	firstBody := validTeamMemoryBody(t, fixture.now, "memory-revoke-0001", store.TeamMemoryActiveContext{}, nil)
	first := serveSignedTeamMemory(t, srv, fixture, "remember-before-revoke", "agent-client", firstBody)
	if first.Code != http.StatusOK {
		t.Fatalf("first remember = %d %q", first.Code, first.Body.String())
	}
	objectsBefore, capsulesBefore := countTeamMemoryObjects(t, fixture.store), countTeamMemoryRows(t, fixture.store)
	if err := fixture.store.RevokeAgentBinding(context.Background(), fixture.ownerID, fixture.bindingID); err != nil {
		t.Fatal(err)
	}

	secondBody := validTeamMemoryBody(t, fixture.now.Add(time.Second), "memory-revoke-0002", store.TeamMemoryActiveContext{}, nil)
	second := serveSignedTeamMemory(t, srv, fixture, "remember-after-revoke", "agent-client", secondBody)
	requireTeamMemoryError(t, second, http.StatusForbidden, teamMemoryErrorPrincipalRevoked)
	if got := countTeamMemoryObjects(t, fixture.store); got != objectsBefore {
		t.Fatalf("revoked request changed memory roots: before %d after %d", objectsBefore, got)
	}
	if got := countTeamMemoryRows(t, fixture.store); got != capsulesBefore {
		t.Fatalf("revoked request changed memory capsules: before %d after %d", capsulesBefore, got)
	}
}

func TestTeamMemoryRememberRouteIsTeamOnlyAndReadinessGated(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	body := validTeamMemoryBody(t, fixture.now, "memory-route-0001", store.TeamMemoryActiveContext{}, nil)
	registered := serveTeamRequest(srv.Handler(), http.MethodPost, TeamMemoryRememberRoutePath, testTeamIPCSecret, "127.0.0.1:49000", body)
	if registered.Code == http.StatusNotFound {
		t.Fatal("team memory route was not registered")
	}

	local, err := New(Config{IPCSecret: "local-secret"})
	if err != nil {
		t.Fatal(err)
	}
	localResponse := serveTeamRequest(local.Handler(), http.MethodPost, TeamMemoryRememberRoutePath, "local-secret", "127.0.0.1:49000", body)
	if localResponse.Code != http.StatusNotFound {
		t.Fatalf("local handler exposed team memory route: %d", localResponse.Code)
	}

	if _, err := fixture.store.DB().Exec(`UPDATE team_writer_leases SET expires_at = '2020-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	readiness := serveTeamRequest(srv.Handler(), http.MethodPost, TeamMemoryRememberRoutePath, testTeamIPCSecret, "127.0.0.1:49000", body)
	requireTeamMemoryError(t, readiness, http.StatusServiceUnavailable, teamErrorSharedMemoryUnavailable)
}

func validTeamMemoryBody(
	t *testing.T,
	timestamp time.Time,
	idempotencyKey string,
	active store.TeamMemoryActiveContext,
	target *store.TeamMemoryTarget,
) []byte {
	t.Helper()
	body, err := json.Marshal(store.TeamMemoryWrite{
		Schema: store.TeamMemorySchema,
		Source: store.CapsuleSource{
			Host: "codex", ConversationScope: "current_turn", Timestamp: timestamp.UTC().Format(time.RFC3339Nano),
		},
		Items: []store.TeamMemoryItem{{
			Kind: "decision", RedactedSummary: "Use an isolated team memory route.", Confidence: 0.95,
			EvidenceHint: "current_turn", Tags: []string{"pilot", "u9"},
		}},
		RawInputIncluded: false, ActiveContext: active, TargetScope: target,
		PrivacyTier: "normal", Retention: "project", IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func teamMemoryClaims(fixture *principalFixture, jti, clientID string, body []byte) map[string]any {
	claims := fixture.claims(jti, clientID, body)
	claims["path"] = TeamMemoryRememberRoutePath
	claims["capabilities"] = []string{"pulse:connect", "pulse:write"}
	return claims
}

func serveSignedTeamMemory(
	t *testing.T,
	srv *TeamServer,
	fixture *principalFixture,
	jti, clientID string,
	body []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	assertion := signPrincipalAssertion(t, fixture.private, "active", nil, teamMemoryClaims(fixture, jti, clientID, body))
	return serveTeamMemoryRequest(srv.Handler(), assertion, "req-"+jti, body)
}

func serveTeamMemoryRequest(handler http.Handler, assertion, requestID string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, TeamMemoryRememberRoutePath, bytes.NewReader(body))
	request.RemoteAddr = "127.0.0.1:48000"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Pulse-Key", testTeamIPCSecret)
	request.Header.Set("X-Pulse-Principal", assertion)
	request.Header.Set("X-Pulse-Request-ID", requestID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeTeamMemoryResult(t *testing.T, response *httptest.ResponseRecorder) teamMemoryRememberResponse {
	t.Helper()
	var result teamMemoryRememberResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v (%q)", err, response.Body.String())
	}
	return result
}

func requirePendingProjectionJobs(t *testing.T, jobs []teamMemoryProjectionJobResponse) {
	t.Helper()
	if len(jobs) != 2 {
		t.Fatalf("projection jobs = %+v", jobs)
	}
	kinds := make([]string, 0, len(jobs))
	for _, job := range jobs {
		if !safeOpaque(job.JobID, 255) || job.State != "pending" {
			t.Fatalf("invalid projection job: %+v", job)
		}
		kinds = append(kinds, job.Kind)
	}
	if !sort.StringsAreSorted(kinds) || strings.Join(kinds, ",") != "embedding,event" {
		t.Fatalf("projection job kinds = %v", kinds)
	}
}

func requireTeamMemoryError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("error response = %d %q, want %d %q", response.Code, response.Body.String(), status, code)
	}
	var envelope struct {
		Error    string `json:"error"`
		Fallback *bool  `json:"fallback"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if envelope.Error != code || envelope.Fallback == nil || *envelope.Fallback {
		t.Fatalf("error envelope = %+v", envelope)
	}
	var exact map[string]any
	_ = json.Unmarshal(response.Body.Bytes(), &exact)
	if len(exact) != 2 {
		t.Fatalf("error envelope had extra fields: %v", exact)
	}
}

func countTeamMemoryRows(t *testing.T, database *store.Store) int {
	t.Helper()
	var count int
	if err := database.DB().QueryRow(`SELECT count(*) FROM team_memory_capsules`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func countTeamMemoryObjects(t *testing.T, database *store.Store) int {
	t.Helper()
	var count int
	if err := database.DB().QueryRow(`SELECT count(*) FROM team_object_registry WHERE object_kind = 'memory'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
