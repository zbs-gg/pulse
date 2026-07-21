package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

type recordingSecurityEventStore struct {
	mu     sync.Mutex
	events []SecurityEvent
	err    error
}

var testSecurityEventGatewayVerifier = SecurityEventGatewayVerifierFunc(
	func(context.Context, string, string, string, string, []byte) error { return nil },
)

func securityEventTestOptions(options SecurityEventHandlerOptions) SecurityEventHandlerOptions {
	options.GatewayVerifier = testSecurityEventGatewayVerifier
	return options
}

type failingFirstSecurityEventStore struct {
	mu      sync.Mutex
	calls   int
	events  []SecurityEvent
	started chan struct{}
	release chan struct{}
}

func (s *failingFirstSecurityEventStore) AppendSecurityEvent(_ context.Context, event SecurityEvent) error {
	s.mu.Lock()
	s.calls++
	call := s.calls
	if call == 1 {
		close(s.started)
	}
	s.mu.Unlock()
	if call == 1 {
		<-s.release
		return errors.New("synthetic first append failure")
	}
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
	return nil
}

func (s *failingFirstSecurityEventStore) snapshot() (int, []SecurityEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, append([]SecurityEvent(nil), s.events...)
}

func (s *recordingSecurityEventStore) AppendSecurityEvent(_ context.Context, event SecurityEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, event)
	return s.err
}

func (s *recordingSecurityEventStore) snapshot() []SecurityEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SecurityEvent(nil), s.events...)
}

func (s *recordingSecurityEventStore) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func TestSecurityEventHandlerStoresOnlyFixedSchema(t *testing.T) {
	store := &recordingSecurityEventStore{}
	handler, err := NewSecurityEventHandler(store, securityEventTestOptions(SecurityEventHandlerOptions{
		Now:               func() time.Time { return time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC) },
		Window:            time.Minute,
		MaxCountPerWindow: 10,
		MaxDedupeEntries:  10,
	}))
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}

	want := SecurityEvent{
		EventType:   SecurityEventTypeAuthenticationDenied,
		ReasonCode:  SecurityEventReasonExpiredCredential,
		MethodClass: SecurityEventMethodWrite,
		PathClass:   SecurityEventPathMCP,
		RequestID:   "request-1234567890abcdef",
		Count:       2,
	}
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, SecurityEventRoutePath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Pulse-Gateway-Assertion", "test-gateway-assertion")
	req.Header.Set("X-Pulse-Request-ID", want.RequestID)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	got := store.snapshot()
	if !reflect.DeepEqual(got, []SecurityEvent{want}) {
		t.Fatalf("stored events = %#v, want %#v", got, []SecurityEvent{want})
	}

	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"count", "event_type", "method_class", "path_class", "reason_code", "request_id"}
	gotKeys := make([]string, 0, len(wire))
	for key := range wire {
		gotKeys = append(gotKeys, key)
	}
	slicesSort(gotKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("wire keys = %v, want fixed schema %v", gotKeys, wantKeys)
	}
}

func slicesSort(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func TestSecurityEventHandlerRejectsUnknownAndForbiddenFields(t *testing.T) {
	valid := map[string]any{
		"event_type":   "authentication_denied",
		"reason_code":  "expired_credential",
		"method_class": "write",
		"path_class":   "mcp",
		"request_id":   "request-1234567890abcdef",
		"count":        1,
	}
	for _, field := range []string{
		"token", "authorization", "header", "subject", "client", "body",
		"error", "prompt", "content", "claims", "metadata",
	} {
		t.Run(field, func(t *testing.T) {
			store := &recordingSecurityEventStore{}
			handler, err := NewSecurityEventHandler(store, securityEventTestOptions(SecurityEventHandlerOptions{}))
			if err != nil {
				t.Fatal(err)
			}
			payload := make(map[string]any, len(valid)+1)
			for key, value := range valid {
				payload[key] = value
			}
			payload[field] = "must-not-cross"
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}

			rec := serveSecurityEvent(handler, body, "application/json")

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if len(store.snapshot()) != 0 {
				t.Fatal("unknown field reached security event storage")
			}
			if bytes.Contains(rec.Body.Bytes(), []byte("must-not-cross")) {
				t.Fatal("response reflected rejected content")
			}
		})
	}
}

