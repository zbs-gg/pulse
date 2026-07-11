package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamauth"
)

const testTeamIPCSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newReadyTeamServer(t *testing.T) (*TeamServer, *principalFixture, store.TeamWriterLease) {
	t.Helper()
	f := newPrincipalFixture(t)
	lease, err := f.store.AcquireTeamWriterLease(context.Background(), store.TeamWriterLeaseRequest{
		WriterID: "team-daemon-router-test", WriterVersion: teamauth.SchemaVersion, TTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("acquire writer lease: %v", err)
	}
	verifier, err := NewPrincipalVerifier(PrincipalVerifierConfig{
		Store: f.store,
		Keyring: PrincipalVerifyKeyring{ActiveKid: "active", Keys: map[string]ed25519.PublicKey{
			"active": f.private.Public().(ed25519.PublicKey),
		}},
		ExpectedStoreID: f.storeID, ExpectedTeamID: f.teamID,
		Clock: func() time.Time { return f.now }, WriterLease: &lease,
	})
	if err != nil {
		t.Fatalf("lease-bound verifier: %v", err)
	}
	f.verifier = verifier
	srv, err := NewTeam(TeamServerConfig{
		IPCSecret: testTeamIPCSecret, Store: f.store, PrincipalVerifier: f.verifier,
		ExpectedStoreID: f.storeID, ExpectedTeamID: f.teamID, WriterLease: lease,
	})
	if err != nil {
		t.Fatalf("NewTeam: %v", err)
	}
	return srv, f, lease
}

func serveTeamRequest(handler http.Handler, method, path, key, remoteAddr string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.RemoteAddr = remoteAddr
	if key != "" {
		req.Header.Set("X-Pulse-Key", key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestTeamRouterIsExactAllowlistAndLocalHandlerIsUnchanged(t *testing.T) {
	srv, _, _ := newReadyTeamServer(t)
	handler := srv.Handler()

	health := serveTeamRequest(handler, http.MethodGet, "/health", testTeamIPCSecret, "127.0.0.1:41000", nil)
	if health.Code != http.StatusOK || strings.TrimSpace(health.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("team health = %d %q", health.Code, health.Body.String())
	}
	ready := serveTeamRequest(handler, http.MethodGet, "/ready", testTeamIPCSecret, "127.0.0.1:41000", nil)
	if ready.Code != http.StatusOK || strings.TrimSpace(ready.Body.String()) != `{"status":"ready","mode":"team-remote","fallback":false}` {
		t.Fatalf("team readiness = %d %q", ready.Code, ready.Body.String())
	}

	legacyPaths := []string{
		"/assets/anime.min.js", "/health/snapshot", "/outbox", "/outbox/ack", "/msg", "/ingest",
		"/memory/remember", "/memory/recall", "/memory/status", "/memory/export", "/memory/import",
		"/memory/delete", "/memory/wipe", "/memory/consolidate", "/graph/delta", "/graph/export",
		"/continuity/resume", "/continuity/checkpoint", "/continuity/observe", "/viewer", "/viewer/data",
		"/retrieve", "/context/query", "/feed_signals", "/team/v1/wipe", "/team/v1/unknown",
	}
	for _, path := range legacyPaths {
		response := serveTeamRequest(handler, http.MethodPost, path, testTeamIPCSecret, "127.0.0.1:41000", []byte(`{}`))
		if response.Code != http.StatusNotFound {
			t.Errorf("team route %s = %d, want 404", path, response.Code)
		}
	}

	for _, path := range []string{PrincipalCheckRoutePath, SecurityEventRoutePath, TeamGraphDeltaRoutePath} {
		response := serveTeamRequest(handler, http.MethodPost, path, testTeamIPCSecret, "127.0.0.1:41000", []byte(`{}`))
		if response.Code == http.StatusNotFound {
			t.Errorf("versioned team route %s was not registered", path)
		}
	}

	local, err := New(Config{IPCSecret: "local-secret"})
	if err != nil {
		t.Fatal(err)
	}
	localHealth := serveTeamRequest(local.Handler(), http.MethodGet, "/health", "local-secret", "127.0.0.1:41000", nil)
	if localHealth.Code != http.StatusOK || !strings.Contains(localHealth.Body.String(), "uptime_seconds") {
		t.Fatalf("local health behavior changed: %d %q", localHealth.Code, localHealth.Body.String())
	}
	localTeamRoute := serveTeamRequest(local.Handler(), http.MethodPost, PrincipalCheckRoutePath, "local-secret", "127.0.0.1:41000", []byte(`{}`))
	if localTeamRoute.Code != http.StatusNotFound {
		t.Fatalf("local handler exposed team route: %d", localTeamRoute.Code)
	}
}

func TestTeamRouterRequiresLoopbackAndExactlyOneIPCSecret(t *testing.T) {
	srv, _, _ := newReadyTeamServer(t)
	handler := srv.Handler()

	for _, tc := range []struct {
		name, key, remote string
	}{
		{name: "missing key", remote: "127.0.0.1:42000"},
		{name: "wrong key", key: "wrong", remote: "127.0.0.1:42000"},
		{name: "remote peer", key: testTeamIPCSecret, remote: "198.51.100.7:42000"},
		{name: "missing peer", key: testTeamIPCSecret, remote: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := serveTeamRequest(handler, http.MethodGet, "/health", tc.key, tc.remote, nil)
			if response.Code != http.StatusUnauthorized || strings.TrimSpace(response.Body.String()) != `{"error":"unauthorized"}` {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "127.0.0.1:42000"
	req.Header.Add("X-Pulse-Key", testTeamIPCSecret)
	req.Header.Add("X-Pulse-Key", testTeamIPCSecret)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate IPC key accepted: %d", recorder.Code)
	}
}

func TestTeamRouterRechecksReadinessBeforeVersionedRoutes(t *testing.T) {
	srv, f, _ := newReadyTeamServer(t)
	if _, err := f.store.DB().Exec(`UPDATE team_writer_leases SET expires_at = '2020-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}

	ready := serveTeamRequest(srv.Handler(), http.MethodGet, "/ready", testTeamIPCSecret, "127.0.0.1:43000", nil)
	if ready.Code != http.StatusServiceUnavailable || strings.TrimSpace(ready.Body.String()) != `{"error":"team_not_ready","fallback":false}` {
		t.Fatalf("readiness failure = %d %q", ready.Code, ready.Body.String())
	}
	principal := serveTeamRequest(srv.Handler(), http.MethodPost, PrincipalCheckRoutePath, testTeamIPCSecret, "127.0.0.1:43000", []byte(`{}`))
	if principal.Code != http.StatusServiceUnavailable || strings.TrimSpace(principal.Body.String()) != `{"error":"principal_store_unavailable"}` {
		t.Fatalf("principal readiness gate = %d %q", principal.Code, principal.Body.String())
	}
	security := serveTeamRequest(srv.Handler(), http.MethodPost, SecurityEventRoutePath, testTeamIPCSecret, "127.0.0.1:43000", []byte(`{}`))
	if security.Code != http.StatusServiceUnavailable || strings.TrimSpace(security.Body.String()) != `{"error":"shared_memory_unavailable","fallback":false}` {
		t.Fatalf("security readiness gate = %d %q", security.Code, security.Body.String())
	}
	health := serveTeamRequest(srv.Handler(), http.MethodGet, "/health", testTeamIPCSecret, "127.0.0.1:43000", nil)
	if health.Code != http.StatusOK {
		t.Fatalf("liveness must survive readiness failure: %d", health.Code)
	}
}

func TestTeamRouterWiresPrincipalAndDurableSecurityHandlers(t *testing.T) {
	srv, f, _ := newReadyTeamServer(t)
	handler := srv.Handler()

	body := f.requestBody("agent-client")
	assertion := signPrincipalAssertion(t, f.private, "active", nil, f.claims("router-principal", "agent-client", body))
	req := httptest.NewRequest(http.MethodPost, PrincipalCheckRoutePath, bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:44000"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Pulse-Key", testTeamIPCSecret)
	req.Header.Set("X-Pulse-Principal", assertion)
	req.Header.Set("X-Pulse-Request-ID", "req-router-principal")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("principal route = %d %q", recorder.Code, recorder.Body.String())
	}

	event := SecurityEvent{
		EventType: SecurityEventTypeAuthenticationDenied, ReasonCode: SecurityEventReasonExpiredCredential,
		MethodClass: SecurityEventMethodRead, PathClass: SecurityEventPathMCP,
		RequestID: "req-router-event", Count: 3,
	}
	eventBody, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	eventAssertion := signPrincipalAssertion(t, f.private, "active", map[string]any{"typ": securityEventAssertionVersion}, f.gatewayEventClaims("router-event", eventBody))
	eventReq := httptest.NewRequest(http.MethodPost, SecurityEventRoutePath, bytes.NewReader(eventBody))
	eventReq.RemoteAddr = "127.0.0.1:44000"
	eventReq.Header.Set("Content-Type", "application/json")
	eventReq.Header.Set("X-Pulse-Key", testTeamIPCSecret)
	eventReq.Header.Set("X-Pulse-Gateway-Assertion", eventAssertion)
	eventReq.Header.Set("X-Pulse-Request-ID", event.RequestID)
	eventRecorder := httptest.NewRecorder()
	handler.ServeHTTP(eventRecorder, eventReq)
	if eventRecorder.Code != http.StatusNoContent {
		t.Fatalf("security route = %d %q", eventRecorder.Code, eventRecorder.Body.String())
	}

	var eventType, reason, methodClass, pathClass, requestID, metadata string
	var count int
	if err := f.store.DB().QueryRow(`
		SELECT event_type, reason_code, method_class, path_class, request_id, aggregate_count, metadata_json
		  FROM team_security_events WHERE request_id = ?`, event.RequestID).
		Scan(&eventType, &reason, &methodClass, &pathClass, &requestID, &count, &metadata); err != nil {
		t.Fatalf("security event was not durable: %v", err)
	}
	if eventType != string(event.EventType) || reason != string(event.ReasonCode) || methodClass != string(event.MethodClass) ||
		pathClass != string(event.PathClass) || requestID != event.RequestID || count != int(event.Count) || metadata != "{}" {
		t.Fatalf("stored security event mismatch: %q %q %q %q %q %d %q", eventType, reason, methodClass, pathClass, requestID, count, metadata)
	}
}

func TestNewTeamRejectsIncompleteOrNotReadyConfiguration(t *testing.T) {
	if _, err := NewTeam(TeamServerConfig{}); err == nil {
		t.Fatal("NewTeam accepted empty configuration")
	}
	f := newPrincipalFixture(t)
	if _, err := NewTeam(TeamServerConfig{
		IPCSecret: testTeamIPCSecret, Store: f.store, PrincipalVerifier: f.verifier,
		ExpectedStoreID: f.storeID, ExpectedTeamID: f.teamID,
	}); err == nil {
		t.Fatal("NewTeam accepted a store without the active writer lease")
	}
}

func TestNewTeamRejectsVerifierNotBoundToItsWriterLease(t *testing.T) {
	f := newPrincipalFixture(t)
	lease, err := f.store.AcquireTeamWriterLease(context.Background(), store.TeamWriterLeaseRequest{
		WriterID: "unbound-verifier", WriterVersion: teamauth.SchemaVersion, TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewTeam(TeamServerConfig{
		IPCSecret: testTeamIPCSecret, Store: f.store, PrincipalVerifier: f.verifier,
		ExpectedStoreID: f.storeID, ExpectedTeamID: f.teamID, WriterLease: lease,
	}); err == nil {
		t.Fatal("NewTeam accepted a verifier that was not bound to its writer lease")
	}
}

func TestNewTeamRejectsWeakIPCSecretEvenWithValidStoreAndLease(t *testing.T) {
	_, f, lease := newReadyTeamServer(t)
	if _, err := NewTeam(TeamServerConfig{
		IPCSecret: "x", Store: f.store, PrincipalVerifier: f.verifier,
		ExpectedStoreID: f.storeID, ExpectedTeamID: f.teamID, WriterLease: lease,
	}); err == nil {
		t.Fatal("NewTeam accepted a weak IPC secret")
	}
}

func TestTeamSecurityEventWriteRechecksWriterLeaseInsideWriteBoundary(t *testing.T) {
	srv, f, _ := newReadyTeamServer(t)
	if _, err := f.store.DB().Exec(`UPDATE team_writer_leases SET expires_at = '2020-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	event := SecurityEvent{
		EventType: SecurityEventTypeAuthenticationDenied, ReasonCode: SecurityEventReasonExpiredCredential,
		MethodClass: SecurityEventMethodRead, PathClass: SecurityEventPathMCP,
		RequestID: "req-stale-writer", Count: 1,
	}
	handler, ok := srv.securityEventHandler.(*securityEventHandler)
	if !ok {
		t.Fatal("team security handler has unexpected type")
	}
	if err := handler.storage.AppendSecurityEvent(context.Background(), event); err == nil {
		t.Fatal("security event write accepted an expired writer lease")
	}
	var count int
	if err := f.store.DB().QueryRow(`SELECT count(*) FROM team_security_events WHERE request_id = ?`, event.RequestID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stale writer persisted %d security events", count)
	}
}

func TestTeamPrincipalReplayWriteRechecksWriterLeaseInsideWriteBoundary(t *testing.T) {
	srv, f, _ := newReadyTeamServer(t)
	if _, err := f.store.DB().Exec(`UPDATE team_writer_leases SET expires_at = '2020-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	body := f.requestBody("agent-client")
	assertion := signPrincipalAssertion(t, f.private, "active", nil, f.claims("stale-writer-principal", "agent-client", body))
	_, err := srv.cfg.PrincipalVerifier.VerifyRequest(
		context.Background(), assertion, "req-stale-writer-principal",
		http.MethodPost, PrincipalCheckRoutePath, body,
	)
	if !errors.Is(err, ErrPrincipalStoreUnavailable) {
		t.Fatalf("principal verification with expired writer lease error = %v", err)
	}
	var count int
	if err := f.store.DB().QueryRow(`SELECT count(*) FROM team_assertion_replay`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stale writer persisted %d assertion replay rows", count)
	}
}

func TestTeamGatewayReplayWriteRechecksWriterLeaseInsideWriteBoundary(t *testing.T) {
	srv, f, _ := newReadyTeamServer(t)
	if _, err := f.store.DB().Exec(`UPDATE team_writer_leases SET expires_at = '2020-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	event := SecurityEvent{
		EventType: SecurityEventTypeAuthenticationDenied, ReasonCode: SecurityEventReasonExpiredCredential,
		MethodClass: SecurityEventMethodRead, PathClass: SecurityEventPathMCP,
		RequestID: "req-stale-writer-gateway", Count: 1,
	}
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	assertion := signPrincipalAssertion(
		t, f.private, "active", map[string]any{"typ": securityEventAssertionVersion},
		f.gatewayEventClaims("stale-writer-gateway", body),
	)
	err = srv.cfg.PrincipalVerifier.VerifyGatewayEvent(
		context.Background(), assertion, event.RequestID,
		http.MethodPost, SecurityEventRoutePath, body,
	)
	if !errors.Is(err, ErrPrincipalStoreUnavailable) {
		t.Fatalf("gateway verification with expired writer lease error = %v", err)
	}
	var count int
	if err := f.store.DB().QueryRow(`SELECT count(*) FROM team_assertion_replay`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stale writer persisted %d gateway replay rows", count)
	}
}
