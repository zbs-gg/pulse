package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nkkmnk/pulse/internal/model"
)

func TestClientPostsMessagesToResolvedAnthropicEndpoint(t *testing.T) {
	var gotKey, gotVersion, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"claude-sonnet-4-6",
			"content":[{"type":"text","text":"hello back"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":5,"output_tokens":3}
		}`))
	}))
	defer server.Close()

	c := New()
	resp, err := c.Chat(context.Background(), model.ChatRequest{
		System:    "be brief",
		Messages:  []model.Message{{Role: "user", Content: "hello"}},
		MaxTokens: 256,
	}, model.ResolvedModel{
		Provider: model.ProviderAnthropic,
		Model:    "claude-sonnet-4-6",
		BaseURL:  server.URL + "/v1",
		APIKey:   "ant_test_key",
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotKey != "ant_test_key" {
		t.Fatalf("x-api-key = %q", gotKey)
	}
	if gotVersion == "" {
		t.Fatalf("anthropic-version header missing")
	}
	if resp.Text != "hello back" || resp.InputTokens != 5 || resp.OutputTokens != 3 || resp.StopReason != "end_turn" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestClientRequiresAPIKey(t *testing.T) {
	c := New()
	_, err := c.Chat(context.Background(), model.ChatRequest{
		Messages: []model.Message{{Role: "user", Content: "hello"}},
	}, model.ResolvedModel{
		Provider: model.ProviderAnthropic,
		Model:    "claude-sonnet-4-6",
		BaseURL:  "https://api.anthropic.com/v1",
		APIKey:   "",
	})
	if err == nil {
		t.Fatal("expected error for missing API key")
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
		Provider: model.ProviderAnthropic,
		Model:    "claude-sonnet-4-6",
		BaseURL:  server.URL + "/v1",
		APIKey:   "ant_test_key",
	})
	if err == nil {
		t.Fatal("expected redirect error")
	}
}

func TestClientPropagates4xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer server.Close()

	c := New()
	_, err := c.Chat(context.Background(), model.ChatRequest{
		Messages: []model.Message{{Role: "user", Content: "hello"}},
	}, model.ResolvedModel{
		Provider: model.ProviderAnthropic,
		Model:    "claude-sonnet-4-6",
		BaseURL:  server.URL + "/v1",
		APIKey:   "wrong",
	})
	if err == nil {
		t.Fatal("expected 401 to surface")
	}
}
