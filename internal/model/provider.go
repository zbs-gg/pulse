package model

import "context"

// Provider is implemented by model backends.
type Provider interface {
	Chat(ctx context.Context, req ChatRequest, resolved ResolvedModel) (*ChatResponse, error)
	Health(ctx context.Context, resolved ResolvedModel) error
	Capabilities() Capabilities
}
