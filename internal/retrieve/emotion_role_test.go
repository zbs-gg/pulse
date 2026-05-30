package retrieve

import "testing"

func TestInferEmotionRoleSameSurfaceEmotionDifferentIntent(t *testing.T) {
	state := &UserState{MoodVector: map[string]float64{"shame": 0.82}}

	cases := []struct {
		name  string
		query string
		want  EmotionRole
	}{
		{
			name:  "why asks for explanation",
			query: "почему мне так стыдно?",
			want:  EmotionRoleExplain,
		},
		{
			name:  "help asks for soothing",
			query: "что поможет когда мне стыдно?",
			want:  EmotionRoleSoothe,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := InferEmotionRole(tc.query, state)
			if got.Role != tc.want {
				t.Fatalf("InferEmotionRole(%q): want %s, got %s (%s)",
					tc.query, tc.want, got.Role, got.Reasoning)
			}
			if got.StateEmotion != "shame" {
				t.Fatalf("state emotion = %q, want shame", got.StateEmotion)
			}
		})
	}
}

func TestInferEmotionRoleRelationTruthBeatsGenericExplanation(t *testing.T) {
	state := &UserState{MoodVector: map[string]float64{"anger": 0.7}}

	got := InferEmotionRole("что у меня с Алексом на самом деле?", state)
	if got.Role != EmotionRoleRelationTruth {
		t.Fatalf("want relation-truth, got %s (%s)", got.Role, got.Reasoning)
	}
}
