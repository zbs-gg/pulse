package model

import (
	"context"
	"fmt"
)

// Router resolves model aliases, enforces runtime policy, and dispatches calls.
type Router struct {
	registry  *Registry
	providers map[string]Provider
}

func NewRouter(registry *Registry, providers map[string]Provider) *Router {
	if providers == nil {
		providers = map[string]Provider{}
	}
	return &Router{registry: registry, providers: providers}
}

func (r *Router) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if r == nil || r.registry == nil {
		return nil, fmt.Errorf("router registry: %w", ErrProviderUnavailable)
	}
	resolved, err := r.registry.Resolve(req.Alias)
	if err != nil {
		return nil, err
	}
	if req.ProviderOverride != "" && req.ProviderOverride != resolved.Provider {
		return nil, fmt.Errorf("provider override forbidden for %s: %w", resolved.Alias, ErrPolicyViolation)
	}
	provider := r.providers[resolved.Provider]
	if provider == nil {
		return nil, fmt.Errorf("provider %s: %w", resolved.Provider, ErrProviderUnavailable)
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = resolved.MaxTokensDefault
	}
	return provider.Chat(ctx, req, resolved)
}
