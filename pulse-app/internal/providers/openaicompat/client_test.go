package openaicompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nkkmnk/pulse/internal/model"
)

func TestClientPostsChatCompletionToLocalEndpoint(t *testing.T) {
	var gotAuth string
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"local-model",
			"choices":[{"message":{"content":"local ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":4,"completion_tokens":2}
		}`))
	}))
	defer server.Close()

	c := New()
	resp, err := c.Chat(context.Background(), model.ChatRequest{
		Messages: []model.Message{{Role: "user", Content: "hello"}},
	}, model.ResolvedModel{
		Provider: model.ProviderOpenAICompat,
		Model:    "local-model",
		BaseURL:  server.URL + "/v1",
		APIKey:   "local-key",
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer local-key" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if resp.Text != "local ok" || resp.InputTokens != 4 || resp.OutputTokens != 2 {
		t.Fatalf("response = %+v", resp)
	}
}

func TestClientAllowsMissingLocalAPIKey(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"local-model","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	c := New()
	_, err := c.Chat(context.Background(), model.ChatRequest{
		Messages: []model.Message{{Role: "user", Content: "hello"}},
	}, model.ResolvedModel{
		Provider: model.ProviderOpenAICompat,
		Model:    "local-model",
		BaseURL:  server.URL + "/v1",
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("expected no auth header, got %q", gotAuth)
	}
}
