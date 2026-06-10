package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompleteCallsMessagesAPI(t *testing.T) {
	var capturedBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("missing x-api-key header")
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Errorf("missing anthropic-version header")
		}
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
            "id": "msg_01",
            "type": "message",
            "role": "assistant",
            "content": [{"type": "text", "text": "hello back"}],
            "model": "claude-opus-4-6",
            "stop_reason": "end_turn",
            "usage": {"input_tokens": 10, "output_tokens": 5}
        }`))
	}))
	defer srv.Close()

	c := NewWithBaseURL("test-key", srv.URL)
	resp, err := c.Complete(context.Background(), CompleteRequest{
		Model:    "claude-opus-4-6",
		System:   "You are the assistant.",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	if resp.Text != "hello back" {
		t.Errorf("expected text 'hello back', got %q", resp.Text)
	}
	if resp.InputTokens != 10 || resp.OutputTokens != 5 {
		t.Errorf("unexpected token counts: %+v", resp)
	}
	if capturedBody["model"] != "claude-opus-4-6" {
		t.Errorf("wrong model: %v", capturedBody["model"])
	}
	sys, _ := capturedBody["system"].(string)
	if !strings.Contains(sys, "assistant") {
		t.Errorf("system prompt missing: %q", sys)
	}
}

func TestCompleteReturnsErrorOn4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"type":"error","error":{"type":"invalid_request","message":"bad"}}`))
	}))
	defer srv.Close()

	c := NewWithBaseURL("test-key", srv.URL)
	_, err := c.Complete(context.Background(), CompleteRequest{
		Model:    "claude-opus-4-6",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatalf("expected error on 400, got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should include status code 400: %q", err)
	}
	if !strings.Contains(err.Error(), "invalid_request") {
		t.Errorf("error should include body details: %q", err)
	}
}

func TestNewWithBaseURLTrailingSlash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Errorf("expected path /messages, got %q (double-slash likely)", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	c := NewWithBaseURL("k", srv.URL+"/")
	_, err := c.Complete(context.Background(), CompleteRequest{
		Model:    "claude-opus-4-6",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
}
