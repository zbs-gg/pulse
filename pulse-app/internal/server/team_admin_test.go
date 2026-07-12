package server

import (
	"context"
	"crypto/sha256"
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
)

func teamStatusBody(active string) []byte {
	return []byte(fmt.Sprintf(`{"schema":"pulse.team.status.v1","active_context":%s}`, active))
}

func teamInspectBody(objectID, active string) []byte {
	return []byte(fmt.Sprintf(
		`{"schema":"pulse.team.inspect.v1","object_id":%q,"active_context":%s}`,
		objectID, active,
	))
}

func teamAuditBody(active, cursor string, limit *int) []byte {
	envelope := map[string]any{
		"schema":         TeamAuditSchema,
		"active_context": json.RawMessage(active),
	}
	if cursor != "" {
		envelope["cursor"] = cursor
	}
	if limit != nil {
		envelope["limit"] = *limit
	}
	body, _ := json.Marshal(envelope)
	return body
}

func TestTeamAdminMetadataContractsAreClosedAndCanonical(t *testing.T) {
	status, err := decodeTeamStatusRequest(teamStatusBody(`{"project_id":"project-1","session_id":"session-1"}`))
	if err != nil || status.Schema != TeamStatusSchema ||
		status.ActiveContext.ProjectID != "project-1" || status.ActiveContext.SessionID != "session-1" {
		t.Fatalf("status = %+v, err=%v", status, err)
	}
	inspect, err := decodeTeamInspectRequest(teamInspectBody("root-inspect-1", `{"repo_id":"repo-1"}`))
	if err != nil || inspect.Schema != TeamInspectSchema || inspect.ObjectID != "root-inspect-1" ||
		inspect.ActiveContext.RepoID != "repo-1" {
		t.Fatalf("inspect = %+v, err=%v", inspect, err)
	}
	audit, err := decodeTeamAuditRequest(teamAuditBody(`{"agent_id":"agent-1"}`, "", nil))
	if err != nil || audit.Schema != TeamAuditSchema || audit.Cursor != "" || audit.Limit != 50 ||
		audit.ActiveContext.AgentID != "agent-1" {
		t.Fatalf("audit = %+v, err=%v", audit, err)
	}
	limit := 7
	audit, err = decodeTeamAuditRequest(teamAuditBody(`{}`, "audit_cursor_001", &limit))
	if err != nil || audit.Cursor != "audit_cursor_001" || audit.Limit != 7 {
		t.Fatalf("paged audit = %+v, err=%v", audit, err)
	}

	invalid := []struct {
		name string
		body []byte
		kind string
	}{
		{"status missing schema", []byte(`{"active_context":{}}`), "status"},
		{"status null context", []byte(`{"schema":"pulse.team.status.v1","active_context":null}`), "status"},
		{"status authority spoof", []byte(`{"schema":"pulse.team.status.v1","active_context":{},"principal_id":"spoofed"}`), "status"},
		{"inspect secret shaped id", teamInspectBody("secret-root", `{}`), "inspect"},
		{"inspect missing object", []byte(`{"schema":"pulse.team.inspect.v1","active_context":{}}`), "inspect"},
		{"inspect team spoof", []byte(`{"schema":"pulse.team.inspect.v1","object_id":"root-1","active_context":{"team_id":"spoofed"}}`), "inspect"},
		{"audit null cursor", []byte(`{"schema":"pulse.team.audit.v1","active_context":{},"cursor":null}`), "audit"},
		{"audit zero limit", []byte(`{"schema":"pulse.team.audit.v1","active_context":{},"limit":0}`), "audit"},
		{"audit broad actor", []byte(`{"schema":"pulse.team.audit.v1","active_context":{},"actor_principal_id":"other"}`), "audit"},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			var err error
			switch test.kind {
			case "status":
				_, err = decodeTeamStatusRequest(test.body)
			case "inspect":
				_, err = decodeTeamInspectRequest(test.body)
			case "audit":
				_, err = decodeTeamAuditRequest(test.body)
			}
			if err == nil {
				t.Fatalf("accepted %s: %s", test.kind, test.body)
			}
		})
	}
}

