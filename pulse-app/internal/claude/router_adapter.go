package claude

import (
	"context"
	"fmt"

	"github.com/nkkmnk/pulse/internal/model"
)

// RouterAdapter adapts the model.Router (multi-provider layer) to the
// legacy ClaudeAPI surface used by the server. Existing call sites can
// keep posting CompleteRequest values; routing/policy live behind the
// router.
//
// Notes for callers:
//   - The adapter ignores CompleteRequest.Model. Model selection is
//     driven by the alias passed to NewRouterAdapter, so callers cannot
//     accidentally bypass policy by setting Model to a raw vendor ID.
//   - Errors from the router (unknown alias, policy violation, provider
//     unavailable) propagate as-is.
type RouterAdapter struct {
	router *model.Router
	alias  string
}

func NewRouterAdapter(router *model.Router, alias string) *RouterAdapter {
	return &RouterAdapter{router: router, alias: alias}
}

func (a *RouterAdapter) Complete(ctx context.Context, req CompleteRequest) (*CompleteResponse, error) {
	if a == nil || a.router == nil {
		return nil, fmt.Errorf("claude.RouterAdapter: router is nil")
	}
	messages := make([]model.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		messages = append(messages, model.Message{Role: m.Role, Content: m.Content})
	}
	resp, err := a.router.Chat(ctx, model.ChatRequest{
		Alias:     a.alias,
		System:    req.System,
		Messages:  messages,
		MaxTokens: req.MaxTokens,
	})
	if err != nil {
		return nil, err
	}
	return &CompleteResponse{
		Text:         resp.Text,
		Model:        resp.Model,
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
		StopReason:   resp.StopReason,
	}, nil
}