func TestSecurityEventIngressContractAndBodyLimit(t *testing.T) {
	if SecurityEventRoutePath != "/team/v1/security-events" {
		t.Fatalf("route = %q, want locked team ingress route", SecurityEventRoutePath)
	}
	if SecurityEventMaxBodyBytes != 4*1024 {
		t.Fatalf("body limit = %d, want 4096", SecurityEventMaxBodyBytes)
	}

	store := &recordingSecurityEventStore{}
	handler, err := NewSecurityEventHandler(store, securityEventTestOptions(SecurityEventHandlerOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"event_type":"authentication_denied","reason_code":"expired_credential","method_class":"write","path_class":"mcp","request_id":"` +
		string(bytes.Repeat([]byte("a"), 4*1024)) + `","count":1}`)
	rec := serveSecurityEvent(handler, body, "application/json")

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
	if len(store.snapshot()) != 0 {
		t.Fatal("oversized event reached storage")
	}
}

func TestSecurityEventHandlerEnforcesHTTPEnvelope(t *testing.T) {
	validBody := []byte(`{"event_type":"authentication_denied","reason_code":"expired_credential","method_class":"write","path_class":"mcp","request_id":"request-1234567890abcdef","count":1}`)
	tests := []struct {
		name        string
		method      string
		contentType string
		body        []byte
		wantStatus  int
		wantAllow   string
	}{
		{name: "post only", method: http.MethodGet, contentType: "application/json", body: validBody, wantStatus: http.StatusMethodNotAllowed, wantAllow: http.MethodPost},
		{name: "content type required", method: http.MethodPost, body: validBody, wantStatus: http.StatusUnsupportedMediaType},
		{name: "json only", method: http.MethodPost, contentType: "text/plain", body: validBody, wantStatus: http.StatusUnsupportedMediaType},
		{name: "trailing JSON rejected", method: http.MethodPost, contentType: "application/json", body: append(append([]byte(nil), validBody...), []byte(` {}`)...), wantStatus: http.StatusBadRequest},
		{name: "json parameters allowed", method: http.MethodPost, contentType: "application/json; charset=utf-8", body: validBody, wantStatus: http.StatusNoContent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &recordingSecurityEventStore{}
			handler, err := NewSecurityEventHandler(store, securityEventTestOptions(SecurityEventHandlerOptions{}))
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(tc.method, SecurityEventRoutePath, bytes.NewReader(tc.body))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			req.Header.Set("X-Pulse-Gateway-Assertion", "test-gateway-assertion")
			req.Header.Set("X-Pulse-Request-ID", "request-1234567890abcdef")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%q", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := rec.Header().Get("Allow"); got != tc.wantAllow {
				t.Fatalf("Allow = %q, want %q", got, tc.wantAllow)
			}
			wantStored := 0
			if tc.wantStatus == http.StatusNoContent {
				wantStored = 1
			}
			if got := len(store.snapshot()); got != wantStored {
				t.Fatalf("stored events = %d, want %d", got, wantStored)
			}
		})
	}
}