func TestOwnerCapabilityIsRecognizedWithoutCreatingAnOwnerMCPRoute(t *testing.T) {
	capabilities := []string{"pulse:connect", "pulse:owner", "pulse:status"}
	if !validCapabilities(capabilities) {
		t.Fatal("sorted owner capability set was rejected")
	}
	canonical, ok := sortedUniqueTeamCapabilities(capabilities)
	if !ok || !reflect.DeepEqual(canonical, capabilities) {
		t.Fatalf("canonical owner capabilities = %v, ok=%v", canonical, ok)
	}
	if isExactTeamDomainPath(OwnerApprovalRoutePath) {
		t.Fatal("Owner administration route entered the ordinary MCP domain assertion allowlist")
	}
}

func TestTeamStatusRouteReturnsOnlyServerDerivedPrincipalState(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	body := teamStatusBody(`{"session_id":"session-status"}`)
	response := serveSignedTeamMetadata(
		t, srv, fixture, "status-route", TeamStatusRoutePath, body,
		[]string{"pulse:connect", "pulse:status"},
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d %q", response.Code, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	wantFields := []string{
		"active_context", "agent_binding_id", "degraded", "degraded_reasons",
		"effective_capabilities", "fallback", "human_principal_id", "membership_id",
		"membership_role", "mode", "policy_version", "principal_id", "principal_kind",
		"projection_state", "schema", "store_id", "team_id",
	}
	if got := sortedAdminMapKeys(result); !reflect.DeepEqual(got, wantFields) {
		t.Fatalf("status fields = %v", got)
	}
	if result["schema"] != TeamStatusResultSchema || result["mode"] != "team-remote" ||
		result["team_id"] != fixture.teamID || result["store_id"] != fixture.storeID ||
		result["principal_id"] != fixture.agentID || result["principal_kind"] != "agent" ||
		result["human_principal_id"] != fixture.ownerID || result["agent_binding_id"] != fixture.bindingID ||
		result["membership_role"] != "owner" || result["projection_state"] != "ready" ||
		result["degraded"] != false || !reflect.DeepEqual(result["degraded_reasons"], []any{}) ||
		result["fallback"] != false {
		t.Fatalf("status result = %v", result)
	}
	context, ok := result["active_context"].(map[string]any)
	if !ok || !reflect.DeepEqual(context, map[string]any{"session_id": "session-status"}) {
		t.Fatalf("active context = %#v", result["active_context"])
	}
	if got := result["effective_capabilities"]; !reflect.DeepEqual(got, []any{"pulse:connect", "pulse:status"}) {
		t.Fatalf("capabilities = %#v", got)
	}
}

