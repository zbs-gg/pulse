package capture

import "testing"

func TestContentHashDeterministic(t *testing.T) {
	h1 := ComputeContentHash("hello", map[string]any{"k": "v"})
	h2 := ComputeContentHash("hello", map[string]any{"k": "v"})
	if h1 != h2 {
		t.Errorf("expected deterministic hash, got %s vs %s", h1, h2)
	}
}

func TestContentHashDiffersOnContent(t *testing.T) {
	h1 := ComputeContentHash("hello", nil)
	h2 := ComputeContentHash("world", nil)
	if h1 == h2 {
		t.Error("expected different hashes for different content")
	}
}

func TestContentHashDiffersOnMetadata(t *testing.T) {
	h1 := ComputeContentHash("hello", map[string]any{"k": "v1"})
	h2 := ComputeContentHash("hello", map[string]any{"k": "v2"})
	if h1 == h2 {
		t.Error("expected different hashes for different metadata")
	}
}

func TestContentHashStableAcrossMapOrder(t *testing.T) {
	m1 := map[string]any{"a": 1, "b": 2}
	m2 := map[string]any{"b": 2, "a": 1}
	if ComputeContentHash("x", m1) != ComputeContentHash("x", m2) {
		t.Error("hash should be stable across map iteration order")
	}
}