func TestSecurityEventHandlerAcceptsOnlyFixedClassifications(t *testing.T) {
	accepted := []SecurityEvent{
		{
			EventType: SecurityEventTypeAuthenticationDenied, ReasonCode: SecurityEventReasonExpiredCredential,
			MethodClass: SecurityEventMethodWrite, PathClass: SecurityEventPathMCP,
		},
		{
			EventType: SecurityEventTypeAuthorizationDenied, ReasonCode: SecurityEventReasonInsufficientScope,
			MethodClass: SecurityEventMethodRead, PathClass: SecurityEventPathTeamAPI,
		},
		{
			EventType: SecurityEventTypeAuthorizationDenied, ReasonCode: SecurityEventReasonPrincipalRevoked,
			MethodClass: SecurityEventMethodOther, PathClass: SecurityEventPathPrincipal,
		},
		{
			EventType: SecurityEventTypeAuthorizationDenied, ReasonCode: SecurityEventReasonPolicyDenied,
			MethodClass: SecurityEventMethodWrite, PathClass: SecurityEventPathMCP,
		},
		{
			EventType: SecurityEventTypePrincipalAssertionDenied, ReasonCode: SecurityEventReasonAssertionReplayed,
			MethodClass: SecurityEventMethodDelete, PathClass: SecurityEventPathPrincipal,
		},
		{
			EventType: SecurityEventTypeOperationDenied, ReasonCode: SecurityEventReasonInvalidContract,
			MethodClass: SecurityEventMethodWrite, PathClass: SecurityEventPathMCP,
		},
		{
			EventType: SecurityEventTypeAuditDegraded, ReasonCode: SecurityEventReasonStoreUnavailable,
			MethodClass: SecurityEventMethodOther, PathClass: SecurityEventPathUnknown,
		},
	}
	for i := range accepted {
		accepted[i].RequestID = "request-1234567890abcdef"
		accepted[i].Count = 1
		store := &recordingSecurityEventStore{}
		handler, err := NewSecurityEventHandler(store, securityEventTestOptions(SecurityEventHandlerOptions{}))
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(accepted[i])
		if err != nil {
			t.Fatal(err)
		}
		rec := serveSecurityEvent(handler, body, "application/json")
		if rec.Code != http.StatusNoContent {
			t.Fatalf("classification %d status = %d; body=%q", i, rec.Code, rec.Body.String())
		}
	}

	base := accepted[0]
	tests := []struct {
		name   string
		mutate func(*SecurityEvent)
	}{
		{name: "event type", mutate: func(event *SecurityEvent) { event.EventType = "credential_with_full_claims" }},
		{name: "reason code", mutate: func(event *SecurityEvent) { event.ReasonCode = "raw_error_text" }},
		{name: "reason type mismatch", mutate: func(event *SecurityEvent) { event.ReasonCode = SecurityEventReasonAssertionReplayed }},
		{name: "method class", mutate: func(event *SecurityEvent) { event.MethodClass = "POST /raw/path" }},
		{name: "path class", mutate: func(event *SecurityEvent) { event.PathClass = "/team/v1/private" }},
		{name: "short request correlation", mutate: func(event *SecurityEvent) { event.RequestID = "short" }},
		{name: "unsafe request correlation", mutate: func(event *SecurityEvent) { event.RequestID = "request.token/value" }},
		{name: "zero count", mutate: func(event *SecurityEvent) { event.Count = 0 }},
		{name: "count cap", mutate: func(event *SecurityEvent) { event.Count = SecurityEventMaxCount + 1 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			event := base
			tc.mutate(&event)
			body, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			store := &recordingSecurityEventStore{}
			handler, err := NewSecurityEventHandler(store, securityEventTestOptions(SecurityEventHandlerOptions{}))
			if err != nil {
				t.Fatal(err)
			}
			rec := serveSecurityEvent(handler, body, "application/json")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%q", rec.Code, rec.Body.String())
			}
			if len(store.snapshot()) != 0 {
				t.Fatal("invalid classification reached storage")
			}
		})
	}
}

func TestSecurityEventHandlerDeduplicatesAndRateLimitsWithinBoundedWindow(t *testing.T) {
	now := time.Date(2026, 7, 11, 1, 0, 0, 0, time.UTC)
	store := &recordingSecurityEventStore{}
	handler, err := NewSecurityEventHandler(store, securityEventTestOptions(SecurityEventHandlerOptions{
		Now:               func() time.Time { return now },
		Window:            time.Minute,
		MaxCountPerWindow: 3,
		MaxDedupeEntries:  3,
	}))
	if err != nil {
		t.Fatal(err)
	}

	event := validSecurityEventFixture()
	event.Count = 2
	if rec := serveSecurityEvent(handler, marshalSecurityEvent(t, event), "application/json"); rec.Code != http.StatusNoContent {
		t.Fatalf("first status = %d; body=%q", rec.Code, rec.Body.String())
	}

	// Count is excluded from the dedupe identity: a replay cannot vary it to
	// consume or persist the same correlated classification twice.
	replay := event
	replay.Count = 1
	if rec := serveSecurityEvent(handler, marshalSecurityEvent(t, replay), "application/json"); rec.Code != http.StatusNoContent {
		t.Fatalf("replay status = %d; body=%q", rec.Code, rec.Body.String())
	}
	if got := len(store.snapshot()); got != 1 {
		t.Fatalf("stored events after replay = %d, want 1", got)
	}

	limited := event
	limited.RequestID = "request-fedcba0987654321"
	if rec := serveSecurityEvent(handler, marshalSecurityEvent(t, limited), "application/json"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited status = %d, want 429; body=%q", rec.Code, rec.Body.String())
	}
	if got := len(store.snapshot()); got != 1 {
		t.Fatalf("rate-limited event reached storage; stored=%d", got)
	}

	now = now.Add(time.Minute)
	if rec := serveSecurityEvent(handler, marshalSecurityEvent(t, limited), "application/json"); rec.Code != http.StatusNoContent {
		t.Fatalf("next-window status = %d; body=%q", rec.Code, rec.Body.String())
	}
	if got := len(store.snapshot()); got != 2 {
		t.Fatalf("stored events after window reset = %d, want 2", got)
	}
}

