package prompt

import (
	"strings"
	"testing"
)

func TestBuildIncludesBaseAndSoul(t *testing.T) {
	b, err := NewBuilder("testdata/soul.md")
	if err != nil {
		t.Fatal(err)
	}
	out, err := b.Build(BuildInput{
		UserMessage: "привет",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.System, "You are the assistant") {
		t.Errorf("system prompt missing base 'You are the assistant': %q", out.System)
	}
	if !strings.Contains(out.System, "Absolute mode") {
		t.Errorf("system prompt missing SOUL.md content")
	}
	if len(out.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out.Messages))
	}
	if out.Messages[0].Role != "user" || out.Messages[0].Content != "привет" {
		t.Errorf("wrong user message: %+v", out.Messages[0])
	}
}

func TestBuildIncludesRecentHistory(t *testing.T) {
	b, err := NewBuilder("testdata/soul.md")
	if err != nil {
		t.Fatal(err)
	}
	out, err := b.Build(BuildInput{
		UserMessage: "и?",
		Recent: []Turn{
			{Role: "user", Text: "как ты"},
			{Role: "assistant", Text: "нормально"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 3 {
		t.Fatalf("expected 3 messages (2 history + 1 current), got %d", len(out.Messages))
	}
	if out.Messages[0].Content != "как ты" {
		t.Errorf("wrong first msg: %q", out.Messages[0].Content)
	}
	if out.Messages[2].Content != "и?" {
		t.Errorf("current user msg not last: %q", out.Messages[2].Content)
	}
}

func TestBuildRejectsEmptyUserMessage(t *testing.T) {
	b, err := NewBuilder("testdata/soul.md")
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Build(BuildInput{})
	if err == nil {
		t.Fatal("expected error for empty UserMessage, got nil")
	}
}

func TestNewBuilderMissingSoul(t *testing.T) {
	_, err := NewBuilder("testdata/does-not-exist.md")
	if err == nil {
		t.Fatal("expected error for missing soul file, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist.md") {
		t.Errorf("error should name the missing path, got: %v", err)
	}
}
