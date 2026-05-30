package model

import (
	"context"
	"errors"
	"testing"
)

type fakeProvider struct {
	name     string
	resolved ResolvedModel
	calls    int
}

func (f *fakeProvider) Chat(ctx context.Context, req ChatRequest, resolved ResolvedModel) (*ChatResponse, error) {
	f.calls++
	f.resolved = resolved
	return &ChatResponse{Text: "ok", Provider: f.name, Model: resolved.Model}, nil
}

func (f *fakeProvider) Health(ctx context.Context, resolved ResolvedModel) error { return nil }
func (f *fakeProvider) Capabilities() Capabilities                               { return Capabilities{} }

func TestRouterRejectsRuntimeProviderOverrideMismatch(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "ant_test")
	reg, err := NewRegistry(DefaultRegistryConfig())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	anthropicProvider := &fakeProvider{name: ProviderAnthropic}
	localProvider := &fakeProvider{name: ProviderOpenAICompat}
	r := NewRouter(reg, map[string]Provider{
		ProviderAnthropic:    anthropicProvider,
		ProviderOpenAICompat: localProvider,
	})
	_, err = r.Chat(context.Background(), ChatRequest{
		Alias:            "anthropic/opus",
		ProviderOverride: ProviderOpenAICompat,
		Messages:         []Message{{Role: "user", Content: "hello"}},
	})
	if !errors.Is(err, ErrPolicyViolation) {
		t.Fatalf("expected ErrPolicyViolation, got %v", err)
	}
	if anthropicProvider.calls != 0 || localProvider.calls != 0 {
		t.Fatalf("providers should not be called: anthropic=%d local=%d", anthropicProvider.calls, localProvider.calls)
	}
}

func TestRouterCallsResolvedProvider(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "ant_test")
	reg, err := NewRegistry(DefaultRegistryConfig())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	provider := &fakeProvider{name: ProviderAnthropic}
	r := NewRouter(reg, map[string]Provider{ProviderAnthropic: provider})
	resp, err := r.Chat(context.Background(), ChatRequest{
		Alias:    "anthropic/opus",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Text != "ok" || provider.calls != 1 {
		t.Fatalf("provider not called correctly: resp=%+v calls=%d", resp, provider.calls)
	}
	if provider.resolved.BaseURL != "https://api.anthropic.com/v1" {
		t.Fatalf("resolved base url = %q", provider.resolved.BaseURL)
	}
	if provider.resolved.Model != "claude-opus-4-6" {
		t.Fatalf("resolved model = %q", provider.resolved.Model)
	}
}

func TestRouterFailsWhenProviderMissing(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "ant_test")
	reg, err := NewRegistry(DefaultRegistryConfig())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	r := NewRouter(reg, nil)
	_, err = r.Chat(context.Background(), ChatRequest{
		Alias:    "anthropic/opus",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("expected ErrProviderUnavailable, got %v", err)
	}
}