func TestSecurityEventHandlerDeduplicatesConcurrentReplay(t *testing.T) {
	store := &recordingSecurityEventStore{}
	handler, err := NewSecurityEventHandler(store, securityEventTestOptions(SecurityEventHandlerOptions{
		MaxCountPerWindow: 32,
		MaxDedupeEntries:  32,
	}))
	if err != nil {
		t.Fatal(err)
	}
	body := marshalSecurityEvent(t, validSecurityEventFixture())

	const workers = 24
	statuses := make(chan int, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses <- serveSecurityEvent(handler, body, "application/json").Code
		}()
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusNoContent {
			t.Fatalf("concurrent replay status = %d, want 204", status)
		}
	}
	if got := len(store.snapshot()); got != 1 {
		t.Fatalf("concurrent replay stored %d events, want 1", got)
	}
}

func TestSecurityEventStorageFailureIsGenericAndRetryable(t *testing.T) {
	store := &recordingSecurityEventStore{}
	store.setError(errors.New("bearer-secret authorization-header subject-value"))
	handler, err := NewSecurityEventHandler(store, securityEventTestOptions(SecurityEventHandlerOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	body := marshalSecurityEvent(t, validSecurityEventFixture())

	rec := serveSecurityEvent(handler, body, "application/json")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	for _, forbidden := range [][]byte{[]byte("bearer-secret"), []byte("authorization-header"), []byte("subject-value")} {
		if bytes.Contains(rec.Body.Bytes(), forbidden) {
			t.Fatalf("storage error leaked %q", forbidden)
		}
	}

	store.setError(nil)
	if retry := serveSecurityEvent(handler, body, "application/json"); retry.Code != http.StatusNoContent {
		t.Fatalf("retry status = %d; body=%q", retry.Code, retry.Body.String())
	}
	if got := len(store.snapshot()); got != 1 {
		t.Fatalf("retry stored %d events, want 1", got)
	}
}

func TestSecurityEventHandlerRequiresSignedGatewayEnvelope(t *testing.T) {
	f := newPrincipalFixture(t)
	store := &recordingSecurityEventStore{}
	handler, err := NewSecurityEventHandler(store, SecurityEventHandlerOptions{
		GatewayVerifier: f.verifier,
	})
	if err != nil {
		t.Fatal(err)
	}
	event := validSecurityEventFixture()
	event.RequestID = "req-security-handler"
	body := marshalSecurityEvent(t, event)
	claims := f.gatewayEventClaims("security-handler", body)
	assertion := signPrincipalAssertion(t, f.private, "active", map[string]any{"typ": "pulse.security_event.v1"}, claims)

	serve := func(candidate []byte, token, requestID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, SecurityEventRoutePath, bytes.NewReader(candidate))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("X-Pulse-Gateway-Assertion", token)
		}
		if requestID != "" {
			req.Header.Set("X-Pulse-Request-ID", requestID)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := serve(body, "", event.RequestID); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing assertion status = %d", rec.Code)
	}
	if rec := serve(body, assertion, "req-other"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong request ID status = %d", rec.Code)
	}
	tamperedEvent := event
	tamperedEvent.Count = 2
	if rec := serve(marshalSecurityEvent(t, tamperedEvent), assertion, event.RequestID); rec.Code != http.StatusUnauthorized {
		t.Fatalf("tampered body status = %d", rec.Code)
	}
	if rec := serve(body, assertion, event.RequestID); rec.Code != http.StatusNoContent {
		t.Fatalf("valid gateway event status = %d body=%q", rec.Code, rec.Body.String())
	}
	if rec := serve(body, assertion, event.RequestID); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed gateway event status = %d", rec.Code)
	}
	if got := len(store.snapshot()); got != 1 {
		t.Fatalf("stored events = %d, want exactly 1", got)
	}
}

