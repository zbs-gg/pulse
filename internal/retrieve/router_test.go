package retrieve

import (
	"context"
	"testing"
)

// stubState implements EmotionState for tests without pulling the full
// UserState type from a sibling package.
type stubState struct {
	dominant      string
	dominantValue float64
	stressed      bool
	recent        int
}

func (s stubState) HasDominantEmotion(threshold float64) (bool, float64, string) {
	if s.dominantValue >= threshold {
		return true, s.dominantValue, s.dominant
	}
	return false, s.dominantValue, s.dominant
}
func (s stubState) IsBodyStressed() bool       { return s.stressed }
func (s stubState) RecentLifeEventsCount() int { return s.recent }

func TestRouter_Heuristics(t *testing.T) {
	r := NewRouter()
	cases := []struct {
		query    string
		state    EmotionState
		expected QueryMode
	}{
		// Factual lookups
		{"когда я был на конференции?", nil, ModeFactual},
		{"когда был мой первый митап?", nil, ModeFactual},
		{"сколько у меня собак?", nil, ModeFactual},
		{"what is my pet's name?", nil, ModeFactual},
		{"when did Sam start a new job?", nil, ModeFactual},
		{"list of all my hobbies", nil, ModeFactual},
		{"what has happened recently in my life", nil, ModeFactual},
		{"tell me about Alice", nil, ModeFactual},
		{"что произошло вчера?", nil, ModeFactual},
		// Chain
		{"почему я тогда злился?", nil, ModeChain},
		{"what led to the conflict with Sam?", nil, ModeChain},
		{"trace the chain of events that brought me here", nil, ModeChain},
		{"из-за чего у нас расходятся?", nil, ModeChain},
		// Borderline: factual wh-question + emotion word — factual mode
		// retrieves emotion-related events through facts; defensible.
		{"когда я был в страхе перед публикой", nil, ModeFactual},
		// Empathic from state (no keyword in query)
		{"что мне почитать?",
			stubState{dominant: "shame", dominantValue: 0.7},
			ModeEmpathic},
		{"какие у меня варианты?",
			stubState{stressed: true}, // user_state stressed → empathic even though query feels factual
			ModeEmpathic},
	}
	for _, tc := range cases {
		got := r.Classify(context.Background(), tc.query, tc.state)
		if got.Mode != tc.expected {
			t.Errorf("Classify(%q): want %s, got %s (conf=%.2f, why=%s)",
				tc.query, tc.expected, got.Mode, got.Confidence, got.Reasoning)
		}
	}
}

func TestRouter_DefaultEmpathic(t *testing.T) {
	r := NewRouter()
	d := r.Classify(context.Background(), "lorem ipsum dolor sit amet", nil)
	if d.Mode != ModeEmpathic {
		t.Errorf("expected default empathic for unrelated query, got %s", d.Mode)
	}
	if d.Confidence >= 0.6 {
		t.Errorf("expected low confidence on default branch, got %.2f", d.Confidence)
	}
}

func TestRouter_Cache(t *testing.T) {
	r := NewRouter()
	q := "когда я родился?"
	d1 := r.Classify(context.Background(), q, nil)
	d2 := r.Classify(context.Background(), q, nil)
	if d1 != d2 {
		t.Errorf("cache should return identical decision; got %+v vs %+v", d1, d2)
	}
}
