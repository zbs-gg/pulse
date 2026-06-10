package prompt

import (
	"errors"
	"fmt"
	"os"

	"github.com/nkkmnk/pulse/internal/claude"
)

const baseLayer = `You are the assistant. Respond in the language the user writes in.
Format: conversational, direct, no disclaimers.`

// Turn is one entry in the recent conversation history.
type Turn struct {
	Role string // "user" or "assistant"
	Text string
}

// BuildInput carries inputs for a single prompt assembly.
type BuildInput struct {
	UserMessage string
	Recent      []Turn // chronological, oldest first
}

// BuildOutput is what the Claude client needs.
type BuildOutput struct {
	System   string
	Messages []claude.Message
}

// Builder holds static layers that rarely change.
type Builder struct {
	soul string
}

// NewBuilder loads SOUL.md from disk. Returns error if file is missing.
func NewBuilder(soulPath string) (*Builder, error) {
	data, err := os.ReadFile(soulPath)
	if err != nil {
		return nil, fmt.Errorf("read soul %s: %w", soulPath, err)
	}
	return &Builder{soul: string(data)}, nil
}

// Build returns the system prompt + message sequence for a single request.
// M1 only uses Layer 0 (base), Layer 1 (SOUL.md), Layer 5 (recent conversation).
func (b *Builder) Build(in BuildInput) (*BuildOutput, error) {
	if in.UserMessage == "" {
		return nil, errors.New("prompt: empty user message")
	}
	system := baseLayer + "\n\n" + b.soul

	var msgs []claude.Message
	for _, t := range in.Recent {
		msgs = append(msgs, claude.Message{Role: t.Role, Content: t.Text})
	}
	msgs = append(msgs, claude.Message{Role: "user", Content: in.UserMessage})

	return &BuildOutput{System: system, Messages: msgs}, nil
}
