package claude

import (
	"context"
	"testing"

	"github.com/nkkmnk/pulse/internal/model"
)

type fakeProvider struct {
	calls    int
	gotReq   model.ChatRequest
	gotModel model.ResolvedModel
	respText string
	respIn   int
	respOut  int
	respStop string
}

func (f *fakeProvider) Chat(ctx context.Context, req model.ChatRequest, resolved model.ResolvedModel) (*model.ChatResponse, error) {
	f.calls++
	f.gotReq = req
	f.gotModel = resolved
	return &model.ChatResponse{
		Text:         f.respText,
		Model:        resolved.Model,
		Provider:     resolved.Provider,
		InputTokens:  f.respIn,
		OutputTokens: f.respOut,
		StopReason:   f.respStop,
	}, nil
}

func (f *fakeProvider) Health(ctx context.Context, resolved model.ResolvedModel) error {
	return nil
}

func (f *fakeProvider) Capabilities() model.Capabilities {
	return model.Capabilities{}
}

func newTestRouter(t *testing.T, alias, providerName string) (*model.Router, *fakeProvider) {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "ant_test_key")
	t.Setenv("DO_INFERENCE_TOKEN", "dop_v1_test")
	reg, err := model.NewRegistry(model.DefaultRegistryConfig())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	fake := &fakeProvider{respText: "ok", respIn: 4, respOut: 2, respStop: "end_turn"}
	router := model.NewRouter(reg, map[string]model.Provider{
		providerName: fake,
		// other providers exist but won't be hit for this alias
		model.ProviderDOInference:  fake,
		model.ProviderOpenAICompat: fake,
		model.ProviderAnthropic:    fake,
	})
	_ = alias
	return router, fake
}

func TestRouterAdapterRoutesViaConfiguredAlias(t *testing.T) {
	router, fake := newTestRouter(t, "anthropic/sonnet", model.ProviderAnthropic)
	a := NewRouterAdapter(router, "anthropic/sonnet")
	resp, err := a.Complete(context.Background(), CompleteRequest{
		System:    "be brief",
		Messages:  []Message{{Role: "user", Content: "hi"}},
		MaxTokens: 256,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("provider calls = %d", fake.calls)
	}
	if fake.gotReq.Alias != "anthropic/sonnet" {
		t.Fatalf("alias passed to router = %q", fake.gotReq.Alias)
	}
	if fake.gotReq.System != "be brief" {
		t.Fatalf("system = %q", fake.gotReq.System)
	}
	if len(fake.gotReq.Messages) != 1 || fake.gotReq.Messages[0].Content != "hi" {
		t.Fatalf("messages = %+v", fake.gotReq.Messages)
	}
	if fake.gotReq.MaxTokens != 256 {
		t.Fatalf("max tokens = %d", fake.gotReq.MaxTokens)
	}
	if fake.gotModel.Provider != model.ProviderAnthropic {
		t.Fatalf("resolved provider = %q", fake.gotModel.Provider)
	}
	if resp.Text != "ok" || resp.InputTokens != 4 || resp.OutputTokens != 2 || resp.StopReason != "end_turn" {
		t.Fatalf("response = %+v", resp)
	}
	if resp.Model != "claude-sonnet-4-6" {
		t.Fatalf("response model = %q", resp.Model)
	}
}

func TestRouterAdapterIgnoresRequestModelOverride(t *testing.T) {
	router, fake := newTestRouter(t, "anthropic/sonnet", model.ProviderAnthropic)
	a := NewRouterAdapter(router, "anthropic/sonnet")
	_, err := a.Complete(context.Background(), CompleteRequest{
		Model:    "claude-opus-4-7",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if fake.gotModel.Model != "claude-sonnet-4-6" {
		t.Fatalf("adapter must not honor req.Model override; got %q", fake.gotModel.Model)
	}
}

func TestRouterAdapterPropagatesUnknownAliasError(t *testing.T) {
	router, _ := newTestRouter(t, "anthropic/sonnet", model.ProviderAnthropic)
	a := NewRouterAdapter(router, "does/not/exist")
	_, err := a.Complete(context.Background(), CompleteRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error for unknown alias")
	}
}
