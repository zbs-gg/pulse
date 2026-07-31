package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/claude"
	"github.com/nkkmnk/pulse/internal/contextquery"
	"github.com/nkkmnk/pulse/internal/outbox"
	"github.com/nkkmnk/pulse/internal/prompt"
	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
)

func newTestOutbox(t *testing.T) (*outbox.Outbox, func()) {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ob := outbox.New(s.DB(), 30*time.Second)
	return ob, func() { s.Close() }
}

func TestHealthRequiresAuth(t *testing.T) {
	srv, err := New(Config{IPCSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without key, got %d", resp.StatusCode)
	}
}

func TestHealthReturnsOkWithAuth(t *testing.T) {
	srv, err := New(Config{IPCSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/health", nil)
	req.Header.Set("X-Pulse-Key", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestHealthBindsTheSupervisorStartupNonce(t *testing.T) {
	nonce := strings.Repeat("a", 64)
	srv, err := New(Config{IPCSecret: "secret", StartupNonce: nonce})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/health", nil)
	req.Header.Set("X-Pulse-Key", "secret")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body healthResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.StartupNonce != nonce {
		t.Fatalf("startup nonce=%q, want exact supervisor nonce", body.StartupNonce)
	}
	if _, err := New(Config{IPCSecret: "secret", StartupNonce: "not-a-nonce"}); err == nil {
		t.Fatal("invalid startup nonce accepted")
	}
}

func TestHealthRejectsWrongKey(t *testing.T) {
	srv, err := New(Config{IPCSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/health", nil)
	req.Header.Set("X-Pulse-Key", "wrong")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong key, got %d", resp.StatusCode)
	}
}

func TestNewRejectsEmptySecret(t *testing.T) {
	_, err := New(Config{})
	if err == nil {
		t.Fatal("expected error for empty IPCSecret, got nil")
	}
}

func TestOutboxListReturnsPending(t *testing.T) {
	ob, cleanup := newTestOutbox(t)
	defer cleanup()

	id, err := ob.Enqueue(outbox.Message{ChatID: 1, Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}

	srv, err := New(Config{IPCSecret: "secret", Outbox: ob})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/outbox?limit=5", nil)
	req.Header.Set("X-Pulse-Key", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var rows []outboxRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 record, got %d", len(rows))
	}
	if rows[0].ID != id {
		t.Errorf("expected id %d, got %d", id, rows[0].ID)
	}
	if rows[0].Text != "hi" {
		t.Errorf("expected text 'hi', got %q", rows[0].Text)
	}
}

func TestOutboxAckMarksSent(t *testing.T) {
	ob, cleanup := newTestOutbox(t)
	defer cleanup()

	id, err := ob.Enqueue(outbox.Message{ChatID: 1, Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ob.Claim(10); err != nil {
		t.Fatal(err)
	}

	srv, err := New(Config{IPCSecret: "secret", Outbox: ob})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body, _ := json.Marshal(map[string]interface{}{"id": id, "success": true})
	req, _ := http.NewRequest("POST", ts.URL+"/outbox/ack", bytes.NewReader(body))
	req.Header.Set("X-Pulse-Key", "secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
}

func TestOutboxListEmptyReturnsEmptyArray(t *testing.T) {
	ob, cleanup := newTestOutbox(t)
	defer cleanup()

	srv, err := New(Config{IPCSecret: "secret", Outbox: ob})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/outbox", nil)
	req.Header.Set("X-Pulse-Key", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	// Must render as "[]" not "null" — bridge depends on this.
	if strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("expected []\\n, got %q", string(body))
	}
}

func TestOutboxListInvalidLimitFallsBackToDefault(t *testing.T) {
	ob, cleanup := newTestOutbox(t)
	defer cleanup()

	srv, err := New(Config{IPCSecret: "secret", Outbox: ob})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Try several malformed values — all should return 200 (no crash).
	for _, limit := range []string{"abc", "0", "101", "-5"} {
		req, _ := http.NewRequest("GET", ts.URL+"/outbox?limit="+limit, nil)
		req.Header.Set("X-Pulse-Key", "secret")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("limit=%s: %v", limit, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("limit=%s: expected 200, got %d", limit, resp.StatusCode)
		}
	}
}

func TestOutboxAckRejectsMalformedJSON(t *testing.T) {
	ob, cleanup := newTestOutbox(t)
	defer cleanup()

	srv, err := New(Config{IPCSecret: "secret", Outbox: ob})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/outbox/ack", strings.NewReader("not json"))
	req.Header.Set("X-Pulse-Key", "secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestOutboxAckRejectsMissingID(t *testing.T) {
	ob, cleanup := newTestOutbox(t)
	defer cleanup()

	srv, err := New(Config{IPCSecret: "secret", Outbox: ob})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/outbox/ack", strings.NewReader(`{"success":true}`))
	req.Header.Set("X-Pulse-Key", "secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

type fakeClaude struct {
	reply string
}

func (f *fakeClaude) Complete(ctx context.Context, req claude.CompleteRequest) (*claude.CompleteResponse, error) {
	return &claude.CompleteResponse{Text: f.reply, InputTokens: 100, OutputTokens: 20}, nil
}

func TestMsgHandlerEnqueuesReply(t *testing.T) {
	ob, cleanup := newTestOutbox(t)
	defer cleanup()
	b, err := prompt.NewBuilder("../prompt/testdata/soul.md")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{
		IPCSecret:    "secret",
		Outbox:       ob,
		Builder:      b,
		Claude:       &fakeClaude{reply: "hey"},
		DefaultModel: "claude-opus-4-6",
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := strings.NewReader(`{
		"chat_id": 42,
		"message_id": 100,
		"text": "привет",
		"timestamp": "2026-04-14T10:00:00Z"
	}`)
	req, _ := http.NewRequest("POST", ts.URL+"/msg", body)
	req.Header.Set("X-Pulse-Key", "secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d", resp.StatusCode)
	}

	records, err := ob.Claim(5)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 message in outbox, got %d", len(records))
	}
	if records[0].Text != "hey" {
		t.Errorf("wrong text: %q", records[0].Text)
	}
	if records[0].ChatID != 42 {
		t.Errorf("wrong chat_id: %d", records[0].ChatID)
	}
	if records[0].ReplyTo == nil || *records[0].ReplyTo != 100 {
		t.Errorf("expected reply_to=100, got %v", records[0].ReplyTo)
	}
}

type fakeClaudeErr struct {
	err error
}

func (f *fakeClaudeErr) Complete(ctx context.Context, req claude.CompleteRequest) (*claude.CompleteResponse, error) {
	return nil, f.err
}

type fakeClaudeEmpty struct{}

func (f *fakeClaudeEmpty) Complete(ctx context.Context, req claude.CompleteRequest) (*claude.CompleteResponse, error) {
	return &claude.CompleteResponse{Text: "   "}, nil
}

func newMsgTestServer(t *testing.T, c ClaudeAPI) (*outbox.Outbox, *httptest.Server, func()) {
	t.Helper()
	ob, cleanup := newTestOutbox(t)
	b, err := prompt.NewBuilder("../prompt/testdata/soul.md")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{
		IPCSecret:    "secret",
		Outbox:       ob,
		Builder:      b,
		Claude:       c,
		DefaultModel: "claude-opus-4-6",
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	return ob, ts, func() {
		ts.Close()
		cleanup()
	}
}

func postMsg(t *testing.T, ts *httptest.Server, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", ts.URL+"/msg", strings.NewReader(body))
	req.Header.Set("X-Pulse-Key", "secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestMsgRejectsBadJSON(t *testing.T) {
	_, ts, cleanup := newMsgTestServer(t, &fakeClaude{reply: "hi"})
	defer cleanup()

	resp := postMsg(t, ts, "not json")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestMsgRejectsMissingChatID(t *testing.T) {
	_, ts, cleanup := newMsgTestServer(t, &fakeClaude{reply: "hi"})
	defer cleanup()

	resp := postMsg(t, ts, `{"message_id":1,"text":"hi"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestMsgRejectsEmptyText(t *testing.T) {
	_, ts, cleanup := newMsgTestServer(t, &fakeClaude{reply: "hi"})
	defer cleanup()

	resp := postMsg(t, ts, `{"chat_id":42,"message_id":1,"text":""}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestMsgReturnsBadGatewayOnClaudeError(t *testing.T) {
	_, ts, cleanup := newMsgTestServer(t, &fakeClaudeErr{err: errors.New("upstream broke")})
	defer cleanup()

	resp := postMsg(t, ts, `{"chat_id":42,"message_id":1,"text":"hi"}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", resp.StatusCode)
	}
}

func TestMsgReturnsBadGatewayOnEmptyReply(t *testing.T) {
	_, ts, cleanup := newMsgTestServer(t, &fakeClaudeEmpty{})
	defer cleanup()

	resp := postMsg(t, ts, `{"chat_id":42,"message_id":1,"text":"hi"}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", resp.StatusCode)
	}
}

func TestContextQueryRouteRequiresAuth(t *testing.T) {
	outbox, cleanup := newTestOutbox(t)
	defer cleanup()
	srv, err := New(Config{Outbox: outbox, IPCSecret: "secret", ContextQuery: fakeContextQuery{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, err := http.Post(ts.URL+"/context/query", "application/json", strings.NewReader(`{"query":"x"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestContextQueryRouteReturnsTypedResult(t *testing.T) {
	outbox, cleanup := newTestOutbox(t)
	defer cleanup()
	srv, err := New(Config{Outbox: outbox, IPCSecret: "secret", ContextQuery: fakeContextQuery{
		result: &contextquery.ContextResult{SchemaVersion: contextquery.SchemaVersion, Query: "x", ModeUsed: "empathic", Scope: "user"},
	}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/context/query", strings.NewReader(`{"query":"x","scope":"user"}`))
	req.Header.Set("X-Pulse-Key", "secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	var got contextquery.ContextResult
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SchemaVersion != contextquery.SchemaVersion || got.Query != "x" {
		t.Fatalf("got = %#v", got)
	}
}

func TestRetrieveEmptyStoreReturnsStableEventIDsArray(t *testing.T) {
	vault, err := store.Open(filepath.Join(t.TempDir(), "empty-retrieval.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	engine := retrieve.New(retrieve.Config{Store: vault, Embedder: productTestEmbedder{}})
	if err := engine.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{IPCSecret: "secret", Store: vault, Retrieval: engine})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/retrieve", strings.NewReader(
		`{"query":"managed readiness","mode":"factual","top_k":1}`,
	))
	req.Header.Set("X-Pulse-Key", "secret")
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got := string(body["event_ids"]); got != "[]" {
		t.Fatalf("event_ids = %s, want []", got)
	}
}

type fakeContextQuery struct {
	result *contextquery.ContextResult
}

func (f fakeContextQuery) Query(_ context.Context, req contextquery.ContextQueryRequest) (*contextquery.ContextResult, error) {
	if f.result != nil {
		return f.result, nil
	}
	return &contextquery.ContextResult{SchemaVersion: contextquery.SchemaVersion, Query: req.Query, Scope: req.Scope}, nil
}
func TestIngestRouteWired(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	srv, err := New(Config{
		IPCSecret: "secret",
		Store:     s,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	body := strings.NewReader(`{"observations":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/ingest", body)
	req.Header.Set("X-Pulse-Key", "secret")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
}