func TestTeamStatusBecomesReadyOnlyAfterExplicitSyntheticActivation(t *testing.T) {
	teamServer, fixture, lease := newInactiveTeamServer(t)
	ready := serveTeamRequest(
		teamServer.Handler(), http.MethodGet, teamReadinessRoutePath,
		testTeamIPCSecret, "127.0.0.1:57500", nil,
	)
	if ready.Code != http.StatusServiceUnavailable ||
		ready.Body.String() != "{\"error\":\"team_not_ready\",\"fallback\":false}\n" {
		t.Fatalf("inactive readiness = %d %q", ready.Code, ready.Body.String())
	}
	blockedBody := teamStatusBody(`{}`)
	blocked := serveSignedTeamMetadata(
		t, teamServer, fixture, "status-before-activation", TeamStatusRoutePath, blockedBody,
		[]string{"pulse:connect", "pulse:status"},
	)
	if blocked.Code != http.StatusServiceUnavailable ||
		blocked.Body.String() != "{\"error\":\"shared_memory_unavailable\",\"fallback\":false}\n" {
		t.Fatalf("inactive status = %d %q", blocked.Code, blocked.Body.String())
	}
	stepUp := newOwnerStepUpVerifierFixture(t, fixture)
	ownerServer, err := NewOwnerAdminServer(OwnerAdminServerConfig{
		IPCSecret: testTeamIPCSecret, Store: fixture.store, StepUpVerifier: stepUp,
		WriterLease: &lease, Clock: func() time.Time { return fixture.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	gate := fmt.Sprintf("%x", sha256.Sum256([]byte("status-ready-synthetic-gates")))
	approvalBody := activationOwnerApprovalBody(fixture, gate)
	assertion := signOwnerApprovalAssertion(
		t, fixture, "status-activation", approvalBody,
		store.OwnerActionSyntheticActivate, fixture.storeID, fixture.teamID,
	)
	approved := serveOwnerAdminRequest(
		ownerServer.Handler(), OwnerApprovalRoutePath, approvalBody, "req-status-activation", assertion,
	)
	if approved.Code != http.StatusOK {
		t.Fatalf("status activation approval = %d %q", approved.Code, approved.Body.String())
	}
	var approval map[string]any
	if err := json.Unmarshal(approved.Body.Bytes(), &approval); err != nil {
		t.Fatal(err)
	}
	activateBody := []byte(fmt.Sprintf(
		`{"schema":"pulse.team.owner.activate.v1","approval_nonce":%q,"gate_digest":%q}`,
		approval["approval_nonce"], gate,
	))
	activated := serveOwnerAdminRequest(
		ownerServer.Handler(), OwnerActivateRoutePath, activateBody, "req-status-activate", "",
	)
	if activated.Code != http.StatusOK {
		t.Fatalf("status activate = %d %q", activated.Code, activated.Body.String())
	}

	body := teamStatusBody(`{}`)
	status := serveSignedTeamMetadata(
		t, teamServer, fixture, "status-after-activation", TeamStatusRoutePath, body,
		[]string{"pulse:connect", "pulse:status"},
	)
	if status.Code != http.StatusOK {
		t.Fatalf("status after activation = %d %q", status.Code, status.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(status.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["projection_state"] != "ready" || result["degraded"] != false ||
		!reflect.DeepEqual(result["degraded_reasons"], []any{}) || result["fallback"] != false {
		t.Fatalf("activated status = %v", result)
	}
}

func TestTeamInspectRouteReturnsOnlyAuthorizedObjectMetadata(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	insertServerDeletionRoot(t, fixture, "root-inspect-visible", fixture.ownerID)
	body := teamInspectBody("root-inspect-visible", `{"session_id":"session-inspect"}`)
	response := serveSignedTeamMetadata(
		t, srv, fixture, "inspect-visible", TeamInspectRoutePath, body,
		[]string{"pulse:connect", "pulse:read"},
	)
	if response.Code != http.StatusOK {
		t.Fatalf("inspect = %d %q", response.Code, response.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	wantFields := []string{
		"author_principal_id", "created_at", "deletion_state", "fallback", "generation",
		"lifecycle_state", "object_id", "object_kind", "privacy_tier", "projection_state",
		"retention", "schema", "scope",
	}
	if got := sortedAdminMapKeys(result); !reflect.DeepEqual(got, wantFields) {
		t.Fatalf("inspect fields = %v", got)
	}
	if result["schema"] != TeamInspectResultSchema || result["object_id"] != "root-inspect-visible" ||
		result["object_kind"] != "memory" || result["author_principal_id"] != fixture.ownerID ||
		result["created_at"] != "2026-07-11T05:00:00Z" || result["privacy_tier"] != "normal" ||
		result["retention"] != "project" || result["lifecycle_state"] != "active" ||
		result["generation"] != float64(1) || result["deletion_state"] != "none" ||
		result["fallback"] != false {
		t.Fatalf("inspect result = %v", result)
	}
	scope, ok := result["scope"].(map[string]any)
	if !ok || scope["type"] != "personal" || scope["id"] != fixture.ownerID ||
		scope["owner_principal_id"] != fixture.ownerID {
		t.Fatalf("inspect scope = %#v", result["scope"])
	}
}

func TestTeamInspectConcealsAbsentAndUnauthorizedWithIdenticalBytes(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	other, err := fixture.store.AddTeamMember(context.Background(), store.AddTeamMemberRequest{
		ActorPrincipalID: fixture.ownerID, Issuer: fixture.issuer,
		Subject: "inspect-other", Role: "member",
	})
	if err != nil {
		t.Fatal(err)
	}
	insertServerDeletionRoot(t, fixture, "root-inspect-hidden", other.PrincipalID)
	absent := serveSignedTeamMetadata(
		t, srv, fixture, "inspect-absent", TeamInspectRoutePath,
		teamInspectBody("root-inspect-absent", `{}`), []string{"pulse:connect", "pulse:read"},
	)
	hidden := serveSignedTeamMetadata(
		t, srv, fixture, "inspect-hidden", TeamInspectRoutePath,
		teamInspectBody("root-inspect-hidden", `{}`), []string{"pulse:connect", "pulse:read"},
	)
	if absent.Code != http.StatusNotFound || hidden.Code != http.StatusNotFound ||
		absent.Body.String() != hidden.Body.String() ||
		absent.Body.String() != "{\"error\":\"concealed_not_found\",\"fallback\":false}\n" {
		t.Fatalf("inspect concealment absent=%d %q hidden=%d %q",
			absent.Code, absent.Body.String(), hidden.Code, hidden.Body.String())
	}
}

func TestTeamAuditRouteReturnsOnlyCurrentPrincipalMetadata(t *testing.T) {
	srv, fixture, _ := newReadyTeamServer(t)
	body := personalTeamGraphBody(t, fixture, "graph-audit-own-0001")
	stored := serveSignedTeamGraph(t, srv, fixture, "graph-audit-own", "agent-client", body)
	if stored.Code != http.StatusOK {
		t.Fatalf("seed graph = %d %q", stored.Code, stored.Body.String())
	}
	audit := serveSignedTeamMetadata(
		t, srv, fixture, "audit-own", TeamAuditRoutePath,
		teamAuditBody(`{}`, "", nil), []string{"pulse:audit", "pulse:connect"},
	)
	if audit.Code != http.StatusOK {
		t.Fatalf("audit = %d %q", audit.Code, audit.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(audit.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if got := sortedAdminMapKeys(result); !reflect.DeepEqual(got, []string{
		"events", "fallback", "own_actions_only", "schema",
	}) {
		t.Fatalf("audit fields = %v", got)
	}
	if result["schema"] != TeamAuditResultSchema || result["own_actions_only"] != true ||
		result["fallback"] != false {
		t.Fatalf("audit result = %v", result)
	}
	events, ok := result["events"].([]any)
	if !ok || len(events) != 1 {
		t.Fatalf("audit events = %#v", result["events"])
	}
	event, ok := events[0].(map[string]any)
	if !ok || event["actor_principal_id"] != fixture.agentID ||
		event["action"] != "team.object.write" || event["outcome"] != "allowed" ||
		event["team_id"] != fixture.teamID || event["mode"] != "team-remote" {
		t.Fatalf("audit event = %#v", events[0])
	}
	wantEventFields := []string{
		"action", "actor_principal_id", "client_key", "event_id", "mode", "occurred_at",
		"outcome", "policy_version", "project_id", "reason_code", "request_id", "target_id",
		"target_kind", "team_id",
	}
	if got := sortedAdminMapKeys(event); !reflect.DeepEqual(got, wantEventFields) {
		t.Fatalf("audit event fields = %v", got)
	}
	if raw := audit.Body.String(); containsAny(raw,
		"Graph fact", "Graph event", "graph-audit-own-0001", fixture.subject, fixture.issuer,
	) {
		t.Fatalf("audit leaked content or external identity: %q", raw)
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func serveSignedTeamMetadata(
	t *testing.T,
	srv *TeamServer,
	fixture *principalFixture,
	jti, path string,
	body []byte,
	capabilities []string,
) *httptest.ResponseRecorder {
	t.Helper()
	claims := fixture.claims(jti, "agent-client", body)
	claims["path"] = path
	claims["capabilities"] = capabilities
	assertion := signPrincipalAssertion(t, fixture.private, "active", nil, claims)
	return serveTeamGraphRequest(srv.Handler(), assertion, "req-"+jti, bodyPathRequest{
		path: path, body: body,
	})
}

func sortedAdminMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