func TestSecurityEventConcurrentDuplicateWaitsAndRetriesFailedAppend(t *testing.T) {
	store := &failingFirstSecurityEventStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	handler, err := NewSecurityEventHandler(store, securityEventTestOptions(SecurityEventHandlerOptions{
		MaxCountPerWindow: 2,
		MaxDedupeEntries:  2,
	}))
	if err != nil {
		t.Fatal(err)
	}
	body := marshalSecurityEvent(t, validSecurityEventFixture())
	firstStatus := make(chan int, 1)
	go func() {
		firstStatus <- serveSecurityEvent(handler, body, "application/json").Code
	}()
	<-store.started

	duplicateStatus := make(chan int, 1)
	go func() {
		duplicateStatus <- serveSecurityEvent(handler, body, "application/json").Code
	}()
	select {
	case status := <-duplicateStatus:
		t.Fatalf("duplicate returned %d before the first append completed", status)
	case <-time.After(25 * time.Millisecond):
	}

	close(store.release)
	if status := <-firstStatus; status != http.StatusServiceUnavailable {
		t.Fatalf("first status = %d, want 503", status)
	}
	if status := <-duplicateStatus; status != http.StatusNoContent {
		t.Fatalf("duplicate retry status = %d, want 204", status)
	}
	calls, events := store.snapshot()
	if calls != 2 || len(events) != 1 {
		t.Fatalf("storage calls=%d events=%d, want 2 calls and 1 durable event", calls, len(events))
	}
}

func TestSecurityEventHandlerOptionsKeepAdmissionStateBounded(t *testing.T) {
	store := &recordingSecurityEventStore{}
	tests := []struct {
		name    string
		options SecurityEventHandlerOptions
	}{
		{name: "negative window", options: SecurityEventHandlerOptions{Window: -time.Second}},
		{name: "window hard cap", options: SecurityEventHandlerOptions{Window: SecurityEventMaxRateWindow + time.Nanosecond}},
		{name: "rate hard cap", options: SecurityEventHandlerOptions{MaxCountPerWindow: SecurityEventMaxRateCountPerWindow + 1}},
		{name: "dedupe hard cap", options: SecurityEventHandlerOptions{MaxDedupeEntries: SecurityEventMaxTrackedDedupeKeys + 1}},
		{name: "dedupe covers admitted events", options: SecurityEventHandlerOptions{MaxCountPerWindow: 2, MaxDedupeEntries: 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewSecurityEventHandler(store, securityEventTestOptions(tc.options)); err == nil {
				t.Fatal("unbounded or incomplete admission options were accepted")
			}
		})
	}
	if _, err := NewSecurityEventHandler(nil, securityEventTestOptions(SecurityEventHandlerOptions{})); err == nil {
		t.Fatal("nil storage was accepted")
	}
	if _, err := NewSecurityEventHandler(store, SecurityEventHandlerOptions{}); err == nil {
		t.Fatal("nil gateway verifier was accepted")
	}
}

type recordingSecurityEventRegistrar struct {
	method  string
	pattern string
	handler http.Handler
}

func (r *recordingSecurityEventRegistrar) Method(method, pattern string, handler http.Handler) {
	r.method = method
	r.pattern = pattern
	r.handler = handler
}

func TestRegisterSecurityEventRouteComposesCallerWrapper(t *testing.T) {
	registrar := &recordingSecurityEventRegistrar{}
	base := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	callerWrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Caller-Wrapper", "present")
		base.ServeHTTP(w, r)
	})

	RegisterSecurityEventRoute(registrar, callerWrapped)

	if registrar.method != http.MethodPost || registrar.pattern != SecurityEventRoutePath {
		t.Fatalf("registered %s %s, want POST %s", registrar.method, registrar.pattern, SecurityEventRoutePath)
	}
	if registrar.handler == nil {
		t.Fatal("route handler was not registered")
	}
	rec := httptest.NewRecorder()
	registrar.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, SecurityEventRoutePath, nil))
	if rec.Code != http.StatusAccepted || rec.Header().Get("X-Caller-Wrapper") != "present" {
		t.Fatalf("caller wrapper was not preserved: status=%d headers=%v", rec.Code, rec.Header())
	}
}

func validSecurityEventFixture() SecurityEvent {
	return SecurityEvent{
		EventType:   SecurityEventTypeAuthenticationDenied,
		ReasonCode:  SecurityEventReasonExpiredCredential,
		MethodClass: SecurityEventMethodWrite,
		PathClass:   SecurityEventPathMCP,
		RequestID:   "request-1234567890abcdef",
		Count:       1,
	}
}

func marshalSecurityEvent(t *testing.T, event SecurityEvent) []byte {
	t.Helper()
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal security event: %v", err)
	}
	return body
}

func serveSecurityEvent(handler http.Handler, body []byte, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, SecurityEventRoutePath, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	requestID := "request-1234567890abcdef"
	var event struct {
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal(body, &event) == nil && event.RequestID != "" {
		requestID = event.RequestID
	}
	req.Header.Set("X-Pulse-Gateway-Assertion", "test-gateway-assertion")
	req.Header.Set("X-Pulse-Request-ID", requestID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
