package doinference

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nkkmnk/pulse/internal/model"
)

func TestClientPostsChatCompletionToResolvedDOBaseURL(t *testing.T) {
	var gotAuth string
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"openai-gpt-5.4-pro",
			"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":1}
		}`))
	}))
	defer server.Close()

	c := New()
	resp, err := c.Chat(context.Background(), model.ChatRequest{
		Messages: []model.Message{{Role: "user", Content: "hello"}},
	}, model.ResolvedModel{
		Provider: model.ProviderDOInference,
		Model:    "openai-gpt-5.4-pro",
		BaseURL:  server.URL + "/v1",
		APIKey:   "dop_v1_test",
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer dop_v1_test" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if resp.Text != "ok" || resp.InputTokens != 3 || resp.OutputTokens != 1 {
		t.Fatalf("response = %+v", resp)
	}
}

func TestClientRejectsCrossHostRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://api.openai.com/v1/chat/completions", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	c := New()
	_, err := c.Chat(context.Background(), model.ChatRequest{
		Messages: []model.Message{{Role: "user", Content: "hello"}},
	}, model.ResolvedModel{
		Provider: model.ProviderDOInference,
		Model:    "openai-gpt-5.4-pro",
		BaseURL:  server.URL + "/v1",
		APIKey:   "dop_v1_test",
	})
	if err == nil {
		t.Fatal("expected redirect error")
	}
}
